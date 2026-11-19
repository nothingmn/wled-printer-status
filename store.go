// The module's own configuration store: one JSON document in the writable directory the host
// grants a module that asks for one.
//
// # Why it exists, and what it is not
//
// The manifest's settings belong to the host: this process reads them on plugin.configure and has
// no call that writes them back. That is enough for a value a person types once, and it is not
// enough for configuration an editor changes, because a change made through a route would be lost
// at the next restart and nothing could say otherwise.
//
// So the module keeps its own configuration: one JSON document in the writable directory the
// operator approved when the module was installed (`needs.data` in module.yaml, `Grants.Data` on
// the configure call). It lives beside the installed module on Prusa Connect's writable config
// volume, which is what makes it survive a restart and an upgrade and lose it to a `module purge`
// or a fresh install — docs/product-requirements.md §4.
//
// The host's settings are read once, as a migration source: the first start with no document
// writes one from them, and every start after that reads the document instead. That is the
// requirement's own words and not a convenience of this file.
//
// # The version, and why it is stamped from the first commit
//
// The document carries a schema version from the first commit, because its shape will change — a
// binding gains a layout, a moment table and a state table appear beside it — and a version added
// later cannot be trusted: the documents written before it are indistinguishable from documents
// at whatever the first stamped version happened to be.
//
// A read therefore never guesses. A document with no version is version 0, which is the shape this
// store would have written before the field existed, and it is stepped up to storeVersion one
// explicit upgrade at a time and then written back stamped. A document at a version this build
// does not know is refused, naming both numbers, rather than half-read: a newer release wrote it
// and this one cannot know what its fields mean.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const (
	// storeFileName is the document's name inside the directory the host granted. It is one
	// file because this is one small document: the configuration a person edits, and not a
	// database, an index or a table of its own.
	storeFileName = "config.json"

	// storeVersion is the schema version this build writes. It goes up by one when the shape of
	// the document changes, and every version below it needs an entry in storeUpgrades so that a
	// document from an older release is stepped up rather than refused.
	//
	// Version 1 is the document with the schema version stamped and the bindings list, which is
	// what the release that introduced this store wrote. Version 2 adds the state table and the
	// moment table beside the bindings — the two tables the Colours and Events screens edit — and
	// a version-1 document is stepped up to it by an entry in storeUpgrades that adds both, empty.
	//
	// Version 3 adds the three things the Layout, Brightness and Overview screens edit: the layout
	// a binding composes with, the brightness windows, and the pause with its absolute expiry. All
	// three arrive empty from version 2 for the reason the two tables did — absent is a default and
	// not a zero — and the step is an entry in storeUpgrades like every other.
	//
	// Version 4 is what multi-room cost. A binding gained a name of its own — `id` on its line —
	// because "a printer may sit on several lights" (docs/product-requirements.md §2.2) means the
	// printer's device id stops naming one binding; and the layout table is keyed on that name
	// rather than on the printer, so a panel and a strip showing one machine keep two pictures.
	// Every version-3 binding was its printer's only one, so the step writes each line's `id` as
	// the printer it already carried and renames each layout row's `printer` key to `binding` with
	// the same value. Nothing is dropped: the rows are the rows.
	//
	// Version 5 is the moment a state's word was written: `set_at` on a state's row. A hold now
	// runs from the later of that moment and the moment the state began, so a word set on a state
	// that is already being shown is shown for its full length instead of being spent before
	// anyone read it. The step from 4 is deliberately a step that changes nothing in the rows:
	// version 4 wrote no such moment, and a row without one keeps version 4's rule — the hold
	// counts from the state's own entry.
	//
	// Version 6 is the panel's seven optional lines: `remaining`, `ends`, `temperatures`,
	// `elapsed`, `material`, `printer` and `layer` on a layout row. Each is absent until somebody
	// switches it on, and absent means off — so the step from 5 changes nothing in any row either.
	// A version-5 row could not have carried one: version 5's build had no such field and refused
	// `remaining: true` by name, so this is a shape change with no data in it, which is why the
	// step is the stamp alone.
	//
	// Version 7 is the list of lights this module knows, which is the first thing in this document
	// that is not a thing somebody edited: until it, a light existed only because a binding named
	// its address, so there was nothing to add a light *to* and no card for a light with no printer
	// on it — docs/product-requirements.md §2.6's "one card per light". Each row carries the
	// address and what the light said about itself when it was read (storedLight, lights.go). The
	// step from 6 is a step with data in it: every address a binding already names becomes a row,
	// so the lights an install is already driving appear on their own, with the address the
	// operator wrote and no facts invented (seededLights).
	//
	// Version 8 is this module's own decision about a light: `ignored` on a light row, written
	// false on every light an older document holds. A network can answer with nineteen WLED
	// instances where two matter, and the operator's words were "lights which dont make sense and
	// i want to push out" — so a light can be kept and left out of every list that offers one,
	// rather than removed and forgotten. The step from 7 writes the flag as false on every row and
	// invents nothing else: a version-7 document carries no such key, and the light it describes
	// was not ignored because there was nothing to ignore it with.
	//
	// Version 9 is the zone this module judges a clock and a brightness window in: `timezone` and
	// `timezone_origin` on the document. The zone used to be a manifest setting, and the manifest has
	// carried no `options` since the release that made this store the only editing surface — so no
	// install could name one, and both the ends line and every brightness window refused. The step
	// from 8 writes both keys empty, because version 8's build could store no zone and nothing it
	// wrote had one; empty is also what version 8's rule was, so a module that names no zone goes on
	// starting unchanged. The `timezone` setting is then read once as the migration source and written
	// here (zoneFromSettings, main.go), and every consumer reads this document from then on. See
	// timezone.go.
	storeVersion = 9

	// storeVersionUnstamped is version 0: a document written before the version was stamped,
	// which is the only older shape this store has ever had.
	storeVersionUnstamped = 0
)

