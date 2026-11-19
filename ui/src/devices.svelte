<script lang="ts">
	// Devices: what each light *is*. Nothing here about printers beyond naming which one sits on a
	// segment.
	//
	// docs/product-requirements.md §2.6 is one card per light, and the split with Bindings is the point
	// of it: Bindings says which printer is on which light, Devices says what the light is, so the two
	// cannot grow into each other. A card carries, in the requirement's own order:
	//
	//   - **the hardware facts** — the name the light calls itself, its firmware, how many pixels it
	//     has and whether it is a panel or a strip. Two sources, because no single route carries all
	//     four: the shape and the pixel count come from the frame the module is drawing, and the
	//     light's own name and its firmware only exist in a snapshot, which is the one read that asks
	//     the light who it is. A card with no snapshot says so rather than showing a guess;
	//   - **its own segments** — id, name and all four bounds, read-only. They belong to WLED exactly
	//     as presets do, so there is no editor here;
	//   - **its own presets, read-only** — from `GET /devices/{name}/presets`: slot and name, slot 0
	//     never listed, an unnamed slot shown as an unnamed slot rather than left out, and a light
	//     that could not be read answering `presets_known: false` with the reason. No preset editor and
	//     no gallery: presets are made in WLED's own interface, and this page only names one;
	//   - **the four proved tests** — `solid`, `border`, `quarters` and the finished word — plus
	//     `identify`, which has no route in this build and is therefore a disabled button with the
	//     reason beside it rather than a button that always fails. The four paint a real light, so the
	//     sentence that says so is above them and each one is a press, never a timer;
	//   - **snapshots** — take one, list them, and restore from one. Restore is destructive, rewrites
	//     the light's whole configuration and restarts it, and **has never been run against a real
	//     light on this build**, so the dialog says that in as many words and shows the sections it
	//     would write and the ones it refuses to;
	//   - **replacing a light** — §2.6's swap, which has no route in this build. The card says what the
	//     swap would require and states that nothing here does it, because a button that cannot work
	//     is worse than a sentence.
	//
	// A light that is not answering has its test buttons off, with the reason on the card: offering a
	// button that cannot reach the light teaches somebody to distrust the tests that do work.
	import { onMount } from 'svelte';

	import { Badge, Button } from '@prusa/connect-ui/primitives';

	import {
		moduleApi,
		type Frame,
		type PresetsBody,
		type RestoreAnswer,
		type Snapshot,
		type SnapshotDetailBody,
		type SnapshotsBody,
	} from './api';
	import { ageText, describe, runText } from './format';
	import { heldHost } from './host';
	import { addressOf, lights, loadLights, nameOf, type LightFacts } from './lights.svelte';

	const host = heldHost();
	const api = moduleApi(host);
	const t = host.t;

	/** The three diagnostic pictures plus the finished word: the four tests this build really has. */
	const PATTERNS = ['solid', 'border', 'quarters'] as const;

	/** How long a test keeps the light. The routes' own default, said out loud before it fires. */
	const HOLD_SECONDS = 60;

	/** One card's own answer from a route that names lights, keyed by the light's route name. */
	const answerFor = <T extends { lights: { light: string; refused?: string; why?: string; answers?: string }[] }>(
		answer: T,
		light: string,
	): { light: string; refused?: string; why?: string; answers?: string } | undefined =>
		answer.lights.find((one) => one.light === light) ?? answer.lights[0];

	let frames = $state<Frame[]>([]);
	/** Each light's own presets, and why it could not be read when it could not. */
	let presets = $state<Record<string, PresetsBody>>({});
	let presetsFault = $state<Record<string, string>>({});
	/** Each light's snapshots, and why the folder could not be read when it could not. */
	let snapshots = $state<Record<string, SnapshotsBody>>({});
	let snapshotsFault = $state<Record<string, string>>({});
	/** One snapshot's detail, keyed `light|id`, read when somebody asks to see what a restore would do. */
	let details = $state<Record<string, SnapshotDetailBody>>({});
	/** One line per light, saying what the last press of a test did. */
	let testSaid = $state<Record<string, string>>({});
	/** One line per light, saying what the last snapshot or restore did. */
	let snapshotSaid = $state<Record<string, string>>({});
	/** The restore dialog: the light, the snapshot, and whether the box has been ticked. */
	let confirming = $state<{ light: string; id: string } | null>(null);
	let understood = $state(false);
	let busy = $state<Record<string, boolean>>({});
	let failure = $state('');
	let loading = $state(true);

	const frameOfLight = (one: LightFacts): Frame | undefined =>
		frames.find((frame) => frame.light.name === nameOf(one));

	/** One snapshot's detail, when the operator has asked to see what a restore would write. */
	const detailOf = (light: string, id: string): SnapshotDetailBody | undefined => details[`${light}|${id}`];

	/** The newest snapshot this module holds of one light, which is the only read of its own name. */
	const newestSnapshot = (one: LightFacts): Snapshot | undefined => snapshots[nameOf(one)]?.snapshots[0];

	/** Whether the light answered, from the two routes that ask it something. */
	const answers = (one: LightFacts): boolean =>
		one.segmentsKnown || (presets[nameOf(one)]?.presets_known ?? false);

	/** The shape, from the frame first and the newest snapshot second. */
	const kindOf = (one: LightFacts): 'panel' | 'strip' | '' =>
		frameOfLight(one)?.kind ?? newestSnapshot(one)?.device.kind ?? '';

	/** How many pixels the light has. */
	const pixelsOf = (one: LightFacts): number => {
		const frame = frameOfLight(one);
		if (frame !== undefined) return frame.kind === 'panel' ? frame.width * frame.height : frame.width;

		return newestSnapshot(one)?.device.pixels ?? 0;
	};

	/** The name the light calls itself, which only a snapshot carries. */
	const calledOf = (one: LightFacts): string => newestSnapshot(one)?.device.controller ?? '';

	/** The firmware, which only a snapshot carries. */
	const firmwareOf = (one: LightFacts): string => newestSnapshot(one)?.device.firmware ?? '';

	/** One segment's four bounds, in the controller's own spelling. */
	const boundsOf = (one: LightFacts, id: number): string => {
		const found = one.segments.find((segment) => segment.id === id);

		return found === undefined ? '' : runText(found);
	};

	/** Which printer sits on one segment of this light, in the operator's words, and empty when none. */
	const onSegmentOf = (one: LightFacts, id: number): string => {
		const whole = one.bindings.find((binding) => binding.whole);
		if (whole !== undefined) return whole.printer_name ?? whole.printer;

		const found = one.bindings.find((binding) => !binding.whole && binding.segment === id);

		return found === undefined ? '' : (found.printer_name ?? found.printer);
	};

	/** A preset as a card names it: its name, or its slot when the light never named it. */
	const presetText = (name: string, slot: number, unnamed: boolean): string =>
		unnamed || name.trim() === '' ? t('devices.preset_unnamed', { slot }) : name;

	/** A file's size as a person reads it. */
	const sizeText = (bytes: number): string =>
		bytes < 1024 ? t('devices.bytes', { bytes }) : t('devices.kilobytes', { size: (bytes / 1024).toFixed(1) });

	const note = (light: string, said: string): void => {
		testSaid[light] = said;
	};

	const load = async (): Promise<void> => {
		loading = true;
		failure = '';

		try {
			await loadLights();

			// A frame first: it is what says whether a light is a panel or a strip, and it never
			// touches a controller. Then the two reads that ask the light itself, per light, each
			// allowed to fail on its own so one unplugged box does not empty the page.
			frames = (await api.listFrames().catch(() => ({ frames: [] as Frame[], count: 0, note: '' }))).frames;

			await Promise.all(
				lights.list.map(async (one) => {
					const light = nameOf(one);

					await Promise.all([
						api
							.listPresets(light)
							.then((answer) => {
								presets[light] = answer;
							})
							.catch((problem: unknown) => {
								presetsFault[light] = describe(problem);
							}),
						api
							.listSnapshots(light)
							.then((answer) => {
								snapshots[light] = answer;
							})
							.catch((problem: unknown) => {
								snapshotsFault[light] = describe(problem);
							}),
					]);
				}),
			);
		} catch (problem) {
			failure = `${t('devices.load_failed')}: ${describe(problem)}`;
		} finally {
			loading = false;
		}
	};

	/** Fires one test on one light. The light is named, so a press paints that light and not both. */
	const firePattern = async (one: LightFacts, pattern: (typeof PATTERNS)[number]): Promise<void> => {
		const light = nameOf(one);
		busy[light] = true;
		testSaid[light] = '';

		try {
			const answer = await api.previewPattern(light, pattern, HOLD_SECONDS);
			const mine = answerFor(answer, light);

			if (mine?.refused !== undefined) {
				note(light, t('devices.test_refused', { pattern, why: mine.why ?? mine.refused }));
			} else {
				note(light, t('devices.test_fired', { pattern, hold: answer.holds_seconds, what: mine?.answers ?? '' }));
			}
		} catch (problem) {
			note(light, t('devices.test_failed', { why: describe(problem) }));
		} finally {
			busy[light] = false;
		}
	};

	/** Fires the finished word on one light, which is the fourth of the four proved tests. */
	const fireFinished = async (one: LightFacts): Promise<void> => {
		const light = nameOf(one);
		busy[light] = true;
		testSaid[light] = '';

		try {
			const answer = await api.previewFinished(light, HOLD_SECONDS);
			const mine = answerFor(answer, light);

			if (mine?.refused !== undefined) {
				note(light, t('devices.test_refused', { pattern: answer.event, why: mine.why ?? mine.refused }));
			} else {
				note(light, t('devices.test_fired_word', { word: answer.event, hold: answer.holds_seconds }));
			}
		} catch (problem) {
			note(light, t('devices.test_failed', { why: describe(problem) }));
		} finally {
			busy[light] = false;
		}
	};

	/** Reads one light now and keeps the copy. A light that does not answer is refused by the route. */
	const takeSnapshot = async (one: LightFacts): Promise<void> => {
		const light = nameOf(one);
		busy[light] = true;
		snapshotSaid[light] = '';

		try {
			const answer = await api.takeSnapshot(light);
			snapshotSaid[light] = t('devices.snapshot_taken', {
				id: answer.snapshot.id,
				size: sizeText(answer.snapshot.bytes),
				presets: answer.snapshot.presets,
			});
			snapshots[light] = await api.listSnapshots(light);
			await refreshFrame(light);
		} catch (problem) {
			snapshotSaid[light] = t('devices.snapshot_failed', { why: describe(problem) });
		} finally {
			busy[light] = false;
		}
	};

	/** A quiet re-read of the pictures: a snapshot is also the only read of a light's own name. */
	const refreshFrame = async (light: string): Promise<void> => {
		try {
			frames = (await api.listFrames()).frames;
		} catch {
			// The card keeps the facts it has.
		}
	};

	/** Reads one snapshot's restore table, so the warning lists what will really be written. */
	const openDetail = async (light: string, id: string): Promise<void> => {
		const key = `${light}|${id}`;
		if (details[key] !== undefined) return;

		try {
			details[key] = await api.readSnapshot(light, id);
		} catch (problem) {
			snapshotSaid[light] = t('devices.snapshot_detail_failed', { why: describe(problem) });
		}
	};

	/** Opens the restore dialog. Nothing is written until the box is ticked and the button is pressed. */
	const askRestore = (light: string, id: string): void => {
		confirming = { light, id };
		understood = false;
		void openDetail(light, id);
	};

	/** Runs the restore the operator confirmed, and reports what the light did. */
	const runRestore = async (): Promise<void> => {
		if (confirming === null || !understood) return;

		const { light, id } = confirming;
		busy[light] = true;
		snapshotSaid[light] = '';

		try {
			const answer: RestoreAnswer = await api.restoreSnapshot(light, id);
			snapshotSaid[light] = t('devices.restore_done', {
				id: answer.restored.id,
				safety: answer.safety_snapshot.id,
				seconds: answer.reboot.answered_after_seconds.toFixed(1),
				observed: answer.reboot.observed ? t('devices.restore_observed') : t('devices.restore_not_observed'),
			});
			snapshots[light] = await api.listSnapshots(light);
			confirming = null;
			understood = false;
			delete details[`${light}|${id}`];
		} catch (problem) {
			snapshotSaid[light] = t('devices.restore_failed', { why: describe(problem) });
		} finally {
			busy[light] = false;
		}
	};

	onMount(() => {
		void load();
	});
