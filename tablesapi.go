// The two editor surfaces the screens call: the state table the Colours screen edits, and the
// moment table the Events screen edits.
//
// # Why they are routes and not settings
//
// AGENTS.md §5.8 is the short answer — a manifest setting cannot hold a table — and
// docs/product-requirements.md §4 is the longer one: the module owns its configuration, the
// settings pane is no longer the editing surface, and the store beside the installed module is
// where an edit is kept. So these routes are the writing half of store.go, and they follow the
// conventions bindingsapi.go already set: one answer object per row, an origin on every value a
// person can change, a registered code for every refusal, and a note on every write saying what a
// write is and is not.
//
// # What a write here does
//
// It does not touch the light itself, and it does not stop the watch: nothing on this surface opens
// a socket to a controller, which is why every one of these writes works while a lamp is unplugged.
// What a colour and a word do is reach the painter on its next pass, because the writer reads this
// store on every pass of its own loop rather than keeping a copy from start-up — so an edit lands on
// the light within a tick, and a module that restarted between two edits reads the same answer.
//
// A moment is still a record only: one moment fires today, and the Events screen's table is not yet
// obeyed. That is said in the note of a moment's write rather than left for somebody to discover by
// looking at a lamp that did not change.
//
// # Why the store is read on every request
//
// Because it is one small file and because the alternative is a copy of it in memory that a second
// writer — a person with an editor, a later release, the next wave's routes — could put out of
// date without this process noticing. Every read is the file, and every write is a read, a change
// and a rename under one lock.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/prusa3d/prusa-connect/sdk/fault"
	"github.com/prusa3d/prusa-connect/sdk/routesapi"
)

// The notes every write on these routes carries, because the two things a caller must not be
// wrong about are whether a change survives and what it changes.
const (
	// persistedNote is what a write to this module's own store is worth: it outlives a restart
	// and an upgrade, and it does not survive a purge. That is §4's rule and the one difference
	// from an ordinary Connect module that the README has to state.
	persistedNote = "This was written into this module's own configuration store, the config.json in the directory " +
		"the host granted, so it survives a restart and an upgrade. A module purge or a fresh install loses it."

	// statesDrawnNote says what a state's colour and word now do on the light, and what a state nobody
	// has edited still does. It is the note that would be false if the painter stopped reading this
	// table, so it is written as the rule rather than as a change.
	statesDrawnNote = "A state's colour is the colour its light is drawn in, and a word a state carries is what a panel " +
		"shows for as long as its hold lasts: the word beats the Layout composer, and a strip - which cannot draw " +
		"a word - shows the state's colour instead. A state nobody has edited keeps the colour internal/render " +
		"holds for it, and holds it as a default rather than as a copy written into this store."

	// momentsFiredNote says the same about the moment table: every moment in it is fired, on its own
	// transition, for the length it names.
	momentsFiredNote = "Every moment in this table is fired on its transition, on every binding whose printer sits on " +
		"that light, and held for the length its `for` names: a preset takes the whole light and puts it back, a word " +
		"is drawn on a panel and becomes the moment's colour on a strip, a colour is painted as the whole light, and " +
		"nothing does nothing. A moment with no row behaves as its default does, which is the finished word for a " +
		"minute and nothing for the rest."

	// noStoreNote is what a read says when there is nothing to write to, so that a screen can say
	// why its save button is off rather than leaving a person to discover it by pressing it.
	noStoreNote = "This module was granted no data directory, or its store has not been written yet, so a change here " +
		"could not be kept. What you are reading is the table this build holds."
)

// What reading the store can answer with, kept as values rather than sentences so that a handler
// answers by asking the error what it is.
var (
	// errNoDataDirectory is a module the operator did not grant a data directory. It runs from
	// its settings exactly as it did before the store existed, so there is nowhere to keep an
	// override and nothing to read one from.
	errNoDataDirectory = errors.New("this module was granted no data directory, so it has no configuration store to keep a change in")

	// errNoDocument is a granted directory with no document in it, which is a start that has not
	// finished rather than a table with nothing in it. A read answers the defaults; a write
	// refuses, because the document is written by the start and a route that wrote one would be
	// writing a bindings list it does not own.
	errNoDocument = errors.New("this module's configuration store has not been written yet")
)

// configTables is this module's own configuration as the routes that edit it reach it: the
// directory the host granted, and a lock around the read-change-rename a write is.
//
// It holds no copy of the document. The file is the record, and a copy in memory would be a second
// record that goes stale the first time something else writes the file.
type configTables struct {
	mu  sync.Mutex
	dir string

	// generation counts the writes this process has made to the document. It exists for the
	// painter: the writer reads the store on its own loop, and a counter is how it can tell
	// "nothing has changed" from "somebody wrote" without comparing file contents every second.
	// A write from outside this process — a person with an editor on the volume — is caught by the
	// file's own modification time instead, which the two together cover.
	generation uint64
}

// stamp is where the document is and how many writes this process has made to it, read under the
// lock so that a reader cannot see a directory and a counter from either side of a change.
func (t *configTables) stamp() (string, uint64) {
	t.mu.Lock()
	defer t.mu.Unlock()

	return t.dir, t.generation
}

// grant records the directory the host gave this module, which arrives once on plugin.configure.
func (t *configTables) grant(dir string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.dir = strings.TrimSpace(dir)
	t.generation++
}

