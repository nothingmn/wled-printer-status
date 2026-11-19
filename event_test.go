package main

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nothingmn/wled-printer-status/internal/printer"
	"github.com/nothingmn/wled-printer-status/internal/render"
	"github.com/nothingmn/wled-printer-status/internal/text"
	"github.com/nothingmn/wled-printer-status/internal/wled"
)

// The event layer, tested where it can be: the decision is a pure function, the word is pixels
// this repository owns, and the hold is a deadline on a clock the test moves. Nothing here waits
// a minute, and nothing here knows a controller's address.

// counted is the part of a fake controller a test counts requests on, so that both the flat
// fakeStrip and the panelStrip built on it can be handed to the wait below.
type counted interface {
	wrote() int
}

// waitForWrites waits until the controller has taken count requests of any kind — a frame, the
// body that establishes the panel, or a chunk of the word's pixels — and fails the test if it
// never does.
func waitForWrites(t *testing.T, strip counted, count int) {
	t.Helper()

	waitFor(t, fmt.Sprintf("the controller to take %d requests", count), func() bool { return strip.wrote() >= count })
}

// waitFor polls until ok holds, and fails the test with what was being waited for when it never
// does. It is how a test waits for a goroutine's side of a write without a sleep long enough to
// be a guess.
func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()

	deadline := time.Now().Add(howLong)

	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("waited %s for %s", howLong, what)
		}

		time.Sleep(time.Millisecond)
	}
}

// paintings is how many times the word was laid on the panel: the body that establishes the
// panel carries the word as the segment's own name, and it goes out once per painting however
// many requests the pixels then take. A test about "one event, not twenty" counts this rather
// than requests, because how many requests a picture costs is the client's chunk arithmetic.
func paintings(strip *panelStrip) int {
	count := 0

	for _, call := range strip.shown() {
		if len(call.segments) > 0 && call.segments[0].Name != "" {
			count++
		}
	}

	return count
}

// wordPixels is every pixel the panel was last given in the finished green, which for this event
// is the word and nothing else.
//
// It reads the picture the module pushed rather than a JSON body: the panel draws no per-LED body
// on this firmware — see pixels.go — so the pixels are a DDP frame now, and a test that read a
// "i" array would be reading a body nothing draws.
func wordPixels(pushed *pushedFrames) []int {
	frame := pushed.last()

	lit := make([]int, 0, len(frame))

	for index, colour := range frame {
		if colour == render.ColourFinished {
			lit = append(lit, index)
		}
	}

	return lit
}

// steadyPaints is every write that carried a frame rather than the word. The word's own first
// request lays a rectangle down too — the whole panel in the background colour — and it names
// the segment, which is how a test tells it from the steady picture it is standing in front of.
func steadyPaints(strip *panelStrip) []paintCall {
	frames := make([]paintCall, 0, len(strip.shown()))

	for _, call := range strip.shown() {
		if len(call.segments) > 0 && call.segments[0].Name != "" {
			continue
		}

		frames = append(frames, call)
	}

	return frames
}

