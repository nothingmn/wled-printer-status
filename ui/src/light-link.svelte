<script lang="ts">
	// A light, named the way a printer is: `morpheus (http://10.0.0.136)`.
	//
	// **Both parts are links**, in a new window, to the light's own page at its address — the same
	// shape `printer-link.svelte` draws for a printer, and for the same reason: a name is a way
	// through to the thing. The target is a judgement worth stating where somebody will read it:
	// Prusa Connect has no page for a WLED controller, so the only page a light has is its own, and
	// the link goes there. Each anchor keeps its own accessible name, because the second one's
	// visible text is a machine address and a reader given one long name for the pair could not tell
	// the two ways through apart; the brackets between them are drawn and hidden, since
	// `left paren` read out between two named links is noise.
	//
	// **The name is what the light calls itself**, read from the module's own light record
	// (`GET /lights`, `calls_itself`) — never the route name, which is only the address without its
	// scheme, and never anything typed here. A light that has never answered has no name to show, so
	// its address stands alone as one link and the screen that wants to add the sentence "it has not
	// answered yet" does so beside this component rather than inside it.
	//
	// **It has to survive a narrow screen.** The address is far longer than the name beside it, so
	// it is allowed to break at any character (`break-all`) and wraps inside the row it is in,
	// rather than widening that row or shoving the controls beside it apart.
	//
	// Five screens name a light in a row and every one of them means the same thing by it, so the
	// pair is written here once — Overview's binding rows and its lights-with-no-printer list,
	// Bindings' cards, Events' per-light header, Diagnostics' per-binding header, and the Lights
	// screen's card. The one place a light's name is not this component is a control that *picks*
	// one — a `<select>` option or a tab — where the title stays plain text because an anchor
	// inside a control swallows the press. Those places say so in a comment of their own.
	import { lightName } from './format';
	import { heldHost } from './host';
	import { calledNameOf } from './lights.svelte';

	interface Props {
		/** The light's name on this module's routes: its address without the scheme. */
		name: string;
		/** Its base URL, as the operator wrote it. This is where both links go. */
		address: string;
		/** The classes the name wore as plain text, so a link sits where the name did. */
		cls?: string;
	}

	let { name, address, cls = '' }: Props = $props();

	const t = heldHost().t;

	/** The bare address, which is the light's only name before it answers. */
	const bare = $derived(name !== '' ? name : lightName(address));

	/** What the light calls itself, and empty for one that has never answered. */
	const called = $derived(calledNameOf(name, address));

	/**
	 * The light's own page: its address, given a scheme when it was stored without one, and built
	 * out of the stored address rather than an address written into this bundle.
	 */
	const url = $derived.by(() => {
		const written = address.trim();
		if (written === '') return `http://${bare}`;

		return /^[a-zA-Z][a-zA-Z0-9+.-]*:\/\//.test(written) ? written : `http://${written}`;
	});

	/**
	 * The address, one step smaller in the controller's own monospace, and breakable: it is the
	 * longest thing in any row it sits in, and `break-all` is what keeps it inside that row.
	 */
	const ADDRESS_LINK =
		'break-all font-data text-xs font-normal underline decoration-dotted underline-offset-2 hover:decoration-solid';
</script>

{#if called === ''}
	<!-- A light that has never answered has no name of its own. Its address is what it has, and a
	     row says that address once rather than the same address twice. -->
	<a
		href={url}
		target="_blank"
		rel="noopener"
		aria-label={t('light.open_address', { address: url })}
		class={ADDRESS_LINK}>{url}</a
	>
{:else}
	<span
		><a
			href={url}
			target="_blank"
			rel="noopener"
			aria-label={t('light.open_page', { light: called })}
			class={cls}>{called}</a
		><span class="font-normal text-wled-text-quiet" aria-hidden="true">{' ('}</span
		><a
			href={url}
			target="_blank"
			rel="noopener"
			aria-label={t('light.open_address', { address: url })}
			class={ADDRESS_LINK}>{url}</a
		><span class="text-wled-text-quiet" aria-hidden="true">{')'}</span
		></span
	>
{/if}
