// The Devices screen's routes: a light's snapshots, its own presets, putting a snapshot back, and the
// flash that says which light a name means.
//
// # What this surface is
//
// docs/product-requirements.md §2.6 gives every light a card carrying its hardware facts, its
// segments, its presets and two buttons: `refresh now`, which reads the light immediately, and
// `download a backup`, which hands the same thing to a file outside the module. §5's third item is
// the pair behind them: GET a light's snapshots, and POST one back. The card's presets are the
// third route here, read-only, because §2.3 makes a moment's `which` "the light's own presets by
// name" and a name nobody can look up is a name nobody can check.
//
// A snapshot exists because a dead light cannot be read. It is not a cache — the requirements say so
// in as many words — it is the copy that was taken while the device was alive, which is exactly the
// copy an operator needs after the device is not.
//
// # The restore, which is the destructive half
//
// Nothing else on this module's surface changes a controller's own configuration. A frame is a
// colour and a fill, a preview holds a picture for a minute, a borrow gives the segment back; a
// restore rewrites the light. So the route is deliberately hard to fire by accident and deliberately
// slow to answer:
//
//   - It takes an explicit confirmation member. A body without it is refused before the snapshot is
//     even read, because a caller that did not mean it must not be able to find out what it would
//     have done.
//   - It takes a safety snapshot of the light as it is now, and keeps the file, before anything is
//     written. A restore that went wrong is reversible, and the answer carries the id of the copy to
//     reverse it with. If that copy cannot be taken, nothing is written: an unreversible destructive
//     change is not one this module makes.
//   - It refuses a snapshot from another light, and one from a light of a different shape or pixel
//     count. Both are the requirements' own rule for replacing hardware, and both are the same
//     failure seen from two sides: a configuration written to the wrong display is a dark or a
//     scrambled light at three in the morning.
//   - It waits for the light to answer again before it says it worked. A configuration write needs
//     "rb": true to reboot, and a light that is mid-reboot cannot be read; an answer written while
//     that is happening is a lie the page will believe.
//
// # What it does not do
//
// It does not restore the firmware revision, the hardware revision, or the network credentials —
// docs/product-requirements.md §2.6 and the measurement in internal/wled/config.go — and it says
// which, section by section, in every answer. A literal hundred-per-cent clone is impossible over
// the API, and the page says so rather than letting an operator believe otherwise.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/prusa3d/prusa-connect/sdk/fault"

	"github.com/nothingmn/wled-printer-status/internal/wled"
)

const (
	// deviceReadTimeout bounds the whole of one snapshot's reads — the light's facts, its
	// configuration, its presets and its state — rather than each one. Four reads at the client's
	// own two-second timeout would be eight seconds, and a route has ten; five seconds is past any
	// answer these lights give (65 ms for a configuration, a quarter of a second for the whole
	// 3.2 KB dump over wifi) and short enough that the route still answers.
	deviceReadTimeout = 5 * time.Second

	// restorePollEvery is how often a restore asks a light whether it is back. It is a poll and
	// not a read of a state: a light rebooting answers nothing at all until its web server is up
	// again, so the poll's own failure is the signal that the reboot happened.
	restorePollEvery = 200 * time.Millisecond

	// restoreWait is how long a restore waits for the light to answer again.
	//
	// The bounds are both real. The host gives a request into a route ten seconds before the
	// caller gets nothing at all, so nine seconds of waiting leaves room for the write, the two
	// snapshots and the answer. The measured reboot on the operator's strip is 5.7 s from the
	// write to the first answer, which leaves three seconds of margin on the only case anybody has
	// put a stopwatch to. A light slower than that is reported as not having answered, with the
	// safety snapshot named in the refusal, which is the honest answer and not a false success.
	restoreWait = 9 * time.Second

	// rebootSettle is how long after a configuration write an answer is not believed.
	//
	// The write itself answers in about 65 ms and the light goes on serving for a moment after
	// that before it restarts, so a poll inside this window is very likely the pre-reboot light
	// answering — and counting it would report a restore complete while the light was about to
	// disappear for six seconds.
	rebootSettle = time.Second

	// referenceFirstRefusal and referenceAnsweredAfter are the measured reboot, carried in every
	// restore answer so that a page can say "about six seconds" from a number rather than from a
	// guess, and so that a light that comes back in twelve stands out against one that came back
	// in five.
	referenceFirstRefusal  = 635 * time.Millisecond
	referenceAnsweredAfter = 5667 * time.Millisecond

	// referenceSource is where those two numbers were measured, in as many words as fit on a
	// screen: an operator comparing this light's reboot against them deserves to know whose reboot
	// they are.
	referenceSource = "measured on CyberDesk, WLED 16.0.1, by hand before this route existed: docs/handoff.md §6"
)

