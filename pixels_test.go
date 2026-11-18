package main

// The second transport, tested where it can be: the datagrams are asserted byte for byte off a
// loopback socket, the rate is a deadline on a clock the test moves, and the hand-back is the read
// the module makes of the controller once the hold is over.
//
// Nothing here needs the operator's panel. Where a test needs a controller it needs only a
// recorder: a datagram cannot be acknowledged, so what a test can check is that the bytes left and
// that the module asked the device whether they landed.

import (
	"context"
	"errors"
	"net"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nothingmn/wled-printer-status/internal/ddp"
	"github.com/nothingmn/wled-printer-status/internal/render"
	"github.com/nothingmn/wled-printer-status/internal/text"
	"github.com/nothingmn/wled-printer-status/internal/wled"
)

// pushedFrames is one controller's pixel transport as a test reads it: every frame the writer
// handed it, in order, and the failures a test asked for.
//
// The frames are copied out because the writer pushes the same slice again for the whole hold —
// that is the point of the repeat — so a recorder holding the slice rather than its contents
// could not tell one push from sixty.
type pushedFrames struct {
	mu     sync.Mutex
	frames [][]wled.RGB
	fail   int
	failed int
	closed bool
}

// Send records one frame, or fails it as the test asked.
func (p *pushedFrames) Send(frame []wled.RGB) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.fail > 0 {
		p.fail--
		p.failed++

		return errors.New("the datagrams did not leave: the socket refused the write")
	}

	p.frames = append(p.frames, append([]wled.RGB(nil), frame...))

	return nil
}

// Close records that the socket was released.
func (p *pushedFrames) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.closed = true

	return nil
}

// pushed is how many frames reached the transport.
func (p *pushedFrames) pushed() int {
	p.mu.Lock()
	defer p.mu.Unlock()

	return len(p.frames)
}

// failures is how many pushes the test made fail.
func (p *pushedFrames) failures() int {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.failed
}

// last is the picture the panel was given most recently.
func (p *pushedFrames) last() []wled.RGB {
	p.mu.Lock()
	defer p.mu.Unlock()

	if len(p.frames) == 0 {
		return nil
	}

	return p.frames[len(p.frames)-1]
}

// all is every frame pushed, in order.
func (p *pushedFrames) all() [][]wled.RGB {
	p.mu.Lock()
	defer p.mu.Unlock()

	return append([][]wled.RGB(nil), p.frames...)
}

// failNext makes the next count pushes fail.
func (p *pushedFrames) failNext(count int) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.fail = count
}

// isClosed reports whether the socket was released.
func (p *pushedFrames) isClosed() bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.closed
}

// pushInto hands a writer a recorder in place of a socket, and the operator's controller address
// so that the address the writer derives is the one the module would derive.
func pushInto(pen *writer) *pushedFrames {
	recorder := &pushedFrames{}
	pen.where = "http://10.0.0.136"
	pen.dialPixels = func(string, int) (pixelPush, error) { return recorder, nil }

	return recorder
}

// TestTheEstablishingBodyIsStillTheOneThePanelTakes pins the one JSON request an event still makes,
// byte for byte, and pins that it is the only one.
//
// The body gives segment 0 the whole panel, switched on, in the background colour, with the word as
// its own name, and switches segment 1 — the frame's remainder — off. It is what the panel falls
// back to when live mode ends, and it is the only thing that puts the word's name on the
// controller, because a datagram carries none.
func TestTheEstablishingBodyIsStillTheOneThePanelTakes(t *testing.T) {
	recorder := &wireCapture{}
	server := recorder.server(t)

	light, err := wled.New(server.URL)
	if err != nil {
		t.Fatalf("wled.New: %v", err)
	}

	pen := newWriter(light, 128, quiet)
	pen.setPanel(wled.Info{LEDCount: 16384, MatrixWidth: 256, MatrixHeight: 64})
	pushInto(pen)

	if _, err = pen.paintEvent(context.Background(), finishedWordEvent()); err != nil {
		t.Fatalf("paint the finished event: %v", err)
	}

	const established = `{"seg":[{"id":0,"start":0,"stop":256,"startY":0,"stopY":64,"on":true,"col":[[0,0,0]],"fx":0,"n":"FINISHED"},{"id":1,"on":false}]}`

	writes := make([]string, 0, len(recorder.bodies))

	for _, body := range recorder.bodies {
		// The read of /json/info is recorded as an empty body; a write is what this test is about.
		if strings.TrimSpace(body) != "" {
			writes = append(writes, body)
		}
	}

	if len(writes) != 1 {
		t.Fatalf("the event made %d JSON writes, want the one that establishes the panel: %v", len(writes), writes)
	}

	if writes[0] != established {
		t.Fatalf("the request is\n%s\nwant\n%s", writes[0], established)
	}
}

