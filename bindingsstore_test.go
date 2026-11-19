// The gate wave 4 has to pass: a binding changed through a route is a binding in the module's own
// document, and the next start runs what the route said.
//
// The defect these tests were written against was not visible from a handler's name. Every route
// changed the running set through runner.revise, which stops the watch and rebuilds it — so a
// change took effect, answered `applied: true`, and was gone at the next restart because nothing
// on that path ever called saveStore. The state, moment, layout, brightness-window and pause
// tables were already written through configTables.revise; the bindings were not. These tests hold
// the same claim for the bindings and for the fact that all six surfaces share one document.
//
// A restart is what has to be proved, and a restart is what these tests simulate rather than
// perform: the module is live on the operator's wall, and stopping it costs him sixteen seconds of
// dark panel. So the proof is the real start path — configurationFor, which is what run calls —
// reading the file the route wrote, with a reader that holds nothing from the route's process.
package main

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/prusa3d/prusa-connect/sdk/deviceapi"
)

// startReader is a restart: one fresh read of the store through the path a start takes, with a
// printer list of its own and no memory of the process that wrote the file.
//
// It is configurationFor and not loadStore, because loadStore proves the bytes and this proves the
// set the module would run — the ids, the lights, the segments, and the check that a set written
// through a route is a set that starts.
func startReader(t *testing.T, dir string, printers []deviceapi.Device) (bindings, string) {
	t.Helper()

	if dir == "" {
		t.Fatalf("this fixture was granted no store, so there is nothing to restart from")
	}

	page := deviceapi.Page{Items: printers, Total: len(printers)}

	set, origin, err := configurationFor(context.Background(), fakeFleet{page: page}, Settings{}, dir, quiet)
	if err != nil {
		t.Fatalf("the store a route wrote would not start: %v", err)
	}

	return set, origin
}

// onlyBinding is the one binding in a set, named, or a failure saying what was in it instead.
func onlyBinding(t *testing.T, what string, set bindings) binding {
	t.Helper()

	if len(set) != 1 {
		t.Fatalf("%s runs %d bindings, want 1: %+v", what, len(set), set)
	}

	return set[0]
}

// sameSet says whether two sets are the same bindings in the same order, and names the first
// difference when they are not.
func sameSet(t *testing.T, what string, got, want bindings) {
	t.Helper()

	if len(got) != len(want) {
		t.Fatalf("%s holds %d bindings, want %d: %+v", what, len(got), len(want), got)
	}

	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s holds %+v at position %d, want %+v", what, got[i], i+1, want[i])
		}
	}
}

// TestABindingCreatedThroughTheRouteIsInTheStoreAndComesBackAtTheNextStart is the defect stated as
// a test: the binding a route created is in the document, and a fresh reader — the start, not this
// process's memory — runs it with its own id, its light and its segment.
func TestABindingCreatedThroughTheRouteIsInTheStoreAndComesBackAtTheNextStart(t *testing.T) {
	light := newFakeStrip()
	light.state = wledStateForSegments(3, 4)

	api := newAPIUnderTest(t, light, testPrinters, bindings{
		{DeviceID: "printer-a", Address: "http://10.0.0.136", Segment: wholeStrip, Brightness: 128},
	})

	status, body := api.do(t, http.MethodPost, "/bindings",
		`{"printer": "Mini", "controller": "http://10.0.0.253", "segment": 3}`)
	if status != http.StatusCreated {
		t.Fatalf("POST /bindings answered %d, want %d: %v", status, http.StatusCreated, body)
	}

	if body["persisted"] != true {
		t.Fatalf("the answer says persisted %v, and this change was written into the module's own store", body["persisted"])
	}

	added := body["binding"].(map[string]any)
	if added["id"] != "printer-b" || added["segment"].(float64) != 3 {
		t.Fatalf("the binding added is %v, want printer-b on segment 3", added)
	}

	// A reader of the file, and not the process that wrote it.
	found, err := loadStore(api.dir)
	if err != nil {
		t.Fatalf("the store a route wrote cannot be read: %v", err)
	}

	if found.Config.Version != storeVersion {
		t.Fatalf("the document a route wrote carries version %d, want %d", found.Config.Version, storeVersion)
	}

	if len(found.Config.Bindings) != 2 {
		t.Fatalf("the store holds %d bindings, want 2: %s", len(found.Config.Bindings), found.Config.Bindings)
	}

	// And the start the next container restart performs reads it: the binding is there with the
	// name the answer promised, on the light it named, on the segment it named.
	set, origin := startReader(t, api.dir, testPrinters)

	if origin != originStore {
		t.Fatalf("the next start says its set came from %q, want the store", origin)
	}

	var carried *binding

	for i := range set {
		if set[i].DeviceID == "printer-b" {
			carried = &set[i]
		}
	}

	if carried == nil {
		t.Fatalf("the next start runs %+v, and the binding the route created is not in it", set)
	}

	if carried.ID != "printer-b" {
		t.Fatalf("the binding comes back named %q, want printer-b", carried.ID)
	}

	if carried.Address != "http://10.0.0.253" || carried.Segment != 3 {
		t.Fatalf("the binding comes back on %s segment %d, want http://10.0.0.253 segment 3",
			carried.Address, carried.Segment)
	}
}

