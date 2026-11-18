// The event layer: what happened, as opposed to what is happening.
//
// Every frame this module has ever written is a function of the printer's state at the moment
// it was decided. Green while it prints, a bar as the job moves, the finished colour once it
// has finished, and the same picture again a minute later because a controller that rebooted
// needs it. That is a status display, and it is the right thing for the ninety-nine minutes of
// an hour in which nothing changes.
//
// It cannot say the one thing a person actually wants from a light across a room: that
// something *just happened*. A whole panel in the finished green looks exactly the same in the
// first second of a finished print as it will at dinner time; the transition — the moment worth
// noticing — is the one thing a frame cannot carry, because a frame does not know what came
// before it. An event does: it is a function of the two states, not of one.
//
// So this file is one step of that: a decision from (the state the printer was in, the state it
// moved to, the controller's geometry) to (a picture, a duration), and the writer's machinery
// for holding that picture for its duration and then going back to the frame it would have
// shown. Nothing here reads a clock: the writer stamps the deadline from its own, which is what
// lets a test move an hour through an event without waiting for one.
//
// The list is deliberately one entry long. Print start and a filament change are both wanted
// and neither has an animation or a duration decided, and inventing either would be a product
// decision made quietly in a code change. What one entry needs is written on eventFor.
package main

import (
	"context"
	"fmt"
	"time"

	"github.com/nothingmn/wled-printer-status/internal/printer"
	"github.com/nothingmn/wled-printer-status/internal/render"
	"github.com/nothingmn/wled-printer-status/internal/wled"
)

const (
	// finishedWord is what the panel spells out when a print completes. It is the segment's
	// own name, which is the only string WLED's text effect can draw, and it is 48 characters
	// at most by the firmware's own limit.
	finishedWord = "FINISHED"

	// finishedEventHolds is how long the word holds the light before the steady finished
	// frame takes it back: one minute.
	//
	// Chosen by this seat, not settled with the operator, and named here so that it is one
	// line to change. A minute is long enough that somebody who looks up when the printer
	// stops sees the word rather than a green panel, and short enough that the panel is not
	// still saying FINISHED an hour later — the steady frame already says a job finished,
	// and it is the picture the light is meant to rest in. It is also inside the write
	// budget by construction: the event's own writes are one repaint when it starts and one
	// when it ends, however long it holds.
	finishedEventHolds = time.Minute

	// scrollingTextEffect is WLED's Scrolling Text, effect id 122 on the firmware this was
	// measured against. It draws the segment's name and nothing else, which is why a word on
	// a panel is SetSegmentName plus this effect and no image anywhere.
	//
	// It requires a two-dimensional controller. On a 1D strip the firmware falls back to a
	// solid colour and the word simply does not appear — so a strip is never sent it; see
	// eventFor.
	scrollingTextEffect = 122

	// The word's effect parameters, one constant each so that tuning it is an edit rather than
	// an archaeology. Every one of them is PROVISIONAL except the grouping: they are the
	// firmware's taste and this build's guess at it, and the panel is where they get settled.
	//
	//   - speed 128 is the middle of the slider: the word scrolls at a reading pace rather
	//     than at a crawl or a blur.
	//   - the vertical position of 24 is where a 48-pixel-tall word sits in a 64-row panel
	//     without its descenders running off the bottom. ix is the effect's own vertical
	//     offset, measured on the live 256x64 panel.
	//   - font size 0 leaves the firmware's own glyph size, because the grouping below is what
	//     makes the letters big; two multipliers on one word is one too many.
	//   - grouping 4 is MEASURED, not guessed: grp multiplies the glyph size on a matrix, and
	//     on a 256x64 panel a grouping of 4 gives roughly 48-pixel-tall characters and about
	//     twelve of them across. That is the "big green letters" the operator asked for; a
	//     grouping of 1 is a few pixels tall, which is a word nobody can read from a doorway.
	//   - palette 0 is the controller's default palette, which is the one that takes its
	//     colours from the segment's own col array — the finished green written with the word.
	finishedTextSpeed    = 128
	finishedTextRow      = 24
	finishedTextCustom1  = 0
	finishedTextFontSize = 0
	finishedTextCustom3  = 0
	finishedTextCheck1   = 0
	finishedTextCheck2   = 0
	finishedTextCheck3   = 0
	finishedTextPalette  = 0
	finishedTextGrouping = 4
)

