// The module's own configuration store: what one document at one version does, and what it does
// with a document an older release wrote.
//
// Every test here works on a directory of its own. Nothing touches a device, a network or the
// process's real store: a store test that read the one this module is running on would pass or
// fail with whatever the operator happens to have configured.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/prusa3d/prusa-connect/sdk/deviceapi"
)

// writeStoreFile puts one document in a fresh directory and hands the directory back, which is
// how a fixture written by another release is made.
func writeStoreFile(t *testing.T, document string) string {
	t.Helper()

	dir := t.TempDir()

	if err := os.WriteFile(storePath(dir), []byte(document), 0o600); err != nil {
		t.Fatalf("write the fixture store: %v", err)
	}

	return dir
}

// storedRaw is the document as it sits on disk, for the assertions about what a write leaves
// behind rather than about what a read made of it.
func storedRaw(t *testing.T, dir string) map[string]any {
	t.Helper()

	raw, err := os.ReadFile(storePath(dir))
	if err != nil {
		t.Fatalf("read the store back: %v", err)
	}

	var held map[string]any
	if err := json.Unmarshal(raw, &held); err != nil {
		t.Fatalf("the store is not a JSON object: %v (%s)", err, raw)
	}

	return held
}

// TestTheStoreRoundTripsAtTheCurrentVersion is the first half of the contract: what this build
// writes, this build reads, with the version in the document and not only in the code.
func TestTheStoreRoundTripsAtTheCurrentVersion(t *testing.T) {
	dir := t.TempDir()

	set := bindings{
		{DeviceID: "printer-a", Address: "http://10.0.0.136", Segment: 3},
		{DeviceID: "printer-b", Address: "http://10.0.0.253", Segment: wholeStrip, Brightness: 128},
	}

	entries, err := storeEntries(set)
	if err != nil {
		t.Fatalf("render the set for the store: %v", err)
	}

	path, err := saveStore(dir, storedConfig{Version: storeVersion, Bindings: entries})
	if err != nil {
		t.Fatalf("save the store: %v", err)
	}

	if path != storePath(dir) {
		t.Fatalf("the store was written at %q, want %q", path, storePath(dir))
	}

	// The version is in the document, which is the whole point: a version kept only in the code
	// is a version that cannot say which shape an old document has.
	onDisk := storedRaw(t, dir)

	if version, _ := onDisk["version"].(float64); int(version) != storeVersion {
		t.Fatalf("the document on disk carries version %v, want %d", onDisk["version"], storeVersion)
	}

	if lines, _ := onDisk["bindings"].([]any); len(lines) != 2 {
		t.Fatalf("the document holds %v bindings, want 2", onDisk["bindings"])
	}

	found, err := loadStore(dir)
	if err != nil {
		t.Fatalf("load the store: %v", err)
	}

	if found.Version != storeVersion || found.Upgraded {
		t.Fatalf("the read is at version %d with upgraded %v, want %d and false",
			found.Version, found.Upgraded, storeVersion)
	}

	if len(found.Config.Bindings) != 2 {
		t.Fatalf("the read holds %d bindings, want 2", len(found.Config.Bindings))
	}

	// The two lines are the same document the set was rendered from: a store that lost the
	// segment, or that read a whole-controller brightness as absent, would refuse the next start
	// with a set the operator never wrote.
	var first storedEntry
	if err := json.Unmarshal(found.Config.Bindings[0], &first); err != nil {
		t.Fatalf("read the first stored line: %v", err)
	}

	if first.Printer != "printer-a" || first.Controller != "http://10.0.0.136" ||
		first.Segment == nil || *first.Segment != 3 || first.Brightness != nil {
		t.Fatalf("the first stored line reads %+v, want printer-a on segment 3 with no brightness", first)
	}

	var second storedEntry
	if err := json.Unmarshal(found.Config.Bindings[1], &second); err != nil {
		t.Fatalf("read the second stored line: %v", err)
	}

	if second.Segment == nil || *second.Segment != wholeStrip ||
		second.Brightness == nil || *second.Brightness != 128 {
		t.Fatalf("the second stored line reads %+v, want the whole strip at brightness 128", second)
	}
}

// TestAStoreWrittenBeforeTheVersionIsUpgradedRatherThanRefused is the second half: the document
// this store would have written without a version key is read, stepped up, and written back
// stamped. It is not rejected, and it is not silently read as though it were current.
func TestAStoreWrittenBeforeTheVersionIsUpgradedRatherThanRefused(t *testing.T) {
	cases := []struct {
		name     string
		fixture  string
		bindings int
	}{
		{
			name:     "a document with one binding and no version",
			fixture:  `{"bindings": [{"printer": "printer-a", "controller": "http://10.0.0.136", "segment": 3}]}`,
			bindings: 1,
		},
		{
			name:     "a document with a whole-controller binding and no version",
			fixture:  `{"bindings": [{"printer": "printer-b", "controller": "http://10.0.0.253", "brightness": 128}]}`,
			bindings: 1,
		},
		{
			name:     "a document with no bindings at all",
			fixture:  `{"bindings": []}`,
			bindings: 0,
		},
		{
			name:     "an empty object, which is what a store with nothing configured looks like",
			fixture:  `{}`,
			bindings: 0,
		},
	}

	for _, one := range cases {
		t.Run(one.name, func(t *testing.T) {
			dir := writeStoreFile(t, one.fixture)

			found, err := loadStore(dir)
			if err != nil {
				t.Fatalf("a document written before the version was stamped was refused: %v", err)
			}

			if found.Version != storeVersionUnstamped {
				t.Fatalf("the read says the document carried version %d, want %d",
					found.Version, storeVersionUnstamped)
			}

			if !found.Upgraded {
				t.Fatal("the read does not say the document was upgraded, and it had no version")
			}

			if found.Config.Version != storeVersion {
				t.Fatalf("the upgraded document is at version %d, want %d",
					found.Config.Version, storeVersion)
			}

			if len(found.Config.Bindings) != one.bindings {
				t.Fatalf("the upgraded document holds %d bindings, want %d",
					len(found.Config.Bindings), one.bindings)
			}

			// Upgrading is not enough on its own: the document has to be written back stamped,
			// or the next start does the same upgrade again and nothing says the shape is
			// current.
			if _, err := saveStore(dir, found.Config); err != nil {
				t.Fatalf("write the upgraded store back: %v", err)
			}

			again, err := loadStore(dir)
			if err != nil {
				t.Fatalf("read the upgraded store back: %v", err)
			}

			if again.Upgraded || again.Version != storeVersion {
				t.Fatalf("the store reads at version %d with upgraded %v after being written back, want %d and false",
					again.Version, again.Upgraded, storeVersion)
			}

			if len(again.Config.Bindings) != one.bindings {
				t.Fatalf("the written-back store holds %d bindings, want %d",
					len(again.Config.Bindings), one.bindings)
			}
		})
	}
}

