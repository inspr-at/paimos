<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, useId, watch } from 'vue'
import { listNodes, type WorkNode } from '../../lib/api'
import { can } from '../../lib/authz'
import { useSession } from '../../stores/session'
import { deletePin, listPins, putPin, message, type AgentRun, type HarnessSession } from '../../lib/agents'
import { getRun, listAllSessions, runNowOnce } from '../../lib/agentRows'
import {
  AUTHOR_FAMILIES, authorFamilyFor, chooseStep, emptyChoice, emptyTouch, familyLabel, fetchAccountCatalog, fillDefaults, presentCascade, sessionReport, workRoleFor,
  type AgentAccountCatalog, type AuthorFamily, type CascadeChoice, type CascadeStep, type CascadeTouch, type CatalogGap, type RequestedRun,
} from '../../lib/accountCascade'
import { wroteSince, writeMark } from '../../lib/position'
import { launchState, staleGrantRejection, startAgent } from '../../lib/startAgent'
import { useAgents } from '../../stores/agents'
import { usePoller } from '../../lib/usePolledData'
import AppIcon from '../AppIcon.vue'

// Shared by /agents and TicketWorkspace. The cascade is host, harness, account,
// model, then thinking. Each later step is filled only from the catalog grant.
const agents = useAgents()
const session = useSession()
const uid = useId()
const dialog = ref<HTMLDialogElement>()
const searchInput = ref<HTMLInputElement>()
const hostSelect = ref<HTMLSelectElement>()
const tickets = ref<WorkNode[]>([])
const ticket = ref<WorkNode | null>(null)
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
let searchTimer: ReturnType<typeof setTimeout> | undefined
const poll = usePoller(() => { now.value = Date.now(); if (run.value) return refresh() }, 3000, { enabled: () => visible.value && !!run.value })
const role = computed(() => workRoleFor(ticket.value))
const view = computed(() => presentCascade({ catalog: catalog.value, role: role.value, authorFamily: authorFamily.value }, choice.value, touch.value))
const piModelBound = computed(() => choice.value.harness === 'pi' && view.value.models.length === 1)
const cascadeLocked = computed(() => loading.value || catalogGap.value === 'failed' || catalogGap.value === 'forbidden' || catalogGap.value === 'family')
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
const mayRunNow = computed(() => permitted.value && session.identity?.principal.kind === 'person')
const canSubmit = computed(() => permitted.value && !loading.value && !busy.value && !run.value && ticket.value?.is_leaf === true && !catalogGap.value && !!view.value.agentId && selectionVisible.value)

