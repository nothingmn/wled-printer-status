package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/prusa3d/prusa-connect/sdk/deviceapi"
	"github.com/prusa3d/prusa-connect/sdk/routesapi"
)

// TestTheRouteAnswers is the permit half: the path openapi.json declares
// answers 200 and the line the settings hold.
func TestTheRouteAnswers(t *testing.T) {
	recorder := httptest.NewRecorder()
	routes().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/wled-printer-status", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /wled-printer-status answered %d, want %d", recorder.Code, http.StatusOK)
	}

	var body struct {
		Message string `json:"message"`
	}

	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("the answer is not JSON: %v", err)
	}

	if body.Message != defaultMessage {
		t.Fatalf("the answer reads %q, want %q", body.Message, defaultMessage)
	}
}

// TestAPathThisModuleDoesNotServeIsRefusedByCode is the refusal half. A client
// branches on the code, so the test reads the code and not the sentence: a
// reworded sentence must not break a caller, and a changed code must.
func TestAPathThisModuleDoesNotServeIsRefusedByCode(t *testing.T) {
	recorder := httptest.NewRecorder()
	routes().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/nothing-here", nil))

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("an unserved path answered %d, want %d", recorder.Code, http.StatusNotFound)
	}

	if got := recorder.Header().Get("Content-Type"); got != "application/problem+json" {
		t.Fatalf("the refusal is %s, want application/problem+json", got)
	}

	var problem struct {
		Code   string            `json:"code"`
		Title  string            `json:"title"`
		Detail string            `json:"detail"`
		Params map[string]string `json:"params"`
	}

	if err := json.Unmarshal(recorder.Body.Bytes(), &problem); err != nil {
		t.Fatalf("the refusal is not JSON: %v", err)
	}

	if want := RouteNotFound.Code().String(); problem.Code != want {
		t.Fatalf("the refusal carries %q, want %q", problem.Code, want)
	}

	// The English is kept beside the code, so a log line and an untranslated
	// screen still read.
	if problem.Title == "" || problem.Detail == "" {
		t.Fatalf("the refusal lost its English: title %q, detail %q", problem.Title, problem.Detail)
	}

	if problem.Params["path"] != "/nothing-here" {
		t.Fatalf("the refusal names the path %q, want /nothing-here", problem.Params["path"])
	}
}

// TestTheManifestDeclaresNoSettingsAndTheCodeHoldsTheDefaults is §4 held against the file and the
// file held against the code.
//
// The manifest carries no `options`. docs/product-requirements.md §3 decision 1 and §4: the host's
// settings pane is not this module's editing surface, the store is, and the manifest "keeps only
// what the host requires". So a fresh install sends an empty settings block — which means the
// defaults in this file are what that install runs on, and nothing may refuse it for want of a
// setting the host has no way to send.
func TestTheManifestDeclaresNoSettingsAndTheCodeHoldsTheDefaults(t *testing.T) {
	manifest := readManifest(t)

	if strings.Contains(manifest, "\noptions:") {
		t.Fatal("module.yaml declares an options block, and the host renders a settings pane from it")
	}

	if keys := settingsKeys(t); len(keys) != 0 {
		t.Fatalf("module.yaml declares the settings %v, want none: the store owns the configuration", keys)
	}

	// The fresh install, which is every install now: an empty block, accepted.
	if err := (Settings{}).validate(); err != nil {
		t.Fatalf("an empty settings block was refused, and that is what every fresh install sends: %v", err)
	}

	storeSettings(t, nil)

	if got := settings(); got.Message != defaultMessage || got.Brightness != defaultBrightness || got.Segment != defaultSegment {
		t.Fatalf("with nothing configured this module runs on %+v, want the message %q, the brightness %d and segment %d",
			got, defaultMessage, defaultBrightness, defaultSegment)
	}
}

// TestNoSettingIsReadFromTheEnvironment keeps every setting in the manifest. A
// setting read from the environment is one an operator cannot find in the
// manifest, cannot see on the settings pane, and cannot set through the API.
func TestNoSettingIsReadFromTheEnvironment(t *testing.T) {
	source, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}

	if strings.Contains(string(source), "os.Getenv") {
		t.Fatal("main.go reads an environment variable. Declare the setting in module.yaml instead")
	}
}