// TestAStoreThisBuildCannotReadIsRefusedByName is the other side of a versioned store: a document
// from a newer release, a document with a key this build does not know, or a document that is not
// one at all is refused with the file named and the reason — never half-read, and never replaced
// by the settings as though nothing had been in it.
func TestAStoreThisBuildCannotReadIsRefusedByName(t *testing.T) {
	cases := []struct {
		name    string
		version string
		fixture string
		want    []string
	}{
		{
			// Named relative to this build's own version, so that the next schema change moves the
			// fixture with it rather than turning this case into a document this build accepts.
			name: "a document from a newer release",
			fixture: fmt.Sprintf(
				`{"version": %d, "bindings": [], "states": [], "moments": [], "layouts": [], "windows": [], "pauses": []}`,
				storeVersion+1),
			want: []string{fmt.Sprintf("schema version %d", storeVersion+1), fmt.Sprintf("version %d", storeVersion)},
		},
		{
			name:    "a negative version, which no release wrote",
			fixture: `{"version": -1, "bindings": []}`,
			want:    []string{"schema version -1", "counts from zero"},
		},
		{
			name: "a key this build does not know",
			fixture: fmt.Sprintf(
				`{"version": %d, "bindings": [], "states": [], "moments": [], "layouts": [], "windows": [], "pauses": [], "colours": []}`,
				storeVersion),
			want: []string{"unknown field"},
		},
		{
			// The three lists version 3 added do not exist at version 2, and the upgrade from it
			// reads the version-2 shape. A document that claims version 2 and carries them is
			// refused rather than stepped up, which is what makes the steps separate shapes rather
			// than one lenient read.
			name:    "a version 2 document carrying a list version 2 does not have",
			fixture: `{"version": 2, "bindings": [], "states": [], "moments": [], "layouts": []}`,
			want:    []string{"schema version 2", "unknown field"},
		},
		{
			// The two tables do not exist at version 1, and the upgrade from it reads the version-1
			// shape. A document that claims version 1 and carries them is refused rather than
			// stepped up, which is what makes the steps separate shapes rather than one lenient read.
			name:    "a version 1 document carrying a table version 1 does not have",
			fixture: `{"version": 1, "bindings": [], "moments": []}`,
			want:    []string{"schema version 1", "unknown field"},
		},
		{
			name:    "a document that is not JSON",
			fixture: "bindings: printer-a\n",
			want:    []string{"not a JSON document"},
		},
	}

	for _, one := range cases {
		t.Run(one.name, func(t *testing.T) {
			dir := writeStoreFile(t, one.fixture)

			_, err := loadStore(dir)
			if err == nil {
				t.Fatalf("a document this build cannot read was accepted: %s", one.fixture)
			}

			if errors.Is(err, errNoStore) {
				t.Fatalf("the refusal reads like a missing store: %v", err)
			}

			// The path is what a person acts on: the file is his to move aside.
			if !strings.Contains(err.Error(), storePath(dir)) {
				t.Fatalf("the refusal %q does not name the file %q", err, storePath(dir))
			}

			for _, want := range one.want {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("the refusal %q does not say %q", err, want)
				}
			}
		})
	}
}

// TestADirectoryWithNoStoreIsTheFirstStartAndNotAFault is the difference the read has to make:
// nothing there is a first start, which the settings seed, and everything else is a refusal.
func TestADirectoryWithNoStoreIsTheFirstStartAndNotAFault(t *testing.T) {
	_, err := loadStore(t.TempDir())
	if !errors.Is(err, errNoStore) {
		t.Fatalf("an empty directory answered %v, want errNoStore", err)
	}
}

// TestTheStoreIsWrittenWhereTheHostGranted is that it does not land anywhere else: the document is
// inside the directory it was given, under one name, and nothing beside it is left behind.
func TestTheStoreIsWrittenWhereTheHostGranted(t *testing.T) {
	dir := t.TempDir()

	if _, err := saveStore(dir, storedConfig{Version: storeVersion}); err != nil {
		t.Fatalf("save the store: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read the granted directory: %v", err)
	}

	if len(entries) != 1 || entries[0].Name() != storeFileName {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}

		t.Fatalf("the granted directory holds %v, want %q alone", names, storeFileName)
	}

	info, err := entries[0].Info()
	if err != nil {
		t.Fatalf("stat the store: %v", err)
	}

	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("the store is mode %o, want 600", mode)
	}
}

