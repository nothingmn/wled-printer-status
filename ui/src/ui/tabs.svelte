<script lang="ts">
	// The tab bar, and the rail that is the same thing turned on its side.
	//
	// The mark of the current item is a thin accent rule: a 2px underline under a
	// horizontal bar, with the bar's own hairline running beneath it, and the
	// same rule on the leading edge once the bar stands up as the module's rail.
	// That is the host's own tab, taken from its source
	// (dist/primitives/nav-tabs/nav-tabs.svelte): uppercase, letter-spaced,
	// `border-selected` on the current one and `text-foreground`, quiet text on
	// the rest. It is deliberately not a filled pill — a pill says "button",
	// and where you are is not a thing you press.
	//
	// The host paints its *sidebar* selection the other way round, as a filled
	// accent block with `--selected-foreground` on it. The module's rail is a tab
	// rail — eight views behind one host tab — and the filled block's own label
	// measures under WCAG AA against its fill, so the rule is the idiom here and
	// the filled block stays where the host uses it.
	//
	// The bar scrolls sideways rather than wrapping, because it is used from a
	// phone (§3 of the requirements).
	import { TAB_CURRENT, TAB_RESTING, TAB_UPRIGHT } from './tab-classes';

	interface Item {
		/** Stable, and only ever compared. */
		id: string;
		/** The words on the rail, in the reader's language. */
		label: string;
	}

	interface Props {
		/** What the bar is, for a screen reader: "Screens". */
		label: string;
		items: Item[];
		/** The id of the item showing. */
		active: string;
		/** `tabs` is a bar; `rail` is a strip on a phone and a column from `md`. */
		orientation?: 'tabs' | 'rail';
		onSelect: (id: string) => void;
		class?: string;
	}

	let { label, items, active, orientation = 'tabs', onSelect, class: className = '' }: Props = $props();

	const base =
		'inline-flex min-h-9 shrink-0 cursor-pointer items-center gap-row whitespace-nowrap border-b-2 px-row pb-row font-ui text-xs font-medium uppercase tracking-label outline-none focus-visible:ring-2 focus-visible:ring-wled-focus focus-visible:ring-offset-2 focus-visible:ring-offset-wled-canvas';

	/** The strip stands up into a column from `md`, and the rule moves with it. */
	const upright = TAB_UPRIGHT;

	const current = TAB_CURRENT;
	const resting = TAB_RESTING;
</script>

<nav aria-label={label} class={className}>
	<ul
		class={orientation === 'rail'
			? 'flex gap-row overflow-x-auto border-b border-wled-line md:flex-col md:gap-0 md:border-b-0 md:border-l'
			: 'flex gap-section overflow-x-auto border-b border-wled-line'}
	>
		{#each items as item (item.id)}
			<li class="shrink-0">
				<button
					type="button"
					aria-current={active === item.id ? 'page' : undefined}
					class="{base} {orientation === 'rail' ? upright : ''} {active === item.id
						? current
						: resting}"
					onclick={() => onSelect(item.id)}
				>
					{item.label}
				</button>
			</li>
		{/each}
	</ul>
</nav>