// TestTheFinishedWordReachesThePanelAsDDPDatagrams is the transport itself, byte for byte: a real
// sender, a real loopback socket, and the datagrams read off it.
//
// The word is 16,384 pixels because that is the whole panel: a datagram names where its pixels
// belong and nothing else, so the pixels outside the letters would otherwise keep whatever the live
// buffer held from the frame before. 16,384 pixels is 35 datagrams — 34 of 480 pixels and one of 64
// — and the push flag is on the last of them and no other, because without it the controller draws
// each datagram as it lands and the panel shows a half-drawn word.
func TestTheFinishedWordReachesThePanelAsDDPDatagrams(t *testing.T) {
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("open a loopback socket for the datagrams: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	strip := newPanelStrip()

	pen := newWriter(strip, 128, quiet)
	pen.setPanel(strip.info)
	pen.where = "http://10.0.0.136"

	dialled := 0

	pen.dialPixels = func(address string, pixels int) (pixelPush, error) {
		dialled++

		if address != "http://10.0.0.136" {
			t.Errorf("the pixels are addressed to %q, want the controller's own address", address)
		}

		if pixels != 16384 {
			t.Errorf("the transport was opened for %d pixels, want the panel's 16384", pixels)
		}

		return ddp.New(conn.LocalAddr().String(), pixels)
	}

	frame, err := pen.paintEvent(context.Background(), finishedWordEvent())
	if err != nil {
		t.Fatalf("paint the finished event: %v", err)
	}

	if dialled != 1 {
		t.Fatalf("the transport was opened %d times for one event, want once", dialled)
	}

	// And the word did not go over the JSON API at all. This is the regression the whole transport
	// exists for: a per-LED body is accepted by this firmware and drawn by nothing, so a build that
	// went back to one would look healthy and show a blank panel.
	if writes := strip.pixelWrites(); len(writes) != 0 {
		t.Fatalf("the event wrote %d per-LED JSON bodies: the panel draws none of them", len(writes))
	}

	packets := readDatagrams(t, conn, ddp.PacketsFor(16384))

	picture := make([]wled.RGB, 16384)

	for index, packet := range packets {
		if len(packet) < ddp.HeaderLen {
			t.Fatalf("datagram %d is %d bytes, shorter than the header", index, len(packet))
		}

		wantFlags := byte(0x40)
		if index == len(packets)-1 {
			wantFlags |= 0x01
		}

		if packet[0] != wantFlags {
			t.Fatalf("datagram %d carries flags %#02x, want %#02x", index, packet[0], wantFlags)
		}

		if packet[1] != byte(index) {
			t.Fatalf("datagram %d carries sequence %d, want %d", index, packet[1], index)
		}

		if packet[2] != 0x0B || packet[3] != 1 {
			t.Fatalf("datagram %d is type %#02x display %d, want RGB24 on display 1", index, packet[2], packet[3])
		}

		first := index * ddp.MaxPixelsPerPacket

		wantOffset := uint32(first * 3)
		gotOffset := uint32(packet[4])<<24 | uint32(packet[5])<<16 | uint32(packet[6])<<8 | uint32(packet[7])

		if gotOffset != wantOffset {
			t.Fatalf("datagram %d starts at channel %d, want %d (pixel %d, three channels a pixel)",
				index, gotOffset, wantOffset, first)
		}

		count := min(ddp.MaxPixelsPerPacket, 16384-first)

		wantLength := uint16(count * 3) //nolint:gosec // a datagram carries at most 1440 bytes.

		gotLength := uint16(packet[8])<<8 | uint16(packet[9])
		if gotLength != wantLength {
			t.Fatalf("datagram %d carries %d colour bytes, want %d for %d pixels", index, gotLength, wantLength, count)
		}

		if len(packet) != ddp.HeaderLen+int(wantLength) {
			t.Fatalf("datagram %d is %d bytes, want %d", index, len(packet), ddp.HeaderLen+int(wantLength))
		}

		for offset := range count {
			base := ddp.HeaderLen + offset*3
			picture[first+offset] = wled.RGB{R: packet[base], G: packet[base+1], B: packet[base+2]}
		}
	}

	// A frame is 35 datagrams and the socket had 35: a 36th would be a frame whose length the writer
	// worked out wrong, and the panel would show the first 64 pixels twice.
	if _, err := conn.Read(make([]byte, 2048)); err == nil {
		t.Fatal("a 36th datagram arrived for a 16,384-pixel panel")
	}

	// The last datagram is the short one, and that is the arithmetic most likely to be one out:
	// 34 datagrams reach 16,320 pixels and stop 64 short of the panel.
	if last := packets[len(packets)-1]; len(last) != ddp.HeaderLen+64*3 {
		t.Fatalf("the last datagram is %d bytes, want the header and the panel's last 64 pixels", len(last))
	}

	// The picture on the wire is the picture this function returned, and the lit pixels in it are
	// exactly the ones the rasteriser names — the same list the JSON body used to carry.
	want, err := text.Raster(finishedWord, finishedWordScale, text.Geometry{Width: 256, Height: 64})
	if err != nil {
		t.Fatalf("rasterise %q: %v", finishedWord, err)
	}

	if !slices.Equal(frame, picture) {
		t.Fatal("the datagrams do not carry the frame the event returned")
	}

	lit := make([]int, 0, len(want))

	for index, colour := range picture {
		switch colour {
		case render.ColourFinished:
			lit = append(lit, index)
		case finishedWordBackground:
		default:
			t.Fatalf("pixel %d is %+v, which is neither the word's colour nor the background", index, colour)
		}
	}

	if !slices.Equal(lit, want) {
		t.Fatalf("the panel is given %d pixels in the finished green and the word is %d pixels: the two lists differ",
			len(lit), len(want))
	}
}

// readDatagrams reads exactly count datagrams off the socket, and fails the test with what it got
// when they do not arrive.
func readDatagrams(t *testing.T, conn *net.UDPConn, count int) [][]byte {
	t.Helper()

	if err := conn.SetReadDeadline(time.Now().Add(howLong)); err != nil {
		t.Fatalf("set the read deadline: %v", err)
	}

	packets := make([][]byte, 0, count)

	for len(packets) < count {
		buffer := make([]byte, 2048)

		read, err := conn.Read(buffer)
		if err != nil {
			t.Fatalf("read datagram %d of %d: %v", len(packets)+1, count, err)
		}

		packets = append(packets, buffer[:read])
	}

	// The deadline is shortened before returning so that the caller's own read — the one that asserts
	// no datagram followed the frame's last — does not sit out the full test timeout waiting for one
	// that will never come.
	if err := conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond)); err != nil {
		t.Fatalf("set the read deadline: %v", err)
	}

	return packets
}

