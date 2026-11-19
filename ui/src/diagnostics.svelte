<script lang="ts">
	// Diagnostics: why the light is doing that.
	//
	// docs/product-requirements.md §2.7, in the decisions this page is built around:
	//
	//   - **Three switchable situations**: printing, finished-then-quiet, and printing-but-the-
	//     light-is-not-answering. They are a *lens*, not three pages: the tiles, the frame, the
	//     timeline and the reason table below are always the live answer for the binding chosen at
	//     the top, and the lens decides what that answer is being *read for*. The last one exists to
	//     separate *what the module decided* — a reason, a picture — from *what landed* — the last
	//     write and its outcome. Neither number is invented for a situation: a page that drew three
	//     canned scenarios would be showing a light the operator does not have, which is the one
	//     thing a diagnostic screen must never do.
	//   - **The four tiles answer one question each**, and every one of them comes from GET /frames:
	//     the printer and how old its reading is, the last write and whether it landed, the reason
	//     with its plain meaning, and the transport.
	//   - **The frame itself is the module's own PNG**, the same picture Overview draws: this screen's
	//     whole point is *what is being sent right now*, and a second renderer on the page would be a
	//     second answer to that.
	//   - **Every reason the module can give**, in the order the module's own rules are applied, with
	//     the current one marked — so "why is it green" has an answer, and the reasons *not* on the
	//     frame say what is not happening.
	//   - **One merged timeline**, carrying only what this module did. §2.7 decides it is mirrored from
	//     the host's log and that there is no second scrollback — and this build serves no log or
	//     timeline route (openapi.json carries no history path at all). So the page shows
	//     the instants the module *does* report, in order, and says plainly that they are all of them.
	//     It does not invent lines, and it does not scrape the container's log from a browser.
	//   - **The honest limits**, stated on the page rather than left for somebody to discover.
	//
	// The reason table and the limits are reference, not the present tense, so both sit behind
	// a collapsed disclosure (the disclosure primitive): the screen opens on the four tiles and the
	// picture, which is what it is for, and the prose is one press away.
	//
	// Two things that are deliberately not here: the mockup's `Copy a diagnostic bundle`, which its own
	// design record marks `needs API` (there is no route, and a button that gathers nothing is worse
	// than no button), and the light-side entries of the mockup's feed — §2.7 locks the timeline to
	// "only what this module did", and changes made elsewhere on the controller are not followed.
	//
	// **The skin.** The three situations are the button primitive in its pressed and resting shapes
	// (they were pills, which the guide keeps for nothing), the four tiles and the picture and the
	// timeline are panels, a state or an outcome is a chip, and every second line is the quiet token.
	import { onMount } from 'svelte';

	import { moduleApi, type Frame } from './api';
	import { ageText, cssColour, describe, lengthText, lightName, segmentText } from './format';
	import { heldHost } from './host';
	import { lights, loadLights } from './lights.svelte';
	import { fateOf, refreshPauses } from './pause.svelte';
	import PrinterLink from './printer-link.svelte';
	import { Button, Chip, Disclosure, Field, FieldSelect, Panel } from './ui';

	const host = heldHost();
	const api = moduleApi(host);
	const t = host.t;

	/** A printer's name, worn as a link to its own page: the shape the Lights screen gives a light. */
	const NAME_LINK = 'underline decoration-dotted underline-offset-2 hover:decoration-solid';

	/** The three situations §2.7 switches between. */
	type Situation = 'printing' | 'finished' | 'lost';

	/** The three, in the order the chips offer them. */
	const SITUATIONS: Situation[] = ['printing', 'finished', 'lost'];

	/**
	 * The reasons that mean a job is on the bed, which is the module's own set (`aboutAJob` in
	 * layout.go). Written once here because two things on this page ask it: whether the picture is
	 * composed — and so sent as pixels — and which of the three situations is in force.
	 */
	const JOB_REASONS = ['progress', 'starting', 'paused-frozen', 'progress-unknown'];

	/**
	 * Every reason the module can give, in the order the module applies its rules (render.Decide):
	 * the first one that matches is the one on the frame, so reading down this table is reading the
	 * ladder. The list is the module's, not the page's: `render.Reason` carries exactly these eight.
	 */
	const REASONS: { id: string; key: string }[] = [
		{ id: 'disconnected', key: 'diagnostics.reason_disconnected' },
		{ id: 'stale', key: 'diagnostics.reason_stale' },
		{ id: 'unknown-state', key: 'diagnostics.reason_unknown_state' },
		{ id: 'not-a-live-job', key: 'diagnostics.reason_not_a_live_job' },
		{ id: 'progress-unknown', key: 'diagnostics.reason_progress_unknown' },
		{ id: 'progress', key: 'diagnostics.reason_progress' },
		{ id: 'paused-frozen', key: 'diagnostics.reason_paused_frozen' },
		{ id: 'starting', key: 'diagnostics.reason_starting' },
	];

	/** The honest limits §2.7 asks for, each as a statement and its own explanation. */
	const LIMITS: { key: string; why: string }[] = [
		{ key: 'diagnostics.limit_strip_word', why: 'diagnostics.limit_strip_word_why' },
		{ key: 'diagnostics.limit_quiet', why: 'diagnostics.limit_quiet_why' },
		{ key: 'diagnostics.limit_elsewhere', why: 'diagnostics.limit_elsewhere_why' },
		{ key: 'diagnostics.limit_pixels', why: 'diagnostics.limit_pixels_why' },
		{ key: 'diagnostics.limit_history', why: 'diagnostics.limit_history_why' },
	];

	/** Every picture, one per binding, as GET /frames last answered. */
	let frames = $state<Frame[]>([]);
	/** The sentence GET /frames carries about what a picture is, which belongs on this page too. */
	let pictureNote = $state('');
	/** Why there is nothing to diagnose yet, when there is nothing. */
	let waiting = $state('');
	/** The binding this page is about: the light and the printer on it. */
	let chosen = $state('');
	/** The situation being explained. */
	let lens = $state<Situation>('printing');
	/** True once the operator has chosen a lens, so a refresh stops moving it under him. */
	let pickedLens = $state(false);
	let loading = $state(true);
	let failure = $state('');

	/** The frame this page is about: the chosen binding, or the first one the module answered. */
	const selected = $derived(frames.find((one) => one.binding === chosen) ?? frames[0] ?? null);

	/** A light's name as a frame gives it: the address without the scheme. */
	const lightOf = (frame: Frame): string =>
		frame.light.name !== '' ? frame.light.name : lightName(frame.light.address);

	/** What a person calls the printer, falling back to the id the host gave it. */
	const printerOf = (frame: Frame): string => (frame.printer.name !== '' ? frame.printer.name : frame.printer.id);

	/** One segment in the light's own words, from the light's own list when it answered. */
	const segmentOf = (frame: Frame): string => {
		if (frame.light.segment < 0) return t('common.whole_light');

		const light = lights.list.find((one) => (one.controller?.name ?? '') === lightOf(frame));
		const segment = light?.segments.find((one) => one.id === frame.light.segment);

		return segmentText(t, frame.light.segment, segment?.name, segment);
	};

	/** The binding at the top's own line: the light, the segment and the printer on it. */
	const bindingText = (frame: Frame): string => `${lightOf(frame)} · ${segmentOf(frame)} · ${printerOf(frame)}`;

	/**
	 * Which of the three situations the module's own answer is in, and empty when it is in none of
	 * them — an idle printer whose last write landed is not a broken variant of these three, and
	 * saying it was would be the page inventing a state.
	 */
	const situationOf = (frame: Frame): Situation | '' => {
		const aJob = JOB_REASONS.includes(frame.frame.reason);
		const failed = frame.last_send !== null && frame.last_send.ok === false;

		if (aJob && failed) return 'lost';
		if (!aJob && frame.reading?.stale === true) return 'finished';
		if (aJob) return 'printing';

		return '';
	};

	/** The situation the module reports now, or empty when it reports none of the three. */
	const inForce = $derived(selected === null ? '' : situationOf(selected));

	/** One situation's name, in the operator's own words. */
	const situationLabel = (one: string): string => {
		if (one === 'printing') return t('diagnostics.situation_printing');
		if (one === 'lost') return t('diagnostics.situation_lost');

		return t('diagnostics.situation_finished');
	};

	/**
	 * What the chosen lens tells a reader to look at. Static prose about the module's own behaviour —
	 * no figure in it is measured, and each sentence is a fact from the module's design record rather
	 * than a claim about the answer above.
	 */
	const readingOf = (one: Situation): string => {
		if (one === 'printing') return t('diagnostics.read_printing');
		if (one === 'lost') return t('diagnostics.read_lost');

		return t('diagnostics.read_finished');
	};

	/** Whether a tile is one the chosen lens is pointing at, which is only a highlight. */
	const inLens = (tile: 1 | 2 | 3): boolean => {
		if (lens === 'lost') return tile === 2 || tile === 3;

		return tile === 1 || tile === 3;
	};

	/** A frame's colour as the route spells it: three bytes. */
	const colourName = (frame: Frame): string =>
		`${frame.frame.colour.r},${frame.frame.colour.g},${frame.frame.colour.b}`;

	/** The caption under the picture: the colour, the fill and the reason it carries. */
	const fillText = (frame: Frame): string => {
		if (frame.frame.total <= 0)
			return t('diagnostics.frame_no_total', { colour: colourName(frame), reason: frame.frame.reason });

		return t('diagnostics.frame_caption', {
			colour: colourName(frame),
			lit: frame.frame.lit,
			total: frame.frame.total,
			reason: frame.frame.reason,
		});
	};

	/**
	 * Whether the picture above goes as pixels over DDP. It is the module's own rule — a panel, a
	 * layout that draws something of its own, and a frame about a job in progress — applied to what
	 * the routes carry; the layout is the third fact and is not on this route, so the tile states the
	 * rule instead of claiming to have watched the wire.
	 */
	const pixelsNow = $derived(
		selected !== null && selected.kind === 'panel' && JOB_REASONS.includes(selected.frame.reason),
	);

	/** The plain meaning of the reason on the frame, or null when this page does not know it. */
	const meaningOf = $derived(REASONS.find((one) => selected !== null && one.id === selected.frame.reason) ?? null);

	/** How long is left of this binding's own pause, in words, and empty when it is not paused. */
	const pausedFor = $derived.by(() => {
		if (selected === null) return '';

		const fate = fateOf({ light: lightOf(selected), segment: selected.light.segment });
		if (fate === undefined || !fate.paused) return '';

		const left = fate.until === undefined ? undefined : (Date.parse(fate.until) - Date.now()) / 1000;

		return lengthText(t, left, fate.forever === true);
	});

	/** One line of the merged timeline. */
	interface Line {
		/** The instant the module reports, and empty for a standing fact rather than one. */
		at: string;
		/** How long ago that was, in words. */
		when: string;
		source: 'printer' | 'module';
		text: string;
		bad: boolean;
	}

	/**
	 * The timeline, built from the instants the module actually reports for this binding: the
	 * printer's last word, the reading the picture was decided from, and the last write with its
	 * outcome — plus the pause, which is a standing fact and carries no instant of its own.
	 *
	 * There is no fourth source and no past: this build serves no log route, and every line below is
	 * a value on GET /frames or GET /pause. The page says so under the list instead of filling it.
	 */
	const timeline = $derived.by<Line[]>(() => {
		const frame = selected;
		if (frame === null) return [];

		const lines: Line[] = [];
		const state = frame.printer.state_known ? frame.printer.state : '';
		const reported =
			state === '' ? t('diagnostics.timeline_said_nothing') : t('diagnostics.timeline_reported', { state });

		lines.push({
			at: frame.reading?.last_change ?? '',
			when: ageText(t, frame.reading?.last_change) ?? '',
			source: 'printer',
			text: frame.reading?.stale === true ? `${reported}${t('diagnostics.timeline_and_stale')}` : reported,
			bad: false,
		});

		lines.push({
			at: frame.reading?.observed_at ?? '',
			when: ageText(t, frame.reading?.observed_at) ?? '',
			source: 'module',
			text: t('diagnostics.timeline_read', { reason: frame.frame.reason }),
			bad: false,
		});

		if (frame.last_send !== null) {
			lines.push({
				at: frame.last_send.at,
				when: ageText(t, frame.last_send.at) ?? '',
				source: 'module',
				text: frame.last_send.ok
					? t('diagnostics.timeline_wrote', { light: lightOf(frame) })
					: t('diagnostics.timeline_write_failed', {
							light: lightOf(frame),
							why: frame.last_send.error ?? t('diagnostics.timeline_no_reason'),
						}),
				bad: !frame.last_send.ok,
			});
		}

		if (pausedFor !== '') {
			lines.push({
				at: '',
				when: '',
				source: 'module',
				text: t('diagnostics.timeline_paused', { length: pausedFor }),
				bad: false,
			});
		}

		// Oldest first, which is how a log reads; a standing fact has no instant and sits last.
		return lines.sort((one, other) => {
			const left = one.at === '' ? Number.MAX_SAFE_INTEGER : Date.parse(one.at);
			const right = other.at === '' ? Number.MAX_SAFE_INTEGER : Date.parse(other.at);

			return (Number.isNaN(left) ? 0 : left) - (Number.isNaN(right) ? 0 : right);
		});
	});

	const load = async (quiet = false): Promise<void> => {
		if (!quiet) loading = true;

		try {
			const pictures = await api.listFrames().catch((problem: unknown) => {
				// A module that has not drawn a picture yet answers 503, which is a fact about the
				// moment rather than a failure of this screen: it is said, not thrown.
				waiting = describe(problem);

				return { frames: [] as Frame[], count: 0, note: '' };
			});

			frames = pictures.frames;
			pictureNote = pictures.note;
			if (pictures.frames.length > 0) waiting = '';

			// The chosen binding survives a refresh, and falls back to the first the module answers
			// when it has gone: a binding deleted in another tab must not leave a blank page.
			if (!frames.some((one) => one.binding === chosen)) chosen = frames[0]?.binding ?? '';

			if (!pickedLens) {
				const first = frames.find((one) => one.binding === chosen);
				const now = first === undefined ? '' : situationOf(first);

				if (now !== '') lens = now;
			}

			failure = '';
		} catch (problem) {
			// A quiet refresh that fails keeps the answer already on the page.
			if (!quiet) failure = `${t('diagnostics.load_failed')}: ${describe(problem)}`;
		} finally {
			loading = false;
		}
	};

	const choose = (one: Situation): void => {
		lens = one;
		pickedLens = true;
	};

	onMount(() => {
		void Promise.all([load(), loadLights(), refreshPauses()]);
	});

	// The present tense changes while the page is open: the reading's age moves, a write fails, a
	// print ends. Ten seconds is the cadence Overview uses, for the same reason.
	$effect(() => {
		const tick = (): void => {
			if (host.signal.aborted) return;
			void Promise.all([load(true), refreshPauses()]);
		};

		const timer = setInterval(tick, 10_000);

		return () => clearInterval(timer);
	});