// TestTheFragmentParses holds openapi.json to the parser the host runs at
// launch. A fragment the host refuses is a plugin.info that answers an error, and a
// module it cannot describe is a module it does not serve.
func TestTheFragmentParses(t *testing.T) {
	fragment, err := routesapi.ParseFragment(openAPI)
	if err != nil {
		t.Fatalf("openapi.json does not parse: %v", err)
	}

	paths := fragment.Paths()
	sort.Strings(paths)

	want := []string{
		"/bindings",
		"/bindings/{id}",
		"/bindings/{id}/layout",
		"/bindings/{id}/pause",
		"/controllers",
		"/controllers/{name}",
		"/devices/{name}/identify",
		"/devices/{name}/presets",
		"/devices/{name}/restore",
		"/devices/{name}/snapshot",
		"/devices/{name}/snapshot/{id}",
		"/frames",
		"/lights",
		"/lights/{light}/layouts",
		"/lights/{light}/moments",
		"/lights/{light}/moments/{moment}",
		"/lights/{name}",
		"/lights/{name}/ignore",
		"/pause",
		"/preview-finished",
		"/preview-frame",
		"/preview-pattern",
		"/printers",
		"/states",
		"/states/{state}",
		"/status",
		"/timezone",
		"/windows",
		"/windows/{index}",
		"/wled-printer-status",
	}

	if !reflect.DeepEqual(paths, want) {
		t.Fatalf("the fragment describes the paths %v, want %v", paths, want)
	}
}

// TestASettingsBlockReadsBackAsWhateverItCarried is the half of the old manifest test that still
// has a subject: an install whose settings were the migration source must read them back exactly,
// because they are what seeds its store.
//
// It also holds the segment's default to the shape that keeps segment 0 nameable: negative, so
// "the whole strip" cannot be confused with the controller's first segment.
func TestASettingsBlockReadsBackAsWhateverItCarried(t *testing.T) {
	storeSettings(t, nil)

	if got := settings(); got.Message != defaultMessage || got.Brightness != defaultBrightness {
		t.Fatalf("with nothing configured this module runs on the message %q and the brightness %d, want %q and %d",
			got.Message, got.Brightness, defaultMessage, defaultBrightness)
	}

	storeSettings(t, &Settings{Message: "Ready.", ControllerAddress: "http://10.0.0.136", Brightness: 60})

	if got := settings(); got.Message != "Ready." || got.Brightness != 60 ||
		got.ControllerAddress != "http://10.0.0.136" {
		t.Fatalf("the settings block read back as %+v", got)
	}

	storeSettings(t, nil)

	if fallback := settings(); fallback.Segment != defaultSegment || defaultSegment >= 0 {
		t.Fatalf("with nothing configured this module drives segment %d, want %d, which is negative so that segment 0 stays nameable",
			fallback.Segment, defaultSegment)
	}
}

// TestTheManifestKeepsOnlyWhatTheHostRequires is §4's list, checked against the file: name, kind,
// version, platforms, permissions, memory, lifecycle — and the data directory the store lives
// on, which is what replaced the settings as the place configuration is kept. `egress` is in that
// list and is absent from the file on purpose: an absent block declares no connections, and the
// test below holds that absence.
//
// The fields Settings still reads are here on purpose and are not a manifest key: they are the
// migration source an install from 0.1.0 seeds its store from, and a key the manifest declared
// would put the settings pane back.
func TestTheManifestKeepsOnlyWhatTheHostRequires(t *testing.T) {
	manifest := readManifest(t)

	for _, want := range []string{
		"\nname: wled-printer-status",
		"\nkind: sink",
		"\nversion: ",
		"\nplatforms:",
		"\npermissions:",
		"\n  memory_mib:",
		"\nlifecycle:",
		"\n  data: true",
		"\nui:",
	} {
		if !strings.Contains(manifest, want) {
			t.Fatalf("module.yaml does not declare %q, and the host requires it", want)
		}
	}

	if strings.Contains(manifest, "\noptions:") || strings.Contains(manifest, "- key: ") {
		t.Fatal("module.yaml carries a setting, and a setting is what the store replaced")
	}

	// The fields are still read, so the struct is not empty and the migration has something to
	// read: this is the half that would rot silently if somebody deleted them.
	if fields := settingsFields(t); len(fields) == 0 {
		t.Fatal("Settings reads no field at all, and an install from 0.1.0 would migrate nothing")
	}
}

