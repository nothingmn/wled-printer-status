// The two ways something other than the steady frame holds the light: a pause, which switches it
// off, and a state's word, which takes the panel for as long as its `holds` says.
//
// # Why they are one file
//
// Both are the answer to "the picture the driver decided is not what this light should show right
// now", and both are decided from the store at the moment of the paint rather than from anything
// the driver sent. They also share the property that makes them easy to get wrong: neither may
// leave the light showing the wrong thing for as long as the process happens to be running. A pause
// that ends and leaves the light dark until the next tick is the defect §3 names; a word that ends
// and leaves a panel saying FINISHED is the same defect wearing a hat.
//
// # What is reused rather than rebuilt
//
// A state's word is not a second text path. It is drawn by the event's own painter — the
// establishing body, the pixels over DDP, the one read that says the controller took them, the
// once-a-second repeat that keeps live mode alive and the hand-back at the end — because that is
// the path the finished word is proved on, on the operator's own panel (AGENTS.md §5.4). What is
// new here is only *when* it applies and *how long* it holds.
package main

import (
	"context"
	"fmt"
	"time"

	"github.com/nothingmn/wled-printer-status/internal/text"
)

// pauseNow is the pause in force for this binding at this moment, and false when there is none.
//
// It is asked of the store every pass rather than remembered, because the answer changes on its own
// — the instant a pause ends passes whether or not anything wrote to the document — and a cached
// answer would be a light that stayed off until something else happened to wake the writer.
func (w *writer) pauseNow() (pauseEnd, bool) {
	if w.store == nil {
		return pauseEnd{}, false
	}

	return w.store.paused(w.now())
}

// holdPause does everything a pause is, and reports whether the light is paused — in which case the
// caller starts its loop again and writes nothing at all.
//
// It exists as one function because the three things a pause must do are one decision: write off
// once when it begins, write nothing while it lasts, and make the first pass after it ends paint
// rather than believe the light is still showing what it showed before. Split across the loop, a
// pause would be three chances to leave a light on.
//
// The wait is bounded by the instant the pause ends and by nothing else: a pause of ten minutes
// ending is a repaint at ten minutes, not at the next tick, and a pause of forever waits for the
// bell the driver rings every tick — which is also how a resume through the route is noticed, since
// a resume is a write to the document and the driver's next frame is what carries the news here.
func (w *writer) holdPause(ctx context.Context) bool {
	end, paused := w.pauseNow()

	if !paused {
		if w.paused {
			w.switchOn(ctx)
		}

		return false
	}

	if !w.paused {
		w.switchOff(ctx, end)
	}

	// Nothing the driver decides is news while the light is off — a print that finished during a
	// pause is not something to celebrate on a dark light — and a slot left full would stop the
	// bell ringing again, which is the only way a resume reaches this goroutine.
	w.latest.Swap(nil)
	w.fired.Swap(nil)

	if end.forever {
		select {
		case <-ctx.Done():
		case <-w.wake:
		}

		return true
	}

	select {
	case <-ctx.Done():
	case <-w.wake:
	case <-w.after(max(end.until.Sub(w.now()), 0)):
	}

	return true
}

// switchOff makes the light dark, which is what a pause is: not frozen at its last picture — that is
// a picture of something no longer true — but off.
//
// The exception is the whole reason this is not one line. A whole light is switched off with the one
// write that does it: `SetPower`, which is controller-wide and is this module's to write because it
// drives that whole controller. A **borrowed zone** is not: the rest of that strip belongs to
// somebody else, and powering the controller down would take their run with it. So it is handed back
// exactly as it was found — the same give-back every borrowed binding owes on its way out — and the
// controller stays on for its owner.
//
// Which of the two it is has already been decided by pause.go's `pauseEffectOn`, and this reads that
// answer rather than making the decision a second time in a second place.
func (w *writer) switchOff(ctx context.Context, end pauseEnd) {
	w.paused = true

	// Whatever the light was holding, it is not holding it any more: the composed picture is not
	// being pushed, and a word is not being re-pushed. Leaving either behind would have the writer
	// believe the panel is still live when the light is off.
	//
	// A preset moment is given back rather than paused, for the reason it exists: the light is not
	// this module's for those seconds, so what it was doing has to be put back before it is switched
	// off — or, on a borrowed zone, instead of it.
	if w.held != nil && w.held.took != nil {
		w.giveLightBack(ctx, w.held.took)

		w.held.took = nil
	}

	w.held = nil
	w.composed = nil
	w.spoken = nil
	w.level = -1

	// The light is off, and the thumbnail says so: black, because black is what is on the wall.
	w.holding.Store(&holding{paused: true})

	if w.pauseEffect == effectHandsBack {
		w.handBack(ctx)

		if w.segment != nil {
			w.logf("%s: the light is paused, so segment %d on %s was given back rather than switched off, which would have taken the rest of that strip with it%s",
				name, *w.segment, w.where, end.phrase())
		}

		return
	}

	if err := w.report(func() error { return w.light.SetPower(ctx, false) }); err != nil {
		// Once, and not retried: the rule a pause keeps is one write and then none, so a controller
		// that would not take this one is said plainly and the pause still holds. It is the one
		// sentence a person reading the log needs to see, because the light on the wall is not off.
		w.logf("%s: the light is paused and it was not switched off: %v", name, err)

		return
	}

	w.logf("%s: the light is off: a pause is in force%s", name, end.phrase())
}

