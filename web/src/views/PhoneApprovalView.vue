<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { onAccessChange, can } from '../lib/authz'
import { useIdentityScope } from '../lib/useIdentityScope'
import { useSession } from '../stores/session'
import { brand } from '../lib/brand'
import { decidePhone, decidePhoneStepUp, stepUpResultLine, phoneError, phoneRequest, reviewPath, type PhoneKind, type PhoneReview } from '../lib/phoneApprovals'
import { scopeLabel } from '../lib/agentState'
import TargetSummary from '../components/deploy/TargetSummary.vue'
import AppIcon from '../components/AppIcon.vue'

const route = useRoute(), session = useSession()
const scope = useIdentityScope(() => session.identity?.principal.kind === 'person' && (can('profile.read') || can('settings.manage')))
const reads = scope.lane()
const review = ref<PhoneReview | null>(null), busy = ref(false), error = ref(''), outcome = ref(''), reason = ref('')
const now = ref(Date.now())
const tick = setInterval(() => { now.value = Date.now() }, 1000)
const expires = computed(() => review.value?.approval?.expires_at ?? review.value?.attach?.expires_at ?? review.value?.stepup?.expires_at ?? '')
const pending = computed(() => review.value?.pending && Date.parse(expires.value) > now.value)
const stepup = computed(() => route.params.kind === 'stepup')
const stepupAllowed = computed(() => !!review.value?.stepup && can(review.value.stepup.permission, review.value.stepup.project_id))
function load() {
  if (busy.value) return
  review.value = null; error.value = ''; outcome.value = ''; reason.value = ''
  const kind = String(route.params.kind), id = String(route.params.requestId)
  if (!['approval', 'attach', 'stepup'].includes(kind) || !/^[\da-f]{8}(-[\da-f]{4}){3}-[\da-f]{12}$/i.test(id)) { error.value = 'This approval link is unavailable.'; return }
  void reads.run(({ after, signal }) => after(phoneRequest<PhoneReview>(reviewPath(kind as PhoneKind, id), 'GET', undefined, signal), value => {
    if (value.kind !== kind || value.request_id !== id || (kind === 'stepup' && (!value.stepup || value.stepup.id !== id || value.stepup.request_digest !== value.request_hash))) throw new Error('The request could not be confirmed. Review again.')
    review.value = value
  }), {
    failed: e => { error.value = phoneError(e) },
  })
}
function decideStepUp(action: 'approve' | 'decline') {
  const current = review.value?.stepup
  if (!current || !pending.value || busy.value || !stepupAllowed.value) return
  const captured = { ...current }
  reads.cancel(); busy.value = true; error.value = ''
  void scope.run(({ after, signal }) => after(decidePhoneStepUp(captured, action, signal), result => {
    if (review.value?.stepup?.id !== captured.id || review.value.stepup.revision !== captured.revision) return
    if ('authorizeURL' in result) { window.location.assign(result.authorizeURL); return }
    review.value = { kind: 'stepup', request_id: result.request.id, request_hash: result.request.request_digest, pending: false, stepup: result.request }
    outcome.value = stepUpResultLine(result.request)
  }), { failed: e => { error.value = phoneError(e) }, settled: () => { busy.value = false } })
}
function decide(decision: 'approved' | 'denied') {
  if (!review.value || !pending.value || busy.value || !can('profile.write')) return
  const value = review.value
  const sent = { decision, request_hash: value.request_hash, reason: reason.value.trim() }
  reads.cancel(); busy.value = true; error.value = ''
  void scope.run(({ after, signal }) => after(decidePhone(value, sent, signal), result => {
    review.value = result
    outcome.value = decision === 'denied' ? 'Declined. Your decision was recorded.' : result.attach?.state === 'approved' && result.attach.consent_mode === 'local_auth'
      ? 'Approved here. Confirm with Touch ID on the paired Mac before the watch starts.' : 'Approved. Your decision was recorded.'
  }), { failed: e => { error.value = phoneError(e) }, settled: () => { busy.value = false } })
}
watch([() => route.fullPath, scope.owner], () => { scope.reset(); busy.value = false; load() }, { immediate: true, flush: 'sync' })
const stopAccess = onAccessChange(() => { scope.reset(); busy.value = false; load() })
onBeforeUnmount(() => { clearInterval(tick); stopAccess() })
</script>

