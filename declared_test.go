// The tests that hold the declaration to the code: the five options are the only addresses this
// module contacts, an address in the store which is not one of them is never dialled, and a route
// that would write one is refused before the store moves.
//
// Each of these is written to fail against the code before the cap existed. The first one is the
// deliberately-broken one: point the permits at an empty settings block and the runner drives
// everything in the store, and the recording controller below sees the request it must not.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nothingmn/wled-printer-status/internal/wled"
)

// recordingController is a WLED-shaped server that counts every request that reaches it. It answers
// the two paths a writer reads before it paints, so a client that is allowed to drive it gets far
// enough to be seen; a client that must never be built leaves the count at zero.
func recordingController(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()

	var hits atomic.Int32

	server := httptest.NewServer(recordingHandler(&hits))

	t.Cleanup(server.Close)

	return server, &hits
}

// recordingHandler is one controller's answers, counting every request it receives.
func recordingHandler(hits *atomic.Int32) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)

		w.Header().Set("Content-Type", "application/json")

		switch {
		case strings.HasSuffix(r.URL.Path, "/json/info"):
			_, _ = io.WriteString(w, `{"name":"Recorder","ver":"0.14.0","leds":{"count":30}}`)
		case strings.HasSuffix(r.URL.Path, "/json/state"):
			_, _ = io.WriteString(w, `{"on":true,"bri":128,"seg":[{"id":0,"start":0,"stop":30}]}`)
		default:
			_, _ = io.WriteString(w, `{}`)
		}
	})
}

// controllerOnPort80 is a recording controller at an address the address rule admits: plain http on
// port 80, which is the only port a light address may name. The option value is therefore the bare
// host, and the server is real rather than a fake, so that "the declared light is painted" stays a
// claim about a socket.
//
// It binds the loopback's port 80 rather than a port of its own. That is not a convenience: an
// address with a port of its own is the very thing this release refuses, so a test that points the
// module at `127.0.0.1:35019` would be testing an address this module cannot be configured with.
func controllerOnPort80(t *testing.T, address string) (*atomic.Int32, string) {
	t.Helper()

	var hits atomic.Int32

	listener, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatalf("listen on %s, which stands in for a light address port 80 is the only one of: %v", address, err)
	}

	server := &httptest.Server{Listener: listener, Config: &http.Server{Handler: recordingHandler(&hits)}}
	server.Start()

	t.Cleanup(server.Close)

	return &hits, "http://" + strings.TrimSuffix(address, ":80")
}

// storeNaming is a document holding one binding and one light row, both at one address.
func storeNaming(t *testing.T, address string) *configTables {
	t.Helper()

	entries, err := storeEntries(bindings{{
		ID: "binding-1", DeviceID: "printer-1", Address: address, Segment: -1, Brightness: 128,
	}})
	if err != nil {
		t.Fatalf("render the fixture's binding: %v", err)
	}

	dir := t.TempDir()

	_, err = saveStore(dir, storedConfig{
		Version:  storeVersion,
		Bindings: entries,
		Lights:   []storedLight{{Address: address}},
	})
	if err != nil {
		t.Fatalf("write the fixture's store: %v", err)
	}

	tables := &configTables{}
	tables.grant(dir)

	return tables
}

// runningAt is a runner with one fake printer per binding and the process's own client factory, so
// a permitted address is dialled for real and an undeclared one is never built.
func runningAt(t *testing.T, declaration Settings, tables *configTables) *runner {
	t.Helper()

	r := &runner{
		newSource: func(binding) (watchSource, error) {
			return &fixedSource{snapshot: printingJob("printer-1", 40)}, nil
		},
		newLight: func(address string) (strip, error) { return wled.New(address) },
		logf:     quiet,
		tick:     5 * time.Millisecond,
		tables:   tables,
	}

	r.setPermits(declaration.declares)

	return r
}

