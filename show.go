// What this module's own store says a light should show, as the painter reads it: a state's
// colour and word, a pause, and the brightness windows.
//
// # Why this file exists, and the rule it keeps
//
// The store is the operator's: the Colours screen edits a colour and a word per state, the header's
// button writes a pause, and the Brightness screen edits a list of windows. What it holds is an
// **override** and never a copy of a default — store.go says so, statetable.go says so — so the
// painter must not carry a second table of colours to fall back on. It asks here, and a question
// the store cannot answer is answered by the renderer, which is the only place the defaults live.
//
// That is the whole shape of this file: three questions with a "no answer" that means "the code's
// own picture stands", and one wrapper that reads the document for them.
//
//	show(state)      the colour in force and the word the state carries, or false for a state the
//	                 table does not hold.
//	paused(at)       the pause in force for this binding at an instant, or false.
//	level(at)        the brightness this module's own window list asks for, or false when the
//	                 list is empty and the `brightness_schedule` setting is still the source.
//
// # The clock, and why nothing here keeps one
//
// A pause and a word's hold are both stored as an absolute instant, and both are judged against
// the clock at the moment the light is painted. Nothing arms a timer: this process is
// reconstructed on every settings save (docs/product-requirements.md §2.8), so a timer would be a
// pause that ended the first time somebody pressed save, hours early, with nothing in the log.
// pause.go says the same thing about the store's own shape; this is the other half of it.
//
// # And why the document is read again rather than kept
//
// The three tables are edited while the process runs, by these routes and by anybody with an
// editor on the volume, so a copy read once at start is a copy that goes out of date with no way
// to notice. tablesapi.go's rule for its own reads is "the file is the record"; the painter's
// reads follow it, with the one addition a loop turning once a second needs: the read is kept
// until the file's own stamp or this process's own write counter says it changed. A pass that has
// nothing new to learn costs one stat.
package main

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/nothingmn/wled-printer-status/internal/wled"
)

// show is one state as the store has it: the colour it is drawn in, where that colour came from,
// and the word it may carry with the length that word holds.
//
// The colour is answered whether or not anybody overrode it, because a word is drawn in it and the
// word is the store's business; `origin` is what lets the painter leave the renderer's own picture
// alone when nobody has spoken. So there is one reader of a state's colour and not two, and the
// default is never copied out of the renderer into a table somebody has to keep in step.
type show struct {
	colour wled.RGB
	origin string

	word  string
	holds string
}

// overridden says the colour is the store's rather than the renderer's.
func (s show) overridden() bool { return s.origin == fromStore }

// showOf is one row of the state table as what the light shows for it.
func showOf(row stateRow) show {
	colour, origin := row.colour()
	word, _ := row.word()
	holds, _ := row.holds()

	return show{colour: colour, origin: origin, word: word, holds: holds}
}

// holdLength is how long one of a state word's four holds lasts, and false for `forever` and for
// anything else. It reads the same values checkHolds accepts, so a hold the store holds is a hold
// this can measure.
func holdLength(holds string) (time.Duration, bool) {
	switch holds {
	case hold30s:
		return 30 * time.Second, true
	case hold60s:
		return time.Minute, true
	case hold5m:
		return 5 * time.Minute, true
	}

	return 0, false
}

// wordUntil is the instant a state's word stops being shown, counted from the moment its state
// began, and the zero time for a word that does not end.
//
// A word's hold is a length of the state, not of the module: the state began when the printer
// entered it, and thirty seconds from then is the thirty seconds the operator asked for. The
// moment is carried rather than remembered here, because the writer is reconstructed and the
// driver is the one thing that saw the transition.
func wordUntil(holds string, since time.Time) time.Time {
	length, finite := holdLength(holds)
	if !finite || since.IsZero() {
		// `forever`, and the one case with no state to count from, which is a frame submitted
		// without one: a word is then shown for as long as its state lasts.
		return time.Time{}
	}

	return since.Add(length)
}

// wordInForce says whether a state's word is still the light's picture at a moment.
//
// It is the whole of "has this word expired", and it is computed at paint time and never cached:
// a value computed once and kept would be a word that ended when the last read happened rather
// than when its hold ran out.
func wordInForce(holds string, since, at time.Time) bool {
	until := wordUntil(holds, since)

	return until.IsZero() || at.Before(until)
}

