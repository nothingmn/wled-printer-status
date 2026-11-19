package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nothingmn/wled-printer-status/internal/wled"
	"github.com/prusa3d/prusa-connect/sdk/deviceapi"
)

// apiUnderTest is the editor's routes over a watch built on fakes and a printer list that needs
// no host: the same handlers the process serves, with the two things they reach replaced.
type apiUnderTest struct {
	mux   http.Handler
	watch *runner
	light *fakeStrip

	// madeSources counts the printers a watch was built for, which is how a test sees that a
	// change started a watch at all rather than only changing a list.
	madeSources *atomic.Int32
}

// newAPIUnderTest builds the routes, a strip standing in for the controller, and a fleet of the
// printers named. Each binding gets its own source, so a watch holding two of them runs two
// readers and two writers.
func newAPIUnderTest(t *testing.T, light *fakeStrip, printers []deviceapi.Device, start bindings) *apiUnderTest {
	t.Helper()

	return newAPIWith(t, light, printers, start, func(string) (strip, error) { return light, nil })
}

// newAPIWith is newAPIUnderTest with the client GET /controllers/{name} builds left to the caller,
// which is how a test reads segments through a real wled client against a server that answers a
// real /json/state body rather than as a wled.State handed over already parsed.
func newAPIWith(t *testing.T, light *fakeStrip, printers []deviceapi.Device, start bindings, newLight func(string) (strip, error)) *apiUnderTest {
	t.Helper()

	ctx, stop := context.WithCancel(context.Background())
	t.Cleanup(stop)

	made := &atomic.Int32{}

	watch := &runner{
		newSource: func(b binding) (watchSource, error) {
			made.Add(1)

			return &fixedSource{snapshot: printingJob(b.DeviceID, 50)}, nil
		},
		newLight: func(string) (strip, error) { return light, nil },
		logf:     quiet,
		tick:     5 * time.Millisecond,
		grace:    2 * time.Second,
	}

	if err := watch.apply(ctx, start); err != nil {
		t.Fatalf("the watch did not start: %v", err)
	}

	t.Cleanup(func() {
		watch.stop()
		forgetBindings()
		forgetPreview()
	})

	page := deviceapi.Page{Items: printers, Total: len(printers)}
	fleet := fakeFleet{page: page}

	mux := http.NewServeMux()
	bindingAPI{
		live:     watch,
		fleet:    func() devices { return fleet },
		parent:   func() context.Context { return ctx },
		newLight: newLight,
		bound:    time.Second,
	}.mount(mux)

	return &apiUnderTest{mux: mux, watch: watch, light: light, madeSources: made}
}

// do runs one request and answers with the status, the body as a map, and the raw JSON.
func (a *apiUnderTest) do(t *testing.T, method, path, body string) (int, map[string]any) {
	t.Helper()

	recorder := httptest.NewRecorder()
	a.mux.ServeHTTP(recorder, httptest.NewRequest(method, path, strings.NewReader(body)))

	var answer map[string]any
	if raw := recorder.Body.Bytes(); len(raw) > 0 {
		if err := json.Unmarshal(raw, &answer); err != nil {
			t.Fatalf("%s %s answered something that is not JSON: %v (%s)", method, path, err, raw)
		}
	}

	return recorder.Code, answer
}

// code is the registered code one refusal carries, which is what a caller branches on.
func code(t *testing.T, answer map[string]any) string {
	t.Helper()

	found, _ := answer["code"].(string)

	return found
}

// raw is one request's answer as the bytes it arrived in, which is where a test sees that a
// member is present rather than only that it decoded to a zero.
func (a *apiUnderTest) raw(t *testing.T, method, path string) (int, string) {
	t.Helper()

	recorder := httptest.NewRecorder()
	a.mux.ServeHTTP(recorder, httptest.NewRequest(method, path, nil))

	return recorder.Code, recorder.Body.String()
}

// testPrinters is the fleet every test below reads: two machines to show and one to leave
// alone.
var testPrinters = []deviceapi.Device{
	{ID: "printer-a", Name: "Hermes MK4", Connected: true},
	{ID: "printer-b", Name: "Mini", Connected: true},
	{ID: "printer-c", Name: "Core One", Connected: false},
}

