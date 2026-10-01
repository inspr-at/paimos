<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, ref, useId, watch } from 'vue'
import { APIError } from '../../lib/api'
import { can } from '../../lib/authz'
import { useAgents } from '../../stores/agents'
import { useSession } from '../../stores/session'
import { fetchAccountCatalog, effortLabel, type AgentAccountCatalog } from '../../lib/accountCascade'
import type { HarnessSession, SessionChangeRequest } from '../../lib/agents'
import { readSessionRequests, sendSessionRequest } from '../../lib/agentRows'
import AppIcon from '../AppIcon.vue'

const props = defineProps<{ session: HarnessSession; now: number }>()
const identity = useSession()
const agents = useAgents()
const uid = useId()
const disclosure = ref<HTMLDetailsElement>()
const allowed = computed(() => identity.identity?.principal.kind === 'person' && can('harness.control', props.session.project_id))
const ended = computed(() => !!props.session.stopped_at || !!props.session.archived_at || props.session.phase === 'stopped')
const kind = ref<'rename_request' | 'model_request'>('rename_request')
const label = ref(''), account = ref(''), model = ref(''), profile = ref('')
const busy = ref(false), error = ref(''), statusError = ref(''), catalogError = ref(''), catalogLoading = ref(false)
const catalog = ref<AgentAccountCatalog | null>(null)
const controls = ref<SessionChangeRequest[]>([])
const accounts = computed(() => (catalog.value?.hosts ?? []).flatMap(host => host.harnesses.filter(h => h.harness === props.session.harness).flatMap(h => h.accounts.map(a => ({ ...a, host: host.label })))) )
const models = computed(() => accounts.value.find(a => a.id === account.value)?.models ?? [])
const efforts = computed(() => models.value.find(m => m.model === model.value)?.efforts ?? [])
const ready = computed(() => allowed.value && !ended.value && !busy.value && (kind.value === 'rename_request' ? !!label.value.trim() : efforts.value.some(e => e.model_profile_id === profile.value)))
const recent = computed(() => {
  const sorted = [...controls.value].sort((a, b) => b.sequence - a.sequence)
  return [...sorted.filter(c => c.state !== 'completed'), ...sorted.filter(c => c.state === 'completed').slice(0, 3)]
})
let revision = 0
let disposed = false, timer: ReturnType<typeof setTimeout> | undefined
let retry: { signature: string; id: string } | undefined
const abort = new AbortController()

async function refresh() {
  const observedRevision = revision
  try {
    const requests = await readSessionRequests(props.session.project_id, props.session.id, abort.signal)
    if (!disposed && observedRevision === revision) { controls.value = requests.filter(c => c.kind === 'rename_request' || c.kind === 'model_request'); statusError.value = '' }
  } catch { if (!disposed) statusError.value = 'Request status could not be refreshed.' }
  finally { if (!disposed) timer = setTimeout(refresh, 5000) }
}
void refresh()
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
watch(kind, value => { error.value = ''; if (value === 'model_request' && !catalog.value && !catalogLoading.value) void loadCatalog() })
watch(account, () => { model.value = ''; profile.value = '' })
watch(model, () => { profile.value = '' })
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
  busy.value = true; revision++; error.value = ''
  try {
    const response = await sendSessionRequest(props.session.project_id, props.session.id, { ...body, request_id: retry.id }, abort.signal)
    const data = await response.json()
    if (!response.ok) throw new APIError(response.status, data.error || `Request failed (${response.status})`)
    const control = data as SessionChangeRequest
    void agents.afterWrite()
    if (!disposed) {
      revision++
      controls.value = [...controls.value.filter(c => c.id !== control.id), control]
      retry = undefined; label.value = ''; profile.value = ''
      if (disclosure.value) {
        disclosure.value.open = false
        disclosure.value.querySelector('summary')?.focus()
      }
    }
  } catch (e) { if (!disposed) error.value = e instanceof Error ? e.message : 'The request could not be sent.' }
  finally { if (!disposed) busy.value = false }
}
</script>

