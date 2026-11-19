# Changelog

What an operator sees change when they upgrade this module, newest release
first. `version:` in `module.yaml` is the newest heading below.

Write the entry in the same change as the code. One bullet per behaviour a
person outside this repository can observe: a route, a setting, a screen, a
refusal. Name the route and the setting key, never a function or a file.

## 0.1.0 - 2026-11-18

The first release, published as a beta: the marketplace lists it on its beta channel, and it will change.

One printer is read through the Connect instance the module runs beside, and one WLED controller is
painted — a colour for the printer's state and a fill for the job's progress. On a controller that carries
several segments the module borrows one, draws inside it, and gives it back when it stops. A finished job
holds the whole light, and keeps holding it.

### Added

- `GET /status` answers what the module currently thinks: the printer it watches, how old that reading
  is, the frame it has decided, and what the last write to the light did.
- The setting `controller_address` names the one WLED controller to drive when `bindings` is empty. It is
  no longer required, because an instance that lists several printers in `bindings` has no single address
  to name; with neither written the module refuses to start and says so.
- The setting `printer` picks the printer to watch, by device id or by name. Left empty it takes the only
  printer the instance holds, and refuses to choose between several.
- The setting `brightness` sets what the module writes to the controller once at start, 0-255.
- The setting `brightness_schedule` follows the time of day: one window per line, each with `from` and `to`
  as `HH:MM`, a `brightness` 0-255 and optionally `days`. A window's `from` minute is inside it and its `to`
  minute is not; a window may wrap midnight (`22:00`-`07:00`); the first window that contains the moment
  decides; and wherever none does, the fixed `brightness` stands. Empty or absent, nothing changes from the
  brightness this module has always written, and it is written once per change rather than once a tick — on
  the next turn of the writer's own loop, so a window's edge lands within a second or two. A borrowed
  segment never follows it: it writes no brightness at all.
- The setting `timezone` names the zone a `brightness_schedule` is judged in, as an IANA name such as
  `America/Denver`. It is **required** whenever a schedule is written, and a schedule with an empty or
  unloadable zone refuses the start with a sentence naming both settings — this module runs inside a
  container whose clock is UTC, so a schedule read there would fire seven hours away from the operator's
  evening and nothing would say so. There is no fallback to UTC. The zone database is compiled into the
  binary.
- The setting `segment` names one segment of a controller that carries several. The module drives that
  segment and leaves the others alone: it reads the segment's run, colour, effect and name once at start,
  draws inside that run, and writes them all back when it stops. In this mode it writes no brightness and
  no power at all — both are controller-wide and would move every segment at once — and no body it sends
  names a second segment. `-1`, the default, drives the whole strip as before. On a controller that reports
  a matrix the setting is refused and the log says why, because a panel's body is two rectangles covering
  the whole of it.
- The module drives the light. It writes the decided colour and progress to the controller as one request
  carrying two segments — the lit part in the state's colour and the rest in the off colour — and writes
  nothing when the picture has not changed, so a state that is merely being re-read does not flicker.
- A frame that has stood unchanged for a minute is written again, so a controller that rebooted comes back
  to the printer's picture instead of staying in its own saved state. While the picture is moving the
  ordinary writes reset that minute and the repaint never fires, so it costs nothing on a print in
  progress.
- A controller that does not take a write is retried on a wait that doubles from a second to half a
  minute, rather than once a second, and a picture that changes during that wait is sent when the
  controller answers rather than attempted immediately. The log reports the first failure, each longer
  wait, and the recovery: five minutes of an unreachable light is six lines instead of three hundred.
- A finished job spells `FINISHED` across a matrix panel, in the finished green, for a minute, and then
  hands the light back to the steady picture. The letters are the module's own: a 5x7 font it carries,
  laid out as pixels and pushed over DDP. No controller effect is asked to draw anything. A strip is
  unchanged and keeps its solid green. The segment's name is cleared when the minute is up, so the word
  does not outlive the event on a controller that keeps a name through a write that omits it.
- `POST /preview-finished` shows that word now, for the same minute, so a person can look at what the
  light will do without waiting for a print to end. It answers `202` with the word, the controller and how
  long it holds, and refuses — with a registered code — when the light is a borrowed segment of somebody
  else's controller, a strip, or a panel too small for the word.
