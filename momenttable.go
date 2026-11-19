// The moment table: what a light shows when something *happens*, as a person edits it — per
// (light, printer) pair, one row per moment.
//
// # What a moment is, and where the list comes from
//
// A state is a condition and lasts until something else happens; a moment is the transition —
// print finished, paused, error — and it is shown for a length and then given back. The list
// below is the approved Events screen's own list (docs/design/v2/events/) and
// docs/product-requirements.md §2.3: eight moments, in the order that page draws them. It is not
// this file's invention and nothing here adds a ninth.
//
// The moment list belongs to the *light* — every light has all eight, because the transition
// happens whether or not anybody configured it — while what a moment shows belongs to the
// (light, printer) pair. That pairing is also what multi-room means: the same printer on the
// office strip and on the workshop panel carries two independent sets, so the workshop can
// celebrate a finish and the office can stay quiet. The screen's indented per-printer sub-rows are
// these same rows and not a second table: one printer on a light is the flat table.
//
// # Absent is a default, not a zero
//
// The store holds a row only for a moment somebody has changed on a printer that is on a light.
// Every other moment is what momentDefault says: the finished moment on, showing the word this
// build already draws, and every other moment off and showing nothing. That default is not a
// guess — §2.1 states it for a one-click bind in as many words ("only the finished moment is on"),
// and the moment that *is* on is the behaviour this module already has and has proved on hardware:
// the word FINISHED, held for exactly sixty seconds, then handed back.
//
// # A preset name is accepted, not checked
//
// `which` for shows: Preset is stored verbatim, and this build does not ask the light whether it
// still holds a preset of that name. A write that failed because the light was unplugged would be
// a worse defect than a name the light no longer answers to: the operator edits the Events screen
// while standing at the light, and the light is exactly the thing that is sometimes off. Whether a
// named preset is still one of the light's belongs to a read of the light's presets
// (docs/product-requirements.md §2.6 lists them as read-only), and this build does not read them
// yet — so a read here says the name it holds and not whether the light still has it.
package main

import (
	"fmt"
	"strings"
	"time"
)

// What a moment can show — docs/product-requirements.md §2.3, in the screen's own words.
const (
	// showsPreset runs one of the light's own presets for the moment. A preset carries its own
	// segments, brightness and LED map, so it takes the whole light: capture, run, put back.
	showsPreset = "Preset"

	// showsWord draws a word. A word is panel only — a strip is one dimension and cannot carry
	// text — and the screen does not offer it on a strip.
	showsWord = "Word"

	// showsColour paints one of the state table's colours, named rather than numbered so that a
	// recoloured state is recoloured here too.
	showsColour = "Colour"

	// showsNothing does nothing at all, which is what a moment that is on but silent says.
	showsNothing = "Nothing"
)

// showsChoices is every value shows may take, in the order the screen offers them. Anything else
// is refused by name.
//
//nolint:gochecknoglobals // one closed list, read and never written.
var showsChoices = []string{showsPreset, showsWord, showsColour, showsNothing}

// The moments' names on the document and on these routes. They are the approved screen's rows,
// kebab-cased, so that a name is one path segment and cannot be mistaken for a sentence.
const (
	momentPrintFinished    = "print-finished"
	momentPrintStarted     = "print-started"
	momentFilamentChange   = "filament-change"
	momentPaused           = "paused"
	momentErrorAttention   = "error-or-attention"
	momentLightLost        = "light-lost"
	momentPrintCancelled   = "print-cancelled"
	momentPrinterWentQuiet = "printer-went-quiet"
)

// momentDefinition is one row of the moment list: its name on these routes, the words the screen
// prints for it, and what it means in one line.
type momentDefinition struct {
	id      string
	label   string
	meaning string
}

