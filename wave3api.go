// The routes the Layout, Brightness and Overview screens call: a binding's layout, the brightness
// windows, and the pause.
//
// # Why they are routes, and why they are in one file
//
// docs/product-requirements.md §4 deletes the manifest's settings text areas as an editing surface
// and makes this module's own store the thing a person edits, edited through routes like the ones
// bindingsapi.go and tablesapi.go already serve. These three arrive together because they are one
// wave's work and they share one fact: none of them changes what the light draws yet. The layout is
// stored, the windows are stored, the pause is stored with the instant it ends at — and the painter
// still composes from the `layout` setting, still takes its brightness from `brightness_schedule`,
// and does not yet obey a pause. Every write on these routes says so in its note, which is the
// convention wave 2 set and the one thing a caller must not be wrong about.
//
// # The rules every route here follows
//
//   - **One answer object per thing.** A layout is an object with an origin on every value; a window
//     carries the index that addresses it; a pause answers is-it-on, why, and when it ends.
//   - **Every refusal is a registered code** with the value in it, listing what would have been
//     accepted. Nothing here answers with a bare sentence.
//   - **The store is read on every request** and never cached, for the reason tablesapi.go gives: it
//     is one small file, and a copy in memory is a second record that another writer — a person with
//     an editor, the next wave's routes — could put out of date without this process noticing.
//   - **Nothing here touches the light.** Not one request on this surface opens a socket to a
//     controller, which is why every one of them works while a light is unplugged.
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/prusa3d/prusa-connect/sdk/fault"
)

// The notes these routes carry beside the store note, each saying what the write does not yet do.
const (
	// layoutDrawnNote says plainly that the layout table is a record this build does not compose
	// from: the picture on a panel still comes from the `layout` setting.
	layoutDrawnNote = "This build composes the panel from the layout setting and not from this table, so this is the " +
		"record the Layout screen edits and not yet a change on the light."

	// layoutFitNote is §2.5's arithmetic, said in one line on every layout answer because it is the
	// rule a screen has to be able to explain: a layout that will not fit is refused rather than
	// cropped, and the refusal happens where the panel's own height is known.
	layoutFitNote = "Whether a layout fits the panel it is pointed at is judged when the frame is drawn, against the " +
		"panel's own reported height: a bar that leaves no room for the text is refused there rather than cropped."

	// pauseNothingChangedNote is what a resume says when there was nothing to resume, so that a
	// screen can tell "already running" from "no such binding".
	pauseNothingChangedNote = "Nothing was paused, so nothing changed."
)

// now is the clock these routes judge a pause and a window against.
//
// It is a field and not a call to time.Now so that a test can hand the routes a clock it moves; in
// the process it is time.Now. It is never cached into an answer that outlives the request — "is it
// paused now" is computed per read, which is the whole reason the store carries an instant rather
// than a duration.
func (api tableAPI) now() time.Time {
	if api.clock != nil {
		return api.clock()
	}

	return time.Now()
}

// zoneName is the IANA zone the brightness windows are judged in: the `timezone` setting.
//
// The setting stays the host's — §2.8 keeps it as the one thing the operator sets, because it is
// read once at start and a route cannot write the host's settings — while the windows themselves move
// into this module's store. So the zone is read from the settings and the windows from the document,
// and an answer that carries a judged window carries the zone it was judged in beside it.
func (api tableAPI) zoneName() string {
	if api.zone != nil {
		return strings.TrimSpace(api.zone())
	}

	return strings.TrimSpace(settings().Timezone)
}

// judgedZone is the zone the windows are judged in, and why there is none.
//
// It returns nil rather than falling back to UTC: a window judged in this process's clock is a window
// seven hours out on the instance this ships for, and a silent fallback would make that the module's
// quiet default. See brightnesswindows.go, and schedule.go before it.
func (api tableAPI) judgedZone() (*time.Location, error) {
	named := api.zoneName()
	if named == "" {
		return nil, nil
	}

	return loadZone(named)
}

