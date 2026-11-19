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
// # What a change costs
//
// Every write here stops the watch and starts it again on the new set, which is what makes a
// change take effect in the writer that is already talking to the controller: no restart, no
// reinstall, no settings save. The cost is one turn of the light's own frame, and — when the
// controller is unreachable — up to shutdownGrace of waiting for a borrowed segment to be
// given back, because a writer that gave a segment back after the next one captured it would
// hand the operator's own bar back as though it were his.
//
// # What a change does not do
//
// It does not save anything. The settings belong to the host: it merges module.yaml's defaults
// with the instance's config block and sends the result on plugin.configure, and a module has
// no call that writes them back. So a change lives until the process restarts or a person
// saves the settings pane, and every write here says so in its answer rather than letting
// somebody build on a change they will lose.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
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

	// One binding, named by the device id of the printer it shows.
	mux.HandleFunc("GET /bindings/{id}", api.readBinding)
	mux.HandleFunc("PUT /bindings/{id}", api.updateBinding)
	mux.HandleFunc("DELETE /bindings/{id}", api.deleteBinding)

	// A binding the set does not hold yet.
	mux.HandleFunc("POST /bindings", api.createBinding)
}

// bindingAnswer is one binding as a caller reads it: the two things that name it, the number
// a person needs to know how much of the controller it takes, and the name of the machine so
// that a list of device ids is not what an operator has to match against the printers screen.
type bindingAnswer struct {
	// ID is the binding's name on these routes: the host's device id for the printer it
	// shows. A printer appears in at most one binding, so it is unique by the module's own
	// rules, and it is the one fact about a binding the host minted rather than a person.
	ID string `json:"id"`

	// Position is where the binding sits in the operator's order, counting from one.
	Position int `json:"position"`

	// Printer is the device id, and PrinterName the name an operator gave the machine. The
	// name is absent when the host could not be read: a binding is this module's own record
	// and a printer's name is the host's, so a host that will not answer must not make the
	// list unreadable.
	Printer     string `json:"printer"`
	PrinterName string `json:"printer_name,omitempty"`

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

	// Persisted is false and means it: nothing on this surface writes the host's settings, so
	// what a caller changes here lasts until the process restarts.
	Persisted bool `json:"persisted"`

	// NamesReadFailed says why a printer's name is missing, absent when the host answered.
	NamesReadFailed string `json:"names_read_failed,omitempty"`
}

// answerOf renders one binding for a caller, with the printer's name when there is one.
func answerOf(b binding, position int, names map[string]string) bindingAnswer {
	return bindingAnswer{
		ID:          b.DeviceID,
		Position:    position,
		Printer:     b.DeviceID,
		PrinterName: names[b.DeviceID],
		Controller:  b.Address,
		Segment:     b.Segment,
		Whole:       b.Segment < 0,
		Brightness:  b.Brightness,
	}
}

