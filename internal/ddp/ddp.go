// Package ddp sends frames of pixels to one WLED controller over DDP, the
// Distributed Display Protocol: raw RGB bytes in UDP datagrams to port 4048.
//
// # A second transport for a picture
//
// This module drives its controller with JSON over HTTP today: one request, one
// body, two segments — a rectangle the job has filled and the remainder switched
// off. That stays the control path, and it stays the read-back path too, because
// a segment can be read back, can carry a name and can run an effect.
//
// DDP is the other thing a controller accepts: no document, no segments, no
// effects and no read-back, only pixels. A datagram carries a ten-byte header and
// a run of RGB bytes, and those bytes land at an offset in one live buffer that
// the controller draws instead of its own segments. It is the transport a picture
// needs when the picture is drawn here rather than expressed as geometry — a bar
// and a percentage and a job name in our own font, which no arrangement of
// segments can say.
//
// None of it is configuration: WLED starts its DDP receiver in the firmware,
// unconditionally, on port 4048, whatever its DMX mode or sync settings are. It is
// unicast, so it crosses the Docker bridge this module runs on. What it costs is
// that a frame is only pixels. A segment can be read back and a datagram cannot,
// so a frame's receipt is proved by the next frame arriving and by nothing else.
// And while a stream is running the controller is in live mode: its own segments
// and effects are bypassed, and 2.5 s of silence returns the strip to them —
// WLED's realtimeTimeoutMs, 2500 on both of this operator's controllers.
//
// # Not wired, on purpose
//
// Nothing in this module calls this package yet. main.go does not import it, the
// writer does not know it exists, and the installed behaviour is unchanged. It is
// a backend in waiting, and the composer that draws a layout will be what selects
// it: choosing DDP over JSON for a given controller is a product decision nobody
// has made. Do not read a frame sent here as having reached a panel until that is
// wired and somebody has looked at the light.
//
// # One frame is thirty-five datagrams
//
// WLED reads 1440 bytes of colour per datagram — DDP_CHANNELS_PER_PACKET — which is
// 480 RGB pixels. The operator's 256x64 panel is 16,384 pixels, so one frame is 35
// datagrams: 34 carrying 480 pixels and one carrying the last 64. The count is worth
// stating because it is the one number here that is easy to be one out on: 34 datagrams
// reach 16,320 pixels and stop 64 short. Each carries the
// byte offset of its first pixel and the number of colour bytes it brings, both
// big-endian, so the controller can place them without assuming the order they
// arrive in or that none was lost. The push flag travels on the last one, as it
// does on every sender WLED ships: without a push the controller draws each
// datagram as it lands, and a frame is then briefly visible half-drawn.
//
// # What this package is not
//
// It does not decide what a frame is, lay one out, or know what a printer is. It is
// handed a buffer of pixels that is exactly the display it was declared with and it
// puts them on the wire. Which pixels, in what colour, is internal/panel's question.
package ddp

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/nothingmn/wled-printer-status/internal/wled"
)

const (
	// DefaultPort is the UDP port a WLED controller's DDP receiver listens on. It is
	// not a setting: ddp.begin(false, DDP_DEFAULT_PORT) runs in the firmware whether
	// or not anything has configured the controller.
	DefaultPort = 4048

	// HeaderLen is the DDP header: flags, sequence number, data type, the display
	// id, a 32-bit big-endian channel offset and a 16-bit big-endian length.
	HeaderLen = 10

	// MaxPixelsPerPacket is how many pixels one datagram may carry: 1440 colour
	// bytes — what WLED reads per packet — over three bytes a pixel.
	MaxPixelsPerPacket = 1440 / channelsPerPixel

	// channelsPerPacket is DDP_CHANNELS_PER_PACKET in the firmware's own terms: the
	// most colour data one datagram carries.
	channelsPerPacket = 1440

	// channelsPerPixel is three, one byte each for red, green and blue. There is no
	// white channel here because the colour type this module already has is RGB.
	channelsPerPixel = 3

	// flagVersion1 is the top two bits of the flags byte saying the packet is DDP
	// version 1, and flagPush is the bit saying the frame is complete.
	flagVersion1 = 0x40
	flagPush     = 0x01

	// dataTypeRGB24 is RGB, eight bits a channel, three channels. It is the value
	// the firmware's own sender writes (DDP_TYPE_RGB24).
	dataTypeRGB24 = 0x0B

	// displayID is header byte 3, which WLED and the reference library both read as
	// one whole byte naming the receiver: 1 is "a display", and 246, 250 and 251 are
	// its own control, config and status ids, which the receiver drops. It is fixed
	// because this module talks to one controller and nothing in DDP would make a
	// second id more true.
	displayID = 1

	// DefaultWriteDeadline is how long a whole frame may take to go out. Thirty-five
	// datagrams to a controller on the same LAN take single-digit milliseconds; half
	// a second bounds the frame well inside the five seconds a liveness ping allows,
	// and a write that cannot be made in that time is a socket that is not draining,
	// not a controller that is thinking.
	DefaultWriteDeadline = 500 * time.Millisecond
)

