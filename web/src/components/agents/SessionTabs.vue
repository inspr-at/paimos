<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import { TAB_LABEL, type LeadTab, type PanelTab } from '../../lib/leadPanel'
import AppIcon from '../AppIcon.vue'
const props = defineProps<{ selected: PanelTab; unread: number; lead?: boolean; pinned?: LeadTab | null }>()
const emit = defineEmits<{ select: [tab: PanelTab]; pin: [] }>()
const tabs = computed<PanelTab[]>(() => props.lead ? ['lead', 'session', 'chat'] : ['overview', 'chat'])
const panelId = (tab: PanelTab) => tab === 'chat' ? 'messages' : tab === 'session' ? 'overview' : tab
function move(event: KeyboardEvent) {
  if (event.altKey || event.ctrlKey || event.metaKey || !['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return
  event.preventDefault(); event.stopPropagation()
  const index = tabs.value.indexOf(props.selected)
  const next = event.key === 'Home' ? tabs.value[0]! : event.key === 'End' ? tabs.value.at(-1)! : tabs.value[(index + (event.key === 'ArrowRight' ? 1 : -1) + tabs.value.length) % tabs.value.length]!
  emit('select', next)
  ;(event.currentTarget as HTMLElement).querySelector<HTMLElement>(`#session-tab-${panelId(next)}`)?.focus()
}
</script>

<template>
  <div class="tabs-line">
    <div class="session-tabs" role="tablist" :aria-label="lead ? 'Lead panel' : 'Session'" @keydown="move">
      <button v-for="tab in tabs" :id="`session-tab-${panelId(tab)}`" :key="tab" type="button" role="tab" :data-tab="tab" :aria-selected="selected === tab" :aria-controls="`session-panel-${panelId(tab)}`" :tabindex="selected === tab ? 0 : -1" @click="emit('select', tab)">
        {{ TAB_LABEL[tab] }}
        <span v-if="tab === 'chat'" class="count" :class="{ hidden: !unread }" :aria-label="unread ? `${unread} unread` : undefined" :aria-hidden="!unread">{{ unread > 99 ? '99+' : unread || 1 }}</span>
        <span v-if="lead" class="pinned" :class="{ hidden: pinned !== tab }" :data-tip="pinned === tab ? 'Your default' : undefined" :aria-hidden="pinned !== tab"><AppIcon name="pin" :size="11" /></span>
      </button>
    </div>
    <button v-if="lead" type="button" class="pin-btn" :aria-pressed="pinned === selected" :aria-label="pinned === selected ? 'Remove your default tab' : 'Make this your default tab'" :data-tip="pinned === selected ? 'Remove your default tab' : 'Make this your default tab'" @click="emit('pin')"><AppIcon name="pin" :size="13" /><span class="pl"><span :class="{ hidden: pinned === selected }" :aria-hidden="pinned === selected">Set as default</span><span :class="{ hidden: pinned !== selected }" :aria-hidden="pinned !== selected">Default</span></span></button>
  </div>
</template>

<style scoped>
.tabs-line { display: flex; align-items: center; gap: 2px; padding: 0 12px; border-bottom: 1px solid var(--line); min-width: 0; }
.session-tabs { display: flex; align-items: center; gap: 2px; min-width: 0; }
button { position: relative; display: inline-flex; align-items: center; gap: 6px; min-height: 40px; padding: 0 12px; border: 0; border-radius: 8px 8px 0 0; background: transparent; color: var(--ink-2); font-size: 13.5px; font-weight: 600; white-space: nowrap; }
button:hover { color: var(--ink); background: var(--row-hover); }
button[aria-selected="true"] { color: var(--teal-ink); background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); }
button:focus-visible { outline: none; box-shadow: var(--focus-ring); }
.count { display: inline-grid; place-items: center; min-width: 18px; height: 18px; padding: 0 5px; border-radius: 999px; background: var(--primary); color: var(--primary-on); font: 700 11px var(--font); }
.pinned { display: inline-grid; color: var(--ink-3); }
.hidden { visibility: hidden; }
.pin-btn { margin-left: auto; gap: 5px; min-height: 30px; padding: 0 9px; border-radius: 999px; color: var(--ink-3); font-size: 12px; }
.pin-btn[aria-pressed="true"] { color: var(--teal-ink); background: var(--chip-teal-bg); }
.pl { display: grid; }
.pl > span { grid-area: 1 / 1; }
@media (max-width: 720px) {
  .tabs-line { padding: 0 8px; }
  button, .pin-btn { min-height: 44px; }
  .pin-btn { min-width: 44px; justify-content: center; padding: 0; }
  .pl { position: absolute; width: 1px; height: 1px; overflow: hidden; clip-path: inset(50%); white-space: nowrap; }
}
@media (pointer: coarse) { button, .pin-btn { min-height: 44px; } }
</style>