// controllerConfig is the part of a controller a snapshot and a restore use. *wled.Client is the
// only implementation in the process; the interface exists so that a test can drive both halves
// without a socket, and so that nothing here can reach a light's pixels.
type controllerConfig interface {
	// Info is the light's facts, which is where its identity and its shape come from.
	Info(ctx context.Context) (wled.Info, error)
	// StateDocument is what the light is doing now, kept in a snapshot as the record of the moment.
	StateDocument(ctx context.Context) (json.RawMessage, error)
	// Config is GET /json/cfg, verbatim.
	Config(ctx context.Context) (json.RawMessage, error)
	// Presets is GET /presets.json, verbatim.
	Presets(ctx context.Context) (json.RawMessage, error)
	// WriteConfig is POST /json/cfg, with whatever the body says about rebooting.
	WriteConfig(ctx context.Context, body json.RawMessage) error
}

// deviceAPI is the Devices screen's routes, and what they reach: the watch, for the lights it
// drives, and the granted directory, for the files.
//
// It is a value with its own fields for the reason bindingAPI is: a test runs the same handlers with
// a fake light, a directory of its own and a clock it moves, and needs no host and no controller.
type deviceAPI struct {
	live *runner

	// tables is the granted directory, reached through the same value the editor's routes use so
	// that there is one answer to where this module's files live.
	tables *configTables

	// newDevice builds the client for one light. Nil means wled.New, which is what the process
	// uses; a test supplies a fake.
	newDevice func(address string) (controllerConfig, error)

	// clock is the clock a snapshot is dated by and a restore's wait is measured against. Nil
	// means time.Now.
	clock func() time.Time

	// wait overrides restoreWait, and poll overrides restorePollEvery. Both are fields for one
	// reason: a test that proved the wait would otherwise spend nine seconds proving it.
	wait time.Duration
	poll time.Duration

	// rest is how the wait passes its time. Nil means time.Sleep; a test hands it a fake clock,
	// so that the sequence of a reboot can be walked through in microseconds.
	rest func(time.Duration)
}

// now is the instant this surface judges a snapshot and a reboot at.
func (api deviceAPI) now() time.Time {
	if api.clock != nil {
		return api.clock()
	}

	return time.Now()
}

// sleep is how a poll waits for its next attempt.
func (api deviceAPI) sleep(d time.Duration) {
	if api.rest != nil {
		api.rest(d)

		return
	}

	time.Sleep(d)
}

// directory is the granted directory, and empty when this module has none.
func (api deviceAPI) directory() string {
	if api.tables == nil {
		return ""
	}

	return api.tables.directory()
}

// mount adds the Devices screen's routes to mux.
//
// The name is a light's name on these routes exactly as it is on the controller routes: its address
// without the scheme, so that one noun has one naming rule. `/devices` rather than `/controllers`
// because the screen is Devices and the requirement names the route that way; the thing named is the
// same thing, which is why there is no second way to list them — GET /controllers is that list.
func (api deviceAPI) mount(mux *http.ServeMux) {
	// The snapshots this module holds for one light, newest first.
	mux.HandleFunc("GET /devices/{name}/snapshot", api.listSnapshot)

	// A snapshot taken now, which is the screen's `refresh now`: the same reads the five-minute
	// cycle would make, on demand, because the light in front of a person is the one worth copying
	// before he changes something.
	mux.HandleFunc("POST /devices/{name}/snapshot", api.takeSnapshot)

	// One snapshot by id, with everything a restore would send and everything it would not.
	mux.HandleFunc("GET /devices/{name}/snapshot/{id}", api.readSnapshot)

	// The light's own presets, by slot and name. Read-only and a read of the light: this is what
	// the Events screen picks a moment's `which` from and what the Devices screen shows as a
	// light's own list. Nothing on this route writes to a controller.
	mux.HandleFunc("GET /devices/{name}/presets", api.listPresets)

	// Putting one back. See the file's own comment for why this one is hard to fire and slow to
	// answer.
	mux.HandleFunc("POST /devices/{name}/restore", api.restore)

	// `identify`: the light goes on and off, one second in each state, five times, so that a person
	// can see which light a name means — and then it is put back exactly as it was found. It is the
	// one route on this surface that writes a light's power, and it takes the light the way a preset
	// moment does rather than writing behind the writer. See identify.go.
	mux.HandleFunc("POST /devices/{name}/identify", api.identify)
}

// deviceNamed is one light the running set drives, by the name these routes give it.
//
// The set and not a discovery sweep: a snapshot is of a light this module is responsible for, and a
// route that would copy any address on the network a caller named is a route that reads somebody
// else's device. Discovery, and adding a light, are the Bindings screen's own work.
func (api deviceAPI) deviceNamed(name string) (controllerAnswer, bool, error) {
	found, err := controllersOf(api.live.set())
	if err != nil {
		return controllerAnswer{}, false, err
	}

	for _, one := range found {
		if one.Name == name {
			return one, true, nil
		}
	}

	return controllerAnswer{}, false, nil
}

// lightFor is the client for one light: the test's when one was supplied, and the process's own
// otherwise.
func (api deviceAPI) lightFor(address string) (controllerConfig, error) {
	if api.newDevice != nil {
		return api.newDevice(address)
	}

	return wled.New(address)
}

