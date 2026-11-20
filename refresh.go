// Refresh everything: POST /refresh.
//
// # What the button is
//
// The operator's own words, and the whole of the requirement: "to the right of `Pause Everything` on
// all screens, lets add a new button `Refresh Everything` which will push the current state to all
// WLED devices, all segments. full refresh of all lights based on the current state that second."
//
// # Why it cannot be a loop over POST /preview-*
//
// The steady pass deliberately does not write a picture that has not changed, and that suppression
// is what keeps a module driving six lights from writing six bodies a second for a day. A button
// whose whole point is the redundant write therefore has to bypass that comparison, and the only
// place that comparison exists is the writer's own goroutine — the one goroutine that talks to one
// controller (AGENTS.md's rule, and drive.go's `last`). So this is not a second writer and not a
// second composer:
//
//   - the **compose** happens on the driver's goroutine, through drive.go's `compose`, which is the
//     same function a tick calls — the reading is taken at this moment, not the frame that was
//     painted last;
//   - the **write** happens on the writer's goroutine, through `deliver` and `enterComposed`, which
//     are the same two paths a tick's picture takes, chosen by the same `drawablePanel` rule.
//
// What the two of them do *not* do is consult the frame the writer is holding as "already shown",
// which is the entire difference between this and the next tick.
//
// # The scope is every binding, and only the segments this module owns
//
// "All segments" is not "every segment on the device". This module borrows one segment per binding,
// and a sweep over every segment of a controller would paint the operator's own zones out of a
// light he set up in WLED's interface. So the sweep is over the bindings the module drives, and
// each one writes through the writer that already knows whose segment it is: a whole-light writer
// writes its own segments, a borrowed-segment writer writes that one id and nothing else.
//
// # What is left alone, and said to be left alone
//
//   - **A light inside a moment.** A word, a preset or a moment's colour is a picture with a
//     deadline, and a steady frame painted under it is the picture gone early. The answer says the
//     light was left alone and names the moment, because the one thing this route must never do is
//     tell a person a light was refreshed while it is still showing something else.
//   - **A light holding a state's word**, which is the same kind of picture for the same reason.
//   - **A light an identify is flashing**, which is the module's own ten seconds of on and off.
//   - **A paused light.** A pause is one write and then nothing at all, and a refresh is writes.
//
// Each of those answers `skipped` with the sentence that says which, so a sweep over six bindings
// reads as six facts rather than as one "done".
//
// # Why the answer is per light and per segment
//
// A person presses this because a light is showing something stale. The useful answer is which
// lights took the picture and which did not, by name — a controller measured answering HTTP in 7.4
// seconds beside one at 0.03 s is the reason the sweep is sequential with its own timeout per light
// rather than one paragraph of optimism. A light that did not answer is `failed` with the reason; a
// light the sweep never reached is `not-reached` rather than silently absent.
package main

import (
	"context"
	"fmt"
	"net/http"
	"sync/atomic"
	"time"
)

const (
	// refreshPerLight is one binding's whole budget: waiting for its goroutine to take the request,
	// the read of the controller the picture may need, and the write. It is generous beside the
	// writer's own two-second request timeout because what it is really bounding is a *queue* — a
	// writer that is in the middle of an event's repeat, or waiting out a failure window, has to be
	// given room to finish before this light is called unanswered.
	refreshPerLight = 8 * time.Second

	// refreshBudget is the whole sweep's budget, and it is inside the host's own ten-second limit
	// on an inbound call. A sweep that ran past it would be answered with a timeout and no result at
	// all, which is the one outcome this route exists to avoid: a light not reached is named as not
	// reached, and the button can be pressed again a moment later.
	refreshBudget = 9 * time.Second
)