// event is one thing the printer did, as the light shows it: a word, the colour it is drawn in,
// and how long it holds the light before the steady frame comes back.
//
// It is a value and not a func, so that the decision that makes one is pure and a test can
// compare two of them. The writer, not this type, decides when the hold started.
type event struct {
	// word is the string the controller draws. It is the segment's name on the wire, so it is
	// also the field a body without it would clear.
	word string
	// colour is what the segment is drawn in: the effect takes its colours from the segment's
	// own palette, so the colour is written before the effect and not with it.
	colour wled.RGB
	// holds is how long the word keeps the light.
	holds time.Duration
}

// eventFor is the whole of the event list, as a pure function: the state the printer left, the
// state it arrived in, and the controller's geometry in; the picture to show and how long to
// show it, or nothing.
//
// There is one entry. A second is a case below plus whatever its animation needs — print start
// and a filament change are both wanted and neither is decided: the first needs a word or a
// pattern, a colour and a hold, and the second needs at least a decision about whether it is
// the same word for a pause and for a filament change, since they share their yellow. Adding
// one is a case and three constants; adding one whose picture is not a word is a second body in
// paintEvent. Neither is this build's decision to make.
//
// The rules, and each of them exists because of a way this could go wrong:
//
//   - A controller that reported no matrix gets nothing. The text effect needs a two-
//     dimensional target; on a strip the firmware falls back to a solid colour, so the request
//     would cost three writes and put no word anywhere. A strip's finished state is the solid
//     green it already shows, which is what the operator asked for on that light.
//   - Only PRINTING to FINISHED is an event. A restart is not a transition — the first
//     snapshot a process reads is a baseline and the driver never reports a change from it —
//     and a state that stood still for twenty ticks is not twenty events. Both of those are the
//     caller's to get right and both are pinned in the tests.
//   - A pause that ends in a finish is not an entry. Nobody has said whether a job that was
//     paused and then completed should celebrate, and a table that guessed would be inventing
//     a requirement.
func eventFor(from, to string, geometry panel) (event, bool) {
	if !geometry.matrix() {
		return event{}, false
	}

	if from == printer.StatePrinting && to == printer.StateFinished {
		return event{word: finishedWord, colour: render.ColourFinished, holds: finishedEventHolds}, true
	}

	return event{}, false
}

// finishedTextEffect is the effect the word runs with, every parameter of it.
//
// Every one is named, zeros included, because a controller applies only what a body names: an
// omitted parameter would leave the previous effect's value in place, and the panel would be
// showing somebody else's configuration of this effect. The values and their reasons are the
// constants above.
func finishedTextEffect() wled.Effect {
	return wled.Effect{
		ID:        scrollingTextEffect,
		Speed:     finishedTextSpeed,
		Intensity: finishedTextRow,
		Custom1:   finishedTextCustom1,
		Custom2:   finishedTextFontSize,
		Custom3:   finishedTextCustom3,
		Check1:    finishedTextCheck1,
		Check2:    finishedTextCheck2,
		Check3:    finishedTextCheck3,
		Palette:   finishedTextPalette,
	}
}

// heldEvent is one event the light is showing: the event, the moment its hold ends, and whether
// its body has actually reached the controller.
//
// It lives on the writer's own goroutine and is never touched by another, which is why it needs
// no lock — and why until is stamped there rather than where the event was fired: the clock a
// test moves is the writer's.
type heldEvent struct {
	event
	until   time.Time
	painted bool
}

