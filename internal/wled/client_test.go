package wled

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// infoBody is a controller's /json/info as WLED 0.14 answers it, cut down to the
// fields this package reads plus the ones it must ignore. The unknown keys are
// here on purpose: every firmware adds some, and a client that refused an answer
// over one would break on an upgrade.
const infoBody = `{
  "ver": "0.14.0",
  "vid": 2310160,
  "leds": {"count": 30, "countP": 30, "pwr": 0, "fps": 42, "maxpwr": 850, "maxseg": 16, "seglc": [1], "lc": 1, "rgbw": false},
  "str": false,
  "name": "desk-strip",
  "udpport": 21324,
  "live": false,
  "lm": "",
  "lip": "",
  "ws": 3,
  "fxcount": 118,
  "palcount": 61,
  "maps": [{"id": 0}],
  "wifi": {"bssid": "9A:2A:6F:C4:FF:51", "rssi": -43, "signal": 100, "channel": 11},
  "fs": {"u": 36, "t": 10354},
  "arch": "ESP32",
  "core": "v4.4.4",
  "mac": "30eda02f6724"
}`

// stateBody is a controller's /json/state with two segments: the first carries
// RGB arrays, the second hex strings, which is the pair of shapes WLED answers
// with depending on how each was last written.
const stateBody = `{
  "on": true,
  "bri": 128,
  "transition": 7,
  "mainseg": 0,
  "seg": [
    {"id": 0, "start": 0, "stop": 20, "len": 20, "grp": 1, "on": true, "bri": 200, "col": [[255, 160, 0], [0, 0, 0], [0, 0, 0]], "fx": 0, "sel": true},
    {"id": 1, "start": 20, "stop": 30, "len": 10, "on": true, "col": ["FF0000", "#00FF00"], "fx": 3}
  ]
}`

// serve starts a controller that answers every request with one body, and hands
// back the base URL to give New.
func serve(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))

	t.Cleanup(server.Close)

	return server
}

// client is a New that failed the test rather than the caller.
func client(t *testing.T, baseURL string, opts ...Option) *Client {
	t.Helper()

	built, err := New(baseURL, opts...)
	if err != nil {
		t.Fatalf("New(%q): %v", baseURL, err)
	}

	return built
}

// TestInfoReadsTheDeviceFactsAndIgnoresTheRest proves the fields this package
// needs come out of a real answer, and that the keys it has never heard of —
// wifi, fs, maps, fxcount, and the rest of a firmware's own business — are not an
// error.
func TestInfoReadsTheDeviceFactsAndIgnoresTheRest(t *testing.T) {
	server := serve(t, http.StatusOK, infoBody)

	info, err := client(t, server.URL).Info(context.Background())
	if err != nil {
		t.Fatalf("Info: %v", err)
	}

	want := Info{
		Version:     "0.14.0",
		Name:        "desk-strip",
		LEDCount:    30,
		MaxSegments: 16,
		UDPPort:     21324,
		Live:        false,
	}

	if info != want {
		t.Fatalf("Info read %+v, want %+v", info, want)
	}
}

// TestInfoToleratesAnAnswerThatOmitsOptionalFields proves a controller that
// publishes only what it has is readable: the missing fields read as zero rather
// than failing the call, so an older firmware is a smaller picture and not an
// outage.
func TestInfoToleratesAnAnswerThatOmitsOptionalFields(t *testing.T) {
	server := serve(t, http.StatusOK, `{"ver": "0.13.1"}`)

	info, err := client(t, server.URL).Info(context.Background())
	if err != nil {
		t.Fatalf("Info: %v", err)
	}

	if info.Version != "0.13.1" {
		t.Fatalf("Version reads %q, want 0.13.1", info.Version)
	}

	if info.LEDCount != 0 || info.MaxSegments != 0 || info.Name != "" || info.UDPPort != 0 || info.Live {
		t.Fatalf("the fields the answer omitted read %+v, want their zero values", info)
	}
}

// TestStateReadsTheStripAndBothColourShapes proves the read half of the state:
// power, brightness, transition, and the segments with their bounds, effect,
// brightness, and colours in both shapes WLED writes them.
func TestStateReadsTheStripAndBothColourShapes(t *testing.T) {
	server := serve(t, http.StatusOK, stateBody)

	state, err := client(t, server.URL).State(context.Background())
	if err != nil {
		t.Fatalf("State: %v", err)
	}

	if !state.On || state.Brightness != 128 || state.Transition != 7 {
		t.Fatalf("State reads on=%v bri=%d transition=%d, want true 128 7",
			state.On, state.Brightness, state.Transition)
	}

	if len(state.Segments) != 2 {
		t.Fatalf("State read %d segments, want 2", len(state.Segments))
	}

	first := state.Segments[0]
	if first.ID != 0 || first.Start != 0 || first.Stop != 20 || !first.On || first.Effect != 0 || first.Brightness != 200 {
		t.Fatalf("segment 0 reads %+v, want id 0, 0-20, on, effect 0, brightness 200", first)
	}

	if len(first.Colors) != 3 || first.Colors[0] != (RGB{R: 255, G: 160, B: 0}) {
		t.Fatalf("segment 0 colours read %v, want three starting at rgb(255,160,0)", first.Colors)
	}

	second := state.Segments[1]
	if second.ID != 1 || second.Start != 20 || second.Stop != 30 || second.Effect != 3 {
		t.Fatalf("segment 1 reads %+v, want id 1, 20-30, effect 3", second)
	}

	wantHex := []RGB{{R: 255, G: 0, B: 0}, {R: 0, G: 255, B: 0}}
	if len(second.Colors) != len(wantHex) || second.Colors[0] != wantHex[0] || second.Colors[1] != wantHex[1] {
		t.Fatalf("segment 1 colours read %v, want %v", second.Colors, wantHex)
	}
}

// TestABooleanCheckValueReadsAsANumber is the shape WLED 16.0.1 answers a segment in: the three
// check values and the transpose code are booleans, not the numbers the documentation writes.
// The read failed on the operator's own desk light over "o1" before this existed, and a module
// that cannot read a segment cannot borrow one either.
func TestABooleanCheckValueReadsAsANumber(t *testing.T) {
	const body = `{
  "on": true, "bri": 255, "transition": 7, "mainseg": 1,
  "seg": [
    {"id": 1, "start": 4, "stop": 14, "len": 10, "grp": 1, "on": true, "bri": 255, "n": "Back 1",
     "col": [[255,255,255],[0,0,0],[0,0,0]], "fx": 0, "sx": 128, "ix": 128, "pal": 0,
     "c1": 128, "c2": 128, "c3": 16, "tp": false, "o1": false, "o2": true, "o3": false, "m12": 0}
  ]
}`

	server := serve(t, http.StatusOK, body)

	state, err := client(t, server.URL).State(context.Background())
	if err != nil {
		t.Fatalf("State: %v", err)
	}

	if len(state.Segments) != 1 {
		t.Fatalf("State read %d segments, want 1", len(state.Segments))
	}

	segment := state.Segments[0]
	if segment.ID != 1 || segment.Start != 4 || segment.Stop != 14 || segment.Name != "Back 1" {
		t.Fatalf("the segment reads %+v, want id 1, 4-14, named Back 1", segment)
	}

	if segment.Check1 != 0 || segment.Check2 != 1 || segment.Check3 != 0 || segment.Transpose != 0 {
		t.Fatalf("the checks read %d %d %d and the transpose %d, want 0 1 0 and 0",
			segment.Check1, segment.Check2, segment.Check3, segment.Transpose)
	}
}