// momentTable is every moment a light has, in the order docs/design/v2/events/ draws them.
//
//nolint:gochecknoglobals // one closed list, read and never written.
var momentTable = []momentDefinition{
	{id: momentPrintFinished, label: "Print finished",
		meaning: "a job completed"},
	{id: momentPrintStarted, label: "Print started",
		meaning: "a job was taken and began"},
	{id: momentFilamentChange, label: "Filament change",
		meaning: "the printer is waiting on a person for filament"},
	{id: momentPaused, label: "Paused",
		meaning: "a running job was paused"},
	{id: momentErrorAttention, label: "Error or attention",
		meaning: "the printer went into error, or began asking for a person"},
	{id: momentLightLost, label: "Light lost",
		meaning: "this module can no longer reach the light it drives"},
	{id: momentPrintCancelled, label: "Print cancelled",
		meaning: "a job was stopped rather than completed"},
	{id: momentPrinterWentQuiet, label: "Printer went quiet",
		meaning: "the printer stopped reporting while a job was live"},
}

// momentEntry is the four things a person sets for one moment on one printer: whether it is on,
// what it shows, which of those it shows, and for how long.
//
// The same four fields are in the stored row and in the request body, and checkEntry judges both,
// so that a body the routes accept and a document the routes read cannot be two shapes of one
// thing.
type momentEntry struct {
	Enabled bool   `json:"enabled"`
	Shows   string `json:"shows"`
	Which   string `json:"which"`
	For     string `json:"for"`
}

// storedMoment is one row of the moment table as the document holds it: which light, which
// printer, which moment, and what that pairing shows.
//
// The light is its name on these routes — the controller's address without the scheme — because
// that is the noun the screen's selector carries and the name every route here addresses a light
// by. The printer is the host's device id, for the reason a binding stores an id: it survives a
// rename of the machine.
type storedMoment struct {
	Light   string `json:"light"`
	Printer string `json:"printer"`
	Moment  string `json:"moment"`
	momentEntry
}

// momentDefault is what one moment does on a light nobody has edited: the entry a person sees
// before he changes anything, and the one a delete puts back.
//
// Only the finished moment is on, which is §2.1's own sentence, and what it shows is what this
// build already does when a print completes: the word FINISHED, in the finished green, held for
// one minute and then handed back. Every other moment is off and shows nothing — the approved
// mockup's seed data names presets such as "Snow Fall" for a print start, and a default naming one
// light's preset would be a default no other light could honour.
func momentDefault(id string) momentEntry {
	if id == momentPrintFinished {
		return momentEntry{
			Enabled: true,
			Shows:   showsWord,
			Which:   finishedWord,
			For:     hold60s,
		}
	}

	return momentEntry{Enabled: false, Shows: showsNothing, Which: "", For: durationForever}
}

// findMoment is the moment one name asks for, by its own name in any case.
func findMoment(name string) (momentDefinition, bool) {
	for _, one := range momentTable {
		if strings.EqualFold(one.id, strings.TrimSpace(name)) {
			return one, true
		}
	}

	return momentDefinition{}, false
}

// momentNames is every moment's name, in the list's order, for a refusal that lists them.
func momentNames() []string {
	names := make([]string, 0, len(momentTable))

	for _, one := range momentTable {
		names = append(names, one.id)
	}

	return names
}

// momentsDescribed is every moment's name beside the words the screen prints for it, which is what
// a refusal about a moment needs to stay useful to a person reading it.
func momentsDescribed() string {
	said := make([]string, 0, len(momentTable))

	for _, one := range momentTable {
		said = append(said, fmt.Sprintf("%s (%s)", one.id, one.label))
	}

	return strings.Join(said, ", ")
}

