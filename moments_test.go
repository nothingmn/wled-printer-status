// The moment table, fired: the tests.
//
// What they are for, in one sentence each: every `shows` value acts on the light when its moment
// happens, a moment that is off does nothing, a preset borrows the whole light and gives it back, a
// preset is refused where the light is not this module's, a word is a word on a panel and a colour on
// a strip, `for` ends the moment and the steady picture comes back, and the finished moment still
// holds its colour through silence.
//
// The store here is a hand-written one — show_test.go's handTable — which answers through the same
// functions the document's reader uses. So these are tests about the rule, not about a mock's idea of
// it, and no file is written.
package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nothingmn/wled-printer-status/internal/printer"
	"github.com/nothingmn/wled-printer-status/internal/render"
	"github.com/nothingmn/wled-printer-status/internal/text"
	"github.com/nothingmn/wled-printer-status/internal/wled"
)

// defaultColour is a state's colour as the renderer holds it, with no store to ask: the table's own
// answer, which is what defaultMomentEvent is about.
func defaultColour(state string) (wled.RGB, bool) {
	definition, known := findState(state)
	if !known {
		return wled.RGB{}, false
	}

	return definition.colour, true
}

// defaultMomentEvent is what the code's own moment table makes of one transition on a light of this
// geometry: the event a module with no store would fire, or nothing.
//
// It is the test's own path into the same decision momentEvent makes — the default entry, the
// geometry, the state table's colours — so that a test about the default table asserts the picture
// rather than a writer's plumbing.
func defaultMomentEvent(from, to string, geometry panel, colour wled.RGB) (event, bool) {
	id, isMoment := momentFor(from, to)
	if !isMoment {
		return event{}, false
	}

	entry := momentDefault(id)
	if !entry.Enabled {
		return event{}, false
	}

	ev, _, err := momentEvent(entry, render.Frame{Colour: colour, Total: geometry.width * geometry.height},
		geometry, defaultColour, false)
	if err != nil || ev.showsNothing() {
		return event{}, false
	}

	return ev, true
}

// momentRun is one watch a moment test drives: the writer, the light, the store it reads, and the
// printer whose reading the test moves.
type momentRun struct {
	pen     *writer
	strip   strip
	source  *changingReadings
	written *logs
}

// runMoments starts one writer and one driver over one binding on the reading given, with the store
// the test wrote, at a tick short enough that a test does not wait for a second.
func runMoments(t *testing.T, pen *writer, store lights, light strip, first printer.Snapshot) *momentRun {
	t.Helper()

	source := &changingReadings{}
	source.show(first)

	written := &logs{}

	pen.store = store
	pen.logf = written.logf

	driver := newDriver(source, pen, light, written.logf)
	driver.store = store
	driver.tick = 2 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())

	t.Cleanup(cancel)
	t.Cleanup(func() {
		current.Store(nil)
		sent.Store(nil)
		forgetBindings()
		forgetPreview()
	})

	go pen.run(ctx)
	go driver.run(ctx)

	return &momentRun{pen: pen, strip: light, source: source, written: written}
}

// momentsAt is the instant these tests start at.
var momentsAt = time.Date(2026, time.November, 19, 20, 0, 0, 0, time.UTC)

// momentTableFor is a store whose one moment says what a test wants it to say.
func momentTableFor(id string, entry momentEntry) *handTable {
	return &handTable{moments: map[string]momentEntry{id: entry}}
}

