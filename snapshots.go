// Snapshots: one light's whole configuration, kept as a file, and the body a restore sends back.
//
// # Why a file of its own, beside the document
//
// docs/product-requirements.md §4 lists snapshots among the things this module keeps in its own
// writable directory, and everything else on that list lives in the one JSON document beside them
// (store.go). A snapshot does not, for two reasons.
//
// It is not a thing a person edits. Every list in the document is a table a screen writes rows into,
// and the upgrade chain's whole job is to carry those rows from one shape to the next. A snapshot
// carried inside the document would put fifteen kilobytes of another device's configuration in the
// path of every restart that reads a binding, and a version 4 written for nothing.
//
// And it is the one thing here that must outlive the document. A `module purge` takes the store; a
// snapshot an operator took before a restore is what he has left afterwards, and it should not be
// lost by the same accident that lost the bindings. So each snapshot is one file, 0600, named so
// that a directory listing tells two of them apart by the light and the moment — and the store stays
// at schema version 3, because nothing joins it.
//
// # What is in one, and what cannot be
//
// A snapshot holds the controller's /json/cfg verbatim, its /presets.json verbatim, its /json/state
// as it was at the moment it was taken, and the identity of the light it came from: the address this
// module names it by, the name the light calls itself, its firmware, and its shape and pixel count.
// The identity is why a restore cannot land on the wrong light.
//
// What it cannot hold is the wifi password. Measured on both of the operator's lights — see the
// package comment in internal/wled/config.go — no configuration dump carries a psk, so a literal
// hundred-per-cent clone is impossible over the API. Every snapshot says which credential members it
// does and does not have, from the bytes it actually holds, and the routes answer with that rather
// than with a promise.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nothingmn/wled-printer-status/internal/wled"
)

const (
	// snapshotFolderName is the directory inside the granted data directory that holds one file
	// per snapshot. It is a subdirectory rather than the data directory itself so that the
	// document and the snapshots cannot be mistaken for each other by a person with a file
	// manager, and so that a purge of snapshots is one directory.
	snapshotFolderName = "snapshots"

	// snapshotSuffix is what every snapshot file ends in, and how the list tells one from
	// anything else that ends up in the folder.
	snapshotSuffix = ".json"

	// snapshotFormat is the shape of a snapshot file, and it is not the store's version: a
	// snapshot is written by one function and read by one function, and the day its shape
	// changes is the day this number goes up and the reader learns the older shape.
	snapshotFormat = 1

	// snapshotTimeLayout is how a snapshot's id carries its moment: sortable, in UTC, and
	// readable — 20261119T183002Z — because a person picks a snapshot out of a list by when it
	// was taken.
	snapshotTimeLayout = "20060102T150405Z"

	// snapshotKindPanel and snapshotKindStrip are the two shapes a light can be. The kind is
	// stored rather than worked out from the height, because it is the thing a restore refuses on:
	// a panel and a strip of the same pixel count are not the same display.
	snapshotKindPanel = "panel"
	snapshotKindStrip = "strip"

	// snapshotFolderMode and snapshotFileMode are what a directory and a file of this module's
	// own are: nobody else's business. A snapshot carries network configuration, which is one
	// step short of a credential and is not something to hand to every process on the volume.
	snapshotFolderMode = 0o700
	snapshotFileMode   = 0o600
)

// snapshotDevice is the identity of the light a snapshot came from, which is what a restore is
// checked against before anything is written.
//
// It is four facts and not one because each answers a different question. Name and Address are how
// this module knows the light: a restore is asked for by that name, and a snapshot taken from
// another address is a snapshot of another light. Controller and Firmware are how the light knows
// itself: an address that now answers to a different name is a box that has been replaced at the
// same address, which is exactly the case a restore must not walk into. Kind, Width, Height and
// Pixels are the display: the same shape and the same pixel count, or no restore — the
// requirements' own rule for a swap, applied to a restore.
type snapshotDevice struct {
	Name       string `json:"name"`
	Address    string `json:"address"`
	Controller string `json:"controller"`
	Firmware   string `json:"firmware"`
	Kind       string `json:"kind"`
	Width      int    `json:"width"`
	Height     int    `json:"height"`
	Pixels     int    `json:"pixels"`
}