// pauseEnd is when a pause ends: an instant, or no end at all.
//
// It is not the stored row. What the painter needs is the answer to "how much longer", and a row
// carries a scope, a length somebody chose and the moment it was set as well — none of which
// changes a write.
type pauseEnd struct {
	until   time.Time
	forever bool
}

// phrase is the end of a pause as a sentence for a log line: the instant it ends, or that it does
// not end until somebody resumes it.
func (p pauseEnd) phrase() string {
	if p.forever {
		return " with no end, so it stays off until it is resumed"
	}

	return " until " + p.until.UTC().Format(time.RFC3339)
}

// endOf is a stored pause's end, in the shape the painter waits on.
func endOf(one storedPause) pauseEnd {
	if one.Expires == nil {
		return pauseEnd{forever: true}
	}

	return pauseEnd{until: *one.Expires}
}

// brightnessNow is what this module's own window list asks for at one moment.
type brightnessNow struct {
	level   int
	why     string
	covered bool
}

// lights is the store as one painter reads it: five questions, each with the answer "nothing of
// mine to say".
//
// It is an interface for one reason: a test paints from a table it wrote by hand, with a clock it
// moves, and never touches a file. The process's only implementation is storeLights below, and a
// writer built without one behaves exactly as this module did before the store was obeyed.
//
// The last two are the two tables the picture itself comes from rather than the light's condition:
// **layout** is the per-binding arrangement of a panel, and **moment** is what a transition shows.
// Neither ever answers "nothing of mine" in a way that leaves the light blank — see their comments
// for the default each falls back to, which is the code's and not a copy in the document.
type lights interface {
	show(state string) (show, bool)
	paused(at time.Time) (pauseEnd, bool)
	level(at time.Time) (brightnessNow, bool)
	moment(id string) (momentEntry, bool)
	layout() (layout, bool)
}

// storeLights is one binding's view of this module's own configuration: the document's three
// tables, read through the tables the routes edit, for one printer.
//
// One per binding, because a pause is kept per binding and a state's colour is not: two bindings on
// one module can be paused independently, and asking a shared reader "am I paused" would be
// answering for whichever printer happened to be asked last.
type storeLights struct {
	// tables is the store the routes write. It owns the directory and the write counter.
	tables *configTables

	// printer is the binding's own device id, which is the name a pause on one binding is kept
	// under, and empty for a writer that is not a binding (a test's).
	printer string

	// light is the binding's light by the name the moment table keys it with — the controller's
	// address without its scheme — and empty for a writer that is not a binding.
	//
	// The moment table is keyed on (light, printer) because the same printer on two lights carries
	// two independent sets (docs/product-requirements.md §2.3), so a reader that did not know its
	// light could not find its own rows.
	light string

	// zone is the `timezone` setting's name, which is the zone the windows are judged in. It is a
	// function rather than a value because the setting is the host's and arrives on configure,
	// which is after this may be built. Nil reads the running settings.
	zone func() string

	// logging is where a refusal goes: a document this build cannot act on, or a window list with
	// no zone to judge it in. Nil is the process's own log.
	logging func(format string, args ...any)

	// mu guards the three fields below. The driver asks one of these questions on its own
	// goroutine and the writer asks two on its own, so the reader is safe for both.
	mu sync.Mutex

	// held is the last read of the document, with the stamp of what it was read from.
	held *storeView

	// zoned is the zone the windows are judged in, kept until the setting names another one.
	zoned     *time.Location
	zonedName string

	// said is the last refusal this reader reported, so that a document left broken costs one line
	// rather than one a second.
	said string
}

// storeStamp is what a read of the store is keyed on: where the document is, how many writes this
// process has made to it, and the file's own modification time and size, which is how a change
// made by somebody else — a person with an editor, another process — is noticed.
type storeStamp struct {
	dir      string
	writes   uint64
	modified time.Time
	size     int64
}