// TestRestoreSegmentWritesTheSegmentsOwnStateBack is the exact body that gives a borrowed
// segment back. Every field the module can overwrite is named — the run, whether it is drawn,
// its colours, its effect, its name and its own brightness — and the effect's parameters are
// deliberately absent, because this package never writes them.
func TestRestoreSegmentWritesTheSegmentsOwnStateBack(t *testing.T) {
	var (
		method string
		path   string
		body   string
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path

		raw, _ := io.ReadAll(r.Body)
		body = string(raw)
		_, _ = io.WriteString(w, `{"success":true}`)
	}))
	t.Cleanup(server.Close)

	was := Segment{
		ID:         1,
		Start:      4,
		Stop:       14,
		On:         true,
		Colors:     []RGB{{R: 255, G: 255, B: 255}},
		Effect:     0,
		Name:       "Back 1",
		Brightness: 255,
	}

	if err := client(t, server.URL).RestoreSegment(context.Background(), was); err != nil {
		t.Fatalf("RestoreSegment: %v", err)
	}

	if method != http.MethodPost || path != "/json/state" {
		t.Fatalf("RestoreSegment sent %s %s, want POST /json/state", method, path)
	}

	want := `{"seg":[{"id":1,"start":4,"stop":14,"on":true,"col":[[255,255,255]],"bri":255,"fx":0,"n":"Back 1"}]}`
	if body != want {
		t.Fatalf("RestoreSegment sent\n%s\nwant\n%s", body, want)
	}
}

// TestRestoreSegmentLeavesABrightnessItNeverHad is the one field a restore must not invent. A
// segment's own brightness of zero means it inherits the strip's; writing zero back as a value
// would put the segment at no light at all.
func TestRestoreSegmentLeavesABrightnessItNeverHad(t *testing.T) {
	var body string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body = string(raw)
		_, _ = io.WriteString(w, `{"success":true}`)
	}))
	t.Cleanup(server.Close)

	if err := client(t, server.URL).RestoreSegment(context.Background(), Segment{ID: 0, Start: 0, Stop: 4, On: true}); err != nil {
		t.Fatalf("RestoreSegment: %v", err)
	}

	if strings.Contains(body, `"bri"`) {
		t.Fatalf("RestoreSegment named a brightness the segment never had: %s", body)
	}
}

// TestSetRangeWritesTheExactBody proves the primitive end to end: the method, the
// path, and the body WLED documents, byte for byte. A range is the one call whose
// numbers are the whole point, so an exact body is the assertion worth making.
func TestSetRangeWritesTheExactBody(t *testing.T) {
	var (
		method string
		path   string
		body   string
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path

		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read the request body: %v", err)
		}

		body = string(raw)
		_, _ = io.WriteString(w, `{"success":true}`)
	}))
	t.Cleanup(server.Close)

	err := client(t, server.URL).SetRange(context.Background(), 3, 5, RGB{R: 255, G: 160, B: 0})
	if err != nil {
		t.Fatalf("SetRange: %v", err)
	}

	if method != http.MethodPost || path != "/json/state" {
		t.Fatalf("SetRange sent %s %s, want POST /json/state", method, path)
	}

	want := `{"seg":[{"id":0,"start":3,"stop":8,"on":true,"col":[[255,160,0]],"fx":0}]}`
	if body != want {
		t.Fatalf("SetRange sent\n%s\nwant\n%s", body, want)
	}
}

// TestSetRangeClampsToTheStripLength proves the clamp: a run that reaches past the
// end of a strip whose length was supplied stops at the last LED instead of asking
// for LEDs that are not there, which WLED would accept and then answer oddly.
func TestSetRangeClampsToTheStripLength(t *testing.T) {
	var body string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body = string(raw)
		_, _ = io.WriteString(w, `{"success":true}`)
	}))
	t.Cleanup(server.Close)

	err := client(t, server.URL).SetRange(context.Background(), 10, 5, RGB{R: 0, G: 0, B: 255}, WithRangeLEDCount(12))
	if err != nil {
		t.Fatalf("SetRange: %v", err)
	}

	want := `{"seg":[{"id":0,"start":10,"stop":12,"on":true,"col":[[0,0,255]],"fx":0}]}`
	if body != want {
		t.Fatalf("SetRange sent\n%s\nwant\n%s", body, want)
	}
}

// TestSetRangeRefusesAnEmptyRun proves a run with nothing in it is refused here
// rather than sent: WLED deletes a segment whose stop is not past its start, so a
// count of zero would not light zero LEDs, it would take the strip's segment away.
func TestSetRangeRefusesAnEmptyRun(t *testing.T) {
	var called bool

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		_, _ = io.WriteString(w, `{"success":true}`)
	}))
	t.Cleanup(server.Close)

	clientUnderTest := client(t, server.URL)

	for _, run := range []struct {
		name  string
		first int
		count int
		total int
	}{
		{"no leds at all", 0, 0, 0},
		{"a negative count", 4, -1, 0},
		{"a run before the strip", -2, 4, 0},
		{"a run past the end", 30, 5, 12},
	} {
		err := clientUnderTest.SetRange(context.Background(), run.first, run.count, RGB{R: 1, G: 2, B: 3},
			WithRangeLEDCount(run.total))
		if !errors.Is(err, ErrInvalidRange) {
			t.Fatalf("%s: SetRange returned %v, want ErrInvalidRange", run.name, err)
		}
	}

	if called {
		t.Fatal("a refused run was sent to the controller anyway")
	}
}

// TestSetBrightnessRejectsWhatIsNotABrightness proves the range check on both ends
// and that nothing leaves the process when it fails: 256 and -1 are the caller's
// arithmetic going wrong, and WLED would clamp both without saying so.
func TestSetBrightnessRejectsWhatIsNotABrightness(t *testing.T) {
	var requests int

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		_, _ = io.WriteString(w, `{"success":true}`)
	}))
	t.Cleanup(server.Close)

	clientUnderTest := client(t, server.URL)

	for _, brightness := range []int{256, -1, 1000} {
		err := clientUnderTest.SetBrightness(context.Background(), brightness)
		if !errors.Is(err, ErrInvalidBrightness) {
			t.Fatalf("SetBrightness(%d) returned %v, want ErrInvalidBrightness", brightness, err)
		}
	}

	if requests != 0 {
		t.Fatalf("a refused brightness reached the controller %d times, want 0", requests)
	}

	// The ends of the range are valid and do travel.
	for _, brightness := range []int{0, 255} {
		if err := clientUnderTest.SetBrightness(context.Background(), brightness); err != nil {
			t.Fatalf("SetBrightness(%d): %v", brightness, err)
		}
	}

	if requests != 2 {
		t.Fatalf("two valid brightnesses reached the controller %d times, want 2", requests)
	}
}

// TestSetSolidPaintsTheStripInOneWrite proves the whole-strip call: one body that
// carries the colour, the brightness and the fade together, with the fade in
// WLED's 100 ms units — 700 ms is 7 — and the solid effect so the strip is not left
// running whatever animation it was on.
func TestSetSolidPaintsTheStripInOneWrite(t *testing.T) {
	var body string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body = string(raw)
		_, _ = io.WriteString(w, `{"success":true}`)
	}))
	t.Cleanup(server.Close)

	err := client(t, server.URL).SetSolid(context.Background(), RGB{R: 0, G: 255, B: 0},
		WithSolidBrightness(128), WithSolidFade(700), WithSolidLEDCount(30))
	if err != nil {
		t.Fatalf("SetSolid: %v", err)
	}

	want := `{"bri":128,"transition":7,"seg":[{"id":0,"start":0,"stop":30,"on":true,"col":[[0,255,0]],"fx":0}]}`
	if body != want {
		t.Fatalf("SetSolid sent\n%s\nwant\n%s", body, want)
	}
}

