# Product requirements — wled-printer-status

**Status: draft, with the state table locked.** The operator approved this seat's colour and
behaviour proposal on 2026-11-18, so every row of §12 is closed and the colours it names are the ones
`internal/render` holds. Every other line remains a draft for his correction, and every line still
marked `open` is a decision he has not made.

**What this document is for.** The two halves of this module are built — a WLED client that
can reach effects, text, geometry and per-LED painting, and a printer-state reducer that
refuses to present stale data as live — the marketplace plumbing is done, and `internal/render`
now decides what the light shows for each printer state. This document is the decision surface
behind that code and the record of what it may not decide. It records what the operator has
said, marks the consequences this seat has drawn from it, and leaves every choice he has not
made visibly unmade.

**How to read it.** Every requirement carries one of two marks:

- `operator` — the requirement comes from the operator's own words, quoted beneath it. The
  quote is the authority; the requirement is this document's attempt to make it checkable.
- `derived` — this seat's reading. Not the operator's decision. It is a consequence of a
  measured platform fact, or of something already built, and it is written down so it can be
  argued with rather than discovered later. Any `derived` requirement he disagrees with is
  simply wrong and changes.

Anything marked **open** is a decision nobody has made. This document does not invent one.

---

## 1. What this is, and who it is for

*This section is written in the operator's terms; the framing around his words is this
seat's.*

A Prusa Connect module that turns a WLED light into a status display for **one 3D printer**.
It watches that printer through Prusa Connect and paints a light — a strip of LEDs along a
desk, or a panel on a wall — so that somebody walking past can tell what the printer is
doing without opening an app.

In his words, the light carries four kinds of information:

> "on a 1D light strip (like cyberdesk).. i would consider: 1. Color as a signal 2. LEDs as
> progress 3. Animations for some form of state indicator 4. Brightness"

It is for the person standing in the room. The audience is not a dashboard, an API consumer
or an integrations market — it is somebody glancing at a light while doing something else.
That is why colour comes first in his list and exact numbers come last.

He intends it to be published and installed by strangers, not kept on his own machines,
which is why the marketplace requirements (§11) and the documented Docker guidance (§3) are
part of the product rather than chores around it.

---

## 2. Constraints — measured facts, not preferences

These are boundaries the requirements have to live inside. They were measured, not
chosen. A requirement that contradicts one of them is not ambitious, it is wrong, and this
document does not contain one.

| # | Measured fact | What it forbids |
| --- | --- | --- |
| M-1 | A module process runs inside the Prusa Connect Docker container, on a bridge network. | Any requirement that needs a module to serve more than the one printer and one controller it was configured for. |
| M-2 | **Multicast does not cross the bridge.** Measured 2026-11-18 with throwaway containers on the same host, same instrument, on both networks. From the default bridge (`prusa_connect_backend`, where the module actually runs): an mDNS browse for `_wled._tcp.local` returned **0 services**, twice; a raw 15 s listen on UDP 5353 received **0 packets from 0 sources**; a 60 s listen on UDP 65506 received **0 node broadcasts**. From `--network host`: the same 12 s browse returned **4 services** (`Outfront Left Strip`, `RobsCloset`, `bed`, `lavalamp`); 15 s on 5353 received **34 packets from 10 sources**; 60 s on 65506 received **31 broadcasts from 15 devices**. | Any requirement that auto-discovery works from inside the module's own network. It is not slow — it is zero, in both directions. |
| M-3 | The wall is the LAN↔bridge boundary, not multicast on the bridge. A query sent from a container appears on the bridge interface and **never** on the host's LAN interface; a packet the host emits *into* the bridge is received by a container **4 of 4 times**, while the same packet sent via the LAN NIC is received **0 times**. | Treating multicast being broken as the diagnosis. The fix is not a multicast setting. |
| M-4 | **Unicast works on both networks.** `GET /json/info` answered **10/10** rounds from the bridge for both known controllers, and a unicast DNS-SD query to a known IP answered **6/6** when spaced 10 s apart. | Any requirement that the module cannot reach a controller it has been given the address of. |
| M-5 | The fleet is far larger than a two-controller household: **fifteen** WLED controllers were broadcasting during the measurement — `CyberDesk`, `morpheus`, `bed`, `lavalamp`, `RobsCloset`, `32x32Rig`, `MoonLamp`, `JapaneseLantern`, `TheGarageLeft`, `TheGarageRight`, `StarNFC`, `robsbox`, `Predator-Meditate`, `DamonAward`, `PrinterCameraLight`. | Any requirement that assumes the only controller in sight is the right one, or that the discovered device is unambiguous. |
| M-6 | A printer that dies **keeps its last state forever** in Connect. The host carries ten state values and `OFFLINE` is not among them; detection of the loss takes roughly 60 s. | Any requirement that shows a progress figure as present tense for a printer the host no longer has a session for. |
| M-7 | `internal/printer` already treats a connected printer that has gone quiet for 30 s as stale, clears `Job.Live`, and makes `LiveProgress`/`LiveRemaining` answer `ok=false`, while keeping `LastChange` and `Age` so last reported 94 % at 14:02 is sayable. | Requirements that contradict the package that is already built and tested. |
| M-8 | Text on a 2D panel is only possible through a segment's `n` field with effect 122: max **48 characters**, glyph height **12 px** maximum, and only enlarged by grouping pixels. **Text is impossible on a 1D strip** — the firmware falls back to a solid colour. | Any requirement for a job name, a percentage or any word on a 1D strip. |
| M-9 | Write rate: ~**53 ms** round-trip to a 48-LED strip, ~**28 ms** to a 256×64 panel. **1–5 writes/s** is the comfortable band. WLED's own `transition` field is the smoother, not repeated writes. | Any requirement for smooth motion by fast repainting, or for a frame rate a LAN cannot carry. |
| M-10 | The `Percent` effect fills a bar from **one parameter**; a bar can also be drawn by resizing a segment, which on a 2D panel gives 256 steps. | Requirements needing a bar shape the device cannot express either way. |
| M-11 | Every 2D segment write must carry **all four bounds** (`start`, `stop`, `startY`, `stopY`) or the device collapses the rows onto one line. | Any requirement that permits a columns-only write to a panel. |
| M-12 | Module settings can express only **nine types**, with **no enum, no colour, no repeated group**; a rich editor needs the module's **UI half**. Any settings save **restarts the module**. | Any requirement that the composer or the per-event animation list is expressible in declared module options alone. |
| M-13 | The module is intended for the Prusa Plugin Marketplace. | Requirements that only make sense on the operator's own machines. |

