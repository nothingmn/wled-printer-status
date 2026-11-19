# wled-printer-status

A Prusa Connect module that turns WLED lights into status displays for 3D printers.

It runs as a service module: a small Go program the Prusa Connect server launches, which reads each
printer's state from that server and paints a light — a strip along a desk, or a panel on a wall — so
that somebody walking past can tell what the printer is doing without opening an app. One instance
shows as many printers as the `bindings` setting lists, each on its own segment of its own controller.

## Status

**This module reads one printer through Prusa Connect and drives one WLED controller, and it has done
that for the length of a real print: an MK4 on a 256×64 panel, watched from 0 % to `FINISHED`.** Several
printers on several controllers is the shape the code has now and the shape the settings express; one
printer on one panel is what has been watched, end to end, on hardware. The
program the server launches reads the printer, decides what the light should show, and writes it to the
controller named in its settings, with nobody standing at the panel. `GET /status` answers what it
currently thinks — the printer, how old its reading is, the frame it has decided, and what the last write
to the light did — so a person can read the decision without walking over to the light.

This is the first release and an early beta: what follows is what has been proved on hardware, and what
has not been. Expect it to change.

What those runs proved, on the hardware rather than in a test:

- **The fill tracks the job.** At 87, 90, 91, 96 and 98 % the lit columns were counted against the panel's
  own geometry, and each one was the printer's percentage of 256 — the fill is decided in the controller's
  pixels rather than in a number that merely looks plausible.
- **A finished print holds the whole panel** in the finished state's colour, and keeps holding it.
- **A controller that goes away is waited out, not hammered.** Unplugged for two minutes, the module
  logged six escalating retries; thirty seconds after the controller came back it was driving the panel
  again, with no restart and nobody touching it.
- **A picture that has stopped moving is written again once a minute**, which is how a controller that
  rebooted into its own saved state comes back to the printer's picture while the printer has nothing new
  to say.
- **A transition is an event, not just a colour.** When the printer's state word moves, the module
  puts a picture on the light for a fixed time and then hands the light back to the steady frame. The
  first one is `FINISHED`: on a panel the word is drawn in pixels — the module's own font, 235 by 35
  pixels on the operator's 256×64 panel, in the finished green — for one minute, after which the whole
  panel in the finished green takes over. **The word is this module's pixels and not a firmware
  feature.** WLED's Scrolling Text effect (id 122) was the first attempt and is not used any more: on
  this panel it lit only the first few columns on the left, at every grouping that was tried, and the
  operator watched all four and saw the same fragment. The second attempt put the same pixels into a
  JSON body one LED at a time, and this firmware accepted every one of them — `200`, `{"success":true}`,
  nothing in the log — and drew none: four variants tried on the panel, and the operator looked twice at
  a light that stayed blank. So the pixels go on the transport a controller keeps for pixels, **DDP**:
  raw colour datagrams to UDP port 4048, one frame of 16,384 pixels, pushed again once a second for the
  minute the word holds, because the controller drops live mode 2.5 seconds after the last datagram. The
  steady frame still goes over JSON, and when the minute is up the light goes back to it. On a strip the
  finished state stays the solid green it has always been, because a word needs a second dimension and a
  strip has one.
- **A borrowed segment works on a strip somebody else also uses.** On a six-zone desk light — 48 LEDs,
  WLED 16.0.1, six named zones — the module drove one of the six for the length of a print with the fill
  moving inside that zone, left the other five byte-identical, wrote no brightness and no power on a strip
  running at its own 255, and gave the segment back on each of three graceful stops.

What that has not shown, and these are gaps rather than formalities: **idle** and a printer that has gone
quiet mid-job have never been seen on a panel. The **starting** state — a print the machine has taken and
has not yet started to move — has, in the waiting yellow at 0 %; it appeared for the first time on the
evening that the `FINISHED` word did. **Nobody has looked at the `FINISHED` word since it moved to DDP.**
The module puts it on the panel and the device confirms what it can — segment 0 holding the whole panel in
black with `FINISHED` as its name, the frame's remainder switched off so nothing covers it, and
`/json/info` reporting `live: true` because the datagrams arrived — but this firmware's `/json/state`
carries no per-pixel array at all, so the letters themselves are only verifiable by eye, and so is the
question of whether this panel's wiring is row-major the way the frame assumes. That is what
`POST /preview-finished` is for: one request puts the word up for its minute, and the install record in
[`docs/installing-locally.md`](docs/installing-locally.md) has the read-back either side of one. Beside it,
`POST /preview-pattern` draws one of three diagnostic pictures — the whole panel white, a white border, or
four coloured quadrants — so that a single look separates a dead transport from a wrong LED map from an
upside-down panel rather than leaving a blank panel to be guessed at. Neither the word nor the ladder has
been looked at by a person yet. And **it has not been through the marketplace**: this beta is the first
release, so nobody has yet installed it the way a stranger would.

So: a working module on one printer, one panel and one borrowed zone of a desk light, and not a finished
product. One event is here and the mechanism for the rest is; the brightness follows the time of day in a
zone the operator names, and a schedule has never yet moved a live panel's brightness at a window's edge
— the install that carried it ran a window that reproduces the value already in force. What is still open
is the rest of the animations — a print starting and a filament change are both wanted and neither is
designed — the panel's richer display, and the composer. What has not been built is written down
in [`docs/product-requirements.md`](docs/product-requirements.md).

## How it is driven, and where its configuration lives

Three things here are not what a Connect module usually does.

- **The module is driven by its own UI.** Its screens are served by the module and are the only editing
  surface, and the manifest declares **no `options`** — so the module's page under Settings, Extensions has
  one tab, its own, and no second and older pane to edit the same data in.
