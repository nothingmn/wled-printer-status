<script lang="ts">
	// The one styled select. Same surface and same focus treatment as the input
	// beside it, because a row of fields that did not line up is the complaint
	// this guide opens with.
	//
	// `color-scheme` is stated on the shell (scheme.ts), which is what makes the
	// browser's own open list readable in the dark theme: the closed control is
	// styled by this stylesheet and the list is painted by the browser from the
	// used colour scheme and from nothing else.
	//
	// The choices come either as `options` — the plain case, a value and a label
	// — or as `children`, for a list where one choice has to be offered and
	// refused at once: a segment another printer already holds is shown greyed
	// out, naming its owner, so the refusal is visible before the save. That is a
	// `<option disabled>`, which no value/label pair can express.
	import type { Snippet } from 'svelte';
	import type { HTMLSelectAttributes } from 'svelte/elements';

	interface Option {
		value: string;
		label: string;
	}

	interface Props extends Omit<HTMLSelectAttributes, 'oninput' | 'value'> {
		/** The choices. The one whose value matches `value` is the selected one. */
		options?: Option[];
		value?: string;
		/** Called with the new value when the choice changes, rather than with the event. */
		oninput?: (value: string) => void;
		/** The choices as `<option>` elements, when one of them has to be `disabled`. */
		children?: Snippet;
	}

	let {
		options = [],
		value = '',
		oninput,
		onchange,
		children,
		class: className = '',
		...rest
	}: Props = $props();

	const control =
		'h-9 min-w-0 rounded-control border border-wled-line-field bg-transparent px-row font-ui text-sm text-wled-text outline-none focus-visible:ring-2 focus-visible:ring-wled-focus focus-visible:ring-offset-2 focus-visible:ring-offset-wled-canvas disabled:text-wled-text-off aria-invalid:border-wled-danger';
</script>

<select
	class="{control} {className}"
	onchange={(event) => {
		oninput?.(event.currentTarget.value);
		onchange?.(event);
	}}
	{...rest}
>
	{#if children}
		{@render children()}
	{:else}
		{#each options as option (option.value)}
			<option value={option.value} selected={option.value === value}>{option.label}</option>
		{/each}
	{/if}
</select>
