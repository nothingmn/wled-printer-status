// The module's own routes, reached through the host's client.
//
// `host.client` is the app's PrusaConnect in session mode, so the credential is
// the cookie the browser already holds and the core authorizes every request.
// Nothing here calls `fetch`: a bundle has no token and no other way out
// (ADR-20260911-4p6 rule 18).
//
// The paths are absolute because that is what the transport builds a URL from,
// and the namespace in them is the one this bundle was installed under. The
// manifest names no namespace -- the host derives it from the module path the
// operator installed -- so it is read off `host.pageHref`, which is the host
// writing it: `/m/<namespace>/<path>`.
import type { Operation, OperationRequest } from '@prusa/connect';
import type { ModuleHost } from '@prusa/connect-ui/slots';

/**
 * One printer, shown on one segment of one controller. `Binding` in openapi.json.
 *
 * Since one printer may sit on several lights (docs/product-requirements.md §2.2),
 * this is no longer the same thing as one printer: a printer shown twice is two of
 * these, with two names, and `light` is what tells them apart.
 */
export interface Binding {
  /**
   * The binding's name on these routes: the host's device id for its printer while
   * that printer is shown once, and `<device id>@<light>:<segment>` — or `:whole` —
   * once the same printer is shown again. It is minted once and never moves, so a
   * path a screen already holds keeps working.
   */
  id: string;
  /** Where it sits in the operator's order, counting from one. */
  position: number;
  /** The host's device id for the printer being watched. */
  printer: string;
  /** The name an operator gave the machine. Absent when the host could not be read. */
  printer_name?: string;
  /**
   * The controller's name on these routes: its address without the scheme, such as
   * `10.0.0.253`. It is the noun GET /controllers names and the one a screen
   * compares when it asks whether a light already shows a printer.
   */
  light: string;
  /** The controller as the operator wrote it, a base URL with no path. */
  controller: string;
  /** The segment this binding drives, and -1 for the whole controller. */
  segment: number;
  /** True when it drives the whole controller, which is what segment -1 says. */
  whole: boolean;
  /** Written once at start, 0-255, and absent on a borrowed segment. */
  brightness?: number;
}

/** One binding as a caller writes it: the same keys one line of the setting carries. */
export interface BindingRequest {
  printer: string;
  controller: string;
  segment?: number;
  brightness?: number;
}

/** One printer the host holds. */
export interface Printer {
  id: string;
  name: string;
  model?: string;
  connected: boolean;
  /**
   * The first binding that shows it, absent when none does. It speaks for the first
   * of several: a printer on two lights answers with the first, which is why a screen
   * asking "is it on *this* light" reads GET /bindings and not this.
   */
  bound?: { id: string; light: string; controller: string; segment: number; whole: boolean };
  /**
   * How many bindings show this printer: 0 for none, 1 for the ordinary case, and 2
   * or more for a printer in more than one room.
   */
  bound_count: number;
}

/** One controller the running set drives, and the bindings on it. */
export interface Controller {
  name: string;
  address: string;
  key: string;
  whole?: string;
  /** The segments the bindings on this controller borrow, never the light's own list. */
  segments: number[];
  printers: string[];
}

/**
 * One segment of a light, as the controller reports it on GET /controllers/{name}.
 * The four bounds keep the controller's own spelling.
 */
export interface Segment {
  id: number;
  /** The segment's own name, as it was given in WLED's own interface. Empty when unnamed. */
  name: string;
  start: number;
  stop: number;
  startY: number;
  stopY: number;
}

/**
 * One light this module knows, as GET /lights answers with it: its address, and what it said about
 * itself the last time it was read.
 *
 * It exists whether or not a binding sits on it, which is the whole of docs/product-requirements.md
 * §2.6's "one card per light": a light with no printer on it is still a card, and a card per
 * binding wearing a light's name is what this replaced.
 */
export interface KnownLight {
  /** Its name on the routes: the address without the scheme, as GET /controllers names one. */
  name: string;
  /** The base URL the operator wrote, with a scheme. */
  address: string;
  /** The name the light calls itself, absent until it has answered here. */
  calls_itself?: string;
  /** The firmware it reports. */
  firmware?: string;
  /** How many LEDs it drives. */
  pixels: number;
  /** Which shape it is, absent until it has answered here. */
  kind?: 'panel' | 'strip';
  /** The panel's geometry in pixels; zero on a strip. */
  width?: number;
  height?: number;
  /** When those facts were read. Absent means nobody has heard from it yet. */
  probed_at?: string;
  /** True when the facts above are the light's own answer. */
  facts_known: boolean;
  /**
   * This module's own decision about the light, and not the light's: an ignored light is kept
   * here and left out of every list that offers a light to act on. It is on this answer rather
   * than off the list, because the Lights screen's Ignored bucket is drawn from this list — a
   * light that vanished from it could not be brought back.
   */
  ignored: boolean;
  /** What the card must not leave a reader to assume, when there is something. */
  note?: string;
}

/**
 * One of the five declared light addresses, as GET /lights answers with it.
 *
 * The slots are the manifest's `options` block — `light1_address` to `light5_address` — and the
 * only addresses this module may contact. An empty slot is filled in the extension's own settings
 * and nowhere else, which is why the screen draws it rather than offering a way to add one.
 */
export interface LightSlot {
  /** The slot's number, counting from one: `light1_address` is slot 1. */
  slot: number;
  /** Whether the option names an address. */
  filled: boolean;
  /** The address as this module dials it, absent on an empty slot. */
  address?: string;
  calls_itself?: string;
  firmware?: string;
  pixels?: number;
  kind?: 'panel' | 'strip';
  width?: number;
  height?: number;
  probed_at?: string;
  facts_known: boolean;
  /** What the slot must not leave a reader to assume: where an empty one is filled in, or that a
   * filled one has not answered yet. */
  note?: string;
}

