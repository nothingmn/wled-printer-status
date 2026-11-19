// The Devices screen's routes: a light's snapshots, and the restore that is the only thing on this
// module's surface which rewrites a controller's own configuration.
//
// Every test here runs against a light that is a fake and a directory of its own. Nothing here
// touches a controller, and nothing here writes a configuration to one: the one place a real light
// is written to is the integration wave, and it is not this file's.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nothingmn/wled-printer-status/internal/wled"
)

// testConfig is a configuration dump shaped like the two the operator's lights answer with: the
// sections this build restores, the two it refuses, and the seven the requirements' table does not
// mention. The credential members are fixtures and not credentials: nothing in this repository has
// ever held a real one, and a test that did would be the leak.
const testConfig = `{
  "rev": ["16.0.1", "b1"],
  "vid": 20210,
  "id": {"name": "CyberDesk", "mdns": "cyberdesk", "inv": "light"},
  "nw": {"ins": {"host": "example.invalid", "token": "fixture-not-a-credential"}, "dns": [1, 1, 1, 1], "linked_remote": "fixture"},
  "wifi": {"phy": 1, "sleep": true},
  "hw": {"led": {"type": 22, "count": 48}},
  "light": {"scale-bri": 100, "gc": {"r": 0, "g": 0, "b": 0}, "tr": 0},
  "def": {"bri": 128, "on": true},
  "if": {"sync": {"send": false, "recv": false}},
  "timers": {"count": 0, "ins": []},
  "um": {},
  "ap": {"ssid": "WLED-ap"},
  "ol": {"clock": 0},
  "ota": {"aota": false},
  "remote": {"enabled": false}
}`

// testPresets is a preset table with two defined slots beside the firmware's own slot 0.
const testPresets = `{"0": {"n": "Off"}, "1": {"n": "Printing", "bri": 128}, "2": {"n": "Alert", "bri": 200}}`

// fakeDevice is one controller as a snapshot and a restore see it: the five calls the routes make,
// with the two behaviours this file has to prove — a write, and what answering looks like after one.
type fakeDevice struct {
	*fakeStrip

	mu sync.Mutex

	// info is what the light says it is.
	info wled.Info

	// config and presets are the two documents it answers with.
	config  json.RawMessage
	presets json.RawMessage

	// presetsErr, when set, is what a read of its preset table answers instead of the table:
	// a light that is unplugged, or one that answers with something that is not a table.
	presetsErr error

	// writes is every configuration body written to it, in order.
	writes []json.RawMessage

	// configReads is how many times its configuration was read, which is how a test tells a
	// refusal that read nothing from one that read and then refused.
	configReads int

	// refuses is how many polls after a write answer with a failure, which is the restart: the
	// light is gone for that many reads and back afterwards.
	refuses int

	// goneAfterWrite makes every read after a write fail for ever, which is a light that never
	// came back.
	goneAfterWrite bool

	// restarting says a write has happened, so the behaviour above is in force: a light that has
	// not been written to answers, which is what makes the identity read before a restore work.
	restarting bool

	// pending is what is left of refuses.
	pending int

	// answerInsideSettle makes the light answer the very next poll after the write, which is the
	// case the route must not believe.
	answerAtOnce bool

	// onWrite runs inside the write, which is how a test looks at the directory at the one moment
	// that matters: before the configuration is taken.
	onWrite func()
}

// newFakeDevice is a strip of 48 LEDs with the fixture's configuration.
func newFakeDevice() *fakeDevice {
	return &fakeDevice{
		fakeStrip: newFakeStrip(),
		info:      wled.Info{Name: "CyberDesk", Version: "16.0.1", LEDCount: 48, MaxSegments: 32},
		config:    json.RawMessage(testConfig),
		presets:   json.RawMessage(testPresets),
	}
}

// Info answers with the light's facts, and with the reboot when one is in progress.
func (f *fakeDevice) Info(context.Context) (wled.Info, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.restarting {
		if f.goneAfterWrite {
			return wled.Info{}, errors.New("the light is not answering: it is restarting")
		}

		if f.pending > 0 && !f.answerAtOnce {
			f.pending--

			return wled.Info{}, errors.New("the light is not answering: it is restarting")
		}
	}

	return f.info, nil
}

