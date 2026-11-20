// Command wled-printer-status is a module that watches printers and drives the
// WLED controllers they are shown on. It is this process and nothing else: it
// draws no screen.
//
// # The routes
//
//	GET /wled-printer-status   the line this module answers with, and who the host says is asking
//	GET /status                what this module currently thinks: the printers it watches, how old
//	                           their readings are, the frames it has decided, and the last send to each light
//	POST /preview-finished     the finished word, now: what the light will do when a print ends,
//	                           without waiting for one. See preview
//	POST /preview-pattern      one diagnostic picture — solid, border or quarters — so that a single
//	                           look tells a dead transport from a wrong LED map from an upside-down
//	                           panel. See pattern.go
//	GET /bindings              the set of bindings this module is running, one printer each
//	GET /bindings/{id}         one of them, and POST, PUT and DELETE on the same names to change it
//	GET /printers              the host's printers, which is where a device id comes from
//	GET /controllers           the same set grouped by controller, read-only. See bindingsapi.go
//	GET /states                the state table: a colour, a word and a hold per state, and where each came from
//	GET /lights/{light}/moments the moment table: every moment that light has, per printer on it
//	                           PUT and DELETE on both to edit one row. See tablesapi.go
//	GET /frames                one picture per binding, reconstructed from the frame this module
//	                           decided, for the Overview screen's thumbnails. See framesapi.go
//	GET /devices/{name}/snapshot  the snapshots this module holds of one light, and POST on the same
//	                           name takes one now. See devicesapi.go
//	GET /devices/{name}/snapshot/{id}  one of them, with what a restore would send and what it would not
//	POST /devices/{name}/restore   put one back: the light's whole configuration, with an explicit
//	                           confirmation, a safety snapshot taken first, and a wait for the light
//	                           to answer again before the route claims it worked
//	POST /devices/{name}/identify  flash the light — on and off, one second in each state, five
//	                           times — and put it back exactly as it was found, so that a person can
//	                           see which light a name means. See identify.go
//
// The paths are relative to the mount, so a caller reaches them under
// /api/v1/modules/com.github.aux.nothingmn.wled-printer-status/. The host decides who may call
// them and then makes the call: view_resource on the instance for a read,
// operate_resource for a write. This process checks no permission; the `observe`
// grant in module.yaml is what lets it read the printer as itself rather than as
// whoever called a route.
//
// # Settings
//
// module.yaml declares no settings, and that is docs/product-requirements.md §3 decision 1
// and §4: the host's settings pane is not this module's editing surface, and the manifest
// "keeps only what the host requires". The configuration a person edits — the lights, the
// moments, the state words, the layout, the brightness windows and a pause — lives in this
// module's own store on the data directory the host grants it (store.go), and is edited
// through this module's own pages and routes.
//
// The keys below are therefore not a surface anybody edits. A start still reads them, for
// two reasons and no third: an instance installed from a release that did carry them seeds
// its store from them once and never reads them again, and a module granted no directory has
// nowhere else to keep anything. With no settings sent at all — which is every fresh install
// — the defaults this file holds apply and the module starts on them: it serves its pages,
// it drives nothing, and the first light is added on its own screen. Nothing refuses to
// start for want of a setting. Nothing here reads an environment variable either.
//
//	message             the line GET /wled-printer-status answers with
//	bindings            one line per printer: the printer, its controller, and the segment it is shown
//	                    on. Written, it wins over the four settings below
//	controller_address  the one WLED controller to drive when bindings is empty
//	printer             which printer to watch then, by device id or by name; empty takes the only one
//	brightness          the brightness written once at start, 0-255, when the whole strip is driven
//	brightness_schedule one window per line: a span of the day and the brightness to run at inside it.
//	                    Written, it moves the brightness at a window's edge
//	timezone            the IANA zone a brightness_schedule is judged in, such as America/Denver. Required
//	                    whenever a schedule is written, and never defaulted to the process's own clock
//	segment             the one segment to drive on a multi-segment strip; -1 drives the whole strip
//
// # What it does with a reading
//
// internal/printer folds the host's telemetry and the printer's record into one
// snapshot that refuses to present a stale reading as a live one. internal/render
// turns that snapshot into a frame: one colour, how many LEDs carry the completed
// part of the job, and the reason. This file is the wiring between those two and
// internal/wled, and drive.go is the part that writes to the light.
//
// # Errors
//
// faults.go lists every error code of this module. The module answers a
// request it cannot serve with one of those codes and its English title. It
// never answers with a sentence alone, because a caller cannot check one.
//
// # Installing it
//
//	sh install.sh
//
// README.md says what that runs and why every change is a new install.
package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"time"

	"github.com/prusa3d/prusa-connect/sdk/deviceapi"
	"github.com/prusa3d/prusa-connect/sdk/extension"
	"github.com/prusa3d/prusa-connect/sdk/fault"
	"github.com/prusa3d/prusa-connect/sdk/routesapi"
	"github.com/prusa3d/prusa-connect/sdk/wire"
	"github.com/prusa3d/prusa-connect/sdk/wirerpc"

	// The zone database is compiled into the binary rather than read from the machine it runs
	// on, and that is not a size optimisation. The process runs inside the Prusa Connect
	// container, where /usr/share/zoneinfo may be absent or a release behind, and
	// time.LoadLocation would then refuse a perfectly good name like America/Denver — a module
	// that will not start over a zone the operator typed correctly, for a reason nothing on the
	// instance can explain. It is four hundred kilobytes against a 32 MiB budget.
	_ "time/tzdata"

	"github.com/nothingmn/wled-printer-status/internal/printer"
	"github.com/nothingmn/wled-printer-status/internal/wled"
)

// What this child reports when the host asks it what it is. The name is the
// manifest's, and the host holds the two to each other.
const (
	name    = "wled-printer-status"
	release = extension.Version("0.6.0")

	// repository is where this module's source lives. It is published with the build facts rather
	// than written into the browser bundle, so the one address a screen links to comes off the
	// process that is running: module.yaml's `issue_tracker` is this address with `/issues` on it.
	repository = "https://github.com/nothingmn/wled-printer-status"
)

// buildFacts is what this process is, as GET /status publishes it: the release the host was told at
// start, the wire major it speaks and the store schema it writes.
//
// It is a function of the running binary and not a value held anywhere, because a version that can
// be stored is a version that can be served by a process that is not running it — which is exactly
// the disagreement this answers. A screen reads it here rather than carrying a number of its own.
func buildFacts() buildView {
	return buildView{
		Version:     release.String(),
		Contract:    wire.ProtocolMajor,
		StoreSchema: storeVersion,
		Repository:  repository,
	}
}

