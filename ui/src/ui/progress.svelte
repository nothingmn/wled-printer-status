<script lang="ts">
	// A thin track in a muted surface with an accent fill, and the number beside
	// it in mono (§5). The fill is the one accent, and the number is machine data,
	// so it is set in the interface's mono face rather than its sans one.
	//
	// `value` of null is a bar with no reading yet — a printer that has not said
	// how far along it is — and it draws no fill rather than a fill of nothing,
	// because a bar at zero and a bar that does not know read differently to
	// somebody glancing at a wall tablet.
	interface Props {
		/** How far along, 0 to 100; null when nothing has said. */
		value: number | null;
		/** What the bar is of, for a reader: "Print progress". */
		label: string;
		/** Whether the number sits beside the track. */
		showValue?: boolean;
	}

	let { value, label, showValue = true }: Props = $props();

	const shown = $derived(value === null ? null : Math.max(0, Math.min(100, value)));
	const text = $derived(shown === null ? '' : `${Math.round(shown)}%`);
</script>

<div class="flex w-full items-center gap-row">
	<div
		role="progressbar"
		aria-label={label}
		aria-valuemin={0}
		aria-valuemax={100}
		aria-valuenow={shown === null ? undefined : Math.round(shown)}
		aria-valuetext={shown === null ? undefined : text}
		class="h-1.5 min-w-0 flex-1 overflow-hidden rounded-chip bg-wled-quiet"
	>
		<div class="h-full rounded-chip bg-wled-progress" style="width: {shown ?? 0}%"></div>
	</div>
	{#if showValue}
		<span
			class="w-10 shrink-0 text-right font-data text-xs tabular-nums text-wled-text-quiet"
			aria-hidden="true">{shown === null ? '—' : text}</span
		>
	{/if}
</div>
