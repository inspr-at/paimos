# Status icons that move (AEON-1362)

Every status in the app is drawn by one component, `web/src/components/work/StatusIcon.vue` (props `state` and
`size`), following Tick's approved design r4b (spec §5–§7, attached to AEON-1356/1357/1362).

- **In progress** is a ring with 45° stripes that slide sideways slowly and continuously, wherever the icon shows.
  It is the only continuous animation. It is a small HTML shape rather than SVG, because only HTML layers slide on
  the compositor; the slide is transform only.
- **Every other status** plays one short animation (420–560 ms, easing out) when its control is hovered (once per
  pointer entry; moving inside does not replay) or gets keyboard focus: Done and Delivered draw their tick, Accepted
  draws two, Cancelled draws its slash, QA pulses once, New's dot sweeps into place, Open's ring glints, Backlog's
  dots and Blocked's dashes turn into place, Archived's square settles. Only the mark on top animates; the ring,
  disc or square never blanks. A control is the nearest button, link, label, menu item or option around the icon,
  or any element marked `data-status-motion` (the rows of "What the statuses mean").
- **Nothing moves** with `prefers-reduced-motion: reduce`. Slides pause off-screen and in hidden tabs. Only
  transform, opacity and stroke-dashoffset animate, so nothing shifts. 24 sliding icons cost about 0.02 ms of
  main-thread time per frame (budget 1 ms, checked in `web/tests/status-icons.spec.ts`).

## For views that build on it

`web/src/lib/statusMotion.ts` holds the small API; `web/src/styles/status-icons.css` holds the shape and motion
rules.

- `useStatusMotion()` is installed once in `App.vue`; icons need nothing else.
- `statusFill(state)` returns the icon's bar form as classes, `f-<pattern> fc-<status>`: outlines for rings,
  45° stripes for In progress (`f-diag`), solids for filled icons, dotted and dashed outlines, a grey tint with one
  slash for Cancelled, a square outline for Archived. `--st-ok-deep` colours Accepted.
- `f-diag` stripes stay still unless the element also has `f-now` (a live segment, always sliding) or sits inside an
  `f-slide-hover` container (sliding while the pointer is over it). Add `f-short` to a segment under 10 px to drop
  the stripes. Swatches stay still.
- `useOffscreenPause(elementRef)` returns a ref that is true while the element is off-screen; bind it as the
  `offscreen` class on anything that slides continuously.