// TestReadingTheSetIsReadingWhatTheWriterRuns is the read half of the editor: the list, and one
// binding by the printer it shows.
func TestReadingTheSetIsReadingWhatTheWriterRuns(t *testing.T) {
	light := newFakeStrip()
	api := newAPIUnderTest(t, light, testPrinters, bindings{
		{DeviceID: "printer-a", Address: "http://10.0.0.136", Segment: wholeStrip, Brightness: 128},
		{DeviceID: "printer-b", Address: "http://10.0.0.253", Segment: 2},
	})

	status, body := api.do(t, http.MethodGet, "/bindings", "")
	if status != http.StatusOK {
		t.Fatalf("GET /bindings answered %d, want %d: %v", status, http.StatusOK, body)
	}

	if count, _ := body["count"].(float64); count != 2 {
		t.Fatalf("the list carries %v bindings, want 2", body["count"])
	}

	// The name is the host's, and reading it is why the list is readable at all: nobody
	// matches a device id against a list of device ids.
	first := body["bindings"].([]any)[0].(map[string]any)
	if first["printer_name"] != "Hermes MK4" {
		t.Fatalf("the first binding's printer is named %v, want Hermes MK4", first["printer_name"])
	}

	if first["whole"] != true || first["segment"].(float64) != -1 {
		t.Fatalf("the first binding is %v on segment %v, want the whole controller", first["whole"], first["segment"])
	}

	// Nothing here can save anything, and the answer has to say so: a caller who thinks a
	// change here survives a restart will lose two printers at the next deploy.
	if body["persisted"] != false {
		t.Fatalf("the list says persisted %v, and nothing on this surface writes the host's settings", body["persisted"])
	}

	status, one := api.do(t, http.MethodGet, "/bindings/printer-b", "")
	if status != http.StatusOK {
		t.Fatalf("GET /bindings/printer-b answered %d, want %d: %v", status, http.StatusOK, one)
	}

	bindingOfOne := one["binding"].(map[string]any)
	if bindingOfOne["controller"] != "http://10.0.0.253" || bindingOfOne["segment"].(float64) != 2 {
		t.Fatalf("the binding reads as %v, want printer-b on segment 2 of the strip", bindingOfOne)
	}

	if bindingOfOne["position"].(float64) != 2 {
		t.Fatalf("the binding sits at position %v, want 2", bindingOfOne["position"])
	}

	status, missing := api.do(t, http.MethodGet, "/bindings/printer-c", "")
	if status != http.StatusNotFound {
		t.Fatalf("a printer this module does not drive answered %d, want %d", status, http.StatusNotFound)
	}

	if got := code(t, missing); got != BindingNotFound.Code().String() {
		t.Fatalf("the refusal carries %q, want %q", got, BindingNotFound.Code().String())
	}
}

// TestAddingABindingIsDrivingItAtOnce is the whole point of the routes: a second printer starts
// being shown without a restart, on the controller it named, on the segment it named.
func TestAddingABindingIsDrivingItAtOnce(t *testing.T) {
	light := newFakeStrip()
	light.state = wledStateForSegments(3, 4)

	api := newAPIUnderTest(t, light, testPrinters, bindings{
		{DeviceID: "printer-a", Address: "http://10.0.0.136", Segment: wholeStrip, Brightness: 128},
	})

	if len(light.shown()) == 0 {
		eventually(t, 3*time.Second, func() string { return "nothing written" }, func() bool {
			return len(light.shown()) > 0
		})
	}

	// One watch runs the whole set, so a change stops it and builds every binding again: the
	// count after the change is the count before it plus one per binding in the new set.
	readersBefore := api.madeSources.Load()

	status, body := api.do(t, http.MethodPost, "/bindings",
		`{"printer": "Mini", "controller": "http://10.0.0.253", "segment": 3}`)
	if status != http.StatusCreated {
		t.Fatalf("POST /bindings answered %d, want %d: %v", status, http.StatusCreated, body)
	}

	if body["applied"] != true || body["persisted"] != false {
		t.Fatalf("the answer says applied %v and persisted %v, want true and false", body["applied"], body["persisted"])
	}

	added := body["binding"].(map[string]any)
	if added["id"] != "printer-b" || added["segment"].(float64) != 3 {
		t.Fatalf("the binding added is %v, want printer-b on segment 3", added)
	}

	// The writer for the new binding is running, which is what "at once" has to mean: a second
	// source was built, and a body addressed to segment 3 reaches the controller.
	eventually(t, 3*time.Second, func() string { return segmentsPhrase(light.shown()) }, func() bool {
		for _, call := range light.shown() {
			if len(call.segments) == 1 && call.segments[0].ID == 3 {
				return true
			}
		}

		return false
	})

	if made := api.madeSources.Load() - readersBefore; made != 2 {
		t.Fatalf("the change built %d printer sources, want 2: one for each binding in the set it started", made)
	}
}