// What one binding's refresh did. These are the words the answer carries and the words the screen
// draws, so they are constants rather than four sentences written at four call sites.
const (
	// refreshRefreshed is a write that the controller took.
	refreshRefreshed = "refreshed"

	// refreshSkipped is a light the sweep deliberately did not write: inside a moment, holding a
	// word, being flashed, or paused.
	refreshSkipped = "skipped"

	// refreshFailed is a light that did not take the picture, with the reason beside it.
	refreshFailed = "failed"

	// refreshUnreached is a light the sweep's own budget ran out before.
	refreshUnreached = "not-reached"
)

// What a refreshed binding actually carried, because the two are different writes to a person
// reading the screen: a JSON body that sets segments, and a pixel stream that a panel draws only
// while it is live.
const (
	refreshWroteSegments = "segments"
	refreshWrotePixels   = "pixels"
)

// sweepingRefresh is true while one sweep is running.
//
// The press is a person's, and two of them a second apart are two sweeps writing over each other on
// the same controllers — so the second is refused with a sentence rather than started. It is one
// flag for the process and not one per binding: the scope of the button is "everything", and a
// second sweep that only overlapped half the first would be the same fault in a smaller shape. The
// screen draws a busy state while its own request is in the air; this is what holds when two
// browsers, or a double press on a lagging page, arrive at once.
//
//nolint:gochecknoglobals // one sweep at a time for the whole process, by design.
var sweepingRefresh atomic.Bool

// refreshDone is what one binding's forced repaint did, as the writer's goroutine answers it.
type refreshDone struct {
	outcome string
	wrote   string
	why     string
}

// refreshAsk is one press's request on one binding, as the route leaves it for that binding's
// driver, and the one answer it gets back. The reply is buffered so that a driver which finishes a
// compose for a caller that has gone away does not block the goroutine that decides frames.
type refreshAsk struct{ reply chan refreshDone }

// refreshCall is the same request once the driver has composed this moment's picture: the frame,
// the state it is about, and the moment this module began showing that state — which is what the
// writer judges a state's word against.
type refreshCall struct {
	scene scene
	reply chan refreshDone
}

// refresh asks this binding's driver to compose a picture now and its writer to write that picture
// whether or not the light already shows it.
//
// It never composes and never writes anything itself. The request goes into the driver's one slot
// and that goroutine does the compose; the writer's slot carries the composed scene to the goroutine
// that owns the controller. It is the hand-over an identify makes, for the same reason.
//
// A request that is not taken inside the caller's deadline is answered as a failure rather than
// left to run later: a forced write that landed ten seconds after the person was told the light did
// not answer would be a repaint nobody asked for. The slot is drained in that case, the same way
// identify.go drains it.
func (d *driver) refresh(ctx context.Context) refreshDone {
	ask := &refreshAsk{reply: make(chan refreshDone, 1)}

	select {
	case d.refreshes <- ask:
	default:
		return refreshDone{outcome: refreshFailed, why: fmt.Sprintf(
			"%s already has a refresh waiting, so this one was not taken", d.where())}
	}

	select {
	case done := <-ask.reply:
		return done

	case <-ctx.Done():
		select {
		case <-d.refreshes:
		default:
		}

		return refreshDone{outcome: refreshFailed, why: fmt.Sprintf(
			"%s did not take the picture in the time the sweep allows for one light: it is not answering, or the watch that drives it is stopping", d.where())}
	}
}

// serveRefresh composes this moment's picture on the driver's own goroutine and hands it to the
// writer for the forced write.
//
// The reading is taken here, now, rather than reused from the tick that just ran: "based on the
// current state that second" is the requirement, and a frame from a second ago is exactly what the
// steady pass would have written anyway.
func (d *driver) serveRefresh(ctx context.Context, ask *refreshAsk) {
	snapshot := d.source.Snapshot()

	frame, state, since := d.compose(snapshot, d.clock())

	ask.reply <- d.writer.forceWrite(ctx, scene{frame: frame, state: state, since: since})
}

// clock is when a refresh composes at. A tick is handed its instant; a refresh asks for one.
func (d *driver) clock() time.Time {
	if d.now != nil {
		return d.now()
	}

	return time.Now()
}

