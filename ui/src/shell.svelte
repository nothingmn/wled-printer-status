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
	//     is showing, so no screen has to remember to carry it.
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

	import { lengthText } from './format';
	import { heldHost } from './host';
	import {
		LENGTHS,
		everythingPaused,
		globalPause,
		pauses,
		pauseEverything,
		refreshPauses,
		resumeEverything,
	} from './pause.svelte';
	import { colourScheme } from './scheme';
	import { SCREENS } from './screens';
	import { Button, Panel, Tabs } from './ui';

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

	const stopFor = async (length: (typeof LENGTHS)[number]['id']): Promise<void> => {
		menu = false;
		await pauseEverything(length);
	};

	onMount(() => {
		void refreshPauses();
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
			<!-- The header's panic button: on every tab, because §3 makes it one. -->
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
				{#if failure !== ''}
					<span class="text-xs text-wled-danger" role="alert">{failure}</span>
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

			{#each SCREENS as screen (screen.id)}
				{#if screen.id === current}
					{@const View = screen.component}
					<View />
				{/if}
			{/each}
		</div>
	</div>
</section>