// TestTheFirstStartWritesTheStoreFromTheSettingsAndTheNextOneReadsIt is the requirement's
// migration rule end to end: the settings seed the document once, and a settings block that
// changes afterwards does not change what the light is showing.
func TestTheFirstStartWritesTheStoreFromTheSettingsAndTheNextOneReadsIt(t *testing.T) {
	dir := t.TempDir()

	fleet := fakeFleet{page: deviceapi.Page{
		Items: []deviceapi.Device{
			{ID: "printer-a", Name: "Hermes MK4"},
			{ID: "printer-b", Name: "Mini"},
		},
		Total: 2,
	}}

	sent := Settings{Bindings: []json.RawMessage{
		json.RawMessage(`{"printer": "printer-a", "controller": "http://10.0.0.136", "segment": 3}`),
	}}

	set, origin, err := configurationFor(context.Background(), fleet, sent, dir, quiet)
	if err != nil {
		t.Fatalf("the first start refused the settings: %v", err)
	}

	if origin != originStore {
		t.Fatalf("the first start says the set came from %q, want the store", origin)
	}

	if len(set) != 1 || set[0].DeviceID != "printer-a" || set[0].Segment != 3 {
		t.Fatalf("the first start runs %+v, want printer-a on segment 3", set)
	}

	// The printer is stored by its device id, not by the name the operator typed: an id survives
	// a rename, and a store that named a printer by a name would refuse a later start for a
	// reason nobody caused.
	stored := storedRaw(t, dir)

	lines, _ := stored["bindings"].([]any)
	if len(lines) != 1 {
		t.Fatalf("the store holds %v bindings, want 1", stored["bindings"])
	}

	first, _ := lines[0].(map[string]any)
	if printer, _ := first["printer"].(string); printer != "printer-a" {
		t.Fatalf("the stored binding names printer %q, want the device id printer-a", printer)
	}

	// The second start reads the document, and the settings are a migration source: an edited
	// settings block is not what runs. That is docs/product-requirements.md §4's rule, and it is
	// pinned here because it is the one consequence an operator is most likely to be surprised
	// by.
	edited := Settings{Bindings: []json.RawMessage{
		json.RawMessage(`{"printer": "printer-b", "controller": "http://10.0.0.253", "segment": 2}`),
	}}

	again, againOrigin, err := configurationFor(context.Background(), fleet, edited, dir, quiet)
	if err != nil {
		t.Fatalf("the second start refused the store: %v", err)
	}

	if againOrigin != originStore {
		t.Fatalf("the second start says the set came from %q, want the store", againOrigin)
	}

	if len(again) != 1 || again[0].DeviceID != "printer-a" || again[0].Segment != 3 {
		t.Fatalf("the second start runs %+v, want the set the store holds, printer-a on segment 3", again)
	}
}

// TestAStartUpgradesTheStoreItFindsAndWritesItBackStamped is the upgrade as a start actually does
// it: the document an older release wrote is read, and what is on disk afterwards is stamped with
// this build's version.
func TestAStartUpgradesTheStoreItFindsAndWritesItBackStamped(t *testing.T) {
	dir := writeStoreFile(t, `{"bindings": [{"printer": "printer-a", "controller": "http://10.0.0.136", "segment": 3}]}`)

	fleet := fakeFleet{page: deviceapi.Page{
		Items: []deviceapi.Device{{ID: "printer-a", Name: "Hermes MK4"}},
		Total: 1,
	}}

	set, origin, err := configurationFor(context.Background(), fleet, Settings{}, dir, quiet)
	if err != nil {
		t.Fatalf("a start from an older store was refused: %v", err)
	}

	if origin != originStore || len(set) != 1 || set[0].Segment != 3 {
		t.Fatalf("the start runs %+v from %q, want the one stored binding from the store", set, origin)
	}

	onDisk := storedRaw(t, dir)
	if version, _ := onDisk["version"].(float64); int(version) != storeVersion {
		t.Fatalf("the store on disk carries version %v after the start, want %d", onDisk["version"], storeVersion)
	}
}

// TestAModuleGrantedNoDirectoryRunsFromItsSettings is the install that has not approved a data
// directory: everything works exactly as it did before the store existed, and nothing is written
// anywhere.
func TestAModuleGrantedNoDirectoryRunsFromItsSettings(t *testing.T) {
	fleet := fakeFleet{page: deviceapi.Page{
		Items: []deviceapi.Device{{ID: "printer-a", Name: "Hermes MK4"}},
		Total: 1,
	}}

	for _, dir := range []string{"", "   "} {
		set, origin, err := configurationFor(context.Background(), fleet, Settings{
			Bindings: []json.RawMessage{
				json.RawMessage(`{"printer": "printer-a", "controller": "http://10.0.0.136", "segment": 3}`),
			},
		}, dir, quiet)
		if err != nil {
			t.Fatalf("a module granted %q refused its settings: %v", dir, err)
		}

		if origin != originSettings {
			t.Fatalf("a module granted %q says its set came from %q, want the settings", dir, origin)
		}

		if len(set) != 1 || set[0].DeviceID != "printer-a" {
			t.Fatalf("a module granted %q runs %+v, want printer-a from the settings", dir, set)
		}
	}
}

// TestAStoreNamingAPrinterTheHostDoesNotHoldIsRefusedNamingTheFile is the refusal a person can act
// on: the file is the thing to edit, so the sentence names it.
func TestAStoreNamingAPrinterTheHostDoesNotHoldIsRefusedNamingTheFile(t *testing.T) {
	dir := writeStoreFile(t, `{"bindings": [{"printer": "ghost", "controller": "http://10.0.0.136"}]}`)

	fleet := fakeFleet{page: deviceapi.Page{
		Items: []deviceapi.Device{{ID: "printer-a", Name: "Hermes MK4"}},
		Total: 1,
	}}

	_, _, err := configurationFor(context.Background(), fleet, Settings{}, dir, quiet)
	if err == nil {
		t.Fatal("a store naming a printer the host does not hold was accepted")
	}

	if !strings.Contains(err.Error(), storePath(dir)) {
		t.Fatalf("the refusal %q does not name the file %q", err, storePath(dir))
	}

	if !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("the refusal %q does not name the printer that missed", err)
	}
}