// switchOn puts the light back when a pause ends or is resumed.
//
// A whole light is settled again — the brightness in force at that moment and the power on — because
// a pause wrote neither of them anywhere the writer can trust: the light has been dark, and what it
// comes back at is what the settings and the windows say now, not what it was at when somebody
// pressed the button.
//
// A borrowed zone is borrowed again, which reads the segment as the controller holds it now. It was
// given back, so it is somebody else's until this module reads it, and writing over what happened to
// it in the meantime would be exactly the promise the borrow exists to keep.
func (w *writer) switchOn(ctx context.Context) {
	w.paused = false
	w.afterPause = true

	// The light is being drawn again, so nothing is holding it: the first frame painted after this
	// is what the thumbnail shows.
	w.holding.Store(nil)

	if w.pauseEffect == effectHandsBack {
		w.borrow(ctx)

		if w.segment != nil {
			w.logf("%s: the pause is over, so segment %d on %s was borrowed again and the light is being drawn in", name, *w.segment, w.where)
		}

		return
	}

	w.settle(ctx)

	w.logf("%s: the pause is over and the light is back on", name)
}

// handBack writes a borrowed segment back exactly as it was found, and forgets it.
//
// It is the same give-back a stop owes, and it is one function so that the two cannot give back two
// different things: the segment as `borrow` captured it, its own colours, effect, name and
// brightness, and nothing else on that controller touched.
func (w *writer) handBack(ctx context.Context) {
	if w.borrowed == nil {
		return
	}

	held := *w.borrowed

	if err := w.gate.do(func() error { return w.light.RestoreSegment(ctx, held.Segment) }); err != nil {
		w.logf("%s: the borrowed segment %d on %s was not given back: %v", name, held.ID, w.where, err)

		return
	}

	w.borrowed = nil
	w.span.Store(nil)

	w.logf("%s: gave segment %d %q back to %s: pixels %d-%d, colour %s, effect %d, brightness %s",
		name, held.ID, held.Name, w.where, held.Start, held.Stop,
		coloursPhrase(held.Colors), held.Effect, brightnessPhrase(held.Brightness))
}

// wordNow is the word the state table puts on this light at this moment, and false when the light
// should show its steady picture instead.
//
// Four things can make the answer false, and each of them is a rule rather than a shortcut:
//
//   - **The state carries no word.** That is the default, and a state nobody has edited shows the
//     colour Appendix A gives it.
//   - **The word's hold has run out.** A word with `holds: 30s` is shown for the first thirty
//     seconds of its state and the steady picture takes the light back after that. `forever` never
//     runs out. The comparison is made against the writer's own clock at the moment of the paint,
//     so nothing has to fire and a process restarted mid-hold judges the same way.
//   - **The light is a strip.** A word is pixels in two dimensions; a strip has one. Its fallback is
//     designed and locked: the state's colour, which is what the steady frame already paints, so
//     this answer is simply "no word".
//   - **The word does not fit the panel.** Refused here rather than at the paint, because a word
//     that cannot be rasterised would otherwise cost a failed write, a widening retry and a dark
//     panel. The state's colour stands, and the refusal is said once.
func (w *writer) wordNow() (show, bool) {
	if w.store == nil {
		return show{}, false
	}

	standing := w.standing.Load()
	if standing == nil || standing.state == "" {
		return show{}, false
	}

	wanted, known := w.store.show(standing.state)
	if !known || wanted.word == "" {
		return show{}, false
	}

	if !wordInForce(wanted.holds, standing.since, wanted.setAt, w.now()) {
		return show{}, false
	}

	geometry := w.panelOf()
	if !geometry.matrix() {
		return show{}, false
	}

	if _, err := text.Place(wanted.word, finishedWordScale, geometryOf(geometry)); err != nil {
		w.sayWord(fmt.Sprintf("%s: the word %q that the %s state carries was not drawn: %v; the state's colour stands",
			name, wanted.word, standing.state, err))

		return show{}, false
	}

	return wanted, true
}

// sinceOf is when a scene says its state began, and the zero time for a scene that says nothing or
// for no scene at all — which is a frame submitted without one, and reads as a word that does not end.
func sinceOf(standing *scene) time.Time {
	if standing == nil {
		return time.Time{}
	}

	return standing.since
}

