// GET /frames: the Overview screen's thumbnails.
//
// The two things these tests are for. A thumbnail must be drawn by the same code that draws the
// light, so that it cannot disagree with the panel — which is proved here by reading the picture
// back and finding the bar, the digits and the colour where the renderer's own arithmetic puts them.
// And a thumbnail must draw for a light that is not answering, from its last reading, saying how old
// that reading is instead of pretending it is current.
package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nothingmn/wled-printer-status/internal/render"
	"github.com/nothingmn/wled-printer-status/internal/text"
	"github.com/nothingmn/wled-printer-status/internal/wled"
)

// framesAt is the instant every frame in these tests is drawn at: a fixed evening, so that the
// rotation of a panel's two halves is a decision and not whatever clock the test binary started on.
var framesAt = time.Date(2026, time.November, 19, 18, 30, 0, 0, time.UTC)

// framesUnderTest is GET /frames over the per-binding store, with nothing else running: every fact
// the route answers with is what a watch published, which is the whole point of it.
type framesUnderTest struct {
	mux   http.Handler
	cache *frameCache
}

// newFramesUnderTest builds the route over the set a test says it is running.
//
// It clears the per-binding store first, so that a frame published by an earlier test cannot be
// answered for this one: publishing happens after this returns, which is also the order the process
// works in — a watch exists before it decides anything.
func newFramesUnderTest(t *testing.T, set bindings) *framesUnderTest {
	t.Helper()

	forgetBindings()

	// The store a one-binding watch publishes into is a package-level one, shared with every other
	// test in this binary. Clearing it here is what makes "no frame decided yet" a state this test
	// can create rather than one it inherits.
	current.Store(nil)
	sent.Store(nil)

	t.Cleanup(func() {
		forgetBindings()
		forgetPreview()
	})

	cache := newFrameCache()

	mux := http.NewServeMux()
	framesAPI{
		live:  &runner{held: set, layout: defaultLayout(), logf: quiet},
		clock: func() time.Time { return framesAt },
		cache: cache,
	}.mount(mux)

	return &framesUnderTest{mux: mux, cache: cache}
}

// do runs one request and answers with the status and the body.
func (a *framesUnderTest) do(t *testing.T) (int, map[string]any) {
	t.Helper()

	recorder := httptest.NewRecorder()
	a.mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/frames", nil))

	var body map[string]any
	if raw := recorder.Body.Bytes(); len(raw) > 0 {
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatalf("the answer is not JSON: %v (%s)", err, raw)
		}
	}

	return recorder.Code, body
}

// panelBinding is the panel on the operator's wall: the whole light, one printer.
func panelBinding() binding {
	return binding{DeviceID: "printer-a", Address: "http://10.0.0.136", Segment: -1, Brightness: 128}
}

// publishPanel publishes one panel frame, with the reading a printing job has.
func publishPanel(t *testing.T, percent int, stale bool, age time.Duration) render.Frame {
	t.Helper()

	snapshot := printingJob("printer-a", float64(percent))
	snapshot.Stale = stale
	snapshot.Age = age

	frame := render.Frame{
		Colour:  render.ColourPrinting,
		Lit:     percent * 16384 / 100,
		Total:   16384,
		Percent: percent,
		Name:    "MK4IS_0.4n_0.2mm_PLA_MK4IS_1h52m.gcode",
		Reason:  render.ReasonProgress,
	}

	publishShape(panelBinding(), bindingShape{geometry: livePanel})
	publishBinding(panelBinding(), snapshot, frame)

	return frame
}

// pixelsOf decodes one answer's picture back into the buffer it was made from.
func pixelsOf(t *testing.T, image map[string]any) ([]wled.RGB, int, int) {
	t.Helper()

	if media, _ := image["media_type"].(string); media != "image/png" {
		t.Fatalf("the picture's media type is %q, want image/png", media)
	}

	encoded, err := base64.StdEncoding.DecodeString(image["base64"].(string))
	if err != nil {
		t.Fatalf("the picture is not base64: %v", err)
	}

	picture, err := png.Decode(bytes.NewReader(encoded))
	if err != nil {
		t.Fatalf("the picture is not a PNG: %v", err)
	}

	bounds := picture.Bounds()

	width, height := bounds.Dx(), bounds.Dy()

	pixels := make([]wled.RGB, 0, width*height)

	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			r, g, b, _ := picture.At(bounds.Min.X+x, bounds.Min.Y+y).RGBA()

			pixels = append(pixels, wled.RGB{R: uint8(r >> 8), G: uint8(g >> 8), B: uint8(b >> 8)})
		}
	}

	return pixels, width, height
}