const (
	// defaultMessage is the line the route answers with when the operator sets
	// nothing. It is what a start with no settings runs on, because the manifest declares no
	// option to carry it: a fresh install sends nothing and this is the default that applies.
	// TestAFreshInstallStartsOnTheDefaultsTheCodeHolds holds the two ends together.
	defaultMessage = "Wled Printer Status is serving."

	// defaultBrightness is the brightness this module writes when nothing names one, and it is
	// what a binding written without a brightness gets (bindingSetting.brightness). Half of the
	// controller's range is bright enough to read across a room and is where WLED itself starts,
	// so it is the number an operator who wrote nothing already expects.
	defaultBrightness = 128

	// defaultSegment is what a binding's segment means when none is named, and it is "the whole
	// strip", which is what this module drove before there was a setting at all.
	//
	// It is negative rather than absent because zero is a segment an operator may
	// name: a controller's first segment is segment 0, and a default of 0 would make
	// "drive the whole strip" impossible to say. It is the zero value's meaning in
	// bindingSetting.segment, and the migration source for a store written by 0.1.0.
	defaultSegment = -1

	// shutdownGrace bounds how long a stop waits for the writer to give a borrowed
	// segment back. The client's own timeout is two seconds, so one write costs less
	// than this and a controller that never answers costs no more: a stop that waited
	// on an unreachable light would be a module the host has to kill.
	shutdownGrace = 5 * time.Second

	// resolveTimeout bounds the one host read plugin.configure makes: the device
	// list it needs to turn the `printer` setting into an id. Configure is answered
	// before any route arrives, so a list that never came back would be a module that
	// never started. Ten seconds is past any answer this deployment gives and short
	// enough that a broken host does not leave the instance looking hung.
	resolveTimeout = 10 * time.Second
)

// Settings is what a start reads out of a settings block. The host sends whatever its
// declaration carries on plugin.configure, and this struct is the whole of what this module
// understands: a key it does not carry refuses the start, so a typo is found before a request
// is.
//
// The manifest declares no options (see the package comment), so on a fresh install the block
// is empty and every field here is zero. Those zeroes are not the defaults an operator gets:
// validate and configure read them the way the paragraphs below describe, and the defaults the
// code holds — defaultMessage, defaultBrightness, defaultSegment, the default layout — are what
// a start with nothing configured runs on. The fields exist for the migration §4 describes and
// for an install granted no data directory.
type Settings struct {
	// Message is the line GET /wled-printer-status answers with.
	Message string `json:"message"`

	// ControllerAddress is the one WLED controller this module drives when no `bindings`
	// list is written, as its base URL: http://10.0.0.136, no path.
	//
	// It is empty and unused when `bindings` is written, which is how one instance drives
	// several printers on several controllers: an address and a printer are two settings,
	// and two settings cannot carry two of anything. It is also empty on a fresh install,
	// because the manifest declares no options to carry it: this field is part of the
	// migration a 0.1.0 install's settings seed a store from, and of the one-binding path a
	// module with no data directory still runs.
	ControllerAddress string `json:"controller_address"`

	// Printer is the printer to watch when no `bindings` list is written, either the host's
	// device id for it or the name an operator gave it. Empty watches the only printer the
	// instance holds and refuses to guess when there is more than one.
	Printer string `json:"printer"`

	// Brightness is what this module writes to the strip, 0-255, when no `bindings` list is
	// written. Nothing else here writes a brightness: the operator sets it and it stays where
	// they put it, except when a `brightness_schedule` is written, where this is the value that
	// stands wherever no window covers the moment. With a segment named it is not written at
	// all, because a brightness is controller-wide and would move every segment on the
	// controller.
	Brightness int `json:"brightness"`

	// BrightnessSchedule is the time-of-day half of the operator's brightness decision: one
	// window per line, each a span of the day and the brightness to run at inside it.
	//
	// It is raw JSON for the same reason Bindings is — a strict decode of a whole slice cannot
	// say which line a misspelt key was in — and a `list` is again the only shape a manifest can
	// offer for it. Empty means the fixed `brightness` above applies and this module behaves
	// exactly as it did before the setting existed.
	//
	// A window is judged in the zone `timezone` names, never in this process's own clock: the
	// process runs in a container whose clock is almost always UTC, and a schedule read there
	// fires seven hours away from the operator's evening without anything saying so.
	BrightnessSchedule []json.RawMessage `json:"brightness_schedule"`

	// Timezone is the IANA zone the schedule's windows are read in, such as America/Denver.
	//
	// It is required when a schedule is written and may be left empty when none is. It is never
	// defaulted to UTC or to the process's local zone: a schedule judged in the wrong clock is a
	// display that is wrong every evening and looks healthy, and a fallback would make that the
	// module's quiet default. A zone that is written but will not load refuses the start.
	//
	// As of schema version 9 this is a migration source and not the authority: this module's own store
	// holds the zone (storedConfig.Timezone), the Overview screen edits it, and the setting is read once
	// on the start that finds the store without one (zoneFromSettings). A module the host granted no
	// data directory still runs on it, because there is no store to hold anything.
	Timezone string `json:"timezone"`

	// Layout is the operator's arrangement of a panel: a progress bar in a band at the top or the
	// bottom, the percentage in digits, and the job's name on the other half of a rotation.
	//
	// It is raw JSON for the reason Bindings is — a block of keys has no other single value
	// shape, and the one surface this was ever written through drew it as a line of JSON.
	// Absent or `{}` is the default layout: a bar at the bottom twelve
	// rows thick, the percentage and the name rotating every fifteen seconds. Every part can be
	// switched off, and a layout with everything off puts the panel back exactly as it was before
	// this setting existed.
	//
	// It is judged at configure, before any writer is built, so a bar too thick for a panel, a
	// misspelt key, or a part this build does not draw refuses the start with the field named
	// rather than being discovered on a light nobody is looking at. See layout.go.
	Layout json.RawMessage `json:"layout"`

	// Segment is the one segment of a multi-segment controller this module drives when no
	// `bindings` list is written: the operator's own numbering, as the controller reports it.
	// The default, -1, drives the whole strip as this module always has, and any number the
	// controller holds names one segment.
	//
	// Named, the module borrows that segment rather than taking it: it reads the segment's
	// run, colour, effect and name once at start, draws inside that run while it runs, and
	// writes all of it back when it stops. It writes no brightness and no power in this mode,
	// and it never writes a second segment.
	Segment int `json:"segment"`

	// Bindings is the list of printers this module shows, one line each, and it wins over the
	// four keys above when it is written.
	//
	// It is raw JSON rather than a slice of a struct because the payload is decoded strictly
	// and a strict decode of a slice cannot say which line a misspelt key was in. Each line is
	// decoded on its own in bindingsFrom, which is what makes the refusal read
	// "bindings[2]: json: unknown field ..." instead of making a person count lines in a text
	// area.
	//
	// A `list` is the only shape a setting can carry several of anything in, which is why the
	// migration this key is the source of reads one line per printer.
	Bindings []json.RawMessage `json:"bindings"`
}