</script>

<section class="flex flex-col gap-4">
	<header class="flex flex-col gap-1">
		<h1 class="text-lg font-semibold">{t('devices.title')}</h1>
		<p class="text-sm opacity-70">{t('devices.intro')}</p>
	</header>

	{#if failure !== ''}
		<div class="rounded border px-3 py-2 text-sm" role="alert">{failure}</div>
	{/if}

	{#if loading}
		<p class="text-sm opacity-70">{t('devices.loading')}</p>
	{:else if lights.list.length === 0}
		<div class="rounded border px-4 py-6">
			<p class="text-sm font-medium">{t('devices.no_lights')}</p>
		</div>
	{:else}
		{#each lights.list as one (nameOf(one))}
			{@const light = nameOf(one)}
			{@const frame = frameOfLight(one)}
			{@const kind = kindOf(one)}
			{@const held = presets[light]}
			{@const kept = snapshots[light]}
			{@const reachable = answers(one)}

			<article class="flex flex-col gap-3 rounded border p-3" data-device={light}>
				<!-- The light, and whether it answered. -->
				<div class="flex flex-wrap items-center gap-2">
					<span class="text-sm font-semibold">{one.controller?.name ?? light}</span>
					<span class="font-mono text-[11px] opacity-60">{addressOf(one)}</span>
					{#if kind === 'panel'}
						<Badge variant="outline" size="sm">{t('devices.shape_panel')}</Badge>
					{:else if kind === 'strip'}
						<Badge variant="outline" size="sm">{t('devices.shape_strip')}</Badge>
					{:else}
						<Badge variant="outline" size="sm">{t('devices.shape_unknown')}</Badge>
					{/if}
					<Badge variant={reachable ? 'secondary' : 'warning'} size="sm">
						{reachable ? t('devices.answering') : t('devices.not_answering')}
					</Badge>
				</div>

				{#if !reachable}
					<p class="text-xs" role="status">{t('devices.not_answering_why', { why: one.segmentsUnread !== '' ? one.segmentsUnread : (held?.presets_unread ?? '') })}</p>
				{/if}

				<!-- The hardware facts, with the source of each said out loud. -->
				<dl class="grid grid-cols-2 gap-x-3 gap-y-1 text-xs md:grid-cols-4">
					<div class="flex flex-col">
						<dt class="opacity-60">{t('devices.fact_called')}</dt>
						<dd class="font-medium">
							{#if calledOf(one) !== ''}
								{calledOf(one)}
							{:else}
								<span class="opacity-60">{t('devices.fact_called_unknown')}</span>
							{/if}
						</dd>
					</div>
					<div class="flex flex-col">
						<dt class="opacity-60">{t('devices.fact_firmware')}</dt>
						<dd class="font-mono">
							{#if firmwareOf(one) !== ''}
								{firmwareOf(one)}
							{:else}
								<span class="opacity-60">{t('devices.fact_firmware_unknown')}</span>
							{/if}
						</dd>
					</div>
					<div class="flex flex-col">
						<dt class="opacity-60">{t('devices.fact_pixels')}</dt>
						<dd class="font-mono">
							{#if pixelsOf(one) > 0}
								{#if kind === 'panel'}
									{t('devices.pixels_panel', { width: frame?.width ?? 0, height: frame?.height ?? 0, pixels: pixelsOf(one) })}
								{:else}
									{t('devices.pixels_strip', { pixels: pixelsOf(one) })}
								{/if}
							{:else}
								<span class="opacity-60">{t('devices.fact_pixels_unknown')}</span>
							{/if}
						</dd>
					</div>
					<div class="flex flex-col">
						<dt class="opacity-60">{t('devices.fact_segments')}</dt>
						<dd class="font-mono">{one.segmentsKnown ? one.segments.length : t('devices.fact_segments_unknown')}</dd>
					</div>
				</dl>

				<p class="text-[11px] opacity-60">{t('devices.facts_source')}</p>

				<!-- Its own segments: the list Bindings picks from, shown where it comes from. -->
				<div class="overflow-hidden rounded border">
					<div class="flex flex-wrap items-center gap-2 border-b bg-black/5 px-3 py-1.5 dark:bg-white/10">
						<span class="text-xs font-medium">{t('devices.segments_heading')}</span>
						<span class="ml-auto text-[10px] opacity-60">{t('devices.segments_read_only')}</span>
					</div>
					{#if !one.segmentsKnown}
						<p class="px-3 py-2 text-xs opacity-70">{t('devices.segments_unread', { why: one.segmentsUnread })}</p>
					{:else if one.segments.length === 0}
						<p class="px-3 py-2 text-xs opacity-70">{t('devices.segments_none')}</p>
					{:else}
						<table class="w-full text-left text-xs">
							<thead>
								<tr class="border-b uppercase tracking-wide opacity-60">
									<th class="px-3 py-1 font-medium">{t('devices.segment_id')}</th>
									<th class="px-3 py-1 font-medium">{t('devices.segment_name')}</th>
									<th class="px-3 py-1 font-medium">{t('devices.segment_bounds')}</th>
									<th class="px-3 py-1 font-medium">{t('devices.segment_printer')}</th>
								</tr>
							</thead>
							<tbody>
								{#each one.segments as segment (segment.id)}
									<tr class="border-b last:border-b-0">
										<td class="px-3 py-1 font-mono">{segment.id}</td>
										<td class="px-3 py-1">{segment.name.trim() === '' ? t('devices.segment_unnamed') : segment.name}</td>
										<td class="px-3 py-1 font-mono">{boundsOf(one, segment.id)}</td>
										<td class="px-3 py-1">
											{onSegmentOf(one, segment.id) === '' ? t('devices.segment_free') : onSegmentOf(one, segment.id)}
										</td>
									</tr>
								{/each}
							</tbody>
						</table>
					{/if}
				</div>

				<!-- Its own presets, read-only. -->
				<div class="overflow-hidden rounded border">
					<div class="flex flex-wrap items-center gap-2 border-b bg-black/5 px-3 py-1.5 dark:bg-white/10">
						<span class="text-xs font-medium">{t('devices.presets_heading')}</span>
						{#if held?.presets_known === true}
							<span class="ml-auto text-[10px] opacity-60">{t('devices.presets_count', { count: held.count })}</span>
						{/if}
					</div>

					{#if (presetsFault[light] ?? '') !== ''}
						<p class="px-3 py-2 text-xs opacity-70" role="status">
							{t('devices.presets_not_served', { why: presetsFault[light] })}
						</p>
					{:else if held !== undefined && !held.presets_known}
						<p class="px-3 py-2 text-xs opacity-70" role="status">
							{t('devices.presets_unread', { why: held.presets_unread ?? held.note })}
						</p>
					{:else if held !== undefined}
						{#if held.presets.length === 0}
							<p class="px-3 py-2 text-xs opacity-70">{t('devices.presets_none')}</p>
						{:else}
							<ul class="flex flex-wrap gap-x-3 gap-y-1 px-3 py-2 font-mono text-[11px]">
								{#each held.presets as preset (preset.slot)}
									<li>
										<span class="opacity-60">{preset.slot}</span>
										<span class={preset.unnamed ? 'opacity-60' : ''}>{presetText(preset.name, preset.slot, preset.unnamed)}</span>
									</li>
								{/each}
							</ul>
						{/if}
						<p class="border-t px-3 py-1.5 text-[10px] opacity-60">{held.note}</p>
					{/if}

					<p class="border-t px-3 py-1.5 text-[10px] opacity-60">{t('devices.presets_read_only')}</p>
				</div>

				<!-- The tests. They paint the light, so the sentence comes before the buttons. -->
				<div class="flex flex-col gap-2 rounded border px-3 py-2">
					<div class="flex flex-wrap items-center gap-2">
						<span class="text-xs font-medium">{t('devices.tests_heading')}</span>
						<span class="text-[10px] opacity-60">{t('devices.tests_before', { hold: HOLD_SECONDS })}</span>
					</div>

					<div class="flex flex-wrap items-center gap-1">
						{#each PATTERNS as pattern (pattern)}
							<Button
								variant="secondary"
								size="sm"
								disabled={busy[light] === true || !reachable}
								onclick={() => void firePattern(one, pattern)}
							>
								{pattern}
							</Button>
						{/each}
						<Button variant="secondary" size="sm" disabled={busy[light] === true || !reachable} onclick={() => void fireFinished(one)}>
							{t('devices.test_word')}
						</Button>

						<!-- `identify` is required by §2.6 and has no route in this build. -->
						<Button variant="ghost" size="sm" disabled title={t('devices.identify_no_route')}>
							{t('devices.test_identify')}
						</Button>
					</div>

					<p class="text-[10px] opacity-60">{t('devices.identify_no_route')}</p>

					{#if !reachable}
						<p class="text-[11px]" role="status">{t('devices.tests_disabled')}</p>
					{/if}
					{#if (testSaid[light] ?? '') !== ''}
						<p class="text-[11px]" role="status">{testSaid[light]}</p>
					{/if}
				</div>

				<!-- Snapshots: take one, list them, and restore from one. -->
				<div class="flex flex-col gap-2 rounded border px-3 py-2">
					<div class="flex flex-wrap items-center gap-2">
						<span class="text-xs font-medium">{t('devices.snapshots_heading')}</span>
						<Button variant="secondary" size="sm" disabled={busy[light] === true || !reachable} onclick={() => void takeSnapshot(one)}>
							{t('devices.snapshot_take')}
						</Button>
						{#if kept !== undefined}
							<span class="ml-auto text-[10px] opacity-60">{t('devices.snapshots_count', { count: kept.count })}</span>
						{/if}
					</div>

					<p class="text-[10px] opacity-60">{t('devices.snapshots_when')}</p>

					{#if (snapshotsFault[light] ?? '') !== ''}
						<p class="text-[11px] opacity-70" role="status">{t('devices.snapshots_unreadable', { why: snapshotsFault[light] })}</p>
					{:else if kept !== undefined && !kept.persisted}
						<p class="text-[11px] opacity-70" role="status">{t('devices.snapshots_no_folder', { why: kept.note ?? '' })}</p>
					{:else if kept !== undefined && kept.snapshots.length === 0}
						<p class="text-[11px] opacity-70">{t('devices.snapshots_none')}</p>
					{/if}

					{#if kept !== undefined}
						{#each kept.snapshots as snapshot (snapshot.id)}
							{@const detail = detailOf(light, snapshot.id)}
							<div class="flex flex-col gap-1 rounded border px-2 py-1.5 text-[11px]">
								<div class="flex flex-wrap items-center gap-2">
									<span class="font-mono">{snapshot.id}</span>
									<span class="opacity-60">{ageText(t, snapshot.taken_at) ?? snapshot.taken_at}</span>
									<span class="font-mono opacity-60">{sizeText(snapshot.bytes)}</span>
									<span class="opacity-60">{t('devices.snapshot_presets', { count: snapshot.presets })}</span>
									<div class="ml-auto flex items-center gap-1">
										<Button variant="link" size="sm" onclick={() => void openDetail(light, snapshot.id)}>
											{t('devices.snapshot_what_restore')}
										</Button>
										<Button variant="ghost" size="sm" disabled={busy[light] === true || !reachable} onclick={() => askRestore(light, snapshot.id)}>
											{t('devices.snapshot_restore')}
										</Button>
									</div>
								</div>

								<p class="opacity-60">{t('devices.snapshot_not_clone', { members: snapshot.omits.map((one) => one.member).join(', ') })}</p>

								{#if detail !== undefined}
									<div class="flex flex-col gap-1 border-t pt-1">
										<p>
											<span class="font-medium">{t('devices.restore_writes')}</span>
											{detail.restore.sections.join(', ')}
											{#if detail.restore.presets}· {t('devices.restore_presets_too')}{/if}
										</p>
										<p class="opacity-70">
											<span class="font-medium">{t('devices.restore_withheld')}</span>
											{detail.restore.withheld.map((one) => one.section).join(', ')}
										</p>
										<p class="opacity-70">{t('devices.restore_nw', { members: detail.restore.nw.members_removed.join(', ') })}</p>
										{#if !detail.restorable}
											<p role="alert">{t('devices.restore_unrestorable', { why: detail.unrestorable ?? '' })}</p>
										{/if}
									</div>
								{/if}
							</div>
						{/each}
					{/if}

					{#if (snapshotSaid[light] ?? '') !== ''}
						<p class="text-[11px]" role="status">{snapshotSaid[light]}</p>
					{/if}
				</div>

				<!-- Replacing a light: what §2.6 asks for, and why no button is here. -->
				<div class="flex flex-col gap-1 rounded border border-dashed px-3 py-2 text-[11px]">
					<span class="text-xs font-medium">{t('devices.replace_heading')}</span>
					<p>{t('devices.replace_none')}</p>
					<p class="opacity-70">{t('devices.replace_bindings')}</p>
					<p class="opacity-70">{t('devices.replace_out_of_scope')}</p>
				</div>
			</article>
		{/each}

		<!-- The restore dialog. It stays on the page rather than in a modal: it is the warning. -->
		{#if confirming !== null}
			{@const detail = details[`${confirming.light}|${confirming.id}`]}
			<div class="flex flex-col gap-2 rounded border p-3" role="dialog" aria-label={t('devices.restore_dialog')}>
				<h2 class="text-sm font-semibold">{t('devices.restore_dialog_heading', { light: confirming.light, id: confirming.id })}</h2>
				<p class="text-sm">{t('devices.restore_warning_destructive')}</p>
				<p class="text-sm font-medium" role="alert">{t('devices.restore_never_fired')}</p>
				<p class="text-xs opacity-80">{t('devices.restore_safety')}</p>
				<p class="text-xs opacity-80">{t('devices.restore_nw_warning')}</p>

				{#if detail !== undefined}
					<p class="text-xs opacity-80">
						<span class="font-medium">{t('devices.restore_writes')}</span>
						{detail.restore.sections.join(', ')}
					</p>
					<p class="text-xs opacity-80">
						<span class="font-medium">{t('devices.restore_withheld')}</span>
						{detail.restore.withheld.map((one) => `${one.section} (${one.why})`).join('; ')}
					</p>
				{/if}

				<label class="flex items-center gap-2 text-sm">
					<input type="checkbox" class="h-4 w-4" bind:checked={understood} />
					<span>{t('devices.restore_understand')}</span>
				</label>

				<div class="flex flex-wrap items-center gap-2">
					<Button variant="secondary" size="sm" disabled={!understood || busy[confirming.light] === true} onclick={() => void runRestore()}>
						{t('devices.restore_now')}
					</Button>
					<Button variant="link" size="sm" onclick={() => (confirming = null)}>{t('devices.restore_cancel')}</Button>
				</div>
			</div>
		{/if}

		<div class="flex flex-col gap-1 text-xs opacity-70">
			<p>{t('devices.note_split')}</p>
			<p>{t('devices.note_segments')}</p>
			<p>{t('devices.note_presets')}</p>
		</div>
	{/if}
</section>
