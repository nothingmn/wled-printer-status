// The stub the harness runs against: a fake host, and the module's own routes answered from fixtures.
//
// It is deliberately the *transport* that is faked and nothing above it. The screens, the primitives,
// the catalogue, the layout arithmetic and the write queue are the module's own code, so an assertion
// made against this page is an assertion about what the operator's browser does. The one thing the
// stub replaces is the network, and it records every call it is given so a test can read what the
// screen actually wrote rather than what it looks like it would have written.
import type { ModuleHost } from '@prusa/connect-ui/slots';

import catalogue from '../src/locales/en.json';

const NAMESPACE = 'com.github.aux.nothingmn.wled-printer-status';

/** One call the transport was given: the operation's id, its method and the path it resolved to. */
export interface StubCall {
	id: string;
	method: string;
	path: string;
	body?: unknown;
}

/** Every call made since the page loaded, in order. */
export const calls: StubCall[] = [];

/** The one thing the harness asserts on: the last body PUT to a binding's layout, and its path. */
export const written: { path: string; body: Record<string, unknown> }[] = [];

/**
 * The catalogue, as the host would translate with it: a key, and `{name}` slots filled from the
 * object. An unknown key answers the key itself, which is what the host does and what the
 * no-raw-key assertion is looking for.
 */
