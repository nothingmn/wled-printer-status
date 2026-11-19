// The moment table, fired: which transition is which moment, and what one binding's entry for it
// puts on the light.
//
// # What was here before this file
//
// One entry, written as a special case in event.go: PRINTING to FINISHED gives the word FINISHED in
// the finished green for a minute. That is one row of the approved Events screen
// (docs/design/v2/events/), and the other seven rows were recorded, editable and reported while
// nothing acted on them. A table nothing reads is a screen that lies about what the light does.
//
// # The two decisions this file makes, and where each comes from
//
// **Which transition is which moment.** The eight moments are the screen's own list, and each one's
// `meaning` in momenttable.go is the sentence this mapping keeps:
//
//	print-finished       a job completed          PRINTING -> FINISHED
//	print-started        a job was taken and began  anything -> PRINTING
//	filament-change      waiting on a person for filament  anything -> ATTENTION
//	paused               a running job was paused  anything -> PAUSED
//	error-or-attention   error, or asking for a person  anything -> ERROR
//	print-cancelled      a job was stopped        anything -> STOPPED
//	printer-went-quiet   stopped reporting while a job was live  the reading going stale
//	light-lost           this module can no longer reach the light  **not fired — see below**
//
// Two of those overlap on paper and one of them is this file's own reading: ATTENTION is where the
// host reports a filament change (Appendix A says so, and says the mapping is a reading), and
// `error-or-attention`'s meaning also covers "began asking for a person". **Each transition fires
// exactly one moment**, so ATTENTION goes to `filament-change`, the more specific of the two, and
// `error-or-attention` fires on ERROR. Firing both would put two pictures on one light from one
// transition, and the second one to arrive would be the one seen — which is not a rule anybody
// chose. The alternative reading, both firing in table order, is a question for the operator and is
// named in this wave's report rather than decided here.
//
// **`light-lost` has no printer transition.** It is a condition of this module's own transport — it
// cannot reach the light — and the driver sees the printer, not the light. Firing it from the
// writer, where a failed write is known, would paint a picture on a light that cannot be reached and
// change the proved failure path (one attempt per doubling window) for no picture at all. What a
// moment *means* on an unreachable light, and where it ends, is a product decision this file does
// not invent: the row stays in the table, is editable, is reported, and does not fire.
//
// # What each `shows` does
//
//	Preset   the whole light for the moment: capture, run for `for`, put back. Refused on a
//	         borrowed zone, because a preset carries its own segments and the rest of that strip is
//	         somebody else's (writer.momentOf refuses it; event.go's fire refuses it again).
//	Word     the word on a panel. On a strip — which cannot carry two dimensions — the moment's
//	         colour instead, which is the locked fallback the state table already keeps.
//	Colour   that state's colour, its store override included, painted as the whole light.
//	Nothing  nothing at all: not even a repaint, so the steady picture is left exactly where it is.
//
// # And where "for" ends
//
// `for` is a duration or `forever`. A duration ends the moment on its own deadline and the steady
// picture takes the light back in the same pass, which is what the writer's hold already does for
// the finished word. `forever` means until the thing the moment was about changes — the state word
// moving — which is the screen's "until it resumes", "until it is cleared" and "until it answers"
// written as one rule.
package main

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nothingmn/wled-printer-status/internal/printer"
	"github.com/nothingmn/wled-printer-status/internal/render"
	"github.com/nothingmn/wled-printer-status/internal/text"
	"github.com/nothingmn/wled-printer-status/internal/wled"
)

// momentFor is the moment one transition of the printer's state word is, and false when the pair is
// not a moment at all.
//
// The pairs that answer false are the ones nobody has decided, and the most important of them is a
// pause that ends in a finish: whether a job that was paused and then completed should celebrate is
// not a question the table can answer, because the table has no row for it. A restart is not a
// transition either — the driver's first look fills in what the printer was without reporting a
// change — and that is the caller's own rule, not this function's.
func momentFor(from, to string) (string, bool) {
	switch to {
	case printer.StateFinished:
		if from == printer.StatePrinting {
			return momentPrintFinished, true
		}
	case printer.StatePrinting:
		if from != printer.StatePrinting {
			return momentPrintStarted, true
		}
	case printer.StatePaused:
		return momentPaused, true
	case printer.StateAttention:
		return momentFilamentChange, true
	case printer.StateError:
		return momentErrorAttention, true
	case printer.StateStopped:
		return momentPrintCancelled, true
	}

	return "", false
}

// momentLength is how long one moment's picture holds the light, and false for a moment that ends
// when its state does.
//
// `forever` is a length somebody chose and not the absence of one — a moment that holds until it
// resumes, is cleared, or the light answers — so it is answered as "no deadline" rather than as a
// zero, which would be a picture that lasted no time at all.
func momentLength(value string) (time.Duration, bool, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == durationForever {
		return 0, false, nil
	}

	length, err := time.ParseDuration(trimmed)
	if err != nil {
		return 0, false, fmt.Errorf("%q is not a length a moment can be shown for: %w", value, err)
	}

	if length <= 0 {
		return 0, false, fmt.Errorf("%q is not a length a moment can be shown for: it must be longer than nothing", value)
	}

	return length, true, nil
}

