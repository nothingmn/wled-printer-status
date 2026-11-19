// The module's own configuration store: one JSON document in the writable directory the host
// grants a module that asks for one.
//
// # Why it exists, and what it is not
//
// The manifest's settings belong to the host: this process reads them on plugin.configure and has
// no call that writes them back. That is enough for a value a person types once, and it is not
// enough for configuration an editor changes, because a change made through a route would be lost
// at the next restart and nothing could say otherwise.
//
// So the module keeps its own configuration: one JSON document in the writable directory the
// operator approved when the module was installed (`needs.data` in module.yaml, `Grants.Data` on
// the configure call). It lives beside the installed module on Prusa Connect's writable config
// volume, which is what makes it survive a restart and an upgrade and lose it to a `module purge`
// or a fresh install — docs/product-requirements.md §4.
//
// The host's settings are read once, as a migration source: the first start with no document
// writes one from them, and every start after that reads the document instead. That is the
// requirement's own words and not a convenience of this file.
//
// # The version, and why it is stamped from the first commit
//
// The document carries a schema version from the first commit, because its shape will change — a
// binding gains a layout, a moment table and a state table appear beside it — and a version added
// later cannot be trusted: the documents written before it are indistinguishable from documents
// at whatever the first stamped version happened to be.
//
// A read therefore never guesses. A document with no version is version 0, which is the shape this
// store would have written before the field existed, and it is stepped up to storeVersion one
// explicit upgrade at a time and then written back stamped. A document at a version this build
// does not know is refused, naming both numbers, rather than half-read: a newer release wrote it
// and this one cannot know what its fields mean.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

const (
	// storeFileName is the document's name inside the directory the host granted. It is one
	// file because this is one small document: the configuration a person edits, and not a
	// database, an index or a table of its own.
	storeFileName = "config.json"

	// storeVersion is the schema version this build writes. It goes up by one when the shape of
	// the document changes, and every version below it needs an entry in storeUpgrades so that a
	// document from an older release is stepped up rather than refused.
	//
	// Version 1 is the document with the schema version stamped and the bindings list, which is
	// what the release that introduced this store wrote. Version 2 adds the state table and the
	// moment table beside the bindings — the two tables the Colours and Events screens edit — and
	// a version-1 document is stepped up to it by an entry in storeUpgrades that adds both, empty.
	storeVersion = 2

	// storeVersionUnstamped is version 0: a document written before the version was stamped,
	// which is the only older shape this store has ever had.
	storeVersionUnstamped = 0
)

// errNoStore reports a granted directory with no document in it. That is the first start of an
// install and not a fault: the settings seed the document, and the start that follows reads it.
var errNoStore = errors.New("this module has no configuration store yet")

// storedConfig is the document as this build writes it: the schema version, then the
// configuration it holds.
type storedConfig struct {
	// Version is the schema version of everything below it, and the first field of the document
	// on purpose: a reader that knows nothing else can still tell whether it may read the rest.
	Version int `json:"version"`

	// Bindings is one line per printer this module shows, in the same shape one line of the
	// `bindings` setting carries: printer, controller, and segment or brightness when they were
	// named.
	//
	// It is raw JSON for the reason the setting is: a strict decode of a slice cannot say which
	// line a misspelt key was in, and parseBindingEntries decodes one line at a time so that a
	// refusal reads `bindings[1]: ...` rather than making a person count lines in a file.
	Bindings []json.RawMessage `json:"bindings"`

	// States is the state table the Colours screen edits: one row per state somebody has
	// changed, carrying only the fields that were changed. A state with no row here is drawn
	// exactly as internal/render draws it — docs/product-requirements.md Appendix A — and that
	// silence is the whole point: an override is written when a person changes something, and a
	// default is never copied into the document to be kept in step with the code by hand.
	States []storedState `json:"states"`

	// Moments is the moment table the Events screen edits, keyed on the (light, printer) pair:
	// one row per moment somebody has changed on one printer on one light. A moment with no row
	// here behaves as momentDefault says, for the reason the state table has none: absent is a
	// default and not a zero.
	//
	// The key is the pair because that is what multi-room means — the same printer on two lights
	// carries two independent sets — and because the moment list itself belongs to the light.
	Moments []storedMoment `json:"moments"`
}

// storedConfigV1 is the document at schema version 1: the version and the bindings list, and
// nothing else. Only the upgrade from it reads it; nothing writes it.
type storedConfigV1 struct {
	Version  int               `json:"version"`
	Bindings []json.RawMessage `json:"bindings"`
}

// storedConfigV0 is the document before the version was stamped: the bindings list and no
// version key. Only an upgrade reads it; nothing writes it.
type storedConfigV0 struct {
	Bindings []json.RawMessage `json:"bindings"`
}

