# Changelog

What an operator sees change when they upgrade this module, newest release
first. `version:` in `module.yaml` is the newest heading below.

Write the entry in the same change as the code. One bullet per behaviour a
person outside this repository can observe: a route, a setting, a screen, a
refusal. Name the route and the setting key, never a function or a file.

## Unreleased

Waiting on `version:` in `module.yaml`, which is not raised until this is released.

### Added

- A printer may sit on several lights. Two bindings may show one printer — a panel in one room and a strip
  in another — and each draws the same job for the room it is in, which is the multi-room status the
  requirements ask for. The same segment of the same controller is still one binding's, and the refusal
  names the light, the segment and the binding that got there first.
- A binding is named by its own `id`: the printer's device id while that printer has one binding, and
  `<device id>@<light>:<segment>` — `07e893bb-b922-4ff0-9ad3-061b957400c3@10.0.0.253:whole` — once it has
  two. The name is written into the configuration document when it is minted and never changed, so the
  binding an install is already running keeps the path it answered at: `GET /bindings/07e893bb-…` still
  answers. `GET /bindings` and `GET /bindings/{id}` carry `id`, `light`, `segment` and `whole`, and
  `GET /printers` carries `bound_count`, so two rows for one printer can be told apart. `POST /bindings`
  answers the new binding's `id`, which is the path to use afterwards.
- `GET /bindings/{id}/layout` and `DELETE` on it are keyed on the binding rather than on the printer, so a
  panel and a strip showing one machine keep two pictures instead of one shared one.
- `GET /controllers/{name}` answers the segments the light itself holds — its `id`, its `name` and its
  four bounds — which is the list every segment picker is built from. `segments_known` says whether the
  controller answered; a light that is unplugged answers `200` with an empty list and `segments_unread`
  saying why, instead of failing the route or offering an empty list as though the light had no segments.
- The module keeps its own configuration in one versioned JSON document in a writable directory it now
  asks for at install. The host's settings are read once, as a migration source, and the document is what
  runs afterwards. That is what makes a change made through the module's routes survive a restart and an
  upgrade — and, as before, not a `module purge` or a fresh install, which wipes the directory.
- A configuration document written by an older release is upgraded to the current schema version and
  written back stamped, rather than being refused or read as though it were current. One written by a
  newer release is refused, naming both versions.
- `GET /states` answers the state table the Colours screen draws: one row per state with the colour, the
  word and the `holds` value in force, and — beside each of the three — whether it is yours or the
  built-in default, so a screen can offer a reset only where there is something to reset. The ten states
  the printer reports are there, and so are the two conditions it has no word for, `Disconnected` and
  `Stale`, marked as conditions and counted separately.
- `PUT /states/{state}` sets a state's colour, word and hold, and `DELETE /states/{state}` puts it back to
  the colour, no word and `holds: forever`. A `holds` that is not `forever`, `30s`, `60s` or `5m` is
  refused by name rather than rounded, and a colour channel outside 0-255 is refused with the number. The
  answer says what the word is doing — `word_shows` is `showing`, `expired`, `later` or `none`, with
  `word_until` beside it in RFC 3339 — so a screen can say "on the light until 20:01" rather than showing a
  200 for a word that will never appear. `GET /states` and `GET /states/{state}` carry the same two fields.
- `GET /lights/{light}/moments` answers one light's whole moment table: every moment it has, with one
  entry per printer on it — the same rows the screen draws as sub-rows when a light carries several
  printers, and the flat table when it carries one. `PUT /lights/{light}/moments/{moment}` sets one
  printer's entry and `DELETE …?printer=` puts it back to the default. A `which` that does not belong to
  its `shows` is refused with the reason. Only the finished moment is on by default, showing the word
  this build already draws for a minute.
- A moment can be edited while the light is not answering: a preset name is taken as written and the light
  is never asked.
