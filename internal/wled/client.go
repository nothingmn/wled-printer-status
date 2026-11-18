// Package wled is an HTTP client for one WLED controller.
//
// It reads a controller's facts and its current state, and it writes a power
// change, a brightness, or a colour to the strip. It knows nothing about
// printers: no idea what a job is, what colour a paused print should be, or how
// often anyone should look. Those are the caller's decisions; this package is
// the part that can be tested without one.
//
// The endpoints are WLED's own: GET /json/info for the device's facts, GET
// /json/state for what the strip is showing, and POST /json/state carrying a
// subset of that same state to change it. WLED accepts any subset on a write,
// which is what makes every call below a small body rather than a whole
// document.
//
// # Why every request is bounded at two seconds
//
// The host that supervises a Prusa Connect module pings it every 15s and waits
// 5s for the answer; three unanswered pings and the process is stopped until a
// human starts it again. A request made here has to finish well inside that
// window — including whichever one is in flight when a ping arrives — so
// DefaultTimeout is 2s. That is short enough to leave room for the answer and
// the work around it, and long enough for a controller on the same LAN, which
// answers in single-digit milliseconds. A caller whose path is slower can raise
// it with WithTimeout, but raising it past 5s means one slow controller can cost
// the whole module its life.
//
// # One call at a time
//
// WLED asks that calls to a controller be serialised rather than fired in
// parallel: it has one small web server, and concurrent writes interleave in
// ways nothing on the wire promises. Client holds a mutex for that reason, so
// two goroutines sharing one Client queue at the device instead of racing
// there. The cost is that a call which is timing out holds the others for up to
// the timeout, which is why the timeout is short.
//
// # A strip and a panel
//
// The same client drives a few-pixel strip and a 16,384-pixel panel, because the
// only difference it sees is geometry: Info carries the panel's width and height
// when the controller publishes a matrix, and a State segment carries its row
// bounds beside its LED bounds. SetRectangle, and the row options on SetSolid and
// SetRange, are what make a write a rectangle on a panel.
//
// # The three tiers a status can be shown in
//
// A controller's abilities are not assumed: they are read from the device and
// used in order, so that a status which cannot be shown in the first way is shown
// in the second and, if even that is impossible, in the third.
//
//  1. An effect, with its parameters. SetEffect selects one of the effects the
//     controller itself lists — Capabilities returns that list — and names its
//     speed, intensity, three custom values, three check values and palette.
//     Scrolling Text (id 122 on the current firmware) is the 2D panel's text
//     carrier, and the string it draws is the segment's own name, written with
//     SetSegmentName. This tier animates, and it is the only tier that puts text
//     on a matrix.
//  2. Computed colour or geometry: SetSolid, SetRange and SetRectangle paint a
//     colour into a run, a band or a rectangle, so a progress bar is the geometry
//     of a segment rather than a picture of one.
//  3. Per-LED painting, Paint. It is the floor: whatever a controller can do, it
//     can be given 1, 2 or 16,384 individual colours, so a status that cannot be
//     shown as an effect or as a rectangle can still be shown at all. That is the
//     only outcome this package refuses to accept, and it is why this tier exists
//     even though it is the crudest.
//
// # What per-LED painting costs
//
// The segment property i is not stored: a controller that loses power or is
// rebooted comes back with whatever its saved preset says, so Paint has to be
// re-sent after every reconnect. While painted, the segment's effect is frozen —
// an animation cannot draw over a list of colours — and the list is cleared as
// soon as an effect is selected again. A write that sets individual LEDs and
// switches the segment on in the same body does not work, so Paint sends only i
// and expects a segment that is already on and already at its brightness.
//
// # What this package does not do
//
// It never writes {"live":true}, which hijacks the strip into realtime mode and
// blanks it, and it never writes a preset, a playlist, or the segment geometry of
// a controller whose matrix it has not read. It draws nothing and maps no printer
// state to a colour: which tier to use, and what a colour means, are the caller's
// decisions.
package wled

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DefaultTimeout is how long one request to a controller may take. It is 2s for
// the reason in the package comment: the host's ping gives this module 5s, and a
// request that outlived that would cost the module three missed pings and its
// life.
const DefaultTimeout = 2 * time.Second

const (
	// pathInfo is the device's facts: firmware version, LED count, segment
	// limit, UDP port, whether something is currently streaming into it.
	pathInfo = "/json/info"

	// pathState is the strip: power, brightness, transition, and one entry per
	// segment. A GET reads it and a POST carrying a subset of it changes it.
	pathState = "/json/state"

	// pathJSON is the controller's catalogue: the effects and palettes it can
	// show, and the per-effect flag strings that say which controls each one has.
	pathJSON = "/json"

	// solidEffect is WLED's effect id for a steady colour. Writing it is what
	// keeps a colour from showing the last effect's animation in it.
	solidEffect = 0

	// firstSegment is the segment every write below addresses. One controller
	// drives one printer's strip, so there is only ever this one.
	firstSegment = 0

	// maxBody is the most of any answer this package will read. /json/info on a
	// large controller is a few kilobytes; a megabyte is far past that and still
	// bounded, so a controller that answers with something enormous cannot spend
	// the module's 128 MiB budget.
	maxBody = 1 << 20

	// MaxSegmentName is the longest segment name a controller stores, and so the
	// longest string that can be drawn by the text effect. A longer name is refused
	// rather than truncated: a caller that asked for 60 characters and got 48 has a
	// bug it should see, and half a word on a panel is not a status.
	MaxSegmentName = 48

	// DefaultPaintByteBudget is how large one per-LED request body may be when the
	// caller sets no budget. It is 8 KiB, comfortably inside both documented limits
	// — about 10 KiB on an ESP8266 and about 24 KiB on an ESP32 — so a controller
	// with the smaller one has room for the rest of the request beside the colours.
	DefaultPaintByteBudget = 8 << 10

	// The values of a segment's m12, which says how a one-dimensional effect is
	// expanded onto a two-dimensional panel.
	ExpandPixels = 0 // the effect drives individual pixels
	ExpandBar    = 1 // the effect is drawn as a bar across the panel
	ExpandArc    = 2 // the effect is drawn as an arc
	ExpandCorner = 3 // the effect is drawn in a corner
)

// The failures a caller can tell apart. Each is wrapped by a typed error below,
// so errors.Is asks the question the caller mean and errors.As gets the detail.
var (
	// ErrUnreachable means the controller could not be talked to at all: refused
	// the connection, no route, DNS failed, the connection dropped.
	ErrUnreachable = errors.New("wled: controller unreachable")

	// ErrTimeout means the controller did not answer inside the deadline. It is
	// kept apart from ErrUnreachable because a slow controller and an absent one
	// are different operational problems: one is a device that is overloaded or
	// asleep, the other is a device that is gone.
	ErrTimeout = errors.New("wled: controller did not answer in time")

	// ErrStatus means the controller answered with a failing HTTP status and a
	// body this package could not read as one of WLED's own refusals.
	ErrStatus = errors.New("wled: controller answered with a failing status")

	// ErrDevice means the controller answered with WLED's own {"error":n}: it was
	// reached, understood the request, and refused it.
	ErrDevice = errors.New("wled: controller refused the request")

	// ErrInvalidBrightness means a brightness outside 0-255 was asked for. The
	// request is not sent: a controller would clamp it silently, and a caller
	// that asked for 256 has a bug it should see.
	ErrInvalidBrightness = errors.New("wled: brightness outside 0-255")

	// ErrInvalidRange means a run of LEDs was named that is empty or outside the
	// strip. The request is not sent.
	ErrInvalidRange = errors.New("wled: not a usable run of leds")

	// ErrInvalidEffect means an effect parameter was outside 0-255, which is what a
	// controller's own fields carry. The request is not sent.
	ErrInvalidEffect = errors.New("wled: an effect parameter is outside 0-255")

	// ErrInvalidRectangle means a rectangle was named that covers nothing or is
	// inverted in one axis: a panel segment whose stop is not past its start takes
	// the segment away rather than drawing nothing, so it is refused here. The
	// request is not sent.
	ErrInvalidRectangle = errors.New("wled: not a usable rectangle")

	// ErrInvalidName means a segment name was empty or longer than MaxSegmentName.
	// The request is not sent.
	ErrInvalidName = errors.New("wled: not a usable segment name")

	// ErrEmptyPaint means per-LED painting was asked for with no colours. There is
	// nothing to draw and no request worth sending; a caller that reached here has
	// computed an empty frame.
	ErrEmptyPaint = errors.New("wled: no colours to paint")

	// ErrPaintBudget means the byte budget left no room for even one LED's colour.
	// The budget is a caller's promise about the controller it is talking to, so a
	// budget that cannot carry a colour is refused rather than exceeded.
	ErrPaintBudget = errors.New("wled: the paint budget cannot carry one led")
)