// Config is the light's configuration dump.
func (f *fakeDevice) Config(context.Context) (json.RawMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.configReads++

	return f.config, nil
}

// Presets is the light's preset table, or the failure that stands for a light which is not
// answering at all: presetsErr is what the route that reads a dead light's presets is tested with.
func (f *fakeDevice) Presets(context.Context) (json.RawMessage, error) {
	if f.presetsErr != nil {
		return nil, f.presetsErr
	}

	return f.presets, nil
}

// StateDocument is what the light is doing now, as the firmware answers it.
func (f *fakeDevice) StateDocument(context.Context) (json.RawMessage, error) {
	return json.RawMessage(`{"on": true, "bri": 128, "seg": [{"id": 0, "start": 0, "stop": 48}]}`), nil
}

// WriteConfig takes one configuration body, which is the destructive half of a restore. It runs the
// test's hook before it records anything, so a hook sees the light exactly as the route found it.
func (f *fakeDevice) WriteConfig(_ context.Context, body json.RawMessage) error {
	if f.onWrite != nil {
		f.onWrite()
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	f.writes = append(f.writes, body)
	f.restarting = true
	f.pending = f.refuses

	return nil
}

// written is every body it took.
func (f *fakeDevice) written() []json.RawMessage {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]json.RawMessage(nil), f.writes...)
}

// reads is how many times its configuration was read.
func (f *fakeDevice) reads() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.configReads
}

// devicesUnderTest is the Devices routes over a watch built on fakes, a directory of its own, and a
// clock the test moves instead of waiting out a reboot.
type devicesUnderTest struct {
	mux    http.Handler
	watch  *runner
	light  *fakeDevice
	dir    string
	tables *configTables

	// now is the clock the routes read, moved by the same function that waits.
	now *time.Time
}

// newDevicesUnderTest builds the routes, the light and the directory.
func newDevicesUnderTest(t *testing.T) *devicesUnderTest {
	t.Helper()

	ctx, stop := context.WithCancel(context.Background())
	t.Cleanup(stop)

	light := newFakeDevice()

	set := bindings{{DeviceID: "printer-a", Address: "http://10.0.0.136", Segment: -1, Brightness: 128}}

	watch := &runner{
		newSource: func(b binding) (watchSource, error) {
			return &fixedSource{snapshot: printingJob(b.DeviceID, 41)}, nil
		},
		newLight: func(string) (strip, error) { return light, nil },
		logf:     quiet,
		tick:     time.Hour,
		grace:    2 * time.Second,
	}

	if err := watch.apply(ctx, set); err != nil {
		t.Fatalf("the watch did not start: %v", err)
	}

	t.Cleanup(func() {
		watch.stop()
		forgetBindings()
		forgetPreview()
	})

	dir := t.TempDir()

	if _, err := saveStore(dir, storedConfig{Version: storeVersion}); err != nil {
		t.Fatalf("write the store the module would have written: %v", err)
	}

	tables := &configTables{}
	tables.grant(dir)

	clock := time.Date(2026, time.November, 19, 18, 30, 0, 0, time.UTC)
	now := &clock

	mux := http.NewServeMux()
	deviceAPI{
		live:      watch,
		tables:    tables,
		newDevice: func(string) (controllerConfig, error) { return light, nil },
		clock:     func() time.Time { return *now },
		poll:      100 * time.Millisecond,
		wait:      3 * time.Second,
		rest: func(d time.Duration) {
			*now = now.Add(d)
		},
	}.mount(mux)

	return &devicesUnderTest{mux: mux, watch: watch, light: light, dir: dir, tables: tables, now: now}
}

