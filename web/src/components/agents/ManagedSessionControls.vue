<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, useId, watch } from 'vue'
import { api, APIError } from '../../lib/api'
import { can } from '../../lib/authz'
import type { HarnessSession } from '../../lib/agents'
import { useVisualViewport } from '../../lib/visualViewport'
import { useSession } from '../../stores/session'
import AppIcon from '../AppIcon.vue'
import FloatingPanel from '../work/FloatingPanel.vue'

interface Ownership { daemon_id: string; generation: string; process_id: string; root_pid: number; group_id: number; started_at: string }
type ManagedSession = HarnessSession & { process_ownership?: Ownership; process_observed_at?: string }
type Setting = 'rename' | 'model' | 'effort'
type Kind = 'steer' | 'interrupt' | 'stop' | Setting
interface SettingModel { model: string; efforts: string[] }
interface Control { id: string; session_id: string; kind: Kind; state: 'pending' | 'claimed' | 'completed'; outcome: 'applied' | 'rejected' | null; reason: string | null; expires_at: string }
const props = defineProps<{ session: ManagedSession; now: number }>()
const auth = useSession(), uid = useId()
const draft = ref(''), error = ref(''), busy = ref(false), composing = ref(false), confirmStop = ref(false)
// The session panel is a full-height sheet below 720px. Keep one action row
// there; Steer, Stop and setting edits open a bottom sheet instead of growing it.
const phoneMedia = window.matchMedia('(max-width: 720px)')
const phone = ref(phoneMedia.matches)
const moreAnchor = ref<HTMLElement | null>(null)
const sheetEl = ref<HTMLElement>()
useVisualViewport(sheetEl)
function syncPhone() { phone.value = phoneMedia.matches }
onMounted(() => phoneMedia.addEventListener('change', syncPhone))
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
const sheetOpen = computed(() => phone.value && (composing.value || !!editing.value || confirmStop.value))
const sheetTitle = computed(() => confirmStop.value ? 'Stop' : editing.value ? settingNames[editing.value] : 'Steer')
function closeSheet() { composing.value = false; editing.value = null; confirmStop.value = false }
function backdrop(event: MouseEvent) { if (event.target === sheetEl.value) closeSheet() }
function toggleMore(event: MouseEvent) {
  const anchor = event.currentTarget as HTMLElement
  moreAnchor.value = moreAnchor.value === anchor ? null : anchor
}
function closeMore(restore: boolean) {
  const trigger = moreAnchor.value
  moreAnchor.value = null
  if (restore) trigger?.focus()
}
function pickSetting(kind: Setting) { moreAnchor.value = null; void editSetting(kind) }
function closeIfItem(event: MouseEvent) {
  if ((event.target as Element | null)?.closest?.('[role="menuitem"]')) moreAnchor.value = null
}
watch(sheetOpen, async open => {
  await nextTick()
  const dialog = sheetEl.value as HTMLDialogElement | undefined
  if (!dialog) return
  if (open && !dialog.open) {
    dialog.showModal()
    await nextTick()
    dialog.querySelector<HTMLElement>('textarea, input, select')?.focus()
  } else if (!open && dialog.open) dialog.close()
})
function reset() { epoch++; editing.value = null; settingValue.value = ''; models.value = []; loadingSettings.value = false; clearTimeout(timer); draft.value = ''; result.value = null; request = null; error.value = ''; busy.value = false; uncertain.value = false; composing.value = false; confirmStop.value = false; moreAnchor.value = null }
watch(() => `${props.session.project_id}/${props.session.id}`, reset)
onBeforeUnmount(() => { epoch++; clearTimeout(timer); request = null; phoneMedia.removeEventListener('change', syncPhone) })
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
    <div v-if="!phone && settings.some(kind => session.advertised_capabilities.includes(kind))" class="settings-row" aria-label="Session settings">
      <span class="settings-title">Session settings</span>
      <button v-for="kind in settings" :key="kind" type="button" class="setting" :disabled="!!unavailable || waiting || !session.advertised_capabilities.includes(kind)" :aria-label="`Edit ${settingNames[kind].toLowerCase()}`" :title="currentSetting(kind)" @click="editSetting(kind)">
        <span>{{ settingNames[kind] }}</span><strong>{{ currentSetting(kind) || 'Set' }}</strong><AppIcon name="chevron-right" :size="12" />
      </button>
    </div>
    <form v-if="!phone && editing" class="setting-form" @submit.prevent="submit(editing)">
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
      <button type="button" class="btn sm ghost" :disabled="!!unavailable || waiting || !session.advertised_capabilities.includes('steer')" @click="composing = !composing; confirmStop = false; editing = null"><AppIcon name="send" :size="14" /><span>Steer</span></button>
      <button type="button" class="btn sm ghost" :disabled="!!unavailable || waiting || !session.advertised_capabilities.includes('interrupt')" @click="submit('interrupt')"><AppIcon name="interrupt" :size="14" /><span>Interrupt</span></button>
      <button type="button" class="btn sm ghost" :disabled="!!unavailable || waiting || !session.advertised_capabilities.includes('stop')" @click="confirmStop = true; composing = false; editing = null"><AppIcon name="halt" :size="14" /><span>Stop</span></button>
      <button v-if="phone" type="button" class="icon-btn flat more" aria-label="More session actions" aria-haspopup="menu" :aria-expanded="!!moreAnchor" :disabled="waiting" @click="toggleMore"><AppIcon name="more" :size="16" /></button>
    </div>
    <FloatingPanel v-if="phone && moreAnchor" :anchor="moreAnchor" align="end" :width="288" label="More session actions" @close="closeMore">
      <div class="overflow-menu">
        <div class="overflow-list" role="menu" aria-label="More session actions" :aria-describedby="`${uid}-limits`" @click="closeIfItem">
          <template v-for="kind in settings" :key="kind">
            <button v-if="session.advertised_capabilities.includes(kind)" type="button" role="menuitem" class="menu-item" :disabled="!!unavailable || waiting" :aria-label="`Edit ${settingNames[kind].toLowerCase()}`" :title="currentSetting(kind)" @click="pickSetting(kind)">
              <AppIcon name="edit" :size="16" /><span class="mi-text"><span>{{ settingNames[kind] }}</span><small>{{ currentSetting(kind) || 'Set' }}</small></span>
            </button>
          </template>
          <slot name="more" />
        </div>
        <p :id="`${uid}-limits`" class="menu-note">Tools stay bound to this run and its budget. Other processes running as the same OS user are outside this isolation boundary.</p>
      </div>
    </FloatingPanel>
    <p v-if="unavailable" class="hint">{{ unavailable }}</p>
    <form v-if="!phone && composing" class="steer-form" @submit.prevent="submit('steer')">
      <label :for="`${uid}-steer`">What should change?</label>
      <textarea :id="`${uid}-steer`" v-model="draft" rows="3" maxlength="8192" :disabled="waiting" :aria-describedby="`${uid}-help`" placeholder="Describe the next step…" />
      <p :id="`${uid}-help`" class="hint">{{ session.harness === 'claude' ? 'Queues your message for the next turn and interrupts the current one.' : 'Adds your message to the running turn.' }}</p>
      <div class="control-row"><button type="submit" class="btn sm primary" :disabled="!canSteer || waiting || !!unavailable">Send steer</button><button type="button" class="btn sm ghost" :disabled="waiting" @click="composing = false">Cancel</button></div>
    </form>
    <div v-if="!phone && confirmStop" class="stop-confirm"><span>End this session?</span><button type="button" class="btn sm" @click="submit('stop')">Confirm stop</button><button type="button" class="btn sm ghost" @click="confirmStop = false">Cancel</button></div>
    <!-- A modal sheet makes the page behind it inert, so a lost receipt stays in the sheet. -->
    <p v-if="feedback && !sheetOpen" class="feedback" role="status">{{ feedback }}</p>
    <p v-if="error && !sheetOpen" class="hint" role="alert">{{ error }}</p>
    <div v-if="uncertain && !sheetOpen" class="control-row"><button type="button" class="btn sm ghost" :disabled="busy" @click="check()">Check result</button><button v-if="request" type="button" class="btn sm ghost" :disabled="busy" @click="sendRequest">Retry same request</button></div>
    <details v-if="!phone"><summary><AppIcon name="chevron-right" :size="12" />Control limits</summary><p>Tools stay bound to this run and its budget. Other processes running as the same OS user are outside this isolation boundary.</p></details>
    <dialog v-if="phone" ref="sheetEl" class="steer-sheet" :aria-labelledby="`${uid}-sheet-title`" @cancel.prevent="closeSheet" @click="backdrop">
      <div class="sheet-card">
        <span class="grabber" aria-hidden="true" />
        <header class="sheet-head">
          <h2 :id="`${uid}-sheet-title`">{{ sheetTitle }}</h2>
          <button type="button" class="icon-btn flat" aria-label="Close" @click="closeSheet"><AppIcon name="close" :size="15" /></button>
        </header>
        <div class="sheet-body">
          <form v-if="confirmStop" class="sheet-form" @submit.prevent="submit('stop')">
            <p class="sheet-lead">End this session?</p>
            <div class="sheet-actions"><button type="submit" class="btn" :disabled="!!unavailable || waiting">Confirm stop</button><button type="button" class="btn ghost" :disabled="waiting" @click="closeSheet">Cancel</button></div>
          </form>
          <form v-else-if="editing" class="sheet-form" @submit.prevent="submit(editing)">
            <input v-if="editing === 'rename'" :id="`${uid}-setting`" v-model="settingValue" maxlength="128" :disabled="waiting" autocomplete="off" :aria-label="settingNames[editing]" />
            <select v-else :id="`${uid}-setting`" v-model="settingValue" :aria-label="settingNames[editing]" :disabled="waiting || loadingSettings || !settingChoices.length">
              <option value="" disabled>Choose {{ settingNames[editing].toLowerCase() }}</option>
              <option v-for="value in settingChoices" :key="value" :value="value">{{ value }}</option>
            </select>
            <p v-if="editing !== 'rename' && !loadingSettings && !settingChoices.length" class="hint">No compatible choices in this account’s catalog.</p>
            <div class="sheet-actions"><button type="submit" class="btn primary" :disabled="!validValue || waiting || loadingSettings || !!unavailable">Save {{ settingNames[editing].toLowerCase() }}</button><button type="button" class="btn ghost" :disabled="waiting" @click="closeSheet">Cancel</button></div>
          </form>
          <form v-else class="sheet-form" @submit.prevent="submit('steer')">
            <label :for="`${uid}-steer`">What should change?</label>
            <textarea :id="`${uid}-steer`" v-model="draft" rows="3" maxlength="8192" :disabled="waiting" :aria-describedby="`${uid}-help`" placeholder="Describe the next step…" />
            <p :id="`${uid}-help`" class="hint">{{ session.harness === 'claude' ? 'Queues your message for the next turn and interrupts the current one.' : 'Adds your message to the running turn.' }}</p>
            <div class="sheet-actions"><button type="submit" class="btn primary" :disabled="!canSteer || waiting || !!unavailable">Send steer</button><button type="button" class="btn ghost" :disabled="waiting" @click="closeSheet">Cancel</button></div>
          </form>
          <p v-if="sheetOpen && feedback" class="feedback" role="status">{{ feedback }}</p>
          <p v-if="sheetOpen && error" class="hint" role="alert">{{ error }}</p>
          <div v-if="sheetOpen && uncertain" class="sheet-actions"><button type="button" class="btn ghost" :disabled="busy" @click="check()">Check result</button><button v-if="request" type="button" class="btn ghost" :disabled="busy" @click="sendRequest">Retry same request</button></div>
        </div>
      </div>
    </dialog>
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
.steer-form,.sheet-form{display:grid;gap:8px}.steer-form label,.sheet-form label{font-size:12px;font-weight:600}
textarea,.sheet-form input,.sheet-form select{box-sizing:border-box;width:100%;border:1px solid var(--line);border-radius:var(--radius-row);background:var(--surface);color:var(--ink);font:inherit}
textarea{resize:vertical;min-height:78px;padding:10px}
.sheet-form input,.sheet-form select{min-width:0;padding:8px}
.hint,.feedback,details,.menu-note{font-size:12px;line-height:1.5;margin:0}.hint,details,.menu-note{color:var(--ink-3)}
.feedback{background:var(--surface-sunken);border-radius:var(--radius-row);padding:8px 10px;overflow-wrap:anywhere}
summary{cursor:pointer;width:fit-content;display:flex;align-items:center;gap:6px;list-style:none}summary::-webkit-details-marker{display:none}details[open] summary :deep(svg){transform:rotate(90deg)}details p{margin:6px 0 0}.stop-confirm{font-size:13px}
.overflow-menu :deep(.menu-item){display:flex;align-items:center;gap:10px;width:100%;min-height:44px;padding:8px 10px;border:0;border-radius:8px;background:transparent;color:var(--ink);font-size:13.5px;text-align:left}
.overflow-menu :deep(.menu-item > svg){color:var(--ink-2);flex-shrink:0}
.overflow-menu :deep(.menu-item:hover:not(:disabled)){background:var(--row-hover)}
.overflow-menu :deep(.menu-item:focus-visible){background:var(--row-selected);box-shadow:inset 0 0 0 1px var(--glass-rim)}
.overflow-menu :deep(.menu-item:disabled){color:var(--ink-3);cursor:not-allowed}
.overflow-menu :deep(.mi-text){display:grid;gap:2px;min-width:0}
.overflow-menu :deep(.mi-text small){overflow:hidden;text-overflow:ellipsis;white-space:nowrap;font-size:11.5px;color:var(--ink-3);line-height:1.35}
.menu-note{margin:4px 10px 6px;padding-top:8px;border-top:1px solid var(--line);overflow-wrap:anywhere}
.overflow-menu:not(:has([role="menuitem"])) .menu-note{border-top:0;padding-top:0}
.steer-sheet{position:fixed;inset:auto 0 calc(100dvh - var(--vv-top, 0px) - var(--vv-h, 100dvh)) 0;width:auto;max-width:none;height:auto;max-height:min(88dvh, var(--vv-h, 88dvh));margin:0;padding:0;border:0;background:transparent;color:var(--ink);overflow:visible}
.steer-sheet::backdrop{background:var(--scrim)}
.sheet-card{display:flex;flex-direction:column;max-height:min(88dvh, var(--vv-h, 88dvh));border-radius:20px 20px 0 0;border-top:1px solid var(--glass-edge);background:var(--surface-raised);box-shadow:0 -18px 40px -18px rgba(0, 0, 0, .35)}
.grabber{align-self:center;width:40px;height:4px;margin-top:8px;border-radius:999px;background:var(--line-2)}
.sheet-head{display:flex;align-items:center;justify-content:space-between;gap:8px;padding:4px 10px 4px 20px}
.sheet-head h2{font-size:17px}
.sheet-head .icon-btn{width:44px;height:44px}
.sheet-body{display:grid;gap:10px;padding:4px 16px calc(16px + env(safe-area-inset-bottom))}
.sheet-lead{margin:0;font-size:14px}
.sheet-actions{display:flex;gap:8px}
.sheet-actions .btn{flex:1;min-height:44px}
@media(max-width:720px){
  .managed-controls{gap:8px;padding-top:8px}
  .control-row{flex-wrap:nowrap}
  .control-row > .btn{flex:0 0 auto;min-width:44px;min-height:44px}
  .more{margin-left:auto;width:44px;height:44px}
}
@media(prefers-reduced-motion:no-preference){
  .steer-sheet[open] .sheet-card{animation:sheet-up .24s cubic-bezier(.2,.7,.2,1)}
  @keyframes sheet-up{from{transform:translateY(24px)}to{transform:none}}
}
</style>
