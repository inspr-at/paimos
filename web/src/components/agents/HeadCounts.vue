<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import { useAgentAppearance } from '../../lib/agentAppearance'
import type { SessionView } from '../../stores/agents'
import AgentStateMark from '../indicators/AgentStateMark.vue'
import { headCounts, type HeadFilter } from './headCounts'

// The Agents head's counts (AEON-780, replaces the live line): each one filters
// Sessions to its state. A zero stays as a faint, disabled count; a problem is
// a full tint with a hairline ring, never an edge bar.
const props = defineProps<{ views: SessionView[]; now: number; loaded: boolean; filter: HeadFilter | null }>()
const emit = defineEmits<{ filter: [filter: HeadFilter] }>()
const { appearance } = useAgentAppearance()
const counts = computed(() => headCounts(props.views, props.now))
function pick(filter: HeadFilter, n: number) {
  // A pressed count always turns off, even after its sessions left the state.
  if (n || props.filter === filter) emit('filter', filter)
}
</script>

<template>
  <div class="head-counts" role="group" aria-label="Show sessions by state">
    <template v-if="!loaded"><span class="skeleton counts-skeleton" aria-hidden="true" /><span class="sr-only">Loading</span></template>
    <template v-else>
      <button
        v-for="c in counts" :key="c.filter" type="button" class="count" :class="{ zero: !c.n, hot: c.filter === 'problem' && c.n, ask: c.filter === 'waiting' && c.n }"
        :data-filter="c.filter" :style="appearance(c.mark)" :aria-pressed="filter === c.filter" :aria-disabled="!c.n && filter !== c.filter ? 'true' : undefined"
        :aria-label="`${c.n} ${c.label}. ${filter === c.filter ? 'Show all sessions' : c.tip}`" :data-tip="filter === c.filter ? 'Show all sessions' : c.tip" @click="pick(c.filter, c.n)"
      ><AgentStateMark :state="c.mark" :size="14" /><b>{{ c.n }}</b><span class="label">{{ c.label }}</span></button>
    </template>
    <slot />
  </div>
</template>

<style scoped>
.head-counts { display: flex; align-items: center; flex-wrap: wrap; gap: 4px; min-width: 0; min-height: 32px; }
.counts-skeleton { display: inline-block; width: 260px; height: 12px; }
.count { display: inline-flex; align-items: center; gap: 7px; height: 30px; padding: 0 9px; border: 0; border-radius: 999px; background: transparent; color: var(--ink-2); font-size: 13px; font-weight: 550; font-variant-numeric: tabular-nums; white-space: nowrap; }
.count b { color: var(--ink); font-weight: 650; font-size: 13.5px; }
@media (hover: hover) { .count:hover { background: var(--row-hover); color: var(--ink); } }
.count.zero { color: var(--ink-3); }
.count.zero b { color: var(--ink-3); font-weight: 500; }
.count.zero .agent-state-mark { opacity: .55; }
.count[aria-disabled="true"] { cursor: default; }
/* Problem and Asks you: a full soft tint with a hairline ring. */
.count.hot { background: color-mix(in srgb, var(--agent-state-color) 9%, transparent); box-shadow: inset 0 0 0 1px color-mix(in srgb, var(--agent-state-color) 30%, transparent); color: var(--ink); }
.count.ask { background: var(--queue-wait-bg); box-shadow: inset 0 0 0 1px var(--queue-wait-line); color: var(--ink); }
.count[aria-pressed="true"] { background: var(--row-selected); box-shadow: inset 0 0 0 1px var(--chip-teal-line); color: var(--teal-ink); }
.count.hot[aria-pressed="true"] { background: color-mix(in srgb, var(--agent-state-color) 9%, transparent); box-shadow: inset 0 0 0 1.5px var(--agent-state-color); color: var(--ink); }
.count:focus-visible, .count[aria-pressed="true"]:focus-visible { box-shadow: var(--focus-ring); }
@media (max-width: 720px) {
  /* Line 2 of the head: 44 px counts, zeros hidden unless one is the active filter. */
  .head-counts { min-height: 44px; gap: 0 4px; }
  .count { height: 44px; padding: 0 10px; }
  .count.zero:not([aria-pressed="true"]) { display: none; }
}
</style>
