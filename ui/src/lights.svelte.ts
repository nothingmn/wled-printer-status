// What lights this module drives, what each one carries, and who is on it.
//
// Both screens in this wave need the same three answers -- the lights, each light's
// own segments, and the bindings sitting on them -- and the arithmetic between them
// is the rule §2.2 records as a correction: a whole-light binding covers every
// segment of that light, and any segment binding excludes the whole light. So
// "taken" means *the same segment, or either side is the whole light*, and a light
// carrying one zone correctly reports its whole light as unavailable. That rule is
// written once, here, rather than in each screen that has to grey something out.
//
// The light's own list comes from GET /controllers/{name}, which is the route the
// requirements asked for before any segment picker could be built (§5). A light that
// will not answer is not a failed screen: it is a light with no segments to offer,
// which is why the read is caught here and carried as a fact on the light.
import { moduleApi, type Binding, type Controller, type ControllerDetail, type KnownLight, type Segment } from './api';
import { describe, lightName } from './format';
import { heldHost } from './host';

/** One light, the segments it reports of itself, and the bindings on it. */
export interface LightFacts {
	controller: Controller;
	/** The light's own segments, in its own order. Empty when it holds none. */
	segments: Segment[];
	/** True when the light answered and `segments` is its own list. */
	segmentsKnown: boolean;
	/** Why its segments could not be read, when they could not. */
	segmentsUnread: string;
	/** Every binding on this light, in the operator's order. */
	bindings: Binding[];
	/**
	 * Whether the operator has pushed this light out of the way. It is a decision kept in the
	 * module's own store, and it is a fact about the light and not about a screen: every list that
	 * offers a light to act on leaves these out, and the Lights screen keeps them in their own
	 * bucket so they can be brought back.
	 */
	ignored: boolean;
}

/**
 * Which of the Lights screen's three buckets one light belongs to, in the order the rule is
 * decided — and it is decided in that order for a reason.
 *
 * **In use wins.** A light a binding drives is in use whatever its flag says, so a flag that
 * somehow outlived a binding can never hide a light this module is writing to. **Ignored comes
 * next**, and only for a light with no binding, which is the only kind the route will ignore.
 * Everything else is **available**: known, nothing on it, and offered everywhere.
 */
export type LightBucket = 'in-use' | 'available' | 'ignored';

/** One light's bucket, by the rule above. It is the only place that rule is written. */
export const bucketOf = (one: LightFacts): LightBucket => {
	if (one.bindings.length > 0) return 'in-use';

	return one.ignored ? 'ignored' : 'available';
};

/** The lights in one bucket, in the operator's order. */
export const inBucket = (list: LightFacts[], bucket: LightBucket): LightFacts[] =>
	list.filter((one) => bucketOf(one) === bucket);

export const lights = $state<{ list: LightFacts[]; failure: string; loading: boolean; loaded: boolean }>({
	list: [],
	failure: '',
	loading: false,
	loaded: false,
});

/**
 * The lights every other screen may offer: known, and not pushed out of the way.
 *
 * One rule, in one place, because "show this light" has to mean the same thing on every screen —
 * the Overview's lights with no printer, the Bindings pickers, and the Lights screen's own In-Use
 * and Available buckets. An ignored light is not gone: it is still in `lights.list`, which is what
 * the Lights screen's Ignored bucket is drawn from, and one call brings it back.
 *
 * A function rather than an exported `$derived`: a module cannot export derived state, and this
 * way each screen derives the list itself and reads `lights.list` inside its own derivation —
 * which is what makes it reactive in that screen rather than in this file.
 */
export const offered = (list: LightFacts[]): LightFacts[] => list.filter((one) => !one.ignored);

/** Reads the lights, their segments and the bindings on them, in that order. */
export const loadLights = async (): Promise<void> => {
	lights.loading = true;
	lights.failure = '';

	try {
		const api = moduleApi(heldHost());
		const [driven, set] = await Promise.all([api.listControllers(), api.listBindings()]);

		const details = await Promise.all(
			driven.controllers.map(async (controller) => {
				try {
					return await api.readController(controller.name);
				} catch (problem) {
					// A light that did not answer. Its segments are unknown rather than
					// absent, and the reason travels beside them so the card can say why
					// its add button is off instead of leaving a person to find out.
					return {
						controller,
						segments_known: false,
						segments: [] as Segment[],
						segments_unread: describe(problem),
					};
				}
			}),
		);

		lights.list = details.map((detail) => {
			const named = lightName(detail.controller?.address ?? '');

			return {
				controller: detail.controller,
				segments: detail.segments ?? [],
				segmentsKnown: detail.segments_known === true,
				segmentsUnread: detail.segments_unread ?? '',
				bindings: set.bindings.filter(
					(one) => one.light === detail.controller?.name || one.light === named,
				),
				// This list is the driven set — GET /controllers is derived from the bindings — so
				// every light on it is in use, and a light in use is never an ignored one.
				ignored: false,
			};
		});

		lights.loaded = true;
	} catch (problem) {
		lights.failure = describe(problem);
	} finally {
		lights.loading = false;
	}
};

