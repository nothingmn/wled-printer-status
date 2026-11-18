package main

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nothingmn/wled-printer-status/internal/render"
	"github.com/nothingmn/wled-printer-status/internal/wled"
)

// deskLight is the operator's own controller as a test knows it: forty-eight LEDs in one run
// and six named segments, of which `Back 1` is the ten-pixel one this module is pointed at.
//
// It is here rather than built by each test because what the numbers mean is the point. The
// other five are his zones, and a body that named one of them would be a module drawing in
// somebody's base or somebody's front.
func deskLight() wled.State {
	orange := []wled.RGB{{R: 255, G: 160, B: 0}}

	return wled.State{
		On:         true,
		Brightness: 200,
		Segments: []wled.Segment{
			{ID: 0, Start: 0, Stop: 4, On: true, Colors: orange, Effect: 0, Name: "Base 1"},
			{ID: 1, Start: 4, Stop: 14, On: true, Colors: orange, Effect: 0, Name: "Back 1"},
			{ID: 2, Start: 14, Stop: 24, On: true, Colors: orange, Effect: 0, Name: "Front 1"},
			{ID: 3, Start: 24, Stop: 28, On: true, Colors: orange, Effect: 0, Name: "Base 2"},
			{ID: 4, Start: 28, Stop: 38, On: true, Colors: orange, Effect: 0, Name: "Back 2"},
			{ID: 5, Start: 38, Stop: 48, On: true, Colors: orange, Effect: 0, Name: "Front 3"},
		},
	}
}

// borrowedStrip is a controller that holds the operator's six segments and will take frames.
func borrowedStrip() *fakeStrip {
	strip := newFakeStrip()
	strip.state = deskLight()

	return strip
}

// TestTheLitLengthInsideABorrowedRun is the fraction arithmetic on a ten-pixel zone, which is
// the whole of what the borrowed run is asked to say.
//
// The two ends are not arithmetic and are worth reading as their own rules: a frame with no
// completed part fills the run, exactly as the strip fills its whole range, and a frame with no
// range at all fills nothing and leaves the run dark.
func TestTheLitLengthInsideABorrowedRun(t *testing.T) {
	const span = 10

	cases := []struct {
		name  string
		frame render.Frame
		span  int
		want  int
	}{
		{"no range known, so nothing to show", render.Frame{Colour: render.ColourUnknown, Lit: 0, Total: 0}, span, 0},
		{"no completed part, so the run is one colour", render.Frame{Colour: render.ColourIdle, Lit: 0, Total: 100}, span, span},
		{"one percent", render.Frame{Colour: render.ColourIdle, Lit: 1, Total: 100}, span, 1},
		{"ten percent", render.Frame{Colour: render.ColourPrinting, Lit: 10, Total: 100}, span, 1},
		{"fifty percent", render.Frame{Colour: render.ColourPrinting, Lit: 50, Total: 100}, span, 5},
		{"ninety-nine percent", render.Frame{Colour: render.ColourPrinting, Lit: 99, Total: 100}, span, 10},
		{"everything", render.Frame{Colour: render.ColourFinished, Lit: 100, Total: 100}, span, span},
		{"a fraction that rounds to nothing still lights one pixel", render.Frame{Colour: render.ColourPrinting, Lit: 2, Total: 1000}, span, 1},
		{"a run with no pixels", render.Frame{Colour: render.ColourPrinting, Lit: 50, Total: 100}, 0, 0},
	}

	for _, one := range cases {
		t.Run(one.name, func(t *testing.T) {
			if got := litInSpan(one.frame, one.span); got != one.want {
				t.Fatalf("a frame of %d of %d over %d pixels lit %d, want %d",
					one.frame.Lit, one.frame.Total, one.span, got, one.want)
			}
		})
	}
}

