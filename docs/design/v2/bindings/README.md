## Bindings

The next tab to walk. Grouped **by light**, which is the whole design decision: on a light, a segment
can carry only one printer, and that is invisible in a flat list.

### What is on the page

- **One card per light**, with its address, its dimensions, and whether it answered.
- **A row per binding**: printer, segment, brightness, when it last wrote, an enabled switch, and
  `test` / `remove`.
- **Segments are the light's own** — read from it, never typed. A segment already carrying a printer
  is offered greyed out, with the printer's name in the option, so the refusal is visible before you
  try it.
- **Brightness only exists on a whole-light binding.** Choose a zone and the field says *none — a
  borrowed zone never gets a brightness*. That rule is why the module never dims somebody else's
  furniture.
- **A count per light** — "2 of 2 segments free" — and the add button switches itself off when none
  are.
- **Lights found with no printer on them**, each with a `+ add a binding` button. These are the
  *discovered* ones — a light is found on the network, a printer comes from Prusa Connect — so a light
  sitting unused is the actionable gap. Pressing the button brings the light into the module and opens
  the binding dialog already pointed at it.
- **Printers with no light**, each with a `+ add a binding` button. Every printer Prusa Connect knows
  about, less the ones already on a light. The button opens the dialog with that printer already
  chosen and the light preselected to one that still has a free segment — you pick the rest.
- **What is refused**, at the bottom, in one line each: a second binding on a segment; a brightness
  on a borrowed segment; a segment the light does not have; a light that does not answer.

### The rule that cost me a correction

A **whole-light binding covers every segment of that light**, and any segment binding excludes the
whole light. My first version only compared exact segment ids, so a light with its whole light taken
still offered its zones — which the module would then refuse at start-up. It is fixed: "taken" means
*same segment, or either side is the whole light*, and a light carrying one zone correctly reports
its whole light as unavailable.

### Open questions for you

1. ~~**Should a printer be allowed on two lights at once?**~~ **Answered: yes.** One printer on two
   lights, for multi-room status — a panel in the workshop and a strip in the office showing the same
   job. It costs a second steady stream for that printer, and it forces a change in Events: a preset
   belongs to a light, so a moment on a two-light printer cannot name one preset once.
2. **What should happen to a binding whose light stops answering?** Today: the light goes to its
   "can't reach" colour and keeps trying with widening retries. The alternative is to drop the binding
   after a while, which loses the configuration for a transient outage.
3. **Does a whole-light binding take precedence over a zone binding on the same light?** The module
   refuses the pair, so the question is really whether that refusal is right — or whether a whole-light
   binding should simply be a zone binding covering everything, and be allowed alongside others on
   segments it does not overlap.

### What is honest about this page

The segment lists are the real ones (morpheus has a panel; CyberDesk has six named zones), but the
module **reports no segments today** — `GET /controllers/{name}` returns an empty list, so the
segment dropdown is the one control here that needs the API before it can be real. Everything else
in this tab is either live today (add, edit, delete, the exclusivity refusal) or a one-line change.
