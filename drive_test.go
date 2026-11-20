package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prusa3d/prusa-connect/sdk/wire"

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

	// state is what the controller answers with a read of /json/state, which is where a
	// borrow comes from: one entry per segment, exactly as the device holds them.
	state wled.State

	// stateErr, when set, is what a read of /json/state fails with: a controller that is
	// unplugged at start.
	stateErr error

	// restores is every segment written back to the controller, in the order it was
	// written: the give-back a writer makes when it stops.
	restores []wled.Segment

	// powers is every power write the controller took, in order, so that a test can tell a light
	// switched off from one left alone.
	powers []bool

	// levels is every brightness the controller was asked for, in order.
	levels []int

	// presets is the controller's own preset table as names, and nil means "takes whatever it is
	// handed" — a light whose table the test does not care about. presetsRun is every preset the
	// controller was asked to run, in order, and presetErr is what every run fails with.
	presets    []string
	presetsRun []string
	presetErr  error
}

func newFakeStrip() *fakeStrip {
	return &fakeStrip{painted: make(chan struct{}, 16)}
}

func (f *fakeStrip) Info(context.Context) (wled.Info, error) {
	return wled.Info{LEDCount: 48}, nil
}

func (f *fakeStrip) SetBrightness(_ context.Context, level int) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.settled++
	f.levels = append(f.levels, level)

	return nil
}

// brightnessWrites is every brightness the controller was asked for, in order. A preset moment's
// give-back writes the value the light was found at, so this is how a test reads that promise.
func (f *fakeStrip) brightnessWrites() []int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]int(nil), f.levels...)
}

func (f *fakeStrip) SetPower(_ context.Context, on bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.settled++
	f.powers = append(f.powers, on)

	return nil
}

// powerWrites is every power write the controller took, in order. A pause is one `false` and a
// resume is one `true`, and a test about a borrowed zone asserts that this stays empty.
func (f *fakeStrip) powerWrites() []bool {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]bool(nil), f.powers...)
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

// wrote is how many frames reached the controller.
func (f *fakeStrip) wrote() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return len(f.paints)
}

// State is the controller's segments, which is what a writer borrows from.
func (f *fakeStrip) State(context.Context) (wled.State, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.state, f.stateErr
}

// RestoreSegment records one segment written back to the controller.
func (f *fakeStrip) RestoreSegment(_ context.Context, segment wled.Segment) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.restores = append(f.restores, segment)

	return nil
}

// RunPreset records one of the controller's own presets being run, by the name a moment named.
//
// A fake with no `presets` set takes any name, which is what a controller holding whatever the test
// named looks like; the tests about a name the light does not hold set `presets` and get the refusal
// the real client gives.
func (f *fakeStrip) RunPreset(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.presetErr != nil {
		return f.presetErr
	}

	if f.presets != nil {
		if !slices.Contains(f.presets, name) {
			return fmt.Errorf("%w: %q", wled.ErrNoSuchPreset, name)
		}
	}

	f.presetsRun = append(f.presetsRun, name)

	return nil
}

// ranPresets is every preset the controller was asked to run, in order.
func (f *fakeStrip) ranPresets() []string {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]string(nil), f.presetsRun...)
}

