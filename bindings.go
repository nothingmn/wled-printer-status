package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/nothingmn/wled-printer-status/internal/wled"
)

// binding is one printer shown on one segment of one controller.
//
// It is the unit the operator's topology is made of. His decision of 2026-11-18 replaced "one
// controller per printer" with something smaller: a binding is (printer, controller, segment),
// a printer appears in at most one binding, a segment is driven by at most one binding, and a
// controller may carry as many bindings as it has segments it is willing to give up. A
// controller that holds no named segments carries exactly one, because the whole light is then
// one display.
//
// That decision superseded an earlier one — one controller per printer, at most — and the code
// here says so rather than quietly growing a second rule beside the first: the controller is no
// longer the unit, the segment is.
type binding struct {
	// DeviceID is the host's id for the printer, which survives a rename. It is resolved from
	// the `printer` setting before a set is built, so every binding here names one machine and
	// not a string two machines might answer to.
	DeviceID string

	// Address is the controller's base URL, as the operator typed it: http://10.0.0.136. Two
	// spellings of one address are one controller — see controllerKey — because two bindings on
	// one controller are the case the rules below exist for.
	Address string

	// Segment is the segment this binding drives, by the operator's own numbering as the
	// controller reports it. -1 is the whole controller, which is what this module drove before
	// a segment could be named, and it is a number rather than a pointer because every binding
	// names something and "the whole controller" is a thing it can name.
	Segment int

	// Brightness is written once at start, 0-255, and only on a whole-controller binding: a
	// brightness is controller-wide and would move every segment somebody else is using. On a
	// borrowed binding it is refused rather than ignored — see check.
	Brightness int
}

// bindings is every binding one watch runs, in the order the operator wrote them.
type bindings []binding

// check holds a set to the operator's rules and refuses one this module cannot run.
//
// Every rule here is a refusal and not a warning, because each names a way two bindings would
// otherwise undo each other silently, at the rate of a tick:
//
//   - one printer, one binding. Two bindings on one printer would each read the same job and
//     draw it, and whichever wrote last would be the answer a person sees — with the other's
//     animation cut short for reasons nothing could explain.
//   - one segment, one binding. WLED has no ownership: the only exclusivity there is, is the
//     one this check makes. Two bindings on one segment overwrite each other on every frame,
//     and neither log line would say so.
//   - -1 claims every segment on its controller, so it collides with any other binding there.
//     A controller somebody also drives is only ever borrowed, one segment at a time, and a
//     whole-controller body (which carries every segment it draws) would take the rest.
//   - a brightness on a borrowed binding is refused rather than ignored: a field nothing reads
//     is a field the next change reads, and a writer that carried one could dim a desk light to
//     the value meant for a status strip.
//
// One rule is deliberately not here, because it needs a fact only the controller has: whether
// the segments two bindings name really exist. The borrow reads /json/state at start and refuses
// a segment the device does not report (see writer.borrow), so a set naming segments a controller
// does not hold writes nothing and says why — which is the same answer, one read later.
func (bs bindings) check() error {
	if len(bs) == 0 {
		return fmt.Errorf("no binding: this module has nothing to watch")
	}

	printers := make(map[string]int, len(bs))
	claims := make(map[string]*claim, len(bs))

	for i, b := range bs {
		where := fmt.Sprintf("binding %d", i+1)

		if strings.TrimSpace(b.DeviceID) == "" {
			return fmt.Errorf("%s names no printer", where)
		}

		key, err := controllerKey(b.Address)
		if err != nil {
			return fmt.Errorf("%s: %w", where, err)
		}

		if b.Segment < -1 {
			return fmt.Errorf(
				"%s names segment %d: name one segment by its number, or -1 for the whole controller",
				where, b.Segment)
		}

		if b.Segment == -1 && (b.Brightness < 0 || b.Brightness > 255) {
			return fmt.Errorf(
				"%s drives the whole of %s at brightness %d, and a WLED controller takes 0-255",
				where, b.Address, b.Brightness)
		}

		if b.Segment >= 0 && b.Brightness != 0 {
			return fmt.Errorf(
				"%s borrows segment %d of %s and carries a brightness of %d: a brightness is controller-wide and this binding must not write one",
				where, b.Segment, b.Address, b.Brightness)
		}

		if earlier, ok := printers[b.DeviceID]; ok {
			return fmt.Errorf(
				"%s watches printer %s, which binding %d already watches: one printer is shown by one binding",
				where, b.DeviceID, earlier)
		}

		held := claims[key]
		if held == nil {
			held = &claim{segments: make(map[int]int, 1)}
			claims[key] = held
		}

		switch {
		case b.Segment == -1 && held.whole != 0:
			return fmt.Errorf(
				"%s drives the whole of %s, which binding %d already drives",
				where, b.Address, held.whole)
		case b.Segment == -1 && len(held.segments) > 0:
			return fmt.Errorf(
				"%s drives the whole of %s, and another binding drives a segment of it: a whole controller leaves no segment for anything else",
				where, b.Address)
		case b.Segment >= 0 && held.whole != 0:
			return fmt.Errorf(
				"%s drives segment %d of %s, which binding %d drives whole: a whole controller leaves no segment for anything else",
				where, b.Segment, b.Address, held.whole)
		case b.Segment >= 0:
			if earlier, ok := held.segments[b.Segment]; ok {
				return fmt.Errorf(
					"%s drives segment %d of %s, which binding %d already drives: one segment is shown by one binding",
					where, b.Segment, b.Address, earlier)
			}

			held.segments[b.Segment] = i + 1
		}

		if b.Segment == -1 {
			held.whole = i + 1
		}

		printers[b.DeviceID] = i + 1
	}

	return nil
}

