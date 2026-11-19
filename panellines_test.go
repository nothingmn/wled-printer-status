// The panel's seven optional lines: the model, the fit, the drawing and the words.
//
// Four questions, and each has its own failure mode:
//
//  1. **A panel with none of them composes exactly as it did.** This is the regression test that
//     matters most — every one of these keys is off by default, and an install that never opens the
//     Layout screen must keep the picture it has been drawing to the byte.
//  2. **The fit is arithmetic and it refuses.** A 256x64 panel with a twelve-row bar leaves 52 rows;
//     the digits at scale 5 take 35 and two lines of seven take 15, so two fit and three do not. The
//     refusal names the line that did not and says what would make room for it.
//  3. **The words are these words.** `42M`, `ENDS 11:12`, `NOZ 221/220 BED 60`, `1H12M`, `PLA`,
//     `HERMES MK4`, `Z 8.6MM` — pinned as strings, because a panel's text is the product here.
//  4. **A line with no reading is not drawn.** Nothing here invents a placeholder: a printer that
//     has not said how long is left leaves that line blank and the rows where they were.
package main

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nothingmn/wled-printer-status/internal/render"
	"github.com/nothingmn/wled-printer-status/internal/text"
)

// panelZone is the zone the operator's instance names, and the one the ETA line is written in.
func panelZone(t *testing.T) *time.Location {
	t.Helper()

	zone, err := time.LoadLocation("America/Denver")
	if err != nil {
		t.Fatalf("load America/Denver: %v", err)
	}

	return zone
}

// withLines is the layout a binding composes with when nobody has laid it out — the stored default,
// which pins the percentage at scale 5 — with some of the seven switched on, in the operator's order.
func withLines(lines ...int) layout {
	one := defaultBindingLayout().shape()

	for _, line := range lines {
		switch line {
		case lineRemaining:
			one.remaining = true
		case lineEnds:
			one.ends = true
		case lineTemperatures:
			one.temperatures = true
		case lineElapsed:
			one.elapsed = true
		case lineMaterial:
			one.material = true
		case linePrinter:
			one.printer = true
		case lineLayer:
			one.layer = true
		}
	}

	return one
}

// panelFactsFor is one printing job's readings: the frame the panel draws from, and the facts its
// optional lines draw from.
func frameWithFacts() render.Frame {
	frame := composingFrame()
	frame.Panel = render.PanelFacts{
		RemainingSeconds: 2520, // 42m
		PrintingSeconds:  4320, // 1h12m
		Nozzle:           render.Degrees{Now: 221.4, Target: 220, Reported: true},
		Bed:              render.Degrees{Now: 60.2, Target: 60, Reported: true},
		Material:         "PLA",
		Printer:          "Hermes MK4",
		HeightMM:         8.6,
		HeightReported:   true,
	}

	return frame
}

// TestAPanelWithNoneOfTheSevenComposesExactlyAsItDid is the regression this change is judged by.
//
// Every one of the seven is off in the default layout, so the picture is the picture this module drew
// before they existed: the same bytes as the default layout's, and a primary band that is the whole
// text band. The picture's own shape is pinned byte for byte in layout_test.go and composer_test.go,
// which pass unchanged; what this adds is the statement that switching nothing on changes nothing.
func TestAPanelWithNoneOfTheSevenComposesExactlyAsItDid(t *testing.T) {
	frame := frameWithFacts()

	// Facts present and no line switched on: the picture must ignore them entirely.
	before := drawWith(t, defaultLayout(), frame)
	after := drawWith(t, withLines(), frame)

	if !slices.Equal(before, after) {
		t.Fatal("a layout with the seven lines off drew a different picture from the default layout")
	}

	bands := bandsOf(t, withLines())
	if bands.primaryRows != bands.textRows || len(bands.lines) != 0 {
		t.Fatalf("a layout with no lines reserves %d of its %d text rows and %d lines, want all of them and none",
			bands.primaryRows, bands.textRows, len(bands.lines))
	}

	// And the default itself carries none of them, which is what "off unless somebody switches it on"
	// means at the model.
	for index := 0; index < panelLineCount; index++ {
		if defaultLayout().lineOn(index) {
			t.Fatalf("%s is switched on in the default layout", lineKey(index))
		}
	}
}

