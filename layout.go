// The panel's layout, as data, and the one function that draws a frame from it.
//
// A frame is a colour and a fill: green while a print runs, a bar that moves, the finished colour
// once it is over. On a strip that is the whole of what a person can be told. On a 256x64 panel it
// is one colour filling one rectangle, and the operator asked for more from it, in his own words:
// *"we could also show the percentage complete, we could render the name of the print job itself,
// and a progress bar"*, *"every X number of seconds to swap over to the name of the print itself"*,
// and *"be able to choose what we want displayed, for example to be able to choose a progress bar,
// say taking up the TOP OR BOTTOM 1 to X pixels across"*.
//
// So a panel's picture is composed from parts, and the arrangement is a value rather than a
// constant in a painter:
//
//   - a **bar** in a band at the top or the bottom of the panel, 1 to 48 rows thick, filled to the
//     job's own fraction of the width;
//   - the **percentage** in digits, in the rest of the panel;
//   - the **job's name**, on the other half of a rotation, because a name is up to 38 characters
//     and a panel is 256 pixels wide;
//   - **time remaining**, off, because this build draws none — see the refusal below.
//
// # Why the arrangement is a setting and not a constant
//
// The manifest's `options` has nine closed types and a `list` or an `object` is a one-line JSON box
// in the settings pane (AGENTS.md §5.8), so a layout cannot be a form of sliders. It can be an
// object written in that box, and that is what it is: `layout`, whose keys are the parts above.
// Absent, or `{}`, it is the default layout — a bar at the bottom twelve rows thick, the percentage
// and the name rotating every fifteen seconds — which is the picture the operator asked for and is
// what an install that never touches the box runs. Every key may be switched off, and a layout with
// everything off draws nothing of its own: the panel then takes the plain frame it has always had,
// which is how an operator puts his panel back exactly as it was before this setting existed.
//
// # The two things this file will not do
//
// **It will not crop.** A bar too thick for the panel it is pointed at, or a bar that leaves less
// than a glyph's worth of rows for the text, is refused with the field, the value and the panel's
// own height — never drawn short, and never quietly shrunk. §5.4's rule about a word that does not
// fit is the same rule one level up.
//
// **It will not draw a part it cannot.** `remaining: true` is refused rather than ignored: a key
// that asks for a picture and gets none is a setting that lies, which is the failure §5.9 records
// for the manifest's closed key set.
//
// # What it does draw, and in what colour
//
// Everything is the state colour (`render.Frame.Colour`) on the panel's own background — the colour
// `render.Frame.Unfilled()` gives an empty range, which the event layer already paints behind the
// word. So a printing panel is the printing green on black: a bar that grows, and the digits or the
// name. Nothing here invents a palette, and the state table is untouched.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/nothingmn/wled-printer-status/internal/render"
	"github.com/nothingmn/wled-printer-status/internal/text"
	"github.com/nothingmn/wled-printer-status/internal/wled"
)

