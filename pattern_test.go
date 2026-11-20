package main

// The diagnostic ladder, tested where it can be: the three pictures asserted pixel by pixel, the
// route proved to reach the same writer the word does and to hand the light back, and the hold's
// bounds refused by name rather than clamped.
//
// Nothing here needs a controller and nothing here waits: a pattern is a slice of colours, and a
// datagram cannot be acknowledged, so what a test can prove is the frame's exact content and that
// it left through the transport the word already uses. What the panel does with it is the
// operator's look, which is what these pictures exist for.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nothingmn/wled-printer-status/internal/render"
	"github.com/nothingmn/wled-printer-status/internal/wled"
)

// pixelAt is one pixel of a frame the module built, read at panel coordinate (x, y) — the same
// y*width+x order the transport uses, written out here so that a test failing on a coordinate says
// which one.
func pixelAt(frame []wled.RGB, geometry panel, x, y int) wled.RGB {
	return frame[y*geometry.width+x]
}

// countOf is how many pixels of a frame carry one colour.
func countOf(frame []wled.RGB, colour wled.RGB) int {
	count := 0

	for _, pixel := range frame {
		if pixel == colour {
			count++
		}
	}

	return count
}

// TestEachPatternIsExactlyThePixelsItSaysItIs is the picture, byte for byte, on the operator's
// 256x64 panel.
//
// Each subtest asserts the whole of its picture rather than a sample: a solid panel is all 16,384
// pixels in one colour; a border is every pixel on the edge and no pixel inside it, counted both
// ways; and the quarters are four 4096-pixel blocks meeting at column 128 and row 32, with the
// colours named at their corners. A pattern that drew one pixel wrong would be a picture an
// operator reads as a wiring fault, so the tests pin the counts and the coordinates, not "roughly
// the right shape".
func TestEachPatternIsExactlyThePixelsItSaysItIs(t *testing.T) {
	geometry := panel{width: 256, height: 64}

	t.Run("solid is the whole panel in white", func(t *testing.T) {
		frame, err := patternFrame("solid", geometry)
		if err != nil {
			t.Fatalf("draw the solid pattern: %v", err)
		}

		if len(frame) != geometry.width*geometry.height {
			t.Fatalf("the solid pattern is %d pixels, want the panel's %d", len(frame), geometry.width*geometry.height)
		}

		if got := countOf(frame, patternWhite); got != len(frame) {
			t.Fatalf("%d of the panel's %d pixels are white, want all of them", got, len(frame))
		}

		// One colour and no second one: a solid picture with a stray pixel is a picture an
		// operator would read as a map fault.
		if got := countOf(frame, finishedWordBackground); got != 0 {
			t.Fatalf("%d pixels of the solid pattern are the background, want none", got)
		}

		if patternWhite != (wled.RGB{R: 255, G: 255, B: 255}) {
			t.Fatalf("the solid colour is %+v, want pure white", patternWhite)
		}
	})

	t.Run("border is the edge and nothing inside it", func(t *testing.T) {
		frame, err := patternFrame("border", geometry)
		if err != nil {
			t.Fatalf("draw the border pattern: %v", err)
		}

		if len(frame) != geometry.width*geometry.height {
			t.Fatalf("the border pattern is %d pixels, want the panel's %d", len(frame), geometry.width*geometry.height)
		}

		// Two pixels in from every side of a 256x64 panel: the top and bottom rows twice over,
		// plus four pixels in each of the sixty rows between them.
		const wantWhite = 2*256 + 2*256 + 4*(64-2*patternBorderThickness)
		if got := countOf(frame, patternWhite); got != wantWhite {
			t.Fatalf("%d pixels of the border are white, want %d", got, wantWhite)
		}

		for y := range geometry.height {
			for x := range geometry.width {
				edge := x < patternBorderThickness || y < patternBorderThickness ||
					x >= geometry.width-patternBorderThickness || y >= geometry.height-patternBorderThickness

				got := pixelAt(frame, geometry, x, y)

				if edge && got != patternWhite {
					t.Fatalf("the edge pixel (%d,%d) is %+v, want white", x, y, got)
				}

				if !edge && got != finishedWordBackground {
					t.Fatalf("the interior pixel (%d,%d) is %+v, want the background", x, y, got)
				}
			}
		}

		// The four corners and the four just inside them, named: a border that is a rectangle
		// reads closed here, and a border drawn as strips or on a diagonal does not.
		for _, corner := range []struct{ x, y int }{{0, 0}, {255, 0}, {0, 63}, {255, 63}, {1, 1}, {254, 62}} {
			if got := pixelAt(frame, geometry, corner.x, corner.y); got != patternWhite {
				t.Fatalf("the corner pixel (%d,%d) is %+v, want white", corner.x, corner.y, got)
			}
		}

		for _, inside := range []struct{ x, y int }{{2, 2}, {253, 2}, {2, 61}, {253, 61}, {128, 32}} {
			if got := pixelAt(frame, geometry, inside.x, inside.y); got != finishedWordBackground {
				t.Fatalf("the interior pixel (%d,%d) is %+v, want the background", inside.x, inside.y, got)
			}
		}
	})

	t.Run("quarters are four blocks meeting in the middle", func(t *testing.T) {
		frame, err := patternFrame("quarters", geometry)
		if err != nil {
			t.Fatalf("draw the quarters pattern: %v", err)
		}

		if len(frame) != geometry.width*geometry.height {
			t.Fatalf("the quarters pattern is %d pixels, want the panel's %d", len(frame), geometry.width*geometry.height)
		}

		const block = 128 * 32

		for _, shape := range []struct {
			what   string
			colour wled.RGB
		}{
			{"red", patternRed},
			{"green", patternGreen},
			{"blue", patternBlue},
			{"white", patternWhite},
		} {
			if got := countOf(frame, shape.colour); got != block {
				t.Fatalf("%d pixels are %s, want one quarter's %d", got, shape.what, block)
			}
		}

		// Exactly four colours and no fifth: an antialiased edge or a stray pixel would be a
		// picture whose corners an operator could not read.
		seen := map[wled.RGB]bool{}
		for _, pixel := range frame {
			seen[pixel] = true
		}

		if len(seen) != 4 {
			t.Fatalf("the quarters pattern holds %d colours, want 4: %+v", len(seen), seen)
		}

		// The corners name the answer this picture exists to give: which way up and which way
		// round. Red top-left, green top-right, blue bottom-left, white bottom-right.
		for _, corner := range []struct {
			x, y   int
			colour wled.RGB
			what   string
		}{
			{0, 0, patternRed, "red at the top left"},
			{255, 0, patternGreen, "green at the top right"},
			{0, 63, patternBlue, "blue at the bottom left"},
			{255, 63, patternWhite, "white at the bottom right"},
		} {
			if got := pixelAt(frame, geometry, corner.x, corner.y); got != corner.colour {
				t.Fatalf("the pixel (%d,%d) is %+v, want %s", corner.x, corner.y, got, corner.what)
			}
		}

		// And the seam is at the panel's half in both directions, so an operator counting
		// columns from the left finds the change where the picture says it is.
		for _, point := range []struct {
			x, y   int
			colour wled.RGB
			what   string
		}{
			{127, 31, patternRed, "red just above and left of the middle"},
			{128, 31, patternGreen, "green just above and right of the middle"},
			{127, 32, patternBlue, "blue just below and left of the middle"},
			{128, 32, patternWhite, "white just below and right of the middle"},
		} {
			if got := pixelAt(frame, geometry, point.x, point.y); got != point.colour {
				t.Fatalf("the pixel (%d,%d) is %+v, want %s", point.x, point.y, got, point.what)
			}
		}
	})

	t.Run("a name the ladder does not hold is refused", func(t *testing.T) {
		if frame, err := patternFrame("quaters", geometry); err == nil || frame != nil {
			t.Fatalf("the misspelt pattern drew %d pixels and no error, want a refusal", len(frame))
		}
	})
}

