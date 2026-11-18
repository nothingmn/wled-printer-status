package text

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

// livePanel is the matrix this module was proved against: the operator's 256x64 panel.
var livePanel = Geometry{Width: 256, Height: 64}

// TestTheFontDrawsItsKnownGlyphsBitForBit is the font itself, asserted as bytes.
//
// The letters of the word the module puts on a panel are here one at a time, each beside the
// art it is supposed to be: a glyph that drifted by one pixel would still be a shape, still
// pass a test that only counted lit pixels, and still be wrong on the light. F, I, N, S, H, E
// and D are FINISHED; the rest of the table is held to the same shape by the checks below —
// present, inside the five bits a glyph has, and not blank.
func TestTheFontDrawsItsKnownGlyphsBitForBit(t *testing.T) {
	known := map[rune][GlyphRows]byte{
		// #####  #....  #....  ####.  #....  #....  #....
		'F': {0x1f, 0x10, 0x10, 0x1e, 0x10, 0x10, 0x10},
		// #####  ..#..  ..#..  ..#..  ..#..  ..#..  #####
		'I': {0x1f, 0x04, 0x04, 0x04, 0x04, 0x04, 0x1f},
		// #...#  ##..#  #.#.#  #..##  #...#  #...#  #...#
		'N': {0x11, 0x19, 0x15, 0x13, 0x11, 0x11, 0x11},
		// .####  #....  #....  .###.  ....#  ....#  ####.
		'S': {0x0f, 0x10, 0x10, 0x0e, 0x01, 0x01, 0x1e},
		// #...#  #...#  #...#  #####  #...#  #...#  #...#
		'H': {0x11, 0x11, 0x11, 0x1f, 0x11, 0x11, 0x11},
		// #####  #....  #....  ####.  #....  #....  #####
		'E': {0x1f, 0x10, 0x10, 0x1e, 0x10, 0x10, 0x1f},
		// ####.  #...#  #...#  #...#  #...#  #...#  ####.
		'D': {0x1e, 0x11, 0x11, 0x11, 0x11, 0x11, 0x1e},
	}

	for character, want := range known {
		got, ok := glyphs[character]
		if !ok {
			t.Fatalf("the font holds no glyph for %q", character)
		}

		if got != want {
			t.Fatalf("%q is drawn %v, want %v", character, got, want)
		}
	}

	// Every character the module needs to say something is in the table, and no glyph uses a
	// bit outside the five columns it has: a bit in the sixth or seventh column would be a
	// pixel the rasteriser never draws, which is a glyph that looks correct here and loses
	// half a stroke on the panel.
	needed := "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789 .-%"

	for _, character := range needed {
		glyph, ok := glyphs[character]
		if !ok {
			t.Fatalf("the font holds no glyph for %q", character)
		}

		for row, bits := range glyph {
			if bits&0xe0 != 0 {
				t.Fatalf("%q row %d is 0x%02x, which uses a bit outside the %d columns it has",
					character, row, bits, GlyphColumns)
			}
		}

		if character != ' ' && glyph == [GlyphRows]byte{} {
			t.Fatalf("%q is blank, and only the space may be", character)
		}
	}

	// And nothing else is in the table, so a character that is not in the list above is
	// refused by Raster rather than drawn as whatever happens to be there.
	for character := range glyphs {
		if !slices.Contains([]rune(needed), character) {
			t.Fatalf("the font holds %q, which is not a character this module says", character)
		}
	}
}

// TestTheWordIsCentredAndItsPixelsAreTheseExactOnes is the rasteriser against a panel small
// enough to write the answer out: "HI" at a scale of one on a 16x8 panel.
//
// Two glyphs are 5 + 1 + 5 = 11 columns, so the word is 11x7 and the panel leaves 5 columns
// and 1 row of slack: the word starts at column 2 and row 0, both floored halves. The index
// list below is every lit pixel of that picture, row-major, and it is asserted as a list
// because that is what the caller is handed — a buffer offset, not a shape.
func TestTheWordIsCentredAndItsPixelsAreTheseExactOnes(t *testing.T) {
	panel := Geometry{Width: 16, Height: 8}

	place, err := Place("HI", 1, panel)
	if err != nil {
		t.Fatalf("place HI on a 16x8 panel: %v", err)
	}

	if want := (Placement{Left: 2, Top: 0, Width: 11, Height: 7}); place != want {
		t.Fatalf("HI lands at %+v, want %+v", place, want)
	}

	lit, err := Raster("HI", 1, panel)
	if err != nil {
		t.Fatalf("rasterise HI on a 16x8 panel: %v", err)
	}

	// The picture, as columns of a 16-wide row:
	//
	//	..H...#I.I...    columns 2 and 6 are H's uprights, 8 to 12 are I's top bar
	//	..#....#.....
	//	..#....#.....
	//	..#####......
	//	..#....#.....
	//	..#....#.....
	//	..H...#I.I...
	want := []int{
		2, 6, 8, 9, 10, 11, 12,
		18, 22, 26,
		34, 38, 42,
		50, 51, 52, 53, 54, 58,
		66, 70, 74,
		82, 86, 90,
		98, 102, 104, 105, 106, 107, 108,
	}

	if !slices.Equal(lit, want) {
		t.Fatalf("HI is the pixels %v, want %v", lit, want)
	}
}

