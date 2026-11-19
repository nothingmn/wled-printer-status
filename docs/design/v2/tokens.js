// The mockups' one place to read a value from, beside theme.css.
//
// A mockup draws the *light itself* on a canvas — a colour, a word, a fill — and those colours are
// Appendix A's, not the page's. They live in theme.css with the rest of the tokens (as
// `--mock-light-green` and the `--wled-pixel-off` mat), and this file is how a script reads one
// back as the three numbers a canvas wants. Nothing here invents a value.
//
// Loaded as a classic script, so a mockup still opens by double-clicking it with nothing installed.
(function () {
	/** One token's value, as CSS resolved it. */
	const token = (name) => getComputedStyle(document.documentElement).getPropertyValue(name).trim();

	/** A token that is a hex colour, as the `[r, g, b]` a canvas takes. */
	const rgbOf = (name) => {
		const value = token(name).replace('#', '');
		const at = (from) => Number.parseInt(value.slice(from, from + 2), 16);
		const held = [at(0), at(2), at(4)];

		return held.some((one) => Number.isNaN(one)) ? [0, 0, 0] : held;
	};

	/** A token that is a hex colour, as the string a 2D context takes for a fill or a stroke. */
	const colourOf = (name) => token(name);

	window.TOKEN = { token, rgbOf, colourOf };
})();
