// The composer, as the store drives it: the tests for the per-binding layout, the pinned percentage
// scale, and side by side.
//
// Three things are being pinned, and they are different questions:
//
//  1. **Which layout the painter composes with.** The store's row for one binding, and the `layout`
//     setting when the binding has none — which is the migration source, so an install that has never
//     opened the Layout screen keeps the picture it has been drawing, to the byte.
//  2. **The percentage scale is a scale.** 3, 4 and 5 draw glyphs of three heights, and a scale that
//     does not fit the band the bar leaves is refused where every other fit question is refused.
//  3. **Side by side.** Digits on the left, the job's name on the right, the name cut to the room the
//     digits leave — and nothing drawn at all where the name would be two glyphs long.
package main

import (
	"testing"
	"time"

	"github.com/nothingmn/wled-printer-status/internal/render"
	"github.com/nothingmn/wled-printer-status/internal/text"
	"github.com/nothingmn/wled-printer-status/internal/wled"
)

// composedAt is the instant these tests draw at: a fixed one, so the take-turns half is a decision
// rather than whatever clock the binary started on.
var composedAt = time.Date(2026, time.November, 19, 21, 0, 0, 0, time.UTC)

// composingFrame is a printing job at 41 % with the operator's own file name: the frame the layout
// has always been tested with, and the one whose digits and name are both non-empty.
func composingFrame() render.Frame {
	return render.Frame{
		Colour:  render.ColourPrinting,
		Lit:     6717,
		Total:   16384,
		Percent: 41,
		Name:    "MK4IS_0.4n_0.2mm_PLA_MK4IS_1h52m.gcode",
		Reason:  render.ReasonProgress,
	}
}

// drawWith is one composed picture, and the rows and columns it lights.
func drawWith(t *testing.T, one layout, frame render.Frame) []wled.RGB {
	t.Helper()

	picture := panelPicture{frame: frame, layout: one, at: composedAt}

	pixels, err := picture.draw(livePanel)
	if err != nil {
		t.Fatalf("the picture was not drawn: %v", err)
	}

	return pixels
}

// lit is every lit pixel of one picture inside one band of rows, as pairs of row and column, in order.
//
// The band matters: a picture is a bar and a text band, and a test about where the *text* sits that
// counted the bar's own rows would pass on the bar alone. Every caller here asks about the band the
// bar leaves.
func lit(pixels []wled.RGB, width int, band panelBands) [][2]int {
	litPixels := make([][2]int, 0, len(pixels))

	for row := band.textTop; row < band.textTop+band.textRows; row++ {
		for column := 0; column < width; column++ {
			if pixels[row*width+column] != finishedWordBackground {
				litPixels = append(litPixels, [2]int{row, column})
			}
		}
	}

	return litPixels
}

// rowsOf is every row a set of lit pixels touches, in order and without repeats.
func rowsTouched(pixels [][2]int) []int {
	seen := make(map[int]bool, len(pixels))
	rows := make([]int, 0, len(pixels))

	for _, one := range pixels {
		if seen[one[0]] {
			continue
		}

		seen[one[0]] = true
		rows = append(rows, one[0])
	}

	return rows
}

// columnsOf is every column a set of lit pixels touches, in order and without repeats.
func columnsTouched(pixels [][2]int) []int {
	seen := make(map[int]bool, len(pixels))
	columns := make([]int, 0, len(pixels))

	for _, one := range pixels {
		if seen[one[1]] {
			continue
		}

		seen[one[1]] = true
		columns = append(columns, one[1])
	}

	return columns
}

// bandsOf is where one layout puts its bar and its text on the operator's panel.
func bandsOf(t *testing.T, one layout) panelBands {
	t.Helper()

	bands, err := one.bandsOn(livePanel)
	if err != nil {
		t.Fatalf("the layout does not fit the panel it is pointed at: %v", err)
	}

	return bands
}

