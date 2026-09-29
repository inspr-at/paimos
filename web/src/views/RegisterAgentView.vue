<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import AppIcon from '../components/AppIcon.vue'
import ConnectedComputers from '../components/agents/ConnectedComputers.vue'
import HarnessMark from '../components/agents/HarnessMark.vue'
import { resilientFetch } from '../lib/api'
import { brand } from '../lib/brand'
import { harnessLabel } from '../lib/agentState'
import { myPermissions, onAccessChange, refreshPermissions } from '../lib/authz'
import { rememberSignInReturn } from '../lib/signInReturn'
import { useSession } from '../stores/session'
import {
  DEFAULT_REVIEW_CHOICE, PUBLIC_PAIRING_GUIDE_PATH, PairingError,
  addHarnessTargetProblem, createOngoingLimits, defaultSelectedAccountKeys, denyPairing, describeProgress,
  emptyRequestLimit, formatAllowanceMoment, formatVerification, getPairingComputer, getPairingGuide,
  isAddHarness, lookupPairing, matchOngoingLimit, ongoingLimitAccounts, ongoingLimitError, pairingPermissions,
  pairingReadGeneration, peekPairingCode, planOngoingLimits, planPoll, platformCaption, presentPublicGuide,
  rememberPairingCode, setHarnessAccount, simpleRequestLimitError, submitApproval, takePairingCode,
  unsupportedVerification, verificationWarning,
  type OngoingLimitDraft, type PairingGuide, type PairingView, type ReviewChoice,
} from '../lib/agentPairing'

const COMPUTER_ID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i
const STEPS = ['Setup', 'Connect', 'Verify'] as const

const session = useSession()
const route = useRoute()
const router = useRouter()
const guide = ref<PairingGuide | null>(null)
const guideLoaded = ref(false)
const guideError = ref('')
const code = ref('')
const current = ref<PairingView | null>(null)
const verify = ref(true)
const limitsWhen = ref<'later' | 'now'>('later')
const limitsOpen = ref(false)
const selected = ref<string[]>([])
const drafts = ref<Record<string, OngoingLimitDraft>>({})
const limitState = ref<Record<string, 'saved' | 'uncertain' | 'failed' | 'absent'>>({})
const permissionsReady = ref(false)
const online = ref(typeof navigator === 'undefined' ? true : navigator.onLine)
const addTarget = ref<PairingView | null>(null)
const grantedKeys = ref<string[] | null>(null)
const platformKey = ref('')
const copied = ref('')
const busy = ref('')
const message = ref('')
const nextStep = ref('')
const presentation = computed(() => guideLoaded.value ? presentPublicGuide(guide.value) : null)
const selectedTarget = computed(() => {
  const targets = presentation.value?.targets ?? []
  return targets.find(item => `${item.platform}/${item.arch}` === platformKey.value) ?? targets[0] ?? null
})
const progress = computed(() => current.value ? describeProgress(current.value) : null)
const permissions = computed(() => pairingPermissions({
  permissions: [...myPermissions()],
  principalKind: session.identity?.principal.kind,
}))
const requestedComputer = computed(() => {
  const value = route.query.computer
  return typeof value === 'string' && COMPUTER_ID.test(value) ? value : ''
})
const adding = computed(() => current.value ? isAddHarness(current.value) : !!requestedComputer.value)
const addRequest = computed(() => !!current.value && isAddHarness(current.value))
const step = computed(() => {
  const phase = progress.value?.phase
  if (!current.value || phase === 'enter_code') return 1
  if (phase === 'review' || current.value.state === 'pending') return 2
  return 3
})
const accountGroups = computed(() => {
  const groups = new Map<string, PairingView['requested_accounts']>()
  for (const account of current.value?.requested_accounts ?? []) {
    const list = groups.get(account.harness) ?? []
    list.push(account)
    groups.set(account.harness, list)
  }
  return [...groups.entries()].map(([harness, accounts]) => ({ harness, accounts }))
})
const terms = computed(() => verify.value ? current.value?.verification ?? null : null)
const pendingReview = computed(() => current.value?.state === 'pending')
const showLimitForm = computed(() => limitsWhen.value === 'now' || limitsOpen.value || Object.values(limitState.value).some(item => item !== 'saved'))
const limitAccounts = computed(() => current.value ? ongoingLimitAccounts({
  pending: pendingReview.value,
  selectedKeys: selected.value,
  grantedKeys: grantedKeys.value,
  limitsNow: limitsWhen.value === 'now',
  showAll: limitsOpen.value && limitsWhen.value !== 'now',
  requested: current.value.requested_accounts,
  enrollments: current.value.enrollments,
}) : [])
const targetProblem = computed(() => current.value ? addHarnessTargetProblem({
  view: current.value,
  requestedComputerId: requestedComputer.value,
  targetName: addTarget.value?.computer_name ?? null,
}) : null)
const selectedHarnesses = computed(() => {
  const view = current.value
  if (!view) return [] as string[]
  const names: string[] = []
  for (const key of selected.value) {
    const harness = view.requested_accounts.find(item => item.account_key === key)?.harness
    if (harness && !names.includes(harness)) names.push(harness)
  }
  return names
})
const blockedHarnesses = computed(() => {
  if (!current.value || !pendingReview.value) return [] as string[]
  return unsupportedVerification(current.value, selected.value).map(item => item.harness)
})
const verifiableLabels = computed(() => selectedHarnesses.value.filter(harness => !blockedHarnesses.value.includes(harness)).map(harness => harnessLabel(harness)))
const blockedLabels = computed(() => blockedHarnesses.value.map(harness => harnessLabel(harness)))
// Nothing selected can be checked: verification stays off and the control cannot be turned on.
const verifyLocked = computed(() => selectedHarnesses.value.length > 0 && verifiableLabels.value.length === 0)
const verificationBlocked = computed(() => {
  if (!current.value || !pendingReview.value || !verify.value || verifyLocked.value) return []
  return unsupportedVerification(current.value, selected.value)
})
const verifyNote = computed(() => {
  if (!pendingReview.value || !current.value) return ''
  if (verifyLocked.value) {
    if (!current.value.verification_capabilities) return `This ${brand.value.short_name} has not said which harnesses can be verified.`
    const names = listNames(blockedLabels.value)
    return names ? `${names} can’t be verified.` : 'Verification is unavailable.'
  }
  if (!verify.value || !verifiableLabels.value.length) return ''
  const will = `${listNames(verifiableLabels.value)} will be verified.`
  return blockedLabels.value.length ? `${will} Leave out ${listNames(blockedLabels.value)}.` : will
})
watch(verifyLocked, locked => { if (locked) verify.value = false })
let pollTimer = 0
let pollStarted = 0

