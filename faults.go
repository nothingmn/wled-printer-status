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

	// BindingNotFound is a read, a change or a delete of a binding this module is not driving.
	// A binding is named by the device id of the printer it shows, so the refusal names the id
	// that missed and says where the list is.
	BindingNotFound = fault.Register(faultDomain, "BINDING_NOT_FOUND", http.StatusNotFound, fault.Document(
		"This module drives no binding of that printer",
		"Read GET /bindings for the set this module is running. A binding is named by the host's device id for the printer it shows, and GET /printers lists the ones this host holds.",
		fault.RetryNever,
		fault.ShowDetail,
		"printer",
	))

	// BindingRefused is a change the module's own rules refuse: two bindings on one segment, two
	// bindings on one printer, the whole controller beside a segment of it, a brightness on a
	// borrowed segment, a printer the host does not hold, or a controller address that is not one.
	//
	// It is a 409 and not a 400 because the body is well formed: what is wrong is the set the
	// change would make, and the sentence names which rule and which two bindings.
	BindingRefused = fault.Register(faultDomain, "BINDING_REFUSED", http.StatusConflict, fault.Document(
		"The set that change would make breaks a rule this module holds two bindings to",
		"One printer is shown by one binding, one segment is shown by one binding, a whole controller leaves no segment for anything else, and a borrowed segment carries no brightness. The detail names the rule and the binding that got there first. Nothing was changed: the set that was running is still running.",
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
