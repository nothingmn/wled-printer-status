package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nothingmn/wled-printer-status/internal/printer"
	"github.com/nothingmn/wled-printer-status/internal/render"
	"github.com/nothingmn/wled-printer-status/internal/wled"
)

// howLong is how long a test waits for a goroutine to reach the client before it
// gives up. It is long enough that a loaded machine does not fail the suite and
// short enough that a writer which never sends fails it quickly.
const howLong = 5 * time.Second

// paintCall is one write a test's controller received.
type paintCall struct {
	solid  bool
	first  int
	count  int
	colour wled.RGB
}

// fakeStrip stands in for a WLED controller. It records what it was asked to
// show, and can be made to block or fail so that a test can drive the writer's
// timing without a network.
type fakeStrip struct {
	mu       sync.Mutex
	paints   []paintCall
	settled  int
	failNext int
	hold     chan struct{}
	painted  chan struct{}
}

func newFakeStrip() *fakeStrip {
	return &fakeStrip{painted: make(chan struct{}, 16)}
}

func (f *fakeStrip) Info(context.Context) (wled.Info, error) {
	return wled.Info{LEDCount: 48}, nil
}

func (f *fakeStrip) SetBrightness(context.Context, int) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.settled++

	return nil
}

func (f *fakeStrip) SetPower(context.Context, bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.settled++

	return nil
}

func (f *fakeStrip) SetSolid(ctx context.Context, colour wled.RGB, _ ...wled.SolidOption) error {
	return f.record(ctx, paintCall{solid: true, colour: colour})
}

func (f *fakeStrip) SetRange(ctx context.Context, first, count int, colour wled.RGB, _ ...wled.RangeOption) error {
	return f.record(ctx, paintCall{first: first, count: count, colour: colour})
}

// record is one write: what was asked for, then however long the controller takes
// to answer, then whether it answered.
func (f *fakeStrip) record(ctx context.Context, call paintCall) error {
	f.mu.Lock()

	failed := f.failNext > 0
	if failed {
		f.failNext--
	}

	f.paints = append(f.paints, call)
	hold := f.hold

	f.mu.Unlock()

	select {
	case f.painted <- struct{}{}:
	default:
	}

	if hold != nil {
		select {
		case <-hold:
		case <-ctx.Done():
			return fmt.Errorf("waiting on the controller: %w", ctx.Err())
		}
	}

	if failed {
		return errors.New("the controller did not answer in time")
	}

	return nil
}

// shown is what the controller was asked to paint, as a copy.
func (f *fakeStrip) shown() []paintCall {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]paintCall(nil), f.paints...)
}

// waitForPaints waits until the controller has been asked for at least count
// writes, and fails the test if it never is.
func (f *fakeStrip) waitForPaints(t *testing.T, count int) {
	t.Helper()

	deadline := time.After(howLong)

	for {
		if len(f.shown()) >= count {
			return
		}

		select {
		case <-f.painted:
		case <-deadline:
			t.Fatalf("the controller was asked for %d writes in %s, wanted %d",
				len(f.shown()), howLong, count)
		}
	}
}

// quiet is a logger for tests that do not read what was logged.
func quiet(string, ...any) {}

// frame is one frame with nothing filled, so that one frame is one write to the
// controller and a test can count them.
func frame(colour wled.RGB, reason render.Reason) render.Frame {
	return render.Frame{Colour: colour, Lit: 0, Total: 48, Reason: reason}
}