- The moment table is what the light shows when something happens, and this release fires it. The
  transitions the printer reports are looked up per binding — finished, started, paused, cancelled,
  filament change, error or attention — and the entry is shown for its `for`, a duration or `forever`, and
  then handed back; a printer that stops reporting while a job is live shows the `printer-went-quiet`
  entry. `Preset` takes the whole light, names one of its own presets and puts back what it captured, and
  is refused on a borrowed zone with the segment named. `Word` is drawn on a panel, and a strip shows the
  moment's colour instead. `Colour` paints that state's colour, so a recoloured state is what a moment
  about it paints. `Nothing`, and an entry that is switched off, show nothing at all. A binding nobody has
  edited fires what this module has always fired: the finished word, for a minute.
- The configuration document is at schema version 2. A version 1 document is stepped up in place, bindings
  and all, with both new tables empty; one written before the version was stamped is stepped through both
  shapes rather than skipping to the current one.
- `GET /bindings/{id}/layout` answers the layout one binding composes with — `bar`, `bar_band`, `bar_rows`,
  `percent`, `percent_scale`, `name`, `rotation` and `rotate_seconds` — with, beside each value, whether it
  is yours or the built-in default. `PUT /bindings/{id}/layout` writes one and `DELETE` puts it back to the
  default; `GET /lights/{light}/layouts` answers every binding on one light. A layout is per binding
  because a panel and a strip do not compose the same way. A binding nobody has edited answers the default
  rather than an empty object.
- The layout table composes the panel, and `percent_scale` and `rotation` are drawn rather than only
  reported. `percent_scale` pins the digits to 5, 4 or 3, and a scale the band cannot hold is refused
  rather than cropped. `rotation: side-by-side` puts the digits on the left and the job's name on the
  right at scale 2, cut to the columns that are left, with the digits a scale smaller there, and a name
  with less than three glyphs of room is not drawn at all. `take-turns`, and a binding with no row of its
  own, draw exactly what this module drew before.
- `GET /windows` answers the brightness windows, judged in the zone the `timezone` setting names and never in
  the container's own clock, with the zone and the instant written beside them. `POST /windows` adds one,
  `PUT /windows/{index}` replaces one in its place and `DELETE /windows/{index}` removes one. A window whose
  end precedes its start wraps midnight and is accepted; one whose two ends are the same is refused, as is a
  brightness outside 0-255 and a window written with no `timezone` to judge it in. Overlapping windows are
  allowed and the first one wins. Nothing here moves the light yet.
- `GET /pause` answers every pause of both scopes: whether it is on, which scope it is, and the absolute
  instant it ends at — or that it does not. `PUT /pause` pauses everything and `PUT /bindings/{id}/pause`
  pauses one binding; `DELETE` on either resumes. The six lengths are `10m`, `1h`, `6h`, `1d`, `3d` and
  `forever`. The expiry is kept as an instant rather than a timer, so a timed pause survives a restart and a
  settings save. A binding that drives a borrowed zone is reported as `hands-back` and never `off`, because
  the rest of that strip belongs to its owner. This release stores and reports a pause and does not yet
  switch the light off.
- The configuration document is at schema version 4. A version 3 document is stepped up in place: every
  binding line gains its `id`, and every layout row is re-keyed from the printer to that binding with every
  field it carried unchanged. A version 2 document is stepped through both shapes, with the layout table,
  the windows and the pause arriving empty, and one written before the version was stamped is stepped
  through every shape rather than skipping to the current one.
- `GET /frames` answers one frame per binding for the Overview screen's thumbnails: the frame this module
  decided — colour, fill, total, percent, the job's name and the reason — how old the reading behind it is,
  when the light last took a write, and the picture itself as a PNG. The picture is drawn by the same code
  that draws the light, so a thumbnail cannot disagree with the panel, and it is a reconstruction: this
  firmware cannot read its pixels back. Nothing on this route opens a socket to a controller, so a light
  that has stopped answering still has a picture, from its last reading, with `stale` true and the age in
  seconds beside it. A panel is composed exactly as the painter composes it while a job is running — with
  the layout that binding composes with, and the half of a take-turns rotation that is up — and is
  otherwise its own fill; a light that is paused is drawn dark, a light holding a moment's word or colour
  is drawn as that word or colour, and a light running one of its own presets keeps the steady frame with
  a sentence saying so, because a preset cannot be read back. A binding that borrows a segment draws only
  that segment's run.
