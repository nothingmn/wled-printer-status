// What the routes that change something ask the host, and what a caller is told when the answer is
// no.
//
// Three tests carry the rule and the rest prove the shapes a person reads:
//
//   - TestEveryMutatingRouteAsks walks this package's own source, so a route that changes something
//     and skips the question fails here rather than in somebody's instance.
//   - TestAViewerIsRefusedEveryMutatingRoute sends one request at every mutating route as a viewer
//     and gets the same refusal from each.
//   - TestTheHostCannotAnswerIsARefusal proves a failed question refuses rather than passes, which
//     is the half that is easy to get wrong and impossible to see.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/prusa3d/prusa-connect/sdk/accessapi"
	"github.com/prusa3d/prusa-connect/sdk/routesapi"

	"github.com/nothingmn/wled-printer-status/internal/wled"
)

// who is the caller every route test's request names. The host forwards a person's name and never
// their credential, so a request that names nobody is the one shape a module cannot act on — see
// TestARequestWithNoCallerIsRefused.
const who = "user:operator"

// forwarded builds the request the host makes: a method, a path, a body, and the person the host
// authorized. Every route test in this package goes through it, so that no test is exercising a
// shape a real request never has.
func forwarded(method, target string, body io.Reader) *http.Request {
	var payload []byte

	if body != nil {
		var err error
		if payload, err = io.ReadAll(body); err != nil {
			panic("read the fixture's body: " + err.Error())
		}
	}

	path, query, _ := strings.Cut(target, "?")

	request, err := routesapi.Request{
		Method:    method,
		Path:      path,
		Query:     query,
		Body:      payload,
		Principal: who,
	}.HTTP(context.Background())
	if err != nil {
		panic("build the fixture's request: " + err.Error())
	}

	return request
}

// requestAs builds one request as a named person made it, which is the fixture a permission test
// needs: the same request twice, as two people.
func requestAs(t *testing.T, person, method, path string, body ...string) *http.Request {
	t.Helper()

	var payload string
	if len(body) > 0 {
		payload = body[0]
	}

	request, err := routesapi.Request{
		Method:    method,
		Path:      path,
		Body:      []byte(payload),
		Principal: person,
	}.HTTP(context.Background())
	if err != nil {
		t.Fatalf("build %s %s as %s: %v", method, path, person, err)
	}

	return request
}

// TestMain installs the one thing a route test cannot build: a host. Every question is answered yes,
// so that a test about a handler is a test about the handler; a test about the permission installs
// its own host — see withHost — and the order is not a race because nothing in this package runs in
// parallel.
func TestMain(m *testing.M) {
	access = allowing{}

	os.Exit(m.Run())
}

// allowing is the host a route test runs against when the question is not what it is testing.
type allowing struct{}

func (allowing) Check(context.Context, accessapi.CheckParams) (accessapi.Decision, error) {
	return accessapi.Decision{Allowed: true}, nil
}

// ladder is a host that answers from the host's own role ladder: a viewer reads an instance, an
// operator acts on it, and only a manager rewrites its configuration (internal/authz resourceMask).
// It records every question it was asked, which is how a test proves what the module asked.
type ladder struct {
	may   map[string][]string
	fails error
	asked []accessapi.CheckParams
}

func (h *ladder) Check(_ context.Context, params accessapi.CheckParams) (accessapi.Decision, error) {
	h.asked = append(h.asked, params)

	if h.fails != nil {
		return accessapi.Decision{}, h.fails
	}

	return accessapi.Decision{Allowed: slices.Contains(h.may[params.Subject], params.Action)}, nil
}

// theLadder is the three people this module's routes are asked about, and what each holds.
func theLadder() *ladder {
	return &ladder{may: map[string][]string{
		"user:viewer":   nil,
		"user:operator": {actionOperateResource},
		"user:manager":  {actionOperateResource, actionConfigureResource},
	}}
}

