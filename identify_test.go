// The Devices screen's `identify`: the light goes on and off so that a person can see which light a
// name means, and is then put back exactly as it was found.
//
// Every test here runs against a light that is a fake, a clock the test moves, and the writer the
// process really runs — so what is proved is the flash's own timing and give-back rather than a mock's
// idea of them. Nothing here flashes a light: the one press against real hardware is the verification
// wave's, with the operator at his desk.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nothingmn/wled-printer-status/internal/render"
	"github.com/nothingmn/wled-printer-status/internal/wled"
)

// timedStrip is the fake controller with the moment of every power write beside it.
//
// §2.6 says the light is on and off "one second each state", and a count of writes cannot tell a flash
// that is one second apart from one that is not. The writer reads its clock for every wait it makes, so
// reading the same clock at the write is what makes the spacing a fact rather than an assumption.
type timedStrip struct {
	*fakeStrip
	clock *fakeClock

	mu     sync.Mutex
	powers []bool
	at     []time.Time
}

func (s *timedStrip) SetPower(ctx context.Context, on bool) error {
	// The moment is taken before the write, from the clock the writer waits on: the fake clock only
	// moves when the test moves it, so this is the instant the writer believed it was writing at.
	at := s.clock.Now()

	if err := s.fakeStrip.SetPower(ctx, on); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.powers = append(s.powers, on)
	s.at = append(s.at, at)

	return nil
}

// since is every power write after the first count, with the moment of each.
func (s *timedStrip) since(count int) ([]bool, []time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if count > len(s.powers) {
		count = len(s.powers)
	}

	return append([]bool(nil), s.powers[count:]...), append([]time.Time(nil), s.at[count:]...)
}

// stateWait waits until the writer is waiting on a clock instant within one state of now, which is the
// wait it arms for the next state of a flash. It is waited for rather than assumed because the writer
// arms it after the write it has just made, and a test that advanced before then would be moving the
// clock past a wait that did not exist yet — a flash that took two seconds a state, for a reason no
// reader of the failure could see.
func (c *fakeClock) stateWait(t *testing.T) {
	t.Helper()

	deadline := time.Now().Add(howLong)

	for {
		c.mu.Lock()

		armed := false

		for _, wait := range c.waits {
			if !wait.due.After(c.at.Add(identifyState)) {
				armed = true

				break
			}
		}

		c.mu.Unlock()

		if armed {
			return
		}

		if time.Now().After(deadline) {
			t.Fatalf("the writer armed no wait within %s of the clock: the flash is not running", identifyState)
		}

		time.Sleep(time.Millisecond)
	}
}

// waitForPowerWrites waits until the controller has taken want power writes since the flash began, and
// answers them.
func waitForPowerWrites(t *testing.T, strip *timedStrip, baseline, want int) []bool {
	t.Helper()

	deadline := time.Now().Add(howLong)

	for {
		if powers, _ := strip.since(baseline); len(powers) >= want {
			return powers
		}

		if time.Now().After(deadline) {
			powers, _ := strip.since(baseline)
			t.Fatalf("the light took %d power writes in %s, wanted %d: %v", len(powers), howLong, want, powers)
		}

		time.Sleep(time.Millisecond)
	}
}

// identifyHarness is one light, the writer that owns it, and the Devices routes over both, with a clock
// the test moves.
type identifyHarness struct {
	t     *testing.T
	mux   http.Handler
	pen   *writer
	strip *timedStrip
	clock *fakeClock
	at    time.Time
}