// TestAVersionOneStoreIsSteppedUpToTwoAndKeepsItsBindings is the upgrade this wave builds on: the
// document the first release wrote is read, stepped up one version at a time to this build's
// version, and written back stamped — with the bindings it held untouched and every new list
// arriving empty rather than filled with a copy of the code's own defaults.
func TestAVersionOneStoreIsSteppedUpToTwoAndKeepsItsBindings(t *testing.T) {
	dir := writeStoreFile(t, `{"version": 1, "bindings": [{"printer": "printer-a", "controller": "http://10.0.0.136", "segment": 3}]}`)

	found, err := loadStore(dir)
	if err != nil {
		t.Fatalf("a version 1 document was refused: %v", err)
	}

	if found.Version != 1 {
		t.Fatalf("the read says the document carried version %d, want 1", found.Version)
	}

	if !found.Upgraded {
		t.Fatal("the read does not say the document was upgraded, and it was written at version 1")
	}

	if found.Config.Version != storeVersion || storeVersion != 7 {
		t.Fatalf("the upgraded document is at version %d, want %d", found.Config.Version, storeVersion)
	}

	// The bindings are the previous release's own record and must cross the upgrade unchanged: a
	// step that dropped them would leave a light that stopped showing a printer at the next start,
	// for a reason nothing on the instance could explain.
	if len(found.Config.Bindings) != 1 {
		t.Fatalf("the upgraded document holds %d bindings, want the one it had", len(found.Config.Bindings))
	}

	var kept storedEntry
	if err := json.Unmarshal(found.Config.Bindings[0], &kept); err != nil {
		t.Fatalf("read the binding the upgrade kept: %v", err)
	}

	if kept.Printer != "printer-a" || kept.Segment == nil || *kept.Segment != 3 {
		t.Fatalf("the binding the upgrade kept reads %+v, want printer-a on segment 3", kept)
	}

	// Empty and not defaulted: a row copied in here would be a second statement of Appendix A,
	// drifting from internal/render one release later.
	if found.Config.States == nil || len(found.Config.States) != 0 {
		t.Fatalf("the upgraded document holds %d state rows, want none copied in", len(found.Config.States))
	}

	if found.Config.Moments == nil || len(found.Config.Moments) != 0 {
		t.Fatalf("the upgraded document holds %d moment rows, want none copied in", len(found.Config.Moments))
	}

	if _, err := saveStore(dir, found.Config); err != nil {
		t.Fatalf("write the upgraded store back: %v", err)
	}

	onDisk := storedRaw(t, dir)
	if version, _ := onDisk["version"].(float64); int(version) != storeVersion {
		t.Fatalf("the store on disk carries version %v after the upgrade, want %d", onDisk["version"], storeVersion)
	}

	if lines, _ := onDisk["bindings"].([]any); len(lines) != 1 {
		t.Fatalf("the store on disk holds %v bindings after the upgrade, want 1", onDisk["bindings"])
	}

	// Every list is written as empty rather than as null, so that an upgraded document and one this
	// build wrote from scratch are the same document on disk.
	for _, name := range []string{"states", "moments", "layouts", "windows", "pauses"} {
		held, present := onDisk[name].([]any)
		if !present || len(held) != 0 {
			t.Fatalf("the store on disk carries %q as %v, want an empty list", name, onDisk[name])
		}
	}

	again, err := loadStore(dir)
	if err != nil {
		t.Fatalf("read the upgraded store back: %v", err)
	}

	if again.Upgraded || again.Version != storeVersion {
		t.Fatalf("the store reads at version %d with upgraded %v after being written back, want %d and false",
			again.Version, again.Upgraded, storeVersion)
	}
}

// TestAVersionTwoStoreIsSteppedToThreeAndKeepsBothTables is a run of upgrade steps, on its own: a
// document the previous release wrote, with a binding, a state override and a moment entry in it, is
// stepped up to the current version — and everything it held is still there afterwards, with the
// three lists version 3 added arriving empty and the binding named by version 4's step.
//
// The bindings, states and moments are asserted one by one because a step that rebuilt the document
// from a struct with three fields would compile, run, and silently drop the two tables the Colours
// and Events screens had already filled in.
func TestAVersionTwoStoreIsSteppedUpAndKeepsBothTables(t *testing.T) {
	dir := writeStoreFile(t, `{
  "version": 2,
  "bindings": [{"printer": "printer-a", "controller": "http://10.0.0.136", "segment": 3}],
  "states": [{"state": "PAUSED", "colour": {"r": 1, "g": 2, "b": 3}, "word": "HOLD", "holds": "60s"}],
  "moments": [{"light": "10.0.0.253", "printer": "printer-a", "moment": "print-started",
               "enabled": true, "shows": "Preset", "which": "Snow Fall", "for": "30s"}]
}`)

	found, err := loadStore(dir)
	if err != nil {
		t.Fatalf("a version 2 document was refused: %v", err)
	}

	if found.Version != 2 {
		t.Fatalf("the read says the document carried version %d, want 2", found.Version)
	}

	if !found.Upgraded {
		t.Fatal("the read does not say the document was upgraded, and it was written at version 2")
	}

	if found.Config.Version != storeVersion {
		t.Fatalf("the upgraded document is at version %d, want %d", found.Config.Version, storeVersion)
	}

	if len(found.Config.Bindings) != 1 {
		t.Fatalf("the upgraded document holds %d bindings, want the one it had", len(found.Config.Bindings))
	}

	if len(found.Config.States) != 1 {
		t.Fatalf("the upgraded document holds %d state rows, want the one it had", len(found.Config.States))
	}

	if word := found.Config.States[0].Word; word == nil || *word != "HOLD" {
		t.Fatalf("the state row the upgrade kept reads %+v, want the word HOLD", found.Config.States[0])
	}

	if len(found.Config.Moments) != 1 {
		t.Fatalf("the upgraded document holds %d moment rows, want the one it had", len(found.Config.Moments))
	}

	if which := found.Config.Moments[0].Which; which != "Snow Fall" {
		t.Fatalf("the moment row the upgrade kept reads %+v, want the preset Snow Fall", found.Config.Moments[0])
	}

	// The three lists arrive empty rather than filled with the code's own defaults, for the reason
	// the two tables did: a default copied into the file is a second statement to keep in step by
	// hand, and this is a document that has never had a layout, a window or a pause in it.
	if found.Config.Layouts == nil || len(found.Config.Layouts) != 0 {
		t.Fatalf("the upgraded document holds %d layout rows, want none copied in", len(found.Config.Layouts))
	}

	if found.Config.Windows == nil || len(found.Config.Windows) != 0 {
		t.Fatalf("the upgraded document holds %d brightness windows, want none copied in", len(found.Config.Windows))
	}

	if found.Config.Pauses == nil || len(found.Config.Pauses) != 0 {
		t.Fatalf("the upgraded document holds %d pauses, want none copied in", len(found.Config.Pauses))
	}

	if _, err := saveStore(dir, found.Config); err != nil {
		t.Fatalf("write the upgraded store back: %v", err)
	}

	onDisk := storedRaw(t, dir)
	if version, _ := onDisk["version"].(float64); int(version) != storeVersion {
		t.Fatalf("the store on disk carries version %v after the upgrade, want %d", onDisk["version"], storeVersion)
	}

	for _, name := range []string{"layouts", "windows", "pauses"} {
		held, present := onDisk[name].([]any)
		if !present || len(held) != 0 {
			t.Fatalf("the store on disk carries %q as %v, want an empty list", name, onDisk[name])
		}
	}
}

