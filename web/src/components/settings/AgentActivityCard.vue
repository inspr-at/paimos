<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { onBeforeUnmount, reactive, watch } from 'vue'
import { api } from '../../lib/api'
import { brand } from '../../lib/brand'
import { can } from '../../lib/authz'
import { dismiss, toast } from '../../lib/toast'
import { useSession } from '../../stores/session'
import SettingsCard from './SettingsCard.vue'

const options = [
  { value: 'off', label: 'Off', detail: 'Collect and show no activity.' },
  { value: 'tool_activity', label: 'Tool activity', detail: 'Use tool calls automatically, with no summary tokens.' },
  { value: 'agent_summary', label: 'Agent summary', detail: 'A few words from the agent; use tool activity after ten minutes.' },
]
interface Field {
  path: string; key: string; label: string; min?: number; max?: number
  draft: string; confirmed: string | null; loading: boolean; saving: boolean
  error: string; conflict: boolean; loadError: boolean; saved: boolean; version: number
}
const field = (path: string, key: string, label: string, min?: number, max?: number): Field =>
  reactive({ path, key, label, min, max, draft: '', confirmed: null, loading: true, saving: false, error: '', conflict: false, loadError: false, saved: false, version: 0 })
const activity = field('/settings/agent-activity', 'mode', 'Agent activity')
const estimate = field('/settings/eta-interval', 'interval_minutes', 'Estimates', 1, 240)
const lost = field('/settings/heartbeat-lost', 'heartbeat_lost_minutes', 'Silent sessions', 5, 1440)
const numbers = [estimate, lost], fields = [activity, ...numbers]
const session = useSession()
const mac = /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent)
const owner = () => `${session.identity?.tenant.id ?? ''}/${session.identity?.principal.id ?? ''}`
let epoch = 0, disposed = false
const timers = new Set<ReturnType<typeof setTimeout>>()
const notices = new Set<number>()
const current = (started: number, identity: string) => !disposed && epoch === started && owner() === identity && can('settings.manage')
function valid(item: Field, value: unknown): boolean {
  if (item === activity) return options.some(option => option.value === value)
  return typeof value === 'number' && Number.isInteger(value) && value >= item.min! && value <= item.max!
}
function valueOf(item: Field, draft: string): string | number {
  return item === activity ? draft : draft.trim() === '' ? NaN : Number(draft)
}
async function read(item: Field) {
  const response = await api(item.path)
  if (!response.ok) throw new Error('load')
  const value: unknown = (await response.json())[item.key]
  if (!valid(item, value)) throw new Error('Invalid settings response')
  return String(value)
}
async function load(item: Field) {
  const started = epoch, identity = owner()
  item.loading = true; item.loadError = false; item.saved = false
  try {
    const value = await read(item)
    if (current(started, identity)) { item.draft = value; item.confirmed = value; item.error = ''; item.conflict = false; item.version++ }
  } catch { if (current(started, identity)) item.loadError = true }
  finally { if (current(started, identity)) item.loading = false }
}
function needsReload(item: Field): boolean {
  return item.conflict || item === activity && !!item.error
}
function keys(event: KeyboardEvent, item: Field) {
  if (event.isComposing || event.repeat) return
  if (event.key === 'Escape' && !event.metaKey && !event.ctrlKey && !event.altKey && !event.shiftKey) {
    event.preventDefault(); event.stopPropagation(); (event.target as HTMLInputElement).blur()
  } else if (event.key === 'Enter' && (mac ? event.metaKey && !event.ctrlKey : event.ctrlKey && !event.metaKey) && !event.altKey && !event.shiftKey) {
    event.preventDefault(); event.stopPropagation(); void save(item)
  }
}
async function save(item: Field, undo?: { version: number; before: string; after: string }) {
  if (!can('settings.manage') || disposed || item.saving || item.loading || item.conflict || item.confirmed === null) return
  if (undo && (item.version !== undo.version || item.confirmed !== undo.after || item.draft !== undo.after)) return
  const draft = undo ? undo.before : item.draft, value = valueOf(item, draft)
  item.saved = false
  if (!valid(item, value)) { item.error = `Use ${item.min} to ${item.max}`; return }
  if (!undo && String(value) === item.confirmed) { item.error = ''; return }
  const started = epoch, identity = owner(), before = item.confirmed
  item.saving = true; item.error = ''
  try {
    if (!current(started, identity)) return
    const response = await api(item.path, { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ [item.key]: value, [`expected_${item.key}`]: valueOf(item, before) }) })
    if (response.status === 409) {
      if (current(started, identity)) { item.conflict = true; item.error = 'Changed elsewhere. Reload to continue.' }
      return
    }
    if (!response.ok) throw new Error('save')
    const confirmed: unknown = (await response.json())[item.key]
    if (!valid(item, confirmed)) throw new Error('Invalid settings response')
    if (!current(started, identity)) return
    item.confirmed = item.draft = String(confirmed); item.version++; item.saved = true
    const version = item.version, after = item.confirmed
    const timer = setTimeout(() => { timers.delete(timer); if (current(started, identity) && item.version === version) item.saved = false }, 2000)
    timers.add(timer)
    const expires = Date.now() + 10_000
    const notice = toast(`${item.label} ${undo ? 'restored' : 'saved'}.`, {
      key: `while-agents-${item.key}`, timeout: 10_000,
      ...(!undo ? { action: { label: 'Undo', run: () => {
        if (!current(started, identity) || Date.now() > expires) return
        dismiss(notice); void save(item, { version, before, after })
      } } } : {}),
    })
    notices.add(notice)
  } catch {
    if (current(started, identity)) {
      item.error = 'Could not save. Try again.'
    }
  } finally { if (current(started, identity)) item.saving = false }
}
watch(owner, () => {
  epoch++
  for (const notice of notices) dismiss(notice)
  notices.clear()
  for (const item of fields) {
    item.draft = ''; item.confirmed = null; item.error = ''; item.conflict = false; item.saving = false; item.saved = false; item.version++
    void load(item)
  }
}, { immediate: true })
onBeforeUnmount(() => {
  disposed = true; epoch++
  for (const timer of timers) clearTimeout(timer)
  for (const notice of notices) dismiss(notice)
})
</script>

