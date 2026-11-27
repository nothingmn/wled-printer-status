package main

// The layout, and the composed panel frame it draws.
//
// Four things are proved here, and each one is a thing that would be wrong on a light nobody is
// looking at rather than wrong in a build:
//
//   - the layout is optional, its defaults are the picture the operator asked for, and every way of
//     writing one this module cannot act on is refused with the field named;
//   - the frame's pixels are where the layout says they are — the bar's band, the digits' box, both
//     halves of the rotation — asserted at coordinates worked out by hand rather than by calling the
//     same function twice;
//   - a job name too long for the panel is cut where this build says it cuts, and the operator's own
//     file name is the example;
//   - the preview route and the steady path draw the **same bytes** for the same picture, which is
//     the one test that stops two pictures drifting apart, and the realtime hand-back is released
//     when the print ends.
//
// Nothing here needs a controller, a clock or a printer: the panel is a fake, the clock is one a test
// moves, and the printing frame is one that render.Decide decided from a snapshot a test built.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prusa3d/prusa-connect/sdk/deviceapi"

	"github.com/nothingmn/wled-printer-status/internal/printer"
	"github.com/nothingmn/wled-printer-status/internal/render"
	"github.com/nothingmn/wled-printer-status/internal/text"
	"github.com/nothingmn/wled-printer-status/internal/wled"
)

// longJobName is the name of the operator's own print, and the example this file cuts: 38 characters
// of machine, nozzle, layer, material and slicer bookkeeping.
const longJobName = "MK4IS_0.4n_0.2mm_PLA_MK4IS_1h52m.gcode"

// logLines is a writer's log as a test reads it: every line, in order.
type logLines struct {
	mu    sync.Mutex
	lines []string
}

// logf is the writer's own log function.
func (l *logLines) logf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.lines = append(l.lines, strings.TrimSpace(fmt.Sprintf(format, args...)))
}

// all is every line written so far, as a copy.
func (l *logLines) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()

	return append([]string(nil), l.lines...)
}

// count is how many lines contain that phrase.
func (l *logLines) count(phrase string) int {
	found := 0

	for _, line := range l.all() {
		if strings.Contains(line, phrase) {
			found++
		}
	}

	return found
}

// atHalfZero is a moment at which the rotation is showing the first half — the percentage — and is
// found by asking rotationHalf rather than by knowing what a Unix second divides by.
func atHalfZero(t *testing.T) time.Time {
	t.Helper()

	at := time.Date(2026, time.November, 18, 22, 0, 0, 0, time.UTC)
	for spin := 0; rotationHalf(at, defaultRotateSeconds*time.Second) != 0; spin++ {
		if spin > defaultRotateSeconds {
			t.Fatal("no moment within a rotation shows the percentage, so rotationHalf is not alternating")
		}

		at = at.Add(time.Second)
	}

	return at
}

// printingSnapshot is a printer that is printing a job at that progress, with that name, and whose
// readings are live. It is what render.Decide reads, so the frame it decides is the frame a printing
// printer really has.
func printingSnapshot(at time.Time, progress float64, jobName string) printer.Snapshot {
	return printer.Snapshot{
		ID:         "07e893bb-b922-4ff0-9ad3-061b957400c3",
		Name:       "Hermes MK4",
		Connected:  true,
		State:      printer.Reading[string]{Value: printer.StatePrinting, Known: true},
		LastChange: at,
		ObservedAt: at,
		Job: printer.Job{
			Live:     true,
			Running:  printer.Reading[bool]{Value: true, Known: true},
			Progress: printer.Reading[float64]{Value: progress, Known: true},
			Display:  printer.Reading[string]{Value: jobName, Known: true},
		},
	}
}

// litBox is the bounding box of the pixels of one colour in a frame, and how many there are.
type litBox struct {
	left, top, right, bottom, count int
}

// boxOf finds the bounding box of one colour in a picture: a rectangle in the panel's own pixel
// order, or an empty box when the picture does not carry that colour at all.
func boxOf(picture []wled.RGB, width int, colour wled.RGB) litBox {
	box := litBox{left: width + 1, top: len(picture) + 1, right: -1, bottom: -1}

	for index, pixel := range picture {
		if pixel != colour {
			continue
		}

		row, column := index/width, index%width

		box.count++
		box.left = min(box.left, column)
		box.right = max(box.right, column)
		box.top = min(box.top, row)
		box.bottom = max(box.bottom, row)
	}

	return box
}

// TestTheLayoutIsOptionalAndItsDefaultsAreThePictureTheOperatorAskedFor is the setting itself: absent,
// empty and `{}` all mean a bar at the bottom twelve rows thick, the percentage and the job's name
// rotating every fifteen seconds.
//
// It is also the promise that an install which never touches the box starts: the reading this module
// acts on is never refused for being absent, and the writer it builds composes a picture. The other
// direction is here too — a writer handed no layout draws nothing of its own, which is how every test
// that predates this setting keeps drawing what it always drew.
func TestTheLayoutIsOptionalAndItsDefaultsAreThePictureTheOperatorAskedFor(t *testing.T) {
	want := layout{
		bar:     true,
		barBand: bandBottom,
		barRows: 12,
		percent: true,
		name:    true,
		rotate:  15 * time.Second,
	}

	for _, shape := range []struct {
		name string
		raw  string
	}{
		{"absent", ""},
		{"null", "null"},
		{"an empty object", "{}"},
		{"whitespace", "  \n {} \n  "},
	} {
		t.Run(shape.name, func(t *testing.T) {
			got, err := parseLayout(json.RawMessage(shape.raw))
			if err != nil {
				t.Fatalf("parseLayout(%q): %v", shape.raw, err)
			}

			if got != want {
				t.Fatalf("parseLayout(%q) = %+v, want %+v", shape.raw, got, want)
			}
		})
	}

	// Through the settings, which is the path a start takes.
	got, err := Settings{}.wantedLayout()
	if err != nil {
		t.Fatalf("the settings with no layout: %v", err)
	}

	if got != want {
		t.Fatalf("the settings with no layout read as %+v, want %+v", got, want)
	}

	// The manifest declares no layout setting: the layout is one of the things the store owns
	// (docs/product-requirements.md §4), and the default above is what a start with nothing
	// configured composes with. The only options are the five light addresses (declared.go).
	for _, key := range settingsKeys(t) {
		if !strings.HasPrefix(key, "light") || !strings.HasSuffix(key, "_address") {
			t.Fatalf("module.yaml declares the setting %q, and the layout is the store's", key)
		}
	}

	// A layout that draws nothing of its own is a panel drawn exactly as it was before this setting
	// existed. That is the value every writer built without a layout carries.
	if (layout{}).draws() {
		t.Fatal("the zero layout draws something, and a writer built without a layout must draw nothing of its own")
	}

	// One part off is a layout that draws, and the default's band is a bottom band.
	if !(layout{bar: true}).draws() {
		t.Fatal("a layout with a bar draws nothing")
	}
}

