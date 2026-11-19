<script lang="ts">
	// One explanatory block, folded away until somebody asks for it.
	//
	// Three blocks on two screens are prose a person reads once and then never needs
	// again: what Bindings refuses and why, the reasons Diagnostics can give, and the
	// honest limits it works inside. All three sat open, so each page opened on its
	// explanation rather than on the thing being explained. They are one component
	// because they are one behaviour.
	//
	// The behaviour is the browser's own, deliberately. `<details>`/`<summary>` is a real
	// focusable control, Enter and Space toggle it, and a screen reader announces whether
	// it is expanded -- so nothing here re-implements a disclosure as a button beside a
	// flag that can drift out of step with what is drawn.
	//
	// Collapsed is the only starting state and there is no prop to open it: a block that
	// can start open is a block somebody will start open.
	import type { Snippet } from 'svelte';

	interface Props {
		/** The line that stays on the page while the rest is folded away. */
		summary: string;
		/** A short second line beside it, for a block whose header carried one. */
		hint?: string;
		/** The block itself: whatever the card body used to hold. */
		children: Snippet;
	}

	let { summary, hint = '', children }: Props = $props();
</script>

<details class="group overflow-hidden rounded border">
	<summary
		class="flex cursor-pointer list-none flex-wrap items-center gap-2 bg-black/5 px-3 py-2 text-sm font-medium hover:bg-black/10 dark:bg-white/10 dark:hover:bg-white/15 [&::-webkit-details-marker]:hidden"
	>
		<span
			class="inline-block text-[10px] opacity-60 transition-transform group-open:rotate-90"
			aria-hidden="true">▶</span
		>
		<span>{summary}</span>
		{#if hint !== ''}
			<span class="ml-auto text-xs font-normal opacity-60">{hint}</span>
		{/if}
	</summary>
	<div class="text-sm">
		{@render children()}
	</div>
</details>
