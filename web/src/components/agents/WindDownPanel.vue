<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onMounted, ref, shallowRef, watch } from 'vue'
import { listHostLabels, type HarnessSession } from '../../lib/agents'
import { useAgents } from '../../stores/agents'
import { useCapacity } from '../../stores/capacity'
import { useAgentPause } from '../../stores/agentPause'
import { useSession } from '../../stores/session'
import { clockTime, liveSession, pausedSession, prediction, predictWindDown, selectedAgent, levelName, type WindDownScope } from '../../lib/agentPause'
import { readPreference, writePreference } from '../../lib/preferences'
import WindDownPicker from './WindDownPicker.vue'
import type { WindDownHost } from './WindDownPicker.vue'
import AppIcon from '../AppIcon.vue'
import KeyCap from '../KeyCap.vue'
const agents = useAgents(), capacity = useCapacity(), pause = useAgentPause(), session = useSession()
const mac = /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent)
const root = ref<HTMLElement>(), switchEl = ref<HTMLInputElement>(), scopeButton = ref<HTMLElement>(), pickerAnchor = shallowRef<HTMLElement | null>(null)
const pickerHosts = ref<WindDownHost[]>([]), pickerSessions = shallowRef<HarnessSession[]>([])
const armed = ref(false), minutes = ref('15'), custom = ref(15), scope = ref<WindDownScope>({ hosts: 'all' }), labels = ref<Record<string, string>>({}), labelsError = ref('')
let scopeEpoch = 0
function setScope(value: WindDownScope) { scopeEpoch++; scope.value = value }
async function loadScope() {
  const turn = ++scopeEpoch
  if (!pause.person) return
  const saved = await readPreference('agents.wind-down-scope')
  const strings = (value: unknown): value is string[] => Array.isArray(value) && value.length <= 200 && value.every(item => typeof item === 'string')
  if (turn === scopeEpoch && !armed.value && !active.value && saved && (saved.hosts === 'all' || strings(saved.hosts)) && (saved.agents === undefined || strings(saved.agents))) scope.value = { hosts: saved.hosts, ...(saved.agents ? { agents: saved.agents as string[] } : {}) }
}
const amount = computed(() => minutes.value === 'custom' ? custom.value : Number(minutes.value))
const validTime = computed(() => Number.isInteger(amount.value) && amount.value >= 1 && amount.value <= 1440)
const deadline = computed(() => agents.now + (validTime.value ? amount.value : 15) * 60_000)
const active = computed(() => !!pause.report?.deadline_at)
const ownershipReady = computed(() => pause.report !== null && !pause.leavingError)
const visible = computed(() => pause.person && (agents.sessions.some(pause.permitted) || active.value))
const controlRows = computed(() => agents.sessions.filter(s => !s.archived_at && pause.permitted(s)))
const rows = computed(() => controlRows.value.filter(pause.windDownPermitted))
const running = computed(() => rows.value.filter(liveSession))
const selectedScope = computed<WindDownScope>(() => active.value && pause.report ? { hosts: pause.report.hosts ?? 'all', ...(pause.report.agents ? { agents: pause.report.agents } : {}) } : scope.value)
const hosts = computed<WindDownHost[]>(() => {
  const computers = capacity.computers.filter(c => !c.archived_at && c.computer_state !== 'revoked')
  const ids = [...new Set([...rows.value.map(s => s.host), ...computers.map(c => c.computer_name)])]
  return ids.map(id => {
    const computer = computers.find(c => c.computer_name === id), count = running.value.filter(s => s.host === id).length, paused = rows.value.filter(s => s.host === id && pausedSession(s)).length
    const online = computer?.connectivity === 'online'
    return { id, label: labels.value[id] || id, online, meta: [computer?.platform || '', paused ? `${paused} paused` : '', !count ? 'nothing running' : '', !online && computer?.last_seen_at ? `last seen ${new Date(computer.last_seen_at).toLocaleDateString()}` : ''].filter(Boolean).join(' · ') }
  }).sort((a, b) => {
    const n = (id: string) => running.value.filter(s => s.host === id).length
    const rank = (h: WindDownHost) => n(h.id) ? 0 : h.online ? 1 : 2
    return rank(a) - rank(b) || n(b.id) - n(a.id) || a.label.localeCompare(b.label)
  })
})
const scopeText = computed(() => {
  const s = selectedScope.value
  if (s.hosts === 'all') return 'all hosts'
  const picked = running.value.filter(row => selectedAgent(row, s)), touched = [...new Set([...s.hosts, ...picked.map(row => row.host)])]
  if (!touched.length) return 'choose agents'
  const partial = touched.some(id => running.value.some(row => row.host === id && !selectedAgent(row, s)))
  const hostLabel = touched.length === 1 ? hosts.value.find(h => h.id === touched[0])?.label || touched[0]! : `${touched.length} hosts`
  return partial ? `${picked.length} agent${picked.length === 1 ? '' : 's'} on ${hostLabel}` : hostLabel
})
const chosen = computed(() => running.value.filter(s => selectedAgent(s, scope.value)))
const allEmpty = computed(() => scope.value.hosts !== 'all' && !scope.value.hosts.length && !scope.value.agents?.length)
const tooMany = computed(() => chosen.value.length > 200 || (scope.value.hosts !== 'all' && scope.value.hosts.length > 200) || (scope.value.agents?.length ?? 0) > 200)
const planRows = computed(() => active.value ? pause.reportIds.map(id => agents.sessionById(id)).filter((s): s is HarnessSession => !!s) : chosen.value)
const untouched = computed(() => running.value.filter(s => active.value ? !pause.reportIds.includes(s.id) : !selectedAgent(s, scope.value)))
const pauseable = computed(() => controlRows.value.filter(s => pause.eligible(s, 'pause-all') && (!active.value || !pause.reportIds.includes(s.id))))
const resumable = computed(() => controlRows.value.filter(s => pause.eligible(s, 'resume-all')))
const startedAt = computed(() => { const times = planRows.value.map(s => Date.parse(s.pause?.requested_at || '')).filter(Number.isFinite); return times.length ? clockTime(Math.min(...times)) : 'Unknown' })
const countLeft = computed(() => Math.max(0, Math.ceil((Date.parse(pause.report?.deadline_at ?? '') - agents.now) / 60_000)))
const previewSummary = computed(() => {
  const levels = chosen.value.map(s => predictWindDown(s, agents.now, deadline.value, pause.interval).level)
  return `${levels.filter(l => l === 'pause' || l === 'pause_quickly').length} hand over · ${levels.filter(l => l === 'wrap_up').length} finish · ${levels.filter(l => l === 'stop_now').length} stop, no handover`
})
function openPicker() {
  if (!scopeButton.value) return
  pickerHosts.value = hosts.value.map(h => ({ ...h })); pickerSessions.value = [...rows.value]; pickerAnchor.value = scopeButton.value
}
function openBulk(mode: 'pause-all' | 'resume-all', event?: Event) { pause.open(mode, mode === 'pause-all' ? pauseable.value : resumable.value, event?.currentTarget instanceof HTMLElement ? event.currentTarget : null) }
function focusWindDown() { root.value?.scrollIntoView({ block: 'nearest' }); switchEl.value?.focus({ preventScroll: true }); if (!active.value) armed.value = true }
function toggle() { if (active.value) void pause.cancelWindDown(); else armed.value = !armed.value }
async function confirm() {
  if (!ownershipReady.value || !validTime.value || allEmpty.value || tooMany.value) return
  const turn = scopeEpoch, owner = `${session.identity?.tenant.id}:${session.identity?.principal.id}`
  const captured = { hosts: scope.value.hosts === 'all' ? 'all' as const : [...scope.value.hosts], ...(scope.value.agents ? { agents: [...scope.value.agents] } : {}) }
  const accepted = await pause.windDown(new Date(deadline.value).toISOString(), captured)
  if (accepted && `${session.identity?.tenant.id}:${session.identity?.principal.id}` === owner && turn === scopeEpoch) { armed.value = false; void writePreference('agents.wind-down-scope', captured) }
}
function line(s: HarnessSession) {
  if (active.value && s.pause) {
    if (!liveSession(s)) return s.pause.state === 'paused' ? 'Paused · handover saved' : s.finished ? 'Finished' : s.stop_reason === 'heartbeat_lost' ? 'Lost contact · exit unconfirmed' : 'Ended'
    if (s.pause.stop_requested) return 'Stop requested · exit unconfirmed'
    if (agents.now >= Date.parse(s.pause.deadline_at)) return 'Deadline reached · awaiting outcome'
    return `${levelName(s.pause.level)}${s.pause.deliver === false && s.pause.starts_at ? ` starts ${clockTime(s.pause.starts_at)}` : ''} · by ${clockTime(s.pause.deadline_at)}`
  }
  const p = predictWindDown(s, agents.now, deadline.value, pause.interval), own = prediction(s, p.level, p.starts, pause.interval)
  return `${levelName(p.level)}${p.starts > agents.now ? ` at ${clockTime(p.starts)}` : ' now'} · ${p.level === 'stop_now' ? `by ${clockTime(deadline.value)} · no handover` : `${own.time} · ${own.outcome}`}`
}
function keydown(event: KeyboardEvent) {
  if (event.key === 'Enter' && !event.altKey && !event.shiftKey && (mac ? event.metaKey && !event.ctrlKey : event.ctrlKey && !event.metaKey) && armed.value && !active.value) { event.preventDefault(); void confirm(); return }
  if (event.key === 'Escape' && armed.value) { event.preventDefault(); event.stopPropagation(); if (event.target instanceof HTMLElement && ['INPUT', 'SELECT'].includes(event.target.tagName)) { event.target.blur(); root.value?.focus({ preventScroll: true }) } else { armed.value = false; switchEl.value?.focus() } }
}
let labelsEpoch = 0
async function loadLabels() {
  const turn = ++labelsEpoch
  labels.value = {}; labelsError.value = ''
  try { const read = await listHostLabels(); if (turn === labelsEpoch) labels.value = Object.fromEntries(read.map(row => [row.host, row.label])) }
  catch { if (turn === labelsEpoch) labelsError.value = 'Personal host names could not be loaded; registered names are shown.' }
}
watch(() => `${session.identity?.tenant.id}:${session.identity?.principal.id}`, () => { labelsEpoch++; scopeEpoch++; armed.value = false; pickerAnchor.value = null; scope.value = { hosts: 'all' }; labels.value = {}; labelsError.value = ''; if (pause.person) { void loadLabels(); void loadScope() } }, { flush: 'sync' })
onMounted(() => { void loadLabels(); void loadScope(); void pause.loadSettings(); void pause.refreshLeaving() })
defineExpose({ openBulk, focusWindDown })
</script>
<template>
  <section v-if="visible" ref="root" tabindex="-1" class="wind-down" :class="{ armed, active }" aria-label="Wind down agents" @keydown="keydown">
    <div class="wind-row">
      <span class="switch"><input ref="switchEl" type="checkbox" role="switch" aria-label="Wind down" :checked="active || armed" :disabled="pause.leavingBusy" @change="toggle" /><span class="knob" /></span>
      <span class="wind-label">Wind down</span>
      <button ref="scopeButton" type="button" class="scope-button" aria-haspopup="dialog" :aria-expanded="!!pickerAnchor" :title="scopeText" @click="openPicker"><span>{{ scopeText }}</span><AppIcon name="chevron" :size="12" /></button>
      <span class="wind-timing"><span class="when">{{ active ? 'started' : 'now · done in' }}</span><span class="time-choice"><select :value="active ? 'started' : minutes" aria-label="Wind-down time" :disabled="active || pause.leavingBusy" @change="minutes = ($event.target as HTMLSelectElement).value"><option v-if="active" value="started">{{ startedAt }}</option><option v-for="n in [5, 10, 15, 30, 60]" :key="n" :value="String(n)">{{ n }} min · by {{ clockTime(agents.now + n * 60000) }}</option><option value="custom">Custom…</option></select><span class="time-value" aria-hidden="true">{{ active ? startedAt : minutes === 'custom' ? 'Custom…' : `${minutes} min` }}</span><AppIcon class="time-chevron" name="chevron" :size="12" /></span><input v-model.number="custom" type="number" min="1" max="1440" aria-label="Custom minutes" :class="{ concealed: minutes !== 'custom' }" :disabled="minutes !== 'custom' || active || pause.leavingBusy" /><span class="by">{{ active ? 'done by' : 'by' }} {{ clockTime(active ? pause.report!.deadline_at! : deadline) }}</span></span>
      <span class="bulk-tools"><button type="button" class="btn sm ghost" :disabled="!pauseable.length || pause.leavingBusy" :title="pauseable.length ? 'Pause agents outside the wind-down' : 'Nothing outside the wind-down can pause right now'" @click="openBulk('pause-all', $event)"><AppIcon name="pause" :size="13" />Pause all…</button><button type="button" class="btn sm ghost" :disabled="!resumable.length || pause.leavingBusy" title="Continue paused agents from their handovers" @click="openBulk('resume-all', $event)"><AppIcon name="play" :size="13" />Resume all…</button></span>
    </div>
    <template v-if="armed && !active">
      <div class="plan-toolbar"><button type="button" class="btn primary wind-confirm" :disabled="!ownershipReady || allEmpty || tooMany || !validTime || pause.leavingBusy" @click="confirm"><span class="desktop-copy">Wind down now · </span><span class="phone-copy">Start · </span>done by {{ clockTime(deadline) }}<span class="submit-keys"><KeyCap k="mod" /><KeyCap k="enter" /></span></button><button type="button" class="btn ghost" @click="armed = false">Cancel<kbd class="keycap">Esc</kbd></button></div>
      <p class="plan-summary">{{ !ownershipReady ? 'Reading owned sessions…' : allEmpty ? 'Choose a host or an agent first.' : tooMany ? 'Select at most 200 agents and 200 hosts at once.' : previewSummary }}</p>
      <p class="plan-note">Preview from the latest reports; eligibility and timing are checked on confirm. New agents can still start on selected hosts.</p>
    </template>
    <div v-if="active" class="plan-toolbar"><strong>{{ !pause.leavingError && pause.settled ? pause.completedAt === null ? 'Wind-down done' : `Wind-down done at ${clockTime(pause.completedAt)}` : `Done by ${clockTime(pause.report!.deadline_at!)} · ${countLeft} min left` }}</strong><button v-if="!pause.leavingError && pause.settled" type="button" class="btn sm ghost" :disabled="pause.leavingBusy" @click="pause.dismissReport()">Dismiss</button><span v-else-if="countLeft === 0">Deadline reached · awaiting reported outcomes</span></div>
    <div v-if="armed || active" class="wind-plan">
      <section v-for="host in hosts.filter(h => planRows.some(s => s.host === h.id) || selectedScope.hosts === 'all' || selectedScope.hosts.includes(h.id))" :key="host.id" class="plan-host"><h3>{{ host.label }}<small>{{ selectedScope.hosts === 'all' || selectedScope.hosts.includes(host.id) ? 'new starts remain allowed' : 'new starts allowed' }}</small></h3><p v-if="!planRows.some(s => s.host === host.id)" class="plan-note">Nothing running</p><div v-for="s in planRows.filter(row => row.host === host.id)" :key="s.id" class="plan-agent"><strong>{{ s.display_label || s.agent?.name || s.host }}</strong><span>{{ line(s) }}</span></div></section>
      <section v-if="untouched.length" class="untouched"><h3>Keep running, untouched</h3><p v-for="s in untouched" :key="s.id">{{ s.display_label || s.agent?.name || s.host }} · {{ labels[s.host] || s.host }}</p></section>
    </div>
    <p v-if="pause.leavingError" class="plan-error" role="alert">{{ pause.leavingError }} <button class="btn sm ghost" type="button" @click="pause.refreshLeaving()">Retry read</button></p><p v-if="labelsError && (armed || pickerAnchor)" class="plan-note" role="status">{{ labelsError }}</p>
    <WindDownPicker v-if="pickerAnchor" :anchor="pickerAnchor" :hosts="pickerHosts" :sessions="pickerSessions" :scope="selectedScope" :readonly="active" @change="setScope" @close="pickerAnchor = null" />
  </section>
