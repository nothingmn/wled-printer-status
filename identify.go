// The one route that makes a light findable: POST /devices/{name}/identify.
//
// # What it is for
//
// docs/product-requirements.md §2.6 puts `identify` beside the four proved tests on a light's card,
// in the operator's own words: it "turns the light on and off — one second each state, five times".
// docs/design/v2/devices/ says what those ten seconds buy — "unmistakable across a room, and it
// leaves the light exactly as it found it" — and the second half is what this file is careful about.
// A person standing in a room with two lights presses it to learn which one the name on the screen
// means, and the light has to be his again when he has finished looking: the module is driving that
// light, and it owes it back in a known state.
//
// # It takes the light the way a preset moment does
//
// A flash is a power write, and power on a WLED controller is not a segment's: SetPower and
// SetBrightness move every segment of that controller at once (see writer.begin). So this route
// cannot write behind the writer's back, and it does not become a second writer to avoid the
// problem. It hands the whole flash to the goroutine that owns the light — writer.identify puts the
// request in the writer's one slot and rings its bell, and writer.flash runs it between two passes of
// the steady frame — which is the same rule every other picture keeps: one goroutine talks to one
// controller. The frame the driver keeps deciding waits in its own slot for the flash's ten seconds,
// so nothing this module would paint can be painted over the flash.
//
// Inside that goroutine the flash takes the light with writer.takeLight and gives it back with
// writer.giveLightBack — the capture and give-back a preset moment already makes. That is power,
// brightness and every segment, in that order, so "exactly as it found it" is a promise this module
// has already measured rather than a second one invented here. It is also what brings the steady
// picture back: the light's own segments are in that give-back. The picture this module draws in
// pixels is not, so the writer paints the frame it was showing in the same pass — drive.go's
// `flashed`.
//
// # Why the answer is 202 and not 200
//
// Ten seconds of flashing does not fit the host's ten-second inbound call, and both ways of
// pretending otherwise are worse than saying what is happening. Shortening the flash to fit would
// silently answer a requirement this route does not own, and holding the call open for nine of the
// ten seconds would answer a timeout with the light mid-flash and nothing said about it. So the route
// answers as soon as the light has taken the first write — which is the proof that it is there and
// answering — with what the flash will do, what power it found the light in, and when it ends. The
// give-back happens at the end of the ten seconds whether or not anybody is still listening, and the
// module's log carries the line that says the light is back. A light that cannot be read, or that
// will not take the first write, is refused before anything else happens.
//
// # The two refusals
//
//   - **A borrowed zone is never flashed.** This module may drive one segment of somebody else's
//     strip to show a colour in; power is not a segment's, so a flash there would take the rest of
//     that strip — the owner's segments and their picture — off and on with it. The same rule the
//     event and the preview refuse with (see ErrPreviewBorrowedSegment), for the same reason.
//   - **A paused light is not flashed.** A pause is the operator's instruction that this light be
//     left alone: one write and then nothing at all, for a duration, or until he resumes it. Ten
//     seconds of writes is the opposite of that, and a light somebody is sleeping beside is the case
//     the pause exists for. The refusal says when the pause ends, so the button can be offered again
//     afterwards rather than failing for no stated reason.
package main

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/prusa3d/prusa-connect/sdk/fault"

	"github.com/nothingmn/wled-printer-status/internal/wled"
)

const (
	// identifyFlashes is how many times the light goes on and off, and identifyState how long each
	// state lasts. They are the requirement's own two numbers (§2.6 and docs/design/v2/devices/), and
	// they are the only two that move if the flash is ever wanted shorter or longer: ten seconds is
	// what makes it unmistakable across a room.
	identifyFlashes = 5
	identifyState   = time.Second

	// identifyAnswer is how long the route waits for the writer to say what the flash will do. It is
	// not the flash's length: the writer decides within one read of the light and one write, so this
	// bound is only reached by a writer that has no light to talk to — a watch that is stopping, or a
	// controller already in a failure window.
	identifyAnswer = 3 * time.Second
)

// identifyRefusal is why a light will not be flashed, kept apart from the sentence so that the route
// can answer with the registered code a client branches on — the shape previewRefusal already has.
type identifyRefusal int

const (
	// identifyAllowed is a flash that will run.
	identifyAllowed identifyRefusal = iota

	// identifyBorrowed is a segment of a controller somebody else also drives.
	identifyBorrowed

	// identifyPaused is a light a pause is in force on.
	identifyPaused

	// identifyUnreachable is a light that did not answer, or a writer that did not take the flash.
	identifyUnreachable
)

