// The module's primitives: the shapes every screen is built from, and the only
// place one is defined.
//
// Eight of them, one per shape the style guide names (§5): a panel, a data row,
// a status chip, a tab bar (which is also the rail), a button, a field, a
// disclosure and a progress bar. Each consumes tokens only — no component here
// writes a colour, a radius or a spacing value of its own, so a theme the host
// flips reaches every one of them and this folder is the one place a shape
// changes.
//
// `Field` is the primitive; `FieldInput` and `FieldSelect` are its two controls,
// exported because an input and a select carry different attribute maps and a
// screen with an attribute `Field` does not offer should compose the control
// rather than cast one.
//
// A screen that needs a shape composes one of these or asks for one; it does not
// restyle from scratch. That is the whole of §6's second rule.
export { default as Button } from './button.svelte';
export { default as Chip } from './chip.svelte';
export { default as Disclosure } from './disclosure.svelte';
export { default as Field } from './field.svelte';
export { default as FieldInput } from './field-input.svelte';
export { default as FieldSelect } from './field-select.svelte';
export { default as Panel } from './panel.svelte';
export { default as Progress } from './progress.svelte';
export { default as Row } from './row.svelte';
export { default as Tabs } from './tabs.svelte';

// What a tab looks like, for the one screen that draws a nested `tablist` of its own (`Tabs`
// is the bar itself, and is what draws the rail).
export { TAB_BAR, TAB_CURRENT, TAB_RESTING, TAB_UPRIGHT } from './tab-classes';
