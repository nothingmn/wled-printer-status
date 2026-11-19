// The diagnostic ladder: three pictures, each answering one question, drawn over the same
// transport and the same hold the word already uses.
//
// # Why one picture is not enough
//
// The word FINISHED is the picture the operator wants to see, and it is a single look that can
// fail in three different ways without saying which. A dark panel is what he sees if the datagrams
// never reach the controller, if they reach it and it draws its own segments instead, or if it
// draws them with a map this module's arithmetic does not match — and those three faults want
// three different repairs. The word cannot separate them, because the word is one shape in one
// colour: there is nothing in it that a wrong orientation would visibly break.
//
// So each rung removes one answer:
//
//   - solid asks *does DDP draw at all*. The whole panel is one colour, so no map, no orientation
//     and no glyph can be wrong about it. Lit means the transport and the drawing work, and every
//     later rung is worth looking at; dark means the transport is the fault and the word would
//     have been dark for a reason that has nothing to do with the word.
//   - border asks *is the map the shape we assume*. Two pixels of white around the entire edge and
//     nothing inside. A closed rectangle means the panel really is indexed y*width+x with x across
//     and y down, because anything else — a diagonal, a run of strips, a border in the wrong place,
//     a border that is half a rectangle — is not a shape this arithmetic can produce on wiring
//     that disagrees with it.
//   - quarters asks *which way up and which way round*. Four colours in four quadrants, so an
//     upside-down or mirrored panel is not a subtlety any more: it is red in the wrong corner,
//     named, and it says exactly which corner.
//
// Everything about them is a value in this file and nothing is asked of the controller: no effect,
// no segment name drawn, no document. They are pixels over DDP exactly as the word is, they hold
// for the same length of time, they are pushed again once a second by the same writer, and the
// steady frame takes the light back at the end the same way — see pixels.go for the transport and
// event.go for the hold.
package main

import (
	"errors"
	"fmt"
	"time"

	"github.com/nothingmn/wled-printer-status/internal/wled"
)

// The colours the diagnostic pictures are drawn in.
//
// Pure primaries and pure white, and nothing dimmed, because each of them has to answer a question
// at three metres rather than look pleasant: a half brightness border in the finished green would
// be a picture this module's own state table could have produced, and the point of these is that
// no printer state can be mistaken for one.
//
// White is the one that matters. No colour in internal/render has all three channels lit — printing
// and finished are 0,170,0, idle and busy are 210,165,0, an error is 200,0,0, stopped is 80,80,80
// and unknown is a dimmed blue — so a white panel is not a state the light is ever in, and white at
// full is the brightest thing this panel can draw. The three quarter colours are full red, green
// and blue for the same reason: they are four values a person can name out loud from the doorway
// and none of them is a state's colour.
//
//nolint:gochecknoglobals // four values, written once, read by the pictures below.
var (
	patternWhite = wled.RGB{R: 255, G: 255, B: 255}
	patternRed   = wled.RGB{R: 255, G: 0, B: 0}
	patternGreen = wled.RGB{R: 0, G: 255, B: 0}
	patternBlue  = wled.RGB{R: 0, G: 0, B: 255}
)

// patternBorderThickness is how many pixels thick the border picture's edge is: two, in every
// direction.
//
// Two and not one, because a single row of LEDs at the edge of a 256x64 panel is the one place a
// wiring fault is hardest to see: a row of white that is a column instead looks like a thin
// rectangle at a glance, while two pixels of white against black has a width a person can compare
// with the panel's own edge. It is also thin enough to leave the middle of the panel empty, which
// is half of what the picture is for — anything lit inside the rectangle is a fault, not decoration.
const patternBorderThickness = 2