// TestAddingABindingRefusesWhatTheRulesRefuse is the refusal half, and the promise the refusal
// makes: the set that was running is still running afterwards.
func TestAddingABindingRefusesWhatTheRulesRefuse(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "a segment the running binding drives whole",
			body: `{"printer": "Hermes MK4", "controller": "http://10.0.0.136", "segment": 7}`,
			want: "a whole controller leaves no segment for anything else",
		},
		{
			name: "a printer the host does not hold",
			body: `{"printer": "Ghost", "controller": "http://10.0.0.136"}`,
			want: "no printer in this instance is called",
		},
		{
			name: "a controller that is not one",
			body: `{"printer": "Mini", "controller": "10.0.0.253"}`,
			want: "needs an http or https scheme",
		},
		{
			name: "a brightness on a borrowed segment",
			body: `{"printer": "Mini", "controller": "http://10.0.0.253", "segment": 2, "brightness": 64}`,
			want: "a brightness is controller-wide",
		},
		{
			name: "a body with no printer",
			body: `{"controller": "http://10.0.0.253"}`,
			want: "no printer",
		},
		{
			name: "a misspelt key, which is refused and not ignored",
			body: `{"printer": "Mini", "controler": "http://10.0.0.253"}`,
			want: "not one binding",
		},
	}

	for _, one := range cases {
		t.Run(one.name, func(t *testing.T) {
			light := newFakeStrip()
			api := newAPIUnderTest(t, light, testPrinters, bindings{
				{DeviceID: "printer-a", Address: "http://10.0.0.136", Segment: wholeStrip, Brightness: 128},
			})

			status, body := api.do(t, http.MethodPost, "/bindings", one.body)
			if status != http.StatusConflict && status != http.StatusBadRequest {
				t.Fatalf("the refusal answered %d, want 409 or 400: %v", status, body)
			}

			if detail, _ := body["detail"].(string); !strings.Contains(detail, one.want) {
				t.Fatalf("the refusal reads %q, want it to contain %q", detail, one.want)
			}

			if got := api.watch.set(); len(got) != 1 || got[0].DeviceID != "printer-a" {
				t.Fatalf("the set is now %+v, and a refused change must leave the set that was running running", got)
			}
		})
	}
}

// TestChangingOneBindingLeavesTheOtherAlone is the edit half: one printer moves to another
// controller, and the writer for the other keeps its own light.
func TestChangingOneBindingLeavesTheOtherAlone(t *testing.T) {
	light := newFakeStrip()
	light.state = wledStateForSegments(5)

	api := newAPIUnderTest(t, light, testPrinters, bindings{
		{DeviceID: "printer-a", Address: "http://10.0.0.136", Segment: wholeStrip, Brightness: 128},
		{DeviceID: "printer-b", Address: "http://10.0.0.253", Segment: 2},
	})

	status, body := api.do(t, http.MethodPut, "/bindings/printer-b",
		`{"controller": "http://10.0.0.253", "segment": 5}`)
	if status != http.StatusOK {
		t.Fatalf("PUT /bindings/printer-b answered %d, want %d: %v", status, http.StatusOK, body)
	}

	set := api.watch.set()
	if len(set) != 2 {
		t.Fatalf("the set holds %d bindings, want 2", len(set))
	}

	if set[1].DeviceID != "printer-b" || set[1].Address != "http://10.0.0.253" || set[1].Segment != 5 {
		t.Fatalf("the changed binding reads %+v, want printer-b on segment 5 of the strip", set[1])
	}

	if set[0].DeviceID != "printer-a" || set[0].Segment != wholeStrip {
		t.Fatalf("the other binding reads %+v, and an edit of one must not move the other", set[0])
	}

	// A printer is the binding's name, so an edit naming a different one is refused rather than
	// quietly moving what the id refers to.
	status, body = api.do(t, http.MethodPut, "/bindings/printer-b",
		`{"printer": "Hermes MK4", "controller": "http://10.0.0.136"}`)
	if status != http.StatusConflict {
		t.Fatalf("an edit naming another printer answered %d, want %d: %v", status, http.StatusConflict, body)
	}
}