// validate refuses settings this module cannot act on, before it acts on them.
//
// The address is held to wled.New's own rules rather than to a second copy of
// them, because that is the check the client makes at its first call and a
// configuration mistake is better named on the instance than discovered as a light
// that never changes.
//
// The brightness schedule and its zone are judged first and for both shapes, because
// they are not about a binding: the schedule is one per instance, judged in one zone,
// and it applies whatever the bindings say. A schedule with no zone, or a zone that
// will not load, refuses the start here — before anything is built and before the light
// is touched — because the alternative is a light judged in the container's clock,
// seven hours out, with nothing on the instance to say so.
//
// Everything below the bindings branch is about the four settings the list replaced, so
// it applies only while no list is written: refusing a stale brightness of 300 in a key
// nothing reads would refuse a start over a field that has no effect. Each line of a
// list is judged where it is parsed, which is where a refusal can name the line.
func (s Settings) validate() error {
	if err := s.checkSchedule(); err != nil {
		return err
	}

	if err := s.checkLayout(); err != nil {
		return err
	}

	if len(s.Bindings) > 0 {
		_, err := parseBindingEntries(s.Bindings)

		return err
	}

	// Nothing written is legal, and as of the release that took the options out of the
	// manifest it is the ordinary case: a fresh install sends an empty block, starts on the
	// defaults this file holds, serves its pages, and drives nothing until a light is added on
	// its own screen. A refusal here would be a module that cannot start until somebody writes
	// a setting the host has no way to send — which is the chicken and egg that made this a
	// refusal while the manifest still offered a settings pane.
	//
	// Nothing below is judged in this case, and that is deliberate: brightness and segment are
	// read only by the one binding the four old keys describe, and refusing a stale brightness
	// in a key nothing reads would refuse a start over a field that has no effect.
	if strings.TrimSpace(s.ControllerAddress) == "" {
		return nil
	}

	if _, err := wled.New(s.ControllerAddress); err != nil {
		return err
	}

	if s.Brightness < 0 || s.Brightness > 255 {
		return fmt.Errorf("brightness is %d, and a WLED controller takes 0-255", s.Brightness)
	}

	// A negative segment other than -1 is refused rather than read as "whole strip": the
	// value would be a typo, and a typo that quietly drove the whole strip is a module
	// changing every segment on a controller the operator asked it to leave alone.
	if s.Segment < -1 {
		return fmt.Errorf("segment is %d: name one segment by its number, or -1 for the whole strip", s.Segment)
	}

	return nil
}

// checkSchedule judges the two settings that describe the time-of-day brightness and keeps
// nothing. configure reads them again for the schedule itself, which is a handful of lines
// parsed once at start rather than a value worth threading out of a refusal.
func (s Settings) checkSchedule() error {
	_, err := s.wantedSchedule()

	return err
}

// wantedSchedule is the schedule these two settings describe, or nil when they describe none.
//
// One function for both callers, so that the settings a start is judged by and the schedule the
// writers are built from cannot be two different readings of the same two fields.
func (s Settings) wantedSchedule() (*brightnessSchedule, error) {
	return parseBrightnessSchedule(s.BrightnessSchedule, s.Timezone)
}

// checkLayout judges the layout setting and keeps nothing. configure reads it again for the layout
// the writers are built from, which is the same handful of lines read once at start.
//
// It is judged for both shapes of the bindings setting, like the schedule and for the same reason:
// a layout is one per instance, it describes a panel rather than a binding, and a start that
// ignored a bar_rows of 60 would put the panel up and leave the frame undrawable for ever.
func (s Settings) checkLayout() error {
	_, err := s.wantedLayout()

	return err
}

// wantedLayout is the layout these settings describe, or the default layout when they describe none.
//
// One function for both callers, so that the settings a start is judged by and the layout the
// writers are built from cannot be two different readings of the same field.
func (s Settings) wantedLayout() (layout, error) {
	return parseLayout(s.Layout)
}

// The state a running instance holds.
//
// held is the settings block this start read, or nil when no start has run. session and
// fleet are written once in run, before the wire can carry a call: they are the two
// things a watch needs and cannot build for itself. sessionCtx is the session's own
// context, which is what a watch runs on — a watch tied to the call that configured
// it would die the moment the host's call was answered.
//
//nolint:gochecknoglobals // the state one process has, by design.
var (
	held atomic.Pointer[Settings]

	session    *wirerpc.Conn
	fleet      *deviceapi.Client
	sessionCtx context.Context

	// live is the one watch this process runs, and the set of bindings it runs.
	//
	// It is a value here rather than three variables in this file because a set can now
	// change while the process runs: the editor's routes add, change and remove a binding,
	// and each of those has to reach the writer that is already talking to the controller.
	// See live.go for what a change costs and why the set being replaced is waited for.
	//
	// Two of its three closures are built from the connection, which is why they are written
	// as closures here and filled in later: printer.NewSource takes the session and the fleet
	// as they are when the watch starts, not as they were when this file was read.
	live = &runner{
		newSource: func(b binding) (watchSource, error) {
			return printer.NewSource(printer.Config{DeviceID: b.DeviceID}, session, fleet)
		},
		newLight: func(address string) (strip, error) { return wled.New(address) },
		logf:     logf,
		// The store the routes edit, so that the writers a change rebuilds are built on what the
		// painter now obeys rather than on the settings alone.
		tables: tables,
	}

	// tables is this module's own configuration as the state and moment tables reach it: the
	// directory the host granted on configure, and the lock a write takes. It holds no copy of the
	// document — the file is the record — so there is nothing here to keep in step. See
	// tablesapi.go.
	tables = &configTables{}
)

// settings is what this module is running on: what the host sent, or the
// defaults this code holds when the host sent nothing.
func settings() Settings {
	if current := held.Load(); current != nil {
		return *current
	}

	return Settings{
		Message:           defaultMessage,
		ControllerAddress: "",
		Brightness:        defaultBrightness,
		Segment:           defaultSegment,
	}
}

// openAPI is the fragment describing the routes, with every path relative to
// the mount. The host holds it to routesapi.ParseFragment at launch and serves
// it rebased under the namespace, which is what cmd/generate-client-sdk reads
// to type this module's surface.
//
//go:embed openapi.json
var openAPI []byte

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", name, err)
		os.Exit(1)
	}
}

