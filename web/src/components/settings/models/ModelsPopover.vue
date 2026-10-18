<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { claimSettingsPopover } from '../../../lib/settingsOverlays'

// One anchored menu for the Models card: opens below its trigger, flips above when that fits better, shrinks its
// list to the room it has, and never moves the trigger. Esc and a click elsewhere close it; Esc returns focus.
const props = defineProps<{ open: boolean; anchor: HTMLElement | null; label: string; role?: 'dialog' | 'presentation'; width?: number; kind?: 'pm' | 'why'; labelledby?: string }>()
const emit = defineEmits<{ 'update:open': [open: boolean]; close: [refocus: boolean] }>()
const panel = ref<HTMLElement>()
let release: (() => void) | undefined, frame = 0
// The side is chosen once per opening. A fold that opens, a scroll or a resize never flips the menu over its trigger,
// and above its trigger the menu keeps the height it opened with: more content scrolls in its list rather than
// pushing the field and the options under the pointer away (AEON-541).
let side: 'above' | 'below' | null = null, openedHeight = 0

function place() {
  const a = props.anchor, p = panel.value
  if (!a || !p) return
  if (!a.isConnected) { close(false); return }
  // Positioned straight on the element: a scroll or resize must not wait for a render.
  p.style.maxHeight = ''
  const rect = a.getBoundingClientRect(), width = p.offsetWidth, roomBelow = innerHeight - rect.bottom - 18, roomAbove = rect.top - 18
  let height = p.offsetHeight
  side ??= height > roomBelow && roomAbove > roomBelow ? 'above' : 'below'
  const up = side === 'above'
  const room = up ? Math.min(roomAbove, openedHeight || roomAbove) : roomBelow
  if (height > room) { p.style.maxHeight = `${room}px`; height = room }
  if (up && !openedHeight) openedHeight = height
  p.style.left = `${Math.max(12, Math.min(rect.left, innerWidth - width - 12))}px`
  p.style.top = `${Math.max(12, up ? rect.top - height - 6 : rect.bottom + 6)}px`
}
function close(refocus = true) {
  if (!props.open) return
  emit('update:open', false); emit('close', refocus)
  if (refocus && props.anchor?.isConnected) props.anchor.focus({ preventScroll: true })
}
function outside(event: PointerEvent) {
  const target = event.target as Node
  if (!panel.value?.contains(target) && !props.anchor?.contains(target)) close(false)
}
function keys(event: KeyboardEvent) {
  if (event.key !== 'Escape' || !props.open) return
  event.preventDefault(); event.stopImmediatePropagation(); close(true)
}
function leaves(event: FocusEvent) {
  const next = event.relatedTarget as Node | null
  if (next && !panel.value?.contains(next) && next !== props.anchor) close(false)
}
function moved() { cancelAnimationFrame(frame); frame = requestAnimationFrame(place) }
function attach() {
  release = claimSettingsPopover(() => close(false))
  document.addEventListener('pointerdown', outside, true)
  document.addEventListener('keydown', keys, true)
  addEventListener('scroll', moved, true); addEventListener('resize', moved)
}
function detach() {
  release?.(); release = undefined
  cancelAnimationFrame(frame)
  document.removeEventListener('pointerdown', outside, true)
  document.removeEventListener('keydown', keys, true)
  removeEventListener('scroll', moved, true); removeEventListener('resize', moved)
}
watch(() => props.open, async open => {
  if (!open) { detach(); return }
  await nextTick()
  if (!props.open) return
  if (!props.anchor?.isConnected) { close(false); return }
  side = null; openedHeight = 0
  place(); attach()
  panel.value?.querySelector<HTMLElement>('[data-autofocus]')?.focus({ preventScroll: true })
}, { immediate: true })
onBeforeUnmount(detach)
defineExpose({ place })
</script>
<template>
  <Teleport to="body">
    <div v-if="open" ref="panel" class="mdl-pop" :class="kind ?? 'pm'" :role="role === 'presentation' ? undefined : (role ?? 'dialog')" :aria-label="labelledby ? undefined : label" :aria-labelledby="labelledby" tabindex="-1" :style="width ? { '--pm-w': `${width}px` } : undefined" @focusout="leaves"><slot /></div>
  </Teleport>
</template>