const (
	// defaultBarRows is how many rows the bar takes out of a 64-row panel when nobody has written
	// a layout: twelve.
	//
	// A twelfth of the panel's height, which reads as a bar rather than as a stripe, and it leaves
	// 52 of the 64 rows for the text — enough for the digits at the largest scale this module
	// draws, with margin above and below them. Twelve rows of sixteen wide is also the
	// conventional proportion of a progress bar, and the point of the number is that it is one
	// line in a settings box away from being something else.
	defaultBarRows = 12

	// defaultRotateSeconds is how long the percentage holds the panel before the job's name takes
	// it, and back: fifteen seconds.
	//
	// The operator asked for *"every X number of seconds"* and named no number, so this is a
	// value chosen here and named in one place. Fifteen is long enough to read a number from
	// across a room without counting, and short enough that somebody who glances at the panel for
	// half a minute sees both halves without waiting for one.
	defaultRotateSeconds = 15

	// maxBarRows is the thickest bar a layout may ask for: 48 of a panel's 64 rows.
	//
	// A third of the panel is left for the text, which is what keeps the digits legible at the
	// smallest scale the layout will use. It is a refusal rather than a clamp for the reason the
	// whole file exists: a bar clamped from 60 rows to 48 would be a picture the operator did not
	// ask for, drawn without saying so.
	maxBarRows = 48

	// nameScaleMax is the largest scale the job's name is drawn at: two pixels per glyph pixel.
	//
	// The percentage is two to four characters and is the thing read from across the room, so it
	// gets the biggest scale that fits (§see percentScaleMax). A name is up to 38 characters, and
	// the question is not how tall it can be but how much of it fits: at scale 2, 21 characters
	// span a 256-wide panel — enough for "MK4IS 0.4N 0.2MM PLA", which is the head of a Prusa
	// file name and the part that identifies the job. At scale 3 only 14 characters fit
	// ("MK4IS 0.4N"), and at 5 only 8. The content wins over the size, deliberately: a name that
	// reads "MK4IS 0.4N" is a fragment, and a fragment is what this file exists to avoid.
	nameScaleMax = 2

	// percentScaleMax is the largest scale the digits are drawn at: five, the same scale the
	// finished word is drawn at.
	//
	// Not a coincidence and not a second decision: the word FINISHED is the largest thing this
	// module puts on the panel, the operator has seen it and said it looks right, so the composed
	// picture's text is never larger. On a 64-row panel with a 12-row bar the text band is 52
	// rows, and 52/7 is seven — the cap is what decides, not the room. At five, "100%" is 4 glyphs
	// and 23 glyph columns, 115 pixels wide and 35 tall, centred in the band.
	percentScaleMax = finishedWordScale

	// bandTop and bandBottom are the two values `bar_band` accepts, in the operator's own words:
	// *"taking up the TOP OR BOTTOM 1 to X pixels across"*.
	bandTop    = "top"
	bandBottom = "bottom"

	// sideMargin is the columns the side-by-side arrangement leaves at the left edge, sideGap
	// the columns between the digits' box and the name's, and sideRight is what it leaves at
	// the right — the mockup's own 10, 16 and 4, which are the 30 it takes off the panel's
	// width before it divides for the name.
	sideMargin = 10
	sideGap    = 16
	sideRight  = 4

	// sideNameScale is the scale the name is drawn at beside the digits: two, the same
	// nameScaleMax, because the name is the part that has to fit rather than the part read from
	// across the room.
	sideNameScale = 2

	// sideDigitsScale is the largest scale the digits take when they share the row with the
	// name, and sideDigitsScaleShort the largest when the text band is too short for that.
	//
	// This is the mockup's own rule — `Math.min(pctScale, freeH>=28?4:3)` — and it is a cap
	// rather than a fit: side by side, the name's fourteen rows and the digits' box have to sit
	// on one line, and the digits give up a scale so that the name has room to be read. The
	// operator approved the picture; the arithmetic is his mockup's.
	sideDigitsScale      = 4
	sideDigitsScaleShort = 3

	// sideBandForFull is the text band below which the digits drop to sideDigitsScaleShort.
	sideBandForFull = 28

	// sideNameMin is the fewest glyphs of a job's name the side-by-side arrangement will draw.
	//
	// Three, which is the mockup's `chars>2`, and below it the name is **not drawn at all** —
	// not cut to two glyphs and not ellipsised. Two letters of a slicer's file name identify
	// nothing, and this font's only punctuation is a full stop, so there is no mark to make of
	// it. The digits stand alone on that panel, which is what the mockup draws.
	sideNameMin = 3
)

// layout is a panel's arrangement: which parts are drawn and where the bar sits.
//
// The zero value is not the default. It is a layout that draws nothing of its own — no bar, no
// digits, no name — which is the picture a panel had before this setting existed, and the reason a
// writer built by a test composes nothing unless a test hands it a layout.
type layout struct {
	// bar draws a progress bar in a band of barRows rows at barBand.
	bar bool
	// barBand is bandTop or bandBottom, and the band the bar occupies when bar is set.
	barBand string
	// barRows is how many rows the bar takes, 1 to maxBarRows.
	barRows int
	// percent draws the job's progress in digits, such as 41%.
	percent bool
	// percentScale pins the digits to one of scales 3, 4 and 5, and 0 means "as large as the
	// text band allows, up to percentScaleMax" — which is what the `layout` setting says,
	// because it has no key for the scale, and what this module drew before the Layout screen
	// could pin one.
	//
	// A pinned scale is a fit question and it is refused rather than shrunk: see bandsOn.
	percentScale int
	// name draws the job's own name, uppercased and cut to what fits.
	name bool
	// rotation is how the digits and the name share the text band: rotationTakeTurns, where one
	// holds the panel and then the other, or rotationSideBySide, where the digits sit on the
	// left and the name on the right. Empty is take-turns, which is what the setting says and
	// what this module drew before side-by-side was built.
	rotation string
	// rotate is how long each half holds the panel before the other takes it, and is unused by
	// side-by-side, where both are up at once.
	rotate time.Duration
}