watch(guide, value => {
  const first = value?.install_targets[0]
  platformKey.value = first ? `${first.platform}/${first.arch}` : ''
})
const stopAccess = onAccessChange(change => { if (change === 'reset') clearSignedInPreview() })
watch(() => session.requiresSignIn, ended => { if (ended) clearSignedInPreview() })

onMounted(async () => {
  window.addEventListener('online', onLine)
  window.addEventListener('offline', onLine)
  await loadGuide()
  await prepareSession()
  if (requestedComputer.value && session.identity && permissions.value.canListComputers) {
    const started = pairingReadGeneration()
    try {
      const target = await getPairingComputer(requestedComputer.value)
      if (started === pairingReadGeneration()) addTarget.value = target
    } catch (error) {
      if (!(error instanceof PairingError && error.code === 'session_reset')) addTarget.value = null
    }
  }
  const stored = peekPairingCode()
  if (!stored) return
  code.value = stored
  if (!session.identity) return
  takePairingCode()
  await lookup()
})
onBeforeUnmount(() => {
  stopAccess()
  if (pollTimer) window.clearTimeout(pollTimer)
  window.removeEventListener('online', onLine)
  window.removeEventListener('offline', onLine)
})

function clearSignedInPreview() {
  current.value = null
  addTarget.value = null
  selected.value = []
  grantedKeys.value = null
  drafts.value = {}
  limitState.value = {}
  limitsOpen.value = false
  message.value = ''
  nextStep.value = ''
  busy.value = ''
  pollStarted = 0
  permissionsReady.value = false
  if (pollTimer) window.clearTimeout(pollTimer)
}

function onLine() { online.value = navigator.onLine }

async function loadGuide() {
  guideError.value = ''
  guideLoaded.value = false
  try { guide.value = await getPairingGuide() }
  catch (error) { guideError.value = error instanceof PairingError ? `${error.message} ${error.next}`.trim() : 'The public guide could not be loaded.' }
  finally { guideLoaded.value = true }
}

async function prepareSession() {
  const response = await resilientFetch('/api/me', {
    credentials: 'same-origin', cache: 'no-store', headers: { Accept: 'application/json' },
  })
  if (!response.ok) return
  await session.refresh()
  await refreshPermissions()
  permissionsReady.value = true
}

async function lookup() {
  message.value = ''
  nextStep.value = ''
  if (!online.value) {
    message.value = 'You appear to be offline.'
    nextStep.value = 'The guide on this page can still be read. Lookup needs a connection.'
    return
  }
  if (!session.identity) {
    if (!rememberPairingCode(code.value)) {
      message.value = 'Enter the 9-digit code from the computer, with or without dashes.'
      nextStep.value = 'A code match does not grant access.'
      return
    }
    rememberSignInReturn(PUBLIC_PAIRING_GUIDE_PATH)
    await router.push({ path: '/signin', query: { return: PUBLIC_PAIRING_GUIDE_PATH } })
    return
  }
  busy.value = 'lookup'
  const started = pairingReadGeneration()
  try {
    const found = await lookupPairing(code.value)
    if (started !== pairingReadGeneration()) return
    current.value = found
    selected.value = defaultSelectedAccountKeys(found.requested_accounts)
    grantedKeys.value = null
    verify.value = verifiableHarnesses(found, selected.value).length > 0
    limitsWhen.value = 'later'
    limitsOpen.value = false
    limitState.value = {}
    armPoll(found)
  } catch (error) {
    if (error instanceof PairingError && error.code === 'session_reset') return
    if (started !== pairingReadGeneration()) return
    current.value = null
    assignError(error, 'The code could not be looked up.')
  } finally { if (started === pairingReadGeneration()) busy.value = '' }
}

function choice(): ReviewChoice {
  return verify.value ? DEFAULT_REVIEW_CHOICE : 'connect_only'
}

async function connect() {
  if (!current.value || busy.value) return
  message.value = ''
  nextStep.value = ''
  if (limitsWhen.value === 'now') {
    for (const key of selected.value) {
      const problem = simpleRequestLimitError(draftFor(key))
      if (problem) {
        message.value = problem
        nextStep.value = 'Ongoing limits are saved by you after approval. They are not part of the pairing grant.'
        return
      }
    }
  }
  busy.value = 'approve'
  const started = pairingReadGeneration()
  const keys = [...selected.value]
  try {
    const approved = await submitApproval({
      view: current.value, choice: choice(), selectedAccountKeys: keys,
      permissions: permissions.value, drafts: Object.values(drafts.value),
      requestedComputerId: requestedComputer.value,
      targetComputerName: addTarget.value?.computer_name ?? null,
    })
    if (started !== pairingReadGeneration()) return
    grantedKeys.value = keys
    current.value = approved
    if (limitsWhen.value === 'now') await saveLimits(approved, keys)
    if (started !== pairingReadGeneration()) return
    armPoll(approved)
  } catch (error) {
    if (error instanceof PairingError && error.code === 'session_reset') return
    if (started === pairingReadGeneration()) assignError(error, 'The computer was not connected.')
  } finally { if (started === pairingReadGeneration()) busy.value = '' }
}