- `GET /wled-printer-status` answers one line and the principal the host authorized.
- The setting `message` sets the line that route answers with.
- The setting `bindings` carries the whole topology as one JSON list: one line per printer, each with its
  controller and the segment it is shown on. It wins over `controller_address`, `printer`, `brightness` and
  `segment` when it is written, and it is the setting that makes two printers on two controllers one
  install. A line that leaves `segment` out drives the whole controller; a line that names one borrows it
  and may carry no brightness. A list the module cannot run refuses the start, and the sentence names the
  line and the field, such as `bindings[1]: json: unknown field "controler"`.
- `GET /bindings` answers the set of bindings the module is running, in the operator's order, with each
  printer's name read from the host. `GET /bindings/{id}` answers one, `POST /bindings` adds one,
  `PUT /bindings/{id}` changes where one printer is shown, and `DELETE /bindings/{id}` stops showing it.
  Every write takes effect at once — the writer is stopped, a borrowed segment given back, and the watch
  started again on the new set — so no restart and no reinstall is needed. None of them save anything: the
  settings belong to the host, so a restart puts back what the settings say, and every answer says so with
  `"persisted": false`.
- `GET /printers` answers the host's printers with the binding that already shows each one, which is where
  a device id comes from: nobody types one. `GET /controllers` answers the same set grouped by controller,
  with the segments each gives up for it.
- **A settings tab that lists the bindings, and edits them.** The module now ships a browser half: one tab
  on its own page under Settings, Extensions, beside the settings it already had. It lists every printer
  with the light it is drawn on, the segment, and whether that light is answering; it adds a printer,
  changes where one is shown, and removes one, with the printers and controllers offered as the choices
  `GET /printers` and `GET /controllers` report rather than as ids typed in. It is additive: the eight
  settings are still the whole configuration, and a module with no bundle loaded — the bundle refused, its
  library range outside the app's — drives the light exactly as before. It writes through the same routes,
  so it saves nothing either, and the tab says so on every screen.
- The binding routes refuse with codes of their own — `BINDING_NOT_FOUND`, `BINDING_REFUSED`,
  `BINDING_BODY_INVALID`, `CONTROLLER_NOT_FOUND` and `PRINTERS_UNAVAILABLE` — and a refusal leaves the set
  that was running running.
- The install asks for `observe`, because the module now reads one printer as itself, and declares two
  outbound connections to the controller in `controller_address`, from this process: port 80, where the
  status colour and the progress are written, and port 4048, where the pixels of the finished word are
  sent over DDP.

### Changed

- **The `FINISHED` word reaches the panel.** It is sent as pixels over DDP — to the controller's port 4048
  — instead of as a per-LED array in a JSON state body, which this firmware accepted and drew by nothing.
  The word is pushed again once a second for the minute it holds, and the light goes back to the steady
  picture when that minute is up. Nothing else about the event changes: same word, same colour, same
  minute, and a preview still shows exactly what a finished print will.
- A print the printer has taken but has not yet moved shows the waiting yellow, not the printing green. At
  0 % progress the light is the same yellow an idle printer shows, and `GET /status` answers
  `"reason": "starting"` with the printer still reported as printing. A whole panel in green is what a
  finished job shows, and a print that had not started was being read as one. From the first lit pixel
  upwards the frame is unchanged: the fill is the printing green and its length is the progress.

### Not in this build

- **No text but the one word.** `FINISHED` is the only thing the module spells, and only on a matrix
  panel wide enough for it: there is no message setting, no clock, and nothing on a strip.
- **No animations.** No pulse, no fade, no movement: a state is one colour and a fill, and the one word
  that is drawn sits still.
- **No layout composer.** The picture is the module's; the operator does not arrange it.
- **No screen beyond the bindings tab.** The bundle is that one tab: no page of its own in the sidebar, no
  section on a printer's card, and no animation editor. The printer and the segment are still written by
  hand in `bindings` when nobody wants to open the tab, and that remains the only durable shape.
- **No saving through the API.** A binding changed through the routes lives until the process restarts.
  The durable shape is the `bindings` setting.
