# Wled Printer Status

A Prusa Connect module that is one Go process serving one route. It
draws nothing in the app.
`make new-module` wrote this skeleton.

Its namespace is `com.github.aux.nothingmn.wled-printer-status`, derived by the host from the repository
address `github.com/nothingmn/wled-printer-status`. You never write the namespace: every address the module
answers on is built from it.

| path | purpose |
| --- | --- |
| `module.yaml` | the manifest: the kind and the version |
| `install.sh` | build, install and see it. The one command below |
| `CHANGELOG.md` | what an operator sees change when they upgrade this module |
| `.vscode/extensions.json` | the editor extensions for the languages in here |
| `main.go` | the process: the handshake, the lifecycle set, the settings and the route |
| `faults.go` | every code this module refuses with, each documented once |
| `main_test.go` | the route answering, the refusal carrying its code, and the manifest's default |
| `openapi.json` | the fragment describing that route, with every path relative to the mount |
| `internal/wled` | the HTTP client for the light: it reads the controller and writes power, brightness and colour. Not wired in yet |
| `internal/printer` | the printer-facing half: one printer's state, how old it is, and whether the host still has it. Not wired in yet |

## Building

The sdk is not published yet, so a build takes it from a checkout of Prusa
Connect. `go.mod` names that checkout at `.core/` in this tree, which is where
the marketplace builder puts it: the builder checks Prusa Connect out there
itself and builds. A sibling path such as `../prusa-connect/sdk` would work
only on the machine this module was written on.

Locally, make the checkout once:

```bash
ln -s ../prusa-connect .core
```

The link is relative, so the same `.core` resolves on this machine and under a
container mount of the parent directory. `.core` is not committed: it is
another repository's tree, and `.gitignore` excludes it.

Go is not installed here, so the build runs in a container. It mounts the
parent directory, which is what makes `../prusa-connect` resolve inside:

```bash
docker run --rm --user $(id -u):$(id -g) \
  -e HOME=/tmp -e GOCACHE=/tmp/gocache -e GOPATH=/tmp/gopath -e CGO_ENABLED=0 \
  -v ~/prusa:/work -w /work/wled-printer-status \
  golang:1.26.11 go build ./...
```

`go vet ./...`, `go test ./...` and `gofmt -l .` run the same way. There is no
`go.sum` and none is needed: the only requirement is the sdk, replaced by a
directory, so nothing is fetched and there is no hash to record. A build with
the marketplace's `-mod=readonly` passes for that reason. Add one with
`go mod tidy` the day a published module is imported.

## Installation

```bash
sh install.sh
```

That builds this platform's binary, and installs the release with
`prusa-connect module add` into the demo's data, under `bin/wled-printer-status-demo`
in the checkout. `TARGET=app sh install.sh` installs it into the real app's
data, `bin/wled-printer-status-app`, instead. What the tools print goes to `install.log`
there, and a good run prints one line saying where the module is installed.

To see it, run this in the checkout. `-dir` is this folder, `../wled-printer-status` when
it sits beside the checkout:

```bash
go run ./cmd/new-module -start demo -dir ../wled-printer-status
```

It starts the demo on a free port with this module installed. It prints the address to open and the
account to sign in with. `-start app` starts the real app instead, which asks
for an account on its first run. Ctrl+C stops everything it started.

Paste what the command printed on your machine under this paragraph. A README
that quotes real output is one a reader can check; one that describes the output
is one they have to trust.

Run `sh install.sh` again after a change to `module.yaml` or to the process.
`module add` records the bytes it installed and the host serves nothing it
cannot verify against that record, so a rebuilt release is a new install. A
core that is already running read its declarations when it started, so it is
restarted too.

## Configuration

`options:` in `module.yaml` names every setting this module reads, each with its
type, its default and one sentence saying what it controls. Nothing here reads
an environment variable, so that list is the whole of what an operator can set.

The host applies the defaults, writes the instance's `config:` block over them
key by key, and sends the result on `plugin.configure` before the first request
arrives. A key the manifest does not name is refused before the process starts.