// TestTheFinishedWordFitsALivePanelAtScaleFive is the arithmetic the event depends on, as
// numbers rather than as a claim.
//
// The word is eight glyphs and seven one-column gaps: 5*8 + 7 = 47 columns. At a scale of 5
// each column is five pixels wide and each row five pixels tall, so the word is 235 pixels
// across and 35 down. On the operator's 256x64 panel that leaves 21 columns and 29 rows of
// slack, so it starts at column 10 and row 14 and has room above and below it.
func TestTheFinishedWordFitsALivePanelAtScaleFive(t *testing.T) {
	const (
		word  = "FINISHED"
		scale = 5
	)

	size, err := Size(word, scale)
	if err != nil {
		t.Fatalf("measure %q at a scale of %d: %v", word, scale, err)
	}

	if size.Width != 235 || size.Height != 35 {
		t.Fatalf("%q at a scale of %d is %dx%d, and this build says 235x35",
			word, scale, size.Width, size.Height)
	}

	place, err := Place(word, scale, livePanel)
	if err != nil {
		t.Fatalf("%q at a scale of %d does not fit a %dx%d panel: %v",
			word, scale, livePanel.Width, livePanel.Height, err)
	}

	if want := (Placement{Left: 10, Top: 14, Width: 235, Height: 35}); place != want {
		t.Fatalf("%q lands at %+v, want %+v", word, place, want)
	}

	// The word it draws is the word: every glyph of FINISHED is in the font, and the pixels
	// that end up lit are the glyphs' own pixels multiplied by the square of the scale.
	small, err := Raster(word, 1, livePanel)
	if err != nil {
		t.Fatalf("rasterise %q at a scale of 1: %v", word, err)
	}

	large, err := Raster(word, scale, livePanel)
	if err != nil {
		t.Fatalf("rasterise %q at a scale of %d: %v", word, scale, err)
	}

	if want := len(small) * scale * scale; len(large) != want {
		t.Fatalf("%q at a scale of %d lights %d pixels, want %d: one glyph pixel is a %dx%d block",
			word, scale, len(large), want, scale, scale)
	}

	// And the word is inside the panel it was placed on, with a margin on every side of it:
	// a pixel at or past the panel's width would have wrapped onto the next row and drawn
	// part of a letter in the wrong place.
	for _, index := range large {
		if index < 0 || index >= livePanel.Width*livePanel.Height {
			t.Fatalf("a lit pixel of %q is at %d, outside a %dx%d panel",
				word, index, livePanel.Width, livePanel.Height)
		}
	}
}

// TestAWordThatWouldNotFitIsRefused is the refusal, at each of the four ways a caller can ask
// for something that cannot be drawn.
//
// Refused rather than clipped, shrunk or drawn off the edge: a word that does not fit is a
// word whose scale or panel the caller has wrong, and a module that picked its own answer
// would be putting a picture on the light that nobody chose.
func TestAWordThatWouldNotFitIsRefused(t *testing.T) {
	for _, shape := range []struct {
		name  string
		word  string
		scale int
		panel Geometry
		want  error
	}{
		{
			// 47 columns at a scale of 6 is 282 pixels across, and the panel is 256.
			name:  "too wide at too big a scale",
			word:  "FINISHED",
			scale: 6,
			panel: livePanel,
			want:  ErrNoFit,
		},
		{
			// 7 rows at a scale of 5 is 35 pixels down, and this panel has 32.
			name:  "too tall for a short panel",
			word:  "FINISHED",
			scale: 5,
			panel: Geometry{Width: 256, Height: 32},
			want:  ErrNoFit,
		},
		{
			// One column too many, which is the edge worth having a test for: 235 pixels on a
			// 234-wide panel is a word refused by one column and not by a margin.
			name:  "a word one column wider than the panel",
			word:  "FINISHED",
			scale: 5,
			panel: Geometry{Width: 234, Height: 64},
			want:  ErrNoFit,
		},
		{name: "nothing to draw", word: "", scale: 5, panel: livePanel, want: ErrNoWord},
		{name: "a character the font has not got", word: "DONE!", scale: 1, panel: livePanel, want: ErrUnknownGlyph},
		{name: "a lower-case word", word: "done", scale: 1, panel: livePanel, want: ErrUnknownGlyph},
		{name: "no pixels per glyph pixel", word: "FINISHED", scale: 0, panel: livePanel, want: ErrScale},
		{name: "a negative scale", word: "FINISHED", scale: -3, panel: livePanel, want: ErrScale},
		{name: "a strip, which has no rows", word: "FINISHED", scale: 1, panel: Geometry{Width: 48}, want: ErrNoPanel},
	} {
		t.Run(shape.name, func(t *testing.T) {
			if _, err := Place(shape.word, shape.scale, shape.panel); !errors.Is(err, shape.want) {
				t.Fatalf("placing %q at a scale of %d on %dx%d gave %v, want %v",
					shape.word, shape.scale, shape.panel.Width, shape.panel.Height, err, shape.want)
			}

			if lit, err := Raster(shape.word, shape.scale, shape.panel); err == nil || lit != nil {
				t.Fatalf("rasterising %q at a scale of %d on %dx%d gave %d pixels and %v, want no pixels and %v",
					shape.word, shape.scale, shape.panel.Width, shape.panel.Height, len(lit), err, shape.want)
			}
		})
	}
}

// TestTheRefusalNamesTheNumbers keeps the error readable by the person who has to act on it:
// an operator told only "does not fit" has to guess whether to change the word or the panel.
func TestTheRefusalNamesTheNumbers(t *testing.T) {
	_, err := Place("FINISHED", 6, livePanel)
	if err == nil {
		t.Fatal("a word 282 pixels wide was placed on a 256-pixel panel")
	}

	for _, wanted := range []string{"282x42", "256x64", "FINISHED"} {
		if !strings.Contains(err.Error(), wanted) {
			t.Fatalf("the refusal reads %q, want it to name %s", err, wanted)
		}
	}
}