// TestTheMomentSetIsTheScreensEight is the list itself, and the one row that does not fire.
//
// The eight are the approved Events screen's own rows, and the table, the routes and the defaults all
// count them from momentTable. What this pins is the mapping from the printer's transitions onto
// them: seven have one, and `light-lost` has none because it is a condition of this module's
// transport rather than a transition of the printer — a fact that is deliberate and is reported rather
// than papered over by inventing a trigger from the writer's failure path.
func TestTheMomentSetIsTheScreensEight(t *testing.T) {
	if len(momentTable) != 8 {
		t.Fatalf("the moment table holds %d moments, and the approved screen draws eight", len(momentTable))
	}

	for _, one := range []struct {
		from string
		to   string
		want string
	}{
		{from: printer.StatePrinting, to: printer.StateFinished, want: momentPrintFinished},
		{from: printer.StateIdle, to: printer.StatePrinting, want: momentPrintStarted},
		{from: printer.StatePrinting, to: printer.StateAttention, want: momentFilamentChange},
		{from: printer.StatePrinting, to: printer.StatePaused, want: momentPaused},
		{from: printer.StatePrinting, to: printer.StateError, want: momentErrorAttention},
		{from: printer.StatePrinting, to: printer.StateStopped, want: momentPrintCancelled},
	} {
		got, known := momentFor(one.from, one.to)
		if !known || got != one.want {
			t.Fatalf("%s to %s is the moment %q (known %v), want %q", one.from, one.to, got, known, one.want)
		}
	}

	// The quiet moment is not a transition of the state word, so it is fired by the driver's own
	// staleness rule rather than by momentFor. What is pinned here is that momentFor never answers it,
	// so that a change of state word cannot be mistaken for a printer going quiet.
	for _, to := range stateNames() {
		if id, known := momentFor(printer.StatePrinting, to); known && id == momentPrinterWentQuiet {
			t.Fatalf("PRINTING to %s answered the quiet moment, which is not a state word", to)
		}
	}

	// And `light-lost` is a moment with no transition at all. Its row stays in the table — it is
	// editable and reported — and nothing fires it, which is the fact this test states rather than
	// leaves to a reader to notice.
	for _, to := range stateNames() {
		for _, from := range stateNames() {
			if id, known := momentFor(from, to); known && id == momentLightLost {
				t.Fatalf("%s to %s answered light-lost, and no state word can say the light is lost", from, to)
			}
		}
	}
}

// TestEachShowsValueFiresOnItsMoment is the four values of `shows`, one light each, on the same
// transition: the finished moment, with the store saying what it shows and nothing else changed.
//
// Each case names what only it can cause, so a case that stopped acting would fail rather than pass
// on another case's write:
//
//	Preset   the light's own preset was run, and the light was put back afterwards
//	Word     the panel's establishing body carried the word
//	Colour   a body carried that colour over the whole light
//	Nothing  nothing at all: not one write beyond the steady frame the driver decided
func TestEachShowsValueFiresOnItsMoment(t *testing.T) {
	preset := "Snow Fall"

	for _, one := range []struct {
		name  string
		entry momentEntry
		want  func(t *testing.T, run *momentRun)
	}{
		{
			name:  "Preset",
			entry: momentEntry{Enabled: true, Shows: showsPreset, Which: preset, For: "30s"},
			want: func(t *testing.T, run *momentRun) {
				t.Helper()

				fake := run.strip.(*panelStrip)

				waitFor(t, "the preset to be run", func() bool { return len(fake.ranPresets()) == 1 })

				if got := fake.ranPresets(); got[0] != preset {
					t.Fatalf("the light ran the preset %q, want %q", got[0], preset)
				}
			},
		},
		{
			name:  "Word",
			entry: momentEntry{Enabled: true, Shows: showsWord, Which: "HELLO", For: "30s"},
			want: func(t *testing.T, run *momentRun) {
				t.Helper()

				fake := run.strip.(*panelStrip)

				waitFor(t, "the word to be established on the panel", func() bool { return paintings(fake) > 0 })

				named := ""
				for _, call := range fake.shown() {
					if len(call.segments) > 0 && call.segments[0].Name != "" {
						named = call.segments[0].Name
					}
				}

				if named != "HELLO" {
					t.Fatalf("the panel's picture is named %q, want the moment's word HELLO", named)
				}
			},
		},
		{
			name:  "Colour",
			entry: momentEntry{Enabled: true, Shows: showsColour, Which: printer.StateError, For: "30s"},
			want: func(t *testing.T, run *momentRun) {
				t.Helper()

				waitFor(t, "the moment's colour to reach the light", func() bool {
					return painted(run.strip.(*panelStrip), render.ColourError)
				})
			},
		},
		{
			name:  "Nothing",
			entry: momentEntry{Enabled: true, Shows: showsNothing, For: "30s"},
			want: func(t *testing.T, run *momentRun) {
				t.Helper()

				fake := run.strip.(*panelStrip)

				// The steady finished frame is painted — the driver decided it, and a moment that shows
				// nothing must not stop the light being told what is true. What must not happen is a
				// picture of a *moment*: the error red, a word, or a preset.
				waitFor(t, "the steady finished frame", func() bool {
					return painted(fake, render.ColourFinished)
				})

				for _, line := range run.written.all() {
					if strings.Contains(line, "moment shows") {
						t.Fatalf("a moment that shows nothing said it showed something: %q", line)
					}
				}
			},
		},
	} {
		t.Run(one.name, func(t *testing.T) {
			strip := newPanelStrip()
			pen := newWriter(strip, 128, quiet)

			clock := newFakeClock(momentsAt)
			clock.watch(pen)
			pushInto(pen)

			run := runMoments(t, pen, momentTableFor(momentPrintFinished, one.entry), strip,
				snapshotOf(printer.StatePrinting, true, false, momentsAt))

			// The baseline: the printing frame is on the light before anything happens.
			waitFor(t, "the printing frame", func() bool { return painted(strip, render.ColourPrinting) })

			run.source.show(snapshotOf(printer.StateFinished, true, false, momentsAt))

			one.want(t, run)
		})
	}
}