// TimeoutError is a request that ran out of time. Its Err carries the transport's
// own error, and errors.Is finds ErrTimeout through it.
type TimeoutError struct {
	// URL is the endpoint that did not answer.
	URL string
	// Err is what the transport reported, usually a context deadline.
	Err error
}

// Error states the endpoint and the cause.
func (e *TimeoutError) Error() string {
	return fmt.Sprintf("wled: %s did not answer in time: %v", e.URL, e.Err)
}

// Unwrap exposes both the sentinel and the transport's error to errors.Is and
// errors.As.
func (e *TimeoutError) Unwrap() []error {
	if e.Err == nil {
		return []error{ErrTimeout}
	}

	return []error{ErrTimeout, e.Err}
}

// UnreachableError is a controller that could not be talked to at all.
type UnreachableError struct {
	// URL is the endpoint that could not be reached.
	URL string
	// Err is what the transport reported: a refused connection, a drop, a name
	// that did not resolve.
	Err error
}

// Error states the endpoint and the cause.
func (e *UnreachableError) Error() string {
	return fmt.Sprintf("wled: %s is unreachable: %v", e.URL, e.Err)
}

// Unwrap exposes both the sentinel and the transport's error to errors.Is and
// errors.As.
func (e *UnreachableError) Unwrap() []error {
	if e.Err == nil {
		return []error{ErrUnreachable}
	}

	return []error{ErrUnreachable, e.Err}
}

// StatusError is an answer whose HTTP status is not 2xx and whose body is not one
// of WLED's refusals. It is what a proxy in the way or a controller mid-restart
// looks like.
type StatusError struct {
	// StatusCode is the status the controller answered with.
	StatusCode int
	// URL is the endpoint that answered.
	URL string
	// Body is the start of the answer, kept so a log line says what came back.
	Body string
}

// Error states the status and the start of the body.
func (e *StatusError) Error() string {
	return fmt.Sprintf("wled: %s answered %d %s: %s",
		e.URL, e.StatusCode, http.StatusText(e.StatusCode), e.Body)
}

// Unwrap makes errors.Is(err, ErrStatus) true for this error.
func (e *StatusError) Unwrap() error { return ErrStatus }

// DeviceError is WLED's own refusal, {"error":n}, which the controller sends with
// a 2xx status or a 4xx one depending on which check it failed. The code is
// WLED's; this package does not interpret it.
type DeviceError struct {
	// Code is the number WLED put in the body.
	Code int
	// StatusCode is the HTTP status the refusal arrived with, or 0 when it came
	// with a 2xx.
	StatusCode int
	// URL is the endpoint that refused the request.
	URL string
	// Body is the start of the answer, kept so a log line says what came back.
	Body string
}

// Error states the controller's code and the HTTP status beside it.
func (e *DeviceError) Error() string {
	if e.StatusCode != 0 {
		return fmt.Sprintf("wled: %s refused the request with error %d (HTTP %d): %s",
			e.URL, e.Code, e.StatusCode, e.Body)
	}

	return fmt.Sprintf("wled: %s refused the request with error %d: %s", e.URL, e.Code, e.Body)
}

// Unwrap makes both errors.Is(err, ErrDevice) true and, when the refusal came with
// a failing status, errors.Is(err, ErrStatus) true as well. A caller that only
// cares that the write did not happen asks one question either way.
func (e *DeviceError) Unwrap() []error {
	if e.StatusCode != 0 {
		return []error{ErrDevice, ErrStatus}
	}

	return []error{ErrDevice}
}

// RGB is one colour, one byte per channel, which is what WLED takes.
type RGB struct {
	// R is the red channel.
	R uint8
	// G is the green channel.
	G uint8
	// B is the blue channel.
	B uint8
}

// channels is the colour as the three numbers WLED reads inside a "col" entry.
func (c RGB) channels() []int { return []int{int(c.R), int(c.G), int(c.B)} }

// Info is the facts a controller reports about itself. Every field is optional on
// the wire: WLED versions differ in what they publish, and a field a version does
// not publish reads here as its zero value rather than failing the read.
type Info struct {
	// Version is the firmware version, "ver" on the wire, such as "0.14.0".
	Version string
	// Name is the name the controller was given, "name" on the wire.
	Name string
	// LEDCount is how many LEDs the controller drives, "leds.count". It is
	// controller-dependent and is never assumed by this package.
	LEDCount int
	// MaxSegments is how many segments the controller supports, "leds.maxseg".
	// Also controller-dependent.
	MaxSegments int
	// MatrixWidth is the panel's width in pixels, "leds.matrix.w", and 0 on a 1D
	// controller, which publishes no matrix at all.
	MatrixWidth int
	// MatrixHeight is the panel's height in pixels, "leds.matrix.h", and 0 on a 1D
	// controller. A non-zero height is what tells a caller the LEDs are a
	// rectangle laid out row by row rather than one run.
	MatrixHeight int
	// UDPPort is the port its realtime UDP protocols listen on, "udpport". This
	// package does not use them; the number is here because a caller that
	// configures the controller may need it.
	UDPPort int
	// Live reports whether something is currently streaming into the controller,
	// "live". A controller in live mode ignores the writes below.
	Live bool
}

// Segment is one length of the strip, as the controller reports it. A 2D
// controller expresses a segment as a rectangle, so the row bounds travel beside
// the LED bounds; on a 1D controller both are absent and read as zero.
//
// The fields WLED omits when they equal the surrounding default read here as zero:
// in particular an omitted "on" reads as false and an omitted "bri" as 0, so a caller
// that needs to tell "off" from "not stated" should compare against the strip's own
// brightness and power.
type Segment struct {
	// ID is the segment's number, which is the id every write below addresses.
	ID int
	// Start is the first LED it covers, inclusive.
	Start int
	// Stop is the LED after its last one, exclusive, as WLED defines it.
	Stop int
	// StartY is the first row it covers, inclusive, on a 2D controller; 0 on a 1D
	// one, which has no rows.
	StartY int
	// StopY is the row after its last one, exclusive, on a 2D controller; 0 on a 1D
	// one.
	StopY int
	// On reports whether the segment is drawn.
	On bool
	// Colors is the segment's palette: one colour for a solid segment, up to
	// three for a gradient.
	Colors []RGB
	// Effect is the effect id it is running, "fx". 0 is solid.
	Effect int
	// Name is the segment's own name, "n". It is what the text effect draws, and
	// it is empty on a segment that has never been named.
	Name string
	// Speed is the effect's speed, "sx"; 0 when unset.
	Speed int
	// Intensity is the effect's intensity, "ix"; 0 when unset. For the text effect
	// it is the vertical position.
	Intensity int
	// Custom1, Custom2 and Custom3 are the effect's own three values, "c1", "c2"
	// and "c3". For the text effect Custom2 is the font size.
	Custom1 int
	// Custom2 is the effect's second custom value.
	Custom2 int
	// Custom3 is the effect's third custom value.
	Custom3 int
	// Check1, Check2 and Check3 are the effect's three check values, "o1", "o2" and
	// "o3".
	Check1 int
	// Check2 is the effect's second check value.
	Check2 int
	// Check3 is the effect's third check value.
	Check3 int
	// Palette is the palette id the effect draws with, "pal".
	Palette int
	// Grouping is how many LEDs each colour covers, "grp". 0 when unset, which a
	// controller treats as one.
	Grouping int
	// Transpose is the segment's rotation code, "tp". 0 is no rotation.
	Transpose int
	// Expand1D is how a one-dimensional effect is laid onto a panel, "m12": one of
	// ExpandPixels, ExpandBar, ExpandArc or ExpandCorner.
	Expand1D int
	// Brightness is the segment's own brightness 0-255 when it has one; 0 when
	// WLED left it to the strip's brightness.
	Brightness int
}

