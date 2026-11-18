# Wled Printer Status

A Prusa Connect module that is one Go process serving one route. It
draws nothing in the app.
`make new-module` wrote this skeleton.

Its namespace is `com.github.aux.robchartier.wled-printer-status`, derived by the host from the repository
address `github.com/robchartier/wled-printer-status`. You never write the namespace: every address the module
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

## Permissions

This module declares no `permissions:`, because it acts only for the person the
host authorized and does nothing of its own: no poll, no sync loop, no
background work. The host takes `view_resource` on the instance before a read
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
with `com.github.aux.robchartier.wled-printer-status`.

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
prusa-connect module add github.com/robchartier/wled-printer-status@0.1.0 -from dist
prusa-connect module list
prusa-connect module remove github.com/robchartier/wled-printer-status
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
