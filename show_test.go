package main

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nothingmn/wled-printer-status/internal/printer"
	"github.com/nothingmn/wled-printer-status/internal/render"
	"github.com/nothingmn/wled-printer-status/internal/text"
	"github.com/nothingmn/wled-printer-status/internal/wled"
)

// handTable is this module's own store as a test writes it: the three questions the painter asks,
// answered from values set by hand, with no file and no clock of its own.
//
// It answers through the same functions the real reader uses — `globalPause`, `endOf`,
// `brightnessSchedule.level` — so a test about what the light does is a test about the rule and not
// about a mock's idea of the rule.
type handTable struct {
	mu       sync.Mutex
	states   map[string]show
	pauses   []storedPause
	windows  []scheduleWindow
	zone     *time.Location
	zoneName string
}

func (h *handTable) show(state string) (show, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()

	one, known := h.states[state]

	return one, known
}

func (h *handTable) paused(at time.Time) (pauseEnd, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if one, held := globalPause(h.pauses, at); held {
		return endOf(one), true
	}

	return pauseEnd{}, false
}

func (h *handTable) level(at time.Time) (brightnessNow, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if len(h.windows) == 0 || h.zone == nil {
		return brightnessNow{}, false
	}

	judged := &brightnessSchedule{windows: h.windows, zone: h.zone, zoneName: h.zoneName}

	level, why, covered := judged.level(at)

	return brightnessNow{level: level, why: why, covered: covered}, true
}

// pausing puts one pause into the table, which is what PUT /pause does to the store.
func (h *handTable) pausing(one storedPause) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.pauses = append(h.pauses, one)
}

// resume takes the pause out of the table, which is what DELETE /pause does to the store.
func (h *handTable) resume() {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.pauses = nil
}

// lightsOf is the real reader over a real document, judged in one zone: the shape the process
// builds, with the `timezone` setting handed over as a value instead of read from the settings.
func lightsOf(tables *configTables, zone string) *storeLights {
	return &storeLights{
		tables:  tables,
		zone:    func() string { return zone },
		logging: quiet,
	}
}

// storeHolding writes one document into a directory of the test's own and answers the tables a
// route — and now the painter — read it through.
func storeHolding(t *testing.T, config storedConfig) *configTables {
	t.Helper()

	dir := t.TempDir()

	tables := &configTables{}
	tables.grant(dir)

	config.Version = storeVersion

	if _, err := saveStore(dir, config); err != nil {
		t.Fatalf("write the store: %v", err)
	}

	return tables
}

// watchOne runs one writer and one driver over one binding, at a tick short enough that a test does
// not wait for a second, and hands both of them the store under test.
func watchOne(t *testing.T, pen *writer, snapshot printer.Snapshot, light strip, store lights) {
	t.Helper()

	pen.store = store

	driver := newDriver(fakeReadings{snapshot: snapshot}, pen, light, quiet)
	driver.store = store
	driver.tick = 5 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())

	t.Cleanup(cancel)
	t.Cleanup(func() {
		current.Store(nil)
		sent.Store(nil)
	})

	go pen.run(ctx)
	go driver.run(ctx)
}

// pixelsIn is every pixel a pushed frame draws in one colour, which is how a test reads a word back
// out of the picture the module sent. A datagram carries no per-pixel body this firmware would
// report, so the pushed frame is the only evidence there is (pixels.go).
func pixelsIn(frame []wled.RGB, colour wled.RGB) []int {
	lit := make([]int, 0, len(frame))

	for index, one := range frame {
		if one == colour {
			lit = append(lit, index)
		}
	}

	return lit
}