- **Its configuration lives in the module's own store**, beside the installed module on Prusa Connect's
  writable config volume: bindings, moments, the state table, layout, schedule, pause state and per-light
  snapshots.
- **That store survives a restart and an upgrade, but not a `module purge` or a fresh install**, which
  wipes it — for which the per-light **`download a backup`** is the answer, writing that light's own
  configuration to a file you keep outside the module.

The host's settings, which used to carry this configuration, are read once at the first start as a
migration source and never again — and after this release there are none to read: a fresh install starts on
the module's own defaults, serves its pages, drives nothing, and its first light is added on the Bindings
screen. What the light is meant to become is specified in the locked
[`docs/product-requirements.md`](docs/product-requirements.md); the eight screens it describes are design
mockups, not the product, in [`docs/design/v2/`](docs/design/v2/README.md).

## What it is meant to do

Four kinds of information are wanted from a light, in the order they matter: colour as a signal, LEDs as
a progress bar, an animation for some states, and brightness. A printing job could be green filling
along the strip. Paused could be amber and still. Finished could be a slow pulse. A printer that has gone
away could be dim, saying nothing rather than lying.

Both halves of that are built:

- **`internal/printer`** tracks one printer honestly. It folds the server's telemetry stream and the
  printer's record into one state, and it will not present a stale reading as a live one — a printer
  switched off at 14:02 is still saying `PRINTING, 94%` in the server's state tree at 15:30, and this
  package is the code that refuses to repeat that as present tense.
- **`internal/wled`** is an HTTP client for one controller. It reads the controller's facts and state,
  and writes power, brightness, a colour or a rectangle — two tiers, effects and geometry. There is no
  "one colour per LED" write in it: a JSON body carrying one is accepted by this panel's firmware and
  drawn by nothing (four of them, every one a `200`, and the operator looked twice at a light that stayed
  blank), so the call was removed rather than left unused. That is also why the finished word does not go
  over this path — see the event section below.
- **`internal/ddp`** is the transport for a picture: one frame is 35 UDP datagrams of raw colour to port
  4048, ten bytes of header each, the push flag on the last one so the controller draws the frame rather
  than each datagram as it lands.

The 1D strip's share of the decision is made: the state's colour, and a fill for the job's progress. What
is missing is the rest of the decision layer: which tier to use on a controller that can do more than
colour, what animations play at the events the operator named, and what to do when two things want the
light at once. Those are product decisions, not coding ones, and what the module is still waiting on is written
down in [`docs/product-requirements.md`](docs/product-requirements.md).

## Rendering

`internal/render` holds the decision itself: `Decide` takes a printer snapshot, the moment to judge it at
and the strip's LED count, and returns a `Frame` — one colour, how many LEDs carry the completed part of
the job, and a short reason naming the rule that fired. It is a pure function: no socket, no clock, no
state kept between calls. It gives each of the ten reported states the operator's colour, and it gives a
disconnected or stale printer a dim red strip with nothing filled, whatever percentage the server still
remembers — except for a printer whose last word is `FINISHED` or `STOPPED`, which keeps that word's colour
through a silence and reports itself stale on the route instead, because a job that is over cannot be
overtaken by a reading the module never received. Two sub-decisions are **provisional** and marked as such
in the source: the fill rounds down rather than to the nearest LED, and the LEDs past the fill are off.
`main.go` calls it once a second, and `drive.go` writes the frame it returns to the controller — so this is
the rule the light is showing, not a rule waiting for a caller.

A frame goes to the controller as **one request carrying two segments**: the lit part in the state's
colour, the rest in the off colour. That is not a style choice. A controller draws one segment per run
and every write here addresses the same one, so a fill written and then a remainder written are one
segment rather than two — the second takes the first one's place, the fill belongs to no segment, and
nothing draws it. A 256×64 panel spent a night doing exactly that: it held one black segment over the
unfilled pixels, the module logged every send as taken, and the panel was dark.

**A frame is re-sent at most once a minute.** A controller that reboots comes back in its own saved
state, not in the frame the module was showing, and a printer that has nothing to say — idle overnight,
a finished job, a paused machine — would otherwise never correct it. So a frame that has stood unwritten
for a minute is written again, unchanged. This is a **floor and not an addition**: the clock runs from
the last write the controller took, so while a print is moving the ordinary writes keep resetting it and
the repaint never fires. It costs traffic only when the picture has stopped moving, where it is one
write a minute.

**What just happened is an event, not a frame.** A frame is a function of the printer's state, so it
cannot say that the state *moved* — a whole panel in the finished green looks the same in the first second
of a finished print as it does an hour later. So the driver turns a state change into an event: a picture
and a duration, decided from the state the printer left and the state it arrived in, with no clock and no
socket in the decision. The writer holds that picture for its duration and then goes back to the frame it
would otherwise have shown; the steady frame the driver keeps deciding waits in the one slot meanwhile, so
an event delays a frame by its own duration and no longer. The one entry so far is `FINISHED` on a panel:
the word, spelled across it in the finished green, for a minute (`finishedEventHolds`). A strip is never
sent it — a word is pixels in two dimensions, and the operator asked for his strip to keep its green — and
neither is a segment borrowed from a controller somebody else also drives, because an animation nobody
asked for is not something this module puts on another person's light.