// TestFadeUnitsRoundsUp proves the conversion at its edges: no fade stays no fade,
// and a fade anyone asked for becomes at least one unit rather than being rounded
// away to none.
func TestFadeUnitsRoundsUp(t *testing.T) {
	for _, transition := range []struct {
		milliseconds int
		want         int
	}{
		{0, 0},
		{-500, 0},
		{1, 1},
		{50, 1},
		{100, 1},
		{101, 2},
		{700, 7},
		{1500, 15},
	} {
		if got := fadeUnits(transition.milliseconds); got != transition.want {
			t.Fatalf("fadeUnits(%d) = %d, want %d", transition.milliseconds, got, transition.want)
		}
	}
}

// TestSetPowerWritesPowerAlone proves the smallest write carries only the key it
// means: WLED changes only what a body names, so a body that said anything else
// would change something nobody asked about.
func TestSetPowerWritesPowerAlone(t *testing.T) {
	var body string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body = string(raw)
		_, _ = io.WriteString(w, `{"success":true}`)
	}))
	t.Cleanup(server.Close)

	if err := client(t, server.URL).SetPower(context.Background(), false); err != nil {
		t.Fatalf("SetPower: %v", err)
	}

	if body != `{"on":false}` {
		t.Fatalf("SetPower sent %s, want {\"on\":false}", body)
	}
}

// TestATimeoutIsTypedAndReturnsPromptly proves the deadline is real and that the
// call comes back when it expires rather than when the controller finally answers.
// The controller here takes 30s to answer and the client is given 150ms: a call
// that waited for the controller would hang the host's 5s ping and cost the module
// three misses.
func TestATimeoutIsTypedAndReturnsPromptly(t *testing.T) {
	const timeout = 150 * time.Millisecond

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(30 * time.Second):
			_, _ = io.WriteString(w, infoBody)
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(server.Close)

	started := time.Now()

	_, err := client(t, server.URL, WithTimeout(timeout)).Info(context.Background())
	elapsed := time.Since(started)

	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("Info against a silent controller returned %v, want ErrTimeout", err)
	}

	var timedOut *TimeoutError
	if !errors.As(err, &timedOut) {
		t.Fatalf("errors.As did not find *TimeoutError in %v", err)
	}

	if elapsed > 2*time.Second {
		t.Fatalf("the call took %s with a %s timeout, want roughly the timeout and not the controller's 30s",
			elapsed, timeout)
	}

	// A timeout is not a refusal and not an unreachable controller: a caller
	// switching on these is deciding between retrying and giving up.
	if errors.Is(err, ErrUnreachable) {
		t.Fatalf("%v also reads as ErrUnreachable, want the two apart", err)
	}
}

// TestAFailingStatusIsTyped proves a 500 with a body that is not WLED's own refusal
// surfaces as a status error carrying the code, so a log line and a caller both see
// what came back rather than "it did not work".
func TestAFailingStatusIsTyped(t *testing.T) {
	server := serve(t, http.StatusInternalServerError, `{"detail":"the controller is restarting"}`)

	_, err := client(t, server.URL).State(context.Background())
	if !errors.Is(err, ErrStatus) {
		t.Fatalf("State against a 500 returned %v, want ErrStatus", err)
	}

	var status *StatusError
	if !errors.As(err, &status) {
		t.Fatalf("errors.As did not find *StatusError in %v", err)
	}

	if status.StatusCode != http.StatusInternalServerError {
		t.Fatalf("the status error carries %d, want 500", status.StatusCode)
	}

	if !strings.Contains(status.Body, "restarting") {
		t.Fatalf("the status error body reads %q, want the controller's answer in it", status.Body)
	}

	if errors.Is(err, ErrDevice) {
		t.Fatalf("%v also reads as ErrDevice, want only a status error", err)
	}
}

// TestAWLEDErrorBodyIsTyped proves a refusal WLED sends with a 200 becomes a device
// error carrying WLED's own code, which is the only thing that says which check
// failed. The body is the shape WLED documents for a rejected write.
func TestAWLEDErrorBodyIsTyped(t *testing.T) {
	server := serve(t, http.StatusOK, `{"error":3}`)

	err := client(t, server.URL).SetPower(context.Background(), true)
	if !errors.Is(err, ErrDevice) {
		t.Fatalf("SetPower against a refusal returned %v, want ErrDevice", err)
	}

	var refusal *DeviceError
	if !errors.As(err, &refusal) {
		t.Fatalf("errors.As did not find *DeviceError in %v", err)
	}

	if refusal.Code != 3 {
		t.Fatalf("the device error carries code %d, want 3", refusal.Code)
	}

	if refusal.StatusCode != 0 {
		t.Fatalf("the device error carries HTTP %d, want 0 for a 200", refusal.StatusCode)
	}
}

// TestAConnectionRefusedIsUnreachable proves the other side of the transport split:
// a controller at an address nothing is listening on reads as unreachable, not as
// a timeout, because the answer arrived immediately and was "no".
func TestAConnectionRefusedIsUnreachable(t *testing.T) {
	// Bind a port and give it back, so the address is one this test knows is free
	// without depending on a number being unused on the machine.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("find a free port: %v", err)
	}

	address := listener.Addr().String()
	if err = listener.Close(); err != nil {
		t.Fatalf("close the free port: %v", err)
	}

	_, err = client(t, "http://"+address).Info(context.Background())
	if !errors.Is(err, ErrUnreachable) {
		t.Fatalf("Info against a refused connection returned %v, want ErrUnreachable", err)
	}

	var unreachable *UnreachableError
	if !errors.As(err, &unreachable) {
		t.Fatalf("errors.As did not find *UnreachableError in %v", err)
	}

	if errors.Is(err, ErrTimeout) {
		t.Fatalf("%v also reads as ErrTimeout, want the two apart", err)
	}
}

// TestNewRefusesAnAddressThatIsNotOne proves the constructor's check: a base URL
// that is not http or https is refused at start-up with no request made, because a
// typo in an address is a configuration mistake and a process that will not start
// is reported far more clearly than one that will not talk to a light.
func TestNewRefusesAnAddressThatIsNotOne(t *testing.T) {
	for _, address := range []string{"", "   ", "10.0.0.136", "ftp://10.0.0.136", "http://"} {
		if _, err := New(address); err == nil {
			t.Fatalf("New(%q) accepted an address that is not one", address)
		}
	}

	built, err := New("http://10.0.0.136/")
	if err != nil {
		t.Fatalf("New with a trailing slash: %v", err)
	}

	if built.base != "http://10.0.0.136" {
		t.Fatalf("the base reads %q, want the trailing slash trimmed", built.base)
	}
}

// TestTheClientSerialisesItsCalls proves the queue the package comment promises: two
// goroutines calling one Client do not overlap at the controller, which is what WLED
// asks for and what two interleaved writes would break.
func TestTheClientSerialisesItsCalls(t *testing.T) {
	var (
		mu       sync.Mutex
		inside   bool
		overlaps int
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		if inside {
			overlaps++
		}

		inside = true
		mu.Unlock()

		time.Sleep(20 * time.Millisecond)

		mu.Lock()
		inside = false
		mu.Unlock()

		_, _ = io.WriteString(w, infoBody)
	}))
	t.Cleanup(server.Close)

	clientUnderTest := client(t, server.URL)

	var wait sync.WaitGroup

	for range 4 {
		wait.Add(1)

		go func() {
			defer wait.Done()

			if _, err := clientUnderTest.Info(context.Background()); err != nil {
				t.Errorf("Info: %v", err)
			}
		}()
	}

	wait.Wait()

	if overlaps != 0 {
		t.Fatalf("%d calls overlapped at the controller, want them serialised", overlaps)
	}
}

