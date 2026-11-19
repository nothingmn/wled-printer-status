<script lang="ts">
	// Layout: how one binding's panel is composed — and, on a strip, why there is almost nothing to
	// compose.
	//
	// docs/product-requirements.md §2.5 gives this screen two renderings, and they are two because the
	// hardware is two:
	//
	//   - **A panel is a matrix** (256 x 64 on the operator's light), so it carries the whole composer:
	//     the bar and the band it sits in, how many rows that band is, the percentage at scale 5, 4 or
	//     3, the job's name, and whether the two take turns or sit side by side, every N seconds.
	//   - **A strip is one dimension** (48 LEDs), so it carries a fill from the left and nothing else.
	//     No bar band, no words, no layers: that is the dimension, not a missing feature. The screen
	//     says so rather than leaving a blank tab for somebody to wonder about, and it offers the four
	//     panel controls nowhere on a strip.
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
	} from './api';
	import { ageText, describe, segmentText } from './format';
	import { heldHost } from './host';
	import { addressOf, lights, loadLights, nameOf } from './lights.svelte';

	const host = heldHost();
	const api = moduleApi(host);
	const t = host.t;

	/** What one binding is being edited towards: the eight keys the route writes. */
	interface Draft {
		bar: boolean;
		bar_band: 'top' | 'bottom';
		bar_rows: number;
		percent: boolean;
		percent_scale: 3 | 4 | 5;
		name: boolean;
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
	const NAME_ROWS = 14;
	const SIDE_DIGITS_SCALE = 4;
	const SIDE_DIGITS_SCALE_SHORT = 3;
	const SIDE_BAND_FOR_FULL = 28;

	/** The four reasons a frame is about a job in progress, which is when a panel composes at all. */
	const JOB_REASONS = ['progress', 'starting', 'paused-frozen', 'progress-unknown'];

	/** The light this page is scoped to, by the name these routes use: its address, no scheme. */
	let chosen = $state('');
	/** The light's bindings and their layouts, as the route last answered. */
	let board = $state<LayoutsBody | null>(null);
	/** One frame per binding, which is the only thing that knows a binding's shape and height. */
	let frames = $state<Frame[]>([]);
	/** Each binding's draft, keyed by the binding's id — the same key the route writes by. */
	let drafts = $state<Record<string, Draft>>({});
	/** One refusal per binding, answered where it was made. */
	let faults = $state<Record<string, string>>({});
	let loading = $state(true);
	let reading = $state(false);
	let busy = $state(false);
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
		rotation: one.rotation,
		rotate_seconds: one.rotate_seconds,
	});

	const draftAt = (one: Layout): Draft => drafts[one.id] ?? draftOf(one);

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

	/**
	 * Whether a draft fits the panel it is pointed at, and the sentence for either answer.
	 *
	 * This is `bandsOn` and `beside` restated: a bar that leaves fewer rows than one glyph, a pinned
	 * scale whose digits are taller than the band, and — side by side — the digits capped at four (or
	 * three on a short band) beside a name fourteen rows tall, which must all fit on one line. It is
	 * arithmetic on the height the frame reports, so it is skipped when that height is unknown.
	 */
	const fitOf = (draft: Draft, height: number): { fits: boolean; said: string } => {
		const rows = barRows(draft);
		const left = textRows(draft, height);

		const band = t('layout.fit_band', { height, rows, left });
		const digits = t('layout.fit_digits', { scale: draft.percent_scale, rows: draft.percent_scale * GLYPH_ROWS });
		const drawn = draft.percent || draft.name;

		if (rows >= height) {
			return { fits: false, said: t('layout.fit_no_room', { rows, height }) };
		}

		if (drawn && left < GLYPH_ROWS) {
			return { fits: false, said: t('layout.fit_no_text', { rows, left, glyph: GLYPH_ROWS }) };
		}

		if (draft.percent && draft.percent_scale * GLYPH_ROWS > left) {
			return { fits: false, said: t('layout.fit_scale', { band, digits, left, scale: draft.percent_scale }) };
		}

		if (draft.rotation === 'side-by-side' && draft.percent && draft.name) {
			const cap = left < SIDE_BAND_FOR_FULL ? SIDE_DIGITS_SCALE_SHORT : SIDE_DIGITS_SCALE;
			const beside = Math.min(draft.percent_scale, cap) * GLYPH_ROWS;

			if (Math.max(beside, NAME_ROWS) > left) {
				return {
					fits: false,
					said: t('layout.fit_beside', { scale: Math.min(draft.percent_scale, cap), digits: beside, name: NAME_ROWS, left }),
				};
			}
		}

		return { fits: true, said: t('layout.fit_ok', { band, left }) };
	};

	/**
	 * The picture's boxes to scale, for the sketch under the controls.
	 *
	 * It draws what the draft *is* — the band, the text band, the digits' box and the name's box — and
	 * not a rendering of the module's glyphs, so it is the arithmetic a person can see. A box taller
	 * than the band it belongs in is drawn at its true height, which is the point of showing it: that
	 * is the picture that will not fit, and the module refuses it rather than cropping it.
	 */
	const sketchOf = (draft: Draft, width: number, height: number): {
		barTop: number;
		barRows: number;
		textTop: number;
		textRows: number;
		digitsX: number;
		digitsRows: number;
		digitsWidth: number;
		nameX: number;
		nameWidth: number;
	} => {
		const rows = barRows(draft);
		const band = { barTop: 0, textTop: 0 };

		if (rows > 0) {
			if (draft.bar_band === 'top') band.textTop = rows;
			else band.barTop = height - rows;
		}

		const left = height - rows;
		const cap = left < SIDE_BAND_FOR_FULL ? SIDE_DIGITS_SCALE_SHORT : SIDE_DIGITS_SCALE;
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
			digitsX: beside ? 10 : Math.max(0, (width - digitsWidth) / 2),
			digitsRows,
			digitsWidth,
			nameX,
			nameWidth: beside ? Math.max(0, width - nameX - 4) : width,
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

	/** Reads one light's bindings, their layouts and the frames drawn for them. */
	const openLight = async (light: string): Promise<void> => {
		chosen = light;
		board = null;
		drafts = {};
		faults = {};
		notice = '';
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
	 * Writes one binding's draft.
	 *
	 * Every key goes, because the route replaces the row whole: a body that left `percent_scale` out
	 * would put it back to the default while the operator watched a control stay where it was. A
	 * draft that does not fit is refused here, before the route is asked, because the module would
	 * draw nothing for it and a saved row that draws nothing is what "refused, not cropped" forbids.
	 */
	const commit = async (one: Layout): Promise<void> => {
		const draft = drafts[one.id];
		if (draft === undefined) return;

		delete faults[one.id];
		notice = '';

		// The route's own fixed limits, refused here so the sentence is beside the control rather
		// than in a toast: a bar is 1 to 48 rows of a 64-row panel, and a rotation of no seconds is
		// not a rotation.
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

		const body: LayoutRequest = {
			bar: draft.bar,
			bar_band: draft.bar_band,
			bar_rows: draft.bar_rows,
			percent: draft.percent,
			percent_scale: draft.percent_scale,
			name: draft.name,
			remaining: false,
			rotation: draft.rotation,
			rotate_seconds: draft.rotate_seconds,
		};

		busy = true;

		try {
			const answer = await api.changeLayout(one.id, body);
			withLayout(answer.layout);
			drafts[one.id] = draftOf(answer.layout);
			notice = t('layout.saved');

			// The painter picks the row up on its next pass and GET /frames draws from the same row, so
			// the picture beside the controls is asked for again rather than left as the last layout's.
			await refreshFrames();
		} catch (problem) {
			// The route's own sentence, verbatim, beside the control that caused it.
			faults[one.id] = t('layout.refused', { why: describe(problem) });
			drafts[one.id] = draftOf(one);
		} finally {
			busy = false;
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
	const reset = async (one: Layout): Promise<void> => {
		busy = true;
		delete faults[one.id];
		notice = '';

		try {
			const answer = await api.resetLayout(one.id);
			withLayout(answer.layout);
			drafts[one.id] = draftOf(answer.layout);
			notice = answer.removed === false ? t('layout.already_default') : t('layout.reset_done');
			await refreshFrames();
		} catch (problem) {
			faults[one.id] = t('layout.refused', { why: describe(problem) });
		} finally {
			busy = false;
		}
	};

	/** One control changed: the draft takes it, and the write follows. */
	const edit = (one: Layout, change: Partial<Draft>): void => {
		drafts[one.id] = { ...draftAt(one), ...change };
		void commit(one);
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
			draft.rotation !== held.rotation ||
			draft.rotate_seconds !== held.rotate_seconds
		);
	};

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

		<!--
			One card per binding, and the card is whichever of the two renderings the hardware asks
			for. The shape comes from the frame the module has drawn, because that is the only route
			that knows it: a light that has never had a frame drawn has no shape to show, and guessing
			one would offer a composer to a strip.
		-->
		{#each rows as one (one.id)}
			{@const draft = draftAt(one)}
			{@const kind = kindOf(one)}
			{@const frame = frameOf(one)}
			{@const height = heightOf(one)}
			{@const fit = kind === 'panel' && height > 0 ? fitOf(draft, height) : null}
			{@const sketch = kind === 'panel' && height > 0 ? sketchOf(draft, frame?.width ?? 256, height) : null}

			<article class="flex flex-col gap-3 rounded border p-3" data-binding={one.id} data-kind={kind}>
				<div class="flex flex-wrap items-center gap-2">
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
						{#if one.overridden}
							<Button variant="ghost" size="sm" disabled={busy} onclick={() => void reset(one)}>
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
						The composer. Every control writes exactly one of the eight keys, and every one
						carries the line that says whether the value in force is yours or the default.
					-->
					<div class="grid gap-3 md:grid-cols-2">
						<div class="flex flex-col gap-2">
							<label class="flex items-center gap-2 text-sm">
								<input
									type="checkbox"
									class="h-4 w-4"
									checked={draft.bar}
									disabled={busy}
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
									disabled={busy || !draft.bar}
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
									disabled={busy || !draft.bar}
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
									disabled={busy}
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
									disabled={busy || !draft.percent}
									onchange={(event) =>
										edit(one, { percent_scale: Number((event.currentTarget as HTMLSelectElement).value) as 3 | 4 | 5 })}
								>
									{#each SCALES as scale (scale)}
										<option value={String(scale)}>{t('layout.scale', { scale })}</option>
									{/each}
								</select>
							</label>

							<label class="flex items-center gap-2 text-sm">
								<input
									type="checkbox"
									class="h-4 w-4"
									checked={draft.name}
									disabled={busy}
									onchange={(event) => edit(one, { name: (event.currentTarget as HTMLInputElement).checked })}
								/>
								<span class="font-medium">{t('layout.name')}</span>
								<span class="text-[10px] opacity-60">{one.name_origin === 'store' ? t('layout.yours') : t('layout.default')}</span>
							</label>
						</div>
					</div>

					<div class="flex flex-wrap items-end gap-3 text-sm">
						<label class="flex flex-col gap-1">
							<span class="text-xs opacity-70">
								{t('layout.rotation')}
								<span class="text-[10px]">{one.rotation_origin === 'store' ? t('layout.yours') : t('layout.default')}</span>
							</span>
							<select
								class="rounded border px-2 py-1"
								value={draft.rotation}
								disabled={busy}
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
								disabled={busy || draft.rotation === 'side-by-side'}
								onchange={(event) => edit(one, { rotate_seconds: Number((event.currentTarget as HTMLInputElement).value) })}
							/>
						</label>

						<label class="flex items-center gap-2 pb-1 text-sm opacity-60" title={t('layout.remaining_note')}>
							<input type="checkbox" class="h-4 w-4" checked={false} disabled />
							<span>{t('layout.remaining')}</span>
						</label>

						{#if dirty(one)}
							<Button variant="link" size="sm" disabled={busy} onclick={() => (drafts[one.id] = draftOf(one))}>
								{t('layout.discard')}
							</Button>
						{/if}
					</div>

					{#if draft.rotation === 'side-by-side'}
						<p class="text-[11px] opacity-70">{one.rotation_note ?? t('layout.side_by_side_what')}</p>
					{/if}

					<p class="text-[11px] opacity-60">{t('layout.remaining_note')}</p>

					<!-- The arithmetic, and the verdict that follows from it. -->
					<div class="rounded border px-2 py-2 text-xs {fit !== null && !fit.fits ? 'border-2' : ''}">
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
						<p class="mt-1 {fit !== null && !fit.fits ? 'font-medium' : 'opacity-70'}" role={fit !== null && !fit.fits ? 'alert' : undefined}>
							{fit === null ? t('layout.fit_unknown') : fit.said}
						</p>

						{#if sketch !== null}
							<svg
								class="mt-2 w-full max-w-[32rem]"
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
										y={sketch.textTop + Math.max(0, (sketch.textRows - sketch.digitsRows) / 2)}
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
										y={sketch.textTop + Math.max(0, (sketch.textRows - 14) / 2)}
										width={sketch.nameWidth}
										height="14"
										fill="none"
										stroke="currentColor"
										stroke-dasharray="2 3"
										stroke-opacity="0.6"
									/>
								{/if}
							</svg>
							<p class="mt-1 text-[10px] opacity-60">{t('layout.sketch_caption')}</p>
						{/if}
					</div>

					{#if frame !== undefined}
						<div class="flex flex-wrap items-end gap-3">
							{#if pictureOf(frame) !== ''}
								<img
									src={pictureOf(frame)}
									alt={t('layout.picture_alt')}
									class="w-48 rounded border"
									style="image-rendering: pixelated;"
								/>
							{/if}
							<div class="flex flex-col gap-1 text-[11px] opacity-70">
								<span>{t('layout.picture_caption')}</span>
								<span>{t('layout.picture_drawn', { age: ageText(t, frame.reading.observed_at) ?? t('common.just_now') })}</span>
								{#if !aboutAJob(frame)}
									<p>{t('layout.picture_no_job')}</p>
								{/if}
								{#if frame.note !== undefined && frame.note !== ''}
									<p>{frame.note}</p>
								{/if}
							</div>
						</div>
					{/if}

					<p class="text-[11px] opacity-60">{t('layout.side_by_side_quiet')}</p>
				{:else if kind === 'strip'}
					<!--
						The strip rendering, deliberately almost empty: the fill, what it starts from, what
						0 % and 100 % do, and the brightness only a whole strip writes. The four panel
						controls are absent because a strip is one dimension — and the page says that,
						rather than leaving a person to read the absence as a fault.
					-->
					<div class="flex flex-col gap-2 text-sm">
						<p>{t('layout.strip_what')}</p>

						{#if frame !== undefined && pictureOf(frame) !== ''}
							<img
								src={pictureOf(frame)}
								alt={t('layout.strip_picture_alt', { light: frame.light.name })}
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
		{/each}

		{#if rows.length === 0}
			<p class="text-sm opacity-70">{t('layout.no_bindings')}</p>
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
