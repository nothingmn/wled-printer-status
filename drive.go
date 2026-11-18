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
	"strings"
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

// The shape a frame takes on a panel: the columns from the first to a boundary, across every
// row, are the fill, and the columns from the boundary to the last are the remainder.
//
// PROVISIONAL, and chosen by this seat rather than settled with the operator. A bar that
// grows left to right is the arrangement that reads as progress from across a room, and it is
// what a person would expect a progress light to do; it is not a decision anyone asked for on
// a 256x64 panel, where a paused job, an error and a finished one all have to be told apart by
// something and a bar alone cannot do it. The constant exists so that the layout the operator
// eventually chooses is one line and one function, not a shape spread through the painter:
// nothing else in this module knows what the shape is.
const (
	// fillAcrossColumns measures the fill in columns, over the panel's width.
	fillAcrossColumns = iota
)

// panelShape is the shape every frame takes on a matrix.
const panelShape = fillAcrossColumns

// panel is a controller's two-dimensional geometry: how many pixels wide its matrix is and
// how many rows it has.
//
// The zero value is a controller that drives one run of pixels, which is what a strip is, and
// that is the shape the 1D path writes. Both dimensions are needed before a controller counts
// as a panel: WLED publishes "leds.matrix" only on a build configured as one, and a half-read
// geometry is a strip.
type panel struct {
	width  int
	height int
}

// matrix reports whether the controller has a matrix at all.
func (p panel) matrix() bool { return p.width > 0 && p.height > 0 }

// logf is where this module's own words go. The host copies the process's
// standard error into its log, which is the only place a person sees this process.
func logf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
}

// strip is the part of *wled.Client that drives a light. It is an interface so
// that a test can watch what was sent without a controller.
//
// State and RestoreSegment are here for the borrow: a module that drives one segment of a
// controller somebody else also uses has to read what that segment was and put it back
// afterwards. Both are unused when this module drives the whole strip.
type strip interface {
	Info(ctx context.Context) (wled.Info, error)
	State(ctx context.Context) (wled.State, error)
	SetBrightness(ctx context.Context, brightness int) error
	SetPower(ctx context.Context, on bool) error
	SetSegments(ctx context.Context, segments []wled.SegmentWrite) error
	RestoreSegment(ctx context.Context, segment wled.Segment) error
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

	// segment names the one segment of a multi-segment controller this writer drives, and
	// nil means the whole strip, which is how this module has always driven a light.
	//
	// It is a pointer rather than a number because 0 is a segment an operator may
	// legitimately name, so there is no number left over to mean "none".
	segment *int

	// where is the controller's address, carried only so the borrow and the give-back can
	// say which controller they were about. A person reading the log has a desk light and a
	// panel on the same network, and "segment 1" alone does not say whose.
	where string

	// borrowed is the nominated segment exactly as the controller was holding it when this
	// module read it, and nil when nothing has been borrowed.
	//
	// It needs no lock: it is written in begin and read by paint, and both run on this
	// writer's own goroutine — the one goroutine that talks to the controller.
	borrowed *borrowed

	// borrowErr is why the nominated segment could not be borrowed. A frame that has nothing
	// to draw on is not written and this is what it reports, which is what makes the status
	// route say the light was not updated instead of claiming it was.
	borrowErr error

	// after is how the writer waits. It is time.After in the process; a test hands
	// it a clock it can move, because a test about a minute-long heartbeat cannot
	// spend a minute waiting for one.
	after func(time.Duration) <-chan time.Time

	// geometry is the matrix the controller reported, and the zero panel for a strip. The
	// driver's goroutine stores it the moment Info answers and the writer's reads it on
	// every frame, so it is an atomic pointer rather than a field two goroutines would
	// race on. Nothing else about a frame depends on it: the colour, the fill and the
	// total are the printer's, and this is only how they are laid out.
	geometry atomic.Pointer[panel]

	latest atomic.Pointer[render.Frame]
	wake   chan struct{}
}

// newWriter prepares a writer for one controller's whole strip. It sends nothing until run
// is called, and run is the only thing that ever talks to the controller.
func newWriter(light strip, brightness int, logf func(format string, args ...any)) *writer {
	pen := prepare(light, logf)
	pen.brightness = brightness

	return pen
}