// State is what the strip is doing now.
type State struct {
	// On is the strip's power, "on".
	On bool
	// Brightness is 0-255, "bri".
	Brightness int
	// Transition is the fade length in 100 ms units, "transition", as the
	// controller reports it.
	Transition int
	// Segments is one entry per segment the controller draws.
	Segments []Segment
}

// Client talks to one controller. It is safe to use from several goroutines, and
// it puts their calls in a queue so the controller sees one at a time.
type Client struct {
	base string
	http *http.Client
	mu   sync.Mutex
}

// Option adjusts a Client as New builds it. Options are applied in the order they
// are passed, so WithHTTPClient after WithTimeout replaces the client and its
// timeout with whatever the caller supplied.
type Option func(*Client)

// WithTimeout sets how long one request may take, replacing DefaultTimeout. See
// the package comment for why the default is 2s and why raising it past 5s is a
// bad trade.
func WithTimeout(timeout time.Duration) Option {
	return func(c *Client) { c.http.Timeout = timeout }
}

// WithHTTPClient makes the Client use the caller's http.Client, which is how a
// caller supplies its own Transport, proxy or TLS settings. The client's Timeout
// is the request deadline, so a caller that passes one should set it.
func WithHTTPClient(client *http.Client) Option {
	return func(c *Client) {
		if client != nil {
			c.http = client
		}
	}
}

// New builds a Client for the controller at baseURL, which is a base such as
// "http://10.0.0.136" with or without a trailing slash. It does not contact the
// controller: the first call that does is Info, State, or one of the writes.
//
// An address that is not an absolute http or https URL is refused here rather
// than at the first call, because a typo in an address is a configuration
// mistake and the host reports a process that will not start far more clearly
// than it reports one that will not talk to a light.
func New(baseURL string, opts ...Option) (*Client, error) {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		return nil, errors.New("wled: the controller address is empty")
	}

	parsed, err := url.Parse(base)
	if err != nil {
		return nil, fmt.Errorf("wled: the controller address %q is not a URL: %w", baseURL, err)
	}

	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("wled: the controller address %q needs an http or https scheme, not %q",
			baseURL, parsed.Scheme)
	}

	if parsed.Host == "" {
		return nil, fmt.Errorf("wled: the controller address %q names no host", baseURL)
	}

	client := &Client{base: base, http: &http.Client{Timeout: DefaultTimeout}}

	for _, option := range opts {
		if option != nil {
			option(client)
		}
	}

	return client, nil
}

// Info reads the controller's facts. WLED answers with more fields than this
// struct names, on every version, and they are ignored: a package that refused an
// answer because a firmware added a key would break on the next firmware update.
//
// The matrix geometry is read separately and tolerantly. A 1D controller publishes
// no "leds.matrix", and a firmware that publishes something else there is a
// controller whose LED count this package can still use, so a matrix it cannot read
// leaves the two fields at 0 rather than failing the call.
func (c *Client) Info(ctx context.Context) (Info, error) {
	var answer struct {
		Version string `json:"ver"`
		Name    string `json:"name"`
		UDPPort int    `json:"udpport"`
		Live    bool   `json:"live"`
		LEDs    struct {
			Count       int             `json:"count"`
			MaxSegments int             `json:"maxseg"`
			Matrix      json.RawMessage `json:"matrix"`
		} `json:"leds"`
	}

	if err := c.do(ctx, http.MethodGet, pathInfo, nil, &answer); err != nil {
		return Info{}, err
	}

	width, height := decodeMatrix(answer.LEDs.Matrix)

	return Info{
		Version:      answer.Version,
		Name:         answer.Name,
		LEDCount:     answer.LEDs.Count,
		MaxSegments:  answer.LEDs.MaxSegments,
		MatrixWidth:  width,
		MatrixHeight: height,
		UDPPort:      answer.UDPPort,
		Live:         answer.Live,
	}, nil
}

// decodeMatrix reads "leds.matrix" into a width and a height, or 0 and 0 when the
// controller publishes none or publishes something unrecognisable.
func decodeMatrix(raw json.RawMessage) (int, int) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return 0, 0
	}

	var matrix struct {
		Width  int `json:"w"`
		Height int `json:"h"`
	}

	if err := json.Unmarshal(raw, &matrix); err != nil {
		return 0, 0
	}

	if matrix.Width < 0 || matrix.Height < 0 {
		return 0, 0
	}

	return matrix.Width, matrix.Height
}

// State reads what the strip is doing now.
func (c *Client) State(ctx context.Context) (State, error) {
	var answer struct {
		On         bool `json:"on"`
		Brightness int  `json:"bri"`
		Transition int  `json:"transition"`
		Segments   []struct {
			ID         int             `json:"id"`
			Start      int             `json:"start"`
			Stop       int             `json:"stop"`
			StartY     int             `json:"startY"`
			StopY      int             `json:"stopY"`
			On         bool            `json:"on"`
			Colors     json.RawMessage `json:"col"`
			Effect     int             `json:"fx"`
			Name       string          `json:"n"`
			Speed      int             `json:"sx"`
			Intensity  int             `json:"ix"`
			Custom1    int             `json:"c1"`
			Custom2    int             `json:"c2"`
			Custom3    int             `json:"c3"`
			Check1     int             `json:"o1"`
			Check2     int             `json:"o2"`
			Check3     int             `json:"o3"`
			Palette    int             `json:"pal"`
			Grouping   int             `json:"grp"`
			Transpose  int             `json:"tp"`
			Expand1D   int             `json:"m12"`
			Brightness int             `json:"bri"`
		} `json:"seg"`
	}

	if err := c.do(ctx, http.MethodGet, pathState, nil, &answer); err != nil {
		return State{}, err
	}

	state := State{
		On:         answer.On,
		Brightness: answer.Brightness,
		Transition: answer.Transition,
	}

	if len(answer.Segments) > 0 {
		state.Segments = make([]Segment, 0, len(answer.Segments))
	}

	for _, segment := range answer.Segments {
		colors, err := decodeColors(segment.Colors)
		if err != nil {
			return State{}, fmt.Errorf("wled: read the colour of segment %d: %w", segment.ID, err)
		}

		state.Segments = append(state.Segments, Segment{
			ID:         segment.ID,
			Start:      segment.Start,
			Stop:       segment.Stop,
			StartY:     segment.StartY,
			StopY:      segment.StopY,
			On:         segment.On,
			Colors:     colors,
			Effect:     segment.Effect,
			Name:       segment.Name,
			Speed:      segment.Speed,
			Intensity:  segment.Intensity,
			Custom1:    segment.Custom1,
			Custom2:    segment.Custom2,
			Custom3:    segment.Custom3,
			Check1:     segment.Check1,
			Check2:     segment.Check2,
			Check3:     segment.Check3,
			Palette:    segment.Palette,
			Grouping:   segment.Grouping,
			Transpose:  segment.Transpose,
			Expand1D:   segment.Expand1D,
			Brightness: segment.Brightness,
		})
	}

	return state, nil
}

// SetPower switches the strip on or off.
func (c *Client) SetPower(ctx context.Context, on bool) error {
	return c.do(ctx, http.MethodPost, pathState, writeState{On: &on}, nil)
}

// SetBrightness sets the strip's brightness, 0-255. A number outside that range is
// refused with ErrInvalidBrightness and nothing is sent: WLED would clamp it, and
// silent clamping hides a caller's arithmetic.
//
// This does not switch the strip on. A brightness written to a strip that is off
// is stored and takes effect when something switches it on, and the decision about
// whether asking for brightness should also mean asking for light belongs to the
// caller.
func (c *Client) SetBrightness(ctx context.Context, brightness int) error {
	if brightness < 0 || brightness > 255 {
		return fmt.Errorf("%w: %d is not 0-255", ErrInvalidBrightness, brightness)
	}

	return c.do(ctx, http.MethodPost, pathState, writeState{Brightness: &brightness}, nil)
}

// solid is what the options to SetSolid fill in.
type solid struct {
	brightness *int
	transition *int
	ledCount   int
	rows       *rows
}

// rows is a span of panel rows: the first row and how many, which is the same
// shape SetRange takes for LEDs, so a caller counts rows the way it counts pixels.
type rows struct {
	first int
	count int
}

// stop is the row after the last one, exclusive, as WLED defines it.
func (r rows) stop() int { return r.first + r.count }

