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
  segments: number[];
  printers: string[];
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

const operation = (
  id: string,
  method: Operation['method'],
  path: string,
  response: Operation['response'],
  body?: string,
): Operation => ({ id, method, path, response, anonymous: false, ...(body ? { body } : {}) });

/** The routes this screen drives, under one namespace. */
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
    readStatus: operation('read-status', 'GET', `${base}/status`, 'json'),
  };
};

/** What a caller sends: the generated clients narrow this; the transport works at this width. */
export interface Call extends OperationRequest {}

/**
 * The screen's whole surface on the module. Every call carries the host's
 * signal, so a person who signs out stops the requests they started.
 */
export const moduleApi = (host: ModuleHost) => {
  const namespace = namespaceOf(host);
  const held = routes(namespace);
  const transport = host.client.transport;

  return {
    namespace,

    listBindings: () =>
      transport.call<{ bindings: Binding[]; count: number }>(held.listBindings, {}, host.signal),

    addBinding: (body: BindingRequest) =>
      transport.call<{ binding: Binding; persisted: boolean }>(held.addBinding, { body }, host.signal),

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
      transport.call<{ controllers: Controller[]; count: number }>(held.listControllers, {}, host.signal),

    readStatus: () => transport.call<StatusBody>(held.readStatus, {}, host.signal),
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
