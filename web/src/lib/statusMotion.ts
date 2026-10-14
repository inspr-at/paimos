// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1362 (design r4b §5–§7): status icon motion and the icons' bar form.
//
// The API is small on purpose; the work-at-a-glance views (AEON-1358/1359) build on it:
// - <StatusIcon :state :size> renders an icon. Its motion needs nothing else.
// - useStatusMotion(): call once, in App.vue. When the control around a status icon is hovered (once per pointer entry)
//   or gets keyboard focus, the icon plays its one-off. It also marks hidden tabs with `tab-hidden` on <html>.
// - useOffscreenPause(el): a ref that is true while el is off-screen. Bind it as the `offscreen` class on anything that
//   slides continuously: the in-progress icon does this itself, and a live `f-diag f-live` segment should too.
// - statusFill(state): the status icon as a bar fill, the `f-<pattern> fc-<status>` classes from styles/status-icons.css.
//   `f-diag` stripes slide with `f-live` (always) or inside an `f-slide-hover` container (while the pointer is over it);
//   `f-short` drops the stripes on a segment too short for them (under 10 px).
// Nothing moves with prefers-reduced-motion.

import { onBeforeUnmount, onMounted, ref, watch, type Ref } from 'vue'
import { statusMeta, type StatusKey } from './work'

export const ONE_OFF_EASE = 'cubic-bezier(.22, .8, .3, 1)'
export const ONE_OFF_ID = 'status-one-off'

export interface OneOffStep { part: string; keyframes: Keyframe[]; delay: number; duration: number }
const draw = (part: string, delay: number, duration: number): OneOffStep => ({ part, keyframes: [{ strokeDashoffset: 1 }, { strokeDashoffset: 0 }], delay, duration })
const turn = (part: string, from: number, duration: number): OneOffStep => ({ part, keyframes: [{ transform: `rotate(${from}deg)` }, { transform: 'rotate(0deg)' }], delay: 0, duration })

// One short animation per status (§7.2). Only the mark on top animates: the base shape (ring, disc, square) never
// disappears or redraws. They end exactly on the static icon, because they leave no fill behind.
// In progress has none: its stripes slide continuously (CSS).
export const ONE_OFFS: Readonly<Partial<Record<StatusKey, readonly OneOffStep[]>>> = {
  done: [draw('.tick', 0, 480)],
  delivered: [draw('.tick', 0, 480)],
  accepted: [draw('.tick', 0, 300), draw('.tick2', 200, 320)],
  cancelled: [draw('.slash', 0, 460)],
  qa: [{ part: '.pulse', keyframes: [{ opacity: 0, transform: 'scale(1)' }, { opacity: .6, transform: 'scale(1.18)', offset: .35 }, { opacity: 0, transform: 'scale(1.5)' }], delay: 0, duration: 560 }],
  new: [turn('.dot', -80, 520)],
  open: [{ part: '.glint', keyframes: [{ strokeDashoffset: 0, opacity: 0 }, { opacity: .85, offset: .2 }, { opacity: .85, offset: .75 }, { strokeDashoffset: -1, opacity: 0 }], delay: 0, duration: 520 }],
  backlog: [turn('.dots', -45, 520)],
  blocked: [turn('.dashes', -45, 420)],
  archived: [{ part: '.sq', keyframes: [{ transform: 'scale(.9)' }, { transform: 'scale(1.04)', offset: .6 }, { transform: 'scale(1)' }], delay: 0, duration: 420 }],
}
const ONE_OFF_KEYS = Object.keys(ONE_OFFS) as StatusKey[]

let reduceQuery: MediaQueryList | null | undefined
function reducedMotion(): boolean {
  if (reduceQuery === undefined) reduceQuery = typeof matchMedia === 'function' ? matchMedia('(prefers-reduced-motion: reduce)') : null
  return reduceQuery?.matches ?? false
}

