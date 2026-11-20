// The harness page: one of the module's own screens, against a stub transport.
//
// Which screen is the hash, so a driver can open `#layout`, `#events` or `#diagnostics` and be looking
// at the real component with nothing between it and the assertions. The host is held before anything
// mounts, exactly as `index.ts` does it, because a screen reads `heldHost()` at construction.
import { mount } from 'svelte';

// The host page's own theme, which the module is drawn inside: every spacing utility in the module
// is a multiple of the host's `--density`, so a harness that did not load this would collapse every
// `w-…` and `p-…` to nothing and assert against a page no operator has ever seen.
import '@prusa/connect-ui/tokens.css';
import '../src/styles.css';
import Diagnostics from '../src/diagnostics.svelte';
import Events from '../src/events.svelte';
import { holdHost } from '../src/host';
import Layout from '../src/layout.svelte';

import { calls, stubHost, written } from './host-fake';

const screen = (window.location.hash.replace('#', '') || 'layout').trim();

holdHost(stubHost());

// The driver reads these rather than the network: what the screen *wrote* is the thing a stub can
// prove and a screenshot cannot.
(window as unknown as Record<string, unknown>).__harness = { calls, written, screen };

const App = screen === 'events' ? Events : screen === 'diagnostics' ? Diagnostics : Layout;

mount(App, { target: document.getElementById('app') as HTMLElement });