// directory is the directory the host granted, and empty when it granted none.
//
// It is here rather than in a field of its own on the Devices routes so that there is one answer to
// where this module's files live: the document and the snapshots are both in it, and two copies of
// the answer would be two things to keep in step.
func (t *configTables) directory() string {
	t.mu.Lock()
	defer t.mu.Unlock()

	return t.dir
}

// load is the document as it stands, read from the granted directory.
func (t *configTables) load() (storedConfig, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	return t.loadLocked()
}

// loadLocked is load with the lock already held.
func (t *configTables) loadLocked() (storedConfig, error) {
	if t.dir == "" {
		return storedConfig{}, errNoDataDirectory
	}

	found, err := loadStore(t.dir)
	if err != nil {
		if errors.Is(err, errNoStore) {
			return storedConfig{}, errNoDocument
		}

		return storedConfig{}, err
	}

	return found.Config, nil
}

// revise reads the document, lets one change amend it, and writes it back under the same lock.
//
// It is a read-modify-write of the whole file and not a patch of one member, so that whatever else
// the document holds — the bindings above all — comes back exactly as it was found. A route here
// that lost a binding line would be a light that stopped showing a printer for a reason nobody
// could reconstruct.
//
// The change cannot fail: every rule is judged before revise is called, so a revision that is
// reached has already been refused or accepted on its own terms. What is left is the file.
func (t *configTables) revise(change func(*storedConfig)) (storedConfig, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	config, err := t.loadLocked()
	if err != nil {
		return storedConfig{}, err
	}

	change(&config)

	if _, err := saveStore(t.dir, config); err != nil {
		return storedConfig{}, err
	}

	// The document on disk is not the one the painter last read.
	t.generation++

	return config, nil
}

// tableAPI is the two tables' routes, and the two things they reach: the watch — for the light's
// own bindings — the host's printer list for the names a screen prints, and the store.
//
// It is a value with its own fields for the reason bindingAPI is: a test runs the same handlers
// against a watch built on fakes and a directory of its own, and needs no host and no controller.
type tableAPI struct {
	live *runner

	// fleet is the host's printer list, for the names a screen prints beside a device id.
	fleet func() devices

	// tables is the store the two tables live in.
	tables *configTables

	// bound is how long one read of the printer list may take, and zero means resolveTimeout.
	bound time.Duration

	// clock is the clock a pause and a brightness window are judged against, and nil means
	// time.Now. A test hands it a fixed instant, because "is this window in force at 20:00" is a
	// question with one right answer and running the test at whatever time it happened to start is
	// not how to ask it.
	clock func() time.Time

	// zone is the IANA zone the brightness windows are judged in and a clock is written in, and nil
	// means this module's own store (zoneName, wave3api.go). It is a field so that a test can hand a
	// zone over instead of writing a document, and so that a test about a module with no zone can say
	// so with one word. The zone route does not read it: its subject is the document.
	zone func() string
}

// timeout is how long one read of the host's printer list may take.
func (api tableAPI) timeout() time.Duration {
	if api.bound > 0 {
		return api.bound
	}

	return resolveTimeout
}

// mount adds the two tables' routes to mux.
func (api tableAPI) mount(mux *http.ServeMux) {
	// The state table: one row per state the light can be in, and the three things a person can
	// change about it.
	mux.HandleFunc("GET /states", api.listStates)
	mux.HandleFunc("GET /states/{state}", api.readState)
	mutating(mux, "PUT /states/{state}", actionConfigureResource, api.updateState)
	mutating(mux, "DELETE /states/{state}", actionConfigureResource, api.deleteState)

	// The moment table: one light's own moments, each with one entry per printer on it.
	mux.HandleFunc("GET /lights/{light}/moments", api.listMoments)
	mux.HandleFunc("GET /lights/{light}/moments/{moment}", api.readMoment)
	mutating(mux, "PUT /lights/{light}/moments/{moment}", actionConfigureResource, api.updateMoment)
	mutating(mux, "DELETE /lights/{light}/moments/{moment}", actionConfigureResource, api.deleteMoment)

	// The layout, the brightness windows and the pause: the three surfaces wave 3 added. They are
	// mounted here because they are the same store and the same watch; what they are for is in
	// wave3api.go.
	api.mountWave3(mux)

	// And the zone those windows are judged in, which schema version 9 moved out of the manifest and
	// into this store. It is mounted here for the same reason: one document, one set of routes.
	api.mountTimezone(mux)
}

// document is the store as a read sees it: the document, and whether there is one to write to.
//
// A module granted no directory, and a module whose first start has not finished writing its
// document, both answer the defaults rather than a refusal: what the Colours screen shows before
// anybody edits it is Appendix A's table, and that is true whether or not a file exists. What
// those two cases cannot do is keep a change, which is what `persisted: false` says and what a
// write refuses.
func (api tableAPI) document() (storedConfig, bool, error) {
	config, err := api.tables.load()

	switch {
	case errors.Is(err, errNoDataDirectory), errors.Is(err, errNoDocument):
		return storedConfig{Version: storeVersion}, false, nil
	case err != nil:
		return storedConfig{}, false, err
	}

	return config, true, nil
}