// givenBack is every segment the controller was given back, as a copy.
func (f *fakeStrip) givenBack() []wled.Segment {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]wled.Segment(nil), f.restores...)
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
				{ID: 0, First: 0, Count: 48, On: true, Colour: render.ColourIdle, ClearName: true},
				{ID: 1, On: false},
			},
		},
		{
			name: "everything filled",
			lit:  48,
			want: []wled.SegmentWrite{
				{ID: 0, First: 0, Count: 48, On: true, Colour: render.ColourIdle, ClearName: true},
				{ID: 1, On: false},
			},
		},
		{
			name: "half filled",
			lit:  24,
			want: []wled.SegmentWrite{
				{ID: 0, First: 0, Count: 24, On: true, Colour: render.ColourIdle, ClearName: true},
				{ID: 1, First: 24, Count: 24, On: true, Colour: unfilled},
			},
		},
		{
			// The fill is where it was; only its colour moved, and both segments must
			// still be written because the fill's colour is the thing that changed.
			name: "same fill, another colour",
			lit:  24,
			want: []wled.SegmentWrite{
				{ID: 0, First: 0, Count: 24, On: true, Colour: render.ColourError, ClearName: true},
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

// TestAStartingFrameIsOneWholePanelInTheWaitingYellow is the painter's half of the
// rule, and it was proved rather than assumed: a frame with no fill is written as one
// segment covering the whole range in the frame's own colour with the remainder
// switched off, so the starting panel is solid yellow — not green, and not a bar left
// over from the frame before it.
//
// The frame comes from render.Decide over a snapshot of a print at 0 %, so this is the
// pair proved together: the rule says starting, and the body that reaches the panel is
// the whole panel in the waiting colour. Both geometries, because the module drives a
// panel and the flat body is what a strip is given.
func TestAStartingFrameIsOneWholePanelInTheWaitingYellow(t *testing.T) {
	now := time.Now()

	starting := render.Decide(printer.Snapshot{
		ID:        "01J8DEVICE",
		Name:      "Morpheus",
		Connected: true,
		State:     printer.Reading[string]{Value: printer.StatePrinting, Known: true},
		Job: printer.Job{
			Live:     true,
			Running:  printer.Reading[bool]{Value: true, Known: true},
			Progress: printer.Reading[float64]{Value: 0, Known: true},
		},
		LastChange: now,
		ObservedAt: now,
	}, now, render.Config{LEDs: 16384})

	if starting.Reason != render.ReasonStarting || starting.Lit != 0 {
		t.Fatalf("the frame is %q with %d lit, want a starting frame with nothing lit",
			starting.Reason, starting.Lit)
	}

	for _, test := range []struct {
		name  string
		want  wled.SegmentWrite
		paint func(t *testing.T) []wled.SegmentWrite
	}{
		{
			name: "the live panel",
			want: wled.SegmentWrite{
				ID: firstSegment, First: 0, Count: 256, FirstRow: 0, RowCount: 64,
				On: true, Colour: render.ColourIdle, ClearName: true,
			},
			paint: func(t *testing.T) []wled.SegmentWrite {
				t.Helper()

				device := newPanelStrip()
				pen := newWriter(device, 128, quiet)
				pen.setPanel(device.info)

				if err := pen.paint(context.Background(), starting); err != nil {
					t.Fatalf("paint the starting frame: %v", err)
				}

				shown := device.shown()

				return shown[0].segments
			},
		},
		{
			name: "a controller with no matrix",
			want: wled.SegmentWrite{ID: firstSegment, First: 0, Count: 16384, On: true, Colour: render.ColourIdle, ClearName: true},
			paint: func(t *testing.T) []wled.SegmentWrite {
				t.Helper()

				device := newFakeStrip()
				pen := newWriter(device, 128, quiet)

				if err := pen.paint(context.Background(), starting); err != nil {
					t.Fatalf("paint the starting frame: %v", err)
				}

				shown := device.shown()

				return shown[0].segments
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := test.paint(t)

			if len(body) != 2 {
				t.Fatalf("the starting frame is written as %d segments, want 2: %+v", len(body), body)
			}

			if body[0] != test.want {
				t.Fatalf("the body is %+v, want one segment covering the whole range in the waiting yellow %+v",
					body[0], test.want)
			}

			if body[1].ID != remainderSegment || body[1].On || body[1].Count != 0 {
				t.Fatalf("the remainder is %+v, want it switched off with no span", body[1])
			}
		})
	}
}

// TestTheStatusRouteSaysStartingForAPrintAtZero: the reason is not only a test's word
// for the rule — the route answers with it, so a person reading /status while the
// printer sits at 0 % is told the print is starting rather than that it is printing.
func TestTheStatusRouteSaysStartingForAPrintAtZero(t *testing.T) {
	now := time.Now()

	snapshot := printer.Snapshot{
		ID:        "01J8DEVICE",
		Name:      "Morpheus",
		Connected: true,
		State:     printer.Reading[string]{Value: printer.StatePrinting, Known: true},
		Job: printer.Job{
			Live:     true,
			Running:  printer.Reading[bool]{Value: true, Known: true},
			Progress: printer.Reading[float64]{Value: 0, Known: true},
		},
		LastChange: now,
		ObservedAt: now,
	}

	frame := render.Decide(snapshot, now, render.Config{LEDs: 16384})
	publish(snapshot, frame)
	recordSend(sendView{At: now.Format(time.RFC3339Nano), OK: true})

	t.Cleanup(func() {
		current.Store(nil)
		sent.Store(nil)
	})

	recorder := httptest.NewRecorder()
	routes().ServeHTTP(recorder, forwarded(http.MethodGet, "/status", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /status answered %d, want %d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}

	var body struct {
		Printer struct {
			State string `json:"state"`
		} `json:"printer"`
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
	}

	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("the answer is not JSON: %v", err)
	}

	if body.Printer.State != printer.StatePrinting {
		t.Fatalf("the printer is %q, want %q: the route must still say the printer is printing",
			body.Printer.State, printer.StatePrinting)
	}

	if body.Frame.Reason != string(render.ReasonStarting) {
		t.Fatalf("the frame's reason is %q, want %q", body.Frame.Reason, render.ReasonStarting)
	}

	if body.Frame.Colour.R != render.ColourIdle.R || body.Frame.Colour.G != render.ColourIdle.G ||
		body.Frame.Colour.B != render.ColourIdle.B {
		t.Fatalf("the frame's colour is %+v, want the waiting yellow %+v", body.Frame.Colour, render.ColourIdle)
	}

	if body.Frame.Lit != 0 || body.Frame.Total != 16384 {
		t.Fatalf("the frame is %d of %d, want 0 of 16384", body.Frame.Lit, body.Frame.Total)
	}
}

// TestTheStatusRoutePublishesWhatTheBuildIs is the half of the Diagnostics screen's version line
// that cannot be faked: the number has to come off the running process, so the route publishes the
// release this binary reported to the host at start, the wire major it speaks, and the store schema
// it writes — never a literal a bundle carries.
//
// Both shapes of the answer carry it, because an instance driving one binding answers the frame
// beside its printer and an instance driving several answers a bindings list; a screen reads the
// build facts whichever one it got, so both are asserted here.
func TestTheStatusRoutePublishesWhatTheBuildIs(t *testing.T) {
	at := time.Date(2026, time.November, 19, 12, 0, 0, 0, time.UTC)

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
		forgetBindings()
	})

	// The build object as the route spells it, and as both shapes of the answer carry it.
	type publishedBuild struct {
		Version     string `json:"version"`
		Contract    uint32 `json:"contract"`
		StoreSchema int    `json:"store_schema"`
		Repository  string `json:"repository"`
	}

	read := func(what string) {
		t.Helper()

		recorder := httptest.NewRecorder()
		routes().ServeHTTP(recorder, forwarded(http.MethodGet, "/status", nil))

		if recorder.Code != http.StatusOK {
			t.Fatalf("%s: GET /status answered %d, want %d: %s",
				what, recorder.Code, http.StatusOK, recorder.Body.String())
		}

		var body struct {
			Bindings []json.RawMessage `json:"bindings"`
			Build    publishedBuild    `json:"build"`
		}

		if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
			t.Fatalf("%s: the answer is not JSON: %v", what, err)
		}

		build := body.Build

		if build.Version != release.String() {
			t.Fatalf("%s: the route reports the version %q, and this process is running %q",
				what, build.Version, release.String())
		}

		if build.Contract != wire.ProtocolMajor {
			t.Fatalf("%s: the route reports the wire contract %d, and this process speaks %d",
				what, build.Contract, wire.ProtocolMajor)
		}

		if build.StoreSchema != storeVersion {
			t.Fatalf("%s: the route reports store schema %d, and this build writes %d",
				what, build.StoreSchema, storeVersion)
		}

		// The repository travels with the version, because the screen's link to the source is that
		// answer's and not one the bundle was built with.
		if build.Repository != repository {
			t.Fatalf("%s: the route reports the repository %q, and this build names %q",
				what, build.Repository, repository)
		}
	}

	// The single-frame answer: the shape an instance driving one binding gives.
	read("one binding")

	// The bindings list, which is what an instance driving several answers with. The entry itself
	// does not matter here: what is asserted is that this shape carries the build facts too.
	bindingMu.Lock()
	bindingHeld = []bindingStatus{{}}
	bindingMu.Unlock()

	read("several bindings")
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
	routes().ServeHTTP(recorder, forwarded(http.MethodGet, "/status", nil))

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

