package printer

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// appliedAt is the moment every test applies its batches at, so an age is
// arithmetic and not a race against the wall clock.
var appliedAt = time.Date(2026, time.November, 18, 14, 2, 0, 0, time.UTC)

// set builds one update, so a test reads as the path and the JSON a printer
// reported rather than as a struct literal.
func set(path, value string) Update {
	return Update{Path: path, Value: json.RawMessage(value)}
}

// TestALaterBatchLeavesAnEarlierValueIntact is the patch-stream rule. The host
// publishes a path only when it moves, so a window that does not mention the
// progress says nothing about it: the second batch must not clear what the first
// one set.
func TestALaterBatchLeavesAnEarlierValueIntact(t *testing.T) {
	first, err := Apply(State{}, Record{ID: "d1", Connected: true}, []Update{set(PathJobProgress, "94")}, appliedAt)
	if err != nil {
		t.Fatalf("the first batch failed: %v", err)
	}

	second, err := Apply(first, Record{ID: "d1", Connected: true},
		[]Update{set(PathJobState, `"PRINTING"`)}, appliedAt.Add(2*time.Second))
	if err != nil {
		t.Fatalf("the second batch failed: %v", err)
	}

	snapshot := second.Snapshot(appliedAt.Add(2 * time.Second))

	if !snapshot.Job.Progress.Known || snapshot.Job.Progress.Value != 94 {
		t.Fatalf("the progress after a batch that never mentioned it is %v (known %v), want 94",
			snapshot.Job.Progress.Value, snapshot.Job.Progress.Known)
	}

	if snapshot.Job.State.Value != StatePrinting {
		t.Fatalf("the job state is %q, want %q", snapshot.Job.State.Value, StatePrinting)
	}
}

// TestABatchWinsOverTheReadItArrivedWith holds the order Apply promises: the
// read's tree first, then the window over it, because the window carries values
// the read may not have seen.
func TestABatchWinsOverTheReadItArrivedWith(t *testing.T) {
	record := Record{ID: "d1", Connected: true, State: map[string]json.RawMessage{
		PathJobProgress: json.RawMessage("93"),
	}}

	state, err := Apply(State{}, record, []Update{set(PathJobProgress, "94")}, appliedAt)
	if err != nil {
		t.Fatalf("the batch failed: %v", err)
	}

	if got := state.Snapshot(appliedAt).Job.Progress.Value; got != 94 {
		t.Fatalf("the progress is %v, want the window's 94 and not the read's 93", got)
	}
}

// TestAPathThisBuildHasNeverSeenIsKeptWithoutAnError is the open-tree rule: a
// node a newer driver invented is still a fact about the printer, and refusing
// the batch over it would cost every path beside it.
func TestAPathThisBuildHasNeverSeenIsKeptWithoutAnError(t *testing.T) {
	const path = "/enclosure/temperature"

	state, err := Apply(State{}, Record{ID: "d1"}, []Update{set(path, "31.5")}, appliedAt)
	if err != nil {
		t.Fatalf("a path this build does not know failed the batch: %v", err)
	}

	snapshot := state.Snapshot(appliedAt)

	if got, ok := snapshot.Tree[path]; !ok || string(got) != "31.5" {
		t.Fatalf("the tree holds %s for %s (present %v), want the value as it came", got, path, ok)
	}

	if !snapshot.LastChange.Equal(appliedAt) {
		t.Fatalf("a path that arrived did not move the change clock: %v, want %v",
			snapshot.LastChange, appliedAt)
	}
}

// TestAValueThisBuildCannotReadIsReportedAndTheLastGoodOneStays is the refusal
// half. The value is reported as a *ValueError, the path keeps what it had, and
// the batch is not lost.
func TestAValueThisBuildCannotReadIsReportedAndTheLastGoodOneStays(t *testing.T) {
	good, err := Apply(State{},
		Record{ID: "d1"},
		[]Update{set(PathJobProgress, "12"), set(PathJobState, `"PRINTING"`)},
		appliedAt)
	if err != nil {
		t.Fatalf("the good batch failed: %v", err)
	}

	bad, err := Apply(good, Record{ID: "d1"}, []Update{set(PathJobProgress, `"twelve"`)}, appliedAt.Add(time.Second))

	var problem *ValueError
	if !errors.As(err, &problem) {
		t.Fatalf("a value this build cannot read reported %v, want a *ValueError", err)
	}

	if problem.Path != PathJobProgress {
		t.Fatalf("the refusal names %q, want %q", problem.Path, PathJobProgress)
	}

	snapshot := bad.Snapshot(appliedAt.Add(time.Second))

	if got := snapshot.Job.Progress.Value; got != 12 {
		t.Fatalf("the progress is %v, want the last value this build could read, 12", got)
	}

	if snapshot.Job.State.Value != StatePrinting {
		t.Fatalf("the path beside the bad one was lost: job state is %q", snapshot.Job.State.Value)
	}
}

