# AGENTS.md — wled-printer-status

Read this before you touch anything in this repository. It is the file that tells you what the code is
for, which facts about the platform are load-bearing, and which mistakes have already been paid for. It
is specific to this module; a rule that would be the same in any Go project does not belong here.

It is written for the agent who arrives knowing nothing and has to change one thing without breaking
three.

---

## 1. What this is, and why it exists

`wled-printer-status` is a **Prusa Connect service module**, published (or publishing) to the **Prusa
Plugin Marketplace**.

A service module is an out-of-process binary. The Prusa Connect server launches it as a child process,
speaks `wirerpc` to it over the child's stdio, and proxies HTTP requests into the routes the module
declares. The module is not a plugin compiled into the server and not a browser-side app: it has its own
process, its own memory budget, and its own lifecycle that the server can end at any time.

This one watches **one** printer through the Connect instance it runs under, and renders that printer's
state onto **one** WLED controller — a 1D LED strip, or a 2D matrix panel. The point of it is a light
somebody walks past, not a screen somebody opens.

The marketplace context is not decoration. It means:

- a stranger must be able to install it, so the README has to be true and the declared network access has
  to be honest — an over-declared or under-declared permission is a review finding, and a module that
  claims a capability nothing uses is a module a reviewer distrusts;
- the release is built by somebody else's builder from a tag, so anything that only builds on your
  machine is broken even though it compiles.

## 2. Where the process runs — the fact that explains most of the code

**The module process runs inside the `prusa_connect` Docker container**, a Debian 13 (glibc) image, as a
child of that container's PID 1. It does **not** run on the host.

Everything odd about this repository follows from that one sentence:

- **Binaries must be static.** Build with `CGO_ENABLED=0`. A dynamically linked build can fail to start
  inside the container and there is no useful error when it does — the process simply is not there.
- **There is no Go on the host, and none may be installed.** A host toolchain would be a second, drifting
  compiler and a dependency the marketplace builder does not have. Every build and every test runs in a
  container.
- **"Networking" means the container's networking**, not the host's. Anything you conclude from running a
  command on the host is a statement about the host, and may be false one namespace over. See §6.
- **The host's log is the only place a person sees this process.** Its stderr goes there; nothing on the
  wire carries a log line.

## 3. Layout, file by file