// identifyPlan is one flash as the route answers with it: what the light was found in, what the flash
// will do, when it ends — and, when it will not happen, why.
type identifyPlan struct {
	// refusal is identifyAllowed for a flash that is running, and the reason otherwise.
	refusal identifyRefusal

	// why is the sentence the refusal travels with. It is empty on a flash that runs.
	why string

	// wasOn is the power the light was found in, which is the power it is put back to: a light found
	// off is left off. powerKnown is false when the answer describes a flash another call started and
	// this one did not read the light for.
	wasOn      bool
	powerKnown bool

	// starts is when the first state went on, and ends when the last one comes off and the light is
	// put back. Ten seconds apart, which is the flash's whole length.
	starts time.Time
	ends   time.Time

	// already says this answer describes a flash that was already running: a second press inside the
	// ten seconds joins the flash in progress rather than queueing another one behind it, which would
	// be a blink nobody asked for arriving after the person had walked away.
	already bool
}

// identifyCall is one flash the route has left for the writer's goroutine, and the one answer it gets
// back. The reply is buffered so that a writer finishing a flash for a caller that has gone away does
// not block the goroutine that owns the light.
type identifyCall struct{ reply chan identifyPlan }

// identify asks the writer to flash its light, and answers what the flash will do or why it will not.
//
// It never writes anything itself. The request goes into the writer's one slot, the writer's bell is
// rung — every wait in the loop selects on that bell, so an idle writer notices within a tick rather
// than at its next heartbeat — and the goroutine that owns the light does the rest. It is the
// hand-over a preview makes, for the same reason.
//
// One flash at a time, and the window is claimed before the writer is asked: a second press inside the
// ten seconds is answered with the flash already running, and the window is released again when the
// writer refuses, so a light that was paused a moment ago can be tried again as soon as the pause is
// over.
func (w *writer) identify(ctx context.Context) identifyPlan {
	now := w.now()

	w.flashMu.Lock()

	if held := w.flashPlan; held != nil && now.Before(held.ends) {
		joined := *held
		joined.already = true

		w.flashMu.Unlock()

		return joined
	}

	// The window is claimed with what the route can know before the light has been read. The writer
	// replaces it with the plan it reads the light for, and clears it when the flash ends.
	w.flashPlan = &identifyPlan{starts: now, ends: now.Add(identifyFlashes * 2 * identifyState)}

	w.flashMu.Unlock()

	call := &identifyCall{reply: make(chan identifyPlan, 1)}

	select {
	case w.asks <- call:
	default:
		// A call is already waiting in the slot, which the window above makes unreachable in
		// practice. Nothing was flashed, and saying so is the only honest answer.
		w.forgetFlash()

		return identifyPlan{refusal: identifyUnreachable, why: fmt.Sprintf(
			"%s already has a flash waiting, so this one was not taken: send it again once that one has run", w.where)}
	}

	select {
	case w.wake <- struct{}{}:
	default:
	}

	select {
	case plan := <-call.reply:
		if plan.refusal != identifyAllowed {
			w.forgetFlash()
		}

		return plan

	case <-ctx.Done():
		// The caller has gone — the host's own ten seconds ran out. The flash is the writer's now and
		// still ends on time, so the window is left standing rather than released under it: a second
		// press has to meet the flash that is really running, not a light it can start again.
		return identifyPlan{refusal: identifyUnreachable, why: fmt.Sprintf(
			"the call was abandoned before %s answered, so nothing was flashed", w.where)}

	case <-time.After(identifyAnswer):
		// Nothing was flashed, so the window is released and the call is taken back out of the slot:
		// a call left there would be a blink ten seconds after the caller was told there would be
		// none. A writer that took it in this instant is a flash that really is running, and the
		// drain below finds the slot empty and changes nothing.
		w.forgetFlash()

		select {
		case <-w.asks:
		default:
		}

		return identifyPlan{refusal: identifyUnreachable, why: fmt.Sprintf(
			"%s did not take a flash within %s: the light is not answering, or the watch that drives it is stopping", w.where, identifyAnswer)}
	}
}

// forgetFlash clears a window no flash is running in.
func (w *writer) forgetFlash() {
	w.flashMu.Lock()
	defer w.flashMu.Unlock()

	w.flashPlan = nil
}

