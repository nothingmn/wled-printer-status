## Layout

The one tab whose backend **already exists and is running on the panel** — `8b67c7e` ships a `layout`
setting that composes the steady frame, and this is the screen for it. Approving this is a small job,
not a new feature.

### The controls are the real setting keys

```json
{"bar": true, "bar_band": "bottom", "bar_rows": 12,
 "percent": true, "name": true, "remaining": false, "rotate_seconds": 15}
```

Every control in the mockup writes exactly one of those, and the panel at the bottom of the page shows
the object the module would be given as you change things. No invented keys.

### What is interactive

- **The bar**: on or off, in a band at the top or the bottom, 1–48 rows. The preview redraws, and the
  fit readout tells you what the band leaves for text.
- **The percentage**: on or off, at scale 5, 4 or 3.
- **The job name**: on or off.
- **Percentage and name: `take turns` or `side by side`** — both, configurable. Taking turns swaps the
  two halves every N seconds (what ships today). Side by side puts the digits on the left and the name
  on the right, the name cut to whatever room is left; that one is marked **needs build** because the
  module alternates today.
- `show the other half` flips the preview in take-turns mode, because the two never appear together.

### Two renderings — a panel and a strip

The light selector switches between them, and the two are different screens because the hardware is
different:

- **Panel** (morpheus, 256 × 64) — the composer above. Words and bands.
- **Strip** (CyberDesk, 48 LEDs) — almost nothing, honestly: fills from the left or the right, what 0%
  and 100% do, and brightness only when the whole strip is taken. No bar band, no words, no layers,
  because a strip is one dimension. The mockup says so on the page rather than leaving a blank tab and
  letting someone wonder.

### Two things that are honestly not available

- **Time remaining** is a disabled control. The module refuses `remaining: true` outright ("this build
  draws no time remaining"), so offering a working toggle would be a lie.
- **Layer** is disabled and marked `needs API` — it is not in the setting at all.

### The arithmetic is shown, not hidden

64 rows tall; a 12-row bar leaves 52. Digits at scale 5 are 25×35, so 2–4 characters fit. The name at
scale 2 is 21 characters across 256 columns, cut at its last whole word. **And the refusal works**: set
the bar to 30 rows and the digits no longer fit — the readout turns red and the panel draws nothing
rather than silently cropping.

### Open questions for you

1. **Should the percentage and the name take turns at all?** They alternate today because they share the
   band. Side by side is possible if the digits shrink.
2. **What should an idle light show** — the word `IDLE`, a dim resting colour, or nothing? Today: the
   plain colour, no text.
3. **Per light or one layout for all?** The setting is per binding, so two panels could differ. Worth
   deciding before someone assumes otherwise.
