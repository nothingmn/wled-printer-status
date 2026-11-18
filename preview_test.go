package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
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

			// And nothing is left waiting: the writer's slot is empty, so no picture arrives a
			// moment after the caller was told the light would not be given one.
			if target := previewNow(); target != nil && target.pen.fired.Load() != nil {
				t.Fatalf("a refused preview stored the %q event in the writer's slot", target.pen.fired.Load().word)
			}
		})
	}
}
