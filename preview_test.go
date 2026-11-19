package main

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
	"github.com/nothingmn/wled-printer-status/internal/text"
	"github.com/nothingmn/wled-printer-status/internal/wled"
)

// The preview route: POST /preview-finished, the request that lets a person look at the finished
// word without waiting for a print to end.
//
// It is the same event the printer's own transition fires, so what these tests prove is that the
// route reaches the writer, that the writer paints the word and hands the light back at the end of
// the hold, and that the three cases in which the light cannot be asked for the word are refused
// before anything is stored — the last of which is the one that matters, because a preview that
// put an animation on somebody else's light would be exactly what the event's own rule exists to
// prevent.

// previewAnswer is the body of an accepted preview.
type previewAnswer struct {
	Event        string     `json:"event"`
	Controller   string     `json:"controller"`
	Colour       colourView `json:"colour"`
	Scale        int        `json:"scale"`
	HoldsSeconds int        `json:"holds_seconds"`
}

// postPreview sends the preview request through the module's own routes and returns what came
// back.
func postPreview(t *testing.T) *httptest.ResponseRecorder {
	t.Helper()

	recorder := httptest.NewRecorder()
	routes().ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/preview-finished", nil))

	return recorder
}

// anyFired is any light's writer still holding an event in its slot, and what that event is
// called. A refusal has to leave nothing waiting — no picture may arrive a moment after the caller
// was told the light would not be given one — and with several lights offered the question is about
// all of them.
func anyFired() (string, bool) {
	for _, target := range previewsNow() {
		if fired := target.pen.fired.Load(); fired != nil {
			return fired.label(), true
		}
	}

	return "", false
}

// problemCode is the registered code of a refusal, which is what a client branches on.
func problemCode(t *testing.T, recorder *httptest.ResponseRecorder) string {
	t.Helper()

	var problem struct {
		Code string `json:"code"`
	}

	if err := json.Unmarshal(recorder.Body.Bytes(), &problem); err != nil {
		t.Fatalf("the refusal is not JSON: %v: %s", err, recorder.Body.String())
	}

	return problem.Code
}

// TestThePreviewPutsTheWordOnThePanelForItsOwnDuration is the whole of the route in one test: the
// answer, the pixels, the hold, and the steady frame coming back.
//
// The word it fires is finishedWordEvent — the same value eventFor returns — so the pixels it
// paints are the word's own, and the frame that replaces it at the end of the minute is the
// picture the light would have been showing had nobody asked.
func TestThePreviewPutsTheWordOnThePanelForItsOwnDuration(t *testing.T) {
	strip := newPanelStrip()

	clock := newFakeClock(time.Date(2026, time.November, 18, 22, 0, 0, 0, time.UTC))
	pen := newWriter(strip, 128, quiet)
	pen.setPanel(strip.info)
	clock.watch(pen)

	pushed := pushInto(pen)

	forgetPreview()
	defer forgetPreview()

	offerPreview(&previewTarget{pen: pen, where: "http://10.0.0.136"})
	defer forgetPreview()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go pen.run(ctx)

	// The light is already showing the steady picture, as it is when somebody walks up to it.
	steady := render.Frame{Colour: render.ColourIdle, Total: 16384, Reason: render.ReasonNoLiveJob}

	pen.submit(steady)
	strip.waitForPaints(t, 1)
	clock.waitArmed(t, 0)

	mark := clock.mark()
	recorder := postPreview(t)

	if recorder.Code != http.StatusAccepted {
		t.Fatalf("the preview answered %d, want %d: %s", recorder.Code, http.StatusAccepted, recorder.Body.String())
	}

	var answer previewAnswer
	if err := json.Unmarshal(recorder.Body.Bytes(), &answer); err != nil {
		t.Fatalf("the answer is not JSON: %v: %s", err, recorder.Body.String())
	}

	if answer.Event != finishedWord || answer.Controller != "http://10.0.0.136" ||
		answer.Scale != finishedWordScale || answer.HoldsSeconds != int(finishedEventHolds/time.Second) {
		t.Fatalf("the answer is %+v, want the word %q on %s at scale %d for %d seconds",
			answer, finishedWord, "http://10.0.0.136", finishedWordScale, int(finishedEventHolds/time.Second))
	}

	if answer.Colour != (colourView{R: render.ColourFinished.R, G: render.ColourFinished.G, B: render.ColourFinished.B}) {
		t.Fatalf("the answer says the word is drawn in %+v, want the finished colour", answer.Colour)
	}

	want, err := text.Raster(finishedWord, finishedWordScale, text.Geometry{Width: 256, Height: 64})
	if err != nil {
		t.Fatalf("rasterise %q: %v", finishedWord, err)
	}

	// The pixels, then the count of paintings: the establishing body is recorded before the pixels
	// it makes room for, so a test that waited on it would read the panel too early.
	waitFor(t, "the previewed word to reach the panel", func() bool {
		return len(wordPixels(pushed)) == len(want)
	})

	if got := wordPixels(pushed); !slices.Equal(got, want) {
		t.Fatalf("the preview left %d green pixels, want the word's %d", len(got), len(want))
	}

	clock.waitArmed(t, mark)

	// The steady frame the driver keeps handing the writer waits in the slot: the word has the
	// light for its own duration and no longer.
	mark = clock.mark()
	pen.submit(steady)
	clock.waitArmed(t, mark)

	if shown := steadyPaints(strip); len(shown) != 1 {
		t.Fatalf("the steady frame was written %d times during the preview, want the 1 before it: %+v", len(shown), shown)
	}

	clock.Advance(finishedEventHolds)

	waitFor(t, "the steady frame to come back when the preview's hold ends", func() bool {
		return len(steadyPaints(strip)) == 2
	})

	if got := paintings(strip); got != 1 {
		t.Fatalf("the preview laid the word down %d times, want once", got)
	}
}