// TestOnlyTheBorrowedSegmentIsEverWritten is the operator's requirement in one assertion: his
// five other zones are never in a body. Every write names one id, and it is the borrowed one.
func TestOnlyTheBorrowedSegmentIsEverWritten(t *testing.T) {
	strip := borrowedStrip()
	pen := newSegmentWriter(strip, 1, "http://10.0.0.253", quiet)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go pen.run(ctx)

	// Four pictures, including the two shapes that write a remainder on a whole strip: a
	// half-filled frame, a frame with nothing filled, a full one, and one with no range.
	pen.submit(render.Frame{Colour: render.ColourPrinting, Lit: 24, Total: 48, Reason: render.ReasonProgress})
	strip.waitForPaints(t, 1)
	pen.submit(frame(render.ColourIdle, render.ReasonNoLiveJob))
	strip.waitForPaints(t, 2)
	pen.submit(render.Frame{Colour: render.ColourFinished, Lit: 48, Total: 48, Reason: render.ReasonProgress})
	strip.waitForPaints(t, 3)
	pen.submit(render.Frame{Colour: render.ColourUnknown, Lit: 0, Total: 0, Reason: render.ReasonDisconnected})
	strip.waitForPaints(t, 4)

	time.Sleep(50 * time.Millisecond)

	for index, call := range strip.shown() {
		if len(call.segments) != 1 {
			t.Fatalf("write %d carried %d segments, and a second one would be one of the operator's: %+v",
				index, len(call.segments), call.segments)
		}

		if call.segments[0].ID != 1 {
			t.Fatalf("write %d named segment %d, and only segment 1 was borrowed",
				index, call.segments[0].ID)
		}
	}
}

// TestTheBorrowedRunIsDrawnInsideTheSegmentItBorrowed checks the geometry of one frame against
// the span that was borrowed: the run starts where the segment starts and is as long as the
// frame's fraction of it, so nothing outside 4..14 is ever written.
func TestTheBorrowedRunIsDrawnInsideTheSegmentItBorrowed(t *testing.T) {
	strip := borrowedStrip()
	pen := newSegmentWriter(strip, 1, "http://10.0.0.253", quiet)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go pen.run(ctx)

	pen.submit(render.Frame{Colour: render.ColourPrinting, Lit: 24, Total: 48, Reason: render.ReasonProgress})
	strip.waitForPaints(t, 1)

	shown := strip.shown()
	fill := shown[0].segments[0]

	if fill.First != 4 || fill.Count != 5 || !fill.On {
		t.Fatalf("the fill is %+v, want five of the ten pixels from 4, drawn", fill)
	}

	if fill.Colour != render.ColourPrinting {
		t.Fatalf("the fill is drawn in %+v, want the frame's colour %+v", fill.Colour, render.ColourPrinting)
	}

	if end := fill.First + fill.Count; end > 14 {
		t.Fatalf("the fill reaches pixel %d, past the borrowed run that ends at 14", end)
	}
}

// TestABorrowedRangeIsLeftDarkRatherThanPaintedBlack is the "no bar" case. A frame that has no
// range to fill is written as a segment that is off and carries no colour and no span: the run
// goes dark because nothing draws in it, never because this module painted it black.
func TestABorrowedRangeIsLeftDarkRatherThanPaintedBlack(t *testing.T) {
	strip := borrowedStrip()
	pen := newSegmentWriter(strip, 1, "http://10.0.0.253", quiet)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go pen.run(ctx)

	pen.submit(render.Frame{Colour: render.ColourUnknown, Lit: 0, Total: 0, Reason: render.ReasonProgressUnknown})
	strip.waitForPaints(t, 1)

	shown := strip.shown()
	if len(shown[0].segments) != 1 {
		t.Fatalf("the frame was written as %d segments, want 1", len(shown[0].segments))
	}

	dark := shown[0].segments[0]
	if dark.On {
		t.Fatal("the segment was switched on with nothing to show, so it is showing its last colour")
	}

	if dark.Count != 0 {
		t.Fatalf("the dark segment names %d pixels, and a body with no span leaves its bounds alone", dark.Count)
	}

	if (dark.Colour != wled.RGB{}) {
		t.Fatalf("the dark segment carries the colour %+v, and a colour is what painting it would be", dark.Colour)
	}
}