// TestTheEventListIsATransitionAndTheFinishedWordHoldsForItsConstant is the decision itself:
// (from, to, geometry) to (what the moment table says, which is the word) or nothing.
//
// It is the *default* table — the code's own rows, with no store to ask — which is what an install
// that has never opened the Events screen acts on, and what this module did before the table drove
// anything. The finished moment's default is the word FINISHED in the finished green for a minute:
// the same picture, in the same colour, for the same hold as the special case this replaces.
//
// The cases that produce nothing are the ones that would be wrong in a way nobody would notice on
// the light: a pair that is not a transition at all — which includes a restart's baseline, a state
// that stood still, and a pause that ends in a finish — and the moments whose default is off, which
// is every one of the eight but this.
func TestTheEventListIsATransitionAndTheFinishedWordHoldsForItsConstant(t *testing.T) {
	if finishedEventHolds != time.Minute {
		t.Fatalf("the finished event holds for %s, and this build says a minute", finishedEventHolds)
	}

	// The default entry's own `for` is the same minute, which is what makes the table the statement of
	// the behaviour rather than a second copy of it.
	holds, finite, err := momentLength(momentDefault(momentPrintFinished).For)
	if err != nil || !finite || holds != finishedEventHolds {
		t.Fatalf("the finished moment's default holds for %q (%s, finite %v, %v), want the %s the event holds",
			momentDefault(momentPrintFinished).For, holds, finite, err, finishedEventHolds)
	}

	ev, ok := defaultMomentEvent(printer.StatePrinting, printer.StateFinished, livePanel, render.ColourFinished)
	if !ok {
		t.Fatal("PRINTING to FINISHED on a panel produced no event")
	}

	if ev.word != "FINISHED" {
		t.Fatalf("the finished event spells %q, want FINISHED", ev.word)
	}

	if ev.colour != render.ColourFinished {
		t.Fatalf("the finished event is drawn in %+v, want the finished colour %+v", ev.colour, render.ColourFinished)
	}

	if ev.holds != finishedEventHolds {
		t.Fatalf("the finished event holds for %s, want %s", ev.holds, finishedEventHolds)
	}

	for _, shape := range []struct {
		name string
		from string
		to   string
	}{
		{name: "a pause that ends in a finish", from: printer.StatePaused, to: printer.StateFinished},
		{name: "a state that stood still", from: printer.StateFinished, to: printer.StateFinished},
		{name: "a print beginning, whose moment is off by default", from: printer.StateIdle, to: printer.StatePrinting},
		{name: "the first word a process saw", from: "", to: printer.StateFinished},
		{name: "a job stopped by a person, whose moment is off by default", from: printer.StatePrinting, to: printer.StateStopped},
		{name: "a printer going into error, whose moment is off by default", from: printer.StatePrinting, to: printer.StateError},
	} {
		t.Run(shape.name, func(t *testing.T) {
			if _, ok := defaultMomentEvent(shape.from, shape.to, livePanel, render.ColourFinished); ok {
				t.Fatalf("%s to %s produced an event, want none", shape.from, shape.to)
			}
		})
	}
}

// TestAWordMomentOnALightThatCannotCarryOneShowsItsColour is the locked fallback, and the one place
// where a moment the table *does* fire is answered with no word: a strip has no second dimension for
// text, and a panel too small for the word at the scale this build draws it has no room. Both are
// given the moment's colour instead, which on a strip is the same picture the steady frame paints —
// so the light does not change, and it costs one body rather than a failed write and a widening retry.
func TestAWordMomentOnALightThatCannotCarryOneShowsItsColour(t *testing.T) {
	for _, shape := range []struct {
		name     string
		geometry panel
	}{
		{name: "a strip"},
		{name: "a panel 32 rows tall, which cannot hold a 35-pixel word", geometry: panel{width: 256, height: 32}},
		{name: "a panel 234 columns wide, which is one column short", geometry: panel{width: 234, height: 64}},
	} {
		t.Run(shape.name, func(t *testing.T) {
			ev, ok := defaultMomentEvent(printer.StatePrinting, printer.StateFinished, shape.geometry, render.ColourFinished)
			if !ok {
				t.Fatalf("%s produced no event, and the finished moment is on", shape.name)
			}

			if ev.word != "" {
				t.Fatalf("the event spells %q on a light that cannot carry it", ev.word)
			}

			if ev.plain == nil || ev.plain.Colour != render.ColourFinished {
				t.Fatalf("the event is %+v, want the moment's colour %+v as the whole picture", ev, render.ColourFinished)
			}
		})
	}
}

