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
 * What the status route answers. Several bindings answer as a list; one binding
 * answers as the single frame this route has always carried, at the top level.
 */
export interface StatusBody {
  bindings?: StatusEntry[];
  controller?: string;
  segment?: number;
  last_send?: LastSend | null;
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

/** What PUT and DELETE on one state answer: the row now in force, and whether it was kept. */
export interface StateAnswer {
  state: StateRow;
  applied?: boolean;
  /** DELETE: false when the state was already at its default, which is not an error. */
  removed?: boolean;
  persisted: boolean;
  note?: string;
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

    readPause: operation('read-pause', 'GET', `${base}/pause`, 'json'),
    pauseEverything: operation('pause-everything', 'PUT', `${base}/pause`, 'json', 'application/json'),
    resumeEverything: operation('resume-everything', 'DELETE', `${base}/pause`, 'json'),
    pauseBinding: operation('pause-binding', 'PUT', `${base}/bindings/{id}/pause`, 'json', 'application/json'),
    resumeBinding: operation('resume-binding', 'DELETE', `${base}/bindings/{id}/pause`, 'json'),
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