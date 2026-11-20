// The zone this module keeps in its own store: the schema step that made room for it, the two routes
// that read and write it, the precedence between it and the `timezone` setting it was migrated from, and
// the two consumers that were refusing until it arrived.
//
// The behaviours under test are the four the register entry names, in the order it names them: the zone
// is stored and stepped forward with the document; the store's zone is what both consumers read; a zone
// that will not load is refused and nothing is written; and nothing ever clears a zone that is set.
package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/prusa3d/prusa-connect/sdk/deviceapi"
)

// ---------------------------------------------------------------------------------------------
// The schema step
// ---------------------------------------------------------------------------------------------

// TestAVersionEightStoreStepsToNineWithNoZoneAndLosesNothing is the migration every existing install
// takes on this release.
//
// The step writes both zone keys empty and touches nothing else, and that is the whole claim: version 8's
// build had no field to put a zone in, so no document it wrote carries one, and a step that invented
// America/Denver — or UTC, which is this container's own clock and looks healthy while being seven hours
// out — would be inventing a decision nobody made, on every install at once.
func TestAVersionEightStoreStepsToNineWithNoZoneAndLosesNothing(t *testing.T) {
	dir := writeStoreFile(t, `{
  "version": 8,
  "bindings": [{"id": "printer-a", "printer": "printer-a", "controller": "http://10.0.0.136", "segment": -1, "brightness": 128}],
  "states": [{"state": "PRINTING", "colour": {"r": 0, "g": 200, "b": 100}}],
  "moments": [],
  "layouts": [{"binding": "printer-a", "ends": true}],
  "windows": [{"from": "20:00", "to": "07:00", "brightness": 40}],
  "lights": [{"address": "http://10.0.0.136", "calls_itself": "morpheus", "ignored": false}],
  "pauses": []
}`)

	found, err := loadStore(dir)
	if err != nil {
		t.Fatalf("a version 8 document was refused: %v", err)
	}

	if found.Version != 8 || !found.Upgraded {
		t.Fatalf("the read says the document carried version %d and upgraded %v, want 8 and true",
			found.Version, found.Upgraded)
	}

	if found.Config.Version != storeVersion || storeVersion != 10 {
		t.Fatalf("the stepped document is at version %d, want %d", found.Config.Version, storeVersion)
	}

	if found.Config.Timezone != "" || found.Config.TimezoneOrigin != "" {
		t.Fatalf("the step wrote a zone nobody named: %q from %q",
			found.Config.Timezone, found.Config.TimezoneOrigin)
	}

	// Nothing else moved: the binding line, the state's colour, the layout row that asks for the ends
	// line, the window and the light are the rows they were. The layout is the one that matters most —
	// it asks for a clock this document cannot draw yet, and the step keeps the request rather than
	// quietly switching it off.
	if len(found.Config.Bindings) != 1 || len(found.Config.Windows) != 1 || len(found.Config.Lights) != 1 {
		t.Fatalf("the stepped document lost rows: %d bindings, %d windows, %d lights",
			len(found.Config.Bindings), len(found.Config.Windows), len(found.Config.Lights))
	}

	if len(found.Config.Layouts) != 1 || found.Config.Layouts[0].Ends == nil || !*found.Config.Layouts[0].Ends {
		t.Fatalf("the stepped document lost the ends line the layout asked for: %+v", found.Config.Layouts)
	}

	if found.Config.Lights[0].CallsItself != "morpheus" {
		t.Fatalf("the stepped document lost the light's facts: %+v", found.Config.Lights[0])
	}
}

