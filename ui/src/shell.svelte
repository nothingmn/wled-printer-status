<script lang="ts">
	// The module's one tab, and the rail the eight screens hang off.
	//
	// The host registers a *tab* on the module's own page under Settings, Extensions
	// (ui.contributes.settings in module.yaml names it `bindings`), so this is the
	// module's whole browser half: one registration, and each screen is a view inside
	// it. The tab rail is ours because the host gives a module no router; there is no
	// URL per screen, no history and nothing to synchronise.
	//
	// §3 of the requirements, in one line each:
	//
	//   - responsive is required, because it is used from a phone, and the rail becomes
	//     a top strip on a narrow screen: one `<nav>`, a row until `md` and a column
	//     after it, and the strip scrolls sideways rather than wrapping;
	//   - `Pause everything` sits in the header of every tab, because it is a panic
	//     button and not a page feature: it is drawn here, once, above whichever screen
	//     is showing, so no screen has to remember to carry it;
	//   - `Refresh everything` sits immediately to the right of it, drawn here for the
	//     same reason -- the operator asked for it "on all screens" -- and it is the one
	//     control in this module whose answer is a list: the route writes every binding
	//     the module drives and says what each light did with it, so the panel below the
	//     header is one row per light and per segment and never a single "done".
	//
	// The shell's root element is also where the module says which colour scheme its
	// controls are in (scheme.ts). `color-scheme` inherits, so one declaration here
	// reaches every select, input and textarea on every screen -- which is what makes an
	// open dropdown's own list readable in the dark theme instead of white on white.
	//
	// It is also where the module's tokens are established: `wled-ui` is the root the
	// token layer hangs on (tokens.css), so every screen below it inherits the host's
	// own values and follows the host's theme, including a flip while the page is open.
	// Nothing here writes a colour, a radius or a spacing value of its own, and no
	// control is drawn twice: the rail is the tab-bar primitive, the header's buttons
	// are the button primitive, and the two banners are panels.
	import { onMount } from 'svelte';

	import { lengthText, segmentText } from './format';
	import { heldHost } from './host';
	import { loadCalledNames } from './lights.svelte';
	import {
		LENGTHS,
		everythingPaused,
		globalPause,
		pauses,
		pauseEverything,
		refreshPauses,
		resumeEverything,
	} from './pause.svelte';
	import { hideRefresh, outcomeKey, refresh, refreshEverything, wroteKey } from './refresh.svelte';
	import { colourScheme } from './scheme';
	import { SCREENS } from './screens';
	import { Button, Panel, Tabs } from './ui';
	import LightLink from './light-link.svelte';

	const host = heldHost();
	const t = host.t;

	/** The screen showing. Overview opens first, which is the one every visit starts on. */
	let current = $state<string>(SCREENS[0].id);
	/** Whether the six lengths are on offer. */
	let menu = $state(false);

	const paused = $derived(everythingPaused());
	const global = $derived(globalPause());
	const left = $derived(global === null ? '' : lengthText(t, global.remaining_seconds, global.forever));

	/** The six screens as the rail wants them: an id to compare, and the words to show. */
	const rail = $derived(SCREENS.map((screen) => ({ id: screen.id, label: t(screen.key) })));

	/** The failure as the screen says it: its own sentence, in the reader's language. */
	const failure = $derived(
		pauses.failure === null ? '' : `${t(pauses.failure.key)}: ${pauses.failure.problem}`,
	);

	/** The refresh's refusal, in the same shape and the same place. */
	const refreshFailure = $derived(
		refresh.failure === null ? '' : `${t(refresh.failure.key)}: ${refresh.failure.problem}`,
	);

	/** What the sweep did, in one line: how many of how many took the picture. */
	const sweep = $derived(
		refresh.view === null
			? ''
			: t('refresh.summary', {
					refreshed: refresh.view.refreshed,
					count: refresh.view.count,
					skipped: refresh.view.skipped,
					failed: refresh.view.failed,
				}),
	);

	/** One row's outcome in the tone it is drawn in: a write, a leaving alone, or a failure. */
	const outcomeTone = (outcome: string): string =>
		outcome === 'refreshed'
			? 'text-wled-text'
			: outcome === 'skipped'
				? 'text-wled-text-quiet'
				: 'text-wled-danger';

	const stopFor = async (length: (typeof LENGTHS)[number]['id']): Promise<void> => {
		menu = false;
		await pauseEverything(length);
	};

	onMount(() => {
		void refreshPauses();
		// The names lights call themselves, so a refresh's rows name a light the way every other
		// screen does: `morpheus (http://10.0.0.136)`. A read that fails leaves the address standing.
		void loadCalledNames();
	});