// newIdentifyHarness builds the routes over a light that is a fake, driven by the writer the process
// really runs.
//
// borrowed builds the writer the way a binding on one segment of somebody else's controller is built,
// which is the module's own constructor for it rather than a flag on this one. store is this module's own
// configuration as the writer reads it — the pause, above all — and it is handed over before the writer
// starts, because swapping a store underneath a running writer is the race the frames tests warn about.
func newIdentifyHarness(t *testing.T, borrowed bool, store lights, state wled.State, stateErr error) *identifyHarness {
	t.Helper()

	at := time.Date(2026, time.November, 19, 20, 0, 0, 0, time.UTC)
	clock := newFakeClock(at)

	light := newFakeStrip()
	light.state = state
	light.stateErr = stateErr

	strip := &timedStrip{fakeStrip: light, clock: clock}

	var pen *writer

	if borrowed {
		pen = newSegmentWriter(strip, 1, "http://10.0.0.136", quiet)
	} else {
		pen = newWriter(strip, 128, quiet)
		pen.where = "http://10.0.0.136"
	}

	pen.store = store

	clock.watch(pen)

	// The set the Devices routes resolve a name against, and the offer that lets a route reach the
	// writer driving it — the same two things the process publishes, and nothing else.
	live := &runner{held: bindings{{
		ID: "printer-a", DeviceID: "printer-a", Address: "http://10.0.0.136", Segment: -1, Brightness: 128,
	}}}

	offerPreview(&previewTarget{pen: pen, where: "http://10.0.0.136", light: "10.0.0.136"})

	t.Cleanup(forgetPreview)

	mux := http.NewServeMux()
	deviceAPI{live: live, clock: clock.Now}.mount(mux)

	ctx, stop := context.WithCancel(context.Background())
	t.Cleanup(stop)

	go pen.run(ctx)

	// The writer's own start-up, waited out before any test presses anything: a whole light is switched
	// on once (writer.settle), a paused one is switched off once, and a borrowed segment is borrowed and
	// written no power at all. None of it is the flash, and a test that read the power writes before it
	// landed would count the light being switched on at start as the flash's first state.
	if !borrowed && store == nil {
		waitForPowerWrites(t, strip, 0, 1)
	}

	return &identifyHarness{t: t, mux: mux, pen: pen, strip: strip, clock: clock, at: at}
}

// press is one POST /devices/{name}/identify, and the status and body it answered with.
func (h *identifyHarness) press() (int, map[string]any) {
	h.t.Helper()

	return h.pressOn("10.0.0.136")
}

// pressOn is one press aimed at the light named.
func (h *identifyHarness) pressOn(name string) (int, map[string]any) {
	h.t.Helper()

	path := "/devices/" + name + "/identify"

	recorder := httptest.NewRecorder()
	h.mux.ServeHTTP(recorder, forwarded(http.MethodPost, path, nil))

	var body map[string]any
	if raw := recorder.Body.Bytes(); len(raw) > 0 {
		if err := json.Unmarshal(raw, &body); err != nil {
			h.t.Fatalf("POST %s answered something that is not JSON: %v (%s)", path, err, raw)
		}
	}

	return recorder.Code, body
}

// endState ends one state of the flash: it waits for the writer's one-second wait for it, then moves the
// clock to exactly the instant it is due.
func (h *identifyHarness) endState() {
	h.t.Helper()

	h.clock.stateWait(h.t)
	h.clock.Advance(identifyState)
}

// flash presses identify and walks the writer through the whole flash, answering the power writes the
// light took after baseline and the moment of each.
//
// Eleven of them, and the arithmetic is the requirement's: on, off, five times is ten states, and the
// eleventh is the give-back putting the power back where it was found.
func (h *identifyHarness) flash(baseline int) ([]bool, []time.Time) {
	h.t.Helper()

	status, body := h.press()
	if status != http.StatusAccepted {
		h.t.Fatalf("identify answered %d: %v", status, body)
	}

	powers := waitForPowerWrites(h.t, h.strip, baseline, 1)
	if len(powers) != 1 {
		h.t.Fatalf("the route answered after %d power writes, want 1: it answers as the flash starts, not when it ends", len(powers))
	}

	for want := 2; want <= identifyFlashes*2+1; want++ {
		h.endState()

		waitForPowerWrites(h.t, h.strip, baseline, want)
	}

	powers, at := h.strip.since(baseline)

	return powers, at
}

