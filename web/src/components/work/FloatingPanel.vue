<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { nextTick, onBeforeUnmount, onMounted, ref } from 'vue'
// A popover anchored to a trigger. It is teleported to <body> so table cells
// and sticky toolbars never clip it; it flips above the trigger near the
// bottom edge, closes on Escape, outside clicks and scroll that moves its trigger, and hands
// focus back to the trigger when it closes by keyboard. A menu closes on Tab;
// a small form (`cycle`) keeps Tab among its own controls instead.
const props = withDefaults(defineProps<{ anchor: HTMLElement | null; align?: 'start' | 'end'; width?: number; label: string; tallest?: number; cycle?: boolean }>(), { align: 'start', width: 240, tallest: 420, cycle: false })
const emit = defineEmits<{ close: [restoreFocus: boolean] }>()
const panel = ref<HTMLElement>()
const x = ref(-9999)
const y = ref(-9999)
// Until placed (off-screen), the content may take its full height so place() measures it.
const maxHeight = ref(props.tallest)
const placedHeight = ref<number>()
const above = ref(false)
let placedAnchor: DOMRect | null = null

// Reserve the opening frame: results and feedback scroll inside it. In
// particular, an above-trigger search must not move when its result count changes.
function place(keepSide = false) {
  if (!props.anchor || !panel.value) return
  const rect = props.anchor.getBoundingClientRect()
  placedAnchor = rect
  const height = Math.max(panel.value.scrollHeight, panel.value.querySelector('input:not([type=checkbox]):not([type=radio])') ? Math.min(320, props.tallest) : 0)
  const room = innerHeight - rect.bottom - 12
  // Open above when the menu would not fit below and there is more room above.
  if (!keepSide) above.value = room < Math.min(height, props.tallest) && rect.top > room
  maxHeight.value = Math.max(160, Math.min(props.tallest, above.value ? rect.top - 12 : room))
  if (!keepSide) placedHeight.value = Math.min(height, maxHeight.value)
  const width = Math.min(props.width, innerWidth - 16)
  const left = props.align === 'end' ? rect.right - width : rect.left
  x.value = Math.round(Math.min(Math.max(8, left), innerWidth - width - 8))
  y.value = Math.round(above.value ? rect.top - Math.min(placedHeight.value ?? height, maxHeight.value) - 6 : rect.bottom + 6)
}
function outside(event: PointerEvent) {
  const target = event.target as Node
  if (panel.value?.contains(target) || props.anchor?.contains(target)) return
  emit('close', false)
}
function scrolled(event: Event) {
  if (panel.value?.contains(event.target as Node)) return
  // Opening can scroll the trigger into view. Its notification may arrive after
  // placement, even after the next frame; only new anchor movement dismisses it.
  // Point-anchored context menus still close on every outside scroll.
  if (props.anchor instanceof HTMLElement && placedAnchor) {
    const rect = props.anchor.getBoundingClientRect()
    if (rect.top === placedAnchor.top && rect.left === placedAnchor.left && rect.bottom === placedAnchor.bottom && rect.right === placedAnchor.right) return
  }
  emit('close', false)
}
// Inside a modal dialog (the release history's ticket panel) the popover moves
// into it: everything outside the top layer is inert while the modal is open.
// (Context menus anchor to a point, not an element: those stay in <body>.)
const layer = (props.anchor instanceof Element ? props.anchor.closest<HTMLElement>('dialog[open]') : null) ?? 'body'
// Escape closes the popover wherever focus is, before any page shortcut sees it;
// another dialog open above the popover's own layer handles its Escape itself.
function escape(event: KeyboardEvent) {
  if (event.key !== 'Escape' || [...document.querySelectorAll('dialog[open]')].some(dialog => dialog !== layer)) return
  event.preventDefault(); event.stopImmediatePropagation()
  emit('close', true)
}
let scrollFrame = 0
let resized: ResizeObserver | null = null
function keydown(event: KeyboardEvent) {
  if (event.key !== 'Tab') return
  if (!props.cycle) { emit('close', false); return }
  const stops = [...(panel.value?.querySelectorAll<HTMLElement>('input, button, textarea, select, [tabindex]') ?? [])]
    .filter(el => el.tabIndex >= 0 && !(el as HTMLButtonElement).disabled && el.getClientRects().length)
  if (!stops.length) return
  const edge = event.shiftKey ? stops[0] : stops[stops.length - 1]
  if (document.activeElement === edge || !panel.value?.contains(document.activeElement)) {
    event.preventDefault()
    ;(event.shiftKey ? stops[stops.length - 1] : stops[0]).focus()
  }
}
const onResize = () => place()
onMounted(async () => {
  await nextTick()
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
  cancelAnimationFrame(scrollFrame)
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
    <div ref="panel" class="floating pop" :class="{ above }" role="dialog" :aria-label="label" :style="{ transform: `translate(${x}px, ${y}px)`, width: `${Math.min(width, 9999)}px`, height: placedHeight === undefined ? undefined : `${placedHeight}px`, boxSizing: 'border-box', maxHeight: `${maxHeight}px`, '--floating-max': `${maxHeight}px` }" @keydown="keydown">
      <slot />
    </div>
  </Teleport>
</template>

<style scoped>
.floating { position: fixed; z-index: 70; top: 0; left: 0; max-width: calc(100vw - 16px); overflow: auto; padding: 6px; overscroll-behavior: contain; }
@media (prefers-reduced-motion: no-preference) {
  .floating { animation: pop-in .14s cubic-bezier(.2, .7, .2, 1); }
  @keyframes pop-in { from { opacity: 0; margin-top: -4px; } to { opacity: 1; margin-top: 0; } }
  .floating.above { animation-name: pop-in-above; }
  @keyframes pop-in-above { from { opacity: 0; margin-top: 4px; } to { opacity: 1; margin-top: 0; } }
}
</style>
