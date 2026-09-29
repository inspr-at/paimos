<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import type { SessionTab } from './sessionChat'

// Overview and Messages for one session; Messages carries the unread count.
const props = defineProps<{ selected: SessionTab; unread: number }>()
const emit = defineEmits<{ select: [tab: SessionTab] }>()
const tabs: { id: SessionTab; label: string }[] = [{ id: 'overview', label: 'Overview' }, { id: 'messages', label: 'Messages' }]
function move(event: KeyboardEvent) {
  if (event.altKey || event.ctrlKey || event.metaKey || !['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return
  event.preventDefault()
  const next = event.key === 'Home' ? tabs[0]! : event.key === 'End' ? tabs[tabs.length - 1]! : tabs.find(t => t.id !== props.selected)!
  emit('select', next.id)
  ;(event.currentTarget as HTMLElement).querySelector<HTMLElement>(`#session-tab-${next.id}`)?.focus()
}
const countLabel = (n: number) => n > 99 ? '99+' : String(n)
</script>

<template>
  <div class="session-tabs" role="tablist" aria-label="Session" @keydown="move">
    <button v-for="tab in tabs" :id="`session-tab-${tab.id}`" :key="tab.id" type="button" role="tab"
      :aria-selected="selected === tab.id" :aria-controls="`session-panel-${tab.id}`" :tabindex="selected === tab.id ? 0 : -1"
      @click="emit('select', tab.id)">
      <span>{{ tab.label }}</span>
      <span v-if="tab.id === 'messages' && unread" class="count" :aria-label="`${unread} unread`">{{ countLabel(unread) }}</span>
    </button>
  </div>
</template>

<style scoped>
.session-tabs { display: flex; gap: 2px; margin-top: 10px; padding: 3px; border-radius: 10px; background: var(--seg-bg); width: max-content; max-width: 100%; }
button { display: inline-flex; align-items: center; justify-content: center; gap: 7px; min-width: 0; min-height: 30px; padding: 0 14px; border: 0; border-radius: 7px; background: transparent; color: var(--ink-2); font-size: 13px; font-weight: 600; white-space: nowrap; }
@media (hover: hover) { button:hover { color: var(--ink); background: var(--row-hover); } }
button[aria-selected="true"] { background: var(--seg-on); color: var(--ink); box-shadow: 0 1px 2px rgba(32, 60, 61, .12), inset 0 0 0 1px var(--glass-edge); }
button:focus-visible { outline: none; box-shadow: var(--focus-ring); }
.count { display: inline-grid; place-items: center; min-width: 18px; height: 18px; padding: 0 5px; border-radius: 999px; background: var(--teal); color: var(--canvas); font: 650 11px/1 var(--mono); font-variant-numeric: tabular-nums; font-variant-ligatures: none; }
@media (max-width: 720px) {
  .session-tabs { width: auto; margin-right: 8px; }
  button { flex: 1 1 0; min-height: 38px; }
}
</style>
