<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
const props = defineProps<{ active: boolean; title: string }>()
const emit = defineEmits<{ cancel: []; save: []; undo: []; edit: [] }>()
const dialog = ref<HTMLDialogElement | null>(null)
function focusAction() { dialog.value?.querySelector<HTMLButtonElement>('[data-policy-save]')?.focus({ preventScroll: true }) }
const phone = ref(false)
const mac = /Mac|iPhone|iPad/.test(navigator.platform)
const submitKey = mac ? 'Command+Enter' : 'Ctrl+Enter'
let invoking: HTMLElement | null = null
let media: MediaQueryList
async function mode() {
  await nextTick()
  const el = dialog.value
  if (!el) return
  el.close()
  if (phone.value && props.active) el.showModal()
  else el.setAttribute('open', '')
}
watch(() => props.active, async active => {
  if (active) invoking = document.activeElement instanceof HTMLElement ? document.activeElement : null
  await mode()
  if (active) focusAction()
  else invoking?.focus({ preventScroll: true })
})
function resize() { phone.value = media.matches; void mode() }
onMounted(() => { media = matchMedia('(max-width: 600px)'); media.addEventListener('change', resize); resize() })
onBeforeUnmount(() => { media?.removeEventListener('change', resize); dialog.value?.close() })
function key(event: KeyboardEvent) {
  const target = event.target as HTMLElement
  const field = !!target.closest('input, textarea, select, [contenteditable="true"]')
  if (event.key === 'Escape') {
    event.preventDefault(); event.stopPropagation()
    if (field) focusAction(); else emit('cancel')
    return
  }
  if (event.altKey || (mac ? event.ctrlKey : event.metaKey)) return
  if (event.key === 'Enter' && (mac ? event.metaKey : event.ctrlKey)) { event.preventDefault(); focusAction(); emit('save'); return }
  if (event.ctrlKey || event.metaKey || field) return
  if (event.key.toLowerCase() === 'e') { event.preventDefault(); emit('edit') }
  if (event.key.toLowerCase() === 'u') { event.preventDefault(); emit('undo') }
}
defineExpose({ submitKey, focusAction })
</script>
<template>
  <div class="editor-reservation" :class="{ 'phone-open': active && phone }">
    <dialog ref="dialog" class="editor-frame" :aria-label="title" :role="phone && active ? 'dialog' : 'region'" :aria-modal="phone && active ? true : undefined" @keydown="key" @cancel.prevent="emit('cancel')">
      <header class="editor-header"><h3>{{ title }}</h3><div class="editor-actions" data-testid="policy-editor-actions"><slot name="actions" :submit-key="submitKey" /></div></header>
      <div class="editor-feedback" role="status" aria-live="polite" data-testid="policy-editor-status"><slot name="status" /></div>
      <div class="editor-body" data-testid="policy-editor-body"><slot /></div>
    </dialog>
  </div>
</template>
<style scoped>
.editor-reservation { height: 310px; margin-block: 12px 18px; }
.editor-frame { position: static; box-sizing: border-box; margin: 0; padding: 0; inline-size: 100%; block-size: 100%; max-inline-size: none; max-block-size: none; border: 0; border-block: 1px solid var(--line); background: transparent; color: var(--ink); display: grid; grid-template-rows: auto 64px minmax(0,1fr); }
.editor-header { padding: 12px 0 8px; display: grid; gap: 8px; }
h3 { margin: 0; font-size: 15px; line-height: 22px; overflow-wrap: anywhere; height: 22px; overflow-y: auto; }
.editor-actions { display: flex; flex-wrap: wrap; gap: 8px; align-items: center; min-height: 36px; }
.editor-feedback { overflow-y: auto; overflow-wrap: anywhere; font-size: 12px; line-height: 1.5; padding-block: 6px; color: var(--ink-2); }
.editor-body { min-height: 0; overflow: auto; padding-bottom: 12px; overscroll-behavior: contain; }
@media (max-width:600px) {
  .editor-reservation { height: 370px; }
  .phone-open .editor-frame { position: fixed; inset: 0; margin: 0; inline-size: 100%; block-size: 100dvh; background: var(--surface); padding: max(14px,env(safe-area-inset-top)) 16px 0; grid-template-rows: auto 80px minmax(0,1fr) auto; }
  .phone-open .editor-frame::backdrop { background: var(--scrim); }
  .phone-open .editor-header { display: contents; }
  .phone-open h3 { grid-row: 1; }
  .phone-open .editor-actions { grid-row: 4; padding: 12px 0 max(16px,env(safe-area-inset-bottom)); border-top: 1px solid var(--line); }
  .phone-open .editor-feedback { grid-row: 2; }
  .phone-open .editor-body { grid-row: 3; }
}
</style>
