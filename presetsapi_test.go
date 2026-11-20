// The Devices screen's read of a light's own presets: GET /devices/{name}/presets.
//
// docs/product-requirements.md §2.3 makes a moment's `which` "the light's own presets by name", and
// §2.6 gives every light its presets read-only. Both need the light's own table, and neither can
// have it from a name typed into a box. These tests hold the route to the five things that can go
// wrong with it: the list itself, slot 0, a slot with no name, a light holding none, and a light
// that does not answer.
//
// Every body below is a fixture shaped like the real thing. The slot numbers and names in
// morpheusPresets are morpheus's own, read from 10.0.0.136 during this work, because a fixture that
// invents its names tests the parser against a document no firmware writes. The bodies are trimmed
// to the members this module ignores: no preset's segments, effect or palette are reproduced here,
// and none can be, because the route has no member to carry one.
package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const (
	// morpheusPresets is 10.0.0.136's own preset table as the light answered it, slot for slot,
	// with each entry cut down to the members this module reads past. Slot 0 is the empty object
	// the firmware actually returns there.
	morpheusPresets = `{
  "0": {},
  "1": {"n": "Matrix ▦", "on": true, "bri": 128, "transition": 7, "mainseg": 0},
  "2": {"n": "Snow Fall ☾ 🎨▦", "on": true, "bri": 128, "transition": 7, "mainseg": 0},
  "3": {"n": "PS Waterfall 🎨▦", "on": true, "bri": 200, "transition": 7, "mainseg": 0},
  "4": {"n": "PS Ballpit 🎨▦", "on": true, "bri": 128, "transition": 7, "mainseg": 0},
  "5": {"n": "PS GEQ 2D Mirror Reversed", "on": true, "bri": 128, "transition": 7, "mainseg": 0},
  "6": {"n": "PS GEQ 2D 🎨▦♫", "on": true, "bri": 128, "transition": 7, "mainseg": 0},
  "7": {"n": "GEQ 3D ☾ 🎨▦♫", "on": true, "bri": 255, "transition": 7, "mainseg": 0},
  "8": {"n": "DJ Light •⋮♫", "on": true, "bri": 255, "transition": 7, "mainseg": 0},
  "9": {"n": "PS GEQ 1D 🎨⋮♫", "on": true, "bri": 128, "transition": 7, "mainseg": 0},
  "10": {"n": "Alien Force Clock Clean Updates", "on": true, "bri": 128, "transition": 7, "mainseg": 0},
  "250": {"n": "~ 11-19 03:36:02 ~", "on": true, "bri": 128, "transition": 7, "mainseg": 0}
}`
)

// presetsOf is one request for a light's presets, and the answer.
func (a *devicesUnderTest) presetsOf(t *testing.T, name string) (int, map[string]any) {
	t.Helper()

	return a.do(t, http.MethodGet, "/devices/"+name+"/presets", "")
}

// ---------------------------------------------------------------------------------------------
// The list
// ---------------------------------------------------------------------------------------------

