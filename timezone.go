// The zone this module judges a clock in and judges a brightness window in: one IANA name, kept in
// this module's own store, and the reason it is not a setting any more.
//
// # Why the zone is a fact of the store and not of the manifest
//
// A brightness window is a span of the day and the brightness to run at inside it, and the ends line
// on a panel is a clock time. Both are read against a zone, and neither may be read against this
// process's own clock: the process runs inside the Prusa Connect container, whose clock is almost
// always UTC, while the operator lives at UTC-7 in a zone that observes daylight saving. A window
// written as "evening, 20:00-07:00, brightness 40" judged against the container's clock dims the light
// at lunchtime, and a clock drawn there is wrong every evening — a display that is wrong and looks
// healthy, which is the worst shape a defect in this module can take (AGENTS.md §5.14).
//
// So the zone is required whenever either of those two things is used, and never defaulted. Until
// schema version 9 it was a manifest setting, and the manifest has carried no `options` since the
// release that made this store the only editing surface — so no install could name one at all, and
// both consumers refused. The store now holds it, the Overview screen edits it, and one upgrade reads
// the old setting once as the migration source.
//
// # The rule this file states, in one sentence
//
// **The store's zone is the authority.** It is read from the document and from nowhere else once a
// document exists. The `timezone` setting is consulted only for a module with no document to read —
// which is a module the host granted no data directory, running exactly as it did before the store
// existed — and it is never consulted again once the start has written it into the store.
package main

import (
	"fmt"
	"strings"
	"time"
)

// How a zone arrived, as the document records it and as an answer prints it.
const (
	// zoneOriginBrowser is the first page load that found no zone and captured the browser's own:
	// Intl.DateTimeFormat().resolvedOptions().timeZone, written once and never overwritten.
	zoneOriginBrowser = "browser"

	// zoneOriginOperator is a zone typed on the Overview screen, which is the only way to change a
	// zone once one exists.
	zoneOriginOperator = "operator"

	// zoneOriginSettings is a zone carried over from the `timezone` setting by the one upgrade that
	// reads it. Nothing writes this value after that upgrade.
	zoneOriginSettings = "settings"
)

// Where the zone in an answer came from, as opposed to how a stored one got into the store. The
// settings' word is shared with the stored source above on purpose: a zone the setting carries and a
// zone the upgrade copied out of it came from the same place, and two spellings of one fact would be
// two things to keep in step.
const (
	// zoneOriginStore is a zone this module's own document holds.
	zoneOriginStore = "store"

	// zoneOriginNone is a module that names no zone: a window cannot be written and no clock is drawn,
	// and both say so rather than falling back to UTC.
	zoneOriginNone = "none"
)

// zoneExample is the zone every refusal names, because an operator needs a value he can type and
// "an IANA name" alone is not one. It is also the zone this module was written against: UTC-7 with
// daylight saving, which is what makes the container's UTC clock a seven-hour mistake in summer.
const zoneExample = "America/Denver"

// zoneInForce is the zone in answer to both questions a consumer has — which zone is in force, and
// where it came from — and the one place that decides between the store and the setting.
//
// It is one function for the two consumers (timezone.go's routes, and show.go's painter through
// tableAPI.zoneName) for the reason the window parser is one function: a clock drawn from one rule
// and a window judged by another is a light that disagrees with the screen about the time.
//
// `stored` says whether a document was read at all. It is not the same question as "does the document
// hold a zone": a document that exists and names none means this module names none, and falling back
// to the setting there would make the setting live for ever — which is the second half of the old
// behaviour §4 exists to end.
func zoneInForce(storeZone string, stored bool) (zone, origin string) {
	if stored {
		named := strings.TrimSpace(storeZone)
		if named == "" {
			return "", zoneOriginNone
		}

		return named, zoneOriginStore
	}

	// No document: a module granted no data directory, or a start before the first document was
	// written. This is the pre-store behaviour kept, and the path the migration reads.
	named := strings.TrimSpace(settings().Timezone)
	if named == "" {
		return "", zoneOriginNone
	}

	return named, zoneOriginSettings
}

// checkZone reads one zone name and answers it as the loader spells it, or the sentence saying why it
// will not do.
//
// It refuses an empty name and a name that will not load, in words that name a value to type: telling
// a person "that is not an IANA name" without an example is telling him what he already knows.
//
// `Local` is refused by name even though the loader accepts it. `Local` is this process's own zone,
// which inside the container is UTC — the exact substitution the whole file exists to prevent, and one
// that would load happily and be seven hours wrong.
func checkZone(named string) (string, error) {
	name := strings.TrimSpace(named)

	if name == "" {
		return "", fmt.Errorf(
			"no zone was named, and this module judges a brightness window and writes a clock in a zone of its own: name an IANA zone such as %s. This process runs inside the Prusa Connect container, whose own clock is almost always UTC, so a window or a clock read there would be hours away from your evening and nothing would say so",
			zoneExample)
	}

	if strings.EqualFold(name, "Local") {
		return "", fmt.Errorf(
			"the zone is %q, which means this process's own clock and not a place: inside the Prusa Connect container that is almost always UTC, so it is not a zone this module will store. Name the place instead, as an IANA name such as %s",
			name, zoneExample)
	}

	location, err := time.LoadLocation(name)
	if err != nil {
		return "", fmt.Errorf(
			"the zone is %q, which is not a zone this build can load: write an IANA name such as %s, because a window and a clock are read in the zone you name and never in this process's own (%w)",
			name, zoneExample, err)
	}

	// The loader's own spelling of the name, which is what every answer and log line prints. It is
	// the same string for every IANA name, and taking it from the loader rather than from the request
	// is what keeps `judged_in` and the stored document the same word.
	return location.String(), nil
}