// validate reports whether the span names at least one row.
func (r rows) validate() error {
	if r.first < 0 || r.count <= 0 {
		return fmt.Errorf("%w: rows from %d, %d of them", ErrInvalidRectangle, r.first, r.count)
	}

	return nil
}

// SolidOption adjusts one SetSolid call.
type SolidOption func(*solid)

// WithSolidBrightness sets the strip's brightness as part of the same write, so
// the colour and the brightness change in one call rather than two. A number
// outside 0-255 is refused with ErrInvalidBrightness before anything is sent.
func WithSolidBrightness(brightness int) SolidOption {
	return func(s *solid) { s.brightness = &brightness }
}

// WithSolidFade fades into the colour over milliseconds. WLED counts a transition
// in 100 ms units and 0 means no fade, so the request is rounded up and anything
// above zero becomes at least one unit: 700 ms is 7, 50 ms is 1.
func WithSolidFade(milliseconds int) SolidOption {
	return func(s *solid) {
		units := fadeUnits(milliseconds)
		s.transition = &units
	}
}

// WithSolidLEDCount states how long the strip is, so the write covers all of it.
// WLED's segment 0 spans the whole strip until something narrows it — a SetRange
// call does — and without the count a SetSolid cannot widen it back. A caller that
// only ever writes whole colours can leave this out.
func WithSolidLEDCount(count int) SolidOption {
	return func(s *solid) { s.ledCount = count }
}

// WithSolidRows states which rows of a panel the whole-strip write covers: firstRow
// is the first row, inclusive, and rowCount is how many. It is how a solid colour
// becomes a full-width band or the whole panel instead of one row.
//
// The row bounds travel with the column bounds on purpose. A write that named
// "start" and "stop" alone would be read as a flat LED index range and would
// collapse the segment to a single row of the panel, so a caller that draws on a
// panel passes the rows it means. A row span that names no row is refused with
// ErrInvalidRectangle before anything is sent.
func WithSolidRows(firstRow, rowCount int) SolidOption {
	return func(s *solid) { s.rows = &rows{first: firstRow, count: rowCount} }
}

// fadeUnits converts milliseconds to WLED's 100 ms transition units, rounding up
// so that a fade someone asked for is never silently dropped to none.
func fadeUnits(milliseconds int) int {
	if milliseconds <= 0 {
		return 0
	}

	return (milliseconds + 99) / 100
}

// SetSolid paints the whole strip one colour, at WLED's solid effect, so the last
// effect the strip was running does not animate the new colour. The optional
// brightness and fade travel in the same write.
//
// The whole strip means the segment WLED calls 0, which covers every LED until
// something narrows it. Pass WithSolidLEDCount when the strip's length is known and
// the write must cover all of it regardless.
func (c *Client) SetSolid(ctx context.Context, color RGB, opts ...SolidOption) error {
	settings := solid{}

	for _, option := range opts {
		if option != nil {
			option(&settings)
		}
	}

	if settings.brightness != nil && (*settings.brightness < 0 || *settings.brightness > 255) {
		return fmt.Errorf("%w: %d is not 0-255", ErrInvalidBrightness, *settings.brightness)
	}

	if settings.rows != nil {
		if err := settings.rows.validate(); err != nil {
			return err
		}
	}

	on := true
	effect := solidEffect

	segment := writeSegment{
		ID:     firstSegment,
		On:     &on,
		Colors: [][]int{color.channels()},
		Effect: &effect,
	}

	if settings.ledCount > 0 {
		start, stop := 0, settings.ledCount
		segment.Start = &start
		segment.Stop = &stop
	}

	if settings.rows != nil {
		startY, stopY := settings.rows.first, settings.rows.stop()
		segment.StartY = &startY
		segment.StopY = &stopY
	}

	return c.do(ctx, http.MethodPost, pathState, writeState{
		Brightness: settings.brightness,
		Transition: settings.transition,
		Segments:   []writeSegment{segment},
	}, nil)
}

// rangeRequest is what the options to SetRange fill in.
type rangeRequest struct {
	ledCount int
	rows     *rows
}

// RangeOption adjusts one SetRange call.
type RangeOption func(*rangeRequest)

// WithRangeLEDCount states how many LEDs the controller drives, so a run that
// reaches past the end of the strip is clamped to the strip instead of asking for
// LEDs that do not exist. The number is the "leds.count" that Info reports; this
// package will not fetch it behind a caller's back.
func WithRangeLEDCount(count int) RangeOption {
	return func(r *rangeRequest) { r.ledCount = count }
}

// WithRangeRows states which rows of a panel the run covers: firstRow is the first
// row, inclusive, and rowCount is how many. On a panel a segment is a rectangle, so
// a run of LEDs is a band across the rows it names; without the rows the controller
// reads the LED bounds as a flat index range and folds the segment onto one row.
//
// A row span that names no row is refused with ErrInvalidRectangle before anything
// is sent.
func WithRangeRows(firstRow, rowCount int) RangeOption {
	return func(r *rangeRequest) { r.rows = &rows{first: firstRow, count: rowCount} }
}

// SetRange sets a contiguous run of LEDs to one colour: first is the first LED it
// covers, inclusive, and count is how many, so the write carries start=first and
// stop=first+count as WLED defines them.
//
// This is a primitive, not a progress bar. Nothing here knows what a run in the
// middle of a strip means; a caller that draws one decides which LEDs and which
// colour.
//
// A first below zero, a count of zero or less, or a run that starts past the end of
// a strip whose length was supplied is refused with ErrInvalidRange and nothing is
// sent.
func (c *Client) SetRange(ctx context.Context, first, count int, color RGB, opts ...RangeOption) error {
	settings := rangeRequest{}

	for _, option := range opts {
		if option != nil {
			option(&settings)
		}
	}

	stop := first + count
	if settings.ledCount > 0 && stop > settings.ledCount {
		stop = settings.ledCount
	}

	if first < 0 || count <= 0 || stop <= first {
		return fmt.Errorf("%w: first %d and count %d over a strip of %d",
			ErrInvalidRange, first, count, settings.ledCount)
	}

	if settings.rows != nil {
		if err := settings.rows.validate(); err != nil {
			return err
		}
	}

	on := true
	effect := solidEffect

	segment := writeSegment{
		ID:     firstSegment,
		Start:  &first,
		Stop:   &stop,
		On:     &on,
		Colors: [][]int{color.channels()},
		Effect: &effect,
	}

	if settings.rows != nil {
		startY, stopY := settings.rows.first, settings.rows.stop()
		segment.StartY = &startY
		segment.StopY = &stopY
	}

	return c.do(ctx, http.MethodPost, pathState, writeState{
		Segments: []writeSegment{segment},
	}, nil)
}

// SegmentWrite is one segment of one write: the id the controller knows it by, the run of
// pixels it covers, whether it is drawn, and the colour it is drawn in. It is what a caller
// asks for. Segment is what a controller reports afterwards, and the two are different
// shapes because a caller states an intent and the controller answers with everything it
// holds.
//
// The span is a run — First is the first one, inclusive, and Count is how many — and what a
// run *is* depends on the controller, which is why the row bounds travel beside it. On a 1D
// controller a run is pixels and RowCount stays zero. On a matrix a segment is a rectangle,
// so Count is a number of **columns** and RowCount is the panel rows it spans: a flat pixel
// index cannot be expressed at all, because the firmware clamps start and stop to the
// panel's width and folds whatever is left onto one row. AGENTS.md §5.1 is the measurement.
type SegmentWrite struct {
	// ID is the segment's number. A controller draws them in order, so a later segment
	// paints over an earlier one.
	ID int
	// First is the first pixel the segment covers, counted from the start of the strip,
	// inclusive — or, on a matrix, its first column.
	First int
	// Count is how many pixels it covers, or how many columns on a matrix. Zero means no
	// span of its own: the segment keeps whatever span the controller holds for it, which
	// is the whole strip on one that has never been narrowed.
	Count int
	// FirstRow is the first panel row the segment covers, inclusive. It is zero on a strip,
	// which has no rows, and zero on a matrix is the top row.
	FirstRow int
	// RowCount is how many panel rows it covers. Zero means no rows at all: the segment
	// keeps the flat, one-row shape a strip has, which is what the 1D body sends and what
	// gives a 1D controller the run it expects.
	RowCount int
	// On reports whether the segment is drawn. A segment that is off draws nothing, so it
	// paints nothing over the segments beneath it — that is how a region is cleared rather
	// than coloured, and it is what keeps a stale bar from a previous frame out of a frame
	// that has none.
	On bool
	// Colour is what the segment is drawn in. It is sent only while On: a segment that is
	// off has nothing to name a colour for.
	Colour RGB
}

