# Handoff — wled-printer-status

Written 2026-11-19 by the wave that installed `531e73c` on the operator's two lights — the release that
fixed a held word's hand-back, which is the last defect a live deploy measured. **Read this first,
then `AGENTS.md`, then `docs/product-requirements.md`.** This document is meant to stand alone: if
something is missing from it, the incoming agent has no other context.

The nine backend items this file used to list as *built as design, not code* are code now, deployed, and
proved on the two lights. What is genuinely open is a short list at the end of §5 — read that before
believing anything is finished.

---

## 1. What this is

A **Prusa Connect service module** in Go that reads 3D printers through the local Connect instance and
drives WLED lights: a colour for state, a fill for progress, words on a matrix panel, and one of the
light's own presets for a moment. It publishes (or is about to) to the Prusa Plugin Marketplace as an
early beta. The point of it is a light somebody walks past, not a screen somebody opens.

## 2. Where everything is

| | |
|---|---|
| Build host | `kevin`, `10.0.0.160`, RHEL 9 — `ssh -o BatchMode=yes robchartier@10.0.0.160 '<cmd>'` |
| Repository on the host | `/home/robchartier/prusa/wled-printer-status`, branch `main` |
| Public remote | `github.com/nothingmn/wled-printer-status` |
| Connect instance | Docker container `prusa_connect`; module CLI is `docker exec prusa_connect /prusa_connect module …` |
| Module routes | `http://10.0.0.160:8080/api/v1/modules/com.github.aux.nothingmn.wled-printer-status/…` — **the `.aux.` matters, omit it and you get a 404** |
| Module's own store | `/config/config.d/module-data/wled-printer-status/config.json`, **schema version 5** |
| Panel | `morpheus` `http://10.0.0.136` — 256 × 64, 16,384 pixels, WLED `14.7.0-mdev` |
| Strip | `CyberDesk` `http://10.0.0.253` — 48 LEDs, six named zones, WLED `16.0.1` |
| The running release | `/config/config.d/wled-release-531e73c` (built from `531e73c`) |
| The way back | `/home/robchartier/wled-rollback/` — `ROLLBACK-69cc791.md` and its archive are the step back from what runs |
| Device backups | `/home/robchartier/wled-backups/<device>-<timestamp>/`; the pre-install copies of both lights are in `wled-rollback/lights-69cc791-20261119T1238Z/` |
| Signing key | `/home/robchartier/.prusa-signing/wled-printer-status/signing-key.pem`, `0600` in a `0700` dir — **never copy, print or commit it** |

**State at the time of writing:** the tree is clean; `git log --oneline -5` says where `HEAD` stands. **The
deployed release was built from `531e73c`** and its installed `linux_amd64` digest is
`17189a45fcd9094b88dfab6c2bfb2dc0b277b124eab79fd69a564686da61d6a3`; the commits above it are
documentation, and the UI bundle was not rebuilt because nothing under `ui/` changed between the previous
release and this one — the shipped `ui/index.js` is `a46ac73b…`, proved `cmp`-identical rather than assumed.
**378 top-level tests** (`go test ./... -count=1 -v | grep -E '^--- PASS: Test'`), green plain and under
`-race`. Version **0.1.0**, installed and driving **two** lights under **one** printer, store at **schema 5**
with two bindings and five empty tables, tag `v0.1.0` pushed, **zero GitHub releases** (so `release.yml`
has never run).

## 3. How to build, and how to prove

**Go is not installed on the host and must not be.** Every build and test runs in a container:

```bash
docker run --rm --user $(id -u):$(id -g) \
  -e HOME=/tmp -e GOCACHE=/tmp/gocache -e GOPATH=/tmp/gopath -e CGO_ENABLED=0 \
  -v /home/robchartier/prusa:/work -w /work/wled-printer-status \
  golang:1.26.11 go test ./... -count=1
```

Add `-e CGO_ENABLED=1` and `-race` for the race run. A **bare `git archive` export cannot build** until
`.core` is recreated inside it as `ln -s /work/prusa-connect <src>/.core` — the `go.mod` replace points at
`./.core/sdk`, and a symlink to a host path does not resolve inside the container.

Four gates before any commit: `go build ./...`, `go vet ./...`, `go test ./... -count=1`, `gofmt -l .` (any
output is a failure), plus `-race` with `CGO_ENABLED=1`. Binaries are static (`CGO_ENABLED=0`); the process
runs inside the container as a child of its PID 1.

**Build a release from `git archive HEAD`, never from a dirty tree** — another writer has been seen
restoring files in this checkout. `/home/robchartier/prusa/build-release-69cc791.sh` is the script this
wave used and is worth copying:

- the four platform binaries come out of the export in `golang:1.26.11`, `-trimpath`;
- **the UI half is only rebuilt when `git diff --stat <installed sha>..HEAD -- ui/` is non-empty.** When
  that diff is empty the bundle is copied out of the release that is already installed and `cmp` is used to
  record that it is byte-identical — a stale bundle is a stale screen with every checksum still passing, and
  a needless rebuild is a new digest nobody can explain. This wave reused `ui/index.js`
  `a46ac73b…`, identical to what was running;
- `checksums.txt` covers every file but itself, then `sha256sum -c` inside the container after the copy in.

**Install / update** (in place, needs one restart — the host reads `config/config.d/resources.json` only
at start):

```bash
docker exec prusa_connect /prusa_connect module add github.com/nothingmn/wled-printer-status@0.1.0 \
  -from /config/config.d/wled-release-<sha> -settings /config/config.d/wled-settings.json -approve
docker restart prusa_connect
```

The same version has now been accepted four times; the host compares the manifest string to the child's
own `extension.Version("0.1.0")` exactly, so a version bump is a decision, not a detail.

**The diagnostic ladder** — the four preview routes fire at every light the module drives, and `light=`,
the name `GET /controllers` answers with, aims one request at one light:

```bash
B=http://10.0.0.160:8080/api/v1/modules/com.github.aux.nothingmn.wled-printer-status
curl -s -X POST "$B/preview-pattern?pattern=solid&hold=180"                       # whole panel white → transport
curl -s -X POST "$B/preview-pattern?pattern=border&hold=180"                     # 2px white edge  → LED map
curl -s -X POST "$B/preview-pattern?pattern=quarters&hold=180"                   # R/G/B/W bands   → orientation
curl -s -X POST "$B/preview-pattern?pattern=solid&hold=8&light=10.0.0.253"       # one light only
curl -s -X POST "$B/preview-finished?hold=180"                                   # the word FINISHED
curl -s -X POST "$B/preview-frame?percent=41&name=MK4IS&hold=8"                  # the composed steady frame
```

That ladder is what closed the biggest open question in the project — one look answered transport, LED map
and orientation at once — and it refused every request on a two-light install until `5104b90`.

**What an answer proves.** Every picture this module draws, including all four patterns, goes out **over
DDP with an establishing body underneath** (§6). So the controller's `/json/state` will *never* show the
pixels: it shows segment 0 carrying the picture's name, the bounds it was given, and `live` in
`/json/info` while the stream runs. To see the actual bands, capture the datagrams:

```bash
docker run -d --name ddpcap --net=host --cap-add=NET_RAW --cap-add=NET_ADMIN \
  -v /tmp/ddp:/out alpine:3 sh -c 'apk add -q tcpdump >/dev/null 2>&1; \
    exec tcpdump -i enp4s0 -n -s0 -U -w /out/ddp.pcap udp port 4048'
# fire a preview, wait out the hold, then: docker stop ddpcap
```

Each datagram is a 10-byte DDP header (offset at bytes 4–8 big-endian, length at 8–10) followed by RGB
triples from `offset/3`. Reassemble by offset and the frame is yours, pixel for pixel. `tcpdump` on the
host itself is not allowed (`sudo` needs a password); the container with `--net=host` is.

## 4. What is settled — do not re-litigate

`docs/product-requirements.md` is **locked** (2026-11-19) and describes eight screens, each of which has a
working mockup in `docs/design/v2/`. Highlights that have already cost arguments:

- **The module is driven by its own UI.** The manifest-settings text areas are gone as an editing surface.
- **Configuration lives in the module's own store**, which survives a restart and an upgrade but **not a
  `module purge` or a fresh install** — the answer is the per-light `download a backup`. This is stated in
  the README because it differs from how Connect modules normally behave.
- **Presets are WLED's.** Our UI only names one. No preset gallery, no effect/speed/palette editor.
- **A state may carry a word**, and that word **beats the Layout composer**.
- **A pause switches the light off** — not frozen — with `10 min / 1 hour / 6 hours / 1 day / 3 days /
  forever`. Exception: a borrowed zone is **handed back**, never powered off, and that has no exception for
  a silent printer (proved by `TestAPauseOnABorrowedZoneHandsItBackWhenThePrinterIsStale`).
- **One printer may sit on several lights** (multi-room). A **segment** carries one printer only.
- **Replacing a light requires identical hardware** — same shape, same pixel count — and matches segments
  **by name only**.
- **Responsive layout is required** (it is used from a phone). No UI localisation. Colour-blind
  accommodation is explicitly not a requirement.

## 5. What is built, and what is not

**All nine items of the old backend list are code, deployed on two lights at schema 5.** What each one is,
in the order the UI needed them:

