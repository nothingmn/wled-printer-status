// The layout a binding composes its frame with, per binding, as the Layout screen edits it.
//
// # Why per binding, when the composer has always been one setting
//
// The `layout` setting is one object for the whole instance, and that is right for an install that
// drives one panel. It stops being right the moment a second light is bound: a 256x64 panel and a
// 48-LED strip do not compose the same way — the strip draws a fill and nothing else, and a bar band
// on it would be a part of a picture that has one dimension — and two bindings on one light can be
// laid out differently because the two screens are looked at from different places.
// docs/product-requirements.md §2.5 is the requirement, and the approved Layout mockup
// (docs/design/v2/layout/) is the control surface: one layout per binding.
//
// # What it carries, and where each key came from
//
// The keys are the mockup's own, and the mockup's README says the controls *are* the setting keys:
// `bar`, `bar_band`, `bar_rows`, `percent`, `name`, `remaining` and `rotate_seconds`, with
// `remaining` a disabled control because this build draws no time remaining. Two keys the mockup's
// JSON block does not show are here because §2.5 asks for them and the requirements outrank the
// mockup: `percent_scale`, which is the "scale 5/4/3" of §2.5 and the mockup's own percentage
// control, and `rotation`, which is §2.5's "rotation of `take-turns` or **`side-by-side`**" and the
// mockup's *"take turns or side by side — both, configurable"*.
//
// # `rotation` is drawn
//
// `side-by-side` is built: the painter puts the digits on the left and the job's name on the
// right, cut to the room the digits leave, and it says so in one line on every answer because the
// digits give up a scale there (see layout.go's `beside`). `take-turns` is what ships and what a
// binding with no row of its own composes with.
//
// # Absent is a default, not a zero
//
// The store holds one row per binding somebody has *changed*, and only the fields it changed — for
// the reason the state table holds only what a person set. A binding with no row, or a row that
// names one field, composes with the built-in default for everything else, and every value in an
// answer carries the origin it came from. That is what lets a screen offer "reset" on exactly the
// values that have something to reset, and it is what guarantees that a layout nobody edited is
// reported as the default rather than as an empty object.
//
// # And it is what the painter reads
//
// `bindingLayout.shape` is the one conversion from this table into the value the composer draws
// with, and show.go is the one reader that asks for it. A binding with no row is not this file's
// business: the painter then composes with the `layout` setting, which is the migration source —
// the same shape the brightness windows keep (docs/product-requirements.md §4).
package main

import (
	"fmt"
	"strings"
	"time"
)

const (
	// rotationTakeTurns and rotationSideBySide are the two values `rotation` accepts, in
	// docs/product-requirements.md §2.5's own words: "rotation of `take-turns` or `side-by-side`".
	//
	// take-turns is what ships: the percentage holds the panel, then the job's name takes it, and
	// back. side-by-side puts the digits on the left and the name on the right, and it needs a
	// build — see the note every answer about it carries.
	rotationTakeTurns  = "take-turns"
	rotationSideBySide = "side-by-side"

	// minPercentScale is the smallest scale a stored layout may pin the percentage to. Scales 3, 4
	// and 5 are what §2.5 offers — "percentage at scale 5/4/3" — and anything else is refused by
	// name rather than rounded to the nearest, because a digit size nobody asked for is a picture
	// nobody asked for.
	minPercentScale = 3
)

// rotationChoices is what `rotation` accepts, in the order the screen offers them.
//
//nolint:gochecknoglobals // one closed list, read and never written.
var rotationChoices = []string{rotationTakeTurns, rotationSideBySide}

// bindingLayoutKeys is the key set of one layout, for a refusal that has to say what it would have
// accepted — the same sentence pattern the bindings setting answers with.
func bindingLayoutKeys() string {
	return "bar, bar_band, bar_rows, percent, percent_scale, name, remaining, ends, temperatures, elapsed, " +
		"material, printer_name, layer, rotation and rotate_seconds"
}

