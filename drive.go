// The part that drives the light.
//
// One goroutine reads the snapshot the printer package holds and decides a frame
// from it; one goroutine writes frames to the controller. They are separate on
// purpose. WLED asks that calls to a controller be serialised — it has one small web
// server, and two writes in flight interleave in ways nothing on the wire promises —
// so there is one writer per binding and one door per controller, and the writers on
// one box take turns at it. And that writer must not be whatever noticed the
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
	"sync"
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

// panel is a controller's geometry: how many pixels wide its matrix is and how many rows it has,
// or — for a light that is one run of pixels — how many LEDs the run holds.
//
// The zero value is a controller that has said nothing about itself, and that is the shape the 1D
// path writes for a strip today: the steady picture of a strip is a colour, which needs no
// rectangle. Both dimensions are needed before a controller counts as a panel: WLED publishes
// "leds.matrix" only on a build configured as one, and a half-read geometry is a strip.
type panel struct {
	width  int
	height int

	// run says this is a light's single run of pixels rather than a matrix: a strip, whose geometry
	// is its LED count laid out one pixel tall. It is a field of its own because a 48x1 rectangle
	// and a 48-LED strip are the same two numbers and different things — the same reason a word is
	// refused on both while a diagnostic pattern is drawn on both, laid out differently.
	run bool
}

// matrix reports whether the controller has a matrix at all.
func (p panel) matrix() bool { return !p.run && p.width > 0 && p.height > 0 }

// runOf reports a light that is one run of pixels, which is what a strip is: the geometry this
// module makes up for one is its LED count in a single row.
func (p panel) runOf() bool { return p.run && p.width > 0 }

// sized reports a light with pixels to draw on at all, whichever shape it is. A light that reported
// nothing is not one, and a picture asked of it is refused rather than drawn into no pixels.
func (p panel) sized() bool { return p.matrix() || p.runOf() }

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
//
// There is no call here that writes one colour per LED. There was: a segment's i array in a
// JSON body, which was how the word was drawn before it moved to DDP. This firmware answered
// every such body 200 and drew none of them, so the call is gone rather than kept as a floor
// nobody stands on — pixels.go has the four measurements.
type strip interface {
	Info(ctx context.Context) (wled.Info, error)
	State(ctx context.Context) (wled.State, error)
	SetBrightness(ctx context.Context, brightness int) error
	SetPower(ctx context.Context, on bool) error
	SetSegments(ctx context.Context, segments []wled.SegmentWrite) error
	RestoreSegment(ctx context.Context, segment wled.Segment) error
	// RunPreset runs one of the controller's own presets, by the name WLED's interface gives it.
	//
	// It is here because a moment may name one (docs/product-requirements.md §2.3): a preset
	// carries its own segments, brightness and LED map, so the light renders it and this module
	// only names it. The name is resolved to a preset id by the client, from the controller's own
	// /presets.json, so no id is ever invented here.
	RunPreset(ctx context.Context, name string) error
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

	// schedule is the time-of-day brightness this writer applies, and nil means the operator
	// wrote none: the fixed brightness then stands, which is what every install before this
	// setting did. It stays nil on a writer that borrows a segment whatever the settings say,
	// because the brightness in this mode is somebody else's — see retune.
	schedule *brightnessSchedule

	// store is this module's own configuration as it reaches the painter: a state's colour and
	// word, the pause, and the brightness windows. Nil means there is no store to ask — a module
	// the host granted no directory, or a writer a test built — and the renderer's own table and
	// the settings are then the whole picture, which is what every install before the store
	// existed did. See show.go.
	store lights

	// printer is the binding's own device id, which is the name a pause on one binding is kept
	// under, and empty for a writer that is not a binding.
	printer string

	// pauseEffect is what a pause does to this binding: it is switched off, or its borrowed zone is
	// handed back. It is pause.go's answer, kept here rather than decided again, because the one
	// thing that must not be got wrong is a borrowed zone being powered off with somebody else's
	// run on the same strip.
	pauseEffect string

	// paused is whether this goroutine has made the one write a pause costs. The store says which
	// instant a pause ends at; this says whether the light has been switched off for it yet, which
	// is what keeps a pause to one write and then none.
	paused bool

	// afterPause is set when a pause ends, so that the frame written next is painted rather than
	// suppressed as "already shown": the light has been dark, and nothing the writer remembers
	// about it is evidence of what is on the wall.
	afterPause bool

	// spoken is the state table's word the light is holding, and nil when it holds none. It is the
	// event's own hold — the picture, its pixels, the repeat that keeps live mode alive and the
	// deadline — because a state's word is the same kind of picture as the finished word.
	spoken *heldEvent

	// holding is what this light is showing *besides* the steady frame, published for the Overview's
	// thumbnail: a state's word, a moment's colour, a preset the light is running, or a pause that
	// switched it off.
	//
	// It is an atomic pointer because the writer is the only goroutine that paints and the
	// thumbnail's route is another: what the light is showing has to be readable without touching
	// the writer's own state. Nothing else about a picture is read this way — the frame, the
	// geometry and the span are published by the driver — and this is here because these four are
	// decided inside the writer and nowhere else.
	holding atomic.Pointer[holding]

	// saidWord is the last thing this writer said about a word it could not draw, so that a word
	// too wide for its panel costs one line and not one a second.
	saidWord string

	// level is the brightness the controller has most recently been asked to take, and -1
	// means it has been asked for nothing yet. It is what makes a schedule cost one write per
	// change and no more: a window that still holds the value the controller already has is
	// not a write at all, and a value that failed to land is asked for again rather than
	// believed.
	level int

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

	// gate is the door every write to this writer's controller passes through, shared with the
	// other bindings on that controller and nil when this writer has the controller to itself.
	// A controller is one small web server, and a module that now holds several bindings on one
	// box takes turns at it rather than knocking twice at once. See gate.
	gate *gate

	// record is where one attempt to write is published for the status route to read. It is
	// recordSend for a watch with one binding, and a per-binding record when a watch holds
	// several, because a route with one body cannot say which binding it is describing.
	record func(report sendView)

	// geometry is the matrix the controller reported, and the zero panel for a strip. The
	// driver's goroutine stores it the moment Info answers and the writer's reads it on
	// every frame, so it is an atomic pointer rather than a field two goroutines would
	// race on. Nothing else about a frame depends on it: the colour, the fill and the
	// total are the printer's, and this is only how they are laid out.
	geometry atomic.Pointer[panel]

	// pixelCount is how many LEDs the controller said it drives, and it is the strip's geometry:
	// a strip reports no matrix, and a picture pushed to it over DDP — which is what a diagnostic
	// pattern is — needs its length. It is stored whether or not there is a matrix, because it is
	// the controller's own report and nothing else in this module asks it twice.
	pixelCount atomic.Int64

	// span is the run of pixels this writer borrowed, and nil for one that drives the whole
	// light. It is an atomic pointer for the reason geometry is one: the writer's goroutine
	// records it when it borrows, and a route drawing a thumbnail reads it without touching the
	// controller or the writer's own state.
	span atomic.Pointer[[2]int]

	// latest holds the newest scene waiting to be painted, and standing the newest one offered —
	// which is not the same thing: latest is emptied by the writer as it paints, while standing is
	// what the state table's word is decided from on every pass, because a word has to be measured
	// against the moment it is painted at and not against the moment it was submitted.
	latest   atomic.Pointer[scene]
	standing atomic.Pointer[scene]

	// fired is the newest event the driver has offered and the writer has not taken. It is an
	// atomic pointer for the same reason latest is one: the driver stores, the writer swaps,
	// and there is exactly one of each. See event.go for what an event is.
	fired atomic.Pointer[event]

	// held is the event the light is showing, with the moment its hold ends. It is written and
	// read only by the writer's own goroutine, which is why it needs no lock.
	held *heldEvent

	// push is the pixel transport the event layer draws through — DDP, the second transport —
	// opened on the first event and held for this writer's life. It is written and read only by
	// the writer's own goroutine, like held.
	push pixelPush

	// dialPixels opens that transport, and is a field rather than a direct call so that a test
	// can hand the writer a recorder instead of a socket. prepare sets it to dialPixels.
	dialPixels func(address string, pixels int) (pixelPush, error)

	// handedBack is set when a hold that pushed pixels ends, so that the frame written after it
	// is followed by one read of what the controller says about realtime — the hand-back's only
	// proof, because a datagram cannot be read back. See pixels.go.
	handedBack bool

	// overwritten says that a picture which is not a frame took the light, so that `last` — the
	// frame the strip did take, and the moment it took it — is not read as a description of the
	// wall.
	//
	// A held word does exactly that: it puts the panel on the establishing body and pushes its
	// pixels over DDP, and the steady frame it stands in front of stops being what anybody can see
	// without the frame being written again. Suppressing that write as "already shown" because the
	// frame equals the last one *was* a panel left on the establishing body — black, with the word's
	// name still on segment 0 — until the next write that was not suppressed, and how long that took
	// was nothing but where the sixty-second heartbeat happened to be in its cycle. Measured on the
	// operator's panel: on the stroke once, and thirteen and fifteen seconds late twice.
	//
	// It is set where the picture is put up and cleared where a frame lands, so the flag means what
	// a reader wants it to mean: `last` is a frame this writer delivered, and this says whether the
	// light is showing it. See hold.go's holdWord and the event path in run, which clears `last`
	// outright for the same reason.
	overwritten bool

	// layout is the operator's arrangement of the panel: a bar in a band, the percentage, the job's
	// name. The zero value draws nothing of its own, so a writer built without one behaves exactly
	// as this module did before the setting existed — which is what every test that does not ask
	// for a layout gets, and what an operator who switches every part off gets too.
	layout layout

	// composed is the picture the panel is holding as pixels this module drew, and nil when the
	// panel is on its own segments. It is not the event slot: an event holds the light for a
	// duration and then gives it back, while this is the steady picture and has no deadline — it
	// lasts until the print it describes ends. See composedPanel.
	composed *composedPanel

	// asks is where a route leaves one identify for this goroutine to run, and flashMu guards the
	// window a flash holds.
	//
	// A flash is ten seconds of power writes on a light this writer owns, so it is not something a
	// route may do for itself: it is handed over here and run by the goroutine that talks to the
	// controller, between two passes of the steady frame. flashPlan is nil when nothing is flashing,
	// and otherwise the plan of the flash in progress — which is both what a second press is answered
	// with and what `already` means. See identify.go.
	asks      chan *identifyCall
	flashMu   sync.Mutex
	flashPlan *identifyPlan

	// refreshes is where a refresh leaves one forced repaint for this writer's goroutine, and
	// nothing else: the steady pass deliberately skips a write whose picture has not changed, so
	// the whole point of the button is a write that goes out anyway. The request is a slot rather
	// than a flag because the caller has to be answered with what the light did — a light that did
	// not take the write has to be named as such and not counted as refreshed — and this goroutine
	// is the only one that talks to this controller, so the write itself has to happen here. See
	// refresh.go.
	refreshes chan *refreshCall

	// flashed is set when an identify has just put the light back, so that the steady picture is
	// painted in the same pass rather than on the heartbeat.
	//
	// The flash's give-back restores the light's own segments, so a plain frame is on the wall again
	// the moment it lands — but the picture this module draws in pixels is not in a segment, and the
	// controller left realtime while the light was dark. A write that is suppressed as "already shown"
	// because the frame equals `last` would then be a panel left on its own segments until the
	// heartbeat, which is the fault `overwritten` exists for in the word's case. So the flag is set by
	// the flash and read by the same two places afterPause is read, and the frame is written.
	flashed bool

	wake chan struct{}
}