/**
 * Every light this module knows, which is a longer list than the one it drives: a light somebody
 * owns exists whether or not a printer is shown on it (docs/product-requirements.md §2.6).
 *
 * It is separate from `lights` on purpose. `lights` is the driven set, and five screens build their
 * pickers from it; a light with nothing on it would be a row in a picker that selects nothing. This
 * is the Lights screen's own list, and it is the only screen that draws a card per light.
 */
export const known = $state<{ list: KnownLight[]; note: string }>({ list: [], note: '' });

/**
 * Reads every light this module knows, its own segments, and the bindings on it, in that order —
 * and puts them in `lights`, so that the card that draws a light draws it the same way whichever
 * list it came from.
 *
 * A light with no binding on it answers GET /controllers/{name} too: a light is a light, and its own
 * segments are exactly what somebody needs to see before he binds the first printer to it.
 */
export const loadKnownLights = async (): Promise<void> => {
	lights.loading = true;
	lights.failure = '';

	try {
		const api = moduleApi(heldHost());
		const [all, set] = await Promise.all([api.listLights(), api.listBindings()]);

		known.list = all.lights;
		known.note = all.note;

		const details = await Promise.all(
			all.lights.map(async (one, index): Promise<ControllerDetail> => {
				try {
					return await api.readController(one.name);
				} catch (problem) {
					return {
						controller: { name: one.name, address: one.address, key: '', segments: [], printers: [] },
						segments_known: false,
						segments: [] as Segment[],
						segments_unread: describe(problem),
					};
				}
			}),
		);

		lights.list = details.map((detail, index) => {
			const named = lightName(detail.controller?.address ?? '');

			return {
				controller: detail.controller,
				segments: detail.segments ?? [],
				segmentsKnown: detail.segments_known === true,
				segmentsUnread: detail.segments_unread ?? '',
				bindings: set.bindings.filter(
					(one) => one.light === detail.controller?.name || one.light === named,
				),
				// The flag is the store's own row, and `details` is built by mapping `all.lights`
				// in order — Promise.all keeps the order — so the row for one light is at the same
				// index as the detail read for it. It is carried rather than derived from the
				// address so that two spellings of one address cannot disagree about it.
				ignored: all.lights[index]?.ignored === true,
			};
		});

		lights.loaded = true;
	} catch (problem) {
		lights.failure = describe(problem);
	} finally {
		lights.loading = false;
	}
};

/**
 * The binding that already takes one segment of one light's bindings, and undefined when
 * none does. A whole-light binding takes every segment, which is why it answers for any
 * segment asked about. This is the rule, in the one place it is written: taken means *the
 * same segment, or either side is the whole light*.
 */
export const takerIn = (bindings: Binding[], segment: number): Binding | undefined =>
	bindings.find((binding) => binding.whole) ??
	bindings.find((binding) => !binding.whole && binding.segment === segment);

/** Every segment of a light no binding has taken. */
export const freeIn = (segments: Segment[], bindings: Binding[]): Segment[] =>
	segments.filter((segment) => takerIn(bindings, segment.id) === undefined);

/**
 * Whether the whole light is still on offer. A whole-light binding covers every
 * segment, so it is refused beside a zone binding; the whole light is therefore only
 * available on a light carrying nothing at all.
 */
export const wholeFree = (bindings: Binding[]): boolean => bindings.length === 0;

/** The binding driving this light's whole light, when one does. */
export const wholeTaker = (one: LightFacts): Binding | undefined => one.bindings.find((binding) => binding.whole);

/** The binding that already takes one segment of this light, and undefined when none does. */
export const takerOf = (one: LightFacts, segment: number): Binding | undefined =>
	takerIn(one.bindings, segment);

/** Every segment of this light no binding has taken. */
export const freeSegments = (one: LightFacts): Segment[] => freeIn(one.segments, one.bindings);

/** Whether the whole light is still on offer on this light. */
export const canTakeWhole = (one: LightFacts): boolean => wholeFree(one.bindings);

/** Whether a binding can be put on this light at all, and why not when it cannot. */
export const roomOn = (one: LightFacts): { free: number; whole: boolean; room: boolean } => {
	const free = freeSegments(one).length;
	const whole = canTakeWhole(one);

	return { free, whole, room: free > 0 || whole };
};

/** The controller's address as an operator wrote it, for a binding that names it. */
export const addressOf = (one: LightFacts): string => one.controller?.address ?? '';

/** The name the module gives one light on its routes. */
export const nameOf = (one: LightFacts): string => one.controller?.name ?? lightName(addressOf(one));