// TestTheLayoutRefusesWhatItCannotActOn is every way of writing this setting that this module will
// not run, one case at a time, each with the field and the value in the sentence.
//
// The rule is the one §5.9 records for the manifest: a key that asks for something and is answered
// with silence is a setting that lies. So an unknown key, a band this module does not know, a bar too
// thick to leave a picture, a rotation of no seconds and a part this build does not draw are all
// refused at configure — before a writer exists and before the light is touched — rather than
// discovered on a panel at midnight.
func TestTheLayoutRefusesWhatItCannotActOn(t *testing.T) {
	for _, shape := range []struct {
		name  string
		raw   string
		names []string
	}{
		{
			name:  "a key this module does not read",
			raw:   `{"bare": true}`,
			names: []string{"unknown field", `"bare"`, "bar_band, bar_rows"},
		},
		{
			name:  "a band that is neither end of the panel",
			raw:   `{"bar_band": "middle"}`,
			names: []string{"bar_band", `"middle"`, `"top"`, `"bottom"`},
		},
		{
			name:  "a bar thicker than the panel may give",
			raw:   `{"bar_rows": 49}`,
			names: []string{"bar_rows", "49", "1 to 48", "16 rows"},
		},
		{
			name:  "a bar of no rows",
			raw:   `{"bar_rows": 0}`,
			names: []string{"bar_rows", "0", "1 to 48"},
		},
		{
			name:  "a key that names no line this build draws",
			raw:   `{"layers": true}`,
			names: []string{"unknown field", `"layers"`, "printer_name, layer"},
		},
		{
			name:  "a rotation of no seconds",
			raw:   `{"rotate_seconds": 0}`,
			names: []string{"rotate_seconds", "0", "15"},
		},
		{
			name:  "a list where one object belongs",
			raw:   `[{"bar": true}]`,
			names: []string{"layout", "a list", "one object"},
		},
	} {
		t.Run(shape.name, func(t *testing.T) {
			got, err := parseLayout(json.RawMessage(shape.raw))
			if err == nil {
				t.Fatalf("parseLayout(%s) answered %+v, want a refusal", shape.raw, got)
			}

			for _, want := range shape.names {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("the refusal does not name %q: %v", want, err)
				}
			}

			// And nothing was applied: a refused layout is not a partly-read one.
			if got != (layout{}) {
				t.Fatalf("a refused layout answered %+v, want the zero layout", got)
			}
		})
	}
}

// TestTheLayoutRefusesBandsThatDoNotFitThePanelItIsPointedAt is the other half of the bar's arithmetic:
// configure judges the setting on its own, and the bands are judged against a panel's real height when
// a frame is drawn.
//
// A twelve-row bar is a good bar on the operator's 64-row panel and an impossible one on a twelve-row
// panel, and only the pair says which. The refusal names both numbers, and nothing is drawn: a
// cropped picture is the answer this build will not give.
func TestTheLayoutRefusesBandsThatDoNotFitThePanelItIsPointedAt(t *testing.T) {
	small := panel{width: 64, height: 12}

	if _, err := defaultLayout().bandsOn(small); err == nil {
		t.Fatal("a twelve-row bar fits a twelve-row panel, and it must not")
	} else if !strings.Contains(err.Error(), "bar_rows is 12") || !strings.Contains(err.Error(), "12 rows high") {
		t.Fatalf("the refusal does not name both numbers: %v", err)
	}

	// Six rows of text is fewer than a glyph, so even a bar that leaves rows leaves too few.
	if _, err := (layout{bar: true, barRows: 6, percent: true}).bandsOn(small); err == nil {
		t.Fatal("six rows of bar left five rows for the text, and a glyph is seven rows")
	}

	// The operator's panel takes the operator's default, and the bar is at the bottom of it.
	bands, err := defaultLayout().bandsOn(livePanel)
	if err != nil {
		t.Fatalf("the default layout on the operator's panel: %v", err)
	}

	if bands.barTop != 52 || bands.barRows != 12 || bands.textTop != 0 || bands.textRows != 52 {
		t.Fatalf("the default layout on a 256x64 panel puts the bar at %d for %d rows and the text at %d for %d rows, "+
			"want the bar at 52 for 12 and the text at 0 for 52", bands.barTop, bands.barRows, bands.textTop, bands.textRows)
	}

	// A top band is the same picture the other way up, and it is the operator's other word.
	top := defaultLayout()
	top.barBand = bandTop

	bands, err = top.bandsOn(livePanel)
	if err != nil {
		t.Fatalf("the default layout with a top band: %v", err)
	}

	if bands.barTop != 0 || bands.barRows != 12 || bands.textTop != 12 || bands.textRows != 52 {
		t.Fatalf("a top band puts the bar at %d for %d rows and the text at %d for %d rows, want 0/12 and 12/52",
			bands.barTop, bands.barRows, bands.textTop, bands.textRows)
	}

	// And a bar switched off leaves the whole panel to the text.
	off := defaultLayout()
	off.bar = false

	bands, err = off.bandsOn(livePanel)
	if err != nil {
		t.Fatalf("a layout with no bar: %v", err)
	}

	if bands.barRows != 0 || bands.textRows != 64 {
		t.Fatalf("a layout with no bar gives the bar %d rows and the text %d, want 0 and 64", bands.barRows, bands.textRows)
	}
}

