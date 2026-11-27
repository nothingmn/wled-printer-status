// Reading as the caller: which printers the person who made this request may see.
//
// # Why a read asks at all
//
// The host decides who may reach this module, and it decides that once per request before the
// request is forwarded. What it cannot decide is which of the printers a route names that person
// may see: a module that reads the fleet as itself — `deviceapi.List` with no `Principal` — reads
// every printer on the server and hands the answer to whoever opened the extension. The bindings,
// the frames, the moment rows and the printer list are all about printers, so every one of them is
// a fact about somebody else's machine unless the question is asked for the caller.
//
// `deviceapi.ListParams.Principal` is the host's own answer to that: "who the call is made for, and
// empty for the module's own work". A read route therefore passes it, per request, and the host
// returns the printers that person may see. That set — and not a guess about device ids — is what
// every answer is filtered by: a binding, a frame, a moment row or a table row whose printer is not
// in the set is absent from the answer, not present and empty.
//
// # The driver is not touched
//
// This is a property of the read path only. The watch keeps driving every binding it was given,
// whoever is looking, and a caller who may see no printer at all gets an empty list rather than an
// error that would stop anything: the module's own work is not a person's request.
//
// # Failing closed
//
// A host that cannot answer is a refusal, never a wide-open read. There is no fallback to the whole
// fleet: the answer to "which printers may you see" that this module could not obtain is not "all of
// them", and a route that answered the fleet because the question failed would be the defect this
// file exists to close.
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/prusa3d/prusa-connect/sdk/deviceapi"
	"github.com/prusa3d/prusa-connect/sdk/fault"
	"github.com/prusa3d/prusa-connect/sdk/routesapi"
)

// visibleKept is how long the host's answer is kept, per person. It is 30 seconds, the figure
// accessKept keeps a permission answer for and for the same two reasons: one screenful of reads
// asks once rather than once per row, and a printer shared with a person — or taken away — reaches
// them while they are still looking at the screen.
//
// It is shorter than a person's patience and shorter than the host's own cache, and a failed
// question is never kept, so a host that recovers is asked again on the next request.
const visibleKept = 30 * time.Second

// noCaller is the refusal for a read that the host did not attribute to anybody. It is a value
// rather than a sentence written at each call site because the same fact has to read the same way
// on every route.
var errNoCaller = errors.New("the host did not name the caller of this request")

// visible is the printers one person may see, as the host answered for them: the rows from the one
// list read, and the ids of those rows.
//
// It is a value and not a set of ids alone because GET /printers is the surface that draws them —
// name, model, whether the host holds a session — and a second read for the same question would be
// a second place for the answer to disagree with itself.
type visible struct {
	items []deviceapi.Device
	ids   map[string]struct{}

	// total is the host's own count for the same question, which is what GET /printers answers as
	// `total`: it is larger than len(items) when the host holds more printers than one page.
	total int
}

// seenOf is one host answer as the set the routes filter by.
func seenOf(page deviceapi.Page) visible {
	ids := make(map[string]struct{}, len(page.Items))

	for _, device := range page.Items {
		ids[device.ID] = struct{}{}
	}

	return visible{items: page.Items, ids: ids, total: page.Total}
}

// has answers whether the caller may see one printer.
func (v visible) has(id string) bool {
	_, ok := v.ids[id]

	return ok
}

// only is a set narrowed to the bindings whose printer the caller may see, in the operator's order.
//
// This is the filter every route applies to the running set before it answers from it: a binding is
// a promise to paint one printer on one light, so a binding whose printer is not visible is not a
// row to draw with its printer blanked out — it is not there.
func (v visible) only(set bindings) bindings {
	out := make(bindings, 0, len(set))

	for _, b := range set {
		if v.has(b.DeviceID) {
			out = append(out, b)
		}
	}

	return out
}

// named is one binding's name as a question about a printer: the device id it starts with may be
// seen. A binding's name is the bare device id while a printer sits on one light, and
// `<device id>@<light>:<segment>` once it sits on more than one (see identified), so the test is on
// the part before the `@`.
func (v visible) named(name string) bool {
	id, _, _ := strings.Cut(name, "@")

	return v.has(id)
}

// namedAmong is a list of binding names narrowed to the ones whose printer the caller may see, in
// the order given.
func (v visible) namedAmong(names []string) []string {
	out := make([]string, 0, len(names))

	for _, name := range names {
		if v.named(name) {
			out = append(out, name)
		}
	}

	return out
}

// publishedFor is the frames the watch published, narrowed to the printers the caller may see.
//
// A published frame names its printer, so a frame for a printer this person may not see is a row
// about somebody else's machine: it is not drawn with its printer blanked out, it is absent.
func publishedFor(held []bindingStatus, seen visible) []bindingStatus {
	out := make([]bindingStatus, 0, len(held))

	for _, one := range held {
		if seen.has(one.Printer.ID) {
			out = append(out, one)
		}
	}

	return out
}

