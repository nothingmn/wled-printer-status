// Package render decides what a one-dimensional WLED strip should show for one
// printer.
//
// It answers one question: given what the printer reported, the moment to judge
// it at, and how many LEDs the strip has, what colour is the strip and how much
// of it is filled. Decide is a pure function — no socket, no clock, no state kept
// between calls — so its whole behaviour sits in one table and can be tested
// without a printer or a controller.
//
// What it does not do is say how to paint. Effects, animations, brightness,
// geometry, text and the choice of WLED tier are separate units, and several of
// their requirements are still open.
package render

import (
	"math"
	"time"

	"github.com/nothingmn/wled-printer-status/internal/printer"
	"github.com/nothingmn/wled-printer-status/internal/wled"
)

// The colours, one per condition the operator settled. Every state the host
// reports has one, and the two conditions the host has no word for have their
// own.
//
// Four of them are the same yellow on purpose. The operator gave yellow to "one
// that is either idle, or waiting for a color change", and this build puts BUSY
// and PAUSED beside them because a machine heating for a print it has taken and a
// print waiting on a person are both waiting rather than working. The strip cannot
// tell those four apart by colour; telling them apart is an animation's job, and
// there is no animation here.
//
// The dim colours are a third of their bright counterpart, computed rather than
// typed, so "dim red" is provably the error red and not a different red somebody
// picked at another time.
var (
	// ColourPrinting is the green of a printer that is printing and healthy.
	ColourPrinting = wled.RGB{R: 0, G: 170, B: 0}
	// ColourIdle is the yellow of an idle printer: nothing in progress.
	ColourIdle = wled.RGB{R: 210, G: 165, B: 0}
	// ColourAttention is the yellow of a printer waiting on a person, which is
	// where a filament change is reported. It shares ColourIdle's value: the
	// operator's word for both was "yellow".
	ColourAttention = wled.RGB{R: 210, G: 165, B: 0}
	// ColourBusy is the yellow of a printer heating or calibrating for a print it
	// has already taken.
	ColourBusy = wled.RGB{R: 210, G: 165, B: 0}
	// ColourPaused is the yellow of a paused print, frozen where it stopped.
	ColourPaused = wled.RGB{R: 210, G: 165, B: 0}
	// ColourError is the red of a printer in error. The operator asked for error
	// codes to map to hues of red; nobody has written the table of codes and their
	// hues, so every error shows this one red until that table exists.
	ColourError = wled.RGB{R: 200, G: 0, B: 0}
	// ColourFinished is the green of a job that completed, held until the next one.
	ColourFinished = wled.RGB{R: 0, G: 170, B: 0}
	// ColourStopped is the grey of a job that was stopped: neither success nor
	// failure, so neither green nor red.
	ColourStopped = wled.RGB{R: 80, G: 80, B: 80}
	// ColourReady is a dim green: ready to print, with nothing running.
	ColourReady = dimmed(ColourPrinting)
	// ColourUnknown is a dim blue. A printer that has said nothing must not look
	// like a verdict, so it gets neither green nor red nor yellow.
	ColourUnknown = dimmed(wled.RGB{R: 0, G: 60, B: 200})
	// ColourDisconnected is a dim red for a printer the host has no session for.
	ColourDisconnected = dimmed(ColourError)
	// ColourStale is a dim red for a printer that is connected but has said
	// nothing new for printer.StaleAfter. It is the same dim red as disconnected:
	// either way the reading is a memory, and a person needs to know that.
	ColourStale = dimmed(ColourError)
)

// dim is the fraction of each channel a dim colour keeps. A third on a strip is
// clearly the same hue and clearly weaker than the bright one.
const dim = 0.35

// dimmed returns colour at a third of its brightness.
func dimmed(colour wled.RGB) wled.RGB {
	return wled.RGB{
		R: uint8(float64(colour.R) * dim),
		G: uint8(float64(colour.G) * dim),
		B: uint8(float64(colour.B) * dim),
	}
}

// Reason names the rule that decided one Frame. It is short, machine-readable and
// meant for a log line or a test, never for a person looking at the light.
type Reason string

