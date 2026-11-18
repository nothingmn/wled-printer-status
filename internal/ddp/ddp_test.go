package ddp

import (
	"errors"
	"os"
	"testing"
	"time"

	"github.com/nothingmn/wled-printer-status/internal/matrix"
	"github.com/nothingmn/wled-printer-status/internal/printer"
	"github.com/nothingmn/wled-printer-status/internal/render"
	"github.com/nothingmn/wled-printer-status/internal/wled"
)

// The operator's panel, which is what every packet count in these tests is about:
// 256 columns of 64 rows is 16,384 pixels. At 480 pixels a datagram that is 34 full
// datagrams covering 16,320 pixels and one more carrying the remaining 64, so a frame
// is 35 — the count is one out if the remainder is forgotten, which is why it is here
// as a number and asserted rather than computed at every use.
const (
	panelPixels = 256 * 64
	panelFrame  = 35
)

// recorder is the socket a byte-exact test uses instead of one. It keeps every
// datagram it is handed, which is what lets a test assert the bytes without a
// network, a device or a port.
type recorder struct {
	// packets is every datagram, in the order it was handed over, copied: the sender
	// reuses its buffer, so a recorder that kept the slice would see the last packet
	// thirty-five times.
	packets [][]byte
	// discard takes the datagram and keeps nothing, so that a measurement of
	// allocations per frame is not measuring the recorder.
	discard bool
	// failFrom is the write number — counted from one — that fails, and err is what it
	// fails with. Zero never fails.
	failFrom int
	err      error
}

// record is one write to the socket.
func (r *recorder) record(packet []byte) (int, error) {
	if r.failFrom > 0 && len(r.packets)+1 >= r.failFrom {
		return 0, r.err
	}

	if !r.discard {
		r.packets = append(r.packets, append([]byte(nil), packet...))
	}

	return len(packet), nil
}

// senderFor is a Sender with a recorder in place of its socket. It is built through
// newSender and not New, so no address is resolved and nothing is dialled.
func senderFor(t *testing.T, pixels int, opts ...Option) (*Sender, *recorder) {
	t.Helper()

	sender, err := newSender(nil, pixels, opts...)
	if err != nil {
		t.Fatalf("a sender for %d pixels was refused: %v", pixels, err)
	}

	recorder := &recorder{}
	sender.write = recorder.record

	return sender, recorder
}

// frameOf is a frame whose every channel is derived from the pixel's index, so a
// payload read back out of a packet says which pixel it came from as well as what
// colour it was, and two pixels cannot be confused for each other.
func frameOf(pixels int) []wled.RGB {
	frame := make([]wled.RGB, pixels)
	for index := range frame {
		frame[index] = wled.RGB{
			R: uint8(index >> 8),
			G: uint8(index),
			B: uint8(index >> 16),
		}
	}

	return frame
}

// header is the first ten bytes of a packet as a comparable value, so a failure
// prints as bytes rather than as a range pointer.
func header(packet []byte) [HeaderLen]byte {
	var head [HeaderLen]byte
	copy(head[:], packet)

	return head
}

// offset is a packet's channel offset, read the way the controller reads it.
func offset(packet []byte) uint32 {
	return uint32(packet[4])<<24 | uint32(packet[5])<<16 | uint32(packet[6])<<8 | uint32(packet[7])
}

// length is a packet's colour byte count, read the way the controller reads it.
func length(packet []byte) uint16 {
	return uint16(packet[8])<<8 | uint16(packet[9])
}

// TestTheHeaderOfTheFirstTheSecondAndTheLastPacket is the header read by eye: the
// flags, the sequence number, the colour type, the display id, and the two numbers
// the controller places the pixels with.
func TestTheHeaderOfTheFirstTheSecondAndTheLastPacket(t *testing.T) {
	sender, recorder := senderFor(t, panelPixels)

	if err := sender.Send(frameOf(panelPixels)); err != nil {
		t.Fatalf("a %d-pixel frame was refused: %v", panelPixels, err)
	}

	if len(recorder.packets) != panelFrame {
		t.Fatalf("the frame is %d datagrams, want %d", len(recorder.packets), panelFrame)
	}

	// Version 1 is the top two bits, the push flag is the last, and only the last
	// datagram of a frame carries it: it is what says the picture is complete.
	first := [HeaderLen]byte{0x40, 0x00, 0x0B, 0x01, 0x00, 0x00, 0x00, 0x00, 0x05, 0xA0}
	second := [HeaderLen]byte{0x40, 0x01, 0x0B, 0x01, 0x00, 0x00, 0x05, 0xA0, 0x05, 0xA0}
	last := [HeaderLen]byte{0x41, 0x22, 0x0B, 0x01, 0x00, 0x00, 0xBF, 0x40, 0x00, 0xC0}

	for _, packet := range []struct {
		name string
		at   int
		want [HeaderLen]byte
	}{
		{name: "the first", at: 0, want: first},
		{name: "the second", at: 1, want: second},
		{name: "the last", at: panelFrame - 1, want: last},
	} {
		got := header(recorder.packets[packet.at])
		if got != packet.want {
			t.Fatalf("the header of %s datagram is % X, want % X", packet.name, got, packet.want)
		}
	}
}

