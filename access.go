// Asking the host, on every request that changes something, whether the caller may make that
// change — and refusing when it cannot answer.
//
// # Why a module asks at all
//
// The host decides who may reach this module at all: it takes view_resource on this module's
// instance for a read and operate_resource for anything else, before the request is forwarded
// (internal/transport/http/httpapi moduleAction). That check cannot tell one write from another,
// so it cannot say that rewriting this module's configuration is a different act from pausing a
// light. It also covers only the requests the host forwards: a call from another module carries a
// person's name the same way, and a route reached down a path the mount did not take is reached
// with no question asked at all.
//
// So this module asks its own question, per route, before the handler runs:
//
//   - **A configuration change asks configure_resource.** Adding, removing or ignoring a light,
//     creating, replacing or deleting a binding, a layout, a state row or a moment, the brightness
//     windows and the zone are all the same act in the host's vocabulary: they rewrite what this
//     module is told to do. Only a manager holds it.
//   - **Acting on a light right now asks operate_resource.** A pause, a resume, a refresh, an
//     identify, a restore and the three previews change what a light shows and leave this module's
//     configuration exactly as it is. An operator holds it.
//
// # The object is the kind, not the instance
//
// The host's resource actions are performed on a resource kind, and the grant that decides them is
// written on the kind: internal/authz's store reads `resource_access` by kind for a point check, and
// the role ladder gives a viewer view_resource, an operator operate_resource and a manager
// configure_resource on the kind. This module's one instance is declared under `kind: sink` in
// module.yaml, so the question names `resource_kind:sink` — the same object the server-slicing
// module asks about (extensions/server-slicing/internal/routes administers).
//
// There is no per-instance question to ask instead: the object a module can name here is the kind,
// and an instance-level check (`sink:<name>`) is a check the graph answers by looking for a grant
// written on a principal, not on the configuration surface.
//
// # Failing closed
//
// A host that cannot answer is a refusal, never a pass. The answer is not kept when the question
// failed, so the next request asks again; a "no" is kept for accessKept like any other answer, which
// is the trade the bundled extensions make too. Nothing here is a substitute for the host's own
// check, and nothing here can allow what the host has already refused.
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/prusa3d/prusa-connect/sdk/accessapi"
	"github.com/prusa3d/prusa-connect/sdk/fault"
	"github.com/prusa3d/prusa-connect/sdk/routesapi"
)

// The two actions this module asks about, spelled exactly as the host's authorization vocabulary
// spells them (internal/authz/vocabulary.go, ActionOperateResource and ActionConfigureResource).
// A name the host does not know is refused rather than answered false, and the module treats that
// refusal as a refusal of the request — so a spelling mistake here is loud, never a quiet pass.
const (
	// actionConfigureResource rewrites what this module is told to do.
	actionConfigureResource = "configure_resource"

	// actionOperateResource acts on a light now, without rewriting the configuration.
	actionOperateResource = "operate_resource"
)

// The object every question names: the resource kind this module's instance is declared under, and
// therefore the object the host's grant is written on. objectTypeResourceKind is
// internal/authz's TypeResourceKind, which is the type the three resource actions are defined on.
const (
	objectTypeResourceKind = "resource_kind"
	objectKind             = "sink"
)

// accessKept is how long the host's answer is kept, per person and action. It is 30 seconds, the
// same figure the bundled modules keep an answer for: long enough that a screen's handful of writes
// is one question, short enough that a role changed on the host takes effect while a person is still
// looking at the screen. A failed question is never kept.
const accessKept = 30 * time.Second

// Access is the host's side of access.check, narrowed to the one method this module calls. The
// session satisfies it, and a test supplies its own.
type Access interface {
	Check(ctx context.Context, params accessapi.CheckParams) (accessapi.Decision, error)
}

// access is how this process asks the host. Nil until the session exists — a build with no
// connection, which is every route test that did not install one — and nil refuses every change,
// because a module that cannot ask is a module that does not know.
var access Access

// question is one thing asked about one person: may they take this action on this module.
type question struct {
	person string
	action string
}

// keptAnswer is one answer and the instant it stops being an answer.
type keptAnswer struct {
	allowed bool
	until   time.Time
}

var (
	answersMu sync.Mutex
	answers   = map[question]keptAnswer{}
)

