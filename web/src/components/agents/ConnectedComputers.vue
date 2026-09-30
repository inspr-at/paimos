<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import AppIcon from '../AppIcon.vue'
import {
  PairingError, activeRunIds, applyComputerListRefresh, computerRemoval, describeComputerStatus, removeComputer, describeEnrollmentStatus, describeHarnessStatus, describeHarnessFix, describeHarnessHint, disconnectComputer, disconnectConfirm,
  disconnectEnrollment, getPairingComputer, lastActiveLabel, listPairingComputers, pairingReadGeneration, pairingScopeKey,
  platformCaption, type DisconnectMode, type PairingPermissions, type PairingView,
} from '../../lib/agentPairing'
import { onAccessChange } from '../../lib/authz'
import { harnessLabel } from '../../lib/agentState'
import { brand } from '../../lib/brand'
import { confirmAction } from '../../lib/confirm'
import { toast } from '../../lib/toast'
import { usePoller } from '../../lib/usePolledData'
import HarnessMark from './HarnessMark.vue'

const props = defineProps<{ permissions: PairingPermissions; compactEmpty?: boolean; embedded?: boolean }>()
const emit = defineEmits<{ removed: [computer: PairingView]; loaded: [computers: PairingView[]] }>()

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

// Revoked computers fold into one quiet disclosure under the live ones (AEON-402).
const active = computed(() => computers.value.filter(item => item.computer_state !== 'revoked'))
const revoked = computed(() => computers.value.filter(item => item.computer_state === 'revoked'))
const revokedOpen = ref(false)
const root = ref<HTMLElement>()
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
const revokeExplanation = computed(() => {
  const copy = revokeCopy.value
  return copy ? `${copy.body} Local processes stay unconfirmed until the computer reports them.` : ''
})
function pathParts(path: string) {
  const cut = Math.max(path.lastIndexOf('/'), path.lastIndexOf('\\'))
  if (cut <= 0 || cut >= path.length - 1) return { head: path, tail: '' }
  return { head: path.slice(0, cut + 1), tail: path.slice(cut + 1) }
}

let loadTurn = 0
watch(() => props.permissions.canListComputers, can => { if (can) void load(); else dropSignedInList() }, { immediate: true })
const stopAccess = onAccessChange(change => { if (change === 'reset') dropSignedInList() })
const poller = usePoller(() => load(), 20_000, { enabled: () => props.permissions.canListComputers })
onMounted(() => poller.start())
onBeforeUnmount(() => { stopAccess(); poller.stop() })

// An accepted write wins over any list read already in flight (AEON-402).
function supersedeLoads() {
  loadTurn += 1
  refreshing.value = false
}

function dropSignedInList() {
  computers.value = []
  state.value = 'idle'
  refreshing.value = false
  message.value = ''
  nextStep.value = ''
  openId.value = ''
  busy.value = ''
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
    emit('loaded', computers.value)
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
function reportedHarnesses(computer: PairingView) {
  return [...new Set(harnesses(computer).map(item => item.harness))]
}
function hasHarnessReports(computer: PairingView) {
  return reportedHarnesses(computer).some(harness => describeHarnessStatus(computer, harness))
}
const removal = (computer: PairingView) => computerRemoval(computer, props.permissions)
// A computer that never confirmed is removed, not disconnected: one action per row.
function canChange(computer: PairingView) {
  return props.permissions.canDisconnect && (computer.computer_state === 'connected' || computer.computer_state === 'draining') && !removal(computer).allowed
}
function toggle(id: string) { openId.value = openId.value === id ? '' : id }
// A missing report is shown as nothing, not as a filler word.
function lastActive(computer: PairingView) {
  const label = lastActiveLabel(computer.last_seen_at)
  return label === 'Not reported' ? '' : label
}
const keyOf = (computer: PairingView) => computer.computer_id ?? computer.request_id

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
  const started = pairingReadGeneration()
  busy.value = mode
  message.value = ''
  nextStep.value = ''
  try {
    const fresh = await getPairingComputer(request.view.computer_id)
    if (started !== pairingReadGeneration()) return
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
    if (started !== pairingReadGeneration()) return
    supersedeLoads()
    replace(next)
    closeDialog()
  } catch (error) {
    if (started !== pairingReadGeneration()) return
    assign(error, 'The computer was not disconnected.')
    if (error instanceof PairingError && error.code === 'conflict' && request.view.computer_id) {
      try { replace(await getPairingComputer(request.view.computer_id)) } catch { /* The conflict message already asks for a fresh review. */ }
    }
  } finally { if (started === pairingReadGeneration()) busy.value = '' }
}

