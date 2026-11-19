// The state table: what a light shows for each condition a printer can be in, as a person edits
// it — a colour, a word, and how long that word holds before the state's own picture comes back.
//
// # The states are not this file's invention
//
// internal/printer names the words the host reports, internal/render decides what each one is
// drawn in, and docs/product-requirements.md Appendix A is the same statement in prose — the
// document says so in as many words: "this table and the code are the same statement and a
// disagreement between them is a defect in one of the two". So the ten host states below are
// those ten, in Appendix A's order, and each one's default colour is the renderer's own value
// rather than a number written out again here. A test asks render for every state's colour and
// compares it with this table.
//
// Appendix A also carries two **conditions** — Disconnected and Stale — which are computed from
// the reading rather than reported by the host: the host has no word for a printer it holds no
// session for, or for one that has said nothing new for thirty seconds, and this module decides
// both for itself. They are rows here, marked as conditions, because the approved Colours screen
// draws them as rows with a colour and a word of their own (docs/design/v2/colours/), and because
// a person who wants a word on a lost printer needs somewhere to say so. They are not host states
// and nothing may treat them as one: `kind` says which each row is, and the answer counts the two
// kinds separately.
//
// # Absent is a default, not a zero
//
// The store holds one row per state somebody has *changed*, and nothing else. A state with no row
// keeps the colour Appendix A gives it, no word, and the word's default hold of forever. That is
// why the document carries no copy of the defaults: they are the code's, they change with the
// code, and a copy written into a file would be a second statement to keep in step by hand — and
// a hand is what this table exists to spare.
package main

import (
	"fmt"
	"strings"

	"github.com/nothingmn/wled-printer-status/internal/printer"
	"github.com/nothingmn/wled-printer-status/internal/render"
	"github.com/nothingmn/wled-printer-status/internal/wled"
)

// where a field of a row came from, as every answer on these routes reports it. It is the whole
// reason a read carries an origin beside each value: a screen that cannot tell an override from a
// default shows a person a number he did not set and cannot reset.
const (
	fromDefault = "default"
	fromStore   = "store"
)

const (
	// disconnectedState is the condition the host has no word for: it holds no session for the
	// printer, and the state tree still carries the last state the printer reported. Appendix A
	// draws it in the error red at a third of its brightness, and never with the last-known
	// progress bar.
	disconnectedState = "Disconnected"

	// staleState is the second condition: connected, and nothing has moved for
	// printer.StaleAfter. It is the same dim red on purpose — either way the reading is a memory
	// rather than an observation — unless the last word is FINISHED or STOPPED, which keeps its
	// own colour because a job that is over is a fact silence cannot retract.
	staleState = "Stale"
)

// The lengths a state's word may hold for, as the Colours screen offers them.
//
// durationForever is one of the four accepted values and not the absence of one: a word that
// holds forever is a decision a person made, the store carries it as `forever`, and the route
// reports it as coming from the store. A word with no hold written has no word at all, and that
// is a different thing.
const (
	durationForever = "forever"
	hold30s         = "30s"
	hold60s         = "60s"
	hold5m          = "5m"
)

// holdsChoices is what a state's holds may be, in the order the screen offers them. Anything else
// is refused by name — see checkHolds — because a hold silently rounded to the nearest of these
// would be a word that stayed up for a length nobody asked for.
//
//nolint:gochecknoglobals // one closed list, read and never written.
var holdsChoices = []string{durationForever, hold30s, hold60s, hold5m}

// stateKind says where one row of the table comes from: a word the host reports, or a condition
// this module computes from the reading.
type stateKind string

const (
	// stateFromHost is one of the ten states the host reports.
	stateFromHost stateKind = "state"

	// stateCondition is one of the two conditions the host has no word for.
	stateCondition stateKind = "condition"
)

// stateDefinition is one row of the table before anybody edits it: what the state is called, what
// kind of thing it is, the colour Appendix A gives it, and what it means in one line for a person
// reading the page.
type stateDefinition struct {
	name    string
	kind    stateKind
	colour  wled.RGB
	meaning string
}