// stateAnswer is one state as a caller reads it: what the state is, the three things a person can
// change, and where each of the three came from.
//
// The origin beside each value is the whole point of the shape. A screen that showed a colour
// without saying whether it is the table's or the store's could not offer the right button: there
// is nothing to reset on a default, and a default shown as though it were an override is a change
// a person believes he made.
type stateAnswer struct {
	State  string     `json:"state"`
	Kind   string     `json:"kind"`
	Colour colourView `json:"colour"`

	// ColourOrigin, WordOrigin and HoldsOrigin are `store` or `default`, one per value.
	ColourOrigin string `json:"colour_origin"`
	Word         string `json:"word"`
	WordOrigin   string `json:"word_origin"`
	Holds        string `json:"holds"`
	HoldsOrigin  string `json:"holds_origin"`

	// WordShows is what the word is doing at the moment this answer was written, and WordUntil is
	// the instant it stops — the two facts a write's success was silent about before schema
	// version 5. A caller who set a word on a printer that finished hours ago was told 200 and
	// watched a light that never changed, so the answer now says which of these it is:
	//
	//	none     the state carries no word
	//	showing  a light is showing this state and the word is on it; WordUntil is when it stops,
	//	         and empty for a word of forever
	//	expired  a light is showing this state and the hold has run out: nothing is showing the
	//	         word, and it will not until it is written again
	//	later    no light is showing this state; the word runs from the moment one enters it
	WordShows string `json:"word_shows"`

	// WordUntil is the last instant the word is showing on any light, in UTC, and empty when
	// nothing is counting down.
	WordUntil string `json:"word_until,omitempty"`

	// Overridden is true when any of the three came from the store, which is what decides whether
	// a screen offers "reset to the default".
	Overridden bool `json:"overridden"`

	// Meaning is what the state means in one line, so that the screen does not have to hold a
	// second copy of Appendix A's prose.
	Meaning string `json:"meaning"`
}

// What a state's word may be doing at the moment an answer is written. See stateAnswer.WordShows.
const (
	wordShowsNone    = "none"
	wordShowsShowing = "showing"
	wordShowsExpired = "expired"
	wordShowsLater   = "later"
)

// wordStanding is one state's word at one instant: what it is doing, when it stops, and how many
// lights are showing it.
type wordStanding struct {
	shows  string
	until  time.Time
	lights int
}

// wordStandingOf answers what one state's word is doing at an instant.
//
// It is the arithmetic the painter does, asked of the same facts: the hold the row carries, the
// moment the word was written, and the moment each light began showing the state. A light is the
// only thing that knows the last of those, so the answer comes from what the writers published
// (see stateStarts) rather than from a second clock or a guess.
//
// The word counts as showing while any light is showing it: a hold is per light because a state
// begins per binding, and an install whose two lights entered the state a minute apart has one
// light still holding the word and one that has moved on.
func wordStandingOf(row stateRow, at time.Time) wordStanding {
	word, _ := row.word()
	if word == "" {
		return wordStanding{shows: wordShowsNone}
	}

	starts := stateStarts(row.definition.name)
	if len(starts) == 0 {
		return wordStanding{shows: wordShowsLater}
	}

	holds, _ := row.holds()
	standing := wordStanding{shows: wordShowsExpired}

	for _, since := range starts {
		until := wordUntil(holds, since, row.setAt())
		if !until.IsZero() && !at.Before(until) {
			continue
		}

		standing.shows = wordShowsShowing
		standing.lights++

		if until.After(standing.until) {
			standing.until = until
		}
	}

	return standing
}

// wordStandingNote is what a write's answer says about the word it just wrote: the sentence that
// turns "200, and nothing happened" into something a person can act on.
//
// It is the prose beside stateAnswer.WordShows, and it exists because the two facts a caller needs
// are not the same fact. A word that is showing is a success with a length; a word that has expired
// is a success worth nothing, and saying so is the whole reason this was written. The fourth case —
// a state no light is showing — is not a failure at all: the word waits for its state, which is
// what a state's word is for.
func wordStandingNote(row stateRow, at time.Time) string {
	word, _ := row.word()
	if word == "" {
		return ""
	}

	standing := wordStandingOf(row, at)

	switch standing.shows {
	case wordShowsShowing:
		if standing.until.IsZero() {
			return fmt.Sprintf("The word %q is on the light now and holds until the state moves. ", word)
		}

		return fmt.Sprintf("The word %q is on the light now, for %d more second(s), until %s. ",
			word, int(standing.until.Sub(at)/time.Second), standing.until.UTC().Format(time.RFC3339))

	case wordShowsExpired:
		return fmt.Sprintf("The word %q has already expired: the %s state is being shown and its hold ran out, "+
			"so nothing is showing it and nothing will until it is written again. ", word, row.definition.name)

	case wordShowsLater:
		return fmt.Sprintf("The word %q is not on the light now: no light is showing %s, and a state's word runs "+
			"from the moment a light enters that state. ", word, row.definition.name)
	}

	return ""
}