// TestADisconnectedPrinterIsNotPresentedAsPrinting is the requirement this
// package exists for, asserted whole.
//
// The printer that went away at 14:02 leaves a tree that still says PRINTING with
// 94% and six minutes left, and the host keeps handing that tree back with no
// expiry. The snapshot may remember it. It may not present it as a job that is
// running.
func TestADisconnectedPrinterIsNotPresentedAsPrinting(t *testing.T) {
	record := Record{ID: "d1", Name: "MK4", Connected: true}
	updates := []Update{
		set(PathState, `"PRINTING"`),
		set(PathJobProgress, "94"),
		set(PathJobRemaining, "360"),
	}

	live, err := Apply(State{}, record, updates, appliedAt)
	if err != nil {
		t.Fatalf("the first batch failed: %v", err)
	}

	// The same tree, while the host still holds a session, is a live reading:
	// without this half the test would pass for a package that never says
	// anything is printing.
	printing := live.Snapshot(appliedAt.Add(time.Second))

	if progress, ok := printing.LiveProgress(); !ok || progress != 94 {
		t.Fatalf("a connected, fresh printer reports live progress %v (ok %v), want 94", progress, ok)
	}

	if remaining, ok := printing.LiveRemaining(); !ok || remaining != 6*time.Minute {
		t.Fatalf("a connected, fresh printer reports %v left (ok %v), want 6m", remaining, ok)
	}

	// The printer goes away. Nothing in the tree moves — a departed printer
	// reports nothing — so the next read carries the last tree and no session.
	gone, err := Apply(live, Record{ID: "d1", Name: "MK4", Connected: false}, nil, appliedAt.Add(2*time.Second))
	if err != nil {
		t.Fatalf("the disconnecting read failed: %v", err)
	}

	snapshot := gone.Snapshot(appliedAt.Add(3 * time.Second))

	if snapshot.Connected {
		t.Fatal("the snapshot reports a printer that went away as connected")
	}

	if snapshot.Job.Live {
		t.Fatal("the snapshot presents a departed printer's job as live")
	}

	if progress, ok := snapshot.LiveProgress(); ok {
		t.Fatalf("the snapshot offers %v as live progress for a departed printer", progress)
	}

	if remaining, ok := snapshot.LiveRemaining(); ok {
		t.Fatalf("the snapshot offers %v as live time remaining for a departed printer", remaining)
	}

	// The memory is still there, with the moment it was taken, which is what
	// lets a caller say "last reported 94% at 14:02" instead of nothing.
	if !snapshot.Job.Progress.Known || snapshot.Job.Progress.Value != 94 {
		t.Fatalf("the last reported progress is %v (known %v), want 94 kept as a fact",
			snapshot.Job.Progress.Value, snapshot.Job.Progress.Known)
	}

	if !snapshot.LastChange.Equal(appliedAt) {
		t.Fatalf("the last change is %v, want %v: the moment the printer last moved", snapshot.LastChange, appliedAt)
	}

	if snapshot.Age != 3*time.Second {
		t.Fatalf("the snapshot is %v old, want 3s", snapshot.Age)
	}

	// The state still reads PRINTING, because that is what the printer last said
	// and this package does not overwrite it with a word the printer never sent.
	// There is no OFFLINE in the state machine, and inventing one would be the
	// same lie in a different place.
	if snapshot.State.Value != StatePrinting {
		t.Fatalf("the state reads %q, want the printer's own last word %q", snapshot.State.Value, StatePrinting)
	}
}

