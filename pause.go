// The pause: the panic button in the header of every screen, at both scopes, kept in this module's
// own store with an absolute instant it ends at.
//
// # The rule, and the one exception
//
// docs/product-requirements.md §3 states it and the handoff repeats it because it has already cost
// arguments: **a pause switches the light off.** Not frozen at its last state — that is a picture of
// something no longer true — but one write and then zero: no frames, no log lines, no retries, until
// it resumes. Six lengths: ten minutes, one hour, six hours, one day, three days, forever.
//
// **The exception is a borrowed zone, and it must not be got wrong.** A binding that drives one
// segment of a strip somebody else also drives cannot switch the light off: the light is not only
// this module's, and the rest of that strip belongs to its owner. So the zone is **handed back** —
// restored to what was there before this module borrowed it — and the controller stays on for
// everybody else. That is the same give-back the writer already performs on the way out of every
// watch, in `writer.release`, which writes `RestoreSegment` with the segment exactly as `writer.borrow`
// captured it.
//
// # Why the expiry is an instant and not a timer
//
// **A timed pause has to survive a restart** (§3), so it lives in the store. And this process is
// reconstructed more often than that: every settings save restarts the module (§2.8), and every start
// reads the document afresh. A pause held in an in-memory timer would therefore be a pause that ends
// the first time somebody saves the settings — silently, hours early, with nothing in the log to say
// why the light came back on. So the document carries `expires`, an absolute RFC 3339 instant in UTC,
// and "is it paused now" is computed from the clock at read time. Nothing runs a timer, nothing has to
// fire, and an expired pause is simply a row whose instant has passed.
//
// # What the painter does with it
//
// A pause switches the light off, and then costs nothing: one write and no more until it ends. The
// writer asks this file's functions on every pass of its own loop — `globalPause` and
// `bindingPause` for "is one in force at this instant", and `pauseEffectOn` for what it does to a
// binding — so nothing is cached and nothing arms a timer. An instant that has passed is simply a
// row that is no longer in force, and the pass that finds it repaints at once rather than waiting
// for the next thing the printer says.
//
// The decision this file owns, and the one the writer must not make for itself, is which bindings go
// dark and which are handed back: `pauseEffectOn` answers "hands-back" for every borrowed binding,
// because switching that controller off would take the rest of somebody else's strip with it.
package main

import (
	"fmt"
	"strings"
	"time"
)

// The two scopes a pause may have.
const (
	// pauseGlobal is the header's "pause everything": every binding this module drives, at both
	// scopes' worth of effect.
	pauseGlobal = "global"

	// pauseOnBinding is one row's "pause": one binding, and the rest of the module carries on.
	pauseOnBinding = "binding"
)

// The six lengths a pause may be set for, in docs/product-requirements.md §3's own order and its own
// words: ten minutes, one hour, six hours, one day, three days, forever.
//
// They are written the way the moment table writes a length — `60s`, `5m` — so that one vocabulary of
// durations is not two. A day is twenty-four hours and not "this time tomorrow": the module has no
// calendar, and a pause that ended an hour later after a clock change would be a light that came back
// on at the wrong time of the morning.
const (
	pause10m = "10m"
	pause1h  = "1h"
	pause6h  = "6h"
	pause1d  = "1d"
	pause3d  = "3d"
)

// pauseChoices is what a pause may be set for, in the order the screen offers them. Anything else is
// refused by name rather than rounded to the nearest, for the reason a hold is: a pause of "about an
// hour" is not the thing anybody asked for.
//
//nolint:gochecknoglobals // one closed list, read and never written.
var pauseChoices = []string{pause10m, pause1h, pause6h, pause1d, pause3d, durationForever}

// pauseLengths is how long each of the five finite choices lasts. It deliberately has no entry for
// forever: forever is the absence of an expiry, and a row that carried both a length and no expiry
// would be two statements of one fact that could disagree.
//
//nolint:gochecknoglobals // one closed table, read and never written.
var pauseLengths = map[string]time.Duration{
	pause10m: 10 * time.Minute,
	pause1h:  time.Hour,
	pause6h:  6 * time.Hour,
	pause1d:  24 * time.Hour,
	pause3d:  72 * time.Hour,
}

// The two things a pause can do to a binding, as an answer reports them.
const (
	// effectOff is the rule: the light is switched off, and stays off until the pause ends.
	effectOff = "off"

	// effectHandsBack is the exception: the borrowed zone is put back exactly as it was found, and
	// the controller stays on for whoever else is using it.
	effectHandsBack = "hands-back"
)