// defaultLayout is what a panel draws when nobody has written a layout: a bar at the bottom, the
// percentage and the name rotating.
func defaultLayout() layout {
	return layout{
		bar:     true,
		barBand: bandBottom,
		barRows: defaultBarRows,
		percent: true,
		name:    true,
		// Empty is take-turns: the setting has no key for the rotation, so what it parses to is
		// the zero value and the painter reads it as what ships. The stored table's default names
		// it outright, because a screen has to be told the value even on an install that never
		// wrote one.
		rotate: defaultRotateSeconds * time.Second,
	}
}

// draws reports whether this layout draws anything of its own.
//
// False is the whole-layout opt-out: the panel takes the plain frame it has always been given — one
// colour over one rectangle or two — and nothing in this file runs. That is the value a writer
// built without a layout carries, which is every test that does not ask for one.
func (l layout) draws() bool { return l.bar || l.percent || l.name }

// layoutJSON is one layout as the settings box carries it: a JSON object, every key optional.
//
// Every field is a pointer so that an absent key is distinguishable from a false one — `{"name":
// false}` must switch the name off, and `{}` must switch nothing off. The keys are the operator's
// words and nothing else; a key this struct does not carry refuses the start, the way every other
// settings block in this module does.
type layoutJSON struct {
	Bar           *bool   `json:"bar"`
	BarBand       *string `json:"bar_band"`
	BarRows       *int    `json:"bar_rows"`
	Percent       *bool   `json:"percent"`
	Name          *bool   `json:"name"`
	Remaining     *bool   `json:"remaining"`
	RotateSeconds *int    `json:"rotate_seconds"`
}

// layoutKeys is the key set, for a refusal that has to say what it would have accepted — the same
// sentence pattern the bindings setting answers with.
func layoutKeys() string {
	return "bar, bar_band, bar_rows, percent, name, remaining and rotate_seconds"
}

// parseLayout reads the `layout` setting.
//
// Absent, empty and `{}` are all the default layout, which is what makes the setting optional and
// what an install that never writes it runs. Everything else is read strictly: a misspelt key, a
// band this module does not know, a bar too thick to leave room for the text, a rotation of no
// seconds, or a part this build cannot draw is refused here, at configure, with the field and the
// value in the sentence. A layout that reaches the writer is one this module can act on.
func parseLayout(raw json.RawMessage) (layout, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return defaultLayout(), nil
	}

	if trimmed[0] != '{' {
		return layout{}, fmt.Errorf(
			"layout: this setting is one JSON object, such as {\"bar\": true, \"percent\": true, \"name\": true}, and it was given %s",
			firstBytePhrase(trimmed[0]))
	}

	var carried layoutJSON

	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(&carried); err != nil {
		return layout{}, fmt.Errorf("layout: %w; it carries %s", err, layoutKeys())
	}

	got := defaultLayout()

	if carried.Bar != nil {
		got.bar = *carried.Bar
	}

	if carried.BarBand != nil {
		band := strings.ToLower(strings.TrimSpace(*carried.BarBand))

		switch band {
		case bandTop, bandBottom:
			got.barBand = band
		default:
			return layout{}, fmt.Errorf(
				"layout: bar_band is %q, and a bar sits at the top or the bottom of the panel: write %q or %q",
				*carried.BarBand, bandTop, bandBottom)
		}
	}

	if carried.BarRows != nil {
		if *carried.BarRows < 1 || *carried.BarRows > maxBarRows {
			return layout{}, fmt.Errorf(
				"layout: bar_rows is %d, and a bar takes 1 to %d rows of a 64-row panel so that at least %d rows are left for the text",
				*carried.BarRows, maxBarRows, 64-maxBarRows)
		}

		got.barRows = *carried.BarRows
	}

	if carried.Percent != nil {
		got.percent = *carried.Percent
	}

	if carried.Name != nil {
		got.name = *carried.Name
	}

	// Refused rather than read as false. A key that asks for a picture this build does not draw,
	// and is answered with silence, is a setting that lies about what the panel will show.
	if carried.Remaining != nil && *carried.Remaining {
		return layout{}, fmt.Errorf(
			"layout: remaining is true, and this build draws no time remaining: nothing would put it on the panel, so it is refused rather than ignored — switch it off, or ask for it as a change that draws it")
	}

	if carried.RotateSeconds != nil {
		if *carried.RotateSeconds < 1 {
			return layout{}, fmt.Errorf(
				"layout: rotate_seconds is %d, and a rotation of no seconds is not a rotation: it is the whole number of seconds each half holds the panel, such as %d",
				*carried.RotateSeconds, defaultRotateSeconds)
		}

		got.rotate = time.Duration(*carried.RotateSeconds) * time.Second
	}

	// Every part switched off is the panel as it was before this setting existed, and that is the zero
	// layout — the value a writer carries when it was built without one. One value for one meaning, so
	// that "this instance draws nothing of its own" is a single comparison rather than a search for
	// which of the parts might still be on.
	if !got.draws() {
		return layout{}, nil
	}

	return got, nil
}