// TestTheComposedFrameIsTheBarTheBandAndTheDigits is the picture itself, at coordinates worked out by
// hand rather than by calling the code twice.
//
// At 41 % of the operator's panel: the bar's band is the bottom twelve rows, and its boundary is
// 41 % of 256 columns — 105 of them, because the fill is the frame's own fraction of the width. The
// digits are "41%" at a scale of 5, which is three glyphs and two gaps: 17 glyph columns, 85 pixels
// wide and 35 tall, centred in the 52 rows the bar leaves. 85 pixels of slack over two is 42 either
// side plus the half pixel the centring drops, so the word starts at column 85 and row 8. The '4' is
// lit in its fourth glyph column and its first row, so the leftmost lit pixel of the picture is
// column 85+15 = 100 and the topmost is row 8.
func TestTheComposedFrameIsTheBarTheBandAndTheDigits(t *testing.T) {
	at := atHalfZero(t)
	frame := previewFrame(41, "MK4IS", 16384)
	picture := panelPicture{frame: frame, layout: defaultLayout(), at: at}

	pixels, err := picture.draw(livePanel)
	if err != nil {
		t.Fatalf("draw the composed frame: %v", err)
	}

	if len(pixels) != 16384 {
		t.Fatalf("the picture is %d pixels, want the whole panel's 16384", len(pixels))
	}

	// The bar: every column below 105 in the bottom twelve rows is the state colour, every column from
	// 105 is the colour an empty range is drawn in, and nothing else is either of them.
	unfilled := finishedWordBackground

	for row := 0; row < 64; row++ {
		for column := 0; column < 256; column++ {
			got := pixels[row*256+column]
			inBand := row >= 52

			switch {
			case inBand && column < 105:
				if got != render.ColourPrinting {
					t.Fatalf("row %d column %d is %+v, want the bar's colour in the filled part", row, column, got)
				}
			case inBand:
				if got != unfilled {
					t.Fatalf("row %d column %d is %+v, want the empty part of the bar", row, column, got)
				}
			}
		}
	}

	// The digits: the set of lit pixels above the bar is exactly the rasteriser's word, placed where
	// the layout puts it. That pins the layout's own decision — which band, which scale, where in the
	// band — and leaves the font's shape to internal/text, which pins it itself.
	lit, err := text.Raster("41%", 5, text.Geometry{Width: 256, Height: 52})
	if err != nil {
		t.Fatalf("rasterise the digits: %v", err)
	}

	want := make([]int, 0, len(lit))
	for _, index := range lit {
		want = append(want, index)
	}

	slices.Sort(want)

	got := make([]int, 0, len(lit))

	for index, pixel := range pixels {
		if pixel == render.ColourPrinting && index/256 < 52 {
			got = append(got, index)
		}
	}

	if !slices.Equal(got, want) {
		t.Fatalf("the digits cover %d pixels above the bar, want the %d the rasteriser puts there", len(got), len(want))
	}

	// And the box those pixels make, as arithmetic rather than as a call: three glyphs and two gaps are
	// 17 glyph columns, 85 pixels of a 256-wide panel, so the word starts at column 85 — 171 of slack,
	// halved and rounded down — and the last of its 35 rows is row 42, which is 8 plus 35 less one.
	box := boxOf(pixels[:52*256], 256, render.ColourPrinting)
	if box.left != 85 || box.top != 8 || box.right != 169 || box.bottom != 42 {
		t.Fatalf("the digits sit in columns %d..%d and rows %d..%d, want 85..169 and 8..42",
			box.left, box.right, box.top, box.bottom)
	}

	// The other half of the rotation: the job's name, at the scale that fits more of it, in the same
	// band. Five glyphs at a scale of 2 are 58 pixels wide and 14 tall, so it starts at column 99 —
	// 256 less 58 is 198, halved — and row 19, which is 52 less 14 halved.
	named := panelPicture{frame: frame, layout: defaultLayout(), at: at.Add(defaultRotateSeconds * time.Second)}

	namedPixels, err := named.draw(livePanel)
	if err != nil {
		t.Fatalf("draw the other half: %v", err)
	}

	nameBox := boxOf(namedPixels[:52*256], 256, render.ColourPrinting)
	if nameBox.left != 99 || nameBox.top != 19 || nameBox.bottom != 32 {
		t.Fatalf("the name sits in columns %d..%d and rows %d..%d, want it to start at 99, 19 and end at row 32",
			nameBox.left, nameBox.right, nameBox.top, nameBox.bottom)
	}

	// The bar is the same bar in both halves: the half that is showing does not move it.
	for index := 52 * 256; index < len(namedPixels); index++ {
		if namedPixels[index] != pixels[index] {
			t.Fatalf("the bar moved between the two halves at pixel %d: %+v then %+v", index, pixels[index], namedPixels[index])
		}
	}
}

// TestTheRotationChangesHalfOnItsOwnInterval is the interval itself: which half a moment falls in,
// either side of a boundary, with no clock and no waiting.
//
// A picture with only one half does not rotate at all: a panel that went blank every fifteen seconds
// because the layout drew no name would read as a fault rather than as a design.
func TestTheRotationChangesHalfOnItsOwnInterval(t *testing.T) {
	rotate := defaultRotateSeconds * time.Second
	at := atHalfZero(t)

	for _, shape := range []struct {
		name string
		at   time.Time
		want int
	}{
		{"the moment", at, 0},
		{"a second before the boundary", at.Add(rotate - time.Second), 0},
		{"the boundary", at.Add(rotate), 1},
		{"the boundary and fourteen seconds", at.Add(2*rotate - time.Second), 1},
		{"two boundaries", at.Add(2 * rotate), 0},
	} {
		if got := rotationHalf(shape.at, rotate); got != shape.want {
			t.Fatalf("%s: rotationHalf = %d, want %d", shape.name, got, shape.want)
		}
	}

	// A picture with a percentage and no name never leaves the percentage, whatever the clock says.
	digitsOnly := panelPicture{
		frame:  render.Frame{Colour: render.ColourPrinting, Percent: 41, Total: 16384, Reason: render.ReasonProgress},
		layout: layout{percent: true, rotate: rotate},
		at:     at,
	}

	bands, err := digitsOnly.layout.bandsOn(livePanel)
	if err != nil {
		t.Fatalf("bands for a picture with no bar and no name: %v", err)
	}

	if halves := digitsOnly.halves(livePanel, bands); !slices.Equal(halves, []string{"41%"}) {
		t.Fatalf("a layout with no name rotates through %v, want only the digits", halves)
	}

	// And a name with no percentage never leaves the name.
	nameOnly := panelPicture{
		frame:  render.Frame{Colour: render.ColourPrinting, Percent: -1, Name: "MK4IS", Total: 16384, Reason: render.ReasonProgressUnknown},
		layout: layout{name: true, rotate: rotate},
		at:     at.Add(rotate),
	}

	bands, err = nameOnly.layout.bandsOn(livePanel)
	if err != nil {
		t.Fatalf("bands for a picture with a name and no digits: %v", err)
	}

	if halves := nameOnly.halves(livePanel, bands); !slices.Equal(halves, []string{"MK4IS"}) {
		t.Fatalf("a layout with no percentage rotates through %v, want only the name", halves)
	}
}