// storedPause is one pause as the document holds it: which scope it is, the length somebody asked
// for, when it was set, and the absolute instant it ends at.
//
// `expires` absent *is* forever, and there is no second field saying so. Two fields for one fact is
// two statements that can disagree, and the one that would disagree is the one a later change reads.
type storedPause struct {
	// Scope is pauseGlobal or pauseOnBinding.
	Scope string `json:"scope"`

	// Printer is the binding a `binding` scope is about, and empty on a `global` one. It is the
	// host's device id, which is the binding's own name on these routes.
	Printer string `json:"printer,omitempty"`

	// Chosen is the length as it was chosen — one of pauseChoices — kept so that an answer can say
	// what somebody asked for rather than only when it ends.
	Chosen string `json:"chosen"`

	// SetAt is the instant the pause was set, which is what a screen shows beside "ends in".
	SetAt time.Time `json:"set_at"`

	// Expires is the absolute instant this pause ends at, and absent for a pause of forever. It is
	// what makes a timed pause survive a restart: nothing has to fire, and the question "is this
	// paused" is answered by comparing it with the clock.
	Expires *time.Time `json:"expires,omitempty"`
}

// forever says whether this pause has no end.
func (p storedPause) forever() bool { return p.Expires == nil }

// inForce says whether this pause is in force at an instant.
//
// It is the whole of "is it paused now", and it is computed and never cached: a value computed once
// and kept would be a pause that ended when the last read happened rather than when its instant
// passed. An expired row is not deleted by anything — a read is not a write, and the row is what says
// why the light stayed off and when it stopped.
func (p storedPause) inForce(now time.Time) bool {
	if p.Expires == nil {
		return true
	}

	return now.Before(*p.Expires)
}

// remaining is how long this pause has left at an instant, and nil for a pause of forever.
func (p storedPause) remaining(now time.Time) *time.Duration {
	if p.Expires == nil {
		return nil
	}

	left := p.Expires.Sub(now)
	if left < 0 {
		left = 0
	}

	return &left
}

// checkStoredPause refuses a pause this build cannot act on, naming what is wrong with it.
//
// It runs on the way in — a route will not write one — and again on the way out, because the document
// is a file a person can edit. The rules are the ones whose failure would be invisible: a scope this
// build does not know, a binding pause naming no binding or a global one naming a printer, a length
// outside the six, and the two ways the expiry and the length can disagree.
func checkStoredPause(one storedPause) error {
	switch strings.TrimSpace(one.Scope) {
	case pauseGlobal:
		if strings.TrimSpace(one.Printer) != "" {
			return fmt.Errorf(
				"a pause of global scope names the printer %q, and a global pause is every binding: write printer on a pause of %q scope",
				one.Printer, pauseOnBinding)
		}
	case pauseOnBinding:
		if strings.TrimSpace(one.Printer) == "" {
			return fmt.Errorf("a pause of %q scope names no printer, so it is not clear which binding is paused",
				pauseOnBinding)
		}
	default:
		return fmt.Errorf("the scope is %q, and a pause is %q or %q",
			one.Scope, pauseGlobal, pauseOnBinding)
	}

	if err := checkPauseLength(one.Chosen); err != nil {
		return err
	}

	if one.SetAt.IsZero() {
		return fmt.Errorf("it carries no set_at, and a pause says when it was set so that a screen can say how long is left")
	}

	if one.Chosen == durationForever {
		if one.Expires != nil {
			return fmt.Errorf(
				"it was set for %s and carries an expiry of %s: a pause of forever does not end, and one that carried both would be two answers to one question",
				durationForever, one.Expires.Format(time.RFC3339))
		}

		return nil
	}

	if one.Expires == nil {
		return fmt.Errorf(
			"it was set for %s and carries no expiry, and only a pause of %s has none: write the instant it ends",
			one.Chosen, durationForever)
	}

	if !one.Expires.After(one.SetAt) {
		return fmt.Errorf(
			"it was set at %s and ends at %s, which is not after it began: a pause ends after it starts",
			one.SetAt.Format(time.RFC3339), one.Expires.Format(time.RFC3339))
	}

	return nil
}

// checkPauseLength refuses a length that is not one of the six, naming the value and the six.
func checkPauseLength(value string) error {
	for _, allowed := range pauseChoices {
		if value == allowed {
			return nil
		}
	}

	return fmt.Errorf("%q is not how long a pause may last: write %s",
		value, strings.Join(quoted(pauseChoices), ", "))
}

// pauseUntil is the instant a pause of this length ends, from a clock, and nil for forever.
//
// One function for the route that writes a pause, so that "ten minutes from now" is computed in one
// place and the answer a caller reads is the answer that was stored.
func pauseUntil(now time.Time, chosen string) (*time.Time, error) {
	if chosen == durationForever {
		return nil, nil
	}

	length, known := pauseLengths[chosen]
	if !known {
		return nil, checkPauseLength(chosen)
	}

	until := now.Add(length).UTC()

	return &until, nil
}