**How these were established, so a reader can weigh them.** M-2, M-3, M-4 and M-5 were
measured with throwaway containers on the host, on both networks, on 2026-11-18. One
instrument caveat belongs beside them: `dig @224.0.0.251` answered only **1 time in 7** even
while 34 packets per 15 s were arriving, because replies go to port 5353 while `dig` queries
from an ephemeral port. **A real listener bound to 5353 is the only trustworthy instrument
for that check** — a browse failure from `dig` proves nothing. M-6, M-8, M-9, M-10 and M-11
are WLED and Prusa Connect behaviour observed against the operator's own controllers and
recorded in this repository's package documentation. M-7 is enforced by code that has tests.

The avahi-reflector route out of M-2 is **unverified**: it was reasoned about from the
compose configuration and from the receiver-side evidence above, and never enabled. It is
recorded in §3 as an option with its costs, not as a fix.

---

## 3. Binding and discovery

### R-01 · One segment, one printer: the binding is the unit · `operator`, amended 2026-11-18

**Requirement.** A **binding** is a (printer, controller, segment) triple. A printer appears in at
most one binding; a segment is driven by at most one binding; a controller may carry as many
bindings as it has segments it is willing to give up; and a controller that holds **no named
segments** carries exactly one, because the whole light is then one display.

**Operator's words.** The rule this replaces, and the one that replaced it, both his, the same day:

> "1 printer to 1 wled controller at most"

> "i agree. lets not limit this. i suspect many would have one strip, 1 wled, and they can partition
> it accordingly into segments. nice addtion"

**Proved by.** The module refuses a set in which one printer is named twice, in which one segment of
one controller is named twice, or in which the whole of a controller is named beside any other
binding on that controller — and it refuses before a client is built, so the mistake is a sentence
on the instance rather than two writers undoing each other at the rate of a tick. However many
bindings a controller carries, it is spoken to through one client and one write door.

**Refuted by.** Any configuration in which two bindings name one printer, two bindings name one
segment, or a whole-controller binding shares a box with another binding.

**Still ruled out by it.** Installing a second module instance to serve a second printer: the
platform mounts one namespace once, so a second printer is a second binding inside this module.

### R-02 · The operator types the controller's address · `operator`

**Requirement.** The WLED device can be added by hand, by IP address.

**Operator's words.**
> "the WLED devices can be added manually, by IP address or auto-discovered"

**Proved by.** The operator sets an address in the instance's configuration and the module
drives that controller, with no code change and no rebuild.

**Refuted by.** An address that only exists in source code, or an install that cannot be
pointed at a device without editing the repository.

### R-03 · The address is a declared setting, not a constant · `derived`

**Requirement.** The controller's address is a first-class declared option in `module.yaml`,
beside `message`, so an operator finds it where he finds every other knob.

**Where this comes from.** Not his words — M-13 and the current state of the tree. The
manifest today declares exactly one option (`message`), and `internal/wled` takes a base URL
handed to it in code. Manual IP working is true of the platform (M-4) and not yet of this
module, and the gap is a manifest change somebody has to ask for.

**Proved by.** `module.yaml` declares an address option with a summary that says what it is
for, and `prusa-connect module add` installs a module an operator can point at his controller
through the instance's `config:` block.

**Refuted by.** A release whose address can only be changed by editing Go.

### R-04 · Auto-discovery is best effort, never required · `operator`

**Requirement.** Where discovery is attempted at all, it is optional. Its failure produces
a plain statement that no device was found and an invitation to type the address — never an
error state and never a light that stops working.

**Operator's words.**
> "we can only do 'best effort' here.. and we will need to document this, and if someone
> expects this to work, we should provide guidance as to how to make sure this works in
> docker. that or they can add manually."

**Proved by.** On a fresh install with no address set, the module reports that nothing was
discovered, says why, and continues serving. After the address is typed, the light works.

**Refuted by.** A module that fails to start, or refuses to serve, because discovery found
nothing.

### R-05 · Discovery is not offered from where it cannot work · `derived`

**Requirement.** The module does not present network discovery as a working path from inside
the bridge network, because it measurably is not one (M-2, M-3). Where a discovery attempt is
still made, its result is reported as inconclusive rather than as no devices existing.

**Where this comes from.** M-2 measured 0 services and 0 packets in both directions from the
container the module runs in. An interface that offers a browse button which always returns
nothing is a lie told by the product, and the operator's own instruction is to be explicit
about this rather than let someone expect it to work.

