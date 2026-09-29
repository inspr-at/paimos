<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, ref, useId, watch } from 'vue'
import { api, APIError } from '../../lib/api'
import { can } from '../../lib/authz'
import type { HarnessSession } from '../../lib/agents'
import { useSession } from '../../stores/session'
import AppIcon from '../AppIcon.vue'

interface Ownership { daemon_id: string; generation: string; process_id: string; root_pid: number; group_id: number; started_at: string }
type ManagedSession = HarnessSession & { process_ownership?: Ownership; process_observed_at?: string }
type Setting = 'rename' | 'model' | 'effort'
type Kind = 'steer' | 'interrupt' | 'stop' | Setting
interface SettingModel { model: string; efforts: string[] }
interface Control { id: string; session_id: string; kind: Kind; state: 'pending' | 'claimed' | 'completed'; outcome: 'applied' | 'rejected' | null; reason: string | null; expires_at: string }
const props = defineProps<{ session: ManagedSession; now: number }>()
const auth = useSession(), uid = useId()
const draft = ref(''), error = ref(''), busy = ref(false), composing = ref(false), confirmStop = ref(false)
const result = ref<Control | null>(null)
const editing = ref<Setting | null>(null), settingValue = ref(''), models = ref<SettingModel[]>([]), loadingSettings = ref(false)
const settingNames: Record<Setting, string> = { rename: 'Name', model: 'Model', effort: 'Effort' }
const settings = ['rename', 'model', 'effort'] as const
const currentSetting = (kind: Setting) => kind === 'rename' ? props.session.display_label || '' : kind === 'model' ? props.session.model || '' : props.session.reasoning_effort || ''
const settingChoices = computed(() => editing.value === 'model' ? models.value.filter(m => m.efforts.includes(props.session.reasoning_effort || '')).map(m => m.model) : models.value.find(m => m.model === props.session.model)?.efforts || [])
const validValue = computed(() => !!editing.value && !!settingValue.value.trim() && [...settingValue.value].length <= 128 && settingValue.value === settingValue.value.trim() && settingValue.value !== currentSetting(editing.value) && (editing.value === 'rename' || settingChoices.value.includes(settingValue.value)))
const uncertain = ref(false)
let epoch = 0, timer: ReturnType<typeof setTimeout> | undefined
let request: { request_id: string; kind: Kind; text?: string; value?: string; expected_ownership: Ownership } | null = null
const available = computed(() => props.session.management_mode === 'managed' && props.session.advertised_capabilities.includes('managed_control_v1'))
const allowed = computed(() => auth.identity?.principal.kind === 'person' && can('harness.control', props.session.project_id))
const fresh = computed(() => {
  const observed = Date.parse(props.session.process_observed_at || '')
  return !!props.session.process_ownership && Number.isFinite(observed) && props.now - observed >= 0 && props.now - observed <= 45000
})
const unavailable = computed(() => !allowed.value ? 'You need permission to control this session.' : props.session.phase === 'stopped' ? 'This session has stopped.' : !fresh.value ? 'Waiting for the owning daemon to confirm this process.' : '')
const waiting = computed(() => busy.value || uncertain.value || (!!result.value && result.value.state !== 'completed'))
const canSteer = computed(() => !!draft.value.trim() && new TextEncoder().encode(draft.value).length <= 8192)
const feedback = computed(() => {
  if (busy.value) return 'Request pending · waiting for the server.'
  if (uncertain.value) return 'Outcome unconfirmed. Check the result before another action.'
  const c = result.value
  if (!c) return ''
  const name = c.kind === 'steer' ? 'Steer' : c.kind === 'interrupt' ? 'Interrupt' : c.kind === 'stop' ? 'Stop' : settingNames[c.kind]
  if (c.state !== 'completed') return `${name} pending · waiting for the daemon.`
  if (c.outcome === 'applied' && settings.includes(c.kind as Setting)) return `${name} applied${c.reason === 'setting_applied_next_turn' ? ' · takes effect next turn.' : '.'}`
  if (c.outcome === 'applied') return c.kind === 'steer' ? (c.reason === 'queued_next_turn' ? 'Steer applied · queued for the next turn.' : 'Steer applied to the running turn.') : c.kind === 'stop' ? 'Stop applied · session exited.' : 'Interrupt applied · current turn interrupted.'
  const reasons: Record<string, string> = { setting_rejected: `${name} rejected by the harness.`, setting_catalog_changed: 'Rejected · the account catalog changed.', authorization_expired: 'Expired before delivery.', authorization_revoked: 'Rejected · permission was revoked.', transient_input_unavailable: 'Rejected · the server lost the pending text.', budget_exhausted: 'Rejected · the run reached its budget.', outcome_unconfirmed: 'Outcome unconfirmed · the input will not be sent again.', child_unavailable: 'Rejected · the owned process is unavailable.', graceful_stop_timeout: 'Stop unconfirmed · the process may still be running.' }
  return reasons[c.reason || ''] || `${name} rejected · refresh this session before trying again.`
})
const path = () => `/projects/${encodeURIComponent(props.session.project_id)}/harness-sessions/${encodeURIComponent(props.session.id)}`
async function json<T>(url: string, body?: unknown): Promise<T> {
  const response = await api(url, body === undefined ? {} : { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) })
  const data = await response.json().catch(() => ({}))
  if (!response.ok) throw new APIError(response.status, data.error || 'The control could not be requested.')
  return data as T
}
function reset() { epoch++; editing.value = null; settingValue.value = ''; models.value = []; loadingSettings.value = false; clearTimeout(timer); draft.value = ''; result.value = null; request = null; error.value = ''; busy.value = false; uncertain.value = false; composing.value = false; confirmStop.value = false }
watch(() => `${props.session.project_id}/${props.session.id}`, reset)
onBeforeUnmount(() => { epoch++; clearTimeout(timer); request = null })
async function check(turn = epoch) {
  const id = result.value?.id || request?.request_id
  if (!id || turn !== epoch) return
  clearTimeout(timer)
  try {
    const value = await json<Control>(`${path()}/controls/${encodeURIComponent(id)}`)
    if (turn !== epoch) return
    if (value.session_id !== props.session.id || value.id !== id) throw new Error('The control response did not match this session.')
    result.value = value; uncertain.value = false; error.value = ''; request = null
    if (value.state === 'completed') return
    if (Date.now() >= Date.parse(value.expires_at) + 5000) { uncertain.value = true; return }
    timer = setTimeout(() => void check(turn), 1500)
  } catch (e) { if (turn === epoch) { uncertain.value = true; error.value = e instanceof Error ? e.message : 'The result could not be checked.' } }
}
async function submit(kind: Kind) {
  if (unavailable.value || waiting.value || !available.value || !props.session.advertised_capabilities.includes(kind) || (kind === 'steer' && !canSteer.value)) return
  if (settings.includes(kind as Setting) && !validValue.value) return
  request = { request_id: crypto.randomUUID(), kind, expected_ownership: { ...props.session.process_ownership! }, ...(kind === 'steer' ? { text: draft.value } : settings.includes(kind as Setting) ? { value: settingValue.value } : {}) }
  await sendRequest()
}
async function editSetting(kind: Setting) {
  if (unavailable.value || waiting.value) return
  editing.value = kind; settingValue.value = currentSetting(kind); composing.value = false; confirmStop.value = false; error.value = ''
  if (kind === 'rename') return
  const turn = epoch
  loadingSettings.value = true
  try {
    const data = await json<{ models: SettingModel[] }>(`${path()}/managed-settings`)
    if (turn === epoch) models.value = data.models
  } catch (e) { if (turn === epoch) error.value = e instanceof Error ? e.message : 'Settings could not be loaded.' }
  finally { if (turn === epoch) loadingSettings.value = false }
}
async function sendRequest() {
  if (!request || busy.value) return
  const turn = epoch, payload = request
  busy.value = true; error.value = ''; confirmStop.value = false
  try {
    const value = await json<Control>(`${path()}/managed-controls`, payload)
    if (turn !== epoch) return
    if (value.session_id !== props.session.id || value.id !== payload.request_id) throw new Error('The control response did not match this session.')
    result.value = value; request = null; uncertain.value = false; draft.value = ''; composing.value = false; editing.value = null
    void check(turn)
  } catch (e) {
    if (turn !== epoch) return
    error.value = e instanceof Error ? e.message : 'The control could not be requested.'
    // A lost response can hide an accepted request. Keep its exact ID and
    // payload in memory for an explicit retry; never create a second request.
    uncertain.value = !(e instanceof APIError && e.status >= 400 && e.status < 500)
    if (!uncertain.value) request = null
  } finally { if (turn === epoch) busy.value = false }
}
</script>

