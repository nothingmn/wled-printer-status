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
- The setting `controller_address` names the WLED controller to drive, and is required: the module will
  not start without it.
- The setting `printer` picks the printer to watch, by device id or by name. Left empty it takes the only
  printer the instance holds, and refuses to choose between several.
- The setting `brightness` sets what the module writes to the controller once at start, 0-255.
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
  drawn one LED at a time. No controller effect is asked to draw anything. A strip is unchanged and keeps
  its solid green.
- `POST /preview-finished` shows that word now, for the same minute, so a person can look at what the
  light will do without waiting for a print to end. It answers `202` with the word, the controller and how
  long it holds, and refuses — with a registered code — when the light is a borrowed segment of somebody
  else's controller, a strip, or a panel too small for the word.
- `GET /wled-printer-status` answers one line and the principal the host authorized.
- The setting `message` sets the line that route answers with.
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
- **No multi-binding.** One printer per installed module and one controller per instance. A printer this
  instance does not watch is a printer on no light at all.