// TestAStoreFromBeforeTheVersionStepsThroughEveryShapeToTheCurrentOne is the other half: a
// document written before the version was stamped does not jump to the current shape, it is
// stepped one version at a time — which is the whole reason the version is stamped.
func TestAStoreFromBeforeTheVersionStepsThroughEveryShapeToTheCurrentOne(t *testing.T) {
	cases := []struct {
		name    string
		fixture string
	}{
		{
			name:    "a document with a binding and no version",
			fixture: `{"bindings": [{"printer": "printer-b", "controller": "http://10.0.0.253", "brightness": 128}]}`,
		},
		{
			name:    "an empty object",
			fixture: `{}`,
		},
	}

	for _, one := range cases {
		t.Run(one.name, func(t *testing.T) {
			dir := writeStoreFile(t, one.fixture)

			found, err := loadStore(dir)
			if err != nil {
				t.Fatalf("a document written before the version was stamped was refused: %v", err)
			}

			if found.Version != storeVersionUnstamped {
				t.Fatalf("the read says the document carried version %d, want 0", found.Version)
			}

			if !found.Upgraded {
				t.Fatal("the read does not say the document was upgraded, and it carried no version")
			}

			if found.Config.Version != storeVersion {
				t.Fatalf("the document reached version %d, want %d", found.Config.Version, storeVersion)
			}

			// All four steps ran: the version was stamped, the two tables were added, the three
			// version-3 lists were added after them, and version 4 named every binding. A step
			// that jumped from 0 straight to the current version would answer here with the
			// bindings intact and everything after it still nil.
			if found.Config.States == nil || found.Config.Moments == nil {
				t.Fatalf("the stepped document has states %v and moments %v, want both present and empty",
					found.Config.States, found.Config.Moments)
			}

			if found.Config.Layouts == nil || found.Config.Windows == nil || found.Config.Pauses == nil {
				t.Fatalf("the stepped document has layouts %v, windows %v and pauses %v, want all three present and empty",
					found.Config.Layouts, found.Config.Windows, found.Config.Pauses)
			}

			// The fourth step, on the line a document written before the version was stamped does
			// carry: a pre-version document that named a printer gets that printer's device id as
			// its binding's name, which is the path it answered at before names existed.
			for _, raw := range found.Config.Bindings {
				var line storedEntry
				if err := json.Unmarshal(raw, &line); err != nil {
					t.Fatalf("read a stepped binding line: %v", err)
				}

				if line.ID != line.Printer || line.ID == "" {
					t.Fatalf("the stepped binding line reads %+v, want its id to be the printer's device id %q",
						line, line.Printer)
				}
			}
		})
	}
}

// TestAStoreAtVersionTwoCarriesBothTablesThroughARoundTrip is the tables' half of the store's
// contract: what this build writes, this build reads, with only the fields a person changed — and
// with the moment rows keyed by the pair, so that one printer on one light is not the same row as
// the same printer on another.
func TestAStoreAtVersionTwoCarriesBothTablesThroughARoundTrip(t *testing.T) {
	dir := t.TempDir()

	word := "PAUSE"
	holds := hold60s

	written := storedConfig{
		Version: storeVersion,
		States: []storedState{
			{State: "PAUSED", Colour: &storedColour{R: 1, G: 2, B: 3}, Word: &word, Holds: &holds},
			{State: disconnectedState, Word: &word},
		},
		Moments: []storedMoment{
			{Light: "10.0.0.253", Printer: "printer-a", Moment: momentPrintStarted,
				momentEntry: momentEntry{Enabled: true, Shows: showsPreset, Which: "Snow Fall", For: hold30s}},
			{Light: "10.0.0.253", Printer: "printer-b", Moment: momentPrintStarted,
				momentEntry: momentEntry{Enabled: false, Shows: showsNothing, Which: "", For: durationForever}},
		},
	}

	if _, err := saveStore(dir, written); err != nil {
		t.Fatalf("save the store: %v", err)
	}

	found, err := loadStore(dir)
	if err != nil {
		t.Fatalf("load the store: %v", err)
	}

	if len(found.Config.States) != 2 {
		t.Fatalf("the read holds %d state rows, want 2", len(found.Config.States))
	}

	if len(found.Config.Moments) != 2 {
		t.Fatalf("the read holds %d moment rows, want 2", len(found.Config.Moments))
	}

	// The two moment rows are one moment on one light for two different printers, and they carry
	// different entries: that pair is the key, and a store that collapsed them would be a
	// multi-room light showing one room's choice in both.
	first, ok := storedMomentFor(found.Config, "10.0.0.253", "printer-a", momentPrintStarted)
	if !ok || first.Shows != showsPreset || first.Which != "Snow Fall" {
		t.Fatalf("the first stored moment reads %+v, want the print-started preset on printer-a", first)
	}

	second, ok := storedMomentFor(found.Config, "10.0.0.253", "printer-b", momentPrintStarted)
	if !ok || second.Enabled || second.Shows != showsNothing {
		t.Fatalf("the second stored moment reads %+v, want it off and showing nothing on printer-b", second)
	}

	// The same printer on another light is another row: nothing here keys on the printer alone.
	if _, ok := storedMomentFor(found.Config, "10.0.0.136", "printer-a", momentPrintStarted); ok {
		t.Fatal("a moment stored on one light was found on another, and the key is the pair")
	}
}

