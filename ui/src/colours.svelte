<script lang="ts">
	// Colours: what each state looks like on a light, and what it says.
	//
	// docs/product-requirements.md §2.4 and Appendix A, in the decisions this page is built on:
	//
	//   - **Ten rows, each drawn twice.** Once on a panel and once on a strip, because a strip
	//     owner reading a page of panels would be reading somebody else's screen. The row set is
	//     the approved mockup's (docs/design/v2/colours/), and the operator settled the one place
	//     it differs from a plain rendering of Appendix A: BUSY and READY are not rows here, and
	//     their two places belong to the two conditions. Those two — the light unreachable, and
	//     the printer lost — carry their own colour and their own word, and are marked as
	//     conditions rather than as words the host reports, because the host has no word for
	//     either and this module computes both from its own reading.
	//
	//     The route still answers twelve rows, BUSY and READY among them, and both are still
	//     overridable through it. This page does not present them and says so on the page rather
	//     than leaving somebody to wonder where they went; nothing here deletes or rewrites
	//     either of them.
	//   - **A colour and a word per state.** The colour is Appendix A's, editable; the word is
	//     free text, which is also how a light is made to speak another language. A word beats
	//     the layout composer while it holds: a light showing a state with a word shows that word,
	//     not the bar and digits.
	//   - **A hold, forever by default.** 30 s, 60 s and 5 min are the others, and they are the
	//     route's own four values rather than a longer list this screen filters — nothing the
	//     route would accept is refused here.
	//   - **Nothing is copied in.** A stored row is an override and the row says so per value:
	//     `colour_origin`, `word_origin`, `holds_origin`, and `overridden` for whether there is
	//     anything to reset at all. A state nobody has touched holds no colour of this page's —
	//     it holds whatever the route answered, which is Appendix A's table and internal/render's
	//     code, one statement in two places.
	//   - **`word_shows` and `word_until` are read and shown.** A word set on a state that has
	//     been showing for hours is spent before anybody reads the page, and before wave 14 the
	//     answer could not say so: it was a 200 that changed nothing. So a row whose word has run
	//     out says so, a row waiting for its state says that instead, and neither looks like a row
	//     whose word is on a light right now.
	//
	// The font is the limit and the page says so: `glyphs.ts` carries internal/text's own table,
	// so the warning beside a word is the truth about what the panel would draw rather than a
	// guess at it. What this page does not carry is the fill behaviour §2.4 also makes editable:
	// there is no route for it (GET /states answers colour, word and hold and nothing else), so a
	// control here would be a control that writes nothing.
	import { onMount } from 'svelte';

	import { Badge, Button } from '@prusa/connect-ui/primitives';

	import {
		moduleApi,
		type FrameColour,
		type Holds,
		type StateRequest,
		type StateRow,
		type StatesBody,
	} from './api';
	import { describe } from './format';
	import { missingGlyphs, fitsPanel } from './glyphs';
	import { heldHost } from './host';
	import StatePreview from './state-preview.svelte';

	const host = heldHost();
	const api = moduleApi(host);
	const t = host.t;

	/** What one row is being edited towards. */
	interface Draft {
		/** The colour as a colour input spells one: `#rrggbb`. */
		colour: string;
		word: string;
		holds: Holds;
	}

	/** The four holds the route accepts, in the order it lists them. Nothing else is offered. */
	const HOLDS: Holds[] = ['forever', '30s', '60s', '5m'];

	let board = $state<StatesBody | null>(null);
	/** Each row's draft, keyed by the state's own name. */
	let drafts = $state<Record<string, Draft>>({});
	/** One refusal per row, so a refused write is answered where it was made. */
	let faults = $state<Record<string, string>>({});
	let loading = $state(true);
	let busy = $state(false);
	let failure = $state('');
	let notice = $state('');

	/** A colour as a colour input spells one. */
	const hexOf = (colour: FrameColour): string =>
		`#${[colour.r, colour.g, colour.b].map((one) => one.toString(16).padStart(2, '0')).join('')}`;

	/** A colour input's value back as three channels, clamped to what the route accepts. */
	const rgbOf = (hex: string): FrameColour => {
		const channel = (at: number): number => {
			const read = Number.parseInt(hex.slice(at, at + 2), 16);

			return Number.isNaN(read) ? 0 : Math.min(255, Math.max(0, read));
		};

		return { r: channel(1), g: channel(3), b: channel(5) };
	};

	const draftOf = (row: StateRow): Draft => ({ colour: hexOf(row.colour), word: row.word, holds: row.holds });

	const draftAt = (row: StateRow): Draft => drafts[row.state] ?? draftOf(row);

	/**
	 * The ten rows this page presents, in the approved mockup's own order.
	 *
	 * §2.4 says ten states each drawn twice, and the mockup's ten are these: the eight the host
	 * reports that a person has something to say about, and, in the place BUSY and READY would
	 * have taken, the light unreachable and the printer lost. Both of those are conditions this
	 * module computes, and both are rows the route answers with `kind: condition` — so they are
	 * editable here exactly as a state is, by their own names, `Disconnected` and `Stale`.
	 *
	 * The order is the mockup's rather than the route's, because this is the mockup's page. A row
	 * the route does not answer is left out rather than drawn empty; a row it answers that is not
	 * in this list is not presented at all, which is what the operator decided for BUSY and READY.
	 */
	const PRESENTED = [
		'PRINTING',
		'PAUSED',
		'ATTENTION',
		'FINISHED',
		'IDLE',
		'ERROR',
		'STOPPED',
		'Disconnected',
		'Stale',
		'UNKNOWN',
	] as const;

	/** The rows, in that order, as the route answered them. */
	const rows = $derived(
		PRESENTED.map((name) =>
			(board?.states ?? []).find((one) => one.state.toLowerCase() === name.toLowerCase()),
		).filter((one): one is StateRow => one !== undefined),
	);

	/** How many of the rows are states the host reports, and how many this module computes. */
	const reportedCount = $derived(rows.filter((one) => one.kind === 'state').length);
	const conditionCount = $derived(rows.filter((one) => one.kind === 'condition').length);

	/**
	 * The mockup's own words for the two conditions, beside the route's names for them.
	 *
	 * The route's name is what a write addresses — `Disconnected`, `Stale` — and the mockup's is
	 * what a person read on the page he approved. Showing both is the only way to be
	 * addressable and recognisable at once.
	 */
	const asideOf = (state: string): string => {
		if (state === 'Disconnected') return t('colours.condition_unreachable');
		if (state === 'Stale') return t('colours.condition_lost');

		return '';
	};

	/**
	 * What the panel is showing behind the preview.
	 *
	 * An expired word is not on any light, so the preview is the colour alone — that is what a
	 * person walking past sees. A word that has been typed but not yet written is shown, because
	 * the next write is the one being made.
	 */
	const previewWord = (row: StateRow): string => {
		const word = draftAt(row).word.trim();
		if (word === '') return '';

		return word === row.word.trim() && row.word_shows === 'expired' ? '' : word;
	};

	/** What the font would make of this word: a character it does not hold, or a word too wide. */
	const wordTrouble = (word: string): string => {
		if (word.trim() === '') return '';

		const missing = missingGlyphs(word);
		if (missing.length > 0) return t('colours.not_drawable', { chars: missing.join(' ') });
		if (!fitsPanel(word)) return t('colours.too_wide', { word: word.trim().toUpperCase() });

		return '';
	};

	/**
	 * What the row's word is doing, in the answer's own three cases plus the one that is not a
	 * case at all: a state carrying no word.
	 */
	const standing = (row: StateRow): { badge: string; line: string } => {
		if (row.word.trim() === '') return { badge: '', line: t('colours.no_word') };

		if (row.word_shows === 'showing') {
			const until = row.word_until ?? '';
			if (until === '') return { badge: t('colours.showing'), line: t('colours.showing_forever') };

			const at = Date.parse(until);

			return {
				badge: t('colours.showing'),
				line: Number.isNaN(at)
					? t('colours.showing_forever')
					: t('colours.showing_until', {
							when: new Date(at).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }),
						}),
			};
		}

		if (row.word_shows === 'expired') return { badge: t('colours.expired_short'), line: t('colours.expired_why') };

		if (row.word_shows === 'later') return { badge: t('colours.later_short'), line: t('colours.later_why') };

		return { badge: '', line: t('colours.no_word') };
	};

	/** A row whose word is set but not on any light does not look like one that is. */
	const notShowing = (row: StateRow): boolean =>
		row.word.trim() !== '' && (row.word_shows === 'expired' || row.word_shows === 'later');

	/** The board with one row replaced, so a write's answer lands without a second read. */
	const withRow = (held: StatesBody | null, row: StateRow): StatesBody | null => {
		if (held === null) return null;

		return { ...held, states: held.states.map((one) => (one.state === row.state ? row : one)) };
	};

	const load = async (): Promise<void> => {
		loading = true;
		failure = '';

		try {
			adopt(await api.listStates());
		} catch (problem) {
			failure = `${t('colours.load_failed')}: ${describe(problem)}`;
		} finally {
			loading = false;
		}
	};

	/**
	 * Takes a fresh answer into the page without throwing away what is being typed.
	 *
	 * The table is re-read while the page is open because `word_shows` and `word_until` are the
	 * answer's own reading of the clock: a hold that runs out while somebody is looking at the
	 * page has to stop saying "showing now" on its own, or the page is describing a light it has
	 * stopped asking about. A row whose draft no longer matches what the store said is being
	 * edited, so it keeps what was typed and takes nothing.
	 */
	const adopt = (answer: StatesBody): void => {
		const before = board;
		board = answer;

		if (before === null) {
			drafts = Object.fromEntries(answer.states.map((one) => [one.state, draftOf(one)]));

			return;
		}

		const was = new Map(before.states.map((one) => [one.state, one]));

		for (const row of answer.states) {
			const old = was.get(row.state);
			const draft = drafts[row.state];

			const untouched =
				old === undefined ||
				draft === undefined ||
				(draft.colour === hexOf(old.colour) && draft.word.trim() === old.word && draft.holds === old.holds);

			if (untouched) drafts[row.state] = draftOf(row);
		}
	};

	/** A quiet re-read: the table as it stands, and nothing said about it. */
	const refresh = async (): Promise<void> => {
		try {
			adopt(await api.listStates());
		} catch {
			// A page that has a table keeps it. The next edit or the next tick will try again.
		}
	};

	/**
	 * Writes one row. The body carries the colour on every write, and the word and hold only when
	 * there is a word — the route replaces the row whole, so a colour change that left the word
	 * out would silently delete it, and a hold with no word to hold is a value with no meaning.
	 */
	const commit = async (row: StateRow): Promise<void> => {
		const draft = drafts[row.state];
		if (draft === undefined) return;

		const word = draft.word.trim();
		const body: StateRequest = { colour: rgbOf(draft.colour) };

		if (word !== '') {
			body.word = word;
			body.holds = draft.holds;
		}

		busy = true;
		delete faults[row.state];

		try {
			const answer = await api.changeState(row.state, body);
			board = withRow(board, answer.state);
			drafts[row.state] = draftOf(answer.state);
		} catch (problem) {
			faults[row.state] = describe(problem);
		} finally {
			busy = false;
		}
	};

	/** Puts one row back to Appendix A's colour, no word, and a hold of forever. */
	const reset = async (row: StateRow): Promise<void> => {
		busy = true;
		delete faults[row.state];
		notice = '';

		try {
			const answer = await api.resetState(row.state);
			board = withRow(board, answer.state);
			drafts[row.state] = draftOf(answer.state);
			notice = answer.removed ? t('colours.reset_done', { state: row.state }) : t('colours.already_default', { state: row.state });
		} catch (problem) {
			faults[row.state] = describe(problem);
		} finally {
			busy = false;
		}
	};

	/** One hold in words. Four literals rather than a built key, so the check can see them. */
	const holdsLabel = (holds: Holds): string => {
		if (holds === 'forever') return t('colours.hold_forever');
		if (holds === '30s') return t('colours.hold_30s');
		if (holds === '60s') return t('colours.hold_60s');

		return t('colours.hold_5m');
	};

	onMount(() => {
		void load();

		// The word's standing is the route's reading of the clock, so the table is asked again
		// while the page is open. Nothing is written by this and nothing is said about it.
		const ticker = setInterval(() => void refresh(), 30_000);

		return () => clearInterval(ticker);
	});