const t = (key: string, params?: Record<string, unknown>): string => {
	const said = (catalogue as Record<string, string>)[key];
	if (said === undefined) return key;
	if (params === undefined) return said;

	// The host's catalogue carries ICU plurals (`{count, plural, one {# hour ago} other {# hours
	// ago}}`), and a stub that filled only plain slots would print that markup into the page — which
	// would make the no-raw-message assertion pass for the wrong reason.
	const plural = said.replace(
		/\{(\w+)\s*,\s*plural\s*,\s*one\s*\{([^{}]*)\}\s*other\s*\{([^{}]*)\}\s*\}/g,
		(_whole, name: string, one: string, other: string) => {
			const count = Number(params[name] ?? 0);

			return (count === 1 ? one : other).replace(/#/g, String(count));
		},
	);

	return plural.replace(/\{(\w+)\}/g, (whole, name: string) =>
		name in params ? String(params[name]) : whole,
	);
};

const origin = (stored: boolean): 'store' | 'default' => (stored ? 'store' : 'default');

interface LayoutState {
	binding: string;
	printer: string;
	light: string;
	segment: number;
	bar: boolean;
	bar_band: 'top' | 'bottom';
	bar_rows: number;
	percent: boolean;
	percent_scale: 3 | 4 | 5;
	name: boolean;
	rotation: 'take-turns' | 'side-by-side';
	rotate_seconds: number;
	remaining: boolean;
	ends: boolean;
	temperatures: boolean;
	elapsed: boolean;
	material: boolean;
	printer_name: boolean;
	layer: boolean;
	clock: '24-hour' | '12-hour';
	/** Which keys a person has written, which is what decides each one's origin. */
	stored: Set<string>;
}

const layoutFor = (one: {
	binding: string;
	printer: string;
	light: string;
	segment: number;
	stored?: string[];
	ends?: boolean;
	clock?: '24-hour' | '12-hour';
}): LayoutState => ({
	binding: one.binding,
	printer: one.printer,
	light: one.light,
	segment: one.segment,
	bar: true,
	bar_band: 'bottom',
	bar_rows: 12,
	percent: true,
	percent_scale: 5,
	name: true,
	rotation: 'take-turns',
	rotate_seconds: 15,
	remaining: false,
	ends: one.ends === true,
	temperatures: false,
	elapsed: false,
	material: false,
	printer_name: false,
	layer: false,
	clock: one.clock ?? '24-hour',
	stored: new Set(one.stored ?? []),
});

const layouts = new Map<string, LayoutState>([
	[
		'printer-a',
		layoutFor({ binding: 'printer-a', printer: 'printer-a', light: '10.0.0.136', segment: 0, ends: true }),
	],
	[
		'printer-b',
		layoutFor({
			binding: 'printer-b',
			printer: 'printer-b',
			light: '10.0.0.253',
			segment: -1,
			ends: true,
			stored: ['ends'],
		}),
	],
]);

/** One layout as the routes answer with it: every value, and where it came from. */
const layoutAnswer = (one: LayoutState): Record<string, unknown> => ({
	id: one.binding,
	printer: one.printer,
	light: one.light,
	segment: one.segment,
	whole: one.segment < 0,
	bar: one.bar,
	bar_origin: origin(one.stored.has('bar')),
	bar_band: one.bar_band,
	bar_band_origin: origin(one.stored.has('bar_band')),
	bar_rows: one.bar_rows,
	bar_rows_origin: origin(one.stored.has('bar_rows')),
	percent: one.percent,
	percent_origin: origin(one.stored.has('percent')),
	percent_scale: one.percent_scale,
	percent_scale_origin: origin(one.stored.has('percent_scale')),
	name: one.name,
	name_origin: origin(one.stored.has('name')),
	rotation: one.rotation,
	rotation_origin: origin(one.stored.has('rotation')),
	rotate_seconds: one.rotate_seconds,
	rotate_seconds_origin: origin(one.stored.has('rotate_seconds')),
	remaining: one.remaining,
	remaining_origin: origin(one.stored.has('remaining')),
	ends: one.ends,
	ends_origin: origin(one.stored.has('ends')),
	temperatures: one.temperatures,
	temperatures_origin: origin(one.stored.has('temperatures')),
	elapsed: one.elapsed,
	elapsed_origin: origin(one.stored.has('elapsed')),
	material: one.material,
	material_origin: origin(one.stored.has('material')),
	printer_name: one.printer_name,
	printer_name_origin: origin(one.stored.has('printer_name')),
	layer: one.layer,
	layer_origin: origin(one.stored.has('layer')),
	clock: one.clock,
	clock_origin: origin(one.stored.has('clock')),
	zone: 'America/Denver',
	origin: one.stored.size > 0 ? 'store' : 'default',
	overridden: one.stored.size > 0,
});

const bindings = [
	{
		id: 'printer-a',
		position: 1,
		printer: 'printer-a',
		printer_name: 'Panel printer',
		light: '10.0.0.136',
		controller: 'http://10.0.0.136',
		segment: 0,
		whole: false,
		brightness: 200,
	},
	{
		id: 'printer-b',
		position: 2,
		printer: 'printer-b',
		printer_name: 'Desk printer',
		light: '10.0.0.253',
		controller: 'http://10.0.0.253',
		segment: -1,
		whole: true,
		brightness: 128,
	},
];

const controllers = [
	{
		name: '10.0.0.136',
		address: 'http://10.0.0.136',
		key: '10.0.0.136',
		segments: [0],
		printers: ['printer-a'],
	},
	{
		name: '10.0.0.253',
		address: 'http://10.0.0.253',
		key: '10.0.0.253',
		segments: [],
		printers: ['printer-b'],
	},
];

// Two panels, and the second is the one the clock is measured against: 256 columns hold either
// spelling of a wall time, and 64 columns hold `ENDS 18:10` and not `ENDS 6:10 PM`. That pair is the
// whole reason the fit verdict has a width in it.
const frames = [
	{
		binding: 'printer-a',
		printer: { id: 'printer-a', name: 'Panel printer', connected: true, retired: false, state: 'PRINTING', state_known: true },
		light: { name: '10.0.0.136', address: 'http://10.0.0.136', segment: 0 },
		kind: 'panel',
		width: 256,
		height: 64,
		frame: { colour: { r: 0, g: 200, b: 100 }, lit: 6721, total: 16384, percent: 41, name: 'MK4IS 0.4N 0.2MM PLA', reason: 'progress' },
		reading: { last_change: '2026-11-19T21:00:00Z', observed_at: '2026-11-19T21:00:04Z', age_seconds: 4, stale: false },
		last_send: null,
	},
	{
		binding: 'printer-b',
		printer: { id: 'printer-b', name: 'Desk printer', connected: true, retired: false, state: 'PRINTING', state_known: true },
		light: { name: '10.0.0.253', address: 'http://10.0.0.253', segment: -1 },
		kind: 'panel',
		width: 64,
		height: 64,
		frame: { colour: { r: 0, g: 200, b: 100 }, lit: 8192, total: 16384, percent: 50, name: 'BENCHY', reason: 'progress' },
		reading: { last_change: '2026-11-19T21:00:00Z', observed_at: '2026-11-19T21:00:04Z', age_seconds: 4, stale: false },
		last_send: null,
	},
];

const moments = ['print-finished', 'print-started'].map((moment) => ({
	moment,
	label: moment === 'print-finished' ? 'Print finished' : 'Print started',
	meaning: 'The printer finished the job on its bed.',
	entries: [
		{
			printer: 'printer-a',
			printer_name: 'Panel printer',
			segment: 0,
			whole: false,
			enabled: moment === 'print-finished',
			shows: moment === 'print-finished' ? 'Word' : 'Nothing',
			which: moment === 'print-finished' ? 'FINISHED' : '',
			for: '1m',
			origin: 'default',
		},
	],
	printer_count: 1,
}));

const states = ['IDLE', 'PRINTING', 'FINISHED'].map((state) => ({
	state,
	kind: 'state',
	colour: { r: 0, g: 120, b: 220 },
	colour_origin: 'default',
	word: '',
	word_origin: 'default',
	holds: 'forever',
	holds_origin: 'default',
	word_shows: 'none',
	overridden: false,
	meaning: 'What the printer is doing.',
}));

/** Answer one operation's path from the fixtures. */
const answer = (path: string, method: string, body: unknown): unknown => {
	// The transport is handed the whole path, namespace included: `/api/v1/modules/<namespace>/…`.
	// What the fixtures are keyed on is the route itself.
	const asked = path.replace(/^\/(?:api\/v1\/modules|m)\/[^/]+/, '');

	if (asked === '/bindings') return { bindings, count: bindings.length, source: 'store', persisted: true };
	if (asked === '/controllers') return { controllers, count: controllers.length };
	if (asked === '/frames') return { frames, count: frames.length, note: 'A picture of what each light is showing.' };
	if (asked === '/states') return { states, count: states.length, conditions: 0, persisted: true };
	if (asked === '/timezone') return { timezone: 'America/Denver', origin: 'store', source: 'operator', stored: 'America/Denver', persisted: true };
	if (asked === '/pause') {
		return {
			now: '2026-11-19T21:00:00Z',
			paused: false,
			pauses: [],
			bindings: bindings.map((one) => ({
				printer: one.printer,
				light: one.light,
				segment: one.segment,
				whole: one.whole,
				borrowed: false,
				paused: false,
				effect: one.segment < 0 ? 'off' : 'hands-back',
			})),
			persisted: true,
			note: 'Nothing is paused.',
		};
	}
	if (asked === '/status') {
		return {
			bindings: [],
			build: {
				version: '0.7.0',
				contract: 1,
				store_schema: 10,
				repository: 'https://github.com/nothingmn/wled-printer-status',
			},
		};
	}

	if (asked === '/lights') {
		return {
			lights: controllers.map((one) => ({
				name: one.name,
				address: one.address,
				calls_itself: one.name === '10.0.0.136' ? 'morpheus' : 'benchy',
				firmware: '0.14.4',
				pixels: 16384,
				kind: 'panel',
				width: one.name === '10.0.0.136' ? 256 : 64,
				height: 64,
				probed_at: '2026-11-19T21:00:00Z',
				facts_known: true,
				ignored: false,
			})),
			count: controllers.length,
			persisted: true,
			note: '',
		};
	}

	const controller = asked.match(/^\/controllers\/([^/]+)$/);
	if (controller !== null) {
		const name = decodeURIComponent(controller[1]);

		return {
			controller: controllers.find((one) => one.name === name),
			segments_known: true,
			segments: [{ id: 0, name: 'panel', start: 0, stop: 16384, startY: 0, stopY: 63 }],
		};
	}

	const onLight = asked.match(/^\/lights\/([^/]+)\/layouts$/);
	if (onLight !== null) {
		const light = decodeURIComponent(onLight[1]);
		const on = [...layouts.values()].filter((one) => one.light === light);

		return {
			light,
			layouts: on.map(layoutAnswer),
			count: on.length,
			persisted: true,
			note: 'Whether a layout fits is judged when the frame is drawn.',
		};
	}

	const oneLayout = asked.match(/^\/bindings\/([^/]+)\/layout$/);
	if (oneLayout !== null) {
		const binding = decodeURIComponent(oneLayout[1]);
		const held = layouts.get(binding);
		if (held === undefined) throw new Error(`no fixture for binding ${binding}`);

		if (method === 'PUT') {
			const sent = (body ?? {}) as Record<string, unknown>;
			written.push({ path: asked, body: sent });

			for (const [key, value] of Object.entries(sent)) {
				if (key === 'id' || key === 'binding') continue;
				(held as unknown as Record<string, unknown>)[key] = value;
				held.stored.add(key);
			}

			return {
				light: held.light,
				layout: layoutAnswer(held),
				applied: true,
				persisted: true,
				note: 'Kept in this module’s own store.',
			};
		}

		held.stored.clear();
		held.clock = '24-hour';
		held.ends = true;

		return { light: held.light, layout: layoutAnswer(held), removed: true, persisted: true, note: '' };
	}

	const onMoments = asked.match(/^\/lights\/([^/]+)\/moments$/);
	if (onMoments !== null) {
		const light = decodeURIComponent(onMoments[1]);
		const on = bindings.filter((one) => one.light === light);

		return {
			light,
			address: on[0]?.controller ?? '',
			printers: on,
			moments: moments.map((one) => ({
				...one,
				entries: one.entries.filter((entry) => on.some((held) => held.printer === entry.printer)),
				printer_count: on.length,
			})),
			count: moments.length,
			persisted: true,
		};
	}

	if (asked.startsWith('/devices/')) return { presets: [], count: 0, name: '' };
	if (asked.startsWith('/snapshots')) return { snapshots: [], count: 0 };

	throw new Error(`the harness stub has no answer for ${method} ${asked}`);
};

/** The fake host: the catalogue, a namespace, and a transport that answers from the fixtures. */
export const stubHost = (): ModuleHost => {
	const host = {
		t,
		signal: new AbortController().signal,
		pageHref: (path: string) => `/m/${NAMESPACE}/${path}`,
		client: {
			transport: {
				call: async (
					operation: { id: string; method: string; path: string },
					params?: { body?: unknown; path?: Record<string, string> },
					_signal?: AbortSignal,
				): Promise<unknown> => {
					let path = operation.path;
					for (const [name, value] of Object.entries(params?.path ?? {})) {
						path = path.replace(`{${name}}`, encodeURIComponent(value));
					}

					calls.push({ id: operation.id, method: operation.method, path, body: params?.body });

					return answer(path, operation.method, params?.body);
				},
			},
		},
	};

	return host as unknown as ModuleHost;
};