// firstBytePhrase names what a caller wrote where an object was expected, in words rather than in
// a byte value a person would have to look up.
func firstBytePhrase(leading byte) string {
	switch leading {
	case '[':
		return "a list, and a layout is one object rather than a list of them"
	case '"':
		return "a string, and a layout is an object"
	default:
		return fmt.Sprintf("something beginning %q", string(leading))
	}
}

// panelBands is where a layout puts the bar and the text on one panel, in rows counted from the top
// row.
type panelBands struct {
	// barTop is the panel row the bar's first row is, and barRows how many rows it covers. Both
	// are zero when the layout draws no bar.
	barTop  int
	barRows int
	// textTop is the row the text band starts at and textRows how tall it is: the whole panel
	// less the bar, whichever end the bar is on.
	textTop  int
	textRows int
}

// bandsOn is where this layout puts its parts on a panel of that geometry, or why it cannot.
//
// The refusal is the point of the function. A bar_rows thicker than the panel, or one that leaves
// fewer rows than a glyph, is named with both numbers — the layout's and the panel's — because the
// two together are what is wrong: twelve rows is a perfectly good bar on the operator's 64-row
// panel and an impossible one on a 12-row panel, and only the pair says which.
func (l layout) bandsOn(geometry panel) (panelBands, error) {
	if !geometry.matrix() {
		return panelBands{}, fmt.Errorf("a layout needs a panel, and %dx%d is not one", geometry.width, geometry.height)
	}

	rows := 0
	if l.bar {
		rows = l.barRows
	}

	if rows < 0 || rows >= geometry.height {
		return panelBands{}, fmt.Errorf(
			"layout: bar_rows is %d and this panel is %d rows high, so the bar would cover it and leave no rows for the picture",
			rows, geometry.height)
	}

	bands := panelBands{barRows: rows, textRows: geometry.height - rows}

	if l.percent || l.name {
		if bands.textRows < text.GlyphRows {
			return panelBands{}, fmt.Errorf(
				"layout: bar_rows is %d and this panel is %d rows high, which leaves %d rows for the text, and the smallest word this font draws is %d rows",
				rows, geometry.height, bands.textRows, text.GlyphRows)
		}
	}

	// A scale the row pinned is a fit question like the band's, and it is refused here for the
	// same reason: the digits at scale 5 are 35 rows, and a panel that leaves 34 for the text
	// would draw them cropped — which is the one thing this file will not do. The mockup says
	// the same thing in as many words: "set the bar to 30 rows and the digits no longer fit —
	// the readout turns red and the panel draws nothing rather than silently cropping."
	//
	// A scale of 0 is "whatever the band allows", which is what the `layout` setting asks for
	// because it has no key for the scale; that rule cannot refuse, because it never asks for
	// more rows than the band has.
	if l.percent && l.percentScale > 0 && l.percentScale*text.GlyphRows > bands.textRows {
		return panelBands{}, fmt.Errorf(
			"layout: percent_scale is %d and this panel leaves %d rows for the text with a %d-row bar, and digits at scale %d are %d rows: a scale that does not fit is refused rather than cropped",
			l.percentScale, bands.textRows, rows, l.percentScale, l.percentScale*text.GlyphRows)
	}

	switch {
	case rows == 0:
		// Nothing to place: the text has the whole panel.
	case l.barBand == bandTop:
		bands.textTop = rows
	default:
		bands.barTop = geometry.height - rows
	}

	return bands, nil
}

// panelPicture is one composed panel frame: the frame the renderer decided, the layout that says
// where its parts go, and the moment the rotation is read at.
//
// It is a value and not a func, so that a test can compare two and so that the two callers which
// draw one — the steady path and the preview route — are provably drawing the same thing: they
// build this value and call draw. Nothing here reads a clock; at is given, which is what lets the
// rotation be tested either side of its boundary without waiting fifteen seconds.
type panelPicture struct {
	frame  render.Frame
	layout layout
	at     time.Time
}