// do runs one request and answers with the status and the body as a map.
func (a *devicesUnderTest) do(t *testing.T, method, path, body string) (int, map[string]any) {
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

// snapshotFiles is every snapshot file in this test's folder, by id.
func (a *devicesUnderTest) snapshotFiles(t *testing.T) []string {
	t.Helper()

	entries, err := os.ReadDir(snapshotFolder(a.dir))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}

		t.Fatalf("read the snapshot folder: %v", err)
	}

	out := make([]string, 0, len(entries))

	for _, entry := range entries {
		out = append(out, entry.Name())
	}

	return out
}

// take is one snapshot through the route, and its id.
func (a *devicesUnderTest) take(t *testing.T) string {
	t.Helper()

	status, body := a.do(t, http.MethodPost, "/devices/10.0.0.136/snapshot", "")
	if status != http.StatusOK {
		t.Fatalf("taking a snapshot answered %d: %v", status, body)
	}

	said := readObject(t, body, "snapshot")

	id, _ := said["id"].(string)
	if id == "" {
		t.Fatalf("the answer carried no snapshot id: %v", body)
	}

	return id
}

// restore is one restore through the route.
func (a *devicesUnderTest) restore(t *testing.T, id string, confirm bool) (int, map[string]any) {
	t.Helper()

	body, err := json.Marshal(map[string]any{"snapshot": id, "confirm": confirm})
	if err != nil {
		t.Fatalf("render the restore body: %v", err)
	}

	return a.do(t, http.MethodPost, "/devices/10.0.0.136/restore", string(body))
}

// ---------------------------------------------------------------------------------------------
// Taking one, and what it says it cannot give back
// ---------------------------------------------------------------------------------------------

// TestASnapshotRoundTripsAndSaysWhatItOmits is the whole of item 3's reading half: a snapshot is
// taken from the light, kept as a file of its own, read back by id, and every answer about it says
// the one thing an operator must not be left to assume — that no configuration dump on these lights
// carries the wifi password, so it is not a clone of the device.
func TestASnapshotRoundTripsAndSaysWhatItOmits(t *testing.T) {
	under := newDevicesUnderTest(t)

	id := under.take(t)

	// The file, its mode, and that it holds what the light answered.
	path := filepath.Join(snapshotFolder(under.dir), id+snapshotSuffix)

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("the snapshot file %s was not written: %v", path, err)
	}

	if mode := info.Mode().Perm(); mode != snapshotFileMode {
		t.Fatalf("the snapshot file is %04o, want %04o: it carries a light's network configuration", mode, snapshotFileMode)
	}

	held, err := loadSnapshot(under.dir, id)
	if err != nil {
		t.Fatalf("the snapshot that was just written does not read back: %v", err)
	}

	if !strings.Contains(string(held.Config), `"light"`) || !strings.Contains(string(held.Presets), "Printing") {
		t.Fatalf("the snapshot does not hold the light's configuration and presets: %s / %s", held.Config, held.Presets)
	}

	if held.Device.Controller != "CyberDesk" || held.Device.Kind != snapshotKindStrip ||
		held.Device.Pixels != 48 || held.Device.Address != "http://10.0.0.136" {
		t.Fatalf("the snapshot's identity is %+v, want the strip CyberDesk at http://10.0.0.136 with 48 pixels",
			held.Device)
	}

	// The list, by name.
	status, body := under.do(t, http.MethodGet, "/devices/10.0.0.136/snapshot", "")
	if status != http.StatusOK {
		t.Fatalf("the list answered %d: %v", status, body)
	}

	if count, _ := body["count"].(float64); count != 1 {
		t.Fatalf("the list holds %v snapshots, want 1: %v", body["count"], body)
	}

	first := readObjects(t, body, "snapshots")[0]
	if first["id"] != id {
		t.Fatalf("the list reports %v, want the snapshot that was taken %s", first["id"], id)
	}

	// One by id, with the restore table and the omissions.
	status, body = under.do(t, http.MethodGet, "/devices/10.0.0.136/snapshot/"+id, "")
	if status != http.StatusOK {
		t.Fatalf("reading the snapshot answered %d: %v", status, body)
	}

	if restorable, _ := body["restorable"].(bool); !restorable {
		t.Fatalf("the snapshot was reported as not restorable: %v", body["unrestorable"])
	}

	said := readObject(t, body, "snapshot")

	if clone, present := said["complete_clone"]; present && clone != false {
		t.Fatalf("the snapshot claims to be a complete clone: %v", clone)
	}

	omits, ok := said["omits"].([]any)
	if !ok || len(omits) != 2 {
		t.Fatalf("the snapshot's omissions are %v, want the instance credential and the wifi password", said["omits"])
	}

	named := map[string]bool{}
	for _, entry := range omits {
		member, _ := entry.(map[string]any)
		named[member["member"].(string)] = true
	}

	if !named["nw.ins"] || !named["nw.psk"] {
		t.Fatalf("the omissions name %v, want nw.ins and nw.psk", named)
	}

	// The preset count is the two defined slots beside the firmware's own slot 0.
	if presets, _ := said["presets"].(float64); presets != 2 {
		t.Fatalf("the snapshot reports %v presets, want 2", said["presets"])
	}
}

