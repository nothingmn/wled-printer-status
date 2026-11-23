// The lights this module knows: the list, adding one by its address, and taking one off.
//
// # Why a light is a thing of its own
//
// Until this file there was no such thing as a light in this module. A light existed because a
// binding named its address (bindings.go), so "the lights" were the bindings grouped by controller
// — which meant a light with no printer on it could not be said at all, and a person had nowhere to
// put one. docs/product-requirements.md §2.6 has always said "one card per light"; this is the list
// that makes that sentence true, and the reason the Lights screen's cards are cards per light
// rather than cards per binding wearing a light's name.
//
// So the light is a row in this module's own store (storedLight, schema version 7): the address as
// the operator wrote it, and what the light said about itself the last time it was read — the name
// it calls itself, its firmware, how many pixels it drives, and whether it is a panel or a strip.
//
// # Adding one is a read, and the read is the point
//
// Adding a light probes it first: GET /json/info, through the client in internal/wled that every
// other read of a light in this module goes through, and not a second HTTP path. Nothing is stored
// until something at that address answered and the answer was a WLED controller's — a typo, a
// printer's own web page and a NAS are refused in words, because a phantom light in the document
// is a card that can never fill and a name nobody can match against the network.
//
// A light does not need a printer to be free. A light you own is a fact whether or not anything is
// shown on it, which is exactly what §2.6's card-per-light was for.
//
// # Taking one off
//
// Removing a light is allowed only when no binding still names it, and says which bindings do when
// it cannot. A binding is a promise that something is being written to that light; deleting the
// light out from under it would leave the writer with an address the list does not hold, and a
// person deleting a card to stop a print being shown would be deleting the wrong card.
//
// # What this file does not do
//
// No discovery. Finding lights on the network is not what this build does, and the reason is measured
// rather than guessed: the module runs inside Prusa Connect's container on a Docker bridge, and mDNS
// reaches that bridge only when the host reflects it there — which the operator's host now does, with
// a browse from inside the container listing nineteen lights by name and address
// (docs/mdns-in-docker.md). What is missing is the browse in this process, so the Lights screen says
// what discovery needs in one sentence, and points at the typed address, which works now.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/prusa3d/prusa-connect/sdk/fault"

	"github.com/nothingmn/wled-printer-status/internal/wled"
)

const (
	// lightProbeTimeout bounds one read of a light for its facts. The client's own timeout is two
	// seconds, so this is the outer bound for a socket that accepts a connection and then says
	// nothing: adding a light is a request a person is waiting on, and the host gives a route ten
	// seconds before the caller gets nothing at all.
	lightProbeTimeout = 3 * time.Second

	// lightKindPanel and lightKindStrip are the two shapes a WLED controller can be, in the words
	// the screens use. A controller that publishes a matrix is a panel; one that does not is a
	// strip, whether it drives 48 pixels or 300.
	lightKindPanel = "panel"
	lightKindStrip = "strip"
)

// storedLight is one light this module knows, as the store holds it: the address, and what the
// light said about itself when it was last read.
//
// The facts are what the light answered, not what the module decided. A light that has never been
// read has the address and nothing else, and `probed_at` absent is how a reader tells that apart
// from a light that answered with a blank name: the migration from schema version 6 writes exactly
// such a row for every address a binding already names, and the start that follows fills them in
// (fillLightFacts).
type storedLight struct {
	// Address is the light's base URL, as the operator wrote it, with a scheme: http://10.0.0.253.
	// An address typed without one is read as http before it is stored, so two rows cannot be one
	// light spelled two ways.
	Address string `json:"address"`

	// CallsItself is the name the light reports for itself, "name" on the wire. It is the light's
	// own, not the operator's: WLED's interface is where it is set, and this module never writes
	// it.
	CallsItself string `json:"calls_itself,omitempty"`

	// Firmware is the version the light reports, "ver" on the wire.
	Firmware string `json:"firmware,omitempty"`

	// Pixels is how many LEDs it drives, "leds.count" on the wire.
	Pixels int `json:"pixels,omitempty"`

	// Kind is lightKindPanel or lightKindStrip: whether it published a matrix.
	Kind string `json:"kind,omitempty"`

	// Width and Height are the panel's geometry in pixels, and both are zero on a strip. They are
	// kept beside Kind because "256 x 64" is how a person recognises the panel in the office, and
	// a pixel count of 16,384 alone is not.
	Width  int `json:"width,omitempty"`
	Height int `json:"height,omitempty"`

	// ProbedAt is when these facts were read, in UTC and RFC 3339. Absent means the light has
	// never answered here — which is the row a migration writes — and a page says so with the age
	// of its last reading rather than drawing a guess.
	ProbedAt string `json:"probed_at,omitempty"`

	// Ignored is this module's own decision about the light, and not the light's: it says the
	// operator does not want this one offered anywhere. The row stays, and the light stays
	// readable, because "push it out of the way" is not "forget it" — a light ignored by mistake
	// has to be findable and bringable back.
	//
	// It is written on every row rather than left out to mean false: the flag belongs to the
	// document, and a person opening config.json to see why a light is missing from a screen
	// should find the word. Schema version 8 introduced it; the upgrade from 7 writes it false on
	// every light, which is what every light then was.
	Ignored bool `json:"ignored"`

	// A light cannot be ignored while a binding drives it (lights.go, ignoreLight). That is the
	// rule that keeps this flag from being able to hide something the module is writing to.
}