// TestAnUndeclaredAddressIsNeverContacted is the cap's whole point. The store holds a binding and a
// light row at a live controller's address, and the settings declare a different one. The runner
// must not build a client for the store's address at all: the recording controller stays at zero
// requests through a watch, a start's fill and a read of the binding list, and the binding is
// reported as refused rather than silently dropped.
//
// This is the test that fails against the code before the cap: with the permits removed — or with an
// empty declaration mistaken for "allow everything" — the watch dials the controller within one
// tick.
func TestAnUndeclaredAddressIsNeverContacted(t *testing.T) {
	server, hits := recordingController(t)

	tables := storeNaming(t, server.URL)

	declaration := Settings{Light1Address: "http://192.0.2.10"}
	r := runningAt(t, declaration, tables)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	set := bindings{{
		ID: "binding-1", DeviceID: "printer-1", Address: server.URL, Segment: -1, Brightness: 128,
	}}

	if err := r.apply(ctx, set); err != nil {
		t.Fatalf("apply the fixture's set: %v", err)
	}

	// Give a watch that should not exist every chance to make a request.
	time.Sleep(80 * time.Millisecond)

	if got := hits.Load(); got != 0 {
		t.Fatalf("a controller at an undeclared address was contacted %d times, want zero: the declaration is the whole address set", got)
	}

	if driving := r.set(); len(driving) != 0 {
		t.Fatalf("the running set holds %d bindings at an undeclared address, want none", len(driving))
	}

	refused := r.refused()
	if len(refused) != 1 || !strings.Contains(refusedReason(refused[0].Address), "light1_address") {
		t.Fatalf("the refused set is %+v, want the one binding with the sentence naming light1_address", refused)
	}

	// And a start's own read of the lights obeys the same declaration.
	tablesAgain := storeNaming(t, server.URL)
	fillLightFacts(ctx, tablesAgain, func(address string) (lightProber, error) { return wled.New(address) },
		declaration.DeclaredLights(), quiet)

	if got := hits.Load(); got != 0 {
		t.Fatalf("a start probed an undeclared address %d times, want zero", got)
	}
}

// TestAFilledSlotIsDriven is the other direction, because a cap that refuses everything would pass
// the test above and be useless. The same store, the same controller, and the option names its
// address: the writer reads the light and paints it.
func TestAFilledSlotIsDriven(t *testing.T) {
	hits, address := controllerOnPort80(t, "127.0.0.1:80")

	tables := storeNaming(t, address)

	declaration := Settings{Light1Address: address}
	r := runningAt(t, declaration, tables)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	set := bindings{{
		ID: "binding-1", DeviceID: "printer-1", Address: address, Segment: -1, Brightness: 128,
	}}

	if err := r.apply(ctx, set); err != nil {
		t.Fatalf("apply the fixture's set: %v", err)
	}

	eventually(t, 2*time.Second, func() string { return "no request reached the declared controller" }, func() bool {
		return hits.Load() > 0
	})
}

// TestNoFilledSlotDrivesNothingAndShowsFiveEmptySlots is the fresh install, and the screen a person
// meets it on. Nothing is driven; GET /lights answers the five slots, none filled, each saying where
// it is filled in.
func TestNoFilledSlotDrivesNothingAndShowsFiveEmptySlots(t *testing.T) {
	server, hits := recordingController(t)

	tables := storeNaming(t, server.URL)

	declaration := Settings{}
	r := runningAt(t, declaration, tables)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	set := bindings{{
		ID: "binding-1", DeviceID: "printer-1", Address: server.URL, Segment: -1, Brightness: 128,
	}}

	if err := r.apply(ctx, set); err != nil {
		t.Fatalf("apply the fixture's set: %v", err)
	}

	time.Sleep(50 * time.Millisecond)

	if got := hits.Load(); got != 0 {
		t.Fatalf("with every option empty the module contacted a controller %d times, want zero", got)
	}

	// No kept host answer survives from another fixture: see forgetSeen.
	forgetSeen()

	mux := http.NewServeMux()
	lightsAPI{tables: tables, slots: declaration.DeclaredLights, permits: declaration.declares}.mount(mux)

	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, forwarded(http.MethodGet, "/lights", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /lights answered %d, want 200: %s", recorder.Code, recorder.Body.String())
	}

	var body struct {
		Slots     []lightSlot `json:"slots"`
		SlotCount int         `json:"slot_count"`
		Lights    []any       `json:"lights"`
		Note      string      `json:"note"`
	}

	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("GET /lights answered something that is not JSON: %v", err)
	}

	if len(body.Slots) != declaredLightSlots || body.SlotCount != declaredLightSlots {
		t.Fatalf("the screen is drawn from %d slots (count %d), want the five the manifest declares", len(body.Slots), body.SlotCount)
	}

	for i, slot := range body.Slots {
		if slot.Filled || slot.Address != "" || slot.Slot != i+1 {
			t.Fatalf("slot %d reads %+v, want an empty slot numbered %d", i+1, slot, i+1)
		}

		if !strings.Contains(slot.Note, fmt.Sprintf("light%d_address", slot.Slot)) {
			t.Fatalf("an empty slot's note reads %q, want the option a person has to fill in", slot.Note)
		}
	}

	if len(body.Lights) != 0 {
		t.Fatalf("the list holds %d lights while no option is filled, want none", len(body.Lights))
	}

	if !strings.Contains(body.Note, "0.8.0") || !strings.Contains(body.Note, "light1_address") {
		t.Fatalf("the empty-state note reads %q, want the migration sentence and the option to fill in", body.Note)
	}
}

