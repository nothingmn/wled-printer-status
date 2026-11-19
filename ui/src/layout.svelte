<script lang="ts">
	// Layout: how one binding's panel is composed — and, on a strip, why there is almost nothing to
	// compose.
	//
	// docs/product-requirements.md §2.5 gives this screen two renderings, and they are two because the
	// hardware is two:
	//
	//   - **A panel is a matrix** (256 x 64 on the operator's light), so it carries the whole composer:
	//     the bar and the band it sits in, how many rows that band is, the percentage at scale 5, 4 or
	//     3, the job's name, whether the two take turns or sit side by side every N seconds, and the
	//     seven optional lines that may sit under the picture — time remaining, the clock it ends at,
	//     the temperatures, how long it has been printing, the material, the printer's name, and how far
	//     up it has printed.
	//
	// The seven lines are a menu and not a layout: each takes seven rows with one blank row between, so
	// most sets of them cannot fit a 64-row panel beside the digits. The arithmetic is therefore part of
	// this screen and not a surprise after saving — how many fit, which lines have to go, and which of
	// the two numbers the picture is made of (the bar's rows or the digits' scale) would take them all.
	// It is the module's own rule restated, and the refusal is a sentence rather than a switch that
	// springs back.
	//   - **A strip is one dimension** (48 LEDs), so it carries a fill from the left and nothing else.
	//     No bar band, no words, no layers: that is the dimension, not a missing feature. The screen
	//     says so rather than leaving a blank tab for somebody to wonder about, and it offers the four
	//     panel controls nowhere on a strip.
	//
	// **The arrangement, and why it is four groups.** The controls used to be one long run — bar, band,
	// rows, percentage, scale, name, rotation, seconds, seven toggles, then the fit sentence — with
	// nothing saying which control belonged with which and the picture below all of it. They are now
	// four named groups, each answering one question, and the picture is beside them rather than under
	// them:
	//
	//   1. **The picture** — what this binding draws, and the verdict on whether it fits, held in its
	//      own column. It is sticky from `lg` so it stays put as the groups below it change height
	//      (which they do: the sharing group appears and disappears), and it is the thing being edited,
	//      so it does not scroll away while somebody is editing it.
	//   2. **What is in it** — the bar (on, which band, how many rows), the percentage (on, at which
	//      scale) and the job's name. These are the three things the picture is made of.
	//   3. **How they share the row** — take turns or side by side, and the turn length. It is shown
	//      only when more than one of bar, percentage and name is switched on, because with one thing on
	//      there is nothing to share the row with and a greyed-out control is the clutter this screen was
	//      reorganised to remove. Hidden never means forgotten: the route replaces the row whole, so the
	//      stored rotation and turn length are written back unchanged while the group is out of sight.
	//   4. **Lines under the picture** — the seven toggles, the live row count, and, when the set cannot
	//      fit, the refusal in words naming the lines that have no room.
	//
	// **Moving between bindings.** One card instead of a stack of them, and a segmented selector at the
	// top naming each binding on this light by its printer and segment. The selector carries a warning
	// on a binding whose arrangement does not fit, so a refusal on the binding nobody is looking at is
	// still visible from here.
	//
	// The arithmetic is shown instead of hidden, because it is the whole of the refusal rule: 64 rows
	// with a 12-row bar leave 52, and digits at scale 5 are 35 rows. A layout that will not fit is
	// **refused, not cropped** — the module judges that when it draws, against the panel's own
	// reported height, and draws nothing rather than a cropped picture. This screen computes the same
	// arithmetic from the height `GET /frames` reports, and refuses the write itself while it does not
	// fit, so nobody is told a layout saved that would draw an empty panel. When the height is unknown
	// — the light has never had a frame drawn, or is unplugged — the write is allowed and the line says
	// the fit is judged when it draws, which is the route's own reasoning: a layout is written while a
	// light may be unplugged, which is exactly when somebody is standing in front of it with a phone.
	//
	// Every value carries the origin it came from, and one reset per binding puts all of them back:
	// the store holds only the fields somebody changed, so a binding nobody laid out answers the
	// built-in default with `origin: default` — never null and never an empty box — and a reset is
	// offered only where there is something to reset. There is no per-value reset because there is no
	// per-value route: `DELETE` on the binding is the route, and the screen says what it does.
	//
	// **A press changes one control and leaves the rest of the screen where it is.** That is a rule
	// about the write path, not a hope: the draft is one object per binding for as long as the screen
	// is open and is mutated where it stands; the controls are never disabled while a write is in the
	// air, because a disabled input swallows the next press and a disabled *focused* one hands its
	// focus back to the page; writes for a binding are chained so two quick presses are two writes in
	// order; an answer whose write has been overtaken by a newer change is not adopted, so nothing
	// flips back to a value the operator has already moved on from; and the "Saved" line above the
	// controls is set and left set, because clearing it before every write lifted the whole card by a
	// row and dropped it back when the answer landed — twice per press, under the pointer.
	//
	// `side-by-side` is built in the module but it composes **only while a job is running**: idle,
	// finished, stopped and error keep the plain state colour, and that is true of take-turns too. The
	// line is on the page, quietly, because a layout that looks like nothing while the printer is idle
	// is not a bug.
	import { onMount } from 'svelte';

	import { Badge, Button } from '@prusa/connect-ui/primitives';

	import {
		moduleApi,
		type Frame,
		type Layout,
		type LayoutRequest,
		type LayoutsBody,
		type Origin,
	} from './api';
	import { ageText, describe, segmentText } from './format';
	import { heldHost } from './host';
	import { addressOf, lights, loadLights, nameOf } from './lights.svelte';

	const host = heldHost();
	const api = moduleApi(host);
	const t = host.t;

	/** One of the seven optional lines: its key on the route, in the store, and in a draft. */
	type LineKey = 'remaining' | 'ends' | 'temperatures' | 'elapsed' | 'material' | 'printer_name' | 'layer';

	/**
	 * The seven lines in the order the module draws them — the operator's ranking — with the words
	 * this screen names each one by and the line the panel draws for it, which is the tooltip.
	 */
	const LINES: { key: LineKey; label: string; what: string }[] = [
		{ key: 'remaining', label: 'layout.line_remaining', what: 'layout.line_remaining_what' },
		{ key: 'ends', label: 'layout.line_ends', what: 'layout.line_ends_what' },
		{ key: 'temperatures', label: 'layout.line_temperatures', what: 'layout.line_temperatures_what' },
		{ key: 'elapsed', label: 'layout.line_elapsed', what: 'layout.line_elapsed_what' },
		{ key: 'material', label: 'layout.line_material', what: 'layout.line_material_what' },
		{ key: 'printer_name', label: 'layout.line_printer_name', what: 'layout.line_printer_name_what' },
		{ key: 'layer', label: 'layout.line_layer', what: 'layout.line_layer_what' },
	];

	/** Where each line's origin is read from, so a toggle can say whose value it is showing. */
	const LINE_ORIGIN: Record<LineKey, keyof Layout> = {
		remaining: 'remaining_origin',
		ends: 'ends_origin',
		temperatures: 'temperatures_origin',
		elapsed: 'elapsed_origin',
		material: 'material_origin',
		printer_name: 'printer_name_origin',
		layer: 'layer_origin',
	};

	/** What one binding is being edited towards: the fifteen keys the route writes. */
	interface Draft {
		bar: boolean;
		bar_band: 'top' | 'bottom';
		bar_rows: number;
		percent: boolean;
		percent_scale: 3 | 4 | 5;
		name: boolean;
		remaining: boolean;
		ends: boolean;
		temperatures: boolean;
		elapsed: boolean;
		material: boolean;
		printer_name: boolean;
		layer: boolean;
		rotation: 'take-turns' | 'side-by-side';
		rotate_seconds: number;
	}

	/**
	 * The three scales §2.5 offers, in the order the mockup lists them. Nothing else is offered: a
	 * scale outside these is refused by the route rather than rounded.
	 */
	const SCALES: (3 | 4 | 5)[] = [5, 4, 3];

	/** The two rotations §2.5 offers, `take-turns` first because it is what this module has shipped. */
	const ROTATIONS: ('take-turns' | 'side-by-side')[] = ['take-turns', 'side-by-side'];

	/**
	 * The font's own numbers, which are what the refusal is made of: one glyph is seven rows, so the
	 * digits at scale five are thirty-five rows and a name at scale two is fourteen. They are the
	 * module's (`internal/text`, `layout.go`), and the arithmetic below is the module's own rule
	 * restated so the screen and the painter cannot disagree about whether a layout fits.
	 */
	const GLYPH_ROWS = 7;
	const NAME_SCALE_MAX = 2;
	const NAME_ROWS = NAME_SCALE_MAX * GLYPH_ROWS;
	const SIDE_DIGITS_SCALE = 4;
	const SIDE_DIGITS_SCALE_SHORT = 3;
	const SIDE_BAND_FOR_FULL = 28;
	const MIN_PERCENT_SCALE = 3;
	/** One optional line's height, and the blank row the module leaves between two of them. */
	const LINE_ROWS = GLYPH_ROWS;
	const LINE_GAP = 1;

	/** The four reasons a frame is about a job in progress, which is when a panel composes at all. */
	const JOB_REASONS = ['progress', 'starting', 'paused-frozen', 'progress-unknown'];

	/** The light this page is scoped to, by the name these routes use: its address, no scheme. */
	let chosen = $state('');
	/**
	 * The binding showing, by the id these routes use. One card is drawn at a time, and this is
	 * which one — so reaching the second binding is a press rather than a scroll past the first.
	 */
	let picked = $state('');
	/** The light's bindings and their layouts, as the route last answered. */
	let board = $state<LayoutsBody | null>(null);
	/** One frame per binding, which is the only thing that knows a binding's shape and height. */
	let frames = $state<Frame[]>([]);
	/** Each binding's draft, keyed by the binding's id — the same key the route writes by. */
	let drafts = $state<Record<string, Draft>>({});
	/** One refusal per binding, answered where it was made. */
	let faults = $state<Record<string, string>>({});
	/**
	 * The writes in the air, one chain per binding.
	 *
	 * A change is written as it is made, so two quick changes are two writes. Chaining them makes
	 * them arrive in the order they were made instead of racing each other, which is what lets the
	 * controls stay live while a write is in flight — a control disabled until the route answers is
	 * a control that swallows the next press, and disabling the one just pressed hands its focus
	 * back to the page.
	 *
	 * It is deliberately not state: nothing on the screen is drawn from it.
	 */
	const queue = new Map<string, Promise<void>>();
	/**
	 * How many times each binding's draft has been edited, so an answer can tell whether it is
	 * still the newest thing said about that binding: the write it answers may have been made
	 * before a change that is already on its way.
	 */
	const revisions = new Map<string, number>();
	/**
	 * How many writes each binding has in the air, and the one thing that reads it is the discard:
	 * a change is not there to be put back while the write that carries it is still out, and a
	 * button that says it is would leave the screen saying one thing and the store another.
	 */
	let writing = $state<Record<string, number>>({});
	let loading = $state(true);
	let reading = $state(false);
	let failure = $state('');
	let notice = $state('');

	/** One layout as a draft to edit. */
	const draftOf = (one: Layout): Draft => ({
		bar: one.bar,
		bar_band: one.bar_band,
		bar_rows: one.bar_rows,
		percent: one.percent,
		percent_scale: one.percent_scale,
		name: one.name,
		remaining: one.remaining,
		ends: one.ends,
		temperatures: one.temperatures,
		elapsed: one.elapsed,
		material: one.material,
		printer_name: one.printer_name,
		layer: one.layer,
		rotation: one.rotation,
		rotate_seconds: one.rotate_seconds,
	});

	const draftAt = (one: Layout): Draft => drafts[one.id] ?? draftOf(one);

	/**
	 * The fifteen keys the route writes, as one request: every key goes, because the route replaces
	 * the row whole — a body that left `percent_scale` out would put it back to the default while
	 * the operator watched a control stay where it was.
	 */
	const bodyOf = (draft: Draft): LayoutRequest => ({
		bar: draft.bar,
		bar_band: draft.bar_band,
		bar_rows: draft.bar_rows,
		percent: draft.percent,
		percent_scale: draft.percent_scale,
		name: draft.name,
		remaining: draft.remaining,
		ends: draft.ends,
		temperatures: draft.temperatures,
		elapsed: draft.elapsed,
		material: draft.material,
		printer_name: draft.printer_name,
		layer: draft.layer,
		rotation: draft.rotation,
		rotate_seconds: draft.rotate_seconds,
	});

	/**
	 * Puts one binding's draft back to a layout, in place.
	 *
	 * In place and never by replacement: a draft is one object for as long as the screen is open,
	 * so a value that arrives from the store changes the one control that reads it and no other
	 * part of the screen is re-made. Replacement is also how an answer used to land on top of a
	 * change made after its write had already gone — the older value winning on screen, with
	 * nothing a person could see to explain it.
	 */
	const adopt = (id: string, one: Layout): void => {
		const draft = drafts[id];

		if (draft === undefined) {
			drafts[id] = draftOf(one);

			return;
		}

		const held = draftOf(one);

		for (const key of Object.keys(held) as (keyof Draft)[]) {
			(draft as unknown as Record<string, unknown>)[key] = held[key];
		}
	};

	/** The frame drawn for one binding, when the module has drawn one. */
	const frameOf = (one: Layout): Frame | undefined => frames.find((frame) => frame.binding === one.id);

	/** A binding's shape, from its own frame, and empty before the module has drawn one. */
	const kindOf = (one: Layout): 'panel' | 'strip' | '' => frameOf(one)?.kind ?? '';

	/** The panel's own height in rows, and 0 when the module has not drawn a frame for it yet. */
	const heightOf = (one: Layout): number => frameOf(one)?.height ?? 0;

	/** One binding's printer, as the row names it. */
	const printerOf = (one: Layout): string =>
		lights.list
			.flatMap((light) => light.bindings)
			.find((binding) => binding.id === one.id)?.printer_name ?? one.printer;

	/** The segment one binding drives, in the light's own words. */
	const segmentOf = (one: Layout): string => {
		const found = lights.list.find((light) => nameOf(light) === chosen)?.segments.find((held) => held.id === one.segment);

		return segmentText(t, one.segment, found?.name, found);
	};

	/** The rows the bar takes, which is none when the bar is switched off. */
	const barRows = (draft: Draft): number => (draft.bar ? draft.bar_rows : 0);

	/** The rows the text band has left: the panel's height less the bar. */
	const textRows = (draft: Draft, height: number): number => height - barRows(draft);

	/** The lines this draft switches on, in the order the module draws them. */
	const linesOn = (draft: Draft): LineKey[] => LINES.filter((line) => draft[line.key]).map((line) => line.key);

	/** The rows a stack of that many lines takes: seven each and one blank row between each pair. */
	const stackHeight = (count: number): number => (count <= 0 ? 0 : count * LINE_ROWS + (count - 1) * LINE_GAP);

	/** The scale the job's name is drawn at in a band of that many rows: as large as it allows, twice at most. */
	const nameScale = (band: number): number => Math.min(Math.max(Math.floor(band / GLYPH_ROWS), 1), NAME_SCALE_MAX);

	/**
	 * One line in the operator's words, spelled out here so the catalogue check can see every key.
	 * `lower` is for the places a list of them sits inside a sentence: the toggle labels are the
	 * names of things and keep their capital, the same words mid-sentence do not.
	 */
	const lineName = (key: LineKey, lower = false): string => {
		const said = lineLabel(key);

		return lower && said !== '' ? `${said.charAt(0).toLowerCase()}${said.slice(1)}` : said;
	};

	/** The line's own label, as its toggle carries it. */
	const lineLabel = (key: LineKey): string => {
		switch (key) {
			case 'remaining':
				return t('layout.line_remaining');
			case 'ends':
				return t('layout.line_ends');
			case 'temperatures':
				return t('layout.line_temperatures');
			case 'elapsed':
				return t('layout.line_elapsed');
			case 'material':
				return t('layout.line_material');
			case 'printer_name':
				return t('layout.line_printer_name');
			case 'layer':
				return t('layout.line_layer');
		}
	};

	/** A set of lines as a sentence: "a", "a and b", "a, b and c". */
	const joinWords = (words: string[]): string =>
		words.length === 0 ? '' : words.length === 1 ? words[0] : `${words.slice(0, -1).join(', ')} and ${words[words.length - 1]}`;

	/** A set of lines in the operator's words, joined for a sentence. */
	const lineNames = (keys: LineKey[], lower = false): string => joinWords(keys.map((key) => lineName(key, lower)));

	/**
	 * The rows the picture's own row needs in a band of that many rows: the taller of the digits and
	 * the name, at the scales that band allows. `layout.go`'s primaryHeight, restated.
	 */
	const primaryHeight = (draft: Draft, band: number): number => {
		let height = 0;

		if (draft.percent) height = Math.max(height, draft.percent_scale * GLYPH_ROWS);
		if (draft.name) height = Math.max(height, nameScale(band) * GLYPH_ROWS);

		// Side by side both parts are up at once and the digits are capped, which is the mockup's rule.
		if (draft.rotation === 'side-by-side' && draft.percent && draft.name) {
			const cap = band < SIDE_BAND_FOR_FULL ? SIDE_DIGITS_SCALE_SHORT : SIDE_DIGITS_SCALE;
			height = Math.max(height, Math.min(draft.percent_scale, cap) * GLYPH_ROWS, NAME_ROWS);
		}

		return height;
	};

	/**
	 * How many of the optional lines a text band of that many rows can hold beside the picture's own
	 * row: the count that leaves a primary band with a glyph's worth of rows and holds what the
	 * picture needs, counted down from the most asked for. Zero when none fits.
	 */
	const stackFit = (draft: Draft, band: number, wanted: number): number => {
		for (let count = wanted; count > 0; count -= 1) {
			const primary = band - stackHeight(count);

			if (primary < GLYPH_ROWS) continue;
			if (primaryHeight(draft, primary) <= primary) return count;
		}

		return 0;
	};

	/**
	 * What in the picture's own row is asking for rows, for the refusal: the part that took the room.
	 * `many` is whether that part is plural, because the refusal's verb has to agree with it — "the
	 * digits at scale 5 take" against "the job's name takes".
	 */
	const primaryPhrase = (draft: Draft, band: number): { said: string; many: boolean } => {
		const digits = draft.percent ? draft.percent_scale * GLYPH_ROWS : 0;
		const name = draft.name ? nameScale(band) * GLYPH_ROWS : 0;

		if (digits > 0 && name > 0 && draft.rotation === 'side-by-side') {
			return { said: t('layout.fit_what_beside'), many: true };
		}

		if (digits >= name && digits > 0) return { said: t('layout.fit_what_digits', { scale: draft.percent_scale }), many: true };
		if (name > 0) return { said: t('layout.fit_what_name'), many: false };

		return { said: t('layout.fit_what_picture'), many: false };
	};

	/**
	 * Whether this draft would draw on a panel of that height with every line it asks for — `holds`
	 * in `layout.go`, asked without a sentence. The candidates below are put back through it, so a
	 * sentence that says "a bar of six rows" is a sentence that has been tried.
	 */
	const holds = (draft: Draft, height: number, wanted: number): boolean => {
		const rows = barRows(draft);
		if (rows >= height) return false;

		const left = height - rows;
		if ((draft.percent || draft.name || wanted > 0) && left < GLYPH_ROWS) return false;

		const count = stackFit(draft, left, wanted);
		if (count < wanted) return false;

		const primary = count > 0 ? left - stackHeight(count) : left;

		return primaryHeight(draft, primary) <= primary;
	};

	/** The thickest bar this panel could hold with every line asked for, and null when none would. */
	const rowsThatFit = (draft: Draft, height: number, wanted: number): number | null => {
		for (let rows = 0; rows < barRows(draft); rows += 1) {
			// A bar of no rows is a bar switched off: the route spells those two differently.
			const thinner: Draft = { ...draft, bar: rows > 0, bar_rows: rows > 0 ? rows : draft.bar_rows };

			if (holds(thinner, height, wanted)) return rows;
		}

		return null;
	};

	/** The smallest percentage scale that would hold every line asked for, and null when none would. */
	const scaleThatFits = (draft: Draft, height: number, wanted: number): number | null => {
		if (!draft.percent) return null;

		for (let scale = MIN_PERCENT_SCALE; scale < draft.percent_scale; scale += 1) {
			if (holds({ ...draft, percent_scale: scale as 3 | 4 | 5 }, height, wanted)) return scale;
		}

		return null;
	};

	/** Every way this panel could hold all the lines asked for, as whole sentences. */
	const roomsFor = (draft: Draft, height: number, wanted: number): string[] => {
		const rooms: string[] = [];
		const rows = rowsThatFit(draft, height, wanted);

		if (rows !== null && rows < barRows(draft)) {
			rooms.push(rows === 0 ? t('layout.room_bar_off', { count: wanted }) : t('layout.room_bar', { rows, count: wanted }));
		}

		const scale = scaleThatFits(draft, height, wanted);
		if (scale !== null) rooms.push(t('layout.room_scale', { scale, count: wanted }));

		return rooms;
	};

	/**
	 * Whether a draft fits the panel it is pointed at, and the sentence for either answer.
	 *
	 * This is `bandsOn`, `fitOn` and `tooTall` restated: a bar that leaves fewer rows than one glyph,
	 * the optional lines the band can hold, a pinned scale whose digits are taller than what the lines
	 * leave, and — side by side — the digits capped at four (or three on a short band) beside a name
	 * fourteen rows tall. A set of lines that does not fit is refused by naming them, so the answer is
	 * which switch to turn off rather than "invalid". It is arithmetic on the height the frame
	 * reports, so it is skipped when that height is unknown.
	 */
	const fitOf = (draft: Draft, height: number): { fits: boolean; said: string } => {
		const rows = barRows(draft);
		const left = textRows(draft, height);
		const wanted = linesOn(draft);

		const band = t('layout.fit_band', { height, rows, left });
		const digits = t('layout.fit_digits', { scale: draft.percent_scale, rows: draft.percent_scale * GLYPH_ROWS });
		const drawn = draft.percent || draft.name;

		if (rows >= height) {
			return { fits: false, said: t('layout.fit_no_room', { rows, height }) };
		}

		if ((drawn || wanted.length > 0) && left < GLYPH_ROWS) {
			return { fits: false, said: t('layout.fit_no_text', { rows, left, glyph: GLYPH_ROWS }) };
		}

		// The lines are placed first, because they are what the picture's own row gives way to.
		const count = stackFit(draft, left, wanted.length);
		const held = wanted.slice(0, count);
		const missed = wanted.slice(count);
		const primary = count > 0 ? left - stackHeight(count) : left;

		if (missed.length > 0) {
			const rooms = roomsFor(draft, height, wanted.length);
			const need = primaryPhrase(draft, left);

			return {
				fits: false,
				said: t('layout.fit_lines_over', {
					lines: lineNames(wanted, true),
					height,
					rows,
					left,
					need: need.said,
					verb: need.many ? t('layout.fit_verb_many') : t('layout.fit_verb_one'),
					needRows: primaryHeight(draft, left),
					fits: count,
					total: wanted.length,
					held: held.length === 0 ? t('layout.fit_none') : lineNames(held, true),
					missed: lineNames(missed, true),
					rooms: rooms.length === 0 ? t('layout.room_none') : rooms.join(' '),
				}),
			};
		}

		if (draft.percent && draft.percent_scale * GLYPH_ROWS > primary) {
			return { fits: false, said: t('layout.fit_scale', { band, digits, left: primary, scale: draft.percent_scale }) };
		}

		if (draft.rotation === 'side-by-side' && draft.percent && draft.name) {
			const cap = primary < SIDE_BAND_FOR_FULL ? SIDE_DIGITS_SCALE_SHORT : SIDE_DIGITS_SCALE;
			const beside = Math.min(draft.percent_scale, cap) * GLYPH_ROWS;

			if (Math.max(beside, NAME_ROWS) > primary) {
				return {
					fits: false,
					said: t('layout.fit_beside', {
						scale: Math.min(draft.percent_scale, cap),
						digits: beside,
						name: NAME_ROWS,
						left: primary,
					}),
				};
			}
		}

		if (wanted.length > 0) {
			return {
				fits: true,
				said: t('layout.fit_lines_ok', {
					band,
					lines: lineNames(wanted, true),
					verb: wanted.length === 1 ? t('layout.fit_verb_one') : t('layout.fit_verb_many'),
					count: wanted.length,
					rows: stackHeight(wanted.length),
					left: primary,
				}),
			};
		}

		return { fits: true, said: t('layout.fit_ok', { band, left }) };
	};

	/**
	 * How the lines this draft asks for divide between what fits and what does not — the same
	 * placement `fitOf` makes, kept separate so the lines group can name the ones with no room
	 * without restating the arithmetic.
	 */
	const linesTally = (
		draft: Draft,
		height: number,
	): { wanted: LineKey[]; fits: number; missed: LineKey[]; stack: number; left: number } => {
		const left = textRows(draft, height);
		const wanted = linesOn(draft);
		const fits = stackFit(draft, left, wanted.length);

		return { wanted, fits, missed: wanted.slice(fits), stack: stackHeight(wanted.length), left };
	};

	/**
	 * The picture's boxes to scale, for the sketch beside the controls.
	 *
	 * It draws what the draft *is* — the band, the text band, the digits' box, the name's box and one
	 * box per optional line — and not a rendering of the module's glyphs, so it is the arithmetic a
	 * person can see. A box that does not fit is drawn at its true height and overflows its band,
	 * which is the point of showing it: that is the picture the module refuses rather than crops.
	 */
	const sketchOf = (
		draft: Draft,
		width: number,
		height: number,
	): {
		barTop: number;
		barRows: number;
		textTop: number;
		textRows: number;
		primaryRows: number;
		digitsX: number;
		digitsTop: number;
		digitsRows: number;
		digitsWidth: number;
		nameX: number;
		nameWidth: number;
		lines: { key: LineKey; top: number; rows: number }[];
	} => {
		const rows = barRows(draft);
		const band = { barTop: 0, textTop: 0 };

		if (rows > 0) {
			if (draft.bar_band === 'top') band.textTop = rows;
			else band.barTop = height - rows;
		}

		const left = height - rows;
		const wanted = linesOn(draft);
		const stack = stackHeight(wanted.length);
		const primaryRows = Math.max(0, left - stack);

		// Every line asked for is drawn, bottom up in the module's order, whether or not it fits: a
		// stack taller than the band has to be visible as one.
		const linesTop = band.textTop + left - stack;
		const lines = wanted.map((key, index) => ({
			key,
			top: linesTop + index * (LINE_ROWS + LINE_GAP),
			rows: LINE_ROWS,
		}));

		const cap = primaryRows < SIDE_BAND_FOR_FULL ? SIDE_DIGITS_SCALE_SHORT : SIDE_DIGITS_SCALE;
		const scale = draft.rotation === 'side-by-side' && draft.name ? Math.min(draft.percent_scale, cap) : draft.percent_scale;

		// "100%" is four glyphs: five columns each plus the space between them, so twenty-three columns
		// at scale one — the module's own measurement of the widest percentage it draws.
		const digitsWidth = Math.min(width, 23 * scale);
		const digitsRows = scale * GLYPH_ROWS;
		const beside = draft.rotation === 'side-by-side';
		const nameX = 10 + digitsWidth + 16;

		return {
			barTop: band.barTop,
			barRows: rows,
			textTop: band.textTop,
			textRows: left,
			primaryRows,
			digitsX: beside ? 10 : Math.max(0, (width - digitsWidth) / 2),
			digitsTop: band.textTop + Math.max(0, (primaryRows - digitsRows) / 2),
			digitsRows,
			digitsWidth,
			nameX,
			nameWidth: beside ? Math.max(0, width - nameX - 4) : width,
			lines,
		};
	};

	/** A frame's own picture, as a data URL, when it has one. */
	const pictureOf = (frame: Frame): string =>
		frame.image === undefined ? '' : `data:${frame.image.media_type};base64,${frame.image.base64}`;

	/** Whether the frame on the wall is a picture of a job in progress rather than a state colour. */
	const aboutAJob = (frame: Frame): boolean => JOB_REASONS.includes(frame.frame.reason);

	/** The light's bindings, each with the frame drawn for it, in the operator's order. */
	const rows = $derived(
		[...(board?.layouts ?? [])].sort(
			(left, right) =>
				(lights.list.find((light) => nameOf(light) === chosen)?.bindings.find((held) => held.id === left.id)?.position ?? 0) -
				(lights.list.find((light) => nameOf(light) === chosen)?.bindings.find((held) => held.id === right.id)?.position ?? 0),
		),
	);

	/**
	 * The binding showing: the one picked, or the light's first when nothing has been picked yet
	 * (and after a re-read, where a picked binding may be gone).
	 */
	const showing = $derived(rows.find((one) => one.id === picked) ?? rows[0]);

	/** Whether a binding is a panel whose draft, as it stands, would not fit it. */
	const overflows = (one: Layout): boolean => {
		const height = heightOf(one);

		return kindOf(one) === 'panel' && height > 0 && !fitOf(draftAt(one), height).fits;
	};

	/** How many of the three things the picture is made of this draft switches on — group 3's test. */
	const partsOn = (draft: Draft): number => (draft.bar ? 1 : 0) + (draft.percent ? 1 : 0) + (draft.name ? 1 : 0);

	/** Reads one light's bindings, their layouts and the frames drawn for them. */
	const openLight = async (light: string): Promise<void> => {
		chosen = light;
		picked = '';
		board = null;
		drafts = {};
		faults = {};
		notice = '';
		queue.clear();
		revisions.clear();
		writing = {};
		reading = true;

		try {
			const [layouts, pictures] = await Promise.all([
				api.listLayouts(light),
				// A frame is the only thing that says whether a light is a panel and how tall it is, so a
				// failure here leaves the shape unknown rather than failing the page.
				api.listFrames().catch(() => ({ frames: [] as Frame[], count: 0, note: '' })),
			]);

			frames = pictures.frames;
			board = layouts;
			drafts = Object.fromEntries(layouts.layouts.map((one) => [one.id, draftOf(one)]));
			failure = '';
		} catch (problem) {
			failure = `${t('layout.load_failed')}: ${describe(problem)}`;
		} finally {
			reading = false;
		}
	};

	const load = async (): Promise<void> => {
		loading = true;
		failure = '';

		try {
			await loadLights();

			const first = lights.list[0];
			if (first !== undefined) await openLight(nameOf(first));
		} catch (problem) {
			failure = `${t('layout.load_failed')}: ${describe(problem)}`;
		} finally {
			loading = false;
		}
	};

	/** The board with one layout replaced, so a write's answer lands without a second read. */
	const withLayout = (answer: Layout): void => {
		if (board === null) return;

		board = {
			...board,
			layouts: board.layouts.map((one) => (one.id === answer.id ? answer : one)),
			count: board.layouts.length,
		};
	};

	/**
	 * Queues one binding's draft to be written.
	 *
	 * The checks are made here, on the draft as it stands, so a refusal is a sentence beside the
	 * control that caused it rather than a toast after a round trip: the route's own fixed limits
	 * (a bar is 1 to 48 rows of a 64-row panel, and a rotation of no seconds is not a rotation),
	 * and — on a panel whose height is known — the same arithmetic the painter would refuse with,
	 * because the module would draw nothing for a layout that does not fit and a saved row that
	 * draws nothing is what "refused, not cropped" forbids.
	 *
	 * What goes out is the draft *as it was when the change was made*, not as it stands when the
	 * write's turn comes: a second change made while the first is in the air is its own write, and
	 * it goes behind it rather than jumping the queue.
	 */
	const commit = (one: Layout): void => {
		const draft = drafts[one.id];
		if (draft === undefined) return;

		delete faults[one.id];

		if (draft.bar_rows < 1 || draft.bar_rows > 48) {
			faults[one.id] = t('layout.rows_out_of_range');

			return;
		}

		if (draft.rotate_seconds < 1) {
			faults[one.id] = t('layout.seconds_out_of_range');

			return;
		}

		if (kindOf(one) === 'panel') {
			const height = heightOf(one);
			if (height > 0) {
				const fit = fitOf(draft, height);
				if (!fit.fits) {
					faults[one.id] = t('layout.not_saved', { why: fit.said });

					return;
				}
			}
		}

		const body = bodyOf(draft);
		const sent = revisions.get(one.id) ?? 0;
		const after = queue.get(one.id) ?? Promise.resolve();

		writing[one.id] = (writing[one.id] ?? 0) + 1;

		const run = after.then(() => send(one, body, sent));

		queue.set(
			one.id,
			run.catch(() => undefined),
		);
	};

	/**
	 * Writes one binding's layout, in order with the writes for that binding before it.
	 *
	 * `sent` is the draft's revision at the moment this body was taken: if the operator has changed
	 * something since, the answer is older than what the screen now says and the draft keeps it —
	 * the write already queued behind this one carries the newer value to the store.
	 */
	const send = async (one: Layout, body: LayoutRequest, sent: number): Promise<void> => {
		try {
			const answer = await api.changeLayout(one.id, body);
			withLayout(answer.layout);

			if ((revisions.get(one.id) ?? 0) === sent) adopt(one.id, answer.layout);

			// Said once and left said. The line is above the controls, so clearing it before every
			// write took the whole screen up a row and put it back down when the answer landed —
			// twice per press, under the pointer, which is how a press ends up on its neighbour.
			notice = t('layout.saved');

			// The painter picks the row up on its next pass and GET /frames draws from the same row, so
			// the picture beside the controls is asked for again rather than left as the last layout's.
			await refreshFrames();
		} catch (problem) {
			// The route's own sentence, verbatim, beside the control that caused it. The row is
			// unchanged, so the draft goes back to what the store holds — in place, so nothing moves.
			notice = '';
			faults[one.id] = t('layout.refused', { why: describe(problem) });
			adopt(one.id, rows.find((row) => row.id === one.id) ?? one);
		} finally {
			writing[one.id] = Math.max(0, (writing[one.id] ?? 1) - 1);
		}
	};

	/** A quiet re-read of the pictures, so a write's effect is shown and not assumed. */
	const refreshFrames = async (): Promise<void> => {
		try {
			frames = (await api.listFrames()).frames;
		} catch {
			// A picture is a courtesy here. The layout the route answered is the fact.
		}
	};

	/** Puts one binding back to the built-in default: the whole row, because that is the route. */
	const reset = (one: Layout): void => {
		delete faults[one.id];

		// Behind the writes already in the air for this binding, so a reset is never answered
		// before the change it replaces.
		const after = queue.get(one.id) ?? Promise.resolve();
		const run = after.then(() => putBack(one));

		queue.set(
			one.id,
			run.catch(() => undefined),
		);
	};

	/** The reset itself, once it is that binding's turn. */
	const putBack = async (one: Layout): Promise<void> => {
		try {
			const answer = await api.resetLayout(one.id);
			withLayout(answer.layout);
			adopt(one.id, answer.layout);
			notice = answer.removed === false ? t('layout.already_default') : t('layout.reset_done');
			await refreshFrames();
		} catch (problem) {
			faults[one.id] = t('layout.refused', { why: describe(problem) });
		}
	};

	/**
	 * One control changed: the draft takes it, in place, and the write follows.
	 *
	 * The draft is one object for the life of the screen and is mutated rather than rebuilt. A
	 * rebuilt object is a new value for every control that reads it, so one press re-rendered all
	 * seven toggles — and, worse, an answer carrying the rebuilt object landed on top of whatever
	 * the operator had changed since, putting the older value back on screen while he watched.
	 */
	const edit = (one: Layout, change: Partial<Draft>): void => {
		if (drafts[one.id] === undefined) drafts[one.id] = draftOf(one);

		const draft = drafts[one.id];

		for (const key of Object.keys(change) as (keyof Draft)[]) {
			const next = change[key];
			if (next !== undefined) (draft as unknown as Record<string, unknown>)[key] = next;
		}

		revisions.set(one.id, (revisions.get(one.id) ?? 0) + 1);
		commit(one);
	};

	/** Whether a draft has been edited away from what the store holds. */
	const dirty = (one: Layout): boolean => {
		const draft = drafts[one.id];
		if (draft === undefined) return false;

		const held = draftOf(one);

		return (
			draft.bar !== held.bar ||
			draft.bar_band !== held.bar_band ||
			draft.bar_rows !== held.bar_rows ||
			draft.percent !== held.percent ||
			draft.percent_scale !== held.percent_scale ||
			draft.name !== held.name ||
			LINES.some((line) => draft[line.key] !== held[line.key]) ||
			draft.rotation !== held.rotation ||
			draft.rotate_seconds !== held.rotate_seconds
		);
	};

	/**
	 * Whether there is a change on this binding to put back: a draft edited away from the row the
	 * store holds, with no write of it in the air. A refusal that never went out leaves one; a
	 * change already on its way does not, because the store is about to hold it.
	 */
	const discardable = (one: Layout): boolean => dirty(one) && (writing[one.id] ?? 0) === 0;

	/** A rotation in words. Two literals rather than a built key, so the check can see them. */
	const rotationLabel = (rotation: 'take-turns' | 'side-by-side'): string =>
		rotation === 'take-turns' ? t('layout.take_turns') : t('layout.side_by_side');

	/** A band in words. */
	const bandLabel = (band: 'top' | 'bottom'): string => (band === 'top' ? t('layout.band_top') : t('layout.band_bottom'));

	/** The brightness a binding writes, and what it means when it writes none. */
	const brightnessOf = (one: Layout): string => {
		const binding = lights.list.flatMap((light) => light.bindings).find((held) => held.id === one.id);

		if (binding === undefined || binding.brightness === undefined) {
			return t('layout.strip_brightness_borrowed', { segment: segmentOf(one) });
		}

		return t('layout.strip_brightness', { level: binding.brightness });
	};

	onMount(() => {
		void load();
	});
