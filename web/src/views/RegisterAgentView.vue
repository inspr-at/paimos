<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import AppIcon from '../components/AppIcon.vue'
import ConnectedComputers from '../components/agents/ConnectedComputers.vue'
import HarnessMark from '../components/agents/HarnessMark.vue'
import PairingUsePreview from '../components/agents/PairingUsePreview.vue'
import { resilientFetch } from '../lib/api'
import { addHarnessCommand, pairingCopy } from '../lib/attachCopy'
import { brand } from '../lib/brand'
import { displayLanguage } from '../lib/displayLanguage'
import { harnessLabel } from '../lib/agentState'
import { myPermissions, onAccessRevalidate, refreshPermissions } from '../lib/authz'
import { rememberSignInReturn } from '../lib/signInReturn'
import { useSession } from '../stores/session'
import { PairingFollow } from '../lib/pairingFollow'
import {
  DEFAULT_REVIEW_CHOICE, PUBLIC_PAIRING_GUIDE_PATH, PairingError,
  addHarnessTargetProblem, approvePairedAccounts, defaultSelectedAccountKeys, denyPairing, describeHarnessStatus, describeProgress,
  formatAllowanceMoment, getPairingComputer, getPairingGuide,
  isAddHarness, lookupPairing, pairingLiveCopy, pairingPermissions,
  pairingEventScope, pairingReadGeneration, peekPairingCode, platformCaption, presentPublicGuide, subscribePairingEvents,
  rememberPairingCode, setHarnessAccount, submitApproval, takePairingCode,
  chooseInstallMethod, installMethods, readPairingInstallMethod, writePairingInstallMethod,
  connectDisabledReason, denyDisabledReason, unsupportedVerification, verifiableAccountKeys, verificationWarning,
  type PairingGuide, type PairingView, type ReviewChoice,
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
const verificationChoice = ref(false)
const connectButton = ref<HTMLButtonElement | null>(null)
const verifyInput = ref<HTMLInputElement | null>(null)
const reviewTitle = ref<HTMLElement | null>(null)
// AEON-1502: everything beyond the one sentence sits behind Details, closed by default.
const detailsOpen = ref(false)
const words = computed(() => pairingCopy(displayLanguage(), brand.value.short_name))
const allowAgents = ref(true)
const approvalPending = ref(false)
const selected = ref<string[]>([])
const permissionsReady = ref(false)
const online = ref(typeof navigator === 'undefined' ? true : navigator.onLine)
const addTarget = ref<PairingView | null>(null)
const grantedKeys = ref<string[] | null>(null)
const platformKey = ref('')
const installMethod = ref('manual')
const copied = ref('')
const busy = ref('')
const message = ref('')
const nextStep = ref('')
const presentation = computed(() => guideLoaded.value ? presentPublicGuide(guide.value) : null)
const methods = computed(() => presentation.value ? installMethods(presentation.value) : [])
const fallbackCommand = computed(() => 'export PATH="$HOME/.local/bin:$PATH"\n' + (presentation.value?.setupCommand ?? ''))
const selectedTarget = computed(() => {
  const targets = presentation.value?.targets ?? []
  return targets.find(item => `${item.platform}/${item.arch}` === platformKey.value) ?? targets[0] ?? null
})
const progress = computed(() => current.value ? describeProgress(current.value) : null)
const liveCopy = computed(() => current.value ? pairingLiveCopy(current.value) : null)
const computersRefresh = ref(0)
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
// An expired or denied request is one sentence and one next step.
const ended = computed(() => progress.value?.phase === 'expired' || progress.value?.phase === 'denied')
const asking = computed(() => pendingReview.value && !ended.value)
const harnessNames = computed(() => accountGroups.value.map(group => harnessLabel(group.harness)))
const titleParts = computed(() => {
  const view = current.value
  if (!view) return []
  if (!addRequest.value) return words.value.pairTitle(view.computer_name, view.tenant_name)
  return words.value.addTitle(accountGroups.value.map(group => ({ name: harnessLabel(group.harness), account: accountName(group.harness) })), view.computer_name)
})
const limitLines = computed(() => terms.value ? words.value.limits(terms.value, formatAllowanceMoment(terms.value.expires_at)) : [])
const endedSentence = computed(() => {
  const view = current.value
  if (!view || !ended.value) return ''
  const adding = !!view.existing_computer_id
  if (progress.value?.phase === 'expired') return adding ? words.value.expiredAdd(harnessNames.value, view.computer_name) : words.value.expiredPair(view.computer_name, view.tenant_name)
  return adding ? words.value.deniedAdd(harnessNames.value, view.computer_name) : words.value.deniedPair(view.computer_name, view.tenant_name)
})
// Today's commands: add-harness for an existing computer, the guide's pair command when this page has it.
const endedCommand = computed(() => {
  const view = current.value
  if (!view || !ended.value) return ''
  return view.existing_computer_id ? addHarnessCommand(accountGroups.value.map(group => group.harness)) : presentation.value?.setupCommand ?? ''
})
const targetProblem = computed(() => current.value ? addHarnessTargetProblem({
  view: current.value,
  requestedComputerId: requestedComputer.value,
  targetName: addTarget.value?.computer_name ?? null,
}) : null)
const connectReason = computed(() => connectDisabledReason({
  busy: busy.value,
  canApprove: permissions.value.canApprove,
  selectedAccountKeys: selected.value,
  targetProblem: targetProblem.value,
}))
const denyReason = computed(() => denyDisabledReason({ busy: busy.value, canDeny: permissions.value.canDeny }))
// One live region at the buttons, rendered before it has text, so a reason or
// the verification choice is announced when it appears (AEON-402).
const actionNote = computed(() => (connectReason.value && words.value.reason(connectReason.value))
  || (verificationChoice.value ? words.value.choice(blockedLabels.value) : '')
  || (denyReason.value && words.value.reason(denyReason.value)) || '')
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
    if (!current.value.verification_capabilities) return words.value.notSaid
    return blockedLabels.value.length ? words.value.cannotVerify(blockedLabels.value) : words.value.unavailable
  }
  if (!verify.value || !verifiableLabels.value.length) return ''
  return blockedLabels.value.length
    ? `${words.value.canVerify(verifiableLabels.value)} ${words.value.cannotVerify(blockedLabels.value)}`
    : words.value.willVerify(verifiableLabels.value)
})
watch(verifyLocked, locked => { if (locked) verify.value = false })
watch([verify, selected, current], () => { verificationChoice.value = false })
// Follows the approved computer: polling with backoff, and a read when the live stream names it.
const follower = new PairingFollow({
  onView: view => { current.value = view; computersRefresh.value += 1 },
  onFailure: error => assignError(error, 'The computer status could not be refreshed.'),
})
let stopLive = () => {}