// storeView is one read of the document as the painter needs it: the tables, already checked, and
// the refusal that stopped them being read when there was one.
type storeView struct {
	stamp   storeStamp
	rows    []stateRow
	pauses  []storedPause
	windows []windowRow

	// moments and layouts are the two tables a picture is composed from: what a transition shows
	// for this binding, and how a panel arranges its parts. They are kept here rather than read on
	// demand for the reason the three above are — one read of one file per pass of the loop, and
	// one refusal that stops the whole document being obeyed rather than half of it.
	moments []storedMoment
	layouts map[string]storedLayout

	// refused is why the document could not be acted on. The painter then draws from the
	// renderer's table, the fixed brightness and no pause — the picture every install had before
	// the store existed — and says why once.
	refused error
}

// show is one state's colour and word, and false for a name the state table does not hold — an
// unknown word the host reported, or a name that is not a state at all.
func (s *storeLights) show(state string) (show, bool) {
	row, held := rowNamed(s.view().rows, state)
	if !held {
		return show{}, false
	}

	return showOf(row), true
}

// paused is the pause in force for this binding at an instant, and false when there is none or it
// has expired.
//
// A pause on the binding itself wins over the global one, which is the order pause.go's own
// per-binding answer uses: a screen reads the two scopes the same way, and a row's own pause is the
// more particular statement of the two.
func (s *storeLights) paused(at time.Time) (pauseEnd, bool) {
	rows := s.view().pauses

	if one, held := bindingPause(rows, s.printer, at); held {
		return endOf(one), true
	}

	if one, held := globalPause(rows, at); held {
		return endOf(one), true
	}

	return pauseEnd{}, false
}

// level is the brightness this module's own window list asks for at an instant, and false when the
// list is empty — which is what makes the `brightness_schedule` setting the source whenever nobody
// has edited the list.
//
// The list is judged by the settings' own evaluator (`brightnessSchedule.level`) in the `timezone`
// setting's zone, so a window written through a route and a window pasted into the settings cannot
// mean two different things. A list with no zone is refused: it is not judged in this process's
// clock, because that clock is the container's and is almost always UTC.
func (s *storeLights) level(at time.Time) (brightnessNow, bool) {
	view := s.view()

	if len(view.windows) == 0 {
		return brightnessNow{}, false
	}

	location, named, err := s.location()
	if err != nil {
		s.say(fmt.Sprintf("%s: this module's own brightness windows are not being judged: %v", name, err))

		return brightnessNow{}, false
	}

	windows := make([]scheduleWindow, 0, len(view.windows))
	for _, one := range view.windows {
		windows = append(windows, one.parsed)
	}

	judged := &brightnessSchedule{windows: windows, zone: location, zoneName: named}

	level, why, covered := judged.level(at)

	// The sentence says whose list decided it, because the two are edited in different places and a
	// person reading "the light's brightness is 40" needs to know which one to change.
	return brightnessNow{level: level, why: "this module's own windows: " + why, covered: covered}, true
}

// moment is what this binding's row for one moment says the light shows, and whether that answer
// came from the document.
//
// A moment with no row answers the default `momentDefault` gives it, which is not a copy of what
// the module does — it *is* what the module does: only the finished moment is on, showing the word
// FINISHED for a minute. So an install that never opens the Events screen fires exactly the event
// this module has fired since the word was built, and this reader is what says so.
//
// A row is found by the (light, printer) pair — the table's own key — so the same printer on two
// lights carries two sets, which is what multi-room means (§2.3).
func (s *storeLights) moment(id string) (momentEntry, bool) {
	for _, one := range s.view().moments {
		if !strings.EqualFold(strings.TrimSpace(one.Light), s.light) || one.Printer != s.printer {
			continue
		}

		if !strings.EqualFold(strings.TrimSpace(one.Moment), id) {
			continue
		}

		return one.momentEntry, true
	}

	return momentDefault(id), false
}

// layout is the arrangement this binding composes with, and false when it has no row of its own —
// in which case the painter falls back to the `layout` setting, which is the migration source
// (docs/product-requirements.md §4).
//
// The row is resolved against the defaults on the way out, so a row that names one field answers for
// all eight and a screen and the painter read one picture. A row that will not check refuses the
// whole document in `read`, so nothing here refuses anything.
func (s *storeLights) layout() (layout, bool) {
	row, held := layoutFor(s.view().layouts, s.printer)
	if !held {
		return layout{}, false
	}

	return row.resolved().shape(), true
}

