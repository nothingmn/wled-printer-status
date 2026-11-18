package main

import (
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
