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

// paintCall is one write a test's controller received: what it was asked to draw, and the
// segments the body carried when it was a multi-segment write.
type paintCall struct {
	first    int
	count    int
	colour   wled.RGB
	segments []wled.SegmentWrite
}

// unfilled is the colour the render package gives the unlit part of the range. It is read
// from the package rather than written out here, so a change to it is a change to these
// tests rather than a test that quietly asserts the old answer.
var unfilled = render.Frame{}.Unfilled()

// samePaint reports whether two writes drew the same picture, which is what a test about a
// repaint means. The segment list itself is not compared: a body is the same picture when
// its fill and the colour it is drawn in are.
func samePaint(a, b paintCall) bool {
	return a.first == b.first && a.count == b.count && a.colour == b.colour
}

// fakeStrip stands in for a WLED controller. It records what it was asked to
// show, and can be made to block or fail so that a test can drive the writer's
// timing without a network.
type fakeStrip struct {
	mu       sync.Mutex
	paints   []paintCall
	settled  int
	failNext int
	broken   bool
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

// SetSegments records one request: the segments its body carried, in order, and the
// first segment's span and colour in the fields the older assertions read. One call is
// one write to the controller, which is what a test counting writes is counting.
func (f *fakeStrip) SetSegments(ctx context.Context, segments []wled.SegmentWrite) error {
	call := paintCall{segments: append([]wled.SegmentWrite(nil), segments...)}

	if len(segments) > 0 {
		call.first, call.count, call.colour = segments[0].First, segments[0].Count, segments[0].Colour
	}

	return f.record(ctx, call)
}

// record is one write: what was asked for, then however long the controller takes
// to answer, then whether it answered.
func (f *fakeStrip) record(ctx context.Context, call paintCall) error {
	f.mu.Lock()

	failed := f.broken
	if !failed && f.failNext > 0 {
		f.failNext--
		failed = true
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

// fail makes the next count writes fail. It takes the lock because a test that calls
// it is doing so with the writer already running.
func (f *fakeStrip) fail(count int) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.failNext = count
}

// stopAnswering makes every write fail from now on: a controller that answers a ping,
// reports its LED count, and will not take a frame. This is the state the installed
// module found a real 16,384-pixel panel in.
func (f *fakeStrip) stopAnswering() {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.broken = true
}

// answerAgain lets writes through again.
func (f *fakeStrip) answerAgain() {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.broken = false
}

// settleWrites is how many brightness and power writes the controller has taken. The
// heartbeat must add none of them: it repaints a frame and moves nothing else.
func (f *fakeStrip) settleWrites() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.settled
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

	// The controller records a paint before the write is reported, so this waits for the
	// report rather than reading it the instant the paint lands.
	last := waitForSend(t, true)
	if last.Error != "" {
		t.Fatalf("a write that succeeded carries the error %q", last.Error)
	}
}

// TestAFrameWithAFillIsOneRequestCarryingBothSegments is the geometry of a progress bar,
// and the defect that left a live panel black. The fill and the remainder used to be two
// writes to one segment, so the second took the first one's bounds and the fill was
// covered by nothing. It is now one request with two segments, and nothing is drawn twice
// or left out.
func TestAFrameWithAFillIsOneRequestCarryingBothSegments(t *testing.T) {
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
	if len(shown) != 1 {
		t.Fatalf("one frame was painted in %d requests, want 1: %+v", len(shown), shown)
	}

	segments := shown[0].segments
	if len(segments) != 2 {
		t.Fatalf("the request carried %d segments, want 2: %+v", len(segments), segments)
	}

	fill := segments[0]
	if fill.ID != firstSegment || fill.First != 0 || fill.Count != 3 {
		t.Fatalf("the fill segment is %+v, want segment %d covering pixels 0 to 3",
			fill, firstSegment)
	}

	if !fill.On || fill.Colour != render.ColourPrinting {
		t.Fatalf("the fill segment is %+v, want it drawn in the state colour %+v",
			fill, render.ColourPrinting)
	}

	rest := segments[1]
	if rest.ID != remainderSegment || rest.First != 3 || rest.Count != 2 {
		t.Fatalf("the remainder segment is %+v, want segment %d covering pixels 3 to 5",
			rest, remainderSegment)
	}

	if !rest.On || rest.Colour != unfilled {
		t.Fatalf("the remainder segment is %+v, want it drawn in the unfilled colour", rest)
	}
}

// TestTheBodyOfEveryShapeOfFrame is the three shapes a frame can take, each written as
// one request carrying both segments — and the fourth case that is easy to get wrong: a
// frame whose fill did not move but whose colour did.
//
// The edge cases are the ones that leave a light lying. A frame with no fill writes the
// whole range in its colour and switches the remainder off, or the bar from the frame
// before it stays on an idle strip. A frame that is all fill does the same, or a black
// band sits across a finished job.
func TestTheBodyOfEveryShapeOfFrame(t *testing.T) {
	for _, shape := range []struct {
		name string
		lit  int
		want []wled.SegmentWrite
	}{
		{
			name: "nothing filled",
			lit:  0,
			want: []wled.SegmentWrite{
				{ID: 0, First: 0, Count: 48, On: true, Colour: render.ColourIdle},
				{ID: 1, On: false},
			},
		},
		{
			name: "everything filled",
			lit:  48,
			want: []wled.SegmentWrite{
				{ID: 0, First: 0, Count: 48, On: true, Colour: render.ColourIdle},
				{ID: 1, On: false},
			},
		},
		{
			name: "half filled",
			lit:  24,
			want: []wled.SegmentWrite{
				{ID: 0, First: 0, Count: 24, On: true, Colour: render.ColourIdle},
				{ID: 1, First: 24, Count: 24, On: true, Colour: unfilled},
			},
		},
		{
			// The fill is where it was; only its colour moved, and both segments must
			// still be written because the fill's colour is the thing that changed.
			name: "same fill, another colour",
			lit:  24,
			want: []wled.SegmentWrite{
				{ID: 0, First: 0, Count: 24, On: true, Colour: render.ColourError},
				{ID: 1, First: 24, Count: 24, On: true, Colour: unfilled},
			},
		},
	} {
		t.Run(shape.name, func(t *testing.T) {
			strip := newFakeStrip()
			pen := newWriter(strip, 128, quiet)

			colour := render.ColourIdle
			if shape.name == "same fill, another colour" {
				colour = render.ColourError
			}

			if err := pen.paint(context.Background(), render.Frame{
				Colour: colour,
				Lit:    shape.lit,
				Total:  48,
				Reason: render.ReasonNoLiveJob,
			}); err != nil {
				t.Fatalf("paint: %v", err)
			}

			shown := strip.shown()
			if len(shown) != 1 {
				t.Fatalf("one frame was painted in %d requests, want 1: %+v", len(shown), shown)
			}

			got := shown[0].segments
			if len(got) != len(shape.want) {
				t.Fatalf("the request carried %d segments, want %d: %+v",
					len(got), len(shape.want), got)
			}

			for i := range shape.want {
				if got[i] != shape.want[i] {
					t.Fatalf("segment %d is %+v, want %+v", i, got[i], shape.want[i])
				}
			}
		})
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

// fakeClock is a clock a test moves by hand, so that a test about a minute-long
// heartbeat does not spend a minute. It stands in for both of the writer's dealings with
// time: reading it, and waiting on it.
type fakeClock struct {
	mu     sync.Mutex
	at     time.Time
	waits  []clockWait
	waited uint64
}

// clockWait is one wait the writer armed: the moment it ends, and the channel it ends on.
type clockWait struct {
	due time.Time
	ch  chan time.Time
}

// newFakeClock returns a clock standing at at.
func newFakeClock(at time.Time) *fakeClock { return &fakeClock{at: at} }

// Now is the clock's reading.
func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.at
}

// After is the writer's wait.
//
// Every wait is kept, not just the newest, because the writer abandons one whenever a frame
// arrives instead: a clock that remembered only the newest would leave the abandoned one
// fired into nothing and the live one unfired, which is a test failing for no reason a
// person could act on.
//
// A wait that is already over fires at once, which is what time.After(0) does. That is what
// lets a test move the clock before the goroutine arms its wait: the writer computes the
// wait from this clock, so a clock already past the deadline yields a wait of nothing and a
// repaint at once, whichever order the two happen in.
func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.waited++

	ch := make(chan time.Time, 1)
	due := c.at.Add(d)

	if !due.After(c.at) {
		ch <- c.at

		return ch
	}

	c.waits = append(c.waits, clockWait{due: due, ch: ch})

	return ch
}

// Advance moves the clock on by d and fires every wait that has come due.
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.at = c.at.Add(d)

	pending := make([]clockWait, 0, len(c.waits))

	for _, wait := range c.waits {
		if wait.due.After(c.at) {
			pending = append(pending, wait)

			continue
		}

		wait.ch <- c.at
	}

	c.waits = pending
}

