package render

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/nothingmn/wled-printer-status/internal/printer"
	"github.com/nothingmn/wled-printer-status/internal/wled"
)

// moment is the instant every test folds its printer at. Nothing here reads the
// clock: the reducer and Decide both take the moment as an argument.
var moment = time.Date(2026, 11, 18, 14, 2, 0, 0, time.UTC)

// strip is the LED count most tests use. 48 divides by most of the percentages a
// person would try, so a wrong fill shows as a wrong number rather than rounding
// noise.
const strip = 48

// staleAge is one second past the point internal/printer stops calling a reading
// present tense.
const staleAge = printer.StaleAfter + time.Second

// snapshotAt folds one state tree into a printer record at moment, and takes the
// snapshot age later.
//
// The snapshots are built through printer.Apply rather than assembled by hand, so
// what Decide is handed is what the reducer actually produces: Job.Live, Running
// and the guarded LiveProgress are the package's own, not a test's idea of them.
func snapshotAt(t *testing.T, connected bool, tree map[string]string, age time.Duration) printer.Snapshot {
	t.Helper()

	record := printer.Record{ID: "printer-1", Name: "test printer", Connected: connected}

	if len(tree) > 0 {
		record.State = make(map[string]json.RawMessage, len(tree))

		for path, value := range tree {
			record.State[path] = json.RawMessage(value)
		}
	}

	state, err := printer.Apply(printer.State{}, record, nil, moment)
	if err != nil {
		t.Fatalf("folding the printer record: %v", err)
	}

	return state.Snapshot(moment.Add(age))
}

// printingTree is the tree of a printer reporting one state and, when percent is
// not nil, one progress figure.
func printingTree(state string, percent *float64) map[string]string {
	tree := map[string]string{"/state": `"` + state + `"`}

	if percent != nil {
		tree["/job/progress"] = string(mustMarshal(*percent))
	}

	return tree
}

// mustMarshal renders one number as the JSON the host would publish.
func mustMarshal(number float64) []byte {
	value, err := json.Marshal(number)
	if err != nil {
		panic(err)
	}

	return value
}

// percent is a pointer to a progress figure, for the tests that want one in the
// tree without an extra variable at the call site.
func percent(value float64) *float64 { return &value }

// TestEveryStateShowsItsColour is the operator's table, one row per condition, and
// the row's name is the state's own word so a failure says which state disagreed.
func TestEveryStateShowsItsColour(t *testing.T) {
	tests := []struct {
		name       string
		tree       map[string]string
		connected  bool
		age        time.Duration
		wantColour wled.RGB
		wantReason Reason
	}{
		{"PRINTING", printingTree(printer.StatePrinting, percent(50)), true, 0, ColourPrinting, ReasonProgress},
		{"IDLE", printingTree(printer.StateIdle, nil), true, 0, ColourIdle, ReasonNoLiveJob},
		{"BUSY", printingTree(printer.StateBusy, percent(4)), true, 0, ColourBusy, ReasonProgress},
		{"READY", printingTree(printer.StateReady, nil), true, 0, ColourReady, ReasonNoLiveJob},
		{"PAUSED", printingTree(printer.StatePaused, percent(50)), true, 0, ColourPaused, ReasonPausedFrozen},
		{"ATTENTION", printingTree(printer.StateAttention, percent(50)), true, 0, ColourAttention, ReasonProgress},
		{"FINISHED", printingTree(printer.StateFinished, percent(100)), true, 0, ColourFinished, ReasonNoLiveJob},
		{"STOPPED", printingTree(printer.StateStopped, percent(31)), true, 0, ColourStopped, ReasonNoLiveJob},
		{"ERROR", printingTree(printer.StateError, nil), true, 0, ColourError, ReasonNoLiveJob},
		{"UNKNOWN", printingTree(printer.StateUnknown, nil), true, 0, ColourUnknown, ReasonNoLiveJob},
		{"disconnected", printingTree(printer.StatePrinting, percent(94)), false, 0, ColourDisconnected, ReasonDisconnected},
		{"stale", printingTree(printer.StatePrinting, percent(94)), true, staleAge, ColourStale, ReasonStale},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			snapshot := snapshotAt(t, test.connected, test.tree, test.age)

			frame := Decide(snapshot, moment.Add(test.age), Config{LEDs: strip})

			if frame.Colour != test.wantColour {
				t.Errorf("colour = %v, want %v", frame.Colour, test.wantColour)
			}

			if frame.Reason != test.wantReason {
				t.Errorf("reason = %q, want %q", frame.Reason, test.wantReason)
			}

			if frame.Total != strip {
				t.Errorf("total = %d, want %d", frame.Total, strip)
			}

			// R-11: green is the one colour that means "good", and only the two
			// states the operator called good may show it.
			if test.wantColour == ColourPrinting && test.name != "PRINTING" && test.name != "FINISHED" {
				t.Errorf("%s must not be shown in the printing green", test.name)
			}
		})
	}
}

