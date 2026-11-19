# Handoff — wled-printer-status

Written 2026-11-19 by the program-manager seat that built most of what is described here. **Read this
first, then `AGENTS.md`, then `docs/product-requirements.md`.** This document is meant to stand alone: if
something is missing from it, the incoming agent has no other context.

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
| Module status route | `localhost:8080/api/v1/modules/com.github.aux.nothingmn.wled-printer-status/status` — **the `.aux.` matters, omit it and you get a 404** |
| Panel | `morpheus` `http://10.0.0.136` — 256 × 64, 16,384 pixels, WLED `14.7.0-mdev` |
| Strip | `CyberDesk` `http://10.0.0.253` — 48 LEDs, six named zones, WLED `16.0.1` |
| Signing key | `/home/robchartier/.prusa-signing/wled-printer-status/signing-key.pem`, `0600` in a `0700` dir — **never copy, print or commit it** |
| Device backups | `/home/robchartier/wled-backups/<device>-<timestamp>/` |

**State at the time of writing:** `HEAD = 9d68394`, working tree clean, level with `origin/main`,
**229 tests**, tag **`v0.1.0`** pushed, **zero GitHub releases** (so `release.yml` has never run).

## 3. How to build, and how to prove

**Go is not installed on the host and must not be.** Every build and test runs in a container:

```bash
docker run --rm --user $(id -u):$(id -g) \
  -e HOME=/tmp -e GOCACHE=/tmp/gocache -e GOPATH=/tmp/gopath -e CGO_ENABLED=0 \
  -v /home/robchartier/prusa:/work -w /work/wled-printer-status \
  golang:1.26.11 go test ./... -count=1
```

Four gates before any commit: `go build ./...`, `go vet ./...`, `go test ./... -count=1`,
`gofmt -l .` (any output is a failure), plus `-race` with `CGO_ENABLED=1`. Binaries are static
(`CGO_ENABLED=0`); the process runs inside the container as a child of its PID 1.

**Install / update** (in place, needs one restart — the host reads `config/config.d/resources.json` only
at start):

```bash
docker exec prusa_connect /prusa_connect module add github.com/nothingmn/wled-printer-status@0.1.0 \
  -from /config/config.d/wled-release-<sha> -settings /config/config.d/wled-settings.json -approve
docker restart prusa_connect
```

Build the release directory from `git archive HEAD`, never from a dirty tree — **another writer has been
seen restoring files in this checkout**.

**Proving the display without eyes is impossible on this firmware** (see §6). What exists instead:

```bash
B=http://10.0.0.160:8080/api/v1/modules/com.github.aux.nothingmn.wled-printer-status
curl -s -X POST "$B/preview-finished?hold=180"                    # the word FINISHED
curl -s -X POST "$B/preview-pattern?pattern=solid&hold=180"       # whole panel white → transport
curl -s -X POST "$B/preview-pattern?pattern=border&hold=180"      # 2px white edge  → LED map
curl -s -X POST "$B/preview-pattern?pattern=quarters&hold=180"    # R/G/B/W corners → orientation
curl -s -X POST "$B/preview-frame?percent=41&name=MK4IS&hold=180" # the composed steady frame
```