// mark is how many waits the writer has armed so far. A test takes one before it causes a
// write and hands it to waitArmed afterwards.
func (c *fakeClock) mark() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.waited
}

// waitArmed waits until the writer has armed a wait since mark, which it only does once it
// has finished the write that moved its deadline.
//
// Without it a test can move the clock while the writer is still inside the write it just
// made, and the deadline the writer arms afterwards is one the test did not mean: a paint is
// recorded before the frame is reported and before the deadline is set.
func (c *fakeClock) waitArmed(t *testing.T, mark uint64) {
	t.Helper()

	deadline := time.Now().Add(howLong)

	for {
		c.mu.Lock()
		armed := c.waited > mark && len(c.waits) > 0
		c.mu.Unlock()

		if armed {
			return
		}

		if time.Now().After(deadline) {
			t.Fatalf("the writer armed no wait on the clock in %s", howLong)
		}

		time.Sleep(time.Millisecond)
	}
}

// watch makes pen read and wait on this clock instead of the real one.
func (c *fakeClock) watch(pen *writer) {
	pen.now = c.Now
	pen.after = c.After
}

// waitForSend waits until the last write the writer recorded satisfies ok, and fails
// the test if nothing it records ever does. The controller records a paint before the
// write is reported, so a test that only waited for the paint could read the previous
// report.
func waitForSend(t *testing.T, ok bool) sendView {
	t.Helper()

	deadline := time.Now().Add(howLong)

	for {
		if last := sent.Load(); last != nil && last.OK == ok {
			return *last
		}

		if time.Now().After(deadline) {
			t.Fatalf("the last write was never recorded as ok=%t; it reads %+v", ok, sent.Load())
		}

		time.Sleep(time.Millisecond)
	}
}