**Proved by.** The settings surface and the guidance name the typed address as the path that
works, and any discovery affordance states its limit in the same breath.

**Refuted by.** A scan-for-devices control whose empty result is indistinguishable from an
empty room.

### R-06 · A typed address is confirmed by reading the device's identity · `derived`

**Requirement.** After an address is saved, the module reads `GET /json/info` and reports
what it found there — the controller's name, MAC, firmware version and LED count — so the
operator can see he typed the right device.

**Where this comes from.** M-5: fifteen controllers were answering on the network during the
measurement. A typed address that is wrong but reachable is the likeliest mistake in this
product, and unicast identity reads work from the bridge (10/10, M-4). This is this seat's
reading of what "make sure this works" has to mean, not a sentence the operator wrote.

**Proved by.** Saving an address shows the name, MAC, version and LED count of the device at
that address, and those four match the WLED web UI for that device.

**Refuted by.** An address that is accepted with no identity read back, or a module that
paints a device it has never identified.

### R-07 · An unreachable controller does not take the module down · `derived`

**Where this comes from.** A module that dies stops being a status display, and the host
stops a process that misses three pings. The light is the least important thing in the room;
the module staying alive is what lets it recover when the controller comes back.

**Requirement.** When the controller does not answer, the module reports the failure, keeps
serving, and retries. Recovery is automatic when the device returns.

**Proved by.** With the controller powered off, the module still answers its route and still
reports its own health; when the controller is powered on, painting resumes without a
reinstall.

**Refuted by.** A module that exits, hangs, or needs a manual restart after the light was
unplugged.

### R-08 · Docker guidance ships with the module · `operator`

**Requirement.** The repository carries operator-facing guidance on making the light work
under Docker, written for a stranger, in four parts:

1. **The host-networking recipe** — what to change in the compose file, and what it costs:
   `network_mode: host` makes `networks:` mutually exclusive (a hard compose error), discards
   `ports:` publication (the app then binds 8080 directly in the host namespace, so the URL is
   unchanged), requires the database to be published on loopback with the app's DSN changed
   from `postgres:5432` to `127.0.0.1:5432`, makes port handover non-atomic during cutover, and
   exposes the database to the host.
2. **Why the bridge fails** — stated plainly and with the measurement: a container's query
   appears on the bridge and never on the LAN, 0 services and 0 packets in both directions.
   The avahi-reflector alternative is named with its cost: a root-level host change, reflecting
   to every bridge on the host and outward as well, so Prusa Connect would advertise itself on
   the LAN. Marked **unverified** — it was reasoned about, not enabled.
3. **Type the address and it always works** — unicast answered 10/10 from the bridge.
4. **How to confirm the device** — by name, MAC, firmware version and LED count read from
   `/json/info`.

**Operator's words.**
> "we can only do 'best effort' here.. and we will need to document this, and if someone
> expects this to work, we should provide guidance as to how to make sure this works in
> docker. that or they can add manually."

**Proved by.** All four parts exist in the repository, and each carries the command or the
number it rests on rather than an assurance.

**Refuted by.** Guidance that says to use host networking without the compose consequences, or
that offers the reflector without saying it is unverified and that it changes the host.

---

## 4. The 1D strip — colour as a signal

### R-09 · Colour is the primary signal · `operator`

**Requirement.** On a 1D strip the dominant information is colour: red means bad, green means
good, yellow means warning.

**Operator's words.**
> "So your typical colors, Red = bad, Green = good, Yellow = warning."

**Proved by.** Reading the segment's colour from `GET /json/state` while the printer is in a
state the table in §12 assigns: it matches the assigned colour.

**Refuted by.** A strip showing a colour the table does not assign for the state it is in.

### R-10 · Every state maps to a colour, and error codes map to hues of red · `operator`

**Requirement.** Each printer state gets a colour from that scheme. Error states map to hues
of red, one hue per error code. A printer that is printing and healthy maps to green. A
printer that is idle, or waiting for something, maps to yellow.

**Operator's words.**
> "Each of those should be mapped over the which states we can get per printer. For example
> error codes would map to hues of red. A printer in a good state, its printing and happy,
> should map to green states. and one that is either idle, or waiting for a color change,
> should be yellow."

**Proved by.** The mapping table in §12 covers every state, and the strip shows the colour it
names. Distinct error codes produce visibly distinct reds rather than one red.

**Refuted by.** A state with no colour, two states sharing a colour with no reason, or an
error shown in any colour but a red hue.

### R-11 · Never green when it is not known to be good · `derived`

**Requirement.** The strip does not show green for a state whose colour the operator has not
chosen, and never for a printer that is disconnected, stale, or unknown.

**Where this comes from.** Green is the one colour the operator assigned a meaning to —
"good". Any state he has not spoken for is not known to be good, so green would assert
something nobody decided (M-6, M-7, and the unmapped states in §12).

**Proved by.** With the printer disconnected, or reporting `UNKNOWN`, the strip is not green.

**Refuted by.** Green on the strip at any moment the printer's state is not one the table
marks green.

### R-12 · Colour and progress occupy the same LEDs · `derived`

**Requirement.** Progress and colour are not two displays. The lit portion of the strip
carries the state colour; there is no second bar and no separate colour region.

**Where this comes from.** The operator's own combined example — a strip that is half done
*and* needs filament — plus M-9's write budget, which does not pay for two independent
displays on one short strip.