// TestTheLinesFitInTheOperatorsOrderAndTheRestIsRefused is the arithmetic, on the operator's own
// panel: 64 rows, a twelve-row bar, and the digits at scale 5 taking 35 of the 52 that are left.
//
// Two lines fit (7 + 1 + 7 = 15) and three do not (23), and the refusal says which line did not fit,
// how many did, and the two settings that would make room for it.
func TestTheLinesFitInTheOperatorsOrderAndTheRestIsRefused(t *testing.T) {
	two := withLines(lineRemaining, lineMaterial)

	bands := bandsOf(t, two)

	if len(bands.lines) != 2 {
		t.Fatalf("two lines on fitted %d, want 2", len(bands.lines))
	}

	if bands.primaryRows != 52-15 {
		t.Fatalf("two lines left the picture's own row %d of 52 rows, want 37", bands.primaryRows)
	}

	if bands.lines[0] != lineRemaining || bands.lines[1] != lineMaterial {
		t.Fatalf("the lines are %v, want the time remaining then the material", bands.lines)
	}

	// Three is one too many, and the sentence has to say so in a way somebody can act on.
	three := withLines(lineRemaining, lineEnds, lineTemperatures)

	_, err := three.bandsOn(livePanel)
	if err == nil {
		t.Fatal("three lines fitted a panel that holds two, and the digits would be cropped")
	}

	for _, want := range []string{
		"the temperatures",      // the line that did not fit
		"time remaining",        // one that did
		"2 of the 3 fit",        // how many
		"the digits at scale 5", // what took the room
		"percent_scale of 3",    // a setting that would make room
		"take 35 of them",       // the number behind the verdict
		"refused rather than cropped",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the refusal does not say %q: %v", want, err)
		}
	}

	// The bar is the other way out, and the sentence names the one that has been tried — switching it
	// off entirely, because even a one-row bar at scale 5 leaves 40 rows for 23 of lines and 35 of
	// digits, which is still three rows short.
	if !strings.Contains(err.Error(), "Switching the bar off would take all 3") {
		t.Fatalf("the refusal names no bar that would fit the three lines: %v", err)
	}

	// Four of the seven at scale 3 fit a default panel: 21 rows of digits and 31 of lines is 52.
	four := withLines(lineRemaining, lineEnds, lineTemperatures, lineMaterial)
	four.percentScale = minPercentScale

	fourBands, err := four.bandsOn(livePanel)
	if err != nil {
		t.Fatalf("four lines at scale 3 were refused: %v", err)
	}

	if len(fourBands.lines) != 4 || fourBands.primaryRows != 52-31 {
		t.Fatalf("four lines at scale 3 hold %d lines over a %d-row picture, want 4 over 21",
			len(fourBands.lines), fourBands.primaryRows)
	}
}

// TestTheLinesAreDrawnUnderTheDigitsAtTheBottomOfTheBand pins where they land, in rows worked out by
// hand: two lines take 15 rows at the bottom of the 52 the bar leaves, so the first starts at row 37
// and the second at row 45, and each is centred across the 256 columns at the font's own scale.
func TestTheLinesAreDrawnUnderTheDigitsAtTheBottomOfTheBand(t *testing.T) {
	picture := panelPicture{
		frame:  frameWithFacts(),
		layout: withLines(lineRemaining, lineMaterial),
		at:     composedAt,
	}

	pixels, err := picture.draw(livePanel)
	if err != nil {
		t.Fatalf("draw the composed panel: %v", err)
	}

	bands := bandsOf(t, picture.layout)

	// The stack is the bottom 15 rows of the band the bar leaves: rows 37..43 are the first line, 44
	// is the gap, and 45..51 are the second.
	wanted := map[string]int{"42M": 37, "PLA": 45}

	for word, top := range wanted {
		size, err := text.Size(word, panelLineScale)
		if err != nil {
			t.Fatalf("size %q: %v", word, err)
		}

		left := (livePanel.width - size.Width) / 2

		lit, err := text.Raster(word, panelLineScale, text.Geometry{Width: size.Width, Height: size.Height})
		if err != nil {
			t.Fatalf("rasterise %q: %v", word, err)
		}

		for _, index := range lit {
			row, column := top+index/size.Width, left+index%size.Width

			if got := pixels[row*livePanel.width+column]; got != render.ColourPrinting {
				t.Fatalf("%q: row %d column %d is %+v, want the frame's colour", word, row, column, got)
			}
		}
	}

	// Nothing is lit in the one blank row between them: the gap is a gap.
	for column := range livePanel.width {
		if got := pixels[44*livePanel.width+column]; got != finishedWordBackground {
			t.Fatalf("the row between the two lines is lit at column %d: %+v", column, got)
		}
	}

	// And the digits are in the rows above the lines, not among them: the picture's own row is
	// centred in what the lines leave.
	digits, err := text.Raster("41%", 5, text.Geometry{Width: livePanel.width, Height: bands.primaryRows})
	if err != nil {
		t.Fatalf("rasterise the digits: %v", err)
	}

	for _, index := range digits {
		row, column := index/livePanel.width, index%livePanel.width

		if got := pixels[row*livePanel.width+column]; got != render.ColourPrinting {
			t.Fatalf("the digits are missing at row %d column %d: %+v", row, column, got)
		}
	}

	// The digits are 35 rows centred in the 37 the lines leave, so they occupy rows 1..35 and the
	// stack starts at 37: the two never touch, which is what reserving the rows buys.
	place, err := text.Place("41%", 5, text.Geometry{Width: livePanel.width, Height: bands.primaryRows})
	if err != nil {
		t.Fatalf("place the digits: %v", err)
	}

	if bottom := place.Top + place.Height; bottom != 36 {
		t.Fatalf("the digits end on row %d and the lines start on row 37", bottom)
	}
}