// TestAnUnchangedFrameIsRepaintedOnceTheHeartbeatElapses is the floor under the
// picture. A frame nothing has changed is not written again before the interval, and
// is written again once the interval has elapsed — which is how a controller that
// rebooted into its own default state recovers while the printer has nothing to say.
func TestAnUnchangedFrameIsRepaintedOnceTheHeartbeatElapses(t *testing.T) {
	strip := newFakeStrip()

	clock := newFakeClock(time.Date(2026, time.November, 18, 22, 0, 0, 0, time.UTC))
	pen := newWriter(strip, 128, quiet)
	clock.watch(pen)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go pen.run(ctx)

	pen.submit(frame(render.ColourIdle, render.ReasonNoLiveJob))
	strip.waitForPaints(t, 1)
	clock.waitArmed(t, 0)

	// One second short of the interval. The printer is still idle, so the picture is
	// still the same and the light must not be written.
	clock.Advance(heartbeatEvery - time.Second)

	if shown := strip.shown(); len(shown) != 1 {
		t.Fatalf("the light was written %d times before the interval elapsed, want 1: %+v",
			len(shown), shown)
	}

	// The interval elapses, so the same frame goes out again.
	clock.Advance(time.Second)
	strip.waitForPaints(t, 2)

	shown := strip.shown()
	if len(shown) != 2 {
		t.Fatalf("the light was written %d times after the interval, want 2: %+v", len(shown), shown)
	}

	if !samePaint(shown[1], shown[0]) {
		t.Fatalf("the heartbeat painted %+v, want the frame already on the strip, %+v",
			shown[1], shown[0])
	}

	// A repaint is a repaint: it does not move the brightness and does not switch the
	// strip on again.
	if settled := strip.settleWrites(); settled != 2 {
		t.Fatalf("the heartbeat made %d brightness and power writes, want the 2 made at start",
			settled)
	}
}