// stateTable is every row, in docs/product-requirements.md Appendix A's order: the ten states the
// host reports, then the two conditions.
//
// The colours are internal/render's own package-level values, by name, so that reading this table
// beside render.go shows the pairing a person can check by eye.
//
//nolint:gochecknoglobals // one table, built once at start-up from the renderer's own values.
var stateTable = []stateDefinition{
	{name: printer.StateIdle, kind: stateFromHost, colour: render.ColourIdle,
		meaning: "nothing in progress; the machine is available"},
	{name: printer.StateBusy, kind: stateFromHost, colour: render.ColourBusy,
		meaning: "heating or calibrating for a print it has already taken"},
	{name: printer.StateReady, kind: stateFromHost, colour: render.ColourReady,
		meaning: "ready to print, with nothing running"},
	{name: printer.StatePrinting, kind: stateFromHost, colour: render.ColourPrinting,
		meaning: "a job is running"},
	{name: printer.StatePaused, kind: stateFromHost, colour: render.ColourPaused,
		meaning: "a job is on the bed and paused"},
	{name: printer.StateFinished, kind: stateFromHost, colour: render.ColourFinished,
		meaning: "a job completed, and holds until the next one"},
	{name: printer.StateStopped, kind: stateFromHost, colour: render.ColourStopped,
		meaning: "a job was stopped"},
	{name: printer.StateAttention, kind: stateFromHost, colour: render.ColourAttention,
		meaning: "waiting on a person; a filament change lives here"},
	{name: printer.StateError, kind: stateFromHost, colour: render.ColourError,
		meaning: "the printer is in error"},
	{name: printer.StateUnknown, kind: stateFromHost, colour: render.ColourUnknown,
		meaning: "the printer has reported no state"},
	{name: disconnectedState, kind: stateCondition, colour: render.ColourDisconnected,
		meaning: "the host holds no session, so the state tree is the last thing the printer said"},
	{name: staleState, kind: stateCondition, colour: render.ColourStale,
		meaning: "connected, but nothing has moved for thirty seconds"},
}

// storedState is one row of the state table as the document holds it: the state it is about, and
// only the fields somebody changed.
//
// Every field is a pointer for the reason a binding's segment is: absent is not zero. A colour of
// 0,0,0 is black, which is a colour a person may pick; a hold of "" is not a hold at all; and an
// absent word is the state carrying none, which is the default and therefore not worth writing.
// A row with nothing in it is never written — DELETE is how a state goes back to its default.
type storedState struct {
	State  string        `json:"state"`
	Colour *storedColour `json:"colour,omitempty"`
	Word   *string       `json:"word,omitempty"`
	Holds  *string       `json:"holds,omitempty"`
}

// storedColour is a colour in the document: one byte per channel, which is what every colour in
// this module travels as. The channels are integers here rather than bytes so that a value out of
// range is refused with the number in it instead of wrapping to a different colour.
type storedColour struct {
	R int `json:"r"`
	G int `json:"g"`
	B int `json:"b"`
}

// channels is a stored colour as the bytes a light takes, for a colour already checked in range.
func (c storedColour) channels() wled.RGB {
	return wled.RGB{R: uint8(c.R), G: uint8(c.G), B: uint8(c.B)}
}

// colourOf is a colour as the document carries it.
func colourOf(colour wled.RGB) storedColour {
	return storedColour{R: int(colour.R), G: int(colour.G), B: int(colour.B)}
}

// inRange says whether a stored colour is one a light can take: every channel 0-255.
func (c storedColour) inRange() bool {
	for _, channel := range []int{c.R, c.G, c.B} {
		if channel < 0 || channel > 255 {
			return false
		}
	}

	return true
}

// stateRow is one row of the table as it stands: the definition, and the override somebody wrote
// for it, if anybody did.
type stateRow struct {
	definition stateDefinition
	override   storedState
}

// overridden says whether this row carries anything a person wrote, which is what the answer
// reports: a row of only defaults is a state nobody has touched.
func (r stateRow) overridden() bool {
	return r.override.Colour != nil || r.override.Word != nil || r.override.Holds != nil
}

// colour is the colour this state is drawn in, and where it came from.
func (r stateRow) colour() (wled.RGB, string) {
	if r.override.Colour != nil {
		return r.override.Colour.channels(), fromStore
	}

	return r.definition.colour, fromDefault
}

// word is the state's word as it stands — empty for no word — and where it came from.
//
// A state's word is off by default: the colour is what a state shows unless a person says
// otherwise, which is what docs/design/v2/colours/ decides — "a word per state, off by default" —
// and what ships today, where the one word there is belongs to one event rather than to a state.
func (r stateRow) word() (string, string) {
	if r.override.Word != nil {
		return *r.override.Word, fromStore
	}

	return "", fromDefault
}

// holds is how long the state's word holds when it has one, and where that came from.
func (r stateRow) holds() (string, string) {
	if r.override.Holds != nil {
		return *r.override.Holds, fromStore
	}

	return durationForever, fromDefault
}

// findState is the row of the table one name asks for, by the table's own name in any case.
//
// The case is forgiven because a name travels in a URL on these routes and a caller that
// lowercased it means the same state; nothing else is. A name the table does not hold is a
// refusal by the route, listing the names it does hold.
func findState(name string) (stateDefinition, bool) {
	for _, one := range stateTable {
		if strings.EqualFold(one.name, strings.TrimSpace(name)) {
			return one, true
		}
	}

	return stateDefinition{}, false
}

// stateNames is every row's name, in the table's order, for a refusal that lists them.
func stateNames() []string {
	names := make([]string, 0, len(stateTable))

	for _, one := range stateTable {
		names = append(names, one.name)
	}

	return names
}