// TestStalenessIsMeasuredFromTheLastChangeAndNotTheLastRead holds the clock to
// what it claims. A read that finds the same tree is not news about the job, so
// it must not refresh the age: a printer the host keeps answering for, whose
// tree has not moved in half a minute, is not printing.
func TestStalenessIsMeasuredFromTheLastChangeAndNotTheLastRead(t *testing.T) {
	held := map[string]json.RawMessage{PathJobProgress: json.RawMessage("40")}

	state, err := Apply(State{}, Record{ID: "d1", Connected: true, State: held}, nil, appliedAt)
	if err != nil {
		t.Fatalf("the first read failed: %v", err)
	}

	// Twenty seconds later the same tree comes back, and the window beside it
	// carries the same progress again: neither is a change.
	later := appliedAt.Add(20 * time.Second)

	unchanged, err := Apply(state, Record{ID: "d1", Connected: true, State: held},
		[]Update{set(PathJobProgress, "40")}, later)
	if err != nil {
		t.Fatalf("the second read failed: %v", err)
	}

	if !unchanged.Snapshot(later).LastChange.Equal(appliedAt) {
		t.Fatalf("a read that found the same tree moved the change clock to %v, want %v",
			unchanged.Snapshot(later).LastChange, appliedAt)
	}

	if sn := unchanged.Snapshot(appliedAt.Add(29 * time.Second)); sn.Stale {
		t.Fatalf("the snapshot is stale at %v old, want fresh until StaleAfter (%v)", sn.Age, StaleAfter)
	}

	stale := unchanged.Snapshot(appliedAt.Add(31 * time.Second))
	if !stale.Stale {
		t.Fatalf("the snapshot is %v old and not stale, want stale past %v", stale.Age, StaleAfter)
	}

	if _, ok := stale.LiveProgress(); ok {
		t.Fatal("a stale snapshot offers a live progress, want none")
	}

	// A value that does move refreshes it, because that is a change.
	moved := appliedAt.Add(20 * time.Second)

	fresh, err := Apply(unchanged, Record{ID: "d1", Connected: true}, []Update{set(PathJobProgress, "41")}, moved)
	if err != nil {
		t.Fatalf("the moving batch failed: %v", err)
	}

	if sn := fresh.Snapshot(appliedAt.Add(31 * time.Second)); sn.Stale {
		t.Fatalf("a value that moved at %v is stale 11s later, want fresh", moved)
	}
}

// TestADeviceWithNoStateTreeGivesAnEmptySnapshotThatIsValid is the admitted-but-
// silent printer: a record exists, the tree does not. Everything is unknown, and
// unknown is not zero and is not IDLE.
func TestADeviceWithNoStateTreeGivesAnEmptySnapshotThatIsValid(t *testing.T) {
	state, err := Apply(State{}, Record{ID: "d1", Name: "MK4"}, nil, appliedAt)
	if err != nil {
		t.Fatalf("a record with no tree failed: %v", err)
	}

	snapshot := state.Snapshot(appliedAt)

	if snapshot.ID != "d1" || snapshot.Name != "MK4" {
		t.Fatalf("the identity is %q/%q, want d1/MK4", snapshot.ID, snapshot.Name)
	}

	if snapshot.Connected {
		t.Fatal("a record that says nothing about a session reads as connected")
	}

	if snapshot.State.Known {
		t.Fatalf("the state reads %q, want unknown: the printer never reported one", snapshot.State.Value)
	}

	if snapshot.Job.Progress.Known || snapshot.Job.Remaining.Known || snapshot.Job.Printing.Known {
		t.Fatal("a job was invented for a printer that reported nothing")
	}

	if snapshot.Job.Running.Known || snapshot.Job.Live {
		t.Fatal("the job reads as running for a printer that reported nothing")
	}

	if snapshot.Nozzle.Now.Known || snapshot.Bed.Now.Known || snapshot.Material.Known {
		t.Fatal("a temperature or material was invented for a printer that reported nothing")
	}

	if len(snapshot.Tree) != 0 {
		t.Fatalf("the tree holds %d paths, want none", len(snapshot.Tree))
	}

	if !snapshot.LastChange.IsZero() {
		t.Fatalf("the last change is %v, want the zero time", snapshot.LastChange)
	}

	if snapshot.Age != 0 {
		t.Fatalf("the age is %v, want zero when nothing has ever been reported", snapshot.Age)
	}

	if !snapshot.Stale {
		t.Fatal("a printer nothing is known about reads as fresh")
	}

	if _, ok := snapshot.LiveProgress(); ok {
		t.Fatal("a printer nothing is known about offers a live progress")
	}
}

// TestAnAbsentStateIsUnknownRatherThanIdle is the never-invent rule at the one
// place it is easiest to get wrong. A printer that has reported a progress but no
// state is not therefore idle, and its progress is not therefore a live job.
func TestAnAbsentStateIsUnknownRatherThanIdle(t *testing.T) {
	state, err := Apply(State{}, Record{ID: "d1", Connected: true},
		[]Update{set(PathJobProgress, "94")}, appliedAt)
	if err != nil {
		t.Fatalf("the batch failed: %v", err)
	}

	snapshot := state.Snapshot(appliedAt)

	if snapshot.State.Known {
		t.Fatalf("the state reads %q, want unknown", snapshot.State.Value)
	}

	if snapshot.Job.Running.Known {
		t.Fatal("whether a job is running was decided from a state the printer never reported")
	}

	if progress, ok := snapshot.LiveProgress(); ok {
		t.Fatalf("the snapshot offers %v as live progress with no state to say a job is running", progress)
	}
}

