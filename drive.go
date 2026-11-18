// The part that drives the light.
//
// One goroutine reads the snapshot the printer package holds and decides a frame
// from it; one goroutine writes frames to the controller. They are separate on
// purpose. WLED asks that calls to a controller be serialised — it has one small web
// server, and two writes in flight interleave in ways nothing on the wire promises —
// so there is exactly one writer. And that writer must not be whatever noticed the
// printer change: a write can block for the client's two-second timeout, and blocking
// there would stall the reader for as long as the light took to answer.
//
// Between the two sits a slot that holds one frame. A frame that arrives while the
// writer is busy replaces the one waiting, so a slow controller is never handed a
// queue of stale pictures to work through: it gets the newest frame or nothing.
package main

import (
	"context"
	"fmt"
	"math"
	"os"
	"sync/atomic"
	"time"

	"github.com/nothingmn/wled-printer-status/internal/printer"
	"github.com/nothingmn/wled-printer-status/internal/render"
	"github.com/nothingmn/wled-printer-status/internal/wled"
)

const (
	// tickEvery is how often the module looks at the snapshot it holds.
	//
	// PROVISIONAL. The window the bus delivers a batch in is one second
	// (printer.DefaultWindowMS), and printer folds each window into its state as it
	// arrives; what it does not offer is a way to be told that a window arrived, and
	// Snapshot is the whole of what it exposes. So the trigger is a tick rather than
	// the window itself, and one second is the closest a tick can sit to it: a window
	// is turned into a frame within a second of landing, well inside the five seconds
	// a state change is allowed to take to reach the light. Reading a snapshot costs
	// a mutex and no I/O, so the tick is not a load on anything. When printer grows a
	// notification this constant is what goes.
	tickEvery = time.Second

	// infoRetryEvery is how often the controller is asked again for its LED count
	// while it has not answered. PROVISIONAL. A controller that is unplugged at start
	// must be picked up when it comes back, and a controller that is never plugged in
	// must not be asked once a second for the life of the process, so the retry is
	// slower than the tick and fast enough that nobody watching the light notices.
	infoRetryEvery = 10 * time.Second
)

// logf is where this module's own words go. The host copies the process's
// standard error into its log, which is the only place a person sees this process.
func logf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
}

// strip is the part of *wled.Client that drives a light. It is an interface so
// that a test can watch what was sent without a controller.
type strip interface {
	Info(ctx context.Context) (wled.Info, error)
	SetBrightness(ctx context.Context, brightness int) error
	SetPower(ctx context.Context, on bool) error
	SetSolid(ctx context.Context, colour wled.RGB, opts ...wled.SolidOption) error
	SetRange(ctx context.Context, first, count int, colour wled.RGB, opts ...wled.RangeOption) error
}

// writer sends the newest frame it has been handed to one controller, and
// nothing else.
//
// It keeps a single slot, not a queue: submit stores the newest frame and rings a
// bell that holds one ring. A frame still waiting when the next arrives is
// overwritten. That is what "latest wins" means here — the light is told what the
// printer is doing now, and a frame nobody has drawn yet is not worth drawing once a
// newer one exists.
type writer struct {
	light      strip
	brightness int
	logf       func(format string, args ...any)
	now        func() time.Time

	latest atomic.Pointer[render.Frame]
	wake   chan struct{}
}

// newWriter prepares a writer for one controller. It sends nothing until run is
// called, and run is the only thing that ever talks to the controller.
func newWriter(light strip, brightness int, logf func(format string, args ...any)) *writer {
	if logf == nil {
		logf = func(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...) }
	}

	return &writer{
		light:      light,
		brightness: brightness,
		logf:       logf,
		now:        time.Now,
		wake:       make(chan struct{}, 1),
	}
}