// mountWave3 adds this wave's routes to mux.
func (api tableAPI) mountWave3(mux *http.ServeMux) {
	// A binding's layout: one picture per binding, because a panel and a strip compose differently
	// and two bindings on one light can differ. See bindinglayout.go.
	mux.HandleFunc("GET /bindings/{id}/layout", api.readLayout)
	mux.HandleFunc("PUT /bindings/{id}/layout", api.updateLayout)
	mux.HandleFunc("DELETE /bindings/{id}/layout", api.deleteLayout)
	mux.HandleFunc("GET /lights/{light}/layouts", api.listLayouts)

	// The brightness windows: the list the Brightness screen edits, judged in the timezone setting's
	// zone. See brightnesswindows.go.
	mux.HandleFunc("GET /windows", api.listWindows)
	mux.HandleFunc("POST /windows", api.createWindow)
	mux.HandleFunc("PUT /windows/{index}", api.updateWindow)
	mux.HandleFunc("DELETE /windows/{index}", api.deleteWindow)

	// The pause: the header's pause-everything and one row's, both with an absolute expiry. See
	// pause.go.
	mux.HandleFunc("GET /pause", api.readPause)
	mux.HandleFunc("PUT /pause", api.setGlobalPause)
	mux.HandleFunc("DELETE /pause", api.clearGlobalPause)
	mux.HandleFunc("PUT /bindings/{id}/pause", api.setBindingPause)
	mux.HandleFunc("DELETE /bindings/{id}/pause", api.clearBindingPause)
}

// bindingAndLight is one binding by the device id these routes address it with, and the light it sits
// on.
//
// The light is resolved from the running set rather than from the store, so that a layout written for
// a binding that has since been removed is refused as a binding that does not exist rather than
// silently kept. A binding whose address will not parse cannot be in a running set — bindings.check
// refuses it — so the error here is a set this process could not have started with.
func (api tableAPI) bindingAndLight(id string) (binding, controllerAnswer, bool, error) {
	set := api.live.set()

	found, ok := set.find(id)
	if !ok {
		return binding{}, controllerAnswer{}, false, nil
	}

	key, err := controllerKey(found.Address)
	if err != nil {
		return binding{}, controllerAnswer{}, false, err
	}

	lights, err := controllersOf(set)
	if err != nil {
		return binding{}, controllerAnswer{}, false, err
	}

	for _, one := range lights {
		if one.Key == key {
			return found, one, true, nil
		}
	}

	return found, controllerAnswer{Name: key, Address: found.Address, Key: key}, true, nil
}

// bindingNotFound is the refusal for a device id the running set does not drive. It is the binding
// editor's own code, because a binding here is a binding: one fact, one code.
func bindingNotFound(id string) *fault.Fault {
	return BindingNotFound.New(
		"This module drives no binding of that printer. Read GET /bindings for the ones it does.").
		With(fault.StringParam("printer", strings.TrimSpace(id)))
}

// ---------------------------------------------------------------------------------------------
// The layout
// ---------------------------------------------------------------------------------------------

// layoutRequest is what PUT /bindings/{id}/layout accepts: one layout, every key optional.
//
// Every field is a pointer for the reason storedLayout's are: absent is not zero, and a body that
// leaves `bar_rows` out must not write a bar of no rows. A body carrying none of the eight is refused
// rather than read as "clear the row", because clearing is what DELETE is for and a mistyped key
// would otherwise reset a binding's layout in silence.
//
// `remaining` is accepted and may only be false. The mockup draws the control, so a client will send
// it; this build draws no time remaining, so true is refused rather than ignored — a setting that
// asks for a picture and gets none is a setting that lies.
type layoutRequest struct {
	Bar           *bool   `json:"bar"`
	BarBand       *string `json:"bar_band"`
	BarRows       *int    `json:"bar_rows"`
	Percent       *bool   `json:"percent"`
	PercentScale  *int    `json:"percent_scale"`
	Name          *bool   `json:"name"`
	Remaining     *bool   `json:"remaining"`
	Rotation      *string `json:"rotation"`
	RotateSeconds *int    `json:"rotate_seconds"`
}