// draw is the composed frame as pixels, in the controller's own order: pixel (x, y) is y*width+x.
//
// The panel is the background colour first, so that nothing the frame before left on the light
// survives into this one; then the bar's band, filled to the frame's own fraction of the width;
// then the text of the half the rotation is showing, centred in the band the bar leaves. It is the
// whole panel every time, because a datagram names where its pixels go and a band would leave the
// rows outside it holding whatever was there before.
func (p panelPicture) draw(geometry panel) ([]wled.RGB, error) {
	if !geometry.matrix() {
		return nil, fmt.Errorf("a composed frame needs a panel, and %dx%d is not one", geometry.width, geometry.height)
	}

	bands, err := p.layout.bandsOn(geometry)
	if err != nil {
		return nil, err
	}

	width, height := geometry.width, geometry.height
	picture := make([]wled.RGB, width*height)

	for index := range picture {
		picture[index] = finishedWordBackground
	}

	if bands.barRows > 0 {
		columns := fillColumns(p.frame, geometry)
		unfilled := p.frame.Unfilled()

		for row := bands.barTop; row < bands.barTop+bands.barRows; row++ {
			line := picture[row*width : (row+1)*width]

			for column := range line {
				if column < columns {
					line[column] = p.frame.Colour

					continue
				}

				line[column] = unfilled
			}
		}
	}

	word, scale, ok := p.word(geometry, bands)
	if !ok {
		return picture, nil
	}

	lits, err := p.text(geometry, bands, word, scale)
	if err != nil {
		return nil, fmt.Errorf("the %q in this layout was not drawn: %w", word, err)
	}

	for _, index := range lits {
		picture[index] = p.frame.Colour
	}

	return picture, nil
}

// text is every pixel the text band lights, as indices into the whole panel.
//
// Two arrangements, and the second is the one this build added. **Take turns** is what the module
// has always drawn: one of the two halves centred in the band the bar leaves, chosen by the clock.
// **Side by side** is both at once — the digits on the left at the margin, the job's name on the
// right after them, cut to the columns that are left — and it is the approved Layout mockup's own
// arrangement, down to its numbers.
//
// Side by side falls back to take-turns' single-part picture when there is only one part to draw:
// a panel showing the percentage and no name has nothing to sit beside, and centring it is what
// this module drew before. The refusal for a name with no room is not an error for the same
// reason — the digits are a true picture of the job, and a name cut to two glyphs would not be.
func (p panelPicture) text(geometry panel, bands panelBands, word string, scale int) ([]int, error) {
	if p.layout.rotation != rotationSideBySide {
		return p.centred(geometry, bands, word, scale)
	}

	digits, hasDigits := p.digits()
	jobName := p.name(geometry.width, sideNameScale)
	if !hasDigits || jobName == "" {
		return p.centred(geometry, bands, word, scale)
	}

	return p.beside(geometry, bands, digits, jobName)
}

// centred is the take-turns picture: one word, rasterised in the band the bar leaves and lifted to
// where that band starts on the panel.
//
// The lift is the whole reason this is a function: `internal/text` answers with indices into the
// geometry it was given, which is the text band and not the panel, and a picture that forgot the
// offset would draw its text over the bar and leave the band empty.
func (p panelPicture) centred(geometry panel, bands panelBands, word string, scale int) ([]int, error) {
	lit, err := text.Raster(word, scale, text.Geometry{Width: geometry.width, Height: bands.textRows})
	if err != nil {
		return nil, err
	}

	lifted := make([]int, 0, len(lit))

	for _, index := range lit {
		row, column := index/geometry.width, index%geometry.width
		lifted = append(lifted, (bands.textTop+row)*geometry.width+column)
	}

	return lifted, nil
}

// beside lays the digits and the job's name on one line across the text band, and is the
// side-by-side arrangement as arithmetic.
//
// The split is the mockup's: the digits' box starts at sideMargin columns, the name starts
// sideGap columns after the digits end, and what is left of the panel's width — less sideRight at
// the far edge — is the name's room, cut to whole glyphs at sideNameScale. Both boxes sit on the
// bottom of the taller of them, which is what makes the two baselines agree: the mockup's
// `y+(h-dH)` and `y+(h-nH)`.
//
// The digits are capped at sideDigitsScale (sideDigitsScaleShort on a short band), which is the
// mockup's own `min(pctScale, …)`: side by side the digits give up a scale so that the name has
// columns to be read in. That cap is arithmetic, not a fit, and the fit is bandsOn's business.
//
// A name with fewer than sideNameMin glyphs of room is **not drawn at all**. The mockup draws it
// only when `chars>2`, and this font has no ellipsis: two letters of a slicer's file name identify
// nothing, so the digits stand alone rather than the panel carrying a fragment.
func (p panelPicture) beside(geometry panel, bands panelBands, digits, jobName string) ([]int, error) {
	digitsScale := min(p.layout.digitsScale(bands.textRows), sideDigitsScale)
	if bands.textRows < sideBandForFull {
		digitsScale = min(digitsScale, sideDigitsScaleShort)
	}

	digitsSize, err := text.Size(digits, digitsScale)
	if err != nil {
		return nil, err
	}

	nameSize, err := text.Size(jobName, sideNameScale)
	if err != nil {
		return nil, err
	}

	// The name's room, in columns: what is left of the panel beside the digits' box and the two
	// margins. In pixels, so that a cut lands where the font's own advance says it does.
	nameLeft := sideMargin + digitsSize.Width + sideGap
	room := geometry.width - nameLeft - sideRight

	height := max(digitsSize.Height, nameSize.Height)
	if height > bands.textRows {
		return nil, fmt.Errorf(
			"layout: the digits at scale %d are %d rows and the name is %d, and this panel leaves %d rows for the text: side by side that does not fit, and it is refused rather than cropped",
			digitsScale, digitsSize.Height, nameSize.Height, bands.textRows)
	}

	top := bands.textTop + (bands.textRows-height)/2

	lit, err := stamp(geometry.width, digits, digitsScale, sideMargin, top+(height-digitsSize.Height))
	if err != nil {
		return nil, err
	}

	if glyphsThatFit(room, sideNameScale) < sideNameMin {
		// No room worth a word: the digits stand alone, exactly as the mockup draws them.
		return lit, nil
	}

	cut := fitName(jobName, room, sideNameScale)
	if cut == "" {
		return lit, nil
	}

	name, err := stamp(geometry.width, cut, sideNameScale, nameLeft, top+(height-nameSize.Height))
	if err != nil {
		return nil, err
	}

	return append(lit, name...), nil
}