// withHost installs a host for one test, empties the answers kept from an earlier one, and puts both
// back when the test ends.
func withHost(t *testing.T, host Access) {
	t.Helper()

	before := access
	access = host

	answersMu.Lock()
	before2 := answers
	answers = map[question]keptAnswer{}
	answersMu.Unlock()

	t.Cleanup(func() {
		access = before

		answersMu.Lock()
		answers = before2
		answersMu.Unlock()
	})
}

// mutatingPaths is every mutating route with a path a request can be sent at: each {name} replaced by
// a value the route's handler would look for and not find, which is what keeps the sweep to the
// question rather than to a fixture for every screen.
func mutatingPaths(t *testing.T) map[string]string {
	t.Helper()

	// Mounting the routes is what records them. It is the same handler the process serves.
	routes()

	patterns := routesAsked()
	if len(patterns) == 0 {
		t.Fatal("no mutating route asked the host anything: the mounts did not go through mutating()")
	}

	out := make(map[string]string, len(patterns))

	for pattern := range patterns {
		method, path, found := strings.Cut(pattern, " ")
		if !found {
			t.Fatalf("%q is not a method and a path", pattern)
		}

		for name, value := range map[string]string{
			"{id}":     "b1",
			"{name}":   "10.0.0.9",
			"{light}":  "10.0.0.9",
			"{moment}": "finished",
			"{state}":  "printing",
			"{index}":  "0",
		} {
			path = strings.ReplaceAll(path, name, value)
		}

		out[method+" "+path] = patterns[pattern]
	}

	return out
}

// submit sends one request at the module's own routes and answers what came back.
func submit(t *testing.T, request *http.Request) *httptest.ResponseRecorder {
	t.Helper()

	recorder := httptest.NewRecorder()
	routes().ServeHTTP(recorder, request)

	return recorder
}

// problem is one refusal as a person and a browser half read it.
type problem struct {
	Code   string `json:"code"`
	Title  string `json:"title"`
	Detail string `json:"detail"`
}

// refusalOf decodes a refusal, and fails the test for an answer that is not one — a route that
// answered a change with a frame would be a route this test could not read.
func refusalOf(t *testing.T, recorder *httptest.ResponseRecorder) problem {
	t.Helper()

	var raised problem
	if err := json.Unmarshal(recorder.Body.Bytes(), &raised); err != nil {
		t.Fatalf("the answer is not a refusal: %v (%s)", err, recorder.Body.String())
	}

	return raised
}

// The two halves of this module's mutating surface, by name: the routes that rewrite what this
// module is told to do, and the routes that change what a light shows and leave the configuration
// alone. The two lists together are every mutating route this module serves.
//
// They are written out because the difference is a permission: moving a route from one list to the
// other changes who may use it, and a test that only read the registration would agree with whatever
// the registration said.
var (
	configurationRoutes = []string{
		"POST /bindings",
		"PUT /bindings/{id}",
		"DELETE /bindings/{id}",
		"POST /lights/{name}/ignore",
		"DELETE /lights/{name}/ignore",
		"PUT /lights/{light}/moments/{moment}",
		"DELETE /lights/{light}/moments/{moment}",
		"PUT /states/{state}",
		"DELETE /states/{state}",
		"PUT /bindings/{id}/layout",
		"DELETE /bindings/{id}/layout",
		"POST /windows",
		"PUT /windows/{index}",
		"DELETE /windows/{index}",
		"PUT /timezone",
	}

	actingRoutes = []string{
		"PUT /pause",
		"DELETE /pause",
		"PUT /bindings/{id}/pause",
		"DELETE /bindings/{id}/pause",
		"POST /refresh",
		"POST /preview-finished",
		"POST /preview-frame",
		"POST /preview-pattern",
		"POST /devices/{name}/snapshot",
		"POST /devices/{name}/restore",
		"POST /devices/{name}/identify",
	}
)

