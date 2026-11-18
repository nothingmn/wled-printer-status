package printer

import (
	"encoding/json"
	"fmt"
	"testing"
)

// frame builds one telemetry envelope the way the host publishes it: a
// CloudEvents envelope whose source is the device origin and whose data is the
// path and the value.
func frame(t *testing.T, source, data string) json.RawMessage {
	t.Helper()

	return json.RawMessage(fmt.Sprintf(
		`{"specversion":"1.0","id":"01J0000000000000000000000","source":%q,`+
			`"type":"com.prusa.connect.device.telemetry.sampled",`+
			`"time":"2026-11-18T14:02:00Z","evclass":"telemetry","hops":0,"data":%s}`,
		source, data))
}

// TestUpdatesReadsOnlyThePrinterItWasAskedAbout is the window filter. A window is
// a fan-out of everything the subscription may see, and one printer is one origin
// out of it: another device's patch, another kind's event, a frame that is not JSON
// and a frame with no addressable path are all silence rather than failure.
func TestUpdatesReadsOnlyThePrinterItWasAskedAbout(t *testing.T) {
	frames := []json.RawMessage{
		frame(t, "urn:prusa:site:home/device/d1", `{"path":"/job/progress","value":94}`),
		frame(t, "urn:prusa:site:home/device/d2", `{"path":"/job/progress","value":7}`),
		frame(t, "urn:prusa:site:home/device/d10", `{"path":"/job/progress","value":8}`),
		frame(t, "urn:prusa:site:home/camera/c1", `{"path":"/job/progress","value":1}`),
		json.RawMessage(`not a frame`),
		frame(t, "urn:prusa:site:home/device/d1", `{"path":"","value":1}`),
		frame(t, "urn:prusa:site:home/device/d1", `{"path":"job/progress","value":1}`),
		frame(t, "urn:prusa:site:home/device/d1", `{"path":"/job/progress"}`),
		frame(t, "urn:prusa:site:home/device/d1", `{"path":"/job/time_remaining","value":360}`),
	}

	updates := Updates(frames, "d1")

	if len(updates) != 2 {
		t.Fatalf("the window gave %d updates, want 2 (/job/progress and /job/time_remaining): %+v",
			len(updates), updates)
	}

	if updates[0].Path != PathJobProgress || string(updates[0].Value) != "94" {
		t.Fatalf("the first update is %s=%s, want /job/progress=94", updates[0].Path, updates[0].Value)
	}

	if updates[1].Path != PathJobRemaining || string(updates[1].Value) != "360" {
		t.Fatalf("the second update is %s=%s, want /job/time_remaining=360",
			updates[1].Path, updates[1].Value)
	}
}

// TestUpdatesKeepsTheValueAsItCame holds the decoder to handing the raw JSON on.
// Nothing in this package rewrites a value: the tree carries bytes the printer's
// driver produced, and a null arrives as a null.
func TestUpdatesKeepsTheValueAsItCame(t *testing.T) {
	frames := []json.RawMessage{
		frame(t, "urn:prusa:site:home/device/d1", `{"path":"/job/time_remaining","value":null}`),
		frame(t, "urn:prusa:site:home/device/d1", `{"path":"/tools/0/material_package_instance/package/material/abbreviation","value":"PETG"}`),
	}

	updates := Updates(frames, "d1")

	if len(updates) != 2 {
		t.Fatalf("the window gave %d updates, want 2", len(updates))
	}

	if string(updates[0].Value) != "null" {
		t.Fatalf("the null arrived as %s, want null", updates[0].Value)
	}

	if string(updates[1].Value) != `"PETG"` {
		t.Fatalf("the text arrived as %s, want the quoted string the host published", updates[1].Value)
	}
}
