<script lang="ts">
	// Lights: what each light *is*, and the list of them. Nothing here about printers beyond naming
	// which one sits on a segment.
	//
	// docs/product-requirements.md §2.6 is one card per light, and the split with Bindings is the point
	// of it: Bindings says which printer is on which light, Lights says what the light is and which
	// lights exist at all, so the two cannot grow into each other. The screen was called Devices until
	// the operator could not find a light on it — a device on this host means a printer session — so it
	// is named for what it holds.
	//
	// Adding a light is the one thing this screen does that changes anything: a person types an
	// address, POST /lights reads the light before it keeps it, and the card that appears carries what
	// answered. A light does not need a printer to be free: it is a fact whether or not anything is
	// shown on it.
	//
	// A card carries, in the requirement's own order:
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
	//   - **replacing a light** — §2.6's swap, which has no route in this build. It used to be a
	//     paragraph on every card, which is an apology repeated once per light; it is now said once
	//     for the page, folded away, because the cards' job is describing the lights.
	//
	// Two things about how a light is *named* on this screen, both his words of 2026-11-19:
	//
	//   - **a light is its own name, and both parts are links.** The heading of a card reads
	//     `CyberDesk (http://10.0.0.253)`: the name the light reports for itself, its address in
	//     parentheses, and both are anchors to the light's own web interface in a new window. The
	//     name comes from `GET /lights` (`calls_itself`), never from the address and never typed. A
	//     light that has not answered has no name, so it shows its address alone — still a link — and
	//     says it has not answered;
	//   - **a light with no printer is not lost among the others.** It gets its own named area at the
	//     top of the screen, in the same idiom Overview's gap lists use: a bordered block, a band
	//     naming the gap with its count, and the cards inside. A light with a printer on one of its
	//     zones *has* a binding and belongs below, under its own band.
	//
	// A light that is not answering has its test buttons off, with the reason on the card: offering a
	// button that cannot reach the light teaches somebody to distrust the tests that do work.
	import { onMount } from 'svelte';

	import { Badge, Button } from '@prusa/connect-ui/primitives';

	import {
		moduleApi,
		type Frame,
		type KnownLight,
		type PresetsBody,
		type RestoreAnswer,
		type Snapshot,
		type SnapshotDetailBody,
		type SnapshotsBody,
	} from './api';
	import Disclosure from './disclosure.svelte';
	import { ageText, describe, lightName, runText } from './format';
	import { heldHost } from './host';
	import { addressOf, known, lights, loadKnownLights, nameOf, type LightFacts } from './lights.svelte';

	const host = heldHost();
	const api = moduleApi(host);
	const t = host.t;

	/** The three diagnostic pictures plus the finished word: the four tests this build really has. */
	const PATTERNS = ['solid', 'border', 'quarters'] as const;

	/** How long a test keeps the light. The routes' own default, said out loud before it fires. */
	const HOLD_SECONDS = 60;

	/** The name a light calls itself, in the card's heading, worn as a link. */
	const NAME_LINK = 'underline decoration-dotted underline-offset-2 hover:decoration-solid';

	/** Its address, in the same heading, in the controller's own monospace and one step smaller. */
	const ADDRESS_LINK = 'font-mono text-xs font-normal underline decoration-dotted underline-offset-2 hover:decoration-solid';

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

	/** The address a person is typing. Cleared once the light has answered and been added. */
	let typed = $state('');
	/** Whether an address is being read. Adding reads the light, so it is not instant. */
	let adding = $state(false);
	/** What the last added light answered, in the words of the route's own sentence. */
	let added = $state('');
	/** Why the last address was refused. A refusal is a sentence, and nothing was stored. */
	let addFailed = $state('');
	/** One line per light, saying what the last removal did, or why it could not. */
	let removeSaid = $state<Record<string, string>>({});

	/** The row this module keeps for one light: its address and what it last said about itself. */
	const knownOf = (one: LightFacts): KnownLight | undefined =>
		known.list.find(
			(row) =>
				lightName(row.address) === lightName(addressOf(one)) || row.name === (one.controller?.name ?? ''),
		);

	const frameOfLight = (one: LightFacts): Frame | undefined =>
		frames.find((frame) => frame.light.name === nameOf(one));

	/**
	 * The light's own web interface, as an anchor's `href`: its address, given a scheme when it was
	 * stored without one. A light is added by its address, so this is the address it answered at —
	 * nothing here builds a URL out of anything else.
	 */
	const urlOf = (one: LightFacts): string => {
		const address = addressOf(one).trim();
		if (address === '') return '';

		return /^[a-zA-Z][a-zA-Z0-9+.-]*:\/\//.test(address) ? address : `http://${address}`;
	};

	/** One light's presets as last read, for a card drawn inside a snippet. */
	const heldOf = (one: LightFacts): PresetsBody | undefined => presets[nameOf(one)];

	/** One light's snapshots as last read, for a card drawn inside a snippet. */
	const keptOf = (one: LightFacts): SnapshotsBody | undefined => snapshots[nameOf(one)];

	/**
	 * The lights no binding names — the ones drawn first, under their own band — and the ones that do
	 * have a printer. A light with a printer on one of its zones has a binding and is not in the first
	 * list: the test is a binding, not room, which is the rule Overview's gap list records too.
	 */
	const unbound = $derived(lights.list.filter((one) => one.bindings.length === 0));
	const bound = $derived(lights.list.filter((one) => one.bindings.length > 0));

	/** One snapshot's detail, when the operator has asked to see what a restore would write. */
	const detailOf = (light: string, id: string): SnapshotDetailBody | undefined => details[`${light}|${id}`];

	/** The newest snapshot this module holds of one light, which is the only read of its own name. */
	const newestSnapshot = (one: LightFacts): Snapshot | undefined => snapshots[nameOf(one)]?.snapshots[0];

	/** Whether the light answered, from the two routes that ask it something. */
	const answers = (one: LightFacts): boolean =>
		one.segmentsKnown || (presets[nameOf(one)]?.presets_known ?? false);

	/** The shape, from the store first, the frame second and the newest snapshot third. */
	const kindOf = (one: LightFacts): 'panel' | 'strip' | '' =>
		knownOf(one)?.kind ?? frameOfLight(one)?.kind ?? newestSnapshot(one)?.device.kind ?? '';

	/** How many pixels the light has: what it said, then what the module is drawing, then a snapshot. */
	const pixelsOf = (one: LightFacts): number => {
		const row = knownOf(one);
		if (row !== undefined && row.facts_known && row.pixels > 0) return row.pixels;

		const frame = frameOfLight(one);
		if (frame !== undefined) return frame.kind === 'panel' ? frame.width * frame.height : frame.width;

		return newestSnapshot(one)?.device.pixels ?? 0;
	};

	/** The panel's own width and height, from the store first and the frame second. */
	const panelSize = (one: LightFacts): { width: number; height: number } => {
		const row = knownOf(one);
		if (row !== undefined && row.facts_known && (row.width ?? 0) > 0) {
			return { width: row.width ?? 0, height: row.height ?? 0 };
		}

		const frame = frameOfLight(one);
		if (frame !== undefined && frame.kind === 'panel') return { width: frame.width, height: frame.height };

		return { width: 0, height: 0 };
	};

	/** The name the light calls itself: what this module read when it was added, or a snapshot. */
	const calledOf = (one: LightFacts): string => {
		const row = knownOf(one);
		if (row !== undefined && row.facts_known && (row.calls_itself ?? '') !== '') return row.calls_itself ?? '';

		return newestSnapshot(one)?.device.controller ?? '';
	};

	/** The firmware: what this module read when it was added, or a snapshot. */
	const firmwareOf = (one: LightFacts): string => {
		const row = knownOf(one);
		if (row !== undefined && row.facts_known && (row.firmware ?? '') !== '') return row.firmware ?? '';

		return newestSnapshot(one)?.device.firmware ?? '';
	};

	/** When the light last answered this module, which is what the card says its facts are as of. */
	const readAgeOf = (one: LightFacts): string => {
		const row = knownOf(one);

		return row === undefined ? '' : (ageText(t, row.probed_at) ?? '');
	};

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
			await loadKnownLights();

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

	/** Reads the light at the address a person typed, and keeps what answered.
	 *
	 * Nothing is kept unless the light answered: the route refuses an address nothing is at, an
	 * address that answers with something that is not a WLED light, and one already on the list, and
	 * each of those is a sentence here rather than a card that never fills.
	 */
	const addLight = async (): Promise<void> => {
		const address = typed.trim();
		if (address === '' || adding) return;

		adding = true;
		added = '';
		addFailed = '';

		try {
			const answer = await api.addLight(address);
			const light = answer.light;

			added = t('devices.added', {
				address: light.address,
				called: (light.calls_itself ?? '') === '' ? t('devices.added_unnamed') : (light.calls_itself ?? ''),
				firmware: (light.firmware ?? '') === '' ? t('devices.fact_firmware_unknown') : (light.firmware ?? ''),
				pixels: light.pixels,
				shape: light.kind === 'panel' ? t('devices.shape_panel') : t('devices.shape_strip'),
			});

			typed = '';
			await load();
		} catch (problem) {
			addFailed = t('devices.add_refused', { why: describe(problem) });
		} finally {
			adding = false;
		}
	};

	/** Takes one light off the list. Refused while a binding still shows a printer on it, in words. */
	const takeOff = async (one: LightFacts): Promise<void> => {
		const light = nameOf(one);
		removeSaid[light] = '';

		try {
			await api.removeLight(light);
			await load();
		} catch (problem) {
			removeSaid[light] = t('devices.remove_refused', { why: describe(problem) });
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
		<p class="text-xs opacity-70">{known.note === '' ? t('devices.discovery_note') : known.note}</p>
	</header>

	<!-- Adding a light: a person types an address, and the module reads the light before it keeps
	     it, so what comes back is what answered. Nothing is kept when something refuses. -->
	<div class="flex flex-wrap items-end gap-2 rounded border px-3 py-2">
		<label class="flex flex-col gap-1 text-xs" for="light-address">
			<span class="font-medium">{t('devices.add_label')}</span>
			<input
				id="light-address"
				class="w-64 rounded border px-2 py-1 font-mono text-sm"
				placeholder={t('devices.add_placeholder')}
				autocomplete="off"
				spellcheck="false"
				bind:value={typed}
			/>
		</label>
		<Button variant="secondary" size="sm" disabled={adding || typed.trim() === ''} onclick={() => void addLight()}>
			{adding ? t('devices.add_busy') : t('devices.add_button')}
		</Button>
		<span class="pb-1 text-[11px] opacity-60">{t('devices.add_hint')}</span>
	</div>

	{#if added !== ''}
		<p class="rounded border px-3 py-2 text-sm" role="status">{added}</p>
	{/if}

	{#if addFailed !== ''}
		<p class="rounded border px-3 py-2 text-sm" role="alert">{addFailed}</p>
	{/if}

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
		<!-- One link to a light's own interface. Both parts of the heading use it, so the new window,
		     the rel that keeps it apart from this page, and the sentence a screen reader hears are
		     written once: `target="_blank"` with `rel="noopener"`, and an accessible name that starts
		     with the visible text and says what will open. -->
		{#snippet lightLink(one: LightFacts, text: string, label: string, cls: string)}
			<a href={urlOf(one)} target="_blank" rel="noopener" aria-label={label} class={cls}>{text}</a>
		{/snippet}

		<!-- The address in parentheses, kept on one line so it never wraps between the name and
		     its own address. The brackets are drawn but hidden from a screen reader: the anchors
		     carry their own names, and `left paren` between them is noise. -->
		{#snippet addressInParens(one: LightFacts)}
			<span class="whitespace-nowrap"
				><span class="font-normal opacity-60" aria-hidden="true">{' ('}</span>{@render lightLink(
					one,
					addressOf(one),
					t('devices.open_address', { address: addressOf(one) }),
					ADDRESS_LINK,
				)}<span class="font-normal opacity-60" aria-hidden="true">{')'}</span
				></span
			>
		{/snippet}

		<!-- The card, written once and drawn in whichever area its light belongs to. #each over a
		     filtered list twice would be two copies of this markup, and two copies of a card drift. -->
		{#snippet card(one: LightFacts)}
			{@const light = nameOf(one)}
			{@const kind = kindOf(one)}
			{@const held = heldOf(one)}
			{@const kept = keptOf(one)}
			{@const reachable = answers(one)}
			{@const called = calledOf(one)}

			<article class="flex flex-col gap-3 rounded border p-3" data-device={light}>
				<!-- The light: the name it calls itself with its address in parentheses, and both are
				     links to its own interface in a new window. A light that has not answered has no
				     name to show, so the address stands alone — still a link — and says so. -->
				<div class="flex flex-wrap items-center gap-2">
					<h2 class="text-sm font-semibold">
						{#if called !== ''}
							{@render lightLink(one, called, t('devices.open_named', { name: called }), NAME_LINK)}{@render addressInParens(one)}
						{:else}
							{@render lightLink(one, addressOf(one), t('devices.open_address', { address: addressOf(one) }), ADDRESS_LINK)}
							<span class="text-xs font-normal opacity-60">{t('devices.no_name_yet')}</span>
						{/if}
					</h2>
					{#if one.bindings.length === 0}
						<Badge variant="outline" size="sm">{t('devices.no_printer_badge')}</Badge>
					{/if}
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
					{#if readAgeOf(one) !== ''}
						<span class="text-[11px] opacity-60">{t('devices.read_at', { age: readAgeOf(one) })}</span>
					{/if}
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
									{t('devices.pixels_panel', { width: panelSize(one).width, height: panelSize(one).height, pixels: pixelsOf(one) })}
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

					{#if one.bindings.length === 0}
						<p class="text-[11px] opacity-70">{t('devices.tests_need_printer')}</p>
					{/if}

					<div class="flex flex-wrap items-center gap-1">
						{#each PATTERNS as pattern (pattern)}
							<Button
								variant="secondary"
								size="sm"
								disabled={busy[light] === true || !reachable || one.bindings.length === 0}
								onclick={() => void firePattern(one, pattern)}
							>
								{pattern}
							</Button>
						{/each}
						<Button variant="secondary" size="sm" disabled={busy[light] === true || !reachable || one.bindings.length === 0} onclick={() => void fireFinished(one)}>
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

				<!-- Taking the light off the list. Off while a binding still writes to it, with the
				     reason beside it, and the route refuses it too if it is called some other way. -->
				<div class="flex flex-wrap items-center gap-2 border-t pt-2">
					<Button
						variant="ghost"
						size="sm"
						disabled={busy[light] === true || one.bindings.length > 0}
						onclick={() => void takeOff(one)}
					>
						{t('devices.remove_button')}
					</Button>
					<span class="text-[10px] opacity-60">
						{one.bindings.length > 0 ? t('devices.remove_bound', { count: one.bindings.length }) : t('devices.remove_note')}
					</span>
				</div>

				{#if (removeSaid[light] ?? '') !== ''}
					<p class="text-[11px]" role="alert">{removeSaid[light]}</p>
				{/if}
			</article>
	{/snippet}

	<!-- The lights no printer is on, first and under their own band: a light somebody has just
	     added is the first thing on the screen, and never one card lost among the working ones.
	     The idiom is Overview's gap list — a bordered block, a band naming the gap with its count,
	     and the rows inside — so the two screens say "this one isn't doing anything yet" the same
	     way. The area stays drawn when it is empty and says so in one line, as Overview's does. -->
	<div class="overflow-hidden rounded border" data-area="unbound">
		<div class="flex flex-wrap items-center gap-2 border-b bg-black/5 px-3 py-2 text-xs font-medium dark:bg-white/10">
			<span>{t('devices.unbound_heading')}</span>
			<span class="ml-auto font-normal opacity-60">{t('devices.lights_count', { count: unbound.length })}</span>
		</div>
		<p class="border-b px-3 py-2 text-xs opacity-70">{t('devices.unbound_note')}</p>
		{#if unbound.length === 0}
			<p class="px-3 py-3 text-sm opacity-70">{t('devices.unbound_none')}</p>
		{:else}
			<div class="flex flex-col gap-3 p-3">
				{#each unbound as one (nameOf(one))}
					{@render card(one)}
				{/each}
			</div>
		{/if}
	</div>

	<!-- The lights a printer is on, the same shape, below. -->
	<div class="overflow-hidden rounded border" data-area="bound">
		<div class="flex flex-wrap items-center gap-2 border-b bg-black/5 px-3 py-2 text-xs font-medium dark:bg-white/10">
			<span>{t('devices.bound_heading')}</span>
			<span class="ml-auto font-normal opacity-60">{t('devices.lights_count', { count: bound.length })}</span>
		</div>
		{#if bound.length === 0}
			<p class="px-3 py-3 text-sm opacity-70">{t('devices.bound_none')}</p>
		{:else}
			<div class="flex flex-col gap-3 p-3">
				{#each bound as one (nameOf(one))}
					{@render card(one)}
				{/each}
			</div>
		{/if}
	</div>

	<!-- §2.6's swap, once for the page rather than once per card: there is no replace route, and a
	     paragraph explaining a feature that does not exist is an apology, not information about a
	     light. It folds away in the same disclosure Bindings and Diagnostics use, and its summary
	     line says both things a reader needs — what a swap would check, and that it is not built. -->
	<Disclosure summary={t('devices.replace_heading')} hint={t('devices.replace_hint')}>
		<div class="flex flex-col gap-1 px-3 py-2 text-[11px]">
			<p>{t('devices.replace_none')}</p>
			<p class="opacity-70">{t('devices.replace_bindings')}</p>
			<p class="opacity-70">{t('devices.replace_out_of_scope')}</p>
		</div>
	</Disclosure>

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
