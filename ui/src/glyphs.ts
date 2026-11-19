// The module's own font, in the browser, so a preview is the picture the panel draws.
//
// docs/product-requirements.md §2.4 asks the Colours screen to draw every state twice, on a
// panel and on a strip, and to warn while a word is typed when a character cannot be drawn.
// Neither is possible from the routes: GET /frames answers the frame the module has *decided*
// for a binding, and the preview routes paint a real light, which is the one thing an editor
// must not do. So the screen draws the same glyphs the module draws, from this copy.
//
// **This is a copy, and it is checked.** The authority is internal/text/font.go: the same
// forty-three glyphs, seven bytes each, leftmost pixel the high bit. `glyphs_test`-style drift
// is a real risk with a table written twice, so this file is compared against font.go by the
// check that runs beside the build, and a disagreement is a failure rather than a surprise on
// the wall. The table is also the answer to "can this character be drawn", which is what the
// warning says: a character outside it is refused by the rasteriser, not drawn as a gap.
//
// What is not here: no lower case (a word is upper-cased when it is drawn, exactly as the
// module does), no accented Latin, no CJK. §2.4 names the limit as the font rather than the
// text, and says the interface warns rather than refuses.

/** How many pixel columns one glyph occupies, from internal/text.GlyphColumns. */
export const GLYPH_COLUMNS = 5;

/** How many pixel rows one glyph occupies, from internal/text.GlyphRows. */
export const GLYPH_ROWS = 7;

/** The blank column between two glyphs, from internal/text.GlyphGap. */
export const GLYPH_GAP = 1;

/**
 * The scale the module draws a word at, from event.go's finishedWordScale.
 *
 * It is a constant and not a best fit: `text.Place` refuses a word too wide or too tall rather
 * than shrinking it, and a light that cannot carry the word shows the colour instead. A preview
 * that fitted the word to the space would be a picture of something the module will not do.
 */
export const WORD_SCALE = 5;

/** The panel the module was measured against: morpheus, 256 × 64. */
export const PANEL = { width: 256, height: 64 } as const;

/** The strip's length: CyberDesk, one run of 48 pixels. */
export const STRIP = 48;

/**
 * The glyphs, one array of seven row bytes per character, top row first. A one means the pixel
 * is lit. Copied row for row from internal/text/font.go.
 */
export const GLYPHS: Readonly<Record<string, readonly number[]>> = {
	A: [0x0e, 0x11, 0x11, 0x1f, 0x11, 0x11, 0x11],
	B: [0x1e, 0x11, 0x11, 0x1e, 0x11, 0x11, 0x1e],
	C: [0x0e, 0x11, 0x10, 0x10, 0x10, 0x11, 0x0e],
	D: [0x1e, 0x11, 0x11, 0x11, 0x11, 0x11, 0x1e],
	E: [0x1f, 0x10, 0x10, 0x1e, 0x10, 0x10, 0x1f],
	F: [0x1f, 0x10, 0x10, 0x1e, 0x10, 0x10, 0x10],
	G: [0x0e, 0x11, 0x10, 0x17, 0x11, 0x11, 0x0e],
	H: [0x11, 0x11, 0x11, 0x1f, 0x11, 0x11, 0x11],
	I: [0x1f, 0x04, 0x04, 0x04, 0x04, 0x04, 0x1f],
	J: [0x07, 0x02, 0x02, 0x02, 0x02, 0x12, 0x0c],
	K: [0x11, 0x12, 0x14, 0x18, 0x14, 0x12, 0x11],
	L: [0x10, 0x10, 0x10, 0x10, 0x10, 0x10, 0x1f],
	M: [0x11, 0x1b, 0x15, 0x11, 0x11, 0x11, 0x11],
	N: [0x11, 0x19, 0x15, 0x13, 0x11, 0x11, 0x11],
	O: [0x0e, 0x11, 0x11, 0x11, 0x11, 0x11, 0x0e],
	P: [0x1e, 0x11, 0x11, 0x1e, 0x10, 0x10, 0x10],
	Q: [0x0e, 0x11, 0x11, 0x11, 0x15, 0x12, 0x0d],
	R: [0x1e, 0x11, 0x11, 0x1e, 0x14, 0x12, 0x11],
	S: [0x0f, 0x10, 0x10, 0x0e, 0x01, 0x01, 0x1e],
	T: [0x1f, 0x04, 0x04, 0x04, 0x04, 0x04, 0x04],
	U: [0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x0e],
	V: [0x11, 0x11, 0x11, 0x11, 0x11, 0x0a, 0x04],
	W: [0x11, 0x11, 0x11, 0x11, 0x15, 0x1b, 0x11],
	X: [0x11, 0x11, 0x0a, 0x04, 0x0a, 0x11, 0x11],
	Y: [0x11, 0x11, 0x0a, 0x04, 0x04, 0x04, 0x04],
	Z: [0x1f, 0x01, 0x02, 0x04, 0x08, 0x10, 0x1f],
	'0': [0x0e, 0x11, 0x13, 0x15, 0x19, 0x11, 0x0e],
	'1': [0x04, 0x0c, 0x04, 0x04, 0x04, 0x04, 0x0e],
	'2': [0x0e, 0x11, 0x01, 0x02, 0x04, 0x08, 0x1f],
	'3': [0x1f, 0x02, 0x04, 0x02, 0x01, 0x11, 0x0e],
	'4': [0x02, 0x06, 0x0a, 0x12, 0x1f, 0x02, 0x02],
	'5': [0x1f, 0x10, 0x1e, 0x01, 0x01, 0x11, 0x0e],
	'6': [0x06, 0x08, 0x10, 0x1e, 0x11, 0x11, 0x0e],
	'7': [0x1f, 0x01, 0x02, 0x04, 0x08, 0x08, 0x08],
	'8': [0x0e, 0x11, 0x11, 0x0e, 0x11, 0x11, 0x0e],
	'9': [0x0e, 0x11, 0x11, 0x0f, 0x01, 0x02, 0x0c],
	' ': [0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00],
	'.': [0x00, 0x00, 0x00, 0x00, 0x00, 0x0c, 0x0c],
	'-': [0x00, 0x00, 0x00, 0x0e, 0x00, 0x00, 0x00],
	'%': [0x19, 0x19, 0x02, 0x04, 0x08, 0x13, 0x13],
	':': [0x00, 0x0c, 0x0c, 0x00, 0x00, 0x0c, 0x0c],
	'/': [0x01, 0x01, 0x02, 0x04, 0x08, 0x10, 0x10],
	_: [0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x1f],
};

