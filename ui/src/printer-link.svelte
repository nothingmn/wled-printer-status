<script lang="ts">
	// A printer's name, as a link to that printer's own page in Prusa Connect.
	//
	// Five screens name a printer and every one of them means the same thing by it, so
	// the anchor is written here once: `target="_blank"` with `rel="noopener"`, and an
	// accessible name that says what will open, because the visible text is only the
	// printer's name. The address is built from the page this bundle is drawn on
	// (links.ts) and never from a host written into this repository -- a hard-coded
	// address would be wrong on every other install.
	//
	// The same shape the Lights screen uses for a light, deliberately: a name is a way
	// through to the thing, on both kinds of device.
	import { heldHost } from './host';
	import { printerUrl } from './links';

	const t = heldHost().t;

	let {
		id,
		name,
		cls = '',
	}: {
		/** The host's own device id for the printer. */
		id: string;
		/** The name a person reads, which is also the anchor's visible text. */
		name: string;
		/** The classes the name wore as plain text, so a link sits where the name did. */
		cls?: string;
	} = $props();
</script>

<a href={printerUrl(id)} target="_blank" rel="noopener" aria-label={t('printer.open_in_connect', { printer: name })} class={cls}
	>{name}</a
>