// TestTheManifestDeclaresNoConnections holds the release to the one claim its egress block used to
// make: it declares nothing. The controllers this module drives are the ones the operator adds on
// the Lights screen, and their addresses are kept in this module's own store, so the manifest cannot
// name them. The host's parser (sdk/manifest/egress.go, checkEgressTarget) accepts an exact host, a
// `*.domain` whose remainder carries a dot, or an IP address, and has no form for a range or for
// "whatever light the operator points it at". Naming the author's own two lights instead described
// the wrong hosts for every other install, so the key is gone.
//
// Absence has to be checked as absence: an `egress` key left behind is a connection an installer is
// asked to approve, and a literal address in this file is the author's home network in every release.
func TestTheManifestDeclaresNoConnections(t *testing.T) {
	manifest := readManifest(t)

	if strings.Contains(manifest, "\negress:") {
		t.Fatal("module.yaml declares egress, and this release connects nowhere outside Prusa Connect " +
			"until an operator adds a light on the Lights screen")
	}

	for _, unwanted := range []string{"\n  - host: ", "\n  - option: ", "10.0.0."} {
		if strings.Contains(manifest, unwanted) {
			t.Fatalf("module.yaml carries %q, and every address this module drives is the operator's, "+
				"kept in the store", unwanted)
		}
	}
}

// TestTheManifestAsksForObserveBecauseTheReaderIsWired keeps the grant and the
// code that earns it in one place. `observe` was withheld while nothing read a
// printer; the wiring that reads one is what makes asking for it true.
func TestTheManifestAsksForObserveBecauseTheReaderIsWired(t *testing.T) {
	manifest := readManifest(t)
	if !strings.Contains(manifest, "permissions:") || !strings.Contains(manifest, "- observe") {
		t.Fatal("module.yaml does not ask for observe, and this module reads the printer as itself")
	}

	source, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}

	if !strings.Contains(string(source), "printer.NewSource(") {
		t.Fatal("main.go builds no printer source, so the observe grant in module.yaml buys nothing")
	}
}

// TestTheVersionAgreesInBothHalves holds the release to the one thing the host will not
// reconcile for it. `module.yaml`'s `version:` is what the installer writes down; `release`, built
// from the literal in `main.go`, is what the process reports back when the host asks. The host
// compares the two at start and refuses a module whose halves differ, which an operator sees as a
// module that installs and never comes up — this tree has been bitten by that twice. Neither file
// is compiled against the other, so nothing else here can notice.
//
// Both halves are read as text and absence fails. A version that could not be found must not read
// as an empty string that happens to match the other empty string.
func TestTheVersionAgreesInBothHalves(t *testing.T) {
	manifestVersion := ""
	for line := range strings.SplitSeq(readManifest(t), "\n") {
		if strings.HasPrefix(line, "#") {
			continue
		}
		if rest, found := strings.CutPrefix(line, "version: "); found {
			manifestVersion = strings.TrimSpace(rest)
			break
		}
	}

	if manifestVersion == "" {
		t.Fatal("module.yaml declares no top-level version:, and the host reads one out of the release")
	}

	source, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}

	found := regexp.MustCompile(`extension\.Version\("([^"]*)"\)`).FindStringSubmatch(string(source))
	if found == nil || found[1] == "" {
		t.Fatal(`main.go carries no extension.Version("...") literal, and that literal is the version the process reports`)
	}

	codeVersion := found[1]
	if manifestVersion != codeVersion {
		t.Fatalf("the halves disagree: module.yaml says version %q and main.go says extension.Version(%q); "+
			"the host refuses a module whose halves differ, so the install would never start",
			manifestVersion, codeVersion)
	}

	// The literal is where the version is written, but `release` is what the process actually
	// reports. Holding the two together catches a literal left behind beside a constant built
	// some other way — the literal would agree with the manifest while the process did not.
	if release.String() != codeVersion {
		t.Fatalf("main.go's literal reads %q and the release the process reports reads %q",
			codeVersion, release.String())
	}
}

// TestTheSettingsRefuseWhatAControllerCannotTake is the half of the settings that
// stops the process: a value the module cannot act on refuses the start, and the
// operator reads the reason on the instance instead of watching a light that never
// changes.
func TestTheSettingsRefuseWhatAControllerCannotTake(t *testing.T) {
	cases := []struct {
		name     string
		settings Settings
		refused  bool
	}{
		{"an address and a brightness", Settings{ControllerAddress: "http://10.0.0.136", Brightness: 128}, false},
		{"an address with no scheme", Settings{ControllerAddress: "10.0.0.136", Brightness: 128}, true},
		{"an address that is not a URL", Settings{ControllerAddress: "not a url at all", Brightness: 128}, true},
		// An empty address with no bindings is nothing configured at all, which is every fresh
		// install now that the manifest declares no options. It is refused by nothing.
		{"nothing configured at all", Settings{}, false},
		{"a brightness above the controller's range", Settings{ControllerAddress: "http://10.0.0.136", Brightness: 256}, true},
		{"a negative brightness", Settings{ControllerAddress: "http://10.0.0.136", Brightness: -1}, true},
		{"the darkest brightness a controller takes", Settings{ControllerAddress: "http://10.0.0.136"}, false},
		{"one segment to drive", Settings{ControllerAddress: "http://10.0.0.136", Segment: 1}, false},
		{"segment 0, which is a segment like any other", Settings{ControllerAddress: "http://10.0.0.136", Segment: 0}, false},
		{"the whole strip, written as a negative segment", Settings{ControllerAddress: "http://10.0.0.136", Segment: -1}, false},
		{"a segment below the one value that means the whole strip", Settings{ControllerAddress: "http://10.0.0.136", Segment: -2}, true},
	}

	for _, one := range cases {
		t.Run(one.name, func(t *testing.T) {
			err := one.settings.validate()

			if one.refused && err == nil {
				t.Fatal("the settings were accepted and this module cannot act on them")
			}

			if !one.refused && err != nil {
				t.Fatalf("usable settings were refused: %v", err)
			}
		})
	}
}