// permitted answers whether the caller of r may take action on this module, and writes the refusal
// itself when they may not. A handler calls it first and returns when it is false, which is why a
// handler that changes nothing for a caller the host said no for is the only shape that exists.
//
// The refusal is a registered code in the shape every other refusal in this module takes: a status,
// a code a browser half switches on, and an English sentence in one line. A viewer who presses Pause
// is told that they may not, not handed the host's error.
func permitted(w http.ResponseWriter, r *http.Request, action string) bool {
	person := routesapi.PrincipalOf(r)
	if person == "" {
		// Every request the host forwards names its caller, and this one did not. There is nobody
		// to ask about, so there is nothing to decide: the change is refused rather than made on
		// behalf of a caller nobody can name.
		refuse(w, r, NotPermitted.New(
			"this module was not told who is asking, so it changed nothing: only a request the host "+
				"forwarded names its caller").
			With(fault.StringParam("action", action)))

		return false
	}

	allowed, err := mayAsk(r.Context(), person, action)
	if err != nil {
		// A question that failed is not an answer. It is logged with the person and the action so
		// that the host's log and this module's line agree about what was asked and never answered.
		fmt.Fprintf(os.Stderr, "%s: %s %s: %v\n", name, r.Method, r.URL.Path, err)

		refuse(w, r, AccessUnknown.New(
			"this module could not ask the host whether you may do that, so it changed nothing. "+
				"Send the same request again in a moment.").
			With(fault.StringParam("action", action)))

		return false
	}

	if !allowed {
		refuse(w, r, NotPermitted.New(
			"the host answered that you may not "+action+" this module, so nothing was changed. "+
				"Configuration is a manager's and acting on a light is an operator's; ask whoever "+
				"administers this server for the role, then send the request again.").
			With(fault.StringParam("action", action)))

		return false
	}

	return true
}

// mayAsk answers whether person may take action on this module, from the host's own answer and from
// the answer kept from a question asked within accessKept.
//
// A question that failed is returned as an error and is not kept: the failure is the host not
// answering, and treating it as a "no" that lasts 30 seconds would make one slow moment a refusal
// nobody could clear by waiting for the host to recover.
func mayAsk(ctx context.Context, person, action string) (bool, error) {
	asked := question{person: person, action: action}
	now := time.Now()

	answersMu.Lock()
	kept, ok := answers[asked]
	answersMu.Unlock()

	if ok && now.Before(kept.until) {
		return kept.allowed, nil
	}

	if access == nil {
		return false, errors.New("this module has no session with the host to ask")
	}

	decision, err := access.Check(ctx, accessapi.CheckParams{
		// The person, never empty: an empty subject is this module asking about itself, and this
		// module's own grant is not what a request from somebody else is decided by.
		Subject:    person,
		Action:     action,
		ObjectType: objectTypeResourceKind,
		ObjectID:   objectKind,
	})
	if err != nil {
		return false, fmt.Errorf("ask whether %s may %s this module: %w", person, action, err)
	}

	answersMu.Lock()
	defer answersMu.Unlock()

	// A sweep on each write drops what is past its instant. The map holds one row per person and
	// action asked about within accessKept, and the host bounds what it will answer anyway.
	for other, answer := range answers {
		if !now.Before(answer.until) {
			delete(answers, other)
		}
	}

	answers[asked] = keptAnswer{allowed: decision.Allowed, until: now.Add(accessKept)}

	return decision.Allowed, nil
}

// asked is the action each mutating route asks about, keyed by the pattern it registered, filled in
// as the routes are mounted.
//
// It is a package-level record for the reason fleet and live are: the mounts are handed a
// *http.ServeMux and nothing else, and a table that had to be passed in as well would be a second
// parameter no reader would thank us for. Nothing writes it after start-up, and a test reads it to
// send one request at every route that changes something — see access_test.go.
//
//nolint:gochecknoglobals // a closed table written while the routes are mounted, read after.
var (
	askedMu sync.Mutex
	asked   = map[string]string{}
)

// mutating registers a route that changes something, and asks the host before the handler runs.
//
// The pattern and the action sit together on purpose: a route's permission is a fact about that
// route, and a table held somewhere else is a table that drifts. `TestEveryMutatingRouteAsks` walks
// this package's own source and refuses a mutating registration that is not one of these, which is
// what keeps "every route that changes something asks" true after this file.
func mutating(mux *http.ServeMux, pattern, action string, h http.HandlerFunc) {
	askedMu.Lock()
	asked[pattern] = action
	askedMu.Unlock()

	mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		if !permitted(w, r, action) {
			return
		}

		h(w, r)
	})
}

// routesAsked is what every mutating route asks about, copied so that a caller cannot hold the
// module's own table.
func routesAsked() map[string]string {
	askedMu.Lock()
	defer askedMu.Unlock()

	out := make(map[string]string, len(asked))
	for pattern, action := range asked {
		out[pattern] = action
	}

	return out
}