// stamp is one word drawn at one place on the panel, as indices into the whole panel's buffer.
//
// It rasterises the word in a geometry of exactly the word's own size, so that `internal/text`'s
// centring has nothing to centre — the placement is this function's, and the one thing that
// decides where a glyph's pixels are is still the rasteriser. The result is sorted because the
// indices it is given are, and the panel is painted by walking them.
func stamp(width int, word string, scale, left, top int) ([]int, error) {
	size, err := text.Size(word, scale)
	if err != nil {
		return nil, err
	}

	if left < 0 || top < 0 || left+size.Width > width {
		return nil, fmt.Errorf("layout: %q at a scale of %d does not fit from column %d of a %d-column panel",
			word, scale, left, width)
	}

	lit, err := text.Raster(word, scale, text.Geometry{Width: size.Width, Height: size.Height})
	if err != nil {
		return nil, err
	}

	stamped := make([]int, 0, len(lit))

	for _, index := range lit {
		row, column := index/size.Width, index%size.Width
		stamped = append(stamped, (top+row)*width+left+column)
	}

	return stamped, nil
}

// word is what the text band says at this moment, and at what scale: the percentage on one half of
// the rotation and the job's name on the other.
//
// A picture with only one of them does not rotate at all — a layout that shows the percentage and
// no name would otherwise spend half its life showing nothing, and a panel that blinked empty every
// fifteen seconds would read as a fault. A picture with neither draws no text, which is what a
// bar-only layout is.
//
// The scales differ on purpose and the arithmetic is in their own comments: the digits are two to
// four characters and get percentScaleMax, the name is up to 38 and takes nameScaleMax so that
// more of it fits.
func (p panelPicture) word(geometry panel, bands panelBands) (string, int, bool) {
	digits, hasDigits := p.digits()
	jobName := p.name(geometry.width, nameScale(bands.textRows))

	switch {
	case !hasDigits && jobName == "":
		return "", 0, false
	case !hasDigits:
		return jobName, nameScale(bands.textRows), true
	case jobName == "":
		return digits, p.layout.digitsScale(bands.textRows), true
	}

	if rotationHalf(p.at, p.layout.rotate) == 0 {
		return digits, p.layout.digitsScale(bands.textRows), true
	}

	return jobName, nameScale(bands.textRows), true
}

// halves is both halves of the rotation, in the order they are shown, and only the ones this
// picture actually has. The preview route answers with it, so that a caller who asks for a
// two-minute hold is told what it will see rather than what it would see if he looked right now.
func (p panelPicture) halves(geometry panel, bands panelBands) []string {
	digits, hasDigits := p.digits()
	jobName := p.name(geometry.width, nameScale(bands.textRows))

	shown := make([]string, 0, 2)

	if hasDigits {
		shown = append(shown, digits)
	}

	if jobName != "" {
		shown = append(shown, jobName)
	}

	return shown
}

// digits is the percentage as the font draws it, or nothing when the layout does not show it or the
// frame has no progress this module may present as current.
//
// A percent of -1 is render.Frame's way of saying there is no live reading, and it is not the same
// as 0: a paused print at 0 % has a number and a printer that has said nothing does not. So the
// digits are absent rather than "0%" for a frame that has no percentage.
func (p panelPicture) digits() (string, bool) {
	if !p.layout.percent || p.frame.Percent < 0 {
		return "", false
	}

	return fmt.Sprintf("%d%%", p.frame.Percent), true
}