1. **`GET /controllers/{name}`** — a light's own segments, so every segment picker has something to offer.
2. **`GET /frames`** — one picture per binding for the Overview thumbnails: the frame the painter decided,
   the reading's age, the last write, and the PNG in base64, drawn by the same code that draws the light.
3. **`GET /devices/{name}/snapshot`, `GET …/snapshot/{id}`, `POST …/restore`** — configuration snapshots
   with an explicit confirmation, a safety snapshot before the write and a wait for the light to answer.
4. **The moment table** — `GET /lights/{light}/moments`, `GET/PUT/DELETE /moments/{moment}`: per
   (light, printer) × moment, colour, word, preset or effect.
5. **The state table** — `GET /states`, `GET/PUT/DELETE /states/{state}`: colour, word and `holds`
   (`forever`, `30s`, `60s`, `5m`) with `colour_origin` / `word_origin` saying what was the operator's.
   The answer carries `word_shows` and `word_until`, because a bare 200 cannot say a word was spent before
   anybody read it.
6. **Layout storage per binding** — `GET/PUT/DELETE /bindings/{id}/layout` and `GET /lights/{light}/layouts`:
   bar, bar band and rows, percentage, name, remaining, rotation, take-turns or side-by-side.
7. **Brightness windows by route** — `GET/POST /windows`, `PUT/DELETE /windows/{index}`, judged in the zone
   the settings name and never defaulted to the process's clock.
8. **Pause, global and per binding** — `GET/PUT/DELETE /pause`, persisted with a timed expiry, with the
   borrowed-zone exception above.
9. **A versioned store** — schema 5 on disk, every version's upgrade an explicit step with its own reader,
   and a document from a *newer* release refused by name and both versions.

