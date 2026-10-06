<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { onBeforeUnmount, ref, watch } from 'vue'
import { dismiss, toasts } from '../lib/toast'
import AppIcon from './AppIcon.vue'
import ReleaseName from './ReleaseName.vue'
function act(id: number, run: () => void) { dismiss(id); run() }
// While a modal dialog is open the page under it is inert and hidden behind the
// top layer: toasts show inside the topmost modal instead, and follow it as
// modals open and close (an Undo stays reachable after its dialog closes).
const layer = ref<HTMLElement | null>(null)
function place() {
  try { layer.value = [...document.querySelectorAll<HTMLElement>('dialog:modal')].at(-1) ?? null } catch { layer.value = null }
}
// Watched only while a toast is up: dialogs opening or closing, or leaving the page.
let dialogs: MutationObserver | null = null
watch(() => toasts.length, count => {
  place()
  if (count && !dialogs && typeof MutationObserver !== 'undefined') {
    dialogs = new MutationObserver(place)
    dialogs.observe(document.body, { subtree: true, childList: true, attributes: true, attributeFilter: ['open'] })
  } else if (!count) { dialogs?.disconnect(); dialogs = null }
}, { flush: 'pre' })
onBeforeUnmount(() => dialogs?.disconnect())
</script>

<template>
  <Teleport :to="layer ?? 'body'" :disabled="!layer">
  <div class="toast-host" aria-live="polite">
    <TransitionGroup name="toast">
      <div v-for="item in toasts" :key="item.id" class="toast" :class="[item.tone, { sticky: item.sticky }]">
        <AppIcon v-if="item.tone === 'error'" name="alert" :size="14" />
        <span v-if="item.release">{{ item.release.before }}<ReleaseName :version="item.release.version" :name="item.release.name" />{{ item.release.after }}</span>
        <span v-else>{{ item.message }}</span>
        <button v-for="a in item.actions" :key="a.label" class="toast-action" type="button" @click="act(item.id, a.run)">{{ a.label }}</button>
        <button class="toast-close" type="button" aria-label="Dismiss" @click="dismiss(item.id)"><AppIcon name="close" :size="12" /></button>
      </div>
    </TransitionGroup>
  </div>
  </Teleport>
</template>

<style scoped>
/* Toasts rise above the footer bar, never over it. */
.toast-host { position: fixed; z-index: 60; left: 50%; bottom: calc(var(--footer-h) + 14px); transform: translateX(-50%); display: flex; flex-direction: column; align-items: center; gap: 8px; pointer-events: none; width: max-content; max-width: calc(100vw - 32px); }
.toast {
  display: flex; align-items: center; gap: 10px; min-height: 40px; padding: 6px 8px 6px 18px; border-radius: 999px; pointer-events: auto;
  background: var(--tip-bg); color: var(--tip-ink); font-size: 13.5px; box-shadow: 0 0 0 1px var(--glass-rim), 0 18px 36px -14px color-mix(in srgb, var(--shadow-black) 45%, transparent);
  -webkit-backdrop-filter: blur(12px); backdrop-filter: blur(12px);
}
.toast.error svg { color: var(--inverse-danger); }
.toast span { min-width: 0; }
/* A release's stamp on the toast's dark glass: the toast's own ink and a hairline, in both themes. */
.toast :deep(.rn-stamp) { background: transparent; border-color: color-mix(in srgb, var(--surface-highlight) 28%, transparent); box-shadow: none; color: var(--tip-ink); }
.toast-action { height: 28px; padding: 0 12px; border: 0; border-radius: 999px; background: color-mix(in srgb, var(--primary-tint) 18%, transparent); color: var(--tip-ink); font-size: 12.5px; font-weight: 700; }
.toast-action:hover { background: color-mix(in srgb, var(--primary-tint) 30%, transparent); }
.toast-close { display: grid; place-items: center; width: 28px; height: 28px; border: 0; border-radius: 50%; background: transparent; color: inherit; opacity: .7; }
.toast-close:hover { opacity: 1; background: color-mix(in srgb, var(--surface-highlight) 8%, transparent); }
@media (prefers-reduced-motion: no-preference) {
  .toast-enter-active, .toast-leave-active { transition: opacity .2s ease, transform .2s ease; }
  .toast-enter-from, .toast-leave-to { opacity: 0; transform: translateY(8px); }
}
.toast-action + .toast-action { margin-left: -4px; }
/* Phones: 44px targets without a heavier toast. The toast loses its vertical
   padding and the buttons fill its height; the visible pill stays 28px. */
@media (max-width: 600px) {
  .toast:not(.sticky) { min-height: 44px; padding-block: 0; }
  .toast-action, .toast-close { position: relative; height: 44px; background: transparent; }
  .toast-close { width: 44px; margin-right: -6px; }
  .toast-action::before, .toast-close::before { content: ''; position: absolute; inset: 8px 0; border-radius: 999px; z-index: -1; }
  .toast-close::before { inset: 8px; }
  .toast-action { isolation: isolate; }
  .toast-close { isolation: isolate; }
  .toast-action::before { background: color-mix(in srgb, var(--primary-tint) 18%, transparent); }
  .toast-action:hover { background: transparent; }
  .toast-action:hover::before { background: color-mix(in srgb, var(--primary-tint) 30%, transparent); }
  .toast-close:hover { background: transparent; }
  .toast-close:hover::before { background: color-mix(in srgb, var(--surface-highlight) 8%, transparent); }
}
@media (max-width: 600px) { .toast-host { bottom: calc(var(--footer-h) + 10px); } .toast { font-size: 13px; } .toast.sticky { flex-wrap: wrap; justify-content: flex-end; border-radius: 20px; padding: 8px 8px 8px 16px; } .toast.sticky span { flex: 1 1 100%; } .toast.sticky:has(> svg) span { flex-basis: calc(100% - 24px); } }
</style>