// run writes the handshake line and then serves frames until the host closes
// the channel.
func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	sessionCtx = ctx

	// Whatever ends the session — the host closing the channel, an interrupt, a stop —
	// the watch is stopped and waited for, because that is what gives a borrowed segment
	// back to the controller it came from.
	defer stopWatching()

	line, err := wire.Handshake{
		Protocol: wire.ProtocolMajor,
		MinMinor: wire.MinMinor,
		MaxMinor: wire.MaxMinor,
		Address:  wire.StdioAddress,
	}.Line()
	if err != nil {
		return fmt.Errorf("render the handshake: %w", err)
	}

	if _, err = os.Stdout.WriteString(line); err != nil {
		return fmt.Errorf("write the handshake: %w", err)
	}

	// The method set is filled in after the connection exists, because two of the
	// things a watch needs — the host's fleet and the session a subscription opens on
	// — are built from the connection itself. The connection holds this map, so
	// filling it here fills the one it dispatches from.
	serves := wirerpc.Methods{}

	conn, err := wirerpc.NewConn(stdio{}, wirerpc.Options{
		Role:    wirerpc.RoleChild,
		Methods: serves,
	})
	if err != nil {
		return fmt.Errorf("build the session: %w", err)
	}

	session = conn
	fleet = deviceapi.NewClient(conn)

	maps.Copy(serves, methods())

	// The pid is here because the host's log is the only place a person sees
	// this process. Nothing on the wire carries it.
	fmt.Fprintf(os.Stderr, "%s %s is serving on stdio as pid %d\n", name, release, os.Getpid())

	if err = conn.Serve(ctx); err != nil {
		return fmt.Errorf("serve: %w", err)
	}

	return nil
}

// methods is the lifecycle set every child answers, plus the one call that
// carries this module's own HTTP surface.
func methods() wirerpc.Methods {
	return wirerpc.Methods{
		wirerpc.MethodInfo: func(context.Context, wirerpc.Call) (any, error) {
			return routesapi.Describe(
				wirerpc.Info{
					Name:     name,
					Version:  release,
					Contract: wirerpc.Contract,
					// Naming the call is what asks the host for the settings. A
					// module that names none is never configured.
					Serves: []wirerpc.Method{wirerpc.MethodConfigure},
				}, openAPI)
		},
		// The settings block the host sends once before the first request, with the grants the
		// operator approved beside it. The manifest declares no options, so on a fresh install the
		// block is empty and this start runs on the defaults in this file; on the one upgrade from
		// a release that did declare them, the block is the migration source §4 describes. Either
		// way this is where the watch starts, and the grants are read for one thing: the writable
		// directory this module keeps its own configuration in. See store.go.
		wirerpc.MethodConfigure: wirerpc.ConfigureGranted(configure),
		// The host is stopping this module. The watch is stopped and waited for here, and
		// not merely abandoned: the writer gives a borrowed segment back on its way out,
		// and a process that answered this call and exited would leave the operator's
		// segment holding this module's last frame with nothing left to put it right.
		// What a stop cannot cover is a process killed without warning — see
		// writer.release.
		wirerpc.MethodShutdown: func(context.Context, wirerpc.Call) (any, error) {
			fmt.Fprintf(os.Stderr, "%s was asked to stop\n", name)

			stopWatching()

			return nil, nil
		},
		routesapi.MethodServe: routesapi.Serve(routes()),
	}
}

// configure turns one settings block into one running watch.
//
// It makes one host read — the device list, to turn a name into an id — and it
// sends nothing to the controller. That read is safe here even though it is made
// while the host waits for this call: the session answers each incoming call on its
// own goroutine and keeps reading, so a call back out on the same connection cannot
// deadlock it.
//
// Nothing here reaches the light. A controller that is unplugged is not a
// configuration mistake and must not stop the module from starting, so the first
// write happens on the writer's own goroutine and a failed one is logged rather than
// returned.
//
// An error returned here refuses the start, and the operator reads it on the
// instance. That is the right answer for a setting this module cannot act on — an
// address that is not a URL, a brightness no controller takes, a printer name that
// matches nothing — because the alternative is a process that runs and never says
// why it is dark.
//
// The grants matter for one of them: a writable directory is where this module keeps its own
// configuration (store.go), and a module that was granted none runs from its settings exactly as
// it did before that store existed.
func configure(ctx context.Context, sent Settings, grants wirerpc.Grants) error {
	if sent.Message == "" {
		sent.Message = defaultMessage
	}

	if err := sent.validate(); err != nil {
		return err
	}

	// The brightness the light runs at, in the one form the writers read: the windows the
	// operator wrote and the zone they are judged in. Nil when no schedule is written, which is
	// the state every install before this setting was in.
	//
	// It is recorded before the watch is built, because build reads it: a route change later in
	// this process rebuilds the writers, and they have to land on the same brightness the
	// settings asked for rather than on the fixed value alone.
	schedule, err := sent.wantedSchedule()
	if err != nil {
		return err
	}

	live.setSchedule(schedule)

	// The arrangement of a panel, in the one form the writers read. A layout this build cannot act
	// on has already refused the start in validate, before anything is built and before the light
	// is touched; this is the reading the writers are given.
	shape, err := sent.wantedLayout()
	if err != nil {
		return err
	}

	live.setLayout(shape)

	// The directory the host granted, recorded for the two tables the screens edit. A module
	// granted none records an empty one, and the tables' routes then read the defaults and refuse
	// to write — which is the same behaviour this module had before the store existed, said out
	// loud rather than discovered.
	tables.grant(grants.Data)

	listing, cancel := context.WithTimeout(ctx, resolveTimeout)
	wanted, origin, err := configurationFor(listing, fleet, sent, grants.Data, logf)
	cancel()

	if err != nil {
		return err
	}

	// Which set this start runs: one binding per line of `bindings`, the one printer the four
	// older keys name, or — when the host granted this module a directory — what its own store
	// holds, which the settings seeded once. Nothing here decides what a binding is — the core
	// underneath runs a set (bindings.go, watch.go) and hands it its own rules.
	//
	// A refusal here leaves the light as it was and the operator holding the reason, which is
	// the answer for a configuration mistake. The settings are stored only once the watch is
	// running, so a process that refused them does not answer /wled-printer-status with a
	// message it is not acting on.
	if err := live.apply(sessionCtx, wanted); err != nil {
		return err
	}

	// Where the set in force came from, for the answer GET /bindings gives. A set read from this
	// module's own store is the one an operator has to be able to tell apart from the settings:
	// the settings seeded that store once and change nothing in it now.
	live.setOrigin(origin)

	// Every light the store knows but has never read is read once now, in the background: an
	// install that came up from the migration to schema version 7 holds addresses and no facts, and
	// the Lights screen would otherwise say "not read yet" about a light that is answering this
	// minute. It is a goroutine because a start must not wait for a controller: a light that is
	// unplugged is not a reason to be slow, and fillLightFacts keeps nothing it could not read.
	go fillLightFacts(sessionCtx, tables, func(address string) (lightProber, error) {
		return wled.New(address)
	}, logf)

	held.Store(&sent)

	return nil
}

