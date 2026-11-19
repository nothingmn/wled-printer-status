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
			name:    "a document from a newer release",
			fixture: `{"version": 2, "bindings": [], "moments": []}`,
			want:    []string{"schema version 2", "version 1"},
		},
		{
			name:    "a negative version, which no release wrote",
			fixture: `{"version": -1, "bindings": []}`,
			want:    []string{"schema version -1", "counts from zero"},
		},
		{
			name:    "a key this build does not know",
			fixture: `{"version": 1, "bindings": [], "moments": []}`,
			want:    []string{"unknown field"},
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