// TestABindingReplacedThroughTheRouteKeepsItsNameAndMovesInTheStore is the case that is easy to get
// wrong twice: a PUT that moves a binding to another segment must write the moved value, and it
// must not rename the binding — a name that never moves was a deliberate choice, because the
// layout table is keyed on it and a screen already holds the path.
func TestABindingReplacedThroughTheRouteKeepsItsNameAndMovesInTheStore(t *testing.T) {
	light := newFakeStrip()
	light.state = wledStateForSegments(2, 5)

	api := newAPIUnderTest(t, light, testPrinters, bindings{
		{DeviceID: "printer-a", Address: "http://10.0.0.136", Segment: wholeStrip, Brightness: 128},
	})

	before := api.watch.set()
	if len(before) != 1 || before[0].ID != "printer-a" {
		t.Fatalf("the fixture runs %+v, want one binding named printer-a", before)
	}

	status, body := api.do(t, http.MethodPut, "/bindings/printer-a",
		`{"printer": "printer-a", "controller": "http://10.0.0.136", "segment": 2}`)
	if status != http.StatusOK {
		t.Fatalf("PUT /bindings/printer-a answered %d, want %d: %v", status, http.StatusOK, body)
	}

	replaced := body["binding"].(map[string]any)
	if replaced["id"] != "printer-a" {
		t.Fatalf("the replaced binding is named %v, want printer-a: a replace moves where it is shown, not what it is called", replaced["id"])
	}

	if replaced["segment"].(float64) != 2 || replaced["whole"] != false {
		t.Fatalf("the replaced binding reads %v, want segment 2 of the panel", replaced)
	}

	found, err := loadStore(api.dir)
	if err != nil {
		t.Fatalf("the store a route wrote cannot be read: %v", err)
	}

	// The moved value is on disk, under the name it already had, and the whole-controller
	// brightness it used to carry went with the move: a borrowed segment carries none.
	raw := storedRaw(t, api.dir)

	lines, _ := raw["bindings"].([]any)
	if len(lines) != 1 {
		t.Fatalf("the store holds %v bindings after a replace, want 1", raw["bindings"])
	}

	line, _ := lines[0].(map[string]any)

	if id, _ := line["id"].(string); id != "printer-a" {
		t.Fatalf("the stored line is named %q, want printer-a", id)
	}

	if segment, _ := line["segment"].(float64); int(segment) != 2 {
		t.Fatalf("the stored line is on segment %v, want 2 — the move is what a restart has to keep", line["segment"])
	}

	if _, carried := line["brightness"]; carried {
		t.Fatalf("the stored line carries a brightness on a borrowed segment: %v", line)
	}

	if len(found.Config.Bindings) != 1 {
		t.Fatalf("the document reads %d bindings, want 1", len(found.Config.Bindings))
	}

	// The restart.
	set, _ := startReader(t, api.dir, testPrinters)

	moved := onlyBinding(t, "the next start", set)

	if moved.ID != "printer-a" {
		t.Fatalf("the moved binding comes back named %q, want printer-a", moved.ID)
	}

	if moved.Address != "http://10.0.0.136" || moved.Segment != 2 {
		t.Fatalf("the moved binding comes back on %s segment %d, want http://10.0.0.136 segment 2",
			moved.Address, moved.Segment)
	}
}

