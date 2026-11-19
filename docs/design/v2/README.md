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