// claim is what one controller has already been asked for, by the binding numbers that asked
// for it. It exists so that the rules are written once per controller and a message can name
// the binding that got there first.
type claim struct {
	// whole is the binding driving the whole controller, or zero for none.
	whole int

	// segments is each named segment and the binding driving it.
	segments map[int]int
}

// controllerKey is one controller's identity for those rules, so that two spellings of one
// address are understood as one controller: the scheme and host, lowercased, without a path or
// a trailing slash.
//
// The address is held to wled.New's own rules rather than to a second copy of them, because that
// is the check the client makes at its first call. An address this module cannot build a client
// for is a configuration mistake, and it is worth naming on the instance rather than as a light
// that never changes.
func controllerKey(address string) (string, error) {
	base := strings.TrimRight(strings.TrimSpace(address), "/")

	if _, err := wled.New(base); err != nil {
		return "", err
	}

	parsed, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("the controller address %q is not a URL: %w", address, err)
	}

	return strings.ToLower(parsed.Scheme + "://" + parsed.Host), nil
}

// controller is one WLED box and the bindings that drive it, in the order they were written.
//
// One entry here becomes one client and one write gate, because a controller is one small web
// server: two bindings on it take turns at the door rather than being given a client each.
type controller struct {
	// Address is the controller as the operator first wrote it, which is what a log line says.
	Address string

	// Key is controllerKey(Address), so two entries cannot describe one controller twice.
	Key string

	// Bindings is every binding on this controller, in the operator's order.
	Bindings bindings
}

// controllers groups a checked set by controller, keeping the order the operator wrote.
//
// It is the shape a watch is built from: one client per entry, one source and one writer per
// binding. The grouping is by key rather than by string so that http://10.0.0.136 and
// http://10.0.0.136/ are one controller, which is the pair a person actually types.
func (bs bindings) controllers() ([]controller, error) {
	if err := bs.check(); err != nil {
		return nil, err
	}

	var order []controller

	at := make(map[string]int, len(bs))

	for _, b := range bs {
		key, err := controllerKey(b.Address)
		if err != nil {
			return nil, err
		}

		if i, ok := at[key]; ok {
			order[i].Bindings = append(order[i].Bindings, b)

			continue
		}

		at[key] = len(order)
		order = append(order, controller{Address: b.Address, Key: key, Bindings: bindings{b}})
	}

	return order, nil
}

