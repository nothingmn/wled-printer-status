// The routes an editor is built on: what this module drives, and what it could drive.
//
// # Why these exist
//
// A binding is (printer, controller, segment), and until now the only way to write one was
// four settings that each hold one value. Four settings cannot say "two printers", so the
// module could run one printer on one light and the multi-binding core underneath it was
// unreachable — by settings, and by any person.
//
// The surface here is the one the module can actually offer. A manifest declares nine value
// shapes and the host draws a `list` or an `object` as a block of JSON in a text area; nothing
// in the host builds a form out of a module's routes, and a module's own page needs a bundled
// browser half built against @prusa/connect-ui. So these routes do not make a screen appear.
// They are what a screen would call, what curl reaches today, and what the `bindings` setting
// in module.yaml writes in one field until there is one.
//
// # What a change does and what it costs
//
// Every write here stops the watch and starts it again on the new set, which is what makes a
// change take effect in the writer that is already talking to the controller: no restart, no
// reinstall, no settings save. The cost is one turn of the light's own frame, and — when the
// controller is unreachable — up to shutdownGrace of waiting for a borrowed segment to be
// given back, because a writer that gave a segment back after the next one captured it would
// hand the operator's own bar back as though it were his.
//
// # What a change does save
//
// It saves the set. These routes are the editing surface docs/product-requirements.md §4 gives
// this module, so a binding added, moved or deleted here is written into the module's own
// configuration store — the same versioned document, the same read-change-rename, the same lock
// the state, moment, layout, window and pause tables write through — and it is what the next
// start reads.
//
// That makes the store unavailable a refusal and not a footnote. A module granted no data
// directory, a directory that will not take the write, a document from a newer release: the route
// answers STORE_UNAVAILABLE and the set that was running is still running. It used to answer
// `applied: true` beside `persisted: false` — honest about the wrong half, because the change did
// take effect and then disappeared at the next restart, with nothing in the answer a screen could
// act on beforehand.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"

	"time"

	"github.com/prusa3d/prusa-connect/sdk/deviceapi"
	"github.com/prusa3d/prusa-connect/sdk/fault"

	"github.com/nothingmn/wled-printer-status/internal/wled"
)

// segmentReadTimeout bounds the one read GET /controllers/{name} makes of a light. The client's
// own timeout is two seconds, so this is the outer bound for a socket that hangs without
// answering at all: the host gives a route ten seconds before the caller gets nothing, and a
// controller that has been unplugged must cost a moment rather than the whole of it.
const segmentReadTimeout = 3 * time.Second

// bindingAPI is the editor's routes, and the two things they reach: the watch this process is
// running and the host's printer list.
//
// It is a value with its own fields rather than a set of functions over the package's globals
// so that a test runs the same handlers against a watch built on fakes and a fleet that needs
// no host. The process fills both in once, in run, before the routes can be called.
type bindingAPI struct {
	live *runner

	// fleet is the host's printer list. It is a function because the process has no
	// connection when this value is built and the honest answer then is nil, not a typed nil
	// pointer behind an interface that panics on its first call.
	fleet func() devices

	// parent is the context a watch started by one of these routes runs on. It is not the
	// request's context: a watch tied to the call that made it would die the moment the answer
	// was written. Zero means the process's own session, which is what the module runs on.
	parent func() context.Context

	// newLight builds the client for one controller's address. It is a field for one route:
	// GET /controllers/{name} asks the light what segments it holds, and a test that ran that
	// through the process's own client would be a test that dials a controller. Nil means
	// wled.New, which is what the process uses.
	newLight func(address string) (strip, error)

	// bound is how long one read of the printer list may take. Zero means resolveTimeout,
	// which is the bound configure already uses: a list that never came back must not leave a
	// route hanging.
	bound time.Duration

	// tables is this module's own configuration store, which is where a binding lives now that
	// this module owns its configuration: a create, a replace and a delete are writes into that
	// document, not a list this process holds for as long as it happens to run. Nil means the
	// module was granted no data directory, and then a change is refused rather than applied and
	// lost — see reviseStored.
	tables *configTables

	// permits answers whether one address is one of the settings' declared light slots. Nil means
	// no declaration is in force — a module built before the manifest carried options, and every
	// test that builds this value of its own — and the route then behaves as it did before the cap.
	// The process sets it to addressDeclared.
	permits func(string) bool
}

// allows answers whether one address may be contacted, with the nil rule above.
func (api bindingAPI) allows(address string) bool {
	if api.permits == nil {
		return true
	}

	return api.permits(address)
}

