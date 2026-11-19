<script lang="ts">
	// One data row: something at the left, the machine's own value in mono, and
	// what is known about it in secondary text at the right.
	//
	// The split is the guide's (§3, §5) and it is the identity of the product: a
	// timestamp, a percentage, a duration, an address or an id is set in mono so
	// it reads as a value, and prose stays in the interface face. A row that set
	// its value in sans would be the one thing this guide calls out by name.
	//
	// Rows are separated by a hairline and nothing else — no card per row, no
	// zebra striping — because the alignment across a panel is what makes a list
	// of them readable.
	import type { Snippet } from 'svelte';

	interface Props {
		/** The machine's value: a timestamp, a percentage, an address, an id. */
		value: string;
		/** The prose line under it, when the value needs one. */
		label?: string;
		/** A thumbnail, a glyph or a colour swatch. */
		leading?: Snippet;
		/** What sits at the right: a state, an age, an action. */
		meta?: Snippet;
		class?: string;
	}

	let { value, label = '', leading, meta, class: className = '' }: Props = $props();
</script>

<div
	class="flex min-h-9 items-center gap-row border-b border-wled-line px-row py-row last:border-b-0 {className}"
>
	{#if leading}
		<div class="shrink-0">{@render leading()}</div>
	{/if}
	<div class="min-w-0 flex-1">
		<div class="truncate font-data text-sm tabular-nums">{value}</div>
		{#if label !== ''}
			<div class="truncate font-ui text-xs text-wled-text-quiet">{label}</div>
		{/if}
	</div>
	{#if meta}
		<div class="shrink-0 text-right font-ui text-xs text-wled-text-quiet">{@render meta()}</div>
	{/if}
</div>