// TestDeletingABindingStopsItsWriterWhileTheOthersRun is the case a restart would hide: a
// binding goes while the watch is running, its writer gives its borrowed segment back, and the
// printer beside it is still being drawn to afterwards.
func TestDeletingABindingStopsItsWriterWhileTheOthersRun(t *testing.T) {
	forgetBindings()

	light := newFakeStrip()
	light.state = wledStateForSegments(2, 5)

	api := newAPIUnderTest(t, light, testPrinters, bindings{
		{DeviceID: "printer-a", Address: "http://10.0.0.136", Segment: 2},
		{DeviceID: "printer-b", Address: "http://10.0.0.136", Segment: 5},
	})

	// Both writers are running before anything is deleted, or the test would prove nothing
	// about deleting one of two.
	eventually(t, 3*time.Second, func() string { return segmentsPhrase(light.shown()) }, func() bool {
		seen := map[int]bool{}

		for _, call := range light.shown() {
			if len(call.segments) == 1 {
				seen[call.segments[0].ID] = true
			}
		}

		return seen[2] && seen[5]
	})

	before := len(light.shown())

	status, body := api.do(t, http.MethodDelete, "/bindings/printer-a", "")
	if status != http.StatusOK {
		t.Fatalf("DELETE /bindings/printer-a answered %d, want %d: %v", status, http.StatusOK, body)
	}

	if body["applied"] != true || body["driving"] != true {
		t.Fatalf("the answer says applied %v and driving %v, want true and true", body["applied"], body["driving"])
	}

	if set := api.watch.set(); len(set) != 1 || set[0].DeviceID != "printer-b" {
		t.Fatalf("the set is %+v, want printer-b alone", set)
	}

	// The segment it borrowed is on the controller it came from, put back as it was found.
	//
	// Segment 5 is in that list too, and that is the honest cost of one watch: a change stops
	// every writer and starts them again, so a borrowed segment beside the deleted one is given
	// back and borrowed again — one flicker of the module's own frame, not a binding that was
	// disturbed.
	given := map[int]bool{}
	for _, one := range light.givenBack() {
		given[one.ID] = true
	}

	if !given[2] {
		t.Fatalf("the segments given back are %v, want segment 2 among them: a deleted binding gives its segment back", given)
	}

	// And the one beside it is still being drawn to, on the same controller.
	eventually(t, 3*time.Second, func() string { return segmentsPhrase(light.shown()) }, func() bool {
		if len(light.shown()) <= before {
			return false
		}

		for _, call := range light.shown()[before:] {
			if len(call.segments) == 1 && call.segments[0].ID == 2 {
				t.Fatalf("segment 2 was written to after the binding that borrowed it was deleted")
			}
		}

		return true
	})

	// Nothing answers for the deleted binding any more, on either route.
	status, missing := api.do(t, http.MethodGet, "/bindings/printer-a", "")
	if status != http.StatusNotFound {
		t.Fatalf("the deleted binding still reads %d, want %d", status, http.StatusNotFound)
	}

	if got := code(t, missing); got != BindingNotFound.Code().String() {
		t.Fatalf("the refusal carries %q, want %q", got, BindingNotFound.Code().String())
	}

	status, again := api.do(t, http.MethodDelete, "/bindings/printer-a", "")
	if status != http.StatusNotFound {
		t.Fatalf("deleting the same binding twice answered %d, want %d: %v", status, http.StatusNotFound, again)
	}
}

