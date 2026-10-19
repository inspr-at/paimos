<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { isSettingsField } from '../../lib/settingsOverlays'
import { getCurrentInstance, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
// A popover anchored to a trigger. It is teleported to <body> so table cells
// and sticky toolbars never clip it; it flips above the trigger near the
// bottom edge, closes on Escape, outside clicks and scroll that moves its trigger, and hands
// focus back to the trigger when it closes by keyboard. A menu closes on Tab;
// a small form (`cycle`) keeps Tab among its own controls instead.
// `phoneSheet`: on a portrait phone the menu opens as a sheet from the bottom edge, over a scrim,
// its items in thumb reach (AEON-1286); elsewhere it stays anchored to its trigger. A swipe down
// on the sheet closes it, as Esc does (AEON-1459), unless its content was scrolled.
// `sheetBelow` is the widest viewport that still opens the phone sheet (Delivery's worst items use 760, AEON-1493).
// `belowHeader`: flipped above its trigger, the panel stops 8 px below the app header instead of covering it. When it
// fits on neither side, the page first scrolls the trigger to 16 px below the header (a jump with reduced motion), and
// the panel then opens below it (Delivery's worst items, AEON-1493).
// `fieldsOwnEscape`: a text field inside handles its own Escape (a stepper cancels its draft); the next Escape closes.
const props = withDefaults(defineProps<{ anchor: HTMLElement | null; align?: 'start' | 'end'; width?: number; label: string; tallest?: number; cycle?: boolean; sheet?: boolean; compactSheet?: boolean; fieldEscape?: boolean; fieldsOwnEscape?: boolean; settingsKeys?: boolean; phoneSheet?: boolean; sheetBelow?: number; belowHeader?: boolean }>(), { align: 'start', width: 240, tallest: 420, cycle: false, sheet: false, fieldEscape: false, fieldsOwnEscape: false, phoneSheet: false, sheetBelow: 720, belowHeader: false })
defineOptions({ inheritAttrs: false })
const floatingID = `floating-${getCurrentInstance()!.uid}`
const floatingOwner = (props.anchor instanceof Element ? props.anchor.closest<HTMLElement>('[data-floating-id]') : null)?.dataset.floatingId
const emit = defineEmits<{ close: [restoreFocus: boolean] }>()
const panel = ref<HTMLElement>()
const x = ref(-9999)
const y = ref(-9999)
// A phone sheet is decided before the first paint, so it never changes form, width or edge while it opens.
const bottomSheet = () => props.phoneSheet && innerWidth <= props.sheetBelow && innerHeight > 480
const bottom = ref(bottomSheet())
const sheetMax = () => Math.max(160, innerHeight - 48)
// Until placed (off-screen), the content may take its full height so place() measures it.
const maxHeight = ref(bottom.value ? sheetMax() : props.tallest)
const placedHeight = ref<number>()
const compactWidth = ref(props.width)
const above = ref(false)
const scrim = ref<HTMLElement>()
// Hidden, without its opening animation, while the page scrolls to make room; it opens once placed.
const parked = ref(false)
let placedAnchor: DOMRect | null = null
let unmounted = false
// The highest top edge of a panel above its trigger: the screen's top, or with `belowHeader` the app header's bottom.
const ceiling = () => props.belowHeader ? Math.max(0, document.querySelector('header.app-header')?.getBoundingClientRect().bottom ?? 0) + 8 : 6

// Below the trigger, content grows downward. Above it, reserve the opening
// height so a search field cannot move when its result count changes.
function place(keepSide = false) {
  if (!props.anchor || !panel.value) return
  const rect = props.anchor.getBoundingClientRect()
  placedAnchor = rect
  bottom.value = bottomSheet()
  if (bottom.value) {
    // The bottom edge holds it (CSS): a taller menu grows upward and scrolls below 48 px of the screen's top.
    maxHeight.value = sheetMax()
    placedHeight.value = undefined
    above.value = false
    return
  }
  // A long picker uses a top-anchored frame with its own scrolling body and
  // pinned actions. Its location never depends on the selected role's preview.
  if (props.sheet) {
    const phone = innerWidth <= 600
    const width = phone ? innerWidth : props.compactSheet ? Math.min(props.width, innerWidth - 32) : Math.min(960, Math.max(480, innerWidth * .64), innerWidth - 32)
    compactWidth.value = width
    x.value = Math.round((innerWidth - width) / 2)
    y.value = phone ? 0 : Math.min(64, Math.round(innerHeight * .08))
    maxHeight.value = phone ? innerHeight : Math.min(props.tallest, innerHeight - y.value - 16)
    placedHeight.value = phone ? innerHeight : undefined
    above.value = false
    return
  }
  const height = panel.value.scrollHeight
  const room = innerHeight - rect.bottom - 12
  const top = ceiling(), roomAbove = rect.top - 6 - top
  // Open above when the menu would not fit below and there is more room above.
  if (!keepSide) above.value = room < Math.min(height, props.tallest) && (props.belowHeader ? roomAbove : rect.top) > room
  maxHeight.value = Math.max(160, Math.min(props.tallest, above.value ? roomAbove : room))
  if (above.value) {
    if (!keepSide) {
      const searchRoom = panel.value.querySelector('input:not([type=checkbox]):not([type=radio])') ? Math.min(320, props.tallest) : 0
      placedHeight.value = Math.min(Math.max(height, searchRoom), maxHeight.value)
    }
  } else placedHeight.value = undefined
  const width = Math.min(props.width, innerWidth - 16)
  const left = props.align === 'end' ? rect.right - width : rect.left
  x.value = Math.round(Math.min(Math.max(8, left), innerWidth - width - 8))
  const lifted = rect.top - Math.min(placedHeight.value ?? height, maxHeight.value) - 6
  y.value = Math.round(above.value ? props.belowHeader ? Math.max(top, lifted) : lifted : rect.bottom + 6)
}
// `belowHeader`: with no room on either side of the trigger, scroll its page so the trigger sits 16 px below the
// header, then let place() open the panel below it. The panel stays parked until the page has come to rest.
async function makeRoom() {
  if (!props.belowHeader || bottom.value || props.sheet || !(props.anchor instanceof HTMLElement) || !panel.value) return
  const rect = props.anchor.getBoundingClientRect(), need = Math.min(panel.value.scrollHeight, props.tallest), top = ceiling()
  if (innerHeight - rect.bottom - 12 >= need || rect.top - 6 - top >= need) return
  const scroller = scrollerOf(props.anchor)
  const target = Math.round(Math.max(0, Math.min(scroller.scrollHeight - scroller.clientHeight, scroller.scrollTop + rect.top - (top + 8))))
  if (Math.abs(target - scroller.scrollTop) < 1) return
  parked.value = true
  scroller.scrollTo({ top: target, behavior: matchMedia('(prefers-reduced-motion: reduce)').matches ? 'instant' : 'smooth' })
  await rest(scroller, target)
  parked.value = false
}
function scrollerOf(element: HTMLElement): HTMLElement {
  for (let parent = element.parentElement; parent; parent = parent.parentElement) {
    const overflow = getComputedStyle(parent).overflowY
    if ((overflow === 'auto' || overflow === 'scroll') && parent.scrollHeight > parent.clientHeight) return parent
  }
  return (document.scrollingElement ?? document.documentElement) as HTMLElement
}
// At rest: the target is reached, or the page has stopped for six frames after it moved (a person scrolled too).
// A smooth scroll that has not started yet is waited for; 90 frames bound it all.
let restFrame = 0
function rest(scroller: HTMLElement, target: number) {
  return new Promise<void>(resolve => {
    const start = scroller.scrollTop
    let last = start, still = 0, frames = 0
    const tick = () => {
      const now = scroller.scrollTop
      still = Math.abs(now - last) < .5 ? still + 1 : 0
      last = now
      frames++
      if (unmounted || Math.abs(now - target) < 1 || (still >= 6 && (Math.abs(now - start) >= .5 || frames >= 30)) || frames >= 90) { restFrame = 0; resolve(); return }
      restFrame = requestAnimationFrame(tick)
    }
    restFrame = requestAnimationFrame(tick)
  })
}
// Teleported child pickers belong to the trigger's overlay. Keep ancestors
// open while interacting in a child, and let the top overlay consume Escape.
function childOverlay(target: Node | null) {
  let overlay = (target instanceof Element ? target : target?.parentElement)?.closest<HTMLElement>('[data-floating-id]')
  for (let depth = 0; overlay?.dataset.floatingOwner && depth < 16; depth++) {
    if (overlay.dataset.floatingOwner === floatingID) return true
    overlay = document.querySelector<HTMLElement>(`[data-floating-id="${CSS.escape(overlay.dataset.floatingOwner)}"]`) ?? undefined
  }
  return false
}
function outside(event: PointerEvent) {
  const target = event.target as Node
  // The scrim closes on its click, which it also consumes, so the tap never reaches the page below.
  if (panel.value?.contains(target) || props.anchor?.contains(target) || childOverlay(target) || target === scrim.value) return
  emit('close', false)
}
// A full-page capture scrolls the page away and back inside one turn. Closing
// on that intermediate event drops the explainer without restoring focus, so
// Escape never reaches it. Exact equality also treats a subpixel restore as
// movement. Dismiss one frame later, and only when the trigger is still more
// than 0.5px away, the same tolerance control stability uses. A scroll that
// leaves the trigger displaced still closes without focusing it. Point-anchored
// context menus have no element to compare, so they still close immediately.
function anchorShifted() {
  if (!(props.anchor instanceof HTMLElement) || !placedAnchor) return true
  const rect = props.anchor.getBoundingClientRect()
  return Math.abs(rect.top - placedAnchor.top) > .5 || Math.abs(rect.left - placedAnchor.left) > .5 || Math.abs(rect.bottom - placedAnchor.bottom) > .5 || Math.abs(rect.right - placedAnchor.right) > .5
}
let settleFrame = 0
function scrolled(event: Event) {
  // A bottom sheet is not anchored to its trigger: the page scrolling behind it never closes it.
  if (bottom.value || panel.value?.contains(event.target as Node)) return
  if (!anchorShifted()) return
  if (!(props.anchor instanceof HTMLElement)) { emit('close', false); return }
  if (settleFrame) return
  settleFrame = requestAnimationFrame(() => {
    settleFrame = 0
    if (panel.value && anchorShifted()) emit('close', false)
  })
}
// Inside a modal dialog (the release history's ticket panel) the popover moves
// into it: everything outside the top layer is inert while the modal is open.
// (Context menus anchor to a point, not an element: those stay in <body>.)
const layer = (props.anchor instanceof Element ? props.anchor.closest<HTMLElement>('dialog[open]') : null) ?? 'body'
// Escape closes the popover wherever focus is, before any page shortcut sees it;
// another dialog open above the popover's own layer handles its Escape itself.
function escape(event: KeyboardEvent) {
  if (childOverlay(document.activeElement)) return
  if (event.key !== 'Escape' || [...document.querySelectorAll('dialog[open]')].some(dialog => dialog !== layer)) return
  // Names loaded after this panel mounts register their Escape listener later.
  // Let the open disclosure consume Escape before dismissing its menu.
  if (panel.value?.querySelector('.read-name-detail:popover-open')) return
  // A form: Escape first leaves a text field or select, the next press closes (AEON-541 keyboard rule).
  const field = event.target
  if (props.fieldsOwnEscape && field instanceof HTMLElement && panel.value?.contains(field) && ['INPUT', 'TEXTAREA', 'SELECT'].includes(field.tagName)) return
  if (props.fieldEscape && field instanceof HTMLElement && panel.value?.contains(field) && (field.tagName === 'SELECT' || (field.tagName === 'INPUT' && !['checkbox', 'radio', 'button'].includes((field as HTMLInputElement).type)))) {
    event.preventDefault(); event.stopImmediatePropagation()
    field.blur(); panel.value.tabIndex = -1; panel.value.focus({ preventScroll: true })
    return
  }
  event.preventDefault(); event.stopImmediatePropagation()
  if (props.settingsKeys && isSettingsField(event.target)) {
    (event.target as HTMLElement).blur(); panel.value?.focus({ preventScroll: true }); return
  }
  emit('close', true)
}
let scrollFrame = 0
let resized: ResizeObserver | null = null
function keydown(event: KeyboardEvent) {
  if (event.key !== 'Tab') return
  if (!props.cycle) { emit('close', false); return }
  const stops = [...(panel.value?.querySelectorAll<HTMLElement>('input, button, textarea, select, a[href], [tabindex]') ?? [])]
    .filter(el => el.tabIndex >= 0 && !(el as HTMLButtonElement).disabled && el.getClientRects().length)
  if (!stops.length) return
  const edge = event.shiftKey ? stops[0] : stops[stops.length - 1]
  if (document.activeElement === edge || !stops.includes(document.activeElement as HTMLElement)) {
    event.preventDefault()
    ;(event.shiftKey ? stops[stops.length - 1] : stops[0]).focus()
  }
}
const onResize = () => place()
// A swipe down on a bottom sheet closes it: mostly downward, 64 px or more, from content at its top.
let swipe: { id: number; x: number; y: number } | null = null
function swipeStart(event: TouchEvent) {
  const touch = event.touches[0]
  swipe = bottom.value && event.touches.length === 1 && touch && (panel.value?.scrollTop ?? 0) <= 0 ? { id: touch.identifier, x: touch.clientX, y: touch.clientY } : null
}
function swipeEnd(event: TouchEvent) {
  const start = swipe
  swipe = null
  const touch = start ? [...event.changedTouches].find(item => item.identifier === start.id) : undefined
  if (!start || !touch) return
  const down = touch.clientY - start.y, across = Math.abs(touch.clientX - start.x)
  if (down >= 64 && down > across * 1.5) emit('close', false)
}
// The collapsed Display menu widens after it counts columns. Place again once that
// width arrives: the resize that caused the recount already placed the narrower menu.
watch(() => props.width, () => place(true))
onMounted(async () => {
  await nextTick()
  if (props.belowHeader) await makeRoom()
  if (unmounted) return
  place()
  document.addEventListener('pointerdown', outside, true)
  // A scroll already under way when the popover opens (the page settling after the
  // click) is delivered before the next frame's callbacks: listen from then on.
  scrollFrame = requestAnimationFrame(() => { scrollFrame = 0; document.addEventListener('scroll', scrolled, true) })
  window.addEventListener('resize', onResize)
  resized = new ResizeObserver(() => place(true))
  for (const child of panel.value?.children ?? []) resized.observe(child)
  window.addEventListener('keydown', escape, true)
  const first = panel.value?.querySelector<HTMLElement>('[data-autofocus]') ?? panel.value?.querySelector<HTMLElement>('input, button, [tabindex="0"]')
  first?.focus({ preventScroll: true })
})
onBeforeUnmount(() => {
  unmounted = true
  cancelAnimationFrame(restFrame)
  cancelAnimationFrame(scrollFrame)
  cancelAnimationFrame(settleFrame)
  document.removeEventListener('pointerdown', outside, true)
  document.removeEventListener('scroll', scrolled, true)
  window.removeEventListener('resize', onResize)
  resized?.disconnect()
  window.removeEventListener('keydown', escape, true)
})
defineExpose({ place })
</script>

<template>
  <Teleport :to="layer">
    <div v-if="bottom" ref="scrim" class="floating-scrim" aria-hidden="true" @click="emit('close', false)" />
    <div ref="panel" v-bind="$attrs" :data-floating-id="floatingID" :data-floating-owner="floatingOwner" class="floating pop" :class="{ above, sheet, bottom, parked }" role="dialog" :aria-label="label" :style="{ transform: bottom ? 'none' : `translate(${x}px, ${y}px)`, width: sheet ? compactSheet ? `${compactWidth}px` : undefined : bottom ? '100%' : `${Math.min(width, 9999)}px`, height: placedHeight === undefined ? undefined : `${placedHeight}px`, boxSizing: 'border-box', maxHeight: `${maxHeight}px`, '--floating-max': `${maxHeight}px` }" @keydown="keydown" @touchstart.passive="swipeStart" @touchend.passive="swipeEnd" @touchcancel.passive="swipe = null">
      <slot />
    </div>
  </Teleport>
</template>

<style scoped>
.floating { position: fixed; z-index: 70; top: 0; left: 0; max-width: calc(100vw - 16px); overflow: auto; padding: 6px; overscroll-behavior: contain; }
.floating:focus { outline: none; }
.floating.sheet { width: min(960px, max(480px, 64vw)); max-width: calc(100vw - 32px); overflow: hidden; }
@media (max-width: 600px) { .floating.sheet { width: 100%; max-width: none; border-radius: 0; } }
.floating.bottom { top: auto; bottom: 0; left: 0; max-width: none; padding: 8px max(8px, env(safe-area-inset-right)) max(8px, env(safe-area-inset-bottom)) max(8px, env(safe-area-inset-left)); border-width: 1px 0 0; border-radius: 20px 20px 0 0; }
.floating-scrim { position: fixed; inset: 0; z-index: 69; background: var(--scrim); }
@media (prefers-reduced-motion: no-preference) {
  .floating { animation: pop-in .14s cubic-bezier(.2, .7, .2, 1); }
  @keyframes pop-in { from { opacity: 0; margin-top: -4px; } to { opacity: 1; margin-top: 0; } }
  .floating.above { animation-name: pop-in-above; }
  .floating.bottom { animation-name: sheet-up; }
  @keyframes sheet-up { from { opacity: 0; transform: translateY(16px); } to { opacity: 1; transform: none; } }
  @keyframes pop-in-above { from { opacity: 0; margin-top: 4px; } to { opacity: 1; margin-top: 0; } }
}
/* After the media rules, so the opening animation starts only once the panel is placed. */
.floating.parked { visibility: hidden; animation: none; }
</style>
