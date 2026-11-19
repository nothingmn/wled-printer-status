# wled-printer-status — v2 product requirements

**Status: LOCKED by the operator, 2026-11-19**, after an eight-screen design review. Every screen below
was built as a working mockup in [`design/v2/`](design/v2/README.md) and approved by the operator by name.
This document is the specification of record; where it disagrees with the repository's earlier draft
requirements, this wins.

---

## 1. What this is

A Prusa Connect **service module** that reads one or more 3D printers through the local Connect instance
and drives WLED lights — a colour for state, a fill for progress, text and words on a matrix panel, and
one of the light's own presets for a moment. It is a light somebody walks past, not a screen somebody
opens. It publishes to the Prusa Plugin Marketplace as an early beta.

**Vocabulary, fixed with the operator:**

- **Panel** — a 2D matrix: rows × columns of pixels (`morpheus`, 256 × 64 = 16,384). Words and bands fit.
- **Strip** — 1D, one run of pixels (`CyberDesk`, 48). A fill fits; a word cannot.
- **Binding** — one printer on one light, on one segment of it.
- **Moment** — a transition (print finished, paused, error…). Has a duration.
- **State** — a condition (printing, idle, error…). Lasts until something else happens.
- **Snapshot** — a stored copy of a light's whole configuration.

The module decides panel vs strip from the controller's own `/json/matrix` field: present means panel,
absent means strip.

---

## 2. The eight screens

Each screen's mockup lives in its own folder under [`design/v2/`](design/v2/README.md), and each folder's
`README.md` records that screen's design decisions and the operator's own words where he gave them.

| Screen | Mockup |
| --- | --- |
| 2.1 Right now (Overview) | [`design/v2/overview/`](design/v2/overview/) |
| 2.2 Bindings | [`design/v2/bindings/`](design/v2/bindings/) |
| 2.3 Events | [`design/v2/events/`](design/v2/events/) |
| 2.4 Colours | [`design/v2/colours/`](design/v2/colours/) |
| 2.5 Layout | [`design/v2/layout/`](design/v2/layout/) |
| 2.6 Devices | [`design/v2/devices/`](design/v2/devices/) |
| 2.7 Diagnostics | [`design/v2/diagnostics/`](design/v2/diagnostics/) |
| 2.8 Settings | [`design/v2/settings/`](design/v2/settings/) |

### 2.1 Right now (Overview) — LOCKED

Answers one question: **what is on my walls at this moment.** Not a dashboard — no counters, no charts.

- **One sentence**: "2 lights showing 2 printers · nothing needs you", or, when something is wrong, the
  light's name and how long: "CyberDesk has not answered for 3 minutes".
- **One row per light**: a thumbnail of the picture it is drawing, which printer it shows, and when we
  last wrote. Thumbnails are **reconstructions** from the module's own frame (colour, fill, percent, name,
  reason) drawn with the module's 5×7 glyphs — not screenshots; this firmware cannot read its pixels back.
- **Quick bind**: two columns — every printer Connect knows, and every auto-discovered light. Pick a
  printer, press `bind` on a light, and the binding exists with stated defaults. `bind them all` pairs
  every remaining light with the picked printer.
- **Pause**, both scopes, on this page: `Pause everything` in the header and `Pause` per row.
- **First run** shows one sentence saying what to do first, not an empty grid.

**Defaults a one-click bind applies, stated on the page:** whole light; brightness follows the day
schedule if there is one else 128; colours from the table untouched; a panel gets bar + percentage + name,
a strip gets the fill; only the finished moment is on.

### 2.2 Bindings — LOCKED

Grouped **by light**, because a segment carries one printer and a flat list hides that.

- A row is printer · segment · brightness · when it last wrote · an enable switch · test / remove.
- Segments come **from the light**, never typed. A taken segment is greyed out and labelled with the
  printer that has it. A free-count per light; the add button switches off at zero.
- **Discovered lights** get their own list, each with an add button that brings the light in and opens the
  dialog pointed at it. **Printers with no light** get their own list, each with a button that opens the
  dialog with that printer chosen and a light that still has a free segment preselected.
- A whole-light binding covers every segment, so it is refused alongside a zone binding.
- **A printer may sit on several lights** — multi-room status — at the cost of a second steady stream.

### 2.3 Events — LOCKED

- The page is scoped to **one light** by the selector at the top.
- The **moment is the top row**. When that light holds more than one printer, each moment gains
  **indented per-printer sub-rows** carrying that printer's segment and its own enabled / shows / which /
  for. One printer collapses to exactly the flat table.
