<script lang="ts">
	// Overview: the one question this screen answers, and the only one it is allowed to.
	//
	// docs/product-requirements.md §2.1, in the operator's own words: *what is on my walls
	// at this moment*. One sentence, one row per light, and nothing that is a second copy
	// of Diagnostics -- no counters, no charts, no rates.
	//
	//   - **The picture is the module's own PNG.** GET /frames answers one frame per
	//     binding with the picture already drawn by the same code that draws the light,
	//     and it is put in an `<img>` as it came. Nothing here redraws it: a second
	//     renderer on the page is a second answer to what is on the wall, and the one on
	//     the page is the one nobody can check against the light.
	//   - **One row per light**, and a row per binding on it, because a segment carries one
	//     printer: a light with two printers is two pictures, and hiding one of them would
	//     hide half of what is on the wall.
	//   - **A pause per row**, beside `Pause everything` in the shell's header: one
	//     mechanism, the same six lengths (pause.svelte.ts).
	//   - **A one-click bind, and the gaps that bind fills stated plainly**: the section is
	//     always on the screen and always lists the printers no binding names and the
	//     lights no binding names. It used to be drawn only when both a printer and a
	//     light were free, which hid exactly the list the operator then asked for.
	//
	// It is refreshed while it is open, because a page that answers "at this moment" and
	// then stops is a page that lies after ten seconds. The refresh keeps the last good
	// frames when a read fails, so a blink does not blank the wall.
	//
	// **The skin.** Every shape here is a primitive: the frames list and the two gap lists
	// are panels, a printer's pause and its write age are chips, the pause and the bind are
	// buttons, and the second weight of text is the quiet token rather than an opacity. The
	// separator between the summary's clauses used to be `opacity-40`, which measures 3.3:1
	// against the canvas — under AA — and is now the tone the guide allows for it.
	import { onMount } from 'svelte';

	import {
		moduleApi,
		type BindingRequest,
		type Frame,
		type PausedBinding,
		type Printer,
		type TimezoneAnswer,
	} from './api';
	import { ageText, describe, lengthText, lightName, segmentText } from './format';
	import { heldHost } from './host';
	import {
		addressOf,
		freeSegments,
		lightTitle,
		lights,
		loadKnownLights,
		nameOf,
		offered,
		roomOn,
		type LightFacts,
	} from './lights.svelte';
	import {
		LENGTHS,
		fateOf,
		everythingPaused,
		pauseOne,
		refreshPauses,
		resumeOne,
	} from './pause.svelte';
	import LightLink from './light-link.svelte';
