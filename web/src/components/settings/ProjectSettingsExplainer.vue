<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, useId, watch } from 'vue'
import AppIcon from '../AppIcon.vue'
import FloatingPanel from '../work/FloatingPanel.vue'
import { claimSettingsPopover } from '../../lib/settingsOverlays'
import { useProjectSettingsOverlay } from '../../lib/projectSettings'
import { fillProjectSettingsExplainer, PROJECT_SETTINGS_EXPLAINERS, type ProjectSettingsExplainerContext, type ProjectSettingsExplainerId } from '../../lib/projectSettingsExplainers'

const props = defineProps<ProjectSettingsExplainerContext & { id: ProjectSettingsExplainerId }>()
const button = ref<HTMLButtonElement>(), heading = ref<HTMLElement>(), content = ref<HTMLElement>()
const open = ref(false), phone = ref(false), headingId = useId()
const registerOverlay = useProjectSettingsOverlay()
let release: (() => void) | undefined, unregister: (() => void) | undefined
const text = computed(() => PROJECT_SETTINGS_EXPLAINERS[props.id])
function fill(value: string): string {
  return fillProjectSettingsExplainer(value, props)
}
function close(restore = true) {
  open.value = false; release?.(); release = undefined; unregister?.(); unregister = undefined
  if (restore && button.value?.isConnected) button.value.focus({ preventScroll: true })
}
async function show() {
  if (open.value) { close(); return }
  phone.value = innerWidth <= 600
  release = claimSettingsPopover(() => close(false))
  open.value = true
  unregister = registerOverlay({ element: () => content.value, close })
  await nextTick()
  if (open.value) heading.value?.focus({ preventScroll: true })
}
watch(() => [props.projectKey, props.id], () => close(false))
onBeforeUnmount(() => close(false))
</script>

<template>
  <button ref="button" type="button" class="how" :data-how="id" aria-haspopup="dialog" :aria-expanded="open" :aria-controls="open ? headingId : undefined" @click="show"><AppIcon name="info" :size="12" /><span>How this works</span></button>
  <Teleport v-if="open && phone" to="body"><div class="explainer-scrim" aria-hidden="true" @click="close()" /></Teleport>
  <FloatingPanel v-if="open" :anchor="button ?? null" :width="400" :tallest="800" :label="text.title" cycle class="explainer project-settings-explainer" :class="{ 'as-sheet': phone }" @close="restore => close(restore)">
    <div ref="content" class="explainer-content" data-project-settings-overlay tabindex="-1">
      <header class="ex-head"><h3 :id="headingId" ref="heading" tabindex="-1" data-autofocus>{{ text.title }}</h3><button type="button" class="icon-btn sm" aria-label="Close explanation" @click="close()"><AppIcon name="close" :size="14" /></button></header>
      <section class="ex-s"><p class="ex-l">What it is</p><p>{{ fill(text.what) }}</p></section>
      <section class="ex-s"><p class="ex-l">What happens</p><p>{{ fill(text.effect) }}</p></section>
      <section class="ex-s"><p class="ex-l">Who sets it</p><p>{{ fill(text.who) }}</p></section>
      <p v-if="'where' in text && text.where" class="ex-where"><AppIcon name="eye" :size="13" /><span>{{ fill(text.where.label) }}</span></p>
    </div>
  </FloatingPanel>
</template>

<style scoped>
.how { display: inline-flex; align-items: center; gap: 4px; padding: 0; border: 0; background: transparent; color: var(--ink-3); font: 500 12px/1.4 var(--font); white-space: nowrap; cursor: pointer; }
.how:hover, .how[aria-expanded="true"] { color: var(--teal-ink); }
.how:hover span, .how[aria-expanded="true"] span { text-decoration: underline; text-underline-offset: 2px; }
/* FloatingPanel's root is a Teleport: it cannot inherit this component's
   scope attribute. Target its explicit, unique class on the teleported panel. */
:global(.floating.pop.project-settings-explainer) { padding: 14px 16px 16px; border: 1px solid var(--glass-edge); background: var(--surface-raised); border-radius: 12px; box-shadow: var(--shadow-pop); }
.explainer-content:focus, h3:focus { outline: none; }
.ex-head { display: flex; align-items: flex-start; gap: 8px; }
.ex-head h3 { flex: 1; min-width: 0; padding-top: 4px; font: 600 15px/1.35 var(--font); }
.ex-s { margin-top: 10px; }
.ex-l { font: 500 10.5px/1.6 var(--mono); letter-spacing: .12em; text-transform: uppercase; color: var(--ink-3); }
.ex-s p:not(.ex-l) { margin-top: 2px; font-size: 13px; line-height: 1.55; color: var(--ink-2); overflow-wrap: anywhere; }
.ex-where { display: flex; align-items: flex-start; gap: 8px; margin-top: 14px; padding: 9px 11px; border-radius: 10px; background: var(--surface-sunken); font-size: 12.5px; line-height: 1.5; color: var(--ink-2); }
.ex-where svg { flex: none; margin-top: 2px; }
.explainer-scrim { position: fixed; inset: 0; z-index: 69; background: var(--scrim); }
:global(.floating.pop.project-settings-explainer.as-sheet) { z-index: 70; top: auto !important; bottom: 0; left: 0; width: 100% !important; max-width: none; max-height: 76dvh !important; height: auto !important; transform: none !important; padding: 16px 18px max(18px, env(safe-area-inset-bottom)); border-radius: 18px 18px 0 0; border-bottom: 0; }
@media (pointer: coarse), (max-width: 600px) { .how { min-height: 44px; padding: 0 6px; margin: -12px -6px; } .ex-head .icon-btn { width: 44px; height: 44px; } }
</style>