// TestNoOtherStateShowsGreen pins R-11 from the other side: of the twelve
// conditions, only PRINTING and FINISHED are green at all.
func TestNoOtherStateShowsGreen(t *testing.T) {
	for _, colour := range []wled.RGB{
		ColourIdle, ColourAttention, ColourBusy, ColourPaused,
		ColourError, ColourStopped, ColourReady, ColourUnknown,
		ColourDisconnected, ColourStale,
	} {
		if colour == ColourPrinting {
			t.Errorf("colour %v is the printing green and is assigned to a condition that is not good", colour)
		}
	}

	if ColourFinished != ColourPrinting {
		t.Errorf("a finished print is green too, so it should be the same green; got %v", ColourFinished)
	}
}

// TestPrintingFillsHalfTheStrip is R-14: half done means half the range lit.
func TestPrintingFillsHalfTheStrip(t *testing.T) {
	snapshot := snapshotAt(t, true, printingTree(printer.StatePrinting, percent(50)), 0)

	frame := Decide(snapshot, moment, Config{LEDs: strip})

	if frame.Lit != strip/2 {
		t.Errorf("lit = %d, want %d (half of %d)", frame.Lit, strip/2, strip)
	}

	if frame.Total != strip {
		t.Errorf("total = %d, want %d: the frame describes one strip", frame.Total, strip)
	}
}

// TestAPrintThatHasNotStartedWaitsInYellow: a print the printer has taken and has
// not yet moved fills nothing, and it must not be drawn in the printing green.
//
// A whole range in green is what this build shows for a job that finished, so a
// print held at 0 % was reading as a print that had already succeeded. The interim
// is the waiting yellow: the colour the panel already carried before the job began,
// with the frame saying "starting" rather than "progress".
func TestAPrintThatHasNotStartedWaitsInYellow(t *testing.T) {
	snapshot := snapshotAt(t, true, printingTree(printer.StatePrinting, percent(0)), 0)

	if progress, ok := snapshot.LiveProgress(); !ok || progress != 0 {
		t.Fatalf("the fixture should carry a live 0 %%, got %v (known %v)", progress, ok)
	}

	frame := Decide(snapshot, moment, Config{LEDs: strip})

	if frame.Lit != 0 {
		t.Errorf("lit = %d, want 0: a print at 0 %% has done nothing", frame.Lit)
	}

	if frame.Reason != ReasonStarting {
		t.Errorf("reason = %q, want %q", frame.Reason, ReasonStarting)
	}

	if frame.Colour == ColourPrinting || frame.Colour == ColourFinished {
		t.Errorf("colour = %v, the green a finished job shows; a print that has not started must not wear it", frame.Colour)
	}

	// The colour is one the panel already carries, not a fifth hue invented for this
	// rule: the same yellow idle, busy, paused and attention are drawn in.
	for _, waiting := range []wled.RGB{ColourIdle, ColourBusy, ColourPaused, ColourAttention} {
		if frame.Colour != waiting {
			t.Errorf("colour = %v, want the waiting yellow %v", frame.Colour, waiting)
		}
	}

	if frame.Total != strip {
		t.Errorf("total = %d, want %d", frame.Total, strip)
	}
}