// TestASnapshotSaysTheWifiPasswordIsNotInTheLightWhenItIsNot is the same omission read from the
// other side: a firmware that did put a psk in its dump is reported honestly as doing so, rather
// than the module repeating a sentence that had stopped being true.
func TestASnapshotSaysTheWifiPasswordIsNotInTheLightWhenItIsNot(t *testing.T) {
	under := newDevicesUnderTest(t)

	under.light.config = json.RawMessage(`{"light": {}, "nw": {"ins": {}, "psk": "fixture-not-a-credential"}}`)

	id := under.take(t)

	status, body := under.do(t, http.MethodGet, "/devices/10.0.0.136/snapshot/"+id, "")
	if status != http.StatusOK {
		t.Fatalf("reading the snapshot answered %d: %v", status, body)
	}

	said := readObject(t, body, "snapshot")
	omits, _ := said["omits"].([]any)

	for _, entry := range omits {
		member, _ := entry.(map[string]any)
		if member["member"] == "nw.psk" {
			why, _ := member["why"].(string)
			if !strings.Contains(why, "This firmware put the wifi password in the dump") {
				t.Fatalf("a dump carrying a psk is described as %q, want it said that this firmware carries one", why)
			}

			return
		}
	}

	t.Fatalf("the snapshot does not name nw.psk at all: %v", said["omits"])
}

// TestALightThatDoesNotAnswerIsNotSnapshotted is the refusal that makes the route worth having: the
// snapshots already held are the reason the list still answers, and a new one cannot be taken.
func TestALightThatDoesNotAnswerIsNotSnapshotted(t *testing.T) {
	under := newDevicesUnderTest(t)
	under.take(t)

	under.light.mu.Lock()
	under.light.goneAfterWrite = true
	under.light.restarting = true
	under.light.mu.Unlock()

	recorder := httptest.NewRecorder()
	under.mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/devices/10.0.0.136/snapshot", nil))

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("a dead light's snapshot answered %d, want %d: %s",
			recorder.Code, http.StatusServiceUnavailable, recorder.Body.String())
	}

	if code := problemCode(t, recorder); !strings.HasSuffix(code, "SNAPSHOT_UNREADABLE") {
		t.Fatalf("the refusal is %q, want SNAPSHOT_UNREADABLE", code)
	}

	// The list still answers, from the copy taken while the light was alive.
	status, body := under.do(t, http.MethodGet, "/devices/10.0.0.136/snapshot", "")
	if status != http.StatusOK {
		t.Fatalf("the list of a dead light's snapshots answered %d: %v", status, body)
	}

	if count, _ := body["count"].(float64); count != 1 {
		t.Fatalf("the dead light's list holds %v snapshots, want the one taken before it died", body["count"])
	}

	if files := under.snapshotFiles(t); len(files) != 1 {
		t.Fatalf("taking a snapshot of a dead light wrote %v, want the one file already there", files)
	}
}

// ---------------------------------------------------------------------------------------------
// The restore's refusals
// ---------------------------------------------------------------------------------------------