// TestAFailedWriteDoesNotMoveTheHeartbeat is why the interval is measured from the
// last write the strip took rather than from the last thing that happened: an attempt
// that failed is not a repaint, so it must not push the deadline out and leave the
// strip wrong for another minute.
//
// It also pins what a heartbeat repaints. The frame that failed never reached the
// controller, so it is no evidence of anything, and the repaint carries the frame the
// strip is known to show.
func TestAFailedWriteDoesNotMoveTheHeartbeat(t *testing.T) {
	strip := newFakeStrip()

	clock := newFakeClock(time.Date(2026, time.November, 18, 22, 0, 0, 0, time.UTC))
	pen := newWriter(strip, 128, quiet)
	clock.watch(pen)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go pen.run(ctx)

	pen.submit(frame(render.ColourPrinting, render.ReasonProgress))
	strip.waitForPaints(t, 1)
	clock.waitArmed(t, 0)

	// One second before the heartbeat is due the printer changes and the write fails.
	strip.fail(1)
	mark := clock.mark()
	clock.Advance(heartbeatEvery - time.Second)

	pen.submit(frame(render.ColourError, render.ReasonNoLiveJob))
	strip.waitForPaints(t, 2)
	clock.waitArmed(t, mark)

	// A failed write must not have restarted the interval: the light is still written
	// nothing until the deadline that the successful write set.
	if shown := strip.shown(); len(shown) != 2 {
		t.Fatalf("the light was written %d times, want the 2 attempts made so far: %+v",
			len(shown), shown)
	}

	clock.Advance(time.Second)
	strip.waitForPaints(t, 3)

	shown := strip.shown()
	if len(shown) != 3 {
		t.Fatalf("the light was written %d times, want 3: %+v", len(shown), shown)
	}

	if !samePaint(shown[2], shown[0]) {
		t.Fatalf("the heartbeat painted %+v, want the last frame the strip took, %+v",
			shown[2], shown[0])
	}

	if samePaint(shown[1], shown[0]) {
		t.Fatalf("the change was written as %+v, the same write as before it", shown[1])
	}
}

// TestAFailedHeartbeatIsRetriedWhenTheWindowElapses is the failure half: a repaint
// that did not reach the controller is reported as a failed write, is not a repaint,
// and does not suppress the next attempt. The module never concludes that the strip is
// showing the frame when the frame did not arrive.
//
// The retry comes when the window the failure opened has elapsed — one second for the
// first failure — and not a whole heartbeat later. "Rebooted" and "down" are the same
// moment to be right about: the repaint has to happen as soon as the controller is
// willing to take one.
func TestAFailedHeartbeatIsRetriedWhenTheWindowElapses(t *testing.T) {
	strip := newFakeStrip()

	clock := newFakeClock(time.Date(2026, time.November, 18, 22, 0, 0, 0, time.UTC))
	pen := newWriter(strip, 128, quiet)
	clock.watch(pen)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go pen.run(ctx)

	pen.submit(frame(render.ColourIdle, render.ReasonNoLiveJob))
	strip.waitForPaints(t, 1)
	clock.waitArmed(t, 0)

	// The controller has rebooted and is not answering: the first repaint is lost.
	strip.fail(1)
	mark := clock.mark()

	clock.Advance(heartbeatEvery)
	strip.waitForPaints(t, 2)
	clock.waitArmed(t, mark)

	failed := waitForSend(t, false)
	if failed.Error == "" {
		t.Fatalf("the failed heartbeat carries no error: %+v", failed)
	}

	if ctx.Err() != nil {
		t.Fatal("the writer stopped on a failed heartbeat")
	}

	// Nothing is submitted and the picture is unchanged. The next attempt is written
	// rather than suppressed as "already shown".
	clock.Advance(backoffFloor)
	strip.waitForPaints(t, 3)

	if retried := waitForSend(t, true); retried.Error != "" {
		t.Fatalf("the retried heartbeat carries the error %q", retried.Error)
	}

	shown := strip.shown()
	if len(shown) != 3 {
		t.Fatalf("the light was written %d times, want 3: %+v", len(shown), shown)
	}

	for i, call := range shown {
		if !samePaint(call, shown[0]) {
			t.Fatalf("write %d painted %+v, want the one frame %+v", i, call, shown[0])
		}
	}
}

