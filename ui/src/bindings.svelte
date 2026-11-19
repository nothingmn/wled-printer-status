<script lang="ts">
	// Bindings: one printer, one light, one segment of it -- grouped by light.
	//
	// docs/product-requirements.md §2.2, and the reason each part is here:
	//
	//   - **Grouped by light.** A segment carries one printer, and a flat list hides that;
	//     one card per light is the only arrangement in which "this light has no room"
	//     and "this light has two printers" are both visible at a glance.
	//   - **A row per binding**: printer, segment, brightness, when it last wrote, a switch,
	//     and the two actions. The switch is the binding's own pause (PUT
	//     /bindings/{id}/pause): the module has no `enabled` field on a binding, and a pause
	//     is the one thing that stops it writing. Off means paused until it is switched back
	//     on, which is said on the page rather than left to be discovered.
	//   - **Segments come from the light**, never typed: every list is
	//     GET /controllers/{name}'s own `segments`. A taken segment is greyed out with the
	//     printer that has it, a free count is carried per light, and the add button goes
	//     off at zero.
	//   - **A printer may sit on several lights** (multi-room) and **a segment may carry one
	//     printer only**: the printer field offers a printer already on another light and
	//     says where, and refuses one already on this light.
	//   - **Nothing is invisible**: printers with no light of their own are listed with the
	//     button that starts their binding.
	//
	// The one list this screen cannot draw is the one the requirements call *discovered
	// lights*: nothing in this build sweeps the network for lights, and a light only becomes
	// known to the module when a binding names its address (GET /controllers is derived from
	// the bindings -- "a controller has no separate life here"). The card is here, and it
	// says exactly that and points at the one way to add a light that exists. That gap is
	// reported rather than papered over: a list that is always empty because it has no
	// source is a lie dressed as a feature.
	import { onMount } from 'svelte';

	import { Badge, Button } from '@prusa/connect-ui/primitives';

	import {
		moduleApi,
		type Binding,
		type BindingRequest,
		type LastSend,
		type Printer,
		type StatusBody,
		type StatusEntry,
	} from './api';
	import BindingDialog from './binding-dialog.svelte';
	import { ageText, describe, segmentText } from './format';
	import { heldHost } from './host';
	import {
		addressOf,
		freeSegments,
		loadLights,
		lights,
		wholeTaker,
		type LightFacts,
	} from './lights.svelte';
	import { everythingPaused, fateOf, pauseOne, refreshPauses, resumeOne } from './pause.svelte';

	const host = heldHost();
	const api = moduleApi(host);
	const t = host.t;

	let printers = $state<Printer[]>([]);
	/** Every binding's last write, keyed `controller|segment`, which is what names one. */
	let sends = $state<Record<string, LastSend | null>>({});
	let failure = $state('');
	let notice = $state('');
	let loading = $state(true);
	let busy = $state(false);

	/** The panel, while it is open: the seed it starts from, and the binding it changes. */
	let panel = $state<{ light: string; printer: string; editing: Binding | null } | null>(null);

	const key = (controller: string, segment: number): string => `${controller}|${segment}`;

	/** The last write to one binding's light, and null before any was tried. */
	const sendOf = (binding: Binding): LastSend | null =>
		sends[key(binding.controller, binding.segment)] ?? null;

	/** Every binding on one light, in the operator's order. */
	const rowsOf = (one: LightFacts): Binding[] =>
		[...one.bindings].sort((left, right) => left.position - right.position);

	/** One segment in the light's own words: its number, its name and the run it covers. */
	const segmentOf = (one: LightFacts, segment: number): string => {
		const found = one.segments.find((held) => held.id === segment);

		return segmentText(t, segment, found?.name, found);
	};

	/** What one light's card says about its room, in §2.2's own terms. */
	const roomLine = (one: LightFacts): string => {
		if (!one.segmentsKnown) return t('bindings.segments_unread_line', { why: one.segmentsUnread });

		const whole = wholeTaker(one);
		if (whole !== undefined) {
			return t('bindings.whole_taken', {
				printer: whole.printer_name ?? t('bindings.printer_gone'),
				total: one.segments.length,
			});
		}

		return t('bindings.free_count', { free: freeSegments(one).length, total: one.segments.length });
	};

	/** Whether the light answered, and how long ago it was last written to. */
	const reachOf = (one: LightFacts): { ok: boolean; said: string } => {
		const writes = rowsOf(one)
			.map(sendOf)
			.filter((held): held is LastSend => held !== null);

		if (writes.length === 0) {
			return one.segmentsKnown
				? { ok: true, said: t('bindings.answered') }
				: { ok: false, said: t('bindings.no_answer') };
		}

		const newest = writes.reduce((held, candidate) => (Date.parse(candidate.at) > Date.parse(held.at) ? candidate : held));

		if (!newest.ok) return { ok: false, said: t('bindings.not_answering') };

		return { ok: true, said: t('bindings.answered_ago', { age: ageText(t, newest.at) ?? t('common.just_now') }) };
	};

	/** The printers Connect knows that nothing shows yet: §2.2's own list. */
	const unbound = $derived(
		printers.filter((one) => !lights.list.some((light) => light.bindings.some((held) => held.printer === one.id))),
	);

	/** The lights a new binding can still go on, which is what the whole-light add needs. */
	const withRoom = $derived(
		lights.list.filter((one) => freeSegments(one).length > 0 || one.bindings.length === 0),
	);

	const load = async (): Promise<void> => {
		loading = true;
		failure = '';

		try {
			const [fleet, status] = await Promise.all([
				api.listPrinters(),
				// A module that has published nothing yet is not a failure of this screen.
				api.readStatus().catch(() => ({}) as StatusBody),
				loadLights(),
			]);

			printers = fleet.printers;

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

	/** Opens the panel on one light, one printer, or one binding. */
	const start = (seed: { light?: string; printer?: string; editing?: Binding | null }): void => {
		// A refusal from an earlier action is not about this panel, so it is not carried in.
		failure = '';
		notice = '';
		panel = { light: seed.light ?? '', printer: seed.printer ?? '', editing: seed.editing ?? null };
	};

	/** Writes one binding, whether it is new or changed, and stops on the way back. */
	const save = async (body: BindingRequest): Promise<void> => {
		busy = true;

		try {
			if (panel?.editing != null) await api.changeBinding(panel.editing.id, body);
			else await api.addBinding(body);

			notice = t('bindings.saved');
			panel = null;
			await load();
			await refreshPauses();
		} catch (problem) {
			// The panel stays open with the refusal in it, so the operator fixes the field
			// rather than reopening the panel and losing what was typed.
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
			notice = t('bindings.removed');
			await load();
		} catch (problem) {
			failure = `${t('bindings.save_failed')}: ${describe(problem)}`;
		} finally {
			busy = false;
		}
	};

	/** The switch: off pauses this binding, on lets it write again. */
	const switching = async (binding: Binding, writing: boolean): Promise<void> => {
		busy = true;
		failure = '';

		try {
			if (writing) await resumeOne(binding.id);
			else await pauseOne(binding.id, 'forever');

			await refreshPauses();
		} catch (problem) {
			failure = `${t('bindings.save_failed')}: ${describe(problem)}`;
		} finally {
			busy = false;
		}
	};

	onMount(() => {
		void Promise.all([load(), refreshPauses()]);
	});
</script>

<section class="flex flex-col gap-4">
	<header class="flex flex-wrap items-end gap-3">
		<div class="flex flex-col gap-1">
			<h1 class="text-lg font-semibold">{t('bindings.title')}</h1>
			<p class="text-sm opacity-70">{t('bindings.intro')}</p>
		</div>
		<div class="ml-auto">
			<Button disabled={loading || printers.length === 0} onclick={() => start({})}>{t('bindings.add')}</Button>
		</div>
	</header>

	<p class="rounded border px-3 py-2 text-sm">{t('bindings.segment_rule')}</p>

	{#if failure !== ''}
		<div class="rounded border px-3 py-2 text-sm" role="alert">{failure}</div>
	{/if}
	{#if notice !== ''}
		<div class="rounded border px-3 py-2 text-sm" role="status">{notice}</div>
	{/if}
	{#if lights.failure !== ''}
		<div class="rounded border px-3 py-2 text-sm" role="alert">
			{t('bindings.lights_failed')}: {lights.failure}
		</div>
	{/if}

	{#if loading}
		<p class="text-sm opacity-70">{t('bindings.loading')}</p>
	{:else if lights.list.length === 0}
		<div class="rounded border px-4 py-6">
			<p class="text-sm font-medium">{t('bindings.empty_heading')}</p>
			<p class="text-sm opacity-70">{t('bindings.empty_body')}</p>
		</div>
	{:else}
		{#each lights.list as one (addressOf(one))}
			{@const reach = reachOf(one)}
			{@const free = freeSegments(one).length}
			{@const room = free > 0 || one.bindings.length === 0}
			<div class="overflow-hidden rounded border">
				<div class="flex flex-wrap items-center gap-2 border-b bg-black/5 px-3 py-2 dark:bg-white/10">
					<span class="text-sm font-semibold">{one.controller?.name}</span>
					<span class="font-mono text-xs opacity-60">{addressOf(one)}</span>
					<Badge variant={reach.ok ? 'secondary' : 'destructive'} size="sm">{reach.said}</Badge>
					<span class="ml-auto text-xs opacity-70">{roomLine(one)}</span>
				</div>

				{#if rowsOf(one).length === 0}
					<p class="px-3 py-3 text-sm opacity-70">{t('bindings.no_printer_here')}</p>
				{:else}
					<div class="overflow-x-auto">
						<table class="w-full min-w-[620px] text-left text-sm">
							<thead>
								<tr class="border-b text-xs uppercase tracking-wide opacity-60">
									<th class="px-3 py-2 font-medium">{t('bindings.printer')}</th>
									<th class="px-3 py-2 font-medium">{t('bindings.segment')}</th>
									<th class="px-3 py-2 font-medium">{t('bindings.brightness')}</th>
									<th class="px-3 py-2 font-medium">{t('bindings.writing')}</th>
									<th class="px-3 py-2 font-medium">{t('bindings.enabled')}</th>
									<th class="px-3 py-2 text-right font-medium">{t('bindings.actions')}</th>
								</tr>
							</thead>
							<tbody>
								{#each rowsOf(one) as binding (binding.id)}
									{@const fate = fateOf({ light: binding.light, segment: binding.segment })}
									{@const paused = fate?.paused === true}
									{@const send = sendOf(binding)}
									<tr class="border-b align-middle last:border-b-0">
										<td class="px-3 py-2">
											<div class="font-medium">{binding.printer_name ?? t('bindings.printer_gone')}</div>
											<div class="text-xs opacity-60">
												{#if !printers.some((held) => held.id === binding.printer)}
													{t('bindings.printer_missing')}
												{:else if paused}
													{fate?.effect === 'hands-back' ? t('bindings.zone_handed_back') : t('bindings.light_off')}
												{:else if (printers.find((held) => held.id === binding.printer)?.bound_count ?? 1) > 1}
													{t('bindings.on_several', {
														count: printers.find((held) => held.id === binding.printer)?.bound_count ?? 1,
													})}
												{:else}
													{t('bindings.on_one_light')}
												{/if}
											</div>
										</td>
										<td class="px-3 py-2">
											<span class="font-mono text-xs">{segmentOf(one, binding.segment)}</span>
											{#if !one.segmentsKnown}
												<span class="text-xs opacity-60">· {t('bindings.segments_unread_short')}</span>
											{/if}
										</td>
										<td class="px-3 py-2">
											{#if binding.brightness !== undefined}
												<span class="font-mono text-xs">{binding.brightness}</span>
											{:else}
												<span class="text-xs opacity-60">{t('bindings.brightness_borrowed_short')}</span>
											{/if}
										</td>
										<td class="px-3 py-2 text-xs opacity-70">
											{send === null
												? t('bindings.never_wrote')
												: send.ok
													? t('bindings.last_wrote', { age: ageText(t, send.at) ?? t('common.just_now') })
													: t('bindings.write_failed', { why: send.error ?? t('bindings.no_answer') })}
										</td>
										<td class="px-3 py-2">
											<input
												type="checkbox"
												class="h-4 w-4 align-middle"
												checked={!paused}
												disabled={busy || everythingPaused()}
												aria-label={t('bindings.writing_switch', { printer: binding.printer_name ?? binding.printer })}
												onchange={(event) => void switching(binding, (event.currentTarget as HTMLInputElement).checked)}
											/>
										</td>
										<td class="px-3 py-2 text-right">
											<div class="flex justify-end gap-1">
												<Button variant="ghost" size="sm" disabled={busy} onclick={() => start({ editing: binding })}>
													{t('bindings.change')}
												</Button>
												<Button variant="ghost" size="sm" disabled={busy} onclick={() => void remove(binding)}>
													{t('bindings.delete')}
												</Button>
											</div>
										</td>
									</tr>
								{/each}
							</tbody>
						</table>
					</div>
				{/if}

				<div class="flex flex-wrap items-center gap-2 border-t bg-black/5 px-3 py-2 dark:bg-white/10">
					<span class="text-xs opacity-70">{roomLine(one)}</span>
					<Button
						variant="secondary"
						size="sm"
						class="ml-auto"
						disabled={busy || !room}
						onclick={() => start({ light: addressOf(one) })}
					>
						{t('bindings.bind_here')}
					</Button>
				</div>
			</div>
		{/each}
	{/if}

	<!-- §2.2's own list, and the honest version of it: there is no sweep to fill it from. -->
	<div class="overflow-hidden rounded border">
		<div class="border-b bg-black/5 px-3 py-2 text-sm font-medium dark:bg-white/10">
			{t('bindings.discovered_heading')}
		</div>
		<p class="px-3 py-3 text-sm opacity-70">{t('bindings.discovered_note')}</p>
		<p class="border-t px-3 py-2 text-xs opacity-60">{t('bindings.discovered_how')}</p>
	</div>

	<div class="overflow-hidden rounded border">
		<div class="flex flex-wrap items-center gap-2 border-b bg-black/5 px-3 py-2 dark:bg-white/10">
			<span class="text-sm font-medium">{t('bindings.unbound_heading')}</span>
			<Badge variant="secondary" size="sm" class="ml-auto">
				{t('bindings.unbound_count', { count: unbound.length })}
			</Badge>
		</div>
		{#if unbound.length === 0}
			<p class="px-3 py-3 text-sm opacity-70">{t('bindings.unbound_empty')}</p>
		{:else}
			{#each unbound as printer (printer.id)}
				<div class="flex flex-wrap items-center gap-2 border-b px-3 py-2 text-sm last:border-b-0">
					<span class="font-medium">{printer.name}</span>
					{#if printer.model}<span class="text-xs opacity-60">{printer.model}</span>{/if}
					{#if !printer.connected}
						<Badge variant="warning" size="sm">{t('bindings.printer_offline')}</Badge>
					{/if}
					<Button
						variant="secondary"
						size="sm"
						class="ml-auto"
						disabled={busy || withRoom.length === 0}
						onclick={() => start({ printer: printer.id, light: withRoom.length === 0 ? '' : addressOf(withRoom[0]) })}
					>
						{t('bindings.add_here')}
					</Button>
				</div>
			{/each}
		{/if}
		{#if unbound.length > 0 && withRoom.length === 0}
			<p class="border-t px-3 py-2 text-xs opacity-60">{t('bindings.no_light_with_room')}</p>
		{/if}
		<p class="border-t px-3 py-2 text-xs opacity-60">{t('bindings.unbound_note')}</p>
	</div>

	<div class="overflow-hidden rounded border">
		<div class="border-b bg-black/5 px-3 py-2 text-sm font-medium dark:bg-white/10">
			{t('bindings.refused_heading')}
		</div>
		<ul class="divide-y text-sm">
			<li class="flex flex-wrap justify-between gap-2 px-3 py-2">
				<span>{t('bindings.refused_second')}</span>
				<span class="text-xs opacity-60">{t('bindings.refused_second_why')}</span>
			</li>
			<li class="flex flex-wrap justify-between gap-2 px-3 py-2">
				<span>{t('bindings.refused_brightness')}</span>
				<span class="text-xs opacity-60">{t('bindings.refused_brightness_why')}</span>
			</li>
			<li class="flex flex-wrap justify-between gap-2 px-3 py-2">
				<span>{t('bindings.refused_segment')}</span>
				<span class="text-xs opacity-60">{t('bindings.refused_segment_why')}</span>
			</li>
			<li class="flex flex-wrap justify-between gap-2 px-3 py-2">
				<span>{t('bindings.refused_light')}</span>
				<span class="text-xs opacity-60">{t('bindings.refused_light_why')}</span>
			</li>
		</ul>
		<p class="border-t px-3 py-2 text-xs opacity-60">{t('bindings.no_test')}</p>
	</div>

	<p class="text-xs opacity-60">{t('bindings.not_saved')}</p>
</section>

{#if panel !== null}
	<BindingDialog
		light={panel.light}
		printer={panel.printer}
		editing={panel.editing}
		{printers}
		lights={lights.list}
		{busy}
		failure={failure}
		onsave={save}
		onclose={() => (panel = null)}
	/>
{/if}