// TestTheLastBindingCanBeDeletedAndTheLightIsLeftAlone is the shape an operator may actually
// want: stop showing anything without removing a setting and restarting the host.
func TestTheLastBindingCanBeDeletedAndTheLightIsLeftAlone(t *testing.T) {
	light := newFakeStrip()

	api := newAPIUnderTest(t, light, testPrinters, bindings{
		{DeviceID: "printer-a", Address: "http://10.0.0.136", Segment: wholeStrip, Brightness: 128},
	})

	status, body := api.do(t, http.MethodDelete, "/bindings/printer-a", "")
	if status != http.StatusOK {
		t.Fatalf("DELETE the last binding answered %d, want %d: %v", status, http.StatusOK, body)
	}

	if body["driving"] != false {
		t.Fatalf("the answer says driving %v, want false", body["driving"])
	}

	if set := api.watch.set(); len(set) != 0 {
		t.Fatalf("the set is %+v, want nothing: the last binding was deleted", set)
	}

	// Nothing is written afterwards: the light is left as the last frame found it.
	settled := len(light.shown())

	time.Sleep(50 * time.Millisecond)

	if now := len(light.shown()); now != settled {
		t.Fatalf("%d writes arrived after the last binding was deleted, want none", now-settled)
	}
}

// TestThePrinterListIsWhereADeviceIdComesFrom answers the question a person asks in front of a
// settings field: where would someone get that GUID. Nobody types one; this is the list, and it
// says which printers are already shown so that a choice can leave them out.
func TestThePrinterListIsWhereADeviceIdComesFrom(t *testing.T) {
	light := newFakeStrip()

	api := newAPIUnderTest(t, light, testPrinters, bindings{
		{DeviceID: "printer-a", Address: "http://10.0.0.136", Segment: wholeStrip, Brightness: 128},
	})

	status, body := api.do(t, http.MethodGet, "/printers", "")
	if status != http.StatusOK {
		t.Fatalf("GET /printers answered %d, want %d: %v", status, http.StatusOK, body)
	}

	if count, _ := body["count"].(float64); count != 3 {
		t.Fatalf("the list carries %v printers, want 3", body["count"])
	}

	shown := body["printers"].([]any)[0].(map[string]any)
	if shown["id"] != "printer-a" || shown["name"] != "Hermes MK4" {
		t.Fatalf("the first printer reads %v, want printer-a named Hermes MK4", shown)
	}

	bound, ok := shown["bound"].(map[string]any)
	if !ok {
		t.Fatalf("the printer this module is showing carries no binding: %v", shown)
	}

	if bound["controller"] != "http://10.0.0.136" || bound["whole"] != true {
		t.Fatalf("its binding reads %v, want the whole of the panel", bound)
	}

	// A printer nothing is shown on has no bound member at all, which is what a picker reads to
	// offer it.
	for _, raw := range body["printers"].([]any) {
		one := raw.(map[string]any)
		if one["id"] == "printer-b" {
			if _, offered := one["bound"]; offered {
				t.Fatalf("printer-b is not being shown, and the list says it is: %v", one)
			}
		}
	}
}

// TestAPrinterListThatCannotBeReadIsARefusal is the honest answer when the host will not say
// what printers it holds: a 503 with a code, not an empty list that reads like a host with no
// printers on it.
func TestAPrinterListThatCannotBeReadIsARefusal(t *testing.T) {
	mux := http.NewServeMux()
	bindingAPI{
		live:  &runner{logf: quiet},
		fleet: func() devices { return nil },
		bound: time.Second,
	}.mount(mux)

	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/printers", nil))

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET /printers with no host answered %d, want %d", recorder.Code, http.StatusServiceUnavailable)
	}

	var answer map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &answer); err != nil {
		t.Fatalf("the refusal is not JSON: %v", err)
	}

	if got := code(t, answer); got != PrintersUnavailable.Code().String() {
		t.Fatalf("the refusal carries %q, want %q", got, PrintersUnavailable.Code().String())
	}
}