/** A word as it is drawn: the module upper-cases what it is given. */
const drawn = (word: string): string => word.trim().toUpperCase();

/**
 * The characters in a word this font cannot draw, once each, in the order they appear.
 *
 * Empty means the word is drawable at some size. This is what §2.4's live warning says out
 * loud: `text.Raster` refuses the whole word rather than leaving a gap, so a word with one of
 * these on it does not reach the panel at all — the state's colour does.
 */
export const missingGlyphs = (word: string): string[] => {
	const missing = new Set<string>();

	for (const character of drawn(word)) {
		if (GLYPHS[character] === undefined) missing.add(character);
	}

	return [...missing];
};

/** How wide a word is at a scale, gaps included: (5n + (n - 1)) × scale. */
export const wordWidth = (word: string, scale: number = WORD_SCALE): number =>
	(GLYPH_COLUMNS * drawn(word).length + GLYPH_GAP * Math.max(0, drawn(word).length - 1)) * scale;

/** How tall a word is at a scale, which is the glyph height and nothing else. */
export const wordHeight = (scale: number = WORD_SCALE): number => GLYPH_ROWS * scale;

/**
 * Whether the module would draw this word on a panel of this size, at its own scale.
 *
 * The module refuses rather than shrinks (internal/text.ErrNoFit), and a light it refuses on
 * shows the state's colour — so this is a fact about the light, not a limit of the preview.
 */
export const fitsPanel = (word: string, panel: { width: number; height: number } = PANEL): boolean =>
	drawn(word) !== '' && wordWidth(word) <= panel.width && wordHeight() <= panel.height;

/**
 * Where the word lands: centred, with the slack's floor on each side, exactly as
 * `text.Place` centres it.
 */
export const placeOf = (
	word: string,
	scale: number,
	panel: { width: number; height: number },
): { left: number; top: number; width: number; height: number } => {
	const width = wordWidth(word, scale);
	const height = wordHeight(scale);

	return {
		left: Math.floor((panel.width - width) / 2),
		top: Math.floor((panel.height - height) / 2),
		width,
		height,
	};
};

/**
 * Draws one word on a panel, in one colour, on the black an unlit pixel is.
 *
 * The shapes are the module's; the drawing is the browser's. A caller that has not checked
 * `missingGlyphs` and `fitsPanel` first is drawing something the panel would refuse, which is
 * why both are asked here again rather than trusted.
 */
export const drawWord = (
	context: CanvasRenderingContext2D,
	word: string,
	scale: number,
	colour: string,
	panel: { width: number; height: number } = PANEL,
): boolean => {
	if (missingGlyphs(word).length > 0 || !fitsPanel(word, panel)) return false;

	const where = placeOf(word, scale, panel);
	const text = drawn(word);

	context.fillStyle = colour;

	for (const [index, character] of [...text].entries()) {
		const glyph = GLYPHS[character];
		if (glyph === undefined) continue;

		for (let row = 0; row < GLYPH_ROWS; row += 1) {
			const bits = glyph[row] ?? 0;

			for (let column = 0; column < GLYPH_COLUMNS; column += 1) {
				// The leftmost pixel is the high bit of the five that are used: bit 4, value 16.
				if ((bits & (0x10 >> column)) === 0) continue;

				context.fillRect(
					where.left + (index * (GLYPH_COLUMNS + GLYPH_GAP) + column) * scale,
					where.top + row * scale,
					scale,
					scale,
				);
			}
		}
	}

	return true;
};