<template>
  <SettingsCard title="While agents work" icon="agent" anchor="while-agents-work">
    <template #lead>What agents show, how often they estimate, and when a silent session counts as lost. Applies to every project.</template>
    <div class="fields" :aria-busy="fields.some(item => item.loading)">
      <div id="agent-activity" class="frow">
        <div id="activity-label" class="flabel">Agent activity<small>What agents show while they work.</small></div>
        <div class="fbody">
          <fieldset class="opts" :aria-invalid="!!activity.error" aria-describedby="mode-feedback" role="radiogroup" aria-labelledby="activity-label" :disabled="activity.loading || activity.saving || activity.loadError || !can('settings.manage')">
            <label v-for="option in options" :key="option.value" class="opt">
              <input v-model="activity.draft" type="radio" name="agent-activity" :value="option.value" @change="save(activity)" />
              <span><strong>{{ option.label }}</strong><small>{{ option.detail }}</small></span>
            </label>
          </fieldset>
        </div>
      </div>
      <div v-for="item in numbers" :id="item === estimate ? 'estimates' : 'silent-sessions'" :key="item.key" class="frow">
        <label class="flabel" :for="item.key">{{ item.label }}<small>{{ item === estimate ? 'How often a working agent reports when a ticket will be ready, and when it will be live.' : `A session running outside ${brand.short_name} that stops reporting is marked Lost contact. Its next heartbeat brings it back.` }}</small></label>
        <div class="fbody">
          <input :id="item.key" :value="item.draft" class="num" @input="item.draft = ($event.target as HTMLInputElement).value" type="number" :min="item.min" :max="item.max" step="1" inputmode="numeric" :disabled="item.loading || item.saving || item.loadError || !can('settings.manage')" :aria-invalid="!!item.error" :aria-describedby="`${item.key}-feedback`" :aria-keyshortcuts="mac ? 'Meta+Enter' : 'Control+Enter'" @blur="save(item)" @keydown="keys($event, item)" />
          <span class="unit">{{ item === estimate ? 'minutes between estimates' : 'minutes without a heartbeat' }}</span>
          <p :id="`${item.key}-feedback`" class="number-feedback" :class="{ error: item.error }" :role="item.error ? 'alert' : 'status'">{{ item.error }}</p>
        </div>
      </div>
    </div>
    <div class="feedback-line">
      <p id="mode-feedback" :role="activity.error || fields.some(item => item.loadError) ? 'alert' : 'status'" :class="{ error: activity.error || fields.some(item => item.loadError) }"><template v-if="activity.error">Agent activity: {{ activity.error }} </template><template v-if="fields.some(item => item.loadError)">Couldn't load this.</template><template v-else-if="!fields.some(item => item.error) && fields.some(item => item.saved)">Saved</template><template v-else-if="!fields.some(item => item.error) && fields.some(item => item.loading)">Loading agent settings…</template></p>
      <button v-if="fields.some(item => item.loadError)" type="button" class="btn sm" @click="fields.filter(item => item.loadError).forEach(item => load(item))">Try again</button>
      <button v-else-if="fields.some(needsReload)" type="button" class="btn sm" :disabled="fields.some(item => item.loading || item.saving)" @click="fields.filter(needsReload).forEach(item => load(item))">Reload</button>
    </div>
  </SettingsCard>
