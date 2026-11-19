<script lang="ts">
	// A printer, named the way the Lights screen names a light:
	// `Hermes MK4 (http://10.0.0.160:8080/printers/07e893bb-...)`.
	//
	// **Both parts are links**, to that printer's own page in Prusa Connect, in a new window --
	// the shape the operator set for a light on the Lights screen, applied to the other kind of
	// device because a name is a way through to the thing whichever kind it is. Each anchor has
	// its own accessible name: the second one's visible text is a machine address, and a screen
	// reader given one long name for the pair could not tell the two ways through apart. The
	// brackets between them are drawn and hidden from a screen reader, because `left paren` read
	// out between two named links is noise.
	//
	// **The address is built, never written.** `links.ts` reads this page's own origin and the
	// root of the app it is drawn under, so a Connect behind a `/connect/` prefix gives
	// `https://host/connect/printers/<id>` with no change here, and no address of one install is
	// baked into the bundle for every other one to be wrong about.
	//
	// **It has to survive a narrow screen.** The address is far longer than the name beside it,
	// so it is allowed to break at any character (`break-all`) and to fall to the next line as a
	// whole: it wraps inside the row it is in, and never widens that row, overflows it, or shoves
	// the controls beside it apart.
	//
	// Six screens name a printer and every one of them means the same thing by it, so the pair is
	// written here once. The one place a printer's name is not this component is a control that
	// *picks* a printer -- Overview's quick-bind picker, the binding dialog's printer field --
	// where the name stays plain text because an anchor inside a button is invalid HTML and would
	// swallow the press. Those two places say so in a comment of their own.
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
		/** The name a person reads, which is also the first anchor's visible text. */
		name: string;
		/** The classes the name wore as plain text, so a link sits where the name did. */
		cls?: string;
	} = $props();

	/** That printer's page, from this page's own address: never a host written into the bundle. */
	const url = $derived(printerUrl(id));

	/**
	 * The address, one step smaller in the controller's own monospace, and breakable: it is the
	 * longest thing in any row it sits in, and `break-all` is what keeps it inside that row.
	 */
	const ADDRESS_LINK =
		'break-all font-data text-xs font-normal underline decoration-dotted underline-offset-2 hover:decoration-solid';
</script>

<span
	><a href={url} target="_blank" rel="noopener" aria-label={t('printer.open_in_connect', { printer: name })} class={cls}
		>{name}</a
	><span class="font-normal text-wled-text-quiet" aria-hidden="true">{' ('}</span
	><a
		href={url}
		target="_blank"
		rel="noopener"
		aria-label={t('printer.open_address', { address: url })}
		class={ADDRESS_LINK}>{url}</a
	><span class="text-wled-text-quiet" aria-hidden="true">{')'}</span
	></span
>