// unknownDevice is the refusal for a light name the running set does not drive, listing the ones it
// does: the controller routes' own code, because a light here is a controller.
func (api deviceAPI) unknownDevice(named string) *fault.Fault {
	found, _ := controllersOf(api.live.set())

	names := make([]string, 0, len(found))
	for _, one := range found {
		names = append(names, one.Name)
	}

	return ControllerNotFound.New(fmt.Sprintf(
		"This module drives no light of that name. It drives %s.", strings.Join(names, ", "))).
		With(fault.StringParam("controller", strings.TrimSpace(named)))
}

// deviceAndLight is the resolved light and its client, with the refusal already chosen for a name
// this module does not drive.
func (api deviceAPI) deviceAndLight(name string) (controllerAnswer, controllerConfig, *fault.Fault) {
	found, ok, err := api.deviceNamed(name)
	if err != nil {
		return controllerAnswer{}, nil, BindingRefused.New(err.Error())
	}

	if !ok {
		return controllerAnswer{}, nil, api.unknownDevice(name)
	}

	light, err := api.lightFor(found.Address)
	if err != nil {
		return controllerAnswer{}, nil, ControllerNotFound.New(fmt.Sprintf(
			"This module drives %s and cannot build a client for it: %v", found.Address, err))
	}

	return found, light, nil
}

// ---------------------------------------------------------------------------------------------
// Taking one
// ---------------------------------------------------------------------------------------------

// snapshotAnswer is one snapshot as a caller reads it.
//
// It carries the file's size beside the light's identity because both are things a person picks a
// snapshot by: two files of one light are told apart by their moment and by which of them has the
// presets in it. The omissions are on every answer that names a snapshot, not only on the one that
// lists them, because the page that offers a restore is the page whose warning matters.
type snapshotAnswer struct {
	ID       string             `json:"id"`
	TakenAt  string             `json:"taken_at"`
	Device   snapshotDevice     `json:"device"`
	Bytes    int64              `json:"bytes"`
	Presets  int                `json:"presets"`
	Omits    []snapshotOmission `json:"omits"`
	Complete bool               `json:"complete_clone"`
	Note     string             `json:"note,omitempty"`
}

// answerOf is one held snapshot as the routes read it.
func snapshotAnswerOf(held heldSnapshot) snapshotAnswer {
	said := snapshotAnswer{
		ID:       held.id,
		TakenAt:  held.doc.TakenAt,
		Device:   held.doc.Device,
		Bytes:    held.size,
		Presets:  presetsHeld(held.doc.Presets),
		Omits:    omissionsOf(held.doc),
		Complete: false,
	}

	if held.why != "" {
		said.Note = "This file is in this module's snapshot folder and cannot be read: " + held.why
	}

	return said
}

// emptyCloneNote is the sentence every answer that names a snapshot carries, because it is the one
// thing an operator must not be left to assume.
const emptyCloneNote = "This is the light's configuration and presets, not a clone of the device: the wifi password is not " +
	"in a configuration dump at all, so a light restored from it keeps the network it is on."

// capture reads one light and dates its snapshot, and writes nothing.
//
// The four reads are one context with one bound: a light that answers its facts and then hangs on
// its presets is one read that took too long, not three that worked.
func (api deviceAPI) capture(ctx context.Context, found controllerAnswer, light controllerConfig) (snapshotDocument, time.Time, error) {
	at := api.now()

	read, cancel := context.WithTimeout(ctx, deviceReadTimeout)
	defer cancel()

	info, err := light.Info(read)
	if err != nil {
		return snapshotDocument{}, at, fmt.Errorf("the light's facts could not be read: %w", err)
	}

	config, err := light.Config(read)
	if err != nil {
		return snapshotDocument{}, at, fmt.Errorf("its configuration could not be read: %w", err)
	}

	presets, err := light.Presets(read)
	if err != nil {
		return snapshotDocument{}, at, fmt.Errorf("its presets could not be read: %w", err)
	}

	state, err := light.StateDocument(read)
	if err != nil {
		return snapshotDocument{}, at, fmt.Errorf("what it is doing now could not be read: %w", err)
	}

	return snapshotDocument{
		Format:  snapshotFormat,
		TakenAt: at.UTC().Format(time.RFC3339),
		Device:  deviceOf(found, info),
		Config:  config,
		Presets: presets,
		State:   state,
	}, at, nil
}