// namesOf is the caller's own printer list as the names a row is labelled from.
//
// It is the same one read the visibility question made, so a route that has already asked who may
// see what does not ask again for the names: one screenful of reads is one question to the host. A
// printer that is not in the map is one the caller may not see, which is why no row that uses it can
// be about somebody else's machine.
func namesOf(seen visible) map[string]string {
	names := make(map[string]string, len(seen.items))

	for _, device := range seen.items {
		names[device.ID] = device.Name
	}

	return names
}

// keptSeen is one answer and the instant it stops being an answer.
type keptSeen struct {
	seen  visible
	until time.Time
}

var (
	seenMu sync.Mutex
	//nolint:gochecknoglobals // the answers kept for visibleKept, per person.
	seenAnswers = map[string]keptSeen{}
)

// seenBy asks the host which printers person may see, and keeps the answer for visibleKept.
//
// A question that failed is returned as an error and is not kept: one slow moment must not become
// thirty seconds of refusal nobody can clear by waiting for the host to recover. The error is the
// caller's to refuse with — see seenOrRefuse — and never a reason to answer the whole fleet.
func seenBy(ctx context.Context, fleet devices, person string, bound time.Duration) (visible, error) {
	if person == "" {
		return visible{}, errNoCaller
	}

	if fleet == nil {
		return visible{}, errors.New("this process has no connection to the host yet")
	}

	now := time.Now()

	seenMu.Lock()
	kept, ok := seenAnswers[person]
	seenMu.Unlock()

	if ok && now.Before(kept.until) {
		return kept.seen, nil
	}

	reading, cancel := context.WithTimeout(ctx, bound)
	defer cancel()

	page, err := fleet.List(reading, deviceapi.ListParams{
		// The person, never empty: an empty principal is this module asking for its own work,
		// and this module's own work is not a request from somebody else — the whole defect was
		// reading the fleet that way.
		Principal: person,
		Limit:     deviceapi.MaxPageLimit,
	})
	if err != nil {
		return visible{}, fmt.Errorf("ask the host which printers %s may see: %w", person, err)
	}

	seen := seenOf(page)

	seenMu.Lock()
	defer seenMu.Unlock()

	// A sweep on each answer, the way the permission answers sweep: the map holds one row per
	// person asked about within visibleKept.
	for other, answer := range seenAnswers {
		if !now.Before(answer.until) {
			delete(seenAnswers, other)
		}
	}

	seenAnswers[person] = keptSeen{seen: seen, until: now.Add(visibleKept)}

	return seen, nil
}

// forgetSeen empties the kept answers. A test uses it between fixtures, so that one test's host
// answer cannot be another's; the process never needs it, because every answer expires on its own
// after visibleKept.
func forgetSeen() {
	seenMu.Lock()
	defer seenMu.Unlock()

	seenAnswers = map[string]keptSeen{}
}

// seenOrRefuse is the caller's visible printers, or the refusal a route answers with instead.
//
// It is the one place a read route decides what a host that cannot answer means, so that no route
// can decide differently: a request that names no caller, and a question the host did not answer,
// are both a refusal in the module's own registered codes — never an empty list that would read as
// "there is nothing", and never the fleet.
func seenOrRefuse(w http.ResponseWriter, r *http.Request, fleet func() devices, bound time.Duration) (visible, bool) {
	return seenOrRefuseWith(w, r, fleet, bound, AccessUnknown)
}

// seenOrPrinters is seenOrRefuse for GET /printers, whose subject is the printer list itself: a list
// the host did not answer for keeps the code that route has always answered, so that a screen that
// already branches on PRINTERS_UNAVAILABLE is not reading a different sentence about the same
// failure.
func seenOrPrinters(w http.ResponseWriter, r *http.Request, fleet func() devices, bound time.Duration) (visible, bool) {
	return seenOrRefuseWith(w, r, fleet, bound, PrintersUnavailable)
}

// seenOrRefuseWith is the one implementation, with the code a host that did not answer is refused
// under.
func seenOrRefuseWith(w http.ResponseWriter, r *http.Request, fleet func() devices, bound time.Duration, unreadable *fault.Definition) (visible, bool) {
	person := routesapi.PrincipalOf(r)

	// A value with no fleet at all is a module with no connection, which is the same refusal as a
	// host that did not answer: it is not a reason to answer the fleet.
	var host devices

	if fleet != nil {
		host = fleet()
	}

	seen, err := seenBy(r.Context(), host, person, bound)
	if err == nil {
		return seen, true
	}

	if errors.Is(err, errNoCaller) {
		refuse(w, r, NotPermitted.New(
			"this module was not told who is asking, so it answered nothing: only a request the host "+
				"forwarded names its caller"))

		return visible{}, false
	}

	// The line goes to the module's log so that the host's log and this one agree about what was
	// asked and never answered.
	fmt.Fprintf(os.Stderr, "%s: %s %s: %v\n", name, r.Method, r.URL.Path, err)

	refuse(w, r, unreadable.New(
		"this module could not ask the host which printers you may see, so it answered nothing rather "+
			"than the whole fleet. Send the same request again in a moment.").
		With(fault.StringParam("route", r.Method+" "+r.URL.Path)))

	return visible{}, false
}
