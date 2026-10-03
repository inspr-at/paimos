<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { nextTick, onBeforeUnmount, onMounted, ref } from 'vue'
// One tooltip for the whole app: any element with data-tip gets it on hover
// (after a short delay) and on keyboard focus. Screen readers use the element's
// own label; the tooltip is decoration for sighted pointer and keyboard users.
const text = ref('')
const x = ref(0)
const y = ref(0)
const below = ref(false)
const scrollable = ref(false)
const tip = ref<HTMLElement>()
// Inside a modal dialog the tooltip moves into it, above the page (the top layer);
// otherwise it sits at the end of <body>, above popovers and menus (which leave
// the app's own stacking layer too).
const layer = ref<HTMLElement | null>(null)
let target: HTMLElement | null = null
let timer: ReturnType<typeof setTimeout> | undefined
let tap: { element: HTMLElement; x: number; y: number } | null = null
let tapped: HTMLElement | null = null

function find(node: EventTarget | null): HTMLElement | null {
  return node instanceof Element ? node.closest<HTMLElement>('[data-tip]') : null
}
async function show(element: HTMLElement) {
  const value = element.dataset.tip
  if (!value) return
  target = element
  layer.value = element.closest<HTMLElement>('dialog[open]')
  text.value = value
  await nextTick()
  const rect = element.getBoundingClientRect()
  const width = tip.value?.offsetWidth ?? 0
  const height = tip.value?.offsetHeight ?? 0
  scrollable.value = (tip.value?.scrollHeight ?? 0) > (tip.value?.clientHeight ?? 0) + 1
  // Menu entries say why beside the menu, so the entries around stay readable.
  if (element.dataset.tipSide === 'end') {
    const menu = element.closest<HTMLElement>('.floating')?.getBoundingClientRect() ?? rect
    below.value = false
    y.value = Math.round(Math.min(Math.max(8, rect.top + rect.height / 2 - height / 2), innerHeight - height - 8))
    if (innerWidth - menu.right - 16 >= width) x.value = Math.round(menu.right + 8)
    else if (menu.left - width - 16 >= 0) x.value = Math.round(menu.left - width - 8)
    // No room on either side (a phone): under the entry, over the ones below it.
    else { x.value = Math.round(Math.min(Math.max(8, rect.left), innerWidth - width - 8)); y.value = Math.round(Math.min(rect.bottom + 6, innerHeight - height - 8)) }
    return
  }
  below.value = rect.top - height - 8 < 8
  x.value = Math.round(Math.min(Math.max(8, rect.left + rect.width / 2 - width / 2), innerWidth - width - 8))
  y.value = Math.round(Math.min(Math.max(8, below.value ? rect.bottom + 8 : rect.top - height - 8), innerHeight - height - 8))
}
function hide() { clearTimeout(timer); target = null; text.value = '' }
function insideTip(node: EventTarget | null) { return node instanceof Node && !!tip.value?.contains(node) }
function over(event: PointerEvent) {
  if (event.pointerType === 'touch' || insideTip(event.target)) return
  const element = find(event.target)
  if (element === target) return
  hide()
  if (element) timer = setTimeout(() => void show(element), 380)
}
function focusIn(event: FocusEvent) {
  const element = find(event.target)
  if (element && element.matches(':focus-visible')) void show(element)
}
// Only passive full-text identities opt into tap reveal. Action tooltips keep
// their existing behaviour, so a tap still performs the action immediately.
function pointerDown(event: PointerEvent) {
  tapped = null
  if (insideTip(event.target)) { tap = null; return }
  const element = find(event.target)
  tap = event.pointerType === 'touch' && element?.hasAttribute('data-tip-touch')
    ? { element, x: event.clientX, y: event.clientY } : null
  if (!tap || target !== tap.element) hide()
}
function pointerUp(event: PointerEvent) {
  const start = tap
  tap = null
  if (!start || find(event.target) !== start.element) return
  if (Math.hypot(event.clientX - start.x, event.clientY - start.y) > 10) { hide(); return }
  tapped = start.element
}
// Touch's compatibility mouse events move focus after pointerup. Reveal on the
// resulting click, after focusout, so that focus change cannot erase the tip.
function click(event: MouseEvent) {
  const element = tapped
  tapped = null
  if (!element || find(event.target) !== element) return
  if (target === element) hide()
  else void show(element)
}
function pointerCancel() { tap = null; tapped = null; hide() }
function scroll(event: Event) { if (!insideTip(event.target)) hide() }
onMounted(() => {
  document.addEventListener('pointerover', over)
  document.addEventListener('focusin', focusIn)
  document.addEventListener('focusout', hide)
  document.addEventListener('pointerdown', pointerDown)
  document.addEventListener('pointerup', pointerUp)
  document.addEventListener('pointercancel', pointerCancel)
  document.addEventListener('click', click)
  document.addEventListener('keydown', hide)
  document.addEventListener('scroll', scroll, true)
})
onBeforeUnmount(() => {
  document.removeEventListener('pointerover', over)
  document.removeEventListener('focusin', focusIn)
  document.removeEventListener('focusout', hide)
  document.removeEventListener('pointerdown', pointerDown)
  document.removeEventListener('pointerup', pointerUp)
  document.removeEventListener('pointercancel', pointerCancel)
  document.removeEventListener('click', click)
  document.removeEventListener('keydown', hide)
  document.removeEventListener('scroll', scroll, true)
  clearTimeout(timer)
})
</script>

<template>
  <Teleport :to="layer ?? 'body'">
    <div v-if="text" ref="tip" class="tooltip" :class="{ below, scrollable }" :style="{ transform: `translate(${x}px, ${y}px)` }" aria-hidden="true">{{ text }}</div>
  </Teleport>
</template>

<style scoped>
.tooltip {
  position: fixed; z-index: 80; top: 0; left: 0; box-sizing: border-box; max-width: min(320px, calc(100vw - 16px)); max-height: calc(100dvh - 16px); overflow: auto; overscroll-behavior: contain; padding: 5px 10px; border-radius: 8px; pointer-events: none;
  background: var(--tip-bg); color: var(--tip-ink); font-size: 12.5px; line-height: 1.4; white-space: pre-line; overflow-wrap: anywhere;
  box-shadow: 0 0 0 1px var(--glass-rim), 0 10px 24px -10px rgba(0, 0, 0, .5);
}
.tooltip.scrollable { pointer-events: auto; }
</style>
