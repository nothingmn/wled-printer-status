// The declared light slots: the only addresses this module may ever connect to.
//
// The manifest's `egress` block can name a host, a `*.domain`, or one declared
// option whose value the operator types — it has no form for "whatever light
// the operator points it at", and no form for a list. That is why the module
// declares five `url` options, `light1_address` to `light5_address`, each with
// one egress entry reading the host from it, and why those five values are the
// whole of the address set. A marketplace reviewer reads the prompt and sees
// every address this process can reach; the process must then act on exactly
// those and no others.
//
// The rule, in one place: `declares` is the only answer to "may this module
// contact that address", and every surface that builds a client or decides to
// drive a binding asks it. An address written into the store by an older
// release, or typed into a route, is not driven, not probed, and never written
// to until an operator copies it into the extension's own settings. The store
// still keeps bindings, segments, layouts, moments and ignore flags keyed by
// address; it is the *set of addresses* that comes from here, read every time
// the settings are read and never cached in the store.
package main

import (
	"fmt"
	"strings"

	"github.com/nothingmn/wled-printer-status/internal/wled"
)

// declaredLightSlots is the number of address slots the manifest declares. The
// count is a limit and not a default: an empty slot permits nothing.
const declaredLightSlots = 5

// declaredLight is one of the manifest's address slots.
type declaredLight struct {
	// Slot is its position, counting from one, and it is the number the option
	// key carries (`light1_address` is slot 1).
	Slot int

	// Address is the slot's value as this module will dial it: trimmed, with a
	// scheme, and without a trailing slash. Empty means the slot permits nothing.
	Address string

	// Filled says the slot names an address.
	Filled bool
}

// lightAddresses is the five option values in slot order, exactly as the host
// sent them.
func (s Settings) lightAddresses() []string {
	return []string{
		s.Light1Address,
		s.Light2Address,
		s.Light3Address,
		s.Light4Address,
		s.Light5Address,
	}
}

// DeclaredLights is the five slots, in order, with empty slots kept so that a
// screen can say how many exist and which one is waiting for an address.
//
// A value that is not an address at all is refused by Settings.validate before
// this is read in the process; on a document written by an older build the
// answer is an empty slot rather than a panic, because a store read must not be
// able to fail a start.
func (s Settings) DeclaredLights() []declaredLight {
	out := make([]declaredLight, 0, declaredLightSlots)

	seen := make(map[string]bool, declaredLightSlots)

	for i, raw := range s.lightAddresses() {
		slot := i + 1

		address := normaliseDeclaredAddress(raw)
		if address == "" {
			out = append(out, declaredLight{Slot: slot})

			continue
		}

		key := lightKey(address)
		if seen[key] {
			// Settings.validate refuses a repeated address; a settings block
			// that reached here anyway still permits it once, and the second
			// slot is reported empty rather than as a second permission.
			out = append(out, declaredLight{Slot: slot})

			continue
		}

		seen[key] = true

		out = append(out, declaredLight{Slot: slot, Address: address, Filled: true})
	}

	return out
}

// declares answers whether one address is one of the declared slots. Two
// spellings of one address are one address, by the same key the binding rules
// use, so http://10.0.0.136 and http://10.0.0.136/ cannot disagree.
func (s Settings) declares(address string) bool {
	wanted := lightKey(address)

	for _, slot := range s.DeclaredLights() {
		if slot.Filled && lightKey(slot.Address) == wanted {
			return true
		}
	}

	return false
}

// checkDeclaredAddresses judges the five values and names the slot that is wrong.
func (s Settings) checkDeclaredAddresses() error {
	seen := make(map[string]string, declaredLightSlots)

	for i, raw := range s.lightAddresses() {
		if strings.TrimSpace(raw) == "" {
			continue
		}

		address, err := normaliseLightAddress(raw)
		if err != nil {
			return fmt.Errorf("light%d_address: %w", i+1, err)
		}

		key := lightKey(address)
		if first, taken := seen[key]; taken {
			return fmt.Errorf(
				"light%d_address and %s name the same controller (%s); one slot per address",
				i+1, first, address)
		}

		seen[key] = fmt.Sprintf("light%d_address", i+1)
	}

	return nil
}

// normaliseDeclaredAddress is one option value as this module dials it, or the
// empty string for a slot nothing filled in. It never fails: an unparseable
// value has already been refused at the start by checkDeclaredAddresses, and a
// value this lenient reading cannot use permits nothing.
func normaliseDeclaredAddress(raw string) string {
	typed := strings.TrimSpace(raw)
	if typed == "" {
		return ""
	}

	if !strings.Contains(typed, "://") {
		typed = "http://" + typed
	}

	base := strings.TrimRight(typed, "/")

	if _, err := wled.New(base); err != nil {
		return ""
	}

	return base
}

// declaredNow is the declaration in force, read on every pass the way the rest
// of the settings are: the options the host last sent, and nothing held in the
// store.
func declaredNow() []declaredLight {
	return settings().DeclaredLights()
}

// addressDeclared answers the process's one question about an address it is
// about to contact. Every client construction goes through it or through a
// caller that has already asked.
func addressDeclared(address string) bool {
	return settings().declares(address)
}

// refusedReason is the sentence a binding, a light row or a route gets when an
// address is not one of the declared slots. One sentence, written once, because
// the same fact must read the same way wherever a person meets it.
func refusedReason(address string) string {
	return fmt.Sprintf(
		"%s is not one of the five light addresses this module's settings declare (light1_address to light5_address), "+
			"so this module does not drive it, probe it, or write to it. Copy its address into one of those settings "+
			"in the extension's own settings, then save; nothing is driven until you do.",
		address)
}