// stateAnswerOf renders one row of the state table as it stands at one instant.
func stateAnswerOf(row stateRow, at time.Time) stateAnswer {
	colour, colourOrigin := row.colour()
	word, wordOrigin := row.word()
	holds, holdsOrigin := row.holds()

	standing := wordStandingOf(row, at)

	until := ""
	if !standing.until.IsZero() {
		until = standing.until.UTC().Format(time.RFC3339)
	}

	return stateAnswer{
		State:        row.definition.name,
		Kind:         string(row.definition.kind),
		Colour:       colourView{R: colour.R, G: colour.G, B: colour.B},
		ColourOrigin: colourOrigin,
		Word:         word,
		WordOrigin:   wordOrigin,
		Holds:        holds,
		HoldsOrigin:  holdsOrigin,
		WordShows:    standing.shows,
		WordUntil:    until,
		Overridden:   row.overridden(),
		Meaning:      row.definition.meaning,
	}
}

// statesAnswer is the whole table, and what a caller has to know to read it.
type statesAnswer struct {
	States []stateAnswer `json:"states"`
	Count  int           `json:"count"`

	// Conditions is how many of Count are conditions rather than states the host reports. The
	// screen's heading says "ten states", and a caller that counted twelve and could not tell why
	// would be looking at a defect that is not there.
	Conditions int `json:"conditions"`

	// Persisted says whether a change here would be kept: false for a module granted no data
	// directory, or one whose document has not been written yet. Note says why.
	Persisted bool   `json:"persisted"`
	Note      string `json:"note,omitempty"`

	// UnknownStates names stored rows about states this build no longer has, so that a document
	// from a newer release is visible rather than silently narrowed.
	UnknownStates []string `json:"unknown_states,omitempty"`
}

// listStates is GET /states: every state the light can be in, with the value in force and where it
// came from.
func (api tableAPI) listStates(w http.ResponseWriter, r *http.Request) {
	config, available, err := api.document()
	if err != nil {
		refuse(w, r, StoreUnavailable.New(err.Error()))

		return
	}

	rows, err := rowsOf(config)
	if err != nil {
		refuse(w, r, StoreUnavailable.New("this module's configuration store cannot be acted on: "+err.Error()))

		return
	}

	at := api.now()

	out := make([]stateAnswer, 0, len(rows))
	for _, row := range rows {
		out = append(out, stateAnswerOf(row, at))
	}

	said := statesAnswer{
		States:        out,
		Count:         len(out),
		Conditions:    len(stateNamesOf(stateCondition)),
		Persisted:     available,
		UnknownStates: unknownStates(config),
	}

	if !available {
		said.Note = noStoreNote
	}

	answer(w, http.StatusOK, said)
}

// stateNotFound is the refusal for a name the state table does not hold, listing the ones it does.
func stateNotFound(named string) *fault.Fault {
	return StateNotFound.New(fmt.Sprintf("There is no state called %q. The table holds %s.",
		strings.TrimSpace(named), strings.Join(stateNames(), ", "))).
		With(fault.StringParam("state", strings.TrimSpace(named)))
}

// readState is GET /states/{state}: one state.
func (api tableAPI) readState(w http.ResponseWriter, r *http.Request) {
	config, available, err := api.document()
	if err != nil {
		refuse(w, r, StoreUnavailable.New(err.Error()))

		return
	}

	rows, err := rowsOf(config)
	if err != nil {
		refuse(w, r, StoreUnavailable.New("this module's configuration store cannot be acted on: "+err.Error()))

		return
	}

	row, known := rowNamed(rows, r.PathValue("state"))
	if !known {
		refuse(w, r, stateNotFound(r.PathValue("state")))

		return
	}

	said := map[string]any{"state": stateAnswerOf(row, api.now()), "persisted": available}

	if !available {
		said["note"] = noStoreNote
	}

	answer(w, http.StatusOK, said)
}

// stateRequest is what PUT /states/{state} accepts: any of the three things a person can change.
//
// Every field is a pointer because absent is not zero here. A colour of 0,0,0 is black and a hold
// of `forever` is a decision; an absent colour is "no colour of mine", which is the default. A body
// that carries none of the three is refused rather than read as "clear everything", because
// clearing is what DELETE is for and a mistyped key would otherwise reset a row in silence.
type stateRequest struct {
	Colour *storedColour `json:"colour"`
	Word   *string       `json:"word"`
	Holds  *string       `json:"holds"`
}

// readStateRequest is one request body, and what is wrong with it.
func readStateRequest(r *http.Request) (stateRequest, error) {
	var sent stateRequest

	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(&sent); err != nil {
		return sent, fmt.Errorf(
			"the body is not a state's row: %w: write colour, word or holds, and nothing else", err)
	}

	if sent.Colour == nil && sent.Word == nil && sent.Holds == nil {
		return sent, errors.New(
			"the body carries none of colour, word and holds: write at least one, or delete the state's override to put it back to its default")
	}

	return sent, nil
}