<template>
  <section v-if="available" class="managed-controls" aria-label="Session controls">
    <div v-if="settings.some(kind => session.advertised_capabilities.includes(kind))" class="settings-row" aria-label="Session settings">
      <span class="settings-title">Session settings</span>
      <button v-for="kind in settings" :key="kind" type="button" class="setting" :disabled="!!unavailable || waiting || !session.advertised_capabilities.includes(kind)" :aria-label="`Edit ${settingNames[kind].toLowerCase()}`" :title="currentSetting(kind)" @click="editSetting(kind)">
        <span>{{ settingNames[kind] }}</span><strong>{{ currentSetting(kind) || 'Set' }}</strong><AppIcon name="chevron-right" :size="12" />
      </button>
    </div>
    <form v-if="editing" class="setting-form" @submit.prevent="submit(editing)">
      <label :for="`${uid}-setting`">{{ settingNames[editing] }}</label>
      <input v-if="editing === 'rename'" :id="`${uid}-setting`" v-model="settingValue" maxlength="128" :disabled="waiting" autocomplete="off" />
      <select v-else :id="`${uid}-setting`" v-model="settingValue" :disabled="waiting || loadingSettings || !settingChoices.length">
        <option value="" disabled>Choose {{ settingNames[editing].toLowerCase() }}</option>
        <option v-for="value in settingChoices" :key="value" :value="value">{{ value }}</option>
      </select>
      <p v-if="editing !== 'rename' && !loadingSettings && !settingChoices.length" class="hint">No compatible choices in this account’s catalog.</p>
      <div class="control-row"><button type="submit" class="btn sm primary" :disabled="!validValue || waiting || loadingSettings || !!unavailable">Save {{ settingNames[editing].toLowerCase() }}</button><button type="button" class="btn sm ghost" :disabled="waiting" @click="editing = null">Cancel</button></div>
    </form>
    <div class="control-row">
      <button type="button" class="btn sm ghost" :disabled="!!unavailable || waiting || !session.advertised_capabilities.includes('steer')" @click="composing = !composing; confirmStop = false; editing = null"><AppIcon name="send" :size="14" />Steer</button>
      <button type="button" class="btn sm ghost" :disabled="!!unavailable || waiting || !session.advertised_capabilities.includes('interrupt')" @click="submit('interrupt')"><AppIcon name="interrupt" :size="14" />Interrupt</button>
      <button type="button" class="btn sm ghost" :disabled="!!unavailable || waiting || !session.advertised_capabilities.includes('stop')" @click="confirmStop = true; composing = false; editing = null"><AppIcon name="halt" :size="14" />Stop</button>
    </div>
    <p v-if="unavailable" class="hint">{{ unavailable }}</p>
    <form v-if="composing" class="steer-form" @submit.prevent="submit('steer')">
      <label :for="`${uid}-steer`">What should change?</label>
      <textarea :id="`${uid}-steer`" v-model="draft" rows="3" maxlength="8192" :disabled="waiting" :aria-describedby="`${uid}-help`" placeholder="Describe the next step…" />
      <p :id="`${uid}-help`" class="hint">{{ session.harness === 'claude' ? 'Queues your message for the next turn and interrupts the current one.' : 'Adds your message to the running turn.' }}</p>
      <div class="control-row"><button type="submit" class="btn sm primary" :disabled="!canSteer || waiting || !!unavailable">Send steer</button><button type="button" class="btn sm ghost" :disabled="waiting" @click="composing = false">Cancel</button></div>
    </form>
    <div v-if="confirmStop" class="stop-confirm"><span>End this session?</span><button type="button" class="btn sm" @click="submit('stop')">Confirm stop</button><button type="button" class="btn sm ghost" @click="confirmStop = false">Cancel</button></div>
    <p v-if="feedback" class="feedback" role="status">{{ feedback }}</p>
    <p v-if="error" class="hint" role="alert">{{ error }}</p>
    <div v-if="uncertain" class="control-row"><button type="button" class="btn sm ghost" :disabled="busy" @click="check()">Check result</button><button v-if="request" type="button" class="btn sm ghost" :disabled="busy" @click="sendRequest">Retry same request</button></div>
    <details><summary><AppIcon name="chevron-right" :size="12" />Control limits</summary><p>Tools stay bound to this run and its budget. Other processes running as the same OS user are outside this isolation boundary.</p></details>
  </section>