// checkEntry refuses an entry this module cannot act on, and the two things it will not do are
// coerce and guess:
//
//   - shows must be one of the four. Anything else is refused with the four named.
//   - which must make sense for shows. A Colour names a state from the state table; a Preset names
//     one of the light's own presets, taken as written; a Word is free text; and Nothing names
//     nothing, so a which beside it is a mistake rather than something to drop in silence.
//
// Where the check cannot be made locally it is not pretended: whether a preset name is still one
// of the light's own is a fact only the light has, and a write must not ask it (see this file's
// comment on that). The case that *is* knowable without asking anything — a colour name that is
// not a colour — is refused here with the reason, which is the refusal the mockup's own page
// should have given instead of quietly substituting FINISHED.
func checkEntry(one momentEntry) error {
	if err := checkFor(one.For); err != nil {
		return err
	}

	switch one.Shows {
	case showsPreset:
		if strings.TrimSpace(one.Which) == "" {
			return fmt.Errorf(
				"shows is %s and which is empty: name one of the light's own presets, as WLED's own interface spells it",
				showsPreset)
		}

		return nil
	case showsWord:
		if strings.TrimSpace(one.Which) == "" {
			return fmt.Errorf("shows is %s and which is empty: write the word the panel should draw", showsWord)
		}

		return nil
	case showsColour:
		if _, known := findState(one.Which); !known {
			return fmt.Errorf(
				"shows is %s and which is %q, which is not one of the table's colours: name a state such as %s, or set shows to %s to name one of the light's presets",
				showsColour, one.Which, strings.Join(quoted(firstStateNames(3)), ", "), showsPreset)
		}

		return nil
	case showsNothing:
		if strings.TrimSpace(one.Which) != "" {
			return fmt.Errorf(
				"shows is %s and which is %q: nothing shows nothing, so drop which — or name what the moment should show",
				showsNothing, one.Which)
		}

		return nil
	default:
		return fmt.Errorf("%q is not something a moment can show: write %s",
			one.Shows, strings.Join(quoted(showsChoices), ", "))
	}
}

// firstStateNames is the first few rows of the state table, for a refusal that shows the shape of
// a value without listing twelve names in a sentence.
func firstStateNames(count int) []string {
	names := stateNames()
	if len(names) > count {
		names = names[:count]
	}

	return names
}

// checkFor refuses a length a moment will not be shown for.
//
// `forever` is one of the accepted values and means "until the light's own state changes", which
// is what the screen's own wording for three of these moments says — until it resumes, until it is
// cleared, until the light answers. Otherwise it is a duration such as 60s or 5m, and it has to be
// positive: a moment shown for no time is a moment that does nothing, which is what shows: Nothing
// says plainly.
func checkFor(value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("the moment's length is empty: write %s, or a length such as 60s or 5m", durationForever)
	}

	if value == durationForever {
		return nil
	}

	length, err := time.ParseDuration(value)
	if err != nil {
		return fmt.Errorf("%q is not a length: write %s, or a duration such as 60s or 5m", value, durationForever)
	}

	if length <= 0 {
		return fmt.Errorf("%q is not a length a moment can be shown for: it must be longer than nothing", value)
	}

	return nil
}

// checkStoredMoment refuses a stored row this build cannot act on — a row about a moment it no
// longer has is reported by unknownMoments instead — and names the row, so that a hand-edited
// document is a sentence a person can act on rather than a light that quietly does nothing.
func checkStoredMoment(one storedMoment) error {
	if err := checkEntry(one.momentEntry); err != nil {
		return fmt.Errorf("the entry for %s on %s for printer %s: %w",
			one.Moment, one.Light, one.Printer, err)
	}

	return nil
}

// momentRowOn is one moment as it stands for one (light, printer) pairing: what that moment does,
// and whether that answer came from the store or from the default.
type momentRowOn struct {
	definition momentDefinition
	entry      momentEntry
	stored     bool
}

// origin is where this row's entry came from, as the answer reports it.
func (r momentRowOn) origin() string {
	if r.stored {
		return fromStore
	}

	return fromDefault
}