// kept answers whether this module has a store it can keep a change in, which is what `persisted`
// says on both read routes.
//
// It is asked of the document and not of the running set: the routes keep the two in step, a start
// reads the document, and a read of it is what the tables' routes make on every request anyway.
// False means the document cannot be read at all — no granted directory, a path that is not one,
// or a document this build will not act on — which is exactly the case where nothing on any of
// these surfaces can be saved.
func (api bindingAPI) kept() bool {
	if api.tables == nil {
		return false
	}

	_, err := api.tables.load()

	return err == nil
}

// lightFor is the client for one controller: the test's when one was supplied, and the process's
// own otherwise.
func (api bindingAPI) lightFor(address string) (strip, error) {
	if api.newLight != nil {
		return api.newLight(address)
	}

	return wled.New(address)
}

// watchContext is the context a change starts its watch on: the session's, which outlives every
// request, and never the request's own.
func (api bindingAPI) watchContext() context.Context {
	if api.parent != nil {
		return api.parent()
	}

	return sessionCtx
}

// mount adds the editor's routes to mux.
func (api bindingAPI) mount(mux *http.ServeMux) {
	// The set this module is running, in the order the operator wrote it.
	mux.HandleFunc("GET /bindings", api.listBindings)

	// The printers the host holds: the answer that turns the device id in a text field into a
	// choice a person can make.
	mux.HandleFunc("GET /printers", api.listPrinters)

	// The controllers this module drives. Read-only, and the reason is at listControllers.
	mux.HandleFunc("GET /controllers", api.listControllers)
	mux.HandleFunc("GET /controllers/{name}", api.readController)

	// One binding, named by its own id: the printer's device id while one printer is shown once,
	// and `<device id>@<light>:<segment>` once a printer sits on more than one light. GET
	// /bindings answers that name for every binding.
	mux.HandleFunc("GET /bindings/{id}", api.readBinding)
	mutating(mux, "PUT /bindings/{id}", actionConfigureResource, api.updateBinding)
	mutating(mux, "DELETE /bindings/{id}", actionConfigureResource, api.deleteBinding)

	// A binding the set does not hold yet.
	mutating(mux, "POST /bindings", actionConfigureResource, api.createBinding)
}

// bindingAnswer is one binding as a caller reads it: the name it is addressed by, the light and
// segment it drives, the number a person needs to know how much of the controller it takes, and
// the name of the machine so that a list of device ids is not what an operator has to match
// against the printers screen.
//
// Light, Segment and Whole are here because two bindings may now show one printer: a list that
// carried only the device id would draw two identical rows for the panel and the strip, and there
// would be nothing on the page to tell them apart.
type bindingAnswer struct {
	// ID is the binding's name on these routes: the host's device id for the printer it shows
	// while that printer has one binding, and `<device id>@<light>:<segment>` once it has two.
	// It is minted once — by identified, or by the migration that introduced it — and never
	// changed, so a path a screen already holds keeps working.
	ID string `json:"id"`

	// Position is where the binding sits in the operator's order, counting from one.
	Position int `json:"position"`

	// Printer is the device id, and PrinterName the name an operator gave the machine. The
	// name is absent when the host could not be read: a binding is this module's own record
	// and a printer's name is the host's, so a host that will not answer must not make the
	// list unreadable.
	Printer     string `json:"printer"`
	PrinterName string `json:"printer_name,omitempty"`

	// Light is the controller's name on these routes — its address without the scheme — which is
	// the noun GET /controllers and the Layout screen address a light by.
	Light string `json:"light"`

	// Controller is the address as the operator wrote it, and Segment the one it drives with
	// -1 for the whole controller. Whole says the same thing in the shape a person reads
	// without knowing that -1 means it.
	Controller string `json:"controller"`
	Segment    int    `json:"segment"`
	Whole      bool   `json:"whole"`

	// Brightness is written once at start, and only on a whole-controller binding. It is
	// absent on a borrowed segment, where nothing writes one.
	Brightness int `json:"brightness,omitempty"`
}

// bindingsAnswer is the set, and what a caller has to know to read it.
type bindingsAnswer struct {
	Bindings []bindingAnswer `json:"bindings"`
	Count    int             `json:"count"`

	// Source says where this set came from: the `bindings` setting, the four settings that
	// came before it, or a change made through these routes since the module started. It is
	// the first thing to read when a list is not the one the settings file shows.
	Source string `json:"source"`

	// Persisted says this set is the one in this module's own configuration store, so it is what
	// the next start runs. A module granted no data directory has nowhere to keep a set and says
	// so here, before a caller finds it out by looking at a dark light after a restart.
	Persisted bool `json:"persisted"`

	// NamesReadFailed says why a printer's name is missing, absent when the host answered.
	NamesReadFailed string `json:"names_read_failed,omitempty"`

	// Refused is every binding the store holds whose address is not one of the five declared light
	// slots. It is not driven, not probed and never written to (declared.go). It is reported here
	// rather than silently dropped, because the row is exactly what a person has to see in order
	// to copy its address into the extension's settings, or delete it.
	Refused []refusedBinding `json:"refused,omitempty"`
}