</script>

<section class="flex flex-col gap-4">
	<header class="flex flex-wrap items-end gap-3">
		<div class="flex flex-col gap-1">
			<h1 class="text-lg font-semibold">{t('layout.title')}</h1>
			<p class="text-sm opacity-70">{t('layout.intro')}</p>
		</div>

		<label class="ml-auto flex flex-col gap-1 text-sm">
			<span class="text-xs opacity-70">{t('layout.light')}</span>
			<select
				class="rounded border px-2 py-1"
				value={chosen}
				disabled={loading || lights.list.length === 0}
				onchange={(event) => void openLight((event.currentTarget as HTMLSelectElement).value)}
			>
				{#each lights.list as one (addressOf(one))}
					<option value={nameOf(one)}>{one.controller?.name ?? addressOf(one)}</option>
				{/each}
			</select>
		</label>
	</header>

	<p class="rounded border px-3 py-2 text-sm">{t('layout.per_binding')}</p>

	{#if failure !== ''}
		<div class="rounded border px-3 py-2 text-sm" role="alert">{failure}</div>
	{/if}
	{#if notice !== ''}
		<div class="rounded border px-3 py-2 text-sm" role="status">{notice}</div>
	{/if}

	{#if loading}
		<p class="text-sm opacity-70">{t('layout.loading')}</p>
	{:else if lights.list.length === 0}
		<div class="rounded border px-4 py-6">
			<p class="text-sm font-medium">{t('layout.no_lights')}</p>
		</div>
	{:else if reading}
		<p class="text-sm opacity-70">{t('layout.reading')}</p>
	{:else if board !== null}
		{#if !board.persisted}
			<p class="rounded border px-3 py-2 text-sm" role="status">
				{t('layout.not_kept', { why: board.note ?? '' })}
			</p>
		{/if}

		{#if rows.length === 0}
			<p class="text-sm opacity-70">{t('layout.no_bindings')}</p>
		{:else}
			<!--
				Moving between bindings. One card is drawn at a time and this picks which, so the
				second binding on a light is a press rather than a scroll past the first. The
				warning badge is the whole reason this is a selector and not a pair of tabs: a
				binding whose arrangement does not fit says so here, without being opened.
			-->
			<div class="flex flex-col gap-1">
				<div class="flex flex-wrap gap-1" role="group" aria-label={t('layout.bindings_heading')}>
					{#each rows as one (one.id)}
						{@const shown = showing !== undefined && showing.id === one.id}
						<button
							type="button"
							class={shown
								? 'flex items-center gap-2 rounded-md border bg-black/5 px-3 py-1.5 text-sm font-semibold dark:bg-white/10'
								: 'flex items-center gap-2 rounded-md border border-transparent px-3 py-1.5 text-sm opacity-70 hover:opacity-100'}
							aria-pressed={shown}
							onclick={() => (picked = one.id)}
						>
							<span>{printerOf(one)}</span>
							<span class="font-mono text-[11px] opacity-60">{segmentOf(one)}</span>
							{#if overflows(one)}
								<Badge variant="warning" size="sm">{t('layout.does_not_fit')}</Badge>
							{/if}
						</button>
					{/each}
				</div>
				<p class="text-[11px] opacity-60">{t('layout.bindings_note')}</p>
			</div>

			{#if showing !== undefined}
				{@const one = showing}
				{@const draft = draftAt(one)}
				{@const kind = kindOf(one)}
				{@const frame = frameOf(one)}
				{@const height = heightOf(one)}
				{@const fit = kind === 'panel' && height > 0 ? fitOf(draft, height) : null}
				{@const sketch = kind === 'panel' && height > 0 ? sketchOf(draft, frame?.width ?? 256, height) : null}
				{@const tally = kind === 'panel' && height > 0 ? linesTally(draft, height) : null}

				<article class="flex flex-col gap-3" data-binding={one.id} data-kind={kind}>
					<!-- Which binding this is, and the one reset that puts all of it back. -->
					<div class="flex flex-wrap items-center gap-2 rounded border px-3 py-2">
						<span class="text-sm font-semibold">{printerOf(one)}</span>
						<span class="font-mono text-[11px] opacity-60">{segmentOf(one)}</span>
						{#if kind === 'panel'}
							<Badge variant="outline" size="sm">{t('layout.panel_chip', { width: frame?.width ?? 0, height })}</Badge>
						{:else if kind === 'strip'}
							<Badge variant="outline" size="sm">{t('layout.strip_chip', { pixels: frame?.width ?? 0 })}</Badge>
						{:else}
							<Badge variant="outline" size="sm">{t('layout.shape_unknown_chip')}</Badge>
						{/if}
						<div class="ml-auto flex flex-wrap items-center gap-1">
							<Badge variant={one.overridden ? 'secondary' : 'outline'} size="sm">
								{one.overridden ? t('layout.yours') : t('layout.default')}
							</Badge>
							{#if discardable(one)}
								<Button variant="link" size="sm" onclick={() => adopt(one.id, one)}>
									{t('layout.discard')}
								</Button>
							{/if}
							{#if one.overridden}
								<Button variant="ghost" size="sm" onclick={() => reset(one)}>
									{t('layout.reset')}
								</Button>
							{/if}
						</div>
					</div>

					{#if (faults[one.id] ?? '') !== ''}
						<p class="rounded border border-dashed px-2 py-1 text-xs" role="alert">{faults[one.id]}</p>
					{/if}

					{#if kind === 'panel'}
						<!--
							The picture kept in view, and the groups beside it. From `lg` the picture's
							column is sticky, so it holds its place while the groups scroll or change
							height — which they do, because the sharing group comes and goes.
						-->
						<div class="grid gap-4 lg:grid-cols-[minmax(0,25rem)_minmax(0,1fr)]">
							<!--
								The column stretches to the row's height and the card inside it is
								what sticks: a sticky element is held inside its own grid area, so
								pinning the grid item itself has nowhere to travel.
							-->
							<div class="flex flex-col">
								<section class="flex flex-col gap-2 overflow-hidden rounded border lg:sticky lg:top-4">
									<div class="flex flex-wrap items-center gap-2 border-b bg-black/5 px-3 py-2 dark:bg-white/10">
										<span class="text-sm font-medium">{t('layout.picture_heading')}</span>
										<span class="ml-auto text-[11px] opacity-60">{t('layout.picture_what')}</span>
									</div>

									<div class="flex flex-col gap-2 px-3 pb-3">
										{#if frame !== undefined && pictureOf(frame) !== ''}
											<!--
												The picture's own pixel size is carried on the element, so the
												card keeps its height while the image decodes: a panel is four
												times as wide as it is tall and must not be laid out square.
											-->
											<img
												src={pictureOf(frame)}
												alt={t('layout.picture_alt')}
												width={frame.image?.width ?? frame.width}
												height={frame.image?.height ?? frame.height}
												class="w-full rounded border"
												style="image-rendering: pixelated;"
											/>
											<p class="text-[10px] opacity-60">{t('layout.picture_caption')}</p>
										{/if}

										{#if frame !== undefined}
											<div class="flex flex-col gap-1 text-[11px] opacity-70">
												<span>{t('layout.picture_drawn', { age: ageText(t, frame.reading.observed_at) ?? t('common.just_now') })}</span>
												{#if !aboutAJob(frame)}
													<p>{t('layout.picture_no_job')}</p>
												{/if}
												{#if frame.note !== undefined && frame.note !== ''}
													<p>{frame.note}</p>
												{/if}
											</div>
										{/if}

										{#if sketch !== null}
											<svg
												class="w-full"
												viewBox="0 0 {frame?.width ?? 256} {height}"
												role="img"
												aria-label={t('layout.sketch_alt', { height, rows: sketch.barRows })}
											>
												<rect x="0" y="0" width={frame?.width ?? 256} height={height} fill="none" stroke="currentColor" stroke-opacity="0.4" />
												{#if sketch.barRows > 0}
													<rect
														x="0"
														y={sketch.barTop}
														width={frame?.width ?? 256}
														height={sketch.barRows}
														fill="currentColor"
														fill-opacity="0.18"
														stroke="currentColor"
														stroke-opacity="0.5"
													/>
												{/if}
												{#if draft.percent}
													<rect
														x={sketch.digitsX}
														y={sketch.digitsTop}
														width={sketch.digitsWidth}
														height={sketch.digitsRows}
														fill="none"
														stroke="currentColor"
														stroke-dasharray="4 3"
														stroke-opacity="0.8"
													/>
												{/if}
												{#if draft.name && draft.rotation === 'side-by-side'}
													<rect
														x={sketch.nameX}
														y={sketch.textTop + Math.max(0, (sketch.primaryRows - 14) / 2)}
														width={sketch.nameWidth}
														height="14"
														fill="none"
														stroke="currentColor"
														stroke-dasharray="2 3"
														stroke-opacity="0.6"
													/>
												{/if}
												{#each sketch.lines as line (line.key)}
													<!-- One box per optional line, seven rows and a blank row apart, along the bottom
													     of the text band: the rows the picture's own row gives way to. -->
													<rect
														data-line={line.key}
														x="0"
														y={line.top}
														width={frame?.width ?? 256}
														height={line.rows}
														fill="currentColor"
														fill-opacity="0.15"
														stroke="currentColor"
														stroke-opacity="0.5"
													/>
												{/each}
											</svg>
											<p class="text-[10px] opacity-60">{t('layout.sketch_caption')}</p>
										{/if}
									</div>

									<!-- The verdict, beside the picture rather than under the controls. -->
									<div class="border-t px-3 py-2 text-xs {fit !== null && !fit.fits ? 'border-2' : ''}">
										<div class="flex flex-wrap items-center gap-2">
											<span class="font-medium">{t('layout.fit_heading')}</span>
											{#if fit === null}
												<Badge variant="outline" size="sm">{t('layout.fit_unknown_badge')}</Badge>
											{:else if fit.fits}
												<Badge variant="secondary" size="sm">{t('layout.fits')}</Badge>
											{:else}
												<Badge variant="warning" size="sm">{t('layout.does_not_fit')}</Badge>
											{/if}
										</div>
										<p
											class="mt-1 {fit !== null && !fit.fits ? 'font-medium' : 'opacity-70'}"
											role={fit !== null && !fit.fits ? 'alert' : undefined}
										>
											{fit === null ? t('layout.fit_unknown') : fit.said}
										</p>
									</div>
								</section>
							</div>

							<div class="flex flex-col gap-3">
								<!-- Group 2: what the picture is made of. -->
								<section class="overflow-hidden rounded border">
									<div class="flex flex-wrap items-center gap-2 border-b bg-black/5 px-3 py-2 dark:bg-white/10">
										<span class="text-sm font-medium">{t('layout.group_content')}</span>
										<span class="ml-auto text-[11px] opacity-60">{t('layout.group_content_what')}</span>
									</div>

									<div class="grid gap-3 px-3 py-3 md:grid-cols-3">
										<div class="flex flex-col gap-2">
											<label class="flex items-center gap-2 text-sm">
												<input
													type="checkbox"
													class="h-4 w-4"
													checked={draft.bar}
													onchange={(event) => edit(one, { bar: (event.currentTarget as HTMLInputElement).checked })}
												/>
												<span class="font-medium">{t('layout.bar')}</span>
												<span class="text-[10px] opacity-60">{one.bar_origin === 'store' ? t('layout.yours') : t('layout.default')}</span>
											</label>

											<label class="flex flex-col gap-1 text-sm">
												<span class="text-xs opacity-70">
													{t('layout.bar_band')}
													<span class="text-[10px]">{one.bar_band_origin === 'store' ? t('layout.yours') : t('layout.default')}</span>
												</span>
												<select
													class="rounded border px-2 py-1"
													value={draft.bar_band}
													disabled={!draft.bar}
													onchange={(event) =>
														edit(one, { bar_band: (event.currentTarget as HTMLSelectElement).value as 'top' | 'bottom' })}
												>
													{#each ['top', 'bottom'] as band (band)}
														<option value={band}>{bandLabel(band as 'top' | 'bottom')}</option>
													{/each}
												</select>
											</label>

											<label class="flex flex-col gap-1 text-sm">
												<span class="text-xs opacity-70">
													{t('layout.bar_rows')}
													<span class="text-[10px]">{one.bar_rows_origin === 'store' ? t('layout.yours') : t('layout.default')}</span>
												</span>
												<input
													type="number"
													class="w-24 rounded border px-2 py-1"
													min="1"
													max="48"
													value={draft.bar_rows}
													disabled={!draft.bar}
													onchange={(event) => edit(one, { bar_rows: Number((event.currentTarget as HTMLInputElement).value) })}
												/>
												<span class="text-[10px] opacity-60">{t('layout.bar_rows_note')}</span>
											</label>
										</div>

										<div class="flex flex-col gap-2">
											<label class="flex items-center gap-2 text-sm">
												<input
													type="checkbox"
													class="h-4 w-4"
													checked={draft.percent}
													onchange={(event) => edit(one, { percent: (event.currentTarget as HTMLInputElement).checked })}
												/>
												<span class="font-medium">{t('layout.percent')}</span>
												<span class="text-[10px] opacity-60">{one.percent_origin === 'store' ? t('layout.yours') : t('layout.default')}</span>
											</label>

											<label class="flex flex-col gap-1 text-sm">
												<span class="text-xs opacity-70">
													{t('layout.percent_scale')}
													<span class="text-[10px]">{one.percent_scale_origin === 'store' ? t('layout.yours') : t('layout.default')}</span>
												</span>
												<select
													class="rounded border px-2 py-1"
													value={String(draft.percent_scale)}
													disabled={!draft.percent}
													onchange={(event) =>
														edit(one, { percent_scale: Number((event.currentTarget as HTMLSelectElement).value) as 3 | 4 | 5 })}
												>
													{#each SCALES as scale (scale)}
														<option value={String(scale)}>{t('layout.scale', { scale })}</option>
													{/each}
												</select>
											</label>
										</div>

										<div class="flex flex-col gap-2">
											<label class="flex items-center gap-2 text-sm">
												<input
													type="checkbox"
													class="h-4 w-4"
													checked={draft.name}
													onchange={(event) => edit(one, { name: (event.currentTarget as HTMLInputElement).checked })}
												/>
												<span class="font-medium">{t('layout.name')}</span>
												<span class="text-[10px] opacity-60">{one.name_origin === 'store' ? t('layout.yours') : t('layout.default')}</span>
											</label>
										</div>
									</div>
								</section>

								<!--
									Group 3: how they share the row. Only when there is more than one of
									them to share it: with one thing on, a rotation control is a control
									with nothing to rotate, and a greyed-out one is the clutter this
									grouping exists to remove. Nothing is lost while it is away — the
									route replaces the row whole, so the stored rotation and turn length
									go back exactly as they came.
								-->
								{#if partsOn(draft) > 1}
									<section class="overflow-hidden rounded border" data-group="share">
										<div class="flex flex-wrap items-center gap-2 border-b bg-black/5 px-3 py-2 dark:bg-white/10">
											<span class="text-sm font-medium">{t('layout.group_share')}</span>
											<span class="ml-auto text-[11px] opacity-60">{t('layout.group_share_what')}</span>
										</div>

										<div class="flex flex-wrap items-end gap-3 px-3 py-3 text-sm">
											<label class="flex flex-col gap-1">
												<span class="text-xs opacity-70">
													{t('layout.rotation')}
													<span class="text-[10px]">{one.rotation_origin === 'store' ? t('layout.yours') : t('layout.default')}</span>
												</span>
												<select
													class="rounded border px-2 py-1"
													value={draft.rotation}
													onchange={(event) =>
														edit(one, {
															rotation: (event.currentTarget as HTMLSelectElement).value as 'take-turns' | 'side-by-side',
														})}
												>
													{#each ROTATIONS as rotation (rotation)}
														<option value={rotation}>{rotationLabel(rotation)}</option>
													{/each}
												</select>
											</label>

											<label class="flex flex-col gap-1">
												<span class="text-xs opacity-70">
													{t('layout.rotate_seconds')}
													<span class="text-[10px]">{one.rotate_seconds_origin === 'store' ? t('layout.yours') : t('layout.default')}</span>
												</span>
												<input
													type="number"
													class="w-20 rounded border px-2 py-1"
													min="1"
													value={draft.rotate_seconds}
													disabled={draft.rotation === 'side-by-side'}
													onchange={(event) => edit(one, { rotate_seconds: Number((event.currentTarget as HTMLInputElement).value) })}
												/>
											</label>
										</div>

										{#if draft.rotation === 'side-by-side'}
											<p class="border-t px-3 py-2 text-[11px] opacity-70">
												{one.rotation_note ?? t('layout.side_by_side_what')}
												{#if !(draft.percent && draft.name)}
													{' '}{t('layout.side_by_side_needs_both')}
												{/if}
											</p>
										{/if}
									</section>
								{/if}

								<!--
									Group 4: the seven optional lines. Each is one switch, each says whether
									the value in force is yours or the default, and the count under the
									heading is the rows they have taken out of the text band — because
									that is what decides whether the picture still fits. When the set
									cannot fit, the refusal is in words here, naming the lines with no
									room; the verdict beside the picture carries the arithmetic.
								-->
								<section class="overflow-hidden rounded border">
									<div class="flex flex-wrap items-center gap-2 border-b bg-black/5 px-3 py-2 dark:bg-white/10">
										<span class="text-sm font-medium">{t('layout.lines_heading')}</span>
										<span class="ml-auto text-[11px] opacity-60">
											{t('layout.lines_count', { on: linesOn(draft).length, rows: stackHeight(linesOn(draft).length) })}
										</span>
									</div>

									<div class="flex flex-col gap-2 px-3 py-3">
										<p class="text-[11px] opacity-60">{t('layout.lines_note')}</p>

										<div class="grid gap-1 md:grid-cols-2">
											{#each LINES as line (line.key)}
												<label class="flex items-center gap-2 text-sm" title={t(line.what)}>
													<input
														type="checkbox"
														class="h-4 w-4"
														checked={draft[line.key]}
														onchange={(event) => edit(one, { [line.key]: (event.currentTarget as HTMLInputElement).checked })}
													/>
													<span>{t(line.label)}</span>
													<span class="text-[10px] opacity-60">
														{(one[LINE_ORIGIN[line.key]] as Origin) === 'store' ? t('layout.yours') : t('layout.default')}
													</span>
												</label>
											{/each}
										</div>

										{#if draft.ends && one.zone === ''}
											<p class="text-[11px]" role="note">{t('layout.line_ends_no_zone')}</p>
										{/if}

										{#if tally !== null && tally.missed.length > 0}
											<p class="rounded border border-dashed px-2 py-1 text-xs" role="alert" data-refusal="lines">
												{t('layout.lines_refused', {
													rows: tally.stack,
													left: tally.left,
													missed: lineNames(tally.missed, true),
												})}
											</p>
										{/if}
									</div>
								</section>

								<p class="text-[11px] opacity-60">{t('layout.side_by_side_quiet')}</p>
							</div>
						</div>
					{:else if kind === 'strip'}
						<!--
							The strip rendering, deliberately almost empty: the fill, what it starts from,
							what 0 % and 100 % do, and the brightness only a whole strip writes. The four
							panel controls are absent because a strip is one dimension — and the page says
							that, rather than leaving a person to read the absence as a fault.
						-->
						<div class="flex flex-col gap-2 rounded border px-3 py-3 text-sm">
							<p>{t('layout.strip_what')}</p>

							{#if frame !== undefined && pictureOf(frame) !== ''}
								<img
									src={pictureOf(frame)}
									alt={t('layout.strip_picture_alt', { light: frame.light.name })}
									width={frame.image?.width ?? frame.width}
									height={frame.image?.height ?? frame.height}
									class="w-full max-w-[32rem] rounded border"
									style="image-rendering: pixelated;"
								/>
							{/if}

							<ul class="flex flex-col gap-1 text-xs opacity-80">
								<li>{t('layout.strip_fill', { lit: frame?.frame.lit ?? 0, total: frame?.frame.total ?? 0 })}</li>
								<li>{t('layout.strip_zero')}</li>
								<li>{t('layout.strip_full')}</li>
								<li>{brightnessOf(one)}</li>
							</ul>

							<p class="text-[11px] opacity-60">{t('layout.strip_fill_note')}</p>
							<p class="text-[11px] opacity-60">{t('layout.side_by_side_quiet')}</p>
							{#if one.overridden}
								<p class="text-[11px] opacity-60">{t('layout.strip_row_yours')}</p>
							{/if}
						</div>
					{:else}
						<p class="text-sm opacity-70">{t('layout.shape_unknown')}</p>
					{/if}
				</article>
			{/if}
		{/if}

		<div class="flex flex-col gap-1 text-xs opacity-70">
			<p>{t('layout.note_store')}</p>
			<p>{t('layout.note_reset')}</p>
			{#if (board.unknown_layouts ?? []).length > 0}
				<p>{t('layout.unknown_layouts', { list: (board.unknown_layouts ?? []).join(', ') })}</p>
			{/if}
		</div>
	{/if}
</section>