// TestALongJobNameIsCutAtTheLastWordThatFits is the decision about a name that is longer than the
// panel, asserted on the operator's own file name.
//
// The three steps are the decision: upper case, because the font has no lower-case set; every
// character the font does not hold becomes a separator, because a Prusa file name is written with
// underscores and the alternative is a panel with no name for any real print; and the cut is at the
// last whole word that fits, because the head of a slicer's name identifies the job and the numbers
// after it are the slicer's own bookkeeping.
//
// Twenty-one characters is what a 256-wide panel holds at a scale of 2, and the twenty-first of them
// is the space before the machine name that comes round a second time — so the picture is the four
// words that finish before it: "MK4IS 0.4N 0.2MM PLA".
func TestALongJobNameIsCutAtTheLastWordThatFits(t *testing.T) {
	if got := drawableName(longJobName); got != "MK4IS 0.4N 0.2MM PLA MK4IS 1H52M.GCODE" {
		t.Fatalf("the name draws as %q, want it upper-cased with its underscores turned into spaces", got)
	}

	// The arithmetic, said out loud: 21 glyphs at a scale of 2 are 21*6-1 columns of glyph, 250 pixels
	// of the panel's 256.
	if fit := glyphsThatFit(256, 2); fit != 21 {
		t.Fatalf("a 256-wide panel holds %d glyphs at a scale of 2, want 21", fit)
	}

	if fit := glyphsThatFit(256, 3); fit != 14 {
		t.Fatalf("a 256-wide panel holds %d glyphs at a scale of 3, want 14", fit)
	}

	if fit := glyphsThatFit(256, 5); fit != 8 {
		t.Fatalf("a 256-wide panel holds %d glyphs at a scale of 5, want 8", fit)
	}

	if got := fitName(longJobName, 256, 2); got != "MK4IS 0.4N 0.2MM PLA" {
		t.Fatalf("the name draws as %q, want %q", got, "MK4IS 0.4N 0.2MM PLA")
	}

	// A name whose first word is itself wider than the panel is cut at the last character that fits
	// rather than dropped: as much of it as there is room for, and no more.
	if got := fitName("SUPERCALIFRAGILISTICEXPIALIDOCIOUS", 256, 2); len([]rune(got)) != 21 ||
		!strings.HasPrefix("SUPERCALIFRAGILISTICEXPIALIDOCIOUS", got) {
		t.Fatalf("a single long word draws as %q, want its first 21 characters", got)
	}

	// A name that fits is untouched, and a name with nothing drawable in it is nothing at all.
	if got := fitName("mk4is", 256, 2); got != "MK4IS" {
		t.Fatalf("a short lower-case name draws as %q, want MK4IS", got)
	}

	if got := fitName("___", 256, 2); got != "" {
		t.Fatalf("a name of underscores draws as %q, want nothing", got)
	}

	// The rasteriser is what draws it, so the cut has to be a word the font can draw: this is the
	// check that the two agree, and it is the reason the font is asked rather than copied.
	if _, err := text.Size(fitName(longJobName, 256, 2), 2); err != nil {
		t.Fatalf("the cut name is not a word the font draws: %v", err)
	}
}

// TestThePreviewAndTheSteadyPictureAreTheSameBytes is the one test that stops two pictures drifting
// apart, and it is the whole reason the composed frame is one function.
//
// The steady path is given the frame render.Decide decided from a printing printer's snapshot; the
// preview route is given the same progress and the same name as query parameters. If the route's
// picture were built differently, or drawn at another scale, or in another colour, the two would part
// company here — and nothing on the light would ever say so.
func TestThePreviewAndTheSteadyPictureAreTheSameBytes(t *testing.T) {
	at := atHalfZero(t)

	printing := render.Decide(printingSnapshot(at, 41, longJobName), at, render.Config{LEDs: 16384})

	if printing.Percent != 41 || printing.Name != longJobName || !aboutAJob(printing) {
		t.Fatalf("the printing frame is %+v, want 41 %% of %s about a job", printing, longJobName)
	}

	// The steady path: a writer on the operator's panel, handed the frame the driver would hand it.
	steadyLight := newPanelStrip()

	steady := newWriter(steadyLight, 128, quiet)
	steady.setPanel(steadyLight.info)
	steady.layout = defaultLayout()

	steadyClock := newFakeClock(at)
	steadyClock.watch(steady)

	steadyPushed := pushInto(steady)

	steadyCtx, stopSteady := context.WithCancel(context.Background())
	defer stopSteady()

	go steady.run(steadyCtx)

	steady.submit(printing)

	waitFor(t, "the steady picture to reach the panel", func() bool { return steadyPushed.pushed() >= 1 })

	// The preview path: another writer on the same panel, at the same moment, with the route asked for
	// the same picture.
	previewLight := newPanelStrip()

	pen := newWriter(previewLight, 128, quiet)
	pen.setPanel(previewLight.info)
	pen.layout = defaultLayout()

	previewClock := newFakeClock(at)
	previewClock.watch(pen)

	previewPushed := pushInto(pen)

	previewCtx, stopPreview := context.WithCancel(context.Background())
	defer stopPreview()

	go pen.run(previewCtx)

	forgetPreview()
	defer forgetPreview()

	offerPreview(&previewTarget{pen: pen, where: "http://10.0.0.136"})

	recorder := postPreviewFrame(t, "/preview-frame?percent=41&name="+url.QueryEscape(longJobName)+"&hold=120")
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("the preview answered %d, want 202: %s", recorder.Code, recorder.Body.String())
	}

	waitFor(t, "the previewed picture to reach the panel", func() bool { return previewPushed.pushed() >= 1 })

	if got, want := previewPushed.last(), steadyPushed.last(); !slices.Equal(got, want) {
		t.Fatalf("the preview and the steady picture differ: %d pixels against %d, first difference at %d",
			len(got), len(want), firstDifference(got, want))
	}

	// The body under the pixels is the same body, and it is the composed one: segment 0 the whole panel
	// with its name cleared, segment 1 switched off so that it cannot be drawn over the picture.
	steadyBody := steadyLight.shown()[0].segments
	previewBody := previewLight.shown()[0].segments

	if len(steadyBody) != 2 || len(previewBody) != 2 {
		t.Fatalf("the establishing bodies carry %d and %d segments, want 2 each", len(steadyBody), len(previewBody))
	}

	if !steadyBody[0].ClearName || steadyBody[0].Name != "" {
		t.Fatalf("the composed establishing body names segment 0 %q (clear %v), want the name cleared",
			steadyBody[0].Name, steadyBody[0].ClearName)
	}

	if steadyBody[1].On {
		t.Fatal("the composed establishing body leaves the frame's remainder switched on, and it would cover the picture")
	}

	for index := range steadyBody {
		if steadyBody[index] != previewBody[index] {
			t.Fatalf("segment %d of the establishing body differs: %+v against %+v",
				index, steadyBody[index], previewBody[index])
		}
	}

	// And the other half of the rotation, fifteen seconds later on both clocks: the same interval, the
	// same name, the same bytes. A rotation that only one path advanced would be a preview that shows
	// something a printing panel does not.
	steadyMark, previewMark := steadyPushed.pushed(), previewPushed.pushed()

	steadyClock.Advance(defaultRotateSeconds * time.Second)
	previewClock.Advance(defaultRotateSeconds * time.Second)

	waitFor(t, "the steady picture's second half", func() bool { return steadyPushed.pushed() > steadyMark })
	waitFor(t, "the preview's second half", func() bool { return previewPushed.pushed() > previewMark })

	if got, want := previewPushed.last(), steadyPushed.last(); !slices.Equal(got, want) {
		t.Fatalf("the two halves of the rotation differ between the paths, first difference at %d",
			firstDifference(got, want))
	}

	// The second half is the name, and it is not the first half: a test that compared two copies of the
	// same picture would pass without proving anything.
	if slices.Equal(previewPushed.last(), steadyPushed.all()[0]) {
		t.Fatal("the rotation drew the same picture on both halves")
	}
}