// TestASlotNotInTheOptionsIsRefusedAndNothingIsWritten is the editor's half of the rule: a person
// may bind a printer to one of the five declared addresses, and a request naming any other address
// is refused in words before the store is touched.
func TestASlotNotInTheOptionsIsRefusedAndNothingIsWritten(t *testing.T) {
	withHost(t, allowing{})

	// A live controller at an address the rule admits but the settings do not name: port 80 on the
	// loopback. It is reachable, so "the refused binding never reached it" is a fact about a socket.
	hits, address := controllerOnPort80(t, "127.0.0.1:80")

	dir := t.TempDir()

	if _, err := saveStore(dir, storedConfig{Version: storeVersion}); err != nil {
		t.Fatalf("write the fixture's store: %v", err)
	}

	tables := &configTables{}
	tables.grant(dir)

	declaration := Settings{Light1Address: "http://192.0.2.10"}
	r := runningAt(t, declaration, tables)

	// No kept host answer survives from another fixture: see forgetSeen.
	forgetSeen()

	mux := http.NewServeMux()
	bindingAPI{
		live:    r,
		fleet:   func() devices { return nil },
		parent:  func() context.Context { return context.Background() },
		tables:  tables,
		permits: declaration.declares,
	}.mount(mux)

	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, forwarded(http.MethodPost, "/bindings",
		strings.NewReader(`{"printer":"printer-1","controller":"`+address+`"}`)))

	if recorder.Code != http.StatusConflict {
		t.Fatalf("a binding at an undeclared address answered %d, want 409: %s", recorder.Code, recorder.Body.String())
	}

	if !strings.Contains(recorder.Body.String(), "light1_address") {
		t.Fatalf("the refusal reads %q, want the sentence naming the five settings", recorder.Body.String())
	}

	if got := hits.Load(); got != 0 {
		t.Fatalf("an undeclared address was contacted %d times by a refused binding, want zero", got)
	}

	config, err := tables.load()
	if err != nil {
		t.Fatalf("read the store back: %v", err)
	}

	if len(config.Bindings) != 0 {
		t.Fatalf("the store holds %d bindings after a refused create, want none", len(config.Bindings))
	}
}

// TestAViewerIsStillRefusedOnAMutatingRoute is the 0.8.0 access rule held beside the new cap. One
// mutating route is named explicitly so that this fails by name if the guard is ever dropped from
// it, rather than being carried only by the table-driven test.
func TestAViewerIsStillRefusedOnAMutatingRoute(t *testing.T) {
	withHost(t, theLadder())

	recorder := submit(t, requestAs(t, "user:viewer", http.MethodPost, "/lights/10.0.0.253/ignore"))

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("a viewer's ignore answered %d, want 403: %s", recorder.Code, recorder.Body.String())
	}

	raised := refusalOf(t, recorder)
	if !strings.HasSuffix(raised.Code, "NOT_PERMITTED") {
		t.Fatalf("a viewer's ignore was refused with %q, want NOT_PERMITTED", raised.Code)
	}
}