// storedLightV7 is one light as schema version 7 wrote it: the address and what the light said
// about itself, and no `ignored` flag, because version 7 had no way to put a light out of the way.
//
// A type of its own rather than a pointer field, for the reason storedLayoutV3 is: the upgrades
// decode strictly, and a version-7 row read into the current shape would be the current shape
// wearing a version it did not have.
type storedLightV7 struct {
	Address     string `json:"address"`
	CallsItself string `json:"calls_itself,omitempty"`
	Firmware    string `json:"firmware,omitempty"`
	Pixels      int    `json:"pixels,omitempty"`
	Kind        string `json:"kind,omitempty"`
	Width       int    `json:"width,omitempty"`
	Height      int    `json:"height,omitempty"`
	ProbedAt    string `json:"probed_at,omitempty"`
}

// atVersion7 is this row as version 7 wrote it, for the step from 6 to 7: the same facts and no
// flag. It is a conversion and not a second row — every field travels unchanged.
func (l storedLight) atVersion7() storedLightV7 {
	return storedLightV7{
		Address:     l.Address,
		CallsItself: l.CallsItself,
		Firmware:    l.Firmware,
		Pixels:      l.Pixels,
		Kind:        l.Kind,
		Width:       l.Width,
		Height:      l.Height,
		ProbedAt:    l.ProbedAt,
	}
}

// toStored is this row at version 8: the same facts, and not ignored. That is the whole of what
// the step from 7 decides, and it is not an invention — version 7 had no flag, so no light it
// describes was ignored.
func (l storedLightV7) toStored() storedLight {
	return storedLight{
		Address:     l.Address,
		CallsItself: l.CallsItself,
		Firmware:    l.Firmware,
		Pixels:      l.Pixels,
		Kind:        l.Kind,
		Width:       l.Width,
		Height:      l.Height,
		ProbedAt:    l.ProbedAt,
		Ignored:     false,
	}
}

// key is this light's identity for the rules that must not see one light twice. It is the same rule
// controllerKey applies to a binding's address, and deliberately so: a light on these routes and a
// controller on the binding routes are one thing, so two spellings of one address must not make
// two rows here when they make one controller there.
func (l storedLight) key() string { return lightKey(l.Address) }

// name is this light's name on the routes: its address without the scheme, which is the name
// GET /controllers gives a controller and the name the Layout and Events screens address a light
// by. http://10.0.0.253 is 10.0.0.253.
func (l storedLight) name() string { return nameOfController(l.key()) }

// read says whether these facts came from the light at all.
func (l storedLight) read() bool { return strings.TrimSpace(l.ProbedAt) != "" }

// lightKey is one light's identity, and it never fails: it is used on documents already written,
// where a refusal would be a start that cannot read its own store. A stored address has a scheme —
// normaliseLightAddress sees to that — so the parse below is the ordinary case, and the fallback is
// the same string trimmed and lowercased, which is still one answer per spelling.
func lightKey(address string) string {
	trimmed := strings.TrimRight(strings.TrimSpace(address), "/")

	parsed, err := url.Parse(trimmed)
	if err == nil && parsed.Host != "" {
		scheme := parsed.Scheme
		if scheme == "" {
			scheme = "http"
		}

		return strings.ToLower(scheme + "://" + parsed.Host)
	}

	return strings.ToLower(trimmed)
}