// TestAPrintAtOnePercentIsGreenAndFilling is the other side of the rule: once the
// print moves, nothing about the frame changes. The fill is the printing green and
// its length is the progress.
//
// The total is the live panel's 16,384 pixels rather than the 48-LED strip, because
// the rule is stated in pixels and 1 % of 48 floors to no LED at all — which is a
// range showing no progress, and is exactly what the starting rule answers. On the
// panel the same 1 % is 163 pixels, so the frame is a bar.
func TestAPrintAtOnePercentIsGreenAndFilling(t *testing.T) {
	const livePanel = 16384

	snapshot := snapshotAt(t, true, printingTree(printer.StatePrinting, percent(1)), 0)

	frame := Decide(snapshot, moment, Config{LEDs: livePanel})

	if frame.Reason != ReasonProgress {
		t.Errorf("reason = %q, want %q: the print has started moving", frame.Reason, ReasonProgress)
	}

	if frame.Colour != ColourPrinting {
		t.Errorf("colour = %v, want the printing green", frame.Colour)
	}

	// 1 % of 16,384 is 163.84, and the fill floors.
	if frame.Lit != 163 {
		t.Errorf("lit = %d, want 163 (1 %% of %d, rounded down)", frame.Lit, livePanel)
	}

	if frame.Lit == 0 {
		t.Error("a print at 1 % on the panel lit nothing")
	}

	// A printing job's fill is proportional from here on, not merely non-zero.
	if half := Decide(
		snapshotAt(t, true, printingTree(printer.StatePrinting, percent(50)), 0),
		moment, Config{LEDs: livePanel},
	); half.Lit != livePanel/2 || half.Colour != ColourPrinting {
		t.Errorf("half the panel is %v with %d lit, want the printing green and %d",
			half.Colour, half.Lit, livePanel/2)
	}
}

// TestOnlyALiveJobAtNoFillSaysStarting: "starting" belongs to one condition, and no
// other rule may borrow it.
//
// A printer that finished or went idle at the same 0 % has no job on the bed and
// keeps not-a-live-job. A live job whose progress the guarded reading refuses did
// not measure 0 % and keeps progress-unknown. A stale or disconnected printer keeps
// its own reason whatever the figure in its tree says. A pause at 0 % is stopped,
// not waiting to begin, and keeps its frozen reading.
func TestOnlyALiveJobAtNoFillSaysStarting(t *testing.T) {
	tests := []struct {
		name      string
		state     string
		progress  *float64
		connected bool
		age       time.Duration
		want      Reason
	}{
		{"IDLE at zero", printer.StateIdle, percent(0), true, 0, ReasonNoLiveJob},
		{"READY at zero", printer.StateReady, percent(0), true, 0, ReasonNoLiveJob},
		{"FINISHED at zero", printer.StateFinished, percent(0), true, 0, ReasonNoLiveJob},
		{"STOPPED at zero", printer.StateStopped, percent(0), true, 0, ReasonNoLiveJob},
		{"ERROR at zero", printer.StateError, percent(0), true, 0, ReasonNoLiveJob},
		{"a live job with no progress reading", printer.StatePrinting, nil, true, 0, ReasonProgressUnknown},
		{"a live job whose progress never arrived", printer.StateBusy, nil, true, 0, ReasonProgressUnknown},
		{"stale at zero", printer.StatePrinting, percent(0), true, staleAge, ReasonStale},
		{"disconnected at zero", printer.StatePrinting, percent(0), false, 0, ReasonDisconnected},
		{"paused at zero", printer.StatePaused, percent(0), true, 0, ReasonPausedFrozen},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			frame := Decide(
				snapshotAt(t, test.connected, printingTree(test.state, test.progress), test.age),
				moment.Add(test.age), Config{LEDs: strip},
			)

			if frame.Reason != test.want {
				t.Errorf("reason = %q, want %q", frame.Reason, test.want)
			}

			if frame.Reason == ReasonStarting {
				t.Errorf("%s was answered with %q, which is the live-job-at-no-fill rule", test.name, ReasonStarting)
			}
		})
	}
}

// TestBusyAtZeroIsAlsoAPrintThatHasNotStarted: BUSY is a live job — a printer heating
// for a print it has already taken — and at no fill it is a print that has not moved.
// Its colour does not change, because BUSY already carries the waiting yellow; only
// the reason becomes the starting one.
func TestBusyAtZeroIsAlsoAPrintThatHasNotStarted(t *testing.T) {
	frame := Decide(
		snapshotAt(t, true, printingTree(printer.StateBusy, percent(0)), 0),
		moment, Config{LEDs: strip},
	)

	if frame.Reason != ReasonStarting {
		t.Errorf("reason = %q, want %q", frame.Reason, ReasonStarting)
	}

	if frame.Colour != ColourBusy {
		t.Errorf("colour = %v, want the busy yellow %v, which is the waiting yellow", frame.Colour, ColourBusy)
	}

	if frame.Lit != 0 {
		t.Errorf("lit = %d, want 0", frame.Lit)
	}
}

