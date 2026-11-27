// The two tables the Colours and Events screens edit: the state table and the moment table, as
// their routes answer and as the store keeps them.
//
// Every test here works on a directory of its own and a watch built on fakes. Nothing touches a
// device, a network, or the store the module is actually running on.
package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/prusa3d/prusa-connect/sdk/deviceapi"

	"github.com/nothingmn/wled-printer-status/internal/printer"
	"github.com/nothingmn/wled-printer-status/internal/render"
	"github.com/nothingmn/wled-printer-status/internal/wled"
)

// tablesUnderTest is the two tables' routes over a watch built on fakes, a printer list that needs
// no host, and a store in a directory of its own.
type tablesUnderTest struct {
	mux    http.Handler
	watch  *runner
	light  *fakeStrip
	dir    string
	tables *configTables
}

// newTablesUnderTest builds the routes and the store beside them. document is what the store holds
// before the first request, and an empty string means a store this build wrote with no rows in it —
// which is the state every install is in before anybody edits a table.
func newTablesUnderTest(t *testing.T, printers []deviceapi.Device, start bindings, document string) *tablesUnderTest {
	t.Helper()

	ctx, stop := context.WithCancel(context.Background())
	t.Cleanup(stop)

	light := newFakeStrip()

	watch := &runner{
		newSource: func(b binding) (watchSource, error) {
			return &fixedSource{snapshot: printingJob(b.DeviceID, 50)}, nil
		},
		newLight: func(string) (strip, error) { return light, nil },
		logf:     quiet,
		// An hour, because nothing on these routes depends on a frame: the tables are a record a
		// screen edits, and a writer painting every five milliseconds would only add noise.
		tick:  time.Hour,
		grace: 2 * time.Second,
	}

	if err := watch.apply(ctx, start); err != nil {
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

	if document != "" {
		if err := os.WriteFile(storePath(dir), []byte(document), 0o600); err != nil {
			t.Fatalf("write the fixture store: %v", err)
		}
	}

	tables := &configTables{}
	tables.grant(dir)

	page := deviceapi.Page{Items: printers, Total: len(printers)}

	// No kept host answer survives from another fixture: see forgetSeen.
	forgetSeen()

	mux := http.NewServeMux()
	tableAPI{
		live:   watch,
		fleet:  func() devices { return fakeFleet{page: page} },
		tables: tables,
		bound:  time.Second,
	}.mount(mux)

	return &tablesUnderTest{mux: mux, watch: watch, light: light, dir: dir, tables: tables}
}

// do runs one request and answers with the status and the body as a map.
func (a *tablesUnderTest) do(t *testing.T, method, path, body string) (int, map[string]any) {
	t.Helper()

	recorder := httptest.NewRecorder()
	a.mux.ServeHTTP(recorder, forwarded(method, path, strings.NewReader(body)))

	var answer map[string]any
	if raw := recorder.Body.Bytes(); len(raw) > 0 {
		if err := json.Unmarshal(raw, &answer); err != nil {
			t.Fatalf("%s %s answered something that is not JSON: %v (%s)", method, path, err, raw)
		}
	}

	return recorder.Code, answer
}

// stored is the document as it sits on disk, for the assertions about what a write leaves behind
// rather than about what its answer said.
func (a *tablesUnderTest) stored(t *testing.T) storedConfig {
	t.Helper()

	found, err := loadStore(a.dir)
	if err != nil {
		t.Fatalf("read the store back: %v", err)
	}

	return found.Config
}

// object is one member of an answer as an object.
func readObject(t *testing.T, held map[string]any, name string) map[string]any {
	t.Helper()

	found, ok := held[name].(map[string]any)
	if !ok {
		t.Fatalf("the answer's %q is %v, and it is not an object", name, held[name])
	}

	return found
}

// objects is one member of an answer as a list of objects.
func readObjects(t *testing.T, held map[string]any, name string) []map[string]any {
	t.Helper()

	list, ok := held[name].([]any)
	if !ok {
		t.Fatalf("the answer's %q is %v, and it is not a list", name, held[name])
	}

	out := make([]map[string]any, 0, len(list))

	for _, one := range list {
		entry, ok := one.(map[string]any)
		if !ok {
			t.Fatalf("the answer's %q holds %v, and it is not an object", name, one)
		}

		out = append(out, entry)
	}

	return out
}

// text is one string member of an object.
func readString(t *testing.T, held map[string]any, name string) string {
	t.Helper()

	found, _ := held[name].(string)

	return found
}

// flag is one boolean member of an object.
func readBool(t *testing.T, held map[string]any, name string) bool {
	t.Helper()

	found, _ := held[name].(bool)

	return found
}

// number is one integer member of an object.
func readInt(t *testing.T, held map[string]any, name string) int {
	t.Helper()

	found, _ := held[name].(float64)

	return int(found)
}

// named is the entry of a list whose name member is want.
func readNamed(t *testing.T, list []map[string]any, name, want string) map[string]any {
	t.Helper()

	for _, one := range list {
		if readString(t, one, name) == want {
			return one
		}
	}

	t.Fatalf("the answer holds no %q of %q", name, want)

	return nil
}

// twoPrinters is one light carrying two printers on two segments, which is the shape the Events
// screen draws as sub-rows and the shape the moment key exists for.
func twoPrinters() []deviceapi.Device {
	return []deviceapi.Device{
		{ID: "printer-a", Name: "Hermes MK4"},
		{ID: "printer-b", Name: "Mini"},
	}
}

// twoBindings is those two printers on one controller, one segment each.
func twoBindings() bindings {
	return bindings{
		{DeviceID: "printer-a", Address: "http://10.0.0.253", Segment: 3},
		{DeviceID: "printer-b", Address: "http://10.0.0.253", Segment: 4},
	}
}

// TestTheStateTableIsTheRenderersOwnTable is the rule docs/product-requirements.md Appendix A
// states: the table and internal/render are the same statement. The ten host states are enumerated
// here by name, and each one's colour is asked of the renderer rather than compared with a number
// typed twice.
func TestTheStateTableIsTheRenderersOwnTable(t *testing.T) {
	want := []string{
		"IDLE", "BUSY", "READY", "PRINTING", "PAUSED",
		"FINISHED", "STOPPED", "ATTENTION", "ERROR", "UNKNOWN",
	}

	if got := stateNamesOf(stateFromHost); !reflect.DeepEqual(got, want) {
		t.Fatalf("the table holds the host states %v, want the ten Appendix A names %v", got, want)
	}

	if got := stateNamesOf(stateCondition); !reflect.DeepEqual(got, []string{disconnectedState, staleState}) {
		t.Fatalf("the table holds the conditions %v, want %s and %s", got, disconnectedState, staleState)
	}

	if len(stateTable) != len(want)+2 {
		t.Fatalf("the table holds %d rows, want the ten states and the two conditions", len(stateTable))
	}

	for _, row := range stateTable {
		if row.kind != stateFromHost {
			continue
		}

		colour, known := render.ColourOf(row.name)
		if !known {
			t.Fatalf("internal/render has no reading of %s, and the table gives it a colour", row.name)
		}

		if colour != row.colour {
			t.Fatalf("the table draws %s in %v and internal/render draws it in %v",
				row.name, row.colour, colour)
		}
	}

	// The two conditions are computed rather than reported, so they come from the renderer's own
	// condition colours and not from a state word.
	wanted := map[string]wled.RGB{
		disconnectedState: render.ColourDisconnected,
		staleState:        render.ColourStale,
	}

	for _, row := range stateTable {
		want, isCondition := wanted[row.name]
		if !isCondition {
			continue
		}

		if row.colour != want {
			t.Fatalf("%s is drawn in %v and the renderer's condition colour is %v", row.name, row.colour, want)
		}
	}

	// A name is matched in any case, because it travels in a URL, and nothing else is.
	if _, known := findState("printing"); !known {
		t.Fatal("findState(\"printing\") does not know the state, and a name travels in a URL here")
	}

	if _, known := findState("PRINTNG"); known {
		t.Fatal("findState knows a name the table does not hold")
	}

	// Four holds, and forever is one of them rather than the absence of one.
	if !reflect.DeepEqual(holdsChoices, []string{"forever", "30s", "60s", "5m"}) {
		t.Fatalf("the holds are %v, want forever, 30s, 60s and 5m", holdsChoices)
	}
}

// TestTheStateTableAnswersEveryStateWithItsDefaultAndSaysSo is the read a Colours screen is drawn
// from: every state, the value in force, and — because the screen cannot show what it cannot
// distinguish — where each value came from.
func TestTheStateTableAnswersEveryStateWithItsDefaultAndSaysSo(t *testing.T) {
	under := newTablesUnderTest(t, twoPrinters(), twoBindings(), "")

	status, body := under.do(t, http.MethodGet, "/states", "")
	if status != http.StatusOK {
		t.Fatalf("GET /states answered %d: %v", status, body)
	}

	if got := readInt(t, body, "count"); got != 12 {
		t.Fatalf("the table answered %d rows, want the ten states and the two conditions", got)
	}

	if got := readInt(t, body, "conditions"); got != 2 {
		t.Fatalf("the table answered %d conditions, want 2", got)
	}

	if !readBool(t, body, "persisted") {
		t.Fatal("the table says a change would not be kept, and it read a store in a granted directory")
	}

	rows := readObjects(t, body, "states")
	if len(rows) != 12 {
		t.Fatalf("the answer carries %d rows, want 12", len(rows))
	}

	paused := readNamed(t, rows, "state", "PAUSED")

	if got := readString(t, paused, "kind"); got != string(stateFromHost) {
		t.Fatalf("PAUSED is a %q, want a state", got)
	}

	colour := readObject(t, paused, "colour")
	if readInt(t, colour, "r") != int(render.ColourPaused.R) ||
		readInt(t, colour, "g") != int(render.ColourPaused.G) ||
		readInt(t, colour, "b") != int(render.ColourPaused.B) {
		t.Fatalf("PAUSED answered the colour %v, want the renderer's %v", colour, render.ColourPaused)
	}

	// Every value of an unedited row is the default, and it says so: this is the answer that makes
	// "reset to the default" a button a screen can offer.
	for _, row := range rows {
		for _, name := range []string{"colour_origin", "word_origin", "holds_origin"} {
			if got := readString(t, row, name); got != fromDefault {
				t.Fatalf("%s's %s is %q, want %q", readString(t, row, "state"), name, got, fromDefault)
			}
		}

		if readBool(t, row, "overridden") {
			t.Fatalf("%s says it carries an override and nothing has been written", readString(t, row, "state"))
		}
	}

	// A state's word is off by default and its hold is forever, which is the screen's own default
	// and not the absence of an answer.
	if got := readString(t, paused, "word"); got != "" {
		t.Fatalf("PAUSED answered the word %q, want no word by default", got)
	}

	if got := readString(t, paused, "holds"); got != durationForever {
		t.Fatalf("PAUSED answered a hold of %q, want forever", got)
	}

	// The two conditions are rows, and a caller can tell them from a state the host reports.
	if got := readString(t, readNamed(t, rows, "state", disconnectedState), "kind"); got != string(stateCondition) {
		t.Fatalf("%s is a %q, want a condition", disconnectedState, got)
	}
}

// TestAStateOverrideIsKeptAndCanBeDeletedBackToItsDefault is the state table's CRUD, end to end: a
// write that reaches the document, a read that carries it with its origin, a delete that puts the
// row back, and — the failure this shape exists to prevent — the bindings that were in the document
// before the write still there after it.
func TestAStateOverrideIsKeptAndCanBeDeletedBackToItsDefault(t *testing.T) {
	document := `{
	  "version": 2,
	  "bindings": [{"printer": "printer-a", "controller": "http://10.0.0.253", "segment": 3}],
	  "states": [],
	  "moments": []
	}`

	under := newTablesUnderTest(t, twoPrinters(), twoBindings(), document)

	status, body := under.do(t, http.MethodPut, "/states/PAUSED",
		`{"colour": {"r": 12, "g": 34, "b": 56}, "word": " PAUSE ", "holds": "60s"}`)
	if status != http.StatusOK {
		t.Fatalf("PUT /states/PAUSED answered %d: %v", status, body)
	}

	written := readObject(t, body, "state")

	if !readBool(t, written, "overridden") {
		t.Fatal("the write answered that the row carries no override")
	}

	if got := readString(t, written, "word"); got != "PAUSE" {
		t.Fatalf("the word reads %q, want PAUSE with its surrounding spaces taken off", got)
	}

	if got := readString(t, written, "word_origin"); got != fromStore {
		t.Fatalf("the word's origin is %q, want %q", got, fromStore)
	}

	if got := readString(t, written, "holds"); got != hold60s {
		t.Fatalf("the hold reads %q, want 60s", got)
	}

	// The whole round trip: read it back through a fresh handler over the same directory, which is
	// what a restart is.
	again := newTablesUnderTest(t, twoPrinters(), twoBindings(), "")
	if copyErr := copyStore(under.dir, again.dir); copyErr != nil {
		t.Fatalf("hand the written store to the second handler: %v", copyErr)
	}

	status, body = again.do(t, http.MethodGet, "/states/PAUSED", "")
	if status != http.StatusOK {
		t.Fatalf("GET /states/PAUSED answered %d: %v", status, body)
	}

	read := readObject(t, body, "state")

	if got := readString(t, read, "word"); got != "PAUSE" {
		t.Fatalf("the stored word reads %q after a restart, want PAUSE", got)
	}

	if got := readString(t, read, "holds"); got != hold60s {
		t.Fatalf("the stored hold reads %q after a restart, want 60s", got)
	}

	colour := readObject(t, read, "colour")
	if readInt(t, colour, "r") != 12 || readInt(t, colour, "g") != 34 || readInt(t, colour, "b") != 56 {
		t.Fatalf("the stored colour reads %v, want 12,34,56", colour)
	}

	// The document holds one row, about that state, with the three fields — and the binding it
	// found is still there, because a write here is a read-modify-write of the whole file.
	stored := under.stored(t)

	if len(stored.States) != 1 || stored.States[0].State != "PAUSED" {
		t.Fatalf("the document holds %+v, want one row for PAUSED", stored.States)
	}

	if len(stored.Bindings) != 1 {
		t.Fatalf("the document holds %d bindings after a state write, want the one it had", len(stored.Bindings))
	}

	// Delete puts it back to the colour Appendix A gives it, no word, and a hold of forever.
	status, body = under.do(t, http.MethodDelete, "/states/PAUSED", "")
	if status != http.StatusOK {
		t.Fatalf("DELETE /states/PAUSED answered %d: %v", status, body)
	}

	if !readBool(t, body, "removed") {
		t.Fatal("the delete answered that there was nothing to remove")
	}

	back := readObject(t, body, "state")

	if readBool(t, back, "overridden") {
		t.Fatal("the state still carries an override after its row was deleted")
	}

	if got := readString(t, back, "colour_origin"); got != fromDefault {
		t.Fatalf("the colour's origin is %q after the delete, want %q", got, fromDefault)
	}

	if readInt(t, readObject(t, back, "colour"), "r") != int(render.ColourPaused.R) {
		t.Fatalf("PAUSED did not go back to the renderer's colour: %v", readObject(t, back, "colour"))
	}

	if got := readString(t, back, "word"); got != "" {
		t.Fatalf("the word reads %q after the delete, want none", got)
	}

	// A second delete is not an error: there is nothing to remove, and the answer says so.
	status, body = under.do(t, http.MethodDelete, "/states/PAUSED", "")
	if status != http.StatusOK {
		t.Fatalf("a second DELETE /states/PAUSED answered %d: %v", status, body)
	}

	if readBool(t, body, "removed") {
		t.Fatal("a second delete answered that it removed a row, and there was none")
	}

	if len(under.stored(t).States) != 0 {
		t.Fatal("the document still holds a state row after the delete")
	}
}

// copyStore hands one directory's document to another, which is how a test reads a written store
// through a second handler without a second grant.
func copyStore(from, to string) error {
	raw, err := os.ReadFile(storePath(from))
	if err != nil {
		return err
	}

	return os.WriteFile(storePath(to), raw, 0o600)
}

// TestAHoldThatIsNotOneOfTheFourIsRefusedByName is the table's one enumerated value: a word holds
// forever, 30s, 60s or 5m, and anything else is a refusal that names what was sent and what is
// accepted. A hold rounded to the nearest of the four would be a word that stayed up for a length
// nobody asked for.
func TestAHoldThatIsNotOneOfTheFourIsRefusedByName(t *testing.T) {
	under := newTablesUnderTest(t, twoPrinters(), twoBindings(), "")

	refused := []string{"10s", "1m", "5 min", "Forever", "forever ", "", "0s", "-30s"}

	for _, holds := range refused {
		t.Run(holds, func(t *testing.T) {
			status, body := under.do(t, http.MethodPut, "/states/PAUSED",
				`{"holds": "`+holds+`"}`)

			if status != http.StatusConflict {
				t.Fatalf("a hold of %q answered %d, want 409: %v", holds, status, body)
			}

			if got := code(t, body); got != StateRefused.Code().String() {
				t.Fatalf("the refusal is %q, want %q", got, StateRefused.Code().String())
			}

			detail, _ := body["detail"].(string)

			if holds != "" && !strings.Contains(detail, holds) {
				t.Fatalf("the refusal %q does not name the value %q that was sent", detail, holds)
			}

			for _, allowed := range holdsChoices {
				if !strings.Contains(detail, allowed) {
					t.Fatalf("the refusal %q does not name the accepted value %q", detail, allowed)
				}
			}
		})
	}

	// And the four are accepted, which is the half a test of refusals usually forgets.
	for _, holds := range holdsChoices {
		status, body := under.do(t, http.MethodPut, "/states/ERROR", `{"word": "ERROR", "holds": "`+holds+`"}`)
		if status != http.StatusOK {
			t.Fatalf("a hold of %q answered %d: %v", holds, status, body)
		}

		if got := readString(t, readObject(t, body, "state"), "holds"); got != holds {
			t.Fatalf("the hold reads %q after being written as %q", got, holds)
		}
	}
}

// TestAColourOutsideTheByteIsRefused is the other value a body can get wrong: three channels of
// 0-255, refused with the number rather than wrapped to a different colour.
func TestAColourOutsideTheByteIsRefused(t *testing.T) {
	under := newTablesUnderTest(t, twoPrinters(), twoBindings(), "")

	for _, body := range []string{
		`{"colour": {"r": 256, "g": 0, "b": 0}}`,
		`{"colour": {"r": 0, "g": -1, "b": 0}}`,
		`{"colour": {"r": 0, "g": 0, "b": 999}}`,
	} {
		status, answer := under.do(t, http.MethodPut, "/states/IDLE", body)
		if status != http.StatusConflict {
			t.Fatalf("%s answered %d, want 409: %v", body, status, answer)
		}

		if got := code(t, answer); got != StateRefused.Code().String() {
			t.Fatalf("the refusal for %s is %q, want %q", body, got, StateRefused.Code().String())
		}
	}

	// A body naming nothing is refused rather than read as "clear the row", because clearing is
	// what DELETE is for.
	status, answer := under.do(t, http.MethodPut, "/states/IDLE", `{}`)
	if status != http.StatusBadRequest {
		t.Fatalf("an empty body answered %d, want 400: %v", status, answer)
	}

	if got := code(t, answer); got != StateBodyInvalid.Code().String() {
		t.Fatalf("the refusal for an empty body is %q, want %q", got, StateBodyInvalid.Code().String())
	}

	// And a key this build does not read is refused rather than ignored.
	status, answer = under.do(t, http.MethodPut, "/states/IDLE", `{"colur": {"r": 1, "g": 2, "b": 3}}`)
	if status != http.StatusBadRequest {
		t.Fatalf("a misspelt key answered %d, want 400: %v", status, answer)
	}
}

// TestAMomentWhoseWhichDoesNotMatchItsShowsIsRefusedWithAReason is the rule that a value is judged
// against what it belongs to, and refused with a sentence rather than quietly coerced: a colour that
// is not a colour, a word with nothing in it, a preset with nothing named, and a which beside
// Nothing are four different mistakes and none of them is silently corrected.
func TestAMomentWhoseWhichDoesNotMatchItsShowsIsRefusedWithAReason(t *testing.T) {
	under := newTablesUnderTest(t, twoPrinters(), twoBindings(), "")

	cases := []struct {
		name string
		body string
		says string
	}{
		{
			name: "a colour that is not one of the table's colours",
			body: `{"enabled": true, "shows": "Colour", "which": "Snow Fall", "for": "10s"}`,
			says: "not one of the table's colours",
		},
		{
			name: "a word with nothing in it",
			body: `{"enabled": true, "shows": "Word", "which": "  ", "for": "10s"}`,
			says: "which is empty",
		},
		{
			name: "a preset with nothing named",
			body: `{"enabled": true, "shows": "Preset", "which": "", "for": "10s"}`,
			says: "which is empty",
		},
		{
			name: "a which beside Nothing",
			body: `{"enabled": true, "shows": "Nothing", "which": "Matrix", "for": "10s"}`,
			says: "nothing shows nothing",
		},
		{
			name: "a shows this build does not have",
			body: `{"enabled": true, "shows": "Effect", "which": "Matrix", "for": "10s"}`,
			says: "not something a moment can show",
		},
		{
			name: "a length that is not a length",
			body: `{"enabled": true, "shows": "Preset", "which": "Matrix", "for": "ten seconds"}`,
			says: "not a length",
		},
		{
			name: "a body that does not say whether the moment is on",
			body: `{"shows": "Preset", "which": "Matrix", "for": "10s"}`,
			says: "does not say whether the moment is on",
		},
		{
			name: "a body that does not say what the moment shows",
			body: `{"enabled": true, "which": "Matrix", "for": "10s"}`,
			says: "does not say what the moment shows",
		},
	}

	for _, one := range cases {
		t.Run(one.name, func(t *testing.T) {
			status, answer := under.do(t, http.MethodPut, "/lights/10.0.0.253/moments/print-started", one.body)

			want := http.StatusConflict
			if strings.Contains(one.says, "does not say") || strings.Contains(one.says, "not JSON") {
				want = http.StatusBadRequest
			}

			if status != want {
				t.Fatalf("%s answered %d, want %d: %v", one.body, status, want, answer)
			}

			detail, _ := answer["detail"].(string)
			if !strings.Contains(detail, one.says) {
				t.Fatalf("the refusal %q does not say %q", detail, one.says)
			}

			// Nothing was written: the refusal happens before the store is touched.
			if held := under.stored(t).Moments; len(held) != 0 {
				t.Fatalf("a refused write left %+v in the document", held)
			}
		})
	}

	// The other half: a colour that *is* one of the table's colours is accepted, and a preset name
	// is taken as written without asking the light whether it has one.
	status, answer := under.do(t, http.MethodPut, "/lights/10.0.0.253/moments/paused",
		`{"printer": "printer-a", "enabled": true, "shows": "Colour", "which": "paused", "for": "forever"}`)
	if status != http.StatusOK {
		t.Fatalf("a moment showing a colour answered %d: %v", status, answer)
	}

	if got := readString(t, readObject(t, answer, "moment"), "which"); got != "paused" {
		t.Fatalf("the colour name reads %q, want the name as it was written", got)
	}

	status, answer = under.do(t, http.MethodPut, "/lights/10.0.0.253/moments/print-started",
		`{"printer": "printer-a", "enabled": true, "shows": "Preset", "which": "Snow Fall", "for": "10s"}`)
	if status != http.StatusOK {
		t.Fatalf("a preset this light may not hold answered %d, and a write must not ask the light: %v", status, answer)
	}
}

// TestAMomentEntrySurvivesForTwoPrintersOnOneLight is the key of the moment table: one light, two
// printers, one moment, two entries — and each one independent of the other, which is what
// multi-room means and what the screen's sub-rows are.
func TestAMomentEntrySurvivesForTwoPrintersOnOneLight(t *testing.T) {
	under := newTablesUnderTest(t, twoPrinters(), twoBindings(), "")

	// Printer A celebrates a print start with one of the light's presets.
	if status, body := under.do(t, http.MethodPut, "/lights/10.0.0.253/moments/print-started",
		`{"printer": "printer-a", "enabled": true, "shows": "Preset", "which": "Snow Fall", "for": "10s"}`); status != http.StatusOK {
		t.Fatalf("the first printer's print-started answered %d: %v", status, body)
	}

	// Printer B stays quiet on the same moment and shows a colour on a pause instead.
	if status, body := under.do(t, http.MethodPut, "/lights/10.0.0.253/moments/print-started",
		`{"printer": "printer-b", "enabled": false, "shows": "Nothing", "which": "", "for": "forever"}`); status != http.StatusOK {
		t.Fatalf("the second printer's print-started answered %d: %v", status, body)
	}

	if status, body := under.do(t, http.MethodPut, "/lights/10.0.0.253/moments/paused",
		`{"printer": "printer-b", "enabled": true, "shows": "Colour", "which": "FINISHED", "for": "5m"}`); status != http.StatusOK {
		t.Fatalf("the second printer's paused answered %d: %v", status, body)
	}

	// The whole screen's read: every moment, every printer, whatever the state of the pair.
	status, body := under.do(t, http.MethodGet, "/lights/10.0.0.253/moments", "")
	if status != http.StatusOK {
		t.Fatalf("GET /lights/10.0.0.253/moments answered %d: %v", status, body)
	}

	if got := readString(t, body, "light"); got != "10.0.0.253" {
		t.Fatalf("the answer is about the light %q, want 10.0.0.253", got)
	}

	if got := readInt(t, body, "count"); got != len(momentTable) {
		t.Fatalf("the answer carries %d moments, want all %d a light has", got, len(momentTable))
	}

	printers := readObjects(t, body, "printers")
	if len(printers) != 2 {
		t.Fatalf("the answer carries %d printers, want the two on the light", len(printers))
	}

	if got := readInt(t, readNamed(t, printers, "printer", "printer-b"), "segment"); got != 4 {
		t.Fatalf("printer-b's segment is %d, want 4", got)
	}

	moments := readObjects(t, body, "moments")

	started := readNamed(t, moments, "moment", momentPrintStarted)
	entries := readObjects(t, started, "entries")

	if len(entries) != 2 {
		t.Fatalf("print-started carries %d entries, want one per printer on the light", len(entries))
	}

	first := readNamed(t, entries, "printer", "printer-a")
	if !readBool(t, first, "enabled") || readString(t, first, "shows") != showsPreset ||
		readString(t, first, "which") != "Snow Fall" || readString(t, first, "for") != "10s" {
		t.Fatalf("printer-a's print-started reads %v, want the preset it was given", first)
	}

	if got := readString(t, first, "origin"); got != fromStore {
		t.Fatalf("printer-a's entry came from %q, want the store", got)
	}

	second := readNamed(t, entries, "printer", "printer-b")
	if readBool(t, second, "enabled") || readString(t, second, "shows") != showsNothing {
		t.Fatalf("printer-b's print-started reads %v, want it off and showing nothing", second)
	}

	// Everything nobody edited is the default, and says so: the finished moment is on with the word
	// this build already draws, and the other seven are off.
	finished := readNamed(t, moments, "moment", momentPrintFinished)

	for _, entry := range readObjects(t, finished, "entries") {
		if !readBool(t, entry, "enabled") || readString(t, entry, "shows") != showsWord ||
			readString(t, entry, "which") != finishedWord || readString(t, entry, "for") != hold60s {
			t.Fatalf("the finished moment reads %v, want the word this build draws for a minute", entry)
		}

		if got := readString(t, entry, "origin"); got != fromDefault {
			t.Fatalf("the finished moment came from %q, want the default", got)
		}
	}

	for _, id := range []string{momentPrintCancelled, momentPrinterWentQuiet, momentLightLost} {
		for _, entry := range readObjects(t, readNamed(t, moments, "moment", id), "entries") {
			if readBool(t, entry, "enabled") {
				t.Fatalf("%s is on by default, and §2.1 says only the finished moment is on", id)
			}
		}
	}

	// The document holds three rows, one per edited pair, and a row for printer-a's pause is not a
	// row for printer-b's: that is the key.
	stored := under.stored(t)
	if len(stored.Moments) != 3 {
		t.Fatalf("the document holds %d moment rows, want 3", len(stored.Moments))
	}

	if _, ok := storedMomentFor(stored, "10.0.0.253", "printer-b", momentPaused); !ok {
		t.Fatal("printer-b's pause is not in the document")
	}

	if _, ok := storedMomentFor(stored, "10.0.0.253", "printer-a", momentPaused); ok {
		t.Fatal("printer-a has a pause entry and it was never written")
	}

	// A restart reads the same three rows back.
	again := newTablesUnderTest(t, twoPrinters(), twoBindings(), "")
	if err := copyStore(under.dir, again.dir); err != nil {
		t.Fatalf("hand the written store to the second handler: %v", err)
	}

	status, body = again.do(t, http.MethodGet, "/lights/10.0.0.253/moments/paused", "")
	if status != http.StatusOK {
		t.Fatalf("GET the paused moment after a restart answered %d: %v", status, body)
	}

	entries = readObjects(t, readObject(t, body, "moment"), "entries")

	if got := readString(t, readNamed(t, entries, "printer", "printer-b"), "which"); got != "FINISHED" {
		t.Fatalf("printer-b's pause reads the colour %q after a restart, want FINISHED", got)
	}

	if got := readString(t, readNamed(t, entries, "printer", "printer-a"), "origin"); got != fromDefault {
		t.Fatalf("printer-a's pause came from %q after a restart, want the default", got)
	}

	// Deleting one printer's row leaves the other's alone.
	status, body = under.do(t, http.MethodDelete, "/lights/10.0.0.253/moments/print-started?printer=printer-a", "")
	if status != http.StatusOK {
		t.Fatalf("DELETE printer-a's print-started answered %d: %v", status, body)
	}

	if !readBool(t, body, "removed") {
		t.Fatal("the delete answered that there was nothing to remove")
	}

	if got := readString(t, readObject(t, body, "moment"), "origin"); got != fromDefault {
		t.Fatalf("printer-a's print-started came from %q after the delete, want the default", got)
	}

	if _, ok := storedMomentFor(under.stored(t), "10.0.0.253", "printer-b", momentPrintStarted); !ok {
		t.Fatal("deleting printer-a's entry took printer-b's with it")
	}
}

// TestALightCarryingOnePrinterIsTheFlatTable is the screen's other shape: one printer on a light is
// one row per moment, and a body need not name the printer because there is nothing to choose.
func TestALightCarryingOnePrinterIsTheFlatTable(t *testing.T) {
	one := bindings{{DeviceID: "printer-a", Address: "http://10.0.0.136", Segment: -1, Brightness: 128}}

	under := newTablesUnderTest(t, []deviceapi.Device{{ID: "printer-a", Name: "Hermes MK4"}}, one, "")

	status, body := under.do(t, http.MethodGet, "/lights/10.0.0.136/moments", "")
	if status != http.StatusOK {
		t.Fatalf("GET /lights/10.0.0.136/moments answered %d: %v", status, body)
	}

	for _, moment := range readObjects(t, body, "moments") {
		if got := readInt(t, moment, "printer_count"); got != 1 {
			t.Fatalf("%s carries %d entries, want the one printer's", readString(t, moment, "moment"), got)
		}
	}

	// No printer in the body: the light carries one, so there is nothing to guess.
	status, body = under.do(t, http.MethodPut, "/lights/10.0.0.136/moments/print-cancelled",
		`{"enabled": true, "shows": "Colour", "which": "STOPPED", "for": "30s"}`)
	if status != http.StatusOK {
		t.Fatalf("a body naming no printer answered %d on a light carrying one: %v", status, body)
	}

	entry := readObject(t, body, "moment")
	if readString(t, entry, "printer") != "printer-a" || !readBool(t, entry, "whole") {
		t.Fatalf("the entry reads %v, want printer-a on the whole light", entry)
	}
}

// TestAMomentCanBeSavedWhileTheLightIsNotAnswering is the rule the Events screen depends on: the
// write is a record, and a lamp that is unplugged must not be able to refuse an edit somebody made
// while standing in front of it.
func TestAMomentCanBeSavedWhileTheLightIsNotAnswering(t *testing.T) {
	under := newTablesUnderTest(t, twoPrinters(), twoBindings(), "")

	under.light.stopAnswering()

	status, body := under.do(t, http.MethodPut, "/lights/10.0.0.253/moments/error-or-attention",
		`{"printer": "printer-a", "enabled": true, "shows": "Preset", "which": "Alien Force Clock", "for": "forever"}`)
	if status != http.StatusOK {
		t.Fatalf("a write against a light that is not answering was refused with %d: %v", status, body)
	}

	if got := readString(t, readObject(t, body, "moment"), "which"); got != "Alien Force Clock" {
		t.Fatalf("the entry reads %q, want the preset name taken as written", got)
	}

	if _, ok := storedMomentFor(under.stored(t), "10.0.0.253", "printer-a", momentErrorAttention); !ok {
		t.Fatal("the entry did not reach the document, and the light was not answering")
	}

	// The read works too, and says the names it holds; whether the light still has that preset is a
	// read of the light's own presets, which this build does not make.
	status, body = under.do(t, http.MethodGet, "/lights/10.0.0.253/moments/error-or-attention", "")
	if status != http.StatusOK {
		t.Fatalf("reading the moment back answered %d: %v", status, body)
	}

	if got := readString(t, readNamed(t, readObjects(t, readObject(t, body, "moment"), "entries"), "printer", "printer-a"), "origin"); got != fromStore {
		t.Fatalf("the entry came from %q, want the store", got)
	}
}

// TestAModuleGrantedNoDataDirectoryAnswersTheDefaultsAndRefusesToWrite is the install that has not
// approved a data directory: everything reads as the table this build holds, nothing can be kept,
// and the answer says which rather than pretending to save.
func TestAModuleGrantedNoDataDirectoryAnswersTheDefaultsAndRefusesToWrite(t *testing.T) {
	under := newTablesUnderTest(t, twoPrinters(), twoBindings(), "")
	under.tables.grant("")

	status, body := under.do(t, http.MethodGet, "/states", "")
	if status != http.StatusOK {
		t.Fatalf("GET /states answered %d without a store: %v", status, body)
	}

	if readBool(t, body, "persisted") {
		t.Fatal("the table says a change would be kept, and the module was granted no directory")
	}

	if got := readString(t, body, "note"); got == "" {
		t.Fatal("the answer carries no note saying why a change could not be kept")
	}

	if got := readInt(t, body, "count"); got != 12 {
		t.Fatalf("the table answered %d rows without a store, want the twelve defaults", got)
	}

	for _, method := range []string{http.MethodPut, http.MethodDelete} {
		response, answer := under.do(t, method, "/states/PAUSED", `{"holds": "60s"}`)
		if response != http.StatusServiceUnavailable {
			t.Fatalf("%s answered %d without a store, want 503: %v", method, response, answer)
		}

		if got := code(t, answer); got != StoreUnavailable.Code().String() {
			t.Fatalf("the refusal is %q, want %q", got, StoreUnavailable.Code().String())
		}
	}
}

// TestAnUnknownNameIsRefusedByCode is the surface's edges: a state the table does not hold, a moment
// no light has, and a light this module does not drive are three different codes, so a caller
// branches on the code and not on a sentence.
func TestAnUnknownNameIsRefusedByCode(t *testing.T) {
	under := newTablesUnderTest(t, twoPrinters(), twoBindings(), "")

	cases := []struct {
		name   string
		method string
		path   string
		body   string
		want   string
	}{
		{"a state the table does not hold", http.MethodGet, "/states/SLEEPING", "", StateNotFound.Code().String()},
		{"a state the table does not hold, written", http.MethodPut, "/states/SLEEPING", `{"holds": "60s"}`, StateNotFound.Code().String()},
		{"a moment no light has", http.MethodGet, "/lights/10.0.0.253/moments/finished", "", MomentNotFound.Code().String()},
		{"a light this module does not drive", http.MethodGet, "/lights/10.0.0.99/moments", "", ControllerNotFound.Code().String()},
		{"a moment on a light it does not drive", http.MethodPut, "/lights/10.0.0.99/moments/paused",
			`{"enabled": true, "shows": "Nothing", "which": "", "for": "forever"}`, ControllerNotFound.Code().String()},
		{"a printer that is not on the light", http.MethodPut, "/lights/10.0.0.253/moments/paused",
			`{"printer": "printer-z", "enabled": true, "shows": "Nothing", "which": "", "for": "forever"}`, MomentRefused.Code().String()},
	}

	for _, one := range cases {
		t.Run(one.name, func(t *testing.T) {
			status, answer := under.do(t, one.method, one.path, one.body)

			if status < 400 {
				t.Fatalf("%s %s answered %d, want a refusal", one.method, one.path, status)
			}

			if got := code(t, answer); got != one.want {
				t.Fatalf("%s %s answered the code %q, want %q", one.method, one.path, got, one.want)
			}
		})
	}

	// A light carrying two printers refuses a body that names neither, rather than picking one.
	status, answer := under.do(t, http.MethodPut, "/lights/10.0.0.253/moments/paused",
		`{"enabled": true, "shows": "Nothing", "which": "", "for": "forever"}`)

	if status != http.StatusConflict {
		t.Fatalf("a body naming no printer on a two-printer light answered %d, want 409: %v", status, answer)
	}

	detail, _ := answer["detail"].(string)
	if !strings.Contains(detail, "printer-a") || !strings.Contains(detail, "printer-b") {
		t.Fatalf("the refusal %q does not name the printers the light carries", detail)
	}
}

// stateRoutesAt builds the state table's routes over a store in a directory of its own, judged at a
// clock the test moves.
//
// No watch runs. These routes answer about the document and about what the writers published — a
// state's start comes from a writer's own report (see stateStarts), which a test sets directly
// rather than starting a watch whose driver would publish a state of its own over it.
func stateRoutesAt(t *testing.T, clock func() time.Time) *tablesUnderTest {
	t.Helper()

	dir := t.TempDir()

	if _, err := saveStore(dir, storedConfig{Version: storeVersion}); err != nil {
		t.Fatalf("write the store the module would have written: %v", err)
	}

	tables := &configTables{}
	tables.grant(dir)

	// No kept host answer survives from another fixture: see forgetSeen.
	forgetSeen()

	mux := http.NewServeMux()
	tableAPI{tables: tables, clock: clock}.mount(mux)

	return &tablesUnderTest{mux: mux, dir: dir, tables: tables}
}

// TestAWordSetOnARunningStateSaysWhenItWillStop is the write's answer, which is where the defect
// was most expensive: it said 200 and meant nothing.
//
// The printer finished four hours ago, so a hold counted from the state's entry expired three
// hours and fifty-nine minutes ago. The word is now counted from the write, and the answer says
// both halves of that: that the word is on the light, and the instant it stops being on it — the
// number a person needs to know when to look.
func TestAWordSetOnARunningStateSaysWhenItWillStop(t *testing.T) {
	at := time.Date(2026, time.November, 19, 20, 0, 0, 0, time.UTC)
	now := at

	under := stateRoutesAt(t, func() time.Time { return now })

	// The light is showing FINISHED, and has been since four hours before the write.
	publishShape(
		binding{DeviceID: "printer-a", Address: "http://10.0.0.136", Segment: -1},
		bindingShape{state: printer.StateFinished, since: at.Add(-4 * time.Hour)})
	t.Cleanup(forgetBindings)

	status, body := under.do(t, http.MethodPut, "/states/FINISHED", `{"word": "SWEEP", "holds": "60s"}`)
	if status != http.StatusOK {
		t.Fatalf("PUT /states/FINISHED answered %d: %v", status, body)
	}

	written := readObject(t, body, "state")

	if got := readString(t, written, "word_shows"); got != wordShowsShowing {
		t.Fatalf("the write says the word is %q, want %q: the state is being shown and the hold starts at the write",
			got, wordShowsShowing)
	}

	if want := at.Add(time.Minute).Format(time.RFC3339); readString(t, written, "word_until") != want {
		t.Fatalf("the write says the word shows until %q, want %q — sixty seconds from the write and not from the state",
			readString(t, written, "word_until"), want)
	}

	// The sentence is the point of the answer: a person reads it rather than decoding a code.
	if note := readString(t, body, "note"); !strings.Contains(note, "on the light now") {
		t.Fatalf("the note reads %q, want it to say the word is on the light now", note)
	}

	// And the moment is in the document, because that is what makes the answer survive a restart.
	stored := under.stored(t)

	if len(stored.States) != 1 || stored.States[0].SetAt == nil || !stored.States[0].SetAt.Equal(at) {
		t.Fatalf("the document holds %+v, want the state's row stamped with the moment of the write", stored.States)
	}

	// A minute and a second later the same word has run out, and the answer says so instead of
	// repeating a success nobody can see on the light.
	now = at.Add(61 * time.Second)

	status, body = under.do(t, http.MethodGet, "/states/FINISHED", "")
	if status != http.StatusOK {
		t.Fatalf("GET /states/FINISHED answered %d: %v", status, body)
	}

	read := readObject(t, body, "state")

	if got := readString(t, read, "word_shows"); got != wordShowsExpired {
		t.Fatalf("a word whose hold has run out reads %q, want %q", got, wordShowsExpired)
	}
}

// TestAWordForAStateNobodyIsShowingSaysItIsWaiting is the third answer: a word set on a state no
// light is showing is not a failure and not a success — it runs from the moment a light enters that
// state, and the answer says which.
func TestAWordForAStateNobodyIsShowingSaysItIsWaiting(t *testing.T) {
	at := time.Date(2026, time.November, 19, 20, 0, 0, 0, time.UTC)

	under := stateRoutesAt(t, func() time.Time { return at })

	status, body := under.do(t, http.MethodPut, "/states/ERROR", `{"word": "OH NO", "holds": "30s"}`)
	if status != http.StatusOK {
		t.Fatalf("PUT /states/ERROR answered %d: %v", status, body)
	}

	written := readObject(t, body, "state")

	if got := readString(t, written, "word_shows"); got != wordShowsLater {
		t.Fatalf("a word on a state no light shows reads %q, want %q", got, wordShowsLater)
	}

	if until := readString(t, written, "word_until"); until != "" {
		t.Fatalf("a word waiting for its state says it shows until %q, and there is no instant it stops at", until)
	}
}

// TestAWriteWithNoWordSaysNothingAboutOne keeps the new field honest: a colour written on its own
// carries no word, so there is no hold to judge and the answer does not invent one.
func TestAWriteWithNoWordSaysNothingAboutOne(t *testing.T) {
	at := time.Date(2026, time.November, 19, 20, 0, 0, 0, time.UTC)

	under := stateRoutesAt(t, func() time.Time { return at })

	status, body := under.do(t, http.MethodPut, "/states/IDLE", `{"colour": {"r": 1, "g": 2, "b": 3}}`)
	if status != http.StatusOK {
		t.Fatalf("PUT /states/IDLE answered %d: %v", status, body)
	}

	if got := readString(t, readObject(t, body, "state"), "word_shows"); got != wordShowsNone {
		t.Fatalf("a colour-only write says its word is %q, want %q", got, wordShowsNone)
	}

	// And no moment is stamped on a row with no word to measure.
	if stored := under.stored(t); len(stored.States) != 1 || stored.States[0].SetAt != nil {
		t.Fatalf("the document holds %+v, want a row with no write moment", stored.States)
	}
}
