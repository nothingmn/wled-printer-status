package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/prusa3d/prusa-connect/sdk/deviceapi"
)

// bindingsSetting is the `bindings` setting written the way an operator writes it, as the raw
// JSON the settings payload carries. Every case below is a text area somebody typed into.
func bindingsSetting(t *testing.T, lines ...string) []json.RawMessage {
	t.Helper()

	out := make([]json.RawMessage, 0, len(lines))

	for _, line := range lines {
		out = append(out, json.RawMessage(line))
	}

	return out
}

// TestTheBindingsSettingCarriesSeveralPrinters is the setting that makes two printers possible
// at all: a list of lines, each resolved to one machine the host holds, with the defaults the
// manifest publishes applied to the fields a line leaves out.
//
// The defaults are the whole reason this is not a plain struct: absent has to be told apart from
// zero. A line with no segment drives the whole controller, and one with no brightness is 128 —
// but a line naming segment 0 drives that segment and carries no brightness at all, because 0
// there would be a whole controller at black.
func TestTheBindingsSettingCarriesSeveralPrinters(t *testing.T) {
	morpheus := deviceapi.Device{ID: "01J8DEVICE", Name: "Morpheus"}
	other := deviceapi.Device{ID: "01J9OTHER", Name: "Other"}
	fleet := fakeFleet{page: deviceapi.Page{Items: []deviceapi.Device{morpheus, other}, Total: 2}}

	cases := []struct {
		name string
		line string
		want binding
	}{
		{
			name: "printer and controller, and nothing else",
			line: `{"printer": "Morpheus", "controller": "http://10.0.0.136"}`,
			want: binding{DeviceID: morpheus.ID, Address: "http://10.0.0.136", Segment: wholeStrip, Brightness: 128},
		},
		{
			name: "the device id, which survives a rename",
			line: `{"printer": "01J9OTHER", "controller": "http://10.0.0.253", "segment": 2}`,
			want: binding{DeviceID: other.ID, Address: "http://10.0.0.253", Segment: 2},
		},
		{
			name: "segment -1, which is the whole controller written out",
			line: `{"printer": "Morpheus", "controller": "http://10.0.0.136", "segment": -1, "brightness": 64}`,
			want: binding{DeviceID: morpheus.ID, Address: "http://10.0.0.136", Segment: wholeStrip, Brightness: 64},
		},
		{
			name: "brightness 0 on the whole controller, which is a strip switched off",
			line: `{"printer": "Morpheus", "controller": "http://10.0.0.136", "brightness": 0}`,
			want: binding{DeviceID: morpheus.ID, Address: "http://10.0.0.136", Segment: wholeStrip, Brightness: 0},
		},
	}

	for _, one := range cases {
		t.Run(one.name, func(t *testing.T) {
			set, err := bindingsFrom(context.Background(), fleet, bindingsSetting(t, one.line))
			if err != nil {
				t.Fatalf("the line was refused: %v", err)
			}

			if len(set) != 1 {
				t.Fatalf("the line became %d bindings, want 1", len(set))
			}

			if set[0] != one.want {
				t.Fatalf("the line reads as %+v, want %+v", set[0], one.want)
			}
		})
	}

	// Two printers on two controllers, which is the whole of what this change is for.
	set, err := bindingsFrom(context.Background(), fleet, bindingsSetting(t,
		`{"printer": "Morpheus", "controller": "http://10.0.0.136"}`,
		`{"printer": "Other", "controller": "http://10.0.0.253", "segment": 2}`,
	))
	if err != nil {
		t.Fatalf("two lines were refused: %v", err)
	}

	if len(set) != 2 {
		t.Fatalf("two lines became %d bindings, want 2", len(set))
	}

	if set[1].DeviceID != other.ID || set[1].Segment != 2 {
		t.Fatalf("the second line reads as %+v, want the other printer on segment 2", set[1])
	}
}

