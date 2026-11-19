// The second transport: the picture itself, as pixels over DDP.
//
// # Why the word is not pixels in a JSON body
//
// The word was written one LED at a time through the JSON API — a state body whose segment
// carried an "i" array, first the LED the run starts at, then one colour per LED. The
// controller answered every one of them {"success":true} and drew nothing. On the operator's
// 256x64 panel (morpheus, WLED 14.7.0-mdev) that was measured four ways: a hundred pixels at
// LED 7680 with the segment frozen, the same with it unfrozen, the same with the segment's
// four bounds declared in the body that carried the pixels, and a chunk of sixty colours in
// case the accepted size was far smaller than the 8 KiB budget believed — 200 on all four,
// no error logged, and the operator looked twice at a panel that stayed blank. The firmware's
// /json/state carries no "i" key at all, so a body that was accepted cannot even be read back
// to see what the controller made of it. Geometry writes on the same endpoint do render: the
// progress bar is segment bounds and it has always worked.
//
// So the picture goes on the transport built for pictures. DDP is a UDP datagram of raw RGB
// to port 4048, which is the buffer a WLED controller draws instead of its own segments while
// a stream is live. It carries no document, no segment and no name — a datagram cannot be read
// back and cannot carry a name — so the establishing body still goes out first over JSON, and
// the name the panel reports while the word is up is that body's doing.
//
// # The rate, and why it is not a stream
//
// A still picture needs no frame rate. It needs to outlive the controller's realtime timeout,
// which on this firmware is realtimeTimeoutMs = 2500 ms: after that much silence WLED drops
// live mode and draws its own segments again. So the same 35 datagrams are pushed again once a
// second — pixelRepeat — which leaves two and a half times the margin and survives one repeat
// arriving late or being lost. Faster buys nothing (the bytes never change) and costs the
// panel's WiFi: 16,384 pixels is 35 datagrams, so once a second is about 50 kB/s for as long
// as the word holds. It is not the JSON write budget of AGENTS.md 5.7: that budget is about a
// controller's single small web server, and a datagram never reaches it.
//
// # The hand-back
//
// When the hold ends this module stops pushing and writes the steady frame over JSON. The
// panel keeps drawing the pixels until the realtime timeout runs out — measured on the device:
// a JSON state write does not end live mode, and /json/info went back to live=false about two
// and a half seconds after the last datagram — so the frame that takes the light back is
// written first and the controller is read afterwards, once, to prove it left realtime. That is
// the only thing that proves the hand-back: the pixels cannot be read back at all.
package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/nothingmn/wled-printer-status/internal/ddp"
	"github.com/nothingmn/wled-printer-status/internal/wled"
)

const (
	// realtimeTimeout is how long this firmware keeps drawing the pixels of a DDP frame after
	// the last datagram of it. It is realtimeTimeoutMs in the firmware, 2500 on both of this
	// operator's controllers, and it is what pixelRepeat is chosen against.
	realtimeTimeout = 2500 * time.Millisecond

	// pixelRepeat is how often a picture that is being held is pushed again: once a second,
	// for the length of the hold.
	//
	// A still picture needs only enough repeats to survive realtimeTimeout, and one a second
	// gives two and a half times that margin while tolerating one late or lost repeat. The
	// arithmetic behind the choice is in the package comment above; the short of it is that
	// this is 35 datagrams a second of identical bytes, which is what a held picture costs and
	// the least that will keep the panel drawing it.
	pixelRepeat = time.Second

	// handbackMargin is how long past realtimeTimeout the hand-back waits before it reads what
	// the controller says. The timeout is the firmware's number and this module's clock is not
	// the same clock, so the read is taken a quarter of a second after the last moment the
	// controller could still be drawing.
	handbackMargin = 250 * time.Millisecond
)

// pixelPush is one controller's pixel transport, as this package uses it.
//
// It is an interface for the reason strip is one: a test watches the frames without a socket.
// Send takes the whole display in the controller's own order — pixel (x, y) is y*width+x — and
// Close releases the socket it opened.
type pixelPush interface {
	Send(frame []wled.RGB) error
	Close() error
}

