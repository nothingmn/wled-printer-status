<script lang="ts">
	// Events: what one light shows when something *happens*.
	//
	// docs/product-requirements.md §2.3, in the five decisions the page is built around:
	//
	//   - **The page is scoped to one light**, by the selector at the top. A moment is a
	//     transition — a print finishing, a pause, a light lost — and the list of them belongs to
	//     the light, so a page covering two lights would be two tables pretending to be one.
	//   - **The moment is the top row.** One printer on the light is exactly the flat table.
	//     More than one printer and each moment keeps its row and gains one indented sub-row per
	//     printer, carrying that printer's segment and its own four settings. Settings belong to
	//     the (light, printer) pair, which is also what makes multi-room sane: the same printer on
	//     two lights carries two independent sets.
	//   - **`shows` decides what `which` is.** Preset names one of the light's own presets, Word is a
	//     word for a panel, Colour names one of the twelve states, and Nothing names nothing. The field
	//     changes with the choice, and the route refuses a `which` that does not belong to its `shows`
	//     — so the refusal is shown where the field is rather than swallowed.
	//   - **A preset is picked from the light's own list**, read from `GET /devices/{name}/presets` when
	//     the page opens: slot and name, nothing else. The list is the only way to name a preset that
	//     the light can actually be asked about, and a name typed into a box is a name nobody can
	//     check. A light that cannot be read answers `presets_known: false` with the reason, and the
	//     field falls back to a text box that still works — because a light that is unplugged must not
	//     stop somebody editing. The fallback keeps the typed value, says why, and stores it as written.
	//     A slot with no name is not offered: a moment names a preset by its name, and an unnamed slot
	//     has none.
	//   - **No preset gallery, no preset editor, no effect, speed or palette control anywhere.** Presets
	//     are made and tuned in WLED's own interface; this page only names one.
	//   - **Editing never needs the light.** PUT and DELETE write this module's own store and send
	//     nothing to the light — which is exactly right, because the moment somebody stands at a
	//     light editing moments is often the moment the light is the thing that is off.
	//
	// Two things this page cannot offer, both reported rather than faked: the mockup's per-row `test`
	// button (no route runs one moment on one light, and the preview routes paint a whole light, which
	// an editor must not do), and a preset's body (the route carries slot and name only, and the body
	// belongs to WLED's own interface).
	//
	// **The skin.** The moment table is a panel with no inner padding so its rows keep the compact
	// rhythm; every select, text box and number box in a row is the field control, every stamp is a
	// chip, and the actions are the button primitive (`link` for a discard, which is an action that
	// must not compete with the row's own value).
	import { onMount } from 'svelte';

	import {
		moduleApi,
		type Frame,
		type Moment,
		type MomentEntry,
		type MomentRequest,
		type MomentsBody,
		type PresetsBody,
		type Shows,
		type StateRow,
	} from './api';
	import { describe, lightName, segmentText } from './format';
	import { fitsPanel, missingGlyphs } from './glyphs';
	import { heldHost } from './host';
	import { addressOf, lights, loadLights, nameOf } from './lights.svelte';
	import PrinterLink from './printer-link.svelte';
	import { Button, Chip, Field, FieldInput, FieldSelect, Panel } from './ui';

	const host = heldHost();
	const api = moduleApi(host);
	const t = host.t;

	/** A printer's name, worn as a link to its own page: the shape the Lights screen gives a light. */
	const NAME_LINK = 'underline decoration-dotted underline-offset-2 hover:decoration-solid';

	/** What one row is being edited towards: the entry the route would be sent. */
	interface Draft {
		enabled: boolean;
		shows: Shows;
		which: string;
		for: string;
	}

	/** The lengths the field suggests. The route accepts any positive duration, so this is a hint. */
	const LENGTH_HINTS = ['forever', '10s', '30s', '60s', '2m', '5m', '15m', '1h'] as const;

	/** The light this page is scoped to, by the name these routes use: its address, no scheme. */
	let chosen = $state('');
	/** The light's moment table as the route last answered it. */
	let board = $state<MomentsBody | null>(null);
	/** The state table's own names, so a Colour can only name a colour the module draws. */
	let states = $state<StateRow[]>([]);
	/** The chosen light's own preset table, and why it could not be read when it could not. */
	let presets = $state<PresetsBody | null>(null);
	let presetsWhy = $state('');
	/** One shape per light, learned from the frame the module has drawn: only a frame knows it. */
	let shapes = $state<Record<string, 'panel' | 'strip'>>({});
	/** Each row's draft, keyed `moment|printer`, which is the same key the route writes by. */
	let drafts = $state<Record<string, Draft>>({});
	/** One refusal per row, so a bad `which` is answered beside the field that caused it. */
	let faults = $state<Record<string, string>>({});
	let loading = $state(true);
	let reading = $state(false);
	let busy = $state(false);
	let failure = $state('');
	let notice = $state('');

	/** The key of one row: the (light, printer) moment this page edits. */
	const rowKey = (moment: string, printer: string): string => `${moment}|${printer}`;

	/** One entry as a draft to edit. */
	const draftOf = (entry: MomentEntry): Draft => ({
		enabled: entry.enabled,
		shows: entry.shows,
		which: entry.which,
		for: entry.for,
	});

	/** One row's draft, or the entry's own values before anything was typed. */
	const draftAt = (moment: string, entry: MomentEntry): Draft => drafts[rowKey(moment, entry.printer)] ?? draftOf(entry);

	/** Whether a draft says enough for the route to accept it, by its own `shows`. */
	const complete = (draft: Draft): boolean => {
		if (draft.shows === 'Nothing') return draft.which.trim() === '';
		if (draft.shows === 'Colour') return states.some((one) => one.state === draft.which);

		return draft.which.trim() !== '';
	};

	/** Whether a row has been edited away from what the store holds. */
	const dirty = (moment: string, entry: MomentEntry): boolean => {
		const draft = drafts[rowKey(moment, entry.printer)];
		if (draft === undefined) return false;

		return (
			draft.enabled !== entry.enabled ||
			draft.shows !== entry.shows ||
			draft.which.trim() !== entry.which ||
			draft.for.trim() !== entry.for
		);
	};

	/** This light's shape, once a frame has told us, and empty before that. */
	const shapeOf = (light: string): 'panel' | 'strip' | '' => shapes[light] ?? '';

	/**
	 * Whether `which` for a Preset can be picked rather than typed.
	 *
	 * It can when the light answered: `presets_known` is the route saying this list is the light's own.
	 * It cannot when the light is unplugged or the route is not on this build, and then the field is a
	 * text box — a light that cannot be asked must not stop somebody editing.
	 */
	const canPick = $derived(presets !== null && presets.presets_known);

	/**
	 * The presets a moment can name: the named ones.
	 *
	 * A moment's `which` is matched on the preset's name, so a slot the light never named can be run
	 * but cannot be named here. It is left out rather than offered as a number that would never match.
	 */
	const namedPresets = $derived((presets?.presets ?? []).filter((one) => !one.unnamed && one.name.trim() !== ''));

	/** Whether this light carries more than one printer, which is what the two table shapes turn on. */
	const manyPrinters = $derived((board?.printers.length ?? 0) > 1);

	/** How many of the light's slots carry no name, which is worth a line when there are any. */
	const unnamedCount = $derived((presets?.presets ?? []).filter((one) => one.unnamed || one.name.trim() === '').length);

	/** One segment in the light's own words: its name, its number and the run it covers. */
	const segmentOf = (segment: number): string => {
		const found = lights.list.find((one) => nameOf(one) === chosen)?.segments.find((one) => one.id === segment);

		return segmentText(t, segment, found?.name, found);
	};

	/** The shows this light is offered. §2.3: a word is panel only, so a strip is not offered one. */
	const offers = (entry: MomentEntry): Shows[] => {
		const all: Shows[] =
			shapeOf(chosen) === 'strip' ? ['Preset', 'Colour', 'Nothing'] : ['Preset', 'Word', 'Colour', 'Nothing'];

		// A stored Word on a strip is shown and not hidden: the route accepts it and the light
		// draws its colour instead, and a select with nothing selected would be a row a person
		// cannot read.
		if (!all.includes(entry.shows)) all.splice(1, 0, entry.shows);

		return all;
	};

	/**
	 * What one `shows` is called. Written out as four literals rather than built from the value,
	 * so that the catalogue check can see every key this screen names.
	 */
	const showsLabel = (shows: Shows): string => {
		if (shows === 'Preset') return t('events.shows_preset');
		if (shows === 'Word') return t('events.shows_word');
		if (shows === 'Colour') return t('events.shows_colour');

		return t('events.shows_nothing');
	};

	/**
	 * Whether a word would reach the panel at all: the font's own glyphs, and the scale of five
	 * the module draws at. The route accepts the word either way — the font is not its business —
	 * so this is a warning and not a refusal, and it says what the light will do instead.
	 */
	const wordTrouble = (word: string): string => {
		if (word.trim() === '') return '';

		const missing = missingGlyphs(word);
		if (missing.length > 0) return t('events.word_undrawable', { chars: missing.join(' ') });
		if (!fitsPanel(word)) return t('events.word_too_wide', { word: word.trim().toUpperCase() });

		return '';
	};

	/** The board with one entry replaced, so a write's answer lands without a second read. */
	const withEntry = (held: MomentsBody | null, moment: string, entry: MomentEntry): MomentsBody | null => {
		if (held === null) return null;

		return {
			...held,
			moments: held.moments.map((one) =>
				one.moment === moment
					? { ...one, entries: one.entries.map((had) => (had.printer === entry.printer ? entry : had)) }
					: one,
			),
		};
	};

	/** Reads one light's table and seeds a draft per row. */
	const openLight = async (light: string): Promise<void> => {
		chosen = light;
		board = null;
		presets = null;
		presetsWhy = '';
		drafts = {};
		faults = {};
		notice = '';
		reading = true;

		try {
			const answer = await api.listMoments(light);
			board = answer;
			drafts = Object.fromEntries(
				answer.moments.flatMap((one) =>
					one.entries.map((entry) => [rowKey(one.moment, entry.printer), draftOf(entry)]),
				),
			);
			failure = '';

			// The light's own presets, so a Preset's `which` is picked rather than typed. A light that
			// cannot be read — or a build that does not serve the route — leaves the list empty and the
			// reason beside it, and the field stays a text box. The table itself is not a failure of
			// this page: editing a moment never needs the light.
			try {
				presets = await api.listPresets(light);
			} catch (problem) {
				presets = null;
				presetsWhy = describe(problem);
			}
		} catch (problem) {
			failure = `${t('events.load_failed')}: ${describe(problem)}`;
		} finally {
			reading = false;
		}
	};

	const load = async (): Promise<void> => {
		loading = true;
		failure = '';

		try {
			await loadLights();

			// Two facts this page needs that the moment table does not carry: the state table's
			// names (a Colour names one of them) and each light's shape — only a frame knows
			// whether a light is a panel, and a strip is not offered a word. Both are reads, and
			// both are allowed to fail without failing the page.
			const [table, pictures] = await Promise.all([
				api.listStates().catch(() => ({ states: [] as StateRow[], count: 0, conditions: 0, persisted: false })),
				api.listFrames().catch(() => ({ frames: [] as Frame[], count: 0, note: '' })),
			]);

			states = table.states;
			shapes = Object.fromEntries(
				pictures.frames.map((frame) => [
					frame.light.name !== '' ? frame.light.name : lightName(frame.light.address),
					frame.kind,
				]),
			);

			const first = lights.list[0];
			if (first !== undefined) await openLight(nameOf(first));
		} catch (problem) {
			failure = `${t('events.load_failed')}: ${describe(problem)}`;
		} finally {
			loading = false;
		}
	};

	/** Writes one row's draft. Nothing is sent to the light; this is the module's own store. */
	const commit = async (moment: string, entry: MomentEntry): Promise<void> => {
		const key = rowKey(moment, entry.printer);
		const draft = drafts[key];
		if (draft === undefined || !complete(draft)) return;

		const body: MomentRequest = {
			// Named on every write, even on a light carrying one printer, so a row can never land
			// on a printer the operator did not point at.
			printer: entry.printer,
			enabled: draft.enabled,
			shows: draft.shows,
			which: draft.which.trim(),
			for: draft.for.trim(),
		};

		busy = true;
		delete faults[key];
		notice = '';

		try {
			const answer = await api.changeMoment(chosen, moment, body);
			board = withEntry(board, moment, answer.moment);
			drafts[key] = draftOf(answer.moment);
		} catch (problem) {
			// The draft stays as it was typed, with the route's own sentence beside it, so the
			// field is fixed rather than retyped.
			faults[key] = describe(problem);
		} finally {
			busy = false;
		}
	};

	/** Puts one row back to the default a delete restores. */
	const reset = async (moment: string, entry: MomentEntry): Promise<void> => {
		const key = rowKey(moment, entry.printer);

		busy = true;
		delete faults[key];
		notice = '';

		try {
			const answer = await api.resetMoment(chosen, moment, entry.printer);
			board = withEntry(board, moment, answer.moment);
			drafts[key] = draftOf(answer.moment);
			notice = answer.removed ? t('events.reset_done') : t('events.was_default');
		} catch (problem) {
			faults[key] = describe(problem);
		} finally {
			busy = false;
		}
	};

	/** Throws a row's draft away and starts again from what the store holds. */
	const discard = (moment: string, entry: MomentEntry): void => {
		drafts[rowKey(moment, entry.printer)] = draftOf(entry);
		delete faults[rowKey(moment, entry.printer)];
	};

	/** `shows` changed, and `which` changes with it: it means nothing on its own. */
	const chooseShows = (moment: string, entry: MomentEntry, shows: Shows): void => {
		const draft = drafts[rowKey(moment, entry.printer)];
		if (draft === undefined) return;

		const named = states.some((one) => one.state === draft.which);

		draft.shows = shows;

		// Nothing names nothing; a Colour is one of the twelve by name, so it starts on one that
		// exists; and a Word or a Preset is whatever the operator writes, so a leftover state name
		// is not carried into it as though it were one.
		if (shows === 'Nothing') draft.which = '';
		else if (shows === 'Colour') draft.which = named ? draft.which : (states[0]?.state ?? '');
		else if (named) draft.which = '';

		void commit(moment, entry);
	};

	onMount(() => {
		void load();
	});
