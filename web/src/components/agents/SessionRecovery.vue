<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, useId, watch } from 'vue'
import { useRouter } from 'vue-router'
import { api, APIError } from '../../lib/api'
import { can } from '../../lib/authz'
import { toast } from '../../lib/toast'
import type { HarnessSession } from '../../lib/agents'
import { useAgents } from '../../stores/agents'
import AppIcon from '../AppIcon.vue'

const props = defineProps<{ session: HarnessSession }>()
const agents = useAgents()
const router = useRouter()
const uid = useId()
const dialog = ref<HTMLDialogElement>()
const cancelButton = ref<HTMLButtonElement>()
const loading = ref(false), busy = ref(false), done = ref(false), visible = ref(false)
const error = ref(''), confirmation = ref(''), reason = ref('')
const action = ref<'archive' | 'force'>('archive')
let epoch = 0, requestId = ''
let opener: HTMLElement | null = null
let timer: ReturnType<typeof setTimeout> | undefined
let forceDeadline = 0
const forceExpired = ref(false)
interface Ownership { daemon_id: string; generation: string; process_id: string; root_pid: number; group_id: number; started_at: string }
interface Preview {
  session_id: string; host: string; display_label: string | null; observed_revision: string
  confirmation: string; process_state: string; process_scope: string; can_archive: boolean
  archive_unavailable_reason?: string
  force_stop_available: boolean; force_stop_reason: string; force_confirmation?: string; process_ownership?: Ownership
}
interface ForceControl { id: string; state: string; outcome: string | null; reason: string | null; expires_at?: string }
const forceControl = ref<ForceControl | null>(null)
const preview = ref<Preview | null>(null)
const allowed = computed(() => can('harness.recover', props.session.project_id) || can('harness.force_stop', props.session.project_id))
const expectedConfirmation = computed(() => action.value === 'force' ? preview.value?.force_confirmation : preview.value?.confirmation)
const available = computed(() => action.value === 'force' ? preview.value?.force_stop_available && can('harness.force_stop', props.session.project_id) : preview.value?.can_archive && can('harness.recover', props.session.project_id))
const canSubmit = computed(() => available.value && !busy.value && !loading.value && !done.value && !forceControl.value && confirmation.value === expectedConfirmation.value && !!reason.value.trim())
const forceResult = computed(() => {
  const control = forceControl.value
  if (!control) return ''
  if (forceExpired.value) return 'The confirmation expired before a verified result was received. Process exit is unconfirmed. Refresh details before another action.'
  if (control.state !== 'completed') return 'Waiting for the owning daemon. Process exit has not been confirmed.'
  if (control.outcome === 'applied' && control.reason === 'owned_group_signalled_root_exited') return 'The daemon signalled the owned process group and verified that its root process exited. Processes outside that group were not targeted.'
  return `The daemon did not confirm termination (${(control.reason || 'unknown result').replaceAll('_', ' ')}). Refresh the session before another action.`
})
const path = () => `/projects/${encodeURIComponent(props.session.project_id)}/harness-sessions/${encodeURIComponent(props.session.id)}`
async function json<T>(url: string, body?: unknown): Promise<T> {
  const response = await api(url, body === undefined ? {} : { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) })
  const data = await response.json().catch(() => ({}))
  if (!response.ok) throw new APIError(response.status, data.error || `Request failed (${response.status})`)
  return data as T
}
async function refresh() {
  const turn = ++epoch
  clearTimeout(timer); forceControl.value = null; forceExpired.value = false
  loading.value = true; error.value = ''; confirmation.value = ''; preview.value = null
  requestId = crypto.randomUUID()
  try {
    const value = await json<Preview>(`${path()}/recovery`)
    if (turn === epoch && visible.value && value.session_id === props.session.id) {
      preview.value = value
      if (!value.can_archive && value.force_stop_available) action.value = 'force'
    }
  } catch (e) { if (turn === epoch) error.value = e instanceof Error ? e.message : 'Recovery details could not be loaded.' }
  finally { if (turn === epoch) loading.value = false }
}
async function open() {
  if (!allowed.value) return
  opener = document.activeElement as HTMLElement
  done.value = false; reason.value = ''; visible.value = true; action.value = 'archive'; forceControl.value = null
  dialog.value?.showModal(); void refresh()
  await nextTick(); cancelButton.value?.focus()
}
function close() {
  if (busy.value) return
  epoch++; clearTimeout(timer); visible.value = false; dialog.value?.close(); opener?.focus({ preventScroll: true })
}
watch(() => props.session.id, () => { epoch++; clearTimeout(timer); visible.value = false; busy.value = false; dialog.value?.close(); preview.value = null })
watch(action, () => { confirmation.value = ''; requestId = crypto.randomUUID(); error.value = '' })
onBeforeUnmount(() => { epoch++; clearTimeout(timer) })
async function checkForce(turn = epoch) {
  if (!forceControl.value || turn !== epoch || !visible.value) return
  if (Date.now() >= forceDeadline) { forceExpired.value = true; return }
  try {
    const result = await json<ForceControl>(`${path()}/controls/${encodeURIComponent(forceControl.value.id)}`)
    if (turn !== epoch) return
    forceControl.value = result; error.value = ''
    if (result.state === 'completed') { await agents.loadAll(); return }
  } catch { if (turn === epoch) error.value = 'The control result could not be checked. Process exit is unconfirmed.' }
  if (turn === epoch && visible.value) timer = setTimeout(() => void checkForce(turn), Math.max(0, Math.min(2000, forceDeadline - Date.now())))
}
async function submit() {
  if (!canSubmit.value || !preview.value) return
  busy.value = true; error.value = ''
  const turn = epoch
  try {
    const result = await json<ForceControl>(`${path()}/${action.value === 'force' ? 'controls/force-stop' : 'archive'}`, { expected_revision: preview.value.observed_revision, confirmation: confirmation.value, request_id: requestId, reason: reason.value.trim() })
    if (turn !== epoch) return
    if (action.value === 'force') {
      forceControl.value = result
      const expiry = result.expires_at ? Date.parse(result.expires_at) : NaN
      forceDeadline = Number.isFinite(expiry) ? Math.min(expiry, Date.now() + 45000) : Date.now() + 45000
      void checkForce(turn)
    } else {
      done.value = true
      toast('Session archived. History is retained. Process state is unknown; no process was stopped.', { sticky: true, key: `session-archive-${props.session.id}` })
      busy.value = false; close()
      await router.replace('/agents')
      await agents.loadAll()
    }
  } catch (e) {
    if (turn !== epoch) return
    if (e instanceof APIError && e.status === 409) {
      preview.value = null; confirmation.value = ''
      error.value = 'This session changed. Refresh the details, then confirm the current session again.'
    } else error.value = e instanceof Error ? e.message : 'The result is unknown. Retry with the same confirmation.'
  } finally { if (turn === epoch) busy.value = false }
}
</script>

