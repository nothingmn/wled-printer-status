<script lang="ts">
	// One state, drawn the way the module draws it: on a panel and on a strip.
	//
	// docs/product-requirements.md §2.4 asks for each state twice, because the two lights in this
	// house are two different things — a 256 × 64 panel that can carry a word, and a 48-pixel
	// strip that cannot — and a strip owner reading a page that only shows panels would be
	// reading somebody else's screen.
	//
	// The shapes are the module's, not this component's: `glyphs.ts` carries the same 5 × 7 font
	// internal/text draws with, at the same scale event.go uses, and refuses a word exactly where
	// the module refuses one. So a word that does not fit is drawn as the colour here for the
	// same reason it would be drawn as the colour on the wall, and `colours.svelte` says why
	// beside it rather than leaving a reader to guess.
	//
	// Nothing here calls a route. The module's preview routes paint a real light, and an editor
	// must not; the picture is the browser's own reading of the same table.
	import type { FrameColour } from './api';
	import { heldHost } from './host';
	import { PANEL, STRIP, WORD_SCALE, drawWord } from './glyphs';

	interface Props {
		/** The colour in force, as the draft has it — so a picker repaints the preview live. */
		colour: FrameColour;
		/** The word the state carries, empty for none. */
		word: string;
		/**
		 * True when the word is not on a light yet — a state no light is showing. The word is
		 * still drawn, because it is what the light will show, but dimmed, because it is not
		 * showing now and a preview that looked identical would be a claim about the wall.
		 */
		pending?: boolean;
	}

	let { colour, word, pending = false }: Props = $props();

	const t = heldHost().t;

	let panel = $state<HTMLCanvasElement | null>(null);
	let strip = $state<HTMLCanvasElement | null>(null);

	/** The colour as CSS writes one, which is also what the canvas takes. */
	const painted = $derived(`rgb(${colour.r} ${colour.g} ${colour.b})`);

	/** A word is only drawn when there is one, and only in the colour it will be drawn in. */
	const spoken = $derived(word.trim() === '' ? '' : word.trim());

	$effect(() => {
		const surface = panel;
		if (surface === null) return;

		const context = surface.getContext('2d');
		if (context === null) return;

		// An unlit pixel is black — render.Frame{}.Unfilled(), which is what the module paints
		// behind a word — and a state without a word is the colour over the whole rectangle.
		// The black is the token layer's (`--wled-pixel-off`), read from this element rather
		// than written here: a colour literal in a screen is the defect style guide §0 names,
		// and this one line was the module's last. Empty means the token is missing, and a
		// canvas left unfilled is better than a screen inventing a value of its own.
		const off = getComputedStyle(surface).getPropertyValue('--wled-pixel-off').trim();
		if (off !== '') {
			context.fillStyle = off;
			context.fillRect(0, 0, PANEL.width, PANEL.height);
		}

		context.globalAlpha = pending ? 0.45 : 1;
		context.fillStyle = painted;

		if (spoken === '') context.fillRect(0, 0, PANEL.width, PANEL.height);
		else if (!drawWord(context, spoken, WORD_SCALE, painted, PANEL)) {
			// The module refuses this word on this light rather than shrinking it, so the panel
			// shows the colour. Drawing a fitted word here would be a picture of nothing.
			context.fillStyle = painted;
			context.fillRect(0, 0, PANEL.width, PANEL.height);
		}

		context.globalAlpha = 1;
	});

	$effect(() => {
		const surface = strip;
		if (surface === null) return;

		const context = surface.getContext('2d');
		if (context === null) return;

		// A strip is one run of pixels and has no second dimension, so a word never lands on it:
		// §2.3's fallback is the moment's colour and §2.4's is the state's, which is this.
		context.globalAlpha = pending ? 0.45 : 1;
		context.fillStyle = painted;
		context.fillRect(0, 0, STRIP, 6);
		context.globalAlpha = 1;
	});
</script>

<div class="flex flex-wrap items-end gap-3">
	<figure class="m-0 flex flex-col gap-1">
		<!--
			The picture is decorative and the sentence is not: a screen reader is given the label on
			the wrapper, because a `<canvas>` cannot carry `role="img"` of its own.
		-->
		<div
			role="img"
			aria-label={spoken === ''
				? t('colours.panel_plain', { colour: painted })
				: t('colours.panel_word', { colour: painted, word: spoken })}
		>
			<canvas
				bind:this={panel}
				width={PANEL.width}
				height={PANEL.height}
				class="block h-[64px] w-[256px] max-w-full rounded bg-black [image-rendering:pixelated]"
				aria-hidden="true"
			></canvas>
		</div>
		<figcaption class="text-[10px] uppercase tracking-wide opacity-60">{t('colours.panel_caption')}</figcaption>
	</figure>

	<figure class="m-0 flex flex-col gap-1">
		<div role="img" aria-label={t('colours.strip_plain', { colour: painted })}>
			<canvas
				bind:this={strip}
				width={STRIP}
				height={6}
				class="block h-[12px] w-[96px] max-w-full rounded bg-black [image-rendering:pixelated]"
				aria-hidden="true"
			></canvas>
		</div>
		<figcaption class="text-[10px] uppercase tracking-wide opacity-60">{t('colours.strip_caption')}</figcaption>
	</figure>
</div>