// holding is what one light is showing instead of its steady frame, as the thumbnail route reads it.
//
// Exactly one of the four is ever set, and the zero value means "the steady frame is what is on the
// light". A pause wins over all of them, because a paused light is off and shows nothing at all.
type holding struct {
	// paused says a pause switched this light off. The thumbnail is black, which is what is on the
	// wall — §3's own words: "The thumbnail shows black because black is what is on the wall."
	paused bool
	// word is the state table's word the panel is holding, in its own colour.
	word   string
	colour wled.RGB
	// plain is the whole picture a moment that shows a colour put on the light.
	plain *render.Frame
	// preset is one of the light's own presets, running for a moment. A thumbnail cannot reconstruct
	// it — the light draws it and this firmware cannot read pixels back — so the route says so in a
	// sentence rather than drawing a picture of a light showing something else.
	preset string
}

// composedPanel is the picture the panel is holding as pixels this module drew, and when the next
// push of it is due.
//
// The push is not a repaint of something that changed: it is what keeps live mode alive, because
// this firmware leaves realtime 2500 ms after the last datagram and draws its own segments again.
// So a composed picture costs one push a second for as long as a print runs, and the arithmetic is
// in pixels.go beside pixelRepeat.
type composedPanel struct {
	picture panelPicture
	repaint time.Time
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
// carry a brightness is one fewer place for that rule to be broken. It takes no
// `brightness_schedule` for the same reason, and watch.start does not hand it one: a schedule
// is a brightness by another name, and a window's edge moving somebody else's desk light is
// exactly what this constructor exists to prevent. See begin and retune.
func newSegmentWriter(light strip, segment int, where string, logf func(format string, args ...any)) *writer {
	pen := prepare(light, logf)
	pen.segment = &segment
	pen.where = where

	// A writer built for one segment of somebody else's controller is the case the pause exception
	// exists for, and it asks pause.go rather than deciding for itself.
	pen.pauseEffect = pauseEffectOn(binding{Segment: segment})

	return pen
}

// prepare builds the writer both constructors start from.
func prepare(light strip, logf func(format string, args ...any)) *writer {
	if logf == nil {
		logf = func(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...) }
	}

	return &writer{
		light:      light,
		logf:       logf,
		now:        time.Now,
		after:      time.After,
		wake:       make(chan struct{}, 1),
		asks:       make(chan *identifyCall, 1),
		refreshes:  make(chan *refreshCall, 1),
		record:     recordSend,
		dialPixels: dialPixels,
		// A whole light is switched off by a pause, which is the rule and the answer pause.go
		// gives a binding with no segment. A writer built for one segment replaces this in
		// newSegmentWriter, from the same function.
		pauseEffect: effectOff,
		// Nothing has been asked of the controller's brightness yet. -1 is a value no
		// controller takes, so "asked for nothing" cannot be mistaken for "asked for 0".
		level: -1,
	}
}

// scene is one turn of the driver as the writer paints it: the frame the renderer decided, the
// state that frame is about, and the moment this module began showing that state.
//
// The last two travel beside the frame because of one rule the writer cannot answer for itself: a
// state carrying a word beats the layout composer for as long as that word's `holds` says, and the
// hold is measured from when the state began. A frame does not know what state it is or when the
// state started, and the writer's own clock is the only thing that may judge whether a hold has run
// out — so the driver hands over the two facts and the writer does the arithmetic at paint time.
type scene struct {
	frame render.Frame
	state string
	since time.Time
}

// submit offers one frame to the writer and never waits for it. The newest frame
// replaces whatever was waiting; the ring is dropped when one is already pending,
// because a pending ring already means "look at the slot".
func (w *writer) submit(frame render.Frame) {
	w.submitScene(scene{frame: frame})
}