// postPattern sends a pattern request through the module's own routes and returns what came back.
func postPattern(t *testing.T, query string) *httptest.ResponseRecorder {
	t.Helper()

	recorder := httptest.NewRecorder()
	routes().ServeHTTP(recorder, forwarded(http.MethodPost, "/preview-pattern"+query, nil))

	return recorder
}

// postPreviewAt is the finished word's route with a query, which is how a test asks it for a hold
// other than its own minute.
func postPreviewAt(t *testing.T, query string) *httptest.ResponseRecorder {
	t.Helper()

	recorder := httptest.NewRecorder()
	routes().ServeHTTP(recorder, forwarded(http.MethodPost, "/preview-finished"+query, nil))

	return recorder
}

// problemDetail is the sentence a refusal carries beside its code.
func problemDetail(t *testing.T, recorder *httptest.ResponseRecorder) string {
	t.Helper()

	var problem struct {
		Detail string `json:"detail"`
	}

	if err := json.Unmarshal(recorder.Body.Bytes(), &problem); err != nil {
		t.Fatalf("the refusal is not JSON: %v: %s", err, recorder.Body.String())
	}

	return problem.Detail
}

// patternAnswer is the body of an accepted pattern.
type patternAnswer struct {
	Pattern      string `json:"pattern"`
	Controller   string `json:"controller"`
	Answers      string `json:"answers"`
	HoldsSeconds int    `json:"holds_seconds"`
}