// TestAFreshInstallStartsOnNoSettingsAndAnEmptyDirectory is the case that matters for a new user,
// and the one the manifest's missing options create: an empty settings block, a data directory
// with nothing in it, and a module that has to come up anyway.
//
// It must not be refused — there is no settings pane to write a light into, so a refusal would be
// a module nobody can start. What it runs is an empty set; the store is written empty, which is
// what records the migration as done; and the next start reads that store instead of looking for
// settings again. The light itself arrives through a route, on the module's own pages.
func TestAFreshInstallStartsOnNoSettingsAndAnEmptyDirectory(t *testing.T) {
	dir := t.TempDir()

	fleet := fakeFleet{page: deviceapi.Page{
		Items: []deviceapi.Device{{ID: "printer-a", Name: "Hermes MK4"}},
		Total: 1,
	}}

	var written logLines

	set, origin, err := configurationFor(context.Background(), fleet, Settings{}, dir, written.logf)
	if err != nil {
		t.Fatalf("a fresh install with no settings was refused, and every fresh install has none: %v", err)
	}

	// The line the start writes is what a person reading the module's log has to go on: it must say
	// that nothing is configured, that the store was written empty, and where.
	if written.count("nothing is configured") != 1 || written.count("written empty at "+storePath(dir)) != 1 {
		t.Fatalf("the start did not say what it did: %q", written.all())
	}

	if len(set) != 0 {
		t.Fatalf("a fresh install runs %+v, want nothing configured", set)
	}

	if origin != originNothing {
		t.Fatalf("a fresh install says its set came from %q, want %q", origin, originNothing)
	}

	stored := storedRaw(t, dir)

	if version, _ := stored["version"].(float64); int(version) != storeVersion {
		t.Fatalf("the store a fresh start wrote carries version %v, want %d", stored["version"], storeVersion)
	}

	if lines, _ := stored["bindings"].([]any); len(lines) != 0 {
		t.Fatalf("the store a fresh start wrote holds %v bindings, want none", stored["bindings"])
	}

	again, againOrigin, err := configurationFor(context.Background(), fleet, Settings{}, dir, quiet)
	if err != nil {
		t.Fatalf("the second start of a fresh install was refused: %v", err)
	}

	if len(again) != 0 || againOrigin != originStore {
		t.Fatalf("the second start runs %+v from %q, want nothing from the store", again, againOrigin)
	}
}

// fakeFleet is the printer list resolvePrinter reads, without a host.
type fakeFleet struct {
	page deviceapi.Page
	err  error
}

func (f fakeFleet) List(context.Context, deviceapi.ListParams) (deviceapi.Page, error) {
	return f.page, f.err
}

