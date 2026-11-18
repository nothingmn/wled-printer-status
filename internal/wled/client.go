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
// bounds beside its LED bounds. Nothing here draws. There is no rectangle helper,
// no mapping from a position to a pixel, and SetRange stays what it is — a
// contiguous run of LEDs in one dimension.
//
// # What this package does not do
//
// It never writes {"live":true}, which hijacks the strip into realtime mode and
// blanks it, and it never uses per-LED control (the segment property i), which
// is non-persistent and freezes the effect. Both are WLED features that exist
// and that this module has no use for.
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

	return c.do(ctx, http.MethodPost, pathState, writeState{
		Brightness: settings.brightness,
		Transition: settings.transition,
		Segments:   []writeSegment{segment},
	}, nil)
}

// rangeRequest is what the options to SetRange fill in.
type rangeRequest struct {
	ledCount int
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

	on := true
	effect := solidEffect

	return c.do(ctx, http.MethodPost, pathState, writeState{
		Segments: []writeSegment{{
			ID:     firstSegment,
			Start:  &first,
			Stop:   &stop,
			On:     &on,
			Colors: [][]int{color.channels()},
			Effect: &effect,
		}},
	}, nil)
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
// writes it: which segment, which LEDs, whether it is drawn, its colour, its effect.
//
// The row bounds are here because a 2D controller expresses a segment as a rectangle
// in both axes, and the two are omitted when unset: nothing in this package draws a
// rectangle, so a write sent from here carries LED bounds alone and a controller with
// rows keeps the rows it already had.
type writeSegment struct {
	ID     int     `json:"id"`
	Start  *int    `json:"start,omitempty"`
	Stop   *int    `json:"stop,omitempty"`
	StartY *int    `json:"startY,omitempty"`
	StopY  *int    `json:"stopY,omitempty"`
	On     *bool   `json:"on,omitempty"`
	Colors [][]int `json:"col,omitempty"`
	Effect *int    `json:"fx,omitempty"`
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