// TestAMomentThatIsOffDoesNothing is the switch on the Events screen: the same moment, the same
// transition, and the light is not told anything but what the driver decided.
func TestAMomentThatIsOffDoesNothing(t *testing.T) {
	strip := newPanelStrip()
	pen := newWriter(strip, 128, quiet)

	clock := newFakeClock(momentsAt)
	clock.watch(pen)
	pushInto(pen)

	// A Colour entry that would be unmistakable if it fired.
	off := momentEntry{Enabled: false, Shows: showsColour, Which: printer.StateError, For: "30s"}

	run := runMoments(t, pen, momentTableFor(momentPrintFinished, off), strip,
		snapshotOf(printer.StatePrinting, true, false, momentsAt))

	waitFor(t, "the printing frame", func() bool { return painted(strip, render.ColourPrinting) })

	run.source.show(snapshotOf(printer.StateFinished, true, false, momentsAt))

	waitFor(t, "the steady finished frame", func() bool { return painted(strip, render.ColourFinished) })

	for _, line := range run.written.all() {
		if strings.Contains(line, "moment") {
			t.Fatalf("a moment that is off wrote %q", line)
		}
	}

	if painted(strip, render.ColourError) {
		t.Fatal("a moment that is off painted its colour anyway")
	}
}

// TestAPresetMomentBorrowsTheWholeLightAndPutsItBack is the Events screen's one rule: a preset carries
// its own segments, brightness and LED map, so running one takes the whole light, and what the light
// was doing goes back exactly as it was found when the moment ends.
func TestAPresetMomentBorrowsTheWholeLightAndPutsItBack(t *testing.T) {
	strip := newPanelStrip()
	strip.state = wled.State{
		On:         true,
		Brightness: 90,
		Segments: []wled.Segment{
			{ID: 0, Start: 0, Stop: 16384, On: true, Colors: []wled.RGB{{R: 10, G: 20, B: 30}}, Effect: 7, Name: "panel"},
			{ID: 1, Start: 0, Stop: 0, On: false},
		},
	}

	pen := newWriter(strip, 128, quiet)

	clock := newFakeClock(momentsAt)
	clock.watch(pen)
	pushInto(pen)

	entry := momentEntry{Enabled: true, Shows: showsPreset, Which: "Matrix", For: "30s"}

	run := runMoments(t, pen, momentTableFor(momentPrintFinished, entry), strip,
		snapshotOf(printer.StatePrinting, true, false, momentsAt))

	waitFor(t, "the printing frame", func() bool { return painted(strip, render.ColourPrinting) })

	// The mark is taken before the moment fires and the wait for it after the preset has run:
	// without that, the clock can move while the writer is still inside the write it just made,
	// and the deadline it arms afterwards is one the test did not mean. That is this test's
	// ordering, not the module's behaviour — it failed once inside a full run and never alone.
	mark := clock.mark()

	run.source.show(snapshotOf(printer.StateFinished, true, false, momentsAt))

	waitFor(t, "the preset to be run", func() bool { return len(strip.ranPresets()) == 1 })
	clock.waitArmed(t, mark)

	// The moment is over. Everything the preset replaced is written back.
	clock.Advance(31 * time.Second)

	waitFor(t, "the light to be put back", func() bool { return len(strip.givenBack()) == 2 })

	given := strip.givenBack()

	if given[0].Effect != 7 || given[0].Name != "panel" || given[0].Colors[0] != (wled.RGB{R: 10, G: 20, B: 30}) {
		t.Fatalf("segment 0 went back as %+v, want the effect, name and colour it was found with", given[0])
	}

	if got := strip.brightnessWrites(); len(got) == 0 || got[len(got)-1] != 90 {
		t.Fatalf("the brightness written after the moment is %v, want the 90 it was found at", got)
	}

	if got := strip.powerWrites(); len(got) == 0 || !got[len(got)-1] {
		t.Fatalf("the power written after the moment is %v, want the light left on as it was found", got)
	}

	if got := strip.ranPresets(); len(got) != 1 {
		t.Fatalf("the preset ran %d times, want once", len(got))
	}
}