// TestABindingDeletedThroughTheRouteIsGoneFromTheStore is the delete half: the row comes out of
// the file rather than only out of the set this process is running, so the binding does not come
// back at the next start — and the binding it did not name is still there.
func TestABindingDeletedThroughTheRouteIsGoneFromTheStore(t *testing.T) {
	light := newFakeStrip()
	light.state = wledStateForSegments(3, 4)

	api := newAPIUnderTest(t, light, testPrinters, bindings{
		{DeviceID: "printer-a", Address: "http://10.0.0.253", Segment: 3},
		{DeviceID: "printer-b", Address: "http://10.0.0.253", Segment: 4},
	})

	status, body := api.do(t, http.MethodDelete, "/bindings/printer-a", "")
	if status != http.StatusOK {
		t.Fatalf("DELETE /bindings/printer-a answered %d, want %d: %v", status, http.StatusOK, body)
	}

	if body["deleted"] != "printer-a" || body["driving"] != true || body["persisted"] != true {
		t.Fatalf("the answer reads %v, want printer-a deleted, still driving, and persisted", body)
	}

	raw := storedRaw(t, api.dir)

	lines, _ := raw["bindings"].([]any)
	if len(lines) != 1 {
		t.Fatalf("the store holds %v bindings after a delete, want 1: %v", raw["bindings"], raw["bindings"])
	}

	line, _ := lines[0].(map[string]any)
	if id, _ := line["id"].(string); id != "printer-b" {
		t.Fatalf("the one line left is named %q, want printer-b: the deleted binding is the one that goes", id)
	}

	set, _ := startReader(t, api.dir, testPrinters)

	left := onlyBinding(t, "the next start", set)
	if left.ID != "printer-b" || left.Segment != 4 {
		t.Fatalf("the next start runs %+v, want printer-b on segment 4 alone", left)
	}
}