<template>
  <main class="phone-review" :class="{ 'stepup-review': stepup }" aria-labelledby="phone-review-title">
    <nav class="phone-nav"><RouterLink to="/agents" class="back">Back to Agents</RouterLink><button v-if="stepup" class="review-again" type="button" :disabled="busy" @click="load">Review again</button></nav>
    <header><AppIcon name="shield" :size="24" /><h1 id="phone-review-title">{{ stepup ? 'Step-up approval' : 'Review approval' }}</h1></header>
    <template v-if="stepup">
      <div class="stepup-body" data-testid="stepup-phone-body">
        <p v-if="session.identity?.principal.kind !== 'person'" role="alert">Sign in as a person to review and decide.</p>
        <p v-else-if="!review && !error" role="status">Loading the request…</p>
        <article v-if="review?.stepup" class="context stepup-context" aria-label="Step-up request">
          <p class="waiting">{{ pending ? 'Holding work' : review.stepup.state === 'pending' ? 'Expired after 15 min · ask again' : stepUpResultLine(review.stepup) }} · Once</p>
          <h2>{{ scopeLabel(review.stepup.permission) }}</h2>
          <dl><dt>Requested by</dt><dd>{{ review.stepup.requested_by }}</dd><dt>Permission</dt><dd>{{ review.stepup.permission }}</dd></dl>
          <section aria-label="Before and after"><h3>Before</h3><pre>{{ JSON.stringify(review.stepup.before, null, 2) }}</pre><h3>After</h3><pre>{{ JSON.stringify(review.stepup.after, null, 2) }}</pre></section>
          <p>Expires <time :datetime="expires">{{ new Date(expires).toLocaleString() }}</time></p>
          <RouterLink :to="{ path: '/decision-desk', query: { needs: `s:${review.stepup.id}` } }">Open in Decision Desk</RouterLink>
          <p>Only a person holding this permission can decide. The first decision is final.</p>
        </article>
      </div>
      <footer class="stepup-footer" data-testid="stepup-phone-actions">
        <div class="stepup-feedback" aria-live="polite"><p v-if="error" class="error" role="alert">{{ error }}</p><p v-else role="status">{{ outcome || (busy ? 'Waiting for verification…' : !review ? 'Review the request before deciding.' : !pending ? 'This request has ended. Review its recorded outcome.' : !stepupAllowed ? 'The target permission is required.' : 'Approve uses the device passkey (Face ID where available), or a fresh sign-in. Decline needs no verification.') }}</p></div>
        <div class="actions"><button class="btn" type="button" :disabled="busy || !pending || !stepupAllowed" @click="decideStepUp('decline')">Decline</button><button class="btn primary" type="button" :disabled="busy || !pending || !stepupAllowed" @click="decideStepUp('approve')">Approve</button></div>
      </footer>
    </template>
    <template v-else>
    <p v-if="session.identity?.principal.kind !== 'person'" role="alert">Sign in as a person to review and decide.</p>
    <p v-else-if="!review && !error" role="status">Loading the request…</p>
    <p v-if="error" class="error" role="alert">{{ error }}</p>
    <button v-if="error && !busy" class="btn" @click="load">Review again</button>
    <article v-if="review" class="glass-card context">
      <template v-if="review.approval">
        <h2>{{ scopeLabel(review.approval.scope) }}</h2>
        <p class="risk">{{ review.approval.risk ?? 'low' }} risk</p>
        <dl><dt>Requested by</dt><dd>{{ review.approval.agent_name || review.approval.agent_principal_id }}</dd><dt>Permission</dt><dd>{{ review.approval.scope }}</dd><dt>Resource</dt><dd>{{ review.approval.resource_kind }}<template v-if="review.approval.resource_id"> · {{ review.approval.resource_id }}</template></dd></dl>
        <TargetSummary :approval="review.approval" />
        <p class="rationale">{{ review.approval.rationale }}</p>
        <RouterLink v-if="review.approval.run_id" :to="{ path: '/agents', query: { run: review.approval.run_id } }">Open agent context</RouterLink>
      </template>
      <template v-if="review.attach">
        <h2>{{ review.attach.snapshot.mode === 'lease' ? 'Link terminal session' : 'Watch terminal session' }}</h2>
        <dl><dt>Computer</dt><dd>{{ review.attach.snapshot.host }}</dd><dt>Agent</dt><dd>{{ review.attach.snapshot.harness }}</dd><dt>Workspace</dt><dd>{{ review.attach.snapshot.process.cwd }}</dd><dt>Process</dt><dd>{{ review.attach.snapshot.process.executable }} · PID {{ review.attach.snapshot.process.pid }}</dd><dt>Project</dt><dd>{{ review.attach.snapshot.project_id }}</dd><dt>Ticket</dt><dd>{{ review.attach.snapshot.ticket_id }}</dd></dl>
        <p>{{ review.attach.snapshot.mode === 'lease' ? 'Links this process to its ticket. It does not read the transcript or send input.' : `Lets ${brand.short_name} read this terminal transcript and, when permitted, send input to the session.` }}</p>
        <p v-if="review.attach.consent_mode === 'local_auth'">After approving here, confirm with Touch ID on the paired Mac. Phone verification does not replace that confirmation.</p>
        <RouterLink to="/agents">Open session context</RouterLink>
      </template>
      <p>Expires <time :datetime="expires">{{ new Date(expires).toLocaleString() }}</time></p>
    </article>
    <p v-if="outcome" role="status" class="outcome">{{ outcome }}</p>
    <p v-else-if="review && !pending" role="status">This request has ended or was already decided. No further decision is available.</p>
    <form v-if="pending && !outcome" @submit.prevent="decide('approved')">
      <label for="phone-reason">Reason (optional; shared with the agent)</label>
      <textarea id="phone-reason" v-model="reason" class="field" rows="3" maxlength="4000" :disabled="busy" />
      <p>A fresh device verification binds your choice to this exact request. If verification is cancelled, no decision is recorded.</p>
      <div class="actions"><button class="btn" type="button" :disabled="busy || !can('profile.write')" @click="decide('denied')">Decline with passkey</button><button class="btn primary" type="submit" :disabled="busy || !can('profile.write')">Approve with passkey</button></div>
      <RouterLink to="/settings/personal#phone-approvals">Set up a device passkey</RouterLink>
      <p v-if="busy" role="status">Waiting for device verification…</p>
    </form>
    </template>
  </main>
