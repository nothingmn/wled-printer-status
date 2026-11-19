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