// newSegmentWriter prepares a writer that drives one nominated segment of a controller it
// shares with other segments, and gives that segment back when the writer stops.
//
// It takes no brightness, and that is not an oversight: a brightness is a controller-wide
// setting, so a writer in this mode must never write one, and a constructor that could not
// carry a brightness is one fewer place for that rule to be broken. See begin for the whole
// of it.
func newSegmentWriter(light strip, segment int, where string, logf func(format string, args ...any)) *writer {
	pen := prepare(light, logf)
	pen.segment = &segment
	pen.where = where

	return pen
}

// prepare builds the writer both constructors start from.
func prepare(light strip, logf func(format string, args ...any)) *writer {
	if logf == nil {
		logf = func(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...) }
	}

	return &writer{
		light: light,
		logf:  logf,
		now:   time.Now,
		after: time.After,
		wake:  make(chan struct{}, 1),
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
//
// It gives back whatever it borrowed on the way out, whatever ended it: the host's stop,
// a second configure replacing this watch, or an interrupt.
func (w *writer) run(ctx context.Context) {
	w.begin(ctx)
	defer w.release()

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

// begin is what the writer does once, before the first frame, and the two modes it has are
// the whole of what this module owes a controller it shares.
//
//   - driving the whole strip (segment nil): the brightness the operator set is written
//     once and the strip is switched on, as this module has always done. That is settle.
//   - driving one borrowed segment: **neither is written, here or at any frame.**
//
// The second is the trap and it is worth stating in full. SetBrightness and SetPower are
// controller-wide: they are not properties of a segment, they move every segment on that
// controller at once. A module that borrowed one segment of a desk light and settled as it
// always has would dim that light to the brightness meant for a status strip and switch on a
// light somebody had deliberately turned off — silently, once, at start, with the other five
// segments looking untouched. So this mode reads the controller's state, borrows the segment
// it was told to drive, and writes no brightness and no power at all.
//
// The structure is what keeps that true rather than a flag checked in five places: the only
// caller of SetBrightness and SetPower is settle, the only caller of settle is this function,
// and this function reaches it only when no segment was named. A writer built by
// newSegmentWriter has no brightness to write even if something asked it to.
func (w *writer) begin(ctx context.Context) {
	if w.segment != nil {
		w.borrow(ctx)

		return
	}

	w.settle(ctx)
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
	geometry := w.panelOf()

	if w.segment != nil {
		return w.paintBorrowed(ctx, frame, geometry)
	}

	return w.light.SetSegments(ctx, segmentsFor(frame, geometry))
}

// paintBorrowed draws one frame into the one segment this module borrowed, and into no
// other. Every body it sends names one id — the borrowed one — because a second would be one
// of the operator's own.
//
// A panel is refused rather than driven. The borrowing is a strip's: a segment there is a run
// of pixels and the run can be shortened every frame without moving anything outside it. On a
// panel this module's own body is two rectangles that cover the whole of it — segment 0 the
// fill, segment 1 the rest — and writing those two on a controller that carries somebody
// else's segments would be writing two of them. So the request is not carried out, and the log
// says why; nothing on the controller is touched, and what was borrowed is given back.
//
// The geometry is read on every frame rather than kept, for the reason setPanel gives: the
// driver stores it the moment the controller answers Info, and until then this is the zero
// panel, which is a strip.
func (w *writer) paintBorrowed(ctx context.Context, frame render.Frame, geometry panel) error {
	if geometry.matrix() {
		return w.refusePanel(geometry)
	}

	if w.borrowed == nil {
		// A controller that was unplugged at start is picked up when it answers, the same way
		// a frame is retried: the borrow is a read, and reads are cheap beside a strip that
		// never lights.
		w.borrow(ctx)

		if w.borrowed == nil {
			return w.borrowErr
		}
	}

	return w.light.SetSegments(ctx, w.borrowed.frame(frame))
}

// refusePanel records that the setting cannot be applied to the controller it was pointed at,
// and returns the reason so that the frame is reported as not sent. It says it once.
func (w *writer) refusePanel(geometry panel) error {
	err := fmt.Errorf("segment %d is named and %s is a %dx%d panel: a panel is drawn as two rectangles across the whole of it, and two rectangles on a controller carrying somebody else's segments would be two of theirs, so this module is driving nothing. Name no segment to draw the panel, or point this instance at a strip",
		*w.segment, w.where, geometry.width, geometry.height)
	w.setBorrowErr(err)

	return err
}

// borrowed is one segment of somebody else's controller, exactly as it was found, and this
// module's promise to give exactly that back.
//
// A controller remembers a segment's bounds: WLED keeps start and stop in its own state and
// writes them to its own flash. So a module that drew a bar by narrowing somebody's segment
// has changed that segment for good — the bounds survive a restart, a power cut and this
// module being uninstalled. That is why the segment is borrowed rather than taken: what was
// there is kept here, in memory, and written back when the module stops.
type borrowed struct {
	// Segment is the segment as the controller reported it: its run, its row bounds, whether it
	// was drawn, its colours, its effect, its name and its own brightness.
	wled.Segment
}

// borrow reads the controller's state once and captures the segment this module was told to
// drive. It logs one line saying what was taken and from where, or why nothing was.
func (w *writer) borrow(ctx context.Context) {
	if w.segment == nil {
		return
	}

	id := *w.segment

	state, err := w.light.State(ctx)
	if err != nil {
		w.setBorrowErr(fmt.Errorf("the segment %d could not be borrowed, so the light is not being driven: %w", id, err))

		return
	}

	for _, segment := range state.Segments {
		if segment.ID == id {
			held := borrowed{Segment: segment}
			w.borrowed = &held
			w.borrowErr = nil

			w.logf("%s: borrowed segment %d %q from %s: pixels %d-%d, colour %s, effect %d, brightness %s; its other segments and the controller's own brightness and power are not touched",
				name, id, segment.Name, w.where, segment.Start, segment.Stop,
				coloursPhrase(segment.Colors), segment.Effect, brightnessPhrase(segment.Brightness))

			return
		}
	}

	w.setBorrowErr(fmt.Errorf("the segment %d could not be borrowed: %s holds the segments %s and this module will not write a segment it did not read",
		id, w.where, segmentIDs(state.Segments)))
}

// setBorrowErr records why the nominated segment could not be borrowed, and says it once. A
// controller that is down fails every attempt, and one line per attempt is the log this module
// was already taught not to write.
func (w *writer) setBorrowErr(err error) {
	if w.borrowErr != nil && w.borrowErr.Error() == err.Error() {
		w.borrowErr = err

		return
	}

	w.borrowErr = err
	w.logf("%s: %v", name, err)
}

// frame is one frame as a body for the borrowed segment: one segment, the borrowed id, and the
// run inside the borrowed span the frame has filled.
func (b *borrowed) frame(frame render.Frame) []wled.SegmentWrite {
	lit := litInSpan(frame, b.Stop-b.Start)

	if lit <= 0 {
		// Nothing to show. The segment is switched off rather than painted black: an off
		// segment draws nothing, so the borrowed range is dark without this module naming a
		// colour nobody asked it for, and the segment keeps the bounds it has — a body with no
		// span leaves the geometry alone — so it can still be given back unchanged.
		return []wled.SegmentWrite{{ID: b.ID, On: false, Name: b.Name}}
	}

	return []wled.SegmentWrite{{
		ID:     b.ID,
		First:  b.Start,
		Count:  lit,
		On:     true,
		Colour: frame.Colour,
		// The name goes on every body. A controller clears a name from a segment written
		// without one, and a module that renamed the operator's zone as a side effect of
		// drawing in it would be doing exactly what it promised not to.
		Name: b.Name,
	}}
}

// litInSpan is how many pixels of a borrowed run carry the completed part of the job.
//
//   - A frame with no range at all — Total zero, which is what the driver holds until the
//     controller has said how many LEDs it drives — has nothing to show, and the answer is
//     nothing lit. The run is left dark rather than painted, which is the difference between a
//     zone that is off and a zone somebody made black.
//   - A frame with no completed part — Lit zero, which is an idle printer, a paused print at
//     zero and a job that has finished — is the whole run in the frame's colour, which is what
//     the strip does with the same frame: the range is one colour and there is no bar in it.
//   - Anything else is the frame's own fraction of the run, rounded, and never less than one
//     pixel: a job that has started and rounds away to nothing would be a zone that went dark
//     while the printer was working, which says the opposite of what is happening.
//
// It rounds rather than floors for the reason fillColumns rounds: a unit of the run is a
// fraction of the whole, and on a ten-pixel zone flooring would throw away most of a percent.
// The cost is that the last pixel fills fractionally early — 47 of 48 hundredths of the print
// is a full zone — and that is the choice this rule makes.
func litInSpan(frame render.Frame, span int) int {
	if span <= 0 || frame.Total <= 0 {
		return 0
	}

	if frame.Lit <= 0 {
		return span
	}

	lit := int(math.Round(float64(frame.Lit) / float64(frame.Total) * float64(span)))

	return min(max(lit, 1), span)
}

// release gives the borrowed segment back, exactly as it was found.
//
// It runs on the way out of the writer's goroutine, so it covers every stop this module can
// see: the host's shutdown call, a second configure replacing the watch, and the process
// catching an interrupt. It is not a promise about a process that is killed without warning —
// a SIGKILL, an out-of-memory kill, a power cut or a container stopped hard leaves the segment
// holding this module's last frame, and nothing here can write after that. The borrowed bounds
// are held in memory and nowhere else, so a module that comes back up reads the segment as the
// module left it and borrows that instead.
//
// The write goes out on a context of its own, because the one that ended the watch is already
// cancelled and a cancelled context fails the request before it is sent.
func (w *writer) release() {
	if w.borrowed == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), wled.DefaultTimeout)
	defer cancel()

	held := *w.borrowed

	if err := w.light.RestoreSegment(ctx, held.Segment); err != nil {
		w.logf("%s: the borrowed segment %d on %s was not given back: %v", name, held.ID, w.where, err)

		return
	}

	w.borrowed = nil

	w.logf("%s: gave segment %d %q back to %s: pixels %d-%d, colour %s, effect %d, brightness %s",
		name, held.ID, held.Name, w.where, held.Start, held.Stop,
		coloursPhrase(held.Colors), held.Effect, brightnessPhrase(held.Brightness))
}