// TestAFrameIsThePictureThePainterDraws is the central claim of item 2: the thumbnail is the frame
// the painter decided, drawn by the module's own code, so it cannot disagree with the panel.
//
// It is checked by reading the picture back and finding each of the painter's own decisions in it:
// the colour where the bar is filled, black where it is not, and the percentage's glyphs where the
// layout's own band and scale put them — the digits rasterised here from the frame's percentage and
// the panel's width, independently of the composition. The whole picture is then compared against
// the composition itself, pixel for pixel.
func TestAFrameIsThePictureThePainterDraws(t *testing.T) {
	under := newFramesUnderTest(t, bindings{panelBinding()})

	frame := publishPanel(t, 41, false, time.Second)

	status, body := under.do(t)
	if status != http.StatusOK {
		t.Fatalf("the route answered %d: %v", status, body)
	}

	if count, _ := body["count"].(float64); count != 1 {
		t.Fatalf("the route answered %v frames, want one per binding: %v", body["count"], body)
	}

	one := readObjects(t, body, "frames")[0]

	if one["kind"] != "panel" {
		t.Fatalf("the frame is a %v, and the light is a panel", one["kind"])
	}

	if width, _ := one["width"].(float64); int(width) != 256 {
		t.Fatalf("the picture is %v pixels wide, want the panel's 256", one["width"])
	}

	pixels, width, height := pixelsOf(t, readObject(t, one, "image"))

	if width != 256 || height != 64 {
		t.Fatalf("the picture decodes as %dx%d, want the panel's 256x64", width, height)
	}

	// The layout the painter composes with, and the bands it puts on this panel.
	shape := defaultLayout()

	bands, err := shape.bandsOn(livePanel)
	if err != nil {
		t.Fatalf("the default layout does not fit the panel: %v", err)
	}

	// The bar: the layout's own band, filled to the frame's own fraction of the columns.
	columns := fillColumns(frame, livePanel)

	if columns <= 0 || columns >= width {
		t.Fatalf("a 41 %% frame fills %d of %d columns, which is not a bar", columns, width)
	}

	inside := bands.barTop*width + columns - 1
	if pixels[inside] != render.ColourPrinting {
		t.Fatalf("the bar's last filled pixel is %+v, want the printing green %+v", pixels[inside], render.ColourPrinting)
	}

	outside := bands.barTop*width + (width - 1)
	if pixels[outside] != (wled.RGB{}) {
		t.Fatalf("the bar's unfilled end is %+v, want black", pixels[outside])
	}

	// The text: the digits this module's own font draws, at the scale the layout gives them, inside
	// the band the layout left for them.
	digits := fmt.Sprintf("%d%%", frame.Percent)

	lit, err := text.Raster(digits, percentScale(bands.textRows), text.Geometry{Width: width, Height: bands.textRows})
	if err != nil {
		t.Fatalf("rasterise the digits: %v", err)
	}

	if len(lit) == 0 {
		t.Fatalf("the digits %q rasterised to nothing", digits)
	}

	row, column := lit[0]/width, lit[0]%width

	if got := pixels[(bands.textTop+row)*width+column]; got != render.ColourPrinting {
		t.Fatalf("the first lit pixel of the digits is %+v, want the printing green %+v", got, render.ColourPrinting)
	}

	// And the whole picture is the composition the painter makes, pixel for pixel.
	expected, err := panelPicture{frame: frame, layout: shape, at: framesAt}.draw(livePanel)
	if err != nil {
		t.Fatalf("draw the composed frame: %v", err)
	}

	if len(expected) != len(pixels) {
		t.Fatalf("the picture holds %d pixels and the composition %d", len(pixels), len(expected))
	}

	differing := 0

	for index := range expected {
		if expected[index] != pixels[index] {
			differing++
		}
	}

	if differing != 0 {
		t.Fatalf("%d of %d pixels differ from the composition the painter makes", differing, len(expected))
	}
}

// TestAStaleBindingStillDrawsAndSaysHowOldItsReadingIs is the second claim: a thumbnail for an
// unplugged lamp still has to draw, from the last reading, and must say that the reading is stale
// rather than pretending.
func TestAStaleBindingStillDrawsAndSaysHowOldItsReadingIs(t *testing.T) {
	under := newFramesUnderTest(t, bindings{panelBinding()})

	publishPanel(t, 41, true, 3*time.Minute)

	status, body := under.do(t)
	if status != http.StatusOK {
		t.Fatalf("the route answered %d: %v", status, body)
	}

	one := readObjects(t, body, "frames")[0]

	reading := readObject(t, one, "reading")
	if stale, _ := reading["stale"].(bool); !stale {
		t.Fatalf("a reading three minutes old is not reported stale: %v", reading)
	}

	if age, _ := reading["age_seconds"].(float64); age < 179 {
		t.Fatalf("the reading's age is %v seconds, want the 180 it was published with", age)
	}

	pixels, _, _ := pixelsOf(t, readObject(t, one, "image"))
	if len(pixels) != 256*64 {
		t.Fatalf("a stale binding drew %d pixels, want the whole panel", len(pixels))
	}

	note, _ := one["note"].(string)
	if !strings.Contains(note, "180 seconds old") {
		t.Fatalf("the frame says %q, want it to name the reading's age", note)
	}
}