/** GET /lights: the five declared slots, and the lights that fill them. */
export interface LightsBody {
  slots: LightSlot[];
  slot_count: number;
  lights: KnownLight[];
  count: number;
  persisted: boolean;
  /**
   * The sentence about where addresses live: the five options in the extension's settings, and —
   * when no slot is filled — the one-time migration from 0.8.0, whose addresses are in the store
   * but no longer in any option.
   */
  note: string;
}

/** GET /controllers/{name}: the light's own segments, which every picker is built from. */
export interface ControllerDetail {
  controller: Controller;
  /** True when the controller answered and `segments` is its own list. */
  segments_known: boolean;
  segments: Segment[];
  /** Why the light could not be read, when it could not. */
  segments_unread?: string;
  note?: string;
}

/** What the last write to a controller did. */
export interface LastSend {
  at: string;
  ok: boolean;
  error?: string;
}

/**
 * One binding's answer on the status route. `controller` and `segment` together
 * name the binding, which is how a frame is matched back to its row: a segment is
 * claimed by one binding only, so the pair is unique even when one printer sits on
 * several lights.
 */
export interface StatusEntry {
  controller: string;
  segment: number;
  last_send: LastSend | null;
}

/**
 * What the process answering is, as GET /status publishes it: the release it reported to the host at
 * start, the wire contract it speaks, and the store schema it writes.
 *
 * It is read from the route and never from this bundle, because a version built into a screen is a
 * claim about a binary the screen is not running. A screen that cannot read it says nothing.
 */
export interface BuildFacts {
  /** The release this process reported to the host at start, as MAJOR.MINOR.PATCH. */
  version: string;
  /** The wire major this build speaks. */
  contract: number;
  /** The schema version of the configuration document this build writes. */
  store_schema: number;
  /**
   * Where this module's source lives. It is on this answer rather than in this bundle, so the one
   * address a screen links to is the process's own — a bundle that carried it would go on naming an
   * address after the project moved.
   */
  repository: string;
}

/**
 * What the status route answers. Several bindings answer as a list; one binding
 * answers as the single frame this route has always carried, at the top level.
 */
export interface StatusBody {
  bindings?: StatusEntry[];
  controller?: string;
  segment?: number;
  last_send?: LastSend | null;
  /** What the process answering is. Absent only on an older build of this module. */
  build?: BuildFacts;
}

/** One colour, one byte per channel, as a frame carries it. */
export interface FrameColour {
  r: number;
  g: number;
  b: number;
}

/** The frame this module decided: colour, fill, total, percent, name and reason. */
export interface FrameDecision {
  colour: FrameColour;
  /** How many LEDs, counted from the first, carry the completed part of the job. */
  lit: number;
  total: number;
  /** The completed part in whole percent, and -1 when there is none to present. */
  percent: number;
  /** The printer's own name for the job on the bed, empty when there is none. */
  name: string;
  /** The rule that decided the frame. */
  reason: string;
}

/** How old the newest fact about the printer is, at the moment the frame was drawn. */
export interface FrameReading {
  last_change: string;
  observed_at: string;
  age_seconds: number;
  stale: boolean;
}

/** The PNG beside a frame: the module's own drawing, base64-encoded. */
export interface FramePicture {
  media_type: string;
  width: number;
  height: number;
  bytes: number;
  /** Empty when the frame could not be drawn; `note` on the frame says why. */
  base64: string;
  cached: boolean;
}

/**
 * One binding's picture, from GET /frames. §2.1 asks for a thumbnail per row and
 * decides it is a reconstruction from the frame the module decided, not a
 * screenshot -- this is the module's own PNG of that reconstruction, so no screen
 * redraws it.
 */
export interface Frame {
  /** The binding's id on these routes, which is what its pause is written by. */
  binding: string;
  printer: { id: string; name: string; connected: boolean; retired: boolean; state: string; state_known: boolean };
  light: { name: string; address: string; segment: number };
  kind: 'panel' | 'strip';
  /** The picture's width in pixels: the light's own, or the borrowed run's. */
  width: number;
  height: number;
  frame: FrameDecision;
  reading: FrameReading;
  last_send: LastSend | null;
  /** Absent when the frame could not be drawn; `note` on the frame says why. */
  image?: FramePicture;
  /** What a person has to know to read the picture honestly. */
  note?: string;
}

/** GET /frames: one picture per binding, and the sentence saying what a picture is. */
export interface FramesBody {
  frames: Frame[];
  count: number;
  note: string;
}

/** The six lengths the panic button offers, refused by name rather than rounded. */
export type PauseLength = '10m' | '1h' | '6h' | '1d' | '3d' | 'forever';

/** How long a pause lasts: the body PUT /pause and PUT /bindings/{id}/pause take. */
export interface PauseRequest {
  for: PauseLength;
}

/** One pause, from GET /pause. */
export interface PauseRow {
  scope: 'global' | 'binding';
  /** The binding a binding-scoped pause is about. */
  printer?: string;
  light?: string;
  segment?: number;
  /** Whether the pause is in force now, computed from the clock at read time. */
  on: boolean;
  /** True when the row is still stored and its instant has passed. */
  expired: boolean;
  why: 'global' | 'binding';
  /** The absolute instant it ends at, absent for forever. */
  until?: string;
  forever: boolean;
  remaining_seconds?: number;
  chosen: PauseLength;
  set_at: string;
  effect?: 'off' | 'hands-back';
}

/** What one pause does to one binding, which is where a borrowed zone is visible. */
export interface PausedBinding {
  printer: string;
  light: string;
  segment: number;
  whole: boolean;
  /** True when this binding drives one zone of a light the module does not own outright. */
  borrowed: boolean;
  paused: boolean;
  due_to?: 'global' | 'binding';
  /** `off` for a whole light, `hands-back` for a borrowed zone. */
  effect: 'off' | 'hands-back';
  until?: string;
  forever?: boolean;
}