// TestARestoreNeedsTheConfirmationAndWritesNothingWithoutIt is the first of the route's two guards:
// a body that did not say it meant it is refused before the snapshot is read, so a caller cannot
// learn from a mistake what it would have done.
func TestARestoreNeedsTheConfirmationAndWritesNothingWithoutIt(t *testing.T) {
	under := newDevicesUnderTest(t)
	id := under.take(t)

	reads := under.light.reads()

	status, body := under.restore(t, id, false)
	if status != http.StatusConflict {
		t.Fatalf("an unconfirmed restore answered %d, want %d: %v", status, http.StatusConflict, body)
	}

	if code, _ := body["code"].(string); !strings.HasSuffix(code, "RESTORE_NOT_CONFIRMED") {
		t.Fatalf("the refusal is %q, want RESTORE_NOT_CONFIRMED", code)
	}

	if written := under.light.written(); len(written) != 0 {
		t.Fatalf("an unconfirmed restore wrote %d bodies to the light, want none", len(written))
	}

	if after := under.light.reads(); after != reads {
		t.Fatalf("an unconfirmed restore read the light's configuration %d times, want none", after-reads)
	}
}

// The restore's refusals, and the one path that writes.
//
// These are the tests this wave exists for: a configuration written to the wrong light, or reported
// as restored while the light is mid-restart, is the failure the route is shaped to make impossible.

// TestARestoreRefusesASnapshotFromAnotherLight is the guard that stops a configuration being written
// to a box it was not taken from. The file is one this module holds; what does not match is which
// light it came from.
func TestARestoreRefusesASnapshotFromAnotherLight(t *testing.T) {
	under := newDevicesUnderTest(t)
	taken := under.take(t)

	held, err := loadSnapshot(under.dir, taken)
	if err != nil {
		t.Fatalf("read the snapshot back: %v", err)
	}

	// The same file, as though it had been copied to this module from the strip in the other room.
	held.ID = ""
	held.Device.Name = "10.0.0.253"
	held.Device.Address = "http://10.0.0.253"
	held.Device.Controller = "CyberDesk Two"

	other, err := saveSnapshot(under.dir, held, *under.now)
	if err != nil {
		t.Fatalf("write the other light's snapshot: %v", err)
	}

	status, body := under.restore(t, other.ID, true)
	if status != http.StatusConflict {
		t.Fatalf("a snapshot from another light answered %d, want %d: %v", status, http.StatusConflict, body)
	}

	if code, _ := body["code"].(string); !strings.HasSuffix(code, "RESTORE_REFUSED") {
		t.Fatalf("the refusal is %q, want RESTORE_REFUSED", code)
	}

	detail, _ := body["detail"].(string)
	if !strings.Contains(detail, "10.0.0.253") || !strings.Contains(detail, "10.0.0.136") {
		t.Fatalf("the refusal says %q, want it to name both lights", detail)
	}

	if written := under.light.written(); len(written) != 0 {
		t.Fatalf("a refused restore wrote %d bodies to the light, want none", len(written))
	}
}

// TestARestoreRefusesASnapshotFromAnotherDisplay is the shape half of the same rule: a
// configuration written to a panel it was not made for is a scrambled picture, not a restore.
func TestARestoreRefusesASnapshotFromAnotherDisplay(t *testing.T) {
	under := newDevicesUnderTest(t)
	taken := under.take(t)

	// The light this address answers with is now the operator's panel: same address, same name,
	// 16,384 pixels instead of 48.
	under.light.mu.Lock()
	under.light.info = wled.Info{Name: "CyberDesk", Version: "16.0.1", LEDCount: 16384, MatrixWidth: 256, MatrixHeight: 64}
	under.light.mu.Unlock()

	status, body := under.restore(t, taken, true)
	if status != http.StatusConflict {
		t.Fatalf("a snapshot of another display answered %d, want %d: %v", status, http.StatusConflict, body)
	}

	if code, _ := body["code"].(string); !strings.HasSuffix(code, "RESTORE_REFUSED") {
		t.Fatalf("the refusal is %q, want RESTORE_REFUSED", code)
	}

	detail, _ := body["detail"].(string)
	if !strings.Contains(detail, "48") || !strings.Contains(detail, "16384") {
		t.Fatalf("the refusal says %q, want it to name both pixel counts", detail)
	}

	if written := under.light.written(); len(written) != 0 {
		t.Fatalf("a refused restore wrote %d bodies to the light, want none", len(written))
	}
}