// readLayoutRequest is one request body, and what is wrong with it.
func readLayoutRequest(r *http.Request) (layoutRequest, error) {
	var sent layoutRequest

	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(&sent); err != nil {
		return sent, fmt.Errorf(
			"the body is not a layout: %w: write any of %s, and nothing else", err, bindingLayoutKeys())
	}

	if sent.carriesNothing() {
		return sent, fmt.Errorf(
			"the body carries none of %s: write at least one, or delete the binding's layout to put it back to its default",
			bindingLayoutKeys())
	}

	return sent, nil
}

// carriesNothing says whether a body named no key at all.
func (sent layoutRequest) carriesNothing() bool {
	return sent.Bar == nil && sent.BarBand == nil && sent.BarRows == nil && sent.Percent == nil &&
		sent.PercentScale == nil && sent.Name == nil && sent.Remaining == nil && sent.Rotation == nil &&
		sent.RotateSeconds == nil
}

// stored is the row this body writes, for the binding it names.
func (sent layoutRequest) stored(printer string) (storedLayout, error) {
	if sent.Remaining != nil && *sent.Remaining {
		return storedLayout{}, fmt.Errorf(
			"remaining is true, and this build draws no time remaining: nothing would put it on the panel, so it is refused rather than ignored — switch it off, or ask for it as a change that draws it")
	}

	one := storedLayout{
		Printer:       printer,
		Bar:           sent.Bar,
		BarBand:       sent.BarBand,
		BarRows:       sent.BarRows,
		Percent:       sent.Percent,
		PercentScale:  sent.PercentScale,
		Name:          sent.Name,
		Rotation:      sent.Rotation,
		RotateSeconds: sent.RotateSeconds,
	}

	if err := checkStoredLayout(one); err != nil {
		return storedLayout{}, err
	}

	return one, nil
}

// layoutAnswerFor is one binding's answer, from the document as it stands.
func layoutAnswerFor(config storedConfig, b binding, light controllerAnswer) (layoutAnswer, error) {
	rows, err := layoutRows(config)
	if err != nil {
		return layoutAnswer{}, err
	}

	row, stored := layoutFor(rows, b.DeviceID)

	return layoutAnswerOf(b, light, row, stored), nil
}

// readLayout is GET /bindings/{id}/layout: the layout one binding composes with, every value with the
// origin it came from.
func (api tableAPI) readLayout(w http.ResponseWriter, r *http.Request) {
	found, light, ok, err := api.bindingAndLight(r.PathValue("id"))
	if err != nil {
		refuse(w, r, BindingRefused.New(err.Error()))

		return
	}

	if !ok {
		refuse(w, r, bindingNotFound(r.PathValue("id")))

		return
	}

	config, available, err := api.document()
	if err != nil {
		refuse(w, r, StoreUnavailable.New(err.Error()))

		return
	}

	said, err := layoutAnswerFor(config, found, light)
	if err != nil {
		refuse(w, r, StoreUnavailable.New("this module's configuration store cannot be acted on: "+err.Error()))

		return
	}

	body := map[string]any{
		"light":     light.Name,
		"layout":    said,
		"persisted": available,
		"note":      layoutFitNote,
	}

	if !available {
		body["note"] = noStoreNote + " " + layoutFitNote
	}

	answer(w, http.StatusOK, body)
}

// updateLayout is PUT /bindings/{id}/layout: one binding's layout, replacing whatever row it had.
//
// The write does not reach the light, which is why it works while a light is unplugged — exactly the
// moment somebody is standing in front of it with a phone. A row is replaced whole rather than merged,
// for the reason setState replaces a state's: a route that merged would have to guess, for every
// absent field, whether the caller meant "leave it" or "clear it".
func (api tableAPI) updateLayout(w http.ResponseWriter, r *http.Request) {
	found, light, ok, err := api.bindingAndLight(r.PathValue("id"))
	if err != nil {
		refuse(w, r, BindingRefused.New(err.Error()))

		return
	}

	if !ok {
		refuse(w, r, bindingNotFound(r.PathValue("id")))

		return
	}

	sent, err := readLayoutRequest(r)
	if err != nil {
		refuse(w, r, LayoutBodyInvalid.New(err.Error()))

		return
	}

	one, err := sent.stored(found.DeviceID)
	if err != nil {
		refuse(w, r, LayoutRefused.New(err.Error()).
			With(fault.StringParam("printer", found.DeviceID)))

		return
	}

	if _, err = api.tables.revise(func(config *storedConfig) { setLayout(config, one) }); err != nil {
		refuse(w, r, StoreUnavailable.New(err.Error()))

		return
	}

	body := map[string]any{
		"light":     light.Name,
		"layout":    layoutAnswerOf(found, light, one, true),
		"applied":   true,
		"persisted": true,
		"note":      persistedNote + " " + layoutDrawnNote + " " + layoutFitNote,
	}

	if one.Rotation != nil && strings.EqualFold(strings.TrimSpace(*one.Rotation), rotationSideBySide) {
		body["note"] = persistedNote + " " + layoutDrawnNote + " " + rotationStoredNote
	}

	answer(w, http.StatusOK, body)
}