// TestAChangedFrameReachesTheLightAndAnUnchangedOneDoesNot is the whole point of
// the comparison: a frame that shows what the light already shows is not sent.
func TestAChangedFrameReachesTheLightAndAnUnchangedOneDoesNot(t *testing.T) {
	strip := newFakeStrip()
	pen := newWriter(strip, 128, quiet)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go pen.run(ctx)

	pen.submit(frame(render.ColourPrinting, render.ReasonProgress))
	strip.waitForPaints(t, 1)

	// The same picture, decided again: the light must not be written again.
	pen.submit(frame(render.ColourPrinting, render.ReasonProgress))
	// A different picture, which must reach the light.
	pen.submit(frame(render.ColourError, render.ReasonNoLiveJob))

	strip.waitForPaints(t, 2)

	// Give the writer room to send anything it was going to send, so that a
	// duplicate would have arrived by now.
	time.Sleep(50 * time.Millisecond)

	shown := strip.shown()
	if len(shown) != 2 {
		t.Fatalf("the light was written %d times, want 2: %+v", len(shown), shown)
	}

	if shown[0].colour != render.ColourPrinting || shown[1].colour != render.ColourError {
		t.Fatalf("the light was shown %+v, want printing then error", shown)
	}
}

// TestTheNewestFrameWinsOverOnesStillWaiting holds the light shut, feeds it three
// frames, and checks that the middle one is dropped rather than queued: a slow
// controller gets the newest picture, not a backlog.
func TestTheNewestFrameWinsOverOnesStillWaiting(t *testing.T) {
	strip := newFakeStrip()
	strip.hold = make(chan struct{})

	pen := newWriter(strip, 128, quiet)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go pen.run(ctx)

	// The first frame is taken by the writer and held inside the controller.
	pen.submit(frame(render.ColourPrinting, render.ReasonProgress))
	strip.waitForPaints(t, 1)

	// Both of these arrive while the writer is inside that write. Only the newest
	// may survive.
	pen.submit(frame(render.ColourIdle, render.ReasonNoLiveJob))
	pen.submit(frame(render.ColourError, render.ReasonDisconnected))

	close(strip.hold)

	strip.waitForPaints(t, 2)
	time.Sleep(50 * time.Millisecond)

	shown := strip.shown()
	if len(shown) != 2 {
		t.Fatalf("the light was written %d times, want 2: %+v", len(shown), shown)
	}

	if shown[0].colour != render.ColourPrinting {
		t.Fatalf("the first write was %+v, want the frame already being written", shown[0])
	}

	if shown[1].colour != render.ColourError {
		t.Fatalf("the second write was %+v, want the newest frame and not the one before it", shown[1])
	}
}