async function remove(computer: PairingView) {
  if (busy.value) return
  const bindings = computer.enrollments.length
  const ok = await confirmAction({
    title: `Remove ${computer.computer_name}?`,
    body: 'It leaves this list. Its history stays in the audit log.',
    points: [
      computer.computer_state !== 'revoked' ? 'Its approval is revoked first, so it can no longer connect with it.' : '',
      bindings ? `${bindings === 1 ? 'Its account binding leaves' : `Its ${bindings} account bindings leave`} Accounts too.` : '',
    ].filter(Boolean),
    confirmLabel: 'Remove', danger: true,
  })
  if (!ok) return
  const started = pairingReadGeneration()
  const key = keyOf(computer)
  const list = computer.computer_state === 'revoked' ? revoked : active
  const index = list.value.findIndex(item => keyOf(item) === key)
  busy.value = `remove:${key}`
  message.value = ''
  nextStep.value = ''
  try {
    await removeComputer(computer, props.permissions)
    if (started !== pairingReadGeneration()) return
    supersedeLoads()
    computers.value = computers.value.filter(item => keyOf(item) !== key)
    toast(`Removed ${computer.computer_name}. Its history stays in the audit log.`)
    emit('removed', computer)
    // A disabled button takes no focus: settle busy before moving focus.
    busy.value = ''
    await nextTick()
    // Focus the next Remove in the same group, else the disclosure or the title.
    const buttons = [...(root.value?.querySelectorAll<HTMLElement>(computer.computer_state === 'revoked' ? '.revoked-list .remove-computer' : '.list .remove-computer') ?? [])]
    const next = buttons[Math.min(index, buttons.length - 1)] ?? root.value?.querySelector<HTMLElement>('.revoked-toggle') ?? root.value?.querySelector<HTMLElement>('h2')
    next?.focus()
  } catch (error) {
    if (started !== pairingReadGeneration()) return
    assign(error, 'The computer was not removed.')
  } finally { if (started === pairingReadGeneration()) busy.value = '' }
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
  <section v-if="visible" ref="root" class="computers" :class="{ embedded }" aria-labelledby="computers-title">
    <header class="head">
      <h2 id="computers-title" tabindex="-1">Connected computers</h2>
      <span v-if="active.length" class="count mono">{{ active.length }}</span>
      <span class="spacer" />
      <button v-if="permissions.canListComputers" type="button" class="icon-btn sm flat" :disabled="refreshing" :aria-label="refreshing ? 'Refreshing computers' : 'Refresh computers'" :data-tip="refreshing ? 'Refreshing…' : 'Refresh'" @click="load"><AppIcon name="refresh" :size="15" /></button>
      <RouterLink v-if="permissions.canApprove && !embedded" class="btn sm" to="/agents/register-agent"><AppIcon name="plus" :size="14" />Add computer</RouterLink>
    </header>

    <p v-if="state === 'loading'" class="muted">Loading paired computers…</p>
    <p v-else-if="state === 'error'" class="problem" role="alert">{{ message }} <button type="button" class="btn sm" @click="load">Try again</button></p>
    <p v-else-if="!computers.length" class="muted">No computers are connected in this workspace yet.</p>
    <p v-else-if="!active.length" class="muted">No computer is connected right now.</p>

    <div v-if="state === 'ready' && active.length" class="list">
      <div class="sheet" aria-hidden="true">
        <span>Computer</span><span>Harnesses</span><span>Status</span><span>Last active</span><span />
      </div>
      <article v-for="computer in active" :key="keyOf(computer)" class="computer" :class="{ open: openId === keyOf(computer), 'has-reports': hasHarnessReports(computer) }">
        <div class="identity">
          <span class="glyph"><AppIcon name="monitor" :size="16" /></span>
          <div class="identity-text">
            <p class="name">{{ computer.computer_name }}</p>
            <p class="meta">
              <span class="where">{{ platformCaption(computer.platform, computer.arch) }}</span>
              <template v-if="computer.workspace_path">
                <span class="sep" aria-hidden="true">·</span>
                <span class="path" :data-tip="computer.workspace_path"><span class="path-head">{{ pathParts(computer.workspace_path).head }}</span><span class="path-tail">{{ pathParts(computer.workspace_path).tail }}</span></span>
              </template>
            </p>
          </div>
        </div>
        <div v-if="hasHarnessReports(computer)" class="harness-line reported">
          <span v-for="harness in reportedHarnesses(computer)" :key="harness" class="harness-report">
            <HarnessMark :harness="harness" :size="14" />
            <span class="harness-report-text" :title="[harnessLabel(harness), describeHarnessStatus(computer, harness)].filter(Boolean).join(' · ')">
              <span>{{ harnessLabel(harness) }}<span v-if="describeHarnessStatus(computer, harness)" class="harness-state"> · {{ describeHarnessStatus(computer, harness) }}</span></span>
              <span v-if="describeHarnessHint(computer, harness)" class="harness-hint">{{ describeHarnessHint(computer, harness) }}</span>
              <code v-if="describeHarnessFix(computer, harness)" class="harness-fix">{{ describeHarnessFix(computer, harness) }}</code>
            </span>
          </span>
        </div>
        <p v-else class="harness-line">
          <HarnessMark v-for="enrollment in harnesses(computer)" :key="enrollment.account_id" :harness="enrollment.harness" :size="14" />
          <span>{{ harnesses(computer).length }} {{ harnesses(computer).length === 1 ? 'harness' : 'harnesses' }}</span>
        </p>
        <p class="status"><span class="dot" :data-state="statusOf(computer).stateLabel" />{{ statusOf(computer).stateLabel }}</p>
        <p class="meta last-active">{{ lastActive(computer) }}</p>
        <div class="actions">
          <button v-if="canChange(computer)" type="button" class="btn sm ghost" @click="openDialog(computer, 'computer')">Disconnect</button>
          <button v-else-if="removal(computer).allowed" type="button" class="btn sm ghost remove-computer" :disabled="!!busy" @click="remove(computer)">{{ busy === `remove:${keyOf(computer)}` ? 'Removing…' : 'Remove' }}<span class="sr-only"> {{ computer.computer_name }}</span></button>
          <button
            type="button" class="icon-btn sm flat details-toggle" :aria-expanded="openId === keyOf(computer)" :aria-label="`${openId === keyOf(computer) ? 'Hide' : 'Show'} details for ${computer.computer_name}`"
            :data-tip="openId === keyOf(computer) ? 'Hide details' : 'Details'" @click="toggle(keyOf(computer))"
          ><AppIcon name="chevron-right" :size="15" class="chev" /></button>
        </div>
        <div v-if="openId === keyOf(computer)" class="detail">
          <p>{{ statusOf(computer).detail }} {{ statusOf(computer).next }}</p>
          <ul>
            <li v-for="enrollment in computer.enrollments" :key="enrollment.account_id">
              <HarnessMark :harness="enrollment.harness" :size="14" />
              <span class="enrollment-name">{{ harnessLabel(enrollment.harness) }} · {{ enrollment.label }}</span>
              <span class="enrollment-meta">{{ enrollment.state === 'connected' ? describeEnrollmentStatus(computer, enrollment) || enrollment.state : enrollment.state }}<template v-if="enrollment.verification_state && enrollment.verification_state !== 'not_selected'"> · verification {{ enrollment.verification_state === 'unavailable' ? 'unavailable' : enrollment.verification_state.replace(/_/g, ' ') }}</template><template v-if="enrollment.local_processes"> · {{ enrollment.local_processes }} local processes</template><template v-if="enrollment.accounting_state === 'unconfirmed'"> · accounting unconfirmed</template></span>
              <span v-if="enrollment.verification_error && enrollment.verification_error !== 'verification_unavailable'" class="enrollment-meta">{{ enrollment.verification_error }}</span>
              <button v-if="canChange(computer) && enrollment.state !== 'revoked'" type="button" class="btn sm ghost remove" @click="openDialog(computer, 'enrollment', enrollment.account_id)">Remove<span class="sr-only"> {{ enrollment.label }} from {{ brand.short_name }}</span></button>
            </li>
          </ul>
          <RouterLink v-if="computer.computer_state === 'connected' && computer.computer_id" class="btn sm add-harness" :to="`/agents/register-agent?computer=${computer.computer_id}`"><AppIcon name="plus" :size="13" />Add harness</RouterLink>
        </div>
      </article>
    </div>
    <div v-if="state === 'ready' && revoked.length" class="revoked">
      <button type="button" class="revoked-toggle" :aria-expanded="revokedOpen" aria-controls="revoked-list" @click="revokedOpen = !revokedOpen">
        <AppIcon name="chevron-right" :size="14" class="chev" />Revoked ({{ revoked.length }})
      </button>
      <ul v-if="revokedOpen" id="revoked-list" class="revoked-list">
        <li v-for="computer in revoked" :key="keyOf(computer)" :title="`${statusOf(computer).detail} ${statusOf(computer).next}`">
          <span class="glyph small"><AppIcon name="monitor" :size="14" /></span>
          <span class="revoked-name">{{ computer.computer_name }}</span>
          <span class="revoked-meta">{{ [platformCaption(computer.platform, computer.arch), lastActive(computer)].filter(Boolean).join(' · ') }}</span>
          <button v-if="removal(computer).allowed" type="button" class="btn sm ghost remove-computer" :disabled="!!busy" @click="remove(computer)">{{ busy === `remove:${keyOf(computer)}` ? 'Removing…' : 'Remove' }}<span class="sr-only"> {{ computer.computer_name }}</span></button>
          <span v-else-if="removal(computer).reason" class="revoked-reason">{{ removal(computer).reason }}</span>
        </li>
      </ul>
    </div>
    <p v-if="message && state !== 'error'" class="problem" role="alert">{{ message }}</p>
    <p v-if="nextStep" class="next-step">{{ nextStep }}</p>

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
          <button type="button" class="revoke" :data-tip="revokeExplanation" aria-describedby="revoke-note" :disabled="!!busy" @click="commit('revoke_now')">{{ busy === 'revoke_now' ? 'Revoking…' : 'Revoke access now' }}</button>
          <span id="revoke-note" class="sr-only">{{ revokeExplanation }}</span>
          <button type="button" class="btn" :disabled="!!busy" @click="closeDialog">{{ confirm.cancelLabel }}</button>
          <button ref="drainButton" type="button" class="btn primary" :disabled="!!busy" @click="commit('drain')">{{ busy === 'drain' ? 'Disconnecting…' : 'Disconnect' }}</button>
        </div>
      </div>
    </dialog>
  </section>
</template>

<style scoped>
.computers { container: computers / inline-size; margin-top: 28px; padding: 6px 8px 8px; border: 1px solid var(--line); border-radius: 16px; background: var(--surface-raised); }
.computers.embedded { margin-top: 0; }
.head { display: flex; align-items: center; gap: 10px; min-height: 44px; padding: 4px 6px 4px 10px; }
.head h2 { font: 650 15px/1.3 var(--font); letter-spacing: 0; }
.count { font-size: 12px; color: var(--ink-3); }
.spacer { flex: 1; }
.head .btn { flex-shrink: 0; }
.muted { padding: 0 10px 10px; color: var(--ink-2); font-size: 13px; }
.meta { color: var(--ink-2); font-size: 13px; }
.problem { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; margin: 8px 10px; color: var(--danger); }
.next-step { padding: 0 10px 8px; color: var(--ink-2); font-size: 13px; }
.sheet, .computer { display: grid; grid-template-columns: minmax(160px, 1.8fr) minmax(110px, 1fr) minmax(96px, .8fr) minmax(72px, .6fr) auto; gap: 6px 14px; align-items: center; }
.sheet span:nth-child(4), .last-active { min-width: 0; }
@container computers (max-width: 760px) {
  .sheet, .computer { grid-template-columns: minmax(140px, 1.6fr) auto auto auto; }
  .sheet span:nth-child(4), .last-active { display: none; }
}
.sheet { padding: 0 10px 6px; color: var(--ink-3); font: 500 10.5px/1 var(--mono); letter-spacing: .14em; text-transform: uppercase; font-variant-ligatures: none; }
.computer { padding: 8px 10px; border-top: 1px solid var(--line); }
.identity { display: flex; align-items: center; gap: 10px; min-width: 0; }
.identity-text { min-width: 0; }
.glyph { display: grid; place-items: center; flex: none; width: 32px; height: 32px; border-radius: 9px; background: var(--surface-sunken); color: var(--ink-2); }
.name { color: var(--ink); font-weight: 650; overflow-wrap: anywhere; }
.identity .meta { display: flex; align-items: baseline; min-width: 0; max-width: 100%; overflow: hidden; font-size: 12px; color: var(--ink-3); white-space: nowrap; }
.where, .sep { flex: none; }
.sep { padding: 0 .35em; }
.path { display: flex; min-width: 0; flex: 1 1 auto; }
.path-head { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.path-tail { flex: none; white-space: nowrap; }
.harness-line, .status { display: flex; align-items: center; gap: 6px; min-width: 0; color: var(--ink); font-size: 13px; white-space: nowrap; }
.harness-line.reported { display: grid; gap: 10px; }
.harness-report { display: flex; align-items: flex-start; gap: 6px; min-width: 0; }
.harness-report-text { display: grid; gap: 3px; min-width: 0; white-space: normal; }
.harness-report-text > span:first-child { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.harness-report :deep(svg) { flex-shrink: 0; margin-top: 2px; }
.harness-fix, .harness-hint { font-size: 12px; line-height: 1.45; color: var(--ink-2); overflow-wrap: anywhere; }
.harness-fix { font-family: var(--mono); }
.harness-state { color: var(--ink-2); }
.last-active { font-size: 12.5px; }
.dot { width: 7px; height: 7px; border-radius: 50%; background: var(--st-backlog); }
.dot[data-state="Connected"] { background: var(--ok); }
.dot[data-state="Draining"], .dot[data-state="Offline"] { background: var(--gold); }
.dot[data-state="Revoked"] { background: var(--danger); }
.actions { display: flex; align-items: center; justify-content: flex-end; gap: 4px; }
.details-toggle .chev { transition: transform .2s ease; }
.computer.open .details-toggle .chev { transform: rotate(90deg); }
@media (prefers-reduced-motion: reduce) { .details-toggle .chev { transition: none; } }
.detail { grid-column: 1 / -1; display: grid; gap: 8px; justify-items: start; padding: 2px 0 6px 42px; color: var(--ink-2); font-size: 12.5px; }
.detail ul { display: grid; gap: 4px; width: 100%; margin: 0; padding: 0; list-style: none; }
.detail li { display: flex; flex-wrap: wrap; align-items: center; gap: 4px 8px; min-height: 32px; }
.enrollment-name { color: var(--ink); font-weight: 550; }
.enrollment-meta { color: var(--ink-3); }
.remove { margin-left: auto; }
.head h2:focus { outline: none; }
.revoked { padding: 2px 4px 2px; border-top: 1px solid var(--line); }
.revoked-toggle { display: inline-flex; align-items: center; gap: 6px; min-height: 36px; padding: 0 8px 0 6px; border: 0; border-radius: 8px; background: none; color: var(--ink-2); font: 550 12.5px/1 var(--font); cursor: pointer; }
@media (hover: hover) { .revoked-toggle:hover { background: var(--row-hover); color: var(--ink); } }
.revoked-toggle:focus-visible { box-shadow: var(--focus-ring); }
.revoked-toggle .chev { transition: transform .2s ease; }
.revoked-toggle[aria-expanded="true"] .chev { transform: rotate(90deg); }
@media (prefers-reduced-motion: reduce) { .revoked-toggle .chev { transition: none; } }
.revoked-list { display: grid; margin: 0; padding: 0 0 4px; list-style: none; }
.revoked-list li { display: flex; flex-wrap: wrap; align-items: center; gap: 4px 10px; min-height: 40px; padding: 4px 6px; color: var(--ink-2); font-size: 13px; }
.glyph.small { width: 26px; height: 26px; border-radius: 7px; }
.revoked-name { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--ink); font-weight: 550; }
.revoked-meta { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--ink-3); font-size: 12px; }
.revoked-list .remove-computer, .revoked-reason { margin-left: auto; flex: none; }
.revoked-reason { color: var(--ink-3); font-size: 12px; }
.disconnect { width: min(420px, calc(100vw - 32px)); padding: 0; border: 0; background: transparent; color: var(--ink); }
.disconnect::backdrop { background: var(--scrim); }
.panel { padding: 18px 18px 16px; border: 1px solid var(--glass-edge); border-radius: 16px; background: var(--surface-raised); box-shadow: var(--shadow-pop); }
.panel header { display: flex; align-items: flex-start; justify-content: space-between; gap: 8px; }
.panel h2 { font: 600 18px/1.3 var(--font); }
.panel p, .panel li { font-size: 13.5px; color: var(--ink-2); }
.panel ul { display: grid; gap: 6px; margin: 10px 0 0; padding: 0; list-style: none; }
.runs { display: flex; gap: 8px; align-items: flex-start; margin-top: 12px; padding: 10px 12px; border-radius: 12px; background: var(--mark-hl); color: var(--ink); }
.choices { display: flex; align-items: center; justify-content: flex-end; flex-wrap: wrap; gap: 8px 12px; margin-top: 16px; }
.revoke { margin-right: auto; padding: 0; border: 0; background: none; color: var(--danger); font-size: 12.5px; font-weight: 550; cursor: pointer; }
@media (hover: hover) { .revoke:hover { text-decoration: underline; } }
.revoke:disabled { opacity: .55; cursor: default; text-decoration: none; }
.revoke:focus-visible { border-radius: 4px; box-shadow: var(--focus-ring); }
@media (max-width: 720px) {
  .computers { padding: 4px 4px 6px; }
  .sheet { display: none; }
  .computer { grid-template-columns: minmax(0, 1fr) auto; grid-template-areas: "id actions" "harness actions" "status actions"; row-gap: 4px; }
  .computer.has-reports { grid-template-areas: "id actions" "harness harness" "status status"; }
  .identity { grid-area: id; }
  .harness-line { grid-area: harness; padding-left: 42px; }
  .status { grid-area: status; padding-left: 42px; }
  .last-active { display: none; }
  .actions { grid-area: actions; align-self: center; }
  .detail { padding-left: 0; }
}
</style>
