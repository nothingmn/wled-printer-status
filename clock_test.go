// The clock format a layout may name: whether the ends line writes a wall time as `ENDS 18:10` or as
// `ENDS 6:10 PM`.
//
// Five questions, and each is a different way the feature could be wrong:
//
//  1. **A document written before the key existed reads, and composes, as 24-hour.** That is the
//     promise an upgrade makes, and the key is absent on every row schema version 10 accepts from an
//     older document rather than copied in as a value.
//  2. **The 12-hour form is the same instant, spelled differently.** Midnight and noon are the two
//     hours a hand-written formatter gets wrong, so they are pinned as words: `12:00 AM` and
//     `12:00 PM`, not `0:00 AM` and not `0:00 PM`.
//  3. **Durations are quantities, not times.** The remaining and elapsed lines read exactly the same
//     on either clock — a toggle that changed them would be a different feature.
//  4. **A clock that does not fit is refused, not trimmed.** `ENDS 6:10 PM` cut to ten columns reads
//     `ENDS 6:10`, which is a different instant drawn as though it were the time.
//  5. **The wall, the thumbnail and the preview agree.** They come through one composition, so this
//     is proved the way the optional lines are: the same frame, pixel for pixel.
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nothingmn/wled-printer-status/internal/render"
)

// twelveHour is the stored row for a binding whose panel writes a 12-hour clock.
func twelveHour() *string {
	said := clockTwelve

	return &said
}

// clockFrame is one printing job whose remaining count is exactly that many seconds, so that the ends
// line's instant is the test's to choose: the frame's own clock (`at`) plus this.
func clockFrame(seconds int) render.Frame {
	frame := frameWithFacts()
	frame.Panel.RemainingSeconds = seconds

	return frame
}

// lineOnClock is one optional line as the panel draws it at that instant, on that clock.
func lineOnClock(line int, clock string, at time.Time, seconds int) string {
	one := withLines(line)
	one.zone = panelZoneForTest()
	one.clock = clock

	return panelPicture{frame: clockFrame(seconds), layout: one, at: at}.lineWord(line, livePanel.width)
}

// panelZoneForTest is the zone the operator's instance names.
func panelZoneForTest() *time.Location {
	zone, err := time.LoadLocation("America/Denver")
	if err != nil {
		panic(err)
	}

	return zone
}

