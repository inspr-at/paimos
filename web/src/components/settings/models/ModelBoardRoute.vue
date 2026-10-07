<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import ModelBoard from './ModelBoard.vue'
import AppIcon from '../../AppIcon.vue'
import KeyCap from '../../KeyCap.vue'
import { useSession } from '../../../stores/session'
import { isSettingsField } from '../../../lib/settingsOverlays'
import { returnFromBoard } from '../../../lib/modelsBoardNavigation'
const route = useRoute(), router = useRouter(), session = useSession()
const frame = ref<HTMLElement>()
const german = computed(() => route.query.lang === 'de' || document.documentElement.lang.startsWith('de'))
const owner = () => session.identity ? `${session.identity.tenant.id}/${session.identity.principal.id}` : ''
let previous: { element: HTMLElement; inert: boolean }[] = [], leaving = false
const done = async () => { if (leaving) return; leaving = true; await returnFromBoard(router, { ...route.query }, owner) }
function keys(event: KeyboardEvent) {
  if (event.key !== 'Escape' || event.defaultPrevented) return
  if (document.querySelector('.popover, [data-board-ghost]')) return
  event.preventDefault(); event.stopPropagation()
  if (isSettingsField(event.target)) { (event.target as HTMLElement).blur(); frame.value?.focus({ preventScroll: true }); return }
  void done()
}
onMounted(() => {
  // Leave the shared toast/tooltip hosts reachable while the underlying page is inert.
  previous = [...(document.querySelector('.app-shell')?.children ?? [])]
    .filter((element): element is HTMLElement => element instanceof HTMLElement && !element.matches('.toast-host, .tooltip-host'))
    .map(element => ({ element, inert: element.inert }))
  for (const item of previous) item.element.inert = true
  frame.value?.focus({ preventScroll: true }); window.addEventListener('keydown', keys)
})
onBeforeUnmount(() => { for (const item of previous) item.element.inert = item.inert; window.removeEventListener('keydown', keys) })
</script>
<template><Teleport to="body"><section ref="frame" class="fullboard" tabindex="-1" aria-labelledby="model-board-title"><header class="full-bar"><div class="full-t"><p class="crumb"><span>{{ german ? 'Einstellungen' : 'Settings' }}</span><AppIcon name="chevron-right" :size="11" /><span>{{ german ? 'Modelle' : 'Models' }}</span></p><h1 id="model-board-title">{{ german ? 'Modellboard' : 'Model board' }}</h1></div><button type="button" class="btn" data-board-done @click="done">{{ german ? 'Fertig' : 'Done' }}<KeyCap k="esc" /></button></header><div class="full-body"><ModelBoard full :german="german" /></div></section></Teleport></template>
<style scoped>.fullboard { position: fixed; inset: 0; z-index: 50; display: flex; flex-direction: column; background: var(--page-bg, var(--surface)); overflow: auto; overscroll-behavior: contain; outline: none; }.full-bar { position: sticky; top: 0; z-index: 3; display: flex; flex-wrap: wrap; align-items: center; justify-content: space-between; gap: 8px 16px; padding: 16px 24px; background: var(--surface-raised); box-shadow: 0 1px 0 var(--line); }.full-t h1 { font-size: 18px; font-weight: 650; }.full-t p { display: inline-flex; align-items: center; gap: 4px; margin: 0; font-size: 11px; color: var(--ink-3); }.full-body { min-width: 0; padding: 16px 12px 32px; }.btn { min-height: 36px; }@media (max-width: 860px) { .full-bar { padding: calc(12px + env(safe-area-inset-top)) 12px 12px; }.btn { min-height: 44px; }.full-body { padding-bottom: calc(32px + env(safe-area-inset-bottom)); } }</style>

<style scoped src="../../../styles/settingsButtons.css"></style>