// fire offers one event to the writer and never waits for it, the way submit offers a frame.
//
// Two things are decided here rather than in paintEvent, and both are rules rather than
// conveniences:
//
//   - **A borrowed segment is never given an event.** The module was pointed at one zone of
//     somebody else's controller to show a progress colour in, not to perform in. An animation
//     is unrequested — the operator asked for it on his own panel, and a zone of his desk light
//     is not that panel — and on a controller other people and processes also write to, the
//     safest place for a picture nobody asked for is nowhere. So the event stops here,
//     before it is stored, and says so once.
//   - **Nothing is queued.** One slot, latest wins, exactly like a frame: two events in quick
//     succession are one event, and the newer one is the true account of what the printer did.
func (w *writer) fire(ev event) {
	if w.segment != nil {
		w.logf("%s: the %q event was not drawn: segment %d on %s is borrowed, and an animation is not something this module puts on a light somebody else is using",
			name, ev.word, *w.segment, w.where)

		return
	}

	if w.fired.Swap(&ev) != nil {
		// An event was already waiting and the ring that put it there is still pending: a
		// second ring would buy nothing and would outlive the look it asked for.
		return
	}

	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// takeEvent moves a fired event into the light, stamped with the moment its hold ends.
//
// The deadline is measured from when the writer takes the event and not from when it was fired:
// the interval between the two is one tick at most, and a hold that started while the previous
// paint was still in flight would be a hold nobody could name a length for.
func (w *writer) takeEvent() {
	fired := w.fired.Swap(nil)
	if fired == nil {
		return
	}

	w.held = &heldEvent{event: *fired, until: w.now().Add(fired.holds)}
}

// deliverEvent paints one event and reports what happened to the status route, so that a light
// which took the word and a light which refused it are distinguishable without looking at the
// panel.
func (w *writer) deliverEvent(ctx context.Context, ev event) error {
	return w.report(func() error { return w.paintEvent(ctx, ev) })
}

// paintEvent draws one event: the word, big, in the event's colour, across the whole panel.
//
// Three requests, in this order, and the order is a measurement rather than a preference:
//
//  1. SetSegments gives segment 0 the whole panel, the event's colour and the word as its name,
//     in one body — which also puts the segment's effect back to solid and leaves whatever
//     effect parameters the controller held untouched. It carries the name even though the
//     next request clears it, because a body that names a segment this module is borrowing
//     must carry that segment's name, and writing it here keeps that true without a rule for
//     which bodies are exempt.
//  2. SetEffect puts the segment on Scrolling Text with every parameter of that effect, over
//     the same rectangle. A body of this shape names no "n", and a controller clears a
//     segment's name when a body writes that segment without one — measured on the operator's
//     desk light, where the first frames of a borrow left his zone unnamed.
//  3. SetSegmentName writes the word again, last, for that reason: it is what the effect draws,
//     and step 2 is exactly the body that would have cleared it.
//
// So the panel shows the steady picture for the length of two requests and then the word.
// Measured at 28 ms a request on a 256x64 panel, nobody watching sees it.
//
// It refuses a controller that reported no matrix, which eventFor has already refused: a strip
// would take these three requests and show one solid colour at the end of them, so the refusal
// is here as the second lock on the same door rather than as a path anything walks.
func (w *writer) paintEvent(ctx context.Context, ev event) error {
	geometry := w.panelOf()

	if !geometry.matrix() {
		return fmt.Errorf("the %q event was not drawn: %s reports no matrix, and a word needs one", ev.word, w.where)
	}

	bounds := wled.Bounds{Start: 0, Stop: geometry.width, StartY: 0, StopY: geometry.height}

	if err := w.light.SetSegments(ctx, []wled.SegmentWrite{{
		ID:       firstSegment,
		First:    0,
		Count:    geometry.width,
		FirstRow: 0,
		RowCount: geometry.height,
		On:       true,
		Colour:   ev.colour,
		Name:     ev.word,
	}}); err != nil {
		return err
	}

	if err := w.light.SetEffect(ctx, finishedTextEffect(),
		wled.WithEffectBounds(bounds), wled.WithEffectGrouping(finishedTextGrouping)); err != nil {
		return err
	}

	return w.light.SetSegmentName(ctx, ev.word)
}