// geometry is the display this light is, in the form the painter's own arithmetic takes.
//
// A strip is one row of its own length, which is what it is: the module's segment writes count
// pixels from the first and there are no rows to write, and a buffer of Width by 1 is the same run
// in the same order.
func (d snapshotDevice) geometry() panel {
	if d.Kind == snapshotKindPanel {
		return panel{width: d.Width, height: d.Height}
	}

	return panel{}
}

// snapshotDocument is one snapshot file.
//
// The config, the presets and the state are the firmware's own documents, kept as the bytes that
// arrived. Decoding them into structs here would be this module deciding which of the firmware's
// keys matter, and a restore built from that decision would quietly drop the rest of an operator's
// configuration.
type snapshotDocument struct {
	// Format is this file's shape, so that a build reading one written by a later release can
	// say so by number instead of half-reading it.
	Format int `json:"format"`

	// ID is the snapshot's name on the routes, and the same string as its file's name without
	// the suffix: the light and the moment, so that a person can tell two apart by looking.
	ID string `json:"id"`

	// TakenAt is when the light was read, in UTC, as RFC 3339.
	TakenAt string `json:"taken_at"`

	// Device is the light it came from.
	Device snapshotDevice `json:"device"`

	// Config is GET /json/cfg, verbatim.
	Config json.RawMessage `json:"config"`

	// Presets is GET /presets.json, verbatim. It is separate because the firmware keeps it
	// separately: it is not a section of the configuration dump.
	Presets json.RawMessage `json:"presets"`

	// State is GET /json/state at the moment of the snapshot. A restore does not write it — a
	// light's power and segments belong to whoever is using it now, not to the moment somebody
	// took a copy — but it is the only record of what the light was doing, and an operator
	// comparing two snapshots of a light that has misbehaved is reading exactly this.
	State json.RawMessage `json:"state"`
}

// snapshotFolder is where the snapshots of one granted directory live.
func snapshotFolder(dir string) string {
	return filepath.Join(dir, snapshotFolderName)
}

// snapshotIDFor is the id, and the file name, of a snapshot of a light taken at a moment.
//
// The light's name comes first because that is what a person scans a list for — "the panel" — and
// the moment second because that is how two of them are told apart. Everything that is not a letter,
// a digit, a dot, a dash or an underscore becomes a dash, so that an address which is also a path
// segment cannot become a path in a directory.
func snapshotIDFor(device string, at time.Time) string {
	cleaned := make([]rune, 0, len(device))

	for _, character := range device {
		switch {
		case character >= 'a' && character <= 'z', character >= 'A' && character <= 'Z',
			character >= '0' && character <= '9', character == '.', character == '-', character == '_':
			cleaned = append(cleaned, character)
		default:
			cleaned = append(cleaned, '-')
		}
	}

	name := strings.Trim(string(cleaned), ".-_")
	if name == "" {
		name = "light"
	}

	return name + "-" + at.UTC().Format(snapshotTimeLayout)
}

