package main

// Reading as the caller: the routes that name a printer answer for the person who asked, and the
// driver keeps driving every light while a restricted person reads.
//
// The fixture is a host that answers as the server does — a list per person, and every request it
// received recorded, so that a test can read the question as well as the answer. Every route the
// module serves that names a printer is exercised through the mux the process mounts, not through a
// handler a test assembled differently.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prusa3d/prusa-connect/sdk/deviceapi"
	"github.com/prusa3d/prusa-connect/sdk/routesapi"

	"github.com/nothingmn/wled-printer-status/internal/render"
)

// other is the second person a test asks as: the one who may see the printer the first may not.
const other = "user:other"

// hostOf is the printer list, answered as the host answers it: the printers one person may see, and
// nothing else. It records every question it was asked, which is how a test can tell that a route
// asked for the caller rather than for the module.
type hostOf struct {
	mu      sync.Mutex
	visible map[string][]deviceapi.Device
	err     error
	asked   []deviceapi.ListParams
}

// List is one question, answered for the person it was asked about.
func (h *hostOf) List(_ context.Context, params deviceapi.ListParams) (deviceapi.Page, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.asked = append(h.asked, params)

	if h.err != nil {
		return deviceapi.Page{}, h.err
	}

	items := h.visible[params.Principal]

	return deviceapi.Page{Items: items, Total: len(items), Limit: params.Limit}, nil
}

// questions is every list read so far, as the principals they named.
func (h *hostOf) questions() []deviceapi.ListParams {
	h.mu.Lock()
	defer h.mu.Unlock()

	return append([]deviceapi.ListParams(nil), h.asked...)
}

// printerNamed is one printer as the host describes it.
func printerNamed(id string) deviceapi.Device {
	return deviceapi.Device{ID: id, Name: strings.ToUpper(id), Connected: true}
}

// twoOwners is the fixture every test here runs on: one printer on the panel, one on the strip, each
// owned by a different person. It is the case the finding was about — someone opening the extension
// and reading the other person's machine.
func twoOwners() (bindings, *hostOf) {
	set := bindings{
		{ID: "printer-a", DeviceID: "printer-a", Address: "http://10.0.0.136", Segment: -1, Brightness: 128},
		{ID: "printer-b", DeviceID: "printer-b", Address: "http://10.0.0.253", Segment: -1, Brightness: 96},
	}

	host := &hostOf{visible: map[string][]deviceapi.Device{
		who:   {printerNamed("printer-a")},
		other: {printerNamed("printer-b")},
	}}

	return set, host
}

// asTheCaller is the whole of the module's read surface over one host and one store: the routes the
// mux mounts, so that a test cannot pass by exercising a handler assembled some other way.
type asTheCaller struct {
	mux  http.Handler
	live *runner
	host *hostOf
}

// newAsTheCaller builds the read routes over the set and the host, with a store of its own.
func newAsTheCaller(t *testing.T, set bindings, host *hostOf) *asTheCaller {
	t.Helper()

	// No kept host answer survives from another fixture: see forgetSeen.
	forgetSeen()
	forgetBindings()
	current.Store(nil)
	sent.Store(nil)

	t.Cleanup(func() {
		forgetSeen()
		forgetBindings()
		current.Store(nil)
		sent.Store(nil)
	})

	live := &runner{held: set, layout: defaultLayout(), logf: quiet}
	tables := storeFor(t, set)

	fleet := func() devices { return host }

	mux := http.NewServeMux()
	bindingAPI{live: live, fleet: fleet, parent: func() context.Context { return context.Background() },
		tables: tables, bound: time.Second}.mount(mux)
	tableAPI{live: live, fleet: fleet, tables: tables, bound: time.Second}.mount(mux)
	framesAPI{live: live, fleet: fleet, bound: time.Second, cache: newFrameCache()}.mount(mux)
	mux.HandleFunc("GET /status", statusRoute(fleet))

	return &asTheCaller{mux: mux, live: live, host: host}
}

// ask runs one request as one person and answers with the status and the body.
func (a *asTheCaller) ask(t *testing.T, person, method, path string) (int, map[string]any) {
	t.Helper()

	recorder := httptest.NewRecorder()
	a.mux.ServeHTTP(recorder, requestAs(t, person, method, path))

	var body map[string]any
	if raw := recorder.Body.Bytes(); len(raw) > 0 {
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatalf("%s %s answered something that is not JSON: %v (%s)", method, path, err, raw)
		}
	}

	return recorder.Code, body
}

