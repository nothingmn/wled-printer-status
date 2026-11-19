package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nothingmn/wled-printer-status/internal/wled"
)

// probeAnswer is what the light at one address does when it is read: answers these facts, or fails
// with this error. It is the whole of the fake below, because a light being added is one read.
type probeAnswer struct {
	info wled.Info
	err  error
}

// probeFleet is the network, as the add route sees it: one answer per address, and every address it
// was asked about in the order it was asked. A test asserting "nothing was probed" reads asked.
type probeFleet struct {
	answers map[string]probeAnswer
	asked   []string
}

func (f *probeFleet) client(address string) (lightProber, error) {
	f.asked = append(f.asked, address)

	answer, ok := f.answers[lightKey(address)]
	if !ok {
		return nil, errors.New("no such address in this fixture")
	}

	return fakeProbe{answer: answer}, nil
}

// fakeProbe is one light's answer, handed over already parsed, which is what lets these tests run
// with no socket and no controller.
type fakeProbe struct{ answer probeAnswer }

func (p fakeProbe) Info(context.Context) (wled.Info, error) { return p.answer.info, p.answer.err }

// lightsUnderTest is the Lights screen's routes over one store and one fake network.
type lightsUnderTest struct {
	mux    http.Handler
	tables *configTables
	dir    string
	fleet  *probeFleet
}

// newLightsUnderTest builds the routes over a fresh store holding no lights, which is the state a
// person adds the first one to.
func newLightsUnderTest(t *testing.T, fleet *probeFleet) *lightsUnderTest {
	t.Helper()

	return newLightsOverStore(t, fleet, storedConfig{Version: storeVersion})
}

// newLightsOverStore is newLightsUnderTest over a document the caller wrote, which is how a test
// starts with a light already known.
func newLightsOverStore(t *testing.T, fleet *probeFleet, start storedConfig) *lightsUnderTest {
	t.Helper()

	dir := t.TempDir()

	if _, err := saveStore(dir, start); err != nil {
		t.Fatalf("write the fixture's store: %v", err)
	}

	tables := &configTables{}
	tables.grant(dir)

	if fleet == nil {
		fleet = &probeFleet{answers: map[string]probeAnswer{}}
	}

	mux := http.NewServeMux()
	lightsAPI{tables: tables, newLight: fleet.client, probe: time.Second}.mount(mux)

	return &lightsUnderTest{mux: mux, tables: tables, dir: dir, fleet: fleet}
}

// do runs one request and answers the status and the body as a map.
func (l *lightsUnderTest) do(t *testing.T, method, path, body string) (int, map[string]any) {
	t.Helper()

	recorder := httptest.NewRecorder()
	l.mux.ServeHTTP(recorder, httptest.NewRequest(method, path, strings.NewReader(body)))

	var answer map[string]any
	if raw := recorder.Body.Bytes(); len(raw) > 0 {
		if err := json.Unmarshal(raw, &answer); err != nil {
			t.Fatalf("%s %s answered something that is not JSON: %v (%s)", method, path, err, raw)
		}
	}

	return recorder.Code, answer
}

// storedLights is the lights rows the document holds on disk, which is what makes "nothing was
// stored" a claim about the file rather than about the answer.
func (l *lightsUnderTest) storedLights(t *testing.T) []storedLight {
	t.Helper()

	found, err := loadStore(l.dir)
	if err != nil {
		t.Fatalf("read the store back: %v", err)
	}

	return found.Config.Lights
}

// detail is a refusal's English, which is what a person reads.
func detail(answer map[string]any) string {
	said, _ := answer["detail"].(string)

	return said
}

