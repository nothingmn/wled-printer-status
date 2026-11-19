package main

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Where the set in force came from, in the words GET /bindings answers with. A list that is not
// the one the settings file shows is the first thing a person has to be able to explain, so the
// answer carries this rather than leaving it to be worked out from a restart.
const (
	originSettings = "the bindings setting of this instance"
	originLegacy   = "controller_address, printer, brightness and segment: the four settings that came before bindings"
	originRoutes   = "a change made through this module's own routes, written into its configuration store"

	// originStore is the set this module read out of its own configuration store. It is a
	// separate answer from the settings on purpose: the settings were the migration source, the
	// store is what runs, and an answer that named the settings for a set the settings no longer
	// produce would be the first thing anyone debugging a stale light got wrong.
	originStore = "this module's own configuration store, which the settings seeded once and do not change again"

	// originNothing is the answer for an instance that was given no configuration at all, which
	// is what a fresh install of this manifest is: no options to send, no store yet, and a start
	// on the defaults the code holds. The store is written empty in the same breath, so the pages
	// that add the first light are the ones that fill it.
	originNothing = "nothing yet: no light is configured, and this module is running on the defaults its code holds"
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

	// layout is the operator's arrangement of a panel, as the settings describe it. It travels with
	// a set for the same reason the schedule does: a change made through the routes rebuilds every
	// writer, and a writer built without the layout would take the panel back to the picture it had
	// before the setting existed — in the middle of a print, with nothing on the instance to say
	// why. See setLayout.
	layout layout

	// tables is this module's own configuration store, which every writer built here is handed so
	// that the painter obeys it: a state's colour and word, the pause, and the brightness windows.
	// Nil means the module was granted no directory and runs from its settings, which is how this
	// module behaved before the store existed.
	tables *configTables
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

// setLayout records the operator's arrangement of a panel, for the writers the next build starts.
//
// Guarded by the same lock as a change, and for the same reason: a change made through the routes
// builds its writers from whatever this holds, and a layout written while a change is in flight
// would otherwise reach half of them. configure calls it before apply, so the watch it starts is
// built from the layout the settings it just judged describe.
func (r *runner) setLayout(shape layout) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.layout = shape
}

// shape is the arrangement of a panel this instance composes with, read under the same lock a change
// takes: a route drawing a thumbnail asks what the painter is composing with, and the answer has to
// be the one the next writer is built from.
//
// It is the `layout` setting's layout and not the per-binding layout table, because that table is a
// record this build does not draw from yet — a thumbnail composed from it would show a picture no
// panel is showing.
func (r *runner) shape() layout {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.layout
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

// reviseStored changes the set in force and writes it into this module's own configuration
// store, and it is the one path every binding route takes.
//
// It is the whole of a binding change: change is handed the set the process is running and returns
// the one to run instead, or the reason it cannot. The read of the current set, the decision and
// the build of the next one happen under one lock, which is what makes a delete beside an add mean
// one thing rather than two — otherwise both requests would read the same set and the second would
// silently undo the first.
//
// It has no sibling that changes the running set without writing the store, on purpose. There was
// one, and it is how a binding came to survive only until the next restart: a route that could take
// the cheap path did, and the answer it wrote said `applied: true` beside `persisted: false`.
//
// The order is the store first, the running set second, and it is not interchangeable. A write
// that cannot reach the store has to leave the light showing what it was showing, so the refusal
// is raised before the watch is touched at all. A refusal at the moment of asking is a smaller lie
// than a disappearance the operator finds out about by looking at a dark strip.
//
// The write itself goes through configTables.revise, which is the same read-change-rename the
// state, moment, layout, brightness-window and pause tables go through: one place that knows the
// document's shape, one lock around the read and the rename, and one answer to what a directory
// that cannot be written means.
//
// Lock order, which is the one thing this method has to get right: the runner's lock is held
// while the store's is taken, never the other way round. Nothing on the store's path waits on the
// watch — a tables route reads the running set before or after its own write, never inside it —
// so the two locks cannot sit waiting on each other.
func (r *runner) reviseStored(parent context.Context, change func(bindings) (bindings, error)) (bindings, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	previous := cloneBindings(r.held)

	next, err := change(cloneBindings(r.held))
	if err != nil {
		return nil, err
	}

	// Named and judged before the store is written. A set with no names is a set the layout table
	// has no key for, and a set that breaks a rule is one applyLocked would refuse after the
	// document had already been changed — so both are done here, where the file has not moved yet.
	if len(next) > 0 {
		next = next.identified()

		if err := next.check(); err != nil {
			return nil, err
		}
	}

	if err := r.keepBindings(next); err != nil {
		// Nothing has been applied. The set the light is being drawn from is the one it was
		// drawn from before this request, and the answer says the change was not made.
		return nil, fmt.Errorf("%w: %v", errNotKept, err)
	}

	if err := r.applyLocked(parent, next); err != nil {
		// The store holds a set the watch would not start. Writing the previous one back is what
		// keeps the document and the light saying the same thing; a write-back that fails is
		// logged rather than swallowed, because the next start would then run a set nobody chose.
		if backErr := r.keepBindings(previous); backErr != nil {
			r.logf("%s: a change was written to the store and then refused by the watch, and the set it replaced could not be written back: %v",
				name, backErr)
		}

		return nil, err
	}

	// The set in force is the store's now, and not a list this process holds until something
	// replaces it.
	r.origin = originRoutes

	return cloneBindings(r.held), nil
}

// keepBindings writes one set into this module's own configuration store.
//
// A module granted no directory has nowhere to keep a binding and says so rather than pretending:
// `needs.data` is how this module asks for one, and docs/product-requirements.md §4 makes the
// store the editing surface, so a change that cannot reach it is a change that did not happen.
func (r *runner) keepBindings(set bindings) error {
	if r.tables == nil {
		return errNoDataDirectory
	}

	entries, err := storeEntries(set)
	if err != nil {
		return err
	}

	_, err = r.tables.revise(func(config *storedConfig) { config.Bindings = entries })

	return err
}

// stop asks the watch to stop and waits for it to have given back what it borrowed.
func (r *runner) stop() {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.stopLocked()
}

// applyLocked is apply for a caller already holding the lock, and it is the whole of the
// change: name, judge, stop, build, or put back.
func (r *runner) applyLocked(parent context.Context, next bindings) error {
	if len(next) > 0 {
		// Named before it is judged, because a name is what the routes address and what the
		// layout table is keyed on, and two bindings of one printer must not arrive without
		// one. An existing name is never touched — see bindings.identified.
		next = next.identified()

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
// install built from settings answering exactly the body it has always answered.
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
		layout:    r.layout,
		tables:    r.tables,
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