// TestAPresetMomentOnABorrowedZoneIsRefused is the refusal the Events screen promises: a preset takes
// the whole light, and on a controller where this module drives one segment the rest of that strip
// belongs to somebody else. Nothing is run, nothing is borrowed, and the log says why once.
func TestAPresetMomentOnABorrowedZoneIsRefused(t *testing.T) {
	strip := newFakeStrip()
	strip.state = wled.State{
		On:         true,
		Brightness: 200,
		Segments: []wled.Segment{
			{ID: 0, Start: 0, Stop: 12, On: true, Name: "desk"},
			{ID: 1, Start: 12, Stop: 24, On: true, Name: "mine"},
		},
	}

	pen := newSegmentWriter(strip, 1, "http://10.0.0.253", quiet)

	clock := newFakeClock(momentsAt)
	clock.watch(pen)

	entry := momentEntry{Enabled: true, Shows: showsPreset, Which: "Matrix", For: "30s"}

	run := runMoments(t, pen, momentTableFor(momentPrintFinished, entry), strip,
		snapshotOf(printer.StatePrinting, true, false, momentsAt))

	waitFor(t, "the segment to be borrowed", func() bool { return len(strip.shown()) > 0 })

	run.source.show(snapshotOf(printer.StateFinished, true, false, momentsAt))

	waitFor(t, "the refusal", func() bool {
		for _, line := range run.written.all() {
			if strings.Contains(line, "borrowed") && strings.Contains(line, "preset") {
				return true
			}
		}

		return false
	})

	if got := strip.ranPresets(); len(got) != 0 {
		t.Fatalf("the preset %v was run on a borrowed zone, want nothing", got)
	}

	// And no second borrow: the writer's own hand-back happens once, when it stops.
	if got := strip.givenBack(); len(got) != 0 {
		t.Fatalf("the segment was given back %d times before the writer stopped, want none", len(got))
	}
}