An event makes three requests and no more. One JSON body establishes the panel: segment 0 the whole of it,
switched on, in the background colour, with the word as its name, and segment 1 — the frame's remainder —
switched off, because a panel draws its segments in order and a remainder left on covers the word. That
body is also the only thing that puts the word's name on the controller, since a datagram carries none.
Then one DDP frame carries the pixels, the whole panel, 35 datagrams, the push flag on the last. Then one
read of `/json/info` says whether the controller took them. For the minute it holds, that same frame is
pushed again **once a second** — the controller drops live mode 2500 ms after the last datagram, so one a
second is two and a half times the margin and 35 datagrams a second is what a held picture costs. At the
end the module stops pushing and writes the steady frame — one request it would have written anyway — and
that body **clears the name**, because this firmware keeps a segment's name through a write that does not
mention one: without it the panel would carry `FINISHED` long after the minute, ready to be drawn the
moment anything set a text effect on that segment. The clear is one key in a body already going out, not a
second request. Then the module waits out that timeout and reads the controller once more: `live: false`
is the only evidence a datagram can never give by itself.

A write that fails is not retried on the next tick. The module waits — a second after the first failure,
then doubling to half a minute — and a picture that changes during that wait is held and sent when the
controller answers again rather than added to a pile of attempts. The log says so once per change of
wait, not once per attempt.

## The panel's layout: a bar, the percentage, and the job's name

A strip can say one thing: a colour, and how much of the run is filled. A panel can say more, and the
`layout` setting says what:

```json
{"bar": true, "bar_band": "bottom", "bar_rows": 12,
 "percent": true, "name": true, "rotate_seconds": 15,
 "remaining": false, "ends": false, "temperatures": false, "elapsed": false,
 "material": false, "printer_name": false, "layer": false}
```

That is the default, and an instance that never writes the box runs on exactly those values. The keys are
the arrangement, and each one is a part of the picture rather than a mode:

| key | what it does | default |
| --- | --- | --- |
| `bar` | a progress bar in a band of the panel, filled to the job's own fraction of the width | `true` |
| `bar_band` | `top` or `bottom`: which end of the panel the band is at | `bottom` |
| `bar_rows` | how many rows the band takes, 1 to 48 of a 64-row panel | `12` |
| `percent` | the progress in digits, such as `41%` | `true` |
| `name` | the job's own name, on the other half of the rotation | `true` |
| `rotate_seconds` | how long each half holds the panel | `15` |
| `remaining` | how long the job has left, as `42M` or `1H12M` | `false` |
| `ends` | the clock time it ends at, as `ENDS 11:12`, in the zone this module holds | `false` |
| `temperatures` | the nozzle and the bed, as `NOZ 221/220 BED 60` | `false` |
| `elapsed` | how long it has been printing, as `1H12M` | `false` |
| `material` | the filament's short name, as `PLA` | `false` |
| `printer_name` | the machine's own name, as `HERMES MK4` | `false` |
| `layer` | how far up the nozzle has got, as `Z 8.6MM` — a position, not a layer number | `false` |

The seven lines are drawn at the font's own size, seven rows each with one blank row between, in that
order, along the bottom of the text band; the digits and the name are centred in the rows they leave. A
line the printer has not reported is left blank while its rows stay reserved. They are a menu, not a
layout: a 64-row panel with a 12-row bar leaves 52 rows, the digits at scale 5 take 35 of them, and two
lines fit — so a third is **refused**, not cropped, and the refusal names the lines and the two numbers
(a thinner bar, or a smaller `percent_scale`) that would take them all. Four fit if the percentage drops to
scale 3.

Three things about it are worth knowing before you write one.

**A layout is judged before the module starts.** A key this build does not read, a band it does not know,
a `bar_rows` outside 1 to 48, a rotation of no seconds, or a set of optional lines the panel cannot hold
refuses the start with the field and the value in the sentence — and so does a bar thicker than the panel
the frame is drawn on. A
bar clamped to fit would be a picture nobody asked for, drawn without saying so.

**Every part can be switched off.** A layout with `bar`, `percent` and `name` all false draws nothing of
its own, and the panel takes the plain frame it had before this setting existed: one colour filling it,
over one JSON request. That is the opt-out.

**The text is drawn here, at sizes worked out for your panel.** A 5×7 glyph at a scale of 2 is 10×14
pixels; at 5 it is 25×35. On a 256×64 panel with a 12-row bar, 52 rows are left: the digits take the
largest scale that fits and is never larger than the word `FINISHED` the panel already draws — a scale of
5, which makes `41%` 85 pixels wide and 35 tall — and the job's name takes a scale of 2, which is 21
characters across the panel. A name is longer than that: `MK4IS_0.4n_0.2mm_PLA_MK4IS_1h52m.gcode` is 38
characters. So it is upper-cased, every character this module's font does not hold becomes a space
(underscores, mostly), and the result is cut at the last whole word that fits — the example becomes
`MK4IS 0.4N 0.2MM PLA`. Not scrolled, because a scrolling word is a frame rate and this module does not
stream; not abbreviated, because a wrong abbreviation is worse than a short name. The head of a slicer's
file name is the part that identifies the job.

**What it costs.** A picture with text cannot go over the JSON API on this firmware, so a printing panel's
frame goes out as pixels over DDP: 16,384 pixels, 35 datagrams, about 50 kB — and that frame is pushed
again **once a second** for as long as the print runs, because the controller leaves live mode 2500 ms
after the last datagram. 35 datagrams and about 50 kB a second is the steady-state cost of a printing
panel. It is not the JSON write budget: a datagram never reaches the controller's web server. The
controller is held in realtime for the duration of the print and released — back to its own segments,
with a read that proves it — when the print finishes, when the reading goes stale, when the printer
disconnects, or when the module cannot reach it. A frame that is not about a job in progress — idle,
finished, stopped, an error, a printer that has gone quiet — is the state colour filling the panel,
unchanged, over JSON.

You can see the picture without running a print:

```bash
curl -s -X POST "$base/preview-frame?percent=41&name=MK4IS&hold=120"
{"lights":[{"controller":"http://10.0.0.136","light":"10.0.0.136"}],"count":1,
 "percent":41,"holds_seconds":120}
```

