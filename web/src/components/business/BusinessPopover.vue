<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { nextTick, onBeforeUnmount, onMounted, ref } from 'vue'
// A popover anchored to a trigger, like work/FloatingPanel, that can also render
// inside a modal dialog (`to`), where anything outside is inert. It is teleported so table cells
// and sticky toolbars never clip it; it flips above the trigger near the
// bottom edge, closes on Escape, outside clicks and page scroll, and hands
// focus back to the trigger when it closes by keyboard.
const props = withDefaults(defineProps<{ anchor: HTMLElement | null; align?: 'start' | 'end'; width?: number; label: string; to?: string }>(), { align: 'start', width: 240, to: 'body' })
const emit = defineEmits<{ close: [restoreFocus: boolean] }>()
const panel = ref<HTMLElement>()
const x = ref(-9999)
const y = ref(-9999)
const maxHeight = ref(360)
const above = ref(false)

function place() {
  if (!props.anchor || !panel.value) return
  const rect = props.anchor.getBoundingClientRect()
  const height = panel.value.scrollHeight
  const room = innerHeight - rect.bottom - 12
  // Open above when the menu would not fit below and there is more room above.
  above.value = room < Math.min(height, 420) && rect.top > room
  maxHeight.value = Math.max(160, Math.min(420, above.value ? rect.top - 12 : room))
  const width = Math.min(props.width, innerWidth - 16)
  const left = props.align === 'end' ? rect.right - width : rect.left
  x.value = Math.round(Math.min(Math.max(8, left), innerWidth - width - 8))
  y.value = Math.round(above.value ? rect.top - Math.min(height, maxHeight.value) - 6 : rect.bottom + 6)
}
function outside(event: PointerEvent) {
  const target = event.target as Node
  if (panel.value?.contains(target) || props.anchor?.contains(target)) return
  emit('close', false)
}
function scrolled(event: Event) {
  if (panel.value?.contains(event.target as Node)) return
  emit('close', false)
}
// Escape closes the popover wherever focus is, before any page shortcut sees it.
function escape(event: KeyboardEvent) {
  if (event.key !== 'Escape' || document.querySelector('dialog[open]')) return
  if (panel.value?.querySelector('.read-name-detail:popover-open')) return
  event.preventDefault(); event.stopImmediatePropagation()
  emit('close', true)
}
function keydown(event: KeyboardEvent) {
  if (event.key === 'Tab') emit('close', false)
}
onMounted(async () => {
  await nextTick()
  place()
  document.addEventListener('pointerdown', outside, true)
  document.addEventListener('scroll', scrolled, true)
  window.addEventListener('resize', place)
  window.addEventListener('keydown', escape, true)
  const first = panel.value?.querySelector<HTMLElement>('[data-autofocus]') ?? panel.value?.querySelector<HTMLElement>('input, button, [tabindex="0"]')
  first?.focus({ preventScroll: true })
})
onBeforeUnmount(() => {
  document.removeEventListener('pointerdown', outside, true)
  document.removeEventListener('scroll', scrolled, true)
  window.removeEventListener('resize', place)
  window.removeEventListener('keydown', escape, true)
})
defineExpose({ place })
</script>

<template>
  <Teleport :to="to">
    <div ref="panel" class="floating pop" :class="{ above }" role="dialog" :aria-label="label" :style="{ transform: `translate(${x}px, ${y}px)`, width: `${Math.min(width, 9999)}px`, maxHeight: `${maxHeight}px` }" @keydown="keydown">
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