// TestAMomentEndsOnItsOwnLengthAndTheSteadyPictureReturns is `for`, either side of its boundary: the
// moment holds the light while it lasts and the picture the driver keeps deciding is what the light
// comes back to.
func TestAMomentEndsOnItsOwnLengthAndTheSteadyPictureReturns(t *testing.T) {
	strip := newPanelStrip()
	pen := newWriter(strip, 128, quiet)

	clock := newFakeClock(momentsAt)
	clock.watch(pen)
	pushInto(pen)

	entry := momentEntry{Enabled: true, Shows: showsColour, Which: printer.StateError, For: "10s"}

	run := runMoments(t, pen, momentTableFor(momentPrintFinished, entry), strip,
		snapshotOf(printer.StatePrinting, true, false, momentsAt))

	waitFor(t, "the printing frame", func() bool { return painted(strip, render.ColourPrinting) })

	mark := clock.mark()
	run.source.show(snapshotOf(printer.StateFinished, true, false, momentsAt))

	waitFor(t, "the moment's colour", func() bool { return painted(strip, render.ColourError) })

	clock.waitArmed(t, mark)

	// One second short of the length: the moment is still the picture.
	clock.Advance(9 * time.Second)
	time.Sleep(20 * time.Millisecond)

	if !painted(strip, render.ColourError) {
		t.Fatal("the moment's colour was gone a second early")
	}

	// At the length it ends, and the steady picture — the finished green — takes the light back.
	after := clock.mark()
	clock.Advance(2 * time.Second)

	waitFor(t, "the steady finished picture to come back", func() bool {
		calls := strip.shown()

		return len(calls) > 0 && calls[len(calls)-1].colour == render.ColourFinished
	})

	clock.waitArmed(t, after)
}

// TestTheFinishedMomentHoldsItsColourThroughSilence is the rule proved on hardware, kept by the table
// rather than beside it: a finished job's word holds for its minute, and after that the light is the
// finished green — and a printer that then goes quiet for four minutes does not take it back. Appendix
// A: "a finished job holds its green until the next job rather than expiring on a timer — including
// through a silence".
func TestTheFinishedMomentHoldsItsColourThroughSilence(t *testing.T) {
	strip := newPanelStrip()
	pen := newWriter(strip, 128, quiet)

	clock := newFakeClock(momentsAt)
	clock.watch(pen)
	pushInto(pen)

	// No store at all: the default table, which is what an install that never opened the Events screen
	// acts on. Its finished row is the word FINISHED for a minute.
	run := runMoments(t, pen, nil, strip, snapshotOf(printer.StatePrinting, true, false, momentsAt))

	waitFor(t, "the printing frame", func() bool { return painted(strip, render.ColourPrinting) })

	mark := clock.mark()
	run.source.show(snapshotOf(printer.StateFinished, true, false, momentsAt))

	waitFor(t, "the finished word", func() bool { return paintings(strip) > 0 })
	clock.waitArmed(t, mark)

	// The word's own minute, and then the finished green.
	clock.Advance(finishedEventHolds + time.Second)

	waitFor(t, "the finished green after the word", func() bool {
		calls := strip.shown()

		return len(calls) > 0 && calls[len(calls)-1].colour == render.ColourFinished && paintings(strip) == 1
	})

	// 232 seconds of silence: the printer says nothing new, the reading goes stale, and the light keeps
	// the finished green. The frame the renderer decides for a stale reading is the finished colour,
	// because a terminal word is a fact silence cannot retract.
	mark = clock.mark()

	run.source.show(snapshotOf(printer.StateFinished, true, true, momentsAt.Add(-232*time.Second)))

	clock.Advance(232 * time.Second)
	time.Sleep(50 * time.Millisecond)

	calls := strip.shown()
	if len(calls) == 0 {
		t.Fatal("the light was never painted")
	}

	if got := calls[len(calls)-1].colour; got != render.ColourFinished {
		t.Fatalf("after 232 seconds of silence the light is %+v, want the finished green %+v",
			got, render.ColourFinished)
	}

	if got := paintings(strip); got != 1 {
		t.Fatalf("the word was established %d times, want once: a silent printer is not a new finish", got)
	}
}