async function saveLimits(pairing: PairingView, keys = grantedKeys.value ?? []) {
  const started = pairingReadGeneration()
  const plan = planOngoingLimits({
    choice: 'ongoing_limits', drafts: keys.map(draftFor), selectedAccountKeys: keys,
    enrollments: pairing.enrollments, permissions: permissions.value,
  })
  if (plan.action === 'blocked') { message.value = plan.message; nextStep.value = plan.next; return }
  if (plan.action !== 'send') return
  const windows = plan.windows.filter(item => limitState.value[item.accountId] !== 'saved')
  if (!windows.length) return
  try {
    const result = await createOngoingLimits(windows, permissions.value)
    if (started !== pairingReadGeneration()) return
    const next = { ...limitState.value }
    for (const id of [...result.created, ...result.reconciled]) next[id] = 'saved'
    limitState.value = next
  } catch (error) {
    if (started !== pairingReadGeneration()) return
    if (error instanceof PairingError) {
      const next = { ...limitState.value }
      for (const id of error.savedAccountIds) next[id] = 'saved'
      const failed = windows.find(item => !error.savedAccountIds.includes(item.accountId))
      if (failed) next[failed.accountId] = error.code === 'allowance_uncertain' ? 'uncertain' : 'failed'
      limitState.value = next
      message.value = error.message
      nextStep.value = error.next
      return
    }
    assignError(error, 'The allowance was not confirmed.')
  }
}

async function checkLimit(accountId: string, key: string) {
  const started = pairingReadGeneration()
  const draft = draftFor(key)
  if (draft.allowance == null || simpleRequestLimitError(draft)) {
    message.value = simpleRequestLimitError(draft) ?? 'Enter the requests and the period.'
    nextStep.value = 'Checking looks for an allowance already saved. It does not send a new one.'
    return
  }
  busy.value = `check:${accountId}`
  const match = await matchOngoingLimit(accountId, {
    starts_at: new Date(draft.starts_at).toISOString(), ends_at: new Date(draft.ends_at).toISOString(),
    unit: 'requests', allowance: draft.allowance, pace_model: 'unrestricted', burst_ratio: 0,
  })
  if (started !== pairingReadGeneration()) return
  limitState.value = { ...limitState.value, [accountId]: match === 'saved' ? 'saved' : match === 'absent' ? 'absent' : 'uncertain' }
  if (match === 'unknown') {
    message.value = 'The allowance may already be saved.'
    nextStep.value = 'Refresh this account before sending the allowance again. Do not approve the pairing again.'
  } else if (match === 'absent') {
    message.value = 'No matching allowance is saved for this account.'
    nextStep.value = 'You can send it for this account. Do not approve the pairing again.'
  } else {
    message.value = ''
    nextStep.value = ''
  }
  busy.value = ''
}

async function saveOne(accountId: string, key: string) {
  if (!current.value || busy.value) return
  const started = pairingReadGeneration()
  const problem = simpleRequestLimitError(draftFor(key))
  if (problem) { message.value = problem; nextStep.value = 'Send the allowance for this account only. Do not approve the pairing again.'; return }
  busy.value = `save:${accountId}`
  message.value = ''
  nextStep.value = ''
  const plan = planOngoingLimits({
    choice: 'ongoing_limits', drafts: [draftFor(key)], selectedAccountKeys: [key],
    enrollments: current.value.enrollments, permissions: permissions.value,
  })
  if (plan.action === 'blocked') { message.value = plan.message; nextStep.value = plan.next; busy.value = ''; return }
  if (plan.action === 'send') {
    try {
      const result = await createOngoingLimits(plan.windows.filter(item => item.accountId === accountId), permissions.value)
      if (started !== pairingReadGeneration()) return
      if (result.created.includes(accountId) || result.reconciled.includes(accountId)) limitState.value = { ...limitState.value, [accountId]: 'saved' }
    } catch (error) {
      if (started !== pairingReadGeneration()) return
      if (error instanceof PairingError) {
        limitState.value = { ...limitState.value, [accountId]: error.code === 'allowance_uncertain' ? 'uncertain' : 'failed' }
        message.value = error.message
        nextStep.value = error.next
      } else assignError(error, 'The allowance was not confirmed.')
    }
  }
  busy.value = ''
}

async function deny() {
  if (!current.value || busy.value) return
  const started = pairingReadGeneration()
  busy.value = 'deny'
  message.value = ''
  nextStep.value = ''
  try {
    const denied = await denyPairing(current.value, permissions.value)
    if (started === pairingReadGeneration()) current.value = denied
  }
  catch (error) {
    if (started !== pairingReadGeneration()) return
    if (error instanceof PairingError && error.code === 'session_reset') return
    assignError(error, 'The request was not denied.')
  }
  finally { if (started === pairingReadGeneration()) busy.value = '' }
}

function resetCode() {
  current.value = null
  grantedKeys.value = null
  message.value = ''
  nextStep.value = ''
  pollStarted = 0
  if (pollTimer) window.clearTimeout(pollTimer)
}

function reviewAsNewComputer() {
  const query = { ...route.query }
  delete query.computer
  addTarget.value = null
  void router.replace({ path: route.path, query })
}

function verifiableHarnesses(view: PairingView, keys: readonly string[]) {
  const blocked = new Set(unsupportedVerification(view, keys).map(item => item.harness))
  const names: string[] = []
  for (const key of keys) {
    const harness = view.requested_accounts.find(item => item.account_key === key)?.harness
    if (harness && !blocked.has(harness) && !names.includes(harness)) names.push(harness)
  }
  return names
}

function pathParts(path: string) {
  const cut = Math.max(path.lastIndexOf('/'), path.lastIndexOf('\\'))
  if (cut <= 0 || cut >= path.length - 1) return { head: path, tail: '' }
  return { head: path.slice(0, cut + 1), tail: path.slice(cut + 1) }
}

