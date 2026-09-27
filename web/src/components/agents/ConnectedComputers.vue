<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import AppIcon from '../AppIcon.vue'
import {
  PairingError, activeRunIds, applyComputerListRefresh, describeComputerStatus, disconnectComputer, disconnectConfirm,
  disconnectEnrollment, getPairingComputer, lastActiveLabel, listPairingComputers, pairingReadGeneration, pairingScopeKey,
  platformCaption, type DisconnectMode, type PairingPermissions, type PairingView,
} from '../../lib/agentPairing'
import { onAccessChange } from '../../lib/authz'
import { harnessLabel } from '../../lib/agentState'
import { usePoller } from '../../lib/usePolledData'
import HarnessMark from './HarnessMark.vue'

const props = defineProps<{ permissions: PairingPermissions; compactEmpty?: boolean }>()

const computers = ref<PairingView[]>([])
const state = ref<'idle' | 'loading' | 'ready' | 'error'>('idle')
const refreshing = ref(false)
const message = ref('')
const nextStep = ref('')
const openId = ref('')
const busy = ref('')
const dialog = ref<HTMLDialogElement>()
const drainButton = ref<HTMLButtonElement>()
const pending = ref<{ view: PairingView; scope: 'computer' | 'enrollment'; accountId?: string } | null>(null)

const visible = computed(() => state.value === 'loading' || state.value === 'error' || computers.value.length > 0 || (state.value === 'ready' && !props.compactEmpty))
const confirm = computed(() => {
  const request = pending.value
  if (!request) return null
  const enrollment = request.accountId ? request.view.enrollments.find(item => item.account_id === request.accountId) : undefined
  const others = request.view.enrollments.filter(item => item.account_id !== request.accountId && item.state !== 'revoked').length
  return disconnectConfirm({
    scope: request.scope,
    computerName: request.view.computer_name,
    mode: 'drain',
    activeRunCount: activeRunIds(request.view).length,
    otherConnectedCount: others,
    ...(enrollment ? { enrollment: { harness: enrollment.harness, label: enrollment.label } } : {}),
  })
})
const revokeCopy = computed(() => {
  const request = pending.value
  if (!request) return null
  const enrollment = request.accountId ? request.view.enrollments.find(item => item.account_id === request.accountId) : undefined
  const others = request.view.enrollments.filter(item => item.account_id !== request.accountId && item.state !== 'revoked').length
  return disconnectConfirm({
    scope: request.scope,
    computerName: request.view.computer_name,
    mode: 'revoke_now',
    activeRunCount: activeRunIds(request.view).length,
    otherConnectedCount: others,
    ...(enrollment ? { enrollment: { harness: enrollment.harness, label: enrollment.label } } : {}),
  })
})
const accountingOpen = computed(() => {
  const view = pending.value?.view
  if (!view) return false
  return view.accounting_state === 'unconfirmed' || view.enrollments.some(item => item.accounting_state === 'unconfirmed')
})

let loadTurn = 0
watch(() => props.permissions.canListComputers, can => { if (can) void load(); else dropSignedInList() }, { immediate: true })
const stopAccess = onAccessChange(change => { if (change === 'reset') dropSignedInList() })
const poller = usePoller(() => load(), 20_000, { enabled: () => props.permissions.canListComputers })
onMounted(() => poller.start())
onBeforeUnmount(() => { stopAccess(); poller.stop() })

function dropSignedInList() {
  computers.value = []
  state.value = 'idle'
  refreshing.value = false
  message.value = ''
  nextStep.value = ''
  openId.value = ''
  loadTurn += 1
  closeDialog()
}

async function load() {
  if (!props.permissions.canListComputers) { dropSignedInList(); return }
  const started = pairingReadGeneration()
  const turn = ++loadTurn
  const had = computers.value.length > 0
  if (!had) state.value = 'loading'
  refreshing.value = true
  try {
    const rows = await listPairingComputers()
    if (turn !== loadTurn || started !== pairingReadGeneration() || !props.permissions.canListComputers) return
    const next = applyComputerListRefresh(computers.value, { ok: true, computers: rows })
    computers.value = next.computers
    state.value = 'ready'
    message.value = ''
    nextStep.value = ''
  } catch (error) {
    if (error instanceof PairingError && error.code === 'session_reset') return
    if (turn !== loadTurn || started !== pairingReadGeneration() || !props.permissions.canListComputers) return
    const next = applyComputerListRefresh(computers.value, { ok: false })
    computers.value = next.computers
    state.value = had ? 'ready' : 'error'
    assign(error, had ? 'Paired computers could not be refreshed.' : 'Paired computers could not be loaded.')
    if (had) nextStep.value = 'The list stays as it was. A failed refresh does not show that a computer disconnected or that local cleanup finished.'
  } finally {
    if (turn === loadTurn && started === pairingReadGeneration()) refreshing.value = false
  }
}

