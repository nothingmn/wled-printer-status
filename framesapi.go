// The Overview screen's one backend need: what each light is drawing, as a picture a browser can
// put on the page.
//
// # What a thumbnail is, decided from the mockup
//
// docs/product-requirements.md §2.1 answers this in the operator's own terms: a thumbnail is a
// **reconstruction**, drawn from the frame this module decided — colour, fill, percent, name — and
// not a screenshot, because this firmware cannot read its own pixels back. docs/design/v2/overview/
// draws it on a `<canvas>`: 256 by 64 for the panel and 48 by 8 for the strip, painted with the same
// 5x7 glyphs the module uses.
//
// So the mockup shows an image, and the smallest thing a browser can draw is an image. This route
// answers with **PNG**: the picture, drawn here by the module's own renderer, encoded by the standard
// library, and handed over as bytes a page puts in an `<img>`. The alternative shapes were weighed
// and rejected:
//
//   - A description for the page to redraw — colour, fill count, percent, name — makes the browser a
//     second renderer. The Overview mockup's own `drawPanel` is one: it places the digits at a fixed
//     corner and the name at another, which is not where `internal/render`'s layout puts them. Two
//     renderers are two answers to "what is on the wall", and the one on the page is the one nobody
//     can check against the light.
//   - A bespoke pixel array is a third format for a job PNG already does, and it leaves the browser
//     to blit it.
//
// The description travels beside the image anyway, because a page needs what a picture cannot say:
// when the last write landed, whether the reading is stale, and which printer the light is showing.
//
// # One renderer, and no second opinion
//
// Both pictures come from the functions the writer paints with. A panel composes with
// `panelPicture.draw` when the painter would compose — `drawsComposed`, the same predicate the
// writer uses to decide between a composed frame and a plain one — and otherwise with
// `matrix.Raster`, which is the picture a panel's own segment bodies draw. A strip is the frame's
// own fill: colour for the pixels the job has completed and black past them, which is what the
// strip body writes.
//
// # Why nothing here reaches a light
//
// A thumbnail has to draw while the lamp is unplugged — that is the whole reason a snapshot exists,
// said of a frame. So every fact on this route comes from what the watch already published, and not
// one request here opens a socket to a controller. The cost is a little staleness, and the answer
// says how much rather than pretending.
package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"sync"
	"time"

	"github.com/nothingmn/wled-printer-status/internal/matrix"
	"github.com/nothingmn/wled-printer-status/internal/render"
	"github.com/nothingmn/wled-printer-status/internal/wled"
)

const (
	// frameCacheLimit is how many encoded thumbnails are kept. One per binding would be enough; the
	// limit is here because a frame changes as a print moves, and a cache with no ceiling is a leak
	// with a nice name. Past it the map is emptied rather than evicted one at a time: there are a
	// handful of bindings, and being wrong about which one to drop costs one re-encode.
	frameCacheLimit = 64
)

// framesAPI is GET /frames: one frame per binding, for the Overview screen's thumbnails.
type framesAPI struct {
	live *runner

	// clock dates a frame, which matters for one thing only: the half of a panel's rotation the
	// picture shows. Nil means time.Now.
	clock func() time.Time

	// cache holds encoded pictures by the frame they were made from. Nil is allowed and means no
	// cache, which is what a test that wants to see the encoding happen uses.
	cache *frameCache
}

// frameCache is the encoded-picture cache, and it is honest about what it is: a key made of the
// frame that produced the bytes, so a picture can never be served for a frame that has changed.
type frameCache struct {
	mu   sync.Mutex
	held map[string][]byte
}

// newFrameCache is an empty cache.
func newFrameCache() *frameCache {
	return &frameCache{held: make(map[string][]byte)}
}