// The failures a caller can tell apart. Each is a sentinel so that errors.Is asks
// the question the caller means, and the two transport failures are also typed so
// that errors.As can get the target and the cause out of one.
var (
	// ErrUnreachable means the datagram could not be handed to the network at all:
	// the address that did not resolve, the socket that refused it, the route that
	// does not exist. On UDP this is nearly always the socket rather than the
	// controller — nothing acknowledges a datagram — so a frame that "succeeded" here
	// proves only that the kernel took it.
	ErrUnreachable = errors.New("ddp: controller unreachable")

	// ErrTimeout means the write did not complete inside the deadline, which for UDP
	// is a send buffer that stays full: a controller that has stopped reading.
	ErrTimeout = errors.New("ddp: the write did not complete in time")

	// ErrEmptyFrame means no pixels were offered. There is nothing to send and a
	// caller that reached here has computed an empty picture.
	ErrEmptyFrame = errors.New("ddp: no pixels to send")

	// ErrFrameTooLarge means the buffer is longer than the display the sender was
	// declared with. Refused rather than truncated: the extra pixels would land past
	// the end of the strip and be dropped by the controller, so a caller that sent
	// them has a geometry bug it should see, not a frame quietly missing its tail.
	ErrFrameTooLarge = errors.New("ddp: the frame is larger than the display")

	// ErrNoDisplay means the sender was built for a display of no pixels, so there is
	// no frame it could ever accept.
	ErrNoDisplay = errors.New("ddp: the display has no pixels")
)

// UnreachableError is a datagram that could not be handed to the network.
type UnreachableError struct {
	// Target is "host:port" the datagram was addressed to.
	Target string
	// Err is what the socket reported.
	Err error
}

// Error states the target and the cause.
func (e *UnreachableError) Error() string {
	return fmt.Sprintf("ddp: %s is unreachable: %v", e.Target, e.Err)
}

// Unwrap exposes both the sentinel and the socket's error to errors.Is and
// errors.As.
func (e *UnreachableError) Unwrap() []error {
	if e.Err == nil {
		return []error{ErrUnreachable}
	}

	return []error{ErrUnreachable, e.Err}
}

// TimeoutError is a write that ran out of time. Its Err carries the socket's own
// error, and errors.Is finds ErrTimeout through it.
type TimeoutError struct {
	// Target is "host:port" the datagram was addressed to.
	Target string
	// Err is what the socket reported, usually a deadline.
	Err error
}

// Error states the target and the cause.
func (e *TimeoutError) Error() string {
	return fmt.Sprintf("ddp: %s did not take the frame in time: %v", e.Target, e.Err)
}

// Unwrap exposes both the sentinel and the socket's error.
func (e *TimeoutError) Unwrap() []error {
	if e.Err == nil {
		return []error{ErrTimeout}
	}

	return []error{ErrTimeout, e.Err}
}

// Sender puts frames on the wire for one controller.
//
// It holds one packet buffer and one sequence number. The buffer is allocated once
// and every datagram of every frame is built in it: a 16,384-pixel frame is 35
// datagrams, and 35 allocations a frame is a garbage collector running on a Pi to
// no purpose. A Sender is not safe for concurrent use — one writer goroutine owns
// it, as the HTTP client's own mutex already assumes of its caller — and it holds
// no lock of its own.
type Sender struct {
	conn   *net.UDPConn
	target string
	// pixels is the display this sender was declared for, and the most a frame may
	// be. A buffer longer than it is refused.
	pixels int
	// timeout is the whole frame's budget, not each datagram's.
	timeout time.Duration
	// packet is the header and one full payload, allocated once.
	packet []byte
	// sequence is the packet's own counter, which starts at zero on the first
	// datagram of the sender's life and wraps at 256 because a byte does.
	sequence uint8
	// write is the one place a datagram leaves this package. It is the socket in the
	// process and a recorder in the byte-exact tests, which is what lets those tests
	// assert the bytes without a network.
	write func(packet []byte) (int, error)
}