// TestEveryPacketCarriesItsOwnOffsetAndLength is the arithmetic across the frame: the
// offset steps by the bytes one datagram carries, the length is the datagram's own
// bytes, and the two together account for every colour byte of the display exactly
// once.
//
// The step is 1440 and not 480, and that is the trap in this protocol: the field is a
// channel offset and the controller divides it by three before it looks at its buffer,
// so 480 pixels further on is 1440. Writing the pixel count puts two thirds of the
// frame in the wrong place, and the panel shows that as garbage rather than as an
// error.
func TestEveryPacketCarriesItsOwnOffsetAndLength(t *testing.T) {
	sender, recorder := senderFor(t, panelPixels)

	if err := sender.Send(frameOf(panelPixels)); err != nil {
		t.Fatalf("a %d-pixel frame was refused: %v", panelPixels, err)
	}

	covered := 0

	for index, packet := range recorder.packets {
		first := index * MaxPixelsPerPacket
		wantPixels := min(MaxPixelsPerPacket, panelPixels-first)

		if got, want := offset(packet), uint32(first*3); got != want {
			t.Fatalf("datagram %d starts at channel %d, want %d", index, got, want)
		}

		if got, want := int(length(packet)), wantPixels*3; got != want {
			t.Fatalf("datagram %d carries %d colour bytes, want %d for %d pixels", index, got, want, wantPixels)
		}

		if got, want := len(packet), HeaderLen+wantPixels*3; got != want {
			t.Fatalf("datagram %d is %d bytes on the wire, want %d", index, got, want)
		}

		// WLED reads into a 1472-byte buffer and drops anything larger, so a datagram
		// that outgrew it would be dropped in silence.
		if len(packet) > 1472 {
			t.Fatalf("datagram %d is %d bytes, past the controller's 1472-byte buffer", index, len(packet))
		}

		covered += int(length(packet))
	}

	if want := panelPixels * 3; covered != want {
		t.Fatalf("the frame's datagrams cover %d colour bytes, want %d", covered, want)
	}

	// The last datagram is the short one: 64 pixels, not a round 480.
	if got := int(length(recorder.packets[panelFrame-1])) / 3; got != 64 {
		t.Fatalf("the last datagram carries %d pixels, want the frame's remaining 64", got)
	}
}

// TestThePixelsArriveInOrder is the payload itself: read every datagram back at the
// offset it declares and the display is the frame that was sent, pixel for pixel. A
// header that lies about where its colours go would pass every test above this one.
func TestThePixelsArriveInOrder(t *testing.T) {
	sender, recorder := senderFor(t, panelPixels)

	frame := frameOf(panelPixels)
	if err := sender.Send(frame); err != nil {
		t.Fatalf("a %d-pixel frame was refused: %v", panelPixels, err)
	}

	received := make([]wled.RGB, panelPixels)

	for _, packet := range recorder.packets {
		start := int(offset(packet)) / 3
		count := int(length(packet)) / 3

		for index := 0; index < count; index++ {
			base := HeaderLen + index*3
			received[start+index] = wled.RGB{
				R: packet[base],
				G: packet[base+1],
				B: packet[base+2],
			}
		}
	}

	for index := range frame {
		if received[index] != frame[index] {
			t.Fatalf("pixel %d arrived as %+v, want %+v", index, received[index], frame[index])
		}
	}
}

