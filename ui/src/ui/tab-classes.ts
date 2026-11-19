// The one definition of what a tab looks like, for the two places a tab bar is drawn.
//
// `tabs.svelte` is the bar itself — the module's rail of screens — and it composes these.
// The Lights screen draws two *nested* tab strips (a light's bucket, then the light inside it)
// that are a `tablist` with an `id` per tab and an `aria-controls` on each one; re-parenting
// them through the rail's component would be a change to the accessible structure of the page,
// so that screen composes the same three strings instead of copying their values. The shape is
// still defined here, once: a screen that wanted a different tab has to change the primitive
// layer, which is the whole of the style guide's second rule.
//
// The mark of the current item is a thin accent rule and never a filled pill: a pill says
// "button", and where you are is not a thing you press (guide §5).
export const TAB_BAR =
	'inline-flex min-h-9 shrink-0 cursor-pointer items-center gap-row whitespace-nowrap border-b-2 px-row pb-row font-ui text-xs font-medium uppercase tracking-label outline-none focus-visible:ring-2 focus-visible:ring-wled-focus focus-visible:ring-offset-2 focus-visible:ring-offset-wled-canvas';

/** A rail stands its rule on the leading edge from `md`; a strip keeps it underneath. */
export const TAB_UPRIGHT = 'md:min-h-0 md:border-b-0 md:border-l-2 md:py-tight md:pl-row';

export const TAB_CURRENT = 'border-wled-accent text-wled-text';

export const TAB_RESTING = 'border-transparent text-wled-text-quiet hover:text-wled-text';