// TestAVersionSixStoreGainsOneLightPerAddressItsBindingsName is the migration the operator does not
// have to perform: his two lights are already named by his two bindings, so the store learns both
// addresses from the document it already has, and every binding comes through untouched.
//
// The addresses are his: morpheus on 10.0.0.136, the 256x64 panel, and CyberDesk on 10.0.0.253, the
// 48-LED strip. No facts are written with them and that is the assertion — a migration that invented
// a firmware version would be a document asserting something nobody measured.
func TestAVersionSixStoreGainsOneLightPerAddressItsBindingsName(t *testing.T) {
	const (
		morpheus  = "07e893bb-b922-4ff0-9ad3-061b957400c3"
		cyberdesk = "1d3c5f18-8e0a-4f2b-9f5a-0c6c2b8d7a11"
	)

	dir := writeStoreFile(t, `{
  "version": 6,
  "bindings": [
    {"id": "`+morpheus+`", "printer": "`+morpheus+`", "controller": "http://10.0.0.136", "segment": -1, "brightness": 128},
    {"id": "`+cyberdesk+`", "printer": "`+cyberdesk+`", "controller": "http://10.0.0.253", "segment": -1, "brightness": 128}
  ],
  "states": [],
  "moments": [],
  "layouts": [],
  "windows": [],
  "pauses": []
}`)

	found, err := loadStore(dir)
	if err != nil {
		t.Fatalf("a version 6 document was refused: %v", err)
	}

	if !found.Upgraded || found.Version != 6 {
		t.Fatalf("the read says the document carried version %d and upgraded %v, want 6 and true",
			found.Version, found.Upgraded)
	}

	if found.Config.Version != 7 {
		t.Fatalf("the stepped document is at version %d, want 7", found.Config.Version)
	}

	if len(found.Config.Lights) != 2 {
		t.Fatalf("the stepped document holds %d lights, want the two its bindings name: %+v",
			len(found.Config.Lights), found.Config.Lights)
	}

	want := map[string]bool{"http://10.0.0.136": false, "http://10.0.0.253": false}

	for _, light := range found.Config.Lights {
		if _, ok := want[light.Address]; !ok {
			t.Fatalf("the migration invented a light at %q: %+v", light.Address, found.Config.Lights)
		}

		want[light.Address] = true

		if light.read() {
			t.Fatalf("the migration wrote facts for %s that nobody read: %+v", light.Address, light)
		}
	}

	for address, seen := range want {
		if !seen {
			t.Fatalf("the migration lost the light at %s, which a binding names", address)
		}
	}

	// And the bindings are the bindings: same lines, same ids, in the same order.
	if len(found.Config.Bindings) != 2 {
		t.Fatalf("the stepped document holds %d binding lines, want 2", len(found.Config.Bindings))
	}

	var first storedEntry
	if err := decodeStrictly(found.Config.Bindings[0], &first); err != nil {
		t.Fatalf("read the first binding back: %v", err)
	}

	if first.ID != morpheus || first.Controller != "http://10.0.0.136" || first.Segment == nil || *first.Segment != -1 {
		t.Fatalf("the first binding did not survive the migration: %+v", first)
	}

	// Written back and read again: a document at this version is not stepped a second time, and
	// its two lights are still two.
	if _, err := saveStore(dir, found.Config); err != nil {
		t.Fatalf("write the stepped store back: %v", err)
	}

	again, err := loadStore(dir)
	if err != nil {
		t.Fatalf("read the document back at version 7: %v", err)
	}

	if again.Upgraded {
		t.Fatal("the document was stepped a second time, and it is already at this build's version")
	}

	if len(again.Config.Lights) != 2 {
		t.Fatalf("the document at rest holds %d lights, want 2", len(again.Config.Lights))
	}
}

// TestARowIsWrittenAsAListAndNeverAsNull is the shape a person opening config.json sees on an
// install that names no light: an empty list and not a key that reads as unknown.
func TestARowIsWrittenAsAListAndNeverAsNull(t *testing.T) {
	dir := t.TempDir()

	if _, err := saveStore(dir, storedConfig{Version: storeVersion}); err != nil {
		t.Fatalf("write an empty store: %v", err)
	}

	held := storedRaw(t, dir)

	lights, ok := held["lights"].([]any)
	if !ok {
		t.Fatalf("the document's lights member reads %#v, want an empty list", held["lights"])
	}

	if len(lights) != 0 {
		t.Fatalf("the document holds %d lights, want none", len(lights))
	}
}