// TestPausedKeepsItsFraction: a paused print keeps the fill it stopped at, and the
// frame says the fill is frozen rather than advancing.
func TestPausedKeepsItsFraction(t *testing.T) {
	snapshot := snapshotAt(t, true, printingTree(printer.StatePaused, percent(37)), 0)

	frame := Decide(snapshot, moment, Config{LEDs: strip})

	// 37 % of 48 is 17.76; the floor rule lands on 17.
	if frame.Lit != 17 {
		t.Errorf("lit = %d, want 17 (37 %% of %d, rounded down)", frame.Lit, strip)
	}

	if frame.Colour != ColourPaused {
		t.Errorf("colour = %v, want the paused yellow", frame.Colour)
	}

	if frame.Reason != ReasonPausedFrozen {
		t.Errorf("reason = %q, want %q", frame.Reason, ReasonPausedFrozen)
	}
}

// TestStalePrinterLightsNothing is the defect this package exists to prevent: a
// printer that stopped answering still says PRINTING, 94 % in the server's tree,
// and the frame must refuse the figure while the snapshot still carries it.
func TestStalePrinterLightsNothing(t *testing.T) {
	snapshot := snapshotAt(t, true, printingTree(printer.StatePrinting, percent(94)), staleAge)

	if !snapshot.Stale {
		t.Fatalf("the snapshot is not stale after %v with no change; the fixture is wrong", staleAge)
	}

	// The memory is still readable. That is the point: the figure is there, and the
	// frame does not use it.
	if !snapshot.Job.Progress.Known || snapshot.Job.Progress.Value != 94 {
		t.Fatalf("the snapshot should still carry the remembered 94 %%, got %+v", snapshot.Job.Progress)
	}

	if _, ok := snapshot.LiveProgress(); ok {
		t.Fatal("LiveProgress answered true for a stale snapshot")
	}

	frame := Decide(snapshot, moment.Add(staleAge), Config{LEDs: strip})

	if frame.Lit != 0 {
		t.Errorf("lit = %d, want 0: a stale printer shows no progress", frame.Lit)
	}

	if frame.Colour != ColourStale {
		t.Errorf("colour = %v, want the dim red", frame.Colour)
	}

	if frame.Reason != ReasonStale {
		t.Errorf("reason = %q, want %q", frame.Reason, ReasonStale)
	}
}

// TestStaleOutranksPrinting asserts the ordering directly: the same printer, one
// second apart, must produce the stale frame and not the printing one.
func TestStaleOutranksPrinting(t *testing.T) {
	fresh := snapshotAt(t, true, printingTree(printer.StatePrinting, percent(94)), 0)

	live := Decide(fresh, moment, Config{LEDs: strip})
	if live.Colour != ColourPrinting || live.Lit != 45 {
		t.Fatalf("the fresh frame should be green and 45/48 lit, got %v, %d", live.Colour, live.Lit)
	}

	// The same snapshot, judged past the staleness horizon with no further change.
	later := Decide(fresh, moment.Add(staleAge), Config{LEDs: strip})

	if later.Reason != ReasonStale {
		t.Fatalf("judged %v later, reason = %q, want %q: staleness must outrank the state", staleAge, later.Reason, ReasonStale)
	}

	if later.Colour != ColourStale || later.Lit != 0 {
		t.Errorf("judged %v later, frame = %v %d lit, want the dim red with nothing lit", staleAge, later.Colour, later.Lit)
	}

	if later == live {
		t.Error("the stale frame is the live frame; the rules did not run in order")
	}
}

// TestDisconnectedPrinterLightsNothing: the host has no session, the tree still
// remembers 94 %, and the strip shows dim red with no fill.
func TestDisconnectedPrinterLightsNothing(t *testing.T) {
	snapshot := snapshotAt(t, false, printingTree(printer.StatePrinting, percent(94)), 0)

	if snapshot.Connected {
		t.Fatal("the fixture is connected; it should not be")
	}

	if !snapshot.Job.Progress.Known || snapshot.Job.Progress.Value != 94 {
		t.Fatalf("the snapshot should still carry the remembered 94 %%, got %+v", snapshot.Job.Progress)
	}

	frame := Decide(snapshot, moment, Config{LEDs: strip})

	if frame.Lit != 0 {
		t.Errorf("lit = %d, want 0", frame.Lit)
	}

	if frame.Colour != ColourDisconnected {
		t.Errorf("colour = %v, want the dim red", frame.Colour)
	}

	if frame.Reason != ReasonDisconnected {
		t.Errorf("reason = %q, want %q", frame.Reason, ReasonDisconnected)
	}
}