// TestTheWordSitsInTheBandTheRasteriserPlacesIt is the picture's geometry: the frame is the whole
// panel, the letters are where internal/text put them, and every other pixel is the background.
//
// The band matters because the panel has no spare columns: the word is 235 by 35 pixels on a 256 by
// 64 panel, so it sits at column 10 and row 14, and a frame that put it anywhere else would be a
// word somebody did not ask for.
func TestTheWordSitsInTheBandTheRasteriserPlacesIt(t *testing.T) {
	place, err := text.Place(finishedWord, finishedWordScale, text.Geometry{Width: 256, Height: 64})
	if err != nil {
		t.Fatalf("place %q: %v", finishedWord, err)
	}

	lit, err := text.Raster(finishedWord, finishedWordScale, text.Geometry{Width: 256, Height: 64})
	if err != nil {
		t.Fatalf("rasterise %q: %v", finishedWord, err)
	}

	frame := wordFrame(livePanel, lit, render.ColourFinished)

	if len(frame) != 256*64 {
		t.Fatalf("the frame is %d pixels, want the whole panel's %d", len(frame), 256*64)
	}

	for index, colour := range frame {
		x, y := index%256, index/256

		inBand := y >= place.Top && y < place.Top+place.Height &&
			x >= place.Left && x < place.Left+place.Width

		if !inBand && colour != finishedWordBackground {
			t.Fatalf("pixel (%d,%d) is %+v, outside the word's band %d..%d by %d..%d, want the background",
				x, y, colour, place.Left, place.Left+place.Width, place.Top, place.Top+place.Height)
		}
	}

	// Every lit pixel is inside the band, and the band is exactly as tall and wide as the word: a
	// frame one row or one column out would still pass the check above, so the edges are asserted
	// here.
	top, bottom, left, right := 64, -1, 256, -1

	for index, colour := range frame {
		if colour != render.ColourFinished {
			continue
		}

		x, y := index%256, index/256
		top, bottom = min(top, y), max(bottom, y)
		left, right = min(left, x), max(right, x)
	}

	if top != place.Top || bottom != place.Top+place.Height-1 {
		t.Fatalf("the letters run from row %d to %d, want the band's %d to %d",
			top, bottom, place.Top, place.Top+place.Height-1)
	}

	if left != place.Left || right != place.Left+place.Width-1 {
		t.Fatalf("the letters run from column %d to %d, want the band's %d to %d",
			left, right, place.Left, place.Left+place.Width-1)
	}
}