// panelStateBody is what a 2D controller answers: the segment is a rectangle, so it
// carries row bounds beside the LED bounds. The numbers are the ones the panel this
// module is meant to drive reported.
const panelStateBody = `{"on":true,"bri":128,"transition":7,"mainseg":0,"seg":[
  {"id":0,"start":0,"stop":256,"startY":0,"stopY":64,"len":256,"grp":1,"spc":0,"of":7,"on":true,"frz":false,"bri":255,"cct":127,"set":0,"col":[[255,0,0],[0,0,0],[0,0,0]],"fx":153,"sx":169,"ix":207,"pal":0,"sel":true}
]}`

// TestInfoCarriesTheMatrixGeometry proves the facts that let one client serve both a
// few-pixel strip and a 16,384-pixel panel: the geometry when the controller
// publishes it, zero when it does not, and zero rather than an error when it
// publishes something this package cannot read — the LED count of such a controller
// is still worth having.
func TestInfoCarriesTheMatrixGeometry(t *testing.T) {
	for _, panel := range []struct {
		name   string
		body   string
		leds   int
		width  int
		height int
	}{
		{
			name:   "a panel",
			body:   `{"ver":"14.7.0-mdev","name":"morpheus","leds":{"count":16384,"maxseg":32,"matrix":{"w":256,"h":64},"rgbw":false},"udpport":21324,"live":false}`,
			leds:   16384,
			width:  256,
			height: 64,
		},
		{
			name: "a strip, which publishes no matrix at all",
			body: `{"ver":"0.14.0","leds":{"count":30,"maxseg":16}}`,
			leds: 30,
		},
		{
			name: "a matrix this package cannot read",
			body: `{"ver":"0.14.0","leds":{"count":30,"maxseg":16,"matrix":"nonsense"}}`,
			leds: 30,
		},
	} {
		server := serve(t, http.StatusOK, panel.body)

		info, err := client(t, server.URL).Info(context.Background())
		if err != nil {
			t.Fatalf("%s: Info: %v", panel.name, err)
		}

		if info.MatrixWidth != panel.width || info.MatrixHeight != panel.height {
			t.Fatalf("%s: the matrix reads %dx%d, want %dx%d",
				panel.name, info.MatrixWidth, info.MatrixHeight, panel.width, panel.height)
		}

		if info.LEDCount != panel.leds {
			t.Fatalf("%s: the LED count reads %d, want %d", panel.name, info.LEDCount, panel.leds)
		}
	}
}

// TestStateCarriesTheRowBoundsOfAPanel proves the segment model carries both axes: on
// a panel a segment is a rectangle, and its row bounds are read beside its LED bounds
// so a caller can tell a full-panel segment from a band of one.
func TestStateCarriesTheRowBoundsOfAPanel(t *testing.T) {
	server := serve(t, http.StatusOK, panelStateBody)

	state, err := client(t, server.URL).State(context.Background())
	if err != nil {
		t.Fatalf("State: %v", err)
	}

	if len(state.Segments) != 1 {
		t.Fatalf("State read %d segments, want 1", len(state.Segments))
	}

	segment := state.Segments[0]
	if segment.Start != 0 || segment.Stop != 256 {
		t.Fatalf("segment 0 covers LEDs %d-%d, want 0-256", segment.Start, segment.Stop)
	}

	if segment.StartY != 0 || segment.StopY != 64 {
		t.Fatalf("segment 0 covers rows %d-%d, want 0-64", segment.StartY, segment.StopY)
	}
}

// TestADecodeFailureKeepsTheCause proves a body that is not the JSON it should be is
// reported with the endpoint and the parser's own error, rather than a bare "bad
// answer" that leaves a person guessing which endpoint and which byte.
func TestADecodeFailureKeepsTheCause(t *testing.T) {
	server := serve(t, http.StatusOK, `<html>not json</html>`)

	_, err := client(t, server.URL).Info(context.Background())
	if err == nil {
		t.Fatal("Info accepted a body that is not JSON")
	}

	var syntax *json.SyntaxError
	if !errors.As(err, &syntax) {
		t.Fatalf("the decode error does not carry the parser's own error: %v", err)
	}
}

// capture is a controller that records every request it is given and answers
// success, so a test can assert the body that was actually sent.
type capture struct {
	methods []string
	paths   []string
	bodies  []string
}

// server starts a controller that records into the capture and answers success.
func (c *capture) server(t *testing.T) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read the request body: %v", err)
		}

		c.methods = append(c.methods, r.Method)
		c.paths = append(c.paths, r.URL.Path)
		c.bodies = append(c.bodies, string(raw))

		_, _ = io.WriteString(w, `{"success":true}`)
	}))
	t.Cleanup(server.Close)

	return server
}

// TestSetEffectWritesEveryParameterAndBothAxes proves the tier-1 write end to end:
// the effect, all nine of its parameters including the zeros, and all four bounds of
// the rectangle it runs in, in one body. The zeros matter: a parameter left out would
// leave the previous effect's value behind, so a body that omitted o2 and o3 would not
// be the body this test wants to see.
func TestSetEffectWritesEveryParameterAndBothAxes(t *testing.T) {
	recorder := &capture{}
	server := recorder.server(t)

	effect := Effect{
		ID:        122, // Scrolling Text
		Speed:     128,
		Intensity: 16,
		Custom1:   7,
		Custom2:   2, // font size
		Custom3:   3,
		Check1:    1,
		Check2:    0,
		Check3:    0,
		Palette:   5,
	}

	err := client(t, server.URL).SetEffect(context.Background(), effect,
		WithEffectBounds(Bounds{Start: 0, Stop: 256, StartY: 0, StopY: 64}))
	if err != nil {
		t.Fatalf("SetEffect: %v", err)
	}

	if len(recorder.methods) != 1 || recorder.methods[0] != http.MethodPost || recorder.paths[0] != "/json/state" {
		t.Fatalf("SetEffect sent %v %v, want one POST /json/state", recorder.methods, recorder.paths)
	}

	want := `{"seg":[{"id":0,"start":0,"stop":256,"startY":0,"stopY":64,"on":true,` +
		`"fx":122,"sx":128,"ix":16,"c1":7,"c2":2,"c3":3,"o1":1,"o2":0,"o3":0,"pal":5}]}`

	if recorder.bodies[0] != want {
		t.Fatalf("SetEffect sent\n%s\nwant\n%s", recorder.bodies[0], want)
	}
}

// TestSetEffectCarriesTheGroupingThatScalesTheWord is the one part of an effect that is
// not a slider: "grp", which on a panel multiplies the text effect's glyphs. It travels
// with the effect because it is the effect's scale, and it is left out when it is zero so
// that a caller which does not care keeps whatever the controller holds.
func TestSetEffectCarriesTheGroupingThatScalesTheWord(t *testing.T) {
	recorder := &capture{}
	server := recorder.server(t)

	word := Effect{ID: 122, Speed: 128, Intensity: 24, Custom2: 0}

	err := client(t, server.URL).SetEffect(context.Background(), word,
		WithEffectBounds(Bounds{Start: 0, Stop: 256, StartY: 0, StopY: 64}),
		WithEffectGrouping(4))
	if err != nil {
		t.Fatalf("SetEffect: %v", err)
	}

	want := `{"seg":[{"id":0,"start":0,"stop":256,"startY":0,"stopY":64,"on":true,` +
		`"fx":122,"sx":128,"ix":24,"c1":0,"c2":0,"c3":0,"o1":0,"o2":0,"o3":0,"pal":0,"grp":4}]}`

	if recorder.bodies[0] != want {
		t.Fatalf("SetEffect sent\n%s\nwant\n%s", recorder.bodies[0], want)
	}

	if err := client(t, server.URL).SetEffect(context.Background(), word); err != nil {
		t.Fatalf("SetEffect without a grouping: %v", err)
	}

	if strings.Contains(recorder.bodies[1], "grp") {
		t.Fatalf("a grouping of zero was written anyway: %s", recorder.bodies[1])
	}
}

