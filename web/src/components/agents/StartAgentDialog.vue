<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, useId, watch } from 'vue'
import { listNodes, type WorkNode } from '../../lib/api'
import { can } from '../../lib/authz'
import { getRun, listAccounts, listAllSessions, listModels, message, type AgentAccount, type AgentRun, type HarnessSession, type ModelProfile } from '../../lib/agents'
import { listPrincipals, type Principal } from '../../lib/business'
import { dispatchHint, launchState, startAgent } from '../../lib/startAgent'
import { useAgents } from '../../stores/agents'
import { usePoller } from '../../lib/usePolledData'
import AppIcon from '../AppIcon.vue'

// Shared by /agents and TicketWorkspace. Open with a ticket to preselect it.
// Queueing is separate from claiming: never synthesize a managed session.
const agents = useAgents()
const uid = useId()
const dialog = ref<HTMLDialogElement>()
const searchInput = ref<HTMLInputElement>()
const tickets = ref<WorkNode[]>([])
const ticket = ref<WorkNode | null>(null)
const query = ref('')
const searching = ref(false)
const searchError = ref('')
const nextCursor = ref<string | null>(null)
const principals = ref<Principal[]>([])
const profiles = ref<ModelProfile[]>([])
const accounts = ref<AgentAccount[]>([])
const agentId = ref('')
const profileId = ref('')
const accountId = ref('')
const loading = ref(false)
const busy = ref(false)
const error = ref('')
const run = ref<AgentRun | null>(null)
const managed = ref<HarnessSession | null>(null)
const reused = ref(false)
const checking = ref(false)
const checkError = ref('')
const now = ref(Date.now())
const visible = ref(false)
let opener: HTMLElement | null = null
let generation = 0
let searchGeneration = 0
let searchTimer: ReturnType<typeof setTimeout> | undefined
const poll = usePoller(() => { now.value = Date.now(); if (run.value) return refresh() }, 3000, { enabled: () => visible.value && !!run.value })
const profile = computed(() => profiles.value.find(p => p.id === profileId.value))
const matchingAccounts = computed(() => accounts.value.filter(a => a.registered_by_principal_id === agentId.value && a.harness === profile.value?.harness))
const hint = computed(() => dispatchHint(accounts.value, agentId.value, profile.value, now.value, accountId.value))
watch([agentId, profileId], () => { accountId.value = '' })
const state = computed(() => run.value ? launchState(run.value) : null)
const sessionConnected = computed(() => !!managed.value && managed.value.phase !== 'stopped')
const permitted = computed(() => can('work_orders.write') && can('run.create'))
const canSubmit = computed(() => permitted.value && !loading.value && !busy.value && !run.value && !!ticket.value && !!agentId.value && !!profile.value)

