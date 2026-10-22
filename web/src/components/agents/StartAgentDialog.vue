<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, useId, watch } from 'vue'
import { listNodes, type ListItem, type ListProject, type WorkNode } from '../../lib/api'
import { can, onAccessRevalidate } from '../../lib/authz'
import { useSession } from '../../stores/session'
import { useProjects } from '../../stores/projects'
import { deletePin, listPins, putPin, message, type AgentRun, type HarnessSession } from '../../lib/agents'
import { getRun, listAllSessions, runNowOnce } from '../../lib/agentRows'
import {
  AUTHOR_FAMILIES, authorFamilyFor, chooseStep, emptyChoice, emptyTouch, familyLabel, fetchAccountCatalog, fillDefaults, presentCascade, sessionReport, workRoleFor,
  type AgentAccountCatalog, type AuthorFamily, type CascadeChoice, type CascadeStep, type CascadeTouch, type CatalogGap, type RequestedRun,
} from '../../lib/accountCascade'
import { wroteSince, writeMark } from '../../lib/position'
import { launchState, staleGrantRejection, startAgent, startAgentCopy } from '../../lib/startAgent'
import { useAgents } from '../../stores/agents'
import { usePoller } from '../../lib/usePolledData'
import { canLaunchLead, leadLaunchReason, LEAD_WORDS, readLead, type ProjectLead } from '../../lib/lead'
import { useWorkQueue } from '../../stores/workQueue'
import AppIcon from '../AppIcon.vue'
import KeyCap from '../KeyCap.vue'

// Shared by /agents and TicketWorkspace. The cascade is host, harness, account,
// model, then thinking. Each later step is filled only from the catalog grant.
const agents = useAgents()
const queue = useWorkQueue()
const session = useSession()
const projects = useProjects()
const copy = startAgentCopy()
const person = computed(() => session.identity ? `${session.identity.tenant.id}/${session.identity.principal.id}` : '')
const oneOff = ref(false)
const newTicket = ref(true)
const projectId = ref('')
const task = ref('')
const instruction = ref('')
const mayChangeFiles = ref(false)
let requestId = '', requestKey = ''
const projectOptions = computed(() => projects.projects.filter(project => !project.archived && !project.frozen))
const selectedProject = computed(() => projectOptions.value.find(project => project.id === projectId.value))
const w = LEAD_WORDS
const mac = typeof navigator !== 'undefined' && /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent)
const uid = useId()
const dialog = ref<HTMLDialogElement>()
const searchInput = ref<HTMLInputElement>()
const taskInput = ref<HTMLInputElement>()
const settingsOpen = ref(false)
const lastSettingsWarning = ref<{ label: string; detail: string }[]>([])
const hostSelect = ref<HTMLSelectElement>()
const tickets = ref<WorkNode[]>([])
type StartTicket = WorkNode & Partial<Pick<ListItem, 'project'>>
const ticket = ref<StartTicket | null>(null)
const dispatchMode = ref<'manual' | 'checking' | 'lead' | 'failed'>('manual')
const dispatchProject = ref<ListProject | null>(null)
const projectLead = ref<ProjectLead | null>(null)
const dispatchError = ref('')
const queuedForLead = ref(false)
const query = ref('')
const searching = ref(false)
const searchError = ref('')
const nextCursor = ref<string | null>(null)
const catalog = ref<AgentAccountCatalog | null>(null)
const catalogGap = ref<CatalogGap>(null)
const catalogMessage = ref('')
const authorFamily = ref<AuthorFamily | ''>('')
const choice = ref<CascadeChoice>(emptyChoice())
const touch = ref<CascadeTouch>(emptyTouch())
const loading = ref(false)
const busy = ref(false)
const error = ref('')
// The run and its session are followed by id, always as the ledger holds them: a
// launch result, a status read or the page's own lists may each bring a newer copy,
// and a response that arrives late never shows an older one (AEON-449).
const runId = ref('')
const run = computed<AgentRun | null>(() => (runId.value && agents.runs[runId.value]) || null)
const requested = ref<RequestedRun | null>(null)
const managedId = ref('')
const managed = computed<HarnessSession | null>(() => (managedId.value && agents.sessionById(managedId.value)) || null)
const reused = ref(false)
const grantStale = ref(false)
const checking = ref(false)
const checkError = ref('')
const remember = ref(false)
const remembered = ref('')
const now = ref(Date.now())
const visible = ref(false)
let opener: HTMLElement | null = null
let generation = 0
let searchGeneration = 0
let catalogGeneration = 0
let dispatchGeneration = 0
let searchTimer: ReturnType<typeof setTimeout> | undefined
const poll = usePoller(() => { now.value = Date.now(); if (run.value) return refresh() }, 3000, { enabled: () => visible.value && !!run.value })
const role = computed(() => workRoleFor(oneOff.value && newTicket.value ? null : ticket.value))
const view = computed(() => presentCascade({ catalog: catalog.value, role: role.value, authorFamily: authorFamily.value }, choice.value, touch.value))
const piModelBound = computed(() => choice.value.harness === 'pi' && view.value.models.length === 1)
const cascadeLocked = computed(() => dispatchMode.value !== 'manual' || loading.value || catalogGap.value === 'failed' || catalogGap.value === 'forbidden' || catalogGap.value === 'family')
const state = computed(() => run.value ? launchState(run.value) : null)
const sessionConnected = computed(() => !!managed.value && managed.value.phase !== 'stopped')
const reported = computed(() => sessionConnected.value && managed.value && requested.value ? sessionReport(managed.value, requested.value) : null)
const permitted = computed(() => can('work_orders.write') && can('run.create'))
const selectionVisible = computed(() => !!view.value.profileId && view.value.efforts.some(effort => effort.value === view.value.profileId) && view.value.models.some(model => model.value === choice.value.modelKey) && view.value.accounts.some(account => account.value === choice.value.accountId))
// Notes that only restate a single option are filler; notes that explain a choice or a gap stay.
const FILLER = [/^Only one (harness|model) /, /^This model grants one thinking level/, /^These harnesses have an enrolled account/, /^Only one host has/]
const note = (text: string) => text && !FILLER.some(pattern => pattern.test(text)) ? text : ''
// The status box below already states an unavailable account's reason.
const notes = computed(() => ({ host: note(view.value.notes.host), harness: note(view.value.notes.harness), account: selectedWait.value || view.value.status.detail.includes(view.value.notes.account) ? '' : view.value.notes.account, model: note(view.value.notes.model), effort: note(view.value.notes.effort) }))
// A closed select cannot wrap: on phones the account option keeps name and plan, and
// the note below it carries the allowance.
const narrow = ref(typeof window !== 'undefined' && window.matchMedia('(max-width: 600px)').matches)
const narrowQuery = typeof window !== 'undefined' ? window.matchMedia('(max-width: 600px)') : null
const onNarrow = (event: MediaQueryListEvent) => { narrow.value = event.matches }
narrowQuery?.addEventListener('change', onNarrow)
const accountLabel = (label: string) => narrow.value ? label.split(' · ').slice(0, 2).join(' · ') : label
const selectedWait = computed(() => catalog.value?.hosts.find(h => h.daemon_id === choice.value.hostId)?.harnesses.find(h => h.harness === choice.value.harness)?.accounts.find(a => a.id === choice.value.accountId)?.wait)
const readOnlyCapability = computed(() => catalog.value?.hosts.find(h => h.daemon_id === choice.value.hostId)?.harnesses.find(h => h.harness === choice.value.harness)?.read_only_run)
const readOnlyReason = computed(() => !oneOff.value || mayChangeFiles.value || !choice.value.harness ? '' : readOnlyCapability.value?.supported && readOnlyCapability.value.policy === 'no_tools' ? '' : readOnlyCapability.value?.reason || 'This account catalog has no qualified read-only one-off adapter. Refresh it or choose another harness.')
const mayRunNow = computed(() => permitted.value && session.identity?.principal.kind === 'person')
const contextReady = computed(() => oneOff.value && newTicket.value ? can('nodes.write') && !projects.loading && !projects.error && !!selectedProject.value?.nodeUpdatedAt && !!task.value.trim() : ticket.value?.is_leaf === true)
const canSubmit = computed(() => dispatchMode.value === 'manual' && permitted.value && !loading.value && !busy.value && !run.value && contextReady.value && !readOnlyReason.value && !catalogGap.value && !!view.value.agentId && selectionVisible.value)
const settingsSummary = computed(() => loading.value ? copy.loading : copy.runsOn(Object.values(view.value.requested).join(' · ')))
const settingsWarning = computed(() => {
  const warnings: { label: string; detail: string }[] = []
  if (loading.value) return warnings
  if (catalogGap.value) warnings.push({ label: copy.accountsWarning, detail: catalogMessage.value })
  if (readOnlyReason.value) warnings.push({ label: copy.readOnlyWarning, detail: readOnlyReason.value })
  if (catalog.value && view.value.status.tone === 'warn') warnings.push(view.value.status)
  return warnings
})
watch(() => settingsWarning.value.map(warning => `${warning.label} ${warning.detail}`).join(' '), warning => {
  if (oneOff.value && visible.value && warning) { lastSettingsWarning.value = settingsWarning.value; settingsOpen.value = true }
})
function toggleSettings() {
  settingsOpen.value = !settingsOpen.value
  if (!settingsOpen.value && !settingsWarning.value.length) lastSettingsWarning.value = []
}