// TestTheStoredLayoutIsWhatTheComposerReads is the first question, and the fallback beside it: the
// store's row wins for the binding that has one, and a binding with no row composes with the setting.
func TestTheStoredLayoutIsWhatTheComposerReads(t *testing.T) {
	stored := layout{
		bar: true, barBand: bandTop, barRows: 6,
		percent: true, percentScale: 3, name: false,
		rotation: rotationTakeTurns, rotate: 20 * time.Second,
	}

	pen := newWriter(newPanelStrip(), 128, quiet)
	pen.layout = defaultLayout()
	pen.store = &handTable{shape: &stored}

	if got := pen.composing(); got != stored {
		t.Fatalf("the painter composes with %+v, want the store's row %+v", got, stored)
	}

	// The other way round: no row of its own, so the setting composes — and it is the setting's value,
	// not a copy of the built-in default.
	setting := layout{bar: false, percent: true, percentScale: 4, name: true, rotate: 5 * time.Second}

	pen.layout = setting
	pen.store = &handTable{}

	if got := pen.composing(); got != setting {
		t.Fatalf("a binding with no stored layout composes with %+v, want the setting's %+v", got, setting)
	}

	// And no store at all is the same answer, which is every writer built by a test.
	pen.store = nil

	if got := pen.composing(); got != setting {
		t.Fatalf("a writer with no store composes with %+v, want the setting's %+v", got, setting)
	}
}

// TestABindingWithNoStoredLayoutComposesExactlyAsItDidBefore is the regression that matters most: the
// panel on the wall is composed by this path right now, and adding the store must not change one pixel
// of it.
func TestABindingWithNoStoredLayoutComposesExactlyAsItDidBefore(t *testing.T) {
	before := defaultLayout()

	pen := newWriter(newPanelStrip(), 128, quiet)
	pen.layout = before
	pen.store = &handTable{}

	if got := pen.composing(); got != before {
		t.Fatalf("the picture is composed with %+v, want the setting's %+v", got, before)
	}

	// The picture itself, through both paths: the store that has no layout for this binding, and no
	// store at all. Byte for byte the same, which is what "exactly as it did" means.
	withStore := drawWith(t, pen.composing(), composingFrame())
	without := drawWith(t, before, composingFrame())

	if len(withStore) != len(without) {
		t.Fatalf("the two pictures are %d and %d pixels", len(withStore), len(without))
	}

	for index := range withStore {
		if withStore[index] != without[index] {
			t.Fatalf("pixel %d differs: %+v with the store, %+v without", index, withStore[index], without[index])
		}
	}
}

