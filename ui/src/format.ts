// The words and the arithmetic every screen repeats, in one place.
//
// Two screens print "last wrote 3 minutes ago" and both need the same buckets, and
// one rule decides what "a segment is taken" means -- the rule §2.2 records as a
// correction: a whole-light binding covers every segment, so taken means the same
// segment *or* either side being the whole light. A second copy of it would be a
// second answer to whether a light has room.
import type { Translate } from '@prusa/connect-ui/i18n';

/**
 * A controller's name on these routes: its address without the scheme and without a
 * trailing slash, lowercased. It is the one the module computes for a binding's
 * `light`, so a screen comparing two of them compares like with like.
 */
export const lightName = (address: string): string =>
	address
		.trim()
		.replace(/^[a-zA-Z][a-zA-Z0-9+.-]*:\/\//, '')
		.replace(/\/+$/, '')
		.toLowerCase();

/**
 * How long ago one RFC 3339 instant was, in the words a row carries. Absent or
 * unreadable answers `null`, so a row says "nothing written yet" rather than
 * inventing a time.
 */
export const ageText = (t: Translate, instant: string | undefined, nowMs: number = Date.now()): string | null => {
	if (!instant) return null;

	const then = Date.parse(instant);
	if (Number.isNaN(then)) return null;

	const seconds = Math.max(0, Math.round((nowMs - then) / 1000));

	if (seconds < 10) return t('common.just_now');
	if (seconds < 90) return t('common.seconds_ago', { count: seconds });

	const minutes = Math.round(seconds / 60);
	if (minutes < 90) return t('common.minutes_ago', { count: minutes });

	const hours = Math.round(minutes / 60);
	if (hours < 36) return t('common.hours_ago', { count: hours });

	return t('common.days_ago', { count: Math.round(hours / 24) });
};

/** A pause's length in words: "forever", "3 days", "45 min". */
export const lengthText = (t: Translate, seconds: number | undefined, forever: boolean): string => {
	if (forever || seconds === undefined) return t('pause.left_forever');
	if (seconds < 3600) return t('pause.left_minutes', { count: Math.max(1, Math.round(seconds / 60)) });
	if (seconds < 86400) return t('pause.left_hours', { count: Math.round(seconds / 3600) });

	return t('pause.left_days', { count: Math.round(seconds / 86400) });
};

/**
 * One segment, as a row or a picker names it: the light's own name for it, its
 * number, and the run it covers. WLED's `stop` is exclusive, so the last LED is
 * `stop - 1` and that is what a person reads.
 */
export const segmentText = (t: Translate, segment: number, name: string | undefined, run?: { start: number; stop: number; startY: number; stopY: number }): string => {
	if (segment < 0) return t('common.whole_light');

	const called = name && name.trim() !== '' ? name.trim() : t('common.segment_number', { count: segment });
	const where = run === undefined ? '' : ` · ${runText(run)}`;

	return `${called}${where}`;
};

/** The pixels one segment covers, in the controller's own spelling. */
export const runText = (run: { start: number; stop: number; startY: number; stopY: number }): string => {
	const columns = `${run.start}\u2013${Math.max(run.start, run.stop - 1)}`;
	const rows = `${run.startY}\u2013${Math.max(run.startY, run.stopY - 1)}`;

	return run.stopY > 0 || run.startY > 0 ? `${columns} \u00d7 ${rows}` : columns;
};

/** What a refusal said, in one line: the fault's own sentence when it carries one. */
export const describe = (problem: unknown): string => {
	if (problem instanceof Error) return problem.message;
	if (typeof problem === 'object' && problem !== null) {
		const said = problem as { detail?: string; message?: string; title?: string; code?: string };

		return said.detail ?? said.message ?? said.title ?? said.code ?? JSON.stringify(problem);
	}

	return String(problem);
};

/** A colour as CSS writes one, for the dot beside a frame that could not be drawn. */
export const cssColour = (colour: { r: number; g: number; b: number }): string =>
	`rgb(${colour.r} ${colour.g} ${colour.b})`;