// normaliseLightAddress is a typed address as this module stores it, or the refusal that says what
// is wrong with it.
//
// An address typed without a scheme is read as http, because that is how a person writes a light's
// address and what a WLED controller answers on. Everything after that is the client's own rule
// (wled.New), not a second copy of it here: the address this module stores is one it can build a
// client for, which is the same check a binding's address already goes through.
func normaliseLightAddress(address string) (string, error) {
	typed := strings.TrimSpace(address)

	if typed == "" {
		return "", errors.New("no address was given: write the light's IP address, such as 10.0.0.253")
	}

	if !strings.Contains(typed, "://") {
		typed = "http://" + typed
	}

	base := strings.TrimRight(typed, "/")

	if _, err := wled.New(base); err != nil {
		return "", fmt.Errorf("%q is not an address a light can answer at: %w", address, err)
	}

	return base, nil
}

// lightOf is one light as the store keeps it, from what the light answered and when.
func lightOf(address string, info wled.Info, at time.Time) storedLight {
	kind := lightKindStrip
	if info.MatrixHeight > 0 {
		kind = lightKindPanel
	}

	return storedLight{
		Address:     address,
		CallsItself: strings.TrimSpace(info.Name),
		Firmware:    strings.TrimSpace(info.Version),
		Pixels:      info.LEDCount,
		Kind:        kind,
		Width:       info.MatrixWidth,
		Height:      info.MatrixHeight,
		ProbedAt:    at.UTC().Format(time.RFC3339),
	}
}

// answeredAsALight is whether what came back is a WLED controller at all.
//
// The test is the firmware, and it is the light's own word: every WLED build answers /json/info with
// "ver" and the rest of the document is tolerant of whatever a firmware adds. Something else at
// that address — a printer's page, a NAS, an API of another kind — either answers no JSON at all,
// which the client refuses, or answers JSON with no firmware in it, which is this. A pixel count of
// zero is not the test: a controller somebody has not configured yet still answers with its name
// and its version, and refusing it would be refusing a light that is merely empty.
func answeredAsALight(info wled.Info) (bool, string) {
	if strings.TrimSpace(info.Version) != "" {
		return true, ""
	}

	said := strings.TrimSpace(info.Name)
	if said == "" {
		said = "no name"
	}

	return false, fmt.Sprintf(
		"something answered at that address, but it is not a WLED light: its /json/info carries no firmware version (it calls itself %q). A WLED controller always answers there with its version, so this is another device on the network.",
		said)
}

// seededLights is the lights list a migration writes from the bindings it already has: one row per
// address a binding names, in the operator's order, with no facts on it.
//
// No facts is not laziness. A migration runs before the watch starts, and one that dialled a
// controller would make a start depend on the network: a light that was unplugged would either hold
// the upgrade up or have a name and a firmware written into the document that nobody observed.
// The address is the fact the document being migrated does have, so it is the fact this writes; the
// start fills the rest in from the light itself (fillLightFacts), and until it does, a card shows
// the address and says its facts have not been read yet.
func seededLights(entries []json.RawMessage) ([]storedLight, error) {
	lights := make([]storedLight, 0, len(entries))
	seen := make(map[string]bool, len(entries))

	for i, raw := range entries {
		var line storedEntry

		if err := decodeStrictly(raw, &line); err != nil {
			return nil, fmt.Errorf("read binding %d for the light it names: %w", i+1, err)
		}

		address := strings.TrimRight(strings.TrimSpace(line.Controller), "/")
		if address == "" {
			continue
		}

		key := lightKey(address)
		if seen[key] {
			continue
		}

		seen[key] = true
		lights = append(lights, storedLight{Address: address})
	}

	return lights, nil
}

// seededLightsOrNothing is seededLights for the one caller that has just written those lines itself:
// the first start of an install, where the entries came out of storeEntries a line above. A line
// there that cannot be read back is a fault in this build and not a fact about somebody's document,
// and a start must still come up — with no rows, which the fill and the add route then correct.
func seededLightsOrNothing(entries []json.RawMessage) []storedLight {
	lights, err := seededLights(entries)
	if err != nil {
		return []storedLight{}
	}

	return lights
}

// knownLightNamed is the light this module knows by its route name, and false for a name it does
// not know. The store is asked, not the running set: a light with no binding on it is still a light
// this module knows, which is the whole of schema version 7.
func (t *configTables) knownLightNamed(named string) (storedLight, bool) {
	wanted := strings.TrimSpace(named)
	if wanted == "" {
		return storedLight{}, false
	}

	config, err := t.load()
	if err != nil {
		return storedLight{}, false
	}

	for _, light := range config.Lights {
		if strings.EqualFold(light.name(), wanted) {
			return light, true
		}
	}

	return storedLight{}, false
}