function resetTask() {
  generation++; instruction.value = ''; mayChangeFiles.value = false; error.value = ''
  remember.value = false; remembered.value = ''
  requestId = ''; requestKey = ''
}
watch([() => ticket.value?.id, projectId, newTicket], resetTask, { flush: 'sync' })
watch(person, () => {
  resetTask(); task.value = ''; searchGeneration++; catalogGeneration++; pinGeneration++; dispatchGeneration++
  visible.value = false; busy.value = false; checking.value = false
  ticket.value = null; projectId.value = ''; runId.value = ''; managedId.value = ''; requested.value = null
  dispatchProject.value = null; projectLead.value = null; dispatchMode.value = 'manual'; queuedForLead.value = false
  catalog.value = null; tickets.value = []; poll.stop(); clearTimeout(searchTimer); dialog.value?.close()
}, { flush: 'sync' })
const mayQueueForLead = computed(() => session.identity?.principal.kind === 'person' && !!dispatchProject.value && can('run.create', dispatchProject.value.id))
const canQueueForLead = computed(() => dispatchMode.value === 'lead' && mayQueueForLead.value && !busy.value && !queue.busy && !queuedForLead.value && ticket.value?.is_leaf === true)
const dismissLabel = computed(() => queuedForLead.value ? 'Close' : 'Cancel')
const submitIcon = computed(() => busy.value ? 'clock' : dispatchMode.value === 'lead' ? queuedForLead.value ? 'check' : 'queue-add' : 'arrow')

// Read the selected project's policy before asking for manual launch choices.
// A delayed answer belongs only to this ticket revision and dialog opening.
async function checkDispatch() {
  const selected = ticket.value, turn = ++dispatchGeneration
  dispatchMode.value = selected ? 'checking' : 'manual'
  dispatchProject.value = null; projectLead.value = null; dispatchError.value = ''; queuedForLead.value = false; error.value = ''; grantStale.value = false
  catalogGeneration++; catalog.value = null; loading.value = false
  if (oneOff.value) { dispatchMode.value = 'manual'; void loadCatalog(); return }
  if (!selected) return
  const { id, updated_at: revision } = selected
  const current = () => turn === dispatchGeneration && visible.value && ticket.value?.id === id && ticket.value?.updated_at === revision
  try {
    // Search rows already carry the project; node-only entry points resolve it
    // with one bounded list read rather than guessing from a key or parent.
    let project = selected.project
    if (project === undefined) {
      const page = await listNodes({ ids: [selected.id], shape: ['leaf'], limit: 1, rollups: 'omit' })
      if (!current()) return
      project = page.items.find(item => item.id === selected.id && item.is_leaf === true)?.project
      if (project === undefined) throw new Error('The ticket’s project could not be read. Choose the ticket again.')
    }
    if (project) {
      const lead = await readLead(project.id)
      if (!current()) return
      if (lead.project_id !== project.id || !Number.isSafeInteger(lead.revision) || lead.revision < 0) throw new Error('The project’s start settings could not be read.')
      dispatchProject.value = project
      projectLead.value = lead
      if (lead.revision > 0) { dispatchMode.value = 'lead'; return }
    }
    if (!current()) return
    dispatchMode.value = 'manual'
    void loadCatalog()
  } catch (e) {
    if (!current()) return
    dispatchMode.value = 'failed'
    dispatchError.value = `Start settings could not be checked. ${message(e)}`
  }
}