// validate reports whether this is a segment a controller can be asked for.
func (s SegmentWrite) validate() error {
	if s.ID < 0 {
		return fmt.Errorf("%w: segment id %d", ErrInvalidRange, s.ID)
	}

	if s.First < 0 || s.Count < 0 || s.FirstRow < 0 || s.RowCount < 0 {
		return fmt.Errorf("%w: segment %d covers %d pixels from %d over %d rows from %d",
			ErrInvalidRange, s.ID, s.Count, s.First, s.RowCount, s.FirstRow)
	}

	return nil
}

// SetSegments writes several segments in one request, which is the whole point of it: a
// picture that needs two segments becomes one body, so the controller is never asked to
// show half of it, and the cost is one request rather than one per segment.
//
// This is the call that makes a progress bar possible on a controller whose writes all
// address the same segment. SetRange narrows segment 0 to the run it writes, so a fill
// written and then a remainder written are one segment, not two: the second body takes the
// first one's place, the fill belongs to no segment, and it is never drawn. A 256x64 panel
// did precisely that — it held one segment covering pixels 10158 to 16384 in black, and
// pixels 0 to 10157 were dark because nothing covered them, while the module logged every
// send as taken.
//
// Every segment that carries a span carries all four bounds. What the four mean is the
// device's, and it was measured rather than assumed: on a two-dimensional build `start` and
// `stop` are **columns** and are clamped to the panel's width, `startY` and `stopY` are rows,
// and a `stopY` of zero becomes one row. So a flat pixel index is not a rectangle and cannot
// be sent as one — 10158 to 16384 on a 256x64 panel arrived as a single row of black over the
// whole width, with the green it was meant to sit beside covered by it.
//
// The two shapes are therefore stated apart. A 1D caller leaves RowCount zero and gets the
// flat run a strip draws, with both rows as zero, which is the body a strip has always taken.
// A 2D caller states its rows and gets a rectangle. The four travel together because a body
// that names columns without rows, or rows without columns, is read as something other than
// it means depending on which parser sees it.
//
// A negative span is refused with ErrInvalidRange before anything is sent, and so is a body
// that names one id twice: a controller would apply the second entry, and a caller that
// disagrees with itself has a bug worth seeing.
func (c *Client) SetSegments(ctx context.Context, segments []SegmentWrite) error {
	if len(segments) == 0 {
		return fmt.Errorf("%w: no segments to write", ErrInvalidRange)
	}

	named := make(map[int]struct{}, len(segments))
	body := make([]writeSegment, 0, len(segments))

	for _, segment := range segments {
		if err := segment.validate(); err != nil {
			return err
		}

		if _, twice := named[segment.ID]; twice {
			return fmt.Errorf("%w: segment %d is named twice", ErrInvalidRange, segment.ID)
		}

		named[segment.ID] = struct{}{}

		on := segment.On
		written := writeSegment{ID: segment.ID, On: &on}

		if segment.Count > 0 {
			start, stop := segment.First, segment.First+segment.Count
			firstRow, lastRow := segment.FirstRow, segment.FirstRow+segment.RowCount

			written.Start, written.Stop = &start, &stop
			written.StartY, written.StopY = &firstRow, &lastRow
		}

		if segment.On {
			effect := solidEffect

			written.Colors = [][]int{segment.Colour.channels()}
			written.Effect = &effect
		}

		body = append(body, written)
	}

	return c.do(ctx, http.MethodPost, pathState, writeState{Segments: body}, nil)
}

// RestoreSegment puts one segment back exactly as State reported it: its run, whether it
// is drawn, the colours it was drawn in, its effect and its own name. It is the write
// that gives back a segment this module borrowed to draw on, and it exists because a
// controller remembers a segment's bounds: every frame written into somebody's segment
// has already moved it, and nothing but a write can move it back.
//
// The four bounds travel together when the segment has a run, for the reason SetSegments
// gives: a body that names start and stop alone is read as a flat range and folds the
// segment onto one row of a panel. A segment the controller reported with no run — one
// already deleted — is written back without bounds, which is what leaving it alone means.
//
// The effect's parameters (speed, intensity, the three custom and three check values) and
// its palette are deliberately not written. This package never writes them while it holds
// a segment, because it paints solid colour and solid colour names none of them, so they
// are still the controller's own and writing them back would be a guess at values that
// were never read back after a frame.
//
// A negative id is refused with ErrInvalidRange and nothing is sent.
func (c *Client) RestoreSegment(ctx context.Context, segment Segment) error {
	if segment.ID < 0 {
		return fmt.Errorf("%w: segment id %d", ErrInvalidRange, segment.ID)
	}

	on := segment.On
	written := writeSegment{ID: segment.ID, On: &on}

	if segment.Stop > segment.Start {
		start, stop := segment.Start, segment.Stop
		written.Start, written.Stop = &start, &stop

		// The row bounds only when the controller published them: a 1D controller has no
		// rows, and a stopY of zero written to one would be a claim about geometry it does
		// not have.
		if segment.StopY > segment.StartY {
			firstRow, lastRow := segment.StartY, segment.StopY
			written.StartY, written.StopY = &firstRow, &lastRow
		}
	}

	effect := segment.Effect
	written.Effect = &effect

	if len(segment.Colors) > 0 {
		written.Colors = make([][]int, 0, len(segment.Colors))
		for _, color := range segment.Colors {
			written.Colors = append(written.Colors, color.channels())
		}
	}

	// An empty name is not written: the segment had none, and a name written as an empty
	// string is not the same thing to every firmware.
	if segment.Name != "" {
		naming := segment.Name
		written.Name = &naming
	}

	// The segment's own brightness only when it had one. Zero is WLED's "inherit the
	// strip's brightness", and writing it back as a value would put the segment at no
	// light at all — the one thing a restore must never do.
	if segment.Brightness > 0 {
		level := segment.Brightness
		written.Brightness = &level
	}

	return c.do(ctx, http.MethodPost, pathState, writeState{
		Segments: []writeSegment{written},
	}, nil)
}

// Bounds is a segment's rectangle on a two-dimensional controller: the columns it
// covers and the rows it covers, in WLED's half-open form — Start and StartY are the
// first column and row it covers, Stop and StopY the ones after its last.
//
// All four numbers travel together, always. WLED reads Start and Stop alone as a flat
// range of LED indices, so a rectangle that named its columns and not its rows would
// be folded onto one row of the panel; this type cannot express that mistake.
type Bounds struct {
	// Start is the first column, inclusive.
	Start int
	// Stop is the column after the last one, exclusive.
	Stop int
	// StartY is the first row, inclusive.
	StartY int
	// StopY is the row after the last one, exclusive.
	StopY int
}

// validate reports whether the rectangle covers at least one pixel. A rectangle that
// covers none is refused rather than sent: a controller treats a segment whose stop
// is not past its start as a segment to delete, so an empty rectangle would take the
// segment away instead of drawing nothing.
func (b Bounds) validate() error {
	if b.Start < 0 || b.StartY < 0 || b.Stop <= b.Start || b.StopY <= b.StartY {
		return fmt.Errorf("%w: columns %d-%d and rows %d-%d",
			ErrInvalidRectangle, b.Start, b.Stop, b.StartY, b.StopY)
	}

	return nil
}

// effectRequest is what the options to SetEffect fill in.
type effectRequest struct {
	bounds *Bounds
}

// EffectOption adjusts one SetEffect call.
type EffectOption func(*effectRequest)

// WithEffectBounds states the rectangle a panel segment covers, so the effect runs
// over that rectangle rather than over the whole panel. All four bounds are sent;
// see Bounds for why they cannot be sent in part.
func WithEffectBounds(bounds Bounds) EffectOption {
	return func(e *effectRequest) { e.bounds = &bounds }
}