// errNoStore reports a granted directory with no document in it. That is the first start of an
// install and not a fault: the settings seed the document, and the start that follows reads it.
var errNoStore = errors.New("this module has no configuration store yet")

// storedConfig is the document as this build writes it: the schema version, then the
// configuration it holds.
type storedConfig struct {
	// Version is the schema version of everything below it, and the first field of the document
	// on purpose: a reader that knows nothing else can still tell whether it may read the rest.
	Version int `json:"version"`

	// Bindings is one line per binding this module shows, in the same shape one line of the
	// `bindings` setting carries — printer, controller, and segment or brightness when they were
	// named — with the binding's own `id` beside them.
	//
	// It is raw JSON for the reason the setting is: a strict decode of a slice cannot say which
	// line a misspelt key was in, and parseBindingEntries decodes one line at a time so that a
	// refusal reads `bindings[1]: ...` rather than making a person count lines in a file.
	Bindings []json.RawMessage `json:"bindings"`

	// States is the state table the Colours screen edits: one row per state somebody has
	// changed, carrying only the fields that were changed. A state with no row here is drawn
	// exactly as internal/render draws it — docs/product-requirements.md Appendix A — and that
	// silence is the whole point: an override is written when a person changes something, and a
	// default is never copied into the document to be kept in step with the code by hand.
	States []storedState `json:"states"`

	// Moments is the moment table the Events screen edits, keyed on the (light, printer) pair:
	// one row per moment somebody has changed on one printer on one light. A moment with no row
	// here behaves as momentDefault says, for the reason the state table has none: absent is a
	// default and not a zero.
	//
	// The key is the pair because that is what multi-room means — the same printer on two lights
	// carries two independent sets — and because the moment list itself belongs to the light.
	Moments []storedMoment `json:"moments"`

	// Layouts is the layout table the Layout screen edits, one row per binding somebody has
	// changed. A binding with no row here composes with the built-in default and says so with an
	// origin of `default`, for the reason the state table has none: absent is a default, the
	// default belongs to the code, and a copy of it in this file would be a second statement to
	// keep in step by hand.
	//
	// The row is named by the binding's id — `binding` — and not by the printer, because one
	// printer may be shown by two bindings on two lights and a panel and a strip do not compose
	// the same way. That is schema version 4's change; see storeVersion.
	Layouts []storedLayout `json:"layouts"`

	// Windows is the brightness windows the Brightness screen edits, in the operator's order: the
	// first one that contains a moment decides, and the rest are not consulted. They are the same
	// shape the `brightness_schedule` setting carries, deliberately — one concept, read by one
	// parser, so that a window written through a route and a window written in the settings file
	// cannot mean two different things.
	Windows []brightnessWindow `json:"windows"`

	// Lights is every light this module knows, whether or not a binding sits on it: the address,
	// and what the light said about itself the last time it was read. It is the list the Lights
	// screen draws a card per entry from, and the list a person adds to by typing an address.
	//
	// It is not derived from the bindings on every read, and that is the point: a light somebody
	// owns is a fact whether or not a printer is shown on it, and a light keeps its address while
	// the binding that introduced it is edited or removed. See lights.go.
	Lights []storedLight `json:"lights"`

	// Pauses is the pause state: at most one row of `global` scope, and at most one per binding.
	// Each row carries the absolute instant it ends at, never a duration and never a timer, because
	// this process is reconstructed on every settings save and on every restart — a pause held in
	// a timer is a pause that ends early the first time somebody saves the settings.
	Pauses []storedPause `json:"pauses"`

	// Timezone is the IANA zone this module judges a brightness window in and writes a clock in,
	// such as America/Denver, and the empty string when it names none.
	//
	// It is the authority and not a copy of anything: the `timezone` setting was the migration
	// source once (zoneFromSettings, main.go) and is not read again after that. It is never
	// defaulted to UTC or to this process's own zone — inside the Prusa Connect container that is
	// almost always UTC, and a schedule or a clock read there is hours away from the operator's
	// evening while looking perfectly healthy (AGENTS.md §5.14). So an absent zone is absent, and
	// the two things that need one refuse until somebody names one.
	Timezone string `json:"timezone"`

	// TimezoneOrigin says how the zone arrived in this document: `browser` for the first page load
	// that found none and captured the browser's own, `operator` for one typed on the Overview
	// screen, and `settings` for one the migration carried over. It is empty exactly when Timezone
	// is, and it is here because the screen has to be able to say where the value came from — a
	// zone a browser guessed and a zone a person chose are different claims about the same wall.
	TimezoneOrigin string `json:"timezone_origin"`
}