// storedEntry is one line of the store's bindings list, and it is the same three or four keys one
// line of the `bindings` setting is written with.
//
// The pointers are the setting's own distinction and are kept for the same reason: absent is not
// zero. Segment 0 is a segment a controller holds, a brightness of 0 is a strip switched off, and
// a line that leaves one out must not be read as either. A whole-controller binding writes its
// brightness, because that is the number the next start has to land on; a borrowed one writes
// none, because a brightness on it is refused.
type storedEntry struct {
	Printer    string `json:"printer"`
	Controller string `json:"controller"`
	Segment    *int   `json:"segment,omitempty"`
	Brightness *int   `json:"brightness,omitempty"`
}

// storeUpgrade is one step of an upgrade: one document at From, read into the shape at From+1.
//
// It exists as data rather than as a chain of ifs so that the next shape change is an entry in a
// table and not a rewrite of the read path — which is the whole reason the version is stamped.
type storeUpgrade struct {
	// from is the version the step reads, and it writes from+1.
	from int

	// why says what changed, for the line the operator reads when his store is stepped up.
	why string

	// step reads one document and answers the document one version later, which the caller
	// decodes strictly at the new version: a step that forgot a field fails there rather than
	// producing a document that reads as a zero.
	step func(raw []byte) ([]byte, error)
}

// storeUpgrades is every step this build can take, lowest first. A version with no step here is a
// version this build cannot read, and the read says so by number.
//
//nolint:gochecknoglobals // a closed table read at start-up, never written.
var storeUpgrades = []storeUpgrade{
	{
		from: storeVersionUnstamped,
		why:  "the schema version is stamped on a document that did not carry one",
		step: func(raw []byte) ([]byte, error) {
			var unstamped storedConfigV0
			if err := decodeStrictly(raw, &unstamped); err != nil {
				return nil, err
			}

			// Version 1 and not the current version: a step answers the document exactly one
			// version later, and the shape at 1 is the one above. Stepping straight to 2 here
			// would leave the upgrade from 1 untrodden and untested, which is the one thing a
			// versioned document exists to prevent.
			return json.Marshal(storedConfigV1{
				Version:  storeVersionUnstamped + 1,
				Bindings: unstamped.Bindings,
			})
		},
	},
	{
		from: 1,
		why:  "the document gained the state table and the moment table, both empty",
		step: func(raw []byte) ([]byte, error) {
			var first storedConfigV1
			if err := decodeStrictly(raw, &first); err != nil {
				return nil, err
			}

			// Both tables arrive empty, and empty is the whole document that version 1 already
			// described: every state keeps the colour internal/render holds for it and every
			// moment behaves as its default says, so the light this document was written for
			// goes on showing exactly what it showed before the upgrade. Nothing is invented
			// for a table nobody has edited yet — a default copied in here would be a second
			// copy of the code's own table, drifting from it one release later.
			return json.Marshal(storedConfig{
				Version:  2,
				Bindings: first.Bindings,
				States:   []storedState{},
				Moments:  []storedMoment{},
			})
		},
	},
}

// storeRead is what one read of the store found.
type storeRead struct {
	// Config is the document at storeVersion, stepped up when an older build wrote it.
	Config storedConfig

	// Version is the version the document carried before any upgrade, which is what the log line
	// names.
	Version int

	// Upgraded says the document must be written back: it was one or more versions behind this
	// build.
	Upgraded bool
}

// storePath is where the document lives inside a granted directory.
func storePath(dir string) string {
	return filepath.Join(dir, storeFileName)
}

// loadStore reads this module's configuration document out of dir.
//
// A directory with no document in it answers errNoStore, which is the first start of an install
// and not a fault. Every other failure is refused with the path in it: a document this build
// cannot read is a document a person has to see, and running from the settings instead would
// quietly discard whatever is in it.
func loadStore(dir string) (storeRead, error) {
	path := storePath(dir)

	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return storeRead{}, errNoStore
		}

		return storeRead{}, fmt.Errorf("read the configuration store %s: %w", path, err)
	}

	found, err := upgradeStore(raw)
	if err != nil {
		return storeRead{}, fmt.Errorf("the configuration store %s: %w", path, err)
	}

	return found, nil
}

// upgradeStore reads one document and brings it to storeVersion, one explicit step at a time.
//
// The version is read first and alone, because the shape of everything else depends on it: a
// document from a newer release has to be refused before any of its fields are read, or the
// refusal would be about a key this build does not know rather than about the version, and that
// sentence sends a person looking in the wrong place.
func upgradeStore(raw []byte) (storeRead, error) {
	version, err := versionOf(raw)
	if err != nil {
		return storeRead{}, err
	}

	if version > storeVersion {
		return storeRead{}, fmt.Errorf(
			"it was written by a newer release, at schema version %d, and this build reads version %d: install the release that wrote it, or move this file aside to start from the settings",
			version, storeVersion)
	}

	found := storeRead{Version: version}
	stepped := raw

	for version < storeVersion {
		upgrade, known := upgradeFrom(version)
		if !known {
			return storeRead{}, fmt.Errorf(
				"it carries schema version %d, and this build has no upgrade from that version to %d",
				version, storeVersion)
		}

		stepped, err = upgrade.step(stepped)
		if err != nil {
			return storeRead{}, fmt.Errorf("upgrade it from schema version %d, where %s: %w",
				version, upgrade.why, err)
		}

		version++
		found.Upgraded = true
	}

	if err := decodeStrictly(stepped, &found.Config); err != nil {
		return storeRead{}, fmt.Errorf("read the document at schema version %d: %w", storeVersion, err)
	}

	// A step that forgot to stamp, or a document whose version key disagrees with the shape it
	// carries, would otherwise be read as a document at this version with a version of its own.
	if found.Config.Version != storeVersion {
		return storeRead{}, fmt.Errorf(
			"it reads at schema version %d after upgrading, and this build writes version %d",
			found.Config.Version, storeVersion)
	}

	return found, nil
}