watch(guide, value => {
  const first = value?.install_targets[0]
  platformKey.value = first ? `${first.platform}/${first.arch}` : ''
  installMethod.value = chooseInstallMethod(installMethods(presentPublicGuide(value)), readPairingInstallMethod(browserStorage()))
})

function browserStorage(): Storage | null {
  try { return localStorage } catch { return null }
}

function rememberInstall(value: string) {
  writePairingInstallMethod(browserStorage(), value)
}
const stopAccess = onAccessRevalidate({ reset: clearSignedInPreview })
watch(() => session.requiresSignIn, ended => { if (ended) clearSignedInPreview() })

onMounted(async () => {
  stopLive = subscribePairingEvents(() => pairingEventScope(current.value), () => follower.poke())
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
  stopLive()
  follower.stop()
  window.removeEventListener('online', onLine)
  window.removeEventListener('offline', onLine)
})

function clearSignedInPreview() {
  current.value = null
  addTarget.value = null
  selected.value = []
  grantedKeys.value = null
  approvalPending.value = false
  allowAgents.value = true
  detailsOpen.value = false
  message.value = ''
  nextStep.value = ''
  busy.value = ''
  follower.stop()
  permissionsReady.value = false
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
    // A harness the computer reports as signed out is never selected (AEON-1502); every other default is today's.
    selected.value = defaultSelectedAccountKeys(found.requested_accounts).filter(key => !signedOut(found, found.requested_accounts.find(account => account.account_key === key)?.harness ?? ''))
    detailsOpen.value = false
    grantedKeys.value = null
    verify.value = verifiableHarnesses(found, selected.value).length > 0
    allowAgents.value = true
    approvalPending.value = false
    follower.follow(found)
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
  if (!current.value || connectReason.value) return
  // The choice is made in Details: verification off, or the harnesses left out. Nothing is sent before that.
  if (verificationBlocked.value.length) {
    verificationChoice.value = true
    detailsOpen.value = true
    await nextTick()
    verifyInput.value?.focus()
    return
  }
  message.value = ''
  nextStep.value = ''
  busy.value = 'approve'
  const started = pairingReadGeneration()
  const keys = [...selected.value]
  try {
    const approved = await submitApproval({
      view: current.value, choice: choice(), selectedAccountKeys: keys,
      permissions: permissions.value,
      requestedComputerId: requestedComputer.value,
      targetComputerName: addTarget.value?.computer_name ?? null,
    })
    if (started !== pairingReadGeneration()) return
    grantedKeys.value = keys
    current.value = approved
    // The daemon reports after this click. Follow it now; account approval must not hold the first read.
    follower.follow(approved)
    computersRefresh.value += 1
    if (allowAgents.value) await approveAccounts(approved, keys)
    if (started !== pairingReadGeneration()) return
  } catch (error) {
    if (error instanceof PairingError && error.code === 'session_reset') return
    if (started === pairingReadGeneration()) assignError(error, 'The computer was not connected.')
  } finally { if (started === pairingReadGeneration()) busy.value = '' }
  // Not connected: focus returns to Connect, enabled again, for a retry.
  if (started === pairingReadGeneration() && pendingReview.value && document.activeElement === reviewTitle.value) {
    await nextTick()
    connectButton.value?.focus()
  }
}

async function approveAccounts(pairing = current.value, keys = grantedKeys.value ?? []) {
  if (!pairing || !keys.length) return
  const started = pairingReadGeneration()
  approvalPending.value = true
  try {
    await approvePairedAccounts(pairing, keys, permissions.value)
    if (started !== pairingReadGeneration()) return
    approvalPending.value = false
    message.value = ''; nextStep.value = ''
  } catch (error) {
    if (started === pairingReadGeneration()) assignError(error, 'Account approval was not confirmed.')
  }
}
async function retryAccountApproval() {
  if (busy.value) return
  const started = pairingReadGeneration()
  busy.value = 'accounts'
  try { await approveAccounts() } finally { if (started === pairingReadGeneration()) busy.value = '' }
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
  detailsOpen.value = false
  grantedKeys.value = null
  approvalPending.value = false
  message.value = ''
  nextStep.value = ''
  follower.stop()
}