// firstDifference is the index of the first pixel that differs between two pictures, or -1.
func firstDifference(a, b []wled.RGB) int {
	for index := 0; index < len(a) && index < len(b); index++ {
		if a[index] != b[index] {
			return index
		}
	}

	return -1
}

// TestTheComposedPanelIsHandedBackWhenThePrintEnds is the end of a print, and the only proof this
// transport allows that the panel is back on its own segments.
//
// While a print runs the panel is held live: the picture is pushed again every second, because this
// firmware leaves realtime 2500 ms after the last datagram. When the print ends the module stops
// pushing, writes the steady frame as segments, and reads the controller once past the firmware's own
// timeout — because a JSON write does not end live mode by itself, measured on this panel.
func TestTheComposedPanelIsHandedBackWhenThePrintEnds(t *testing.T) {
	at := atHalfZero(t)

	light := newPanelStrip()
	lines := &logLines{}

	pen := newWriter(light, 128, lines.logf)
	pen.setPanel(light.info)
	pen.layout = defaultLayout()

	clock := newFakeClock(at)
	clock.watch(pen)

	pushed := pushInto(pen)

	ctx, stop := context.WithCancel(context.Background())
	defer stop()

	go pen.run(ctx)

	pen.submit(render.Decide(printingSnapshot(at, 41, longJobName), at, render.Config{LEDs: 16384}))

	waitFor(t, "the composed picture to reach the panel", func() bool { return pushed.pushed() >= 1 })

	if got := light.realtimeReads(); got != 1 {
		t.Fatalf("the controller was asked about live mode %d times when the picture went up, want once", got)
	}

	// The print ends. The steady frame the driver decides for a finished job is not about a job, so the
	// layout draws nothing of it and the panel goes back to its own segments.
	finished := render.Frame{Colour: render.ColourFinished, Total: 16384, Percent: -1, Reason: render.ReasonNoLiveJob}

	mark := clock.mark()
	pen.submit(finished)

	// The frame that takes the light back is the plain one: the whole panel in the finished colour, not
	// the composed picture's establishing body, which is the background.
	finishedFrames := func() int {
		shown := 0

		for _, call := range light.shown() {
			if len(call.segments) > 0 && call.segments[0].On && call.segments[0].Colour == render.ColourFinished {
				shown++
			}
		}

		return shown
	}

	waitFor(t, "the steady finished frame to be written", func() bool { return finishedFrames() >= 1 })

	// The read that proves the hand-back is taken past the firmware's own timeout, on the writer's own
	// clock, so the test moves the clock rather than waiting two and a half seconds.
	light.takeRealtime(false)
	clock.Advance(realtimeTimeout + handbackMargin)
	clock.waitArmed(t, mark)

	waitFor(t, "the module to read the controller after the hand-back", func() bool {
		return light.realtimeReads() >= 2
	})

	if lines.count("the light is back on its own segments") != 1 {
		t.Fatalf("the hand-back was not said once: %v", lines.all())
	}

	// The last body written is the running print's steady frame — the whole panel in the finished
	// colour — and not a composed picture: segment 0 covers the panel, segment 1 is off, and the name is
	// cleared so that nothing outlives the hold.
	last := light.shown()[len(light.shown())-1].segments
	if len(last) != 2 || last[0].Count != 256 || last[0].RowCount != 64 || !last[0].ClearName {
		t.Fatalf("the frame that took the light back is %+v, want the whole panel in one colour with its name cleared", last)
	}

	if last[0].Colour != render.ColourFinished {
		t.Fatalf("the frame that took the light back is drawn in %+v, want the finished colour", last[0].Colour)
	}

	// And nothing is pushed any more: the panel is on its own segments, so the pixel transport is idle.
	if got := pushed.pushed(); got != 1 {
		t.Fatalf("the transport pushed %d frames, want the one that put the picture up and no more", got)
	}
}