// TestALightsOwnPresetsAreOfferedBySlotAndName is the whole point of the route: a real light's
// table, read from the light, answered as slots and names in slot order — so that the Events screen
// can offer a moment's preset instead of asking the operator to type one, and the Devices screen has
// the light's own list to show read-only.
//
// It holds the other half of the contract too, the one §2.3 draws: a preset is a name, and a
// preset's body never leaves the light. Every entry carries slot, name and unnamed and nothing
// else, which is what makes it impossible for this route to hand out the segments, effects and
// palettes of a preset — or anything a preset might hold.
func TestALightsOwnPresetsAreOfferedBySlotAndName(t *testing.T) {
	under := newDevicesUnderTest(t)
	under.light.presets = json.RawMessage(morpheusPresets)

	status, body := under.presetsOf(t, "10.0.0.136")
	if status != http.StatusOK {
		t.Fatalf("a light's presets answered %d: %v", status, body)
	}

	if device := readString(t, body, "device"); device != "10.0.0.136" {
		t.Fatalf("the answer names the light %q, want 10.0.0.136", device)
	}

	if address := readString(t, body, "address"); address != "http://10.0.0.136" {
		t.Fatalf("the answer names the address %q, want the one the set drives", address)
	}

	if known, _ := body["presets_known"].(bool); !known {
		t.Fatalf("an answering light's presets are reported as unknown: %v", body)
	}

	want := []struct {
		slot int
		name string
	}{
		{1, "Matrix ▦"},
		{2, "Snow Fall ☾ 🎨▦"},
		{3, "PS Waterfall 🎨▦"},
		{4, "PS Ballpit 🎨▦"},
		{5, "PS GEQ 2D Mirror Reversed"},
		{6, "PS GEQ 2D 🎨▦♫"},
		{7, "GEQ 3D ☾ 🎨▦♫"},
		{8, "DJ Light •⋮♫"},
		{9, "PS GEQ 1D 🎨⋮♫"},
		{10, "Alien Force Clock Clean Updates"},
		{250, "~ 11-19 03:36:02 ~"},
	}

	listed := readObjects(t, body, "presets")
	if len(listed) != len(want) {
		t.Fatalf("the light holds %d presets and the answer lists %d: %v", len(want), len(listed), listed)
	}

	for i, one := range want {
		entry := listed[i]

		if slot, _ := entry["slot"].(float64); int(slot) != one.slot {
			t.Fatalf("preset %d is slot %v, want slot %d — the list is not in slot order", i, entry["slot"], one.slot)
		}

		if name, _ := entry["name"].(string); name != one.name {
			t.Fatalf("slot %d is named %q, want %q", one.slot, name, one.name)
		}

		if unnamed, present := entry["unnamed"]; present && unnamed != false {
			t.Fatalf("slot %d is reported as unnamed: %v", one.slot, unnamed)
		}

		// The promise: a preset is a slot and a name, and no member here could carry a body.
		if len(entry) != 3 {
			t.Fatalf("slot %d is answered with the members %v, want slot, name and unnamed only", one.slot, entry)
		}
	}

	if count, _ := body["count"].(float64); int(count) != len(want) {
		t.Fatalf("the answer counts %v presets, want %d", body["count"], len(want))
	}

	if _, refused := body["presets_unread"]; refused {
		t.Fatalf("an answering light's presets carry a reason they could not be read: %v", body["presets_unread"])
	}

	// Reading a light is all this route does: nothing was written to it.
	if writes := under.light.written(); len(writes) != 0 {
		t.Fatalf("reading the presets wrote %d bodies to the light, want none", len(writes))
	}
}

// TestTheCountIsTheSameReadingASnapshotTakes ties the two answers about one light's presets
// together: the list this route carries and the number a snapshot reports come from one parse, so
// a page that compares them cannot find them disagreeing.
func TestTheCountIsTheSameReadingASnapshotTakes(t *testing.T) {
	under := newDevicesUnderTest(t)
	under.light.presets = json.RawMessage(morpheusPresets)

	_, body := under.presetsOf(t, "10.0.0.136")

	listed := readObjects(t, body, "presets")
	held := presetsHeld(under.light.presets)

	if held != len(listed) || held != 11 {
		t.Fatalf("the snapshot's count is %d and the route lists %d of 11 presets", held, len(listed))
	}
}

// ---------------------------------------------------------------------------------------------
// Slot 0, which is the firmware's and not the operator's
// ---------------------------------------------------------------------------------------------

// TestSlotZeroIsNeverOfferedAsAPreset answers the slot-0 question as a test rather than as a comment.
//
// Measured, not assumed: both of the operator's lights answer /presets.json with slot 0 present as
// an empty object — no name, no body — while every user preset is a slot numbered one or higher
// carrying a name. WLED keeps 0 for itself, so it is not a preset a moment can name and not a row a
// picker should offer.
//
// The second table is the shape the older fixture in this suite imagines, with slot 0 named "Off",
// and it is here for the same reason: whatever slot 0 holds, it is not one of the light's presets,
// and a firmware that did name it must not change the number of presets a light is reported to hold.
func TestSlotZeroIsNeverOfferedAsAPreset(t *testing.T) {
	for _, table := range []string{
		`{"0": {}, "1": {"n": "Printing"}}`,
		`{"0": {"n": "Off"}, "1": {"n": "Printing"}, "2": {"n": "Alert"}}`,
	} {
		under := newDevicesUnderTest(t)
		under.light.presets = json.RawMessage(table)

		status, body := under.presetsOf(t, "10.0.0.136")
		if status != http.StatusOK {
			t.Fatalf("a light's presets answered %d: %v", status, body)
		}

		listed := readObjects(t, body, "presets")

		for _, entry := range listed {
			if slot, _ := entry["slot"].(float64); int(slot) == 0 {
				t.Fatalf("slot 0 was offered as a preset: %v", entry)
			}
		}

		if len(listed) == 0 {
			t.Fatalf("the table %s lists no presets at all", table)
		}

		// The count a snapshot reports and the list this route answers are one reading.
		if held := presetsHeld(json.RawMessage(table)); held != len(listed) {
			t.Fatalf("the table %s is counted as %d presets and listed as %d", table, held, len(listed))
		}
	}
}

