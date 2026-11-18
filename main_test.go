package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"sort"
	"strconv"
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

// TestTheManifestPublishesTheCodesDefault holds module.yaml and this code to
// one default. An operator reads the manifest to learn what a setting does when
// they write nothing, so a manifest that publishes a different number than the
// code uses is documentation that lies.
func TestTheManifestPublishesTheCodesDefault(t *testing.T) {
	manifest, err := os.ReadFile("module.yaml")
	if err != nil {
		t.Fatalf("read module.yaml: %v", err)
	}

	declared := `default: "` + defaultMessage + `"`
	if !strings.Contains(string(manifest), declared) {
		t.Fatalf("module.yaml does not declare %s for the message setting", declared)
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

	if want := []string{"/preview-finished", "/status", "/wled-printer-status"}; !reflect.DeepEqual(paths, want) {
		t.Fatalf("the fragment describes the paths %v, want %v", paths, want)
	}
}

// TestTheSettingsCarryTheDefaultsTheManifestPublishes is the other half of the
// test above: what the manifest says a setting is worth when nobody writes it, and
// what this code holds when the host sends nothing, are the same numbers. An
// operator reads the manifest to learn what happens by default, so the two drifting
// apart is documentation that lies.
func TestTheSettingsCarryTheDefaultsTheManifestPublishes(t *testing.T) {
	storeSettings(t, nil)

	if got := settings(); got.Message != defaultMessage || got.Brightness != defaultBrightness {
		t.Fatalf("with nothing configured this module runs on the message %q and the brightness %d, want %q and %d",
			got.Message, got.Brightness, defaultMessage, defaultBrightness)
	}

	storeSettings(t, &Settings{Message: "Ready.", ControllerAddress: "http://10.0.0.136", Brightness: 60})

	if got := settings(); got.Message != "Ready." || got.Brightness != 60 ||
		got.ControllerAddress != "http://10.0.0.136" {
		t.Fatalf("the configured settings read back as %+v", got)
	}

	manifest := readManifest(t)
	if want := `default: "` + strconv.Itoa(defaultBrightness) + `"`; !strings.Contains(manifest, want) {
		t.Fatalf("module.yaml does not declare %s for the brightness setting", want)
	}

	// The segment's default is what says "the whole strip", and an operator reads it in the
	// manifest rather than in this file.
	storeSettings(t, nil)

	if fallback := settings(); fallback.Segment != defaultSegment || defaultSegment >= 0 {
		t.Fatalf("with nothing configured this module drives segment %d, want %d, which is negative so that segment 0 stays nameable",
			fallback.Segment, defaultSegment)
	}

	if want := `default: "` + strconv.Itoa(defaultSegment) + `"`; !strings.Contains(manifest, want) {
		t.Fatalf("module.yaml does not declare %s for the segment setting", want)
	}
}

// TestTheManifestDeclaresExactlyTheSettingsTheCodeReads holds the two lists
// together. A key in the manifest that no field reads is a knob that does nothing; a
// field with no key in the manifest is a setting the host refuses to send, so the
// module would never see it.
func TestTheManifestDeclaresExactlyTheSettingsTheCodeReads(t *testing.T) {
	declared := settingsKeys(t)
	read := settingsFields(t)

	if len(declared) == 0 {
		t.Fatal("module.yaml declares no option at all")
	}

	sort.Strings(declared)
	sort.Strings(read)

	if !reflect.DeepEqual(declared, read) {
		t.Fatalf("module.yaml declares the options %v, and Settings reads %v", declared, read)
	}
}

// TestTheEgressEntriesNameTheAddressSetting is what makes the module publishable
// with a user-supplied address: the connections the operator is asked to approve are
// the setting they type, not a host this module could have hard-coded.
//
// There are two of them and they are two ports, because the module makes two kinds of
// connection to the same box: HTTP on 80 for the state, and UDP on 4048 — DDP — for the
// pixels of the finished word. The manifest schema has no field for the protocol, so the
// port is what separates them and the `sends` line is where a reviewer reads that the
// second one is raw colour bytes. A build that pushed pixels while declaring only port 80
// would be a module whose permissions do not describe what it does, which is a finding.
func TestTheEgressEntriesNameTheAddressSetting(t *testing.T) {
	manifest := readManifest(t)

	field, ok := reflect.TypeOf(Settings{}).FieldByName("ControllerAddress")
	if !ok {
		t.Fatal("Settings carries no ControllerAddress")
	}

	key, _, _ := strings.Cut(field.Tag.Get("json"), ",")

	for _, want := range []string{"option: " + key, "ports: [80]", "ports: [4048]", "from: process", "DDP"} {
		if !strings.Contains(manifest, want) {
			t.Fatalf("module.yaml does not declare %q in its egress entry", want)
		}
	}

	// Two entries, one per port, each naming the setting rather than a host — and exactly one of
	// host and option per entry, which is the rule the manifest parser enforces at load: a module
	// that named both would not load at all.
	if entries := strings.Count(manifest, "\n  - option: "); entries != 2 {
		t.Fatalf("module.yaml carries %d egress entries that name an option, want 2: HTTP on 80 and DDP on 4048", entries)
	}

	if entries := strings.Count(manifest, "\n  - host: "); entries != 0 {
		t.Fatalf("module.yaml carries %d egress entries that name a host, and the address is typed in", entries)
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
		{"an empty address", Settings{Brightness: 128}, true},
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
