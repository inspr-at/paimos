<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { deleteRecurrence, getRecurrence, getRoutineExecutionSettings, listRoutines, pauseRecurrence, putRoutineExecutionSettings, runRecurrence } from '../../lib/api'
import { can, myProjectRole, myWorkspaceRole, onAccessChange } from '../../lib/authz'
import { confirmAction } from '../../lib/confirm'
import { toast } from '../../lib/toast'
import { useIdentityScope } from '../../lib/useIdentityScope'
import { usePoller } from '../../lib/usePolledData'
import { ROUTINE_STATE_WORDS, effortVerb, reasonWords, recurrenceName, recurrenceZone, routineScope, routineState, triggerWords, waitReasonWords, when, type Recurrence, type RecurrenceResult, type RoutineExecutionSettings, type RoutineScopeKind, type RoutineState } from '../../lib/recurrences'
import { useProjects } from '../../stores/projects'
import { useSession } from '../../stores/session'
import AppIcon from '../AppIcon.vue'
import KeyCap from '../KeyCap.vue'
import FloatingPanel from '../work/FloatingPanel.vue'
import RecurrenceEditor from './RecurrenceEditor.vue'
import RecurrenceHistory from './RecurrenceHistory.vue'
import RecurrenceRun from './RecurrenceRun.vue'