// submit offers one frame to the writer and never waits for it. The newest frame
// replaces whatever was waiting; the ring is dropped when one is already pending,
// because a pending ring already means "look at the slot".
func (w *writer) submit(frame render.Frame) {
	w.latest.Store(&frame)

	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// run writes frames until ctx ends. It is the only goroutine that talks to the
// controller.
func (w *writer) run(ctx context.Context) {
	w.settle(ctx)

	// last is what this goroutine last delivered. It moves only after a write
	// succeeded, so a write that failed is tried again on the next identical frame
	// instead of being suppressed as "already shown".
	var last *render.Frame

	for {
		select {
		case <-ctx.Done():
			return
		case <-w.wake:
		}

		frame := w.latest.Swap(nil)
		if frame == nil {
			continue
		}

		if last != nil && samePicture(*last, *frame) {
			continue
		}

		if err := w.paint(ctx, *frame); err != nil {
			if ctx.Err() != nil {
				return
			}

			// A write that failed is a light that is showing something older than
			// the printer is. It is reported and survived: the host stops a process that
			// misses three pings, and a module that exited over one unreachable controller
			// would be a status display that needs a person to switch it back on.
			w.logf("%s: the light was not updated: %v", name, err)
			recordSend(sendView{At: w.now().UTC().Format(time.RFC3339Nano), OK: false, Error: err.Error()})

			continue
		}

		last = frame
		recordSend(sendView{At: w.now().UTC().Format(time.RFC3339Nano), OK: true})
	}
}

// settle writes the brightness this instance runs at, once, and makes sure the
// strip is on.
//
// The brightness is written here and nowhere else. The operator sets it, and this
// module does not move it afterwards — not for a state change, not for an animation,
// not for the time of day. The power write is the other half of never leaving a strip
// dark: a controller somebody switched off would keep its colours and show none of
// them.
//
// Both are best effort. A brightness or power write that failed is logged and the
// frames follow anyway, because colour writes do not depend on either and a module
// that stopped here would show nothing at all.
func (w *writer) settle(ctx context.Context) {
	if err := w.light.SetBrightness(ctx, w.brightness); err != nil {
		w.logf("%s: the light's brightness was not set: %v", name, err)
	}

	if err := w.light.SetPower(ctx, true); err != nil {
		w.logf("%s: the light was not switched on: %v", name, err)
	}
}

// paint draws one frame on the strip.
//
// A frame with nothing filled is the whole range in the state's colour: an idle
// strip is yellow, not dark, and a printer that went quiet is dim red. A frame with
// a fill is the fill first and then the remainder, so the segment ends up covering
// the whole strip however narrow a previous write left it.
func (w *writer) paint(ctx context.Context, frame render.Frame) error {
	if frame.Lit <= 0 {
		return w.light.SetSolid(ctx, frame.Colour, wled.WithSolidLEDCount(frame.Total))
	}

	if err := w.light.SetRange(ctx, 0, frame.Lit, frame.Colour,
		wled.WithRangeLEDCount(frame.Total)); err != nil {
		return err
	}

	if rest := frame.Total - frame.Lit; rest > 0 {
		return w.light.SetRange(ctx, frame.Lit, rest, frame.Unfilled(),
			wled.WithRangeLEDCount(frame.Total))
	}

	return nil
}

// samePicture reports whether two frames would show the same thing, which is what
// decides that no write is needed.
//
// Equality is the colour, the lit count and the total. The first two are what a
// person sees — a strip showing green with 24 of 48 lit says the same thing whichever
// rule decided it — and the total is how long the range is: a frame decided before the
// controller had said how many LEDs it drives carries the colour and no fill, and
// painting it again once the length is known is the difference between a bar and no
// bar. The reason is deliberately not compared. Two frames with the same colour and
// the same fill need no write even when different rules produced them, and the reason
// is for a log line and for the status route, not for the light.
func samePicture(a, b render.Frame) bool {
	return a.Colour == b.Colour && a.Lit == b.Lit && a.Total == b.Total
}

// readings is what the driver reads each tick. *printer.Source satisfies it.
type readings interface {
	Snapshot() printer.Snapshot
}

// driver is the goroutine that turns the printer's state into frames.
type driver struct {
	source readings
	writer *writer
	light  strip
	logf   func(format string, args ...any)
	tick   time.Duration

	// leds is how many LEDs the controller drives, learned from the controller
	// itself rather than assumed. Until it is known a frame carries the colour and no
	// fill, which is all a strip of unknown length can honestly show.
	leds       int
	infoAt     time.Time
	infoFailed bool
}

// newDriver prepares the reader for one watch. It sends nothing until run is
// called.
func newDriver(source readings, pen *writer, light strip, logf func(format string, args ...any)) *driver {
	if logf == nil {
		logf = func(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...) }
	}

	return &driver{source: source, writer: pen, light: light, logf: logf, tick: tickEvery}
}

// run decides a frame on every tick until ctx ends and hands it to the writer.
func (d *driver) run(ctx context.Context) {
	ticker := time.NewTicker(d.tick)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case at := <-ticker.C:
			d.learn(ctx, at)

			snapshot := d.source.Snapshot()
			frame := render.Decide(snapshot, at, render.Config{LEDs: d.leds})

			publish(snapshot, frame)
			d.writer.submit(frame)
		}
	}
}

// learn asks the controller how many LEDs it drives, once, and again only while it
// has not answered.
//
// The length is the controller's own fact and no part of the printer's, so it is
// read from the device rather than from a setting. It is read here, on the driver's
// goroutine, and never in plugin.configure: a controller that is unplugged at start
// would otherwise refuse the configuration, and being unplugged is not a reason for
// a module to refuse to run.
func (d *driver) learn(ctx context.Context, at time.Time) {
	if d.leds > 0 {
		return
	}

	if !d.infoAt.IsZero() && at.Sub(d.infoAt) < infoRetryEvery {
		return
	}

	d.infoAt = at

	info, err := d.light.Info(ctx)
	if err != nil {
		if !d.infoFailed {
			d.infoFailed = true
			d.logf("%s: the controller did not say how many LEDs it drives: %v", name, err)
		}

		return
	}

	d.infoFailed = false
	d.leds = info.LEDCount

	if d.leds > 0 {
		d.logf("%s: the controller drives %d LEDs", name, d.leds)
	}
}