**Proved by.** At 50 % with a warning state, half the strip is lit in the warning colour.

**Refuted by.** A layout in which colour occupies some LEDs and progress occupies others.

### R-13 · A state change is visible within a few seconds · `derived`

**Requirement.** When the printer's state changes, the strip shows the new colour within 5 s.

**Where this comes from.** M-6 and M-9: the module follows an event stream, and one write per
state change sits well inside the comfortable band. A status display that lags its printer by
a minute is not a status display.

**Proved by.** Pausing a print and timing the strip's change to yellow.

**Refuted by.** A change that only appears on the next scheduled repaint, or more than 5 s
after the printer reported it.

---

## 5. The 1D strip — LEDs as progress

### R-14 · LEDs fill from the first to the last in proportion to progress · `operator`

**Requirement.** The number of LEDs in use, counted from LED 0 to the last LED of the
configured range, is lit according to the printer's reported percentage. Half done means half
the range lit.

**Operator's words.**
> "Then the number of LEDs which are actually in use, starting at LED 0 to LED (MAX) should be
> lit up based on the % progress of the printer itself."

**Proved by.** At 50 % on a 48-LED range, 24 LEDs are lit and 24 are not. At 10 %, 4 or 5 are.

**Refuted by.** A fill that grows from the middle, wraps, or counts from an LED other than
the first.

### R-15 · Colour and progress are shown together, and mean different things · `operator`

**Requirement.** A printer can be part-way through a job *and* needing attention, and the
strip shows both at once: the fill says how far, the colour says what is going on.

**Operator's words.**
> "And the color is based on the #1 above. So it could be 50% done, and needing filament
> change for example."

**Proved by.** A print paused for a filament change at 50 % shows a half-lit strip in the
warning colour.

**Refuted by.** A strip that must choose one of the two, or that shows progress in a colour
that means something else.

### R-16 · The lit and unlit parts are distinguishable · `derived`

**Requirement.** The unfilled remainder is visually different from the fill in every state,
including states whose colour is bright.

**Where this comes from.** M-9's 1–5 writes/s and the operator's fill model both assume the
boundary is readable at a glance. It says nothing about what the unlit LEDs should be — that
is **open** (Q-15).

**Proved by.** A photograph of the strip at 50 % in which the boundary between lit and unlit
can be seen without measuring.

**Refuted by.** A strip that reads as uniformly lit at 10 % and at 90 %.

### R-17 · Progress only advances on live readings · `derived`

**Requirement.** The fill reflects `LiveProgress` only. A stale or disconnected printer's last
known percentage may be shown, but not as a filling bar that implies the job is running.

**Where this comes from.** M-6 and M-7 — this is the rule `internal/printer` already enforces,
and the display must not defeat it.

**Proved by.** Taking the printer off the network mid-print: the fill stops, and the strip
enters the not-live presentation of R-36.

**Refuted by.** A bar that keeps its last percentage while the strip looks exactly as it did
when the printer was alive.

---

## 6. The 1D strip — animations

### R-18 · An animation plays at start, at end, and at a filament swap · `operator`

**Requirement.** The light plays an animation on the events the operator named: a print
starting, a print ending, and a filament change.

**Operator's words.**
> "The animations. At print start and end, and even filament swap, we could play some cool
> animations on it..."

**Proved by.** Starting a print produces a motion on the strip distinguishable from the static
green of the print itself; the same for a print ending and for a filament change.

**Refuted by.** A light that goes from one solid colour to the next with nothing in between.

### R-19 · The operator chooses the animation for each event · `operator`

**Requirement.** Each event can be assigned an animation, from a list of events, by the
operator — chosen, not fixed by the module.

**Operator's words.**
> "Maybe we allow the user to choose the animation on each of these (and any other event). I
> can see a list of events with a drop down for the animation to play."

**Proved by.** A surface lists the events and lets each be pointed at a different animation, and
changing one changes what plays for that event and only that event.

**Refuted by.** One animation for all events, or an assignment only editable in source.

### R-20 · The event list is not closed · `operator`

**Requirement.** The set of events is extensible. The three named events are present from the
start; the list can gain events without the feature being redesigned.

**Operator's words.**
> "and any other event"

**Proved by.** An event beyond the first three can be added and assigned an animation.

**Refuted by.** A fixed set of exactly three events baked into the design. Which events belong
in the first version is **open** (Q-3).

### R-21 · An animation that cannot play is replaced, not skipped to black · `derived`

**Requirement.** Where a controller has no usable effect for an animation, and where effects
cannot be reached at all, the event still produces a visible change on the strip — a per-LED
painting, or a colour change — rather than nothing.

**Operator's words.**
> "best effort with the WLED stuff as well. worst case scenario is paint by pixel and not
> animations/text effects"

**Where this comes from.** His words set the floor; the tiers are what `internal/wled` already
implements. The requirement is that the floor is always available, since per-LED painting works
on any controller.

**Proved by.** A controller whose effect list is empty still shows a distinguishable event on
print start.

**Refuted by.** A dark strip at a print start because the animation was unavailable.

---

## 7. The 1D strip — brightness

### R-22 · Brightness is set once and does not move on its own · `operator`

**Requirement.** The operator sets a brightness for the light, and in this mode the module
never changes it.

**Operator's words.**
> "Brightness. Iniitally im thinking: 1. Just allow the user to set a fixed brightness for the
> printer, never change it"

**Proved by.** The brightness reported by `GET /json/state` is the value the operator set, and
is unchanged across state changes, animations and days.