// The Routines box (AEON-1182; was "Recurring work"): a project's routines, your own
// (Settings › Personal) or the workspace's (Settings › Routines). One list, one editor.
const props = withDefaults(defineProps<{ project?: { id: string; routeKey: string; title?: string } | null; scope?: RoutineScopeKind; selectedId?: string; consent?: boolean }>(), { project: null, scope: 'project', selectedId: undefined, consent: false })
const session = useSession(), projects = useProjects()
const kind = computed<RoutineScopeKind>(() => props.scope ?? 'project')
const items = ref<Recurrence[]>([]), cursor = ref<string | null>(null), selected = ref(''), failure = ref(''), loading = ref(false), linked = ref<Recurrence | null>(null), linkFailure = ref(''), busy = ref<string[]>([])
const editor = ref<{ item?: Recurrence; consent?: boolean } | null>(null), runPrompt = ref<Recurrence | null>(null), menu = ref<{ item: Recurrence; anchor: HTMLElement } | null>(null), revoked = ref<'' | 'draft' | 'saved'>('')
const person = computed(() => !!session.identity && session.identity.principal.kind !== 'agent')
const workspaceAdmin = computed(() => ['owner', 'admin'].includes(myWorkspaceRole()?.key ?? ''))
const projectId = computed(() => props.project?.id ?? '')
const mayManage = computed(() => kind.value === 'project' ? can('recurrences.manage', projectId.value) : kind.value === 'personal' ? person.value && can('recurrences.manage') : can('recurrences.manage') && workspaceAdmin.value)
const mayRead = () => kind.value === 'project' ? can('nodes.read', projectId.value) : kind.value === 'personal' ? person.value : can('nodes.read')
const identity = useIdentityScope(mayRead), reads = identity.lane(), links = identity.lane(), switchReads = identity.lane()
const mayManageItem = (item: Recurrence) => mayManage.value && (kind.value === 'project' || can('recurrences.manage', item.project_id))
const inScope = (item: Recurrence) => kind.value === 'project' ? item.project_id === projectId.value : routineScope(item) === kind.value
const chosen = computed(() => {
  const row = items.value.find(item => item.id === selected.value)
  if (selected.value !== props.selectedId) return row
  const source = linked.value?.id === selected.value ? linked.value : undefined
  if (!source) return undefined
  return row && row.project_id === source.project_id && (row.revision > source.revision || row.revision === source.revision && row.occurrence_count > source.occurrence_count) ? row : source
})
const heading = computed(() => kind.value === 'personal' ? 'Your routines' : kind.value === 'workspace' ? 'Workspace routines' : 'Routines')
const lead = computed(() => kind.value === 'personal' ? 'Only you see these. They act with your rights, in projects where routines are on.' : kind.value === 'workspace' ? 'For the whole workspace. Everyone sees them; admins change them.' : 'Tickets that create themselves, and work an agent does on them by itself.')
const cardId = computed(() => kind.value === 'personal' ? 'routines' : kind.value === 'workspace' ? 'workspace-routines' : 'recurring-work')
const readOnlyReason = computed(() => kind.value === 'workspace' && can('recurrences.manage') ? 'Changing workspace routines needs a workspace admin' : 'Needs the Manage recurring work permission')
const readOnlyNote = computed(() => kind.value === 'workspace' ? 'You can read workspace routines. Changing them needs an admin.' : `You can read routines. ${readOnlyReason.value}.`)
const projectKeyOf = (item: Recurrence) => props.project && item.project_id === props.project.id ? props.project.routeKey : projects.byId(item.project_id)?.routeKey ?? ''
const outputOf = (item: Recurrence) => props.project && item.project_id === props.project.id ? props.project : projects.byId(item.project_id) ?? null
// ---------- The project switch: "Agents may run routines in WEB" ----------
const settings = ref<RoutineExecutionSettings | null>(null), switchBusy = ref(false)
const showSwitch = computed(() => kind.value === 'project' && !!projectId.value && can('harness.read', projectId.value))
const mayTurn = computed(() => person.value && can('harness.control', projectId.value) && can('run.create', projectId.value) && (workspaceAdmin.value || ['owner', 'admin'].includes(myProjectRole(projectId.value)?.key ?? '')))
function readSwitch() {
  settings.value = null
  if (!showSwitch.value) return
  const id = projectId.value
  void switchReads.run(({ after, signal }) => after(getRoutineExecutionSettings(id, signal), result => { if (id === projectId.value) settings.value = result }), { failed: () => {} })
}
watch(showSwitch, value => { if (value && !settings.value) readSwitch() })
let epoch = 0, alive = true
function clear() { identity.reset(); epoch++; items.value = []; cursor.value = null; selected.value = ''; menu.value = null; editor.value = null; runPrompt.value = null; failure.value = ''; busy.value = []; loading.value = false; linked.value = null; linkFailure.value = ''; settings.value = null }
const stopAccess = onAccessChange(change => { if (change === 'reset') clear() })
function load(older = false) {
  if (!identity.owner.value || busy.value.length || editor.value || runPrompt.value || menu.value) return
  const filter = { projectId: kind.value === 'project' ? projectId.value : undefined, scope: kind.value, after: older ? cursor.value || undefined : undefined }
  loading.value = true; failure.value = ''
  void reads.run(({ after, signal }) => after(listRoutines(filter, signal), page => {
    // Lists are replaced, never merged across people or scopes; Older appends this list's next page.
    const rows = page.items.filter(inScope)
    items.value = older ? [...items.value, ...rows.filter(row => !items.value.some(known => known.id === row.id))] : rows
    cursor.value = page.next_cursor
    if (!props.selectedId && linked.value?.id !== selected.value && !items.value.some(item => item.id === selected.value)) selected.value = items.value[0]?.id || ''
  }), { failed: error => { failure.value = error instanceof Error ? error.message : 'Routines could not be loaded.' }, settled: () => { loading.value = false } })
}
function selectSource() {
  links.cancel(); linked.value = null; linkFailure.value = ''
  const id = props.selectedId
  if (!id) { if (!items.value.some(item => item.id === selected.value)) selected.value = items.value[0]?.id || ''; return }
  selected.value = id
  void links.run(({ after, signal }) => after(getRecurrence(id, signal), item => {
    if (item.id !== id || !inScope(item)) throw new Error(kind.value === 'project' ? 'This routine belongs to another project.' : 'This routine isn’t one of these.')
    linked.value = item
    // A Desk link-out (?consent=1) opens the editor at "Let agents run it".
    if (props.consent && item.definition?.assignment && mayManageItem(item) && !editor.value) editor.value = { item, consent: true }
  }), { failed: error => { linkFailure.value = error instanceof Error ? error.message : 'The linked routine could not be loaded.' } })
}
watch([projectId, () => kind.value, () => identity.owner.value], () => { clear(); selectSource(); load(); readSwitch() }, { immediate: true, flush: 'sync' })
watch(() => props.selectedId, () => { menu.value = null; editor.value = null; runPrompt.value = null; selectSource() }, { flush: 'sync' })
watch(mayManage, value => { if (!value) { if (editor.value) revoked.value = 'draft'; editor.value = null; runPrompt.value = null; menu.value = null } })
const poller = usePoller(() => { if (!cursor.value) load() }, 20_000)
onMounted(() => { poller.start(); if (kind.value !== 'project') void projects.load() })
onBeforeUnmount(() => { alive = false; epoch++; stopAccess(); poller.stop() })
function current(item: Recurrence) { return alive && mayManageItem(item) && inScope(item) && (chosen.value?.id === item.id && chosen.value.revision === item.revision || items.value.some(row => row.id === item.id && row.revision === item.revision)) }
function replace(item: Recurrence) { if (linked.value?.id === item.id && item.revision >= linked.value.revision && item.occurrence_count >= linked.value.occurrence_count) linked.value = { ...linked.value, ...item }; const index = items.value.findIndex(row => row.id === item.id), known = items.value[index]; if (known && item.revision >= known.revision && item.occurrence_count >= known.occurrence_count) items.value[index] = { ...known, ...item } }
function closeMenu(restore = true) { const anchor = menu.value?.anchor; menu.value = null; if (restore) anchor?.focus() }
// Each write the editor makes lands here; saved also closes it.
function changed(item: Recurrence) { if (!inScope(item)) return; if (items.value.some(row => row.id === item.id) || linked.value?.id === item.id) replace(item); else items.value.push(item); selected.value = item.id; revoked.value = '' }
function saved(item: Recurrence, outcome: 'saved' | 'consented' | 'kept' = 'saved') {
  editor.value = null; changed(item)
  if (outcome === 'consented') toast(`Agents may run “${recurrenceName(item)}”${item.next_at ? ` · next run ${when(item.next_at, recurrenceZone(item.trigger))}` : ''}.`, { timeout: 8000 })
  else if (outcome === 'kept') toast(`Kept paused. ${recurrenceName(item)} waits until you let agents run it.`)
  else toast(`Saved ${recurrenceName(item)}`)
}
function refreshItem(id: string) { void identity.run(({ after, signal }) => after(getRecurrence(id, signal), item => { replace(item) }), { failed: error => { toast(error instanceof Error ? error.message : 'The routine could not be refreshed.', { tone: 'error' }) } }) }
function toggle(item: Recurrence, undo = false) {
  if (!current(item) || busy.value.includes(item.id)) return
  const owner = identity.owner.value, generation = epoch, scopeKey = `${kind.value}/${projectId.value}`, paused = !item.paused
  reads.cancel(); busy.value = [...busy.value, item.id]
  void identity.run(({ after, signal }) => after(pauseRecurrence(item.id, paused, item.revision, signal), updated => {
    replace(updated)
    toast(`${paused ? 'Paused' : 'Resumed'} ${recurrenceName(item)}.`, { timeout: 8000, action: undo ? undefined : { label: 'Undo', run: () => {
      if (!alive || owner !== identity.owner.value || generation !== epoch || scopeKey !== `${kind.value}/${projectId.value}` || !current(updated)) { toast('The routine changed since, so nothing was undone.', { tone: 'error' }); return }
      toggle(updated, true)
    } } })
  }), { failed: error => { toast(error instanceof Error ? error.message : 'Pause or resume failed.', { tone: 'error' }) }, settled: () => { busy.value = busy.value.filter(id => id !== item.id) } })
}
function run(item: Recurrence) {
  if (!current(item) || busy.value.includes(item.id)) return
  reads.cancel(); busy.value = [...busy.value, item.id]
  void identity.run(({ after, signal }) => after(getRecurrence(item.id, signal), latest => {
    if (!current(item) || latest.id !== item.id || !inScope(latest)) return
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
  void identity.run(({ after, signal }) => after(confirmAction({ title: `Delete ${recurrenceName(item)}?`, body: `No more tickets are created. Its existing tickets, receipts and provenance stay.`, confirmLabel: 'Delete', danger: true }), confirmed => {
    if (!confirmed || !current(item)) return
    reads.cancel(); busy.value = [...busy.value, item.id]
    return after(deleteRecurrence(item.id, item.revision, signal), () => { items.value = items.value.filter(row => row.id !== item.id); if (linked.value?.id === item.id) linked.value = null; if (selected.value === item.id) selected.value = items.value[0]?.id || ''; toast(`Deleted ${recurrenceName(item)}.`) })
  }), { failed: error => { toast(error instanceof Error ? error.message : 'The routine was not deleted.', { tone: 'error' }) }, settled: () => { busy.value = busy.value.filter(id => id !== item.id) } })
}
function copyCLI(item: Recurrence) {
  closeMenu(); const generation = epoch
  void identity.run(({ after }) => after(navigator.clipboard.writeText(`aeon recur get ${item.id}`), () => { if (generation === epoch) toast('Copied CLI command.') }), { failed: () => toast('The clipboard is unavailable.', { tone: 'error' }) })
}
function rowKeys(event: KeyboardEvent, item: Recurrence) {
  if (event.metaKey || event.ctrlKey || event.altKey || (event.target as HTMLElement).closest('input, textarea, select, [contenteditable="true"]')) return
  const at = items.value.findIndex(row => row.id === item.id)
  if (['ArrowDown', 'j', 'ArrowUp', 'k'].includes(event.key)) {
    event.preventDefault(); const next = items.value[at + (['ArrowDown', 'j'].includes(event.key) ? 1 : -1)]
    if (next) { selected.value = next.id; document.getElementById(`recurrence-${next.id}`)?.focus({ preventScroll: true }) }
  } else if (event.key === 'p' && mayManageItem(item)) { event.preventDefault(); toggle(item) }
  else if (event.key === 'n' && mayManageItem(item)) { event.preventDefault(); run(item) }
  else if (event.key === 'e' && mayManageItem(item)) { event.preventDefault(); editor.value = { item } }
  else if (event.key === 'Enter' && event.target === event.currentTarget) { event.preventDefault(); selected.value = item.id }
}
function menuKeys(event: KeyboardEvent) { if (!['ArrowUp', 'ArrowDown'].includes(event.key)) return; const buttons = [...(event.currentTarget as HTMLElement).querySelectorAll<HTMLButtonElement>('button:not(:disabled)')], at = buttons.indexOf(event.target as HTMLButtonElement); event.preventDefault(); buttons[(at + (event.key === 'ArrowDown' ? 1 : -1) + buttons.length) % buttons.length]?.focus() }

// ---------- Agent rows: the derived state chip and its reason ----------
const STATE_ICON: Record<RoutineState, 'clock' | 'pulse' | 'person-wait' | 'pause' | 'stop' | 'alert'> = { scheduled: 'clock', running: 'pulse', waiting: 'person-wait', held: 'pause', stopped: 'stop', failed: 'alert' }
const STATE_TINT: Record<RoutineState, string> = { scheduled: 'calm', running: 'live', waiting: 'wait', held: 'wait', stopped: 'calm', failed: 'bad' }
const me = computed(() => session.identity?.principal.id ?? '')
function stateNote(item: Recurrence) {
  const state = routineState(item), wait = item.execution_policy?.wait_reason ?? ''
  if (state === 'waiting') return item.definition?.owner_principal_id === me.value ? 'You to let agents run it' : 'Its owner to let agents run it'
  if (state === 'held') return waitReasonWords(wait || item.last_run?.reason || '', { project: projectKeyOf(item) })
  if (state === 'stopped') return 'Paused'
  return item.last_run?.reason ? reasonWords(item.last_run.reason) : 'Agents may run it'
}

function turnOff() {
  const known = settings.value, id = projectId.value, key = props.project?.routeKey ?? 'this project'
  if (!known || !mayTurn.value || switchBusy.value) return
  void identity.run(({ after, signal }) => after(confirmAction({ title: `Stop agents running routines in ${key}?`, body: 'No new runs start. Running work finishes inside what it already holds; nothing is deleted. Ticket-only routines aren’t affected.', confirmLabel: 'Turn off', danger: true }), confirmed => {
    if (!confirmed || id !== projectId.value || settings.value?.revision !== known.revision) return
    switchBusy.value = true
    return after(putRoutineExecutionSettings(id, { expected_revision: known.revision, automatic_launch_enabled: false, qualification_id: null }, signal), result => { if (id === projectId.value) settings.value = result; toast(`Agents no longer run routines in ${key}.`) })
  }), { failed: error => { toast(error instanceof Error ? error.message : 'The switch was not changed.', { tone: 'error' }); readSwitch() }, settled: () => { switchBusy.value = false } })
}
</script>
<template>
  <div class="recurring-layout">
    <section :id="cardId" class="settings-card glass-card" :aria-labelledby="`${cardId}-title`">
      <header class="card-head"><span class="card-icon"><AppIcon name="repeat" :size="16" /></span><div class="card-titles"><h2 :id="`${cardId}-title`">{{ heading }}</h2><p>{{ lead }}</p></div><div class="card-aside"><button type="button" class="btn sm" :disabled="!mayManage" :data-tip="mayManage ? 'New routine' : readOnlyReason" @click="revoked = ''; editor = {}"><AppIcon name="plus" :size="13" />New routine…</button></div></header>
      <p v-if="revoked" class="ro-note" role="status"><AppIcon name="eye" :size="13" /><span><b>The routine editor closed:</b> {{ revoked === 'saved' ? 'your rights here changed. The routine stays saved; agents don’t run it until its owner lets them.' : 'your rights here changed, so you can no longer change routines here. Nothing was saved; your draft was discarded.' }} You can still read them.</span></p>
      <p v-else-if="!mayManage" class="permission-note" role="note">{{ readOnlyNote }}</p>
      <div v-if="showSwitch && settings" class="enable"><span class="en-ic"><AppIcon name="agent" :size="15" /></span><div class="en-t"><b>Agents may run routines in {{ project?.routeKey }}</b><small>{{ settings.automatic_launch_enabled ? 'On. Each routine still needs its owner to let agents run it.' : settings.wait_reason === 'automatic_launch_disabled' && !settings.consent_enabled ? `Off, as for every project. Ask OPS to qualify ${project?.routeKey}; then an owner turns it on. Ticket-only routines work either way.` : `Off: ${waitReasonWords(settings.wait_reason, { project: project?.routeKey })} Ticket-only routines work either way.` }}</small></div><span class="stc calm"><AppIcon :name="settings.automatic_launch_enabled ? 'check' : 'stop'" :size="12" /><span>{{ settings.automatic_launch_enabled ? 'On' : 'Off' }}</span></span><button v-if="settings.automatic_launch_enabled" type="button" class="btn sm" :disabled="!mayTurn || switchBusy" :data-tip="mayTurn ? undefined : `Only a ${project?.routeKey} owner or admin can change this`" @click="turnOff">Turn off…</button></div>
      <div class="card-body"><p v-if="failure" class="error" role="alert">{{ failure }} <button type="button" class="btn sm" @click="load()">Retry</button></p><p v-if="loading && !items.length" class="empty" role="status">Loading routines…</p><p v-else-if="!items.length && !failure && !linkFailure" class="empty">No routines yet. Use New routine…, or Repeat… on a ticket.</p>
        <div v-if="items.length" class="recurrence-list" role="table" :aria-label="heading">
          <div class="list-head" role="row"><span role="columnheader">Name</span><span role="columnheader">Trigger</span><span role="columnheader">Next run</span><span role="columnheader">Last result</span><span role="columnheader">Actions</span></div>
          <div v-for="item in items" :id="`recurrence-${item.id}`" :key="item.id" class="recurrence-row" :class="{ selected: selected === item.id, paused: item.paused }" role="row" :tabindex="selected === item.id ? 0 : -1" :aria-label="recurrenceName(item)" @click="selected = item.id" @keydown.stop="rowKeys($event, item)">
            <div class="cell name" role="cell"><span class="glyph" :class="item.definition?.assignment ? 'agent' : 'ticket'" aria-hidden="true"><AppIcon :name="item.definition?.assignment ? 'agent' : 'repeat'" :size="14" /></span><span class="name-t"><b>{{ recurrenceName(item) }}</b><small>{{ item.template.type === 'task' ? 'Task' : 'Ticket' }} {{ item.parent_id === item.project_id ? 'at top level' : 'in an epic' }}{{ kind !== 'project' && projectKeyOf(item) ? ` of ${projectKeyOf(item)}` : '' }}{{ item.definition?.assignment ? ` · an agent ${effortVerb(item.definition.assignment.role)}` : item.queue_each ? ' · queued' : '' }}</small></span></div>
            <div class="cell trigger" role="cell"><span>{{ triggerWords(item.trigger) }}</span><small>{{ item.overlap_policy === 'skip' ? 'skips while the last is open' : 'creates even while open' }}</small></div>
            <div class="cell next" role="cell"><span><template v-if="item.paused"><AppIcon name="pause" :size="12" />Paused</template><template v-else>{{ item.trigger.kind === 'event' ? 'After the next release' : item.next_at ? when(item.next_at, recurrenceZone(item.trigger)) : 'No next date' }}</template></span><small>{{ item.paused ? item.trigger.kind === 'event' ? 'releases are ignored' : item.next_at ? `would be ${when(item.next_at, recurrenceZone(item.trigger))}` : '' : item.open_previous && item.overlap_policy === 'skip' ? `skipped if #${item.open_previous.number} is still open` : item.queue_each ? 'into the work queue' : '' }}</small></div>
            <div v-if="item.definition?.assignment" class="cell last" role="cell"><span><span class="stc" :class="STATE_TINT[routineState(item)]"><AppIcon :name="STATE_ICON[routineState(item)]" :size="12" /><span>{{ ROUTINE_STATE_WORDS[routineState(item)] }}</span></span></span><small :title="stateNote(item)">{{ stateNote(item) }}</small></div>
            <div v-else class="cell last" role="cell"><span v-if="item.last_result"><RouterLink v-if="item.last_result.key && projectKeyOf(item)" :to="`/p/${encodeURIComponent(projectKeyOf(item))}/${encodeURIComponent(item.last_result.key)}`" @click.stop>{{ item.last_result.key }}</RouterLink><template v-else>{{ item.last_result.outcome === 'skipped' ? 'Skipped' : item.last_result.key || 'Ticket unavailable' }}</template><span class="number">#{{ item.last_result.number }}</span></span><span v-else>Nothing yet</span><small>{{ item.last_result ? `${when(item.last_result.scheduled_at, recurrenceZone(item.trigger))} · ${item.last_result.outcome === 'skipped' ? reasonWords(item.last_result.reason) : item.last_result.state || 'created'}` : '' }}</small></div>
            <div class="cell row-actions" role="cell"><button type="button" class="btn sm toggle" :disabled="!mayManageItem(item) || busy.includes(item.id)" :data-tip="mayManageItem(item) ? item.paused ? 'Resume · p' : 'Pause · p' : readOnlyReason" @click.stop="toggle(item)"><AppIcon :name="item.paused ? 'play' : 'pause'" :size="12" />{{ item.paused ? 'Resume' : 'Pause' }}</button><button type="button" class="btn sm run" :disabled="!mayManageItem(item) || busy.includes(item.id)" :data-tip="mayManageItem(item) ? 'Run now · n' : readOnlyReason" @click.stop="run(item)">Run now</button><button type="button" class="icon-btn sm flat" :aria-label="`More for ${recurrenceName(item)}`" aria-haspopup="menu" :aria-expanded="menu?.item.id === item.id" @click.stop="menu = { item, anchor: $event.currentTarget as HTMLElement }"><AppIcon name="more" :size="15" /></button></div>
          </div>
        </div>
        <button v-if="cursor" type="button" class="btn sm ghost more" :disabled="loading" @click="load(true)">More routines</button>
        <p class="list-hint"><KeyCap k="j" /><KeyCap k="k" /> move · <KeyCap k="p" /> pause / resume · <KeyCap k="n" /> run now · <KeyCap k="e" /> edit</p>
        <p v-if="linkFailure" class="error" role="alert">{{ linkFailure }} <button type="button" class="btn sm" @click="selectSource()">Retry</button></p>
      </div>
    </section>
    <RecurrenceHistory v-if="chosen && projectKeyOf(chosen)" :key="chosen.id" :item="chosen" :project-key="projectKeyOf(chosen)" :may-manage="mayManageItem(chosen)" :busy="busy.includes(chosen.id)" @toggle="toggle(chosen!)" @run="run(chosen!)" @edit="editor = { item: chosen }" />
    <FloatingPanel v-if="menu" :anchor="menu.anchor" :width="236" align="end" :label="`More for ${recurrenceName(menu.item)}`" @close="closeMenu"><div class="row-menu" role="menu" @keydown="menuKeys"><button type="button" role="menuitem" :disabled="!mayManageItem(menu.item) || busy.includes(menu.item.id)" :data-tip="!mayManageItem(menu.item) ? readOnlyReason : undefined" @click="editor = { item: menu.item }; closeMenu(false)"><AppIcon name="edit" :size="14" />Edit…</button><button type="button" role="menuitem" @click="selected = menu.item.id; closeMenu()"><AppIcon name="history" :size="14" />History</button><button type="button" role="menuitem" @click="copyCLI(menu.item)"><AppIcon name="terminal" :size="14" />Copy CLI command</button><div class="separator" /><button type="button" role="menuitem" class="danger" :disabled="!mayManageItem(menu.item) || busy.includes(menu.item.id)" @click="remove(menu.item)"><AppIcon name="trash" :size="14" />Delete…</button></div></FloatingPanel>
    <RecurrenceEditor v-if="editor" :project="editor.item ? outputOf(editor.item) : project" :scope="kind" :recurrence="editor.item" :consent="editor.consent" @close="editor = null" @saved="saved" @changed="changed" @revoked="state => { revoked = state ? 'saved' : 'draft' }" />
    <RecurrenceRun v-if="runPrompt" :key="runPrompt.id" :item="runPrompt" @close="runPrompt = null" @ran="ran" />
  </div>
</template>
<style scoped>
.recurring-layout { display: grid; gap: 16px; padding: 18px 0; }.settings-card { padding: 20px; }.card-head { display: flex; align-items: center; gap: 12px; }.card-icon { display: grid; place-items: center; flex: none; align-self: flex-start; width: 32px; height: 32px; border-radius: 10px; background: var(--chip-teal-bg); color: var(--teal-ink); box-shadow: inset 0 0 0 1px var(--chip-teal-line); }.card-titles { flex: 1; min-width: 0; }.card-titles h2 { font: 600 15px/1.35 var(--font); }.card-titles p { margin-top: 2px; font-size: 13px; }.card-aside { flex: none; }.card-body { margin-top: 14px; }.permission-note { margin-top: 12px; font-size: 12px; color: var(--ink-3); }.empty { padding: 24px 8px; font-size: 13px; color: var(--ink-3); }
.recurrence-list { display: grid; grid-template-columns: minmax(170px, 1.3fr) minmax(140px, 1.1fr) minmax(125px, .9fr) minmax(150px, 1fr) 208px; }.list-head, .recurrence-row { display: grid; grid-template-columns: subgrid; grid-column: 1/-1; align-items: center; }.list-head { height: 32px; border-top: 1px solid var(--line); border-bottom: 1px solid var(--line); font: 500 10.5px var(--mono); letter-spacing: .14em; text-transform: uppercase; color: var(--ink-3); }.list-head > span, .cell { padding: 0 8px; min-width: 0; }.list-head > span:last-child { text-align: right; }.recurrence-row { min-height: 64px; border-radius: 10px; margin: 2px -6px 0; padding: 0 6px; font-size: 13px; cursor: pointer; }.recurrence-row:hover { background: var(--row-hover); }.recurrence-row.selected { background: var(--row-selected); box-shadow: inset 0 0 0 1px var(--chip-teal-line); }.recurrence-row:focus-visible { box-shadow: var(--focus-ring); }.cell:not(.row-actions) { display: grid; line-height: 1.3; }.cell > b, .cell > span, .cell > small { white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }.cell b { font-weight: 600; }.cell small { height: 15px; font-size: 11.5px; color: var(--ink-3); }.next > span, .last > span { display: flex; align-items: center; gap: 5px; height: 22px; }.last a { font: 600 11.5px var(--mono); }.number { color: var(--ink-3); font-size: 11.5px; }.paused .name b { color: var(--ink-2); }.row-actions { display: flex; justify-content: flex-end; gap: 4px; }.toggle { width: 92px; padding: 0; }.run { width: 76px; padding: 0; }.list-hint { display: flex; align-items: center; gap: 4px; margin-top: 10px; font-size: 12px; color: var(--ink-3); }.more { margin-top: 10px; }.row-menu { display: grid; gap: 1px; }.row-menu button { display: flex; align-items: center; gap: 10px; width: 100%; min-height: 34px; padding: 0 10px; text-align: left; border: 0; border-radius: 8px; background: transparent; font-size: 13.5px; }.row-menu button:hover:not(:disabled) { background: var(--row-hover); }.row-menu .danger { color: var(--danger); }.separator { height: 1px; margin: 4px 6px; background: var(--line); }
/* The name carries the kind of routine: the gold loop for ticket-only, teal for an agent. */
.cell.name { grid-template-columns: 32px minmax(0, 1fr); align-items: center; gap: 10px; }.glyph { display: grid; place-items: center; width: 32px; height: 32px; border-radius: 10px; }.glyph.agent { color: var(--teal-ink); background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); }.glyph.ticket { color: var(--marker-on); background: var(--marker); }.name-t { display: grid; min-width: 0; }.name-t b, .name-t small { white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.stc { display: inline-flex; align-items: center; gap: 6px; height: 22px; padding: 0 8px; border-radius: 999px; font-size: 12px; font-weight: 600; white-space: nowrap; color: var(--ink-2); background: var(--surface-sunken); box-shadow: inset 0 0 0 1px var(--line); }.stc svg { flex: none; }.stc.wait { color: var(--queue-wait-ink); background: var(--queue-wait-bg); box-shadow: inset 0 0 0 1px var(--queue-wait-line); }.stc.bad { color: var(--danger); background: var(--danger-bg); box-shadow: inset 0 0 0 1px var(--danger-line); }.stc.live { color: var(--teal-ink); background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); }
.ro-note { display: flex; align-items: flex-start; gap: 8px; margin: 14px 0 0; padding: 8px 12px; border-radius: 10px; background: var(--surface-sunken); font-size: 12.5px; line-height: 1.5; color: var(--ink-2); }.ro-note svg { flex: none; margin-top: 2px; color: var(--ink-3); }.ro-note b { color: var(--ink); font-weight: 600; }
.enable { display: grid; grid-template-columns: 32px minmax(0, 1fr) auto auto; align-items: center; gap: 12px; margin-top: 16px; padding: 10px 12px; border-radius: 12px; background: var(--surface-sunken); }.en-ic { display: grid; place-items: center; width: 32px; height: 32px; border-radius: 10px; background: var(--surface-raised); box-shadow: inset 0 0 0 1px var(--line); color: var(--ink-2); }.en-t { display: grid; min-width: 0; }.en-t b { font-size: 13px; font-weight: 600; color: var(--ink); }.en-t small { font-size: 12px; line-height: 1.45; color: var(--ink-3); }.enable .btn { min-width: 92px; }
@media (max-width: 1000px) { .recurrence-list { grid-template-columns: minmax(0, 1fr); }.list-head { display: none; }.recurrence-row { grid-template-columns: minmax(0, 1fr); gap: 2px; padding: 10px 6px; border-top: 1px solid var(--line); margin: 0 -6px; }.cell { padding: 0; }.name b { margin-bottom: 1px; }.trigger small { display: none; }.next, .last { padding-top: 4px; }.row-actions { justify-content: flex-start; margin-top: 8px; }.list-hint { display: none; } }
@media (max-width: 600px) { .settings-card { padding: 16px; }.card-head { flex-wrap: wrap; }.card-aside { flex-basis: 100%; padding-left: 44px; }.enable { display: flex; flex-wrap: wrap; }.enable .en-t { flex: 1 1 calc(100% - 44px); }.enable .stc { margin-left: 44px; } }
@media (pointer: coarse) { .enable .btn { min-height: 44px; } }
</style>
