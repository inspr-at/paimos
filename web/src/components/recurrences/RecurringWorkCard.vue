<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { deleteRecurrence, getRecurrence, listRecurrences, pauseRecurrence, runRecurrence } from '../../lib/api'
import { can, onAccessChange } from '../../lib/authz'
import { confirmAction } from '../../lib/confirm'
import { toast } from '../../lib/toast'
import { useIdentityScope } from '../../lib/useIdentityScope'
import { usePoller } from '../../lib/usePolledData'
import { recurrenceName, recurrenceZone, reasonWords, triggerWords, when, type Recurrence, type RecurrenceResult } from '../../lib/recurrences'
import AppIcon from '../AppIcon.vue'
import KeyCap from '../KeyCap.vue'
import FloatingPanel from '../work/FloatingPanel.vue'
import RecurrenceEditor from './RecurrenceEditor.vue'
import RecurrenceHistory from './RecurrenceHistory.vue'
import RecurrenceRun from './RecurrenceRun.vue'

const props = defineProps<{ project: { id: string; routeKey: string; title?: string }; selectedId?: string }>()
const items = ref<Recurrence[]>([]), cursor = ref<string | null>(null), selected = ref(''), failure = ref(''), loading = ref(false), busy = ref<string[]>([])
const editor = ref<{ item?: Recurrence } | null>(null), runPrompt = ref<Recurrence | null>(null), menu = ref<{ item: Recurrence; anchor: HTMLElement } | null>(null)
const mayManage = computed(() => can('recurrences.manage', props.project.id)), scope = useIdentityScope(() => can('nodes.read', props.project.id)), reads = scope.lane()
const chosen = computed(() => items.value.find(item => item.id === selected.value))
const readOnlyReason = 'Needs the Manage recurring work permission'
let epoch = 0, alive = true
function clear() { scope.reset(); epoch++; items.value = []; cursor.value = null; selected.value = ''; menu.value = null; editor.value = null; runPrompt.value = null; failure.value = ''; busy.value = []; loading.value = false }
const stopAccess = onAccessChange(change => { if (change === 'reset') clear() })
function load(older = false) {
  if (!scope.owner.value || busy.value.length || editor.value || runPrompt.value || menu.value) return
  const projectId = props.project.id, afterCursor = older ? cursor.value || undefined : undefined
  loading.value = true; failure.value = ''
  void reads.run(({ after, signal }) => after(listRecurrences(projectId, afterCursor, signal), page => {
    items.value = older ? [...items.value, ...page.items.filter(row => !items.value.some(known => known.id === row.id))] : page.items
    cursor.value = page.next_cursor
    if (!items.value.some(item => item.id === selected.value)) selected.value = items.value.find(item => item.id === props.selectedId)?.id || items.value[0]?.id || ''
  }), { failed: error => { failure.value = error instanceof Error ? error.message : 'Recurring work could not be loaded.' }, settled: () => { loading.value = false } })
}
watch([() => props.project.id, () => scope.owner.value], () => { clear(); load() }, { immediate: true, flush: 'sync' })
watch(() => props.selectedId, id => { if (id && items.value.some(item => item.id === id)) selected.value = id })
watch(mayManage, value => { if (!value) { editor.value = null; runPrompt.value = null; menu.value = null } })
const poller = usePoller(() => { if (!cursor.value) load() }, 20_000)
onMounted(() => poller.start())
onBeforeUnmount(() => { alive = false; epoch++; stopAccess(); poller.stop() })
function current(item: Recurrence) { return alive && mayManage.value && item.project_id === props.project.id && items.value.some(row => row.id === item.id && row.revision === item.revision) }
function replace(item: Recurrence) { const index = items.value.findIndex(row => row.id === item.id), known = items.value[index]; if (known && item.revision >= known.revision && item.occurrence_count >= known.occurrence_count) items.value[index] = { ...known, ...item } }
function closeMenu(restore = true) { const anchor = menu.value?.anchor; menu.value = null; if (restore) anchor?.focus() }
function saved(item: Recurrence) { editor.value = null; if (items.value.some(row => row.id === item.id)) replace(item); else items.value.push(item); selected.value = item.id; toast(`Saved ${recurrenceName(item)}`) }
function refreshItem(id: string) { void scope.run(({ after, signal }) => after(getRecurrence(id, signal), item => { replace(item) }), { failed: error => { toast(error instanceof Error ? error.message : 'The recurrence could not be refreshed.', { tone: 'error' }) } }) }
function toggle(item: Recurrence, undo = false) {
  if (!current(item) || busy.value.includes(item.id)) return
  const owner = scope.owner.value, generation = epoch, projectId = props.project.id, paused = !item.paused
  reads.cancel(); busy.value = [...busy.value, item.id]
  void scope.run(({ after, signal }) => after(pauseRecurrence(item.id, paused, item.revision, signal), updated => {
    replace(updated)
    toast(`${paused ? 'Paused' : 'Resumed'} ${recurrenceName(item)}.`, { timeout: 8000, action: undo ? undefined : { label: 'Undo', run: () => {
      if (!alive || owner !== scope.owner.value || generation !== epoch || projectId !== props.project.id || !current(updated)) { toast('The recurrence changed since, so nothing was undone.', { tone: 'error' }); return }
      toggle(updated, true)
    } } })
  }), { failed: error => { toast(error instanceof Error ? error.message : 'Pause or resume failed.', { tone: 'error' }) }, settled: () => { busy.value = busy.value.filter(id => id !== item.id) } })
}
function run(item: Recurrence) {
  if (!current(item) || busy.value.includes(item.id)) return
  reads.cancel(); busy.value = [...busy.value, item.id]
  void scope.run(({ after, signal }) => after(getRecurrence(item.id, signal), latest => {
    if (!items.value.some(row => row.id === latest.id)) return
    replace(latest)
    if (latest.trigger.kind === 'event' || (latest.open_previous && latest.overlap_policy === 'skip')) { runPrompt.value = latest; return }
    return after(runRecurrence(latest.id, { idempotency_key: crypto.randomUUID(), expected_revision: latest.revision }, signal), ran)
  }), { failed: error => { toast(error instanceof Error ? error.message : 'Run now failed.', { tone: 'error' }) }, settled: () => { busy.value = busy.value.filter(id => id !== item.id) } })
}
function ran(result: RecurrenceResult) {
  runPrompt.value = null
  toast(result.outcome === 'created' ? `Created occurrence #${result.number}.` : `Skipped: ${reasonWords(result.reason)}`)
  refreshItem(result.recurrence_id)
}
function remove(item: Recurrence) {
  closeMenu(false)
  if (!current(item)) return
  void scope.run(({ after, signal }) => after(confirmAction({ title: `Delete ${recurrenceName(item)}?`, body: `No more tickets are created. Its existing tickets, receipts and provenance stay.`, confirmLabel: 'Delete', danger: true }), confirmed => {
    if (!confirmed || !current(item)) return
    reads.cancel(); busy.value = [...busy.value, item.id]
    return after(deleteRecurrence(item.id, item.revision, signal), () => { items.value = items.value.filter(row => row.id !== item.id); if (selected.value === item.id) selected.value = items.value[0]?.id || ''; toast(`Deleted ${recurrenceName(item)}.`) })
  }), { failed: error => { toast(error instanceof Error ? error.message : 'The recurrence was not deleted.', { tone: 'error' }) }, settled: () => { busy.value = busy.value.filter(id => id !== item.id) } })
}
function copyCLI(item: Recurrence) {
  closeMenu(); const generation = epoch
  void scope.run(({ after }) => after(navigator.clipboard.writeText(`aeon recur get ${item.id}`), () => { if (generation === epoch) toast('Copied CLI command.') }), { failed: () => toast('The clipboard is unavailable.', { tone: 'error' }) })
}
function rowKeys(event: KeyboardEvent, item: Recurrence) {
  if (event.metaKey || event.ctrlKey || event.altKey || (event.target as HTMLElement).closest('input, textarea, select, [contenteditable="true"]')) return
  const at = items.value.findIndex(row => row.id === item.id)
  if (['ArrowDown', 'j', 'ArrowUp', 'k'].includes(event.key)) {
    event.preventDefault(); const next = items.value[at + (['ArrowDown', 'j'].includes(event.key) ? 1 : -1)]
    if (next) { selected.value = next.id; document.getElementById(`recurrence-${next.id}`)?.focus({ preventScroll: true }) }
  } else if (event.key === 'p' && mayManage.value) { event.preventDefault(); toggle(item) }
  else if (event.key === 'n' && mayManage.value) { event.preventDefault(); run(item) }
  else if (event.key === 'e' && mayManage.value) { event.preventDefault(); editor.value = { item } }
  else if (event.key === 'Enter' && event.target === event.currentTarget) { event.preventDefault(); selected.value = item.id }
}
function menuKeys(event: KeyboardEvent) { if (!['ArrowUp', 'ArrowDown'].includes(event.key)) return; const buttons = [...(event.currentTarget as HTMLElement).querySelectorAll<HTMLButtonElement>('button:not(:disabled)')], at = buttons.indexOf(event.target as HTMLButtonElement); event.preventDefault(); buttons[(at + (event.key === 'ArrowDown' ? 1 : -1) + buttons.length) % buttons.length]?.focus() }
</script>
<template>
  <div class="recurring-layout">
    <section id="recurring-work" class="settings-card glass-card" aria-labelledby="recurring-work-title">
      <header class="card-head"><span class="card-icon"><AppIcon name="repeat" :size="16" /></span><div class="card-titles"><h2 id="recurring-work-title">Recurring work</h2><p>Tickets that create themselves, on a schedule or after a release is published.</p></div><div class="card-aside"><button type="button" class="btn sm" :disabled="!mayManage" :data-tip="mayManage ? 'New recurring work' : readOnlyReason" @click="editor = {}"><AppIcon name="plus" :size="13" />New…</button></div></header>
      <p v-if="!mayManage" class="permission-note" role="note">You can read recurring work. {{ readOnlyReason }}.</p>
      <div class="card-body"><p v-if="failure" class="error" role="alert">{{ failure }} <button type="button" class="btn sm" @click="load()">Retry</button></p><p v-if="loading && !items.length" class="empty" role="status">Loading recurring work…</p><p v-else-if="!items.length && !failure" class="empty">No recurring work yet. Use New…, or Repeat… on a ticket.</p>
        <div v-if="items.length" class="recurrence-list" role="table" aria-label="Recurring work">
          <div class="list-head" role="row"><span role="columnheader">Name</span><span role="columnheader">Trigger</span><span role="columnheader">Next run</span><span role="columnheader">Last result</span><span role="columnheader">Actions</span></div>
          <div v-for="item in items" :id="`recurrence-${item.id}`" :key="item.id" class="recurrence-row" :class="{ selected: selected === item.id, paused: item.paused }" role="row" :tabindex="selected === item.id ? 0 : -1" :aria-label="recurrenceName(item)" @click="selected = item.id" @keydown.stop="rowKeys($event, item)">
            <div class="cell name" role="cell"><b>{{ recurrenceName(item) }}</b><small>{{ item.template.type === 'task' ? 'Task' : 'Ticket' }} {{ item.parent_id === project.id ? 'at top level' : 'in an epic' }}{{ item.queue_each ? ' · queued' : '' }}</small></div>
            <div class="cell trigger" role="cell"><span>{{ triggerWords(item.trigger) }}</span><small>{{ item.overlap_policy === 'skip' ? 'skips while the last is open' : 'creates even while open' }}</small></div>
            <div class="cell next" role="cell"><span><template v-if="item.paused"><AppIcon name="pause" :size="12" />Paused</template><template v-else>{{ item.trigger.kind === 'event' ? 'After the next release' : item.next_at ? when(item.next_at, recurrenceZone(item.trigger)) : 'No next date' }}</template></span><small>{{ item.paused ? item.trigger.kind === 'event' ? 'releases are ignored' : item.next_at ? `would be ${when(item.next_at, recurrenceZone(item.trigger))}` : '' : item.open_previous && item.overlap_policy === 'skip' ? `skipped if #${item.open_previous.number} is still open` : item.queue_each ? 'into the work queue' : '' }}</small></div>
            <div class="cell last" role="cell"><span v-if="item.last_result"><RouterLink v-if="item.last_result.key" :to="`/p/${encodeURIComponent(project.routeKey)}/${encodeURIComponent(item.last_result.key)}`" @click.stop>{{ item.last_result.key }}</RouterLink><template v-else>{{ item.last_result.outcome === 'skipped' ? 'Skipped' : 'Ticket unavailable' }}</template><span class="number">#{{ item.last_result.number }}</span></span><span v-else>Nothing yet</span><small>{{ item.last_result ? `${when(item.last_result.scheduled_at, recurrenceZone(item.trigger))} · ${item.last_result.outcome === 'skipped' ? reasonWords(item.last_result.reason) : item.last_result.state || 'created'}` : '' }}</small></div>
            <div class="cell row-actions" role="cell"><button type="button" class="btn sm toggle" :disabled="!mayManage || busy.includes(item.id)" :data-tip="mayManage ? item.paused ? 'Resume · p' : 'Pause · p' : readOnlyReason" @click.stop="toggle(item)"><AppIcon :name="item.paused ? 'play' : 'pause'" :size="12" />{{ item.paused ? 'Resume' : 'Pause' }}</button><button type="button" class="btn sm run" :disabled="!mayManage || busy.includes(item.id)" :data-tip="mayManage ? 'Run now · n' : readOnlyReason" @click.stop="run(item)">Run now</button><button type="button" class="icon-btn sm flat" :aria-label="`More for ${recurrenceName(item)}`" aria-haspopup="menu" :aria-expanded="menu?.item.id === item.id" @click.stop="menu = { item, anchor: $event.currentTarget as HTMLElement }"><AppIcon name="more" :size="15" /></button></div>
          </div>
        </div>
        <button v-if="cursor" type="button" class="btn sm ghost more" :disabled="loading" @click="load(true)">More recurring work</button>
        <p class="list-hint"><KeyCap k="j" /><KeyCap k="k" /> move · <KeyCap k="p" /> pause / resume · <KeyCap k="n" /> run now · <KeyCap k="e" /> edit</p>
      </div>
    </section>
    <RecurrenceHistory v-if="chosen" :key="chosen.id" :item="chosen" :project-key="project.routeKey" :may-manage="mayManage" :busy="busy.includes(chosen.id)" @toggle="toggle(chosen!)" @run="run(chosen!)" @edit="editor = { item: chosen }" />
    <FloatingPanel v-if="menu" :anchor="menu.anchor" :width="236" align="end" :label="`More for ${recurrenceName(menu.item)}`" @close="closeMenu"><div class="row-menu" role="menu" @keydown="menuKeys"><button type="button" role="menuitem" :disabled="!mayManage || busy.includes(menu.item.id)" :data-tip="!mayManage ? readOnlyReason : undefined" @click="editor = { item: menu.item }; closeMenu(false)"><AppIcon name="edit" :size="14" />Edit…</button><button type="button" role="menuitem" @click="selected = menu.item.id; closeMenu()"><AppIcon name="history" :size="14" />History</button><button type="button" role="menuitem" @click="copyCLI(menu.item)"><AppIcon name="terminal" :size="14" />Copy CLI command</button><div class="separator" /><button type="button" role="menuitem" class="danger" :disabled="!mayManage || busy.includes(menu.item.id)" @click="remove(menu.item)"><AppIcon name="trash" :size="14" />Delete…</button></div></FloatingPanel>
    <RecurrenceEditor v-if="editor" :project="project" :recurrence="editor.item" @close="editor = null" @saved="saved" />
    <RecurrenceRun v-if="runPrompt" :key="runPrompt.id" :item="runPrompt" @close="runPrompt = null" @ran="ran" />
  </div>