// view is what this module currently thinks: the printer, how old its reading is,
// and the frame decided from it.
//
// It is published as one value rather than as three, so that a reader never sees a
// frame from one tick beside the age of a reading from another.
type view struct {
	// Printer is the printer being watched, as the snapshot describes it.
	Printer printerView `json:"printer"`
	// Reading is when the printer last said anything and how long ago that was.
	Reading readingView `json:"reading"`
	// Frame is what the light is being asked to show.
	Frame frameView `json:"frame"`
}

// statusAnswer is the body of GET /status: what this module thinks, and what
// happened the last time it tried to tell the light.
type statusAnswer struct {
	Printer  printerView `json:"printer"`
	Reading  readingView `json:"reading"`
	Frame    frameView   `json:"frame"`
	LastSend *sendView   `json:"last_send"`
}

// printerView is the printer as a caller of the route reads it.
type printerView struct {
	// ID and Name are the printer's identity: the host's id, which survives a
	// reconnect, and the name an operator gave it.
	ID   string `json:"id"`
	Name string `json:"name"`
	// Connected is the host's answer about a session right now.
	Connected bool `json:"connected"`
	// Retired reports that the id no longer names a machine.
	Retired bool `json:"retired"`
	// State is the last state the printer reported, and StateKnown says whether
	// this build had a reading of the word. An unknown word is not "unknown state":
	// the printer said something this build does not recognise.
	State      string `json:"state"`
	StateKnown bool   `json:"state_known"`
}

// readingView is how old the newest fact about the printer is. A person reading
// the route can tell a live strip from a memory without watching the light.
type readingView struct {
	// LastChange is when anything about the printer last moved, in UTC, and empty
	// when nothing has been reported yet.
	LastChange string `json:"last_change"`
	// ObservedAt is when the reading behind Frame was taken, in UTC.
	ObservedAt string `json:"observed_at"`
	// AgeSeconds is how old LastChange was at ObservedAt, to the millisecond.
	AgeSeconds float64 `json:"age_seconds"`
	// Stale reports that the newest fact is older than printer.StaleAfter, so no
	// reading describes the printer now.
	Stale bool `json:"stale"`
}

// frameView is render.Frame as a caller of the route reads it. The fields are the
// frame's, one for one, named for a reader who has not read the Go.
type frameView struct {
	// Colour is the colour of the whole range.
	Colour colourView `json:"colour"`
	// Lit is how many LEDs, counted from the first, carry the completed part of
	// the job. Zero means the whole range is Colour.
	Lit int `json:"lit"`
	// Total is how many LEDs the range holds, and zero until the controller has
	// said.
	Total int `json:"total"`
	// Reason names the rule that decided the frame.
	Reason string `json:"reason"`
}

// colourView is one colour, one byte per channel.
type colourView struct {
	R uint8 `json:"r"`
	G uint8 `json:"g"`
	B uint8 `json:"b"`
}

// sendView is what the last attempt to write to the light did.
//
// It is what makes the route worth reading when the light and the module disagree:
// a frame that keeps failing to reach a controller is a working module with a broken
// light, and a frame that reached a controller that is now unplugged is a light
// showing something older than the reading beside it.
type sendView struct {
	// At is when the attempt was made, in UTC.
	At string `json:"at"`
	// OK reports that the controller took the frame.
	OK bool `json:"ok"`
	// Error is what went wrong, and empty when nothing did.
	Error string `json:"error,omitempty"`
}

// What the driver and the writer publish for the status route to read.
//
//nolint:gochecknoglobals // the one thing the route reads back out of the watch.
var (
	current atomic.Pointer[view]
	sent    atomic.Pointer[sendView]
)

// publish replaces what the module says it thinks, in one store.
func publish(snapshot printer.Snapshot, frame render.Frame) {
	current.Store(&view{
		Printer: printerView{
			ID:         snapshot.ID,
			Name:       snapshot.Name,
			Connected:  snapshot.Connected,
			Retired:    snapshot.Retired,
			State:      snapshot.State.Value,
			StateKnown: snapshot.State.Known,
		},
		Reading: readingView{
			LastChange: when(snapshot.LastChange),
			ObservedAt: when(snapshot.ObservedAt),
			AgeSeconds: math.Round(snapshot.Age.Seconds()*1000) / 1000,
			Stale:      snapshot.Stale,
		},
		Frame: frameView{
			Colour: colourView{R: frame.Colour.R, G: frame.Colour.G, B: frame.Colour.B},
			Lit:    frame.Lit,
			Total:  frame.Total,
			Reason: string(frame.Reason),
		},
	})
}

// recordSend publishes what one write to the light did.
func recordSend(report sendView) {
	sent.Store(&report)
}

// when is a moment as the route prints it, and empty for a moment that never
// happened: an empty string is not a 1970 the reader would have to notice.
func when(at time.Time) string {
	if at.IsZero() {
		return ""
	}

	return at.UTC().Format(time.RFC3339Nano)
}
