// Running one of the controller's own presets: the one picture this module names rather than draws.
//
// # Why this exists, and what it is not
//
// A moment on the Events screen may name a preset the light already holds
// (docs/product-requirements.md §2.3, docs/design/v2/events/). A preset carries its own segments,
// brightness, effect, palette and LED map, and the light renders it: no DDP, no pixel arithmetic for
// the length of the moment, and the animation survives this module restarting because it lives on
// the light. The module's whole part is the name, the capture before it and the give-back after.
//
// So this file holds one call — run the preset called this — and the two facts it needs to turn a
// name into something WLED acts on. It is **not** a preset editor and there is no call here that
// creates, changes or deletes one: presets belong to WLED's own interface, where the effects and
// palettes actually live, and this package only reads the table to find the id a name belongs to.
//
// # Why the name is resolved here and not by the caller
//
// WLED's state write takes a preset by **id** (`{"ps": 3}`). The operator names one by the words
// WLED's interface shows him. Somewhere has to translate, and it has to be the place that already
// reads /presets.json — this package, in config.go — so that there is one reading of that document
// and one answer to "which preset is that". A caller holding an id of its own invention would be a
// second, staler copy of the light's own table.
//
// A name the light does not hold is refused with ErrNoSuchPreset rather than written: WLED accepts
// `{"ps": 99}` for a slot it does not have and draws nothing, which on a light the operator is
// looking at is indistinguishable from a moment that did nothing.
package wled

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

// ErrNoSuchPreset means the controller's own preset table holds no preset by that name.
var ErrNoSuchPreset = errors.New("wled: the controller holds no preset by that name")

// presetEntry is the part of one preset this package reads: its own name, which is the only string
// WLED's interface shows a person.
type presetEntry struct {
	Name string `json:"n"`
}

// presetTable is /presets.json as a map of id to preset.
//
// The ids are the object's keys rather than a field, which is why this is a map of strings: the
// document is `{"1": {...}, "2": {...}}` and the number a state write takes is the key.
type presetTable map[string]presetEntry

// RunPreset runs one of the controller's own presets, named as WLED's own interface spells it.
//
// The name is matched without regard to case and with its surrounding spaces trimmed, because it is
// typed by a person on the Events screen and copied from another window; nothing else is forgiven.
// A light holding two presets of one name answers with the lower id, so that two moments showing the
// same name show the same animation rather than whichever the map happened to yield.
func (c *Client) RunPreset(ctx context.Context, name string) error {
	wanted := strings.TrimSpace(name)
	if wanted == "" {
		return fmt.Errorf("%w: no name was given", ErrNoSuchPreset)
	}

	raw, err := c.Presets(ctx)
	if err != nil {
		return err
	}

	id, err := presetID(raw, wanted)
	if err != nil {
		return err
	}

	return c.do(ctx, http.MethodPost, pathState, writeState{Preset: &id}, nil)
}

// presetID is the id of the preset that carries this name, from the table as the controller answered
// with it.
func presetID(raw json.RawMessage, name string) (int, error) {
	var table presetTable

	if err := json.Unmarshal(raw, &table); err != nil {
		return 0, fmt.Errorf("wled: the preset table this controller answered with is not one: %w", err)
	}

	found := make([]int, 0, 1)

	for key, one := range table {
		if !strings.EqualFold(strings.TrimSpace(one.Name), name) {
			continue
		}

		id, convErr := strconv.Atoi(key)
		if convErr != nil {
			// A key that is not a number is not a preset id this firmware takes, and it is not a
			// refusal either: it is a slot this build cannot name.
			continue
		}

		found = append(found, id)
	}

	if len(found) == 0 {
		return 0, fmt.Errorf("%w: %q", ErrNoSuchPreset, name)
	}

	sort.Ints(found)

	return found[0], nil
}
