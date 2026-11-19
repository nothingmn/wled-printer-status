## Diagnostics

The tab that has to answer *"why is the light doing that"* — which is the question this whole project
kept running into. It teaches by letting you switch between three real situations rather than showing one
static wall of numbers.

### The three situations the page switches between

1. **printing, all well** — a live job at 41%, reading two seconds old, the last write landed in 28 ms,
   reason `progress`, transported over DDP.
2. **finished, then quiet** — reading 242 seconds old and stale, and the picture **holds its green**,
   reason `not-a-live-job`. The log has one line for the staleness and nothing after it, however long the
   silence lasts.
3. **printing, light not answering** — and this is the useful one: the **frame is still right**
   (`progress`, green, 6717 pixels) and the **wire is not** — the last write failed with *no route to
   host*, retries widening 1 → 2 → 4 → 8 → 16 seconds. It separates *what it decided* from *what landed*,
   which is the distinction an operator needs at 3am.

### The merged timeline — printer, module, light and settings in one feed

The history is **one timeline, mirrored from the host's log** (not a second scrollback kept in memory —
two histories that disagree are worse than one), and every entry is tagged with **who caused it**:

- **printer** — a state change, a connection gained or lost, staleness entered or left.
- **module** — a frame written, a word fired and taken back, a backoff doubling, a brightness resolved.
- **light** — something changed on the controller that the module did not write. *"brightness changed
  128 → 40 — not by this module"* is the line that turns "why did my light go dark at 2am" into an
  answer.
- **settings** — a window resolved, a binding added, a segment borrowed and given back.

Filter chips narrow the feed to one source. The honest limit is stated on the page: **WLED has no event
stream**, so light-side changes are found by reading the controller and comparing, which means we can say
*what* changed and *whether we did it* — not *why* someone else did, and up to one poll late.

- **Four tiles**: the printer and its reading age (with stale or live), the last write and whether it
  landed, **why this picture** and the reason's plain meaning, and the transport in use.
- **The frame itself**, drawn from the colour, the fill count and the reason the module reports.
- **Every reason the module can give**, in a table, with the current one marked — so "why is it green"
  has an answer rather than a guess.
- **The history**, with the rule stated: progress is silent, a state change or a connection gained or lost
  is one line, and every failure is one line per widening retry.
- **The honest limits** — a word on a strip, a printer that goes quiet mid-job, a finished job that goes
  quiet, and the fact that a controller's pixels cannot be read back on this firmware.

### Two things that need building

- **A diagnostic bundle** — status, the last log lines and the settings, with the signing key redacted.
  `needs API`; it would be one route and it saves asking an operator to copy three things by hand.
- **The history served by the module** rather than only by the host's log. `needs API`. Today the log
  lives where the host writes it.

### What is real today

Everything except the two marked items: `/status` carries the colour, the fill count, the percent, the
name and the reason; the last-write result is real; the log lines are the ones the module actually writes.

### Open question for you

**Should the history page be able to show more than the last N lines** — a scrollback the module keeps in
memory, say the last two hundred — or is the host's log the right place and this tab should only ever
mirror the tail? My view: mirror the tail, because two histories that disagree are worse than one.