// TestALineWithNoReadingIsNotDrawnAndItsRowStaysReserved is the rule about silence, one level down: a
// printer that has not said how long is left leaves that line blank — and the rows where they were,
// because a picture whose digits changed size as readings came and went would be a panel that
// twitched.
func TestALineWithNoReadingIsNotDrawnAndItsRowStaysReserved(t *testing.T) {
	silent := composingFrame()
	silent.Panel = render.NoPanelFacts() // nothing was reported
	silent.Panel.Printer = "Hermes MK4"

	picture := panelPicture{
		frame:  silent,
		layout: withLines(lineRemaining, lineMaterial),
		at:     composedAt,
	}

	pixels, err := picture.draw(livePanel)
	if err != nil {
		t.Fatalf("draw a panel whose lines have no readings: %v", err)
	}

	bands := bandsOf(t, picture.layout)

	// The rows are still reserved, so the digits are drawn in 37 rows and not 52.
	if bands.primaryRows != 37 {
		t.Fatalf("the picture's own row has %d rows, want the 37 the lines reserve", bands.primaryRows)
	}

	for row := bands.textTop + bands.primaryRows; row < bands.textTop+bands.textRows; row++ {
		for column := range livePanel.width {
			if got := pixels[row*livePanel.width+column]; got != finishedWordBackground {
				t.Fatalf("row %d column %d is lit, and neither line has a reading: %+v", row, column, got)
			}
		}
	}
}

// TestTheWordsTheLinesSay is the text itself, one row per line, because the panel's words are the
// product and a format that drifted would be a picture nobody asked for.
func TestTheWordsTheLinesSay(t *testing.T) {
	facts := frameWithFacts()
	zone := panelZone(t)

	// 21:00 in Denver on 2026-11-19 is 04:00 UTC the next day, so 42 minutes later is 21:42 there.
	at := time.Date(2026, time.November, 19, 21, 0, 0, 0, zone)

	picture := panelPicture{
		frame:  facts,
		layout: withLines(lineRemaining, lineEnds, lineTemperatures, lineElapsed, lineMaterial, linePrinter, lineLayer),
		at:     at,
	}
	picture.layout.zone = zone

	for _, one := range []struct {
		line int
		want string
	}{
		{lineRemaining, "42M"},
		{lineEnds, "ENDS 21:42"},
		{lineTemperatures, "NOZ 221/220 BED 60"},
		{lineElapsed, "1H12M"},
		{lineMaterial, "PLA"},
		{linePrinter, "HERMES MK4"},
		{lineLayer, "Z 8.6MM"},
	} {
		if got := picture.lineWord(one.line, livePanel.width); got != one.want {
			t.Fatalf("%s reads %q, want %q", lineKey(one.line), got, one.want)
		}
	}
}

// TestACountdownReadsAsMinutesAndHours is the one format with arithmetic in it, at the boundaries
// somebody would notice: the last minute, the hour, and the machine that has said nothing.
func TestACountdownReadsAsMinutesAndHours(t *testing.T) {
	for _, one := range []struct {
		seconds int
		want    string
	}{
		{-1, ""},  // not reported
		{0, "0M"}, // the machine is saying the job is over
		{59, "1M"},
		{60, "1M"},
		{61, "2M"},
		{2520, "42M"},
		{3600, "1H"},
		{4320, "1H12M"},
		{7200, "2H"},
		{86399, "24H"},
	} {
		if got := durationWord(one.seconds); got != one.want {
			t.Fatalf("%d seconds reads %q, want %q", one.seconds, got, one.want)
		}
	}
}