// TestAnEventHoldsTheLightForItsOwnDurationAndNoLonger is the override and its return in one
// test, either side of the boundary.
//
// Before the duration is up, the steady frame the driver keeps deciding waits in the slot and is
// not written: the light is holding the word. At the duration the frame goes out, and the word is
// not painted again — the panel is back to the picture it would have shown if the event had never
// existed.
func TestAnEventHoldsTheLightForItsOwnDurationAndNoLonger(t *testing.T) {
	strip := newPanelStrip()

	clock := newFakeClock(time.Date(2026, time.November, 18, 22, 0, 0, 0, time.UTC))
	pen := newWriter(strip, 128, quiet)
	pen.setPanel(strip.info)
	clock.watch(pen)

	pushed := pushInto(pen)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go pen.run(ctx)

	// The frame the transition's own tick decides: the whole panel in the finished green.
	steady := render.Frame{Colour: render.ColourFinished, Total: 16384, Reason: render.ReasonNoLiveJob}

	pen.submit(steady)
	strip.waitForPaints(t, 1)
	clock.waitArmed(t, 0)

	// The printer finished. The panel is established and then given the word, in as many requests
	// as the pixels take.
	mark := clock.mark()
	pen.fire(finishedWordEvent())

	waitFor(t, "the word's pixels to reach the panel", func() bool { return len(wordPixels(pushed)) > 0 })

	want, err := text.Raster(finishedWord, finishedWordScale, text.Geometry{Width: 256, Height: 64})
	if err != nil {
		t.Fatalf("rasterise %q: %v", finishedWord, err)
	}

	if got := wordPixels(pushed); !slices.Equal(got, want) {
		t.Fatalf("the panel was given %d green pixels, want the word's %d", len(got), len(want))
	}

	clock.waitArmed(t, mark)

	// The driver's next tick decides the same steady frame, and it must not be written: the word
	// is holding the light.
	mark = clock.mark()
	pen.submit(steady)
	clock.waitArmed(t, mark)

	if shown := steadyPaints(strip); len(shown) != 1 {
		t.Fatalf("the steady frame was written %d times during the event, want the 1 before it: %+v", len(shown), shown)
	}

	// One second short of the hold.
	clock.Advance(finishedEventHolds - time.Second)
	time.Sleep(20 * time.Millisecond)

	if shown := steadyPaints(strip); len(shown) != 1 {
		t.Fatalf("the steady frame was written %d times one second before the hold ended, want 1: %+v", len(shown), shown)
	}

	// The hold ends. The frame the writer is holding goes out, which is a moment the test moves to
	// rather than a moment it waits for.
	clock.Advance(time.Second)

	waitFor(t, "the steady frame to come back at the end of the hold", func() bool {
		return len(steadyPaints(strip)) == 2
	})

	shown := steadyPaints(strip)
	if len(shown) != 2 {
		t.Fatalf("the light was written %d frames, want 2: %+v", len(shown), shown)
	}

	back := shown[1].segments
	if len(back) != 2 || !back[0].On || back[0].Count != 256 || back[0].RowCount != 64 {
		t.Fatalf("the frame after the event is %+v, want the whole panel", back)
	}

	if back[0].Colour != render.ColourFinished || back[1].On {
		t.Fatalf("the frame after the event is %+v, want the finished colour and no remainder", back)
	}

	if got := paintings(strip); got != 1 {
		t.Fatalf("the word was laid down %d times, want once", got)
	}
}

// TestTheFrameThatEndsTheEventClearsTheWordFromTheSegmentName is the debt the first live event
// left behind: the word is a segment name while it holds, and on this firmware a body that does
// not mention "n" keeps the name it found. So the frame that takes the light back has to say
// "no name" explicitly — an empty string, not an omitted key — or the panel carries
// "FINISHED" until something else names the segment.
//
// Nothing draws a segment's name while the steady frame holds a solid colour, which is why this
// was invisible on the panel. It would not stay invisible: anything that set a text effect on
// that segment later would draw the last print's word over a picture about the current one.
//
// It costs no request. The key rides in the body the writer was already sending.
func TestTheFrameThatEndsTheEventClearsTheWordFromTheSegmentName(t *testing.T) {
	strip := newPanelStrip()

	clock := newFakeClock(time.Date(2026, time.November, 18, 22, 0, 0, 0, time.UTC))
	pen := newWriter(strip, 128, quiet)
	pen.setPanel(strip.info)
	clock.watch(pen)

	pushInto(pen)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go pen.run(ctx)

	steady := render.Frame{Colour: render.ColourFinished, Total: 16384, Reason: render.ReasonNoLiveJob}

	pen.submit(steady)
	strip.waitForPaints(t, 1)

	// The word goes up: the establishing body names the segment, because /json/state is where a
	// person reads which picture the panel is holding and a datagram can carry no string.
	pen.fire(finishedWordEvent())

	waitFor(t, "the word to be laid on the panel", func() bool { return paintings(strip) == 1 })

	named := steadyPaints(strip)
	if len(named) != 1 {
		t.Fatalf("the panel was written %d frames before the word, want 1: %+v", len(named), named)
	}

	// The establishing body is the painting, and it is told apart from a frame by its name.
	for _, call := range strip.shown() {
		if len(call.segments) > 0 && call.segments[0].Name == finishedWord && call.segments[0].ClearName {
			t.Fatalf("the body that put the word up also cleared the name it was setting: %+v", call.segments[0])
		}
	}

	// The hold ends, and the frame that takes the light back is the newest thing the driver
	// decided — the whole panel in the finished green, with no name on it. It is submitted here
	// because that is what the driver does every tick: the writer's slot holds the newest frame
	// for the length of the hold and writes it when the hold is over.
	pen.submit(steady)
	clock.Advance(finishedEventHolds)

	waitFor(t, "the steady frame to come back at the end of the hold", func() bool {
		return len(steadyPaints(strip)) == 2
	})

	back := steadyPaints(strip)[1].segments
	if len(back) != 2 {
		t.Fatalf("the frame after the event carries %d segments, want 2: %+v", len(back), back)
	}

	if back[0].Name != "" {
		t.Fatalf("the frame after the event names the segment %q, want no name", back[0].Name)
	}

	if !back[0].ClearName {
		t.Fatalf("the frame after the event does not clear the segment's name, so %q outlives the event: %+v",
			finishedWord, back[0])
	}

	// Only the segment the word was named on. Segment 1 is never named by any body this module
	// writes, so a clear there would be a claim about state it does not have.
	if back[1].ClearName {
		t.Fatalf("the remainder segment is told to clear a name it never had: %+v", back[1])
	}
}

