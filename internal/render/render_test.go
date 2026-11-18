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

// TestPrintingAtZeroLightsNothing: a job that has done nothing fills nothing. The
// strip still carries the printing green, because it is still printing.
func TestPrintingAtZeroLightsNothing(t *testing.T) {
	snapshot := snapshotAt(t, true, printingTree(printer.StatePrinting, percent(0)), 0)

	frame := Decide(snapshot, moment, Config{LEDs: strip})

	if frame.Lit != 0 {
		t.Errorf("lit = %d, want 0", frame.Lit)
	}

	if frame.Colour != ColourPrinting {
		t.Errorf("colour = %v, want the printing green", frame.Colour)
	}

	if frame.Reason != ReasonProgress {
		t.Errorf("reason = %q, want %q: the progress was measured, it was just zero", frame.Reason, ReasonProgress)
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