// holdFlash publishes the plan the writer read the light for, so that a press arriving during the
// flash is answered with the flash that is really running rather than the window's placeholder.
func (w *writer) holdFlash(plan identifyPlan) {
	w.flashMu.Lock()
	defer w.flashMu.Unlock()

	w.flashPlan = &plan
}

// serveIdentify runs the flash a route has asked for, if one is waiting, and reports whether it did —
// in which case the pass it was asked in writes nothing else.
//
// It is called near the top of the writer's loop, before the pause and before any frame, because a
// flash is the light going on and off and every other write in that pass would be a picture painted
// over it. It is before the pause on purpose: a paused light has to be *answered* — refused, with the
// reason and when the pause ends — rather than left waiting for a pause nobody may ever end.
func (w *writer) serveIdentify(ctx context.Context) bool {
	select {
	case call := <-w.asks:
		w.flash(ctx, call)

		return true
	default:
		return false
	}
}

// flash is one identify: the light goes on and off, one second in each state, five times, and is then
// put back exactly as it was found.
//
// It runs on the writer's goroutine, between two passes of the steady frame, which is what keeps the
// flash from being painted over: the pictures the driver keeps deciding wait in the writer's one slot
// for the ten seconds, and the frame painted at the end is the newest one decided.
//
// The light's own record of what it took is not written to the writer's last send (drive.go's
// `record`). That field is the frame this writer delivered, and ten power writes in ten seconds would
// bury the transition a person reads /status for; a flash that fails part-way says so in the log, which
// is where the give-back's own line goes too.
func (w *writer) flash(ctx context.Context, call *identifyCall) {
	defer w.forgetFlash()

	if w.segment != nil {
		call.reply <- identifyPlan{refusal: identifyBorrowed, why: fmt.Sprintf(
			"segment %d on %s is borrowed, and power is not a segment's: switching that controller off would take the rest of the strip with it, so a flash here would be inside somebody else's run", *w.segment, w.where)}

		return
	}

	if end, paused := w.pauseNow(); paused {
		call.reply <- identifyPlan{refusal: identifyPaused, why: fmt.Sprintf(
			"%s is paused%s, and a pause is one write and then nothing at all: a flash is ten seconds of writes on a light the operator asked to leave alone", w.where, end.phrase())}

		return
	}

	held, err := w.takeLight(ctx)
	if err != nil {
		call.reply <- identifyPlan{refusal: identifyUnreachable, why: err.Error()}

		return
	}

	// The first state goes on before the answer, so that a light which will not take the write is
	// refused rather than reported as flashing. It is also the state the flash begins in: on.
	if err := w.writePower(ctx, true); err != nil {
		call.reply <- identifyPlan{refusal: identifyUnreachable, why: fmt.Sprintf(
			"%s did not take the first write of a flash, so nothing was flashed: %v", w.where, err)}

		return
	}

	starts := w.now()
	plan := identifyPlan{
		wasOn:      held.on,
		powerKnown: true,
		starts:     starts,
		ends:       starts.Add(identifyFlashes * 2 * identifyState),
	}

	w.holdFlash(plan)

	call.reply <- plan

	w.logf("%s: identify: the light is going on and off, one second in each state, %d times%s",
		name, identifyFlashes, endPhrase(plan.ends))

	// On, then off, one second in each state, five times. The first "on" is already written; each of
	// the ten states is waited out here rather than counted, because the states are the thing a person
	// in the room sees and a flash whose seconds drifted would be a different instrument.
	flashed := 0

	var failure error

	for i := 0; i < identifyFlashes && failure == nil; i++ {
		if !w.holdState(ctx) {
			failure = ctx.Err()

			break
		}

		if err := w.writePower(ctx, false); err != nil {
			failure = err

			break
		}

		if !w.holdState(ctx) {
			failure = ctx.Err()

			break
		}

		if i+1 < identifyFlashes {
			if err := w.writePower(ctx, true); err != nil {
				failure = err

				break
			}
		}

		flashed = i + 1
	}

	// The light goes back exactly as it was found — every segment, the brightness, and the power last,
	// so that what is written is what is drawn — on a context of its own: a watch that stopped
	// mid-flash would otherwise leave a light in whatever state the flash stopped in, and a cancelled
	// context fails a write before it is sent.
	giveBack, cancel := context.WithTimeout(context.WithoutCancel(ctx), wled.DefaultTimeout)
	w.giveLightBack(giveBack, held)
	cancel()

	// The light is not showing the picture this writer last delivered: it has been switched off and
	// on, and a picture pushed over DDP before that is not on the wall any more. See drive.go's
	// `flashed`, which paints the frame this writer was showing in the very next pass.
	w.flashed = true

	if failure != nil {
		w.logf("%s: identify stopped after %d of %d flashes: %v; the light was told to go back to what it was found showing",
			name, flashed, identifyFlashes, failure)

		return
	}

	w.logf("%s: identify: the light went on and off %d times, one second in each state, and was put back as it was found: power %s, brightness %d, %d segment(s)",
		name, flashed, powerPhrase(held.on), held.brightness, len(held.segments))
}