// refusedBinding is one binding the declaration refuses, and why.
type refusedBinding struct {
	ID         string `json:"id"`
	Controller string `json:"controller"`
	Light      string `json:"light"`
	Segment    int    `json:"segment"`
	Whole      bool   `json:"whole"`
	Reason     string `json:"reason"`
}

// refusedAnswersOf renders the refused rows, in the operator's order.
func refusedAnswersOf(set bindings) []refusedBinding {
	out := make([]refusedBinding, 0, len(set))

	for _, b := range set {
		out = append(out, refusedBinding{
			ID:         b.ID,
			Controller: b.Address,
			Light:      lightName(b.Address),
			Segment:    b.Segment,
			Whole:      b.Segment < 0,
			Reason:     refusedReason(b.Address),
		})
	}

	return out
}

// answerOf renders one binding for a caller, with the printer's name when there is one.
func answerOf(b binding, position int, names map[string]string) bindingAnswer {
	return bindingAnswer{
		ID:          b.ID,
		Position:    position,
		Printer:     b.DeviceID,
		PrinterName: names[b.DeviceID],
		Light:       lightName(b.Address),
		Controller:  b.Address,
		Segment:     b.Segment,
		Whole:       b.Segment < 0,
		Brightness:  b.Brightness,
	}
}

// positionOf is where one binding sits in a set, counting from one, and zero for one the set
// does not hold. It is looked up by the binding's name, which is the only thing that names one.
func positionOf(set bindings, id string) int {
	for i, b := range set {
		if b.ID != "" && b.ID == id {
			return i + 1
		}
	}

	return 0
}

// answersOf renders a set, in the order the operator wrote it.
func answersOf(set bindings, names map[string]string) []bindingAnswer {
	out := make([]bindingAnswer, 0, len(set))

	for i, b := range set {
		out = append(out, answerOf(b, i+1, names))
	}

	return out
}

// timeout is how long one read of the host's printer list may take.
func (api bindingAPI) timeout() time.Duration {
	if api.bound > 0 {
		return api.bound
	}

	return resolveTimeout
}

// printerNames is every printer the host holds, by device id, and why the read failed when it
// did. It is one host read with the same bound as the one configure makes: a list that never
// came back must not leave a route hanging.
//
// It is a function rather than a method because two surfaces need it — the binding editor, to put
// a name beside a device id, and the moment table, to label the sub-rows an Events screen draws —
// and a second copy of this read would be a second place for the bound and the failure sentence to
// drift.
func printerNames(ctx context.Context, fleet devices, bound time.Duration) (map[string]string, string) {
	if fleet == nil {
		return nil, "this process has no connection to the host yet"
	}

	reading, cancel := context.WithTimeout(ctx, bound)
	defer cancel()

	page, err := fleet.List(reading, deviceapi.ListParams{Limit: deviceapi.MaxPageLimit})
	if err != nil {
		return nil, err.Error()
	}

	names := make(map[string]string, len(page.Items))

	for _, device := range page.Items {
		names[device.ID] = device.Name
	}

	return names, ""
}

// printerNames is the same read on the binding editor's own route, with the bound this value
// carries.
func (api bindingAPI) printerNames(ctx context.Context) (map[string]string, string) {
	return printerNames(ctx, api.fleet(), api.timeout())
}

// listBindings is GET /bindings: the set this module is driving.
func (api bindingAPI) listBindings(w http.ResponseWriter, r *http.Request) {
	// The whole stored set, refused rows included: a binding the declaration will not drive is a
	// row a person has to be able to find, so it is answered in `refused` rather than left out.
	running := api.live.set()
	names, why := api.printerNames(r.Context())

	answer(w, http.StatusOK, bindingsAnswer{
		Bindings:        answersOf(running, names),
		Count:           len(running),
		Refused:         refusedAnswersOf(api.live.refused()),
		Source:          api.live.source(),
		Persisted:       api.kept(),
		NamesReadFailed: why,
	})
}