// TestADocumentWrittenBeforeTheClockExistedReadsAndComposes24Hour is the upgrade promise, in three
// steps: the version-9 document steps up, the step writes no clock onto any row, and the picture the
// stepped document composes is the one version 9 drew.
//
// The third step is the one that matters to the operator, and it is why the assertion is on a composed
// line rather than on a field: an upgrade that changed a default would change a panel he is looking at,
// and it would look perfectly healthy while doing it.
func TestADocumentWrittenBeforeTheClockExistedReadsAndComposes24Hour(t *testing.T) {
	const ninth = `{
		"version": 9,
		"bindings": [{"id": "printer-a", "printer": "printer-a", "controller": "10.0.0.253", "segment": 0}],
		"states": [],
		"moments": [],
		"layouts": [{"binding": "printer-a", "bar": true, "ends": true, "percent_scale": 5}],
		"windows": [],
		"lights": [],
		"pauses": [],
		"timezone": "America/Denver",
		"timezone_origin": "operator"
	}`

	found, err := upgradeStore([]byte(ninth))
	if err != nil {
		t.Fatalf("a version-9 document was refused: %v", err)
	}

	if !found.Upgraded || found.Version != 9 {
		t.Fatalf("the document read at version %d, upgraded %v, want version 9 stepped up", found.Version, found.Upgraded)
	}

	if found.Config.Version != storeVersion {
		t.Fatalf("the stepped document is at version %d, want %d", found.Config.Version, storeVersion)
	}

	if len(found.Config.Layouts) != 1 {
		t.Fatalf("the stepped document holds %d layout rows, want the one it had", len(found.Config.Layouts))
	}

	// Nothing is invented: the row carries no clock, and the answer says where the 24-hour it reads
	// as came from — the default, not the store.
	row := found.Config.Layouts[0]
	if row.Clock != nil {
		t.Fatalf("the step wrote clock %q onto a row that had none", *row.Clock)
	}

	if got := row.resolved().clock; got != clockTwentyFour {
		t.Fatalf("a row naming no clock resolves to %q, want %q", got, clockTwentyFour)
	}

	stepped, err := json.Marshal(found.Config)
	if err != nil {
		t.Fatalf("read the stepped document back: %v", err)
	}

	if strings.Contains(string(stepped), `"clock"`) {
		t.Fatalf("the stepped document carries a clock key: %s", stepped)
	}

	// And the picture: the document composes the clock the module has always written, at the instant
	// the frame names, from the zone the document holds.
	tables := storeHolding(t, found.Config)
	pen := newWriter(newPanelStrip(), 128, quiet)
	pen.store = &storeLights{tables: tables, logging: quiet}

	at := time.Date(2026, time.November, 19, 21, 0, 0, 0, panelZoneForTest())
	picture := panelPicture{frame: clockFrame(42 * 60), layout: pen.composing(), at: at}

	if got := picture.lineWord(lineEnds, livePanel.width); got != "ENDS 21:42" {
		t.Fatalf("a stepped document's ends line reads %q, want ENDS 21:42", got)
	}

	// The layout answer, which a screen draws its control from, says 24-hour and that it is the
	// default rather than something somebody chose.
	answer := layoutAnswerOf(binding{ID: "printer-a"}, controllerAnswer{Name: "10.0.0.253"}, row, true, "America/Denver")

	if answer.Clock != clockTwentyFour || answer.ClockOrigin != fromDefault {
		t.Fatalf("the answer reports clock %q from %q, want %q from %q",
			answer.Clock, answer.ClockOrigin, clockTwentyFour, fromDefault)
	}
}

// TestTheTwelveHourFormIsTheSameInstantSpelledDifferently is the arithmetic of the clock, pinned as
// words: a morning, an evening, midnight and noon.
//
// Noon and midnight are the reason this test exists. `3:04 PM` is Go's own 12-hour form, and the two
// hours it could plausibly get wrong are the ones where the 24-hour arithmetic wraps — 00:00 is
// twelve at night and 12:00 is twelve at noon, and both are written with a 12.
func TestTheTwelveHourFormIsTheSameInstantSpelledDifferently(t *testing.T) {
	zone := panelZoneForTest()

	for _, one := range []struct {
		name   string
		at     time.Time
		ends   int
		twelve string
		four   string
	}{
		{
			name:   "a morning",
			at:     time.Date(2026, time.November, 19, 9, 0, 0, 0, zone),
			ends:   42 * 60,
			twelve: "ENDS 9:42 AM",
			four:   "ENDS 09:42",
		},
		{
			name:   "an evening",
			at:     time.Date(2026, time.November, 19, 21, 0, 0, 0, zone),
			ends:   42 * 60,
			twelve: "ENDS 9:42 PM",
			four:   "ENDS 21:42",
		},
		{
			name:   "midnight",
			at:     time.Date(2026, time.November, 19, 23, 30, 0, 0, zone),
			ends:   30 * 60,
			twelve: "ENDS 12:00 AM",
			four:   "ENDS 00:00",
		},
		{
			name:   "noon",
			at:     time.Date(2026, time.November, 19, 11, 30, 0, 0, zone),
			ends:   30 * 60,
			twelve: "ENDS 12:00 PM",
			four:   "ENDS 12:00",
		},
	} {
		t.Run(one.name, func(t *testing.T) {
			if got := lineOnClock(lineEnds, clockTwelve, one.at, one.ends); got != one.twelve {
				t.Fatalf("the 12-hour ends line reads %q, want %q", got, one.twelve)
			}

			if got := lineOnClock(lineEnds, clockTwentyFour, one.at, one.ends); got != one.four {
				t.Fatalf("the 24-hour ends line reads %q, want %q", got, one.four)
			}
		})
	}
}

