// The screens this module's one tab holds.
//
// docs/product-requirements.md §2 names eight screens and §3 decides the tab rail
// becomes a top strip on a narrow screen, so navigation is the module's own and is
// built once, here. The host gives a module a tab, not a router: adding a screen is
// this file and nothing else -- one import, one line in the list -- and the shell
// picks up the rail entry, the ordering and the body from it. There is deliberately
// no route, no URL and no navigation state to keep in step with the host.
//
// Only screens that exist are listed. A rail entry for a screen with nothing behind
// it is a link that lies, so the six screens of the next waves join this list as
// they land, in the order §2 lists them.
import type { Component } from 'svelte';

import Bindings from './bindings.svelte';
import Overview from './overview.svelte';

/** One screen: its name on the rail, and the component that draws it. */
export interface Screen {
	/** Stable, and only ever used inside this bundle. */
	id: string;
	/** The key of its name in the module's own catalogue. */
	key: string;
	component: Component;
}

/**
 * Every screen, in the order the requirements list them: Right now first, because it
 * is the one that answers the question the module exists for.
 */
export const SCREENS: Screen[] = [
	{ id: 'right-now', key: 'screen.right_now', component: Overview },
	{ id: 'bindings', key: 'screen.bindings', component: Bindings },
];