// TestAStripIsGivenTheFinishedMomentsColourAndNoWord is the strip half of the rule, through the driver
// rather than through the decision: a 48-LED strip that finishes a print is written the solid green it
// was already being written, and nothing else.
//
// A word is drawn in two dimensions and a strip has one, so §2.3's fallback is the moment's colour —
// which on this moment is the same finished green the steady frame carries. The picture on the strip
// is therefore exactly what it was before the moment existed; what the moment adds is one body, not an
// establishment and not pixels, because there is nothing a strip cannot answer here.
func TestAStripIsGivenTheFinishedMomentsColourAndNoWord(t *testing.T) {
	source := &changingReadings{}
	strip := newFakeStrip()

	var written logs

	pen := newWriter(strip, 128, quiet)
	reader := newDriver(source, pen, strip, written.logf)
	reader.leds = 48

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	at := time.Date(2026, time.November, 18, 22, 0, 0, 0, time.UTC)

	source.show(snapshotOf(printer.StatePrinting, true, false, at))
	reader.step(ctx, at)

	source.show(snapshotOf(printer.StateFinished, true, false, at.Add(tickEvery)))
	reader.step(ctx, at.Add(tickEvery))

	// What was offered to the writer is a colour and not a word: nothing to establish, nothing to push.
	//
	// The writer's goroutine is started below this read, and not before it. The slot this reads holds
	// the event only until the writer takes it — takeEvent swaps it to nil — so a writer already
	// running can empty it between the driver's turn and this line, which reads exactly like an event
	// that was never fired. That is the flake this test carried: it failed about one full -race run in
	// four, at HEAD as well as here, and passed every time in isolation. Nothing about the picture
	// changed: the driver stores the scene and the event whether or not the writer is running, and
	// the writer takes both when it starts.
	fired := pen.fired.Load()
	if fired == nil {
		t.Fatal("a strip was offered no event, and the finished moment's colour is what it shows")
	}

	if fired.word != "" || fired.plain == nil {
		t.Fatalf("a strip was offered the %q event, want the moment's colour as a plain picture", fired.word)
	}

	if fired.plain.Colour != render.ColourFinished {
		t.Fatalf("the strip was offered %+v, want the finished green", fired.plain)
	}

	go pen.run(ctx)

	waitFor(t, "the finished frame to reach the strip", func() bool {
		shown := strip.shown()

		return len(shown) > 0 && shown[len(shown)-1].segments[0].Colour == render.ColourFinished
	})

	shown := strip.shown()
	solid := shown[len(shown)-1].segments[0]
	if solid.Colour != render.ColourFinished || solid.Count != 48 || solid.RowCount != 0 {
		t.Fatalf("the finished frame on a strip is %+v, want 48 pixels in the finished colour with no rows", solid)
	}

	// The log still carries the transition, and now also what the moment did with it: the light is what
	// it shows, the history is what changed, and both come from the same comparison.
	written.want(t,
		"wled-printer-status: the printer Morpheus went from PRINTING to FINISHED",
		"wled-printer-status: the print-finished moment shows its colour for 60s, because this light cannot carry the word \"FINISHED\"")
}

