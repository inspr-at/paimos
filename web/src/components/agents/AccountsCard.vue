<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { createWindow, type AgentAccount, type AllowanceWrite } from '../../lib/agents'
import { can } from '../../lib/authz'
import { accountName, accountPlan, allowanceWindowLabel } from '../../lib/accountCascade'
import { PACE_LABEL, UNIT_LABEL, bindingWindow, duration, harnessLabel } from '../../lib/agentState'
import type { Availability } from '../../stores/agents'
import { useCapacity } from '../../stores/capacity'
import CapacityLearning from './CapacityLearning.vue'
import { useSession } from '../../stores/session'
import AppIcon from '../AppIcon.vue'
import AllowanceWindowForm from './AllowanceWindowForm.vue'
import ClaudeStatuslineToggle from '../settings/ClaudeStatuslineToggle.vue'
import { ABSENT_ALLOWANCE, allowanceFailure, beginSave, findSavedAllowance, holdUncertain, releaseHeld, settleSave, UNCERTAIN_ALLOWANCE, type HeldAllowance } from './allowanceWindow'

// Per account: the window that binds first, how much of it is left and whether use
// is running ahead of the pace the window allows. Admins can drain or resume.
// A person with account.manage can add one allowance window for the open account.
const props = defineProps<{ accounts: AgentAccount[]; state: Availability; now: number; admin: boolean; set: (account: AgentAccount, state: AgentAccount['state']) => Promise<void> }>()
const emit = defineEmits<{ 'allowance-created': [] }>()
const session = useSession()
const capacity = useCapacity()
const learned = (id: string) => capacity.rows.find(r => r.id === id)
const mayManage = computed(() => session.identity?.principal.kind === 'person' && can('account.manage'))
const busy = ref('')
const error = ref('')
const editingId = ref('')
const flight = ref<number | null>(null)
const serverMessage = ref('')
const uncertain = ref(false)
const allowanceStatus = ref('')
const pending = ref<Record<string, HeldAllowance>>({})
let generation = 0
const rows = computed(() => [...props.accounts]
  .map(account => ({ account, window: bindingWindow(account.windows, props.now) }))
  .sort((a, b) => (a.account.state === 'available' ? 0 : 1) - (b.account.state === 'available' ? 0 : 1) || allowanceRank(a.window) - allowanceRank(b.window)))
function allowanceRank(window: ReturnType<typeof bindingWindow>) {
  if (!window) return 2
  if (window.window.provisional) return 1.5
  return window.left
}
function agentsAllowed(account: AgentAccount) { return account.state === 'available' && account.ongoing_use_approved !== false }
async function toggle(account: AgentAccount) {
  busy.value = account.id; error.value = ''
  try { await props.set(account, agentsAllowed(account) ? 'draining' : 'available') }
  catch (e) { error.value = e instanceof Error ? e.message : 'The account did not change. Please try again.' }
  finally { busy.value = '' }
}
function showPending(accountId: string) {
  if (pending.value[accountId]) {
    uncertain.value = true
    serverMessage.value = UNCERTAIN_ALLOWANCE
  } else {
    uncertain.value = false
    serverMessage.value = ''
  }
}
function openAllowance(accountId: string) {
  if (flight.value !== null || editingId.value === accountId) return
  generation += 1
  editingId.value = accountId
  allowanceStatus.value = ''
  showPending(accountId)
}
function closeAllowance() {
  generation += 1
  editingId.value = ''
  serverMessage.value = ''
  uncertain.value = false
}
function reviseAllowance(accountId: string) {
  pending.value = releaseHeld(pending.value, accountId)
  uncertain.value = false
  serverMessage.value = ''
}
async function saveAllowance(account: AgentAccount, body: AllowanceWrite) {
  const accountId = account.id
  const name = accountName(account)
  const started = generation
  const decision = beginSave({ busy: flight.value !== null, editingId: editingId.value, accountId, generation: started, body, now: props.now })
  if (decision.action === 'ignore') return
  if (decision.action === 'invalid') { serverMessage.value = decision.message; uncertain.value = false; return }
  flight.value = started
  serverMessage.value = ''
  try {
    await createWindow(accountId, body)
    applySettlement(settleSave({
      outcome: 'saved', message: '', accountName: name, startedAccountId: accountId, startedGeneration: started,
      currentAccountId: editingId.value, currentGeneration: generation,
    }))
  } catch (caught) {
    const failure = allowanceFailure(caught)
    if (failure.kind === 'uncertain') pending.value = holdUncertain(pending.value, accountId, name, body)
    applySettlement(settleSave({
      outcome: failure.kind === 'uncertain' ? 'uncertain' : 'rejected', message: failure.message, accountName: name,
      startedAccountId: accountId, startedGeneration: started, currentAccountId: editingId.value, currentGeneration: generation,
    }))
  } finally {
    if (flight.value === started) flight.value = null
  }
}
function applySettlement(settled: ReturnType<typeof settleSave>) {
  if (settled.clearAccountId) pending.value = releaseHeld(pending.value, settled.clearAccountId)
  if (settled.refresh) emit('allowance-created')
  if (settled.closeForm) editingId.value = ''
  uncertain.value = settled.uncertain
  serverMessage.value = settled.formMessage ?? ''
  if (settled.status) allowanceStatus.value = settled.status
}
async function checkAllowance(account: AgentAccount) {
  const held = pending.value[account.id]
  if (!held || flight.value !== null || editingId.value !== account.id) return
  const started = generation
  flight.value = started
  const match = await findSavedAllowance(account.id, held.body)
  if (flight.value === started) flight.value = null
  if (generation !== started || editingId.value !== account.id) return
  if (match === 'saved') {
    pending.value = releaseHeld(pending.value, account.id)
    uncertain.value = false
    serverMessage.value = ''
    editingId.value = ''
    allowanceStatus.value = `Allowance window saved for ${held.name}.`
    emit('allowance-created')
    return
  }
  if (match === 'absent') {
    pending.value = releaseHeld(pending.value, account.id)
    uncertain.value = false
    serverMessage.value = ABSENT_ALLOWANCE
    return
  }
  uncertain.value = true
  serverMessage.value = UNCERTAIN_ALLOWANCE
}
watch(mayManage, allowed => { if (!allowed) closeAllowance() })
const stateLabel: Record<AgentAccount['state'], string> = { available: 'Available', draining: 'Draining', unavailable: 'Unavailable' }
</script>