/**
 * One binding's answer to a refresh: the light, the segment of it this module owns, and what the
 * write did. `RefreshLight` in openapi.json.
 *
 * It is one entry per binding because a binding is one segment of one light, which is the whole of
 * the scope — other segments of the same controller are the operator's own WLED setup and were not
 * touched. `light` is the controller's route name (its address without the scheme), which is what
 * matches it to GET /lights and so to the name the light calls itself.
 */
export interface RefreshEntry {
  /** The binding's id on these routes. */
  binding: string;
  /** The host's device id for the printer it watches. */
  printer?: string;
  /** The controller's name on these routes: its address without the scheme. */
  light: string;
  /** The base URL the operator wrote, which is where the light's own page is. */
  controller: string;
  /** The segment this binding drives, and -1 for the whole light. */
  segment: number;
  /** True when it drives the whole light. */
  whole: boolean;
  /**
   * What the sweep did: the controller took the write, the light was left alone (inside a moment,
   * holding a state's word, being flashed, or paused), the write failed, or the sweep's own time
   * ran out before this light.
   */
  outcome: 'refreshed' | 'skipped' | 'failed' | 'not-reached';
  /** On a refreshed binding: a JSON segment body, or pixels this module drew over DDP. */
  wrote?: 'segments' | 'pixels';
  /** The sentence behind skipped, failed and not reached, and absent on a refreshed one. */
  why?: string;
}

/**
 * POST /refresh: the whole sweep, one row per binding.
 *
 * There is no single "done" in this answer and that is deliberate: a person presses this because a
 * light is showing something stale, and the useful answer is which lights took the picture and which
 * did not. `count` is zero on an install driving nothing, which is the case the note explains.
 */
export interface RefreshBody {
  /** The instant the sweep began, RFC 3339 in UTC. */
  at: string;
  lights: RefreshEntry[];
  count: number;
  refreshed: number;
  skipped: number;
  failed: number;
  note: string;
}

/** GET /pause: every pause there is, and every binding's own fate. */
export interface PauseBody {
  now: string;
  paused: boolean;
  why?: 'global' | 'binding';
  until?: string;
  forever?: boolean;
  /** The printers whose own pause is in force, when no global pause is. */
  paused_by?: string[];
  pauses: PauseRow[];
  bindings: PausedBinding[];
  persisted: boolean;
  note?: string;
}

/**
 * What a moment can show — docs/product-requirements.md §2.3 in the screen's own words, and
 * momenttable.go's four constants. Anything else is refused by the route by name.
 */
export type Shows = 'Preset' | 'Word' | 'Colour' | 'Nothing';

/**
 * What one moment does on one printer on one light: whether it is on, what it shows, which of
 * those it shows, for how long — and where that answer came from.
 *
 * The moment belongs to the light and this entry to the (light, printer) pair, which is what
 * makes the same printer show one thing in the office and another in the workshop. One printer
 * on a light is one entry per moment, which is the screen's flat table.
 */
export interface MomentEntry {
  /** The host's device id for the printer this entry is about. */
  printer: string;
  /** The name an operator gave the machine, absent when the host could not be read. */
  printer_name?: string;
  /** The segment that printer is on, on this light, and -1 for the whole light. */
  segment: number;
  /** True when that printer drives the whole light. */
  whole: boolean;
  /** Whether the moment is shown at all. */
  enabled: boolean;
  shows: Shows;
  /**
   * Which one of those: a preset's name as WLED spells it, the text to draw for a Word, a
   * state's name for a Colour, and empty for Nothing. It is meaningful only for its own
   * `shows`, and the route refuses a mismatch rather than coercing one.
   */
  which: string;
  /** `forever` — until the light's own state changes — or a duration such as `60s` or `5m`. */
  for: string;
  /**
   * `store` when somebody edited this entry, `default` when it is what a delete would put
   * back: for the finished moment on, showing this build's word for a minute; for every other
   * moment, off and showing nothing.
   */
  origin: 'store' | 'default';
}

/** One moment a light has, and what each printer on it shows for it. */
export interface Moment {
  /** print-finished, print-started, filament-change, paused, error-or-attention, light-lost,
   * print-cancelled or printer-went-quiet. */
  moment: string;
  /** The words the approved Events screen prints for it, such as "Print finished". */
  label: string;
  /** What the moment is, in one line. */
  meaning: string;
  /** One entry per printer on the light, in the operator's order. */
  entries: MomentEntry[];
  /** How many printers this moment carries an entry for, which is how the screen indents. */
  printer_count: number;
}

/** GET /lights/{light}/moments: the Events screen's whole table for one light. */
export interface MomentsBody {
  light: string;
  address: string;
  /** Every printer on this light, in the operator's order, in the shape GET /bindings carries. */
  printers: Binding[];
  moments: Moment[];
  count: number;
  /** False when this module has no store to keep a change in; `note` says why. */
  persisted: boolean;
  note?: string;
  /** Stored rows about moments this build does not have, as light/printer/moment. */
  unknown_moments?: string[];
}

/**
 * One entry as a caller writes it. `enabled` and `shows` are required, because a body that
 * forgot them would switch a moment off in silence.
 */
export interface MomentRequest {
  /** Required when the light carries more than one printer; the route refuses a guess. */
  printer?: string;
  enabled: boolean;
  shows: Shows;
  which?: string;
  for?: string;
}

/** What PUT and DELETE on one moment answer: the entry now in force, and whether it was kept. */
export interface MomentAnswer {
  light: string;
  moment: MomentEntry;
  /** PUT: true when the entry is the one in force. */
  applied?: boolean;
  /** DELETE: false when there was nothing stored, which is not an error. */
  removed?: boolean;
  persisted: boolean;
  note?: string;
}

/** How long a state's word holds before the state's own picture comes back. */
export type Holds = 'forever' | '30s' | '60s' | '5m';

/** Where one value of a state's row came from: the store, or Appendix A's own answer. */
export type Origin = 'store' | 'default';

/** What a state's word is doing at the moment the answer was written. */
export type WordShows = 'none' | 'showing' | 'expired' | 'later';