// knownLightAt is the light this module knows at one address, and false for an address it does not
// know. Two spellings of one address are one light, by the same key the rules use.
func (t *configTables) knownLightAt(address string) (storedLight, bool) {
	key := lightKey(address)

	config, err := t.load()
	if err != nil {
		return storedLight{}, false
	}

	for _, light := range config.Lights {
		if light.key() == key {
			return light, true
		}
	}

	return storedLight{}, false
}

// lightProber is the one read this file makes of a light: its facts. *wled.Client is the only
// implementation in the process; the interface exists so that a test can add a light without a
// socket, and so that nothing here can reach a light's pixels.
type lightProber interface {
	Info(ctx context.Context) (wled.Info, error)
}

// lightsAPI is the Lights screen's own surface: the five declared slots, the lights that fill them,
// and the ignore flag.
//
// There is no route that adds a light and none that removes one. The manifest's five `url` options
// are the only addresses this module may contact (declared.go), and the extension's own settings
// are where an operator fills them in; a screen that could add a sixth address would be a screen
// that made the egress declaration false. Binding, ignoring and testing stay here, because all
// three act on a light the declaration already names.
//
// It is a value with its own fields for the reason deviceAPI is: a test runs the same handlers over
// a store of its own and a light that needs no network.
type lightsAPI struct {
	// tables is the store the list lives in. Nil means this module was granted no data directory,
	// and then a light cannot be kept — see StoreUnavailable.
	tables *configTables

	// newLight builds the client for one address. Nil means wled.New, which is what the process
	// uses; a test supplies a fake.
	newLight func(address string) (lightProber, error)

	// probe bounds one read of a light. Zero means lightProbeTimeout.
	probe time.Duration

	// now dates a reading. Nil means time.Now.
	now func() time.Time

	// slots is the declaration in force: five entries, empty ones included. Nil means this
	// instance has no declaration — a module built before the manifest carried options, and the
	// tests that supply their own store — and the list is then the store's rows, as it was before
	// the cap.
	slots func() []declaredLight

	// permits answers whether one address may be contacted. Nil means the same as slots' nil: no
	// declaration is in force. The process sets it to addressDeclared, so an installed module
	// always answers from its options and an empty answer permits nothing.
	permits func(string) bool
}

// declaredSlots is the five slots this screen draws, or nil for an instance that has no
// declaration in force.
func (api lightsAPI) declaredSlots() []declaredLight {
	if api.slots != nil {
		return api.slots()
	}

	if api.permits != nil {
		return declaredNow()
	}

	return nil
}

// allowed answers whether one address may be contacted, with the same nil rule as the two fields.
func (api lightsAPI) allowed(address string) bool {
	if api.permits == nil {
		return true
	}

	return api.permits(address)
}

// probeFor is the client for one address: the test's when one was supplied, and the process's own
// otherwise.
func (api lightsAPI) probeFor(address string) (lightProber, error) {
	if api.newLight != nil {
		return api.newLight(address)
	}

	return wled.New(address)
}

// waited is how long one probe may take.
func (api lightsAPI) waited() time.Duration {
	if api.probe > 0 {
		return api.probe
	}

	return lightProbeTimeout
}

// at is the instant a reading is dated at.
func (api lightsAPI) at() time.Time {
	if api.now != nil {
		return api.now()
	}

	return time.Now()
}

// mount adds the Lights screen's routes to mux.
//
// `/lights` is the list and not `/devices`, because a device on this host means a printer session:
// the screen was called Devices and a person looking for a light did not find one, which is the
// report this answers. The names a light answers to are unchanged — GET /controllers, GET
// /devices/{name}/presets, POST /devices/{name}/identify — because those routes are about the same
// light and renaming them would break every screen that already holds a path.
func (api lightsAPI) mount(mux *http.ServeMux) {
	// The five declared slots, and every light of those that has answered. A binding is not
	// needed for a light to be listed: the declaration is what makes it a light this module may
	// contact, and a slot is filled in the extension's own settings rather than here.
	mux.HandleFunc("GET /lights", api.listLights)

	// A light put out of the way — kept, and left out of every list that offers one — and the
	// same light brought back. The two verbs are the two decisions, in the shape this surface
	// already uses: the light is the noun, and what happens to it is the method.
	mutating(mux, "POST /lights/{name}/ignore", actionConfigureResource, api.ignoreLight)
	mutating(mux, "DELETE /lights/{name}/ignore", actionConfigureResource, api.unignoreLight)
}