// override is the row this request writes, for the state it names, at the moment it is written.
//
// The moment is stamped on every row that carries a word, and it is the whole of the fix for a
// hold that was spent before anybody read it: the painter measures a word's hold from the later of
// this instant and the moment the state began (see wordUntil), so a word written onto a state
// that has been running for hours is shown for its full length from now. A row without a word
// carries no moment, because there would be nothing for it to mean.
func (sent stateRequest) override(state string, at time.Time) (storedState, error) {
	one := storedState{State: state, Colour: sent.Colour, Holds: sent.Holds}

	if sent.Colour != nil && !sent.Colour.inRange() {
		return storedState{}, fmt.Errorf(
			"colour is %d,%d,%d and a channel is 0-255", sent.Colour.R, sent.Colour.G, sent.Colour.B)
	}

	if sent.Holds != nil {
		if err := checkHolds(*sent.Holds); err != nil {
			return storedState{}, err
		}
	}

	// A word is drawn, so its surrounding spaces are not part of it: a leading space is a blank
	// glyph column on the panel and a trailing one is a word that looks right and is not. An empty
	// word means the state carries none, which is the default, so it is not written as though it
	// were a decision.
	if sent.Word != nil {
		if word := strings.TrimSpace(*sent.Word); word != "" {
			one.Word = &word
			one.SetAt = &at
		}
	}

	return one, nil
}

// refusalOf is the registered refusal for a body this table will not write. It carries the state
// the body was about, and — when the hold is the value that was refused — the hold as it was
// written: a registered code declares the values its sentence names, and a client reads them there
// rather than parsing the sentence.
func (sent stateRequest) refusalOf(state string, err error) *fault.Fault {
	raised := StateRefused.New(err.Error()).With(fault.StringParam("state", state))

	if sent.Holds != nil && checkHolds(*sent.Holds) != nil {
		raised = raised.With(fault.StringParam("holds", *sent.Holds))
	}

	return raised
}

// updateState is PUT /states/{state}: the state's colour, word and hold, as the body writes them.
//
// It replaces the whole row: a field the body leaves out goes back to its default, which is what a
// PUT means and what the screen's one form for all three fields does anyway.
func (api tableAPI) updateState(w http.ResponseWriter, r *http.Request) {
	definition, known := findState(r.PathValue("state"))
	if !known {
		refuse(w, r, stateNotFound(r.PathValue("state")))

		return
	}

	sent, err := readStateRequest(r)
	if err != nil {
		refuse(w, r, StateBodyInvalid.New(err.Error()))

		return
	}

	// One clock for the write and for its answer: the moment stamped on the row is the moment the
	// answer judges the word at, so "it shows until 12:01" and a hold that begins at 12:00 cannot
	// be a second apart in the same reply.
	at := api.now()

	one, err := sent.override(definition.name, at)
	if err != nil {
		refuse(w, r, sent.refusalOf(definition.name, err))

		return
	}

	config, err := api.tables.revise(func(config *storedConfig) { setState(config, one) })
	if err != nil {
		refuse(w, r, StoreUnavailable.New(err.Error()))

		return
	}

	rows, err := rowsOf(config)
	if err != nil {
		refuse(w, r, StoreUnavailable.New("this module's configuration store cannot be acted on: "+err.Error()))

		return
	}

	row, _ := rowNamed(rows, definition.name)

	answer(w, http.StatusOK, map[string]any{
		"state":     stateAnswerOf(row, at),
		"applied":   true,
		"persisted": true,
		"note":      wordStandingNote(row, at) + persistedNote + " " + statesDrawnNote,
	})
}

// deleteState is DELETE /states/{state}: the state goes back to the colour Appendix A gives it, no
// word, and a hold of forever.
//
// A state with no override is not a refusal. There is nothing to delete, and the answer says so
// with `removed: false` beside the default it was already showing, because a screen that had to
// treat "already at the default" as an error would be telling a person something went wrong when
// nothing did.
func (api tableAPI) deleteState(w http.ResponseWriter, r *http.Request) {
	definition, known := findState(r.PathValue("state"))
	if !known {
		refuse(w, r, stateNotFound(r.PathValue("state")))

		return
	}

	var removed bool

	config, err := api.tables.revise(func(config *storedConfig) {
		removed = withoutState(config, definition.name)
	})
	if err != nil {
		refuse(w, r, StoreUnavailable.New(err.Error()))

		return
	}

	rows, err := rowsOf(config)
	if err != nil {
		refuse(w, r, StoreUnavailable.New("this module's configuration store cannot be acted on: "+err.Error()))

		return
	}

	row, _ := rowNamed(rows, definition.name)

	answer(w, http.StatusOK, map[string]any{
		"state":     stateAnswerOf(row, api.now()),
		"removed":   removed,
		"persisted": true,
		"note":      persistedNote,
	})
}

// momentEntryAnswer is one printer's entry for one moment, with what the light it hangs on gives
// the screen to draw beside it — the segment, and whether that segment is the whole light.
type momentEntryAnswer struct {
	Printer     string `json:"printer"`
	PrinterName string `json:"printer_name,omitempty"`
	Segment     int    `json:"segment"`
	Whole       bool   `json:"whole"`

	Enabled bool   `json:"enabled"`
	Shows   string `json:"shows"`
	Which   string `json:"which"`
	For     string `json:"for"`

	// Origin is `store` or `default`: `default` means nobody has edited this moment on this
	// printer on this light, and what is shown is what a delete would put back.
	Origin string `json:"origin"`
}

// momentAnswer is one moment as a caller reads it: the moment, and one entry per printer on the
// light. One printer is one entry, which is the flat table the screen draws for a light carrying a
// single printer.
type momentAnswer struct {
	Moment   string              `json:"moment"`
	Label    string              `json:"label"`
	Meaning  string              `json:"meaning"`
	Entries  []momentEntryAnswer `json:"entries"`
	Printers int                 `json:"printer_count"`
}