// TestAStoreThatCannotBeWrittenRefusesTheChangeAndLeavesTheSetRunning is the difference between a
// refusal and a lie: a store the change cannot reach answers a failure, and the set that was
// running is the set that is still running. It used to answer applied-true/persisted-false, which
// is a change the light shows and the next restart takes away.
func TestAStoreThatCannotBeWrittenRefusesTheChangeAndLeavesTheSetRunning(t *testing.T) {
	start := bindings{{DeviceID: "printer-a", Address: "http://10.0.0.136", Segment: wholeStrip, Brightness: 128}}

	cases := []struct {
		name  string
		store func(t *testing.T) *configTables
		want  []string
	}{
		{
			name: "a module granted no data directory",
			store: func(*testing.T) *configTables {
				return nil
			},
			want: []string{"granted no data directory"},
		},
		{
			name: "a granted path that is not a directory",
			store: func(t *testing.T) *configTables {
				t.Helper()

				blocked := filepath.Join(t.TempDir(), "not-a-store")
				if err := os.WriteFile(blocked, []byte("this is a file, not a data directory\n"), 0o600); err != nil {
					t.Fatalf("write the blocking file: %v", err)
				}

				tables := &configTables{}
				tables.grant(blocked)

				return tables
			},
			want: []string{"not a directory"},
		},
		{
			name: "a document a newer release wrote",
			store: func(t *testing.T) *configTables {
				t.Helper()

				dir := writeStoreFile(t, `{"version": 5, "bindings": []}`)

				tables := &configTables{}
				tables.grant(dir)

				return tables
			},
			want: []string{"schema version 5", "version 4"},
		},
	}

	for _, one := range cases {
		t.Run(one.name, func(t *testing.T) {
			light := newFakeStrip()

			api := newAPIStored(t, light, testPrinters, start,
				func(string) (strip, error) { return light, nil }, one.store(t))

			before := api.watch.set()

			responses := []struct {
				method string
				path   string
				body   string
			}{
				{http.MethodPost, "/bindings", `{"printer": "Mini", "controller": "http://10.0.0.253", "segment": 3}`},
				{http.MethodPut, "/bindings/printer-a", `{"controller": "http://10.0.0.136", "segment": 2}`},
				{http.MethodDelete, "/bindings/printer-a", ""},
			}

			for _, sent := range responses {
				status, body := api.do(t, sent.method, sent.path, sent.body)

				if status != http.StatusServiceUnavailable {
					t.Fatalf("%s %s answered %d, want %d: %v",
						sent.method, sent.path, status, http.StatusServiceUnavailable, body)
				}

				if got := code(t, body); got != StoreUnavailable.Code().String() {
					t.Fatalf("%s %s answered %q, want %q", sent.method, sent.path, got, StoreUnavailable.Code().String())
				}

				// The sentence says why, in the operator's terms: a store this module cannot
				// reach is a refusal, and the reason has to name the thing he can fix.
				detail, _ := body["detail"].(string)
				if detail == "" {
					t.Fatalf("%s %s refused without saying why: %v", sent.method, sent.path, body)
				}

				for _, wanted := range one.want {
					if !strings.Contains(detail, wanted) {
						t.Fatalf("%s %s refused with %q, and the reason does not name %q",
							sent.method, sent.path, detail, wanted)
					}
				}

				// And nothing was applied: the answer must not carry applied at all, and the
				// set the writer is running is the set it was running before the request.
				if _, applied := body["applied"]; applied {
					t.Fatalf("%s %s refused and still says applied: %v", sent.method, sent.path, body)
				}

				sameSet(t, "the running set after a refused "+sent.method, api.watch.set(), before)
			}

			// The light is still being written to on the binding that was already there: a
			// refusal that stopped the writer would be a dark wall for a reason nobody asked for.
			eventually(t, 3*time.Second, func() string { return segmentsPhrase(light.shown()) }, func() bool {
				return len(light.shown()) > 0
			})
		})
	}
}