// takeSnapshot is POST /devices/{name}/snapshot: the light in front of a person, copied now.
func (api deviceAPI) takeSnapshot(w http.ResponseWriter, r *http.Request) {
	found, light, refused := api.deviceAndLight(r.PathValue("name"))
	if refused != nil {
		refuse(w, r, refused)

		return
	}

	dir := api.directory()
	if dir == "" {
		refuse(w, r, StoreUnavailable.New(noStoreNote))

		return
	}

	doc, at, err := api.capture(r.Context(), found, light)
	if err != nil {
		refuse(w, r, SnapshotUnreadable.New(fmt.Sprintf(
			"%s was not snapshotted: %v", found.Name, err)))

		return
	}

	saved, err := saveSnapshot(dir, doc, at)
	if err != nil {
		refuse(w, r, StoreUnavailable.New(err.Error()))

		return
	}

	held, err := loadSnapshot(dir, saved.ID)
	if err != nil {
		refuse(w, r, StoreUnavailable.New(err.Error()))

		return
	}

	said := snapshotAnswerOf(heldSnapshot{doc: held, size: snapshotSizeOf(dir, saved.ID), id: saved.ID})
	said.Note = emptyCloneNote

	answer(w, http.StatusOK, map[string]any{
		"device":   found.Name,
		"snapshot": said,
		"note":     persistedNote + " " + emptyCloneNote,
	})
}

// listSnapshot is GET /devices/{name}/snapshot: every snapshot this module holds for one light,
// newest first — and the light's own identity beside them, so a page can say what it is about to
// copy without a second request.
//
// A light that is not answering is not a refusal here: a snapshot exists because a dead light cannot
// be read, so the list of them has to work while the light is dead. Nothing on this route opens a
// socket to a controller.
func (api deviceAPI) listSnapshot(w http.ResponseWriter, r *http.Request) {
	found, ok, err := api.deviceNamed(r.PathValue("name"))
	if err != nil {
		refuse(w, r, BindingRefused.New(err.Error()))

		return
	}

	if !ok {
		refuse(w, r, api.unknownDevice(r.PathValue("name")))

		return
	}

	dir := api.directory()
	if dir == "" {
		answer(w, http.StatusOK, map[string]any{
			"device":    found.Name,
			"address":   found.Address,
			"snapshots": []snapshotAnswer{},
			"count":     0,
			"persisted": false,
			"note":      noStoreNote,
		})

		return
	}

	held, err := listSnapshots(dir)
	if err != nil {
		refuse(w, r, StoreUnavailable.New(err.Error()))

		return
	}

	mine := make([]snapshotAnswer, 0, len(held))
	unreadable := make([]map[string]any, 0)

	for _, one := range held {
		if one.why != "" {
			// A file whose light cannot be known is still this module's, and a person looking
			// for a backup he took needs to see that it is there and why it will not open.
			unreadable = append(unreadable, map[string]any{"id": one.id, "why": one.why})

			continue
		}

		if one.doc.Device.Name != found.Name {
			continue
		}

		mine = append(mine, snapshotAnswerOf(one))
	}

	said := map[string]any{
		"device":    found.Name,
		"address":   found.Address,
		"snapshots": mine,
		"count":     len(mine),
		"persisted": true,
		"note":      emptyCloneNote,
	}

	if len(unreadable) > 0 {
		said["unreadable"] = unreadable
	}

	answer(w, http.StatusOK, said)
}

// readSnapshot is GET /devices/{name}/snapshot/{id}: one snapshot, with the body a restore would
// send and the list of what it would not.
//
// The plan is worked out here and not at restore time only, because the page that offers the button
// is the page that has to show the table: eight sections written, three refused, and the network
// section sent without its credential. A restore shows the same table again, from the same function.
func (api deviceAPI) readSnapshot(w http.ResponseWriter, r *http.Request) {
	found, ok, err := api.deviceNamed(r.PathValue("name"))
	if err != nil {
		refuse(w, r, BindingRefused.New(err.Error()))

		return
	}

	if !ok {
		refuse(w, r, api.unknownDevice(r.PathValue("name")))

		return
	}

	dir := api.directory()
	if dir == "" {
		refuse(w, r, StoreUnavailable.New(noStoreNote))

		return
	}

	doc, err := loadSnapshot(dir, r.PathValue("id"))
	if errors.Is(err, errNoSnapshot) {
		refuse(w, r, SnapshotNotFound.New(fmt.Sprintf(
			"This module holds no snapshot called %q for %s. Read GET /devices/%s/snapshot for the ones it holds.",
			strings.TrimSpace(r.PathValue("id")), found.Name, found.Name)).
			With(fault.StringParam("snapshot", strings.TrimSpace(r.PathValue("id")))))

		return
	}

	if err != nil {
		refuse(w, r, StoreUnavailable.New(err.Error()))

		return
	}

	if doc.Device.Name != found.Name {
		refuse(w, r, SnapshotNotFound.New(fmt.Sprintf(
			"The snapshot %q was taken from %s and this route names %s.",
			doc.ID, doc.Device.Name, found.Name)).
			With(fault.StringParam("snapshot", strings.TrimSpace(r.PathValue("id")))))

		return
	}

	size, err := snapshotSize(dir, doc.ID)
	if err != nil {
		size = 0
	}

	said := snapshotAnswerOf(heldSnapshot{doc: doc, size: size, id: doc.ID})
	said.Note = emptyCloneNote

	body := map[string]any{
		"device":   found.Name,
		"snapshot": said,
		"counts": map[string]any{
			"config_bytes":  len(doc.Config),
			"presets_bytes": len(doc.Presets),
			"state_bytes":   len(doc.State),
		},
	}

	// A snapshot this build cannot build a body for is still readable: the page can show what is
	// in it and why it will not restore, which is better than a 404 about a file that exists.
	plan, planErr := restoreBody(doc)
	if planErr != nil {
		body["restorable"] = false
		body["unrestorable"] = planErr.Error()
	} else {
		body["restorable"] = true
		body["restore"] = plan
	}

	answer(w, http.StatusOK, body)
}

