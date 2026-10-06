# PAIMOS UI conventions

This is the tracked source for UI and taste rules. Apply it to every UI change,
alongside the safety and ownership rules in [AGENTS.md](../AGENTS.md).

## Design and materials

- Implement the approved Opus design named in the brief closely. Without one,
  build only the specified UI, reuse components and note "needs Opus design" in the summary.
- Reuse existing PAIMOS components before creating new ones.
- Use shared tokens and styles from [tokens.css](../web/src/styles/tokens.css)
  and [base.css](../web/src/styles/base.css). Design light and dark; do not just invert colours.
- Use hairlines, subtle full tints, whitespace, type weight and a fitting metaphor.
- No coloured edge accents: no coloured bars or thick borders on a row, card,
  callout, toast or panel's left or top edge to mark selection, emphasis or state.
- No decorative side frames, decorative glows, gradient blobs or grey-pill soup.
  Do not put forced pills or grey rounded rectangles around everything.
- Use centred SVG icons, never text glyphs.
- Use plain words. Explain who may see or do something before they try.
  Release names are marketing names, never version numbers.

## Controls stay put (AEON-541)

- Prefer a frame anchored at a fixed top position, with navigation, choices and
  actions above content that grows downward. Short content stays short; no padded fixed heights.
- For long content, use a pinned action footer and scrolling body. Phones use
  full-height sheets with pinned headers/actions and safe areas; no free-floating buttons.
- Selecting, hovering, typing or toggling never moves or resizes controls or their neighbours.
  Loading, empty, permission and error states replace content in place; feedback never pushes controls.
- Keep selector rows fixed in height; explanations occupy a reserved slot below the list.
- Keep action positions and focus through a whole series. A series with an already
  scrolling body keeps the same frame size. Decide widths on opening or resizing, not during use.

## Text, space and phones

- Avoid fixed pixel widths for controls or frames with variable text; size to content
  with sensible limits, `clamp()` and container queries. Use wide screens when available.
- Fit labels in every language, including long German text; stack buttons when needed.
- Wrap headings up to two lines. List names use the shared clip-tip to expose full text
  on hover, focus or tap. Never clip reasons, requests or options while someone decides.
- Show whole list rows and make scrolling visible. Test phones at 390 px; every feature
  remains available on phones and small tablets, with a replacement if its desktop form cannot fit.
- On coarse pointers, touch targets are at least 44 px without overlapping neighbouring targets.

## Keyboard

- Single-letter shortcuts work only outside text fields. Submit from a field with
  ⌘↵ on macOS or Ctrl+↵ elsewhere; detect the platform and match labels.
- Esc leaves a field, then closes on the next press. Preserve browser and OS shortcuts,
  including ⌘S/⌘R/⌘D/⌘P/⌘A and Ctrl+A. Show keycaps on the buttons they trigger.

## Evidence

- Use the reusable Playwright stability guard for every changed view: measure named actions,
  selectors, selector groups and the clicked row before/after each option interaction and series step (±0.5 px).
- Require at least one interaction and positive-size samples; measure user scrolling in
  scroll-container coordinates and reject horizontal overflow. Assert frame height only for
  phone sheets or already scrolling bodies; top-anchored frames may grow downward.
- Capture every changed view at 390, 1024 and 1440 px in light and dark, with long German
  text where length matters. Save under `web/test-results/<slug>/`, name them in the summary, never commit them.
- Run targeted Playwright one spec at a time with `--workers=1`, under the assigned worker's test rules.
