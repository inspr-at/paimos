<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import AppIcon from '../components/AppIcon.vue'
import ConnectedComputers from '../components/agents/ConnectedComputers.vue'
import HarnessMark from '../components/agents/HarnessMark.vue'
import { resilientFetch } from '../lib/api'
import { harnessLabel } from '../lib/agentState'
import { myPermissions, refreshPermissions } from '../lib/authz'
import { rememberSignInReturn } from '../lib/signInReturn'
import { useSession } from '../stores/session'
import {
  DEFAULT_REVIEW_CHOICE, PUBLIC_PAIRING_GUIDE_PATH, PairingError,
  createOngoingLimits, defaultSelectedAccountKeys, denyPairing, describeProgress,
  emptyRequestLimit, formatAllowanceMoment, formatVerification, getPairingComputer, getPairingGuide,
  isAddHarness, lookupPairing, matchOngoingLimit, ongoingLimitError, pairingPermissions, peekPairingCode,
  planOngoingLimits, planPoll, platformCaption, publicGuideSections, rememberPairingCode, setHarnessAccount,
  simpleRequestLimitError, submitApproval, takePairingCode, verificationWarning,
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
const busy = ref('')
const message = ref('')
const nextStep = ref('')
const sections = computed(() => guideLoaded.value ? publicGuideSections(guide.value) : [])
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
const limitAccounts = computed(() => {
  if (!current.value) return []
  if (pendingReview.value) return selected.value.map(key => ({ key, id: '', label: current.value?.requested_accounts.find(account => account.account_key === key)?.label ?? key }))
  return current.value.enrollments.filter(item => item.state !== 'revoked').map(item => ({ key: item.account_key, id: item.account_id, label: item.label }))
})
let pollTimer = 0
let pollStarted = 0

onMounted(async () => {
  window.addEventListener('online', onLine)
  window.addEventListener('offline', onLine)
  await loadGuide()
  await prepareSession()
  if (requestedComputer.value && session.identity && permissions.value.canListComputers) {
    try { addTarget.value = await getPairingComputer(requestedComputer.value) } catch { addTarget.value = null }
  }
  const stored = peekPairingCode()
  if (!stored) return
  code.value = stored
  if (!session.identity) return
  takePairingCode()
  await lookup()
})
onBeforeUnmount(() => {
  if (pollTimer) window.clearTimeout(pollTimer)
  window.removeEventListener('online', onLine)
  window.removeEventListener('offline', onLine)
})

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
  try {
    current.value = await lookupPairing(code.value)
    selected.value = defaultSelectedAccountKeys(current.value.requested_accounts)
    verify.value = true
    limitsWhen.value = 'later'
    limitsOpen.value = false
    limitState.value = {}
    armPoll(current.value)
  } catch (error) {
    current.value = null
    assignError(error, 'The code could not be looked up.')
  } finally { busy.value = '' }
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
  try {
    const approved = await submitApproval({
      view: current.value, choice: choice(), selectedAccountKeys: selected.value,
      permissions: permissions.value, drafts: Object.values(drafts.value),
    })
    current.value = approved
    if (limitsWhen.value === 'now') await saveLimits(approved)
    armPoll(approved)
  } catch (error) { assignError(error, 'The computer was not connected.') }
  finally { busy.value = '' }
}

async function saveLimits(pairing: PairingView) {
  const keys = limitAccounts.value.map(item => item.key)
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
    const next = { ...limitState.value }
    for (const id of [...result.created, ...result.reconciled]) next[id] = 'saved'
    limitState.value = next
  } catch (error) {
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
      if (result.created.includes(accountId) || result.reconciled.includes(accountId)) limitState.value = { ...limitState.value, [accountId]: 'saved' }
    } catch (error) {
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
  busy.value = 'deny'
  message.value = ''
  nextStep.value = ''
  try { current.value = await denyPairing(current.value, permissions.value) }
  catch (error) { assignError(error, 'The request was not denied.') }
  finally { busy.value = '' }
}