// get is the picture for one key, and whether it was already held.
func (c *frameCache) get(key string) ([]byte, bool) {
	if c == nil {
		return nil, false
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	encoded, held := c.held[key]

	return encoded, held
}

// put records one picture.
func (c *frameCache) put(key string, encoded []byte) {
	if c == nil {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if len(c.held) >= frameCacheLimit {
		c.held = make(map[string][]byte, frameCacheLimit)
	}

	c.held[key] = encoded
}

// mount adds the Overview screen's route to mux.
func (api framesAPI) mount(mux *http.ServeMux) {
	mux.HandleFunc("GET /frames", api.listFrames)
}

// frameAnswer is one binding's frame: the description a page reads, and the picture it draws.
type frameAnswer struct {
	// Binding is the printer this frame is about, by the id the binding routes use, and Printer is
	// the name a person reads beside it.
	Binding string      `json:"binding"`
	Printer printerView `json:"printer"`

	// Light is where it is drawn: the light's name on these routes, its address, and the segment
	// with -1 for the whole light.
	Light lightView `json:"light"`

	// Kind is panel or strip, and Width and Height are what the binding draws on — the light's own
	// matrix for a panel, one row for a strip, and the borrowed run for a segment binding. It is
	// the geometry of the picture beside it.
	Kind   string `json:"kind"`
	Width  int    `json:"width"`
	Height int    `json:"height"`

	// Frame, Reading and LastSend are what this module decided, how old the reading behind it is,
	// and what the last write to the light did.
	Frame    frameView   `json:"frame"`
	Reading  readingView `json:"reading"`
	LastSend *sendView   `json:"last_send"`

	// Image is the picture itself.
	Image imageAnswer `json:"image"`

	// Note says what a person has to know to read the picture honestly.
	Note string `json:"note,omitempty"`
}

// lightView is one light as a frame names it.
type lightView struct {
	Name    string `json:"name"`
	Address string `json:"address"`
	Segment int    `json:"segment"`
}

// imageAnswer is one encoded picture.
//
// Base64 inside the JSON rather than a second route, because a page drawing an Overview row wants
// the frame with the row's other facts and not a second round trip per light; and because a second
// route would need a cache key in its URL, which is a pixel array by another name.
type imageAnswer struct {
	MediaType string `json:"media_type"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	Bytes     int    `json:"bytes"`
	Base64    string `json:"base64"`
	Cached    bool   `json:"cached"`
}

// framesAnswer is the body of GET /frames.
type framesAnswer struct {
	Frames []frameAnswer `json:"frames"`
	Count  int           `json:"count"`
	Note   string        `json:"note"`
}

// framesNote is the sentence the route carries, because a reconstruction is a thing a person can be
// misled by if nobody says what it is.
const framesNote = "Each picture is reconstructed from the frame this module decided - the colour, the fill, the " +
	"percentage and the job's name, drawn by the same code that draws the light. It is not a screenshot: this " +
	"firmware cannot read its pixels back. A light that is not answering still draws, from its last reading, " +
	"and its age says how old that reading is."

// listFrames is GET /frames.
//
// It refuses only when there is nothing to describe at all — no frame has been decided yet — which is
// the same refusal /status makes and for the same reason: a frame of zeros would read as "the light
// is black", and that is a claim this module has not made.
func (api framesAPI) listFrames(w http.ResponseWriter, r *http.Request) {
	published := bindingsSeen()
	set := api.live.set()

	out := make([]frameAnswer, 0, max(len(published), 1))

	if len(published) > 0 {
		for _, held := range published {
			one, err := api.frameOf(held, set)
			if err != nil {
				// A frame that cannot be drawn is a fault of one binding, and a page with
				// three rows should lose the third rather than all of them. The reason is
				// carried on the row.
				out = append(out, frameAnswer{
					Binding: held.Printer.ID,
					Printer: held.Printer,
					Light:   lightView{Address: held.Controller, Segment: held.Segment},
					Frame:   held.Frame,
					Reading: held.Reading,
					Note:    "This frame could not be drawn: " + err.Error(),
				})

				continue
			}

			out = append(out, one)
		}

		answer(w, http.StatusOK, framesAnswer{Frames: out, Count: len(out), Note: framesNote})

		return
	}

	// A watch holding one binding publishes into the store /status has always read, because that is
	// what an install built from settings answers. The frame is the same frame; what it lacks is the
	// binding's own name, which the running set has.
	seen := current.Load()
	if seen == nil || len(set) != 1 {
		refuse(w, r, NoReadingYet.New(
			"this module has not decided a frame yet: it answers this once it has taken its first reading"))

		return
	}

	held := bindingStatus{
		Controller: set[0].Address,
		Segment:    set[0].Segment,
		Printer:    seen.Printer,
		Reading:    seen.Reading,
		Frame:      seen.Frame,
		LastSend:   sent.Load(),
	}

	one, err := api.frameOf(held, set)
	if err != nil {
		refuse(w, r, NoReadingYet.New("this module cannot draw the frame it decided: "+err.Error()))

		return
	}

	out = append(out, one)

	answer(w, http.StatusOK, framesAnswer{Frames: out, Count: len(out), Note: framesNote})
}

// frameOf is one binding's answer: the picture, and everything beside it.
func (api framesAPI) frameOf(held bindingStatus, set bindings) (frameAnswer, error) {
	found, bindingOf, ok := api.bindingOf(held, set)

	said := frameAnswer{
		Binding:  bindingOf.DeviceID,
		Printer:  held.Printer,
		Light:    lightView{Name: found, Address: held.Controller, Segment: held.Segment},
		Frame:    held.Frame,
		Reading:  held.Reading,
		LastSend: held.LastSend,
	}

	if said.Binding == "" {
		said.Binding = held.Printer.ID
	}

	if !ok {
		// The binding has gone since the frame was published. The picture is still the honest
		// record of what that light was last asked to show, so it is drawn; what it cannot claim
		// is a light this module still drives.
		said.Note = "This binding is no longer in the set this module runs: the picture is the last frame it decided."
	}

	frame := render.Frame{
		Colour:  wled.RGB{R: held.Frame.Colour.R, G: held.Frame.Colour.G, B: held.Frame.Colour.B},
		Lit:     held.Frame.Lit,
		Total:   held.Frame.Total,
		Percent: held.Frame.Percent,
		Name:    held.Frame.Name,
		Reason:  render.Reason(held.Frame.Reason),
	}

	shape := shapeOf(held.Controller, held.Segment)
	width, height := shape.pixelsOf(frame)
	if width <= 0 || height <= 0 {
		return frameAnswer{}, fmt.Errorf(
			"nothing is known about the shape of %s yet, so there is no picture to draw", said.Light.Address)
	}

	pixels, err := api.pictureOf(frame, shape)
	if err != nil {
		return frameAnswer{}, err
	}

	said.Kind, said.Width, said.Height = shape.kind(), width, height
	said.Image, err = api.encode(pixels, width, height, cacheKey(found, held, shape, width, height, api.now()))
	if err != nil {
		return frameAnswer{}, err
	}

	// A preset the light is running is the one picture this module cannot reconstruct: the light draws
	// it and this firmware cannot be read back. So the frame stands and the sentence says what is
	// actually there, which is the honest half of a thumbnail rather than a picture of something else.
	if shape.preset != "" {
		said.Note = fmt.Sprintf(
			"This light is running its own preset %q for a moment, and a preset cannot be reconstructed: "+
				"the picture beside this sentence is the steady frame the moment will hand back to.",
			shape.preset)
	}

	if held.Reading.Stale && said.Note == "" {
		said.Note = fmt.Sprintf(
			"The reading behind this picture is %.0f seconds old, so this is what the light would show once it "+
				"answers: it cannot be asked what it is showing now.", held.Reading.AgeSeconds)
	}

	return said, nil
}

// bindingOf is the light a published frame belongs to, and the binding itself, and whether the
// running set still holds it.
//
// The published frame names its binding by the controller and the segment, which is the pair the
// watch publishes under, so the set is searched by that pair rather than by the printer id: a frame
// published for a binding that has since been deleted must not be attributed to whichever new
// binding happens to name the same printer.
func (api framesAPI) bindingOf(held bindingStatus, set bindings) (string, binding, bool) {
	name := ""

	for _, one := range set {
		key, err := controllerKey(one.Address)
		if err != nil {
			continue
		}

		if one.Address == held.Controller || key == held.Controller {
			name = nameOfController(key)
		}

		if one.Address == held.Controller && one.Segment == held.Segment {
			return nameOfController(key), one, true
		}
	}

	if name == "" {
		name = nameOfController(held.Controller)
	}

	return name, binding{Address: held.Controller, Segment: held.Segment}, false
}

// pictureOf is one frame as pixels, drawn by the painter's own code.
//
// # What the painter is showing, not just what the frame says
//
// A thumbnail that disagrees with the panel is worse than no thumbnail, so the picture is the one the
// light is holding rather than the steady frame this module decided. Four things can be on a light
// instead of that frame, and each is published by the writer for exactly this route:
//
//	paused   the light is off, so the picture is black — §3: "The thumbnail shows black because
//	         black is what is on the wall."
//	plain    a moment that shows a colour painted the whole light in it, and that is the picture.
//	word     a state's word is on the panel, drawn by the same rasteriser the event layer uses.
//	preset   the light is running one of its own presets, which this firmware cannot be read back,
//	         so the steady frame stands and the answer says in a sentence that a preset is running.
//
// # The three cases that are left
//
// A panel that its own layout composes is composed, by the same function the steady picture goes
// through, with the layout that binding composes with. A panel with nothing to compose is the raster
// the panel's own segment bodies draw. A strip is the frame's fill: its colour across the pixels the
// job has completed and black past them, which is exactly what the strip body writes — the run's
// length is the frame's own Lit, with no rounding between, because a strip's unit is one LED.
func (api framesAPI) pictureOf(frame render.Frame, shape bindingShape) ([]wled.RGB, error) {
	width, height := shape.pixelsOf(frame)

	if width <= 0 || height <= 0 {
		return nil, fmt.Errorf("a frame of %d x %d is not a picture", width, height)
	}

	if shape.paused {
		return dark(width * height), nil
	}

	if shape.plain != nil {
		return api.plainPicture(*shape.plain, shape)
	}

	if shape.word != "" && shape.geometry.matrix() {
		pixels, err := event{word: shape.word, colour: shape.wordColour}.draw(shape.geometry, api.now())
		if err != nil {
			return nil, fmt.Errorf("the word %q the panel is holding was not drawn: %w", shape.word, err)
		}

		return pixels, nil
	}

	shape.layout = shape.composing(api.layout())

	if shape.geometry.matrix() && drawsComposed(frame, shape.geometry, shape.layout) {
		picture := panelPicture{frame: frame, layout: shape.layout, at: api.now()}

		pixels, err := picture.draw(shape.geometry)
		if err != nil {
			return nil, fmt.Errorf("the composed frame was not drawn: %w", err)
		}

		return pixels, nil
	}

	if shape.geometry.matrix() {
		return matrix.Raster(frame, matrix.Geometry{Width: shape.geometry.width, Height: shape.geometry.height})
	}

	pixels := make([]wled.RGB, width)

	lit := frame.Lit
	if lit <= 0 || lit > width {
		// Nothing completed, or a frame whose fill is the whole range: the run is one colour,
		// which is what render.Frame's own rule says and what the strip body writes.
		lit = width
	}

	for index := range pixels {
		if index < lit {
			pixels[index] = frame.Colour

			continue
		}

		pixels[index] = frame.Unfilled()
	}

	return pixels, nil
}

// plainPicture is a moment's colour as the whole light: the colour over the binding's own run, which
// is what the segments write that painted it did.
func (api framesAPI) plainPicture(frame render.Frame, shape bindingShape) ([]wled.RGB, error) {
	width, height := shape.pixelsOf(frame)

	if width <= 0 || height <= 0 {
		return nil, fmt.Errorf("a frame of %d x %d is not a picture", width, height)
	}

	if shape.geometry.matrix() {
		return matrix.Raster(frame, matrix.Geometry{Width: shape.geometry.width, Height: shape.geometry.height})
	}

	pixels := make([]wled.RGB, width)
	for index := range pixels {
		pixels[index] = frame.Colour
	}

	return pixels, nil
}

// dark is a light that is off: every pixel in the colour an empty range is drawn in, which is the
// black this module paints behind everything else.
func dark(pixels int) []wled.RGB {
	off := make([]wled.RGB, pixels)

	for index := range off {
		off[index] = render.Frame{}.Unfilled()
	}

	return off
}

// encode is one picture as a PNG, from the cache when the frame has not changed.
//
// The cost of a miss is one buffer of the display's own size and a deflate of it, measured in the
// tests beside this file: a 256 x 64 panel of three colours is a few hundred bytes of PNG and well
// under a millisecond of work on the host this runs on. That is cheap enough to be honest about —
// and the cache exists because the Overview asks again every few seconds for a frame that has not
// moved, not because the encoding is expensive.
func (api framesAPI) encode(pixels []wled.RGB, width, height int, key string) (imageAnswer, error) {
	if encoded, held := api.cache.get(key); held {
		return imageAnswer{
			MediaType: "image/png",
			Width:     width,
			Height:    height,
			Bytes:     len(encoded),
			Base64:    base64.StdEncoding.EncodeToString(encoded),
			Cached:    true,
		}, nil
	}

	encoded, err := pngOf(pixels, width, height)
	if err != nil {
		return imageAnswer{}, err
	}

	api.cache.put(key, encoded)

	return imageAnswer{
		MediaType: "image/png",
		Width:     width,
		Height:    height,
		Bytes:     len(encoded),
		Base64:    base64.StdEncoding.EncodeToString(encoded),
	}, nil
}

// pngOf draws one buffer of colours as a PNG, one image pixel per LED.
//
// No scaling and no decoration: the picture is the light's own pixels, and a page that wants it big
// says so in CSS. BestSpeed because the compression ratio of a picture with three flat colours is
// already enormous and the millisecond it saves is the one this route is trying not to spend.
func pngOf(pixels []wled.RGB, width, height int) ([]byte, error) {
	if width <= 0 || height <= 0 || len(pixels) < width*height {
		return nil, fmt.Errorf("a picture of %d x %d does not fit %d colours", width, height, len(pixels))
	}

	picture := image.NewRGBA(image.Rect(0, 0, width, height))

	for index := 0; index < width*height; index++ {
		one := pixels[index]
		picture.SetRGBA(index%width, index/width, color.RGBA{R: one.R, G: one.G, B: one.B, A: 255})
	}

	var encoded bytes.Buffer

	encoder := png.Encoder{CompressionLevel: png.BestSpeed}

	if err := encoder.Encode(&encoded, picture); err != nil {
		return nil, fmt.Errorf("encode the picture: %w", err)
	}

	return encoded.Bytes(), nil
}

// cacheKey is what makes a cached picture honest: every fact the picture is made from, and nothing
// else. Two frames that draw the same bytes share an entry; a frame that moved does not.
func cacheKey(light string, held bindingStatus, shape bindingShape, width, height int, at time.Time) string {
	parts := make([]any, 0, 10)
	parts = append(parts,
		light, held.Controller, held.Segment, width, height,
		held.Frame.Colour, held.Frame.Lit, held.Frame.Total, held.Frame.Percent,
		held.Frame.Name, held.Frame.Reason,
		// What the light is showing instead of that frame, and the arrangement a panel is composed
		// with: a cached picture served across a change of any of these would be a thumbnail of a
		// light that no longer looks like that.
		shape.paused, shape.word, shape.wordColour, shape.preset, shape.laidOut,
		shape.layout.bar, shape.layout.barBand, shape.layout.barRows, shape.layout.percent,
		shape.layout.percentScale, shape.layout.name, shape.layout.rotation,
		int(shape.layout.rotate/time.Second),
	)

	if shape.plain != nil {
		parts = append(parts, shape.plain.Colour, shape.plain.Lit, shape.plain.Total, shape.plain.Reason)
	} else {
		parts = append(parts, nil)
	}

	// The half of the rotation a take-turns panel is showing is part of the picture: two pictures of
	// one frame that differ only in which half is up are two pictures, and a cache that served one for
	// the other would disagree with the panel for fifteen seconds at a time.
	parts = append(parts, rotationHalf(at, shape.layout.rotate))

	key, err := json.Marshal(parts)
	if err != nil {
		// A key that cannot be built is a cache that cannot be used, and never a wrong picture.
		return ""
	}

	return string(key)
}

// layout is the arrangement of a panel this instance runs on, which is the one the painter composes
// with: the layout table in the store is a record this build does not draw from yet.
func (api framesAPI) layout() layout {
	if api.live == nil {
		return defaultLayout()
	}

	return api.live.shape()
}

// now is the moment a composed picture's rotation is read at.
func (api framesAPI) now() time.Time {
	if api.clock != nil {
		return api.clock()
	}

	return time.Now()
}
