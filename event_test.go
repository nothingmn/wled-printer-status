package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nothingmn/wled-printer-status/internal/printer"
	"github.com/nothingmn/wled-printer-status/internal/render"
	"github.com/nothingmn/wled-printer-status/internal/wled"
)

// The event layer, tested where it can be: the decision is a pure function, and the hold is a
// deadline on a clock the test moves. Nothing here waits a minute, and nothing here knows a
// controller's address.

// finishedEvent is the event a completed print stands for, as the tests name it.
func finishedEvent() event {
	return event{word: finishedWord, colour: render.ColourFinished, holds: finishedEventHolds}
}

// counted is the part of a fake controller a test counts requests on, so that both the flat
// fakeStrip and the panelStrip built on it can be handed to the wait below.
type counted interface {
	wrote() int
}

// waitForWrites waits until the controller has taken count requests of any kind — a frame, an
// effect or a name — and fails the test if it never does. A test about a word cannot count only
// frames: the word is two requests that are not frames.
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

// steadyPaints is every write that carried a frame rather than a word. An event's first request
// lays a rectangle down too — it is the whole panel in the event's colour — and it is written with
// the word as its name, which is how a test tells it from the steady picture it is standing in
// front of.
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
// (from, to, geometry) to (word, colour, duration), or nothing.
//
// The cases that return nothing are the ones that would be wrong in a way nobody would notice
// on the light: a strip, which cannot draw a word at all; a pair that is not a transition, which
// includes a restart's baseline and a state that stood still; and a pause that ends in a finish,
// which is a pair nobody has decided about.
func TestTheEventListIsATransitionAndTheFinishedWordHoldsForItsConstant(t *testing.T) {
	if finishedEventHolds != time.Minute {
		t.Fatalf("the finished event holds for %s, and this build says a minute", finishedEventHolds)
	}

	ev, ok := eventFor(printer.StatePrinting, printer.StateFinished, livePanel)
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
		name     string
		from     string
		to       string
		geometry panel
	}{
		{name: "a strip", from: printer.StatePrinting, to: printer.StateFinished},
		{name: "a pause", from: printer.StatePrinting, to: printer.StatePaused, geometry: livePanel},
		{name: "a pause that ends in a finish", from: printer.StatePaused, to: printer.StateFinished, geometry: livePanel},
		{name: "a state that stood still", from: printer.StateFinished, to: printer.StateFinished, geometry: livePanel},
		{name: "a print beginning", from: printer.StateIdle, to: printer.StatePrinting, geometry: livePanel},
		{name: "the first word a process saw", from: "", to: printer.StateFinished, geometry: livePanel},
		{name: "a job stopped by a person", from: printer.StatePrinting, to: printer.StateStopped, geometry: livePanel},
	} {
		t.Run(shape.name, func(t *testing.T) {
			if _, ok := eventFor(shape.from, shape.to, shape.geometry); ok {
				t.Fatalf("%s to %s produced an event, want none", shape.from, shape.to)
			}
		})
	}
}

// TestTheFinishedEventBodyIsTheWordOnThePanel is the three requests a panel receives, quoted as
// bytes: the whole panel in the finished green with the word as the segment's name, then the
// effect with every one of its parameters, then the word again — last, because the request
// before it is a body that writes the segment without a name, and a controller clears a name
// that way. The order is the measurement, so the test pins the order.
func TestTheFinishedEventBodyIsTheWordOnThePanel(t *testing.T) {
	recorder := &wireCapture{}
	server := recorder.server(t)

	light, err := wled.New(server.URL)
	if err != nil {
		t.Fatalf("wled.New: %v", err)
	}

	pen := newWriter(light, 128, quiet)
	pen.setPanel(wled.Info{LEDCount: 16384, MatrixWidth: 256, MatrixHeight: 64})

	if err := pen.paintEvent(context.Background(), finishedEvent()); err != nil {
		t.Fatalf("paint the finished event: %v", err)
	}

	want := []string{
		// The panel, the finished green, and the word as the segment's own name.
		`{"seg":[{"id":0,"start":0,"stop":256,"startY":0,"stopY":64,"on":true,"col":[[0,170,0]],"fx":0,"n":"FINISHED"}]}`,
		// Scrolling Text with every parameter named, zeros included, over the same rectangle,
		// with a grouping that multiplies the glyphs four times.
		`{"seg":[{"id":0,"start":0,"stop":256,"startY":0,"stopY":64,"on":true,` +
			`"fx":122,"sx":128,"ix":24,"c1":0,"c2":0,"c3":0,"o1":0,"o2":0,"o3":0,"pal":0,"grp":4}]}`,
		// The word again, last: the request above carries no "n" and would have cleared it.
		`{"seg":[{"id":0,"n":"FINISHED"}]}`,
	}

	if len(recorder.bodies) != len(want) {
		t.Fatalf("the event was painted in %d requests, want %d: %v", len(recorder.bodies), len(want), recorder.bodies)
	}

	for i := range want {
		if recorder.bodies[i] != want[i] {
			t.Fatalf("request %d is\n%s\nwant\n%s", i+1, recorder.bodies[i], want[i])
		}
	}
}

