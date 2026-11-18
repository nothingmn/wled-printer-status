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
//
// The writer also keeps a floor under the picture: a frame it has not written for
// heartbeatEvery is written again even though nothing about it changed. A controller
// that rebooted came back in its own default state, and without that repaint a strip
// whose printer has nothing to say — idle overnight, a finished job, a paused machine
// — would stay wrong until something else moved. It is a floor and not an addition:
// see heartbeatEvery for why it cannot raise the traffic of a moving picture.
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

	// heartbeatEvery is the longest the writer lets a frame stand unwritten before
	// painting it again, unchanged.
	//
	// A controller that loses power, is unplugged, or reboots its own firmware comes
	// back in whatever state it saved for itself — not in the frame this module was
	// showing. The writer suppresses a frame whose colour, fill and length equal the
	// last one the strip took, which is right for traffic and wrong here: if the
	// printer's picture does not change, nothing is ever sent, and the light stays
	// wrong until something else moves. Idle overnight, a finished job and a paused
	// machine are all that case.
	//
	// This is a floor, not an addition, and that is the whole design: it is measured
	// from the last write the strip took, of any kind, successfully. While a print is
	// moving the ordinary writes land more often than this and reset the clock, so
	// the heartbeat never fires and adds no traffic at all. It fires only when the
	// picture has stopped moving, where it costs one repaint a minute — the low end of
	// the write budget rather than a stream.
	heartbeatEvery = 60 * time.Second

	// backoffFloor is how long the writer waits before trying the light again after
	// one failed write, and backoffCap is the longest it will ever wait between
	// attempts. Each failure in a row doubles the wait, to the cap.
	//
	// These exist because of what the installed module did to a real controller: a
	// write that failed was never recorded as sent, so the next second's frame retried
	// it, and the next, against a 16,384-pixel panel that was already struggling. The
	// log was one line per attempt, and the host answered a ping in 3 ms throughout.
	// That is self-inflicted congestion, and a device in that state cannot recover
	// while it is being hammered.
	//
	// The floor is a second because one failure is usually a blip. The cap is half a
	// minute: long enough that an hour-long outage costs a hundred-odd attempts rather
	// than three and a half thousand, short enough that a controller switched back on
	// is found again in half a minute.
	backoffFloor = time.Second
	backoffCap   = 30 * time.Second

	// firstSegment and remainderSegment are the two segments a frame is written as. The
	// controller draws them in order, so the fill is 0 and the unfilled part is 1; both
	// travel in one request, because one segment cannot hold two spans.
	firstSegment     = 0
	remainderSegment = 1
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
	SetSegments(ctx context.Context, segments []wled.SegmentWrite) error
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

	// after is how the writer waits. It is time.After in the process; a test hands
	// it a clock it can move, because a test about a minute-long heartbeat cannot
	// spend a minute waiting for one.
	after func(time.Duration) <-chan time.Time

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
		after:      time.After,
		wake:       make(chan struct{}, 1),
	}
}

