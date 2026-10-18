<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { nextTick, onBeforeUnmount, onMounted, ref } from 'vue'
// One tooltip for the whole app: any element with data-tip gets it on hover
// (after a short delay), on keyboard focus, and on touch long press. A tap
// always performs the source's action. Screen readers use the element's
// own label; overflowing text becomes an accessible, focusable scroll region.
const text = ref('')
const x = ref(0)
const y = ref(0)
const below = ref(false)
const scrollable = ref(false)
const tip = ref<HTMLElement>()
// Retain the anchor's panel/dialog DOM ownership for outside-pointer checks.
// A manual popover puts it in the top layer, escaping the owner's overflow and
// transformed containing block while keeping viewport-relative coordinates.
const layer = ref<HTMLElement | null>(null)
let target: HTMLElement | null = null
type Trigger = 'pointer' | 'focus' | 'touch'
let trigger: Trigger | null = null
let focusOwner: HTMLElement | null = null
let restoringFocus = false
let timer: ReturnType<typeof setTimeout> | undefined
let focusObserver: MutationObserver | undefined
let sourceObserver: MutationObserver | undefined
let touchInput = false
let pressTimer: ReturnType<typeof setTimeout> | undefined
let press: { element: HTMLElement; value: string; pointerId: number; x: number; y: number } | undefined
let heldAction: HTMLElement | undefined

function cancelPress() { clearTimeout(pressTimer); press = undefined }
function coarse(event: PointerEvent) {
  return event.pointerType === 'touch' || (event.pointerType === 'pen' && matchMedia('(pointer: coarse)').matches)
}