// TestThePatternRouteDrawsThePatternAndHandsTheLightBack is the whole route in one test: the
// answer, the pixels that reached the transport, the hold running to its own length and no
// longer, the steady frame coming back, and the one read that proves the controller left realtime.
//
// The hold is fifteen seconds rather than the minute, because the route took a hold and this is
// the value the operator will type tomorrow; the deadline is moved on the writer's own clock, so
// the test asserts the length without waiting for it.
func TestThePatternRouteDrawsThePatternAndHandsTheLightBack(t *testing.T) {
	strip := newPanelStrip()

	clock := newFakeClock(time.Date(2026, time.November, 18, 22, 0, 0, 0, time.UTC))

	var written logs

	pen := newWriter(strip, 128, written.logf)
	pen.setPanel(strip.info)
	clock.watch(pen)

	pushed := pushInto(pen)

	forgetPreview()
	defer forgetPreview()

	offerPreview(&previewTarget{pen: pen, where: "http://10.0.0.136"})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go pen.run(ctx)

	// The light is already showing the steady picture, as it is when somebody walks up to it.
	steady := render.Frame{Colour: render.ColourIdle, Total: 16384, Reason: render.ReasonNoLiveJob}

	pen.submit(steady)
	strip.waitForPaints(t, 1)
	clock.waitArmed(t, 0)

	mark := clock.mark()
	recorder := postPattern(t, "?pattern=quarters&hold=15")

	if recorder.Code != http.StatusAccepted {
		t.Fatalf("the pattern answered %d, want %d: %s", recorder.Code, http.StatusAccepted, recorder.Body.String())
	}

	var answer patternAnswer
	if err := json.Unmarshal(recorder.Body.Bytes(), &answer); err != nil {
		t.Fatalf("the answer is not JSON: %v: %s", err, recorder.Body.String())
	}

	if answer.Pattern != "quarters" || answer.Controller != "http://10.0.0.136" || answer.HoldsSeconds != 15 {
		t.Fatalf("the answer is %+v, want quarters on http://10.0.0.136 for 15 seconds", answer)
	}

	if !strings.Contains(answer.Answers, "top-left") {
		t.Fatalf("the answer does not say what the picture answers: %q", answer.Answers)
	}

	want, err := patternFrame("quarters", panel{width: 256, height: 64})
	if err != nil {
		t.Fatalf("lay out the quarters pattern: %v", err)
	}

	// The pixels, then the count of paintings: the establishing body is recorded before the
	// pixels it makes room for, so a test that waited on it would read the panel too early.
	waitFor(t, "the pattern's frame to reach the transport", func() bool { return pushed.pushed() == 1 })

	if got := pushed.last(); !slices.Equal(got, want) {
		t.Fatalf("the frame pushed is not the quarters pattern: %d pixels, first %+v, want %d, first %+v",
			len(got), firstPixel(got), len(want), firstPixel(want))
	}

	// The establishing body carries the pattern's name, which is what /json/state reports while
	// the picture is up: a datagram carries no string, so the name is the body's doing.
	if got := paintings(strip); got != 1 {
		t.Fatalf("the pattern laid the panel down %d times, want once", got)
	}

	if shown := strip.shown(); len(shown) == 0 || len(shown[len(shown)-1].segments) == 0 ||
		shown[len(shown)-1].segments[0].Name != "quarters" {
		t.Fatalf("the establishing body does not name the pattern: %+v", strip.shown())
	}

	clock.waitArmed(t, mark)

	// The steady frame the driver keeps handing the writer waits in the slot: the pattern has the
	// light for the hold it was given and no longer.
	pen.submit(steady)

	// Fourteen of the fifteen seconds, a second at a time: the picture is pushed again every
	// second — a still picture that is not repeated is dropped by the controller's realtime
	// timeout — and the steady frame does not come back until the hold is over. The push count is
	// the thing waited on rather than the writer's own arm, because the repeat is what a repeat is.
	for second := 1; second <= 14; second++ {
		clock.Advance(pixelRepeat)

		wanted := second + 1
		waitFor(t, "the pattern to be pushed again", func() bool { return pushed.pushed() >= wanted })

		if got := paintings(strip); got != 1 {
			t.Fatalf("%d seconds in, the pattern had been laid down %d times, want once: a still picture is repeated, not rebuilt", second, got)
		}

		if shown := steadyPaints(strip); len(shown) != 1 {
			t.Fatalf("the steady frame came back %d seconds into a fifteen-second hold: %+v", second, shown)
		}
	}

	// The last second, and the light goes back.
	clock.Advance(time.Second)

	waitFor(t, "the steady frame to come back when the pattern's hold ends", func() bool {
		return len(steadyPaints(strip)) == 2
	})

	// The hand-back's only proof: the controller is read once, past its realtime timeout, and says
	// it has left live mode. Nothing has been pushed since the hold ended, so this is what the
	// firmware reports once its own timeout runs out. The wait is advanced in steps because the
	// writer arms it on its own goroutine, after the frame it follows.
	confirmed := strip.realtimeReads()

	strip.takeRealtime(false)

	advanceUntil(t, clock, realtimeTimeout+handbackMargin, "the hand-back to read what the controller says about realtime",
		func() bool { return strip.realtimeReads() > confirmed })

	lines := strings.Join(written.all(), "\n")

	if !strings.Contains(lines, "reports live=false") || !strings.Contains(lines, "back on its own segments") {
		t.Fatalf("the hand-back did not say the light came back: %q", lines)
	}
}

