<script lang="ts">
	// A panel: a surface, a hairline, square-ish corners, and nothing else.
	//
	// The guide's whole geometry is in this one shape (§4, §5): flat, 1px, no
	// gradient, no shadow, no glass. Hierarchy comes from the space inside and
	// around it, so the border separates and never decorates. The heading is
	// medium weight and small — size does less work here than alignment does.
	//
	// Three things a screen may put *in* the panel rather than build beside it:
	//
	//   - `title` and `hint`, the heading row;
	//   - `meta`, the right end of that row — a count, a chip, a button. Every
	//     grouped block on these screens has one (a printer count, a "read-only"
	//     note, `bind them all`), and without it a screen would grow its own
	//     header row beside this one;
	//   - `pad="none"`, for a body that draws its own rows edge to edge: a
	//     table, a list separated by hairlines. The guide's rhythm is generous
	//     between sections and compact inside data, so a table inside a panel's
	//     padding would be the one place the two rules fought. A screen that
	//     needs it says so here rather than restyling a section of its own.
	//
	// It takes the attributes of the `<section>` it draws, because a screen
	// sometimes has to mark one — `data-device`, `data-light-panel`, the hooks a
	// harness reads — and a panel that could not be found was a panel a screen
	// would have re-implemented to get its attribute back.
	import type { Snippet } from 'svelte';
	import type { HTMLAttributes } from 'svelte/elements';

	interface Props extends Omit<HTMLAttributes<HTMLElement>, 'class' | 'children'> {
		/** The panel's own heading, when it has one. Omitted, the panel is a box. */
		title?: string;
		/**
		 * The heading as a component instead of a string, for the one shape a string cannot carry:
		 * a light's name and address are two links (light-link.svelte), and a panel whose heading
		 * is a light is the card a person recognises that light by. `title` is what a heading says
		 * when it is words; this is what it says when it is a thing.
		 */
		heading?: Snippet;
		/** A short second line beside the heading. */
		hint?: string;
		/** What sits at the right end of the heading row: a count, a chip, a button. */
		meta?: Snippet;
		/** `none` when the body is rows edge to edge — a table or a list — and pads itself. */
		pad?: 'panel' | 'none';
		/** The body. */
		children?: Snippet;
		class?: string;
	}

	let {
		title = '',
		heading,
		hint = '',
		meta,
		pad = 'panel',
		children,
		class: className = '',
		...rest
	}: Props = $props();

	/** A heading is drawn for a title, a supplied heading, or what sits beside it, and for nothing else. */
	const headed = $derived(title !== '' || heading !== undefined || hint !== '' || meta !== undefined);
</script>

<section
	class="rounded-panel border border-wled-line bg-wled-panel text-wled-panel-ink {className}"
	{...rest}
>
	{#if headed}
		<header class="flex flex-wrap items-center gap-row border-b border-wled-line px-panel py-row">
			{#if heading}
				{@render heading()}
			{:else if title !== ''}
				<h2 class="font-ui text-sm font-medium">{title}</h2>
			{/if}
			{#if hint !== ''}
				<p class="font-ui text-xs text-wled-text-quiet">{hint}</p>
			{/if}
			{#if meta}
				<div class="ml-auto flex flex-wrap items-center gap-row">{@render meta()}</div>
			{/if}
		</header>
	{/if}
	<div class={pad === 'panel' ? 'p-panel' : ''}>
		{@render children?.()}
	</div>
</section>