// lightAnswer is one light as a caller reads it: its name on these routes, its address, and what it
// said about itself.
type lightAnswer struct {
	// Name is its name on the routes — the address without the scheme — and it is what the screens
	// address this light by on every other route.
	Name string `json:"name"`

	// Address is the base URL the operator wrote.
	Address string `json:"address"`

	// CallsItself, Firmware, Pixels, Kind, Width and Height are the light's own answer. They are
	// absent together when the light has never been read here, and FactsKnown says which of the two
	// a caller is looking at — an empty name is a light that answered with no name, not a light
	// nobody has read.
	CallsItself string `json:"calls_itself,omitempty"`
	Firmware    string `json:"firmware,omitempty"`
	Pixels      int    `json:"pixels"`
	Kind        string `json:"kind,omitempty"`
	Width       int    `json:"width,omitempty"`
	Height      int    `json:"height,omitempty"`

	// ProbedAt is when those facts were read, absent until the light has answered here.
	ProbedAt string `json:"probed_at,omitempty"`

	// FactsKnown is true when the facts above are the light's own answer.
	FactsKnown bool `json:"facts_known"`

	// Ignored is this module's own decision about the light: it is kept here, and left out of
	// every list that offers a light to act on — the Overview's gap list, the pickers on
	// Bindings, and the Lights screen's own In-Use and Available buckets. It is on this answer
	// rather than left out of it, because the Ignored bucket is built from this list: a light
	// that vanished from GET /lights could not be brought back.
	Ignored bool `json:"ignored"`

	// Note is the one thing a card must not leave a reader to assume, when there is one.
	Note string `json:"note,omitempty"`
}

// answerOf is one stored light as the routes read it.
func lightAnswerOf(light storedLight) lightAnswer {
	said := lightAnswer{
		Name:        light.name(),
		Address:     light.Address,
		CallsItself: light.CallsItself,
		Firmware:    light.Firmware,
		Pixels:      light.Pixels,
		Kind:        light.Kind,
		Width:       light.Width,
		Height:      light.Height,
		ProbedAt:    light.ProbedAt,
		FactsKnown:  light.read(),
		Ignored:     light.Ignored,
	}

	if !said.FactsKnown {
		said.Note = "This light has not been read by this module yet, so its own name, its firmware and its shape are not known here. Its address is. It is read as soon as it answers."
	}

	return said
}

// lightSlot is one of the five declared addresses, as the Lights screen draws it: the slot number,
// whether an address fills it, and — when it does — the light's own answer if it has ever been read
// here.
//
// It is the declaration and not the store. A slot is empty because the operator has not filled that
// option in yet, which is a different fact from a light this module has never managed to read, and
// the two carry different notes.
type lightSlot struct {
	// Slot is the slot's number, counting from one: `light1_address` is slot 1.
	Slot int `json:"slot"`

	// Filled says the option names an address.
	Filled bool `json:"filled"`

	// Address is the address as this module dials it, absent on an empty slot.
	Address string `json:"address,omitempty"`

	// CallsItself, Firmware, Pixels, Kind, Width and Height are the light's own answer, absent
	// together until it has been read here.
	CallsItself string `json:"calls_itself,omitempty"`
	Firmware    string `json:"firmware,omitempty"`
	Pixels      int    `json:"pixels,omitempty"`
	Kind        string `json:"kind,omitempty"`
	Width       int    `json:"width,omitempty"`
	Height      int    `json:"height,omitempty"`

	// ProbedAt is when those facts were read, absent until the light has answered.
	ProbedAt string `json:"probed_at,omitempty"`

	// FactsKnown is true when the facts above are the light's own answer.
	FactsKnown bool `json:"facts_known"`

	// Note is what a reader must not have to assume: an empty slot says where it is filled in, and
	// a filled one that has not answered says so.
	Note string `json:"note,omitempty"`
}

// lightsAnswer is GET /lights.
type lightsAnswer struct {
	// Slots is always the five declared slots, in order, empty ones included. A module whose
	// settings name none answers five empty entries rather than nothing: the cap is the shape of
	// the feature, and a person has to be able to see the slot he is meant to fill in.
	Slots []lightSlot `json:"slots"`

	// SlotCount is how many slots the manifest declares.
	SlotCount int `json:"slot_count"`

	Lights []lightAnswer `json:"lights"`
	Count  int           `json:"count"`

	// Persisted says the list is the one in this module's own store, so the next start reads it.
	Persisted bool `json:"persisted"`

	// Note is the sentence the screen shows about finding lights on the network: there is no browse
	// in this build, and what a browse needs is the host carrying mDNS across the Docker bridge —
	// measured working on the operator's host, and written down in docs/mdns-in-docker.md. The note
	// names that requirement rather than promising the feature, and points at the typed address,
	// which works now.
	Note string `json:"note"`
}