// sayWord reports one word this writer could not draw, once.
func (w *writer) sayWord(message string) {
	if w.saidWord == message {
		return
	}

	w.saidWord = message

	w.logf("%s", message)
}

// speak keeps the state table's word on the light: it paints it once, pushes it again while it
// holds, and lets go the moment it stops applying.
//
// It reports whether the word is holding the light — in which case the caller waits and the steady
// frame stays in its slot — and how long it may wait before looking again. The wait is the earlier
// of the next repeat and the moment the hold ends, so a hold that runs out is a repaint at that
// instant rather than at the next tick: the whole reason the deadline is carried here at all.
func (w *writer) speak(ctx context.Context) (time.Duration, bool, error) {
	wanted, ok := w.wordNow()

	if !ok {
		if w.spoken == nil {
			return 0, false, nil
		}

		// The word has stopped applying: the state moved, or its hold ran out. The panel is given
		// back by the frame the caller paints in this same pass, which is why nothing is written
		// here — the picture that follows is the one the driver already decided.
		w.spoken = nil
		w.composed = nil
		w.handedBack = true
		w.holding.Store(nil)

		return 0, false, nil
	}

	switch {
	case w.spoken == nil ||
		w.spoken.word != wanted.word ||
		w.spoken.colour != wanted.colour ||
		!w.spoken.until.Equal(wordUntil(wanted.holds, sinceOf(w.standing.Load()), wanted.setAt)):
		if err := w.holdWord(ctx, wanted); err != nil {
			return 0, true, err
		}

	case !w.spoken.repush.After(w.now()):
		if err := w.repushWord(); err != nil {
			return 0, true, err
		}
	}

	wait := max(w.spoken.repush.Sub(w.now()), 0)

	if !w.spoken.until.IsZero() {
		wait = min(wait, max(w.spoken.until.Sub(w.now()), 0))
	}

	return wait, true, nil
}

// holdWord paints one word on the panel, through the event's own painter.
//
// It carries no `holds`: this is not an event with a deadline, it is a picture that lasts as long as
// its state does, and what ends it is `wordNow` answering no. The instant its hold runs out is kept
// beside it so that the loop can wake at exactly that moment.
func (w *writer) holdWord(ctx context.Context, wanted show) error {
	// The panel is not holding a composed picture any more, whatever it was doing before: the word
	// has taken it, and a `composed` left standing would have the writer re-push a picture that is
	// not on the light.
	w.composed = nil

	// The light is showing this word and not the frame the writer last delivered, so the picture
	// the word hands back to has to be written when the hold runs out rather than suppressed as
	// "already shown" — see overwritten in drive.go, and the event path there, which clears what
	// the writer remembers for the same reason. The word *is* the establishing body plus pixels:
	// the frame behind it stops being visible the moment this lands.
	w.overwritten = true

	ev := event{word: wanted.word, colour: wanted.colour}

	frame, err := w.paintReported(ctx, ev)
	if err != nil {
		return err
	}

	w.spoken = &heldEvent{
		event:   ev,
		until:   wordUntil(wanted.holds, sinceOf(w.standing.Load()), wanted.setAt),
		frame:   frame,
		repush:  w.now().Add(pixelRepeat),
		painted: true,
	}

	// What the light is showing, for the Overview's thumbnail: the word, in its own colour. It is
	// published here rather than worked out again by the route, because the route cannot know which
	// word the panel is holding without asking this goroutine.
	w.holding.Store(&holding{word: wanted.word, colour: wanted.colour})

	// One line, on the way up, like every other transition in this log: a picture that stood all
	// evening says nothing, and an operator who wants to know why his panel is showing a word needs
	// to find it in the history.
	if w.spoken.until.IsZero() {
		w.logf("%s: the panel is showing the word %q that a state carries, with no end", name, wanted.word)

		return nil
	}

	w.logf("%s: the panel is showing the word %q that a state carries until %s",
		name, wanted.word, w.spoken.until.UTC().Format(time.RFC3339))

	return nil
}

// repushWord pushes the word that is already up once more.
//
// The same pixels go out rather than a fresh drawing: a word does not move, and it is the repeat
// that keeps the firmware in realtime — 2500 ms of silence and the panel goes back to its own
// segments (pixels.go has the measurement). A repeat that fails is one line and the hold continues,
// on the same rule the finished word's repeats keep: the frame after the hold is where a dead
// controller is reported, not forty lines during it.
func (w *writer) repushWord() error {
	if err := w.republish(w.spoken); err != nil {
		if w.spoken.pushFails == 0 {
			w.logf("%s: the word %q did not reach the panel again and it stays up: %v", name, w.spoken.word, err)
		}

		w.spoken.pushFails++

		// The wait is still moved on, or a controller that refuses every datagram would be retried
		// as fast as this loop turns.
		w.spoken.repush = w.now().Add(pixelRepeat)

		return nil
	}

	w.spoken.repush = w.now().Add(pixelRepeat)

	return nil
}