// readers is every read route that names a printer, with the member its answer carries the printer
// in and how many rows the one visible printer should leave there.
//
// The list is the test's own enumeration of the surface: a route added later that names a printer has
// to be added here, which is the point of writing it out rather than counting a total.
func readers() []struct {
	path   string
	member string
} {
	return []struct {
		path   string
		member string
	}{
		{path: "/printers", member: "printers"},
		{path: "/bindings", member: "bindings"},
		{path: "/controllers", member: "controllers"},
		{path: "/status", member: "bindings"},
		{path: "/frames", member: "frames"},
		{path: "/lights/10.0.0.136/moments", member: "moments"},
		{path: "/lights/10.0.0.136/layouts", member: "layouts"},
	}
}

// TestACallerSeesOnlyTheirOwnPrinterOnEveryReadRoute is the finding, closed: each read route answers
// the printers the caller may see, and the other person's printer is absent from the answer rather
// than present and empty.
func TestACallerSeesOnlyTheirOwnPrinterOnEveryReadRoute(t *testing.T) {
	set, host := twoOwners()
	api := newAsTheCaller(t, set, host)

	for _, b := range set {
		publishBinding(b, printingJob(b.DeviceID, 40), render.Frame{
			Colour: render.ColourPrinting, Lit: 12, Total: 48, Percent: 25, Reason: render.ReasonProgress,
		})
	}

	for _, read := range readers() {
		t.Run(read.path, func(t *testing.T) {
			status, body := api.ask(t, who, http.MethodGet, read.path)
			if status != http.StatusOK {
				t.Fatalf("%s answered %d for a caller who may see one printer: %v", read.path, status, body)
			}

			rows, ok := body[read.member].([]any)
			if !ok || len(rows) == 0 {
				t.Fatalf("%s carries %v under %q, want at least the one moment row a light has: %v",
					read.path, body[read.member], read.member, body)
			}

			// The printer's own id appears nowhere the caller may not see it. The test reads the whole
			// body as one string, which is the assertion that cannot be fooled by a member it did not
			// think of.
			raw, _ := json.Marshal(body)
			if strings.Contains(string(raw), "printer-b") {
				t.Fatalf("%s answered the other person's printer: %s", read.path, raw)
			}
		})
	}

	// The mirror image, so that a route which answered nothing at all cannot pass: the second person
	// sees their own printer and not the first's.
	status, body := api.ask(t, other, http.MethodGet, "/bindings")
	if status != http.StatusOK {
		t.Fatalf("GET /bindings answered %d for the second person: %v", status, body)
	}

	raw, _ := json.Marshal(body)
	if !strings.Contains(string(raw), "printer-b") || strings.Contains(string(raw), "printer-a") {
		t.Fatalf("the second person's binding list is %s, want their own binding and not the first's", raw)
	}
}

// TestABindingOfAPrinterTheCallerMayNotSeeIsNotFound is the single-binding route: the answer is the
// one a name this module does not drive gets, so that the binding's existence is not in the answer
// either.
func TestABindingOfAPrinterTheCallerMayNotSeeIsNotFound(t *testing.T) {
	set, host := twoOwners()
	api := newAsTheCaller(t, set, host)

	for _, path := range []string{"/bindings/printer-b", "/bindings/printer-b/layout"} {
		status, body := api.ask(t, who, http.MethodGet, path)
		if status != http.StatusNotFound {
			t.Fatalf("%s answered %d for a binding of a printer this caller may not see, want 404: %v",
				path, status, body)
		}

		if found := code(t, body); !strings.HasSuffix(found, "BINDING_NOT_FOUND") {
			t.Fatalf("%s answered %q, want BINDING_NOT_FOUND", path, found)
		}
	}

	// And a light whose only printer is somebody else's is a light this module does not drive.
	for _, path := range []string{"/lights/10.0.0.253/moments", "/lights/10.0.0.253/layouts"} {
		status, body := api.ask(t, who, http.MethodGet, path)
		if status != http.StatusNotFound {
			t.Fatalf("%s answered %d for a light this caller may not see, want 404: %v", path, status, body)
		}
	}

	// The caller's own binding is still there, so the test cannot pass by refusing everything.
	status, body := api.ask(t, who, http.MethodGet, "/bindings/printer-a")
	if status != http.StatusOK {
		t.Fatalf("GET /bindings/printer-a answered %d for the printer's owner: %v", status, body)
	}
}

