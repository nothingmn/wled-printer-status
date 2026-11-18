package main

import (
	"context"
	"sync"
	"time"

	"github.com/nothingmn/wled-printer-status/internal/printer"
	"github.com/nothingmn/wled-printer-status/internal/render"
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
				pen = newSegmentWriter(light, b.Segment, b.Address, c.logf)
			} else {
				pen = newWriter(light, b.Brightness, c.logf)
			}

			pen.gate = door

			if c.record != nil {
				report := c.record
				pen.record = func(sent sendView) { report(b, sent) }
			}

			drive := newDriver(source, pen, light, c.logf)

			if c.tick > 0 {
				drive.tick = c.tick
			}

			if c.publish != nil {
				show := c.publish
				drive.pub = func(snapshot printer.Snapshot, frame render.Frame) { show(b, snapshot, frame) }
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

		c.logf("%s: watching printer %s and driving %s at brightness %d",
			name, r.binding.DeviceID, r.binding.Address, r.binding.Brightness)
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
}
