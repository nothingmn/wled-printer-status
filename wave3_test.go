// The gates wave 3 has to pass: a layout kept per binding, brightness windows edited by route, and a
// pause kept with an absolute instant — including the one thing about a pause that must not be got
// wrong, that a borrowed zone is handed back rather than powered off.
//
// The routes are exercised over the same watch-on-fakes the table tests use, with two differences
// that this wave needs: a clock the test can move, and a zone the windows are judged in. Both are
// fields of tableAPI, so nothing here reaches a host or a controller.
package main

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/prusa3d/prusa-connect/sdk/deviceapi"

	"github.com/nothingmn/wled-printer-status/internal/wled"
)

// operatorZone is the zone the windows are judged in for these tests: the operator's own, at UTC-7 in
// November, which is exactly the zone a window judged in the container's UTC clock would fire seven
// hours out.
//
//nolint:gochecknoglobals // one location, loaded once for these tests.
var operatorZone = mustZone("America/Denver")

// mustZone loads a zone or fails the test binary, because a test that silently fell back to UTC would
// be asserting the very behaviour the windows exist to avoid.
func mustZone(name string) *time.Location {
	location, err := time.LoadLocation(name)
	if err != nil {
		panic("the zone database has no " + name + ": " + err.Error())
	}

	return location
}

// wave3UnderTest is the table routes over the same store and watch, with a clock and a zone of the
// test's own.
type wave3UnderTest struct {
	*tablesUnderTest

	// now is the clock the routes read. A test moves it by writing through the pointer it hands in.
	now *time.Time
}

// newWave3UnderTest builds the routes with a fixed clock and a named zone.
//
// It builds on newTablesUnderTest rather than repeating the watch and the store: the harness is the
// same one, and only the two fields this wave added are different, so the mount is repeated here with
// those two set.
func newWave3UnderTest(t *testing.T, printers []deviceapi.Device, start bindings, document string, at time.Time, zone string) *wave3UnderTest {
	t.Helper()

	under := newTablesUnderTest(t, printers, start, document)

	clock := at
	now := &clock

	page := deviceapi.Page{Items: printers, Total: len(printers)}

	mux := http.NewServeMux()
	tableAPI{
		live:   under.watch,
		fleet:  func() devices { return fakeFleet{page: page} },
		tables: under.tables,
		bound:  time.Second,
		clock:  func() time.Time { return *now },
		zone:   func() string { return zone },
	}.mount(mux)

	under.mux = mux

	return &wave3UnderTest{tablesUnderTest: under, now: now}
}

// mixedBindings is the set the pause's exception is about: one printer on a light this module drives
// outright, and one on a zone borrowed from a strip somebody else also drives.
func mixedBindings() bindings {
	return bindings{
		{DeviceID: "printer-a", Address: "http://10.0.0.136", Segment: -1, Brightness: 128},
		{DeviceID: "printer-b", Address: "http://10.0.0.253", Segment: 3},
	}
}

// mixedPrinters are those two printers.
func mixedPrinters() []deviceapi.Device {
	return []deviceapi.Device{
		{ID: "printer-a", Name: "Panel printer"},
		{ID: "printer-b", Name: "Desk printer"},
	}
}

// at is the instant these tests are run at: an evening in the operator's own zone.
func at() time.Time {
	return time.Date(2026, 11, 19, 23, 0, 0, 0, operatorZone)
}

// ---------------------------------------------------------------------------------------------
// The layout
// ---------------------------------------------------------------------------------------------

// TestALayoutRoundTripsPerBindingAndAnUneditedBindingReportsTheDefault is the whole of item 6's
// contract in two halves: a binding somebody laid out reports what he wrote, with the origin saying
// so; a binding nobody touched reports the built-in default with an origin of `default` — never null,
// never an empty object.
func TestALayoutRoundTripsPerBindingAndAnUneditedBindingReportsTheDefault(t *testing.T) {
	under := newWave3UnderTest(t, twoPrinters(), twoBindings(), "", at(), "America/Denver")

	status, body := under.do(t, http.MethodPut, "/bindings/printer-a/layout",
		`{"bar": false, "bar_band": "top", "bar_rows": 20, "percent_scale": 4, "rotation": "take-turns", "rotate_seconds": 30}`)
	if status != http.StatusOK {
		t.Fatalf("a layout was refused with %d: %v", status, body)
	}

	written := readObject(t, body, "layout")

	for _, one := range []struct {
		name string
		want any
	}{
		{"bar", false},
		{"bar_band", "top"},
		{"bar_rows", float64(20)},
		{"percent_scale", float64(4)},
		{"rotation", "take-turns"},
		{"rotate_seconds", float64(30)},
	} {
		if got := written[one.name]; got != one.want {
			t.Fatalf("the layout answered %s as %v, want %v", one.name, got, one.want)
		}
	}

	// The fields that were written come from the store; the ones that were not come from the default,
	// which is the whole point of a per-field origin: there is nothing to reset on a default.
	for _, name := range []string{"bar_origin", "bar_band_origin", "bar_rows_origin", "percent_scale_origin", "rotate_seconds_origin"} {
		if got := readString(t, written, name); got != fromStore {
			t.Fatalf("%s reads %q after being written, want %q", name, got, fromStore)
		}
	}

	for _, name := range []string{"percent_origin", "name_origin"} {
		if got := readString(t, written, name); got != fromDefault {
			t.Fatalf("%s reads %q though nobody wrote it, want %q", name, got, fromDefault)
		}
	}

	if got := readString(t, written, "origin"); got != fromStore {
		t.Fatalf("the layout's own origin reads %q, want %q", got, fromStore)
	}

	// The other binding on the same light was never laid out, and it answers the default rather than
	// a zero: a bar at the bottom, twelve rows, the digits and the name taking turns every fifteen
	// seconds.
	status, body = under.do(t, http.MethodGet, "/bindings/printer-b/layout", "")
	if status != http.StatusOK {
		t.Fatalf("the unedited binding's layout answered %d: %v", status, body)
	}

	untouched := readObject(t, body, "layout")

	if got := readString(t, untouched, "origin"); got != fromDefault {
		t.Fatalf("an unedited binding's layout reports origin %q, want %q", got, fromDefault)
	}

	if got := untouched["overridden"]; got != false {
		t.Fatalf("an unedited binding's layout reports overridden %v, want false", got)
	}

	for _, one := range []struct {
		name string
		want any
	}{
		{"bar", true},
		{"bar_band", bandBottom},
		{"bar_rows", float64(defaultBarRows)},
		{"percent", true},
		{"percent_scale", float64(percentScaleMax)},
		{"name", true},
		{"rotation", rotationTakeTurns},
		{"rotate_seconds", float64(defaultRotateSeconds)},
		{"remaining", false},
	} {
		if got := untouched[one.name]; got != one.want {
			t.Fatalf("an unedited binding's %s reads %v, want %v", one.name, got, one.want)
		}
	}

	// Two bindings on one light differ, which is the reason the table is per binding at all.
	status, body = under.do(t, http.MethodGet, "/lights/10.0.0.253/layouts", "")
	if status != http.StatusOK {
		t.Fatalf("the light's layouts answered %d: %v", status, body)
	}

	layouts := readObjects(t, body, "layouts")
	if len(layouts) != 2 {
		t.Fatalf("the light answered %d layouts, want its two bindings", len(layouts))
	}

	if readNamed(t, layouts, "printer", "printer-a")["bar"] != false {
		t.Fatal("the layout written for printer-a came back with its bar on")
	}

	if readNamed(t, layouts, "printer", "printer-b")["bar"] != true {
		t.Fatal("the layout nobody wrote for printer-b did not come back as the default")
	}

	// What the document holds is only what was written: a default copied in would be a second
	// statement of the code's own table, drifting from it one release later.
	held := under.stored(t)
	if len(held.Layouts) != 1 {
		t.Fatalf("the store holds %d layout rows, want the one that was written", len(held.Layouts))
	}

	if held.Layouts[0].Percent != nil || held.Layouts[0].Name != nil {
		t.Fatalf("the stored row carries fields nobody wrote: %+v", held.Layouts[0])
	}
}