function resetCode() {
  current.value = null
  message.value = ''
  nextStep.value = ''
  if (pollTimer) window.clearTimeout(pollTimer)
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
    const pairing = await getPairingComputer(computerId)
    current.value = pairing
    armPoll(pairing)
  } catch (error) {
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
</script>

<template>
  <article class="connect" data-aeon-pairing-guide="pairing-v1">
    <header class="intro">
      <p class="eyebrow">Agents</p>
      <h1>Connect a computer</h1>
      <p class="lede">{{ adding ? 'Add a harness on a computer that is already paired. It keeps the same daemon.' : 'Use your existing AI accounts with Aeon.' }}</p>
    </header>

    <ol class="steps" aria-label="Setup progress">
      <li v-for="(label, index) in STEPS" :key="label" :class="{ done: step > index + 1, current: step === index + 1 }">
        <span class="num" aria-hidden="true"><AppIcon v-if="step > index + 1" name="check" :size="12" /><template v-else>{{ index + 1 }}</template></span>
        <span>{{ label }}</span>
      </li>
    </ol>

    <p v-if="!online" class="banner" role="status"><AppIcon name="alert" :size="15" />You appear to be offline. The guide can still be read. Lookup, approval and disconnect need a connection.</p>
    <p v-if="addTarget" class="banner">Adding a harness on {{ addTarget.computer_name }}. Start that on the computer, then enter the new code here. This page does not send a lifecycle secret.</p>
    <p v-else-if="requestedComputer && session.identity" class="banner">Enter the code from the computer you are adding a harness to.</p>

    <section v-if="!current" class="card">
      <template v-if="!guideLoaded">
        <p class="muted">Loading the guide for this Aeon…</p>
      </template>
      <template v-else>
        <section v-for="(section, index) in sections" :key="section.heading">
          <h2 v-if="index > 0">{{ section.heading }}</h2>
          <p v-for="paragraph in section.paragraphs" :key="paragraph" class="copy">{{ paragraph }}</p>
        </section>
      </template>
      <p v-if="guideError" class="problem" role="alert">{{ guideError }} <button type="button" class="btn sm" @click="loadGuide">Try again</button></p>

      <form @submit.prevent="lookup">
        <h2>Code from the computer</h2>
        <label>Pairing code
          <input v-model="code" class="field" name="user-code" autocomplete="one-time-code" inputmode="numeric" spellcheck="false" />
        </label>
        <button class="btn primary go" type="submit" :disabled="!!busy">{{ busy === 'lookup' ? 'Looking up…' : session.identity ? 'Look up code' : 'Sign in to review the code' }}</button>
        <p v-if="permissionsReady && session.identity && !permissions.canLookup" class="note">Only a signed-in person who can manage accounts can review a pairing code. An agent session cannot approve it.</p>
      </form>
    </section>

    <section v-else-if="progress" class="card" aria-live="polite">
      <header class="review-head">
        <div>
          <h2>{{ adding && pendingReview ? 'Add a harness' : pendingReview ? 'Review this computer' : progress.title }}</h2>
          <p>{{ pendingReview ? 'Confirm the details and choose which harnesses to connect.' : progress.detail }}</p>
        </div>
        <button type="button" class="btn sm" @click="resetCode">Use a different code</button>
      </header>
      <p v-if="current && requestedComputer && current.existing_computer_id && current.existing_computer_id !== requestedComputer" class="problem" role="alert">This code is not an add-harness request for the computer you opened.</p>
      <p v-else-if="requestedComputer && pendingReview && !current.existing_computer_id" class="problem" role="alert">This code starts a new computer. It does not add a harness to the computer you opened.</p>

      <div class="facts">
        <div><span class="glyph"><AppIcon name="monitor" :size="16" /></span><div><p class="k">Computer</p><p>{{ current.computer_name }}</p><p class="sub">{{ platformCaption(current.platform, current.arch) }}</p></div></div>
        <div><span class="glyph"><AppIcon name="layers" :size="16" /></span><div><p class="k">Workspace</p><p>{{ current.tenant_name }}</p></div></div>
        <div><span class="glyph"><AppIcon name="folder" :size="16" /></span><div><p class="k">Folder</p><p class="path">{{ current.workspace_path }}</p></div></div>
      </div>
      <p v-if="current.capabilities.length" class="sub caps">{{ current.capabilities.map(capability).join(', ') }}</p>

      <template v-if="pendingReview">
        <h3>Select harnesses</h3>
        <p class="sub">Choose which accounts to connect. A harness can be left out. Each selected harness uses one account.</p>
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
        </div>

        <label class="verify">
          <input v-model="verify" type="checkbox" />
          <span>
            <strong>Verify selected harnesses</strong>
            <span class="sub">One short read-only verification per selected harness. One at a time. No repository changes or privileged actions.</span>
          </span>
          <span v-if="terms" class="expiry"><AppIcon name="clock" :size="14" />{{ terms.allowance }} {{ terms.unit }} · until {{ formatAllowanceMoment(terms.expires_at) }}</span>
        </label>
        <p v-if="terms" class="sub exact">Server allowance: {{ formatVerification(terms) }}</p>
        <p v-if="terms && verificationWarning(terms)" class="problem" role="alert">{{ verificationWarning(terms) }}</p>
        <p v-if="verify && !current.verification" class="problem">The server did not include verification terms. Leave verification off, or look the code up again.</p>

        <fieldset>
          <legend>After connecting</legend>
          <label class="radio"><input v-model="limitsWhen" type="radio" value="later" /> <span><strong>Connect only</strong><span class="sub">You can set request limits later.</span></span></label>
          <label class="radio"><input v-model="limitsWhen" type="radio" value="now" /> <span><strong>Set ongoing limits</strong><span class="sub">Requests for a period, saved by you after approval.</span></span></label>
        </fieldset>
      </template>

      <div v-else class="progress-copy">
        <p>{{ progress.next }}</p>
        <ul class="enrollments">
          <li v-for="enrollment in current.enrollments" :key="enrollment.account_id">
            <HarnessMark :harness="enrollment.harness" :size="14" />
            <span>{{ harnessLabel(enrollment.harness) }} · {{ enrollment.label }} · {{ enrollment.state }}</span>
            <span v-if="enrollment.verification_state">Verification {{ enrollment.verification_state.replace(/_/g, ' ') }}</span>
            <span v-if="enrollment.verification_error">{{ enrollment.verification_error }}</span>
            <span v-if="enrollment.local_processes">Local processes {{ enrollment.local_processes }}</span>
            <span v-if="enrollment.accounting_state === 'unconfirmed'">Accounting unconfirmed</span>
          </li>
        </ul>
        <button v-if="permissions.canSetOngoingLimits && current.enrollments.some(item => item.state === 'connected')" type="button" class="btn sm" @click="limitsOpen = !limitsOpen">{{ limitsOpen ? 'Hide ongoing limits' : 'Set ongoing limits' }}</button>
      </div>

      <div v-if="showLimitForm && limitAccounts.length" class="limits">
        <h3>Ongoing limits</h3>
        <p class="sub">A number of requests for a period. This is an Aeon allowance, not the vendor subscription. The paired computer estimates one request at a time.</p>
        <div v-for="account in limitAccounts" :key="account.key" class="limit">
          <h4>{{ account.label }}</h4>
          <p v-if="account.id && limitState[account.id] === 'saved'">Requests for this period are saved.</p>
          <template v-else>
            <label>Requests
              <input class="field" type="number" min="1" step="1" :value="draftFor(account.key).allowance ?? ''" @input="setDraft(account.key, { allowance: ($event.target as HTMLInputElement).value === '' ? null : Number(($event.target as HTMLInputElement).value) })" />
            </label>
            <label>Starts (your local time)
              <input class="field" type="datetime-local" :value="draftFor(account.key).starts_at" @input="setDraft(account.key, { starts_at: ($event.target as HTMLInputElement).value })" />
            </label>
            <label>Ends (your local time)
              <input class="field" type="datetime-local" :value="draftFor(account.key).ends_at" @input="setDraft(account.key, { ends_at: ($event.target as HTMLInputElement).value })" />
            </label>
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
        <button class="btn primary go" type="button" :disabled="!!busy || !permissions.canApprove || !selected.length" @click="connect">{{ busy === 'approve' ? (addRequest ? 'Adding…' : 'Connecting…') : addRequest ? 'Add harness' : 'Connect computer' }}</button>
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
.steps { display: flex; align-items: center; gap: 0; margin: 22px 0; padding: 0; list-style: none; }
.steps li { display: flex; align-items: center; gap: 8px; color: var(--ink-3); font-size: 13px; font-weight: 600; }
.steps li:not(:last-child) { flex: 1; }
.steps li:not(:last-child)::after { content: ''; flex: 1; height: 1px; margin: 0 10px; background: var(--line-2); }
.steps .current { color: var(--ink); }
.num { display: grid; place-items: center; width: 22px; height: 22px; border-radius: 50%; background: var(--surface-sunken); color: var(--ink-2); font: 600 12px/1 var(--mono); }
.current .num, .done .num { background: var(--teal); color: #fff; }
.card { padding: 18px; border: 1px solid var(--line); border-radius: 16px; background: var(--surface-raised); }
.card h2, .card h3, .card h4 { margin: 16px 0 6px; }
.card h2 { font: 600 16px/1.3 var(--font); letter-spacing: 0; }
.card h3, .card h4 { font: 600 14px/1.3 var(--font); }
.card > section:first-child h2, .review-head h2 { margin-top: 0; }
.copy { overflow-wrap: anywhere; white-space: pre-wrap; }
.banner, .problem { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; margin: 0 0 12px; }
.banner { padding: 10px 12px; border-radius: 12px; background: var(--surface-sunken); color: var(--ink-2); }
.problem { color: var(--danger); }
form label, .limit label { display: grid; gap: 6px; margin: 10px 0; }
.facts { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 10px; margin-top: 14px; }
.facts > div { display: flex; gap: 10px; min-width: 0; padding: 12px; border-radius: 12px; background: var(--surface-sunken); }
.glyph { display: grid; place-items: center; width: 28px; height: 28px; color: var(--ink-2); }
.k { color: var(--ink-3); font-size: 11px; letter-spacing: .08em; text-transform: uppercase; }
.path, .copy { overflow-wrap: anywhere; }
.caps { margin-top: 8px; }
.harness, .verify, .radio { display: flex; align-items: center; gap: 10px; min-height: 52px; margin-top: 8px; padding: 10px 12px; border-radius: 12px; background: var(--surface-sunken); }
.harness { justify-content: space-between; }
.check, .verify, .radio { cursor: pointer; }
.check { display: flex; align-items: center; gap: 10px; min-width: 0; }
.check input, .verify input, .radio input { width: 16px; height: 16px; margin: 0; accent-color: var(--teal); }
.account-pick { flex: 0 1 220px; }
.account-pick .field { height: 36px; }
.verify { align-items: flex-start; }
.verify strong, .radio strong { display: block; color: var(--ink); }
.expiry { display: inline-flex; align-items: center; gap: 6px; margin-left: auto; color: var(--ink-2); font-size: 13px; white-space: nowrap; }
.exact { margin-top: 8px; }
fieldset { margin: 16px 0 0; padding: 0; border: 0; }
legend { margin-bottom: 4px; color: var(--ink); font-weight: 650; }
.radio { align-items: flex-start; }
.actions { display: flex; flex-wrap: wrap; align-items: center; gap: 10px; margin-top: 16px; }
.go { min-height: 44px; padding: 0 18px; }
.keep { display: inline-flex; align-items: center; gap: 6px; margin: 0; color: var(--ink-3); font-size: 13px; }
.review-head { display: flex; align-items: flex-start; justify-content: space-between; gap: 12px; }
.enrollments { display: grid; gap: 8px; margin: 10px 0; padding: 0; list-style: none; }
.enrollments li, .row-actions { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; }
.limit { margin-top: 8px; padding-top: 8px; border-top: 1px solid var(--line); }
.sr { position: absolute; width: 1px; height: 1px; overflow: hidden; clip-path: inset(50%); }
@media (max-width: 720px) {
  .connect { padding: 18px 12px 32px; }
  .facts, .steps { grid-template-columns: 1fr; }
  .steps { flex-direction: column; align-items: stretch; gap: 8px; }
  .steps li:not(:last-child) { flex: none; }
  .steps li:not(:last-child)::after { display: none; }
  .harness, .verify { flex-wrap: wrap; }
  .account-pick, .expiry { flex-basis: 100%; margin-left: 0; }
  .review-head { flex-direction: column; }
}
</style>