// TestTheComposedPicturesRepeatsAreRecordedAsWrites is the defect the operator reported as "ive
// seen '4 minutes ago' under diagnostics": a panel being written every second reported the age of
// the moment its picture was first established.
//
// The picture on a panel is pixels this module draws, and this firmware leaves realtime 2500 ms
// after the last datagram — so a print costs one push a second for as long as it runs. Every one of
// those is a write to the light, and /status's last write is what a person reads to know the light
// is still being written. The repeats were left out of the record on the argument that a field
// that ticks every second buries the transition it was read for; the field's own label says
// otherwise, and the transition is on the timeline.
func TestTheComposedPicturesRepeatsAreRecordedAsWrites(t *testing.T) {
	at := atHalfZero(t)

	light := newPanelStrip()

	pen := newWriter(light, 128, quiet)
	pen.setPanel(light.info)
	pen.layout = defaultLayout()

	clock := newFakeClock(at)
	clock.watch(pen)

	pushed := pushInto(pen)

	sent.Store(nil)
	t.Cleanup(func() { sent.Store(nil) })

	ctx, stop := context.WithCancel(context.Background())
	defer stop()

	go pen.run(ctx)

	pen.submit(render.Decide(printingSnapshot(at, 41, longJobName), at, render.Config{LEDs: 16384}))

	waitFor(t, "the composed picture to reach the panel", func() bool { return pushed.pushed() >= 1 })

	established := sent.Load()
	if established == nil || !established.OK {
		t.Fatalf("the write that put the composed picture up was recorded as %+v, want a taken write", established)
	}

	// One second on the writer's own clock with nothing new from the printer: the repeat that keeps
	// the panel in realtime is the only write in that second, and it has to move the field.
	clock.Advance(pixelRepeat)

	waitFor(t, "the repeat to be recorded as a write", func() bool {
		last := sent.Load()

		return last != nil && last.At != established.At
	})

	if last := sent.Load(); !last.OK {
		t.Fatalf("the repeat was recorded as a failed write: %+v", last)
	}
}

// TestAFailedPushIsLoggedOnceAndSurvived is the other half of a light that does not answer: a push
// that fails costs one line and a doubling wait, and the module carries on.
//
// The line and the wait are the ones a failed frame already costs. A push a second at a light that is
// not taking datagrams is the storm this module was taught not to make, so the window gates the
// repeats too — and when the controller answers again the picture goes up without a restart.
func TestAFailedPushIsLoggedOnceAndSurvived(t *testing.T) {
	at := atHalfZero(t)

	light := newPanelStrip()
	lines := &logLines{}

	pen := newWriter(light, 128, lines.logf)
	pen.setPanel(light.info)
	pen.layout = defaultLayout()

	clock := newFakeClock(at)
	clock.watch(pen)

	pushed := pushInto(pen)
	pushed.failNext(1)

	ctx, stop := context.WithCancel(context.Background())
	defer stop()

	go pen.run(ctx)

	mark := clock.mark()
	pen.submit(render.Decide(printingSnapshot(at, 41, longJobName), at, render.Config{LEDs: 16384}))

	waitFor(t, "the failed push to be logged", func() bool {
		return lines.count("the light was not updated (failure 1") == 1
	})

	if got := pushed.pushed(); got != 0 {
		t.Fatalf("%d frames reached the transport, want none: the push was made to fail", got)
	}

	// The wait doubles from a second. The writer arms it before it goes quiet, so the test waits for the
	// arm and then moves the clock: advancing a clock nothing is waiting on fires nothing, and the retry
	// would be a second of fake time away for ever.
	clock.waitArmed(t, mark)
	clock.Advance(backoffFloor)

	waitFor(t, "the picture after the wait", func() bool { return pushed.pushed() >= 1 })

	// One line for one failure, and no line saying the light came back: the recovery is silent because
	// nothing was lost. The rule §5.11 keeps is one line per widening, and one failure is one line.
	if got := lines.count("the light was not updated"); got != 1 {
		t.Fatalf("a single failed push wrote %d lines, want one: %v", got, lines.all())
	}

	if got := lines.count("is answering again"); got != 1 {
		t.Fatalf("the recovery wrote %d lines, want one: %v", got, lines.all())
	}
}

// TestAStripIsUntouchedByAPanelLayout is §5.4 from the other side: a strip has no second dimension, so
// there is no composed picture to draw and nothing changes about what it is told.
//
// The setting is read on a strip and unused, which is what makes it safe to leave in an instance whose
// controller is a strip: the frame is the flat run of LEDs this module has always written, and not one
// datagram is pushed.
func TestAStripIsUntouchedByAPanelLayout(t *testing.T) {
	light := newFakeStrip()

	pen := newWriter(light, 128, quiet)
	pen.layout = defaultLayout()

	pushed := pushInto(pen)

	ctx, stop := context.WithCancel(context.Background())
	defer stop()

	go pen.run(ctx)

	// The frame a printing printer decides, which carries a percentage and a name.
	frame := previewFrame(41, longJobName, 48)

	pen.submit(frame)

	light.waitForPaints(t, 1)

	shown := light.shown()[0].segments
	if len(shown) != 2 {
		t.Fatalf("a strip is given %d segments, want the two it has always been given", len(shown))
	}

	for _, segment := range shown {
		if segment.FirstRow != 0 || segment.RowCount != 0 {
			t.Fatalf("a strip is given rows %+v, and a strip has none", segment)
		}
	}

	if shown[0].Count != frame.Lit {
		t.Fatalf("the strip's fill covers %d LEDs, want the frame's %d", shown[0].Count, frame.Lit)
	}

	if pushed.pushed() != 0 {
		t.Fatalf("a strip was given %d frames of pixels, and a strip has nowhere to put them", pushed.pushed())
	}

	// And a preview of a composed frame on a strip is refused rather than drawn folded into one run.
	forgetPreview()
	defer forgetPreview()

	offerPreview(&previewTarget{pen: pen, where: "http://10.0.0.253"})

	recorder := postPreviewFrame(t, "/preview-frame?percent=41")
	if recorder.Code != http.StatusConflict {
		t.Fatalf("a preview on a strip answered %d, want 409: %s", recorder.Code, recorder.Body.String())
	}

	if got := problemCode(t, recorder); got != PreviewNotDrawable.Code().String() {
		t.Fatalf("the refusal carries %q, want %q", got, PreviewNotDrawable.Code().String())
	}
}