**Refuted by.** Any write to brightness other than one the operator asked for.

### R-23 · Brightness may follow the time of day · `operator`

**Requirement.** As an alternative to a fixed brightness, the light's brightness follows the
time of day.

**Operator's words.**
> "2. Based on the time of day"

**Proved by.** With this mode selected, brightness at two recorded times of day differs in the
way the schedule says. What the schedule actually is — the times, the values, whether it ramps
and in which time zone — is **open** (Q-6), and until it is decided this requirement cannot be
proved.

**Refuted by.** A time-of-day mode whose brightness does not change with the clock, or changes
at times the operator did not choose.

### R-24 · The module never leaves the strip dark by its own writes · `derived`

**Requirement.** No write sequence leaves the strip off or at zero brightness as a side effect
— in particular, painting individual LEDs is never sent to a segment that is off or at a
brightness the module has not set.

**Where this comes from.** `internal/wled` records that a body which switches a segment on and
paints it in one request does not work, and that painted colours freeze the segment's effect.
The failure this prevents is the worst one available to this product: a light that is simply
dark and looks broken.

**Proved by.** After any state change, animation or reconnect, the strip is on at the
configured brightness; a rebooted controller is repainted rather than left at its saved preset.

**Refuted by.** Any moment in the module's own operation where the strip goes dark without the
printer having gone dark.

---

## 8. The 2D panel

### R-25 · Everything the 1D strip does, a panel does first · `operator`

**Requirement.** A 2D panel carries all of the colour, progress, animation and brightness
behaviour above, and then adds what only a panel can do.

**Operator's words.**
> "On a 2D strip — All the above for 1D, with the additions:"

**Proved by.** A panel shows the same state colours and the same proportional fill as a strip,
before any panel-only element is considered.

**Refuted by.** A panel product that has no colour-for-state behaviour, or that treats the panel
as a different feature rather than a superset.

### R-26 · A panel too coarse for text falls back to the 1D behaviour · `operator`

**Requirement.** Where a panel does not have the resolution for the richer display, it falls
back to the 1D presentation — colour, fill, animation — rather than showing a degraded attempt
at text or a bar.

**Operator's words.**
> "Now not all 2D strips will have the resolution which we have on Morpheus, and in those cases
> we should just basically revert back to the above (1D lighting options)."

**Proved by.** A small panel shows the 1D presentation and nothing illegible.

**Refuted by.** Text rendered at a size nobody can read, or a bar occupying a panel too small to
show where it starts.

**The threshold is open** (Q-7). He named the panel he has — "the resolution which we have on
Morpheus" — and this document does not invent a pixel count in his place. Until he gives one,
this requirement cannot be checked against a specific device.

### R-27 · A dense panel can show the percentage in colour · `operator`

**Requirement.** Where resolution allows, the panel shows the percentage complete, in colour.

**Operator's words.**
> "we could also show the percentage complete"

**Proved by.** A photograph of the panel in which the percentage shown matches the printer's
reported progress.

**Refuted by.** A number that lags the printer, or is rendered in a colour the table does not
assign.

### R-28 · A dense panel can show the job's name · `operator`

**Requirement.** Where resolution allows, the panel shows the name of the print job.

**Operator's words.**
> "we could render the name of the print job itself, and a progress bar"

**Proved by.** The panel shows the job name as Prusa Connect reports it, in characters a person
can read.

**Refuted by.** A name that is truncated into nonsense, or absent when the job has one. What
happens past 48 characters — truncate, scroll, or cut at a word — is **open** (Q-10).

### R-29 · A dense panel can show a progress bar · `operator`

**Requirement.** Where resolution allows, the panel shows a progress bar.

**Operator's words.**
> "and a progress bar"

**Proved by.** The bar's filled fraction matches the reported percentage, and its position
matches the operator's choice.

**Refuted by.** A bar that is present but cannot be told apart from the background.

### R-30 · The bar can sit at the top or the bottom, one to X rows deep · `operator`

**Requirement.** The progress bar can occupy the top or the bottom of the panel, between 1 and
X pixel rows tall, across the width.

**Operator's words.**
> "to be able to choose a progress bar, say taking up the TOP OR BOTTOM 1 to X pixels across"

**Proved by.** The bar occupies the chosen edge, the chosen number of rows, and the full width.

**Refuted by.** A bar pinned to one edge with no choice, or one that does not span the width.

### R-31 · The remaining rows can rotate between the percentage and the job name · `operator`

**Requirement.** The rows not taken by the bar show the percentage, and optionally swap, every X
seconds, to the job's name.

**Operator's words.**
> "Then the rest of the pixels could be the percentage text progress indicator, and optionally
> every X number of seconds to swap over to the name of the print itself...."

**Proved by.** With rotation on, the panel alternates between the percentage and the name at the
chosen interval.

**Refuted by.** Both shown at once in a space that fits one, or a swap that never happens.
**The interval X is open** (Q-8).

### R-32 · Text is drawn only where the firmware can draw it · `derived`

**Requirement.** Words on a panel are drawn through effect 122 with the string in the segment's
name field, at most 48 characters, at a glyph height the panel can carry. No requirement in this
product asks a 1D strip to display text.

**Where this comes from.** M-8. His sentence "we could render the name of the print job itself"
is a panel sentence — on a strip the firmware falls back to a solid colour, so a job name on a
48-LED strip is not a smaller version of the feature, it is a different colour.

**Proved by.** A request body carrying effect 122 and a segment name of 48 characters or fewer
reaches the panel, and the text appears.

