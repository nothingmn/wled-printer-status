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
// controller per printer" with something smaller: a binding is (printer, controller, segment), a
// segment is driven by at most one binding, and a controller may carry as many bindings as it has
// segments it is willing to give up. A controller that holds no named segments carries exactly
// one, because the whole light is then one display.
//
// That decision superseded an earlier one — one controller per printer, at most — and the code
// here says so rather than quietly growing a second rule beside the first: the controller is no
// longer the unit, the segment is.
//
// docs/product-requirements.md §2.2 adds the other half, and it is the reason a binding needs a
// name of its own: "A printer may sit on several lights — multi-room status — at the cost of a
// second steady stream", and a segment still carries one printer. So the same printer may appear
// in two bindings while no segment may, which is what makes "the printer's device id" stop being a
// binding's identity.
type binding struct {
	// ID is this binding's name on the routes and the key of the layout table. It is the host's
	// device id for the printer when this is the only binding carrying that printer, and
	// `<device id>@<light>:<segment>` when the printer sits on more than one — see qualifiedID.
	//
	// It is the one field here the host does not mint: identified names a set before it runs, and
	// the store writes the name down so that a binding is named once and keeps the name. That is
	// what lets the panel's binding keep answering at the path it answered at before a strip was
	// bound beside it.
	ID string

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
//   - one segment, one binding. WLED has no ownership: the only exclusivity there is, is the
//     one this check makes. Two bindings on one segment overwrite each other on every frame,
//     and neither log line would say so. This is "a segment carries one printer only", and it
//     is the rule that protects a borrowed zone.
//   - -1 claims every segment on its controller, so it collides with any other binding there.
//     A controller somebody also drives is only ever borrowed, one segment at a time, and a
//     whole-controller body (which carries every segment it draws) would take the rest.
//   - a brightness on a borrowed binding is refused rather than ignored: a field nothing reads
//     is a field the next change reads, and a writer that carried one could dim a desk light to
//     the value meant for a status strip.
//   - two bindings cannot share a name. The name is what the routes address and what the layout
//     table is keyed on, so a shared one would be two bindings answering at one path.
//
// One rule is deliberately *not* here, and its absence is a requirement: "a printer may sit on
// several lights" (docs/product-requirements.md §2.2). Two bindings carrying one printer are two
// displays of one machine, which is the product — a panel in the office and a strip on the desk
// read the same job and each draws it for the room it is in. What they must not do is share a
// segment, and that is the first rule above.
//
// An empty set is likewise not a refusal, and that is a requirement too. The manifest declares no
// options (docs/product-requirements.md §4), so a fresh install is configured with nothing at all:
// it must start, serve its pages and drive nothing until a light is added there. "Nothing to watch"
// was a refusal while every install had a settings block to write in; with no such block it would be
// a module that cannot start until somebody writes a setting the host cannot send. `runner.apply`
// already says the same thing in its own words: an empty set means this module drives nothing, and
// the light is left as the last frame found it.
//
// One further check is not here, because it needs a fact only the controller has: whether the
// segments two bindings name really exist. The borrow reads /json/state at start and refuses
// a segment the device does not report (see writer.borrow), so a set naming segments a controller
// does not hold writes nothing and says why — which is the same answer, one read later.
func (bs bindings) check() error {
	claims := make(map[string]*claim, len(bs))
	named := make(map[string]int, len(bs))

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

		held := claims[key]
		if held == nil {
			held = &claim{segments: make(map[int]seat, 1)}
			claims[key] = held
		}

		mine := seat{at: i + 1, printer: b.DeviceID}

		switch {
		case b.Segment == -1 && held.whole.held():
			return fmt.Errorf(
				"%s drives the whole of %s, which binding %d (%s) already drives",
				where, b.Address, held.whole.at, held.whole.printer)
		case b.Segment == -1 && len(held.segments) > 0:
			return fmt.Errorf(
				"%s drives the whole of %s, and another binding drives a segment of it: a whole controller leaves no segment for anything else",
				where, b.Address)
		case b.Segment >= 0 && held.whole.held():
			return fmt.Errorf(
				"%s drives segment %d of %s, which binding %d (%s) drives whole: a whole controller leaves no segment for anything else",
				where, b.Segment, b.Address, held.whole.at, held.whole.printer)
		case b.Segment >= 0:
			if earlier, ok := held.segments[b.Segment]; ok {
				return fmt.Errorf(
					"%s drives segment %d of %s, which binding %d (%s) already drives: one segment is shown by one binding",
					where, b.Segment, b.Address, earlier.at, earlier.printer)
			}

			held.segments[b.Segment] = mine
		}

		if b.Segment == -1 {
			held.whole = mine
		}

		// A binding with no name yet is named by identified before a set runs, so the only
		// thing to refuse here is two bindings that carry the same one.
		if earlier, ok := named[b.ID]; ok && b.ID != "" {
			return fmt.Errorf(
				"%s and binding %d are both named %q: a binding's name is what its routes address, so two bindings cannot share one",
				where, earlier, b.ID)
		}

		named[b.ID] = i + 1
	}

	return nil
}