// ---------------------------------------------------------------------------------------------
// A slot with no name
// ---------------------------------------------------------------------------------------------

// TestASlotWithNoNameIsStillAPresetTheOperatorCanPick is the empty-name case, measured on neither
// light — both answer with every slot named — and so held to a fixture shaped like the ones they do.
//
// An unnamed slot is still a picture the light holds and can be run by its number. Dropping it would
// leave the operator unable to reach a preset WLED's own interface still lists, so it is offered
// with an empty name and unnamed true, and the answer says in words that at least one row will read
// blank — a blank row nobody explained reads as a rendering fault.
func TestASlotWithNoNameIsStillAPresetTheOperatorCanPick(t *testing.T) {
	under := newDevicesUnderTest(t)
	under.light.presets = json.RawMessage(`{"0": {}, "1": {"n": "Printing"}, "2": {"bri": 128}, "3": {"n": "   "}}`)

	status, body := under.presetsOf(t, "10.0.0.136")
	if status != http.StatusOK {
		t.Fatalf("a light's presets answered %d: %v", status, body)
	}

	listed := readObjects(t, body, "presets")
	if len(listed) != 3 {
		t.Fatalf("the three named slots beside slot 0 are listed as %d presets: %v", len(listed), listed)
	}

	want := []struct {
		slot    int
		name    string
		unnamed bool
	}{
		{1, "Printing", false},
		{2, "", true},
		{3, "", true},
	}

	for i, one := range want {
		entry := listed[i]

		if slot, _ := entry["slot"].(float64); int(slot) != one.slot {
			t.Fatalf("preset %d is slot %v, want slot %d", i, entry["slot"], one.slot)
		}

		if name, _ := entry["name"].(string); name != one.name {
			t.Fatalf("slot %d is named %q, want %q: a name is trimmed, and a missing one is empty", one.slot, name, one.name)
		}

		if unnamed, _ := entry["unnamed"].(bool); unnamed != one.unnamed {
			t.Fatalf("slot %d is reported as unnamed=%v, want %v", one.slot, entry["unnamed"], one.unnamed)
		}
	}

	if note := readString(t, body, "note"); !strings.Contains(note, "carries no name") {
		t.Fatalf("the answer does not say a slot has no name: %q", note)
	}
}

// ---------------------------------------------------------------------------------------------
// A light that holds none, and a light that cannot be asked
// ---------------------------------------------------------------------------------------------

// TestALightThatHoldsNoPresetsSaysSo is an empty list that means what it says. Both of the operator's
// lights hold presets, so the body is the one a light with none answers: slot 0, the firmware's own,
// and nothing else.
func TestALightThatHoldsNoPresetsSaysSo(t *testing.T) {
	under := newDevicesUnderTest(t)
	under.light.presets = json.RawMessage(`{"0": {}}`)

	status, body := under.presetsOf(t, "10.0.0.136")
	if status != http.StatusOK {
		t.Fatalf("a light holding no presets answered %d: %v", status, body)
	}

	if known, _ := body["presets_known"].(bool); !known {
		t.Fatalf("a light that answered its empty table is reported as unknown: %v", body)
	}

	listed, isList := body["presets"].([]any)
	if !isList {
		t.Fatalf("the answer's presets are %v, and not an empty list: a null reads as a fault", body["presets"])
	}

	if len(listed) != 0 {
		t.Fatalf("a light holding no presets listed %v", listed)
	}

	if count, _ := body["count"].(float64); count != 0 {
		t.Fatalf("a light holding no presets counts %v", body["count"])
	}

	note := readString(t, body, "note")
	if !strings.Contains(note, "holds no presets") {
		t.Fatalf("the answer does not say the light holds none: %q", note)
	}

	if note == presetUnreadNote {
		t.Fatal("an empty table was answered with the sentence for a light that could not be read")
	}
}