// TestABorrowedSegmentCostsNoBrightnessAndNoPower is the trap, asserted rather than described.
// Both calls are controller-wide, so a single one of them at start would move every one of the
// operator's six zones at once.
func TestABorrowedSegmentCostsNoBrightnessAndNoPower(t *testing.T) {
	strip := borrowedStrip()
	pen := newSegmentWriter(strip, 1, "http://10.0.0.253", quiet)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		defer close(done)

		pen.run(ctx)
	}()

	pen.submit(render.Frame{Colour: render.ColourPrinting, Lit: 24, Total: 48, Reason: render.ReasonProgress})
	strip.waitForPaints(t, 1)

	cancel()

	select {
	case <-done:
	case <-time.After(howLong):
		t.Fatal("the writer did not stop in time")
	}

	if settled := strip.settleWrites(); settled != 0 {
		t.Fatalf("the writer made %d brightness or power writes, and it must make none", settled)
	}
}

// TestABorrowedSegmentIsGivenBackVerbatimWhenTheWriterStops is the other half of borrowing: the
// controller remembers a segment's bounds, so a module that shortened one must put it back
// exactly — run, colour, effect and name — or the operator's zone stays a ten-pixel bar of
// whatever this module was last showing.
func TestABorrowedSegmentIsGivenBackVerbatimWhenTheWriterStops(t *testing.T) {
	strip := borrowedStrip()
	pen := newSegmentWriter(strip, 1, "http://10.0.0.253", quiet)

	// What the controller was holding before a frame arrived.
	var was wled.Segment

	for _, segment := range deskLight().Segments {
		if segment.ID == 1 {
			was = segment
		}
	}

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		defer close(done)

		pen.run(ctx)
	}()

	pen.submit(render.Frame{Colour: render.ColourPrinting, Lit: 24, Total: 48, Reason: render.ReasonProgress})
	strip.waitForPaints(t, 1)

	cancel()

	select {
	case <-done:
	case <-time.After(howLong):
		t.Fatal("the writer did not stop in time")
	}

	given := strip.givenBack()

	if len(given) != 1 {
		t.Fatalf("the controller was given back %d segments, want 1: %+v", len(given), given)
	}

	if !reflect.DeepEqual(given[0], was) {
		t.Fatalf("the segment was given back as %+v, and it was borrowed as %+v", given[0], was)
	}
}

// TestAWriterThatCannotFindItsSegmentWritesNothing is the case that must never become a write.
// A controller that does not hold the segment an operator named is not an invitation to write
// one: WLED would create it, at the bounds this module chose, in somebody's strip.
func TestAWriterThatCannotFindItsSegmentWritesNothing(t *testing.T) {
	strip := newFakeStrip()
	strip.state = wled.State{On: true, Segments: []wled.Segment{{ID: 0, Start: 0, Stop: 4, On: true}}}

	written := &logs{}
	pen := newSegmentWriter(strip, 7, "http://10.0.0.253", written.logf)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go pen.run(ctx)

	pen.submit(frame(render.ColourPrinting, render.ReasonProgress))

	// Long enough for the first attempt and its retry to have gone out, if they were going to.
	time.Sleep(100 * time.Millisecond)

	if shown := strip.shown(); len(shown) != 0 {
		t.Fatalf("the light was written %d times for a segment the controller does not hold: %+v", len(shown), shown)
	}

	if !written.contains("could not be borrowed") {
		t.Fatalf("the log does not say why the light is not being driven: %v", written.all())
	}
}

// TestTheBorrowIsSaidOnceInTheLogAndSoIsTheGiveBack is the one line each end of a borrow owes a
// person reading the log: what was taken and from where, and what was put back.
func TestTheBorrowIsSaidOnceInTheLogAndSoIsTheGiveBack(t *testing.T) {
	strip := borrowedStrip()
	written := &logs{}
	pen := newSegmentWriter(strip, 1, "http://10.0.0.253", written.logf)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		defer close(done)

		pen.run(ctx)
	}()

	pen.submit(frame(render.ColourPrinting, render.ReasonProgress))
	strip.waitForPaints(t, 1)

	cancel()

	select {
	case <-done:
	case <-time.After(howLong):
		t.Fatal("the writer did not stop in time")
	}

	for _, want := range []string{
		`borrowed segment 1 "Back 1" from http://10.0.0.253: pixels 4-14`,
		`gave segment 1 "Back 1" back to http://10.0.0.253: pixels 4-14`,
	} {
		if !written.contains(want) {
			t.Fatalf("the log does not carry %q: %v", want, written.all())
		}
	}

	if got := written.count(); got != 2 {
		t.Fatalf("the borrow and the give-back wrote %d lines, want 2: %v", got, written.all())
	}
}