**Refuted by.** A 49-character name sent, a name sent to a strip in the expectation of text, or
text attempted through any field other than the segment name.

### R-33 · Every panel write names all four bounds · `derived`

**Requirement.** Any segment written on a panel carries `start`, `stop`, `startY` and `stopY`
together.

**Where this comes from.** M-11: a columns-only write collapses the rows onto one line. This is
not a style rule; it is the difference between a bar and a single stripe.

**Proved by.** The request body logged for any panel write contains all four fields.

**Refuted by.** A body with `start` and `stop` alone, on any panel.

---

## 9. The composer

### R-34 · The operator composes what the light shows · `operator`

**Requirement.** What the display shows is composed by the operator, not fixed by the module:
the elements, where they sit, and what fills them are his to arrange.

**Operator's words.**
> "oh, lets go big my friend. the composer. this will be epic."

and, on the panel:

> "the overall, coolest thing would to be able to choose what we want displayed"

**Proved by.** Two operators can end up with two different displays from the same module, and one
operator can change his without a rebuild.

**Refuted by.** A layout that only the module's author can change.

### R-35 · A layout is data: regions, each with something filling it, and a rotation · `derived`

**Requirement.** A composed layout is stored as data describing regions of the display, what
fills each region, and how the display rotates between them. It is not code, and it survives an
upgrade.

**Where this comes from.** This seat's reading of "the composer", constrained by M-10 and M-12:
a bar is either the `Percent` effect's one parameter or a resized segment (256 steps across a
panel), and a region has to be expressible as one of those plus text where text is possible.

**Proved by.** A saved layout can be read back, edited and re-applied; a region's contents can be
changed without touching the module's source.

**Refuted by.** A layout that only exists as constants in Go, or that is lost on upgrade.

### R-36 · The composer needs the module's UI half · `derived`

**Requirement.** The composer is served by the module's own UI half. Nothing in this document
assumes a layout, a colour or a per-event animation can be expressed as a declared module option.

**Where this comes from.** M-12: nine setting types, no enum, no colour, no repeated group, and
a settings save that restarts the module. A colour picker, a list of events with a dropdown each,
and a grid editor are all outside that vocabulary. This is not a preference about where the screen
lives; it is the only surface that can carry the composer at all.

**Proved by.** A layout can be composed and changed in a running module without restarting it,
through a surface the module serves.

**Refuted by.** A composer implemented as a dozen text options, or a design that claims the
settings screen can express it. Whether the layout also lives in a file the operator may edit is
**open** (Q-13).

---

## 10. Saying "this is old" without lying

### R-37 · A printer that is gone is never shown as present · `derived`

**Requirement.** When the host no longer holds a session for the printer, or nothing about it has
changed for 30 s, the display stops presenting the last readings as the printer's current state.
Where the display can carry text, it can say what the printer last reported, at what time, and how
long ago that was — *was printing at 94 % when last heard, six minutes ago*. Where it cannot carry
text, it is visibly distinguishable from a live display.

**Where this comes from.** Nothing the operator said; M-6 and M-7. The host keeps a dead printer's
state tree forever, dates nothing, and has no `OFFLINE` value among its ten states, so a module
reading it naively says *printing, 94 %, six minutes left* about a printer that was switched off
at 14:02 — forever. `internal/printer` already refuses to do that, and the requirement has to match
the code rather than outrun it.

**Proved by.** Powering a printer off mid-print: after the host notices (~60 s) and after the 30 s
stale window, the display's text form names the last state, the last percentage, and the age; and
the strip is not showing live-looking progress. `LiveProgress` answers `ok=false` at the same moment.

**Refuted by.** A display still showing a rising bar, or the plain green of a healthy print, for a
printer that has been gone for ten minutes.

**What the strip does when the printer is gone is now decided.** On 2026-11-18 the operator approved
the proposal: dim red, breathing, and never the last-known progress bar. So the strip's answer is a
colour of its own rather than an absence, and R-37 can be proved on a strip as well as in text. The
dim red and the cleared bar are built and tested in `internal/render`; the breathing is approved and
still unbuilt.

---

## 11. Installable by a stranger

### R-38 · A stranger can install it, and the install tells the truth · `derived`

**Requirement.** The module installs from the marketplace by an operator who has never seen this
repository, using one documented command; it declares a licence and the platforms it ships; and
what it declares about itself — its permissions and the addresses it talks to — matches what it
actually does.

**Where this comes from.** M-13, and the operator's intent to publish. The second half is the part
that bites: a declared permission that nothing uses is a false statement in the one place an
operator is invited to read before granting. `observe` was withheld for exactly that reason while
`internal/printer` existed and nothing called it, and put back on 2026-11-18 when the module began
listing a device to resolve the `printer` setting, reading it, and following its event stream.

**Proved by.** A fresh install on a machine that has never seen the source; and a permission list in
which every entry is used by the shipped behaviour, with the outbound reach to the WLED address
described in the documentation beside it.

**Refuted by.** An install that needs a checkout, a licence field the marketplace would refuse, or a
permission granted for work the module does not do.

---

## 12. State-to-display mapping

The ten states the host reports, plus the two conditions the host has no word for. **Every row is
now closed.** Three or four by the operator's own words, the rest by his assent on 2026-11-18 to the
set this seat proposed: *"love your set of defaults for the colors/animations. yes to that"*. The
colours below are the ones `internal/render` implements, so this table and the code are the same
statement and a disagreement between them is a defect in one of the two.