// submitScene offers one turn of the driver, frame and state together.
//
// The scene is kept in two places for two different readers, and both are the writer's own
// goroutine: latest is the one slot the painter takes, and standing is what the state table's word
// is decided from on every pass — including the passes where nothing new has been decided, because
// a word that holds for thirty seconds has to be re-judged against the clock after the driver has
// stopped sending anything new.
func (w *writer) submitScene(sc scene) {
	w.standing.Store(&sc)

	if w.latest.Swap(&sc) != nil {
		// A scene was already waiting, and the ring that put it there is still pending: a
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

		// The ring is taken here, before either slot is looked at, and both slots are read
		// afterwards. That order is what makes the ring safe as a hint: a producer stores its
		// data and then rings, so a pass that has taken the ring and then looked will see
		// everything that was stored before the ring it took. Draining after looking would
		// let an event store itself and ring in the window between the look and the wait, and
		// the wait would then begin with a pending ring already spent — an event nobody takes
		// until the next frame arrives.
		//
		// The ring is a hint that a slot may hold something, and this is the look it asked for;
		// left behind it would ring for a frame already in hand, and the pass it woke would
		// abandon the wait armed below and arm it again.
		select {
		case <-w.wake:
		default:
		}

		// An identify, before anything else this pass would write. A flash is the light going on and
		// off, so a frame written in the middle of it would be the module painting over the one thing
		// the person in the room is looking at — and the steady frame waits in its slot for ten
		// seconds, which is what a hold does with a picture. It comes before the pause because a
		// paused light has to be *refused* with the reason rather than left waiting for a pause
		// nobody may end. See identify.go.
		if w.serveIdentify(ctx) {
			continue
		}

		// A refresh's forced write, before the pause and before anything else this pass would
		// paint. It is asked here rather than on the driver's tick because it is a write the steady
		// pass would skip as "already shown" — the picture has not changed, and going out anyway is
		// the whole of what the button does — and because a light inside a moment, a word or a pause
		// has to be *answered* rather than written over: see refresh.go.
		if w.serveRefresh(ctx) {
			continue
		}

		// The pause, before anything this loop would write. A pause is a light switched off and
		// then nothing at all: no frames, no brightness, no retries, no lines, until it ends. It
		// is asked here rather than on the driver's tick because the instant it ends is the store's
		// and the clock's, and the wait below is bounded by that instant — so a pause that runs out
		// repaints at the moment it runs out and not at the next time the printer happens to move.
		if w.holdPause(ctx) {
			if ctx.Err() != nil {
				return
			}

			continue
		}

		// A scheduled brightness changes at a window's edge and at no other moment — not when
		// the printer moves, not when a frame is written — so it is checked on every pass of
		// this loop rather than on anything the driver hands over. The loop already turns at
		// the driver's own second (every tick offers a frame), so a window boundary lands
		// within about a second of being crossed, and the wait for the light is never the
		// reason a schedule is late.
		//
		// A write that fails is treated as any other failed write to this controller: it opens
		// the same doubling window, so a controller that is off costs one attempt per window
		// rather than one per second, and the value is asked for again when the light answers.
		if err := w.retune(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}

			failures++

			hold := backoffFor(failures)
			retryAt = w.now().Add(hold)

			w.logFailure(failures, hold, err)

			continue
		}

		// An event takes the light for its own duration. Its deadline is stamped from the clock
		// the writer reads, and it is taken here — before the steady frame, after the backoff
		// window — because it is not a frame: it holds the light against the frames the driver
		// keeps deciding. An event that arrives while the controller is down waits for the
		// window like everything else, and its hold starts when it lands, not when it was fired.
		w.takeEvent(ctx)

		if w.held != nil {
			if wait, holding := w.held.holdLeft(w.now(), w.standing.Load()); holding {
				if !w.held.painted {
					if err := w.deliverEvent(ctx, w.held.event); err != nil {
						if ctx.Err() != nil {
							return
						}

						failures++

						hold := backoffFor(failures)
						retryAt = w.now().Add(hold)

						w.logFailure(failures, hold, err)

						continue
					}

					if failures > 0 {
						w.logf("%s: the light is answering again after %d failed writes", name, failures)
					}

					failures, retryAt = 0, time.Time{}

					// The word is on the light and no steady frame is, and saying so is what
					// makes the frame after the event paint rather than be suppressed as
					// "already shown": the picture the light holds changed underneath the
					// writer, so the frame it last delivered is no longer evidence of anything.
					last, lastOK = nil, w.now()

					w.held.painted = true
				} else if w.held.frame != nil && !w.held.repush.After(w.now()) {
					// The word is a still picture and the controller stops drawing it after
					// realtimeTimeout of silence, so the same frame goes out again once a second
					// for the length of the hold. A repeat that fails is one line and the hold
					// continues: the picture may still be on the panel, and a controller that
					// has stopped taking datagrams is reported by the frame that follows this
					// hold rather than by forty lines during it.
					//
					// A repeat is a write to the light and is recorded as one. That is what makes
					// /status's last_send an age a person can read as a write age: a minute-long
					// hold used to leave the field standing at the instant the hold began, so the
					// panel read "17 minutes ago" while it was being written every second.
					if err := w.republish(w.held); err != nil {
						if ctx.Err() != nil {
							return
						}

						if w.held.pushFails == 0 {
							w.logf("%s: the %q picture did not reach the panel again and the hold continues: %v",
								name, w.held.label(), err)
						}

						w.held.pushFails++
					}

					w.held.repush = w.now().Add(pixelRepeat)
				}

				// The steady frame waits in the slot, newest first, for the event's own
				// remaining time and no longer: an event delays a frame by its duration, not
				// by its duration plus a heartbeat. The wait is re-armed on every pass, so a
				// frame that arrives during the hold shortens what is left of it rather than
				// extending it, and the frame painted at the end is the newest one decided.
				//
				// A held picture also wakes the writer when the next repeat of it is due, which
				// is the only reason this is not simply the hold's remaining time.
				wait = w.until(w.held, wait)

				select {
				case <-ctx.Done():
					return
				case <-w.wake:
				case <-w.after(wait):
				}

				continue
			}

			// The hold is over. An event that pushed pixels has to be proved to have let the
			// light go, and that proof is a read of the controller taken after the frame below
			// has been written — see handedBack.
			//
			// A preset's moment is the other half of a capture taken when it began: the light goes
			// back exactly as it was found before the steady picture is written over it, so that
			// what the operator had is what the moment ends on rather than what this module drew.
			if w.held.took != nil {
				w.giveLightBack(ctx, w.held.took)
			}

			// Whatever the hold was showing, it is not showing it any more: the frame painted below
			// is what the light comes back to, and the thumbnail follows it.
			w.holding.Store(nil)

			if w.held.frame != nil {
				w.handedBack = true
			}

			w.held = nil
		}

		// The state table's word, and it comes before the frame because of the rule it keeps: a
		// state carrying a word beats the layout composer, so the word is the picture and the
		// frame the driver keeps deciding waits in its one slot. While it holds, nothing below
		// runs; when it stops applying — the state moved, or its hold ran out — the word lets go
		// and the frame is painted in this same pass, which is what makes an expiry immediate.
		wait, holding, err := w.speak(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}

			failures++

			hold := backoffFor(failures)
			retryAt = w.now().Add(hold)

			w.logFailure(failures, hold, err)

			continue
		}

		if holding {
			select {
			case <-ctx.Done():
				return
			case <-w.wake:
			case <-w.after(wait):
			}

			continue
		}

		// Nothing is in the way, so anything the driver has decided goes out now, newest
		// first: a frame that arrived during the window is held here, in the one slot,
		// and never queued.
		var frame *render.Frame

		if decided := w.latest.Swap(nil); decided != nil {
			picture := decided.frame
			frame = &picture
		} else if w.afterPause || w.flashed {
			// A pause has just ended, or a flash has just put the light back, and the driver may
			// not have said anything new yet. The newest scene it offered is still the picture, and
			// waiting a tick for it to offer it again is the light that stays dark for a second
			// after the button — which is the defect this rule exists to prevent, at a smaller
			// scale. For a flash it is the picture drawn in pixels that needs it: the give-back
			// restored the light's own segments, and this module's pixels are not in them.
			if standing := w.standing.Load(); standing != nil {
				picture := standing.frame
				frame = &picture
			}
		}

		// A pause has just ended, or a flash has, so whatever this writer last delivered is no
		// evidence of what is on the wall: the light was dark, and a frame identical to the one
		// before it must still be painted rather than suppressed as "already shown".
		if w.afterPause || w.flashed {
			w.afterPause = false
			w.flashed = false

			last, lastOK = nil, w.now()
		}

		// A composed picture — a bar, digits, a job's name — is pixels this module drew, and
		// this firmware draws pixels only while a stream is live: it leaves realtime 2500 ms
		// after the last datagram. So a panel holding one is pushed again every pixelRepeat
		// for as long as the picture is the one the layout draws, and a pass with nothing new
		// from the printer waits for that push rather than for the heartbeat. The frames the
		// driver keeps deciding wait in the one slot, newest first, exactly as they do behind an
		// event: the panel is told the newest thing that is true, once a second.
		if w.composed != nil {
			picture := w.composed.picture

			if frame != nil {
				picture.frame = *frame
			}

			if !w.drawablePanel(picture.frame) {
				// The print ended, the reading went stale, or the operator's layout no longer
				// draws a job: the panel goes back to its own segments, and the frame that takes
				// it back is the plain one below. handedBack is what makes the writer read the
				// controller afterwards, past the firmware's own timeout, and prove it left
				// realtime — the only proof this transport allows.
				w.composed = nil
				w.handedBack = true
				frame = &picture.frame
			} else {
				if frame == nil {
					if wait := w.composed.repaint.Sub(w.now()); wait > 0 {
						select {
						case <-ctx.Done():
							return
						case <-w.wake:
						case <-w.after(wait):
						}

						continue
					}
				}

				picture.at = w.now()

				// Recorded, for the same reason the event's repeat is: this is a write to the
				// light, once a second, for as long as the print runs, and a last_send that
				// skipped it would stand at the moment the picture was first established — which
				// is the "4 minutes ago" a panel showed during a print while the strip beside it
				// read twenty-six seconds.
				if err := w.report(func() error { return w.repeatComposed(picture) }); err != nil {
					if ctx.Err() != nil {
						return
					}

					// A controller that has stopped taking datagrams is tried on the same
					// doubling window as one that has stopped taking bodies: a push a second
					// against a light that is not answering is the storm this module was taught
					// not to make. While the window is open the panel keeps whatever it last
					// took, which for an unreachable controller is the establishing body.
					failures++

					wait := backoffFor(failures)
					retryAt = w.now().Add(wait)

					w.logFailure(failures, wait, err)

					continue
				}

				if failures > 0 {
					w.logf("%s: the light is answering again after %d failed writes", name, failures)
				}

				failures, retryAt = 0, time.Time{}

				w.composed.picture = picture
				w.composed.repaint = w.now().Add(pixelRepeat)

				// The plain path's memory is not evidence about pixels: it compares the colour
				// and the fill of a frame, and neither is what is on the panel.
				last, lastOK = nil, w.now()

				continue
			}
		}

		// The other direction: a frame that is about a job, on a panel whose layout draws
		// something. The picture is painted once through the event's own painter — the
		// establishing body, the pixels and the one read that says the controller took them —
		// and then repeated, rather than painted again, for as long as it is the picture.
		//
		// Every one of those writes is recorded for /status, the establishing one and each
		// repeat: last_send answers "when did this module last write to this light", and the
		// repeats are writes. Leaving them out made the field a change-age wearing a write-age
		// label, which is how a panel being written every second came to report four minutes.
		if frame != nil && w.composed == nil && w.drawablePanel(*frame) {
			picture := w.pictureOf(*frame)

			if err := w.report(func() error { return w.enterComposed(ctx, picture) }); err != nil {
				if ctx.Err() != nil {
					return
				}

				failures++

				wait := backoffFor(failures)
				retryAt = w.now().Add(wait)

				w.logFailure(failures, wait, err)

				// The frame goes back in the slot. It was taken out to paint and the light did not take
				// it, so the pass after the window has to have something to paint: without this the
				// panel would keep the picture before it until the driver's next tick, and a failed
				// entry would cost a whole second on every attempt that never quite lands.
				w.latest.Store(&scene{frame: *frame})

				continue
			}

			if failures > 0 {
				w.logf("%s: the light is answering again after %d failed writes", name, failures)
			}

			failures, retryAt = 0, time.Time{}

			w.composed = &composedPanel{picture: picture, repaint: w.now().Add(pixelRepeat)}
			last, lastOK = nil, w.now()

			continue
		}

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

		// An unchanged picture is not written again — unless a picture that is not a frame has
		// taken the light since `last` was delivered, which is the held word's doing and the
		// reason this is not a comparison of two frames alone: the frame the word stands in
		// front of is exactly the frame `last` holds, so "already shown" would be a panel left
		// on the word's establishing body with nothing coming to paint it until the heartbeat.
		if !forced && last != nil && !w.overwritten && samePicture(*last, *frame) {
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

		// What the light is showing is a frame this writer delivered, so the frame after it may
		// be suppressed as already shown again.
		w.overwritten = false

		// The frame that takes the light back from a held word is written; now the controller is
		// read once to prove it left realtime. A JSON state write does not end live mode by
		// itself — measured on this panel — so the read is taken after the firmware's own
		// timeout, which is why it lives there and not here.
		if w.handedBack {
			w.handedBack = false

			w.confirmHandback(ctx)
		}
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
	return w.report(func() error { return w.paint(ctx, frame) })
}