// storedLayout is one binding's layout as the document holds it: the binding it is about, and only
// the fields somebody changed.
//
// The row is keyed on the binding's id and not on the printer, because a printer may now sit on
// several lights (docs/product-requirements.md §2.2) and the same printer's panel and strip do not
// compose the same way. A printer-keyed row would be one picture shared by two very different
// displays, and the second binding's layout would silently overwrite the first's.
//
// Every field is a pointer for the reason a stored state's are: absent is not zero. A `bar_rows` of
// 0 is not a row count at all and must not be read as "no bar" — that is `bar: false` — and a
// `rotate_seconds` of 0 is not a rotation. `remaining` is deliberately *not* a field: it can only
// ever be false in this build, so writing it into the document would be writing a value nothing can
// change, and a request that asks for true is refused by name before the row is built.
type storedLayout struct {
	// Binding is the binding's id on the routes — the printer's device id while one printer is
	// shown once, and the device id, light and segment once it is shown twice. Schema version 4
	// is what renamed this key from `printer`; see store.go's v3 upgrade.
	Binding string `json:"binding"`

	Bar           *bool   `json:"bar,omitempty"`
	BarBand       *string `json:"bar_band,omitempty"`
	BarRows       *int    `json:"bar_rows,omitempty"`
	Percent       *bool   `json:"percent,omitempty"`
	PercentScale  *int    `json:"percent_scale,omitempty"`
	Name          *bool   `json:"name,omitempty"`
	Rotation      *string `json:"rotation,omitempty"`
	RotateSeconds *int    `json:"rotate_seconds,omitempty"`

	// The seven optional lines, each a pointer for the reason every other field is: absent is not
	// false. A row written before schema version 6 has none of them, and none of them being
	// written is what "this binding draws none" means — so the upgrade from 5 changes no row, and
	// a row that carries `"material": false` is a person who switched it off deliberately.
	//
	// `remaining` was not a field at all before version 6: version 5's build refused it with a
	// sentence, because nothing would have drawn it. This build draws it, so a row may now say
	// so — see layout.go's layoutJSON for the setting that carries the same seven keys.
	Remaining    *bool `json:"remaining,omitempty"`
	Ends         *bool `json:"ends,omitempty"`
	Temperatures *bool `json:"temperatures,omitempty"`
	Elapsed      *bool `json:"elapsed,omitempty"`
	Material     *bool `json:"material,omitempty"`
	PrinterName  *bool `json:"printer_name,omitempty"`
	Layer        *bool `json:"layer,omitempty"`
}

// empty says whether this row carries nothing at all, which is a row that is never written: DELETE
// is how a binding goes back to its default, and a row of defaults in the file would be a second
// statement of the default drifting from the first.
func (l storedLayout) empty() bool {
	return l.Bar == nil && l.BarBand == nil && l.BarRows == nil && l.Percent == nil &&
		l.PercentScale == nil && l.Name == nil && l.Rotation == nil && l.RotateSeconds == nil &&
		l.Remaining == nil && l.Ends == nil && l.Temperatures == nil && l.Elapsed == nil &&
		l.Material == nil && l.PrinterName == nil && l.Layer == nil
}

// bindingLayout is one binding's layout with every default filled in: what the composer would draw
// from it once the painter reads this table.
//
// It is a value of its own rather than the stored row, because a row that carries one field answers
// for all eight: this is the resolved reading, and the row is what a person wrote.
type bindingLayout struct {
	bar          bool
	barBand      string
	barRows      int
	percent      bool
	percentScale int
	name         bool
	rotation     string
	rotate       time.Duration

	// The seven optional lines, all false in the default: a binding nobody has laid out draws the
	// picture this module has always drawn.
	remaining    bool
	ends         bool
	temperatures bool
	elapsed      bool
	material     bool
	printer      bool
	layer        bool
}