// TestAnEventHoldsTheLightForItsOwnDurationAndNoLonger is the override and its return in one
// test, either side of the boundary.
//
// Before the duration is up, the steady frame the driver keeps deciding waits in the slot and is
// not written: the light is holding the word. At the duration the frame goes out, and the word
// is not written again — the panel is back to the picture it would have shown if the event had
// never existed.
func TestAnEventHoldsTheLightForItsOwnDurationAndNoLonger(t *testing.T) {
	strip := newPanelStrip()

	clock := newFakeClock(time.Date(2026, time.November, 18, 22, 0, 0, 0, time.UTC))
	pen := newWriter(strip, 128, quiet)
	pen.setPanel(strip.info)
	clock.watch(pen)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go pen.run(ctx)

	// The frame the transition's own tick decides: the whole panel in the finished green.
	steady := render.Frame{Colour: render.ColourFinished, Total: 16384, Reason: render.ReasonNoLiveJob}

	pen.submit(steady)
	strip.waitForPaints(t, 1)
	clock.waitArmed(t, 0)

	// The printer finished. One frame, one effect and one name reach the controller.
	mark := clock.mark()
	pen.fire(finishedEvent())
	waitForWrites(t, strip, 4)
	clock.waitArmed(t, mark)

	if ran := strip.ran(); len(ran) != 1 || ran[0].ID != scrollingTextEffect {
		t.Fatalf("the panel was put onto %+v, want one effect, id %d", ran, scrollingTextEffect)
	}

	if words := strip.words(); len(words) != 1 || words[0] != "FINISHED" {
		t.Fatalf("the panel was given the words %q, want one FINISHED", words)
	}

	// The driver's next tick decides the same steady frame, and it must not be written: the
	// word is holding the light.
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

	// The hold ends. The frame in the slot goes out, and it is the steady finished picture.
	clock.Advance(time.Second)

	// The frame the writer is holding goes out at the end of the hold, which is a moment the test
	// moves to rather than a moment it waits for.
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

	if words := strip.words(); len(words) != 1 {
		t.Fatalf("the word was written again after the event: %q", words)
	}
}

// TestAStripGetsNoEventAndKeepsItsSolidFrame is the strip half of the rule, through the driver
// rather than through the decision: a 48-LED strip that finishes a print is written the solid
// green it was already being written, and nothing else.
//
// The reason is the firmware's: the text effect needs a matrix, and on a strip it falls back to
// a solid colour — so the three requests would cost a controller three writes and put no word
// anywhere. The operator asked for the strip to keep its green, and that is what it does.
func TestAStripGetsNoEventAndKeepsItsSolidFrame(t *testing.T) {
	source := &changingReadings{}
	strip := newFakeStrip()

	var written logs

	pen := newWriter(strip, 128, quiet)
	reader := newDriver(source, pen, strip, written.logf)
	reader.leds = 48

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go pen.run(ctx)

	at := time.Date(2026, time.November, 18, 22, 0, 0, 0, time.UTC)

	source.show(snapshotOf(printer.StatePrinting, true, false, at))
	reader.step(ctx, at)

	source.show(snapshotOf(printer.StateFinished, true, false, at.Add(tickEvery)))
	reader.step(ctx, at.Add(tickEvery))

	// Nothing was even offered to the writer: the decision refused it, so the writer has no
	// event in its hand and no request to make.
	if fired := pen.fired.Load(); fired != nil {
		t.Fatalf("a strip was offered the %q event, want none", fired.word)
	}

	if ran := strip.ran(); len(ran) != 0 {
		t.Fatalf("a strip was put onto the effects %+v, want none", ran)
	}

	if words := strip.words(); len(words) != 0 {
		t.Fatalf("a strip was given the words %q, want none", words)
	}

	waitFor(t, "the finished frame to reach the strip", func() bool {
		shown := strip.shown()

		return len(shown) > 0 && shown[len(shown)-1].segments[0].Colour == render.ColourFinished
	})

	shown := strip.shown()
	solid := shown[len(shown)-1].segments[0]
	if solid.Colour != render.ColourFinished || solid.Count != 48 || solid.RowCount != 0 {
		t.Fatalf("the finished frame on a strip is %+v, want 48 pixels in the finished colour with no rows", solid)
	}

	// The log still carries the transition. The event is what the light shows, not what the
	// history records, and both come from the same comparison.
	written.want(t, "wled-printer-status: the printer Morpheus went from PRINTING to FINISHED")
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

	pen.fire(finishedEvent())
	written.waitForCount(t, 2)

	if ran := strip.ran(); len(ran) != 0 {
		t.Fatalf("a borrowed segment was put onto the effects %+v, want none", ran)
	}

	if words := strip.words(); len(words) != 0 {
		t.Fatalf("a borrowed segment was given the words %q, want none", words)
	}

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
// first snapshot is a baseline, so a module that comes back up beside a printer which has
// already finished shows the steady finished panel and never the word. A restart is not
// something the printer did.
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

	if ran := strip.ran(); len(ran) != 0 || len(strip.words()) != 0 {
		t.Fatalf("a restart fired an event: %+v %q", strip.ran(), strip.words())
	}

	written.want(t)

	// The same thing with a process that saw the print running and then restarted: the new
	// driver's first reading is FINISHED, which is a reading and not a transition. A second
	// writer and a second driver on the same controller are what a restart is.
	restartedPen := newWriter(strip, 128, quiet)
	restartedPen.setPanel(strip.info)

	restarted := newDriver(source, restartedPen, strip, quiet)
	restarted.leds = 16384

	source.show(snapshotOf(printer.StateFinished, true, false, at.Add(tickEvery)))
	restarted.step(ctx, at.Add(tickEvery))

	if fired := restartedPen.fired.Load(); fired != nil {
		t.Fatalf("a restart fired the %q event, want none", fired.word)
	}

	if ran := strip.ran(); len(ran) != 0 {
		t.Fatalf("a restart after a print fired an event: %+v", ran)
	}
}