// deleteLayout is DELETE /bindings/{id}/layout: the binding goes back to the built-in default.
//
// A binding with no row is not an error: the answer says removed: false beside the default it was
// already composing with, because a screen distinguishing "already at the default" from "no such
// binding" needs those to be different answers.
func (api tableAPI) deleteLayout(w http.ResponseWriter, r *http.Request) {
	found, light, ok, err := api.bindingAndLight(r.PathValue("id"))
	if err != nil {
		refuse(w, r, BindingRefused.New(err.Error()))

		return
	}

	if !ok {
		refuse(w, r, bindingNotFound(r.PathValue("id")))

		return
	}

	var removed bool

	if _, err = api.tables.revise(func(config *storedConfig) {
		removed = withoutLayout(config, found.DeviceID)
	}); err != nil {
		refuse(w, r, StoreUnavailable.New(err.Error()))

		return
	}

	answer(w, http.StatusOK, map[string]any{
		"light":     light.Name,
		"layout":    layoutAnswerOf(found, light, storedLayout{}, false),
		"removed":   removed,
		"persisted": true,
		"note":      persistedNote + " " + layoutDrawnNote,
	})
}

// listLayouts is GET /lights/{light}/layouts: every binding on one light, each with its layout — the
// shape the Layout screen is drawn from, because the screen is scoped to one light.
func (api tableAPI) listLayouts(w http.ResponseWriter, r *http.Request) {
	light, found, err := api.lightOf(r.PathValue("light"))
	if err != nil {
		refuse(w, r, BindingRefused.New(err.Error()))

		return
	}

	if !found {
		refuse(w, r, api.unknownLight(r.PathValue("light")))

		return
	}

	config, available, err := api.document()
	if err != nil {
		refuse(w, r, StoreUnavailable.New(err.Error()))

		return
	}

	rows, err := layoutRows(config)
	if err != nil {
		refuse(w, r, StoreUnavailable.New("this module's configuration store cannot be acted on: "+err.Error()))

		return
	}

	set := api.live.set()
	on := bindingsOn(set, light)

	out := make([]layoutAnswer, 0, len(on))

	for _, one := range on {
		row, stored := layoutFor(rows, one.DeviceID)
		out = append(out, layoutAnswerOf(one, light, row, stored))
	}

	said := map[string]any{
		"light":     light.Name,
		"layouts":   out,
		"count":     len(out),
		"persisted": available,
		"note":      layoutFitNote,
	}

	if unknown := unknownLayouts(config, set); len(unknown) > 0 {
		said["unknown_layouts"] = unknown
	}

	if !available {
		said["note"] = noStoreNote + " " + layoutFitNote
	}

	answer(w, http.StatusOK, said)
}

// ---------------------------------------------------------------------------------------------
// The brightness windows
// ---------------------------------------------------------------------------------------------

// windowRequest is what POST /windows and PUT /windows/{index} accept: one window, and nothing else.
//
// It is the `brightness_schedule` setting's own line — `from`, `to`, `brightness`, `days` — because
// the store holds the same shape and one parser reads both. See brightnesswindows.go.
type windowRequest struct {
	From       string   `json:"from"`
	To         string   `json:"to"`
	Brightness *int     `json:"brightness"`
	Days       []string `json:"days"`
}

// readWindowRequest is one request body, and what is wrong with it.
func readWindowRequest(r *http.Request) (windowRequest, error) {
	var sent windowRequest

	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(&sent); err != nil {
		return sent, fmt.Errorf("the body is not a window: %w: write %s, and nothing else",
			err, quotedWindows())
	}

	return sent, nil
}