// TestAVersionThreeStoreKeepsItsLayoutThroughTheRekeying is the step this wave added, on the
// document the operator is actually running: one whole-controller binding at brightness 128, no
// state, moment, window or pause rows, and a layout row keyed on the printer because version 3 had
// no other key to use.
//
// Two things have to survive it and they are different claims. The row has to still be there, with
// every field somebody set — a step that rebuilt the table from a struct would compile, run and
// quietly empty it. And it has to be readable *for that binding*: the key moves from the printer to
// the binding's name, and a row left on the old key would be a row nothing reads while the panel
// went back to the built-in default in the middle of a print.
func TestAVersionThreeStoreKeepsItsLayoutThroughTheRekeying(t *testing.T) {
	const panel = "07e893bb-b922-4ff0-9ad3-061b957400c3"

	dir := writeStoreFile(t, `{
  "version": 3,
  "bindings": [{"printer": "`+panel+`", "controller": "http://10.0.0.136", "segment": -1, "brightness": 128}],
  "states": [],
  "moments": [],
  "layouts": [{"printer": "`+panel+`", "bar_band": "top", "bar_rows": 6, "percent_scale": 3}],
  "windows": [],
  "pauses": []
}`)

	found, err := loadStore(dir)
	if err != nil {
		t.Fatalf("a version 3 document was refused: %v", err)
	}

	if found.Version != 3 {
		t.Fatalf("the read says the document carried version %d, want 3", found.Version)
	}

	if !found.Upgraded {
		t.Fatal("the read does not say the document was upgraded, and it was written at version 3")
	}

	if found.Config.Version != storeVersion {
		t.Fatalf("the upgraded document is at version %d, want %d", found.Config.Version, storeVersion)
	}

	// The binding gained a name, and it is the printer's own device id: that is the path the panel
	// answered at before this version existed, so nothing a screen holds stops working.
	if len(found.Config.Bindings) != 1 {
		t.Fatalf("the upgraded document holds %d bindings, want the one it had", len(found.Config.Bindings))
	}

	var line storedEntry
	if err := json.Unmarshal(found.Config.Bindings[0], &line); err != nil {
		t.Fatalf("read the stepped binding line: %v", err)
	}

	if line.ID != panel || line.Printer != panel || line.Controller != "http://10.0.0.136" ||
		line.Segment == nil || *line.Segment != -1 || line.Brightness == nil || *line.Brightness != 128 {
		t.Fatalf("the stepped binding line reads %+v, want the panel at id %q, whole, brightness 128", line, panel)
	}

	// The layout row is the row, and every field it carried is the field it carries.
	if len(found.Config.Layouts) != 1 {
		t.Fatalf("the upgraded document holds %d layout rows, want the one it had", len(found.Config.Layouts))
	}

	row := found.Config.Layouts[0]
	if row.Binding != panel {
		t.Fatalf("the layout row is keyed on %q, want the binding's name %q", row.Binding, panel)
	}

	if row.BarBand == nil || *row.BarBand != "top" || row.BarRows == nil || *row.BarRows != 6 ||
		row.PercentScale == nil || *row.PercentScale != 3 {
		t.Fatalf("the layout row the upgrade kept reads %+v, want band top, six rows and scale 3", row)
	}

	// And it is read for that binding: the panel finds its row, and a second binding of the same
	// printer — the strip this version exists for — finds nothing rather than the panel's picture.
	rows, err := layoutRows(found.Config)
	if err != nil {
		t.Fatalf("the upgraded layout table cannot be read: %v", err)
	}

	resolved, held := layoutFor(rows, panel)
	if !held {
		t.Fatalf("the upgraded layout row is not found for binding %q", panel)
	}

	if resolved.BarBand == nil || *resolved.BarBand != "top" || resolved.BarRows == nil || *resolved.BarRows != 6 {
		t.Fatalf("the layout read for %q is %+v, want the row's own band and rows", panel, resolved)
	}

	if _, held := layoutFor(rows, panel+"@10.0.0.253:whole"); held {
		t.Fatal("the panel's layout row was read for a second binding of the same printer")
	}

	// On disk the key is `binding` and there is no `printer` left in the row: a document that kept
	// both would be two keys for one fact, and the next release would have to guess which one wins.
	if _, err := saveStore(dir, found.Config); err != nil {
		t.Fatalf("write the upgraded store back: %v", err)
	}

	onDisk := storedRaw(t, dir)
	if version, _ := onDisk["version"].(float64); int(version) != storeVersion {
		t.Fatalf("the store on disk carries version %v after the upgrade, want %d", onDisk["version"], storeVersion)
	}

	layouts, _ := onDisk["layouts"].([]any)
	if len(layouts) != 1 {
		t.Fatalf("the store on disk holds %v layout rows, want 1", onDisk["layouts"])
	}

	written := layouts[0].(map[string]any)
	if written["binding"] != panel {
		t.Fatalf("the row on disk is keyed on %v, want %q", written["binding"], panel)
	}

	if _, held := written["printer"]; held {
		t.Fatalf("the row on disk still carries a printer key: %v", written)
	}
}