// where is the controller this driver's binding writes, for the sentences this file writes.
func (d *driver) where() string {
	if d.writer == nil {
		return "the light"
	}

	if d.writer.where != "" {
		return d.writer.where
	}

	return "the light"
}

// forceWrite hands one composed picture to the writer's goroutine and waits for what it did.
//
// It is the writer's own slot rather than a direct call because the writer is the only goroutine
// that may talk to this controller, and because the answer has to be the write's outcome: "did the
// controller take it" is the one fact a person pressing this button is asking for.
func (w *writer) forceWrite(ctx context.Context, sc scene) refreshDone {
	call := &refreshCall{scene: sc, reply: make(chan refreshDone, 1)}

	select {
	case w.refreshes <- call:
	default:
		return refreshDone{outcome: refreshFailed, why: fmt.Sprintf(
			"%s already has a refresh waiting, so this one was not taken", w.where)}
	}

	// The bell every wait in the loop selects on, so an idle writer notices at once rather than at
	// its next heartbeat.
	select {
	case w.wake <- struct{}{}:
	default:
	}

	select {
	case done := <-call.reply:
		return done

	case <-ctx.Done():
		select {
		case <-w.refreshes:
		default:
		}

		return refreshDone{outcome: refreshFailed, why: fmt.Sprintf(
			"%s did not take the picture in the time the sweep allows for one light: it is not answering, or the watch that drives it is stopping", w.where)}
	}
}

// serveRefresh runs the forced repaint a refresh has left, if one is waiting, and reports whether
// it did — in which case the pass it was asked in writes nothing else.
//
// It sits near the top of the writer's loop, after an identify and before the pause: after, because
// a flash owns the light for its ten seconds; before, because a paused light has to be *answered*
// with the reason rather than left waiting, and because the answer to a person who pressed a button
// must not wait for a pause that may have no end.
func (w *writer) serveRefresh(ctx context.Context) bool {
	select {
	case call := <-w.refreshes:
		call.reply <- w.repaintNow(ctx, call.scene)

		return true
	default:
		return false
	}
}

// repaintNow writes one picture to this controller whatever the light already shows, after deciding
// whether the light is one this sweep may write at all.
//
// The skip is not a courtesy: a word, a preset, a moment's colour and a pause are all pictures that
// own the light for a length of time this writer was told to respect, and a steady frame written
// under one of them is that picture ending early — the operator would see the refresh button
// destroy the thing the light was showing, twice, and then read an answer saying it had been
// refreshed.
func (w *writer) repaintNow(ctx context.Context, sc scene) refreshDone {
	if end, paused := w.pauseNow(); paused {
		return refreshDone{outcome: refreshSkipped, why: fmt.Sprintf(
			"%s is paused%s, and a pause is one write and then nothing at all: nothing was written", w.where, end.phrase())}
	}

	if until, flashing := w.flashing(); flashing {
		return refreshDone{outcome: refreshSkipped, why: fmt.Sprintf(
			"%s is being flashed by an identify until %s, and the steady picture waits for that to finish: nothing was written",
			w.where, until.UTC().Format(time.RFC3339))}
	}

	if w.held != nil {
		return refreshDone{outcome: refreshSkipped, why: fmt.Sprintf(
			"%s is showing the %q moment%s, and a light inside a moment is left alone: refreshing under it would paint the moment away",
			w.where, w.held.label(), holdPhrase(w.held.holds))}
	}

	if w.spoken != nil {
		return refreshDone{outcome: refreshSkipped, why: fmt.Sprintf(
			"%s is holding the word %q for its state, which is the same kind of picture as a moment, so it is left alone",
			w.where, w.spoken.word)}
	}

	// A panel showing a job is pixels this module drew, and a full refresh of it is the establishing
	// body and the pixels again — the same two writes the steady pass makes when it enters that
	// picture, not the repeat that keeps live mode alive. A person pressing this wants the panel
	// rebuilt, not kept alive.
	if w.drawablePanel(sc.frame) {
		picture := w.pictureOf(sc.frame)
		picture.at = w.now()

		if err := w.report(func() error { return w.enterComposed(ctx, picture) }); err != nil {
			return refreshDone{outcome: refreshFailed, why: fmt.Sprintf(
				"%s did not take the picture: %v", w.where, err)}
		}

		w.composed = &composedPanel{picture: picture, repaint: w.now().Add(pixelRepeat)}
		w.overwritten = false

		return refreshDone{outcome: refreshRefreshed, wrote: refreshWrotePixels}
	}

	if err := w.deliver(ctx, sc.frame); err != nil {
		return refreshDone{outcome: refreshFailed, why: fmt.Sprintf(
			"%s did not take the picture: %v", w.where, err)}
	}

	// The light is on its own segments now, and a picture that was composed is not on it any more.
	// The hand-back is proved the way the steady pass proves it: one read of the controller after
	// the frame, taken there rather than here so this answer is not held open by it.
	if w.composed != nil {
		w.composed = nil
		w.handedBack = true
	}

	w.overwritten = false

	return refreshDone{outcome: refreshRefreshed, wrote: refreshWroteSegments}
}

