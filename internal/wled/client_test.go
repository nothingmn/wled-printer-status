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
