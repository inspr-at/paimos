<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { previewRecurrence, recurrenceHistory } from '../../lib/api'
import { useIdentityScope } from '../../lib/useIdentityScope'
import { changeWords, reasonWords, recurrenceName, recurrenceZone, renderTitle, triggerWords, when, type HistoryFilter, type Recurrence, type RecurrenceHistoryEntry, type RecurrenceResult } from '../../lib/recurrences'
import AppIcon, { type IconName } from '../AppIcon.vue'
const props = defineProps<{ item: Recurrence; projectKey: string; mayManage: boolean; busy: boolean }>()
const emit = defineEmits<{ toggle: []; run: []; edit: [] }>()
const scope = useIdentityScope(), historyLane = scope.lane(), previewLane = scope.lane()
const filter = ref<HistoryFilter>('all'), entries = ref<RecurrenceHistoryEntry[]>([]), cursor = ref<number | null>(null), times = ref<string[]>([]), loading = ref(false), failure = ref(''), previewFailure = ref('')
const filters: [HistoryFilter, string][] = [['all', 'All'], ['created', 'Created'], ['skipped', 'Skipped'], ['changes', 'Changes']]
const zone = computed(() => recurrenceZone(props.item.trigger))
const icons: Record<string, IconName> = { 'recurrence.occurred': 'check', 'recurrence.skipped': 'not', 'recurrence.paused': 'pause', 'recurrence.resumed': 'play', 'recurrence.updated': 'edit', 'recurrence.created': 'repeat' }
function load(older = false) {
  const id = props.item.id, selectedFilter = filter.value, before = older ? cursor.value || undefined : undefined
  loading.value = true; failure.value = ''
  void historyLane.run(({ after, signal }) => after(recurrenceHistory(id, selectedFilter, before, signal), page => { entries.value = older ? [...entries.value, ...page.items] : page.items; cursor.value = page.next_cursor }), { failed: error => { failure.value = error instanceof Error ? error.message : 'History unavailable.' }, settled: () => { loading.value = false } })
}
watch(() => [props.item.id, props.item.revision, props.item.occurrence_count], () => {
  load(); previewFailure.value = ''; times.value = []
  const id = props.item.id
  void previewLane.run(({ after, signal }) => after(previewRecurrence(id, signal), page => { times.value = page.times }), { failed: error => { previewFailure.value = error instanceof Error ? error.message : 'Preview unavailable.' } })
}, { immediate: true })
watch(() => props.item.id, () => { entries.value = []; cursor.value = null; filter.value = 'all' }, { flush: 'sync' })
watch(filter, () => load())
function occurrence(entry: RecurrenceHistoryEntry): RecurrenceResult | null { return entry.after && 'outcome' in entry.after ? entry.after : null }
function title(entry: RecurrenceHistoryEntry) {
  const action = entry.type.slice('recurrence.'.length)
  return action === 'occurred' ? 'Created' : action === 'skipped' ? 'Skipped' : `${{ created: 'Set up', updated: 'Changed', paused: 'Paused', resumed: 'Resumed' }[action] || action} by ${entry.actor_name}`
}
</script>
<template>
  <section class="recurrence-history glass-card" aria-label="Recurrence history">
    <header class="history-head"><p class="eyebrow">Recurring work</p><h2>{{ recurrenceName(item) }}</h2><p>{{ triggerWords(item.trigger) }}</p><div class="history-actions"><button type="button" class="btn sm toggle" :disabled="!mayManage || busy" @click="emit('toggle')"><AppIcon :name="item.paused ? 'play' : 'pause'" :size="12" />{{ item.paused ? 'Resume' : 'Pause' }}</button><button type="button" class="btn sm" :disabled="!mayManage || busy" @click="emit('run')">Run now</button><button type="button" class="btn sm ghost" :disabled="!mayManage || busy" @click="emit('edit')"><AppIcon name="edit" :size="13" />Edit…</button></div></header>
    <div class="history-grid"><div class="history-section"><p class="eyebrow">Upcoming</p><p v-if="item.paused" class="empty">Paused · Resume to continue</p><p v-else-if="item.trigger.kind === 'event'" class="empty">After the next release</p><p v-else-if="previewFailure" class="error" role="alert">{{ previewFailure }}</p><ol v-else class="upcoming"><li v-for="(at, index) in times" :key="at"><span>{{ when(at, zone) }}</span><span class="what">{{ index === 0 && item.open_previous && item.overlap_policy === 'skip' ? `Skipped if #${item.open_previous.number} is still open` : renderTitle(item.template.title, item.occurrence_count + index + 1, at, zone) }}</span></li></ol></div>
      <div class="history-section"><div class="history-filter"><p class="eyebrow">History</p><div class="seg" role="radiogroup" aria-label="Show history"><button v-for="[value, label] in filters" :key="value" type="button" role="radio" :aria-checked="filter === value" @click="filter = value">{{ label }}</button></div></div>
        <p v-if="failure" class="error" role="alert">{{ failure }} <button type="button" class="btn sm ghost" @click="load()">Retry</button></p><p v-else-if="loading && !entries.length" class="empty" role="status">Loading history…</p><p v-else-if="!entries.length" class="empty">Nothing {{ filter === 'created' ? 'created' : filter === 'skipped' ? 'skipped' : 'changed' }} yet.</p>
        <ol class="timeline"><li v-for="entry in entries" :key="entry.id"><span class="dot" :class="{ created: entry.type === 'recurrence.occurred' }"><AppIcon :name="icons[entry.type] || 'history'" :size="12" /></span><div><p class="entry-title">{{ title(entry) }} <RouterLink v-if="entry.node" :to="`/p/${encodeURIComponent(projectKey)}/${encodeURIComponent(entry.node.key)}`" class="ticket-key">{{ entry.node.key }}</RouterLink><span v-if="entry.node"> {{ entry.node.title }}</span><span v-else-if="occurrence(entry)?.node_id"> · ticket no longer available</span></p><p class="entry-detail">{{ when(entry.at, zone) }}<template v-if="occurrence(entry)"> · #{{ occurrence(entry)!.number }}<template v-if="occurrence(entry)!.occurrence_key.startsWith('manual:')"> · Run now</template><template v-if="entry.node"> · {{ entry.node.state }}</template><template v-if="occurrence(entry)!.reason"> · {{ reasonWords(occurrence(entry)!.reason) }}</template></template><template v-else-if="entry.type === 'recurrence.updated' && entry.after && 'template' in entry.after"> · {{ changeWords(entry.before, entry.after) }}</template></p></div></li></ol>
        <button v-if="cursor" type="button" class="btn sm ghost" :disabled="loading" @click="load(true)">Older history</button>
      </div>
    </div>
  </section>