async function queueForLead() {
  if (!canQueueForLead.value || !ticket.value || !dispatchProject.value) return
  const { id, updated_at: revision } = ticket.value, project = dispatchProject.value.id, turn = generation
  const identity = () => `${session.identity?.tenant?.id}:${session.identity?.principal.id}`
  const actor = identity()
  const current = () => turn === generation && visible.value && identity() === actor && ticket.value?.id === id && ticket.value?.updated_at === revision && dispatchProject.value?.id === project
  busy.value = true; error.value = ''
  try {
    const result = await queue.add(project, id)
    if (!current()) return
    if (!result || result.node_id !== id) throw new Error('The queue addition could not be confirmed. Reload before trying again.')
    queuedForLead.value = true
    void agents.afterWrite()
  } catch (e) { if (current()) error.value = message(e) }
  finally { if (turn === generation) busy.value = false }
}

async function search(more = false) {
  const turn = ++searchGeneration
  searching.value = true; searchError.value = ''
  try {
    const page = await listNodes({ kind: ['work'], shape: ['leaf'], q: query.value.trim(), limit: 30, sort: '-updated_at', rollups: 'omit', ...(more && nextCursor.value ? { cursor: nextCursor.value } : {}) })
    if (turn !== searchGeneration || !visible.value) return
    const leaves = page.items.filter(item => item.is_leaf === true)
    tickets.value = more ? [...tickets.value, ...leaves] : leaves
    nextCursor.value = page.next_cursor
  } catch (e) {
    if (turn !== searchGeneration) return
    searchError.value = e instanceof Error && e.message.includes('Work aggregates exceeded a resource limit; narrow the scope or retry.')
      ? 'Too many matches. Type more of the title or the key.'
      : message(e)
    tickets.value = []
    nextCursor.value = null
  }
  finally { if (turn === searchGeneration) searching.value = false }
}
watch(query, () => { clearTimeout(searchTimer); searchTimer = setTimeout(() => void search(), 200) })
async function loadCatalog() {
  const turn = ++catalogGeneration
  loading.value = true
  catalogGap.value = null
  catalogMessage.value = ''
  const result = await fetchAccountCatalog(role.value.role, authorFamily.value)
  if (turn !== catalogGeneration || !visible.value) return
  catalog.value = result.catalog
  catalogGap.value = result.gap
  catalogMessage.value = result.message
  if (!result.catalog) touch.value = emptyTouch()
  choice.value = fillDefaults(result.catalog, result.catalog ? choice.value : emptyChoice(), touch.value)
  loading.value = false
}
function pick(step: CascadeStep, value: string) {
  grantStale.value = false
  error.value = ''
  const next = chooseStep(choice.value, touch.value, step, value)
  touch.value = next.touch
  choice.value = fillDefaults(catalog.value, next.choice, next.touch)
}
function setFamily(value: string) {
  authorFamily.value = (AUTHOR_FAMILIES as readonly string[]).includes(value) ? value as AuthorFamily : ''
  touch.value = emptyTouch()
  choice.value = emptyChoice()
  void loadCatalog()
}
function selectTicket(item: StartTicket) {
  if (busy.value || item.is_leaf !== true) return
  ticket.value = item
  authorFamily.value = authorFamilyFor(item)
  touch.value = emptyTouch()
  choice.value = emptyChoice()
  remember.value = false
  remembered.value = ''
  void checkDispatch()
}
let pinGeneration = 0
watch([() => oneOff.value && newTicket.value ? undefined : ticket.value?.id, () => choice.value.harness, catalog], async () => {
  const id = oneOff.value && newTicket.value ? undefined : ticket.value?.id
  const harness = choice.value.harness
  const turn = ++pinGeneration
  if (!visible.value || !id || !harness) return
  try {
    const pins = await listPins(id)
    if (turn !== pinGeneration || !visible.value || oneOff.value && newTicket.value || ticket.value?.id !== id || choice.value.harness !== harness) return
    const pin = pins.find(item => item.harness === harness && item.account_id)
    if (!pin?.account_id || touch.value.account || !view.value.accounts.some(account => account.value === pin.account_id)) return
    remembered.value = pin.account_id
    pick('account', pin.account_id)
    remember.value = true
  } catch { /* A missing pin leaves the checkbox clear. */ }
})
async function open(initial?: StartTicket, mode: 'expert' | 'once' = 'expert') {
  if (busy.value) return
  opener = document.activeElement as HTMLElement
  generation++; visible.value = true
  oneOff.value = mode === 'once'; newTicket.value = !initial; projectId.value = ''; resetTask(); task.value = ''; settingsOpen.value = false; lastSettingsWarning.value = []
  ticket.value = initial?.is_leaf === true ? initial : null; query.value = ''; tickets.value = []; nextCursor.value = null
  authorFamily.value = authorFamilyFor(ticket.value)
  catalog.value = null; catalogGap.value = null; catalogMessage.value = ''
  choice.value = emptyChoice(); touch.value = emptyTouch()
  remember.value = false; remembered.value = ''
  runId.value = ''; requested.value = null; managedId.value = ''; reused.value = false
  error.value = ''; checkError.value = ''; searchError.value = ''; grantStale.value = false; checking.value = false
  dialog.value?.showModal()
  void checkDispatch()
  if (!oneOff.value && !ticket.value) void loadCatalog()
  if (oneOff.value) void projects.load(true)
  if (!ticket.value) void search()
  poll.start()
  await nextTick()
  if (oneOff.value) taskInput.value?.focus()
  else if (ticket.value) hostSelect.value?.focus()
  else searchInput.value?.focus()
}
function openOnce(initial?: StartTicket) { return open(initial, 'once') }
function escape() {
  const active = document.activeElement
  if (active instanceof HTMLTextAreaElement || active instanceof HTMLSelectElement || active instanceof HTMLInputElement && ['text', 'search'].includes(active.type)) { active.blur(); return }
  close()
}
function formKey(event: KeyboardEvent) {
  if (event.key === 'Escape') { event.preventDefault(); event.stopPropagation(); escape(); return }
  if (event.key !== 'Enter') return
  if (!event.altKey && !event.shiftKey && (mac ? event.metaKey && !event.ctrlKey : event.ctrlKey && !event.metaKey)) {
    event.preventDefault(); event.stopPropagation(); void submit()
  } else if (event.target instanceof HTMLInputElement && ['text', 'search'].includes(event.target.type) && !event.ctrlKey && !event.metaKey) event.preventDefault()
}
function close() {
  if (busy.value) return
  generation++; searchGeneration++; catalogGeneration++; dispatchGeneration++; visible.value = false
  poll.stop(); clearTimeout(searchTimer)
  dialog.value?.close(); opener?.focus({ preventScroll: true })
}
function changeTicket() {
  ticket.value = null
  authorFamily.value = ''
  touch.value = emptyTouch()
  choice.value = emptyChoice()
  remember.value = false
  remembered.value = ''
  void checkDispatch()
  if (!oneOff.value) void loadCatalog()
  void search()
  void nextTick(() => searchInput.value?.focus())
}
async function submit(runNow = false) {
  if (dispatchMode.value === 'lead') { if (!runNow) await queueForLead(); return }
  if (!canSubmit.value) return
  const context = oneOff.value && newTicket.value && selectedProject.value
    ? { id: selectedProject.value.id, updated_at: selectedProject.value.nodeUpdatedAt! } as WorkNode
    : ticket.value
  if (!context) return
  busy.value = true; error.value = ''
  const turn = generation, owner = person.value
  const chosen = { ticket: { ...context }, agentId: view.value.agentId, profileId: view.value.profileId, accountId: choice.value.accountId, runNow }
  const options = { instruction: (instruction.value.trim() || newTicket.value) ? instruction.value : task.value, repositoryMutationAllowed: mayChangeFiles.value, ...(newTicket.value ? { task: task.value } : {}) }
  const signature = JSON.stringify([owner, chosen, options])
  if (signature !== requestKey) { requestKey = signature; requestId = crypto.randomUUID() }
  const isCurrent = () => turn === generation && visible.value && owner === person.value
  const pinned = { ...view.value.requested }
  const pin = { ticket: oneOff.value && newTicket.value ? undefined : ticket.value?.id, harness: choice.value.harness, account: choice.value.accountId, remember: remember.value, remembered: remembered.value }
  const mark = writeMark()
  try {
    if (pin.ticket && pin.account && pin.remember) await putPin({ ticket_id: pin.ticket, harness: pin.harness, account_id: pin.account })
    else if (pin.ticket && pin.remembered && !pin.remember) await deletePin(pin.ticket, pin.harness)
    if (!isCurrent()) return
    const result = await startAgent({ ...chosen, ...(oneOff.value ? { runOnce: { ...options, requestId } } : {}), isCurrent }, { run: agents.admitRun, runs: agents.admitRuns })
    if (!isCurrent()) return
    runId.value = result.run.id; reused.value = result.reused; requested.value = pinned
    void agents.afterWrite()
    await refresh()
    await nextTick(); if (isCurrent()) dialog.value?.querySelector<HTMLElement>('[data-result]')?.focus()
  } catch (e) {
    if (!isCurrent()) return
    // A pin or a work order may already be written when a later step fails: the lists re-read either way.
    if (wroteSince(mark)) void agents.afterWrite()
    if (staleGrantRejection(e)) {
      const next = chooseStep(choice.value, touch.value, 'model', '')
      const pinned = { ...next.touch, account: true }
      touch.value = pinned
      choice.value = fillDefaults(catalog.value, next.choice, pinned)
      grantStale.value = true
      error.value = 'This account no longer allows that model. Nothing was queued, and another account was not used. Refresh the catalog, then choose a model it offers.'
    } else error.value = `${message(e)} Your selections are kept. Retrying checks for an existing work order and run first.`
  }
  finally { if (isCurrent()) busy.value = false }
}
async function overrideQueued() {
  if (!run.value || busy.value) return
  const turn = generation, owner = person.value, id = run.value.id
  const current = () => turn === generation && owner === person.value && visible.value && runId.value === id
  busy.value = true; checkError.value = ''
  try { const wire = await runNowOnce(id); if (current()) { agents.admitRun(wire); void agents.afterWrite() } }
  catch (e) { if (current()) checkError.value = message(e) }
  finally { if (current()) busy.value = false }
}
async function refresh() {
  if (!run.value || checking.value) return
  const turn = generation, id = run.value.id
  checking.value = true
  try {
    const wire = await getRun(id)
    if (turn !== generation || !visible.value) return
    agents.admitRun(wire)
    checkError.value = ''
    const agentId = agents.runs[id]?.agent_principal_id
    if (!agentId) return
    const page = await listAllSessions({ agent: agentId, limit: 200 })
    if (turn !== generation || !visible.value) return
    managedId.value = agents.admitSessions(page.items).find(s => s.run_id === id && s.management_mode === 'managed')?.id ?? ''
    if (managedId.value) void agents.refreshSessions()
  } catch { if (turn === generation) checkError.value = 'Status could not be refreshed. The last reported state is shown.' }
  finally { if (turn === generation) checking.value = false }
}
const stopAccess = onAccessRevalidate({ reset: () => {
  busy.value = false; close()
  ticket.value = null; dispatchProject.value = null; projectLead.value = null; dispatchMode.value = 'manual'; queuedForLead.value = false
} })
onBeforeUnmount(() => { stopAccess(); narrowQuery?.removeEventListener('change', onNarrow); generation++; searchGeneration++; catalogGeneration++; dispatchGeneration++; poll.stop(); clearTimeout(searchTimer) })
defineExpose({ open, openOnce })
</script>