// TestEveryEditingSurfaceWritesTheSameDocument is the regression test for the shape of the fix
// rather than for one route: the bindings and the five tables are written through one path into
// one document, so a write on any of them finds the others and leaves them alone. Four copies of
// the write path would pass every single-route test in this repository and lose a table the first
// time somebody edited a binding.
func TestEveryEditingSurfaceWritesTheSameDocument(t *testing.T) {
	light := newFakeStrip()
	light.state = wledStateForSegments(3, 4)

	start := bindings{
		{DeviceID: "printer-a", Address: "http://10.0.0.136", Segment: wholeStrip, Brightness: 128},
	}

	api := newAPIStored(t, light, testPrinters, start,
		func(string) (strip, error) { return light, nil }, storeFor(t, start))

	// The tables' routes, on the same watch and the same store as the bindings' routes: one
	// process, one document, six surfaces.
	mux, ok := api.mux.(*http.ServeMux)
	if !ok {
		t.Fatalf("the fixture's routes are not a ServeMux")
	}

	page := deviceapi.Page{Items: testPrinters, Total: len(testPrinters)}

	tableAPI{
		live:   api.watch,
		fleet:  func() devices { return fakeFleet{page: page} },
		tables: api.store,
		bound:  time.Second,
		clock:  func() time.Time { return time.Date(2026, 11, 19, 23, 0, 0, 0, mustZone("America/Denver")) },
		zone:   func() string { return "America/Denver" },
	}.mount(mux)

	// One write on every surface that keeps something.
	writes := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodPost, "/bindings", `{"printer": "Mini", "controller": "http://10.0.0.253", "segment": 3}`},
		{http.MethodPut, "/states/PAUSED", `{"colour": {"r": 12, "g": 34, "b": 56}, "word": "PAUSE", "holds": "60s"}`},
		{http.MethodPut, "/lights/10.0.0.136/moments/print-cancelled",
			`{"enabled": true, "shows": "Colour", "which": "STOPPED", "for": "30s"}`},
		{http.MethodPut, "/bindings/printer-a/layout",
			`{"bar": false, "bar_band": "top", "bar_rows": 20, "percent_scale": 4}`},
		{http.MethodPost, "/windows", `{"from": "20:00", "to": "07:00", "brightness": 40}`},
		{http.MethodPut, "/pause", `{"for": "1h"}`},
	}

	for _, sent := range writes {
		if status, body := api.do(t, sent.method, sent.path, sent.body); status != http.StatusOK && status != http.StatusCreated {
			t.Fatalf("%s %s answered %d, want a write that was kept: %v", sent.method, sent.path, status, body)
		}
	}

	// Everything in one document, at this build's version.
	found, err := loadStore(api.dir)
	if err != nil {
		t.Fatalf("the document six writes went into cannot be read: %v", err)
	}

	if found.Config.Version != storeVersion {
		t.Fatalf("the document carries version %d, want %d", found.Config.Version, storeVersion)
	}

	if len(found.Config.Bindings) != 2 {
		t.Fatalf("the document holds %d bindings, want 2: the binding write is the only one that should add one",
			len(found.Config.Bindings))
	}

	if len(found.Config.States) != 1 {
		t.Fatalf("the document holds %d state rows, want the one PAUSED row: %+v", len(found.Config.States), found.Config.States)
	}

	if len(found.Config.Moments) != 1 {
		t.Fatalf("the document holds %d moment rows, want 1: %+v", len(found.Config.Moments), found.Config.Moments)
	}

	if len(found.Config.Layouts) != 1 {
		t.Fatalf("the document holds %d layout rows, want 1: %+v", len(found.Config.Layouts), found.Config.Layouts)
	}

	if len(found.Config.Windows) != 1 {
		t.Fatalf("the document holds %d windows, want 1: %+v", len(found.Config.Windows), found.Config.Windows)
	}

	if len(found.Config.Pauses) != 1 {
		t.Fatalf("the document holds %d pauses, want 1: %+v", len(found.Config.Pauses), found.Config.Pauses)
	}

	// The half that catches a second write path: a binding write afterwards must find the five
	// tables where the tables' routes left them.
	if status, body := api.do(t, http.MethodDelete, "/bindings/printer-b", ""); status != http.StatusOK {
		t.Fatalf("DELETE /bindings/printer-b answered %d: %v", status, body)
	}

	if status, body := api.do(t, http.MethodPost, "/bindings",
		`{"printer": "Core One", "controller": "http://10.0.0.253", "segment": 3}`); status != http.StatusCreated {
		t.Fatalf("the second POST /bindings answered %d: %v", status, body)
	}

	after, err := loadStore(api.dir)
	if err != nil {
		t.Fatalf("the document cannot be read after two binding writes: %v", err)
	}

	if len(after.Config.States) != 1 || len(after.Config.Moments) != 1 || len(after.Config.Layouts) != 1 ||
		len(after.Config.Windows) != 1 || len(after.Config.Pauses) != 1 {
		t.Fatalf("a binding write lost a table: states %d, moments %d, layouts %d, windows %d, pauses %d",
			len(after.Config.States), len(after.Config.Moments), len(after.Config.Layouts),
			len(after.Config.Windows), len(after.Config.Pauses))
	}

	if len(after.Config.Bindings) != 2 {
		t.Fatalf("the document holds %d bindings after a delete and a create, want 2: %s",
			len(after.Config.Bindings), after.Config.Bindings)
	}

	// And the restart runs the two bindings that are left, on the segments the routes named.
	set, _ := startReader(t, api.dir, testPrinters)
	if len(set) != 2 {
		t.Fatalf("the next start runs %d bindings, want 2: %+v", len(set), set)
	}

	for _, want := range []struct {
		printer string
		segment int
	}{{"printer-a", wholeStrip}, {"printer-c", 3}} {
		var held *binding

		for i := range set {
			if set[i].DeviceID == want.printer {
				held = &set[i]
			}
		}

		if held == nil {
			t.Fatalf("the next start runs %+v, and %s is not in it", set, want.printer)
		}

		if held.Segment != want.segment {
			t.Fatalf("%s comes back on segment %d, want %d", want.printer, held.Segment, want.segment)
		}
	}
}