// TestAStoredZoneSurvivesTheReadAndIsOnDiskInWords is the other half: a document at this version with a
// zone in it reads back as it was written, and the keys are in the file where a person opening config.json
// can find them.
func TestAStoredZoneSurvivesTheReadAndIsOnDiskInWords(t *testing.T) {
	dir := t.TempDir()

	if _, err := saveStore(dir, storedConfig{
		Version:        storeVersion,
		Timezone:       "America/Denver",
		TimezoneOrigin: zoneOriginBrowser,
	}); err != nil {
		t.Fatalf("write the store: %v", err)
	}

	found, err := loadStore(dir)
	if err != nil {
		t.Fatalf("the document this build wrote was refused: %v", err)
	}

	if found.Upgraded {
		t.Fatal("a document at this build's own version was stepped, and stepping is for older ones")
	}

	if found.Config.Timezone != "America/Denver" || found.Config.TimezoneOrigin != zoneOriginBrowser {
		t.Fatalf("the zone read back as %q from %q, want America/Denver from %q",
			found.Config.Timezone, found.Config.TimezoneOrigin, zoneOriginBrowser)
	}

	raw, err := os.ReadFile(storePath(dir))
	if err != nil {
		t.Fatalf("read the document: %v", err)
	}

	for _, want := range []string{`"timezone": "America/Denver"`, `"timezone_origin": "browser"`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("the document does not carry %s:\n%s", want, raw)
		}
	}
}

// TestADocumentFromANewerReleaseIsStillRefused is the existing rule, held for the version this change
// writes: a store from the release after this one is refused by number rather than half-read.
func TestADocumentFromANewerReleaseIsStillRefused(t *testing.T) {
	dir := writeStoreFile(t, `{"version": 11, "bindings": [], "timezone": "America/Denver"}`)

	_, err := loadStore(dir)
	if err == nil {
		t.Fatal("a document at version 11 was read by a build that writes version 10")
	}

	if !strings.Contains(err.Error(), "11") || !strings.Contains(err.Error(), "10") {
		t.Fatalf("the refusal %q does not name both versions", err)
	}
}

// ---------------------------------------------------------------------------------------------
// The precedence: the store wins, and the setting is read once
// ---------------------------------------------------------------------------------------------

// TestZoneFromSettingsWritesOnceAndInventsNothing is the migration rule as a function of its three cases:
// a document with no zone takes the setting's and records where it came from; a document that already has
// one is never overwritten; and a setting that names nothing writes nothing at all.
func TestZoneFromSettingsWritesOnceAndInventsNothing(t *testing.T) {
	empty := storedConfig{Version: storeVersion}

	if !zoneFromSettings(&empty, Settings{Timezone: "America/Denver"}) {
		t.Fatal("a document with no zone did not take the setting's")
	}

	if empty.Timezone != "America/Denver" || empty.TimezoneOrigin != zoneOriginSettings {
		t.Fatalf("the document took %q from %q, want America/Denver from %q",
			empty.Timezone, empty.TimezoneOrigin, zoneOriginSettings)
	}

	// The second start: the document holds a zone now, and the setting is not looked at again. This is
	// the sentence "read once, then never again" as an assertion.
	if zoneFromSettings(&empty, Settings{Timezone: "Europe/Paris"}) {
		t.Fatal("a document that already held a zone was written again from the setting")
	}

	if empty.Timezone != "America/Denver" {
		t.Fatalf("the setting overwrote the store's zone: %q", empty.Timezone)
	}

	// And a setting that names no zone invents nothing, which is what keeps a module without one
	// refusing rather than reading UTC.
	bare := storedConfig{Version: storeVersion}

	if zoneFromSettings(&bare, Settings{}) {
		t.Fatal("a setting that named no zone wrote one anyway")
	}

	if bare.Timezone != "" || bare.TimezoneOrigin != "" {
		t.Fatalf("a document with no zone and no setting came out carrying %q from %q",
			bare.Timezone, bare.TimezoneOrigin)
	}
}