// stateNamesOf is every row's name of one kind, for the answer's count of states and conditions.
func stateNamesOf(kind stateKind) []string {
	names := make([]string, 0, len(stateTable))

	for _, one := range stateTable {
		if one.kind == kind {
			names = append(names, one.name)
		}
	}

	return names
}

// checkHolds refuses a hold that is not one of the four the screen offers, naming the value and
// the four. It is a refusal rather than a rounding because the length is the whole content of the
// field: a word held "about a minute" is not the thing anybody asked for.
func checkHolds(value string) error {
	for _, allowed := range holdsChoices {
		if value == allowed {
			return nil
		}
	}

	return fmt.Errorf("%q is not how long a word may hold: write %s; a word with no hold holds %s",
		value, strings.Join(quoted(holdsChoices), ", "), durationForever)
}

// quoted wraps each value in quotes for a refusal a person reads, so that 30s and 5m are visibly
// the values and not part of the sentence.
func quoted(values []string) []string {
	out := make([]string, 0, len(values))

	for _, one := range values {
		out = append(out, fmt.Sprintf("%q", one))
	}

	return out
}

// checkStoredState refuses an override this build cannot act on: a colour outside 0-255, or a hold
// that is not one of the four.
//
// It runs on the way in — a route will not write one — and again on the way out, because the
// document is a file a person can edit with a text editor. A row that would otherwise be obeyed as
// written is refused with the row named, which is the same answer this module gives a document it
// cannot read: nothing is half-applied and the file is the thing to open.
func checkStoredState(one storedState) error {
	if one.Colour != nil && !one.Colour.inRange() {
		return fmt.Errorf("the override for %s carries the colour %d,%d,%d, and a channel is 0-255",
			one.State, one.Colour.R, one.Colour.G, one.Colour.B)
	}

	if one.Holds != nil {
		if err := checkHolds(*one.Holds); err != nil {
			return fmt.Errorf("the override for %s: %w", one.State, err)
		}
	}

	return nil
}

// rowsOf is the table as it stands with one document's overrides applied: every row, in the
// table's order, whether or not anybody has edited it.
//
// A stored row naming a state this build no longer has is left out here and reported by
// unknownStates instead of silently applied: it belongs to a state this build cannot draw, and a
// table that quietly grew a row nobody can see would be the failure the version stamp exists to
// prevent.
func rowsOf(config storedConfig) ([]stateRow, error) {
	overrides := make(map[string]storedState, len(config.States))

	for _, one := range config.States {
		definition, known := findState(one.State)
		if !known {
			continue
		}

		if err := checkStoredState(one); err != nil {
			return nil, err
		}

		one.State = definition.name
		overrides[definition.name] = one
	}

	rows := make([]stateRow, 0, len(stateTable))

	for _, definition := range stateTable {
		rows = append(rows, stateRow{definition: definition, override: overrides[definition.name]})
	}

	return rows, nil
}

// rowNamed is one row of an already-checked set of rows, and false for a name the table does not
// hold.
func rowNamed(rows []stateRow, name string) (stateRow, bool) {
	for _, row := range rows {
		if strings.EqualFold(row.definition.name, strings.TrimSpace(name)) {
			return row, true
		}
	}

	return stateRow{}, false
}

// unknownStates is the states a document holds an override for that this build no longer has — a
// row written by a newer release, or one left behind after a state was renamed. They are reported
// beside the table rather than dropped in silence, because a row a person wrote is not this
// route's to delete and a table that quietly shrank would be the failure nobody could see.
func unknownStates(config storedConfig) []string {
	unknown := make([]string, 0)

	for _, one := range config.States {
		if _, known := findState(one.State); !known {
			unknown = append(unknown, one.State)
		}
	}

	return unknown
}

// setState puts one override into the document, replacing whatever row the same state had.
//
// The row is replaced whole rather than merged: a route that merged would have to decide, for
// every absent field, whether the caller meant "leave it" or "clear it", and the one answer that
// is always wrong is the guess. A field is cleared by not writing it in a route that carries all
// three, or by DELETE, which takes the whole row away.
func setState(config *storedConfig, one storedState) {
	definition, known := findState(one.State)
	if !known {
		return
	}

	one.State = definition.name

	kept := make([]storedState, 0, len(config.States)+1)

	for _, held := range config.States {
		if !strings.EqualFold(held.State, definition.name) {
			kept = append(kept, held)
		}
	}

	config.States = append(kept, one)
}

// withoutState is the document with one state's override taken away, and false when it had none.
func withoutState(config *storedConfig, name string) bool {
	kept := make([]storedState, 0, len(config.States))
	removed := false

	for _, held := range config.States {
		if strings.EqualFold(held.State, name) {
			removed = true

			continue
		}

		kept = append(kept, held)
	}

	config.States = kept

	return removed
}