// zoneFromSettings writes the `timezone` setting into a document that names no zone, and reports
// whether it wrote anything.
//
// This is the whole of the migration that schema version 9 made necessary. Until it, the zone was a
// manifest setting and the manifest has declared no `options` since §4 made this store the only
// editing surface — so no install could name one, and both things that need a zone refused on every
// install. The setting still exists for the one install that was configured before that: it is read
// here, once, on the start that finds the document without a zone, and never again. After this write the
// store is the authority (zoneInForce, timezone.go), which is the same shape the brightness schedule and
// the layout rows have.
//
// Three things it deliberately does not do:
//
//   - **It never overwrites.** A document that names a zone keeps it, whatever the setting says, so a
//     zone the operator set on the Overview screen cannot be undone by a settings save.
//   - **It invents nothing.** A setting that names no zone writes nothing, and the document keeps the
//     empty string that refuses a window and draws no clock. UTC is not a fallback: it is this
//     container's own clock and would look healthy while being seven hours out.
//   - **It re-checks nothing.** The settings were judged by Settings.validate before the watch was
//     built, and a zone that will not load refuses the start there, so a zone this function is handed
//     has already been loaded once.
func zoneFromSettings(config *storedConfig, sent Settings) bool {
	if strings.TrimSpace(config.Timezone) != "" {
		return false
	}

	named := strings.TrimSpace(sent.Timezone)
	if named == "" {
		return false
	}

	config.Timezone = named
	config.TimezoneOrigin = zoneOriginSettings

	return true
}

// originOf is where a set built from the settings came from, in the words GET /bindings answers
// with: the list, the four settings that came before it, or nothing at all.
func originOf(sent Settings) string {
	if len(sent.Bindings) > 0 {
		return originSettings
	}

	if strings.TrimSpace(sent.ControllerAddress) == "" {
		return originNothing
	}

	return originLegacy
}

// configurationFor is the set this start runs, and where it came from.
//
// The host granted this module a writable directory (needs.data in module.yaml), and that
// directory is its own store: one versioned JSON document holding the configuration a person
// edits. The host's settings are the migration source — the first start with no document writes
// one from them, and every start after that reads the document instead — which is
// docs/product-requirements.md §4's rule and not this function's convenience.
//
// A module granted no directory runs from its settings exactly as it did before the store
// existed. That is not a fallback that pretends to be the store: nothing is written, nothing is
// read, and the line the start writes says which of the two happened.
func configurationFor(ctx context.Context, fleet devices, sent Settings, dataDir string, log func(string, ...any)) (bindings, string, error) {
	if strings.TrimSpace(dataDir) == "" {
		set, err := wantedBindings(ctx, fleet, sent)
		if err != nil {
			return nil, "", err
		}

		return set, originOf(sent), nil
	}

	found, err := loadStore(dataDir)

	switch {
	case errors.Is(err, errNoStore):
		// No document: this is the first start of an install, and the settings are the thing it
		// is migrated from. The document is written before the watch is built, because a store
		// that was not written would make the next start the same migration again — and a set
		// the operator changed in the settings pane between the two starts would be read here
		// but lost at the third.
		//
		// An empty set is what a fresh install asks for now: the manifest declares no options, so
		// the host sends an empty block, and §4's "if the host settings are absent, defaults apply"
		// is this. The document is written anyway, empty, because that is what records the
		// migration as done — the next start must not look for settings again — and because the
		// store's other lists (moments, words, layouts, windows, a pause) are edited on their own
		// screens from the first minute.
		set, buildErr := wantedBindings(ctx, fleet, sent)
		if buildErr != nil {
			return nil, "", buildErr
		}

		entries, renderErr := storeEntries(set)
		if renderErr != nil {
			return nil, "", renderErr
		}

		// The document this first start writes: the bindings, the lights those bindings name — with
		// the address on each and no facts, because a start must not wait for a controller — and the
		// zone, when the settings name one.
		seed := storedConfig{
			Version:  storeVersion,
			Bindings: entries,
			// And the lights those bindings name, as rows with the address on them: a fresh
			// install from settings has the same claim on the list as an upgraded one, and a
			// person who wrote two bindings in the settings must not have to retype the two
			// addresses they already name. The start fills the facts in (fillLightFacts).
			Lights: seededLightsOrNothing(entries),
		}

		// The setting is the migration source for the zone as it is for the bindings, and a module
		// whose settings name none keeps the empty string — the state that refuses a window and
		// draws no clock rather than reading UTC and looking healthy while being hours out.
		zoneFromSettings(&seed, sent)

		path, writeErr := saveStore(dataDir, seed)
		if writeErr != nil {
			return nil, "", writeErr
		}

		if len(set) == 0 {
			log("%s: nothing is configured and nothing was sent, so this start runs on the defaults this module's code holds: its pages answer, it drives no light, and the first light is added on its own Bindings screen. The configuration store was written empty at %s, schema version %d",
				name, path, storeVersion)

			return set, originNothing, nil
		}

		log("%s: this module's own configuration store was written for the first time at %s, from the settings, at schema version %d; the settings are a migration source and are not applied again",
			name, path, storeVersion)

		return set, originStore, nil

	case err != nil:
		return nil, "", err
	}

	if seededZone := zoneFromSettings(&found.Config, sent); found.Upgraded || seededZone {
		// The document was stepped up to this build's version, or the zone the settings name was
		// written into a document that had none. Writing it back now is what keeps the upgrade from
		// being redone — and re-decided — on every start, and what makes the setting the migration
		// source for the zone: after this write, the store holds it and the setting is read no more.
		path, writeErr := saveStore(dataDir, found.Config)
		if writeErr != nil {
			return nil, "", writeErr
		}

		if found.Upgraded {
			log("%s: the configuration store at %s was upgraded from schema version %d to %d",
				name, path, found.Version, storeVersion)
		}

		if seededZone {
			log("%s: the timezone setting named %s and the configuration store at %s, schema version %d, held none, so it was written into the store once; the setting is a migration source and is not read again",
				name, found.Config.Timezone, path, storeVersion)
		}
	}

	set, err := bindingsFrom(ctx, fleet, found.Config.Bindings)
	if err != nil {
		return nil, "", fmt.Errorf("the configuration store %s: %w", storePath(dataDir), err)
	}

	log("%s: the set this start runs is the one in this module's own store at %s, schema version %d (%d bindings); the settings are a migration source and are not applied again",
		name, storePath(dataDir), storeVersion, len(set))

	return set, originStore, nil
}