// Effect is one effect and the parameters it runs with: the id the controller lists
// it under ("fx"), its speed and intensity ("sx", "ix"), the three custom values
// ("c1", "c2", "c3"), the three check values ("o1", "o2", "o3") and its palette
// ("pal").
//
// Every field is sent on every write, zeros included, because a controller applies
// only what a body names and an omitted parameter would leave the last effect's value
// behind. A caller that wants to change one parameter and keep the rest reads the
// segment back with State first — a Segment carries all of them — or leaves the write
// to SetSolid and SetRange, which name no effect parameters at all.
//
// Two effects are worth naming here. Solid is id 0 and is what SetSolid and SetRange
// write. Scrolling Text is id 122 on the firmware this was measured against, and it
// draws the segment's own name — SetSegmentName — with Custom2 as the font size and
// Intensity as the vertical position; the id is not guaranteed across firmware, which
// is what Capabilities exists to check.
type Effect struct {
	// ID is the effect's id in the controller's own list, "fx".
	ID int
	// Speed is the effect's speed, "sx", 0-255.
	Speed int
	// Intensity is the effect's intensity, "ix", 0-255; for the text effect, the
	// vertical position.
	Intensity int
	// Custom1 is the effect's first custom value, "c1", 0-255.
	Custom1 int
	// Custom2 is the effect's second custom value, "c2", 0-255; for the text effect,
	// the font size.
	Custom2 int
	// Custom3 is the effect's third custom value, "c3", 0-255.
	Custom3 int
	// Check1 is the effect's first check value, "o1", 0-255.
	Check1 int
	// Check2 is the effect's second check value, "o2", 0-255.
	Check2 int
	// Check3 is the effect's third check value, "o3", 0-255.
	Check3 int
	// Palette is the palette id it draws with, "pal".
	Palette int
}

// validate reports whether every parameter is one a controller can carry: an id in
// 0-255 and each parameter in 0-255. A number outside that range is a caller's
// arithmetic going wrong, and a controller would cut it down without saying so.
func (e Effect) validate() error {
	for _, parameter := range []struct {
		name  string
		value int
	}{
		{"effect id", e.ID},
		{"speed", e.Speed},
		{"intensity", e.Intensity},
		{"custom 1", e.Custom1},
		{"custom 2", e.Custom2},
		{"custom 3", e.Custom3},
		{"check 1", e.Check1},
		{"check 2", e.Check2},
		{"check 3", e.Check3},
		{"palette", e.Palette},
	} {
		if parameter.value < 0 || parameter.value > 255 {
			return fmt.Errorf("%w: %s is %d, not 0-255", ErrInvalidEffect, parameter.name, parameter.value)
		}
	}

	return nil
}

// SetEffect puts a segment onto one effect with the parameters given. The write
// names the effect, every parameter of it, and — when WithEffectBounds is passed — all
// four bounds of the rectangle it covers. It names no colour, because an effect takes
// its colours from its palette, and it names no strip brightness, so the strip's
// power and brightness are the caller's to set with SetPower and SetBrightness.
//
// The segment itself is switched on, because an effect written to a segment that is
// not drawn is not shown. For text on a panel the order of SetSegmentName and this
// call does not matter: both write the same segment and neither clears the other's
// field.
func (c *Client) SetEffect(ctx context.Context, effect Effect, opts ...EffectOption) error {
	settings := effectRequest{}

	for _, option := range opts {
		if option != nil {
			option(&settings)
		}
	}

	if err := effect.validate(); err != nil {
		return err
	}

	if settings.bounds != nil {
		if err := settings.bounds.validate(); err != nil {
			return err
		}
	}

	on := true

	segment := writeSegment{
		ID:        firstSegment,
		On:        &on,
		Effect:    &effect.ID,
		Speed:     &effect.Speed,
		Intensity: &effect.Intensity,
		Custom1:   &effect.Custom1,
		Custom2:   &effect.Custom2,
		Custom3:   &effect.Custom3,
		Check1:    &effect.Check1,
		Check2:    &effect.Check2,
		Check3:    &effect.Check3,
		Palette:   &effect.Palette,
	}

	if settings.bounds != nil {
		segment.Start = &settings.bounds.Start
		segment.Stop = &settings.bounds.Stop
		segment.StartY = &settings.bounds.StartY
		segment.StopY = &settings.bounds.StopY
	}

	return c.do(ctx, http.MethodPost, pathState, writeState{
		Segments: []writeSegment{segment},
	}, nil)
}

// SetSegmentName gives the segment its own name, which is the string the text effect
// draws and nothing else. It is the only way to put a word on a matrix: the effect
// decides where the word goes, and the name is what it says.
//
// The name is capped at MaxSegmentName characters, which is what a controller stores;
// an empty name or a longer one is refused with ErrInvalidName rather than truncated,
// because half a word on a panel is not a status. A name-only write carries no bounds
// at all, so it leaves the segment's rectangle exactly as it was.
func (c *Client) SetSegmentName(ctx context.Context, name string) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("%w: the name is empty", ErrInvalidName)
	}

	if len(name) > MaxSegmentName {
		return fmt.Errorf("%w: %d characters, at most %d", ErrInvalidName, len(name), MaxSegmentName)
	}

	return c.do(ctx, http.MethodPost, pathState, writeState{
		Segments: []writeSegment{{ID: firstSegment, Name: &name}},
	}, nil)
}

// rectangleRequest is what the options to SetRectangle fill in.
type rectangleRequest struct {
	grouping  *int
	transpose *int
	expand1D  *int
}

// RectangleOption adjusts one SetRectangle call.
type RectangleOption func(*rectangleRequest)

// WithRectangleGrouping sets the segment's grouping, "grp": how many LEDs each colour
// covers. One is every LED its own colour; two makes each colour a pair, which is how
// a coarse bar is drawn on a panel too small to need every pixel.
func WithRectangleGrouping(leds int) RectangleOption {
	return func(r *rectangleRequest) { r.grouping = &leds }
}

// WithRectangleTranspose sets the segment's transpose code, "tp": 0 leaves the effect
// as it is, and the higher codes rotate it through WLED's own quarter turns. A code
// outside 0-255 is refused with ErrInvalidRectangle.
func WithRectangleTranspose(code int) RectangleOption {
	return func(r *rectangleRequest) { r.transpose = &code }
}

// WithRectangleExpand1D sets how a one-dimensional effect is laid onto the panel,
// "m12": one of ExpandPixels, ExpandBar, ExpandArc or ExpandCorner. Anything else is
// refused with ErrInvalidRectangle, because a code a controller does not know leaves
// the panel showing something nobody chose.
func WithRectangleExpand1D(mode int) RectangleOption {
	return func(r *rectangleRequest) { r.expand1D = &mode }
}

// SetRectangle paints one colour into an explicit rectangle of a panel: columns
// Start..Stop and rows StartY..StopY, all four sent, always. It is the geometry tier —
// a band of rows is a bar, and resizing the rectangle is how a progress bar grows
// without painting a pixel.
//
// A rectangle that covers nothing, or is inverted in either axis, is refused with
// ErrInvalidRectangle and nothing is sent. A rectangle wider than the panel is not
// refused here: this package does not know the matrix unless the caller supplies it,
// and a controller clips a segment to its own LEDs.
func (c *Client) SetRectangle(ctx context.Context, bounds Bounds, color RGB, opts ...RectangleOption) error {
	settings := rectangleRequest{}

	for _, option := range opts {
		if option != nil {
			option(&settings)
		}
	}

	if err := bounds.validate(); err != nil {
		return err
	}

	for _, geometry := range []struct {
		name  string
		value *int
	}{
		{"grouping", settings.grouping},
		{"transpose", settings.transpose},
		{"expand 1D", settings.expand1D},
	} {
		if geometry.value != nil && (*geometry.value < 0 || *geometry.value > 255) {
			return fmt.Errorf("%w: %s is %d, not 0-255", ErrInvalidRectangle, geometry.name, *geometry.value)
		}
	}

	on := true
	effect := solidEffect

	return c.do(ctx, http.MethodPost, pathState, writeState{
		Segments: []writeSegment{{
			ID:        firstSegment,
			Start:     &bounds.Start,
			Stop:      &bounds.Stop,
			StartY:    &bounds.StartY,
			StopY:     &bounds.StopY,
			On:        &on,
			Colors:    [][]int{color.channels()},
			Effect:    &effect,
			Grouping:  settings.grouping,
			Transpose: settings.transpose,
			Expand1D:  settings.expand1D,
		}},
	}, nil)
}

// paintRequest is what the options to Paint fill in.
type paintRequest struct {
	budget int
}

// PaintOption adjusts one Paint call.
type PaintOption func(*paintRequest)

