<script lang="ts">
	// The bindings screen: the one tab on this module's own page under Settings,
	// Extensions. Every binding the module drives -- the printer, the light it is
	// drawn on, the segment it takes and whether that light is answering -- and the
	// same four added, changed and removed in place, with the printers and
	// controllers offered as the choices the module itself reports rather than as
	// ids somebody has to know.
	//
	// # One printer on several lights
	//
	// docs/product-requirements.md §2.2 is the rule this screen obeys: a printer may
	// sit on several lights -- multi-room status -- and a segment carries one printer
	// only. So where a printer is bound is two questions and not one:
	//
	//   - is it already on the light being edited? Then the field does not offer it:
	//     that light already shows it, its row is already in the table below, and the
	//     segment it would take is taken;
	//   - is it on some other light? Then the field offers it and says where it is,
	//     because naming the second light for a machine is exactly what multi-room is.
	//
	// The screen asked one question before -- "is this printer bound at all?" -- which
	// was right while a printer could be bound once and hid every printer already
	// shown anywhere, so a second light could not be given to one from here.
	//
	// Where a printer is, is read off GET /bindings and not off GET /printers: a
	// printer's `bound` member carries the first of its bindings and cannot say
	// whether *this* light is one of them. `bound_count` says how many there are, which
	// is what explains two rows of one printer below.
	import { onMount } from 'svelte';

	import { Badge, Button } from '@prusa/connect-ui/primitives';

	import {
		moduleApi,
		type Binding,
		type BindingRequest,
		type Controller,
		type LastSend,
		type Printer,
		type StatusBody,
		type StatusEntry,
	} from './api';
	import { heldHost } from './host';

	const host = heldHost();
	const api = moduleApi(host);
	const t = host.t;

	/** A binding as the editor holds it, with the numbers kept as text. */
	interface Draft {
		printer: string;
		/** The controller picked from the ones the module is already driving. */
		controller: string;
		/** The address typed in, when the operator names one the module is not driving yet. */
		address: string;
		segment: string;
		brightness: string;
		/** True when the controller is a new address rather than one in the list. */
		fresh: boolean;
	}

	let bindings = $state<Binding[]>([]);
	let printers = $state<Printer[]>([]);
	let controllers = $state<Controller[]>([]);
	/** Every binding's last write, keyed `controller|segment`, which is what names one. */
	let sends = $state<Record<string, LastSend | null>>({});
	let failure = $state('');
	let loading = $state(true);
	let busy = $state(false);

	/** The binding being changed, and null when a new one is being made. */
	let editing = $state<string | null>(null);
	/** The draft of the editor, and null when no editor is open. */
	let draft = $state<Draft | null>(null);

	const key = (controller: string, segment: number): string => `${controller}|${segment}`;

	/** The last write to one binding's controller, and null before any was tried. */
	const sendOf = (binding: Binding): LastSend | null | undefined =>
		sends[key(binding.controller, binding.segment)];

	/**
	 * A controller's name on these routes: its address without the scheme, which is what
	 * a binding carries as `light`. It is computed here for an address typed in rather
	 * than picked, because the editor has to compare like with like: the module names a
	 * binding's light, and this names the light the editor is pointed at.
	 */
	const lightName = (address: string): string =>
		address
			.trim()
			.replace(/^[a-zA-Z][a-zA-Z0-9+.-]*:\/\//, '')
			.replace(/\/+$/, '')
			.toLowerCase();

	/** The address the editor is pointed at: the one picked, or the one being typed. */
	const draftAddress = $derived(draft === null ? '' : draft.fresh ? draft.address : draft.controller);

	/** The light being edited, named the way GET /bindings names one. */
	const draftLight = $derived(lightName(draftAddress));

	/**
	 * The bindings on the light being edited, other than the row being changed: the
	 * segments that light has already taken, and the printers that took them.
	 */
	const taken = $derived(bindings.filter((one) => one.light === draftLight && one.id !== editing));

	/**
	 * The printers the light being edited does not already show, in the operator's own
	 * order. A printer on a different light is here, and must be: that is multi-room.
	 */
	const free = $derived(printers.filter((one) => !taken.some((held) => held.printer === one.id)));

	/** The printers an add could still place somewhere: one on every light has nowhere to go. */
	const addable = $derived(
		controllers.length === 0
			? printers
			: printers.filter((one) =>
					controllers.some(
						(light) =>
							!bindings.some(
								(placed) => placed.light === lightName(light.address) && placed.printer === one.id,
							),
					),
				),
	);

	/** The segment the editor is pointed at, read the way save() reads it: -1 is the whole light. */
	const wanted = $derived.by(() => {
		const segment = Number.parseInt(draft?.segment ?? '', 10);

		return Number.isNaN(segment) ? -1 : segment;
	});

	/**
	 * The binding that already takes the segment the editor is pointed at, and undefined
	 * when none does. It is the refusal the route makes, said before Save is pressed: one
	 * segment is shown by one binding, and a whole light leaves no segment for anything
	 * else. It is a warning and not a lock, because this screen's list can be a moment
	 * behind a change made elsewhere, and the route has the last word.
	 */
	const conflict = $derived(
		wanted < 0 ? taken[0] : taken.find((one) => one.segment < 0 || one.segment === wanted),
	);

	/** Every controller address the module is driving, and the one being changed. */
	const addresses = $derived.by(() => {
		const known = controllers.map((one) => one.address);
		const current = draft?.controller;

		return current && !known.includes(current) ? [current, ...known] : known;
	});

	/** Where a printer is already shown, on any light but the one being edited. */
	const shownElsewhere = (one: Printer): string =>
		bindings
			.filter((held) => held.printer === one.id && held.light !== draftLight)
			.map((held) => (held.whole ? held.light : `${held.light}:${held.segment}`))
			.join(', ');

	/**
	 * What one printer is called in the field. A printer already on this light says so
	 * instead of disappearing, so that the operator can see where it went; a printer on
	 * another light is offered and says where, so that adding it here is his choice and
	 * not a surprise.
	 */
	const choiceName = (one: Printer): string => {
		if (!free.some((held) => held.id === one.id)) {
			return `${one.name} ${t('bindings.on_this_light')}`;
		}

		const where = shownElsewhere(one);

		return where === '' ? one.name : `${one.name} ${t('bindings.already_on')} ${where}`;
	};

	/** One taken segment in the operator's words: which segment, and whose it is. */
	const takenName = (held: Binding): string =>
		`${held.whole ? t('bindings.whole') : `${t('bindings.segment')} ${held.segment}`} \u2014 ${
			held.printer_name ?? t('bindings.printer_gone')
		}`;

	/** How many bindings show one printer, which is what two rows of one name are. */
	const countOf = (printer: string): number =>
		printers.find((one) => one.id === printer)?.bound_count ?? 1;

	/** What a refusal said, in one line. */
	const describe = (problem: unknown): string => {
		if (problem instanceof Error) return problem.message;
		if (typeof problem === 'object' && problem !== null) {
			const said = problem as { detail?: string; message?: string; title?: string; code?: string };

			return said.detail ?? said.message ?? said.title ?? said.code ?? JSON.stringify(problem);
		}

		return String(problem);
	};

	const load = async (): Promise<void> => {
		loading = true;
		failure = '';

		try {
			const [list, fleet, driven, status] = await Promise.all([
				api.listBindings(),
				api.listPrinters(),
				api.listControllers(),
				// A module with one binding answers as a single frame, so a status
				// route that has published nothing yet is not a failure of this screen.
				api.readStatus().catch(() => ({}) as StatusBody),
			]);

			bindings = list.bindings;
			printers = fleet.printers;
			controllers = driven.controllers;

			const entries: StatusEntry[] =
				status.bindings ??
				(status.controller !== undefined && status.segment !== undefined
					? [{ controller: status.controller, segment: status.segment, last_send: status.last_send ?? null }]
					: []);

			sends = Object.fromEntries(entries.map((one) => [key(one.controller, one.segment), one.last_send]));
		} catch (problem) {
			failure = `${t('bindings.load_failed')}: ${describe(problem)}`;
		} finally {
			loading = false;
		}
	};

	/**
	 * Keeps the printer field pointing at a printer the light being edited can still take.
	 *
	 * It runs when the light changes, because the field's offer is the light's: a printer
	 * picked for one light may already be on the next. A row being edited keeps the printer
	 * it shows -- a binding's printer does not move, and the route refuses one that tries.
	 */
	const retarget = (): void => {
		if (!draft || editing !== null) return;

		const chosen = draft.printer;

		if (!free.some((one) => one.id === chosen)) {
			draft.printer = free[0]?.id ?? '';
		}
	};

	const startNew = (): void => {
		const address = addresses[0] ?? '';
		const light = lightName(address);

		editing = null;
		draft = {
			printer:
				printers.find(
					(one) => !bindings.some((placed) => placed.light === light && placed.printer === one.id),
				)?.id ?? '',
			controller: address,
			address: '',
			segment: '-1',
			brightness: '',
			fresh: addresses.length === 0,
		};
	};

	const startEdit = (binding: Binding): void => {
		editing = binding.id;
		draft = {
			printer: binding.printer,
			controller: binding.controller,
			address: '',
			segment: String(binding.segment),
			brightness: binding.brightness === undefined ? '' : String(binding.brightness),
			fresh: false,
		};
	};

	const stop = (): void => {
		editing = null;
		draft = null;
	};

	/** Swaps the controller field between the list and a typed address. */
	const toggleController = (): void => {
		if (!draft) return;

		draft.fresh = !draft.fresh;
		draft.address = '';
		draft.controller = addresses[0] ?? '';
		retarget();
	};

	const save = async (): Promise<void> => {
		if (!draft || draft.printer === '') return;

		const segment = Number.parseInt(draft.segment, 10);
		const body: BindingRequest = {
			printer: draft.printer,
			controller: draft.fresh ? draft.address.trim() : draft.controller,
			segment: Number.isNaN(segment) ? -1 : segment,
		};

		// A brightness is controller-wide, so it belongs on a whole-controller
		// binding and is refused on a borrowed segment. Sending none is how the
		// setting says "leave the controller's own alone".
		const brightness = Number.parseInt(draft.brightness, 10);
		if (body.segment !== undefined && body.segment < 0 && !Number.isNaN(brightness)) {
			body.brightness = brightness;
		}

		busy = true;
		failure = '';

		try {
			if (editing) await api.changeBinding(editing, body);
			else await api.addBinding(body);

			stop();
			await load();
		} catch (problem) {
			failure = `${t('bindings.save_failed')}: ${describe(problem)}`;
		} finally {
			busy = false;
		}
	};

	const remove = async (binding: Binding): Promise<void> => {
		busy = true;
		failure = '';

		try {
			await api.removeBinding(binding.id);
			await load();
		} catch (problem) {
			failure = `${t('bindings.save_failed')}: ${describe(problem)}`;
		} finally {
			busy = false;
		}
	};

	const nameOf = (binding: Binding): string => binding.printer_name ?? t('bindings.printer_gone');

	onMount(() => {
		void load();
	});
</script>

<section class="flex flex-col gap-4 p-4">
	<header class="flex flex-col gap-1">
		<h2 class="text-base font-semibold">{t('bindings.title')}</h2>
		<p class="text-sm opacity-70">{t('bindings.intro')}</p>
		<p class="text-sm opacity-70">{t('bindings.multi_room')}</p>
		<p class="text-sm opacity-70">{t('bindings.not_saved')}</p>
	</header>

	{#if failure}
		<div class="rounded border px-3 py-2 text-sm" role="alert">{failure}</div>
	{/if}

	{#if loading}
		<p class="text-sm opacity-70">{t('bindings.loading')}</p>
	{:else if bindings.length === 0 && !draft}
		<div class="rounded border px-4 py-6 text-center">
			<p class="text-sm font-medium">{t('bindings.empty_heading')}</p>
			<p class="text-sm opacity-70">{t('bindings.empty_body')}</p>
		</div>
	{:else if bindings.length > 0}
		<table class="w-full text-left text-sm">
			<thead>
				<tr class="border-b">
					<th class="py-2 pr-3 font-medium">{t('bindings.position')}</th>
					<th class="py-2 pr-3 font-medium">{t('bindings.printer')}</th>
					<th class="py-2 pr-3 font-medium">{t('bindings.controller')}</th>
					<th class="py-2 pr-3 font-medium">{t('bindings.segment')}</th>
					<th class="py-2 pr-3 font-medium">{t('bindings.reach_heading')}</th>
					<th class="py-2 font-medium">{t('bindings.actions')}</th>
				</tr>
			</thead>
			<tbody>
				{#each bindings as binding (binding.id)}
					<tr class="border-b align-middle">
						<td class="py-2 pr-3 opacity-70">{binding.position}</td>
						<td class="py-2 pr-3">
							<div>{nameOf(binding)}</div>
							{#if countOf(binding.printer) > 1}
								<div class="text-xs opacity-60">{t('bindings.several')}</div>
							{/if}
						</td>
						<td class="py-2 pr-3">
							<div class="font-mono text-xs">{binding.light}</div>
							<div class="font-mono text-xs opacity-60">{binding.controller}</div>
						</td>
						<td class="py-2 pr-3">
							{binding.whole ? t('bindings.whole') : binding.segment}
						</td>
						<td class="py-2 pr-3">
							{#if !sendOf(binding)}
								<Badge variant="secondary">{t('bindings.no_send_yet')}</Badge>
							{:else if sendOf(binding)?.ok}
								<Badge variant="success">{t('bindings.reachable')}</Badge>
							{:else}
								<Badge variant="destructive">{t('bindings.unreachable')}</Badge>
							{/if}
						</td>
						<td class="py-2">
							<div class="flex gap-1">
								<Button variant="ghost" size="sm" onclick={() => startEdit(binding)} disabled={busy}>
									{t('bindings.edit')}
								</Button>
								<Button variant="ghost" size="sm" onclick={() => remove(binding)} disabled={busy}>
									{t('bindings.delete')}
								</Button>
							</div>
						</td>
					</tr>
				{/each}
			</tbody>
		</table>
	{/if}

	{#if draft}
		<form
			class="flex flex-col gap-3 rounded border p-4"
			onsubmit={(event) => {
				event.preventDefault();
				void save();
			}}
		>
			<div class="flex flex-wrap items-end gap-3">
				{#if draft.fresh}
					<label class="flex flex-col gap-1 text-sm">
						<span class="font-medium">{t('bindings.controller')}</span>
						<input
							class="rounded border px-2 py-1 font-mono text-xs"
							placeholder="http://10.0.0.136"
							value={draft.address}
							oninput={(event) => {
								if (!draft) return;

								draft.address = (event.currentTarget as HTMLInputElement).value;
								retarget();
							}}
						/>
					</label>
				{:else}
					<label class="flex flex-col gap-1 text-sm">
						<span class="font-medium">{t('bindings.controller')}</span>
						<select
							class="rounded border px-2 py-1"
							value={draft.controller}
							onchange={(event) => {
								if (!draft) return;

								draft.controller = (event.currentTarget as HTMLSelectElement).value;
								retarget();
							}}
						>
							{#each addresses as address (address)}
								<option value={address}>{address}</option>
							{/each}
						</select>
					</label>
				{/if}

				<label class="flex flex-col gap-1 text-sm">
					<span class="font-medium">{t('bindings.printer')}</span>
					<select class="rounded border px-2 py-1" bind:value={draft.printer} disabled={editing !== null}>
						{#if draft.printer === ''}
							<option value="">{t('bindings.no_printer_free')}</option>
						{/if}
						{#each printers as printer (printer.id)}
							<option
								value={printer.id}
								disabled={printer.id !== draft.printer && !free.some((held) => held.id === printer.id)}
							>
								{choiceName(printer)}
							</option>
						{/each}
					</select>
				</label>

				<label class="flex flex-col gap-1 text-sm">
					<span class="font-medium">{t('bindings.segment')}</span>
					<input class="w-24 rounded border px-2 py-1" type="number" bind:value={draft.segment} />
				</label>

				<label class="flex flex-col gap-1 text-sm">
					<span class="font-medium">{t('bindings.brightness')}</span>
					<input
						class="w-24 rounded border px-2 py-1"
						type="number"
						min="0"
						max="255"
						bind:value={draft.brightness}
						disabled={Number(draft.segment) >= 0}
					/>
				</label>

				<div class="pb-1">
					<Button variant="link" size="sm" onclick={toggleController}>
						{draft.fresh ? t('bindings.known_controller') : t('bindings.other_controller')}
					</Button>
				</div>
			</div>

			{#if taken.length > 0}
				<p class="text-xs opacity-70">
					{t('bindings.taken_here')}
					{taken.map(takenName).join(' \u00b7 ')}
				</p>
			{/if}

			{#if conflict}
				<p class="rounded border px-3 py-2 text-sm" role="status">
					{t('bindings.segment_taken')}
					{conflict.printer_name ?? t('bindings.printer_gone')}
					({conflict.whole ? t('bindings.whole') : conflict.segment}).
					{#if wanted < 0 || conflict.whole}
						{t('bindings.whole_takes_all')}
					{/if}
				</p>
			{/if}

			<div class="flex gap-2">
				<Button type="submit" disabled={busy || draft.printer === ''}>{t('bindings.save')}</Button>
				<Button variant="secondary" onclick={stop} disabled={busy}>{t('bindings.cancel')}</Button>
			</div>
		</form>
	{:else}
		<div>
			<Button variant="secondary" onclick={startNew} disabled={busy || addable.length === 0}>
				{t('bindings.add')}
			</Button>
		</div>
	{/if}
</section>
