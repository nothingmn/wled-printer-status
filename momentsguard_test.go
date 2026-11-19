// The guard for the class of defect that took this suite red four times in one day: a fixture
// anchored to an instant the calendar walks past, judged by code that reads a clock the test does
// not own.
//
// What happened, in one sentence: the same tree passed at 19:45Z and failed at 20:06Z, because a
// fixture said 20:00Z and the driver's own ticker judged it stale thirty seconds later. It was
// repaired one fixture at a time every time it appeared, so it appeared again — four show tests at a
// fixed noon, a pause stamped at noon against a writer built from now, and then the moment table.
// These tests are what replaces repairing it again.
package main

import (
	"bytes"
	"os"
	"regexp"
	"testing"
	"time"

	"github.com/nothingmn/wled-printer-status/internal/printer"
)

// absoluteInstant is a date and a time written out, which is what such a fixture must never be
// anchored to: it is a fact about the day it was typed, not about the moment the test runs.
var absoluteInstant = regexp.MustCompile(`[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}`)

// TestAMomentReadingIsStampedAtTheRun is the door itself.
//
// A reading is live because it was taken now, and quiet only because the test said so — never
// because a date in the source happens to be on the right side of printer.StaleAfter today. If this
// fails, momentReading.now() has stopped taking its instant from the run, and every moment test
// downstream is about to become a test of what time it is.
func TestAMomentReadingIsStampedAtTheRun(t *testing.T) {
	fresh := liveReading(printer.StatePrinting).now()

	if fresh.Stale {
		t.Fatal("a live reading is stamped stale")
	}

	// Five seconds is not a tolerance on the rule; it is room for a loaded machine between the stamp
	// and this check. An anchor that has gone stale is minutes or months away, never seconds.
	if age := time.Since(fresh.LastChange); age < 0 || age > 5*time.Second {
		t.Fatalf("a live reading is stamped %v from now: a moment fixture must carry the run's instant, not a date", age)
	}

	if quiet := (quietReading(printer.StateFinished)).now(); !quiet.Stale {
		t.Fatal("a quiet reading is stamped live")
	}
}

// TestNoRealClockFixtureIsAnchoredToAnInstant is the guard, and it is a scan of two named files
// rather than of the package.
//
// A scan of the whole package cannot answer the question cheaply: half the fixtures here carry a
// fixed instant on purpose, and are correct, because the same instant is handed in as the clock the
// code reads (`newFakeClock(at)` in show_test, `clock:` on the routes in wave3, denver(t, 21, 0)
// for the brightness windows). What makes a literal dangerous is not the literal: it is being judged
// by a clock the test does not own. That is a fact about a test's wiring, not about its text, so a
// scanner over every file would either flag correct code or need a parser. What is cheap and exact
// is to name the two places where a fixture is judged by the machine's clock, and refuse the token
// that anchors it there.
//
// Each entry is one way the defect comes back:
//
//	moments_test.go    a moment reading, judged by driver.run's real ticker
//	framesapi_test.go  a pause with an expiry, judged by a writer the process starts
//
// A third place would be a third entry, and the failure below is written to say so.
func TestNoRealClockFixtureIsAnchoredToAnInstant(t *testing.T) {
	for _, one := range []struct {
		file  string
		find  func([]byte) int
		why   string
		fixed string
	}{
		{
			file: "moments_test.go",
			find: func(source []byte) int { return bytes.Index(source, []byte("time.Date(")) },
			why: "a moment fixture anchored to a date the calendar walks past. Take the reading from " +
				"liveReading or quietReading, so its instant is the run's.",
			fixed: "a fixed instant",
		},
		{
			file: "framesapi_test.go",
			find: func(source []byte) int { return bytes.Index(source, []byte("Expires:")) },
			why: "a pause with an expiry, judged by a writer that reads the machine's clock: it is in " +
				"force until its instant and expired for ever after. Give the fixture no expiry, or hand " +
				"the writer a clock.",
			fixed: "an expiry",
		},
	} {
		source, err := os.ReadFile(one.file)
		if err != nil {
			t.Fatalf("read %s: %v — this guard is looking at a file that is not there, and proves nothing", one.file, err)
		}

		if at := one.find(source); at >= 0 {
			t.Fatalf("%s holds %s at byte %d: %s\nIf this file has stopped being judged by a clock the "+
				"test does not own, take its entry out of this guard in the same commit.", one.file, one.fixed, at, one.why)
		}
	}

	// The same rule for a date written out as text, which no reader would notice among the fixtures.
	source, err := os.ReadFile("moments_test.go")
	if err != nil {
		t.Fatalf("read moments_test.go: %v", err)
	}

	if where := absoluteInstant.FindIndex(source); where != nil {
		t.Fatalf("moments_test.go writes the instant %q at byte %d: a now written down is a now that "+
			"expires. Take the reading from liveReading or quietReading, or derive it from time.Now().",
			source[where[0]:where[1]], where[0])
	}
}
