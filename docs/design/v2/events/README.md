## Events → presets

The design decision behind this mockup: **an event names a preset and does nothing else.** No effect
picker, no speed, no palette on the event row. Every one of those settings is touched exactly once,
inside *create a preset*, and the result is saved into a slot on the light.

### Why presets rather than the module drawing animation

- The light renders it. No DDP, no 50 kB/s, no pixel arithmetic for the length of the print.
- It survives a module restart, because it lives on the light.
- It is visible and editable in WLED's own UI too — the operator can tune a preset there and this
  page picks it up, because the list is read from the light rather than invented here.
- It is the same choice Prusa Connect's own modules make: name what the hardware can already do.

### The flow as built

The page is **scoped to one light** by the selector at the top, and the moments are always the top
level.

- **One printer on that light** — one row per moment, exactly as before: enabled, moment, shows,
  which, for, test.
- **More than one printer** — each moment stays the top row (marked *2 printers*) and beneath it, one
  **indented sub-row per printer**, carrying that printer's segment and its own settings. The moment
  row itself holds no controls; the sub-rows do.

Each moment's `shows` and `which` behave as before: `Preset` lists the presets that light already
has, `Word` offers a text box (**panel only** — a strip does not offer it at all), `Colour` names the
colour, `Nothing` does nothing.

**Settings therefore belong to a (light, printer) pair**, not to a printer. That is also what makes
multi-room sane: the same printer on two lights carries two independent sets, so the office strip can
show something different from the workshop panel.

**There is no preset gallery and no preset editor.** Presets are created, tuned and deleted in
WLED's own interface, where the effects and palettes actually live. This page only names one.

### The rule that has to hold

A preset carries its own segments, brightness and LED map, so running one **takes the whole light**.
The module captures what was there, runs the preset for the moment, then puts it back. On a borrowed
zone it refuses instead — somebody else's desk light is not ours to take over, even briefly.

The consequence to be aware of: for the duration of a preset, the progress bar is gone. That is fine
for a finishing moment; it is a decision for a long one such as *error, until cleared*.

### What is honest about this page

The names come from the panel's own `/presets.json` — five named presets today, six empty slots. The
module **reads** them and never writes one: creating or editing a preset is WLED's job, not ours.