// TestThePreviewRefusesWhereTheLightCannotCarryTheWord is the safe half, one case at a time.
//
// Each refusal carries a registered code, and in each the controller took nothing: the check is
// before the event is stored, so a refused preview cannot leave a picture waiting in the writer's
// slot to be painted a moment later — which is what "refused" has to mean on a light somebody else
// is using.
func TestThePreviewRefusesWhereTheLightCannotCarryTheWord(t *testing.T) {
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

	short := newPanelStrip()
	short.info = wled.Info{LEDCount: 2048, MatrixWidth: 64, MatrixHeight: 32}

	strip := newFakeStrip()

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
				pen := newWriter(strip, 128, quiet)

				return &previewTarget{pen: pen, where: "http://10.0.0.253"}
			},
			status:     http.StatusConflict,
			code:       PreviewNotDrawable.Code().String(),
			controller: strip,
		},
		{
			name: "a panel too small for the word",
			target: func() *previewTarget {
				pen := newWriter(short, 128, quiet)
				pen.setPanel(short.info)

				return &previewTarget{pen: pen, where: "http://10.0.0.136"}
			},
			status:     http.StatusConflict,
			code:       PreviewNotDrawable.Code().String(),
			controller: short,
		},
		{
			name:   "no light is being driven at all",
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

			recorder := postPreview(t)

			if recorder.Code != shape.status {
				t.Fatalf("the preview answered %d, want %d: %s", recorder.Code, shape.status, recorder.Body.String())
			}

			if got := problemCode(t, recorder); got != shape.code {
				t.Fatalf("the refusal carries %q, want %q", got, shape.code)
			}

			if shape.controller != nil && shape.controller.wrote() != 0 {
				t.Fatalf("a refused preview wrote %d requests to the controller, want none", shape.controller.wrote())
			}

			// And nothing is left waiting on any light: every writer's slot is empty, so no
			// picture arrives a moment after the caller was told none would be given one.
			if what, waiting := anyFired(); waiting {
				t.Fatalf("a refused preview stored the %q event in a writer's slot", what)
			}
		})
	}
}

// twoLightWatch starts a watch driving two bindings on two controllers, and answers the clients it
// drives by address.
//
// The addresses are loopback on purpose: a preview pushes its picture as datagrams, and a test
// inside the container must not be able to reach a light on the operator's wall. Nothing listens on
// this process's own 4048, so the datagrams go nowhere and the run still goes through every step
// the real one does.
func twoLightWatch(t *testing.T) map[string]strip {
	t.Helper()

	return runTwoLightWatch(t, map[string]strip{
		"http://127.0.0.1:9":  newPanelStrip(),
		"http://127.0.0.1:10": newPanelStrip(),
	})
}