// stopWatching stops the watch this process is running and waits for it to finish giving
// back what it borrowed.
//
// It is a function rather than a cancel call at each site because the wait is the point: the
// writer's give-back is a write to the controller, and a stop that cancelled the watch and
// returned would race that write against the process exit. shutdownGrace is what keeps a
// controller that never answers from turning a stop into something the host has to kill.
//
// Called with no watch in place it does nothing, so the second configure of a process and the
// deferred stop at the end of run can both call it without asking first.
func stopWatching() {
	live.stop()
}

// devices is the one read resolvePrinter makes of the host's fleet.
// *deviceapi.Client satisfies it.
type devices interface {
	List(ctx context.Context, params deviceapi.ListParams) (deviceapi.Page, error)
}

// resolvePrinter turns the `printer` setting into the one device id to watch.
//
// A setting that names a device's id wins over one that names it, because an id
// survives a rename. An empty setting takes the only printer the instance holds and
// refuses to choose between several: picking one would put a person's strip on the
// wrong machine, and nothing here can know which of two printers was meant.
//
// A name two printers answer to is refused for the same reason, and the refusal
// says to use the id.
func resolvePrinter(ctx context.Context, fleet devices, setting string) (string, error) {
	wanted := strings.TrimSpace(setting)

	page, err := fleet.List(ctx, deviceapi.ListParams{Limit: deviceapi.MaxPageLimit})
	if err != nil {
		return "", fmt.Errorf("read the printer list: %w", err)
	}

	if wanted == "" {
		switch len(page.Items) {
		case 0:
			return "", fmt.Errorf("this instance holds no printer to watch")
		case 1:
			return page.Items[0].ID, nil
		default:
			return "", fmt.Errorf(
				"the printer setting is empty and this instance holds %d printers; name the one to watch, by id or by name",
				len(page.Items))
		}
	}

	for _, device := range page.Items {
		if device.ID == wanted {
			return device.ID, nil
		}
	}

	var named []deviceapi.Device

	for _, device := range page.Items {
		if strings.EqualFold(device.Name, wanted) {
			named = append(named, device)
		}
	}

	switch len(named) {
	case 1:
		return named[0].ID, nil
	case 0:
		if page.Total > len(page.Items) {
			return "", fmt.Errorf(
				"no printer among the first %d of %d is called %q; narrow it with the device id",
				len(page.Items), page.Total, wanted)
		}

		return "", fmt.Errorf("no printer in this instance is called %q", wanted)
	default:
		return "", fmt.Errorf("%d printers are called %q; name the one to watch by its device id",
			len(named), wanted)
	}
}

// routes is the surface this module serves, written as any Go program writes
// one. The paths carry no prefix: /status here is
// /api/v1/modules/com.github.aux.nothingmn.wled-printer-status/status to a caller.
//
// drivingNothing is what the two picture routes answer when the set in force is empty, which is a
// fresh install of this manifest: there are no options to name a light in, so a module starts with
// nothing configured and its own pages are where the first one arrives. It is a different fact
// from "no reading has arrived yet", and the two must not share a sentence: one of them clears up
// on its own and the other never will until somebody adds a light.
const drivingNothing = "this module is driving nothing: no light is configured yet, so there is no frame to report. Add one on this module's Bindings screen."

