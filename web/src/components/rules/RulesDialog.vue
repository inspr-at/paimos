<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { nextTick, onBeforeUnmount, onMounted, ref, useId } from 'vue'
import AppIcon from '../AppIcon.vue'

// One modal for the rules page: a title, one short line, a body that scrolls on
// its own. Centered task actions precede the body; side/phone actions stay below.
// 'side' slides in from the right (preview, history); 'center' is a task dialog.
const props = withDefaults(defineProps<{ title: string; lede?: string; size?: 'center' | 'wide' | 'side' | 'sheet'; busy?: boolean }>(), { lede: '', size: 'center', busy: false })
const emit = defineEmits<{ close: [] }>()
const dialog = ref<HTMLDialogElement>()
const id = useId()
let opener: HTMLElement | null = null

onMounted(async () => {
  opener = document.activeElement as HTMLElement | null
  dialog.value?.showModal()
  await nextTick()
  const first = dialog.value?.querySelector<HTMLElement>('[data-autofocus]')
  first?.focus()
})
onBeforeUnmount(() => { dialog.value?.close(); opener?.focus?.({ preventScroll: true }) })
function close() { if (!props.busy) emit('close') }
function backdrop(event: MouseEvent) { if (event.target === dialog.value) close() }
</script>

<template>
  <dialog ref="dialog" class="rules-dialog" :class="`size-${size}`" :aria-labelledby="`${id}-title`" @cancel.prevent="close" @click="backdrop">
    <div class="card">
      <header class="head">
        <div class="titles">
          <h2 :id="`${id}-title`">{{ title }}</h2>
          <p v-if="lede" class="lede">{{ lede }}</p>
        </div>
        <button type="button" class="icon-btn sm flat" :aria-label="`Close ${title.toLowerCase()}`" data-tip="Close · Esc" :disabled="busy" @click="close"><AppIcon name="close" :size="16" /></button>
      </header>
      <div v-if="$slots.pinned" class="pinned"><slot name="pinned" /></div>
      <footer v-if="$slots.footer" class="foot"><slot name="footer" /></footer>
      <div class="body"><slot /></div>
    </div>
  </dialog>
</template>

<style scoped>
.rules-dialog { padding: 0; border: 0; background: transparent; color: var(--ink); max-width: none; max-height: none; overflow: visible; }
.rules-dialog::backdrop { background: var(--scrim); }
.card { display: flex; flex-direction: column; min-height: 0; background: var(--surface); box-shadow: var(--shadow-pop); border: 1px solid var(--glass-edge); }
.size-center, .size-wide { position: fixed; inset: 96px 0 auto; width: min(560px, calc(100vw - 24px)); margin: 0 auto; }
.size-wide { width: min(760px, calc(100vw - 24px)); }
.size-center .card, .size-wide .card { max-height: min(760px, calc(100dvh - 112px)); border-radius: 16px; }
.size-side, .size-sheet { position: fixed; inset: 0 0 0 auto; width: min(var(--dialog-m), 100vw); height: 100%; margin: 0; }
.size-sheet { width: min(var(--dialog-l), 100vw); }
.size-side .card, .size-sheet .card { height: 100%; border-radius: 16px 0 0 16px; }
.head { flex: none; display: flex; align-items: flex-start; gap: 12px; padding: 18px 20px 12px; }
.titles { flex: 1; min-width: 0; }
.head h2 { margin: 0; font-size: 17px; font-weight: 650; letter-spacing: -.01em; }
.lede { margin: 4px 0 0; color: var(--ink-2); font-size: 13px; line-height: 1.45; }
.body { flex: 1; min-height: 0; overflow: auto; padding: 4px 20px 16px; display: flex; flex-direction: column; gap: 14px; overscroll-behavior: contain; }
.pinned { flex: none; display: flex; flex-direction: column; gap: 10px; padding: 12px 20px 4px; border-top: 1px solid var(--line); }
.size-side .body, .size-sheet .body { order: 1; }
.size-side .pinned, .size-sheet .pinned { order: 2; }
.size-side .foot, .size-sheet .foot { order: 3; }
.foot :deep(.btn:disabled) { opacity: .5; filter: saturate(.3); box-shadow: none; cursor: not-allowed; }
.foot { flex: none; display: flex; flex-wrap: wrap; align-items: center; justify-content: flex-end; gap: 8px; padding: 12px 20px 16px; border-top: 1px solid var(--line); background: var(--surface); }
@media (max-width: 600px) {
  .size-center, .size-wide { inset: 0; width: 100vw; margin: 0; }
  .body { order: 1; }
  .pinned { order: 2; }
  .foot { order: 3; }
  .size-center .card, .size-wide .card { height: 100dvh; max-height: 100dvh; border-radius: 16px 16px 0 0; }
  .size-side .card, .size-sheet .card { border-radius: 0; }
  .head { padding: 16px 16px 10px; }
  .body { padding: 4px 16px 14px; }
  .pinned { padding: 10px 16px 2px; }
  .foot { padding: 10px 16px calc(12px + env(safe-area-inset-bottom)); }
}
@media (prefers-reduced-motion: no-preference) {
  .rules-dialog[open] .card { animation: rules-dialog-in .16s ease-out; }
  .size-side[open] .card, .size-sheet[open] .card { animation-name: rules-dialog-side; }
}
@keyframes rules-dialog-in { from { opacity: 0; transform: translateY(6px); } }
@keyframes rules-dialog-side { from { opacity: .4; transform: translateX(24px); } }
</style>
