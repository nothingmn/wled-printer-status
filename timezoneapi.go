// The routes the Overview screen calls to read and write the zone: GET /timezone and PUT /timezone.
//
// # Why this is a route and not a setting
//
// docs/product-requirements.md §4 takes the configuration out of the manifest and puts it in this
// module's own store, edited through routes and this module's own pages. The zone is configuration in
// exactly that sense: it changes how a brightness window is judged and what a panel's clock reads, so a
// person changes it while the process runs and expects the change to hold. It also has to be *setatable
// on the day of install*, and with no `options` in the manifest there was no way to do that at all —
// which is why both consumers used to refuse on every install (timezone.go).
//
// # The two rules a caller must be able to read off this file
//
//   - **The store is the authority.** A read answers the document's zone, and the `timezone` setting
//     only for a module with no document at all — the migration source. A write refuses rather than
//     storing nothing when the module has no directory to keep it in.
//   - **Nothing is ever cleared.** An empty or unloadable zone is refused with a registered code, and
//     the document is left exactly as it was: a module whose clock went silent because somebody sent an
//     empty body is a light that stopped saying the time for a reason nobody can find.
//
// The browser capture is the first load's job and not this route's (ui/src/overview.svelte): a route
// that guessed a zone from a request header would be guessing with the server's own address, and this
// module's process runs in a container with no idea where the person looking at the page is. The body
// therefore carries which of the two wrote it — `browser` or `operator` — so that the document and the
// screen can both say where a value came from.
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// zoneRequest is what PUT /timezone accepts: one zone, and who wrote it.
//
// `source` is optional and defaults to `operator`. It is limited to the two ways a zone arrives after
// the migration — a browser's first-load capture and a person typing on the Overview screen — because
// the third, `settings`, is the upgrade's own and nothing may write it later.
type zoneRequest struct {
	Timezone string `json:"timezone"`
	Source   string `json:"source"`
}

// zoneAnswer is what both routes answer with: the zone in force, where it came from, how a stored one
// got there, and whether a change here could be kept.
//
// It is the same body for the read and the write, for the reason every window route answers the whole
// list: a screen redraws from one answer instead of reconciling a value against a record of what it
// sent. `persisted` is what tells it whether the field it is drawing can be saved at all, which is the
// difference between a module granted a data directory and one that was not.
type zoneAnswer struct {
	// Timezone is the zone in force, as the loader spells it, and empty when this module names none.
	Timezone string `json:"timezone"`

	// Origin is where that value came from: `store`, `settings` or `none`.
	Origin string `json:"origin"`

	// Source is how a stored zone got there: `browser`, `operator` or `settings`. It is present only
	// when Origin is `store`, and absent when the document does not say — a document a person edited
	// by hand can carry a zone with no origin, and the screen then says only that the store holds it.
	Source string `json:"source,omitempty"`

	// Stored says the zone is in this module's own document, which is what the screen reads as "do not
	// capture one from this browser".
	Stored bool `json:"stored"`

	// Persisted says a change here could be kept. False is a module the host granted no data
	// directory: it runs from its settings exactly as it did before the store existed.
	Persisted bool `json:"persisted"`

	// Note is the one sentence the screen shows under the field, and it says what the zone does rather
	// than that it saved.
	Note string `json:"note,omitempty"`
}

// zoneStoredNote is what a stored zone does, in one sentence on every answer that has one.
const zoneStoredNote = "A brightness window is judged in this zone and a panel's ends line writes its clock in it, " +
	"never in this process's own clock — inside the Prusa Connect container that is almost always UTC."

// zoneMissingNote is what an answer with no zone says, so that a screen can say why a window will not
// save rather than leaving a person to discover it by pressing the button.
const zoneMissingNote = "No zone is named, so no brightness window can be saved and a panel draws no clock: " +
	"a window or a clock read in this process's own clock would be hours out and nothing would say so."

// mountTimezone adds the two zone routes to mux.
func (api tableAPI) mountTimezone(mux *http.ServeMux) {
	mux.HandleFunc("GET /timezone", api.readTimezone)
	mutating(mux, "PUT /timezone", actionConfigureResource, api.writeTimezone)
}