// TestNullClearsAReadingWithoutAnError is the printer taking a value away. It is
// kept in the tree as it came, it is not an error, and it leaves the reading
// unknown rather than zero.
func TestNullClearsAReadingWithoutAnError(t *testing.T) {
	state, err := Apply(State{}, Record{ID: "d1"},
		[]Update{set(PathJobRemaining, "600"), set(PathMaterial, `"PLA"`)}, appliedAt)
	if err != nil {
		t.Fatalf("the first batch failed: %v", err)
	}

	cleared, err := Apply(state, Record{ID: "d1"},
		[]Update{set(PathJobRemaining, "null"), set(PathMaterial, "null")}, appliedAt.Add(time.Second))
	if err != nil {
		t.Fatalf("null for a known path was reported as a failure: %v", err)
	}

	snapshot := cleared.Snapshot(appliedAt.Add(time.Second))

	if snapshot.Job.Remaining.Known || snapshot.Material.Known {
		t.Fatal("a value the printer took away still reads as known")
	}

	if got := string(snapshot.Tree[PathJobRemaining]); got != "null" {
		t.Fatalf("the tree holds %q for the cleared path, want the null as it came", got)
	}
}

// TestANegativeTimeRemainingIsReportedAndNotCountedDown refuses the -1 that
// several firmwares answer for "I do not know". It is not a figure a second
// before now, and presenting it as one would be inventing a value.
func TestANegativeTimeRemainingIsReportedAndNotCountedDown(t *testing.T) {
	state, err := Apply(State{}, Record{ID: "d1"},
		[]Update{set(PathJobRemaining, "600")}, appliedAt)
	if err != nil {
		t.Fatalf("the first batch failed: %v", err)
	}

	odd, err := Apply(state, Record{ID: "d1"}, []Update{set(PathJobRemaining, "-1")}, appliedAt.Add(time.Second))

	var problem *ValueError
	if !errors.As(err, &problem) {
		t.Fatalf("a negative remaining time reported %v, want a *ValueError", err)
	}

	if got := odd.Snapshot(appliedAt.Add(time.Second)).Job.Remaining.Value; got != 10*time.Minute {
		t.Fatalf("the remaining time is %v, want the last reading this build could read, 10m", got)
	}
}

// TestTheTreeInASnapshotIsACopy keeps a caller from reaching back into the state
// it was given: a display that edits what it was handed must not change what the
// next window folds into.
func TestTheTreeInASnapshotIsACopy(t *testing.T) {
	state, err := Apply(State{}, Record{ID: "d1"}, []Update{set(PathJobProgress, "10")}, appliedAt)
	if err != nil {
		t.Fatalf("the batch failed: %v", err)
	}

	snapshot := state.Snapshot(appliedAt)
	snapshot.Tree[PathJobProgress] = json.RawMessage("99")
	delete(snapshot.Tree, PathJobProgress)

	again := state.Snapshot(appliedAt)

	if got := again.Job.Progress.Value; got != 10 {
		t.Fatalf("a caller's edit reached the state: the progress now reads %v, want 10", got)
	}
}

// TestTheStatesThisBuildCallsRunning is the table behind Job.Running. A word this
// build has never seen leaves it unknown, because a firmware that added a state
// did not thereby take the job off the bed.
func TestTheStatesThisBuildCallsRunning(t *testing.T) {
	cases := []struct {
		state string
		want  Reading[bool]
	}{
		{StateBusy, Reading[bool]{Value: true, Known: true}},
		{StatePrinting, Reading[bool]{Value: true, Known: true}},
		{StatePaused, Reading[bool]{Value: true, Known: true}},
		{StateAttention, Reading[bool]{Value: true, Known: true}},
		{StateIdle, Reading[bool]{Value: false, Known: true}},
		{StateReady, Reading[bool]{Value: false, Known: true}},
		{StateFinished, Reading[bool]{Value: false, Known: true}},
		{StateStopped, Reading[bool]{Value: false, Known: true}},
		{StateError, Reading[bool]{Value: false, Known: true}},
		{StateUnknown, Reading[bool]{Value: false, Known: true}},
		{"DRYING", Reading[bool]{}},
	}

	for _, current := range cases {
		state, err := Apply(State{}, Record{ID: "d1", Connected: true},
			[]Update{set(PathState, quote(t, current.state))}, appliedAt)
		if err != nil {
			t.Fatalf("%s failed the batch: %v", current.state, err)
		}

		if got := state.Snapshot(appliedAt).Job.Running; got != current.want {
			t.Errorf("%s reads as running %+v, want %+v", current.state, got, current.want)
		}
	}
}