// TestSetEffectRefusesAParameterOutsideAByte proves an effect parameter outside 0-255
// is refused before anything is sent: a controller stores these in one byte and would
// cut the number down without saying so.
func TestSetEffectRefusesAParameterOutsideAByte(t *testing.T) {
	recorder := &capture{}
	server := recorder.server(t)

	clientUnderTest := client(t, server.URL)

	for _, effect := range []Effect{
		{ID: 256},
		{ID: 1, Speed: -1},
		{ID: 1, Intensity: 300},
		{ID: 1, Palette: 256},
	} {
		if err := clientUnderTest.SetEffect(context.Background(), effect); !errors.Is(err, ErrInvalidEffect) {
			t.Fatalf("SetEffect(%+v) returned %v, want ErrInvalidEffect", effect, err)
		}
	}

	if len(recorder.bodies) != 0 {
		t.Fatalf("a refused effect reached the controller %d times, want 0", len(recorder.bodies))
	}
}

// TestSetSegmentNameWritesTheTextCarrier proves the string the text effect draws is
// the segment's n field, and that the write carries nothing else — no bounds, so a
// panel's rectangle is left exactly as it was.
func TestSetSegmentNameWritesTheTextCarrier(t *testing.T) {
	recorder := &capture{}
	server := recorder.server(t)

	if err := client(t, server.URL).SetSegmentName(context.Background(), "PRINTING 94%"); err != nil {
		t.Fatalf("SetSegmentName: %v", err)
	}

	if len(recorder.bodies) != 1 || recorder.methods[0] != http.MethodPost || recorder.paths[0] != "/json/state" {
		t.Fatalf("SetSegmentName sent %v %v, want one POST /json/state", recorder.methods, recorder.paths)
	}

	want := `{"seg":[{"id":0,"n":"PRINTING 94%"}]}`
	if recorder.bodies[0] != want {
		t.Fatalf("SetSegmentName sent\n%s\nwant\n%s", recorder.bodies[0], want)
	}
}

// TestSetSegmentNameRefusesWhatIsNotAName proves the cap is enforced rather than
// truncated: 48 characters is a name and travels, and an empty, blank or 49-character
// one is refused with nothing sent. Half a word on a panel is not a status.
func TestSetSegmentNameRefusesWhatIsNotAName(t *testing.T) {
	recorder := &capture{}
	server := recorder.server(t)

	clientUnderTest := client(t, server.URL)

	for _, name := range []string{"", "   	 ", strings.Repeat("A", MaxSegmentName+1)} {
		if err := clientUnderTest.SetSegmentName(context.Background(), name); !errors.Is(err, ErrInvalidName) {
			t.Fatalf("SetSegmentName(%q) returned %v, want ErrInvalidName", name, err)
		}
	}

	if len(recorder.bodies) != 0 {
		t.Fatalf("a refused name reached the controller %d times, want 0", len(recorder.bodies))
	}

	if err := clientUnderTest.SetSegmentName(context.Background(), strings.Repeat("A", MaxSegmentName)); err != nil {
		t.Fatalf("SetSegmentName with %d characters: %v", MaxSegmentName, err)
	}

	if len(recorder.bodies) != 1 {
		t.Fatalf("a name of exactly %d characters did not travel", MaxSegmentName)
	}
}

// TestSetRectangleWritesTheWholeGeometry proves the tier-2 write: all four bounds, the
// colour, the solid effect so the rectangle is not filled by an animation, and the
// three geometry controls — grouping, transpose and the 1D expansion — in one body.
func TestSetRectangleWritesTheWholeGeometry(t *testing.T) {
	recorder := &capture{}
	server := recorder.server(t)

	err := client(t, server.URL).SetRectangle(context.Background(),
		Bounds{Start: 0, Stop: 256, StartY: 0, StopY: 64},
		RGB{R: 0, G: 255, B: 0},
		WithRectangleGrouping(2),
		WithRectangleTranspose(1),
		WithRectangleExpand1D(ExpandCorner))
	if err != nil {
		t.Fatalf("SetRectangle: %v", err)
	}

	if len(recorder.bodies) != 1 || recorder.methods[0] != http.MethodPost || recorder.paths[0] != "/json/state" {
		t.Fatalf("SetRectangle sent %v %v, want one POST /json/state", recorder.methods, recorder.paths)
	}

	want := `{"seg":[{"id":0,"start":0,"stop":256,"startY":0,"stopY":64,"on":true,` +
		`"col":[[0,255,0]],"fx":0,"grp":2,"tp":1,"m12":3}]}`

	if recorder.bodies[0] != want {
		t.Fatalf("SetRectangle sent\n%s\nwant\n%s", recorder.bodies[0], want)
	}
}

// TestSetRectangleRefusesWhatIsNotARectangle proves an empty or inverted rectangle
// never leaves the process. A controller reads a segment whose stop is not past its
// start as a segment to delete, so sending one would take the panel's segment away
// rather than draw nothing on it.
func TestSetRectangleRefusesWhatIsNotARectangle(t *testing.T) {
	recorder := &capture{}
	server := recorder.server(t)

	clientUnderTest := client(t, server.URL)

	for _, rectangle := range []struct {
		name   string
		bounds Bounds
	}{
		{"the zero value", Bounds{}},
		{"inverted columns", Bounds{Start: 10, Stop: 5, StartY: 0, StopY: 1}},
		{"inverted rows", Bounds{Start: 0, Stop: 4, StartY: 5, StopY: 2}},
		{"no rows at all", Bounds{Start: 0, Stop: 4, StartY: 7, StopY: 7}},
		{"a negative column", Bounds{Start: -1, Stop: 4, StartY: 0, StopY: 1}},
	} {
		err := clientUnderTest.SetRectangle(context.Background(), rectangle.bounds, RGB{R: 1, G: 2, B: 3})
		if !errors.Is(err, ErrInvalidRectangle) {
			t.Fatalf("%s: SetRectangle returned %v, want ErrInvalidRectangle", rectangle.name, err)
		}
	}

	if len(recorder.bodies) != 0 {
		t.Fatalf("a refused rectangle reached the controller %d times, want 0", len(recorder.bodies))
	}
}

// TestTheColourWritesCarryPanelRows proves the row options on the two existing colour
// writes: with rows supplied, SetSolid and SetRange each carry all four bounds, so a
// panel draws a band instead of the single row a columns-only body would collapse to.
func TestTheColourWritesCarryPanelRows(t *testing.T) {
	recorder := &capture{}
	server := recorder.server(t)

	clientUnderTest := client(t, server.URL)

	err := clientUnderTest.SetSolid(context.Background(), RGB{R: 0, G: 255, B: 0},
		WithSolidBrightness(128), WithSolidFade(700), WithSolidLEDCount(30), WithSolidRows(0, 64))
	if err != nil {
		t.Fatalf("SetSolid: %v", err)
	}

	wantSolid := `{"bri":128,"transition":7,"seg":[{"id":0,"start":0,"stop":30,"startY":0,"stopY":64,` +
		`"on":true,"col":[[0,255,0]],"fx":0}]}`
	if recorder.bodies[0] != wantSolid {
		t.Fatalf("SetSolid sent\n%s\nwant\n%s", recorder.bodies[0], wantSolid)
	}

	err = clientUnderTest.SetRange(context.Background(), 10, 5, RGB{R: 0, G: 0, B: 255},
		WithRangeLEDCount(256), WithRangeRows(3, 2))
	if err != nil {
		t.Fatalf("SetRange: %v", err)
	}

	wantRange := `{"seg":[{"id":0,"start":10,"stop":15,"startY":3,"stopY":5,"on":true,` +
		`"col":[[0,0,255]],"fx":0}]}`
	if recorder.bodies[1] != wantRange {
		t.Fatalf("SetRange sent\n%s\nwant\n%s", recorder.bodies[1], wantRange)
	}
}