// storedConfigV3 is the document at schema version 3: the version, the bindings list, the two
// tables Colours and Events edit, and the three the Layout, Brightness and Overview screens edit.
// Only the upgrade from it reads it; nothing writes it.
//
// The layouts in it are storedLayoutV3, keyed on the printer, which is the shape version 4 replaced.
// The bindings are raw JSON for the reason they are everywhere: the step has to rewrite each line,
// and a typed list here would refuse a line for a reason that is the next version's business.
type storedConfigV3 struct {
	Version  int                `json:"version"`
	Bindings []json.RawMessage  `json:"bindings"`
	States   []storedState      `json:"states"`
	Moments  []storedMoment     `json:"moments"`
	Layouts  []storedLayoutV3   `json:"layouts"`
	Windows  []brightnessWindow `json:"windows"`
	Pauses   []storedPause      `json:"pauses"`
}

// storedLayoutV3 is one layout row as version 3 wrote it: keyed on the printer, because version 3
// could not have two bindings of one printer and the printer was therefore the binding's name.
//
// A type of its own rather than storedLayout with a second key, because the step decodes strictly: a
// version-3 row read into the current shape would put its `printer` key through DisallowUnknownFields
// and refuse a document this build is supposed to read.
type storedLayoutV3 struct {
	Printer string `json:"printer"`

	Bar           *bool   `json:"bar,omitempty"`
	BarBand       *string `json:"bar_band,omitempty"`
	BarRows       *int    `json:"bar_rows,omitempty"`
	Percent       *bool   `json:"percent,omitempty"`
	PercentScale  *int    `json:"percent_scale,omitempty"`
	Name          *bool   `json:"name,omitempty"`
	Rotation      *string `json:"rotation,omitempty"`
	RotateSeconds *int    `json:"rotate_seconds,omitempty"`
}

// toStored is this row at version 4: the same eight fields, keyed on the binding its printer named.
//
// The value is the printer's device id unchanged, and that is not a guess: version 3 refused a second
// binding of one printer, so on every document version 3 wrote, the printer *was* the binding's name
// — which is why the migration renames the key and copies the value rather than inventing one.
func (l storedLayoutV3) toStored() storedLayout {
	return storedLayout{
		Binding:       l.Printer,
		Bar:           l.Bar,
		BarBand:       l.BarBand,
		BarRows:       l.BarRows,
		Percent:       l.Percent,
		PercentScale:  l.PercentScale,
		Name:          l.Name,
		Rotation:      l.Rotation,
		RotateSeconds: l.RotateSeconds,
	}
}

// storedConfigV6 is the document at schema version 6: everything version 5 held, and the panel's
// seven optional lines on a layout row. Only the upgrades from it and the one above it read it;
// nothing writes it.
//
// It is a type of its own rather than storedConfig minus the lights list, because the steps below
// decode strictly: a version-6 document read into the current shape would accept a `lights` key
// that version 6 never wrote, and a document half one shape and half another is the one thing the
// version stamp exists to catch.
type storedConfigV6 struct {
	Version  int                `json:"version"`
	Bindings []json.RawMessage  `json:"bindings"`
	States   []storedState      `json:"states"`
	Moments  []storedMoment     `json:"moments"`
	Layouts  []storedLayout     `json:"layouts"`
	Windows  []brightnessWindow `json:"windows"`
	Pauses   []storedPause      `json:"pauses"`
}