// coloursPhrase names a segment's colours in a log line: one red,green,blue triple per colour,
// and "none" for a segment the controller reported without one.
func coloursPhrase(colors []wled.RGB) string {
	if len(colors) == 0 {
		return "none"
	}

	parts := make([]string, 0, len(colors))
	for _, color := range colors {
		parts = append(parts, fmt.Sprintf("%d,%d,%d", color.R, color.G, color.B))
	}

	return strings.Join(parts, " ")
}

// brightnessPhrase names a segment's own brightness, or says that it inherits the strip's.
func brightnessPhrase(level int) string {
	if level == 0 {
		return "inherited"
	}

	return fmt.Sprintf("%d", level)
}

// segmentIDs is every id a controller reported, for a log line that says what it does hold
// when the segment an operator named is not among them.
func segmentIDs(segments []wled.Segment) string {
	if len(segments) == 0 {
		return "none"
	}

	ids := make([]string, 0, len(segments))
	for _, segment := range segments {
		ids = append(ids, fmt.Sprintf("%d", segment.ID))
	}

	return strings.Join(ids, ", ")
}

// setPanel records the geometry the controller reported. It is called from the driver's
// goroutine the moment the controller answers Info, and it is what turns a frame from a run
// of pixels into a rectangle.
//
// A controller that reports no matrix leaves the zero panel in place, so a strip keeps the
// body it has always been given. A geometry that changes under a running writer — a
// controller swapped for a panel and asked again — does not happen in this module, because
// the LED count is learned once and never re-read; if that changes, the frame that crosses
// the change is the one to look at.
func (w *writer) setPanel(info wled.Info) {
	reported := panel{width: info.MatrixWidth, height: info.MatrixHeight}
	if !reported.matrix() {
		return
	}

	w.geometry.Store(&reported)
}