// TestTheStoresZoneWinsOverTheSetting is the precedence a person can see: an instance whose settings name
// one zone and whose store names another is judged in the store's, because that is the one the Overview
// screen shows and the one somebody chose.
func TestTheStoresZoneWinsOverTheSetting(t *testing.T) {
	storeSettings(t, &Settings{Timezone: "Europe/Paris"})

	under := newZoneUnderTest(t, `{"version": 9, "bindings": [], "states": [], "moments": [], "layouts": [], "windows": [], "lights": [], "pauses": [], "timezone": "America/Denver", "timezone_origin": "operator"}`)

	status, body := under.do(t, http.MethodGet, "/timezone", "")
	if status != http.StatusOK {
		t.Fatalf("GET /timezone answered %d: %v", status, body)
	}

	if got := body["timezone"]; got != "America/Denver" {
		t.Fatalf("the answer names the zone %v, want the store's America/Denver and not the setting's Europe/Paris", got)
	}

	if got := body["origin"]; got != zoneOriginStore {
		t.Fatalf("the answer says the zone came from %v, want %q", got, zoneOriginStore)
	}

	if got := body["source"]; got != zoneOriginOperator {
		t.Fatalf("the answer says the zone was written by %v, want %q", got, zoneOriginOperator)
	}

	// And the window evaluator reads the same zone: judged_in is the store's, not the setting's.
	status, body = under.do(t, http.MethodGet, "/windows", "")
	if status != http.StatusOK {
		t.Fatalf("GET /windows answered %d: %v", status, body)
	}

	if got := body["judged_in"]; got != "America/Denver" {
		t.Fatalf("the windows are judged in %v, want the store's America/Denver", got)
	}
}

// TestAModuleWithNoStoreStillRunsOnTheSetting is the module the store never reached: granted no data
// directory, it answers the setting's zone and says a change cannot be kept.
func TestAModuleWithNoStoreStillRunsOnTheSetting(t *testing.T) {
	storeSettings(t, &Settings{Timezone: "America/Denver"})

	mux := http.NewServeMux()
	tableAPI{tables: &configTables{}, clock: at}.mount(mux)

	recorder := served(t, mux, http.MethodGet, "/timezone", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /timezone on a module with no directory answered %d: %s",
			recorder.Code, recorder.Body.String())
	}

	var body struct {
		Timezone  string `json:"timezone"`
		Origin    string `json:"origin"`
		Stored    bool   `json:"stored"`
		Persisted bool   `json:"persisted"`
	}

	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("the answer is not JSON: %v", err)
	}

	if body.Timezone != "America/Denver" || body.Origin != zoneOriginSettings {
		t.Fatalf("the answer is %q from %q, want America/Denver from %q", body.Timezone, body.Origin, zoneOriginSettings)
	}

	if body.Stored || body.Persisted {
		t.Fatalf("a module with no directory claims stored=%v persisted=%v", body.Stored, body.Persisted)
	}
}

// ---------------------------------------------------------------------------------------------
// The routes
// ---------------------------------------------------------------------------------------------

// TestTheZoneRouteIsOnTheProcesssOwnMux is the mount, not the handler: a route described in openapi.json
// and proved in a test of its own is still a 404 for the operator if nobody mounted it.
func TestTheZoneRouteIsOnTheProcesssOwnMux(t *testing.T) {
	recorder := served(t, routes(), http.MethodGet, "/timezone", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /timezone on the process's own mux answered %d, want 200: %s",
			recorder.Code, recorder.Body.String())
	}

	var body struct {
		Timezone  string `json:"timezone"`
		Origin    string `json:"origin"`
		Persisted bool   `json:"persisted"`
	}

	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("the answer is not JSON: %v", err)
	}

	// No data directory in a test, so the module says so rather than claiming a zone is stored.
	if body.Persisted {
		t.Fatalf("a module with no data directory answered persisted=true: %s", recorder.Body.String())
	}
}