// saveSnapshot writes one snapshot into the granted directory and answers it with the id it was
// given, which is not always the one it arrived with: two snapshots of one light inside one second
// are two files, and the second is told apart by a suffix rather than by overwriting the first.
//
// It writes a temporary file beside the snapshot and renames it over the name, for the reason
// saveStore does: a snapshot half-written is a file somebody believes is a backup.
func saveSnapshot(dir string, doc snapshotDocument, at time.Time) (snapshotDocument, error) {
	folder := snapshotFolder(dir)

	if err := os.MkdirAll(folder, snapshotFolderMode); err != nil {
		return doc, fmt.Errorf("write this light's snapshot into %s: %w", folder, err)
	}

	id, err := freeSnapshotID(folder, doc.Device.Name, at)
	if err != nil {
		return doc, err
	}

	doc.Format = snapshotFormat
	doc.ID = id

	if doc.TakenAt == "" {
		doc.TakenAt = at.UTC().Format(time.RFC3339)
	}

	document, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return doc, fmt.Errorf("render the snapshot of %s: %w", doc.Device.Name, err)
	}

	document = append(document, '\n')

	path := filepath.Join(folder, id+snapshotSuffix)

	temp, err := os.CreateTemp(folder, "."+id+".*")
	if err != nil {
		return doc, fmt.Errorf("write the snapshot %s: %w", path, err)
	}

	name := temp.Name()

	// A no-op once the rename has taken the name, and the cleanup for every refusal below.
	defer func() { _ = os.Remove(name) }()

	if err = temp.Chmod(snapshotFileMode); err != nil {
		_ = temp.Close()

		return doc, fmt.Errorf("write the snapshot %s: %w", path, err)
	}

	if _, err = temp.Write(document); err != nil {
		_ = temp.Close()

		return doc, fmt.Errorf("write the snapshot %s: %w", path, err)
	}

	if err = temp.Close(); err != nil {
		return doc, fmt.Errorf("write the snapshot %s: %w", path, err)
	}

	if err = os.Rename(name, path); err != nil {
		return doc, fmt.Errorf("write the snapshot %s: %w", path, err)
	}

	return doc, nil
}

// freeSnapshotID is the id for a snapshot of this light at this moment that no file is holding, and
// the reason there is none when a hundred of them are.
func freeSnapshotID(folder, device string, at time.Time) (string, error) {
	base := snapshotIDFor(device, at)

	for suffix := 0; suffix < 100; suffix++ {
		id := base
		if suffix > 0 {
			id += "-" + strconv.Itoa(suffix+1)
		}

		_, err := os.Stat(filepath.Join(folder, id+snapshotSuffix))
		if errors.Is(err, fs.ErrNotExist) {
			return id, nil
		}

		if err != nil {
			return "", fmt.Errorf("look for the snapshot %s: %w", id, err)
		}
	}

	return "", fmt.Errorf("a hundred snapshots of %s are already held for %s: remove one before taking another",
		device, at.UTC().Format(time.RFC3339))
}