// TestAStoredColourIsPaintedForItsState is the whole of the first rule: the state table's colour is
// what the light is written in, and the frame the routes answer with says the same thing — a
// thumbnail drawn from a colour the light does not have would be the second answer this module
// exists to avoid.
func TestAStoredColourIsPaintedForItsState(t *testing.T) {
	at := time.Date(2026, time.November, 19, 12, 0, 0, 0, time.UTC)

	blue := wled.RGB{R: 20, G: 60, B: 220}
	table := &handTable{states: map[string]show{
		printer.StatePrinting: {colour: blue, origin: fromStore},
	}}

	strip := newFakeStrip()
	pen := newWriter(strip, 128, quiet)
	watchOne(t, pen, printingAt(41, at), strip, table)

	strip.waitForPaints(t, 1)

	if got := strip.shown()[0].colour; got != blue {
		t.Fatalf("the light was painted %+v, want the store's %+v", got, blue)
	}

	seen := current.Load()
	if seen == nil {
		t.Fatal("the driver decided a frame and published nothing")
	}

	if got := seen.Frame.Colour; got != (colourView{R: blue.R, G: blue.G, B: blue.B}) {
		t.Fatalf("the frame /status answers with is %+v, want the store's colour", got)
	}
}

// TestAStateNobodyOverrodeIsPaintedInTheRenderersColour is the other side of it, and the one that
// matters for the store's shape: a row that exists but carries no override of ours must not be taken
// as a colour somebody chose. The table deliberately answers a colour that is not the renderer's,
// with the origin saying it is the default, and the light must show the renderer's.
func TestAStateNobodyOverrodeIsPaintedInTheRenderersColour(t *testing.T) {
	at := time.Date(2026, time.November, 19, 12, 0, 0, 0, time.UTC)

	for _, one := range []struct {
		name  string
		table *handTable
	}{
		{
			name: "a row carrying the renderer's default",
			table: &handTable{states: map[string]show{
				printer.StatePrinting: {colour: wled.RGB{R: 1, G: 2, B: 3}, origin: fromDefault},
			}},
		},
		{name: "no row at all", table: &handTable{}},
	} {
		t.Run(one.name, func(t *testing.T) {
			strip := newFakeStrip()
			pen := newWriter(strip, 128, quiet)
			watchOne(t, pen, printingAt(41, at), strip, one.table)

			strip.waitForPaints(t, 1)

			if got := strip.shown()[0].colour; got != render.ColourPrinting {
				t.Fatalf("the light was painted %+v, want the renderer's printing colour %+v",
					got, render.ColourPrinting)
			}
		})
	}
}

// TestAWordBeatsTheLayoutComposer is the locked rule that overrides the composer: a state carrying a
// word shows that word on a panel, and the bar-and-digits picture — which this frame would otherwise
// be drawn as — does not appear.
func TestAWordBeatsTheLayoutComposer(t *testing.T) {
	at := time.Date(2026, time.November, 19, 12, 0, 0, 0, time.UTC)

	table := &handTable{states: map[string]show{
		printer.StatePrinting: {
			colour: render.ColourPrinting,
			origin: fromStore,
			word:   "HELLO",
			holds:  durationForever,
		},
	}}

	strip := newPanelStrip()
	pen := newWriter(strip, 128, quiet)
	pen.layout = defaultLayout()
	pen.setPanel(strip.info)

	pushed := pushInto(pen)

	watchOne(t, pen, printingAt(41, at), strip, table)

	waitFor(t, "the word to reach the panel", func() bool { return paintings(strip) > 0 })

	// One painting only: the word went up once and is being repeated as pixels, not re-established
	// once a tick. A composed picture's establishing body carries no name, so the name is also what
	// tells the word apart from the picture underneath it.
	if got := paintings(strip); got != 1 {
		t.Fatalf("the panel was given a named picture %d times, want the word once", got)
	}

	named := ""

	for _, call := range strip.shown() {
		if len(call.segments) > 0 && call.segments[0].Name != "" {
			named = call.segments[0].Name
		}
	}

	if named != "HELLO" {
		t.Fatalf("the panel's established picture is named %q, want the state's word HELLO", named)
	}

	// The composer really was in play: the frame the driver decided is a job in progress on a panel
	// whose layout draws, which is exactly what `drawsComposed` says a panel composes for.
	seen := current.Load()
	if seen == nil {
		t.Fatal("the driver decided a frame and published nothing")
	}

	decided := render.Frame{
		Colour:  wled.RGB{R: seen.Frame.Colour.R, G: seen.Frame.Colour.G, B: seen.Frame.Colour.B},
		Lit:     seen.Frame.Lit,
		Total:   seen.Frame.Total,
		Percent: seen.Frame.Percent,
		Name:    seen.Frame.Name,
		Reason:  render.Reason(seen.Frame.Reason),
	}

	if !drawsComposed(decided, livePanel, defaultLayout()) {
		t.Fatalf("the frame %+v is not one the composer would draw, so this test proves nothing", decided)
	}

	want, err := text.Raster("HELLO", finishedWordScale, text.Geometry{Width: 256, Height: 64})
	if err != nil {
		t.Fatalf("rasterise HELLO: %v", err)
	}

	if got := pixelsIn(pushed.last(), render.ColourPrinting); !slices.Equal(got, want) {
		t.Fatalf("the panel was given %d pixels of the word, want the %d the font draws", len(got), len(want))
	}

	// And the composed picture was never laid down underneath it.
	if got := steadyPaints(strip); len(got) != 0 {
		t.Fatalf("the composer painted %d frames while the word held, want none: %+v", len(got), got)
	}
}

