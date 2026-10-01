<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import AppIcon from '../components/AppIcon.vue'
import HarnessMark from '../components/agents/HarnessMark.vue'
import LinkedAccounts from '../components/agents/LinkedAccounts.vue'
import { approveAccountLink, lookupAccountLink, validAccountLinkCode, type AccountLinkReview } from '../lib/accountLink'
import { harnessLabel } from '../lib/agentState'
import { can } from '../lib/authz'
import { useSession } from '../stores/session'

const session = useSession()
const allowed = computed(() => session.identity?.principal.kind === 'person' && can('profile.write') && !session.requiresSignIn)
const scope = computed(() => `${session.identity?.tenant.id ?? ''}:${session.identity?.principal.id ?? ''}:${allowed.value}`)
const code = ref('')
const review = ref<AccountLinkReview | null>(null)
const error = ref('')
const busy = ref(false)
const linked = ref(false)
const linksRevision = ref(0)
const now = ref(Date.now())
const confirmButton = ref<HTMLButtonElement | null>(null)
const expired = computed(() => !!review.value && !linked.value && Date.parse(review.value.expires_at) <= now.value)
let turn = 0
let timer: ReturnType<typeof setTimeout> | undefined
let expiryTimer: ReturnType<typeof setInterval> | undefined
let controller: AbortController | undefined

function clear() {
  turn++; controller?.abort(); clearTimeout(timer); clearInterval(expiryTimer)
  review.value = null; error.value = ''; busy.value = false; linked.value = false
}
watch(scope, () => { clear(); code.value = '' })
watch(code, value => {
  clear()
  if (allowed.value && validAccountLinkCode(value)) timer = setTimeout(() => void lookup(), 300)
})
async function lookup() {
  if (busy.value || !allowed.value || !validAccountLinkCode(code.value)) return
  clearTimeout(timer)
  const started = ++turn
  const owner = scope.value
  controller?.abort(); controller = new AbortController()
  busy.value = true; error.value = ''
  try {
    const result = await lookupAccountLink(code.value, controller.signal)
    if (started !== turn || owner !== scope.value) return
    if (result.tenant_id !== session.identity?.tenant.id || result.person_id !== session.identity?.principal.id || result.state !== 'pending') throw new Error('This review belongs to another session. Enter the code again.')
    review.value = result; now.value = Date.now()
    expiryTimer = setInterval(() => { now.value = Date.now() }, 1000)
  } catch (cause) { if (started === turn && owner === scope.value) error.value = cause instanceof Error ? cause.message : 'Account linking is unavailable.' }
  finally {
    if (started === turn) {
      busy.value = false
      await nextTick()
      if (started === turn && owner === scope.value && review.value && !expired.value) confirmButton.value?.focus()
    }
  }
}
async function confirm() {
  const current = review.value
  if (busy.value || !allowed.value || !current || expired.value || linked.value) return
  const started = ++turn
  const owner = scope.value
  busy.value = true; error.value = ''
  controller?.abort(); controller = new AbortController()
  try {
    const result = await approveAccountLink(current, code.value, controller.signal)
    if (started !== turn || owner !== scope.value) return
    if (result.person_id !== current.person_id || result.tenant_id !== current.tenant_id || result.account_id !== current.account_id || result.state !== 'linked') throw new Error('Account confirmation could not be verified. Check your linked accounts.')
    review.value = result; linked.value = true; linksRevision.value++
    clearInterval(expiryTimer)
  } catch (cause) {
    if (started === turn && owner === scope.value) { review.value = null; error.value = cause instanceof Error ? cause.message : 'Check your linked accounts before trying again.' }
  } finally { if (started === turn) busy.value = false }
}
onBeforeUnmount(clear)
</script>

<template>
  <div class="link-page">
    <div class="link-heading"><AppIcon name="link" :size="23" /><h1>Link an account</h1></div>
    <p v-if="!allowed" class="hint" role="status">Only a signed-in person can link their own account.</p>
    <section v-else-if="linked && review" class="link-card success" role="status">
      <AppIcon name="check" :size="25" />
      <h2>Linked to {{ review.person_name }}</h2>
      <p>{{ harnessLabel(review.harness) }} · {{ review.account_label }}</p>
      <a class="btn" href="#linked-accounts">Your linked accounts</a>
    </section>
    <section v-else class="link-card">
      <template v-if="allowed">
        <p class="hint">Enter the code from your agent window.</p>
        <form @submit.prevent="lookup">
          <label for="account-link-code">Account code</label>
          <div class="code-field">
            <input id="account-link-code" v-model="code" class="field" inputmode="numeric" autocomplete="off" maxlength="16" placeholder="482 913" :disabled="busy && !!review" :aria-invalid="!!error" :aria-describedby="error ? 'link-error' : undefined" />
            <span v-if="busy && !review" class="hint" role="status">Checking…</span>
          </div>
        </form>
        <div v-if="review" class="review">
          <HarnessMark :harness="review.harness" :size="24" />
          <h2>Link {{ harnessLabel(review.harness) }} ({{ review.account_label }}) to {{ review.person_name }}?</h2>
          <p>{{ review.computer_name }} · {{ review.tenant_name }}</p>
          <p v-if="expired" class="hint" role="status">This code expired. Request a fresh code in the agent window.</p>
          <button ref="confirmButton" type="button" class="btn primary" :disabled="busy || expired" @click="confirm"><AppIcon name="link" :size="15" />{{ busy ? 'Linking…' : 'Link account' }}</button>
        </div>
        <p v-if="error" id="link-error" class="error" role="alert">{{ error }}</p>
      </template>
    </section>
    <LinkedAccounts v-if="allowed" :revision="linksRevision" />
  </div>
</template>

<style scoped>
.link-page { width: min(100%, 540px); margin: 40px auto; padding: 0 20px; }
.link-heading { display: flex; align-items: center; gap: 12px; margin-bottom: 22px; }
h1 { margin: 0; font-size: 25px; letter-spacing: -.025em; }
.link-card { padding: 26px; border: 1px solid var(--line); border-radius: var(--radius-lg, 14px); background: var(--surface); display: grid; gap: 16px; }
.hint { margin: 0; color: var(--ink-2); line-height: 1.5; }
form { display: grid; gap: 7px; }
label { color: var(--ink-2); font-size: 12px; font-weight: 600; }
.code-field { display: flex; align-items: center; gap: 12px; }
.code-field .field { width: 165px; font-family: var(--mono, ui-monospace, monospace); font-size: 22px; letter-spacing: .12em; padding: 9px 12px; }
.review { display: grid; gap: 12px; padding-top: 18px; border-top: 1px solid var(--line); }
h2 { margin: 0; font-size: 18px; line-height: 1.4; overflow-wrap: anywhere; }
.review p, .success p { margin: 0; color: var(--ink-2); font-size: 13px; line-height: 1.5; }
.btn { width: fit-content; }
.error { margin: 0; color: var(--danger); font-size: 13px; line-height: 1.5; }
.success { justify-items: start; }
@media (max-width: 600px) { .link-page { margin: 22px auto; padding: 0 14px; } .link-card { padding: 20px; } }
</style>