// TestTheBindingsSettingNamesTheLineItCannotRead is the other half: a list this module cannot
// run refuses the start, and the sentence says which line and which field. The operator is
// looking at a block of JSON in a text area, so "binding 2" is not enough and neither is
// Go's own message on its own.
func TestTheBindingsSettingNamesTheLineItCannotRead(t *testing.T) {
	morpheus := deviceapi.Device{ID: "01J8DEVICE", Name: "Morpheus"}
	other := deviceapi.Device{ID: "01J9OTHER", Name: "Other"}
	fleet := fakeFleet{page: deviceapi.Page{Items: []deviceapi.Device{morpheus, other}, Total: 2}}

	cases := []struct {
		name  string
		lines []string
		want  string
	}{
		{
			name:  "a line that names no printer",
			lines: []string{`{"controller": "http://10.0.0.136"}`},
			want:  "bindings[0] names no printer",
		},
		{
			name:  "a misspelt key, which is refused and not ignored",
			lines: []string{`{"printer": "Morpheus", "controler": "http://10.0.0.136"}`},
			want:  `bindings[0]: json: unknown field "controler"`,
		},
		{
			name:  "the second line is the one with the misspelt key",
			lines: []string{`{"printer": "Morpheus", "controller": "http://10.0.0.136"}`, `{"printer": "Morpheus", "controller": "http://10.0.0.136", "britness": 64}`},
			want:  `bindings[1]: json: unknown field "britness"`,
		},
		{
			name: "two lines on one segment",
			lines: []string{
				`{"printer": "Morpheus", "controller": "http://10.0.0.136", "segment": 2}`,
				`{"printer": "Morpheus", "controller": "http://10.0.0.136", "segment": 2}`,
			},
			want: "one printer is shown by one binding",
		},
		{
			name: "two printers on one segment",
			lines: []string{
				`{"printer": "Morpheus", "controller": "http://10.0.0.136", "segment": 2}`,
				`{"printer": "Other", "controller": "http://10.0.0.136", "segment": 2}`,
			},
			want: "one segment is shown by one binding",
		},
		{
			name:  "a controller that is not one",
			lines: []string{`{"printer": "Morpheus", "controller": "10.0.0.136"}`},
			want:  "needs an http or https scheme",
		},
		{
			name:  "a printer the host does not hold",
			lines: []string{`{"printer": "Ghost", "controller": "http://10.0.0.136"}`},
			want:  `bindings[0]: no printer in this instance is called "Ghost"`,
		},
		{
			name:  "a brightness on a borrowed segment, which would dim the whole controller",
			lines: []string{`{"printer": "Morpheus", "controller": "http://10.0.0.136", "segment": 2, "brightness": 64}`},
			want:  "a brightness is controller-wide",
		},
	}

	for _, one := range cases {
		t.Run(one.name, func(t *testing.T) {
			_, err := bindingsFrom(context.Background(), fleet, bindingsSetting(t, one.lines...))
			if err == nil {
				t.Fatalf("the lines were accepted, want a refusal containing %q", one.want)
			}

			if !strings.Contains(err.Error(), one.want) {
				t.Fatalf("the refusal reads %q, want it to contain %q", err, one.want)
			}
		})
	}
}

// TestTwoPrintersOnTwoControllersIsOneSettingsBlock is the case the operator asked for, run
// through the settings path rather than through the parser alone: two machines, two lights, one
// text field. It is here because a parser that accepts the list and a settings block that
// cannot carry one would look the same from the parser's own test.
func TestTwoPrintersOnTwoControllersIsOneSettingsBlock(t *testing.T) {
	hermes := deviceapi.Device{ID: "07e893bb-b922-4ff0-9ad3-061b957400c3", Name: "Hermes MK4"}
	mini := deviceapi.Device{ID: "01J9MINI", Name: "Mini"}

	fleet := fakeFleet{page: deviceapi.Page{Items: []deviceapi.Device{hermes, mini}, Total: 2}}

	sent := Settings{
		Message: defaultMessage,
		// The four older settings, still in the config block of an install that has been
		// running: empty here, which is what an instance driven by the list looks like.
		ControllerAddress: "",
		Brightness:        defaultBrightness,
		Segment:           defaultSegment,
		Bindings: bindingsSetting(t,
			`{"printer": "Hermes MK4", "controller": "http://10.0.0.136"}`,
			`{"printer": "Mini", "controller": "http://10.0.0.253", "segment": 2}`,
		),
	}

	if err := sent.validate(); err != nil {
		t.Fatalf("the settings were refused: %v", err)
	}

	set, err := wantedBindings(context.Background(), fleet, sent)
	if err != nil {
		t.Fatalf("the set was refused: %v", err)
	}

	grouped, err := set.controllers()
	if err != nil {
		t.Fatalf("the set cannot be grouped: %v", err)
	}

	if len(grouped) != 2 {
		t.Fatalf("the set drives %d controllers, want 2", len(grouped))
	}

	if grouped[0].Bindings[0].DeviceID != hermes.ID || grouped[1].Bindings[0].Segment != 2 {
		t.Fatalf("the set is %+v, want Hermes on the panel and Mini on segment 2 of the strip", grouped)
	}
}