// TestTheStatusRouteKeepsTheTruthWhileTheFinishedPanelHolds is the honesty half of the
// rule the operator's panel now follows: the picture stops reacting to a finished
// printer's silence, and the route still reports the reading as stale, with its real
// age, so nobody reads the green as a claim that the module knows something current.
func TestTheStatusRouteKeepsTheTruthWhileTheFinishedPanelHolds(t *testing.T) {
	at := time.Date(2026, time.November, 19, 1, 27, 56, 0, time.UTC)

	snapshot := snapshotOf(printer.StateFinished, true, true, at.Add(-65*time.Second))
	snapshot.ObservedAt = at
	snapshot.Age = 65 * time.Second

	frame := render.Decide(snapshot, at, render.Config{LEDs: 16384})
	if frame.Colour != render.ColourFinished {
		t.Fatalf("the frame for a finished printer that has gone quiet is %+v, want the finished green", frame)
	}

	publish(snapshot, frame)

	t.Cleanup(func() {
		current.Store(nil)
		sent.Store(nil)
	})

	recorder := httptest.NewRecorder()
	routes().ServeHTTP(recorder, forwarded(http.MethodGet, "/status", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /status answered %d, want %d", recorder.Code, http.StatusOK)
	}

	var body struct {
		Reading struct {
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
			Reason string `json:"reason"`
		} `json:"frame"`
	}

	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("the answer is not JSON: %v", err)
	}

	if !body.Reading.Stale {
		t.Fatal("the route calls a 65-second-old reading live, which is the lie this module exists to avoid")
	}

	if body.Reading.AgeSeconds != 65 {
		t.Fatalf("the route says the reading is %v seconds old, want 65", body.Reading.AgeSeconds)
	}

	if body.Frame.Colour.R != render.ColourFinished.R ||
		body.Frame.Colour.G != render.ColourFinished.G ||
		body.Frame.Colour.B != render.ColourFinished.B {
		t.Fatalf("the frame's colour is %+v, want the finished green %+v", body.Frame.Colour, render.ColourFinished)
	}

	if body.Frame.Lit != 0 {
		t.Fatalf("the frame lights %d LEDs, want 0: a finished job has no progress to fill", body.Frame.Lit)
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
	routes().ServeHTTP(recorder, forwarded(http.MethodGet, "/status", nil))

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

// The panel is not a longer strip, and the tests below are the ones that say so.
//
// Everything above this line was written when a matrix was believed to accept a flat run of
// pixels: the firmware would add startY times its width to start and lay the two numbers out
// row by row. It does not. A body naming pixels 10158 to 16384 with stopY 0 was POSTed to a real
// 256x64 panel, answered {"success":true}, and the device reported both segments as start 0,
// stop 256, startY 0, stopY 1 — start and stop are columns, clamped to the width, and stopY 0 is
// one row. Both segments became the same line and the panel was dark.
//
// So a panel frame is two rectangles that cover it between them, and the size of the fill is
// the frame's own fraction of the panel's width.

// wireCapture is a controller that records the exact body of every request it is sent and
// answers the success WLED answers. A body is asserted as bytes because that is what goes down
// the socket; a struct compared here would be a test of this module's encoding rather than of
// what the device is asked for.
type wireCapture struct {
	bodies []string
}

func (c *wireCapture) server(t *testing.T) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read the request body: %v", err)
		}

		c.bodies = append(c.bodies, string(raw))

		_, _ = io.WriteString(w, `{"success":true}`)
	}))
	t.Cleanup(server.Close)

	return server
}

// livePanel is the matrix the module was proved against.
var livePanel = panel{width: 256, height: 64}

// panelStrip is a controller that reports itself as that matrix, so a test drives the writer
// through the geometry it would learn from the device rather than setting it by hand.
type panelStrip struct {
	*fakeStrip
	info  wled.Info
	live  atomic.Bool
	reads atomic.Int64
}

func newPanelStrip() *panelStrip {
	strip := &panelStrip{fakeStrip: newFakeStrip(), info: wled.Info{LEDCount: 16384, MatrixWidth: 256, MatrixHeight: 64}}

	// A panel that takes live mode is the healthy answer, and the only one this module can get: a
	// datagram is not acknowledged and no pixel is reported, so "live" is the whole of what the
	// device will say about a pushed picture. A test that wants the failure flips it.
	strip.live.Store(true)

	return strip
}

// Info answers with the geometry and with whatever the test has said about live mode. The flag is
// atomic because the writer's goroutine reads it while a test changes it.
func (p *panelStrip) Info(context.Context) (wled.Info, error) {
	p.reads.Add(1)

	info := p.info
	info.Live = p.live.Load()

	return info, nil
}