// TestTwentyIdenticalStatesFireOneEventAndOnlyOnTheTransition is the count both ways round: a
// printer that says PRINTING for twenty turns fires nothing, the turn it says FINISHED fires
// one event, and the twenty turns after that fire none. The event is a function of the
// transition, so it can happen once per transition — not once per look.
func TestTwentyIdenticalStatesFireOneEventAndOnlyOnTheTransition(t *testing.T) {
	source := &changingReadings{}
	strip := newPanelStrip()

	var written logs

	pen := newWriter(strip, 128, quiet)
	pen.setPanel(strip.info)

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

	if ran := strip.ran(); len(ran) != 0 {
		t.Fatalf("twenty turns of PRINTING put the panel onto %+v, want no event", ran)
	}

	source.show(snapshotOf(printer.StateFinished, true, false, at))
	reader.step(ctx, at.Add(21*tickEvery))

	waitFor(t, "the finished word to reach the panel", func() bool {
		return len(strip.ran()) == 1 && len(strip.words()) == 1
	})

	if ran := strip.ran(); len(ran) != 1 {
		t.Fatalf("the transition fired %d events, want 1: %+v", len(ran), ran)
	}

	for i := 22; i <= 42; i++ {
		reader.step(ctx, at.Add(time.Duration(i)*tickEvery))
	}

	if ran := strip.ran(); len(ran) != 1 || len(strip.words()) != 1 {
		t.Fatalf("twenty turns of FINISHED fired more events: %d effects, %q", len(ran), strip.words())
	}
}

// TestAFailedEventWriteIsLoggedOncePerWideningRetry is the failure half, and it is the same rule
// a failed frame follows: an event that did not reach the controller is reported, retried inside
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

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go pen.run(ctx)

	// One steady frame lands before the controller goes quiet, so the event is the first thing
	// that fails rather than the frame.
	pen.submit(render.Frame{Colour: render.ColourPrinting, Total: 16384, Reason: render.ReasonProgress})
	strip.waitForPaints(t, 1)
	clock.waitArmed(t, 0)

	strip.stopAnswering()

	pen.fire(finishedEvent())
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

	// The controller answers again, the window elapses, and the word goes out — once.
	strip.answerAgain()

	time.Sleep(5 * time.Millisecond)
	clock.Advance(backoffCap)

	waitFor(t, "the recovered event to reach the panel", func() bool { return len(strip.ran()) == 1 })

	if ran := strip.ran(); ran[0].ID != scrollingTextEffect {
		t.Fatalf("the recovered event wrote the effect %+v, want id %d", ran[0], scrollingTextEffect)
	}

	if words := strip.words(); len(words) != 1 || words[0] != "FINISHED" {
		t.Fatalf("the recovered event wrote the words %q, want one FINISHED", words)
	}
}