// TestPaintWritesTheIndexAndHexColours proves the tier-3 encoding byte for byte: the
// LED list names the index its first colour belongs to and then one six-digit hex
// colour per LED, and it carries nothing else — no power, because switching a segment
// on and painting it in the same body does not work.
func TestPaintWritesTheIndexAndHexColours(t *testing.T) {
	recorder := &capture{}
	server := recorder.server(t)

	err := client(t, server.URL).Paint(context.Background(), 7, []RGB{
		{R: 255, G: 0, B: 0},
		{R: 0, G: 255, B: 0},
		{R: 0, G: 0, B: 255},
	})
	if err != nil {
		t.Fatalf("Paint: %v", err)
	}

	if len(recorder.bodies) != 1 || recorder.methods[0] != http.MethodPost || recorder.paths[0] != "/json/state" {
		t.Fatalf("Paint sent %v %v, want one POST /json/state", recorder.methods, recorder.paths)
	}

	want := `{"seg":[{"id":0,"i":[7,"FF0000","00FF00","0000FF"]}]}`
	if recorder.bodies[0] != want {
		t.Fatalf("Paint sent\n%s\nwant\n%s", recorder.bodies[0], want)
	}
}

// TestPaintRefusesWhatItCannotSend proves the three refusals reach no controller: an
// empty frame and a nil one have nothing to draw, a negative first LED is not an LED,
// and a budget smaller than one colour would be broken by the first chunk rather than
// honoured.
func TestPaintRefusesWhatItCannotSend(t *testing.T) {
	recorder := &capture{}
	server := recorder.server(t)

	clientUnderTest := client(t, server.URL)
	oneColour := []RGB{{R: 1, G: 2, B: 3}}

	for _, paint := range []struct {
		name   string
		first  int
		colors []RGB
		opts   []PaintOption
		want   error
	}{
		{"an empty slice", 0, []RGB{}, nil, ErrEmptyPaint},
		{"a nil slice", 0, nil, nil, ErrEmptyPaint},
		{"a negative first led", -1, oneColour, nil, ErrInvalidRange},
		{"a budget under one colour", 0, oneColour, []PaintOption{WithPaintByteBudget(34)}, ErrPaintBudget},
		{"a zero budget", 0, oneColour, []PaintOption{WithPaintByteBudget(0)}, ErrPaintBudget},
	} {
		err := clientUnderTest.Paint(context.Background(), paint.first, paint.colors, paint.opts...)
		if !errors.Is(err, paint.want) {
			t.Fatalf("%s: Paint returned %v, want %v", paint.name, err, paint.want)
		}
	}

	if len(recorder.bodies) != 0 {
		t.Fatalf("a refused paint reached the controller %d times, want 0", len(recorder.bodies))
	}
}

// TestPaintSplitsAFrameToStayInsideTheBudget proves the whole of the largest frame this
// module drives: 16,384 colours go out in as many requests as the budget needs, every
// body is inside the budget, each names the LED its first colour belongs to, and the
// colours reassemble into the frame that was handed in, in order.
func TestPaintSplitsAFrameToStayInsideTheBudget(t *testing.T) {
	recorder := &capture{}
	server := recorder.server(t)

	const panelLEDs = 16384

	colors := make([]RGB, panelLEDs)
	for led := range colors {
		colors[led] = RGB{R: uint8(led), G: uint8(led >> 8), B: 0x40}
	}

	if err := client(t, server.URL).Paint(context.Background(), 0, colors); err != nil {
		t.Fatalf("Paint: %v", err)
	}

	if len(recorder.bodies) < 2 {
		t.Fatalf("a %d-colour frame was sent in %d request(s), want it split", panelLEDs, len(recorder.bodies))
	}

	painted := 0

	for request, body := range recorder.bodies {
		if recorder.methods[request] != http.MethodPost || recorder.paths[request] != "/json/state" {
			t.Fatalf("request %d was %s %s, want POST /json/state",
				request, recorder.methods[request], recorder.paths[request])
		}

		if len(body) > DefaultPaintByteBudget {
			t.Fatalf("request %d carries %d bytes, over the %d budget",
				request, len(body), DefaultPaintByteBudget)
		}

		var decoded struct {
			Segments []struct {
				Pixels []json.RawMessage `json:"i"`
			} `json:"seg"`
		}

		if err := json.Unmarshal([]byte(body), &decoded); err != nil {
			t.Fatalf("request %d does not decode: %v: %s", request, err, body)
		}

		if len(decoded.Segments) != 1 || len(decoded.Segments[0].Pixels) < 2 {
			t.Fatalf("request %d carries no led list: %s", request, body)
		}

		pixels := decoded.Segments[0].Pixels

		var index int
		if err := json.Unmarshal(pixels[0], &index); err != nil {
			t.Fatalf("request %d does not name its first led: %v: %s", request, err, body)
		}

		if index != painted {
			t.Fatalf("request %d starts at led %d, want %d: the frame would be out of order",
				request, index, painted)
		}

		for offset, raw := range pixels[1:] {
			var got string
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatalf("request %d led %d is not a colour: %v", request, painted+offset, err)
			}

			if got != colors[painted+offset].hex() {
				t.Fatalf("request %d led %d reads %s, want %s",
					request, painted+offset, got, colors[painted+offset].hex())
			}
		}

		painted += len(pixels) - 1
	}

	if painted != panelLEDs {
		t.Fatalf("the requests painted %d leds, want %d", painted, panelLEDs)
	}

	// The split is arithmetic, not a guess: at 8 KiB a body holds 907 colours from a
	// one-digit index, and 16,384 needs eighteen of those and a short last one.
	if len(recorder.bodies) != 19 {
		t.Fatalf("the frame went out in %d requests, want 19", len(recorder.bodies))
	}

	if len(recorder.bodies[0]) != paintBodyBytes(0, 907) {
		t.Fatalf("the first request carries %d bytes, want the %d of 907 colours",
			len(recorder.bodies[0]), paintBodyBytes(0, 907))
	}

	// A 48-pixel strip is one request, which is the other end of the same arithmetic.
	if fits := paintColoursThatFit(0, DefaultPaintByteBudget); fits < 48 {
		t.Fatalf("a %d-byte budget carries %d colours, want at least the 48 of a strip",
			DefaultPaintByteBudget, fits)
	}
}