// TestAnInvalidRotationIsRefusedByName is the value §2.5 adds and the mockup's `needs build` label is
// about: two rotations exist, and a third is refused with both of them named.
func TestAnInvalidRotationIsRefusedByName(t *testing.T) {
	under := newWave3UnderTest(t, twoPrinters(), twoBindings(), "", at(), "America/Denver")

	for _, rotation := range []string{"round-robin", "takes-turns", "side_by_side", "both"} {
		status, body := under.do(t, http.MethodPut, "/bindings/printer-a/layout",
			`{"rotation": "`+rotation+`"}`)

		if status != http.StatusConflict {
			t.Fatalf("rotation %q answered %d, want 409: %v", rotation, status, body)
		}

		if got := code(t, body); got != LayoutRefused.Code().String() {
			t.Fatalf("the refusal for %q is %q, want %q", rotation, got, LayoutRefused.Code().String())
		}

		detail, _ := body["detail"].(string)

		if !strings.Contains(detail, rotation) {
			t.Fatalf("the refusal %q does not name the value %q that was sent", detail, rotation)
		}

		for _, allowed := range rotationChoices {
			if !strings.Contains(detail, allowed) {
				t.Fatalf("the refusal %q does not name the accepted value %q", detail, allowed)
			}
		}
	}

	// Nothing was written by any of the four.
	if held := under.stored(t); len(held.Layouts) != 0 {
		t.Fatalf("a refused rotation left %d rows in the store", len(held.Layouts))
	}
}

// TestASideBySideRotationIsStoredAndSaysWhatItDraws is the operator's decision built: the value is
// kept, reported, and drawn — the digits on the left and the name on the right, cut to the room that
// is left — and the answer says in one line what the picture will look like, because the digits give
// up a scale there.
func TestASideBySideRotationIsStoredAndSaysWhatItDraws(t *testing.T) {
	under := newWave3UnderTest(t, twoPrinters(), twoBindings(), "", at(), "America/Denver")

	status, body := under.do(t, http.MethodPut, "/bindings/printer-a/layout", `{"rotation": "side-by-side"}`)
	if status != http.StatusOK {
		t.Fatalf("side-by-side was refused with %d: %v", status, body)
	}

	layout := readObject(t, body, "layout")

	if got := readString(t, layout, "rotation"); got != rotationSideBySide {
		t.Fatalf("rotation reads %q, want %q", got, rotationSideBySide)
	}

	if note := readString(t, layout, "rotation_note"); !strings.Contains(note, "side by side, the digits sit on the left") {
		t.Fatalf("the answer does not say what side by side draws: %q", note)
	}

	if note := readString(t, body, "note"); !strings.Contains(note, "side by side, the digits sit on the left") {
		t.Fatalf("the write's note does not say what side by side draws: %q", note)
	}

	held := under.stored(t)
	if len(held.Layouts) != 1 || held.Layouts[0].Rotation == nil || *held.Layouts[0].Rotation != rotationSideBySide {
		t.Fatalf("the store does not hold the rotation that was written: %+v", held.Layouts)
	}

	// And the read says the same thing the write did, which is what a screen reloading the page sees.
	status, body = under.do(t, http.MethodGet, "/bindings/printer-a/layout", "")
	if status != http.StatusOK {
		t.Fatalf("reading the layout back answered %d: %v", status, body)
	}

	if note := readString(t, readObject(t, body, "layout"), "rotation_note"); !strings.Contains(note, "side by side, the digits sit on the left") {
		t.Fatalf("the read does not say what side by side draws: %q", note)
	}
}

// TestTheSevenOptionalLinesAreStoredPerBinding is the model the operator asked for: seven lines he
// can switch on one binding at a time, each reported with the origin of its value so a screen can
// say whether the answer is his or the default.
func TestTheSevenOptionalLinesAreStoredPerBinding(t *testing.T) {
	under := newWave3UnderTest(t, twoPrinters(), twoBindings(), "", at(), "America/Denver")

	body := `{"remaining": true, "temperatures": true, "material": true, "printer_name": true, "layer": true}`

	status, answer := under.do(t, http.MethodPut, "/bindings/printer-a/layout", body)
	if status != http.StatusOK {
		t.Fatalf("five of the lines were refused with %d: %v", status, answer)
	}

	said := readObject(t, answer, "layout")

	for _, key := range []string{"remaining", "temperatures", "material", "printer_name", "layer"} {
		if !readBool(t, said, key) {
			t.Fatalf("%s reads false in the answer, and it was written true", key)
		}

		if got := readString(t, said, key+"_origin"); got != fromStore {
			t.Fatalf("%s's origin reads %q, want %q", key, got, fromStore)
		}
	}

	for _, key := range []string{"ends", "elapsed"} {
		if readBool(t, said, key) {
			t.Fatalf("%s reads true and it was not written", key)
		}

		if got := readString(t, said, key+"_origin"); got != fromDefault {
			t.Fatalf("%s's origin reads %q, want %q", key, got, fromDefault)
		}
	}

	// The zone the ETA line would write a clock in travels beside them, because it is the one thing
	// a screen cannot compute: the setting's, not the layout's.
	if got := readString(t, said, "zone"); got != "America/Denver" {
		t.Fatalf("the answer's zone reads %q, want America/Denver", got)
	}

	// And it is on that binding only: the other one is untouched, which is what per binding means.
	held := under.stored(t)
	if len(held.Layouts) != 1 || held.Layouts[0].Material == nil || !*held.Layouts[0].Material {
		t.Fatalf("the store does not hold the lines that were written: %+v", held.Layouts)
	}
}