// takeRealtime says whether the controller is drawing a live picture, as a test would set it when
// the panel takes the word and again when its realtime timeout runs out.
func (p *panelStrip) takeRealtime(live bool) { p.live.Store(live) }

// realtimeReads is how many times the writer asked the controller about live mode.
func (p *panelStrip) realtimeReads() int64 { return p.reads.Load() }

// TestAPanelIsNotAStrip runs one frame through both shapes. The 1D body is what a strip has
// always been given, unchanged to the pixel; the 2D body is a rectangle, and the same frame
// produces both depending only on what the controller said about itself.
func TestAPanelIsNotAStrip(t *testing.T) {
	frame := render.Frame{Colour: render.ColourPrinting, Lit: 10158, Total: 16384, Reason: render.ReasonProgress}

	flat := segmentsFor(frame, panel{})
	if len(flat) != 2 {
		t.Fatalf("the strip body carries %d segments, want 2: %+v", len(flat), flat)
	}

	if flat[0].Count != 10158 || flat[0].FirstRow != 0 || flat[0].RowCount != 0 {
		t.Fatalf("the strip fill is %+v, want 10158 pixels from 0 and no rows", flat[0])
	}

	if flat[1].First != 10158 || flat[1].Count != 6226 {
		t.Fatalf("the strip remainder is %+v, want 6226 pixels from 10158", flat[1])
	}

	rectangles := segmentsFor(frame, livePanel)
	if len(rectangles) != 2 {
		t.Fatalf("the panel body carries %d segments, want 2: %+v", len(rectangles), rectangles)
	}

	fill := rectangles[0]
	if fill.First != 0 || fill.Count != 159 || fill.FirstRow != 0 || fill.RowCount != 64 {
		t.Fatalf("the panel fill is columns %d..%d over rows %d..%d, want columns 0..159 over rows 0..64",
			fill.First, fill.First+fill.Count, fill.FirstRow, fill.FirstRow+fill.RowCount)
	}

	rest := rectangles[1]
	if rest.First != 159 || rest.Count != 97 || rest.FirstRow != 0 || rest.RowCount != 64 {
		t.Fatalf("the panel remainder is columns %d..%d over rows %d..%d, want columns 159..256 over rows 0..64",
			rest.First, rest.First+rest.Count, rest.FirstRow, rest.FirstRow+rest.RowCount)
	}

	// The fill and the remainder are adjacent, so no column belongs to both and none belongs to
	// neither: that is what makes the pair a picture of the panel rather than two bars on it.
	if fill.First+fill.Count != rest.First || rest.First+rest.Count != livePanel.width {
		t.Fatalf("the two rectangles do not tile the width: fill ends at %d, remainder starts at %d and ends at %d, want %d",
			fill.First+fill.Count, rest.First, rest.First+rest.Count, livePanel.width)
	}

	if fill.Colour != render.ColourPrinting || rest.Colour != unfilled {
		t.Fatalf("the panel is drawn in %+v and %+v, want the state colour and the unfilled colour",
			fill.Colour, rest.Colour)
	}
}

// TestTheFillBoundaryOnAPanel is the arithmetic between a frame's fraction and a column of a
// 256-wide panel: the boundary is the fraction times the width, rounded, and never outside one
// column to all of them.
//
// Rounding, where the 48-LED strip floors. A column here is 64 pixels, so a floor would hold the
// bar still for the first two thirds of a percent of a job; an LED on the strip is over two
// percent, where flooring is what keeps the bar from claiming more than the printer reported.
// The unit decides, and the tests say both.
func TestTheFillBoundaryOnAPanel(t *testing.T) {
	const total = 16384

	for _, shape := range []struct {
		name string
		lit  int
		want int
	}{
		{name: "nothing filled", lit: 0, want: 0},
		{name: "a job that has just started", lit: 1, want: 1},
		{name: "one percent", lit: 164, want: 3},           // 2.56 columns, rounded to 3
		{name: "a tenth", lit: 1638, want: 26},             // 25.59 columns, rounded to 26
		{name: "sixty-two percent", lit: 10158, want: 159}, // 158.72 columns
		{name: "half", lit: 8192, want: 128},
		{name: "ninety-nine percent", lit: 16220, want: 253}, // 253.44 columns
		{name: "everything filled", lit: total, want: 256},
		// A printer that reports more than it has must not light a column that does not exist.
		{name: "more than everything", lit: total * 2, want: 256},
	} {
		t.Run(shape.name, func(t *testing.T) {
			got := fillColumns(render.Frame{Lit: shape.lit, Total: total}, livePanel)
			if got != shape.want {
				t.Fatalf("%d of %d pixels fills %d columns, want %d", shape.lit, total, got, shape.want)
			}
		})
	}

	// A frame decided before the controller had said how many pixels it drives has no
	// fraction to apply, and no column is an honest answer.
	if got := fillColumns(render.Frame{Lit: 0, Total: 0}, livePanel); got != 0 {
		t.Fatalf("a frame with no total fills %d columns, want 0", got)
	}

	// A panel whose width is zero is not a panel; the 1D path owns that frame.
	if got := fillColumns(render.Frame{Lit: 10, Total: 20}, panel{}); got != 0 {
		t.Fatalf("a frame with no geometry fills %d columns, want 0", got)
	}
}