// ---------------------------------------------------------------------------------------------
// The flash itself
// ---------------------------------------------------------------------------------------------

// TestIdentifyFlashesTheLightOnAndOffOneSecondInEachState is the requirement's own sentence, measured:
// the light goes on, then off, one second apart, five times — and the route answers while it is
// happening rather than after it.
func TestIdentifyFlashesTheLightOnAndOffOneSecondInEachState(t *testing.T) {
	harness := newIdentifyHarness(t, false, nil, wled.State{
		On: true, Brightness: 128, Segments: []wled.Segment{{ID: 0, Start: 0, Stop: 48}},
	}, nil)

	baseline := len(harness.strip.powerWrites())

	status, body := harness.press()
	if status != http.StatusAccepted {
		t.Fatalf("identify answered %d: %v", status, body)
	}

	// The plan, before the flash has run: what will happen, where the light started, and when it ends.
	if flashes, _ := body["flashes"].(float64); int(flashes) != identifyFlashes {
		t.Fatalf("the answer promises %v flashes, want %d: %v", body["flashes"], identifyFlashes, body)
	}

	if per, _ := body["seconds_per_state"].(float64); per != identifyState.Seconds() {
		t.Fatalf("the answer promises %v seconds a state, want %v: %v", body["seconds_per_state"], identifyState.Seconds(), body)
	}

	if wasOn, ok := body["was_on"].(bool); !ok || !wasOn {
		t.Fatalf("the answer says was_on %v (present %t), want true: the light was found on: %v", body["was_on"], ok, body)
	}

	// The two-second write, not the ten-second one: the call was answered as the flash began.
	if got := len(harness.strip.powerWrites()) - baseline; got != 1 {
		t.Fatalf("the route answered after %d power writes, want 1: it holds the call for the whole flash otherwise", got)
	}

	powers := waitForPowerWrites(t, harness.strip, baseline, 1)

	for want := 2; want <= identifyFlashes*2+1; want++ {
		harness.endState()

		powers = waitForPowerWrites(t, harness.strip, baseline, want)
	}

	_, at := harness.strip.since(baseline)

	// On, then off, five times, and then the give-back switching it back on: eleven writes.
	wantSequence := []bool{true, false, true, false, true, false, true, false, true, false, true}
	if len(powers) != len(wantSequence) {
		t.Fatalf("the flash made %d power writes, want %d: %v", len(powers), len(wantSequence), powers)
	}

	for i, want := range wantSequence {
		if powers[i] != want {
			t.Fatalf("power write %d was %t, want %t: the whole flash reads %v", i+1, powers[i], want, powers)
		}
	}

	if len(at) != len(wantSequence) {
		t.Fatalf("the light took %d power writes and %d moments, which cannot both be right", len(powers), len(at))
	}

	// One second in each state, measured on the clock the writer waits on. The last gap is the
	// give-back, which follows the fifth "off" by its own second.
	for i := 1; i < len(at); i++ {
		if gap := at[i].Sub(at[i-1]); gap != identifyState {
			t.Fatalf("power write %d came %s after the one before it, want %s: the states are %v", i+1, gap, identifyState, at)
		}
	}

	if total := at[len(at)-1].Sub(at[0]); total != identifyFlashes*2*identifyState {
		t.Fatalf("the flash ran %s from the first write to the give-back, want the requirement's %s",
			total, identifyFlashes*2*identifyState)
	}
}