// TestAddingALightReadsItFirstAndKeepsWhatAnswered is the flow the operator asked for: type an
// address, and what comes back is the light's own name, firmware, pixel count and shape — read
// before it was kept, and kept exactly as it answered.
func TestAddingALightReadsItFirstAndKeepsWhatAnswered(t *testing.T) {
	fleet := &probeFleet{answers: map[string]probeAnswer{
		"http://10.0.0.253": {info: wled.Info{
			Name: "CyberDesk", Version: "0.16.0", LEDCount: 48, MaxSegments: 16,
		}},
	}}

	api := newLightsUnderTest(t, fleet)

	status, body := api.do(t, http.MethodPost, "/lights", `{"address":"10.0.0.253"}`)
	if status != http.StatusCreated {
		t.Fatalf("POST /lights answered %d, want %d: %v", status, http.StatusCreated, body)
	}

	light := body["light"].(map[string]any)

	if light["name"] != "10.0.0.253" || light["address"] != "http://10.0.0.253" {
		t.Fatalf("the light reads %v/%v, want 10.0.0.253/http://10.0.0.253", light["name"], light["address"])
	}

	if light["calls_itself"] != "CyberDesk" || light["firmware"] != "0.16.0" ||
		light["pixels"].(float64) != 48 || light["kind"] != lightKindStrip {
		t.Fatalf("the answer does not carry what the light said: %v", light)
	}

	if light["facts_known"] != true || light["probed_at"] == nil {
		t.Fatalf("the answer does not say the facts were read: %v", light)
	}

	// A typed address with no scheme is read as http before it is probed, so the light was asked
	// at one address and not two.
	if len(fleet.asked) != 1 || fleet.asked[0] != "http://10.0.0.253" {
		t.Fatalf("the light was probed at %v, want one read of http://10.0.0.253", fleet.asked)
	}

	held := api.storedLights(t)
	if len(held) != 1 || held[0].Address != "http://10.0.0.253" || held[0].CallsItself != "CyberDesk" ||
		held[0].Pixels != 48 || held[0].Kind != lightKindStrip {
		t.Fatalf("the document holds %+v, want one row carrying what the light answered", held)
	}
}

// TestAPanelIsAPanelAndAStripIsAStrip is the shape test on the one field a person reads to tell two
// lights apart: a controller that publishes a matrix is a panel with width and height, and one that
// does not is a strip.
func TestAPanelIsAPanelAndAStripIsAStrip(t *testing.T) {
	fleet := &probeFleet{answers: map[string]probeAnswer{
		"http://10.0.0.136": {info: wled.Info{
			Name: "morpheus", Version: "0.14.0", LEDCount: 16384, MatrixWidth: 256, MatrixHeight: 64,
		}},
	}}

	api := newLightsUnderTest(t, fleet)

	status, body := api.do(t, http.MethodPost, "/lights", `{"address":"http://10.0.0.136"}`)
	if status != http.StatusCreated {
		t.Fatalf("POST /lights answered %d, want %d: %v", status, http.StatusCreated, body)
	}

	light := body["light"].(map[string]any)

	if light["kind"] != lightKindPanel || light["width"].(float64) != 256 || light["height"].(float64) != 64 ||
		light["pixels"].(float64) != 16384 {
		t.Fatalf("the panel reads %v, want a 256 x 64 panel of 16384 pixels", light)
	}
}

// TestAnAddressThatIsNotALightIsRefusedInWordsAndNothingIsStored is the refusal half, and the one
// thing it must be: a sentence a person can act on, and a document that did not change.
//
// Four ways to be wrong, and each one is the operator's own list: nothing answers; something answers
// that is not a WLED light; the light is already on the list; and the address is not an address.
func TestAnAddressThatIsNotALightIsRefusedInWordsAndNothingIsStored(t *testing.T) {
	known := storedConfig{
		Version: storeVersion,
		Lights:  []storedLight{{Address: "http://10.0.0.253", CallsItself: "CyberDesk"}},
	}

	cases := []struct {
		name     string
		body     string
		want     string
		wantSays []string
	}{
		{
			name:     "nothing answers",
			body:     `{"address":"10.0.0.99"}`,
			want:     LightRefused.Code().String(),
			wantSays: []string{"Nothing answered at http://10.0.0.99", "Nothing was added"},
		},
		{
			name:     "something that is not a WLED light",
			body:     `{"address":"10.0.0.98"}`,
			want:     LightRefused.Code().String(),
			wantSays: []string{"not a WLED light", "no firmware version"},
		},
		{
			name:     "already on the list",
			body:     `{"address":"http://10.0.0.253/"}`,
			want:     LightAlreadyKnown.Code().String(),
			wantSays: []string{"already on the list", "10.0.0.253"},
		},
		{
			name:     "not an address at all",
			body:     `{"address":"not a light"}`,
			want:     LightBodyInvalid.Code().String(),
			wantSays: []string{"not a light"},
		},
		{
			name:     "no address in the body",
			body:     `{"address":"  "}`,
			want:     LightBodyInvalid.Code().String(),
			wantSays: []string{"names no address"},
		},
		{
			name:     "a body with a key this module does not read",
			body:     `{"adress":"10.0.0.253"}`,
			want:     LightBodyInvalid.Code().String(),
			wantSays: []string{"adress"},
		},
	}

	for _, one := range cases {
		t.Run(one.name, func(t *testing.T) {
			fleet := &probeFleet{answers: map[string]probeAnswer{
				"http://10.0.0.99":   {err: errors.New("dial tcp 10.0.0.99:80: connect: connection refused")},
				"http://10.0.0.98":   {info: wled.Info{Name: "some-nas"}},
				"http://not a light": {info: wled.Info{Version: "0.16.0"}},
			}}

			api := newLightsOverStore(t, fleet, known)

			status, body := api.do(t, http.MethodPost, "/lights", one.body)
			if status < 400 {
				t.Fatalf("POST /lights answered %d for %s, want a refusal: %v", status, one.name, body)
			}

			if got := code(t, body); got != one.want {
				t.Fatalf("%s was refused with %q, want %q: %v", one.name, got, one.want, body)
			}

			for _, says := range one.wantSays {
				if !strings.Contains(detail(body), says) {
					t.Fatalf("the refusal for %s reads %q, which does not say %q", one.name, detail(body), says)
				}
			}

			held := api.storedLights(t)
			if len(held) != 1 || held[0].Address != "http://10.0.0.253" {
				t.Fatalf("%s left the document holding %+v, want the one light it started with", one.name, held)
			}
		})
	}
}