</template>
<style scoped>
.history-head { padding: 16px 18px 12px; border-bottom: 1px solid var(--line); }.history-head h2 { font: 600 15px/1.35 var(--font); }.history-head > p:last-of-type { margin-top: 2px; font-size: 12.5px; color: var(--ink-3); }.history-actions { display: flex; gap: 6px; margin-top: 12px; }.toggle { width: 92px; padding: 0; }
.history-grid { display: grid; grid-template-columns: minmax(0, 1fr) minmax(0, 1.5fr); gap: 0 28px; }.history-section { min-width: 0; padding: 12px 18px; }.history-filter { display: flex; align-items: center; flex-wrap: wrap; justify-content: space-between; gap: 10px; min-height: 32px; }.history-filter .seg button { height: 24px; padding: 0 9px; font-size: 12px; }.upcoming { padding: 0; list-style: none; margin-top: 4px; }.upcoming li { display: flex; gap: 12px; justify-content: space-between; align-items: center; min-height: 30px; border-top: 1px solid var(--line); font-size: 12.5px; }.upcoming li:first-child { border: 0; }.upcoming li > span:first-child { flex: none; font-variant-numeric: tabular-nums; }.what { overflow: hidden; white-space: nowrap; text-overflow: ellipsis; color: var(--ink-3); }.empty { padding: 10px 0; font-size: 12.5px; color: var(--ink-3); }.timeline { list-style: none; padding: 0; margin: 6px 0 0; }.timeline li { display: grid; grid-template-columns: 22px minmax(0, 1fr); gap: 10px; padding: 8px 0; }.dot { width: 22px; height: 22px; display: grid; place-items: center; border-radius: 50%; color: var(--ink-3); background: var(--surface-raised); box-shadow: inset 0 0 0 1px var(--line-2); }.dot.created { color: var(--teal-ink); background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); }.entry-title { color: var(--ink); font-size: 13px; overflow-wrap: anywhere; }.ticket-key { font: 600 11.5px var(--mono); }.entry-detail { color: var(--ink-3); font-size: 12px; margin-top: 1px; overflow-wrap: anywhere; }
@media (max-width: 860px) { .history-grid { grid-template-columns: minmax(0, 1fr); } }
</style>