```yaml
resources:
  sink:
    wled-printer-status:
      config:
        message: Ready.
```

## WLED client

`internal/wled` is the only code in here that talks to a light. `package wled` gives you
a `Client` for one controller — its base URL and its own `*http.Client` — and it uses
four of WLED's own endpoints.

| call | endpoint | what it does |
| --- | --- | --- |
| `Info` | `GET /json/info` | the controller's facts: firmware version, LED count, segment limit, matrix width and height, name, UDP port, whether something is streaming into it |
| `State` | `GET /json/state` | power, brightness, and each segment's LED bounds, row bounds, colours, effect, name, effect parameters and brightness |
| `Capabilities` | `GET /json` | the effects and palettes the controller itself lists, each with the id it is written by, and each effect's fxdata line when the document carries one |
| `SetPower`, `SetBrightness`, `SetSolid`, `SetRange`, `SetRectangle`, `SetEffect`, `SetSegmentName`, `Paint` | `POST /json/state` | one small body carrying only the keys that change |

Every request is bounded at two seconds, because the host pings this module every
fifteen seconds and waits five for the answer: a call that blocked longer would cost the
process three missed pings. Calls to one controller are queued behind a mutex, which is
what WLED asks for. Failures are typed — `ErrUnreachable`, `ErrTimeout`, `ErrStatus` for a
failing HTTP status, `ErrDevice` for WLED's own `{"error":n}` — and a brightness outside
0-255, a run of LEDs with nothing in it, an empty or inverted rectangle, an effect
parameter outside a byte, and a name longer than 48 characters are all refused before
anything is sent.

A panel and a strip are the same client. `Info` reports the matrix geometry when the
controller publishes it and zero when it does not, and a segment carries its row bounds
beside its LED bounds.

**Three tiers, in the order a status should be tried.** WLED's abilities differ by
firmware, so they are read from the device and not assumed: `Capabilities` says which
effects exist, and a caller falls back only when the tier above cannot show the status.

1. **An effect, with parameters** — `SetEffect` writes the effect id and its speed,
   intensity, three custom values, three check values and palette. Text on a 2D panel is
   effect 122 ("Scrolling Text") and the string is the segment's name, written with
   `SetSegmentName`.
2. **Computed colour or geometry** — `SetSolid`, `SetRange` and `SetRectangle` put a
   colour into a run, a band or a rectangle. Every body that names LED bounds names the
   row bounds too, because WLED reads columns alone as a flat index range and folds the
   segment onto one row of the panel.
3. **Per-LED painting** — `Paint` sends one colour per LED in the segment's `i` list,
   split into as many requests as a byte budget needs (8 KiB by default, so a 16,384-pixel
   frame is nineteen requests and a 48-pixel strip is one). It is the floor: whatever a
   controller can do, it can be given individual colours, so a status that cannot be
   shown any other way is still shown.

That last tier costs something and the cost is in the package comment: the colours are
not saved across a reboot, they freeze the segment's effect until an effect is selected
again, and a body that switches a segment on and paints it at once does not work — so
`Paint` sends the LED list alone and expects a segment that is already on and at its
brightness. Painting is also the slowest way to move a pixel, which is why it is the
fallback and not the default.

It is **not wired into the module**: `main.go` imports nothing from it, and `module.yaml`
and `openapi.json` are untouched, so the route still answers with the configured message
alone. There is no product behaviour in it either — no printer state, no colour for a
paused print, no progress bar, no polling loop. Those are the module's decisions and they
have not been made.

Read paths only have been run against hardware: `Info` and `State` were called once
against the WLED panel at `http://10.0.0.136/` and answered in 16 ms and 15 ms. **No write
has ever been sent to a real controller**, and the client has not been run as part of the
module at all. Every write it can make is proven against a local test server and nothing
else.

## Printer state