function statusOf(computer: PairingView) {
  return describeComputerStatus(computer)
}
function harnesses(computer: PairingView) {
  return computer.enrollments.filter(item => item.state !== 'revoked')
}
function canChange(computer: PairingView) {
  return props.permissions.canDisconnect && (computer.computer_state === 'connected' || computer.computer_state === 'draining')
}
function toggle(id: string) { openId.value = openId.value === id ? '' : id }

function openDialog(computer: PairingView, scope: 'computer' | 'enrollment', accountId?: string) {
  message.value = ''
  nextStep.value = ''
  pending.value = { view: computer, scope, ...(accountId ? { accountId } : {}) }
  dialog.value?.showModal()
  queueMicrotask(() => drainButton.value?.focus())
}
function closeDialog() {
  if (dialog.value?.open) dialog.value.close()
  pending.value = null
}

async function commit(mode: DisconnectMode) {
  const request = pending.value
  if (!request?.view.computer_id || busy.value) return
  busy.value = mode
  message.value = ''
  nextStep.value = ''
  try {
    const fresh = await getPairingComputer(request.view.computer_id)
    if (pairingScopeKey(fresh) !== pairingScopeKey(request.view)) {
      replace(fresh)
      pending.value = { ...request, view: fresh }
      message.value = 'This computer changed.'
      nextStep.value = 'Review the harnesses again before disconnecting. The previous action was not sent.'
      return
    }
    const next = request.scope === 'enrollment' && request.accountId
      ? await disconnectEnrollment(fresh, request.accountId, mode, props.permissions)
      : await disconnectComputer(fresh, mode, props.permissions)
    replace(next)
    closeDialog()
  } catch (error) {
    assign(error, 'The computer was not disconnected.')
    if (error instanceof PairingError && error.code === 'conflict' && request.view.computer_id) {
      try { replace(await getPairingComputer(request.view.computer_id)) } catch { /* The conflict message already asks for a fresh review. */ }
    }
  } finally { busy.value = '' }
}

function replace(next: PairingView) {
  const index = computers.value.findIndex(item => item.computer_id === next.computer_id || item.request_id === next.request_id)
  if (index === -1) computers.value = [next, ...computers.value]
  else computers.value = computers.value.map((item, i) => i === index ? next : item)
  if (pending.value && (pending.value.view.computer_id === next.computer_id || pending.value.view.request_id === next.request_id)) {
    pending.value = { ...pending.value, view: next }
  }
}

function assign(error: unknown, fallback: string) {
  if (error instanceof PairingError) { message.value = error.message; nextStep.value = error.next; return }
  message.value = fallback
  nextStep.value = 'Try again. This page will not stop a process on the computer.'
}
</script>

