package main

import (
	"context"
	"sync"
	"time"
)

// Where the set in force came from, in the words GET /bindings answers with. A list that is not
// the one the settings file shows is the first thing a person has to be able to explain, so the
// answer carries this rather than leaving it to be worked out from a restart.
const (
	originSettings = "the bindings setting of this instance"
	originLegacy   = "controller_address, printer, brightness and segment: the four settings that came before bindings"
	originRoutes   = "a change made through this module's own routes, which the settings do not carry"
)

// runner owns the one watch this process runs, and the set of bindings it was built from.
//
// It exists because a set of bindings can now change while the process is running: the
// editor's routes add, change and remove a binding, and each of those has to take effect in
// the writer that is already talking to the controller — no restart, because a restart is not
// something this process can do to itself and the host only restarts it when a person saves
// its settings.
//
// The rule it keeps is the one configure kept when a set could only arrive from the settings:
// what runs is one set, it is judged before anything is built, and the set being replaced is
// waited for rather than only cancelled — a writer gives a borrowed segment back on its way
// out, and a second writer that borrowed the same segment before that happened would capture
// this module's own bar as the segment's original state and then give that bar back as though
// it were the operator's.
//
// The zero value is not usable: newSource and newLight are how the process reaches a printer
// and a controller, and a test supplies fakes so the same code runs with no host and no light.
type runner struct {
	// mu guards every field below, and is held for the whole of one change: two requests
	// arriving at once must not each build their answer from the set the other is replacing.
	//
	// It is held across the stop, which waits up to the grace for a writer to return. A reader
	// of the set therefore waits a moment behind a change whose controller is unreachable —
	// the alternative is a change that answers before the previous writer has given its
	// segment back, and that is the failure this whole type exists to prevent.
	mu     sync.Mutex
	held   bindings
	cancel context.CancelFunc
	done   chan struct{}

	// origin is where the set in force came from, in the words GET /bindings answers with. It
	// is the first thing a reader of a list that is not the one in the settings file needs.
	origin string

	// newSource builds the reader for one binding's printer, and newLight the client for one
	// controller. One light is built per controller however many bindings sit on it.
	newSource func(binding) (watchSource, error)
	newLight  func(address string) (strip, error)

	// logf is where the watch's own lines go. Nil means the process's log.
	logf func(format string, args ...any)

	// tick overrides the driver's interval. Zero keeps the process's own.
	tick time.Duration

	// grace bounds how long a change waits for a writer to give its segment back. Zero means
	// shutdownGrace, which is the bound a stop already uses.
	grace time.Duration

	// schedule is the time-of-day brightness the settings describe, and nil when they describe
	// none. It is not part of a set of bindings — the schedule is one per instance, judged in
	// one zone — but it does have to travel with one, because a set built by a route change
	// starts writers that must land on the same brightness the settings asked for. See
	// setSchedule.
	schedule *brightnessSchedule
}

// set is the set in force, as a copy: the caller gets the bindings and not the lock.
func (r *runner) set() bindings {
	r.mu.Lock()
	defer r.mu.Unlock()

	return cloneBindings(r.held)
}

// setOrigin records where the set in force came from. configure says which of its two shapes
// produced it; a change through the routes says so for itself.
func (r *runner) setOrigin(where string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.origin = where
}

// setSchedule records the time-of-day brightness this instance runs at, for the writers the
// next build starts.
//
// It is guarded by the same lock a change takes, because a route change builds writers from
// whatever this holds: a schedule written while a change is in flight would otherwise reach
// half the writers of one build. configure calls it before apply, so the watch it starts is
// built from the schedule the settings it just judged describe.
func (r *runner) setSchedule(schedule *brightnessSchedule) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.schedule = schedule
}

// origin is where the set in force came from, in the words the routes answer with.
func (r *runner) source() string {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.origin == "" {
		return "no set is running"
	}

	return r.origin
}

// apply makes next the one set this process runs, replacing whatever ran before it.
//
// An empty next is legal and means what it says: this module drives nothing, and the light is
// left as the last frame found it. Everything else is held to bindings.check before the
// running watch is touched, so a set that breaks a rule never interrupts a working light.
//
// A refusal after the old watch has stopped — which is only possible if the new set's
// printers or controllers cannot be built, both of which check already reads — puts the old
// set back, and the returned error says the change did not happen. A light that went dark
// because a refused edit was applied halfway would be the worse answer.
func (r *runner) apply(parent context.Context, next bindings) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.applyLocked(parent, next)
}

