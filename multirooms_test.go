package main

import (
	"net/http"
	"strings"
	"testing"

	"github.com/prusa3d/prusa-connect/sdk/deviceapi"
)

// The multi-room wave, on the routes: docs/product-requirements.md §2.2 is locked and says "A
// printer may sit on several lights — multi-room status — at the cost of a second steady stream",
// while a segment carries one printer. The tests below are the two halves of that sentence and the
// identity it forced: the second light is accepted, the second binding of one segment is not, and
// the binding that already existed keeps the path it answered at.

// panelPrinter is the printer this module is running for today, by the host's own device id: the panelPrinter on
// 10.0.0.136 carries it whole. The strip on 10.0.0.253 is the second light this wave exists to let
// the operator bind.
const panelPrinter = "07e893bb-b922-4ff0-9ad3-061b957400c3"

// TestOnePrinterOnTwoLightsIsAccepted is the case the build refused until this wave: the same
// printer, a second controller, one more binding.
//
// The added binding's name is asserted exactly, because it is the name a screen will address it by
// and the name the layout table is keyed on: the printer's device id is already the panelPrinter's name,
// so the strip takes the device id, the light and the segment, which is three facts that cannot
// collide with another binding's.
func TestOnePrinterOnTwoLightsIsAccepted(t *testing.T) {
	light := newFakeStrip()

	fleet := []deviceapi.Device{{ID: panelPrinter, Name: "Hermes MK4", Connected: true}}

	api := newAPIUnderTest(t, light, fleet, bindings{
		{DeviceID: panelPrinter, Address: "http://10.0.0.136", Segment: wholeStrip, Brightness: 128},
	})

	status, body := api.do(t, http.MethodPost, "/bindings",
		`{"printer": "Hermes MK4", "controller": "http://10.0.0.253", "brightness": 96}`)
	if status != http.StatusCreated {
		t.Fatalf("adding a second light for one printer answered %d, want %d: %v",
			status, http.StatusCreated, body)
	}

	if body["applied"] != true {
		t.Fatalf("the answer says applied %v, and the second light has to be driving now", body["applied"])
	}

	added := body["binding"].(map[string]any)

	const wantID = panelPrinter + "@10.0.0.253:whole"
	if added["id"] != wantID {
		t.Fatalf("the new binding is named %v, want %q", added["id"], wantID)
	}

	if added["printer"] != panelPrinter {
		t.Fatalf("the new binding shows %v, want the one printer %q", added["printer"], panelPrinter)
	}

	if added["light"] != "10.0.0.253" || added["whole"] != true || added["brightness"].(float64) != 96 {
		t.Fatalf("the new binding reads %v, want the whole of 10.0.0.253 at brightness 96", added)
	}

	set := api.watch.set()
	if len(set) != 2 {
		t.Fatalf("the set holds %d bindings, want 2: one printer on two lights", len(set))
	}

	if set[0].ID != panelPrinter || set[0].Address != "http://10.0.0.136" {
		t.Fatalf("the existing binding reads %+v, and adding a light must not move it", set[0])
	}

	if set[1].ID != wantID || set[1].DeviceID != panelPrinter {
		t.Fatalf("the added binding reads %+v, want the panelPrinter's second light named %q", set[1], wantID)
	}

	// And it answers at its own path, which is what a screen needs to hear back.
	status, one := api.do(t, http.MethodGet, "/bindings/"+wantID, "")
	if status != http.StatusOK {
		t.Fatalf("GET /bindings/%s answered %d, want %d: %v", wantID, status, http.StatusOK, one)
	}
}

// TestASegmentTwiceIsRefusedNamingTheConflict is the rule multi-room does not lift: WLED has no
// ownership, so the only exclusivity there is is the one this module makes. The refusal has to name
// the segment, the light, and the binding that got there first — under multi-room "binding 1" alone
// is a number a person has to count to, in a list where two lines may carry the same printer.
func TestASegmentTwiceIsRefusedNamingTheConflict(t *testing.T) {
	light := newFakeStrip()
	light.state = wledStateForSegments(2)

	api := newAPIUnderTest(t, light, testPrinters, bindings{
		{DeviceID: "printer-a", Address: "http://10.0.0.136", Segment: 2},
	})

	status, body := api.do(t, http.MethodPost, "/bindings",
		`{"printer": "Mini", "controller": "http://10.0.0.136", "segment": 2}`)
	if status != http.StatusConflict {
		t.Fatalf("a second binding on one segment answered %d, want %d: %v", status, http.StatusConflict, body)
	}

	detail, _ := body["detail"].(string)

	for _, want := range []string{"one segment is shown by one binding", "segment 2", "10.0.0.136", "printer-a"} {
		if !strings.Contains(detail, want) {
			t.Fatalf("the refusal reads %q, want it to name %q", detail, want)
		}
	}

	// A refused change leaves the set that was running running.
	if set := api.watch.set(); len(set) != 1 || set[0].Segment != 2 {
		t.Fatalf("the set is now %+v, and a refused change must not disturb it", set)
	}
}

