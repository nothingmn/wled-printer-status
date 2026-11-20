package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nothingmn/wled-printer-status/internal/printer"
	"github.com/nothingmn/wled-printer-status/internal/render"
)

// The five proofs this feature carries, and the one thing each is protecting:
//
//   - a forced write goes out even when the picture is the one the light already has, because the
//     steady pass suppresses exactly that write and the button exists to make it;
//   - a light inside a moment is skipped and said to be skipped, because telling a person a panel
//     was refreshed while it is still showing a finished word is the worst answer this route can give;
//   - a light that did not answer is a failure with its reason, named by its address;
//   - a second press while a sweep runs is refused by code, because two sweeps over the same
//     controllers are two writes per light and two answers that disagree;
//   - nothing bound is a clear "there is nothing to refresh" rather than an empty success.
//
// The route's own scope is proved with them: a refresh writes the segment the module owns and no
// other segment of that controller.

// refreshRun is one binding a refresh test drives: the writer that owns the light, the light, and
// the driver a press goes through — which is the path the route itself takes, one hop per goroutine.
type refreshRun struct {
	pen   *writer
	strip *fakeStrip
	drive *driver
}

// runRefresh starts one writer and one driver over one printer reading, at a tick short enough that
// a test does not wait a second for a frame it is not testing.
func runRefresh(t *testing.T, pen *writer, strip *fakeStrip, snapshot printer.Snapshot) *refreshRun {
	t.Helper()

	// The reading is stamped now, because the driver judges staleness against the clock it ticks on
	// and a fixture pinned to a fixed instant is a live printer only for the half-minute after it.
	// See momentReading in moments_test.go for what that costs when it is got wrong.
	snapshot.ObservedAt, snapshot.LastChange = time.Now(), time.Now()

	// A writer built by a test has no controller address, and every sentence this route writes names
	// the light it is about, so the address the watch would have given it is given here.
	if pen.where == "" {
		pen.where = "http://10.0.0.136"
	}

	drive := newDriver(fakeReadings{snapshot: snapshot}, pen, strip, quiet)
	drive.tick = 5 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())

	t.Cleanup(cancel)
	t.Cleanup(func() {
		current.Store(nil)
		sent.Store(nil)
		forgetPreview()
	})

	go pen.run(ctx)
	go drive.run(ctx)

	return &refreshRun{pen: pen, strip: strip, drive: drive}
}

// press is one press of the button, on this binding, inside the time one light is allowed.
func (r *refreshRun) press(t *testing.T) refreshDone {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), refreshPerLight)
	defer cancel()

	return r.drive.refresh(ctx)
}

// TestARefreshWritesThePictureTheLightAlreadyShows is the whole of the button.
//
// The steady pass suppresses a frame the light already has, which is what keeps a module driving six
// lights from writing six bodies a second for a day — so the proof that this is a *forced* write is
// a second write carrying the same picture as the first. Anything less (a change to the picture, a
// different colour) would pass whether the suppression was bypassed or not.
func TestARefreshWritesThePictureTheLightAlreadyShows(t *testing.T) {
	strip := newFakeStrip()
	pen := newWriter(strip, 128, quiet)

	run := runRefresh(t, pen, strip, printingJob("printer-1", 41))

	strip.waitForPaints(t, 1)

	// Many ticks pass with the same reading, and not one of them is a write: the comparison this
	// button exists to bypass is working. The wait is long enough for several ticks to have run.
	time.Sleep(40 * time.Millisecond)

	if got := strip.wrote(); got != 1 {
		t.Fatalf("the steady pass wrote %d times for one unchanged picture, want 1: this test cannot "+
			"prove a forced write against a writer that writes everything anyway", got)
	}

	done := run.press(t)

	if done.outcome != refreshRefreshed {
		t.Fatalf("the press answered %q (%s), want %q", done.outcome, done.why, refreshRefreshed)
	}

	if done.wrote != refreshWroteSegments {
		t.Fatalf("the press wrote %q, want %q on a strip", done.wrote, refreshWroteSegments)
	}

	strip.waitForPaints(t, 2)

	shown := strip.shown()
	if len(shown) != 2 {
		t.Fatalf("the controller took %d writes, want exactly 2: the one frame and the refreshed copy", len(shown))
	}

	if !samePaint(shown[0], shown[1]) {
		t.Fatalf("the refreshed write drew %+v and the frame before it drew %+v: the point of the "+
			"press is that the same picture goes out again", shown[1], shown[0])
	}
}

