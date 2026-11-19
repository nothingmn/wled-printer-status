# v2 design mockups

These are **design mockups, not the product**: eight throwaway HTML pages, one per screen of the locked
requirements, each opened by double-clicking its `index.html` in a browser with nothing installed. They
exist so the operator could look at a screen and say yes or no before anything was built — what the module
actually serves is a Go program, and no page here is copied from it or into it. Each folder's `README.md`
records the decisions behind that screen and, where he gave them, the operator's own words, so a mockup and
its reasoning stay together. Four earlier whole-page cuts were superseded during the review and are not
carried: a tabbed control panel, a light-first drill-in, a full tabbed walk, and an every-feature tour.

| Folder | Requirement | Original sketch |
| --- | --- | --- |
| [`overview/`](overview/) | 2.1 Right now (Overview) | `012-overview` |
| [`bindings/`](bindings/) | 2.2 Bindings | `006-bindings` |
| [`events/`](events/) | 2.3 Events | `005-events-to-presets` |
| [`colours/`](colours/) | 2.4 Colours | `008-colours` |
| [`layout/`](layout/) | 2.5 Layout | `007-layout` |
| [`devices/`](devices/) | 2.6 Devices | `009-devices` |
| [`diagnostics/`](diagnostics/) | 2.7 Diagnostics | `010-diagnostics` |
| [`settings/`](settings/) | 2.8 Settings | `011-settings` |

The requirements themselves are [`../../product-requirements.md`](../../product-requirements.md).

## The mockups take the module's values, not their own

These pages were drawn before the design system existed, so each carried its own palette, 7-to-10px
corners, pill buttons and drop shadows. They were restyled onto the style guide in the same wave that moved
the module's seven screens onto it, because **the mockups and the guide disagreeing tells the next reader
two different things about what the product looks like** (style guide §7).

| File | What it is |
| --- | --- |
| `theme.css` | the module's tokens — the same names as `ui/src/tokens.css`, carrying the values those tokens resolve to on the operator's install, for both themes. Every mockup links it. |
| `tokens.js` | how a mockup's script reads a value back — the light's own colours (Appendix A) and the unlit mat — so no page invents one. |
| `check-theme.mjs` | the check that keeps the two token lists in step. |

A mockup now links `../theme.css` and `../tokens.js` and writes **no colour, radius, space or type value
of its own**: `grep -rE '#[0-9a-fA-F]{3,8}' docs/design/v2/*/index.html` returns nothing. Both themes are
in the one sheet — the light values are the default, and `prefers-color-scheme: dark` or `class="dark"` on
`<html>` switches to the dark ones — which is the same two-skin rule the module follows and the same skins
the host flips between.

### How a future change stays in step

1. **Add the token to `ui/src/tokens.css` first.** That is the file the module paints from, and a shade a
   mockup needs but the module does not have is a token that is missing, not a value to type here.
2. **Copy its resolved value into `theme.css`**, in the `:root` block and in the `.dark` block — read from
   the running page in both themes, not from memory.
3. **Run `node docs/design/v2/check-theme.mjs`.** It compares the two lists of `--wled-…` names and exits
   non-zero with the names that are in one file and not the other, so step 2 cannot be half done:

   ```
   module tokens (ui/src/tokens.css): 51
   mockup tokens (docs/design/v2/theme.css): 51
   missing from the mockups: 0
   missing from the module: 0
   total_errors: 0
   ```

The values are checked by eye because they are a transcription of what a host resolves to, which is a fact
about one install; the names are checked by the script because they are the thing that drifts silently.