<template>
  <section v-if="visible" class="computers" aria-labelledby="computers-title">
    <header class="head">
      <div>
        <h2 id="computers-title">Connected computers</h2>
        <p>Computers paired with Aeon in this workspace.</p>
      </div>
      <div class="head-actions">
        <button v-if="permissions.canListComputers" type="button" class="btn sm" :disabled="refreshing" @click="load"><AppIcon name="refresh" :size="14" />{{ refreshing ? 'Refreshing…' : 'Refresh' }}</button>
        <RouterLink v-if="permissions.canApprove" class="btn sm" to="/agents/register-agent"><AppIcon name="plus" :size="14" />Add computer</RouterLink>
      </div>
    </header>

    <p v-if="state === 'loading'" class="muted">Loading paired computers…</p>
    <p v-else-if="state === 'error'" class="problem" role="alert">{{ message }} <button type="button" class="btn sm" @click="load">Try again</button></p>
    <p v-else-if="!computers.length" class="muted">No computers are connected in this workspace yet.</p>

    <div v-else class="list">
      <div class="sheet" aria-hidden="true">
        <span>Computer</span><span>Harnesses</span><span>Status</span><span>Last active</span><span>Actions</span>
      </div>
      <article v-for="computer in computers" :key="computer.computer_id ?? computer.request_id" class="computer">
        <div class="identity">
          <span class="glyph"><AppIcon name="monitor" :size="16" /></span>
          <div>
            <p class="name">{{ computer.computer_name }}</p>
            <p class="meta">{{ platformCaption(computer.platform, computer.arch) }} · {{ computer.tenant_name }} · {{ computer.workspace_path }}</p>
          </div>
        </div>
        <p class="harness-line">
          <HarnessMark v-for="enrollment in harnesses(computer)" :key="enrollment.account_id" :harness="enrollment.harness" :size="14" />
          <span>{{ harnesses(computer).length }} {{ harnesses(computer).length === 1 ? 'harness' : 'harnesses' }}</span>
        </p>
        <p class="status"><span class="dot" :data-state="statusOf(computer).stateLabel" />{{ statusOf(computer).stateLabel }}</p>
        <p class="meta">{{ lastActiveLabel(computer.last_seen_at) }}</p>
        <div class="actions">
          <RouterLink v-if="computer.computer_state === 'connected' && computer.computer_id" class="btn sm" :to="`/agents/register-agent?computer=${computer.computer_id}`">Add harness</RouterLink>
          <button v-if="canChange(computer)" type="button" class="btn sm" @click="openDialog(computer, 'computer')">Disconnect</button>
          <button type="button" class="btn sm" :aria-expanded="openId === (computer.computer_id ?? computer.request_id)" @click="toggle(computer.computer_id ?? computer.request_id)">{{ openId === (computer.computer_id ?? computer.request_id) ? 'Hide' : 'Details' }}</button>
        </div>
        <div v-if="openId === (computer.computer_id ?? computer.request_id)" class="detail">
          <p>{{ statusOf(computer).detail }}</p>
          <p>{{ statusOf(computer).next }}</p>
          <ul>
            <li v-for="enrollment in computer.enrollments" :key="enrollment.account_id">
              <HarnessMark :harness="enrollment.harness" :size="14" />
              <span>{{ harnessLabel(enrollment.harness) }} · {{ enrollment.label }} · {{ enrollment.state }}</span>
              <span v-if="enrollment.verification_state && enrollment.verification_state !== 'not_selected'">Verification {{ enrollment.verification_state === 'unavailable' ? 'unavailable' : enrollment.verification_state.replace(/_/g, ' ') }}</span>
              <span v-if="enrollment.verification_error && enrollment.verification_error !== 'verification_unavailable'">{{ enrollment.verification_error }}</span>
              <span v-if="enrollment.local_processes">Local processes {{ enrollment.local_processes }}</span>
              <span v-if="enrollment.accounting_state === 'unconfirmed'">Accounting unconfirmed</span>
              <button v-if="canChange(computer) && enrollment.state !== 'revoked'" type="button" class="btn sm" @click="openDialog(computer, 'enrollment', enrollment.account_id)">Remove {{ enrollment.label }} from Aeon</button>
            </li>
          </ul>
        </div>
      </article>
    </div>
    <p v-if="message && state !== 'error'" class="problem" role="alert">{{ message }}</p>
    <p v-if="nextStep">{{ nextStep }}</p>

    <dialog ref="dialog" class="disconnect" aria-labelledby="disconnect-title" @cancel.prevent="closeDialog" @click="(event) => { if (event.target === dialog) closeDialog() }">
      <div v-if="confirm && revokeCopy && pending" class="panel">
        <header>
          <h2 id="disconnect-title">{{ confirm.title }}</h2>
          <button type="button" class="icon-btn" aria-label="Close" @click="closeDialog"><AppIcon name="close" :size="14" /></button>
        </header>
        <p v-if="activeRunIds(pending.view).length" class="runs"><AppIcon name="alert" :size="15" />{{ activeRunIds(pending.view).length }} {{ activeRunIds(pending.view).length === 1 ? 'run' : 'runs' }} in progress. These runs continue unless they finish first.</p>
        <p>{{ confirm.body }}</p>
        <ul>
          <li v-for="point in confirm.points" :key="point">{{ point }}</li>
          <li v-if="accountingOpen">Run accounting stays unconfirmed. Disconnecting does not settle it.</li>
        </ul>
        <div class="choices">
          <button ref="drainButton" type="button" class="btn primary" :disabled="!!busy" @click="commit('drain')">{{ busy === 'drain' ? 'Disconnecting…' : confirm.confirmLabel }}</button>
          <button type="button" class="btn" :disabled="!!busy" @click="commit('revoke_now')">{{ busy === 'revoke_now' ? 'Revoking…' : revokeCopy.confirmLabel }}</button>
        </div>
        <p class="fine">{{ revokeCopy.body }} Local processes stay unconfirmed until the computer reports them.</p>
      </div>
    </dialog>
  </section>