// TestARefreshLeavesALightInsideAMomentAlone is the answer a person must never be given wrongly.
//
// The finished moment is showing its own picture, held for thirty seconds of the test's clock. A
// forced write under it would paint the steady frame over the thing the operator is looking at, and
// an answer of "refreshed" would be a statement about a panel that is showing something else.
func TestARefreshLeavesALightInsideAMomentAlone(t *testing.T) {
	strip := newPanelStrip()
	pen := newWriter(strip, 128, quiet)

	clock := newMomentClock()
	clock.watch(pen)
	pushInto(pen)

	// A colour, so the moment's picture is one the fake controller records and a test can see it
	// arrive; thirty seconds, so the hold is still in force when the press happens.
	entry := momentEntry{Enabled: true, Shows: showsColour, Which: printer.StateError, For: "30s"}

	source := &changingReadings{}
	source.show(printingAt(41, time.Now()))

	drive := newDriver(source, pen, strip, quiet)
	drive.store = momentTableFor(momentPrintFinished, entry)
	drive.tick = 5 * time.Millisecond

	pen.store = drive.store

	ctx, cancel := context.WithCancel(context.Background())

	t.Cleanup(cancel)
	t.Cleanup(func() {
		current.Store(nil)
		sent.Store(nil)
		forgetPreview()
	})

	go pen.run(ctx)
	go drive.run(ctx)

	waitFor(t, "the printing frame", func() bool { return painted(strip, render.ColourPrinting) })

	// The print finishes, and the moment takes the light.
	source.show(finishedAt("printer-1"))

	waitFor(t, "the moment's own picture", func() bool { return painted(strip, render.ColourError) })

	before := strip.wrote()

	press, pressCancel := context.WithTimeout(context.Background(), refreshPerLight)
	done := drive.refresh(press)
	pressCancel()

	if done.outcome != refreshSkipped {
		t.Fatalf("a light inside a moment answered %q (%s), want %q", done.outcome, done.why, refreshSkipped)
	}

	if !strings.Contains(done.why, "moment") {
		t.Fatalf("the refusal reads %q, and a person has to be told it is inside a moment", done.why)
	}

	// The hold is thirty seconds of the writer's clock, so nothing about the press may have written:
	// a frame under the moment would be the picture gone early.
	if got := strip.wrote(); got != before {
		t.Fatalf("the controller took %d writes during the press, and the light is inside a moment "+
			"that owns it: before %d, after %d", got-before, before, got)
	}
}

// TestARefreshNamesALightThatDidNotAnswer is the per-light half of the answer: a controller that
// takes nothing is a failure carrying its address and the reason, and never a refreshed row.
func TestARefreshNamesALightThatDidNotAnswer(t *testing.T) {
	strip := newFakeStrip()
	pen := newWriter(strip, 128, quiet)

	run := runRefresh(t, pen, strip, printingJob("printer-1", 41))

	strip.waitForPaints(t, 1)

	// The controller is up enough to be asked and will take no frame, which is the state the
	// installed module found a real panel in.
	strip.stopAnswering()

	done := run.press(t)

	if done.outcome != refreshFailed {
		t.Fatalf("a light that took no write answered %q (%s), want %q", done.outcome, done.why, refreshFailed)
	}

	if !strings.Contains(done.why, "did not take the picture") {
		t.Fatalf("the failure reads %q, and it has to say the light did not take the picture", done.why)
	}

	if !strings.Contains(done.why, "10.0.0.136") && pen.where != "" {
		t.Fatalf("the failure reads %q, and a per-light answer has to name the light", done.why)
	}
}