// TestTheEndsLineIsRefusedOnAModuleWithNoZone is the one refusal the optional lines still carry.
//
// The clock time is written in the zone this module's own store holds and in nothing else (AGENTS.md
// §5.14), so a layout that asks for it on a module with no zone would draw nothing every time —
// which is the failure the old `remaining: true` refusal was there to prevent, in a new place. The
// sentence names the screen that sets one, because the manifest has declared no settings since §4 and
// "the timezone setting" would send a person to a pane that does not exist.
func TestTheEndsLineIsRefusedOnAModuleWithNoZone(t *testing.T) {
	under := newWave3UnderTest(t, twoPrinters(), twoBindings(), "", at(), "")

	status, body := under.do(t, http.MethodPut, "/bindings/printer-a/layout", `{"ends": true}`)
	if status != http.StatusConflict {
		t.Fatalf("ends: true with no zone answered %d, want 409: %v", status, body)
	}

	detail, _ := body["detail"].(string)
	if !strings.Contains(detail, "ends is true") || !strings.Contains(detail, "Overview") ||
		!strings.Contains(detail, "America/Denver") {
		t.Fatalf("the refusal %q does not name where a zone is set or a value to set", detail)
	}

	// Every other line is still accepted, because only the clock needs a zone.
	status, body = under.do(t, http.MethodPut, "/bindings/printer-a/layout", `{"remaining": true}`)
	if status != http.StatusOK {
		t.Fatalf("remaining: true with no zone answered %d, want 200: %v", status, body)
	}
}

// TestALayoutIsDeletedBackToTheDefault is the reset: the row goes, the answer says whether there was
// one, and asking twice is not an error — a screen that could not tell "already at the default" from
// "no such binding" would offer the wrong button.
func TestALayoutIsDeletedBackToTheDefault(t *testing.T) {
	under := newWave3UnderTest(t, twoPrinters(), twoBindings(), "", at(), "America/Denver")

	if status, body := under.do(t, http.MethodPut, "/bindings/printer-a/layout", `{"name": false}`); status != http.StatusOK {
		t.Fatalf("writing the layout answered %d: %v", status, body)
	}

	status, body := under.do(t, http.MethodDelete, "/bindings/printer-a/layout", "")
	if status != http.StatusOK {
		t.Fatalf("deleting the layout answered %d: %v", status, body)
	}

	if got := body["removed"]; got != true {
		t.Fatalf("the delete says removed %v, want true", got)
	}

	if got := readString(t, readObject(t, body, "layout"), "origin"); got != fromDefault {
		t.Fatalf("the layout reads origin %q after the delete, want %q", got, fromDefault)
	}

	status, body = under.do(t, http.MethodDelete, "/bindings/printer-a/layout", "")
	if status != http.StatusOK {
		t.Fatalf("deleting an absent layout answered %d, want 200: %v", status, body)
	}

	if got := body["removed"]; got != false {
		t.Fatalf("the second delete says removed %v, want false", got)
	}

	if held := under.stored(t); len(held.Layouts) != 0 {
		t.Fatalf("the store holds %d layout rows after the delete", len(held.Layouts))
	}
}

// TestALayoutForABindingThisModuleDoesNotDriveIsRefused is the difference between an edit and a lie:
// a layout written for a printer that is not in the running set would be a row nobody could see.
func TestALayoutForABindingThisModuleDoesNotDriveIsRefused(t *testing.T) {
	under := newWave3UnderTest(t, twoPrinters(), twoBindings(), "", at(), "America/Denver")

	status, body := under.do(t, http.MethodPut, "/bindings/ghost/layout", `{"name": false}`)
	if status != http.StatusNotFound {
		t.Fatalf("a layout for an unknown binding answered %d, want 404: %v", status, body)
	}

	if got := code(t, body); got != BindingNotFound.Code().String() {
		t.Fatalf("the refusal is %q, want %q", got, BindingNotFound.Code().String())
	}
}

// ---------------------------------------------------------------------------------------------
// The brightness windows
// ---------------------------------------------------------------------------------------------

// TestAWindowIsJudgedAtItsBoundaryInstants pins the rule an hour of a display depends on: a window is
// half-open, so it is in force on the stroke of its start and not on the stroke of its end.
//
// The instants are built in the operator's own zone and the windows are judged there, which is the
// whole reason the zone is a setting: the same wall clock read in the container's UTC would answer
// differently for every one of these cases.
func TestAWindowIsJudgedAtItsBoundaryInstants(t *testing.T) {
	document := `{"version": 3, "bindings": [], "states": [], "moments": [], "layouts": [],
	  "windows": [{"from": "20:00", "to": "07:00", "brightness": 40}], "pauses": []}`

	cases := []struct {
		name string
		when time.Time
		want bool
	}{
		{"a minute before it opens", time.Date(2026, 11, 19, 19, 59, 0, 0, operatorZone), false},
		{"on the stroke of its start", time.Date(2026, 11, 19, 20, 0, 0, 0, operatorZone), true},
		{"inside the evening", time.Date(2026, 11, 19, 23, 0, 0, 0, operatorZone), true},
		{"one minute before midnight", time.Date(2026, 11, 19, 23, 59, 0, 0, operatorZone), true},
		{"midnight, which it wraps", time.Date(2026, 11, 20, 0, 0, 0, 0, operatorZone), true},
		{"one minute before it closes", time.Date(2026, 11, 20, 6, 59, 0, 0, operatorZone), true},
		{"on the stroke of its end", time.Date(2026, 11, 20, 7, 0, 0, 0, operatorZone), false},
		{"the middle of the day", time.Date(2026, 11, 20, 12, 0, 0, 0, operatorZone), false},
	}

	for _, one := range cases {
		t.Run(one.name, func(t *testing.T) {
			under := newWave3UnderTest(t, twoPrinters(), twoBindings(), document, one.when, "America/Denver")

			status, body := under.do(t, http.MethodGet, "/windows", "")
			if status != http.StatusOK {
				t.Fatalf("the window list answered %d: %v", status, body)
			}

			if got := readString(t, body, "judged_in"); got != "America/Denver" {
				t.Fatalf("the answer says the windows were judged in %q, want America/Denver", got)
			}

			windows := readObjects(t, body, "windows")
			if len(windows) != 1 {
				t.Fatalf("%d windows answered, want 1", len(windows))
			}

			if got := windows[0]["active"]; got != one.want {
				t.Fatalf("the window reads active %v at %s, want %v",
					got, one.when.In(operatorZone).Format(time.RFC3339), one.want)
			}

			_, won := body["wins"]

			if won != one.want {
				t.Fatalf("the answer names a winning window %v at %s, want %v",
					won, one.when.In(operatorZone).Format(time.RFC3339), one.want)
			}
		})
	}
}