// TestAWordMomentIsAWordOnAPanelAndAColourOnAStrip is §2.3's locked fallback, driven through the
// writer rather than through the decision alone: the panel is given the word, and the strip — which
// has no second dimension — is given the moment's colour instead.
func TestAWordMomentIsAWordOnAPanelAndAColourOnAStrip(t *testing.T) {
	entry := momentEntry{Enabled: true, Shows: showsWord, Which: "HELLO", For: "30s"}

	t.Run("a panel", func(t *testing.T) {
		strip := newPanelStrip()
		pen := newWriter(strip, 128, quiet)

		clock := newFakeClock(momentsAt)
		clock.watch(pen)
		pushed := pushInto(pen)

		run := runMoments(t, pen, momentTableFor(momentPrintFinished, entry), strip,
			snapshotOf(printer.StatePrinting, true, false, momentsAt))

		waitFor(t, "the printing frame", func() bool { return painted(strip, render.ColourPrinting) })

		run.source.show(snapshotOf(printer.StateFinished, true, false, momentsAt))

		waitFor(t, "the word's pixels", func() bool { return len(wordPixels(pushed)) > 0 })

		want, err := text.Raster("HELLO", finishedWordScale, text.Geometry{Width: 256, Height: 64})
		if err != nil {
			t.Fatalf("rasterise HELLO: %v", err)
		}

		if got := pixelsIn(pushed.last(), render.ColourFinished); len(got) != len(want) {
			t.Fatalf("the panel was given %d pixels of the word, want the %d the font draws", len(got), len(want))
		}
	})

	t.Run("a strip", func(t *testing.T) {
		strip := newFakeStrip()
		pen := newWriter(strip, 128, quiet)

		clock := newFakeClock(momentsAt)
		clock.watch(pen)

		run := runMoments(t, pen, momentTableFor(momentPrintFinished, entry), strip,
			snapshotOf(printer.StatePrinting, true, false, momentsAt))

		waitFor(t, "the printing frame", func() bool { return painted(strip, render.ColourPrinting) })

		run.source.show(snapshotOf(printer.StateFinished, true, false, momentsAt))

		waitFor(t, "the moment's colour", func() bool { return painted(strip, render.ColourFinished) })

		for _, call := range strip.shown() {
			for _, segment := range call.segments {
				if segment.Name != "" {
					t.Fatalf("the strip was given a named segment %q, and a word is panel only", segment.Name)
				}
			}
		}

		if len(strip.shown()) == 0 {
			t.Fatal("the strip was never painted")
		}
	})
}

// painted reports whether any body the light was given carried this colour, which is how a test reads
// "the light was painted that colour" out of a fake controller.
func painted(strip counted, colour wled.RGB) bool {
	for _, call := range bodiesOf(strip) {
		if call.colour == colour {
			return true
		}

		for _, segment := range call.segments {
			if segment.Colour == colour && segment.Count > 0 {
				return true
			}
		}
	}

	return false
}

// bodiesOf is every request one of the two fakes recorded, whichever it is.
func bodiesOf(strip counted) []paintCall {
	switch one := strip.(type) {
	case *fakeStrip:
		return one.shown()
	case *panelStrip:
		return one.shown()
	}

	return nil
}