// momentsOn is every moment a light has, in the list's order, each with the entry one printer's
// pairing carries — the stored one when there is one, and the default when there is not.
//
// A light returns every moment whether or not anybody has configured it: the list belongs to the
// light, and a screen that drew only the rows somebody had touched would be a screen with holes in
// it. Every stored row is judged on the way through, whichever light it is about, because a
// document this build cannot act on is a document a person has to open rather than one it reads
// the half of it understands.
func momentsOn(config storedConfig, light, printer string) ([]momentRowOn, error) {
	held := make(map[string]storedMoment, len(momentTable))

	for _, one := range config.Moments {
		definition, known := findMoment(one.Moment)
		if !known {
			continue
		}

		if err := checkStoredMoment(one); err != nil {
			return nil, err
		}

		if one.Light == light && one.Printer == printer {
			one.Moment = definition.id
			held[definition.id] = one
		}
	}

	rows := make([]momentRowOn, 0, len(momentTable))

	for _, definition := range momentTable {
		row := momentRowOn{definition: definition, entry: momentDefault(definition.id)}

		if stored, ok := held[definition.id]; ok {
			row.entry = stored.momentEntry
			row.stored = true
		}

		rows = append(rows, row)
	}

	return rows, nil
}

// momentsStored is every stored moment row, checked, for a reader that looks its own rows up by the
// (light, printer) key after one read of the document.
//
// It exists beside momentsOn for the reason rowsOf exists beside the state routes: a route answers
// about one (light, printer) pair and this answers about the document, and both judge every row so
// that a document with a bad line in it is refused rather than half-obeyed. A row about a moment this
// build no longer has is left out here and reported by unknownMoments, exactly as momentsOn does.
func momentsStored(config storedConfig) ([]storedMoment, error) {
	kept := make([]storedMoment, 0, len(config.Moments))

	for _, one := range config.Moments {
		definition, known := findMoment(one.Moment)
		if !known {
			continue
		}

		if err := checkStoredMoment(one); err != nil {
			return nil, err
		}

		one.Moment = definition.id

		kept = append(kept, one)
	}

	return kept, nil
}

// storedMomentFor is the row a document holds for one (light, printer) and one moment.
func storedMomentFor(config storedConfig, light, printer, moment string) (storedMoment, bool) {
	for _, one := range config.Moments {
		if sameMomentKey(one, storedMoment{Light: light, Printer: printer, Moment: moment}) {
			return one, true
		}
	}

	return storedMoment{}, false
}

// setMoment puts one entry into the document, replacing whatever row the same pairing had.
func setMoment(config *storedConfig, one storedMoment) {
	kept := make([]storedMoment, 0, len(config.Moments)+1)

	for _, held := range config.Moments {
		if !sameMomentKey(held, one) {
			kept = append(kept, held)
		}
	}

	config.Moments = append(kept, one)
}

// withoutMoment is the document with one pairing's entry for one moment taken away, and false when
// it had none.
func withoutMoment(config *storedConfig, light, printer, moment string) bool {
	kept := make([]storedMoment, 0, len(config.Moments))
	removed := false

	wanted := storedMoment{Light: light, Printer: printer, Moment: moment}

	for _, held := range config.Moments {
		if sameMomentKey(held, wanted) {
			removed = true

			continue
		}

		kept = append(kept, held)
	}

	config.Moments = kept

	return removed
}

// sameMomentKey says whether two rows are about the same (light, printer) moment: the pair and the
// moment, and nothing else. It is the key of this table, written once so that a write and a delete
// cannot disagree about it.
func sameMomentKey(one, other storedMoment) bool {
	return one.Light == other.Light &&
		one.Printer == other.Printer &&
		strings.EqualFold(one.Moment, other.Moment)
}

// unknownMoments is every stored row about a moment this build no longer has, named by its light,
// printer and moment, so that a document written by a newer release is reported rather than
// silently ignored.
func unknownMoments(config storedConfig) []string {
	unknown := make([]string, 0)

	for _, one := range config.Moments {
		if _, known := findMoment(one.Moment); !known {
			unknown = append(unknown, fmt.Sprintf("%s/%s/%s", one.Light, one.Printer, one.Moment))
		}
	}

	return unknown
}