// holdState waits out one state of a flash: one second, and not a second cut short by a frame that
// arrived. It answers false only when the watch is stopping.
func (w *writer) holdState(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return false
	case <-w.after(identifyState):
		return true
	}
}

// writePower is one power write of a flash, through the gate every write to this controller passes
// through: several writers on one box take turns at it rather than knocking twice at once.
func (w *writer) writePower(ctx context.Context, on bool) error {
	return w.gate.do(func() error { return w.light.SetPower(ctx, on) })
}

// endPhrase is when a flash ends, as the clause a log line reads with.
func endPhrase(at time.Time) string {
	return ", until " + at.UTC().Format(time.RFC3339)
}

// identifyStarted is what the route says when a flash has started: what it will do, what it found the
// light in, and when it ends.
//
// was_on is absent when the answer describes a flash another call started, because this call did not
// read the light: a field of false there would be a statement about the light nobody made.
type identifyStarted struct {
	Controller      string  `json:"controller"`
	Light           string  `json:"light"`
	Flashing        bool    `json:"flashing"`
	Already         bool    `json:"already"`
	Flashes         int     `json:"flashes"`
	SecondsPerState float64 `json:"seconds_per_state"`
	Seconds         float64 `json:"seconds"`
	WasOn           *bool   `json:"was_on,omitempty"`
	EndsAt          string  `json:"ends_at"`
	Note            string  `json:"note"`
}

// identify is the route: the light goes on and off, one second in each state, five times, so that a
// person can see which light a name means.
//
// POST, because it writes to a light. A body is not read: there is nothing to decide, and the two
// numbers belong to the requirement rather than to the caller.
func (api deviceAPI) identify(w http.ResponseWriter, r *http.Request) {
	named := r.PathValue("name")

	found, ok, err := api.deviceNamed(named)
	if err != nil {
		refuse(w, r, BindingRefused.New(err.Error()))

		return
	}

	if !ok {
		refuse(w, r, api.unknownDevice(named))

		return
	}

	pen, driven := penOf(found.Name)
	if !driven {
		refuse(w, r, PreviewNotRunning.New(fmt.Sprintf(
			"this module is driving no light at this instant, so %q cannot be flashed: it answers once its watch is running, which is within a second of starting", named)))

		return
	}

	plan := pen.identify(r.Context())

	switch plan.refusal {
	case identifyBorrowed:
		refuse(w, r, IdentifyBorrowedSegment.New(plan.why).With(fault.StringParam("controller", found.Name)))

		return

	case identifyPaused:
		refuse(w, r, IdentifyPaused.New(plan.why).With(fault.StringParam("controller", found.Name)))

		return

	case identifyUnreachable:
		refuse(w, r, IdentifyUnreachable.New(plan.why).With(fault.StringParam("controller", found.Name)))

		return

	case identifyAllowed:
	}

	body := identifyStarted{
		Controller:      found.Address,
		Light:           found.Name,
		Flashing:        true,
		Already:         plan.already,
		Flashes:         identifyFlashes,
		SecondsPerState: identifyState.Seconds(),
		Seconds:         (identifyFlashes * 2 * identifyState).Seconds(),
		EndsAt:          plan.ends.UTC().Format(time.RFC3339),
		Note: "The light goes on and off, one second in each state, five times, and is then put back " +
			"exactly as it was found: the power it was in, its brightness and every segment. It takes " +
			"ten seconds, so this answer says what is running rather than holding the call open for it; " +
			"the module's log carries the line when the light is back.",
	}

	if plan.powerKnown {
		wasOn := plan.wasOn
		body.WasOn = &wasOn
	}

	if plan.already {
		body.Note = "This light is already flashing: the press joined the flash that was running rather " +
			"than starting a second one. It is put back exactly as it was found when it ends, at the " +
			"moment in ends_at."
	}

	answer(w, http.StatusAccepted, body)
}