// The binding editor's routes are mounted from bindingsapi.go, which says what they are for and
// what a change through them costs.
func routes() http.Handler {
	mux := http.NewServeMux()

	// The host's printer list, as the routes read it. It is a function rather than a value because
	// this process has no connection to the host when this handler is built in a test, and the
	// honest answer then is that the printer list cannot be read.
	held := func() devices {
		if fleet == nil {
			return nil
		}

		return fleet
	}

	// The editor's routes: the bindings this module drives, the printers the host holds, and the
	// controllers the set runs on.
	bindingAPI{
		live:   live,
		fleet:  held,
		parent: func() context.Context { return sessionCtx },
		// The store, because a binding added, moved or deleted here is written into this
		// module's own document rather than into a list this process happens to be holding.
		// See reviseStored.
		tables: tables,
	}.mount(mux)

	// The two tables the screens edit, on the same terms: the same watch, the same host list, and
	// this module's own store beside them. See tablesapi.go.
	tableAPI{
		live:   live,
		fleet:  held,
		tables: tables,
	}.mount(mux)

	// A light's snapshots and a restore onto it. The snapshots are files in the directory the store
	// is in, and the restore is the one route in this module that rewrites a controller's own
	// configuration: see devicesapi.go.
	deviceAPI{
		live:   live,
		tables: tables,
	}.mount(mux)

	// The lights this module knows: the list a screen draws a card per light from, a light added by
	// its address, and a light taken off the list. See lights.go.
	lightsAPI{tables: tables}.mount(mux)

	// The Overview screen's thumbnails: one reconstructed picture per binding, drawn by the same
	// code that draws the light and cheap enough to ask for on every draw. See framesapi.go.
	framesAPI{
		live:  live,
		cache: newFrameCache(),
	}.mount(mux)

	// `Refresh everything`, the header's second button beside the pause: every binding the module
	// drives, written from the reading taken at this moment, whether or not the picture changed.
	// See refresh.go.
	refreshAPI{}.mount(mux)

	mux.HandleFunc("GET /wled-printer-status", func(w http.ResponseWriter, r *http.Request) {
		answer(w, http.StatusOK, map[string]any{
			"message": settings().Message,
			// Who the host says is calling. It is the host's answer and not a
			// header this process trusted.
			"principal": routesapi.PrincipalOf(r),
		})
	})

	// What this module thinks, so a person can read it without walking over to
	// the light. A frame of zeros would read as "the light is black", which is a claim
	// this module has not made, so a request that arrives before the first frame is
	// decided is refused by code instead.
	mux.HandleFunc("GET /status", func(w http.ResponseWriter, r *http.Request) {
		// A watch holding more than one binding publishes each binding under its own
		// controller and segment, because one body cannot say whose frame it describes. That
		// store is empty for a single-binding install — the shape every install built from
		// settings has — so this branch costs one lock and the body below is unchanged.
		if held := bindingsSeen(); len(held) > 0 {
			// The build facts ride on both shapes of this answer, so a screen reads them whichever
			// one the instance answers with, and neither has to guess which it got.
			answer(w, http.StatusOK, map[string]any{"bindings": held, "build": buildFacts()})

			return
		}

		seen := current.Load()
		if seen == nil {
			if len(live.set()) == 0 {
				refuse(w, r, NoReadingYet.New(drivingNothing))

				return
			}

			refuse(w, r, NoReadingYet.New(
				"this module has not decided a frame yet: it answers this once it has taken its first reading"))

			return
		}

		answer(w, http.StatusOK, statusAnswer{
			Printer:  seen.Printer,
			Reading:  seen.Reading,
			Frame:    seen.Frame,
			LastSend: sent.Load(),
			Build:    buildFacts(),
		})
	})

	// The finished word, now, so that a person can look at it without waiting for a print to
	// end. It fires the same event a completed print fires — the same word, the same colour,
	// the same minute — because a preview that showed a different picture would be telling the
	// operator something about a light he does not have.
	//
	// Nothing here writes to the controller: the event goes into each writer's one slot and each
	// writer's own goroutine paints it, behind the same gate and the same backoff as every other
	// write, and hands the steady frame back when the hold ends. So the route cannot reach a
	// light in a way the module's ordinary path could not, and it cannot change what a light
	// rests at afterwards.
	//
	// It fires at every light this instance drives, and a request that wants one names it with
	// `light`. A light that cannot be asked for the word — a borrowed segment of somebody else's
	// controller, or a strip — is refused for that light and reported beside the ones that took
	// it; the route refuses as a whole, with a registered code, only when no light took it at
	// all. See previewTargets and paintPreview.
	mux.HandleFunc("POST /preview-finished", func(w http.ResponseWriter, r *http.Request) {
		targets, problem := previewTargets(r)
		if problem != nil {
			refuse(w, r, problem)

			return
		}

		ev := finishedWordEvent()

		// The hold the caller asked for, or the event's own minute. A hold past the ceiling is
		// refused before anything is stored, for the reason the ceiling exists: a preview is a
		// status display that has stopped being one for as long as it runs.
		holds, err := holdFrom(r)
		if err != nil {
			refuse(w, r, holdRefusal(r, err))

			return
		}

		ev.holds = holds

		painted, problem := paintPreview(targets, func(*previewTarget) event { return ev })
		if problem != nil {
			refuse(w, r, problem)

			return
		}

		answer(w, http.StatusAccepted, withLights(map[string]any{
			"event":  ev.word,
			"colour": colourView{R: ev.colour.R, G: ev.colour.G, B: ev.colour.B},
			"scale":  finishedWordScale,
			// How long the word keeps the light, in whole seconds: the number a person needs to
			// know when to look, and to know how long he has to look.
			"holds_seconds": int(ev.holds / time.Second),
		}, painted))
	})

	// The panel's own picture, drawn with the values the caller gives, so that the composed layout
	// can be looked at without a print. It is the same machinery the two routes below use — the
	// same writer, the same establishing body, the same once-a-second repeat over DDP, the same
	// deadline and the same hand-back — because everything else about it is the point: what it
	// draws is what a printing panel draws, or the picture would be a lie about a light.
	//
	// percent is required and name is not: a caller who asks for a preview of the composed layout
	// without naming a progress has not said what to draw, and a route that answered with a picture
	// of nothing would be a route that quietly ignored a misspelt parameter.
	mux.HandleFunc("POST /preview-frame", func(w http.ResponseWriter, r *http.Request) {
		targets, problem := previewTargets(r)
		if problem != nil {
			refuse(w, r, problem)

			return
		}

		holds, err := holdFrom(r)
		if err != nil {
			refuse(w, r, holdRefusal(r, err))

			return
		}

		percent, err := percentFrom(r)
		if err != nil {
			refuse(w, r, PreviewFrameInvalid.
				New(err.Error()).
				With(fault.StringParam("percent", strings.TrimSpace(r.URL.Query().Get("percent")))))

			return
		}

		jobName := strings.TrimSpace(r.URL.Query().Get("name"))

		// A composed picture is a rectangle: the bar is a band of rows and the text is centred in
		// another, and a strip has one row. So each light is handed the picture drawn for its own
		// geometry, and a light that has no rectangle refuses it — which is what keeps this route
		// refusing on a strip, for the reason the word does.
		painted, problem := paintPreview(targets, func(target *previewTarget) event {
			geometry := target.pen.panelOf()

			return composedEvent(
				target.pen.pictureOf(previewFrame(percent, jobName, geometry.width*geometry.height)), holds)
		})
		if problem != nil {
			refuse(w, r, problem)

			return
		}

		body := map[string]any{
			"percent":       percent,
			"holds_seconds": int(holds / time.Second),
		}

		// The name as each panel will draw it, and what its rotation shows: both are per light,
		// because a 64x32 panel and a 256x64 one cut the same name at the same scale differently.
		details := make([]map[string]any, 0, len(painted))

		for i, one := range painted {
			detail := map[string]any{"controller": one.Controller, "light": one.Light}

			// A light that refused the picture has no name or rotation to report — the bands it
			// would report them for do not exist on it — so its entry carries the refusal and
			// nothing that would be a guess about a picture it is not drawing.
			if one.Refused != "" {
				detail["refused"], detail["why"] = one.Refused, one.Why
				details = append(details, detail)

				continue
			}

			geometry := targets[i].pen.panelOf()
			bands, _ := targets[i].pen.composing().bandsOn(geometry)
			picture := targets[i].pen.pictureOf(previewFrame(percent, jobName, geometry.width*geometry.height))

			detail["name"] = picture.name(geometry.width, nameScale(bands.textRows))
			detail["halves"] = picture.halves(geometry, bands)

			details = append(details, detail)

			if len(targets) == 1 {
				body["name"] = detail["name"]
				body["halves"] = detail["halves"]
				body["layout"] = targets[i].pen.composing().view()
				body["rotation_seconds"] = int(targets[i].pen.composing().rotate / time.Second)
			}
		}

		body["lights"], body["count"] = details, len(details)

		if len(details) == 1 {
			body["controller"] = details[0]["controller"]
		}

		answer(w, http.StatusAccepted, body)
	})

	// The diagnostic ladder: three pictures that answer, one each, the three faults a blank panel
	// can hide — nothing drawn, the wrong map, and the panel upside down or mirrored. See
	// pattern.go for what each one is and why it is that shape and those colours.
	//
	// It shares the whole of the finished word's machinery rather than copying it: the same slot
	// in the same writer, the same establishing body, the same once-a-second repeat over DDP and
	// the same hand-back when the hold ends. What it does not share is the word: a pattern carries
	// its own pixels, so the refusal for a panel too small to hold the word does not apply, and a
	// strip is given the same picture laid out along its one row rather than being refused — the
	// ladder is the instrument that says whether anything is lit, and a strip is a light.
	mux.HandleFunc("POST /preview-pattern", func(w http.ResponseWriter, r *http.Request) {
		targets, problem := previewTargets(r)
		if problem != nil {
			refuse(w, r, problem)

			return
		}

		// The hold first, then the pattern. Both are refusals the caller fixes by retyping one
		// value, and the hold is the route's own bound: a caller who sends neither gets the
		// event's own minute and a pattern that exists.
		holds, err := holdFrom(r)
		if err != nil {
			refuse(w, r, holdRefusal(r, err))

			return
		}

		ev, err := patternEvent(r.URL.Query().Get("pattern"), holds)
		if err != nil {
			refuse(w, r, PreviewPatternUnknown.
				New(err.Error()).
				With(fault.StringParam("pattern", r.URL.Query().Get("pattern"))))

			return
		}

		painted, problem := paintPreview(targets, func(*previewTarget) event { return ev })
		if problem != nil {
			refuse(w, r, problem)

			return
		}

		shape, _ := patternNamed(ev.pattern)

		// What a person is supposed to learn by looking, so that the route and the look are one
		// errand rather than two. It is per light because a one-row light does not show four
		// quadrants: it shows four bands, and the sentence has to say which.
		body := map[string]any{
			"pattern":       ev.pattern,
			"holds_seconds": int(ev.holds / time.Second),
		}

		details := make([]map[string]any, 0, len(painted))

		for i, one := range painted {
			detail := map[string]any{
				"controller": one.Controller,
				"light":      one.Light,
				"answers":    shape.answersOn(targets[i].pen.panelOf()),
			}

			if one.Refused != "" {
				detail["refused"], detail["why"] = one.Refused, one.Why
			}

			details = append(details, detail)
		}

		body["lights"], body["count"] = details, len(details)

		if len(details) == 1 {
			body["controller"] = details[0]["controller"]
			body["answers"] = details[0]["answers"]
		}

		answer(w, http.StatusAccepted, body)
	})

	// Everything else is this module's own refusal. It carries the code from
	// faults.go and names the path, which is what proves the group prefix was
	// taken off before the request arrived.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		refuse(w, r, RouteNotFound.
			New("this module serves no route at "+r.URL.Path).
			With(fault.StringParam("path", r.URL.Path)))
	})

	return mux
}