function harnessCapability(harness: string) {
  return current.value ? unsupportedVerification(current.value, current.value.requested_accounts.filter(account => account.harness === harness).map(account => account.account_key)) : []
}

async function copyText(value: string, label: string) {
  if (!value) return
  try {
    await navigator.clipboard.writeText(value)
    copied.value = label
  } catch {
    copied.value = ''
    message.value = `${label} could not be copied.`
    nextStep.value = 'Select the text and copy it from the page.'
  }
}

function selectedKey(harness: string) {
  return selected.value.find(key => current.value?.requested_accounts.find(account => account.account_key === key)?.harness === harness) ?? ''
}

function toggleHarness(harness: string, accounts: PairingView['requested_accounts'], on: boolean) {
  if (!current.value) return
  const key = on ? (selectedKey(harness) || accounts[0]?.account_key || null) : null
  selected.value = setHarnessAccount(current.value.requested_accounts, selected.value, harness, key)
}

function chooseAccount(harness: string, accountKey: string) {
  if (!current.value) return
  selected.value = setHarnessAccount(current.value.requested_accounts, selected.value, harness, accountKey || null)
}

function draftFor(key: string): OngoingLimitDraft {
  return drafts.value[key] ?? emptyRequestLimit(key)
}

function setDraft(key: string, patch: Partial<OngoingLimitDraft>) {
  drafts.value = { ...drafts.value, [key]: { ...draftFor(key), ...patch, account_key: key, unit: 'requests', pace_model: 'unrestricted', burst_ratio: 0 } }
}

function armPoll(pairing: PairingView) {
  if (pollTimer) window.clearTimeout(pollTimer)
  if (!pollStarted) pollStarted = Date.now()
  const computerId = pairing.computer_id
  const phase = describeProgress(pairing).phase
  const failed = pairing.setup_state === 'setup_failed' || pairing.enrollments.some(item => item.verification_state === 'failed' || item.verification_state === 'cancelled' || item.verification_state === 'expired' || item.verification_state === 'ownership_lost')
  if (!computerId || failed || phase === 'connected' || phase === 'expired' || phase === 'denied' || phase === 'revoked') return
  const plan = planPoll({ startedAt: pollStarted, now: Date.now(), intervalSeconds: pairing.interval_seconds, state: pairing.state })
  if (plan.action === 'stop') return
  pollTimer = window.setTimeout(() => void refreshComputer(computerId), plan.delayMs)
}

async function refreshComputer(computerId: string) {
  try {
    const started = pairingReadGeneration()
    const pairing = await getPairingComputer(computerId)
    if (started !== pairingReadGeneration() || current.value?.computer_id !== computerId) return
    current.value = pairing
    armPoll(pairing)
  } catch (error) {
    if (error instanceof PairingError && error.code === 'session_reset') return
    if (error instanceof PairingError && error.code === 'rate_limited') {
      const plan = planPoll({ startedAt: pollStarted, now: Date.now(), rateLimited: true, retryAfterSeconds: error.retryAfterSeconds, state: current.value?.state })
      if (plan.action === 'wait') pollTimer = window.setTimeout(() => void refreshComputer(computerId), plan.delayMs)
      return
    }
    assignError(error, 'The computer status could not be refreshed.')
  }
}

function assignError(error: unknown, fallback: string) {
  if (error instanceof PairingError) { message.value = error.message; nextStep.value = error.next; return }
  message.value = fallback
  nextStep.value = 'Try again when you have a connection.'
}

function capability(value: string) {
  return value === 'managed_runs' ? 'Managed runs' : value
}

/** The reason without the harness name the row already shows. */
function harnessReason(harness: string) {
  const reason = harnessCapability(harness)[0]?.reason ?? ''
  const label = harnessLabel(harness)
  if (!reason.toLowerCase().startsWith(`${label.toLowerCase()} `)) return reason
  const rest = reason.slice(label.length + 1)
  return rest.charAt(0).toUpperCase() + rest.slice(1)
}

function listNames(names: string[]) {
  if (names.length < 2) return names[0] ?? ''
  return `${names.slice(0, -1).join(', ')} and ${names.at(-1)}`
}

function enrollmentDetail(enrollment: PairingView['enrollments'][number]) {
  const parts: string[] = [enrollment.state.replace(/_/g, ' ')]
  if (enrollment.verification_state && enrollment.verification_state !== 'not_selected') {
    parts.push(`verification ${enrollment.verification_state === 'unavailable' ? 'unavailable' : enrollment.verification_state.replace(/_/g, ' ')}`)
  }
  if (enrollment.verification_error && enrollment.verification_error !== 'verification_unavailable') parts.push(enrollment.verification_error)
  if (enrollment.local_processes) parts.push(`local processes ${enrollment.local_processes}`)
  if (enrollment.accounting_state === 'unconfirmed') parts.push('accounting unconfirmed')
  return parts.join(' · ')
}
</script>

