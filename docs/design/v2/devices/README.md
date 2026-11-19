## Devices

Bindings says *which printer is on which light*. This tab says *what the light is* — and nothing about
printers beyond naming which one a segment carries. That split is the whole point: it stops the two tabs
growing into each other.

### What is on the page

- **A card per light**: address, WLED version, pixel count, shape (2D panel or 1D strip), maximum
  segments, DDP port, and whether it answered.
- **Its own segments** — id, name, range, and which printer is on each. This is the list Bindings picks
  from, shown where it comes from.
- **Its own presets** — how many slots are defined and which are named. **Read only, and deliberately
  so**: presets are created and tuned in WLED's own interface. This is the same rule the Events tab is
  built to.
- **Test the pixels** — `solid`, `border`, `quarters`, `the word`, `identify`. The first four exist and
  are proved on hardware; they are what diagnosed the display.
- **Found on the network, not added** — discovered lights, each with `+ add`. Adding asks the light what
  it is first — version, pixels, segments — so a wrong address fails at the door instead of at 3am.

### Refusals stated, and two of them built

- **A light that does not answer when added** is refused rather than saved broken.
- **Removing a light that carries a printer** is **offered, and unwinds it** — the confirmation names
  every printer that will be left without a light, and says the light itself is untouched (no preset,
  segment or brightness changes; the module simply stops writing). Verified: *"Remove morpheus?"* →
  *"morpheus removed — Hermes MK4 now has no light"*.
- **A second light at the same address** is refused.
- **Editing a segment's name or range** is not offered — segments belong to WLED, same as presets.
- **Editing a preset here** is not offered at all.

### `identify`

Turns the light on and off, **one second in each state, five times** — ten seconds, unmistakable across a
room, and it leaves the light exactly as it found it.

### A detail worth keeping

When a light is not answering, its **test buttons are disabled** and the page says why. Offering a test
button on a light that cannot receive would be a button that always fails, which teaches the user to
distrust the tests that do work.

### Replacing a light — when the hardware dies

The operator's case: *"my entire WLED lights, the LEDs and the controller and everything just died on me
and I had to replace them. I'd want to install them, add the light — hopefully auto discovery would work —
and then be able to just replace this light, and all the printers that are using it would repoint to the
new light."*

Every light card carries **`Replace this light…`**, and the flow is deliberately picky, because a swap is
supposed to change nothing except which box is driving the pixels.

#### Hardware must match — a gate, not a warning

**Same shape and same pixel count, or the swap does not happen.** His words: *"the panel size and led count
have to match must match."* Candidates that fail are shown in the list **disabled, with the reason**
(*"cannot replace 48 px strip"*), so the operator sees why rather than wondering where the light went. A
panel→strip "downgrade with a warning" was the earlier design and he cut it: a smaller light is not a
replacement, it is a different display.

**Physical mounting is explicitly out of scope.** Whether the new panel is hung where the old one was, or
upside down, is not the module's business — it cannot know, and pretending to check would be worse than
saying nothing.

#### A missing segment pauses the swap, and the page walks you through creating it

Segments are matched **by name only**. There is no "same position" guess and no chooser, because a name is
the contract the operator controls.

When the destination light does not have a segment the old one uses:

1. **The swap pauses** and names what is missing, per segment.
2. **`copy the name`** puts the segment name on the clipboard, so it can be pasted straight into WLED's
   segment name field on the new light — and the toast says exactly where to go and to save the preset.
3. **The page polls the new light every 23 seconds** and ticks each name off as it appears, with a live
   *"checked 2 s ago · checking again in 21 s"* and a **check now** button for the impatient. The operator
   can go back and forth, copying one segment at a time, and watch them disappear from the list.
4. **Swap stays disabled** until every name is present — *"Waiting for 1 segment"* — then reads
   *"Move 1 printer to WLED-4D19"*.

**Verified by clicking:** `CyberDesk` (48 LEDs, six zones, `Front 1` carrying Mini) against `WLED-4D19`
(48 LEDs, three zones) paused with *"does not have 1 segment that CyberDesk uses"*; `copy the name` put
`Front 1` on the clipboard with the instruction; the countdown moved from *0 s / 23 s* to *2 s / 21 s*; and
once the segment appeared the button became `Move 1 printer to WLED-4D19`. A panel→panel swap with the
segment already named matched immediately with no pause.

#### Snapshot, and restoring it onto the new light

Every light card carries a **snapshot** line and two buttons:

- **`refresh now`** — pulls `GET /json/cfg` and `GET /presets.json` immediately.
- **`download a backup`** — writes a JSON file of the same thing, for keeping outside the module.

The module pulls on a **five-minute cycle**, and the reason is not speed — a config is 3 KB and reads in
milliseconds. **The reason is that a dead light cannot be read.** The snapshot is the copy that exists
*because* it was taken while the device was alive, which is exactly the case the operator described.

Measured on his hardware: `morpheus` **3,230 bytes** of config and **10,620** of presets; `CyberDesk`
**2,623** and **12,205**. About 15 KB a device — small enough to keep **in memory**, which is what we do,
rather than writing a file of SSIDs to the host.

**What the swap restores, and what it refuses to.** The dialog lists the sections with checkboxes:

| restored | never |
|---|---|
| `light` — LED type, count, matrix, colour order | `nw` — wifi networks and credentials |
| `def` — default brightness, preset, power-up behaviour | `rev` — firmware revision |
| `if` — MQTT, NTP, sync, live data, Hue | `vid` — hardware revision |
| `timers`, `um` — the light's own timers and usermods | |
| `ap` — the fallback access point | |
| `presets` — every preset, by number and name | |
| `id` — its name and mDNS name | |

**`nw` is the one that matters**, and it is not a limitation, it is the point: the new light is already on
the network, or we could not be talking to it. Writing its wifi section is the single best way to strand
it mid-restore. And there is nothing secret to restore anyway — **WLED does not put the PSK in a config
dump**; both of his devices report `ssid present: true, psk present: false`. So a literal 100% clone
including the wifi password is not possible over the API, and the page says so rather than implying
otherwise.

**The reboot window is shown, not hidden.** A swap reads: *moving the printers → writing the snapshot →
asking it to reboot → waiting for it to answer → answered after 14 s, configuration matches the snapshot.*
That sequence is on screen while it happens because it is the risky part: a cfg write reboots the device,
and for about fifteen seconds the light is gone. **`POST /json/cfg` itself has not been exercised against
his hardware** — that stays unverified until it is.

**Verified in the mockup:** the restore table renders 8 checked sections and 3 disabled ones; the button
reads *"Move 1 printer and restore"*; and after the swap the new device carries `Hermes MK4`, its own
snapshot line at *"just now · 3.2 KB · 10.6 KB (11 defined)"*, and the old light's presets and names.

- **Segments are `needs API`** — the module reports none today (`GET /controllers/{name}` returns an
  empty list), so the table is the one thing here that cannot be filled yet.
- **Presets are readable today** (`/presets.json`) and already inform what this page shows.
- **The tests exist today** and are the four that proved the display.

### Open questions for you

1. **What should `identify` do** — blink the light three times, or paint a number so you can tell two
   identical strips apart?
2. **Should this tab offer editing a segment's name or range?** My view: no. Those live in WLED, the
   same as presets, and a copy here would drift.
3. **Removing a light that carries a printer** — refuse (as built), or offer "remove and unbind its
   printers" as an explicit second action?