// TestAPanelLayoutThatDrawsNothingIsThePlainFrame is the operator's way back: every part switched off
// means the panel takes the frame it had before this setting existed, over the JSON path, with no
// pixels pushed and no realtime held.
func TestAPanelLayoutThatDrawsNothingIsThePlainFrame(t *testing.T) {
	at := atHalfZero(t)

	light := newPanelStrip()
	pen := newWriter(light, 128, quiet)
	pen.setPanel(light.info)

	// A layout written with every part off parses, and it is the zero layout the writer acts on.
	off, err := parseLayout(json.RawMessage(`{"bar": false, "percent": false, "name": false}`))
	if err != nil {
		t.Fatalf("a layout with every part off: %v", err)
	}

	if off != (layout{}) {
		t.Fatalf("a layout with every part off reads as %+v, want the zero layout", off)
	}

	pen.layout = off

	clock := newFakeClock(at)
	clock.watch(pen)

	pushed := pushInto(pen)

	ctx, stop := context.WithCancel(context.Background())
	defer stop()

	go pen.run(ctx)

	pen.submit(render.Decide(printingSnapshot(at, 41, longJobName), at, render.Config{LEDs: 16384}))

	light.waitForPaints(t, 1)

	if pushed.pushed() != 0 {
		t.Fatalf("a panel with no layout pushed %d frames of pixels, want the plain frame over JSON", pushed.pushed())
	}

	shown := light.shown()[0].segments
	if len(shown) != 2 || shown[0].FirstRow != 0 || shown[0].RowCount != 64 {
		t.Fatalf("the plain panel frame is %+v, want two rectangles over every row of the panel", shown)
	}
}

// TestThePreviewOfTheComposedPanelRefusesWhatItCannotDraw is the route's own door, one refusal at a
// time, and in every case nothing was fired and nothing was written.
func TestThePreviewOfTheComposedPanelRefusesWhatItCannotDraw(t *testing.T) {
	panelLight := newPanelStrip()

	short := newPanelStrip()
	short.info = wled.Info{LEDCount: 64 * 12, MatrixWidth: 64, MatrixHeight: 12}

	strip := newFakeStrip()

	borrowed := newPanelStrip()
	borrowed.state = wled.State{
		On:         true,
		Brightness: 255,
		Segments:   []wled.Segment{{ID: 2, Start: 14, Stop: 24, On: true, Colors: []wled.RGB{{R: 8, G: 255, B: 0}}, Effect: 102, Name: "Front 1"}},
	}

	for _, shape := range []struct {
		name       string
		path       string
		target     func() *previewTarget
		status     int
		code       string
		controller counted
	}{
		{
			name: "a progress that is not there",
			path: "/preview-frame?hold=30",
			target: func() *previewTarget {
				pen := newWriter(panelLight, 128, quiet)
				pen.setPanel(panelLight.info)
				pen.layout = defaultLayout()

				return &previewTarget{pen: pen, where: "http://10.0.0.136"}
			},
			status:     http.StatusBadRequest,
			code:       PreviewFrameInvalid.Code().String(),
			controller: panelLight,
		},
		{
			name: "a progress past a hundred",
			path: "/preview-frame?percent=101",
			target: func() *previewTarget {
				pen := newWriter(panelLight, 128, quiet)
				pen.setPanel(panelLight.info)
				pen.layout = defaultLayout()

				return &previewTarget{pen: pen, where: "http://10.0.0.136"}
			},
			status:     http.StatusBadRequest,
			code:       PreviewFrameInvalid.Code().String(),
			controller: panelLight,
		},
		{
			name: "a hold past the ceiling",
			path: "/preview-frame?percent=41&hold=301",
			target: func() *previewTarget {
				pen := newWriter(panelLight, 128, quiet)
				pen.setPanel(panelLight.info)
				pen.layout = defaultLayout()

				return &previewTarget{pen: pen, where: "http://10.0.0.136"}
			},
			status:     http.StatusBadRequest,
			code:       PreviewHoldOutOfRange.Code().String(),
			controller: panelLight,
		},
		{
			name: "a panel whose rows the bands do not fit",
			path: "/preview-frame?percent=41",
			target: func() *previewTarget {
				pen := newWriter(short, 128, quiet)
				pen.setPanel(short.info)
				pen.layout = defaultLayout()

				return &previewTarget{pen: pen, where: "http://10.0.0.136"}
			},
			status:     http.StatusConflict,
			code:       PreviewNotDrawable.Code().String(),
			controller: short,
		},
		{
			name: "a strip",
			path: "/preview-frame?percent=41",
			target: func() *previewTarget {
				pen := newWriter(strip, 128, quiet)

				return &previewTarget{pen: pen, where: "http://10.0.0.253"}
			},
			status:     http.StatusConflict,
			code:       PreviewNotDrawable.Code().String(),
			controller: strip,
		},
		{
			name: "a borrowed segment",
			path: "/preview-frame?percent=41",
			target: func() *previewTarget {
				pen := newSegmentWriter(borrowed, 2, "http://10.0.0.253", quiet)
				pen.setPanel(borrowed.info)
				pen.layout = defaultLayout()

				return &previewTarget{pen: pen, where: "http://10.0.0.253"}
			},
			status:     http.StatusConflict,
			code:       PreviewBorrowedSegment.Code().String(),
			controller: borrowed,
		},
		{
			name:   "no light is being driven at all",
			path:   "/preview-frame?percent=41",
			target: func() *previewTarget { return nil },
			status: http.StatusServiceUnavailable,
			code:   PreviewNotRunning.Code().String(),
		},
	} {
		t.Run(shape.name, func(t *testing.T) {
			forgetPreview()
			defer forgetPreview()

			if target := shape.target(); target != nil {
				offerPreview(target)
			}

			recorder := postPreviewFrame(t, shape.path)

			if recorder.Code != shape.status {
				t.Fatalf("the preview answered %d, want %d: %s", recorder.Code, shape.status, recorder.Body.String())
			}

			if got := problemCode(t, recorder); got != shape.code {
				t.Fatalf("the refusal carries %q, want %q", got, shape.code)
			}

			if shape.controller != nil && shape.controller.wrote() != 0 {
				t.Fatalf("a refused preview wrote %d requests to the controller, want none", shape.controller.wrote())
			}

			if what, waiting := anyFired(); waiting {
				t.Fatalf("a refused preview stored the %q event in a writer's slot", what)
			}
		})
	}
}