// TestAStoredLayoutComposesWithTheBandOnTopAndAtTheBottom is the band, both ways, drawn from a layout
// the store holds: the bar's rows are the frame's colour up to its own fraction of the width, and the
// text has the rest of the panel.
func TestAStoredLayoutComposesWithTheBandOnTopAndAtTheBottom(t *testing.T) {
	for _, band := range []string{bandTop, bandBottom} {
		t.Run(band, func(t *testing.T) {
			stored := layout{
				bar: true, barBand: band, barRows: 8,
				percent: true, percentScale: 4, name: false,
				rotation: rotationTakeTurns, rotate: 15 * time.Second,
			}

			strip := newPanelStrip()

			pen := newWriter(strip, 128, quiet)
			pen.setPanel(strip.info)
			pen.store = &handTable{shape: &stored}

			frame := composingFrame()

			if !pen.drawablePanel(frame) {
				t.Fatal("the store's layout draws, and the panel was not composed with it")
			}

			pixels := drawWith(t, pen.composing(), frame)

			barTop := 0
			if band == bandBottom {
				barTop = livePanel.height - stored.barRows
			}

			// The bar's own rows: the frame's colour across the filled columns, and the unfilled colour
			// past them. Nothing of the text is in those rows.
			columns := fillColumns(frame, livePanel)

			for row := barTop; row < barTop+stored.barRows; row++ {
				if got := pixels[row*livePanel.width]; got != frame.Colour {
					t.Fatalf("row %d starts with %+v, want the bar's colour %+v", row, got, frame.Colour)
				}

				if got := pixels[row*livePanel.width+columns]; got != frame.Unfilled() {
					t.Fatalf("row %d at column %d is %+v, want the unfilled colour %+v",
						row, columns, got, frame.Unfilled())
				}
			}

			// The text is in the band the bar leaves and nowhere else, and the bar is in its own rows:
			// a picture whose parts overlapped would pass every colour check above and still be wrong.
			bands := bandsOf(t, stored)

			if band == bandTop {
				if bands.barTop != 0 || bands.textTop != stored.barRows {
					t.Fatalf("the top band is at rows %d..%d with the text from %d",
						bands.barTop, bands.barTop+bands.barRows, bands.textTop)
				}
			} else if bands.barTop != livePanel.height-stored.barRows || bands.textTop != 0 {
				t.Fatalf("the bottom band is at rows %d..%d with the text from %d",
					bands.barTop, bands.barTop+bands.barRows, bands.textTop)
			}

			// The digits are in the band the bar leaves, centred in it, and in no row of the bar: the
			// band's own position is what this test is about, so the text has to move with it.
			digitsHeight := text.GlyphRows * stored.percentScale
			wantTop := bands.textTop + (bands.textRows-digitsHeight)/2

			drawn := lit(pixels, livePanel.width, bands)
			if len(drawn) == 0 {
				t.Fatalf("the %s band carries no digits at all", band)
			}

			for _, row := range rowsTouched(drawn) {
				if row < wantTop || row >= wantTop+digitsHeight {
					t.Fatalf("row %d carries a digit, and the digits occupy rows %d..%d",
						row, wantTop, wantTop+digitsHeight)
				}
			}
		})
	}
}

// TestPercentScaleThreeFourAndFiveAllDraw is the scale being a scale: the digits are drawn at three
// heights, seven rows of glyph per scale, and the three pictures are different pictures.
func TestPercentScaleThreeFourAndFiveAllDraw(t *testing.T) {
	seen := make(map[int]int, 3)

	for _, scale := range []int{3, 4, 5} {
		one := layout{
			bar: true, barBand: bandBottom, barRows: defaultBarRows,
			percent: true, percentScale: scale, name: false,
			rotation: rotationTakeTurns, rotate: 15 * time.Second,
		}

		bands := bandsOf(t, one)
		pixels := drawWith(t, one, composingFrame())
		drawn := lit(pixels, livePanel.width, bands)

		// The exact pixels the rasteriser draws for the same digits in the same band, offset into the
		// band: three scales are three heights, and the picture is the centred word rather than the
		// digits at a scale nobody asked for.
		want, err := text.Raster("41%", scale, text.Geometry{Width: livePanel.width, Height: bands.textRows})
		if err != nil {
			t.Fatalf("rasterise 41%% at scale %d: %v", scale, err)
		}

		if len(drawn) != len(want) {
			t.Fatalf("the digits at scale %d light %d pixels, want the rasteriser's %d", scale, len(drawn), len(want))
		}

		for _, index := range want {
			row, column := index/livePanel.width, index%livePanel.width

			if got := pixels[(bands.textTop+row)*livePanel.width+column]; got != render.ColourPrinting {
				t.Fatalf("the pixel at row %d column %d is %+v at scale %d, want the digits' colour",
					row, column, got, scale)
			}
		}

		seen[scale] = len(want)
	}

	if seen[3] == seen[4] || seen[4] == seen[5] || seen[3] == seen[5] {
		t.Fatalf("the three scales drew the same width of digits: %v", seen)
	}
}

