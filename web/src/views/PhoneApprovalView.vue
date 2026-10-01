<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { onAccessChange, can } from '../lib/authz'
import { useIdentityScope } from '../lib/useIdentityScope'
import { useSession } from '../stores/session'
import { decidePhone, phoneError, phoneRequest, reviewPath, type PhoneKind, type PhoneReview } from '../lib/phoneApprovals'
import { scopeLabel } from '../lib/agentState'
import TargetSummary from '../components/deploy/TargetSummary.vue'
import AppIcon from '../components/AppIcon.vue'

const route = useRoute(), session = useSession()
const scope = useIdentityScope(() => session.identity?.principal.kind === 'person' && can('profile.read'))
const reads = scope.lane()
const review = ref<PhoneReview | null>(null), busy = ref(false), error = ref(''), outcome = ref(''), reason = ref('')
const now = ref(Date.now())
const tick = setInterval(() => { now.value = Date.now() }, 1000)
const expires = computed(() => review.value?.approval?.expires_at ?? review.value?.attach?.expires_at ?? '')
const pending = computed(() => review.value?.pending && Date.parse(expires.value) > now.value)
function load() {
  if (busy.value) return
  review.value = null; error.value = ''; outcome.value = ''; reason.value = ''
  const kind = String(route.params.kind), id = String(route.params.requestId)
  if (!['approval', 'attach'].includes(kind) || !/^[\da-f]{8}(-[\da-f]{4}){3}-[\da-f]{12}$/i.test(id)) { error.value = 'This approval link is unavailable.'; return }
  void reads.run(({ after, signal }) => after(phoneRequest<PhoneReview>(reviewPath(kind as PhoneKind, id), 'GET', undefined, signal), value => { review.value = value }), {
    failed: e => { error.value = phoneError(e) },
  })
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
  <main class="phone-review" aria-labelledby="phone-review-title">
    <RouterLink to="/agents" class="back">Back to Agents</RouterLink>
    <header><AppIcon name="shield" :size="24" /><h1 id="phone-review-title">Review approval</h1></header>
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
        <p>{{ review.attach.snapshot.mode === 'lease' ? 'Links this process to its ticket. It does not read the transcript or send input.' : 'Lets Aeon read this terminal transcript and, when permitted, send input to the session.' }}</p>
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
@media (max-width: 420px) { .actions { grid-template-columns: 1fr; } dl { grid-template-columns: 1fr; } dd { margin-bottom: 8px; } }
</style>