// report runs one write to the light and records what it did, which is what makes /status
// worth reading when the light and the module disagree. It is one place for a frame and for an
// event, so that neither can reach the controller without the route knowing.
func (w *writer) report(write func() error) error {
	at := w.now().UTC().Format(time.RFC3339Nano)

	if err := w.gate.do(write); err != nil {
		w.record(sendView{At: at, OK: false, Error: err.Error()})

		return err
	}

	w.record(sendView{At: at, OK: true})

	return nil
}

// begin is what the writer does once, before the first frame, and the two modes it has are
// the whole of what this module owes a controller it shares.
//
//   - driving the whole strip (segment nil): the brightness in force at this moment is written
//     once — the fixed setting, or the window of a `brightness_schedule` that covers now — and
//     the strip is switched on, as this module has always done. That is settle.
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
	if _, paused := w.pauseNow(); paused {
		// A light that is already paused is switched off by the first pass of the loop and
		// nothing else: `settle` writes a brightness and a power-on, and a pause is one write
		// followed by none. A borrowed zone is not even borrowed, because there is nothing to
		// hand back yet and the zone is somebody else's until this module draws in it.
		return
	}

	if w.segment != nil {
		w.borrow(ctx)

		return
	}

	w.settle(ctx)
}

// settle writes the brightness this instance runs at, once, and makes sure the
// strip is on.
//
// The brightness written here is the one in force at this moment: the fixed
// `brightness` when no schedule is written, which is every install before the
// schedule existed, and the window's value when one covers now. It is written here
// and in retune, and nowhere else in the process.
//
// The power write is the other half of never leaving a strip dark: a controller
// somebody switched off would keep its colours and show none of them.
//
// Both are best effort. A brightness or power write that failed is logged and the
// frames follow anyway, because colour writes do not depend on either and a module
// that stopped here would show nothing at all. A brightness that failed is not
// recorded as written, so the next pass asks for it again rather than believing a
// value the controller never took.
func (w *writer) settle(ctx context.Context) {
	wanted, why, _ := w.levelAt(w.now())

	if err := w.gate.do(func() error { return w.light.SetBrightness(ctx, wanted) }); err != nil {
		w.logf("%s: the light's brightness was not set: %v", name, err)
	} else {
		w.level = wanted

		// Without a schedule the line above the watch already says what the light runs at,
		// and a second line repeating it is noise. With one, this sentence is the only thing
		// anywhere that says which window was judged and which clock it was judged in — the
		// fact a person needs when the light is bright at the wrong hour.
		if why != "" {
			w.logf("%s: the light's brightness is %d, %s", name, wanted, why)
		}
	}

	if err := w.gate.do(func() error { return w.light.SetPower(ctx, true) }); err != nil {
		w.logf("%s: the light was not switched on: %v", name, err)
	}
}

