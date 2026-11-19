<script lang="ts">
	// The one place a binding is made or changed.
	//
	// docs/product-requirements.md §2.2, in the three rules this panel exists to obey:
	//
	//   - **Segments come from the light, never typed.** The list is GET
	//     /controllers/{name}'s own `segments` -- the ids, the names and the bounds the
	//     controller reports of itself -- so a segment this light does not have cannot be
	//     chosen, and the refusal the module would make at start is one nobody can reach.
	//   - **A segment carries one printer.** A taken segment is offered greyed out and
	//     labelled with the printer that has it, so the refusal is visible before the
	//     save button is pressed. A whole-light binding takes every segment, so it is
	//     offered only on a light carrying nothing, and choosing it hides the zones.
	//   - **A brightness only exists on the whole light.** A borrowed zone never gets one,
	//     because a brightness is controller-wide and the rest of that strip is somebody
	//     else's furniture.
	//
	// The panel is mounted by the screen for one edit and thrown away when it closes, so
	// its state starts from the seed the screen passes and never has to be reset.
	//
	// It carries `colourScheme` on its body div because of where it is drawn: the library
	// portals a dialog to `<body>`, outside the module's own markup and therefore outside
	// the shell's colour scheme and outside this module's own stylesheet. Without it, the
	// three selects here are the three that stay white on white in the dark theme.
	import { Button, Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@prusa/connect-ui/primitives';

	import type { Binding, BindingRequest, Printer, Segment } from './api';
	import { segmentText } from './format';
	import { heldHost } from './host';
	import { addressOf, takerIn, type LightFacts } from './lights.svelte';
	import { colourScheme } from './scheme';

	interface Props {
		/** The controller address to start on, empty for none. */
		light: string;
		/** The printer to start on, empty for none. */
		printer: string;
		/** The binding being changed, and null when one is being made. */
		editing: Binding | null;
		lights: LightFacts[];
		printers: Printer[];
		busy: boolean;
		failure: string;
		onsave: (body: BindingRequest) => Promise<void>;
		onclose: () => void;
	}

	let {
		light: seedLight = '',
		printer: seedPrinter = '',
		editing,
		lights,
		printers,
		busy = false,
		failure = '',
		onsave,
		onclose,
	}: Props = $props();

	const t = heldHost().t;

	let open = $state(true);
	let light = $state('');
	let printer = $state('');
	/** The segment, as the select's own string, so -1 for the whole light reads as it is. */
	let segment = $state('-1');
	let brightness = $state('');

	/** The light being edited, by its facts. */
	const facts = $derived(lights.find((one) => addressOf(one) === light) ?? null);

	/** The other bindings on that light: the ones a new one has to fit beside. */
	const others = $derived((facts?.bindings ?? []).filter((one) => one.id !== editing?.id));

	/** The light's own segments, in its own order. */
	const segments = $derived<Segment[]>(facts?.segments ?? []);

	/** The whole light is on offer only on a light carrying nothing else. */
	const wholeFree = $derived(others.length === 0);

	/** Every choice the segment field offers, taken ones disabled and saying whose they are. */
	const choices = $derived.by(() => {
		const offered: { id: number; label: string; disabled: boolean }[] = [];

		if (wholeFree) offered.push({ id: -1, label: t('common.whole_light'), disabled: false });

		for (const one of segments) {
			const taker = takerIn(others, one.id);

			offered.push({
				id: one.id,
				label:
					taker === undefined
						? segmentText(t, one.id, one.name, one)
						: `${segmentText(t, one.id, one.name, one)} \u2014 ${taker.printer_name ?? t('bindings.printer_gone')}`,
				disabled: taker !== undefined,
			});
		}

		return offered;
	});

	/** The light's answer about its own segments, in the one line the panel needs. */
	const readable = $derived(facts !== null && facts.segmentsKnown);

	const whole = $derived(Number(segment) < 0);

	/** A printer already on this light says so rather than disappearing. */
	const printerLabel = (one: Printer): string => {
		if (one.id === printer) return one.name;

		const here = others.find((held) => held.printer === one.id);
		if (here === undefined) return one.name;

		return `${one.name} ${t('bindings.on_this_light')}`;
	};

	/** The light a new binding starts on: the seed, else the first one with room. */
	const startLight = (): string => {
		if (seedLight !== '') return seedLight;

		return lights.find((one) => one.bindings.length === 0 || one.segments.length > one.bindings.length) !== undefined
			? addressOf(lights.find((one) => one.bindings.length === 0 || one.segments.length > one.bindings.length) as LightFacts)
			: (lights[0] === undefined ? '' : addressOf(lights[0]));
	};

	/** The first segment a light can still take, which is what a new binding starts on. */
	const firstChoice = (address: string): string => {
		const held = lights.find((one) => addressOf(one) === address);
		const taken = (held?.bindings ?? []).filter((one) => one.id !== editing?.id);

		if (taken.length === 0) return '-1';

		const free = (held?.segments ?? []).find((one) => takerIn(taken, one.id) === undefined);

		return free === undefined ? '-1' : String(free.id);
	};

	// Seeded once, on mount: the screen mounts this panel for one edit and unmounts it to
	// close, so there is nothing to reset and no reason to watch the seed afterwards.
	let seeded = false;

	$effect(() => {
		if (seeded) return;
		seeded = true;

		const address = editing === null ? startLight() : editing.controller;

		light = address;
		printer = editing === null ? seedPrinter : editing.printer;
		segment = editing === null ? firstChoice(address) : String(editing.segment);
		brightness = editing?.brightness === undefined ? '' : String(editing.brightness);
	});

	/** Moving to another light re-offers the segments that light has, and starts on a free one. */
	const moveTo = (address: string): void => {
		light = address;
		segment = firstChoice(address);
	};

	const level = $derived(Number.parseInt(brightness, 10));

	const canSave = $derived(readable && printer !== '' && light !== '');

	const submit = async (): Promise<void> => {
		const wanted = Number.parseInt(segment, 10);
		const body: BindingRequest = {
			printer,
			controller: light,
			segment: Number.isNaN(wanted) ? -1 : wanted,
		};

		// A brightness is controller-wide, so it travels only on a whole-light binding and
		// only when one was typed. Leaving it out is how the page says "the module's own
		// default stands", which is 128 unless a day schedule covers the moment.
		if (body.segment === -1 && brightness.trim() !== '' && !Number.isNaN(level)) body.brightness = level;

		await onsave(body);
	};

	$effect(() => {
		if (!open) onclose();
	});
</script>

<Dialog bind:open>
	<DialogContent size="md">
		<DialogHeader>
			<DialogTitle>{editing === null ? t('bindings.add_title') : t('bindings.change_title')}</DialogTitle>
			<DialogDescription>{t('bindings.dialog_intro')}</DialogDescription>
		</DialogHeader>

		<div class="flex flex-col gap-3 py-3" use:colourScheme>
			{#if failure !== ''}
				<div class="rounded border px-3 py-2 text-sm" role="alert">{failure}</div>
			{/if}

			<label class="flex flex-col gap-1 text-sm">
				<span class="font-medium">{t('bindings.light')}</span>
				<select
					class="rounded border px-2 py-1"
					value={light}
					onchange={(event) => moveTo((event.currentTarget as HTMLSelectElement).value)}
				>
					{#each lights as one (addressOf(one))}
						<option value={addressOf(one)}>
							{one.controller?.name} · {addressOf(one)}
						</option>
					{/each}
				</select>
			</label>

			{#if !readable}
				<p class="rounded border px-3 py-2 text-sm" role="status">
					{t('bindings.segments_unreadable', { why: facts?.segmentsUnread ?? t('bindings.no_answer') })}
				</p>
			{/if}

			<label class="flex flex-col gap-1 text-sm">
				<span class="font-medium">{t('bindings.segment')}</span>
				<select class="rounded border px-2 py-1" bind:value={segment} disabled={!readable}>
					{#each choices as choice (choice.id)}
						<option value={String(choice.id)} disabled={choice.disabled}>{choice.label}</option>
					{/each}
				</select>
				<span class="text-xs opacity-60">{t('bindings.segments_from_light')}</span>
			</label>

			<label class="flex flex-col gap-1 text-sm">
				<span class="font-medium">{t('bindings.printer')}</span>
				<select class="rounded border px-2 py-1" bind:value={printer}>
					{#if printer === ''}
						<option value="">{t('bindings.pick_printer')}</option>
					{/if}
					{#each printers as one (one.id)}
						<option value={one.id} disabled={printerLabel(one) !== one.name}>{printerLabel(one)}</option>
					{/each}
				</select>
			</label>

			<label class="flex flex-col gap-1 text-sm">
				<span class="font-medium">{t('bindings.brightness')}</span>
				{#if whole}
					<input
						class="w-24 rounded border px-2 py-1"
						type="number"
						min="0"
						max="255"
						bind:value={brightness}
						placeholder="128"
					/>
					<span class="text-xs opacity-60">{t('bindings.brightness_note')}</span>
				{:else}
					<span class="text-xs opacity-60">{t('bindings.brightness_borrowed')}</span>
				{/if}
			</label>
		</div>

		<DialogFooter showCloseButton={false}>
			<Button variant="secondary" disabled={busy} onclick={onclose}>{t('bindings.cancel')}</Button>
			<Button disabled={busy || !canSave} onclick={() => void submit()}>{t('bindings.save')}</Button>
		</DialogFooter>
	</DialogContent>
</Dialog>