async function search(more = false) {
  const turn = ++searchGeneration
  searching.value = true; searchError.value = ''
  try {
    const page = await listNodes({ kind: ['ticket'], q: query.value.trim(), limit: 30, sort: '-updated_at', ...(more && nextCursor.value ? { cursor: nextCursor.value } : {}) })
    if (turn !== searchGeneration || !visible.value) return
    tickets.value = more ? [...tickets.value, ...page.items] : page.items
    nextCursor.value = page.next_cursor
  } catch (e) { if (turn === searchGeneration) { searchError.value = message(e); tickets.value = []; nextCursor.value = null } }
  finally { if (turn === searchGeneration) searching.value = false }
}
watch(query, () => { clearTimeout(searchTimer); searchTimer = setTimeout(() => void search(), 200) })
async function loadOptions() {
  const turn = generation
  loading.value = true; error.value = ''
  try {
    const [people, models, pool] = await Promise.all([listPrincipals(), listModels(), listAccounts()])
    if (turn !== generation || !visible.value) return
    principals.value = people.filter(p => p.kind === 'agent' && !p.roles.includes('system'))
    profiles.value = models.filter(p => p.enabled)
    accounts.value = pool
    if (!principals.value.some(p => p.id === agentId.value)) agentId.value = ''
    if (!profiles.value.some(p => p.id === profileId.value)) profileId.value = ''
  } catch (e) { if (turn === generation) error.value = message(e) }
  finally { if (turn === generation) loading.value = false }
}
async function open(initial?: WorkNode) {
  if (busy.value) return
  opener = document.activeElement as HTMLElement
  generation++; visible.value = true
  ticket.value = initial ?? null; query.value = ''; tickets.value = []; nextCursor.value = null
  agentId.value = ''; profileId.value = ''; accountId.value = ''; run.value = null; managed.value = null; reused.value = false
  error.value = ''; checkError.value = ''; searchError.value = ''
  principals.value = []; profiles.value = []; accounts.value = []
  dialog.value?.showModal()
  void loadOptions()
  if (!initial) void search()
  poll.start()
  await nextTick()
  if (initial) dialog.value?.querySelector<HTMLSelectElement>('select')?.focus()
  else searchInput.value?.focus()
}
function close() {
  if (busy.value) return
  generation++; searchGeneration++; visible.value = false
  poll.stop(); clearTimeout(searchTimer)
  dialog.value?.close(); opener?.focus({ preventScroll: true })
}
function changeTicket() { ticket.value = null; void search(); void nextTick(() => searchInput.value?.focus()) }
async function submit() {
  if (!canSubmit.value || !ticket.value) return
  busy.value = true; error.value = ''
  try {
    const result = await startAgent({ ticket: ticket.value, agentId: agentId.value, profileId: profileId.value, accountId: accountId.value || undefined })
    run.value = result.run; reused.value = result.reused
    agents.recordRun(result.run)
    await refresh()
    await nextTick(); dialog.value?.querySelector<HTMLElement>('[data-result]')?.focus()
  } catch (e) { error.value = `${message(e)} Your selections are kept. Retrying checks for an existing work order and run first.` }
  finally { busy.value = false }
}
async function refresh() {
  if (!run.value || checking.value) return
  const turn = generation, runId = run.value.id
  checking.value = true
  try {
    const current = await getRun(runId)
    if (turn !== generation || !visible.value) return
    run.value = current; agents.recordRun(current); checkError.value = ''
    const page = await listAllSessions({ agent: current.agent_principal_id, limit: 200 })
    if (turn !== generation || !visible.value) return
    managed.value = page.items.find(s => s.run_id === runId && s.management_mode === 'managed') ?? null
    if (managed.value) void agents.refreshSessions()
  } catch { if (turn === generation) checkError.value = 'Status could not be refreshed. The last reported state is shown.' }
  finally { checking.value = false }
}
onBeforeUnmount(() => { generation++; searchGeneration++; poll.stop(); clearTimeout(searchTimer) })
defineExpose({ open })
</script>

