<script lang="ts">
	// The one styled input: the host's own control — the surface it sits on, a
	// hairline border, small, with the accent reserved for focus (§5).
	//
	// Its own file rather than a branch inside `field.svelte`, because an input
	// and a select carry different attribute maps and one spread cannot satisfy
	// both: the union type it would need is rejected by whichever element it is
	// not. Two thin controls, each typed against its own element, and `field`
	// composes them.
	import type { HTMLInputAttributes } from 'svelte/elements';

	interface Props extends Omit<HTMLInputAttributes, 'oninput' | 'value'> {
		/** The current value, passed as an attribute; the DOM's own value wins afterwards. */
		value?: string;
		/** Called with the new value on every edit, rather than with the event. */
		oninput?: (value: string) => void;
	}

	let { value = '', oninput, class: className = '', ...rest }: Props = $props();

	const control =
		'h-9 min-w-0 rounded-control border border-wled-line-field bg-transparent px-2 font-ui text-sm text-wled-text outline-none placeholder:text-wled-text-quiet focus-visible:ring-2 focus-visible:ring-wled-focus focus-visible:ring-offset-2 focus-visible:ring-offset-wled-canvas disabled:text-wled-text-off aria-invalid:border-wled-danger';
</script>

<input
	{value}
	class="{control} {className}"
	oninput={(event) => oninput?.(event.currentTarget.value)}
	{...rest}
/>