// WithPaintByteBudget sets the largest request body Paint may send, in bytes. The
// default is DefaultPaintByteBudget, 8 KiB. A budget too small to carry one LED is
// refused with ErrPaintBudget rather than exceeded.
func WithPaintByteBudget(budget int) PaintOption {
	return func(p *paintRequest) { p.budget = budget }
}

// Paint colours individual LEDs: colors[0] is LED number first, colors[1] the one
// after it, and so on, which is WLED's segment property i.
//
// It is the floor under the other two tiers. Effects can animate and can carry text,
// rectangles can draw a bar, but a controller that cannot do either can still be
// given one colour per LED, so this is the call that makes "cannot be shown at all"
// impossible. Everything it costs is in the package comment: the colours are not
// saved across a reboot, they freeze the segment's effect, and the segment must
// already be on and at its brightness — a body that switches a segment on and sets
// its LEDs together does not work, so this one sends the LEDs alone.
//
// A frame longer than the byte budget is split into several requests, each starting
// at the LED its first colour belongs to, so the order of the colours is preserved
// across the whole frame and every body stays inside the budget. Each request is a
// separate call to the controller, so another writer can land between two of them;
// WLED has no transaction, and a caller that needs a frame to appear at once keeps it
// within one chunk.
//
// An empty slice is refused with ErrEmptyPaint and no request is sent, and a first
// below zero with ErrInvalidRange. first is the LED within the segment, which for
// segment 0 — the only one this package writes — is the LED number on the device; on
// a panel that is y*width + x.
func (c *Client) Paint(ctx context.Context, first int, colors []RGB, opts ...PaintOption) error {
	settings := paintRequest{budget: DefaultPaintByteBudget}

	for _, option := range opts {
		if option != nil {
			option(&settings)
		}
	}

	if len(colors) == 0 {
		return fmt.Errorf("%w: the slice is empty", ErrEmptyPaint)
	}

	if first < 0 {
		return fmt.Errorf("%w: first led %d", ErrInvalidRange, first)
	}

	remaining := colors
	index := first

	for len(remaining) > 0 {
		fits := paintColoursThatFit(index, settings.budget)
		if fits < 1 {
			return fmt.Errorf("%w: %d bytes cannot carry the %d a body needs",
				ErrPaintBudget, settings.budget, paintBodyBytes(index, 1))
		}

		count := min(fits, len(remaining))

		segment := writeSegment{ID: firstSegment, Pixels: paintPixels(index, remaining[:count])}
		if err := c.do(ctx, http.MethodPost, pathState, writeState{
			Segments: []writeSegment{segment},
		}, nil); err != nil {
			return err
		}

		remaining = remaining[count:]
		index += count
	}

	return nil
}

// paintEntryBytes is what one LED costs inside a paint body when it is written as the
// hex colour WLED reads: a comma, a quote, six digits and a closing quote. Every
// colour costs the same, which is what makes the chunk arithmetic exact rather than
// estimated.
const paintEntryBytes = len(`,"FFFFFF"`)

// paintEnvelope is what a paint body spends on everything but its colours: the
// wrapper, the segment id, the LED list, and the single index entry that says where
// the list starts, counted with a one-digit index.
var paintEnvelope = func() int {
	raw, err := json.Marshal(writeState{
		Segments: []writeSegment{{ID: firstSegment, Pixels: []any{0}}},
	})
	if err != nil {
		// The body is a struct of integers and strings; this cannot happen, and a
		// client that silently guessed at its own encoding would be worse than one
		// that stops here.
		panic("wled: the per-led body does not encode: " + err.Error())
	}

	return len(raw)
}()

// paintBodyBytes is how long the body for a chunk of colours would be if it started at
// the LED index and carried that many colours.
func paintBodyBytes(index, colors int) int {
	return paintEnvelope + len(strconv.Itoa(index)) - 1 + colors*paintEntryBytes
}

// paintColoursThatFit is how many colours a chunk starting at the LED index can carry
// inside a budget of that many bytes, or 0 when not even one fits. The index's own
// digits are counted, so a chunk late in a 16,384-LED frame is measured against the
// five-digit index it will actually be written with.
func paintColoursThatFit(index, budget int) int {
	room := budget - paintEnvelope - (len(strconv.Itoa(index)) - 1)
	if room < paintEntryBytes {
		return 0
	}

	return room / paintEntryBytes
}

// paintPixels is the segment's i property for one chunk: the index its first colour
// belongs to, then each colour as six hex digits. Naming the index on every chunk,
// including the first, is what lets a frame that was split be reassembled in order.
func paintPixels(index int, colors []RGB) []any {
	pixels := make([]any, 0, len(colors)+1)
	pixels = append(pixels, index)

	for _, color := range colors {
		pixels = append(pixels, color.hex())
	}

	return pixels
}

// hex is the colour as WLED reads one inside a segment's i list: six uppercase hex
// digits, no leading hash. It is the same shape parseHexColor accepts back.
func (c RGB) hex() string { return fmt.Sprintf("%02X%02X%02X", c.R, c.G, c.B) }

// EffectInfo is one effect a controller offers: the id to write, the name to show in a
// list, and the effect's own flag string when the controller publishes one.
type EffectInfo struct {
	// ID is the effect's id, which is both its position in the controller's list and
	// the number SetEffect writes as fx.
	ID int
	// Name is the effect's name as the controller reports it, such as "Solid" or
	// "Scrolling Text".
	Name string
	// Flags is the effect's line of the controller's fxdata, when the controller
	// published one: a short string of characters saying which of the effect's
	// controls it actually uses. It is empty when the answer carried no fxdata, which
	// is a controller whose effects this package cannot describe beyond their names.
	Flags string
}

// Palette is one palette a controller offers.
type Palette struct {
	// ID is the palette's id, which is the number SetEffect writes as pal.
	ID int
	// Name is the palette's name as the controller reports it. A leading asterisk is
	// the controller's own marker for a palette it generates rather than one it
	// stores.
	Name string
}

// Capabilities is what a controller says it can show. It is read from the controller
// rather than assumed, because effect ids are per-firmware: the same number is a
// different effect on a different version.
type Capabilities struct {
	// Effects is every effect the controller lists, in its own order.
	Effects []EffectInfo
	// Palettes is every palette it lists, in its own order.
	Palettes []Palette
}

// Capabilities reads the controller's catalogue of effects and palettes from GET
// /json. It is what a caller builds a choice from, and what says whether the effect it
// means to use exists on this firmware at all.
//
// The flag strings come from the same document's fxdata when it has one; when it does
// not, every EffectInfo reads with empty Flags and its own name, which is enough to
// build a list and not enough to know which of an effect's controls matter. A document
// that pairs them differently is not guessed at: an effect whose line is missing reads
// empty rather than taking its neighbour's.
func (c *Client) Capabilities(ctx context.Context) (Capabilities, error) {
	var answer struct {
		Effects  []string        `json:"effects"`
		Palettes []string        `json:"palettes"`
		FXData   json.RawMessage `json:"fxdata"`
	}

	if err := c.do(ctx, http.MethodGet, pathJSON, nil, &answer); err != nil {
		return Capabilities{}, err
	}

	flags := decodeFXData(answer.FXData)

	capabilities := Capabilities{
		Effects:  make([]EffectInfo, 0, len(answer.Effects)),
		Palettes: make([]Palette, 0, len(answer.Palettes)),
	}

	for id, name := range answer.Effects {
		effect := EffectInfo{ID: id, Name: name}
		if id < len(flags) {
			effect.Flags = flags[id]
		}

		capabilities.Effects = append(capabilities.Effects, effect)
	}

	for id, name := range answer.Palettes {
		capabilities.Palettes = append(capabilities.Palettes, Palette{ID: id, Name: name})
	}

	return capabilities, nil
}

// decodeFXData reads the fxdata that may travel with the effect list. A controller
// publishes it as one newline-separated string; a firmware that publishes an array of
// lines instead is read too. Anything else, or nothing at all, is no flags rather than
// a failed call: the effect list is still worth having.
func decodeFXData(raw json.RawMessage) []string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return nil
	}

	var text string
	if err := json.Unmarshal(trimmed, &text); err == nil {
		if text == "" {
			return nil
		}

		lines := strings.Split(text, "\n")
		for index, line := range lines {
			lines[index] = strings.TrimSuffix(line, "\r")
		}

		return lines
	}

	var lines []string
	if err := json.Unmarshal(trimmed, &lines); err == nil {
		return lines
	}

	return nil
}