</script>

<section class="flex flex-col gap-4">
	<header class="flex flex-col gap-1">
		<h1 class="text-lg font-semibold">{t('colours.title')}</h1>
		<p class="text-sm opacity-70">{t('colours.intro')}</p>
	</header>

	<p class="rounded border px-3 py-2 text-sm">{t('colours.note')}</p>

	{#if failure !== ''}
		<div class="rounded border px-3 py-2 text-sm" role="alert">{failure}</div>
	{/if}
	{#if notice !== ''}
		<div class="rounded border px-3 py-2 text-sm" role="status">{notice}</div>
	{/if}

	{#if loading}
		<p class="text-sm opacity-70">{t('colours.loading')}</p>
	{:else if board !== null}
		{#if !board.persisted}
			<p class="rounded border px-3 py-2 text-sm" role="status">
				{t('colours.not_kept', { why: board.note ?? '' })}
			</p>
		{/if}

		<!--
			One card per row, drawn twice: a colour, an editable word, the hold that word carries,
			and where each of the three came from. The card is a snippet because the two conditions
			are the same row as the ten states — the route counts them apart, and the page says so
			rather than offering them a second kind of control.
		-->
		{#snippet card(row: StateRow)}
			{@const draft = draftAt(row)}
			{@const colour = rgbOf(draft.colour)}
			{@const told = standing(row)}
			{@const trouble = wordTrouble(draft.word)}

			<article class="flex flex-col gap-2 rounded border p-3 {notShowing(row) ? 'border-dashed' : ''}">
				<div class="flex flex-wrap items-center gap-2">
					<input
						type="color"
						class="h-6 w-9 cursor-pointer rounded border bg-transparent p-0"
						value={draft.colour}
						disabled={busy}
						aria-label={t('colours.colour_of', { state: row.state })}
						onchange={(event) => {
							drafts[row.state] = { ...draft, colour: (event.currentTarget as HTMLInputElement).value };
							void commit(row);
						}}
					/>
					<span class="text-sm font-semibold">{row.state}</span>
					{#if row.kind === 'condition'}
						<Badge variant="outline" size="sm">{t('colours.condition')}</Badge>
						<span class="text-[10px] opacity-60">{asideOf(row.state)}</span>
					{/if}
					<span class="font-mono text-[10px] opacity-60">
						{colour.r}, {colour.g}, {colour.b}
						{#if row.colour_origin === 'store'}· {t('colours.yours')}{/if}
					</span>
					<div class="ml-auto flex flex-wrap items-center gap-1">
						<Badge variant={row.overridden ? 'secondary' : 'outline'} size="sm">
							{row.overridden ? t('colours.yours') : t('colours.default')}
						</Badge>
						{#if row.overridden}
							<Button variant="ghost" size="sm" disabled={busy} onclick={() => void reset(row)}>
								{t('colours.reset')}
							</Button>
						{/if}
					</div>
				</div>

				<StatePreview {colour} word={previewWord(row)} pending={row.word_shows === 'later'} />

				<div class="flex flex-wrap items-end gap-3 text-sm">
					<label class="flex flex-col gap-1">
						<span class="text-xs opacity-70">
							{t('colours.word')}
							{#if row.word_origin === 'store'}· {t('colours.yours')}{/if}
						</span>
						<input
							type="text"
							class="w-40 rounded border px-2 py-1"
							value={draft.word}
							placeholder={t('colours.word_placeholder')}
							disabled={busy}
							onchange={(event) => {
								drafts[row.state] = { ...draft, word: (event.currentTarget as HTMLInputElement).value };
								void commit(row);
							}}
						/>
					</label>

					<label class="flex flex-col gap-1">
						<span class="text-xs opacity-70">
							{t('colours.holds')}
							{#if row.holds_origin === 'store'}· {t('colours.yours')}{/if}
						</span>
						<select
							class="rounded border px-2 py-1"
							value={draft.holds}
							disabled={busy || draft.word.trim() === ''}
							onchange={(event) => {
								drafts[row.state] = { ...draft, holds: (event.currentTarget as HTMLSelectElement).value as Holds };
								void commit(row);
							}}
						>
							{#each HOLDS as holds (holds)}
								<option value={holds}>{holdsLabel(holds)}</option>
							{/each}
						</select>
					</label>
				</div>

				{#if draft.word.trim() === ''}
					<p class="text-[11px] opacity-60">{t('colours.holds_needs_word')}</p>
				{:else if trouble !== ''}
					<p class="text-[11px]" role="alert">{trouble}</p>
				{/if}

				<div class="flex flex-wrap items-center gap-2 text-xs">
					{#if told.badge !== ''}
						<Badge variant={row.word_shows === 'showing' ? 'secondary' : 'warning'} size="sm">{told.badge}</Badge>
					{/if}
					<span class="opacity-70">{told.line}</span>
				</div>

				<p class="text-[11px] opacity-60">{row.meaning}</p>

				{#if (faults[row.state] ?? '') !== ''}
					<p class="text-[11px]" role="alert">{t('colours.refused')} {faults[row.state]}</p>
				{/if}
			</article>
		{/snippet}

		<div class="overflow-hidden rounded border">
			<div class="flex flex-wrap items-center gap-2 border-b bg-black/5 px-3 py-2 dark:bg-white/10">
				<span class="text-sm font-medium">{t('colours.rows_heading')}</span>
				<span class="ml-auto text-xs opacity-60">
					{t('colours.counts', { states: reportedCount, conditions: conditionCount })}
				</span>
			</div>

			<div class="grid gap-3 p-3 md:grid-cols-2">
				{#each rows as row (row.state)}
					{@render card(row)}
				{/each}
			</div>

			<p class="border-t px-3 py-2 text-xs opacity-70">{t('colours.busy_ready_note')}</p>
		</div>

		<div class="flex flex-col gap-1 text-xs opacity-70">
			<p>{t('colours.conditions_note')}</p>
			<p>{t('colours.font_note')}</p>
			<p>{t('colours.kept_note')}</p>
			{#if (board.unknown_states ?? []).length > 0}
				<p>{t('colours.unknown_states', { list: (board.unknown_states ?? []).join(', ') })}</p>
			{/if}
		</div>
	{/if}
</section>