// TestAPanelFrameAlwaysCoversTheWholePanel is the two edges, which matter more on a panel than
// on a strip: a controller keeps a segment's rectangle until something rewrites it, so a frame
// that leaves a segment alone leaves the frame before it showing.
//
// Nothing filled and everything filled are the same body — the whole panel in the frame's colour
// and the remainder switched off — and the remainder carries no bounds at all, because a
// rectangle with no width is a segment the controller deletes rather than one that draws
// nothing, and an off segment draws nothing wherever it points.
func TestAPanelFrameAlwaysCoversTheWholePanel(t *testing.T) {
	for _, shape := range []struct {
		name  string
		lit   int
		total int
	}{
		{name: "nothing filled", lit: 0, total: 16384},
		{name: "everything filled", lit: 16384, total: 16384},
		// The boundary rounds onto the last column: there is no remainder to draw, so this is
		// the filled edge and not a bar with a rectangle of no width beside it.
		{name: "all but a handful of pixels", lit: 16380, total: 16384},
	} {
		t.Run(shape.name, func(t *testing.T) {
			body := segmentsFor(render.Frame{Colour: render.ColourFinished, Lit: shape.lit, Total: shape.total}, livePanel)
			if len(body) != 2 {
				t.Fatalf("the body carries %d segments, want 2: %+v", len(body), body)
			}

			whole := body[0]
			if whole.First != 0 || whole.Count != 256 || whole.FirstRow != 0 || whole.RowCount != 64 || !whole.On {
				t.Fatalf("the panel is %+v, want columns 0..256 over rows 0..64 and drawn", whole)
			}

			if body[1].On || body[1].Count != 0 {
				t.Fatalf("the remainder is %+v, want it switched off with no span of its own", body[1])
			}
		})
	}

	// The bar a middle frame drew must not survive the edge that follows it. Both frames are
	// built from the same writer, one after the other, and the second body is asserted to carry
	// no remainder rectangle at all.
	strip := newPanelStrip()
	pen := newWriter(strip, 128, quiet)
	pen.setPanel(strip.info)

	middle := render.Frame{Colour: render.ColourPrinting, Lit: 8192, Total: 16384, Reason: render.ReasonProgress}
	if err := pen.paint(context.Background(), middle); err != nil {
		t.Fatalf("paint the middle frame: %v", err)
	}

	finished := render.Frame{Colour: render.ColourFinished, Lit: 16384, Total: 16384, Reason: render.ReasonNoLiveJob}
	if err := pen.paint(context.Background(), finished); err != nil {
		t.Fatalf("paint the finished frame: %v", err)
	}

	shown := strip.shown()
	if len(shown) != 2 {
		t.Fatalf("two frames were painted in %d requests, want 2", len(shown))
	}

	if last := shown[1].segments; len(last) != 2 || last[1].On || last[1].Count != 0 {
		t.Fatalf("the frame after a bar is %+v, want the remainder switched off so the bar cannot stay", last)
	}
}

// TestAPanelBodyIsOneRequestWithFourBounds is the body and the wire together: what a frame
// becomes as bytes on a socket, from the frame through segmentsFor, SetSegments and the client's
// own encoder. It is the body this module will POST to the panel, so the four bounds and the
// order of the keys are asserted rather than described.
//
// The three frames are the ones the live panel was proved with: 62 % — the frame that was green
// and arrived dark — then a tenth and then all of it. Each is one request carrying two segments,
// and the fill's rectangle ends at the column the arithmetic predicts with the remainder
// starting there. Segment 0 also carries `"n":""` in every shape, which is the name an event put
// there being cleared; it is one key in a body that goes out anyway, not a second request.
func TestAPanelBodyIsOneRequestWithFourBounds(t *testing.T) {
	for _, shape := range []struct {
		name string
		lit  int
		want string
	}{
		{
			name: "sixty-two percent",
			lit:  10158,
			want: `{"seg":[{"id":0,"start":0,"stop":159,"startY":0,"stopY":64,"on":true,"col":[[0,170,0]],"fx":0,"n":""},` +
				`{"id":1,"start":159,"stop":256,"startY":0,"stopY":64,"on":true,"col":[[0,0,0]],"fx":0}]}`,
		},
		{
			name: "a tenth",
			lit:  1638,
			want: `{"seg":[{"id":0,"start":0,"stop":26,"startY":0,"stopY":64,"on":true,"col":[[0,170,0]],"fx":0,"n":""},` +
				`{"id":1,"start":26,"stop":256,"startY":0,"stopY":64,"on":true,"col":[[0,0,0]],"fx":0}]}`,
		},
		{
			name: "everything",
			lit:  16384,
			want: `{"seg":[{"id":0,"start":0,"stop":256,"startY":0,"stopY":64,"on":true,"col":[[0,170,0]],"fx":0,"n":""},` +
				`{"id":1,"on":false}]}`,
		},
		{
			name: "nothing",
			lit:  0,
			want: `{"seg":[{"id":0,"start":0,"stop":256,"startY":0,"stopY":64,"on":true,"col":[[0,170,0]],"fx":0,"n":""},` +
				`{"id":1,"on":false}]}`,
		},
	} {
		t.Run(shape.name, func(t *testing.T) {
			recorder := &wireCapture{}
			server := recorder.server(t)

			light, err := wled.New(server.URL)
			if err != nil {
				t.Fatalf("wled.New: %v", err)
			}

			pen := newWriter(light, 128, quiet)
			pen.setPanel(wled.Info{LEDCount: 16384, MatrixWidth: 256, MatrixHeight: 64})

			if err := pen.paint(context.Background(), render.Frame{
				Colour: render.ColourPrinting,
				Lit:    shape.lit,
				Total:  16384,
				Reason: render.ReasonProgress,
			}); err != nil {
				t.Fatalf("paint: %v", err)
			}

			if len(recorder.bodies) != 1 {
				t.Fatalf("one frame was painted in %d requests, want 1: %v", len(recorder.bodies), recorder.bodies)
			}

			if recorder.bodies[0] != shape.want {
				t.Fatalf("the panel body is\n%s\nwant\n%s", recorder.bodies[0], shape.want)
			}

			// And the body is a body: it parses as the state write a controller takes, with
			// two segments in it.
			var posted struct {
				Segments []map[string]any `json:"seg"`
			}
			if err := json.Unmarshal([]byte(recorder.bodies[0]), &posted); err != nil {
				t.Fatalf("the body is not JSON: %v", err)
			}

			if len(posted.Segments) != 2 {
				t.Fatalf("the body carries %d segments, want 2", len(posted.Segments))
			}
		})
	}
}