// TestAVersionFourStoreStepsToFiveAndKeepsEveryTable is this wave's step, on a document with a row
// in every table — which is the operator's own install, plus the three tables he has not filled in.
//
// Two claims. Nothing is dropped: the bindings, the state row and its word and hold, the moment
// rows, the layout row, the windows and the pause all cross the step as they were. And nothing is
// invented: version 4 wrote no write-moment for a state's word, so no row here gains one, and every
// word keeps the rule version 4 had — its hold counts from the state's own entry.
func TestAVersionFourStoreStepsToFiveAndKeepsEveryTable(t *testing.T) {
	const panel = "07e893bb-b922-4ff0-9ad3-061b957400c3"

	dir := writeStoreFile(t, `{
  "version": 4,
  "bindings": [{"id": "`+panel+`", "printer": "`+panel+`", "controller": "http://10.0.0.136", "segment": -1, "brightness": 128}],
  "states": [{"state": "FINISHED", "word": "SWEEP", "holds": "60s", "colour": {"r": 0, "g": 170, "b": 0}}],
  "moments": [{"light": "10.0.0.136", "printer": "`+panel+`", "moment": "print-started", "enabled": true, "shows": "word", "which": "GO"}],
  "layouts": [{"binding": "`+panel+`", "bar_band": "top", "bar_rows": 6}],
  "windows": [{"from": "08:00", "to": "18:00", "brightness": 180}],
  "pauses": [{"scope": "global", "chosen": "1h", "set_at": "2026-11-19T12:00:00Z", "expires": "2026-11-19T13:00:00Z"}]
}`)

	found, err := loadStore(dir)
	if err != nil {
		t.Fatalf("a version 4 document was refused: %v", err)
	}

	if found.Version != 4 {
		t.Fatalf("the read says the document carried version %d, want 4", found.Version)
	}

	if !found.Upgraded {
		t.Fatal("the read does not say the document was upgraded, and it was written at version 4")
	}

	if found.Config.Version != storeVersion {
		t.Fatalf("the upgraded document is at version %d, want %d", found.Config.Version, storeVersion)
	}

	// Every table is the table it was. The row counts are the claim that nothing was dropped; the
	// fields below are the claim that nothing was rewritten.
	if len(found.Config.Bindings) != 1 || len(found.Config.States) != 1 || len(found.Config.Moments) != 1 ||
		len(found.Config.Layouts) != 1 || len(found.Config.Windows) != 1 || len(found.Config.Pauses) != 1 {
		t.Fatalf("the stepped document holds %d bindings, %d states, %d moments, %d layouts, %d windows and %d pauses, "+
			"want one of each", len(found.Config.Bindings), len(found.Config.States), len(found.Config.Moments),
			len(found.Config.Layouts), len(found.Config.Windows), len(found.Config.Pauses))
	}

	var line storedEntry
	if err := json.Unmarshal(found.Config.Bindings[0], &line); err != nil {
		t.Fatalf("read the stepped binding line: %v", err)
	}

	if line.ID != panel || line.Printer != panel || line.Brightness == nil || *line.Brightness != 128 {
		t.Fatalf("the stepped binding line reads %+v, want the panel at id %q, brightness 128", line, panel)
	}

	row := found.Config.States[0]
	if row.State != "FINISHED" || row.Word == nil || *row.Word != "SWEEP" || row.Holds == nil || *row.Holds != hold60s {
		t.Fatalf("the stepped state row reads %+v, want FINISHED carrying SWEEP for 60s", row)
	}

	// The one thing this version adds, and the one thing a version-4 row must not be given: no
	// moment. A stamp here would be a hold that begins at the upgrade, and every word on an install
	// that upgraded would appear for its length on a light nobody had touched.
	if row.SetAt != nil {
		t.Fatalf("the stepped state row gained the write moment %v, and version 4 never recorded one", row.SetAt)
	}

	if row.Colour == nil || row.Colour.G != 170 {
		t.Fatalf("the stepped state row's colour reads %+v, want the green it carried", row.Colour)
	}

	layout := found.Config.Layouts[0]
	if layout.Binding != panel || layout.BarBand == nil || *layout.BarBand != "top" || layout.BarRows == nil || *layout.BarRows != 6 {
		t.Fatalf("the stepped layout row reads %+v, want the panel's own band and rows", layout)
	}

	if found.Config.Windows[0].Brightness == nil || *found.Config.Windows[0].Brightness != 180 ||
		found.Config.Pauses[0].Chosen != pause1h {
		t.Fatalf("the stepped window and pause read %+v and %+v, want the window at 180 and the pause of an hour",
			found.Config.Windows[0], found.Config.Pauses[0])
	}

	// And it is written back at the new version, as a document this build wrote rather than a
	// version-4 document read leniently.
	if _, err := saveStore(dir, found.Config); err != nil {
		t.Fatalf("write the upgraded store back: %v", err)
	}

	onDisk := storedRaw(t, dir)

	if version, _ := onDisk["version"].(float64); int(version) != storeVersion {
		t.Fatalf("the store on disk carries version %v after the upgrade, want %d", onDisk["version"], storeVersion)
	}

	again, err := loadStore(dir)
	if err != nil {
		t.Fatalf("read the document back at version %d: %v", storeVersion, err)
	}

	if again.Upgraded {
		t.Fatal("the document was stepped a second time, and it is already at this build's version")
	}
}