// TestABorrowedSegmentIsNeverGivenTheEvent is the suppression, and it is tested where the two
// halves of the rule could disagree: the writer is pointed at a segment *and* at a controller
// that reports a matrix, which is the configuration in which the geometry alone would have
// allowed a word. The event stops at the writer's door instead.
//
// The reason is not squeamishness about somebody else's light. This module was pointed at one
// zone of a controller to show a colour in, it writes no brightness and no power there, and an
// animation nobody asked for is the one thing it could do that its owner would not have agreed
// to — so the answer is nothing, said once, and the steady frame carries on.
func TestABorrowedSegmentIsNeverGivenTheEvent(t *testing.T) {
	strip := newPanelStrip()
	strip.state = wled.State{
		On:         true,
		Brightness: 255,
		Segments: []wled.Segment{{
			ID:     2,
			Start:  14,
			Stop:   24,
			On:     true,
			Colors: []wled.RGB{{R: 8, G: 255, B: 0}},
			Effect: 102,
			Name:   "Front 1",
		}},
	}

	var written logs

	pen := newSegmentWriter(strip, 2, "http://10.0.0.253", written.logf)
	pen.setPanel(strip.info)

	ctx, cancel := context.WithCancel(context.Background())

	go pen.run(ctx)

	// The borrow is one line, and it is the whole of what the writer has said so far.
	written.waitForCount(t, 1)

	pen.fire(finishedWordEvent())
	written.waitForCount(t, 2)

	if writes := strip.wrote(); writes != 0 {
		t.Fatalf("the event wrote %d requests to a borrowed segment, want none", writes)
	}

	lines := written.all()
	if !strings.Contains(lines[1], "was not drawn") || !strings.Contains(lines[1], "FINISHED") {
		t.Fatalf("the suppression line is %q, want it to say the event was not drawn and which one", lines[1])
	}

	// And the segment is still given back on the way out, which is the promise the borrow mode
	// exists for: an event that changed nothing must not have changed what is handed back.
	cancel()

	waitFor(t, "the borrowed segment to be given back", func() bool { return len(strip.givenBack()) > 0 })

	if given := strip.givenBack(); len(given) != 1 || given[0].Name != "Front 1" {
		t.Fatalf("the segment was given back as %+v, want the one segment it borrowed", given)
	}
}

// TestARestartIsNotATransition is the invariant a process that restarts hourly would break: the
// first snapshot is a baseline, so a module that comes back up beside a printer which has already
// finished shows the steady finished panel and never the word. A restart is not something the
// printer did.
func TestARestartIsNotATransition(t *testing.T) {
	at := time.Date(2026, time.November, 18, 22, 0, 0, 0, time.UTC)
	ctx := context.Background()

	// The process starts while the print is already finished. Its first look is a baseline.
	source := &changingReadings{}
	strip := newPanelStrip()

	var written logs

	pen := newWriter(strip, 128, quiet)
	pen.setPanel(strip.info)

	reader := newDriver(source, pen, strip, written.logf)
	reader.leds = 16384

	source.show(snapshotOf(printer.StateFinished, true, false, at))
	reader.step(ctx, at)

	if fired := pen.fired.Load(); fired != nil {
		t.Fatalf("a restart beside a finished print fired the %q event", fired.word)
	}

	if painted := paintings(strip); painted != 0 {
		t.Fatalf("a restart fired an event: %d paintings", painted)
	}

	written.want(t)

	// The same thing with a process that saw the print running and then restarted: the new
	// driver's first reading is FINISHED, which is a reading and not a transition. A second writer
	// and a second driver on the same controller are what a restart is.
	restartedPen := newWriter(strip, 128, quiet)
	restartedPen.setPanel(strip.info)

	restarted := newDriver(source, restartedPen, strip, quiet)
	restarted.leds = 16384

	source.show(snapshotOf(printer.StateFinished, true, false, at.Add(tickEvery)))
	restarted.step(ctx, at.Add(tickEvery))

	if fired := restartedPen.fired.Load(); fired != nil {
		t.Fatalf("a restart fired the %q event, want none", fired.word)
	}
}

