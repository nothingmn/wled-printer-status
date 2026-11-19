// The pause, for the whole module and for one binding, held once for every screen.
//
// docs/product-requirements.md §3 puts `Pause everything` in the header of every tab,
// because it is a panic button and not a page feature, and §2.1 puts a pause on each
// row of Right now. Two controls, one mechanism: the same six lengths, the same store
// behind them, one place that reads GET /pause. That is why this lives beside the
// screens rather than inside one of them -- a second implementation of the panic
// button is a second thing that can be wrong when it matters.
//
// The six lengths are the module's own, refused by name rather than rounded
// (PauseRequest in openapi.json): 10m, 1h, 6h, 1d, 3d and forever.
import { moduleApi, type PauseBody, type PauseLength, type PauseRow } from './api';
import { describe } from './format';
import { heldHost } from './host';

/** One length on offer, in the operator's own words. */
export interface Length {
	id: PauseLength;
	key: string;
}

/** The six, in the order the mockup lists them: the shortest first, forever last. */
export const LENGTHS: Length[] = [
	{ id: '10m', key: 'pause.10m' },
	{ id: '1h', key: 'pause.1h' },
	{ id: '6h', key: 'pause.6h' },
	{ id: '1d', key: 'pause.1d' },
	{ id: '3d', key: 'pause.3d' },
	{ id: 'forever', key: 'pause.forever' },
];

/** A refusal, kept as a key and a sentence so the screen draws it in its own language. */
export interface Refusal {
	key: string;
	problem: string;
}

/**
 * What GET /pause last said. One object rather than exported state, because a
 * module may not export a reassigned rune: the object is the one thing and its
 * members are what change.
 */
export const pauses = $state<{ view: PauseBody | null; failure: Refusal | null; busy: boolean }>({
	view: null,
	failure: null,
	busy: false,
});

/** Reads every pause there is. A store that cannot be read is a failure to say, not a silent "off". */
export const refreshPauses = async (): Promise<void> => {
	try {
		pauses.view = await moduleApi(heldHost()).readPause();
		pauses.failure = null;
	} catch (problem) {
		pauses.failure = { key: 'pause.read_failed', problem: describe(problem) };
	}
};

/** Runs one pause write, then re-reads the whole answer so the page shows what is true. */
const write = async (act: () => Promise<unknown>, failureKey: string): Promise<boolean> => {
	pauses.busy = true;
	pauses.failure = null;

	try {
		await act();
		await refreshPauses();

		return true;
	} catch (problem) {
		pauses.failure = { key: failureKey, problem: describe(problem) };

		return false;
	} finally {
		pauses.busy = false;
	}
};

/** Stops every write and every log line, for one of the six lengths. */
export const pauseEverything = (length: PauseLength): Promise<boolean> =>
	write(() => moduleApi(heldHost()).pauseEverything(length), 'pause.failed');

/** Resumes everything, and repaints at once. */
export const resumeEverything = (): Promise<boolean> =>
	write(() => moduleApi(heldHost()).resumeEverything(), 'pause.resume_failed');

/** Stops one binding: its light goes off, or a borrowed zone is handed back. */
export const pauseOne = (binding: string, length: PauseLength): Promise<boolean> =>
	write(() => moduleApi(heldHost()).pauseBinding(binding, length), 'pause.failed');

/** Takes one binding's own pause away. */
export const resumeOne = (binding: string): Promise<boolean> =>
	write(() => moduleApi(heldHost()).resumeBinding(binding), 'pause.resume_failed');

/** The global pause's own row, when one is in force. */
export const globalPause = (): PauseRow | null =>
	pauses.view?.pauses.find((one) => one.scope === 'global' && one.on) ?? null;

/**
 * Whether the module as a whole is paused. `why` is the scope that pauses it, so a
 * row paused by the global pause reads "paused with everything" and not "paused".
 */
export const everythingPaused = (): boolean =>
	pauses.view?.paused === true && pauses.view.why === 'global';

/** What one binding's pause says, by the binding's own light and segment. */
export const fateOf = (binding: { light: string; segment: number }): PauseBody['bindings'][number] | undefined =>
	pauses.view?.bindings.find((one) => one.light === binding.light && one.segment === binding.segment);
