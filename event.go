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
// # The word is drawn in pixels, and that is a correction
//
// The first build used WLED's own Scrolling Text effect (id 122) and put the word in the
// segment's name, which is the only string that effect can draw. On the operator's panel it
// does not honour the segment it is given: four different groupings, every parameter named, and
// each attempt lit only the first few columns on the left of the 256-wide panel — the operator
// watched all four and reported the same fragment. So the word is not a name and not an effect
// any more. It is a glyph table this repository owns (internal/text), rasterised onto the panel
// by this module and written one LED at a time through wled.Client.Paint. Nothing in this file
// asks a controller to draw anything.
//
// The list is deliberately one entry long. Print start and a filament change are both wanted
// and neither has an animation or a duration decided, and inventing either would be a product
// decision made quietly in a code change. What one entry needs is written on eventFor.
package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/nothingmn/wled-printer-status/internal/printer"
	"github.com/nothingmn/wled-printer-status/internal/render"
	"github.com/nothingmn/wled-printer-status/internal/text"
	"github.com/nothingmn/wled-printer-status/internal/wled"
)

const (
	// finishedWord is what the panel spells out when a print completes.
	//
	// Every character of it has to be in internal/text's font, which holds the capitals, the
	// digits, a space, a full stop, a hyphen and a percent sign. A word outside that set is
	// refused by the rasteriser rather than drawn short.
	finishedWord = "FINISHED"

	// finishedWordScale is how many pixels of the panel one pixel of the glyph table becomes.
	//
	// At 5, the eight glyphs and their seven one-pixel gaps are 47 glyph columns, so the word is
	// 235 pixels across and 35 down — 5*47 and 5*7. The operator's panel is 256x64, which leaves
	// 21 columns and 29 rows of slack, so the word sits at column 10 and row 14 with room above
	// and below it; 35 pixels of height on a panel 64 high is the "letters you can read from a
	// doorway" this is for. internal/text's tests hold that arithmetic. A panel too small for it
	// is given no word rather than a smaller or a clipped one — see eventFor.
	finishedWordScale = 5

	// finishedEventHolds is how long the word holds the light before the steady finished
	// frame takes it back: one minute.
	//
	// Chosen by this seat, not settled with the operator, and named here so that it is one
	// line to change. A minute is long enough that somebody who looks up when the printer
	// stops sees the word rather than a green panel, and short enough that the panel is not
	// still saying FINISHED an hour later — the steady frame already says a job finished,
	// and it is the picture the light is meant to rest in. It is also inside the write
	// budget by construction: the event's own writes are one painting when it starts and one
	// when it ends, however long it holds.
	finishedEventHolds = time.Minute
)

// finishedWordBackground is what the panel is painted behind the word.
//
// It is the colour the steady frame already gives the part of the range a job has not filled,
// read from render rather than written out here so that the two pictures cannot drift: the
// event stands in front of that picture, and a background of a different colour would be the
// light changing twice for one thing. Today that colour is black.
//
//nolint:gochecknoglobals // one value, computed once from the renderer's own rule.
var finishedWordBackground = render.Frame{}.Unfilled()

// The refusals a preview answers with, so that the route can tell them apart without reading a
// sentence. Both mean "this light cannot show the word", and they are separate because what the
// operator does about each is different: one is a setting, and one is the hardware it names.
var (
	// ErrPreviewBorrowedSegment means this writer drives one segment of a controller somebody
	// else also drives. An animation nobody asked for is the one thing a zone's owner would not
	// have agreed to, so the preview refuses exactly where the event itself refuses.
	ErrPreviewBorrowedSegment = errors.New("the module is driving a borrowed segment, and an animation is not something it puts on a light somebody else is using")

	// ErrPreviewNotDrawable means the controller cannot show this word: it reported no matrix,
	// so it is a strip, or its panel is too small for the word at finishedWordScale.
	ErrPreviewNotDrawable = errors.New("this light cannot carry the word")
)

// event is one thing the printer did, as the light shows it: a word, the colour it is drawn in,
// and how long it holds the light before the steady frame comes back.
//
// It is a value and not a func, so that the decision that makes one is pure and a test can
// compare two of them. The writer, not this type, decides when the hold started.
type event struct {
	// word is what the panel is asked to spell, in the font internal/text carries. paintEvent
	// rasterises it; no controller is asked to draw a string.
	word string
	// colour is what the word's lit pixels are drawn in. Every other pixel of the panel is
	// finishedWordBackground for the length of the hold.
	colour wled.RGB
	// holds is how long the word keeps the light.
	holds time.Duration
}

