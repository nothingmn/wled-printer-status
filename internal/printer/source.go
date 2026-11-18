package printer

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/prusa3d/prusa-connect/sdk/busapi"
	"github.com/prusa3d/prusa-connect/sdk/deviceapi"
	"github.com/prusa3d/prusa-connect/sdk/event"
)

// What a Source does when a caller leaves the timing unset.
const (
	// DefaultReadEvery is how often the printer's record is read when a caller
	// sets no interval. The record is what carries Connected, Retired and the
	// issues, and none of those arrives on the telemetry lane, so a module that
	// only subscribed would never learn that its printer went away. One read
	// every fifteen seconds is one call in flight at most, against a host that
	// allows eight.
	DefaultReadEvery = 15 * time.Second

	// DefaultWindowMS is the coalescing window asked of the bus when a caller
	// names none. The host's floor is 100ms and its ceiling is 10s
	// (busapi.MinIntervalMS, busapi.MaxIntervalMS), and a printer that is
	// printing publishes a burst every two seconds: one second folds each burst
	// into one window without holding a value back long enough to matter.
	DefaultWindowMS = 1000
)

const (
	// readTimeout bounds one device read. The host's own deadline on a call into
	// this module is 10s, and a read that outlived that would be a read whose
	// answer nobody was waiting for. A read that fails changes nothing, so the
	// snapshot ages rather than lying.
	readTimeout = 5 * time.Second

	// resubscribeDelay is how long a Source waits before following the stream
	// again after the host ended the subscription. The host ends one routinely —
	// a queue overflow, a resume it could not honour, a grant recomputed — and a
	// module that exited on that would be a module that never came back.
	resubscribeDelay = time.Second

	// problemsBuffer bounds the problems a caller may be slow to read. A full
	// buffer drops the newest problem rather than making the stream wait for a
	// consumer.
	problemsBuffer = 16
)

// The refusals a Source reports about how it was built, as opposed to what the
// host said. They are found at startup rather than at the first call.
var (
	// ErrNoDevice reports a Config that names no usable printer.
	ErrNoDevice = errors.New("printer: the config names no device")
	// ErrNoSession reports a Source built without the host session or without
	// the fleet client: there is nothing to subscribe on and nothing to read.
	ErrNoSession = errors.New("printer: the config has no host session")
)

// Devices is the one read this package makes of the host's fleet.
// *deviceapi.Client satisfies it.
type Devices interface {
	Get(ctx context.Context, params deviceapi.GetParams) (deviceapi.Device, error)
}

// Config is what a Source needs to watch one printer.
type Config struct {
	// DeviceID is the one printer this Source watches: the host's id for it. A
	// module carries a single printer, so there is no list here and no fleet.
	DeviceID string

	// ReadEvery is how often the printer's record is read. Zero takes
	// DefaultReadEvery.
	ReadEvery time.Duration

	// WindowMS is the coalescing window asked of the bus, in milliseconds. Zero
	// takes DefaultWindowMS.
	WindowMS int
}

// withDefaults fills in what a caller left unset.
func (c Config) withDefaults() Config {
	if c.ReadEvery <= 0 {
		c.ReadEvery = DefaultReadEvery
	}

	if c.WindowMS == 0 {
		c.WindowMS = DefaultWindowMS
	}

	return c
}

// Source watches one printer: it owns the bus subscription and the periodic
// device read, and folds both into one State.
//
// One method takes the readings, Snapshot, and it is safe to call from any
// goroutine at any time, including before Run and after it returns. Nothing here
// is presentation: it decides nothing about what a display shows.
type Source struct {
	config  Config
	conn    busapi.Opener
	devices Devices
	origin  string
	now     func() time.Time

	mu    sync.Mutex
	state State

	// resync is armed by the window callback when the host says continuity is
	// gone, and drained by the goroutine that reads. It is buffered and written
	// without waiting, so the callback never blocks on a read.
	resync chan struct{}

	// problems carries what could not be failed on, for a caller that logs.
	problems chan error
}

// NewSource prepares a Source for one printer. It makes no call: the first read
// happens when Run starts.
//
// conn is the module's session, which is what busapi.Subscribe opens a stream on,
// and devices is the fleet client the periodic read goes through. Both are
// required: a Source that cannot read its printer is not a Source.
func NewSource(config Config, conn busapi.Opener, devices Devices) (*Source, error) {
	if conn == nil || devices == nil {
		return nil, ErrNoSession
	}

	origin, err := event.NewOrigin(event.KindDevice, config.DeviceID)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrNoDevice, err)
	}

	return &Source{
		config:   config.withDefaults(),
		conn:     conn,
		devices:  devices,
		origin:   origin.String(),
		now:      time.Now,
		resync:   make(chan struct{}, 1),
		problems: make(chan error, problemsBuffer),
	}, nil
}

