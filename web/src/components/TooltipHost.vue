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
const tip = ref<HTMLElement>()
// Inside a modal dialog the tooltip moves into it, above the page (the top layer);
// otherwise it sits at the end of <body>, above popovers and menus (which leave
// the app's own stacking layer too).
const layer = ref<HTMLElement | null>(null)
let target: HTMLElement | null = null
let timer: ReturnType<typeof setTimeout> | undefined
let focusObserver: MutationObserver | undefined
let touched: { element: HTMLElement; text: string } | null = null
let keyFrame = 0

function find(node: EventTarget | null): HTMLElement | null {
  if (!(node instanceof Element)) return null
  const active = node.getAttribute('aria-activedescendant')
  const activeName = active ? document.getElementById(active)?.querySelector<HTMLElement>('[data-clip-tip]') : null
  if (activeName) return activeName
  return node.closest<HTMLElement>('[data-tip]')
    ?? node.closest('button, a[href], label, [role="option"], [role="menuitemradio"]')?.querySelector<HTMLElement>('[data-clip-tip]')
    ?? null
}
async function show(element: HTMLElement) {
  const value = element.dataset.tip
  if (!value) return
  target = element
  layer.value = element.closest<HTMLElement>('dialog[open]')
  text.value = value
  await nextTick()
  if (target !== element || !element.isConnected) { if (target === element) hide(); return }
  const rect = element.getBoundingClientRect()
  if (rect.bottom < 0 || rect.top > innerHeight) { hide(); return }
  const width = tip.value?.offsetWidth ?? 0
  const height = tip.value?.offsetHeight ?? 0
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
  y.value = Math.round(below.value ? rect.bottom + 8 : rect.top - height - 8)
}
function hide() { clearTimeout(timer); target = null; text.value = '' }
function over(event: PointerEvent) {
  if (event.pointerType === 'touch') return
  const element = find(event.target)
  if (element === target) return
  hide()
  if (element) timer = setTimeout(() => void show(element), 380)
}
function revealFocused(focused: Element) {
  if (document.activeElement !== focused || !focused.matches(':focus-visible')) return
  const element = find(focused)
  if (element) void show(element)
  else hide()
}
function focusIn(event: FocusEvent) {
  focusObserver?.disconnect()
  const focused = event.target
  if (!(focused instanceof Element)) return
  const reveal = () => revealFocused(focused)
  // Grids and comboboxes keep focus on their root while arrows change the
  // active row. Reveal its clipped name without adding extra Tab stops.
  focusObserver = new MutationObserver(reveal)
  focusObserver.observe(focused, { attributes: true, subtree: true, attributeFilter: ['aria-activedescendant', 'data-tip'] })
  reveal()
}
function focusOut() { focusObserver?.disconnect(); cancelAnimationFrame(keyFrame); keyFrame = 0; hide() }
function pointerDown(event: PointerEvent) {
  if (find(event.target) !== touched?.element) touched = null
  hide()
}
function key(event: KeyboardEvent) {
  touched = null; hide()
  cancelAnimationFrame(keyFrame); keyFrame = 0
  const focused = document.activeElement
  if (event.key === 'Escape' || !(focused instanceof Element) || !focused.hasAttribute('aria-activedescendant')) return
  // The input retains focus while Vue patches its options. Re-read the active
  // name after that patch, including searches whose active option id stays 0.
  keyFrame = requestAnimationFrame(() => { keyFrame = 0; revealFocused(focused) })
}
function scrolled() {
  // Native focus may scroll its row after focusin has revealed the name.
  if (target && find(document.activeElement) === target) void show(target)
  else hide()
}
function touch(event: MouseEvent) {
  if (!('pointerType' in event) || event.pointerType !== 'touch') return
  const element = find(event.target)
  if (!element?.hasAttribute('data-clip-tip')) return
  if (!(event.target instanceof Node) || !element.contains(event.target)) return
  const full = element.dataset.tip ?? ''
  // The first tap reveals the name before its link/button can act. A second
  // tap activates the same name; a changed name starts a new disclosure.
  if (!touched || touched.element !== element || touched.text !== full) {
    touched = { element, text: full }
    event.preventDefault(); event.stopPropagation()
    void show(element)
  } else { touched = null; hide() }
}
onMounted(() => {
  document.addEventListener('pointerover', over)
  document.addEventListener('focusin', focusIn)
  document.addEventListener('focusout', focusOut)
  document.addEventListener('pointerdown', pointerDown)
  document.addEventListener('click', touch, true)
  document.addEventListener('keydown', key)
  document.addEventListener('scroll', scrolled, true)
})
onBeforeUnmount(() => {
  document.removeEventListener('pointerover', over)
  document.removeEventListener('focusin', focusIn)
  document.removeEventListener('focusout', focusOut)
  document.removeEventListener('pointerdown', pointerDown)
  document.removeEventListener('click', touch, true)
  document.removeEventListener('keydown', key)
  document.removeEventListener('scroll', scrolled, true)
  clearTimeout(timer)
  focusObserver?.disconnect()
  cancelAnimationFrame(keyFrame)
})
</script>

<template>
  <Teleport :to="layer ?? 'body'">
    <div v-if="text" ref="tip" class="tooltip" :class="{ below }" :style="{ transform: `translate(${x}px, ${y}px)` }" aria-hidden="true">{{ text }}</div>
  </Teleport>
</template>

<style scoped>
.tooltip {
  position: fixed; z-index: 80; top: 0; left: 0; max-width: min(320px, calc(100vw - 16px)); padding: 5px 10px; border-radius: 8px; pointer-events: none; overflow-wrap: anywhere;
  background: var(--tip-bg); color: var(--tip-ink); font-size: 12.5px; line-height: 1.4; white-space: pre-line;
  box-shadow: 0 0 0 1px var(--glass-rim), 0 10px 24px -10px rgba(0, 0, 0, .5);
}
</style>