// stored is the window this body describes, checked by the settings' own parser.
func (sent windowRequest) stored(where string) (brightnessWindow, error) {
	one := brightnessWindow{From: sent.From, To: sent.To, Brightness: sent.Brightness, Days: sent.Days}

	if _, err := checkWindowRow(one, where); err != nil {
		return brightnessWindow{}, err
	}

	return one, nil
}

// windowsBody is the answer every window route makes: the whole list, so that a screen redraws from
// one answer rather than reconciling a list against a row. It carries the zone and the instant the
// windows were judged at, because a brightness window an hour wrong is worse than none.
func (api tableAPI) windowsBody(config storedConfig, available bool, extra map[string]any) (map[string]any, error) {
	rows, err := windowsOf(config)
	if err != nil {
		return nil, err
	}

	zone, err := api.judgedZone()
	if err != nil {
		return nil, err
	}

	now := api.now()

	said := windowsAnswerOf(rows, zone, api.zoneName(), now, available)

	body := map[string]any{
		"windows":   said.Windows,
		"count":     said.Count,
		"judged_in": said.JudgedIn,
		"now":       said.Now,
		"persisted": said.Persisted,
	}

	if said.Wins != nil {
		body["wins"] = *said.Wins
		body["level"] = *said.Level
	}

	switch {
	case zone == nil && len(rows) > 0:
		body["note"] = windowsZonedNote
	case zone == nil:
		body["note"] = windowsStoredNote + " " + windowsBorrowedNote
	default:
		body["note"] = windowsStoredNote + " " + windowsBorrowedNote
	}

	if !available {
		body["note"] = noStoreNote
	}

	for key, value := range extra {
		body[key] = value
	}

	return body, nil
}

// listWindows is GET /windows: every window, judged at this instant in the timezone setting's zone.
func (api tableAPI) listWindows(w http.ResponseWriter, r *http.Request) {
	config, available, err := api.document()
	if err != nil {
		refuse(w, r, StoreUnavailable.New(err.Error()))

		return
	}

	body, err := api.windowsBody(config, available, nil)
	if err != nil {
		api.refuseWindows(w, r, err)

		return
	}

	answer(w, http.StatusOK, body)
}

// createWindow is POST /windows: one window added to the end of the list.
//
// The end, because the order is what decides which of two overlapping windows wins and a new window
// at the front would silently take over moments another one covers. Added at the end, it takes over
// nothing.
func (api tableAPI) createWindow(w http.ResponseWriter, r *http.Request) {
	sent, err := readWindowRequest(r)
	if err != nil {
		refuse(w, r, WindowBodyInvalid.New(err.Error()))

		return
	}

	if err = api.requireZone(); err != nil {
		refuse(w, r, WindowRefused.New(err.Error()))

		return
	}

	config, available, err := api.document()
	if err != nil {
		refuse(w, r, StoreUnavailable.New(err.Error()))

		return
	}

	if !available {
		refuse(w, r, StoreUnavailable.New(noStoreNote))

		return
	}

	rows, err := windowsOf(config)
	if err != nil {
		refuse(w, r, StoreUnavailable.New("this module's configuration store cannot be acted on: "+err.Error()))

		return
	}

	one, err := sent.stored(fmt.Sprintf("windows[%d]", len(rows)))
	if err != nil {
		refuse(w, r, WindowRefused.New(err.Error()))

		return
	}

	var written storedConfig

	if written, err = api.tables.revise(func(config *storedConfig) {
		config.Windows = append(config.Windows, one)
	}); err != nil {
		refuse(w, r, StoreUnavailable.New(err.Error()))

		return
	}

	body, err := api.windowsBody(written, true, map[string]any{
		"added": len(written.Windows) - 1,
		"note":  persistedNote + " " + windowsStoredNote + " " + windowsBorrowedNote,
	})
	if err != nil {
		api.refuseWindows(w, r, err)

		return
	}

	answer(w, http.StatusOK, body)
}