// levelAt is the brightness this writer should be running at, a sentence saying which rule chose it
// — empty when there is nothing to explain — and whether a window chose it at all.
//
// The store's list wins when it holds anything, and the setting is what it falls back to: the
// setting is the migration source (docs/product-requirements.md §4), the list is what the Brightness
// screen edits, and both are read by `brightnessSchedule.level` so a window cannot mean two things.
// `judgesBrightness` is the companion question — "is anything judging this at all" — and retune
// needs both, because "no window covers now" and "nothing is being judged" carry the same brightness
// and only the first is worth keeping in step with the controller.
//
// The fixed `brightness` setting is the answer whenever no window contains the moment, which is what
// makes a schedule with a gap in it, or a schedule that ends, behave like the setting this module has
// always had rather than like a light that goes dark between windows.
func (w *writer) levelAt(when time.Time) (int, string, bool) {
	if w.store != nil {
		if asked, held := w.store.level(when); held {
			if asked.covered {
				return asked.level, asked.why, true
			}

			return w.brightness, asked.why, false
		}
	}

	level, why, covered := w.schedule.level(when)
	if !covered {
		return w.brightness, why, false
	}

	return level, why, true
}

// judgesBrightness reports whether anything is judging the brightness by the time of day at this
// moment: a list of windows in this module's own store, or the `brightness_schedule` setting.
//
// It exists because "no window covers now" and "nothing is being judged at all" are different
// answers with the same brightness in them, and only the first is a reason to keep comparing the
// value the controller holds against the value the windows ask for. It asks the store the same
// question `levelAt` does, so the two cannot disagree about whether there is a list.
func (w *writer) judgesBrightness(when time.Time) bool {
	if w.store != nil {
		if _, held := w.store.level(when); held {
			return true
		}
	}

	return w.schedule != nil
}

