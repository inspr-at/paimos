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
let sourceObserver: MutationObserver | undefined

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
  if (!value || !element.isConnected) { hide(); return }
  clearTimeout(timer)
  if (target !== element) {
    sourceObserver?.disconnect()
    sourceObserver = new MutationObserver(() => {
      if (target !== element) return
      if (!element.isConnected || !element.dataset.tip) hide()
      else if (element.dataset.tip !== text.value) void show(element)
    })
    // Observe removals too: a record can leave the DOM without pointerout or
    // focusout. Attribute filtering keeps unrelated name updates inexpensive.
    sourceObserver.observe(document.body, { childList: true, subtree: true, attributes: true, attributeFilter: ['data-tip'] })
  }
  target = element
  layer.value = element.closest<HTMLElement>('dialog[open]')
  text.value = value
  await nextTick()
  if (target !== element || !element.isConnected) { if (target === element) hide(); return }
  const rect = element.getBoundingClientRect()
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
function hide() { clearTimeout(timer); sourceObserver?.disconnect(); target = null; text.value = '' }
function over(event: PointerEvent) {
  if (event.pointerType === 'touch') return
  const element = find(event.target)
  if (element === target) return
  hide()
  if (element) timer = setTimeout(() => void show(element), 380)
}
function revealFocus(focused: Element) {
  if (document.activeElement !== focused || !focused.matches(':focus-visible')) return
  const element = find(focused)
  if (element?.dataset.tip) void show(element)
  else hide()
}
function focusIn(event: FocusEvent) {
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
  revealFocus(focused)
}
function keydown(event: KeyboardEvent) {
  hide()
  // An arrow at a list boundary need not change aria-activedescendant.
  // Refresh after the owning component has processed that navigation.
  if (['ArrowDown', 'ArrowUp', 'ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) {
    const focused = document.activeElement
    if (focused?.hasAttribute('aria-activedescendant')) void nextTick(() => revealFocus(focused))
  }
}
function focusOut() { focusObserver?.disconnect(); hide() }
function touch(event: MouseEvent) {
  // Touch's compatibility mouse events can move focus after pointerup. Reveal
  // after click, so that focusout cannot immediately erase the tapped name.
  if (!('pointerType' in event) || event.pointerType !== 'touch') return
  const element = find(event.target)
  if (element?.hasAttribute('data-clip-tip')) void show(element)
}
onMounted(() => {
  document.addEventListener('pointerover', over)
  document.addEventListener('focusin', focusIn)
  document.addEventListener('focusout', focusOut)
  document.addEventListener('pointerdown', hide)
  document.addEventListener('click', touch)
  document.addEventListener('keydown', keydown)
  document.addEventListener('scroll', hide, true)
})
onBeforeUnmount(() => {
  document.removeEventListener('pointerover', over)
  document.removeEventListener('focusin', focusIn)
  document.removeEventListener('focusout', focusOut)
  document.removeEventListener('pointerdown', hide)
  document.removeEventListener('click', touch)
  document.removeEventListener('keydown', keydown)
  document.removeEventListener('scroll', hide, true)
  hide()
  focusObserver?.disconnect()
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