// TestRefusedProgressLightsNothing: a running job whose progress the guarded
// reading refuses fills nothing, and says so — it does not fall back to 0 % (a
// figure nobody measured), 100 % (a guess), or the last number it remembered.
func TestRefusedProgressLightsNothing(t *testing.T) {
	// PRINTING with no /job/progress at all: the printer never reported one.
	refused := snapshotAt(t, true, printingTree(printer.StatePrinting, nil), 0)

	if _, ok := refused.LiveProgress(); ok {
		t.Fatal("LiveProgress answered true with no progress in the tree")
	}

	frame := Decide(refused, moment, Config{LEDs: strip})

	if frame.Lit != 0 {
		t.Errorf("lit = %d, want 0", frame.Lit)
	}

	if frame.Lit == strip {
		t.Errorf("lit = %d, want 0: refused progress must not be read as 100 %%", frame.Lit)
	}

	if frame.Colour != ColourPrinting {
		t.Errorf("colour = %v, want the printing green: the state is still known", frame.Colour)
	}

	if frame.Reason != ReasonProgressUnknown {
		t.Errorf("reason = %q, want %q", frame.Reason, ReasonProgressUnknown)
	}

	// And it is distinguishable from a measured zero, which is the whole point of
	// carrying a reason.
	measured := Decide(
		snapshotAt(t, true, printingTree(printer.StatePrinting, percent(0)), 0),
		moment, Config{LEDs: strip},
	)

	if measured.Lit != frame.Lit {
		t.Fatalf("a measured 0 %% should light the same 0 LEDs; got %d and %d", measured.Lit, frame.Lit)
	}

	if measured.Reason == frame.Reason {
		t.Errorf("a measured 0 %% and a refused reading both say %q; a caller cannot tell them apart", frame.Reason)
	}
}

// TestErrorIsRed: an error never shows in any colour but red.
func TestErrorIsRed(t *testing.T) {
	snapshot := snapshotAt(t, true, printingTree(printer.StateError, nil), 0)

	frame := Decide(snapshot, moment, Config{LEDs: strip})

	if frame.Colour != ColourError {
		t.Fatalf("colour = %v, want the error red %v", frame.Colour, ColourError)
	}

	if frame.Colour.R <= frame.Colour.G || frame.Colour.R <= frame.Colour.B {
		t.Errorf("colour = %v is not dominated by red", frame.Colour)
	}

	if frame.Lit != 0 {
		t.Errorf("lit = %d, want 0: an error has no job on the bed to fill", frame.Lit)
	}

	if frame.Colour == ColourPrinting {
		t.Error("an error must never be green")
	}
}

// TestFillRoundsDown pins the provisional rounding rule at the boundaries it
// decides: half an LED is not an LED, and a bar never claims more than was
// reported.
func TestFillRoundsDown(t *testing.T) {
	tests := []struct {
		name     string
		leds     int
		progress float64
		want     int
	}{
		// 50 % of three LEDs is 1.5. Floor is 1; nearest would be 2. This is the
		// case the rule is pinned by.
		{"half an odd LED rounds down", 3, 50, 1},
		// 50 % of a single LED is 0.5, and lights nothing.
		{"half an only LED lights nothing", 1, 50, 0},
		{"half a 48 strip", strip, 50, 24},
		{"just short of the end", strip, 99.9, 47},
		{"the end", strip, 100, 48},
		{"past the end clamps", strip, 250, 48},
		{"below zero clamps", strip, -5, 0},
		{"a tenth of a strip", 48, 10, 4},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			snapshot := snapshotAt(t, true, printingTree(printer.StatePrinting, percent(test.progress)), 0)

			frame := Decide(snapshot, moment, Config{LEDs: test.leds})

			if frame.Lit != test.want {
				t.Errorf("%v %% of %d LEDs lit %d, want %d", test.progress, test.leds, frame.Lit, test.want)
			}

			if frame.Lit > frame.Total {
				t.Errorf("lit %d is more than the %d LEDs that exist", frame.Lit, frame.Total)
			}
		})
	}
}

