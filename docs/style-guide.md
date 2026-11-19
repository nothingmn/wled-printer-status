---
id: WLED-STYLE-GUIDE
title: Style guide — matching Prusa Connect
status: active
provenance: derived from two image analyses of the host's printer page, plus this project's own constraints
updated: 2026-11-19
---

> **This file is the design contract for this module's screens, and it is a copy.**
> It came from the program workspace — `docs/program/wled-style-guide.md` there — where the
> operator keeps the portfolio's documents, and it is reproduced here verbatim apart from
> this note so a screen is restyled against the document rather than against somebody's
> recollection of the screenshot behind it. It was derived from two analyses of the host's
> own printer page, one in dark mode and one in light.
>
> How to read it here: **§0 outranks the palette.** The hex values in §2 are an acceptance
> reference, not the source of truth — every colour the module paints comes from the host's
> own CSS custom properties, mapped once in `ui/src/tokens.css`, and the only literal value
> in the UI is the one line there marked FALLBACK. §6 is the enforcement: one token file, one
> primitives layer in `ui/src/ui/`, Tailwind configured with the tokens, and the checks a
> reviewer runs. Where this guide disagrees with `docs/product-requirements.md`, the
> requirements win — they are locked — and where it disagrees with what the host actually
> publishes, §0 applies and the host wins.

# Style guide — matching Prusa Connect

**What this is.** The visual contract for the module's screens. The module renders **inside** Prusa Connect's own page, so anything
that does not share that page's surfaces, type, borders, spacing and accent reads as a widget bolted on. This document is what every
screen is restyled against, and what a reviewer checks.

**Where it came from.** The operator sent one screenshot of the host's printer page in **dark** mode and had an AI analyse it; a second
analysis covers **light** mode. Their text is the body of this guide. Two things were changed in the transcription, both marked below:
the hex values are **approximate and must not be hardcoded**, and the geometry is stated as rules rather than adjectives.

---

## 0. The rule that outranks the palette

**The host is the source of truth, not these hexes.** The analyses derived colours "visually from the screenshot", so they are an
**acceptance reference**: a restyled screen should look like this, but the values must come from the host's own CSS custom properties
and the module must follow the host's current theme — including a theme flipped while the page is open.

- Read the host's tokens from the page the module is drawn on (its CSS custom properties, or `@prusa/connect-ui`'s own styles).
- Map them once, in one place, into the module's own token names.
- **No literal colour anywhere else.** A component that writes `#1B1B1D` is a defect even when the value is right, because it stops
  following the host.
- The hexes below are what a *check* compares against, and the fallback when a token is missing.

---

## 1. The aesthetic

Industrial minimalism: a technical control panel, precise and utilitarian, not decorative. Information density with clear separation.
Flat surfaces — **no gradients, no shadows, no glass**. Hierarchy comes from **spacing, typography and contrast**, not ornament.

Dark mode reads as a modern terminal; light mode reads as enterprise engineering software. They are the same design in two skins: the
relationships (accent usage, geometry, type roles) are identical, only the surface and text values change.

## 2. Colour

**Dark**

| Role | Value (reference) |
|---|---|
| Canvas | `#000000` |
| Panel surface | `#1B1B1D` |
| Border | `#303034` |
| Accent | `#FF5A1F` |
| Text | `#E8E8EB` |

**Light**

| Role | Value (reference) |
|---|---|
| Canvas | `#FFFFFF` |
| Sidebar / subtle surface | `#F6F7F9` |
| Border | `#DEE1E7` |
| Accent | `#F4511E` |
| Text | `#202127` |

**Rules**
- **One accent.** It marks the active tab, the progress fill, links, and the primary action. Never a decorative flourish, never two
  accent colours competing on one screen.
- **Status has its own muted family**, deliberately softer than the accent: a muted blue for "printing", a subdued green for "completed",
  red for destructive and for error. Status chips are **soft-filled with darker text**, not saturated blocks.
- **Borders are hairline and low contrast** — 1px, the values above. They separate; they do not decorate.
- **Text has two weights of importance only**: primary and secondary. A third grey for "disabled" is allowed; nothing else.

## 3. Typography

Two families, two jobs — and the split is the identity of the product:

- **Sans-serif** for navigation, section headings, labels, buttons, prose.
- **Monospaced** for machine data: filenames, timestamps, percentages, durations, addresses, ids, and the completion estimate.

Suitable: **Inter** or **IBM Plex Sans** for the interface, **IBM Plex Mono** or **JetBrains Mono** for the machine data. Whatever is
chosen must be the host's own stack if the host exposes one.

- **Navigation labels are uppercase with noticeable letter spacing** and a light weight.
- **Headings are medium weight, not bold.** Size establishes hierarchy far less than spacing and alignment do.
- Never set prose in mono, and never set a filename, percentage or timestamp in sans.

## 4. Geometry and space

- **Almost square corners.** The host's panels are rectangular; a large radius is off-brand. Buttons and inputs are rectangular rather
  than pill-shaped.
- **No elevation.** A panel is a surface plus a 1px border. Nothing floats.
- **Generous space between sections, compact space inside data.** That contrast is what makes the screen readable: the whitespace does
  the grouping, so boxes do not have to.
- **A spacing scale, not one-off values.** Every gap, padding and margin comes from the same small set. A screen with eight different
  paddings is the "packed with content" complaint wearing a different hat.
- **Alignment is a grid.** Rows line up across a panel or they are wrong.

## 5. Components

- **Panels** — flat surface, hairline border, square corners, internal padding from the scale.
- **Tabs** — a horizontal bar of uppercase letter-spaced labels; the active one is marked by a **thin accent underline**, with a hairline
  divider running beneath the whole bar. Not a filled pill.
- **Status chips** — soft-filled, small, uppercase or small-caps, with the darker text of their family.
- **Progress** — a thin track in a muted surface with an accent fill. The number sits beside it in mono.
- **Data rows** — compact, separated by hairlines, with a thumbnail or icon at the left, the machine value in mono, metadata in
  secondary text at the right.
- **Buttons** — primary is an accent fill with white text; secondary is a pale tint of the accent with dark text; destructive is a
  transparent surface with a thin red outline and red text. All rectangular.
- **Inputs and selects** — the surface plus a hairline border, small, with the accent reserved for focus.
- **Disclosures** (our collapsed explanations) — a hairline-topped row with a plain-language summary line and a chevron; the body
  indented to the same grid.
- **Focus rings** — visible in **both** themes, using the accent, and never removed. A control that cannot be seen when focused is not
  finished.

## 6. Standardised CSS — how this is enforced, not just described

The operator asked for a standardised stylesheet, and it is the only way the guide survives contact with seven screens and four agents.

1. **One token file.** CSS custom properties for every colour, space, radius, border and type role, defined for both themes and mapped
   from the host. Components consume tokens; they never write a value.
2. **One primitives layer.** A small set of shared components for the shapes above — panel, row, chip, tab bar, button, field,
   disclosure, progress. A screen that needs a shape composes a primitive or asks for one; it does not restyle from scratch.
3. **Tailwind is configured with the tokens**, so a class name refers to a token rather than an arbitrary value; arbitrary-value
   classes (`bg-[#1B1B1D]`, `p-[13px]`) are the smell that this rule is being broken.
4. **Acceptance checks a reviewer runs:**
   - `grep -rE '#[0-9a-fA-F]{3,8}' ui/src --include=*.svelte` returns **nothing** (the token file is the only place a colour literal
     lives);
   - the same for arbitrary-value spacing classes;
   - both themes rendered, with a theme flip while the page is open;
   - every interactive control reachable and visibly focused by keyboard;
   - text contrast meets **WCAG AA** against its own surface, in both themes.

## 7. What "the whole UI" includes

- Every screen in the module's tab: **Overview, Bindings, Events, Colours, Layout, Lights, Diagnostics**.
- Every **shared component** they are built from — the printer link, the disclosure, the pause control, the thumbnail, the frame sketch.
- **The mockups in `docs/design/v2/`.** They are the approved design record, and if the guide and the mockups disagree, the mockups are
  restyled too — otherwise the next reader is told two different things about what the product looks like.

## 8. What this guide does not change

Behaviour. Not a control moves, not a word changes, nothing is added or removed except by its own entry in the register. **This is a
skin, and a skin that changes behaviour has stopped being a skin.**
