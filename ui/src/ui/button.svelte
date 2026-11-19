<script lang="ts">
	// One button, four shapes — and the shapes are the host's own, token for
	// token (node_modules/@prusa/connect-ui/dist/primitives/button/variants.js):
	// a primary action is the accent fill with the accent's ink, a secondary one
	// is the quiet tint of it, a destructive one is an outline with red text
	// rather than a red block, and a ghost one is a hover with no edge at all.
	//
	// `destructive` takes `--wled-danger` (the host's `--destructive-text`) and
	// not `--wled-danger-fill`, because the fill measures under AA against the
	// panel it sits on and the text tone clears every surface. That is the
	// host's own correction, not this module's opinion.
	//
	// The focus ring is never removed: two pixels of the accent colour, offset
	// from the canvas so it reads in both themes. A control that cannot be seen
	// when it is focused is not finished (style guide §5).
	import type { Snippet } from 'svelte';
	import type { HTMLButtonAttributes } from 'svelte/elements';

	type Variant = 'primary' | 'secondary' | 'destructive' | 'ghost' | 'link';
	type Size = 'sm' | 'md';

	interface Props extends Omit<HTMLButtonAttributes, 'class' | 'onclick' | 'children'> {
		variant?: Variant;
		size?: Size;
		class?: string;
		onclick?: (event: MouseEvent) => void;
		children?: Snippet;
	}

	let {
		variant = 'secondary',
		size = 'sm',
		class: className = '',
		onclick,
		children,
		type = 'button',
		...rest
	}: Props = $props();

	const base =
		'inline-flex shrink-0 cursor-pointer items-center justify-center gap-row whitespace-nowrap rounded-control border font-ui font-medium outline-none focus-visible:ring-2 focus-visible:ring-wled-focus focus-visible:ring-offset-2 focus-visible:ring-offset-wled-canvas disabled:pointer-events-none disabled:cursor-default disabled:border-transparent disabled:bg-wled-off disabled:text-wled-text-off';

	const variants: Record<Variant, string> = {
		primary:
			'border-wled-accent-fill bg-wled-accent-fill text-wled-accent-fill-ink hover:border-wled-accent-fill-hover hover:bg-wled-accent-fill-hover active:bg-wled-accent-fill-press',
		secondary:
			'border-wled-accent-quiet bg-wled-accent-quiet text-wled-accent-quiet-ink hover:bg-wled-accent-quiet-hover',
		destructive:
			'border-wled-danger bg-transparent text-wled-danger hover:bg-wled-danger-fill hover:text-wled-danger-ink',
		ghost:
			'border-transparent bg-transparent text-wled-text-quiet hover:bg-wled-hover hover:text-wled-text',
		// A press that reads as a word, not a control: the accent text tone and an
		// underline that goes solid on hover. It is the shape a screen uses for
		// "discard", "show me what this would write" — an action beside a data row
		// that must not compete with the row's own value. Same focus ring as any
		// other button, because it is still a button.
		link: 'border-transparent bg-transparent text-wled-accent-text underline decoration-dotted underline-offset-2 hover:decoration-solid',
	};

	const sizes: Record<Size, string> = {
		sm: 'h-7 px-row text-xs',
		md: 'h-9 px-panel text-sm',
	};
</script>

<button {type} class="{base} {variants[variant]} {sizes[size]} {className}" {onclick} {...rest}>
	{@render children?.()}
</button>