// TestUnknownWordIsNotAVerdict: a state word this build has no reading of, and a
// printer that never reported one, are both dim blue with nothing lit — never
// green, never red.
func TestUnknownWordIsNotAVerdict(t *testing.T) {
	for _, test := range []struct {
		name string
		tree map[string]string
	}{
		{"a word this build does not know", printingTree("OFFLINE", percent(94))},
		// A printer that has reported something but never a state. A printer that
		// has reported nothing at all is stale by internal/printer's own rule, and
		// that row is in the table above.
		{"no state reported at all", map[string]string{"/temperature/bed": "21.5"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			frame := Decide(snapshotAt(t, true, test.tree, 0), moment, Config{LEDs: strip})

			if frame.Colour != ColourUnknown {
				t.Errorf("colour = %v, want the dim blue", frame.Colour)
			}

			if frame.Lit != 0 {
				t.Errorf("lit = %d, want 0", frame.Lit)
			}

			if frame.Reason != ReasonUnknownState {
				t.Errorf("reason = %q, want %q", frame.Reason, ReasonUnknownState)
			}
		})
	}
}

// TestZeroLEDStripLightsNothing: a strip with no LEDs is not a crash and not a
// division by zero, whatever the printer says.
func TestZeroLEDStripLightsNothing(t *testing.T) {
	snapshot := snapshotAt(t, true, printingTree(printer.StatePrinting, percent(50)), 0)

	for _, leds := range []int{0, -12} {
		frame := Decide(snapshot, moment, Config{LEDs: leds})

		if frame.Total != 0 || frame.Lit != 0 {
			t.Errorf("%d LEDs gave total %d and lit %d, want 0 and 0", leds, frame.Total, frame.Lit)
		}
	}
}

// TestUnfilledRemainderIsOff pins the provisional answer to Q-15 so that changing
// it is a deliberate act and not a surprise: the LEDs past the fill are dark.
func TestUnfilledRemainderIsOff(t *testing.T) {
	frame := Decide(
		snapshotAt(t, true, printingTree(printer.StatePrinting, percent(50)), 0),
		moment, Config{LEDs: strip},
	)

	if frame.Unfilled() != (wled.RGB{}) {
		t.Errorf("unfilled = %v, want off; if this was changed on purpose, change the test too", frame.Unfilled())
	}
}

// foldAt folds a tree into a snapshot whose newest fact changed at changed and which is
// read at read, so a test can walk a printer through a silence and out of it without
// touching a clock.
func foldAt(t *testing.T, tree map[string]string, changed, read time.Time) printer.Snapshot {
	t.Helper()

	record := printer.Record{ID: "printer-1", Name: "test printer", Connected: true, State: map[string]json.RawMessage{}}

	for path, value := range tree {
		record.State[path] = json.RawMessage(value)
	}

	state, err := printer.Apply(printer.State{}, record, nil, changed)
	if err != nil {
		t.Fatalf("folding the printer record: %v", err)
	}

	return state.Snapshot(read)
}

// TestATerminalWordHoldsItsColourThroughASilence is the defect the operator watched on
// the night of 2026-11-19: a finished print whose panel flapped between the finished
// green and the stale dark red every minute or two, because the bed cooled, the cooling
// restarted the change clock, and each dip past StaleAfter was painted as a light that
// had lost contact. A finish is terminal — nothing the printer could report next would
// change it — so the last known state stands and the picture does not react. Both
// terminal words are held, each in its own colour, on both sides of the horizon and well
// past it.
func TestATerminalWordHoldsItsColourThroughASilence(t *testing.T) {
	cases := []struct {
		state  string
		colour wled.RGB
	}{
		{printer.StateFinished, ColourFinished},
		{printer.StateStopped, ColourStopped},
	}

	ages := []time.Duration{
		0,
		printer.StaleAfter - time.Second, // the last moment it is still live
		printer.StaleAfter,               // the moment it tips over
		printer.StaleAfter + time.Second, // one second past
		91 * time.Second,                 // the flap the operator saw, twice over
		10 * time.Minute,                 // a long silence
		6 * time.Hour,                    // an overnight one
	}

	for _, current := range cases {
		t.Run(current.state, func(t *testing.T) {
			fresh := Decide(snapshotAt(t, true, printingTree(current.state, percent(100)), 0), moment, Config{LEDs: strip})

			if fresh.Colour != current.colour || fresh.Lit != 0 || fresh.Reason != ReasonNoLiveJob {
				t.Fatalf("the live frame is %+v, want the %s colour, nothing lit and %q",
					fresh, current.state, ReasonNoLiveJob)
			}

			for _, age := range ages {
				snapshot := snapshotAt(t, true, printingTree(current.state, percent(100)), age)
				frame := Decide(snapshot, moment.Add(age), Config{LEDs: strip})

				if frame != fresh {
					t.Errorf("judged %v after the last change the frame is %+v, want the frame it showed when the word was fresh, %+v",
						age, frame, fresh)
				}

				// The module still tells the truth about the age of what it knows, even
				// where the picture stops reacting to it.
				if age > printer.StaleAfter && !snapshot.Stale {
					t.Errorf("a reading %v old is not stale; the fixture is wrong", age)
				}

				if age > printer.StaleAfter && snapshot.Age != age {
					t.Errorf("the snapshot says the reading is %v old, want %v", snapshot.Age, age)
				}
			}
		})
	}
}

