package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nothingmn/wled-printer-status/internal/printer"
	"github.com/nothingmn/wled-printer-status/internal/render"
	"github.com/nothingmn/wled-printer-status/internal/wled"
)

// gate is one controller's door: every write this module makes to a controller passes through
// it, one at a time.
//
// It exists because a controller is one small web server, and the module may now hold more than
// one binding on it. Two bindings writing at once would be two requests in flight at a device
// measured at ~53 ms for a 48-LED strip and ~28 ms for a 256x64 panel; and a body that carries a
// segment is only meaningful against the state the previous body left, so overlapping them is
// how a panel ends up showing half of one frame and half of another.
//
// A nil gate writes straight through. That is the shape a writer a test builds on its own gets,
// and the shape a controller with one binding behaves like anyway.
//
// What the gate does not do is share a failure window. Each binding's writer still doubles its
// own wait on a failed write, which at the thirty-second cap means a controller that is down
// sees one attempt per binding per half minute rather than one for the whole box. That is a
// deliberate limit rather than an oversight: the wait lives in the writer's own loop, which is
// the code the backoff was proved in, and moving it here would be a second copy of the storm
// rule for the sake of two writes a minute.
type gate struct{ mu sync.Mutex }

// do passes one write through the gate and returns what it did.
func (g *gate) do(write func() error) error {
	if g == nil {
		return write()
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	return write()
}

// watchSource is one binding's view of its printer: what the driver reads each tick and what
// went wrong underneath it. *printer.Source is the only implementation in the process; the
// interface exists so that a test can run a watch with no host and no device.
type watchSource interface {
	Snapshot() printer.Snapshot
	Problems() <-chan error
	Run(ctx context.Context) error
}

// watchConfig is everything one watch is built from.
//
// It is a struct rather than a list of arguments because a watch is now built from a set rather
// than a setting: the caller supplies how to reach each printer and each controller, and a test
// supplies fakes, and neither of them has to know how many bindings there are.
type watchConfig struct {
	// bindings is what to run. It is held to bindings.check before anything is built.
	bindings bindings

	// newSource builds the reader for one binding's printer.
	newSource func(binding) (watchSource, error)

	// newLight builds the client for one controller's address. It is called once per
	// controller, so the bindings sharing a controller share one client and one gate.
	newLight func(address string) (strip, error)

	logf func(format string, args ...any)

	// publish and record, when set, replace the process-wide store /status reads with a
	// per-binding one. A watch holding several bindings needs that: the route has one body and
	// several things to say, and the bodies are named by controller and segment so a caller can
	// tell them apart. Nil means the single-binding store, which is what an install with one
	// binding reads and what every test of the writer already exercises.
	publish func(b binding, snapshot printer.Snapshot, frame render.Frame)
	record  func(b binding, report sendView)

	// tick overrides the driver's interval. Zero keeps the process's own.
	tick time.Duration

	// schedule is the time-of-day brightness the settings describe, and nil when the operator
	// wrote none. It reaches a whole-controller writer only: a binding that borrows a segment
	// is never handed one, because a brightness is controller-wide. See writer.retune.
	schedule *brightnessSchedule

	// layout is the operator's arrangement of a panel. Every writer gets it, borrowed segment or
	// not: it is what a panel's picture is composed from, and a borrowed segment cannot be a panel
	// at all (writer.refusePanel), so the layout is simply never read on one.
	layout layout

	// tables is this module's own configuration store, which the painter now obeys: a state's
	// colour and word, the pause, and the brightness windows. Nil means there is none to read — a
	// test's watch — and the renderer's table and the settings are then the whole picture.
	tables *configTables
}

// start runs one watch: a source, a writer and a driver per binding, one client and one gate per
// controller.
//
// Everything is built before any goroutine starts, so a set that cannot be run — an address this
// module cannot parse, a printer the host will not resolve — leaves the light as it was and the
// caller holding the reason. configure returns that reason and the operator reads it on the
// instance, which is the right answer for a configuration mistake: a process that started and
// never said why it was dark is worse.
//
// The returned cancel stops the watch and the returned channel is closed when every goroutine
// has returned — including each writer's give-back of a borrowed segment. That wait is the same
// one stopWatching already makes for a single binding, and it is why the channel exists rather
// than a bare cancel.
func (c watchConfig) start(parent context.Context) (context.CancelFunc, chan struct{}, error) {
	if c.logf == nil {
		c.logf = logf
	}

	controllers, err := c.bindings.controllers()
	if err != nil {
		return nil, nil, err
	}

	ctx, cancel := context.WithCancel(parent)

	type runner struct {
		binding binding
		source  watchSource
		pen     *writer
		drive   *driver
	}

	runners := make([]runner, 0, len(c.bindings))

	for _, ctl := range controllers {
		light, err := c.newLight(ctl.Address)
		if err != nil {
			cancel()

			return nil, nil, err
		}

		// One door per controller. Every writer on this controller holds the same one.
		door := &gate{}

		for _, b := range ctl.Bindings {
			b := b

			source, err := c.newSource(b)
			if err != nil {
				cancel()

				return nil, nil, err
			}

			var pen *writer

			if b.Segment >= 0 {
				// A writer that drives one borrowed segment. It is built without the
				// brightness, because in this mode this module writes none: see writer.begin.
				// The schedule is not handed over either, for the same reason — a brightness
				// is controller-wide, and a window's edge must not move somebody else's light.
				pen = newSegmentWriter(light, b.Segment, b.Address, c.logf)
			} else {
				pen = newWriter(light, b.Brightness, c.logf)
				pen.schedule = c.schedule
			}

			// Every writer knows its controller's address, whether it drives the whole light or
			// one borrowed segment. The event layer needs it for a second reason the JSON client
			// does not have: pixels go over DDP to a host and a port, not to a route on the
			// controller's own web server, and the address has to come from the same setting both
			// transports are built from so the two cannot drift apart.
			pen.where = b.Address

			pen.gate = door
			pen.layout = c.layout

			// The store, as this binding's painter reads it: one reader per binding, because a
			// pause is kept per binding and one reader shared between two would be answering for
			// whichever printer happened to be asked last. The light's name travels with it because
			// the moment table is keyed on the (light, printer) pair — the same printer on two lights
			// carries two independent sets, which is what multi-room means (§2.3).
			if c.tables != nil {
				pen.store = &storeLights{
					tables:  c.tables,
					printer: b.DeviceID,
					light:   lightName(b.Address),
					logging: c.logf,
				}
				pen.printer = b.DeviceID
			}

			// What a pause does to this binding, from pause.go's own answer: a whole light is
			// switched off, and a zone this module borrows is handed back rather than powered
			// down. Deciding it here and not in the writer is the point — the writer obeys the
			// answer, and there is one place the exception can be got wrong.
			pen.pauseEffect = pauseEffectOn(b)

			if c.record != nil {
				report := c.record
				pen.record = func(sent sendView) { report(b, sent) }
			}

			drive := newDriver(source, pen, light, c.logf)
			drive.store = pen.store

			if c.tick > 0 {
				drive.tick = c.tick
			}

			// Every frame this driver decides is published twice over: the answer /status reads,
			// and the shape of the display it was decided for. The second is what GET /frames
			// draws a thumbnail from, and it is published here rather than worked out there
			// because the writer is the only thing that knows what the controller reported about
			// itself and which run a borrowed segment holds. A thumbnail drawn from a second
			// reading of the light would be a second answer to what the light is.
			show := c.publish
			if show == nil {
				show = func(_ binding, snapshot printer.Snapshot, frame render.Frame) { publish(snapshot, frame) }
			}

			drive.pub = func(snapshot printer.Snapshot, frame render.Frame) {
				publishShape(b, pen.shapeOf())
				show(b, snapshot, frame)
			}

			runners = append(runners, runner{binding: b, source: source, pen: pen, drive: drive})
		}
	}

	for _, r := range runners {
		if r.binding.Segment >= 0 {
			c.logf("%s: watching printer %s and driving segment %d of %s, leaving its other segments and its brightness and power alone",
				name, r.binding.DeviceID, r.binding.Segment, r.binding.Address)

			continue
		}

		if c.schedule == nil {
			c.logf("%s: watching printer %s and driving %s at brightness %d",
				name, r.binding.DeviceID, r.binding.Address, r.binding.Brightness)

			continue
		}

		// One line naming the zone, because a schedule judged in the wrong clock is a light
		// that is wrong every evening and says nothing about it. The fixed brightness is named
		// too: it is what an instance with a schedule still runs at between windows.
		c.logf("%s: watching printer %s and driving %s at the brightness of brightness_schedule judged in %s, with %d standing wherever no window covers the moment",
			name, r.binding.DeviceID, r.binding.Address, c.schedule.zoneName, r.binding.Brightness)
	}

	// One light, one preview. A watch with several bindings offers none: the route names no
	// binding, and firing one of several would be picking a light on the operator's behalf. See
	// previewHeld.
	var offered *previewTarget

	if len(runners) == 1 {
		offered = &previewTarget{pen: runners[0].pen, where: runners[0].binding.Address}
		offerPreview(offered)
	}

	var group sync.WaitGroup

	done := make(chan struct{})

	for _, r := range runners {
		r := r

		group.Add(4)

		go func() {
			defer group.Done()

			r.pen.run(ctx)
		}()

		go func() {
			defer group.Done()

			r.drive.run(ctx)
		}()

		// The printer package reports what it refused to believe and never changes the
		// snapshot because of it. Draining that channel is the only way a person learns that a
		// read failed or that the subscription ended.
		go func() {
			defer group.Done()

			for {
				select {
				case <-ctx.Done():
					return
				case problem, ok := <-r.source.Problems():
					if !ok {
						return
					}

					c.logf("%s: %v", name, problem)
				}
			}
		}()

		go func() {
			defer group.Done()

			if runErr := r.source.Run(ctx); runErr != nil {
				c.logf("%s: stopped watching printer %s: %v", name, r.binding.DeviceID, runErr)
			}
		}()
	}

	go func() {
		group.Wait()

		// The writers have returned, so nothing is left to paint a preview: the route falls
		// back to saying so rather than storing pixels in a slot nobody reads.
		if offered != nil {
			withdrawPreview(offered)
		}

		close(done)
	}()

	return cancel, done, nil
}

// bindingStatus is one binding's answer on the status route: the reading, the frame and the last
// send a single-binding install reads, with the controller and segment they belong to.
type bindingStatus struct {
	// Controller is the controller as the operator wrote it, and Segment is the one this
	// binding drives with -1 for the whole controller. Together they name the binding, which is
	// what a caller with several needs in order to tell two frames apart.
	Controller string `json:"controller"`
	Segment    int    `json:"segment"`

	Printer  printerView `json:"printer"`
	Reading  readingView `json:"reading"`
	Frame    frameView   `json:"frame"`
	LastSend *sendView   `json:"last_send"`
}

// The per-binding store, which is empty for a watch holding one binding.
//
// A single-binding watch publishes into the older store /status has always read, so an install
// built from settings answers exactly the body it answered before this change. A watch holding
// several publishes here instead, because one body cannot name two bindings and a route that
// answered for whichever binding wrote last would be a route that lies about which light it is
// describing. The two stores are not merged: the route reads this one first and falls through to
// the other, and a watch never writes both.
var (
	bindingMu   sync.Mutex
	bindingHeld []bindingStatus
)

// inBinding applies one change to a binding's answer, adding the binding on its first change.
func inBinding(b binding, change func(*bindingStatus)) {
	bindingMu.Lock()
	defer bindingMu.Unlock()

	for i := range bindingHeld {
		if bindingHeld[i].Controller == b.Address && bindingHeld[i].Segment == b.Segment {
			change(&bindingHeld[i])

			return
		}
	}

	held := bindingStatus{Controller: b.Address, Segment: b.Segment}
	change(&held)

	bindingHeld = append(bindingHeld, held)
}

// publishBinding records one binding's reading and the frame decided from it.
func publishBinding(b binding, snapshot printer.Snapshot, frame render.Frame) {
	inBinding(b, func(held *bindingStatus) {
		seen := viewOf(snapshot, frame)
		held.Printer, held.Reading, held.Frame = seen.Printer, seen.Reading, seen.Frame
	})
}

// recordBinding records one attempt to write, for one binding.
func recordBinding(b binding, report sendView) {
	inBinding(b, func(held *bindingStatus) { held.LastSend = &report })
}

// bindingsSeen is every binding that has published an answer, in the order they first did, as a
// copy: the caller gets the bodies and not the lock.
func bindingsSeen() []bindingStatus {
	bindingMu.Lock()
	defer bindingMu.Unlock()

	return append([]bindingStatus(nil), bindingHeld...)
}

// forgetBindings empties the per-binding store. A test uses it between watches; the process
// never needs it, because a watch holding one binding never writes here and a process that
// restarts begins with an empty store.
func forgetBindings() {
	bindingMu.Lock()
	defer bindingMu.Unlock()

	bindingHeld = nil

	bindingShapeMu.Lock()
	defer bindingShapeMu.Unlock()

	bindingShapes = nil
}

// bindingShape is what one binding draws on: the light's own matrix, and the run of it a borrowed
// segment drives.
//
// It exists because a thumbnail has to know how big the picture is, and the only honest source of
// that is the writer: the controller's own report of its geometry, and the bounds the segment had
// when it was borrowed. A route that read /json/info for itself would answer for whatever the light
// says now, in a second request, on a light that may not answer at all — and the Overview thumbnail
// is precisely the picture that has to draw for a light that has stopped answering.
type bindingShape struct {
	// geometry is the matrix the controller reported, and the zero panel for a strip.
	geometry panel

	// span is the run a borrowed segment occupies, and nil for a binding that drives the whole
	// light. It is what makes the strip thumbnail the binding's own zone rather than somebody
	// else's 48 pixels.
	span *[2]int

	// layout is the arrangement this binding composes a panel with — the store's row for it, or the
	// `layout` setting when it has none — and laidOut says one was published at all.
	//
	// It travels here because the thumbnail has to be composed by the same arrangement the painter
	// uses: a picture drawn from the instance's setting while the panel composes with its own row
	// would be a thumbnail of a light that does not exist. laidOut is a second field rather than a
	// test on the layout, because a binding deliberately laid out with every part switched off draws
	// nothing, and that is not the same fact as "nothing was published".
	layout  layout
	laidOut bool

	// paused, word, wordColour, plain and preset are what the light is showing instead of its steady
	// frame: a pause that switched it off, a state's word, a moment's colour, or a preset the light
	// itself is running. All zero means the steady frame is what is on the wall.
	//
	// They are published by the writer, which is the only thing that decides them, and read by the
	// thumbnail route, which must never ask the light.
	paused     bool
	word       string
	wordColour wled.RGB
	plain      *render.Frame
	preset     string
}

// kind is panel or strip, in the words the routes use.
func (s bindingShape) kind() string {
	if s.geometry.matrix() {
		return snapshotKindPanel
	}

	return snapshotKindStrip
}

// composing is the arrangement to draw this binding's picture with: the one the binding published,
// and the instance's own setting for a shape that published none.
//
// The fallback is real and not a formality: a test's hand-built shape, and a frame published before a
// writer existed, both arrive with nothing laid out, and the picture they describe is the one the
// instance composes. laidOut is what tells "published nothing" from "published a layout that draws
// nothing", which are different pictures.
func (s bindingShape) composing(fallback layout) layout {
	if s.laidOut {
		return s.layout
	}

	return fallback
}

// pixelsOf is the picture this binding draws for one frame: the panel's own geometry, the borrowed
// run, or the run the frame says the light holds.
//
// The last case is the whole-light strip before the controller has said how many LEDs it drives,
// which is a width of zero and a frame of nothing to draw: a thumbnail of a light whose length is
// unknown would be a picture of a light that does not exist.
func (s bindingShape) pixelsOf(frame render.Frame) (int, int) {
	if s.geometry.matrix() {
		return s.geometry.width, s.geometry.height
	}

	if s.span != nil {
		return s.span[1] - s.span[0], 1
	}

	return frame.Total, 1
}

// The shape of each binding, beside the per-binding store above and published for the same reason:
// a route that is asked what a light is drawing needs the display as well as the frame, and both
// come from the watch rather than from a second reading of the light.
//
//nolint:gochecknoglobals // the one thing GET /frames reads out of the watch.
var (
	bindingShapeMu sync.Mutex
	bindingShapes  map[string]bindingShape
)

// shapeKey names one binding's shape the way the status store names the binding: by its controller
// and its segment, which is the pair that survives a printer being renamed.
func shapeKey(controller string, segment int) string {
	return fmt.Sprintf("%s#%d", controller, segment)
}

// lightName is a controller's address as the moment table keys it: the name the routes give a light,
// which is its key without the scheme (`http://10.0.0.136` is `10.0.0.136`). An address this module
// cannot parse answers with the address itself, which is the name the moment routes would have
// written for it.
func lightName(address string) string {
	key, err := controllerKey(address)
	if err != nil {
		return strings.TrimRight(strings.TrimSpace(address), "/")
	}

	return nameOfController(key)
}

// publishShape records what one binding draws on, as its writer last saw it.
func publishShape(b binding, shape bindingShape) {
	bindingShapeMu.Lock()
	defer bindingShapeMu.Unlock()

	if bindingShapes == nil {
		bindingShapes = make(map[string]bindingShape, 1)
	}

	bindingShapes[shapeKey(b.Address, b.Segment)] = shape
}

// shapeOf is the shape recorded for one binding, and the zero shape for one that has not published
// — which draws nothing, and says so rather than guessing a width.
func shapeOf(controller string, segment int) bindingShape {
	bindingShapeMu.Lock()
	defer bindingShapeMu.Unlock()

	return bindingShapes[shapeKey(controller, segment)]
}

// previewTarget is the writer POST /preview-finished fires the finished word at, and the
// controller it writes, for the sentences the route and the log need.
type previewTarget struct {
	pen   *writer
	where string
}

// previewHeld is the one light a preview may be fired at, and nothing when there is no such
// light.
//
// The route cannot reach a writer any other way: a writer is built inside start and owned by its
// own goroutine, and the whole point of that ownership is that nothing else touches the
// controller. So the one writer a single-binding watch builds is offered here, and taken back
// when that watch's goroutines have all returned — because a route that answered after the
// writer had returned would be promising a picture nothing was left to paint, and the pixels it
// stored would sit in a slot nobody reads.
//
// A watch holding several bindings offers nothing. The route names no binding, so firing one of
// several would be picking a light on the operator's behalf, and picking wrong would put the
// word on somebody's desk light.
//
//nolint:gochecknoglobals // the one thing the preview route reads out of the watch.
var previewHeld atomic.Pointer[previewTarget]

// offerPreview publishes the one writer a preview may be fired at.
func offerPreview(target *previewTarget) { previewHeld.Store(target) }

// withdrawPreview takes that offer away, and only when it is this watch's own: a second
// configuration replaces the watch, and the newer offer must survive the older watch stopping.
func withdrawPreview(target *previewTarget) { previewHeld.CompareAndSwap(target, nil) }

// previewNow is the offered writer, or nil when no single light is being driven.
func previewNow() *previewTarget { return previewHeld.Load() }

// forgetPreview takes the offer away. A test uses it between watches; the process never needs
// it, because a process that starts begins with nothing offered.
func forgetPreview() { previewHeld.Store(nil) }