// TestTheWordIsPushedAgainOnceASecondForAsLongAsItHolds is the rate, on a clock the test moves.
//
// A still picture needs no frame rate: it needs to outlive the controller's realtime timeout, so
// one repeat a second is two and a half times the margin at 2500 ms. The count is the assertion —
// sixty pushes for a sixty-second hold, then no more, because a repeat after the hold is a word
// this module is still drawing on a light it has given back.
func TestTheWordIsPushedAgainOnceASecondForAsLongAsItHolds(t *testing.T) {
	strip := newPanelStrip()

	clock := newFakeClock(time.Date(2026, time.November, 18, 22, 0, 0, 0, time.UTC))
	pen := newWriter(strip, 128, quiet)
	pen.setPanel(strip.info)
	clock.watch(pen)

	pushed := pushInto(pen)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go pen.run(ctx)

	steady := render.Frame{Colour: render.ColourFinished, Total: 16384, Reason: render.ReasonNoLiveJob}

	pen.submit(steady)
	strip.waitForPaints(t, 1)
	clock.waitArmed(t, 0)

	pen.fire(finishedWordEvent())

	waitFor(t, "the word's first frame to reach the transport", func() bool { return pushed.pushed() == 1 })

	first := pushed.last()

	// The driver keeps deciding the steady frame while the word holds; it waits in the writer's one
	// slot and goes out when the hold ends.
	pen.submit(steady)

	// One repeat for each second of the hold: the picture goes out at the moment the hold starts and
	// at every whole second after it, and the last of those is one second before the hold ends.
	for second := 2; second <= int(finishedEventHolds/time.Second); second++ {
		clock.Advance(pixelRepeat)

		want := second
		waitFor(t, "the picture's repeat", func() bool { return pushed.pushed() == want })
	}

	if got := pushed.pushed(); got != 60 {
		t.Fatalf("a %s hold pushed the picture %d times, want 60: one at the start and one a second after it",
			finishedEventHolds, got)
	}

	// Every repeat is the same bytes as the first: the picture does not move, and a frame rebuilt per
	// push would be arithmetic running for a minute to no effect.
	for index, frame := range pushed.all() {
		if !slices.Equal(frame, first) {
			t.Fatalf("push %d is not the picture the hold started with", index+1)
		}
	}

	// The hold ends: the steady frame takes the light back and nothing is pushed after it.
	clock.Advance(pixelRepeat)

	waitFor(t, "the steady frame to come back at the end of the hold", func() bool {
		return len(steadyPaints(strip)) == 2
	})

	if got := pushed.pushed(); got != 60 {
		t.Fatalf("the picture was pushed %d times by the end of the hold, want 60: a repeat after the hold is a word on a light this module has given back", got)
	}

	// One socket for the writer's life, released when the writer stops.
	cancel()

	waitFor(t, "the pixel socket to be released when the writer stops", pushed.isClosed)
}