// TestAMovingPictureCostsNoHeartbeatWrites is the other half of the design, and the
// half that decides whether this may ship at all: a print whose picture keeps changing
// must not pay for the heartbeat.
//
// Twenty changes, each one second inside the interval, so almost twenty minutes of
// simulated printing, and exactly twenty writes. The heartbeat fires zero times,
// because every write that lands pushes the deadline out in front of it.
func TestAMovingPictureCostsNoHeartbeatWrites(t *testing.T) {
	strip := newFakeStrip()

	clock := newFakeClock(time.Date(2026, time.November, 18, 22, 0, 0, 0, time.UTC))
	pen := newWriter(strip, 128, quiet)
	clock.watch(pen)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go pen.run(ctx)

	const changes = 20

	for i := 1; i <= changes; i++ {
		mark := clock.mark()

		// Two pictures the strip can tell apart, alternating as a job's state and
		// progress keep the frame moving.
		if i%2 == 0 {
			pen.submit(frame(render.ColourError, render.ReasonNoLiveJob))
		} else {
			pen.submit(frame(render.ColourPrinting, render.ReasonProgress))
		}

		strip.waitForPaints(t, i)
		clock.waitArmed(t, mark)

		clock.Advance(heartbeatEvery - time.Second)
	}

	shown := strip.shown()
	if len(shown) != changes {
		t.Fatalf("the light was written %d times for %d changes, want %d: the heartbeat added %d writes",
			len(shown), changes, changes, len(shown)-changes)
	}

	if settled := strip.settleWrites(); settled != 2 {
		t.Fatalf("the light took %d brightness and power writes, want the 2 made at start", settled)
	}
}

// logs is a logger that keeps every line, for a test that is about how many lines there
// are.
type logs struct {
	mu    sync.Mutex
	lines []string
}

func (l *logs) logf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func (l *logs) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()

	return len(l.lines)
}

// TestAnOutageBacksOffInsteadOfRetryingEveryTick is the defect the installed module
// showed on a real controller. A failed write was never recorded as sent, so the next
// second's frame retried it, and the next: one POST a second, each waiting out the full
// two-second timeout, against a 16,384-pixel panel that was already struggling. The host
// answered a ping in 3 ms throughout, which is the tell — the congestion was ours.
//
// A picture is waiting at every window, which is a print in progress: there is always a
// newer frame than the one that failed. The waits the writer chooses are a second, two,
// four, eight, sixteen and then thirty at the cap, so five minutes of that costs fourteen
// attempts and six log lines where it used to cost three hundred of each.
func TestAnOutageBacksOffInsteadOfRetryingEveryTick(t *testing.T) {
	strip := newFakeStrip()
	strip.stopAnswering()

	start := time.Date(2026, time.November, 18, 22, 0, 0, 0, time.UTC)

	clock := newFakeClock(start)

	var written logs

	pen := newWriter(strip, 128, written.logf)
	clock.watch(pen)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go pen.run(ctx)

	pen.submit(frame(render.ColourPrinting, render.ReasonProgress))
	strip.waitForPaints(t, 1)
	clock.waitArmed(t, 0)

	// The windows the writer should open, one per failure, from the floor to the cap and
	// then the cap again. Advancing by exactly one of them must produce exactly one
	// attempt: a shorter wait would have produced two by now, and a longer one would have
	// timed out above.
	attempts := 1

	for _, wait := range []time.Duration{
		1 * time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second,
		16 * time.Second, 30 * time.Second, 30 * time.Second, 30 * time.Second,
		30 * time.Second, 30 * time.Second, 30 * time.Second, 30 * time.Second,
		30 * time.Second,
	} {
		mark := clock.mark()

		// A picture waiting when the window opens — the newest one, as a print in
		// progress keeps supplying.
		pen.submit(frame(render.ColourError, render.ReasonDisconnected))

		clock.Advance(wait)

		attempts++
		strip.waitForPaints(t, attempts)
		clock.waitArmed(t, mark)
	}

	// The five minutes are up, and the window opened by the last failure has not elapsed:
	// nothing else is attempted.
	clock.Advance(5*time.Minute - clock.Now().Sub(start))

	if shown := strip.shown(); len(shown) != 14 {
		t.Fatalf("five minutes of a controller that will not answer cost %d attempts, want 14: %+v",
			len(shown), shown)
	}

	if lines := written.count(); lines != 6 {
		t.Fatalf("five minutes of a controller that will not answer wrote %d log lines, want 6",
			lines)
	}
}

