<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { resilientFetch } from '../lib/api'
import { myPermissions, refreshPermissions } from '../lib/authz'
import { rememberSignInReturn } from '../lib/signInReturn'
import { useSession } from '../stores/session'
import {
  DEFAULT_REVIEW_CHOICE, PUBLIC_PAIRING_GUIDE_PATH, PairingError,
  createOngoingLimits, defaultSelectedAccountKeys, denyPairing, describeProgress,
  formatVerification, getPairingComputer, getPairingGuide, lookupPairing, ongoingLimitError,
  pairingPermissions, peekPairingCode, planOngoingLimits, planPoll, publicGuideSections,
  rememberPairingCode, submitApproval, takePairingCode, verificationWarning,
  type OngoingLimitDraft, type PairingGuide, type PairingView, type ReviewChoice,
} from '../lib/agentPairing'

// Plain public guide. The designed review layout waits for an approved direction.
// This page keeps the route anonymous, shows the server's own terms, and sends
// only the human code through sign-in.
const session = useSession()
const router = useRouter()
const guide = ref<PairingGuide | null>(null)
const guideLoaded = ref(false)
const guideError = ref('')
const code = ref('')
const current = ref<PairingView | null>(null)
const choice = ref<ReviewChoice>(DEFAULT_REVIEW_CHOICE)
const selected = ref<string[]>([])
const drafts = ref<Record<string, OngoingLimitDraft>>({})
const busy = ref('')
const message = ref('')
const nextStep = ref('')
const sections = computed(() => guideLoaded.value ? publicGuideSections(guide.value) : [])
const progress = computed(() => current.value ? describeProgress(current.value) : null)
const permissions = computed(() => pairingPermissions({
  permissions: [...myPermissions()],
  principalKind: session.identity?.principal.kind,
}))
const accountGroups = computed(() => {
  const groups = new Map<string, PairingView['requested_accounts']>()
  for (const account of current.value?.requested_accounts ?? []) {
    const list = groups.get(account.harness) ?? []
    list.push(account)
    groups.set(account.harness, list)
  }
  return [...groups.entries()].map(([harness, accounts]) => ({ harness, accounts }))
})
const terms = computed(() => choice.value === 'one_per_harness' ? current.value?.verification ?? null : null)
let pollTimer = 0
let pollStarted = 0

onMounted(async () => {
  try { guide.value = await getPairingGuide() }
  catch (error) { guideError.value = textOf(error) }
  finally { guideLoaded.value = true }
  await prepareSession()
  const stored = peekPairingCode()
  if (!stored) return
  code.value = stored
  if (!session.identity) return
  takePairingCode()
  await lookup()
})
onBeforeUnmount(() => { if (pollTimer) window.clearTimeout(pollTimer) })

async function prepareSession() {
  // A public read of /me must not mark an anonymous visit as an ended session.
  const response = await resilientFetch('/api/me', {
    credentials: 'same-origin', cache: 'no-store', headers: { Accept: 'application/json' },
  })
  if (!response.ok) return
  await session.refresh()
  await refreshPermissions()
}

async function lookup() {
  message.value = ''
  nextStep.value = ''
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
    choice.value = DEFAULT_REVIEW_CHOICE
    armPoll(current.value)
  } catch (error) {
    current.value = null
    assignError(error, 'The code could not be looked up.')
  } finally { busy.value = '' }
}