// TestTheEndsLineNeedsAZoneAndWritesItInThatZone is the one line that is a clock rather than a count.
//
// A module with no zone draws no clock at all: the process runs in a container whose clock is UTC and
// the operator lives at UTC-7 (AGENTS.md §5.14), so a time written there would be wrong every evening
// and look healthy. The route refuses the layout in that state; the painter's own answer is silence.
func TestTheEndsLineNeedsAZoneAndWritesItInThatZone(t *testing.T) {
	zone := panelZone(t)
	at := time.Date(2026, time.November, 19, 21, 0, 0, 0, zone)

	zoned := panelPicture{frame: frameWithFacts(), layout: withLines(lineEnds), at: at}
	zoned.layout.zone = zone

	if got := zoned.lineWord(lineEnds, livePanel.width); got != "ENDS 21:42" {
		t.Fatalf("the ends line reads %q, want ENDS 21:42", got)
	}

	// The same instant with no zone configured: nothing is drawn, and nothing is invented.
	unzoned := panelPicture{frame: frameWithFacts(), layout: withLines(lineEnds), at: at}

	if got := unzoned.lineWord(lineEnds, livePanel.width); got != "" {
		t.Fatalf("the ends line reads %q with no zone, want nothing at all", got)
	}

	// And a printer that has not said how long is left has no clock either, even with a zone.
	quiet := composingFrame()
	quiet.Panel = render.NoPanelFacts()

	silent := panelPicture{frame: quiet, layout: withLines(lineEnds), at: at}
	silent.layout.zone = zone

	if got := silent.lineWord(lineEnds, livePanel.width); got != "" {
		t.Fatalf("the ends line reads %q with no remaining count, want nothing at all", got)
	}
}

// TestAHeaterThatSaidNothingIsLeftOutOfTheTemperatureLine is the half of the temperatures line that
// would be a lie if it were filled in: a target of zero is a heater that is off, and a heater that
// said nothing is not one that is off.
func TestAHeaterThatSaidNothingIsLeftOutOfTheTemperatureLine(t *testing.T) {
	for _, one := range []struct {
		name  string
		facts render.PanelFacts
		want  string
	}{
		{
			name:  "both heaters",
			facts: render.PanelFacts{Nozzle: render.Degrees{Now: 221, Target: 220, Reported: true}, Bed: render.Degrees{Now: 60, Target: 60, Reported: true}},
			want:  "NOZ 221/220 BED 60",
		},
		{
			name:  "the bed alone",
			facts: render.PanelFacts{Bed: render.Degrees{Now: 60, Target: 60, Reported: true}},
			want:  "BED 60",
		},
		{
			name:  "neither, which is no reading and not a machine at zero",
			facts: render.PanelFacts{},
			want:  "",
		},
	} {
		if got := temperaturesWord(one.facts); got != one.want {
			t.Fatalf("%s reads %q, want %q", one.name, got, one.want)
		}
	}
}

// TestTheHeightLineIsAPositionAndNotALayer is the one line that is not what the operator asked for by
// name. He asked for "layer / Z height"; the machine reports a Z position and no layer count at all
// (internal/printer's PathAxisZ), so the line says Z and nothing pretends to be L42.
func TestTheHeightLineIsAPositionAndNotALayer(t *testing.T) {
	if got := heightWord(render.PanelFacts{HeightMM: 8.6, HeightReported: true}); got != "Z 8.6MM" {
		t.Fatalf("the height line reads %q, want Z 8.6MM", got)
	}

	if got := heightWord(render.PanelFacts{}); got != "" {
		t.Fatalf("the height line reads %q with no reading, want nothing at all", got)
	}

	if strings.Contains(heightWord(render.PanelFacts{HeightMM: 8.6, HeightReported: true}), "L") &&
		!strings.Contains(heightWord(render.PanelFacts{HeightMM: 8.6, HeightReported: true}), "MM") {
		t.Fatal("the height line is claiming a layer number")
	}
}

// TestAStateWordStillBeatsTheComposer is the rule the lines do not touch: a panel showing a state's
// word is not composed at all, and the finished colour holds through silence.
func TestAStateWordStillBeatsTheComposer(t *testing.T) {
	finished := composingFrame()
	finished.Reason = render.ReasonNoLiveJob

	if drawsComposed(finished, livePanel, withLines(lineRemaining, lineTemperatures)) {
		t.Fatal("a panel with no live job was composed, and a state's word is what it shows")
	}
}

// TestAStripNeverComposesTheLines is the other half: the seven lines are a panel's, and a strip is one
// dimension. A stored panel layout on a strip stays as inert as it has always been.
func TestAStripNeverComposesTheLines(t *testing.T) {
	// A strip is one run of pixels: `run`, one row, and never a matrix.
	strip := panel{width: 48, height: 1, run: true}

	if drawsComposed(composingFrame(), strip, withLines(lineRemaining, lineTemperatures, lineMaterial)) {
		t.Fatal("a strip was composed with the optional lines, and a strip is one dimension")
	}

	if _, err := withLines(lineRemaining).bandsOn(strip); err == nil {
		t.Fatal("the lines' arithmetic accepted a strip as a panel")
	}
}
