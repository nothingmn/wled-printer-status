# Installing it on a running Prusa Connect

This is the record of one install, on the operator's own host, with the commands as they were run and
the output they gave. A person repeating it needs a Prusa Connect that runs modules, a WLED controller on
the same network, one printer, and **no Go toolchain on the host** — the container is the whole toolchain.

The host is `kevin` (`10.0.0.160`), the instance is the two containers `prusa_connect` and
`prusa_connect_db`, the controller is `morpheus` at `http://10.0.0.136` — a 256×64 panel, 16,384 pixels —
and the printer is `Hermes MK4`, device id `07e893bb-b922-4ff0-9ad3-061b957400c3`, printing while this was
done, so every reading below is a real one that has since moved on.

The release below was built from commit `156b51f`, which is what the working tree held when the build ran;
the manifest that went into the release is that commit's `module.yaml`. Check `git log -1` before you build
and know that the binaries you install are the ones your tree is at, not the ones `main` moved to since.

## 1. The release is one directory with a binary per declared platform

`module.yaml` declares four platforms, so the release carries four binaries named
`<name>_<GOOS>_<GOARCH>`, `.exe` added for Windows, and the manifest at its root. The name and the platform
list are the manifest's; the host looks the file up by both, and refuses a release that ships no binary for
the platform it is running on.

```console
$ cd ~/prusa/wled-printer-status
$ docker run --rm --user $(id -u):$(id -g) \
    -e HOME=/tmp -e GOCACHE=/tmp/gocache -e GOPATH=/tmp/gopath -e CGO_ENABLED=0 \
    -v /home/robchartier/prusa:/work -w /work/wled-printer-status \
    -v /tmp/wled-release:/out \
    golang:1.26.11 sh -c 'for p in linux_amd64 linux_arm64 darwin_arm64 windows_amd64; do
      goos=${p%%_*}; goarch=${p#*_}; ext=; [ "$goos" = windows ] && ext=.exe
      GOOS=$goos GOARCH=$goarch go build -trimpath -o /out/wled-printer-status_${goos}_${goarch}${ext} .
      echo "exit[$p]=$?"
    done'
exit[linux_amd64]=0
exit[linux_arm64]=0
exit[darwin_arm64]=0
exit[windows_amd64]=0
```

`CGO_ENABLED=0` is not optional: the binary runs inside the Prusa Connect container, so it has to be
static. The parent directory is mounted rather than this one because `go.mod` replaces the SDK with
`./.core/sdk`, and `.core` is a relative symlink to `../prusa-connect` — the same path resolves on the host
and under the mount. `/out` is a second mount so the release lands outside the repository; a build
artefact does not belong in the tree.

```console
$ cp module.yaml /tmp/wled-release/module.yaml
$ ls -l /tmp/wled-release
total 36968
-rw-r--r--. 1 robchartier robchartier     477 Nov 18 12:49 checksums.txt
-rw-------. 1 robchartier robchartier    4768 Nov 18 12:49 module.yaml
-rwxr-xr-x. 1 robchartier robchartier 9293698 Nov 18 12:49 wled-printer-status_darwin_arm64
-rwxr-xr-x. 1 robchartier robchartier 9727883 Nov 18 12:49 wled-printer-status_linux_amd64
-rwxr-xr-x. 1 robchartier robchartier 9015083 Nov 18 12:49 wled-printer-status_linux_arm64
-rwxr-xr-x. 1 robchartier robchartier 9803264 Nov 18 12:49 wled-printer-status_windows_amd64.exe
```

## 2. `checksums.txt` is optional, and unforgiving

The install verifies a checksum file when the release carries one, and records a digest of what it wrote
either way. A release that carries one must list **every** file in the directory but `checksums.txt` and
`checksums.txt.sig` — a file beside it that the list leaves out is refused, because it would be installed
with nothing having checked it. So the file is written from the directory, not by hand:

```console
$ cd /tmp/wled-release
$ sha256sum module.yaml wled-printer-status_linux_amd64 wled-printer-status_linux_arm64 \
    wled-printer-status_darwin_arm64 wled-printer-status_windows_amd64.exe > checksums.txt
$ cat checksums.txt
551258e87bb9193d26eb58abab179543b989cc640d5f67562cd6fec4ff8c85dc  module.yaml
b3d2d47c1a72321c52b3371c7a3af9c05dbb6ba41ae26342f7e1daa5dc010da9  wled-printer-status_linux_amd64
b9f288dd57d01ba9eccd6060b409af51123539ccd1b77bb8fd4f4096e55cbe6f  wled-printer-status_linux_arm64
a5a5a5e378ede6d94e7f5ba25a198f2753f0ae8533077afb53078dcee22bf17e  wled-printer-status_darwin_arm64
214ba33a0b08242f47fdbd7757d4e42db904f184094d47f9085a788e823056d1  wled-printer-status_windows_amd64.exe
$ sha256sum -c checksums.txt
module.yaml: OK
wled-printer-status_linux_amd64: OK
wled-printer-status_linux_arm64: OK
wled-printer-status_darwin_arm64: OK
wled-printer-status_windows_amd64.exe: OK
```

There is no `ui/` directory and no entry for one in the release this record installed, because the manifest
that release carried had no `ui` block: this module drew nothing in the app then. The release is the
manifest and the binaries and nothing else. (The bindings tab came later — see run nine — and a release
that carries a bundle lists it in `checksums.txt` like every other file.)

## 3. Copy the release where the core can read it

`/config/config.d` is a Docker volume and the only writable place the core can see. The two files the
install reads go there, and the declaration the install writes lands beside them.

Ownership is the trap. `docker cp` preserves the number the source file carries, so a copy made from a
normal user account lands owned by uid 1000, while the core runs as `prusa` (uid 10001) — and the volume's
root is `0750 prusa:prusa`, so **root inside the container cannot read it either**: the container runs with
`no-new-privileges` and no `DAC_OVERRIDE`. The first attempt failed on exactly that:

```console
$ docker exec prusa_connect /prusa_connect module add … -from /config/config.d/wled-release-0.1.0 …
prusa-connect: read the release's module.yaml: open /config/config.d/wled-release-0.1.0/module.yaml: permission denied
```

The fix is to land the files owned by `prusa` in the first place: untar them as that user, since `tar` run
by `prusa` creates what it extracts, and give the manifest mode 0644 — it is 0600 in the working tree, which
is what the copy carried.

```console
$ docker exec prusa_connect mkdir -p /config/config.d/wled-release-0.1.0
$ tar -C /tmp/wled-release -cf - . | docker exec -i prusa_connect tar -xf - -C /config/config.d/wled-release-0.1.0
$ docker exec prusa_connect chmod 644 /config/config.d/wled-release-0.1.0/module.yaml
$ docker exec prusa_connect ls -l /config/config.d/wled-release-0.1.0
-rw-r--r--. 1 prusa prusa     477 Nov 18 19:49 checksums.txt
-rw-r--r--. 1 prusa prusa    4768 Nov 18 19:49 module.yaml
-rwxr-xr-x. 1 prusa prusa 9293698 Nov 18 19:49 wled-printer-status_darwin_arm64
-rwxr-xr-x. 1 prusa prusa 9727883 Nov 18 19:49 wled-printer-status_linux_amd64
-rwxr-xr-x. 1 prusa prusa 9015083 Nov 18 19:49 wled-printer-status_linux_arm64
-rwxr-xr-x. 1 prusa prusa 9803264 Nov 18 19:49 wled-printer-status_windows_amd64.exe
```

The untar lands as `prusa` without being told to, because `docker exec` runs as the image's own user —
`uid=10001(prusa) gid=10001(prusa)` — and `tar` creates what it extracts as whoever runs it. Prove the core
can read the release before you install it, as the core's uid rather than as your own account:

```console
$ docker exec -u 10001 prusa_connect sh -c \
    "cd /config/config.d/wled-release-0.1.0 && sha256sum -c checksums.txt"
module.yaml: OK
wled-printer-status_linux_amd64: OK
wled-printer-status_linux_arm64: OK
wled-printer-status_darwin_arm64: OK
wled-printer-status_windows_amd64.exe: OK
```

A `permission denied` here is the same failure the install would hit a minute later, caught while nothing
is half-installed.

## 4. The settings the install writes into the declaration

`-settings` takes a JSON file whose keys are the options `module.yaml` declares. Nothing else is accepted:
the host checks every key against the manifest when it starts the module, and a key the manifest does not
name refuses the start.

| key | what it is here |
| --- | --- |
| `message` | the default line; left at the manifest's own words |
| `controller_address` | `http://10.0.0.136` — the one controller for an install that lists no `bindings`. It is **no longer required**: an instance that names several printers in `bindings` has no single address to name, and the module refuses to start only when neither is written. The egress entry names this setting |
| `printer` | `07e893bb-b922-4ff0-9ad3-061b957400c3` — the device id, so a second printer arriving later cannot change what this instance watches |
| `brightness` | `128` — written once at start and never moved afterwards, unless a schedule is written, where it is the value that stands between windows |
| `brightness_schedule` | a list of windows, one per line (`from`, `to`, `brightness`, optional `days`); empty is the fixed brightness this instance has always run at |
| `timezone` | the IANA zone a `brightness_schedule` is judged in, such as `America/Denver` — required whenever a schedule is written, and never defaulted to the container's clock |
| `segment` | one segment of a controller that carries several, or `-1` (the default) for the whole controller. A named segment is borrowed, drawn in, and given back; that mode writes no brightness and no power |
| `bindings` | the whole topology as one JSON list — one line per printer with its `controller` and optionally its `segment` — and it wins over the four settings above it when written. The release in run nine is the one that reads it; no install in this record yet carries a settings file that lists more than one binding |

```console
$ cat /tmp/wled-settings.json
{
  "message": "Wled Printer Status is serving.",
  "controller_address": "http://10.0.0.136",
  "printer": "07e893bb-b922-4ff0-9ad3-061b957400c3",
  "brightness": 128
}
$ docker exec -i prusa_connect sh -c "cat > /config/config.d/wled-settings.json" < /tmp/wled-settings.json
```

## 5. Install it, approved

`-approve` is what writes the requested permissions and the events into the declaration, grants the
instance what they name, and switches it on. Without it the declaration is written switched off. The
release is named by the module path the namespace is derived from, so the path in the command is the
repository address, not the module's name.

```console
$ docker exec prusa_connect /prusa_connect module add \
    github.com/nothingmn/wled-printer-status@0.1.0 \
    -from /config/config.d/wled-release-0.1.0 \
    -settings /config/config.d/wled-settings.json \
    -approve
installed com.github.aux.nothingmn.wled-printer-status/wled-printer-status as sink:wled-printer-status
  files       config/config.d/modules/wled-printer-status
  namespace   com.github.aux.nothingmn.wled-printer-status
  checksums   5 files verified against checksums.txt
  setting     message (string, default Wled Printer Status is serving.). The line GET /wled-printer-status answers with.
  setting     controller_address (string, required, write it under this instance's config block). The WLED controller to drive, as its base URL such as http://10.0.0.136, with no path and no trailing slash.
  setting     printer (string, no default). The printer to watch, by the server's device id for it or by the name an operator gave it. Empty watches the only printer the instance holds, and refuses to choose between several.
  setting     brightness (int, default 128). The brightness written to the controller once at start, 0-255. This module never changes it afterwards.
  connects to the host in the setting controller_address port 80 from the process: The module drives the WLED controller the operator names, and a WLED controller answers HTTP on its own port 80. Sends: The current status colour and progress, as a WLED state write to /json/state. No printer data, no credentials, and nothing beyond the controller's own /json endpoints.
  granted     org:local#viewer@sink:wled-printer-status for observe. The viewer role also carries view_resource, read_storage
  memory      128 MiB, the default (memory_mib)
  enabled     yes

The record is written to config/config.d/resources.json. A core is answering on :8080 and this command
cannot make it read that file again, so restart it so that it reads the change. The same install through
the API, POST /api/v1/modules, takes effect at once.
The grants are in the database that core is reading, so its next request answers by them. An event stream
already open keeps the access it was opened with until that core restarts.
```

**Installing restarts the core.** The command writes the record and the files; the running core read its
record at start and will not read it again, which is what that paragraph says. The install is finished only
after the restart in the next section.

The install copies what it needs itself: the binary goes to `config/config.d/modules/wled-printer-status/bin/`
and the declaration to `config/config.d/resources.json`. There is no bundle, so no `bundle/` directory.

## 5a. Installing over an install that is already there

**`module add` updates in place. `module remove` is not needed to replace a running module**, so nothing
here has to be uninstalled first. Proved on 2026-11-18 by replacing a live, enabled
`sink:wled-printer-status` built from an older commit with the release from `d0be1a6` — the same four
flags as a first install, pointed at the new release directory:

```console
$ docker exec prusa_connect /prusa_connect module add \
    github.com/nothingmn/wled-printer-status@0.1.0 \
    -from /config/config.d/wled-release-d0be1a6 \
    -settings /config/config.d/wled-settings.json \
    -approve; echo "MODULE_ADD_EXIT=$?"
… same lines as §5 …
  memory      32 MiB, set by the author (memory_mib)
  new         memory 32 MiB, which the release it replaced did not ask for
  enabled     yes
MODULE_ADD_EXIT=0
```