// TestAChangedFrameWaitsOutTheBackoffAndThenGoesOut is the second half of the backoff:
// a picture that changes during a window is not a reason to try again. It is held in
// the one slot, and the newest one is what goes out when the window elapses — never an
// older one, and never early.
func TestAChangedFrameWaitsOutTheBackoffAndThenGoesOut(t *testing.T) {
	strip := newFakeStrip()

	clock := newFakeClock(time.Date(2026, time.November, 18, 22, 0, 0, 0, time.UTC))
	pen := newWriter(strip, 128, quiet)
	clock.watch(pen)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go pen.run(ctx)

	pen.submit(frame(render.ColourPrinting, render.ReasonProgress))
	strip.waitForPaints(t, 1)
	clock.waitArmed(t, 0)

	// The controller stops taking frames, so the next picture fails and opens a window.
	strip.stopAnswering()
	mark := clock.mark()

	pen.submit(frame(render.ColourIdle, render.ReasonNoLiveJob))
	strip.waitForPaints(t, 2)
	clock.waitArmed(t, mark)

	// A new picture while the window is open. It buys no attempt at all.
	pen.submit(frame(render.ColourError, render.ReasonDisconnected))

	if shown := strip.shown(); len(shown) != 2 {
		t.Fatalf("the light was written %d times inside the backoff, want the 2 already made: %+v",
			len(shown), shown)
	}

	// The window elapses and the controller answers again: the held picture goes out,
	// and it is the newest one rather than the one that failed.
	strip.answerAgain()
	clock.Advance(backoffFloor)
	strip.waitForPaints(t, 3)

	shown := strip.shown()
	if len(shown) != 3 {
		t.Fatalf("the light was written %d times, want 3: %+v", len(shown), shown)
	}

	if shown[2].colour != render.ColourError {
		t.Fatalf("the write after the backoff is %+v, want the picture that arrived during it, %+v",
			shown[2], render.ColourError)
	}
}

// TestASuccessResetsTheBackoffToTheFloor is what keeps the backoff a backoff: one write
// that lands forgives the ones that did not. Without it a controller that stuttered once
// an hour would spend the rest of its life waiting half a minute between frames.
func TestASuccessResetsTheBackoffToTheFloor(t *testing.T) {
	strip := newFakeStrip()

	clock := newFakeClock(time.Date(2026, time.November, 18, 22, 0, 0, 0, time.UTC))
	pen := newWriter(strip, 128, quiet)
	clock.watch(pen)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go pen.run(ctx)

	// Three failures in a row. Each frame is submitted while the previous window is
	// still open, so it is held and attempted when the window elapses, and the wait
	// doubles: a second, then two, then four.
	strip.stopAnswering()

	pen.submit(frame(render.ColourPrinting, render.ReasonProgress))
	strip.waitForPaints(t, 1)
	clock.waitArmed(t, 0)

	written := 1

	for _, wait := range []time.Duration{backoffFloor, 2 * time.Second} {
		mark := clock.mark()

		pen.submit(frame(render.ColourError, render.ReasonDisconnected))
		clock.Advance(wait)

		written++
		strip.waitForPaints(t, written)
		clock.waitArmed(t, mark)
	}

	// The controller answers again: this write lands and the count is forgiven.
	strip.answerAgain()
	mark := clock.mark()

	pen.submit(frame(render.ColourPrinting, render.ReasonProgress))
	clock.Advance(4 * time.Second)

	written++
	strip.waitForPaints(t, written)
	clock.waitArmed(t, mark)

	// One more failure. Had the count survived, its window would be eight seconds; it
	// must be the floor again.
	strip.fail(1)
	mark = clock.mark()

	pen.submit(frame(render.ColourError, render.ReasonDisconnected))
	written++
	strip.waitForPaints(t, written)
	clock.waitArmed(t, mark)

	// A second after that failure the light is tried again. Eight seconds after it, it
	// would not be, and this would time out.
	pen.submit(frame(render.ColourIdle, render.ReasonNoLiveJob))
	clock.Advance(backoffFloor)

	written++
	strip.waitForPaints(t, written)

	if shown := strip.shown(); len(shown) != written {
		t.Fatalf("the light was written %d times, want %d: %+v", len(shown), written, shown)
	}
}