// TestAWholeStripIsDrivenAsItAlwaysWas is the other side of the setting: with no segment named,
// the writer settles the brightness and the power and writes both segments, exactly as before
// there was a setting at all.
func TestAWholeStripIsDrivenAsItAlwaysWas(t *testing.T) {
	strip := newFakeStrip()
	pen := newWriter(strip, 128, quiet)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go pen.run(ctx)

	pen.submit(render.Frame{Colour: render.ColourPrinting, Lit: 24, Total: 48, Reason: render.ReasonProgress})
	strip.waitForPaints(t, 1)

	time.Sleep(50 * time.Millisecond)

	if settled := strip.settleWrites(); settled != 2 {
		t.Fatalf("the whole-strip writer made %d brightness and power writes, want 2", settled)
	}

	shown := strip.shown()
	if len(shown) != 1 || len(shown[0].segments) != 2 {
		t.Fatalf("the frame was written as %+v, want one body of two segments", shown)
	}

	if shown[0].segments[0].ID != 0 || shown[0].segments[1].ID != 1 {
		t.Fatalf("the body names the segments %d and %d, want 0 and 1",
			shown[0].segments[0].ID, shown[0].segments[1].ID)
	}
}

// TestAPanelRefusesToBeDrivenByOneSegment holds the panel path where it was, and holds the
// module to writing nothing rather than something wrong. A panel's body is two rectangles that
// cover the whole of it; on a controller carrying somebody else's segments that is two of
// theirs, so the setting is refused and the matrix is left alone.
func TestAPanelRefusesToBeDrivenByOneSegment(t *testing.T) {
	light := newPanelStrip()
	light.state = deskLight()

	written := &logs{}
	pen := newSegmentWriter(light, 1, "http://10.0.0.136", written.logf)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go pen.run(ctx)

	// The geometry is what the driver stores the moment the controller answers Info.
	pen.setPanel(light.info)

	pen.submit(render.Frame{Colour: render.ColourPrinting, Lit: 9666, Total: 16384, Reason: render.ReasonProgress})

	time.Sleep(100 * time.Millisecond)

	if shown := light.shown(); len(shown) != 0 {
		t.Fatalf("the panel was written %d times with one segment named, want none: %+v", len(shown), shown)
	}

	if settled := light.settleWrites(); settled != 0 {
		t.Fatalf("the panel took %d brightness or power writes, and this mode writes neither", settled)
	}

	if !written.contains("is a 256x64 panel") {
		t.Fatalf("the log does not say why the panel is not being driven: %v", written.all())
	}
}

// TestThePanelBodyIsUnchangedWhenNoSegmentIsNamed is the other half: without the setting, a
// panel is still the two rectangles it has always been given, from the same function as before.
func TestThePanelBodyIsUnchangedWhenNoSegmentIsNamed(t *testing.T) {
	frame := render.Frame{Colour: render.ColourPrinting, Lit: 9666, Total: 16384, Reason: render.ReasonProgress}

	body := segmentsFor(frame, livePanel)

	if len(body) != 2 {
		t.Fatalf("the panel body carries %d segments, want 2: %+v", len(body), body)
	}

	if body[0].ID != 0 || body[0].Count != 151 || body[0].RowCount != 64 {
		t.Fatalf("the fill is %+v, want segment 0 over 151 columns and every row", body[0])
	}

	if body[1].ID != 1 || body[1].First != 151 || body[1].Count != 105 {
		t.Fatalf("the remainder is %+v, want segment 1 from column 151", body[1])
	}
}

// contains reports whether any log line carries the phrase, which is how a test asks whether a
// person reading the log would have been told.
func (l *logs) contains(phrase string) bool {
	for _, line := range l.all() {
		if strings.Contains(line, phrase) {
			return true
		}
	}

	return false
}