// Option adjusts a Sender as it is built.
type Option func(*Sender)

// WithWriteDeadline sets how long a whole frame may take, replacing
// DefaultWriteDeadline. Raising it past a few seconds means one unreachable
// controller can hold a writer goroutine for longer than the host's own patience.
func WithWriteDeadline(deadline time.Duration) Option {
	return func(s *Sender) {
		if deadline > 0 {
			s.timeout = deadline
		}
	}
}

// New builds a Sender for the controller at target, which is "host:port" — usually
// "10.0.0.136:4048" and DefaultPort when the port is WLED's. pixels is how many
// pixels the display has, and the most one frame may carry.
//
// It resolves the address and opens a connected UDP socket, and sends nothing: the
// first datagram is the first frame. An address that cannot be resolved or a socket
// that cannot be opened is an *UnreachableError, because a module that cannot even
// open its socket is a module whose light will never move.
//
// The Sender owns the socket. Close is the only thing that releases it.
func New(target string, pixels int, opts ...Option) (*Sender, error) {
	if strings.TrimSpace(target) == "" {
		return nil, &UnreachableError{Target: target, Err: errors.New("the address is empty")}
	}

	address, err := net.ResolveUDPAddr("udp", target)
	if err != nil {
		return nil, &UnreachableError{Target: target, Err: err}
	}

	// A port of zero is a datagram the kernel will happily take and never deliver, and DDP
	// has no read-back to notice it with, so it is refused here rather than found later on a
	// panel that never changed.
	if address.Port == 0 {
		return nil, &UnreachableError{Target: target, Err: errors.New("the address names no port")}
	}

	conn, err := net.DialUDP("udp", nil, address)
	if err != nil {
		return nil, &UnreachableError{Target: target, Err: err}
	}

	sender, err := newSender(conn, pixels, opts...)
	if err != nil {
		_ = conn.Close()

		return nil, err
	}

	if sender.target == "" {
		sender.target = target
	}

	return sender, nil
}

// NewWithConn builds a Sender on a socket the caller already has, which is how a
// caller shares one socket between two senders or wraps a socket it configured
// itself. conn should be connected — net.DialUDP — because a datagram is sent to
// it and not addressed again per packet.
//
// The Sender takes the socket over and Close closes it. Passing nil is refused with
// ErrUnreachable: there is no socket to send on, and a Sender that silently dropped
// frames would be worse than one that says so.
func NewWithConn(conn *net.UDPConn, pixels int, opts ...Option) (*Sender, error) {
	if conn == nil {
		return nil, &UnreachableError{Target: "(no socket)", Err: errors.New("the connection is nil")}
	}

	target := ""
	if remote := conn.RemoteAddr(); remote != nil {
		target = remote.String()
	}

	sender, err := newSender(conn, pixels, opts...)
	if err != nil {
		return nil, err
	}

	sender.target = target

	return sender, nil
}

// newSender is the constructor both exported ones share, and the one the tests use
// with no socket at all. Keeping it separate is what lets the byte-exact tests set
// a write of their own without a Sender that knows it is being tested.
func newSender(conn *net.UDPConn, pixels int, opts ...Option) (*Sender, error) {
	if pixels <= 0 {
		return nil, fmt.Errorf("%w: %d", ErrNoDisplay, pixels)
	}

	sender := &Sender{
		conn:    conn,
		pixels:  pixels,
		timeout: DefaultWriteDeadline,
		packet:  make([]byte, HeaderLen+channelsPerPacket),
	}

	for _, option := range opts {
		if option != nil {
			option(sender)
		}
	}

	return sender, nil
}

// Close releases the socket. It is safe to call on a Sender built with
// NewWithConn, whose socket this Sender owns from then on.
func (s *Sender) Close() error {
	if s.conn == nil {
		return nil
	}

	return s.conn.Close()
}

// PacketsFor is how many datagrams one frame of that many pixels becomes. It is the
// caller's arithmetic made once here so that no other file has to know the packet
// size: 16,384 pixels is 35 datagrams, 34 full and one carrying 64 pixels.
func PacketsFor(pixels int) int {
	if pixels <= 0 {
		return 0
	}

	return (pixels-1)/MaxPixelsPerPacket + 1
}