// TestTheSequenceNumberWrapsAtTwoHundredAndFiftySix is the packet counter's whole
// range: eight frames of the operator's panel is 272 datagrams, so the byte goes all
// the way round and past.
func TestTheSequenceNumberWrapsAtTwoHundredAndFiftySix(t *testing.T) {
	sender, recorder := senderFor(t, panelPixels)

	const frames = 8
	for range frames {
		if err := sender.Send(frameOf(panelPixels)); err != nil {
			t.Fatalf("frame was refused: %v", err)
		}
	}

	if got, want := len(recorder.packets), frames*panelFrame; got != want {
		t.Fatalf("%d frames are %d datagrams, want %d", frames, got, want)
	}

	for index, packet := range recorder.packets {
		if got, want := packet[1], uint8(index%256); got != want {
			t.Fatalf("datagram %d carries sequence %d, want %d", index, got, want)
		}
	}

	// Named so that the wrap is visible as a value and not only as an offset into a
	// slice: the 256th datagram is sequence 0 again.
	if got := recorder.packets[256][1]; got != 0 {
		t.Fatalf("the 256th datagram carries sequence %d, want 0", got)
	}
}

// TestAShortFrameIsNotPadded is the two edges of a frame's length: one datagram is one
// whole frame when the display fits in one, and a frame of one pixel past that is two
// datagrams with a one-pixel tail.
//
// Nothing is padded to a round packet. A datagram says where its pixels go, so the ones
// it does not name keep whatever the frame before left there — which is why a caller
// sends the whole display.
func TestAShortFrameIsNotPadded(t *testing.T) {
	for _, frame := range []struct {
		name    string
		pixels  int
		packets int
		last    int
	}{
		{name: "one pixel", pixels: 1, packets: 1, last: 1},
		{name: "one full packet", pixels: MaxPixelsPerPacket, packets: 1, last: MaxPixelsPerPacket},
		{name: "one pixel past a full packet", pixels: MaxPixelsPerPacket + 1, packets: 2, last: 1},
		{name: "a strip", pixels: 48, packets: 1, last: 48},
	} {
		t.Run(frame.name, func(t *testing.T) {
			sender, recorder := senderFor(t, panelPixels)

			if err := sender.Send(frameOf(frame.pixels)); err != nil {
				t.Fatalf("%d pixels were refused: %v", frame.pixels, err)
			}

			if len(recorder.packets) != frame.packets {
				t.Fatalf("%d pixels are %d datagrams, want %d", frame.pixels, len(recorder.packets), frame.packets)
			}

			last := recorder.packets[len(recorder.packets)-1]
			if got := int(length(last)) / 3; got != frame.last {
				t.Fatalf("the last datagram carries %d pixels, want %d", got, frame.last)
			}

			if last[0]&flagPush == 0 {
				t.Fatalf("the last datagram's flags are %02X, want the push bit set", last[0])
			}

			if got := offset(last); got != uint32((frame.pixels-frame.last)*3) {
				t.Fatalf("the last datagram starts at channel %d, want %d", got, (frame.pixels-frame.last)*3)
			}
		})
	}
}

// TestThePushFlagIsOnTheLastDatagramAndNoOther is the flag that says a picture is
// complete. Set on every datagram it asks the controller to draw thirty-five times a
// frame; absent altogether the controller draws each datagram as it lands, and a frame
// is visible half-drawn.
func TestThePushFlagIsOnTheLastDatagramAndNoOther(t *testing.T) {
	sender, recorder := senderFor(t, panelPixels)

	if err := sender.Send(frameOf(panelPixels)); err != nil {
		t.Fatalf("a %d-pixel frame was refused: %v", panelPixels, err)
	}

	pushed := 0

	for index, packet := range recorder.packets {
		set := packet[0]&flagPush != 0

		if packet[0]&0xC0 != flagVersion1 {
			t.Fatalf("datagram %d carries flags %02X, want version 1 in the top two bits", index, packet[0])
		}

		if set {
			pushed++
		}

		if want := index == len(recorder.packets)-1; set != want {
			t.Fatalf("datagram %d has push %t, want %t", index, set, want)
		}
	}

	if pushed != 1 {
		t.Fatalf("%d datagrams carry the push flag, want 1", pushed)
	}
}