// TestThePrinterListIsAskedForTheCaller is the question itself: GET /printers passes the caller's
// principal to the host, with the page bound, and answers the rows that came back for it.
func TestThePrinterListIsAskedForTheCaller(t *testing.T) {
	set, host := twoOwners()
	api := newAsTheCaller(t, set, host)

	status, body := api.ask(t, who, http.MethodGet, "/printers")
	if status != http.StatusOK {
		t.Fatalf("GET /printers answered %d: %v", status, body)
	}

	questions := host.questions()
	if len(questions) == 0 {
		t.Fatal("GET /printers answered without asking the host anything")
	}

	last := questions[len(questions)-1]

	// The person, never empty: an empty principal is this module asking for its own work, which is
	// exactly the read the finding was about.
	if last.Principal != who {
		t.Fatalf("the list was asked for the principal %q, want %q", last.Principal, who)
	}

	if last.Limit != deviceapi.MaxPageLimit {
		t.Fatalf("the list was asked for the limit %d, want %d", last.Limit, deviceapi.MaxPageLimit)
	}

	if count, _ := body["count"].(float64); int(count) != 1 {
		t.Fatalf("the list answered %v printers, want the one this caller may see: %v", body["count"], body)
	}

	if total, _ := body["total"].(float64); int(total) != 1 {
		t.Fatalf("the list answered a total of %v, want the caller's own fleet of one: %v", body["total"], body)
	}
}

// TestACallerWhoSeesNoPrinterGetsEmptyListsAndNoError is the other side of failing closed: nothing
// visible is not an error that stops the module, and it is not the whole fleet either.
func TestACallerWhoSeesNoPrinterGetsEmptyListsAndNoError(t *testing.T) {
	set, host := twoOwners()
	api := newAsTheCaller(t, set, host)

	// The host answers, and the answer is that this person may see nothing.
	host.visible = map[string][]deviceapi.Device{}

	publishBinding(set[0], printingJob("printer-a", 40), render.Frame{Colour: render.ColourPrinting, Total: 48})

	for _, read := range readers() {
		if strings.Contains(read.path, "10.0.0.136") {
			// A light the caller may see nothing on is a light this module does not drive for them.
			status, body := api.ask(t, who, http.MethodGet, read.path)
			if status != http.StatusNotFound {
				t.Fatalf("%s answered %d for a caller who may see no printer, want 404: %v", read.path, status, body)
			}

			continue
		}

		status, body := api.ask(t, who, http.MethodGet, read.path)
		if status != http.StatusOK {
			t.Fatalf("%s answered %d for a caller who may see no printer, want 200 and an empty list: %v",
				read.path, status, body)
		}

		rows, ok := body[read.member].([]any)
		if !ok || len(rows) != 0 {
			t.Fatalf("%s answered %v under %q, want an empty list", read.path, body[read.member], read.member)
		}

		raw, _ := json.Marshal(body)
		if strings.Contains(string(raw), "printer-a") || strings.Contains(string(raw), "printer-b") {
			t.Fatalf("%s named a printer to a caller who may see none: %s", read.path, raw)
		}
	}
}

// TestAHostThatCannotAnswerIsARefusalAndNotTheFleet is the fifth rule: a question that failed is a
// refusal, never a wide-open read.
func TestAHostThatCannotAnswerIsARefusalAndNotTheFleet(t *testing.T) {
	set, host := twoOwners()
	api := newAsTheCaller(t, set, host)

	host.err = errors.New("the host did not answer")

	for _, one := range []struct {
		path string
		code string
	}{
		{path: "/bindings", code: "ACCESS_UNKNOWN"},
		{path: "/status", code: "ACCESS_UNKNOWN"},
		{path: "/frames", code: "ACCESS_UNKNOWN"},
		{path: "/controllers", code: "ACCESS_UNKNOWN"},
		{path: "/printers", code: "PRINTERS_UNAVAILABLE"},
	} {
		status, body := api.ask(t, who, http.MethodGet, one.path)
		if status != http.StatusServiceUnavailable {
			t.Fatalf("%s answered %d when the host could not answer, want 503: %v", one.path, status, body)
		}

		if found := code(t, body); !strings.HasSuffix(found, one.code) {
			t.Fatalf("%s answered %q, want %s", one.path, found, one.code)
		}

		raw, _ := json.Marshal(body)
		if strings.Contains(string(raw), "printer-a") || strings.Contains(string(raw), "printer-b") {
			t.Fatalf("%s answered the fleet when the host could not answer: %s", one.path, raw)
		}
	}
}

