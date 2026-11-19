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
	import { onMount } from 'svelte';

	import { Button } from '@prusa/connect-ui/primitives';

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
	import { SCREENS } from './screens';

	const host = heldHost();
	const t = host.t;

	/** The screen showing. Right now opens first, which is the one every visit starts on. */
	let current = $state<string>(SCREENS[0].id);
	/** Whether the six lengths are on offer. */
	let menu = $state(false);

	const paused = $derived(everythingPaused());
	const global = $derived(globalPause());
	const left = $derived(global === null ? '' : lengthText(t, global.remaining_seconds, global.forever));

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

<section class="flex flex-col gap-3 p-4">
	<div class="flex flex-col gap-3 md:flex-row md:items-start md:gap-5">
		<!-- The rail. A top strip on a phone that scrolls sideways; a column from md. -->
		<nav
			aria-label={t('shell.screens')}
			class="flex gap-1 overflow-x-auto md:w-40 md:shrink-0 md:flex-col md:overflow-visible"
		>
			{#each SCREENS as screen (screen.id)}
				<button
					type="button"
					class={current === screen.id
						? 'shrink-0 whitespace-nowrap rounded-md bg-black/5 px-3 py-1.5 text-sm font-semibold dark:bg-white/10'
						: 'shrink-0 whitespace-nowrap rounded-md px-3 py-1.5 text-sm opacity-70 hover:opacity-100'}
					aria-current={current === screen.id ? 'page' : undefined}
					onclick={() => (current = screen.id)}
				>
					{t(screen.key)}
				</button>
			{/each}
		</nav>

		<div class="flex min-w-0 flex-1 flex-col gap-3">
			<!-- The header's panic button: on every tab, because §3 makes it one. -->
			<div class="flex flex-wrap items-center gap-2">
				{#if paused}
					<Button variant="secondary" size="sm" disabled={pauses.busy} onclick={() => void resumeEverything()}>
						{t('shell.resume_now')}
					</Button>
				{:else}
					<Button
						variant="secondary"
						size="sm"
						disabled={pauses.busy}
						aria-expanded={menu}
						onclick={() => (menu = !menu)}
					>
						{t('shell.pause_everything')}
					</Button>
				{/if}
				{#if failure !== ''}
					<span class="text-xs opacity-70" role="alert">{failure}</span>
				{/if}
			</div>

			{#if menu}
				<div class="flex flex-wrap items-center gap-1.5 rounded border px-3 py-2">
					<span class="pr-1 text-xs opacity-70">{t('shell.stop_writing_for')}</span>
					{#each LENGTHS as length (length.id)}
						<Button variant="ghost" size="sm" disabled={pauses.busy} onclick={() => void stopFor(length.id)}>
							{t(length.key)}
						</Button>
					{/each}
					<Button variant="link" size="sm" onclick={() => (menu = false)}>{t('shell.cancel')}</Button>
				</div>
			{/if}

			{#if paused}
				<div class="flex flex-wrap items-center gap-2 rounded border px-3 py-2 text-sm" role="status">
					<span class="font-medium">{t('shell.paused_heading')}</span>
					<span class="opacity-70">{t('shell.paused_banner', { length: left })}</span>
				</div>
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