// TestIdentifyPutsTheLightBackAsItWasFound is the half §2.6 calls the promise: the light is put back
// exactly as it was found — the segments it was drawing, the brightness it was at, and the power it was
// in — rather than left dark or left at whatever the flash happened to end on.
func TestIdentifyPutsTheLightBackAsItWasFound(t *testing.T) {
	harness := newIdentifyHarness(t, false, nil, wled.State{
		On: true, Brightness: 96, Segments: []wled.Segment{{ID: 0, Name: "Desk", Start: 0, Stop: 48}},
	}, nil)

	baseline := len(harness.strip.powerWrites())

	harness.flash(baseline)

	// The give-back is the last thing the light was told: its own segment, its own brightness, and the
	// power it was found in.
	restores := harness.strip.givenBack()
	if len(restores) != 1 {
		t.Fatalf("the give-back wrote %d segments back, want the 1 the light was drawing: %v", len(restores), restores)
	}

	if restores[0].Name != "Desk" || restores[0].Start != 0 || restores[0].Stop != 48 {
		t.Fatalf("the give-back wrote %+v back, want the segment the light was drawing", restores[0])
	}

	powers := waitForPowerWrites(t, harness.strip, baseline, identifyFlashes*2+1)
	if !powers[len(powers)-1] {
		t.Fatalf("the flash left the light off: its power writes end %v, want the give-back to switch it back on", powers)
	}

	if levels := harness.strip.brightnessWrites(); levels[len(levels)-1] != 96 {
		t.Fatalf("the give-back left the brightness at %d, want the 96 the light was found at: %v",
			levels[len(levels)-1], levels)
	}
}

// TestIdentifyLeavesALightItFoundOffOff is the other side of "exactly as it was found": a light that was
// dark when the button was pressed is flashed, because that is what the button is for, and is dark again
// when it ends. Leaving it on would be this module turning a light on that nobody asked it to.
func TestIdentifyLeavesALightItFoundOffOff(t *testing.T) {
	harness := newIdentifyHarness(t, false, nil, wled.State{
		On: false, Brightness: 128, Segments: []wled.Segment{{ID: 0, Start: 0, Stop: 48}},
	}, nil)

	baseline := len(harness.strip.powerWrites())

	status, body := harness.press()
	if status != http.StatusAccepted {
		t.Fatalf("identify answered %d: %v", status, body)
	}

	if wasOn, ok := body["was_on"].(bool); !ok || wasOn {
		t.Fatalf("the answer says was_on %v (present %t), want false: the light was found off: %v", body["was_on"], ok, body)
	}

	waitForPowerWrites(t, harness.strip, baseline, 1)

	for want := 2; want <= identifyFlashes*2+1; want++ {
		harness.endState()

		waitForPowerWrites(t, harness.strip, baseline, want)
	}

	powers := waitForPowerWrites(t, harness.strip, baseline, identifyFlashes*2+1)

	// Five on, five off, and the give-back — which is off as well, because that is what was found.
	if powers[0] != true {
		t.Fatalf("the flash did not switch a dark light on, so there was nothing to see: %v", powers)
	}

	if powers[len(powers)-1] != false {
		t.Fatalf("the flash left a light that was found off on: %v", powers)
	}
}

// TestIdentifyRepaintsTheSteadyPictureInTheSamePass is the rule that matters more than the flash: the
// hand-back to the writer is immediate. The light has been dark, so the frame the writer last delivered
// is no evidence of anything — and a writer that waited for its sixty-second heartbeat to paint it
// would leave the light on its own segments, or dark, for up to a minute after the flash.
func TestIdentifyRepaintsTheSteadyPictureInTheSamePass(t *testing.T) {
	harness := newIdentifyHarness(t, false, nil, wled.State{
		On: true, Brightness: 128, Segments: []wled.Segment{{ID: 0, Start: 0, Stop: 48}},
	}, nil)

	// The picture the light is showing before anybody presses anything.
	harness.pen.submit(frame(render.ColourPrinting, render.ReasonProgress))
	harness.strip.waitForPaints(t, 1)

	baseline := len(harness.strip.powerWrites())

	harness.flash(baseline)

	// No clock is advanced here: the repaint is what the writer does as the flash lets go, not what
	// the heartbeat would do a minute later.
	harness.strip.waitForPaints(t, 2)

	shown := harness.strip.shown()
	if len(shown) != 2 {
		t.Fatalf("the light was painted %d times, want the picture and its repaint: %+v", len(shown), shown)
	}

	if !samePaint(shown[1], shown[0]) {
		t.Fatalf("the repaint painted %+v, want the picture that was on the light before the flash, %+v",
			shown[1], shown[0])
	}
}