// TestTheClockFormatLeavesTheDurationsAlone is the boundary of the feature: the toggle is about a wall
// time, and the two lines that are quantities read the same on either clock.
//
// It is proved on the words rather than on the layout, because the defect it guards against is a
// toggle wired to the wrong line — a `1H12M` that became `1H12M PM` would pass any test that only
// looked at the ends line.
func TestTheClockFormatLeavesTheDurationsAlone(t *testing.T) {
	at := time.Date(2026, time.November, 19, 21, 0, 0, 0, panelZoneForTest())

	for _, line := range []int{lineRemaining, lineElapsed} {
		twelve := lineOnClock(line, clockTwelve, at, 42*60)
		four := lineOnClock(line, clockTwentyFour, at, 42*60)

		if twelve != four {
			t.Fatalf("line %d reads %q on a 12-hour clock and %q on a 24-hour one, want the same words", line, twelve, four)
		}

		if twelve == "" {
			t.Fatalf("line %d reads nothing, so this test proves nothing", line)
		}
	}

	// The words themselves, so that a change to the duration format is caught here too rather than
	// only in panellines_test.
	if got := lineOnClock(lineElapsed, clockTwelve, at, 42*60); got != "1H12M" {
		t.Fatalf("the elapsed line reads %q, want 1H12M", got)
	}
}

// TestTheWidthsOfTwoClocks is the measurement the fit is judged by, taken from the module's own font
// rather than assumed.
//
// The optional lines are drawn at one scale — the font's own — and centred across the panel, so a line
// has the panel's whole width and the question is only how many columns the word takes. The numbers are
// pinned here because they are what the refusal quotes and what the screen's own arithmetic restates:
// a 12-hour clock costs half again the columns of a 24-hour one, and it is the extra four glyphs of
// ` PM` and the second digit of an afternoon hour that cost it.
func TestTheWidthsOfTwoClocks(t *testing.T) {
	for _, one := range []struct {
		word  string
		width int
	}{
		// Ten glyphs: 5 columns each and one between, at the scale the lines are drawn at.
		{word: "ENDS 18:10", width: 59},
		{word: "ENDS 00:00", width: 59},
		// Twelve, with a one-digit hour: an evening on the 12-hour clock.
		{word: "ENDS 6:10 PM", width: 71},
		// Thirteen, with a two-digit hour: the longest a wall time can be, and what the fit is
		// judged against so that it does not come and go with the hour.
		{word: "ENDS 12:59 PM", width: 77},
	} {
		if got := lineWidth(one.word); got != one.width {
			t.Fatalf("%q is %d columns wide, want %d", one.word, got, one.width)
		}
	}

	if got := lineWidth(endsWidestWord(clockTwelve)); got != 77 {
		t.Fatalf("the widest 12-hour spelling is %d columns, want 77", got)
	}

	if got := lineWidth(endsWidestWord(clockTwentyFour)); got != 59 {
		t.Fatalf("the widest 24-hour spelling is %d columns, want 59", got)
	}

	// On the operator's own panel, at every scale the percentage may be pinned to, both clocks fit:
	// the lines are drawn at one scale and the digits' scale does not change their width. What the
	// scale changes is the rows the lines have, which is the arithmetic panellines_test covers.
	for _, scale := range []int{3, 4, 5} {
		one := withLines(lineEnds)
		one.zone = panelZoneForTest()
		one.percentScale = scale

		for _, clock := range []string{clockTwentyFour, clockTwelve} {
			one.clock = clock

			bands, err := one.bandsOn(livePanel)
			if err != nil {
				t.Fatalf("the ends line on a %s clock does not fit the 256x64 panel at scale %d: %v", clock, scale, err)
			}

			// The measurement this feature was asked for, at each of the three scales: the word's
			// width against the panel's, and the rows the line takes out of the text band. The width
			// is the same at every scale — the lines are drawn at one — which is the finding.
			t.Logf("scale %d, %s: the ends line is at most %d columns of %d, and %d line takes %d rows leaving %d for the picture's own row",
				scale, clock, lineWidth(endsWidestWord(clock)), livePanel.width,
				len(bands.lines), bands.textRows-bands.primaryRows, bands.primaryRows)
		}
	}
}