// previewTargets answers the lights one preview request is about: every light this instance
// drives, in the order the settings name them, or the ones the request's `light` parameter names.
//
// The set is the watch's, read whole — see previewHeld — so a request cannot be answered for one
// configuration and painted on another. An instance driving no light at all is the one case these
// routes still refuse: there is nothing to put a picture on, and saying so is the whole answer.
func previewTargets(r *http.Request) ([]*previewTarget, *fault.Fault) {
	targets := previewsNow()
	if len(targets) == 0 {
		return nil, PreviewNotRunning.New(
			"this module is driving no light at all, so there is nothing to preview a picture on: " +
				"it answers once its watch is running, which is within a second of starting")
	}

	wanted := strings.TrimSpace(r.URL.Query().Get("light"))
	if wanted == "" {
		return targets, nil
	}

	chosen := make([]*previewTarget, 0, 1)

	for _, target := range targets {
		if target.named(wanted) {
			chosen = append(chosen, target)
		}
	}

	if len(chosen) == 0 {
		return nil, ControllerNotFound.
			New(fmt.Sprintf("This module drives no light called %q. Read GET /controllers for the ones it does.", wanted)).
			With(fault.StringParam("controller", wanted))
	}

	return chosen, nil
}

// previewPaint is one light's answer to a preview: the controller it went to, the name a caller
// could have used to target it, and — for a light that would not take the picture — the code it
// refused with and the sentence saying why.
//
// It is one record per light because a preview on a two-light install is two writes and one of them
// can be refused: an answer that said only "accepted" would be hiding a light that stayed dark, and
// one that failed the whole request because a strip cannot draw a word would be taking the panel's
// preview away with it.
type previewPaint struct {
	Controller string `json:"controller"`
	Light      string `json:"light"`
	Refused    string `json:"refused,omitempty"`
	Why        string `json:"why,omitempty"`
}

// previewRefusal is the two ways a light can refuse a preview, kept apart from the error so that
// the code can be reported per light and still be the registered code a caller branches on.
type previewRefusal int

const (
	refusalBorrowedSegment previewRefusal = iota
	refusalNotDrawable
)

// code is the registered code this refusal carries.
func (r previewRefusal) code() string {
	if r == refusalBorrowedSegment {
		return PreviewBorrowedSegment.Code().String()
	}

	return PreviewNotDrawable.Code().String()
}

// faultOf is the refusal as the route raises it when no light took the picture.
func (r previewRefusal) faultOf(message string) *fault.Fault {
	if r == refusalBorrowedSegment {
		return PreviewBorrowedSegment.New(message)
	}

	return PreviewNotDrawable.New(message)
}

// paintPreview fires one picture at every light the request is about, in the order the watch drives
// them, and answers one record per light.
//
// picture builds the event for one light, because a composed frame is drawn at the light's own
// geometry while a word and a pattern are the same event everywhere. Each writer stores its own
// copy, so one light taking the picture cannot change what another is handed.
//
// The refusal is the other half: a light that will not take the picture does not stop the others,
// and the route refuses as a whole only when not one light took it — with the code that fired first
// and a sentence naming every light, so that a caller reading one answer learns which lights are
// dark and why.
func paintPreview(targets []*previewTarget, picture func(*previewTarget) event) ([]previewPaint, *fault.Fault) {
	painted := make([]previewPaint, 0, len(targets))
	refusals := make([]string, 0, len(targets))
	kinds := make([]previewRefusal, 0, len(targets))

	took := 0

	for _, target := range targets {
		record := previewPaint{Controller: target.where, Light: target.light}

		err := target.pen.preview(picture(target))
		if err == nil {
			took++

			painted = append(painted, record)

			continue
		}

		kind := refusalNotDrawable
		if errors.Is(err, ErrPreviewBorrowedSegment) {
			kind = refusalBorrowedSegment
		}

		record.Refused, record.Why = kind.code(), err.Error()

		refusals = append(refusals, err.Error())
		kinds = append(kinds, kind)

		painted = append(painted, record)
	}

	if took == 0 {
		return nil, kinds[0].faultOf(strings.Join(refusals, "; "))
	}

	return painted, nil
}

// withLights adds the lights a preview was painted on to an answer, and the single controller field
// a caller reading a one-light install has always read.
//
// `controller` is kept for exactly the case it can be true in — a request that resolved to one
// light — and left out when there are several, where it would be naming one of them as though the
// answer were about it. `lights` is always there, so a caller with one light and a caller with four
// read the same field.
func withLights(body map[string]any, painted []previewPaint) map[string]any {
	body["lights"], body["count"] = painted, len(painted)

	if len(painted) == 1 {
		body["controller"] = painted[0].Controller
	}

	return body
}

// answer writes one JSON body, which is every route above that answers.
func answer(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	_ = json.NewEncoder(w).Encode(body)
}

// stdio is the pair the host handed over. Close shuts the reading half, which
// is what ends a session whose host has gone quiet.
type stdio struct{}

func (stdio) Read(p []byte) (int, error)  { return os.Stdin.Read(p) }
func (stdio) Write(p []byte) (int, error) { return os.Stdout.Write(p) }
func (stdio) Close() error                { return os.Stdin.Close() }