It draws exactly what a printing panel draws with those values — same layout, same scales, same colours,
same route through the writer — and, for a request that resolved to one light, `name`, `halves`, `layout` and
`rotation_seconds` say what that light will show, in order. `percent` is required and is a whole number 0 to
100; `hold` works as it does on the other two previews (default 60, ceiling 300, refused past it).

On an instance driving more than one light the picture goes to every one of them and each draws it at its own
geometry, so `name` and `halves` are per light in `lights` — a 64x32 panel and a 256x64 one cut the same name
differently. A strip has no rows for a bar or a line of digits, so it is refused for that light and the panel
still gets its picture.

## Looking at the finished word without waiting for a print

A person who wants to know what his panel will do when a print ends should not have to run a print to find
out. The module serves one request for exactly that:

```bash
curl -s -X POST http://localhost:8080/api/v1/modules/com.github.aux.nothingmn.wled-printer-status/preview-finished
{"lights":[{"controller":"http://10.0.0.136","light":"10.0.0.136"}],"count":1,
 "event":"FINISHED","colour":{"r":0,"g":170,"b":0},"scale":5,"holds_seconds":60}
```

`POST /preview-finished` fires the finished event now: the same word, in the same colour, for the same
minute a completed print gets, so what you look at is what the light will do rather than a rehearsal of
something else. The pixels go out on the module's own writers, behind the same gate and the same retry
window as every other write, and the steady frame takes each light back when the minute is up — the
route changes nothing about a light's ordinary picture and writes nothing to a controller itself.

It goes to **every light the instance drives**, and `?light=10.0.0.136` aims it at one — the name is the one
`GET /controllers` gives, the address without its scheme. A light that cannot be asked for the word is
refused for that light and named in `lights` beside the ones that took it; the request is refused as a whole
only when no light took it:

| answer | code | why |
| --- | --- | --- |
| 503 | `PREVIEW_NOT_RUNNING` | the module is driving no light at all yet, which is its state for the first second of a start |
| 404 | `CONTROLLER_NOT_FOUND` | `light` names a controller this instance drives nothing on |
| 409 | `PREVIEW_BORROWED_SEGMENT` | that light is one borrowed segment of a controller somebody else also drives, and an animation is not something this module puts there |
| 409 | `PREVIEW_NOT_DRAWABLE` | that controller reported no matrix (a strip), or its panel is too small for the word at the scale this build draws it |

`controller`, `name` and `halves` appear when the request resolved to one light; a multi-light answer carries
the same facts per light in `lights`, because a field naming one of two lights would be answering for a light
the picture did not go to.

The route asks the host for `operate_resource` on the module's instance, because it changes what the light
shows.

## Telling a blank panel from a wrong one

A dark panel answers "did it draw?" with nothing, and the same darkness can be three different faults:
the datagrams never arrived, the controller drew its own segments instead, or it drew them with a map this
module's arithmetic does not match. The word cannot separate them, because the word is one shape in one
colour. So there is a second route, a ladder of three pictures, each answering one question and each
assuming the answer before it:

```bash
base=http://localhost:8080/api/v1/modules/com.github.aux.nothingmn.wled-printer-status
curl -s -X POST "$base/preview-pattern?pattern=solid&hold=15"
curl -s -X POST "$base/preview-pattern?pattern=border&hold=15"
curl -s -X POST "$base/preview-pattern?pattern=quarters&hold=15"
```

| picture | on a panel | on a strip | what it answers |
| --- | --- | --- | --- |
| `solid` | the whole panel in pure white | the whole run in pure white | **does DDP draw at all.** Lit means the transport and the drawing work and every later rung is worth looking at; dark means the transport is the fault, and the word would have been dark for a reason that has nothing to do with the word |
| `border` | a white edge two pixels thick around the whole panel, nothing inside | white at the two ends of the run, nothing between | **is the LED map the shape this module assumes.** A closed rectangle means row-major `y*width+x` with x across and y down; anything else means the panel is wired or mapped differently. On a strip the two ends answer the same question — whether the run the controller draws is the run this module believes |
| `quarters` | four blocks: red top-left, green top-right, blue bottom-left, white bottom-right | four bands along the run in the same order, twelve pixels each on a 48-LED strip | **which way up and which way round.** An upside-down picture puts red in the bottom right and names it; a mirrored one swaps red and green. A strip names the end it is wrong at |

This is the only instrument there is: the firmware cannot report what its pixels are (`pwr` reads 0 on a lit
panel, `/json/state` carries no per-LED colours), so a person looking is the measurement. That is why it runs
on **every light the instance drives** rather than on one — a diagnostic that refused because the install
holds two lights would stop working exactly when there is more to diagnose. `?light=` aims it at one.

The colours are chosen so that no printer state can be mistaken for one: every colour in the state table
has at least one channel unlit — printing and finished are `0,170,0`, idle and busy `210,165,0`, an error
`200,0,0`, stopped `80,80,80` — while `solid` is white at full, the brightest thing the panel can draw.

Each picture goes out as pixels over DDP on the same writer, with the same establishing body, the same
once-a-second repeat and the same hand-back to the steady frame the word uses; the segment's name is the
pattern's, so `/json/state` says which picture the panel is holding. It answers `202` with the picture, one
entry per light in `lights` — the controller, the light's name, and the sentence above in that light's own
words — and how long it holds. It refuses with a code in these cases:

| answer | code | why |
| --- | --- | --- |
| 400 | `PREVIEW_PATTERN_UNKNOWN` | no picture of that name; the sentence lists `solid`, `border` and `quarters` |
| 400 | `PREVIEW_HOLD_OUT_OF_RANGE` | `hold` is not a whole number of seconds, is not positive, or is above 300 |
| 503 | `PREVIEW_NOT_RUNNING` | the module is driving no light at all yet |
| 404 | `CONTROLLER_NOT_FOUND` | `?light=` names a controller this instance drives nothing on |
| 409 | `PREVIEW_BORROWED_SEGMENT` | no light took it: the one it was aimed at is a borrowed segment of somebody else's controller |
| 409 | `PREVIEW_NOT_DRAWABLE` | no light took it: the controller has not yet said how many pixels it drives |

