# AI UX Spec

`PAI-201` design reference for the v2 AI assist layer.

## Intent

The AI layer should read like an editorial compile strip:

- no chat bubbles
- no celebratory motion
- visible telemetry
- compact inline decisions

## States

### Activity

- `idle`: no extra chrome
- `pending`: no strip before `250 ms`
- `working`: strip shows action title, phase, elapsed time, action key
- `stalled`: same strip, plus slower-provider note
- `failed`: muted red strip with direct error line
- `cancelled`: same visual family as failed

### Result

- result strips sit directly under the initiating control or field
- summary copy must be countable and action-specific
- result rows may auto-dismiss after `12 s` if no decision is required
- detail-heavy payloads still use the existing modal path

### Decision

- primary action first
- secondary actions stay ghosted
- keyboard intent:
  - `Enter` = primary
  - `Esc` = dismiss

## Typography

- display: `Bricolage Grotesque`
- body: `DM Sans`
- chrome and telemetry: `DM Mono` or `JetBrains Mono`

## Color

- active/info: existing `--brand-blue*`
- muted chrome: `--text-muted`
- failure: current app red family only

## Motion

- easing: `cubic-bezier(.2, .7, .1, 1)`
- duration: `<= 250 ms`
- reduced-motion: instant state changes

## A11y

- activity strips use `role="status"` and `aria-live="polite"`
- decorative sweep bars stay `aria-hidden`
- decision controls stay keyboard reachable without pointer-only affordances

## Flow

```text
idle
  -> pending
  -> working
    -> stalled
    -> failed
    -> result
      -> decision
        -> applied
        -> dismissed
```

## PAI-811 Agent Mode taste-review inputs

PAI-811 owns four ImageGen taste inputs as governed PPM attachments rather
than generated files in this repository:

- attachment `#231`: Detail 1 focused delivery
- attachment `#230`: Detail 10 operational overview
- attachment `#228`: Detail 100 aggregate truth
- attachment `#229`: 390 px Detail 10 mobile direction

The three desktop inputs preserve the established shell, typography, color,
selected-card hierarchy, stage truth, and attention queue. They are direction
for human taste review, not implementation authority or evidence that a
candidate matches exact pixel geometry.

The mobile input is deliberately looser: it expresses density, hierarchy, and
readability direction, while the real responsive composition keeps the
existing Detail-10 information architecture. At phone width the app header
retains feed authority, narration and controls reflow in document order, the
redundant feed dock is hidden, and the selected delivery remains the primary
surface. That is not the same layout as attachment `#229`, and no parity,
correctness, or automated browser result should be described as a human taste
acceptance.

Candidate acceptance state, review decisions, and screenshots belong to the
PAI-811 PPM comments and attachments. Generated text and pixels are never
acceptance evidence. The shipped Vue DOM, exact browser geometry,
reduced-motion checks, accessibility assertions, and deterministic screenshots
provide candidate correctness evidence only; the reusable procedure and claim
boundaries live in [`../../VISUAL_VERIFY.md`](../../VISUAL_VERIFY.md).