// TestAFailedWriteIsLoggedAndRetried is the failure the host turns into a dead
// module: a controller that does not answer must cost a log line and a retry, and
// nothing else — in particular not the last known state, which is what the light is
// showing until the retry lands.
func TestAFailedWriteIsLoggedAndRetried(t *testing.T) {
	strip := newFakeStrip()
	strip.failNext = 1

	var (
		mu    sync.Mutex
		lines []string
	)

	pen := newWriter(strip, 128, func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()

		lines = append(lines, fmt.Sprintf(format, args...))
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go pen.run(ctx)

	pen.submit(frame(render.ColourPrinting, render.ReasonProgress))
	strip.waitForPaints(t, 1)

	// The same picture again. It must be sent, because the first attempt never
	// reached the light: suppressing it would leave the strip wrong until the
	// printer changed.
	pen.submit(frame(render.ColourPrinting, render.ReasonProgress))
	strip.waitForPaints(t, 2)

	mu.Lock()
	logged := strings.Join(lines, "\n")
	mu.Unlock()

	if !strings.Contains(logged, "not updated") {
		t.Fatalf("the failed write was not logged; the log reads %q", logged)
	}

	if ctx.Err() != nil {
		t.Fatal("the writer stopped on a failed write, and a module that exits is a module that needs a person")
	}

	last := sent.Load()
	if last == nil || !last.OK {
		t.Fatalf("the last write is reported as %+v, want the retry reported as taken", last)
	}

	if last.Error != "" {
		t.Fatalf("a write that succeeded carries the error %q", last.Error)
	}
}

// TestTheFrameWithAFillPaintsTheFillAndTheRemainder is the geometry of a progress
// bar: the lit LEDs in the state's colour, then the rest in the unfilled colour, so
// the segment covers the whole strip afterwards.
func TestTheFrameWithAFillPaintsTheFillAndTheRemainder(t *testing.T) {
	strip := newFakeStrip()
	pen := newWriter(strip, 128, quiet)

	if err := pen.paint(context.Background(), render.Frame{
		Colour: render.ColourPrinting,
		Lit:    3,
		Total:  5,
		Reason: render.ReasonProgress,
	}); err != nil {
		t.Fatalf("paint: %v", err)
	}

	shown := strip.shown()
	if len(shown) != 2 {
		t.Fatalf("a frame with a fill was painted in %d writes, want 2: %+v", len(shown), shown)
	}

	if shown[0].first != 0 || shown[0].count != 3 || shown[0].colour != render.ColourPrinting {
		t.Fatalf("the first write is %+v, want the first 3 LEDs in the state colour", shown[0])
	}

	if shown[1].first != 3 || shown[1].count != 2 {
		t.Fatalf("the second write is %+v, want the 2 LEDs after the fill", shown[1])
	}
}

// TestTheStatusRouteAnswersWhatTheModuleThinks is the reason the route exists: a
// person can read the frame, the printer and the age of the reading without walking
// over to the light.
func TestTheStatusRouteAnswersWhatTheModuleThinks(t *testing.T) {
	at := time.Date(2026, time.November, 18, 12, 0, 0, 0, time.UTC)

	publish(printer.Snapshot{
		ID:         "01J8DEVICE",
		Name:       "Morpheus",
		Connected:  true,
		State:      printer.Reading[string]{Value: printer.StatePrinting, Known: true},
		LastChange: at.Add(-12 * time.Second),
		ObservedAt: at,
		Age:        12 * time.Second,
	}, render.Frame{
		Colour: render.ColourPrinting,
		Lit:    24,
		Total:  48,
		Reason: render.ReasonProgress,
	})

	recordSend(sendView{At: at.Format(time.RFC3339Nano), OK: true})

	t.Cleanup(func() {
		current.Store(nil)
		sent.Store(nil)
	})

	recorder := httptest.NewRecorder()
	routes().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/status", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /status answered %d, want %d", recorder.Code, http.StatusOK)
	}

	var body struct {
		Printer struct {
			ID        string `json:"id"`
			Name      string `json:"name"`
			Connected bool   `json:"connected"`
			State     string `json:"state"`
		} `json:"printer"`
		Reading struct {
			LastChange string  `json:"last_change"`
			AgeSeconds float64 `json:"age_seconds"`
			Stale      bool    `json:"stale"`
		} `json:"reading"`
		Frame struct {
			Colour struct {
				R uint8 `json:"r"`
				G uint8 `json:"g"`
				B uint8 `json:"b"`
			} `json:"colour"`
			Lit    int    `json:"lit"`
			Total  int    `json:"total"`
			Reason string `json:"reason"`
		} `json:"frame"`
		LastSend *struct {
			OK bool `json:"ok"`
		} `json:"last_send"`
	}

	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("the answer is not JSON: %v", err)
	}

	if body.Printer.ID != "01J8DEVICE" || body.Printer.Name != "Morpheus" || !body.Printer.Connected {
		t.Fatalf("the answer names the printer %+v", body.Printer)
	}

	if body.Printer.State != printer.StatePrinting {
		t.Fatalf("the answer says the printer is %q, want %q", body.Printer.State, printer.StatePrinting)
	}

	if body.Reading.AgeSeconds != 12 {
		t.Fatalf("the answer says the reading is %v seconds old, want 12", body.Reading.AgeSeconds)
	}

	if body.Reading.Stale {
		t.Fatal("a twelve-second-old reading was reported as stale")
	}

	if body.Frame.Colour.R != render.ColourPrinting.R ||
		body.Frame.Colour.G != render.ColourPrinting.G ||
		body.Frame.Colour.B != render.ColourPrinting.B {
		t.Fatalf("the frame's colour is %+v, want %+v", body.Frame.Colour, render.ColourPrinting)
	}

	if body.Frame.Lit != 24 || body.Frame.Total != 48 {
		t.Fatalf("the frame is %d of %d LEDs, want 24 of 48", body.Frame.Lit, body.Frame.Total)
	}

	if body.Frame.Reason != string(render.ReasonProgress) {
		t.Fatalf("the frame's reason is %q, want %q", body.Frame.Reason, render.ReasonProgress)
	}

	if body.LastSend == nil || !body.LastSend.OK {
		t.Fatalf("the answer reports the last send as %+v, want a taken frame", body.LastSend)
	}
}