| State | What it means | Display | Where it comes from |
| --- | --- | --- | --- |
| `IDLE` | Nothing in progress; the machine is available | **Yellow** | Operator's own words: "one that is either idle, or waiting for a color change, should be yellow" |
| `BUSY` | Heating or calibrating for a print it has already taken | **Yellow** | Proposed as waiting rather than working, and approved 2026-11-18. His words give green to "printing and happy" and yellow to "idle, or waiting" — this is preparing to print while nothing is wrong, so it sits with waiting |
| `READY` | Ready, with nothing running | **Dim green** | Proposed as a quieter version of the printing green, and approved. Dim is a third of the bright channel values, computed rather than picked, so it is provably the same green |
| `PRINTING` | A job is running | **Green**, with the filled bar in the same green | Operator's own words: "A printer in a good state, its printing and happy, should map to green states" |
| `PAUSED` | A job is on the bed and paused | **Yellow, breathing, bar frozen where it stopped** | Proposed and approved. The frozen bar is built; the breathing is approved behaviour this build does not yet animate |
| `FINISHED` | A job completed | **Steady green, held until the next job** | Proposed and approved. It is the printing green rather than a hue of its own: the print succeeded |
| `STOPPED` | A job was stopped | **Grey**, or off | Proposed and approved as one of those two, with the choice left open inside the approval. This build shows a mid grey (`80,80,80`) — neither success nor failure, so neither green nor red |
| `ATTENTION` | Waiting on a person; the filament change lives here | **Yellow** | Operator's own words: "waiting for a color change, should be yellow". This seat maps "waiting for a color change" onto `ATTENTION`, which is where a filament change is reported — the mapping is a reading, the colour is his |
| `ERROR` | The printer is in error | **Red, in hues** | Operator's own words: "Red = bad" and "error codes would map to hues of red". No table of codes and hues exists yet, so every error shows one red (`200,0,0`) |
| `UNKNOWN` | The printer has reported no state | **Dim blue** | Proposed and approved. A printer that has said nothing must not look like a verdict, so neither green, red nor yellow |
| **Disconnected** | The host holds no session; the state tree is the last one it reported | **Dim red, breathing, and never the last-known progress bar** | M-6, proposed and approved. It is the error red at a third brightness, deliberately: the reading is a memory, not an observation |
| **Stale** | Connected, but nothing has changed for 30 s | **Dim red, breathing, and never the last-known progress bar** | M-7, proposed and approved, and the same dim red as disconnected on purpose — either way there is nothing current to show |

**What this settles, and what it does not.** The colour of every state is decided, and the two rules
that matter most are decided with it: a disconnected or stale printer never keeps its progress bar,
and a finished job holds its green until the next job rather than expiring on a timer. **The breathing
on `PAUSED`, disconnected and stale is approved but not built** — there is no animation layer yet, so
those three currently show a steady colour. `STOPPED`'s grey-versus-off choice, `ERROR`'s code-to-hue
table, and the fill's rounding remain the open questions they were (Q-17), and are marked so where
they appear.

---

## 13. Out of scope

These follow from R-01 and from M-1, and they are out of scope because they are removed rather
than deferred.

- **Fleets.** No many-printer view, no summary across printers, no view that answers which printer needs
  attention.
- **Arbitration.** No priority rule for two bindings on one controller, because two bindings never
  name one segment: the set is refused at configuration (R-01) rather than arbitrated at the wire.
  What no module can enforce is another tool writing the same pixels — WLED has no ownership.
- **A light as a control.** The product shows state; it does not pause, cancel or start a print.
- **A dashboard.** Numbers, charts, history and remaining-time graphs are the app's work. The
  light shows what a person glancing at it needs.
- **Auto-discovery as a working path from the module's own network.** Out of scope by
  measurement (M-2), not by preference — R-05 says it may not be presented as working.
- **Text on a 1D strip.** Impossible in the firmware (M-8), so it is not a requirement anybody may
  later add without changing the hardware.
- **Smooth motion by fast repainting.** The LAN carries 1–5 writes/s comfortably and WLED's own
  transition field does the smoothing (M-9).
- **Colour schemes, palettes and themes beyond the state mapping.** He asked for a signal, not a
  look; a theme engine is a different product.
- **Reading or writing the printer's own hardware, cameras, enclosures or sensors.** This module
  reads one printer's reported state and writes one light. Nothing else.
- **Multiple lights per printer, chained or mirrored.** R-01 still removes it: a printer appears in
  one binding, so its status reaches one segment.

---

## 14. Open questions

Every decision the operator has not made, and what each one blocks. Questions he has since answered
stay in the list with his words beside them, so a later reader can see what was asked and what closed
it rather than finding a question silently gone. **Fourteen remain open; three are answered.**

1. **Q-1 · The colours he did not name.** `READY`, `BUSY`, `PAUSED`, `FINISHED`, `STOPPED`,
   `UNKNOWN`. Blocks: the mapping table being complete, and therefore R-10's "every state maps to
   a colour".
   **ANSWERED 2026-11-18.** He approved this seat's proposed set in full: *"love your set of defaults
   for the colors/animations. yes to that"*. Dim green, yellow, yellow, steady green, grey, dim blue —
   the values and their reasoning are in §12, and `internal/render` implements them.