// Run watches the printer until ctx ends, and returns nil when it does.
//
// The first read happens before the subscription opens, so a snapshot taken in the
// first second of the module's life says what the printer is rather than nothing at
// all. After that the reads run on their own goroutine and the stream is followed on
// this one, which is the separation that keeps a slow host answer off the window
// path. A read that fails does not end the watch and a subscription the host ends is
// opened again a second later: neither is a reason to stop telling a caller what is
// known.
//
// Call it in its own goroutine, and take snapshots from anywhere.
func (s *Source) Run(ctx context.Context) error {
	s.readAndReport(ctx)

	go s.poll(ctx)

	return s.follow(ctx)
}

// Snapshot reads the printer as it stands now.
func (s *Source) Snapshot() Snapshot {
	at := s.now()

	s.mu.Lock()
	defer s.mu.Unlock()

	return s.state.Snapshot(at)
}

// Problems is where a caller learns what this package refused to believe: a read
// that failed, a value this build could not read, a subscription that ended.
//
// The channel is buffered and written without waiting, so a caller that never
// reads it costs the stream nothing beyond a dropped problem. Nothing that
// arrives here changes the snapshot: a failed read leaves the last one standing
// and lets it age, which is a fact a display can see for itself.
func (s *Source) Problems() <-chan error { return s.problems }

// poll reads the printer on the interval, and whenever the stream asked for a
// read. It runs on its own goroutine so that a read in flight is never a window
// waiting.
func (s *Source) poll(ctx context.Context) {
	ticker := time.NewTicker(s.config.ReadEvery)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-s.resync:
		case <-ticker.C:
		}

		s.readAndReport(ctx)
	}
}

// follow opens the subscription and keeps opening it, until ctx ends.
//
// It narrows to this printer's origin, which is one device's telemetry and nothing
// else on the deployment, and it does not narrow by type: a frame about the printer
// that is not a reading is left alone by Updates, and a type list would have to be
// extended every time the host grew one worth reading.
func (s *Source) follow(ctx context.Context) error {
	params := busapi.SubscribeParams{
		Origins:    []string{s.origin},
		IntervalMS: s.config.WindowMS,
	}

	for {
		err := busapi.Subscribe(ctx, s.conn, params, s.fold)

		if ctx.Err() != nil {
			return nil
		}

		s.report(err)

		select {
		case <-ctx.Done():
			return nil
		case <-time.After(resubscribeDelay):
		}
	}
}

// fold applies one window, and it runs on the goroutine that reads the stream.
//
// It therefore does no I/O and makes no host call. A window callback that waited
// on a network call would stop this module reading the stream for as long as the
// call took: the host queues nothing for a slow reader without bound, so the
// windows behind it are lost and continuity goes with them, and every snapshot
// taken meanwhile would wait on the same lock. The read a resync calls for is
// asked for and made elsewhere.
//
// A value this build cannot read is reported and dropped rather than returned:
// ending the subscription over one odd number would cost every path behind it.
func (s *Source) fold(window busapi.Window) error {
	if window.Resync {
		select {
		case s.resync <- struct{}{}:
		default:
		}
	}

	updates := Updates(window.Telemetry, s.config.DeviceID)
	if len(updates) == 0 {
		return nil
	}

	at := s.now()

	s.mu.Lock()
	defer s.mu.Unlock()

	next, err := s.state.WithUpdates(updates, at)
	s.state = next

	s.report(err)

	return nil
}

// read reads the printer's record once and folds it in.
func (s *Source) read(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, readTimeout)
	defer cancel()

	device, err := s.devices.Get(ctx, deviceapi.GetParams{ID: s.config.DeviceID})
	if err != nil {
		return fmt.Errorf("printer: read %s: %w", s.config.DeviceID, err)
	}

	at := s.now()

	s.mu.Lock()
	defer s.mu.Unlock()

	next, applyErr := s.state.WithRecord(RecordFromDevice(device), at)
	s.state = next

	return applyErr
}

// readAndReport reads once and reports what went wrong, unless it went wrong
// because the caller is shutting down.
func (s *Source) readAndReport(ctx context.Context) {
	if err := s.read(ctx); err != nil && ctx.Err() == nil {
		s.report(err)
	}
}

// report offers one problem to a caller that may not be reading.
func (s *Source) report(err error) {
	if err == nil {
		return
	}

	select {
	case s.problems <- err:
	default:
	}
}