// TestPaintChunkArithmeticIsExact proves the arithmetic the splitting rests on, at its
// edges: the envelope a body spends before any colour, what one colour costs, how many
// fit at a one-digit and at a five-digit index, and the two smallest budgets that can
// and cannot carry a single colour.
func TestPaintChunkArithmeticIsExact(t *testing.T) {
	if paintEnvelope != 26 {
		t.Fatalf("the per-led envelope is %d bytes, want 26", paintEnvelope)
	}

	if paintEntryBytes != 9 {
		t.Fatalf("one colour costs %d bytes, want 9", paintEntryBytes)
	}

	for _, fit := range []struct {
		index  int
		budget int
		want   int
	}{
		{0, 34, 0},     // the envelope and a colour is one byte more than this
		{0, 35, 1},     // exactly one colour
		{0, 8192, 907}, // the default budget at the frame's first led
		{907, 8192, 907},
		{16326, 8192, 906}, // a five-digit index leaves room for one colour fewer
	} {
		if got := paintColoursThatFit(fit.index, fit.budget); got != fit.want {
			t.Fatalf("paintColoursThatFit(%d, %d) = %d, want %d",
				fit.index, fit.budget, got, fit.want)
		}
	}

	for _, body := range []struct {
		index   int
		colors  int
		want    int
		budget  int
		inside  bool
		comment string
	}{
		{0, 48, 458, 8192, true, "a 48-led strip in one request"},
		{0, 907, 8189, 8192, true, "the most the default budget carries"},
		{0, 908, 8198, 8192, false, "one more than the default budget carries"},
	} {
		if got := paintBodyBytes(body.index, body.colors); got != body.want {
			t.Fatalf("paintBodyBytes(%d, %d) = %d, want %d: %s",
				body.index, body.colors, got, body.want, body.comment)
		}

		if got := paintBodyBytes(body.index, body.colors) <= body.budget; got != body.inside {
			t.Fatalf("%s: %d bytes inside a %d budget reads %v, want %v",
				body.comment, paintBodyBytes(body.index, body.colors), body.budget, got, body.inside)
		}
	}
}

// catalogueBody is a 2D controller's GET /json, cut down to the lists this package
// reads: the effect names, the palette names, and the fxdata that says which controls
// each effect has. The effect ids are this document's own positions, which is what a
// controller writes as fx — a real firmware lists 123 of them and "Scrolling Text" is
// id 122, so a list this short is a fixture and not a device.
const catalogueBody = `{
  "state": {"on": true, "bri": 128, "seg": [{"id": 0, "start": 0, "stop": 16384, "startY": 0, "stopY": 64}]},
  "info": {"ver": "14.7.0-mdev", "leds": {"count": 16384, "maxseg": 32, "matrix": {"w": 256, "h": 64}}},
  "effects": ["Solid", "Blink", "Breathe", "Scrolling Text"],
  "palettes": ["Default", "* Random Cycle", "Ocean"],
  "fxdata": ";;1\n!;1\n!;0\n-;1"
}`

// TestCapabilitiesReadsTheEffectAndPaletteLists proves the catalogue a dropdown is
// built from: every effect with the id it is written by and its own fxdata line, and
// every palette with its id — including the asterisk a controller puts on a palette it
// generates rather than stores.
func TestCapabilitiesReadsTheEffectAndPaletteLists(t *testing.T) {
	server := serve(t, http.StatusOK, catalogueBody)

	capabilities, err := client(t, server.URL).Capabilities(context.Background())
	if err != nil {
		t.Fatalf("Capabilities: %v", err)
	}

	wantEffects := []EffectInfo{
		{ID: 0, Name: "Solid", Flags: ";;1"},
		{ID: 1, Name: "Blink", Flags: "!;1"},
		{ID: 2, Name: "Breathe", Flags: "!;0"},
		{ID: 3, Name: "Scrolling Text", Flags: "-;1"},
	}

	if len(capabilities.Effects) != len(wantEffects) {
		t.Fatalf("Capabilities read %d effects, want %d", len(capabilities.Effects), len(wantEffects))
	}

	for index, want := range wantEffects {
		if capabilities.Effects[index] != want {
			t.Fatalf("effect %d reads %+v, want %+v", index, capabilities.Effects[index], want)
		}
	}

	wantPalettes := []Palette{
		{ID: 0, Name: "Default"},
		{ID: 1, Name: "* Random Cycle"},
		{ID: 2, Name: "Ocean"},
	}

	if len(capabilities.Palettes) != len(wantPalettes) {
		t.Fatalf("Capabilities read %d palettes, want %d", len(capabilities.Palettes), len(wantPalettes))
	}

	for index, want := range wantPalettes {
		if capabilities.Palettes[index] != want {
			t.Fatalf("palette %d reads %+v, want %+v", index, capabilities.Palettes[index], want)
		}
	}
}

// TestCapabilitiesWithoutAFullFXDataStillListsTheEffects proves the tolerant half: a
// controller that publishes no fxdata gives a list with empty flags rather than an
// error, and a document whose fxdata is shorter than its effect list gives empty flags
// to the effects it does not describe rather than lending them a neighbour's.
func TestCapabilitiesWithoutAFullFXDataStillListsTheEffects(t *testing.T) {
	for _, catalogue := range []struct {
		name  string
		body  string
		flags []string
	}{
		{
			name:  "no fxdata at all",
			body:  `{"effects": ["Solid", "Blink"], "palettes": ["Default"]}`,
			flags: []string{"", ""},
		},
		{
			name:  "fxdata shorter than the list",
			body:  `{"effects": ["Solid", "Blink", "Breathe"], "palettes": [], "fxdata": ";;1"}`,
			flags: []string{";;1", "", ""},
		},
		{
			name:  "fxdata as an array of lines",
			body:  `{"effects": ["Solid", "Blink"], "palettes": [], "fxdata": [";;1", "!;1"]}`,
			flags: []string{";;1", "!;1"},
		},
	} {
		server := serve(t, http.StatusOK, catalogue.body)

		capabilities, err := client(t, server.URL).Capabilities(context.Background())
		if err != nil {
			t.Fatalf("%s: Capabilities: %v", catalogue.name, err)
		}

		if len(capabilities.Effects) != len(catalogue.flags) {
			t.Fatalf("%s: read %d effects, want %d", catalogue.name, len(capabilities.Effects), len(catalogue.flags))
		}

		for index, want := range catalogue.flags {
			if capabilities.Effects[index].Flags != want {
				t.Fatalf("%s: effect %d flags read %q, want %q",
					catalogue.name, index, capabilities.Effects[index].Flags, want)
			}
		}
	}
}

// textStateBody is a panel running the text effect with a name set, so the read side
// can be shown to carry what the write side sends: an effect's parameters and the
// segment name are read back, which is what lets a caller change one and keep the rest.
const textStateBody = `{"on":true,"bri":200,"seg":[
  {"id":0,"start":0,"stop":256,"startY":0,"stopY":8,"len":256,"on":true,"n":"PRINTING 94%",
   "col":[[255,255,255],[0,0,0],[0,0,0]],"fx":122,"sx":128,"ix":16,"c1":7,"c2":2,"c3":3,
   "o1":1,"o2":0,"o3":0,"pal":5,"grp":2,"tp":1,"m12":3}
]}`