// defaultBindingLayout is what a binding composes with when nobody has laid it out.
//
// It is the same picture defaultLayout in layout.go describes — a bar at the bottom twelve rows
// thick, the percentage and the name taking turns every fifteen seconds — expressed with the two keys
// §2.5 adds. The percentage's scale is the largest this build draws, which is what the painted
// default reaches on the 64-row panel it ships for: 64 rows less a 12-row bar leaves 52, and 52/7 is
// seven, so the cap of five is what decides.
//
// It is written out here rather than read from layout.go's defaultLayout because these are the
// stored table's defaults and a screen has to be told them even on an instance that never configured
// a layout at all. A test holds the two to the same picture.
func defaultBindingLayout() bindingLayout {
	return bindingLayout{
		bar:          true,
		barBand:      bandBottom,
		barRows:      defaultBarRows,
		percent:      true,
		percentScale: percentScaleMax,
		name:         true,
		rotation:     rotationTakeTurns,
		rotate:       defaultRotateSeconds * time.Second,
	}
}

// resolved is the layout this row describes: the defaults, with every field the row names applied.
//
// The row is assumed checked — checkStoredLayout has run on it on the way in and again on the way
// out — so nothing here refuses anything. What it does is the whole point of the pointer fields: a
// field the row leaves out keeps the default rather than becoming a zero.
func (l storedLayout) resolved() bindingLayout {
	got := defaultBindingLayout()

	if l.Bar != nil {
		got.bar = *l.Bar
	}

	if l.BarBand != nil {
		got.barBand = strings.ToLower(strings.TrimSpace(*l.BarBand))
	}

	if l.BarRows != nil {
		got.barRows = *l.BarRows
	}

	if l.Percent != nil {
		got.percent = *l.Percent
	}

	if l.PercentScale != nil {
		got.percentScale = *l.PercentScale
	}

	if l.Name != nil {
		got.name = *l.Name
	}

	if l.Rotation != nil {
		got.rotation = strings.ToLower(strings.TrimSpace(*l.Rotation))
	}

	if l.RotateSeconds != nil {
		got.rotate = time.Duration(*l.RotateSeconds) * time.Second
	}

	got.remaining = onOff(l.Remaining, got.remaining)
	got.ends = onOff(l.Ends, got.ends)
	got.temperatures = onOff(l.Temperatures, got.temperatures)
	got.elapsed = onOff(l.Elapsed, got.elapsed)
	got.material = onOff(l.Material, got.material)
	got.printer = onOff(l.PrinterName, got.printer)
	got.layer = onOff(l.Layer, got.layer)

	return got
}

// shape is this layout as the composer draws it: the one conversion from the stored table into the
// value layout.go's `panelPicture` reads.
//
// It is a method rather than a function of eight arguments for the reason resolved is one: the
// field names on both sides are close enough to invite a mis-ordered literal, and one conversion is
// one place to get wrong instead of three.
func (l bindingLayout) shape() layout {
	return layout{
		bar:          l.bar,
		barBand:      l.barBand,
		barRows:      l.barRows,
		percent:      l.percent,
		percentScale: l.percentScale,
		name:         l.name,
		rotation:     l.rotation,
		rotate:       l.rotate,
		remaining:    l.remaining,
		ends:         l.ends,
		temperatures: l.temperatures,
		elapsed:      l.elapsed,
		material:     l.material,
		printer:      l.printer,
		layer:        l.layer,
	}
}

