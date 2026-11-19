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
designed — the panel's richer display, and the composer. The open ones are written down in
[`docs/product-requirements.md`](docs/product-requirements.md).

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
light at once. Those are product decisions, not coding ones, and the ones still open are written down
as open questions in [`docs/product-requirements.md`](docs/product-requirements.md).

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

## Looking at the finished word without waiting for a print

A person who wants to know what his panel will do when a print ends should not have to run a print to find
out. The module serves one request for exactly that:

```bash
curl -s -X POST http://localhost:8080/api/v1/modules/com.github.aux.nothingmn.wled-printer-status/preview-finished
{"event":"FINISHED","controller":"http://10.0.0.136","colour":{"r":0,"g":170,"b":0},"scale":5,"holds_seconds":60}
```

`POST /preview-finished` fires the finished event now: the same word, in the same colour, for the same
minute a completed print gets, so what you look at is what the light will do rather than a rehearsal of
something else. The pixels go out on the module's own writer, behind the same gate and the same retry
window as every other write, and the steady frame takes the light back when the minute is up — the
route changes nothing about the light's ordinary picture and writes nothing to the controller itself.

It refuses, with a code and a sentence, in the three cases where the light cannot be asked for the word:

| answer | code | why |
| --- | --- | --- |
| 503 | `PREVIEW_NOT_RUNNING` | the module is not driving exactly one light yet — an instance holding several bindings has no single light to preview on |
| 409 | `PREVIEW_BORROWED_SEGMENT` | the light is one borrowed segment of a controller somebody else also drives, and an animation is not something this module puts there |
| 409 | `PREVIEW_NOT_DRAWABLE` | the controller reported no matrix (a strip), or its panel is too small for the word at the scale this build draws it |

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

| picture | what it is | what it answers |
| --- | --- | --- |
| `solid` | the whole panel in pure white | **does DDP draw at all.** Lit means the transport and the drawing work and every later rung is worth looking at; dark means the transport is the fault, and the word would have been dark for a reason that has nothing to do with the word |
| `border` | a white edge two pixels thick around the whole panel, nothing inside | **is the LED map the shape this module assumes.** A closed rectangle means row-major `y*width+x` with x across and y down; strips, a diagonal, a border in the wrong place or a border that is only half a rectangle means the panel is wired or mapped differently |
| `quarters` | four blocks: red top-left, green top-right, blue bottom-left, white bottom-right | **which way up and which way round.** An upside-down picture puts red in the bottom right and names it; a mirrored one swaps red and green |

The colours are chosen so that no printer state can be mistaken for one: every colour in the state table
has at least one channel unlit — printing and finished are `0,170,0`, idle and busy `210,165,0`, an error
`200,0,0`, stopped `80,80,80` — while `solid` is white at full, the brightest thing the panel can draw.

Each picture goes out as pixels over DDP on the same writer, with the same establishing body, the same
once-a-second repeat and the same hand-back to the steady frame the word uses; the segment's name is the
pattern's, so `/json/state` says which picture the panel is holding. It answers `202` with the picture, the
controller, the sentence above and how long it holds. It refuses with a code in these cases:

| answer | code | why |
| --- | --- | --- |
| 400 | `PREVIEW_PATTERN_UNKNOWN` | no picture of that name; the sentence lists `solid`, `border` and `quarters` |
| 400 | `PREVIEW_HOLD_OUT_OF_RANGE` | `hold` is not a whole number of seconds, is not positive, or is above 300 |
| 503 | `PREVIEW_NOT_RUNNING` | the module is not driving exactly one light yet |
| 409 | `PREVIEW_BORROWED_SEGMENT` | the light is one borrowed segment of somebody else's controller |
| 409 | `PREVIEW_NOT_DRAWABLE` | the controller reported no matrix, so it is a strip |

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