- `GET /devices/{name}/snapshot` answers the snapshots this module holds of one light, newest first, and
  `POST` on the same route takes one now: the light's `/json/cfg`, its `/presets.json`, its `/json/state`,
  and the identity a restore is checked against — the address, the name the light calls itself, its
  firmware, and its shape and pixel count. Each snapshot is a file of its own in the module's data
  directory, `0600`, named after the light and the moment, `10.0.0.136-20261119T183002Z`. They are files and
  not part of the configuration document, so the document stays at schema version 3 and a module purge that
  loses the bindings does not lose the backups.
  `GET /devices/{name}/snapshot/{id}` answers one, with the table a restore would send and the list it
  would not.
- Every snapshot says what it does not hold: the wifi password is not in a configuration dump at all on
  either of the operator's lights, so a literal hundred-per-cent clone including the credentials is
  impossible over the API, and `complete_clone` is false on every answer rather than implied.
- The four preview routes fire at **every light the instance drives** rather than only at an install holding
  one: `POST /preview-finished`, `POST /preview-frame` and `POST /preview-pattern` answer one entry per light
  in `lights`, and `?light=<name>` aims a request at one of them by the name `GET /controllers` gives it. A
  light that cannot take the picture is refused for that light and named beside the ones that did, and the
  request is refused as a whole only when no light took it. `PREVIEW_NOT_RUNNING` now means one thing —
  the instance is driving no light at all — instead of describing a two-light install the message did not
  fit. This is what a two-light install was missing: the firmware cannot be asked what its pixels are, so
  these routes are the only instrument that says whether anything is lit, and they refused on the one
  deployment with more than one light to look at.
- The diagnostic ladder runs on a strip. A controller that reports no matrix is one run of LEDs, so
  `solid` lights the whole run, `border` puts white at its two ends, and `quarters` becomes four bands in
  the same order — twelve pixels each on a 48-LED strip — instead of the route refusing the light outright.
  A word and a composed frame still need a panel and are still refused on a strip.
- The configuration document is at schema version 5. A state's row now carries the moment its word was
  written, and a version 4 document is stepped up in place with every table unchanged: no row gains a
  moment, because version 4 never recorded one. A version 6 document is refused, naming both versions, and
  one written before the version was stamped is stepped through every shape rather than skipping to the
  current one.
- `POST /devices/{name}/restore` puts a snapshot's configuration back on the light it was taken from. It
  requires `confirm: true`, refuses a snapshot from another light and one from a display of another shape
  or pixel count, takes a safety snapshot of the light as it is now and keeps it before writing anything,
  and waits for the light to answer again before it says the restore took — nine seconds at most, against
  the 5.7 seconds a measured reboot takes. It writes `light`, `def`, `if`, `timers`, `um`, `ap`, `id` and
  the presets, sends the `nw` section whole except for its `ins` credential, and never writes `rev`, `vid`,
  `hw`, `ol`, `ota`, `remote` or `wifi`; the answer lists both sides with the reason for each. A restore
  that writes the configuration and then does not see the light answer is refused with the safety
  snapshot's id rather than reported as a success.
- The Events screen is the moment table, one light at a time: the light is chosen at the top, the moment is
  the row, and a light carrying more than one printer gains an indented sub-row per printer with that
  printer's segment and its own `enabled`, `shows`, `which` and `for` — a light carrying one printer is
  exactly the flat table. `shows` decides what `which` is: a preset picked from the light's own
  `GET /devices/{name}/presets` list, or — when the light cannot be read — typed, with the route's own reason
  beside the field and the name stored as written, because a light that is unplugged must not stop somebody
  editing; a word
  for a panel; one of the twelve state
  names; or nothing. The field changes with the choice, and a `which` that does not belong to its `shows`
  is refused with the reason beside the field rather than coerced. A change is written into the module's
  own store and kept whether or not the light is answering, a row at its default says so and offers no
  reset, and a stored row offers one.