// TestTheControllerListIsTheSetGrouped is the read-only side of the controllers: every
// controller the running set drives, with the segments it gives up and the printers on it.
func TestTheControllerListIsTheSetGrouped(t *testing.T) {
	light := newFakeStrip()

	api := newAPIUnderTest(t, light, testPrinters, bindings{
		{DeviceID: "printer-a", Address: "http://10.0.0.136", Segment: 2},
		{DeviceID: "printer-b", Address: "http://10.0.0.253", Segment: wholeStrip, Brightness: 128},
		{DeviceID: "printer-c", Address: "http://10.0.0.136/", Segment: 5},
	})

	status, body := api.do(t, http.MethodGet, "/controllers", "")
	if status != http.StatusOK {
		t.Fatalf("GET /controllers answered %d, want %d: %v", status, http.StatusOK, body)
	}

	if count, _ := body["count"].(float64); count != 2 {
		t.Fatalf("the set drives %v controllers, want 2: two spellings of one address are one controller", body["count"])
	}

	// Nothing here asks a controller which segments it holds, and the answer must not pretend
	// otherwise.
	if body["segments_known"] != false {
		t.Fatalf("the list says segments_known %v, and nothing here reads a controller", body["segments_known"])
	}

	panel := body["controllers"].([]any)[0].(map[string]any)
	if panel["name"] != "10.0.0.136" || panel["key"] != "http://10.0.0.136" {
		t.Fatalf("the first controller reads %v, want name 10.0.0.136 and key http://10.0.0.136", panel)
	}

	segments := panel["segments"].([]any)
	if len(segments) != 2 || segments[0].(float64) != 2 || segments[1].(float64) != 5 {
		t.Fatalf("the panel's borrowed segments are %v, want 2 then 5 in the operator's order", segments)
	}

	status, one := api.do(t, http.MethodGet, "/controllers/10.0.0.253", "")
	if status != http.StatusOK {
		t.Fatalf("GET /controllers/10.0.0.253 answered %d, want %d: %v", status, http.StatusOK, one)
	}

	stripController := one["controller"].(map[string]any)
	if stripController["whole"] != "printer-b" {
		t.Fatalf("the strip's whole-controller binding reads %v, want printer-b", stripController["whole"])
	}

	status, missing := api.do(t, http.MethodGet, "/controllers/192.168.1.9", "")
	if status != http.StatusNotFound {
		t.Fatalf("a controller this module does not drive answered %d, want %d", status, http.StatusNotFound)
	}

	if got := code(t, missing); got != ControllerNotFound.Code().String() {
		t.Fatalf("the refusal carries %q, want %q", got, ControllerNotFound.Code().String())
	}
}

// wledStateForSegments is a controller that holds the segments named, which is what a borrow
// reads and what the give-back writes into.
func wledStateForSegments(ids ...int) wled.State {
	state := wled.State{}

	for i, id := range ids {
		state.Segments = append(state.Segments, wled.Segment{
			ID: id, Start: i * 10, Stop: i*10 + 8, Name: "borrowed", On: true,
		})
	}

	return state
}

// panelState is a real /json/state body from a 256×64 panel: two segments side by side, each
// named, each carrying all four of its bounds. It is the document the controller actually
// publishes — the keys are the ones AGENTS.md §8 lists for this firmware — because the point of
// this route is to read the light's own answer and not a shape this repository invented.
const panelState = `{
  "on": true,
  "bri": 128,
  "transition": 7,
  "seg": [
    {"id": 0, "start": 0, "stop": 128, "startY": 0, "stopY": 64, "on": true,
     "col": [[0, 170, 0], [0, 0, 0], [0, 0, 0]], "fx": 0, "n": "left"},
    {"id": 1, "start": 128, "stop": 256, "startY": 0, "stopY": 64, "on": true,
     "col": [[0, 0, 0]], "fx": 0, "n": "right"}
  ]
}`

// stripState is a real /json/state body from a 48-LED strip: one segment, a run of pixels, and no
// rows at all, because a strip has no second dimension to have them in.
const stripState = `{
  "on": true,
  "bri": 200,
  "transition": 7,
  "seg": [
    {"id": 0, "start": 0, "stop": 48, "on": true, "col": [[0, 0, 255]], "fx": 0, "n": "desk"}
  ]
}`

