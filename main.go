// Command wled-printer-status is a module that watches one printer and drives
// one WLED controller from what it sees. It is this process and nothing else: it
// draws no screen, and its whole surface is two routes.
//
// # The routes
//
//	GET /wled-printer-status   the line this module answers with, and who the host says is asking
//	GET /status                what this module currently thinks: the printer it watches, how old
//	                           its reading is, the frame it has decided, and the last send to the light
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
// module.yaml declares every setting this module reads, each with its type, its
// default and one sentence saying what it controls. The host merges the
// operator's config block over those defaults and sends the result on
// plugin.configure before the first request arrives. Nothing here reads an
// environment variable: a setting an operator cannot see in the manifest is a
// setting nobody can find.
//
//	message             the line GET /wled-printer-status answers with
//	controller_address  the WLED controller to drive; the manifest's egress entry names this setting
//	printer             which printer to watch, by device id or by name; empty takes the only one
//	brightness          the brightness written once at start, 0-255, when the whole strip is driven
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
	"fmt"
	"maps"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prusa3d/prusa-connect/sdk/deviceapi"
	"github.com/prusa3d/prusa-connect/sdk/extension"
	"github.com/prusa3d/prusa-connect/sdk/fault"
	"github.com/prusa3d/prusa-connect/sdk/routesapi"
	"github.com/prusa3d/prusa-connect/sdk/wire"
	"github.com/prusa3d/prusa-connect/sdk/wirerpc"

	"github.com/nothingmn/wled-printer-status/internal/printer"
	"github.com/nothingmn/wled-printer-status/internal/wled"
)

// What this child reports when the host asks it what it is. The name is the
// manifest's, and the host holds the two to each other.
const (
	name    = "wled-printer-status"
	release = extension.Version("0.1.0")
)