// TestAnEmptyFrameAndOnePastTheDisplayAreRefused is the two frames this sender will not
// send. Both are refused before a datagram is built, so a refused frame leaves the
// socket untouched rather than half-written.
func TestAnEmptyFrameAndOnePastTheDisplayAreRefused(t *testing.T) {
	sender, recorder := senderFor(t, panelPixels)

	if err := sender.Send(nil); !errors.Is(err, ErrEmptyFrame) {
		t.Fatalf("an empty frame returns %v, want ErrEmptyFrame", err)
	}

	if err := sender.Send([]wled.RGB{}); !errors.Is(err, ErrEmptyFrame) {
		t.Fatalf("an empty slice returns %v, want ErrEmptyFrame", err)
	}

	if err := sender.Send(frameOf(panelPixels + 1)); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("a %d-pixel frame returns %v, want ErrFrameTooLarge", panelPixels+1, err)
	}

	if len(recorder.packets) != 0 {
		t.Fatalf("a refused frame sent %d datagrams, want none", len(recorder.packets))
	}

	// A sender declared for a display of nothing has no frame it could accept.
	if _, err := newSender(nil, 0); !errors.Is(err, ErrNoDisplay) {
		t.Fatalf("a sender for no pixels returns %v, want ErrNoDisplay", err)
	}

	// The display is the declared one, not the packet size: a 48-pixel strip accepts 48
	// pixels and refuses 49.
	strip, _ := senderFor(t, 48)
	if err := strip.Send(frameOf(49)); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("49 pixels for a 48-pixel display returns %v, want ErrFrameTooLarge", err)
	}
}

// TestASecondFrameReusesThePacketBuffer is the allocation claim, and it is measured
// rather than asserted: a frame of the operator's panel is 35 datagrams, and the sender
// builds all of them in one buffer it allocated when it was made.
//
// This is testing.AllocsPerRun around Send with the recorder discarding its datagrams,
// so the number is the sender's own allocations and not the test's. Zero means the packet
// buffer is reused across frames; a per-frame build would report 35 from the copies inside
// the loop. The socket call is not in the measurement — it is the recorder here — so the
// claim is about the packet building, which is the part that would grow with the frame.
func TestASecondFrameReusesThePacketBuffer(t *testing.T) {
	sender, recorder := senderFor(t, panelPixels)
	recorder.discard = true

	frame := frameOf(panelPixels)

	// One frame first, so that anything the first send does once — the deadline, the
	// sequence — is not what the measurement sees.
	if err := sender.Send(frame); err != nil {
		t.Fatalf("the first frame was refused: %v", err)
	}

	first := &sender.packet[0]
	size := cap(sender.packet)

	allocs := testing.AllocsPerRun(20, func() {
		if err := sender.Send(frame); err != nil {
			t.Fatalf("a repeated frame was refused: %v", err)
		}
	})

	if allocs != 0 {
		t.Fatalf("a frame allocates %.1f times on average, want 0: the packet buffer is not being reused", allocs)
	}

	if &sender.packet[0] != first {
		t.Fatalf("the packet buffer moved between frames, want the one allocated at construction")
	}

	if cap(sender.packet) != size {
		t.Fatalf("the packet buffer grew from %d to %d bytes", size, cap(sender.packet))
	}

	if want := HeaderLen + channelsPerPacket; size != want {
		t.Fatalf("the packet buffer is %d bytes, want %d: one header and one full payload", size, want)
	}
}

// TestASocketThatWillNotTakeTheDatagramIsATypedError is the failure a caller has to be
// able to tell from a frame that went out: the socket refused the datagram, or the
// socket ran out of time. Both are refused at the first datagram that fails, and the
// typed error carries the target and the socket's own error.
func TestASocketThatWillNotTakeTheDatagramIsATypedError(t *testing.T) {
	refused := errors.New("connection refused")

	sender, recorder := senderFor(t, panelPixels)
	recorder.failFrom = 2
	recorder.err = &netError{refused}

	err := sender.Send(frameOf(panelPixels))
	if !errors.Is(err, ErrUnreachable) {
		t.Fatalf("a refused datagram returns %v, want ErrUnreachable", err)
	}

	var unreachable *UnreachableError
	if !errors.As(err, &unreachable) {
		t.Fatalf("a refused datagram returns %v, want an *UnreachableError", err)
	}

	if !errors.Is(err, refused) {
		t.Fatalf("the socket's own error is not reachable through %v", err)
	}

	// The frame stopped where it failed: the datagram before it was sent, and the ones
	// after it were not. A caller that treats that as a picture is showing half of one.
	if len(recorder.packets) != 1 {
		t.Fatalf("a frame refused at its second datagram sent %d, want 1", len(recorder.packets))
	}

	timedOut, recorder := senderFor(t, panelPixels)
	recorder.failFrom = 1
	recorder.err = &timeoutError{os.ErrDeadlineExceeded}

	err = timedOut.Send(frameOf(panelPixels))
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("a datagram that ran out of time returns %v, want ErrTimeout", err)
	}

	var timeout *TimeoutError
	if !errors.As(err, &timeout) {
		t.Fatalf("a datagram that ran out of time returns %v, want a *TimeoutError", err)
	}
}

