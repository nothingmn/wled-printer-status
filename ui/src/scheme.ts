// The colour scheme the module's own controls are drawn in, said in the one place the
// browser reads it.
//
// A `<select>` in a dark page has two halves: the closed control, which is styled by the
// page's own CSS, and the open list, which the browser draws itself. The list is painted
// from the element's *used colour scheme* -- the `color-scheme` property -- and from
// nothing else on the page. This module's palette comes from
// `@prusa/connect-ui/tokens.css`, which switches on the `.dark` class the host puts on
// `<html>`; nothing in that path ever tells the browser which scheme it is looking at,
// so the closed control was right and every option in it was white text on a white
// popup. That is the whole of the defect, and it is a defect in the mechanism, not in
// any one control: ten of the module's thirteen selects were unusable, in every screen.
//
// So the module states the scheme once, on the element every control inherits it from.
// `color-scheme` is an inherited property, which is why one declaration covers every
// select, input and textarea inside that element and nothing has to be said per control.
//
// It is an action and not a Tailwind utility because the module's own stylesheet is
// wrapped in `@scope` by the bundle, so its rules reach only the markup inside the
// module's slot -- and the bind panel is not inside it: the library portals a dialog to
// `<body>`, which is exactly why that panel carries this action on its own body div.
// The host's theme is a class on `<html>`, so it is watched rather than sampled once:
// the person who flips the theme while the page is open gets the right list too.
export const colourScheme = (node: HTMLElement): { destroy: () => void } => {
	const apply = (): void => {
		const dark = typeof document !== 'undefined' && document.documentElement.classList.contains('dark');

		node.style.colorScheme = dark ? 'dark' : 'light';
	};

	apply();

	if (typeof MutationObserver === 'undefined') return { destroy: () => {} };

	const watcher = new MutationObserver(apply);
	watcher.observe(document.documentElement, { attributes: true, attributeFilter: ['class'] });

	return { destroy: () => watcher.disconnect() };
};