// TestALiveJobIsStillDowngradedBySilence is the other half of the rule, and what makes
// the change above a narrowing rather than a removal: the four states with a job on the
// bed go quiet in the same way a finished printer does, and the strip still goes dim red
// for them, with nothing lit, at the horizon and long past it.
//
// PAUSED is deliberately on this side. A pause is a job on the bed that can resume or
// end, so a reading that stops moving no longer says the print is still stopped where it
// stood; the frozen bar must not be presented as current, which is exactly what R-17
// forbids. Only a word that records the job as over is beyond a later reading's reach.
func TestALiveJobIsStillDowngradedBySilence(t *testing.T) {
	for _, state := range []string{
		printer.StatePrinting, printer.StateBusy, printer.StatePaused, printer.StateAttention,
	} {
		for _, age := range []time.Duration{printer.StaleAfter + time.Second, 10 * time.Minute} {
			t.Run(state+"/"+age.String(), func(t *testing.T) {
				snapshot := snapshotAt(t, true, printingTree(state, percent(50)), age)

				if snapshot.Terminal() {
					t.Fatalf("a printer that says %s reads as terminal; the fixture is wrong", state)
				}

				frame := Decide(snapshot, moment.Add(age), Config{LEDs: strip})

				if frame.Colour != ColourStale {
					t.Errorf("colour = %v, want the stale dim red %v for a %s that has gone quiet",
						frame.Colour, ColourStale, state)
				}

				if frame.Lit != 0 {
					t.Errorf("lit = %d, want 0: a silent printer shows no progress", frame.Lit)
				}

				if frame.Reason != ReasonStale {
					t.Errorf("reason = %q, want %q", frame.Reason, ReasonStale)
				}
			})
		}
	}
}

// TestAFinishedPrinterDoesNotFlashWhenItsReadingComesBack walks the flap itself, tick by
// tick, in the order the driver saw it: a finished frame, a dip past the horizon, a
// cooling bed moving a value and the reading coming back, another dip, another recovery.
// Not one of those ticks may ask the light for anything but the finished frame, and the
// reading stays stale throughout the dips — the light holds and the route keeps telling
// the truth about the age.
func TestAFinishedPrinterDoesNotFlashWhenItsReadingComesBack(t *testing.T) {
	tree := printingTree(printer.StateFinished, percent(100))

	// The cooling bed moves a value at each of these moments, which is what restarts the
	// change clock and brought the reading back every minute or two.
	changed := []time.Duration{0, 95 * time.Second, 200 * time.Second}
	judged := []time.Duration{
		0,
		31 * time.Second,                 // first dip
		95 * time.Second,                 // the bed moved, read at once
		95*time.Second + 40*time.Second,  // second dip
		200 * time.Second,                // the bed moved again
		200*time.Second + 90*time.Second, // third dip
	}

	want := ColourFinished
	staleSeen := 0

	for _, at := range judged {
		latest := changed[0]

		for _, moment := range changed {
			if moment <= at {
				latest = moment
			}
		}

		snapshot := foldAt(t, tree, moment.Add(latest), moment.Add(at))

		if snapshot.Stale {
			staleSeen++
		}

		frame := Decide(snapshot, moment.Add(at), Config{LEDs: strip})

		if frame.Colour != want || frame.Lit != 0 || frame.Reason != ReasonNoLiveJob {
			t.Fatalf("at %v after the start the frame is %+v, want the finished green held with nothing lit",
				at, frame)
		}
	}

	if staleSeen == 0 {
		t.Fatal("no tick in this fixture is stale; it does not reproduce the flap it is about")
	}

	// And the dips are what the route reports as stale, with a real age, while the frame
	// above was decided green.
	dipped := foldAt(t, tree, moment, moment.Add(90*time.Second))

	if !dipped.Stale || dipped.Age != 90*time.Second {
		t.Fatalf("the dip reads stale=%v age=%v, want stale at 90s", dipped.Stale, dipped.Age)
	}

	if _, ok := dipped.LiveProgress(); ok {
		t.Fatal("the dip offers a live progress; the reading is meant to be honest about its age")
	}
}