/**
 * One row of the state table: a state, the colour it is drawn in, the word that replaces the
 * state's own picture while it holds, and where each of the three came from.
 *
 * `kind` separates the ten states the host reports from the two conditions this module computes
 * for itself, which is why a twelve-row answer has `conditions: 2`.
 */
export interface StateRow {
  /** IDLE, BUSY, READY, PRINTING, PAUSED, FINISHED, STOPPED, ATTENTION, ERROR or UNKNOWN, or
   * one of the conditions Disconnected and Stale. */
  state: string;
  kind: 'state' | 'condition';
  colour: FrameColour;
  colour_origin: Origin;
  /** The text the panel draws while the state holds, and empty for no word. */
  word: string;
  word_origin: Origin;
  holds: Holds;
  holds_origin: Origin;
  /**
   * What the word is doing now. `expired` is the one that matters: a word set on a state that
   * has been showing for hours is spent before anybody reads it, and an answer that did not
   * say so looked like a success.
   */
  word_shows: WordShows;
  /** The last instant the word is showing, RFC 3339 in UTC. Empty when nothing counts down. */
  word_until?: string;
  /** True when any of the three came from the store, which is what decides a reset. */
  overridden: boolean;
  /** What the state means, in one line. */
  meaning: string;
}

/** GET /states: the Colours screen's whole table. */
export interface StatesBody {
  states: StateRow[];
  /** How many rows the table holds: twelve, of which ten are states. */
  count: number;
  /** How many of `count` are conditions rather than states the host reports. */
  conditions: number;
  persisted: boolean;
  note?: string;
  unknown_states?: string[];
}

/**
 * One state's row as a caller writes it. At least one of the three is required: a body carrying
 * none of them is refused, because clearing a row is what DELETE is for.
 */
export interface StateRequest {
  colour?: FrameColour;
  word?: string;
  holds?: Holds;
}

/**
 * The zone this module judges a brightness window in and writes a clock in, as GET and PUT /timezone
 * answer. `TimezoneAnswer` in openapi.json.
 *
 * It is the module's own store that holds it, so `stored` is the question a screen has to ask before it
 * captures anything: a browser may fill the field once, on the first load that finds nothing stored, and
 * never again. `origin` says where the value came from — this store, the `timezone` setting the schema-9
 * upgrade migrated it from, or nowhere — and `source` says how a stored one got there.
 */
export interface TimezoneAnswer {
  /** The IANA zone in force, as the loader spells it, and '' when this module names none. */
  timezone: string;
  /** Where the value came from: this module's own store, the setting it was migrated from, or nowhere. */
  origin: 'store' | 'settings' | 'none';
  /** How a stored zone got there. Absent unless origin is 'store', and absent when the row has no source. */
  source?: 'browser' | 'operator' | 'settings';
  /** True when the zone is in this module's own document, which is what forbids a capture. */
  stored: boolean;
  /** True when a change here could be kept. */
  persisted: boolean;
  note?: string;
}

/** Who wrote a zone: the first page load that found none, or a person on this screen. */
export type TimezoneSource = 'browser' | 'operator';

/** What PUT and DELETE on one state answer: the row now in force, and whether it was kept. */
export interface StateAnswer {
  state: StateRow;
  applied?: boolean;
  /** DELETE: false when the state was already at its default, which is not an error. */
  removed?: boolean;
  persisted: boolean;
  note?: string;
}

/**
 * One binding's layout: whether the bar is drawn and in which band, how thick it is, whether the
 * percentage and the job's name are drawn and at what scale, and how the two share the panel.
 *
 * Every value carries the origin it came from — `store` when this binding has a row in the module's
 * configuration store, `default` when it has none — because that is what lets a screen offer a reset
 * on exactly the binding that has something to reset, and what makes a binding nobody laid out read
 * as the built-in default rather than as nothing at all.
 */
export interface Layout {
  /** The binding's id on these routes, which is the path a layout is written by. */
  id: string;
  printer: string;
  light: string;
  segment: number;
  whole: boolean;
  /** Whether the progress bar is drawn. */
  bar: boolean;
  bar_origin: Origin;
  /** Which end of the panel the bar sits at. */
  bar_band: 'top' | 'bottom';
  bar_band_origin: Origin;
  /** How many rows of the panel the bar takes, 1 to 48. */
  bar_rows: number;
  bar_rows_origin: Origin;
  /** Whether the job's progress is drawn in digits. */
  percent: boolean;
  percent_origin: Origin;
  /** The scale the digits are drawn at: the three §2.5 offers. */
  percent_scale: 3 | 4 | 5;
  percent_scale_origin: Origin;
  /** Whether the job's own name is drawn. */
  name: boolean;
  name_origin: Origin;
  /** How the percentage and the name share the panel. */
  rotation: 'take-turns' | 'side-by-side';
  rotation_origin: Origin;
  /** How long each half holds the panel, in seconds. */
  rotate_seconds: number;
  rotate_seconds_origin: Origin;
  /**
   * The seven optional lines a panel can carry under the picture, each with the origin it came
   * from. They are drawn in this order — the operator's own ranking — along the bottom of the
   * text band, at the font's own size, and a line whose reading is absent is not drawn while its
   * rows stay reserved.
   */
  remaining: boolean;
  remaining_origin: Origin;
  /** The clock the job ends at, such as `ENDS 11:12`, in `zone`. */
  ends: boolean;
  ends_origin: Origin;
  /** `NOZ 221/220 BED 60`. */
  temperatures: boolean;
  temperatures_origin: Origin;
  /** How long it has been printing, such as `1H12M`. */
  elapsed: boolean;
  elapsed_origin: Origin;
  /** The filament's short name, such as `PLA`. */
  material: boolean;
  material_origin: Origin;
  /** The machine's own name, such as `HERMES MK4`. */
  printer_name: boolean;
  printer_name_origin: Origin;
  /**
   * How far up the print the nozzle has got, as `Z 8.6MM`. The machine reports a position and no
   * layer count, so this is the Z half of "layer / Z height" and not a layer number.
   */
  layer: boolean;
  layer_origin: Origin;
  /**
   * How the ends line writes its wall time: `24-hour` for `ENDS 18:10`, `12-hour` for `ENDS 6:10 PM`.
   *
   * It is the row's own field rather than a setting, because it is per binding — the operator's
   * words were "a toggle, for 12 vs 24 hour clock, per layout". It governs that one line and nothing
   * else: the remaining and elapsed lines are durations and read the same on either clock.
   */
  clock: '24-hour' | '12-hour';
  clock_origin: Origin;
  /**
   * The IANA zone the ETA line writes its clock in — the `timezone` setting's — and empty when
   * this module has none it can load. With no zone the ETA line is blank: a clock written in the
   * container's own zone would be hours out.
   */
  zone: string;
  /** `store` when anything about this layout came from the store. */
  origin: Origin;
  /** True when any value came from the store, which is what decides whether to offer a reset. */
  overridden: boolean;
  /** Present only when the rotation is side-by-side: what that arrangement draws. */
  rotation_note?: string;
}

