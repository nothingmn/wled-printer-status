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
import { moduleApi, type Binding, type Controller, type Segment } from './api';
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
}

export const lights = $state<{ list: LightFacts[]; failure: string; loading: boolean; loaded: boolean }>({
	list: [],
	failure: '',
	loading: false,
	loaded: false,
});

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
