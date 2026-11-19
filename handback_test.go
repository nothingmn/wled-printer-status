// The hand-back a held word owes the panel, at the stroke its hold runs out.
//
// A word set on a state that is already in force stands in front of a picture that is already on
// the wall, and when its hold runs out the picture is what the panel goes back to. On a live
// 256x64 panel it did not: the establishing body had left segment 0 black with the word as its
// name, live mode ended two and a half seconds later, and the panel stayed black — the name still
// on it — until a later write came round (measured three times: on the stroke once, and 13 and 15
// seconds late twice).
//
// The test below is that shape and no other: the steady frame is on the panel first, the word is
// written on top of it, and the driver's own tick keeps offering the same frame it has been
// offering all along, because a printer that has finished has nothing new to say. What is written
// to the panel in the pass the hold runs out in is the whole question — and the shape is chosen so
// that the writer's sixty-second heartbeat is *not* due at that instant, which is what makes the
// blank visible here instead of being papered over by an unrelated repaint.
package main

import (
	"context"
	"testing"
	"time"

	"github.com/nothingmn/wled-printer-status/internal/printer"
	"github.com/nothingmn/wled-printer-status/internal/render"
)

// wordedRunningPanel is a panel on its steady finished frame, with a store whose FINISHED row
// carries a word that was written on the state while it was already being shown.
type wordedRunningPanel struct {
	strip *panelStrip
	table *handTable
	pen   *writer
	clock *fakeClock
	turn  scene
	at    time.Time
}

// runWordedRunningPanel builds one and leaves it on the steady frame, before any word is set.
//
// The panel is four hours into its finished state, and the frame is written now — so the writer's
// heartbeat is measured from this instant and comes due a minute later.
func runWordedRunningPanel(t *testing.T, at time.Time) *wordedRunningPanel {
	t.Helper()

	strip := newPanelStrip()
	pen := newWriter(strip, 128, quiet)
	pen.setPanel(strip.info)

	table := &handTable{states: map[string]show{
		printer.StateFinished: {colour: render.ColourFinished, origin: fromStore},
	}}

	pen.store = table

	clock := newFakeClock(at)
	clock.watch(pen)
	pushInto(pen)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	go pen.run(ctx)

	one := &wordedRunningPanel{
		strip: strip,
		table: table,
		pen:   pen,
		clock: clock,
		turn: scene{
			frame: render.Frame{Colour: render.ColourFinished, Total: 16384, Reason: render.ReasonNoLiveJob},
			state: printer.StateFinished,
			since: at.Add(-4 * time.Hour),
		},
		at: at,
	}

	pen.submitScene(one.turn)

	waitFor(t, "the finished colour to reach the panel", func() bool { return len(steadyPaints(strip)) == 1 })

	return one
}

// writing puts the word in the table, which is what the Colours screen's save does to the store.
func (p *wordedRunningPanel) writing(word, holds string, setAt time.Time) {
	p.table.mu.Lock()
	defer p.table.mu.Unlock()

	p.table.states[printer.StateFinished] = show{
		colour: render.ColourFinished, origin: fromStore, word: word, holds: holds, setAt: setAt,
	}
}

// ticking turns the writer's clock forward one second at a time and offers the same scene each
// time, which is what the driver's once-a-second tick does over a printer with nothing new to say.
//
// Each turn waits for the writer to have armed its next wait, so the pass a hold runs out in is a
// pass the test can name rather than a race against the goroutine.
func (p *wordedRunningPanel) ticking(t *testing.T, seconds int) {
	t.Helper()

	for range seconds {
		mark := p.clock.mark()

		p.clock.Advance(time.Second)
		p.pen.submitScene(p.turn)

		p.clock.waitArmed(t, mark)
	}
}

// TestAWordOnARunningStateHandsTheSteadyFrameBackAtTheStroke is the defect a live panel showed,
// with the heartbeat deliberately not due.
//
// The word is written fifteen seconds after the frame was last painted, and it holds for thirty.
// The hold therefore runs out forty-five seconds after that paint, with fifteen seconds still to go
// before the heartbeat — so a hand-back that waits for the heartbeat is fifteen seconds of black
// panel, which is the number the operator's wall produced.
func TestAWordOnARunningStateHandsTheSteadyFrameBackAtTheStroke(t *testing.T) {
	at := time.Date(2026, time.November, 19, 12, 0, 0, 0, time.UTC)

	panel := runWordedRunningPanel(t, at)

	// Fifteen seconds of the steady picture, then the word is written on the state that is already
	// on the wall.
	panel.ticking(t, 15)
	panel.writing("QA", hold30s, at.Add(15*time.Second))
	panel.pen.submitScene(panel.turn)

	waitFor(t, "the word to reach the panel", func() bool { return paintings(panel.strip) == 1 })

	if got := len(steadyPaints(panel.strip)); got != 1 {
		t.Fatalf("the panel was given the steady frame %d times while the word went up, want 1", got)
	}

	// The hold runs out. Every second of it, the driver says the same thing it has been saying.
	panel.ticking(t, 30)

	if got := len(steadyPaints(panel.strip)); got != 2 {
		t.Fatalf("the panel was given the steady frame %d times by the instant the hold ran out, want 2: "+
			"the word's hand-back is the picture that was already there, and no later write is allowed to stand in for it",
			got)
	}

	back := steadyPaints(panel.strip)[1]

	if back.segments[0].Colour != render.ColourFinished {
		t.Fatalf("the panel went back to %+v, want the state's colour %+v", back.segments[0].Colour, render.ColourFinished)
	}

	// The name the word put on the segment is the word's, and the picture that ends it has to clear
	// it: this firmware keeps a segment's name through a body that does not mention one.
	if back.segments[0].Name != "" {
		t.Fatalf("the frame that took the panel back left the segment named %q, want no name", back.segments[0].Name)
	}
}

// TestAWordOfAMinuteAlsoHandsBackAtTheStroke is the same rule with the hold the operator measured
// against — and it is here because a minute-long hold puts the stroke exactly on the heartbeat's
// due moment, where a hand-back that waits for the heartbeat looks prompt by accident. It has to
// be the word's own end that paints the picture, not a coincidence of the clock.
func TestAWordOfAMinuteAlsoHandsBackAtTheStroke(t *testing.T) {
	at := time.Date(2026, time.November, 19, 12, 0, 0, 0, time.UTC)

	panel := runWordedRunningPanel(t, at)

	panel.writing("QA", hold60s, at)
	panel.pen.submitScene(panel.turn)

	waitFor(t, "the word to reach the panel", func() bool { return paintings(panel.strip) == 1 })

	panel.ticking(t, 60)

	if got := len(steadyPaints(panel.strip)); got != 2 {
		t.Fatalf("the panel was given the steady frame %d times by the instant a minute-long hold ran out, want 2", got)
	}
}