// flashing reports whether an identify holds this light at this instant, and until when.
func (w *writer) flashing() (time.Time, bool) {
	w.flashMu.Lock()
	defer w.flashMu.Unlock()

	if w.flashPlan == nil || !w.now().Before(w.flashPlan.ends) {
		return time.Time{}, false
	}

	return w.flashPlan.ends, true
}

// refreshEntry is one binding's answer: which light, which segment of it, and what the write did.
//
// It is one entry per binding because one binding is one segment of one light — the segment is what
// the module owns there, and naming it is what makes "segment 2 of 10.0.0.136 was refreshed while
// segment 4, which is the operator's own, was not touched" a sentence the screen can draw.
type refreshEntry struct {
	// Binding is the binding's id on these routes, which is how a row of the answer is matched back
	// to a row of the Overview.
	Binding string `json:"binding"`
	// Printer is the host's device id for the printer this binding watches.
	Printer string `json:"printer,omitempty"`
	// Light is the controller's name on these routes: its address without the scheme, which is what
	// matches it to GET /lights and therefore to the name the light calls itself.
	Light string `json:"light"`
	// Controller is the base URL the operator wrote, which is where the light's own page is.
	Controller string `json:"controller"`
	// Segment is the segment this binding drives, and -1 for the whole light.
	Segment int `json:"segment"`
	// Whole is true when it drives the whole light, which is what segment -1 says.
	Whole bool `json:"whole"`
	// Outcome is refreshed, skipped, failed or not-reached.
	Outcome string `json:"outcome"`
	// Wrote is segments or pixels, on a binding that was refreshed.
	Wrote string `json:"wrote,omitempty"`
	// Why is what a person has to read: the moment a light was left in, or the reason it did not
	// answer. Empty on a refreshed one.
	Why string `json:"why,omitempty"`
}

// refreshAnswer is the whole sweep: one entry per binding, the counts a heading needs, and the
// sentence saying what the sweep does and does not touch.
type refreshAnswer struct {
	At        string         `json:"at"`
	Lights    []refreshEntry `json:"lights"`
	Count     int            `json:"count"`
	Refreshed int            `json:"refreshed"`
	Skipped   int            `json:"skipped"`
	Failed    int            `json:"failed"`
	Note      string         `json:"note"`
}

// refreshNote is what the answer says beside the rows: the scope in the operator's terms, and the
// one thing a person would otherwise have to guess at — that other segments of the same controller
// were not touched, on purpose.
const refreshNote = "Every binding this module drives was written from the reading taken as the sweep ran: " +
	"the segment this module owns on each light, and nothing else on that controller. A light inside a moment, " +
	"holding a state's word, being flashed or paused was left alone and is listed as such. One light is tried " +
	"at a time, each with its own time limit, so a controller that does not answer is named rather than counted " +
	"as refreshed."