// loadSnapshot reads one snapshot by id.
//
// An id is a file name and nothing else: one carrying a path separator, or a leading dot, names no
// snapshot this module holds rather than being resolved into one. That is the whole of the guard a
// route needs when the id arrives from a caller.
func loadSnapshot(dir, id string) (snapshotDocument, error) {
	trimmed := strings.TrimSpace(id)

	if trimmed == "" || trimmed != filepath.Base(trimmed) || strings.HasPrefix(trimmed, ".") ||
		strings.ContainsAny(trimmed, `/\`) {
		return snapshotDocument{}, errNoSnapshot
	}

	path := filepath.Join(snapshotFolder(dir), trimmed+snapshotSuffix)

	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return snapshotDocument{}, errNoSnapshot
		}

		return snapshotDocument{}, fmt.Errorf("read the snapshot %s: %w", path, err)
	}

	var doc snapshotDocument

	if err = json.Unmarshal(raw, &doc); err != nil {
		return snapshotDocument{}, fmt.Errorf("the snapshot %s is not one this build can read: %w", path, err)
	}

	if doc.Format > snapshotFormat {
		return snapshotDocument{}, fmt.Errorf(
			"the snapshot %s was written by a newer release, at format %d, and this build reads format %d",
			path, doc.Format, snapshotFormat)
	}

	if doc.ID == "" {
		doc.ID = trimmed
	}

	return doc, nil
}

// snapshotSize is one snapshot file's size on disk.
func snapshotSize(dir, id string) (int64, error) {
	info, err := os.Stat(filepath.Join(snapshotFolder(dir), id+snapshotSuffix))
	if err != nil {
		return 0, err
	}

	return info.Size(), nil
}

// snapshotSizeOf is that number, and zero when the file cannot be measured: a size in an answer is
// worth less than the answer itself.
func snapshotSizeOf(dir, id string) int64 {
	size, err := snapshotSize(dir, id)
	if err != nil {
		return 0
	}

	return size
}

// errNoSnapshot is a snapshot id this module holds no file for.
var errNoSnapshot = errors.New("this module holds no snapshot of that name")

// heldSnapshot is one file in the folder as the list reports it: the document, the file's size, and
// why it cannot be read when it cannot.
type heldSnapshot struct {
	doc  snapshotDocument
	size int64
	id   string
	why  string
}

// listSnapshots reads every snapshot in the folder, newest first.
//
// A file that will not decode is reported rather than dropped, with the reason: a snapshot somebody
// believes he has and cannot restore is a fact about the module's own directory that he has to be
// able to see, and a list that silently left it out would be the module losing his backup twice.
func listSnapshots(dir string) ([]heldSnapshot, error) {
	folder := snapshotFolder(dir)

	entries, err := os.ReadDir(folder)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}

		return nil, fmt.Errorf("read this module's snapshots in %s: %w", folder, err)
	}

	held := make([]heldSnapshot, 0, len(entries))

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), snapshotSuffix) ||
			strings.HasPrefix(entry.Name(), ".") {
			continue
		}

		id := strings.TrimSuffix(entry.Name(), snapshotSuffix)

		info, statErr := entry.Info()
		if statErr != nil {
			held = append(held, heldSnapshot{id: id, why: statErr.Error()})

			continue
		}

		doc, readErr := loadSnapshot(dir, id)
		if readErr != nil {
			held = append(held, heldSnapshot{id: id, size: info.Size(), why: readErr.Error()})

			continue
		}

		held = append(held, heldSnapshot{doc: doc, size: info.Size(), id: id})
	}

	sort.Slice(held, func(a, b int) bool {
		if held[a].doc.TakenAt == held[b].doc.TakenAt {
			return held[a].id > held[b].id
		}

		return held[a].doc.TakenAt > held[b].doc.TakenAt
	})

	return held, nil
}

// ---------------------------------------------------------------------------------------------
// What a restore sends
// ---------------------------------------------------------------------------------------------

// restoreSections are the sections of a configuration a restore writes, and they are
// docs/product-requirements.md §2.6's table: the light's LED setup, its defaults, its interfaces,
// its timers, its usermods, its access point, its presets and its name.
//
// The presets are not read from here — the firmware keeps them in their own document — but they are
// in the same sentence of the requirement and they are written in the same body, so they are named
// beside the rest in the answer.
var restoreSections = []string{"light", "def", "if", "timers", "um", "ap", "id"} //nolint:gochecknoglobals // a closed list.

// withheldSection is one section of a dump a restore does not write, and why.
type withheldSection struct {
	Section string `json:"section"`
	Why     string `json:"why"`
}

// restoreWithheld is every other section the operator's two lights carry, with the reason it is not
// written. It is a list rather than a silence because a person restoring a light is entitled to
// know what he is not getting back — and because two of these are the requirements' own refusals and
// five are sections its table does not mention at all, which is a decision worth stating rather than
// a default worth hiding.
var restoreWithheld = []withheldSection{ //nolint:gochecknoglobals // a closed list.
	{
		Section: "rev",
		Why:     "the firmware revision: the light's own record of what is running, and not a setting anything restores.",
	},
	{
		Section: "vid",
		Why:     "the hardware revision: it belongs to the box the firmware found, and writing another box's would be a lie about this one.",
	},
	{
		Section: "hw",
		Why:     "the LED output mapping and relay configuration: the requirements' restore table does not list it, and a section nobody has decided about is not written.",
	},
	{
		Section: "ol",
		Why:     "the overlay configuration: the requirements' restore table does not list it.",
	},
	{
		Section: "ota",
		Why:     "the update configuration: the requirements' restore table does not list it.",
	},
	{
		Section: "remote",
		Why:     "the remote-instance configuration: the requirements' restore table does not list it, and this light's own is not the part a restore is for.",
	},
	{
		Section: "wifi",
		Why:     "the wifi radio, sleep and transmit settings: the requirements' restore table does not list it, and writing a radio's settings is the one write that can strand a light that is otherwise working.",
	},
}

// sentNetwork is what happened to the network section, which is the one section a restore has to
// send and must not send whole.
type sentNetwork struct {
	Sent    bool     `json:"sent"`
	Removed []string `json:"members_removed"`
	Why     string   `json:"why"`
}

// restorePlan is the body one snapshot becomes, and everything the answer says about it.
//
// It is built whole before anything is written, so that a snapshot this build cannot restore is
// refused with nothing sent, and so that the answer's list of sections is the list that actually
// went out rather than a description of what was intended.
type restorePlan struct {
	Body     json.RawMessage   `json:"-"`
	Sections []string          `json:"sections"`
	Withheld []withheldSection `json:"withheld"`
	Network  sentNetwork       `json:"nw"`
	Presets  bool              `json:"presets"`
}

// restoreBody is the body a restore sends for one snapshot.
//
// # The network section, and why it is sent
//
// Omitting "nw" makes the firmware clear nw.linked_remote (cfg.cpp:64), so a body that leaves the
// section out silently breaks a light's remote linking — a change nobody asked for, in the one part
// of the configuration that is about reaching the light at all. So the section is sent, and the one
// member that is a credential is not: nw.ins identifies this module to a remote instance and is
// nobody else's business, and writing a copy of it back from a file would be handing a credential
// to whoever can read the file. Everything else in the section — the linked remote, the DNS
// servers, the ESP-NOW settings — is the light's own configuration and goes back as it was.
//
// # The reboot
//
// "rb": true is in the body, because a configuration write does not reboot by itself: measured, the
// write answers in about 65 ms and the light goes on drawing its own segments until something tells
// it to restart. Asking in the same request is one round trip rather than two, and a second request
// that failed would leave a light holding a configuration it had not yet been told to load.
func restoreBody(doc snapshotDocument) (restorePlan, error) {
	var config map[string]json.RawMessage

	if err := json.Unmarshal(doc.Config, &config); err != nil {
		return restorePlan{}, fmt.Errorf("the snapshot's configuration is not an object: %w", err)
	}

	body := make(map[string]json.RawMessage, len(restoreSections)+4)
	plan := restorePlan{
		Sections: append([]string{}, restoreSections...),
		Withheld: append([]withheldSection{}, restoreWithheld...),
	}

	for _, section := range restoreSections {
		raw, carried := config[section]
		if !carried {
			return restorePlan{}, fmt.Errorf(
				"the snapshot carries no %q section, so restoring it would leave that part of the light as something else found it: take a fresh snapshot of the light and restore that",
				section)
		}

		body[section] = raw
	}

	network, carried := config["nw"]
	if !carried {
		return restorePlan{}, errors.New(
			"the snapshot carries no \"nw\" section, and a configuration write that omits it clears the light's linked remote: taken from WLED 16.0.1, cfg.cpp:64")
	}

	var members map[string]json.RawMessage
	if err := json.Unmarshal(network, &members); err != nil {
		return restorePlan{}, fmt.Errorf("the snapshot's \"nw\" section is not an object: %w", err)
	}

	removed := make([]string, 0, 1)

	if _, held := members["ins"]; held {
		delete(members, "ins")

		removed = append(removed, "ins")
	}

	encoded, err := json.Marshal(members)
	if err != nil {
		return restorePlan{}, fmt.Errorf("render the snapshot's \"nw\" section: %w", err)
	}

	body["nw"] = encoded

	plan.Network = sentNetwork{
		Sent:    true,
		Removed: removed,
		Why: "The section is sent whole except for the one member that is a credential, because a configuration " +
			"write that omits \"nw\" clears the light's linked remote, and nw.ins is not something this module " +
			"writes back from a file.",
	}

	if len(doc.Presets) > 0 {
		body["presets"] = doc.Presets

		plan.Presets = true
		plan.Sections = append(plan.Sections, "presets")
	}

	body["rb"] = json.RawMessage("true")

	encodedBody, err := json.Marshal(body)
	if err != nil {
		return restorePlan{}, fmt.Errorf("render the restore body: %w", err)
	}

	plan.Body = encodedBody

	return plan, nil
}

// ---------------------------------------------------------------------------------------------
// What a snapshot says it does not hold
// ---------------------------------------------------------------------------------------------

// snapshotOmission is one thing a snapshot cannot give back, and why.
type snapshotOmission struct {
	Member string `json:"member"`
	Why    string `json:"why"`
}

// omissionsOf is what one snapshot does not hold, worked out from the bytes it does hold rather than
// promised in a constant: a firmware that started carrying a wifi password would change this answer
// without anybody editing a string, and the UI would stop saying the thing that had become untrue.
func omissionsOf(doc snapshotDocument) []snapshotOmission {
	omitted := []snapshotOmission{{
		Member: "nw.ins",
		Why: "The credential this module would use to reach a remote instance. It is in the configuration " +
			"it was read from and is deliberately not written back from a file.",
	}}

	if credentialHeld(doc.Config, "psk") {
		omitted = append(omitted, snapshotOmission{
			Member: "nw.psk",
			Why:    "This firmware put the wifi password in the dump. A restore still does not write the network section's credentials back.",
		})

		return omitted
	}

	return append(omitted, snapshotOmission{
		Member: "nw.psk",
		Why: "The wifi password is not in a configuration dump at all on the lights this build was measured " +
			"against - neither section carried a psk or even an ssid - so a literal hundred-per-cent clone " +
			"including the wifi credentials is impossible over the API. The light keeps the network it is on.",
	})
}

// credentialHeld reports whether a configuration dump carries one credential member, in either of
// the two sections a firmware keeps network settings in.
func credentialHeld(config json.RawMessage, member string) bool {
	var dump struct {
		Network map[string]json.RawMessage `json:"nw"`
		Wifi    map[string]json.RawMessage `json:"wifi"`
	}

	if err := json.Unmarshal(config, &dump); err != nil {
		return false
	}

	if _, held := dump.Network[member]; held {
		return true
	}

	_, held := dump.Wifi[member]

	return held
}

// presetSlot is one of a controller's own presets, as this module reads the table: the number a
// state write names it by, and the words WLED's own interface shows for it.
//
// It is slot and name and nothing else. A preset's body carries segments, an effect, a palette, a
// brightness and a LED map — it is the light's whole picture — and none of that is this module's to
// carry: docs/product-requirements.md §2.3 permits naming one and forbids drawing, editing or
// re-creating one. There is deliberately no field here that could hold a body, so no route built on
// this type can leak one.
type presetSlot struct {
	// Slot is the number a state write addresses and a moment's preset is resolved to.
	Slot int `json:"slot"`

	// Name is the preset's name as the light holds it, with the surrounding spaces trimmed,
	// because that trim is exactly what a moment's `which` is matched on (internal/wled/preset.go).
	// Empty on a slot that has never been named.
	Name string `json:"name"`

	// Unnamed is true when the slot carries no name. It is a member rather than something a caller
	// works out from an empty string, because the two facts a screen needs are different: this
	// slot is a preset the operator can pick, and this slot has no words to show for it. An
	// unnamed slot is still offered — it is a picture the light holds — and a screen that hides it
	// leaves the operator unable to reach a preset WLED's own interface still lists.
	Unnamed bool `json:"unnamed"`
}

// presetEntry is the part of one preset this module reads out of the table: its name, `n` on the
// wire, which is the only string WLED's interface shows a person.
type presetEntry struct {
	Name string `json:"n"`
}

// presetSlots parses the controller's own preset table — GET /presets.json, which is
// `{"1": {...}, "2": {...}}` — into the slots it holds, lowest slot first.
//
// # One reading of that document
//
// This is the only parser of /presets.json in this package, and both callers go through it: the
// snapshot's count (presetsHeld) and GET /devices/{name}/presets. A second parse of the same table
// is a second answer to "which presets does this light hold", and the two drift the moment one of
// them learns something the other does not. internal/wled has its own reader for its own reason —
// it resolves a name to an id to run one — and that is the only other reading in the tree.
//
// # Slot 0
//
// WLED keeps slot 0 for itself. Measured on both of this operator's lights rather than assumed
// (morpheus 10.0.0.136 and CyberDesk 10.0.0.253, WLED's own /presets.json): slot 0 is present in
// the document as an empty object `{}` — no name, no body, no segments — while every user preset is
// a slot numbered 1 or higher carrying `n`. So slot 0 is skipped, whatever it holds, and the count
// and the list agree with each other and with what WLED's own interface offers. A slot numbered
// below one is skipped with it: no firmware writes a preset a state cannot address.
//
// # What is not a slot
//
// A key that is not a number is not a slot this firmware takes, and internal/wled's own resolver
// says the same thing. It is skipped rather than reported, because there is no number to offer a
// caller who has to write one back.
func presetSlots(presets json.RawMessage) ([]presetSlot, error) {
	var table map[string]json.RawMessage

	if err := json.Unmarshal(presets, &table); err != nil {
		return nil, fmt.Errorf("the preset table is not an object keyed by slot: %w", err)
	}

	slots := make([]presetSlot, 0, len(table))

	for key, body := range table {
		slot, convErr := strconv.Atoi(strings.TrimSpace(key))
		if convErr != nil || slot < 1 {
			continue
		}

		var entry presetEntry
		// An entry that is not an object — `null`, a number, a string — carries no name. That is
		// not an error: it is still a slot, and an unnamed one is still offered.
		_ = json.Unmarshal(body, &entry)

		name := strings.TrimSpace(entry.Name)

		slots = append(slots, presetSlot{Slot: slot, Name: name, Unnamed: name == ""})
	}

	sort.Slice(slots, func(i, j int) bool { return slots[i].Slot < slots[j].Slot })

	return slots, nil
}

// presetsHeld is how many presets a snapshot's table defines, by the same reading the Devices route
// lists them with: slot 0 excluded, because the firmware keeps it for itself. A table this build
// cannot read holds no presets it can count.
func presetsHeld(presets json.RawMessage) int {
	slots, err := presetSlots(presets)
	if err != nil {
		return 0
	}

	return len(slots)
}

// deviceOf is one light as a snapshot records it: this module's name for it, the light's own, and
// the display the configuration was written for.
func deviceOf(found controllerAnswer, info wled.Info) snapshotDevice {
	device := snapshotDevice{
		Name:       found.Name,
		Address:    found.Address,
		Controller: strings.TrimSpace(info.Name),
		Firmware:   strings.TrimSpace(info.Version),
		Kind:       snapshotKindStrip,
		Width:      info.LEDCount,
		Height:     1,
	}

	if info.MatrixWidth > 0 && info.MatrixHeight > 0 {
		device.Kind = snapshotKindPanel
		device.Width = info.MatrixWidth
		device.Height = info.MatrixHeight
	}

	device.Pixels = device.Width * device.Height

	return device
}