/** GET /lights/{light}/layouts: every binding on one light, each with the layout it composes with. */
export interface LayoutsBody {
  light: string;
  layouts: Layout[];
  count: number;
  persisted: boolean;
  /** Bindings the store holds a layout for that this module does not drive. */
  unknown_layouts?: string[];
  note?: string;
}

/** What PUT and DELETE on one binding's layout answer: the layout now in force. */
export interface LayoutAnswer {
  light: string;
  layout: Layout;
  /** PUT: true when the layout is the one written. */
  applied?: boolean;
  /** DELETE: false when there was nothing stored, which is not an error. */
  removed?: boolean;
  persisted: boolean;
  note?: string;
}

/**
 * One layout as a caller writes it. The write replaces the binding's row whole, so a key the body
 * leaves out goes back to its default — which is why every write from a screen sends all eight.
 */
export interface LayoutRequest {
  bar: boolean;
  bar_band: 'top' | 'bottom';
  bar_rows: number;
  percent: boolean;
  percent_scale: 3 | 4 | 5;
  name: boolean;
  /** The seven optional lines, in the order they are drawn. */
  remaining: boolean;
  ends: boolean;
  temperatures: boolean;
  elapsed: boolean;
  material: boolean;
  printer_name: boolean;
  layer: boolean;
  rotation: 'take-turns' | 'side-by-side';
  rotate_seconds: number;
  /**
   * How the ends line writes its wall time. Every write sends it, like every other key, because the
   * route replaces the row whole — a body that left it out would put a binding back to 24-hour while
   * the operator watched the control stay where it was.
   */
  clock: '24-hour' | '12-hour';
}

/**
 * One of a light's own presets: the slot it sits at and the name WLED's own interface shows for it,
 * and nothing else. The body of a preset — its segments, effect, palette, brightness and LED map —
 * never travels here: it belongs to WLED's own interface, which is where a preset is made.
 */
export interface Preset {
  /** The slot the preset is stored in, and the number a state write addresses. Slot 0 is never listed. */
  slot: number;
  /** The name, with surrounding spaces trimmed, because that trim is what a moment's `which` matches. */
  name: string;
  /** True when the slot carries no name; it is still a preset the light holds and can run. */
  unnamed: boolean;
}

/**
 * GET /devices/{name}/presets: the preset table the light itself reports, and whether it answered.
 *
 * A light that could not be read is not refused: `presets_known` is false, the list is empty and
 * `presets_unread` says why — the difference between a light holding no presets and one nobody
 * could ask, which a picker has to keep apart.
 */
export interface PresetsBody {
  device: string;
  address: string;
  presets_known: boolean;
  presets: Preset[];
  count: number;
  presets_unread?: string;
  note: string;
}

/** The light a snapshot was taken from: how this module names it, and how it names itself. */
export interface SnapshotDevice {
  /** The light's name on these routes: its address without the scheme. */
  name: string;
  address: string;
  /** The name the light calls itself, from its own /json/info. */
  controller: string;
  firmware: string;
  kind: 'panel' | 'strip';
  width: number;
  height: number;
  pixels: number;
}

/** Something a snapshot cannot give back, and the reason. */
export interface SnapshotOmission {
  member: string;
  why: string;
}

/** One snapshot of one light: its configuration, its presets and its state, kept as a file. */
export interface Snapshot {
  /** The snapshot's name on these routes, and its file's name: the light and the moment. */
  id: string;
  taken_at: string;
  device: SnapshotDevice;
  /** The file's size on disk. */
  bytes: number;
  /** How many preset slots it defines, not counting slot 0. */
  presets: number;
  /** What this snapshot cannot give back: always `nw.ins`, and the wifi password. */
  omits: SnapshotOmission[];
  /** Always false: a literal hundred-per-cent clone is impossible over the API. */
  complete_clone: boolean;
  note?: string;
}

/** GET /devices/{name}/snapshot: the snapshots this module holds of one light, newest first. */
export interface SnapshotsBody {
  device: string;
  address: string;
  snapshots: Snapshot[];
  count: number;
  /** False when this module was granted no data directory, in which case none can be taken. */
  persisted: boolean;
  /** Files in the snapshot folder that cannot be decoded, reported rather than left out. */
  unreadable?: { id: string; why: string }[];
  note?: string;
}

/** What a restore writes and what it refuses to, worked out from one snapshot before anything is sent. */
export interface RestorePlan {
  /** The sections written, in the order they go out. */
  sections: string[];
  /** The sections of the dump that are not written, each with its reason. */
  withheld: { section: string; why: string }[];
  /** The network section is sent whole except for its credential. */
  nw: { sent: boolean; members_removed: string[]; why: string };
  /** True when the snapshot's preset table is written with the configuration. */
  presets: boolean;
}