// storedConfigV7 is the document at schema version 7: everything version 8 holds, with the lights
// list and no `ignored` flag on a light. Both the step that writes it and the step that reads it
// use it; nothing else does.
//
// It is a type of its own for the reason storedConfigV6 is. The step from 6 writes version 7, and
// the step from 7 reads it back; if the first wrote the current shape, the second would refuse the
// document it had just produced — a light row carrying `"ignored": false` is not a version-7 row,
// and the strict decode is what says so.
type storedConfigV7 struct {
	Version  int                `json:"version"`
	Bindings []json.RawMessage  `json:"bindings"`
	States   []storedState      `json:"states"`
	Moments  []storedMoment     `json:"moments"`
	Layouts  []storedLayout     `json:"layouts"`
	Windows  []brightnessWindow `json:"windows"`
	Lights   []storedLightV7    `json:"lights"`
	Pauses   []storedPause      `json:"pauses"`
}

// storedConfigV8 is the document at schema version 8: everything version 9 holds, and no zone on
// it. Both the step that writes it and the step that reads it use it; nothing else does.
//
// It is a type of its own for the reason storedConfigV7 is. The step from 7 writes version 8, and
// the step from 8 reads it back; if the first wrote the current shape, the second would refuse the
// document it had just produced — a version-8 document carrying `"timezone": ""` is not a
// version-8 document, and the strict decode is what says so.
type storedConfigV8 struct {
	Version  int                `json:"version"`
	Bindings []json.RawMessage  `json:"bindings"`
	States   []storedState      `json:"states"`
	Moments  []storedMoment     `json:"moments"`
	Layouts  []storedLayout     `json:"layouts"`
	Windows  []brightnessWindow `json:"windows"`
	Lights   []storedLight      `json:"lights"`
	Pauses   []storedPause      `json:"pauses"`
}

// storedConfigV2 is the document at schema version 2: the version, the bindings list, and the two
// tables the Colours and Events screens edit. Only the upgrade from it reads it; nothing writes it.
//
// It is a type of its own rather than storedConfig minus three fields, because the step below
// decodes it strictly: a version-2 document read into the current shape would accept a `layouts`
// key that version 2 never wrote, and a document that is half one shape and half another is the
// one thing the version stamp exists to catch.
type storedConfigV2 struct {
	Version  int               `json:"version"`
	Bindings []json.RawMessage `json:"bindings"`
	States   []storedState     `json:"states"`
	Moments  []storedMoment    `json:"moments"`
}

// storedConfigV1 is the document at schema version 1: the version and the bindings list, and
// nothing else. Only the upgrade from it reads it; nothing writes it.
type storedConfigV1 struct {
	Version  int               `json:"version"`
	Bindings []json.RawMessage `json:"bindings"`
}

// storedConfigV0 is the document before the version was stamped: the bindings list and no
// version key. Only an upgrade reads it; nothing writes it.
type storedConfigV0 struct {
	Bindings []json.RawMessage `json:"bindings"`
}

// storedEntry is one line of the store's bindings list, and it is the same keys one line of the
// `bindings` setting is written with, beside the binding's own name.
//
// The pointers are the setting's own distinction and are kept for the same reason: absent is not
// zero. Segment 0 is a segment a controller holds, a brightness of 0 is a strip switched off, and
// a line that leaves one out must not be read as either. A whole-controller binding writes its
// brightness, because that is the number the next start has to land on; a borrowed one writes
// none, because a brightness on it is refused.
type storedEntry struct {
	// ID is the binding's name, and it is written here rather than derived on every start because
	// a name that is derived is a name that can change: a printer's second binding would take the
	// bare device id from the first if the first were ever removed, and the path a screen holds
	// would move under it. Schema version 4 introduced it; the upgrade from 3 fills every existing
	// line with the printer it already names, which was that binding's name.
	ID string `json:"id,omitempty"`

	Printer    string `json:"printer"`
	Controller string `json:"controller"`
	Segment    *int   `json:"segment,omitempty"`
	Brightness *int   `json:"brightness,omitempty"`
}

// storeUpgrade is one step of an upgrade: one document at From, read into the shape at From+1.
//
// It exists as data rather than as a chain of ifs so that the next shape change is an entry in a
// table and not a rewrite of the read path — which is the whole reason the version is stamped.
type storeUpgrade struct {
	// from is the version the step reads, and it writes from+1.
	from int

	// why says what changed, for the line the operator reads when his store is stepped up.
	why string

	// step reads one document and answers the document one version later, which the caller
	// decodes strictly at the new version: a step that forgot a field fails there rather than
	// producing a document that reads as a zero.
	step func(raw []byte) ([]byte, error)
}

