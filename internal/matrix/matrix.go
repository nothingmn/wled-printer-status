// Package matrix lays a render.Frame out on a matrix as a buffer of pixels.
//
// It answers the second half of the question a frame asks: render.Decide says what
// colour and how much of the range, and this says where those pixels are. The answer
// for the shape the module draws today is one rectangle — the columns from the first
// to a boundary, across every row, in the frame's colour, and the columns after it in
// the colour an empty range is drawn in.
//
// It exists because a frame now has two ways to reach a controller and they have to
// show the same picture. JSON draws the frame as segments and lets the controller
// work out the pixels; DDP carries the pixels themselves, so something here has to
// decide which pixel is which. That decision belongs in one place with no transport
// in it, so that the two backends can be compared and, when the composer arrives, so
// that the layout has one home to grow in.
//
// # The same rule the module's painter applies, said twice
//
// The module's painter states this layout as well, in the two segments it writes, and
// it cannot call this package yet: it is unexported and the writer predates this. So
// the fill boundary is written twice — fillColumns in the root package and
// FillColumns here — and the agreement test in the root package compares the two
// pictures at five fractions, pixel for pixel, through the pixels the JSON body
// describes. That test is the guard against the two drifting apart. When the composer
// lands, the painter's copy goes and calls this one, and the test then guards a single
// statement of the rule rather than two.
//
// # What it is not
//
// No transport, no socket, no clock, no printer. It turns one frame and one geometry
// into pixels, and nothing else in the module knows the layout when it changes.
package matrix

import (
	"errors"
	"fmt"
	"math"

	"github.com/nothingmn/wled-printer-status/internal/render"
	"github.com/nothingmn/wled-printer-status/internal/wled"
)

// The failures a caller can tell apart.
var (
	// ErrNoMatrix means the geometry is not a matrix: a controller that drives one run
	// of pixels publishes no rows, and a frame laid out for it is the 1D path's, not
	// this one's.
	ErrNoMatrix = errors.New("matrix: not a matrix")

	// ErrShortBuffer means the caller's buffer cannot hold the panel's pixels. Refused
	// rather than filled in part: half a panel is a picture nobody asked for.
	ErrShortBuffer = errors.New("matrix: the buffer is smaller than the panel")
)

// Geometry is a controller's matrix: how many pixels wide it is and how many rows it
// has.
//
// Both dimensions are needed before a controller counts as a matrix. WLED publishes
// "leds.matrix" only on a build configured as one, and a half-read geometry — a width
// with no height, or the other way round — is a strip with a number missing. The zero
// value is therefore a strip, and Raster refuses it.
type Geometry struct {
	// Width is the panel's width in pixels.
	Width int
	// Height is the panel's height in rows.
	Height int
}

// Matrix reports whether this geometry is a matrix at all.
func (g Geometry) Matrix() bool { return g.Width > 0 && g.Height > 0 }

// Pixels is how many pixels the panel holds: the width times the height, and the
// length every buffer below is measured in.
func (g Geometry) Pixels() int {
	if !g.Matrix() {
		return 0
	}

	return g.Width * g.Height
}

// Raster returns the picture for one frame on this panel: a new buffer of the panel's
// own size, in row-major order, so that pixel (x, y) is buf[y*Width+x].
//
// Row-major is the controller's own order, not a choice made here: a per-LED write
// addresses a panel pixel by y*width+x, and DDP's live buffer is the same run in the
// same order. A buffer laid out the other way puts the picture on its side.
//
// A geometry that is not a matrix is refused with ErrNoMatrix. One allocation of
// Width*Height colours is 49,152 bytes on a 256x64 panel; a caller that repaints often
// holds a buffer and calls RasterInto instead.
func Raster(frame render.Frame, geometry Geometry) ([]wled.RGB, error) {
	pixels := geometry.Pixels()
	if pixels == 0 {
		return nil, fmt.Errorf("%w: %dx%d", ErrNoMatrix, geometry.Width, geometry.Height)
	}

	buffer := make([]wled.RGB, pixels)

	if err := RasterInto(buffer, frame, geometry); err != nil {
		return nil, err
	}

	return buffer, nil
}

// RasterInto writes the picture for one frame into dst, which must hold at least
// geometry.Pixels() colours, and leaves anything past them alone.
//
// It is the same picture as Raster without the allocation, which is what a writer
// repainting a panel once a second wants: the buffer is allocated when the geometry is
// learned and reused for the life of the process.
func RasterInto(dst []wled.RGB, frame render.Frame, geometry Geometry) error {
	if !geometry.Matrix() {
		return fmt.Errorf("%w: %dx%d", ErrNoMatrix, geometry.Width, geometry.Height)
	}

	pixels := geometry.Pixels()
	if len(dst) < pixels {
		return fmt.Errorf("%w: %d colours for %d pixels", ErrShortBuffer, len(dst), pixels)
	}

	dst = dst[:pixels]

	// The two edges are one picture, not a bar of no width. A frame with nothing filled
	// has no completed part to mark a boundary against, and a frame whose boundary
	// reached the last column has no remainder; both are the whole panel in the frame's
	// colour. The JSON body says the same thing by switching its remainder segment off,
	// and a renderer that painted a black panel for an idle printer would be saying the
	// printer had said nothing.
	if frame.Lit <= 0 || FillColumns(frame, geometry) >= geometry.Width {
		for index := range dst {
			dst[index] = frame.Colour
		}

		return nil
	}

	columns := FillColumns(frame, geometry)
	unfilled := frame.Unfilled()

	for row := 0; row < geometry.Height; row++ {
		line := dst[row*geometry.Width : (row+1)*geometry.Width]

		for column := range line {
			if column < columns {
				line[column] = frame.Colour

				continue
			}

			line[column] = unfilled
		}
	}

	return nil
}

// FillColumns is how many of the panel's columns carry the completed part of the job:
// the frame's fraction of the panel's width, rounded to the nearest column, and never
// outside one column to all of them.
//
// The fraction is the frame's own — Lit out of Total — and it is rounded rather than
// floored because a column is a twelfth of a row here: on a 256-wide panel one column
// is 64 pixels, so flooring would throw away most of a percent of the job and the bar
// would sit still while the printer was working. That is the opposite of the choice
// render makes for a 48-LED strip, where one LED is over two percent and flooring keeps
// the bar from ever claiming more than was reported. The unit decides.
//
// One column at least, because a job that has started and rounded away to nothing would
// be a dark panel, which is a light saying the printer had said nothing. The whole
// width at most, because a fill that reaches the last column is the "everything filled"
// edge and not a bar with a remainder of no width.
func FillColumns(frame render.Frame, geometry Geometry) int {
	if frame.Total <= 0 || frame.Lit <= 0 || geometry.Width <= 0 {
		return 0
	}

	// The one place the layout says what "across" means. This build fills across the
	// columns; a layout that filled down the rows would measure the frame's fraction
	// against the height and nothing below it would change. It is here, in a function of
	// its own, because the composer's layout will be a change to this and to nothing else.
	across := geometry.Width

	columns := int(math.Round(float64(frame.Lit) / float64(frame.Total) * float64(across)))

	return min(max(columns, 1), across)
}