// patternShape is one rung of the ladder: what the route calls it, what a person learns from it,
// and the pixels it lays down.
type patternShape struct {
	// name is what the pattern parameter accepts, and it is also the name the establishing body
	// gives the segment, so that /json/state says which picture the panel is holding. A datagram
	// carries no name, which is why the name goes out over JSON at all.
	name string

	// answers is the fault this picture separates out, in the words a person would use walking up
	// to the panel. It is answered by the route, so that whoever fires one does not have to hold
	// three meanings in his head to read the result.
	answers string

	// runAnswers is the same sentence for a light that is one run of pixels rather than a
	// rectangle. A strip has no corners and no second dimension, so the panel's sentence would be
	// describing a picture that is not on the wall; empty means the panel's sentence is also true
	// of a strip, which is the case for solid.
	runAnswers string

	// frame lays the picture out as the whole light: the panel's rectangle in the controller's own
	// order — pixel (x, y) is y*width+x — or a strip's single row, one pixel per LED.
	frame func(geometry panel) []wled.RGB
}

// answersOn is what this picture answers on one light, in the words for that light's shape.
func (s patternShape) answersOn(geometry panel) string {
	if geometry.matrix() || s.runAnswers == "" {
		return s.answers
	}

	return s.runAnswers
}

// patternShapes is the ladder, in the order a person should walk it: does anything draw, is the
// shape right, is it the right way up. Each one assumes the answer before it.
//
//nolint:gochecknoglobals // the table is the ladder; three entries that never change.
var patternShapes = []patternShape{
	{
		name:    "solid",
		answers: "does DDP draw at all: a lit panel means the transport works, a dark one means no datagram was drawn and the word would be dark for that reason alone",
		// The one picture whose question a strip answers exactly as a panel does: the whole run
		// lit, or nothing drawn.
		frame: solidFrame,
	},
	{
		name:       "border",
		answers:    "is the LED map the shape this module assumes: a closed white rectangle two pixels thick means row-major y*width+x with x across and y down, anything else means the panel is wired or mapped differently",
		runAnswers: "is the run the length this module assumes: white at both ends and nothing between them, so a run that is shorter or longer than the controller says shows its white in the wrong place",
		frame:      borderFrame,
	},
	{
		name:       "quarters",
		answers:    "which way up and which way round: red top-left, green top-right, blue bottom-left, white bottom-right, so an upside-down or mirrored panel names the corner it is wrong in",
		runAnswers: "which way round the run is: red at the start, green, blue and white at the far end, so a run wired backwards, or one shorter than this module believes, names the wrong end",
		frame:      quartersFrame,
	},
}

// ErrUnknownPattern is a pattern name this module does not draw. It is a refusal rather than a
// fallback to a default picture: a caller asking for "quaters" wants to know the name missed,
// because a picture nobody asked for is a look that answers a question nobody asked.
var ErrUnknownPattern = errors.New("this module draws no pattern of that name")

// patternNamed is the ladder's entry for a name, and whether the ladder holds it.
func patternNamed(wanted string) (patternShape, bool) {
	for _, shape := range patternShapes {
		if shape.name == wanted {
			return shape, true
		}
	}

	return patternShape{}, false
}

// patternNames is the three names, in the order a person walks them, for a refusal that has to say
// what it would have accepted.
func patternNames() []string {
	names := make([]string, 0, len(patternShapes))

	for _, shape := range patternShapes {
		names = append(names, shape.name)
	}

	return names
}

// patternChoices is the ladder as one sentence, for a refusal and for a log line.
func patternChoices() string {
	switch len(patternShapes) {
	case 0:
		return "no pattern"
	case 1:
		return patternShapes[0].name
	}

	choices := patternShapes[0].name

	for _, shape := range patternShapes[1 : len(patternShapes)-1] {
		choices += ", " + shape.name
	}

	return choices + " and " + patternShapes[len(patternShapes)-1].name
}

// patternFrame draws one pattern over the whole panel, or says the name is not one this module
// knows.
func patternFrame(name string, geometry panel) ([]wled.RGB, error) {
	shape, ok := patternNamed(name)
	if !ok {
		return nil, fmt.Errorf("%w: %q is not one of them: this module draws %s", ErrUnknownPattern, name, patternChoices())
	}

	return shape.frame(geometry), nil
}