// segmentBody is one segment of an answer, for the comparisons below.
type segmentBody struct {
	id     int
	name   string
	start  int
	stop   int
	startY int
	stopY  int
}

// newSegmentsUnderTest builds the routes with the controller answered by a real wled client
// pointed at a server that serves body, so the segments a caller reads were parsed by
// internal/wled from the document rather than handed over as a wled.State.
func newSegmentsUnderTest(t *testing.T, body string) *apiUnderTest {
	t.Helper()

	controller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/json/state" {
			http.NotFound(w, r)

			return
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))

	t.Cleanup(controller.Close)

	light := newFakeStrip()

	api := newAPIWith(t, light, testPrinters, bindings{
		{DeviceID: "printer-a", Address: "http://10.0.0.136", Segment: 2},
	}, func(string) (strip, error) { return wled.New(controller.URL) })

	return api
}

// TestTheControllerRouteAnswersTheLightsOwnSegments is item one of the backend list: the segments
// a light holds, with the id, the name and the bounds of each, because every segment picker is
// empty without them.
//
// The bodies are the controllers' own /json/state documents, read through the real client, so what
// is asserted is what a light says rather than a shape this module hoped for.
func TestTheControllerRouteAnswersTheLightsOwnSegments(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		segments []segmentBody
	}{
		{
			name: "a panel, whose segments are rectangles",
			body: panelState,
			segments: []segmentBody{
				{id: 0, name: "left", start: 0, stop: 128, startY: 0, stopY: 64},
				{id: 1, name: "right", start: 128, stop: 256, startY: 0, stopY: 64},
			},
		},
		{
			name: "a strip, which has no rows",
			body: stripState,
			segments: []segmentBody{
				{id: 0, name: "desk", start: 0, stop: 48, startY: 0, stopY: 0},
			},
		},
	}

	for _, one := range cases {
		t.Run(one.name, func(t *testing.T) {
			api := newSegmentsUnderTest(t, one.body)

			status, raw := api.raw(t, http.MethodGet, "/controllers/10.0.0.136")
			if status != http.StatusOK {
				t.Fatalf("GET /controllers/10.0.0.136 answered %d, want %d: %s", status, http.StatusOK, raw)
			}

			var body struct {
				Controller struct {
					Name     string `json:"name"`
					Segments []int  `json:"segments"`
				} `json:"controller"`
				SegmentsKnown bool `json:"segments_known"`
				Segments      []struct {
					ID     *int    `json:"id"`
					Name   *string `json:"name"`
					Start  *int    `json:"start"`
					Stop   *int    `json:"stop"`
					StartY *int    `json:"startY"`
					StopY  *int    `json:"stopY"`
				} `json:"segments"`
			}

			if err := json.Unmarshal([]byte(raw), &body); err != nil {
				t.Fatalf("the answer is not JSON: %v (%s)", err, raw)
			}

			if !body.SegmentsKnown {
				t.Fatalf("the answer says the segments are not known, and the controller answered: %s", raw)
			}

			// The controller entry is still the record of what this module has taken, which is a
			// different fact from what the light holds.
			if body.Controller.Name != "10.0.0.136" {
				t.Fatalf("the controller reads as %q, want 10.0.0.136", body.Controller.Name)
			}

			if len(body.Controller.Segments) != 1 || body.Controller.Segments[0] != 2 {
				t.Fatalf("the borrowed segments are %v, want [2]", body.Controller.Segments)
			}

			if len(body.Segments) != len(one.segments) {
				t.Fatalf("the answer carries %d segments, want %d: %s", len(body.Segments), len(one.segments), raw)
			}

			for i, want := range one.segments {
				got := body.Segments[i]

				// Every bound is present, including the two a 1D light leaves at zero: a
				// segment whose rows were left out reads as row zero on a panel, which is the
				// collapse this shape exists to prevent.
				if got.ID == nil || got.Name == nil || got.Start == nil || got.Stop == nil ||
					got.StartY == nil || got.StopY == nil {
					t.Fatalf("segment %d is half-specified: %s", i, raw)
				}

				if *got.ID != want.id || *got.Name != want.name ||
					*got.Start != want.start || *got.Stop != want.stop ||
					*got.StartY != want.startY || *got.StopY != want.stopY {
					t.Fatalf("segment %d reads {%d %q %d %d %d %d}, want {%d %q %d %d %d %d}",
						i, *got.ID, *got.Name, *got.Start, *got.Stop, *got.StartY, *got.StopY,
						want.id, want.name, want.start, want.stop, want.startY, want.stopY)
				}
			}
		})
	}
}

