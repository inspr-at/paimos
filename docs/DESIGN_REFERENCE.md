# Paimos design reference

The user selected these original assets on 2026-09-06 as the baseline for every future Paimos design, color and style choice. This direction supersedes the earlier forest palette and old logo shape.

## Authoritative assets

Original directory: `/Users/markus/Library/CloudStorage/GoogleDrive-markus.barta@augmentoring.com/Meine Ablage/Augmentoring/Design/Products/paimos/`.

| Original          | Repository copy                                                                     | SHA-256                                                            |
| ----------------- | ----------------------------------------------------------------------------------- | ------------------------------------------------------------------ |
| `paimos-hero.png` | [`frontend/public/brand/paimos-hero.png`](../frontend/public/brand/paimos-hero.png) | `f9467ce93d4d076a9dd9e555ae76047077171a401048fdca828634ff379588ba` |
| `paimos-logo.svg` | [`frontend/public/brand/paimos-logo.svg`](../frontend/public/brand/paimos-logo.svg) | `970f026a738885b60bf9fac122b3563fe2b9fe242199845920acdc3975628396` |

Both copies are byte-identical originals. Public `logo.svg`, `favicon.svg` and `app-icon.svg` are byte-identical aliases of the supplied mark for existing application/default-branding URLs. Explicitly configured customer branding remains supported.

## Palette and material

View the actual hero before making visual decisions: pearl and ivory marble, luminous aqua glass, fine gold connections, an open architectural gathering place. Use these materials through restrained native surfaces, clear hierarchy and light, rather than drawing an ornamental network over the work.

| Role                 | Bright          | Dark counterpart                        |
| -------------------- | --------------- | --------------------------------------- |
| Logo teal, immutable | `#0E6F6C`       | Same supplied fill, with pearl backing  |
| Logo gold, immutable | `#D69B31`       | Same supplied fill                      |
| Canvas               | Pearl `#F7F6F2` | Deep blue teal `#102327`                |
| Reading surface      | Ivory `#FFFEFA` | `#183034`                               |
| Main text            | `#203C3D`       | `#EDF4F0`                               |
| Supporting text      | `#596E70`       | `#ACC3C2`                               |
| Interactive accent   | Teal `#0E6F6C`  | Aqua `#A4E5DF`                          |
| Glass tint           | `#C7EFEC`       | Subdued aqua light over the dark canvas |

Canonical brand tokens live in `frontend/src/brand/paimos.css`; Habitat's semantic day/night tokens and surfaces live in `frontend/src/components/habitat/habitat.css`. The legacy token name `--h-mint` now denotes the teal/aqua interaction accent.

## Rules

- Preserve the supplied SVG geometry, square viewBox and two fills. Do not redraw, invert, grayscale, recolor or replace it with the old P-shaped mark. Use pearl backing when contrast requires it; do not dim the mark through inherited opacity.
- Keep reading surfaces solid. Use aqua light at their edges, subtle elevation and fine gold details. Gold is an accent, not small text on ivory. Verify text contrast in both themes.
- Use the original hero selectively as decorative artwork, never as evidence of real workers or topology. It may appear in setup's illustration area; hide it when space is needed for controls. Do not enlarge it into a marketing header above operational work.
- Keep useful work, one clear next action, current identity and security state visible. Preserve responsive reflow, keyboard focus, reduced motion, inspection, voice and all command authorization boundaries.
- Presence must follow authoritative evidence. Unknown and stopped generations stay visually quiet; intentional stops are labeled Stopped. Gold lines and aqua animation must not invent activity, parent relationships or recovery needs.
- Extend this baseline for future screens. Do not restore the earlier forest colors or introduce a competing visual system without a new user direction.