// find is the binding one printer is shown by, and false for a printer this module does not
// drive. A printer appears in at most one binding, so the device id is the whole of a
// binding's name: it survives a rename of the machine, it survives the controller being
// swapped for another, and it is the one thing about a binding the host minted rather than the
// operator.
func (bs bindings) find(id string) (binding, bool) {
	for _, b := range bs {
		if b.DeviceID == id {
			return b, true
		}
	}

	return binding{}, false
}

// without is the set with one binding taken out, in the operator's order. A set that does not
// hold the id is returned unchanged, and false says so.
func (bs bindings) without(id string) (bindings, bool) {
	kept := make(bindings, 0, len(bs))

	for _, b := range bs {
		if b.DeviceID != id {
			kept = append(kept, b)
		}
	}

	if len(kept) == len(bs) {
		return bs, false
	}

	return kept, true
}

// with is the set with one binding put where the one it replaces was, or at the end when the
// set does not hold it yet. Keeping the position is what stops an edit from moving a line on
// the list a person is reading.
func (bs bindings) with(b binding) bindings {
	for i := range bs {
		if bs[i].DeviceID == b.DeviceID {
			next := append(bindings(nil), bs...)
			next[i] = b

			return next
		}
	}

	return append(append(bindings(nil), bs...), b)
}

// bindingSetting is one entry of the `bindings` setting: the shape an operator writes in the
// one JSON field the manifest can offer.
//
// It exists because a manifest declares nine value shapes — string, int, number, bool,
// duration, url, secret, list, object — and the settings form draws the last two as a block of
// JSON in a text area. A list of bindings is therefore the most a setting can be, and this is
// what one line of it looks like:
//
//	[{"printer": "Hermes MK4", "controller": "http://10.0.0.136"},
//	 {"printer": "Mini",       "controller": "http://10.0.0.136", "segment": 3}]
//
// Segment and brightness are pointers so that a line that leaves them out is not read as zero.
// Zero is a real answer to both — segment 0 is the first segment a controller holds, and a
// brightness of 0 is a strip switched off — so an absent one has to be told apart from a
// written one, and it is the difference between driving the whole controller and borrowing its
// first segment.
type bindingSetting struct {
	// Printer is the printer to watch, by the host's device id or by the name an operator gave
	// it. The id is the better answer: a name is not unique until the host says it is.
	Printer string `json:"printer"`

	// Controller is the WLED controller to drive, as its base URL: http://10.0.0.136, no path.
	Controller string `json:"controller"`

	// Segment is the one segment to drive on a controller that carries several. Absent or -1
	// drives the whole controller, which is what this module did before a segment could be
	// named.
	Segment *int `json:"segment"`

	// Brightness is what is written once at start, 0-255, and only on a whole-controller
	// binding: a brightness is controller-wide and would move every segment somebody else is
	// using. Absent on a whole-controller binding means defaultBrightness; on a borrowed
	// segment it must be absent or zero, and check refuses anything else.
	Brightness *int `json:"brightness"`
}

// segment is the segment this entry drives, with "the whole controller" written as -1.
func (s bindingSetting) segment() int {
	if s.Segment == nil {
		return defaultSegment
	}

	return *s.Segment
}

// brightness is the brightness this entry asks for, with the module's own default on a whole
// controller and nothing at all on a borrowed segment.
func (s bindingSetting) brightness() int {
	if s.Brightness != nil {
		return *s.Brightness
	}

	if s.segment() < 0 {
		return defaultBrightness
	}

	return 0
}