// timezoneBody is the answer both routes make, from the document as it stands after the read or write.
//
// It reads the zone through zoneInForce and not through api.zoneName(), and that is deliberate: the
// `zone` field on tableAPI is a seam a test uses to name a zone without a store, while this route's
// whole subject is the store. A seam that answered here would let a test prove a capture against a
// zone no document holds.
func (api tableAPI) timezoneBody(config storedConfig, available bool) map[string]any {
	zone, origin := zoneInForce(config.Timezone, available)

	body := map[string]any{
		"timezone":  zone,
		"origin":    origin,
		"stored":    origin == zoneOriginStore,
		"persisted": available,
	}

	if origin == zoneOriginStore && config.TimezoneOrigin != "" {
		body["source"] = config.TimezoneOrigin
	}

	switch {
	case zone != "":
		body["note"] = zoneStoredNote
	case !available:
		body["note"] = noStoreNote + " " + zoneMissingNote
	default:
		body["note"] = zoneMissingNote
	}

	return body
}

// readTimezone is GET /timezone: the zone in force, and where it came from.
//
// A module with no store answers the setting's zone with an origin of `settings` and persisted false,
// and a module with a store that holds none answers an empty zone with an origin of `none`. Both are
// 200: neither is a fault, and the screen's next move is different in each case.
func (api tableAPI) readTimezone(w http.ResponseWriter, r *http.Request) {
	config, available, err := api.document()
	if err != nil {
		refuse(w, r, StoreUnavailable.New(err.Error()))

		return
	}

	answer(w, http.StatusOK, api.timezoneBody(config, available))
}

// writeTimezone is PUT /timezone: one zone stored, over whatever the document held.
//
// A zone that will not load is refused with the sentence naming an example, and nothing is written —
// not the zone, not the origin, not a blank. The read that follows a refusal is the document as it was,
// which is what the screen shows, so a failed save never renders as a saved one.
func (api tableAPI) writeTimezone(w http.ResponseWriter, r *http.Request) {
	sent, err := readZoneRequest(r)
	if err != nil {
		refuse(w, r, TimezoneBodyInvalid.New(err.Error()))

		return
	}

	named, err := checkZone(sent.Timezone)
	if err != nil {
		refuse(w, r, TimezoneRefused.New(err.Error()))

		return
	}

	source := sent.Source
	if source == "" {
		source = zoneOriginOperator
	}

	if source != zoneOriginBrowser && source != zoneOriginOperator {
		refuse(w, r, TimezoneBodyInvalid.New(fmt.Sprintf(
			"source is %q, and a zone written through this route came from a browser's first-load capture or from a person typing it: write %q or %q, or leave source out and it is %q",
			sent.Source, zoneOriginBrowser, zoneOriginOperator, zoneOriginOperator)))

		return
	}

	_, available, err := api.document()
	if err != nil {
		refuse(w, r, StoreUnavailable.New(err.Error()))

		return
	}

	if !available {
		refuse(w, r, StoreUnavailable.New(noStoreNote))

		return
	}

	var written storedConfig

	if written, err = api.tables.revise(func(config *storedConfig) {
		config.Timezone = named
		config.TimezoneOrigin = source
	}); err != nil {
		refuse(w, r, StoreUnavailable.New(err.Error()))

		return
	}

	body := api.timezoneBody(written, true)
	body["note"] = persistedNote + " " + zoneStoredNote

	answer(w, http.StatusOK, body)
}

// readZoneRequest is one request body, and what is wrong with it.
//
// The decoder is strict for the reason every other body on this surface is: a misspelt key that is
// ignored is a field left at its zero, and the zero here is an empty zone — a write refused with a
// sentence about a zone nobody sent, when what happened is that somebody typed `timezome`.
func readZoneRequest(r *http.Request) (zoneRequest, error) {
	var sent zoneRequest

	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(&sent); err != nil {
		return sent, fmt.Errorf("the body is not a zone: %w: write {\"timezone\": \"%s\"}, and nothing else but source",
			err, zoneExample)
	}

	return sent, nil
}
