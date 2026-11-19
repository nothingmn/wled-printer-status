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
	//
	// # Three levels, because the page was overloaded
	//
	// His words, 2026-11-19: *"im actually thinking sub-sub-tabs on the lights main tab. top level is
	// 'Lights', then 'In-Use', 'Available', 'Ignored', and then a new sub-sub tab for each light, where
	// the name of the light (or if not available its IP) is the title of the tab. i think this because
	// the page is very overloaded.. hard to clearly see the difference between the lights."*
	//
	// So the screen is the first level, and it holds three buckets:
	//
	//   - **In-Use** — a light with at least one binding. This module is writing to it;
	//   - **Available** — known, no binding, not ignored. Every list that offers a light offers these;
	//   - **Ignored** — known, no binding, and pushed out of the way. Kept, readable, and out of every
	//     other list in the module, which is the whole point of the flag: his network answers with
	//     about nineteen WLED instances and he cares about two.
	//
	// Inside a bucket, each light is a tab of its own, titled with the name it calls itself — or, when
	// it has never answered, its address — and the card that used to be one of a stack is that tab's
	// whole content. **A tab is keyed by the light's address and labelled by its name**, so a rename
	// changes a word and never moves a tab; and **a strip that grows scrolls rather than wraps**,
	// because nineteen lights in a wrapped strip would rebuild the overload one level down.
	//
	// The rule that makes ignoring safe is the route's, and this screen says it in the same words: a
	// light a binding drives cannot be ignored, and the button says so instead of failing. Un-ignoring
	// is always available, from the Ignored bucket, and brings the light back everywhere.
	//
	// **The skin.** A card is the panel primitive, its facts are a definition grid, its segments and
	// presets are panels with no inner padding, an answer is a chip, and every action is a button.
	// The two tab strips compose the tab-bar classes the rail is drawn from (`tab-classes.ts`), because
	// a nested `tablist` keeps its own ids, its `aria-controls` and the data hooks the operator's
	// harness reads; what a tab looks like is still defined once, in the primitive layer.
	import { onMount } from 'svelte';

	import {
		moduleApi,
		type Binding,
		type Frame,
		type KnownLight,
		type PresetsBody,
		type RestoreAnswer,
		type Snapshot,
		type SnapshotDetailBody,
		type SnapshotsBody,
	} from './api';
	import PrinterLink from './printer-link.svelte';
	import { ageText, describe, lightName, runText } from './format';
	import { heldHost } from './host';
	import {
		addressOf,
		inBucket,
		known,
		lights,
		loadKnownLights,
		nameOf,
		type LightBucket,
		type LightFacts,
	} from './lights.svelte';
	import { Button, Chip, Disclosure, Field, FieldInput, Panel, TAB_BAR, TAB_CURRENT, TAB_RESTING } from './ui';

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
	const ADDRESS_LINK = 'font-data text-xs font-normal underline decoration-dotted underline-offset-2 hover:decoration-solid';

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
	/** One line per light, saying what the last ignore or un-ignore did. */
	let ignoreSaid = $state<Record<string, string>>({});

	/**
	 * Which bucket is open, and which light inside it. The light is held by its **address**, not by
	 * its name: a name is what the light calls itself and it can change, and a tab that moved when
	 * somebody renamed a light in WLED would be a tab that moved for no reason a person did here.
	 *
	 * `selected` empty means "the first light in the bucket", which is what makes the screen land
	 * on something after a load without a second piece of state to keep in step.
	 */
	let bucket = $state<LightBucket>('in-use');
	let selected = $state('');
	/** Whether the screen has chosen where to open. It does that once, and never moves underfoot. */
	let landed = $state(false);

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
	 * The three buckets, in the order the screen draws them, with the key of each one's name. The
	 * key is a table and not a template string so that every word this screen says is in the
	 * catalogue where a translator can find it.
	 */
	const BUCKETS: { id: LightBucket; key: string }[] = [
		{ id: 'in-use', key: 'devices.bucket_in_use' },
		{ id: 'available', key: 'devices.bucket_available' },
		{ id: 'ignored', key: 'devices.bucket_ignored' },
	];

	/** The lights in one bucket, in the operator's order. */
	const lightsIn = (id: LightBucket): LightFacts[] => inBucket(lights.list, id);

	/**
	 * The light whose tab is open in one bucket: the one asked for, or the first in the bucket when
	 * nothing has been asked for — or when the light that was asked for has just moved out of it.
	 *
	 * A function of the bucket and not a derived, so the panel is drawn from the bucket it is
	 * headed by in the same expression: a tab strip and the panel under it cannot then disagree
	 * about which bucket they are showing.
	 */
	const currentIn = (id: LightBucket): LightFacts | undefined => {
		const list = lightsIn(id);

		return list.find((one) => lightName(addressOf(one)) === lightName(selected)) ?? list[0];
	};

	/**
	 * The light whose tab is open, as a list of none or one: an `{#each}` over that list draws the
	 * panel without a cast and without a second condition, so the panel cannot be drawn for a light
	 * the strip above it does not have.
	 */
	const openIn = (id: LightBucket): LightFacts[] => {
		const one = currentIn(id);

		return one === undefined ? [] : [one];
	};

	/** What one bucket holds, said under its strip so no tab has to carry an explanation. */
	const noteOf = (id: LightBucket): string => {
		if (id === 'in-use') return t('devices.bucket_note_in_use');
		if (id === 'available') return t('devices.bucket_note_available');

		return t('devices.bucket_note_ignored');
	};

	/** What an empty bucket says. Empty is a fact, and each of the three is a different one. */
	const emptyOf = (id: LightBucket): string => {
		if (id === 'in-use') return t('devices.bucket_empty_in_use');
		if (id === 'available') return t('devices.bucket_empty_available');

		return t('devices.bucket_empty_ignored');
	};

	/**
	 * A light's tab is titled with the name it calls itself, and with its address when it has never
	 * answered — his words, and the same rule the card's heading follows. The address is what is
	 * left when a light has no name, and it is still the thing a person recognises on a network.
	 */
	const titledOf = (one: LightFacts): string => {
		const called = calledOf(one);

		return called === '' ? addressOf(one) : called;
	};

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

	/**
	 * Which binding sits on one segment of this light, and undefined when none does. It is the
	 * binding and not just its name, because the name is now a link to that printer's own page
	 * and the link needs the printer's id: the same anchor every other screen gives a printer.
	 */
	const binderOf = (one: LightFacts, id: number): Binding | undefined => {
		const whole = one.bindings.find((binding) => binding.whole);
		if (whole !== undefined) return whole;

		return one.bindings.find((binding) => !binding.whole && binding.segment === id);
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

			landOnce();
		} catch (problem) {
			failure = `${t('devices.load_failed')}: ${describe(problem)}`;
		} finally {
			loading = false;
		}
	};

	/**
	 * Where the screen opens, the first time it has anything to open.
	 *
	 * **In-Use first**, because that is the page's job and the calmest landing; **Available** when
	 * nothing is in use, and **Ignored** when that is all there is. It happens once and only once:
	 * after it, a bucket stays where the person put it, so a light moving out of a bucket never
	 * moves the screen under his hand.
	 */
	const landOnce = (): void => {
		if (landed) return;

		const order: LightBucket[] = ['in-use', 'available', 'ignored'];
		bucket = order.find((one) => inBucket(lights.list, one).length > 0) ?? 'in-use';
		landed = true;
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

			// The light somebody just added is the one he wants to look at, so the screen goes to
			// the tab it landed on rather than leaving it behind a bucket he has to guess. A light
			// that has never answered has no name yet, and its tab is titled by its address — which
			// is exactly the light he is looking at.
			bucket = 'available';
			selected = light.address;
		} catch (problem) {
			addFailed = t('devices.add_refused', { why: describe(problem) });
		} finally {
			adding = false;
		}
	};

	/**
	 * Puts one light out of the way, or brings it back.
	 *
	 * The route refuses ignoring a light a binding drives, and the button is off in exactly that
	 * case — but the refusal is still read here, because a light can be bound in another window
	 * between this screen's read and the press, and a refusal nobody prints is a press that did
	 * nothing.
	 *
	 * After either decision the screen follows the light to the bucket it landed in, so the result
	 * of the press is in front of the person rather than gone from the tab he was on.
	 */
	const setIgnored = async (one: LightFacts, ignored: boolean): Promise<void> => {
		const light = nameOf(one);
		const address = addressOf(one);

		busy[light] = true;
		ignoreSaid[light] = '';

		try {
			const answer = ignored ? await api.ignoreLight(light) : await api.unignoreLight(light);

			ignoreSaid[light] = answer.note;
			await load();

			bucket = ignored ? 'ignored' : 'available';
			selected = address;
		} catch (problem) {
			ignoreSaid[light] = t(ignored ? 'devices.ignore_refused' : 'devices.unignore_refused', {
				why: describe(problem),
			});
		} finally {
			busy[light] = false;
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

<section class="flex flex-col gap-section">
	<header class="flex flex-col gap-tight">
		<h1 class="font-ui text-lg font-medium">{t('devices.title')}</h1>
		<p class="font-ui text-sm text-wled-text-quiet">{t('devices.intro')}</p>
		<p class="font-ui text-xs text-wled-text-quiet">{known.note === '' ? t('devices.discovery_note') : known.note}</p>
	</header>

	<!-- Adding a light: a person types an address, and the module reads the light before it keeps
	     it, so what comes back is what answered. Nothing is kept when something refuses. -->
	<Panel>
		<div class="flex flex-wrap items-end gap-section">
			<Field id="light-address" label={t('devices.add_label')}>
				{#snippet children()}
					<FieldInput
						id="light-address"
						class="w-64 font-data"
						placeholder={t('devices.add_placeholder')}
						autocomplete="off"
						spellcheck="false"
						value={typed}
						oninput={(value) => (typed = value)}
					/>
				{/snippet}
			</Field>
			<Button variant="secondary" disabled={adding || typed.trim() === ''} onclick={() => void addLight()}>
				{adding ? t('devices.add_busy') : t('devices.add_button')}
			</Button>
			<span class="pb-tight font-ui text-xs text-wled-text-quiet">{t('devices.add_hint')}</span>
		</div>
	</Panel>

	{#if added !== ''}
		<Panel>
			<p class="font-ui text-sm" role="status">{added}</p>
		</Panel>
	{/if}

	{#if addFailed !== ''}
		<Panel>
			<p class="font-ui text-sm text-wled-danger" role="alert">{addFailed}</p>
		</Panel>
	{/if}

	{#if failure !== ''}
		<Panel>
			<p class="font-ui text-sm text-wled-danger" role="alert">{failure}</p>
		</Panel>
	{/if}

	{#if loading}
		<p class="font-ui text-sm text-wled-text-quiet">{t('devices.loading')}</p>
	{:else if lights.list.length === 0}
		<Panel>
			<p class="font-ui text-sm font-medium">{t('devices.no_lights')}</p>
		</Panel>
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
				><span class="font-normal text-wled-text-quiet" aria-hidden="true">{' ('}</span>{@render lightLink(
					one,
					addressOf(one),
					t('devices.open_address', { address: addressOf(one) }),
					ADDRESS_LINK,
				)}<span class="font-normal text-wled-text-quiet" aria-hidden="true">{')'}</span
				></span
			>
		{/snippet}

		<!-- The card, written once and drawn in whichever area its light belongs to. An each-block over
		     a filtered list twice would be two copies of this markup, and two copies of a card drift. -->
		{#snippet card(one: LightFacts)}
			{@const light = nameOf(one)}
			{@const kind = kindOf(one)}
			{@const held = heldOf(one)}
			{@const kept = keptOf(one)}
			{@const reachable = answers(one)}
			{@const called = calledOf(one)}

			<Panel data-device={light}>
				<div class="flex flex-col gap-section">
					<!-- The light: the name it calls itself with its address in parentheses, and both are
					     links to its own interface in a new window. A light that has not answered has no
					     name to show, so the address stands alone — still a link — and says so. -->
					<div class="flex flex-wrap items-center gap-row">
						<h2 class="font-ui text-sm font-medium">
							{#if called !== ''}
								{@render lightLink(one, called, t('devices.open_named', { name: called }), NAME_LINK)}{@render addressInParens(one)}
							{:else}
								{@render lightLink(one, addressOf(one), t('devices.open_address', { address: addressOf(one) }), ADDRESS_LINK)}
								<span class="text-xs font-normal text-wled-text-quiet">{t('devices.no_name_yet')}</span>
							{/if}
						</h2>
						{#if one.bindings.length === 0}
							<Chip>{t('devices.no_printer_badge')}</Chip>
						{/if}
						{#if kind === 'panel'}
							<Chip>{t('devices.shape_panel')}</Chip>
						{:else if kind === 'strip'}
							<Chip>{t('devices.shape_strip')}</Chip>
						{:else}
							<Chip>{t('devices.shape_unknown')}</Chip>
						{/if}
						<Chip tone={reachable ? 'quiet' : 'idle'}>
							{reachable ? t('devices.answering') : t('devices.not_answering')}
						</Chip>
						{#if readAgeOf(one) !== ''}
							<span class="font-ui text-xs text-wled-text-quiet">{t('devices.read_at', { age: readAgeOf(one) })}</span>
						{/if}
					</div>

					{#if !reachable}
						<p class="font-ui text-xs" role="status">{t('devices.not_answering_why', { why: one.segmentsUnread !== '' ? one.segmentsUnread : (held?.presets_unread ?? '') })}</p>
					{/if}

					<!-- The hardware facts, with the source of each said out loud. -->
					<dl class="grid grid-cols-2 gap-x-section gap-y-tight font-ui text-xs md:grid-cols-4">
						<div class="flex flex-col">
							<dt class="text-wled-text-quiet">{t('devices.fact_called')}</dt>
							<dd class="font-medium">
								{#if calledOf(one) !== ''}
									{calledOf(one)}
								{:else}
									<span class="text-wled-text-quiet">{t('devices.fact_called_unknown')}</span>
								{/if}
							</dd>
						</div>
						<div class="flex flex-col">
							<dt class="text-wled-text-quiet">{t('devices.fact_firmware')}</dt>
							<dd class="font-data">
								{#if firmwareOf(one) !== ''}
									{firmwareOf(one)}
								{:else}
									<span class="text-wled-text-quiet">{t('devices.fact_firmware_unknown')}</span>
								{/if}
							</dd>
						</div>
						<div class="flex flex-col">
							<dt class="text-wled-text-quiet">{t('devices.fact_pixels')}</dt>
							<dd class="font-data">
								{#if pixelsOf(one) > 0}
									{#if kind === 'panel'}
										{t('devices.pixels_panel', { width: panelSize(one).width, height: panelSize(one).height, pixels: pixelsOf(one) })}
									{:else}
										{t('devices.pixels_strip', { pixels: pixelsOf(one) })}
									{/if}
								{:else}
									<span class="text-wled-text-quiet">{t('devices.fact_pixels_unknown')}</span>
								{/if}
							</dd>
						</div>
						<div class="flex flex-col">
							<dt class="text-wled-text-quiet">{t('devices.fact_segments')}</dt>
							<dd class="font-data">{one.segmentsKnown ? one.segments.length : t('devices.fact_segments_unknown')}</dd>
						</div>
					</dl>

					<p class="font-ui text-xs text-wled-text-quiet">{t('devices.facts_source')}</p>

					<!-- Its own segments: the list Bindings picks from, shown where it comes from. -->
					<Panel title={t('devices.segments_heading')} pad="none">
						{#snippet meta()}
							<span class="font-ui text-xs text-wled-text-quiet">{t('devices.segments_read_only')}</span>
						{/snippet}
						{#if !one.segmentsKnown}
							<p class="px-panel py-panel font-ui text-xs text-wled-text-quiet">{t('devices.segments_unread', { why: one.segmentsUnread })}</p>
						{:else if one.segments.length === 0}
							<p class="px-panel py-panel font-ui text-xs text-wled-text-quiet">{t('devices.segments_none')}</p>
						{:else}
							<table class="w-full border-collapse text-left font-ui text-xs">
								<thead>
									<tr class="border-b border-wled-line uppercase tracking-label text-wled-text-quiet">
										<th class="px-row py-tight font-medium">{t('devices.segment_id')}</th>
										<th class="px-row py-tight font-medium">{t('devices.segment_name')}</th>
										<th class="px-row py-tight font-medium">{t('devices.segment_bounds')}</th>
										<th class="px-row py-tight font-medium">{t('devices.segment_printer')}</th>
									</tr>
								</thead>
								<tbody>
									{#each one.segments as segment (segment.id)}
										{@const taker = binderOf(one, segment.id)}
										<tr class="border-b border-wled-line last:border-b-0">
											<td class="px-row py-tight font-data">{segment.id}</td>
											<td class="px-row py-tight">{segment.name.trim() === '' ? t('devices.segment_unnamed') : segment.name}</td>
											<td class="px-row py-tight font-data">{boundsOf(one, segment.id)}</td>
											<td class="px-row py-tight">
												{#if taker === undefined}
													{t('devices.segment_free')}
												{:else if taker.printer_name}
													<PrinterLink id={taker.printer} name={taker.printer_name} cls={NAME_LINK} />
												{:else}
													{t('bindings.printer_gone')}
												{/if}
											</td>
										</tr>
									{/each}
								</tbody>
							</table>
						{/if}
					</Panel>

					<!-- Its own presets, read-only. -->
					<Panel title={t('devices.presets_heading')} pad="none">
						{#snippet meta()}
							{#if held?.presets_known === true}
								<span class="font-ui text-xs text-wled-text-quiet">{t('devices.presets_count', { count: held.count })}</span>
							{/if}
						{/snippet}

						{#if (presetsFault[light] ?? '') !== ''}
							<p class="px-panel py-panel font-ui text-xs text-wled-text-quiet" role="status">
								{t('devices.presets_not_served', { why: presetsFault[light] })}
							</p>
						{:else if held !== undefined && !held.presets_known}
							<p class="px-panel py-panel font-ui text-xs text-wled-text-quiet" role="status">
								{t('devices.presets_unread', { why: held.presets_unread ?? held.note })}
							</p>
						{:else if held !== undefined}
							{#if held.presets.length === 0}
								<p class="px-panel py-panel font-ui text-xs text-wled-text-quiet">{t('devices.presets_none')}</p>
							{:else}
								<ul class="flex flex-wrap gap-x-section gap-y-tight px-panel py-row font-data text-xs">
									{#each held.presets as preset (preset.slot)}
										<li>
											<span class="text-wled-text-quiet">{preset.slot}</span>
											<span class={preset.unnamed ? 'text-wled-text-quiet' : ''}>{presetText(preset.name, preset.slot, preset.unnamed)}</span>
										</li>
									{/each}
								</ul>
							{/if}
							<p class="border-t border-wled-line px-panel py-tight font-ui text-xs text-wled-text-quiet">{held.note}</p>
						{/if}

						<p class="border-t border-wled-line px-panel py-tight font-ui text-xs text-wled-text-quiet">
							{t('devices.presets_read_only')}
						</p>
					</Panel>

					<!-- The tests. They paint the light, so the sentence comes before the buttons. -->
					<Panel title={t('devices.tests_heading')}>
						<div class="flex flex-col gap-row">
							<div class="flex flex-wrap items-center gap-row">
								<span class="font-ui text-xs text-wled-text-quiet">{t('devices.tests_before', { hold: HOLD_SECONDS })}</span>
							</div>

							{#if one.bindings.length === 0}
								<p class="font-ui text-xs text-wled-text-quiet">{t('devices.tests_need_printer')}</p>
							{/if}

							<div class="flex flex-wrap items-center gap-tight">
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

							<p class="font-ui text-xs text-wled-text-quiet">{t('devices.identify_no_route')}</p>

							{#if !reachable}
								<p class="font-ui text-xs" role="status">{t('devices.tests_disabled')}</p>
							{/if}
							{#if (testSaid[light] ?? '') !== ''}
								<p class="font-ui text-xs" role="status">{testSaid[light]}</p>
							{/if}
						</div>
					</Panel>

					<!-- Snapshots: take one, list them, and restore from one. -->
					<Panel title={t('devices.snapshots_heading')} pad="none">
						{#snippet meta()}
							<Button variant="secondary" size="sm" disabled={busy[light] === true || !reachable} onclick={() => void takeSnapshot(one)}>
								{t('devices.snapshot_take')}
							</Button>
							{#if kept !== undefined}
								<span class="font-ui text-xs text-wled-text-quiet">{t('devices.snapshots_count', { count: kept.count })}</span>
							{/if}
						{/snippet}

						<div class="flex flex-col gap-row px-panel py-row">
							<p class="font-ui text-xs text-wled-text-quiet">{t('devices.snapshots_when')}</p>

							{#if (snapshotsFault[light] ?? '') !== ''}
								<p class="font-ui text-xs text-wled-text-quiet" role="status">{t('devices.snapshots_unreadable', { why: snapshotsFault[light] })}</p>
							{:else if kept !== undefined && !kept.persisted}
								<p class="font-ui text-xs text-wled-text-quiet" role="status">{t('devices.snapshots_no_folder', { why: kept.note ?? '' })}</p>
							{:else if kept !== undefined && kept.snapshots.length === 0}
								<p class="font-ui text-xs text-wled-text-quiet">{t('devices.snapshots_none')}</p>
							{/if}
						</div>

						{#if kept !== undefined}
							{#each kept.snapshots as snapshot (snapshot.id)}
								{@const detail = detailOf(light, snapshot.id)}
								<div class="flex flex-col gap-tight border-t border-wled-line px-panel py-row font-ui text-xs">
									<div class="flex flex-wrap items-center gap-row">
										<span class="font-data">{snapshot.id}</span>
										<span class="text-wled-text-quiet">{ageText(t, snapshot.taken_at) ?? snapshot.taken_at}</span>
										<span class="font-data text-wled-text-quiet">{sizeText(snapshot.bytes)}</span>
										<span class="text-wled-text-quiet">{t('devices.snapshot_presets', { count: snapshot.presets })}</span>
										<div class="ml-auto flex items-center gap-tight">
											<Button variant="link" size="sm" onclick={() => void openDetail(light, snapshot.id)}>
												{t('devices.snapshot_what_restore')}
											</Button>
											<Button variant="ghost" size="sm" disabled={busy[light] === true || !reachable} onclick={() => askRestore(light, snapshot.id)}>
												{t('devices.snapshot_restore')}
											</Button>
										</div>
									</div>

									<p class="text-wled-text-quiet">{t('devices.snapshot_not_clone', { members: snapshot.omits.map((one) => one.member).join(', ') })}</p>

									{#if detail !== undefined}
										<div class="flex flex-col gap-tight border-t border-wled-line pt-tight">
											<p>
												<span class="font-medium">{t('devices.restore_writes')}</span>
												{detail.restore.sections.join(', ')}
												{#if detail.restore.presets}· {t('devices.restore_presets_too')}{/if}
											</p>
											<p class="text-wled-text-quiet">
												<span class="font-medium">{t('devices.restore_withheld')}</span>
												{detail.restore.withheld.map((one) => one.section).join(', ')}
											</p>
											<p class="text-wled-text-quiet">{t('devices.restore_nw', { members: detail.restore.nw.members_removed.join(', ') })}</p>
											{#if !detail.restorable}
												<p class="text-wled-danger" role="alert">{t('devices.restore_unrestorable', { why: detail.unrestorable ?? '' })}</p>
											{/if}
										</div>
									{/if}
								</div>
							{/each}
						{/if}

						{#if (snapshotSaid[light] ?? '') !== ''}
							<p class="border-t border-wled-line px-panel py-row font-ui text-xs" role="status">{snapshotSaid[light]}</p>
						{/if}
					</Panel>

					<!-- Ignoring: offered on a light nothing is bound to, and off with the reason on one a
					     binding drives — the route refuses that case in the same words, so the button and the
					     refusal cannot disagree. Un-ignoring is always offered, from the Ignored bucket,
					     because the way back has to be as easy as the way in. -->
					<div class="flex flex-wrap items-center gap-row border-t border-wled-line pt-row">
						{#if one.ignored}
							<Button
								variant="secondary"
								size="sm"
								disabled={busy[light] === true}
								onclick={() => void setIgnored(one, false)}
							>
								{t('devices.unignore_button')}
							</Button>
							<span class="font-ui text-xs text-wled-text-quiet">{t('devices.unignore_note')}</span>
						{:else}
							<Button
								variant="ghost"
								size="sm"
								disabled={busy[light] === true || one.bindings.length > 0}
								onclick={() => void setIgnored(one, true)}
							>
								{t('devices.ignore_button')}
							</Button>
							<span class="font-ui text-xs text-wled-text-quiet">
								{one.bindings.length > 0
									? t('devices.ignore_bound', { count: one.bindings.length })
									: t('devices.ignore_note')}
							</span>
						{/if}
					</div>

					{#if (ignoreSaid[light] ?? '') !== ''}
						<p class="font-ui text-xs" role="status">{ignoreSaid[light]}</p>
					{/if}

					<!-- Taking the light off the list. Off while a binding still writes to it, with the
					     reason beside it, and the route refuses it too if it is called some other way. -->
					<div class="flex flex-wrap items-center gap-row border-t border-wled-line pt-row">
						<Button
							variant="ghost"
							size="sm"
							disabled={busy[light] === true || one.bindings.length > 0}
							onclick={() => void takeOff(one)}
						>
							{t('devices.remove_button')}
						</Button>
						<span class="font-ui text-xs text-wled-text-quiet">
							{one.bindings.length > 0 ? t('devices.remove_bound', { count: one.bindings.length }) : t('devices.remove_note')}
						</span>
					</div>

					{#if (removeSaid[light] ?? '') !== ''}
						<p class="font-ui text-xs text-wled-danger" role="alert">{removeSaid[light]}</p>
					{/if}
				</div>
			</Panel>
		{/snippet}

		<!-- The screen, level by level. **Lights** is this page; under it the three buckets; under a
		     bucket, one tab per light. It replaces two stacked areas of cards, which is what he could not
		     tell apart: "the page is very overloaded.. hard to clearly see the different between the
		     lights and such".

		     Every strip is `flex-nowrap` inside `overflow-x-auto` and every tab is `shrink-0`: a strip
		     that grows **scrolls and never wraps**. Nineteen lights in a wrapped strip would rebuild the
		     overload one level down, and his network answers with about nineteen. -->
		<Panel pad="none">
			<div
				class="flex flex-nowrap gap-tight overflow-x-auto border-b border-wled-line px-row py-tight"
				role="tablist"
				aria-label={t('devices.buckets_label')}
				data-strip="buckets"
			>
				{#each BUCKETS as one (one.id)}
					{@const count = inBucket(lights.list, one.id).length}
					<button
						type="button"
						role="tab"
						id={`bucket-${one.id}`}
						aria-selected={bucket === one.id}
						aria-controls="bucket-panel"
						data-bucket-tab={one.id}
						class="{TAB_BAR} {bucket === one.id ? TAB_CURRENT : TAB_RESTING}"
						onclick={() => {
							bucket = one.id;
							selected = '';
						}}
					>
						<span>{t(one.key)}</span>
						<span class="font-normal normal-case tracking-normal text-wled-text-quiet" data-bucket-count={one.id}>{count}</span>
					</button>
				{/each}
			</div>

			<div id="bucket-panel" role="tabpanel" aria-labelledby={`bucket-${bucket}`}>
				<!-- What this bucket holds, in one line, so no tab has to carry an explanation. -->
				<p class="border-b border-wled-line px-panel py-row font-ui text-xs text-wled-text-quiet" data-bucket-note={bucket}>{noteOf(bucket)}</p>

				{#if lightsIn(bucket).length === 0}
					<p class="px-panel py-panel font-ui text-sm text-wled-text-quiet" data-bucket-empty={bucket}>{emptyOf(bucket)}</p>
				{:else}
					<!-- Level three: one tab per light, **keyed by address and labelled by name**. A rename
					     in WLED changes a word on a tab and never moves the tab, and a light that has never
					     answered is titled by its address, which is the only name it has. -->
					<div
						class="flex flex-nowrap gap-tight overflow-x-auto border-b border-wled-line px-row py-tight"
						role="tablist"
						aria-label={t('devices.lights_label')}
						data-strip="lights"
					>
						{#each lightsIn(bucket) as one (addressOf(one))}
							{@const open = currentIn(bucket)}
							<button
								type="button"
								role="tab"
								id={`light-${nameOf(one)}`}
								aria-selected={open !== undefined && addressOf(open) === addressOf(one)}
								aria-controls="light-panel"
								data-light-tab={addressOf(one)}
								title={titledOf(one)}
								class="{TAB_BAR} {open !== undefined && addressOf(open) === addressOf(one) ? TAB_CURRENT : TAB_RESTING}"
								onclick={() => (selected = addressOf(one))}
							>
								{titledOf(one)}
							</button>
						{/each}
					</div>

					{#each openIn(bucket) as open (addressOf(open))}
						<div id="light-panel" role="tabpanel" class="p-panel" data-light-panel={addressOf(open)}>
							{@render card(open)}
						</div>
					{/each}
				{/if}
			</div>
		</Panel>

		<!-- §2.6's swap, once for the page rather than once per card: there is no replace route, and a
		     paragraph explaining a feature that does not exist is an apology, not information about a
		     light. It folds away in the same disclosure Bindings and Diagnostics use, and its summary
		     line says both things a reader needs — what a swap would check, and that it is not built. -->
		<Disclosure summary={t('devices.replace_heading')} hint={t('devices.replace_hint')}>
			<div class="flex flex-col gap-tight font-ui text-xs">
				<p>{t('devices.replace_none')}</p>
				<p class="text-wled-text-quiet">{t('devices.replace_bindings')}</p>
				<p class="text-wled-text-quiet">{t('devices.replace_out_of_scope')}</p>
			</div>
		</Disclosure>

		<!-- The restore dialog. It stays on the page rather than in a modal: it is the warning. -->
		{#if confirming !== null}
			{@const detail = details[`${confirming.light}|${confirming.id}`]}
			<Panel role="dialog" aria-label={t('devices.restore_dialog')}>
				<div class="flex flex-col gap-row">
					<h2 class="font-ui text-sm font-medium">{t('devices.restore_dialog_heading', { light: confirming.light, id: confirming.id })}</h2>
					<p class="font-ui text-sm">{t('devices.restore_warning_destructive')}</p>
					<p class="font-ui text-sm font-medium text-wled-danger" role="alert">{t('devices.restore_never_fired')}</p>
					<p class="font-ui text-xs text-wled-text-quiet">{t('devices.restore_safety')}</p>
					<p class="font-ui text-xs text-wled-text-quiet">{t('devices.restore_nw_warning')}</p>

					{#if detail !== undefined}
						<p class="font-ui text-xs text-wled-text-quiet">
							<span class="font-medium">{t('devices.restore_writes')}</span>
							{detail.restore.sections.join(', ')}
						</p>
						<p class="font-ui text-xs text-wled-text-quiet">
							<span class="font-medium">{t('devices.restore_withheld')}</span>
							{detail.restore.withheld.map((one) => `${one.section} (${one.why})`).join('; ')}
						</p>
					{/if}

					<label class="flex items-center gap-row font-ui text-sm">
						<input type="checkbox" class="h-4 w-4 accent-wled-accent-fill" bind:checked={understood} />
						<span>{t('devices.restore_understand')}</span>
					</label>

					<div class="flex flex-wrap items-center gap-row">
						<Button variant="secondary" size="sm" disabled={!understood || busy[confirming.light] === true} onclick={() => void runRestore()}>
							{t('devices.restore_now')}
						</Button>
						<Button variant="link" size="sm" onclick={() => (confirming = null)}>{t('devices.restore_cancel')}</Button>
					</div>
				</div>
			</Panel>
		{/if}

		<div class="flex flex-col gap-tight font-ui text-xs text-wled-text-quiet">
			<p>{t('devices.note_split')}</p>
			<p>{t('devices.note_segments')}</p>
			<p>{t('devices.note_presets')}</p>
		</div>
	{/if}
</section>