</template>

<style scoped>
.phone-review { width: min(100%, 620px); margin: 0 auto; padding: 24px 16px calc(32px + env(safe-area-inset-bottom)); display: grid; gap: 18px; }
header { display: flex; align-items: center; gap: 12px; }
h1 { font: 650 24px/1.3 var(--font); }
h2 { font: 600 18px/1.4 var(--font); }
p { font-size: 14px; line-height: 1.6; margin: 0; }
.context { padding: 20px; display: grid; gap: 14px; }
dl { display: grid; gap: 4px 12px; grid-template-columns: 110px minmax(0, 1fr); margin: 0; font-size: 13px; }
dt { color: var(--ink-2); } dd { margin: 0; overflow-wrap: anywhere; }
.rationale { white-space: pre-wrap; overflow-wrap: anywhere; }
.risk { color: var(--warn-ink); text-transform: capitalize; }
.error { color: var(--danger); }
.outcome { padding: 16px; border: 1px solid var(--line); border-radius: 12px; background: var(--surface); }
form { display: grid; gap: 10px; }
.actions { display: grid; grid-template-columns: 1fr 1fr; gap: 10px; }
.btn { min-height: 48px; white-space: normal; }
textarea { width: 100%; }
.stepup-review { height: calc(100dvh - 120px); min-height: 0; grid-template-rows: auto auto minmax(0, 1fr) auto; padding-bottom: env(safe-area-inset-bottom); }
.stepup-body { min-height: 0; overflow-y: auto; overscroll-behavior: contain; }
.stepup-context { padding: 16px 0; }
.stepup-context h3 { margin: 0 0 8px; font-size: 13px; color: var(--ink-2); }
.stepup-context pre { white-space: pre-wrap; overflow-wrap: anywhere; font: 12px/1.6 var(--mono); margin: 0 0 20px; }
.stepup-context section { border-block: 1px solid var(--line); padding-top: 16px; }
.stepup-footer { display: flex; flex-direction: column; gap: 10px; padding: 12px 0 calc(12px + env(safe-area-inset-bottom)); border-top: 1px solid var(--line); }
.stepup-feedback { height: 5.6em; overflow-y: auto; font-size: 14px; }
.stepup-footer .actions { grid-template-columns: 1fr 1fr; }
.phone-nav { display: flex; align-items: center; justify-content: space-between; gap: 12px; }
.review-again { border: 0; background: transparent; color: var(--ink-2); min-height: 44px; }
@media (max-width: 420px) { .actions { grid-template-columns: 1fr; } dl { grid-template-columns: 1fr; } dd { margin-bottom: 8px; } }
</style>