</script>

<section class="flex flex-col gap-section">
	<header class="flex flex-wrap items-end gap-section">
		<div class="flex flex-col gap-tight">
			<h1 class="font-ui text-lg font-medium">{t('events.title')}</h1>
			<p class="font-ui text-sm text-wled-text-quiet">{t('events.intro')}</p>
		</div>

		<Field id="events-light" class="ml-auto" label={t('events.light')}>
			{#snippet children()}
				<FieldSelect
					id="events-light"
					value={chosen}
					disabled={loading || lights.list.length === 0}
					oninput={(value) => void openLight(value)}
				>
					{#snippet children()}
						{#each lights.list as one (addressOf(one))}
							<option value={nameOf(one)} selected={nameOf(one) === chosen}>
								{one.controller?.name ?? addressOf(one)}
							</option>
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
	{#if notice !== ''}
		<Panel>
			<p class="font-ui text-sm" role="status">{notice}</p>
		</Panel>
	{/if}

	{#if loading}
		<p class="font-ui text-sm text-wled-text-quiet">{t('events.loading')}</p>
	{:else if lights.list.length === 0}
		<Panel>
			<p class="font-ui text-sm font-medium">{t('events.no_lights')}</p>
		</Panel>
	{:else if board !== null}
		<Panel>
			<p class="font-ui text-sm">
				{#if manyPrinters}
					<span class="font-medium">{t('events.many_printers', { count: board.printers.length })}</span>
				{:else}
					<span class="font-medium">{t('events.one_printer')}</span>
				{/if}
				{t('events.presets_are_theirs')}
				{#if shapeOf(chosen) === 'strip'}
					{t('events.strip_no_word')}
				{:else if shapeOf(chosen) === ''}
					{t('events.shape_unknown')}
				{/if}
			</p>
		</Panel>

		{#if !board.persisted}
			<Panel>
				<p class="font-ui text-sm" role="status">
					{t('events.not_kept', { why: board.note ?? '' })}
				</p>
			</Panel>
		{/if}

		<!--
			One snippet for the four settings and the actions, because the flat table and the
			indented sub-rows carry exactly the same controls: two copies would be two answers to
			what a row holds. The leading cell differs — a moment, or a printer and its segment —
			and belongs to the table, not here.
		-->
		{#snippet controls(one: Moment, entry: MomentEntry)}
			{@const draft = draftAt(one.moment, entry)}
			{@const who = `${one.label} · ${entry.printer_name ?? entry.printer}`}
			{@const key = rowKey(one.moment, entry.printer)}

			<td class="px-row py-row align-top">
				<input
					type="checkbox"
					class="h-4 w-4 align-middle accent-wled-accent-fill"
					checked={draft.enabled}
					disabled={busy}
					aria-label={t('events.on_for', { moment: who })}
					onchange={(event) => {
						drafts[key] = {
							...draft,
							enabled: (event.currentTarget as HTMLInputElement).checked,
						};
						void commit(one.moment, entry);
					}}
				/>
			</td>

			<td class="px-row py-row align-top">
				<FieldSelect
					value={draft.shows}
					disabled={busy}
					aria-label={t('events.shows_for', { moment: who })}
					oninput={(value) => chooseShows(one.moment, entry, value as Shows)}
				>
					{#snippet children()}
						{#each offers(entry) as shows (shows)}
							<option value={shows} selected={shows === draft.shows}>{showsLabel(shows)}</option>
						{/each}
					{/snippet}
				</FieldSelect>
			</td>

			<td class="px-row py-row align-top">
				{#if draft.shows === 'Preset'}
					<div class="flex flex-col gap-tight">
						{#if canPick}
							<!-- Picked from the light's own table, so the name is one the light can be asked for. -->
							<FieldSelect
								value={draft.which}
								disabled={busy}
								aria-label={t('events.which_for', { moment: who })}
								oninput={(value) => {
									drafts[key] = { ...draft, which: value };
									void commit(one.moment, entry);
								}}
							>
								{#snippet children()}
									<option value="">{t('events.pick_preset')}</option>
									{#each namedPresets as preset (preset.slot)}
										<option value={preset.name} selected={preset.name === draft.which}>{preset.name}</option>
									{/each}
									{#if draft.which.trim() !== '' && !namedPresets.some((one) => one.name === draft.which.trim())}
										<option value={draft.which} selected>
											{t('events.preset_not_on_light', { name: draft.which })}
										</option>
									{/if}
								{/snippet}
							</FieldSelect>
							<span class="font-ui text-xs text-wled-text-quiet">
								{t('events.presets_read', { count: namedPresets.length })}
							</span>
							{#if unnamedCount > 0}
								<span class="font-ui text-xs text-wled-text-quiet">
									{t('events.presets_unnamed_hidden', { count: unnamedCount })}
								</span>
							{/if}
							{#if namedPresets.length === 0 && draft.which.trim() === ''}
								<span class="font-ui text-xs text-wled-danger" role="alert">{t('events.presets_none_yet')}</span>
							{/if}
						{:else}
							<!-- The list could not be read. The box still works, and the reason is beside it. -->
							<FieldInput
								class="w-40"
								type="text"
								value={draft.which}
								placeholder={t('events.preset_placeholder')}
								disabled={busy}
								aria-label={t('events.which_for', { moment: who })}
								onchange={(event) => {
									drafts[key] = {
										...draft,
										which: (event.currentTarget as HTMLInputElement).value,
									};
									void commit(one.moment, entry);
								}}
							/>
							<span class="font-ui text-xs text-wled-text-quiet">
								{t('events.presets_typed', {
									why: presetsWhy !== '' ? presetsWhy : (presets?.presets_unread ?? presets?.note ?? ''),
								})}
							</span>
						{/if}
					</div>
				{:else if draft.shows === 'Word'}
					<div class="flex flex-col gap-tight">
						<FieldInput
							class="w-40"
							type="text"
							value={draft.which}
							placeholder={t('events.word_placeholder')}
							disabled={busy}
							aria-label={t('events.which_for', { moment: who })}
							onchange={(event) => {
								drafts[key] = {
									...draft,
									which: (event.currentTarget as HTMLInputElement).value,
								};
								void commit(one.moment, entry);
							}}
						/>
						<span class="font-ui text-xs text-wled-text-quiet">{t('events.word_panel_only')}</span>
						{#if wordTrouble(draft.which) !== ''}
							<span class="font-ui text-xs text-wled-danger" role="alert">{wordTrouble(draft.which)}</span>
						{/if}
					</div>
				{:else if draft.shows === 'Colour'}
					<FieldSelect
						value={draft.which}
						disabled={busy}
						aria-label={t('events.which_for', { moment: who })}
						oninput={(value) => {
							drafts[key] = { ...draft, which: value };
							void commit(one.moment, entry);
						}}
					>
						{#snippet children()}
							{#each states as colour (colour.state)}
								<option value={colour.state} selected={colour.state === draft.which}>{colour.state}</option>
							{/each}
						{/snippet}
					</FieldSelect>
				{:else}
					<span class="font-ui text-xs text-wled-text-quiet">{t('events.nothing_names_nothing')}</span>
				{/if}
			</td>

			<td class="px-row py-row align-top">
				<FieldInput
					class="w-24 font-data"
					type="text"
					value={draft.for}
					list="moment-lengths"
					disabled={busy}
					aria-label={t('events.for_for', { moment: who })}
					onchange={(event) => {
						drafts[key] = {
							...draft,
							for: (event.currentTarget as HTMLInputElement).value,
						};
						void commit(one.moment, entry);
					}}
				/>
			</td>

			<td class="px-row py-row text-right align-top">
				<div class="flex flex-wrap items-center justify-end gap-tight">
					<Chip>
						{entry.origin === 'store' ? t('events.yours') : t('events.default')}
					</Chip>
					{#if dirty(one.moment, entry) && complete(draft)}
						<Button variant="ghost" size="sm" disabled={busy} onclick={() => void commit(one.moment, entry)}>
							{t('events.save')}
						</Button>
					{/if}
					{#if dirty(one.moment, entry)}
						<Button variant="link" size="sm" disabled={busy} onclick={() => discard(one.moment, entry)}>
							{t('events.discard')}
						</Button>
					{/if}
					{#if entry.origin === 'store'}
						<Button variant="ghost" size="sm" disabled={busy} onclick={() => void reset(one.moment, entry)}>
							{t('events.reset')}
						</Button>
					{/if}
				</div>
			</td>
		{/snippet}

		<Panel title={t('events.moments')} pad="none">
			{#snippet meta()}
				<span class="font-ui text-xs text-wled-text-quiet">
					{manyPrinters ? t('events.each_moment_then_printers') : t('events.one_row_per_moment')}
				</span>
			{/snippet}

			<div class="overflow-x-auto">
				{#if manyPrinters}
					<table class="w-full min-w-200 border-collapse text-left font-ui text-sm">
						<thead>
							<tr class="border-b border-wled-line text-xs uppercase tracking-label text-wled-text-quiet">
								<th class="px-row py-row font-medium">{t('events.moment')}</th>
								<th class="px-row py-row font-medium">{t('events.printer_segment')}</th>
								<th class="px-row py-row font-medium">{t('events.on')}</th>
								<th class="px-row py-row font-medium">{t('events.shows')}</th>
								<th class="px-row py-row font-medium">{t('events.which')}</th>
								<th class="px-row py-row font-medium">{t('events.for')}</th>
								<th class="px-row py-row"></th>
							</tr>
						</thead>
						<tbody>
							{#each board.moments as one (one.moment)}
								<tr class="border-b border-wled-line bg-wled-sunken">
									<td colspan="7" class="px-row py-row">
										<span class="text-sm font-medium">{one.label}</span>
										<span class="ml-row text-xs text-wled-text-quiet">{one.meaning}</span>
										<span class="ml-row text-xs text-wled-text-quiet">
											· {t('events.printer_count', { count: one.printer_count })}
										</span>
									</td>
								</tr>
								{#each one.entries as entry (entry.printer)}
									<tr class="border-b border-wled-line">
										<td colspan="2" class="px-row py-row pl-panel align-top">
											<span class="font-medium"
												>{#if entry.printer_name}<PrinterLink
														id={entry.printer}
														name={entry.printer_name}
														cls={NAME_LINK}
													/>{:else}{entry.printer}{/if}</span
												>
											<span class="block font-data text-xs text-wled-text-quiet">
												↳ {segmentOf(entry.segment)}
											</span>
										</td>
										{@render controls(one, entry)}
									</tr>
									{#if (faults[rowKey(one.moment, entry.printer)] ?? '') !== ''}
										<tr class="border-b border-wled-line">
											<td colspan="7" class="px-row py-row pl-panel text-xs">
												<span class="text-wled-danger" role="alert">
													{t('events.refused')} {faults[rowKey(one.moment, entry.printer)]}
												</span>
											</td>
										</tr>
									{/if}
								{/each}
							{/each}
						</tbody>
					</table>
				{:else}
					<!-- One printer: exactly the flat table §2.3 asks for, with no sub-rows. -->
					<table class="w-full min-w-192 border-collapse text-left font-ui text-sm">
						<thead>
							<tr class="border-b border-wled-line text-xs uppercase tracking-label text-wled-text-quiet">
								<th class="px-row py-row font-medium">{t('events.moment')}</th>
								<th class="px-row py-row font-medium">{t('events.on')}</th>
								<th class="px-row py-row font-medium">{t('events.shows')}</th>
								<th class="px-row py-row font-medium">{t('events.which')}</th>
								<th class="px-row py-row font-medium">{t('events.for')}</th>
								<th class="px-row py-row"></th>
							</tr>
						</thead>
						<tbody>
							{#each board.moments as one (one.moment)}
								{#each one.entries as entry (entry.printer)}
									<tr class="border-b border-wled-line last:border-b-0">
										<td class="px-row py-row align-top">
											<span class="font-medium">{one.label}</span>
											<span class="block text-xs text-wled-text-quiet">{one.meaning}</span>
										</td>
										{@render controls(one, entry)}
									</tr>
									{#if (faults[rowKey(one.moment, entry.printer)] ?? '') !== ''}
										<tr class="border-b border-wled-line last:border-b-0">
											<td colspan="6" class="px-row py-row text-xs">
												<span class="text-wled-danger" role="alert">
													{t('events.refused')} {faults[rowKey(one.moment, entry.printer)]}
												</span>
											</td>
										</tr>
									{/if}
								{/each}
							{/each}
						</tbody>
					</table>
				{/if}
			</div>

			<div class="border-t border-wled-line bg-wled-sunken px-panel py-row font-ui text-xs text-wled-text-quiet">
				{t('events.footnote')}
			</div>
		</Panel>

		<datalist id="moment-lengths">
			{#each LENGTH_HINTS as hint (hint)}
				<option value={hint}></option>
			{/each}
		</datalist>

		<div class="flex flex-col gap-tight font-ui text-xs text-wled-text-quiet">
			<p>{t('events.for_forever')}</p>
			<p>{t('events.preset_not_checked')}</p>
			<p>{t('events.kept_without_light')}</p>
			{#if (board.unknown_moments ?? []).length > 0}
				<p>{t('events.unknown_moments', { list: (board.unknown_moments ?? []).join(', ') })}</p>
			{/if}
		</div>
	{:else if reading}
		<p class="font-ui text-sm text-wled-text-quiet">{t('events.reading')}</p>
	{/if}
</section>
