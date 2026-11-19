## Settings

The tab about the thing doing the work, rather than what the light does.

### What is on the page

- **What it is**, read from the manifest and the process: version, kind, memory budget, permissions
  asked, **egress declared** (HTTP 80 and UDP 4048 — the reviewer checks these against what the code
  actually does), platforms built, lifecycle.
- **Yours to set**: the **timezone**, which is the zone the brightness schedule is judged in — validated
  as you type, and refusing with the module's own wording: *"timezone is "America/Denverr", which is not a
  zone this build can load: write an IANA name such as America/Denver, because the schedule is judged in
  the zone you name and not in this process's own clock"*.
- **The log level** is gone too. It was a disabled select marked `needs API` with a paragraph explaining
  that verbose mode is deliberately absent — which is a paragraph the user does not need on a page with
  nothing to change in it. The policy is stated once, in Diagnostics, where the log is actually read.
- **One thing to know before saving anything**: a settings save **restarts the module**, because the host
  owns the configuration and the process reads it once at start. The light blinks once on the way through.

### A section was removed on review

An earlier cut carried a card headed *"What this module cannot do"*, listing the `docker restart` and
`module remove` commands. The operator's verdict: *"I don't understand the purpose of that section. Hard to
justify, I suspect we should just remove it."* He is right — a web page reciting shell commands is
documentation in the wrong place, and those commands already live in `docs/installing-locally.md`.
**Restart and remove are simply not on this page.**

### Why the dead controls are dead

Every disabled control on this page says why in the same breath. A greyed-out select with no explanation
is a bug report waiting to happen; a greyed-out select that says *"changes only — the fixed policy"* is a
decision the user can see.

### What is honest here

- The memory budget note is real: **32 MiB rather than 16, because the Go runtime takes 85% of it as a
  soft limit and at 16 the ceiling lands inside normal use.** Measured, not guessed.
- The timezone refusal is the module's own wording, so the page cannot drift from the thing that does the
  refusing.

### Open question

Nothing in this tab is waiting on a decision, except whether it should exist as a tab at all rather than
living behind a cog on the module header. My view: a tab is fine — it is where someone looks when
something is wrong and they want to know what they are running.