// TestAStripShowsTheStatesColourInsteadOfItsWord is the fallback the requirements lock: a strip has
// no second dimension to draw a word in, so a state carrying one shows its colour there.
func TestAStripShowsTheStatesColourInsteadOfItsWord(t *testing.T) {
	at := time.Date(2026, time.November, 19, 12, 0, 0, 0, time.UTC)

	amber := wled.RGB{R: 200, G: 90, B: 0}
	table := &handTable{states: map[string]show{
		printer.StatePrinting: {
			colour: amber,
			origin: fromStore,
			word:   "HELLO",
			holds:  durationForever,
		},
	}}

	said := &logs{}

	strip := newFakeStrip()
	pen := newWriter(strip, 128, said.logf)

	pushed := pushInto(pen)

	watchOne(t, pen, printingAt(41, at), strip, table)

	strip.waitForPaints(t, 1)

	if got := strip.shown()[0].colour; got != amber {
		t.Fatalf("the strip was painted %+v, want the state's colour %+v", got, amber)
	}

	if all := pushed.all(); len(all) != 0 {
		t.Fatalf("a strip was given %d pixel frames, and a word cannot be drawn on one", len(all))
	}

	// And it fell back by the rule and not because the word could not be drawn: a strip refusing a
	// word because it "did not fit" would be the same picture for the wrong reason, and it would put
	// a refusal in the log that a person would read as a mistake in his own word.
	said.mu.Lock()
	defer said.mu.Unlock()

	for _, line := range said.lines {
		if strings.Contains(line, "was not drawn") {
			t.Fatalf("the strip refused the word rather than falling back to its colour: %q", line)
		}
	}
}

// TestAWordsHoldIsMeasuredFromTheStartOfItsState is the arithmetic on its own: the four holds the
// Colours screen offers, and `forever`, judged either side of the instant they run out.
func TestAWordsHoldIsMeasuredFromTheStartOfItsState(t *testing.T) {
	since := time.Date(2026, time.November, 19, 12, 0, 0, 0, time.UTC)

	for _, one := range []struct {
		holds   string
		just    time.Duration
		at      time.Duration
		forever bool
		inForce bool
	}{
		{holds: hold30s, just: 29 * time.Second, at: 29 * time.Second, inForce: true},
		{holds: hold30s, just: 30 * time.Second, at: 30 * time.Second, inForce: false},
		{holds: hold60s, just: 59 * time.Second, at: 59 * time.Second, inForce: true},
		{holds: hold60s, just: 60 * time.Second, at: 60 * time.Second, inForce: false},
		{holds: hold5m, just: 5*time.Minute - time.Second, at: 5*time.Minute - time.Second, inForce: true},
		{holds: hold5m, just: 5 * time.Minute, at: 5 * time.Minute, inForce: false},
		{holds: durationForever, just: 400 * 24 * time.Hour, at: 400 * 24 * time.Hour, forever: true, inForce: true},
	} {
		t.Run(one.holds, func(t *testing.T) {
			until := wordUntil(one.holds, since)

			if got := until.IsZero(); got != one.forever {
				t.Fatalf("a hold of %s ends at %s, and forever=%v was wanted", one.holds, until, one.forever)
			}

			if got := wordInForce(one.holds, since, since.Add(one.at)); got != one.inForce {
				t.Fatalf("a hold of %s %s in force %s after its state began, want %v",
					one.holds, map[bool]string{true: "is", false: "is not"}[got], one.at, one.inForce)
			}
		})
	}
}