// borrowedZone says whether a binding drives a zone that is not the whole light — which is the fact
// that makes a pause hand the zone back instead of switching the light off.
//
// It is the binding's own segment and nothing else, and that is not a shortcut: `writer.borrow` is
// called exactly when the writer was built with a segment, the writer's segment comes from the
// binding's, and a binding with a segment is a segment of a controller this module does not own
// outright. The writer's `borrowed` field is the same fact after the fact — the segment as the
// controller was holding it — and it lives on the writer's goroutine and is not readable from a
// route. So the durable fact is the one to judge a pause by, and it is the same fact the writer
// judges a frame by.
//
// A whole-light binding is segment -1 and is not borrowed: this module drives the whole controller,
// so switching it off takes nothing from anybody.
func borrowedZone(b binding) bool { return b.Segment >= 0 }

// pauseEffectOn is what a pause does to one binding, and it is the one place the exception lives.
//
// A borrowed zone is handed back. A whole-light binding is switched off. Nothing else is possible,
// and the wiring wave reads this answer rather than deciding for itself — which is the whole reason
// the decision is made here, in a wave that does not switch anything off at all.
func pauseEffectOn(b binding) string {
	if borrowedZone(b) {
		return effectHandsBack
	}

	return effectOff
}

// pauseRows is the document's pause rows as they stand, checked.
//
// Two rows of the same scope — two globals, or two pauses on one binding — are refused rather than
// resolved by taking one: which of two pauses a person meant is not something this module can guess,
// and the second would silently shadow the first.
func pauseRows(config storedConfig) ([]storedPause, error) {
	rows := make([]storedPause, 0, len(config.Pauses))
	seen := make(map[string]bool, len(config.Pauses))

	for _, one := range config.Pauses {
		if err := checkStoredPause(one); err != nil {
			return nil, fmt.Errorf("the pause: %w", err)
		}

		key := pauseKey(one.Scope, one.Printer)
		if seen[key] {
			return nil, fmt.Errorf("the document holds two pauses for %s: there is one pause per scope, and which of two a person meant is not something this module can guess", key)
		}

		seen[key] = true

		rows = append(rows, one)
	}

	return rows, nil
}

// pauseKey is how one pause is addressed: its scope, and the printer when the scope is a binding.
func pauseKey(scope, printer string) string {
	if scope == pauseGlobal {
		return pauseGlobal
	}

	return pauseOnBinding + " " + strings.TrimSpace(printer)
}

// pauseFor is the row one scope holds, and false when nothing is set for it.
func pauseFor(rows []storedPause, scope, printer string) (storedPause, bool) {
	wanted := pauseKey(scope, printer)

	for _, one := range rows {
		if pauseKey(one.Scope, one.Printer) == wanted {
			return one, true
		}
	}

	return storedPause{}, false
}

// globalPause is the document's global pause in force at an instant, and false when there is none or
// it has expired.
func globalPause(rows []storedPause, now time.Time) (storedPause, bool) {
	one, held := pauseFor(rows, pauseGlobal, "")
	if !held || !one.inForce(now) {
		return storedPause{}, false
	}

	return one, true
}

// bindingPause is one binding's own pause in force at an instant, and false when it has none or it
// has expired.
func bindingPause(rows []storedPause, printer string, now time.Time) (storedPause, bool) {
	one, held := pauseFor(rows, pauseOnBinding, printer)
	if !held || !one.inForce(now) {
		return storedPause{}, false
	}

	return one, true
}

// setPause puts one pause into the document, replacing whatever row the same scope held.
func setPause(config *storedConfig, one storedPause) {
	wanted := pauseKey(one.Scope, one.Printer)

	kept := make([]storedPause, 0, len(config.Pauses)+1)

	for _, held := range config.Pauses {
		if pauseKey(held.Scope, held.Printer) != wanted {
			kept = append(kept, held)
		}
	}

	config.Pauses = append(kept, one)
}

// withoutPause is the document with one scope's pause taken away, and false when it had none.
func withoutPause(config *storedConfig, scope, printer string) bool {
	wanted := pauseKey(scope, printer)

	kept := make([]storedPause, 0, len(config.Pauses))
	removed := false

	for _, held := range config.Pauses {
		if pauseKey(held.Scope, held.Printer) == wanted {
			removed = true

			continue
		}

		kept = append(kept, held)
	}

	config.Pauses = kept

	return removed
}