// readBinding is GET /bindings/{id}: one binding, by its own name.
func (api bindingAPI) readBinding(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	set := api.live.all()

	found, ok := set.find(id)
	if !ok {
		refuse(w, r, BindingNotFound.New(
			"This module drives no binding of that name. Read GET /bindings for the ones it does.").
			With(fault.StringParam("binding", id)))

		return
	}

	names, why := api.printerNames(r.Context())

	foundAnswer := answerOf(found, positionOf(set, found.ID), names)

	said := map[string]any{
		"binding":           foundAnswer,
		"persisted":         api.kept(),
		"names_read_failed": why,
	}

	if !api.allows(found.Address) {
		said["refused"] = refusedAnswersOf(bindings{found})
	}

	answer(w, http.StatusOK, said)
}

// bindingRequest is what POST and PUT accept: one binding, written the way the `bindings`
// setting writes a line of its list.
//
// Segment and Brightness are pointers for the reason the setting's are: absent is not zero.
// Zero is a segment a controller holds and a brightness a strip takes.
type bindingRequest struct {
	Printer    string `json:"printer"`
	Controller string `json:"controller"`
	Segment    *int   `json:"segment"`
	Brightness *int   `json:"brightness"`
}

// read is one request body, and the refusal that says what was wrong with it.
func readBindingRequest(r *http.Request) (bindingRequest, error) {
	var sent bindingRequest

	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(&sent); err != nil {
		return sent, fmt.Errorf(
			"the body is not one binding: %w: write printer and controller, and segment or brightness when they are wanted", err)
	}

	return sent, nil
}

// resolveName is the device id the host holds for what the request named, refusing a printer
// this host does not hold. It is what makes the device id in a text field a choice: a caller
// reads GET /printers, and the id it carries is one this call will accept.
func (api bindingAPI) resolveName(ctx context.Context, named string) (string, error) {
	if strings.TrimSpace(named) == "" {
		return "", errors.New("no printer: write the device id from GET /printers, or the name an operator gave the machine")
	}

	fleet := api.fleet()
	if fleet == nil {
		return "", errors.New("this process has no connection to the host yet, so no printer list can be read")
	}

	reading, cancel := context.WithTimeout(ctx, api.timeout())
	defer cancel()

	return resolvePrinter(reading, fleet, named)
}

// toSetting turns one request into the binding it asks for, with the defaults the manifest
// publishes for the fields it left out.
//
// The name is carried in rather than asked for: a PUT replaces a binding and keeps its name, and
// a POST passes none so that identified can mint one from the printer and the light.
func toSetting(id, deviceID string, asked bindingRequest) binding {
	one := bindingSetting{Controller: asked.Controller, Segment: asked.Segment, Brightness: asked.Brightness}

	return binding{
		ID:         id,
		DeviceID:   deviceID,
		Address:    strings.TrimSpace(one.Controller),
		Segment:    one.segment(),
		Brightness: one.brightness(),
	}
}

// createBinding is POST /bindings: one more binding, shown from now on — a second light for a
// printer already shown, or the first light for one that is not.
//
// A printer already shown is no longer a refusal. docs/product-requirements.md §2.2: "A printer
// may sit on several lights — multi-room status — at the cost of a second steady stream." What is
// still refused is the same segment of the same controller twice, and bindings.check says so when
// the set is judged, naming the binding that got there first.
//
// The set is written into this module's own store before the writer is touched, so the binding is
// what the next start reads; a store that will not take the write refuses the request and leaves
// the running set alone.
func (api bindingAPI) createBinding(w http.ResponseWriter, r *http.Request) {
	asked, err := readBindingRequest(r)
	if err != nil {
		refuse(w, r, BindingBodyInvalid.New(err.Error()))

		return
	}

	// The declaration first, before the host is asked and before the store is touched: an address
	// the settings do not name is a binding this module may not drive, so it is refused in words
	// and nothing is written. This is the route-level half of declared.go's rule; the watch is the
	// other half, for a binding that reached the store before the rule existed.
	if !api.allows(strings.TrimSpace(asked.Controller)) {
		refuse(w, r, BindingRefused.New(refusedReason(strings.TrimSpace(asked.Controller))))

		return
	}

	deviceID, err := api.resolveName(r.Context(), asked.Printer)
	if err != nil {
		refuse(w, r, BindingRefused.New(err.Error()))

		return
	}

	one := toSetting("", deviceID, asked)

	var added binding

	set, err := api.live.reviseStored(api.watchContext(), func(current bindings) (bindings, error) {
		// Appended and then named across the whole set, in the operator's order: a printer's
		// first binding is already named with the bare device id, so the second takes the
		// qualified one and the first keeps the path it answered at before. See identified.
		next := append(append(bindings(nil), current...), one).identified()

		added = next[len(next)-1]

		return next, nil
	})
	if err != nil {
		api.refuseChange(w, r, err)

		return
	}

	names, why := api.printerNames(r.Context())

	answer(w, http.StatusCreated, map[string]any{
		"binding":           answerOf(added, positionOf(set, added.ID), names),
		"applied":           true,
		"persisted":         true,
		"names_read_failed": why,
		"note":              appliedNote,
	})
}