/** GET /devices/{name}/snapshot/{id}: one snapshot, and what restoring it would do. */
export interface SnapshotDetailBody {
  device: string;
  snapshot: Snapshot;
  counts: { config_bytes: number; presets_bytes: number; state_bytes: number };
  /** False when this build cannot build a restore body from this file, in which case see `unrestorable`. */
  restorable: boolean;
  unrestorable?: string;
  restore: RestorePlan;
}

/** What a restore measured while it waited for the light to answer again. */
export interface Reboot {
  requested: boolean;
  /** True when at least one poll failed, so the light was seen to go away and come back. */
  observed: boolean;
  polls: number;
  failed_polls: number;
  first_refusal_seconds?: number;
  answered_after_seconds: number;
  outage_seconds?: number;
  waited_seconds: number;
  measured_reference: string[];
  measured_source: string;
  note?: string;
}

/** POST /devices/{name}/restore: what was written, and what the light did. */
export interface RestoreAnswer {
  device: string;
  restored: Snapshot;
  /** The copy of the light as it stood before the write, named so a bad restore can be undone. */
  safety_snapshot: Snapshot;
  restore: RestorePlan;
  reboot: Reboot;
  complete_clone: boolean;
  omits: SnapshotOmission[];
  note?: string;
}

/** One light's answer to a preview: whether it took the picture, and what it did with it. */
export interface PreviewLight {
  controller: string;
  light: string;
  /** Absent when this light took the picture; otherwise the registered code it refused with. */
  refused?: string;
  why?: string;
  name?: string;
  halves?: string[];
  answers?: string;
}

/** POST /preview-pattern: one diagnostic picture, and what each light did with it. */
export interface PatternAnswer {
  pattern: 'solid' | 'border' | 'quarters';
  holds_seconds: number;
  lights: PreviewLight[];
  count: number;
  controller?: string;
  answers?: string;
}

/** POST /preview-finished: the finished word fired early, and what each light did with it. */
export interface FinishedAnswer {
  event: string;
  colour: FrameColour;
  scale: number;
  holds_seconds: number;
  lights: PreviewLight[];
  count: number;
  controller?: string;
}

const operation = (
  id: string,
  method: Operation['method'],
  path: string,
  response: Operation['response'],
  body?: string,
): Operation => ({ id, method, path, response, anonymous: false, ...(body ? { body } : {}) });

/** The routes this module's screens drive, under one namespace. */
const routes = (namespace: string) => {
  const base = `/api/v1/modules/${namespace}`;

  return {
    listBindings: operation('list-bindings', 'GET', `${base}/bindings`, 'json'),
    readBinding: operation('read-binding', 'GET', `${base}/bindings/{id}`, 'json'),
    addBinding: operation('add-binding', 'POST', `${base}/bindings`, 'json', 'application/json'),
    changeBinding: operation('change-binding', 'PUT', `${base}/bindings/{id}`, 'json', 'application/json'),
    removeBinding: operation('remove-binding', 'DELETE', `${base}/bindings/{id}`, 'json'),
    listPrinters: operation('list-printers', 'GET', `${base}/printers`, 'json'),
    listControllers: operation('list-controllers', 'GET', `${base}/controllers`, 'json'),
    readController: operation('read-controller', 'GET', `${base}/controllers/{name}`, 'json'),

    // The five declared light addresses and the lights that fill them. There is no add and no
    // remove: the addresses are the extension's own settings, and a screen that could add a sixth
    // would make the manifest's egress declaration false.
    listLights: operation('list-lights', 'GET', `${base}/lights`, 'json'),

    // Putting one light out of the way, and bringing it back. Two verbs on one noun, the way the
    // rest of this surface works: the light is the thing, and the method is the decision. Ignoring
    // is refused while a binding drives the light; un-ignoring is always allowed.
    ignoreLight: operation('ignore-light', 'POST', `${base}/lights/{name}/ignore`, 'json'),
    unignoreLight: operation('unignore-light', 'DELETE', `${base}/lights/{name}/ignore`, 'json'),
    readStatus: operation('read-status', 'GET', `${base}/status`, 'json'),
    listFrames: operation('list-frames', 'GET', `${base}/frames`, 'json'),

    // The moment table, one light at a time: the light is the noun because a moment belongs to
    // the light while what it shows belongs to the (light, printer) pair. There is no POST — the
    // eight moments are a closed list, so a moment is edited and reset, never added.
    listMoments: operation('list-moments', 'GET', `${base}/lights/{light}/moments`, 'json'),
    changeMoment: operation(
      'change-moment',
      'PUT',
      `${base}/lights/{light}/moments/{moment}`,
      'json',
      'application/json',
    ),
    resetMoment: operation('delete-moment', 'DELETE', `${base}/lights/{light}/moments/{moment}`, 'json'),

    // The state table: one row per state the light can be in, written whole and reset whole.
    listStates: operation('list-states', 'GET', `${base}/states`, 'json'),
    changeState: operation('change-state', 'PUT', `${base}/states/{state}`, 'json', 'application/json'),
    resetState: operation('delete-state', 'DELETE', `${base}/states/{state}`, 'json'),

    // The zone a brightness window is judged in and a clock is written in: one IANA name, kept in this
    // module's own store. The read is what a screen asks before it captures one from the browser, and
    // the write is the only way to change one — there is no DELETE, because this module never forgets a
    // zone once it has one.
    readTimezone: operation('read-timezone', 'GET', `${base}/timezone`, 'json'),
    writeTimezone: operation('write-timezone', 'PUT', `${base}/timezone`, 'json', 'application/json'),

    readPause: operation('read-pause', 'GET', `${base}/pause`, 'json'),
    pauseEverything: operation('pause-everything', 'PUT', `${base}/pause`, 'json', 'application/json'),
    resumeEverything: operation('resume-everything', 'DELETE', `${base}/pause`, 'json'),
    pauseBinding: operation('pause-binding', 'PUT', `${base}/bindings/{id}/pause`, 'json', 'application/json'),
    resumeBinding: operation('resume-binding', 'DELETE', `${base}/bindings/{id}/pause`, 'json'),

    // `Refresh everything`, the header's button beside the pause: every binding the module drives,
    // written from the reading taken as the sweep runs, whether or not the picture changed. It has
    // no body — there is nothing to decide — and it answers per light and per segment.
    refreshEverything: operation('refresh-everything', 'POST', `${base}/refresh`, 'json'),

    // The layout a binding composes with: one row per binding, because a 256x64 panel and a 48-LED
    // strip do not compose the same way and two bindings on one light can be laid out differently.
    // The list is keyed on the light and the write on the binding, which is what the screens need.
    listLayouts: operation('list-layouts', 'GET', `${base}/lights/{light}/layouts`, 'json'),
    changeLayout: operation('change-layout', 'PUT', `${base}/bindings/{id}/layout`, 'json', 'application/json'),
    resetLayout: operation('delete-layout', 'DELETE', `${base}/bindings/{id}/layout`, 'json'),

    // A light's own preset table: names only, read-only, one route that a picker and a card share.
    listPresets: operation('list-presets', 'GET', `${base}/devices/{name}/presets`, 'json'),

    // Snapshots and the one restore. The list has to answer while the light is dead — that is what a
    // snapshot is for — and the read of one carries the table a restore would send, so the page that
    // offers the button is the page that shows it.
    listSnapshots: operation('list-snapshots', 'GET', `${base}/devices/{name}/snapshot`, 'json'),
    takeSnapshot: operation('take-snapshot', 'POST', `${base}/devices/{name}/snapshot`, 'json'),
    readSnapshot: operation('read-snapshot', 'GET', `${base}/devices/{name}/snapshot/{id}`, 'json'),
    restoreSnapshot: operation('restore-snapshot', 'POST', `${base}/devices/{name}/restore`, 'json', 'application/json'),

    // The proved tests. Each paints a real light, so nothing calls them without a press: they are
    // the only instrument this build has, because the firmware cannot be asked what its pixels are.
    previewPattern: operation('preview-pattern', 'POST', `${base}/preview-pattern`, 'json'),
    previewFinished: operation('preview-finished', 'POST', `${base}/preview-finished`, 'json'),
  };
};