// TestAStripControllerStillGetsTheFlatBody is the other half of the same promise, proved at the
// wire rather than in the segment list: a controller that reports no matrix — a strip — is given
// the body it has always been given, with the run of pixels counted from the first and both row
// bounds as zero.
//
// It runs through the driver's own learning step, so what is asserted is what a real strip
// produces: Info answers, the writer records the geometry, and the frame that follows is flat.
// The flat body carries the same `"n":""` on segment 0 that the panel's does: a strip has no
// word, but the same writer clears the name in the body it was writing anyway.
func TestAStripControllerStillGetsTheFlatBody(t *testing.T) {
	recorder := &wireCapture{}
	server := recorder.server(t)

	light, err := wled.New(server.URL)
	if err != nil {
		t.Fatalf("wled.New: %v", err)
	}

	pen := newWriter(light, 128, quiet)
	strip := newPanelStrip()
	strip.info = wled.Info{LEDCount: 48}
	reader := newDriver(nil, pen, strip, quiet)

	reader.learn(context.Background(), time.Now())

	if err := pen.paint(context.Background(), render.Frame{
		Colour: render.ColourPrinting,
		Lit:    24,
		Total:  48,
		Reason: render.ReasonProgress,
	}); err != nil {
		t.Fatalf("paint: %v", err)
	}

	want := `{"seg":[{"id":0,"start":0,"stop":24,"startY":0,"stopY":0,"on":true,"col":[[0,170,0]],"fx":0,"n":""},` +
		`{"id":1,"start":24,"stop":48,"startY":0,"stopY":0,"on":true,"col":[[0,0,0]],"fx":0}]}`

	if len(recorder.bodies) != 1 || recorder.bodies[0] != want {
		t.Fatalf("the strip body is\n%v\nwant one body\n%s", recorder.bodies, want)
	}
}

// What the log carries, and what it deliberately does not.
//
// The operator's question was whether the log should have an item on each change or only
// when the controller is unreachable. The answer these tests pin is: one line per event,
// none per turn. /status is the present tense — what the printer is doing now — and the
// log is the history, what moved while nobody was looking. So a state word that changes
// is one line naming the printer and both words, a printer that keeps saying the same
// thing is none however many times the module looks, and a frame that moved only because
// the job progressed is none either.

// changingReadings is a snapshot a test changes between the driver's turns.
type changingReadings struct {
	mu       sync.Mutex
	snapshot printer.Snapshot
}

func (r *changingReadings) Snapshot() printer.Snapshot {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.snapshot
}

// show makes the driver read snapshot on its next turn.
func (r *changingReadings) show(snapshot printer.Snapshot) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.snapshot = snapshot
}

// all is every line logged so far, as a copy.
func (l *logs) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()

	return append([]string(nil), l.lines...)
}

// want fails unless the log holds exactly these lines, in this order. A test about a log
// is about which lines exist, so the count is asserted as a count and the words as words.
func (l *logs) want(t *testing.T, want ...string) {
	t.Helper()

	got := l.all()
	if len(got) != len(want) {
		t.Fatalf("the log holds %d lines, want %d:\n got: %q\nwant: %q",
			len(got), len(want), got, want)
	}

	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("log line %d is %q, want %q", i+1, got[i], want[i])
		}
	}
}

// waitForCount waits until count lines have been logged. A line about a failed write is
// written after the attempt it reports, so a test that only waited for the attempt could
// read the log before the line lands.
func (l *logs) waitForCount(t *testing.T, count int) {
	t.Helper()

	deadline := time.Now().Add(howLong)

	for l.count() < count {
		if time.Now().After(deadline) {
			t.Fatalf("the log holds %d lines after %s, want %d: %q",
				l.count(), howLong, count, l.all())
		}

		time.Sleep(time.Millisecond)
	}
}

// snapshotOf is one reading off the host: this module's one printer, a state word, whether
// the host holds a session with it, and whether its newest fact is older than StaleAfter.
func snapshotOf(state string, connected, stale bool, at time.Time) printer.Snapshot {
	return printer.Snapshot{
		ID:         "01J8DEVICE",
		Name:       "Morpheus",
		Connected:  connected,
		State:      printer.Reading[string]{Value: state, Known: state != ""},
		LastChange: at,
		ObservedAt: at,
		Stale:      stale,
	}
}

// newLogWatch is one driver a test turns by hand: a printer whose reading the test sets, a
// controller that answers at once, and a log the test counts lines in. No ticker, no
// network, and no wall clock in the assertions.
func newLogWatch(t *testing.T) (*driver, *changingReadings, *logs) {
	t.Helper()

	source := &changingReadings{}
	strip := newFakeStrip()

	written := &logs{}
	reader := newDriver(source, newWriter(strip, 128, quiet), strip, written.logf)

	// The LED count is the controller's own fact and is logged once when it is learned.
	// These tests are about the printer, so it is already known; the line about it would
	// otherwise be counted as though it were a line about the printer.
	reader.leds = 48

	t.Cleanup(func() {
		current.Store(nil)
		sent.Store(nil)
	})

	return reader, source, written
}