// panelOf is the geometry the next frame is written against.
func (w *writer) panelOf() panel {
	if held := w.geometry.Load(); held != nil {
		return *held
	}

	return panel{}
}

// segmentsFor is the body one frame becomes: segment 0 the part of the range the job has
// completed, segment 1 the rest.
//
// The shape is the controller's, not the frame's. A strip draws one run of pixels and a panel
// draws a rectangle, and the two are not the same body: a flat pixel index sent to a panel is
// clamped to the panel's width and folded onto one row, which is how 62 % of 16,384 pixels
// arrived as a single line of black across the top. stripSegments and panelSegments below are
// the two shapes, and each says what it is for.
func segmentsFor(frame render.Frame, geometry panel) []wled.SegmentWrite {
	if geometry.matrix() {
		return panelSegments(frame, geometry)
	}

	return stripSegments(frame)
}

// stripSegments is one frame on a controller that drives a single run of pixels: segment 0 the
// run the job has completed, segment 1 the rest.
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
func stripSegments(frame render.Frame) []wled.SegmentWrite {
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

// panelSegments is one frame on a controller that drives a matrix: two rectangles that
// together cover every pixel of the panel, so that neither a stale bar nor an unpainted band
// is left over from the frame before.
//
// The shape is the one panelShape names — the fill is the columns from the first to a
// boundary, spanning every row, and the remainder is the columns after it. Every segment that
// carries a span therefore carries all four bounds: columns 0..boundary over rows 0..height on
// the fill, the columns after it over the same rows on the remainder.
//
// The two edges are the same two the strip has, and they matter more here, because a panel
// keeps a segment's rectangle until something rewrites it. Nothing filled, and so nearly
// everything filled that the boundary reaches the last column: the whole panel in the frame's
// colour and the remainder switched off. Written as edges rather than as a boundary of zero or
// of the full width, because a rectangle with no width is a segment WLED deletes rather than a
// segment that draws nothing — and a deleted segment leaves the pixels it covered to whatever
// was under them.
//
// A segment that is switched off carries no bounds at all, which is what the 1D body does and
// what AGENTS.md §5.1 means by "if you write bounds, write four": an off segment draws nothing,
// so the rectangle it keeps cannot be seen, and any rectangle named for it would be a claim
// about where the fill ends that the frame does not make.
func panelSegments(frame render.Frame, geometry panel) []wled.SegmentWrite {
	columns := fillColumns(frame, geometry)

	if frame.Lit <= 0 || columns >= geometry.width {
		return []wled.SegmentWrite{
			{
				ID:       firstSegment,
				First:    0,
				Count:    geometry.width,
				FirstRow: 0,
				RowCount: geometry.height,
				On:       true,
				Colour:   frame.Colour,
			},
			{ID: remainderSegment, On: false},
		}
	}

	return []wled.SegmentWrite{
		{
			ID:       firstSegment,
			First:    0,
			Count:    columns,
			FirstRow: 0,
			RowCount: geometry.height,
			On:       true,
			Colour:   frame.Colour,
		},
		{
			ID:       remainderSegment,
			First:    columns,
			Count:    geometry.width - columns,
			FirstRow: 0,
			RowCount: geometry.height,
			On:       true,
			Colour:   frame.Unfilled(),
		},
	}
}

// fillColumns is how many of the panel's columns carry the completed part of the job: the
// frame's fraction of the panel's width, rounded to the nearest column.
//
// The fraction is the frame's own — Lit out of Total — and it is rounded rather than floored
// because a column is a twelfth of a row here: on a 256-wide panel one column is 64 pixels, so
// flooring would throw away most of a percent of the job and the bar would sit still while the
// printer was working. That is the opposite of the choice lit makes for a 48-LED strip, where
// one LED is over two percent and flooring keeps the bar from ever claiming more than was
// reported; the two are different because the unit is.
//
// The answer is zero when nothing is filled, and otherwise clamped to at least one column and
// at most the whole width. One column, because a job that has started and rounded away to
// nothing would be a dark panel, which is a light saying the printer had said nothing. The whole
// width, because a fill that reaches the last column is the "everything filled" edge rather than
// a bar with a remainder of no width, and panelSegments treats it as one.
func fillColumns(frame render.Frame, geometry panel) int {
	if frame.Total <= 0 || frame.Lit <= 0 {
		return 0
	}

	// The one place a layout says what "across" means. fillAcrossColumns measures the boundary
	// in columns, over the panel's width; a layout that filled downwards would be these two
	// numbers the other way round, and nothing below would change.
	across := geometry.width
	if panelShape != fillAcrossColumns {
		across = geometry.height
	}

	if across <= 0 {
		return 0
	}

	columns := int(math.Round(float64(frame.Lit) / float64(frame.Total) * float64(across)))

	return min(max(columns, 1), across)
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

// transitions is what the log has already been told about the printer.
//
// It exists so that each change is said once and a tick that finds nothing new says
// nothing. A line per tick would be a minute of noise on a live print, and twenty lines
// for a state word that moved once; /status already answers what the printer is doing
// now, so the log is left to be the history — what changed while nobody was looking.
type transitions struct {
	// ever reports that a snapshot has been read. The first look is a baseline and not
	// a change: nothing has moved yet, and a module restarted hourly must not announce
	// the printer's state as though it had moved each time it started.
	ever bool

	connected bool

	// state is the last state word this build understood, and hadState says whether it
	// has held one. A word the build does not know is not a state a change can be
	// reported from, and it leaves the last known word in place — which is what the
	// host's own memory does, and what /status prints beside state_known.
	state    string
	hadState bool

	stale bool
}

// driver is the goroutine that turns the printer's state into frames.
type driver struct {
	source readings
	writer *writer
	light  strip
	logf   func(format string, args ...any)
	tick   time.Duration

	// told is what the log has already been told, so that a tick which finds the
	// printer as it left it writes no line.
	told transitions

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
			d.step(ctx, at)
		}
	}
}

