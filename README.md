# wled-printer-status

A Prusa Connect module that turns a WLED light into a status display for one 3D printer.

It runs as a service module: a small Go program the Prusa Connect server launches, which reads one
printer's state from that server and paints a light — a strip along a desk, or a panel on a wall — so
that somebody walking past can tell what the printer is doing without opening an app.

## Status

**This module is early, and it has never been run against a live host or a live controller.** What is
written is wired: the program the server launches reads one printer, decides what a 1D strip should
show, and writes that to the controller named in its settings. `GET /status` answers what it currently
thinks — the printer, how old its reading is, the frame it has decided, and what the last write to the
light did — so a person can read the decision without walking over to the light.

So: it is a working first light and not a finished product. Colour and a progress bar are the part that
is decided. Animations, the panel's richer display, a brightness schedule and the composer are still
open, and none of them is here; the open ones are written down in
[`docs/product-requirements.md`](docs/product-requirements.md). Nothing has been proven on real
hardware: there is no record of this module driving a strip in somebody's room.

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
  and writes power, brightness, colour, a rectangle, or one colour per LED. Three tiers, tried in order,
  so that a light which cannot animate can still show a colour and a light which cannot draw a rectangle
  can still be given individual pixels.

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
remembers. Two sub-decisions are **provisional** and marked as such in the source: the fill rounds down
rather than to the nearest LED, and the LEDs past the fill are off. `main.go` calls it once a second, and
`drive.go` writes the frame it returns to the controller — so this is the rule the light is showing, not
a rule waiting for a caller.

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

`gofmt -l .` prints nothing when the tree is formatted. As of this commit there are **85 tests** — 18 in
the root package, 20 in `internal/printer`, 14 in `internal/render`, 33 in `internal/wled` — and they pass
without touching a network or a real controller: the light and the printer are both faked in the tests
that need one. There is no `go.sum`: the only dependency is the SDK, replaced by a directory, so nothing
is fetched and there is no hash to record.

## Installing

There is no install path worth following today. `install.sh` in this tree is the project generator's
script and is stale: it wants a Go toolchain on your machine and a Prusa Connect checkout at a fixed
path. Do not read it as documentation of how this module installs.

When there is a release, a module is installed through the server's own command, which verifies the
release, records its digest, and writes the declaration:

```bash
prusa-connect module add github.com/nothingmn/wled-printer-status@0.1.0 -from dist
```

Without `-approve` the declaration is written switched off, and the command prints what the module asked
for so you can read it first. This module asks for `observe`, which is what lets it read one printer and
follow that printer's event stream as itself, with nobody behind the call. It declares one outbound
connection: the WLED controller named in `controller_address`, on port 80, from this process.

## Binding a printer to a light

The rule is one controller per printer, and at most one printer per controller. Not because it is
convenient, but because two writers to one controller interleave: WLED has one small web server and
promises nothing about concurrent writes, so two printers sharing a strip produce a fight, and a module's
mutex only serialises its own calls. A progress bar belonging to two jobs is not a bar.

The address is **typed in, not discovered**. Automatic discovery does not work from inside a container
on Docker's default bridge — see below — and the module does not attempt it.

The address is the setting `controller_address`, and it is required: the module refuses to start until one
is written and says so on the instance, rather than running and never mentioning why the light is dark.
`printer` names the printer to watch, by the server's device id or by the name an operator gave it; left
empty it takes the only printer the instance holds, and refuses to choose between several rather than
put somebody's strip on the wrong machine. `brightness` is what the module writes to the controller once at
start and does not move again.

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

The floor tier — one colour per LED — is the expensive one: a full 16,384-pixel repaint is **19 requests**
at the default 8 KiB body budget. That is fine at one frame every few seconds and wrong for animation,
which is why it is the fallback and not the default.

## What it deliberately will not do

- **Fleets.** One printer, one light. The printer half takes a single device id and has no list in it.
- **More than one printer per light, or more than one light per printer.** The write model and the budget
  are built for one writer.
- **Priorities.** If a finished print and an overheating hotend both want the light, there is no
  arbitration, because nobody has decided what wins. That is one of the open questions in the
  requirements.
- **WLED's realtime mode, presets and playlists.** The client never writes `{"live":true}` — it blanks the
  strip — and never touches a preset or a playlist.
- **Prusa's cloud.** It makes no outbound call to Prusa's servers; it reads what the Connect instance in
  front of it knows.

## Licence and where the requirements live

MIT — see [`LICENSE`](LICENSE), and `module.yaml` declares the same.

The product requirements are a draft at [`docs/product-requirements.md`](docs/product-requirements.md).
Nothing in it is locked; every decision the operator has not made is marked open. That file, not this
README, is the place to argue with what the light should show.
