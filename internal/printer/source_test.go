package printer

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/prusa3d/prusa-connect/sdk/busapi"
	"github.com/prusa3d/prusa-connect/sdk/deviceapi"
)

// fakeDevices answers the one read a Source makes, counts what it was asked for,
// and can refuse.
type fakeDevices struct {
	device deviceapi.Device
	err    error
	reads  int
}

// Get counts the read and answers with what the test prepared.
func (f *fakeDevices) Get(context.Context, deviceapi.GetParams) (deviceapi.Device, error) {
	f.reads++

	return f.device, f.err
}

// watched builds a Source over a fake fleet and a clock the test drives.
//
// It has no session, because these tests never open a stream: fold and read are
// the two paths a live host would drive, and NewSource refuses a Source with
// nothing to subscribe on, which is the correct refusal for a real module.
func watched(t *testing.T, devices Devices, now func() time.Time) *Source {
	t.Helper()

	return &Source{
		config:   Config{DeviceID: "d1"}.withDefaults(),
		devices:  devices,
		now:      now,
		resync:   make(chan struct{}, 1),
		problems: make(chan error, problemsBuffer),
	}
}

// TestTheWindowCallbackMakesNoDeviceRead is the rule that keeps this module alive.
//
// The window callback runs on the goroutine that reads the bus. If it read the
// device itself, every window would wait on a host call, the windows behind it
// would be lost, and every snapshot taken meanwhile would wait on the same lock.
// So: a window advances the snapshot and the fleet is never asked.
func TestTheWindowCallbackMakesNoDeviceRead(t *testing.T) {
	devices := &fakeDevices{}
	source := watched(t, devices, func() time.Time { return appliedAt })

	window := busapi.Window{
		Telemetry: []json.RawMessage{
			frame(t, "urn:prusa:site:home/device/d1", `{"path":"/state","value":"PRINTING"}`),
			frame(t, "urn:prusa:site:home/device/d1", `{"path":"/job/progress","value":94}`),
		},
		Cursor: "cursor-1",
	}

	if err := source.fold(window); err != nil {
		t.Fatalf("the window failed: %v", err)
	}

	if devices.reads != 0 {
		t.Fatalf("the window callback read the device %d times, want none", devices.reads)
	}

	if got := source.Snapshot().Job.Progress.Value; got != 94 {
		t.Fatalf("the snapshot reads %v after the window, want 94", got)
	}
}

// TestAResyncAsksForAReadWithoutMakingOne holds the other half of that rule. A
// resync says continuity is gone and the answer is a read, so the callback asks the
// read goroutine for one and returns: two resyncs in a row arm one read and neither
// of them waits.
func TestAResyncAsksForAReadWithoutMakingOne(t *testing.T) {
	devices := &fakeDevices{}
	source := watched(t, devices, func() time.Time { return appliedAt })

	for range 2 {
		if err := source.fold(busapi.Window{Resync: true}); err != nil {
			t.Fatalf("the resync window failed: %v", err)
		}
	}

	if devices.reads != 0 {
		t.Fatalf("the window callback read the device %d times, want none", devices.reads)
	}

	select {
	case <-source.resync:
	default:
		t.Fatal("a resync armed no read, want one")
	}

	select {
	case <-source.resync:
		t.Fatal("two resyncs armed two reads, want the second folded into the first")
	default:
	}
}