function find(node: EventTarget | null): HTMLElement | null {
  if (!(node instanceof Element)) return null
  const active = node.getAttribute('aria-activedescendant')
  const activeName = active ? document.getElementById(active)?.querySelector<HTMLElement>('[data-clip-tip]') : null
  if (activeName) return activeName
  return node.closest<HTMLElement>('[data-tip]')
    ?? node.closest('button, a[href], label, [role="option"], [role="menuitemradio"]')?.querySelector<HTMLElement>('[data-clip-tip]')
    ?? null
}
async function show(element: HTMLElement, cause: Trigger) {
  const value = element.dataset.tip
  if (!value || !element.isConnected) { hide(); return }
  clearTimeout(timer)
  if (target !== element) {
    sourceObserver?.disconnect()
    sourceObserver = new MutationObserver(() => {
      if (target !== element) return
      if (!element.isConnected || !element.dataset.tip) hide()
      else if (element.dataset.tip !== text.value && trigger) void show(element, trigger)
    })
    // Observe removals too: a record can leave the DOM without pointerout or
    // focusout. Attribute filtering keeps unrelated name updates inexpensive.
    sourceObserver.observe(document.body, { childList: true, subtree: true, attributes: true, attributeFilter: ['data-tip'] })
  }
  target = element
  trigger = cause
  if (!tip.value?.contains(document.activeElement)) {
    focusOwner = cause === 'focus' && document.activeElement instanceof HTMLElement
      ? document.activeElement
      : element.closest<HTMLElement>('button, a[href], input, [tabindex]')
  }
  layer.value = element.closest<HTMLElement>('.floating, [popover]:popover-open, dialog[open]')
  text.value = value
  await nextTick()
  if (target !== element || !element.isConnected) { if (target === element) hide(); return }
  if (!tip.value?.matches(':popover-open')) tip.value?.showPopover()
  const rect = element.getBoundingClientRect()
  const width = tip.value?.offsetWidth ?? 0
  const height = tip.value?.offsetHeight ?? 0
  scrollable.value = (tip.value?.scrollHeight ?? 0) > (tip.value?.clientHeight ?? 0)
  const clampY = (preferred: number) => Math.round(Math.max(8, Math.min(preferred, innerHeight - height - 8)))
  // Menu entries say why beside the menu, so the entries around stay readable.
  if (element.dataset.tipSide === 'end') {
    const menu = element.closest<HTMLElement>('.floating')?.getBoundingClientRect() ?? rect
    below.value = false
    y.value = clampY(rect.top + rect.height / 2 - height / 2)
    if (innerWidth - menu.right - 16 >= width) x.value = Math.round(menu.right + 8)
    else if (menu.left - width - 16 >= 0) x.value = Math.round(menu.left - width - 8)
    // No room on either side (a phone): under the entry, over the ones below it.
    else { x.value = Math.round(Math.min(Math.max(8, rect.left), innerWidth - width - 8)); y.value = clampY(rect.bottom + 6) }
    return
  }
  below.value = rect.top - height - 8 < 8
  x.value = Math.round(Math.min(Math.max(8, rect.left + rect.width / 2 - width / 2), innerWidth - width - 8))
  y.value = clampY(below.value ? rect.bottom + 8 : rect.top - height - 8)
}
function hide() { clearTimeout(timer); cancelPress(); sourceObserver?.disconnect(); target = null; trigger = null; focusOwner = null; text.value = ''; scrollable.value = false }
function hasFocusedTip() {
  return target !== null && (tip.value?.contains(document.activeElement)
    || (trigger === 'focus' && focusOwner === document.activeElement && find(focusOwner) === target))
}
function insideTip(event: Event) { return event.target instanceof Node && tip.value?.contains(event.target) }
function over(event: PointerEvent) {
  if (coarse(event)) return
  // Keyboard focus owns its disclosure until blur or Escape; incidental
  // pointer movement must neither dismiss it nor replace it with another tip.
  if (hasFocusedTip() || trigger === 'touch' || insideTip(event)) return
  const element = find(event.target)
  if (element === target) return
  hide()
  if (element) timer = setTimeout(() => void show(element, 'pointer'), 380)
}
function revealFocus(focused: Element) {
  // Touch can retain :focus-visible from the last keyboard action and pickers
  // may autofocus their search. Neither is a request to disclose after a tap.
  if (touchInput || document.activeElement !== focused || !focused.matches(':focus-visible')) return
  const element = find(focused)
  if (element?.dataset.tip) void show(element, 'focus')
  else hide()
}
function focusIn(event: FocusEvent) {
  if (insideTip(event)) return
  focusObserver?.disconnect()
  const focused = event.target
  if (!(focused instanceof Element)) return
  let name = find(focused)
  let value = name?.dataset.tip
  // Async results may mount at the same active index; resize/text observers
  // may make that row clipped later. Remember the source identity and value
  // so tooltip DOM mutations cannot reopen a deliberately dismissed tip.
  focusObserver = new MutationObserver(() => {
    const current = find(focused)
    const currentValue = current?.dataset.tip
    if (current === name && currentValue === value) return
    name = current; value = currentValue
    revealFocus(focused)
  })
  focusObserver.observe(document.body, { childList: true, subtree: true, attributes: true,
    attributeFilter: ['aria-activedescendant', 'data-tip', 'data-clip-tip'] })
  if (!restoringFocus) revealFocus(focused)
}
function keydown(event: KeyboardEvent) {
  touchInput = false
  cancelPress()
  if (event.key === 'Escape' && trigger === 'touch') {
    event.preventDefault(); event.stopImmediatePropagation(); hide(); return
  }
  const inRegion = scrollable.value && tip.value?.contains(document.activeElement)
  // Capture before the owning panel's Tab/escape handlers. Reading a name must
  // not navigate its options or close it; native region scrolling stays intact.
  if (inRegion && event.key === 'Escape') {
    event.preventDefault(); event.stopImmediatePropagation()
    const owner = focusOwner
    hide()
    restoringFocus = true
    try { if (owner?.isConnected) owner.focus({ preventScroll: true }) }
    finally { restoringFocus = false }
    return
  }
  if (event.key === 'Tab' && !event.ctrlKey && !event.metaKey && !event.altKey
    && scrollable.value && focusOwner === document.activeElement && !event.shiftKey) {
    event.preventDefault(); event.stopImmediatePropagation()
    tip.value?.focus({ preventScroll: true })
    return
  }
  if (inRegion) {
    if (event.key === 'Tab' && event.shiftKey) {
      event.preventDefault(); event.stopImmediatePropagation()
      focusOwner?.focus({ preventScroll: true })
    }
    return
  }
  if (event.key === 'Escape' || (trigger !== 'touch' && !hasFocusedTip())) hide()
  // An arrow at a list boundary need not change aria-activedescendant.
  // Refresh after the owning component has processed that navigation.
  if (['ArrowDown', 'ArrowUp', 'ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) {
    const focused = document.activeElement
    if (focused?.hasAttribute('aria-activedescendant')) void nextTick(() => revealFocus(focused))
  }
}
function focusOut(event: FocusEvent) {
  if (event.relatedTarget instanceof Node && (tip.value?.contains(event.relatedTarget)
    || (insideTip(event) && event.relatedTarget === focusOwner))) return
  focusObserver?.disconnect()
  // A held name stays readable after pointer release changes focus.
  if (trigger !== 'touch' && !press) hide()
}
function pointerDown(event: PointerEvent) {
  touchInput = coarse(event)
  heldAction = undefined
  const previous = press
  cancelPress()
  if (insideTip(event)) return
  if (!touchInput) { if (!hasFocusedTip()) hide(); return }
  const element = find(event.target)
  if (element !== target || trigger !== 'touch') hide()
  // Multiple contacts are a scrolling/zoom gesture, never a disclosure.
  if (previous || event.isPrimary === false || event.button !== 0
    || !element?.hasAttribute('data-clip-tip') || !element.dataset.tip) return
  // A clipped-name hold belongs to disclosure, not an ancestor's selection
  // timer. Leave native scrolling, focus and ordinary taps intact; only the
  // completed hold's compatibility click is consumed below.
  event.stopPropagation()
  const started = { element, value: element.dataset.tip, pointerId: event.pointerId, x: event.clientX, y: event.clientY }
  press = started
  pressTimer = setTimeout(() => {
    if (press !== started || !element.isConnected || element.dataset.tip !== started.value
      || !element.hasAttribute('data-clip-tip')) { cancelPress(); return }
    heldAction = element.closest<HTMLElement>('button, a[href], label, [role="option"], [role="menuitemradio"]') ?? element
    void show(element, 'touch')
  }, 500)
}
function pointerMove(event: PointerEvent) {
  if (press?.pointerId === event.pointerId
    && Math.hypot(event.clientX - press.x, event.clientY - press.y) > 8) cancelPress()
}
function pointerEnd(event: PointerEvent) { if (press?.pointerId === event.pointerId) cancelPress() }
function heldClick(event: MouseEvent) {
  // Only consume the compatibility click belonging to a completed hold.
  // Keyboard activation and the next fresh tap retain their normal action.
  if (event.detail === 0 || !(event.target instanceof Node) || !heldAction?.contains(event.target)) return
  event.preventDefault(); event.stopImmediatePropagation(); heldAction = undefined
}
function contextMenu(event: MouseEvent) {
  if (event.target instanceof Node && (heldAction?.contains(event.target) || press?.element.contains(event.target))) {
    event.preventDefault(); event.stopImmediatePropagation()
  }
}
function scroll(event: Event) {
  // Scrolling within a long disclosure must leave it readable. Page and
  // nested-container scrolling follow the keyboard source's new position.
  if (insideTip(event)) return
  cancelPress()
  if ((hasFocusedTip() || trigger === 'touch') && target) void show(target, trigger ?? 'focus')
  else hide()
}
onMounted(() => {
  document.addEventListener('pointerover', over)
  document.addEventListener('focusin', focusIn)
  document.addEventListener('focusout', focusOut)
  document.addEventListener('pointerdown', pointerDown, true)
  document.addEventListener('pointermove', pointerMove, true)
  document.addEventListener('pointerup', pointerEnd, true)
  document.addEventListener('pointercancel', pointerEnd, true)
  document.addEventListener('click', heldClick, true)
  document.addEventListener('contextmenu', contextMenu, true)
  window.addEventListener('keydown', keydown, true)
  document.addEventListener('scroll', scroll, true)
  window.addEventListener('resize', scroll)
})
onBeforeUnmount(() => {
  document.removeEventListener('pointerover', over)
  document.removeEventListener('focusin', focusIn)
  document.removeEventListener('focusout', focusOut)
  document.removeEventListener('pointerdown', pointerDown, true)
  document.removeEventListener('pointermove', pointerMove, true)
  document.removeEventListener('pointerup', pointerEnd, true)
  document.removeEventListener('pointercancel', pointerEnd, true)
  document.removeEventListener('click', heldClick, true)
  document.removeEventListener('contextmenu', contextMenu, true)
  window.removeEventListener('keydown', keydown, true)
  document.removeEventListener('scroll', scroll, true)
  window.removeEventListener('resize', scroll)
  hide()
  focusObserver?.disconnect()
})
</script>

<template>
  <Teleport :to="layer ?? 'body'">
    <div v-if="text" ref="tip" popover="manual" class="tooltip" :class="{ below, scrollable }" :style="{ transform: `translate(${x}px, ${y}px)` }"
      :role="scrollable ? 'region' : undefined" :aria-label="scrollable ? 'Full text' : undefined" :aria-hidden="scrollable ? undefined : true" :tabindex="scrollable ? 0 : undefined"
      @keydown="event => { if (event.key !== 'Tab') event.stopPropagation() }">{{ text }}</div>
  </Teleport>
</template>

<style scoped>
.tooltip {
  position: fixed; z-index: 80; inset: auto; top: 0; left: 0; margin: 0; border: 0; width: max-content; max-width: min(320px, calc(100vw - 16px)); max-height: calc(100dvh - 16px); padding: 5px 10px; border-radius: 8px; pointer-events: none; overflow-wrap: anywhere;
  overflow-y: auto; overscroll-behavior: contain;
  background: var(--tip-bg); color: var(--tip-ink); font-size: 12.5px; line-height: 1.4; white-space: pre-line;
  box-shadow: 0 0 0 1px var(--glass-rim), 0 10px 24px -10px color-mix(in srgb, var(--shadow-black) 50%, transparent);
}
/* Only overflowing text takes pointer input; ordinary tips leave controls
   underneath reachable. */
.tooltip.scrollable { pointer-events: auto; }
.tooltip:focus-visible { outline: 2px solid var(--ink-2); outline-offset: -2px; }
</style>
