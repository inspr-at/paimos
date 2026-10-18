<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { inject, onBeforeUnmount, onMounted, ref } from 'vue'
import { onAction, shortcutTyping } from '../../lib/shortcuts'
import { settingsSubmitKey } from '../../lib/settingsOverlays'
import { PROJECT_SETTINGS_OVERLAY } from '../../lib/projectSettings'
import AppIcon from '../AppIcon.vue'

// escape: an open layer inside the sheet (a list) closes first and returns true.
// focusSheet: the sheet itself takes focus (no field to type into first, and no ring on Close).
const props = defineProps<{ title: string; description?: string; closeLabel?: string; lang?: string; escape?: () => boolean; focusSheet?: boolean }>()
const emit = defineEmits<{ close: []; submit: [] }>()
const dialog = ref<HTMLDialogElement>()
const registerOverlay = inject(PROJECT_SETTINGS_OVERLAY, null)
let unregister: (() => void) | undefined
const opener = document.activeElement as HTMLElement | null
const owns = (event: KeyboardEvent) => !!dialog.value?.contains(event.target as Node) && !!dialog.value?.matches(':modal')
const keys = registerOverlay ? [] : [
  onAction('project.settings.dismiss', event => {
    if (props.escape?.()) return
    if (shortcutTyping(event.target)) { (event.target as HTMLElement).blur(); dialog.value?.focus({ preventScroll: true }) }
    else emit('close')
  }, { when: owns }),
  onAction('project.settings.submit', () => emit('submit'), { when: event => owns(event) && settingsSubmitKey(event) }),
]
function backdrop(event: MouseEvent) {
  if (event.target !== dialog.value) return
  const box = dialog.value.getBoundingClientRect()
  if (event.clientX < box.left || event.clientX > box.right || event.clientY < box.top || event.clientY > box.bottom) emit('close')
}
onMounted(() => {
  unregister = registerOverlay?.({ element: () => dialog.value, close: () => emit('close'), submit: () => emit('submit') })
  dialog.value?.showModal(); (props.focusSheet ? dialog.value : dialog.value?.querySelector<HTMLElement>('[autofocus]'))?.focus({ preventScroll: true })
})
onBeforeUnmount(() => { unregister?.(); keys.forEach(dispose => dispose()); dialog.value?.close(); if (opener?.isConnected) opener.focus({ preventScroll: true }) })
</script>

<template>
  <Teleport to="body">
    <dialog ref="dialog" class="project-settings-sheet sheet" tabindex="-1" aria-labelledby="project-settings-sheet-title" :lang="lang" @cancel.prevent="emit('close')" @click="backdrop">
      <header class="sh-head"><div class="sh-t"><h2 id="project-settings-sheet-title">{{ props.title }}</h2><slot name="description"><p v-if="description">{{ description }}</p></slot></div><button type="button" class="icon-btn" :aria-label="closeLabel ?? 'Close sheet'" @click="emit('close')"><AppIcon name="close" :size="16" /></button></header>
      <div class="sh-body"><slot /></div>
      <footer class="sh-foot"><slot name="actions" /></footer>
    </dialog>
  </Teleport>
</template>

<style scoped>
.project-settings-sheet { position: fixed; inset: 0 0 0 auto; margin: 0; width: min(30rem, 100vw); max-width: 100vw; height: 100dvh; max-height: 100dvh; padding: 0; border: 0; background: var(--surface-raised); color: var(--ink); box-shadow: var(--shadow-pop); }
.project-settings-sheet[open] { display: grid; grid-template-rows: auto minmax(0, 1fr) auto; }.project-settings-sheet:focus { outline: none; }.project-settings-sheet::backdrop { background: var(--scrim); }
.sh-head { display: flex; align-items: flex-start; gap: 12px; padding: max(20px, env(safe-area-inset-top)) 16px 10px 26px; }.sh-t { flex: 1; min-width: 0; }.sh-t h2 { font: 600 18px/1.3 var(--font); overflow-wrap: anywhere; }.sh-t :slotted(p), .sh-t p { margin-top: 4px; font-size: 13px; color: var(--ink-2); line-height: 1.5; }
.sh-body { overflow: auto; min-width: 0; padding: 4px 26px 20px; }.sh-foot { display: flex; flex-wrap: wrap; justify-content: flex-end; gap: 8px; padding: 14px 20px max(14px, env(safe-area-inset-bottom)); border-top: 1px solid var(--line); }
@media (pointer: coarse), (max-width: 600px) { .sh-head .icon-btn { width: 44px; height: 44px; }.sh-foot :deep(.btn) { min-height: 44px; flex: 1 0 auto; } }
</style>