// momentsAnswer is one light's whole moment table.
type momentsAnswer struct {
	Light   string `json:"light"`
	Address string `json:"address"`

	// Printers is every printer on this light, in the operator's order, in the same shape
	// GET /bindings carries — which is what the screen's indented sub-rows are labelled from.
	Printers []bindingAnswer `json:"printers"`

	Moments []momentAnswer `json:"moments"`
	Count   int            `json:"count"`

	Persisted      bool     `json:"persisted"`
	Note           string   `json:"note,omitempty"`
	UnknownMoments []string `json:"unknown_moments,omitempty"`
}

// lightOf is the controller one light name asks for, and false for a name this module does not
// drive.
//
// A light is a controller on these routes and is named the way GET /controllers names one: its
// address without the scheme, so that the name is one path segment. There is no second noun with a
// second naming rule, which would be a second thing for a person to get wrong.
func (api tableAPI) lightOf(name string, set bindings) (controllerAnswer, bool, error) {
	found, err := controllersOf(set)
	if err != nil {
		return controllerAnswer{}, false, err
	}

	for _, one := range found {
		if one.Name == name {
			return one, true, nil
		}
	}

	return controllerAnswer{}, false, nil
}

// unknownLight is the refusal for a light name this module does not drive, listing the ones it
// does. It is the controller's own code, because a light here is a controller: one fact, one code.
//
// The list is the caller's own: a light this module drives only for printers the caller may not see
// is not named here, because naming it would be telling them about rows they cannot read.
func (api tableAPI) unknownLight(named string, set bindings) *fault.Fault {
	found, _ := controllersOf(set)

	names := make([]string, 0, len(found))
	for _, one := range found {
		names = append(names, one.Name)
	}

	return ControllerNotFound.New(fmt.Sprintf(
		"This module drives no light of that name. It drives %s.", strings.Join(names, ", "))).
		With(fault.StringParam("controller", strings.TrimSpace(named)))
}

// momentNotFound is the refusal for a moment this build does not have, listing the eight it does.
func momentNotFound(named string) *fault.Fault {
	return MomentNotFound.New(fmt.Sprintf("There is no moment called %q. Every light has %s.",
		strings.TrimSpace(named), momentsDescribed())).
		With(fault.StringParam("moment", strings.TrimSpace(named)))
}

// entriesOf is one light's entries for one moment, per printer on that light, in the operator's
// order. It is the one place a moment's rows are assembled, so that a read of one moment and a read
// of the whole table cannot describe it two ways.
//
// It walks the light's own bindings rather than the device ids the controller entry carries, so
// that each entry says which segment that printer is on and whether it is the whole light — the two
// things the screen's sub-rows are labelled with.
func entriesOf(config storedConfig, light controllerAnswer, set bindings, definition momentDefinition, names map[string]string) ([]momentEntryAnswer, error) {
	on := bindingsOn(set, light)

	entries := make([]momentEntryAnswer, 0, len(on))

	for _, printer := range on {
		rows, err := momentsOn(config, light.Name, printer.DeviceID)
		if err != nil {
			return nil, err
		}

		for _, row := range rows {
			if row.definition.id == definition.id {
				entries = append(entries, momentEntryAnswerOf(row, printer, names))
			}
		}
	}

	return entries, nil
}

// momentEntryAnswerOf renders one moment's entry for one printer.
func momentEntryAnswerOf(row momentRowOn, printer binding, names map[string]string) momentEntryAnswer {
	return momentEntryAnswer{
		Printer:     printer.DeviceID,
		PrinterName: names[printer.DeviceID],
		Segment:     printer.Segment,
		Whole:       printer.Segment < 0,
		Enabled:     row.entry.Enabled,
		Shows:       row.entry.Shows,
		Which:       row.entry.Which,
		For:         row.entry.For,
		Origin:      row.origin(),
	}
}

// bindingsOn is the bindings one light carries, in the operator's order.
//
// It compares controller keys and not addresses, so that the two spellings of one address a person
// actually types — with and without a trailing slash — are one light here as they are everywhere
// else.
func bindingsOn(set bindings, light controllerAnswer) bindings {
	held := make(bindings, 0, len(light.Printers))

	for _, b := range set {
		if key, err := controllerKey(b.Address); err == nil && key == light.Key {
			held = append(held, b)
		}
	}

	return held
}

// visibleSet is the caller's printers, and the running set narrowed to them.
//
// It is the one thing a table route asks before it answers: a moment's rows, a layout's rows and a
// light's own name are all about printers, so the set they are read from is the caller's and not
// the module's. It writes the refusal itself, the way seenOrRefuse does. See visible.go.
func (api tableAPI) visibleSet(w http.ResponseWriter, r *http.Request) (visible, bindings, bool) {
	seen, ok := seenOrRefuse(w, r, api.fleet, api.timeout())
	if !ok {
		return visible{}, nil, false
	}

	return seen, seen.only(api.live.set()), true
}