// Plays one status icon's one-off and returns how many parts it animated: 0 with reduced motion and for In progress.
// A replay cancels the running one first.
export function playOneOff(icon: Element): number {
  const key = ONE_OFF_KEYS.find(status => icon.classList.contains(`st-${status}`))
  const steps = key && ONE_OFFS[key]
  if (!steps || reducedMotion()) return 0
  for (const animation of icon.getAnimations({ subtree: true })) if (animation.id === ONE_OFF_ID) animation.cancel()
  let played = 0
  for (const step of steps) {
    const part = icon.querySelector(step.part)
    if (!part) continue
    part.animate(step.keyframes, { id: ONE_OFF_ID, delay: step.delay, duration: step.duration, easing: ONE_OFF_EASE, fill: 'backwards' })
    played++
  }
  return played
}

// A status icon's control: the nearest interactive element around it (a table status, header count, sheet chip, menu
// item, filter option, row link), or anything marked data-status-motion.
export const MOTION_HOSTS = 'button, a[href], label, summary, [role="button"], [role="link"], [role^="menuitem"], [role="option"], [role="tab"], [data-status-motion]'
// A control shows one icon, sometimes two (from → to); more means the element is a container, not an icon's control.
const MAX_ICONS = 4

function iconsOf(host: Element): Element[] {
  const icons: Element[] = []
  for (const icon of host.querySelectorAll('svg.status-icon')) {
    if (icon.closest(MOTION_HOSTS) !== host) continue
    if (icon.checkVisibility && !icon.checkVisibility({ visibilityProperty: true })) continue
    if (icons.push(icon) > MAX_ICONS) return []
  }
  return icons
}

// Once per entry: moving inside the control does not replay.
function pointerOver(event: PointerEvent) {
  const host = (event.target as Element | null)?.closest?.(MOTION_HOSTS)
  if (!host || (event.relatedTarget instanceof Node && host.contains(event.relatedTarget))) return
  for (const icon of iconsOf(host)) playOneOff(icon)
}
function focusIn(event: FocusEvent) {
  const target = event.target as Element | null
  if (!target?.matches?.(':focus-visible')) return
  const host = target.closest(MOTION_HOSTS)
  if (host) for (const icon of iconsOf(host)) playOneOff(icon)
}
function markHiddenTab() { document.documentElement.classList.toggle('tab-hidden', document.hidden) }

export function useStatusMotion(): void {
  onMounted(() => {
    markHiddenTab()
    document.addEventListener('pointerover', pointerOver, { passive: true })
    document.addEventListener('focusin', focusIn)
    document.addEventListener('visibilitychange', markHiddenTab)
  })
  onBeforeUnmount(() => {
    document.removeEventListener('pointerover', pointerOver)
    document.removeEventListener('focusin', focusIn)
    document.removeEventListener('visibilitychange', markHiddenTab)
  })
}

// One IntersectionObserver for everything that slides.
let observer: IntersectionObserver | null = null
const reporters = new WeakMap<Element, (offscreen: boolean) => void>()
function sharedObserver(): IntersectionObserver | null {
  if (!observer && typeof IntersectionObserver === 'function') {
    observer = new IntersectionObserver(entries => { for (const entry of entries) reporters.get(entry.target)?.(!entry.isIntersecting) })
  }
  return observer
}

export function useOffscreenPause(target: Readonly<Ref<Element | null | undefined>>): Ref<boolean> {
  const offscreen = ref(false)
  let observed: Element | null = null
  const release = () => {
    if (!observed) return
    observer?.unobserve(observed); reporters.delete(observed); observed = null
  }
  watch(target, element => {
    release(); offscreen.value = false
    const io = element ? sharedObserver() : null
    if (!element || !io) return
    observed = element
    reporters.set(element, value => { offscreen.value = value })
    io.observe(element)
  }, { flush: 'post' })
  onBeforeUnmount(release)
  return offscreen
}

// The bar form of each status icon (§5.1): ring → outline, in progress → 45° stripes, filled → solid, dotted and dashed
// rings → dotted and dashed outlines, ⊘ → grey tint with one slash, □ → square outline.
const STATUS_FILL: Readonly<Record<StatusKey, string>> = {
  new: 'f-ring', open: 'f-ring', backlog: 'f-dotted', blocked: 'f-dashed', progress: 'f-diag', qa: 'f-halo',
  done: 'f-tint', delivered: 'f-solid', accepted: 'f-deep', cancelled: 'f-cancel', archived: 'f-square', other: 'f-ring',
}
export function statusFill(state: string): string {
  const { key } = statusMeta(state)
  return `${STATUS_FILL[key]} fc-${key}`
}