// runTwoLightWatch is the same watch over whatever clients a test hands it, so that a mixed install
// — a panel in one room and a strip in the other — is the same harness as two panels.
func runTwoLightWatch(t *testing.T, lights map[string]strip) map[string]strip {
	t.Helper()

	// Whatever a previous test offered is taken away first, so that what this watch offers is the
	// only thing the routes can see.
	forgetPreview()

	ctx, stop := context.WithCancel(context.Background())
	t.Cleanup(stop)

	watch := &runner{
		newSource: func(b binding) (watchSource, error) {
			return &fixedSource{snapshot: printingJob(b.DeviceID, 50)}, nil
		},
		newLight: func(address string) (strip, error) { return lights[address], nil },
		logf:     quiet,
		// Short, because the geometry a preview is drawn at is what the driver learns on its first
		// turn: a watch that has not turned yet is a light whose length nobody has asked for, and
		// the route says so rather than guessing. The wait below is for that turn.
		tick:  20 * time.Millisecond,
		grace: 2 * time.Second,
		// The picture a composed preview reports is drawn at the arrangement this writer composes
		// with, and a runner built without one has no bands, so the route answers with an empty
		// name and no halves for a picture it did draw. What the instance composes with when
		// nobody has written a layout is defaultLayout, so that is what this watch runs with.
		layout: defaultLayout(),
	}

	start := bindings{
		{ID: "printer-a", DeviceID: "printer-a", Address: "http://127.0.0.1:9", Segment: -1, Brightness: 128},
		{ID: "printer-b", DeviceID: "printer-b", Address: "http://127.0.0.1:10", Segment: -1, Brightness: 128},
	}

	if err := watch.apply(ctx, start); err != nil {
		t.Fatalf("the watch did not start: %v", err)
	}

	t.Cleanup(func() {
		watch.stop()
		forgetBindings()
		forgetPreview()
	})

	// A preview is drawn at the geometry the controller reported, and the driver asks for it on its
	// first pass. Waiting for that read is what makes the test about the ladder rather than about
	// which goroutine got there first — the module's own window between starting and knowing how
	// long a strip is, which the refusal names.
	for address, light := range lights {
		panel, isPanel := light.(*panelStrip)
		if !isPanel {
			continue
		}

		waitFor(t, "the controller at "+address+" to say what it drives", func() bool {
			return panel.reads.Load() > 0
		})
	}

	return lights
}

// testTheOfferIsEveryLight is the defect this wave fixed, as one assertion: a watch holding two
// bindings offered the preview routes *nothing at all*, so every diagnostic rung answered
// PREVIEW_NOT_RUNNING and an install with two lights had no instrument at all.
func TestATwoLightWatchOffersEveryLightToThePreviews(t *testing.T) {
	twoLightWatch(t)

	offered := previewsNow()
	if len(offered) != 2 {
		t.Fatalf("a watch driving two bindings offered the previews %d lights, want 2: this is the "+
			"PREVIEW_NOT_RUNNING the two-light install met", len(offered))
	}

	names := []string{offered[0].light, offered[1].light}
	for _, want := range []string{"127.0.0.1:9", "127.0.0.1:10"} {
		if !slices.Contains(names, want) {
			t.Fatalf("the offered lights are named %v, want %q among them", names, want)
		}
	}
}

// TestThePatternLadderRunsOnEveryLightAnInstanceDrives is the rung the operator could not fire: one
// request, two lights, and both of them told to draw the picture.
func TestThePatternLadderRunsOnEveryLightAnInstanceDrives(t *testing.T) {
	twoLightWatch(t)

	recorder := httptest.NewRecorder()
	routes().ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/preview-pattern?pattern=quarters&hold=8", nil))

	if recorder.Code != http.StatusAccepted {
		t.Fatalf("the pattern answered %d, want %d: %s", recorder.Code, http.StatusAccepted, recorder.Body.String())
	}

	var answer struct {
		Pattern    string         `json:"pattern"`
		Count      int            `json:"count"`
		Lights     []previewPaint `json:"lights"`
		Controller string         `json:"controller"`
	}

	if err := json.Unmarshal(recorder.Body.Bytes(), &answer); err != nil {
		t.Fatalf("the answer is not JSON: %v: %s", err, recorder.Body.String())
	}

	if answer.Pattern != "quarters" || answer.Count != 2 || len(answer.Lights) != 2 {
		t.Fatalf("the answer is %+v, want quarters on 2 lights", answer)
	}

	// `controller` is the one-light field, and it is absent when there is no one controller to
	// name: an answer that filled it would be naming one of two lights as though the picture had
	// gone only there.
	if answer.Controller != "" {
		t.Fatalf("the answer names one controller %q for a two-light preview", answer.Controller)
	}

	seen := make([]string, 0, 2)

	for _, one := range answer.Lights {
		if one.Refused != "" {
			t.Fatalf("the light at %s refused the pattern: %s", one.Controller, one.Why)
		}

		seen = append(seen, one.Controller)
	}

	slices.Sort(seen)

	if want := []string{"http://127.0.0.1:10", "http://127.0.0.1:9"}; !slices.Equal(seen, want) {
		t.Fatalf("the picture went to %v, want both lights %v", seen, want)
	}
}