// TestEveryMutatingRouteAsks walks this package's source and holds two things together: every route
// registered for a method that changes something goes through mutating(), and every mutating() call
// names one of the two actions.
//
// It reads the source rather than the mounted table because the table is written by the calls it is
// checking: a route registered with a plain HandleFunc would never appear in it, and the sweep below
// would pass over a route nobody asked about.
func TestEveryMutatingRouteAsks(t *testing.T) {
	registered := map[string]bool{}
	questions := map[string]string{}

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read this package's directory: %v", err)
	}

	files := token.NewFileSet()

	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}

		file, parseErr := parser.ParseFile(files, name, nil, 0)
		if parseErr != nil {
			t.Fatalf("parse %s: %v", name, parseErr)
		}

		ast.Inspect(file, func(node ast.Node) bool {
			call, isCall := node.(*ast.CallExpr)
			if !isCall {
				return true
			}

			switch fun := call.Fun.(type) {
			case *ast.SelectorExpr:
				// mux.HandleFunc("<pattern>", ...) — a registration with no question in it.
				if fun.Sel.Name != "HandleFunc" || len(call.Args) == 0 {
					return true
				}

				if pattern, isText := textOf(call.Args[0]); isText && changes(pattern) {
					registered[pattern] = true
				}
			case *ast.Ident:
				// mutating(mux, "<pattern>", "<action>", ...) — a registration that asks.
				if fun.Name != "mutating" || len(call.Args) < 3 {
					return true
				}

				pattern, isText := textOf(call.Args[1])
				if !isText {
					return true
				}

				// The action is one of the two constants, so the argument is an identifier and
				// not a string: what is checked here is that it names one of them.
				switch given := call.Args[2].(type) {
				case *ast.Ident:
					questions[pattern] = given.Name
				default:
					questions[pattern], _ = textOf(given)
				}
			}

			return true
		})
	}

	for pattern := range registered {
		if _, asked := questions[pattern]; !asked {
			t.Errorf("%q changes something and nothing asks the host whether the caller may: "+
				"register it with mutating() and name the action", pattern)
		}
	}

	// The names of the two constants, because that is what the source says: an argument that is
	// neither is an action this module has no spelling of.
	for pattern, action := range questions {
		if action != "actionOperateResource" && action != "actionConfigureResource" {
			t.Errorf("%q asks about %s, and the two actions this module asks about are "+
				"actionConfigureResource and actionOperateResource", pattern, action)
		}
	}

	// And what each route asks: the lists at the top of this file against the source.
	want := map[string]string{}

	for _, pattern := range configurationRoutes {
		want[pattern] = "actionConfigureResource"
	}

	for _, pattern := range actingRoutes {
		want[pattern] = "actionOperateResource"
	}

	for pattern, action := range want {
		asked, isAsked := questions[pattern]
		if !isAsked {
			t.Errorf("%q is in the table above and nothing in this package asks about it", pattern)

			continue
		}

		if asked != action {
			t.Errorf("%q asks about %s, and it is %s: a route in the wrong half is a different "+
				"person's to use", pattern, asked, action)
		}
	}

	for pattern := range questions {
		if _, known := want[pattern]; !known {
			t.Errorf("%q asks the host a question and no list above says which half it is in", pattern)
		}
	}
}

// textOf answers the string literal a call's argument is, and false for anything else.
func textOf(expression ast.Expr) (string, bool) {
	literal, isLiteral := expression.(*ast.BasicLit)
	if !isLiteral || literal.Kind != token.STRING {
		return "", false
	}

	text, err := strconv.Unquote(literal.Value)
	if err != nil {
		return "", false
	}

	return text, true
}

// changes reports whether a mux pattern is a request that changes something.
func changes(pattern string) bool {
	method, _, _ := strings.Cut(pattern, " ")

	return method == http.MethodPost || method == http.MethodPut ||
		method == http.MethodPatch || method == http.MethodDelete
}

// TestTheActionsAreTheHostsOwnNames holds the two strings this module asks about to the host's own
// vocabulary (internal/authz/vocabulary.go): the host refuses a question about a name it does not
// hold, and a rename there would show up here as a failing test rather than as a module whose every
// write answers ACCESS_UNKNOWN.
func TestTheActionsAreTheHostsOwnNames(t *testing.T) {
	if actionConfigureResource != "configure_resource" {
		t.Fatalf("the configuration action is %q, and the host's is configure_resource", actionConfigureResource)
	}

	if actionOperateResource != "operate_resource" {
		t.Fatalf("the acting action is %q, and the host's is operate_resource", actionOperateResource)
	}

	// The kind the manifest declares, and the object type the host's three resource actions are
	// defined on. A question about another kind is a question about a grant this module does not
	// have and cannot be given.
	if objectKind != "sink" || objectTypeResourceKind != "resource_kind" {
		t.Fatalf("this module asks about %s:%s, and its instance is declared under kind sink",
			objectTypeResourceKind, objectKind)
	}
}

