// Package text draws a short word in pixels on a matrix panel.
//
// It is the second half of the question the event layer asks: the event knows *what* to
// say, and this knows which pixels say it. A word is set in the five-by-seven font in
// font.go, one glyph at a time, scaled by whole pixels, and placed across a panel's width
// and down its height so that the word is centred in both directions.
//
// # Why a font in this repository
//
// WLED has a text effect, and on the firmware this module was pointed at it does not honour
// the segment it is given: four groupings were tried on a 256x64 panel and every one lit only
// the first few columns on the left. Rather than tune a firmware parameter nobody here can
// read, the module draws the word itself: a glyph table it owns, a rasteriser it owns, and a
// per-pixel write. So the package holds data and arithmetic, and nothing else.
//
// # What it does not do
//
// No socket, no clock, no colour, no transport. It answers with pixel indices — ascending,
// row-major, which is the order a WLED panel addresses its pixels in — and leaves it to the
// caller which colour a lit pixel is drawn in.
//
// There is no kerning, no descender, no lower case and no wrapping. A word is one line, and a
// word that does not fit is refused rather than clipped: half a word on a panel is a message
// that reads as something other than what was meant.
package text

import (
	"errors"
	"fmt"
)

// The failures a caller can tell apart.
var (
	// ErrNoFit means the word, at the scale it was asked for, is wider or taller than the
	// panel. It is refused rather than clipped or shrunk: a caller that wanted a smaller word
	// asks for one, and a module that silently picked its own scale would put a picture on the
	// light that nobody chose.
	ErrNoFit = errors.New("text: the word does not fit the panel")

	// ErrNoWord means there is nothing to draw. An empty string is a mistake at the caller and
	// not an empty picture on the light.
	ErrNoWord = errors.New("text: there is no word to draw")

	// ErrUnknownGlyph means the word carries a character this font does not hold. Refused
	// rather than drawn blank, because a missing letter changes what the word says.
	ErrUnknownGlyph = errors.New("text: the font holds no glyph for that character")

	// ErrScale means the scale is not a positive whole number of pixels per glyph pixel. Zero
	// would draw nothing and a negative would draw backwards.
	ErrScale = errors.New("text: the scale must be at least one pixel per glyph pixel")

	// ErrNoPanel means the geometry is not a matrix: a panel needs both a width and a height,
	// and a controller that reports one of them is a strip.
	ErrNoPanel = errors.New("text: the geometry is not a panel")
)

// Geometry is the panel a word is drawn on: how many pixels wide it is and how many rows it
// has. The zero value is refused, because a strip has no rows to draw a word on.
type Geometry struct {
	// Width is the panel's width in pixels.
	Width int
	// Height is the panel's height in rows.
	Height int
}

// Panel reports whether this geometry is a panel at all.
func (g Geometry) Panel() bool { return g.Width > 0 && g.Height > 0 }

// Placement is where one word lands on one panel: the pixel column and row its first glyph
// pixel sits at, and the size the whole word occupies in pixels.
//
// It is a value of its own so that a caller which needs the geometry of the picture — to
// write only the rows the word touches, say — gets the same numbers the rasteriser uses. Two
// copies of the centring arithmetic would be two answers to where the word is.
type Placement struct {
	// Left is the pixel column the word starts at, counted from zero at the panel's left edge.
	Left int
	// Top is the pixel row the word starts at, counted from zero at the panel's top row.
	Top int
	// Width is how many pixel columns the whole word covers, gaps included.
	Width int
	// Height is how many pixel rows the word covers.
	Height int
}

