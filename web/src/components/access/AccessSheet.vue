<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { nextTick, onBeforeUnmount, onMounted, ref } from 'vue'
import AppIcon from '../AppIcon.vue'
import { useRoute } from 'vue-router'
import { safeReturnPath } from '../../lib/signInReturn'
import { useSession } from '../../stores/session'

// A sheet over the page: a panel on the right (or centred, for a short form),
// with a scrim. It is not in the browser's top layer, so menus and pickers
// opened inside it show above it. Focus moves in and stays in; Escape and the
// scrim close it (unless a menu is open, which closes first); focus returns.
const props = withDefaults(defineProps<{ title: string; label?: string; size?: 'side' | 'center'; wide?: boolean; actionsFirst?: boolean; submitShortcut?: boolean }>(), { label: undefined, size: 'side', wide: false, actionsFirst: false, submitShortcut: false })
const emit = defineEmits<{ close: []; submit: [] }>()
const route = useRoute()
const session = useSession()
function signInAgain() { window.open(`/signin?error=expired&return=${encodeURIComponent(safeReturnPath(route.fullPath))}`, '_blank', 'noopener') }
const panel = ref<HTMLElement>()
let opener: HTMLElement | null = null
const focusables = () => [...(panel.value?.querySelectorAll<HTMLElement>('a[href], button:not(:disabled), input:not(:disabled), select:not(:disabled), textarea:not(:disabled), [tabindex="0"]') ?? [])].filter(el => el.offsetParent !== null)
function keydown(event: KeyboardEvent) {
  if (document.querySelector('.floating, dialog[open]')) return
  if (props.submitShortcut && panel.value?.contains(document.activeElement) && !event.isComposing && !event.repeat && !event.altKey && !event.shiftKey && (event.metaKey || event.ctrlKey) && event.key === 'Enter') {
    event.preventDefault(); event.stopPropagation(); if (!session.requiresSignIn) emit('submit')
  }
  else if (event.key === 'Escape') {
    event.preventDefault(); event.stopPropagation()
    const target = document.activeElement
    if (props.actionsFirst && target instanceof HTMLElement && target.matches('input:not([type=checkbox]):not([type=radio]), textarea, select, [contenteditable=true]')) target.blur()
    else if (!session.requiresSignIn) emit('close')
  }
  else if (event.key === 'Tab') {
    const items = focusables()
    if (!items.length) return
    const first = items[0]!, last = items[items.length - 1]!
    if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last.focus() }
    else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first.focus() }
  }
}
onMounted(async () => {
  opener = document.activeElement as HTMLElement | null
  window.addEventListener('keydown', keydown, true)
  document.body.classList.add('sheet-open')
  await nextTick()
  const first = panel.value?.querySelector<HTMLElement>('[data-autofocus]') ?? panel.value?.querySelector<HTMLElement>('.sheet-close')
  first?.focus({ preventScroll: true })
})
onBeforeUnmount(() => {
  window.removeEventListener('keydown', keydown, true)
  document.body.classList.remove('sheet-open')
  if (opener?.isConnected) opener.focus({ preventScroll: true })
})
defineExpose({ panel })
</script>

<template>
  <Teleport to="body">
    <div class="sheet-root" :class="[size, { 'actions-first': actionsFirst }]">
      <div class="sheet-scrim" aria-hidden="true" @click="!session.requiresSignIn && emit('close')" />
      <section ref="panel" class="sheet" :class="{ wide }" role="dialog" aria-modal="true" :aria-label="props.label ?? title">
        <p v-if="session.requiresSignIn" class="sheet-ended" role="alert">Your session has ended. This sheet stays here so you can keep what you entered or copy a one-time secret. <button type="button" class="btn sm" data-session-keep @click="signInAgain">Sign in again</button></p>
        <header class="sheet-head">
          <slot name="head"><h2 class="sheet-title">{{ title }}</h2></slot>
          <button type="button" class="icon-btn sm flat sheet-close" :aria-label="`Close ${props.label ?? title}`" :disabled="session.requiresSignIn" @click="emit('close')"><AppIcon name="close" :size="14" /></button>
        </header>
        <div class="sheet-body"><slot /></div>
        <footer v-if="$slots.foot" class="sheet-foot"><slot name="foot" /></footer>
      </section>
    </div>
  </Teleport>
