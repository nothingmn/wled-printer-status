package text

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// uiGlyphs is where the browser's copy of this table lives: the Colours screen draws a state twice,
// on a panel and on a strip, from the module's own glyphs rather than from a screenshot, and it
// cannot read this package. ui/src/glyphs.ts says so itself.
const uiGlyphs = "../../ui/src/glyphs.ts"

// TestTheBrowsersGlyphsAreTheModulesGlyphs holds the copy to the authority.
//
// The table is written twice — once here, once in the bundle — and a table written twice drifts:
// one glyph edited in one place is a preview that draws a letter the light will not, and nothing on
// any screen would say which of the two is wrong. So the copy is compared glyph for glyph, and the
// count is compared too, because a missing glyph is a character the browser draws blank while the
// module refuses the word.
//
// A Go-only checkout has no ui/ directory. That is not a failure of the table: the test says the
// copy is not there to compare and skips, which is a different fact from a disagreement.
func TestTheBrowsersGlyphsAreTheModulesGlyphs(t *testing.T) {
	source, err := os.ReadFile(uiGlyphs)
	if err != nil {
		t.Skipf("the browser's copy is not in this checkout (%v), so there is nothing to compare", err)
	}

	drawn, err := parseUIGlyphs(string(source))
	if err != nil {
		t.Fatalf("read %s: %v", uiGlyphs, err)
	}

	if len(drawn) != len(glyphs) {
		t.Fatalf("the browser's table holds %d glyphs and this package holds %d: %s", len(drawn), len(glyphs), missing(drawn))
	}

	for character, rows := range glyphs {
		copied, ok := drawn[string(character)]
		if !ok {
			t.Fatalf("this package draws %q and the browser's table does not", character)
		}

		if len(copied) != GlyphRows {
			t.Fatalf("the browser's %q is %d rows, want %d", character, len(copied), GlyphRows)
		}

		for i, row := range rows {
			if copied[i] != row {
				t.Fatalf("row %d of %q is 0x%02x in this package and 0x%02x in the browser's table: the copy has drifted",
					i+1, character, row, copied[i])
			}
		}
	}

	for character := range drawn {
		if _, ok := glyphs[[]rune(character)[0]]; !ok {
			t.Fatalf("the browser's table draws %q and this package does not, so a preview would promise a letter the light refuses", character)
		}
	}
}

// entryLine is one glyph of a table written as `'A': [0x0e, ...]`, with the key in single quotes or
// bare, which is how both files write the letters and the marks.
var entryLine = regexp.MustCompile(`^\s*(?:'(.+?)'|([A-Za-z_]))\s*:\s*\[([^\]]*)\]\s*,?\s*$`)

// parseUIGlyphs reads the browser's table out of the source text: the lines between `export const
// GLYPHS` and its closing brace.
func parseUIGlyphs(source string) (map[string][]byte, error) {
	drawn := make(map[string][]byte, 43)
	inside := false

	for _, line := range strings.Split(source, "\n") {
		if strings.Contains(line, "export const GLYPHS") {
			inside = true

			continue
		}

		if !inside {
			continue
		}

		if strings.HasPrefix(strings.TrimSpace(line), "};") {
			break
		}

		found := entryLine.FindStringSubmatch(line)
		if found == nil {
			continue
		}

		key := found[1]
		if key == "" {
			key = found[2]
		}

		rows := make([]byte, 0, GlyphRows)

		for _, part := range strings.Split(found[3], ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}

			value, err := strconv.ParseUint(strings.TrimPrefix(part, "0x"), 16, 8)
			if err != nil {
				return nil, err
			}

			rows = append(rows, byte(value))
		}

		drawn[key] = rows
	}

	return drawn, nil
}

// missing is the glyphs this package holds and the browser's table does not, for a count that
// disagrees with itself. A count alone sends a person looking through fifty lines.
func missing(drawn map[string][]byte) string {
	absent := make([]string, 0, len(glyphs))

	for character := range glyphs {
		if _, ok := drawn[string(character)]; !ok {
			absent = append(absent, strconv.QuoteRune(character))
		}
	}

	if len(absent) == 0 {
		return "the counts differ and every glyph of this package is there, so the browser's table holds one this package does not"
	}

	return "missing from the browser's table: " + strings.Join(absent, ", ")
}