// runWordedPanel starts a panel writer holding one state's word, and answers the controller, the
// recorder of pushed pixels, the fake clock and the stop.
func runWordedPanel(t *testing.T, holds string) (*panelStrip, *pushedFrames, *fakeClock, time.Time) {
	t.Helper()

	at := time.Date(2026, time.November, 19, 12, 0, 0, 0, time.UTC)

	clock := newFakeClock(at)

	strip := newPanelStrip()
	pen := newWriter(strip, 128, quiet)
	pen.setPanel(strip.info)
	pen.store = &handTable{states: map[string]show{
		printer.StateFinished: {colour: render.ColourFinished, origin: fromStore, word: "DONE", holds: holds},
	}}

	clock.watch(pen)
	pushed := pushInto(pen)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	go pen.run(ctx)

	// A finished job: no job on the bed, so the composer has nothing to draw and the frame the word
	// gives the panel back to is the plain finished colour.
	steady := render.Frame{Colour: render.ColourFinished, Total: 16384, Reason: render.ReasonNoLiveJob}
	pen.submitScene(scene{frame: steady, state: printer.StateFinished, since: at})

	waitFor(t, "the word to reach the panel", func() bool { return paintings(strip) == 1 })

	return strip, pushed, clock, at
}

// TestAWordsHoldExpiresAndTheColourComesBack is the expiry, either side of the instant, with a clock
// the test moves: the word holds the panel for the thirty seconds its state asked for, and the
// light goes back to the state's colour at the stroke — not at the next thing the printer says.
func TestAWordsHoldExpiresAndTheColourComesBack(t *testing.T) {
	strip, _, clock, _ := runWordedPanel(t, hold30s)

	mark := clock.mark()

	// One second short of the hold.
	clock.Advance(29 * time.Second)
	clock.waitArmed(t, mark)

	time.Sleep(20 * time.Millisecond)

	if got := steadyPaints(strip); len(got) != 0 {
		t.Fatalf("the panel was given the steady frame %d times before the hold ran out: %+v", len(got), got)
	}

	// The hold runs out. Nothing is submitted: the writer's own wait is what brings the frame back.
	clock.Advance(time.Second)

	waitFor(t, "the state's colour to come back", func() bool { return len(steadyPaints(strip)) == 1 })

	back := steadyPaints(strip)[0]
	if len(back.segments) == 0 {
		t.Fatalf("the frame that took the panel back is %+v, want segments", back)
	}

	if back.segments[0].Colour != render.ColourFinished {
		t.Fatalf("the panel went back to %+v, want the state's colour %+v",
			back.segments[0].Colour, render.ColourFinished)
	}
}

// TestAForeverWordDoesNotExpire is the other end: `forever` is a decision and not a long hold, and no
// amount of clock moves the light off it.
func TestAForeverWordDoesNotExpire(t *testing.T) {
	strip, _, clock, _ := runWordedPanel(t, durationForever)

	for range 6 {
		mark := clock.mark()
		clock.Advance(10 * time.Minute)
		clock.waitArmed(t, mark)

		time.Sleep(10 * time.Millisecond)
	}

	if got := steadyPaints(strip); len(got) != 0 {
		t.Fatalf("a word of forever gave the panel back after an hour: %+v", got)
	}

	if got := paintings(strip); got != 1 {
		t.Fatalf("the word was laid down %d times, want once: the repeat is pixels, not a new painting", got)
	}
}

