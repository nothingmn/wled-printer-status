// Running one of a controller's own presets: the name resolved against the light's own table, and the
// body that runs it.
//
// The two things worth pinning. **The id comes from the light**, not from a number this module invented:
// the operator names a preset by the words WLED's interface shows him, and the translation is a read of
// /presets.json. And **a name the light does not hold is refused**, because WLED answers `{"ps":99}` for
// a slot it does not have with a success and draws nothing — a moment that did nothing, indistinguishable
// from a moment nobody could see.
package wled

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// presetsAndState is a controller that answers with one preset table and records the state write that
// follows, so that a test sees both halves of RunPreset in the order it makes them.
type presetsAndState struct {
	table   string
	method  string
	path    string
	body    string
	written bool
}

// server answers /presets.json with the table and everything else with success, recording each request.
func (p *presetsAndState) server(t *testing.T) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)

		switch r.URL.Path {
		case pathPresets:
			_, _ = io.WriteString(w, p.table)
		case pathState:
			p.method, p.path, p.body = r.Method, r.URL.Path, string(raw)
			p.written = true
			_, _ = io.WriteString(w, `{"success":true}`)
		default:
			_, _ = io.WriteString(w, `{"success":true}`)
		}
	}))

	t.Cleanup(server.Close)

	return server
}

// TestRunPresetNamesTheLightsOwnPresetAndRunsIt is the whole call: the name is found in the table the
// controller answered with, and the id that table keys it by is what the state write carries.
func TestRunPresetNamesTheLightsOwnPresetAndRunsIt(t *testing.T) {
	controller := &presetsAndState{table: `{"1":{"n":"Matrix"},"2":{"n":"Snow Fall"},"3":{"n":"Alien Force Clock"},"0":{}}`}

	if err := client(t, controller.server(t).URL).RunPreset(context.Background(), "Snow Fall"); err != nil {
		t.Fatalf("RunPreset: %v", err)
	}

	if !controller.written {
		t.Fatal("RunPreset read the table and wrote nothing")
	}

	if controller.method != http.MethodPost || controller.path != pathState {
		t.Fatalf("RunPreset sent %s %s, want POST %s", controller.method, controller.path, pathState)
	}

	// The body is one key: a preset carries its own segments, brightness and LED map, so nothing else is
	// named beside it.
	if want := `{"ps":2}`; controller.body != want {
		t.Fatalf("RunPreset sent\n%s\nwant\n%s", controller.body, want)
	}
}

// TestRunPresetForgivesOnlyCaseAndSpaces is the matching rule, said as a table: a name typed by a person
// and copied between windows differs in case and in spaces, and nothing else is forgiven.
func TestRunPresetForgivesOnlyCaseAndSpaces(t *testing.T) {
	for _, one := range []struct {
		name string
		want string
	}{
		{name: "snow fall", want: `{"ps":2}`},
		{name: "  SNOW FALL  ", want: `{"ps":2}`},
		{name: "Alien Force Clock", want: `{"ps":3}`},
	} {
		t.Run(one.name, func(t *testing.T) {
			controller := &presetsAndState{table: `{"1":{"n":"Matrix"},"2":{"n":"Snow Fall"},"3":{"n":"Alien Force Clock"}}`}

			if err := client(t, controller.server(t).URL).RunPreset(context.Background(), one.name); err != nil {
				t.Fatalf("RunPreset(%q): %v", one.name, err)
			}

			if controller.body != one.want {
				t.Fatalf("RunPreset(%q) sent %s, want %s", one.name, controller.body, one.want)
			}
		})
	}
}

// TestRunPresetRefusesANameTheLightDoesNotHold is the refusal, and it is a refusal rather than a write:
// WLED would take a number for a slot it does not have and draw nothing, and this module would report a
// moment that nobody could see.
func TestRunPresetRefusesANameTheLightDoesNotHold(t *testing.T) {
	controller := &presetsAndState{table: `{"1":{"n":"Matrix"}}`}

	err := client(t, controller.server(t).URL).RunPreset(context.Background(), "Snow Fall")
	if !errors.Is(err, ErrNoSuchPreset) {
		t.Fatalf("RunPreset answered %v, want ErrNoSuchPreset", err)
	}

	if controller.written {
		t.Fatalf("a name the light does not hold was written anyway: %s", controller.body)
	}
}

// TestRunPresetTakesTheLowerIDofTwoPresetsOfOneName keeps two moments naming one animation rather than
// whichever the table's map happened to yield: WLED lets two slots carry one name, and a module that
// picked at random would put a different animation on the light on different days.
func TestRunPresetTakesTheLowerIDofTwoPresetsOfOneName(t *testing.T) {
	controller := &presetsAndState{table: `{"7":{"n":"Matrix"},"2":{"n":"Matrix"}}`}

	if err := client(t, controller.server(t).URL).RunPreset(context.Background(), "Matrix"); err != nil {
		t.Fatalf("RunPreset: %v", err)
	}

	if want := `{"ps":2}`; controller.body != want {
		t.Fatalf("RunPreset sent %s, want the lower id %s", controller.body, want)
	}
}

// TestRunPresetRefusesAnEmptyName is the one thing a caller can get wrong without the light being
// involved: an empty which is a moment with no preset named, and it is refused before a request is made.
func TestRunPresetRefusesAnEmptyName(t *testing.T) {
	controller := &presetsAndState{table: `{"1":{"n":"Matrix"}}`}

	if err := client(t, controller.server(t).URL).RunPreset(context.Background(), "   "); !errors.Is(err, ErrNoSuchPreset) {
		t.Fatalf("RunPreset on an empty name answered %v, want ErrNoSuchPreset", err)
	}

	if controller.written {
		t.Fatal("an empty name was written")
	}
}
