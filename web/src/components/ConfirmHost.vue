<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { nextTick, ref, watch } from 'vue'
import { confirmState, settleConfirm } from '../lib/confirm'
import AppIcon from './AppIcon.vue'
const dialog = ref<HTMLDialogElement>()
const confirmButton = ref<HTMLButtonElement>()
const cancelButton = ref<HTMLButtonElement>()
let opener: HTMLElement | null = null
watch(() => confirmState.request, async request => {
  if (request && !dialog.value?.open) {
    opener = document.activeElement as HTMLElement
    dialog.value?.showModal()
    await nextTick()
    ;(request.danger ? cancelButton.value : confirmButton.value)?.focus()
  } else if (!request && dialog.value?.open) {
    dialog.value.close()
    opener?.focus({ preventScroll: true })
  }
})
function backdrop(event: MouseEvent) { if (event.target === dialog.value) settleConfirm(false) }
</script>

<template>
  <dialog ref="dialog" class="confirm" aria-labelledby="confirm-title" aria-describedby="confirm-body" @cancel.prevent="settleConfirm(false)" @click="backdrop">
    <div v-if="confirmState.request" class="confirm-card">
      <h2 id="confirm-title">{{ confirmState.request.title }}</h2>
      <div class="actions">
        <button ref="cancelButton" type="button" class="btn" @click="settleConfirm(false)">{{ confirmState.request.cancelLabel ?? 'Cancel' }}</button>
        <button ref="confirmButton" type="button" class="btn" :class="confirmState.request.danger ? 'danger-solid' : 'on'" @click="settleConfirm(true)">{{ confirmState.request.confirmLabel }}</button>
      </div>
      <div id="confirm-body">
        <p v-if="confirmState.request.body">{{ confirmState.request.body }}</p>
        <ul v-if="confirmState.request.points?.length" class="points">
          <li v-for="point in confirmState.request.points" :key="point"><AppIcon name="info" :size="13" /><span>{{ point }}</span></li>
        </ul>
      </div>
    </div>
  </dialog>
</template>

<style scoped>
.confirm { position: fixed; inset: 96px 0 auto; margin: 0 auto; width: min(var(--dialog-s), calc(100vw - 32px)); max-height: calc(100dvh - 112px); padding: 0; border: 0; background: transparent; color: var(--ink); overflow: visible; }
.confirm::backdrop { background: var(--scrim); backdrop-filter: blur(2px); }
.confirm-card { box-sizing: border-box; display: flex; flex-direction: column; max-height: calc(100dvh - 112px); padding: 22px 24px 18px; border-radius: var(--radius); border: 1px solid var(--glass-edge); background: linear-gradient(165deg, var(--surface-raised), var(--surface-raised-2)); box-shadow: var(--shadow-pop), var(--shadow); }
h2 { flex: none; height: 26px; font-size: 18px; overflow: auto; white-space: nowrap; }
#confirm-body { min-height: 0; overflow: auto; scrollbar-gutter: stable; overflow-wrap: anywhere; }
p { margin-top: 8px; font-size: 13.5px; color: var(--ink-2); }
.points { display: grid; gap: 6px; margin: 12px 0 0; padding: 0; list-style: none; }
.points li { display: grid; grid-template-columns: 14px minmax(0, 1fr); gap: 8px; align-items: start; font-size: 13px; line-height: 1.45; color: var(--ink-2); }
.points svg { margin-top: 2.5px; color: var(--ink-3); }
.actions .btn { width: max-content; min-width: min(88px, 100%); max-width: 100%; flex: none; height: auto; min-height: 34px; white-space: normal; overflow-wrap: anywhere; line-height: 1.4; }
.actions { flex: none; display: flex; flex-wrap: wrap; justify-content: flex-end; gap: 8px; margin: 12px 0; }
.danger-solid { color: var(--surface-highlight); border-color: transparent; background: linear-gradient(180deg, var(--danger-hi), var(--danger-fill)); box-shadow: 0 0 0 1px color-mix(in srgb, var(--danger-fill) 50%, transparent), 0 8px 18px -10px color-mix(in srgb, var(--danger-fill) 70%, transparent); }
.danger-solid:hover { filter: brightness(1.05); background: linear-gradient(180deg, var(--danger-hi), var(--danger-fill)); }
@media (max-width: 720px) {
  .confirm { position: fixed; inset: 0; width: 100%; height: 100dvh; max-width: none; max-height: none; margin: 0; }
  .confirm-card { height: 100%; max-height: none; border-radius: 0; padding-bottom: calc(18px + env(safe-area-inset-bottom)); }
  #confirm-body { flex: 1; order: 1; }
  .actions { order: 2; margin-bottom: 0; }
  .actions .btn { flex: 1 0 auto; min-height: 44px; }
}
</style>