// revise changes the set in force: change is handed the set the process is running and returns
// the one to run instead, or the reason it cannot.
//
// The read of the current set and the build of the next one happen under one lock, which is
// what makes a delete beside an add mean one thing rather than two — otherwise both requests
// would read the same set and the second would silently undo the first.
func (r *runner) revise(parent context.Context, change func(bindings) (bindings, error)) (bindings, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	next, err := change(cloneBindings(r.held))
	if err != nil {
		return nil, err
	}

	if err := r.applyLocked(parent, next); err != nil {
		return nil, err
	}

	// A set changed through these routes is not the one the settings file says, and the answer
	// to GET /bindings has to say which it is.
	r.origin = originRoutes

	return cloneBindings(r.held), nil
}

// stop asks the watch to stop and waits for it to have given back what it borrowed.
func (r *runner) stop() {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.stopLocked()
}

// applyLocked is apply for a caller already holding the lock, and it is the whole of the
// change: judge, stop, build, or put back.
func (r *runner) applyLocked(parent context.Context, next bindings) error {
	if len(next) > 0 {
		if err := next.check(); err != nil {
			return err
		}
	}

	previous := cloneBindings(r.held)

	r.stopLocked()

	// The per-binding store /status reads belongs to the watch that filled it. Clearing it
	// here is what stops a binding that has just been deleted from answering on the status
	// route for as long as the process runs; the new watch fills it again within a tick, and
	// a single-binding watch never writes there at all.
	forgetBindings()

	if len(next) == 0 {
		r.held = nil

		return nil
	}

	cancel, done, err := r.build(parent, next)
	if err == nil {
		r.cancel, r.done, r.held = cancel, done, next

		return nil
	}

	// Put back what was running. A change that cannot start is a change that did not happen,
	// and the operator's light must not be the thing that pays for it.
	if len(previous) > 0 {
		if again, back, rebuildErr := r.build(parent, previous); rebuildErr == nil {
			r.cancel, r.done, r.held = again, back, previous
			r.logf("%s: the change was refused and the set it would have replaced is running again: %v",
				name, err)

			return err
		} else {
			r.logf("%s: the change was refused, and the set it would have replaced did not start again either: %v",
				name, rebuildErr)
		}
	}

	r.held = nil

	return err
}

// stopLocked cancels the watch in force and waits for its goroutines, including every
// writer's give-back. It clears the fields first, so a second call does nothing.
func (r *runner) stopLocked() {
	cancel, done := r.cancel, r.done
	r.cancel, r.done = nil, nil

	if cancel == nil || done == nil {
		return
	}

	cancel()

	grace := r.grace
	if grace <= 0 {
		grace = shutdownGrace
	}

	wait, stop := context.WithTimeout(context.Background(), grace)
	defer stop()

	select {
	case <-done:
	case <-wait.Done():
		r.logf("%s: the watch had not stopped within %s of being asked, so a borrowed segment may still be holding this module's last frame",
			name, grace)
	}
}

// build starts one watch on one set and returns its stop and its completion channel.
//
// A set of one publishes into the store /status has always read, which is what keeps an
// install configured in the settings pane answering exactly the body it has always answered.
// A set of several publishes per binding, because one body cannot say which of two lights its
// frame describes and a route that answered for whichever binding wrote last would be a route
// that lies.
func (r *runner) build(parent context.Context, set bindings) (context.CancelFunc, chan struct{}, error) {
	config := watchConfig{
		bindings:  set,
		newSource: r.newSource,
		newLight:  r.newLight,
		logf:      r.logf,
		tick:      r.tick,
		schedule:  r.schedule,
	}

	if len(set) > 1 {
		config.publish = publishBinding
		config.record = recordBinding
	}

	return config.start(parent)
}

// cloneBindings is a set of its own, so that a caller can keep the body it was handed while
// the process changes what runs.
func cloneBindings(set bindings) bindings {
	if set == nil {
		return nil
	}

	return append(bindings(nil), set...)
}