// advanceUntil moves a test's clock on in steps until ok holds.
//
// One Advance is not enough for the writer's waits that are armed after the test has moved the
// clock: the hand-back's wait is armed by the writer's own goroutine, a moment after the frame it
// follows, and an Advance that lands before it would leave a wait that never fires. Stepping until
// the fact appears is the same order the writer and the test actually run in.
func advanceUntil(t *testing.T, clock *fakeClock, step time.Duration, what string, ok func() bool) {
	t.Helper()

	deadline := time.Now().Add(howLong)

	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("waited %s for %s", howLong, what)
		}

		clock.Advance(step)
		time.Sleep(time.Millisecond)
	}
}

// firstPixel is the first colour of a frame, for a failure message that has to name what was
// pushed without printing 16,384 colours.
func firstPixel(frame []wled.RGB) wled.RGB {
	if len(frame) == 0 {
		return wled.RGB{}
	}

	return frame[0]
}

// TestAHoldIsAWholeNumberOfSecondsWithADefaultAndACeiling is the hold's rules, one case at a time.
//
// The default is the event's own minute, which is what the routes did before the parameter existed;
// a given hold is a whole number of seconds inside the ceiling; and everything else is refused by
// name rather than clamped, because a hold quietly shortened would be a look the caller was told he
// had and did not get.
func TestAHoldIsAWholeNumberOfSecondsWithADefaultAndACeiling(t *testing.T) {
	for _, shape := range []struct {
		query string
		want  time.Duration
		bad   bool
	}{
		{"", finishedEventHolds, false},
		{"?hold=", finishedEventHolds, false},
		{"?hold=15", 15 * time.Second, false},
		{"?hold=%20180%20", 3 * time.Minute, false},
		{"?hold=300", previewHoldCeiling, false},
		{"?hold=301", 0, true},
		{"?hold=0", 0, true},
		{"?hold=-5", 0, true},
		{"?hold=abc", 0, true},
		{"?hold=1.5", 0, true},
	} {
		t.Run("hold="+shape.query, func(t *testing.T) {
			r := forwarded(http.MethodPost, "/preview-finished"+shape.query, nil)

			got, err := holdFrom(r)

			if shape.bad {
				if err == nil {
					t.Fatalf("%q was accepted as a hold of %s, want a refusal", shape.query, got)
				}

				if !strings.Contains(err.Error(), "300") || !strings.Contains(err.Error(), "60") {
					t.Fatalf("the refusal does not name the ceiling and the default: %v", err)
				}

				return
			}

			if err != nil {
				t.Fatalf("%q was refused: %v", shape.query, err)
			}

			if got != shape.want {
				t.Fatalf("%q is a hold of %s, want %s", shape.query, got, shape.want)
			}
		})
	}
}