| path | what it is, and what you may assume |
| --- | --- |
| `main.go` | **The process.** It does the handshake, answers the lifecycle set, reads the settings, serves `GET /wled-printer-status`, `GET /status` and `POST /preview-finished`, and builds the three things below from those settings: a `printer.Source`, a `writer` and a `driver`. The `observe` grant in the manifest is what lets it read the printer as itself. |
| `drive.go` | **The join.** A `driver` turns each snapshot into a `render.Frame` and hands it to a latest-wins `writer`, which owns the one-minute heartbeat, the doubling failure backoff, the one-line-per-transition log, and both paint shapes — a flat run for a strip, tiling rectangles for a panel. One `writer` per binding; the writers on one controller share its door. Package `main`. |
| `bindings.go` | **The topology.** A `binding` is (printer, controller, segment) — the unit the operator's decision of 2026-11-18 made, replacing one-controller-per-printer. `bindings.check` refuses a set that names one printer twice, one segment twice, or a whole controller beside any other binding on it; `bindings.controllers` groups a checked set by controller, which is what a watch is built from. Package `main`. |
| `watch.go` | **The watch.** One `printer.Source`, one `writer` and one `driver` per binding; one `wled.Client` and one write `gate` per controller, so the bindings on one box take turns at its single small web server. `start` builds everything before any goroutine starts, so a set it cannot run leaves the light as it was. Package `main`. |
| `event.go` | **The event layer.** `eventFor` is one pure function from (the state the printer left, the state it arrived in, the controller's geometry) to a picture and a duration, and the table it holds has one entry. `writer.fire`/`takeEvent`/`paintEvent` are the writer's half: the hold, the return to the steady frame, and the three things a word on a panel costs — one body that establishes the panel over JSON, one frame of pixels pushed over DDP, and a read that says the controller took it. `writer.preview` is the same event, fired now, for the route. Package `main`, beside the frame path because it is the same writer. |
| `pixels.go` | **The pixel transport.** The DDP sender for one controller — one socket, opened on the first event — the address arithmetic that turns the operator's `controller_address` into `host:4048`, the frame that lays the word out as the whole panel, and the repeat that keeps a still picture alive past WLED's 2500 ms realtime timeout. It is also where the four measurements that killed per-LED JSON control are written down. Package `main`, because one writer owns both transports. |
| `internal/text/` | **The word, in pixels.** A 5×7 glyph table drawn for this repository — capitals, digits, a space, a full stop, a hyphen and a percent sign — and a rasteriser that centres a word on a panel, returns the lit pixel indices in the panel's own row-major order, and refuses a word that would not fit at the scale asked for. No transport, no clock, no colour: it answers with indices and leaves the drawing to the caller. It exists because WLED's own text effect does not honour the segment on this firmware — see §5.4. |
| `internal/render/` | **The decision.** `Decide` is a pure function from a snapshot, a moment and an LED count to a colour, a fill and a reason. The operator's state table lives here, and the two calls still marked PROVISIONAL (Q-15's unfilled colour, Q-17's rounding) are one-line changes. |
| `internal/ddp/` | **The event's transport.** Frames of pixels over DDP/UDP to port 4048: the ten-byte header, the big-endian channel offset, the push flag on the last datagram and no other, 480 pixels a datagram. Steady frames still go over JSON, because only JSON reads back and carries a segment's name and geometry; the word goes over DDP because this firmware draws none of the per-LED colours a JSON body carries. Both paths are proved to draw the same frame by `ddp_agreement_test.go`. |
| `internal/matrix/` | Lays a `render.Frame` out as pixels on a matrix, row-major, so the DDP path can be compared against the JSON one. Its fill boundary is a second copy of the painter's `fillColumns`; the two are held together by a test until the writer can call this one. |
| `internal/printer/` | The printer half: a pure reducer (`printer.Apply`, `State.Snapshot`) and a `Source` that owns the bus subscription and the periodic device read. It holds no presentation and no policy. The offline-honesty rule lives here — see §5.3. |
| `internal/wled/` | An HTTP client for exactly one controller: `Info`, `State`, `Capabilities`, and the writes in three tiers (`SetEffect`/`SetSegmentName`, `SetSolid`/`SetRange`/`SetRectangle`/`SetSegments`, `Paint`). Per-client mutex, 2 s default timeout. |
| `faults.go` | The fault domain literal and every refusal code. The domain is **hardcoded** and must match the repository owner — see §7. |
| `module.yaml` | The manifest: name, kind, version, licence, platforms, `options`, `egress`, `needs`, `openapi`. It requests **`observe`, and the reader that uses it is wired** — remove the grant only by removing the reader. **Its key set is closed** — see §5.9. |
| `openapi.json` | The route fragment, every path relative to the mount. Held to `routesapi.ParseFragment` at launch. |
| `install.sh` | **The generator's script, and stale.** It expects a Go toolchain on the machine and a Prusa Connect checkout at a fixed `/work/prusa-connect`. Do not trust it, do not "fix" it as a side errand, and do not cite it as documentation. |
| `.github/workflows/release.yml` | Publishes to the marketplace on a released tag. Pins `core-ref: sdk/v0.1.0-beta.1`. |
| `docs/product-requirements.md` | The product requirements. A **draft**, except the state table, which the operator locked on 2026-11-18. Fourteen questions there are still his to answer. |

The split matters in one direction only: a change to a `internal/` package or to `drive.go`'s painting is
a change to working code that has tests, and a change to *what* the light should show — a new colour, an
event, an animation, a layout — is a product decision the requirements list under a still-open question.
Do not let a code change make one of those decisions quietly.

## 4. Build and test

Wrap the container command once and use it for everything:

```bash
build() {
  docker run --rm --user $(id -u):$(id -g) \
    -e HOME=/tmp -e GOCACHE=/tmp/gocache -e GOPATH=/tmp/gopath -e CGO_ENABLED=0 \
    -v ~/prusa:/work -w /work/wled-printer-status \
    golang:1.26.11 "$@"
}
```

The four gates. All four must be clean before you commit; a commit that leaves one dirty is a commit that
hides a problem behind the others:

```bash
build go build ./...
build go vet ./...
build go test ./... -count=1
build gofmt -l .
```

`gofmt -l .` printing any path is a failure. Today there are **177 tests**: 74 in the root package, 20 in
`internal/printer`, 17 in `internal/render`, 41 in `internal/wled`, 14 in `internal/ddp`, 6 in
`internal/matrix` and 5 in `internal/text`. That number is a fact about this commit, not a target — update it here when it
changes, and never quote it without re-running.

The tests never touch a network and never touch a device. They are fakes and `httptest` servers. If a test
you add needs a real controller or a live Connect instance to pass, it belongs in a manual note, not in
the suite.

**`.core` is a gitignored relative symlink** to the Prusa Connect checkout (`ln -s ../prusa-connect
.core`). `go.mod` replaces the SDK with `./.core/sdk` because that is the path **the marketplace builder
creates for itself** when it checks the core out and builds. A sibling path such as `../prusa-connect/sdk`
resolves on one machine and nowhere CI runs — it builds locally and fails in the builder, which is the
worst failure shape available: green here, refused at review.

The workflow pins `core-ref: sdk/v0.1.0-beta.1`. **Pin it, and raise it deliberately.** That pin decides
which SDK the published binary is built against, so raising it is a change to the shipping artefact's
dependency and needs the tests re-run against the new tag — not a tidy-up.

## 5. The invariants that cost blood

Each of these was learned by breaking it. Each one says what happens if you ignore it, because a rule
without a failure mode gets refactored away by the next agent who finds it inconvenient.

**5.1 Every 2D segment write carries all four bounds, and a flat index is not a rectangle.**
`start`, `stop`, `startY`, `stopY` — always, not just the first two. What the four mean was
**measured on the live 256×64 panel**, because it had been guessed and the guess was wrong:
on a 2D build `start`/`stop` are **columns and are clamped to the panel's width**, `startY`/`stopY`
are **rows**, and a `stopY` of **0 becomes one row**. A body naming pixels 10158 to 16384 with
`stopY` 0 answered `{"success":true}` and the device then reported *both* segments as
`start=0 stop=256 startY=0 stopY=1` — the black remainder covering the green fill on a single
line, and the panel dark. There is no flat pixel index on a panel: the firmware adds nothing to
`start`, and a pixel count in a `len` field is a count of columns, so a width-times-height number
is twelve times too large on a 32-row panel. So a panel frame is **rectangles that tile it** —
columns `0..N` over rows `0..height` for the fill, columns `N..width` over the same rows for the
remainder — and the boundary is the frame's fraction of the **width**. A 1D controller is the
other shape and is unchanged: no rows, a run of pixels from the first, both row bounds zero.

**5.2 Telemetry is a patch stream, not a snapshot.** A path is published only when it moves. A state map
built from the stream is therefore an **accumulation that starts empty** — a path that has not moved
since the module started is not in the window, and treating a window as a complete picture loses
everything that was already true at startup. This is why `Source` also polls `device.get`: `Connected`,
the issues and the rest of the record do not arrive on the telemetry lane at all.

**5.3 A printer that dies keeps its last state forever.** The server's state tree is documented as what
the device *last* reported and nothing in the host dates it; `/state` has ten values and `OFFLINE` is not
among them. A printer switched off at 14:02 still says `PRINTING` at 15:30, and the server takes about
**60 seconds** to notice anything at all. So the display must be able to say *"was printing at 94 % when
last heard, six minutes ago"* and must never present that as live. `internal/printer` enforces this:
`Snapshot.Connected`, `Snapshot.Job.Live`, and `LiveProgress()`/`LiveRemaining()` returning `ok=false`
unless the printer is connected **and** fresh (`StaleAfter` is 30 s). **Do not weaken that pair to make a
renderer simpler.** It is the reason the package exists, and a display that lies about a dead printer is
worse than no display.

**5.4 Text on this panel is pixels this module draws, and effect 122 is not it.** The obvious road was
WLED's own Scrolling Text — **effect 122**, the word in the segment's `n` field, 48 characters at most.
It does not work on the operator's 256×64 panel (`morpheus`, `14.7.0-mdev`): four different groupings,
each with the word as the name and every parameter written (`ix` 128, `c2` 255), lit **only the first few
columns on the left**, and the operator watched all four and reported the same fragment. So the word is
drawn here instead: `internal/text` holds a 5×7 glyph table this repository owns and a rasteriser.
**Nothing in this module asks a controller to draw a string any more**, and a change that puts effect 122
back because it "should work" is repeating a measurement this file already records.

**And the pixels do not go over the JSON API either.** `Paint` — a state body whose segment carries an
`i` array, one colour per LED — is accepted by this firmware and drawn by nothing. Measured on the same
panel on 2026-11-18, four ways, each one a 200 with `{"success":true}` and no line in the log: a hundred
pixels at LED 7680 with the segment frozen; the same with it unfrozen; the same with the segment's four
bounds declared in the same body; and a chunk of sixty colours in case the accepted size was far smaller
than the 8 KiB budget believed. The operator looked twice at a panel that stayed blank. `/json/state`
carries no `i` key at all on `14.7.0-mdev` — the segment keys are listed in §8 — so a body that was
accepted cannot even be read back to see what the controller made of it, while **geometry writes on the
same endpoint do render**: the progress bar is segment bounds and it has always worked. So the word goes
on the transport built for pictures, DDP, and `Paint` stays in `internal/wled` as an unused tier with its
tests rather than as a road back. `pixels.go` has the rate, the frame and the proof.

The 1D half of the rule is unchanged and cannot be fixed from here: a strip has no second dimension, so
a word is impossible on it and its finished state is the solid green it already shows. Any 2D feature
needs a 1D fallback that is designed, not an afterthought: if your status is text-only, a strip owner
gets nothing.

**5.5 On a 2D build, `start`/`stop` are columns and `startY`/`stopY` are rows.** `len` is a count of
**columns**, not of pixels. A width-times-height count in a `len` field is twelve times too large on a
32-row panel and the firmware will either refuse or wrap.

**5.6 Effect parameters are per-effect.** WLED publishes `/json/fxdata`, one flag string per effect,
saying which of the sliders are live for that effect. Read it rather than assuming. The concrete trap:
`Percent` fills from `ix` and **ignores** `sx`, so a progress bar built as an effect parameter must be
written to `ix`; written to `sx` it looks like it is being set and never moves.

**5.7 Write budget: 1–5 writes a second.** Never stream. Use the `transition` field to fade, so the
controller interpolates instead of the module sending frames. Measured: a 48-LED strip answers in ~53 ms
and a 256×64 panel in ~28 ms, so the wire is not the limit — WLED's single small web server is. A full
16,384-pixel `Paint` is 19 requests at the 8 KiB default budget; that is a once-every-few-seconds floor,
not an animation path.

**That budget is the HTTP server's, and DDP is not on it.** A datagram never reaches the controller's web
server, which is the one small task the number above is about, so the event's picture is pushed again
once a second for as long as it holds (`pixelRepeat`) — 35 datagrams a second of identical bytes, about
50 kB/s. The rate is chosen against the controller's own `realtimeTimeoutMs` of 2500 ms: a still picture
needs only enough repeats to survive it, one a second is two and a half times the margin, and faster
would be the same bytes for nothing. **It is still not permission to stream.** Raising it is a change to
what the panel's WiFi does all evening, and the arithmetic belongs in the same sentence as the number.

Two waits sit under that budget and neither is an addition to it. A still picture is repainted once a
minute (`heartbeatEvery`), because a controller that reboots comes back in its own saved state and a
printer that has nothing to say would never correct it; the interval is measured from the last write that
landed, so a moving print resets it and pays nothing. A write that **fails** opens a wait that doubles
from a second to half a minute (`backoffFloor`/`backoffCap`), and nothing bypasses it — a changed picture
waits in the slot rather than knocking again. That is the difference between a retry and a storm: the
installed module once POSTed once a second at a controller that answered a ping in 3 ms and took none of
the writes.

**5.8 A module's settings cannot express a composer.** The manifest's `options` has **nine closed
types**, no enum, no repeated group, and `list`/`object` render as a one-line JSON box in the UI. There is
no way to build a colour picker or a per-state mapping table out of settings. A rich editor needs the
module's **UI half** (a separate browser-side bundle the manifest declares), and — whatever you build —
**every settings save restarts the module**, so nothing may depend on in-memory state surviving a save.

**5.9 The manifest key set is closed.** An unknown key is **refused and the module will not load**. There
is no forward-compatible "extra field". This is why `author:` is absent even though the marketplace asks
for it: the key does not exist, so writing it produces a module that does not start. When you want a key
that is not there, the answer is the SDK or the marketplace schema, never a hopeful line in the YAML.

**5.10 A frame is one request carrying two segments.** Every write in `internal/wled` addresses segment
0, so a fill written and then a remainder written are **one segment, not two**: the second body replaces
the first one's bounds, the pixels the fill covered belong to no segment, and nothing draws them. A
256×64 panel spent a night proving it — one black segment over pixels 10158 to 16384, no segment at all
over the 10158 the module believed were green, `/status` reporting `progress`, and every send logged as
taken. `paint` builds both segments and `SetSegments` sends them in one body: one request, so the device is
never asked to show half a frame, and half the traffic of writing them one after the other.

**5.11 The log carries events, not ticks — one line per transition, silence otherwise.** `/status` is the
present tense and the log is the history, so a line exists to answer *"what changed while nobody was
looking"*. The driver writes one when the printer's state word moves (`the printer <name> went from X to
Y`), one when the host loses or regains its session with it, and one when the reading goes stale or live
again; the writer keeps its failure lines — the first failure, each widening retry, one on recovery.
Everything else is deliberately silent: a progress tick, a frame that changed, a successful send, and the
heartbeat repaint, which fires once a minute on a still picture and must cost no line at all — otherwise
an idle night becomes a thousand lines that hide the one transition a person needed. A printer still
saying `PRINTING` is one line at the start of the hour and none during it. Do not "improve" this into a
line per tick: the route answers the present tense, and the tests count the lines.

**5.12 One segment, one binding — and the refusal happens before a client is built.**
A binding is (printer, controller, segment). One printer appears in at most one binding; one
segment is driven by at most one binding; a controller may carry several; and a controller that
holds no named segments carries exactly one, because the whole light is then one display.
`bindings.check` enforces that, and `watch.start` calls it before it builds a client, so a mistake
is a sentence the operator reads on the instance rather than two writers overwriting each other on
a controller at the rate of a tick. Two costs ride with the rule and neither is avoidable from here:
**WLED has no ownership**, so exclusivity holds only inside this module and another tool can still
write those pixels; and every borrowed binding owes its own capture and its own restore, which
multiplies as bindings grow. A whole-controller binding (-1) claims every segment on its box and
therefore collides with anything else there — the check refuses that pairing rather than letting a
two-rectangle panel body take the segments somebody else's zone lives in.

**5.13 An event is a transition, and a strip gets nothing.** A frame is a function of the printer's state;
an event is a function of the transition between two states, which is the one thing a frame cannot carry,
because a frame does not know what came before it. `event.go` holds the whole of it: `eventFor` takes the
two state words and the controller's geometry and returns a picture and a duration, purely, so it is
tested with no clock, no socket and no device; the writer stamps the deadline from its own clock and holds
the picture for exactly that long.

Four rules, each with the failure it prevents:

- **A strip gets nothing.** A word is pixels on a two-dimensional panel; a 1D strip has no second
  dimension, so the pixels would fold into one run of LEDs and buy no word at all. A strip's finished
  state is the whole run in the finished green, which is what the operator asked for on that light. §5.4
  is the same fact from the other side.
- **A borrowed segment gets nothing, at the writer's door.** `fire` refuses before the event is even
  stored. A module pointed at one zone of somebody else's controller was given a colour to show there,
  not an animation to perform; it writes no brightness and no power on that controller either, and an
  unrequested animation is the one thing a zone's owner would not have agreed to. Suppression beats
  carrying the word while preserving the captured name and bounds, because the safest thing to put on a
  light somebody else is using is nothing.
- **Only a transition counts.** The driver's first snapshot is a baseline, so a restart fires nothing even
  if the printer is already `FINISHED`; and a state word that stands still fires nothing however often it
  is read. Both are the `transitions` bookkeeping the log already kept, reused rather than reimplemented.
- **An event never costs a frame more than its own duration.** The steady frame the driver keeps deciding
  waits in the writer's one slot while the event holds, newest first; at the deadline the frame goes out.
  When the event lands the writer drops what it last delivered (`last = nil`), because the picture on the
  light has changed underneath it and the frame that follows must paint rather than be suppressed as
  "already shown". A failed event write goes through the same doubling backoff and the same
  one-line-per-widening log as any other write, so a controller that is off costs a handful of lines, not
  one per tick.

Adding an event is one case in `eventFor` plus its word, colour and hold: the next two wanted (a print
starting, a filament change) need their animation and duration decided by the operator before they get
one, and a case invented here would be a requirement nobody agreed to. An entry whose picture is not a
word needs a second body in `paintEvent` as well, and that is a design decision of its own.

**And a name is not cleared everywhere.** The borrow work measured that a body writing a segment without
its `n` clears that segment's name — WLED 16.0.1 on the desk light. The same steady body was written to
the panel (WLED 14.7.0-mdev) after a `FINISHED` event and the name survived: segment 0 read
`"n":"FINISHED"` with `fx` back to 0. So a body that must keep a name carries it, and build nothing that
depends on a write without a name removing one. The event's establishing body carries the word as the
segment's name for exactly this reason — it is also how `/json/state` still says `FINISHED` while the
word is up in pixels — and the pixels after it name nothing.

## 6. The supervision you live inside

The server that launches this process imposes limits. They are not advisory; they are how the process
dies.

- **Inbound call timeout: 10 s.** A request into a route must answer inside 10 s or the caller gets
  nothing. This is why the WLED client's default timeout is 2 s and the device read is bounded at 5 s.
- **Liveness ping every 15 s, 5 s to answer, three misses and the process is killed.** A missed ping is
  not a warning.
- **128 MiB RSS watched every 2 s.** Stay under it; a per-pixel frame buffer for a 16,384-pixel panel is
  small, but a naive retained history of them is not.
- **Three failures in an hour switches the module off until a human acts.** It does not restart itself.
  So a crash loop that looks like "it will come back" is a module that is off, and somebody has to be
  told.

One asymmetry worth knowing: a **slow handler does not cost the ping**, because requests are served in
their own goroutine. A **slow bus callback stalls the subscription** — the frames stop arriving and the
module's picture of the printer silently freezes, which looks exactly like a healthy module.

## 7. Marketplace mechanics

- **The extension id is `com.github.aux.<owner>.<repository>`**, here
  `com.github.aux.nothingmn.wled-printer-status`. It is also **the namespace every route mounts under**,
  so `go.mod`'s module path, `faults.go`'s `faultDomain` and the repository name must all agree. Change
  one and the fault codes stop matching the mount, and clients that branch on a code never see it.
- **The release tag must end with the version in `module.yaml`.** A tag that does not is a release that
  publishes nothing, after the run has already started.
- **The signing key lives in a GitHub environment named `release`**, and the secret is
  `PRUSA_CONNECT_EXTENSION_SIGNING_KEY` — **not** the Slicer-plugin secret name, which is a different
  key for a different product. The builder runs the publish job inside that environment.
- **The signing key file is on the build host, outside every repository.** Never print it, never commit
  it, and never leave it anywhere that gets bind-mounted into a container — a mount of `~` for a build is
  a mount of the key.

## 8. What is unverified — say so, do not imply otherwise

Nothing in this module has ever run against a live Prusa Connect host. Specifically:

- **The bus subscription has never carried a real printer's frames.** Its cadence, the origin narrowing
  and the 30 s staleness horizon are proven against **fakes only**. A module's bus does not exist until
  the module is installed and serving, which is why this has not been done yet.
- **The writes have run against the panel, and its *pixels* cannot be read back.** Frames and the finished
  event have been installed and driven the operator's 256×64 panel since 2026-11-18, and `/json/state` reads
  back the geometry each one leaves — segment 0's four bounds, the name, which segments are switched off —
  and the module's own `last_send`. What it does not carry is the per-LED `i` array: on `14.7.0-mdev` the
  segment keys are `bri c1 c2 c3 cct col frz fx grp id ix len m12 mY mi n o1 o2 o3 of on pal rY rev sel set
  si spc start startY stop stopY sx tp`, and WLED's websocket pixel read returned all-zero frames the one
  time it was tried here. So **a picture's pixels are unverified except by a person looking at the light**,
  which is what `POST /preview-finished` exists for. A claim that the letters are the right size, or in the
  right place, has no evidence behind it but someone's eyes.
- **DDP reaches this panel, and that is all that is proved about it.** Measured from inside the
  `prusa_connect` container on 2026-11-18 — not from the host, because the module's network namespace is the
  container's — a single datagram carrying a ten-byte header to `10.0.0.136:4048` turned `/json/info`'s `live`
  from false to true, and a full 35-datagram frame is taken the same way. What `live=true` proves is that the
  controller took the pixels into the buffer it draws instead of its segments; it cannot prove a pixel of it
  was drawn, and nothing on this firmware reports individual colours. **Whether the letters appear, and
  whether the panel's wiring maps row-major the way `y*width+x` assumes, is unverified.**
- **Live mode ends on the timeout, not on a write.** Also measured on this panel: a JSON state write while the
  controller was live left `live` true, and it went false about 2.5 s after the last datagram — WLED's
  `realtimeTimeoutMs`, 2500 ms. So the frame that takes the light back is written first and the controller is
  read afterwards, once, past that timeout: that read is the only proof the hand-back happened, and the
  module logs one line saying what it found.
- **The first live run of the pixel word drew it under the frame's remainder.** The establishing body left
  segment 1 switched on, so the black band over columns 28..256 covered the word and the panel showed a
  fragment on the left — the failure the change exists to end, from the other direction. Fixed, pinned in
  the test, and the install record has the read-back either side of it. It is here because a body that
  writes one segment has to think about every other segment the panel will draw after it.
- **The release workflow has never executed.** It is written from the builder's documented interface,
  not from a green run.

An agent that describes any of these as working has produced a false statement about the product, in a
repository that will be read by a marketplace reviewer. Write "unverified" beside the claim instead.

## 9. Forking

A fork must change **three things together**:

1. `go.mod`'s module path,
2. `faults.go`'s `faultDomain` literal,
3. `module.yaml`'s `name`.

They move as one because the extension id is derived from the repository, becomes the mount point, and
becomes the fault namespace. A fork that changes two of three loads on one machine and fails to serve
under its own namespace. The fork must also **register its own repository and its own signing key** —
the existing one signs `nothingmn`'s releases and will not sign yours. None of this is expensive today
and all of it is expensive after anybody has installed the module, because an installed declaration
points at an id that no longer exists.

## 10. If you are asked to change what the light shows

The wiring exists: `main.go` builds a source, a driver and a writer from the settings, and `drive.go`
paints. What is left in the product is mostly decisions rather than plumbing. Read
`docs/product-requirements.md` first and treat every item marked **open** as a decision that is not
yours. The hard parts:

- **choosing a tier per controller** — from `Info` and `Capabilities`, not from a hardcoded guess, with a
  designed fallback for a 1D strip when the feature is 2D-only (§5.4);
- **changing the state table** — the colours in `internal/render` are the operator's decision, approved
  on 2026-11-18. Adding a state, a colour or an event is a product decision; treat his table as the
  authority and ask before editing it;
- **the settings**, which cannot express a colour table (§5.8) and restart the module on every save;
- **the write budget**, which rules out a per-frame render loop (§5.7);
- **the dead printer**, which must be visible as dead rather than frozen at 94 % (§5.3).

If the request is "make it show X" and X is unspecified, the honest move is to say which decisions are
unspecified and ask, rather than to invent a palette and ship it as the answer.