async function search(more = false) {
  const turn = ++searchGeneration
  searching.value = true; searchError.value = ''
  try {
    const page = await listNodes({ kind: ['work'], shape: ['leaf'], q: query.value.trim(), limit: 30, sort: '-updated_at', ...(more && nextCursor.value ? { cursor: nextCursor.value } : {}) })
    if (turn !== searchGeneration || !visible.value) return
    const leaves = page.items.filter(item => item.is_leaf === true)
    tickets.value = more ? [...tickets.value, ...leaves] : leaves
    nextCursor.value = page.next_cursor
  } catch (e) { if (turn === searchGeneration) { searchError.value = message(e); tickets.value = []; nextCursor.value = null } }
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
function selectTicket(item: WorkNode) {
  if (item.is_leaf !== true) return
  ticket.value = item
  authorFamily.value = authorFamilyFor(item)
  touch.value = emptyTouch()
  choice.value = emptyChoice()
  remember.value = false
  remembered.value = ''
  void loadCatalog()
}
let pinGeneration = 0
watch([() => ticket.value?.id, () => choice.value.harness, catalog], async () => {
  const id = ticket.value?.id
  const harness = choice.value.harness
  const turn = ++pinGeneration
  if (!visible.value || !id || !harness) return
  try {
    const pins = await listPins(id)
    if (turn !== pinGeneration || !visible.value || ticket.value?.id !== id || choice.value.harness !== harness) return
    const pin = pins.find(item => item.harness === harness && item.account_id)
    if (!pin?.account_id || touch.value.account || !view.value.accounts.some(account => account.value === pin.account_id)) return
    remembered.value = pin.account_id
    pick('account', pin.account_id)
    remember.value = true
  } catch { /* A missing pin leaves the checkbox clear. */ }
})
async function open(initial?: WorkNode) {
  if (busy.value) return
  opener = document.activeElement as HTMLElement
  generation++; visible.value = true
  ticket.value = initial?.is_leaf === true ? initial : null; query.value = ''; tickets.value = []; nextCursor.value = null
  authorFamily.value = authorFamilyFor(ticket.value)
  catalog.value = null; catalogGap.value = null; catalogMessage.value = ''
  choice.value = emptyChoice(); touch.value = emptyTouch()
  remember.value = false; remembered.value = ''
  runId.value = ''; requested.value = null; managedId.value = ''; reused.value = false
  error.value = ''; checkError.value = ''; searchError.value = ''; grantStale.value = false
  dialog.value?.showModal()
  void loadCatalog()
  if (!ticket.value) void search()
  poll.start()
  await nextTick()
  if (ticket.value) hostSelect.value?.focus()
  else searchInput.value?.focus()
}
function close() {
  if (busy.value) return
  generation++; searchGeneration++; catalogGeneration++; visible.value = false
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
  void loadCatalog()
  void search()
  void nextTick(() => searchInput.value?.focus())
}
async function submit(runNow = false) {
  if (!canSubmit.value || !ticket.value) return
  busy.value = true; error.value = ''
  const pinned = { ...view.value.requested }
  const mark = writeMark()
  try {
    if (choice.value.accountId && remember.value) await putPin({ ticket_id: ticket.value.id, harness: choice.value.harness, account_id: choice.value.accountId })
    else if (remembered.value && !remember.value) await deletePin(ticket.value.id, choice.value.harness)
    const result = await startAgent({ ticket: ticket.value, agentId: view.value.agentId, profileId: view.value.profileId, accountId: choice.value.accountId, runNow }, { run: agents.admitRun, runs: agents.admitRuns })
    runId.value = result.run.id; reused.value = result.reused; requested.value = pinned
    void agents.afterWrite()
    await refresh()
    await nextTick(); dialog.value?.querySelector<HTMLElement>('[data-result]')?.focus()
  } catch (e) {
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
  finally { busy.value = false }
}
async function overrideQueued() {
  if (!run.value || busy.value) return
  busy.value = true; checkError.value = ''
  try { agents.admitRun(await runNowOnce(run.value.id)); void agents.afterWrite() }
  catch (e) { checkError.value = message(e) }
  finally { busy.value = false }
}
async function refresh() {
  if (!run.value || checking.value) return
  const turn = generation, id = run.value.id
  checking.value = true
  try {
    agents.admitRun(await getRun(id))
    if (turn !== generation || !visible.value) return
    checkError.value = ''
    const agentId = agents.runs[id]?.agent_principal_id
    if (!agentId) return
    const page = await listAllSessions({ agent: agentId, limit: 200 })
    if (turn !== generation || !visible.value) return
    managedId.value = agents.admitSessions(page.items).find(s => s.run_id === id && s.management_mode === 'managed')?.id ?? ''
    if (managedId.value) void agents.refreshSessions()
  } catch { if (turn === generation) checkError.value = 'Status could not be refreshed. The last reported state is shown.' }
  finally { checking.value = false }
}
onBeforeUnmount(() => { narrowQuery?.removeEventListener('change', onNarrow); generation++; searchGeneration++; catalogGeneration++; poll.stop(); clearTimeout(searchTimer) })
defineExpose({ open })
</script>

<template>
  <dialog ref="dialog" class="launch-dialog" :aria-labelledby="`${uid}-title`" @cancel.prevent="close">
    <form class="launch-card" @submit.prevent="submit()">
      <header class="launch-head">
        <h2 :id="`${uid}-title`">Start agent</h2>
        <button type="button" class="icon-btn flat" aria-label="Close start agent" :disabled="busy" @click="close"><AppIcon name="close" /></button>
      </header>

      <template v-if="!run">
        <fieldset :disabled="busy" class="launch-fields">
          <legend class="sr-only">Run setup</legend>
          <div class="ticket-field">
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
            <label v-if="choice.accountId" class="remember"><input v-model="remember" type="checkbox"> Remember for this ticket</label>
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
        </fieldset>

        <div v-if="!loading && !catalogGap && catalog" class="dispatch-status" :class="{ warn: view.status.tone === 'warn' }" role="status"><AppIcon :name="view.status.tone === 'warn' ? 'clock' : 'info'" :size="17" /><div><strong>{{ view.status.label }}</strong><p>{{ view.status.detail }}</p></div></div>
        <div v-if="catalogGap === 'failed' || catalogGap === 'forbidden'" class="error" role="alert"><p>{{ catalogMessage }}</p><button v-if="catalogGap === 'failed'" type="button" class="btn sm" @click="loadCatalog">Retry catalog</button></div>
        <div v-if="error" class="error" role="alert"><p>{{ error }}</p><button v-if="grantStale" type="button" class="btn sm" @click="loadCatalog">Refresh catalog</button></div>
        <p v-if="!permitted" class="note">Starting an agent requires work-order write and run-create permission.</p>
        <footer><button type="button" class="btn" :disabled="busy" @click="close">Cancel</button><button v-if="mayRunNow && selectedWait?.run_now_allowed" type="button" class="btn" :disabled="!canSubmit" @click="submit(true)">Run now once</button><button type="submit" class="btn primary" :disabled="!canSubmit"><AppIcon :name="busy ? 'clock' : 'arrow'" :size="15" />{{ busy ? 'Queueing…' : 'Queue run' }}</button></footer>
      </template>

      <template v-else>
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
        </div>
        <button v-if="mayRunNow && run.status === 'queued' && run.wait?.run_now_allowed" type="button" class="btn" :disabled="busy" @click="overrideQueued">Run now once</button>
        <p v-if="checkError" class="error" role="alert">{{ checkError }}</p>
        <footer class="result-footer"><button type="button" class="btn ghost" :disabled="checking" @click="refresh"><AppIcon name="refresh" :size="14" />Refresh status</button><button type="button" class="btn" :disabled="busy" @click="close">Close</button><RouterLink class="btn primary" :to="managed ? `/agents/${managed.id}` : '/agents'" @click="close">{{ managed ? 'Open session' : 'Go to Agents' }}<AppIcon name="arrow" :size="15" /></RouterLink></footer>
      </template>
    </form>
  </dialog>
</template>

<style scoped>
.launch-dialog { width: min(var(--dialog-l), calc(100vw - 24px)); max-height: calc(100dvh - 32px); padding: 0; border: 1px solid var(--line-2); border-radius: 22px; background: var(--surface-raised); color: var(--ink); box-shadow: 0 24px 80px var(--scrim); overflow: auto; scrollbar-gutter: stable; }
.launch-dialog::backdrop { background: var(--scrim); backdrop-filter: blur(5px); }
.launch-card { padding: 22px 24px 20px; }
.launch-head { display: flex; gap: 12px; align-items: center; margin-bottom: 18px; }
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
footer { display: flex; flex-wrap: wrap; justify-content: flex-end; align-items: center; gap: 8px; margin-top: 20px; padding-top: 16px; border-top: 1px solid var(--line); }
footer > p { flex: 1; font-size: 12px; color: var(--ink-2); line-height: 1.5; }
@media (pointer: coarse), (max-width: 600px) { footer .btn { min-height: 44px; } }
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
  .launch-dialog { max-height: calc(100dvh - 16px); width: calc(100vw - 16px); border-radius: 18px; }
  .launch-card { padding: 20px 16px; }
  .pair, .pair.pi-bound { grid-template-columns: minmax(0, 1fr); gap: 14px; }
  .launch-head { margin-bottom: 14px; }
  .launch-fields { gap: 14px; }
  .dispatch-status { margin-top: 16px; }
  footer { margin-top: 18px; padding-top: 16px; }
  .launch-head .icon-btn { width: 44px; height: 44px; }
  .chosen-ticket { flex-wrap: wrap; }
  .chosen-ticket .btn { min-height: 44px; }
  footer:not(.result-footer) .btn { flex: 1; }
  .result-footer { display: grid; grid-template-columns: 1fr 1fr; }
  .result-footer > :first-child { grid-column: 1 / -1; justify-self: center; }
  .field { font-size: 16px; }
}
</style>
