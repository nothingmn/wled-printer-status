package main

import (
	"context"
	"encoding/json"
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

// wordPixels is every pixel the controller was left showing in the finished green, which for
// this event is the word and nothing else.
func wordPixels(strip *panelStrip) []int {
	return strip.pixelsIn(render.ColourFinished)
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
// (from, to, geometry) to (word, colour, duration), or nothing.
//
// The cases that return nothing are the ones that would be wrong in a way nobody would notice
// on the light: a strip, which cannot draw a word at all; a panel too small to hold the word at
// the scale this build draws it; a pair that is not a transition, which includes a restart's
// baseline and a state that stood still; and a pause that ends in a finish, which is a pair
// nobody has decided about.
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
		{
			name: "a panel 32 rows tall, which cannot hold a 35-pixel word",
			from: printer.StatePrinting, to: printer.StateFinished, geometry: panel{width: 256, height: 32},
		},
		{
			name: "a panel 234 columns wide, which is one column short",
			from: printer.StatePrinting, to: printer.StateFinished, geometry: panel{width: 234, height: 64},
		},
		{name: "a pause", from: printer.StatePrinting, to: printer.StatePaused, geometry: livePanel},
		{name: "a pause that ends in a finish", from: printer.StatePaused, to: printer.StateFinished, geometry: livePanel},
		{name: "a state that stood still", from: printer.StateFinished, to: printer.StateFinished, geometry: livePanel},
		{name: "a print beginning", from: printer.StateIdle, to: printer.StatePrinting, geometry: livePanel},
		{name: "the first word a process saw", from: "", to: printer.StateFinished, geometry: livePanel},
		{name: "a job stopped by a person", from: printer.StatePrinting, to: printer.StateStopped, geometry: livePanel},
	} {
		t.Run(shape.name, func(t *testing.T) {
			if _, ok := eventFor(shape.from, shape.to, shape.geometry); ok {
				t.Fatalf("%s to %s produced an event on a %dx%d light, want none",
					shape.from, shape.to, shape.geometry.width, shape.geometry.height)
			}
		})
	}
}