<template>
  <dialog ref="dialog" class="launch-dialog" :class="{ 'run-once': oneOff }" :aria-labelledby="`${uid}-title`" @cancel.prevent="escape">
    <form class="launch-card" @submit.prevent="submit()" @keydown="formKey">
      <header class="launch-head">
        <h2 :id="`${uid}-title`">{{ oneOff ? copy.once : 'Start agent' }}</h2>
        <button type="button" class="icon-btn flat" aria-label="Close start agent" :disabled="busy" @click="close"><AppIcon name="close" /></button>
      </header>

      <template v-if="!run">
        <div class="launch-body">
        <fieldset :disabled="busy" class="launch-fields" :class="{ 'one-off': oneOff }">
          <legend class="sr-only">Run setup</legend>
          <div v-if="oneOff" class="select-field">
            <label :for="`${uid}-task`">{{ copy.task }}</label>
            <input :id="`${uid}-task`" ref="taskInput" v-model="task" class="field" maxlength="160" />
          </div>
          <div v-if="oneOff" class="context-choice select-field">
            <label :for="`${uid}-context`">{{ copy.context }}</label>
            <select :id="`${uid}-context`" v-model="newTicket" class="field">
              <option :value="true">{{ copy.createTicket }}</option>
              <option :value="false">{{ copy.existingTicket }}</option>
            </select>
          </div>
          <div v-if="oneOff && newTicket" class="new-ticket">
            <div class="select-field">
              <label :for="`${uid}-project`">{{ copy.project }}</label>
              <select :id="`${uid}-project`" v-model="projectId" class="field"><option value="">{{ copy.chooseProject }}</option><option v-for="project in projectOptions" :key="project.id" :value="project.id">{{ project.routeKey }} · {{ project.title }}</option></select>
            </div>
            <p class="note">A visible Run once ticket is created in this project and excluded from release notes.</p>
            <p v-if="!can('nodes.write')" class="note">Creating a ticket requires node-write permission. An existing ticket can be selected instead.</p>
            <p v-if="projects.error" class="note bad" role="alert">{{ projects.error }} <button type="button" class="btn sm" @click="projects.load(true)">Retry projects</button></p>
          </div>
          <div v-if="!oneOff || !newTicket" class="ticket-field">
            <label :for="`${uid}-search`">Ticket</label>
            <div v-if="ticket" class="chosen-ticket">
              <AppIcon name="ticket" :size="18" />
              <div><span class="mono ticket-key">{{ ticket.key }}</span><strong>{{ ticket.title }}</strong></div>
              <button type="button" class="btn sm ghost" @click="changeTicket">Change<span class="sr-only"> ticket</span></button>
            </div>
            <template v-else>
              <input :id="`${uid}-search`" ref="searchInput" v-model="query" class="field" type="search" placeholder="Find a ticket by key or title" autocomplete="off" :aria-describedby="`${uid}-results`" />
              <div class="ticket-results" :aria-busy="searching">
                <p v-if="searching" :id="`${uid}-results`" class="note" role="status">Finding tickets…</p>
                <p v-else-if="searchError" :id="`${uid}-results`" class="note bad" role="alert">{{ searchError }} <button class="btn sm" type="button" @click="search()">Retry search</button></p>
                <p v-else-if="!tickets.length" :id="`${uid}-results`" class="note">No tickets found. Try another key or title.</p>
                <template v-else>
                  <p :id="`${uid}-results`" class="sr-only">Choose a ticket from the results.</p>
                  <button v-for="item in tickets" :key="item.id" type="button" class="ticket-result" @click="selectTicket(item)"><span class="mono">{{ item.key }}</span><span>{{ item.title }}</span><AppIcon name="arrow" :size="14" /></button>
                  <button v-if="nextCursor" type="button" class="btn sm more" @click="search(true)">More tickets</button>
                </template>
              </div>
            </template>
          </div>
          <div v-if="oneOff" class="instruction-field select-field">
            <label :for="`${uid}-instruction`">{{ copy.instruction }}</label>
            <textarea :id="`${uid}-instruction`" v-model="instruction" class="field instruction" maxlength="2000" rows="3" />
            <p class="note">{{ copy.instructionHelp }}</p>
          </div>
          <div v-if="oneOff" class="restriction-field">
            <label class="remember"><input v-model="mayChangeFiles" type="checkbox"> {{ copy.files }}</label>
          </div>
          <p v-if="oneOff" class="note restriction-note" role="status">{{ mayChangeFiles ? copy.writable : copy.readOnly }}</p>
          <div v-if="oneOff" class="run-settings-summary" :class="{ warn: settingsWarning.length }">
            <div class="run-summary-line"><p>{{ settingsSummary }}</p><button type="button" class="btn ghost settings-toggle" :aria-expanded="settingsOpen" :aria-controls="`${uid}-settings`" @click="toggleSettings">{{ settingsOpen ? copy.hide : copy.change }}</button></div>
            <p v-if="settingsWarning.length || lastSettingsWarning.length" class="note settings-status" :class="{ resolved: !settingsWarning.length }" :role="settingsWarning.length ? 'status' : undefined" :aria-hidden="!settingsWarning.length || undefined"><template v-for="(warning, index) in settingsWarning.length ? settingsWarning : lastSettingsWarning" :key="index">{{ index ? ' ' : '' }}<strong>{{ warning.label }}</strong><span> · {{ warning.detail }}</span></template></p>
            <button v-if="catalogGap === 'failed'" type="button" class="btn sm" @click="loadCatalog">Retry catalog</button>
          </div>
          <template v-if="dispatchMode === 'manual'">
          <div v-show="!oneOff || settingsOpen" :id="`${uid}-settings`" class="run-settings-fields" :aria-label="oneOff ? copy.settings : undefined">
          <p v-if="view.roleNote && role.source !== 'default'" class="note role-note">{{ view.roleNote }}</p>
          <div v-if="role.role === 'review-gate'" class="select-field">
            <label :for="`${uid}-family`">Author family</label>
            <select :id="`${uid}-family`" class="field" :value="authorFamily" @change="setFamily(($event.target as HTMLSelectElement).value)">
              <option value="">Choose the author family</option>
              <option v-for="family in AUTHOR_FAMILIES" :key="family" :value="family">{{ familyLabel(family) }}</option>
            </select>
            <p class="note">Review work excludes this family. Accounts load after it is chosen.</p>
          </div>
          <div class="pair">
          <div class="select-field">
            <label :for="`${uid}-host`">Host</label>
            <select :id="`${uid}-host`" ref="hostSelect" class="field" :value="choice.hostId" :disabled="cascadeLocked || !view.hosts.length" :aria-describedby="notes.host ? `${uid}-host-note` : undefined" @change="pick('host', ($event.target as HTMLSelectElement).value)">
              <option value="">{{ loading ? 'Loading accounts…' : 'Choose a host' }}</option>
              <option v-for="host in view.hosts" :key="host.value" :value="host.value">{{ host.label }}</option>
            </select>
            <p v-if="notes.host" :id="`${uid}-host-note`" class="note">{{ notes.host }}</p>
          </div>
          <div class="select-field">
            <label :for="`${uid}-harness`">Harness</label>
            <select :id="`${uid}-harness`" class="field" :value="choice.harness" :disabled="cascadeLocked || !choice.hostId" :aria-describedby="notes.harness ? `${uid}-harness-note` : undefined" @change="pick('harness', ($event.target as HTMLSelectElement).value)">
              <option value="">Choose a harness</option>
              <option v-for="item in view.harnesses" :key="item.value" :value="item.value">{{ item.label }}</option>
            </select>
            <p v-if="notes.harness" :id="`${uid}-harness-note`" class="note">{{ notes.harness }}</p>
          </div>
          </div>
          <div class="select-field">
            <label :for="`${uid}-account`">Account</label>
            <select :id="`${uid}-account`" class="field" :value="choice.accountId" :disabled="cascadeLocked || !choice.harness" :aria-describedby="notes.account ? `${uid}-account-note` : undefined" @change="pick('account', ($event.target as HTMLSelectElement).value)">
              <option value="">Choose an account</option>
              <option v-for="account in view.accounts" :key="account.value" :value="account.value">{{ accountLabel(account.label) }}</option>
            </select>
            <p v-if="notes.account" :id="`${uid}-account-note`" class="note">{{ notes.account }}</p>
            <label v-if="choice.accountId && !oneOff" class="remember"><input v-model="remember" type="checkbox"> Remember for this ticket</label>
          </div>
          <div class="pair" :class="{ 'pi-bound': piModelBound }">
          <div class="select-field">
            <label :for="`${uid}-model`">Model</label>
            <input v-if="piModelBound" :id="`${uid}-model`" class="field pinned-model" :value="view.models[0]?.label" :title="view.models[0]?.label" readonly :aria-describedby="`${uid}-pi-model-note`" />
            <select v-else :id="`${uid}-model`" class="field" :value="choice.modelKey" :disabled="cascadeLocked || !choice.accountId" :aria-describedby="notes.model ? `${uid}-model-note` : undefined" @change="pick('model', ($event.target as HTMLSelectElement).value)">
              <option value="">Choose a model</option>
              <option v-for="model in view.models" :key="model.value" :value="model.value">{{ model.label }}</option>
            </select>
            <p v-if="piModelBound" :id="`${uid}-pi-model-note`" class="note">Model is set in Settings, under Accounts.</p>
            <p v-if="notes.model" :id="`${uid}-model-note`" class="note">{{ notes.model }}</p>
          </div>
          <div class="select-field">
            <label :for="`${uid}-effort`">Thinking</label>
            <select :id="`${uid}-effort`" class="field" :value="choice.profileId" :disabled="cascadeLocked || !choice.modelKey" :aria-describedby="notes.effort ? `${uid}-effort-note` : undefined" @change="pick('effort', ($event.target as HTMLSelectElement).value)">
              <option value="">Choose a thinking level</option>
              <option v-for="effort in view.efforts" :key="effort.value" :value="effort.value">{{ effort.label }}</option>
            </select>
            <p v-if="notes.effort" :id="`${uid}-effort-note`" class="note">{{ notes.effort }}</p>
          </div>
          </div>
          </div>
          </template>
        </fieldset>

        <div v-if="dispatchMode === 'lead'" class="lead-notice" role="status">
          <strong>This project starts work through its {{ w.l }}.</strong>
          <p>{{ queuedForLead ? `Queued for the ${w.l}. Nothing has started yet.` : `Queue this ticket for the ${w.l}; it chooses the host, harness, account, model and thinking when dispatching work.` }}</p>
          <p v-if="!canLaunchLead(projectLead)">{{ leadLaunchReason(projectLead) }} Queued work stays queued.</p>
          <p v-else-if="projectLead?.state === 'paused'">The {{ w.l }} is paused. Queued work waits until it resumes.</p>
          <p v-if="!mayQueueForLead">Queueing for the {{ w.l }} requires a person with permission to queue work in this project.</p>
        </div>
        <p v-else-if="dispatchMode === 'checking'" class="note" role="status">Checking how this project starts work…</p>
        <div v-else-if="dispatchMode === 'failed'" class="error" role="alert"><p>{{ dispatchError }}</p><button type="button" class="btn sm" @click="checkDispatch">Retry start settings</button></div>
        <div v-if="!oneOff && dispatchMode === 'manual' && !loading && !catalogGap && catalog" class="dispatch-status" :class="{ warn: view.status.tone === 'warn' }" role="status"><AppIcon :name="view.status.tone === 'warn' ? 'clock' : 'info'" :size="17" /><div><strong>{{ view.status.label }}</strong><p>{{ view.status.detail }}</p></div></div>
        <div v-if="!oneOff && dispatchMode === 'manual' && (catalogGap === 'failed' || catalogGap === 'forbidden')" class="error" role="alert"><p>{{ catalogMessage }}</p><button v-if="catalogGap === 'failed'" type="button" class="btn sm" @click="loadCatalog">Retry catalog</button></div>
        <div v-if="error" class="error" role="alert"><p>{{ error }}</p><button v-if="grantStale" type="button" class="btn sm" @click="loadCatalog">Refresh catalog</button></div>
        <p v-if="dispatchMode === 'manual' && !permitted" class="note">Starting an agent requires work-order write and run-create permission.</p>
        </div>
        <footer><button type="button" class="btn" :disabled="busy" @click="close"><span class="queue-label"><span>{{ dismissLabel }}</span><span aria-hidden="true">Cancel</span><span aria-hidden="true">Close</span></span><KeyCap k="Esc" class="hint" aria-hidden="true" /></button><button v-if="dispatchMode === 'manual' && mayRunNow && selectedWait?.run_now_allowed" type="button" class="btn" :disabled="!canSubmit" @click="submit(true)">{{ copy.skipWait }}</button><button type="submit" class="btn primary" :disabled="dispatchMode === 'lead' ? !canQueueForLead : !canSubmit" :aria-keyshortcuts="mac ? 'Meta+Enter' : 'Control+Enter'"><AppIcon :name="submitIcon" :size="15" /><span class="queue-label"><span>{{ dispatchMode === 'lead' ? queuedForLead ? 'Queued' : busy ? 'Queueing…' : `Queue for ${w.l}` : busy ? copy.queueing : copy.queue }}</span><span aria-hidden="true">Queue for {{ w.l }}</span><span aria-hidden="true">{{ copy.queue }}</span><span aria-hidden="true">{{ copy.queueing }}</span></span><KeyCap k="mod" class="hint" aria-hidden="true" /><KeyCap k="enter" class="hint" aria-hidden="true" /></button></footer>
      </template>

      <template v-else>
        <div class="launch-body">
        <div class="run-result" data-result tabindex="-1" role="status">
          <span class="result-icon"><AppIcon :name="managed ? 'check' : 'clock'" :size="24" /></span>
          <p class="eyebrow">{{ reused ? 'Existing run' : 'Run created' }}</p>
          <h3>{{ sessionConnected ? 'Managed session connected' : state?.label }}</h3>
          <p>{{ sessionConnected ? 'The daemon registered this session. Open it to follow progress and send controls.' : state?.detail }}</p>
          <div v-if="ticket" class="result-ticket"><span class="mono">{{ ticket.key }}</span><strong>{{ ticket.title }}</strong></div>
          <div v-if="requested" class="requested">
            <p class="requested-title">Requested configuration</p>
            <ul>
              <li><span>Host</span><strong>{{ requested.host }}</strong></li>
              <li><span>Harness</span><strong>{{ requested.harness }}</strong></li>
              <li><span>Account</span><strong>{{ requested.account }}</strong></li>
              <li><span>Model</span><strong>{{ requested.model }}</strong></li>
              <li><span>Thinking</span><strong>{{ requested.thinking }}</strong></li>
            </ul>
          </div>
          <div v-if="reported" class="requested">
            <p class="requested-title">Reported by the session</p>
            <ul>
              <li><span>Account</span><strong>{{ reported.account }}</strong></li>
              <li><span>Model</span><strong>{{ reported.model }}</strong></li>
              <li><span>Thinking</span><strong>{{ reported.thinking }}</strong></li>
            </ul>
            <p v-if="reported.differs" class="note">This differs from the requested configuration.</p>
            <p v-else-if="!reported.modelKnown || !reported.accountKnown || !reported.thinkingKnown" class="note">The session has not reported every field.</p>
          </div>
          <p class="note mono">Run {{ run.id.slice(0, 8) }}</p>
          <p v-if="run.run_once" class="note">{{ run.repository_mutation_allowed ? 'May change files' : 'Read-only · no tools or repository access' }}</p>
        </div>
        <button v-if="mayRunNow && run.status === 'queued' && run.wait?.run_now_allowed" type="button" class="btn" :disabled="busy" @click="overrideQueued">{{ copy.skipWait }}</button>
        <p v-if="checkError" class="error" role="alert">{{ checkError }}</p>
        </div>
        <footer class="result-footer"><button type="button" class="btn ghost" :disabled="checking" @click="refresh"><AppIcon name="refresh" :size="14" />Refresh status</button><button type="button" class="btn" :disabled="busy" @click="close">Close</button><RouterLink class="btn primary" :to="managed ? `/agents/${managed.id}` : '/agents'" @click="close">{{ managed ? 'Open session' : 'Go to Agents' }}<AppIcon name="arrow" :size="15" /></RouterLink></footer>
      </template>
    </form>
  </dialog>