// TestAWindowThatWrapsMidnightIsAcceptedAndSaysSo is the evening window almost everybody writes
// first: 20:00 to 07:00. Its end precedes its start, and that is a window rather than a mistake —
// schedule.go's own rule, kept here because refusing it would make the most common window
// unrepresentable. The answer says it understood, so that a screen can show it rather than mangling it.
func TestAWindowThatWrapsMidnightIsAcceptedAndSaysSo(t *testing.T) {
	under := newWave3UnderTest(t, twoPrinters(), twoBindings(), "", at(), "America/Denver")

	status, body := under.do(t, http.MethodPost, "/windows", `{"from": "20:00", "to": "07:00", "brightness": 40}`)
	if status != http.StatusOK {
		t.Fatalf("an evening window was refused with %d: %v", status, body)
	}

	windows := readObjects(t, body, "windows")
	if len(windows) != 1 {
		t.Fatalf("%d windows answered, want 1", len(windows))
	}

	if got := windows[0]["wraps_midnight"]; got != true {
		t.Fatalf("the window reports wraps_midnight %v, want true", got)
	}

	if got := readString(t, windows[0], "span"); got != "20:00-07:00" {
		t.Fatalf("the window's span reads %q, want 20:00-07:00", got)
	}

	if got := windows[0]["brightness"]; got != float64(40) {
		t.Fatalf("the window's brightness reads %v, want 40", got)
	}
}

// TestAWindowWhoseEndsAreTheSameIsRefusedWithAReason is the one window this module will not guess at:
// 20:00 to 20:00 is either a whole day or no time at all, and the refusal says so in those words.
func TestAWindowWhoseEndsAreTheSameIsRefusedWithAReason(t *testing.T) {
	under := newWave3UnderTest(t, twoPrinters(), twoBindings(), "", at(), "America/Denver")

	status, body := under.do(t, http.MethodPost, "/windows", `{"from": "20:00", "to": "20:00", "brightness": 40}`)
	if status != http.StatusConflict {
		t.Fatalf("a window with one end answered %d, want 409: %v", status, body)
	}

	if got := code(t, body); got != WindowRefused.Code().String() {
		t.Fatalf("the refusal is %q, want %q", got, WindowRefused.Code().String())
	}

	detail, _ := body["detail"].(string)
	for _, want := range []string{"no time at all", "20:00"} {
		if !strings.Contains(detail, want) {
			t.Fatalf("the refusal %q does not say %q", detail, want)
		}
	}

	if held := under.stored(t); len(held.Windows) != 0 {
		t.Fatalf("a refused window left %d in the store", len(held.Windows))
	}
}

// TestAWindowOutsideTheControllersRangeIsRefused is the brightness: 0-255 is what a WLED controller
// takes, and a value outside it is refused with the number rather than wrapped to another level.
func TestAWindowOutsideTheControllersRangeIsRefused(t *testing.T) {
	under := newWave3UnderTest(t, twoPrinters(), twoBindings(), "", at(), "America/Denver")

	for _, body := range []string{
		`{"from": "08:00", "to": "09:00", "brightness": 256}`,
		`{"from": "08:00", "to": "09:00", "brightness": -1}`,
		`{"from": "08:00", "to": "09:00"}`,
		`{"from": "25:00", "to": "09:00", "brightness": 40}`,
		`{"from": "08:00", "to": "09:00", "brightness": 40, "days": ["funday"]}`,
	} {
		status, answer := under.do(t, http.MethodPost, "/windows", body)

		if status != http.StatusConflict {
			t.Fatalf("%s answered %d, want 409: %v", body, status, answer)
		}

		if got := code(t, answer); got != WindowRefused.Code().String() {
			t.Fatalf("the refusal for %s is %q, want %q", body, got, WindowRefused.Code().String())
		}
	}
}

// TestAWindowCannotBeWrittenWithoutAZone is the rule that keeps a window from firing seven hours out:
// with no zone named there is nothing to judge a window against, and the refusal is the module's own
// timezone sentence rather than a quiet fallback to the container's clock.
func TestAWindowCannotBeWrittenWithoutAZone(t *testing.T) {
	under := newWave3UnderTest(t, twoPrinters(), twoBindings(), "", at(), "")

	status, body := under.do(t, http.MethodPost, "/windows", `{"from": "20:00", "to": "07:00", "brightness": 40}`)
	if status != http.StatusConflict {
		t.Fatalf("a window with no zone answered %d, want 409: %v", status, body)
	}

	if got := code(t, body); got != WindowRefused.Code().String() {
		t.Fatalf("the refusal is %q, want %q", got, WindowRefused.Code().String())
	}

	detail, _ := body["detail"].(string)
	for _, want := range []string{"Overview", "America/Denver"} {
		if !strings.Contains(detail, want) {
			t.Fatalf("the refusal %q does not say %q", detail, want)
		}
	}

	// A zone that is written and will not load is refused too, with the value in the sentence.
	under = newWave3UnderTest(t, twoPrinters(), twoBindings(), "", at(), "America/Denverr")

	status, body = under.do(t, http.MethodPost, "/windows", `{"from": "20:00", "to": "07:00", "brightness": 40}`)
	if status != http.StatusConflict {
		t.Fatalf("a window with an unloadable zone answered %d, want 409: %v", status, body)
	}

	if detail, _ := body["detail"].(string); !strings.Contains(detail, "America/Denverr") {
		t.Fatalf("the refusal %q does not name the zone that would not load", detail)
	}
}