import PrinterLink from './printer-link.svelte';
	import { Button, Chip, Field, FieldInput, Panel } from './ui';

	const host = heldHost();
	const api = moduleApi(host);
	const t = host.t;

	/** A printer's name, worn as a link to its own page. The address beside it is drawn by
	 *  printer-link.svelte, so both parts are links, which is the shape a light wears on the
	 *  Lights screen. */
	const NAME_LINK = 'underline decoration-dotted underline-offset-2 hover:decoration-solid';

	/** Every picture, one per binding, as GET /frames last answered. */
	let frames = $state<Frame[]>([]);
	/** The sentence GET /frames carries about what a thumbnail is. */
	let pictureNote = $state('');
	/** Why there is no picture at all yet, which is not the same as a broken screen. */
	let waiting = $state('');
	let printers = $state<Printer[]>([]);
	/** The printer picked for a one-click bind, and null when none is. */
	let picked = $state<string | null>(null);
	/** The row whose six lengths are open, and null when none is. */
	let open = $state<string | null>(null);
	let failure = $state('');
	let notice = $state('');
	let loading = $state(true);
	/** True while a pause menu is being obeyed, so one press is one write. */
	let working = $state(false);

	/**
	 * The zone this module judges a brightness window in and writes a clock in, as GET /timezone last
	 * answered, and null before the first read.
	 *
	 * It is on this screen because it is the one thing that has to be set on the day of install and
	 * there is nowhere else to set it: the manifest declares no settings, so a brightness window and a
	 * panel's ends line both refused on every install until this field existed.
	 */
	let zone = $state<TimezoneAnswer | null>(null);
	/** What is in the field, which is the stored zone until somebody edits it. */
	let zoneDraft = $state('');
	/** True while a write is in flight, so one press is one write. */
	let zoneSaving = $state(false);
	/** Why the last write failed, in the route's own words. Nothing is rendered as saved underneath it. */
	let zoneError = $state('');
	/** What the last write did, when it did it. */
	let zoneNotice = $state('');
	/**
	 * True once this page has asked the route, so a capture can never be attempted twice: the refresh
	 * below runs every ten seconds and a second capture is the one thing the browser must not do.
	 */
	let zoneAsked = $state(false);

	/** A light's name as a frame or a binding gives it: the address without the scheme. */
	const keyOf = (frame: Frame): string => frame.light.name !== '' ? frame.light.name : lightName(frame.light.address);

	/** What a person calls the printer, falling back to the id the host gave it. */
	const printerOf = (frame: Frame): string => frame.printer.name !== '' ? frame.printer.name : frame.printer.id;

	/**
	 * The pictures grouped by the light they are on, in the order the module answered.
	 * This is the screen's one structural decision: §2.1 says one row per light, and a
	 * light carrying two printers is two rows under the one light.
	 */
	const groups = $derived.by(() => {
		const held = new Map<string, { light: string; address: string; frames: Frame[] }>();

		for (const frame of frames) {
			const key = keyOf(frame);
			const seen = held.get(key);

			if (seen === undefined) held.set(key, { light: key, address: frame.light.address, frames: [frame] });
			else seen.frames.push(frame);
		}

		return [...held.values()];
	});

	/** One segment in the light's own words, from the light's own list when it answered. */
	const segmentOf = (frame: Frame): string => {
		if (frame.light.segment < 0) return t('common.whole_light');

		const light = lights.list.find((one) => (one.controller?.name ?? '') === keyOf(frame));
		const segment = light?.segments.find((one) => one.id === frame.light.segment);

		return segmentText(t, frame.light.segment, segment?.name, segment);
	};

	/**
	 * Everything that needs the operator, in §2.1's shape: the light's name and how long,
	 * never an error code. A light that would not take a write is the light not answering;
	 * a printer whose newest fact is stale is the printer gone quiet, which is a different
	 * fault and says so.
	 */
	const troubles = $derived.by(() => {
		const said: string[] = [];

		for (const frame of frames) {
			const age = (instant: string | undefined): string => ageText(t, instant) ?? t('common.just_now');

			if (frame.last_send?.ok === false) {
				said.push(t('overview.light_not_answering', { light: keyOf(frame), age: age(frame.last_send.at) }));
				continue;
			}

			if (frame.reading?.stale === true) {
				said.push(t('overview.printer_quiet', { printer: printerOf(frame), age: age(frame.reading.last_change) }));
			}
		}

		return said;
	});

	/** How many bindings are paused on their own account, which the sentence counts. */
	const pausedCount = $derived(
		frames.filter((frame) => fateOf({ light: keyOf(frame), segment: frame.light.segment })?.due_to === 'binding').length,
	);

	/** How long a binding's own pause has left, in words. */
	const pausedForOf = (fate: PausedBinding | undefined): string => {
		if (fate === undefined) return '';

		const left = fate.until === undefined ? undefined : (Date.parse(fate.until) - Date.now()) / 1000;

		return lengthText(t, left, fate.forever === true);
	};

	/**
	 * Every light this screen may offer, which is every light the module knows except the ones the
	 * operator has pushed out of the way on the Lights screen.
	 */
	const offeredLights = $derived(offered(lights.list));

	/**
	 * The printers no binding names: §2.1's first gap list, in his own words.
	 *
	 * Read from the bindings rather than from a printer's own `bound`, because that field
	 * speaks only for the first of several: a printer in two rooms answers with the first
	 * light, and a screen asking "is this printer on anything" has to read GET /bindings.
	 */
	const freePrinters = $derived(
		printers.filter(
			(one) => !offeredLights.some((light) => light.bindings.some((held) => held.printer === one.id)),
		),
	);

	/**
	 * The lights no binding names: §2.1's second gap list, and the answer to "where are the
	 * lights I could put a printer on".
	 *
	 * A light with a printer on one of its six zones **has a binding** and is therefore not
	 * here. That is the distinction that made the old "0 with room" reading useless: room is
	 * not the test, a binding is, and a light wearing one printer on one zone is a light
	 * somebody has already given a job to.
	 *
	 * **And a light pushed out of the way is not here either.** `offeredLights` is the one rule for
	 * "a light this module offers", shared with the Bindings pickers, so ignoring a light on the
	 * Lights screen takes it out of this list without a second decision being written here.
	 */
	const freeLights = $derived(offeredLights.filter((one) => one.bindings.length === 0));

	/**
	 * Whether there is a pair to make, which is when a bind button and the defaults behind it
	 * are worth drawing.
	 *
	 * The section itself is always drawn -- that is his rule of 2026-11-19, and it replaced
	 * "hide it unless both sides are free", which hid the very list he went looking for. What
	 * this decides is narrower: with no printer to pick, a bind button on a light would be a
	 * control that cannot act, so the lights are listed plainly instead.
	 */
	const canBind = $derived(freePrinters.length > 0 && freeLights.length > 0);

	const load = async (quiet = false): Promise<void> => {
		if (!quiet) loading = true;

		try {
			const [pictures, fleet] = await Promise.all([
				// A module that has not drawn a picture yet answers 503, which is a fact about
				// the moment and not a failure of this screen: it is said, not thrown.
				api.listFrames().catch((problem: unknown) => {
					waiting = describe(problem);

					return { frames: [] as Frame[], count: 0, note: '' };
				}),
				api.listPrinters(),
			]);

			frames = pictures.frames;
			pictureNote = pictures.note;
			printers = fleet.printers;
			if (pictures.frames.length > 0) waiting = '';
			failure = '';
		} catch (problem) {
			// A quiet refresh that fails keeps the wall it already drew.
			if (!quiet) failure = `${t('overview.load_failed')}: ${describe(problem)}`;
		} finally {
			loading = false;
		}
	};

	/** Puts one printer on one light with the defaults §2.1 states. */
	const bind = async (one: LightFacts): Promise<void> => {
		if (picked === null) return;

		const room = roomOn(one);
		const body: BindingRequest = { printer: picked, controller: addressOf(one) };

		// The whole light unless a zone is already taken: a whole-light binding and a zone
		// binding are refused beside each other, so the second printer on a light takes the
		// first free segment instead. A brightness is left out on purpose -- the module
		// writes 128 at start unless a day schedule covers the moment, which is exactly the
		// default this page promises.
		if (!room.whole) {
			const first = freeSegments(one)[0];

			if (first === undefined) {
				failure = t('overview.no_segment_free');

				return;
			}

			body.segment = first.id;
		}

		const printer = printers.find((held) => held.id === picked);

		working = true;
		failure = '';
		notice = '';

		try {
			await api.addBinding(body);
			// The light's title in a sentence, which is the same name and bare address a tab wears:
			// a sentence cannot carry two links, and the row it is about is drawn with them below.
			notice = t('overview.bind_done', { printer: printer?.name ?? picked, light: lightTitle(nameOf(one), addressOf(one)) });
			picked = null;
			await Promise.all([load(true), loadKnownLights()]);
		} catch (problem) {
			failure = `${t('overview.bind_failed')}: ${describe(problem)}`;
		} finally {
			working = false;
		}
	};

	/** Pairs every light with no binding with the picked printer, which is what `bind them all` says. */
	const bindEvery = async (): Promise<void> => {
		if (picked === null) return;

		const printer = printers.find((held) => held.id === picked);
		working = true;
		failure = '';
		notice = '';

		try {
			let placed = 0;

			for (const one of freeLights) {
				const body: BindingRequest = { printer: picked, controller: addressOf(one) };
				const first = freeSegments(one)[0];

				if (!roomOn(one).whole && first !== undefined) body.segment = first.id;
				await api.addBinding(body);
				placed += 1;
			}

			notice = t('overview.bind_all_done', { count: placed, printer: printer?.name ?? picked });
			picked = null;
			await Promise.all([load(true), loadKnownLights()]);
		} catch (problem) {
			failure = `${t('overview.bind_failed')}: ${describe(problem)}`;
		} finally {
			working = false;
		}
	};

	const stopRow = async (frame: Frame, length: (typeof LENGTHS)[number]['id']): Promise<void> => {
		open = null;
		working = true;
		await pauseOne(frame.binding, length);
		working = false;
	};

	const resumeRow = async (frame: Frame): Promise<void> => {
		working = true;
		await resumeOne(frame.binding);
		working = false;
	};

	/**
	 * The zone this browser reports, in words a route can store, and '' when it reports nothing.
	 *
	 * A browser with no locale behind it, or one that has been told to leave the zone to the system's
	 * own clock, answers nothing here. That is a fact and not a failure: the field is then left empty
	 * and editable, and no write is made — an empty string sent as a zone would be refused by the route
	 * anyway, and writing a guess would be worse than writing nothing.
	 */
	const browserZone = (): string => {
		try {
			return (Intl.DateTimeFormat().resolvedOptions().timeZone ?? '').trim();
		} catch {
			return '';
		}
	};

	/**
	 * Asks the route once, and captures this browser's zone if the module holds none.
	 *
	 * The rule the operator set: **capture on the first load where nothing is stored, and never again.**
	 * `stored` is what decides it, and it is the route's answer rather than anything this page remembers,
	 * so a reload after a capture finds a stored zone and writes nothing. A failed capture is said plainly
	 * and the field keeps showing what the read answered — which is nothing — rather than the value that
	 * was refused.
	 */
	const loadZone = async (): Promise<void> => {
		// Asked once per page, which is what makes a capture a first-load act rather than a thing the
		// ten-second refresh below could repeat.
		if (zoneAsked) return;

		zoneAsked = true;

		let answer: TimezoneAnswer;

		try {
			answer = await api.readTimezone();
		} catch (problem) {
			zoneError = `${t('overview.timezone_read_failed')}: ${describe(problem)}`;

			return;
		}

		zone = answer;
		zoneDraft = answer.timezone;

		if (answer.stored || answer.timezone !== '') return;

		const here = browserZone();
		if (here === '') return;

		try {
			const written = await api.writeTimezone(here, 'browser');
			zone = written;
			zoneDraft = written.timezone;
			zoneNotice = t('overview.timezone_captured', { zone: written.timezone });
		} catch (problem) {
			// Not saved, and the screen says so: the field still shows the empty zone the read found,
			// which is what the document holds, and the failure is the route's own sentence.
			zoneError = `${t('overview.timezone_capture_failed')}: ${describe(problem)}`;
		}
	};

	/** Stores the zone the operator typed, and renders the answer rather than the value that was sent. */
	const saveZone = async (): Promise<void> => {
		zoneSaving = true;
		zoneError = '';
		zoneNotice = '';

		try {
			const written = await api.writeTimezone(zoneDraft, 'operator');
			zone = written;
			zoneDraft = written.timezone;
			zoneNotice = t('overview.timezone_saved', { zone: written.timezone });
		} catch (problem) {
			// The route's refusal names the value and an example that would be accepted. Nothing is
			// rendered as saved: the field keeps the text, the document keeps the old zone.
			zoneError = `${t('overview.timezone_save_failed')}: ${describe(problem)}`;
		} finally {
			zoneSaving = false;
		}
	};

	/** One sentence saying where the zone in force came from, which is the thing a person needs to trust it. */
	const zoneOriginText = (answer: TimezoneAnswer): string => {
		if (answer.origin === 'none') return t('overview.timezone_none');

		if (answer.origin === 'settings') return t('overview.timezone_from_settings');

		switch (answer.source) {
			case 'browser':
				return t('overview.timezone_from_browser');
			case 'operator':
				return t('overview.timezone_from_operator');
			case 'settings':
				return t('overview.timezone_from_setting');
			default:
				return t('overview.timezone_from_store');
		}
	};

	onMount(() => {
		void Promise.all([load(), loadKnownLights(), refreshPauses(), loadZone()]);
	});

	// The page answers "at this moment", so it keeps answering. Ten seconds is fast enough to
	// watch a print move and slow enough that two lights cost six reads a minute.
	$effect(() => {
		const tick = (): void => {
			if (host.signal.aborted) return;
			void Promise.all([load(true), refreshPauses()]);
		};

		const timer = setInterval(tick, 10_000);

		return () => clearInterval(timer);
	});