// seat is one binding's place in a set, as a claim records it: where it sits, and the printer it
// shows. The printer is kept beside the position because under multi-room a refusal that named
// only "binding 2" would be a refusal about a number the operator has to count to, in a list where
// the same printer may occupy two lines.
type seat struct {
	// at is the binding's position in the set, counting from one, and zero for no binding.
	at int

	// printer is the host's device id the binding shows.
	printer string
}

// held says whether a claim has been taken.
func (s seat) held() bool { return s.at != 0 }

// claim is what one controller has already been asked for, by the bindings that asked
// for it. It exists so that the rules are written once per controller and a message can name
// the binding that got there first.
type claim struct {
	// whole is the binding driving the whole controller, and the zero seat for none.
	whole seat

	// segments is each named segment and the binding driving it.
	segments map[int]seat
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

// find is the binding one name on these routes addresses, and false for a name this module does
// not drive.
//
// The name is the binding's own — ID — and not the printer's device id: under multi-room one
// printer is two bindings, so the device id names a machine and the ID names a binding. A set that
// has not been through identified has no names, which is why applyLocked names one before it runs.
func (bs bindings) find(id string) (binding, bool) {
	for _, b := range bs {
		if b.ID != "" && b.ID == id {
			return b, true
		}
	}

	return binding{}, false
}

// onPrinter is every binding carrying one printer, in the operator's order: one entry, two, or
// none. It is what a printer-keyed question asks — "is this printer shown at all, and where" —
// and it is deliberately not find, because a printer is not a binding's name.
func (bs bindings) onPrinter(deviceID string) bindings {
	held := make(bindings, 0, 1)

	for _, b := range bs {
		if b.DeviceID == deviceID {
			held = append(held, b)
		}
	}

	return held
}

// identified is the set with every binding named: a name already written stays exactly as it is,
// and a binding with none takes the printer's device id when no earlier binding in the set holds
// it, and qualifiedID otherwise.
//
// It is the one place a name is minted, and it runs on every set before it is checked or run — so
// the order of the set is the only thing that decides which of two bindings of one printer takes
// the bare device id. An existing binding keeps it, because an existing binding is already named
// in the store and appears first in the list it was written in; a binding added later is appended
// and takes the qualified name. That is what leaves the panel answering at the path it answered at
// before a strip was bound beside it.
func (bs bindings) identified() bindings {
	named := append(bindings(nil), bs...)

	taken := make(map[string]bool, len(named))

	for _, b := range named {
		if b.ID != "" {
			taken[b.ID] = true
		}
	}

	for i := range named {
		if named[i].ID != "" {
			continue
		}

		want := named[i].DeviceID

		if taken[want] {
			want = qualifiedID(named[i])
		}

		named[i].ID = want
		taken[want] = true
	}

	return named
}

// qualifiedID is the name a binding takes when its printer's device id is already another
// binding's name in the set: the device id, the light, and the segment.
//
// Those three are unique together — check refuses a second binding on one segment of one
// controller — so two bindings of one printer cannot mint the same qualified name. The light is
// the controller's name on the routes (its key without the scheme), so the name reads as an
// address a person can match against the Bindings screen: a 48-LED strip on 10.0.0.253 driving the
// whole light is `07e893bb-...@10.0.0.253:whole`.
func qualifiedID(b binding) string {
	light := strings.TrimRight(strings.TrimSpace(b.Address), "/")

	if key, err := controllerKey(b.Address); err == nil {
		light = nameOfController(key)
	}

	segment := fmt.Sprintf("%d", b.Segment)
	if b.Segment < 0 {
		segment = "whole"
	}

	return b.DeviceID + "@" + light + ":" + segment
}

// without is the set with one binding taken out, in the operator's order. A set that does not
// hold the name is returned unchanged, and false says so.
func (bs bindings) without(id string) (bindings, bool) {
	kept := make(bindings, 0, len(bs))

	for _, b := range bs {
		if b.ID != id {
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
//
// It matches on the name, so a binding whose name is not in the set yet is appended — which is
// what makes POST an add and PUT a replace. A binding with no name at all cannot match, and is
// named on its way in by identified.
func (bs bindings) with(b binding) bindings {
	for i := range bs {
		if b.ID != "" && bs[i].ID == b.ID {
			next := append(bindings(nil), bs...)
			next[i] = b

			return next
		}
	}

	return append(append(bindings(nil), bs...), b)
}

// bindingSetting is one entry of the `bindings` setting: one printer on one light, as the
// migration from 0.1.0's settings reads it and as the editor's routes write it.
//
// It is a shape of several fields rather than one of a settings block's single values, so a list
// of them is what the block carried — one line per printer — and this struct decodes one line at
// a time, so that a refusal can name the line:
//
//	[{"printer": "Hermes MK4", "controller": "http://10.0.0.136"},
//	 {"printer": "Mini",       "controller": "http://10.0.0.136", "segment": 3}]
//
// One printer may appear on two lines — one per light — as long as no two lines take the same
// segment of the same controller.
//
// Segment and brightness are pointers so that a line that leaves them out is not read as zero.
// Zero is a real answer to both — segment 0 is the first segment a controller holds, and a
// brightness of 0 is a strip switched off — so an absent one has to be told apart from a
// written one, and it is the difference between driving the whole controller and borrowing its
// first segment.
type bindingSetting struct {
	// ID is the binding's name, and it is written rather than derived for every binding the store
	// holds: a name minted once and kept is what stops a printer's second light from renaming the
	// first. The store writes it (store.go); a line of the `bindings` setting may carry one, and a
	// line that does not is named by identified.
	ID string `json:"id,omitempty"`

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
			ID:         strings.TrimSpace(one.ID),
			DeviceID:   deviceID,
			Address:    one.Controller,
			Segment:    one.segment(),
			Brightness: one.brightness(),
		})
	}

	// Every binding needs a name before the set can be run or read back: the store writes it, the
	// routes address it, and the layout table is keyed on it. A line that carried one keeps it
	// exactly, which is how the panel's binding survives the upgrade that introduced the field.
	set = set.identified()

	if err := set.check(); err != nil {
		return nil, err
	}

	return set, nil
}

// wantedBindings is the set one settings block asks for.
//
// `bindings` wins over the four settings that came before it, because an install that has been
// running for a week carries them and its store has to be seeded from what it has: an operator
// who writes a list gets the list, and the single printer the old keys name is not silently
// added to it or silently left driving. That is said beside the setting and in the answer the
// editor's routes give, rather than left for somebody to work out from two lights on one strip.
//
// A block that names neither a list nor a controller asks for nothing, and an empty set is what
// this returns for it. That is the fresh install: the manifest declares no options, so the host
// sends an empty block, and the module must start (docs/product-requirements.md §4 — "If the
// host settings are absent, defaults apply"). The set is empty on purpose and legal: the runner
// builds no writer, the pages answer, and the operator's first light arrives through a route.
func wantedBindings(ctx context.Context, fleet devices, sent Settings) (bindings, error) {
	if len(sent.Bindings) > 0 {
		return bindingsFrom(ctx, fleet, sent.Bindings)
	}

	if strings.TrimSpace(sent.ControllerAddress) == "" {
		return nil, nil
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

	set := bindings{one}.identified()

	if err := set.check(); err != nil {
		return nil, err
	}

	return set, nil
}