</template>

<style scoped>
.fields { display: grid; }
.frow { display: grid; grid-template-columns: minmax(0, 1fr) minmax(0, 1.5fr); gap: 6px 20px; padding: 14px 0; border-top: 1px solid var(--line); }
.frow:first-child { border-top: 0; padding-top: 0; }
.frow:last-child { padding-bottom: 0; }
.flabel { font-size: 13px; font-weight: 600; color: var(--ink); }
.flabel small { display: block; margin-top: 2px; font-weight: 400; font-size: 12.5px; line-height: 1.45; color: var(--ink-2); }
.fbody { display: flex; flex-wrap: wrap; align-items: center; gap: 8px 10px; min-width: 0; }
.unit { font-size: 13px; color: var(--ink-2); }
.opts[aria-invalid="true"] { box-shadow: 0 0 0 1px var(--danger); border-radius: 10px; }
.opts { display: grid; gap: 4px; width: 100%; border: 0; margin: 0; padding: 0; }
.opt { display: grid; grid-template-columns: 20px minmax(0, 1fr); gap: 10px; align-items: start; padding: 8px 10px; border-radius: 10px; cursor: pointer; }
.opt:hover { background: var(--row-hover); }
.opt:has(input:checked) { background: var(--row-selected); }
.opt input { margin: 3px 0 0; accent-color: var(--teal); width: 16px; height: 16px; }
.opt strong { display: block; font-size: 13px; font-weight: 600; }
.opt small { display: block; font-size: 12.5px; color: var(--ink-2); }
.num { width: 7ch; min-height: 34px; padding: 4px 10px; border: 1px solid var(--line-2); border-radius: 8px; background: var(--surface); color: var(--ink); font: inherit; font-variant-numeric: tabular-nums; text-align: right; }
.num[aria-invalid="true"] { border-color: var(--danger); box-shadow: 0 0 0 1px var(--danger); }
/* Keep keyboard focus visible without the global aqua glow. Outlines do not
   change control geometry; invalid numbers retain their red border. */
.num:focus-visible, .opt input:focus-visible, .feedback-line .btn:focus-visible { outline: 2px solid var(--ink-2); outline-offset: 2px; box-shadow: none; }
.feedback-line { display: flex; align-items: center; gap: 8px; min-height: 44px; font-size: 12.5px; color: var(--ink-2); }
.error { color: var(--danger); }
.number-feedback { flex-basis: 100%; min-height: 1.5em; font-size: 12.5px; line-height: 1.5; }
@media (max-width: 720px) {
  .frow { grid-template-columns: minmax(0, 1fr); }
  .opt, .num { min-height: 44px; }
}
@media (pointer: coarse) { .opt, .num { min-height: 44px; } }
</style>
