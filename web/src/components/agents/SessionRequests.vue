<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, ref, useId } from 'vue'
import { APIError } from '../../lib/api'
import { can } from '../../lib/authz'
import { useAgents } from '../../stores/agents'
import { useSession } from '../../stores/session'
import { fetchAccountCatalog, effortLabel, type AgentAccountCatalog } from '../../lib/accountCascade'
import type { HarnessSession, SessionChangeRequest } from '../../lib/agents'
import { readSessionRequests, sendSessionRequest } from '../../lib/agentRows'

// Requests to a session that runs on its own (AEON-225). AEON-1438 moved them out
// of the composer: the panel's ⋯ menu opens the form (mode "form"); Details lists
// the recent requests and their outcome (mode "list").
const props = defineProps<{ session: HarnessSession; now: number; mode: 'form' | 'list'; kind?: 'rename_request' | 'model_request'; name?: string }>()
const emit = defineEmits<{ sent: [control: SessionChangeRequest]; cancel: [] }>()
const identity = useSession()
const agents = useAgents()
const uid = useId()
const allowed = computed(() => identity.identity?.principal.kind === 'person' && can('harness.control', props.session.project_id))
const ended = computed(() => !!props.session.stopped_at || !!props.session.archived_at || props.session.phase === 'stopped')
const kind = computed(() => props.kind ?? 'rename_request')
const label = ref(props.name ?? ''), account = ref(''), model = ref(''), profile = ref('')
const busy = ref(false), error = ref(''), statusError = ref(''), catalogError = ref(''), catalogLoading = ref(false)
const catalog = ref<AgentAccountCatalog | null>(null)
const controls = ref<SessionChangeRequest[]>([])
const accounts = computed(() => (catalog.value?.hosts ?? []).flatMap(host => host.harnesses.filter(h => h.harness === props.session.harness).flatMap(h => h.accounts.map(a => ({ ...a, host: host.label })))))
const models = computed(() => accounts.value.find(a => a.id === account.value)?.models ?? [])
const efforts = computed(() => models.value.find(m => m.model === model.value)?.efforts ?? [])
const ready = computed(() => allowed.value && !ended.value && !busy.value && (kind.value === 'rename_request' ? !!label.value.trim() && label.value.trim() !== props.name : efforts.value.some(e => e.model_profile_id === profile.value)))
const recent = computed(() => {
  const sorted = [...controls.value].sort((a, b) => b.sequence - a.sequence)
  return [...sorted.filter(c => c.state !== 'completed'), ...sorted.filter(c => c.state === 'completed').slice(0, 3)]
})
let revision = 0
let disposed = false, timer: ReturnType<typeof setTimeout> | undefined
let retry: { signature: string; id: string } | undefined
const abort = new AbortController()

// The list follows the session's requests while Details is on screen.
async function refresh() {
  const observedRevision = revision
  try {
    const requests = await readSessionRequests(props.session.project_id, props.session.id, abort.signal)
    if (!disposed && observedRevision === revision) { controls.value = requests.filter(c => c.kind === 'rename_request' || c.kind === 'model_request'); statusError.value = '' }
  } catch { if (!disposed) statusError.value = 'Request status could not be refreshed.' }
  finally { if (!disposed) timer = setTimeout(refresh, 5000) }
}
if (props.mode === 'list') void refresh()
onBeforeUnmount(() => { disposed = true; abort.abort(); clearTimeout(timer) })
async function loadCatalog() {
  catalogLoading.value = true; catalogError.value = ''
  const result = await fetchAccountCatalog('build')
  if (disposed) return
  catalog.value = result.catalog; catalogError.value = result.message; catalogLoading.value = false
  const reported = accounts.value.filter(a => a.label === props.session.account_label)
  if (reported.length === 1) account.value = reported[0]!.id
  else if (accounts.value.length === 1) account.value = accounts.value[0]!.id
}
if (props.mode === 'form' && kind.value === 'model_request') void loadCatalog()
function pickAccount() { model.value = ''; profile.value = '' }
function pickModel() { profile.value = '' }
function state(c: SessionChangeRequest) {
  if (c.state === 'completed') return c.reason === 'request_expired' ? 'Expired' : c.outcome === 'applied' ? 'Applied' : 'Rejected'
  if (Date.parse(c.expires_at) <= props.now) return 'Expired'
  return 'Requested · waiting for the session'
}
function title(c: SessionChangeRequest) {
  return c.kind === 'rename_request' ? `Rename to ${c.request_payload.display_label}` : [c.request_payload.model, c.request_payload.reasoning_effort].filter(Boolean).join(' · ')
}
async function submit() {
  if (!ready.value) return
  const change = kind.value === 'rename_request' ? { display_label: label.value.trim() } : { account_id: account.value, model_profile_id: profile.value }
  const body = { kind: kind.value, expected_generation: props.session.id, ...change }
  const signature = JSON.stringify(body)
  if (retry?.signature !== signature) retry = { signature, id: crypto.randomUUID() }
  busy.value = true; error.value = ''
  try {
    const response = await sendSessionRequest(props.session.project_id, props.session.id, { ...body, request_id: retry.id }, abort.signal)
    const data = await response.json()
    if (!response.ok) throw new APIError(response.status, data.error || `Request failed (${response.status})`)
    void agents.afterWrite()
    if (!disposed) { retry = undefined; emit('sent', data as SessionChangeRequest) }
  } catch (e) { if (!disposed) error.value = e instanceof Error ? e.message : 'The request could not be sent.' }
  finally { if (!disposed) busy.value = false }
}
// A request sent from the ⋯ menu shows here at once, before the next read.
function add(control: SessionChangeRequest) {
  revision++
  controls.value = [...controls.value.filter(c => c.id !== control.id), control]
}
defineExpose({ add })
</script>

