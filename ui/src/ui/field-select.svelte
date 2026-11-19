<script lang="ts">
	// The one styled select. Same surface and same focus treatment as the input
	// beside it, because a row of fields that did not line up is the complaint
	// this guide opens with.
	//
	// `color-scheme` is stated on the shell (scheme.ts), which is what makes the
	// browser's own open list readable in the dark theme: the closed control is
	// styled by this stylesheet and the list is painted by the browser from the
	// used colour scheme and from nothing else.
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
	}

	let {
		options = [],
		value = '',
		oninput,
		onchange,
		class: className = '',
		...rest
	}: Props = $props();

	const control =
		'h-9 w-full min-w-0 rounded-control border border-wled-line-field bg-transparent px-2 font-ui text-sm text-wled-text outline-none focus-visible:ring-2 focus-visible:ring-wled-focus focus-visible:ring-offset-2 focus-visible:ring-offset-wled-canvas disabled:text-wled-text-off aria-invalid:border-wled-danger';
</script>

<select
	class="{control} {className}"
	onchange={(event) => {
		oninput?.(event.currentTarget.value);
		onchange?.(event);
	}}
	{...rest}
>
	{#each options as option (option.value)}
		<option value={option.value} selected={option.value === value}>{option.label}</option>
	{/each}
</select>
