<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { hiddenCopy, planningListItem, type MatchCounts, type PlanningItem, type PlanningSource } from '../../lib/deliveryPlanning'
import TicketTable from '../work/TicketTable.vue'
const props = defineProps<{ source: PlanningSource; items: PlanningItem[]; matches?: MatchCounts; loaded: boolean; loading: boolean; error: string; more: boolean; incomplete: boolean; projectId: string; projectKey: string; now: number; query: string; scrollRoot: HTMLElement | null; density: 'compact' | 'comfortable' }>()
const emit = defineEmits<{ open: [key: string]; copy: [key: string]; newTab: [key: string] }>()
const route = useRoute(), router = useRouter()
const rowHref = (row: { key: string }) => router.resolve({ path: `/p/${encodeURIComponent(props.projectKey)}/${encodeURIComponent(row.key)}`, query: { ...route.query, section: 'releases' } }).href
const rows = computed(() => props.items.map(planningListItem))
const rowsById = computed(() => new Map(rows.value.map(row => [row.id, row])))
const groups = computed(() => [{ key: 'all', label: '', rows: rows.value, total: rows.value.length }])
const cursor = computed(() => props.items[0]?.item_id ?? null)
const summary = computed(() => props.error ? `Work could not be loaded: ${props.error}` : props.loading && !props.loaded ? 'Loading work…' : !props.loaded ? 'Work is waiting to load' : props.items.length
  ? `${props.items.length} of ${props.matches?.incomplete ? 'at least ' : ''}${props.matches?.shown_count ?? props.items.length} matching work loaded${props.more ? ' · more available' : ''}` : props.matches?.hidden_count ? 'Matching work is hidden' : 'No matching work')
</script>
<template>
  <div class="planning-work" :data-work-source="source">
    <TicketTable v-if="rows.length" embedded :row-href="rowHref" :groups="groups" group="none" :rows-by-id="rowsById" :cursor-id="cursor" :open-id="null" :query="query" :sort="[]" :density="density"
      :loading="false" :loading-more="false" error="" more-error="" :has-more="false" :filtered="false" :hiding-closed="false" :collapsed="new Set()" :total="null"
      :project-key="projectKey" :project-id="projectId" :scroll-root="scrollRoot" :now="now" :show-assignee="true" :creating="false" :known-states="[]" :create="async () => false"
      @open="row => emit('open', row.key)" @copy="row => emit('copy', row.key)" @new-tab="row => emit('newTab', row.key)" />
    <span class="insertion" :data-insertion="source" aria-hidden="true" />
    <p class="work-summary" :class="{ error }" :role="error ? 'alert' : 'status'" :data-tip="summary">{{ summary }}</p>
    <p v-if="hiddenCopy(matches)" class="hidden-count">{{ hiddenCopy(matches) }}</p>
    <p v-if="incomplete || matches?.incomplete" class="partial-count">Counts are a lower bound; more matching work may exist.</p>
  </div>
</template>
<style scoped>
.planning-work { min-width: 0; }
.insertion { display: block; height: 0; }
.work-summary { margin: 0; padding: 0 12px 0 44px; height: 36px; line-height: 36px; overflow: hidden; white-space: nowrap; text-overflow: ellipsis; border-bottom: 1px solid var(--line); font-size: 12px; color: var(--ink-3); }
.work-summary.error { color: var(--danger); }
.hidden-count, .partial-count { margin: 0; padding: 8px 12px 8px 44px; font-size: 12px; color: var(--ink-3); border-bottom: 1px solid var(--line); }
@media (max-width: 720px) { .work-summary, .hidden-count, .partial-count { padding-left: 28px; } }
</style>