<template>
  <form v-if="mode === 'form'" class="request-form" :aria-labelledby="`${uid}-title`" @submit.prevent="submit">
    <h3 :id="`${uid}-title`">{{ kind === 'rename_request' ? 'Rename session' : 'Change model or effort' }}</h3>
    <template v-if="kind === 'rename_request'">
      <label :for="`${uid}-label`" class="sr-only">New session name</label>
      <input :id="`${uid}-label`" v-model="label" class="field" maxlength="128" autocomplete="off" data-autofocus :disabled="busy" />
      <p class="hint">{{ name || 'The session' }} gets this as a request and renames itself at its next step.</p>
    </template>
    <template v-else>
      <label :for="`${uid}-account`">Account catalog</label>
      <select :id="`${uid}-account`" v-model="account" class="field" data-autofocus :disabled="busy || catalogLoading" @change="pickAccount"><option value="">Choose an account</option><option v-for="a in accounts" :key="a.id" :value="a.id">{{ a.label }} · {{ a.host }}</option></select>
      <label :for="`${uid}-model`">Model</label>
      <select :id="`${uid}-model`" v-model="model" class="field" :disabled="busy || !account" @change="pickModel"><option value="">Choose a model</option><option v-for="m in models" :key="m.model" :value="m.model">{{ m.model }}</option></select>
      <label :for="`${uid}-effort`">Effort</label>
      <select :id="`${uid}-effort`" v-model="profile" class="field" :disabled="busy || !model"><option value="">Choose effort</option><option v-for="e in efforts" :key="e.model_profile_id" :value="e.model_profile_id">{{ effortLabel(e.effort) }}</option></select>
    </template>
    <div class="request-actions">
      <button type="button" class="btn sm" :disabled="busy" @click="emit('cancel')">Cancel</button>
      <button type="submit" class="btn sm primary request-send" :disabled="!ready"><span class="btn-label"><span>{{ busy ? 'Requesting…' : 'Send request' }}</span><span aria-hidden="true">Requesting…</span></span></button>
    </div>
    <!-- What varies sits below the actions, so they never move. -->
    <template v-if="kind === 'model_request'">
      <p v-if="catalogLoading" class="hint" role="status">Loading models…</p>
      <p v-else-if="catalogError" class="error" role="alert">{{ catalogError }} <button type="button" class="btn sm ghost" @click="loadCatalog">Retry catalog</button></p>
      <p v-else-if="!accounts.length || (account && !models.length)" class="hint">No model choices in this account catalog.</p>
      <p v-else class="hint">The session decides what it can apply; its account stays the same.</p>
    </template>
    <p v-if="error" class="error" role="alert">{{ error }}</p>
  </form>
  <section v-else-if="recent.length || statusError" class="session-requests" :aria-labelledby="`${uid}-requests`">
    <h3 :id="`${uid}-requests`" class="eyebrow">Session requests</h3>
    <ol v-if="recent.length" aria-label="Recent session requests" aria-live="polite">
      <li v-for="c in recent" :key="c.id"><span class="request-title" :title="title(c)">{{ title(c) }}</span><span class="request-state">{{ state(c) }}</span><span v-if="c.outcome === 'rejected' && c.reason && c.reason !== 'request_expired'" class="hint">{{ c.reason.replaceAll('_', ' ') }}</span></li>
    </ol>
    <p v-if="statusError" class="hint" role="status">{{ statusError }}</p>
  </section>
</template>

<style scoped>
.request-form { display: grid; gap: 10px; padding: 8px 8px 6px; font-size: 13px; color: var(--ink-2); }
.request-form h3 { margin: 0; font-size: 13.5px; font-weight: 650; line-height: 1.3; color: var(--ink); }
.request-form label { color: var(--ink-3); font-size: 12px; }
.request-form label + .field, .request-form label + select { margin-top: -6px; }
.field { width: 100%; min-width: 0; height: 36px; font: inherit; font-size: 13.5px; }
.request-actions { display: flex; flex-wrap: wrap; justify-content: flex-end; gap: 8px; }
.hint { margin: 0; color: var(--ink-3); font-size: 12px; line-height: 1.45; overflow-wrap: anywhere; }
.error { margin: 0; color: var(--danger); overflow-wrap: anywhere; }
.session-requests { margin-top: 24px; font-size: 12.5px; color: var(--ink-2); }
.session-requests h3 { margin: 0 0 10px; }
ol { list-style: none; padding: 0; margin: 0; display: grid; gap: 0; }
li { display: grid; gap: 2px; min-width: 0; padding: 8px 0; border-top: 1px solid var(--line); }
li:first-child { border-top: 0; padding-top: 0; }
.request-title { overflow-wrap: anywhere; color: var(--ink); }
.request-state { font-size: 12px; }
@media (pointer: coarse) { .field { height: 44px; font-size: 16px; } .request-actions .btn { height: 44px; } }
</style>