// TestAStateChangeWritesOneLineAndRepeatsWriteNone is the core of the policy: the log is
// the history, so a state word that moves is one line and a printer that keeps saying the
// same word is none — and a progress tick, which moves the picture and not the state, is
// none either.
func TestAStateChangeWritesOneLineAndRepeatsWriteNone(t *testing.T) {
	reader, source, written := newLogWatch(t)

	at := time.Date(2026, time.November, 18, 22, 0, 0, 0, time.UTC)
	ctx := context.Background()

	// The first look is a baseline, not a change: nothing has moved yet.
	source.show(snapshotOf(printer.StatePrinting, true, false, at))
	reader.step(ctx, at)

	written.want(t)

	// Twenty turns of a printer that is still printing are twenty looks at the same thing.
	for i := 0; i < 20; i++ {
		reader.step(ctx, at.Add(time.Duration(i)*tickEvery))
	}

	written.want(t)

	// The job progresses, so the picture changes, and the printer still says PRINTING:
	// the light is written and the log stays empty.
	printing := snapshotOf(printer.StatePrinting, true, false, at)
	printing.Job = printer.Job{
		Live:     true,
		Running:  printer.Reading[bool]{Value: true, Known: true},
		Progress: printer.Reading[float64]{Value: 41, Known: true},
	}
	source.show(printing)
	reader.step(ctx, at.Add(21*tickEvery))

	written.want(t)

	// The state word moves. That is one line, naming the printer and both words.
	paused := snapshotOf(printer.StatePaused, true, false, at)
	paused.Job = printing.Job
	source.show(paused)
	reader.step(ctx, at.Add(22*tickEvery))

	written.want(t, "wled-printer-status: the printer Morpheus went from PRINTING to PAUSED")

	// And a printer that stays paused for twenty turns is still one line.
	for i := 0; i < 20; i++ {
		reader.step(ctx, at.Add(23*tickEvery+time.Duration(i)*tickEvery))
	}

	written.want(t, "wled-printer-status: the printer Morpheus went from PRINTING to PAUSED")

	source.show(snapshotOf(printer.StateFinished, true, false, at))
	reader.step(ctx, at.Add(60*tickEvery))

	written.want(t,
		"wled-printer-status: the printer Morpheus went from PRINTING to PAUSED",
		"wled-printer-status: the printer Morpheus went from PAUSED to FINISHED")
}

// TestAConnectionChangeWritesOneLineEach is the host's session with the printer: losing it
// is one line, regaining it is one line, and the twenty turns in between are none.
func TestAConnectionChangeWritesOneLineEach(t *testing.T) {
	reader, source, written := newLogWatch(t)

	at := time.Date(2026, time.November, 18, 22, 0, 0, 0, time.UTC)
	ctx := context.Background()

	source.show(snapshotOf(printer.StatePrinting, true, false, at))
	reader.step(ctx, at)

	written.want(t)

	source.show(snapshotOf(printer.StatePrinting, false, false, at))
	reader.step(ctx, at.Add(tickEvery))

	// The state word does not move — the host's memory still says PRINTING — so the only
	// line is the connection going.
	written.want(t, "wled-printer-status: the printer Morpheus is no longer connected")

	for i := 0; i < 20; i++ {
		reader.step(ctx, at.Add(2*tickEvery+time.Duration(i)*tickEvery))
	}

	written.want(t, "wled-printer-status: the printer Morpheus is no longer connected")

	source.show(snapshotOf(printer.StatePrinting, true, false, at))
	reader.step(ctx, at.Add(30*tickEvery))

	written.want(t,
		"wled-printer-status: the printer Morpheus is no longer connected",
		"wled-printer-status: the printer Morpheus is connected again")

	for i := 0; i < 20; i++ {
		reader.step(ctx, at.Add(31*tickEvery+time.Duration(i)*tickEvery))
	}

	written.want(t,
		"wled-printer-status: the printer Morpheus is no longer connected",
		"wled-printer-status: the printer Morpheus is connected again")
}

// TestTheReadingGoingStaleWritesOneLineEach is the honesty rule of §5.3 seen from the log:
// a reading that has aged past StaleAfter is one line when it happens, one when the printer
// speaks again, and nothing in between — a stale printer is not a line a minute.
func TestTheReadingGoingStaleWritesOneLineEach(t *testing.T) {
	reader, source, written := newLogWatch(t)

	at := time.Date(2026, time.November, 18, 22, 0, 0, 0, time.UTC)
	ctx := context.Background()

	source.show(snapshotOf(printer.StatePrinting, true, false, at))
	reader.step(ctx, at)

	written.want(t)

	source.show(snapshotOf(printer.StatePrinting, true, true, at))
	reader.step(ctx, at.Add(tickEvery))

	written.want(t, "wled-printer-status: the reading from the printer Morpheus is stale")

	for i := 0; i < 20; i++ {
		reader.step(ctx, at.Add(2*tickEvery+time.Duration(i)*tickEvery))
	}

	written.want(t, "wled-printer-status: the reading from the printer Morpheus is stale")

	source.show(snapshotOf(printer.StatePrinting, true, false, at))
	reader.step(ctx, at.Add(30*tickEvery))

	written.want(t,
		"wled-printer-status: the reading from the printer Morpheus is stale",
		"wled-printer-status: the reading from the printer Morpheus is live again")

	for i := 0; i < 20; i++ {
		reader.step(ctx, at.Add(31*tickEvery+time.Duration(i)*tickEvery))
	}

	written.want(t,
		"wled-printer-status: the reading from the printer Morpheus is stale",
		"wled-printer-status: the reading from the printer Morpheus is live again")
}