function reviewAsNewComputer() {
  const query = { ...route.query }
  delete query.computer
  addTarget.value = null
  void router.replace({ path: route.path, query })
}

function verifiableHarnesses(view: PairingView, keys: readonly string[]) {
  const verifiable = verifiableAccountKeys(view, keys)
  const names: string[] = []
  for (const key of verifiable) {
    const harness = view.requested_accounts.find(item => item.account_key === key)?.harness
    if (harness && !names.includes(harness)) names.push(harness)
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

/** The computer reported this harness as signed out; it cannot be selected here. */
function signedOut(view: PairingView | null, harness: string) {
  return !!view && (view.harness_details?.[harness]?.state === 'login_required' || view.harness_statuses?.[harness] === 'login_required')
}

/** The account a harness connects with: the selected ones, or the only one found. */
function accountName(harness: string) {
  const accounts = current.value?.requested_accounts.filter(account => account.harness === harness) ?? []
  const chosen = accounts.filter(account => selected.value.includes(account.account_key)).map(account => account.label)
  if (chosen.length) return chosen.join(', ')
  return accounts.length === 1 ? accounts[0]!.label : ''
}

function selectedKey(harness: string) {
  return selected.value.find(key => current.value?.requested_accounts.find(account => account.account_key === key)?.harness === harness) ?? ''
}

function toggleHarness(harness: string, accounts: PairingView['requested_accounts'], on: boolean) {
  if (!current.value) return
  if (isolatedAccounts(accounts)) {
    selected.value = selected.value.filter(key => !accounts.some(account => account.account_key === key))
    if (on) selected.value.push(...accounts.map(account => account.account_key))
  } else {
    const key = on ? (selectedKey(harness) || accounts[0]?.account_key || null) : null
    selected.value = setHarnessAccount(current.value.requested_accounts, selected.value, harness, key)
  }
}

function isolatedAccounts(accounts: PairingView['requested_accounts']) {
  return accounts.length > 1 && accounts.every(account => !!account.config_home_id) && new Set(accounts.map(account => account.config_home_id)).size === accounts.length
}

function toggleAccount(key: string, on: boolean) {
  selected.value = selected.value.filter(value => value !== key)
  if (on) selected.value.push(key)
}

function chooseAccount(harness: string, accountKey: string) {
  if (!current.value) return
  selected.value = setHarnessAccount(current.value.requested_accounts, selected.value, harness, accountKey || null)
}

function harnessState(enrollment: PairingView['enrollments'][number]) {
  return current.value ? describeHarnessStatus(current.value, enrollment.harness) : ''
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
    <header v-if="!asking && !ended" class="intro">
      <p class="eyebrow">Agents</p>
      <h1>Connect your machine</h1>
      <p class="lede">{{ adding ? 'Add a harness on a computer that is already paired. It keeps the same daemon.' : 'Pair a computer with agentd; no API key needed.' }}</p>
      <p class="key-path">For a CLI or script, <RouterLink to="/settings/access/agents?new=1">create an agent and key</RouterLink>.</p>
    </header>

    <ol v-if="!asking && !ended" class="steps" aria-label="Setup progress">
      <li v-for="(label, index) in STEPS" :key="label" :class="{ done: step > index + 1, current: step === index + 1 }">
        <span class="num" aria-hidden="true"><AppIcon v-if="step > index + 1" name="check" :size="12" /><template v-else>{{ index + 1 }}</template></span>
        <span>{{ label }}</span>
      </li>
    </ol>

    <p v-if="!online" class="banner" role="status"><AppIcon name="alert" :size="15" />You appear to be offline. The guide can still be read. Lookup, approval and disconnect need a connection.</p>
    <p v-if="addTarget && !current" class="banner">Adding a harness on {{ addTarget.computer_name }}. Start that on the computer, then enter the new code here. This page does not send a lifecycle secret.</p>
    <p v-else-if="requestedComputer && session.identity && !current" class="banner">Enter the code from the computer you are adding a harness to.</p>

    <section v-if="!current" class="card">
      <template v-if="!guideLoaded || !presentation">
        <p class="muted">Loading the guide for this {{ brand.short_name }}…</p>
      </template>
      <template v-else>
        <label v-if="methods.length" class="install-choice">Install on this computer
          <select v-model="installMethod" class="field" aria-label="Install on this computer" @change="rememberInstall(($event.target as HTMLSelectElement).value)">
            <option v-if="methods.includes('manual')" value="manual">macOS or Linux · direct download</option>
            <option v-if="methods.includes('homebrew')" value="homebrew">macOS · Homebrew</option>
            <option v-if="methods.includes('nix')" value="nix">{{ presentation.nixLabel }}</option>
          </select>
        </label>
        <p v-if="presentation.nixHint" class="nix-hint">{{ presentation.nixHint.replaceAll(' · ', '\u00a0·\u00a0') }}</p>
        <div v-if="presentation.homebrewCommand && installMethod === 'homebrew'" class="install-guide">
          <p class="copy">Run these from your project folder.</p>
          <pre class="command"><code>{{ presentation.homebrewCommand }}</code></pre>
          <button type="button" class="btn sm" @click="copyText(presentation.homebrewCommand, 'Homebrew commands')"><AppIcon :name="copied === 'Homebrew commands' ? 'check' : 'copy'" :size="13" />{{ copied === 'Homebrew commands' ? 'Copied' : 'Copy commands' }}</button>
          <p class="copy">Confirm the folder and accounts, then approve the code below to start the service.</p>
          <details class="service-details">
            <summary><AppIcon name="chevron-right" :size="12" class="disclosure-chev" />Release and upgrades</summary>
            <p class="copy">Homebrew installs the latest INSPR release; <code>aeon-agentd status</code> tells you if this server needs a different version.</p>
            <p class="copy">Upgrading or rerunning pair does not drain or restart an existing daemon; arrange a restart after work finishes and verify Touch ID on the new daemon.</p>
          </details>
        </div>
        <div v-if="methods.includes('manual') && installMethod === 'manual'" class="install-guide">
          <p v-if="!selectedTarget" class="copy">{{ presentation.installNote }}</p>
          <template v-else>
            <label>Platform
              <select v-model="platformKey" class="field" aria-label="Install platform">
                <option v-for="target in presentation.targets" :key="`${target.platform}/${target.arch}`" :value="`${target.platform}/${target.arch}`">{{ platformCaption(target.platform, target.arch) }}</option>
              </select>
            </label>
            <p class="copy">Direct download for this server’s version. Copy the checksum installer into your terminal, then pair from your project folder.</p>
            <button type="button" class="btn sm" @click="copyText(selectedTarget.command, 'Install command')"><AppIcon :name="copied === 'Install command' ? 'check' : 'copy'" :size="13" />{{ copied === 'Install command' ? 'Copied' : 'Copy checksum installer' }}</button>
            <details class="service-details"><summary><AppIcon name="chevron-right" :size="12" class="disclosure-chev" />Read installer</summary><pre class="command installer-source"><code>{{ selectedTarget.command }}</code></pre></details>
          </template>
          <pre class="command"><code>{{ fallbackCommand }}</code></pre>
          <button type="button" class="btn sm" @click="copyText(fallbackCommand, 'Fallback pair')"><AppIcon :name="copied === 'Fallback pair' ? 'check' : 'copy'" :size="13" />{{ copied === 'Fallback pair' ? 'Copied' : 'Copy pairing command' }}</button>
          <p class="copy">Approve the code below; only then is the user service installed.</p>
        </div>
        <div v-if="presentation.managedSetup && installMethod === 'nix'" class="nix-guide">
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
        </div>
        <details v-if="methods.length" class="service-details trouble">
          <summary><AppIcon name="chevron-right" :size="12" class="disclosure-chev" />Trouble?</summary>
          <p class="copy">If you see <code>usage: paimos-agentd setup|status…</code>, an older agentd is running first. Update the Nix pin to a release that includes pair, or run the path-proof command above. Add a harness with the same binary: <code>env "$(brew --prefix)/bin/aeon-agentd" add-harness</code> or <code>env "$HOME/.nix-profile/bin/aeon-agentd" add-harness</code>.</p>
        </details>
        <div v-if="presentation.address" class="agent-address">
          <p class="copy">Setting up another computer, or letting an agent do it? Share this address.</p>
          <div class="address">
            <code :title="presentation.address">{{ presentation.address }}</code>
            <button type="button" class="btn sm" aria-label="Copy guide address" @click="copyText(presentation.address, 'Address')"><AppIcon :name="copied === 'Address' ? 'check' : 'copy'" :size="13" />{{ copied === 'Address' ? 'Copied' : 'Copy' }}</button>
          </div>
        </div>
      </template>
      <p v-if="guideError" class="problem" role="alert">{{ guideError }} <button type="button" class="btn sm" @click="loadGuide">Try again</button></p>

      <details v-if="presentation" class="manual">
        <summary><AppIcon name="chevron-right" :size="12" class="disclosure-chev" />pi with OpenRouter</summary>
        <div class="manual-body">
          <p class="copy">On a paired computer, enter the key locally; choose the model in Settings, under Accounts.</p>
          <pre class="command"><code>aeon-agentd add-harness --harness pi --provider openrouter</code></pre>
          <p class="copy">For a new computer, add <code>--harness pi --provider openrouter</code> to the pairing command above.</p>
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
        </div>
      </details>
      <details v-if="presentation" class="manual">
        <summary><AppIcon name="chevron-right" :size="12" class="disclosure-chev" />Disconnect and uninstall</summary>
        <div class="manual-body">
          <pre class="command"><code>aeon-agentd disconnect</code></pre>
          <p class="copy">{{ installMethod === 'nix' ? 'Wait for “disconnected”: current work finishes and access is revoked.' : 'Wait for “disconnected”: current work finishes, access is revoked and the service is removed.' }}</p>
          <p class="copy">Vendor sign-ins and project files stay on your computer.</p>
          <template v-if="installMethod === 'homebrew'">
            <pre class="command"><code>brew uninstall aeon-agentd</code></pre>
          </template>
          <p v-else-if="installMethod === 'nix'" class="copy">Disable the service and remove the package in your Nix / Home Manager configuration, then apply it through its review path.</p>
          <p v-else class="copy">Remove the aeon-agentd link in ~/.local/bin and downloaded versions in ~/.local/lib/aeon.</p>
          <p class="copy">Keep private pairing state until cleanup and accounting recovery are complete.</p>
        </div>
      </details>
    </section>

    <section v-else-if="progress" class="card" :class="{ ask: asking || ended }" aria-live="polite" aria-label="Pairing review">
      <!-- AEON-1502: the routine approval is one sentence, Add or Deny; everything else is behind Details. -->
      <template v-if="asking">
        <header class="ask-head">
          <h1 ref="reviewTitle" tabindex="-1" class="ask-title" data-review-title><template v-for="(part, index) in titleParts" :key="index"><span v-if="part.kind === 'account'" class="acct">{{ part.text }}</span><span v-else-if="part.kind === 'computer'" class="host">{{ part.text }}</span><template v-else>{{ part.text }}</template></template></h1>
          <button type="button" class="btn sm ghost" :disabled="!!busy" @click="resetCode">{{ words.otherCode }}</button>
        </header>
        <ul v-if="addRequest && accountGroups.length > 1" class="who">
          <li v-for="group in accountGroups" :key="group.harness"><HarnessMark :harness="group.harness" :size="16" /><span class="name">{{ harnessLabel(group.harness) }}</span><span class="acct">{{ accountName(group.harness) || words.leftOut }}</span></li>
        </ul>
        <ul v-else-if="!addRequest" class="who checklist">
          <li v-for="group in accountGroups" :key="group.harness" class="harness" :class="{ off: signedOut(current, group.harness) }">
            <label class="check">
              <input type="checkbox" :checked="!!selectedKey(group.harness) && !signedOut(current, group.harness)" :disabled="signedOut(current, group.harness)" :aria-label="words.connectHarness(harnessLabel(group.harness))" @change="toggleHarness(group.harness, group.accounts, ($event.target as HTMLInputElement).checked)" />
              <HarnessMark :harness="group.harness" :size="16" />
              <span class="name">{{ harnessLabel(group.harness) }}</span>
              <span class="acct">{{ signedOut(current, group.harness) ? words.notSignedIn : accountName(group.harness) || words.leftOut }}</span>
            </label>
          </li>
        </ul>
        <p v-if="targetProblem" class="problem" role="alert">{{ targetProblem.message }} {{ targetProblem.next }}</p>
        <button v-if="targetProblem" type="button" class="btn sm" @click="reviewAsNewComputer">{{ words.newComputer }}</button>
        <p class="facts-line" data-review-facts>{{ words.facts(verify, allowAgents) }}</p>
        <p class="role" data-review-role><AppIcon name="users" :size="15" />{{ words.role }}</p>

        <div class="actions">
          <button ref="connectButton" class="btn primary go" type="button" :disabled="!!connectReason" :aria-describedby="connectReason || verificationChoice ? 'connect-reason' : undefined" @click="connect">{{ busy === 'approve' ? (addRequest ? words.adding : words.pairing) : addRequest ? words.add : words.pair }}</button>
          <button class="btn go" type="button" :disabled="!!denyReason" :aria-describedby="denyReason ? 'connect-reason' : undefined" @click="deny">{{ words.deny }}</button>
          <p id="connect-reason" class="connect-reason" :class="{ quiet: !actionNote }" role="status">{{ actionNote }}</p>
        </div>

        <details class="more" :open="detailsOpen" @toggle="detailsOpen = ($event.target as HTMLDetailsElement).open">
          <summary><AppIcon name="chevron-right" :size="12" class="disclosure-chev" />{{ words.details }}</summary>
          <dl class="rows">
            <div class="row"><dt>{{ words.workspace }}</dt><dd>{{ current.tenant_name }} · {{ platformCaption(current.platform, current.arch) }}</dd></div>
            <div class="row"><dt>{{ words.folder }}</dt><dd class="path">{{ current.workspace_path }}</dd></div>
            <div v-for="group in accountGroups" :key="group.harness" class="row harness-row">
              <dt>{{ words.account(harnessLabel(group.harness)) }}</dt>
              <dd>
                <div v-if="isolatedAccounts(group.accounts)" class="account-pick">
                  <label v-for="account in group.accounts" :key="account.account_key" class="check">
                    <input type="checkbox" :checked="selected.includes(account.account_key)" :disabled="signedOut(current, group.harness)" :aria-label="words.connectAccount(account.label)" @change="toggleAccount(account.account_key, ($event.target as HTMLInputElement).checked)" />
                    <span>{{ account.label }}</span>
                  </label>
                </div>
                <select v-else class="field account-pick" :value="selectedKey(group.harness)" :disabled="signedOut(current, group.harness)" :aria-label="words.accountFor(harnessLabel(group.harness))" @change="chooseAccount(group.harness, ($event.target as HTMLSelectElement).value)">
                  <option value="">{{ words.leaveOut }}</option>
                  <option v-for="account in group.accounts" :key="account.account_key" :value="account.account_key">{{ account.label }}</option>
                </select>
                <p v-if="verify && harnessCapability(group.harness).length" class="harness-note"><AppIcon name="info" :size="14" />{{ harnessReason(group.harness) }}</p>
              </dd>
            </div>
            <div class="row">
              <dt>{{ words.verification }}</dt>
              <dd class="verify" :class="{ locked: verifyLocked }">
                <label class="check"><input ref="verifyInput" v-model="verify" type="checkbox" :disabled="verifyLocked" /><span>{{ words.verifyLabel }}</span></label>
                <p v-if="verifyNote" class="sub" :title="blockedHarnesses.map(harness => harnessCapability(harness)[0]?.reason).filter(Boolean).join(' ')">{{ verifyNote }}</p>
                <template v-if="verify && terms && !blockedLabels.length">
                  <p v-for="line in limitLines" :key="line" class="limits">{{ line }}</p>
                  <p v-if="verificationWarning(terms)" class="problem" role="alert">{{ words.limitsDiffer }}</p>
                </template>
                <p v-if="verify && !verifyLocked && !current.verification" class="problem">{{ words.noTerms }}</p>
              </dd>
            </div>
            <PairingUsePreview :language="displayLanguage()" />
            <div class="row">
              <dt><label for="after-connecting">{{ words.after }}</label></dt>
              <dd>
                <select id="after-connecting" class="field" :value="allowAgents ? 'use' : 'paused'" @change="allowAgents = ($event.target as HTMLSelectElement).value === 'use'">
                  <option value="use">{{ words.allowAgents }}</option>
                  <option value="paused">{{ words.keepPaused }}</option>
                </select>
              </dd>
            </div>
          </dl>
          <p class="keep"><AppIcon name="shield" :size="14" />{{ words.footer }}</p>
        </details>
      </template>

      <template v-else-if="ended">
        <header class="ask-head">
          <h1 ref="reviewTitle" tabindex="-1" class="ask-title ended-title" data-review-title>{{ endedSentence }}</h1>
          <button type="button" class="btn sm ghost" :disabled="!!busy" @click="resetCode">{{ words.otherCode }}</button>
        </header>
        <p class="facts-line next-step" data-review-next><template v-if="endedCommand">{{ words.runAgain(current.computer_name).before }}<code class="command-inline">{{ endedCommand }}</code>{{ words.runAgain(current.computer_name).after }}</template><template v-else>{{ words.newRequest }}</template></p>
      </template>

      <template v-else>
      <header class="review-head">
        <div>
          <h2 ref="reviewTitle" tabindex="-1">{{ liveCopy?.title }}</h2>
          <p>{{ liveCopy?.detail }}</p>
        </div>
        <button type="button" class="btn sm ghost" :disabled="!!busy" @click="resetCode">Use a different code</button>
      </header>

      <div class="facts">
        <div><span class="glyph"><AppIcon name="monitor" :size="16" /></span><div><p class="k">Computer</p><p>{{ current.computer_name }}</p><p class="sub">{{ [platformCaption(current.platform, current.arch), ...current.capabilities.map(capability)].join(' · ') }}</p></div></div>
        <div><span class="glyph"><AppIcon name="layers" :size="16" /></span><div><p class="k">Workspace</p><p>{{ current.tenant_name }}</p></div></div>
        <div><span class="glyph"><AppIcon name="folder" :size="16" /></span><div><p class="k">Folder</p><p class="path" :data-tip="current.workspace_path"><span class="path-head">{{ pathParts(current.workspace_path).head }}</span><span class="path-tail">{{ pathParts(current.workspace_path).tail }}</span></p></div></div>
      </div>

      <div class="progress-copy">
        <p>{{ liveCopy?.next }}</p>
        <ul class="enrollments">
          <li v-for="enrollment in current.enrollments" :key="enrollment.account_id">
            <HarnessMark :harness="enrollment.harness" :size="16" />
            <div class="enrollment-text">
              <p><strong>{{ enrollment.label }}</strong> <span class="sub">{{ harnessLabel(enrollment.harness) }}</span></p>
              <p v-if="harnessState(enrollment)" class="sub">{{ harnessState(enrollment) }}</p>
              <p class="sub">{{ enrollmentDetail(enrollment) }}</p>
            </div>
          </li>
        </ul>
        <p v-if="grantedKeys && !approvalPending" class="sub">{{ allowAgents ? 'Agents may use the selected accounts once setup finishes.' : 'Agents stay paused. Turn them on in Settings / Accounts.' }}</p>
        <button v-if="approvalPending && permissions.canApproveAccounts" type="button" class="btn sm" :disabled="!!busy" @click="retryAccountApproval">Retry account approval</button>
      </div>
      </template>
    </section>

    <p v-if="message" class="problem" role="alert">{{ message }}</p>
    <p v-if="nextStep" class="next">{{ nextStep }}</p>

    <ConnectedComputers :permissions="permissions" :refresh-token="computersRefresh" />
  </article>
</template>

<style scoped>
.install-choice { display: grid; gap: 8px; color: var(--ink); font-weight: 600; }
.install-choice .field { font-weight: 500; }
.nix-hint, .pending-note { margin: 8px 0 0; color: var(--ink-2); font-size: 13px; }
.install-guide { display: grid; justify-items: start; gap: 10px; margin-top: 16px; }
.trouble { margin-top: 14px; }
.install-guide > label, .install-guide > details, .install-guide .command { width: 100%; min-width: 0; box-sizing: border-box; }
.install-guide > p { margin: 0; }
.installer-source { max-height: 280px; overflow: auto; }
.connect { width: min(760px, 100%); margin: 0 auto; padding: 28px var(--gutter) 48px; }
.intro h1 { margin-top: 6px; }
.lede, .sub, .next, .note, .copy { color: var(--ink-2); }
.lede { margin-top: 8px; }
.key-path { margin-top: 4px; font-size: 13px; color: var(--ink-2); }
.key-path a { text-decoration: underline; text-underline-offset: 3px; }
.sub, .note { font-size: 13px; }
.steps { display: flex; align-items: center; gap: 0; margin: 22px 0; padding: 0; list-style: none; }
.steps li { display: flex; align-items: center; gap: 8px; color: var(--ink-3); font-size: 13px; font-weight: 600; white-space: nowrap; }
.steps li:not(:last-child) { flex: 1; }
.steps li:not(:last-child)::after { content: ''; flex: 1; min-width: 12px; height: 1px; margin: 0 10px; background: var(--line-2); }
.steps .current { color: var(--ink); }
.steps .done { color: var(--ink-2); }
.num { display: grid; place-items: center; width: 22px; height: 22px; flex-shrink: 0; border-radius: 50%; background: var(--surface-sunken); color: var(--ink-2); font: 600 12px/1 var(--mono); }
.current .num, .done .num { background: var(--primary); color: var(--primary-on); }
.card { padding: 20px; border: 1px solid var(--line); border-radius: 16px; background: var(--surface-raised); }
.card h2, .card h3, .card h4 { margin: 20px 0 6px; }
.card h2 { font: 600 16px/1.3 var(--font); letter-spacing: 0; }
.card h3, .card h4 { font: 600 14px/1.3 var(--font); }
.card > section:first-child h2, .review-head h2 { margin-top: 0; }
.copy { overflow-wrap: anywhere; white-space: pre-wrap; }
.banner, .problem { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; margin: 0 0 12px; }
.banner { padding: 10px 12px; border-radius: 12px; background: var(--surface-sunken); color: var(--ink-2); }
.problem { color: var(--danger); }
/* Public guide commands and optional instance details. */
.address { min-width: 0; display: flex; align-items: center; gap: 8px; margin-top: 8px; padding: 6px 6px 6px 12px; border-radius: 10px; background: var(--surface-sunken); }
.agent-address { margin-top: 22px; }
.agent-address > p { margin: 0; }
.address code { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font: 13px/1.4 var(--mono); }
.address .btn, .command + .btn { gap: 6px; flex-shrink: 0; }
.code-form { display: grid; gap: 8px; margin-top: 20px; padding-top: 20px; border-top: 1px solid var(--line); }
.code-form label { font-weight: 600; font-size: 14px; }
.code-row { display: flex; flex-wrap: wrap; gap: 10px; }
.code { flex: 0 1 220px; min-width: 0; height: 44px; font: 500 16px/1 var(--mono); letter-spacing: .08em; }
.code-form .note { font-size: 12.5px; color: var(--ink-3); }
.manual { margin-top: 18px; padding-top: 14px; border-top: 1px solid var(--line); }
.manual summary { cursor: pointer; font-size: 13.5px; font-weight: 600; color: var(--ink-2); }
.manual-body { display: grid; grid-template-columns: minmax(0, 1fr); min-width: 0; gap: 6px; margin-top: 12px; font-size: 13px; }
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
/* AEON-1502: the ask. One sentence, the facts line, the role line, Add or Deny, then Details. */
.card.ask { padding: 24px 24px 18px; }
.ask-head { display: flex; align-items: flex-start; justify-content: space-between; gap: 16px; }
.ask-head .btn { flex-shrink: 0; margin-top: 4px; }
.ask-title { min-width: 0; font: 400 clamp(22px, 2.2vw, 27px)/1.3 var(--serif); letter-spacing: -.015em; overflow-wrap: anywhere; text-wrap: pretty; }
.ask-title:focus { outline: none; }
.ask-title .acct { font: 500 .66em/1.3 var(--font); letter-spacing: 0; color: var(--ink-2); }
.ask-title .host { font: 500 .8em/1.3 var(--mono); letter-spacing: 0; }
.who { display: grid; gap: 2px; margin: 12px 0 0; padding: 0; list-style: none; }
.who li { display: flex; align-items: center; gap: 10px; min-height: 32px; min-width: 0; font-size: 14px; }
.who .name { font-weight: 600; }
.who .acct { min-width: 0; overflow-wrap: anywhere; color: var(--ink-2); font-size: 13px; }
.checklist .check { display: flex; align-items: center; gap: 10px; min-width: 0; min-height: 36px; cursor: pointer; }
.checklist .off .check { cursor: default; color: var(--ink-3); }
.checklist .off .name { color: var(--ink-3); }
.check input { width: 16px; height: 16px; margin: 0; flex-shrink: 0; accent-color: var(--teal); }
.facts-line { margin-top: 14px; color: var(--ink-2); font-size: 13.5px; }
.role { display: flex; align-items: center; gap: 8px; margin-top: 6px; color: var(--ink-2); font-size: 13.5px; }
.role svg { flex-shrink: 0; color: var(--ink-3); }
.ask .actions { margin-top: 18px; padding-top: 0; border-top: 0; }
.ask > .problem { margin: 12px 0 8px; }
.next-step { margin-top: 12px; }
.command-inline { padding: 1px 6px; border-radius: 6px; background: var(--surface-sunken); font: 12.5px/1.6 var(--mono); overflow-wrap: anywhere; }
/* Details grows downward only: nothing above it moves when it opens. */
.more { margin-top: 14px; padding-top: 12px; border-top: 1px solid var(--line); }
.more > summary { min-height: 32px; cursor: pointer; color: var(--ink-2); font-size: 13.5px; font-weight: 600; }
/* No column gap: each row's hairline runs unbroken across label and value. */
.rows { display: grid; grid-template-columns: fit-content(15rem) minmax(0, 1fr); margin: 8px 0 0; }
.rows :deep(.row) { display: contents; }
.rows :deep(dt), .rows :deep(dd) { margin: 0; padding: 9px 0; border-top: 1px solid var(--line); font-size: 13.5px; line-height: 1.45; }
.rows :deep(dt) { padding-right: 20px; color: var(--ink-3); }
.rows :deep(dd) { min-width: 0; color: var(--ink); overflow-wrap: anywhere; }
.rows .path { font: 12.5px/1.6 var(--mono); }
.rows .field { height: 36px; max-width: 100%; }
.account-pick { display: grid; gap: 4px; }
.account-pick .check { display: flex; align-items: center; gap: 10px; min-height: 32px; cursor: pointer; }
.harness-note { display: flex; align-items: flex-start; gap: 6px; margin: 6px 0 0; font-size: 13px; color: var(--ink-2); }
.harness-note svg { flex-shrink: 0; margin-top: 2px; color: var(--ink-3); }
.verify .check { display: flex; align-items: center; gap: 10px; min-height: 28px; cursor: pointer; }
.verify.locked .check { cursor: default; }
.verify.locked input { opacity: .4; }
.verify .sub { margin-top: 2px; color: var(--ink-3); font-size: 12.5px; }
.limits { margin-top: 2px; color: var(--ink-3); font-size: 12.5px; }
.verify .problem { margin: 6px 0 0; font-size: 13px; }
.actions { display: flex; flex-wrap: wrap; align-items: center; gap: 10px; margin-top: 20px; padding-top: 16px; border-top: 1px solid var(--line); }
.connect-reason { flex-basis: 100%; margin: 0; color: var(--ink-2); font-size: 13px; }
/* Empty, the live region stays in the page for screen readers but takes no room. */
.connect-reason.quiet { position: absolute; width: 1px; height: 1px; overflow: hidden; clip-path: inset(50%); white-space: nowrap; }
.go { min-height: 44px; padding: 0 18px; }
.keep { display: flex; align-items: flex-start; gap: 8px; margin: 12px 0 0; color: var(--ink-3); font-size: 12.5px; line-height: 1.5; }
.keep svg { flex-shrink: 0; margin-top: 2px; }
.review-head { display: flex; align-items: flex-start; justify-content: space-between; gap: 12px; }
.review-head p { margin-top: 4px; color: var(--ink-2); font-size: 13.5px; }
.review-head h2:focus { outline: none; }
.review-head .btn { flex-shrink: 0; }
.progress-copy > p { margin-top: 16px; font-size: 13.5px; color: var(--ink-2); }
.enrollments { display: grid; gap: 8px; margin: 12px 0; padding: 0; list-style: none; }
.enrollments li { display: flex; align-items: flex-start; gap: 10px; padding: 10px 12px; border-radius: 12px; background: var(--surface-sunken); }
.enrollments li > :first-child { margin-top: 2px; }
.enrollment-text { min-width: 0; }
.enrollment-text .sub { margin-top: 2px; }
.sr { position: absolute; width: 1px; height: 1px; overflow: hidden; clip-path: inset(50%); }
@media (max-width: 720px) {
  .connect { padding: 18px 16px 32px; }
  .card { padding: 16px; }
  .steps { margin: 16px 0; }
  .steps li:not(:last-child)::after { margin: 0 8px; }
  .facts { grid-template-columns: 1fr; gap: 0; padding: 4px 12px; border-radius: 12px; background: var(--surface-sunken); }
  .facts > div { padding: 10px 0; border-radius: 0; background: none; }
  .facts > div + div { border-top: 1px solid var(--line); }
  .card.ask { padding: 18px 16px 14px; }
  .ask-head { flex-direction: column; gap: 8px; }
  .ask-head .btn { margin: 0 0 0 -11px; }
  .rows { grid-template-columns: minmax(0, 1fr); }
  .rows :deep(dt) { padding-right: 0; }
  .rows :deep(dd) { padding-top: 0; border-top: 0; }
  .rows .field { width: 100%; }
  .review-head { flex-direction: column; gap: 8px; }
  .review-head .btn { margin-left: -11px; }
  .code-row .code { flex: 1 1 100%; }
  .code-row .go { flex: 1 1 100%; }
  /* Phones: Add and Deny full width, stacked, primary first. */
  .ask .actions .go { flex: 1 1 100%; }
}
</style>