2. **Q-2 · How a disconnected or stale printer looks on a 1D strip.** Off, a single dim LED, a slow
   pulse, the last colour at half brightness — all are available and none is his. Blocks: R-37, and
   the strip half of R-37. It also decides whether R-36's text form is the only way this is ever
   said, which would leave a strip unable to say it.
   **ANSWERED 2026-11-18** by the same assent. Both show the dim red, breathing, with no progress
   bar. The dim red and the absent bar are built and tested; the breathing is approved and unbuilt.
3. **Q-3 · The full event list.** He named three and said "any other event". Does the list also
   carry: connecting, disconnecting, going stale, an error appearing, a print resuming, layer
   changes, a first-layer milestone? Blocks: R-20's first version and the settings surface in R-19.
4. **Q-4 · What the animation choices are.** Chosen from the controller's own effect list, which
   differs by firmware and by device, or from a small set this module carries and paints itself?
   The first is richer and never matches across devices; the second is identical everywhere and is
   painted per LED. Blocks: the contents of the dropdown in R-19, and whether two of his controllers
   can be configured the same way.
   **Partly answered 2026-11-18:** he approved the animation defaults this seat proposed, alongside
   the colours. But that proposal was made in conversation and is **not written down in this
   repository**, so the list itself has to be recovered and recorded before the dropdown is built.
   This document does not reconstruct it from memory.
5. **Q-5 · What happens when an animation ends.** Return to the state colour, play once and hold the
   last frame, or run for a fixed duration? Blocks: how R-18 and R-12 coexist — an animation that
   never returns is a display that has stopped showing state.
6. **Q-6 · The time-of-day schedule.** Which hours, which brightness at each, whether it ramps
   between them, and which time zone it follows (the module's container clock or the printer's).
   Blocks: R-23 entirely — it cannot be proved without a schedule.
7. **Q-7 · The resolution threshold for the richer panel display.** What pixel count or geometry
   makes a panel dense enough for text and a bar. He named one panel — "the resolution
   which we have on Morpheus" — and not a number. Blocks: R-26, and therefore when R-27 to R-31
   apply at all.
8. **Q-8 · The rotation interval X.** How many seconds the panel holds the percentage before
   swapping to the job name, and whether the interval is the same for every region. Blocks: R-31.
9. **Q-9 · Default bar placement and depth.** Top or bottom by default, and how many rows, before
   the operator chooses. Blocks: what a panel shows on first install, before anyone composes it.
10. **Q-10 · A job name longer than 48 characters.** Truncate with an ellipsis, scroll it, or cut at
    a word boundary. Blocks: R-28's acceptance observation, since matching the reported name is
    not achievable past 48 characters.
11. **Q-11 · What the panel shows when progress is unknown.** A job that has just started may report
    no percentage at all. Dashes, an empty bar, the job name alone, or the previous figure with an
    age beside it — the last of those is what R-37 asks for elsewhere, which may be the answer, but
    it is not this seat's to give. Blocks: the percentage region in R-27.
12. **Q-12 · What the module offers when fifteen controllers are visible.** Only a typed address, or
    a list to pick from — and if a list, built how, given that discovery cannot run from the module's
    network (M-2, M-5)? Blocks: the settings surface behind R-02.
13. **Q-13 · Where a composed layout lives.** Only in the module's UI half, or also in a file the
    operator may edit by hand? The first keeps one source of truth; the second is the only way to
    move a layout between machines without a screen. Blocks: R-35's storage rule and whether R-08's
    guidance can include a copyable layout.
14. **Q-14 · What the light does after a print finishes and nothing else starts.** Hold the finished
    colour until the next job, return to idle after a time, or turn off. Blocks: R-18's end animation
    having somewhere to land, and R-37 if the answer is "off".
    **ANSWERED 2026-11-18** by the same assent: hold the finished green until the next job. It does
    not expire on a timer and it does not fall back to idle.
15. **Q-15 · The unfilled remainder on a strip.** Off, or a dim version of the state colour? Blocks:
    R-16's observation being sharp, and the look of the product at low percentages.
16. **Q-16 · Whether brightness belongs to this module or to somebody else.** WLED reports whether
    something is streaming into it, and a strip in a room may be driven by more than one thing. When
    another writer is present, does this module set brightness anyway, defer, or stop painting and say
    so? Blocks: R-22 and R-24, and the whole question of what happens when the light is already in use.
17. **Q-17 · Rounding of the progress fill.** Floor, or nearest. Small — it moves the boundary by one
    LED — but it changes what a photograph of the strip proves, so R-14's observation stays ambiguous
    until it is settled. Blocks: R-14, and nothing else.

---

## 15. What this document deliberately does not decide

- **No colour** for any state the operator had not named at the time this was written. That list is
  no longer a gap: on 2026-11-18 he approved the whole set, and §12 records it.
- **No threshold** for a dense panel, and therefore no list of which devices get the rich
  display.
- **No animation** — not one name, not one effect, not one duration.
- **No brightness schedule**, and no decision on which clock it follows.
- **No rounding rule** for the progress fill. At exactly 50 % of a 48-LED range the count is 24
  either way; every other percentage differs between floor and round, which is Q-17 and not this
  document's call.
- **No layout** in the composer: no region count, no grid, no default composition. The composer's
  *form* as data (R-35) is a reading of what "composer" must mean, not a design of one.
- **No design and no code.** Nothing here says how any of it is built. There is no architecture in
  this document, deliberately: it decides what the light shows, and the how is the next seat's.

---

*Draft for correction. The operator's words are the authority in every `operator` requirement; the
`derived` ones are this seat's reading and are wrong if he says so; the open questions are his to
answer and nobody else's.*