// checkStoredLayout refuses a layout this build cannot act on, naming the field and the value.
//
// It runs on the way in — a route will not write one — and again on the way out, because the document
// is a file a person can edit with a text editor. A row that would otherwise be obeyed as written is
// refused with the binding named, which is the same answer this module gives a document it cannot
// read at all: nothing is half-applied and the file is the thing to open.
//
// What it deliberately does not judge is whether the layout **fits** the panel it is pointed at.
// §2.5's rule is "a layout that will not fit is refused, not cropped", and the refusal already lives
// in the one place that knows the geometry: layout.go's bandsOn, at the moment a frame is drawn. A
// route cannot judge it — the panel's own rows arrive from the controller's /json/matrix, and a
// layout is written while the light may be unplugged, which is exactly when somebody is standing in
// front of it with a phone. So the fixed limits are judged here and the arithmetic is judged there.
func checkStoredLayout(one storedLayout) error {
	if one.BarBand != nil {
		band := strings.ToLower(strings.TrimSpace(*one.BarBand))

		if band != bandTop && band != bandBottom {
			return fmt.Errorf(
				"bar_band is %q, and a bar sits at the top or the bottom of the panel: write %q or %q",
				*one.BarBand, bandTop, bandBottom)
		}
	}

	if one.BarRows != nil && (*one.BarRows < 1 || *one.BarRows > maxBarRows) {
		return fmt.Errorf(
			"bar_rows is %d, and a bar takes 1 to %d rows of a 64-row panel so that at least %d rows are left for the text",
			*one.BarRows, maxBarRows, 64-maxBarRows)
	}

	if one.PercentScale != nil && (*one.PercentScale < minPercentScale || *one.PercentScale > percentScaleMax) {
		return fmt.Errorf(
			"percent_scale is %d, and the percentage is drawn at scale %d, 4 or %d: a scale outside those is refused rather than rounded, because a digit size nobody asked for is a picture nobody asked for",
			*one.PercentScale, minPercentScale, percentScaleMax)
	}

	if one.Rotation != nil {
		rotation := strings.ToLower(strings.TrimSpace(*one.Rotation))

		if rotation != rotationTakeTurns && rotation != rotationSideBySide {
			return fmt.Errorf(
				"rotation is %q, and the percentage and the name either take turns or sit side by side: write %s",
				*one.Rotation, strings.Join(quoted(rotationChoices), " or "))
		}
	}

	if one.RotateSeconds != nil && *one.RotateSeconds < 1 {
		return fmt.Errorf(
			"rotate_seconds is %d, and a rotation of no seconds is not a rotation: it is the whole number of seconds each half holds the panel, such as %d",
			*one.RotateSeconds, defaultRotateSeconds)
	}

	return nil
}

// layoutRows is the document's layout table as it stands, by binding id, with every row checked.
//
// A row naming a binding this build no longer drives is left out here and reported by
// unknownLayouts instead of being applied: a binding that was removed keeps its layout in the file
// — a deletion is not this route's to erase, and adding the binding back should find it again — but
// a table that quietly grew a row nobody can see would be the failure the version stamp exists to
// prevent.
func layoutRows(config storedConfig) (map[string]storedLayout, error) {
	rows := make(map[string]storedLayout, len(config.Layouts))

	for _, one := range config.Layouts {
		binding := strings.TrimSpace(one.Binding)
		if binding == "" {
			return nil, fmt.Errorf("a layout row names no binding, and a layout belongs to one binding")
		}

		if err := checkStoredLayout(one); err != nil {
			return nil, fmt.Errorf("the layout for %s: %w", binding, err)
		}

		one.Binding = binding
		rows[binding] = one
	}

	return rows, nil
}

// layoutFor is one binding's row, and false when nobody has laid that binding out.
func layoutFor(rows map[string]storedLayout, binding string) (storedLayout, bool) {
	one, held := rows[binding]

	return one, held
}

// unknownLayouts is the bindings a document holds a layout for that the running set does not drive.
// They are reported beside the table rather than dropped in silence, for the reason unknownStates is.
func unknownLayouts(config storedConfig, set bindings) []string {
	driven := make(map[string]bool, len(set))

	for _, b := range set {
		driven[b.ID] = true
	}

	unknown := make([]string, 0)

	for _, one := range config.Layouts {
		if binding := strings.TrimSpace(one.Binding); binding != "" && !driven[binding] {
			unknown = append(unknown, binding)
		}
	}

	return unknown
}

