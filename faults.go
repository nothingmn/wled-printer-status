// The codes this module refuses a caller with.
//
// A refusal a person or a client can see is a registered code, never a bare
// English sentence. The code is what a browser half switches on to choose a
// translation, and the English sentence travels beside it so a log line and an
// untranslated screen still read.
//
// Registering a code takes its documentation with it: what went wrong, what the
// caller should do, whether sending the same request again can work, and the
// parameters the sentence names. The sdk refuses a registration without them.
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/prusa3d/prusa-connect/sdk/fault"
)

// faultDomain is the first half of every code below. It is this module's
// namespace, which the host derived from the Go module path github.com/nothingmn/wled-printer-status, so no
// other author can register a code under it. The sdk refuses the core
// namespace, which is what keeps this module's codes apart from the server's.
//
//nolint:gochecknoglobals // a domain built once at start-up; a const cannot hold one.
var faultDomain = mustDomain("com.github.aux.nothingmn.wled-printer-status")

// The codes this module raises. Add a code here, and nowhere else, so the list
// a client branches on is one file.
//
//nolint:gochecknoglobals // definitions built once at start-up.
var (
	// RouteNotFound is a path this module does not serve.
	RouteNotFound = fault.Register(faultDomain, "ROUTE_NOT_FOUND", http.StatusNotFound, fault.Document(
		"This module does not serve that path",
		"Read openapi.json. Every path in it is relative to the mount the operator gave this module.",
		fault.RetryNever,
		fault.ShowDetail,
		"path",
	))

	// NoReadingYet is a status read that arrived before the first frame was
	// decided. It is transient by construction — the first reading is taken when the
	// watch starts, and a frame follows within a tick — and it exists so that the route
	// never answers with a frame of zeros, which a reader would take for a strip that is
	// black rather than for a module that has not looked yet.
	NoReadingYet = fault.Register(faultDomain, "NO_READING_YET", http.StatusServiceUnavailable, fault.Document(
		"This module has not decided a frame yet",
		"It answers once it has taken its first reading, which is within a second of starting. Send the same request again.",
		fault.RetryAllowed,
		fault.ShowDetail,
	))

	// PreviewNotRunning is a preview that arrived when this instance is not driving exactly one
	// light. The route fires the word on the instance's own panel, and a watch holding several
	// bindings has no single light to fire at: picking one of them would be putting a picture on
	// a light the caller did not name.
	PreviewNotRunning = fault.Register(faultDomain, "PREVIEW_NOT_RUNNING", http.StatusServiceUnavailable, fault.Document(
		"This module is not driving one light yet",
		"It answers once its watch is running, which is within a second of starting, and only for an instance that drives exactly one controller. An instance holding several bindings has no single light to preview on.",
		fault.RetryAllowed,
		fault.ShowDetail,
	))

	// PreviewBorrowedSegment is a preview asked of a module that drives one borrowed segment of
	// somebody else's controller. The same rule that keeps the event off that light keeps the
	// preview off it.
	PreviewBorrowedSegment = fault.Register(faultDomain, "PREVIEW_BORROWED_SEGMENT", http.StatusConflict, fault.Document(
		"The light is a borrowed segment, so this module will not animate it",
		"This module was pointed at one segment of a controller somebody else also drives, and it shows a colour there. An animation nobody asked for is not something it puts on another person's light. Point this instance at its own controller to see the word.",
		fault.RetryNever,
		fault.ShowDetail,
	))

	// PreviewNotDrawable is a preview asked of a light that cannot carry the word: a strip, which
	// has no second dimension, or a panel too small for the word at the scale this build draws it.
	PreviewNotDrawable = fault.Register(faultDomain, "PREVIEW_NOT_DRAWABLE", http.StatusConflict, fault.Document(
		"This light cannot carry the word",
		"The finished word is drawn in pixels on a two-dimensional panel. The controller this instance drives reported no matrix, so it is a strip, or its panel is too small for the word at the scale this build draws it. The details say which.",
		fault.RetryNever,
		fault.ShowDetail,
	))

	// PreviewPatternUnknown is a diagnostic pattern asked for by a name this module does not draw.
	// It is a 400 and not a 409 because nothing about the light is wrong: the request names a
	// picture that does not exist, and the sentence lists the three that do.
	PreviewPatternUnknown = fault.Register(faultDomain, "PREVIEW_PATTERN_UNKNOWN", http.StatusBadRequest, fault.Document(
		"This module draws no diagnostic pattern of that name",
		"The pictures this module can put on the panel to test it are solid, border and quarters: solid is the whole panel in white, border is a white edge two pixels thick, quarters is four colours in four quadrants. Name one of those three in the pattern parameter. Nothing was fired and nothing was changed.",
		fault.RetryNever,
		fault.ShowDetail,
		"pattern",
	))

	// PreviewHoldOutOfRange is a hold these preview routes will not run: not a whole number of
	// seconds, not positive, or longer than the ceiling. It is refused rather than clamped,
	// because a hold silently shortened would be a look the caller was told he had.
	PreviewHoldOutOfRange = fault.Register(faultDomain, "PREVIEW_HOLD_OUT_OF_RANGE", http.StatusBadRequest, fault.Document(
		"The hold is not a length these previews will run",
		"hold is a whole number of seconds. It defaults to 60, which is what the finished event itself holds for, and it may not exceed 300: a preview keeps the light away from the printer's own picture for as long as it runs. Nothing was fired.",
		fault.RetryNever,
		fault.ShowDetail,
		"hold",
	))

	// PreviewFrameInvalid is a preview of the composed panel asked for with a progress that is not a
	// percentage, or with none at all. It is a 400 and not a 409 for the reason the pattern's refusal
	// is one: nothing about the light is wrong, the request does not say what to draw. Nothing was
	// fired by it.
	PreviewFrameInvalid = fault.Register(faultDomain, "PREVIEW_FRAME_INVALID", http.StatusBadRequest, fault.Document(
		"This preview does not say what progress to draw",
		"percent is required and is a whole number from 0 to 100. name is optional and is the job name to draw, which the panel upper-cases and cuts to the columns it has. Nothing was fired and the light was not changed.",
		fault.RetryNever,
		fault.ShowDetail,
		"percent",
	))

	// BindingNotFound is a read, a change or a delete of a binding this module is not driving.
	// A binding is named by its own id — the printer's device id while one printer is shown once,
	// and the light and segment appended once it is shown on more than one — so the refusal names
	// the id that missed and says where the list is.
	BindingNotFound = fault.Register(faultDomain, "BINDING_NOT_FOUND", http.StatusNotFound, fault.Document(
		"This module drives no binding of that name",
		"Read GET /bindings for the set this module is running: it carries every binding's id. A binding is named by the printer's device id while that printer has one binding, and by the device id, the light and the segment once it has two.",
		fault.RetryNever,
		fault.ShowDetail,
		"binding",
	))

	// BindingRefused is a change the module's own rules refuse: two bindings on one segment, the
	// whole controller beside a segment of it, a brightness on a borrowed segment, a printer the
	// host does not hold, or a controller address that is not one.
	//
	// One printer on two lights is *not* one of these: it is what multi-room means.
	//
	// It is a 409 and not a 400 because the body is well formed: what is wrong is the set the
	// change would make, and the sentence names which rule and which two bindings.
	BindingRefused = fault.Register(faultDomain, "BINDING_REFUSED", http.StatusConflict, fault.Document(
		"The set that change would make breaks a rule this module holds two bindings to",
		"One segment is shown by one binding, a whole controller leaves no segment for anything else, and a borrowed segment carries no brightness. One printer on two lights is allowed. The detail names the rule and the binding that got there first. Nothing was changed: the set that was running is still running.",
		fault.RetryNever,
		fault.ShowDetail,
	))

	// BindingBodyInvalid is a body that is not one binding at all: not JSON, or a key this
	// module does not read. It is refused rather than ignored, because a misspelt key would
	// otherwise write a binding with a field left at its zero — a brightness of nothing on the
	// controller an operator meant to leave alone.
	BindingBodyInvalid = fault.Register(faultDomain, "BINDING_BODY_INVALID", http.StatusBadRequest, fault.Document(
		"The body is not one binding",
		"Write a JSON object with printer and controller, and with segment or brightness when they are wanted. Every other key is refused rather than ignored, so a misspelt one is reported instead of silently doing nothing.",
		fault.RetryNever,
		fault.ShowDetail,
	))

	// PrintersUnavailable is a read of the host's printer list that did not answer, which is what
	// tells a caller which printers it may name. Nothing about the light was touched.
	PrintersUnavailable = fault.Register(faultDomain, "PRINTERS_UNAVAILABLE", http.StatusServiceUnavailable, fault.Document(
		"This module could not read the host's printer list",
		"The list is the host's, and it is what says which printers exist and what their device ids are. Send the same request again once the host answers.",
		fault.RetryAllowed,
		fault.ShowDetail,
	))

	// ControllerNotFound is a read of a controller this module drives no binding on.
	ControllerNotFound = fault.Register(faultDomain, "CONTROLLER_NOT_FOUND", http.StatusNotFound, fault.Document(
		"This module drives no controller of that name",
		"Read GET /controllers for the ones it does. A controller has no separate existence here: it appears when a binding names it and it is gone when the last binding on it is deleted, so the list is the bindings, grouped.",
		fault.RetryNever,
		fault.ShowDetail,
		"controller",
	))

	// StoreUnavailable is a read or a write where this module has no configuration store it can
	// act on: the operator granted it no data directory, the document has not been written yet,
	// the document is one this build cannot act on, or the directory would not take the write.
	// It covers every editing surface — the bindings and the tables beside them — because a
	// binding is written into the same document the tables are. It is a 503 and not a 404 because
	// the route exists and the thing it edits exists — what is missing is the place an edit would
	// be kept.
	StoreUnavailable = fault.Register(faultDomain, "STORE_UNAVAILABLE", http.StatusServiceUnavailable, fault.Document(
		"This module has no configuration store it can read or write",
		"This module keeps its configuration — the bindings it drives and the tables a screen edits — in one JSON document in the data directory the operator approves when the module is installed, and it writes that document itself at start-up. If the module was granted no directory, or the directory cannot be written, nothing on this surface can be saved: the change was not made, and what was running is running still. Send the same request again after the module has been configured, or reinstall it granting a data directory.",
		fault.RetryAllowed,
		fault.ShowDetail,
	))

	// StateNotFound is a read, a change or a delete of a state the table does not hold. The table
	// is not this module's invention — it is the ten states the host reports plus the two
	// conditions the host has no word for — so the refusal lists the names it does hold.
	StateNotFound = fault.Register(faultDomain, "STATE_NOT_FOUND", http.StatusNotFound, fault.Document(
		"This module has no state of that name",
		"Read GET /states for the table. A state is named by the word the host reports — PRINTING, PAUSED, ERROR and the rest — or by one of the two conditions this module computes for itself, Disconnected and Stale.",
		fault.RetryNever,
		fault.ShowDetail,
		"state",
	))

	// StateRefused is a well-formed body that breaks one of the table's own rules: a colour
	// channel outside 0-255, or a hold that is not one of the four the screen offers. It is a 409
	// and not a 400 because the body parsed: what is wrong is the value, and the sentence names it.
	StateRefused = fault.Register(faultDomain, "STATE_REFUSED", http.StatusConflict, fault.Document(
		"The value is not one this module can act on",
		"A colour is three channels of 0-255. A word's hold is one of forever, 30s, 60s and 5m, and it is refused rather than rounded because the length is the whole content of the field. The detail names the value that was sent and the values that are accepted. Nothing was changed.",
		fault.RetryNever,
		fault.ShowDetail,
		"state", "holds",
	))

	// StateBodyInvalid is a body that is not a state's row at all: not JSON, a key this module does
	// not read, or none of colour, word and holds. A body carrying none of the three is refused
	// rather than read as "clear the row", because clearing is what DELETE is for.
	StateBodyInvalid = fault.Register(faultDomain, "STATE_BODY_INVALID", http.StatusBadRequest, fault.Document(
		"The body is not a state's row",
		"Write a JSON object carrying at least one of colour (three channels of 0-255), word (the text the panel draws) and holds (forever, 30s, 60s or 5m). Every other key is refused rather than ignored, so a misspelt one is reported instead of silently doing nothing. Nothing was changed.",
		fault.RetryNever,
		fault.ShowDetail,
	))

	// MomentNotFound is a read, a change or a delete of a moment every light has and this build
	// does not. The eight are the approved Events screen's own list.
	MomentNotFound = fault.Register(faultDomain, "MOMENT_NOT_FOUND", http.StatusNotFound, fault.Document(
		"This module has no moment of that name",
		"Read GET /lights/{light}/moments for every moment a light has. A moment is a transition — print-finished, print-started, filament-change, paused, error-or-attention, light-lost, print-cancelled, printer-went-quiet — and the list belongs to the light.",
		fault.RetryNever,
		fault.ShowDetail,
		"moment",
	))

	// MomentRefused is a well-formed entry that breaks one of the moment table's own rules: a
	// shows this module does not have, a which that does not belong to its shows, a length that is
	// not a length, or a printer that is not on the light it was asked about. It is a 409 and not a
	// 400 because the body parsed; what is wrong is the entry, and the sentence says what.
	MomentRefused = fault.Register(faultDomain, "MOMENT_REFUSED", http.StatusConflict, fault.Document(
		"The entry is not one this module can act on",
		"shows is Preset, Word, Colour or Nothing, and which has to belong to it: a Colour names a state from the state table, a Preset names one of the light's own presets as written, a Word is the text to draw, and Nothing names nothing. A length is forever, or a duration such as 60s. The printer has to be one on that light. Nothing was changed — and nothing was sent to the light either.",
		fault.RetryNever,
		fault.ShowDetail,
		"moment", "printer",
	))

	// MomentBodyInvalid is a body that is not a moment's entry at all: not JSON, a key this module
	// does not read, or a body that does not say whether the moment is on or what it shows.
	MomentBodyInvalid = fault.Register(faultDomain, "MOMENT_BODY_INVALID", http.StatusBadRequest, fault.Document(
		"The body is not a moment's entry",
		"Write a JSON object carrying enabled (true or false), shows (Preset, Word, Colour or Nothing), which (a preset name, a word, or a state's name), for (forever or a duration such as 60s), and printer when the light carries more than one printer. Every other key is refused rather than ignored. Nothing was changed.",
		fault.RetryNever,
		fault.ShowDetail,
	))

	// LayoutRefused is a well-formed layout that breaks one of its own rules: a band this build does
	// not know, a bar taller than the panel can spare, a percentage scale outside 3-5, a rotation
	// that is neither take-turns nor side-by-side, a rotation of no seconds, or remaining asked for
	// on a build that draws none. It is a 409 and not a 400 because the body parsed: what is wrong is
	// the value, and the sentence names it beside the values that are accepted.
	LayoutRefused = fault.Register(faultDomain, "LAYOUT_REFUSED", http.StatusConflict, fault.Document(
		"The layout is not one this module can act on",
		"bar_band is top or bottom. bar_rows is 1 to 48 of a 64-row panel. percent_scale is 3, 4 or 5. rotation is take-turns or side-by-side, and side-by-side is drawn. rotate_seconds is a whole number of seconds, one or more. remaining may only be false: this build draws no time remaining. The detail names the value that was sent and the values accepted. Nothing was changed.",
		fault.RetryNever,
		fault.ShowDetail,
		"binding", "bar_band", "bar_rows", "percent_scale", "rotation", "rotate_seconds",
	))

	// LayoutBodyInvalid is a body that is not a layout at all: not JSON, a key this module does not
	// read, or none of the eight keys. A body carrying none of them is refused rather than read as
	// "clear the layout", because clearing is what DELETE is for.
	LayoutBodyInvalid = fault.Register(faultDomain, "LAYOUT_BODY_INVALID", http.StatusBadRequest, fault.Document(
		"The body is not a layout",
		"Write a JSON object carrying at least one of bar, bar_band, bar_rows, percent, percent_scale, name, rotation and rotate_seconds. Every other key is refused rather than ignored, so a misspelt one is reported instead of silently doing nothing. Nothing was changed.",
		fault.RetryNever,
		fault.ShowDetail,
	))

	// WindowNotFound is a window position this module's list does not hold. Positions are indices
	// counting from zero, as the list reports them, and they shift when a window before them is
	// removed.
	WindowNotFound = fault.Register(faultDomain, "WINDOW_NOT_FOUND", http.StatusNotFound, fault.Document(
		"This module holds no brightness window at that position",
		"Read GET /windows for the list. A window is addressed by its index, counting from zero, and the list reports each one's index beside it. Removing a window moves the ones after it up one position.",
		fault.RetryNever,
		fault.ShowDetail,
		"index",
	))

	// WindowRefused is a well-formed window this module will not store, or a window write on a module
	// with no timezone to judge it in: two ends the same, a time of day that is not HH:MM, a
	// brightness outside 0-255, a day that is not one of the seven, or a `timezone` setting naming
	// nothing or naming a zone that will not load. The zone cases are here because a window judged in
	// the container's own clock fires hours away from the evening it was written for.
	WindowRefused = fault.Register(faultDomain, "WINDOW_REFUSED", http.StatusConflict, fault.Document(
		"The window is not one this module can act on",
		"from and to are a time of day written HH:MM; an end earlier than the start wraps midnight and is accepted. The two ends may not be the same, because that is either a whole day or no time at all and which was meant cannot be guessed. brightness is 0-255, which is what a WLED controller takes. days is optional and names any of mon, tue, wed, thu, fri, sat and sun. A window can only be judged in a zone: name one in the timezone setting, as an IANA name such as America/Denver. Nothing was changed.",
		fault.RetryNever,
		fault.ShowDetail,
		"index", "timezone",
	))

	// WindowBodyInvalid is a body that is not a window at all: not JSON, or a key this module does
	// not read.
	WindowBodyInvalid = fault.Register(faultDomain, "WINDOW_BODY_INVALID", http.StatusBadRequest, fault.Document(
		"The body is not a brightness window",
		"Write a JSON object carrying from and to as a time of day written HH:MM, brightness as 0-255, and days optionally as a list of mon, tue, wed, thu, fri, sat and sun. Every other key is refused rather than ignored. Nothing was changed.",
		fault.RetryNever,
		fault.ShowDetail,
	))

	// PauseRefused is a well-formed pause this module will not store: a length that is not one of the
	// six the panic button offers.
	PauseRefused = fault.Register(faultDomain, "PAUSE_REFUSED", http.StatusConflict, fault.Document(
		"The pause is not one this module can act on",
		"for is one of 10m, 1h, 6h, 1d, 3d and forever, and it is refused by name rather than rounded to the nearest because a pause of about an hour is not the thing anybody asked for. Nothing was changed.",
		fault.RetryNever,
		fault.ShowDetail,
		"for",
	))

	// PauseBodyInvalid is a body that is not a pause at all: not JSON, a key this module does not
	// read, or no length at all.
	PauseBodyInvalid = fault.Register(faultDomain, "PAUSE_BODY_INVALID", http.StatusBadRequest, fault.Document(
		"The body is not a pause",
		"Write a JSON object carrying for, one of 10m, 1h, 6h, 1d, 3d and forever. Every other key is refused rather than ignored. Nothing was changed.",
		fault.RetryNever,
		fault.ShowDetail,
	))

	// SnapshotNotFound is a snapshot id this module holds no file for, or one taken from another
	// light. Both are the same answer to a caller: there is no snapshot of this light by that name.
	SnapshotNotFound = fault.Register(faultDomain, "SNAPSHOT_NOT_FOUND", http.StatusNotFound, fault.Document(
		"This module holds no snapshot of that name for this light",
		"Read GET /devices/{name}/snapshot for the ids it holds, newest first. A snapshot is named by the light it was taken from and the moment it was taken, and a snapshot taken from another light is not one this light can be restored from.",
		fault.RetryNever,
		fault.ShowDetail,
		"snapshot",
	))

	// SnapshotUnreadable is a light that could not be read, so no snapshot of it could be taken —
	// including the safety snapshot a restore takes first, which is why a restore refuses on this
	// code rather than writing and hoping. A dead light is exactly the case a snapshot exists for,
	// and it is also the case in which a new one cannot be taken.
	SnapshotUnreadable = fault.Register(faultDomain, "SNAPSHOT_UNREADABLE", http.StatusServiceUnavailable, fault.Document(
		"This light could not be read, so it was not snapshotted",
		"A snapshot is the light's own configuration, presets and state, read from the light: a light that does not answer cannot be copied, and a restore also takes a copy of the light as it is first, so it does nothing at all while the light is unreadable. Read GET /devices/{name}/snapshot for the snapshots this module already holds — those exist because they were taken while the light was alive, and they survive the light dying.",
		fault.RetryAllowed,
		fault.ShowDetail,
		"controller",
	))

	// RestoreNotConfirmed is a restore body that did not say it meant it. It is refused before the
	// snapshot is read, because a restore rewrites a controller's whole configuration and a caller
	// that did not mean it must not be able to learn what it would have done.
	RestoreNotConfirmed = fault.Register(faultDomain, "RESTORE_NOT_CONFIRMED", http.StatusConflict, fault.Document(
		"The restore was not confirmed",
		"A restore rewrites the light's whole configuration and restarts it, so the body has to carry confirm: true beside the snapshot's id. Nothing was read and nothing was written.",
		fault.RetryNever,
		fault.ShowDetail,
		"snapshot",
	))

	// RestoreRefused is a restore this module will not attempt: a snapshot this build cannot build
	// a body from, a snapshot taken from another light or from a display of another shape, a light
	// that did not answer when it was asked who it is, or a controller that refused the
	// configuration write. Every one of them leaves the light as it was, or says which snapshot
	// puts it back.
	RestoreRefused = fault.Register(faultDomain, "RESTORE_REFUSED", http.StatusConflict, fault.Document(
		"The restore was refused and nothing was written",
		"A snapshot is restored only onto the light it was taken from: the same address, the same controller name, the same shape and the same pixel count. Anything else is a configuration written to a display it was not made for, which is a scrambled picture rather than a restore. The detail names what did not match, and every refusal either wrote nothing at all or names the safety snapshot that puts the light back.",
		fault.RetryNever,
		fault.ShowDetail,
		"snapshot", "controller", "safety_snapshot",
	))

	// RestoreBodyInvalid is a restore body that is not one: not JSON, a key this module does not
	// read, or no snapshot named.
	RestoreBodyInvalid = fault.Register(faultDomain, "RESTORE_BODY_INVALID", http.StatusBadRequest, fault.Document(
		"The body is not a restore request",
		"Write a JSON object carrying snapshot, the id of a snapshot GET /devices/{name}/snapshot reports, and confirm: true to mean it. Every other key is refused rather than ignored. Nothing was changed.",
		fault.RetryNever,
		fault.ShowDetail,
	))

	// RestoreNotAnswered is a restore that wrote the configuration and then did not see the light
	// answer again inside the wait. It is a 504 because the module reached the light, the light
	// took the body, and what did not happen is the answer coming back.
	//
	// It is deliberately not a success. A light that is mid-reboot looks exactly like this, so the
	// restore may yet have worked, and the detail says so rather than either claiming it did or
	// claiming it did not — and names the safety snapshot, which is the one thing a person can act
	// on either way.
	RestoreNotAnswered = fault.Register(faultDomain, "RESTORE_NOT_ANSWERED", http.StatusGatewayTimeout, fault.Document(
		"The light was written the snapshot and has not answered since",
		"A configuration write restarts the light, and this module waits for it to answer before saying the restore took: a light that is still rebooting looks exactly like one that has failed. This answer means the wait ran out. The restore may still have worked — read GET /devices/{name}/snapshot, or look at the light. If it did not, restoring the safety snapshot named here puts the light back as it was.",
		fault.RetryAllowed,
		fault.ShowDetail,
		"snapshot", "safety_snapshot",
	))
)

// mustDomain builds this module's fault domain. A name the sdk refuses is a
// mistake in this file, so it stops the process at start-up rather than
// answering a caller with an empty code.
func mustDomain(name string) fault.Domain {
	domain, err := fault.NewDomain(name)
	if err != nil {
		panic(fmt.Sprintf("wled-printer-status: %s is not a fault domain: %v", name, err))
	}

	return domain
}

// holdRefusal turns holdFrom's error into the registered refusal, carrying the value the caller
// sent so that the sentence and the fault agree about what was asked for.
func holdRefusal(r *http.Request, err error) *fault.Fault {
	return PreviewHoldOutOfRange.
		New(err.Error()).
		With(fault.StringParam("hold", strings.TrimSpace(r.URL.Query().Get("hold"))))
}

// refuse writes one refusal as RFC 9457 frames it, which is the shape the
// generated client reads and the shape describeFault reads in the browser half:
// the code, the English sentence, the values that sentence names, and where the
// code is documented.
func refuse(w http.ResponseWriter, r *http.Request, raised *fault.Fault) {
	problem := raised.ProblemJSON(r.URL.Path)

	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(problem.Status)

	_ = json.NewEncoder(w).Encode(problem)
}