// TestARequestWithNoCallerReadsNothing is the refusal a request the host did not attribute gets. It
// is the same stance the mutating routes take: nobody to ask about, so nothing answered.
func TestARequestWithNoCallerReadsNothing(t *testing.T) {
	set, host := twoOwners()
	api := newAsTheCaller(t, set, host)

	request, err := routesapi.Request{Method: http.MethodGet, Path: "/bindings"}.HTTP(context.Background())
	if err != nil {
		t.Fatalf("build a request with no caller: %v", err)
	}

	recorder := httptest.NewRecorder()
	api.mux.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("a read with no caller answered %d, want 403: %s", recorder.Code, recorder.Body.String())
	}
}

// TestTheVisibilityAnswerIsKeptForAMoment is the cost side: one screenful of reads is one question to
// the host, and the answer is kept for the figure this report quotes.
func TestTheVisibilityAnswerIsKeptForAMoment(t *testing.T) {
	set, host := twoOwners()
	api := newAsTheCaller(t, set, host)

	if visibleKept != 30*time.Second {
		t.Fatalf("the host's answer is kept for %s, want the 30 seconds this release states", visibleKept)
	}

	for range 5 {
		status, body := api.ask(t, who, http.MethodGet, "/bindings")
		if status != http.StatusOK {
			t.Fatalf("GET /bindings answered %d: %v", status, body)
		}
	}

	if asked := len(host.questions()); asked != 1 {
		t.Fatalf("five reads asked the host %d times, want one question kept for the screenful: %v",
			asked, host.questions())
	}

	// A second person is a second question: the answer is one person's, so it cannot be shared.
	if status, _ := api.ask(t, other, http.MethodGet, "/bindings"); status != http.StatusOK {
		t.Fatalf("GET /bindings answered %d for the second person", status)
	}

	if asked := len(host.questions()); asked != 2 {
		t.Fatalf("the second person's read asked the host %d times in all, want two questions", asked)
	}
}

// TestTheDriverKeepsDrivingWhileARestrictedCallerReads is the rule that the filter is a property of
// the read path and of nothing else: both lights are still written to, second after second, while a
// person who may see one of them reads.
func TestTheDriverKeepsDrivingWhileARestrictedCallerReads(t *testing.T) {
	left, right := newFakeStrip(), newFakeStrip()

	set := bindings{
		{ID: "printer-a", DeviceID: "printer-a", Address: "http://10.0.0.136", Segment: -1, Brightness: 128},
		{ID: "printer-b", DeviceID: "printer-b", Address: "http://10.0.0.253", Segment: -1, Brightness: 96},
	}

	lights := map[string]*fakeStrip{"http://10.0.0.136": left, "http://10.0.0.253": right}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	watch := &runner{
		newSource: func(b binding) (watchSource, error) {
			return &fixedSource{snapshot: printingJob(b.DeviceID, 40)}, nil
		},
		newLight: func(address string) (strip, error) { return lights[address], nil },
		logf:     quiet,
		tick:     5 * time.Millisecond,
		grace:    2 * time.Second,
	}

	if err := watch.apply(ctx, set); err != nil {
		t.Fatalf("start the watch: %v", err)
	}

	t.Cleanup(watch.stop)

	host := &hostOf{visible: map[string][]deviceapi.Device{who: {printerNamed("printer-a")}}}
	forgetSeen()

	mux := http.NewServeMux()
	bindingAPI{live: watch, fleet: func() devices { return host },
		parent: func() context.Context { return ctx }, bound: time.Second}.mount(mux)

	// A restricted caller reads while the writers run: the read must not stop a light, and it must
	// not reach the one they cannot see.
	status, body := apiDo(t, mux, "/bindings")
	if status != http.StatusOK {
		t.Fatalf("GET /bindings answered %d while the watch was running: %v", status, body)
	}

	if count, _ := body["count"].(float64); int(count) != 1 {
		t.Fatalf("the caller was answered %v bindings, want the one they may see", body["count"])
	}

	eventually(t, 3*time.Second, func() string {
		return fmt.Sprintf("the panel was written %d times and the strip %d", left.wrote(), right.wrote())
	}, func() bool { return left.wrote() > 0 && right.wrote() > 0 })
}

// apiDo is one GET on a mux, as the fixed caller, answered with the status and the body.
func apiDo(t *testing.T, mux http.Handler, path string) (int, map[string]any) {
	t.Helper()

	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, forwarded(http.MethodGet, path, nil))

	var body map[string]any
	if raw := recorder.Body.Bytes(); len(raw) > 0 {
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatalf("GET %s answered something that is not JSON: %v (%s)", path, err, raw)
		}
	}

	return recorder.Code, body
}