// storeUpgrades is every step this build can take, lowest first. A version with no step here is a
// version this build cannot read, and the read says so by number.
//
//nolint:gochecknoglobals // a closed table read at start-up, never written.
var storeUpgrades = []storeUpgrade{
	{
		from: storeVersionUnstamped,
		why:  "the schema version is stamped on a document that did not carry one",
		step: func(raw []byte) ([]byte, error) {
			var unstamped storedConfigV0
			if err := decodeStrictly(raw, &unstamped); err != nil {
				return nil, err
			}

			// Version 1 and not the current version: a step answers the document exactly one
			// version later, and the shape at 1 is the one above. Stepping straight to 2 here
			// would leave the upgrade from 1 untrodden and untested, which is the one thing a
			// versioned document exists to prevent.
			return json.Marshal(storedConfigV1{
				Version:  storeVersionUnstamped + 1,
				Bindings: unstamped.Bindings,
			})
		},
	},
	{
		from: 1,
		why:  "the document gained the state table and the moment table, both empty",
		step: func(raw []byte) ([]byte, error) {
			var first storedConfigV1
			if err := decodeStrictly(raw, &first); err != nil {
				return nil, err
			}

			// Both tables arrive empty, and empty is the whole document that version 1 already
			// described: every state keeps the colour internal/render holds for it and every
			// moment behaves as its default says, so the light this document was written for
			// goes on showing exactly what it showed before the upgrade. Nothing is invented
			// for a table nobody has edited yet — a default copied in here would be a second
			// copy of the code's own table, drifting from it one release later.
			//
			// The shape at 2 and not storedConfig, which is the shape at 3: a step answers the
			// document exactly one version later, and a step that stamped this one with the
			// three version-3 keys would be writing a document that version 3's own upgrade
			// would then decode and refuse for carrying keys version 2 never had.
			return json.Marshal(storedConfigV2{
				Version:  2,
				Bindings: first.Bindings,
				States:   []storedState{},
				Moments:  []storedMoment{},
			})
		},
	},
	{
		from: 2,
		why:  "the document gained the per-binding layout, the brightness windows and the pause, all empty",
		step: func(raw []byte) ([]byte, error) {
			var second storedConfigV2
			if err := decodeStrictly(raw, &second); err != nil {
				return nil, err
			}

			// Three empty lists, for the reason version 2's two arrived empty. An install with no
			// pause set is an install that is not paused; a binding nobody has laid out composes
			// with the default the painter holds; and a window list with nothing in it is the
			// fixed brightness, which is what every install before windows ran. Copying any of
			// those into the file would be a second copy of a decision the code already makes.
			//
			// The shape at 3 and not storedConfig, which is the shape at 4: a step answers the
			// document exactly one version later, and the layout rows here are still keyed on the
			// printer, which is what version 3 wrote and what the next step renames.
			return json.Marshal(storedConfigV3{
				Version:  3,
				Bindings: second.Bindings,
				States:   second.States,
				Moments:  second.Moments,
				Layouts:  []storedLayoutV3{},
				Windows:  []brightnessWindow{},
				Pauses:   []storedPause{},
			})
		},
	},
	{
		from: 3,
		why:  "every binding was named, and the layout table was re-keyed from the printer to the binding",
		step: func(raw []byte) ([]byte, error) {
			var third storedConfigV3
			if err := decodeStrictly(raw, &third); err != nil {
				return nil, err
			}

			// Every binding line gains `id`, and the value is the printer the line already names.
			// That is the whole migration and it is not an invention: version 3 refused a second
			// binding of one printer (bindings.check's rule then was "one printer is shown by one
			// binding"), so on every document version 3 wrote, the printer's device id was that
			// binding's only name — and it is the name the routes answered at, which is why the
			// panel keeps its path across this upgrade.
			lines := make([]json.RawMessage, 0, len(third.Bindings))

			for i, raw := range third.Bindings {
				var line storedEntry
				if err := decodeStrictly(raw, &line); err != nil {
					return nil, fmt.Errorf("read binding %d: %w", i+1, err)
				}

				if strings.TrimSpace(line.ID) == "" {
					line.ID = line.Printer
				}

				stamped, err := json.Marshal(line)
				if err != nil {
					return nil, err
				}

				lines = append(lines, stamped)
			}

			// And every layout row is renamed, not rewritten: `printer` becomes `binding` and the
			// eight fields travel exactly as they were, so a panel somebody had laid out composes
			// with the same picture after the upgrade as before it.
			rows := make([]storedLayout, 0, len(third.Layouts))

			for _, one := range third.Layouts {
				rows = append(rows, one.toStored())
			}

			return json.Marshal(storedConfigV6{
				Version:  4,
				Bindings: lines,
				States:   third.States,
				Moments:  third.Moments,
				Layouts:  rows,
				Windows:  third.Windows,
				Pauses:   third.Pauses,
			})
		},
	},
	{
		from: 4,
		why:  "a state's word carries the moment it was written, so a hold begins at the write",
		step: func(raw []byte) ([]byte, error) {
			// Version 4 and version 5 are the same document and not the same rows: version 5's
			// rows may carry `set_at` and version 4's never did, so a version-4 document decodes
			// into this shape with that field absent on every row. Absent is the rule version 4
			// had — a hold counts from the moment the state began — and inventing a moment here
			// would be inventing a decision nobody made, on every row of every existing install.
			//
			// What the step is for is the discipline the table exists for: a version 5 document
			// has to have been *written* by this build, one step at a time, so that a version 4
			// document is stepped rather than read as though it were version 5 with a field
			// missing. The read after it decodes strictly at version 5.
			var fourth storedConfigV6
			if err := decodeStrictly(raw, &fourth); err != nil {
				return nil, err
			}

			fourth.Version = 5

			return json.Marshal(fourth)
		},
	},
	{
		from: 5,
		why:  "a layout row may name the panel's seven optional lines, all absent until somebody switches one on",
		step: func(raw []byte) ([]byte, error) {
			// Version 5 and version 6 are the same document and not the same rows: version 6's
			// rows may carry the seven line keys and version 5's never did, so a version-5
			// document decodes into this shape with every one of them absent — which is the rule
			// version 6 has, because absent means the line is not switched on.
			//
			// A row that a person has edited by hand to carry `"material": true` on a version-5
			// document is read as it stands and becomes a version-6 row that draws the line. That
			// is deliberate: the key means what it says, and refusing it would be refusing a
			// layout for the version it arrived in.
			var fifth storedConfigV6
			if err := decodeStrictly(raw, &fifth); err != nil {
				return nil, err
			}

			fifth.Version = 6

			return json.Marshal(fifth)
		},
	},
	{
		from: 6,
		why:  "every address a binding already named became a light of its own, with no facts invented",
		step: func(raw []byte) ([]byte, error) {
			// The rows are the addresses the bindings already name, and the facts on each are
			// empty on purpose: this is a migration, it runs before the watch starts, and a
			// migration that dialled a controller would make a start depend on the network —
			// a light that is unplugged would either hold the upgrade up or have a name and a
			// firmware written into the document that nobody observed. The address is the fact
			// this step does have, so it is the fact this step writes; the start that follows
			// this one reads what each light says about itself and fills the rest in
			// (fillLightFacts, lights.go).
			var sixth storedConfigV6
			if err := decodeStrictly(raw, &sixth); err != nil {
				return nil, err
			}

			lights, err := seededLights(sixth.Bindings)
			if err != nil {
				return nil, err
			}

			// At version 7 and not at the shape this build writes: the step above this one reads a
			// version-7 document strictly, and a light row carrying an `ignored` flag is not one.
			// The rows are converted, not invented — the same addresses, the same facts, the same
			// order.
			rows := make([]storedLightV7, 0, len(lights))

			for _, one := range lights {
				rows = append(rows, one.atVersion7())
			}

			return json.Marshal(storedConfigV7{
				Version:  7,
				Bindings: sixth.Bindings,
				States:   sixth.States,
				Moments:  sixth.Moments,
				Layouts:  sixth.Layouts,
				Windows:  sixth.Windows,
				Pauses:   sixth.Pauses,
				Lights:   rows,
			})
		},
	},
	{
		from: 7,
		why:  "every light gains the ignored flag, false, so this module can put one out of the way without forgetting it",
		step: func(raw []byte) ([]byte, error) {
			// The one thing this step writes is the flag, and it writes it false on every row: a
			// version-7 document has no such key because version 7's build had no way to ignore a
			// light, so no light it describes was ignored. Nothing else is touched — the address,
			// the name the light calls itself, its firmware, its pixels, its shape and the moment it
			// was read all travel exactly as they were.
			//
			// It is written on the row rather than left out to mean false. Absent-means-default is
			// this store's rule for the tables a person edits, where a default belongs to the code;
			// this flag belongs to the document, and a person who opens config.json to see why a
			// light is not on a screen should find the word there.
			var seventh storedConfigV7
			if err := decodeStrictly(raw, &seventh); err != nil {
				return nil, err
			}

			lights := make([]storedLight, 0, len(seventh.Lights))

			for _, one := range seventh.Lights {
				lights = append(lights, one.toStored())
			}

			// At version 8 and not at the shape this build writes: the step above this one reads a
			// version-8 document strictly, and a document carrying a zone is not one. The rows are the
			// rows, and the zone this step cannot know is the step above's business — version 8's
			// build had no zone to write, so no key is invented for it here.
			return json.Marshal(storedConfigV8{
				Version:  8,
				Bindings: seventh.Bindings,
				States:   seventh.States,
				Moments:  seventh.Moments,
				Layouts:  seventh.Layouts,
				Windows:  seventh.Windows,
				Pauses:   seventh.Pauses,
				Lights:   lights,
			})
		},
	},
	{
		from: 8,
		why:  "the document gained the zone this module judges a clock and a brightness window in, empty until somebody names one",
		step: func(raw []byte) ([]byte, error) {
			// Both keys are written empty, and empty is the truth: version 8's build had no field to
			// put a zone in, so no document it wrote carries one, and inventing America/Denver (or UTC,
			// which is worse — it is the container's own clock and looks healthy while being hours out)
			// would be inventing a decision nobody made on every install.
			//
			// What fills it is the start that follows: the `timezone` setting is the migration source,
			// read once, and written here by zoneFromSettings (main.go). A module with no setting keeps
			// the empty string, which is the state that refuses a window and draws no clock — the same
			// state version 8 was in, and the reason this upgrade changes no light.
			var eighth storedConfigV8
			if err := decodeStrictly(raw, &eighth); err != nil {
				return nil, err
			}

			return json.Marshal(storedConfig{
				Version:        9,
				Bindings:       eighth.Bindings,
				States:         eighth.States,
				Moments:        eighth.Moments,
				Layouts:        eighth.Layouts,
				Windows:        eighth.Windows,
				Lights:         eighth.Lights,
				Pauses:         eighth.Pauses,
				Timezone:       "",
				TimezoneOrigin: "",
			})
		},
	},
}