// patternEvent is one pattern as an event: the picture, and how long it holds the light.
//
// It is an event and not a second route through the writer, so that a pattern goes out behind the
// same gate, the same backoff, the same once-a-second repeat and the same hand-back the word does.
// The word's colour is left at zero: a pattern carries its own colours per pixel and there is no
// single one to name.
func patternEvent(name string, holds time.Duration) (event, error) {
	shape, ok := patternNamed(name)
	if !ok {
		return event{}, fmt.Errorf("%w: %q is not one of them: this module draws %s", ErrUnknownPattern, name, patternChoices())
	}

	return event{pattern: shape.name, holds: holds}, nil
}

// solidFrame is the whole panel in white: the transport question, with nothing in the picture that
// a wrong map or a wrong orientation could change.
func solidFrame(geometry panel) []wled.RGB {
	frame := make([]wled.RGB, geometry.width*geometry.height)

	for index := range frame {
		frame[index] = patternWhite
	}

	return frame
}

// borderFrame is two pixels of white around the panel's whole edge and the background everywhere
// else, or — on a light that is one run of pixels — white at the two ends and the background
// between them.
//
// The inside is finishedWordBackground rather than black written out here, for the reason the word
// uses it: it is the colour the steady frame gives an unfilled range, read from the renderer, so
// the picture the light rests at afterwards and the inside of this one cannot drift apart. Today
// that colour is black, and a dark interior is what makes two pixels of white read as an edge.
//
// A strip's answer is its two ends, because that is what it has: a run of forty-eight pixels given
// white at 0 and 47 and nothing between says whether the run the controller draws is the run this
// module believes, which is the same question the rectangle's edge answers for a panel. Its
// thickness is one pixel and not two, because two pixels at each end of a short run is most of the
// run, and the dark middle is what the picture is read by.
func borderFrame(geometry panel) []wled.RGB {
	frame := make([]wled.RGB, geometry.width*geometry.height)

	if !geometry.matrix() {
		if len(frame) > 0 {
			frame[0] = patternWhite
			frame[len(frame)-1] = patternWhite
		}

		return frame
	}

	for y := range geometry.height {
		for x := range geometry.width {
			edge := x < patternBorderThickness || y < patternBorderThickness ||
				x >= geometry.width-patternBorderThickness || y >= geometry.height-patternBorderThickness

			if edge {
				frame[y*geometry.width+x] = patternWhite

				continue
			}

			frame[y*geometry.width+x] = finishedWordBackground
		}
	}

	return frame
}

// quartersFrame is the panel in four blocks: red top-left, green top-right, blue bottom-left, white
// bottom-right. On a light that is one run of pixels it is four bands along the run, in the same
// order: red at the start, then green, then blue, then white at the far end.
//
// The split is at the panel's half, floor-divided, so an odd dimension leaves the extra column or
// row in the right-hand or bottom block. This panel is 256x64, where the question does not arise;
// it is written down because a fallback that silently picked a side would be a second answer to
// where the middle is. A strip's quarter is its length quartered the same way, so a run of 48 is
// four bands of twelve and a run of 50 is twelve, twelve, twelve and fourteen — the extra pixels
// at the far end, where the panel puts its extra column.
//
// A run too short to quarter — fewer than four pixels — gives each pixel its own colour in order
// and repeats the last one, which is the same picture degraded rather than a division by zero.
func quartersFrame(geometry panel) []wled.RGB {
	frame := make([]wled.RGB, geometry.width*geometry.height)

	if !geometry.matrix() {
		quarter := geometry.width / 4
		colours := [4]wled.RGB{patternRed, patternGreen, patternBlue, patternWhite}

		for x := range geometry.width {
			band := x
			if quarter > 0 {
				band = min(x/quarter, len(colours)-1)
			}

			frame[x] = colours[min(band, len(colours)-1)]
		}

		return frame
	}

	halfWidth, halfHeight := geometry.width/2, geometry.height/2

	for y := range geometry.height {
		for x := range geometry.width {
			var colour wled.RGB

			switch {
			case y < halfHeight && x < halfWidth:
				colour = patternRed
			case y < halfHeight:
				colour = patternGreen
			case x < halfWidth:
				colour = patternBlue
			default:
				colour = patternWhite
			}

			frame[y*geometry.width+x] = colour
		}
	}

	return frame
}
