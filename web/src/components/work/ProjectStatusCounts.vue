<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import type { ProjectSummary } from '../../lib/api'
import type { HeaderDensity } from '../../lib/projectHeader'
import { groupTotal, HEADER_STATUS_GROUPS, statusCount, statusIsHidden, type HeaderStatusGroup } from '../../lib/projectStatusCounts'
import type { ListFilters } from '../../lib/ticketList'
import { statusMeta } from '../../lib/work'
import StatusIcon from './StatusIcon.vue'

const props = defineProps<{ summary: ProjectSummary; filters: ListFilters; density: HeaderDensity }>()
const emit = defineEmits<{ group: [group: HeaderStatusGroup]; status: [state: string] }>()
const unavailable = computed(() => !props.summary.status_counts || props.summary.status_counts_truncated)
const pressedGroup = (group: HeaderStatusGroup) => props.filters.status.length > 0 && props.filters.statusScope === group.id
const pressedStatus = (state: string) => props.filters.statusScope === 'canonical' && props.filters.status.length === 1 && props.filters.status[0] === state
const hidden = (state: string) => !props.filters.showClosed && statusIsHidden(state, props.summary.status_counts)
const number = (count: number) => count.toLocaleString('en-GB')
// Choose the numeric slots when this project opens, not on every live count.
// One spare digit accommodates growth without moving controls under the pointer.
const digits = ref(2)
watch(() => props.summary.id, () => {
  const largest = Math.max(...HEADER_STATUS_GROUPS.map(group => groupTotal(props.summary, group.id)), ...(props.summary.status_counts ?? []).map(item => item.count), 0)
  digits.value = Math.max(2, number(largest).length + 1)
}, { immediate: true })
</script>

<template>
  <div class="project-status-counts" :class="{ comfortable: density === 'comfortable' }" :style="{ '--count-digits': `${digits}ch` }" role="group" aria-label="Filter tickets by status">
    <div class="group-counts">
      <div v-for="group in HEADER_STATUS_GROUPS" :key="group.id" class="status-group" :class="{ quiet: group.id === 'closed' }" :data-group="group.id">
        <button type="button" class="group-count" :aria-pressed="pressedGroup(group)" :aria-label="`Filter ${group.label.toLowerCase()} tickets: ${number(groupTotal(summary, group.id))}`" :data-tip="`Show only ${group.label.toLowerCase()} tickets; click again to clear`" @click="emit('group', group)">
          <StatusIcon :state="group.icon" :size="11" /><b>{{ number(groupTotal(summary, group.id)) }}</b><span>{{ group.label.toLowerCase() }}</span>
        </button>
        <div class="status-items">
          <button v-for="state in group.states" :key="state" type="button" class="status-count" :class="{ 'is-hidden': hidden(state) }" :data-state="state" :aria-pressed="pressedStatus(state)" :aria-label="`Filter ${statusMeta(state).label} tickets: ${statusCount(summary, state) === null ? 'count unavailable' : number(statusCount(summary, state)!)}${hidden(state) ? '. Hidden by Hide; click to show' : ''}`" :data-tip="hidden(state) ? 'Hidden by Hide; click to show' : `Show ${statusMeta(state).label}; click again to clear`" @click="emit('status', state)">
            <StatusIcon :state="state" :size="11" /><span>{{ statusMeta(state).label }}</span><b>{{ statusCount(summary, state) === null ? '—' : number(statusCount(summary, state)!) }}</b>
          </button>
        </div>
      </div>
    </div>
    <p v-if="unavailable" class="counts-note">{{ summary.status_counts_truncated ? 'Status counts are partial; group totals are complete.' : 'Status counts unavailable.' }}</p>
  </div>
</template>

<style scoped>
.project-status-counts { min-width: 0; }
.group-counts { display: flex; align-items: start; gap: 10px; }
.status-group { min-width: 0; }
.status-group.quiet { display: none; }
.group-count, .status-count { display: inline-flex; align-items: center; gap: 5px; border: 0; background: transparent; color: var(--ink-2); white-space: nowrap; border-radius: 6px; }
.group-count { min-height: 28px; padding: 0 4px; font-size: 12px; }
.group-count b { inline-size: var(--count-digits); text-align: right; color: var(--ink); font: 600 12px/1 var(--mono); font-variant-numeric: tabular-nums; }
.group-count:hover, .status-count:hover { background: var(--row-hover); color: var(--ink); }
button[aria-pressed="true"] { background: var(--row-selected); color: var(--teal-ink); box-shadow: inset 0 0 0 1px var(--chip-teal-line); }
button:focus-visible { outline: 2px solid var(--teal); outline-offset: 1px; }
.status-items, .counts-note { display: none; }
.comfortable .group-counts { display: grid; grid-template-columns: repeat(4, max-content); column-gap: clamp(8px, 1.4vw, 24px); }
.comfortable .status-group { display: grid; align-content: start; gap: 2px; }
.comfortable .status-group.quiet { opacity: .8; }
.comfortable .status-items { display: grid; grid-template-columns: repeat(2, max-content); gap: 0 6px; }
.status-count { min-height: 24px; padding: 0 4px; font-size: 11px; }
.status-count b { margin-left: auto; inline-size: var(--count-digits); text-align: right; font: 500 11px/1 var(--mono); font-variant-numeric: tabular-nums; }
.status-count.is-hidden { opacity: .55; }
.status-count.is-hidden:hover { opacity: 1; }
.comfortable .counts-note { display: block; margin: 0; color: var(--ink-3); font-size: 11px; }
@media (min-width: 601px) and (max-width: 1100px) {
  /* Keep project names readable alongside the grid on smaller desktops.
     Every group retains its two status sub-columns. */
  .comfortable .group-counts { grid-template-columns: repeat(2, max-content); row-gap: 8px; }
  .comfortable .group-counts { column-gap: 8px; }
  .comfortable .status-items { column-gap: 3px; }
  .status-count { padding-inline: 1px; gap: 3px; }
}
@media (max-width: 600px) {
  .comfortable .group-counts { display: flex; gap: 10px; }
  .comfortable .status-items, .comfortable .status-group.quiet, .comfortable .counts-note { display: none; }
}
@media (pointer: coarse) {
  .group-count, .status-count { min-height: 44px; }
}
</style>
