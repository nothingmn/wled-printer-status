<script lang="ts">
	// A collapsed explanation: a hairline-topped row with a plain-language summary
	// line and a chevron, and the body indented to the same grid (§5).
	//
	// The behaviour is the browser's own on purpose — `<details>`/`<summary>` is
	// a real focusable control, Enter and Space toggle it, and a screen reader
	// announces whether it is open — so nothing here re-implements a disclosure
	// as a button beside a flag that can drift out of step with what is drawn.
	// Collapsed is the only starting state and there is no prop to open it.
	import type { Snippet } from 'svelte';

	interface Props {
		/** The line that stays on the page while the rest is folded away. */
		summary: string;
		/** A short second line beside it, for a block whose header carried one. */
		hint?: string;
		/** The block itself. */
		children: Snippet;
	}

	let { summary, hint = '', children }: Props = $props();
</script>

<details class="group border-t border-wled-line">
	<summary
		class="flex cursor-pointer list-none items-center gap-row py-row font-ui text-sm font-medium outline-none focus-visible:ring-2 focus-visible:ring-wled-focus focus-visible:ring-offset-2 focus-visible:ring-offset-wled-canvas [&::-webkit-details-marker]:hidden"
	>
		<span
			class="inline-block text-wled-text-quiet transition-transform group-open:rotate-90"
			aria-hidden="true">▶</span
		>
		<span>{summary}</span>
		{#if hint !== ''}
			<span class="ml-auto font-ui text-xs font-normal text-wled-text-quiet">{hint}</span>
		{/if}
	</summary>
	<div class="pb-row pl-panel font-ui text-sm text-wled-text">
		{@render children()}
	</div>
</details>
