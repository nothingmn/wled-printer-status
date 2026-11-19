## Right now (Overview)

The landing page, built to answer exactly one question: **what is on my walls at this moment**.

### What it deliberately is not

Not a dashboard. There are no tiles, no counters, no "writes per second", no charts. Every one of those
would be a second copy of something Diagnostics already reports, and a second copy is a second place to be
wrong.

### What it is

- **One sentence** at the top: *"2 lights showing 2 printers · nothing needs you"* — or, when something
  does, the sentence says which light and for how long: *"CyberDesk has not answered for 3 minutes"*.
- **One row per light**, and each row carries only what you would see by walking over: the picture the light
  is drawing, which printer it is showing, and when we last wrote to it.
- **The panel row draws the composed frame** — bar, percentage, job name — because that is what the light
  shows. **The strip row draws only its zone**: CyberDesk's `Front 1`, not the whole 48 LEDs, because the
  rest of that strip belongs to somebody else.
- **A toggle for the unhappy case**, so the page can be judged in both states without unplugging anything:
  *"everything answering"* and *"as if one light went quiet"*.

### The two things it gets right

1. **When nothing is wrong it says so in one line**, rather than making you read sixteen green elements to
   discover the same thing.
2. **When something is wrong it names the light, not an error code** — *"CyberDesk has not answered for 3
   minutes"* is the sentence an operator wants.

### What is honest about it

The picture is **reconstructed** from what the module reports — colour, fill count, percent, name and
reason — not a screenshot of the controller. On this firmware the controller's own pixels cannot be read
back, so a reconstruction is the only honest option, and it is drawn with the same 5×7 glyphs the module
uses, so it does not lie about the shape.

### Pausing — everything, or one light

Two scopes, one mechanism:

- **Pause everything** in the header — stops every write and every log line for the whole module. The
  printer is still watched; nothing is sent and nothing is written down.
- **Pause** on any row — the same thing for one light only. The row dims, the chip reads
  `paused · 3 days`, and the summary counts it: *"… · 1 paused"*.

**Six durations**, chosen by the operator's own example: 10 minutes, 1 hour, 6 hours, 1 day, 3 days, and
**forever**. His words: *"if the printer is in a guest bedroom, I would want to pause it for say, seventy
two hours while I have guests sleeping in that room"* — which is what `3 days` is for — and *"I would add
pause forever as well."*

**A trap this caught in my own code:** `forever` was first stored as `null`, which is exactly the value
used for *not paused*, so a forever pause silently did nothing and the row kept rendering as "printing".
It is now a distinct sentinel, and the test is "is the pause value set", not "is it truthy".

A global pause wins over a per-light one: the rows read `paused with everything`, and resuming is a single
click that repaints every light immediately.

### The question a pause has to answer — answered

**A pause switches the light off.** The operator's words: *"I would actually turn the light off if it's
paused. Don't leave it at the current state. That's misleading, but literally turn the light off."*

So a pause is **one write and then zero**: the light goes dark, and after that nothing is written and
nothing is logged until it resumes. The thumbnail shows black, because black is what is on the wall — a
frozen green bar would be a picture of something that is no longer true.

**The one exception, and it matters:** a binding that holds a *borrowed zone* cannot switch the light off.
The rest of that strip belongs to somebody else, and powering it off would take their pixels with it. So a
borrowed zone is **handed back to its owner** — the module restores what it captured and stops — and the
whole light is never powered off. That is the same rule the module already keeps everywhere else: give back
what is not yours.

Resume repaints immediately, because the light is dark and the first thing it must do is catch up.

### Two implementation facts worth recording

- **A timed pause must survive a restart**, so it cannot live only in memory. It belongs in the module's own
  writable data directory, not in the host's settings — settings are read once at start and a save restarts
  the module, which would make "pause for 3 days" a thing you could lose by touching an unrelated setting.
- **Resume repaints immediately.** A light that has been paused for three days is showing a three-day-old
  picture; the first thing resume must do is repaint, not wait for the next percentage tick.

### The thumbnails — can they actually be drawn?

Yes, and cheaply, but be precise about what they are.

They are **reconstructions, not screenshots**. The module knows the frame it decided and sent — colour, fill
count, percent, job name, reason — and the page redraws that with the same 5×7 glyphs the module uses. On
this firmware the controller's own pixels cannot be read back, so a reconstruction is the only honest
option; it is faithful to the decision, not to a photograph.

What it needs from the API: **one route that returns a frame per binding**, because `/status` today answers
for the module as a whole. `GET /frames` returning an array of `{binding, colour, lit, total, percent, name,
reason, last_send}` would do it. Marked `needs API` — and it is small.


### Quick bind — putting a printer on a light from this page

The operator's ask: *"you can list all the printers and then list anything auto discovered — all the lights
that are auto discovered — and then do a simple basic binding on that page immediately. So just one click
bind in between the printer and the light, and we just accept a bunch of default basic default setup."*

So the page has two columns under the wall:

- **Printers** — every printer Prusa Connect knows about, each saying where it already is (*"on morpheus"*)
  or *"no light yet"*. One click picks one.
- **Lights found** — everything discovery answered with, each carrying a `bind` button that names the picked
  printer: *"bind XL"*.

One click on a light and the binding exists. **`bind them all`** in the column header pairs every remaining
light with the picked printer in one go — the case where somebody has bought three strips.

**The defaults are stated on the page, not hidden behind the click:**

- the **whole light** — no segment, until you pick one in Bindings
- **brightness** follows the day schedule if there is one, else 128
- **colours** from the table, untouched
- a **panel** gets the bar, percentage and job name; a **strip** gets the fill
- **events** — the finished word on a panel, its colour on a strip; the rest off until switched on

Verified by clicking: with nothing picked the bind buttons are disabled; picking `XL` turns them into
*"bind XL"*; one click adds a row for `XL` on `WLED-8F2A`, drops that light from the discovered list, and
the summary counts *"1 light still to set up"*. `bind them all` with `Core One` bound the last light and the
column read *"Every light that answered is now in the module."*

**Does the module need a landing page at all**, or should it open on Bindings? This mockup is the argument
for keeping one: if it stays this small, it earns the tab by answering the one question nothing else
answers at a glance. The moment it grows tiles and summaries, it becomes a second Diagnostics and should be
deleted.
