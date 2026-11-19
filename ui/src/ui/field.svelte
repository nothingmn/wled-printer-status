<script lang="ts">
	// One field: a label, a control, and the one line under it that says what it
	// wants or what went wrong.
	//
	// The control is the host's own input — the surface it sits on, a hairline
	// border, small, with the accent reserved for focus (§5) — and the field
	// itself is a column of the spacing scale, so a row of fields lines up.
	//
	// The label is small and letter-spaced rather than a second sentence: a form
	// on this page is a control panel, and a control panel labels its controls.
	//
	// The control is one of two files (`field-input`, `field-select`) rather than
	// a branch here, because an input and a select carry different attribute maps
	// and a single spread cannot satisfy both. This file is the label, the
	// message and the column; a screen that needs a control shape not offered
	// here composes those two directly.
	import type { Snippet } from 'svelte';

	import FieldInput from './field-input.svelte';
	import FieldSelect from './field-select.svelte';

	interface Option {
		value: string;
		label: string;
	}

	interface Props {
		/** The control's id, and the label's `for`. */
		id: string;
		/** The words above the control. */
		label: string;
		/** An input, or a select with `options`. */
		kind?: 'input' | 'select';
		/** The choices, for `kind: 'select'`. The current one is `value`. */
		options?: Option[];
		value?: string;
		/** Called with the new value on every edit, and on a select's change. */
		oninput?: (value: string) => void;
		/** A line under the control: what it wants. */
		hint?: string;
		/** A line under the control: what went wrong. Outranks `hint`. */
		error?: string;
		type?: 'text' | 'number' | 'url' | 'search';
		name?: string;
		placeholder?: string;
		disabled?: boolean;
		required?: boolean;
		/** A control of the screen's own, instead of `kind`. */
		children?: Snippet;
		class?: string;
	}

	let {
		id,
		label,
		kind = 'input',
		options = [],
		value = '',
		oninput,
		hint = '',
		error = '',
		type = 'text',
		name = '',
		placeholder = '',
		disabled = false,
		required = false,
		children,
		class: className = '',
	}: Props = $props();
</script>

<div class="flex flex-col gap-tight {className}">
	<label
		for={id}
		class="font-ui text-xs font-medium uppercase tracking-label text-wled-text-quiet">{label}</label
	>
	{#if children}
		{@render children()}
	{:else if kind === 'select'}
		<FieldSelect
			{id}
			{name}
			{disabled}
			{required}
			{options}
			{value}
			{oninput}
			class="w-full"
			aria-invalid={error !== ''}
		/>
	{:else}
		<FieldInput
			{id}
			{name}
			{type}
			{placeholder}
			{disabled}
			{required}
			{value}
			{oninput}
			class="w-full"
			aria-invalid={error !== ''}
		/>
	{/if}
	{#if error !== ''}
		<p class="font-ui text-xs text-wled-danger" role="alert">{error}</p>
	{:else if hint !== ''}
		<p class="font-ui text-xs text-wled-text-quiet">{hint}</p>
	{/if}
</div>