// TestAPercentScaleThatDoesNotFitTheBandIsRefused is the fit rule §2.5 states, in the place the layout
// already refuses one: a pinned scale whose digits are taller than the band the bar leaves is an error,
// not a crop. It is the mockup's own example — a thirty-row bar on a sixty-four-row panel.
func TestAPercentScaleThatDoesNotFitTheBandIsRefused(t *testing.T) {
	one := layout{
		bar: true, barBand: bandBottom, barRows: 30,
		percent: true, percentScale: 5, name: false,
		rotation: rotationTakeTurns, rotate: 15 * time.Second,
	}

	if _, err := one.bandsOn(livePanel); err == nil {
		t.Fatal("a 30-row bar and scale-5 digits were accepted on a 64-row panel, want the refusal")
	}

	picture := panelPicture{frame: composingFrame(), layout: one, at: composedAt}

	if _, err := picture.draw(livePanel); err == nil {
		t.Fatal("the picture was drawn with 35 rows of digits in a 34-row band")
	}

	// And scale 4 in the same band does fit, which is what makes the refusal about the scale rather
	// than about the bar.
	one.percentScale = 4

	fits := panelPicture{frame: composingFrame(), layout: one, at: composedAt}

	if _, err := fits.draw(livePanel); err != nil {
		t.Fatalf("scale-4 digits were refused in a 34-row band: %v", err)
	}
}

// sideBySide is the arrangement from the approved mockup, expressed as the layout the store holds.
func sideBySide(scale int) layout {
	return layout{
		bar: true, barBand: bandBottom, barRows: defaultBarRows,
		percent: true, percentScale: scale, name: true,
		rotation: rotationSideBySide, rotate: 15 * time.Second,
	}
}

// TestSideBySidePutsTheDigitsAndTheNameOnOneLine is the split: the digits start at the mockup's left
// margin, the name starts one gap after them, and the name is cut to the columns that are left.
func TestSideBySidePutsTheDigitsAndTheNameOnOneLine(t *testing.T) {
	one := sideBySide(5)

	bands := bandsOf(t, one)
	pixels := drawWith(t, one, composingFrame())
	drawn := lit(pixels, livePanel.width, bands)

	// The digits give up a scale side by side — the mockup's own cap — so the box the name is measured
	// against is theirs at scale 4, not 5.
	digits, err := text.Size("41%", sideDigitsScale)
	if err != nil {
		t.Fatalf("size the digits: %v", err)
	}

	nameLeft := sideMargin + digits.Width + sideGap
	room := livePanel.width - nameLeft - sideRight

	columns := columnsTouched(drawn)
	if len(columns) == 0 {
		t.Fatal("the picture is empty")
	}

	// The digits begin inside their own box, which starts at the mockup's margin: a glyph's leftmost
	// pixel is not always the box's first column, so the check is the box and not the pixel.
	if got := columns[0]; got < sideMargin || got >= sideMargin+text.GlyphColumns*sideDigitsScale {
		t.Fatalf("the leftmost lit column is %d, and the digits' box starts at %d", got, sideMargin)
	}

	// The gap between the two boxes is empty, which is what makes them two things on one line.
	for _, column := range columns {
		if column >= sideMargin+digits.Width && column < nameLeft {
			t.Fatalf("column %d is in the gap between the digits (%d) and the name (%d)",
				column, sideMargin+digits.Width, nameLeft)
		}
	}

	// Nothing is drawn in the right margin, and nothing is drawn past the room the name was cut to.
	if last := columns[len(columns)-1]; last >= livePanel.width-sideRight {
		t.Fatalf("the picture reaches column %d, and the mockup leaves the last %d columns clear",
			last, sideRight)
	}

	inNameBox := 0

	for _, column := range columns {
		if column >= nameLeft {
			inNameBox++
		}
	}

	if inNameBox == 0 {
		t.Fatalf("nothing was drawn in the name's box, which starts at column %d", nameLeft)
	}

	if inNameBox > room {
		t.Fatalf("the name covers %d columns and it has %d", inNameBox, room)
	}

	firstInName := 0

	for _, column := range columns {
		if column >= nameLeft {
			firstInName = column

			break
		}
	}

	if firstInName >= nameLeft+text.GlyphColumns*sideNameScale {
		t.Fatalf("the name begins at column %d, and its box starts at %d", firstInName, nameLeft)
	}

	// Both parts are on one line: every lit pixel is inside the text band the bar leaves, and the band
	// is one band and not two.
	if got := len(rowsTouched(drawn)); got != text.GlyphRows*sideNameScale && got != text.GlyphRows*sideDigitsScale {
		t.Fatalf("the line is %d rows tall, want the height of one of the two boxes", got)
	}

	// The name really is cut: the whole of it does not fit, and what is drawn is the head of it.
	whole, err := text.Size(drawableName(composingFrame().Name), sideNameScale)
	if err != nil {
		t.Fatalf("size the whole name: %v", err)
	}

	if whole.Width <= room {
		t.Fatalf("the whole name is %d columns and the room is %d, so this test proves nothing", whole.Width, room)
	}
}