// TestARefreshLeavesAPausedLightAlone is the other picture that owns a light: a pause is one write
// and then nothing at all, and a refresh is writes. The light must be told nothing, and the answer
// must say why rather than counting it as refreshed.
func TestARefreshLeavesAPausedLightAlone(t *testing.T) {
	strip := newFakeStrip()
	pen := newWriter(strip, 128, quiet)

	// A pause in force for an hour from now, so the press lands in the middle of it.
	expires := time.Now().Add(time.Hour)
	pen.store = &handTable{pauses: []storedPause{{
		Scope:   pauseGlobal,
		Chosen:  "1h",
		SetAt:   time.Now(),
		Expires: &expires,
	}}}

	run := runRefresh(t, pen, strip, printingJob("printer-1", 41))

	// The pause's own write: the light goes off, once, and that write is a power write rather than
	// a frame — which is why it is counted here and not as a paint.
	waitFor(t, "the light to be switched off by the pause", func() bool { return len(strip.powerWrites()) > 0 })

	before := len(strip.shown())
	powers := len(strip.powerWrites())

	done := run.press(t)

	if done.outcome != refreshSkipped {
		t.Fatalf("a paused light answered %q (%s), want %q", done.outcome, done.why, refreshSkipped)
	}

	if !strings.Contains(done.why, "paused") {
		t.Fatalf("the refusal reads %q, and it has to say the light is paused", done.why)
	}

	if got := len(strip.shown()); got != before {
		t.Fatalf("the controller took %d frames during the press, and a pause is one write and then "+
			"nothing at all: before %d, after %d", got-before, before, got)
	}

	if got := len(strip.powerWrites()); got != powers {
		t.Fatalf("the controller took %d power writes during the press, want none", got-powers)
	}
}

// TestARefreshWritesOnlyTheSegmentTheModuleOwns is the scope: "all segments" is not "every segment
// on the device". The module borrows one zone of this strip, and the operator's other five are his.
func TestARefreshWritesOnlyTheSegmentTheModuleOwns(t *testing.T) {
	strip := borrowedStrip()
	pen := newSegmentWriter(strip, 1, "http://10.0.0.253", quiet)

	run := runRefresh(t, pen, strip, printingJob("printer-1", 41))

	strip.waitForPaints(t, 1)

	done := run.press(t)

	if done.outcome != refreshRefreshed {
		t.Fatalf("the press answered %q (%s), want %q", done.outcome, done.why, refreshRefreshed)
	}

	shown := strip.shown()

	for index, call := range shown {
		if len(call.segments) != 1 {
			t.Fatalf("write %d carried %d segments, and a second one would be one of the operator's: %+v",
				index, len(call.segments), call.segments)
		}

		if call.segments[0].ID != 1 {
			t.Fatalf("write %d named segment %d, and only segment 1 was borrowed", index, call.segments[0].ID)
		}
	}
}