Three things that output tells you:

- **`new`** names what changed against the release it replaced. On this run it was the budget alone; the
  declared version did not move, so the same `0.1.0` in the command is right.
- **One restart is still owed.** The command says so itself, and the instance came back `healthy` eight
  seconds after `docker restart prusa_connect`.
- **A reinstall is also how the manifest's catalogue fields reach the settings pane.** An older declaration
  carried `"summary": "One sentence saying what wled-printer-status does."` because that is what its
  manifest said; the same instance after this install carries the real one, a `title`, the `codeowner` as
  `author`, and `asked.memory_mib: 32`. Nothing rewrites those in place — they arrive with the release.

Leave the release directory alone until you are satisfied: the declaration names it as the module's
`source`, and it is the only copy of what was installed. Remove the *previous* one, though — a release
directory left behind is what a later install silently picks up.

**Third run, 2026-11-18 21:14Z, from `bd85035`.** The same four flags and the same settings file
(`controller_address` `http://10.0.0.136`, `printer` `07e893bb-b922-4ff0-9ad3-061b957400c3`, `brightness`
128), pointed at `/config/config.d/wled-release-bd85035`. The command answered `5 files verified against
checksums.txt`, `memory 32 MiB, set by the author` and `enabled yes`, with no `new` lines: the declaration
it replaced already carried every field this release declares. `docker restart prusa_connect` came back
`healthy` ten seconds later, readiness answered `serving 4 declared 4 blocked []`, and the installed
binary's `sha256` (`bc4ca32f37b0a1f817a7e252234e644e75a44e775f9a26ddc2127da4cd821389`) is the release's own
`linux_amd64` line. The release directory from `d0be1a6` was removed afterwards.

That binary carries two things the instance had never run before: the state-transition logging
(`16709f1`, `ef50466`) and the unwired `ddp` and `matrix` packages (`f220e81`). The log shows the
transition strings are in the installed binary — `grep -a "went from %s to %s"` on it finds the format —
but a transition line only appears when the printer's state word moves, so a print that is already
`PRINTING` when the core restarts produces the start lines and no transition until it ends.

**Fourth run, 2026-11-18 21:40Z, from `d141884`.** Two commits, both of the same change: `30e45a0` adds
the `segment` setting and the borrow, `7c9b825` and `d141884` are what the first run against a real
multi-segment controller taught — WLED 16.0.1 answers `o1`–`o3` and `tp` as booleans rather than numbers, so
the read of `/json/state` failed and no segment could be borrowed; and a body written into a segment without
its `n` clears the name that segment had, so the first frames left the operator's zone unnamed.

The target this time was the operator's desk light, **`CyberDesk` at `http://10.0.0.253`** — WLED 16.0.1 on
an ESP32, 48 LEDs in one run, and six named segments he uses: `Base 1` 0–4, `Back 1` 4–14, `Front 1` 14–24,
`Base 2` 24–28, `Back 2` 28–38, `Front 2` 38–48. The settings file named `"segment": 2` — `Front 1` — with
`brightness` 128 left in place and never written:

```console
$ docker exec prusa_connect cat /config/config.d/wled-settings.json
{ "message": "…", "controller_address": "http://10.0.0.253",
  "printer": "07e893bb-…", "brightness": 128, "segment": 2 }
$ docker exec prusa_connect /prusa_connect module add \
    github.com/nothingmn/wled-printer-status@0.1.0 \
    -from /config/config.d/wled-release-d141884 \
    -settings /config/config.d/wled-settings.json -approve
  setting   segment (int, default -1). The number of the one segment to drive … 
  enabled   yes
```

What the run proved, all of it read off the device rather than off the module:

- **The borrow is said once, and it is a read.** `borrowed segment 2 "Front 1" from http://10.0.0.253:
  pixels 14-24, colour 8,255,0 0,0,0 0,0,0, effect 102, brightness 255`.
- **The controller's brightness was never written.** His strip runs at `bri` 255 and stayed 255 for the
  whole run, while the instance's `brightness` setting is 128 — a settle would have written 128. The panel
  at `10.0.0.136`, which this module settles, reads 128 for the same reason.
- **The five segments he did not name were byte-identical before and after**, comparing each `seg` entry
  as a whole: `Base 1`, `Back 1`, `Base 2`, `Back 2`, `Front 2` — bounds, rows, `col`, `fx`, `sx`, `ix`,
  `pal`, `bri` and `n` — over two reads a minute apart and a third four minutes in. `Front 1` went
  14–24 → 14–16 → 14–17 as the print moved, with `n` still `"Front 1"`.
- **The give-back works on hardware.** Three graceful stops each wrote the captured segment back and said
  so: `gave segment 1 "Back 1" back to http://10.0.0.253: pixels 4-14, colour 255,255,255 …, effect 0,
  brightness 255`, after which all six segments were byte-identical to the state captured before the run.

One thing to know when repeating this: **something else writes to that controller too.** Between two reads
`mainseg` moved and `Front 2`'s colour changed, with its `sel` flag flipping. This module's body cannot
carry `mainseg` or `sel`, and it names one segment id per body, so those changes were another client — an
open WLED app or an automation — not this module. A proof against a light somebody else also drives has to
say which writer each difference belongs to.

`10.0.0.136` was restored to the state captured for it before this work, by POSTing that document back, and
reads back with no field differing from it.

**Fifth run, 2026-11-18 22:07Z, from `c649b90`.** The desk light went back to its owner and the instance was
pointed at the panel: `controller_address` `http://10.0.0.136`, the same printer id, `brightness` 128, and
**no `segment`** in the settings — the whole controller. The release was built from `c649b90`, whose
`linux_amd64` digest is `b6f96bb0c75c5983a2a6718822b25488c77735851b87df00ee7235df64b13b54`, and that is what
the installed binary reads back. `module add` answered `5 files verified against checksums.txt`,
`memory 32 MiB, set by the author`, `enabled yes`. `docker restart prusa_connect` came back `healthy` in
twelve seconds, readiness answered `serving 4 declared 4 blocked []`, the instance `ready`, and the log:

```console
… wled-printer-status: watching printer 07e893bb-… and driving http://10.0.0.136 at brightness 128
… wled-printer-status: the controller drives 16384 LEDs
… wled-printer-status: the controller is a 256x64 panel, so a frame is drawn as a rectangle
… pid=14 ppid=1 cmd=config/config.d/modules/wled-printer-status/bin/wled-printer-status_linux_amd64
```

The desk light, given back before the new instance started:

```console
… gave segment 2 "Front 1" back to http://10.0.0.253: pixels 14-24, colour 8,255,0 0,0,0 0,0,0,
  effect 102, brightness 255
```

All six segments read back afterwards: `Base 1`, `Back 1`, `Base 2`, `Back 2` and `Front 2` byte-identical
to the document captured before the repoint, and `Front 1` at 14–24 in `8,255,0` with `fx` 102 — the borrow's
own capture, not the green bar it was carrying at the moment of the repoint, which is the give-back doing
its job. The controller's `bri` stayed 255 and `mainseg` 5 throughout.

The panel tracked the print, and then spelled out the transition. Two pairs, taken in the same session:

```console
--- 22:08:28Z  module  "state":"PRINTING","frame":{"colour":{"r":0,"g":170,"b":0},"lit":8683,"total":16384,"reason":"progress"}
--- 22:08:28Z  panel   seg 0 start 0 stop 136 startY 0 stopY 64  col [[0,170,0]]  fx 0
                      136/256 = 53.1 %, and 8683/16384 = 53.0 %

--- 22:58:21Z  module  "state":"FINISHED","frame":{"colour":{"r":0,"g":170,"b":0},"lit":0,"total":16384,"reason":"not-a-live-job"},
                      "last_send":{"at":"2026-11-18T22:58:18.461221809Z","ok":true}
--- 22:58:21Z  panel   seg 0 start 0 stop 256 startY 0 stopY 64 len 256 grp 4 on true
                      n "FINISHED"  col [[0,170,0]]  fx 122  sx 128  ix 24  pal 0  c1 0  c2 0  c3 0
                      seg 1 on false
--- 22:58:18Z  log     the printer Hermes MK4 went from PRINTING to FINISHED
```

That is the event on the device: the word as the segment's name, the effect at 122, the grouping at 4 and
the finished green, over the whole panel, a second after the printer said `FINISHED`. A minute later the
steady frame took the light back —

```console
--- 22:59:18Z  panel   seg 0 start 0 stop 256 startY 0 stopY 64 on true col [[0,170,0]] fx 0
                      seg 1 start 253 stop 256 on false
```

— which is `last_send` at 22:59:18, sixty seconds after the event's own write. Two things a reader should
know about that second read: the word is gone from the drawing because `fx` is back to 0, and **the name
survived** — `n` still reads `FINISHED` — where on WLED 16.0.1 the desk light cleared a name from any body
written without one. The clear-the-name behaviour is firmware's, not the wire's, so nothing may depend on
it; the event writes the name last for that reason.

What is still unverified: **nobody has looked at the light.** The read-back proves the fields the word is
drawn from; whether the letters are 48 pixels tall and where `ix` 24 puts them is a question for eyes, and
WLED's websocket pixel read returned all-zero frames the one time it was tried on this panel. `grp` 4 and
`ix` 24 are this build's guess at the operator's *"big green letters"*.

A third transition arrived two minutes later — `FINISHED` to `PRINTING` at 23:00:15Z, a second job — and it
fired nothing: a print beginning is an event the operator wants and one nobody has designed, so the table
has no entry for it and the panel was given the steady waiting yellow of a job at 0 % instead. By 23:02Z
the frame read `"reason":"starting"`, which is the first time that state has been seen on a panel.

The two release directories from `bd85035` and `d141884` were removed after this install proved healthy.

**Sixth and seventh runs, 2026-11-18 23:18Z and 23:21Z, from `3dd9ce7` and then `ea3bafa`.** These two installs
are the change from *a word the firmware draws* to *a word this module draws*: a 5×7 font carried in
`internal/text`, rasterised onto the panel and written one LED at a time. `AGENTS.md` §5.4 has why, and the
short of it is that effect 122 lit only the first few columns on this panel at four different groupings.

Two runs, because the first one found a defect that only the device could show. The first is worth recording
before the second, because the failure shape is the same fragment the whole change exists to end.

**Run six — `3dd9ce7`, 23:18Z.** Built and installed the same way as the runs above, with one difference in
the build: the release was built from `git archive HEAD` rather than from the working tree, because another
writer was editing this checkout during the session — two files were restored under the session at
16:14:47 local and one was left with a torn tail at 16:15:14, and a build that reads the working tree can
pick up a file mid-write. `git archive` reads committed objects only, so what was built is what was
committed:

```console
$ docker run --rm --user $(id -u):$(id -g) \
    -e HOME=/tmp -e GOCACHE=/tmp/gocache -e GOPATH=/tmp/gopath -e CGO_ENABLED=0 \
    -v ~/prusa:/work -v /tmp/wled-release-3dd9ce7:/out -w /work/wled-printer-status \
    golang:1.26.11 sh -c 'mkdir -p /out/src && git archive HEAD | tar -x -C /out/src &&
      ln -s /work/prusa-connect /out/src/.core && cd /out/src &&
      for p in linux_amd64 linux_arm64 darwin_arm64 windows_amd64; do …; done'
exit[linux_amd64]=0  exit[linux_arm64]=0  exit[darwin_arm64]=0  exit[windows_amd64]=0
$ sha256sum -c checksums.txt
module.yaml: OK   wled-printer-status_linux_amd64: OK   … (four platforms)
```

`module add` answered `5 files verified against checksums.txt`, `memory 32 MiB, set by the author`,
`enabled yes`; the restart at **23:18:56Z** was `healthy` at **23:19:06Z** (fifteen seconds), readiness
`serving 4 declared 4 blocked []`, and the installed `linux_amd64` digest read back as
`bad378ef82…9632b`, which is the release's own line. The log said what it always says — `the controller
drives 16384 LEDs`, `the controller is a 256x64 panel`.

Then the preview, fired at **23:19:12Z**:

```console
$ curl -s -X POST localhost:8080/api/v1/modules/com.github.aux.nothingmn.wled-printer-status/preview-finished
{"colour":{"r":0,"g":170,"b":0},"controller":"http://10.0.0.136","event":"FINISHED","holds_seconds":60,"scale":5}
$ curl -s http://10.0.0.136/json/state | …        # 23:19:14Z
seg 0 start 0 stop 256 rows 0..64 on true  fx 0  n 'FINISHED'  col [0,0,0]
seg 1 start 28 stop 256 rows 0..64 on true  fx 0  n None        col [0,0,0]
```

**Segment 1 was still on, over columns 28 to 256, in black — drawn on top of the word.** The event's
establishing body gave segment 0 the whole panel and left the frame's remainder where it was; a panel draws
its segments in order, so most of the word was under a black band and what was left was a fragment at the
left. Exactly the fragment that started this work, arrived at from the other direction.

**Run seven — `ea3bafa`, 23:21Z.** The body now switches segment 1 off as well, as the steady frame's
"everything filled" edge already did, and the test pins both segments in the first request. Same build, same
`module add` arguments, release `/config/config.d/wled-release-ea3bafa`; restart at **23:21:03Z**, `healthy`
at **23:21:14Z**, readiness `serving 4 declared 4 blocked []`, installed digest `9375e0b6dd…90cc2`.

The preview, fired at **23:21:17Z** — one event, sixty seconds, and this is the read-back either side of it:

```console
$ curl -s -X POST …/preview-finished            # PREVIEW_FIRED_AT 2026-11-18T23:21:17Z
 HTTP=202 {"event":"FINISHED","controller":"http://10.0.0.136","colour":{"r":0,"g":170,"b":0},"scale":5,"holds_seconds":60}