// TestAViewerIsRefusedEveryMutatingRoute is the finding this release answers: a viewer may read
// every screen and may change none of them.
func TestAViewerIsRefusedEveryMutatingRoute(t *testing.T) {
	withHost(t, theLadder())

	for route := range mutatingPaths(t) {
		method, path, _ := strings.Cut(route, " ")

		recorder := submit(t, requestAs(t, "user:viewer", method, path))

		if recorder.Code != http.StatusForbidden {
			t.Fatalf("a viewer's %s %s answered %d, want 403: %s",
				method, path, recorder.Code, recorder.Body.String())
		}

		if raised := refusalOf(t, recorder); !strings.HasSuffix(raised.Code, "NOT_PERMITTED") {
			t.Fatalf("a viewer's %s %s was refused with %q, want NOT_PERMITTED", method, path, raised.Code)
		}
	}
}

// TestAnOperatorMayActAndAManagerMayConfigure is the other half: the person the route is for is let
// through, and the person one rung below is not.
//
// An operator holds operate_resource and not configure_resource, which is the host's own ladder — so
// "an operator may do everything" is the wrong assertion and would hide the distinction this release
// exists to make.
func TestAnOperatorMayActAndAManagerMayConfigure(t *testing.T) {
	cases := []struct {
		action  string
		allows  string
		refuses string
	}{
		{action: actionOperateResource, allows: "user:operator", refuses: "user:viewer"},
		{action: actionConfigureResource, allows: "user:manager", refuses: "user:operator"},
	}

	for _, one := range cases {
		t.Run(one.action, func(t *testing.T) {
			withHost(t, theLadder())

			for route, action := range mutatingPaths(t) {
				if action != one.action {
					continue
				}

				method, path, _ := strings.Cut(route, " ")

				allowed := submit(t, requestAs(t, one.allows, method, path))
				if raised := refusalOf(t, allowed); strings.HasSuffix(raised.Code, "NOT_PERMITTED") ||
					strings.HasSuffix(raised.Code, "ACCESS_UNKNOWN") {
					t.Fatalf("%s %s was refused for %s with %s, and %s holds %s",
						method, path, one.allows, raised.Code, one.allows, one.action)
				}

				refused := submit(t, requestAs(t, one.refuses, method, path))
				if refused.Code != http.StatusForbidden {
					t.Fatalf("%s %s answered %s %d for %s, want the refusal 403",
						one.refuses, method, path, refused.Code, path)
				}

				if raised := refusalOf(t, refused); !strings.HasSuffix(raised.Code, "NOT_PERMITTED") {
					t.Fatalf("%s %s was refused for %s with %q, want NOT_PERMITTED",
						method, path, one.refuses, raised.Code)
				}
			}
		})
	}
}

// TestTheHostCannotAnswerIsARefusal is the fail-closed half. A question that could not be asked is
// not an answer, so the change does not happen and the caller is told to try again — never allowed
// because nobody said no.
func TestTheHostCannotAnswerIsARefusal(t *testing.T) {
	withHost(t, &ladder{fails: errors.New("the session is gone")})

	for route := range mutatingPaths(t) {
		method, path, _ := strings.Cut(route, " ")

		recorder := submit(t, requestAs(t, "user:manager", method, path))

		if recorder.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s %s answered %d with the host unable to answer, want 503: %s",
				method, path, recorder.Code, recorder.Body.String())
		}

		if raised := refusalOf(t, recorder); !strings.HasSuffix(raised.Code, "ACCESS_UNKNOWN") {
			t.Fatalf("%s %s answered %q with the host unable to answer, want ACCESS_UNKNOWN",
				method, path, raised.Code)
		}
	}
}