// Send puts one whole frame on the wire: frame[0] is the display's first pixel and
// the frame is the whole display, in row-major order on a panel, so pixel (x, y) is
// frame[y*width+x].
//
// It sends len(frame) pixels in order and stops. It does not pad a short frame: a
// datagram says where its pixels go, so the pixels it does not name keep whatever
// the previous frame left there. A caller with a 16,384-pixel display should send
// 16,384 pixels, and a caller that sends fewer has chosen to leave the rest of the
// panel showing something older.
//
// An empty buffer is refused with ErrEmptyFrame and a buffer longer than the display
// the sender was declared with with ErrFrameTooLarge, both before a datagram is
// built. A failed write stops the frame where it is and returns the typed error: the
// pixels already sent are in the controller's live buffer and will be drawn, which is
// exactly why a caller must not treat a half-sent frame as a picture — the next frame
// is what corrects it.
func (s *Sender) Send(frame []wled.RGB) error {
	if len(frame) == 0 {
		return fmt.Errorf("%w: the buffer is empty", ErrEmptyFrame)
	}

	if len(frame) > s.pixels {
		return fmt.Errorf("%w: %d pixels for a display of %d", ErrFrameTooLarge, len(frame), s.pixels)
	}

	// One deadline for the frame rather than one per datagram, so a socket that stops
	// draining cannot spend the timeout 35 times over.
	deadline := time.Now().Add(s.timeout)

	for first := 0; first < len(frame); first += MaxPixelsPerPacket {
		last := min(first+MaxPixelsPerPacket, len(frame))

		// The push flag says the frame is complete, so it goes on the last datagram and
		// nowhere else. It is also the shortest thing to get wrong in a way nothing
		// reports: without it the controller draws every datagram as it lands.
		s.pack(first, frame[first:last], last == len(frame))

		packet := s.packet[:HeaderLen+(last-first)*channelsPerPixel]
		if err := s.send(packet, deadline); err != nil {
			return err
		}

		s.sequence++
	}

	return nil
}

// pack writes one datagram into the sender's buffer: the ten header bytes, then the
// colours.
//
// The offset is in **channels**, not pixels: the controller divides it by the number
// of channels a pixel has before it looks at its buffer, so 480 pixels further on is
// 1440 and not 480. A pixel count written here puts two thirds of a frame in the
// wrong place and the panel shows it as garbage rather than as an error.
func (s *Sender) pack(first int, pixels []wled.RGB, last bool) {
	packet := s.packet

	packet[0] = flagVersion1
	if last {
		packet[0] |= flagPush
	}

	packet[1] = s.sequence
	packet[2] = dataTypeRGB24
	packet[3] = displayID

	offset := uint32(first * channelsPerPixel)
	packet[4] = byte(offset >> 24)
	packet[5] = byte(offset >> 16)
	packet[6] = byte(offset >> 8)
	packet[7] = byte(offset)

	length := uint16(len(pixels) * channelsPerPixel)
	packet[8] = byte(length >> 8)
	packet[9] = byte(length)

	for index, colour := range pixels {
		base := HeaderLen + index*channelsPerPixel
		packet[base] = colour.R
		packet[base+1] = colour.G
		packet[base+2] = colour.B
	}
}

// send hands one datagram to the socket, or to the recorder a test put in its place.
func (s *Sender) send(packet []byte, deadline time.Time) error {
	if s.write != nil {
		if _, err := s.write(packet); err != nil {
			return s.transportError(err)
		}

		return nil
	}

	if err := s.conn.SetWriteDeadline(deadline); err != nil {
		return &UnreachableError{Target: s.target, Err: err}
	}

	if _, err := s.conn.Write(packet); err != nil {
		return s.transportError(err)
	}

	return nil
}

// transportError turns a socket failure into the typed error a caller can act on,
// keeping the socket's own error behind it.
func (s *Sender) transportError(cause error) error {
	var netErr net.Error

	timedOut := errors.Is(cause, os.ErrDeadlineExceeded) ||
		(errors.As(cause, &netErr) && netErr.Timeout())

	if timedOut {
		return &TimeoutError{Target: s.target, Err: cause}
	}

	return &UnreachableError{Target: s.target, Err: cause}
}
