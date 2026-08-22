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

## PAI-811 Agent Mode taste acceptance

The final ImageGen pass was grounded in the certified real-browser Detail
1/10/100 and 390 px captures. These images are taste references, not product
screenshots or implementation authority:

- [Detail 1 focused delivery](pai811-agent-mode-detail-1.png)
- [Detail 10 operational overview](pai811-agent-mode-detail-10.png)
- [Detail 100 aggregate truth](pai811-agent-mode-detail-100.png)
- [Detail 10 mobile density](pai811-agent-mode-detail-10-mobile.png)

Controller taste decision: **accepted as direction, with no redesign**. The
desktop references affirm the existing shell, typography, color, selected-card
hierarchy, stage truth, and attention queue; implementation should change only
spacing and alignment where measured browser geometry requires it. The mobile
reference is accepted specifically for its non-occluding flow: narration
collapses in place, controls follow the selected delivery, and the selected
card plus needs-input queue remain readable without overlap or horizontal
scrolling.

Generated text and pixel geometry are never acceptance evidence. The shipped
Vue DOM, 390/736/1024/wide and 200% browser measurements, reduced-motion tests,
accessibility assertions, and inspected deterministic screenshots remain the
authoritative proof.
