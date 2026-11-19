## Colours

Ten states, each shown as it would actually appear — a small panel preview and a strip preview side by
side — with its colour, and optionally **a word**.

### The idea this page settles

The idle question had three candidate answers: leave it as the plain colour, make it a new *event*, or
have the light hold whatever the last event left.

**None of them is a separate mechanism.** A state is a condition, not a transition — it lasts until
something else happens — so the honest place for "what an idle light shows" is here, next to its
colour:

- `finished` holds **FINISHED** until the next job starts.
- `idle` can say **IDLE**.
- `error` can say **ERROR** until it is cleared.

That also gives you "keep the last event" for free, because the finished state holding its word *is*
that behaviour — with no second system to keep in step.

### The answers this page was built to

1. **A timeout, defaulting to infinity.** Each state's word carries a `holds` value — `forever` by
   default, or 30 s / 60 s / 5 min.
2. **All ten states get a word**, not just the obvious three.
3. **`printing` gets one too** — and it replaces the composer while it is up: a word on `printing`
   means the bar and digits are not what that light shows. That is a real conflict with Layout, and
   the state wins because a state is a condition while the composer is a picture of a running job.

**And a fourth thing fell out of the third answer.** Editable words are the i18n path — *"that way it
can, at least be changed to different languages"* — but the limit is the font, not the text:

- **Any A–Z and digits word works today** (43 glyphs: letters, digits, `% . - _ /` and space), so
  GERMAN, TERMINE, PARADO all draw now.
- **Accented Latin** (É, Ü, Å, Ø) is about thirty more 5×7 glyphs — small work.
- **Greek and Cyrillic** are a second alphabet, feasible but not a tweak.
- **Chinese, Japanese, Korean** cannot be drawn at 5×7 at all: a legible glyph needs 12×12 and up.

The page proves it rather than claiming it — type `TERMINÉ` and the preview leaves a gap and says
*"not drawable by this font: É — the panel would leave a gap there"*.

### What the page shows

- **A colour per state**, approved on 2026-11-18, with a picker that repaints the previews live.
- **A word per state**, off by default, with the word editable and marked *panel only* — because a
  strip is one dimension and cannot carry text.
- **Both renderings for every state**, so a strip owner can see what they get: the state's colour, and
  for `printing` the fill rather than a full strip.
- **The fill behaviour**, which is the same on both shapes: growing or shrinking fill, rounding, what
  0% and 100% do, and what an unfilled pixel is coloured.

### What is real and what is not

- The **colours** are the operator's approved table and already ship in the module.
- **Editing them** needs a route: a settings field cannot hold a table (AGENTS §5.8), so this is
  `needs API` even though the values are live.
- **A word per state** is new — `needs build`. Today only `FINISHED` has a word and only for sixty
  seconds, as an event.
- **The fill behaviour** is coded as constants today, two of them marked provisional.

### Open questions for you

1. **Should a state's word hold forever, or time out?** Finished holding FINISHED until the next job is
   useful on a wall; holding it for three days is noise.
2. **Which states deserve a word?** The mockup offers it on all ten; realistically `finished`, `idle`
   and `error` are the three that say something a colour cannot.
3. **Does `printing` ever want a word** — the job name, say — or is that the Layout tab's business?