// TestARestoreWaitsForTheLightAndReportsTheOutage is the whole of the writing path: the body that
// goes out, the fact that the picture is not believed until the light has been gone and come back,
// and the numbers the answer carries.
func TestARestoreWaitsForTheLightAndReportsTheOutage(t *testing.T) {
	under := newDevicesUnderTest(t)
	taken := under.take(t)

	// The light takes the write, is gone for three polls, and answers afterwards.
	under.light.mu.Lock()
	under.light.refuses = 3
	under.light.mu.Unlock()

	status, body := under.restore(t, taken, true)
	if status != http.StatusOK {
		t.Fatalf("the restore answered %d, want %d: %v", status, http.StatusOK, body)
	}

	if restored := readObject(t, body, "restored"); restored["id"] != taken {
		t.Fatalf("the answer says it restored %v, want %s", restored["id"], taken)
	}

	plan := readObject(t, body, "restore")

	// The eight sections the requirements restore, presets among them, in one list.
	wanted := []string{"light", "def", "if", "timers", "um", "ap", "id", "presets"}

	got := make([]string, 0, len(wanted))
	for _, one := range plan["sections"].([]any) {
		got = append(got, one.(string))
	}

	if strings.Join(got, ",") != strings.Join(wanted, ",") {
		t.Fatalf("the restore writes %v, want %v", got, wanted)
	}

	// And every section it refuses, by name, with a reason.
	withheld := map[string]string{}
	for _, entry := range plan["withheld"].([]any) {
		one := entry.(map[string]any)
		withheld[one["section"].(string)], _ = one["why"].(string)
	}

	for _, section := range []string{"rev", "vid", "hw", "ol", "ota", "remote", "wifi"} {
		if withheld[section] == "" {
			t.Fatalf("the restore does not say why %q is withheld: %v", section, withheld)
		}
	}

	// The reboot, measured rather than assumed.
	reboot := readObject(t, body, "reboot")

	if requested, _ := reboot["requested"].(bool); !requested {
		t.Fatalf("the restore does not report having asked for the restart: %v", reboot)
	}

	if observed, _ := reboot["observed"].(bool); !observed {
		t.Fatalf("the restore did not see the light go away and come back: %v", reboot)
	}

	if answered, _ := reboot["answered_after_seconds"].(float64); answered < 1.0 {
		t.Fatalf("the restore believes an answer %.3f s after the write, and the settle window is a second: %v",
			answered, reboot)
	}

	if failures, _ := reboot["failed_polls"].(float64); failures != 3 {
		t.Fatalf("the restore counted %v failed polls, want the 3 the light was away for: %v", failures, reboot)
	}

	// The reference measurement travels with the answer, so a page can compare this light against
	// the one the route was built from.
	if reference, _ := reboot["measured_reference"].([]any); len(reference) != 2 {
		t.Fatalf("the answer carries no measured reference: %v", reboot)
	}
}