// TestATerminalJobsStalenessIsOneLineHoweverLongItLasts is the flap the operator watched
// on the night of 2026-11-19, seen from the log: a finished printer whose reading dipped
// into staleness and back every minute or two wrote two lines for every dip, for as long
// as the cooling bed took to stop moving. A job that is over has nothing new to say, so
// the first dip is the line and the ten after it are not.
func TestATerminalJobsStalenessIsOneLineHoweverLongItLasts(t *testing.T) {
	reader, source, written := newLogWatch(t)

	at := time.Date(2026, time.November, 19, 1, 6, 0, 0, time.UTC)
	ctx := context.Background()

	source.show(snapshotOf(printer.StateFinished, true, false, at))
	reader.step(ctx, at)

	written.want(t)

	// The first dip, then ten more of the shape the instance's own log recorded: stale
	// for a minute or two, live again for a few seconds as a value moved.
	for i := 0; i < 11; i++ {
		dipped := at.Add(time.Duration(2*i+1) * time.Minute)

		source.show(snapshotOf(printer.StateFinished, true, true, dipped))
		reader.step(ctx, dipped)

		source.show(snapshotOf(printer.StateFinished, true, false, dipped.Add(tickEvery)))
		reader.step(ctx, dipped.Add(tickEvery))
	}

	written.want(t, "wled-printer-status: the reading from the printer Morpheus is stale")
}

// TestAJobThatGoesBackToPrintingReportsStalenessAgain: the quiet rule belongs to a job
// that is over and to nothing else. The moment the printer says it is doing something
// again, a silence is doubt once more — the recovery is said, because that is when the
// reading came back, and the next dip is said as it always was.
func TestAJobThatGoesBackToPrintingReportsStalenessAgain(t *testing.T) {
	reader, source, written := newLogWatch(t)

	at := time.Date(2026, time.November, 19, 1, 6, 0, 0, time.UTC)
	ctx := context.Background()

	source.show(snapshotOf(printer.StateFinished, true, false, at))
	reader.step(ctx, at)

	written.want(t)

	source.show(snapshotOf(printer.StateFinished, true, true, at))
	reader.step(ctx, at.Add(tickEvery))

	written.want(t, "wled-printer-status: the reading from the printer Morpheus is stale")

	// The bed cools and the reading comes back on a job that is still over: nothing to say.
	source.show(snapshotOf(printer.StateFinished, true, false, at))
	reader.step(ctx, at.Add(2*tickEvery))

	written.want(t, "wled-printer-status: the reading from the printer Morpheus is stale")

	// A new print starts. The word moves and the reading is live again, and from here the
	// pair reports as it always did.
	source.show(snapshotOf(printer.StatePrinting, true, false, at))
	reader.step(ctx, at.Add(3*tickEvery))

	written.want(t,
		"wled-printer-status: the reading from the printer Morpheus is stale",
		"wled-printer-status: the printer Morpheus went from FINISHED to PRINTING",
		"wled-printer-status: the reading from the printer Morpheus is live again")

	source.show(snapshotOf(printer.StatePrinting, true, true, at))
	reader.step(ctx, at.Add(4*tickEvery))

	written.want(t,
		"wled-printer-status: the reading from the printer Morpheus is stale",
		"wled-printer-status: the printer Morpheus went from FINISHED to PRINTING",
		"wled-printer-status: the reading from the printer Morpheus is live again",
		"wled-printer-status: the reading from the printer Morpheus is stale")
}

// TestAHeartbeatRepaintWritesNoLine is the line a minute that must not exist. A still
// picture is repainted every heartbeatEvery so that a controller which rebooted corrects
// itself, and that repaint is routine: the log must hold nothing at all, however many
// repaints a quiet hour costs.
func TestAHeartbeatRepaintWritesNoLine(t *testing.T) {
	strip := newFakeStrip()

	clock := newFakeClock(time.Date(2026, time.November, 18, 22, 0, 0, 0, time.UTC))

	var written logs

	pen := newWriter(strip, 128, written.logf)
	clock.watch(pen)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go pen.run(ctx)

	pen.submit(frame(render.ColourIdle, render.ReasonNoLiveJob))
	strip.waitForPaints(t, 1)
	clock.waitArmed(t, 0)

	// Three repaints of a frame nothing has changed: one a minute for three minutes.
	clock.Advance(heartbeatEvery)
	strip.waitForPaints(t, 2)
	clock.Advance(heartbeatEvery)
	strip.waitForPaints(t, 3)
	clock.Advance(heartbeatEvery)
	strip.waitForPaints(t, 4)

	written.want(t)

	// The same frame offered again, as the driver offers it on every turn, is still not an
	// event: the writer has it already and the log says nothing.
	pen.submit(frame(render.ColourIdle, render.ReasonNoLiveJob))
	time.Sleep(50 * time.Millisecond)

	if shown := strip.shown(); len(shown) != 4 {
		t.Fatalf("an unchanged frame was written %d times, want the 4 repaints: %+v", len(shown), shown)
	}

	written.want(t)
}

// TestAFailedWriteStillWritesItsOneLine is the other half of the policy: a controller that
// will not take a frame is loud, and it is loud once rather than once per attempt.
func TestAFailedWriteStillWritesItsOneLine(t *testing.T) {
	strip := newFakeStrip()
	strip.stopAnswering()

	var written logs

	pen := newWriter(strip, 128, written.logf)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go pen.run(ctx)

	pen.submit(frame(render.ColourPrinting, render.ReasonProgress))
	strip.waitForPaints(t, 1)

	written.waitForCount(t, 1)

	// The window a failed write opens is a second long, so nothing else is attempted and
	// nothing else is said in this time.
	time.Sleep(50 * time.Millisecond)

	lines := written.all()
	if len(lines) != 1 {
		t.Fatalf("one failed write wrote %d lines, want 1: %q", len(lines), lines)
	}

	if !strings.Contains(lines[0], "not updated") {
		t.Fatalf("the failed write was logged as %q, want a line saying the light was not updated", lines[0])
	}
}