`gofmt -l .` prints nothing when the tree is formatted. As of this commit there are **214 tests** — 108 in
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
before it can read the files, the settings file, the `module add` that writes the declaration, and the
restart the install owes afterwards. Start there rather than here.

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
follow that printer's event stream as itself, with nobody behind the call. It declares **two** outbound
connections, both to the controller named in `controller_address` and both from this process: port 80,
where the status colour and the progress are written as JSON state, and **UDP port 4048**, where the pixels
of the finished word go over DDP once a second for the minute the word holds.

## Binding a printer to a light

A binding is one printer, one controller, and the segment of that controller it is shown on. The
**segment** is the unit, not the controller: a controller may carry as many bindings as it has segments it
is willing to give up, and a printer appears in at most one binding. Two bindings on one segment would
overwrite each other on every frame and neither log line would say so, so the module refuses that rather
than leaving it to be discovered — and a binding driving the whole of a controller leaves no segment for
anything else on it.

That replaces the older rule of one controller per printer. It is worth knowing why the older one was
there: two writers to one controller do interleave, because WLED has one small web server and promises
nothing about concurrent writes. What answers that now is one client per controller with one gate in
front of it — writes to one box take turns — while each binding still decides its own frame.

The address is **typed in, not discovered**. Automatic discovery does not work from inside a container
on Docker's default bridge — see below — and the module does not attempt it.

### Two printers, two controllers: the `bindings` setting

One printer is one line of the `bindings` setting, which is a JSON list. This is the whole of it — an
instance showing `Hermes MK4` on the panel and `Mini` on segment 3 of the panel beside it:

```yaml
resources:
  sink:
    wled-printer-status:
      config:
        bindings:
          - printer: Hermes MK4
            controller: http://10.0.0.136
          - printer: Mini
            controller: http://10.0.0.136
            segment: 3
```

On the settings pane the same thing is one field, because a manifest declares nine value shapes and a list
is the largest of them: paste it as JSON in the text area for `bindings`.

```json
[{"printer": "Hermes MK4", "controller": "http://10.0.0.136"},
 {"printer": "Mini", "controller": "http://10.0.0.136", "segment": 3}]
```

Each line carries `printer` (the device id or the name an operator gave it), `controller` (a base URL), and
optionally `segment` and `brightness`. A line that leaves `segment` out drives the whole controller; a line
that names one borrows it and may carry no brightness at all, because a brightness is controller-wide.

`bindings` **wins** over `controller_address`, `printer`, `brightness` and `segment` when it is written. The
four older keys stay in the manifest because an install that has been running for a week carries them in
its config block, and they are not merged into a list: half a list beside one address would be two answers
to one question.

A list this module cannot run refuses the start, and the sentence names the line and the field —
`bindings[1]: json: unknown field "controler"`, or `binding 2 drives segment 3 of http://10.0.0.136, which
binding 1 already drives`. That message is on the instance, where a person is already looking.

### The device id question

A printer is named by the device id the server minted for it, which is a GUID, and nothing in a settings
form offers a list of them. Nobody types one: read them from the module itself.

```bash
curl -s http://localhost:8080/api/v1/modules/com.github.aux.nothingmn.wled-printer-status/printers
{"count":3,"total":3,"printers":[
  {"id":"07e893bb-b922-4ff0-9ad3-061b957400c3","name":"Hermes MK4","model":"MK4","connected":true,
   "bound":{"controller":"http://10.0.0.136","segment":-1,"whole":true}},
  {"id":"1c0f...","name":"Mini","model":"MINI","connected":true},
  …]}
```

`bound` is the binding already showing that printer, so a choice can leave it out. This is the answer to
"where would someone get that GUID": from the module, not from memory.

### Changing bindings while it runs

The module serves the set it is running, and edits it:

| request | what it does |
| --- | --- |
| `GET /bindings` | the set in force, in the operator's order, with printer names read from the host |
| `GET /bindings/{id}` | one binding, by the printer's device id |
| `POST /bindings` | `{"printer": "Mini", "controller": "http://10.0.0.253", "segment": 2}` |
| `PUT /bindings/{id}` | the same body, for a binding that exists; the printer in the path is its name |
| `DELETE /bindings/{id}` | stop showing one printer, and give a borrowed segment back |
| `GET /printers` | the host's printers, each with the binding that already shows it |
| `GET /controllers` | the same set grouped by controller, with the segments each gives up |

Every write takes effect **at once**: the watch is stopped — waiting for a borrowed segment to be given
back — and started again on the new set, so no restart and no reinstall is needed. What it does **not** do
is save: the settings belong to the host, a module has no call that writes them, and a restart or a save
of the settings pane puts back what the settings say. Every write's answer carries `"persisted": false`
and says so in words.

A refusal means nothing changed: the set that was running is running still. The whole watch is rebuilt on
every change, so a borrowed segment beside the one you changed is given back and borrowed again — a
flicker of the module's own frame, not a binding that was disturbed.

`GET /controllers` has no create, change or delete. A controller has no separate existence here: it
appears when the first binding names it and is gone when the last one leaves, so those are the binding
routes, and a second way to write the same thing would be a second thing to be wrong.

### Brightness that follows the time of day

The fixed `brightness` is one of the two things an operator asked for; the other is a brightness that
follows the clock. It is two settings: `brightness_schedule`, a list of windows
pasted into the settings pane's JSON box, and `timezone`, the zone those windows are read in.

```json
[{"from": "20:00", "to": "07:00", "brightness": 40},
 {"from": "09:00", "to": "17:00", "brightness": 200, "days": ["sat", "sun"]}]
```

```yaml
      config:
        timezone: America/Denver
        brightness: 128
        brightness_schedule:
          - from: "20:00"
            to: "07:00"
            brightness: 40
```

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

An install with no `brightness_schedule` behaves exactly as it did before this setting existed: the
fixed brightness is written once at start and never moved.

### The four older settings

`controller_address` is empty and unused when `bindings` is written, and it is no longer required for that
reason. Written alone, it behaves as it always has: the module refuses to start with no address and no
list and says so on the instance, rather than running and never mentioning why the light is dark.
`printer` names the printer to watch, by the server's device id or by the name an operator gave it; left
empty it takes the only printer the instance holds, and refuses to choose between several rather than
put somebody's strip on the wrong machine. `brightness` is what the module writes to the controller once at
start and does not move again — unless a `brightness_schedule` is written, where it is the value that
stands wherever no window covers the moment (see above).

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

Automatic discovery — mDNS on UDP 5353 — does not cross into a container on the default bridge, in
either direction. This was measured rather than assumed:

| what was tried | result |
| --- | --- |
| mDNS browse from inside the container | **0** controllers |
| a raw 15 s listen on UDP 5353 inside the container | **0** packets |
| the same 12 s browse, host networking | **4** of 15 broadcasting controllers |
| `GET /json/info` from inside the container, 10 rounds | **10 of 10** answered |

Discovery finds nothing while plain unicast HTTP to the same address on the same network works every
time. That is why the module takes a typed address: the reliable path is the one that already works.

If you want the browse anyway — to find the address in the first place — the container has to share the
host's network namespace:

```yaml
# docker-compose.override.yml — for a discovery browse, not for the module
services:
  prusa_connect:
    network_mode: host
```

That is not free. Host networking means the container shares the host's interfaces: it sees every port
the host has, it can bind a host port, and the `ports:` entries in your compose file stop mapping
anything — whatever reached the container by published port now has to reach the host's own address. Use
it to find a controller, then go back to the bridge and type the address in.

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

The product requirements are a draft at [`docs/product-requirements.md`](docs/product-requirements.md).
Nothing in it is locked; every decision the operator has not made is marked open. That file, not this
README, is the place to argue with what the light should show.