// TestAZoneIsWrittenReadBackAndNeverCleared is the round trip and the rule that outlives it: a zone is
// written, the answer and the document both carry it, and no request clears it.
func TestAZoneIsWrittenReadBackAndNeverCleared(t *testing.T) {
	under := newZoneUnderTest(t, `{"version": 9, "bindings": [], "states": [], "moments": [], "layouts": [], "windows": [], "lights": [], "pauses": []}`)

	status, body := under.do(t, http.MethodPut, "/timezone", `{"timezone": "America/Denver", "source": "browser"}`)
	if status != http.StatusOK {
		t.Fatalf("PUT /timezone answered %d: %v", status, body)
	}

	if body["timezone"] != "America/Denver" || body["origin"] != zoneOriginStore || body["source"] != zoneOriginBrowser {
		t.Fatalf("the write answered %v, want the zone from the store written by the browser", body)
	}

	if held := under.stored(t); held.Timezone != "America/Denver" || held.TimezoneOrigin != zoneOriginBrowser {
		t.Fatalf("the document holds %q from %q after the write", held.Timezone, held.TimezoneOrigin)
	}

	// The read agrees, which is what a screen that redraws from the read sees.
	status, body = under.do(t, http.MethodGet, "/timezone", "")
	if status != http.StatusOK || body["timezone"] != "America/Denver" {
		t.Fatalf("the read after the write answered %d with %v", status, body)
	}

	// The operator's own change is the second write, and it replaces the first whole.
	status, body = under.do(t, http.MethodPut, "/timezone", `{"timezone": "Europe/Paris"}`)
	if status != http.StatusOK {
		t.Fatalf("the operator's write answered %d: %v", status, body)
	}

	if body["timezone"] != "Europe/Paris" || body["source"] != zoneOriginOperator {
		t.Fatalf("the operator's write answered %v, want Europe/Paris written by the operator", body)
	}

	// And an empty one is refused rather than read as "clear it": the zone is still Europe/Paris.
	status, body = under.do(t, http.MethodPut, "/timezone", `{"timezone": ""}`)
	if status != http.StatusConflict {
		t.Fatalf("an empty zone answered %d, want 409: %v", status, body)
	}

	if got := code(t, body); got != TimezoneRefused.Code().String() {
		t.Fatalf("the refusal is %q, want %q", got, TimezoneRefused.Code().String())
	}

	if held := under.stored(t); held.Timezone != "Europe/Paris" {
		t.Fatalf("a refused empty zone changed the document to %q", held.Timezone)
	}
}

// TestAnUnloadableZoneIsRefusedAndNothingIsStored is the fourth behaviour the register entry names.
//
// The refusal names the value and an example that would be accepted, and the document is left exactly as
// it was — not the zone, not the origin, not a blank. The read that follows is the document as it stands,
// which is what a screen shows, so a failed save can never render as a saved one.
func TestAnUnloadableZoneIsRefusedAndNothingIsStored(t *testing.T) {
	under := newZoneUnderTest(t, `{"version": 9, "bindings": [], "states": [], "moments": [], "layouts": [], "windows": [], "lights": [], "pauses": [], "timezone": "America/Denver", "timezone_origin": "browser"}`)

	before, err := os.ReadFile(storePath(under.dir))
	if err != nil {
		t.Fatalf("read the document: %v", err)
	}

	for _, sent := range []string{
		`{"timezone": "Mars/Olympus"}`,
		`{"timezone": "America/Denvr"}`,
		`{"timezone": "Local"}`,
		`{"timezone": "   "}`,
	} {
		status, body := under.do(t, http.MethodPut, "/timezone", sent)

		if status != http.StatusConflict {
			t.Fatalf("%s answered %d, want 409: %v", sent, status, body)
		}

		if got := code(t, body); got != TimezoneRefused.Code().String() {
			t.Fatalf("%s was refused with %q, want %q", sent, got, TimezoneRefused.Code().String())
		}

		detail, _ := body["detail"].(string)
		if !strings.Contains(detail, "America/Denver") {
			t.Fatalf("the refusal for %s does not name an example zone: %q", sent, detail)
		}

		// Named values: the one that was sent is in the sentence, so a person knows which field is wrong.
		if sent == `{"timezone": "Mars/Olympus"}` && !strings.Contains(detail, "Mars/Olympus") {
			t.Fatalf("the refusal does not name the value that would not load: %q", detail)
		}

		if held := under.stored(t); held.Timezone != "America/Denver" || held.TimezoneOrigin != zoneOriginBrowser {
			t.Fatalf("%s changed the document to %q from %q", sent, held.Timezone, held.TimezoneOrigin)
		}
	}

	// The file itself is byte for byte what it was: a refusal is not a rewrite.
	after, err := os.ReadFile(storePath(under.dir))
	if err != nil {
		t.Fatalf("read the document again: %v", err)
	}

	if string(before) != string(after) {
		t.Fatalf("a refused write rewrote the document:\nbefore %s\nafter  %s", before, after)
	}
}