// pausedLight is one whole-light writer whose store holds one pause: the controller, the clock that
// judges it, the store itself and the writer, with the clock's mark taken before the writer could
// arm anything — so a test that moves the clock to the instant the pause ends is moving one the
// writer is really waiting on.
type pausedLight struct {
	strip *fakeStrip
	clock *fakeClock
	table *handTable
	pen   *writer
	mark  uint64
}

// pausingWriter builds one, already running, with the picture it was showing when somebody pressed
// the button.
func pausingWriter(t *testing.T, at, until time.Time) *pausedLight {
	t.Helper()

	clock := newFakeClock(at)

	strip := newFakeStrip()

	pen := newWriter(strip, 128, quiet)
	clock.watch(pen)

	table := &handTable{pauses: []storedPause{{
		Scope:   pauseGlobal,
		Chosen:  pause10m,
		SetAt:   at,
		Expires: &until,
	}}}

	pen.store = table

	// The picture the light was showing when somebody pressed the button, and the mark, both taken
	// before the writer gets to arm its first wait.
	pen.submit(frame(render.ColourPrinting, render.ReasonProgress))

	mark := clock.mark()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	go pen.run(ctx)

	return &pausedLight{strip: strip, clock: clock, table: table, pen: pen, mark: mark}
}

// TestAPauseSwitchesTheLightOff is the rule and the price of it: one write and then zero. The light
// is switched off rather than frozen, no frame is painted while the pause holds, and a picture that
// arrives during it is dropped rather than queued for the light's return.
func TestAPauseSwitchesTheLightOff(t *testing.T) {
	at := time.Date(2026, time.November, 19, 12, 0, 0, 0, time.UTC)

	paused := pausingWriter(t, at, at.Add(10*time.Minute))

	waitFor(t, "the light to be switched off", func() bool { return len(paused.strip.powerWrites()) == 1 })

	if on := paused.strip.powerWrites()[0]; on {
		t.Fatal("the first power write was an on, and a pause switches the light off")
	}

	// A frame decided while the pause holds is not painted.
	if got := paused.strip.wrote(); got != 0 {
		t.Fatalf("the light was painted %d frames while paused, want none: %+v", got, paused.strip.shown())
	}

	// Neither does an event: a print that finished during a pause is not something to celebrate on
	// a dark light, and the slot is emptied rather than left holding a word for the light's return.
	paused.pen.fire(finishedWordEvent())

	// And nothing writes a brightness at it either: the light is off, and a brightness is a write.
	time.Sleep(80 * time.Millisecond)

	if got := paused.strip.powerWrites(); len(got) != 1 {
		t.Fatalf("the light was switched %d times while paused, want once: %v", len(got), got)
	}

	if got := paused.strip.wrote(); got != 0 {
		t.Fatalf("the light was painted %d frames while paused, want none", got)
	}

	if got := paused.strip.powerWrites(); len(got) != 1 {
		t.Fatalf("an event during a pause cost %v power writes, want none", got)
	}
}

// TestAPauseThatExpiresRepaintsAtThatMoment is the defect the rule exists to prevent: a pause that
// runs out must not leave the light dark until the printer happens to say something. Nothing is
// submitted after the pause is set — only the clock moves — and the light comes back.
func TestAPauseThatExpiresRepaintsAtThatMoment(t *testing.T) {
	at := time.Date(2026, time.November, 19, 12, 0, 0, 0, time.UTC)

	paused := pausingWriter(t, at, at.Add(10*time.Minute))

	waitFor(t, "the light to be switched off", func() bool { return len(paused.strip.powerWrites()) == 1 })

	// The wait the writer arms is the instant the pause ends and nothing else, so the clock is moved
	// only once that wait exists.
	paused.clock.waitArmed(t, paused.mark)
	paused.clock.Advance(10 * time.Minute)

	waitFor(t, "the light to come back when the pause ended", func() bool { return paused.strip.wrote() == 1 })

	if got := paused.strip.powerWrites(); !slices.Equal(got, []bool{false, true}) {
		t.Fatalf("the power writes were %v, want the light switched off and then on", got)
	}

	if got := paused.strip.shown()[0].colour; got != render.ColourPrinting {
		t.Fatalf("the light came back in %+v, want the picture it was showing before the pause", got)
	}
}