const (
	// lightsPersistedNote is read by a page that has at least one filled slot.
	lightsPersistedNote = "The five addresses this module may contact are the extension's own settings, light1_address to " +
		"light5_address. A light is bound, ignored and tested here; its address is filled in there. A settings save " +
		"restarts this module."

	// lightsEmptyNote is the same fact for a page with no filled slot at all.
	lightsEmptyNote = "None of the five light addresses is filled in, so this module is driving nothing. Open the " +
		"extension's own settings and set light1_address to light5_address — one WLED controller's address each, such as " +
		"http://10.0.0.136. The addresses are a fixed five-slot declaration because the manifest's egress block cannot name " +
		"a host this module learns at run time (prusa3d/prusa-connect#49 and #58); filling a slot is what makes that host " +
		"reachable, and nothing else is."

	// lightsUpgradeNote is the one-time migration from a release that kept addresses in its store.
	lightsUpgradeNote = "Upgrading from 0.8.0? This module can no longer read light addresses from its own store: copy " +
		"each light's address into the extension's own settings — light1_address to light5_address, five slots — and this " +
		"screen will fill them in. Until you do, nothing is driven, nothing is probed and nothing is written to."
)

// listLights is GET /lights: the five declared slots, and the lights that fill them.
//
// It makes no read of any light. A light that is unplugged must not make this list slow or empty:
// what it said when it last answered is the row, and `probed_at` is the age of that reading.
//
// Every row in `lights` is a declared address. A store row naming anything else is not a light this
// module may contact, and it is left out rather than drawn as a card that could never be read: the
// declaration is the list. An instance with no declaration in force — the shape of every build
// before 0.9.0, and of the tests — answers the store's rows as it always did.
func (api lightsAPI) listLights(w http.ResponseWriter, r *http.Request) {
	if api.tables == nil {
		slots := api.slotAnswers(nil)

		answer(w, http.StatusOK, lightsAnswer{Lights: []lightAnswer{}, Count: 0, Persisted: false,
			Slots: slots, SlotCount: len(slots),
			Note: noStoreNote + " " + lightsPersistedNote})

		return
	}

	config, err := api.tables.load()
	if err != nil {
		// A list this module cannot read is a module with no store, which is not an error for the
		// screen: it says so. The sentence about where addresses live is still here, because it
		// is true whether or not a document could be read.
		slots := api.slotAnswers(nil)

		answer(w, http.StatusOK, lightsAnswer{Lights: []lightAnswer{}, Count: 0, Persisted: false,
			Slots: slots, SlotCount: len(slots),
			Note: noStoreNote + " " + lightsPersistedNote})

		return
	}

	known := make([]lightAnswer, 0, len(config.Lights))

	for _, light := range config.Lights {
		if !api.allowed(light.Address) {
			continue
		}

		known = append(known, lightAnswerOf(light))
	}

	slots := api.slotAnswers(config.Lights)

	note := lightsPersistedNote

	filled := false

	for _, slot := range slots {
		if slot.Filled {
			filled = true
		}
	}

	if !filled {
		note = lightsEmptyNote + " " + lightsUpgradeNote
	}

	answer(w, http.StatusOK, lightsAnswer{
		Lights:    known,
		Count:     len(known),
		Slots:     slots,
		SlotCount: len(slots),
		Persisted: true,
		Note:      note,
	})
}

// slotAnswers is the declaration as the screen draws it, with each filled slot's facts read from
// the store row for that address.
func (api lightsAPI) slotAnswers(stored []storedLight) []lightSlot {
	declared := api.declaredSlots()

	out := make([]lightSlot, 0, len(declared))

	for _, slot := range declared {
		said := lightSlot{Slot: slot.Slot, Filled: slot.Filled}

		if !slot.Filled {
			said.Note = fmt.Sprintf(
				"Slot %d is empty. Fill it in the extension's own settings — light%d_address — not on this screen; "+
					"this module reads nothing at that slot until it has an address.", slot.Slot, slot.Slot)

			out = append(out, said)

			continue
		}

		said.Address = slot.Address

		key := lightKey(slot.Address)

		for _, light := range stored {
			if light.key() != key {
				continue
			}

			said.CallsItself = light.CallsItself
			said.Firmware = light.Firmware
			said.Pixels = light.Pixels
			said.Kind = light.Kind
			said.Width = light.Width
			said.Height = light.Height
			said.ProbedAt = light.ProbedAt
			said.FactsKnown = light.read()

			break
		}

		if !said.FactsKnown {
			said.Note = "This slot names an address, and this module has not read a light there yet, so its own name, " +
				"firmware and shape are not known here. The address is. It is read as soon as it answers."
		}

		out = append(out, said)
	}

	return out
}

