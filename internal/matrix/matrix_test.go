package matrix

import (
	"errors"
	"fmt"
	"testing"

	"github.com/nothingmn/wled-printer-status/internal/render"
	"github.com/nothingmn/wled-printer-status/internal/wled"
)

// livePanel is the matrix the module was proved against: 256 columns of 64 rows.
var livePanel = Geometry{Width: 256, Height: 64}

// pixel is one pixel of a buffer, read back as the coordinates it belongs to, so a
// failure says where on the panel the picture is wrong rather than at which index.
func pixel(buffer []wled.RGB, geometry Geometry, index int) string {
	return fmt.Sprintf("(%d,%d)=%+v", index%geometry.Width, index/geometry.Width, buffer[index])
}

// TestTheWholePanelIsTheFramesColourAtTheEdges is the two frames that have no bar in
// them: nothing filled, and everything filled. Both are the panel in the frame's colour
// with no remainder, because a frame with no completed part has no boundary to mark and
// a frame whose boundary reached the last column has nothing after it.
//
// The alternative — a dark panel for an idle printer — would be a light saying the
// printer had said nothing, which is what the dim colours exist to avoid.
func TestTheWholePanelIsTheFramesColourAtTheEdges(t *testing.T) {
	for _, edge := range []struct {
		name  string
		frame render.Frame
	}{
		{name: "nothing filled", frame: render.Frame{Colour: render.ColourIdle, Lit: 0, Total: livePanel.Pixels()}},
		{name: "everything filled", frame: render.Frame{Colour: render.ColourFinished, Lit: livePanel.Pixels(), Total: livePanel.Pixels()}},
		{name: "more than everything", frame: render.Frame{Colour: render.ColourPrinting, Lit: livePanel.Pixels() * 2, Total: livePanel.Pixels()}},
	} {
		t.Run(edge.name, func(t *testing.T) {
			buffer, err := Raster(edge.frame, livePanel)
			if err != nil {
				t.Fatalf("the frame was refused: %v", err)
			}

			if len(buffer) != livePanel.Pixels() {
				t.Fatalf("the buffer is %d pixels, want %d", len(buffer), livePanel.Pixels())
			}

			for index := range buffer {
				if buffer[index] != edge.frame.Colour {
					t.Fatalf("pixel %s is not the frame's colour %+v", pixel(buffer, livePanel, index), edge.frame.Colour)
				}
			}
		})
	}
}

// TestTheFillIsAColumnCountAcrossEveryRow is the shape itself: the boundary is a column,
// the fill runs from the first column to it in every row, and the columns after it are
// the unfilled colour in every row.
//
// The boundary is the module's own — 62 % of a panel whose LED count is 16,384 is 10,158
// pixels, which is 159 columns of 256 — so this test says what the picture is at that
// column and not what some fraction in this package rounds to.
func TestTheFillIsAColumnCountAcrossEveryRow(t *testing.T) {
	frame := render.Frame{Colour: render.ColourPrinting, Lit: 10158, Total: livePanel.Pixels()}

	columns := FillColumns(frame, livePanel)
	if columns != 159 {
		t.Fatalf("62 %% fills %d columns, want 159", columns)
	}

	buffer, err := Raster(frame, livePanel)
	if err != nil {
		t.Fatalf("the frame was refused: %v", err)
	}

	lit, dark := 0, 0

	for row := 0; row < livePanel.Height; row++ {
		for column := 0; column < livePanel.Width; column++ {
			got := buffer[row*livePanel.Width+column]

			switch {
			case column < columns:
				if got != frame.Colour {
					t.Fatalf("pixel (%d,%d) is %+v, want the frame's colour %+v", column, row, got, frame.Colour)
				}

				lit++
			default:
				if got != frame.Unfilled() {
					t.Fatalf("pixel (%d,%d) is %+v, want the unfilled colour %+v", column, row, got, frame.Unfilled())
				}

				dark++
			}
		}
	}

	if want := columns * livePanel.Height; lit != want {
		t.Fatalf("%d pixels are lit, want %d: %d columns over %d rows", lit, want, columns, livePanel.Height)
	}

	if want := livePanel.Pixels() - columns*livePanel.Height; dark != want {
		t.Fatalf("%d pixels are dark, want %d", dark, want)
	}
}

// TestTheBoundaryIsNeverOutsideOneColumnToAllOfThem is the arithmetic under the shape, at
// the values a job actually passes through, and at both of its ends.
func TestTheBoundaryIsNeverOutsideOneColumnToAllOfThem(t *testing.T) {
	const total = 16384

	for _, shape := range []struct {
		name string
		lit  int
		want int
	}{
		{name: "nothing filled", lit: 0, want: 0},
		{name: "a job that has just started", lit: 1, want: 1},
		{name: "a tenth", lit: 1638, want: 26},
		{name: "sixty-two percent", lit: 10158, want: 159},
		{name: "eighty-eight percent", lit: 14417, want: 225},
		{name: "half", lit: 8192, want: 128},
		{name: "one pixel short", lit: total - 1, want: 256},
		{name: "everything filled", lit: total, want: 256},
		{name: "more than everything", lit: total * 2, want: 256},
	} {
		t.Run(shape.name, func(t *testing.T) {
			got := FillColumns(render.Frame{Lit: shape.lit, Total: total}, livePanel)
			if got != shape.want {
				t.Fatalf("%d of %d pixels fills %d columns, want %d", shape.lit, total, got, shape.want)
			}
		})
	}

	// A frame decided before the controller had said how many pixels it drives has no
	// fraction to apply, and no column is an honest answer.
	if got := FillColumns(render.Frame{Lit: 0, Total: 0}, livePanel); got != 0 {
		t.Fatalf("a frame with no total fills %d columns, want 0", got)
	}

	// A frame that claims a fill with no total at all is the same case: the fraction is
	// undefined and must not be invented.
	if got := FillColumns(render.Frame{Lit: 100, Total: 0}, livePanel); got != 0 {
		t.Fatalf("a frame with a fill and no total fills %d columns, want 0", got)
	}
}