// TestIdentifyJoinsAFlashThatIsAlreadyRunning is one flash at a time: a second press inside the ten
// seconds is answered with the flash that is really running — the same end, and no second blink queued
// behind it for somebody who has already walked away.
func TestIdentifyJoinsAFlashThatIsAlreadyRunning(t *testing.T) {
	harness := newIdentifyHarness(t, false, nil, wled.State{
		On: true, Brightness: 128, Segments: []wled.Segment{{ID: 0, Start: 0, Stop: 48}},
	}, nil)

	baseline := len(harness.strip.powerWrites())

	firstStatus, first := harness.press()
	if firstStatus != http.StatusAccepted {
		t.Fatalf("the first identify answered %d: %v", firstStatus, first)
	}

	secondStatus, second := harness.press()
	if secondStatus != http.StatusAccepted {
		t.Fatalf("the second identify answered %d, want 202: %v", secondStatus, second)
	}

	if already, _ := second["already"].(bool); !already {
		t.Fatalf("the second press says already=%v, want true: it joined a flash that was running: %v", second["already"], second)
	}

	if second["ends_at"] != first["ends_at"] {
		t.Fatalf("the second press says the flash ends at %v and the first said %v: they are two flashes, not one",
			second["ends_at"], first["ends_at"])
	}

	// One flash's worth of writes, not two: eleven.
	for want := 2; want <= identifyFlashes*2+1; want++ {
		harness.endState()

		waitForPowerWrites(t, harness.strip, baseline, want)
	}

	// Nothing is queued behind it. A further state of the clock writes nothing at all, which is what a
	// second flash waiting its turn would do.
	harness.clock.Advance(identifyState)
	time.Sleep(20 * time.Millisecond)

	if powers, _ := harness.strip.since(baseline); len(powers) != identifyFlashes*2+1 {
		t.Fatalf("the light took %d power writes, want the one flash's %d: a second one is running: %v",
			len(powers), identifyFlashes*2+1, powers)
	}
}

// ---------------------------------------------------------------------------------------------
// The refusals
// ---------------------------------------------------------------------------------------------

// TestIdentifyRefusesALightItDrivesOneBorrowedSegmentOf is the refusal the whole route turns on: power
// on a WLED controller is not a segment's, so a flash there would take the owner's segments — and their
// picture — off and on with it. Nothing is written, and the answer names the segment.
func TestIdentifyRefusesALightItDrivesOneBorrowedSegmentOf(t *testing.T) {
	harness := newIdentifyHarness(t, true, nil, wled.State{
		On: true, Brightness: 128, Segments: []wled.Segment{
			{ID: 0, Start: 0, Stop: 24},
			{ID: 1, Name: "Back 1", Start: 24, Stop: 48},
		},
	}, nil)

	status, body := harness.press()
	if status != http.StatusConflict {
		t.Fatalf("identify on a borrowed segment answered %d, want 409: %v", status, body)
	}

	if code, _ := body["code"].(string); !strings.HasSuffix(code, "IDENTIFY_BORROWED_SEGMENT") {
		t.Fatalf("the refusal carries %q, want IDENTIFY_BORROWED_SEGMENT: %v", code, body)
	}

	detail, _ := body["detail"].(string)
	if !strings.Contains(detail, "segment 1") || !strings.Contains(detail, "10.0.0.136") {
		t.Fatalf("the refusal does not name the segment and the controller: %q", detail)
	}

	// Not one power write: the owner's strip is untouched.
	if powers := harness.strip.powerWrites(); len(powers) != 0 {
		t.Fatalf("the refusal wrote power to somebody else's strip: %v", powers)
	}
}