// TestThePanelBindingKeepsItsPathWhenTheStripIsBound is the promise the identity had to keep, and
// it is written as the literal path rather than a computed one on purpose: a screen, a bookmark and
// a curl line hold strings, and a string that the module starts answering differently at is a
// screen that breaks with nothing on the instance to explain it.
//
// The path is the one the running install answers at today: GET
// /bindings/07e893bb-b922-4ff0-9ad3-061b957400c3.
func TestThePanelBindingKeepsItsPathWhenTheStripIsBound(t *testing.T) {
	const panelPath = "/bindings/07e893bb-b922-4ff0-9ad3-061b957400c3"

	light := newFakeStrip()

	fleet := []deviceapi.Device{{ID: panelPrinter, Name: "Hermes MK4", Connected: true}}

	api := newAPIUnderTest(t, light, fleet, bindings{
		{DeviceID: panelPrinter, Address: "http://10.0.0.136", Segment: wholeStrip, Brightness: 128},
	})

	status, before := api.do(t, http.MethodGet, panelPath, "")
	if status != http.StatusOK {
		t.Fatalf("GET %s answered %d before the strip was bound, want %d: %v",
			panelPath, status, http.StatusOK, before)
	}

	if got := before["binding"].(map[string]any)["id"]; got != panelPrinter {
		t.Fatalf("the panelPrinter's binding is named %v, want %q", got, panelPrinter)
	}

	// The strip is bound beside it, which is the change that could have moved the name.
	if code, body := api.do(t, http.MethodPost, "/bindings",
		`{"printer": "Hermes MK4", "controller": "http://10.0.0.253", "brightness": 96}`); code != http.StatusCreated {
		t.Fatalf("binding the strip answered %d, want %d: %v", code, http.StatusCreated, body)
	}

	status, after := api.do(t, http.MethodGet, panelPath, "")
	if status != http.StatusOK {
		t.Fatalf("GET %s answered %d once the strip was bound, want %d: %v",
			panelPath, status, http.StatusOK, after)
	}

	named := after["binding"].(map[string]any)
	if named["id"] != panelPrinter {
		t.Fatalf("the panelPrinter's binding is named %v after the strip was bound, want %q", named["id"], panelPrinter)
	}

	// And it is the same row: the light and the segment did not move either.
	if named["light"] != "10.0.0.136" || named["whole"] != true {
		t.Fatalf("the panelPrinter's binding reads %v, want the whole of 10.0.0.136", named)
	}
}

// TestTwoBindingsOfOnePrinterAnswerDistinguishableBodies is the requirement on the answers rather
// than on the set: a screen draws two rows for one printer, so each row has to say which binding it
// is, which light it is on, and which segment it drives. A list of device ids twice would be two
// identical rows and nothing to act on.
func TestTwoBindingsOfOnePrinterAnswerDistinguishableBodies(t *testing.T) {
	light := newFakeStrip()
	light.state = wledStateForSegments(4)

	fleet := []deviceapi.Device{{ID: panelPrinter, Name: "Hermes MK4", Connected: true}}

	api := newAPIUnderTest(t, light, fleet, bindings{
		{DeviceID: panelPrinter, Address: "http://10.0.0.136", Segment: wholeStrip, Brightness: 128},
		{DeviceID: panelPrinter, Address: "http://10.0.0.253", Segment: 4},
	})

	status, body := api.do(t, http.MethodGet, "/bindings", "")
	if status != http.StatusOK {
		t.Fatalf("GET /bindings answered %d, want %d: %v", status, http.StatusOK, body)
	}

	rows := body["bindings"].([]any)
	if len(rows) != 2 {
		t.Fatalf("the list carries %d bindings, want 2", len(rows))
	}

	first := rows[0].(map[string]any)
	second := rows[1].(map[string]any)

	if first["printer"] != panelPrinter || second["printer"] != panelPrinter {
		t.Fatalf("the two rows show %v and %v, want one printer %q", first["printer"], second["printer"], panelPrinter)
	}

	if first["id"] == second["id"] {
		t.Fatalf("the two rows share the id %v, and a list of two must name two", first["id"])
	}

	if first["light"] != "10.0.0.136" || second["light"] != "10.0.0.253" {
		t.Fatalf("the rows name the lights %v and %v, want 10.0.0.136 and 10.0.0.253",
			first["light"], second["light"])
	}

	if first["whole"] != true || second["whole"] != false || second["segment"].(float64) != 4 {
		t.Fatalf("the rows carry %v/%v and %v/%v, want the panelPrinter whole and the strip on segment 4",
			first["whole"], first["segment"], second["whole"], second["segment"])
	}

	// The printer list says the same thing as a count, which is what a picker needs in order not to
	// believe a printer with two lights is a printer with one.
	status, printers := api.do(t, http.MethodGet, "/printers", "")
	if status != http.StatusOK {
		t.Fatalf("GET /printers answered %d, want %d: %v", status, http.StatusOK, printers)
	}

	listed := printers["printers"].([]any)[0].(map[string]any)

	if count, _ := listed["bound_count"].(float64); count != 2 {
		t.Fatalf("the printer list counts %v bindings for the panelPrinter, want 2", listed["bound_count"])
	}

	bound, _ := listed["bound"].(map[string]any)
	if bound["id"] != panelPrinter || bound["light"] != "10.0.0.136" {
		t.Fatalf("the printer's first binding reads %v, want the panelPrinter on 10.0.0.136", bound)
	}
}