// TestSideBySideDrawsNoNameWhereThereIsNoRoom is the mockup's own rule for a narrow panel: fewer than
// three glyphs of room and the name is not drawn at all — not cut to two glyphs, and not ellipsised,
// because this font has no ellipsis and two letters of a slicer's file name identify nothing.
func TestSideBySideDrawsNoNameWhereThereIsNoRoom(t *testing.T) {
	narrow := panel{width: 110, height: 64}
	one := sideBySide(3)

	picture := panelPicture{frame: composingFrame(), layout: one, at: composedAt}

	pixels, err := picture.draw(narrow)
	if err != nil {
		t.Fatalf("the picture was not drawn: %v", err)
	}

	digits, err := text.Size("41%", 3)
	if err != nil {
		t.Fatalf("size the digits: %v", err)
	}

	nameLeft := sideMargin + digits.Width + sideGap

	if room := narrow.width - nameLeft - sideRight; glyphsThatFit(room, sideNameScale) >= sideNameMin {
		t.Fatalf("the narrow panel has room for a name, so this test proves nothing")
	}

	// Every row of the panel, the bar's included: the name's box is empty in all of it.
	for row := 0; row < narrow.height; row++ {
		for column := nameLeft; column < narrow.width; column++ {
			if got := pixels[row*narrow.width+column]; got != finishedWordBackground {
				t.Fatalf("a pixel was drawn at row %d column %d, in the name's box, and there is no room for a name",
					row, column)
			}
		}
	}
}

// TestTakeTurnsIsUnchangedForTheSameInput is the other half of the same rule: the arrangement that
// ships draws exactly what it drew before — one half centred in the band — and a layout that names the
// rotation and one that leaves it out are the same picture.
func TestTakeTurnsIsUnchangedForTheSameInput(t *testing.T) {
	named := layout{
		bar: true, barBand: bandBottom, barRows: defaultBarRows,
		percent: true, name: true, rotation: rotationTakeTurns, rotate: 15 * time.Second,
	}

	unnamed := named
	unnamed.rotation = ""

	frame := composingFrame()

	if one, two := drawWith(t, named, frame), drawWith(t, unnamed, frame); len(one) != len(two) {
		t.Fatal("the two pictures are different lengths")
	} else {
		for index := range one {
			if one[index] != two[index] {
				t.Fatalf("naming the rotation changed pixel %d", index)
			}
		}
	}

	// And what it draws is one half, centred in the band the bar leaves: the digits on one half of the
	// clock and the name on the other, which is the rule this module has always had.
	bands, err := named.bandsOn(livePanel)
	if err != nil {
		t.Fatalf("bands: %v", err)
	}

	for _, at := range []time.Time{composedAt, composedAt.Add(15 * time.Second)} {
		picture := panelPicture{frame: frame, layout: named, at: at}

		word, scale, ok := picture.word(livePanel, bands)
		if !ok {
			t.Fatal("a printing frame with a percentage and a name draws neither")
		}

		want, err := text.Raster(word, scale, text.Geometry{Width: livePanel.width, Height: bands.textRows})
		if err != nil {
			t.Fatalf("rasterise %q: %v", word, err)
		}

		got, err := picture.draw(livePanel)
		if err != nil {
			t.Fatalf("draw: %v", err)
		}

		if drawn := lit(got, livePanel.width, bands); len(drawn) != len(want) {
			t.Fatalf("the %q half lights %d pixels in the text band, want the rasteriser's %d",
				word, len(drawn), len(want))
		}
	}
}