// TestAPreviewCanBeAimedAtOneLight is the other half: a request that names a light gets that light,
// by the name the routes beside these use, and nothing else.
func TestAPreviewCanBeAimedAtOneLight(t *testing.T) {
	twoLightWatch(t)

	recorder := httptest.NewRecorder()
	routes().ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/preview-pattern?pattern=solid&light=127.0.0.1:10", nil))

	if recorder.Code != http.StatusAccepted {
		t.Fatalf("the aimed pattern answered %d, want %d: %s", recorder.Code, http.StatusAccepted, recorder.Body.String())
	}

	var answer struct {
		Count      int            `json:"count"`
		Lights     []previewPaint `json:"lights"`
		Controller string         `json:"controller"`
	}

	if err := json.Unmarshal(recorder.Body.Bytes(), &answer); err != nil {
		t.Fatalf("the answer is not JSON: %v: %s", err, recorder.Body.String())
	}

	if answer.Count != 1 || len(answer.Lights) != 1 || answer.Lights[0].Controller != "http://127.0.0.1:10" {
		t.Fatalf("the aimed pattern is %+v, want one light at http://127.0.0.1:10", answer)
	}

	// One light resolved, so the single-controller field is true again and is filled.
	if answer.Controller != "http://127.0.0.1:10" {
		t.Fatalf("the answer names %q, want the light that was asked for", answer.Controller)
	}

	// A name this instance drives nothing on is refused by name, and the refusal lists where the
	// names are — not answered with a default light.
	recorder = httptest.NewRecorder()
	routes().ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/preview-pattern?pattern=solid&light=morpheus", nil))

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("an unknown light answered %d, want %d: %s", recorder.Code, http.StatusNotFound, recorder.Body.String())
	}

	if got := problemCode(t, recorder); got != ControllerNotFound.Code().String() {
		t.Fatalf("the refusal carries %q, want %q", got, ControllerNotFound.Code().String())
	}
}

// TestAPreviewWithNoLightBoundSaysThat is the one refusal left: an instance driving nothing has no
// light to put a picture on, and the sentence has to say *that* rather than talking about "exactly
// one light", which described the old rule and left a two-light install reading a message about a
// situation it was not in.
func TestAPreviewWithNoLightBoundSaysThat(t *testing.T) {
	forgetPreview()
	t.Cleanup(forgetPreview)

	recorder := httptest.NewRecorder()
	routes().ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/preview-pattern?pattern=solid", nil))

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("the pattern answered %d, want %d: %s", recorder.Code, http.StatusServiceUnavailable, recorder.Body.String())
	}

	if got := problemCode(t, recorder); got != PreviewNotRunning.Code().String() {
		t.Fatalf("the refusal carries %q, want %q", got, PreviewNotRunning.Code().String())
	}

	var problem struct {
		Detail string `json:"detail"`
	}

	if err := json.Unmarshal(recorder.Body.Bytes(), &problem); err != nil {
		t.Fatalf("the refusal is not JSON: %v: %s", err, recorder.Body.String())
	}

	if !strings.Contains(problem.Detail, "no light at all") {
		t.Fatalf("the refusal reads %q, want it to say no light is being driven", problem.Detail)
	}

	if strings.Contains(problem.Detail, "exactly one") {
		t.Fatalf("the refusal still talks about exactly one light: %q", problem.Detail)
	}
}