// TestARequestWithNoCallerIsRefused is a request that did not come from the host. Nothing forwarded
// by the host is missing its principal, so this is the shape a route reached another way has — and
// it is refused rather than acted on for a caller nobody can name.
func TestARequestWithNoCallerIsRefused(t *testing.T) {
	withHost(t, allowing{})

	recorder := httptest.NewRecorder()
	routes().ServeHTTP(recorder, requestAs(t, "", http.MethodPost, "/refresh", ""))

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("a request naming no caller answered %d, want 403: %s", recorder.Code, recorder.Body.String())
	}

	if raised := refusalOf(t, recorder); !strings.HasSuffix(raised.Code, "NOT_PERMITTED") {
		t.Fatalf("a request naming no caller was refused with %q, want NOT_PERMITTED", raised.Code)
	}
}

// TestTheQuestionNamesTheKindAndThePerson is what the host is asked, and it is the half a reader
// cannot see from the routes: the action is the host's own word for it, the object is the resource
// kind this module is declared under, and the subject is the person in the request.
func TestTheQuestionNamesTheKindAndThePerson(t *testing.T) {
	host := theLadder()
	withHost(t, host)

	// One route of each kind, through the module's own mux.
	submit(t, requestAs(t, "user:manager", http.MethodPost, "/lights/10.0.0.253/ignore"))
	submit(t, requestAs(t, "user:operator", http.MethodPost, "/refresh"))

	want := []accessapi.CheckParams{
		{Subject: "user:manager", Action: actionConfigureResource, ObjectType: "resource_kind", ObjectID: "sink"},
		{Subject: "user:operator", Action: actionOperateResource, ObjectType: "resource_kind", ObjectID: "sink"},
	}

	if len(host.asked) != len(want) {
		t.Fatalf("the host was asked %d questions, want %d: %+v", len(host.asked), len(want), host.asked)
	}

	for i, asked := range host.asked {
		if asked != want[i] {
			t.Fatalf("question %d is %+v, want %+v", i, asked, want[i])
		}
	}
}

// TestAChangeWithTheQuestionRefusedTouchesNothing is the side effect rather than the answer: a light
// a viewer asked to ignore is left exactly as the store had it, and nothing is read from or written
// to the light.
func TestAChangeWithTheQuestionRefusedTouchesNothing(t *testing.T) {
	withHost(t, theLadder())

	under := newLightsOverStore(t, &probeFleet{answers: map[string]probeAnswer{
		lightKey("10.0.0.253"): {info: wled.Info{Name: "CyberDesk", LEDCount: 48}},
	}}, storedConfig{
		Version: storeVersion,
		Lights:  []storedLight{{Address: "http://10.0.0.253"}},
	})

	recorder := httptest.NewRecorder()
	under.mux.ServeHTTP(recorder, requestAs(t, "user:viewer", http.MethodPost, "/lights/10.0.0.253/ignore"))

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("a viewer ignoring a light answered %d, want 403: %s", recorder.Code, recorder.Body.String())
	}

	if len(under.fleet.asked) != 0 {
		t.Fatalf("nothing may be probed for a caller who may not ignore a light, and %v was", under.fleet.asked)
	}

	lights := under.storedLights(t)
	if len(lights) != 1 || lights[0].Ignored {
		t.Fatalf("the store holds %+v after a refused ignore, want the light still not ignored", lights)
	}
}

// TestTheAnswerIsKeptForAMoment is the cache: a screen's handful of writes is one question, not one
// per keystroke. It is kept for accessKept and no longer.
func TestTheAnswerIsKeptForAMoment(t *testing.T) {
	host := theLadder()
	withHost(t, host)

	submit(t, requestAs(t, "user:operator", http.MethodPost, "/refresh"))
	submit(t, requestAs(t, "user:operator", http.MethodPost, "/refresh"))

	if len(host.asked) != 1 {
		t.Fatalf("the same question was asked %d times, want once while the answer is kept: %+v",
			len(host.asked), host.asked)
	}
}