// updateBinding is PUT /bindings/{id}: the same binding, on a different controller or segment, or
// at a different brightness.
//
// The name is what a caller addresses, so it does not move: a binding that is moved to another
// segment keeps the name it had, because a path a screen already holds must keep working — and
// because the layout table is keyed on that name, so a name that moved would orphan the picture
// somebody composed. What is refused is a body naming a different printer than this binding
// shows — that is a different binding, not an edit — and a body that would put this binding on a
// segment another one drives.
//
// The replaced binding is written into this module's own store before the writer is touched, so a
// binding moved from one segment to another is on the new segment after the next start too, under
// the name it already had.
func (api bindingAPI) updateBinding(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	asked, err := readBindingRequest(r)
	if err != nil {
		refuse(w, r, BindingBodyInvalid.New(err.Error()))

		return
	}

	// A move to an address the declaration does not name is refused before anything is read or
	// written, for the same reason a create is: the watch would never drive it.
	if !api.allows(strings.TrimSpace(asked.Controller)) {
		refuse(w, r, BindingRefused.New(refusedReason(strings.TrimSpace(asked.Controller))))

		return
	}

	// The printer a body names is resolved before the change is taken, because a host read
	// inside the change would hold the lock the whole watch is rebuilt under.
	var named string

	if wanted := strings.TrimSpace(asked.Printer); wanted != "" {
		named, err = api.resolveName(r.Context(), wanted)
		if err != nil {
			refuse(w, r, BindingRefused.New(err.Error()))

			return
		}
	}

	var changed binding

	set, err := api.live.reviseStored(api.watchContext(), func(current bindings) (bindings, error) {
		found, held := current.find(id)
		if !held {
			return nil, fmt.Errorf("%w: %s", errNoBinding, id)
		}

		if named != "" && named != found.DeviceID {
			return nil, fmt.Errorf(
				"a binding is named once and this one shows printer %s: to show %s instead, delete this binding and add another",
				found.DeviceID, named)
		}

		// The id is the found binding's own, whatever the body carried: a replace moves where a
		// binding is shown and never what it is called.
		changed = toSetting(found.ID, found.DeviceID, asked)

		return current.with(changed), nil
	})
	if err != nil {
		api.refuseChange(w, r, err)

		return
	}

	names, why := api.printerNames(r.Context())

	answer(w, http.StatusOK, map[string]any{
		"binding":           answerOf(changed, positionOf(set, changed.ID), names),
		"applied":           true,
		"persisted":         true,
		"names_read_failed": why,
		"note":              appliedNote,
	})
}

// deleteBinding is DELETE /bindings/{id}: this binding stops being shown. A printer with two
// bindings loses one of them, which is the only way back from a second light.
//
// The last binding may be deleted. It is a thing an operator may want — the light stops being
// written to, and is left as the last frame found it — and it is better said than done by
// removing a setting and restarting the host.
//
// The row comes out of this module's store before the writer is touched, so a deleted binding does
// not come back at the next start.
func (api bindingAPI) deleteBinding(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	var left bindings

	_, err := api.live.reviseStored(api.watchContext(), func(current bindings) (bindings, error) {
		kept, held := current.without(id)
		if !held {
			return nil, fmt.Errorf("%w: %s", errNoBinding, id)
		}

		left = kept

		return kept, nil
	})
	if err != nil {
		api.refuseChange(w, r, err)

		return
	}

	answer(w, http.StatusOK, map[string]any{
		"deleted":   id,
		"remaining": answersOf(left, nil),
		"applied":   true,
		"driving":   len(left) > 0,
		"persisted": true,
		"note":      appliedNote,
	})
}