// storeRead is what one read of the store found.
type storeRead struct {
	// Config is the document at storeVersion, stepped up when an older build wrote it.
	Config storedConfig

	// Version is the version the document carried before any upgrade, which is what the log line
	// names.
	Version int

	// Upgraded says the document must be written back: it was one or more versions behind this
	// build.
	Upgraded bool
}

// storePath is where the document lives inside a granted directory.
func storePath(dir string) string {
	return filepath.Join(dir, storeFileName)
}

// loadStore reads this module's configuration document out of dir.
//
// A directory with no document in it answers errNoStore, which is the first start of an install
// and not a fault. Every other failure is refused with the path in it: a document this build
// cannot read is a document a person has to see, and running from the settings instead would
// quietly discard whatever is in it.
func loadStore(dir string) (storeRead, error) {
	path := storePath(dir)

	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return storeRead{}, errNoStore
		}

		return storeRead{}, fmt.Errorf("read the configuration store %s: %w", path, err)
	}

	found, err := upgradeStore(raw)
	if err != nil {
		return storeRead{}, fmt.Errorf("the configuration store %s: %w", path, err)
	}

	return found, nil
}

// upgradeStore reads one document and brings it to storeVersion, one explicit step at a time.
//
// The version is read first and alone, because the shape of everything else depends on it: a
// document from a newer release has to be refused before any of its fields are read, or the
// refusal would be about a key this build does not know rather than about the version, and that
// sentence sends a person looking in the wrong place.
func upgradeStore(raw []byte) (storeRead, error) {
	version, err := versionOf(raw)
	if err != nil {
		return storeRead{}, err
	}

	if version > storeVersion {
		return storeRead{}, fmt.Errorf(
			"it was written by a newer release, at schema version %d, and this build reads version %d: install the release that wrote it, or move this file aside to start from the settings",
			version, storeVersion)
	}

	found := storeRead{Version: version}
	stepped := raw

	for version < storeVersion {
		upgrade, known := upgradeFrom(version)
		if !known {
			return storeRead{}, fmt.Errorf(
				"it carries schema version %d, and this build has no upgrade from that version to %d",
				version, storeVersion)
		}

		stepped, err = upgrade.step(stepped)
		if err != nil {
			return storeRead{}, fmt.Errorf("upgrade it from schema version %d, where %s: %w",
				version, upgrade.why, err)
		}

		version++
		found.Upgraded = true
	}

	if err := decodeStrictly(stepped, &found.Config); err != nil {
		return storeRead{}, fmt.Errorf("read the document at schema version %d: %w", storeVersion, err)
	}

	// A step that forgot to stamp, or a document whose version key disagrees with the shape it
	// carries, would otherwise be read as a document at this version with a version of its own.
	if found.Config.Version != storeVersion {
		return storeRead{}, fmt.Errorf(
			"it reads at schema version %d after upgrading, and this build writes version %d",
			found.Config.Version, storeVersion)
	}

	return found, nil
}