// requireZone refuses a window write on a module whose `timezone` setting names no zone, with the
// setting's own sentence: a window that cannot be judged is a window that must not be stored.
func (api tableAPI) requireZone() error {
	zone, err := api.judgedZone()
	if err != nil {
		return err
	}

	if zone == nil {
		_, err = loadZone("")

		return err
	}

	return nil
}

// updateWindow is PUT /windows/{index}: one window replaced whole, in place.
func (api tableAPI) updateWindow(w http.ResponseWriter, r *http.Request) {
	sent, err := readWindowRequest(r)
	if err != nil {
		refuse(w, r, WindowBodyInvalid.New(err.Error()))

		return
	}

	if err = api.requireZone(); err != nil {
		refuse(w, r, WindowRefused.New(err.Error()))

		return
	}

	config, available, err := api.document()
	if err != nil {
		refuse(w, r, StoreUnavailable.New(err.Error()))

		return
	}

	if !available {
		refuse(w, r, StoreUnavailable.New(noStoreNote))

		return
	}

	rows, err := windowsOf(config)
	if err != nil {
		refuse(w, r, StoreUnavailable.New("this module's configuration store cannot be acted on: "+err.Error()))

		return
	}

	index, err := windowIndexOf(rows, r.PathValue("index"))
	if err != nil {
		refuse(w, r, WindowNotFound.New(err.Error()).With(fault.StringParam("index", r.PathValue("index"))))

		return
	}

	one, err := sent.stored(fmt.Sprintf("windows[%d]", index))
	if err != nil {
		refuse(w, r, WindowRefused.New(err.Error()).With(fault.StringParam("index", r.PathValue("index"))))

		return
	}

	var written storedConfig

	if written, err = api.tables.revise(func(config *storedConfig) {
		config.Windows[index] = one
	}); err != nil {
		refuse(w, r, StoreUnavailable.New(err.Error()))

		return
	}

	body, err := api.windowsBody(written, true, map[string]any{
		"replaced": index,
		"note":     persistedNote + " " + windowsStoredNote + " " + windowsBorrowedNote,
	})
	if err != nil {
		api.refuseWindows(w, r, err)

		return
	}

	answer(w, http.StatusOK, body)
}

// deleteWindow is DELETE /windows/{index}: one window taken out, and the windows after it move up one
// place. That shift is stated in the note, because the index is a position and not a minted identity.
func (api tableAPI) deleteWindow(w http.ResponseWriter, r *http.Request) {
	config, available, err := api.document()
	if err != nil {
		refuse(w, r, StoreUnavailable.New(err.Error()))

		return
	}

	if !available {
		refuse(w, r, StoreUnavailable.New(noStoreNote))

		return
	}

	rows, err := windowsOf(config)
	if err != nil {
		refuse(w, r, StoreUnavailable.New("this module's configuration store cannot be acted on: "+err.Error()))

		return
	}

	index, err := windowIndexOf(rows, r.PathValue("index"))
	if err != nil {
		refuse(w, r, WindowNotFound.New(err.Error()).With(fault.StringParam("index", r.PathValue("index"))))

		return
	}

	var written storedConfig

	if written, err = api.tables.revise(func(config *storedConfig) {
		config.Windows = append(config.Windows[:index], config.Windows[index+1:]...)
	}); err != nil {
		refuse(w, r, StoreUnavailable.New(err.Error()))

		return
	}

	body, err := api.windowsBody(written, true, map[string]any{
		"removed": true,
		"note": persistedNote + " This is a list, so the windows after the one removed have each moved up one " +
			"position: their indices are one lower than they were. " + windowsStoredNote,
	})
	if err != nil {
		api.refuseWindows(w, r, err)

		return
	}

	answer(w, http.StatusOK, body)
}

// refuseWindows refuses a read of the window list that the store or the zone made impossible, with the
// code that says which.
func (api tableAPI) refuseWindows(w http.ResponseWriter, r *http.Request, err error) {
	if isRefusal(err) {
		refuse(w, r, WindowRefused.New(err.Error()))

		return
	}

	refuse(w, r, StoreUnavailable.New("this module's configuration store cannot be acted on: "+err.Error()))
}