// TestAResumeRepaintsImmediately is the same rule from the other side: a resume taken out of the
// store puts the light back on and paints, rather than leaving it dark until the printer moves.
//
// The resume reaches the writer on the next thing the driver says — a tick, in the process — and
// what this pins is that a tick is all it takes: the picture is painted rather than suppressed as
// "already shown", and the light is not left dark for the minute the heartbeat would have taken.
func TestAResumeRepaintsImmediately(t *testing.T) {
	at := time.Date(2026, time.November, 19, 12, 0, 0, 0, time.UTC)

	paused := pausingWriter(t, at, at.Add(10*time.Minute))

	waitFor(t, "the light to be switched off", func() bool { return len(paused.strip.powerWrites()) == 1 })

	paused.table.resume()

	// The driver's next tick, which is the only thing a resume in the process waits for.
	paused.pen.submit(frame(render.ColourPrinting, render.ReasonProgress))

	waitFor(t, "the light to come back on a resume", func() bool { return len(paused.strip.powerWrites()) == 2 })

	if got := paused.strip.powerWrites(); !slices.Equal(got, []bool{false, true}) {
		t.Fatalf("the power writes were %v, want the light switched off and then on", got)
	}

	waitFor(t, "the picture to be painted again", func() bool { return paused.strip.wrote() == 1 })

	if got := paused.strip.shown()[0].colour; got != render.ColourPrinting {
		t.Fatalf("the light came back in %+v, want the picture it was showing before the pause", got)
	}
}

// TestAPauseOnABorrowedZoneHandsItBackAndPowersNothingOff is the exception that must not be got
// wrong: a zone this module borrows is given back exactly as it was found, and its controller is
// never powered off and never given a brightness — the rest of that strip is somebody else's.
//
// The pause arrives while the zone is already being driven, which is the case that matters: a light
// this module was drawing in and must now stop drawing in without taking anything with it.
func TestAPauseOnABorrowedZoneHandsItBackAndPowersNothingOff(t *testing.T) {
	at := time.Date(2026, time.November, 19, 12, 0, 0, 0, time.UTC)

	strip := newFakeStrip()
	strip.state = wled.State{Segments: []wled.Segment{
		{ID: 0, Start: 0, Stop: 12, Name: "left", Effect: 3, Brightness: 90},
		{ID: 1, Start: 12, Stop: 36, Name: "right", Effect: 3, Brightness: 90},
		{ID: 2, Start: 36, Stop: 48, Name: "top", Effect: 3, Brightness: 90},
	}}

	table := &handTable{}

	pen := newSegmentWriter(strip, 1, "http://10.0.0.253", quiet)
	pen.store = table
	pen.printer = "01J8DEVICE"

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	go pen.run(ctx)

	// The zone is being drawn in before anybody presses the button.
	pen.submit(frame(render.ColourPrinting, render.ReasonProgress))
	strip.waitForPaints(t, 1)

	if len(strip.givenBack()) != 0 {
		t.Fatal("the zone was given back before anything was paused")
	}

	until := at.Add(10 * time.Minute)
	table.pausing(storedPause{Scope: pauseGlobal, Chosen: pause10m, SetAt: at, Expires: &until})

	// The driver's next tick.
	pen.submit(frame(render.ColourPrinting, render.ReasonProgress))

	waitFor(t, "the borrowed zone to be given back", func() bool { return len(strip.givenBack()) == 1 })

	given := strip.givenBack()[0]
	if given.ID != 1 || given.Name != "right" || given.Start != 12 || given.Stop != 36 {
		t.Fatalf("the zone was given back as %+v, want segment 1 exactly as it was found", given)
	}

	if got := strip.powerWrites(); len(got) != 0 {
		t.Fatalf("the controller was switched %v, and a borrowed zone is handed back rather than powered off", got)
	}

	if got := strip.settleWrites(); got != 0 {
		t.Fatalf("the controller was given %d brightness or power writes, and a borrowed zone gets neither", got)
	}

	// One painting before the pause and none after it: the zone is drawn in, then left as found.
	if got := strip.wrote(); got != 1 {
		t.Fatalf("the borrowed zone was painted %d frames, want the one before the pause", got)
	}
}

