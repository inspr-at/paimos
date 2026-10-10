<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { nextTick, onBeforeUnmount, onMounted, ref, useId, watch } from 'vue'
import AppIcon from './AppIcon.vue'

// An explicit read action remains a tap/Enter/Space action even when the
// shared, passive clip-tip uses a long press. Native popover placement keeps
// this disclosure in the panel's DOM without moving or activating its row.
const props = defineProps<{ text: string }>()
const id = useId()
const anchor = `--read-name-${id.replace(/[^a-z0-9-]/gi, '-')}`
const button = ref<HTMLButtonElement>()
const detail = ref<HTMLElement>()
const shown = ref(false)
let restoring = false
async function read() {
  shown.value = true
  await nextTick()
  if (shown.value && !detail.value?.matches(':popover-open')) detail.value?.showPopover()
}
function close() { detail.value?.hidePopover(); shown.value = false }
function outside(event: PointerEvent) {
  if (!shown.value || !(event.target instanceof Node) || button.value?.contains(event.target) || detail.value?.contains(event.target)) return
  close()
}
function focused(event: FocusEvent) {
  if (!restoring && (event.target as HTMLElement).matches(':focus-visible')) void read()
}
function left(event: FocusEvent) {
  const next = event.relatedTarget
  if (next instanceof Node && (button.value?.contains(next) || detail.value?.contains(next))) return
  close()
}
function escape(event: KeyboardEvent) {
  if (!shown.value || event.key !== 'Escape') return
  event.preventDefault(); event.stopImmediatePropagation()
  close()
  restoring = true
  try { button.value?.focus({ preventScroll: true }) } finally { restoring = false }
}
function tab(event: KeyboardEvent) {
  if (event.ctrlKey || event.metaKey || event.altKey || event.shiftKey || !detail.value || detail.value.scrollHeight <= detail.value.clientHeight) return
  event.preventDefault(); event.stopPropagation()
  detail.value.focus({ preventScroll: true })
}
function returnToButton(event: KeyboardEvent) {
  if (!event.shiftKey || event.ctrlKey || event.metaKey || event.altKey) return
  event.preventDefault(); event.stopPropagation()
  button.value?.focus({ preventScroll: true })
}
watch(() => props.text, close)
onMounted(() => {
  window.addEventListener('keydown', escape, true)
  document.addEventListener('pointerdown', outside, true)
})
onBeforeUnmount(() => {
  window.removeEventListener('keydown', escape, true)
  document.removeEventListener('pointerdown', outside, true)
})
</script>

<template>
  <button ref="button" type="button" class="read-name" :aria-label="`Read full name: ${text}`"
    :aria-expanded="shown" :aria-controls="shown ? id : undefined" :style="{ anchorName: anchor }"
    @focus="focused" @focusout="left" @click="read" @keydown.enter.stop @keydown.space.stop @keydown.tab="tab">
    <AppIcon name="info" :size="14" />
  </button>
  <div v-if="shown" :id="id" ref="detail" popover="manual" class="read-name-detail" role="region" aria-label="Full name" tabindex="0"
    :style="{ positionAnchor: anchor }" @focusout="left"
    @keydown="event => { if (event.key !== 'Tab') event.stopPropagation() }" @keydown.tab="returnToButton">{{ text }}</div>
</template>

<style scoped>
.read-name { display: grid; place-items: center; align-self: stretch; min-width: 28px; padding: 0 6px; border: 0; background: transparent; color: var(--ink-3); }
.read-name:focus-visible { outline: none; box-shadow: var(--focus-ring); }
@media (hover: hover) { .read-name:hover { color: var(--ink); background: var(--row-hover); } }
@media (pointer: coarse), (max-width: 720px) { .read-name { min-width: 44px; min-height: 44px; } }
.read-name-detail {
  position: fixed; inset: auto; top: anchor(bottom); left: clamp(8px, anchor(left), calc(100vw - 328px));
  position-try-fallbacks: flip-block, --read-name-fit; margin: 8px 0; border: 0; width: max-content;
  max-width: min(320px, calc(100vw - 16px)); max-height: calc(100dvh - 32px); padding: 5px 10px; border-radius: 8px;
  overflow: auto; overscroll-behavior: contain; overflow-wrap: anywhere; white-space: pre-line;
  background: var(--tip-bg); color: var(--tip-ink); font-size: 12.5px; line-height: 1.4;
  box-shadow: 0 0 0 1px var(--glass-rim), 0 10px 24px -10px color-mix(in srgb, var(--shadow-black) 50%, transparent);
}
.read-name-detail:focus-visible { outline: 2px solid var(--ink-2); outline-offset: -2px; }
@position-try --read-name-fit { top: 8px; bottom: 8px; }
</style>