// statusHost is GET /status over a host that answers with these printers: the route the mux mounts,
// with the one fact a status test needs — who may see what.
func statusHost(t *testing.T, printers ...deviceapi.Device) http.Handler {
	t.Helper()

	forgetSeen()

	return statusRoute(func() devices {
		return &hostOf{visible: map[string][]deviceapi.Device{who: printers}}
	})
}

// TestALightAddressIsPlainHTTPOnPortEighty is the address rule, as words: a value that reaches past
// the two ports the release declares is refused, and the refusal names them.
func TestALightAddressIsPlainHTTPOnPortEighty(t *testing.T) {
	for _, one := range []struct {
		name     string
		address  string
		accepted string
		refused  string
	}{
		{name: "a bare host", address: "10.0.0.136", accepted: "http://10.0.0.136"},
		{name: "the same host with a scheme", address: "http://10.0.0.136", accepted: "http://10.0.0.136"},
		{name: "the same host with its port named", address: "http://10.0.0.136:80", accepted: "http://10.0.0.136:80"},
		{name: "a trailing slash", address: "http://10.0.0.136/", accepted: "http://10.0.0.136"},
		{name: "https", address: "https://10.0.0.136", refused: "https"},
		{name: "a port of its own", address: "http://10.0.0.136:8080", refused: "8080"},
		{name: "the DDP port on the web address", address: "http://10.0.0.136:4048", refused: "4048"},
		{name: "a path", address: "http://10.0.0.136/wled", refused: "bare host"},
	} {
		t.Run(one.name, func(t *testing.T) {
			got, err := checkLightAddress(one.address)

			if one.refused == "" {
				if err != nil {
					t.Fatalf("%q was refused, want %q: %v", one.address, one.accepted, err)
				}

				if got != one.accepted {
					t.Fatalf("%q was stored as %q, want %q", one.address, got, one.accepted)
				}

				return
			}

			if err == nil {
				t.Fatalf("%q was accepted as %q, want it refused: it reaches past the declared ports",
					one.address, got)
			}

			// The two ports the release declares, in the words a person reads.
			for _, want := range []string{"80", "4048", one.refused} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("the refusal of %q reads %q, want it to name %q", one.address, err, want)
				}
			}
		})
	}
}

// TestASettingsValueThatReachesPastTheDeclaredPortsIsRefused is the same rule where an operator meets
// it first: a filled option, judged at the start, with the port named in the sentence.
func TestASettingsValueThatReachesPastTheDeclaredPortsIsRefused(t *testing.T) {
	for _, address := range []string{"https://10.0.0.136", "http://10.0.0.136:8080"} {
		sent := Settings{Light1Address: address}

		err := sent.validate()
		if err == nil {
			t.Fatalf("settings naming %q were accepted, want the refusal naming the declared ports", address)
		}

		for _, want := range []string{"light1_address", "80", "4048"} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("the refusal of %q reads %q, want it to name %q", address, err, want)
			}
		}

		// And the lenient reading a document gets permits nothing at all, rather than a client.
		if declared := (Settings{Light1Address: address}).DeclaredLights()[0]; declared.Filled {
			t.Fatalf("a slot holding %q counts as filled, want a slot that permits nothing", address)
		}
	}
}

// TestABindingAtAnAddressThatReachesPastTheDeclaredPortsIsRefused is the editor's half: a binding
// typed with https or a port of its own is refused in the same words, before the store is touched.
func TestABindingAtAnAddressThatReachesPastTheDeclaredPortsIsRefused(t *testing.T) {
	withHost(t, allowing{})

	light := newFakeStrip()
	api := newAPIUnderTest(t, light, []deviceapi.Device{printerNamed("printer-a")}, bindings{
		{DeviceID: "printer-a", Address: "http://10.0.0.136", Segment: -1, Brightness: 128},
	})

	for _, address := range []string{"https://10.0.0.253", "http://10.0.0.253:8080"} {
		status, body := api.do(t, http.MethodPost, "/bindings",
			fmt.Sprintf(`{"printer":"printer-a","controller":%q}`, address))

		if status != http.StatusConflict {
			t.Fatalf("a binding at %q answered %d, want 409: %v", address, status, body)
		}

		detail, _ := body["detail"].(string)
		for _, want := range []string{"80", "4048"} {
			if !strings.Contains(detail, want) {
				t.Fatalf("the refusal of %q reads %q, want it to name %q", address, detail, want)
			}
		}
	}

	// Nothing was written: the set is the one the fixture started with.
	if set := api.watch.all(); len(set) != 1 {
		t.Fatalf("the set holds %d bindings after two refused creates, want the one it had", len(set))
	}
}