<template>
  <!-- Lives inside the "Accounts and pacing" disclosure, whose summary is the visible
       title; the heading stays for the region's name only. -->
  <section class="accounts" aria-labelledby="accounts-title">
    <h2 id="accounts-title" class="sr-only">Accounts and pacing</h2>
    <p v-if="state === 'forbidden'" class="note">Accounts and their allowances are visible to workspace admins.</p>
    <p v-else-if="state === 'error'" class="note" role="alert">Accounts could not be loaded right now.</p>
    <div v-else-if="state === 'idle'" class="sk"><span class="skeleton" /><span class="skeleton short" /><span class="skeleton" /></div>
    <p v-else-if="!accounts.length" class="note">No accounts are enrolled. The local agent daemon enrolls one per harness sign-in.</p>
    <ul v-else class="list">
      <li v-for="{ account, window } in rows" :key="account.id" class="account" :class="account.state">
        <div class="top">
          <span class="dot" :class="account.state" aria-hidden="true" />
          <span class="label">{{ accountName(account) }}</span>
          <span v-if="accountPlan(account)" class="plan">{{ accountPlan(account) }}</span>
          <span class="harness">{{ harnessLabel(account.harness) }}</span>
          <span class="spacer" />
          <span v-if="account.state !== 'available'" class="state-text">{{ stateLabel[account.state] }}</span>
          <button v-if="mayManage && editingId !== account.id" type="button" class="btn sm add-window" :aria-label="`Add allowance window for ${accountName(account)}`" :disabled="flight !== null || busy === account.id" @click="openAllowance(account.id)">Add allowance window</button>
          <button v-if="mayManage" type="button" role="switch" class="btn sm toggle" :aria-label="`Agents may use it · ${accountName(account)}`" :aria-checked="agentsAllowed(account)" :disabled="busy === account.id" @click="toggle(account)"><span>Agents may use it</span><span class="switch-state">{{ agentsAllowed(account) ? 'On' : 'Off' }}</span></button>
        </div>
        <template v-if="window?.window.provisional">
          <p class="facts">
            <span class="left">{{ allowanceWindowLabel(window.window) }} {{ UNIT_LABEL[window.window.unit] }} · allowance unknown</span>
            <span class="provisional">Unmeasured</span>
            <span class="reset">resets in {{ duration(window.resetsIn) }}</span>
          </p>
        </template>
        <template v-else-if="window">
          <div class="meter" role="meter" :aria-valuenow="Math.round(window.left * 100)" aria-valuemin="0" aria-valuemax="100" :aria-label="`${accountName(account)}: ${Math.round(window.left * 100)}% of ${allowanceWindowLabel(window.window)} ${UNIT_LABEL[window.window.unit]} left`">
            <span class="fill" :class="window.pace" :style="{ width: `${Math.max(2, window.left * 100)}%` }" />
            <span class="pace-mark" :style="{ left: `${Math.min(100, (1 - window.expected) * 100)}%` }" :data-tip="`Pace allows ${Math.round(window.expected * 100)}% used by now`" />
          </div>
          <p class="facts">
            <span class="left"><b>{{ Math.round(window.left * 100) }}%</b> {{ UNIT_LABEL[window.window.unit] }} left · {{ allowanceWindowLabel(window.window) }}</span>
            <span class="pace" :class="window.pace">{{ PACE_LABEL[window.pace] }}</span>
            <span class="reset">resets in {{ duration(window.resetsIn) }}</span>
          </p>
        </template>
        <p v-else class="facts muted">No active allowance window</p>
        <CapacityLearning :learning="learned(account.id)?.learning" :host="account.host_label" :now="now" />
        <ClaudeStatuslineToggle v-if="mayManage && account.harness === 'claude' && account.statusline_opt_in" :account="account" @changed="emit('allowance-created')" />
        <AllowanceWindowForm
          v-if="mayManage && editingId === account.id"
          :account="account" :now="now" :busy="flight !== null" :server-message="serverMessage" :uncertain="uncertain"
          :pending="pending[account.id]?.body ?? null"
          @save="saveAllowance(account, $event)" @cancel="closeAllowance" @check="checkAllowance(account)" @revise="reviseAllowance(account.id)"
        />
      </li>
    </ul>
    <p v-if="allowanceStatus" class="note" role="status">{{ allowanceStatus }}</p>
    <p v-if="error" class="note error" role="alert"><AppIcon name="alert" :size="13" />{{ error }}</p>
  </section>