</template>
<style scoped>
.submit-keys{display:inline-flex;gap:2px;align-items:center;flex:none}

.wind-down{padding:12px 0;border-block:1px solid var(--line);min-width:0}.wind-down.armed,.wind-down.active{background:color-mix(in srgb,var(--aqua-3) 32%,transparent)}.wind-row{display:grid;grid-template-columns:34px 70px 140px minmax(0,1fr) auto;align-items:center;gap:8px;min-height:40px}.switch{position:relative;display:inline-block;flex:none;width:34px;height:20px}.switch input{-webkit-appearance:none;appearance:none;position:absolute;inset:0;width:100%;height:100%;margin:0;border:0;opacity:0;cursor:pointer}.switch .knob{position:absolute;left:50%;top:50%;transform:translate(-50%,-50%);width:34px;height:20px;border-radius:999px;background:var(--track);box-shadow:inset 0 0 0 1px var(--line-2);pointer-events:none}.switch .knob::before{content:"";position:absolute;left:3px;top:3px;width:14px;height:14px;border-radius:50%;background:#fbfaf6;box-shadow:0 1px 2px #0004}.switch input:checked+.knob{background:var(--teal)}.switch input:checked+.knob::before{transform:translateX(14px)}.switch input:focus-visible+.knob{box-shadow:var(--focus-ring)}.switch input:disabled+.knob{opacity:.5}.wind-label{font-size:13px;font-weight:600;white-space:nowrap}.scope-button{width:140px;height:32px;display:flex;gap:6px;align-items:center;justify-content:space-between;padding:0 8px;background:transparent;border:1px solid var(--line-2);border-radius:6px;font-size:12px;color:var(--teal-ink)}.scope-button>span{overflow:hidden;text-overflow:ellipsis;white-space:nowrap;min-width:0}.wind-timing{display:grid;grid-template-columns:88px 82px 44px 104px;align-items:center;gap:6px;font-size:12px;color:var(--ink-2);white-space:nowrap}.wind-timing select,.wind-timing input{height:32px;min-width:0;width:100%;background:var(--field-bg);border:1px solid var(--line);border-radius:5px;color:var(--ink);font-size:12px}.wind-timing input{padding:0 4px}.time-choice{position:relative;min-width:0}.time-choice select{appearance:none;color:transparent}.time-choice option{color:var(--ink);background:var(--field-bg)}.time-value{position:absolute;inset:0 18px 0 8px;display:flex;align-items:center;color:var(--ink);pointer-events:none;overflow:hidden}.time-chevron{position:absolute;right:6px;top:50%;transform:translateY(-50%);color:var(--ink-2);pointer-events:none}.concealed{visibility:hidden}.by{font-variant-numeric:tabular-nums}.bulk-tools{display:flex;align-items:center;gap:2px;border-left:1px solid var(--line);padding-left:8px}.bulk-tools .btn{color:var(--ink-2);font-size:12px;padding-inline:7px}.plan-toolbar{display:flex;align-items:center;gap:8px;min-height:56px;padding-top:10px}.wind-confirm{width:350px;font-size:12px}.plan-toolbar>strong{font-size:13px}.plan-toolbar>span{font-size:12px;color:var(--ink-3)}.plan-summary{font-size:12px;margin:8px 0}.plan-note{font-size:12px;color:var(--ink-3);margin:8px 0}.wind-plan{padding-top:8px;display:grid;gap:14px}.plan-host h3,.untouched h3{display:flex;align-items:baseline;gap:10px;flex-wrap:wrap;font-size:13px;font-weight:650}.plan-host h3 small{color:var(--ink-3);font-size:11px;font-weight:400}.plan-agent{display:grid;grid-template-columns:minmax(0,1fr) minmax(0,1.5fr);gap:12px;align-items:center;height:48px;border-bottom:1px solid var(--line);font-size:12px}.plan-agent strong{overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.plan-agent>span{color:var(--ink-2);font-variant-numeric:tabular-nums}.untouched p{font-size:12px;margin:6px 0;color:var(--ink-3)}.plan-error{font-size:12px;color:var(--danger);overflow-wrap:anywhere}.phone-copy{display:none}
@media(max-width:1200px){.wind-row{grid-template-columns:34px 70px 140px minmax(0,1fr);row-gap:4px}.bulk-tools{grid-column:1/-1;grid-row:1;border-left:0;padding:0;justify-content:flex-end}.switch,.wind-label,.scope-button,.wind-timing{grid-row:2}}
@media(max-width:720px){.wind-row{grid-template-columns:44px 68px minmax(0,1fr);gap:4px 8px}.switch{width:44px;height:44px}.bulk-tools{justify-content:flex-start;height:44px}.bulk-tools .btn{min-height:44px}.scope-button{width:100%;height:36px}.wind-timing{grid-row:3;grid-column:1/-1;grid-template-columns:92px 86px 48px minmax(0,1fr);height:44px}.wind-timing select,.wind-timing input{height:36px}.plan-toolbar{min-height:62px}.plan-toolbar .btn{min-height:44px}.wind-confirm{width:270px;min-width:0;padding:0 10px;white-space:nowrap}.desktop-copy{display:none}.phone-copy{display:inline}.plan-agent{height:62px;grid-template-columns:minmax(0,1fr) minmax(0,1.2fr);gap:6px}.plan-host h3{display:grid;gap:4px}.plan-toolbar>span{display:none}}
</style>
