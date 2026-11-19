<script lang="ts">
	// A panel: a surface, a hairline, square-ish corners, and nothing else.
	//
	// The guide's whole geometry is in this one shape (§4, §5): flat, 1px, no
	// gradient, no shadow, no glass. Hierarchy comes from the space inside and
	// around it, so the border separates and never decorates. The heading is
	// medium weight and small — size does less work here than alignment does.
	import type { Snippet } from 'svelte';

	interface Props {
		/** The panel's own heading, when it has one. Omitted, the panel is a box. */
		title?: string;
		/** A short second line beside the heading. */
		hint?: string;
		/** The body, padded from the spacing scale. */
		children?: Snippet;
		class?: string;
	}

	let { title = '', hint = '', children, class: className = '' }: Props = $props();
</script>

<section
	class="rounded-panel border border-wled-line bg-wled-panel text-wled-panel-ink {className}"
>
	{#if title !== ''}
		<header class="flex flex-wrap items-baseline gap-row border-b border-wled-line px-panel py-row">
			<h2 class="font-ui text-sm font-medium">{title}</h2>
			{#if hint !== ''}
				<p class="font-ui text-xs text-wled-text-quiet">{hint}</p>
			{/if}
		</header>
	{/if}
	<div class="p-panel">
		{@render children?.()}
	</div>
</section>