## Holding a preview for longer than a minute

Both preview routes take `hold`: how many seconds the picture keeps the light, as a whole number.

```bash
curl -s -X POST "$base/preview-finished?hold=180"     # the word, for three minutes
curl -s -X POST "$base/preview-pattern?pattern=border&hold=180"
```

Absent, it is the finished event's own 60 seconds — what both routes did before the parameter existed.
The ceiling is 300, which is above the three minutes a person needs to walk to the panel and look without
racing the clock, and far enough below an hour that a mistyped number cannot leave the light off its
printer's picture for long. Past the ceiling the request is refused, not clamped: a hold quietly shortened
is a look the caller was told he had and did not get. The answer's `holds_seconds` is what the light is
actually holding for.

The binding routes refuse with codes of their own, and a caller branches on the code rather than on the
sentence:

| answer | code | why |
| --- | --- | --- |
| 404 | `BINDING_NOT_FOUND` | no binding of that printer; the detail names the id and points at `GET /bindings` |
| 409 | `BINDING_REFUSED` | the set that change would make breaks a rule, names a printer the host does not hold, or names an address that is not a controller. Nothing was changed |
| 400 | `BINDING_BODY_INVALID` | the body is not one binding: not JSON, or a key this module does not read |
| 404 | `CONTROLLER_NOT_FOUND` | no binding drives that controller |
| 503 | `PRINTERS_UNAVAILABLE` | the host's printer list could not be read, so no printer could be resolved |

## What you need

- **A Prusa Connect instance you run yourself**, because that server is what launches a service module.
  The module is a child process of that server, not a standalone program.
- **A WLED controller on the same network**, flashed and joined to your Wi-Fi, with a known address.
  A strip and a matrix are the same to this code; the difference is geometry it reads from the device.
- **No Go on your machine.** Builds happen in a container. This host has no Go toolchain and installing
  one is not part of the plan — the container is the whole toolchain.

## Building and testing

The SDK this module is built against is not published yet. `go.mod` replaces it with `./.core/sdk`, which
is the path the marketplace's own builder creates for itself: it checks Prusa Connect out at `.core/`
inside this tree and builds there. A sibling path like `../prusa-connect/sdk` would work on one machine
and find nothing on CI, so the path in `go.mod` is the marketplace's, not a convenience.

Locally, make that checkout once, as a **relative** symlink so the same `.core` resolves both on the host
and under a container mount of the parent directory:

```bash
ln -s ../prusa-connect .core
```

`.core` is gitignored: it is another repository's tree. Then wrap the container command once per shell:

```bash
build() {
  docker run --rm --user $(id -u):$(id -g) \
    -e HOME=/tmp -e GOCACHE=/tmp/gocache -e GOPATH=/tmp/gopath -e CGO_ENABLED=0 \
    -v ~/prusa:/work -w /work/wled-printer-status \
    golang:1.26.11 "$@"
}
```

The parent directory is mounted, which is what makes `../prusa-connect` resolve inside the container.
`CGO_ENABLED=0` is not optional: the binary has to be static, because it ends up running inside the
Prusa Connect container rather than on your host.

The four gates, all of which must be clean before anything is committed:

```bash
build go build ./...
build go vet ./...
build go test ./... -count=1
build gofmt -l .
```

`gofmt -l .` prints nothing when the tree is formatted. As of this commit there are **229 tests** — 123 in
the root package, 23 in `internal/printer`, 20 in `internal/render`, 38 in `internal/wled`, 14 in
`internal/ddp`, 6 in `internal/matrix` and 5 in `internal/text` — and they pass without touching a network
or a real controller:
the light and the printer are both faked in the tests that need one, and the ones about a minute-long
heartbeat or a five-minute outage move a fake clock instead of waiting. There is no `go.sum`: the only
dependency is the SDK, replaced by a directory, so nothing is fetched and there is no hash to record.

## Installing

There is an install path, and it has been walked. [`docs/installing-locally.md`](docs/installing-locally.md)
is the record of one install on a running Prusa Connect, with the commands as they were run and the output
they gave: the release directory and the checksums a release has to carry, the ownership the core needs
before it can read the files, the `module add` that writes the declaration, and the restart the install owes
afterwards. Start there rather than here — and read its §4a first, which is what a person installing this
release needs and every earlier run in that record did not: the manifest declares no settings, so `-settings`
is left off.

`install.sh` in this tree is the project generator's script and is stale: it wants a Go toolchain on your
machine and a Prusa Connect checkout at a fixed path. Do not read it as documentation of how this module
installs.

A module is installed through the server's own command, which verifies the release, records its digest,
and writes the declaration:

```bash
prusa-connect module add github.com/nothingmn/wled-printer-status@0.1.0 -from dist
```

Without `-approve` the declaration is written switched off, and the command prints what the module asked
for so you can read it first. This module asks for `observe`, which is what lets it read one printer and
follow that printer's event stream as itself, with nobody behind the call, and for one writable
directory — its own configuration store, one versioned JSON document holding the bindings and the five
tables the Colours, Events, Layout, Brightness and Overview screens edit, which is what lets a change made
through any of its routes survive a restart and an upgrade. It declares **two** outbound
connections, both from this process: port 80, where the status colour and the progress are written as JSON
state, and **UDP port 4048**, where pixels go over DDP once a second while a panel or a word holds one. Both
were once declared against the setting the address was typed into; a manifest with no settings has nothing to
point at, so the two controllers this release is proven against are named in the block and the note beside it
says what that costs.