// TestTheFinishedWordIsDrawnInPixelsOnThePanel is what the panel is actually sent, quoted as
// bytes where a body is fixed and decoded where it is arithmetic.
//
// Two things are asserted and they are different questions. The first body is quoted exactly:
// the whole panel, switched on, in the colour the steady frame gives an unfilled range, with the
// word as the segment's own name — the request that makes the pixels below land. The bodies
// after it are the pixels: one Paint call split by the client's 8 KiB budget, each naming the
// LED its first colour belongs to, and the union of them is exactly the pixel list the
// rasteriser returns for the word. So the panel is not asked to draw anything; it is told which
// of its 16,384 pixels are green.
func TestTheFinishedWordIsDrawnInPixelsOnThePanel(t *testing.T) {
	recorder := &wireCapture{}
	server := recorder.server(t)

	light, err := wled.New(server.URL)
	if err != nil {
		t.Fatalf("wled.New: %v", err)
	}

	pen := newWriter(light, 128, quiet)
	pen.setPanel(wled.Info{LEDCount: 16384, MatrixWidth: 256, MatrixHeight: 64})

	if err = pen.paintEvent(context.Background(), finishedWordEvent()); err != nil {
		t.Fatalf("paint the finished event: %v", err)
	}

	// The panel, established: segment 0 whole, on, in the background colour, with the word as its
	// name — and segment 1, the frame's remainder, switched off.
	//
	// Segment 1 is in this body because a panel draws its segments in order. The first time this
	// event ran on the live panel, the remainder from the frame before it was still on over
	// columns 28..256, in black, drawn over the word: the word was a fragment on the left, which
	// is the failure the pixel word exists to end. The establishing body is what puts it away.
	const established = `{"seg":[{"id":0,"start":0,"stop":256,"startY":0,"stopY":64,"on":true,"col":[[0,0,0]],"fx":0,"n":"FINISHED"},{"id":1,"on":false}]}`

	if len(recorder.bodies) < 2 {
		t.Fatalf("the event was painted in %d requests, want the panel and then its pixels: %v",
			len(recorder.bodies), recorder.bodies)
	}

	if recorder.bodies[0] != established {
		t.Fatalf("the first request is\n%s\nwant\n%s", recorder.bodies[0], established)
	}

	// The band the word sits in starts at row 14 of a 256-wide panel, so its first LED is
	// 14*256 = 3,584, and it covers 35 rows of 256 = 8,960 pixels — the word's 235-column
	// picture, with the background either side of it and between the letters.
	//
	// The split is the client's: 907 colours while the LED index has four digits, 906 once it
	// has five, and the last request carries what is left.
	wantStarts := []int{3584, 4491, 5398, 6305, 7212, 8119, 9026, 9933, 10840, 11746}
	wantCounts := []int{907, 907, 907, 907, 907, 907, 907, 907, 906, 798}

	pixelBodies := recorder.bodies[1:]
	if len(pixelBodies) != len(wantStarts) {
		t.Fatalf("the word was sent in %d requests, want %d: the band is 8,960 pixels and a body carries about 906",
			len(pixelBodies), len(wantStarts))
	}

	lit := make([]int, 0, 16384)

	for index, body := range pixelBodies {
		var painted struct {
			Seg []struct {
				ID     int               `json:"id"`
				Pixels []json.RawMessage `json:"i"`
			} `json:"seg"`
		}

		if err := json.Unmarshal([]byte(body), &painted); err != nil {
			t.Fatalf("request %d is not a state body: %v: %s", index+2, err, body)
		}

		if len(painted.Seg) != 1 || painted.Seg[0].ID != firstSegment {
			t.Fatalf("request %d addresses %+v, want one segment, id %d", index+2, painted.Seg, firstSegment)
		}

		pixels := painted.Seg[0].Pixels
		if len(pixels) < 2 {
			t.Fatalf("request %d carries %d entries in \"i\", want an index and colours", index+2, len(pixels))
		}

		var first int
		if err := json.Unmarshal(pixels[0], &first); err != nil {
			t.Fatalf("request %d does not start with an LED index: %v: %s", index+2, err, body)
		}

		if first != wantStarts[index] {
			t.Fatalf("request %d starts at LED %d, want %d", index+2, first, wantStarts[index])
		}

		if got := len(pixels) - 1; got != wantCounts[index] {
			t.Fatalf("request %d carries %d colours, want %d", index+2, got, wantCounts[index])
		}

		// A colour is six hex digits. The finished green is 00AA00, and everything else in the
		// band is the background the panel was established with.
		for offset, raw := range pixels[1:] {
			var colour string
			if err := json.Unmarshal([]byte(raw), &colour); err != nil {
				t.Fatalf("request %d carries %s where a colour belongs: %v", index+2, raw, err)
			}

			if colour == "00AA00" {
				lit = append(lit, first+offset)
			}
		}
	}

	slices.Sort(lit)

	want, err := text.Raster(finishedWord, finishedWordScale, text.Geometry{Width: 256, Height: 64})
	if err != nil {
		t.Fatalf("rasterise %q: %v", finishedWord, err)
	}

	if !slices.Equal(lit, want) {
		t.Fatalf("the panel is given %d pixels in the finished green and the word is %d pixels: the two lists differ",
			len(lit), len(want))
	}

	// And no request of the event asks a controller to draw anything, which is the whole
	// correction: the word is this module's pixels, not a firmware feature it has to talk into
	// working.
	for _, body := range recorder.bodies {
		if strings.Contains(body, `"fx":122`) || strings.Contains(body, `"grp":`) {
			t.Fatalf("a request of the finished event carries the text effect: %s", body)
		}
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

	waitFor(t, "the word's pixels to reach the panel", func() bool { return len(wordPixels(strip)) > 0 })

	want, err := text.Raster(finishedWord, finishedWordScale, text.Geometry{Width: 256, Height: 64})
	if err != nil {
		t.Fatalf("rasterise %q: %v", finishedWord, err)
	}

	if got := wordPixels(strip); !slices.Equal(got, want) {
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

// TestAStripGetsNoEventAndKeepsItsSolidFrame is the strip half of the rule, through the driver
// rather than through the decision: a 48-LED strip that finishes a print is written the solid
// green it was already being written, and nothing else.
//
// The reason is the light's: a word is drawn in two dimensions and a strip has one. The operator
// asked for the strip to keep its green, and that is what it does — no establishment, no pixels,
// no request a strip cannot answer.
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

	// Nothing was even offered to the writer: the decision refused it, so the writer has no event
	// in its hand and no request to make.
	if fired := pen.fired.Load(); fired != nil {
		t.Fatalf("a strip was offered the %q event, want none", fired.word)
	}

	if writes := strip.pixelWrites(); len(writes) != 0 {
		t.Fatalf("a strip was given %d per-LED writes, want none", len(writes))
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

	pen.fire(finishedWordEvent())
	written.waitForCount(t, 2)

	if writes := strip.pixelWrites(); len(writes) != 0 {
		t.Fatalf("a borrowed segment was given %d per-LED writes, want none", len(writes))
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

	if writes := strip.pixelWrites(); len(writes) != 0 || paintings(strip) != 0 {
		t.Fatalf("a restart fired an event: %d per-LED writes, %d paintings", len(writes), paintings(strip))
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

	if writes := strip.pixelWrites(); len(writes) != 0 {
		t.Fatalf("a restart after a print fired an event: %d per-LED writes", len(writes))
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

	if lit := wordPixels(strip); len(lit) != 0 {
		t.Fatalf("%d pixels of the word reached a controller that was not answering", len(lit))
	}

	strip.answerAgain()

	time.Sleep(5 * time.Millisecond)
	clock.Advance(backoffCap)

	waitFor(t, "the recovered event to lay the word on the panel", func() bool {
		return len(wordPixels(strip)) == len(want)
	})

	if got := wordPixels(strip); !slices.Equal(got, want) {
		t.Fatalf("the recovered event left %d green pixels, want the word's %d", len(got), len(want))
	}
}