<template>
  <article class="connect" data-aeon-pairing-guide="pairing-v1">
    <header class="intro">
      <p class="eyebrow">Agents</p>
      <h1>Connect a computer</h1>
      <p class="lede">{{ adding ? 'Add a harness on a computer that is already paired. It keeps the same daemon.' : `Use your existing AI accounts with ${brand.short_name}.` }}</p>
    </header>

    <ol class="steps" aria-label="Setup progress">
      <li v-for="(label, index) in STEPS" :key="label" :class="{ done: step > index + 1, current: step === index + 1 }">
        <span class="num" aria-hidden="true"><AppIcon v-if="step > index + 1" name="check" :size="12" /><template v-else>{{ index + 1 }}</template></span>
        <span>{{ label }}</span>
      </li>
    </ol>

    <p v-if="!online" class="banner" role="status"><AppIcon name="alert" :size="15" />You appear to be offline. The guide can still be read. Lookup, approval and disconnect need a connection.</p>
    <p v-if="addRequest && !targetProblem" class="banner">Adding a harness on {{ current?.computer_name }}. It keeps the same daemon. This page does not send a lifecycle secret.</p>
    <p v-else-if="addTarget && !current" class="banner">Adding a harness on {{ addTarget.computer_name }}. Start that on the computer, then enter the new code here. This page does not send a lifecycle secret.</p>
    <p v-else-if="requestedComputer && session.identity && !current" class="banner">Enter the code from the computer you are adding a harness to.</p>

    <section v-if="!current" class="card">
      <template v-if="!guideLoaded || !presentation">
        <p class="muted">Loading the guide for this {{ brand.short_name }}…</p>
      </template>
      <template v-else>
        <ol class="howto">
          <li v-for="(stepText, index) in presentation.steps" :key="stepText">
            <span class="howto-num" aria-hidden="true">{{ index + 1 }}</span>
            <div class="howto-body">
              <p>{{ stepText }}</p>
              <div v-if="index === 0" class="address">
                <code :title="presentation.address">{{ presentation.address || `This ${brand.short_name} has not published its address yet.` }}</code>
                <button v-if="presentation.address" type="button" class="btn sm" @click="copyText(presentation.address, 'Address')"><AppIcon :name="copied === 'Address' ? 'check' : 'copy'" :size="13" />{{ copied === 'Address' ? 'Copied' : 'Copy' }}</button>
              </div>
            </div>
          </li>
        </ol>
      </template>
      <p v-if="guideError" class="problem" role="alert">{{ guideError }} <button type="button" class="btn sm" @click="loadGuide">Try again</button></p>

      <details v-if="presentation?.managedSetup" class="manual nix-guide">
        <summary><AppIcon name="chevron-right" :size="12" class="disclosure-chev" />Nix / Home Manager</summary>
        <div class="manual-body">
          <p v-if="presentation.managedSetup.platform_note" class="copy">{{ presentation.managedSetup.platform_note }}</p>
          <p v-if="presentation.managedSetup.prerequisite_note" class="copy">{{ presentation.managedSetup.prerequisite_note }}</p>
          <p class="copy">Run this from your project folder, not your home folder.</p>
          <pre class="command"><code>{{ presentation.managedSetup.command }}</code></pre>
          <button type="button" class="btn sm" @click="copyText(presentation.managedSetup.command, 'Pair command')"><AppIcon :name="copied === 'Pair command' ? 'check' : 'copy'" :size="13" />{{ copied === 'Pair command' ? 'Copied' : 'Copy pairing command' }}</button>
          <p class="copy">Confirm the folder and accounts, then enter the code below.</p>
          <details class="service-details">
            <summary><AppIcon name="chevron-right" :size="12" class="disclosure-chev" />Declarative service</summary>
            <p class="copy">Home Manager keeps ownership of the binary and service.</p>
            <a :href="presentation.managedSetup.module_url" target="_blank" rel="noopener noreferrer"><code>{{ presentation.managedSetup.service_option }}</code></a>
            <p class="copy">{{ presentation.managedSetup.service_note }}</p>
          </details>
        </div>
      </details>

      <form class="code-form" @submit.prevent="lookup">
        <label for="pairing-code">Pairing code</label>
        <div class="code-row">
          <input id="pairing-code" v-model="code" class="field code" name="user-code" autocomplete="one-time-code" inputmode="numeric" spellcheck="false" placeholder="123-456-789" />
          <button class="btn primary go" type="submit" :disabled="!!busy">{{ busy === 'lookup' ? 'Looking up…' : session.identity ? 'Look up code' : 'Sign in to review the code' }}</button>
        </div>
        <p v-if="presentation" class="note">{{ presentation.note }}</p>
        <p v-if="permissionsReady && session.identity && !permissions.canLookup" class="note">Only a signed-in person who can manage accounts can review a pairing code. An agent session cannot approve it.</p>
      </form>
      <details v-if="presentation" class="manual">
        <summary><AppIcon name="chevron-right" :size="12" class="disclosure-chev" />Manual and agent setup</summary>
        <div class="manual-body">
        <p v-for="paragraph in presentation.manualParagraphs" :key="paragraph" class="copy">{{ paragraph }}</p>
        <p class="copy install-note"><AppIcon name="shield" :size="14" />{{ presentation.installNote }}</p>
        <label v-if="presentation.targets.length > 1">Platform
          <select v-model="platformKey" class="field" aria-label="Install platform">
            <option v-for="target in presentation.targets" :key="`${target.platform}/${target.arch}`" :value="`${target.platform}/${target.arch}`">{{ platformCaption(target.platform, target.arch) }}</option>
          </select>
        </label>
        <template v-if="selectedTarget">
          <p class="k">{{ platformCaption(selectedTarget.platform, selectedTarget.arch) }}</p>
          <pre class="command"><code>{{ selectedTarget.command }}</code></pre>
          <button type="button" class="btn sm" @click="copyText(selectedTarget.command, 'Install command')">{{ copied === 'Install command' ? 'Copied' : 'Copy install command' }}</button>
        </template>
        <template v-if="presentation.setupCommand">
          <p class="k">Setup command</p>
          <pre class="command"><code>{{ presentation.setupCommand }}</code></pre>
          <button type="button" class="btn sm" @click="copyText(presentation.setupCommand, 'Setup command')">{{ copied === 'Setup command' ? 'Copied' : 'Copy setup command' }}</button>
        </template>
        </div>
      </details>
    </section>

    <section v-else-if="progress" class="card" aria-live="polite" aria-label="Pairing review">
      <header class="review-head">
        <div>
          <h2>{{ adding && pendingReview ? 'Add a harness' : pendingReview ? 'Review this computer' : progress.title }}</h2>
          <p>{{ pendingReview ? 'Confirm the details and choose which harnesses to connect.' : progress.detail }}</p>
        </div>
        <button type="button" class="btn sm ghost" :disabled="!!busy" @click="resetCode">Use a different code</button>
      </header>
      <p v-if="targetProblem" class="problem" role="alert">{{ targetProblem.message }} {{ targetProblem.next }}</p>
      <button v-if="targetProblem" type="button" class="btn sm" @click="reviewAsNewComputer">Review as a new computer</button>

      <div class="facts">
        <div><span class="glyph"><AppIcon name="monitor" :size="16" /></span><div><p class="k">Computer</p><p>{{ current.computer_name }}</p><p class="sub">{{ [platformCaption(current.platform, current.arch), ...current.capabilities.map(capability)].join(' · ') }}</p></div></div>
        <div><span class="glyph"><AppIcon name="layers" :size="16" /></span><div><p class="k">Workspace</p><p>{{ current.tenant_name }}</p></div></div>
        <div><span class="glyph"><AppIcon name="folder" :size="16" /></span><div><p class="k">Folder</p><p class="path" :data-tip="current.workspace_path"><span class="path-head">{{ pathParts(current.workspace_path).head }}</span><span class="path-tail">{{ pathParts(current.workspace_path).tail }}</span></p></div></div>
      </div>

      <template v-if="pendingReview">
        <h3>Harnesses</h3>
        <p class="sub">Each selected harness connects with one account.</p>
        <div v-for="group in accountGroups" :key="group.harness" class="harness">
          <label class="check">
            <input type="checkbox" :checked="!!selectedKey(group.harness)" :aria-label="`Connect ${harnessLabel(group.harness)}`" @change="toggleHarness(group.harness, group.accounts, ($event.target as HTMLInputElement).checked)" />
            <HarnessMark :harness="group.harness" />
            <span>{{ harnessLabel(group.harness) }}</span>
          </label>
          <label class="account-pick">
            <span class="sr">Account for {{ harnessLabel(group.harness) }}</span>
            <select class="field" :value="selectedKey(group.harness)" :aria-label="`Account for ${harnessLabel(group.harness)}`" @change="chooseAccount(group.harness, ($event.target as HTMLSelectElement).value)">
              <option value="">Leave out</option>
              <option v-for="account in group.accounts" :key="account.account_key" :value="account.account_key">{{ account.label }}</option>
            </select>
          </label>
          <p v-if="verify && harnessCapability(group.harness).length" class="harness-note"><AppIcon name="info" :size="14" />{{ harnessReason(group.harness) }}</p>
        </div>

        <label class="verify" :class="{ locked: verifyLocked }">
          <input v-model="verify" type="checkbox" :disabled="verifyLocked" />
          <span>
            <strong>Verify selected harnesses</strong>
            <span v-if="verifyNote" class="sub">{{ verifyNote }}</span>
            <span v-if="verify && terms && !blockedLabels.length" class="sub">{{ formatVerification(terms) }}</span>
          </span>
          <time v-if="verify && terms && !blockedLabels.length" class="expiry" :datetime="terms.expires_at"><AppIcon name="clock" :size="14" />until {{ formatAllowanceMoment(terms.expires_at) }}</time>
        </label>
        <p v-if="verify && terms && !blockedLabels.length && verificationWarning(terms)" class="problem" role="alert">{{ verificationWarning(terms) }}</p>
        <p v-if="verify && !verifyLocked && !current.verification" class="problem">The server did not include verification terms. Leave verification off, or look the code up again.</p>

        <fieldset>
          <legend>After connecting</legend>
          <label class="radio"><input v-model="limitsWhen" type="radio" value="later" /> <span><strong>Keep ongoing runs paused</strong><span class="sub">You can set request limits later.</span></span></label>
          <label class="radio"><input v-model="limitsWhen" type="radio" value="now" /> <span><strong>Set ongoing limits</strong><span class="sub">Requests for a period, saved by you after approval.</span></span></label>
        </fieldset>
      </template>

      <div v-else class="progress-copy">
        <p>{{ progress.next }}</p>
        <ul class="enrollments">
          <li v-for="enrollment in current.enrollments" :key="enrollment.account_id">
            <HarnessMark :harness="enrollment.harness" :size="16" />
            <div class="enrollment-text">
              <p><strong>{{ enrollment.label }}</strong> <span class="sub">{{ harnessLabel(enrollment.harness) }}</span></p>
              <p class="sub">{{ enrollmentDetail(enrollment) }}</p>
            </div>
          </li>
        </ul>
        <button v-if="permissions.canSetOngoingLimits && current.enrollments.some(item => item.state === 'connected')" type="button" class="btn sm" @click="limitsOpen = !limitsOpen">{{ limitsOpen ? 'Hide ongoing limits' : 'Set ongoing limits' }}</button>
      </div>

      <div v-if="showLimitForm && limitAccounts.length" class="limits">
        <h3>{{ limitsOpen && limitsWhen !== 'now' && !pendingReview ? 'Ongoing limits for connected accounts' : 'Ongoing limits for this request' }}</h3>
        <p class="sub">{{ limitsOpen && limitsWhen !== 'now' && !pendingReview ? 'Every connected account on this computer, including ones this request did not select.' : `Requests per period for each selected account, in your local time. An ${brand.short_name} allowance, not the vendor subscription.` }}</p>
        <div v-for="account in limitAccounts" :key="account.key" class="limit">
          <h4>{{ account.label }}</h4>
          <p v-if="account.id && limitState[account.id] === 'saved'">Requests for this period are saved.</p>
          <template v-else>
            <div class="limit-fields">
            <label>Requests
              <input class="field" type="number" min="1" step="1" :value="draftFor(account.key).allowance ?? ''" @input="setDraft(account.key, { allowance: ($event.target as HTMLInputElement).value === '' ? null : Number(($event.target as HTMLInputElement).value) })" />
            </label>
            <label>Starts
              <input class="field" type="datetime-local" :value="draftFor(account.key).starts_at" @input="setDraft(account.key, { starts_at: ($event.target as HTMLInputElement).value })" />
            </label>
            <label>Ends
              <input class="field" type="datetime-local" :value="draftFor(account.key).ends_at" @input="setDraft(account.key, { ends_at: ($event.target as HTMLInputElement).value })" />
            </label>
            </div>
            <p v-if="ongoingLimitError(draftFor(account.key)) && (draftFor(account.key).allowance || draftFor(account.key).starts_at)" class="problem">{{ simpleRequestLimitError(draftFor(account.key)) }}</p>
            <p v-if="account.id && limitState[account.id] === 'uncertain'" class="problem">This allowance may already be saved. Check it before sending again.</p>
            <div v-if="account.id" class="row-actions">
              <button v-if="limitState[account.id] === 'uncertain'" type="button" class="btn sm" :disabled="!!busy" @click="checkLimit(account.id, account.key)">Check again</button>
              <button v-else type="button" class="btn sm" :disabled="!!busy" @click="saveOne(account.id, account.key)">Save allowance</button>
            </div>
          </template>
        </div>
      </div>

      <div v-if="pendingReview" class="actions">
        <button class="btn primary go" type="button" :disabled="!!busy || !permissions.canApprove || !selected.length || !!targetProblem || verificationBlocked.length > 0" @click="connect">{{ busy === 'approve' ? (addRequest ? 'Adding…' : 'Connecting…') : addRequest ? 'Add harness' : 'Connect computer' }}</button>
        <button class="btn" type="button" :disabled="!!busy || !permissions.canDeny" @click="deny">Deny</button>
        <p class="keep"><AppIcon name="shield" :size="14" />Vendor sign-ins and project files stay on the computer.</p>
      </div>
      <p v-if="pendingReview && !selected.length" class="note">Choose at least one harness.</p>
      <p v-if="session.identity && permissionsReady && !permissions.canApprove" class="note">Only a signed-in person who can manage accounts can connect this computer.</p>
    </section>

    <p v-if="message" class="problem" role="alert">{{ message }}</p>
    <p v-if="nextStep" class="next">{{ nextStep }}</p>

    <ConnectedComputers :permissions="permissions" />
  </article>