## Binding a printer to a light

A binding is one printer, one controller, and the segment of that controller it is shown on. The
**segment** is the unit, not the controller: a controller may carry as many bindings as it has segments it
is willing to give up, and a printer may sit on several lights at once — it is the segment, or the whole of
a controller, that belongs to one binding. Two bindings on one segment would overwrite each other on every
frame and neither log line would say so, so the module refuses that rather than leaving it to be
discovered — and a binding driving the whole of a controller leaves no segment for anything else on it.

That replaces the older rule of one controller per printer. It is worth knowing why the older one was
there: two writers to one controller do interleave, because WLED has one small web server and promises
nothing about concurrent writes. What answers that now is one client per controller with one gate in
front of it — writes to one box take turns — while each binding still decides its own frame.

The address is **typed in, not discovered**. mDNS does not reach a container on a Docker bridge until the
host is told to carry it there — [`docs/mdns-in-docker.md`](docs/mdns-in-docker.md) is that change, one
setting on the host, and the browse it makes possible has been measured working from inside the module's
own container. What the module does not do yet is browse for lights itself; the address you write is the
one it uses.

### Two printers, two controllers: the Bindings screen

A binding is written on the module's own **Bindings** screen, which is served from `GET/POST /bindings` and
stored in the module's own configuration document — one printer, one controller, one segment. Nothing about
it is written in `module.yaml` any more. The same two bindings, as the route carries them:

```json
[{"printer": "Hermes MK4", "controller": "http://10.0.0.136"},
 {"printer": "Mini", "controller": "http://10.0.0.136", "segment": 3}]
```

Each line carries `printer` (the device id or the name an operator gave it), `controller` (a base URL), and
optionally `segment` and `brightness`. A line that leaves `segment` out drives the whole controller; a line
that names one borrows it and may carry no brightness at all, because a brightness is controller-wide.

The four keys that used to sit beside it — `controller_address`, `printer`, `brightness` and `segment` — are
gone from the manifest with the rest of the settings. They are read once, from an install that carried them,
as the migration source this section's rules then apply to; a list wins over them there, and they are never
merged into a list, because half a list beside one address would be two answers to one question.

A list this module cannot run refuses the start, and the sentence names the line and the field —
`bindings[1]: json: unknown field "controler"`, or `binding 2 drives segment 3 of http://10.0.0.136, which
binding 1 already drives`. That message is on the instance, where a person is already looking.

### The device id question

A printer is named by the device id the server minted for it, which is a GUID, and no text field offers a
list of them — the Bindings screen's picker does. Nobody types one: read them from the module itself.

```bash
curl -s http://localhost:8080/api/v1/modules/com.github.aux.nothingmn.wled-printer-status/printers
{"count":3,"total":3,"printers":[
  {"id":"07e893bb-b922-4ff0-9ad3-061b957400c3","name":"Hermes MK4","model":"MK4","connected":true,
   "bound":{"controller":"http://10.0.0.136","segment":-1,"whole":true}},
  {"id":"1c0f...","name":"Mini","model":"MINI","connected":true},
  …]}
```

`bound` is the first binding already showing that printer, and `bound_count` is how many there are, so a
choice can say "already on two lights" rather than believing a printer in two rooms is one row. This is the
answer to "where would someone get that GUID": from the module, not from memory.

### Changing bindings while it runs

The module serves the set it is running, and edits it:

| request | what it does |
| --- | --- |
| `GET /bindings` | the set in force, in the operator's order, with printer names read from the host |
| `GET /bindings/{id}` | one binding, by its own id |
| `POST /bindings` | `{"printer": "Mini", "controller": "http://10.0.0.253", "segment": 2}` |
| `PUT /bindings/{id}` | the same body, for a binding that exists; the id in the path is its name |
| `DELETE /bindings/{id}` | stop showing that binding, and give a borrowed segment back |
| `GET /printers` | the host's printers, each with the binding that already shows it and how many there are |
| `GET /controllers` | the same set grouped by controller, with the segments each gives up |

A binding's `id` is its name, and it is minted once and never changed. The first binding of a printer is
named by the printer's device id — so an install that has been running answers at the path it always did —
and a second binding of the same printer takes `<device id>@<light>:<segment>`, such as
`07e893bb-b922-4ff0-9ad3-061b957400c3@10.0.0.253:whole`. One printer on several lights is what multi-room
is; the same segment of the same controller twice is still refused, and the refusal names the binding that
got there first.

Every write takes effect **at once**: the watch is stopped — waiting for a borrowed segment to be given
back — and started again on the new set, so no restart and no reinstall is needed. It is also **kept**:
the set is written into this module's own configuration store, the same `config.json` the colour, moment,
layout, brightness-window and pause tables live in, and that is what the next start reads. Every write's
answer carries `"persisted": true` and says where it went.

That makes a store the module cannot reach a **refusal** and not a footnote. A module granted no data
directory, a directory that will not take the write, or a document written by a newer release answers
`503 STORE_UNAVAILABLE` — and the set that was running is running still, rather than a change that takes
effect now and disappears at the next restart. `GET /bindings` answers `"persisted"` for the same fact.

A refusal means nothing changed: the set that was running is running still. The whole watch is rebuilt on
every change, so a borrowed segment beside the one you changed is given back and borrowed again — a
flicker of the module's own frame, not a binding that was disturbed.

`GET /controllers` has no create, change or delete. A controller has no separate existence here: it
appears when the first binding names it and is gone when the last one leaves, so those are the binding
routes, and a second way to write the same thing would be a second thing to be wrong.

### Brightness that follows the time of day