// TestATwelveHourClockTooWideForThePanelIsRefusedAndNotTrimmed is the failure this feature could ship
// with: a panel narrow enough for `ENDS 21:42` and not for `ENDS 9:42 PM`, where the answer must be the
// module's own refusal rather than a clock cut down to something that reads as another time.
//
// It is judged in the arithmetic every route asks (bandsOn), against the widest spelling the format can
// produce, so that the verdict does not change at noon.
func TestATwelveHourClockTooWideForThePanelIsRefusedAndNotTrimmed(t *testing.T) {
	narrow := panel{width: 64, height: 64}

	one := withLines(lineEnds)
	one.zone = panelZoneForTest()

	// 24-hour fits this panel: fifty-nine columns of sixty-four.
	if _, err := one.bandsOn(narrow); err != nil {
		t.Fatalf("the 24-hour ends line does not fit a 64-column panel: %v", err)
	}

	// 12-hour does not, and the refusal names the clock, the width it needs and the panel it has.
	one.clock = clockTwelve

	_, err := one.bandsOn(narrow)
	if err == nil {
		t.Fatal("a 77-column clock fits a 64-column panel, and it must not")
	}

	for _, want := range []string{"12-hour", "77 columns", "ENDS 12:59 PM", "64"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the refusal %q does not say %q", err.Error(), want)
		}
	}

	// The word itself is whole, which is the half that says the picture was refused rather than
	// trimmed: the line still reads the instant, and no shorter time is ever produced.
	picture := panelPicture{
		frame:  clockFrame(42 * 60),
		layout: one,
		at:     time.Date(2026, time.November, 19, 21, 0, 0, 0, panelZoneForTest()),
	}

	if got := picture.lineWord(lineEnds, narrow.width); got != "ENDS 9:42 PM" {
		t.Fatalf("the ends line reads %q on a narrow panel, want the whole time ENDS 9:42 PM", got)
	}

	// And drawing it is refused with the painter's own measurement rather than cropping the word.
	if _, err := picture.draw(narrow); err == nil {
		t.Fatal("a picture whose ends line does not fit was drawn")
	}

	// A layout with no zone has no clock to draw, so a narrow panel is not refused for a line that
	// would be blank: the refusal is about a word, and there is no word.
	unzoned := withLines(lineEnds)
	unzoned.clock = clockTwelve

	if _, err := unzoned.bandsOn(narrow); err != nil {
		t.Fatalf("a layout with no zone was refused for a clock it cannot draw: %v", err)
	}
}