</script>

<section class="flex flex-col gap-section">
	<header class="flex flex-wrap items-end gap-section">
		<div class="flex flex-col gap-tight">
			<h1 class="font-ui text-lg font-medium">{t('diagnostics.title')}</h1>
			<p class="font-ui text-sm text-wled-text-quiet">{t('diagnostics.intro')}</p>
		</div>

		<Field id="diagnostics-binding" class="ml-auto" label={t('diagnostics.binding')}>
			{#snippet children()}
				<FieldSelect
					id="diagnostics-binding"
					value={chosen}
					disabled={loading || frames.length === 0}
					oninput={(value) => (chosen = value)}
				>
					{#snippet children()}
						{#each frames as frame (frame.binding)}
							<option value={frame.binding} selected={frame.binding === chosen}>{bindingText(frame)}</option>
						{/each}
					{/snippet}
				</FieldSelect>
			{/snippet}
		</Field>
	</header>

	{#if failure !== ''}
		<Panel>
			<p class="font-ui text-sm text-wled-danger" role="alert">{failure}</p>
		</Panel>
	{/if}

	{#if loading}
		<p class="font-ui text-sm text-wled-text-quiet">{t('diagnostics.loading')}</p>
	{:else if selected === null}
		<Panel>
			<p class="font-ui text-sm font-medium">{t('diagnostics.nothing_yet')}</p>
			{#if waiting !== ''}
				<p class="mt-tight font-ui text-xs text-wled-text-quiet">{t('diagnostics.nothing_yet_why', { why: waiting })}</p>
			{/if}
		</Panel>
	{:else}
		{@const frame = selected}

		<!-- The three situations: a lens on the same screen, never a second set of numbers. -->
		<div class="flex flex-col gap-row">
			<div class="flex flex-wrap items-center gap-tight">
				<span class="pr-tight font-ui text-xs font-medium uppercase tracking-label text-wled-text-quiet">
					{t('diagnostics.situations_heading')}
				</span>
				{#each SITUATIONS as one (one)}
					<Button
						variant={lens === one ? 'secondary' : 'ghost'}
						size="sm"
						aria-pressed={lens === one}
						onclick={() => choose(one)}
					>
						{situationLabel(one)}
					</Button>
				{/each}
				{#if inForce === lens}
					<Chip>{t('diagnostics.now_happening')}</Chip>
				{/if}
				{#if pausedFor !== ''}
					<Chip>{t('diagnostics.paused_badge', { length: pausedFor })}</Chip>
				{/if}
			</div>

			<Panel>
				<p class="font-ui text-sm">
					{#if inForce === lens}
						<span class="font-medium">{t('diagnostics.now_body_same')}</span>
					{:else if inForce === ''}
						<span class="font-medium">
							{t('diagnostics.now_body_none', { situation: situationLabel(lens) })}
						</span>
					{:else}
						<span class="font-medium">
							{t('diagnostics.now_body_other', { situation: situationLabel(inForce) })}
						</span>
					{/if}
					{readingOf(lens)}
				</p>
			</Panel>
		</div>

		<!-- The four tiles: each answers one question, and all four come from GET /frames. -->
		<div class="grid gap-row sm:grid-cols-2 lg:grid-cols-4">
			<Panel class={inLens(1) ? 'ring-1 ring-wled-accent' : ''}>
				<div class="flex flex-col gap-tight">
					<p class="font-ui text-xs font-medium uppercase tracking-label text-wled-text-quiet">
						{t('diagnostics.tile_printer')}
					</p>
					<p class="font-data text-sm font-medium">
						{frame.printer.state_known ? frame.printer.state : t('diagnostics.state_unknown')}
					</p>
					<p class="font-ui text-xs text-wled-text-quiet">
						<PrinterLink id={frame.printer.id} name={printerOf(frame)} cls={NAME_LINK} />
						{#if frame.frame.percent >= 0}
							· {t('diagnostics.percent', { percent: frame.frame.percent })}
						{/if}
					</p>
					<p class="flex flex-wrap items-center gap-tight font-ui text-xs text-wled-text-quiet">
						<span>
							{frame.reading?.last_change
								? t('diagnostics.reading_age', { age: ageText(t, frame.reading.last_change) ?? '' })
								: t('diagnostics.no_reading')}
						</span>
						<Chip tone={frame.reading?.stale === true ? 'error' : 'quiet'}>
							{frame.reading?.stale === true ? t('diagnostics.stale') : t('diagnostics.live')}
						</Chip>
					</p>
				</div>
			</Panel>

			<Panel class={inLens(2) ? 'ring-1 ring-wled-accent' : ''}>
				<div class="flex flex-col gap-tight">
					<p class="font-ui text-xs font-medium uppercase tracking-label text-wled-text-quiet">
						{t('diagnostics.tile_last_write')}
					</p>
					<p class="font-ui text-sm font-medium">
						{frame.last_send === null
							? t('diagnostics.never_written')
							: (ageText(t, frame.last_send.at) ?? t('diagnostics.never_written'))}
					</p>
					<p class="flex flex-wrap items-center gap-tight font-ui text-xs">
						{#if frame.last_send !== null}
							<Chip tone={frame.last_send.ok ? 'quiet' : 'error'}>
								{frame.last_send.ok ? t('diagnostics.landed') : t('diagnostics.did_not_land')}
							</Chip>
						{:else}
							<span class="text-wled-text-quiet">{t('diagnostics.no_write_yet')}</span>
						{/if}
					</p>
					{#if frame.last_send !== null && !frame.last_send.ok && (frame.last_send.error ?? '') !== ''}
						<p class="break-words font-data text-xs text-wled-text-quiet">{frame.last_send.error}</p>
					{/if}
				</div>
			</Panel>

			<Panel class={inLens(3) ? 'ring-1 ring-wled-accent' : ''}>
				<div class="flex flex-col gap-tight">
					<p class="font-ui text-xs font-medium uppercase tracking-label text-wled-text-quiet">
						{t('diagnostics.tile_why')}
					</p>
					<p class="flex items-center gap-tight font-data text-sm font-medium">
						<span
							class="inline-block h-2.5 w-2.5 shrink-0 rounded-full border border-wled-line-field"
							style={`background: ${cssColour(frame.frame.colour)}`}
						></span>
						{frame.frame.reason}
					</p>
					<p class="font-ui text-xs text-wled-text-quiet">
						{meaningOf === null ? t('diagnostics.reason_unfamiliar') : t(meaningOf.key)}
					</p>
					{#if frame.frame.name !== ''}
						<p class="truncate font-ui text-xs text-wled-text-quiet">
							{t('diagnostics.job_name', { name: frame.frame.name })}
						</p>
					{/if}
				</div>
			</Panel>

			<Panel>
				<div class="flex flex-col gap-tight">
					<p class="font-ui text-xs font-medium uppercase tracking-label text-wled-text-quiet">
						{t('diagnostics.tile_transport')}
					</p>
					<p class="font-ui text-sm font-medium">
						{pixelsNow ? t('diagnostics.transport_pixels') : t('diagnostics.transport_state')}
					</p>
					<p class="font-ui text-xs text-wled-text-quiet">
						{pixelsNow ? t('diagnostics.transport_pixels_meaning') : t('diagnostics.transport_state_meaning')}
					</p>
					<p class="font-ui text-xs text-wled-text-quiet">{t('diagnostics.transport_rule')}</p>
				</div>
			</Panel>
		</div>

		<!-- The frame itself, the module's own PNG: the same picture Overview draws, because the
		     question this page answers is what is being sent right now. -->
		<Panel title={t('diagnostics.frame_heading')} hint={t('diagnostics.frame_route')}>
			<div class="flex flex-col gap-row">
				{#if (frame.image?.base64 ?? '') !== ''}
					<img
						src={`data:${frame.image?.media_type ?? 'image/png'};base64,${frame.image?.base64 ?? ''}`}
						alt={t('diagnostics.frame_alt', { light: lightOf(frame), printer: printerOf(frame) })}
						class={frame.kind === 'panel'
							? 'block h-auto w-full max-w-128 rounded-panel bg-wled-pixel-off object-contain [image-rendering:pixelated]'
							: 'block h-6 w-full max-w-128 rounded-panel bg-wled-pixel-off [image-rendering:pixelated]'}
					/>
				{:else}
					<div
						class="flex h-16 w-full max-w-128 items-center justify-center rounded-panel bg-wled-pixel-off px-row text-center font-ui text-xs text-wled-pixel-off-ink"
					>
						{t('diagnostics.frame_missing')}
					</div>
				{/if}
				<p class="font-data text-xs text-wled-text-quiet">{fillText(frame)}</p>
				{#if frame.note}
					<p class="font-ui text-xs text-wled-text-quiet">{frame.note}</p>
				{/if}
				{#if pictureNote !== ''}
					<p class="font-ui text-xs text-wled-text-quiet">{pictureNote}</p>
				{/if}
			</div>
		</Panel>

		<!-- One merged timeline. It carries only what this module did, and it says what it cannot. -->
		<Panel title={t('diagnostics.timeline_heading')} hint={t('diagnostics.timeline_route')} pad="none">
			{#if timeline.length === 0}
				<p class="px-panel py-panel font-ui text-sm text-wled-text-quiet">{t('diagnostics.timeline_empty')}</p>
			{:else}
				<ol>
					{#each timeline as line, index (index)}
						<li class="flex flex-wrap items-baseline gap-row border-b border-wled-line px-panel py-row font-ui text-sm last:border-b-0">
							<span class="w-24 shrink-0 font-data text-xs text-wled-text-quiet">{line.when}</span>
							<Chip>
								{line.source === 'printer'
									? t('diagnostics.source_printer')
									: t('diagnostics.source_module')}
							</Chip>
							<span class={line.bad ? 'min-w-0 flex-1 text-wled-danger' : 'min-w-0 flex-1'}>
								{line.text}
							</span>
						</li>
					{/each}
				</ol>
			{/if}

			<div class="flex flex-col gap-tight border-t border-wled-line bg-wled-sunken px-panel py-row font-ui text-xs text-wled-text-quiet">
				<p>{t('diagnostics.timeline_limit')}</p>
				<p>{t('diagnostics.timeline_no_history')}</p>
			</div>
		</Panel>

		<!-- Every reason the module can give, with the current one marked. Folded away: the
		     tiles and the picture are the present tense, and this table is the reference. -->
		<Disclosure summary={t('diagnostics.reasons_heading')} hint={t('diagnostics.reasons_route')}>
			<p class="border-b border-wled-line py-row font-ui text-xs text-wled-text-quiet">{t('diagnostics.reasons_intro')}</p>
			<table class="w-full border-collapse font-ui text-sm">
				<thead>
					<tr class="border-b border-wled-line text-left text-xs uppercase tracking-label text-wled-text-quiet">
						<th class="py-row font-medium">{t('diagnostics.reason_column')}</th>
						<th class="py-row font-medium">{t('diagnostics.means_column')}</th>
					</tr>
				</thead>
				<tbody>
					{#each REASONS as row (row.id)}
						{@const here = row.id === frame.frame.reason}
						<tr class={here ? 'border-b border-wled-line bg-wled-sunken last:border-b-0' : 'border-b border-wled-line last:border-b-0'}>
							<td class="whitespace-nowrap py-row font-data text-xs">{row.id}</td>
							<td class="py-row">
								<span class="text-wled-text-quiet">{t(row.key)}</span>
								{#if here}
									<span class="ml-tight inline-block align-middle">
										<Chip>{t('diagnostics.this_one')}</Chip>
									</span>
								{/if}
							</td>
						</tr>
					{/each}
					{#if meaningOf === null}
						<tr class="bg-wled-sunken">
							<td class="whitespace-nowrap py-row font-data text-xs">{frame.frame.reason}</td>
							<td class="py-row text-wled-text-quiet">
								{t('diagnostics.reason_unfamiliar')}
								<span class="ml-tight inline-block align-middle">
									<Chip>{t('diagnostics.this_one')}</Chip>
								</span>
							</td>
						</tr>
					{/if}
				</tbody>
			</table>
		</Disclosure>

		<!-- The honest limits, stated rather than discovered — and folded away for the same
		     reason: they are read once, and the screen opens on the picture. -->
		<Disclosure summary={t('diagnostics.limits_heading')}>
			<ul>
				{#each LIMITS as limit (limit.key)}
					<li class="flex flex-wrap items-baseline justify-between gap-row border-b border-wled-line py-row last:border-b-0">
						<span>{t(limit.key)}</span>
						<span class="font-ui text-xs text-wled-text-quiet">{t(limit.why)}</span>
					</li>
				{/each}
			</ul>
		</Disclosure>
	{/if}
</section>