async function connect() {
  if (!current.value || busy.value) return
  busy.value = 'approve'
  message.value = ''
  nextStep.value = ''
  try {
    const approved = await submitApproval({
      view: current.value, choice: choice.value, selectedAccountKeys: selected.value,
      permissions: permissions.value, drafts: Object.values(drafts.value),
    })
    current.value = approved
    if (choice.value === 'ongoing_limits') {
      const plan = planOngoingLimits({
        choice: choice.value, drafts: Object.values(drafts.value), selectedAccountKeys: selected.value,
        enrollments: approved.enrollments, permissions: permissions.value,
      })
      if (plan.action === 'blocked') { message.value = plan.message; nextStep.value = plan.next }
      else if (plan.action === 'send') await createOngoingLimits(plan.windows, permissions.value)
    }
    armPoll(approved)
  } catch (error) { assignError(error, 'The computer was not connected.') }
  finally { busy.value = '' }
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

function selectAccount(key: string) {
  const account = current.value?.requested_accounts.find(item => item.account_key === key)
  if (!account) return
  selected.value = [
    ...selected.value.filter(item => current.value?.requested_accounts.find(row => row.account_key === item)?.harness !== account.harness),
    key,
  ]
}

function draftFor(key: string): OngoingLimitDraft {
  return drafts.value[key] ?? { account_key: key, starts_at: '', ends_at: '', unit: 'requests', allowance: null, pace_model: 'unrestricted', burst_ratio: 0 }
}

function setDraft(key: string, patch: Partial<OngoingLimitDraft>) {
  drafts.value = { ...drafts.value, [key]: { ...draftFor(key), ...patch, account_key: key } }
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
}

function textOf(error: unknown) {
  return error instanceof PairingError ? `${error.message} ${error.next}`.trim() : 'The public guide could not be loaded.'
}
</script>

<template>
  <article class="guide">
    <p class="eyebrow">Agents</p>
    <section v-for="(section, index) in sections" :key="section.heading">
      <h1 v-if="index === 0">{{ section.heading }}</h1>
      <h2 v-else>{{ section.heading }}</h2>
      <p v-for="paragraph in section.paragraphs" :key="paragraph">{{ paragraph }}</p>
    </section>
    <p v-if="guideError" class="problem" role="alert">{{ guideError }}</p>

    <form @submit.prevent="lookup">
      <h2>Code from the computer</h2>
      <label>Pairing code
        <input v-model="code" class="field" name="user-code" autocomplete="one-time-code" inputmode="numeric" spellcheck="false" />
      </label>
      <button class="btn primary" type="submit" :disabled="!!busy">{{ session.identity ? 'Look up code' : 'Sign in to review the code' }}</button>
    </form>

    <section v-if="current && progress">
      <h2>{{ progress.title }}</h2>
      <p>{{ progress.detail }}</p>
      <p>{{ progress.next }}</p>
      <dl>
        <div><dt>Workspace</dt><dd>{{ current.tenant_name }}</dd></div>
        <div><dt>Computer</dt><dd>{{ current.computer_name }} · {{ current.platform }}/{{ current.arch }}</dd></div>
        <div><dt>Folder</dt><dd>{{ current.workspace_path }}</dd></div>
        <div><dt>Capabilities</dt><dd>{{ current.capabilities.join(', ') || 'None published' }}</dd></div>
      </dl>

      <fieldset v-for="group in accountGroups" :key="group.harness">
        <legend>{{ group.harness }}</legend>
        <label v-for="account in group.accounts" :key="account.account_key">
          <input type="radio" :name="`harness-${group.harness}`" :checked="selected.includes(account.account_key)" @change="selectAccount(account.account_key)" />
          {{ account.label }}
        </label>
      </fieldset>

      <fieldset v-if="current.state === 'pending'">
        <legend>When it connects</legend>
        <label><input v-model="choice" type="radio" value="one_per_harness" /> One short read-only verification per selected harness</label>
        <label><input v-model="choice" type="radio" value="connect_only" /> Connect only</label>
        <label><input v-model="choice" type="radio" value="ongoing_limits" /> Set ongoing limits</label>
      </fieldset>
      <p v-if="terms">{{ formatVerification(terms) }}</p>
      <p v-if="terms && verificationWarning(terms)" class="problem" role="alert">{{ verificationWarning(terms) }}</p>
      <p v-if="current.state === 'pending' && choice === 'one_per_harness' && !current.verification">The server did not include verification terms. Choose Connect only, or look the code up again.</p>

      <div v-if="current.state === 'pending' && choice === 'ongoing_limits'">
        <div v-for="key in selected" :key="key">
          <h3>{{ current.requested_accounts.find(account => account.account_key === key)?.label }}</h3>
          <p>Enter the allowance you intend. This is not the vendor subscription quota.</p>
          <label>Starts (your local time)
            <input class="field" type="datetime-local" :value="draftFor(key).starts_at" @input="setDraft(key, { starts_at: ($event.target as HTMLInputElement).value })" />
          </label>
          <label>Ends (your local time)
            <input class="field" type="datetime-local" :value="draftFor(key).ends_at" @input="setDraft(key, { ends_at: ($event.target as HTMLInputElement).value })" />
          </label>
          <label>Allowance
            <input class="field" type="number" min="1" step="1" :value="draftFor(key).allowance ?? ''" @input="setDraft(key, { allowance: ($event.target as HTMLInputElement).value === '' ? null : Number(($event.target as HTMLInputElement).value) })" />
          </label>
          <label>Unit
            <select class="field" :value="draftFor(key).unit" @change="setDraft(key, { unit: ($event.target as HTMLSelectElement).value as OngoingLimitDraft['unit'] })">
              <option value="requests">Requests</option>
              <option value="tokens">Tokens</option>
              <option value="cost_micros">Cost in micros</option>
            </select>
          </label>
          <label>Pace
            <select class="field" :value="draftFor(key).pace_model" @change="setDraft(key, { pace_model: ($event.target as HTMLSelectElement).value as OngoingLimitDraft['pace_model'] })">
              <option value="unrestricted">Unrestricted</option>
              <option value="steady">Steady</option>
              <option value="frontload">Frontload</option>
            </select>
          </label>
          <label>Burst ratio
            <input class="field" type="number" min="0" max="1" step="0.01" :value="draftFor(key).burst_ratio" @input="setDraft(key, { burst_ratio: Number(($event.target as HTMLInputElement).value) })" />
          </label>
          <p v-if="ongoingLimitError(draftFor(key))" class="problem">{{ ongoingLimitError(draftFor(key)) }}</p>
        </div>
      </div>

      <div v-if="current.state === 'pending'" class="actions">
        <button class="btn primary" type="button" :disabled="!!busy || !permissions.canApprove" @click="connect">Connect computer</button>
        <button class="btn" type="button" :disabled="!!busy || !permissions.canDeny" @click="deny">Deny</button>
      </div>
      <p v-if="session.identity && !permissions.canApprove">Only a signed-in person who can manage accounts can connect this computer.</p>
    </section>

    <p v-if="message" class="problem" role="alert">{{ message }}</p>
    <p v-if="nextStep">{{ nextStep }}</p>
  </article>
</template>

<style scoped>
.guide { max-width: 40rem; margin: 0 auto; padding: 2.5rem 1.25rem 4rem; }
.guide h1 { margin-bottom: .75rem; }
.guide h2, .guide h3 { margin: 1.75rem 0 .5rem; }
.guide p, .guide li { margin: .4rem 0 .8rem; }
.guide form, .guide fieldset, .guide dl { margin: 1.5rem 0; }
.guide fieldset { border: 0; padding: 0; }
.guide label { display: grid; gap: .35rem; margin: .7rem 0; }
.guide dl div { display: grid; gap: .15rem; margin: .7rem 0; }
.guide dt { color: var(--ink-3); font-size: 11px; letter-spacing: .08em; text-transform: uppercase; }
.guide dd { margin: 0; overflow-wrap: anywhere; }
.guide .problem { color: var(--danger); }
.actions { display: flex; flex-wrap: wrap; gap: .6rem; margin-top: 1rem; }
</style>