// ignoreLight is POST /lights/{name}/ignore: put one light out of the way without forgetting it.
//
// The rule is the refusal, and it is the whole of what makes this safe: **a light a binding drives
// cannot be ignored.** A binding is this module's promise to write to that light, and a flag that
// hid it would leave every screen with nothing to say about where those writes are going. So the
// check runs first, and a light that is in use is refused with the bindings named.
//
// What it does instead of refusing is one flag on one row. The light stays in the store, stays
// readable through GET /lights and on the Lights screen's Ignored bucket, and keeps its address and
// the age of its last reading — so a light ignored by mistake can be found and brought back
// (unignoreLight), which is the difference between this and removing it.
func (api lightsAPI) ignoreLight(w http.ResponseWriter, r *http.Request) {
	api.setIgnored(w, r, true)
}

// unignoreLight is DELETE /lights/{name}/ignore: bring one light back everywhere.
//
// It is always allowed, on any light, for the reason the rule above exists: the way out has to be
// as easy as the way in. A light that was not ignored answers with its row as it stands, because a
// second press of "bring it back" is not a mistake worth a refusal.
func (api lightsAPI) unignoreLight(w http.ResponseWriter, r *http.Request) {
	api.setIgnored(w, r, false)
}

// setIgnored is the one decision the two routes make, and the one write.
func (api lightsAPI) setIgnored(w http.ResponseWriter, r *http.Request, ignored bool) {
	if api.tables == nil {
		refuse(w, r, StoreUnavailable.New(noStoreNote))

		return
	}

	named := strings.TrimSpace(r.PathValue("name"))

	known, ok := api.tables.knownLightNamed(named)
	if !ok {
		refuse(w, r, LightNotFound.New(fmt.Sprintf(
			"This module knows no light called %q. Read GET /lights for the ones it knows.", named)).
			With(fault.StringParam("light", named)))

		return
	}

	// The check that makes the flag safe, and it is only made on the way in: a light already
	// ignored has no binding — it could not have been ignored if it had — and a light somebody is
	// bringing back must never be held out of the UI because a binding appeared meanwhile.
	if ignored {
		users, err := api.bindingsOn(known)
		if err != nil {
			refuse(w, r, StoreUnavailable.New(err.Error()))

			return
		}

		if len(users) > 0 {
			refuse(w, r, LightRefused.New(fmt.Sprintf(
				"%s is shown by %s, so it cannot be ignored: a binding is this module's promise to write to that light, "+
					"and a light it is writing to is not one it may hide. Delete the binding on the Bindings screen first, "+
					"and then ignore the light. Nothing was changed.",
				known.name(), strings.Join(users, ", "))).
				With(fault.StringParam("light", known.name())))

			return
		}
	}

	var kept storedLight

	if _, err := api.tables.revise(func(config *storedConfig) {
		for i := range config.Lights {
			if config.Lights[i].key() == known.key() {
				config.Lights[i].Ignored = ignored
				kept = config.Lights[i]
			}
		}
	}); err != nil {
		refuse(w, r, StoreUnavailable.New(fmt.Sprintf(
			"%s was not %s: %v. Nothing was changed.", known.name(), changedWord(ignored), err)))

		return
	}

	// The light was taken off the list between the read above and the write: answering with a row
	// that is no longer there would be a screen drawing a light the list does not hold.
	if strings.TrimSpace(kept.Address) == "" {
		refuse(w, r, LightNotFound.New(fmt.Sprintf(
			"%s was taken off the list before it could be %s. Read GET /lights for the ones it knows.",
			known.name(), changedWord(ignored))).
			With(fault.StringParam("light", known.name())))

		return
	}

	answer(w, http.StatusOK, map[string]any{
		"light": lightAnswerOf(kept),
		"note":  ignoredNote(kept, ignored),
	})
}

// changedWord is what happened to the light, in the tense the refusals above are written in.
func changedWord(ignored bool) string {
	if ignored {
		return "ignored"
	}

	return "brought back"
}

// ignoredNote is what the answer says beyond the row: where the light went, and how to undo it.
func ignoredNote(light storedLight, ignored bool) string {
	if !ignored {
		return fmt.Sprintf(
			"%s is back on the lights list: every screen that offers a light offers this one again. Nothing about the "+
				"light itself changed — it was only ever left out of those lists.", light.name())
	}

	return fmt.Sprintf(
		"%s is ignored: it stays in this module's store and on the Lights screen's Ignored bucket, with its address "+
			"and the age of its last reading, and it is left out of every list that offers a light — the Overview's "+
			"lights with no printer, the pickers on Bindings, and this module's own In-Use and Available buckets. "+
			"Nothing was written to the light. Bring it back with DELETE /lights/%s/ignore.",
		light.name(), light.name())
}