// setLayout puts one binding's layout into the document, replacing whatever row that binding had.
//
// The row is replaced whole rather than merged, for the reason setState replaces a state's row whole:
// a route that merged would have to decide, for every absent field, whether the caller meant "leave
// it" or "clear it", and the one answer that is always wrong is the guess. A field goes back to its
// default by not being written in a route that carries all eight, or by DELETE.
func setLayout(config *storedConfig, one storedLayout) {
	binding := strings.TrimSpace(one.Binding)

	kept := make([]storedLayout, 0, len(config.Layouts)+1)

	for _, held := range config.Layouts {
		if strings.TrimSpace(held.Binding) != binding {
			kept = append(kept, held)
		}
	}

	one.Binding = binding

	config.Layouts = append(kept, one)
}

// withoutLayout is the document with one binding's layout taken away, and false when it had none.
func withoutLayout(config *storedConfig, binding string) bool {
	wanted := strings.TrimSpace(binding)

	kept := make([]storedLayout, 0, len(config.Layouts))
	removed := false

	for _, held := range config.Layouts {
		if strings.TrimSpace(held.Binding) == wanted {
			removed = true

			continue
		}

		kept = append(kept, held)
	}

	config.Layouts = kept

	return removed
}

// layoutAnswer is one binding's layout as a caller reads it: every value in force, where each one
// came from, and the two fields that say what the value means for this build.
//
// An origin beside each value is the whole reason the shape is this wide. A screen that showed a
// bar band without saying whether it is the store's or the default could not offer the right button:
// there is nothing to reset on a default, and a default shown as though it were an override is a
// change a person believes he made.
type layoutAnswer struct {
	// ID is the binding's name on these routes, and Light, Segment and Whole are the rest of
	// what makes it one binding: a layout belongs to a segment of a light, not to a printer in
	// the abstract. Printer is the machine it shows, which is no longer the same thing as the
	// binding once a printer sits on two lights.
	ID      string `json:"id"`
	Printer string `json:"printer"`
	Light   string `json:"light"`
	Segment int    `json:"segment"`
	Whole   bool   `json:"whole"`

	Bar           bool   `json:"bar"`
	BarOrigin     string `json:"bar_origin"`
	BarBand       string `json:"bar_band"`
	BarBandOrigin string `json:"bar_band_origin"`
	BarRows       int    `json:"bar_rows"`
	BarRowsOrigin string `json:"bar_rows_origin"`

	Percent            bool   `json:"percent"`
	PercentOrigin      string `json:"percent_origin"`
	PercentScale       int    `json:"percent_scale"`
	PercentScaleOrigin string `json:"percent_scale_origin"`

	Name       bool   `json:"name"`
	NameOrigin string `json:"name_origin"`

	Rotation       string `json:"rotation"`
	RotationOrigin string `json:"rotation_origin"`
	RotateSeconds  int    `json:"rotate_seconds"`
	RotateOrigin   string `json:"rotate_seconds_origin"`

	// The seven optional lines, each with the origin it came from, in the operator's own words.
	//
	// `remaining` is one of them now. It used to be reported as a constant false beside a note
	// saying this build drew none; it draws one, and the answer says so with the same shape every
	// other value here has.
	Remaining          bool   `json:"remaining"`
	RemainingOrigin    string `json:"remaining_origin"`
	Ends               bool   `json:"ends"`
	EndsOrigin         string `json:"ends_origin"`
	Temperatures       bool   `json:"temperatures"`
	TemperaturesOrigin string `json:"temperatures_origin"`
	Elapsed            bool   `json:"elapsed"`
	ElapsedOrigin      string `json:"elapsed_origin"`
	Material           bool   `json:"material"`
	MaterialOrigin     string `json:"material_origin"`
	PrinterName        bool   `json:"printer_name"`
	PrinterNameOrigin  string `json:"printer_name_origin"`
	Layer              bool   `json:"layer"`
	LayerOrigin        string `json:"layer_origin"`

	// Zone is the IANA zone the ETA line's clock is written in — the `timezone` setting's — and
	// empty when this module has none it can load. A screen needs it for one sentence: the ETA line
	// writes a clock time and nothing else, so with no zone it is a key that would draw nothing.
	Zone string `json:"zone"`

	// Origin is `store` when this binding has a row in the document and `default` when it has none,
	// and Overridden says the same thing in the shape a screen branches on.
	Origin     string `json:"origin"`
	Overridden bool   `json:"overridden"`

	// RotationNote says what this build does with the rotation it was given, and is present only
	// when that differs from what was asked for. It is a sentence rather than a second boolean
	// because "needs a build" is the fact, and a boolean called needs_build would be one screen's
	// wording rather than the module's.
	RotationNote string `json:"rotation_note,omitempty"`
}