// TestTheHandBackReleasesRealtimeAndTheSteadyFrameTakesTheLightBack is the other half of the
// transport: what proves the light was let go.
//
// A JSON state write does not end live mode by itself — measured on the operator's panel, live
// stayed true through a state write and went false about 2.5 s after the last datagram — so the
// module stops pushing, writes the steady frame, waits out the firmware's own timeout and reads what
// the controller says. That read is the only evidence the hand-back exists at all.
func TestTheHandBackReleasesRealtimeAndTheSteadyFrameTakesTheLightBack(t *testing.T) {
	strip := newPanelStrip()

	clock := newFakeClock(time.Date(2026, time.November, 18, 22, 0, 0, 0, time.UTC))

	var written logs

	pen := newWriter(strip, 128, written.logf)
	pen.setPanel(strip.info)
	clock.watch(pen)

	pushed := pushInto(pen)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go pen.run(ctx)

	pen.submit(render.Frame{Colour: render.ColourPrinting, Total: 16384, Reason: render.ReasonProgress})
	strip.waitForPaints(t, 1)
	clock.waitArmed(t, 0)

	// The panel takes live mode: that is what /json/info answers while the word is up.
	strip.takeRealtime(true)

	pen.fire(finishedWordEvent())

	waitFor(t, "the word to reach the panel", func() bool { return len(wordPixels(pushed)) > 0 })

	// The push itself reads the controller once, to hear that live mode took. The hand-back is the
	// read after that one.
	waitFor(t, "the read that confirms live mode", func() bool { return strip.realtimeReads() >= 1 })

	confirmed := strip.realtimeReads()

	// The frame the driver decides during the hold is what takes the light back when it ends.
	pen.submit(render.Frame{Colour: render.ColourPrinting, Total: 16384, Reason: render.ReasonProgress})

	// The hold runs out, the steady frame is written, and the writer arms the wait that ends the
	// realtime timeout before it reads the controller.
	mark := clock.mark()
	clock.Advance(finishedEventHolds)

	waitFor(t, "the steady frame to take the light back", func() bool {
		return len(steadyPaints(strip)) == 2
	})

	clock.waitArmed(t, mark)

	// Nothing has been pushed since the hold ended, so the panel is back on its own segments — which
	// is what the controller reports once its own timeout has run out.
	strip.takeRealtime(false)

	clock.Advance(realtimeTimeout + handbackMargin)

	waitFor(t, "the hand-back to read what the controller says about realtime", func() bool {
		return strip.realtimeReads() > confirmed
	})

	lines := strings.Join(written.all(), "\n")

	if !strings.Contains(lines, "reports live=false") {
		t.Fatalf("the hand-back said nothing about the controller leaving realtime: %q", lines)
	}

	if !strings.Contains(lines, "back on its own segments") {
		t.Fatalf("the hand-back did not say the light came back: %q", lines)
	}

	if got := pushed.pushed(); got != 1 {
		t.Fatalf("the picture was pushed %d times, want once: this test never advances the repeat's own second", got)
	}
}