// upgradeFrom is the step that reads version, and false for a version this build cannot step.
func upgradeFrom(version int) (storeUpgrade, bool) {
	for _, upgrade := range storeUpgrades {
		if upgrade.from == version {
			return upgrade, true
		}
	}

	return storeUpgrade{}, false
}

// versionOf is the schema version one document carries, and storeVersionUnstamped for a document
// that carries none.
//
// The probe is lenient and reads one key: which keys are allowed in the rest of the document
// depends on the version, so a strict read here would refuse a newer document for the wrong
// reason and refuse an older one for a key the current shape simply does not have.
func versionOf(raw []byte) (int, error) {
	var probe struct {
		Version *int `json:"version"`
	}

	if err := json.Unmarshal(raw, &probe); err != nil {
		return 0, fmt.Errorf("it is not a JSON document: %w", err)
	}

	if probe.Version == nil {
		return storeVersionUnstamped, nil
	}

	if *probe.Version < 0 {
		return 0, fmt.Errorf(
			"it carries schema version %d, and a version counts from zero", *probe.Version)
	}

	return *probe.Version, nil
}

// decodeStrictly reads one document and refuses a key this build does not know, which is the rule
// every other document in this module is read by: a misspelt key that is ignored is a field left
// at its zero, and a field left at its zero here is a binding pointing at nothing.
func decodeStrictly(raw []byte, into any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(into); err != nil {
		return err
	}

	if decoder.More() {
		return errors.New("it carries more than one JSON document")
	}

	return nil
}

