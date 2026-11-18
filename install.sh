#!/bin/sh
# Wled Printer Status, from source to installed on a core of the checkout.
#
#   sh install.sh             build it and install it into the demo's data
#   TARGET=app sh install.sh  install it into the real app's data instead
#
# Each step prints its number as it starts, from 1, and a good run ends with
# one line naming where the module is installed. Getting any one of them
# wrong shows nothing on the screen and says nothing, so they are written down
# once, here:
#
#   - dist/ is written as the release layout, with module.yaml at its root
#   - this platform's binary is built into the release, under the name the
#     host looks for
#   - `module add` verifies the release, records its digest, and writes the
#     declaration into a machine-owned overlay of this module's own
#
# What the tools print goes to $LOG. The end of that file is shown when a step
# fails, and nothing of it otherwise.
#
# `go run ./cmd/new-module -start demo -dir <this folder>`, run in the
# checkout, starts the core with this module.
set -e

HERE=$(cd "$(dirname "$0")" && pwd)
CHECKOUT=/work/prusa-connect

NAME=wled-printer-status
MODULE=github.com/robchartier/wled-printer-status
VERSION=0.1.0

# Everything this writes goes under the checkout's bin/, which is ignored, and
# that is where a build artefact belongs. The demo keeps its records in demo/
# under $WORK. The real app keeps them in $WORK itself, beside its
# configuration file.
TARGET=${TARGET:-demo}
WORK=$CHECKOUT/bin/$NAME-$TARGET

case "$TARGET" in
demo) DATA=$WORK/demo ;;
app) DATA=$WORK ;;
*)
	echo "FAIL  TARGET is demo or app, not $TARGET."
	exit 1
	;;
esac

CONFIG=$WORK/config.yaml
RELEASE=$HERE/dist
LOG=$WORK/install.log

for tool in go; do
	if ! command -v "$tool" > /dev/null 2>&1; then
		echo "FAIL  $tool is not on PATH, and this needs it."
		exit 1
	fi
done

mkdir -p "$WORK" "$DATA"
: > "$LOG"

STEP=0

# step prints the number of the step that starts now, and what it does.
step() {
	STEP=$((STEP + 1))
	echo "$STEP. $*"
}

# quietly runs one command and adds what it prints to $LOG. When the command
# fails, the end of the log is printed and the install stops. pnpm warns on
# every run when ~/.npmrc names an environment variable that is not set, and
# that line says nothing about this module, so it is left out.
quietly() {
	if ! "$@" >> "$LOG" 2>&1; then
		echo "FAIL  $* failed. The end of $LOG:"
		grep -v 'Failed to replace env in config' "$LOG" | tail -n 30
		exit 1
	fi
}

# The release is written again from nothing on every run, so a binary from an
# earlier run is never part of what is installed.
step "the release is written to dist..."
rm -rf "$RELEASE"
mkdir -p "$RELEASE"
cp "$HERE/module.yaml" "$RELEASE/module.yaml"

# The file name is <name>_<GOOS>_<GOARCH>, which is what the host looks for in
# the directory the declaration points at. It goes into the release directory,
# which the step above clears first.
step "the process half builds..."
BINARY="${NAME}_$(go env GOOS)_$(go env GOARCH)$(go env GOEXE)"
quietly go -C "$HERE" build -o "$RELEASE/$BINARY" .
chmod +x "$RELEASE/$BINARY"

# The operator's file. It names where the machine-owned overlay goes, where
# modules are installed and where the authorization graph is, and nothing else.
# It is written once and kept, so a second run installs over the first rather
# than beside it.
#
# The graph is under $DATA because that is where the demo keeps its own,
# whatever this file says. A graph named anywhere else is one the install
# writes the grants of your module into and the core never reads.
if [ ! -f "$CONFIG" ]; then
	cat > "$CONFIG" <<- YAML
		core:
		  overlay: $DATA/resources.json
		  modules: $WORK/modules
		  authz:
		    database: $DATA/config.d/authz.db
	YAML
fi

# A file written by an earlier run names no graph, so the install would write
# the grants of your module into a database beside the file and the core would
# read its own and find nothing. The file is yours, so this says what to add
# rather than editing it.
if ! grep -q 'database:' "$CONFIG"; then
	echo
	echo "NOTE  $CONFIG names no authorization graph, so the grants this install"
	echo "      writes go to a database the core does not read. Add these two lines"
	echo "      under core: and run this again:"
	echo
	echo "        authz:"
	echo "          database: $DATA/config.d/authz.db"
	echo
fi

step "$MODULE@$VERSION installs..."
# -approve writes the permissions the manifest asks for into the declaration,
# grants your module what those permissions name, and switches the instance on.
# That is what an operator does after reading what the module asked for, and
# this is your own module on your own machine, so it is approved here.
quietly go -C "$CHECKOUT" run ./cmd/prusa-connect module add "$MODULE@$VERSION" \
	-config "$CONFIG" -from "$RELEASE" -approve

echo "OK    $NAME is installed in $WORK"