// pauseAnswer is one pause as a caller reads it, answering the three questions §3's read has to
// answer about every one: is it on, why, and when exactly it ends.
type pauseAnswer struct {
	Scope string `json:"scope"`

	// Printer, Light and Segment name the binding a `binding` pause is about, and are absent on a
	// global one.
	Printer string `json:"printer,omitempty"`
	Light   string `json:"light,omitempty"`
	Segment *int   `json:"segment,omitempty"`

	// On is the first question, computed from the clock at read time. Expired says the row is still
	// in the document with its instant behind it, which is a different answer from "nobody set one".
	On      bool `json:"on"`
	Expired bool `json:"expired"`

	// Why is the second: `global` for the header's pause-everything, `binding` for one row's.
	Why string `json:"why"`

	// Until is the third: the absolute instant this pause ends at, in UTC, and absent for forever.
	// Forever says in one word that it does not end, because an absent field is not a statement.
	Until            string `json:"until,omitempty"`
	Forever          bool   `json:"forever"`
	RemainingSeconds *int   `json:"remaining_seconds,omitempty"`

	// Chosen is the length somebody picked, and SetAt when they picked it.
	Chosen string `json:"chosen"`
	SetAt  string `json:"set_at"`

	// Effect is what this pause does to its binding: off, or hands-back for a borrowed zone.
	Effect string `json:"effect,omitempty"`
}

// pauseAnswerOf renders one stored pause, judged at an instant, with the binding it is about.
func pauseAnswerOf(one storedPause, now time.Time) pauseAnswer {
	said := pauseAnswer{
		Scope:   one.Scope,
		Printer: one.Printer,
		On:      one.inForce(now),
		Expired: !one.inForce(now),
		Why:     one.Scope,
		Forever: one.forever(),
		Chosen:  one.Chosen,
		SetAt:   one.SetAt.UTC().Format(time.RFC3339),
	}

	if one.Expires != nil {
		said.Until = one.Expires.UTC().Format(time.RFC3339)
	}

	if left := one.remaining(now); left != nil && said.On {
		seconds := int(left.Round(time.Second) / time.Second)
		said.RemainingSeconds = &seconds
	}

	return said
}

// bindingPauseAnswer is one binding's answer to "what is a pause doing to you", whether the pause that
// reaches it is its own or the global one.
type bindingPauseAnswer struct {
	Printer string `json:"printer"`
	Light   string `json:"light"`
	Segment int    `json:"segment"`
	Whole   bool   `json:"whole"`

	// Borrowed says this binding drives one zone of a light this module does not own outright, which
	// is the fact that decides the exception.
	Borrowed bool `json:"borrowed"`

	// Paused is whether a pause reaches this binding at all, and DueTo says which one: its own, or
	// the global pause, or nothing.
	Paused bool   `json:"paused"`
	DueTo  string `json:"due_to,omitempty"`

	// Effect is what the pause does to it: off, or hands-back. It is reported for every binding,
	// paused or not, because a screen showing the whole set is exactly where the exception has to be
	// visible before somebody presses the button.
	Effect string `json:"effect"`

	Until   string `json:"until,omitempty"`
	Forever bool   `json:"forever,omitempty"`
}

// bindingPauseAnswerOf is one binding's reading, from the two scopes' pauses.
func bindingPauseAnswerOf(b binding, light controllerAnswer, rows []storedPause, now time.Time) bindingPauseAnswer {
	said := bindingPauseAnswer{
		Printer: b.DeviceID,
		Light:   light.Name,
		Segment: b.Segment,
		Whole:   b.Segment < 0,

		Borrowed: borrowedZone(b),
		Effect:   pauseEffectOn(b),
	}

	reaching, held := bindingPause(rows, b.DeviceID, now)
	if held {
		said.Paused = true
		said.DueTo = pauseOnBinding
	} else if global, any := globalPause(rows, now); any {
		reaching, held = global, true
		said.Paused = true
		said.DueTo = pauseGlobal
	}

	if held {
		said.Forever = reaching.forever()

		if reaching.Expires != nil {
			said.Until = reaching.Expires.UTC().Format(time.RFC3339)
		}
	}

	return said
}

// Notes every pause write carries, because the two things a caller must not be wrong about are what
// a pause is worth and what it does.
const (
	// pauseStoredNote says a pause survives a restart and a settings save, and what it does to the light
	// while it lasts.
	pauseStoredNote = "This was written into this module's own configuration store with the absolute instant it ends " +
		"at, so it survives a restart and a settings save. The light is switched off now and stays off until that " +
		"instant passes or the pause is cleared: no frames, no brightness, no retries in between, and then it comes " +
		"back on by itself."

	// pauseHandBackNote is the exception, stated wherever a pause is written, because the one thing
	// that must not be got wrong is that a borrowed zone is given back rather than powered off.
	pauseHandBackNote = "A binding that drives one segment of a strip somebody else also drives is handed back rather " +
		"than switched off: the rest of that strip belongs to its owner, and powering the controller down would take " +
		"it with it."

	// pauseResumedNote says what DELETE did.
	pauseResumedNote = "The pause was taken out of this module's own configuration store, so nothing will pause this " +
		"on the next start either. The light is switched back on and its picture is painted again at once, rather than " +
		"waiting for the next tick of the printer."
)