// finishedWordEvent is the picture a completed print is given, and the one the preview route
// fires. It is a function, and both paths call it, so that what an operator previews is
// provably what his panel will do when the print ends.
func finishedWordEvent() event {
	return event{word: finishedWord, colour: render.ColourFinished, holds: finishedEventHolds}
}

// eventFor is the whole of the event list, as a pure function: the state the printer left, the
// state it arrived in, and the controller's geometry in; the picture to show and how long to
// show it, or nothing.
//
// There is one entry. A second is a case below plus whatever its animation needs — print start
// and a filament change are both wanted and neither is decided: the first needs a word or a
// pattern, a colour and a hold, and the second needs at least a decision about whether it is
// the same word for a pause and for a filament change, since they share their yellow. Adding
// one is a case and three constants; adding one whose picture is not this word is a second body
// in paintEvent. Neither is this build's decision to make.
//
// The rules, and each of them exists because of a way this could go wrong:
//
//   - A controller that reported no matrix gets nothing. The word is drawn in pixels on a
//     two-dimensional panel, and a strip has no second dimension to draw it in: the panel's
//     index arithmetic would fold the letters into one run of LEDs. A strip's finished state is
//     the solid green it already shows, which is what the operator asked for on that light.
//   - A panel too small to hold the word at finishedWordScale gets nothing either. The other
//     answers — a smaller word, a broken one, a clipped one — are all decisions this build has
//     not been given, so it says nothing rather than pick. Silence is the same answer the strip
//     gets, and for the same reason: the steady finished frame is a true picture of what
//     happened, and a light is not the place to argue about a panel's size. The preview route
//     says the size out loud, because a person who asked for the word deserves the sentence.
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

	if from != printer.StatePrinting || to != printer.StateFinished {
		return event{}, false
	}

	ev := finishedWordEvent()

	// The word has to fit the panel it is going on, and that is arithmetic this package can do
	// before anything is sent; a word that does not fit would otherwise cost a failed write, a
	// widening retry and a hold that shows the steady frame anyway.
	if _, err := text.Place(ev.word, finishedWordScale, geometryOf(geometry)); err != nil {
		return event{}, false
	}

	return ev, true
}