// TestOverlappingWindowsAreAllowedAndTheFirstOneWins is schedule.go's rule kept at the route: overlap
// is not a defect the module invents a rule for, it is a list, and the first window that contains the
// moment decides.
func TestOverlappingWindowsAreAllowedAndTheFirstOneWins(t *testing.T) {
	document := `{"version": 3, "bindings": [], "states": [], "moments": [], "layouts": [],
	  "windows": [{"from": "20:00", "to": "08:00", "brightness": 40},
	              {"from": "22:00", "to": "23:30", "brightness": 200}], "pauses": []}`

	under := newWave3UnderTest(t, twoPrinters(), twoBindings(), document, at(), "America/Denver")

	status, body := under.do(t, http.MethodGet, "/windows", "")
	if status != http.StatusOK {
		t.Fatalf("the window list answered %d: %v", status, body)
	}

	if got := body["wins"]; got != float64(0) {
		t.Fatalf("the winning window is %v, want the first one", got)
	}

	if got := body["level"]; got != float64(40) {
		t.Fatalf("the brightness in force is %v, want the first window's 40", got)
	}

	windows := readObjects(t, body, "windows")

	// Both are in force at 23:00; exactly one wins.
	if windows[0]["active"] != true || windows[1]["active"] != true {
		t.Fatalf("both overlapping windows should be in force at 23:00: %v", windows)
	}

	if windows[0]["wins"] != true || windows[1]["wins"] != false {
		t.Fatalf("the first window should win and the second should not: %v", windows)
	}
}

// TestAWindowIsAddedReplacedAndRemoved is the CRUD, and what a removal does to the positions after it:
// the index is a position in a list, so the windows behind the one removed move up one.
func TestAWindowIsAddedReplacedAndRemoved(t *testing.T) {
	under := newWave3UnderTest(t, twoPrinters(), twoBindings(), "", at(), "America/Denver")

	status, body := under.do(t, http.MethodPost, "/windows", `{"from": "08:00", "to": "09:00", "brightness": 200}`)
	if status != http.StatusOK {
		t.Fatalf("adding a window answered %d: %v", status, body)
	}

	if got := body["added"]; got != float64(0) {
		t.Fatalf("the first window was added at %v, want 0", got)
	}

	if status, body = under.do(t, http.MethodPost, "/windows", `{"from": "20:00", "to": "07:00", "brightness": 40}`); status != http.StatusOK {
		t.Fatalf("adding a second window answered %d: %v", status, body)
	}

	if got := body["added"]; got != float64(1) {
		t.Fatalf("the second window was added at %v, want 1", got)
	}

	status, body = under.do(t, http.MethodPut, "/windows/0", `{"from": "08:00", "to": "10:00", "brightness": 180}`)
	if status != http.StatusOK {
		t.Fatalf("replacing a window answered %d: %v", status, body)
	}

	if got := body["replaced"]; got != float64(0) {
		t.Fatalf("the replaced window is %v, want 0", got)
	}

	if held := under.stored(t); len(held.Windows) != 2 || held.Windows[0].To != "10:00" || *held.Windows[0].Brightness != 180 {
		t.Fatalf("the store does not hold the replaced window: %+v", held.Windows)
	}

	status, body = under.do(t, http.MethodDelete, "/windows/0", "")
	if status != http.StatusOK {
		t.Fatalf("removing a window answered %d: %v", status, body)
	}

	remaining := readObjects(t, body, "windows")
	if len(remaining) != 1 || readString(t, remaining[0], "from") != "20:00" {
		t.Fatalf("after removing the first window the list reads %v, want the evening window at position 0", remaining)
	}

	// A position the list does not hold is refused by name rather than read as the nearest.
	status, body = under.do(t, http.MethodDelete, "/windows/4", "")
	if status != http.StatusNotFound {
		t.Fatalf("an absent position answered %d, want 404: %v", status, body)
	}

	if got := code(t, body); got != WindowNotFound.Code().String() {
		t.Fatalf("the refusal is %q, want %q", got, WindowNotFound.Code().String())
	}
}

// TestAWindowNamingADayOfTheWeekKeepsIt is the day-of-week question answered rather than assumed: no
// mockup asks for a weekday control, so nothing here requires one — and the field the setting already
// has travels through the store untouched rather than being dropped.
func TestAWindowNamingADayOfTheWeekKeepsIt(t *testing.T) {
	under := newWave3UnderTest(t, twoPrinters(), twoBindings(), "", at(), "America/Denver")

	status, body := under.do(t, http.MethodPost, "/windows",
		`{"from": "20:00", "to": "23:00", "brightness": 40, "days": ["fri", "sat"]}`)
	if status != http.StatusOK {
		t.Fatalf("a window naming days answered %d: %v", status, body)
	}

	windows := readObjects(t, body, "windows")
	if len(windows) != 1 {
		t.Fatalf("%d windows answered, want 1", len(windows))
	}

	days, ok := windows[0]["days"].([]any)
	if !ok || len(days) != 2 || days[0] != "fri" || days[1] != "sat" {
		t.Fatalf("the window's days read %v, want fri and sat", windows[0]["days"])
	}

	// 2026-11-19 is a Thursday in Denver, so a Friday-and-Saturday window is out of force at 23:00.
	if got := windows[0]["active"]; got != false {
		t.Fatalf("a Friday window reads active %v on a Thursday, want false", got)
	}
}

// ---------------------------------------------------------------------------------------------
// The pause
// ---------------------------------------------------------------------------------------------