</template>

<style scoped>
.launch-dialog { position: fixed; inset: 16px auto auto 50%; transform: translateX(-50%); margin: 0; width: min(var(--dialog-l), calc(100vw - 24px)); max-height: calc(100dvh - 32px); padding: 0; border: 1px solid var(--line-2); border-radius: 22px; background: var(--surface-raised); color: var(--ink); box-shadow: 0 24px 80px var(--scrim); overflow: hidden; }
.launch-dialog::backdrop { background: var(--scrim); backdrop-filter: blur(5px); }
.launch-card { padding: 22px 24px 20px; display: flex; flex-direction: column; max-height: calc(100dvh - 34px); min-height: 0; }
.launch-head { order: 0; flex-shrink: 0; }
.launch-body { order: 2; overflow-y: auto; scrollbar-gutter: stable; min-height: 0; }
.launch-card > footer { order: 1; flex-shrink: 0; margin: 0 0 16px; padding: 0 0 16px; border-top: 0; border-bottom: 1px solid var(--line); }
.new-ticket { display: grid; gap: 14px; }
.instruction.field { height: auto; resize: none; }
.one-off .restriction-note { min-block-size: 3em; }
.run-settings-fields { display: grid; gap: 16px; min-width: 0; }
.run-settings-summary { padding-block: 12px; border-block: 1px solid var(--line); }
.run-summary-line { display: flex; align-items: center; gap: 12px; }
.run-summary-line p { flex: 1; min-width: 0; margin: 0; font-size: 13px; line-height: 1.6; overflow-wrap: anywhere; }
.settings-toggle { flex-shrink: 0; min-height: 44px; }
.settings-status { min-block-size: 3em; margin: 4px 0 0; }
.settings-status strong { font-weight: 600; }
.settings-status.resolved { visibility: hidden; }
.run-settings-summary.warn .settings-status { color: var(--ink-2); }
.queue-label { display: grid; }
.queue-label > span { grid-area: 1 / 1; }
.queue-label .inactive { visibility: hidden; }
.launch-head { display: flex; gap: 12px; align-items: center; margin-bottom: 18px; }
.lead-notice { margin-top: 18px; font-size: 14px; line-height: 1.6; }
.lead-notice p { margin-top: 8px; color: var(--ink-2); }
.queue-label > [aria-hidden] { visibility: hidden; }
.launch-head h2 { font-size: 21px; letter-spacing: -.4px; }
.launch-head .icon-btn { margin-left: auto; flex-shrink: 0; }
.result-icon { display: grid; place-items: center; width: 48px; height: 48px; border-radius: 14px; background: var(--row-selected); color: var(--teal-ink); flex-shrink: 0; }
.launch-fields { border: 0; padding: 0; margin: 0; display: grid; gap: 16px; min-width: 0; }
.pair { display: grid; grid-template-columns: minmax(0, 1fr) minmax(0, 1fr); gap: 16px; align-items: start; }
label { display: block; margin-bottom: 7px; font-size: 12px; font-weight: 650; color: var(--ink-2); }
.field { width: 100%; height: 44px; min-width: 0; font-size: 14px; }
.chosen-ticket { display: flex; align-items: center; gap: 12px; background: var(--surface-sunken); border: 1px solid var(--line); padding: 13px; border-radius: 12px; }
.chosen-ticket > svg { color: var(--teal-ink); flex-shrink: 0; }
.chosen-ticket > div { min-width: 0; flex: 1; }
.chosen-ticket strong { display: block; font-size: 14px; line-height: 1.4; overflow-wrap: anywhere; margin-top: 4px; }
.ticket-key { font-size: 11px; color: var(--teal-ink); }
.ticket-results { max-height: 180px; overflow-y: auto; border: 1px solid var(--line); border-radius: 10px; margin-top: 8px; }
.ticket-result { display: flex; align-items: center; gap: 10px; width: 100%; padding: 10px 12px; min-height: 44px; border: 0; background: transparent; color: var(--ink); text-align: left; font-size: 13px; }
.ticket-result > .mono { font-size: 11px; color: var(--teal-ink); flex-shrink: 0; }
.ticket-result > span:nth-child(2) { flex: 1; min-width: 0; overflow-wrap: anywhere; }
.ticket-result > svg { flex-shrink: 0; }
.ticket-result:hover { background: var(--row-hover); }
.ticket-result:focus-visible { outline: 2px solid var(--teal); outline-offset: -2px; background: var(--row-selected); }
.ticket-results > .note { padding: 12px; }
.more { margin: 8px 12px; }
.note { font-size: 12px; line-height: 1.5; color: var(--ink-2); margin-top: 6px; }
.remember { display: flex; align-items: center; gap: 8px; margin: 8px 0 0; font-size: 13px; font-weight: 500; color: var(--ink-2); }
.remember input { margin: 0; }
.role-note { margin-top: 0; }
.dispatch-status { display: flex; gap: 10px; align-items: flex-start; font-size: 13px; line-height: 1.5; }
.dispatch-status > svg { margin-top: 2px; flex-shrink: 0; }
.dispatch-status { margin-top: 18px; padding: 12px 14px; border: 1px solid var(--line); border-radius: 12px; background: var(--surface-sunken); }
.dispatch-status p { color: var(--ink-2); margin-top: 3px; font-size: 12px; }
.dispatch-status.warn { background: var(--surface-sunken); }
.error { color: var(--danger); background: var(--danger-bg); border-radius: 10px; padding: 12px; margin-top: 14px; font-size: 13px; line-height: 1.5; }
.error .btn { margin-top: 8px; }
.bad { color: var(--danger); }
footer { order: 1; display: flex; flex: none; flex-wrap: wrap; justify-content: flex-end; align-items: center; gap: 8px; margin-bottom: 16px; padding-bottom: 16px; border-bottom: 1px solid var(--line); }
footer > p { flex: 1; font-size: 12px; color: var(--ink-2); line-height: 1.5; }
@media (pointer: coarse), (max-width: 600px) { footer .btn, .remember { min-height: 44px; } }
.run-result { display: grid; justify-items: center; text-align: center; padding: 14px 0; gap: 12px; outline: none; }
.run-result h3 { font-size: 25px; letter-spacing: -.5px; }
.run-result > p:not(.eyebrow) { color: var(--ink-2); font-size: 14px; line-height: 1.6; max-width: 410px; }
.result-icon { width: 58px; height: 58px; border-radius: 50%; margin-bottom: 6px; }
.result-ticket { width: 100%; padding: 14px; border-radius: 12px; background: var(--surface-sunken); }
.result-ticket span { display: block; font-size: 11px; color: var(--teal-ink); margin-bottom: 6px; }
.result-ticket strong { font-size: 14px; overflow-wrap: anywhere; }
.requested { width: 100%; margin: 0; padding: 12px 14px; text-align: left; background: var(--surface-sunken); border-radius: 12px; }
.requested-title { margin: 0 0 8px; font-size: 12px; font-weight: 650; color: var(--ink-2); }
.requested ul { list-style: none; margin: 0; padding: 0; display: grid; gap: 8px; }
.requested li { display: flex; justify-content: space-between; gap: 12px; font-size: 13px; }
.requested span { color: var(--ink-3); flex-shrink: 0; }
.requested strong { font-weight: 600; text-align: right; overflow-wrap: anywhere; }
.requested .note { margin-top: 8px; }
.pair.pi-bound { grid-template-columns: minmax(0, 1fr) 130px; }
.field.pinned-model { font-family: var(--mono); font-size: 12px; text-overflow: ellipsis; }
@media (max-width: 600px) {
  .launch-dialog { inset: 0; transform: none; max-height: 100dvh; height: 100dvh; width: 100%; max-width: 100vw; border-radius: 0; }
  .launch-dialog.run-once { max-width: none; }
  .launch-card { padding: max(16px, env(safe-area-inset-top)) 16px max(16px, env(safe-area-inset-bottom)); height: 100%; max-height: 100dvh; }
  .launch-body { flex: 1; }
  .launch-card > footer { order: 3; margin: 14px 0 0; padding: 14px 0 0; border-bottom: 0; border-top: 1px solid var(--line); }
  .pair, .pair.pi-bound { grid-template-columns: minmax(0, 1fr); gap: 14px; }
  .launch-head { margin-bottom: 14px; }
  .launch-fields, .run-settings-fields { gap: 14px; }
  .dispatch-status { margin-top: 16px; }
  footer { order: 3; margin: 16px 0 0; padding: 16px 0 0; border-bottom: 0; border-top: 1px solid var(--line); }
  .launch-head .icon-btn { width: 44px; height: 44px; }
  .chosen-ticket { flex-wrap: wrap; }
  .chosen-ticket .btn { min-height: 44px; }
  footer:not(.result-footer) .btn { flex: 1; }
  .result-footer { display: grid; grid-template-columns: 1fr 1fr; }
  .result-footer > :first-child { grid-column: 1 / -1; justify-self: center; }
  .field { font-size: 16px; }
}
</style>
