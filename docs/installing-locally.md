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

There is no `ui/` directory and no entry for one, because the manifest has no `ui` block: this module
draws nothing in the app, so the release is the manifest and the binaries and nothing else.

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
| `controller_address` | `http://10.0.0.136` — required; the egress entry names this setting |
| `printer` | `07e893bb-b922-4ff0-9ad3-061b957400c3` — the device id, so a second printer arriving later cannot change what this instance watches |
| `brightness` | `128` — written once at start and never moved afterwards |
| `message` | the default line; left at the manifest's own words |

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