// TestTheStatusRouteRefusesBeforeItHasLooked holds the route to its own code
// rather than to a frame of zeros, which a person would read as a strip that is
// black.
func TestTheStatusRouteRefusesBeforeItHasLooked(t *testing.T) {
	current.Store(nil)
	sent.Store(nil)

	t.Cleanup(func() {
		current.Store(nil)
		sent.Store(nil)
	})

	recorder := httptest.NewRecorder()
	routes().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/status", nil))

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET /status answered %d before the first reading, want %d",
			recorder.Code, http.StatusServiceUnavailable)
	}

	var problem struct {
		Code string `json:"code"`
	}

	if err := json.Unmarshal(recorder.Body.Bytes(), &problem); err != nil {
		t.Fatalf("the refusal is not JSON: %v", err)
	}

	if !strings.HasSuffix(problem.Code, "NO_READING_YET") {
		t.Fatalf("the refusal carries %q, want a code ending in NO_READING_YET", problem.Code)
	}
}

// fakeReadings is a snapshot the driver reads, without a host.
type fakeReadings struct{ snapshot printer.Snapshot }

func (f fakeReadings) Snapshot() printer.Snapshot { return f.snapshot }

// TestTheDriverTurnsASnapshotIntoALitFrame is the wiring in one test: a printing
// printer at 50 % becomes a green frame with half the strip lit, it reaches the
// controller, and the status route can see it.
func TestTheDriverTurnsASnapshotIntoALitFrame(t *testing.T) {
	now := time.Now()

	source := fakeReadings{snapshot: printer.Snapshot{
		ID:        "01J8DEVICE",
		Name:      "Morpheus",
		Connected: true,
		State:     printer.Reading[string]{Value: printer.StatePrinting, Known: true},
		Job: printer.Job{
			Live:     true,
			Running:  printer.Reading[bool]{Value: true, Known: true},
			Progress: printer.Reading[float64]{Value: 50, Known: true},
		},
		LastChange: now,
		ObservedAt: now,
	}}

	strip := newFakeStrip()
	pen := newWriter(strip, 128, quiet)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	driver := newDriver(source, pen, strip, quiet)
	driver.tick = 5 * time.Millisecond

	t.Cleanup(func() {
		current.Store(nil)
		sent.Store(nil)
	})

	go pen.run(ctx)
	go driver.run(ctx)

	strip.waitForPaints(t, 1)

	seen := current.Load()
	if seen == nil {
		t.Fatal("the driver decided a frame and published nothing")
	}

	if seen.Frame.Lit != 24 || seen.Frame.Total != 48 {
		t.Fatalf("the frame published is %d of %d LEDs, want 24 of 48",
			seen.Frame.Lit, seen.Frame.Total)
	}

	if seen.Frame.Colour.R != render.ColourPrinting.R ||
		seen.Frame.Colour.G != render.ColourPrinting.G ||
		seen.Frame.Colour.B != render.ColourPrinting.B {
		t.Fatalf("the frame published is coloured %+v, want the printing colour", seen.Frame.Colour)
	}

	if seen.Printer.Name != "Morpheus" {
		t.Fatalf("the frame published names the printer %q, want Morpheus", seen.Printer.Name)
	}
}