// TestTwentyIdenticalStatesFireOneEventAndOnlyOnTheTransition is the count both ways round: a
// printer that says PRINTING for twenty turns fires nothing, the turn it says FINISHED fires one
// event, and the twenty turns after that fire none. The event is a function of the transition, so
// it can happen once per transition — not once per look.
func TestTwentyIdenticalStatesFireOneEventAndOnlyOnTheTransition(t *testing.T) {
	source := &changingReadings{}
	strip := newPanelStrip()

	var written logs

	pen := newWriter(strip, 128, quiet)
	pen.setPanel(strip.info)
	pushInto(pen)

	reader := newDriver(source, pen, strip, written.logf)
	reader.leds = 16384

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go pen.run(ctx)

	at := time.Date(2026, time.November, 18, 22, 0, 0, 0, time.UTC)

	source.show(snapshotOf(printer.StatePrinting, true, false, at))
	reader.step(ctx, at)

	for i := 1; i <= 20; i++ {
		reader.step(ctx, at.Add(time.Duration(i)*tickEvery))
	}

	if got := paintings(strip); got != 0 {
		t.Fatalf("twenty turns of PRINTING laid the word down %d times, want no event", got)
	}

	source.show(snapshotOf(printer.StateFinished, true, false, at))
	reader.step(ctx, at.Add(21*tickEvery))

	waitFor(t, "the finished word to reach the panel", func() bool { return paintings(strip) == 1 })

	if got := paintings(strip); got != 1 {
		t.Fatalf("the transition fired %d events, want 1", got)
	}

	for i := 22; i <= 42; i++ {
		reader.step(ctx, at.Add(time.Duration(i)*tickEvery))
	}

	if got := paintings(strip); got != 1 {
		t.Fatalf("twenty turns of FINISHED fired the event %d times, want once", got)
	}
}

// TestAFailedEventWriteIsLoggedOncePerWideningRetry is the failure half, and it is the same rule a
// failed frame follows: an event that did not reach the controller is reported, retried inside
// the backoff window, survived, and never turned into a line per tick.
//
// The count is the assertion. A controller that is off for twenty seconds costs five attempts —
// one a second, doubling — and five lines; it must not cost twenty lines, which is what the
// installed module once wrote into the operator's log.
func TestAFailedEventWriteIsLoggedOncePerWideningRetry(t *testing.T) {
	strip := newPanelStrip()

	clock := newFakeClock(time.Date(2026, time.November, 18, 22, 0, 0, 0, time.UTC))

	var written logs

	pen := newWriter(strip, 128, written.logf)
	pen.setPanel(strip.info)
	clock.watch(pen)

	pushed := pushInto(pen)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go pen.run(ctx)

	// One steady frame lands before the controller goes quiet, so the event is the first thing
	// that fails rather than the frame.
	pen.submit(render.Frame{Colour: render.ColourPrinting, Total: 16384, Reason: render.ReasonProgress})
	strip.waitForPaints(t, 1)
	clock.waitArmed(t, 0)

	strip.stopAnswering()

	pen.fire(finishedWordEvent())
	waitForWrites(t, strip, 2)

	// Twenty seconds of a controller that will not answer.
	for i := 0; i < 20; i++ {
		clock.Advance(time.Second)
		time.Sleep(2 * time.Millisecond)
	}

	lines := written.all()

	if len(lines) > 6 {
		t.Fatalf("a twenty-second outage wrote %d lines, want one per widening retry (at most 6): %q", len(lines), lines)
	}

	if !strings.Contains(strings.Join(lines, "\n"), "not updated") {
		t.Fatalf("the failed event was not logged as a failed write: %q", lines)
	}

	if ctx.Err() != nil {
		t.Fatal("the writer stopped on a failed event, and a module that exits is a module that needs a person")
	}

	// The controller answers again, the window elapses, and the word goes out — the whole word,
	// not the part of it that was in flight when the light went quiet. Every attempt that failed
	// failed on its first request, so no pixel of the word had reached the panel before this.
	want, err := text.Raster(finishedWord, finishedWordScale, text.Geometry{Width: 256, Height: 64})
	if err != nil {
		t.Fatalf("rasterise %q: %v", finishedWord, err)
	}

	if lit := wordPixels(pushed); len(lit) != 0 {
		t.Fatalf("%d pixels of the word reached a controller that was not answering", len(lit))
	}

	strip.answerAgain()

	time.Sleep(5 * time.Millisecond)
	clock.Advance(backoffCap)

	waitFor(t, "the recovered event to lay the word on the panel", func() bool {
		return len(wordPixels(pushed)) == len(want)
	})

	if got := wordPixels(pushed); !slices.Equal(got, want) {
		t.Fatalf("the recovered event left %d green pixels, want the word's %d", len(got), len(want))
	}
}