$ curl -s http://10.0.0.136/json/state          # 23:21:19Z, inside the hold
seg 0  start 0  stop 256  rows 0..64  on true   fx 0  n 'FINISHED'  col [0,0,0]
seg 1  start 33 stop 256  rows 0..64  on FALSE  fx 0  n None        col [0,0,0]

$ …  /status                                    # 23:21:19Z
"last_send":{"at":"2026-11-18T23:21:17.462501271Z","ok":true}   printer PRINTING, frame progress 2129/16384

$ curl -s http://10.0.0.136/json/state          # 23:22:32Z, after the hold
seg 0  start 0  stop 36   on true   n None  col [0,170,0]
seg 1  start 36 stop 256  on true   n None  col [0,0,0]
"last_send":{"at":"2026-11-18T23:22:17.463009126Z","ok":true}   frame progress 2293/16384
```

Three things that read-back proves and one it cannot. The event went out in **one** painting and the module
recorded it as taken; it held for **60.0 s** — the write that ended it is 23:22:17.463Z against the event's
23:21:17.462Z — and the frame that took the light back is the running print's progress bar, 36 of 256
columns at 2293/16384 = 14.0 %, which is the printer's own number a second earlier. What it cannot prove is
the letters: **this firmware's `/json/state` carries no `i` array at all** — the segment keys on
`14.7.0-mdev` are `bri c1 c2 c3 cct col frz fx grp id ix len m12 mY mi n o1 o2 o3 of on pal rY rev sel set si
spc start startY stop stopY sx tp` — so a per-pixel read-back is not available here, and the geometry plus
the segment's name is the whole of what the device will say about the word. The letters themselves are the
operator's to look at, which is what the preview route is for.

Neither of the two earlier release directories (`c649b90`, `3dd9ce7`) was removed: they are still the
declared source of an install until this one is signed off, and a directory removed mid-run leaves the
declaration pointing at nothing.

**Eighth run, 2026-11-18 23:41Z, from `16aaf8b`.** The change from *a word written as a JSON per-LED array*
to *a word pushed as pixels over DDP*, because this firmware accepts the array and draws none of it —
`AGENTS.md` §5.4 has the four measurements, and the operator looked twice at a blank panel. Built from
`git archive HEAD` for the reason run six gives, release `/config/config.d/wled-release-16aaf8b`, whose
`linux_amd64` digest is `737651e6b3c167c8ea6dbf9799889bc07864e7d3d7bded30146080e95a782b3b` and is what the
installed binary reads back.

The install answered `5 files verified against checksums.txt`, `memory 32 MiB, set by the author`,
`enabled yes`, and — new — **two egress lines**, because the module now declares the second connection it
makes to the same box:

```console
  connects to the host in the setting controller_address port 80 from the process: … the body that gives
  the panel its geometry and the word as the segment's name. …
  connects to the host in the setting controller_address port 4048 from the process: The word a finished
  print shows is pixels this module draws, and a WLED controller takes pixels over DDP — raw UDP datagrams
  to port 4048 … Raw colour bytes only — no printer data, no credentials, no document.
```

`docker restart prusa_connect` at **23:41:41Z** was `healthy` at **23:41:48Z** (seven seconds), readiness
answered `serving 4 declared 4 blocked []`, the process was `pid=32 ppid=1`, and the log said what it
always says plus nothing new: `the controller drives 16384 LEDs`, `the controller is a 256x64 panel`.

**What was measured before the preview, from inside the module's own container.** A single datagram with a
ten-byte DDP header, written from `docker exec prusa_connect bash` to `/dev/udp/10.0.0.136/4048`, turned
`/json/info`'s `live` from `false` to `true`.

```console
$ curl -s http://10.0.0.136/json/info | jq -c '{live}'                      # 23:36Z
{"live":false}
$ docker exec prusa_connect bash -c 'printf "@\x00\x0b\x01\x00\x00\x00\x00\x00\x03\xc3\xbf\x00\xc3\xbf"     > /dev/udp/10.0.0.136/4048'
$ curl -s http://10.0.0.136/json/info | jq -c '{live}'
{"live":true}
```

From the host, with a full 35-datagram frame: `live=true` 0.3 s after the frame and `live=false` 3.3 s
after it with nothing sent. And **a JSON state write does not end live mode** — a write sent while live
left `live` true, and it went false on the timeout ~2.5 s later. That is why the module writes the steady
frame first and reads the controller afterwards.

**The preview, fired twice.** First at **23:42:01Z**, and the read-back half a second later:

```console
$ curl -s -X POST …/preview-finished        # 23:42:01Z
 HTTP=202 {"colour":{"r":0,"g":170,"b":0},"controller":"http://10.0.0.136","event":"FINISHED","holds_seconds":60,"scale":5}