// TestALightWithNoPrinterIsALight is §2.6's card-per-light: a light is on the list, and can be read,
// whether or not anything is shown on it.
func TestALightWithNoPrinterIsALight(t *testing.T) {
	fleet := &probeFleet{answers: map[string]probeAnswer{}}

	api := newLightsOverStore(t, fleet, storedConfig{
		Version: storeVersion,
		Lights: []storedLight{{
			Address: "http://10.0.0.253", CallsItself: "CyberDesk", Firmware: "0.16.0",
			Pixels: 48, Kind: lightKindStrip, ProbedAt: time.Now().UTC().Format(time.RFC3339),
		}},
	})

	status, body := api.do(t, http.MethodGet, "/lights", "")
	if status != http.StatusOK {
		t.Fatalf("GET /lights answered %d, want 200: %v", status, body)
	}

	if body["count"].(float64) != 1 || body["persisted"] != true {
		t.Fatalf("the list reads count %v persisted %v, want 1 and true", body["count"], body["persisted"])
	}

	light := body["lights"].([]any)[0].(map[string]any)

	if light["name"] != "10.0.0.253" || light["facts_known"] != true || light["calls_itself"] != "CyberDesk" {
		t.Fatalf("the light reads %v, want the row the store holds", light)
	}

	// The list is the store's: nothing was dialled to answer it, so a light that is unplugged
	// answers with what it last said.
	if len(fleet.asked) != 0 {
		t.Fatalf("GET /lights probed %v, want no read of any light", fleet.asked)
	}

	// And the screen is told, in the answer, that discovery is not switched on — the sentence a
	// person reads instead of a card that can never fill.
	note, _ := body["note"].(string)
	if !strings.Contains(note, "not switched on yet") || !strings.Contains(note, "type") {
		t.Fatalf("the note reads %q, want the sentence about discovery and the typed address", note)
	}
}

// TestRemovingALightIsRefusedWhileABindingUsesIt is the other half of a light being a thing: you can
// take one off the list, but not out from under a binding that is writing to it.
func TestRemovingALightIsRefusedWhileABindingUsesIt(t *testing.T) {
	const printer = "printer-a"
	whole := wholeStrip

	api := newLightsOverStore(t, nil, storedConfig{
		Version: storeVersion,
		Bindings: []json.RawMessage{rawBinding(t, storedEntry{
			ID: printer, Printer: printer, Controller: "http://10.0.0.253", Segment: &whole,
		})},
		Lights: []storedLight{{Address: "http://10.0.0.253"}},
	})

	status, body := api.do(t, http.MethodDelete, "/lights/10.0.0.253", "")
	if status != http.StatusConflict {
		t.Fatalf("DELETE /lights/10.0.0.253 answered %d, want 409: %v", status, body)
	}

	if got := code(t, body); got != LightRefused.Code().String() {
		t.Fatalf("the refusal carries %q, want %q: %v", got, LightRefused.Code().String(), body)
	}

	if !strings.Contains(detail(body), printer) || !strings.Contains(detail(body), "cannot be removed") {
		t.Fatalf("the refusal reads %q, want it to name the binding that uses the light", detail(body))
	}

	if held := api.storedLights(t); len(held) != 1 {
		t.Fatalf("the refused removal left %d lights, want the one it had", len(held))
	}
}