// TestAWindowInTheStoreBeatsTheSettingsList is the brightness moving house: the list the Brightness
// screen edits decides when it holds anything, and the `brightness_schedule` setting is the source it
// fell out of.
func TestAWindowInTheStoreBeatsTheSettingsList(t *testing.T) {
	evening := denver(t, 21, 0)

	settings := scheduleOf(t, "America/Denver", `{"from": "20:00", "to": "07:00", "brightness": 200}`)

	forty := 40

	tables := storeHolding(t, storedConfig{
		Windows: []brightnessWindow{{From: "20:00", To: "07:00", Brightness: &forty}},
	})

	pen := newWriter(newFakeStrip(), 128, quiet)
	pen.schedule = settings
	pen.store = lightsOf(tables, "America/Denver")

	if level, why, covered := settings.level(evening); level != 200 || !covered {
		t.Fatalf("the setting answers %d at %s (%s), so this test proves nothing", level, evening, why)
	}

	level, why, covered := pen.levelAt(evening)
	if level != 40 || !covered {
		t.Fatalf("the brightness at %s is %d (%s), want the store's 40", evening, level, why)
	}
}

// TestAnEmptyStoreListFallsBackToTheSetting is the migration source, kept: an install whose windows
// were pasted into the settings before the Brightness screen existed goes on running exactly as it
// did.
func TestAnEmptyStoreListFallsBackToTheSetting(t *testing.T) {
	evening := denver(t, 21, 0)

	settings := scheduleOf(t, "America/Denver", `{"from": "20:00", "to": "07:00", "brightness": 200}`)

	tables := storeHolding(t, storedConfig{})

	pen := newWriter(newFakeStrip(), 128, quiet)
	pen.schedule = settings
	pen.store = lightsOf(tables, "America/Denver")

	if level, why, covered := pen.levelAt(evening); level != 200 || !covered {
		t.Fatalf("the brightness at %s is %d (%s), want the setting's 200: an empty store list is not a schedule", evening, level, why)
	}
}

// TestAWindowListWithNoZoneIsRefusedRatherThanJudgedInUTC is the trap schedule.go records: a window
// judged in the container's clock fires seven hours away from the operator's evening and nothing
// says so. With no zone named, the list is not judged at all, the refusal says which setting is
// missing, and the light stays on the fixed brightness.
func TestAWindowListWithNoZoneIsRefusedRatherThanJudgedInUTC(t *testing.T) {
	evening := denver(t, 21, 0)

	forty := 40

	tables := storeHolding(t, storedConfig{
		Windows: []brightnessWindow{{From: "20:00", To: "07:00", Brightness: &forty}},
	})

	said := &logs{}

	lights := lightsOf(tables, "")

	lights.logging = said.logf

	if _, judged := lights.level(evening); judged {
		t.Fatal("a window list with no zone was judged, and the only clock it could have been judged in is UTC")
	}

	pen := newWriter(newFakeStrip(), 128, quiet)
	pen.store = lights

	if level, _, covered := pen.levelAt(evening); level != 128 || covered {
		t.Fatalf("the brightness with no zone to judge in is %d (covered=%v), want the fixed 128", level, covered)
	}

	if got := said.count(); got == 0 {
		t.Fatal("the refusal was not said, so nothing tells the operator which setting to fix")
	}
}