// TestTheThumbnailAndThePanelWriteTheSameClock is the one-composition rule, for this key.
//
// `ENDS 18:10` and `ENDS 6:10 PM` are two pictures of one frame, and the Overview draws its thumbnail
// from the same layout the wall does — so the assertion is pixel for pixel, and a cache key built from
// every fact the picture is made from has to carry the clock as well, or a cached thumbnail outlives
// the change and shows a time the panel has stopped writing.
func TestTheThumbnailAndThePanelWriteTheSameClock(t *testing.T) {
	under := newFramesUnderTest(t, bindings{panelBinding()})

	at := time.Date(2026, time.November, 19, 21, 0, 0, 0, panelZoneForTest())

	frame := render.Frame{
		Colour:  render.ColourPrinting,
		Lit:     41 * 16384 / 100,
		Total:   16384,
		Percent: 41,
		Name:    "MK4IS_0.4n_0.2mm_PLA_MK4IS_1h52m.gcode",
		Reason:  render.ReasonProgress,
		Panel: render.PanelFacts{
			RemainingSeconds: 2520,
			PrintingSeconds:  4320,
			Material:         "PLA",
			Printer:          "Hermes MK4",
		},
	}

	own := withLines(lineEnds)
	own.zone = at.Location()
	own.clock = clockTwelve

	publishBinding(panelBinding(), printingJob("printer-a", 41), frame)
	publishShape(panelBinding(), bindingShape{geometry: livePanel, layout: own, laidOut: true})

	pixels, width, height, _ := publishedPicture(t, under)

	want, err := panelPicture{frame: frame, layout: own, at: framesAt}.draw(livePanel)
	if err != nil {
		t.Fatalf("draw the panel the writer composes: %v", err)
	}

	if len(want) != width*height {
		t.Fatalf("the composition is %d pixels and the picture %dx%d", len(want), width, height)
	}

	for index := range want {
		if pixels[index] != want[index] {
			t.Fatalf("pixel %d is %+v, want the panel's own %+v", index, pixels[index], want[index])
		}
	}

	// The words themselves, on each clock: 18:30 UTC is 11:30 in Denver, and forty-two minutes on
	// is 12:12 — the hour a 12-hour clock has to write differently from a 24-hour one.
	if got := (panelPicture{frame: frame, layout: own, at: framesAt}).lineWord(lineEnds, livePanel.width); got != "ENDS 12:12 PM" {
		t.Fatalf("the ends line reads %q on the 12-hour clock, want ENDS 12:12 PM", got)
	}

	// The same frame on a 24-hour clock is a different picture of a different width of word, so the
	// pixel comparison above is not comparing two identical pictures.
	four := own
	four.clock = clockTwentyFour

	if got := (panelPicture{frame: frame, layout: four, at: framesAt}).lineWord(lineEnds, livePanel.width); got != "ENDS 12:12" {
		t.Fatalf("the ends line reads %q on the 24-hour clock, want ENDS 12:12", got)
	}

	// And the preview route, which answers a layout's own view: it is the same composition, so it
	// reports the same format rather than the default.
	if got := own.view().Clock; got != clockTwelve {
		t.Fatalf("the layout view reports clock %q, want %q", got, clockTwelve)
	}

	if got := four.view().Clock; got != clockTwentyFour {
		t.Fatalf("the layout view reports clock %q, want %q", got, clockTwentyFour)
	}

	other, err := panelPicture{frame: frame, layout: four, at: framesAt}.draw(livePanel)
	if err != nil {
		t.Fatalf("draw the same frame on a 24-hour clock: %v", err)
	}

	if len(other) == len(want) && slices.Equal(other, want) {
		t.Fatal("the two clocks drew the same picture, so this test proves nothing")
	}

	// And the cache cannot serve one for the other.
	held := bindingStatus{Controller: "10.0.0.136", Segment: 0}

	twelveKey := cacheKey("10.0.0.136", held, bindingShape{geometry: livePanel, layout: own, laidOut: true}, width, height, framesAt)
	fourKey := cacheKey("10.0.0.136", held, bindingShape{geometry: livePanel, layout: four, laidOut: true}, width, height, framesAt)

	if twelveKey == fourKey {
		t.Fatal("a 12-hour picture and a 24-hour one share a cache key, so a thumbnail can outlive the change")
	}
}