That ladder is what closed the biggest open question in the project — one look answered transport, LED
map and orientation at once. Use it before doubting the code.

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
  forever`. Exception: a borrowed zone is **handed back**, never powered off.
- **One printer may sit on several lights** (multi-room). A **segment** carries one printer only.
- **Replacing a light requires identical hardware** — same shape, same pixel count — and matches segments
  **by name only**.
- **Responsive layout is required** (it is used from a phone). No UI localisation. Colour-blind
  accommodation is explicitly not a requirement.

## 5. What is built, and what is not

**Built and proved on hardware:** the steady frame (bar, percentage, job name), the `FINISHED` word over
DDP, the stale-name clear, the finished-job-holds-its-colour rule, the composed panel, the four test
patterns, and config snapshot/restore (see §6).

**Built as design, not code** — the backend work list, in the order the UI needs it:

1. `GET /controllers/{name}` returning the light's **own segments** — every segment picker is empty today.
2. `GET /frames` — one frame per binding, for the Overview thumbnails.
3. `GET /devices/{name}/snapshot` + `POST …/restore`.
4. The moment table: CRUD for (light, printer) × moment.
5. The state table CRUD (colour, word, `holds`).
6. Layout storage per binding, including `rotation: take-turns | side-by-side` (side-by-side needs a build).
7. Brightness windows edited by route rather than JSON.
8. Pause: global and per binding, persisted with a timed expiry.
9. A **versioned** configuration store (version it from the first commit — the shape will change).

## 6. Measured facts — the expensive knowledge

Each cost real time to learn; none should be re-derived.

- **The controller's pixels cannot be read back.** `/json/state` has no `i` array, `/peek` is 404, and
  `pwr` reads **0 even with the whole panel solid red**. A picture is verified by eye, or not at all.
- **WLED's own text effect (id 122) does not work on the panel.** Four groupings, every parameter set:
  only the left-hand columns ever lit. Text is drawn by this module instead (`internal/text`, 5×7 glyphs).
- **Individual pixels over the JSON API are silently ignored** on this firmware. Four bodies, HTTP 200,
  nothing lit. The per-LED call was deleted; the evidence is in `AGENTS.md` §5.4.
- **DDP works.** Whole panel = 35 datagrams (34×480 px + one of 64), 10-byte header, big-endian channel
  offset, push flag on the last only. Realtime dies `realtimeTimeoutMs` = 2500 ms after the last datagram,
  so a still picture is repeated **once a second** — about 50 kB/s, ~400 kbit/s.
- **The panel's LED map is row-major `y*width+x`** and correctly oriented — proved with the `quarters`
  pattern, not assumed.
- **A config write does not reboot** (WLED 16.0.1, measured): `POST /json/cfg` accepts full or **partial**
  bodies, answers `200 {"success":true}` in 65 ms, and needs `"rb":true` in the body or a separate
  `POST /json/state {"rb":true}`. **A reboot takes 5.7 s** (first refusal 0.635 s, first answer 5.667 s,
  same IP). An identity restore caused **no outage at all** — 0 failed polls of 90.
- **Omitting the `nw` section makes WLED clear `nw.linked_remote`** (`cfg.cpp:64`). Send **`nw` minus
  `ins`** — keep the section, never the credential. **The WiFi PSK is not in a config dump at all**
  (`ssid` present, `psk` absent on both lights), so a literal 100% clone is impossible.
- **A prerelease version cannot install.** A child reports `extension.Version("0.1.0")` and the host
  compares the manifest string to it exactly, so `0.1.0-beta.1` parses, installs and never starts. The beta
  is expressed by a GitHub **pre-release** on the same `0.1.0`.
- **The Connect host renders nothing from a module's OpenAPI.** A real page needs a `ui:` bundle built
  against `@prusa/connect-ui` — which is public, and **belongs in CI**, not on the host.
- **A module page's settings are nine closed types**, no enum, no colour; `list`/`object` render as a
  one-line JSON box. This is why the product needs its own UI.
- **Commit messages truncate at a backtick** when passed over ssh. Use `git commit -F -` with a heredoc.

## 7. What the operator still owes

- **The v0.1.0 release**, whenever he wants the marketplace beta: Draft a new release → existing tag
  `v0.1.0` → paste the prepared body → **tick "Set as a pre-release"** → Publish. That fires
  `release.yml`.
- Judgement on things only eyes can settle — see the "Not proved" list in the requirements §6.

## 8. Rules that will bite you

From `AGENTS.md`, which outranks this document: a borrowed segment is **always** given back; two writers
never share one segment; **secrets never enter tracked files**; scope every write to your own files (other
agents work in this checkout); never `git reset --hard`, `git checkout -- .` or `git clean`; **no claim
without the command and its output beside it**, and write "unverified" where the evidence is missing.

Two habits this project rewarded: **measure rather than reason** (three of the six biggest discoveries
were guesses that turned out to be false), and **say what the light will actually do** rather than what the
code intends.

## 9. Where the evidence is

- `docs/program/portfolio.md` in the program-manager tree — the decision register, every entry with the
  operator's own words and the command output that proved it.
- `docs/program/wled-printer-status-v2-requirements.md` — the locked requirements.
- `docs/program/wled-printer-status-test-sheet.md` — what needs the operator's eyes, item by item.
- `docs/design/v2/` in the repository — the eight approved mockups.
- `/home/robchartier/wled-backups/` — device configuration snapshots taken before any write.
