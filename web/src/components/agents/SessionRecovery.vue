<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, useId, watch } from 'vue'
import { useRouter } from 'vue-router'
import { APIError } from '../../lib/api'
import { can } from '../../lib/authz'
import { toast } from '../../lib/toast'
import type { HarnessSession } from '../../lib/agents'
import { readControlResponse, readSessionRecovery, forceStopSession, archiveSession } from '../../lib/agentRows'
import { useAgents } from '../../stores/agents'
import AppIcon from '../AppIcon.vue'

const props = defineProps<{ session: HarnessSession; hideTrigger?: boolean }>()
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
let expiryTimer: ReturnType<typeof setTimeout> | undefined
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
async function json<T>(answer: Promise<Response>): Promise<T> {
  const response = await answer
  const data = await response.json().catch(() => ({}))
  if (!response.ok) throw new APIError(response.status, data.error || `Request failed (${response.status})`)
  return data as T
}
async function refresh() {
  const turn = ++epoch
  clearTimeout(timer); clearTimeout(expiryTimer); forceControl.value = null; forceExpired.value = false
  loading.value = true; error.value = ''; confirmation.value = ''; preview.value = null
  requestId = crypto.randomUUID()
  try {
    const value = await json<Preview>(readSessionRecovery(props.session.project_id, props.session.id))
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
  epoch++; clearTimeout(timer); clearTimeout(expiryTimer); visible.value = false; dialog.value?.close(); opener?.focus({ preventScroll: true })
}
watch(() => props.session.id, () => { epoch++; clearTimeout(timer); clearTimeout(expiryTimer); visible.value = false; busy.value = false; dialog.value?.close(); preview.value = null })
watch(action, () => { confirmation.value = ''; requestId = crypto.randomUUID(); error.value = '' })
onBeforeUnmount(() => { epoch++; clearTimeout(timer); clearTimeout(expiryTimer) })
async function finishForce() {
  toast(forceResult.value, { sticky: true, key: `session-force-${props.session.id}` })
  busy.value = false; close()
  await router.replace('/agents')
  await agents.loadAll()
}
async function checkForce(turn = epoch) {
  if (!forceControl.value || turn !== epoch || !visible.value) return
  if (forceControl.value.state === 'completed') { await finishForce(); return }
  if (Date.now() >= forceDeadline) { forceExpired.value = true; return }
  try {
    const result = await json<ForceControl>(readControlResponse(props.session.project_id, props.session.id, forceControl.value.id))
    if (turn !== epoch) return
    forceControl.value = result; error.value = ''
    if (result.state === 'completed') { forceExpired.value = false; await finishForce(); return }
  } catch { if (turn === epoch) error.value = 'The control result could not be checked. Process exit is unconfirmed.' }
  if (turn === epoch && visible.value && !forceExpired.value) timer = setTimeout(() => void checkForce(turn), Math.max(0, Math.min(2000, forceDeadline - Date.now())))
}
async function submit() {
  if (!canSubmit.value || !preview.value) return
  busy.value = true; error.value = ''
  const turn = epoch
  try {
    const result = await json<ForceControl>((action.value === 'force' ? forceStopSession : archiveSession)(props.session.project_id, props.session.id, { expected_revision: preview.value.observed_revision, confirmation: confirmation.value, request_id: requestId, reason: reason.value.trim() }))
    // Accepted: a force stop is now a control for the daemon, an archive is done. Either changes the lists.
    void agents.afterWrite()
    if (turn !== epoch) return
    if (action.value === 'force') {
      forceControl.value = result
      const expiry = result.expires_at ? Date.parse(result.expires_at) : NaN
      forceDeadline = Number.isFinite(expiry) ? Math.min(expiry, Date.now() + 45000) : Date.now() + 45000
      expiryTimer = setTimeout(() => {
        if (turn === epoch && visible.value && forceControl.value?.state !== 'completed') {
          forceExpired.value = true; clearTimeout(timer)
        }
      }, Math.max(0, forceDeadline - Date.now()))
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
defineExpose({ open })
</script>

<template>
  <button v-if="allowed && !session.archived_at && !hideTrigger" type="button" class="btn sm ghost" @click="open"><AppIcon name="wrench" :size="14" />Recover</button>
  <dialog ref="dialog" class="recovery-dialog" :aria-labelledby="`${uid}-title`" :aria-describedby="`${uid}-intro`" @cancel.prevent="close">
    <div class="recovery-card">
      <h2 :id="`${uid}-title`">{{ done ? 'Session archived' : forceControl ? 'Force stop requested' : 'Recover session' }}</h2>
      <div class="recovery-actions">
        <button ref="cancelButton" type="button" class="btn" :disabled="busy" @click="close"><span class="btn-label"><span>{{ done || forceControl ? 'Close' : 'Cancel' }}</span><span aria-hidden="true">Cancel</span></span></button>
        <button v-if="!done && !forceControl && !preview && !loading" type="button" class="btn" @click="refresh">Refresh details</button>
        <button v-if="forceExpired" type="button" class="btn" @click="refresh">Refresh details</button>
        <button v-if="!done && !forceControl && available" type="button" class="btn" :class="action === 'force' ? 'danger' : 'primary'" :disabled="!canSubmit" @click="submit"><span class="btn-label"><span>{{ busy ? 'Requesting…' : action === 'force' ? 'Force stop session' : 'Archive session' }}</span><span aria-hidden="true">Force stop session</span><span aria-hidden="true">Archive session</span></span></button>
      </div>
      <div class="recovery-body">
      <p v-if="done" :id="`${uid}-intro`" role="status">The registration is closed and its history is retained. Process state is unknown; no process was stopped.</p>
      <p v-else-if="forceControl" :id="`${uid}-intro`" role="status">{{ forceResult }}</p>
      <template v-else>
        <p :id="`${uid}-intro`">Close a session that is stuck or gone. Its history and ticket links stay.</p>
        <p v-if="loading" class="quiet" role="status">Loading current session…</p>
        <template v-if="preview">
          <div v-if="preview.force_stop_available && preview.can_archive" class="choices" role="radiogroup" aria-label="Recovery action">
            <button type="button" role="radio" class="choice" :aria-checked="action === 'archive'" :aria-describedby="`${uid}-archive-desc`" :disabled="busy" @click="action = 'archive'">
              <span class="choice-dot" aria-hidden="true" /><span class="choice-text"><strong>Archive registration</strong><small :id="`${uid}-archive-desc`">Closes the record only. No process is touched.</small></span>
            </button>
            <button type="button" role="radio" class="choice" :aria-checked="action === 'force'" :aria-describedby="`${uid}-force-desc`" :disabled="busy" @click="action = 'force'">
              <span class="choice-dot" aria-hidden="true" /><span class="choice-text"><strong>Force stop process</strong><small :id="`${uid}-force-desc`">Kills its owned process group. Unsaved work may be lost.</small></span>
            </button>
          </div>
          <dl class="recovery-facts">
            <div><dt>Session</dt><dd :title="preview.session_id">{{ preview.display_label || 'Unnamed session' }}</dd></div>
            <div><dt>Host</dt><dd>{{ preview.host }}</dd></div>
            <template v-if="action === 'force' && preview.process_ownership">
              <div><dt>Affected</dt><dd>Root PID {{ preview.process_ownership.root_pid }} and every process in its owned group {{ preview.process_ownership.group_id }}, including children that join it. Escaped descendants and other process groups are excluded.</dd></div>
              <div><dt>Daemon</dt><dd :title="`Generation ${preview.process_ownership.generation} · process ${preview.process_ownership.process_id}`">{{ preview.process_ownership.daemon_id }} · started {{ preview.process_ownership.started_at }}</dd></div>
            </template>
          </dl>
          <p class="recovery-note" :class="{ danger: action === 'force' }"><AppIcon :name="action === 'force' ? 'alert' : 'info'" :size="15" /><span>{{ action === 'force' ? 'Force stop immediately kills the owned process group. Unsaved work may be lost. The daemon rechecks this exact identity first.' : preview.process_scope }}</span></p>
          <template v-if="available">
            <label :for="`${uid}-reason`">Reason for recovery</label>
            <input :id="`${uid}-reason`" v-model="reason" class="field" maxlength="240" :disabled="busy" placeholder="Why is this needed?" />
            <label :for="`${uid}-confirmation`">Type the exact confirmation</label>
            <code class="confirmation-text">{{ expectedConfirmation }}</code>
            <input :id="`${uid}-confirmation`" v-model="confirmation" class="field" autocomplete="off" spellcheck="false" :disabled="busy" />
          </template>
          <p v-else class="quiet">No recovery action is available for this registration.</p>
          <p v-if="!preview.force_stop_available" class="fine">No force stop: {{ preview.force_stop_reason.replace(/^Force stop /, '') }}</p>
          <p v-if="preview.archive_unavailable_reason" class="fine">Archive unavailable: {{ preview.archive_unavailable_reason }}</p>
        </template>
      </template>
      <p v-if="error" class="recovery-error" role="alert">{{ error }}</p>
      </div>
    </div>
  </dialog>
</template>

<style scoped>
.recovery-dialog { position: fixed; inset: 96px 0 auto; margin: 0 auto; width: min(var(--dialog-m), calc(100vw - 28px)); max-height: calc(100dvh - 112px); padding: 0; border: 1px solid var(--glass-edge); border-radius: var(--radius); background: var(--surface-raised); color: var(--ink); box-shadow: var(--shadow-pop); overflow: hidden; }
.recovery-dialog::backdrop { background: var(--scrim); backdrop-filter: blur(2px); }
.recovery-card { padding: 22px 24px 20px; display: flex; flex-direction: column; gap: 16px; box-sizing: border-box; max-height: min(660px, calc(100dvh - 114px)); }
.recovery-body { flex: 1; align-content: start; display: grid; gap: 14px; min-height: 0; overflow-y: auto; overscroll-behavior: contain; padding: 2px; margin: -2px; }
h2 { flex: none; font-size: 19px; line-height: 1.2; }
p { font-size: 13px; line-height: 1.5; color: var(--ink-2); }
.quiet { color: var(--ink-3); }
.choices { display: grid; gap: 8px; }
.choice { display: flex; align-items: flex-start; gap: 10px; width: 100%; padding: 11px 12px; border: 0; border-radius: 10px; background: var(--surface-sunken); box-shadow: inset 0 0 0 1px var(--line); color: var(--ink); text-align: left; cursor: pointer; }
.choice[aria-checked="true"] { background: var(--row-selected); box-shadow: inset 0 0 0 1.5px var(--teal); }
.choice:focus-visible { outline: none; box-shadow: var(--focus-ring); }
.choice-dot { flex: none; width: 16px; height: 16px; margin-top: 1px; border-radius: 50%; box-shadow: inset 0 0 0 1.5px var(--ink-3); }
.choice[aria-checked="true"] .choice-dot { box-shadow: inset 0 0 0 5px var(--teal); }
.choice-text { display: grid; gap: 2px; }
.choice-text strong { font-size: 13.5px; font-weight: 600; }
.choice-text small { font-size: 12px; color: var(--ink-2); }
.recovery-facts { display: grid; gap: 8px; margin: 0; }
.recovery-facts div { display: grid; grid-template-columns: 72px minmax(0, 1fr); gap: 12px; }
dt { font-size: 12.5px; color: var(--ink-3); }
dd { margin: 0; font-size: 13px; overflow-wrap: anywhere; }
.recovery-note { display: flex; gap: 8px; align-items: flex-start; }
.recovery-note svg { flex-shrink: 0; margin-top: 2px; color: var(--ink-3); }
.recovery-note.danger, .recovery-note.danger svg { color: var(--danger); }
.fine { font-size: 12px; color: var(--ink-3); }
label { font-size: 12px; font-weight: 600; margin-bottom: -8px; color: var(--ink-2); }
.field { width: 100%; min-height: 40px; }
code { display: block; font: 11px/1.5 var(--mono); overflow-wrap: anywhere; color: var(--ink-2); }
.confirmation-text { padding: 8px 10px; border-radius: 8px; background: var(--code-bg); user-select: all; }
.recovery-error { color: var(--danger); }
.recovery-actions { flex-shrink: 0; display: flex; justify-content: flex-end; flex-wrap: wrap; gap: 8px; }
/* Actions keep their labels' width (AEON-730); changing labels reserve their widest form. */
@media (max-width: 500px) { .recovery-dialog { inset: 0; width: 100%; height: 100dvh; max-width: none; max-height: none; margin: 0; border-radius: 0; } .recovery-card { height: 100%; max-height: none; padding: 18px 16px calc(16px + env(safe-area-inset-bottom)); gap: 14px; } .recovery-body { order: 1; } .recovery-actions { order: 2; } .recovery-actions .btn { flex: 1; } }
</style>