// refuseChange is what a create, a replace and a delete answer when the change they asked for was
// not made. Three answers and not one, because a caller acts differently on each:
//
//   - the binding is not there, so the path names something this module does not drive — 404;
//   - the set that change would make breaks a rule, so the body has to change — 409;
//   - the change could not be kept, so the same request may succeed later and the set that was
//     running is running still — 503.
//
// It is one function so that no route can answer a store that would not take the write with
// "refused" and no route can apply a change it could not keep.
func (api bindingAPI) refuseChange(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, errNoBinding):
		refuse(w, r, BindingNotFound.New(err.Error()).With(fault.StringParam("binding", r.PathValue("id"))))
	case errors.Is(err, errNotKept):
		refuse(w, r, StoreUnavailable.New(err.Error()))
	default:
		refuse(w, r, BindingRefused.New(err.Error()))
	}
}

// printerAnswer is one printer the host holds: the choice a person makes instead of typing a
// device id.
type printerAnswer struct {
	ID string `json:"id"`

	// Name is what an operator calls the machine, and Model what it is.
	Name  string `json:"name"`
	Model string `json:"model,omitempty"`

	// Connected says whether the host holds a session for it now.
	Connected bool `json:"connected"`

	// Bound is the first binding that shows this printer, and absent when none does. It is the
	// flat answer a picker wants — "is this printer already on a light, and where" — and it
	// speaks for the first of several when a printer sits on two.
	Bound *boundAnswer `json:"bound,omitempty"`

	// BoundCount is how many bindings show this printer: 0 for none, 1 for the ordinary case, and
	// 2 or more for a printer in more than one room. It is a count rather than a list because the
	// bindings themselves are GET /bindings' answer, and a second copy of them here would be a
	// second place to keep in step.
	BoundCount int `json:"bound_count"`
}

// boundAnswer is where a printer is already shown.
type boundAnswer struct {
	// ID is the binding's name on these routes, Light the controller's name, and the three after
	// them what the binding drives — which is how two bound answers for one printer are told
	// apart.
	ID         string `json:"id"`
	Light      string `json:"light"`
	Controller string `json:"controller"`
	Segment    int    `json:"segment"`
	Whole      bool   `json:"whole"`
}

// listPrinters is GET /printers: every printer the host holds, each with the binding that
// already shows it.
//
// This is the route that answers "where would someone get that GUID": nobody types one. A
// caller reads this list and passes back an `id`, and the same list says which of them are
// already taken.
func (api bindingAPI) listPrinters(w http.ResponseWriter, r *http.Request) {
	fleet := api.fleet()
	if fleet == nil {
		refuse(w, r, PrintersUnavailable.New("This process has no connection to the host yet. It answers once the module is configured."))

		return
	}

	reading, cancel := context.WithTimeout(r.Context(), api.timeout())
	defer cancel()

	page, err := fleet.List(reading, deviceapi.ListParams{Limit: deviceapi.MaxPageLimit})
	if err != nil {
		refuse(w, r, PrintersUnavailable.New(err.Error()))

		return
	}

	set := api.live.set()

	out := make([]printerAnswer, 0, len(page.Items))

	for _, device := range page.Items {
		one := printerAnswer{ID: device.ID, Name: device.Name, Model: device.Model, Connected: device.Connected}

		// Every binding that shows this printer, not only the first: a printer on two lights is
		// the case multi-room exists for, and a count of one beside a strip that is also driving
		// it would be the answer this whole change was made to stop giving.
		if held := set.onPrinter(device.ID); len(held) > 0 {
			one.Bound = &boundAnswer{
				ID:         held[0].ID,
				Light:      lightName(held[0].Address),
				Controller: held[0].Address,
				Segment:    held[0].Segment,
				Whole:      held[0].Segment < 0,
			}
			one.BoundCount = len(held)
		}

		out = append(out, one)
	}

	answer(w, http.StatusOK, map[string]any{
		"printers": out,
		"count":    len(out),
		"total":    page.Total,
	})
}

// controllerAnswer is one controller this module drives, and what it gives up for it.
//
// A controller has no life of its own here: it is an address a binding names. It appears when
// the first binding names it and it is gone when the last one leaves, which is why there is no
// create, change or delete for one — those are the binding routes, and a controller route that
// pretended otherwise would be a second way to write the same thing.
type controllerAnswer struct {
	// Name is how these routes address it: the address without its scheme, so that it is one
	// path segment and a browser can hold it.
	Name string `json:"name"`

	// Address is the address as an operator wrote it, and Key the same controller with its
	// scheme and host lowercased, which is what the rules compare.
	Address string `json:"address"`
	Key     string `json:"key"`

	// Whole is the binding that drives the whole controller, and absent when none does.
	// Segments are the borrowed ones, in the operator's order.
	//
	// They are the segments this module has taken, and never the segments the controller holds:
	// the light's own list is what GET /controllers/{name} asks the controller for, and it is
	// the list a segment picker is built from.
	Whole    *string  `json:"whole,omitempty"`
	Segments []int    `json:"segments"`
	Printers []string `json:"printers"`
}

