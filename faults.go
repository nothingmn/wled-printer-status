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