// TestAPrinterOnTwoSegmentsOfOneLightNeedsNoSecondName is the case the identity has to survive and
// the one that would have collided if a name were built from the printer alone: two zones of one
// panelPrinter showing one machine are two bindings, and each answers at its own path.
func TestAPrinterOnTwoSegmentsOfOneLightNeedsNoSecondName(t *testing.T) {
	light := newFakeStrip()
	light.state = wledStateForSegments(2, 5)

	fleet := []deviceapi.Device{{ID: panelPrinter, Name: "Hermes MK4", Connected: true}}

	api := newAPIUnderTest(t, light, fleet, bindings{
		{DeviceID: panelPrinter, Address: "http://10.0.0.136", Segment: 2},
		{DeviceID: panelPrinter, Address: "http://10.0.0.136", Segment: 5},
	})

	set := api.watch.set()
	if len(set) != 2 {
		t.Fatalf("the set holds %d bindings, want 2", len(set))
	}

	if set[0].ID != panelPrinter {
		t.Fatalf("the first binding is named %q, want the bare device id %q", set[0].ID, panelPrinter)
	}

	if want := panelPrinter + "@10.0.0.136:5"; set[1].ID != want {
		t.Fatalf("the second binding is named %q, want %q", set[1].ID, want)
	}

	for _, one := range set {
		if status, body := api.do(t, http.MethodGet, "/bindings/"+one.ID, ""); status != http.StatusOK {
			t.Fatalf("GET /bindings/%s answered %d, want %d: %v", one.ID, status, http.StatusOK, body)
		}
	}
}

// TestDeletingOneOfTwoBindingsLeavesTheOtherNamed is the stability the store exists for: a name is
// minted once and kept, so removing the panelPrinter does not hand its name to the strip and move the path
// a screen is pointing at. The remaining binding keeps the name it was given.
func TestDeletingOneOfTwoBindingsLeavesTheOtherNamed(t *testing.T) {
	light := newFakeStrip()

	fleet := []deviceapi.Device{{ID: panelPrinter, Name: "Hermes MK4", Connected: true}}

	api := newAPIUnderTest(t, light, fleet, bindings{
		{DeviceID: panelPrinter, Address: "http://10.0.0.136", Segment: wholeStrip, Brightness: 128},
		{DeviceID: panelPrinter, Address: "http://10.0.0.253", Segment: wholeStrip, Brightness: 96},
	})

	stripID := panelPrinter + "@10.0.0.253:whole"

	if status, body := api.do(t, http.MethodDelete, "/bindings/"+panelPrinter, ""); status != http.StatusOK {
		t.Fatalf("deleting the panelPrinter's binding answered %d, want %d: %v", status, http.StatusOK, body)
	}

	set := api.watch.set()
	if len(set) != 1 {
		t.Fatalf("the set holds %d bindings after the delete, want 1", len(set))
	}

	if set[0].ID != stripID {
		t.Fatalf("the remaining binding is named %q, want it to keep %q", set[0].ID, stripID)
	}

	// The bare device id answers nothing now: it named the binding that was deleted, and a name is
	// not a printer's to inherit.
	if status, _ := api.do(t, http.MethodGet, "/bindings/"+panelPrinter, ""); status != http.StatusNotFound {
		t.Fatalf("the deleted binding's path answered %d, want %d", status, http.StatusNotFound)
	}
}