// name is the job's name as the panel can draw it, or nothing when the layout does not show it, the
// frame carries none, or none of it fits.
func (p panelPicture) name(width, scale int) string {
	if !p.layout.name || p.frame.Name == "" {
		return ""
	}

	return fitName(p.frame.Name, width, scale)
}

// composedEvent is the panel's own picture as an event: the layout drawn from that frame, holding
// the light for holds.
//
// It exists so that the preview of a printing panel goes through the machinery an event already
// has — the establishing body, the once-a-second repeat over DDP, the deadline and the hand-back —
// rather than through a second path that would have to earn each of them again. The steady picture
// a printing panel holds is painted by the same function; what it does not have is a deadline,
// because it lasts as long as the print does.
func composedEvent(picture panelPicture, holds time.Duration) event {
	return event{picture: &picture, holds: holds}
}

// layoutView is a layout as a caller of the routes reads it: the operator's words, one field each,
// with the rotation in whole seconds rather than in a Go duration.
type layoutView struct {
	Bar           bool   `json:"bar"`
	BarBand       string `json:"bar_band"`
	BarRows       int    `json:"bar_rows"`
	Percent       bool   `json:"percent"`
	Name          bool   `json:"name"`
	RotateSeconds int    `json:"rotate_seconds"`
}

// view is this layout as the routes answer with it.
func (l layout) view() layoutView {
	return layoutView{
		Bar:           l.bar,
		BarBand:       l.barBand,
		BarRows:       l.barRows,
		Percent:       l.percent,
		Name:          l.name,
		RotateSeconds: int(l.rotate / time.Second),
	}
}

// percentScale is the scale the digits are drawn at in a text band of that many rows: as large as
// the band allows, and never larger than the finished word.
func percentScale(textRows int) int {
	return min(max(textRows/text.GlyphRows, 1), percentScaleMax)
}

// digitsScale is the scale this layout draws the digits at in a text band of that many rows: the
// scale it pins when it pins one, and otherwise the largest the band allows.
//
// The pinned scale is not checked here: bandsOn has already refused a layout whose scale does not
// fit its panel, which is the one place that knows the geometry.
func (l layout) digitsScale(textRows int) int {
	if l.percentScale > 0 {
		return l.percentScale
	}

	return percentScale(textRows)
}

// nameScale is the scale the job's name is drawn at in a text band of that many rows.
func nameScale(textRows int) int {
	return min(max(textRows/text.GlyphRows, 1), nameScaleMax)
}

// rotationHalf is which half of the rotation a moment falls in: 0 for the percentage, 1 for the
// name.
//
// It is a function of the moment and the interval and of nothing else — no start time is
// remembered, so a module that restarts mid-print does not restart the rotation, and two pictures
// composed at the same moment agree. The slot is the moment's own second over the interval, and a
// moment before 1970 is counted from the epoch in the same direction rather than flipping the
// halves.
func rotationHalf(at time.Time, rotate time.Duration) int {
	seconds := int64(rotate / time.Second)
	if seconds < 1 {
		return 0
	}

	slot := at.Unix() / seconds
	if slot < 0 {
		slot = -slot
	}

	return int(slot % 2)
}