$ curl -s http://10.0.0.136/json/info       # 23:42:02Z   live=True
$ curl -s http://10.0.0.136/json/state      # 23:42:02Z
seg0 0..256 rows 0..64 on=True fx=0 n='FINISHED' col=[[0,0,0]…]
seg1 84..256 rows 0..64 on=False
```

Second at **23:43:34Z**, sampled every two seconds for the whole hold, which pins both ends:

```console
fired   23:43:34Z  202     last_send=23:43:34.688   (the establishing body and the frame's first push)
23:43:36Z … 23:44:34Z  live=True  seg0 0..256 n='FINISHED'   (thirty consecutive samples)
23:44:34Z  live=True   last_send=23:44:34.689              (the frame that ends the hold)
23:44:36Z  live=False  seg0 0..92 on=True n=''             (the light is back on its own segments)
23:44:37.499Z  the module's log:
  the "FINISHED" word is off the panel and http://10.0.0.136 reports live=false: the light is back on
  its own segments
```

**60.001 s** from the event's own write to the frame that took the light back, `live` true for every sample
in between and false at the first sample after, and one line in the log rather than one a second. Across
both previews: no `not updated` line, no failed write, and the steady frame took the light back both
times — `seg0 start 0 stop 92` at 23:44:36Z and, a minute later, `seg0 0..95` against the module's own
`"frame":{"lit":6062,"total":16384,"reason":"progress"}`, which is 6062/16384 × 256 = 94.7, so 95
columns. The progress bar is exactly what it was before this change.

What this does **not** prove is the letters. `live=true` says the controller took the pixels into the
buffer it draws; nothing on this firmware reports individual colours, so whether the word appears, is the
right size, sits in the right place, and whether this panel's wiring maps row-major the way the frame
assumes, are all questions for a person looking at the light. **Nobody has looked yet.**

**Ninth run, 2026-11-19 00:01Z, from `5c42026`.** The change from *a binding is four settings* to *a
binding is one line of a list*, and the routes an editor would call. Built from `git archive HEAD` for the
reason run six gives, release `/config/config.d/wled-release-5c42026`, whose `linux_amd64` digest is
`d89c63fdd167ccde7cce397838c5be74f1129a8571bf0d6f40ef69f8c4a50832`.

The settings file was **not changed**: `controller_address` `http://10.0.0.136`, the printer id, brightness
128, no `segment`. This run's question was whether a release that can carry several printers still runs
one, from the settings an instance already has — and it does, in the same words as every run before it.
That setting is no longer required in the manifest, so the install now prints `(string, no default)` for
it and a sixth line for the list:

```console
$ docker exec prusa_connect /prusa_connect module add \
    github.com/nothingmn/wled-printer-status@0.1.0 \
    -from /config/config.d/wled-release-5c42026 \
    -settings /config/config.d/wled-settings.json -approve
  checksums   5 files verified against checksums.txt
  setting     controller_address (string, no default). The one WLED controller to drive when `bindings` is empty, as its base URL …
  setting     bindings (list, default []). The printers this module shows, one line each, written as a JSON list of objects with a printer key and a controller key. …
  memory      32 MiB, set by the author (memory_mib)
  enabled     yes
MODULE_ADD_EXIT=0
$ docker restart prusa_connect        # 00:01:33Z
prusa_connect                          # healthy at 00:01:40Z — seven seconds
$ curl -s localhost:8080/api/v1/readiness
{"app":"served","discovery":"announcing","ready":true,"serving":4,"declared":4,"blocked":[],…}
```

The module came back as `pid=30` and said what it always says:

```console
… wled-printer-status 0.1.0 is serving on stdio as pid 30
… wled-printer-status: watching printer 07e893bb-… and driving http://10.0.0.136 at brightness 128
… wled-printer-status: the controller drives 16384 LEDs
… wled-printer-status: the controller is a 256x64 panel, so a frame is drawn as a rectangle
```

**What the new routes answer on the live instance.** Every read below is the module's own, taken through
the host's mount at `.../modules/com.github.aux.nothingmn.wled-printer-status/`:

```console
$ curl -s …/bindings
{"bindings":[{"id":"07e893bb-…","position":1,"printer":"07e893bb-…","printer_name":"Hermes MK4",
 "controller":"http://10.0.0.136","segment":-1,"whole":true,"brightness":128}],"count":1,
 "source":"controller_address, printer, brightness and segment: the four settings that came before bindings",
 "persisted":false}
$ curl -s …/printers
{"count":1,"total":1,"printers":[{"id":"07e893bb-…","name":"Hermes MK4","model":"MK4","connected":true,
 "bound":{"controller":"http://10.0.0.136","segment":-1,"whole":true}}]}
$ curl -s …/controllers
{"controllers":[{"name":"10.0.0.136","address":"http://10.0.0.136","key":"http://10.0.0.136",
 "whole":"07e893bb-…","segments":[],"printers":["07e893bb-…"]}],"count":1,"segments_known":false}
```

**The refusals, on the live instance.** Four requests that must change nothing, each answered by a code:

```console
POST /bindings {"printer":"07e893bb-…","controller":"http://10.0.0.136"}
  409 BINDING_REFUSED  "printer 07e893bb-… is already shown by a binding: one printer is shown by one
                        binding, so change that one with PUT /bindings/07e893bb-…"
POST /bindings {"printer":"Ghost","controller":"http://10.0.0.136"}
  409 BINDING_REFUSED  "no printer in this instance is called \"Ghost\""
POST /bindings {"printer":"07e893bb-…","controler":"http://10.0.0.136"}      # a misspelt key
  400 BINDING_BODY_INVALID  "the body is not one binding: json: unknown field \"controler\" …"
PUT /bindings/nope {"controller":"http://10.0.0.136"}
  404 BINDING_NOT_FOUND  {"params":{"printer":"nope"}, …}
```

**A change reaching the writer without a restart.** The one binding was written back unchanged —
`PUT /bindings/07e893bb-…` with the controller, segment -1 and brightness 128 it already had — because a
change that altered where the panel draws would have been a change to what the operator is looking at:

```console
$ date -u; curl -s -X PUT …/bindings/07e893bb-… -d '{"controller":"http://10.0.0.136","segment":-1,"brightness":128}'
00:01:56Z
 HTTP=200 {"applied":true,…,"persisted":false,"note":"This took effect now: the writer was stopped and
 started again on the new set. It is not saved. …"}
$ docker logs --since 60s prusa_connect | grep "watching printer"
00:01:36.423Z  watching printer 07e893bb-… and driving http://10.0.0.136 at brightness 128   ← the start
00:01:56.250Z  watching printer 07e893bb-… and driving http://10.0.0.136 at brightness 128   ← the PUT
$ docker exec prusa_connect sh -c 'tr -c "[:print:]" " " < /proc/30/cmdline'
config/config.d/modules/wled-printer-status/bin/wled-printer-status_linux_amd64                 ← same pid
$ curl -s …/status
… "last_send":{"at":"2026-11-19T00:01:57.269823185Z","ok":true} …
$ curl -s …/bindings | jq -r .source
a change made through this module's own routes, which the settings do not carry
```

Twenty seconds after the start, the watch was stopped and started again inside the same process, and the
panel took the same frame a second later. `source` is what says the set in force is no longer the one the
settings file carries — which is also the thing a restart undoes.

**What this run does not prove.** Two printers on two controllers, because this instance holds one printer
and the second controller belongs to the desk light that must not be written to. The `bindings` list is
proven by the host's own manifest parser accepting it (`manifest.Parse` answered `ACCEPTED kind=sink
version=0.1.0 options=6 egress=2` with `bindings type=list default="[]"`) and by the tests in the root
package; getting it onto a live instance costs one `module add` and one restart, which this run did not
spend twice. And **nobody has looked at the panel** — this change does not draw anything differently, and
the frame it reports is the frame the run before it reported.

**Tenth run, 2026-11-19 00:25Z, from `1ecdf9c`.** The change from *a brightness written once* to *a
brightness that follows the time of day*, judged in a zone the operator names. Built from `git archive
HEAD` for the reason run six gives; release `/config/config.d/wled-release-1ecdf9c`, whose `linux_amd64`
digest is `dcbd2ef7757cb61150e3f063c885dff530f6a0a57f76998be3fab4b6ad543357` and is what the installed binary
reads back.

`ui/` is unchanged between the release that last carried a bundle (`e8bc059`) and this commit — the only
difference between the two trees is `.github/workflows/ci.yml` — so the browser half in this release is the
bundle that release installed, carried over rather than rebuilt, and it is listed in `checksums.txt` like
every other file. `module add` answered `10 files verified against checksums.txt` and printed the two new
settings among the six it now lists.

**The refusal came first, and on the instance rather than in a test.** The release was installed once with a
misspelt zone — `"timezone": "America/Denverr"`, everything else as below — and the core restarted. The module
printed its handshake line, was asked to configure, and refused. Nothing was written to the panel: the
refusal happens in `plugin.configure`, before a writer exists.

```console
00:25:15.576Z  wled-printer-status 0.1.0 is serving on stdio as pid 15
00:25:15.584Z  WARN resource failed to start  attempt=1 class=permanent
  error="core.internal/RESOURCE_PLUGIN_SETTINGS: the module refused the settings it was given: wirerpc:
  -32603: timezone is \"America/Denverr\", which is not a zone this build can load: write an IANA name such
  as America/Denver, because the schedule is judged in the zone you name and not in this process's own
  clock (unknown time zone America/Denverr)"
00:25:15.584Z  resource state changed  state=failed result=start-error serving=false attempt=1
00:25:16.168Z  readiness ready=true serving=3 declared=4 … sink:wled-printer-status=failed/optional
```

`class=permanent` is the useful half: the core does not retry this, so a misspelt zone is one failed start and
a sentence, not a loop. The rest of the instance stayed `ready` throughout (`serving 3 declared 4` with this
one instance `failed`), which is what an optional instance refusing its own settings should look like.

**Then the settings it should have had.** A schema-valid schedule that does nothing, written on purpose: a
window that covers the whole day at the brightness already in force, so the panel is not changed while the
unverified word on it is still waiting for the operator's eyes.

```console
$ docker exec prusa_connect cat /config/config.d/wled-settings.json
{
  "message": "Wled Printer Status is serving.",
  "controller_address": "http://10.0.0.136",
  "printer": "07e893bb-b922-4ff0-9ad3-061b957400c3",
  "brightness": 128,
  "brightness_schedule": [
    {"from": "00:00", "to": "23:59", "brightness": 128}
  ],
  "timezone": "America/Denver"
}
$ docker exec prusa_connect /prusa_connect module add \
    github.com/nothingmn/wled-printer-status@0.1.0 \
    -from /config/config.d/wled-release-1ecdf9c \
    -settings /config/config.d/wled-settings.json -approve
  checksums   10 files verified against checksums.txt
  memory      32 MiB, set by the author (memory_mib)
  enabled     yes
MODULE_ADD_EXIT=0
```

`docker restart prusa_connect` at **00:25:44Z** was `healthy` at **00:25:44Z + 25 s**, readiness answered
`serving 4 declared 4 blocked []` with this instance `ready`, and the log named the zone — both the line the
watch prints and the line the brightness write prints:

```console
00:25:46.436Z  watching printer 07e893bb-… and driving http://10.0.0.136 at the brightness of
               brightness_schedule judged in America/Denver, with 128 standing wherever no window covers
               the moment
00:25:46.449Z  the light's brightness is 128, the 00:00-23:59 window of America/Denver, where it is 17:25 MST
00:25:47.450Z  the controller drives 16384 LEDs
00:25:47.450Z  the controller is a 256x64 panel, so a frame is drawn as a rectangle
```

`17:25 MST` against a container clock at `00:25Z` is the whole point of the change in one line: the window was
read in the operator's zone, and the log says which clock it read. A build reading the process's clock would
have printed something else here, and the test that catches that is
`TestAScheduleIsJudgedInItsZoneAndNotInTheContainersClock`.

The panel, read straight after, unchanged and still tracking the print that was running:

```console
$ curl -s http://10.0.0.136/json/state            # 00:26Z
bri 128  on true  mainseg 0
seg0 start 0 stop 202 rows 0..64  col [[0,170,0],[0,0,0],[0,0,0]]  n None  fx 0
seg1 start 202  on true
$ curl -s …/status
"frame":{"colour":{"r":0,"g":170,"b":0},"lit":12943,"total":16384,"reason":"progress"},
"last_send":{"at":"2026-11-19T00:25:47.450858004Z","ok":true}
```

12943/16384 × 256 = 202.2, so 202 columns: the progress bar is the printer's own number, and `bri` is the
128 the fixed setting already had — the schedule wrote the value that was already there, which is what a
do-nothing schedule is for. The installed `linux_amd64` digest read back as the release's own line, above.

**What this run does not prove.** A window's *edge* moving a live panel's brightness. The schedule installed
covers the whole day at the value in force, so no edge was crossed and no second brightness write was made;
that path is proved by the tests with an injected clock and by nothing on this device. It was written that
way on purpose: the `FINISHED` word on this panel is still unverified, nobody has looked at it, and the
brightness is not the thing to change underneath an experiment that only the operator's eyes can settle. The
desk light at `10.0.0.253` was not written to, no preview was fired, and no pixels were pushed.

**Eleventh run, 2026-11-19 00:39Z, from `c817859`.** The change that clears the word's name when its hold
ends, and the removal of the per-LED JSON call (`Paint`) that this firmware accepts and draws nothing of.
Built from `git archive HEAD` for the reason run six gives; release `/config/config.d/wled-release-c817859`,
whose `linux_amd64` digest is `22a137ff9e6ccdd91fe123acca0c6a374335c901529084ef9a7437bdfa54f166` and is what
the installed binary reads back.

`ui/` is unchanged between `e8bc059` and this commit, so the bundle was carried over from the release it was
last built into and is listed in `checksums.txt` with everything else: ten files, which `module add` counted
back. The settings file was **not** touched — a do-nothing whole-day window at 128 in `America/Denver` — so
the panel's brightness is where the tenth run left it. The install printed the eight settings and the two
egress lines (port 80, and port 4048 for the pixels), said `memory 32 MiB, set by the author`, `enabled yes`,
and `MODULE_ADD_EXIT=0`.

```console
$ docker restart prusa_connect        # 00:39:33Z
$ docker ps --format "{{.Names}}\t{{.Status}}" | grep prusa_connect
prusa_connect     Up 27 seconds (healthy)      # and still healthy at two minutes
prusa_connect_db  Up 25 hours (healthy)
$ curl -s localhost:8080/api/v1/readiness
{"ready":true,"serving":4,"declared":4,"blocked":[],…}    # this instance ready
… wled-printer-status: watching printer 07e893bb-… and driving http://10.0.0.136 at the brightness of
                       brightness_schedule judged in America/Denver, with 128 standing wherever no window covers
… wled-printer-status: the light's brightness is 128, the 00:00-23:59 window of America/Denver, where it is 17:39 MST
… wled-printer-status: the controller drives 16384 LEDs
… wled-printer-status: the controller is a 256x64 panel, so a frame is drawn as a rectangle
```

**The name, proved on the device rather than in a test.** The debt this run closes was a `FINISHED` name that
outlived its own event, because this firmware keeps a segment's name through a body that does not mention
`"n"`. A preview could not be fired to make one — the operator's word experiment is unverified and must be
left exactly as it was — so the leftover state was reproduced directly, with a body that carries the name
and nothing else, and the module's ordinary write was watched clearing it:

```console
$ curl -s -X POST -H 'Content-Type: application/json' -d '{"seg":[{"id":0,"n":"FINISHED"}]}' \
    http://10.0.0.136/json/state                    # 00:40:07.997Z
{"success":true}
$ curl -s http://10.0.0.136/json/state              # 00:40:08Z
seg 0  start 0  stop 236  rows 0..64  on true  fx 0  n 'FINISHED'  col [0,170,0]
                                                    # 00:40:49Z — no request from anyone but the module
seg 0  start 0  stop 238  rows 0..64  on true  fx 0  n None        col [0,170,0]
seg 1  start 238 stop 256            on true          n None        col [0,0,0]
$ curl -s …/status                                  # 00:40:54Z
… "frame":{"colour":{"r":0,"g":170,"b":0},"lit":15237,"total":16384,"reason":"progress"},
  "last_send":{"at":"2026-11-19T00:40:48.992686417Z","ok":true}
```

The write that cleared the name is the write the progress bar was making anyway — `last_send` is
`00:40:48.993Z`, the read a second later shows the bar two columns further on and no name, and nothing was
sent to make the clear happen. `bri` reads 128 throughout, so the brightness the tenth run settled is
untouched.

**What this run does not prove.** That a letter is lit. No preview was fired and no datagram was pushed: the
word itself is still exactly as the operator left it, unlooked at, and `AGENTS.md` §8 says so. What is proved
here is the hand-back's other half — the name does not survive the event on this firmware, and clearing it
costs no request.

**Twelfth run, 2026-11-19 01:34Z, from `87fb786`.** The change that stops a finished panel
flapping into the stale red while its printer cools, and the nine tests behind it. Built from
`git archive HEAD` for the reason run six gives; release `/config/config.d/wled-release-87fb786`,
whose `linux_amd64` digest is
`c477de39ad7eae516c37ce785d40e78bf9f087ae545080d2565e0f69014a65f4` and is what the installed binary
reads back. `ui/` is unchanged between `e8bc059` and this commit (`git diff --stat e8bc059..HEAD --
ui/` is empty), so the browser half is the bundle that release installed, carried over rather than
rebuilt, and it is listed in `checksums.txt` like every other file: ten files, which `module add`
counted back. The settings file was not touched.

```console
$ docker exec -u 10001 prusa_connect sh -c \
    "cd /config/config.d/wled-release-87fb786 && sha256sum -c checksums.txt"
module.yaml: OK   README.md: OK   CHANGELOG.md: OK   LICENSE: OK
ui/index.js: OK   ui/locales/en.json: OK
wled-printer-status_linux_amd64: OK  … (four platforms)
$ docker exec prusa_connect /prusa_connect module add \
    github.com/nothingmn/wled-printer-status@0.1.0 \
    -from /config/config.d/wled-release-87fb786 \
    -settings /config/config.d/wled-settings.json -approve
  checksums   10 files verified against checksums.txt
  memory      32 MiB, set by the author (memory_mib)
  enabled     yes
MODULE_ADD_EXIT=0
$ docker restart prusa_connect        # 01:34:42Z
prusa_connect                         # Up 20 seconds (healthy) at 01:35:03Z
$ curl -s localhost:8080/api/v1/readiness
{"ready":true,"serving":4,"declared":4,"blocked":[],…}   # 01:34:45.890Z
… the module's files are serving … draws=true
… watching printer 07e893bb-… and driving http://10.0.0.136 at the brightness of brightness_schedule …
… the light's brightness is 128, the 00:00-23:59 window of America/Denver, where it is 18:34 MST
… the controller drives 16384 LEDs
… the controller is a 256x64 panel, so a frame is drawn as a rectangle
```

**What it was there to fix, and what the old build was doing.** The MK4 had finished its print and
its bed was still cooling, so a value moved every minute or two, which restarted the change clock;
30 s later the reading was stale again, and every one of those dips was painted as a light that had
lost contact. The log from the build this one replaced, and `/status` read a minute later:

```console
01:06:59Z  the reading from the printer Hermes MK4 is live again
01:07:38Z  the reading from the printer Hermes MK4 is stale
01:08:55Z  the reading from the printer Hermes MK4 is live again
01:10:23Z  the reading from the printer Hermes MK4 is stale
… and so on through 01:27:56Z …
$ curl -s …/status                              # 01:29:35Z
"state":"FINISHED", "age_seconds":128.317, "stale":true,
"frame":{"colour":{"r":70,"g":0,"b":0},"lit":0,"total":16384,"reason":"stale"}
```

**The same printer, after the restart, read on both sides of one of those former flaps.** The tree
moved for the last time at `01:37:29.135Z`; the reading crossed the 30 s horizon at about
`01:38:00Z`, which is the moment the old build painted `70,0,0`:

```console
01:37:51Z  /status  FINISHED  age=22.0     stale=false  frame 0,170,0  lit 0/16384  not-a-live-job
01:38:00Z            the reading ages past 30 s — the old build's dip into the stale red
01:38:16Z  /status  FINISHED  age=47.0     stale=true   frame 0,170,0  lit 0/16384  not-a-live-job
01:40:31Z  /status  FINISHED  age=181.999  stale=true   frame 0,170,0  lit 0/16384  not-a-live-job
```

And the panel's own answer, read at `01:40:31Z` while the reading was 182 s stale — not the
module's claim about it:

```console
$ curl -s http://10.0.0.136/json/state            # 01:40:31Z
bri 128  on true  mainseg 0
seg0 start 0 stop 256 on true col [0,170,0] n None fx 0
seg1 start 253 stop 256 on false
$ … /status  "last_send":{"at":"2026-11-19T01:39:45.227585555Z","ok":true}
```

Segment 0 covers columns 0–256 in `[0,170,0]` — the whole panel in the finished green — and the
write that put it there was the heartbeat repaint at `01:39:45.228Z`, while the reading was already
stale. From `01:38:00Z` to the last sample the reading did not recover, and the light did not move.

**The log said it once.** Between the restart and `01:40:31Z` the reading entered staleness at
`01:35:16Z`, came back three times as the bed moved, and entered it again at about `01:38:00Z`;
one line exists for all of that, and no `live again` line at all:

```console
$ docker logs prusa_connect 2>&1 | grep -a "the reading from the printer Hermes MK4"
01:35:16.133Z  the reading from the printer Hermes MK4 is stale
```

**Read again forty-six minutes later, at `02:26:04Z`, from the same recording.** The tree had moved at
`02:23:57.591Z` — the change clock the age is measured from — so the reading had come back at that
moment and aged past the 30 s horizon again since. The log had still written nothing for it:

```console
$ curl -s …/status                                  # 02:26:04Z
"state":"FINISHED", "age_seconds":126.543, "stale":true,
"frame":{"colour":{"r":0,"g":170,"b":0},"lit":0,"total":16384,"reason":"not-a-live-job"},
"last_send":{"at":"2026-11-19T02:25:45.969Z","ok":true}
$ curl -s http://10.0.0.136/json/state              # 02:26:04Z
bri 128  on true  mainseg 0
seg0 start 0 stop 256 startY 0 stopY 64 col [0,170,0] fx 0 frz true
$ docker logs prusa_connect 2>&1 | grep -a "the reading from the printer Hermes MK4" | wc -l
27          # 26 of them from the build this one replaced, the last line at 01:35:16.133Z
$ docker logs prusa_connect 2>&1 | grep -ac "live again"
13          # the latest at 01:32:26.992Z, before this build was installed
```

So the green held on the panel for the whole of that window, and a silence that had come and gone at
least once cost no line — what the two-minute samples above could only begin to show. The panel's own
JSON is the controller's account of what it holds, not a person's look at the light, and it does not
change the verdict on the word.

**What this run does not prove.** The live case — no print was started to put a *printing* job's
reading into staleness, so that half of the rule is proved by
`TestALiveJobIsStillDowngradedBySilence` and the tests beside it and not by this device. The word on
the panel is still unlooked at: no preview was fired, no DDP frame pushed, and `10.0.0.253` was not
written to.

**Thirteenth run, 2026-11-19 05:23Z, from `ea47a1b`.** The change that makes one look at the panel
enough: `POST /preview-pattern` draws one of three diagnostic pictures — `solid`, `border`, `quarters` —
over the same writer, establishing body, once-a-second DDP repeat and hand-back the finished word uses,
and both preview routes take `hold` in whole seconds (default 60, ceiling 300, refused past it by name).
Built from `git archive HEAD` for the reason run six gives; release
`/config/config.d/wled-release-ea47a1b`, whose `linux_amd64` digest is
`a3a9f6e01143a2e007ea8f8fad6b37ad5afb1daf617d42ed5c16d70a66b36d38` and is what the installed binary
reads back. `ui/` is unchanged between `e8bc059` and this commit, so the bundle was carried over from the
release it was last built into: `ui/index.js` and `ui/locales/en.json` are the same two digests run twelve
installed, and `module.yaml`'s digest is unchanged too — this change touches no setting. Ten files, which
`module add` counted back. The settings file was **not** touched.

```console
$ docker exec -u 10001 prusa_connect sh -c \
    "cd /config/config.d/wled-release-ea47a1b && sha256sum -c checksums.txt"
module.yaml: OK   README.md: OK   CHANGELOG.md: OK   LICENSE: OK
ui/index.js: OK   ui/locales/en.json: OK
wled-printer-status_linux_amd64: OK  … (four platforms)
$ docker exec prusa_connect /prusa_connect module add \
    github.com/nothingmn/wled-printer-status@0.1.0 \
    -from /config/config.d/wled-release-ea47a1b \
    -settings /config/config.d/wled-settings.json -approve
  checksums   10 files verified against checksums.txt
  memory      32 MiB, set by the author (memory_mib)
  enabled     yes
$ docker restart prusa_connect        # 05:23:12Z
prusa_connect                         # Up 20 seconds (healthy) at 05:23:34Z
$ curl -s localhost:8080/api/v1/readiness
{"ready":true,"serving":4,"declared":4,"blocked":[],…}   # 05:23:36Z
… the module's OpenAPI document is served … paths=9        # eight before this release
… watching printer 07e893bb-… and driving http://10.0.0.136 at the brightness of brightness_schedule …
… the controller drives 16384 LEDs
… the controller is a 256x64 panel, so a frame is drawn as a rectangle
```

**The three pictures, fired once each with `hold=15` and read on both sides.** The panel was holding the
steady finished green and `live:false` before the first one; every firing was followed by a read of
`/json/info` once a second for twenty seconds, and by a read of `/json/state` five seconds in, while the
picture was up. `10.0.0.253` was not touched. Nothing here says a pixel was drawn: `/json/info`'s `live`
is the only thing this firmware reports about a pushed frame, and it says the controller took the
datagrams into the buffer it draws instead of its segments.

```console
$ curl -s http://10.0.0.136/json/info | grep -o '"live":[a-z]*'
"live":false                                  # 05:23:44, before anything
$ curl -s -X POST …/preview-pattern?pattern=solid&hold=15
{"answers":"does DDP draw at all: …","controller":"http://10.0.0.136","holds_seconds":15,"pattern":"solid"}
05:23:45 … 05:24:00  "live":true              # sixteen samples, one a second
05:24:01 … 05:24:05  "live":false             # seventeen seconds after the fire

$ curl -s -X POST …/preview-pattern?pattern=border&hold=15   # 05:24:17
05:24:18 … 05:24:33  "live":true
05:24:34 … 05:24:39  "live":false
$ curl -s http://10.0.0.136/json/state        # 05:24:22, five seconds in
seg0 start 0 stop 256 rows 0..64 on true  n 'border'

$ curl -s -X POST …/preview-pattern?pattern=quarters&hold=15 # 05:24:41
05:24:42 … 05:24:58  "live":true
05:24:59 … 05:25:02  "live":false
$ curl -s http://10.0.0.136/json/state        # 05:24:47, five seconds in
seg0 start 0 stop 256 rows 0..64 on true  n 'quarters'
```

Each hold ran the fifteen seconds it was given and no longer, and the hand-back was said once per
firing — `the picture is off the panel and http://10.0.0.136 reports live=false: the light is back on
its own segments` (the first at `05:24:02.236Z`) — because the line names "the picture" and not the word
now that a pattern holds the panel through the same path.

**The refusals, on the live instance rather than in a test.**

```console
$ curl -s -o /dev/null -w "%{http_code}\n" -X POST …/preview-pattern?pattern=quaters
400
{"code":"…/PREVIEW_PATTERN_UNKNOWN","title":"This module draws no diagnostic pattern of that name",
 "detail":"this module draws no pattern of that name: \"quaters\" is not one of them: this module draws
            solid, border and quarters","params":{"pattern":"quaters"},"status":400}
$ curl -s -X POST …/preview-pattern?pattern=solid&hold=301
{"code":"…/PREVIEW_HOLD_OUT_OF_RANGE","detail":"… 301 seconds is outside it; it defaults to 60 and may
  not exceed 300","params":{"hold":"301"},"status":400}
$ curl -s -X POST …/preview-finished?hold=abc
{"code":"…/PREVIEW_HOLD_OUT_OF_RANGE","detail":"… \"abc\" is not a whole number of seconds; it defaults
  to 60 and may not exceed 300","status":400}
```

Nothing was fired by any of the three, and the steady frame was untouched: the panel read back at
`05:25:12Z` exactly as it had at `05:23:44Z` — `on true`, `bri 128`, `mainseg 0`, seg0 `start 0 stop 256`
rows `0..64` `on` `col [0,170,0]` `fx 0` `frz true` and no name on it, seg1 `253..256` off, `live:false`.
The release directory for `87fb786` was **left in place**, against the usual practice of removing the one
an install replaced: the operator's own look at the panel is tomorrow and a release that is known to run
is worth more overnight than a tidy volume.

**What this run does not prove.** That a pixel of any of the three pictures was drawn, or that the
letters of the word are. The word itself was **not** fired: `preview-finished` without a `hold` still
holds for its own minute by the tests behind it, and putting it up here would have spent the operator's
experiment. What is proved is that the routes answer, that the frames leave, that the controller takes
each one into realtime for the hold asked for, and that the light comes back to the picture it was
holding. `AGENTS.md` §8 says the rest.

**Fourteenth run, 2026-11-19 05:51Z, from `8b67c7e`.** The change from *a panel filled with the state
colour* to *a composed picture*, and the setting that arranges it: a twelve-row bar at the bottom of the
panel, the percentage in digits, and the job's own name on the other half of a fifteen-second rotation —
`POST /preview-frame` draws exactly what a printing panel draws, from the same function. Built from
`git archive HEAD` for the reason run six gives, into a build tree of its own under `/tmp` with `.core`
pointed at the mounted core checkout, because the release directory is checksummed and must carry ten files
and nothing else. Release `/config/config.d/wled-release-8b67c7e`; its `linux_amd64` digest is
`967bdf068d37f143d9d9fadadf915727c3202df1ea9924930e189adf72fe5318` and is what the installed binary reads
back.

`ui/` was carried from `wled-release-ui-e8bc059` as in the runs before this one, since the bundle is built
outside this repository. `module add` answered `10 files verified against checksums.txt`,
`memory 32 MiB, set by the author`, `enabled yes`, and printed **nine** settings, the new one among them:

```console
$ docker exec prusa_connect /prusa_connect module add \
    github.com/nothingmn/wled-printer-status@0.1.0 \
    -from /config/config.d/wled-release-8b67c7e \
    -settings /config/config.d/wled-settings.json -approve
  setting     layout (object, default {}). The arrangement of a panel, as one JSON object with the keys
              bar, bar_band, bar_rows, percent, name, remaining and rotate_seconds. …
  checksums   10 files verified against checksums.txt
  memory      32 MiB, set by the author (memory_mib)
  enabled     yes
MODULE_ADD_EXIT=0
```

The settings file was **not** changed: it carries no `layout` key, which is the case this release had to
work for an existing install. `docker restart prusa_connect` at **05:51:10Z** was `healthy` twelve seconds
later, readiness answered `serving 4 declared 4 blocked []` with this instance `ready`, and the OpenAPI
document reported **paths=10** — nine before this release:

```console
… wled-printer-status 0.1.0 is serving on stdio as pid 23
… the module's OpenAPI document is served … paths=10
… the module's files are serving … draws=true
… watching printer 07e893bb-… and driving http://10.0.0.136 at the brightness of brightness_schedule …
… the light's brightness is 128, the 00:00-23:59 window of America/Denver, where it is 22:51 MST
… the controller drives 16384 LEDs
… the controller is a 256x64 panel, so a frame is drawn as a rectangle
```

**The new picture was on the panel before any preview was fired**, because the printer was printing. Read at
**05:51:32Z**, twenty seconds after the start:

```console
$ curl -s …/status
"frame":{"colour":{"r":0,"g":170,"b":0},"lit":983,"total":16384,"percent":6,
         "name":"5_1_0.4n_0.2mm_PLA_MK4IS_1h52m.gcode","reason":"progress"},
"last_send":{"at":"2026-11-19T05:51:13.3222857Z","ok":true}
$ curl -s http://10.0.0.136/json/info   →  live true
$ curl -s http://10.0.0.136/json/state
seg 0  start 0 stop 256  rows 0..64  on true   fx 0  n None  col [[0,0,0],…]
seg 1  start 15 stop 256 rows 0..64  on false  fx 0  n None  col [[0,0,0],…]
```

That is the composed establishing body, exactly as designed: segment 0 the whole panel in the background
colour with its name **cleared**, segment 1 switched off, and `live: true` because the pixels are on their
way over DDP. `last_send` is the one JSON write that put the body there; `percent` and `name` are the two
facts the picture is composed from, on the route for the first time.

**`preview-frame`, fired twice.** The first at `percent=41&name=MK4IS&hold=120`, the second at
`percent=100&hold=120` — three digits and no name, which is the other half of the same question:

```console
$ date -u +"BEFORE %H:%M:%SZ"          # 05:51:41Z   live=true before the fire
$ curl -s -X POST "…/preview-frame?percent=41&name=MK4IS&hold=120"      # FIRED 05:51:41Z
{"controller":"http://10.0.0.136","halves":["41%","MK4IS"],"holds_seconds":120,
 "layout":{"bar":true,"bar_band":"bottom","bar_rows":12,"percent":true,"name":true,
           "rotate_seconds":15},
 "name":"MK4IS","percent":41,"rotation_seconds":15}
$ …/status    # 05:51:46Z
"last_send":{"at":"2026-11-19T05:51:41.531862518Z","ok":true}
$ …/json/state  # 05:51:43Z, inside the hold
seg 0  start 0 stop 256 rows 0..64 on true  n None
seg 1  start 15 stop 256 rows 0..64 on false
$ …/json/info at 05:51:43Z, 05:53:44Z, 05:53:49Z  →  live true, live true, live true

$ curl -s -X POST "…/preview-frame?percent=100&hold=120"                # FIRED 05:53:49Z
{"controller":"http://10.0.0.136","halves":["100%"],"holds_seconds":120,
 "layout":{…},"name":"","percent":100,"rotation_seconds":15}
$ …/status    # 05:53:54Z
"last_send":{"at":"2026-11-19T05:53:49.694789608Z","ok":true}
$ …/json/info at 05:53:51Z, 05:55:52Z, 05:55:57Z  →  live true, live true, live true
```

Both answered `202`, both echoed the layout this instance is running, and both wrote **one** body — the
establishing body — at the second they were fired. `halves` is what a person can read without walking
anywhere: the first preview shows the digits and then the name, the second only the digits, because a
preview with no name has nothing to rotate to.

**What the reads prove and what they cannot.** `live` is true before, during and after both previews — and
that is this release working, not the preview's hand-back: the printer is printing, so the panel is held
over DDP by the composed picture the whole time, and the module's own picture is what it went back to when
each hold ended. Twenty samples three seconds apart, taken straight after the second hold, show the steady
state and are the strongest evidence in this record that the transport decision is the right one:

```console
05:56:20Z live=true 10 | 2026-11-19T05:53:49.694789608Z | progress
… sixteen more samples, live=true, percent 10 …
05:57:03Z live=true 11 | 2026-11-19T05:53:49.694789608Z | progress
… four more samples, live=true, percent 11 …
```

Fifty-eight seconds, the progress moved from 10 % to 11 %, the panel stayed in realtime throughout, and
`last_send` did not move at all: **the picture changed and not one JSON body was written.** The composed
frame is carried entirely by the once-a-second push, and the controller's web server saw exactly two writes
in the four minutes the two previews ran.

What this run does **not** prove is the picture itself. No pixel is readable on `14.7.0-mdev` — the segment
keys still carry no `i` array — so whether the bar is twelve rows at the bottom, whether `41%` is 85 by 35
pixels in the right place, whether the name reads `MK4IS 0.4N 0.2MM PLA` on a real light, and whether the
rotation actually swaps every fifteen seconds are all questions for a person standing in front of it.
`AGENTS.md` §8 says so; the tests say what the arithmetic is.

**Fifteenth run, 2026-11-19 11:31Z, from `3c12d89`.** The change that lets one printer sit on several
lights: `81281f2` moves the binding set out of the settings and into this module's own store, refuses a
change it cannot write there, and teaches the store to step a version 3 document up to 4; `3c12d89` is
the README sentence that still claimed a printer appears in at most one binding. Version **0.1.0**,
release `/config/config.d/wled-release-3c12d89`, and its `linux_amd64` digest is what the installed
binary reads back:

```console
$ docker exec prusa_connect sha256sum \
    /config/config.d/modules/wled-printer-status/bin/wled-printer-status_linux_amd64 \
    /config/config.d/wled-release-3c12d89/wled-printer-status_linux_amd64
e6bb2abc7f09cc5e83091d821e74a3b8f54bfa711bb180d7ac046497508d8f18  …/bin/wled-printer-status_linux_amd64
e6bb2abc7f09cc5e83091d821e74a3b8f54bfa711bb180d7ac046497508d8f18  …/wled-release-3c12d89/wled-printer-status_linux_amd64
$ docker exec prusa_connect sh -c 'cd /config/config.d/wled-release-3c12d89 && sha256sum -c checksums.txt' | tail -3
wled-printer-status_linux_amd64: OK
wled-printer-status_linux_arm64: OK
wled-printer-status_windows_amd64.exe: OK
```

**The store, in five lines.** A release that changes the schema is the one deploy where a healthy module
proves nothing, so the log is the evidence, read in order:

```console
$ docker logs prusa_connect 2>&1 | grep -a 'wled-printer-status' | grep -a 'schema version'
10:24:48Z  … the store was written for the first time …, from the settings, at schema version 3 …
11:13:58Z  … was upgraded from schema version 3 to 4 …
11:13:58Z  … the set this start runs is the one in this module's own store …, schema version 4 (1 bindings) …
11:31:14Z  … schema version 4 (1 bindings) …
11:31:48Z  … schema version 4 (2 bindings) …
```

The document was seeded at version 3 when this module first owned its settings; the release before this
one stepped it to 4 in place; the install's own restart at **11:31:14Z** read the migrated document with
one binding; the second binding was added after that; and the core restart at **11:31:47Z** — read off
`bedmesh` and `storage:local` going `starting`→`ready` in the same second, so it is the container and
not the module alone — brought this instance up at **11:31:48Z** on **two** bindings. The store
therefore carried two bindings across a restart, which is what a schema change has to survive:

```console
$ curl -s …/bindings | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d["count"], d["persisted"], [b["id"] for b in d["bindings"]])'
2 True ['07e893bb-b922-4ff0-9ad3-061b957400c3', '07e893bb-b922-4ff0-9ad3-061b957400c3@10.0.0.253:whole']
$ docker exec prusa_connect cat /config/config.d/module-data/wled-printer-status/config.json \
    | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d["version"], len(d["bindings"]), {k: len(v) for k, v in d.items() if isinstance(v, list) and k != "bindings"})'
4 2 {'states': 0, 'moments': 0, 'layouts': 0, 'windows': 0, 'pauses': 0}
```

Two bindings and five empty tables at version 4 on disk: the same printer on the panel
(`http://10.0.0.136`, the whole controller) and on the desk strip (`http://10.0.0.253`) at once, which
is the case the settings file could not express and the locked requirement §2.2 asks for. Health after
the restart, read at 11:39Z:

```console
$ docker ps --format '{{.Names}} {{.Status}}' | grep prusa_connect
prusa_connect Up 8 minutes (healthy)
$ curl -s localhost:8080/api/v1/readiness
{"app":"served","discovery":"announcing","ready":true,"serving":4,"declared":4,"blocked":[],
 … {"resource":"sink:wled-printer-status","state":"ready","required":false,"serving":true}]}
```

**The way back** is the release this one replaced, `714417b`, copied out before the install as
`/home/robchartier/wled-rollback/wled-release-0.1.0-714417b-20261119T112841Z.tar.gz` (24 MB) with
`ROLLBACK-714417b.md` beside it: that file carries the exact `module add … -from …` line and the digest
of the binary it restores, `c7ea4fa7a66b351342069190d1bad1c236c7bc762cd2b9d3cc5810f897d37a2a`. The store
is not a second complication on this step — `714417b` is the build that wrote schema 4 in the first
place, the 11:13:58 upgrade above being its own start — so a binary-only rollback lands on a document
it reads. One thing to know before reaching for `LATEST`: it still names the `8b67c7e` archive, which is
three releases behind what runs.

The rest of this run is the functional check of the two-light install, on 2026-11-19 between 04:33 and
04:40 MST. Every read route answered, a state's word reached the panel and the panel's own `/json/state`
carried it as the segment's name, a pause put both lights out and a resume brought them back within the
second, a brightness window moved both controllers to 40 and back to 128, and both lights were left solid
green at 128 with the store's five tables empty again. Three routes did **not** answer:
`preview-finished`, `preview-pattern` and `preview-frame` each refused with `PREVIEW_NOT_RUNNING` —
*this module is not driving exactly one light* — which is `main.go`'s own rule, so on a two-light install
the diagnostic ladder cannot be fired at all. The frames' own pixels were not read either — nothing on
this firmware exposes them — and both lights are still on the printer's finished green, so a composed
layout and a progress bar remain questions for a print.

**Sixteenth run, 2026-11-19 12:17Z, from `69cc791`.** The two defects the two-light sweep found, and the
one test the previous wave left uncommitted. `5104b90` makes all four preview routes fire at every light
the module drives and makes a word set on a state that is already showing hold from the write; `1d5bb59`
and `69cc791` are **test-only** commits on top of it, and that is a claim with a command behind it rather
than a hope:

```console
$ docker run --rm … golang:1.26.11 sh -c 'CGO_ENABLED=0 go build -trimpath -o /work/…-check/from-5104b90 .'   # from git archive 5104b90
$ sha256sum …/from-5104b90 …/wled-build-69cc791/release/wled-printer-status_linux_amd64
2f3daf3ea55bf79e4b23ef9921e81fd5fa7eea2121c8100093c5c25d066625ab  …/from-5104b90
2f3daf3ea55bf79e4b23ef9921e81fd5fa7eea2121c8100093c5c25d066625ab  …/release/wled-printer-status_linux_amd64
```

Version **0.1.0**, release `/config/config.d/wled-release-69cc791`, ten files and `sha256sum -c` OK inside
the container before the install. **The UI half was not rebuilt**: `git diff --stat 3c12d89..HEAD -- ui/`
is empty, so the bundle was copied out of the running release and `cmp` reports it byte-identical —
`ui/index.js` `a46ac73bf2bf5d224f9313f63e3059dd43a2f31d911d833dcfb7fb43859d118a` on both sides. The same
version was accepted for the fourth time (`installed … as sink:wled-printer-status … checksums 10 files
verified … enabled yes`), and one restart at **12:17:46Z** brought it back healthy in six seconds with the
status route answering `200`.

**The store, in three lines** — the schema change is the part a healthy module proves nothing about:

```console
$ docker logs prusa_connect 2>&1 | grep -a 'was upgraded from schema version'
12:17:48.995Z … the configuration store at …/config.json was upgraded from schema version 4 to 5 …
12:17:48.997Z … the set this start runs is the one in this module's own store …, schema version 5 (2 bindings) …
12:17:48.984Z … wled-printer-status 0.1.0 is serving on stdio as pid 15 …
```

**The ladder, on both lights, at once.** `POST /preview-pattern?pattern=quarters&hold=8` answered `202`
with `count: 2` and both lights named, each carrying its own sentence about what the picture answers; then
`?pattern=solid&hold=8&light=10.0.0.253` answered with **one** light. What the lights were actually
displaying was taken off the wire, because a picture never appears in `/json/state`: the module writes one
establishing body that names the picture and sends the pixels themselves over DDP, and no firmware here
reads pixels back. A `tcpdump` in a `--net=host` container on `udp port 4048`, reassembled by DDP offset,
is the picture.

```console
WINDOW A — the quarters request, aimed at both lights
  10.0.0.136 panel: 245 datagrams, 16384 pixels
    corners: TL (255,0,0)  TR (0,255,0)  BL (0,0,255)  BR (255,255,255)
    areas:   (255,0,0) x4096, (0,255,0) x4096, (0,0,255) x4096, (255,255,255) x4096
  10.0.0.253 strip: 8 datagrams, 48 pixels
    band 0 (pixels  0-11): (255,0,0) x12
    band 1 (pixels 12-23): (0,255,0) x12
    band 2 (pixels 24-35): (0,0,255) x12
    band 3 (pixels 36-47): (255,255,255) x12
WINDOW B — the solid request, aimed at the strip alone
  10.0.0.136 panel: 0 datagrams, 0 pixels          ← untouched, which is what light= promises
  10.0.0.253 strip: 8 datagrams, 48 pixels, all (255,255,255)
```

The controller's own documents agree with the picture and cannot show it: during the hold both answered
`/json/info` `live=true lm='DDP'`, and `/json/state` carried the establishing body — `seg0 n='quarters'
start=0 stop=48` on the strip, `0..256` with the remainder segment off on the panel. After the hold both
were back on their own segments, green, with the name cleared.

**A word on the long-running state.** The printer finished hours before this, so `FINISHED` had been in
force far longer than any hold — the case that used to spend the word before anybody could read it.

```console
$ PUT …/states/FINISHED {"word":"QA","holds":"60s"}
  {"applied":true,"note":"The word \"QA\" is on the light now, for 60 more second(s), until 2026-11-19T12:22:35Z. …"}
$ curl -s http://10.0.0.136/json/state    # 3 s later, the panel's own document
  seg0 n='QA' start=0 stop=256     /json/info live=true lm='DDP'
$ GET …/frames    panel png 259 B  ef8511f3c365de64  →  331 B  42246fdc7d0f1efe     (changed)
                  strip png  86 B  03fe38e28f44551d  →   86 B  03fe38e28f44551d     (a strip cannot draw a word)
$ docker exec prusa_connect cat …/config.json
  {"state": "FINISHED", "word": "QA", "holds": "30s", "set_at": "2026-11-19T12:24:33.160940222Z"}
$ DELETE …/states/FINISHED   → removed true
```

The word appears and the panel goes back to plain green — with one thing to know: the return is not always
at the instant the hold ends. Measured three times, the panel was back to green at the hold's end once and
**13 s and 15 s late** twice, and in that gap it is not green but black — the establishing body with the
word's name still on segment 0, waiting for the module's sixty-second repaint heartbeat to put the steady
frame back. The frame digest returns to `ef8511f3c365de64` exactly.

**What was left running.** Both lights solid green at brightness 128 with the finished colour
(`[0,170,0]`), `live=false` on both, no pause (`GET /pause` → `paused false, pauses []`), no state
overrides (`GET /states` → 12 rows, 0 overridden), and the store at version 5 with two bindings and five
empty tables. Both controllers' `/json/cfg` were compared against the copies taken before the install and
are **byte-identical** — 5,182 bytes for the panel, 4,185 for the strip, the `ins` members stored empty in
both. Configuration did not move; only pixels did.

**The way back** is `3c12d89`, copied out before the install as
`/home/robchartier/wled-rollback/wled-release-0.1.0-3c12d89-20261119T1212Z.tar.gz` with
`ROLLBACK-3c12d89.md` beside it. That note matters more than the earlier ones: `3c12d89` writes **schema
4** and refuses a version-5 document by name, so its restore puts the saved version-4 store back **before**
the restart. Rolling back also gives up the ladder — `3c12d89` still refuses every preview route on a
two-light install.

**Ninth run, 2026-11-19 12:39Z, from `531e73c`.** The word's hand-back, which the run above left as a
measured defect: a word set on a state already in force stood in front of a frame that had already been
delivered, and when its hold ran out the steady frame was compared against that frame, found equal, and
suppressed as "already shown" — leaving the panel on the word's black establishing body, name and all,
until the next write that was not suppressed. How long that took was nothing but where the
sixty-second heartbeat happened to sit: on the stroke once, 13 s and 15 s late twice. The writer now
carries `overwritten`, set where a held word takes the light and cleared where a frame lands, and the
suppression asks it first.

The four binaries were built from `git archive HEAD`. **The UI bundle was not rebuilt**: `git diff --stat
69cc791..HEAD -- ui/` is empty for this range, so the bundle the release carries is the installed one,
and that was proved rather than asserted:

```console
$ sha256sum /tmp/ui-check/{installed,release}/ui/index.js
a46ac73bf2bf5d224f9313f63e3059dd43a2f31d911d833dcfb7fb43859d118a  …/installed/ui/index.js
a46ac73bf2bf5d224f9313f63e3059dd43a2f31d911d833dcfb7fb43859d118a  …/release/ui/index.js
CMP-OK: installed ui/index.js is byte-identical to the installed release's
$ … /ui/locales/en.json
9e88bd7d4d7858db426321f98fc34c61cbc96bc616c69bbcfe940bc9e44d00a9  (both, cmp OK)
$ sha256sum -c checksums.txt
10 files: OK
```

`module add … -from /config/config.d/wled-release-531e73c -settings … -approve` answered
`MODULE_ADD_EXIT=0`, `memory 32 MiB`, `enabled yes`, the same two egress lines run eight describes;
`docker restart prusa_connect` at **12:39:52Z** was `healthy` at **12:40:09Z**, `/status` answered `200`,
and the installed binary reads back `17189a45fcd9094b88dfab6c2bfb2dc0b277b124eab79fd69a564686da61d6a3`,
which is the release's own line. The log said `schema version 5 (2 bindings)` and named both controllers.

**The proof is the panel's own `/json/state` read ten times a second across the stroke**, because that
document carries the body the panel falls back to the moment live mode ends — so it is black exactly while
it says black, and green exactly while it says green. Two rounds, each with the hold the module stamped in
its own log line:

| round | `holds` | word's body written | hold ended (module's own words) | steady body written | late by |
|---|---|---|---|---|---|
| A | 60 s | 12:40:45.051 `col [0,0,0] n 'QA'` | `…T12:41:44Z` | **12:41:44.289** `col [0,170,0] n None` | **0.29 s** |
| B | 30 s | 12:42:28.938 `col [0,0,0] n 'QA'` | `…T12:42:58Z` | **12:42:58.069** `col [0,170,0] n None` | **0.07 s** |

Round B is the shape the defect was measured in — a thirty-second hold set fifteen seconds after the
panel was last painted, so the heartbeat was still fifteen seconds away at the stroke — and it is the one
that used to be black for the rest of that cycle. The segment name is cleared in the same body, which is
the other half of the hand-back on this firmware.

**What was left running.** Both lights solid green at brightness 128 (`[0,170,0]`) with no segment name,
`live=false`, no pause (`/pause` → `paused false`), the state table empty again (`DELETE /states/FINISHED`
→ `removed true`, row `overridden false`), the store at version 5 with two bindings and no rows, and both
controllers' `/json/cfg` **byte-identical** to the copies taken before the install.

**The way back** is `69cc791`, copied out before the install as
`/home/robchartier/wled-rollback/wled-release-0.1.0-69cc791-20261119T1238Z.tar.gz` with
`ROLLBACK-69cc791.md` beside it. Unlike the `3c12d89` note, this restore needs no store step: both builds
read schema 5, so the saved document is for a damaged file and not for a version mismatch.

**Seventeenth run, 2026-11-19 18:15Z, from `905454f`.** The release that puts a page on the module and
gives the module a version of its own: **`0.2.0`**, tagged `v0.2.0` as a new annotated tag on a new commit.
`v0.1.0` is published at `701d090` — the eight-route module from before all of this — and a published tag
is not ours to rewrite, so this is a second tag rather than a moved one.

**The version is two declarations, and they have to agree.** After the bump the manifest said `0.2.0` while
the process still reported `0.1.0` through `extension.Version`. The host compares the manifest's `version`
against the child's own answer as exact text, so that pair installs cleanly and then **never starts**. Both
moved in the one commit:

```console
$ git diff c621984..905454f --stat
 CHANGELOG.md           | 10 ++++++----
 main.go                |  2 +-
 module.yaml            |  2 +-
 ui/src/locales/en.json |  2 +-
 4 files changed, 10 insertions(+), 6 deletions(-)
```

**The way back was taken before anything was installed**, out of the config volume and through the
container: `wled-release-0.1.0-531e73c-20261119T1812Z.tar.gz` (sha256 `4a6fe68d…`, ten files `OK` on a
`sha256sum -c` of the archive re-extracted after packing), the store at schema 5 with two bindings
(`config-v5-531e73c-20261119T1812Z.json`), the settings file every install since `9f95c11` has been given
(`882f2cb6…`), and both controllers' `/json/cfg`, `/presets.json` and `/json/state` under
`lights-531e73c-20261119T1812Z/`, every file proved to parse and every `nw.ins` stored empty. The installed
binary was `cmp`-identical to the archive's `linux_amd64` (`17189a45…`), so what is archived is the thing
that was running. `ROLLBACK-531e73c.md` carries the restore line and the store half.

**The bundle was rebuilt, not carried forward.** `ui/src` has changed enormously since the installed
`a46ac73b…` — one screen, 22,803 bytes:

```console
$ … pnpm install --frozen-lockfile && pnpm run check && pnpm run build
svelte-check found 0 errors and 0 warnings
dist/ui/index.js  217.15 kB │ gzip: 43.68 kB
✓ built in 573ms
$ sha256sum ui/dist/ui/index.js ui/dist/ui/locales/en.json
4434ddc6261bd40ad2b9a62dd9304c0f316d857932ed766d113e099ffaa87872  ui/dist/ui/index.js
bac1d3ccaa7093bd187bddea4401ed23efd3655e9e24d05b50a162378755b7c0  ui/dist/ui/locales/en.json
$ sha256sum /config/config.d/modules/wled-printer-status/bundle/ui/index.js   # what was installed
a46ac73bf2bf5d224f9313f63e3059dd43a2f31d911d833dcfb7fb43859d118a
```

The two digests differ, which is the point: a stale copy would have passed every checksum in the release and
shown the operator the previous screen.

**What the bundle carries is seven screens, not eight.** `ui/src/screens.ts` lists seven, and the built
bundle holds each one's id and its catalogue key. `screen.settings` appears nowhere in it, because the
requirements' §2.8 Settings screen has no component in this tree — the rail a person sees has seven
entries. Read from the shipped file rather than from the source:

```console
$ python3 - <<'PY'   # findall r'id: "([a-z-]+)",\s*key: "(screen\.[a-z_]+)"' over ui/dist/ui/index.js
SCREENS entries in the shipped bundle: 7
  right-now    screen.right_now
  bindings     screen.bindings
  events       screen.events
  colours      screen.colours
  layout       screen.layout
  devices      screen.devices
  diagnostics  screen.diagnostics
screen.settings present: False
PY
```

**The install**, the same four flags as every run above:

```console
$ docker exec prusa_connect /prusa_connect module add \
    github.com/nothingmn/wled-printer-status@0.2.0 \
    -from /config/config.d/wled-release-905454f \
    -settings /config/config.d/wled-settings.json -approve; echo "MODULE_ADD_EXIT=$?"
installed com.github.aux.nothingmn.wled-printer-status/wled-printer-status as sink:wled-printer-status
  files       config/config.d/modules/wled-printer-status
  namespace   com.github.aux.nothingmn.wled-printer-status
  checksums   10 files verified against checksums.txt
  … one `setting` line per option in module.yaml …
  connects to the host in the setting controller_address port 80 from the process: …
  connects to the host in the setting controller_address port 4048 from the process: …
  granted     org:local#viewer@sink:wled-printer-status for observe. The viewer role also carries
              view_resource, read_storage
  memory      32 MiB, set by the author (memory_mib)
  enabled     yes
MODULE_ADD_EXIT=0
```

It asked for nothing new — no `new` line, because `observe` was already granted and the data directory
already approved. `docker restart prusa_connect` came back `Up (healthy)`, `/status` answered `200`, and the
log named the release from both ends:

```console
$ docker logs --since 5m prusa_connect | grep -E 'is serving on stdio|plugin_version|own store'
msg="wled-printer-status 0.2.0 is serving on stdio as pid 32" instance=sink:wled-printer-status
msg="… the set this start runs is the one in this module's own store at
     /config/config.d/module-data/wled-printer-status/config.json, schema version 5 (2 bindings);
     the settings are a migration source and are not applied again"
… plugin=wled-printer-status plugin_version=0.2.0 contract=1.1.0 wire_minor=0 mount=/api/v1/modules/…
msg="the module's OpenAPI document is served" … paths=26
msg="the module's files are serving" … draws=true
```

**Every route the work names, answered by the installed build.** Reads only, one line of each body, taken
at 18:16–18:18Z:

| route | status | what it answered |
|---|---|---|
| `GET /status` | `200` | two bindings, `PRINTING`, frame `[0,170,0]`, `reason: progress`, `stale: false` |
| `GET /controllers` | `200` | both lights by address, `10.0.0.136` and `10.0.0.253` |
| `GET /controllers/10.0.0.136` | `200` | `segments_known: true`, the panel's own segment list |
| `GET /controllers/10.0.0.253` | `200` | `segments_known: true`, the strip's six zones |
| `GET /printers` | `200` | `Hermes MK4`, `bound_count: 2` |
| `GET /bindings` | `200` | `count: 2`, `persisted: true`, `source: … its own configuration store` |
| `GET /bindings/07e893bb-…` | `200` | the panel's binding, `whole: true`, `brightness: 128` |
| `GET /bindings/07e893bb-…@10.0.0.253:whole` | `200` | the strip's binding, minted rather than the printer's id |
| `GET /states` | `200` | twelve rows: ten states, then `Disconnected` and `Stale` as conditions |
| `GET /states/PRINTING` | `200` | `[0,170,0]`, `holds: forever`, `word_shows: none` |
| `GET /lights/10.0.0.136/moments` | `200` | the panel's moment table, one printer on it |
| `GET /lights/10.0.0.253/moments` | `200` | the strip's, keyed on the composite binding id |
| `GET /bindings/…/layout` | `200` | `bar: true`, `bar_band: bottom`, every value with its origin |
| `GET /bindings/…@10.0.0.253:whole/layout` | `200` | the strip's own layout, not the panel's |
| `GET /windows` | `200` | `count: 0`, `judged_in: America/Denver` |
| `GET /pause` | `200` | both bindings `paused: false`, `effect: off` |
| `GET /frames` | `200` | panel 256 × 64 PNG 379 B, strip 48 × 1 PNG 91 B, both `last_send.ok: true` |
| `GET /devices/10.0.0.136/presets` | `200` | `presets_known: true`, the panel's own presets by slot and name |
| `GET /devices/10.0.0.253/presets` | `200` | `presets_known: true`, three presets, slot 0 deliberately absent |
| `GET /devices/10.0.0.136/snapshot` | `200` | `count: 0`, `persisted: true` |
| `GET /devices/10.0.0.253/snapshot` | `200` | `count: 0`, `persisted: true` |

`/devices/{name}` keys on the **light**, not on the printer: asking it for the printer's device id answers
`404 CONTROLLER_NOT_FOUND` with "This module drives no light of that name. It drives 10.0.0.136,
10.0.0.253." — which is the route telling you the right parameter. The presets route is new in this release
and answered `ROUTE_NOT_FOUND` on the build it replaced.

**Identify, fired exactly once, on the strip** — never on the panel. It answers `202` as the flash starts,
because ten seconds does not fit the host's ten-second call, so the proof is the light's own document read
on both sides of it:

```console
$ curl -s http://10.0.0.253/json/state | jq -c '{on,bri,seg0:{col:.seg[0].col[0],stop:.seg[0].stop}}'
{"on":true,"bri":128,"seg0":{"col":[0,170,0],"stop":23}}           ← 18:16:52Z
$ curl -s -o id.json -w '%{http_code}' -X POST …/devices/10.0.0.253/identify
202
$ jq -c . id.json
{"controller":"http://10.0.0.253","light":"10.0.0.253","flashing":true,"already":false,"flashes":5,
 "seconds_per_state":1,"seconds":10,"was_on":true,"ends_at":"2026-11-19T18:17:02Z","note":"…"}
$ sleep 14; curl -s http://10.0.0.253/json/state | jq -c '{on,bri,seg0:{col:.seg[0].col[0],stop:.seg[0].stop}}'
{"on":true,"bri":128,"seg0":{"col":[0,170,0],"stop":23}}           ← 18:17:06Z
$ sha256sum /tmp/strip-{before,after}.json     # the same document, volatile members dropped
6faddf76f34f19e490ca153ed17d2e3c94c376b1ac2b4c2fc3fc3a44c2c3bc34  /tmp/strip-before.json
6faddf76f34f19e490ca153ed17d2e3c94c376b1ac2b4c2fc3fc3a44c2c3bc34  /tmp/strip-after.json
```

The module's own log said when it was back: `the light went on and off 5 times, one second in each state,
and was put back as it was found: power on, brightness 128, 6 segment(s)` at `18:17:02.922Z`.

**The page is served, and it is the bytes that were shipped.** The bundle root is the module's `ui/`, and
`ui.entry` is `ui/index.js` relative to it, so the asset sits at the doubled `ui/ui`:

```console
$ curl -s -o /tmp/served.js -w 'HTTP %{http_code}  size=%{size_download}\n' \
    http://10.0.0.160:8080/api/v1/modules/com.github.aux.nothingmn.wled-printer-status/ui/ui/index.js
HTTP 200  size=217153
$ sha256sum /tmp/served.js /home/robchartier/build-wled-905454f/release/ui/index.js
4434ddc6261bd40ad2b9a62dd9304c0f316d857932ed766d113e099ffaa87872  /tmp/served.js
4434ddc6261bd40ad2b9a62dd9304c0f316d857932ed766d113e099ffaa87872  …/release/ui/index.js
$ curl -s -o /dev/null -w 'HTTP %{http_code}  size=%{size_download}\n' \
    http://10.0.0.160:8080/            # the Connect SPA, not the module
HTTP 200  size=3431
```

**What was left running.** Both lights on at **brightness 128** with the printing colour `[0,170,0]` and a
progress fill — 49 % of the print, so a bar rather than a solid fill: 8,028 of 16,384 pixels on the panel and
23 of 48 on the strip. Nothing paused (`/pause` → both `paused: false`), every table in the store empty
(`states`, `moments`, `layouts`, `windows`, `pauses` all `0`), the store at schema 5 with two bindings, and
both controllers' `/json/cfg` **byte-identical** to the copies taken before the install. One measured
wrinkle worth knowing: the strip was at `bri 255` when the rollback was taken and the module's start write
put it back to `128`; the brightness is written once at start and never touched again, so a controller
somebody else dimmed stays where they left it until the next restart.

**What this run does not prove.** That a person saw the page — **no browser was opened against it.** What is
proved is that the asset answers `200`, is 217,153 bytes, hashes to the shipped file, and carries seven
screens; whether those seven draw correctly is what the browser was not used for. Nor does it prove the
panel is painting: its `/json/state` segment colour is the establishing body (`[0,0,0]`, `stop 256`),
because a panel's frame arrives over DDP, and only `/status` and `/frames` describe what was drawn.

## 6. Restart the core once, and prove it is well

```console
$ docker restart prusa_connect
prusa_connect
$ docker ps --format "{{.Names}}\t{{.Status}}" | grep prusa_connect
prusa_connect	Up 2 minutes (healthy)
prusa_connect_db	Up 20 hours (healthy)
$ curl -s localhost:8080/api/v1/readiness
{"app":"served","discovery":"announcing","ready":true,"serving":4,"declared":4,"blocked":[],"instances":[
 {"resource":"storage:local","state":"ready","required":true,"serving":true},
 {"resource":"sink:bedmesh","state":"ready","required":false,"serving":true},
 {"resource":"sink:threemf","state":"ready","required":false,"serving":true},
 {"resource":"sink:wled-printer-status","state":"ready","required":false,"serving":true}]}
$ docker exec prusa_connect /prusa_connect module list
INSTANCE                  TYPE                                                              RUNTIME  ENABLED  UI     SOURCE   PATH
sink:bedmesh              com.github.aux.honzajaros.connect-bed-mesh/bedmesh                service  true     true   overlay  config/config.d/modules/bedmesh/bin
sink:threemf              com.github.aux.prusa3d.threemf/threemf                            service  true     true   overlay  config/config.d/modules/threemf/bin
sink:wled-printer-status  com.github.aux.nothingmn.wled-printer-status/wled-printer-status  service  true     false  overlay  config/config.d/modules/wled-printer-status/bin
```

The readiness line is the one to read: `serving` and `declared` are both 4, `blocked` is empty, and the new
instance is `ready`. It came back healthy about five seconds after the restart and was still healthy a
minute later.

## 7. What proves it is driving the panel

**The process is a child of the core.** The image carries no `ps`, so the process table is read from
`/proc`:

```console
$ docker exec prusa_connect sh -c 'for d in /proc/[0-9]*; do pid=${d#/proc/};
    cmd=$(tr -c "[:print:]" " " < "$d/cmdline"); [ -z "$cmd" ] && continue;
    ppid=$(cut -d" " -f4 < "$d/stat"); echo "pid=$pid ppid=$ppid cmd=$cmd"; done' | grep 'ppid=1'
pid=15 ppid=1 cmd=config/config.d/modules/threemf/bin/threemf_linux_amd64
pid=22 ppid=1 cmd=config/config.d/modules/wled-printer-status/bin/wled-printer-status_linux_amd64
pid=31 ppid=1 cmd=config/config.d/modules/bedmesh/bin/bedmesh_linux_amd64
```

**The module's own status route answers with a frame.** The route is `/status` under the mount the host
assigns, which is derived from the repository address, so its full path runs through the namespace:

```console
$ curl -s localhost:8080/api/v1/modules/com.github.aux.nothingmn.wled-printer-status/status
{"printer":{"id":"07e893bb-b922-4ff0-9ad3-061b957400c3","name":"Hermes MK4","connected":true,
 "retired":false,"state":"PRINTING","state_known":true},
 "reading":{"last_change":"2026-11-18T19:54:21.456199775Z","observed_at":"2026-11-18T19:54:22.420166747Z",
 "age_seconds":0.964,"stale":false},
 "frame":{"colour":{"r":0,"g":170,"b":0},"lit":9666,"total":16384,"reason":"progress"},
 "last_send":{"at":"2026-11-18T19:54:22.470636692Z","ok":true}}
```

Green, 9,666 of 16,384 pixels lit, the rule `progress`, and the last write to the controller taken —
`ok: true`. The read is under a second old, so the frame is not resting on a stale printer.

**The panel is holding what the module decided, and it advances with the print.** Two reads ninety
seconds apart, each beside the printer's own progress, taken in the same session:

```console
--- 19:54:14Z  printer  "/job/progress":58
--- 19:54:23Z  module   "frame":{"colour":{"r":0,"g":170,"b":0},"lit":9666,"total":16384,"reason":"progress"},"last_send":{…"ok":true}
--- 19:54:23Z  panel    "seg":[{"id":0,"start":194,"stop":256,"startY":37,"stopY":64,"len":62,…,"col":[[0,0,0],…],"fx":0,…}]

--- 19:55:53Z  module   "frame":{"colour":{"r":0,"g":170,"b":0},"lit":9830,"total":16384,"reason":"progress"},"last_send":{…"ok":true}
--- 19:55:53Z  panel    "seg":[{"id":0,"start":102,"stop":256,"startY":38,"stopY":64,"len":154,…,"col":[[0,0,0],…],"fx":0,…}]
--- 19:55:57Z  printer  "/job/progress":60
```

The segment in that read is the **unfilled** half of the panel — the range the module writes in black after
it writes the lit range in green — and on a 256-wide panel its first pixel index is the module's lit count,
exactly:

```console
37*256+194 = 9666   (module: lit 9666)
38*256+102 = 9830   (module: lit 9830)
9666/16384 = 0.590  9830/16384 = 0.600   (printer: 58 %, then 60 %)
```

So the fill the panel is holding is the printer's progress, decided by the module a second after the
printer last reported, and it moves as the print moves: 9,666 → 9,830 pixels in ninety seconds.

**That arithmetic belonged to the build installed that day.** It wrote a flat pixel index, and a flat index
is not a rectangle: on a 256-wide panel it paints down columns and wraps, which is the block that was on the
panel. The build from `d0be1a6` writes column geometry instead — segment 0 is `start 0 … stop N`,
`startY 0`, `stopY 64` in the state colour, segment 1 the black remainder from `N` to 256 — so the `stop` on
segment 0 *is* the fill, and it equals the frame's fraction of 256:

```console
--- 20:25:49Z  printer  {"progress":87,"state":"PRINTING",…}
--- 20:25:49Z  module   "frame":{"colour":{"r":0,"g":170,"b":0},"lit":14254,"total":16384,"reason":"progress"},"last_send":{…"ok":true}
--- 20:25:49Z  panel    "seg":[{"id":0,"start":0,"stop":223,"startY":0,"stopY":64,…,"col":[[0,170,0],…]},
                            {"id":1,"start":223,"stop":256,"startY":0,"stopY":64,…,"col":[[0,0,0],…]}]

--- 20:28:45Z  printer  {"progress":90,"state":"PRINTING",…}
--- 20:28:45Z  module   "frame":{"colour":{"r":0,"g":170,"b":0},"lit":14745,"total":16384,"reason":"progress"},"last_send":{…"ok":true}
--- 20:28:45Z  panel    "seg":[{"id":0,"start":0,"stop":230,"startY":0,"stopY":64,…,"col":[[0,170,0],…]},
                            {"id":1,"start":230,"stop":256,"startY":0,"stopY":64,…,"col":[[0,0,0],…]}]
```

14,254/16,384 × 256 = 222.7, so 223 columns; 14,745/16,384 × 256 = 230.4, so 230. The fill's `stop` moved
with the printer's 87 % → 90 % over those three minutes, and the black remainder shrank from 33 columns to
26. Green is `[0,170,0]`, the state colour for printing.

Two things to know when you read this yourself:

- `GET /json/state` reports the segments and their geometry, not the pixels. The lit range is written
  first and the remainder second, so the read shows the remainder — the black half — and the fill has to be
  worked out from the index it starts at, as above. To see the colours themselves you have to look at the
  panel.
- A read of the panel's pixels over WLED's own websocket (`/ws`, `{"lv":true}`) returned all-zero frames
  during this run, including with a known red patch in a second segment, so **that instrument did not
  work here and the pixel colours below the segment level are unverified** — the geometry above is what
  was measured against the module.

The module's log is worth a look after an install, because a light that is off or out of reach is a
logged line and not a stopped module:

```console
$ docker logs --since 15m prusa_connect 2>&1 | grep wled-printer-status | head -3
… level=INFO msg="wled-printer-status: the light was not updated: wled: http://10.0.0.136/json/state is unreachable: Post \"http://10.0.0.136/json/state\": dial tcp 10.0.0.136:80: connect: no route to host" …
… level=INFO msg="wled-printer-status: the controller drives 16384 LEDs" …
```

Those two lines are the first minute after the restart: the controller was briefly unreachable from the
container's network, the module kept its frame, and when the controller answered it read the geometry and
carried on.

## 8. Removing it

**This section is the removal path, not a run recorded here**: the module was left installed and driving
the panel, which is the demonstration it was installed for. The commands are checked against
`module remove --help` on this build; the readiness line below is what that route answers when the instance
is gone, and it is for the person removing it to confirm.

`module remove` takes the declaration and the installed files away and leaves the operator's own
configuration alone. The argument is the instance — `sink:wled-printer-status` — or the module path, or the
name:

```console
$ docker exec prusa_connect /prusa_connect module remove sink:wled-printer-status
$ docker restart prusa_connect
prusa_connect
$ curl -s localhost:8080/api/v1/readiness
```

`serving` and `declared` dropping back to 3, with `blocked` empty, is the check that the instance is gone
and nothing else was disturbed. Then take away what was copied in for the install — the release directory
and the settings file are not the core's, they are only what the install was given:

```console
$ docker exec prusa_connect rm -rf /config/config.d/wled-release-0.1.0 /config/config.d/wled-settings.json
$ docker exec prusa_connect ls /config/config.d
```

A removal that keeps the release directory costs nothing but leaves a stale copy of a binary inside the
core's config volume, and the next install can silently use it. Remove it.

## Two facts a reader has to know

- **Installing restarts the core.** `module add` writes the record; a core that is already answering read
  its record at start and will not read it again. The install is not finished until the restart, and the
  readiness route is what says it came back.
- **Three failures in an hour switch the module off, and it stays off until a person switches it on.** The
  count covers a crash, a failed start and going over the memory budget — not a light that cannot be
  reached, which this module logs and survives. `core.modules_switch_off` sets other numbers for every
  module: `failures` 0–100 and `window` 1m–24h, and `failures: 0` never switches a module off. `service.
  switch_off` on the instance sets them for this module alone. A module that is off is not restarting
  itself: it waits for a person.