- `shows` is `Preset`, `Word`, `Colour` or `Nothing`; `which` is the light's own presets by name, a text
  box for a word, or the colour's name.
- **No preset gallery, no preset editor, no effect/speed/palette controls anywhere in our UI.** Presets are
  created and tuned in WLED's own interface; we only name one.
- A preset **borrows the whole light**: capture, run for the moment, put back. Refused on a borrowed zone.
  A word is **panel only**; a strip shows the moment's colour instead.
- **Settings key on the (light, printer) pair**, which is also how multi-room works without extra
  machinery.

### 2.4 Colours — LOCKED

- **Ten states**, each drawn twice (panel and strip), each with a colour and an **editable word**. The
  colours are [Appendix A](#appendix-a--state-to-display-mapping).
- A word's `holds` **defaults to forever**, with 30 s / 60 s / 5 min available.
- **A state carrying a word beats the Layout composer.** A light showing `PRINTING` shows that word, not
  the bar and digits.
- Words are free text — that is also the i18n path. The limit is the **font, best effort**: 43 glyphs
  today (A–Z, 0–9, `% . - _ /`, space); accented Latin is later work; CJK is out of reach at 5×7. The UI
  warns live when a character cannot be drawn.
- Fill behaviour is editable: fill or emptying, rounding, what 0% and 100% do, the colour of what is not
  lit. A job that stops reporting holds its colour if finished or stopped, and dims to "can't reach" if
  live — that last rule is built and proved on hardware.

### 2.5 Layout — LOCKED, two renderings

- **Panel**: bar in a band (top or bottom, 1–48 rows), percentage at scale 5/4/3, job name, rotation of
  `take-turns` or **`side-by-side`** (both, configurable; side-by-side needs a build).
- **Strip**: deliberately almost empty — fills from the left or the right, what 0% and 100% do, brightness
  only when the whole strip is taken. No bar band, no words, no layers: that is the dimension, not a
  missing feature.
- Only a **job in progress** composes. Idle, finished, stopped and error keep the plain colour, unless a
  state word applies (2.4).
- The fit arithmetic is shown: 64 rows, a 12-row bar leaves 52; scale-5 digits are 25×35. A layout that
  will not fit is **refused, not cropped**.

### 2.6 Devices — LOCKED

One card per light, and nothing about printers on it. Hardware facts, **its own segments** (the list
Bindings picks from), **its own presets** (read-only), snapshot controls, the four proved tests
(`solid`, `border`, `quarters`, `the word`) and `identify`.

- **`identify` turns the light on and off — one second each state, five times.**
- **No editing of segment names or ranges** — segments belong to WLED, same as presets.
- **Removing a light that carries a printer offers remove-and-unbind**, naming every printer that loses
  its light, and stating that the light itself is untouched.
- A light that is not answering has its test buttons **disabled**, rather than offering a button that
  always fails.

#### Replacing a light (hardware died)

- **Hardware must match: same shape and same pixel count, or no swap.** Mismatched candidates are listed
  disabled with the reason.
- **Segments match by name only.** A missing segment **pauses the swap**; `copy the name` puts it on the
  clipboard for pasting into WLED, and the page **polls the new light every 23 seconds**, ticking names
  off until all are present. Swap stays disabled until then.
- **Never** restore the `nw` section, `rev` or `vid`. Restore `light`, `def`, `if`, `timers`, `um`, `ap`,
  `presets`, `id`.
- **Physical mounting is out of scope** — the module cannot know where the new hardware hangs.

#### Snapshots

- Each light is snapshotted every **5 minutes** and on demand: `GET /json/cfg` plus `GET /presets.json`.
  Measured: 3.2 KB + 10.6 KB on the panel, 2.6 KB + 12.2 KB on the strip. **In memory**, not on disk, so
  no file of network names is written to the host.
- **`download a backup`** writes a JSON file for keeping outside the module.
- The cache exists **because a dead light cannot be read** — not for speed.
- **WLED withholds the WiFi password** (`ssid` present, `psk` absent, on both lights measured), so a
  literal 100% clone including credentials is impossible. The page says so.

### 2.7 Diagnostics — LOCKED

- **Three switchable situations**: printing, finished-then-quiet, and printing-but-the-light-is-not-
  answering. The last separates *what it decided* from *what landed*.
- **Four tiles**: printer + reading age (live/stale), last write and whether it landed, why-this-picture
  with the reason's plain meaning, transport.
- **The frame itself**, and **every reason the module can give** with the current one marked.
- **One timeline, mirrored from the host's log** — no second scrollback. It carries **only what this
  module did**: `printer` (what it reported that made us act) and `module` (frames, words, brightness,
  failures and their widening retries). Changes made elsewhere on the controller are **not** followed.
- **The honest limits**, stated on the page.

### 2.8 Settings — LOCKED

- **What it is**: version, kind, memory budget (32 MiB, 16 measured — the Go runtime takes 85% as a soft
  limit), permissions asked, **egress declared** (HTTP 80, UDP 4048), platforms, lifecycle.
- **Yours to set**: the **timezone** the schedule is judged in, validated with the module's own refusal.
- **One thing before saving**: a settings save restarts the module.
- **The log level and the host-command card were both removed on review** — a paragraph the user cannot
  act on does not belong on the page.

---

## 3. Cross-cutting decisions

| # | Decision |
|---|---|
| 1 | **Delete the manifest-settings text areas as an editing surface.** Our UI is the editor. See §4 for the consequence. |
| 2 | **First run says one sentence** about what to do first. |
| 3 | **Responsive is required.** It is used from a phone; the tab rail becomes a top strip on narrow screens. |
| 4 | Colour-blind accommodation is **not** a requirement. (The editable state words happen to help; that is not why they exist.) |
| 5 | **No UI localisation.** The light's words are translatable by the user; the interface stays English. |
| 6 | **`Pause everything` sits in the header of every tab** — it is a panic button, not a page feature. |

### Pause — the full rule

Two scopes, six durations: **10 minutes, 1 hour, 6 hours, 1 day, 3 days, forever.**

**A pause switches the light off.** Not frozen at its last state — that is a picture of something no
longer true. One write and then zero: no writes, no log lines, no retries, until it resumes. The
thumbnail shows black because black is what is on the wall.

**Exception:** a binding holding a **borrowed zone** cannot switch the light off — the rest of that strip
belongs to somebody else. It **hands the zone back** instead.

A timed pause **must survive a restart**, so it lives in the module's own store, not in host settings.
**Resume repaints immediately.**

---

## 4. Configuration ownership — the consequence of decision 1

Deleting the settings text areas means the host's settings pane is no longer the editing surface. That
forces a decision with real consequences:

- **The module owns its configuration**, edited through its routes and persisted in its own writable data
  directory (bindings, moments, state table, layout, schedule, pause state, snapshots).
- **The manifest keeps only what the host requires**: name, kind, version, platforms, permissions,
  egress, memory, lifecycle. No `options` carrying the product's data model.
- **His existing install was configured through host settings**, so the first start after this change must
  **read the host settings once as a migration source**, write them into the module's store, and never
  read them again. If the host settings are absent, defaults apply.
- **What survives what:** the store lives beside the installed module on Connect's writable config volume,
  so it survives a restart and an upgrade. A **`module purge` or a fresh install loses it** — and the
  answer to that is the `download a backup` button that already exists per light, plus a config export.
  This is a real difference from how Connect modules usually behave (host-owned settings) and must be
  stated in the README and the marketplace listing.

---

## 5. Backend work this UI is waiting on

| # | Route / capability | Why |
|---|---|---|
| 1 | `GET /controllers/{name}` returns the light's **own segments** | every segment picker is empty today |
| 2 | `GET /frames` — one frame per binding | the thumbnails on Overview |
| 3 | `GET /devices/{name}/snapshot` + `POST …/restore` | backup and restore |
| 4 | Moment table: CRUD for (light, printer) × moment | the Events screen |
| 5 | State table CRUD (colour + word + holds) | the Colours screen |
| 6 | Layout storage per binding | the Layout screen |
| 7 | Brightness windows edited by route, not JSON | the Brightness screen |
| 8 | Pause: global and per binding, persisted, timed | the panic button |
| 9 | `POST /json/cfg` write path | **unverified** — being tested on the strip |

---

## 6. What is proved, and what is not

**Proved on hardware:** the progress bar tracks a live print; a real state change is noticed and logged;
the `FINISHED` word appears on the panel (235 × 35, centred) and hands the light back after exactly 60
seconds; DDP frames are accepted and drawn, and the panel's LED map is row-major; a finished job keeps its
colour through 232 seconds of silence; the stale segment name is cleared; the composed frame (bar,
percentage, job name) draws and tracks at 41%.

**Not proved:** whether `POST /json/cfg` writes and reboots cleanly on this hardware; brightness windows
moving a live panel at a window edge; the two-printer-on-two-lights path with real hardware; anything on a
phone yet, since responsive layout is a new requirement.

---

## 7. Standing rules that outrank this document

From the repository's `AGENTS.md`: secrets never in tracked files; the panel's pixels cannot be read back,
so a picture is verified by eye; a borrowed segment is always given back; two writers never share one
segment; and no claim is made without the command and its output beside it.

---

## Appendix A — State-to-display mapping

**Locked 2026-11-18**, and carried into this document from the repository's earlier draft requirements,
which this document otherwise replaces: §2.4 decides that each state is drawn with a colour and an
editable word, and this table is which colour. The ten states the host reports, plus the two conditions
the host has no word for. Every row is closed — three or four by the operator's own words, the rest by his
assent on 2026-11-18 to the set proposed to him: *"love your set of defaults for the colors/animations.
yes to that"*. The colours below are the ones `internal/render` implements, so this table and the code are
the same statement and a disagreement between them is a defect in one of the two.

| State | What it means | Display | Where it comes from |
| --- | --- | --- | --- |
| `IDLE` | Nothing in progress; the machine is available | **Yellow** | Operator's own words: "one that is either idle, or waiting for a color change, should be yellow" |
| `BUSY` | Heating or calibrating for a print it has already taken | **Yellow** | Proposed as waiting rather than working, and approved 2026-11-18. His words give green to "printing and happy" and yellow to "idle, or waiting" — this is preparing to print while nothing is wrong, so it sits with waiting |
| `READY` | Ready, with nothing running | **Dim green** | Proposed as a quieter version of the printing green, and approved. Dim is a third of the bright channel values, computed rather than picked, so it is provably the same green |
| `PRINTING` | A job is running | **Green**, with the filled bar in the same green | Operator's own words: "A printer in a good state, its printing and happy, should map to green states" |
| `PAUSED` | A job is on the bed and paused | **Yellow, breathing, bar frozen where it stopped** | Proposed and approved. The frozen bar is built; the breathing is approved behaviour this build does not yet animate |
| `FINISHED` | A job completed | **Steady green, held until the next job** | Proposed and approved. It is the printing green rather than a hue of its own: the print succeeded |
| `STOPPED` | A job was stopped | **Grey**, or off | Proposed and approved as one of those two, with the choice left open inside the approval. This build shows a mid grey (`80,80,80`) — neither success nor failure, so neither green nor red |
| `ATTENTION` | Waiting on a person; the filament change lives here | **Yellow** | Operator's own words: "waiting for a color change, should be yellow". The mapping of "waiting for a color change" onto `ATTENTION`, which is where a filament change is reported, is a reading; the colour is his |
| `ERROR` | The printer is in error | **Red, in hues** | Operator's own words: "Red = bad" and "error codes would map to hues of red". No table of codes and hues exists yet, so every error shows one red (`200,0,0`) |
| `UNKNOWN` | The printer has reported no state | **Dim blue** | Proposed and approved. A printer that has said nothing must not look like a verdict, so neither green, red nor yellow |
| **Disconnected** | The host holds no session; the state tree is the last one it reported | **Dim red, breathing, and never the last-known progress bar** | A printer that dies keeps its last state in the host forever, and the host has no `OFFLINE` state to say so. Proposed and approved. It is the error red at a third brightness, deliberately: the reading is a memory, not an observation |
| **Stale** | Connected, but nothing has changed for 30 s | **Dim red, breathing, and never the last-known progress bar** — unless the last word is `FINISHED` or `STOPPED`, which keeps its own colour | Proposed and approved, and the same dim red as disconnected on purpose — either way there is nothing current to show. The exception is the operator's own rule taken seriously: a job that is over is a fact, so a quiet printer beside it has nothing new to say, and the reading is still reported stale with its age while the picture holds |

**What this settles, and what it does not.** Every state's colour is decided, and the two rules that
matter most are decided with it: a disconnected or stale printer never keeps its progress bar, and a
finished job holds its green until the next job rather than expiring on a timer — including through a
silence, because a cooling bed that moves a value every minute or two is not news about a job that is
over. **The breathing on `PAUSED`, disconnected and stale is approved but not built** — there is no
animation layer yet, so those three currently show a steady colour. `STOPPED`'s grey-versus-off choice,
`ERROR`'s code-to-hue table and the rounding of the fill are the three the earlier draft left open and
this document does not close; §2.4 makes the fill's rounding an editable behaviour, which is where that
answer will live.