// submit offers one frame to the writer and never waits for it. The newest frame
// replaces whatever was waiting; the ring is dropped when one is already pending,
// because a pending ring already means "look at the slot".
func (w *writer) submit(frame render.Frame) {
	if w.latest.Swap(&frame) != nil {
		// A frame was already waiting, and the ring that put it there is still pending: a
		// second ring would buy nothing and would outlive the look it asked for.
		return
	}

	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// run writes frames until ctx ends. It is the only goroutine that talks to the
// controller.
func (w *writer) run(ctx context.Context) {
	w.settle(ctx)

	// last is what this goroutine last delivered, and lastOK is when the strip took
	// it. Both move only after a write succeeded, so a write that failed is tried
	// again on the next identical frame instead of being suppressed as "already
	// shown" — and, for the same reason, a failed write leaves the heartbeat's clock
	// where it was, because that clock is the last write that actually landed.
	var (
		last     *render.Frame
		lastOK   time.Time
		failures int
		retryAt  time.Time
	)

	for {
		// A failed write opens a window in which nothing is sent, however much the
		// printer has to say. This is the difference between a retry and a storm: a
		// controller that is answering slowly, or not at all, is tried once per window
		// instead of once per tick, and a picture that changed while the window is open
		// waits in the slot rather than being added to a pile of attempts.
		//
		// The window gates the heartbeat too. The heartbeat is why a rebooted controller
		// heals, not a reason to keep knocking while the controller is down.
		if wait := retryAt.Sub(w.now()); !retryAt.IsZero() && wait > 0 {
			select {
			case <-ctx.Done():
				return
			case <-w.after(wait):
			}

			continue
		}

		// Nothing is in the way, so anything the driver has decided goes out now, newest
		// first: a frame that arrived during the window is held here, in the one slot,
		// and never queued.
		//
		// The ring is taken with it. It is a hint that the slot may hold something, and
		// this is the look it asked for; left behind it would ring for a frame already in
		// hand, and the pass it woke would abandon the wait armed below and arm it again.
		select {
		case <-w.wake:
		default:
		}

		frame := w.latest.Swap(nil)

		forced := false

		if frame == nil {
			if last == nil {
				// Nothing has ever reached the strip and nothing is waiting: there is
				// nothing to repaint, and a frame of this writer's own invention would be
				// a picture nobody decided.
				select {
				case <-ctx.Done():
					return
				case <-w.wake:
				}

				continue
			}

			select {
			case <-ctx.Done():
				return
			case <-w.wake:
				continue
			case <-w.after(w.beatIn(lastOK)):
			}

			// Nothing about this frame has changed and it is sent again anyway: a
			// controller that rebooted is showing its own saved state, and a picture
			// that never moves would otherwise never correct it. This is the same
			// single writer sending the same frame — no second goroutine, no re-reading
			// the printer, no power or brightness write.
			frame, forced = last, true
		}

		if !forced && last != nil && samePicture(*last, *frame) {
			continue
		}

		if err := w.deliver(ctx, *frame); err != nil {
			if ctx.Err() != nil {
				return
			}

			failures++

			// A write that failed is a light showing something older than the printer is,
			// and it is survived rather than fatal: the host stops a process that misses
			// three pings, and a module that exited over one unreachable controller would
			// be a status display that needs a person to switch it back on.
			wait := backoffFor(failures)
			retryAt = w.now().Add(wait)

			w.logFailure(failures, wait, err)

			continue
		}

		if failures > 0 {
			w.logf("%s: the light is answering again after %d failed writes", name, failures)
		}

		failures, retryAt = 0, time.Time{}
		last, lastOK = frame, w.now()
	}
}

// beatIn is how long the writer may wait before the frame it is showing is due a
// repaint: one interval from the last write that landed.
//
// It is measured from that write and not from "the last thing that happened", which is
// the difference between a floor and an addition. Every write that succeeds — a new
// picture, or an earlier heartbeat — pushes the deadline out, so a print whose picture
// keeps moving keeps the heartbeat unarmed and costs nothing at all.
func (w *writer) beatIn(lastOK time.Time) time.Duration {
	if wait := lastOK.Add(heartbeatEvery).Sub(w.now()); wait > 0 {
		return wait
	}

	return 0
}

// backoffFor is how long the writer waits before trying the light again after
// failures failed writes in a row: the floor, then doubling, to the cap.
//
// The floor is a tick's length because one failed write is usually the controller
// blinking, and the next frame is worth trying a second later. The cap is half a
// minute because a controller switched off for an hour must not be knocked on three
// hundred times, and must still be found within half a minute of being switched back
// on. The cap is also what makes an outage's cost bounded: five minutes of it is a
// dozen attempts, not three hundred.
func backoffFor(failures int) time.Duration {
	if failures < 1 {
		return 0
	}

	wait := backoffFloor
	for i := 1; i < failures && wait < backoffCap; i++ {
		wait *= 2
	}

	if wait > backoffCap {
		return backoffCap
	}

	return wait
}

// logFailure reports a failed write, and stops reporting it on every attempt.
//
// A controller that is down fails on every attempt, and one line per attempt is a log
// nobody can read and a disk that fills: at a tick a second, a five-minute outage wrote
// three hundred lines, and the operator found it by reading them. So the first failure
// is reported, then only the ones that move the backoff to a longer wait — the failures
// that change what the module is doing — and then once when the light answers again.
// The same five-minute outage now writes six lines.
func (w *writer) logFailure(failures int, wait time.Duration, err error) {
	if failures > 1 && backoffFor(failures) == backoffFor(failures-1) {
		return
	}

	w.logf("%s: the light was not updated (failure %d, next try in %s): %v", name, failures, wait, err)
}

// deliver paints one frame and reports what happened to the status route.
//
// It is the writer's only write of a frame, whether that frame is new or a heartbeat
// repaint of the last one, so a repaint is recorded like any other write. A frame that
// failed is recorded as failed and is never reported as taken: a person reading the
// route must not be told the light is current when the light did not answer.
func (w *writer) deliver(ctx context.Context, frame render.Frame) error {
	if err := w.paint(ctx, frame); err != nil {
		recordSend(sendView{At: w.now().UTC().Format(time.RFC3339Nano), OK: false, Error: err.Error()})

		return err
	}

	recordSend(sendView{At: w.now().UTC().Format(time.RFC3339Nano), OK: true})

	return nil
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
// It is one request, whatever the frame, and it carries both segments. Not two requests,
// for two reasons: two requests leave a window in which the panel is showing half a frame
// — this frame's fill underneath the previous frame's remainder — and they double the
// traffic to a device already known to be slow. It is also not optional. Every write this
// module makes addresses one segment, so a fill written on its own and then a remainder
// written on its own are one segment rather than two: the second takes the first one's
// place, and the pixels the fill covered belong to no segment and are never drawn.
//
// That is not a theory. A 256x64 panel held one segment covering pixels 10158 to 16384 in
// black — exactly the unfilled part — and the 10158 pixels the module had painted green
// belonged to nothing, so the panel was dark while the module logged every send as taken.
func (w *writer) paint(ctx context.Context, frame render.Frame) error {
	return w.light.SetSegments(ctx, segmentsFor(frame))
}

// segmentsFor is the body one frame becomes: segment 0 the part of the range the job has
// completed, segment 1 the rest.
//
// Three shapes, and each one written for a reason:
//
//   - nothing filled: segment 0 covers the whole range in the frame's colour, and segment
//     1 is switched off. A frame with no fill is the whole range in one colour, not a range
//     with a black tail, and an idle strip must not keep a bar from the frame before it.
//   - everything filled: the same, and for the same reason in the other direction — there
//     is no remainder, and a stale remainder segment would be a black band across a
//     finished job.
//   - anything between: two segments, and both are written even when only the colour
//     moved, because in that case the fill's colour is the thing that changed.
func segmentsFor(frame render.Frame) []wled.SegmentWrite {
	if frame.Lit <= 0 || frame.Lit >= frame.Total {
		return []wled.SegmentWrite{
			{ID: firstSegment, First: 0, Count: frame.Total, On: true, Colour: frame.Colour},
			{ID: remainderSegment, On: false},
		}
	}

	return []wled.SegmentWrite{
		{ID: firstSegment, First: 0, Count: frame.Lit, On: true, Colour: frame.Colour},
		{
			ID:     remainderSegment,
			First:  frame.Lit,
			Count:  frame.Total - frame.Lit,
			On:     true,
			Colour: frame.Unfilled(),
		},
	}
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