// segmentAnswer is one of a light's own segments, as the light reports it.
//
// ID is the number every write addresses and the number a binding names. Name is the segment's
// own name, `n` on the wire — the one WLED's text effect used to draw, and the name a person
// gave the zone in WLED's own interface. It is empty on a segment that was never named.
//
// The four bounds are the controller's own numbers, in its own spelling, because a caller reads
// them beside the same four keys in /json/state and writes them back in a segment write. On a 2D
// controller start/stop are columns and startY/stopY are rows, and all four are always carried:
// a bound missing its rows would be a bound that reads as the whole panel's width on row zero,
// which is the collapse docs/handoff.md §6 records from a segment write that left them out.
type segmentAnswer struct {
	ID     int    `json:"id"`
	Name   string `json:"name"`
	Start  int    `json:"start"`
	Stop   int    `json:"stop"`
	StartY int    `json:"startY"`
	StopY  int    `json:"stopY"`
}

// controllerReadAnswer is GET /controllers/{name}: the entry the list carries, and the segments
// the light itself reports.
//
// The two are separate members and separate facts. The controller entry is this module's own
// record — the bindings on that address, and which segments they have taken. Segments is the
// controller's answer, which is what a person picks from: a controller knows how many segments it
// holds and what they are called, and nothing this module stores can say that.
//
// SegmentsKnown is true only when the controller answered. A light that does not answer leaves
// it false with the reason in SegmentsUnread and an empty list, rather than a refusal of the
// route: the bindings on that address are still true, and an empty list presented as the light's
// answer would read like a light with no segments at all.
type controllerReadAnswer struct {
	Controller controllerAnswer `json:"controller"`

	SegmentsKnown bool            `json:"segments_known"`
	Segments      []segmentAnswer `json:"segments"`

	// SegmentsUnread says why the list is empty, and is absent when the controller answered.
	SegmentsUnread string `json:"segments_unread,omitempty"`
}

// controllersAnswer is every controller, and what this module can and cannot see about it.
type controllersAnswer struct {
	Controllers []controllerAnswer `json:"controllers"`
	Count       int                `json:"count"`

	// SegmentsKnown is false and means it: a controller's segments are what the device
	// reports, and this module reads them when a writer borrows one. Nothing here asks a
	// controller anything, so the list says which segments are taken and not which exist.
	SegmentsKnown bool `json:"segments_known"`
}

// controllersOf groups the running set the way a watch does, for a caller to read.
func controllersOf(set bindings) ([]controllerAnswer, error) {
	grouped, err := set.controllers()
	if err != nil {
		return nil, err
	}

	out := make([]controllerAnswer, 0, len(grouped))

	for _, ctl := range grouped {
		one := controllerAnswer{
			Name:     nameOfController(ctl.Key),
			Address:  ctl.Address,
			Key:      ctl.Key,
			Segments: []int{},
			Printers: []string{},
		}

		for _, b := range ctl.Bindings {
			if b.Segment < 0 {
				printer := b.DeviceID
				one.Whole = &printer
			} else {
				one.Segments = append(one.Segments, b.Segment)
			}

			// Each printer once, however many segments of this light it is on: this list is what
			// a screen offers as "the printers on this light", and the same machine twice is one
			// machine. One printer on two lights still appears on both lists, because each list
			// is one controller's.
			if !slices.Contains(one.Printers, b.DeviceID) {
				one.Printers = append(one.Printers, b.DeviceID)
			}
		}

		out = append(out, one)
	}

	return out, nil
}

// nameOfController is a controller's name on these routes: its key without the scheme, which
// is one path segment. http://10.0.0.136 becomes 10.0.0.136.
func nameOfController(key string) string {
	if _, rest, found := strings.Cut(key, "://"); found {
		return rest
	}

	return key
}

// listControllers is GET /controllers: every controller the running set drives.
func (api bindingAPI) listControllers(w http.ResponseWriter, r *http.Request) {
	found, err := controllersOf(api.live.set())
	if err != nil {
		refuse(w, r, BindingRefused.New(err.Error()))

		return
	}

	answer(w, http.StatusOK, controllersAnswer{Controllers: found, Count: len(found), SegmentsKnown: false})
}