</template>

<style scoped>
.sheet-root { position: fixed; inset: 0; z-index: 55; display: flex; justify-content: flex-end; }
.sheet-root.center { align-items: center; justify-content: center; padding: 16px; }
.sheet-ended { position: relative; display: flex; align-items: center; flex-wrap: wrap; gap: 8px; padding: 10px 18px; background: var(--chip-teal-bg); font-size: 12px; }
.sheet-scrim { position: absolute; inset: 0; background: var(--scrim); backdrop-filter: blur(2px); }
.sheet {
  position: relative; display: flex; flex-direction: column; width: min(520px, 100vw); height: 100%;
  border-left: 1px solid var(--glass-edge); background: linear-gradient(165deg, var(--surface-raised), var(--surface-raised-2)); box-shadow: var(--shadow-pop);
}
.sheet.wide { width: min(640px, 100vw); }
.center .sheet { width: min(480px, 100%); height: auto; max-height: calc(100dvh - 32px); border: 1px solid var(--glass-edge); border-radius: var(--radius); }
.center .sheet.wide { width: min(580px, 100%); }
.sheet-head { display: flex; align-items: flex-start; gap: 12px; padding: 18px 18px 12px 22px; }
.sheet-head > :first-child { flex: 1; min-width: 0; }
.sheet-title { font: 600 17px/1.3 var(--font); letter-spacing: -.005em; color: var(--ink); }
.sheet-close { margin-top: -2px; }
.sheet-body { flex: 1; min-height: 0; overflow: auto; padding: 4px 22px 22px; overscroll-behavior: contain; }
.sheet-foot { display: flex; justify-content: flex-end; gap: 8px; padding: 12px 22px 16px; border-top: 1px solid var(--line); }
/* Keep the frame and actions at the top; variable content grows below them.
   Long content scrolls independently. Short forms keep their natural height. */
.center.actions-first { align-items: flex-start; padding-top: min(64px, 8dvh); }
.center.actions-first .sheet { max-height: calc(100dvh - min(64px, 8dvh) - 16px); }
.actions-first .sheet-head { order: 0; flex-shrink: 0; }
.actions-first .sheet-foot { order: 1; flex-shrink: 0; border-top: 0; border-bottom: 1px solid var(--line); }
.actions-first .sheet-foot :deep(.btn.primary) { flex: 1; }
.actions-first .sheet-foot :deep(.keycap) { display: inline-flex; gap: 3px; width: auto; }
.actions-first .sheet-body { order: 2; padding-top: 16px; }
@media (prefers-reduced-motion: no-preference) {
  .sheet { animation: sheet-in .2s cubic-bezier(.2, .7, .2, 1); }
  .center .sheet { animation-name: pop-in; }
  @keyframes sheet-in { from { transform: translateX(24px); opacity: 0; } to { transform: none; opacity: 1; } }
  @keyframes pop-in { from { transform: translateY(8px); opacity: 0; } to { transform: none; opacity: 1; } }
}
@media (max-width: 600px) {
  .sheet-root.center { padding: 0; align-items: stretch; }
  .center.actions-first .sheet { max-height: none; }
  .center .sheet, .center .sheet.wide { width: 100%; max-height: none; height: 100%; border-radius: 0; }
  .sheet-head { padding: 14px 12px 10px 16px; }
  .sheet-body { padding: 4px 16px 18px; }
  .sheet-foot { padding: 10px 16px 14px; }
  .sheet-foot :deep(.btn) { height: 44px; }
  .sheet-close { width: 44px; height: 44px; }
}
</style>
