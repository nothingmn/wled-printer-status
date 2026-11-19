// Where a printer's own page is, built out of the page this module is drawn on.
//
// A printer's page in Connect is a route in the app's own front end, not a route this
// module serves: `/printers/<id>`, which is what the app's own rows link to (the shell's
// `printerHref`, and `/printers/:id` in the app's route table).
//
// The host in that address is deliberately not written into this repository. A module
// cannot know the address somebody reaches their Connect install at -- one operator's is
// `http://10.0.0.160:8080`, the next is a name behind a reverse proxy, and behind a proxy
// the app may also sit under a path prefix. So the origin is the browser's own, and the
// root is read off the address this bundle was drawn at: the host renders a module's page
// at `<root>/settings/extensions/<namespace>/<tab>`, so everything before that marker is
// the app's root. On this install that gives `http://10.0.0.160:8080/printers/<id>`; on a
// proxied one it gives `https://connect.example.com/connect/printers/<id>` with no change.

/** The path the host draws a module's own page under. Everything before it is the app's root. */
const MODULE_PAGE = '/settings/extensions/';

/** This page's own address, or undefined where there is none (a server render, a test). */
const here = (): Location | undefined => (typeof location === 'undefined' ? undefined : location);

/**
 * The root the Connect app is served from, as a prefix that ends in a slash: `/`, or
 * `/connect/` when the app is mounted under a proxy prefix. Read off this page's own
 * address, and `/` when the address does not name a module page at all.
 */
export const appRoot = (): string => {
  const path = here()?.pathname ?? '';
  const at = path.indexOf(MODULE_PAGE);

  if (at <= 0) return '/';

  return `${path.slice(0, at)}/`;
};

/**
 * One route of the app, as an absolute address when the page knows its own origin and a
 * root-relative one when it does not. A relative address is still a working link; it is
 * simply resolved by the browser instead of here.
 */
export const appUrl = (path: string): string => {
  const trail = path.startsWith('/') ? path.slice(1) : path;

  return `${here()?.origin ?? ''}${appRoot()}${trail}`;
};

/**
 * One printer's page in Prusa Connect. The id is encoded as one path segment, which is
 * what the app's own router expects of it.
 */
export const printerUrl = (id: string): string => appUrl(`printers/${encodeURIComponent(id)}`);