// TestAWordPreviewPaintsThePanelsAndSaysWhichLightCannotCarryIt is the case a two-light install with
// a strip in it meets: the word cannot be drawn on one run of pixels, and that is a fact about one
// light rather than about the request.
//
// Before this, the whole request failed with PREVIEW_NOT_DRAWABLE and the panel got nothing — so the
// one light that could show the word was taken away by the one that could not. The answer now names
// both: the panel took it, the strip did not, and the code and the sentence say which the strip is.
func TestAWordPreviewPaintsThePanelsAndSaysWhichLightCannotCarryIt(t *testing.T) {
	runTwoLightWatch(t, map[string]strip{
		"http://127.0.0.1:9":  newPanelStrip(),
		"http://127.0.0.1:10": newFakeStrip(),
	})

	recorder := httptest.NewRecorder()
	routes().ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/preview-finished?hold=8", nil))

	if recorder.Code != http.StatusAccepted {
		t.Fatalf("the word answered %d, want %d: %s", recorder.Code, http.StatusAccepted, recorder.Body.String())
	}

	var answer struct {
		Count  int            `json:"count"`
		Lights []previewPaint `json:"lights"`
	}

	if err := json.Unmarshal(recorder.Body.Bytes(), &answer); err != nil {
		t.Fatalf("the answer is not JSON: %v: %s", err, recorder.Body.String())
	}

	if answer.Count != 2 || len(answer.Lights) != 2 {
		t.Fatalf("the answer names %d lights, want both: %+v", answer.Count, answer)
	}

	byController := map[string]previewPaint{}
	for _, one := range answer.Lights {
		byController[one.Controller] = one
	}

	panel, held := byController["http://127.0.0.1:9"]
	if !held || panel.Refused != "" {
		t.Fatalf("the panel's entry is %+v, want the card taken and no refusal", panel)
	}

	strip, held := byController["http://127.0.0.1:10"]
	if !held || strip.Refused != PreviewNotDrawable.Code().String() {
		t.Fatalf("the strip's entry is %+v, want it refused with %q", strip, PreviewNotDrawable.Code().String())
	}

	if strip.Why == "" {
		t.Fatal("the strip's refusal carries no sentence saying why")
	}
}

// TestTheComposedPreviewNamesWhatEachLightWillDraw is the fourth route on two lights: the picture goes
// to both, and each entry says the name that light will cut and the halves its rotation shows —
// which are per light, because a name that fits one panel is cut differently on another.
func TestTheComposedPreviewNamesWhatEachLightWillDraw(t *testing.T) {
	runTwoLightWatch(t, map[string]strip{
		"http://127.0.0.1:9":  newPanelStrip(),
		"http://127.0.0.1:10": newPanelStrip(),
	})

	recorder := httptest.NewRecorder()
	routes().ServeHTTP(recorder,
		httptest.NewRequest(http.MethodPost, "/preview-frame?percent=41&name=MK4IS&hold=8", nil))

	if recorder.Code != http.StatusAccepted {
		t.Fatalf("the composed picture answered %d, want %d: %s", recorder.Code, http.StatusAccepted, recorder.Body.String())
	}

	var answer struct {
		Count      int              `json:"count"`
		Lights     []map[string]any `json:"lights"`
		Controller string           `json:"controller"`
		Percent    int              `json:"percent"`
	}

	if err := json.Unmarshal(recorder.Body.Bytes(), &answer); err != nil {
		t.Fatalf("the answer is not JSON: %v: %s", err, recorder.Body.String())
	}

	if answer.Count != 2 || len(answer.Lights) != 2 || answer.Percent != 41 {
		t.Fatalf("the answer is %+v, want 41%% drawn on 2 lights", answer)
	}

	if answer.Controller != "" {
		t.Fatalf("the answer names one controller %q for a two-light picture", answer.Controller)
	}

	for _, one := range answer.Lights {
		if refused, _ := one["refused"].(string); refused != "" {
			t.Fatalf("the light at %v refused the composed picture: %v", one["controller"], one["why"])
		}

		if halves, _ := one["halves"].([]any); len(halves) == 0 {
			t.Fatalf("the light at %v was told nothing about its rotation: %v", one["controller"], one)
		}

		if name, _ := one["name"].(string); name != "MK4IS" {
			t.Fatalf("the light at %v will draw the name %q, want MK4IS", one["controller"], name)
		}
	}
}