// TestALightNobodyUsesComesOffTheList is the removal that works, and the name it is addressed by:
// the address without the scheme, which is the name GET /lights gives it.
func TestALightNobodyUsesComesOffTheList(t *testing.T) {
	api := newLightsOverStore(t, nil, storedConfig{
		Version: storeVersion,
		Lights: []storedLight{
			{Address: "http://10.0.0.136"},
			{Address: "http://10.0.0.253"},
		},
	})

	status, body := api.do(t, http.MethodDelete, "/lights/10.0.0.136", "")
	if status != http.StatusOK {
		t.Fatalf("DELETE /lights/10.0.0.136 answered %d, want 200: %v", status, body)
	}

	if body["deleted"] != "10.0.0.136" || body["address"] != "http://10.0.0.136" {
		t.Fatalf("the answer reads %v, want the light that was removed", body)
	}

	held := api.storedLights(t)
	if len(held) != 1 || held[0].Address != "http://10.0.0.253" {
		t.Fatalf("the document holds %+v, want the one light nobody asked to remove", held)
	}

	status, body = api.do(t, http.MethodDelete, "/lights/10.0.0.99", "")
	if status != http.StatusNotFound {
		t.Fatalf("DELETE of a name the list does not hold answered %d, want 404: %v", status, body)
	}

	if got := code(t, body); got != LightNotFound.Code().String() {
		t.Fatalf("the refusal carries %q, want %q: %v", got, LightNotFound.Code().String(), body)
	}
}

// TestASeededLightIsFilledInWhenItAnswers is what makes the migration's empty rows honest: the start
// reads every light it has never read, and the one that answers stops saying nobody has read it. The
// one that does not answer keeps its row and its silence.
func TestASeededLightIsFilledInWhenItAnswers(t *testing.T) {
	fleet := &probeFleet{answers: map[string]probeAnswer{
		"http://10.0.0.136": {info: wled.Info{
			Name: "morpheus", Version: "0.14.0", LEDCount: 16384, MatrixWidth: 256, MatrixHeight: 64,
		}},
		"http://10.0.0.253": {err: errors.New("connect: no route to host")},
	}}

	api := newLightsOverStore(t, fleet, storedConfig{
		Version: storeVersion,
		Lights: []storedLight{
			{Address: "http://10.0.0.136"},
			{Address: "http://10.0.0.253"},
		},
	})

	fillLightFacts(context.Background(), api.tables, fleet.client, nil)

	held := api.storedLights(t)
	if len(held) != 2 {
		t.Fatalf("the fill left %d lights, want both", len(held))
	}

	if !held[0].read() || held[0].CallsItself != "morpheus" || held[0].Kind != lightKindPanel || held[0].Pixels != 16384 {
		t.Fatalf("the light that answered reads %+v, want what it said", held[0])
	}

	if held[1].read() {
		t.Fatalf("the light that did not answer was given facts: %+v", held[1])
	}

	// A light that answered is not read again on the next pass: this is a start's work and not a
	// timer, and a second start must not dial a light it already has facts for. The one that was
	// dark is tried again, because a light that was unplugged is exactly what a later read fixes.
	before := len(fleet.asked)
	fillLightFacts(context.Background(), api.tables, fleet.client, nil)

	second := fleet.asked[before:]
	if len(second) != 1 || lightKey(second[0]) != lightKey("http://10.0.0.253") {
		t.Fatalf("the second pass read %v, want only the light that never answered", second)
	}
}