- The Colours screen shows ten rows, each drawn twice — a 256 × 64 panel and a 48-pixel strip — from the
  module's own font at its own scale of five, with a colour, an editable word and a `holds` per row. The
  rows are the approved screen's own: the eight states a person has something to say about, and, in the
  two places `BUSY` and `READY` would have taken, the light unreachable and the printer lost — the two
  conditions this module computes, and the host has no word for either. `BUSY` and `READY` are still in
  the table the routes serve and still paintable and overridable; this page does not edit them and says
  so. Each of the colour, the word and the hold says whether it came from you or from the table, a reset
  appears only where something is stored, and the word's standing is shown rather than implied: `showing`
  with the instant it stops, `expired` when the hold has run out — where the preview drops the word,
  because that is what the light shows — or `later` when no light is showing that state. A character the
  font cannot draw, and a word too wide for the panel at that scale, are named beside the field, because
  the module refuses such a word and draws the state's colour instead.
- The Layout screen has two renderings, because a panel and a strip are not the same hardware. A **panel** is
  the composer: the bar on or off, the band it sits in, how many rows it takes of the panel's own height, the
  percentage at scale 5, 4 or 3, the job's name, and whether the two take turns or sit side by side, every N
  seconds — and the arithmetic is shown rather than hidden, with the boxes drawn to scale and a verdict: 64
  rows with a 12-row bar leaves 52, and digits at scale five are 35 rows. A layout that will not fit is
  **refused, not cropped**, so a change that would draw an empty panel is not written and the sentence says
  which numbers disagree; a light whose frame has never been drawn has no known height, and then the write is
  allowed and the fit is judged where it is drawn, exactly as the route argues. Every value carries whether it
  is yours or the default, a binding nobody has laid out answers the built-in default rather than an empty
  box, and `DELETE` on the binding — one reset for the whole row, because that is the route — puts every value
  back. A **strip** is deliberately almost empty: it fills from the left, 0 % lights nothing and 100 % lights
  the whole run, a brightness exists only for a whole strip and is edited in Bindings, and the four panel
  controls are not offered — with the page saying that is the dimension rather than a missing feature, and
  that a layout stored for a strip is kept and changes nothing. **`side-by-side` is built and drawn**, and the
  page says quietly that only a job in progress composes, so a layout's look needs a print.
- The Devices screen is one card per light and nothing about printers beyond which segment one sits on. A card
  carries the hardware facts — the name the light calls itself and its firmware from its last snapshot, the
  shape and pixel count from the frame the module is drawing, and its segment count; the light's own segments
  with all four bounds, read-only; its own presets, read-only, with an unnamed slot shown as unnamed and a
  light that could not be read saying why; the four proved tests as presses that say before they fire that
  they paint the real light for a minute; an `identify` that is disabled until the screen is wired to the
  route below; snapshots with a take-now button, what each file cannot give back, and a restore behind a
  warning that names the destruction, states that it has **never been run against a real light**, and shows
  the sections written and the sections withheld; and a paragraph where `Replace this light…` would be, saying
  there is no replace route and what the swap would have to check. A light that is not answering has every
  test and its restore switched off, with the reason on the card.
- `GET /devices/{name}/presets` answers the presets the light itself holds, by slot and by name: the list a
  moment's `which` is picked from and the read-only list a light's card shows, read from the controller's
  own `/presets.json` when the route is called. `presets_known` says whether the light answered — a light
  that is unplugged answers `200` with an empty list and `presets_unread` saying why, rather than failing
  the page or offering an empty list as though the light held nothing — and a light holding none says so in
  words. Slot 0 is not listed: it is WLED's own, and both lights answer with an empty object there. A slot
  with no name is listed with an empty name and `unnamed: true`, because it is still a preset the light
  holds. No preset's body is read out: segments, effects and palettes stay in WLED's own interface.
- `POST /devices/{name}/identify` flashes a light so that a person can see which light a name means: on,
  then off, one second in each state, five times, and then the light is put back exactly as it was found —
  the power it was in, its brightness and every segment it was drawing — with the picture this module was
  showing painted again the moment it is back rather than at the next heartbeat. The flash is handed to the
  writer that owns the light rather than written beside it, so the steady frame waits for the flash instead
  of being painted over it. Ten seconds of flashing does not fit the host's ten-second call, so the answer
  says what is running rather than holding the call open for it: `202` carries how many flashes, how long
  each state lasts, the power the light was found in and when the flash ends, and the module's log carries
  the line when the light is back. A light this module drives one borrowed segment of is refused (`409
  IDENTIFY_BORROWED_SEGMENT`) — power on a WLED controller belongs to the whole strip, so a flash there
  would take the owner's segments with it — and so is a paused light (`409 IDENTIFY_PAUSED`, naming when
  the pause ends). A light that does not answer is a `503 IDENTIFY_UNREACHABLE` with nothing written,
  rather than a call the host gives up on.