// listMoments is GET /lights/{light}/moments: every moment that light has, each with its entry for
// every printer on it — the printers the caller may see, and no others.
func (api tableAPI) listMoments(w http.ResponseWriter, r *http.Request) {
	seen, set, ok := api.visibleSet(w, r)
	if !ok {
		return
	}

	light, found, err := api.lightOf(r.PathValue("light"), set)
	if err != nil {
		refuse(w, r, BindingRefused.New(err.Error()))

		return
	}

	if !found {
		refuse(w, r, api.unknownLight(r.PathValue("light"), set))

		return
	}

	config, available, err := api.document()
	if err != nil {
		refuse(w, r, StoreUnavailable.New(err.Error()))

		return
	}

	names := namesOf(seen)

	moments := make([]momentAnswer, 0, len(momentTable))

	for _, definition := range momentTable {
		entries, err := entriesOf(config, light, set, definition, names)
		if err != nil {
			refuse(w, r, StoreUnavailable.New("this module's configuration store cannot be acted on: "+err.Error()))

			return
		}

		moments = append(moments, momentAnswer{
			Moment:   definition.id,
			Label:    definition.label,
			Meaning:  definition.meaning,
			Entries:  entries,
			Printers: len(entries),
		})
	}

	said := momentsAnswer{
		Light:          light.Name,
		Address:        light.Address,
		Printers:       answersOf(bindingsOn(set, light), names),
		Moments:        moments,
		Count:          len(moments),
		Persisted:      available,
		UnknownMoments: unknownMoments(config),
	}

	if !available {
		said.Note = noStoreNote
	}

	answer(w, http.StatusOK, said)
}

// readMoment is GET /lights/{light}/moments/{moment}: one moment, with one entry per printer the
// caller may see.
func (api tableAPI) readMoment(w http.ResponseWriter, r *http.Request) {
	seen, set, ok := api.visibleSet(w, r)
	if !ok {
		return
	}

	light, found, err := api.lightOf(r.PathValue("light"), set)
	if err != nil {
		refuse(w, r, BindingRefused.New(err.Error()))

		return
	}

	if !found {
		refuse(w, r, api.unknownLight(r.PathValue("light"), set))

		return
	}

	definition, known := findMoment(r.PathValue("moment"))
	if !known {
		refuse(w, r, momentNotFound(r.PathValue("moment")))

		return
	}

	config, available, err := api.document()
	if err != nil {
		refuse(w, r, StoreUnavailable.New(err.Error()))

		return
	}

	names := namesOf(seen)

	entries, err := entriesOf(config, light, set, definition, names)
	if err != nil {
		refuse(w, r, StoreUnavailable.New("this module's configuration store cannot be acted on: "+err.Error()))

		return
	}

	said := map[string]any{
		"light": light.Name,
		"moment": momentAnswer{
			Moment:   definition.id,
			Label:    definition.label,
			Meaning:  definition.meaning,
			Entries:  entries,
			Printers: len(entries),
		},
		"persisted": available,
	}

	if !available {
		said["note"] = noStoreNote
	}

	answer(w, http.StatusOK, said)
}

// momentRequest is what PUT /lights/{light}/moments/{moment} accepts: one printer's entry.
//
// Enabled is a pointer so that a body which forgets it is refused rather than switching a moment
// off in silence — the one mistake a whole-row replace makes easy, and the one nobody would notice
// until a print finished and the light did nothing.
type momentRequest struct {
	Printer string  `json:"printer"`
	Enabled *bool   `json:"enabled"`
	Shows   *string `json:"shows"`
	Which   *string `json:"which"`
	For     *string `json:"for"`
}

// readMomentRequest is one request body, and what is wrong with it.
func readMomentRequest(r *http.Request) (momentRequest, error) {
	var sent momentRequest

	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(&sent); err != nil {
		return sent, fmt.Errorf(
			"the body is not a moment's entry: %w: write enabled, shows, which and for, and printer when the light carries more than one", err)
	}

	if sent.Enabled == nil {
		return sent, errors.New("the body does not say whether the moment is on: write enabled, true or false")
	}

	if sent.Shows == nil {
		return sent, fmt.Errorf("the body does not say what the moment shows: write shows, one of %s",
			strings.Join(quoted(showsChoices), ", "))
	}

	return sent, nil
}

// entry is the entry this request asks for, with the two defaults a body may leave out.
func (sent momentRequest) entry() momentEntry {
	one := momentEntry{Enabled: *sent.Enabled, Shows: *sent.Shows, For: durationForever}

	if sent.Which != nil {
		one.Which = *sent.Which
	}

	if sent.For != nil {
		one.For = strings.TrimSpace(*sent.For)
	}

	return one
}

// printerOn is the printer one request names on one light, and why it cannot be the one asked for.
//
// An empty printer is the flat case and not a guess: a light carrying one printer has exactly one
// answer, and the screen's single-printer table does not send a name because there is nothing to
// choose. A light carrying several refuses the empty name and lists them, because picking one
// would be putting an operator's edit on a printer he did not name.
func printerOn(light controllerAnswer, named string) (binding, error) {
	wanted := strings.TrimSpace(named)

	if wanted == "" {
		if len(light.Printers) == 1 {
			return binding{DeviceID: light.Printers[0]}, nil
		}

		return binding{}, fmt.Errorf(
			"this light carries %d printers (%s), so name the one the moment is for in printer",
			len(light.Printers), strings.Join(light.Printers, ", "))
	}

	for _, id := range light.Printers {
		if id == wanted {
			return binding{DeviceID: id}, nil
		}
	}

	return binding{}, fmt.Errorf("%s is not a printer on the light %s: it carries %s",
		wanted, light.Name, strings.Join(light.Printers, ", "))
}