// TestEachOfTheSixPauseLengthsComputesItsExpiryFromTheClock is the six the panic button offers, each
// turned into the absolute instant it ends at. The instants are written out rather than computed from
// the same table the code uses: a test that asked the code what it meant would agree with any answer.
func TestEachOfTheSixPauseLengthsComputesItsExpiryFromTheClock(t *testing.T) {
	// The instant is UTC on purpose, so that the arithmetic below is the plain one and a daylight
	// saving change cannot make this test right in summer and wrong in winter.
	now := time.Date(2026, 11, 19, 20, 0, 0, 0, time.UTC)

	cases := []struct {
		chosen string
		until  string
	}{
		{pause10m, "2026-11-19T20:10:00Z"},
		{pause1h, "2026-11-19T21:00:00Z"},
		{pause6h, "2026-11-20T02:00:00Z"},
		{pause1d, "2026-11-20T20:00:00Z"},
		{pause3d, "2026-11-22T20:00:00Z"},
		{durationForever, ""},
	}

	for _, one := range cases {
		t.Run(one.chosen, func(t *testing.T) {
			under := newWave3UnderTest(t, twoPrinters(), twoBindings(), "", now, "America/Denver")

			status, body := under.do(t, http.MethodPut, "/pause", `{"for": "`+one.chosen+`"}`)
			if status != http.StatusOK {
				t.Fatalf("a pause of %s answered %d: %v", one.chosen, status, body)
			}

			pause := readObject(t, body, "pause")

			if got := readString(t, pause, "chosen"); got != one.chosen {
				t.Fatalf("the pause was written as %q, want %q", got, one.chosen)
			}

			if got := readString(t, pause, "until"); got != one.until {
				t.Fatalf("a pause of %s ends at %q, want %q", one.chosen, got, one.until)
			}

			if got := pause["forever"]; got != (one.until == "") {
				t.Fatalf("a pause of %s reports forever %v", one.chosen, got)
			}

			// What the document holds is an instant, not a duration: the one field that makes a
			// timed pause survive a restart.
			raw := storedRaw(t, under.dir)
			pauses, _ := raw["pauses"].([]any)

			if len(pauses) != 1 {
				t.Fatalf("the store holds %v pauses, want 1", raw["pauses"])
			}

			row, _ := pauses[0].(map[string]any)

			if one.until == "" {
				if _, present := row["expires"]; present {
					t.Fatalf("a pause of forever carries an expiry: %v", row)
				}

				return
			}

			if got, _ := row["expires"].(string); got != one.until {
				t.Fatalf("the stored expiry reads %q, want the instant %q", got, one.until)
			}

			for _, forbidden := range []string{"duration", "for", "seconds", "timer"} {
				if _, present := row[forbidden]; present {
					t.Fatalf("the stored pause carries %q, and a pause is kept as an instant rather than a length: %v",
						forbidden, row)
				}
			}
		})
	}
}

// TestALengthThatIsNotOneOfTheSixIsRefusedByName is the panic button's vocabulary, refused by name
// rather than rounded: a pause of about an hour is not the thing anybody asked for.
func TestALengthThatIsNotOneOfTheSixIsRefusedByName(t *testing.T) {
	under := newWave3UnderTest(t, twoPrinters(), twoBindings(), "", at(), "America/Denver")

	for _, body := range []string{
		`{"for": "30m"}`,
		`{"for": "2h"}`,
		`{"for": "1w"}`,
		`{"for": "1d4h"}`,
		`{"for": "0m"}`,
	} {
		status, answer := under.do(t, http.MethodPut, "/pause", body)

		if status != http.StatusConflict {
			t.Fatalf("%s answered %d, want 409: %v", body, status, answer)
		}

		if got := code(t, answer); got != PauseRefused.Code().String() {
			t.Fatalf("the refusal for %s is %q, want %q", body, got, PauseRefused.Code().String())
		}

		detail, _ := answer["detail"].(string)

		for _, allowed := range pauseChoices {
			if !strings.Contains(detail, allowed) {
				t.Fatalf("the refusal %q does not name the accepted value %q", detail, allowed)
			}
		}
	}

	// A body that is not a pause at all is the other code, because the two are different fixes: a
	// misspelt key, and a body that names no length at all. A length is not optional, so leaving it
	// out is a body that does not say what is wanted rather than a length this module will not take.
	for _, body := range []string{`{"durration": "10m"}`, `{"for": ""}`, `{}`} {
		status, answer := under.do(t, http.MethodPut, "/pause", body)

		if status != http.StatusBadRequest {
			t.Fatalf("%s answered %d, want 400: %v", body, status, answer)
		}

		if got := code(t, answer); got != PauseBodyInvalid.Code().String() {
			t.Fatalf("the refusal for %s is %q, want %q", body, got, PauseBodyInvalid.Code().String())
		}
	}
}

// TestAnExpiredPauseReadsAsNotPausedAndNothingHadToRun is the whole reason the store holds an instant:
// the pause ends because the clock passed it, not because something fired. The row is still in the
// document — a read is not a write — and it says it has expired, which is a different answer from
// "nobody set one".
func TestAnExpiredPauseReadsAsNotPausedAndNothingHadToRun(t *testing.T) {
	start := time.Date(2026, 11, 19, 20, 0, 0, 0, time.UTC)

	under := newWave3UnderTest(t, twoPrinters(), twoBindings(), "", start, "America/Denver")

	if status, body := under.do(t, http.MethodPut, "/pause", `{"for": "10m"}`); status != http.StatusOK {
		t.Fatalf("the pause answered %d: %v", status, body)
	}

	// One minute inside the pause.
	*under.now = start.Add(9 * time.Minute)

	_, body := under.do(t, http.MethodGet, "/pause", "")
	if got := body["paused"]; got != true {
		t.Fatalf("nine minutes into a ten-minute pause the module reads paused %v, want true", got)
	}

	// One minute past it. Nothing but the clock has moved.
	*under.now = start.Add(11 * time.Minute)

	_, body = under.do(t, http.MethodGet, "/pause", "")

	if got := body["paused"]; got != false {
		t.Fatalf("past the expiry the module reads paused %v, want false", got)
	}

	pauses := readObjects(t, body, "pauses")
	if len(pauses) != 1 {
		t.Fatalf("%d pauses answered, want the expired one still recorded", len(pauses))
	}

	if got := pauses[0]["on"]; got != false {
		t.Fatalf("the expired pause reads on %v, want false", got)
	}

	if got := pauses[0]["expired"]; got != true {
		t.Fatalf("the expired pause reads expired %v, want true", got)
	}

	// The row is still there, because a read must not rewrite the store.
	if held := under.stored(t); len(held.Pauses) != 1 {
		t.Fatalf("the store holds %d pauses after the expiry, want the row that says when it ended", len(held.Pauses))
	}
}