// TestAZoneBodyThatIsNotAZoneIsRefused is the 400 half: a misspelt key is reported rather than read as an
// empty zone, and a source outside the two a zone can arrive from is refused by name.
func TestAZoneBodyThatIsNotAZoneIsRefused(t *testing.T) {
	under := newZoneUnderTest(t, `{"version": 9, "bindings": [], "states": [], "moments": [], "layouts": [], "windows": [], "lights": [], "pauses": []}`)

	// A misspelt key is refused rather than ignored, because a key ignored is a field left at its zero
	// and the zero here is an empty zone. (`timeZone` would be accepted: a Go decode matches a key by
	// case, which every body on this surface shares.)
	for _, sent := range []string{
		`{"timezome": "America/Denver"}`,
		`{"timezone": "America/Denver", "source": "settings"}`,
		`{"timezone": "America/Denver", "origin": "browser"}`,
		`not json at all`,
	} {
		status, body := under.do(t, http.MethodPut, "/timezone", sent)

		if status != http.StatusBadRequest {
			t.Fatalf("%s answered %d, want 400: %v", sent, status, body)
		}

		if got := code(t, body); got != TimezoneBodyInvalid.Code().String() {
			t.Fatalf("%s was refused with %q, want %q", sent, got, TimezoneBodyInvalid.Code().String())
		}

		if held := under.stored(t); held.Timezone != "" {
			t.Fatalf("%s wrote a zone anyway: %q", sent, held.Timezone)
		}
	}
}

// TestAWindowIsJudgedInTheStoresZone is the first consumer, end to end through the routes: a window
// written against a store's zone is judged there, and the same window written against a store with no
// zone is refused rather than judged in this process's clock.
func TestAWindowIsJudgedInTheStoresZone(t *testing.T) {
	under := newZoneUnderTest(t, `{"version": 9, "bindings": [], "states": [], "moments": [], "layouts": [], "windows": [], "lights": [], "pauses": [], "timezone": "America/Denver", "timezone_origin": "browser"}`)

	status, body := under.do(t, http.MethodPost, "/windows", `{"from": "20:00", "to": "07:00", "brightness": 40}`)
	if status != http.StatusOK {
		t.Fatalf("writing a window on a module with a stored zone answered %d: %v", status, body)
	}

	if got := body["judged_in"]; got != "America/Denver" {
		t.Fatalf("the window list is judged in %v, want America/Denver", got)
	}

	// The instant the routes judge at is 23:00 in the operator's zone, which is inside the window.
	if got, ok := body["wins"]; !ok || got != float64(0) {
		t.Fatalf("no window is in force at 23:00 in America/Denver: %v", body)
	}

	if got, ok := body["level"]; !ok || got != float64(40) {
		t.Fatalf("the brightness in force is %v, want 40: %v", body["level"], body)
	}

	// And the same route refuses on a module that names no zone at all, which is the state every
	// install was in before this release.
	bare := newZoneUnderTest(t, `{"version": 9, "bindings": [], "states": [], "moments": [], "layouts": [], "windows": [], "lights": [], "pauses": []}`)

	status, body = bare.do(t, http.MethodPost, "/windows", `{"from": "20:00", "to": "07:00", "brightness": 40}`)
	if status != http.StatusConflict {
		t.Fatalf("a window on a module with no zone answered %d, want 409: %v", status, body)
	}

	detail, _ := body["detail"].(string)
	if !strings.Contains(detail, "Overview") || !strings.Contains(detail, "America/Denver") {
		t.Fatalf("the refusal %q does not name where to set a zone or a value to set", detail)
	}
}