// TestResolvePrinterPicksOnePrinterAndRefusesToGuess is the rule that keeps a
// person's strip off the wrong machine: an empty setting takes the only printer, and
// every case where the answer is not one printer is refused with what to write
// instead.
func TestResolvePrinterPicksOnePrinterAndRefusesToGuess(t *testing.T) {
	morpheus := deviceapi.Device{ID: "01J8DEVICE", Name: "Morpheus"}
	other := deviceapi.Device{ID: "01J9OTHER", Name: "Other"}

	cases := []struct {
		name    string
		fleet   fakeFleet
		setting string
		want    string
		refused bool
	}{
		{
			name:  "the only printer, with nothing written",
			fleet: fakeFleet{page: deviceapi.Page{Items: []deviceapi.Device{morpheus}, Total: 1}},
			want:  morpheus.ID,
		},
		{
			name:    "two printers and nothing written",
			fleet:   fakeFleet{page: deviceapi.Page{Items: []deviceapi.Device{morpheus, other}, Total: 2}},
			refused: true,
		},
		{
			name:    "no printer at all",
			fleet:   fakeFleet{page: deviceapi.Page{}},
			refused: true,
		},
		{
			name:    "the device id",
			fleet:   fakeFleet{page: deviceapi.Page{Items: []deviceapi.Device{morpheus, other}, Total: 2}},
			setting: other.ID,
			want:    other.ID,
		},
		{
			name:    "the name an operator gave it, whatever case they typed",
			fleet:   fakeFleet{page: deviceapi.Page{Items: []deviceapi.Device{morpheus, other}, Total: 2}},
			setting: "morpheus",
			want:    morpheus.ID,
		},
		{
			name:    "a name two printers answer to",
			fleet:   fakeFleet{page: deviceapi.Page{Items: []deviceapi.Device{morpheus, {ID: "01J9TWIN", Name: "Morpheus"}}, Total: 2}},
			setting: "Morpheus",
			refused: true,
		},
		{
			name:    "a printer that is not there",
			fleet:   fakeFleet{page: deviceapi.Page{Items: []deviceapi.Device{morpheus}, Total: 1}},
			setting: "Ghost",
			refused: true,
		},
	}

	for _, one := range cases {
		t.Run(one.name, func(t *testing.T) {
			got, err := resolvePrinter(context.Background(), one.fleet, one.setting)

			if one.refused {
				if err == nil {
					t.Fatalf("the printer resolved to %q and this module should have refused to pick", got)
				}

				return
			}

			if err != nil {
				t.Fatalf("resolve the printer: %v", err)
			}

			if got != one.want {
				t.Fatalf("the printer resolved to %q, want %q", got, one.want)
			}
		})
	}
}

// readManifest is module.yaml as text. Tests here hold the file to the code rather
// than to a YAML parser: the host parses the manifest when the module installs, and a
// second parser in the tests would prove only that two parsers agree.
func readManifest(t *testing.T) string {
	t.Helper()

	document, err := os.ReadFile("module.yaml")
	if err != nil {
		t.Fatalf("read module.yaml: %v", err)
	}

	return string(document)
}

// settingsKeys reads the option keys module.yaml declares, in order.
func settingsKeys(t *testing.T) []string {
	t.Helper()

	var keys []string

	for line := range strings.SplitSeq(readManifest(t), "\n") {
		if rest, found := strings.CutPrefix(strings.TrimSpace(line), "- key: "); found {
			keys = append(keys, strings.TrimSpace(rest))
		}
	}

	return keys
}

// settingsFields is the config key of every field of Settings, sorted.
func settingsFields(t *testing.T) []string {
	t.Helper()

	kind := reflect.TypeOf(Settings{})
	fields := make([]string, 0, kind.NumField())

	for index := range kind.NumField() {
		name, _, _ := strings.Cut(kind.Field(index).Tag.Get("json"), ",")
		fields = append(fields, name)
	}

	return fields
}

// storeSettings puts a settings block in place for one test and takes it away
// again, so that a test which configures the module does not configure the next one.
func storeSettings(t *testing.T, written *Settings) {
	t.Helper()

	held.Store(written)

	t.Cleanup(func() { held.Store(nil) })
}

// TestTheLightsRouteIsOnTheProcesssOwnMux is the mount, not the handler.
//
// Every other assertion about the lights list is made against a mux the test built, and a route
// that exists, is described in openapi.json and answers beautifully in a test of its own is still a
// 404 for the operator if nobody mounted it in routes(). This asks the process's own handler, with
// no store granted, which is the answer a module that was given no data directory gives: an empty
// list, persisted false, and the sentence about discovery rather than a failure.
func TestTheLightsRouteIsOnTheProcesssOwnMux(t *testing.T) {
	recorder := httptest.NewRecorder()
	routes().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/lights", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /lights answered %d, want %d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}

	var body struct {
		Lights    []any  `json:"lights"`
		Count     int    `json:"count"`
		Persisted bool   `json:"persisted"`
		Note      string `json:"note"`
	}

	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("the answer is not JSON: %v (%s)", err, recorder.Body.String())
	}

	if body.Lights == nil {
		t.Fatalf("the answer carries %#v where the list goes, want a list", body.Lights)
	}

	if body.Count != len(body.Lights) {
		t.Fatalf("count reads %d and the list holds %d", body.Count, len(body.Lights))
	}

	// The sentence names what discovery needs — the host carrying mDNS across the Docker bridge —
	// rather than saying it is coming, and it still points at the typed address.
	if !strings.Contains(body.Note, "mDNS") || !strings.Contains(body.Note, "type") {
		t.Fatalf("the note reads %q, want the sentence about what discovery needs and the typed address", body.Note)
	}
}