// isRefusal says whether an error came from a window's own rules rather than from the file. The
// parser's errors are the only ones whose text begins with a line name, and the two are told apart
// here so that a bad window in a hand-edited document reads as a refused window rather than as an
// unavailable store.
func isRefusal(err error) bool {
	return strings.HasPrefix(err.Error(), "windows[")
}

// ---------------------------------------------------------------------------------------------
// The pause
// ---------------------------------------------------------------------------------------------

// pauseRequest is what a pause route accepts: how long, and nothing else.
//
// `for` is the field's name because the moment table already uses it for a length, and one word for
// one idea is what keeps a caller from having to remember which route spells it differently.
type pauseRequest struct {
	For string `json:"for"`
}

// readPauseRequest is one request body, and what is wrong with it.
func readPauseRequest(r *http.Request) (pauseRequest, error) {
	var sent pauseRequest

	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(&sent); err != nil {
		return sent, fmt.Errorf(
			"the body is not a pause: %w: write for, one of %s, and nothing else",
			err, strings.Join(quoted(pauseChoices), ", "))
	}

	if strings.TrimSpace(sent.For) == "" {
		return sent, fmt.Errorf("the body does not say how long the pause lasts: write for, one of %s",
			strings.Join(quoted(pauseChoices), ", "))
	}

	return sent, nil
}

// pause is the row this body asks for, at the scope and for the printer given.
func (sent pauseRequest) pause(scope, printer string, now time.Time) (storedPause, error) {
	chosen := strings.TrimSpace(sent.For)

	if err := checkPauseLength(chosen); err != nil {
		return storedPause{}, err
	}

	until, err := pauseUntil(now, chosen)
	if err != nil {
		return storedPause{}, err
	}

	return storedPause{Scope: scope, Printer: printer, Chosen: chosen, SetAt: now.UTC(), Expires: until}, nil
}

// readPause is GET /pause: every pause there is, whether it is in force, why, and when it ends — and
// what each binding's fate would be, which is where the borrowed-zone exception is visible.
func (api tableAPI) readPause(w http.ResponseWriter, r *http.Request) {
	config, available, err := api.document()
	if err != nil {
		refuse(w, r, StoreUnavailable.New(err.Error()))

		return
	}

	rows, err := pauseRows(config)
	if err != nil {
		refuse(w, r, StoreUnavailable.New("this module's configuration store cannot be acted on: "+err.Error()))

		return
	}

	now := api.now()

	set := api.live.set()

	lights, err := controllersOf(set)
	if err != nil {
		refuse(w, r, BindingRefused.New(err.Error()))

		return
	}

	byKey := make(map[string]controllerAnswer, len(lights))
	for _, one := range lights {
		byKey[one.Key] = one
	}

	// Every binding's reading, in the operator's order, with the light it sits on.
	readings := make([]bindingPauseAnswer, 0, len(set))

	for _, b := range set {
		light := controllerAnswer{Name: b.Address, Address: b.Address}
		if key, keyErr := controllerKey(b.Address); keyErr == nil {
			if held, known := byKey[key]; known {
				light = held
			}
		}

		readings = append(readings, bindingPauseAnswerOf(b, light, rows, now))
	}

	// The stored pauses themselves, with the binding's effect beside each one so that the exception
	// is on the row a screen draws rather than only in the list of bindings.
	pauses := make([]pauseAnswer, 0, len(rows))

	for _, one := range rows {
		said := pauseAnswerOf(one, now)

		if one.Scope == pauseOnBinding {
			if held, ok := set.find(one.Printer); ok {
				said.Effect = pauseEffectOn(held)
			}
		}

		pauses = append(pauses, said)
	}

	body := map[string]any{
		"now":       now.UTC().Format(time.RFC3339),
		"pauses":    pauses,
		"bindings":  readings,
		"persisted": available,
	}

	global, globalOn := globalPause(rows, now)

	pausedBy := make([]string, 0)

	for _, one := range readings {
		if one.Paused && one.DueTo == pauseOnBinding {
			pausedBy = append(pausedBy, one.Printer)
		}
	}

	switch {
	case globalOn:
		body["paused"] = true
		body["why"] = pauseGlobal
		body["forever"] = global.forever()

		if global.Expires != nil {
			body["until"] = global.Expires.UTC().Format(time.RFC3339)
		}
	case len(pausedBy) > 0:
		body["paused"] = true
		body["why"] = pauseOnBinding
		body["paused_by"] = pausedBy
	default:
		body["paused"] = false
	}

	if !available {
		body["note"] = noStoreNote + " " + pauseHandBackNote
	} else {
		body["note"] = pauseHandBackNote
	}

	answer(w, http.StatusOK, body)
}