// TestTheRestoreBodyCarriesTheNetworkSectionWithoutItsCredential is the measurement this whole item
// rests on: omitting nw clears the light's linked remote, and writing nw.ins would be writing a
// credential back out of a file. So the section goes, whole, minus the one member.
func TestTheRestoreBodyCarriesTheNetworkSectionWithoutItsCredential(t *testing.T) {
	under := newDevicesUnderTest(t)
	taken := under.take(t)

	status, body := under.restore(t, taken, true)
	if status != http.StatusOK {
		t.Fatalf("the restore answered %d: %v", status, body)
	}

	written := under.light.written()
	if len(written) != 1 {
		t.Fatalf("the restore wrote %d bodies to the light, want one", len(written))
	}

	var sent map[string]json.RawMessage
	if err := json.Unmarshal(written[0], &sent); err != nil {
		t.Fatalf("the body written to the light is not an object: %v", err)
	}

	// The network section is there, and the credential is not.
	network, carried := sent["nw"]
	if !carried {
		t.Fatalf("the body carries no nw section, and a write without one clears the light's linked remote: %s", written[0])
	}

	var members map[string]json.RawMessage
	if err := json.Unmarshal(network, &members); err != nil {
		t.Fatalf("the body's nw section is not an object: %v", err)
	}

	if _, held := members["ins"]; held {
		t.Fatalf("the body writes nw.ins back: %s", network)
	}

	// Everything else in the section survives: the measurement's whole point is that the section
	// has to be sent, not that it has to be emptied.
	for _, member := range []string{"dns", "linked_remote"} {
		if _, held := members[member]; !held {
			t.Fatalf("the body's nw section dropped %q: %s", member, network)
		}
	}

	// The restart is asked for in the body, which is the one request: a second one could be lost.
	var restart bool
	if err := json.Unmarshal(sent["rb"], &restart); err != nil || !restart {
		t.Fatalf("the body does not ask for a restart (rb: true): %s", written[0])
	}

	// The presets go with it, from their own document.
	if _, carried := sent["presets"]; !carried {
		t.Fatalf("the body carries no presets: %s", written[0])
	}

	// And the three refusals hold in the body itself, not only in the answer's table.
	for _, section := range []string{"rev", "vid", "hw", "ol", "ota", "remote", "wifi"} {
		if _, carried := sent[section]; carried {
			t.Fatalf("the body writes %q, which the restore refuses: %s", section, written[0])
		}
	}
}

// TestTheSafetySnapshotIsWrittenBeforeTheConfigurationAndPutsTheLightBack is the reversibility the
// route promises: a copy of the light as it is now is on disk at the moment the write goes out, and
// it is a different copy from the one being restored.
func TestTheSafetySnapshotIsWrittenBeforeTheConfigurationAndPutsTheLightBack(t *testing.T) {
	under := newDevicesUnderTest(t)
	taken := under.take(t)

	// The light changes after the snapshot was taken, which is the case a restore is for: the copy
	// on disk is the configuration the operator wants back, and the safety copy must be the light
	// as the restore found it.
	under.light.mu.Lock()
	under.light.config = json.RawMessage(`{"light": {"scale-bri": 42}, "def": {}, "if": {}, "timers": {}, "um": {}, "ap": {}, "id": {}, "nw": {}}`)
	under.light.mu.Unlock()

	var atWrite []string
	under.light.onWrite = func() {
		entries, err := os.ReadDir(snapshotFolder(under.dir))
		if err != nil {
			return
		}

		for _, entry := range entries {
			atWrite = append(atWrite, strings.TrimSuffix(entry.Name(), snapshotSuffix))
		}
	}

	status, body := under.restore(t, taken, true)
	if status != http.StatusOK {
		t.Fatalf("the restore answered %d: %v", status, body)
	}

	safety := readObject(t, body, "safety_snapshot")

	safetyID, _ := safety["id"].(string)
	if safetyID == "" || safetyID == taken {
		t.Fatalf("the safety snapshot is %q, want a new copy beside the one being restored (%q)", safetyID, taken)
	}

	// It was there when the configuration went out, and not after.
	found := false

	for _, id := range atWrite {
		if id == safetyID {
			found = true
		}
	}

	if !found {
		t.Fatalf("at the moment of the write the folder held %v and the safety snapshot is %s: it was not taken first",
			atWrite, safetyID)
	}

	// And it is the light as the restore found it, not the configuration being restored into it.
	saved, err := loadSnapshot(under.dir, safetyID)
	if err != nil {
		t.Fatalf("read the safety snapshot: %v", err)
	}

	if !strings.Contains(string(saved.Config), "42") {
		t.Fatalf("the safety snapshot holds %s, want the configuration the light had when the restore ran", saved.Config)
	}
}