// TestAPauseOnABorrowedZoneHandsItBackAndAWholeLightIsSwitchedOff is the exception, and the one thing
// in this wave that must not be got wrong. The global pause reaches both bindings; the one on a whole
// light goes off, and the one on a borrowed zone is handed back rather than powered off — because the
// rest of that strip belongs to somebody else.
//
// The borrow is genuine here rather than assumed: the desk strip is given a segment 3, the watch is
// rebuilt over it exactly as a settings save rebuilds it, and the writer captures the zone. The test
// then proves three things in order — that the pause decided "hands-back" for that binding, that the
// pause itself sent nothing to either controller, and that the zone the writer is holding is given
// back untouched when the watch stops, which is the give-back the exception relies on.
func TestAPauseOnABorrowedZoneHandsItBackAndAWholeLightIsSwitchedOff(t *testing.T) {
	under := newWave3UnderTest(t, mixedPrinters(), mixedBindings(), "", at(), "America/Denver")

	// The desk strip's segment 3 as its owner left it. A borrow captures exactly this. The write is
	// taken under the fake's own lock because the watch already running on it has a writer that reads
	// the state on its own goroutine: setting the field bare is a data race, and the race detector
	// says so.
	under.light.mu.Lock()
	under.light.state = wled.State{On: true, Segments: []wled.Segment{
		{ID: 3, Start: 0, Stop: 8, On: true, Name: "Desk zone"},
	}}
	under.light.mu.Unlock()

	if err := under.watch.apply(context.Background(), mixedBindings()); err != nil {
		t.Fatalf("the watch did not restart over the desk strip: %v", err)
	}

	painted := len(under.light.shown())

	status, body := under.do(t, http.MethodPut, "/pause", `{"for": "1h"}`)
	if status != http.StatusOK {
		t.Fatalf("the global pause answered %d: %v", status, body)
	}

	for _, want := range []string{"handed back", "rather than switched off"} {
		if note := readString(t, body, "note"); !strings.Contains(note, want) {
			t.Fatalf("the write's note does not say %q: %q", want, note)
		}
	}

	status, body = under.do(t, http.MethodGet, "/pause", "")
	if status != http.StatusOK {
		t.Fatalf("the pause read answered %d: %v", status, body)
	}

	if got := body["paused"]; got != true {
		t.Fatalf("the module reads paused %v after a global pause, want true", got)
	}

	if got := readString(t, body, "why"); got != pauseGlobal {
		t.Fatalf("the pause reads why %q, want %q", got, pauseGlobal)
	}

	bindings := readObjects(t, body, "bindings")
	if len(bindings) != 2 {
		t.Fatalf("%d bindings answered, want the two this module drives", len(bindings))
	}

	whole := readNamed(t, bindings, "printer", "printer-a")
	borrowed := readNamed(t, bindings, "printer", "printer-b")

	if got := readString(t, whole, "effect"); got != effectOff {
		t.Fatalf("the whole-light binding's effect reads %q, want %q", got, effectOff)
	}

	if got := readString(t, borrowed, "effect"); got != effectHandsBack {
		t.Fatalf("the borrowed binding's effect reads %q, want %q — a paused borrowed zone is handed back, never powered off",
			got, effectHandsBack)
	}

	if got := borrowed["borrowed"]; got != true {
		t.Fatalf("the borrowed binding reads borrowed %v, want true", got)
	}

	if got := readString(t, borrowed, "due_to"); got != pauseGlobal {
		t.Fatalf("the borrowed binding's pause reads due_to %q, want %q", got, pauseGlobal)
	}

	// The pause itself touched neither controller: this wave stores and reports, and nothing was
	// switched off. That is the assertion behind "it does not switch the light off yet".
	if got := len(under.light.shown()); got != painted {
		t.Fatalf("the pause sent %d frames to the controller, and it must send none", got-painted)
	}

	if back := under.light.givenBack(); len(back) != 0 {
		t.Fatalf("the pause gave %d segments back by itself: the wiring wave is what acts on a pause", len(back))
	}

	// And the borrow really was in flight: stopping the watch gives the zone back exactly as the
	// controller was holding it, which is the give-back the pause's exception is built on.
	under.watch.stop()

	back := under.light.givenBack()
	if len(back) != 1 {
		t.Fatalf("the watch gave back %d segments, want the one zone it borrowed", len(back))
	}

	if back[0].ID != 3 || back[0].Name != "Desk zone" || back[0].Stop != 8 {
		t.Fatalf("the zone given back reads %+v, want segment 3 \"Desk zone\" of 0-8 as it was found", back[0])
	}
}

// TestThePauseDecisionIsTheBindingsOwnSegment is the function the wiring wave will read, held to the
// one rule: a segment is a zone somebody else also drives, and -1 is the whole light.
func TestThePauseDecisionIsTheBindingsOwnSegment(t *testing.T) {
	cases := []struct {
		segment  int
		borrowed bool
		effect   string
	}{
		{-1, false, effectOff},
		{0, true, effectHandsBack},
		{3, true, effectHandsBack},
	}

	for _, one := range cases {
		b := binding{DeviceID: "printer", Address: "http://10.0.0.253", Segment: one.segment}

		if got := borrowedZone(b); got != one.borrowed {
			t.Fatalf("segment %d reads borrowed %v, want %v", one.segment, got, one.borrowed)
		}

		if got := pauseEffectOn(b); got != one.effect {
			t.Fatalf("segment %d reads effect %q, want %q", one.segment, got, one.effect)
		}
	}
}

// TestAPauseOnOneBindingLeavesTheOtherAlone is the second scope: the row's own pause, which reaches one
// binding and nothing else.
func TestAPauseOnOneBindingLeavesTheOtherAlone(t *testing.T) {
	under := newWave3UnderTest(t, mixedPrinters(), mixedBindings(), "", at(), "America/Denver")

	status, body := under.do(t, http.MethodPut, "/bindings/printer-b/pause", `{"for": "6h"}`)
	if status != http.StatusOK {
		t.Fatalf("the binding's pause answered %d: %v", status, body)
	}

	if got := readString(t, body, "effect"); got != effectHandsBack {
		t.Fatalf("the write answers effect %q, want %q", got, effectHandsBack)
	}

	status, body = under.do(t, http.MethodGet, "/pause", "")
	if status != http.StatusOK {
		t.Fatalf("the pause read answered %d: %v", status, body)
	}

	if got := readString(t, body, "why"); got != pauseOnBinding {
		t.Fatalf("the pause reads why %q, want %q", got, pauseOnBinding)
	}

	pausedBy, _ := body["paused_by"].([]any)
	if len(pausedBy) != 1 || pausedBy[0] != "printer-b" {
		t.Fatalf("the pause names %v as paused, want printer-b alone", body["paused_by"])
	}

	bindings := readObjects(t, body, "bindings")

	if got := readNamed(t, bindings, "printer", "printer-a")["paused"]; got != false {
		t.Fatalf("printer-a reads paused %v under a pause on printer-b, want false", got)
	}

	if got := readNamed(t, bindings, "printer", "printer-b")["paused"]; got != true {
		t.Fatalf("printer-b reads paused %v under its own pause, want true", got)
	}
}