</template>

<style scoped>
.managed-controls{display:grid;gap:10px;padding-top:12px;min-width:0}
.settings-row{display:grid;grid-template-columns:repeat(3,minmax(0,1fr));gap:6px}
.settings-title{grid-column:1/-1;font-size:12px;color:var(--ink-3)}
.setting{display:flex;align-items:center;gap:6px;min-width:0;text-align:left;border:1px solid var(--line);border-radius:var(--radius-row);padding:8px;background:var(--surface);color:var(--ink);font:inherit;cursor:pointer}
.setting span{font-size:11px;color:var(--ink-3)}.setting strong{font-size:12px;font-weight:500;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;flex:1}.setting :deep(svg){flex-shrink:0}.setting:disabled{opacity:.55;cursor:default}
.setting-form{display:grid;gap:8px}.setting-form label{font-size:12px;font-weight:600}.setting-form input,.setting-form select{box-sizing:border-box;min-width:0;width:100%;border:1px solid var(--line);border-radius:var(--radius-row);background:var(--surface);color:var(--ink);font:inherit;padding:8px}
@media(max-width:600px){.settings-row{grid-template-columns:minmax(0,1fr)}.setting{padding:7px 8px}.setting span{width:42px}}
.control-row,.stop-confirm{display:flex;align-items:center;gap:8px;flex-wrap:wrap}
.steer-form{display:grid;gap:8px}.steer-form label{font-size:12px;font-weight:600}
textarea{box-sizing:border-box;width:100%;resize:vertical;min-height:78px;border:1px solid var(--line);border-radius:var(--radius-row);background:var(--surface);color:var(--ink);font:inherit;padding:10px}
.hint,.feedback,details{font-size:12px;line-height:1.5;margin:0}.hint,details{color:var(--ink-3)}
.feedback{background:var(--surface-sunken);border-radius:var(--radius-row);padding:8px 10px;overflow-wrap:anywhere}
summary{cursor:pointer;width:fit-content;display:flex;align-items:center;gap:6px;list-style:none}summary::-webkit-details-marker{display:none}details[open] summary :deep(svg){transform:rotate(90deg)}details p{margin:6px 0 0}.stop-confirm{font-size:13px}
</style>