### Fixed

- A state's word now hands the light back to its steady picture in the same pass its hold runs out. Before
  this, a light carrying a word set on a state that was already showing could go **black** the moment the
  hold expired — the panel's own establishing body, with the word's name still on the segment — and stay
  black until the module's once-a-minute repaint came round: measured on the operator's 256 × 64 panel
  three times, on the stroke once and 13 s and 15 s late twice. The picture now lands 0.07 s and 0.29 s
  after the stroke, with the segment's name cleared in the same body.
- A word set on a state that is already being shown is now shown for its full length, counted from the
  write instead of from the moment the state began. Before this, `PUT /states/FINISHED` with
  `{"word":"SWEEP","holds":"60s"}` answered `200` and changed nothing on a light: the printer had finished
  hours earlier, so the sixty seconds had run out before the word existed. A word still written before its
  state begins holds from the entry, which is what it always did, and a word whose hold has run out is now
  reported as `expired` in the answer rather than as an ordinary success.
- The Bindings tab offers a printer that is already shown on another light, and says which light it is on
  beside the name — `Hermes MK4 — already on 10.0.0.136` — so a second light can be given to a printer from
  the screen and not only by `POST /bindings`. A printer already shown on the light being edited is still
  not offered there, the segments that light has taken are listed under the segment field, and a segment
  that is already taken — or a whole-light binding where a segment is taken, and a segment where the light
  is taken whole — is said before Save rather than only in the refusal after it. Each row names its light
  beside the address, and a printer shown by more than one binding says so under its name, so two rows of
  one printer read as two lights rather than as a duplicate.
- A binding added, moved or deleted through `POST /bindings`, `PUT /bindings/{id}` or
  `DELETE /bindings/{id}` is now written into the module's own configuration document, in the same write the
  colour, moment, layout, brightness-window and pause tables use. Before this it changed only what the
  running process was driving: the answer said `applied: true` with `persisted: false`, and the binding was
  gone at the next restart or settings save — which is how a strip bound an hour earlier disappeared and
  left the panel alone. A replace keeps the binding's `id`, so a binding moved from one segment to another
  is on the new segment after a restart under the name it already had.
- A change to a binding that cannot be kept is refused rather than applied and lost. If the module was
  granted no data directory, if the directory will not take the write, or if the document was written by a
  newer release, the route answers `503 STORE_UNAVAILABLE`, the set that was running is still running, and
  nothing is reported as applied.

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
- `POST /preview-pattern` draws one diagnostic picture instead of the word — `solid`, `border` or
  `quarters` — so that one look at the panel separates the three faults a blank panel hides: a dead
  transport, a wrong LED map, and a panel upside down or mirrored. It goes out on the same writer, the
  same establishing body, the same once-a-second repeat over DDP and the same hand-back as the word. It
  answers `202` with the picture, the controller, the question the picture answers and how long it holds,
  and refuses — with a registered code — for a name it does not draw (`PREVIEW_PATTERN_UNKNOWN`, whose
  sentence lists the three) and for the three cases the word's route refuses.
- Both preview routes take `hold`, a whole number of seconds: absent it is the event's own 60, and above
  300 the request is refused (`PREVIEW_HOLD_OUT_OF_RANGE`) rather than clamped, because a hold quietly
  shortened is a look the caller was told he had. Three minutes is what the ladder needs — time to walk to
  the panel and read it.