// positionOf is where one binding sits in a set, counting from one, and zero for one the set
// does not hold.
func positionOf(set bindings, id string) int {
	for i, b := range set {
		if b.DeviceID == id {
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
	set := api.live.set()
	names, why := api.printerNames(r.Context())

	answer(w, http.StatusOK, bindingsAnswer{
		Bindings:        answersOf(set, names),
		Count:           len(set),
		Source:          api.live.source(),
		Persisted:       false,
		NamesReadFailed: why,
	})
}

// readBinding is GET /bindings/{id}: one binding, by the device id of the printer it shows.
func (api bindingAPI) readBinding(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	set := api.live.set()

	found, ok := set.find(id)
	if !ok {
		refuse(w, r, BindingNotFound.New(
			"This module drives no binding of that printer. Read GET /bindings for the ones it does.").
			With(fault.StringParam("printer", id)))

		return
	}

	names, why := api.printerNames(r.Context())

	foundAnswer := answerOf(found, positionOf(set, found.DeviceID), names)

	answer(w, http.StatusOK, map[string]any{
		"binding":           foundAnswer,
		"persisted":         false,
		"names_read_failed": why,
	})
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
func toSetting(deviceID string, asked bindingRequest) binding {
	one := bindingSetting{Controller: asked.Controller, Segment: asked.Segment, Brightness: asked.Brightness}

	return binding{
		DeviceID:   deviceID,
		Address:    strings.TrimSpace(one.Controller),
		Segment:    one.segment(),
		Brightness: one.brightness(),
	}
}

// createBinding is POST /bindings: one more printer, shown from now on.
func (api bindingAPI) createBinding(w http.ResponseWriter, r *http.Request) {
	asked, err := readBindingRequest(r)
	if err != nil {
		refuse(w, r, BindingBodyInvalid.New(err.Error()))

		return
	}

	deviceID, err := api.resolveName(r.Context(), asked.Printer)
	if err != nil {
		refuse(w, r, BindingRefused.New(err.Error()))

		return
	}

	one := toSetting(deviceID, asked)

	set, err := api.live.revise(api.watchContext(), func(current bindings) (bindings, error) {
		if _, held := current.find(one.DeviceID); held {
			return nil, fmt.Errorf(
				"printer %s is already shown by a binding: one printer is shown by one binding, so change that one with PUT /bindings/%s",
				one.DeviceID, one.DeviceID)
		}

		return current.with(one), nil
	})
	if err != nil {
		refuse(w, r, BindingRefused.New(err.Error()))

		return
	}

	names, why := api.printerNames(r.Context())

	answer(w, http.StatusCreated, map[string]any{
		"binding":           answerOf(one, positionOf(set, one.DeviceID), names),
		"applied":           true,
		"persisted":         false,
		"names_read_failed": why,
		"note":              appliedNote,
	})
}

// updateBinding is PUT /bindings/{id}: the same printer, on a different controller or segment,
// or at a different brightness.
//
// The printer is the binding's name here, so a body naming a different one is refused rather
// than quietly moving the id: the answer a caller would look for afterwards is not the one
// that exists.
func (api bindingAPI) updateBinding(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	asked, err := readBindingRequest(r)
	if err != nil {
		refuse(w, r, BindingBodyInvalid.New(err.Error()))

		return
	}

	if strings.TrimSpace(asked.Printer) != "" && strings.TrimSpace(asked.Printer) != id {
		refuse(w, r, BindingRefused.New(fmt.Sprintf(
			"a binding is named by the printer it shows and this one shows %s: to show %s instead, delete this binding and add the other",
			id, strings.TrimSpace(asked.Printer))))

		return
	}

	one := toSetting(id, asked)

	set, err := api.live.revise(api.watchContext(), func(current bindings) (bindings, error) {
		if _, held := current.find(id); !held {
			return nil, fmt.Errorf("%w: %s", errNoBinding, id)
		}

		return current.with(one), nil
	})
	if err != nil {
		if errors.Is(err, errNoBinding) {
			refuse(w, r, BindingNotFound.New(err.Error()).With(fault.StringParam("printer", id)))

			return
		}

		refuse(w, r, BindingRefused.New(err.Error()))

		return
	}

	names, why := api.printerNames(r.Context())

	answer(w, http.StatusOK, map[string]any{
		"binding":           answerOf(one, positionOf(set, id), names),
		"applied":           true,
		"persisted":         false,
		"names_read_failed": why,
		"note":              appliedNote,
	})
}

// deleteBinding is DELETE /bindings/{id}: this printer stops being shown.
//
// The last binding may be deleted. It is a thing an operator may want — the light stops being
// written to, and is left as the last frame found it — and it is better said than done by
// removing a setting and restarting the host.
func (api bindingAPI) deleteBinding(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	var left bindings

	_, err := api.live.revise(api.watchContext(), func(current bindings) (bindings, error) {
		kept, held := current.without(id)
		if !held {
			return nil, fmt.Errorf("%w: %s", errNoBinding, id)
		}

		left = kept

		return kept, nil
	})
	if err != nil {
		if errors.Is(err, errNoBinding) {
			refuse(w, r, BindingNotFound.New(err.Error()).With(fault.StringParam("printer", id)))

			return
		}

		refuse(w, r, BindingRefused.New(err.Error()))

		return
	}

	answer(w, http.StatusOK, map[string]any{
		"deleted":   id,
		"remaining": answersOf(left, nil),
		"applied":   true,
		"driving":   len(left) > 0,
		"persisted": false,
		"note":      appliedNote,
	})
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

	// Bound is the binding that already shows this printer, and absent when none does. It is
	// here so that a picker can leave out the printers this module is already showing rather
	// than offering a second binding the rules would refuse.
	Bound *boundAnswer `json:"bound,omitempty"`
}

// boundAnswer is where a printer is already shown.
type boundAnswer struct {
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

		if held, ok := set.find(device.ID); ok {
			one.Bound = &boundAnswer{Controller: held.Address, Segment: held.Segment, Whole: held.Segment < 0}
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

			one.Printers = append(one.Printers, b.DeviceID)
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
var errNoBinding = errors.New("this module drives no binding of that printer")

// appliedNote is what every write on these routes answers with, because the one thing a
// caller must not be wrong about is whether the change survives a restart.
const appliedNote = "This took effect now: the writer was stopped and started again on the new set. " +
	"It is not saved. The host owns the settings, so restarting this module or saving its settings pane puts back what the settings say."