<template>
  <section class="session-requests" aria-label="Session requests">
    <details v-if="allowed && !ended" ref="disclosure">
      <summary><AppIcon name="chevron-right" :size="12" />Ask this session to…</summary>
      <form class="request-form" @submit.prevent="submit">
        <label :for="`${uid}-kind`" class="sr-only">Change to request</label>
        <select :id="`${uid}-kind`" v-model="kind" class="field" :disabled="busy"><option value="rename_request">Rename</option><option value="model_request">Change model or effort</option></select>
        <template v-if="kind === 'rename_request'">
          <label :for="`${uid}-label`" class="sr-only">Requested session name</label>
          <input :id="`${uid}-label`" v-model="label" class="field" maxlength="128" placeholder="New session name" :disabled="busy" />
        </template>
        <template v-else>
          <label :for="`${uid}-account`">Account catalog</label>
          <select :id="`${uid}-account`" v-model="account" class="field" :disabled="busy || catalogLoading"><option value="">Choose an account</option><option v-for="a in accounts" :key="a.id" :value="a.id">{{ a.label }} · {{ a.host }}</option></select>
          <label :for="`${uid}-model`">Model</label>
          <select :id="`${uid}-model`" v-model="model" class="field" :disabled="busy || !account"><option value="">Choose a model</option><option v-for="m in models" :key="m.model" :value="m.model">{{ m.model }}</option></select>
          <label :for="`${uid}-effort`">Effort</label>
          <select :id="`${uid}-effort`" v-model="profile" class="field" :disabled="busy || !model"><option value="">Choose effort</option><option v-for="e in efforts" :key="e.model_profile_id" :value="e.model_profile_id">{{ effortLabel(e.effort) }}</option></select>
          <p class="hint">The session decides what it can apply; its account stays the same.</p>
          <p v-if="catalogLoading" role="status">Loading models…</p>
          <p v-else-if="catalogError" role="alert">{{ catalogError }} <button type="button" class="btn sm ghost" @click="loadCatalog">Retry catalog</button></p>
          <p v-else-if="!accounts.length || (account && !models.length)" class="hint">No model choices in this account catalog.</p>
        </template>
        <p v-if="error" class="error" role="alert">{{ error }}</p>
        <button type="submit" class="btn sm request-send" :disabled="!ready">{{ busy ? 'Requesting…' : 'Send request' }}</button>
      </form>
    </details>
    <ol v-if="recent.length" aria-label="Recent session requests" aria-live="polite">
      <li v-for="c in recent" :key="c.id"><span class="request-title" :title="title(c)">{{ title(c) }}</span><span class="request-state">{{ state(c) }}</span><span v-if="c.outcome === 'rejected' && c.reason && c.reason !== 'request_expired'" class="hint">{{ c.reason.replaceAll('_', ' ') }}</span></li>
    </ol>
    <p v-if="statusError" class="hint" role="status">{{ statusError }}</p>
  </section>
</template>

<style scoped>
.session-requests { max-height: 45vh; overflow: auto; overscroll-behavior: contain; font-size: 12.5px; color: var(--ink-2); margin: 0 0 10px; }
summary { display: flex; align-items: center; gap: 5px; cursor: pointer; padding: 5px 0; color: var(--ink-2); }
details[open] summary svg { transform: rotate(90deg); }
summary:focus-visible { outline: none; box-shadow: var(--focus-ring); border-radius: 4px; }
.request-form { display: grid; gap: 7px; padding: 8px 0; }
.field { width: 100%; min-width: 0; padding: 7px 9px; font: inherit; }
.request-form label { color: var(--ink-3); }
.request-send { justify-self: end; }
.hint { color: var(--ink-3); font-size: 12px; overflow-wrap: anywhere; }
.error { color: var(--danger); overflow-wrap: anywhere; }
ol { list-style: none; padding: 0; margin: 6px 0 0; display: grid; gap: 6px; }
li { display: grid; gap: 2px; padding: 7px 9px; border-radius: 7px; background: var(--code-bg); min-width: 0; }
.request-title { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.request-state { color: var(--ink); font-size: 12px; }
</style>