`internal/printer` is the printer-facing half: one printer, tracked honestly, with nothing
about presentation in it. `package printer` gives you a `Source` for one device — you hand
it the module's session and the fleet client, and it owns the `bus.subscribe` window and
the periodic `device.get` read — and a pure reducer, `printer.Apply`, which folds a batch
of telemetry paths and one device record into a new state and needs no network to test.

The point of it is the offline case, because the host has no word for "gone". A printer
that is switched off keeps the state tree it last reported and nothing in the host dates
it, and `/state` carries ten values with no OFFLINE among them. A module that reads that
tree and nothing else will say `PRINTING, 94%, six minutes left` about a printer that went
away at 14:02, forever. So every snapshot carries the two facts that tell the difference —
whether the host still holds a session for the printer, and when anything about it
actually last changed — and it does not present the rest as present tense:
`snapshot.Connected` is false, `snapshot.Job.Live` is false, and `LiveProgress()` and
`LiveRemaining()` answer `ok=false`. The last readings are still there, with
`snapshot.LastChange` and `snapshot.Age` beside them, so "last reported 94% at 14:02" is
sayable and "printing, 94%, six minutes left" is not. A printer that stays connected but
stops reporting gets the same treatment after thirty seconds.

Three more rules it keeps. A path it has never seen is kept as it came, because the state
tree is open and a newer firmware's node is still a fact. A path the printer never wrote,
or wrote `null` to, is unknown and not zero — no invented IDLE, no invented `0%`. And a
value it cannot read is reported as a typed refusal while the last value it could read
stays.

It is **not wired into the module**: `main.go` imports nothing from it, the route still
answers with the configured message alone, and none of the display decisions have been
made. **It has never run against a live host** — a module's bus does not exist until the
module is installed and serving, so the subscription and the read are proven against fakes
only, and no window has ever carried a real printer's patches.

## Permissions

This module asks for one: `observe`, in `module.yaml`. It is the action that reads devices
and subscribes to the event stream, and it is what `internal/printer` takes — a module
reading its own printer does so with nobody behind it, so the host holds it to the
instance's own list before the authorization graph is asked. Nothing consumes the grant
yet: `main.go` imports nothing from that package, so the module still acts only for the
person the host authorized. Delete the line if you would rather not grant a capability
nothing uses.

The host takes `view_resource` on the instance before a read
and `operate_resource` before a write, and `openapi.json` states that on each
operation.

A module that does work with nobody behind it adds `permissions:` to
`module.yaml`, one line per permission saying what it lets the module do. An
operator reads that list before `module add -approve` writes it into the
declaration and grants the module what the list names. The install prints every
grant it wrote, and `module remove` takes them back.

## Errors

When the module cannot do what a request asks, it answers with an error. The
error has a `code` and a `title` in English. Every code of this module starts
with `com.github.aux.nothingmn.wled-printer-status`.

A program that calls the module should check the `code`. The title is for
people. You can change the wording of a title without breaking a caller.

`faults.go` lists every code, with what went wrong and what the caller can do
about it. Add a new code in that file.

## What the pieces are

The release is `dist`: `module.yaml` at its root and the binary
`wled-printer-status_<GOOS>_<GOARCH>` beside it. `-from dist` is what the install
reads. A `checksums.txt` is optional and verified when it is there; a release
you just built has nothing to prove against.

```bash
prusa-connect module add github.com/nothingmn/wled-printer-status@0.1.0 -from dist
prusa-connect module list
prusa-connect module remove github.com/nothingmn/wled-printer-status
```

Without `-approve` the declaration is written switched off, and the command
prints what the module asked for so an operator can read it first.

## Troubleshooting

| what you see | what it means |
| --- | --- |
| `module add` refuses the release | `dist` is not the release layout. `module.yaml` sits at its root |
| the module is listed and switched off | `module add` ran without `-approve`. Read what it printed, then switch the instance on |
| a response carries a code and an English sentence | the process refused the request. Look the code up in `faults.go` |
| a change does not show | the install was not run again. Every change is a new install |