// TestTheDocumentIsWhatThePainterReadsForMomentsAndLayouts is the one-reader claim, proved against a
// real document rather than a hand-written table: a moment row and a layout row written into this
// module's own store are what the painter finds, a row about another light is not, and a binding with
// no row gets the code's defaults.
func TestTheDocumentIsWhatThePainterReadsForMomentsAndLayouts(t *testing.T) {
	tables := storeHolding(t, storedConfig{
		Moments: []storedMoment{{
			Light: "10.0.0.136", Printer: "printer-a", Moment: momentPrintFinished,
			momentEntry: momentEntry{Enabled: true, Shows: showsWord, Which: "DONE", For: "30s"},
		}},
		Layouts: []storedLayout{{
			Printer: "printer-a", BarBand: ptr("top"), BarRows: ptr(6), PercentScale: ptr(3),
			Rotation: ptr(rotationSideBySide),
		}},
	})

	painter := &storeLights{tables: tables, printer: "printer-a", light: "10.0.0.136", logging: quiet}

	entry, stored := painter.moment(momentPrintFinished)
	if !stored || entry.Shows != showsWord || entry.Which != "DONE" || entry.For != "30s" {
		t.Fatalf("the document's finished moment reads as %+v (stored %v)", entry, stored)
	}

	// A moment the document says nothing about is the code's default, which is the finished word and
	// nothing for the rest.
	if one, held := painter.moment(momentPrintStarted); held || one.Enabled {
		t.Fatalf("a moment with no row reads as %+v (stored %v), want the default off", one, held)
	}

	if one, held := painter.moment(momentPrintFinished); one.Which != "DONE" || !held {
		t.Fatalf("the row is not the one the painter found: %+v", one)
	}

	// The layout: the row's own fields, with the ones it leaves out resolved against the defaults.
	shape, held := painter.layout()
	if !held {
		t.Fatal("the document holds a layout for this binding and the painter found none")
	}

	if shape.barBand != bandTop || shape.barRows != 6 || shape.percentScale != 3 || shape.rotation != rotationSideBySide {
		t.Fatalf("the layout resolves to %+v, want the row's band, rows, scale and rotation", shape)
	}

	if !shape.bar || !shape.percent || !shape.name {
		t.Fatalf("the fields the row left out did not resolve to the defaults: %+v", shape)
	}

	// Another light's row is not this binding's: the key is the (light, printer) pair, and the same
	// printer on a second light carries its own set (docs/product-requirements.md §2.3).
	other := &storeLights{tables: tables, printer: "printer-a", light: "10.0.0.253", logging: quiet}

	if one, held := other.moment(momentPrintFinished); held || one.Which == "DONE" {
		t.Fatalf("a moment row written for one light was read for another: %+v (stored %v)", one, held)
	}

	// The layout table is keyed by the printer rather than the pair, and that is this build's own
	// shape: /bindings/{id}/layout addresses a binding by the printer's device id, and bindings.check
	// refuses one printer on two bindings ("one printer is shown by one binding"), so a printer names
	// exactly one binding and the row is found from a second binding object naming the same printer,
	// whatever light it carries. Multi-room — the same printer on two lights, which §2.2 allows — is
	// the change that would have to key this table on the pair, and this assertion is where that
	// shows up: when multi-room lands, the row below is a defect rather than a fact.
	if _, held := other.layout(); !held {
		t.Fatal("the layout row for printer-a was not found from a second binding object naming the same printer")
	}

	// And a binding the document says nothing about answers the setting, because the reader says so.
	unknown := &storeLights{tables: tables, printer: "printer-b", light: "10.0.0.136", logging: quiet}

	if _, held := unknown.layout(); held {
		t.Fatal("a binding with no row of its own answered as though it had one")
	}
}