// TestTheRouteTakesTheClockAndReportsItsOrigin is the route's half: the field is accepted, refused by
// name where it is not one of the two, reported with the origin it came from, and put back by DELETE
// like every other value on the row.
func TestTheRouteTakesTheClockAndReportsItsOrigin(t *testing.T) {
	under := newWave3UnderTest(t, twoPrinters(), twoBindings(), "", at(), "America/Denver")

	status, body := under.do(t, http.MethodPut, "/bindings/printer-a/layout", `{"clock": "12-hour", "ends": true}`)
	if status != http.StatusOK {
		t.Fatalf("a 12-hour clock was refused with %d: %v", status, body)
	}

	written := readObject(t, body, "layout")

	if got := readString(t, written, "clock"); got != clockTwelve {
		t.Fatalf("the layout answered clock %q, want %q", got, clockTwelve)
	}

	if got := readString(t, written, "clock_origin"); got != fromStore {
		t.Fatalf("the clock's origin reads %q, want %q", got, fromStore)
	}

	// What the store holds is the row, and a read of it answers the same two values.
	held := under.stored(t)
	if len(held.Layouts) != 1 || held.Layouts[0].Clock == nil || *held.Layouts[0].Clock != clockTwelve {
		t.Fatalf("the store holds %+v, want one row naming a 12-hour clock", held.Layouts)
	}

	status, body = under.do(t, http.MethodGet, "/bindings/printer-a/layout", "")
	if status != http.StatusOK {
		t.Fatalf("reading the layout back answered %d: %v", status, body)
	}

	read := readObject(t, body, "layout")

	if got := readString(t, read, "clock"); got != clockTwelve {
		t.Fatalf("the layout reads back %q, want %q", got, clockTwelve)
	}

	// An unsupported value is refused the way an unsupported rotation is: by name, with both of the
	// accepted values in the sentence, and nothing written.
	for _, bad := range []string{"13-hour", "12", "AM/PM", ""} {
		status, body = under.do(t, http.MethodPut, "/bindings/printer-a/layout", fmt.Sprintf(`{"clock": %q}`, bad))
		if status != http.StatusConflict {
			t.Fatalf("clock %q answered %d, want 409: %v", bad, status, body)
		}

		if got := code(t, body); got != LayoutRefused.Code().String() {
			t.Fatalf("the refusal for %q is %q, want %q", bad, got, LayoutRefused.Code().String())
		}

		detail, _ := body["detail"].(string)
		for _, want := range []string{clockTwentyFour, clockTwelve} {
			if !strings.Contains(detail, want) {
				t.Fatalf("the refusal %q does not name the accepted value %q", detail, want)
			}
		}
	}

	if held := under.stored(t); len(held.Layouts) != 1 || held.Layouts[0].Clock == nil || *held.Layouts[0].Clock != clockTwelve {
		t.Fatalf("a refused clock changed the row: %+v", held.Layouts)
	}

	// And DELETE puts it back to the default — the 24-hour clock, from the default rather than the
	// store — and a second DELETE says nothing was there, as it does for every other field.
	status, body = under.do(t, http.MethodDelete, "/bindings/printer-a/layout", "")
	if status != http.StatusOK {
		t.Fatalf("deleting the layout answered %d: %v", status, body)
	}

	back := readObject(t, body, "layout")

	if got := readString(t, back, "clock"); got != clockTwentyFour {
		t.Fatalf("a deleted layout reports clock %q, want %q", got, clockTwentyFour)
	}

	if got := readString(t, back, "clock_origin"); got != fromDefault {
		t.Fatalf("a deleted layout reports the clock's origin as %q, want %q", got, fromDefault)
	}

	status, body = under.do(t, http.MethodDelete, "/bindings/printer-a/layout", "")
	if status != http.StatusOK {
		t.Fatalf("a second delete answered %d, want 200: %v", status, body)
	}

	if removed, _ := body["removed"].(bool); removed {
		t.Fatalf("a second delete reported a row removed: %v", body)
	}
}

// TestTheClockIsOfferedOnTheLayoutsOfALight is the read a screen is drawn from: the value and its
// origin travel with every binding's layout, including the one nobody has touched.
func TestTheClockIsOfferedOnTheLayoutsOfALight(t *testing.T) {
	under := newWave3UnderTest(t, twoPrinters(), twoBindings(), "", at(), "America/Denver")

	if status, body := under.do(t, http.MethodPut, "/bindings/printer-a/layout", `{"clock": "12-hour"}`); status != http.StatusOK {
		t.Fatalf("writing a 12-hour clock answered %d: %v", status, body)
	}

	status, body := under.do(t, http.MethodGet, "/lights/10.0.0.253/layouts", "")
	if status != http.StatusOK {
		t.Fatalf("the light's layouts answered %d: %v", status, body)
	}

	written := readNamed(t, readObjects(t, body, "layouts"), "printer", "printer-a")
	if got := readString(t, written, "clock"); got != clockTwelve {
		t.Fatalf("the laid-out binding reads clock %q, want %q", got, clockTwelve)
	}

	untouched := readNamed(t, readObjects(t, body, "layouts"), "printer", "printer-b")
	if got := readString(t, untouched, "clock"); got != clockTwentyFour {
		t.Fatalf("an unedited binding reads clock %q, want %q", got, clockTwentyFour)
	}

	if got := readString(t, untouched, "clock_origin"); got != fromDefault {
		t.Fatalf("an unedited binding's clock origin reads %q, want %q", got, fromDefault)
	}
}