// ---------------------------------------------------------------------------------------------
// The light's own presets
// ---------------------------------------------------------------------------------------------

// presetsAnswer is one light's own preset table as a caller reads it: the slot numbers and the
// names, and which of the two facts about them this module could establish.
//
// The two members are separate for the reason controllerReadAnswer's are. Device and Address are
// this module's own record and are always true. Presets is the light's answer, and PresetsKnown
// says whether there is one: a light that does not answer leaves it false with the reason in
// PresetsUnread and an empty list, because an empty list presented as the light's answer reads as a
// light holding no presets — and a picker built on that would send the operator to WLED's own
// interface to build one he already has.
type presetsAnswer struct {
	Device  string `json:"device"`
	Address string `json:"address"`

	PresetsKnown bool         `json:"presets_known"`
	Presets      []presetSlot `json:"presets"`
	Count        int          `json:"count"`

	// PresetsUnread says why the list is empty, and is absent when the light answered.
	PresetsUnread string `json:"presets_unread,omitempty"`

	Note string `json:"note"`
}

const (
	// presetZeroNote is the one omission this route makes, said in words rather than left to be
	// discovered. Measured, not assumed: see presetSlots.
	presetZeroNote = "Slot 0 is not in this list: WLED keeps it for itself, and both lights this build was " +
		"measured against answer with an empty object there — no name and no body. Every slot WLED's own " +
		"interface offers is listed, unnamed ones included."

	// presetNoneNote is an empty list that means what it says.
	presetNoneNote = "This light answered and holds no presets of its own. A moment that shows a preset needs one " +
		"built and tuned in WLED's own interface first, because this module only ever names one and never " +
		"writes one."

	// presetUnreadNote is the empty list that means something else entirely, and it names where the
	// last copy of the table is — the snapshot taken while the light was alive.
	presetUnreadNote = "The list is empty because this module could not read the light, not because the light " +
		"holds no presets. Read GET /devices/{name}/snapshot for the copies this module already holds: each " +
		"one carries the light's whole preset table as it was the last time the light answered."

	// presetUnnamedNote is said only when the light holds at least one slot with no name, because a
	// blank row in a picker otherwise reads as a rendering fault.
	presetUnnamedNote = " At least one slot carries no name; each of those is still a preset the light holds " +
		"and can be run by its slot, so it is listed with an empty name and unnamed true rather than left out."
)

// listPresets is GET /devices/{name}/presets: the presets the light itself holds, by slot and name.
//
// # Why this route exists
//
// docs/product-requirements.md §2.3 makes a moment's `which` "the light's own presets by name", and
// §2.6 gives every light its presets read-only. A name typed into a box because the module cannot
// list the light's own table is a name nobody can check, and a screen that shows a light's presets
// cannot show them without asking. This is the asking: one read of /presets.json, through the parser
// the snapshot already counts with, and slot and name per preset.
//
// # Why an unreachable light is a 200
//
// It is the shape GET /controllers/{name} already answers with, for the same reason: the lights this
// module drives are a fact even when one of them is unplugged, so the route answers, says the list
// could not be read, and says why. A light that is asleep must not turn a page into a failure — the
// events and moments this module holds are still there, and the screen has the stored names to fall
// back on. The reason travels in presets_unread, the list is empty, and presets_known is false.
//
// # What it never does
//
// It reads and nothing else. There is no preset here whose body this module copies, keeps or writes,
// and no member that could carry one: naming a preset is the whole of this module's part in them.
func (api deviceAPI) listPresets(w http.ResponseWriter, r *http.Request) {
	found, ok, err := api.deviceNamed(r.PathValue("name"))
	if err != nil {
		refuse(w, r, BindingRefused.New(err.Error()))

		return
	}

	if !ok {
		refuse(w, r, api.unknownDevice(r.PathValue("name")))

		return
	}

	said := presetsAnswer{Device: found.Name, Address: found.Address, Presets: []presetSlot{}}

	table, err := api.presetTableOf(r.Context(), found.Address)
	if err == nil {
		var slots []presetSlot

		slots, err = presetSlots(table)
		if err == nil {
			said.PresetsKnown = true
			said.Presets = slots
			said.Count = len(slots)
			said.Note = presetZeroNote

			if len(slots) == 0 {
				said.Note = presetNoneNote
			}

			for _, one := range slots {
				if one.Unnamed {
					said.Note += presetUnnamedNote

					break
				}
			}
		}
	}

	if err != nil {
		// The read failed, or the light answered with something that is not a preset table. Both
		// are the same fact to a caller: this module has no list, and the reason is here.
		said.PresetsUnread = err.Error()
		said.Note = presetUnreadNote
	}

	answer(w, http.StatusOK, said)
}

