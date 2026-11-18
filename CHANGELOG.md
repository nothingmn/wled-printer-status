# Changelog

What an operator sees change when they upgrade this module, newest release
first. `version:` in `module.yaml` is the newest heading below.

Write the entry in the same change as the code. One bullet per behaviour a
person outside this repository can observe: a route, a setting, a screen, a
refusal. Name the route and the setting key, never a function or a file.

## 0.1.0 - unreleased

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
- `GET /wled-printer-status` answers one line and the principal the host authorized.
- The setting `message` sets the line that route answers with.
- The install asks for `observe`, because the module now reads one printer as itself, and declares one
  outbound connection: the controller in `controller_address`, on port 80, from this process.

### Changed

- A print the printer has taken but has not yet moved shows the waiting yellow, not the printing green. At
  0 % progress the light is the same yellow an idle printer shows, and `GET /status` answers
  `"reason": "starting"` with the printer still reported as printing. A whole panel in green is what a
  finished job shows, and a print that had not started was being read as one. From the first lit pixel
  upwards the frame is unchanged: the fill is the printing green and its length is the progress.