- The setting `layout` arranges a panel: `{"bar": true, "bar_band": "bottom", "bar_rows": 12, "percent":
  true, "name": true, "remaining": false, "rotate_seconds": 15}`. That is the default and an instance that
  never writes the box runs on it. The bar takes a band at the top or the bottom, 1 to 48 rows thick,
  filled to the job's own fraction of the width; the percentage is drawn in digits at the largest scale the
  band allows and never larger than the word `FINISHED`; the job's name takes the other half of the
  rotation. A name longer than the panel holds — every Prusa file name — is upper-cased, has every
  character this module's font does not hold turned into a space, and is cut at the last whole word that
  fits, which is 21 characters at a 256-wide panel: `MK4IS_0.4n_0.2mm_PLA_MK4IS_1h52m.gcode` draws as
  `MK4IS 0.4N 0.2MM PLA`. It is not scrolled and not abbreviated. A key the module does not read, a band it
  does not know, a `bar_rows` outside 1 to 48, a rotation of no seconds, or `remaining: true` refuses the
  start with the field and the value in the sentence; a bar thicker than the panel it is drawn on is
  refused the same way rather than cropped. Every part can be switched off, and a layout with all three
  off draws nothing of its own: the panel takes the plain frame it had before, over JSON, which is the
  opt-out.
- `POST /preview-frame?percent=41&name=MK4IS&hold=120` draws that picture now, with the values given, so
  the composed layout can be looked at without a print. It goes through the same writer, the same
  establishing body, the same once-a-second repeat and the same hand-back as the other two previews, and it
  draws from the same function a printing panel draws from, so the two cannot drift. `percent` is required
  and is a whole number 0 to 100; `name` is optional; `hold` behaves as it does elsewhere. It answers `202`
  with the progress, the name as the panel will actually draw it, both halves of the rotation in order and
  the layout in force, and refuses with `PREVIEW_FRAME_INVALID` for a progress it cannot read.
- `GET /status` now carries `percent` — the job's progress in whole percent, or `-1` when there is no
  percentage the module may present as current — and `name`, the printer's own name for the job.
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
  `GET /printers` and `GET /controllers` report rather than as ids typed in. It is additive: the settings
  are still the whole configuration, and a module with no bundle loaded — the bundle refused, its
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

- **A printing panel now shows a bar, the percentage and the job's name.** Until now a printing job filled
  the whole panel green with a bar growing across it. It is now the composed picture the `layout` setting
  describes: a bar in a band at the bottom twelve rows thick, the digits of the progress, and the job's own
  name on the other half of a fifteen-second rotation — all in the state colour on the panel's own
  background. It goes out as pixels over DDP rather than as segment geometry, because text on this panel
  cannot be geometry, and it is pushed again once a second for as long as the print runs: 35 datagrams and
  about 50 kB a second, which is what a picture held in realtime costs. The controller is released — back
  to its own segments, with a read that proves it — when the print ends, when the reading goes stale, when
  the printer disconnects, or when the module cannot reach it. Every other picture is unchanged: idle,
  finished, stopped and an error are still the state colour filling the panel, over one JSON request, and a
  strip is untouched, because a strip has no second dimension to arrange anything in.
- **A finished light stays finished when the printer goes quiet.** Until now a printer that stopped
  reporting was painted as a light that had lost contact — dim red — whatever it had last said, so a
  finished print whose bed was still cooling moved a value every minute or two, went stale again 30 s
  later, and the light flapped between the finished green and the stale red all night. Silence now only
  downgrades a job that can still be in flight: `FINISHED` keeps its green and `STOPPED` its grey through
  a silence, and a print that is running, paused or waiting on a person still turns the light dim red.
  `GET /status` keeps telling the truth either way — `"stale": true` and the real `age_seconds` — and the
  log says a finished printer went stale once, rather than once per dip.
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
- **No layout composer.** The arrangement is seven values — which parts are drawn, which end the bar is at,
  how thick it is, how long the rotation holds — and not a free arrangement of regions with something
  filling each. There is no editor for it: the box is JSON in the settings pane, it is one per instance and
  not one per binding, and the parts are fixed even though their sizes and positions are not.
- **No screen beyond the bindings tab.** The bundle is that one tab: no page of its own in the sidebar, no
  section on a printer's card, and no animation editor. The printer and the segment are still written by
  hand in `bindings` when nobody wants to open the tab, and that remains the only durable shape.
- **No saving through the API.** A binding changed through the routes lives until the process restarts.
  The durable shape is the `bindings` setting.