// TestAMomentWithNoLengthHoldsUntilItsStateMoves is `for: forever`, which the Events screen offers as
// "until it resumes", "until it is cleared" and "until it answers" written as one rule: the moment holds
// the light until the state it was fired for moves, and then the steady picture is what is there.
func TestAMomentWithNoLengthHoldsUntilItsStateMoves(t *testing.T) {
	strip := newPanelStrip()
	pen := newWriter(strip, 128, quiet)

	clock := newFakeClock(momentsAt)
	clock.watch(pen)
	pushInto(pen)

	entry := momentEntry{Enabled: true, Shows: showsColour, Which: printer.StateError, For: durationForever}

	run := runMoments(t, pen, momentTableFor(momentPrintFinished, entry), strip,
		snapshotOf(printer.StatePrinting, true, false, momentsAt))

	waitFor(t, "the printing frame", func() bool { return painted(strip, render.ColourPrinting) })

	run.source.show(snapshotOf(printer.StateFinished, true, false, momentsAt))

	waitFor(t, "the moment's colour", func() bool { return painted(strip, render.ColourError) })

	// A minute of a quiet clock changes nothing: the moment has no deadline of its own.
	clock.Advance(time.Minute)
	time.Sleep(30 * time.Millisecond)

	if !painted(strip, render.ColourError) {
		t.Fatal("a moment with no length ended on its own")
	}

	// The state moves, and that is what ends it: the steady idle picture is painted.
	//
	// The order matters. The writer looks again on its own next repeat — a hold with no deadline wakes
	// once a second, which is what keeping a pushed picture alive costs anyway — so the clock has to be
	// moved past one of them *after* the driver has taken the new state up. On a real clock the second
	// passes by itself; here the test owns it.
	run.source.show(snapshotOf(printer.StateIdle, true, false, momentsAt.Add(2*time.Minute)))

	waitFor(t, "the driver to be showing the idle state", func() bool {
		standing := pen.standing.Load()

		return standing != nil && standing.state == printer.StateIdle
	})

	clock.Advance(2 * time.Second)

	waitFor(t, "the steady idle picture", func() bool {
		calls := strip.shown()

		return len(calls) > 0 && calls[len(calls)-1].colour == render.ColourIdle
	})
}

// TestAPrinterThatGoesQuietFiresItsMoment is the one moment that is not a change of the state word: the
// reading goes stale while a job is live, which is what "the printer stopped reporting" means. A printer
// whose last word says the job is over is not quiet about a job, so that case fires nothing.
func TestAPrinterThatGoesQuietFiresItsMoment(t *testing.T) {
	strip := newPanelStrip()
	pen := newWriter(strip, 128, quiet)

	clock := newFakeClock(momentsAt)
	clock.watch(pen)
	pushInto(pen)

	entry := momentEntry{Enabled: true, Shows: showsColour, Which: printer.StateError, For: "30s"}
	table := momentTableFor(momentPrinterWentQuiet, entry)

	run := runMoments(t, pen, table, strip, snapshotOf(printer.StatePrinting, true, false, momentsAt))

	waitFor(t, "the printing frame", func() bool { return painted(strip, render.ColourPrinting) })

	// The printer says nothing for a minute: the same state word, a stale reading.
	run.source.show(snapshotOf(printer.StatePrinting, true, true, momentsAt))

	waitFor(t, "the quiet moment's colour", func() bool { return painted(strip, render.ColourError) })

	// And a printer whose last word is FINISHED going quiet fires nothing: a job that is over is a fact
	// silence cannot retract, and the light keeps its finished green.
	strip2 := newPanelStrip()
	pen2 := newWriter(strip2, 128, quiet)

	clock2 := newFakeClock(momentsAt)
	clock2.watch(pen2)
	pushInto(pen2)

	run2 := runMoments(t, pen2, table, strip2, snapshotOf(printer.StateFinished, true, false, momentsAt))

	waitFor(t, "the finished frame", func() bool { return painted(strip2, render.ColourFinished) })

	run2.source.show(snapshotOf(printer.StateFinished, true, true, momentsAt))

	time.Sleep(50 * time.Millisecond)

	if painted(strip2, render.ColourError) {
		t.Fatal("a finished job going quiet fired the quiet moment's picture")
	}
}