The fixed `brightness` is one of the two things an operator asked for; the other is a brightness that
follows the clock. It is a list of windows, written on the module's own **Brightness** screen (`POST
/windows`, `PUT/DELETE /windows/{index}`), and a `timezone` the windows are judged in. The shape the route
carries is:

```json
[{"from": "20:00", "to": "07:00", "brightness": 40},
 {"from": "09:00", "to": "17:00", "brightness": 200, "days": ["sat", "sun"]}]
```

The zone is kept in the same document, beside the windows and not inside each one: the windows are judged in
one zone, and a zone per window would be several answers to one question. It is set on the module's
**Overview** screen, through `GET /timezone` and `PUT /timezone`, and it is an IANA name such as
`America/Denver`. An install that has never had one filled in gets it from the browser on the first page load
that finds none stored — once, and never again — and an empty or unloadable name is refused rather than
stored, so a zone is never cleared by accident.

Each window carries `from` and `to` as `HH:MM`, a `brightness` 0–255, and optionally `days` (`mon`
through `sun`; absent or empty covers every day). The rules, in the order a person reads them:

- **`from` is inside the window and `to` is outside it.** `20:00`–`07:00` is in force at 20:00 and over
  at 07:00, not at 07:01.
- **A window may wrap midnight.** That is the evening window every operator writes first.
- **The first window that contains the moment decides**, in the order you wrote them.
- **Where no window covers the moment, the fixed `brightness` stands.** A gap between two windows is
  therefore the setting this module has always had, and a light is never left dark between windows.
- **A window with equal ends is refused** rather than read as a whole day or as nothing: which one was
  meant is not something the module can guess.
- **`days` names the calendar day the moment falls on, in `timezone`.** A window that wraps midnight with
  `days: ["fri"]` covers Friday evening to midnight and not the small hours of Saturday — name both to
  have both.
- **A borrowed segment never follows the schedule.** It writes no brightness at all, here or at any
  window's edge, because a brightness is controller-wide and would move every segment on that
  controller.

**The zone is not optional, and it is not the container's clock.** This process runs inside the Prusa
Connect container, whose clock is UTC; an operator in Denver is seven hours behind it and shifts again
for daylight saving. A schedule read against the container's clock dims the light at lunchtime and
leaves it bright all evening, and nothing on the instance would say which clock was read. So a schedule
written with an empty or unloadable `timezone` **refuses the start** with a sentence naming both settings
— never a silent fallback to UTC:

```
brightness_schedule is written and timezone is empty: name the zone its windows are judged in, as an
IANA name such as America/Denver. This process runs inside the Prusa Connect container, whose own clock
is almost always UTC, so a schedule with no zone of its own would fire seven hours away from the
operator's evening and nothing in the log would say so
```

The zone database is compiled into the binary, so a name is judged against the IANA data the release
shipped with rather than against whatever the container's image carries.

The brightness written is one per change and no more: the module writes the value in force when it
starts, then a window's value when that window's edge is crossed — within a second or two of the edge,
because the writer turns with the driver's own tick rather than on a print event — and nothing while the
value stands. A value that failed to reach the controller is asked for again on the ordinary doubling
retry, and the log carries one line per change naming the window and the zone:

```
wled-printer-status: the light's brightness is 40, the 20:00-07:00 window of America/Denver, where it is 20:00 MST
```

An install with no window written behaves exactly as it did before windows existed: the fixed brightness is
written once at start and never moved.

### The four settings the store was seeded from

`controller_address` is empty and unused when a binding list is written, and it is no longer required for
that reason. Written alone, it behaves as it always has: one printer, one controller, and a refusal if
neither is named. `printer` names the printer to watch, by the server's device id or by the name an operator
gave it; left empty it takes the only printer the instance holds, and refuses to choose between several
rather than put somebody's strip on the wrong machine. `brightness` is what the module writes to the
controller once at start and does not move again — unless a brightness window covers the moment, when it is
the value that stands wherever none does (see above).

**These four are not in `module.yaml`.** They were, until this release, and they are still read by a start
for exactly one purpose: an instance installed from a release that carried them seeds its store from them on
its first start and never reads them again. A fresh install sends none of them, starts on the module's own
defaults, and is configured on its own screens.

`segment` names one segment of a controller that carries several, and the module then drives that segment
and leaves the rest of the controller alone. It reads the segment once at start — its run, its colour, its
effect and its name — draws inside that run while it runs, and writes all of it back when it stops, so a
controller that remembers what was put on it is given back what it had. In this mode it writes **no**
brightness and **no** power, because both are controller-wide and would move every segment at once, and it
never writes a second segment. The default, -1, drives the whole strip, which is what this module did
before the setting existed. On a controller that reports a matrix the setting is refused and the log says
so: a panel's body is two rectangles covering the whole of it, and two rectangles on a controller carrying
somebody else's segments would be two of theirs.

A segment is borrowed and not taken, and that has one limit worth knowing: the captured segment is held in
the module's memory and nowhere else. A stop the module can see — the host stopping it, a second
configuration, an interrupt — gives the segment back. A process killed without warning, a power cut, or a
container stopped hard cannot, and the segment then stays as the module left it until something writes it
again.

What you write for the address is what you would put in a browser: `http://10.0.0.136`, no path, no
trailing slash needed.

## Finding your controller from Docker

Automatic discovery — mDNS on UDP 5353 — does not cross into a container on a Docker bridge by default,
in either direction. That is a **host** setting rather than a container one: the host has to be told to
carry the traffic, and once it is, a browse from inside the container works with the module still on its
own bridge. [`docs/mdns-in-docker.md`](docs/mdns-in-docker.md) is that setting, the check, the failure
modes and the one-line undo.

Both states were measured on the operator's host rather than assumed:

| what was tried | result |
| --- | --- |
| mDNS browse from inside the container, stock host | **0** controllers |
| `net.ipv4.conf.*.mc_forwarding` set to 1 | **impossible here** — the file is read-only, and the kernel refuses a multicast-routing socket |
| mDNS browse from inside the container, after `enable-reflector=yes` on the host | **19** controllers, by name, address and `mac` |
| the same browse on a bridge created *after* that change and named in no config | **19** controllers |
| `GET /json/info` from inside the container, 10 rounds | **10 of 10** answered |

A browse that returns a light's name but no address is the reflector answering from the host's cache:
the browse has to ask for SRV and TXT after the PTR, and then for the host's address. The document above
has a small program that does that in the right order.

What has **not** changed is this module: it still takes a typed address and does not browse. The sentence
on the Lights screen says what discovery needs rather than promising it is coming, because the container
side is the next piece of work.

Giving the container the host's network namespace used to be the only way to browse from it, and is no
longer needed for that:

```yaml
# docker-compose.override.yml — was needed for a discovery browse, is not any more
services:
  prusa_connect:
    network_mode: host
```

It is still not free if you use it for something else: the container shares the host's interfaces, it can
bind a host port, and the `ports:` entries in your compose file stop mapping anything.

Once you have a candidate address, confirm you have the right device before trusting it, from anywhere on
the network:

```bash
curl -s http://10.0.0.136/json/info
```

Four fields settle it:

- **`mac`** — unique per board, and unchanged across a DHCP lease. This is the field that identifies the
  box; everything else can change.
- **`ver`** — the firmware version, which is what decides whether text and matrix features exist at all.
  The effects a controller can show are read from the device rather than assumed, but knowing the version
  up front saves a surprise.
- **`name`** — whatever somebody typed into the WLED UI. Two controllers in one house can carry the same
  name, so this confirms a hunch and never identifies a device.
- **`leds.count`** — how many pixels you are driving. This is the number that decides what the light can
  show: a 1D strip cannot display text, however new its firmware.

## Speed, and the budget it implies

A 48-LED strip answered a request in about **53 ms** and a 256×64 panel — 16,384 pixels — in about
**28 ms**. Both are fast enough that the limit is not the network; it is WLED's own web server, one small
task on a microcontroller, plus the plain fact that a light being updated forty times a second is a light
nobody can read.

**1 to 5 writes a second is the comfortable band.** Nothing should stream. Fades belong in a write's
`transition` field, done by the controller, not in a run of writes from this module.

The JSON side is cheap now that a frame is one request: a steady picture costs one write when it changes
and one a minute when it does not. What is **not** cheap is one colour per LED — a full 16,384-pixel
repaint in that shape is 19 requests at an 8 KiB body budget, which is fine at one frame every few seconds
and wrong for animation. That is one of the reasons the word is not drawn that way: this module has no
per-LED write at all, and the picture goes over DDP instead, where the same frame is 35 datagrams and never
touches the controller's web server. The event's own rate is chosen against the controller rather than
against this budget — one repeat a second for a still picture, so that it outlives WLED's 2500 ms realtime
timeout — and it is still not a stream: the bytes never change.

**A printing panel pays that rate for the length of the print.** A frame with a bar and text in it is
pixels, and pixels are drawn only in realtime, so the composed picture is pushed again once a second for as
long as the job runs — 35 datagrams and about 50 kB a second, on the panel's own WiFi, for however many
hours the print takes. That is the price of the feature and it is stated here rather than discovered on a
meter: three glyphs of text are not expressible as segment geometry on this firmware (AGENTS.md §5.4), and
a picture that is not repeated is a picture that vanishes two and a half seconds after it is drawn. The
JSON side stays at one write per change of picture — the establishing body goes out once when the composed
picture takes the panel and the plain frame once when it gives it back — so the controller's web server
sees exactly what it saw before this feature existed.

## What it deliberately will not do

- **Fleets.** A list of bindings is not a fleet: each line names one machine and one segment, there is no
  discovery, and nothing is chosen for you.
- **Two printers on one segment, or one printer on two.** A segment is shown by one binding and a printer
  by one binding, and the module refuses either way round rather than letting two frames undo each other.
- **Priorities.** If a finished print and an overheating hotend both want the light, there is no
  arbitration, because nobody has decided what wins. That is one of the open questions in the
  requirements.
- **WLED's live mode, presets and playlists.** The pixel transport is the one exception and it is the
  reason the word exists: DDP puts the panel into the controller's own live mode for the minute the word
  holds, which is what a datagram does by definition. It is not a JSON `{"live": true}` write — the client
  never sends that, and never touches a preset or a playlist. When the hold ends the module stops pushing,
  the controller drops live mode 2500 ms later, and its own segments are drawn again.
- **Prusa's cloud.** It makes no outbound call to Prusa's servers; it reads what the Connect instance in
  front of it knows.

## Licence and where the requirements live

MIT — see [`LICENSE`](LICENSE), and `module.yaml` declares the same.

The product requirements are **locked** at
[`docs/product-requirements.md`](docs/product-requirements.md): the operator approved them, and the
eight screens they specify, on 2026-11-19, and that file — not this README — is the place to argue
with what the light should show. The screens themselves are design mockups in
[`docs/design/v2/`](docs/design/v2/README.md).

What they look like is a separate document, and it is the one a restyle is judged against:
[`docs/style-guide.md`](docs/style-guide.md), the operator's own style guide for this module, derived
from his instance's printer page in both themes. The module renders *inside* that page, so the guide's
rule outranks its own palette — every colour comes from the host's CSS custom properties, mapped once in
[`ui/src/tokens.css`](ui/src/tokens.css), and every shape in every screen is one of the eight primitives
in [`ui/src/ui/`](ui/src/ui/). A component that writes a hex value is the defect the guide names.