// TestAStripFrameIsTheFramesOwnFill is the other display: a strip is one row, and the picture is the
// frame's colour across the pixels the job has completed and black past them — which is what the
// strip body writes, with no rounding between, because a strip's unit is one LED.
func TestAStripFrameIsTheFramesOwnFill(t *testing.T) {
	b := binding{DeviceID: "printer-b", Address: "http://10.0.0.253", Segment: -1, Brightness: 128}

	under := newFramesUnderTest(t, bindings{b})

	frame := render.Frame{
		Colour:  render.ColourPrinting,
		Lit:     20,
		Total:   48,
		Percent: 42,
		Reason:  render.ReasonProgress,
	}

	publishShape(b, bindingShape{})
	publishBinding(b, printingJob("printer-b", 50), frame)

	status, body := under.do(t)
	if status != http.StatusOK {
		t.Fatalf("the route answered %d: %v", status, body)
	}

	one := readObjects(t, body, "frames")[0]

	if one["kind"] != "strip" {
		t.Fatalf("the frame is a %v, and the light is a strip", one["kind"])
	}

	pixels, width, height := pixelsOf(t, readObject(t, one, "image"))

	if width != 48 || height != 1 {
		t.Fatalf("the strip's picture is %dx%d, want 48x1: a strip is one row", width, height)
	}

	for index, pixel := range pixels {
		want := frame.Colour
		if index >= frame.Lit {
			want = wled.RGB{}
		}

		if pixel != want {
			t.Fatalf("pixel %d is %+v, want %+v", index, pixel, want)
		}
	}
}

// TestABorrowedSegmentDrawsOnlyItsOwnRun is the mockup's own rule for the strip row: CyberDesk's
// Front 1, not the whole 48 LEDs, because the rest of that strip belongs to somebody else.
func TestABorrowedSegmentDrawsOnlyItsOwnRun(t *testing.T) {
	b := binding{DeviceID: "printer-b", Address: "http://10.0.0.253", Segment: 3}

	under := newFramesUnderTest(t, bindings{b})

	frame := render.Frame{
		Colour:  render.ColourIdle,
		Lit:     0,
		Total:   48,
		Percent: -1,
		Reason:  render.ReasonNoLiveJob,
	}

	publishShape(b, bindingShape{span: &[2]int{14, 24}})
	publishBinding(b, printingJob("printer-b", 50), frame)

	status, body := under.do(t)
	if status != http.StatusOK {
		t.Fatalf("the route answered %d: %v", status, body)
	}

	one := readObjects(t, body, "frames")[0]

	pixels, width, height := pixelsOf(t, readObject(t, one, "image"))

	if width != 10 || height != 1 {
		t.Fatalf("the borrowed run's picture is %dx%d, want the segment's own 10 pixels", width, height)
	}

	for index, pixel := range pixels {
		if pixel != render.ColourIdle {
			t.Fatalf("pixel %d of an idle zone is %+v, want the idle yellow", index, pixel)
		}
	}

	light := readObject(t, one, "light")
	if segment, _ := light["segment"].(float64); int(segment) != 3 {
		t.Fatalf("the frame names segment %v, want the one it borrowed", light["segment"])
	}
}

// TestThePictureIsCachedAgainstTheFrameThatMadeIt is the cost, answered honestly: the Overview asks
// again every few seconds for a frame that has not moved, and the second ask is served from the
// cache rather than re-encoded — while a frame that has moved is not.
func TestThePictureIsCachedAgainstTheFrameThatMadeIt(t *testing.T) {
	under := newFramesUnderTest(t, bindings{panelBinding()})

	publishPanel(t, 41, false, time.Second)

	_, body := under.do(t)
	first := readObject(t, readObjects(t, body, "frames")[0], "image")

	if cached, _ := first["cached"].(bool); cached {
		t.Fatalf("the first encode was served from an empty cache")
	}

	_, body = under.do(t)
	second := readObject(t, readObjects(t, body, "frames")[0], "image")

	if cached, _ := second["cached"].(bool); !cached {
		t.Fatalf("the same frame was encoded twice: %v", second)
	}

	if first["base64"] != second["base64"] {
		t.Fatalf("two answers for one frame carry different pictures")
	}

	// A frame that moved is a new picture, not a stale one.
	publishPanel(t, 42, false, time.Second)

	_, body = under.do(t)
	third := readObject(t, readObjects(t, body, "frames")[0], "image")

	if cached, _ := third["cached"].(bool); cached {
		t.Fatalf("a frame that moved was served from the cache: %v", third)
	}

	if third["base64"] == first["base64"] {
		t.Fatalf("41 %% and 42 %% drew the same picture")
	}
}

// TestNoFrameDecidedYetIsRefusedRatherThanDrawn is the refusal /status makes for the same reason: a
// frame of zeros would read as "the light is black", and that is a claim this module has not made.
func TestNoFrameDecidedYetIsRefusedRatherThanDrawn(t *testing.T) {
	under := newFramesUnderTest(t, bindings{panelBinding()})

	recorder := httptest.NewRecorder()
	under.mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/frames", nil))

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("a module with no reading answered %d, want %d: %s",
			recorder.Code, http.StatusServiceUnavailable, recorder.Body.String())
	}

	if code := problemCode(t, recorder); !strings.HasSuffix(code, "NO_READING_YET") {
		t.Fatalf("the refusal is %q, want NO_READING_YET", code)
	}
}