Around them: the binding routes (`GET/POST /bindings`, `GET/PUT/DELETE /bindings/{id}`), `/printers`,
`/controllers`, `GET /devices/{name}/presets` (a light's own presets, by slot and name, read-only, which
is the list a moment's `which` is picked from), `POST /devices/{name}/identify` (built in this tree and
**not yet deployed**: the flash that says which light a name means — on and off, one second in each state,
five times, then put back exactly as it was found; it answers `202` as the flash starts because ten seconds
does not fit the host's ten-second call), the four preview routes, `/status`,
`/wled-printer-status`, and a Svelte UI bundle served from the module's own `ui/`.

**Genuinely open — do not report these as done:**

- **Multi-room's per-binding pause is still keyed on the printer.** Two bindings driven from one printer
  cannot be paused independently: the pause row's scope names the printer, and the printer is the same for
  both lights. The store shape and `pause.go` accept a per-binding scope; the route and the key are what
  have not been separated.
- **`light-lost` never fires.** A binding whose controller stops answering is logged and survives, and its
  thumbnail goes stale, but no state or moment says "this light is gone", so nothing on the wall shows it.
- **`error-or-attention` vs `filament-change` on ATTENTION.** The printer reports one ATTENTION word for
  both a filament change and an error a person must clear, and this module cannot tell them apart, so both
  draw the ATTENTION colour and neither carries a word. Distinguishing them needs the printer's own error
  code mapped, which has not been done.
- **Snapshot retention.** Snapshots are taken before every restore and never pruned: how many are kept, for
  how long, and who deletes them is an open question, not a bug.

## 6. Measured facts — the expensive knowledge

Each cost real time to learn; none should be re-derived.

- **The controller's pixels cannot be read back.** `/json/state` has no `i` array, `/peek` is 404, and
  `pwr` reads **0 even with the whole panel solid red**. A picture is verified by eye, by the DDP capture
  in §3, or not at all.
- **But the panel's own segments are the right instrument for "is it black".** Whatever `live` is doing,
  `/json/state` carries the body the panel falls back to the instant live mode ends, and that body is the
  colour the wall shows. So reading `/json/state` ten times a second across a hold's end answers "was
  there a black gap" without a capture and without an eye — measured that way, a word's hand-back lands
  0.07–0.29 s after the stroke (`531e73c`) and was missing entirely before it.
- **A frame equal to the last one delivered is not proof the light is showing it.** The writer suppresses
  an unchanged picture to save traffic, and a held word takes the light with its own establishing body plus
  DDP pixels — so the frame "already shown" is not on the wall while the word is up. That suppression was
  the black panel: the steady frame at a hold's end was never written, and whether the panel came back in
  0 s or 15 s depended only on where the sixty-second heartbeat sat. A picture that is not a frame must
  say so (`overwritten` in `drive.go`), the way the event path clears `last` for the same reason.
- **Every picture goes over DDP, patterns included.** `paintEvent` writes one establishing body — segment 0
  given the whole geometry, in the background colour, named with the picture's word and otherwise with its
  name cleared; segment 1 the frame's remainder, off — and then pushes pixels. Which is why `/json/state`
  during a `quarters` preview reads `seg0 n='quarters' start=0 stop=48` on the strip and `0..256` plus a
  remainder segment on the panel, all in black, with the bands visible only on the wire.
- **The strip's `quarters` is four bands of twelve, red→green→blue→white, in that order** — captured off
  the wire on 2026-11-19: pixels 0–11 `(255,0,0)`, 12–23 `(0,255,0)`, 24–35 `(0,0,255)`,
  36–47 `(255,255,255)`. The panel's is four 128 × 32 quadrants, 4096 pixels each, the same four colours.
- **Realtime dies 2.5 s after the last datagram**, so a still picture is repeated **once a second** —
  about 50 kB/s, ~400 kbit/s.
- **The panel's LED map is row-major `y*width+x`** and correctly oriented — proved with the `quarters`
  pattern, not assumed.
- **A config write does not reboot** (WLED 16.0.1, measured): `POST /json/cfg` accepts full or **partial**
  bodies, answers `200 {"success":true}` in 65 ms, and needs `"rb":true` in the body or a separate
  `POST /json/state {"rb":true}`. **A reboot takes 5.7 s.** An identity restore caused **no outage at all**.
  Nothing in this module posts `/json/cfg`; pixels are its job, configuration is not.
- **Omitting the `nw` section makes WLED clear `nw.linked_remote`** (`cfg.cpp:64`). Send **`nw` minus
  `ins`** — keep the section, never the credential. **The WiFi PSK is not in a config dump at all**, so a
  literal 100 % clone is impossible.
- **A prerelease version cannot install.** `0.1.0-beta.1` parses, installs and never starts. The beta is
  expressed by a GitHub **pre-release** on the same `0.1.0`.
- **The Connect host renders nothing from a module's OpenAPI**, and a module page's settings are nine
  closed types with no enum and no colour. This is why the product needs its own UI.
- **A word's `set_at` is what makes a word on a long-running state visible.** The hold runs from the later
  of the state's entry and the write, and the write's instant is kept in the store — measured at schema 5:
  `{"state":"FINISHED","word":"QA","holds":"30s","set_at":"2026-11-19T12:24:33.160940222Z"}`.
- **A test that stamps a fixture with a fixed noon and lets the writer keep `time.Now` is a time bomb.**
  `TestAPauseOnABorrowedZoneHandsItBackAndPowersNothingOff` passed all morning and failed every run after
  12:10 UTC, because its pause had silently expired; the writers a pause test builds must be given
  `newFakeClock(at)` and `clock.watch(pen)`. Fixed in `69cc791`, with a stale-printer sibling beside it.
- **Commit messages truncate at a backtick** when passed over ssh. Use `git commit -F -` with a heredoc, or
  a message written to a file.

## 7. What the operator still owes

- **The v0.1.0 release**, whenever he wants the marketplace beta: Draft a new release → existing tag
  `v0.1.0` → paste the prepared body → **tick "Set as a pre-release"** → Publish. That fires `release.yml`.
- **A QA round on the two lights** — the ladder through both, a word set on `FINISHED` and cleared, and the
  four open items in §5 judged: only his eyes settle what a light is showing, and §6 says why no route can.
- **`AGENTS.md`** still carries a stale test count and the sentence about a printer appearing in at most one
  binding. That file is the operator's to release; a policy guard blocks an agent editing it.

## 8. Rules that will bite you

From `AGENTS.md`, which outranks this document: a borrowed segment is **always** given back; two writers
never share one segment; **secrets never enter tracked files**; scope every write to your own files (other
agents work in this checkout); never `git reset --hard`, `git checkout -- .` or `git clean`; **no claim
without the command and its output beside it**, and write "unverified" where the evidence is missing.

Two habits this project rewarded: **measure rather than reason** (three of the six biggest discoveries were
guesses that turned out to be false), and **say what the light will actually do** rather than what the code
intends.

## 9. Where the evidence is

- `docs/installing-locally.md` — the install log, run by run, with the commands and their output. The
  ninth run is this deploy: `531e73c`, binary `17189a45…`, store unchanged at 5, and the panel read
  across the stroke of a held word.
- `docs/handoff.md` (this file) — the starting point, and §5's open list.
- `docs/program/portfolio.md` in the program-manager tree — the decision register, every entry with the
  operator's own words and the command output that proved it.
- `docs/program/wled-printer-status-v2-requirements.md` — the locked requirements.
- `docs/program/wled-printer-status-test-sheet.md` — what needs the operator's eyes, item by item.
- `docs/design/v2/` in the repository — the eight approved mockups.
- `/home/robchartier/wled-rollback/` — every release archive, its rollback note, and the pre-install copies
  of both controllers.