<template>
  <button v-if="allowed && !session.archived_at" type="button" class="btn sm" @click="open"><AppIcon name="wrench" :size="14" />Recover</button>
  <dialog ref="dialog" class="recovery-dialog" :aria-labelledby="`${uid}-title`" :aria-describedby="`${uid}-intro`" @cancel.prevent="close">
    <div class="recovery-card">
      <div class="recovery-body">
      <h2 :id="`${uid}-title`">{{ done ? 'Session archived' : forceControl ? 'Force stop requested' : 'Recover session' }}</h2>
      <p v-if="done" :id="`${uid}-intro`" role="status">The registration is closed and its history is retained. Process state is unknown; no process was stopped.</p>
      <p v-else-if="forceControl" :id="`${uid}-intro`" role="status">{{ forceResult }}</p>
      <template v-else>
        <p :id="`${uid}-intro`">Archive a registration or stop a verified owned process. Ticket links, outcomes and history remain available.</p>
        <p v-if="loading" role="status">Loading current session…</p>
        <template v-if="preview">
          <div v-if="preview.force_stop_available && preview.can_archive" class="seg" role="radiogroup" aria-label="Recovery action">
            <button type="button" role="radio" :aria-checked="action === 'archive'" :disabled="busy" @click="action = 'archive'">Archive registration</button>
            <button type="button" role="radio" :aria-checked="action === 'force'" :disabled="busy" @click="action = 'force'">Force stop process</button>
          </div>
          <dl class="recovery-facts">
            <div><dt>Session</dt><dd>{{ preview.display_label || 'Unnamed session' }}<code>{{ preview.session_id }}</code></dd></div>
            <div><dt>Host</dt><dd>{{ preview.host }}</dd></div>
            <div v-if="action === 'archive'"><dt>Process state</dt><dd>Unknown</dd></div>
            <template v-else-if="preview.process_ownership">
              <div><dt>Affected scope</dt><dd>Root PID {{ preview.process_ownership.root_pid }} and every process in its owned group {{ preview.process_ownership.group_id }}, including children that join it. Escaped descendants and other process groups are excluded.</dd></div>
              <div><dt>Daemon</dt><dd>{{ preview.process_ownership.daemon_id }}<code>{{ preview.process_ownership.generation }}</code></dd></div>
              <div><dt>Process identity</dt><dd><code>{{ preview.process_ownership.process_id }}</code>Started {{ preview.process_ownership.started_at }}</dd></div>
            </template>
          </dl>
          <div class="recovery-note"><AppIcon :name="action === 'force' ? 'alert' : 'info'" :size="16" /><p>{{ action === 'force' ? 'Force stop immediately kills the owned process group. Unsaved work may be lost. The daemon must recheck this exact identity before acting.' : `${preview.process_scope} A missing heartbeat does not prove that a process exited.` }}</p></div>
          <p v-if="!preview.force_stop_available" class="force-status"><strong>Force stop unavailable.</strong> {{ preview.force_stop_reason }}</p>
          <p v-if="preview.archive_unavailable_reason" class="force-status"><strong>Archive unavailable.</strong> {{ preview.archive_unavailable_reason }}</p>
          <template v-if="available">
            <label :for="`${uid}-reason`">Reason for recovery</label>
            <input :id="`${uid}-reason`" v-model="reason" class="field" maxlength="240" :disabled="busy" placeholder="Why is this action needed?" />
            <label :for="`${uid}-confirmation`">Type the exact confirmation</label>
            <code class="confirmation-text">{{ expectedConfirmation }}</code>
            <input :id="`${uid}-confirmation`" v-model="confirmation" class="field" autocomplete="off" spellcheck="false" :disabled="busy" />
          </template>
          <p v-else>No recovery action is available for this registration.</p>
        </template>
      </template>
      <p v-if="error" class="recovery-error" role="alert">{{ error }}</p>
      </div>
      <div class="recovery-actions">
        <button ref="cancelButton" type="button" class="btn" :disabled="busy" @click="close">{{ done || forceControl ? 'Close' : 'Cancel' }}</button>
        <button v-if="!done && !forceControl && !preview && !loading" type="button" class="btn" @click="refresh">Refresh details</button>
        <button v-if="forceExpired" type="button" class="btn" @click="refresh">Refresh details</button>
        <button v-if="!done && !forceControl && available" type="button" class="btn" :class="action === 'force' ? 'danger' : 'primary'" :disabled="!canSubmit" @click="submit">{{ busy ? 'Requesting…' : action === 'force' ? 'Force stop session' : 'Archive session' }}</button>
      </div>
    </div>
  </dialog>
