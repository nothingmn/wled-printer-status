<script lang="ts">
	// A status chip: small, soft-filled, uppercase, in the darker text of its own
	// family. Not a saturated block and not a pill (§2, §5).
	//
	// Each tone is one of the host's six state families, which are exactly a
	// step-3 fill under its own hue's step-11/12 text — the pairing the guide
	// describes, taken rather than mixed here. The host's printer-lifecycle
	// palette would give a brighter fill and plain foreground text; a chip has
	// words in it, so the family with a text tone is the one that works.
	import type { Snippet } from 'svelte';

	type Tone = 'printing' | 'done' | 'error' | 'idle' | 'quiet';

	interface Props {
		tone?: Tone;
		children?: Snippet;
		class?: string;
	}

	let { tone = 'quiet', children, class: className = '' }: Props = $props();

	const tones: Record<Tone, string> = {
		printing: 'border-wled-status-printing bg-wled-status-printing text-wled-status-printing-ink',
		done: 'border-wled-status-done bg-wled-status-done text-wled-status-done-ink',
		error: 'border-wled-status-error bg-wled-status-error text-wled-status-error-ink',
		idle: 'border-wled-status-idle bg-wled-status-idle text-wled-status-idle-ink',
		quiet: 'border-wled-status-quiet bg-wled-status-quiet text-wled-status-quiet-ink',
	};

	const skin = $derived(tones[tone]);
</script>

<span
	class="inline-flex shrink-0 items-center gap-tight whitespace-nowrap rounded-chip border px-1.5 py-0.5 font-ui text-xs font-medium uppercase tracking-label {skin} {className}"
>
	{@render children?.()}
</span>