// saveStore writes this module's configuration document into dir, at storeVersion.
//
// It writes a temporary file beside the document and renames it over the old one, so that a
// process killed mid-write leaves the previous document rather than half of a new one: this file
// is the only copy of what a person configured, and a truncated one is a store somebody has to
// delete by hand.
//
// 0600, because it is this module's own record on a volume other processes can read, and nothing
// in it is worth handing to them.
func saveStore(dir string, config storedConfig) (string, error) {
	path := storePath(dir)

	// Both tables are written as empty arrays when they hold nothing, never as null: a null
	// reads as "this build does not know the field", and a person opening config.json to see
	// what the state table holds deserves a list rather than an absence. It is also what makes
	// an upgraded document and a fresh one the same document on disk.
	if config.States == nil {
		config.States = []storedState{}
	}

	if config.Moments == nil {
		config.Moments = []storedMoment{}
	}

	// The three lists version 3 added, for the same reason and one more: the first start of an
	// install writes this document from the settings without naming any of them, and a null there
	// would read as a field this build does not know.
	if config.Layouts == nil {
		config.Layouts = []storedLayout{}
	}

	if config.Windows == nil {
		config.Windows = []brightnessWindow{}
	}

	if config.Pauses == nil {
		config.Pauses = []storedPause{}
	}

	// And the lights list, for the reason above and one of its own: the first start of an install
	// that names no light writes this document with an empty list rather than a null, so that a
	// person opening it sees "no lights yet" and not a key this build does not know.
	if config.Lights == nil {
		config.Lights = []storedLight{}
	}

	document, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return path, fmt.Errorf("render the configuration store: %w", err)
	}

	document = append(document, '\n')

	temp, err := os.CreateTemp(dir, "."+storeFileName+".*")
	if err != nil {
		return path, fmt.Errorf("write the configuration store %s: %w", path, err)
	}

	name := temp.Name()

	// A no-op once the rename has taken the name, and the cleanup for every refusal below.
	defer func() { _ = os.Remove(name) }()

	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()

		return path, fmt.Errorf("write the configuration store %s: %w", path, err)
	}

	if _, err := temp.Write(document); err != nil {
		_ = temp.Close()

		return path, fmt.Errorf("write the configuration store %s: %w", path, err)
	}

	if err := temp.Close(); err != nil {
		return path, fmt.Errorf("write the configuration store %s: %w", path, err)
	}

	if err := os.Rename(name, path); err != nil {
		return path, fmt.Errorf("write the configuration store %s: %w", path, err)
	}

	return path, nil
}

// storeEntries renders a checked set as the lines the document holds.
//
// The printer is written by its device id rather than by the name the operator typed, because the
// store is this module's record and not a copy of his settings: an id survives a rename, and a
// document that named a printer by a name that has since changed would refuse the next start for
// a reason nobody caused.
//
// The binding's name is written beside it, because that is what the next start has to hand back to
// the routes and to the layout table. A set is named before it is checked, so every binding here
// has one; the empty case is written out rather than trusted, since a line with no id would be a
// binding renamed at the next start — the one thing a name exists to prevent.
func storeEntries(set bindings) ([]json.RawMessage, error) {
	entries := make([]json.RawMessage, 0, len(set))

	for _, b := range set {
		segment := b.Segment

		line := storedEntry{ID: b.ID, Printer: b.DeviceID, Controller: b.Address, Segment: &segment}

		if b.Segment < 0 {
			brightness := b.Brightness
			line.Brightness = &brightness
		}

		raw, err := json.Marshal(line)
		if err != nil {
			return nil, fmt.Errorf("render binding of printer %s for the configuration store: %w", b.DeviceID, err)
		}

		entries = append(entries, raw)
	}

	return entries, nil
}