// namedPrinter puts the printer a caller named into a refusal, and leaves it out when the body named
// none: an empty parameter would be a value in the answer that nobody sent, and the sentence already
// says the light carries more than one.
func namedPrinter(raised *fault.Fault, named string) *fault.Fault {
	if wanted := strings.TrimSpace(named); wanted != "" {
		return raised.With(fault.StringParam("printer", wanted))
	}

	return raised
}

// printerBindingOn is the binding one device id has on one light, which is where its segment is:
// the entry a caller reads beside a moment needs the segment and whether it is the whole light.
func printerBindingOn(set bindings, light controllerAnswer, deviceID string) (binding, bool) {
	for _, one := range bindingsOn(set, light) {
		if one.DeviceID == deviceID {
			return one, true
		}
	}

	return binding{DeviceID: deviceID}, false
}

// updateMoment is PUT /lights/{light}/moments/{moment}: one printer's entry for one moment, on one
// light.
//
// The write does not reach the light, which is the rule that matters most here: an edit made while
// the lamp is unplugged must be kept, because the lamp being off is exactly when somebody is
// standing in front of it with a phone. A preset name is therefore accepted as written — see
// momenttable.go.
func (api tableAPI) updateMoment(w http.ResponseWriter, r *http.Request) {
	light, found, err := api.lightOf(r.PathValue("light"), api.live.set())
	if err != nil {
		refuse(w, r, BindingRefused.New(err.Error()))

		return
	}

	if !found {
		refuse(w, r, api.unknownLight(r.PathValue("light"), api.live.set()))

		return
	}

	definition, known := findMoment(r.PathValue("moment"))
	if !known {
		refuse(w, r, momentNotFound(r.PathValue("moment")))

		return
	}

	sent, err := readMomentRequest(r)
	if err != nil {
		refuse(w, r, MomentBodyInvalid.New(err.Error()))

		return
	}

	// The entry is judged before the printer is resolved, because what a moment shows does not
	// depend on which printer it goes to: a body with two mistakes in it should be answered about
	// the one a person can fix without looking at the Bindings screen.
	one := sent.entry()

	if err = checkEntry(one); err != nil {
		refuse(w, r, MomentRefused.New(err.Error()).With(fault.StringParam("moment", definition.id)))

		return
	}

	printer, err := printerOn(light, sent.Printer)
	if err != nil {
		refuse(w, r, namedPrinter(MomentRefused.New(err.Error()), sent.Printer))

		return
	}

	if _, err = api.tables.revise(func(config *storedConfig) {
		setMoment(config, storedMoment{
			Light:       light.Name,
			Printer:     printer.DeviceID,
			Moment:      definition.id,
			momentEntry: one,
		})
	}); err != nil {
		refuse(w, r, StoreUnavailable.New(err.Error()))

		return
	}

	names, _ := printerNames(r.Context(), api.fleet(), routesapi.PrincipalOf(r), api.timeout())

	held, _ := printerBindingOn(api.live.set(), light, printer.DeviceID)

	answer(w, http.StatusOK, map[string]any{
		"light": light.Name,
		"moment": momentEntryAnswerOf(
			momentRowOn{definition: definition, entry: one, stored: true}, held, names),
		"applied":   true,
		"persisted": true,
		"note":      persistedNote + " " + momentsFiredNote,
	})
}

// deleteMoment is DELETE /lights/{light}/moments/{moment}: one printer's entry goes back to the
// default for that moment.
//
// The printer is a query parameter rather than a fourth path segment, because the path already
// carries the light and the moment; and because a DELETE body is the one part of HTTP a proxy may
// drop. Absent, it is the light's only printer when the light carries one — see printerOn.
func (api tableAPI) deleteMoment(w http.ResponseWriter, r *http.Request) {
	light, found, err := api.lightOf(r.PathValue("light"), api.live.set())
	if err != nil {
		refuse(w, r, BindingRefused.New(err.Error()))

		return
	}

	if !found {
		refuse(w, r, api.unknownLight(r.PathValue("light"), api.live.set()))

		return
	}

	definition, known := findMoment(r.PathValue("moment"))
	if !known {
		refuse(w, r, momentNotFound(r.PathValue("moment")))

		return
	}

	named := strings.TrimSpace(r.URL.Query().Get("printer"))

	printer, err := printerOn(light, named)
	if err != nil {
		refuse(w, r, namedPrinter(MomentRefused.New(err.Error()), named))

		return
	}

	var removed bool

	if _, err = api.tables.revise(func(config *storedConfig) {
		removed = withoutMoment(config, light.Name, printer.DeviceID, definition.id)
	}); err != nil {
		refuse(w, r, StoreUnavailable.New(err.Error()))

		return
	}

	names, _ := printerNames(r.Context(), api.fleet(), routesapi.PrincipalOf(r), api.timeout())

	held, _ := printerBindingOn(api.live.set(), light, printer.DeviceID)

	answer(w, http.StatusOK, map[string]any{
		"light": light.Name,
		"moment": momentEntryAnswerOf(
			momentRowOn{definition: definition, entry: momentDefault(definition.id)}, held, names),
		"removed":   removed,
		"persisted": true,
		"note":      persistedNote,
	})
}