</script>

<section class="flex flex-col gap-section">
	<header class="flex flex-col gap-tight">
		<h1 class="font-ui text-lg font-medium">{t('overview.title')}</h1>
		<p class="font-ui text-sm">
			{#if everythingPaused()}
				<span class="font-medium">{t('overview.everything_paused')}</span>
				<span class="text-wled-text-quiet">{t('overview.everything_paused_body')}</span>
			{:else}
				<span class="font-medium">{t('overview.summary_lights', { count: groups.length })}</span>
				{t('overview.summary_showing', { count: frames.length })}
				<span class="text-wled-text-quiet" aria-hidden="true">·</span>
				{#if troubles.length === 0}
					<span class="font-medium">{t('overview.nothing_needs_you')}</span>
				{:else}
					<span class="font-medium">{troubles.join(' · ')}</span>
				{/if}
				{#if pausedCount > 0}
					<span class="text-wled-text-quiet" aria-hidden="true">·</span>
					<span class="font-medium">{t('overview.paused_count', { count: pausedCount })}</span>
				{/if}
			{/if}
		</p>
	</header>

	{#if failure !== ''}
		<Panel>
			<p class="font-ui text-sm text-wled-danger" role="alert">{failure}</p>
		</Panel>
	{/if}

	{#if notice !== ''}
		<Panel>
			<p class="font-ui text-sm" role="status">{notice}</p>
		</Panel>
	{/if}

	{#if loading}
		<p class="font-ui text-sm text-wled-text-quiet">{t('overview.loading')}</p>
	{:else if groups.length === 0}
		<!-- §2.1 and §3: the first run says one sentence about what to do first, not an
		     empty grid, and a module that has just started says what it is waiting for. -->
		<Panel>
			<p class="font-ui text-sm font-medium">{t('overview.first_run')}</p>
			{#if waiting !== ''}
				<p class="mt-tight font-ui text-xs text-wled-text-quiet">
					{t('overview.no_picture_yet', { why: waiting })}
				</p>
			{/if}
		</Panel>
	{:else}
		<Panel pad="none">
			{#each groups as group (group.light)}
				<div class="flex flex-wrap items-center gap-row border-b border-wled-line bg-wled-sunken px-panel py-row">
					<!-- The light's identity, in the one shape a light wears: its name as it calls itself
					     and its address, both links to its own page. It used to be two bare spans here
					     while the printer below it was two anchors, which read as half-finished. -->
					<span class="font-ui text-sm font-medium">
						<LightLink name={group.light} address={group.address} cls={NAME_LINK} />
					</span>
					{#if group.frames.length > 1}
						<Chip>
							{t('overview.printers_on_light', { count: group.frames.length })}
						</Chip>
					{/if}
				</div>
				{#each group.frames as frame (frame.binding)}
					{@const fate = fateOf({ light: keyOf(frame), segment: frame.light.segment })}
					{@const paused = fate?.paused === true}
					<div
						class="flex flex-col gap-row border-b border-wled-line px-panel py-row last:border-b-0 sm:flex-row sm:items-center sm:gap-panel"
					>
						<div class="min-w-0 sm:w-48 sm:shrink-0">
							{#if (frame.image?.base64 ?? '') !== ''}
								<img
									src={`data:${frame.image?.media_type ?? 'image/png'};base64,${frame.image?.base64 ?? ''}`}
									alt={t('overview.thumbnail_alt', { light: group.light, printer: printerOf(frame) })}
									class={frame.kind === 'panel'
										? 'block h-auto w-full max-w-48 rounded-panel bg-wled-pixel-off object-contain [image-rendering:pixelated]'
										: 'block h-3 w-full max-w-48 rounded-panel bg-wled-pixel-off [image-rendering:pixelated]'}
								/>
							{:else}
								<div
									class="flex h-10 w-full max-w-48 items-center justify-center rounded-panel bg-wled-pixel-off px-row text-center font-ui text-xs text-wled-pixel-off-ink"
								>
									{t('overview.picture_missing')}
								</div>
							{/if}
						</div>

						<div class="min-w-0 flex-1 font-ui text-sm">
							<div class="font-medium">
								<PrinterLink id={frame.printer.id} name={printerOf(frame)} cls={NAME_LINK} />
								<span class="font-normal text-wled-text-quiet">· {segmentOf(frame)}</span>
							</div>
							<div class="text-xs text-wled-text-quiet">
								{#if frame.printer.state_known}
									{frame.printer.state}
								{:else}
									{t('overview.state_unknown')}
								{/if}
								{#if frame.frame.percent >= 0}
									· {frame.frame.percent}%
								{/if}
								{#if paused}
									· {fate?.effect === 'hands-back' ? t('overview.handed_back') : t('overview.switched_off')}
								{/if}
							</div>
							{#if frame.note}
								<div class="text-xs text-wled-text-quiet">{frame.note}</div>
							{/if}
						</div>

						<div class="flex flex-col items-start gap-tight sm:items-end">
							{#if paused}
								<Chip>
									{fate?.due_to === 'global'
										? t('overview.paused_with_everything')
										: t('overview.paused_for', { length: pausedForOf(fate) })}
								</Chip>
							{:else}
								<Chip tone={frame.last_send?.ok === false ? 'error' : 'quiet'}>
									{frame.last_send === null
										? t('overview.no_write_yet')
										: ageText(t, frame.last_send.at) === null
											? t('overview.written')
											: t('overview.wrote_ago', { age: ageText(t, frame.last_send.at) ?? '' })}
								</Chip>
							{/if}

							{#if paused && fate?.due_to === 'binding'}
								<Button variant="ghost" size="sm" disabled={working} onclick={() => void resumeRow(frame)}>
									{t('overview.resume')}
								</Button>
							{:else if !paused}
								<Button
									variant="ghost"
									size="sm"
									disabled={working || everythingPaused()}
									aria-expanded={open === frame.binding}
									onclick={() => (open = open === frame.binding ? null : frame.binding)}
								>
									{t('overview.pause')}
								</Button>
							{/if}

							{#if open === frame.binding}
								<div class="flex flex-wrap justify-end gap-tight">
									{#each LENGTHS as length (length.id)}
										<Button variant="ghost" size="sm" disabled={working} onclick={() => void stopRow(frame, length.id)}>
											{t(length.key)}
										</Button>
									{/each}
								</div>
							{/if}
						</div>
					</div>
				{/each}
			{/each}
		</Panel>
	{/if}

	{#if pictureNote !== '' && frames.length > 0}
		<p class="font-ui text-xs text-wled-text-quiet">{pictureNote}</p>
	{/if}

	<!-- §2.1's quick bind, and his rule of 2026-11-19: the section is always drawn, and it
	     always shows the two gap lists — the printers no binding names and the lights no
	     binding names. It used to be hidden unless both sides were free, which hid exactly
	     the list he then went looking for; a side with nothing in it now says so in one
	     line, and the lights are listed plainly when there is no printer to pick, so the
	     section never draws a control that cannot act. -->
	<h2 class="font-ui text-sm font-medium">{t('overview.quick_bind_heading')}</h2>

	<div class="grid gap-section md:grid-cols-2">
		<Panel title={t('overview.printers_heading')} pad="none">
			{#snippet meta()}
				<span class="font-ui text-xs font-normal text-wled-text-quiet">
					{t('overview.printers_count', { count: freePrinters.length })}
				</span>
			{/snippet}
			{#if loading}
				<p class="px-panel py-panel font-ui text-sm text-wled-text-quiet">{t('overview.loading')}</p>
			{:else if printers.length === 0}
				<p class="px-panel py-panel font-ui text-sm text-wled-text-quiet">{t('overview.no_printers')}</p>
			{:else if freePrinters.length === 0}
				<p class="px-panel py-panel font-ui text-sm text-wled-text-quiet">{t('overview.no_free_printers')}</p>
			{:else}
				<!-- The name here stays plain text: this row *is* the control that picks a printer,
				     and an anchor inside a button is invalid HTML and would swallow the press. Where a
				     printer is named as information -- this screen's rows, Bindings' rows, the Lights
				     screen's segment column -- printer-link.svelte draws its name and its address, as
				     both links. -->
				{#each freePrinters as printer (printer.id)}
					<button
						type="button"
						class={picked === printer.id
							? 'flex w-full items-center gap-row border-b border-wled-line bg-wled-sunken px-panel py-row text-left font-ui text-sm last:border-b-0'
							: 'flex w-full items-center gap-row border-b border-wled-line px-panel py-row text-left font-ui text-sm last:border-b-0 hover:bg-wled-hover'}
						aria-pressed={picked === printer.id}
						onclick={() => (picked = picked === printer.id ? null : printer.id)}
					>
						<span class="font-medium">{printer.name}</span>
						{#if printer.model}
							<span class="text-xs text-wled-text-quiet">{printer.model}</span>
						{/if}
						{#if picked === printer.id}
							<span class="ml-auto text-xs text-wled-text-quiet">{t('overview.picked')}</span>
						{/if}
					</button>
				{/each}
			{/if}
		</Panel>

		<Panel title={t('overview.lights_heading')} pad="none">
			{#snippet meta()}
				<span class="font-ui text-xs font-normal text-wled-text-quiet">
					{t('overview.lights_count', { count: freeLights.length })}
				</span>
				{#if canBind}
					<Button
						variant="secondary"
						size="sm"
						disabled={picked === null || working}
						onclick={() => void bindEvery()}
					>
						{t('overview.bind_them_all', { count: freeLights.length })}
					</Button>
				{/if}
			{/snippet}
			{#if !lights.loaded}
				<p class="px-panel py-panel font-ui text-sm text-wled-text-quiet">{t('overview.loading')}</p>
			{:else if lights.failure !== ''}
				<p class="px-panel py-panel font-ui text-sm text-wled-danger" role="alert">
					{t('overview.lights_unreadable', { why: lights.failure })}
				</p>
			{:else if lights.list.length === 0}
				<p class="px-panel py-panel font-ui text-sm text-wled-text-quiet">{t('overview.no_lights')}</p>
			{:else if offeredLights.length === 0}
				<p class="px-panel py-panel font-ui text-sm text-wled-text-quiet">{t('overview.no_offered_lights')}</p>
			{:else if freeLights.length === 0}
				<p class="px-panel py-panel font-ui text-sm text-wled-text-quiet">{t('overview.no_free_lights')}</p>
			{:else}
				{#each freeLights as one (addressOf(one))}
					<div class="flex flex-wrap items-center gap-row border-b border-wled-line px-panel py-row font-ui text-sm last:border-b-0">
						<span class="font-medium">
							<LightLink name={nameOf(one)} address={addressOf(one)} cls={NAME_LINK} />
						</span>
						{#if canBind}
							<span class="ml-auto">
								<Button
									variant="primary"
									size="sm"
									disabled={picked === null || working}
									onclick={() => void bind(one)}
								>
									{picked === null ? t('overview.bind') : t('overview.bind_one', { printer: printers.find((held) => held.id === picked)?.name ?? '' })}
								</Button>
							</span>
						{/if}
					</div>
				{/each}
			{/if}
			<p class="border-t border-wled-line px-panel py-row font-ui text-xs text-wled-text-quiet">
				{t('overview.lights_source')}
			</p>
		</Panel>
	</div>

	<!-- The zone, and nothing else. It is on this screen because it is the one setting that had no
	     surface at all: the manifest declares none, and a brightness window and a panel's ends line both
	     refused on every install until somebody could name a zone. The field shows the value in force and
	     the line under it says where that value came from; the browser fills it once, on the first load
	     that finds nothing stored, and after that only this button writes. -->
	<Panel title={t('overview.timezone_heading')} data-zone-panel="right-now">
		{#snippet meta()}
			{#if zone?.stored}
				<span class="font-ui text-xs font-normal text-wled-text-quiet">{t('overview.timezone_stored')}</span>
			{/if}
		{/snippet}

		<div class="flex flex-col gap-row">
			<Field
				id="overview-timezone"
				label={t('overview.timezone_label')}
				hint={zone === null ? t('overview.timezone_loading') : zoneOriginText(zone)}
			>
				{#snippet children()}
					<div class="flex flex-wrap items-end gap-row">
						<FieldInput
							id="overview-timezone"
							class="w-64"
							value={zoneDraft}
							placeholder={t('overview.timezone_placeholder')}
							disabled={zoneSaving || zone === null || zone.persisted === false}
							aria-invalid={zoneError !== ''}
							oninput={(value) => (zoneDraft = value)}
						/>
						<Button
							variant="primary"
							size="md"
							disabled={zoneSaving || zone === null || zone.persisted === false || zoneDraft.trim() === ''}
							onclick={() => void saveZone()}
						>
							{zoneSaving ? t('overview.timezone_saving') : t('overview.timezone_save')}
						</Button>
					</div>
				{/snippet}
			</Field>

			{#if zone?.persisted === false}
				<p class="font-ui text-xs text-wled-text-quiet">{t('overview.timezone_not_stored')}</p>
			{/if}

			<!-- One line under the field and not three: the hint above already says where the value came
			     from, and a second sentence repeating what the zone is for is the clutter §5 removes. The
			     route's own note is deliberately not drawn here. -->
			{#if zoneError !== ''}
				<p class="font-ui text-sm text-wled-danger" role="alert">{zoneError}</p>
			{:else if zoneNotice !== ''}
				<p class="font-ui text-sm" role="status">{zoneNotice}</p>
			{/if}
		</div>
	</Panel>

	{#if canBind}
		<Panel title={t('overview.defaults_heading')}>
			<ul class="list-disc pl-panel font-ui text-sm">
				<li>{t('overview.defaults_whole')}</li>
				<li>{t('overview.defaults_brightness')}</li>
				<li>{t('overview.defaults_colours')}</li>
				<li>{t('overview.defaults_shape')}</li>
				<li>{t('overview.defaults_events')}</li>
			</ul>
		</Panel>

		<p class="font-ui text-xs text-wled-text-quiet">{t('overview.footnote')}</p>
	{/if}
</section>