// TestBothPreviewRoutesTakeTheHoldAndRefusePastTheCeiling is the parameter at the route, on both
// of them, where a caller actually meets it: the accepted value is what the writer was given, and
// the refused one carries its own code and left the controller and the writer's slot alone.
func TestBothPreviewRoutesTakeTheHoldAndRefusePastTheCeiling(t *testing.T) {
	strip := newPanelStrip()

	pen := newWriter(strip, 128, quiet)
	pen.setPanel(strip.info)

	forgetPreview()
	defer forgetPreview()

	offerPreview(&previewTarget{pen: pen, where: "http://10.0.0.136"})

	for _, shape := range []struct {
		name   string
		path   string
		query  string
		status int
		code   string
		holds  time.Duration
	}{
		{
			name:   "the word keeps its own minute when no hold is given",
			path:   "/preview-finished",
			query:  "",
			status: http.StatusAccepted,
			holds:  finishedEventHolds,
		},
		{
			name:   "the word takes a hold",
			path:   "/preview-finished",
			query:  "?hold=180",
			status: http.StatusAccepted,
			holds:  3 * time.Minute,
		},
		{
			name:   "the word refuses a hold past the ceiling",
			path:   "/preview-finished",
			query:  "?hold=301",
			status: http.StatusBadRequest,
			code:   PreviewHoldOutOfRange.Code().String(),
		},
		{
			name:   "a pattern takes a hold",
			path:   "/preview-pattern",
			query:  "?pattern=border&hold=15",
			status: http.StatusAccepted,
			holds:  15 * time.Second,
		},
		{
			name:   "a pattern refuses a hold past the ceiling",
			path:   "/preview-pattern",
			query:  "?pattern=border&hold=301",
			status: http.StatusBadRequest,
			code:   PreviewHoldOutOfRange.Code().String(),
		},
	} {
		t.Run(shape.name, func(t *testing.T) {
			pen.fired.Store(nil)

			recorder := httptest.NewRecorder()
			routes().ServeHTTP(recorder, forwarded(http.MethodPost, shape.path+shape.query, nil))

			if recorder.Code != shape.status {
				t.Fatalf("%s answered %d, want %d: %s", shape.path+shape.query, recorder.Code, shape.status, recorder.Body.String())
			}

			if shape.code != "" {
				if got := problemCode(t, recorder); got != shape.code {
					t.Fatalf("the refusal carries %q, want %q", got, shape.code)
				}

				if detail := problemDetail(t, recorder); !strings.Contains(detail, "300") {
					t.Fatalf("the refusal does not name the ceiling: %q", detail)
				}

				if strip.wrote() != 0 {
					t.Fatalf("a refused hold wrote %d requests to the controller, want none", strip.wrote())
				}

				if fired := pen.fired.Load(); fired != nil {
					t.Fatalf("a refused hold stored the %q event in the writer's slot", fired.label())
				}

				return
			}

			fired := pen.fired.Load()
			if fired == nil {
				t.Fatalf("%s stored no event in the writer's slot", shape.path+shape.query)
			}

			if fired.holds != shape.holds {
				t.Fatalf("%s stored a hold of %s, want %s", shape.path+shape.query, fired.holds, shape.holds)
			}
		})
	}
}