</template>

<style scoped>
.connect { width: min(760px, 100%); margin: 0 auto; padding: 28px var(--gutter) 48px; }
.intro h1 { margin-top: 6px; }
.lede, .sub, .next, .note, .copy { color: var(--ink-2); }
.lede { margin-top: 8px; }
.sub, .note { font-size: 13px; }
.steps { display: flex; align-items: center; gap: 0; margin: 22px 0; padding: 0; list-style: none; }
.steps li { display: flex; align-items: center; gap: 8px; color: var(--ink-3); font-size: 13px; font-weight: 600; white-space: nowrap; }
.steps li:not(:last-child) { flex: 1; }
.steps li:not(:last-child)::after { content: ''; flex: 1; min-width: 12px; height: 1px; margin: 0 10px; background: var(--line-2); }
.steps .current { color: var(--ink); }
.steps .done { color: var(--ink-2); }
.num { display: grid; place-items: center; width: 22px; height: 22px; flex-shrink: 0; border-radius: 50%; background: var(--surface-sunken); color: var(--ink-2); font: 600 12px/1 var(--mono); }
.current .num, .done .num { background: var(--teal); color: var(--surface); }
.card { padding: 20px; border: 1px solid var(--line); border-radius: 16px; background: var(--surface-raised); }
.card h2, .card h3, .card h4 { margin: 20px 0 6px; }
.card h2 { font: 600 16px/1.3 var(--font); letter-spacing: 0; }
.card h3, .card h4 { font: 600 14px/1.3 var(--font); }
.card > section:first-child h2, .review-head h2 { margin-top: 0; }
.copy { overflow-wrap: anywhere; white-space: pre-wrap; }
.banner, .problem { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; margin: 0 0 12px; }
.banner { padding: 10px 12px; border-radius: 12px; background: var(--surface-sunken); color: var(--ink-2); }
.problem { color: var(--danger); }
.limit label { display: grid; gap: 6px; font-size: 13px; color: var(--ink-2); }
/* Public guide: three numbered steps, the address inside the first. */
.howto { display: grid; grid-template-columns: minmax(0, 1fr); gap: 14px; margin: 0; padding: 0; list-style: none; }
.howto li { display: flex; gap: 12px; min-width: 0; }
.howto-num { display: grid; place-items: center; width: 24px; height: 24px; flex-shrink: 0; border-radius: 50%; background: var(--surface-sunken); color: var(--ink-2); font: 600 12px/1 var(--mono); }
.howto-body { flex: 1; min-width: 0; padding-top: 2px; }
.howto-body > p { color: var(--ink); }
.address { display: flex; align-items: center; gap: 8px; margin-top: 8px; padding: 6px 6px 6px 12px; border-radius: 10px; background: var(--surface-sunken); }
.address code { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font: 13px/1.4 var(--mono); }
.address .btn, .command + .btn { gap: 6px; flex-shrink: 0; }
.code-form { display: grid; gap: 8px; margin-top: 20px; padding-top: 20px; border-top: 1px solid var(--line); }
.code-form label { font-weight: 600; font-size: 14px; }
.code-row { display: flex; flex-wrap: wrap; gap: 10px; }
.code { flex: 0 1 220px; min-width: 0; height: 44px; font: 500 16px/1 var(--mono); letter-spacing: .08em; }
.code-form .note { font-size: 12.5px; color: var(--ink-3); }
.manual { margin-top: 18px; padding-top: 14px; border-top: 1px solid var(--line); }
.manual summary { cursor: pointer; font-size: 13.5px; font-weight: 600; color: var(--ink-2); }
.manual-body { display: grid; gap: 6px; margin-top: 12px; font-size: 13px; }
.install-note { display: flex; gap: 8px; margin-top: 6px; padding: 10px 12px; border-radius: 10px; background: var(--surface-sunken); white-space: normal; }
.install-note svg { flex-shrink: 0; margin-top: 2px; color: var(--ink-3); }
.manual-body .k { margin-top: 8px; }
.command { display: block; margin: 0 0 4px; padding: 12px; overflow-wrap: anywhere; white-space: pre-wrap; border-radius: 10px; background: var(--surface-sunken); font: 12.5px/1.5 var(--mono); }
.manual-body .btn { justify-self: start; }
.service-details { font-size: 13px; color: var(--ink-2); }
.service-details summary { cursor: pointer; color: var(--ink-2); }
.service-details p, .service-details a { display: block; margin-top: 8px; overflow-wrap: anywhere; }
/* Review */
.facts { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 10px; margin-top: 16px; }
.facts > div { display: flex; gap: 10px; min-width: 0; padding: 12px; border-radius: 12px; background: var(--surface-sunken); }
.facts .sub { font-size: 12.5px; }
.glyph { display: grid; place-items: center; width: 24px; height: 24px; flex-shrink: 0; color: var(--ink-3); }
.k { color: var(--ink-3); font-size: 11px; letter-spacing: .08em; text-transform: uppercase; }
.path { display: flex; min-width: 0; overflow: hidden; }
.path-head { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.path-tail { flex: none; white-space: nowrap; }
.harness, .verify, .radio { display: flex; align-items: center; gap: 10px; min-height: 52px; margin-top: 8px; padding: 10px 12px; border-radius: 12px; background: var(--surface-sunken); }
.harness { justify-content: space-between; flex-wrap: wrap; }
.check, .verify, .radio { cursor: pointer; }
.check { display: flex; align-items: center; gap: 10px; min-width: 0; font-weight: 600; }
.check input, .verify input, .radio input { width: 16px; height: 16px; margin: 0; flex-shrink: 0; accent-color: var(--teal); }
.harness-note { display: flex; align-items: flex-start; gap: 6px; flex-basis: 100%; margin: 0; padding-left: 26px; font-size: 13px; color: var(--ink-2); }
.harness-note svg { flex-shrink: 0; margin-top: 2px; color: var(--ink-3); }
.account-pick { flex: 0 1 220px; }
.account-pick .field { height: 36px; }
.verify { align-items: flex-start; margin-top: 16px; }
.verify.locked { cursor: default; }
.verify input { margin-top: 2px; }
.verify.locked input { opacity: .4; }
.verify input:disabled { cursor: default; }
.verify > span { flex: 1; min-width: 0; }
.verify strong, .radio strong { display: block; color: var(--ink); font-weight: 600; }
.verify .sub { display: block; margin-top: 2px; color: var(--ink-3); font-size: 12.5px; font-weight: 400; }
.expiry { display: inline-flex; align-items: center; gap: 6px; color: var(--ink-3); font-size: 12.5px; white-space: nowrap; }
fieldset { margin: 20px 0 0; padding: 0; border: 0; }
legend { margin-bottom: 4px; color: var(--ink); font-weight: 600; font-size: 14px; }
.radio { align-items: flex-start; }
.radio input { margin-top: 2px; }
.radio .sub { display: block; margin-top: 2px; }
.actions { display: flex; flex-wrap: wrap; align-items: center; gap: 10px; margin-top: 20px; padding-top: 16px; border-top: 1px solid var(--line); }
.go { min-height: 44px; padding: 0 18px; }
.keep { display: inline-flex; align-items: center; gap: 6px; margin: 0 0 0 auto; color: var(--ink-3); font-size: 12.5px; }
.review-head { display: flex; align-items: flex-start; justify-content: space-between; gap: 12px; }
.review-head p { margin-top: 4px; color: var(--ink-2); font-size: 13.5px; }
.review-head .btn { flex-shrink: 0; }
.progress-copy > p { margin-top: 16px; font-size: 13.5px; color: var(--ink-2); }
.enrollments { display: grid; gap: 8px; margin: 12px 0; padding: 0; list-style: none; }
.enrollments li { display: flex; align-items: flex-start; gap: 10px; padding: 10px 12px; border-radius: 12px; background: var(--surface-sunken); }
.enrollments li > :first-child { margin-top: 2px; }
.enrollment-text { min-width: 0; }
.enrollment-text .sub { margin-top: 2px; }
.row-actions { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; margin-top: 10px; }
.limit { margin-top: 12px; padding-top: 12px; border-top: 1px solid var(--line); }
.limit h4 { margin-top: 0; }
.limit-fields { display: grid; grid-template-columns: minmax(0, 1fr) minmax(0, 1.4fr) minmax(0, 1.4fr); gap: 10px; margin-top: 8px; }
.limit .problem { margin: 8px 0 0; }
.sr { position: absolute; width: 1px; height: 1px; overflow: hidden; clip-path: inset(50%); }
@media (max-width: 720px) {
  .connect { padding: 18px 16px 32px; }
  .card { padding: 16px; }
  .steps { margin: 16px 0; }
  .steps li:not(:last-child)::after { margin: 0 8px; }
  .facts { grid-template-columns: 1fr; gap: 0; padding: 4px 12px; border-radius: 12px; background: var(--surface-sunken); }
  .facts > div { padding: 10px 0; border-radius: 0; background: none; }
  .facts > div + div { border-top: 1px solid var(--line); }
  .harness, .verify { flex-wrap: wrap; }
  .account-pick { flex-basis: 100%; }
  .expiry { display: none; }
  .review-head { flex-direction: column; gap: 8px; }
  .review-head .btn { margin-left: -11px; }
  .code-row .code { flex: 1 1 100%; }
  .code-row .go { flex: 1 1 100%; }
  .limit-fields { grid-template-columns: 1fr; }
  .actions .go { flex: 1 1 auto; }
  .keep { flex-basis: 100%; margin-left: 0; }
}
</style>