// TestStateCarriesTheEffectParametersAndTheName proves the read side of tier 1: every
// parameter SetEffect writes comes back, so a caller can preserve what it does not mean
// to change, and the name the text effect draws is readable rather than write-only.
func TestStateCarriesTheEffectParametersAndTheName(t *testing.T) {
	server := serve(t, http.StatusOK, textStateBody)

	state, err := client(t, server.URL).State(context.Background())
	if err != nil {
		t.Fatalf("State: %v", err)
	}

	if len(state.Segments) != 1 {
		t.Fatalf("State read %d segments, want 1", len(state.Segments))
	}

	segment := state.Segments[0]

	want := Segment{
		ID:        0,
		Start:     0,
		Stop:      256,
		StartY:    0,
		StopY:     8,
		On:        true,
		Colors:    []RGB{{R: 255, G: 255, B: 255}, {R: 0, G: 0, B: 0}, {R: 0, G: 0, B: 0}},
		Effect:    122,
		Name:      "PRINTING 94%",
		Speed:     128,
		Intensity: 16,
		Custom1:   7,
		Custom2:   2,
		Custom3:   3,
		Check1:    1,
		Check2:    0,
		Check3:    0,
		Palette:   5,
		Grouping:  2,
		Transpose: 1,
		Expand1D:  3,
	}

	if segment.ID != want.ID || segment.Start != want.Start || segment.Stop != want.Stop ||
		segment.StartY != want.StartY || segment.StopY != want.StopY || segment.On != want.On {
		t.Fatalf("segment reads %+v, want the bounds of %+v", segment, want)
	}

	if segment.Effect != want.Effect || segment.Name != want.Name ||
		segment.Speed != want.Speed || segment.Intensity != want.Intensity ||
		segment.Custom1 != want.Custom1 || segment.Custom2 != want.Custom2 ||
		segment.Custom3 != want.Custom3 || segment.Check1 != want.Check1 ||
		segment.Check2 != want.Check2 || segment.Check3 != want.Check3 ||
		segment.Palette != want.Palette || segment.Grouping != want.Grouping ||
		segment.Transpose != want.Transpose || segment.Expand1D != want.Expand1D {
		t.Fatalf("segment reads %+v, want the effect parameters of %+v", segment, want)
	}

	if len(segment.Colors) != len(want.Colors) || segment.Colors[0] != want.Colors[0] {
		t.Fatalf("segment colours read %v, want %v", segment.Colors, want.Colors)
	}
}

// TestSetSegmentsWritesAPanelAsOneRequestWithFourBounds is the body a live 256x64 panel takes,
// and it is no longer the body this package used to send.
//
// The old body here named a flat pixel range — start 0, stop 10158, then start 10158, stop
// 16384, both with stopY 0 — on the theory that the firmware added startY times the width to
// start and read the pair as a row-major run. It was POSTed to a real panel, answered
// {"success":true}, and the device then reported both segments as start 0, stop 256, startY 0,
// stopY 1: start and stop are columns and were clamped to the width, stopY 0 became one row,
// and the black remainder covered the green fill on that single row. Both segments ended up
// the same line, and the panel was dark.
//
// So a panel body is a rectangle: 62 % of 16,384 pixels is 158.72 columns, rounded to 159, and
// the fill is columns 0 to 159 across all 64 rows with the remainder in columns 159 to 256.
func TestSetSegmentsWritesAPanelAsOneRequestWithFourBounds(t *testing.T) {
	recorder := &capture{}
	server := recorder.server(t)

	err := client(t, server.URL).SetSegments(context.Background(), []SegmentWrite{
		{ID: 0, First: 0, Count: 159, FirstRow: 0, RowCount: 64, On: true, Colour: RGB{R: 0, G: 170, B: 0}},
		{ID: 1, First: 159, Count: 97, FirstRow: 0, RowCount: 64, On: true, Colour: RGB{}},
	})
	if err != nil {
		t.Fatalf("SetSegments: %v", err)
	}

	if len(recorder.methods) != 1 || recorder.methods[0] != http.MethodPost || recorder.paths[0] != "/json/state" {
		t.Fatalf("SetSegments sent %v %v, want one POST /json/state", recorder.methods, recorder.paths)
	}

	want := `{"seg":[{"id":0,"start":0,"stop":159,"startY":0,"stopY":64,"on":true,"col":[[0,170,0]],"fx":0},` +
		`{"id":1,"start":159,"stop":256,"startY":0,"stopY":64,"on":true,"col":[[0,0,0]],"fx":0}]}`

	if recorder.bodies[0] != want {
		t.Fatalf("SetSegments sent\n%s\nwant\n%s", recorder.bodies[0], want)
	}
}

// TestSetSegmentsWritesAStripAsAFlatRun is the other shape, and it is unchanged: a caller that
// states no rows gets the run of pixels a 1D controller has always been given, with both row
// bounds as zero. The four bounds travel together on a strip too, and zero on both is what says
// "one run, no rectangle" — which is the only meaning a strip has.
func TestSetSegmentsWritesAStripAsAFlatRun(t *testing.T) {
	recorder := &capture{}
	server := recorder.server(t)

	err := client(t, server.URL).SetSegments(context.Background(), []SegmentWrite{
		{ID: 0, First: 0, Count: 24, On: true, Colour: RGB{R: 0, G: 170, B: 0}},
		{ID: 1, First: 24, Count: 24, On: true, Colour: RGB{}},
	})
	if err != nil {
		t.Fatalf("SetSegments: %v", err)
	}

	want := `{"seg":[{"id":0,"start":0,"stop":24,"startY":0,"stopY":0,"on":true,"col":[[0,170,0]],"fx":0},` +
		`{"id":1,"start":24,"stop":48,"startY":0,"stopY":0,"on":true,"col":[[0,0,0]],"fx":0}]}`

	if recorder.bodies[0] != want {
		t.Fatalf("SetSegments sent\n%s\nwant\n%s", recorder.bodies[0], want)
	}
}

// TestSetSegmentsSwitchesASegmentOffWithoutAColour proves the other shape: a segment that
// is not drawn says so and names no colour, in the same request. It is how a frame with no
// remainder clears the segment a previous frame's remainder was in — an off segment draws
// nothing, so it cannot paint over the frame it is part of.
func TestSetSegmentsSwitchesASegmentOffWithoutAColour(t *testing.T) {
	recorder := &capture{}
	server := recorder.server(t)

	err := client(t, server.URL).SetSegments(context.Background(), []SegmentWrite{
		{ID: 0, First: 0, Count: 48, On: true, Colour: RGB{R: 210, G: 165, B: 0}},
		{ID: 1, On: false},
	})
	if err != nil {
		t.Fatalf("SetSegments: %v", err)
	}

	want := `{"seg":[{"id":0,"start":0,"stop":48,"startY":0,"stopY":0,"on":true,"col":[[210,165,0]],"fx":0},` +
		`{"id":1,"on":false}]}`

	if len(recorder.bodies) != 1 || recorder.bodies[0] != want {
		t.Fatalf("SetSegments sent %v, want one body\n%s", recorder.bodies, want)
	}
}

// TestSetSegmentsRefusesABodyAControllerCannotTake proves the two caller mistakes are
// caught here rather than at the device: the same segment named twice, which a controller
// would resolve silently by applying the second, and a span with nothing in it, which a
// controller reads as a segment to delete.
func TestSetSegmentsRefusesABodyAControllerCannotTake(t *testing.T) {
	recorder := &capture{}
	server := recorder.server(t)

	clientUnderTest := client(t, server.URL)

	for _, body := range [][]SegmentWrite{
		nil,
		{
			{ID: 0, Count: 5, On: true},
			{ID: 0, First: 5, Count: 5, On: true},
		},
		{{ID: 0, First: -1, Count: 5, On: true}},
		{{ID: 0, First: 0, Count: -5, On: true}},
		{{ID: -1, First: 0, Count: 5, On: true}},
		{{ID: 0, First: 0, Count: 5, FirstRow: -1, RowCount: 8, On: true}},
		{{ID: 0, First: 0, Count: 5, FirstRow: 0, RowCount: -8, On: true}},
	} {
		if err := clientUnderTest.SetSegments(context.Background(), body); err == nil {
			t.Fatalf("SetSegments accepted %+v", body)
		}
	}

	if len(recorder.methods) != 0 {
		t.Fatalf("a refused body was still sent: %v", recorder.methods)
	}
}