const (
	// defaultMessage is the line the route answers with when the operator sets
	// nothing. It is the default of the `message` setting in module.yaml, and the
	// two are the same words on purpose: what the manifest publishes is what this
	// code does when nobody overrides it. TestTheManifestPublishesTheCodesDefault
	// holds them together.
	defaultMessage = "Wled Printer Status is serving."

	// defaultBrightness is the brightness this module writes when the operator
	// sets none, and it is the default of the `brightness` setting in module.yaml
	// for the same reason as defaultMessage. Half of the controller's range is
	// bright enough to read across a room and is where WLED itself starts, so it is
	// the number an operator who wrote nothing already expects.
	defaultBrightness = 128

	// defaultSegment is the `segment` setting's default, and it means "the whole
	// strip", which is what this module drove before there was a setting at all.
	//
	// It is negative rather than absent because zero is a segment an operator may
	// name: a controller's first segment is segment 0, and a default of 0 would make
	// "drive the whole strip" impossible to say. It is written out in module.yaml too,
	// because the host applies that default and an operator reads it there.
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

// Settings is what an operator writes under this instance's config block. The
// host merges it over the defaults module.yaml declares and sends the result on
// plugin.configure. A key this struct does not carry refuses the start, so a
// typo is found before a request is.
type Settings struct {
	// Message is the line GET /wled-printer-status answers with.
	Message string `json:"message"`

	// ControllerAddress is the WLED controller this module drives, written as its
	// base URL: http://10.0.0.136, no path. The manifest's egress entry names this
	// key, because the host it connects to is one the operator types rather than one
	// this module could declare.
	ControllerAddress string `json:"controller_address"`

	// Printer is the printer to watch, either the host's device id for it or the
	// name an operator gave it. Empty watches the only printer the instance holds and
	// refuses to guess when there is more than one.
	Printer string `json:"printer"`

	// Brightness is what this module writes to the strip once at start, 0-255.
	// Nothing else here writes a brightness: the operator sets it and it stays where
	// they put it. With a segment named it is not written at all, because a brightness
	// is controller-wide and would move every segment on the controller.
	Brightness int `json:"brightness"`

	// Segment is the one segment of a multi-segment controller this module drives: the
	// operator's own numbering, as the controller reports it. The default, -1, drives the
	// whole strip as this module always has, and any number the controller holds names one
	// segment.
	//
	// Named, the module borrows that segment rather than taking it: it reads the segment's
	// run, colour, effect and name once at start, draws inside that run while it runs, and
	// writes all of it back when it stops. It writes no brightness and no power in this mode,
	// and it never writes a second segment.
	Segment int `json:"segment"`
}

// validate refuses settings this module cannot act on, before it acts on them.
//
// The address is held to wled.New's own rules rather than to a second copy of
// them, because that is the check the client makes at its first call and a
// configuration mistake is better named on the instance than discovered as a light
// that never changes.
func (s Settings) validate() error {
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

// The state a running instance holds.
//
// held is what an operator wrote, merged over the manifest's defaults. session and
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

	// watchMu guards watch, which is how a second plugin.configure stops the watch
	// the first one started. The host calls configure once; this exists so that a
	// second call replaces the first rather than putting a second writer on the same
	// light.
	//
	// watchDone is closed when that watch's writer has returned. A stop waits on it,
	// because the writer gives a borrowed segment back on its way out and a process that
	// exited first would leave somebody else's segment holding this module's last frame.
	watchMu   sync.Mutex
	watch     context.CancelFunc
	watchDone chan struct{}
)

// settings is what this module is running on: what the host sent, or the
// defaults this code holds when the host sent nothing.
func settings() Settings {
	if current := held.Load(); current != nil {
		return *current
	}

	return Settings{Message: defaultMessage, Brightness: defaultBrightness, Segment: defaultSegment}
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
		// The settings the host merged from module.yaml's defaults and this
		// instance's config block, sent once before the first request. This is where the
		// watch starts: the controller's address and the printer to watch are both
		// settings, so there is nothing to start before they arrive.
		wirerpc.MethodConfigure: wirerpc.Configure(configure),
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
func configure(ctx context.Context, sent Settings) error {
	if sent.Message == "" {
		sent.Message = defaultMessage
	}

	if err := sent.validate(); err != nil {
		return err
	}

	// The client is built now and contacts nothing until the writer asks it to.
	// wled.New refuses a bad address here rather than at the first call.
	if _, err := wled.New(sent.ControllerAddress); err != nil {
		return err
	}

	listing, cancel := context.WithTimeout(ctx, resolveTimeout)
	deviceID, err := resolvePrinter(listing, fleet, sent.Printer)
	cancel()

	if err != nil {
		return err
	}

	held.Store(&sent)

	// The set this build can express from its settings: one printer, on one controller, on one
	// segment. The core underneath runs a set rather than a pair (bindings.go, watch.go) because
	// the operator's decision of 2026-11-18 made the segment — not the controller — the unit, and
	// the surface that writes more than one binding is the UI half rather than a settings block
	// that cannot express a list. Nothing here decides what a binding is: it hands the core the
	// one the operator configured and lets the core's own rules judge it.
	wanted := bindings{{
		DeviceID:   deviceID,
		Address:    sent.ControllerAddress,
		Segment:    sent.Segment,
		Brightness: sent.Brightness,
	}}

	// A borrowed binding carries no brightness, whatever the setting says: its default is 128
	// and a brightness is controller-wide. The writer for this mode has no brightness to write
	// (see newSegmentWriter), and the set says so rather than carrying a number nothing reads —
	// bindings.check refuses one here.
	if sent.Segment >= 0 {
		wanted[0].Brightness = 0
	}

	// One watch at a time, and the one being replaced is waited for rather than only
	// cancelled: a writer gives its segment back on the way out, and a second writer that
	// borrowed the same segment before that happened would capture this module's own bar as
	// the segment's original state — and then give that bar back as though it were his.
	stopWatching()

	// Nothing has been started yet that a refusal here would have to undo: a set that cannot be
	// run leaves the light as it was and the operator holding the reason, which is the answer
	// for a configuration mistake.
	stopWatch, done, err := watchConfig{
		bindings: wanted,
		newSource: func(b binding) (watchSource, error) {
			source, err := printer.NewSource(printer.Config{DeviceID: b.DeviceID}, session, fleet)
			if err != nil {
				return nil, err
			}

			return source, nil
		},
		newLight: func(address string) (strip, error) { return wled.New(address) },
		logf:     logf,
	}.start(sessionCtx)

	if err != nil {
		return err
	}

	watchMu.Lock()
	watch, watchDone = stopWatch, done
	watchMu.Unlock()

	return nil
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
	watchMu.Lock()
	cancel, done := watch, watchDone
	watch, watchDone = nil, nil
	watchMu.Unlock()

	if cancel == nil || done == nil {
		return
	}

	cancel()

	wait, stop := context.WithTimeout(context.Background(), shutdownGrace)
	defer stop()

	select {
	case <-done:
	case <-wait.Done():
		logf("%s: the watch did not finish giving its segment back within %s of being stopped",
			name, shutdownGrace)
	}
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
func routes() http.Handler {
	mux := http.NewServeMux()

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
			answer(w, http.StatusOK, map[string]any{"bindings": held})

			return
		}

		seen := current.Load()
		if seen == nil {
			refuse(w, r, NoReadingYet.New(
				"this module has not decided a frame yet: it answers this once it has taken its first reading"))

			return
		}

		answer(w, http.StatusOK, statusAnswer{
			Printer:  seen.Printer,
			Reading:  seen.Reading,
			Frame:    seen.Frame,
			LastSend: sent.Load(),
		})
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