// TestAnUnchangedBrightnessCostsNoWrite is the write budget on the path that now reads the store
// every second: a light whose window has not moved is not written again, however often the painter
// looks.
func TestAnUnchangedBrightnessCostsNoWrite(t *testing.T) {
	forty := 40

	tables := storeHolding(t, storedConfig{
		Windows: []brightnessWindow{{From: "00:00", To: "23:59", Brightness: &forty}},
	})

	strip := newFakeStrip()
	pen := newWriter(strip, 128, quiet)
	pen.store = lightsOf(tables, "America/Denver")

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	go pen.run(ctx)

	waitFor(t, "the brightness to be set once", func() bool { return strip.settleWrites() >= 2 })

	settled := strip.settleWrites()

	// A printing light whose picture does not move: ten turns of the driver, one picture.
	for range 10 {
		pen.submit(frame(render.ColourPrinting, render.ReasonProgress))
		time.Sleep(20 * time.Millisecond)
	}

	if got := strip.settleWrites(); got != settled {
		t.Fatalf("the brightness was written %d times for one unchanged window, want %d", got-settled, 0)
	}

	if got := strip.wrote(); got != 1 {
		t.Fatalf("the light was painted %d frames for one unchanged picture, want 1", got)
	}
}

// TestTheStoreIsWhatThePainterReadsAndAWriteIsNoticed is the reader itself: a document on disk, the
// three questions asked of it, and the write a route makes being visible to the painter without a
// restart.
func TestTheStoreIsWhatThePainterReadsAndAWriteIsNoticed(t *testing.T) {
	word := "WARMING"
	holds := hold30s

	tables := storeHolding(t, storedConfig{States: []storedState{{
		State:  printer.StatePrinting,
		Colour: &storedColour{R: 1, G: 2, B: 3},
		Word:   &word,
		Holds:  &holds,
	}}})

	lights := lightsOf(tables, "America/Denver")

	one, known := lights.show(printer.StatePrinting)
	if !known {
		t.Fatal("the state the document holds a row for is not in the table")
	}

	if one.colour != (wled.RGB{R: 1, G: 2, B: 3}) || !one.overridden() {
		t.Fatalf("the state is %+v, want the document's colour as an override", one)
	}

	if one.word != word || one.holds != holds {
		t.Fatalf("the state carries the word %q holding %q, want %q holding %q", one.word, one.holds, word, holds)
	}

	// A state nobody has edited: the row exists and every value in it is the renderer's own.
	idle, known := lights.show(printer.StateIdle)
	if !known {
		t.Fatal("IDLE is not in the state table")
	}

	if idle.overridden() {
		t.Fatalf("IDLE reads as overridden (%+v), and nobody has written a row for it", idle)
	}

	if idle.colour != render.ColourIdle {
		t.Fatalf("IDLE is drawn in %+v, want the renderer's %+v", idle.colour, render.ColourIdle)
	}

	// A write through the routes, which is what the Colours screen does.
	if _, err := tables.revise(func(config *storedConfig) {
		setState(config, storedState{State: printer.StatePrinting, Colour: &storedColour{R: 9, G: 8, B: 7}})
	}); err != nil {
		t.Fatalf("write the state: %v", err)
	}

	changed, _ := lights.show(printer.StatePrinting)
	if changed.colour != (wled.RGB{R: 9, G: 8, B: 7}) {
		t.Fatalf("the painter still reads %+v after the write, want 9,8,7", changed.colour)
	}

	if changed.word != "" {
		t.Fatalf("the write replaced the row whole, so the word should be gone; it is %q", changed.word)
	}
}

// TestAPauseIsReadFromTheDocumentAndExpiresOnItsOwnInstant is the pause read off a real document:
// in force before its instant, not after it, and no timer anywhere doing it.
func TestAPauseIsReadFromTheDocumentAndExpiresOnItsOwnInstant(t *testing.T) {
	at := time.Date(2026, time.November, 19, 12, 0, 0, 0, time.UTC)
	until := at.Add(10 * time.Minute)

	tables := storeHolding(t, storedConfig{Pauses: []storedPause{{
		Scope:   pauseGlobal,
		Chosen:  pause10m,
		SetAt:   at,
		Expires: &until,
	}}})

	lights := lightsOf(tables, "America/Denver")

	if _, paused := lights.paused(at); !paused {
		t.Fatal("a pause set for ten minutes is not in force at the instant it was set")
	}

	if _, paused := lights.paused(until.Add(-time.Second)); !paused {
		t.Fatal("a pause is not in force one second before it ends")
	}

	if _, paused := lights.paused(until); paused {
		t.Fatal("a pause is still in force at the instant it ends")
	}
}