</template>

<style scoped>
.recovery-dialog { width: min(540px, calc(100vw - 28px)); max-height: calc(100dvh - 28px); padding: 0; border: 1px solid var(--glass-edge); border-radius: var(--radius); background: var(--surface-raised); color: var(--ink); box-shadow: var(--shadow-pop); overflow: hidden; }
.recovery-dialog::backdrop { background: var(--scrim); backdrop-filter: blur(2px); }
.recovery-card { padding: 24px; display: flex; flex-direction: column; gap: 14px; max-height: calc(100dvh - 30px); }
.recovery-body { display: grid; gap: 14px; min-height: 0; overflow-y: auto; overscroll-behavior: contain; padding-right: 2px; }
h2 { font-size: 20px; line-height: 1.2; }
p { font-size: 13px; line-height: 1.5; color: var(--ink-2); }
.recovery-facts { display: grid; gap: 12px; margin: 0; padding: 14px; background: var(--code-bg); border-radius: 10px; }
.recovery-facts div { display: grid; grid-template-columns: 95px minmax(0, 1fr); gap: 12px; }
dt { font-size: 12px; color: var(--ink-3); }
dd { margin: 0; font-size: 13px; overflow-wrap: anywhere; }
code { display: block; font: 11px/1.5 var(--mono); overflow-wrap: anywhere; color: var(--ink-2); }
.recovery-note { display: flex; gap: 10px; align-items: flex-start; }
.recovery-note svg { flex-shrink: 0; margin-top: 2px; color: var(--ink-3); }
.force-status { font-size: 12px; }
label { font-size: 12px; font-weight: 600; margin-bottom: -8px; }
.field { width: 100%; min-height: 40px; }
.confirmation-text { padding: 10px; border-radius: 8px; background: var(--code-bg); user-select: all; }
.recovery-error { color: var(--danger); }
.recovery-actions { flex-shrink: 0; display: flex; justify-content: flex-end; flex-wrap: wrap; gap: 8px; margin-top: 4px; }
.recovery-actions .btn { min-height: 40px; }
@media (max-width: 500px) { .recovery-card { padding: 18px; gap: 12px; } .recovery-facts div { grid-template-columns: 78px minmax(0, 1fr); gap: 8px; } }
</style>