// TestThePanelFactsArePresentTenseOnly is the rule the percentage has had since it was written, one
// field over: a temperature from four minutes ago is not a temperature, and a countdown read off a
// printer that has gone quiet is a countdown to a moment that has already passed.
//
// A strip ignores the field entirely; what it protects is a panel.
func TestThePanelFactsArePresentTenseOnly(t *testing.T) {
	tree := printingTree(printer.StatePrinting, percent(41))
	tree[printer.PathJobRemaining] = "2520"
	tree[printer.PathJobPrinting] = "4320"
	tree[printer.PathNozzleTemp] = "221.4"
	tree[printer.PathNozzleTarget] = "220"
	tree[printer.PathBedTemp] = "60.2"
	tree[printer.PathBedTarget] = "60"
	tree[printer.PathMaterial] = `"PLA"`
	tree[printer.PathAxisZ] = "8.6"

	// A panel's count, which is only ever a number in these tests: nothing in this package draws.
	const panel = 16384

	fresh := Decide(snapshotAt(t, true, tree, 0), moment, Config{LEDs: panel})

	if fresh.Panel.RemainingSeconds != 2520 || fresh.Panel.PrintingSeconds != 4320 {
		t.Fatalf("the counts read %d and %d, want 2520 and 4320", fresh.Panel.RemainingSeconds, fresh.Panel.PrintingSeconds)
	}

	if !fresh.Panel.Nozzle.Reported || fresh.Panel.Nozzle.Now != 221.4 || fresh.Panel.Nozzle.Target != 220 {
		t.Fatalf("the nozzle reads %+v, want 221.4 of 220", fresh.Panel.Nozzle)
	}

	if !fresh.Panel.Bed.Reported || fresh.Panel.Bed.Now != 60.2 {
		t.Fatalf("the bed reads %+v, want 60.2", fresh.Panel.Bed)
	}

	if fresh.Panel.Material != "PLA" || fresh.Panel.Printer != "test printer" {
		t.Fatalf("the material and the name read %q and %q, want PLA and test printer",
			fresh.Panel.Material, fresh.Panel.Printer)
	}

	if !fresh.Panel.HeightReported || fresh.Panel.HeightMM != 8.6 {
		t.Fatalf("the height reads %+v, want 8.6 mm", fresh.Panel)
	}

	// A reading one second past the staleness horizon carries none of them: the printer is still the
	// printer, and everything it said is a memory.
	stale := Decide(snapshotAt(t, true, tree, staleAge), moment.Add(staleAge), Config{LEDs: panel})

	if stale.Reason != ReasonStale {
		t.Fatalf("the stale frame's reason is %q, want %q", stale.Reason, ReasonStale)
	}

	if stale.Panel.RemainingSeconds != -1 || stale.Panel.PrintingSeconds != -1 ||
		stale.Panel.Nozzle.Reported || stale.Panel.Bed.Reported ||
		stale.Panel.Material != "" || stale.Panel.HeightReported {
		t.Fatalf("a stale frame carries readings: %+v", stale.Panel)
	}

	// The machine's own name is identity rather than a reading, so it survives.
	if stale.Panel.Printer != "test printer" {
		t.Fatalf("the stale frame's printer name reads %q, want the machine's", stale.Panel.Printer)
	}

	// And a disconnected printer carries nothing at all.
	gone := Decide(snapshotAt(t, false, tree, 0), moment, Config{LEDs: panel})

	if gone.Panel.RemainingSeconds != -1 || gone.Panel.Material != "" || gone.Panel.Nozzle.Reported {
		t.Fatalf("a disconnected frame carries readings: %+v", gone.Panel)
	}
}