// TestTheEndsLineDrawsAClockFromTheStoresZone is the second consumer, at the point where the words are
// made.
//
// It is the whole chain in one assertion: the document names a zone, the painter's reader is built over
// that document and no zone is handed to it, the writer composes its layout, and the panel's ends line
// reads a clock time. Before this release the same chain drew nothing on every install, because there was
// no way to name a zone at all.
func TestTheEndsLineDrawsAClockFromTheStoresZone(t *testing.T) {
	zone := panelZone(t)
	at := time.Date(2026, time.November, 19, 21, 0, 0, 0, zone)

	tables := storeHolding(t, storedConfig{
		Version:        storeVersion,
		Timezone:       "America/Denver",
		TimezoneOrigin: zoneOriginBrowser,
	})

	pen := newWriter(newPanelStrip(), 128, quiet)
	pen.layout = withLines(lineEnds)
	pen.store = &storeLights{tables: tables, logging: quiet}

	picture := panelPicture{frame: frameWithFacts(), layout: pen.composing(), at: at}

	if got := picture.lineWord(lineEnds, livePanel.width); got != "ENDS 21:42" {
		t.Fatalf("the ends line reads %q, want ENDS 21:42 from the zone the store holds", got)
	}

	// The same picture on a document that holds no zone: nothing is drawn, and nothing is invented —
	// the process's own clock would have said 04:42 and looked healthy.
	unzoned := storeHolding(t, storedConfig{Version: storeVersion})

	silent := newWriter(newPanelStrip(), 128, quiet)
	silent.layout = withLines(lineEnds)
	silent.store = &storeLights{tables: unzoned, logging: quiet}

	if got := (panelPicture{frame: frameWithFacts(), layout: silent.composing(), at: at}).lineWord(lineEnds, livePanel.width); got != "" {
		t.Fatalf("the ends line reads %q on a document with no zone, want nothing at all", got)
	}
}

// ---------------------------------------------------------------------------------------------
// The harness these tests run on
// ---------------------------------------------------------------------------------------------

// served is one request against a handler, as the recorder it answered with — the shape the two tests
// above need, because they read the answer as a struct rather than as a map.
func served(t *testing.T, handler http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(method, path, strings.NewReader(body)))

	return recorder
}

// zoneUnderTest is the wave-3 routes with no zone handed to them: the store is the only place a zone can
// come from, which is what makes every assertion here about the document rather than about a test seam.
type zoneUnderTest struct {
	*tablesUnderTest
}

// newZoneUnderTest builds the routes over the given document, with the clock at 23:00 in the operator's
// zone and nothing overriding the zone.
func newZoneUnderTest(t *testing.T, document string) *zoneUnderTest {
	t.Helper()

	under := newTablesUnderTest(t, twoPrinters(), twoBindings(), document)

	clock := at()
	now := &clock

	page := deviceapi.Page{Items: twoPrinters(), Total: len(twoPrinters())}

	mux := http.NewServeMux()
	tableAPI{
		live:   under.watch,
		fleet:  func() devices { return fakeFleet{page: page} },
		tables: under.tables,
		bound:  time.Second,
		clock:  func() time.Time { return *now },
	}.mount(mux)

	under.mux = mux

	return &zoneUnderTest{tablesUnderTest: under}
}