// TestAVersionFiveStoreStepsThroughSixToSevenAndInventsNoLine is the step a version-5 document takes
// to this build, and the one thing the layout half of it must not do.
//
// Version 6 is the panel's seven optional lines, and every one of them is absent until somebody
// switches it on. A version-5 row never carried one and must not acquire one at the upgrade: a line
// written as `false` by the upgrade would be indistinguishable from a line a person had switched off,
// and the row would grow a key nobody wrote on every install that had ever been laid out.
//
// Version 7 is the lights list, which a version-5 document reaches through the version-6 step and
// then the one above it.
func TestAVersionFiveStoreStepsThroughSixToSevenAndInventsNoLine(t *testing.T) {
	const panel = "07e893bb-b922-4ff0-9ad3-061b957400c3"

	dir := writeStoreFile(t, `{
  "version": 5,
  "bindings": [{"id": "`+panel+`", "printer": "`+panel+`", "controller": "http://10.0.0.136", "segment": -1, "brightness": 128}],
  "states": [],
  "moments": [],
  "layouts": [{"binding": "`+panel+`", "bar": true, "bar_band": "top", "bar_rows": 12, "percent": true, "percent_scale": 3, "name": true, "rotation": "take-turns", "rotate_seconds": 15}],
  "windows": [],
  "pauses": []
}`)

	found, err := loadStore(dir)
	if err != nil {
		t.Fatalf("a version 5 document was refused: %v", err)
	}

	if found.Version != 5 {
		t.Fatalf("the read says the document carried version %d, want 5", found.Version)
	}

	if !found.Upgraded {
		t.Fatal("the read does not say the document was upgraded, and it was written at version 5")
	}

	if found.Config.Version != 7 || storeVersion != 7 {
		t.Fatalf("the upgraded document is at version %d, want 7", found.Config.Version)
	}

	// The address the binding names became a light, with no facts on it: the migration has the
	// address and nothing else, and inventing a name here would be writing a fact nobody read.
	if len(found.Config.Lights) != 1 || found.Config.Lights[0].Address != "http://10.0.0.136" {
		t.Fatalf("the stepped document holds the lights %+v, want one row for http://10.0.0.136", found.Config.Lights)
	}

	if found.Config.Lights[0].read() || found.Config.Lights[0].CallsItself != "" || found.Config.Lights[0].Pixels != 0 {
		t.Fatalf("the upgrade invented facts for a light nobody read: %+v", found.Config.Lights[0])
	}

	if len(found.Config.Layouts) != 1 {
		t.Fatalf("the stepped document holds %d layout rows, want the one it had", len(found.Config.Layouts))
	}

	row := found.Config.Layouts[0]
	if row.Binding != panel || row.BarBand == nil || *row.BarBand != "top" ||
		row.PercentScale == nil || *row.PercentScale != 3 || row.RotateSeconds == nil || *row.RotateSeconds != 15 {
		t.Fatalf("the stepped layout row reads %+v, want the picture it described", row)
	}

	for _, held := range []*bool{
		row.Remaining, row.Ends, row.Temperatures, row.Elapsed, row.Material, row.PrinterName, row.Layer,
	} {
		if held != nil {
			t.Fatalf("the upgrade wrote a line into a version-5 row: %+v", row)
		}
	}

	// And a version-6 row that does name one keeps it, through a write and a read: the upgrade is
	// the only step in question here, and the shape is the shape.
	// The row in the document, not the copy read above: a slice element is what a write hands back.
	line := true
	found.Config.Layouts[0].Material = &line

	if _, err := saveStore(dir, found.Config); err != nil {
		t.Fatalf("write the stepped store back: %v", err)
	}

	again, err := loadStore(dir)
	if err != nil {
		t.Fatalf("read the document back at version 6: %v", err)
	}

	if again.Upgraded {
		t.Fatal("the document was stepped a second time, and it is already at this build's version")
	}

	if again.Config.Layouts[0].Material == nil || !*again.Config.Layouts[0].Material {
		t.Fatalf("the line the row names did not survive the round trip: %+v", again.Config.Layouts[0])
	}
}

// TestTheInstalledStoreMigratesToThisBuild is the real document, copied off the running install.
//
// It is skipped unless WLED_STORE_FIXTURE names a directory holding one, because the document it
// steps up is the operator's own and must not be a fixture this repository invents: the point of
// the test is that the file an install is actually running on reaches schema version 7 with its
// bindings intact and one light per address they name. The copy is made into a temporary directory
// first, so a read here can never write over the document that is in use.
//
// The command, run on the host that has the module's store in its container:
//
//	docker cp prusa_connect:/config/config.d/module-data/wled-printer-status/config.json /tmp/live/config.json
//	WLED_STORE_FIXTURE=/tmp/live go test -run TestTheInstalledStoreMigratesToThisBuild -v .
func TestTheInstalledStoreMigratesToThisBuild(t *testing.T) {
	fixture := strings.TrimSpace(os.Getenv("WLED_STORE_FIXTURE"))
	if fixture == "" {
		t.Skip("set WLED_STORE_FIXTURE to a directory holding a copy of an installed store")
	}

	source, err := os.ReadFile(storePath(fixture))
	if err != nil {
		t.Fatalf("read the installed store at %s: %v", storePath(fixture), err)
	}

	before, err := versionOf(source)
	if err != nil {
		t.Fatalf("read the version of the installed store: %v", err)
	}

	dir := t.TempDir()

	if err := os.WriteFile(storePath(dir), source, 0o600); err != nil {
		t.Fatalf("copy the installed store: %v", err)
	}

	found, err := loadStore(dir)
	if err != nil {
		t.Fatalf("the installed document at schema version %d was refused: %v", before, err)
	}

	if found.Config.Version != storeVersion {
		t.Fatalf("the installed document stepped to version %d, want %d", found.Config.Version, storeVersion)
	}

	if !found.Upgraded {
		t.Fatalf("the installed document at version %d was not stepped up", before)
	}

	// Every address a binding names, once, and the bindings are the lines they were.
	named := make([]string, 0, len(found.Config.Bindings))

	for i, raw := range found.Config.Bindings {
		var line storedEntry

		if err := decodeStrictly(raw, &line); err != nil {
			t.Fatalf("read binding %d of the installed document: %v", i+1, err)
		}

		named = append(named, line.Controller)
	}

	if len(named) == 0 {
		t.Fatal("the installed document names no binding, so there is nothing for this test to prove")
	}

	if len(found.Config.Lights) != len(named) {
		t.Fatalf("the stepped document holds %d lights, want the %d its bindings name: %+v",
			len(found.Config.Lights), len(named), found.Config.Lights)
	}

	for i, address := range named {
		if lightKey(found.Config.Lights[i].Address) != lightKey(address) {
			t.Fatalf("light %d is at %q, want the address its binding names, %q",
				i+1, found.Config.Lights[i].Address, address)
		}

		t.Logf("binding %d names %s, which is now light %q", i+1, address, found.Config.Lights[i].name())
	}

	// Written back and read again: the document this build would leave behind reads at 7 without
	// another step, and still holds the lights.
	if _, err := saveStore(dir, found.Config); err != nil {
		t.Fatalf("write the stepped document back: %v", err)
	}

	again, err := loadStore(dir)
	if err != nil {
		t.Fatalf("read the stepped document back: %v", err)
	}

	if again.Upgraded || len(again.Config.Lights) != len(named) {
		t.Fatalf("the document at rest upgraded %v and holds %d lights, want no upgrade and %d",
			again.Upgraded, len(again.Config.Lights), len(named))
	}

	t.Logf("schema version %d stepped to %d, %d bindings kept, %d lights written",
		before, again.Config.Version, len(again.Config.Bindings), len(again.Config.Lights))

	if written, err := os.ReadFile(storePath(dir)); err == nil {
		t.Logf("the stepped document this build leaves behind:\n%s", written)
	}
}
