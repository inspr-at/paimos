<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script lang="ts">
// The three work counts a project shows everywhere: Open, Doing, Done. "Doing"
// is the short, kanban-standard word; "In progress" stays in the tooltip and
// what screen readers hear.
export type StatKind = 'open' | 'doing' | 'done'
export const STATS: { kind: StatKind; label: string; long: string; state: string }[] = [
  { kind: 'open', label: 'Open', long: 'Open', state: 'open' },
  { kind: 'doing', label: 'Doing', long: 'In progress', state: 'in_progress' },
  { kind: 'done', label: 'Done', long: 'Done', state: 'done' },
]
</script>
<script setup lang="ts">
import { computed } from 'vue'
import StatusIcon from '../work/StatusIcon.vue'

// One count cell: the status icon holds the left edge of the cell and the number
// its right edge in tabular figures, so a column of cells reads as two straight
// lines. `label` puts the word between them (cards).
const props = withDefaults(defineProps<{ kind: StatKind; value: number; size?: number; label?: boolean }>(), { size: 11, label: false })
const meta = computed(() => STATS.find(s => s.kind === props.kind)!)
</script>

<template>
  <span class="stat-count" :class="[`k-${kind}`, { zero: !value, labelled: label }]">
    <StatusIcon :state="meta.state" :size="size" />
    <span v-if="label" class="stat-label">{{ meta.label }}</span>
    <span class="stat-num">{{ value.toLocaleString('en-GB') }}</span>
  </span>
</template>

<style scoped>
.stat-count { display: flex; align-items: center; gap: 8px; min-width: 0; font: 500 13px/1 var(--mono); font-variant-numeric: tabular-nums; font-variant-ligatures: none; color: var(--ink); white-space: nowrap; }
.stat-num { margin-left: auto; text-align: right; }
.stat-label { font: 500 12.5px/1 var(--font); color: var(--ink-2); }
.zero .stat-num { color: var(--ink-3); }
</style>