// writeState is a POST /json/state body. Every field is a pointer so that "not
// mentioned in this write" is distinct from "set to zero": WLED changes only what
// the body names, and a brightness of 0 that arrived by accident would darken a
// strip.
type writeState struct {
	On         *bool          `json:"on,omitempty"`
	Brightness *int           `json:"bri,omitempty"`
	Transition *int           `json:"transition,omitempty"`
	Segments   []writeSegment `json:"seg,omitempty"`
}

// writeSegment is one segment of that body, in the order the WLED documentation
// writes it: which segment, which LEDs, whether it is drawn, its colour, its effect
// and that effect's parameters, then the geometry controls, then the name and the
// per-LED list.
//
// Brightness is the segment's own, "bri" inside the segment rather than the strip's, and
// only RestoreSegment writes it. It is here so that a segment which carried a brightness
// of its own before this package borrowed it can be given that brightness back.
//
// The row bounds are here because a 2D controller expresses a segment as a rectangle
// in both axes, and they must travel with the column bounds or not at all. A write
// that carries "start" and "stop" without "startY" and "stopY" is read as a flat range
// of LED indices: the controller recomputes the segment's rows from those two numbers
// and leaves the segment one row high, whatever rows it covered before. A write that
// carries no bounds at all — a name, a per-LED list — does not touch the geometry.
type writeSegment struct {
	ID         int     `json:"id"`
	Start      *int    `json:"start,omitempty"`
	Stop       *int    `json:"stop,omitempty"`
	StartY     *int    `json:"startY,omitempty"`
	StopY      *int    `json:"stopY,omitempty"`
	On         *bool   `json:"on,omitempty"`
	Colors     [][]int `json:"col,omitempty"`
	Brightness *int    `json:"bri,omitempty"`
	Effect     *int    `json:"fx,omitempty"`
	Speed      *int    `json:"sx,omitempty"`
	Intensity  *int    `json:"ix,omitempty"`
	Custom1    *int    `json:"c1,omitempty"`
	Custom2    *int    `json:"c2,omitempty"`
	Custom3    *int    `json:"c3,omitempty"`
	Check1     *int    `json:"o1,omitempty"`
	Check2     *int    `json:"o2,omitempty"`
	Check3     *int    `json:"o3,omitempty"`
	Palette    *int    `json:"pal,omitempty"`
	Grouping   *int    `json:"grp,omitempty"`
	Transpose  *int    `json:"tp,omitempty"`
	Expand1D   *int    `json:"m12,omitempty"`
	Name       *string `json:"n,omitempty"`
	Pixels     []any   `json:"i,omitempty"`
}

// do performs one request and is the only place this package talks to a
// controller.
func (c *Client) do(ctx context.Context, method, path string, payload, out any) error {
	// One call at a time per controller, as WLED asks. The lock is held across the
	// request on purpose: releasing it between encode and send would let two
	// writes interleave at the device, which is the thing being avoided.
	c.mu.Lock()
	defer c.mu.Unlock()

	endpoint := c.base + path

	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("wled: encode the request to %s: %w", endpoint, err)
		}

		body = bytes.NewReader(encoded)
	}

	request, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return fmt.Errorf("wled: build the request to %s: %w", endpoint, err)
	}

	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}

	answer, err := c.http.Do(request)
	if err != nil {
		return c.transportError(endpoint, err)
	}

	defer func() { _ = answer.Body.Close() }()

	// A body that never finishes is a timeout too: the http.Client deadline covers
	// reading it, and that error arrives here rather than from Do.
	raw, err := io.ReadAll(io.LimitReader(answer.Body, maxBody))
	if err != nil {
		return c.transportError(endpoint, err)
	}

	failing := answer.StatusCode < 200 || answer.StatusCode > 299

	// WLED's own refusal, wherever it arrives. Some checks answer 400, others answer
	// 200 with the error in the body, so the body decides and the status is recorded
	// beside it.
	if code, refused := deviceRefusal(raw); refused {
		status := 0
		if failing {
			status = answer.StatusCode
		}

		return &DeviceError{
			Code:       code,
			StatusCode: status,
			URL:        endpoint,
			Body:       snippet(raw),
		}
	}

	if failing {
		return &StatusError{StatusCode: answer.StatusCode, URL: endpoint, Body: snippet(raw)}
	}

	if out == nil {
		return nil
	}

	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("wled: decode the answer from %s: %w", endpoint, err)
	}

	return nil
}

// transportError turns a transport failure into the typed error a caller can act
// on, keeping the transport's own error behind it.
func (c *Client) transportError(endpoint string, cause error) error {
	var netErr net.Error

	timedOut := errors.Is(cause, context.DeadlineExceeded) ||
		errors.Is(cause, os.ErrDeadlineExceeded) ||
		(errors.As(cause, &netErr) && netErr.Timeout())

	if timedOut {
		return &TimeoutError{URL: endpoint, Err: cause}
	}

	return &UnreachableError{URL: endpoint, Err: cause}
}

// deviceRefusal reads WLED's {"error":n} out of an answer, if it is there.
func deviceRefusal(raw []byte) (int, bool) {
	var refusal struct {
		Error *int `json:"error"`
	}

	if err := json.Unmarshal(raw, &refusal); err != nil || refusal.Error == nil {
		return 0, false
	}

	return *refusal.Error, true
}

// snippet is the start of an answer, short enough for a log line and with its
// whitespace collapsed so a pretty-printed error does not become five lines.
func snippet(raw []byte) string {
	const limit = 200

	text := strings.Join(strings.Fields(string(raw)), " ")
	if text == "" {
		return "(empty)"
	}

	if len(text) > limit {
		return text[:limit] + "..."
	}

	return text
}

// decodeColors reads a segment's "col". WLED accepts both shapes on the wire — a
// list of RGB arrays, [[255,160,0]], and a list of hex strings, ["FFA000"] — and
// answers with whichever the caller wrote last, so both are read here.
func decodeColors(raw json.RawMessage) ([]RGB, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return nil, nil
	}

	var entries []json.RawMessage
	if err := json.Unmarshal(trimmed, &entries); err != nil {
		return nil, fmt.Errorf("not a list of colours: %w", err)
	}

	colors := make([]RGB, 0, len(entries))

	for index, entry := range entries {
		if quoted := bytes.TrimSpace(entry); len(quoted) > 0 && quoted[0] == '"' {
			var text string
			if err := json.Unmarshal(quoted, &text); err != nil {
				return nil, fmt.Errorf("colour %d: %w", index, err)
			}

			color, err := parseHexColor(text)
			if err != nil {
				return nil, fmt.Errorf("colour %d: %w", index, err)
			}

			colors = append(colors, color)

			continue
		}

		var channels []int
		if err := json.Unmarshal(entry, &channels); err != nil {
			return nil, fmt.Errorf("colour %d: %w", index, err)
		}

		if len(channels) < 3 {
			return nil, fmt.Errorf("colour %d carries %d channels, want 3", index, len(channels))
		}

		colors = append(colors, RGB{
			R: channelByte(channels[0]),
			G: channelByte(channels[1]),
			B: channelByte(channels[2]),
		})
	}

	return colors, nil
}

// parseHexColor reads the hex form WLED also accepts, "RRGGBB" or "WWRRGGBB" with
// or without a leading hash. The white channel of the longer form is dropped: this
// package has no RGBW strip to use it on.
func parseHexColor(text string) (RGB, error) {
	digits := strings.TrimPrefix(strings.TrimSpace(text), "#")
	if len(digits) == 8 {
		digits = digits[2:]
	}

	if len(digits) != 6 {
		return RGB{}, fmt.Errorf("%q is not a hex colour", text)
	}

	value, err := strconv.ParseUint(digits, 16, 32)
	if err != nil {
		return RGB{}, fmt.Errorf("%q is not a hex colour: %w", text, err)
	}

	return RGB{
		R: uint8(value >> 16 & 0xff),
		G: uint8(value >> 8 & 0xff),
		B: uint8(value & 0xff),
	}, nil
}

// channelByte holds one channel inside what a byte can carry, because WLED
// documents 0-255 and nothing promises it never sends more.
func channelByte(value int) uint8 {
	switch {
	case value < 0:
		return 0
	case value > 255:
		return 255
	default:
		return uint8(value)
	}
}