// The reasons a Frame can carry. Each names one rule of Decide, so a caller can
// tell "no job to show" from "a job whose progress this build cannot believe".
const (
	// ReasonDisconnected: the host holds no session for the printer.
	ReasonDisconnected Reason = "disconnected"
	// ReasonStale: the printer is connected but has reported nothing new for
	// printer.StaleAfter.
	ReasonStale Reason = "stale"
	// ReasonUnknownState: the printer said nothing, or a word this build has no
	// reading of.
	ReasonUnknownState Reason = "unknown-state"
	// ReasonNoLiveJob: the state is a real one with no job on the bed, so there is
	// no progress to show.
	ReasonNoLiveJob Reason = "not-a-live-job"
	// ReasonProgressUnknown: a job is running but the printer has not reported a
	// progress this build may present as current.
	ReasonProgressUnknown Reason = "progress-unknown"
	// ReasonProgress: the fill is the job's live progress.
	ReasonProgress Reason = "progress"
	// ReasonPausedFrozen: the fill is the progress a paused print stopped at.
	ReasonPausedFrozen Reason = "paused-frozen"
)

// Config is what the decision needs that the printer does not report.
type Config struct {
	// LEDs is how many LEDs the strip has, counted from the first. Zero or less
	// is a strip with nothing on it, and lights nothing.
	LEDs int
}

// Frame is what the strip should show at one moment: one colour, how much of the
// range carries the completed part of a job, and why.
type Frame struct {
	// Colour is the colour of the whole range. It is the state's colour even when
	// nothing is filled: an idle strip is yellow and a printer that has gone quiet
	// is dim red, because a strip that went dark would be saying something the
	// printer did not.
	Colour wled.RGB

	// Lit is how many LEDs, counted from the first, carry the completed part of
	// the job in Colour. Zero means there is no completed part to show, and then
	// the whole range is Colour: a bar of nothing would be indistinguishable from
	// a strip that is off, and a paused print at 0 % has the same answer.
	Lit int

	// Total is how many LEDs the range holds.
	Total int

	// Reason names the rule that fired.
	Reason Reason
}

// Unfilled is the colour of the LEDs past Lit, when there is a bar to draw.
//
// It is not the colour of an empty range. With Lit at zero there is no completed
// part to mark a fill against, and the whole range is Colour (see Frame.Lit): a
// caller that painted Lit LEDs in Colour and nothing else would show a dark strip
// for an idle printer and for a printer that had gone quiet, which is the opposite
// of what those two are meant to say.
//
// PROVISIONAL. Q-15 is open: whether the remainder is off or a dim wash of the
// state colour was never decided by the operator, and this build takes off
// because it makes the boundary sharpest at any brightness. To take the other
// answer, replace the return below with "return dimmed(f.Colour)" — one line.
func (f Frame) Unfilled() wled.RGB {
	return wled.RGB{}
}

// Decide returns what the strip should show for one printer snapshot.
//
// The rules are applied in this order, and the order is the point: a printer that
// has gone quiet must not be mistaken for one that is printing, and the progress
// it last reported must not be shown as a bar still filling.
//
//  1. Not connected, or stale at at → ColourDisconnected or ColourStale, nothing
//     filled, and the reason says which.
//  2. Otherwise the state's settled colour.
//  3. The fill is the snapshot's guarded live progress. If that reading refuses,
//     nothing is filled and the reason says so; no percentage is invented and none
//     remembered is reused.
//  4. A state with no job on the bed fills nothing: idle, ready, finished,
//     stopped, unknown and an unrecognised word all show their colour alone.
//  5. A paused print keeps the fraction it stopped at, which is that same guarded
//     progress value, provided the reading is still live.
//
// at is the moment to judge the snapshot at, so a caller holding a snapshot can
// ask what the strip would have shown a moment later and a test needs no clock.
// It is never read from the process: there is no call to time.Now in this package.
func Decide(snapshot printer.Snapshot, at time.Time, cfg Config) Frame {
	total := cfg.LEDs
	if total < 0 {
		total = 0
	}

	frame := Frame{Total: total}

	// Rule 1 first. Everything below is a claim about the printer now, and these
	// two conditions are the ones under which no such claim may be made.
	if !snapshot.Connected {
		frame.Colour, frame.Reason = ColourDisconnected, ReasonDisconnected

		return frame
	}

	if staleAt(snapshot, at) {
		frame.Colour, frame.Reason = ColourStale, ReasonStale

		return frame
	}

	rule := ruleFor(snapshot.State.Value, snapshot.State.Known)
	frame.Colour = rule.colour

	switch {
	case !rule.named:
		frame.Reason = ReasonUnknownState

		return frame
	case !rule.job:
		frame.Reason = ReasonNoLiveJob

		return frame
	}

	progress, ok := snapshot.LiveProgress()
	if !ok {
		// The guarded reading refused. Nothing is filled: 0 % would be a figure
		// nobody measured and 100 % a guess about a job that may not have started.
		frame.Reason = ReasonProgressUnknown

		return frame
	}

	frame.Lit = lit(progress, total)
	frame.Reason = ReasonProgress

	if snapshot.State.Known && snapshot.State.Value == printer.StatePaused {
		frame.Reason = ReasonPausedFrozen
	}

	return frame
}