/** What a caller sends: the generated clients narrow this; the transport works at this width. */
export interface Call extends OperationRequest {}

/**
 * The screens' whole surface on the module. Every call carries the host's
 * signal, so a person who signs out stops the requests they started.
 */
export const moduleApi = (host: ModuleHost) => {
  const namespace = namespaceOf(host);
  const held = routes(namespace);
  const transport = host.client.transport;

  return {
    namespace,

    listBindings: () =>
      transport.call<{ bindings: Binding[]; count: number; source?: string; persisted?: boolean }>(
        held.listBindings,
        {},
        host.signal,
      ),

    addBinding: (body: BindingRequest) =>
      transport.call<{ binding: Binding; applied?: boolean; persisted: boolean }>(
        held.addBinding,
        { body },
        host.signal,
      ),

    changeBinding: (id: string, body: BindingRequest) =>
      transport.call<{ binding: Binding; applied: boolean; persisted: boolean }>(
        held.changeBinding,
        { path: { id }, body },
        host.signal,
      ),

    removeBinding: (id: string) =>
      transport.call<{ deleted: string; applied: boolean; persisted: boolean }>(
        held.removeBinding,
        { path: { id } },
        host.signal,
      ),

    listPrinters: () =>
      transport.call<{ printers: Printer[]; count: number; total: number }>(
        held.listPrinters,
        {},
        host.signal,
      ),

    listControllers: () =>
      transport.call<{ controllers: Controller[]; count: number; segments_known: boolean }>(
        held.listControllers,
        {},
        host.signal,
      ),

    /** One light, and the segments it reports of itself: the list every picker is built from. */
    readController: (name: string) =>
      transport.call<ControllerDetail>(held.readController, { path: { name } }, host.signal),

    /** Every light this module knows, whether or not a printer is shown on it. */
    listLights: () => transport.call<LightsBody>(held.listLights, {}, host.signal),

    /**
     * Puts one light out of the way: it stays on the list and off every screen that offers a
     * light. Refused while a binding drives it, and the refusal names the bindings.
     */
    ignoreLight: (name: string) =>
      transport.call<{ light: KnownLight; note: string }>(held.ignoreLight, { path: { name } }, host.signal),

    /** Brings an ignored light back everywhere. Always allowed, on any light. */
    unignoreLight: (name: string) =>
      transport.call<{ light: KnownLight; note: string }>(held.unignoreLight, { path: { name } }, host.signal),

    readStatus: () => transport.call<StatusBody>(held.readStatus, {}, host.signal),

    /** One picture per binding, drawn by the module's own renderer. */
    listFrames: () => transport.call<FramesBody>(held.listFrames, {}, host.signal),

    /** One light's whole moment table: every moment, with one entry per printer on it. */
    listMoments: (light: string) =>
      transport.call<MomentsBody>(held.listMoments, { path: { light } }, host.signal),

    /**
     * Writes one (light, printer) entry for one moment. The body replaces the entry whole, so a
     * caller sends all four fields: a field left out goes back to its default, not to what the
     * row had.
     */
    changeMoment: (light: string, moment: string, body: MomentRequest) =>
      transport.call<MomentAnswer>(held.changeMoment, { path: { light, moment }, body }, host.signal),

    /**
     * Puts one printer's entry for one moment back to its default. The printer is a query
     * parameter because the path already carries the light and the moment, and because a DELETE
     * body is the one part of HTTP a proxy may drop.
     */
    resetMoment: (light: string, moment: string, printer: string) =>
      transport.call<MomentAnswer>(held.resetMoment, { path: { light, moment }, query: { printer } }, host.signal),

    /** Every state, with the colour, word and hold in force and where each came from. */
    listStates: () => transport.call<StatesBody>(held.listStates, {}, host.signal),

    /**
     * Writes one state's row, whole. A field the body leaves out goes back to its default — the
     * colour Appendix A gives the state, no word, and a hold of forever — so a caller that means
     * to keep a word sends it with every colour change.
     */
    changeState: (state: string, body: StateRequest) =>
      transport.call<StateAnswer>(held.changeState, { path: { state }, body }, host.signal),

    /** Puts one state back to Appendix A's colour, no word, and a hold of forever. */
    resetState: (state: string) =>
      transport.call<StateAnswer>(held.resetState, { path: { state } }, host.signal),

    /** The zone in force, where it came from, and how a stored one got there. */
    readTimezone: () => transport.call<TimezoneAnswer>(held.readTimezone, {}, host.signal),

    /**
     * Stores one zone. `source` is 'browser' for the first load's capture and 'operator' for a person
     * typing it; an empty or unloadable zone is refused by the route and nothing is written, which is
     * why a screen must show the refusal rather than the value it sent.
     */
    writeTimezone: (timezone: string, source: TimezoneSource = 'operator') =>
      transport.call<TimezoneAnswer>(held.writeTimezone, { body: { timezone, source } }, host.signal),

    readPause: () => transport.call<PauseBody>(held.readPause, {}, host.signal),

    pauseEverything: (length: PauseLength) =>
      transport.call<{ pause: PauseRow; paused: boolean; persisted: boolean; note?: string }>(
        held.pauseEverything,
        { body: { for: length } satisfies PauseRequest },
        host.signal,
      ),

    resumeEverything: () =>
      transport.call<{ scope: 'global'; resumed: boolean; persisted: boolean; note?: string }>(
        held.resumeEverything,
        {},
        host.signal,
      ),

    pauseBinding: (id: string, length: PauseLength) =>
      transport.call<{
        pause: PauseRow;
        paused: boolean;
        effect?: 'off' | 'hands-back';
        persisted: boolean;
        note?: string;
      }>(held.pauseBinding, { path: { id }, body: { for: length } satisfies PauseRequest }, host.signal),

    resumeBinding: (id: string) =>
      transport.call<{ scope: 'binding'; printer?: string; resumed: boolean; persisted: boolean; note?: string }>(
        held.resumeBinding,
        { path: { id } },
        host.signal,
      ),

    /**
     * Writes every binding this module drives, now, from the reading taken as the sweep runs, and
     * answers what each light did with it. A sweep already running is refused by code
     * (REFRESH_BUSY) rather than joined: see the refusal's own sentence.
     */
    refreshEverything: () => transport.call<RefreshBody>(held.refreshEverything, {}, host.signal),

    /** Every binding on one light, each with the layout it composes with and where each value came from. */
    listLayouts: (light: string) =>
      transport.call<LayoutsBody>(held.listLayouts, { path: { light } }, host.signal),

    /**
     * Writes one binding's layout. The body replaces the binding's row whole, so a caller sends every
     * key: a key left out goes back to the built-in default rather than to what the row held.
     */
    changeLayout: (id: string, body: LayoutRequest) =>
      transport.call<LayoutAnswer>(held.changeLayout, { path: { id }, body }, host.signal),

    /** Puts one binding's layout back to the built-in default, which is what a row of nothing means. */
    resetLayout: (id: string) =>
      transport.call<LayoutAnswer>(held.resetLayout, { path: { id } }, host.signal),

    /** The light's own preset table, by slot and name — names only, and never a preset's body. */
    listPresets: (name: string) =>
      transport.call<PresetsBody>(held.listPresets, { path: { name } }, host.signal),

    /** Every snapshot this module holds of one light, newest first. Answers while the light is dead. */
    listSnapshots: (name: string) =>
      transport.call<SnapshotsBody>(held.listSnapshots, { path: { name } }, host.signal),

    /** Reads the light and keeps a copy of it now. A light that does not answer is refused, not stored half-read. */
    takeSnapshot: (name: string) =>
      transport.call<{ device: string; snapshot: Snapshot; note?: string }>(
        held.takeSnapshot,
        { path: { name } },
        host.signal,
      ),

    /** One snapshot, with the table a restore would send and the list it would not. */
    readSnapshot: (name: string, id: string) =>
      transport.call<SnapshotDetailBody>(held.readSnapshot, { path: { name, id } }, host.signal),

    /**
     * Writes a snapshot's configuration back onto its light and waits for it to answer. `confirm` is
     * required by the route and checked before the snapshot is even read: a restore rewrites the
     * light's whole configuration and restarts it.
     */
    restoreSnapshot: (name: string, id: string) =>
      transport.call<RestoreAnswer>(
        held.restoreSnapshot,
        { path: { name }, body: { snapshot: id, confirm: true } },
        host.signal,
      ),

    /**
     * Paints one diagnostic picture on one light, for a hold, and answers what that light did with it.
     * The light is named because a preview without one fires at every light the instance drives.
     */
    previewPattern: (light: string, pattern: 'solid' | 'border' | 'quarters', hold: number) =>
      transport.call<PatternAnswer>(
        held.previewPattern,
        { query: { light, pattern, hold } },
        host.signal,
      ),

    /** Fires the finished moment early on one light, for its own hold. */
    previewFinished: (light: string, hold: number) =>
      transport.call<FinishedAnswer>(held.previewFinished, { query: { light, hold } }, host.signal),
  };
};

/**
 * The namespace this module is installed under, out of the host's own spelling
 * of one of its addresses. A module is not told its namespace and must not
 * choose one (ADR-20260911-4p6 rule 9), so the address is the way to read it.
 */
const namespaceOf = (host: ModuleHost): string => {
  const parts = host.pageHref('bindings').split('/').filter(Boolean);
  const namespace = parts[1];

  if (!namespace) {
    throw new Error(`this module was given no namespace: pageHref answered ${host.pageHref('bindings')}`);
  }

  return namespace;
};