// dialPixels opens the pixel transport for one controller's address, which is the base URL the
// operator set, and for a display of that many pixels.
//
// It is a separate function so that writer.dialPixels can be replaced in a test, and it is
// called once per writer and not once per frame: a socket opened a second is a socket closed a
// second.
func dialPixels(address string, pixels int) (pixelPush, error) {
	target, err := pixelTarget(address)
	if err != nil {
		return nil, err
	}

	return ddp.New(target, pixels)
}

// pixelTarget turns the controller's base URL into the "host:port" DDP is addressed to.
//
// The port is not a setting and is not read from the controller: WLED starts its DDP receiver
// in the firmware on 4048 whatever else is configured. The host is the one the operator named,
// so a module pointed at a second panel pushes at that panel — the address comes from the same
// setting the JSON client was built from and cannot drift from it.
func pixelTarget(address string) (string, error) {
	trimmed := strings.TrimSpace(address)
	if trimmed == "" {
		return "", fmt.Errorf("%w: the controller's address is not known", errNoPixelTarget)
	}

	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("%w: %q is not an address: %v", errNoPixelTarget, address, err)
	}

	host := parsed.Hostname()
	if host == "" {
		return "", fmt.Errorf("%w: %q names no host", errNoPixelTarget, address)
	}

	return net.JoinHostPort(host, strconv.Itoa(ddp.DefaultPort)), nil
}

// errNoPixelTarget is why a controller's address could not be turned into a DDP target. The
// address is a setting, so this is a configuration mistake and it is said as one rather than
// sent as a datagram to nowhere.
var errNoPixelTarget = errors.New("the controller's address cannot be pushed to")

// wordFrame lays the word out as the whole panel, in the controller's own pixel order: the
// event's colour where the rasteriser lit a pixel, and the background everywhere else.
//
// The whole panel and not the rows the word sits in, because a datagram names where its pixels
// go and nothing else: the pixels outside the band would keep whatever the live buffer held
// from the frame before, which on the first push of a hold is the steady picture the panel was
// showing. Naming all 16,384 pixels costs sixteen more datagrams than a band of 35 rows and
// leaves nothing on the panel that this module did not decide.
func wordFrame(geometry panel, lit []int, colour wled.RGB) []wled.RGB {
	frame := make([]wled.RGB, geometry.width*geometry.height)

	for index := range frame {
		frame[index] = finishedWordBackground
	}

	for _, index := range lit {
		if index >= 0 && index < len(frame) {
			frame[index] = colour
		}
	}

	return frame
}

// pixelSender is the transport for this writer's controller, opened on first use.
//
// One socket per writer for the writer's whole life, and the writer's own goroutine is the only
// caller: a socket opened and closed per event would be a socket per minute on a light that is
// meant to be quiet, and it would need a lock the rest of this file does not.
func (w *writer) pixelSender(geometry panel) (pixelPush, error) {
	if w.push != nil {
		return w.push, nil
	}

	dial := w.dialPixels
	if dial == nil {
		dial = dialPixels
	}

	push, err := dial(w.where, geometry.width*geometry.height)
	if err != nil {
		return nil, err
	}

	w.push = push

	return push, nil
}

// closePixels releases the socket, and is called on the way out of the writer's goroutine.
//
// Nothing is sent to say goodbye: a datagram that stops arriving is how this transport ends,
// and the controller is back on its own segments one realtime timeout later.
func (w *writer) closePixels() {
	if w.push == nil {
		return
	}

	_ = w.push.Close()
	w.push = nil
}

// pushWord puts one picture on the panel and returns when the controller has taken every
// datagram of it.
//
// A failed push is returned rather than retried here: the caller owns the retry, the log line
// and the hold, and a second retry path inside this one would be two answers to one question.
func (w *writer) pushWord(frame []wled.RGB) error {
	push, err := w.pixelSender(w.paintPanel())
	if err != nil {
		return err
	}

	return push.Send(frame)
}

