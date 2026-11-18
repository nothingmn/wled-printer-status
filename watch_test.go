package main

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nothingmn/wled-printer-status/internal/printer"
	"github.com/nothingmn/wled-printer-status/internal/wled"
)

// fixedSource is one binding's printer, holding one snapshot and reporting nothing. It runs
// until its context ends, which is what a watch's stop is.
type fixedSource struct {
	snapshot printer.Snapshot
	problems chan error
}

func (f *fixedSource) Snapshot() printer.Snapshot { return f.snapshot }

func (f *fixedSource) Problems() <-chan error { return f.problems }

func (f *fixedSource) Run(ctx context.Context) error {
	<-ctx.Done()

	return ctx.Err()
}

// guardedStrip is a controller that notices two writes inside it at once. It is the fakeStrip the
// writer tests already use, with a door and a stopwatch: the door counts who is inside, and the
// stopwatch makes a write long enough that two of them overlap if nothing is holding them apart.
type guardedStrip struct {
	*fakeStrip

	inside  atomic.Int32
	overlap atomic.Int32
	took    time.Duration
}

func (g *guardedStrip) SetSegments(ctx context.Context, segments []wled.SegmentWrite) error {
	if g.inside.Add(1) > 1 {
		g.overlap.Add(1)
	}

	defer g.inside.Add(-1)

	if g.took > 0 {
		time.Sleep(g.took)
	}

	return g.fakeStrip.SetSegments(ctx, segments)
}

// printingJob is one printer in the middle of a job, which is the snapshot that makes a frame
// carry a fill and a colour rather than nothing.
func printingJob(id string, progress float64) printer.Snapshot {
	now := time.Now()

	return printer.Snapshot{
		ID:        id,
		Name:      id,
		Connected: true,
		State:     printer.Reading[string]{Value: printer.StatePrinting, Known: true},
		Job: printer.Job{
			Live:     true,
			Running:  printer.Reading[bool]{Value: true, Known: true},
			Progress: printer.Reading[float64]{Value: progress, Known: true},
		},
		LastChange: now,
		ObservedAt: now,
	}
}

// eventually waits for a condition the watch reaches on its own goroutines, and fails the test
// with what was seen instead of waiting out the test binary's timeout.
func eventually(t *testing.T, within time.Duration, seen func() string, cond func() bool) {
	t.Helper()

	deadline := time.Now().Add(within)

	for time.Now().Before(deadline) {
		if cond() {
			return
		}

		time.Sleep(2 * time.Millisecond)
	}

	t.Fatalf("the watch did not get there within %s; it had: %s", within, seen())
}

// TestAWatchRunsOneBindingPerSegmentOfOneController is the multi-binding core end to end: two
// printers shown on two segments of one controller, one client and one door between them, and
// each writer drawing only inside the segment it borrowed and giving it back on the way out.
func TestAWatchRunsOneBindingPerSegmentOfOneController(t *testing.T) {
	forgetBindings()

	t.Cleanup(func() {
		forgetBindings()
		current.Store(nil)
		sent.Store(nil)
	})

	light := &guardedStrip{fakeStrip: newFakeStrip(), took: 25 * time.Millisecond}
	light.state = wled.State{Segments: []wled.Segment{
		{ID: 2, Start: 10, Stop: 18, Name: "Front", On: true},
		{ID: 5, Start: 30, Stop: 40, Name: "Side", On: true},
	}}

	front := &fixedSource{snapshot: printingJob("printer-a", 50)}
	side := &fixedSource{snapshot: printingJob("printer-b", 60)}

	var madeSources int
	var madeLights int

	watched, done, err := watchConfig{
		bindings: bindings{
			{DeviceID: "printer-a", Address: "http://10.0.0.136", Segment: 2},
			{DeviceID: "printer-b", Address: "http://10.0.0.136", Segment: 5},
		},
		newSource: func(b binding) (watchSource, error) {
			madeSources++

			if b.DeviceID == "printer-a" {
				return front, nil
			}

			return side, nil
		},
		newLight: func(string) (strip, error) {
			madeLights++

			return light, nil
		},
		logf:    quiet,
		tick:    5 * time.Millisecond,
		publish: publishBinding,
		record:  recordBinding,
	}.start(context.Background())
	if err != nil {
		t.Fatalf("the watch did not start: %v", err)
	}

	defer func() {
		watched()

		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("the watch did not give its segments back within five seconds of being stopped")
		}
	}()

	eventually(t, 3*time.Second, func() string {
		return segmentsPhrase(light.shown())
	}, func() bool {
		return len(light.shown()) >= 2
	})

	if madeLights != 1 {
		t.Fatalf("the controller was built %d times, want once: one client and one door per controller", madeLights)
	}

	if madeSources != 2 {
		t.Fatalf("%d printer sources were built, want one per binding", madeSources)
	}

	if overlaps := light.overlap.Load(); overlaps != 0 {
		t.Fatalf("%d writes overlapped on one controller, and one controller hears one thing at a time", overlaps)
	}

	for _, call := range light.shown() {
		if len(call.segments) != 1 {
			t.Fatalf("one write carried %d segments, want the one this binding borrowed", len(call.segments))
		}

		if id := call.segments[0].ID; id != 2 && id != 5 {
			t.Fatalf("a write addressed segment %d, and this watch borrows 2 and 5", id)
		}
	}

	if held := bindingsSeen(); len(held) != 2 {
		t.Fatalf("the status store holds %d bindings, want 2: one body per binding", len(held))
	}

	for _, held := range bindingsSeen() {
		if held.LastSend == nil || !held.LastSend.OK {
			t.Fatalf("the binding on segment %d reports no successful send: %+v", held.Segment, held.LastSend)
		}

		if held.Frame.Total == 0 {
			t.Fatalf("the binding on segment %d reports no frame", held.Segment)
		}
	}

	watched()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the watch did not stop within five seconds")
	}

	if given := light.givenBack(); len(given) != 2 {
		t.Fatalf("%d segments were given back, want both borrowed ones: a shared controller is left as it was found", len(given))
	}
}