// geometryOf is a panel as the rasteriser reads one, so that the one place that decides where a
// word sits is internal/text and this package only says how big the light is.
func geometryOf(geometry panel) text.Geometry {
	return text.Geometry{Width: geometry.width, Height: geometry.height}
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

	// frame is the picture the panel was given when the hold started, and repush is when the
	// next repeat of it is due. A still picture has to outlive the controller's realtime
	// timeout, so it is pushed again every pixelRepeat for as long as the hold lasts — see
	// pixels.go for the rate and the arithmetic behind it.
	frame  []wled.RGB
	repush time.Time

	// pushFails counts repeats this hold has failed, so that a controller which has stopped
	// taking datagrams costs one line and not one a second. The hold continues: the picture
	// may still be on the panel, and the frame after the event is where a dead controller is
	// reported.
	pushFails int
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

// preview fires one event now, on the operator's behalf, and says whether the light will show
// it. POST /preview-finished is the only caller.
//
// It is the same event a finished print fires — finishedWordEvent, not a copy of it — so that
// looking at the preview tells a person what his panel will do when the print ends, rather than
// what some other picture would look like.
//
// It refuses, with a typed error the route turns into a code, exactly the cases in which the
// light cannot be asked for the word:
//
//   - a borrowed segment, for the reason fire gives: somebody else's light is not this module's
//     to animate;
//   - a controller that reported no matrix, which is a strip;
//   - a panel too small for the word at finishedWordScale.
//
// What it does not do is write anything itself. It stores the event in the writer's one slot
// and rings the writer's bell, so the pixels go out on the writer's own goroutine — behind the
// same gate, the same backoff and the same one-line-per-widening failure log as every other
// write. A preview therefore cannot reach the controller in a way a real event could not, and
// it cannot outlast the hold, because the writer's own deadline is what ends it.
//
// It stores rather than swapping the way fire does: a preview is something a person just asked
// for, so it replaces an event that was waiting rather than being dropped because the printer
// happened to fire one a moment earlier. If an event is on the light at that moment, this one is
// taken when that hold ends, and the reporter is told the light took it.
func (w *writer) preview(ev event) error {
	if w.segment != nil {
		return fmt.Errorf("%w: segment %d on %s", ErrPreviewBorrowedSegment, *w.segment, w.where)
	}

	geometry := w.panelOf()
	if !geometry.matrix() {
		return fmt.Errorf("%w: %s reported no matrix, and a word needs a two-dimensional panel",
			ErrPreviewNotDrawable, w.where)
	}

	if _, err := text.Place(ev.word, finishedWordScale, geometryOf(geometry)); err != nil {
		return fmt.Errorf("%w: %w", ErrPreviewNotDrawable, err)
	}

	w.fired.Store(&ev)

	select {
	case w.wake <- struct{}{}:
	default:
	}

	return nil
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
//
// The frame that went out is kept on the hold, because the hold pushes that same frame again
// every pixelRepeat until it ends: the picture does not move, and only its repetition is what
// keeps the panel drawing it.
func (w *writer) deliverEvent(ctx context.Context, ev event) error {
	var frame []wled.RGB

	err := w.report(func() error {
		var pushErr error

		frame, pushErr = w.paintEvent(ctx, ev)

		return pushErr
	})
	if err != nil {
		return err
	}

	if w.held != nil {
		w.held.frame = frame
		w.held.repush = w.now().Add(pixelRepeat)
	}

	return nil
}

// paintEvent draws one event: the word, in the event's colour, on a dark panel, and returns the
// picture it put on the panel so that the hold can push it again.
//
// # The sequence, and why it is this one
//
//  1. **Establish the panel.** One SetSegments body gives segment 0 the whole panel, switched on,
//     in finishedWordBackground, with the word as the segment's own name — and switches segment
//     1, the frame's remainder, off. It writes no brightness and no power: both are
//     controller-wide, the operator set them, and a picture does not get to move them. What it
//     does write is the geometry the panel falls back to when live mode ends, and the name the
//     state read reports while the word is up: a datagram carries neither.
//
//     Segment 1 is not tidiness. It is the frame's remainder, the black part of a progress bar,
//     and the panel draws segments in order — so a remainder left on from the frame before the
//     event is drawn **over** the word the moment live mode ends. Measured on the live panel the
//     first time this event ran: segment 1 held columns 28..256 in black and the word was a
//     fragment on the left, which is the same fragment this whole change exists to stop showing.
//
//     The name travels with it because /json/state then says which picture the panel is holding,
//     and a body written without one clears the name that was there.
//
//  2. **Push the word as pixels.** One DDP frame — the whole panel, 16,384 pixels, 35 datagrams —
//     in the event's colour where the rasteriser lit a pixel and in finishedWordBackground
//     everywhere else. A JSON body carrying the same colours one LED at a time is accepted by
//     this firmware and drawn by nothing, which is why the pixels do not go that way; pixels.go
//     has the four measurements and what the controller will not report about them.
//
//     The frame goes out in requests of about 490 bytes rather than 16,384 colours in a
//     document, so the panel is never showing half a picture: a datagram names where its pixels
//     belong, and the frame is pushed with the push flag on its last one, which is when the
//     controller draws it.
//
//  3. **Confirm it was taken.** One read of /json/info, because live=true is the only thing this
//     transport can prove. A controller that answers live=false has drawn its own segments and
//     not the word, and that is one line in the log rather than a silence.
//
// A strip, and a panel too small for the word, are refused here as well as in eventFor: a
// second lock on the same door costs one comparison, and the preview route reaches this function
// by a path the decision table never walks.
func (w *writer) paintEvent(ctx context.Context, ev event) ([]wled.RGB, error) {
	geometry := w.panelOf()

	if !geometry.matrix() {
		return nil, fmt.Errorf("the %q event was not drawn: %s reports no matrix, and a word needs one", ev.word, w.where)
	}

	if _, err := text.Place(ev.word, finishedWordScale, geometryOf(geometry)); err != nil {
		return nil, fmt.Errorf("the %q event was not drawn: %w", ev.word, err)
	}

	lit, err := text.Raster(ev.word, finishedWordScale, geometryOf(geometry))
	if err != nil {
		return nil, fmt.Errorf("the %q event was not drawn: %w", ev.word, err)
	}

	if err = w.light.SetSegments(ctx, []wled.SegmentWrite{{
		ID:       firstSegment,
		First:    0,
		Count:    geometry.width,
		FirstRow: 0,
		RowCount: geometry.height,
		On:       true,
		Colour:   finishedWordBackground,
		Name:     ev.word,
	}, {
		// The frame's remainder, switched off rather than coloured: an off segment draws nothing,
		// so the word underneath it is not covered, and a body that left it on would put a black
		// band over the right-hand end of the word.
		ID: remainderSegment,
		On: false,
	}}); err != nil {
		return nil, err
	}

	frame := wordFrame(geometry, lit, ev.colour)

	if err = w.pushWord(frame); err != nil {
		return nil, fmt.Errorf("the %q event's pixels did not reach %s: %w", ev.word, w.where, err)
	}

	w.confirmRealtime(ctx)

	return frame, nil
}