// upgradeFrom is the step that reads version, and false for a version this build cannot step.
func upgradeFrom(version int) (storeUpgrade, bool) {
	for _, upgrade := range storeUpgrades {
		if upgrade.from == version {
			return upgrade, true
		}
	}

	return storeUpgrade{}, false
}

// versionOf is the schema version one document carries, and storeVersionUnstamped for a document
// that carries none.
//
// The probe is lenient and reads one key: which keys are allowed in the rest of the document
// depends on the version, so a strict read here would refuse a newer document for the wrong
// reason and refuse an older one for a key the current shape simply does not have.
func versionOf(raw []byte) (int, error) {
	var probe struct {
		Version *int `json:"version"`
	}

	if err := json.Unmarshal(raw, &probe); err != nil {
		return 0, fmt.Errorf("it is not a JSON document: %w", err)
	}

	if probe.Version == nil {
		return storeVersionUnstamped, nil
	}

	if *probe.Version < 0 {
		return 0, fmt.Errorf(
			"it carries schema version %d, and a version counts from zero", *probe.Version)
	}

	return *probe.Version, nil
}

// decodeStrictly reads one document and refuses a key this build does not know, which is the rule
// every other document in this module is read by: a misspelt key that is ignored is a field left
// at its zero, and a field left at its zero here is a binding pointing at nothing.
func decodeStrictly(raw []byte, into any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(into); err != nil {
		return err
	}

	if decoder.More() {
		return errors.New("it carries more than one JSON document")
	}

	return nil
}

// saveStore writes this module's configuration document into dir, at storeVersion.
//
// It writes a temporary file beside the document and renames it over the old one, so that a
// process killed mid-write leaves the previous document rather than half of a new one: this file
// is the only copy of what a person configured, and a truncated one is a store somebody has to
// delete by hand.
//
// 0600, because it is this module's own record on a volume other processes can read, and nothing
// in it is worth handing to them.
func saveStore(dir string, config storedConfig) (string, error) {
	path := storePath(dir)

	// Both tables are written as empty arrays when they hold nothing, never as null: a null
	// reads as "this build does not know the field", and a person opening config.json to see
	// what the state table holds deserves a list rather than an absence. It is also what makes
	// an upgraded document and a fresh one the same document on disk.
	if config.States == nil {
		config.States = []storedState{}
	}

	if config.Moments == nil {
		config.Moments = []storedMoment{}
	}

	document, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return path, fmt.Errorf("render the configuration store: %w", err)
	}

	document = append(document, '\n')

	temp, err := os.CreateTemp(dir, "."+storeFileName+".*")
	if err != nil {
		return path, fmt.Errorf("write the configuration store %s: %w", path, err)
	}

	name := temp.Name()

	// A no-op once the rename has taken the name, and the cleanup for every refusal below.
	defer func() { _ = os.Remove(name) }()

	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()

		return path, fmt.Errorf("write the configuration store %s: %w", path, err)
	}

	if _, err := temp.Write(document); err != nil {
		_ = temp.Close()

		return path, fmt.Errorf("write the configuration store %s: %w", path, err)
	}

	if err := temp.Close(); err != nil {
		return path, fmt.Errorf("write the configuration store %s: %w", path, err)
	}

	if err := os.Rename(name, path); err != nil {
		return path, fmt.Errorf("write the configuration store %s: %w", path, err)
	}

	return path, nil
}

// storeEntries renders a checked set as the lines the document holds.
//
// The printer is written by its device id rather than by the name the operator typed, because the
// store is this module's record and not a copy of his settings: an id survives a rename, and a
// document that named a printer by a name that has since changed would refuse the next start for
// a reason nobody caused.
func storeEntries(set bindings) ([]json.RawMessage, error) {
	entries := make([]json.RawMessage, 0, len(set))

	for _, b := range set {
		segment := b.Segment

		line := storedEntry{Printer: b.DeviceID, Controller: b.Address, Segment: &segment}

		if b.Segment < 0 {
			brightness := b.Brightness
			line.Brightness = &brightness
		}

		raw, err := json.Marshal(line)
		if err != nil {
			return nil, fmt.Errorf("render binding of printer %s for the configuration store: %w", b.DeviceID, err)
		}

		entries = append(entries, raw)
	}

	return entries, nil
}