</template>

<style scoped>
.computers { margin-top: 28px; padding: 18px 18px 8px; border: 1px solid var(--line); border-radius: 16px; background: var(--surface-raised); }
.head { display: flex; align-items: flex-start; justify-content: space-between; gap: 12px; margin-bottom: 12px; }
.head h2 { font: 600 16px/1.3 var(--font); letter-spacing: 0; }
.head p, .meta, .fine { color: var(--ink-2); font-size: 13px; }
.head-actions { display: flex; flex-wrap: wrap; gap: 8px; }
.head .btn { flex-shrink: 0; }
.problem { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; margin: 8px 0; color: var(--danger); }
.sheet, .computer { display: grid; grid-template-columns: minmax(140px, 1.6fr) minmax(96px, 1fr) minmax(96px, .8fr) minmax(72px, .6fr) auto; gap: 8px 14px; align-items: center; }
.sheet { padding: 0 4px 8px; color: var(--ink-3); font-size: 11px; letter-spacing: .08em; text-transform: uppercase; }
.computer { padding: 12px 4px; border-top: 1px solid var(--line); }
.identity { display: flex; gap: 10px; min-width: 0; }
.glyph { display: grid; place-items: center; width: 32px; height: 32px; border-radius: 9px; background: var(--surface-sunken); color: var(--ink-2); }
.name { color: var(--ink); font-weight: 650; }
.meta, .name { overflow-wrap: anywhere; }
.harness-line, .status { display: flex; align-items: center; gap: 6px; min-width: 0; color: var(--ink); }
.dot { width: 7px; height: 7px; border-radius: 50%; background: var(--st-backlog); }
.dot[data-state="Connected"] { background: var(--ok); }
.dot[data-state="Draining"], .dot[data-state="Offline"] { background: var(--gold); }
.dot[data-state="Revoked"] { background: var(--danger); }
.actions { display: flex; flex-wrap: wrap; justify-content: flex-end; gap: 6px; }
.detail { grid-column: 1 / -1; display: grid; gap: 6px; padding: 4px 0 8px; color: var(--ink-2); font-size: 13px; }
.detail ul { display: grid; gap: 8px; margin: 0; padding: 0; list-style: none; }
.detail li { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; }
.disconnect { width: min(420px, calc(100vw - 32px)); padding: 0; border: 0; background: transparent; color: var(--ink); }
.disconnect::backdrop { background: var(--scrim); }
.panel { padding: 18px 18px 16px; border: 1px solid var(--glass-edge); border-radius: 16px; background: var(--surface-raised); box-shadow: var(--shadow-pop); }
.panel header { display: flex; align-items: flex-start; justify-content: space-between; gap: 8px; }
.panel h2 { font: 600 18px/1.3 var(--font); }
.panel p, .panel li { font-size: 13.5px; color: var(--ink-2); }
.panel ul { display: grid; gap: 6px; margin: 10px 0 0; padding: 0; list-style: none; }
.runs { display: flex; gap: 8px; align-items: flex-start; margin-top: 12px; padding: 10px 12px; border-radius: 12px; background: var(--mark-hl); color: var(--ink); }
.choices { display: grid; gap: 8px; margin-top: 16px; }
.choices .btn { width: 100%; min-height: 44px; }
.fine { margin-top: 8px; }
@media (max-width: 720px) {
  .computers { padding: 14px 12px 6px; }
  .sheet { display: none; }
  .computer { grid-template-columns: 1fr; }
  .actions { justify-content: flex-start; }
  .head { flex-direction: column; }
}
</style>