// presetTableOf reads one light's /presets.json, bounded, through the same client the snapshots use.
//
// The bound is the devices surface's own: the client already times out at two seconds, and this is
// the outer guard for a socket that accepts the connection and then says nothing — the case a
// timeout on the write does not cover.
func (api deviceAPI) presetTableOf(ctx context.Context, address string) (json.RawMessage, error) {
	light, err := api.lightFor(address)
	if err != nil {
		return nil, fmt.Errorf("this module drives %s and cannot build a client for it: %w", address, err)
	}

	read, cancel := context.WithTimeout(ctx, deviceReadTimeout)
	defer cancel()

	return light.Presets(read)
}

// ---------------------------------------------------------------------------------------------
// Putting one back
// ---------------------------------------------------------------------------------------------

// restoreRequest is the body POST /devices/{name}/restore accepts.
//
// Two members and no more. `snapshot` names the file; `confirm` is the operator saying out loud that
// he means it, and it is a member of the body rather than a query parameter because a query
// parameter is what a browser puts in a link somebody can be tricked into opening.
type restoreRequest struct {
	Snapshot string `json:"snapshot"`
	Confirm  bool   `json:"confirm"`
}

// readRestoreRequest is one request body, and what is wrong with it.
func readRestoreRequest(r *http.Request) (restoreRequest, error) {
	var sent restoreRequest

	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(&sent); err != nil {
		return sent, fmt.Errorf(
			"the body is not a restore request: %w: write snapshot and confirm, and nothing else", err)
	}

	if strings.TrimSpace(sent.Snapshot) == "" {
		return sent, errors.New(
			"the body names no snapshot: read GET /devices/{name}/snapshot for the ids, and write one in snapshot")
	}

	return sent, nil
}

// rebootReport is what a restore measured while it waited for the light to come back.
//
// Every number here is taken rather than assumed, including the two it is compared against. The
// reference is the hand measurement this route was built from, and it is in the answer so that a
// page can say "about six seconds" from a number, and so that a light which took twelve stands out.
type rebootReport struct {
	Requested            bool     `json:"requested"`
	Observed             bool     `json:"observed"`
	Polls                int      `json:"polls"`
	FailedPolls          int      `json:"failed_polls"`
	FirstRefusalSeconds  float64  `json:"first_refusal_seconds,omitempty"`
	AnsweredAfterSeconds float64  `json:"answered_after_seconds"`
	OutageSeconds        float64  `json:"outage_seconds,omitempty"`
	WaitedSeconds        float64  `json:"waited_seconds"`
	Reference            []string `json:"measured_reference"`
	Source               string   `json:"measured_source"`
	Note                 string   `json:"note,omitempty"`
}

// seconds is a duration as a person reads it: three places is a millisecond, which is the finest
// thing a poll of this length can honestly claim.
func seconds(d time.Duration) float64 {
	return float64(d.Microseconds()) / 1e6
}