// refreshNothingNote is the answer when the module drives nothing at all, which is a fresh install
// and not a failure: there is nothing to refresh until a printer is shown on a light.
const refreshNothingNote = "This module is driving nothing, so there is nothing to refresh: no printer is shown on a " +
	"light yet. Add one on this module's Bindings screen, and this button will push the picture to every light it drives."

// refreshAPI is the route the header's button presses. It holds nothing: the bindings it writes are
// the watch's own published set, read at the moment of the press, because a sweep must write what
// the process is driving now and not what it was driving when this handler was built.
type refreshAPI struct{}

// mountRefresh adds this route to the module's own mux.
func (api refreshAPI) mount(mux *http.ServeMux) {
	mux.HandleFunc("POST /refresh", api.refresh)
}

// refresh is the route: push the current picture to every light this module drives, now, and answer
// what each one did with it.
//
// POST, because it writes to every controller the module drives. A body is not read: there is
// nothing to decide. A second press while one sweep runs is refused by code (REFRESH_BUSY) rather
// than joined, because two sweeps would be two writes per light and the answer to the second press
// would be a mixture of both.
func (api refreshAPI) refresh(w http.ResponseWriter, r *http.Request) {
	driven := previewsNow()

	if len(driven) == 0 {
		answer(w, http.StatusOK, refreshAnswer{
			At:     time.Now().UTC().Format(time.RFC3339),
			Lights: []refreshEntry{},
			Note:   refreshNothingNote,
		})

		return
	}

	if !sweepingRefresh.CompareAndSwap(false, true) {
		refuse(w, r, RefreshBusy.New(
			"a sweep is already running: every binding it drives is being written one light at a time, and "+
				"starting a second one would write over the first. The answer to this press is the one already "+
				"in flight, and the button is ready again when it returns."))

		return
	}

	defer sweepingRefresh.Store(false)

	deadline := time.Now().Add(refreshBudget)

	body := refreshAnswer{
		At:     time.Now().UTC().Format(time.RFC3339),
		Lights: make([]refreshEntry, 0, len(driven)),
		Note:   refreshNote,
	}

	for _, target := range driven {
		entry := refreshEntry{
			Binding:    target.binding,
			Printer:    target.printer,
			Light:      target.light,
			Controller: target.where,
			Segment:    target.segment,
			Whole:      target.whole,
		}

		// The sweep is sequential and each light gets its own slice of what is left: a controller
		// that hangs costs its own limit and not the sweep, which is what makes the next light
		// reachable while one of them is being slow.
		done, reached := api.one(target, r, deadline)

		entry.Outcome, entry.Wrote, entry.Why = done.outcome, done.wrote, done.why

		if !reached {
			entry.Outcome = refreshUnreached
			entry.Wrote = ""
			entry.Why = "the sweep's own time ran out before this light: press it again and this one is reached first"
		}

		switch entry.Outcome {
		case refreshRefreshed:
			body.Refreshed++
		case refreshSkipped:
			body.Skipped++
		default:
			body.Failed++
		}

		body.Lights = append(body.Lights, entry)
	}

	body.Count = len(body.Lights)

	answer(w, http.StatusOK, body)
}

// one forces one binding's repaint, inside what is left of the sweep's budget, and reports whether
// it was reached at all.
//
// A binding with no driver is not a light to write: it is a target published by a watch that is
// stopping, and calling it a failure would be a sentence about a light nothing is driving.
func (api refreshAPI) one(target *previewTarget, r *http.Request, deadline time.Time) (refreshDone, bool) {
	if target.drive == nil {
		return refreshDone{outcome: refreshFailed, why: fmt.Sprintf(
			"%s is not being driven at this instant: its watch is stopping, so nothing was written", target.where)}, true
	}

	left := time.Until(deadline)
	if left <= 0 {
		return refreshDone{}, false
	}

	if left > refreshPerLight {
		left = refreshPerLight
	}

	ctx, cancel := context.WithTimeout(r.Context(), left)
	defer cancel()

	return target.drive.refresh(ctx), true
}