// momentOf is one moment as this binding's light will show it: the event the writer is to hold, the
// words a log line says about it, and the refusal when the light cannot carry it.
//
// It is the writer's own three facts — the geometry the controller reported, the store, and whether
// this binding borrows a zone — handed to momentEvent, which is the whole of the decision and is a
// function so that a test can make one without a writer.
func (w *writer) momentOf(entry momentEntry, frame render.Frame) (event, string, error) {
	ev, what, err := momentEvent(entry, frame, w.panelOf(), w.colourOfState, w.segment != nil)
	if errors.Is(err, errPresetWholeLight) {
		return event{}, "", fmt.Errorf(
			"the preset %q was not run: segment %d on %s is borrowed, and a preset carries its own segments, so running it would take the rest of that strip with it",
			entry.Which, *w.segment, w.where)
	}

	return ev, what, err
}

// errPresetWholeLight is a preset named where the light is not this module's to take whole.
var errPresetWholeLight = errors.New("a preset takes the whole light")

// momentEvent is one moment's entry as the event the writer holds, for a panel of this geometry,
// against a frame whose colour a strip falls back to.
//
// frame is the picture the driver decided this tick, and it is the moment's own colour whenever the
// entry does not name one: a moment about a paused printer is shown in the paused yellow, and one
// about a printer that has gone quiet in the dim red the frame for a stale reading is drawn in —
// which is the same colour rule the steady picture keeps, so the two cannot disagree.
//
// A Word entry is refused nothing: a strip cannot draw one, so it is given the moment's colour, which
// is the fallback §2.3 locks. A Preset entry is refused where the binding borrows a zone — the preset
// carries its own segments and the rest of that strip is somebody else's — with
// errPresetWholeLight, which the writer turns into a sentence naming the segment.
func momentEvent(entry momentEntry, frame render.Frame, geometry panel,
	colourOf func(state string) (wled.RGB, bool), borrowsZone bool,
) (event, string, error) {
	holds, _, err := momentLength(entry.For)
	if err != nil {
		return event{}, "", err
	}

	switch entry.Shows {
	case showsNothing:
		return event{}, "", nil

	case showsWord:
		word := strings.ToUpper(strings.TrimSpace(entry.Which))

		if geometry.matrix() {
			if _, err := text.Place(word, finishedWordScale, geometryOf(geometry)); err == nil {
				return event{word: word, colour: frame.Colour, holds: holds},
					fmt.Sprintf("the word %q for %s", word, phraseFor(entry.For)), nil
			}
		}

		// A strip, or a panel too small for the word at the scale this build draws it: the moment's
		// colour, which is what the light can honestly show and what the state table does with the
		// same problem.
		return plainMoment(frame, frame.Colour, holds),
			fmt.Sprintf("its colour for %s, because this light cannot carry the word %q", phraseFor(entry.For), word), nil

	case showsColour:
		colour, known := colourOf(entry.Which)
		if !known {
			return event{}, "", fmt.Errorf(
				"the moment's which is %q, which is not a state this build draws, so nothing was shown", entry.Which)
		}

		return plainMoment(frame, colour, holds),
			fmt.Sprintf("the %s colour for %s", entry.Which, phraseFor(entry.For)), nil

	case showsPreset:
		if borrowsZone {
			return event{}, "", errPresetWholeLight
		}

		return event{preset: strings.TrimSpace(entry.Which), holds: holds},
			fmt.Sprintf("its own preset %q for %s", entry.Which, phraseFor(entry.For)), nil
	}

	return event{}, "", fmt.Errorf("%q is not something a moment can show", entry.Shows)
}

// colourOfState is the colour a state is drawn in: the store's when somebody has overridden it, and
// the renderer's own when nobody has.
//
// It is one function for the state table's moment entries and the strip fallback, and it is the same
// rule the driver's colourOf keeps for a frame — the store first, and the code's table otherwise,
// never a copy of a default.
func (w *writer) colourOfState(state string) (wled.RGB, bool) {
	definition, known := findState(state)
	if !known {
		return wled.RGB{}, false
	}

	if w.store != nil {
		if held, ok := w.store.show(definition.name); ok && held.overridden() {
			return held.colour, true
		}
	}

	return definition.colour, true
}

// plainMoment is a moment drawn as one colour over the whole light: the frame's own range, with the
// moment's colour in it and nothing filled.
//
// The range is the frame's, so a strip is a run of its own length and a panel is the whole rectangle.
// Nothing is filled, so the picture is one colour and not a bar — a moment is a thing that happened,
// and a progress bar would be a claim about a job that the moment does not make.
func plainMoment(frame render.Frame, colour wled.RGB, holds time.Duration) event {
	plain := render.Frame{Colour: colour, Total: frame.Total, Percent: -1, Reason: frame.Reason}

	return event{plain: &plain, holds: holds}
}

// phraseFor is a moment's length as a log line says it, because "for forever" reads as a mistake and
// the sentence has to be one a person can act on.
func phraseFor(value string) string {
	if strings.TrimSpace(value) == durationForever {
		return "as long as its state lasts"
	}

	return value
}