// TestAControllerThatHoldsNoSegmentsIsAnAnswerAndNotAnAbsence is the difference between "this
// light has none" and "nobody asked it": a controller that answered with an empty list has been
// read, and the answer says so.
func TestAControllerThatHoldsNoSegmentsIsAnAnswerAndNotAnAbsence(t *testing.T) {
	api := newSegmentsUnderTest(t, `{"on": false, "bri": 0, "seg": []}`)

	status, raw := api.raw(t, http.MethodGet, "/controllers/10.0.0.136")
	if status != http.StatusOK {
		t.Fatalf("GET /controllers/10.0.0.136 answered %d, want %d: %s", status, http.StatusOK, raw)
	}

	var body struct {
		SegmentsKnown  bool             `json:"segments_known"`
		Segments       []map[string]any `json:"segments"`
		SegmentsUnread string           `json:"segments_unread"`
	}

	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		t.Fatalf("the answer is not JSON: %v (%s)", err, raw)
	}

	if !body.SegmentsKnown {
		t.Fatalf("a controller that answered with no segments is reported as unread: %s", raw)
	}

	if len(body.Segments) != 0 {
		t.Fatalf("the answer carries %d segments, want none: %s", len(body.Segments), raw)
	}

	// An empty list, not a null: a picker iterating it should find nothing rather than crash.
	if !strings.Contains(raw, `"segments":[]`) {
		t.Fatalf("the answer does not carry an empty list: %s", raw)
	}

	if body.SegmentsUnread != "" {
		t.Fatalf("the answer gives a reason %q for segments it read", body.SegmentsUnread)
	}
}

// TestAControllerThatDoesNotAnswerStillAnswersWithItsBindings is the honest degradation: a light
// that is unplugged is not a 500 and not a panic, and it is not an empty segment list presented as
// the light's answer either. The bindings on that address are still true, so the route still
// carries them and says why the segments are missing.
func TestAControllerThatDoesNotAnswerStillAnswersWithItsBindings(t *testing.T) {
	controller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))

	// Closed before the route is called, so the read fails the way an unplugged controller's
	// does: a refused connection, not a slow one.
	controller.Close()

	light := newFakeStrip()

	api := newAPIWith(t, light, testPrinters, bindings{
		{DeviceID: "printer-a", Address: "http://10.0.0.136", Segment: 2},
	}, func(string) (strip, error) { return wled.New(controller.URL) })

	status, raw := api.raw(t, http.MethodGet, "/controllers/10.0.0.136")
	if status != http.StatusOK {
		t.Fatalf("a controller that does not answer refused the route with %d: %s", status, raw)
	}

	var body struct {
		Controller struct {
			Name     string `json:"name"`
			Segments []int  `json:"segments"`
		} `json:"controller"`
		SegmentsKnown  bool             `json:"segments_known"`
		Segments       []map[string]any `json:"segments"`
		SegmentsUnread string           `json:"segments_unread"`
	}

	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		t.Fatalf("the answer is not JSON: %v (%s)", err, raw)
	}

	if body.SegmentsKnown {
		t.Fatalf("the answer says the segments are known, and nothing answered: %s", raw)
	}

	if len(body.Segments) != 0 || !strings.Contains(raw, `"segments":[]`) {
		t.Fatalf("the answer carries segments it did not read: %s", raw)
	}

	if body.SegmentsUnread == "" {
		t.Fatalf("the answer gives no reason for segments it could not read: %s", raw)
	}

	if body.Controller.Name != "10.0.0.136" || len(body.Controller.Segments) != 1 {
		t.Fatalf("the controller's own record was lost when the light did not answer: %s", raw)
	}
}