// restore is POST /devices/{name}/restore.
//
// The order of the checks is the whole of the safety: nothing is written until the snapshot has been
// shown to belong to this light, this light has been shown to be the display the snapshot was taken
// from, and a copy of the light as it is now is on disk. Every refusal before the write leaves the
// light exactly as it was.
func (api deviceAPI) restore(w http.ResponseWriter, r *http.Request) {
	found, ok, err := api.deviceNamed(r.PathValue("name"))
	if err != nil {
		refuse(w, r, BindingRefused.New(err.Error()))

		return
	}

	if !ok {
		refuse(w, r, api.unknownDevice(r.PathValue("name")))

		return
	}

	dir := api.directory()
	if dir == "" {
		refuse(w, r, StoreUnavailable.New(noStoreNote))

		return
	}

	sent, err := readRestoreRequest(r)
	if err != nil {
		refuse(w, r, RestoreBodyInvalid.New(err.Error()))

		return
	}

	// The confirmation, before the snapshot is even read: a caller that did not mean to restore
	// must not be able to learn from this route what it would have done.
	if !sent.Confirm {
		refuse(w, r, RestoreNotConfirmed.New(
			"confirm is false, and a restore rewrites the light's whole configuration: send snapshot and "+
				"confirm: true to mean it. Nothing was read and nothing was written.").
			With(fault.StringParam("snapshot", strings.TrimSpace(sent.Snapshot))))

		return
	}

	doc, err := loadSnapshot(dir, sent.Snapshot)
	if errors.Is(err, errNoSnapshot) {
		refuse(w, r, SnapshotNotFound.New(fmt.Sprintf(
			"This module holds no snapshot called %q for %s. Read GET /devices/%s/snapshot for the ones it holds.",
			strings.TrimSpace(sent.Snapshot), found.Name, found.Name)).
			With(fault.StringParam("snapshot", strings.TrimSpace(sent.Snapshot))))

		return
	}

	if err != nil {
		refuse(w, r, StoreUnavailable.New(err.Error()))

		return
	}

	light, err := api.lightFor(found.Address)
	if err != nil {
		refuse(w, r, RestoreRefused.New(fmt.Sprintf(
			"This module drives %s and cannot build a client for it: %v. Nothing was written.",
			found.Address, err)))

		return
	}

	// The light's own identity, read now. A restore cannot be checked against a light nobody can
	// read, and it must not be attempted on one: the address alone does not say which box answers.
	info, err := api.identity(r.Context(), light)
	if err != nil {
		refuse(w, r, RestoreRefused.New(fmt.Sprintf(
			"%s did not answer, so this module cannot tell whether the snapshot belongs to it: %v. Nothing was written.",
			found.Name, err)))

		return
	}

	live := deviceOf(found, info)

	if refusal := sameLight(doc.Device, live); refusal != "" {
		refuse(w, r, RestoreRefused.New(refusal+". Nothing was written.").
			With(fault.StringParam("snapshot", doc.ID)).
			With(fault.StringParam("controller", found.Name)))

		return
	}

	// The safety snapshot, before anything is written. If it cannot be taken, nothing is: a
	// destructive change that cannot be undone is not one this module makes.
	safetyDoc, safetyAt, err := api.capture(r.Context(), found, light)
	if err != nil {
		refuse(w, r, SnapshotUnreadable.New(fmt.Sprintf(
			"%s was not restored: a copy of its configuration as it is now could not be taken first, so the "+
				"restore would not be reversible: %v. Nothing was written.", found.Name, err)))

		return
	}

	safetySaved, err := saveSnapshot(dir, safetyDoc, safetyAt)
	if err != nil {
		refuse(w, r, StoreUnavailable.New(fmt.Sprintf(
			"%s was not restored: the safety snapshot of its configuration could not be written, so the restore "+
				"would not be reversible: %v. Nothing was written.", found.Name, err)))

		return
	}

	plan, err := restoreBody(doc)
	if err != nil {
		refuse(w, r, RestoreRefused.New(fmt.Sprintf(
			"%s was not restored: %v. Nothing was written; the safety snapshot %s is kept.",
			found.Name, err, safetySaved.ID)).
			With(fault.StringParam("snapshot", doc.ID)))

		return
	}

	// The write and the wait run on a context that does not die with the caller.
	//
	// A restore is destructive and one-way: once the body is on its way, a caller that hangs up
	// must not be able to leave a light holding half a configuration because the connection it
	// asked over went away. So the write is detached from the request — and bounded by the wait
	// below, not by the client.
	write := context.WithoutCancel(r.Context())

	wroteAt := api.now()

	if err := light.WriteConfig(write, plan.Body); err != nil {
		refuse(w, r, RestoreRefused.New(fmt.Sprintf(
			"%s refused the configuration: %v. The safety snapshot %s is kept, and restoring it puts the light "+
				"back as it was.", found.Name, err, safetySaved.ID)).
			With(fault.StringParam("snapshot", doc.ID)).
			With(fault.StringParam("safety_snapshot", safetySaved.ID)))

		return
	}

	reboot, err := api.waitForAnswer(write, light, wroteAt)
	if err != nil {
		refuse(w, r, RestoreNotAnswered.New(fmt.Sprintf(
			"%s was written the snapshot and has not answered again in %.1f seconds, so this module cannot say "+
				"the restore took: a light that is still rebooting looks exactly like this. It may be about to "+
				"answer. The safety snapshot %s is kept, and restoring it puts the light back as it was.",
			found.Name, seconds(api.waited()), safetySaved.ID)).
			With(fault.StringParam("snapshot", doc.ID)).
			With(fault.StringParam("safety_snapshot", safetySaved.ID)))

		return
	}

	safetySize, statErr := snapshotSize(dir, safetySaved.ID)
	if statErr != nil {
		safetySize = 0
	}

	restored := snapshotAnswerOf(heldSnapshot{doc: doc, size: snapshotSizeOf(dir, doc.ID), id: doc.ID})
	safety := snapshotAnswerOf(heldSnapshot{doc: safetySaved, size: safetySize, id: safetySaved.ID})

	answer(w, http.StatusOK, map[string]any{
		"device":          found.Name,
		"restored":        restored,
		"safety_snapshot": safety,
		"restore":         plan,
		"reboot":          reboot,
		"complete_clone":  false,
		"omits":           omissionsOf(doc),
		"note": "This module wrote the snapshot's configuration back and asked the light to reboot; it answered " +
			"again " + fmt.Sprintf("%.1f", reboot.AnsweredAfterSeconds) + " seconds later. " + emptyCloneNote,
	})
}

// waited is how long a restore waits, which is restoreWait unless a test said otherwise.
func (api deviceAPI) waited() time.Duration {
	if api.wait > 0 {
		return api.wait
	}

	return restoreWait
}