// netError is the socket error a recorder hands back: an error that says it is a net
// error without being one, which is all Send looks at.
type netError struct{ cause error }

func (e *netError) Error() string   { return e.cause.Error() }
func (e *netError) Unwrap() error   { return e.cause }
func (e *netError) Timeout() bool   { return false }
func (e *netError) Temporary() bool { return false }

// timeoutError is the same for a deadline.
type timeoutError struct{ cause error }

func (e *timeoutError) Error() string   { return e.cause.Error() }
func (e *timeoutError) Unwrap() error   { return e.cause }
func (e *timeoutError) Timeout() bool   { return true }
func (e *timeoutError) Temporary() bool { return true }

// TestATargetThatIsNotAnAddressIsUnreachable is the constructor's failure, and it needs
// no Network: an address with no port does not resolve, so nothing is asked of a name
// server and nothing is dialled.
func TestATargetThatIsNotAnAddressIsUnreachable(t *testing.T) {
	for _, target := range []string{"", "   ", "10.0.0.136", "not a host:port", ":"} {
		_, err := New(target, panelPixels)
		if !errors.Is(err, ErrUnreachable) {
			t.Fatalf("the target %q returns %v, want ErrUnreachable", target, err)
		}

		var unreachable *UnreachableError
		if !errors.As(err, &unreachable) {
			t.Fatalf("the target %q returns %v, want an *UnreachableError", target, err)
		}

		if unreachable.Target != target {
			t.Fatalf("the target %q is reported as %q", target, unreachable.Target)
		}
	}

	// A nil socket is refused for the same reason: there is no way to send, and a sender
	// that quietly dropped frames would be worse than one that says so.
	if _, err := NewWithConn(nil, panelPixels); !errors.Is(err, ErrUnreachable) {
		t.Fatalf("a nil socket returns %v, want ErrUnreachable", err)
	}
}

// TestPacketsForTheFramesThatMatter is the only arithmetic another package needs from
// this one: how many datagrams a frame of that many pixels is.
func TestPacketsForTheFramesThatMatter(t *testing.T) {
	for _, shape := range []struct {
		name   string
		pixels int
		want   int
	}{
		{name: "no display", pixels: 0, want: 0},
		{name: "one pixel", pixels: 1, want: 1},
		{name: "a 48-LED strip", pixels: 48, want: 1},
		{name: "one full packet", pixels: MaxPixelsPerPacket, want: 1},
		{name: "one past a full packet", pixels: MaxPixelsPerPacket + 1, want: 2},
		{name: "the operator's panel", pixels: panelPixels, want: panelFrame},
		{name: "a frame of exactly 34 packets", pixels: 34 * MaxPixelsPerPacket, want: 34},
	} {
		if got := PacketsFor(shape.pixels); got != shape.want {
			t.Fatalf("%s: %d pixels is %d datagrams, want %d", shape.name, shape.pixels, got, shape.want)
		}
	}
}

// TestTheWriteDeadlineBoundsTheWholeFrame is the bound on a frame, not on a datagram: a
// socket that stops draining must cost the timeout once and not thirty-five times, and
// the default has to sit inside the five seconds the host allows a ping to be answered.
func TestTheWriteDeadlineBoundsTheWholeFrame(t *testing.T) {
	if DefaultWriteDeadline <= 0 || DefaultWriteDeadline > 5*time.Second {
		t.Fatalf("the default write deadline is %s, want a positive one inside the host's 5s ping window", DefaultWriteDeadline)
	}

	sender, _ := senderFor(t, panelPixels)
	if sender.timeout != DefaultWriteDeadline {
		t.Fatalf("a new sender's deadline is %s, want %s", sender.timeout, DefaultWriteDeadline)
	}

	short, _ := senderFor(t, panelPixels, WithWriteDeadline(time.Second))
	if short.timeout != time.Second {
		t.Fatalf("WithWriteDeadline(time.Second) gave %s", short.timeout)
	}

	// A deadline of zero or less is not a deadline: the default is kept rather than a
	// socket left with no bound at all.
	zero, _ := senderFor(t, panelPixels, WithWriteDeadline(0))
	if zero.timeout != DefaultWriteDeadline {
		t.Fatalf("WithWriteDeadline(0) gave %s, want the default %s", zero.timeout, DefaultWriteDeadline)
	}
}