// TestAnUnknownPatternIsRefusedAndTheThreeAreNamed is the mistake a person actually makes at the
// keyboard: a name that missed. The refusal is a 400 because nothing about the light is wrong, it
// carries a registered code, and its sentence lists all three names — so the fix is a retype and
// not a read of this repository.
func TestAnUnknownPatternIsRefusedAndTheThreeAreNamed(t *testing.T) {
	strip := newPanelStrip()

	pen := newWriter(strip, 128, quiet)
	pen.setPanel(strip.info)

	forgetPreview()
	defer forgetPreview()

	offerPreview(&previewTarget{pen: pen, where: "http://10.0.0.136"})

	for _, query := range []string{"", "?pattern=", "?pattern=quaters", "?pattern=SOLID"} {
		t.Run("pattern="+query, func(t *testing.T) {
			pen.fired.Store(nil)

			recorder := postPattern(t, query)

			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("the pattern answered %d, want %d: %s", recorder.Code, http.StatusBadRequest, recorder.Body.String())
			}

			if got := problemCode(t, recorder); got != PreviewPatternUnknown.Code().String() {
				t.Fatalf("the refusal carries %q, want %q", got, PreviewPatternUnknown.Code().String())
			}

			detail := problemDetail(t, recorder)

			for _, name := range patternNames() {
				if !strings.Contains(detail, name) {
					t.Fatalf("the refusal does not name %q: %q", name, detail)
				}
			}

			if strip.wrote() != 0 {
				t.Fatalf("a refused pattern wrote %d requests to the controller, want none", strip.wrote())
			}

			if fired := pen.fired.Load(); fired != nil {
				t.Fatalf("a refused pattern stored the %q event in the writer's slot", fired.label())
			}
		})
	}
}

// TestThePatternRouteRefusesWhereTheLightCannotTakeIt is the three cases the word's route already
// refuses, held for the ladder as well: no single light, a borrowed segment, and a strip.
//
// A pattern is pixels too, so all three mean what they mean for the word — and the borrowed one
// matters most, because these pictures are diagnostic and an operator trying to find a fault must
// not find it by lighting up somebody else's desk.
func TestThePatternRouteRefusesWhereTheLightCannotTakeIt(t *testing.T) {
	borrowed := newPanelStrip()
	borrowed.state = wled.State{
		On:         true,
		Brightness: 255,
		Segments: []wled.Segment{{
			ID:     2,
			Start:  14,
			Stop:   24,
			On:     true,
			Colors: []wled.RGB{{R: 8, G: 255, B: 0}},
			Effect: 102,
			Name:   "Front 1",
		}},
	}

	flat := newFakeStrip()

	for _, shape := range []struct {
		name       string
		target     func() *previewTarget
		status     int
		code       string
		controller counted
	}{
		{
			name: "a borrowed segment of somebody else's controller",
			target: func() *previewTarget {
				pen := newSegmentWriter(borrowed, 2, "http://10.0.0.253", quiet)
				pen.setPanel(borrowed.info)

				return &previewTarget{pen: pen, where: "http://10.0.0.253"}
			},
			status:     http.StatusConflict,
			code:       PreviewBorrowedSegment.Code().String(),
			controller: borrowed,
		},
		{
			name: "a strip",
			target: func() *previewTarget {
				pen := newWriter(flat, 128, quiet)

				return &previewTarget{pen: pen, where: "http://10.0.0.253"}
			},
			status:     http.StatusConflict,
			code:       PreviewNotDrawable.Code().String(),
			controller: flat,
		},
		{
			name:   "no single light is being driven",
			target: func() *previewTarget { return nil },
			status: http.StatusServiceUnavailable,
			code:   PreviewNotRunning.Code().String(),
		},
	} {
		t.Run(shape.name, func(t *testing.T) {
			forgetPreview()

			if target := shape.target(); target != nil {
				offerPreview(target)
			}

			defer forgetPreview()

			recorder := postPattern(t, "?pattern=solid&hold=15")

			if recorder.Code != shape.status {
				t.Fatalf("the pattern answered %d, want %d: %s", recorder.Code, shape.status, recorder.Body.String())
			}

			if got := problemCode(t, recorder); got != shape.code {
				t.Fatalf("the refusal carries %q, want %q", got, shape.code)
			}

			if shape.controller != nil && shape.controller.wrote() != 0 {
				t.Fatalf("a refused pattern wrote %d requests to the controller, want none", shape.controller.wrote())
			}

			if what, waiting := anyFired(); waiting {
				t.Fatalf("a refused pattern stored the %q event in a writer's slot", what)
			}
		})
	}
}

// stripUnderAPattern is the operator's second light as the ladder has to treat it: a controller that
// reported no matrix and said it drives forty-eight LEDs.
//
// It is built the way the watch builds one — a writer, and setPanel with what Info answered — because
// the length a strip is drawn against comes from that call and nowhere else. A strip the module has
// measured nothing of is a light with no geometry, and a picture asked of it is still refused.
func stripUnderAPattern(t *testing.T) (*fakeStrip, *pushedFrames) {
	t.Helper()

	strip := newFakeStrip() // forty-eight LEDs and no matrix, which is the strip on the desk

	info, err := strip.Info(context.Background())
	if err != nil {
		t.Fatalf("read the strip's geometry: %v", err)
	}

	pen := newWriter(strip, 128, quiet)
	pen.setPanel(info)

	pushed := pushInto(pen)

	forgetPreview()
	t.Cleanup(forgetPreview)

	offerPreview(&previewTarget{pen: pen, where: "http://10.0.0.253", light: "10.0.0.253"})

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	go pen.run(ctx)

	return strip, pushed
}