// TestAPauseSurvivesAReconstructionOfTheProcess is the property the store exists for: the process is
// rebuilt on every settings save and every restart, so a second process reading the same document must
// see the same pause with the same instant still ahead of it.
func TestAPauseSurvivesAReconstructionOfTheProcess(t *testing.T) {
	start := time.Date(2026, 11, 19, 20, 0, 0, 0, time.UTC)

	first := newWave3UnderTest(t, mixedPrinters(), mixedBindings(), "", start, "America/Denver")

	if status, body := first.do(t, http.MethodPut, "/pause", `{"for": "3d"}`); status != http.StatusOK {
		t.Fatalf("the pause answered %d: %v", status, body)
	}

	// A second process: a fresh harness over the same store, three hours later.
	dir := first.dir

	held, err := loadStore(dir)
	if err != nil {
		t.Fatalf("the store a pause was written into could not be read again: %v", err)
	}

	if len(held.Config.Pauses) != 1 {
		t.Fatalf("the document holds %d pauses, want the one that was written", len(held.Config.Pauses))
	}

	if held.Config.Pauses[0].Expires == nil {
		t.Fatal("the pause was written with no expiry, so nothing would end it")
	}

	if want := start.Add(72 * time.Hour); !held.Config.Pauses[0].Expires.Equal(want) {
		t.Fatalf("the stored expiry is %s, want %s", held.Config.Pauses[0].Expires, want)
	}

	// Read again at three hours in, through the routes: still paused, with the instant it ends at.
	*first.now = start.Add(3 * time.Hour)

	status, body := first.do(t, http.MethodGet, "/pause", "")
	if status != http.StatusOK {
		t.Fatalf("the pause read answered %d: %v", status, body)
	}

	if got := body["paused"]; got != true {
		t.Fatalf("three hours into a three-day pause the module reads paused %v, want true", got)
	}

	if got := readString(t, body, "until"); got != "2026-11-22T20:00:00Z" {
		t.Fatalf("the pause ends at %q, want 2026-11-22T20:00:00Z", got)
	}
}

// TestAResumeTakesThePauseOutOfTheStore is the other half of the panic button: the row goes, and asking
// for a resume that was never set is not an error — it says so, because "already running" and "no such
// binding" are different answers.
func TestAResumeTakesThePauseOutOfTheStore(t *testing.T) {
	under := newWave3UnderTest(t, twoPrinters(), twoBindings(), "", at(), "America/Denver")

	if status, body := under.do(t, http.MethodPut, "/pause", `{"for": "1d"}`); status != http.StatusOK {
		t.Fatalf("the pause answered %d: %v", status, body)
	}

	status, body := under.do(t, http.MethodDelete, "/pause", "")
	if status != http.StatusOK {
		t.Fatalf("the resume answered %d: %v", status, body)
	}

	if got := body["resumed"]; got != true {
		t.Fatalf("the resume says resumed %v, want true", got)
	}

	_, body = under.do(t, http.MethodGet, "/pause", "")
	if got := body["paused"]; got != false {
		t.Fatalf("the module reads paused %v after a resume, want false", got)
	}

	if held := under.stored(t); len(held.Pauses) != 0 {
		t.Fatalf("the store holds %d pauses after a resume", len(held.Pauses))
	}

	// The second resume is answered, not refused.
	status, body = under.do(t, http.MethodDelete, "/pause", "")
	if status != http.StatusOK {
		t.Fatalf("a second resume answered %d, want 200: %v", status, body)
	}

	if got := body["resumed"]; got != false {
		t.Fatalf("a second resume says resumed %v, want false", got)
	}
}

// TestAPauseOnABindingThisModuleDoesNotDriveIsRefused is the same rule the layout routes hold: a pause
// for a printer that is not in the running set would be a row nobody could read.
func TestAPauseOnABindingThisModuleDoesNotDriveIsRefused(t *testing.T) {
	under := newWave3UnderTest(t, twoPrinters(), twoBindings(), "", at(), "America/Denver")

	for _, call := range []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodPut, "/bindings/ghost/pause", `{"for": "1h"}`},
		{http.MethodDelete, "/bindings/ghost/pause", ""},
	} {
		status, body := under.do(t, call.method, call.path, call.body)
		if status != http.StatusNotFound {
			t.Fatalf("%s %s answered %d, want 404: %v", call.method, call.path, status, body)
		}

		if got := code(t, body); got != BindingNotFound.Code().String() {
			t.Fatalf("the refusal is %q, want %q", got, BindingNotFound.Code().String())
		}
	}
}

// TestEveryWriteInThisWaveSaysWhatItDoes is the note discipline wave 2 set: every write says what it
// does and what it is worth, because somebody standing in front of a light needs to know whether the
// change he just saved is a change he will see. Three of these five now change the light, and the
// layout's does not; the notes are where that difference lives.
func TestEveryWriteInThisWaveSaysWhatItDoes(t *testing.T) {
	under := newWave3UnderTest(t, twoPrinters(), twoBindings(), "", at(), "America/Denver")

	cases := []struct {
		method string
		path   string
		body   string
		want   string
		kept   string
	}{
		{http.MethodPut, "/bindings/printer-a/layout", `{"name": false}`,
			"is what the light draws", "survives a restart and an upgrade"},
		{http.MethodPut, "/pause", `{"for": "10m"}`,
			"switched off now and stays off", "survives a restart and a settings save"},
		{http.MethodPut, "/bindings/printer-a/pause", `{"for": "10m"}`,
			"switched off now and stays off", "survives a restart and a settings save"},
		{http.MethodPost, "/windows", `{"from": "08:00", "to": "09:00", "brightness": 100}`,
			"what the light's brightness follows", "survives a restart and an upgrade"},
		{http.MethodDelete, "/pause", "",
			"painted again at once", "nothing will pause this on the next start either"},
	}

	for _, one := range cases {
		status, body := under.do(t, one.method, one.path, one.body)
		if status != http.StatusOK {
			t.Fatalf("%s %s answered %d: %v", one.method, one.path, status, body)
		}

		note := readString(t, body, "note")

		if !strings.Contains(note, one.want) {
			t.Fatalf("%s %s does not say %q in its note: %q", one.method, one.path, one.want, note)
		}

		if !strings.Contains(note, one.kept) {
			t.Fatalf("%s %s does not say what it is worth (%q): %q", one.method, one.path, one.kept, note)
		}
	}
}