// rule is what one state word means to the strip.
type rule struct {
	colour wled.RGB
	// job reports that a job is on the bed, so the fill may describe it.
	job bool
	// named reports that this build has a reading of the word at all.
	named bool
}

// ruleFor is the operator's table. It is a switch and not a package-level map so
// that no mutable table exists in this package: the only things Decide reads are
// its arguments and the colour values above.
//
// The four states with a job on the bed are the four internal/printer treats as
// live (BUSY, PRINTING, PAUSED, ATTENTION). Everything else — IDLE, READY,
// FINISHED, STOPPED, ERROR, UNKNOWN — has no progress to show, and a word this
// build has never seen is not assumed to have one.
func ruleFor(state string, known bool) rule {
	if !known {
		return rule{colour: ColourUnknown}
	}

	switch state {
	case printer.StatePrinting:
		return rule{colour: ColourPrinting, job: true, named: true}
	case printer.StateBusy:
		return rule{colour: ColourBusy, job: true, named: true}
	case printer.StatePaused:
		return rule{colour: ColourPaused, job: true, named: true}
	case printer.StateAttention:
		return rule{colour: ColourAttention, job: true, named: true}
	case printer.StateIdle:
		return rule{colour: ColourIdle, named: true}
	case printer.StateReady:
		return rule{colour: ColourReady, named: true}
	case printer.StateFinished:
		return rule{colour: ColourFinished, named: true}
	case printer.StateStopped:
		return rule{colour: ColourStopped, named: true}
	case printer.StateError:
		return rule{colour: ColourError, named: true}
	case printer.StateUnknown:
		return rule{colour: ColourUnknown, named: true}
	}

	return rule{colour: ColourUnknown}
}

// staleAt reports whether the newest fact in the snapshot is older than
// printer.StaleAfter at the moment asked about, or whether it never had one.
//
// It re-applies the rule internal/printer applies rather than only trusting
// snapshot.Stale, because at may be later than the moment the snapshot was taken:
// a snapshot kept for a minute is a minute older than it says, and a caller that
// kept one across a silence would otherwise show a green strip for a printer that
// stopped answering.
func staleAt(snapshot printer.Snapshot, at time.Time) bool {
	if snapshot.Stale || snapshot.LastChange.IsZero() {
		return true
	}

	return at.Sub(snapshot.LastChange) > printer.StaleAfter
}

// lit is how many of total LEDs a progress figure fills.
//
// PROVISIONAL. Q-17 is open: floor or nearest. This build floors, so the bar never
// claims more than the printer reported — at 99.9 % of 48 the strip shows 47, not a
// bar that looks finished a moment early. To take the other answer, replace the
// math.Floor call with math.Round — one call.
//
// A percentage outside 0..100 is clamped rather than trusted: a firmware that
// reports 101 must not light an LED that does not exist.
func lit(progress float64, total int) int {
	if total <= 0 || !(progress > 0) {
		// !(progress > 0) is false for NaN as well as for zero and negatives.
		return 0
	}

	if progress >= 100 {
		return total
	}

	filled := int(math.Floor(progress / 100 * float64(total)))
	if filled > total {
		return total
	}

	return filled
}