// TestARestoreThatNeverSeesTheLightAnswerReportsFailure is the sentence the UI must be able to
// trust: a write that went out and a light that never came back is not a success.
func TestARestoreThatNeverSeesTheLightAnswerReportsFailure(t *testing.T) {
	under := newDevicesUnderTest(t)
	taken := under.take(t)

	under.light.mu.Lock()
	under.light.goneAfterWrite = true
	under.light.mu.Unlock()

	status, body := under.restore(t, taken, true)
	if status != http.StatusGatewayTimeout {
		t.Fatalf("a light that never came back answered %d, want %d: %v", status, http.StatusGatewayTimeout, body)
	}

	if code, _ := body["code"].(string); !strings.HasSuffix(code, "RESTORE_NOT_ANSWERED") {
		t.Fatalf("the refusal is %q, want RESTORE_NOT_ANSWERED", code)
	}

	// The write did go out — that is the fact the answer has to carry — and the safety snapshot is
	// named so that a person can act on it either way.
	if written := under.light.written(); len(written) != 1 {
		t.Fatalf("the light was written %d bodies, want the one that was then not answered", len(written))
	}

	params, _ := body["params"].(map[string]any)
	if safety, _ := params["safety_snapshot"].(string); safety == "" {
		t.Fatalf("the refusal names no safety snapshot: %v", body)
	}

	detail, _ := body["detail"].(string)
	if !strings.Contains(detail, "may be about to answer") {
		t.Fatalf("the refusal says %q, want it to say the restore may still have worked", detail)
	}
}

// TestAnAnswerInsideTheSettleWindowIsNotBelieved is the trap the wait exists for: the light serves
// for a moment after taking a configuration and before it restarts, so the first poll that succeeds
// is very likely the light that is about to disappear.
func TestAnAnswerInsideTheSettleWindowIsNotBelieved(t *testing.T) {
	under := newDevicesUnderTest(t)
	taken := under.take(t)

	under.light.mu.Lock()
	under.light.answerAtOnce = true
	under.light.mu.Unlock()

	status, body := under.restore(t, taken, true)
	if status != http.StatusOK {
		t.Fatalf("the restore answered %d: %v", status, body)
	}

	reboot := readObject(t, body, "reboot")

	if answered, _ := reboot["answered_after_seconds"].(float64); answered < 1.0 {
		t.Fatalf("an answer %.3f s after the write was believed, and the settle window is a second: %v",
			answered, reboot)
	}

	if observed, _ := reboot["observed"].(bool); observed {
		t.Fatalf("a light that never went away is reported as having restarted: %v", reboot)
	}

	note, _ := reboot["note"].(string)
	if !strings.Contains(note, "answered throughout") {
		t.Fatalf("the answer says %q, want it to say the light answered throughout", note)
	}
}

// TestASnapshotOfALightThisModuleDoesNotDriveIsRefused keeps the surface on the lights this module
// is responsible for: a route that copied any address a caller named would read somebody else's
// device.
func TestASnapshotOfALightThisModuleDoesNotDriveIsRefused(t *testing.T) {
	under := newDevicesUnderTest(t)

	recorder := httptest.NewRecorder()
	under.mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/devices/10.0.0.99/snapshot", nil))

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("a light this module does not drive answered %d, want %d: %s",
			recorder.Code, http.StatusNotFound, recorder.Body.String())
	}

	if code := problemCode(t, recorder); !strings.HasSuffix(code, "CONTROLLER_NOT_FOUND") {
		t.Fatalf("the refusal is %q, want CONTROLLER_NOT_FOUND", code)
	}
}

// TestASnapshotIdIsAFileNameAndNothingElse is the guard a route reading an id from a caller needs:
// a path is not a snapshot.
func TestASnapshotIdIsAFileNameAndNothingElse(t *testing.T) {
	under := newDevicesUnderTest(t)
	under.take(t)

	for _, id := range []string{"../config", ".hidden", "a/b", `a\b`, ""} {
		if _, err := loadSnapshot(under.dir, id); !errors.Is(err, errNoSnapshot) {
			t.Fatalf("the id %q was resolved to something: %v", id, err)
		}
	}
}