// TestTheJobStateWinsOverTheMachineState holds the one precedence in the tree: a
// machine that reports what the print is doing apart from its own state has said
// something more specific, and it stands.
func TestTheJobStateWinsOverTheMachineState(t *testing.T) {
	state, err := Apply(State{}, Record{ID: "d1", Connected: true}, []Update{
		set(PathState, `"IDLE"`),
		set(PathJobState, `"PAUSED"`),
	}, appliedAt)
	if err != nil {
		t.Fatalf("the batch failed: %v", err)
	}

	if !state.Snapshot(appliedAt).Job.Running.Value {
		t.Fatal("a paused print on an idle machine does not read as a job on the bed")
	}
}

// TestTheWordsThisBuildCallsTerminal is the table behind Snapshot.Terminal, and it is
// the other half of Job.Running: the two words in which a job is over, and nothing
// else. Everything else can still be overtaken by a reading this module has not
// received, so a caller must treat silence about it as doubt.
func TestTheWordsThisBuildCallsTerminal(t *testing.T) {
	cases := []struct {
		state string
		want  bool
	}{
		{StateFinished, true},
		{StateStopped, true},
		{StatePrinting, false},
		{StatePaused, false},
		{StateBusy, false},
		{StateAttention, false},
		{StateIdle, false},
		{StateReady, false},
		{StateError, false},
		{StateUnknown, false},
		{"DRYING", false},
	}

	for _, current := range cases {
		state, err := Apply(State{}, Record{ID: "d1", Connected: true},
			[]Update{set(PathState, quote(t, current.state))}, appliedAt)
		if err != nil {
			t.Fatalf("%s failed the batch: %v", current.state, err)
		}

		if got := state.Snapshot(appliedAt).Terminal(); got != current.want {
			t.Errorf("%s reads as terminal %v, want %v", current.state, got, current.want)
		}
	}
}

// TestTerminalFollowsTheWordThatStands holds Terminal to the same precedence Running
// keeps: a machine that reports what the print is doing apart from its own state has
// said something more specific, and "the job is over" must not be read off a different
// word from "a job is on the bed".
func TestTerminalFollowsTheWordThatStands(t *testing.T) {
	finishedJob, err := Apply(State{}, Record{ID: "d1", Connected: true}, []Update{
		set(PathState, `"PRINTING"`),
		set(PathJobState, `"FINISHED"`),
	}, appliedAt)
	if err != nil {
		t.Fatalf("the batch failed: %v", err)
	}

	snapshot := finishedJob.Snapshot(appliedAt)

	if !snapshot.Terminal() {
		t.Fatal("a machine reporting PRINTING whose job reports FINISHED does not read as terminal")
	}

	if snapshot.Job.Running.Value {
		t.Fatal("the same snapshot reads as a job on the bed; the two readings disagree about the word that stands")
	}

	resumed, err := Apply(State{}, Record{ID: "d1", Connected: true}, []Update{
		set(PathState, `"FINISHED"`),
		set(PathJobState, `"PRINTING"`),
	}, appliedAt)
	if err != nil {
		t.Fatalf("the batch failed: %v", err)
	}

	if resumed.Snapshot(appliedAt).Terminal() {
		t.Fatal("a machine reporting FINISHED whose job reports PRINTING reads as terminal")
	}
}

// TestAPrinterThatSaidNothingIsNotTerminal: no word is not a finished job. A printer
// this build has never heard from is unknown, and silence about it is doubt.
func TestAPrinterThatSaidNothingIsNotTerminal(t *testing.T) {
	state, err := Apply(State{}, Record{ID: "d1", Connected: true}, nil, appliedAt)
	if err != nil {
		t.Fatalf("the empty batch failed: %v", err)
	}

	if state.Snapshot(appliedAt).Terminal() {
		t.Fatal("a printer that has reported no state reads as terminal")
	}
}

// quote renders a state word as the JSON string the tree carries.
func quote(t *testing.T, text string) string {
	t.Helper()

	encoded, err := json.Marshal(text)
	if err != nil {
		t.Fatalf("quote %q: %v", text, err)
	}

	return string(encoded)
}