// TestThePreviewOfTheComposedPanelSaysWhatItWillDraw is the answer a person reads before walking to the
// panel: the progress, the name as the panel will actually draw it rather than as it was typed, both
// halves of the rotation, and the layout that is in force.
func TestThePreviewOfTheComposedPanelSaysWhatItWillDraw(t *testing.T) {
	light := newPanelStrip()

	pen := newWriter(light, 128, quiet)
	pen.setPanel(light.info)
	pen.layout = defaultLayout()

	forgetPreview()
	defer forgetPreview()

	offerPreview(&previewTarget{pen: pen, where: "http://10.0.0.136"})

	recorder := postPreviewFrame(t, "/preview-frame?percent=41&name="+url.QueryEscape(longJobName)+"&hold=120")
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("the preview answered %d, want 202: %s", recorder.Code, recorder.Body.String())
	}

	var answer struct {
		Controller  string     `json:"controller"`
		Percent     int        `json:"percent"`
		Name        string     `json:"name"`
		Halves      []string   `json:"halves"`
		Layout      layoutView `json:"layout"`
		Holds       int        `json:"holds_seconds"`
		RotationSec int        `json:"rotation_seconds"`
	}

	if err := json.Unmarshal(recorder.Body.Bytes(), &answer); err != nil {
		t.Fatalf("the answer is not JSON: %v: %s", err, recorder.Body.String())
	}

	if answer.Controller != "http://10.0.0.136" || answer.Percent != 41 || answer.Holds != 120 || answer.RotationSec != 15 {
		t.Fatalf("the answer is %+v, want 41 %% on the controller for 120 seconds with a fifteen-second rotation", answer)
	}

	if answer.Name != "MK4IS 0.4N 0.2MM PLA" {
		t.Fatalf("the answer says the name draws as %q, want the cut the panel will show", answer.Name)
	}

	if !slices.Equal(answer.Halves, []string{"41%", "MK4IS 0.4N 0.2MM PLA"}) {
		t.Fatalf("the answer says the rotation shows %v, want the digits and then the name", answer.Halves)
	}

	if answer.Layout != defaultLayout().view() {
		t.Fatalf("the answer says the layout is %+v, want the default %+v", answer.Layout, defaultLayout().view())
	}

	// Nothing was written by the route: the event waits in the writer's one slot for the writer's own
	// goroutine, which no test here started.
	if light.wrote() != 0 {
		t.Fatalf("the route wrote %d requests to the controller, want none", light.wrote())
	}
}

// TestTheLayoutReachesTheWriterThroughTheSettings is the wiring: a layout written in the settings is the
// layout the writer draws, and a change made through the routes rebuilds the writers with it.
func TestTheLayoutReachesTheWriterThroughTheSettings(t *testing.T) {
	shape, err := Settings{Layout: json.RawMessage(`{"bar_rows": 20, "bar_band": "top", "rotate_seconds": 5}`)}.wantedLayout()
	if err != nil {
		t.Fatalf("the settings' layout: %v", err)
	}

	if shape.barRows != 20 || shape.barBand != bandTop || shape.rotate != 5*time.Second {
		t.Fatalf("the settings' layout reads as %+v, want a twenty-row bar at the top rotating every five seconds", shape)
	}

	// The parts that were not written keep the default, because absent is not false.
	if !shape.bar || !shape.percent || !shape.name {
		t.Fatalf("a partly-written layout switches off what it did not mention: %+v", shape)
	}

	// The runner carries it to the writers a route change rebuilds.
	live.setLayout(shape)
	defer live.setLayout(defaultLayout())

	if live.layout != shape {
		t.Fatalf("the runner holds %+v, want the layout the settings described", live.layout)
	}
}

// postPreviewFrame sends one preview request through the module's own routes.
func postPreviewFrame(t *testing.T, path string) *httptest.ResponseRecorder {
	t.Helper()

	recorder := httptest.NewRecorder()
	routes().ServeHTTP(recorder, forwarded(http.MethodPost, path, nil))

	return recorder
}

// TestTheStatusRouteCarriesThePercentageAndTheName is the route the operator reads instead of walking to
// the panel: the two facts the picture is composed from are in it, so a panel showing 41 % of the wrong
// job is a disagreement between two things he can see rather than a picture and a silence.
//
// A frame with no percentage carries -1 and not 0, which is the difference between an unknown progress and
// a print that has not moved.
func TestTheStatusRouteCarriesThePercentageAndTheName(t *testing.T) {
	for _, shape := range []struct {
		name    string
		frame   render.Frame
		percent int
		jobName string
	}{
		{
			name:    "printing",
			frame:   render.Decide(printingSnapshot(atHalfZero(t), 41, longJobName), atHalfZero(t), render.Config{LEDs: 16384}),
			percent: 41,
			jobName: longJobName,
		},
		{
			name:    "a job with no progress this module may present",
			frame:   render.Frame{Colour: render.ColourPrinting, Total: 16384, Percent: -1, Name: "MK4IS", Reason: render.ReasonProgressUnknown},
			percent: -1,
			jobName: "MK4IS",
		},
	} {
		t.Run(shape.name, func(t *testing.T) {
			// The printer the route is answered for: a reading with no printer in it is not a thing a
			// watch publishes, and the route is now asked which printers the caller may see.
			publish(printer.Snapshot{ID: "01J8DEVICE", Name: "Morpheus", Connected: true}, shape.frame)
			defer publish(printer.Snapshot{}, render.Frame{})

			// A watch holding one binding publishes into that store, so the per-binding one has to be empty
			// for the route to answer the single frame.
			forgetBindings()

			recorder := httptest.NewRecorder()
			statusHost(t, deviceapi.Device{ID: "01J8DEVICE"}).ServeHTTP(recorder, forwarded(http.MethodGet, "/status", nil))

			if recorder.Code != http.StatusOK {
				t.Fatalf("the status route answered %d, want 200: %s", recorder.Code, recorder.Body.String())
			}

			var answer statusAnswer
			if err := json.Unmarshal(recorder.Body.Bytes(), &answer); err != nil {
				t.Fatalf("the answer is not JSON: %v: %s", err, recorder.Body.String())
			}

			if answer.Frame.Percent != shape.percent || answer.Frame.Name != shape.jobName {
				t.Fatalf("the frame reads percent %d and name %q, want %d and %q",
					answer.Frame.Percent, answer.Frame.Name, shape.percent, shape.jobName)
			}
		})
	}
}