// TestARefreshWithNothingBoundSaysSo is the empty case, through the process's own mux: a fresh
// install drives nothing, and the answer says there is nothing to refresh rather than an empty
// success a screen would draw as a blank panel.
func TestARefreshWithNothingBoundSaysSo(t *testing.T) {
	forgetPreview()

	recorder := httptest.NewRecorder()
	routes().ServeHTTP(recorder, forwarded(http.MethodPost, "/refresh", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("POST /refresh answered %d, want %d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}

	var body refreshAnswer
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("the answer is not JSON: %v (%s)", err, recorder.Body.String())
	}

	if body.Count != 0 || len(body.Lights) != 0 {
		t.Fatalf("the answer covers %d bindings (%d rows), want none", body.Count, len(body.Lights))
	}

	if !strings.Contains(body.Note, "nothing to refresh") {
		t.Fatalf("the note reads %q, and it has to say there is nothing to refresh", body.Note)
	}
}

// TestASecondPressIsRefusedWhileOneRuns is the double-fire guard, at the route and not in the
// screen: two browsers, or a double press on a page that has not been told yet, arrive here.
func TestASecondPressIsRefusedWhileOneRuns(t *testing.T) {
	strip := newFakeStrip()
	strip.state = deskLight()

	pen := newWriter(strip, 128, quiet)

	// The write is held open, so the first press is inside it when the second one arrives — which is
	// the only way to prove the refusal rather than a fast sweep and a lucky second request.
	held := make(chan struct{})

	// Released twice on the way out — once by the test, once by its cleanup, whichever gets there
	// first — so it is released once.
	release := sync.OnceFunc(func() { close(held) })
	strip.mu.Lock()
	strip.hold = held
	strip.mu.Unlock()

	drive := newDriver(fakeReadings{snapshot: printingJob("printer-1", 41)}, pen, strip, quiet)
	drive.tick = 5 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())

	t.Cleanup(cancel)
	t.Cleanup(func() {
		release()
		current.Store(nil)
		sent.Store(nil)
		forgetPreview()
	})

	go pen.run(ctx)
	go drive.run(ctx)

	offerPreview(&previewTarget{
		pen:     pen,
		drive:   drive,
		where:   pen.where,
		light:   lightName(pen.where),
		binding: "printer-1",
		printer: "printer-1",
		segment: -1,
		whole:   true,
	})

	first := make(chan *httptest.ResponseRecorder, 1)

	go func() {
		recorder := httptest.NewRecorder()
		routes().ServeHTTP(recorder, forwarded(http.MethodPost, "/refresh", nil))

		first <- recorder
	}()

	// The sweep is inside the write once the controller has been handed a frame.
	select {
	case <-strip.painted:
	case <-time.After(howLong):
		t.Fatal("the first press never reached the controller")
	}

	waitFor(t, "the sweep to be running", func() bool { return sweepingRefresh.Load() })

	second := httptest.NewRecorder()
	routes().ServeHTTP(second, forwarded(http.MethodPost, "/refresh", nil))

	if second.Code != http.StatusConflict {
		t.Fatalf("the second press answered %d, want %d: %s", second.Code, http.StatusConflict, second.Body.String())
	}

	var problem struct {
		Code   string `json:"code"`
		Detail string `json:"detail"`
	}

	if err := json.Unmarshal(second.Body.Bytes(), &problem); err != nil {
		t.Fatalf("the refusal is not JSON: %v (%s)", err, second.Body.String())
	}

	if !strings.HasSuffix(problem.Code, "REFRESH_BUSY") {
		t.Fatalf("the refusal code reads %q, want REFRESH_BUSY", problem.Code)
	}

	if !strings.Contains(problem.Detail, "already running") {
		t.Fatalf("the refusal detail reads %q, and it has to say a sweep is already running", problem.Detail)
	}

	// The first press finishes with the per-light answer once the controller takes the write.
	strip.mu.Lock()
	strip.hold = nil
	strip.mu.Unlock()

	release()

	select {
	case recorder := <-first:
		if recorder.Code != http.StatusOK {
			t.Fatalf("the first press answered %d, want %d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
		}

		var body refreshAnswer
		if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
			t.Fatalf("the first answer is not JSON: %v (%s)", err, recorder.Body.String())
		}

		if body.Count != 1 || body.Refreshed != 1 {
			t.Fatalf("the first answer covers %d bindings with %d refreshed, want one and one: %s",
				body.Count, body.Refreshed, recorder.Body.String())
		}
	case <-time.After(howLong):
		t.Fatal("the first press never answered")
	}
}

// TestTheRefreshRouteIsOnTheProcesssOwnMux is the mount, not the handler: a route described in
// openapi.json and proved in a handler test of its own is still a 404 for the operator if nobody
// mounted it in routes().
func TestTheRefreshRouteIsOnTheProcesssOwnMux(t *testing.T) {
	forgetPreview()

	recorder := httptest.NewRecorder()
	routes().ServeHTTP(recorder, forwarded(http.MethodPost, "/refresh", nil))

	if recorder.Code == http.StatusNotFound {
		t.Fatalf("POST /refresh answered 404: the route is not on the process's own mux")
	}
}

// finishedAt is one printer that has just finished a job, stamped now, so the finished moment fires
// and its hold is measured from this run rather than from a date in the past.
func finishedAt(id string) printer.Snapshot {
	now := time.Now()

	return printer.Snapshot{
		ID:        id,
		Name:      id,
		Connected: true,
		State:     printer.Reading[string]{Value: printer.StateFinished, Known: true},
		Job: printer.Job{
			Live:     true,
			Running:  printer.Reading[bool]{Value: false, Known: true},
			Progress: printer.Reading[float64]{Value: 100, Known: true},
		},
		LastChange: now,
		ObservedAt: now,
	}
}