// fitName is a job's name as a panel of that width can draw it at that scale.
//
// Three things happen, in this order, and each one is a decision this build makes rather than an
// accident of the font:
//
//  1. **It is upper-cased.** `internal/text` holds capitals only — it has no lower-case set, and a
//     font that silently folded case would hide a mistake instead of naming it, so the caller that
//     writes a word is the one that upper-cases it. A job name is written by a slicer, not by a
//     person.
//  2. **Every character the font does not hold becomes a separator.** A Prusa file name is
//     `MK4IS_0.4n_0.2mm_PLA_MK4IS_1h52m.gcode`: it carries underscores, which the font does not
//     have, and runs of spaces once they are turned into one. The result reads as
//     `MK4IS 0.4N 0.2MM PLA MK4IS 1H52M.GCODE` — the same words, without a character the panel
//     cannot draw. The alternative, refusing the name, would leave the panel with no name for
//     every real print the operator starts, which is worse than a dropped underscore.
//  3. **It is cut at the last word boundary that fits.** Not mid-word, unless the name's first word
//     is itself longer than the panel; not with an ellipsis, because the font's only punctuation is
//     a full stop and a name does not end in one; and not scrolled, because a scrolling word is a
//     frame rate and this module's whole write budget (§5.7) exists to say it does not stream. The
//     head of a slicer's file name is the part that identifies the job — machine, nozzle, layer,
//     material — and the tail is the slicer's own bookkeeping, which is why the head is what
//     survives.
//
// A name that does not fit at all — a single word wider than the panel — is cut at the last
// character that fits, so the picture still says as much of it as there is room for.
func fitName(name string, width, scale int) string {
	cleaned := drawableName(name)
	if cleaned == "" {
		return ""
	}

	room := glyphsThatFit(width, scale)
	if room < 1 {
		return ""
	}

	drawn := []rune(cleaned)
	if len(drawn) <= room {
		return cleaned
	}

	// Everything that fits, with the separator it may have ended on taken off, and then the question
	// that decides between a cut and a further cut: does the name continue with a space? If it does,
	// what fits is whole words and it stays — "MK4IS 0.4N 0.2MM PLA" is a cut between words even
	// though the character after it is the space before the next one.
	kept := strings.TrimRight(string(drawn[:room]), " ")

	length := len([]rune(kept))
	if length == len(drawn) || drawn[length] == ' ' {
		return kept
	}

	if space := strings.LastIndex(kept, " "); space > 0 {
		return strings.TrimRight(kept[:space], " ")
	}

	return kept
}

// drawableName is a name with only the characters this font holds, upper-cased and with its
// separators collapsed.
func drawableName(name string) string {
	var kept strings.Builder

	separator := false

	for _, character := range strings.ToUpper(name) {
		if character == ' ' || !drawable(character) {
			separator = true

			continue
		}

		if separator && kept.Len() > 0 {
			kept.WriteRune(' ')
		}

		separator = false
		kept.WriteRune(character)
	}

	return kept.String()
}

// drawable reports whether the font holds a glyph for this character, asked of the font itself so
// that the layout cannot disagree with what the rasteriser will accept. A second copy of the
// font's coverage here would be a second answer to the same question, and the one that drifts is
// always the copy.
func drawable(character rune) bool {
	_, err := text.Size(string(character), 1)

	return err == nil
}

// glyphsThatFit is how many glyphs of that scale span a panel of that width, gaps included: n
// glyphs are 5n columns of glyph and n-1 of gap, so scale*(6n-1) pixels.
func glyphsThatFit(width, scale int) int {
	if width <= 0 || scale < 1 {
		return 0
	}

	room := width / scale
	if room < text.GlyphColumns {
		return 0
	}

	fit := (room + 1) / 6

	for fit > 0 && fit*(text.GlyphColumns+text.GlyphGap)*scale-scale > width {
		fit--
	}

	return fit
}

// aboutAJob reports whether a frame is a live job's picture: the four reasons render gives a frame
// that has a job on the bed.
//
// Everything else — idle, ready, finished, stopped, an error, an unknown word, a printer that has
// gone quiet — is the panel's resting colour, and the layout does not touch it. That is deliberate
// on two counts: the resting picture is the one the operator approved and the one he reads from
// across a room, and the composed picture is the only one that has to be held live over DDP, so
// keeping it to the jobs in progress keeps the controller on its own segments for the rest of the
// day.
func aboutAJob(frame render.Frame) bool {
	switch frame.Reason {
	case render.ReasonProgress, render.ReasonStarting, render.ReasonPausedFrozen, render.ReasonProgressUnknown:
		return true
	}

	return false
}

// previewFrame is the frame a printing printer at this progress would have decided: the printing
// green, the job's own fraction of the panel, and the percentage and name the layout draws from it.
//
// It exists because the preview route is given a number rather than a snapshot, and the picture it
// draws has to be the picture a printing panel draws — which means the same frame, from the same
// rules. The fill is floored, as render.lit floors it and for the reason Q-17 gives there: a bar
// never claims more of the job than the printer reported.
//
// A percent of -1 is a job whose progress this module may not present as current: the picture then
// carries the name and no digits, exactly as a printing frame with no live progress does.
func previewFrame(percent int, jobName string, total int) render.Frame {
	frame := render.Frame{
		Colour:  render.ColourPrinting,
		Total:   total,
		Percent: percent,
		Name:    jobName,
		Reason:  render.ReasonProgress,
	}

	if percent < 0 {
		frame.Reason = render.ReasonProgressUnknown

		return frame
	}

	if total <= 0 {
		return frame
	}

	clamped := min(max(percent, 0), 100)

	switch {
	case clamped <= 0:
		frame.Lit = 0
	case clamped >= 100:
		frame.Lit = total
	default:
		frame.Lit = int(math.Floor(float64(clamped) / 100 * float64(total)))
	}

	return frame
}