// parseBindingEntries reads the `bindings` setting into the entries it holds.
//
// The list arrives as raw JSON because the settings payload is decoded strictly, and a strict
// decode of a slice cannot say which entry a misspelt key was in: "json: unknown field
// \"controler\"" leaves a person counting entries in a text area. Decoding one entry at a time
// is what makes the refusal read "bindings[2]: json: unknown field \"controler\"" — the place
// in the field as well as the field.
func parseBindingEntries(entries []json.RawMessage) ([]bindingSetting, error) {
	parsed := make([]bindingSetting, 0, len(entries))

	for i, raw := range entries {
		var one bindingSetting

		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()

		if err := decoder.Decode(&one); err != nil {
			return nil, fmt.Errorf("bindings[%d]: %w: every line carries printer and controller, and may carry segment and brightness", i, err)
		}

		parsed = append(parsed, one)
	}

	return parsed, nil
}

// bindingsFrom is the set the `bindings` setting describes, with every printer resolved to the
// one machine the host holds and every default the manifest publishes applied.
//
// Each refusal names the entry it is about, because the operator wrote a list and is reading a
// one-line message on the instance: "bindings[1] names no printer" is something to act on, and
// "no printer" is not.
func bindingsFrom(ctx context.Context, fleet devices, entries []json.RawMessage) (bindings, error) {
	parsed, err := parseBindingEntries(entries)
	if err != nil {
		return nil, err
	}

	set := make(bindings, 0, len(parsed))

	for i, one := range parsed {
		if strings.TrimSpace(one.Printer) == "" {
			return nil, fmt.Errorf("bindings[%d] names no printer: write the printer's device id, or the name an operator gave it", i)
		}

		deviceID, err := resolvePrinter(ctx, fleet, one.Printer)
		if err != nil {
			return nil, fmt.Errorf("bindings[%d]: %w", i, err)
		}

		set = append(set, binding{
			DeviceID:   deviceID,
			Address:    one.Controller,
			Segment:    one.segment(),
			Brightness: one.brightness(),
		})
	}

	if err := set.check(); err != nil {
		return nil, err
	}

	return set, nil
}

// wantedBindings is the set one settings block asks for.
//
// `bindings` wins over the four settings that came before it, because the manifest still
// declares them and an install that has been running for a week carries them: an operator who
// writes a list gets the list, and the single printer the old keys name is not silently added
// to it or silently left driving. That is said here, in the manifest beside the setting, and in
// the answer the editor's routes give, rather than left for somebody to work out from two
// lights on one strip.
func wantedBindings(ctx context.Context, fleet devices, sent Settings) (bindings, error) {
	if len(sent.Bindings) > 0 {
		return bindingsFrom(ctx, fleet, sent.Bindings)
	}

	if strings.TrimSpace(sent.ControllerAddress) == "" {
		return nil, fmt.Errorf(
			"this instance drives nothing: write the bindings setting, which carries one line per printer, or name one controller in controller_address")
	}

	// The client is built now and contacts nothing until the writer asks it to. wled.New
	// refuses a bad address here rather than at the first call.
	if _, err := wled.New(sent.ControllerAddress); err != nil {
		return nil, err
	}

	deviceID, err := resolvePrinter(ctx, fleet, sent.Printer)
	if err != nil {
		return nil, err
	}

	one := binding{
		DeviceID:   deviceID,
		Address:    sent.ControllerAddress,
		Segment:    sent.Segment,
		Brightness: sent.Brightness,
	}

	// A borrowed binding carries no brightness, whatever the setting says: its default is 128
	// and a brightness is controller-wide. The writer for this mode has no brightness to write
	// (see newSegmentWriter), and the set says so rather than carrying a number nothing reads —
	// bindings.check refuses one.
	if one.Segment >= 0 {
		one.Brightness = 0
	}

	set := bindings{one}

	if err := set.check(); err != nil {
		return nil, err
	}

	return set, nil
}