// retune writes the brightness a schedule asks for, and only when it differs from the
// one the controller has already been given.
//
// **It never writes one for a borrowed segment, and that is the whole reason this
// method is a method rather than a line in the frame path.** A brightness is
// controller-wide: SetBrightness moves every segment on the box, including the five
// somebody else is using. A writer that drives one borrowed segment of a desk light
// must therefore write no brightness at all — at start, at a window's edge, ever — so
// the check is here as well as at the constructor, and the schedule is never handed to
// a segment writer in the first place (see watch.start). Two guards, because the cost
// of getting this wrong is somebody's desk light dimming at eight in the evening for a
// reason nobody on that desk can see.
//
// A value that has not changed is not written. That is what keeps a schedule at one
// write per boundary rather than one a tick, and it is the same rule the frame path
// keeps with samePicture.
func (w *writer) retune(ctx context.Context) error {
	if w.segment != nil {
		return nil
	}

	wanted, why, _ := w.levelAt(w.now())
	if !w.judgesBrightness(w.now()) {
		// Nothing is judging the brightness by the time of day — no window in the store and no
		// schedule in the settings — so the fixed value `settle` wrote is the whole answer and
		// there is nothing here to keep in step.
		return nil
	}

	if wanted == w.level {
		return nil
	}

	if err := w.gate.do(func() error { return w.light.SetBrightness(ctx, wanted) }); err != nil {
		return fmt.Errorf("the brightness %d was not set (%s): %w", wanted, why, err)
	}

	w.level = wanted

	// One line per change, like every other line in this log: a window's edge is a
	// transition, and a value that stood all evening writes nothing at all.
	w.logf("%s: the light's brightness is %d, %s", name, wanted, why)

	return nil
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

// drawablePanel reports whether a frame should be drawn as a composed picture: a panel, a layout
// that draws something of its own, and a frame that is about a job in progress.
//
// All three, and the third is not decoration. A panel's resting picture — idle, finished, stopped,
// an error, a printer that has gone quiet — is the state colour filling it, which is what the
// operator has been looking at and what reads from across a room; the layout is the picture of a
// print in progress, where a bar and a number mean something. It is also what keeps the controller
// off realtime for the rest of the day: a composed frame is held live, and there is no reason to
// hold a light live to show one colour.
func (w *writer) drawablePanel(frame render.Frame) bool {
	return drawsComposed(frame, w.panelOf(), w.composing())
}

// composing is the arrangement this binding draws a panel with, and it is the one place that decides
// between the store's table and the `layout` setting.
//
// The store's row for this binding wins when there is one — that is what the Layout screen edits, and
// a picture composed from anything else would be a screen that lies about the wall. A binding with no
// row composes with the `layout` setting, which is the migration source exactly as the brightness
// windows are (docs/product-requirements.md §4): an install that has never opened the Layout screen
// keeps the picture it has been drawing, to the byte.
//
// It is asked on every pass rather than kept, for the reason show.go gives about every other table:
// the document is edited while the process runs, and a value cached at start is a panel whose layout
// stops matching the screen the moment somebody changes it.
//
// The zone the ETA line writes its clock in is stamped on the answer here, and here only, so that
// every path that draws a panel has it: this writer's own picture, the shape published for the
// thumbnail, and the preview route all come through this function. A layout read straight out of the
// store has no zone of its own — the zone is the `timezone` setting's, not the row's — so a
// painter that took the row directly would draw no clock on a binding that has one.
func (w *writer) composing() layout {
	shape := w.layout

	if w.store != nil {
		if one, held := w.store.layout(); held {
			shape = one
		}
	}

	if zone, ok := w.clock(); ok {
		shape.zone = zone
	}

	return shape
}

// clock is the zone a clock time is written in, from the store this binding reads.
//
// Nil for a writer built without a store — every test that is not about the settings — and nil for a
// module whose `timezone` setting names no zone it can load. Both draw no clock rather than a clock
// in this process's own zone, which is seven hours from the operator's evening (AGENTS.md §5.14).
func (w *writer) clock() (*time.Location, bool) {
	if w.store == nil {
		return nil, false
	}

	return w.store.clock()
}

// drawsComposed is the rule above as a function of its three facts, so that the one other caller
// that has to make the same decision — GET /frames, drawing a thumbnail — is making it by the same
// rule rather than by a copy of it. A picture composed on the Overview from a rule the painter does
// not use is a thumbnail that disagrees with the light, which is the one thing it must never do.
func drawsComposed(frame render.Frame, geometry panel, shape layout) bool {
	return geometry.matrix() && shape.draws() && aboutAJob(frame)
}

// shapeOf is what this writer draws on, as GET /frames needs it: the geometry the controller
// reported, the run of a segment it borrowed, the arrangement it composes with, what its light is
// showing instead of the steady frame, and the state it is showing with the moment that began.
//
// It is read by the driver's goroutine on every tick, so every field it touches is either an atomic
// pointer or a value the driver already owns. Nothing here writes to the light, and nothing here takes
// a lock: the Overview asks once per binding per draw, and a thumbnail must never cost a request.
func (w *writer) shapeOf() bindingShape {
	shape := bindingShape{
		geometry: w.panelOf(),
		span:     w.span.Load(),
		layout:   w.composing(),
		laidOut:  true,
	}

	if held := w.holding.Load(); held != nil {
		shape.paused, shape.word, shape.wordColour = held.paused, held.word, held.colour
		shape.plain, shape.preset = held.plain, held.preset
	}

	// The state and its start are the scene this writer last took, which is also what the state
	// table's word is judged against. A writer that has not taken one yet publishes neither, and a
	// state of "" is a light this module is not showing anything for.
	if standing := w.standing.Load(); standing != nil {
		shape.state, shape.since = standing.state, standing.since
	}

	return shape
}

// pictureOf is a frame as the operator's layout draws it at this moment.
func (w *writer) pictureOf(frame render.Frame) panelPicture {
	return panelPicture{frame: frame, layout: w.composing(), at: w.now()}
}

// enterComposed paints a composed picture for the first time, through the event's own painter.
//
// It is that painter and not a copy of it, deliberately: the establishing body, the pixels and the
// one read of /json/info that says the controller took them are three steps with three ways to be
// wrong, and the preview route walks them through paintEvent. A second implementation here would
// make "the preview draws exactly what a printing panel draws" a claim about two functions agreeing,
// which is the kind of claim that stops being true the first time one of them is edited.
func (w *writer) enterComposed(ctx context.Context, picture panelPicture) error {
	_, err := w.paintEvent(ctx, event{picture: &picture})

	return err
}

// repeatComposed draws the composed picture again at this moment and pushes it.
//
// The push is what keeps live mode alive: 2500 ms of silence and this firmware goes back to drawing
// its own segments, which on this panel is one rectangle in the background colour. The picture is
// drawn again rather than pushed from the bytes of the last push because the rotation is read from
// the clock — the half that takes the panel changes on its own interval, and a repeat that re-sent
// old bytes would hold the digits until the printer's progress next moved.
func (w *writer) repeatComposed(picture panelPicture) error {
	pixels, err := picture.draw(w.paintPanel())
	if err != nil {
		return fmt.Errorf("the composed frame was not drawn: %w", err)
	}

	return w.pushWord(pixels)
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

			// What this binding draws on, for the thumbnail: a borrowed zone is its own run of
			// pixels and not the strip it sits in.
			w.span.Store(&[2]int{segment.Start, segment.Stop})

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
	// The pixel socket goes first and needs no condition: a writer that never pushed holds
	// nothing, and one that did is done with it whatever else this function returns early from.
	w.closePixels()

	// A light a preset moment took is put back before the borrowed segment is, and on a context of
	// its own for the same reason: the one that ended this watch is already cancelled. A writer that
	// stops mid-moment is a writer whose presets would otherwise outlive it — the light would keep
	// running an animation this module switched on and no longer knows about.
	if w.held != nil && w.held.took != nil {
		giveBack, cancel := context.WithTimeout(context.Background(), wled.DefaultTimeout)
		w.giveLightBack(giveBack, w.held.took)
		cancel()

		w.held.took = nil
	}

	if w.borrowed == nil {
		return
	}

	// The write goes out on a context of its own, because the one that ended the watch is already
	// cancelled and a cancelled context fails the request before it is sent. The give-back itself is
	// handBack's, so that a stop and a pause cannot put two different segments back.
	ctx, cancel := context.WithTimeout(context.Background(), wled.DefaultTimeout)
	defer cancel()

	w.handBack(ctx)
}

// takenLight is a whole controller as it was found before something took it: its power, its
// brightness and every segment the controller was drawing.
//
// It is not `borrowed`, and the difference is what each promise is about. `borrowed` is one segment
// of somebody else's strip, kept for the life of the writer because the module draws in it and owes
// it back on the way out. This is the whole of the operator's own light, kept for the length of one
// thing that takes it: a preset carries its own segments, brightness and LED map
// (docs/design/v2/events/), so running one replaces everything the light was doing, and the moment is
// not over until all of it is back.
//
// An identify flash takes it the same way, for the same reason and with the same give-back: the flash
// is power writes on a light this module owns, and §2.6's promise is that it "leaves the light exactly
// as it found it". See identify.go.
//
// Both are refused on a writer that borrows a zone, before any of this runs — there is no part of
// somebody else's strip this module may take whole.
type takenLight struct {
	on         bool
	brightness int
	segments   []wled.Segment
}

// takeLight reads the controller once and keeps what it was doing.
func (w *writer) takeLight(ctx context.Context) (*takenLight, error) {
	state, err := w.light.State(ctx)
	if err != nil {
		return nil, fmt.Errorf("the light could not be read before it was taken, so it was not taken: %w", err)
	}

	return &takenLight{
		on:         state.On,
		brightness: state.Brightness,
		segments:   append([]wled.Segment(nil), state.Segments...),
	}, nil
}

// giveLightBack puts a whole light back exactly as it was found: every segment, then the brightness,
// then the power.
//
// That order is deliberate. This controller remembers a segment's bounds and colours in its own
// state, so the segments go back first — while the strip is still on, so that what is written is
// what is drawn — and the power last, because a restore that switched the light on before the
// pictures were back would show the moment's preset for one more frame. A give-back that fails is
// said once and not retried: the steady picture is written over the light in the same pass anyway,
// and a retry loop against a controller that will not take a body is the storm this module was
// taught not to make.
func (w *writer) giveLightBack(ctx context.Context, held *takenLight) {
	if held == nil {
		return
	}

	err := w.gate.do(func() error {
		for _, segment := range held.segments {
			if restoreErr := w.light.RestoreSegment(ctx, segment); restoreErr != nil {
				return restoreErr
			}
		}

		if levelErr := w.light.SetBrightness(ctx, held.brightness); levelErr != nil {
			return levelErr
		}

		return w.light.SetPower(ctx, held.on)
	})
	if err != nil {
		w.logf("%s: the light was not put back as it was found: %v", name, err)

		return
	}

	w.logf("%s: put the light back as the preset moment found it: %d segments, brightness %d, %s",
		name, len(held.segments), held.brightness, powerPhrase(held.on))
}

// powerPhrase says whether the light was on when it was taken, in a log line's words.
func powerPhrase(on bool) string {
	if on {
		return "on"
	}

	return "off"
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
// body it has always been given — but its LED count is kept beside it, because that is the
// geometry a picture pushed to a strip is drawn against (see paintPanel). A geometry that
// changes under a running writer — a controller swapped for a panel and asked again — does not
// happen in this module, because the LED count is learned once and never re-read; if that
// changes, the frame that crosses the change is the one to look at.
func (w *writer) setPanel(info wled.Info) {
	if info.LEDCount > 0 {
		w.pixelCount.Store(int64(info.LEDCount))
	}

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

// paintPanel is the geometry a picture drawn in pixels is laid out and pushed against: the matrix
// the controller reported, or — for a strip, which is one run of LEDs — that run, one pixel tall.
//
// It is a second question and not a change to panelOf, because the two are asked by different
// things. panelOf answers what the steady picture is drawn on, and a strip's steady picture is a
// colour written over JSON, which needs no rectangle. paintPanel answers where a picture goes when
// it is pixels, and a strip is a light with pixels: the diagnostic ladder has to be able to run on
// one, or a two-light install with a strip in it could not be tested at all.
//
// The zero panel when the controller has said nothing about itself, which every caller refuses.
func (w *writer) paintPanel() panel {
	if held := w.geometry.Load(); held != nil {
		return *held
	}

	if count := w.pixelCount.Load(); count > 0 {
		return panel{width: int(count), height: 1, run: true}
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
// Segment 0 also clears its name, and that is not tidiness. An event writes the word into the
// segment's name — it is the only thing that puts it there, because a datagram carries none —
// and on this firmware a body that writes a segment without "n" leaves the name it found
// there. So without this a panel keeps "FINISHED" for as long as it is not written again, and
// anything that later sets a text effect on that segment would draw the last print's word over
// a picture about the current one. It rides in the body that was going out anyway: one key,
// no extra request. Segment 1 is not named by any body this module writes, so there is nothing
// of its own to clear.
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
			{ID: firstSegment, First: 0, Count: frame.Total, On: true, Colour: frame.Colour, ClearName: true},
			{ID: remainderSegment, On: false},
		}
	}

	return []wled.SegmentWrite{
		{ID: firstSegment, First: 0, Count: frame.Lit, On: true, Colour: frame.Colour, ClearName: true},
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
//
// Segment 0 clears its name for the reason stripSegments gives: the word an event puts in that
// name would otherwise outlive the event on this firmware, which keeps a name through a body
// that does not mention one.
func panelSegments(frame render.Frame, geometry panel) []wled.SegmentWrite {
	columns := fillColumns(frame, geometry)

	if frame.Lit <= 0 || columns >= geometry.width {
		return []wled.SegmentWrite{
			{
				ID:        firstSegment,
				First:     0,
				Count:     geometry.width,
				FirstRow:  0,
				RowCount:  geometry.height,
				On:        true,
				Colour:    frame.Colour,
				ClearName: true,
			},
			{ID: remainderSegment, On: false},
		}
	}

	return []wled.SegmentWrite{
		{
			ID:        firstSegment,
			First:     0,
			Count:     columns,
			FirstRow:  0,
			RowCount:  geometry.height,
			On:        true,
			Colour:    frame.Colour,
			ClearName: true,
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

	// stale is whether the log has already said the reading is stale. On a job that is
	// over it is deliberately not cleared when the reading comes back — see the staleness
	// rule at the end of note.
	stale bool
}

// driver is the goroutine that turns the printer's state into frames.
type driver struct {
	source readings
	writer *writer
	light  strip
	logf   func(format string, args ...any)
	tick   time.Duration

	// pub is where the frame decided this tick is published for the status route. It is the
	// process-wide publish when a watch holds one binding; a watch holding several gives each
	// driver its own, because a route with one body cannot describe two bindings.
	pub func(snapshot printer.Snapshot, frame render.Frame)

	// told is what the log has already been told, so that a tick which finds the
	// printer as it left it writes no line.
	told transitions

	// leds is how many LEDs the controller drives, learned from the controller
	// itself rather than assumed. Until it is known a frame carries the colour and no
	// fill, which is all a strip of unknown length can honestly show.
	leds       int
	infoAt     time.Time
	infoFailed bool

	// store is the same store the writer reads, and it is here for the one thing a frame carries:
	// a state's colour. A frame is what /status answers with and what the Overview's thumbnail is
	// drawn from, so a colour changed in the store has to reach the frame rather than only the
	// pixels — otherwise the page and the wall would disagree about the same state.
	//
	// The word is deliberately not decided here. It is a picture, it lasts as long as its state
	// does, and whether it has run out is a question for the moment of the paint; the driver hands
	// the writer the state and the moment the state began and lets it draw.
	store lights

	// shown is the row of the state table the last frame was about, and shownSince the moment this
	// module began showing it. A word holds from the start of its state — thirty seconds of a state
	// the printer entered thirty seconds ago, not thirty seconds of a module running — and neither
	// fact is in the frame.
	shown      string
	shownSince time.Time
	shownEver  bool

	// now is the clock a refresh composes at. A tick arrives with the instant it is being decided
	// for and carries it; a refresh has no tick to borrow one from, so it reads the clock here. It
	// is a field rather than a call so that a test can hand this driver the same clock it hands the
	// writer, whose holds are judged against it.
	now func() time.Time

	// refreshes is where a refresh leaves one compose for this driver's goroutine. A frame is
	// built out of the state this goroutine owns — the state a word is judged against, the
	// controller's LED count, the colour the store holds for a row — so the second way to compose
	// a frame would be a second answer to what the light shows. There is one way, and it lives on
	// this goroutine: see compose.
	refreshes chan *refreshAsk
}

// newDriver prepares the reader for one watch. It sends nothing until run is
// called.
func newDriver(source readings, pen *writer, light strip, logf func(format string, args ...any)) *driver {
	if logf == nil {
		logf = func(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...) }
	}

	return &driver{source: source, writer: pen, light: light, logf: logf, tick: tickEvery, pub: publish,
		now: time.Now, refreshes: make(chan *refreshAsk, 1)}
}

// run decides a frame on every tick until ctx ends and hands it to the writer.
//
// A refresh is served between two ticks, on this same goroutine, because composing a frame is
// this goroutine's work: see compose.
func (d *driver) run(ctx context.Context) {
	ticker := time.NewTicker(d.tick)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case at := <-ticker.C:
			d.step(ctx, at)
		case call := <-d.refreshes:
			d.serveRefresh(ctx, call)
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
	//
	// note answers with the moments that change stands for, and they are fired below rather than
	// here: a moment that shows a colour shows *this tick's* colour, and the frame does not exist
	// yet.
	moments := d.note(snapshot)

	// What this tick's frame is, the row of the state table it is about, and the moment this module
	// began showing that state: one call, because a refresh composes a frame the same way and a
	// second copy of this arithmetic would be a second answer to what the light shows.
	frame, shown, since := d.compose(snapshot, at)

	// The moment table acts after the frame, on the same tick, so that what a moment shows and what
	// the steady picture shows are the same colour rules. The event takes the light from the writer
	// when it reaches it; the scene below still goes into the slot, because the steady picture is
	// what the light returns to when the moment's length runs out.
	//
	// The state travels with it because a moment with no length — "until it resumes", "until it is
	// cleared" — ends when the state it was fired for moves, and the writer's standing scene is the
	// newest tick rather than this one.
	d.fire(moments, frame, shown)

	d.pub(snapshot, frame)
	d.writer.submitScene(scene{frame: frame, state: shown, since: since})
}

// compose is one printer reading as a frame: the picture this moment asks for, the row of the
// state table it is about, and the moment this module began showing that state.
//
// It is the one place a frame is decided. A tick calls it and so does a refresh, which is the
// difference between the two answers being the same answer and two functions happening to agree:
// the requirement's own words for the button are a picture "based on the current state that
// second", so a refresh that re-sent the frame it painted last would be reporting the past.
//
// The colour is looked up here rather than left to the painter because a frame is what /status
// answers with and what the Overview draws its thumbnail from: a colour that only reached the
// pixels would be a page that disagrees with the wall.
func (d *driver) compose(snapshot printer.Snapshot, at time.Time) (render.Frame, string, time.Time) {
	frame := render.Decide(snapshot, at, render.Config{LEDs: d.leds})

	state, since := d.showing(snapshot, frame, at)
	frame.Colour = d.colourOf(state, frame.Colour)

	return frame, state, since
}

// showing is the row of the state table this frame is about, and the moment this module began
// showing that state.
//
// The row is read off the frame's own reason rather than worked out again here, and that is the
// point: whether a printer counts as disconnected or stale is `render.Decide`'s rule — with its
// thirty-second horizon and its exception for a job that is over — and a second copy of that
// arithmetic in this file would be a second answer to which picture is on the light.
func (d *driver) showing(snapshot printer.Snapshot, frame render.Frame, at time.Time) (string, time.Time) {
	name := ""

	switch frame.Reason {
	case render.ReasonDisconnected:
		name = disconnectedState
	case render.ReasonStale:
		name = staleState
	default:
		if definition, known := findState(snapshot.State.Value); known && snapshot.State.Known {
			// The table's own spelling, so that a store row written in any case is the same row.
			name = definition.name
		}
	}

	if !d.shownEver || name != d.shown {
		// The state began here: the first look is a baseline rather than a transition, and a
		// module restarted beside a printer that has been printing for an hour begins showing
		// that state at its own first look. A word's hold is counted from this moment, which is
		// the only one this process can honestly name.
		d.shown, d.shownSince, d.shownEver = name, at, true
	}

	return d.shown, d.shownSince
}

// colourOf is the colour the light is drawn in for one state: the store's, when somebody has
// overridden it, and otherwise the one the renderer decided.
//
// The fallback is the renderer's own answer and not a copy of a default, which is the rule the store
// keeps everywhere else: a state nobody has edited has no row to read, and `show.overridden` is what
// says whether the colour that came back was the operator's or the code's.
func (d *driver) colourOf(state string, fallback wled.RGB) wled.RGB {
	if d.store == nil || state == "" {
		return fallback
	}

	held, known := d.store.show(state)
	if !known || !held.overridden() {
		return fallback
	}

	return held.colour
}

// note writes one line for each thing about the printer that has changed since the last
// turn, and nothing at all for a turn that finds it as it was.
//
// It answers with the moments those changes stand for, in the order they are named in the table, so
// that the caller fires them against the frame it is about to decide. The log is what changed and the
// light is what a person walking past sees, and both come from the same comparison, which is why they
// are decided in one place.
//
// Four things are worth a line: the state word moving, the host losing and regaining its
// session with the printer, and the reading going stale or live again. Everything else is
// either routine — a progress tick, a frame repainted, a write that succeeded — or the
// present tense, which /status answers better than a log can. A printer that says
// PRINTING for an hour is one line at the start of the hour and none during it, however
// often the module looks.
//
// Staleness is a line of the first kind except on a job that is over, where it is said
// once and the dips that follow are not: see the rule at the end of this method.
func (d *driver) note(snapshot printer.Snapshot) []string {
	if !d.told.ever {
		d.told = transitions{
			ever:      true,
			connected: snapshot.Connected,
			state:     snapshot.State.Value,
			hadState:  snapshot.State.Known,
			stale:     snapshot.Stale,
		}

		return nil
	}

	moments := make([]string, 0, 2)

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

			// The word moved, so the light is told what happened as well as what is: the log
			// line above is for a person reading the history, and the moment is for a person
			// walking past the panel. The frame the driver hands the writer a moment later is
			// still the steady picture, and the writer goes back to it when the moment's own
			// duration is up.
			//
			// The pair is turned into a moment by momentFor, which is where the whole of that
			// mapping lives — this method notices a change and no more than that.
			if id, isMoment := momentFor(d.told.state, snapshot.State.Value); isMoment {
				moments = append(moments, id)
			}
		}

		d.told.state, d.told.hadState = snapshot.State.Value, true
	}

	// A job that is over says the same thing however quiet the printer goes. On a
	// finished machine the reading dips into staleness and back every minute or two —
	// a cooling bed is a value that moved, so the change clock restarts — and each dip
	// is a true fact about a printer with nothing new to say, not news. So the entry
	// into staleness is said once, the recovery is not said at all while the last word
	// is terminal, and told.stale is left where the entry put it so the next dip is
	// silent too. The first word that says the printer is doing something again — a
	// print, a pause, an idle machine somebody may walk up to — puts the pair back:
	// from there a silence is doubt again, and it is logged as one.
	if snapshot.Stale != d.told.stale {
		switch {
		case snapshot.Stale:
			d.told.stale = true
			d.logf("%s: the reading from %s is stale", name, printerPhrase(snapshot))

			// The printer stopped reporting, and the moment table has a row for exactly that:
			// "the printer stopped reporting while a job was live". A printer whose last word says
			// the job is over is not quiet about a job — it is quiet about nothing — so that case
			// fires nothing, which is the same exception the paint keeps.
			if !snapshot.Terminal() {
				moments = append(moments, momentPrinterWentQuiet)
			}
		case !snapshot.Terminal():
			d.told.stale = false
			d.logf("%s: the reading from %s is live again", name, printerPhrase(snapshot))
		}
	}

	return moments
}

// fire offers the light what the moment table says a set of transitions shows, in the order the
// transitions happened, against the frame this tick decided.
//
// The table is asked per moment, on the driver's own goroutine, and the answer is the binding's own
// row — the (light, printer) pair — with the code's default where the document holds none. A moment
// that is switched off does nothing at all, and a moment whose `shows` is Nothing does nothing at
// all: not a repaint, not a log line, nothing. That is what the screen's own "Nothing" means.
//
// A refusal — a preset named on a borrowed zone, a colour that is not a state, a word the font cannot
// spell — is one line and no picture, and the steady frame the driver already submitted stands. A
// moment that failed must not take the light down with it.
func (d *driver) fire(moments []string, frame render.Frame, state string) {
	for _, id := range moments {
		d.fireMoment(id, frame, state)
	}
}

// fireMoment is one moment, acted on as the table says.
func (d *driver) fireMoment(id string, frame render.Frame, state string) {
	entry := momentDefault(id)
	if d.store != nil {
		entry, _ = d.store.moment(id)
	}

	if !entry.Enabled {
		return
	}

	ev, what, err := d.writer.momentOf(entry, frame)
	if err != nil {
		d.logf("%s: the %s moment showed nothing: %v", name, id, err)

		return
	}

	if ev.showsNothing() {
		return
	}

	d.logf("%s: the %s moment shows %s", name, id, what)

	ev.state = state

	d.writer.fire(ev)
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

	// Build is what this process is: the release it reported to the host, the wire contract it
	// speaks and the store schema it writes. It rides on this route rather than on a route of its
	// own because the Diagnostics screen already reads this module's present tense, and because a
	// version served from anywhere but the running process is a number a bundle can disagree with.
	Build buildView `json:"build"`
}

// buildView is what this process is, as a caller of the route reads it.
//
// Every number here is the running binary's own and not a literal in a screen: Version is the
// release this process answered the host with at start, which is the number the host compares
// against the manifest's `version:`; Contract is the wire major it speaks to the host; and
// StoreSchema is the schema version of the configuration document it writes. A screen that
// printed a version of its own would be stating something about a binary it is not running.
type buildView struct {
	Version string `json:"version"`

	// Contract is the wire major, the number two peers must agree on to talk at all.
	Contract uint32 `json:"contract"`

	// StoreSchema is the schema version of the document this build writes, and the one it steps an
	// older document up to before reading it.
	StoreSchema int `json:"store_schema"`

	// Repository is where this module's source lives. It travels with the version for the same
	// reason: a screen offers the reader a way to the source, and it must be the address the
	// process was built with rather than one a bundle was built with.
	Repository string `json:"repository"`
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
	// Percent is the completed part of the job in whole percent, and -1 when
	// there is no percentage this build may present as current: no job on the
	// bed, no live reading, or a progress the guarded reading refused. It is what
	// a panel's layout draws in digits.
	Percent int `json:"percent"`
	// Name is the printer's own name for the job on the bed, and empty when
	// there is none or the reading is not live. What a panel draws from it is the
	// layout's business.
	Name string `json:"name"`
	// Reason names the rule that decided the frame.
	Reason string `json:"reason"`
	// Panel is what a panel's optional lines draw, and absent when the readings are not present
	// tense: a frame that is stale or disconnected carries none of it, which is the same rule
	// Percent keeps by going to -1.
	Panel *panelFactsView `json:"panel,omitempty"`
}

// panelFactsView is render.PanelFacts as a caller of the route reads it, and as the thumbnail route
// needs it back: every value with the unit in its name, and no zero standing for "nothing".
type panelFactsView struct {
	// RemainingSeconds and PrintingSeconds are what the machine says, and absent when it has said
	// nothing.
	RemainingSeconds *int `json:"remaining_seconds,omitempty"`
	PrintingSeconds  *int `json:"printing_seconds,omitempty"`
	// NozzleNow, NozzleTarget, BedNow and BedTarget are in degrees Celsius, and present only as a
	// pair for each heater: a target of zero is a heater that is off.
	NozzleNow    *float64 `json:"nozzle_now,omitempty"`
	NozzleTarget *float64 `json:"nozzle_target,omitempty"`
	BedNow       *float64 `json:"bed_now,omitempty"`
	BedTarget    *float64 `json:"bed_target,omitempty"`
	// Material is the filament's short name, and absent when the machine has said nothing.
	Material string `json:"material,omitempty"`
	// Printer is the machine's own name, from the host's record.
	Printer string `json:"printer,omitempty"`
	// HeightMM is the nozzle's Z position, and absent when the machine reports none.
	HeightMM *float64 `json:"height_mm,omitempty"`
}

// panelFactsViewOf is one frame's facts as the route prints them, and nil when there are none.
func panelFactsViewOf(facts render.PanelFacts) *panelFactsView {
	said := panelFactsView{Material: facts.Material, Printer: facts.Printer}

	if facts.RemainingSeconds >= 0 {
		said.RemainingSeconds = &facts.RemainingSeconds
	}

	if facts.PrintingSeconds >= 0 {
		said.PrintingSeconds = &facts.PrintingSeconds
	}

	if facts.Nozzle.Reported {
		said.NozzleNow, said.NozzleTarget = &facts.Nozzle.Now, &facts.Nozzle.Target
	}

	if facts.Bed.Reported {
		said.BedNow, said.BedTarget = &facts.Bed.Now, &facts.Bed.Target
	}

	if facts.HeightReported {
		said.HeightMM = &facts.HeightMM
	}

	if said == (panelFactsView{}) {
		return nil
	}

	return &said
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
	seen := viewOf(snapshot, frame)
	current.Store(&seen)
}

// viewOf is one reading and one frame as the route reads them.
//
// It is a function rather than the body of publish because a watch may hold more than one
// binding, and the per-binding store in watch.go needs the same answer under a name. Two copies
// of this mapping would be two answers to what /status means.
func viewOf(snapshot printer.Snapshot, frame render.Frame) view {
	return view{
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
			Colour:  colourView{R: frame.Colour.R, G: frame.Colour.G, B: frame.Colour.B},
			Lit:     frame.Lit,
			Total:   frame.Total,
			Percent: frame.Percent,
			Name:    frame.Name,
			Reason:  string(frame.Reason),
			Panel:   panelFactsViewOf(frame.Panel),
		},
	}
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