// Place returns where a word lands on a panel, or why it cannot be drawn there.
//
// The size is the font's, scaled: a word of n glyphs is GlyphColumns*n + GlyphGap*(n-1)
// glyph columns wide, and GlyphRows tall, multiplied by the scale. FINISHED at a scale of 5
// is eight glyphs and seven gaps — 47 columns of glyph, 235 pixels — and 35 pixels tall.
//
// The word is centred: the space left over — the panel's width less the word's — is split in
// two, and the word starts at the left half of it, rounded down. Rounded down rather than up
// so that a word with an odd pixel of slack sits one pixel nearer the left edge, which is
// where a reader's eye starts; the difference is one pixel and the rule has to be one of the
// two.
//
// A word wider or taller than the panel is refused with ErrNoFit, and the error names the
// numbers: the size asked for and the size available.
func Place(word string, scale int, g Geometry) (Placement, error) {
	if !g.Panel() {
		return Placement{}, fmt.Errorf("%w: %dx%d", ErrNoPanel, g.Width, g.Height)
	}

	size, err := Size(word, scale)
	if err != nil {
		return Placement{}, err
	}

	if size.Width > g.Width || size.Height > g.Height {
		return Placement{}, fmt.Errorf("%w: %q at a scale of %d is %dx%d and the panel is %dx%d",
			ErrNoFit, word, scale, size.Width, size.Height, g.Width, g.Height)
	}

	// The floor of half the slack, and both halves of the picture: a word is centred left to
	// right and top to bottom by the same rule, so there is one sentence to read rather than a
	// horizontal rule and a vertical one that could disagree.
	return Placement{
		Left:   (g.Width - size.Width) / 2,
		Top:    (g.Height - size.Height) / 2,
		Width:  size.Width,
		Height: size.Height,
	}, nil
}

// Size returns how many pixels a word occupies at a scale, before it is placed on any panel.
//
// It is Place's arithmetic without the panel, for a caller that wants to know whether a word
// will fit before it has read a controller's geometry.
func Size(word string, scale int) (Placement, error) {
	if scale < 1 {
		return Placement{}, fmt.Errorf("%w: %d", ErrScale, scale)
	}

	if word == "" {
		return Placement{}, ErrNoWord
	}

	drawn, err := glyphsFor(word)
	if err != nil {
		return Placement{}, err
	}

	columns := GlyphColumns*len(drawn) + GlyphGap*(len(drawn)-1)

	return Placement{Width: columns * scale, Height: GlyphRows * scale}, nil
}

// Raster returns the lit pixels of one word on one panel, as indices into a row-major buffer
// of the panel's pixels — the pixel at column x of row y is y*Geometry.Width + x, which is the
// order a WLED panel addresses them in.
//
// The indices ascend, so a caller can walk them beside a buffer in the same order without
// looking each one up. Only lit pixels are returned: the gaps between glyphs, the space inside
// a letter and the margin around the word are not in the list, and what colour they are drawn
// in is the caller's decision.
//
// A word, a scale or a panel this package cannot draw is refused with the error that names
// why, and nothing is returned. There is no partial answer: a caller that got half a word
// would have no way to tell it from a word that is half lit.
func Raster(word string, scale int, g Geometry) ([]int, error) {
	place, err := Place(word, scale, g)
	if err != nil {
		return nil, err
	}

	drawn, err := glyphsFor(word)
	if err != nil {
		return nil, err
	}

	// A bitmap first and the indices after, because a glyph's pixels are walked glyph by glyph
	// and a lit pixel is written once per scaled copy of it — so the walk arrives out of order
	// and the answer has to be sorted by construction rather than by a sort afterwards.
	lit := make([]bool, g.Width*g.Height)

	advance := (GlyphColumns + GlyphGap) * scale

	for index, glyph := range drawn {
		left := place.Left + index*advance

		for row := range GlyphRows {
			bits := glyph[row]
			if bits == 0 {
				continue
			}

			for column := range GlyphColumns {
				// The leftmost pixel of a glyph is its high bit; see font.go.
				if bits&(1<<(GlyphColumns-1-column)) == 0 {
					continue
				}

				for dy := range scale {
					y := place.Top + row*scale + dy

					for dx := range scale {
						x := left + column*scale + dx
						lit[y*g.Width+x] = true
					}
				}
			}
		}
	}

	indices := make([]int, 0, len(lit))

	for index, on := range lit {
		if on {
			indices = append(indices, index)
		}
	}

	return indices, nil
}

// glyphsFor is the word as glyph rows, or the first character the font cannot draw.
//
// It refuses before anything is rasterised, so a word with one bad character costs no
// arithmetic and leaves no half-built picture behind.
func glyphsFor(word string) ([][GlyphRows]byte, error) {
	if word == "" {
		return nil, ErrNoWord
	}

	drawn := make([][GlyphRows]byte, 0, len(word))

	for _, character := range word {
		glyph, ok := glyphs[character]
		if !ok {
			return nil, fmt.Errorf("%w: %q in %q", ErrUnknownGlyph, character, word)
		}

		drawn = append(drawn, glyph)
	}

	return drawn, nil
}