// TestALightThatDoesNotAnswerIsNotAnEmptyList is the degradation that matters most: a page asking a
// dead light must not be told the light holds nothing. It is the shape GET /controllers/{name}
// already answers with — 200, presets_known false, the reason in words, an empty list — so that the
// Events screen can fall back to the names it already holds instead of failing.
func TestALightThatDoesNotAnswerIsNotAnEmptyList(t *testing.T) {
	under := newDevicesUnderTest(t)

	under.light.presetsErr = errors.New(
		"wled: http://10.0.0.136 is unreachable: dial tcp 10.0.0.136:80: connect: no route to host")

	status, body := under.presetsOf(t, "10.0.0.136")
	if status != http.StatusOK {
		t.Fatalf("a dead light's presets answered %d, want 200 with the reason in the body: %v", status, body)
	}

	if known, _ := body["presets_known"].(bool); known {
		t.Fatalf("a light that did not answer is reported as having answered: %v", body)
	}

	reason := readString(t, body, "presets_unread")
	if !strings.Contains(reason, "unreachable") {
		t.Fatalf("the reason a dead light's presets could not be read is %q, want the light's own failure", reason)
	}

	listed, isList := body["presets"].([]any)
	if !isList || len(listed) != 0 {
		t.Fatalf("a dead light's presets are %v, want an empty list", body["presets"])
	}

	if count, _ := body["count"].(float64); count != 0 {
		t.Fatalf("a dead light counts %v presets", body["count"])
	}

	note := readString(t, body, "note")
	if note == presetNoneNote {
		t.Fatal("a light that could not be read was answered with the sentence for a light holding none")
	}

	if !strings.Contains(note, "GET /devices/{name}/snapshot") {
		t.Fatalf("the answer does not name where the last copy of the table is: %q", note)
	}

	// A read that failed wrote nothing, and could not have: there is no write on this route.
	if writes := under.light.written(); len(writes) != 0 {
		t.Fatalf("an unreadable light was written %d bodies, want none", len(writes))
	}
}

// TestAPresetTableThatIsNotOneIsNotAnEmptyList is the third empty list: a light that answers, with
// something that is not a preset table. It is reported as unread rather than as a light holding no
// presets, because those are different facts and only one of them is true.
func TestAPresetTableThatIsNotOneIsNotAnEmptyList(t *testing.T) {
	under := newDevicesUnderTest(t)
	under.light.presets = json.RawMessage(`[{"n": "Printing"}]`)

	status, body := under.presetsOf(t, "10.0.0.136")
	if status != http.StatusOK {
		t.Fatalf("a malformed table answered %d: %v", status, body)
	}

	if known, _ := body["presets_known"].(bool); known {
		t.Fatalf("a table this build cannot read is reported as the light's own: %v", body)
	}

	if reason := readString(t, body, "presets_unread"); !strings.Contains(reason, "not an object keyed by slot") {
		t.Fatalf("the reason is %q, want the body's own shape named", reason)
	}

	if listed, isList := body["presets"].([]any); !isList || len(listed) != 0 {
		t.Fatalf("a malformed table listed %v", body["presets"])
	}
}

// TestThePresetsOfALightThisModuleDoesNotDriveAreRefused is the one refusal on this route: a name
// this module drives nothing on is not a light it can be asked about, and the answer lists the ones
// it has.
func TestThePresetsOfALightThisModuleDoesNotDriveAreRefused(t *testing.T) {
	under := newDevicesUnderTest(t)

	recorder := httptest.NewRecorder()
	under.mux.ServeHTTP(recorder, forwarded(http.MethodGet, "/devices/10.0.0.99/presets", nil))

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("a light this module does not drive answered %d, want %d: %s",
			recorder.Code, http.StatusNotFound, recorder.Body.String())
	}

	if code := problemCode(t, recorder); !strings.HasSuffix(code, "CONTROLLER_NOT_FOUND") {
		t.Fatalf("the refusal is %q, want CONTROLLER_NOT_FOUND", code)
	}
}