// setGlobalPause is PUT /pause: the header's pause-everything, for one of the six lengths.
func (api tableAPI) setGlobalPause(w http.ResponseWriter, r *http.Request) {
	api.setPause(w, r, pauseGlobal, "")
}

// setBindingPause is PUT /bindings/{id}/pause: one binding paused, and the rest of the module left
// alone.
func (api tableAPI) setBindingPause(w http.ResponseWriter, r *http.Request) {
	found, _, ok, err := api.bindingAndLight(r.PathValue("id"))
	if err != nil {
		refuse(w, r, BindingRefused.New(err.Error()))

		return
	}

	if !ok {
		refuse(w, r, bindingNotFound(r.PathValue("id")))

		return
	}

	api.setPause(w, r, pauseOnBinding, found.DeviceID)
}

// setPause is the write both scopes share: one row, judged, with every answer saying what this wave
// does and does not do with it.
func (api tableAPI) setPause(w http.ResponseWriter, r *http.Request, scope, printer string) {
	sent, err := readPauseRequest(r)
	if err != nil {
		refuse(w, r, PauseBodyInvalid.New(err.Error()))

		return
	}

	now := api.now()

	one, err := sent.pause(scope, printer, now)
	if err != nil {
		refuse(w, r, PauseRefused.New(err.Error()))

		return
	}

	if _, err = api.tables.revise(func(config *storedConfig) { setPause(config, one) }); err != nil {
		refuse(w, r, StoreUnavailable.New(err.Error()))

		return
	}

	said := pauseAnswerOf(one, now)

	body := map[string]any{
		"pause":     said,
		"paused":    true,
		"persisted": true,
		"note":      persistedNote + " " + pauseStoredNote + " " + pauseHandBackNote,
	}

	// The binding's own fate, so that the exception is answered by the write that a screen has just
	// made rather than only by a read afterwards.
	if scope == pauseOnBinding {
		if held, ok := api.live.set().find(printer); ok {
			said.Effect = pauseEffectOn(held)
			body["pause"] = said
			body["effect"] = said.Effect
		}
	}

	answer(w, http.StatusOK, body)
}

// clearGlobalPause is DELETE /pause: the global pause is taken out of the store.
func (api tableAPI) clearGlobalPause(w http.ResponseWriter, r *http.Request) {
	api.clearPause(w, r, pauseGlobal, "")
}

// clearBindingPause is DELETE /bindings/{id}/pause: one binding's own pause, taken out.
func (api tableAPI) clearBindingPause(w http.ResponseWriter, r *http.Request) {
	found, _, ok, err := api.bindingAndLight(r.PathValue("id"))
	if err != nil {
		refuse(w, r, BindingRefused.New(err.Error()))

		return
	}

	if !ok {
		refuse(w, r, bindingNotFound(r.PathValue("id")))

		return
	}

	api.clearPause(w, r, pauseOnBinding, found.DeviceID)
}

// clearPause is the resume both scopes share.
//
// A scope with nothing set is not an error: the answer says resumed: false, because a screen telling
// "already running" from "no such binding" needs those to be different answers.
func (api tableAPI) clearPause(w http.ResponseWriter, r *http.Request, scope, printer string) {
	var removed bool

	if _, err := api.tables.revise(func(config *storedConfig) {
		removed = withoutPause(config, scope, printer)
	}); err != nil {
		refuse(w, r, StoreUnavailable.New(err.Error()))

		return
	}

	note := pauseResumedNote
	if !removed {
		note = pauseNothingChangedNote
	}

	body := map[string]any{
		"scope":     scope,
		"resumed":   removed,
		"persisted": true,
		"note":      note,
	}

	if printer != "" {
		body["printer"] = printer
	}

	answer(w, http.StatusOK, body)
}