// TestIdentifyRefusesAPausedLight is the deliberate choice about a pause: a pause is the operator's own
// instruction that this light be left alone — one write and then nothing at all — and a flash is ten
// seconds of writes, which is the opposite of it. The refusal says when the pause ends, so the button
// can be offered again by itself rather than failing for no stated reason.
func TestIdentifyRefusesAPausedLight(t *testing.T) {
	at := time.Date(2026, time.November, 19, 20, 0, 0, 0, time.UTC)

	until := at.Add(10 * time.Minute)

	table := &handTable{}
	table.pausing(storedPause{Scope: pauseGlobal, Chosen: pause10m, SetAt: at, Expires: &until})

	harness := newIdentifyHarness(t, false, table, wled.State{
		On: true, Brightness: 128, Segments: []wled.Segment{{ID: 0, Start: 0, Stop: 48}},
	}, nil)

	// The pause's own one write, which is the light being switched off.
	waitForPowerWrites(t, harness.strip, 0, 1)

	status, body := harness.press()
	if status != http.StatusConflict {
		t.Fatalf("identify on a paused light answered %d, want 409: %v", status, body)
	}

	if code, _ := body["code"].(string); !strings.HasSuffix(code, "IDENTIFY_PAUSED") {
		t.Fatalf("the refusal carries %q, want IDENTIFY_PAUSED: %v", code, body)
	}

	detail, _ := body["detail"].(string)
	if !strings.Contains(detail, until.UTC().Format(time.RFC3339)) {
		t.Fatalf("the refusal does not say when the pause ends (%s): %q", until.UTC().Format(time.RFC3339), detail)
	}

	// The pause's one write and no more: the flash did not write.
	if powers := harness.strip.powerWrites(); len(powers) != 1 {
		t.Fatalf("the flash wrote power to a paused light: %v", powers)
	}
}

// TestIdentifyRefusesALightThatDoesNotAnswer is the honest answer instead of a timeout: the flash reads
// the light first, so that it can be put back exactly as it was found, and a light that cannot be read
// cannot be flashed. Nothing is written, and the answer is the module's own 503 rather than a call the
// host gives up on.
func TestIdentifyRefusesALightThatDoesNotAnswer(t *testing.T) {
	harness := newIdentifyHarness(t, false, nil, wled.State{}, errLightGone)

	// The writer's own start-up settle still switches the light on, best effort; what matters is that
	// the flash writes nothing after that.
	baseline := len(harness.strip.powerWrites())

	status, body := harness.press()
	if status != http.StatusServiceUnavailable {
		t.Fatalf("identify on a light that does not answer answered %d, want 503: %v", status, body)
	}

	if code, _ := body["code"].(string); !strings.HasSuffix(code, "IDENTIFY_UNREACHABLE") {
		t.Fatalf("the refusal carries %q, want IDENTIFY_UNREACHABLE: %v", code, body)
	}

	detail, _ := body["detail"].(string)
	if !strings.Contains(detail, "not answering") {
		t.Fatalf("the refusal does not say the light could not be read: %q", detail)
	}

	if powers := harness.strip.powerWrites(); len(powers) != baseline {
		t.Fatalf("a light that could not be read was written power anyway: %v", powers)
	}
}

// TestIdentifyRefusesALightThisModuleDoesNotDrive is the route's own naming rule: a light that is not in
// the running set is a 404 with the names it does drive, exactly as every other route on this surface
// answers one.
func TestIdentifyRefusesALightThisModuleDoesNotDrive(t *testing.T) {
	harness := newIdentifyHarness(t, false, nil, wled.State{On: true}, nil)

	status, body := harness.pressOn("10.0.0.999")
	if status != http.StatusNotFound {
		t.Fatalf("identify on a light this module does not drive answered %d, want 404: %v", status, body)
	}

	if code, _ := body["code"].(string); !strings.HasSuffix(code, "CONTROLLER_NOT_FOUND") {
		t.Fatalf("the refusal carries %q, want CONTROLLER_NOT_FOUND: %v", code, body)
	}
}

// errLightGone is a light that is not answering at all, which is what a controller that has been
// unplugged looks like from here.
var errLightGone = errors.New("the light is not answering")