</script>

<section
	class="wled-ui flex flex-col gap-section bg-wled-canvas p-page font-ui text-sm text-wled-text"
	use:colourScheme
>
	<div class="flex flex-col gap-section md:flex-row md:items-start">
		<!-- The rail. A top strip on a phone that scrolls sideways; a column from md,
		     with the accent rule moved to its leading edge. -->
		<Tabs
			label={t('shell.screens')}
			items={rail}
			active={current}
			orientation="rail"
			onSelect={(id) => (current = id)}
			class="md:w-40 md:shrink-0"
		/>

		<div class="flex min-w-0 flex-1 flex-col gap-section">
			<!-- The header's two buttons: the panic button §3 puts on every tab, and the refresh
			     immediately to its right, which the operator asked for on all screens. -->
			<div class="flex flex-wrap items-center gap-row">
				{#if paused}
					<Button variant="secondary" disabled={pauses.busy} onclick={() => void resumeEverything()}>
						{t('shell.resume_now')}
					</Button>
				{:else}
					<Button
						variant="secondary"
						disabled={pauses.busy}
						aria-expanded={menu}
						onclick={() => (menu = !menu)}
					>
						{t('shell.pause_everything')}
					</Button>
				{/if}
				<!-- The busy state is the control's own words, and `disabled` is what makes a second
				     press in the same frame impossible from this screen; a press that arrives anyway
				     — another window, a page not yet told — is refused by the route (REFRESH_BUSY). -->
				<Button
					variant="secondary"
					disabled={refresh.busy}
					aria-busy={refresh.busy}
					onclick={() => void refreshEverything()}
				>
					{refresh.busy ? t('shell.refreshing') : t('shell.refresh_everything')}
				</Button>
				{#if failure !== ''}
					<span class="text-xs text-wled-danger" role="alert">{failure}</span>
				{/if}
				{#if refreshFailure !== ''}
					<span class="text-xs text-wled-danger" role="alert">{refreshFailure}</span>
				{/if}
			</div>

			{#if menu}
				<Panel>
					<div class="flex flex-wrap items-center gap-row">
						<span class="pr-1 text-xs text-wled-text-quiet">{t('shell.stop_writing_for')}</span>
						{#each LENGTHS as length (length.id)}
							<Button variant="ghost" disabled={pauses.busy} onclick={() => void stopFor(length.id)}>
								{t(length.key)}
							</Button>
						{/each}
						<Button variant="ghost" onclick={() => (menu = false)}>{t('shell.cancel')}</Button>
					</div>
				</Panel>
			{/if}

			{#if paused}
				<Panel>
					<div class="flex flex-wrap items-center gap-row" role="status">
						<span class="font-medium">{t('shell.paused_heading')}</span>
						<span class="text-wled-text-quiet">{t('shell.paused_banner', { length: left })}</span>
					</div>
				</Panel>
			{/if}

			<!-- The sweep's answer: one row per light and per segment, never a single "done". A light
			     left alone or unanswered is named beside its reason, because the one thing this panel
			     must not do is read as six successes. -->
			{#if refresh.view !== null}
				<Panel title={t('refresh.heading')} hint={sweep} data-refresh-answer>
					{#snippet meta()}
						<Button variant="ghost" onclick={hideRefresh}>{t('refresh.hide')}</Button>
					{/snippet}
					{#if refresh.view.count === 0}
						<p class="text-xs text-wled-text-quiet">{t('refresh.nothing')}</p>
					{:else}
						<ul class="flex flex-col">
							{#each refresh.view.lights as row (row.binding)}
								{@const wrote = wroteKey(row.wrote ?? '')}
								<li
									class="flex flex-wrap items-baseline gap-row border-b border-wled-line py-row last:border-b-0"
									data-refresh-binding={row.binding}
									data-refresh-outcome={row.outcome}
								>
									<LightLink name={row.light} address={row.controller} cls="font-medium" />
									<span class="text-xs text-wled-text-quiet">
										{segmentText(t, row.segment, undefined)}
									</span>
									<span class="ml-auto text-xs {outcomeTone(row.outcome)}">
										{t(outcomeKey(row.outcome))}{wrote === '' ? '' : ` · ${t(wrote)}`}
									</span>
								</li>
								{#if row.why}
									<li class="pb-row text-xs text-wled-text-quiet">{row.why}</li>
								{/if}
							{/each}
						</ul>
					{/if}
				</Panel>
			{/if}

			{#each SCREENS as screen (screen.id)}
				{#if screen.id === current}
					{@const View = screen.component}
					<View />
				{/if}
			{/each}
		</div>
	</div>
</section>