// TestASourceReadsTheDeviceAndSaysWhatItHolds is the read path whole: one call to
// the fleet, folded in, and a snapshot that carries the identity, the session and
// the tree the host answered with.
func TestASourceReadsTheDeviceAndSaysWhatItHolds(t *testing.T) {
	devices := &fakeDevices{device: deviceapi.Device{
		ID:        "d1",
		Name:      "MK4",
		Connected: true,
		State: map[string]json.RawMessage{
			PathState:       json.RawMessage(`"PRINTING"`),
			PathJobProgress: json.RawMessage("94"),
			PathBedTemp:     json.RawMessage("59.9"),
		},
		Issues: []deviceapi.Issue{{Reason: "filament_runout", Severity: "error", Detail: "PETG in slot 1"}},
	}}

	source := watched(t, devices, func() time.Time { return appliedAt })

	if err := source.read(context.Background()); err != nil {
		t.Fatalf("the read failed: %v", err)
	}

	snapshot := source.Snapshot()

	if devices.reads != 1 {
		t.Fatalf("the source made %d reads, want 1", devices.reads)
	}

	if snapshot.ID != "d1" || snapshot.Name != "MK4" || !snapshot.Connected {
		t.Fatalf("the snapshot identity is %q/%q connected %v, want d1/MK4 connected",
			snapshot.ID, snapshot.Name, snapshot.Connected)
	}

	if progress, ok := snapshot.LiveProgress(); !ok || progress != 94 {
		t.Fatalf("the snapshot reports live progress %v (ok %v), want 94", progress, ok)
	}

	if !snapshot.Bed.Now.Known || snapshot.Bed.Now.Value != 59.9 {
		t.Fatalf("the bed reads %v (known %v), want 59.9", snapshot.Bed.Now.Value, snapshot.Bed.Now.Known)
	}

	if len(snapshot.Issues) != 1 || snapshot.Issues[0].Reason != "filament_runout" {
		t.Fatalf("the issues are %+v, want the host's filament_runout", snapshot.Issues)
	}
}

// TestAFailedReadLeavesTheLastReadingStanding is what a read that cannot reach the
// host must do: change nothing. The snapshot then ages, which a caller can see,
// rather than inventing a second answer or clearing what it knew.
func TestAFailedReadLeavesTheLastReadingStanding(t *testing.T) {
	now := appliedAt

	devices := &fakeDevices{device: deviceapi.Device{
		ID:        "d1",
		Connected: true,
		State:     map[string]json.RawMessage{PathJobProgress: json.RawMessage("94")},
	}}

	source := watched(t, devices, func() time.Time { return now })

	if err := source.read(context.Background()); err != nil {
		t.Fatalf("the first read failed: %v", err)
	}

	devices.err = errors.New("the host could not answer")
	now = appliedAt.Add(40 * time.Second)

	if err := source.read(context.Background()); err == nil {
		t.Fatal("a read the host refused reported no error")
	}

	snapshot := source.Snapshot()

	if !snapshot.Connected {
		t.Fatal("a failed read cleared the session the host last reported")
	}

	if got := snapshot.Job.Progress.Value; got != 94 {
		t.Fatalf("a failed read changed the tree: progress is %v, want 94", got)
	}

	if !snapshot.Stale {
		t.Fatalf("the snapshot is %v old after a failed read and not stale, want stale", snapshot.Age)
	}

	if _, ok := snapshot.LiveProgress(); ok {
		t.Fatal("a snapshot nothing has been heard about for 40s offers a live progress")
	}
}

// TestAProblemReachesACallerAndNeverBlocksTheStream is the reporting rule. A value
// this build cannot read is offered to a caller that logs, and a caller that never
// reads costs the callback nothing.
func TestAProblemReachesACallerAndNeverBlocksTheStream(t *testing.T) {
	devices := &fakeDevices{}
	source := watched(t, devices, func() time.Time { return appliedAt })

	window := busapi.Window{Telemetry: []json.RawMessage{
		frame(t, "urn:prusa:site:home/device/d1", `{"path":"/job/progress","value":"a lot"}`),
	}}

	if err := source.fold(window); err != nil {
		t.Fatalf("a value this build cannot read ended the subscription: %v", err)
	}

	select {
	case problem := <-source.Problems():
		var named *ValueError
		if !errors.As(problem, &named) {
			t.Fatalf("the problem reported %v, want a *ValueError", problem)
		}
	default:
		t.Fatal("a value this build cannot read reached no caller")
	}

	// Nobody reads the rest, and the callback still returns.
	for range problemsBuffer * 2 {
		if err := source.fold(window); err != nil {
			t.Fatalf("a full problem buffer ended the subscription: %v", err)
		}
	}
}