// TestAWatchRefusesTwoBindingsOnOneSegmentBeforeItBuildsAnything is the refusal at the door: the
// set is judged before a client is built, so a mistake is a sentence on the instance rather than
// two writers undoing each other on a controller.
func TestAWatchRefusesTwoBindingsOnOneSegmentBeforeItBuildsAnything(t *testing.T) {
	var madeLights, madeSources int

	_, _, err := watchConfig{
		bindings: bindings{
			{DeviceID: "printer-a", Address: "http://10.0.0.136", Segment: 2},
			{DeviceID: "printer-b", Address: "http://10.0.0.136", Segment: 2},
		},
		newSource: func(binding) (watchSource, error) {
			madeSources++

			return &fixedSource{snapshot: printingJob("printer-a", 10)}, nil
		},
		newLight: func(string) (strip, error) {
			madeLights++

			return newFakeStrip(), nil
		},
		logf: quiet,
	}.start(context.Background())

	if err == nil {
		t.Fatal("a set with two bindings on one segment was started, want a refusal")
	}

	if !strings.Contains(err.Error(), "one segment is shown by one binding") {
		t.Fatalf("the refusal reads %q, want it to name the rule", err)
	}

	if madeLights != 0 || madeSources != 0 {
		t.Fatalf("the refusal still built %d clients and %d sources, want nothing built", madeLights, madeSources)
	}
}

// TestAWatchWithOneBindingKeepsTheSingleStatusBody is the shape an install built from settings
// actually has: one binding, and /status answering the body it has always answered rather than a
// list of one. The route reads the per-binding store first, so this is what keeps a one-printer
// install's answer unchanged.
func TestAWatchWithOneBindingKeepsTheSingleStatusBody(t *testing.T) {
	forgetBindings()

	t.Cleanup(func() {
		forgetBindings()
		current.Store(nil)
		sent.Store(nil)
	})

	light := newFakeStrip()

	watched, done, err := watchConfig{
		bindings: bindings{{DeviceID: "printer-a", Address: "http://10.0.0.136", Segment: wholeStrip, Brightness: 128}},
		newSource: func(binding) (watchSource, error) {
			return &fixedSource{snapshot: printingJob("printer-a", 25)}, nil
		},
		newLight: func(string) (strip, error) { return light, nil },
		logf:     quiet,
		tick:     5 * time.Millisecond,
	}.start(context.Background())
	if err != nil {
		t.Fatalf("the watch did not start: %v", err)
	}

	defer func() {
		watched()

		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("the watch did not stop within five seconds")
		}
	}()

	eventually(t, 3*time.Second, func() string { return "no frame yet" }, func() bool {
		return current.Load() != nil
	})

	if held := bindingsSeen(); len(held) != 0 {
		t.Fatalf("a one-binding watch wrote %d entries to the per-binding store, want none", len(held))
	}
}

// segmentsPhrase is what a failed wait prints: the segment ids the controller was asked to draw.
func segmentsPhrase(calls []paintCall) string {
	if len(calls) == 0 {
		return "no write at all"
	}

	ids := make([]string, 0, len(calls))

	for _, call := range calls {
		if len(call.segments) == 0 {
			ids = append(ids, "no segment")

			continue
		}

		for _, segment := range call.segments {
			ids = append(ids, fmt.Sprintf("segment %d", segment.ID))
		}
	}

	return strings.Join(ids, ", ")
}