<template>
  <dialog ref="dialog" class="launch-dialog" :aria-labelledby="`${uid}-title`" @cancel.prevent="close">
    <form class="launch-card" @submit.prevent="submit">
      <header class="launch-head">
        <span class="launch-icon"><AppIcon name="agent" :size="22" /></span>
        <div><p class="eyebrow">Managed session</p><h2 :id="`${uid}-title`">Start agent</h2></div>
        <button type="button" class="icon-btn flat" aria-label="Close start agent" :disabled="busy" @click="close"><AppIcon name="close" /></button>
      </header>
      <p class="intro">Give a ticket to an agent. Its daemon starts a fresh session when an account is ready.</p>

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
                  <button v-for="item in tickets" :key="item.id" type="button" class="ticket-result" @click="ticket = item"><span class="mono">{{ item.key }}</span><span>{{ item.title }}</span><AppIcon name="arrow" :size="14" /></button>
                  <button v-if="nextCursor" type="button" class="btn sm more" @click="search(true)">More tickets</button>
                </template>
              </div>
            </template>
          </div>
          <div class="select-field">
            <label :for="`${uid}-agent`">Agent</label>
            <select :id="`${uid}-agent`" v-model="agentId" class="field" :disabled="loading || !principals.length"><option value="">{{ loading ? 'Loading agents…' : 'Choose an agent' }}</option><option v-for="person in principals" :key="person.id" :value="person.id">{{ person.name }}</option></select>
            <p v-if="!loading && !principals.length && !error" class="note">No agent principals available. Set up an agent in Access first.</p>
          </div>
          <div class="select-field">
            <label :for="`${uid}-model`">Model profile</label>
            <select :id="`${uid}-model`" v-model="profileId" class="field" :disabled="loading || !profiles.length"><option value="">{{ loading ? 'Loading profiles…' : 'Choose a model profile' }}</option><option v-for="model in profiles" :key="model.id" :value="model.id">{{ model.slug }} · {{ model.harness }}</option></select>
            <p v-if="profile" class="note">{{ profile.model }} · {{ profile.effort }} effort</p>
            <p v-if="!loading && !profiles.length && !error" class="note">No enabled model profiles. Enable one in model settings first.</p>
          </div>
          <div class="select-field">
            <label :for="`${uid}-account`">Account <span class="optional">optional</span></label>
            <select :id="`${uid}-account`" v-model="accountId" class="field" :disabled="loading || !agentId || !profileId" :aria-describedby="`${uid}-account-note`"><option value="">Automatic · daemon chooses</option><option v-for="account in matchingAccounts" :key="account.id" :value="account.id">{{ account.label }} · {{ account.daemon_id }}{{ account.state === 'available' ? '' : ` · ${account.state}` }}</option></select>
            <p :id="`${uid}-account-note`" class="note">{{ accountId ? 'Only this account will be used. The run waits if it has no capacity.' : 'The daemon chooses from its matching enrolled accounts.' }}</p>
          </div>
        </fieldset>

        <div v-if="!loading && !error" class="dispatch-status" :class="{ warn: hint.tone === 'warn' }" role="status"><AppIcon :name="hint.tone === 'warn' ? 'clock' : 'info'" :size="17" /><div><strong>{{ hint.label }}</strong><p>{{ hint.detail }}</p></div></div>
        <div v-if="error" class="error" role="alert"><p>{{ error }}</p><button v-if="!principals.length || !profiles.length" type="button" class="btn sm" @click="loadOptions">Reload options</button></div>
        <p v-if="!permitted" class="note">Starting an agent requires work-order write and run-create permission.</p>
        <footer><p>The run is queued first.<br />You can follow it in Agents.</p><button type="button" class="btn" :disabled="busy" @click="close">Cancel</button><button type="submit" class="btn primary" :disabled="!canSubmit"><AppIcon :name="busy ? 'clock' : 'arrow'" :size="15" />{{ busy ? 'Queueing…' : 'Queue run' }}</button></footer>
      </template>

      <template v-else>
        <div class="run-result" data-result tabindex="-1" role="status">
          <span class="result-icon"><AppIcon :name="managed ? 'check' : 'clock'" :size="24" /></span>
          <p class="eyebrow">{{ reused ? 'Existing run' : 'Run created' }}</p>
          <h3>{{ sessionConnected ? 'Managed session connected' : state?.label }}</h3>
          <p>{{ sessionConnected ? 'The daemon registered this session. Open it to follow progress and send controls.' : state?.detail }}</p>
          <div v-if="ticket" class="result-ticket"><span class="mono">{{ ticket.key }}</span><strong>{{ ticket.title }}</strong></div>
          <p class="note mono">Run {{ run.id.slice(0, 8) }}</p>
        </div>
        <p v-if="checkError" class="error" role="alert">{{ checkError }}</p>
        <footer class="result-footer"><button type="button" class="btn ghost" :disabled="checking" @click="refresh"><AppIcon name="refresh" :size="14" />Refresh status</button><button type="button" class="btn" :disabled="busy" @click="close">Close</button><RouterLink class="btn primary" :to="managed ? `/agents/${managed.id}` : '/agents'" @click="close">{{ managed ? 'Open session' : 'Go to Agents' }}<AppIcon name="arrow" :size="15" /></RouterLink></footer>
      </template>
    </form>
  </dialog>
