// Reading and writing a controller's whole configuration, which is what a snapshot is made of.
//
// # Why these three calls are here and not in client.go
//
// client.go talks to a light's segments: it says what to draw. These three do not draw anything.
// They read the document the firmware keeps about itself — GET /json/cfg — and the preset table
// beside it, GET /presets.json, and they write that document back. They are here as their own file
// because they are one errand (a restore) and because what is known about them was measured
// separately from everything else, below.
//
// # What was measured, on the operator's two lights
//
//   - A configuration write does not reboot the controller by itself. POST /json/cfg accepts a
//     full or a partial body, answers 200 {"success":true} in about 65 ms, and needs "rb": true in
//     the body, or a separate POST /json/state {"rb":true}, to reboot at all. A reboot takes about
//     5.7 s: the first refused poll at 0.635 s and the first answer at 5.667 s, on the same
//     address. That is docs/handoff.md §6, and it is why a restore asks for its reboot in the body
//     it writes rather than in a second request that could be lost between the two.
//
//   - Omitting the "nw" section makes the firmware clear nw.linked_remote (cfg.cpp:64). So a body
//     that means "put this configuration back" has to carry "nw", and has to carry it without the
//     one member that is a credential: nw.ins. That rule is in snapshots.go, where the body is
//     built; it is named here because this is where the measurement belongs.
//
//   - Neither light puts the wifi password in the dump at all: on morpheus (WLED 14.7.0-mdev) and
//     CyberDesk (WLED 16.0.1) the "nw" section carries ins, dns, espnow and linked_remote, and the
//     "wifi" section carries phy, sleep and txpwr, and neither carries psk or even ssid. A literal
//     hundred-per-cent clone including the wifi credentials is therefore impossible over the API,
//     and a snapshot says so rather than implying otherwise.
//
// The two documents are 3230 and 2621 bytes of config and 10620 and 12205 bytes of presets — about
// 15 KB a light, which is what made a file per snapshot a reasonable thing to keep.
package wled

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

const (
	// pathConfig is the controller's own configuration: the light's LED setup, its defaults, its
	// interfaces, its timers, its usermods, its access point, its name, and its network section.
	// A GET reads it whole; a POST takes a whole or a partial body and answers {"success":true}.
	pathConfig = "/json/cfg"

	// pathPresets is the table of presets, which is not part of /json/cfg on either of the
	// firmwares measured. A restore has to carry it separately, so it is read separately.
	pathPresets = "/presets.json"
)

// ErrConfigRefused means the controller answered a configuration write without saying it took it.
// A 200 that does not carry success:true is the firmware telling a caller nothing useful, and a
// restore that treated it as a write would be reporting a change that may not have happened.
var ErrConfigRefused = errors.New("wled: the controller did not say it took the configuration")

// Config reads the controller's whole configuration, verbatim.
//
// It is returned as the bytes that arrived, not as a struct: this package has no business
// deciding which of the firmware's own sections exist, and a snapshot that decoded only the keys
// this build knows would lose the rest of an operator's configuration on the way back.
func (c *Client) Config(ctx context.Context) (json.RawMessage, error) {
	var answer json.RawMessage

	if err := c.do(ctx, http.MethodGet, pathConfig, nil, &answer); err != nil {
		return nil, err
	}

	if len(answer) == 0 {
		return nil, fmt.Errorf("wled: %s%s answered with no configuration", c.base, pathConfig)
	}

	return answer, nil
}

// StateDocument reads what the controller is doing now, verbatim.
//
// It is the same document State parses, and it is here for the one caller that has to keep it rather
// than act on it: a snapshot records the state as it was at the moment it was taken, and re-encoding
// a parsed State would keep only the parts this build knows about.
func (c *Client) StateDocument(ctx context.Context) (json.RawMessage, error) {
	var answer json.RawMessage

	if err := c.do(ctx, http.MethodGet, pathState, nil, &answer); err != nil {
		return nil, err
	}

	if len(answer) == 0 {
		return nil, fmt.Errorf("wled: %s%s answered with no state", c.base, pathState)
	}

	return answer, nil
}

// Presets reads the controller's preset table, verbatim.
//
// A firmware that has never had a preset saved answers with an object carrying "0" and nothing
// else, which is not an error: it is a light with no presets, and a snapshot of it is a snapshot of
// a light with no presets.
func (c *Client) Presets(ctx context.Context) (json.RawMessage, error) {
	var answer json.RawMessage

	if err := c.do(ctx, http.MethodGet, pathPresets, nil, &answer); err != nil {
		return nil, err
	}

	if len(answer) == 0 {
		return nil, fmt.Errorf("wled: %s%s answered with no preset table", c.base, pathPresets)
	}

	return answer, nil
}

// WriteConfig posts one configuration body and holds the controller to saying it took it.
//
// The body is carried as an object rather than as the bytes it arrived in, so that this call can
// refuse something that is not a configuration before it spends a request on it: a body that is an
// array or a string would be answered by the firmware with an error this package would then have to
// explain, and the caller's mistake is the one worth naming.
//
// It does not wait for anything and it does not reboot unless the body says so. Both are the
// caller's decision, and both are measured facts rather than defaults: see the package comment.
func (c *Client) WriteConfig(ctx context.Context, body json.RawMessage) error {
	var payload map[string]json.RawMessage

	if err := json.Unmarshal(body, &payload); err != nil {
		return fmt.Errorf("wled: the configuration to write is not an object: %w", err)
	}

	if len(payload) == 0 {
		return errors.New("wled: the configuration to write carries nothing")
	}

	var answer struct {
		Success *bool `json:"success"`
	}

	if err := c.do(ctx, http.MethodPost, pathConfig, payload, &answer); err != nil {
		return err
	}

	if answer.Success == nil || !*answer.Success {
		return fmt.Errorf("%w: %s%s answered without success", ErrConfigRefused, c.base, pathConfig)
	}

	return nil
}