// TestALightNoBindingDrivesStillAnswersItsOwnSegments is the last thing a light with no printer on it
// needs: its segments, which is what a person picks from when he binds the first printer to it.
func TestALightNoBindingDrivesStillAnswersItsOwnSegments(t *testing.T) {
	light := newFakeStrip()
	light.state = wled.State{Segments: []wled.Segment{{ID: 0, Start: 0, Stop: 48, Name: "desk"}}}

	tables := &configTables{}
	dir := t.TempDir()
	tables.grant(dir)

	if _, err := saveStore(dir, storedConfig{
		Version: storeVersion,
		Lights:  []storedLight{{Address: "http://10.0.0.253"}},
	}); err != nil {
		t.Fatalf("write a store naming one light: %v", err)
	}

	api := newAPIStored(t, light, testPrinters, nil, func(string) (strip, error) { return light, nil }, tables)

	status, body := api.do(t, http.MethodGet, "/controllers/10.0.0.253", "")
	if status != http.StatusOK {
		t.Fatalf("GET /controllers/10.0.0.253 answered %d, want 200: %v", status, body)
	}

	if body["segments_known"] != true {
		t.Fatalf("the light's own segments were not read: %v", body)
	}

	if len(body["segments"].([]any)) != 1 {
		t.Fatalf("the light reports %v segments, want the one it holds", body["segments"])
	}
}

// rawBinding is one line of the store's bindings list, as the document holds it.
func rawBinding(t *testing.T, line storedEntry) json.RawMessage {
	t.Helper()

	raw, err := json.Marshal(line)
	if err != nil {
		t.Fatalf("render a binding line: %v", err)
	}

	return raw
}

// TestAddingARealLightByItsAddress is the operator's own flow against a light that is really there:
// POST /lights with one address, through the process's own WLED client and a store of its own, and
// nothing written to the light — the only request made of it is GET /json/info.
//
// It is skipped unless WLED_LIVE_LIGHT names an address, because a test that dials a controller is a
// test that fails on a machine that has none. The command, on the host the lights are on:
//
//	WLED_LIVE_LIGHT=10.0.0.253 go test -run TestAddingARealLightByItsAddress -v .
func TestAddingARealLightByItsAddress(t *testing.T) {
	address := strings.TrimSpace(os.Getenv("WLED_LIVE_LIGHT"))
	if address == "" {
		t.Skip("set WLED_LIVE_LIGHT to a light's address to run this against the real thing")
	}

	dir := t.TempDir()

	tables := &configTables{}
	tables.grant(dir)

	// The document an installed module already has: the store is written at the first start, and
	// adding a light is a change to a document that exists rather than the creation of one.
	if _, err := saveStore(dir, storedConfig{Version: storeVersion}); err != nil {
		t.Fatalf("write the empty store an install has: %v", err)
	}

	mux := http.NewServeMux()
	lightsAPI{tables: tables}.mount(mux)

	body, err := json.Marshal(map[string]string{"address": address})
	if err != nil {
		t.Fatalf("render the request body: %v", err)
	}

	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/lights", strings.NewReader(string(body))))

	if recorder.Code != http.StatusCreated {
		t.Fatalf("POST /lights with %s answered %d: %s", address, recorder.Code, recorder.Body.String())
	}

	var added struct {
		Light lightAnswer `json:"light"`
	}

	if err := json.Unmarshal(recorder.Body.Bytes(), &added); err != nil {
		t.Fatalf("the answer is not JSON: %v (%s)", err, recorder.Body.String())
	}

	t.Logf("%s answered: calls itself %q, firmware %q, %d pixels, a %s of %d x %d",
		added.Light.Address, added.Light.CallsItself, added.Light.Firmware,
		added.Light.Pixels, added.Light.Kind, added.Light.Width, added.Light.Height)

	if !added.Light.FactsKnown || added.Light.Pixels == 0 || added.Light.Kind == "" {
		t.Fatalf("the light answered but its facts were not kept: %+v", added.Light)
	}

	held, err := loadStore(dir)
	if err != nil {
		t.Fatalf("read the store the light was added to: %v", err)
	}

	if len(held.Config.Lights) != 1 || !held.Config.Lights[0].read() {
		t.Fatalf("the document holds %+v, want one light with facts on it", held.Config.Lights)
	}

	// And adding it twice is the refusal the operator would see for a typo, not a second row.
	again := httptest.NewRecorder()
	mux.ServeHTTP(again, httptest.NewRequest(http.MethodPost, "/lights", strings.NewReader(string(body))))

	if again.Code != http.StatusConflict {
		t.Fatalf("adding the same light twice answered %d, want 409: %s", again.Code, again.Body.String())
	}

	if after, err := loadStore(dir); err != nil {
		t.Fatalf("read the store after the refused add: %v", err)
	} else if len(after.Config.Lights) != 1 {
		t.Fatalf("the refused second add left %d lights, want the one it had", len(after.Config.Lights))
	}
}