// TestTheHeaderFieldsAreTheProtocolsOwn pins the four constants that a reviewer can
// check against the protocol by eye, so that a change to any of them is a failing test
// and not a quietly different picture.
func TestTheHeaderFieldsAreTheProtocolsOwn(t *testing.T) {
	sender, recorder := senderFor(t, panelPixels)

	if err := sender.Send(frameOf(panelPixels)); err != nil {
		t.Fatalf("a %d-pixel frame was refused: %v", panelPixels, err)
	}

	packet := recorder.packets[0]

	if got, want := packet[0], byte(0x40); got != want {
		t.Fatalf("the flags byte is %#02x, want %#02x: version 1, no push", got, want)
	}

	if got, want := packet[2], byte(0x0B); got != want {
		t.Fatalf("the data type is %#02x, want %#02x: RGB, 8 bits a channel, three channels", got, want)
	}

	if got, want := packet[3], byte(1); got != want {
		t.Fatalf("the display id is %d, want %d", got, want)
	}

	if got, want := MaxPixelsPerPacket, 480; got != want {
		t.Fatalf("a datagram carries %d pixels, want %d: 1440 colour bytes", got, want)
	}

	if got, want := HeaderLen, 10; got != want {
		t.Fatalf("the header is %d bytes, want %d", got, want)
	}
}

// TestTheFirstDatagramOfAnEightyEightPercentFrame is the bytes a person can check against
// the protocol by eye, taken from the code that would send them rather than written out
// here: a printing printer at 88 %, a 256x64 panel, the picture the module's own rule gives
// that frame, and the first of its thirty-five datagrams.
//
// The frame is decided by render.Decide and laid out by matrix, so the colour and the fill
// are the module's and not this test's. The header is then asserted byte for byte, and the
// first few pixels are checked to be the frame's colour and to arrive in the order R, G, B.
func TestTheFirstDatagramOfAnEightyEightPercentFrame(t *testing.T) {
	at := time.Now()

	frame := render.Decide(printer.Snapshot{
		ID:        "01J8DEVICE",
		Name:      "Morpheus",
		Connected: true,
		State:     printer.Reading[string]{Value: printer.StatePrinting, Known: true},
		Job: printer.Job{
			Live:     true,
			Running:  printer.Reading[bool]{Value: true, Known: true},
			Progress: printer.Reading[float64]{Value: 88, Known: true},
		},
		LastChange: at,
		ObservedAt: at,
	}, at, render.Config{LEDs: panelPixels})

	geometry := matrix.Geometry{Width: 256, Height: 64}

	pixels, err := matrix.Raster(frame, geometry)
	if err != nil {
		t.Fatalf("the frame was refused: %v", err)
	}

	sender, recorder := senderFor(t, panelPixels)

	if err := sender.Send(pixels); err != nil {
		t.Fatalf("the frame was refused: %v", err)
	}

	first := recorder.packets[0]

	// 0x40     version 1, no push — this is not the frame's last datagram
	// 0x00     the sender's first sequence number
	// 0x0B     RGB, eight bits a channel, three channels
	// 0x01     the display id
	// 00000000 the first pixel's channel offset
	// 05A0     1440 colour bytes, which is 480 pixels
	want := [HeaderLen]byte{0x40, 0x00, 0x0B, 0x01, 0x00, 0x00, 0x00, 0x00, 0x05, 0xA0}

	if got := header(first); got != want {
		t.Fatalf("the first datagram's header is % X, want % X", got, want)
	}

	// A fill of 225 columns over 64 rows is 14,400 pixels, so every pixel of this datagram
	// is inside the fill and carries the frame's colour.
	for pixel := range 3 {
		base := HeaderLen + pixel*3

		got := wled.RGB{R: first[base], G: first[base+1], B: first[base+2]}
		if got != frame.Colour {
			t.Fatalf("pixel %d of the first datagram is %+v, want the frame's colour %+v", pixel, got, frame.Colour)
		}
	}

	t.Logf("88 %% of %d pixels is %d lit; the fill is %d columns of %d",
		panelPixels, frame.Lit, matrix.FillColumns(frame, geometry), geometry.Width)
	t.Logf("the first datagram, %d bytes, begins: % X | % X",
		len(first), first[:HeaderLen], first[HeaderLen:HeaderLen+12])
}