// layoutAnswerOf renders one binding's layout: the row it has — possibly none — the picture that row
// describes, and where each part of that picture came from.
//
// zone is the IANA zone the ETA line's clock is written in, and empty when this module has none it
// can load: it is the `timezone` setting's and not the layout's, so it is handed in rather than
// stored on the row.
func layoutAnswerOf(b binding, light controllerAnswer, row storedLayout, stored bool, zone string) layoutAnswer {
	resolved := row.resolved()

	origin := fromDefault
	if stored {
		origin = fromStore
	}

	originOf := func(written bool) string {
		if written {
			return fromStore
		}

		return fromDefault
	}

	said := layoutAnswer{
		ID:      b.ID,
		Printer: b.DeviceID,
		Light:   light.Name,
		Segment: b.Segment,
		Whole:   b.Segment < 0,

		Bar:           resolved.bar,
		BarOrigin:     originOf(row.Bar != nil),
		BarBand:       resolved.barBand,
		BarBandOrigin: originOf(row.BarBand != nil),
		BarRows:       resolved.barRows,
		BarRowsOrigin: originOf(row.BarRows != nil),

		Percent:            resolved.percent,
		PercentOrigin:      originOf(row.Percent != nil),
		PercentScale:       resolved.percentScale,
		PercentScaleOrigin: originOf(row.PercentScale != nil),

		Name:       resolved.name,
		NameOrigin: originOf(row.Name != nil),

		Rotation:       resolved.rotation,
		RotationOrigin: originOf(row.Rotation != nil),
		RotateSeconds:  int(resolved.rotate / time.Second),
		RotateOrigin:   originOf(row.RotateSeconds != nil),

		Remaining:          resolved.remaining,
		RemainingOrigin:    originOf(row.Remaining != nil),
		Ends:               resolved.ends,
		EndsOrigin:         originOf(row.Ends != nil),
		Temperatures:       resolved.temperatures,
		TemperaturesOrigin: originOf(row.Temperatures != nil),
		Elapsed:            resolved.elapsed,
		ElapsedOrigin:      originOf(row.Elapsed != nil),
		Material:           resolved.material,
		MaterialOrigin:     originOf(row.Material != nil),
		PrinterName:        resolved.printer,
		PrinterNameOrigin:  originOf(row.PrinterName != nil),
		Layer:              resolved.layer,
		LayerOrigin:        originOf(row.Layer != nil),

		Zone: zone,

		Origin:     origin,
		Overridden: stored,
	}

	if resolved.rotation == rotationSideBySide {
		said.RotationNote = rotationSideBySideNote
	}

	return said
}

// rotationSideBySideNote is what every answer about a side-by-side rotation says, because the
// picture it draws is not the same scale the take-turns picture uses and a screen that showed the
// stored scale without saying so would be describing a picture nobody will see.
const rotationSideBySideNote = "side by side, the digits sit on the left and the job's name on the right, cut to the room " +
	"that is left: the digits are drawn at scale 4 at most there (3 when the text band is short), so that the name " +
	"has columns to be read in. A name with less than three glyphs of room is not drawn at all."