</template>
<style scoped>
.recurring-layout { display: grid; gap: 16px; padding: 18px 0; }.settings-card { padding: 20px; }.card-head { display: flex; align-items: center; gap: 12px; }.card-icon { display: grid; place-items: center; flex: none; align-self: flex-start; width: 32px; height: 32px; border-radius: 10px; background: var(--chip-teal-bg); color: var(--teal-ink); box-shadow: inset 0 0 0 1px var(--chip-teal-line); }.card-titles { flex: 1; min-width: 0; }.card-titles h2 { font: 600 15px/1.35 var(--font); }.card-titles p { margin-top: 2px; font-size: 13px; }.card-aside { flex: none; }.card-body { margin-top: 14px; }.permission-note { margin-top: 12px; font-size: 12px; color: var(--ink-3); }.empty { padding: 24px 8px; font-size: 13px; color: var(--ink-3); }
.recurrence-list { display: grid; grid-template-columns: minmax(130px, 1.2fr) minmax(140px, 1.1fr) minmax(125px, .9fr) minmax(135px, 1fr) 208px; }.list-head, .recurrence-row { display: grid; grid-template-columns: subgrid; grid-column: 1/-1; align-items: center; }.list-head { height: 32px; border-top: 1px solid var(--line); border-bottom: 1px solid var(--line); font: 500 10.5px var(--mono); letter-spacing: .14em; text-transform: uppercase; color: var(--ink-3); }.list-head > span, .cell { padding: 0 8px; min-width: 0; }.list-head > span:last-child { text-align: right; }.recurrence-row { min-height: 60px; border-radius: 10px; margin: 2px -6px 0; padding: 0 6px; font-size: 13px; cursor: pointer; }.recurrence-row:hover { background: var(--row-hover); }.recurrence-row.selected { background: var(--row-selected); box-shadow: inset 0 0 0 1px var(--chip-teal-line); }.recurrence-row:focus-visible { box-shadow: var(--focus-ring); }.cell:not(.row-actions) { display: grid; line-height: 1.3; }.cell > b, .cell > span, .cell > small { white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }.cell b { font-weight: 600; }.cell small { height: 15px; font-size: 11.5px; color: var(--ink-3); }.next > span, .last > span { display: flex; align-items: center; gap: 5px; height: 17px; }.last a { font: 600 11.5px var(--mono); }.number { color: var(--ink-3); font-size: 11.5px; }.paused .name b { color: var(--ink-2); }.row-actions { display: flex; justify-content: flex-end; gap: 4px; }.toggle { width: 92px; padding: 0; }.run { width: 76px; padding: 0; }.list-hint { display: flex; align-items: center; gap: 4px; margin-top: 10px; font-size: 12px; color: var(--ink-3); }.more { margin-top: 10px; }.row-menu { display: grid; gap: 1px; }.row-menu button { display: flex; align-items: center; gap: 10px; width: 100%; min-height: 34px; padding: 0 10px; text-align: left; border: 0; border-radius: 8px; background: transparent; font-size: 13.5px; }.row-menu button:hover:not(:disabled) { background: var(--row-hover); }.row-menu .danger { color: var(--danger); }.separator { height: 1px; margin: 4px 6px; background: var(--line); }
@media (max-width: 1000px) { .recurrence-list { grid-template-columns: minmax(0, 1fr); }.list-head { display: none; }.recurrence-row { grid-template-columns: minmax(0, 1fr); gap: 2px; padding: 10px 6px; border-top: 1px solid var(--line); margin: 0 -6px; }.cell { padding: 0; }.name b { margin-bottom: 1px; }.trigger small { display: none; }.next, .last { padding-top: 4px; }.row-actions { justify-content: flex-start; margin-top: 8px; }.list-hint { display: none; } }
@media (max-width: 600px) { .settings-card { padding: 16px; }.card-head { flex-wrap: wrap; }.card-aside { flex-basis: 100%; padding-left: 44px; } }
</style>
