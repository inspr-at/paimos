<!--
  PAIMOS — Your Professional & Personal AI Project OS
  Copyright (C) 2026 Markus Barta <markus@barta.com>
  AGPL-3.0-only — see LICENSE.

  PAI-805 — filter bar. Filters narrow the lanes; they never touch the
  selection (the view pins an excluded selected delivery).
-->
<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'

import AppIcon from '@/components/AppIcon.vue'
import type { Delivery } from '@/services/agentMode'
import {
  filtersActive,
  type AgentModeFilters,
  type HealthFilter,
} from '@/composables/agent-mode/agentModeFilters'

const props = defineProps<{
  filters: AgentModeFilters
  deliveries: readonly Delivery[]
}>()

const emit = defineEmits<{ 'update:filters': [value: AgentModeFilters] }>()
const { t } = useI18n()

const projects = computed(() => {
  const m = new Map<number, { id: number; key: string; name: string; count: number }>()
  for (const d of props.deliveries) {
    const p = m.get(d.lane.projectId)
    if (p) p.count += 1
    else m.set(d.lane.projectId, { id: d.lane.projectId, key: d.lane.projectKey, name: d.lane.projectName, count: 1 })
  }
  return [...m.values()].sort((a, b) => a.name.localeCompare(b.name))
})

const healthOptions: Array<{ value: HealthFilter; count: (list: readonly Delivery[]) => number }> = [
  { value: 'all', count: (l) => l.length },
  { value: 'attention', count: (l) => l.filter((d) => d.attention.level > 0 || d.health === 'attention' || d.health === 'at_risk').length },
  { value: 'blocked', count: (l) => l.filter((d) => d.health === 'blocked' || d.activity.kind === 'blocked' || d.blockers.length > 0).length },
  { value: 'stale', count: (l) => l.filter((d) => d.freshness.state === 'stale' || d.freshness.state === 'unknown').length },
]

const active = computed(() => filtersActive(props.filters))

function update(patch: Partial<AgentModeFilters>) {
  emit('update:filters', { ...props.filters, ...patch })
}

function onProject(event: Event) {
  const raw = (event.target as HTMLSelectElement).value
  update({ projectId: raw === '' ? null : Number(raw) })
}

function onQuery(event: Event) {
  update({ query: (event.target as HTMLInputElement).value })
}

function clear() {
  update({ projectId: null, health: 'all', query: '' })
}
</script>

<template>
  <div class="am-filters" role="group" :aria-label="t('agentMode.filters.label')">
    <label class="am-filter-project">
      <span class="am-sr-only">{{ t('agentMode.filters.project') }}</span>
      <AppIcon name="folder" :size="12" aria-hidden="true" />
      <select :value="filters.projectId ?? ''" @change="onProject">
        <option value="">{{ t('agentMode.filters.allProjects') }}</option>
        <option v-for="p in projects" :key="p.id" :value="p.id">{{ p.key }} · {{ p.name }} ({{ p.count }})</option>
      </select>
    </label>

    <div class="am-filter-health" role="group" :aria-label="t('agentMode.filters.healthLabel')">
      <button
        v-for="opt in healthOptions"
        :key="opt.value"
        type="button"
        :aria-pressed="filters.health === opt.value ? 'true' : 'false'"
        class="am-filter-chip"
        :class="{ 'is-active': filters.health === opt.value }"
        @click="update({ health: opt.value })"
      >
        {{ t(`agentMode.filters.health.${opt.value}`) }}
        <span class="am-filter-count">{{ opt.count(deliveries) }}</span>
      </button>
    </div>

    <label class="am-filter-query">
      <span class="am-sr-only">{{ t('agentMode.filters.query') }}</span>
      <AppIcon name="search" :size="12" aria-hidden="true" />
      <input
        type="search"
        :value="filters.query"
        :placeholder="t('agentMode.filters.query')"
        autocomplete="off"
        spellcheck="false"
        @input="onQuery"
      />
    </label>

    <button v-if="active" type="button" class="am-filter-clear" @click="clear">
      <AppIcon name="x" :size="11" aria-hidden="true" />
      {{ t('agentMode.filters.clear') }}
    </button>
  </div>
</template>

<style scoped>
.am-sr-only { position: absolute; width: 1px; height: 1px; margin: -1px; overflow: hidden; clip: rect(0, 0, 0, 0); white-space: nowrap; border: 0; }
.am-filters {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
}
.am-filter-project,
.am-filter-query {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 0 8px;
  height: 30px;
  border: 1px solid var(--am-line);
  border-radius: 9px;
  background: var(--am-surface);
  color: var(--am-muted);
}
.am-filter-project select,
.am-filter-query input {
  height: 100%;
  padding: 0;
  border: 0;
  background: transparent;
  color: var(--am-ink);
  font: inherit;
  font-size: 12px;
  outline: none;
  width: auto;
}
.am-filter-project select { max-width: 240px; }
.am-filter-query { flex: 1 1 180px; max-width: 320px; }
.am-filter-query input { flex: 1; min-width: 0; }
.am-filter-project:focus-within,
.am-filter-query:focus-within { border-color: var(--am-focus); box-shadow: 0 0 0 2px color-mix(in srgb, var(--am-focus) 18%, transparent); }

.am-filter-health {
  display: inline-flex;
  padding: 2px;
  border: 1px solid var(--am-line);
  border-radius: 9px;
  background: var(--am-surface);
}
.am-filter-chip {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  padding: 4px 9px;
  border: 0;
  border-radius: 7px;
  background: transparent;
  color: var(--am-muted);
  font-size: 12px;
}
.am-filter-chip.is-active { background: color-mix(in srgb, var(--am-ink) 9%, var(--am-surface)); color: var(--am-ink); font-weight: 600; }
.am-filter-chip:focus-visible { outline: 2px solid var(--am-focus); outline-offset: 1px; }
.am-filter-count { font-family: 'JetBrains Mono', ui-monospace, monospace; font-size: 10.5px; font-variant-numeric: tabular-nums; opacity: 0.85; }
.am-filter-clear {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 5px 9px;
  border: 1px dashed var(--am-line-strong);
  border-radius: 8px;
  background: transparent;
  color: var(--am-muted);
  font-size: 12px;
}
.am-filter-clear:hover { color: var(--am-ink); border-color: var(--am-ink); }
.am-filter-clear:focus-visible { outline: 2px solid var(--am-focus); outline-offset: 2px; }
</style>