// step is one turn of the watch: learn what the controller drives, read the printer,
// say what changed, and hand the newest frame to the writer.
//
// It is a method rather than the body of run so that a test can drive one turn at a
// time, at a moment it chooses, with no ticker and no clock of its own.
func (d *driver) step(ctx context.Context, at time.Time) {
	d.learn(ctx, at)

	snapshot := d.source.Snapshot()

	// What the printer did is said before the frame is decided, because the frame is
	// only what the light is asked to show about it. The two are different questions
	// and the log answers the first.
	d.note(snapshot)

	frame := render.Decide(snapshot, at, render.Config{LEDs: d.leds})

	publish(snapshot, frame)
	d.writer.submit(frame)
}

// note writes one line for each thing about the printer that has changed since the last
// turn, and nothing at all for a turn that finds it as it was.
//
// Four things are worth a line: the state word moving, the host losing and regaining its
// session with the printer, and the reading going stale or live again. Everything else is
// either routine — a progress tick, a frame repainted, a write that succeeded — or the
// present tense, which /status answers better than a log can. A printer that says
// PRINTING for an hour is one line at the start of the hour and none during it, however
// often the module looks.
func (d *driver) note(snapshot printer.Snapshot) {
	if !d.told.ever {
		d.told = transitions{
			ever:      true,
			connected: snapshot.Connected,
			state:     snapshot.State.Value,
			hadState:  snapshot.State.Known,
			stale:     snapshot.Stale,
		}

		return
	}

	if snapshot.Connected != d.told.connected {
		d.told.connected = snapshot.Connected

		if snapshot.Connected {
			d.logf("%s: %s is connected again", name, printerPhrase(snapshot))
		} else {
			d.logf("%s: %s is no longer connected", name, printerPhrase(snapshot))
		}
	}

	if snapshot.State.Known {
		if d.told.hadState && snapshot.State.Value != d.told.state {
			d.logf("%s: %s went from %s to %s", name, printerPhrase(snapshot),
				d.told.state, snapshot.State.Value)
		}

		d.told.state, d.told.hadState = snapshot.State.Value, true
	}

	if snapshot.Stale != d.told.stale {
		d.told.stale = snapshot.Stale

		if snapshot.Stale {
			d.logf("%s: the reading from %s is stale", name, printerPhrase(snapshot))
		} else {
			d.logf("%s: the reading from %s is live again", name, printerPhrase(snapshot))
		}
	}
}

// printerPhrase names the printer in a log line the way the person who named it would:
// by its name, and by what it is when no name has been read yet.
func printerPhrase(snapshot printer.Snapshot) string {
	if snapshot.Name == "" {
		return "the printer"
	}

	return "the printer " + snapshot.Name
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

	// The geometry goes to the writer, which is the thing that builds a body, and it goes
	// once: the LED count is learned once and never re-read, so the frame that arrives after
	// this one is written against the matrix the controller reported. A strip reports no
	// matrix and leaves the writer's zero panel in place, which is the flat body it has
	// always been given.
	d.writer.setPanel(info)

	if d.leds > 0 {
		d.logf("%s: the controller drives %d LEDs", name, d.leds)
	}

	if info.MatrixWidth > 0 && info.MatrixHeight > 0 {
		d.logf("%s: the controller is a %dx%d panel, so a frame is drawn as a rectangle",
			name, info.MatrixWidth, info.MatrixHeight)
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