</template>

<style scoped>
.accounts { border-top: 1px solid var(--line); padding-top: 8px; }
.note { padding: 8px 18px 16px; font-size: 12.5px; color: var(--ink-2); }
.note.error { display: flex; align-items: center; gap: 6px; color: var(--danger); }
.sk { display: grid; gap: 10px; padding: 6px 16px 18px; }
.sk .short { width: 60%; }
.list { margin: 0; padding: 0 10px 10px; list-style: none; display: grid; grid-template-columns: minmax(0, 1fr); gap: 2px; }
.account { padding: 10px 8px 10px; border-radius: 10px; }
.account + .account { box-shadow: inset 0 1px 0 var(--line); border-radius: 0; }
.account.unavailable .label { color: var(--ink-2); }
.top { display: flex; align-items: center; flex-wrap: wrap; gap: 6px 8px; min-height: 24px; }
.dot { width: 8px; height: 8px; border-radius: 50%; background: var(--ok); flex-shrink: 0; }
.toggle { min-height: 44px; gap: 8px; }
.switch-state { min-width: 30px; font-weight: 650; }
.toggle[aria-checked="true"] { background: var(--row-selected); }
.dot.draining { background: var(--gold); }
.dot.unavailable { background: var(--st-closed); }
.label { font-size: 13px; font-weight: 600; color: var(--ink); min-width: 0; overflow-wrap: anywhere; }
.harness, .plan { flex-shrink: 0; height: 18px; padding: 0 6px; border-radius: 5px; background: var(--chip-bg); box-shadow: inset 0 0 0 1px var(--chip-line); font: 500 10px/18px var(--mono); color: var(--ink-2); font-variant-ligatures: none; }
.plan { font-family: var(--font); letter-spacing: 0; }
.spacer { flex: 1; }
.state-text { font-size: 11.5px; color: var(--gold-ink); font-weight: 600; }
.account.unavailable .state-text { color: var(--ink-3); }
.toggle, .add-window { height: 24px; padding: 0 8px; font-size: 12px; }
.add-window { border-color: transparent; background: transparent; color: var(--teal-ink); }
/* Permission is always visible. The secondary manual editor stays on hover. */
@media (hover: hover) and (min-width: 601px) { .account .add-window { opacity: 0; } .account:hover .add-window, .account:focus-within .add-window { opacity: 1; } }
.meter { position: relative; height: 6px; margin: 9px 0 7px; border-radius: 999px; background: var(--skeleton); }
.fill { position: absolute; inset: 0 auto 0 0; border-radius: inherit; background: linear-gradient(90deg, var(--teal), var(--st-qa-fill)); }
.fill.ahead { background: linear-gradient(90deg, var(--gold), var(--gold-2)); }
.pace-mark { position: absolute; top: -3px; width: 2px; height: 12px; margin-left: -1px; border-radius: 1px; background: var(--ink-2); opacity: .55; }
.facts { display: flex; align-items: center; flex-wrap: wrap; gap: 4px 10px; font-size: 12px; color: var(--ink-2); }
.facts b { font: 600 12.5px/1 var(--mono); color: var(--ink); font-variant-numeric: tabular-nums; }
.facts.muted { margin-top: 4px; color: var(--ink-3); }
.pace { font-weight: 600; color: var(--ok); }
.pace.ahead { color: var(--gold-ink); }
.pace.under { color: var(--ink-2); font-weight: 500; }
.provisional { font-weight: 600; color: var(--ink-2); }
.reset { margin-left: auto; color: var(--ink-3); }
</style>