// TestQuartersOnAStripIsFourBandsOfTwelve is the degradation the ladder has to have: a strip is one
// run of pixels, so four quadrants are not a picture it can show, and four bands along the run are
// the same question asked of it.
//
// Before this, the route refused a strip outright with PREVIEW_NOT_DRAWABLE — so on an install with
// a panel and a strip, the light that is hardest to diagnose was the one the diagnostic could not
// reach. What it must not do now is fault, and what it must draw is twelve pixels of each colour in
// the panel's own order: red at the start, then green, then blue, then white at the far end.
func TestQuartersOnAStripIsFourBandsOfTwelve(t *testing.T) {
	_, pushed := stripUnderAPattern(t)

	recorder := postPattern(t, "?pattern=quarters&hold=8")

	if recorder.Code != http.StatusAccepted {
		t.Fatalf("the pattern answered %d, want %d: %s", recorder.Code, http.StatusAccepted, recorder.Body.String())
	}

	var answer patternAnswer
	if err := json.Unmarshal(recorder.Body.Bytes(), &answer); err != nil {
		t.Fatalf("the answer is not JSON: %v: %s", err, recorder.Body.String())
	}

	// The sentence is the strip's own: a run has two ends, not four corners, and telling a person to
	// look at the corners of his strip would be a sentence about a picture that is not on it.
	if !strings.Contains(answer.Answers, "start") || strings.Contains(answer.Answers, "top-left") {
		t.Fatalf("the answer describes the run rather than a panel's corners: %q", answer.Answers)
	}

	want, err := patternFrame("quarters", panel{width: 48, height: 1, run: true})
	if err != nil {
		t.Fatalf("lay out the quarters pattern on the run: %v", err)
	}

	if len(want) != 48 {
		t.Fatalf("the run's picture is %d pixels, want 48", len(want))
	}

	waitFor(t, "the strip's picture to reach the transport", func() bool { return pushed.pushed() >= 1 })

	got := pushed.last()

	if len(got) != 48 {
		t.Fatalf("the strip was pushed %d pixels, want its 48: a frame of another length is a light drawn short or past its end", len(got))
	}

	bands := []struct {
		name   string
		from   int
		to     int
		colour wled.RGB
	}{
		{"red", 0, 12, patternRed},
		{"green", 12, 24, patternGreen},
		{"blue", 24, 36, patternBlue},
		{"white", 36, 48, patternWhite},
	}

	for _, band := range bands {
		for pixel := band.from; pixel < band.to; pixel++ {
			if got[pixel] != band.colour {
				t.Fatalf("pixel %d is %+v, want the %s band's %+v", pixel, got[pixel], band.name, band.colour)
			}
		}
	}
}

// TestBorderOnAStripIsItsTwoEnds is the other rung that has to degrade: a rectangle's edge is not a
// shape a run can show, and the run's answer to the same question is where it begins and ends.
func TestBorderOnAStripIsItsTwoEnds(t *testing.T) {
	_, pushed := stripUnderAPattern(t)

	if recorder := postPattern(t, "?pattern=border&hold=8"); recorder.Code != http.StatusAccepted {
		t.Fatalf("the border pattern answered %d: %s", recorder.Code, recorder.Body.String())
	}

	waitFor(t, "the strip's picture to reach the transport", func() bool { return pushed.pushed() >= 1 })

	got := pushed.last()

	if len(got) != 48 || got[0] != patternWhite || got[47] != patternWhite {
		t.Fatalf("the run's edge is %d pixels with %+v and %+v at its ends, want white at both ends of 48",
			len(got), got[0], got[len(got)-1])
	}

	for pixel := 1; pixel < 47; pixel++ {
		if got[pixel] != finishedWordBackground {
			t.Fatalf("pixel %d inside the run is %+v, want the background", pixel, got[pixel])
		}
	}
}