// bindingsOn is the bindings that name one light, in the operator's order, as the refusal above
// names them.
//
// A binding line is read as the line it is and not resolved into a set: what this needs is which
// bindings name the light, and the line carries it — the printer's name on the host is not
// consulted, so a host that is not answering must not be able to block removing a light from a
// list that has nothing to do with printers.
func (api lightsAPI) bindingsOn(light storedLight) ([]string, error) {
	config, err := api.tables.load()
	if err != nil {
		return nil, err
	}

	return addressesNaming(config.Bindings, light.key())
}

// addressesNaming is every binding line that names one light, read without resolving a printer.
func addressesNaming(entries []json.RawMessage, key string) ([]string, error) {
	parsed, err := parseBindingEntries(entries)
	if err != nil {
		return nil, err
	}

	users := make([]string, 0, 1)

	for i, one := range parsed {
		if lightKey(one.Controller) != key {
			continue
		}

		label := strings.TrimSpace(one.ID)
		if label == "" {
			label = fmt.Sprintf("binding %d", i+1)
		}

		users = append(users, label)
	}

	return users, nil
}

// holdsLight says whether a list already holds one light, by key.
func holdsLight(lights []storedLight, key string) bool {
	for _, light := range lights {
		if light.key() == key {
			return true
		}
	}

	return false
}

// rememberLightFacts writes what a reading of a light learned into its row, adding the row when
// this module has no row for that address yet.
//
// It is how a light keeps its facts current without a timer: fillLightFacts reads the declared
// addresses at start, takeSnapshot already reads a light's facts to name the copy it keeps
// (devicesapi.go), and a reading that has been taken is a reading worth keeping. The row it adds is
// for an address the caller has already proved is one of the declared slots — the declaration is
// what makes a light this module may contact, so a reading of one is a reading worth recording, and
// a route cannot reach any other address to smuggle a row in.
func rememberLightFacts(tables *configTables, address string, info wled.Info, at time.Time) {
	if tables == nil || strings.TrimSpace(info.Version) == "" {
		return
	}

	key := lightKey(address)

	_, _ = tables.revise(func(config *storedConfig) {
		for i := range config.Lights {
			if config.Lights[i].key() != key {
				continue
			}

			config.Lights[i] = lightOf(config.Lights[i].Address, info, at)

			return
		}

		config.Lights = append(config.Lights, lightOf(address, info, at))
	})
}

// fillLightFacts reads every declared address whose facts are not known yet, and keeps what
// answered.
//
// It runs in the background of a start, which is why it is written to be safe to lose: a light that
// does not answer keeps its slot with no facts on it, the card draws the address and says the facts
// have not been read yet, and nothing waits on this. A light that does answer stops saying that,
// and its row is added to the store if the declaration named an address the store had never held
// — which is what a fresh install looks like the moment an operator fills the first slot in.
//
// declared is the five slots. Nil means no declaration is in force, and the store's own rows are
// then read exactly as they were before the cap, which is what a test with a store of its own gets.
func fillLightFacts(ctx context.Context, tables *configTables, newLight func(string) (lightProber, error), declared []declaredLight, log func(string, ...any)) {
	if tables == nil {
		return
	}

	config, err := tables.load()
	if err != nil {
		return
	}

	wanted := make([]string, 0, len(config.Lights))

	if declared == nil {
		for _, light := range config.Lights {
			if light.read() || strings.TrimSpace(light.Address) == "" {
				continue
			}

			wanted = append(wanted, light.Address)
		}
	} else {
		for _, slot := range declared {
			if !slot.Filled {
				continue
			}

			key := lightKey(slot.Address)

			known := false

			for _, light := range config.Lights {
				if light.key() != key {
					continue
				}

				known = light.read()

				break
			}

			if !known {
				wanted = append(wanted, slot.Address)
			}
		}
	}

	for _, address := range wanted {
		read, cancel := context.WithTimeout(ctx, lightProbeTimeout)

		prober, err := newLight(address)
		if err != nil {
			cancel()

			continue
		}

		info, err := prober.Info(read)

		cancel()

		if err != nil {
			continue
		}

		if ok, _ := answeredAsALight(info); !ok {
			continue
		}

		rememberLightFacts(tables, address, info, time.Now())

		if log != nil {
			log("%s: %s answered, and what it says about itself is now in the store", name, lightName(address))
		}
	}
}