// TestAStripIsNotAPanel is the geometry this package refuses: a controller that drives
// one run of pixels publishes no rows, and a frame laid out for it belongs to the 1D
// path. One dimension without the other is the same refusal, because a half-read matrix
// is a strip with a number missing and not a panel.
func TestAStripIsNotAPanel(t *testing.T) {
	frame := render.Frame{Colour: render.ColourPrinting, Lit: 24, Total: 48}

	for _, geometry := range []Geometry{
		{},
		{Width: 256},
		{Height: 64},
		{Width: 0, Height: 64},
	} {
		if _, err := Raster(frame, geometry); !errors.Is(err, ErrNoMatrix) {
			t.Fatalf("%+v returns %v, want ErrNoMatrix", geometry, err)
		}

		if err := RasterInto(make([]wled.RGB, 16384), frame, geometry); !errors.Is(err, ErrNoMatrix) {
			t.Fatalf("%+v returns %v, want ErrNoMatrix", geometry, err)
		}

		if geometry.Matrix() {
			t.Fatalf("%+v reports itself a matrix", geometry)
		}

		if got := geometry.Pixels(); got != 0 {
			t.Fatalf("%+v holds %d pixels, want 0", geometry, got)
		}
	}
}

// TestRasterIntoReusesTheCallersBuffer is the writer's path: one buffer allocated when
// the geometry is learned, filled again for every frame, and nothing allocated after
// that. It is also the check that the two entry points draw the same picture.
func TestRasterIntoReusesTheCallersBuffer(t *testing.T) {
	buffer := make([]wled.RGB, livePanel.Pixels())
	before := &buffer[0]

	frame := render.Frame{Colour: render.ColourPrinting, Lit: 4096, Total: livePanel.Pixels()}

	if err := RasterInto(buffer, frame, livePanel); err != nil {
		t.Fatalf("the frame was refused: %v", err)
	}

	if &buffer[0] != before {
		t.Fatalf("RasterInto moved the caller's buffer")
	}

	allocated, err := Raster(frame, livePanel)
	if err != nil {
		t.Fatalf("the frame was refused: %v", err)
	}

	if len(allocated) != len(buffer) {
		t.Fatalf("Raster made %d pixels and RasterInto filled %d", len(allocated), len(buffer))
	}

	for index := range allocated {
		if allocated[index] != buffer[index] {
			t.Fatalf("pixel %s differs between the two entry points", pixel(buffer, livePanel, index))
		}
	}

	// A buffer with one pixel too few is refused rather than filled in part: half a panel
	// is a picture nobody asked for.
	short := make([]wled.RGB, livePanel.Pixels()-1)
	if err := RasterInto(short, frame, livePanel); !errors.Is(err, ErrShortBuffer) {
		t.Fatalf("a short buffer returns %v, want ErrShortBuffer", err)
	}

	// A larger buffer is filled as far as the panel goes and left alone after it.
	roomy := make([]wled.RGB, livePanel.Pixels()+8)
	if err := RasterInto(roomy, frame, livePanel); err != nil {
		t.Fatalf("a roomy buffer was refused: %v", err)
	}

	for index := livePanel.Pixels(); index < len(roomy); index++ {
		if roomy[index] != (wled.RGB{}) {
			t.Fatalf("RasterInto wrote past the panel at %d", index)
		}
	}
}

// TestThePictureIsRowMajor is the order the buffer is in, and it is the controller's and
// not this package's: a panel pixel is addressed as y*width+x, so the first column of a
// four-wide panel is entries 0, 4 and 8 and not entries 0, 1 and 2.
//
// A buffer laid out the other way draws the picture on its side, which is the kind of
// mistake a test of the fill's area cannot see: the count is right and the picture is
// rotated.
func TestThePictureIsRowMajor(t *testing.T) {
	geometry := Geometry{Width: 4, Height: 3}

	// One column filled, which is the smallest fill the rule allows.
	frame := render.Frame{Colour: render.ColourPrinting, Lit: 1, Total: geometry.Pixels()}

	buffer, err := Raster(frame, geometry)
	if err != nil {
		t.Fatalf("the frame was refused: %v", err)
	}

	if len(buffer) != 12 {
		t.Fatalf("the buffer is %d pixels, want 12", len(buffer))
	}

	for index := range buffer {
		// The first column of every row, and nothing else.
		lit := index%geometry.Width == 0

		want := frame.Unfilled()
		if lit {
			want = frame.Colour
		}

		if buffer[index] != want {
			t.Fatalf("pixel %s is %+v, want %+v", pixel(buffer, geometry, index), buffer[index], want)
		}
	}
}