// TestAFailedRepeatIsLoggedOnceAndTheHoldSurvives is the failure shape of a repeated picture: a
// controller that stops taking datagrams costs one line, not one a second, and the hold runs to its
// own end because the picture may still be on the panel.
func TestAFailedRepeatIsLoggedOnceAndTheHoldSurvives(t *testing.T) {
	strip := newPanelStrip()

	clock := newFakeClock(time.Date(2026, time.November, 18, 22, 0, 0, 0, time.UTC))

	var written logs

	pen := newWriter(strip, 128, written.logf)
	pen.setPanel(strip.info)
	clock.watch(pen)

	pushed := pushInto(pen)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go pen.run(ctx)

	pen.submit(render.Frame{Colour: render.ColourPrinting, Total: 16384, Reason: render.ReasonProgress})
	strip.waitForPaints(t, 1)
	clock.waitArmed(t, 0)

	pen.fire(finishedWordEvent())

	waitFor(t, "the word's first frame to reach the transport", func() bool { return pushed.pushed() == 1 })

	// Every repeat from here fails: forty seconds of a controller that takes no datagrams.
	pushed.failNext(1000)

	for range 40 {
		clock.Advance(pixelRepeat)
		time.Sleep(2 * time.Millisecond)
	}

	waitFor(t, "the failed repeats to be counted", func() bool { return pushed.failures() >= 3 })

	repeated := 0

	for _, line := range written.all() {
		if strings.Contains(line, "did not reach the panel again") {
			repeated++
		}
	}

	if repeated != 1 {
		t.Fatalf("forty seconds of failed repeats wrote %d lines about them, want one: %q", repeated, written.all())
	}

	if !strings.Contains(strings.Join(written.all(), "\n"), "the hold continues") {
		t.Fatalf("the line about a failed repeat does not say the hold survived: %q", written.all())
	}

	if ctx.Err() != nil {
		t.Fatal("the writer stopped on a failed repeat, and a module that exits is a module that needs a person")
	}

	// The hold still ends on its own clock, and the frame the driver decided takes the light back.
	pushed.failNext(0)

	pen.submit(render.Frame{Colour: render.ColourPrinting, Total: 16384, Reason: render.ReasonProgress})

	clock.Advance(finishedEventHolds)

	waitFor(t, "the steady frame to come back at the end of the hold", func() bool {
		return len(steadyPaints(strip)) == 2
	})
}

// TestThePixelTransportIsThisControllersOwnHost is the address arithmetic: the datagrams go to the
// host the operator set, on DDP's own port, and an address that cannot be turned into one is a
// configuration mistake reported as itself rather than a frame sent nowhere.
func TestThePixelTransportIsThisControllersOwnHost(t *testing.T) {
	for _, shape := range []struct {
		name    string
		address string
		want    string
	}{
		{name: "the operator's panel", address: "http://10.0.0.136", want: "10.0.0.136:4048"},
		{name: "a trailing slash", address: "http://10.0.0.136/", want: "10.0.0.136:4048"},
		{name: "a base URL naming a port of its own", address: "http://10.0.0.136:80", want: "10.0.0.136:4048"},
		{name: "a name rather than an address", address: "https://morpheus.local", want: "morpheus.local:4048"},
	} {
		t.Run(shape.name, func(t *testing.T) {
			got, err := pixelTarget(shape.address)
			if err != nil {
				t.Fatalf("pixelTarget(%q): %v", shape.address, err)
			}

			if got != shape.want {
				t.Fatalf("pixelTarget(%q) is %q, want %q", shape.address, got, shape.want)
			}
		})
	}

	for _, address := range []string{"", "   ", "10.0.0.136", "http://"} {
		t.Run("refused: "+address, func(t *testing.T) {
			if _, err := pixelTarget(address); !errors.Is(err, errNoPixelTarget) {
				t.Fatalf("pixelTarget(%q) answered %v, want the address refused as unusable", address, err)
			}
		})
	}
}

// TestAnEventCannotBePushedWithNoAddress is the same refusal from the writer's side: a writer that
// never learned its controller's address cannot put a word on a panel, and it says so instead of
// sending a datagram to a host nobody named.
func TestAnEventCannotBePushedWithNoAddress(t *testing.T) {
	strip := newPanelStrip()

	pen := newWriter(strip, 128, quiet)
	pen.setPanel(strip.info)
	pen.where = ""
	pen.dialPixels = dialPixels

	if _, err := pen.paintEvent(context.Background(), finishedWordEvent()); !errors.Is(err, errNoPixelTarget) {
		t.Fatalf("the event answered %v, want the missing address named", err)
	}

	// The establishing body went out first and is what is left on the panel: the segment on, the
	// whole panel, the word as its name. A word that cannot be pushed is a panel that says FINISHED
	// by name rather than by pixels, which is a truthful degradation rather than a dark one.
	if writes := strip.shown(); len(writes) != 1 {
		t.Fatalf("the failed event wrote %d JSON requests, want the establishing one: %+v", len(writes), writes)
	}
}