// republish pushes the picture a hold is already showing again.
//
// This is the whole of what keeps a picture on the panel: the controller drops live mode after
// realtimeTimeout of silence, so a still picture is pushed again once a second for as long as it
// holds. For the word and for the patterns the bytes are the ones the hold started with — the
// picture does not move, which is why nothing here rebuilds it. A composed frame is the exception:
// its rotation is a function of the clock, so it is drawn again at the moment it goes out and the
// hold's own copy is replaced with what the panel now holds.
//
// A repeat is a write to the light, so it is recorded like any other write, through report. The
// rule it keeps is the one /status is read by: last_send answers "when did this module last write
// to this light", and a hold can last a minute — leaving the repeats out left the field standing
// at the instant the hold began, which is a change-age wearing a write-age label.
func (w *writer) republish(held *heldEvent) error {
	return w.report(func() error {
		push, err := w.pixelSender(w.paintPanel())
		if err != nil {
			return err
		}

		frame := held.frame

		if held.picture != nil {
			if frame, err = held.event.draw(w.paintPanel(), w.now()); err != nil {
				return err
			}

			held.frame = frame
		}

		return push.Send(frame)
	})
}

// until is how long the writer may wait before looking again: the earlier of the hold's end and
// the next repeat of the picture it is holding.
//
// A hold with no picture — one whose pixels never went out — waits only for its deadline, which
// is why the frame is checked for rather than trusted: a repeat of nothing would be a datagram a
// second carrying no word.
func (w *writer) until(held *heldEvent, wait time.Duration) time.Duration {
	if held.frame == nil {
		return wait
	}

	due := held.repush.Sub(w.now())
	if due < 0 {
		due = 0
	}

	if due < wait {
		return due
	}

	return wait
}

// confirmRealtime reads what the controller says about live mode, once, right after the picture's
// pixels have gone out.
//
// It is the only read-back this transport has. A datagram cannot be acknowledged and the
// firmware reports no per-LED colours, so "live=true" is the whole of what the device will say
// about the picture — and "live=false" is the one failure worth a line, because it means the
// panel is drawing its own segments and not the pixels just sent. The line names the picture by
// what it is — the word, the pattern's name, or "composed" — so that a pattern which did not take is
// not reported as the finished word.
func (w *writer) confirmRealtime(ctx context.Context, label, noun string) {
	info, err := w.light.Info(ctx)

	switch {
	case err != nil:
		w.logf("%s: the %q %s's pixels were pushed to %s and whether the panel took realtime cannot be read: %v",
			name, label, noun, w.where, err)
	case !info.Live:
		w.logf("%s: the %q %s's pixels were pushed to %s and the controller reports live=false: the panel is drawing its own segments and not the %s",
			name, label, noun, w.where, noun)
	}
}

// confirmHandback proves the light went back to its own segments after an event that held it.
//
// It runs after the steady frame has been written, because the frame is what the panel shows
// once live mode ends — and a JSON state write does not end live mode by itself: measured on
// this panel, live stayed true through a state write and went false about 2.5 s after the last
// datagram. So the wait is the firmware's own timeout, the read is taken past it, and the one
// line says what the controller answered. A controller that has not left live mode is said so
// plainly: it is not a failure of the frame, but it is the fact that the frame is not on the
// panel yet.
//
// The line says "the picture" and not "the word" because a diagnostic pattern holds the panel
// through the same path, and the hold it ends is already gone by the time this runs: the label
// would have to be carried on the writer for one word of a log line, and which picture it was is
// on the lines either side of it.
func (w *writer) confirmHandback(ctx context.Context) {
	select {
	case <-ctx.Done():
		return
	case <-w.after(realtimeTimeout + handbackMargin):
	}

	info, err := w.light.Info(ctx)

	switch {
	case err != nil:
		w.logf("%s: the picture is off the panel and whether %s has left realtime cannot be read: %v",
			name, w.where, err)
	case info.Live:
		w.logf("%s: the picture is off the panel and %s still reports live=true, so the panel is still drawing the pixels: its own segments return when the realtime timeout runs out",
			name, w.where)
	default:
		w.logf("%s: the picture is off the panel and %s reports live=false: the light is back on its own segments",
			name, w.where)
	}
}