// readController is GET /controllers/{name}: one controller, by the name the list gives it, and
// the segments the light itself holds.
//
// The segments are the reason this route exists. Every segment picker is empty without them — a
// binding names a segment, and the number a person picks has to be a segment the controller
// really holds, with the name he gave it in WLED's own interface to recognise it by. The answer
// is one read of the controller's /json/state, whose seg array is what it says it holds; that
// document is already parsed by internal/wled for the borrow, and this is the same read.
func (api bindingAPI) readController(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	found, err := controllersOf(api.live.set())
	if err != nil {
		refuse(w, r, BindingRefused.New(err.Error()))

		return
	}

	for _, one := range found {
		if one.Name == name {
			answer(w, http.StatusOK, api.readSegments(r.Context(), one))

			return
		}
	}

	// A light this module knows but drives no binding on. It is still a light, and its own segments
	// are exactly what a person adding a binding to it needs to see — schema version 7's whole
	// point is that a light exists before anything is shown on it (lights.go). Its entry carries no
	// printers and no taken segments, because nothing is bound to it yet.
	if api.tables != nil {
		if light, ok := api.tables.knownLightNamed(name); ok && api.allows(light.Address) {
			answer(w, http.StatusOK, api.readSegments(r.Context(), controllerAnswer{
				Name:     light.name(),
				Address:  light.Address,
				Key:      light.key(),
				Segments: []int{},
				Printers: []string{},
			}))

			return
		}
	}

	names := make([]string, 0, len(found))
	for _, one := range found {
		names = append(names, one.Name)
	}

	sort.Strings(names)

	refuse(w, r, ControllerNotFound.New(fmt.Sprintf(
		"This module drives no controller of that name. It drives %s.", strings.Join(names, ", "))).
		With(fault.StringParam("controller", name)))
}

// readSegments is one controller's entry with the segments its light reports.
//
// A light that does not answer is not a refusal: it is a controller this module drives whose
// segments could not be read, and the answer says so in words with an empty list. That is the
// difference between "this light has no segments" and "nobody asked it" — a picker that offered
// nothing because the light was asleep, with no reason on the page, is the failure this shape
// exists to prevent.
func (api bindingAPI) readSegments(ctx context.Context, one controllerAnswer) controllerReadAnswer {
	said := controllerReadAnswer{Controller: one, Segments: []segmentAnswer{}}

	state, err := api.stateOf(ctx, one.Address)
	if err != nil {
		said.SegmentsUnread = err.Error()

		return said
	}

	said.SegmentsKnown = true
	said.Segments = segmentsOf(state)

	return said
}

// stateOf asks one controller what it is doing now, which is where its segments are.
//
// The read is bounded twice: the client has its own two-second timeout, and the context here is
// the outer bound for a socket that hangs without answering at all. The host gives a route ten
// seconds before the caller gets nothing, so this one must not be able to spend them.
func (api bindingAPI) stateOf(ctx context.Context, address string) (wled.State, error) {
	if !api.allows(address) {
		return wled.State{}, errors.New(refusedReason(address))
	}

	light, err := api.lightFor(address)
	if err != nil {
		return wled.State{}, err
	}

	reading, cancel := context.WithTimeout(ctx, segmentReadTimeout)
	defer cancel()

	return light.State(reading)
}

// segmentsOf is the light's own segments, in its own order, as this surface carries them.
func segmentsOf(state wled.State) []segmentAnswer {
	out := make([]segmentAnswer, 0, len(state.Segments))

	for _, segment := range state.Segments {
		out = append(out, segmentAnswer{
			ID:     segment.ID,
			Name:   segment.Name,
			Start:  segment.Start,
			Stop:   segment.Stop,
			StartY: segment.StartY,
			StopY:  segment.StopY,
		})
	}

	return out
}

// errNoBinding is a change asked of a binding this module is not driving. It is a value rather
// than a sentence so that the handler answers 404 by asking the error, and not by looking for
// words it wrote itself a moment earlier.
var errNoBinding = errors.New("this module drives no binding of that name")

// errNotKept is a change the configured store would not take: no data directory, a directory that
// will not accept the write, or a document this build cannot act on. It is a value for the same
// reason errNoBinding is — the handler answers 503 by asking the error rather than by matching a
// sentence — and it is the one error a binding route must never answer as "applied".
var errNotKept = errors.New("the change was not made, because it could not be written into this module's configuration store")

// appliedNote is what every write on these routes answers with, because the two things a caller
// must not be wrong about are whether the change survives and what it does to the light. It is
// composed from the same persistedNote the tables' writes use, so one sentence answers "where does
// this live" on every editing surface this module serves.
const appliedNote = persistedNote + " It took effect now: the writer was stopped and started again on " +
	"the new set, or stopped for good when the set became empty. A borrowed segment was given back to " +
	"the controller it came from before this answered."