</template>

<style scoped>
.launch-dialog { width: min(600px, calc(100vw - 24px)); max-height: calc(100dvh - 32px); padding: 0; border: 1px solid var(--line-2); border-radius: 22px; background: var(--surface-raised); color: var(--ink); box-shadow: 0 24px 80px var(--scrim); }
.launch-dialog::backdrop { background: var(--scrim); backdrop-filter: blur(5px); }
.launch-card { padding: 26px; }
.launch-head { display: flex; gap: 12px; align-items: center; }
.launch-head h2 { font-size: 24px; letter-spacing: -.6px; margin-top: 3px; }
.launch-head .icon-btn { margin-left: auto; flex-shrink: 0; }
.launch-icon, .result-icon { display: grid; place-items: center; width: 48px; height: 48px; border-radius: 14px; background: var(--row-selected); color: var(--teal-ink); flex-shrink: 0; }
.intro { color: var(--ink-2); font-size: 14px; line-height: 1.55; margin: 18px 0 22px; }
.launch-fields { border: 0; padding: 0; margin: 0; display: grid; gap: 18px; min-width: 0; }
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
.optional { margin-left: 4px; font-weight: 400; color: var(--ink-3); }
.dispatch-status { display: flex; gap: 10px; align-items: flex-start; font-size: 13px; line-height: 1.5; }
.dispatch-status > svg { margin-top: 2px; flex-shrink: 0; }
.dispatch-status { margin-top: 22px; padding: 14px; border: 1px solid var(--line); border-radius: 12px; background: var(--surface-sunken); }
.dispatch-status p { color: var(--ink-2); margin-top: 3px; font-size: 12px; }
.dispatch-status.warn { background: var(--surface-sunken); }
.error { color: var(--danger); background: var(--danger-bg); border-radius: 10px; padding: 12px; margin-top: 14px; font-size: 13px; line-height: 1.5; }
.error .btn { margin-top: 8px; }
.bad { color: var(--danger); }
footer { display: flex; flex-wrap: wrap; justify-content: flex-end; align-items: center; gap: 8px; margin-top: 24px; padding-top: 20px; border-top: 1px solid var(--line); }
footer > p { flex: 1; font-size: 12px; color: var(--ink-2); line-height: 1.5; }
footer .btn { min-height: 44px; }
.run-result { display: grid; justify-items: center; text-align: center; padding: 14px 0; gap: 12px; outline: none; }
.run-result h3 { font-size: 25px; letter-spacing: -.5px; }
.run-result > p:not(.eyebrow) { color: var(--ink-2); font-size: 14px; line-height: 1.6; max-width: 410px; }
.result-icon { width: 58px; height: 58px; border-radius: 50%; margin-bottom: 6px; }
.result-ticket { width: 100%; padding: 14px; border-radius: 12px; background: var(--surface-sunken); }
.result-ticket span { display: block; font-size: 11px; color: var(--teal-ink); margin-bottom: 6px; }
.result-ticket strong { font-size: 14px; overflow-wrap: anywhere; }
@media (max-width: 600px) {
  .launch-dialog { max-height: calc(100dvh - 16px); width: calc(100vw - 16px); border-radius: 18px; }
  .launch-card { padding: 20px 16px; }
  .intro { margin: 16px 0; }
  .launch-fields { gap: 14px; }
  .dispatch-status { margin-top: 16px; }
  footer { margin-top: 18px; padding-top: 16px; }
  .launch-head h2 { font-size: 22px; }
  .launch-head .icon-btn { width: 44px; height: 44px; }
  .chosen-ticket { flex-wrap: wrap; }
  .chosen-ticket .btn { min-height: 44px; }
  footer > p { flex-basis: 100%; }
  .result-footer { display: grid; grid-template-columns: 1fr 1fr; }
  .result-footer > :first-child { grid-column: 1 / -1; justify-self: center; }
  .field { font-size: 16px; }
}
</style>