// location is the zone the windows are judged in, and the refusal when there is none to judge them
// in. It is kept until the setting names another one, so a list is judged without loading a zone on
// every pass.
func (s *storeLights) location() (*time.Location, string, error) {
	named := strings.TrimSpace(s.zoneName())

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.zoned != nil && s.zonedName == named {
		return s.zoned, s.zonedName, nil
	}

	location, err := loadZone(named)
	if err != nil {
		return nil, "", err
	}

	s.zoned, s.zonedName = location, location.String()

	return s.zoned, s.zonedName, nil
}

// zoneName is the IANA zone the windows are judged in: the `timezone` setting, or the function a
// test hands over instead.
func (s *storeLights) zoneName() string {
	if s.zone != nil {
		return s.zone()
	}

	return settings().Timezone
}

// view is the document as it stands, read again only when something says it has changed.
//
// The lock is held across the read, and that is deliberate rather than careless: the driver asks one
// of these questions on its own goroutine and the writer asks two on its own, and two readers of one
// changed document would otherwise both parse it. What is inside is one small file.
func (s *storeLights) view() storeView {
	dir, writes := s.tables.stamp()
	if dir == "" {
		// A module the host granted no directory runs from its settings, and has nothing to say
		// about a state's colour, a pause or a window.
		return storeView{}
	}

	info, err := os.Stat(storePath(dir))
	if err != nil {
		return storeView{}
	}

	stamp := storeStamp{dir: dir, writes: writes, modified: info.ModTime(), size: info.Size()}

	s.mu.Lock()

	if s.held != nil && s.held.stamp == stamp {
		held := *s.held
		s.mu.Unlock()

		return held
	}

	view := storeView{stamp: stamp}

	s.read(&view)

	s.held = &view

	s.mu.Unlock()

	// Outside the lock, because `say` takes it: one refusal is reported, and a store left broken
	// costs one line rather than one a second.
	if view.refused != nil {
		s.say(fmt.Sprintf("%s: this module's configuration store cannot be acted on, so the light is drawn from the defaults until it is fixed: %v",
			name, view.refused))
	}

	return view
}

// read fills one view from the document, and is the whole of what a refusal can be about: the file,
// then each of the tables.
//
// A table that will not read refuses the whole document rather than being skipped, for the reason
// the routes refuse a read of it: a bad line in a file a person edits is a line a person has to
// look at, and half a store obeyed is a light with no explanation for what it is showing.
func (s *storeLights) read(view *storeView) {
	found, err := loadStore(view.stamp.dir)
	if err != nil {
		view.refused = err

		return
	}

	if view.rows, err = rowsOf(found.Config); err != nil {
		view.refused = err

		return
	}

	if view.pauses, err = pauseRows(found.Config); err != nil {
		view.refused = err

		return
	}

	if view.windows, err = windowsOf(found.Config); err != nil {
		view.refused = err

		return
	}

	// The five tables are judged together: a document is obeyed as one thing or not at all, which is
	// what makes "the store's answer or the code's" a question with two answers rather than six.
	if view.moments, err = momentsStored(found.Config); err != nil {
		view.refused = err

		return
	}

	if view.layouts, err = layoutRows(found.Config); err != nil {
		view.refused = err
	}
}

// say reports one refusal, and stops repeating it.
//
// A store left broken fails on every pass of the writer's loop and on every tick of the driver's, and
// one line per second is a log nobody can read: the first is the one that matters, and a second
// sentence that differs from it is a different fact. The lock is its own because two goroutines ask
// this reader questions.
func (s *storeLights) say(message string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.said == message {
		return
	}

	s.said = message

	s.logf("%s", message)
}

// logf is where a refusal goes, and the process's own log when nothing was handed over.
func (s *storeLights) logf(format string, args ...any) {
	if s.logging != nil {
		s.logging(format, args...)

		return
	}

	logf(format, args...)
}
