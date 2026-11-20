// `Refresh everything`, held once for every screen — the button beside the pause.
//
// The operator's own words for it are in refresh.go's header, and the two facts a screen has to
// carry are the reason this lives beside the screens rather than inside one of them:
//
//   - **the answer is per light and per segment, never a single "done".** A sweep writes every
//     binding the module drives, one light at a time, and a controller that did not answer has to be
//     named as such. So this holds the whole answer and the shell draws a row per binding; nothing
//     here summarises it into a yes.
//   - **a press cannot double-fire.** The screen draws a busy state while its own request is in the
//     air, and the route refuses a second sweep by code — this is the half a person sees, and
//     `busy` is set before the request leaves rather than when it returns, so two presses of one
//     button in one frame are one request.
import { moduleApi, type RefreshBody } from './api';
import { describe } from './format';
import { heldHost } from './host';

/** A refusal, kept as a key and a sentence so the screen draws it in its own language. */
export interface Refusal {
	key: string;
	problem: string;
}

/**
 * What POST /refresh last said. One object rather than exported state, because a module may not
 * export a reassigned rune: the object is the one thing and its members are what change.
 */
export const refresh = $state<{ view: RefreshBody | null; failure: Refusal | null; busy: boolean }>({
	view: null,
	failure: null,
	busy: false,
});

/**
 * Writes every binding the module drives, and keeps what each one did.
 *
 * A failed sweep clears the rows it replaced: an old answer drawn beside "that refresh was not
 * accepted" would be a picture of lights the press did not touch.
 */
export const refreshEverything = async (): Promise<void> => {
	if (refresh.busy) return;

	refresh.busy = true;
	refresh.failure = null;

	try {
		refresh.view = await moduleApi(heldHost()).refreshEverything();
	} catch (problem) {
		refresh.view = null;
		refresh.failure = { key: 'refresh.failed', problem: describe(problem) };
	} finally {
		refresh.busy = false;
	}
};

/** Takes the answer off the screen. The button stays where it is. */
export const hideRefresh = (): void => {
	refresh.view = null;
};

/**
 * The four outcomes the route answers with, as the keys a screen draws them by.
 *
 * One table rather than a string built from the outcome, because the two spellings differ — the
 * route says `not-reached` and a catalogue key cannot carry a hyphen — and a screen that guessed
 * would draw a raw key the moment a fifth outcome appeared.
 */
const OUTCOME_KEYS: Record<string, string> = {
	refreshed: 'refresh.outcome_refreshed',
	skipped: 'refresh.outcome_skipped',
	failed: 'refresh.outcome_failed',
	'not-reached': 'refresh.outcome_not_reached',
};

/** What one refreshed binding carried: a segment body, or pixels this module drew. */
const WROTE_KEYS: Record<string, string> = {
	segments: 'refresh.wrote_segments',
	pixels: 'refresh.wrote_pixels',
};

/**
 * The word for one binding's outcome.
 *
 * An outcome this build does not know is drawn as a failure rather than as a success: a row whose
 * answer cannot be read is not evidence that the light was written.
 */
export const outcomeKey = (outcome: string): string =>
	OUTCOME_KEYS[outcome] ?? 'refresh.outcome_failed';

/** The words for what one refreshed binding carried, and empty for anything else. */
export const wroteKey = (wrote: string): string => WROTE_KEYS[wrote] ?? '';