// pollEvery is how often a restore asks.
func (api deviceAPI) pollEvery() time.Duration {
	if api.poll > 0 {
		return api.poll
	}

	return restorePollEvery
}

// identity reads one light's facts, bounded.
func (api deviceAPI) identity(ctx context.Context, light controllerConfig) (wled.Info, error) {
	read, cancel := context.WithTimeout(ctx, deviceReadTimeout)
	defer cancel()

	return light.Info(read)
}

// sameLight is why a snapshot must not be restored onto this light, and empty when it may.
//
// It is two questions and both are refusals. The address says which light the snapshot was taken
// from, and a snapshot of another address is a snapshot of another light. The controller's own name
// says which box is answering now: an address that has been handed to a replacement answers to a
// different name, and that is precisely the case in which writing an old configuration would put the
// wrong light's settings on new hardware. Shape and pixel count come with them, because a
// configuration written to a display of another size is a scrambled picture rather than a restore.
func sameLight(from, to snapshotDevice) string {
	if !strings.EqualFold(strings.TrimSpace(from.Address), strings.TrimSpace(to.Address)) {
		return fmt.Sprintf(
			"the snapshot was taken from %s (%s) and this route names %s (%s), so it is a snapshot of a different light",
			from.Name, from.Address, to.Name, to.Address)
	}

	if from.Kind != to.Kind || from.Width != to.Width || from.Height != to.Height {
		return fmt.Sprintf(
			"the snapshot is a %s of %d x %d (%d pixels) and this light is a %s of %d x %d (%d pixels): a "+
				"configuration written to a display of another shape is a scrambled picture, not a restore",
			from.Kind, from.Width, from.Height, from.Pixels,
			to.Kind, to.Width, to.Height, to.Pixels)
	}

	if !strings.EqualFold(strings.TrimSpace(from.Controller), strings.TrimSpace(to.Controller)) {
		return fmt.Sprintf(
			"the snapshot was taken from the light that called itself %q and the light at %s calls itself %q, "+
				"so something has been replaced at that address",
			from.Controller, to.Address, to.Controller)
	}

	return ""
}

// waitForAnswer waits until the light answers again, and answers what it measured.
//
// The rule is the whole of it: an answer inside rebootSettle of the write is not believed, because
// the light serves for a moment after taking a configuration and before it restarts — counting that
// one would report a completed restore while the light was about to disappear. After that window,
// the first answer is the light back, whether or not a refusal was seen in between; a light that
// never went away is reported as never having gone away, rather than as a reboot nobody observed.
func (api deviceAPI) waitForAnswer(ctx context.Context, light controllerConfig, wroteAt time.Time) (rebootReport, error) {
	report := rebootReport{
		Requested: true,
		Reference: []string{
			fmt.Sprintf("first refusal %.3f s", seconds(referenceFirstRefusal)),
			fmt.Sprintf("first answer %.3f s", seconds(referenceAnsweredAfter)),
		},
		Source: referenceSource,
	}

	deadline := wroteAt.Add(api.waited())
	var firstRefusal time.Time

	for {
		if err := ctx.Err(); err != nil {
			return report, err
		}

		if !api.now().Before(deadline) {
			report.WaitedSeconds = seconds(api.waited())

			return report, errors.New("the light did not answer inside the wait")
		}

		api.sleep(api.pollEvery())

		// One poll is bounded by what is left of the wait as well as by the client's own timeout,
		// so that a poll begun a moment before the deadline cannot carry the route past the ten
		// seconds the host allows a call: the arithmetic is in restoreWait.
		remaining := deadline.Sub(api.now())
		if remaining <= 0 {
			report.WaitedSeconds = seconds(api.waited())

			return report, errors.New("the light did not answer inside the wait")
		}

		bound := deviceReadTimeout
		if remaining < bound {
			bound = remaining
		}

		read, cancel := context.WithTimeout(ctx, bound)
		_, err := light.Info(read)

		cancel()

		at := api.now()
		report.Polls++

		if err != nil {
			report.FailedPolls++

			if firstRefusal.IsZero() {
				firstRefusal = at
				report.FirstRefusalSeconds = seconds(at.Sub(wroteAt))
				report.Observed = true
			}

			continue
		}

		if at.Sub(wroteAt) < rebootSettle {
			// The light that took the write, not the light that came back from a reboot.
			continue
		}

		report.AnsweredAfterSeconds = seconds(at.Sub(wroteAt))
		report.WaitedSeconds = report.AnsweredAfterSeconds

		if report.Observed {
			report.OutageSeconds = seconds(at.Sub(firstRefusal))
			report.Note = "The light stopped answering and answered again, so a reboot was seen."

			return report, nil
		}

		report.Note = "The light answered throughout, so no reboot was seen: either it restarted faster than " +
			"this module polls, or it took the configuration without restarting. The configuration was written " +
			"and the light answered after it."

		return report, nil
	}
}
