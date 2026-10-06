<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onMounted, ref, watch } from 'vue'
import { listHostLabels, type HarnessSession } from '../../lib/agents'
import { useAgents } from '../../stores/agents'
import { useCapacity } from '../../stores/capacity'
import { useAgentPause } from '../../stores/agentPause'
import { useSession } from '../../stores/session'
import { clockTime, liveSession, normalizeScope, pausedSession, prediction, predictWindDown, selectedAgent, levelName, windDownResult, type WindDownScope } from '../../lib/agentPause'
import { readPreference, writePreference } from '../../lib/preferences'
import FloatingPanel from '../work/FloatingPanel.vue'
import AppIcon from '../AppIcon.vue'
import KeyCap from '../KeyCap.vue'

// Wind down as a head control (AEON-783, design AEON-721 d3): a ghost button at
// rest with a popover form, a teal chip while a wind-down runs, and a status
// popover with the per-computer plan. Pause all… / Resume all… live under "Right now".
const agents = useAgents(), capacity = useCapacity(), pause = useAgentPause(), session = useSession()
const mac = /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent)
const PRESETS = [5, 15, 30, 60] as const
const MIN_AHEAD = 2, MAX_AHEAD = 720
const restButton = ref<HTMLElement>(), chipButton = ref<HTMLElement>()
const panel = ref<'form' | 'status' | null>(null)
const labels = ref<Record<string, string>>({}), labelsError = ref('')
// What the person last chose, remembered per person.
const scope = ref<WindDownScope>({ hosts: 'all' }), savedMinutes = ref<number>(15)
// The form's own draft.
const where = ref('all'), picked = ref<string[]>([]), mins = ref<number | null>(15), typed = ref(''), submitted = ref(false)
let scopeEpoch = 0
const hhmm = (at: number) => { const d = new Date(at); return `${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}` }
const minuteOf = (at: number) => Math.floor(at / 60_000) * 60_000
// "19:44" or "19.44": the next time the clock shows it, in minutes ahead of now.
function parseBy(text: string, now: number): { at: number; ahead: number } | null {
  const m = /^\s*(\d{1,2})[:.](\d{2})\s*$/.exec(text)
  if (!m) return null
  const hours = Number(m[1]), minutes = Number(m[2])
  if (hours > 23 || minutes > 59) return null
  const d = new Date(now); d.setHours(hours, minutes, 0, 0)
  if (d.getTime() <= minuteOf(now)) d.setDate(d.getDate() + 1)
  return { at: d.getTime(), ahead: Math.round((d.getTime() - minuteOf(now)) / 60_000) }
}
async function loadScope() {
  const turn = ++scopeEpoch
  if (!pause.person) return
  const saved = await readPreference('agents.wind-down-scope')
  const strings = (value: unknown): value is string[] => Array.isArray(value) && value.length <= 200 && value.every(item => typeof item === 'string')
  if (turn !== scopeEpoch || !saved) return
  if (saved.hosts === 'all' || strings(saved.hosts)) if (saved.agents === undefined || strings(saved.agents)) scope.value = { hosts: saved.hosts as WindDownScope['hosts'], ...(saved.agents ? { agents: saved.agents as string[] } : {}) }
  if (typeof saved.minutes === 'number' && (PRESETS as readonly number[]).includes(saved.minutes)) savedMinutes.value = saved.minutes
}
const active = computed(() => !!pause.report?.deadline_at)
const ownershipReady = computed(() => pause.report !== null && !pause.leavingError)
const visible = computed(() => pause.person && (agents.sessions.some(pause.permitted) || active.value))
const controlRows = computed(() => agents.sessions.filter(s => !s.archived_at && pause.permitted(s)))
const rows = computed(() => controlRows.value.filter(pause.windDownPermitted))
const running = computed(() => rows.value.filter(liveSession))
const hosts = computed(() => {
  const computers = capacity.computers.filter(c => !c.archived_at && c.computer_state !== 'revoked')
  const ids = [...new Set([...rows.value.map(s => s.host), ...computers.map(c => c.computer_name)])]
  const count = (id: string) => running.value.filter(s => s.host === id).length
  const online = (id: string) => computers.find(c => c.computer_name === id)?.connectivity === 'online'
  return ids.map(id => ({ id, label: labels.value[id] || id, count: count(id), paused: rows.value.filter(s => s.host === id && pausedSession(s)).length }))
    .sort((a, b) => Number(!!b.count) - Number(!!a.count) || Number(online(b.id)) - Number(online(a.id)) || b.count - a.count || a.label.localeCompare(b.label))
})
const hostIds = computed(() => hosts.value.map(h => h.id))
const hostLabel = (id: string) => hosts.value.find(h => h.id === id)?.label || labels.value[id] || id
const name = (s: HarnessSession) => s.display_label || s.agent?.name || s.host
// ---------- The form ----------
const formScope = computed<WindDownScope>(() => {
  if (where.value === 'all') return { hosts: 'all' }
  if (where.value === 'pick') return normalizeScope({ hosts: [], agents: picked.value.filter(id => running.value.some(s => s.id === id)) }, rows.value, hostIds.value)
  return normalizeScope({ hosts: [where.value.slice(2)] }, rows.value, hostIds.value)
})
const chosen = computed(() => running.value.filter(s => selectedAgent(s, formScope.value)))
const whereOptions = computed(() => [
  { value: 'all', label: `All computers · ${running.value.length} agent${running.value.length === 1 ? '' : 's'}` },
  ...(hosts.value.length > 1 ? hosts.value.map(h => ({ value: `h:${h.id}`, label: `${h.label} · ${h.count} agent${h.count === 1 ? '' : 's'}` })) : []),
  { value: 'pick', label: 'Chosen agents…' },
])
const byText = computed(() => mins.value === null ? typed.value : hhmm(agents.now + mins.value * 60_000))
const parsed = computed(() => mins.value !== null ? { at: agents.now + mins.value * 60_000, ahead: mins.value } : parseBy(typed.value, agents.now))
const timeError = computed(() => {
  if (mins.value !== null) return ''
  const complete = /^\s*\d{1,2}[:.]\d{2}\s*$/.test(typed.value)
  if (!submitted.value && !complete) return ''
  const p = parseBy(typed.value, agents.now)
  if (!p) return 'Type a time like 19:44.'
  return p.ahead < MIN_AHEAD || p.ahead > MAX_AHEAD ? `Pick a time between ${hhmm(minuteOf(agents.now) + MIN_AHEAD * 60_000)} and ${hhmm(minuteOf(agents.now) + MAX_AHEAD * 60_000)}.` : ''
})
const scopeError = computed(() => {
  if (!submitted.value) return ''
  if (!chosen.value.length) return where.value === 'pick' ? 'Choose at least one agent.' : where.value === 'all' ? 'Nothing is running.' : 'Nothing is running on that computer.'
  if (chosen.value.length > 200) return 'Select at most 200 agents at once.'
  return ''
})
const error = computed(() => timeError.value || scopeError.value)
const deadline = computed(() => parsed.value && parsed.value.ahead >= MIN_AHEAD && parsed.value.ahead <= MAX_AHEAD ? parsed.value.at : agents.now + 15 * 60_000)
// The button always carries a time of the same width, so it never moves while typing.
const goTime = computed(() => hhmm(timeError.value || !parsed.value ? 0 : parsed.value.at))
const aheadNote = computed(() => parsed.value && !timeError.value ? `in ${parsed.value.ahead} min` : 'hh:mm')
const wherePhrase = computed(() => where.value === 'all' ? 'on all computers' : where.value === 'pick' ? 'you chose' : `on ${hostLabel(where.value.slice(2))}`)
const previewLevels = computed(() => {
  const levels = chosen.value.map(s => predictWindDown(s, agents.now, deadline.value, pause.interval).level)
  return `${levels.filter(l => l === 'pause' || l === 'pause_quickly').length} hand over · ${levels.filter(l => l === 'wrap_up').length} finish · ${levels.filter(l => l === 'stop_now').length} stop, no handover`
})
const canPause = computed(() => controlRows.value.filter(s => pause.eligible(s, 'pause-all') && (!active.value || !pause.reportIds.includes(s.id))))
const canResume = computed(() => controlRows.value.filter(s => pause.eligible(s, 'resume-all')))
function whereOf(s: WindDownScope): { where: string; ids: string[] } {
  if (s.hosts === 'all' && !s.agents) return { where: 'all', ids: running.value.map(r => r.id) }
  const ids = running.value.filter(r => selectedAgent(r, s)).map(r => r.id)
  if (s.hosts !== 'all' && s.hosts.length === 1 && hosts.value.length > 1 && hostIds.value.includes(s.hosts[0]!)) {
    const onHost = running.value.filter(r => r.host === s.hosts[0])
    if (ids.length === onHost.length && ids.every(id => onHost.some(r => r.id === id))) return { where: `h:${s.hosts[0]}`, ids }
  }
  return ids.length ? { where: 'pick', ids } : { where: 'all', ids: running.value.map(r => r.id) }
}
function openForm() {
  const remembered = whereOf(scope.value)
  where.value = remembered.where; picked.value = remembered.ids; mins.value = savedMinutes.value; typed.value = ''; submitted.value = false
  panel.value = 'form'
}
function chooseWhere(value: string) {
  where.value = value
  if (value === 'pick' && !picked.value.length) picked.value = running.value.map(s => s.id)
}
function togglePick(id: string) { picked.value = picked.value.includes(id) ? picked.value.filter(p => p !== id) : [...picked.value, id] }
function setPreset(m: number) { mins.value = m; typed.value = '' }
function typeBy(value: string) { mins.value = null; typed.value = value }
function presetKeys(event: KeyboardEvent) {
  const step = event.key === 'ArrowRight' || event.key === 'ArrowDown' ? 1 : event.key === 'ArrowLeft' || event.key === 'ArrowUp' ? -1 : 0
  if (!step) return
  const group = event.currentTarget
  if (!(group instanceof HTMLElement)) return
  event.preventDefault()
  const at = mins.value === null ? -1 : PRESETS.indexOf(mins.value as typeof PRESETS[number]), next = PRESETS[(at + step + PRESETS.length) % PRESETS.length]!
  setPreset(next)
  void nextTick(() => group.querySelector<HTMLElement>('[aria-checked="true"]')?.focus())
}
function formKeys(event: KeyboardEvent) {
  if (event.key !== 'Enter' || event.isComposing) return
  const shortcut = !event.altKey && !event.shiftKey && (mac ? event.metaKey && !event.ctrlKey : event.ctrlKey && !event.metaKey)
  if (shortcut) { event.preventDefault(); void confirm(); return }
  const target = event.target
  if (target instanceof HTMLInputElement || target instanceof HTMLTextAreaElement) event.preventDefault()
}
async function confirm() {
  if (!ownershipReady.value || pause.leavingBusy) return
  submitted.value = true
  if (error.value || !parsed.value) { await nextTick(); document.getElementById(timeError.value ? 'wd-by' : 'wd-where')?.focus(); return }
  const turn = scopeEpoch, owner = `${session.identity?.tenant.id}:${session.identity?.principal.id}`
  const at = mins.value !== null ? agents.now + mins.value * 60_000 : parsed.value.at
  const captured: WindDownScope = { hosts: formScope.value.hosts === 'all' ? 'all' : [...formScope.value.hosts], ...(formScope.value.agents ? { agents: [...formScope.value.agents] } : {}) }
  const accepted = await pause.windDown(new Date(at).toISOString(), captured)
  if (!accepted || `${session.identity?.tenant.id}:${session.identity?.principal.id}` !== owner || turn !== scopeEpoch) return
  scope.value = captured
  if (mins.value !== null) savedMinutes.value = mins.value
  void writePreference('agents.wind-down-scope', { ...captured, ...(mins.value !== null ? { minutes: mins.value } : {}) })
  closePanel(false)
  await nextTick(); chipButton.value?.focus({ preventScroll: true })
}
// ---------- The chip and its status ----------
const selectedScope = computed<WindDownScope>(() => active.value && pause.report ? { hosts: pause.report.hosts ?? 'all', ...(pause.report.agents ? { agents: pause.report.agents } : {}) } : scope.value)
const planRows = computed(() => active.value ? pause.reportIds.map(id => agents.sessionById(id)).filter((s): s is HarnessSession => !!s) : [])
const untouched = computed(() => running.value.filter(s => !pause.reportIds.includes(s.id)))
const planHosts = computed(() => hosts.value.filter(h => planRows.value.some(s => s.host === h.id) || selectedScope.value.hosts === 'all' || selectedScope.value.hosts.includes(h.id)))
const tally = computed(() => {
  const counts = { running: 0, handover: 0, terminal: 0, lost: 0 }
  for (const row of planRows.value) counts[windDownResult(row)]++
  return counts
})
const total = computed(() => planRows.value.length)
const left = computed(() => tally.value.running + tally.value.lost)
const done = computed(() => !pause.leavingError && pause.settled)
const byClock = computed(() => pause.report?.deadline_at ? hhmm(Date.parse(pause.report.deadline_at)) : '')
const minutesLeft = computed(() => Math.max(0, Math.ceil((Date.parse(pause.report?.deadline_at ?? '') - agents.now) / 60_000)))
const startedAt = computed(() => { const times = planRows.value.map(s => Date.parse(s.pause?.requested_at || '')).filter(Number.isFinite); return times.length ? hhmm(Math.min(...times)) : 'unknown' })
const progressPct = computed(() => total.value ? ((tally.value.handover + tally.value.terminal) / total.value) * 100 : 0)
const ring = computed(() => { const c = 2 * Math.PI * 5, share = progressPct.value / 100; return `${(share * c).toFixed(1)} ${c.toFixed(1)}` })
const chipRest = computed(() => done.value ? '' : minutesLeft.value === 0 ? `· deadline reached · by ${byClock.value}` : `· ${left.value} left · by ${byClock.value}`)
const progressSentence = computed(() => {
  const { handover, terminal, lost } = tally.value
  const parts = [`${handover} of ${total.value} handed over`]
  if (terminal) parts.push(`${terminal} confirmed terminal ${terminal === 1 ? 'outcome' : 'outcomes'}`)
  if (lost) parts.push(`${lost} lost contact, exit unconfirmed`)
  if (!done.value) parts.push(minutesLeft.value === 0 ? 'deadline reached, awaiting reported outcomes' : `${minutesLeft.value} min left`)
  return `${parts.join(' · ')}. New starts remain allowed until enforcement exists; started ${startedAt.value}.`
})
function line(s: HarnessSession) {
  if (s.pause) {
    if (!liveSession(s)) {
      const result = windDownResult(s)
      if (result === 'handover') return 'Handed over · handover saved'
      if (result === 'lost') return 'Lost contact · exit unconfirmed'
      return s.finished ? 'Finished' : 'Ended'
    }
    if (s.pause.stop_requested) return 'Stop requested · exit unconfirmed'
    if (agents.now >= Date.parse(s.pause.deadline_at)) return 'Deadline reached · awaiting outcome'
    return `${levelName(s.pause.level)}${s.pause.deliver === false && s.pause.starts_at ? ` starts ${clockTime(s.pause.starts_at)}` : ''} · by ${hhmm(Date.parse(s.pause.deadline_at))}`
  }
  const p = predictWindDown(s, agents.now, deadline.value, pause.interval), own = prediction(s, p.level, p.starts, pause.interval)
  return `${levelName(p.level)} · ${own.outcome}`
}
function toggleStatus() { panel.value === 'status' ? closePanel(true) : (panel.value = 'status') }
async function cancel() {
  closePanel(false)
  await pause.cancelWindDown()
  await nextTick(); if (!active.value) restButton.value?.focus({ preventScroll: true })
}
function bulk(mode: 'pause-all' | 'resume-all') {
  const targets = mode === 'pause-all' ? canPause.value : canResume.value
  if (!targets.length) return
  const anchor = restButton.value ?? chipButton.value ?? null
  closePanel(false)
  pause.open(mode, targets, anchor)
}
function closePanel(restore: boolean) {
  const was = panel.value
  panel.value = null
  if (restore && was) (active.value ? chipButton.value : restButton.value)?.focus({ preventScroll: true })
}
const anchor = computed(() => active.value ? chipButton.value ?? null : restButton.value ?? null)
let labelsEpoch = 0
async function loadLabels() {
  const turn = ++labelsEpoch
  labels.value = {}; labelsError.value = ''
  try { const read = await listHostLabels(); if (turn === labelsEpoch) labels.value = Object.fromEntries(read.map(row => [row.host, row.label])) }
  catch { if (turn === labelsEpoch) labelsError.value = 'Personal computer names could not be loaded; registered names are shown.' }
}
watch(() => `${session.identity?.tenant.id}:${session.identity?.principal.id}`, () => { labelsEpoch++; scopeEpoch++; panel.value = null; scope.value = { hosts: 'all' }; savedMinutes.value = 15; labels.value = {}; labelsError.value = ''; if (pause.person) { void loadLabels(); void loadScope() } }, { flush: 'sync' })
// The chip replaces the button; a status popover for a wind-down that is gone closes.
watch(active, now => { if (!now && panel.value === 'status') panel.value = null })
onMounted(() => { void loadLabels(); void loadScope(); void pause.loadSettings(); void pause.refreshLeaving() })
defineExpose({ open: openForm, openStatus: () => { panel.value = 'status' } })
</script>
<template>
  <span v-if="visible" class="wind-down" :class="{ active }">
    <span v-if="active" class="wd-chip" :class="{ done }" role="group" :aria-label="done ? 'Wind-down complete' : 'Wind-down active'">
      <button ref="chipButton" type="button" class="wd-main" data-wd="chip" aria-haspopup="dialog" :aria-expanded="panel === 'status'" title="Wind-down: details and cancel" @click="toggleStatus">
        <svg class="wd-ring" viewBox="0 0 14 14" aria-hidden="true"><circle class="trk" cx="7" cy="7" r="5" /><circle class="val" cx="7" cy="7" r="5" :stroke-dasharray="ring" /></svg>
        <span>{{ done ? 'Wound down' : 'Winding down' }}</span><span v-if="chipRest" class="wd-rest">{{ chipRest }}</span>
      </button>
      <button type="button" class="wd-x" data-wd="cancel" :aria-label="done ? 'Dismiss' : 'Cancel the wind-down'" :title="done ? 'Dismiss' : 'Cancel the wind-down'" :disabled="pause.leavingBusy" @click="cancel"><AppIcon name="close" :size="12" /></button>
    </span>
    <button v-else ref="restButton" type="button" class="btn sm ghost wd-btn" :class="{ failed: !!pause.leavingError }" data-wd="open" aria-haspopup="dialog" :aria-expanded="panel === 'form'" :title="pause.leavingError ? 'The wind-down status could not be read' : 'Wind down, pause all or resume all'" aria-label="Wind down" @click="panel === 'form' ? closePanel(true) : openForm()">
      <AppIcon :name="pause.leavingError ? 'alert' : 'winddown'" :size="15" /><span class="wl">Wind down</span>
    </button>

    <FloatingPanel v-if="panel" :anchor="anchor" align="end" :width="panel === 'form' ? 380 : 360" :tallest="640" :label="panel === 'form' ? 'Wind down' : 'Wind-down'" field-escape cycle @close="restore => closePanel(restore)">
      <form v-if="panel === 'form'" class="wdf" novalidate @submit.prevent="confirm" @keydown="formKeys">
        <h3><AppIcon name="winddown" :size="16" />Wind down</h3>
        <p>Agents finish their current step, save a handover and stop by the time you set. New starts on those computers remain allowed until enforcement exists.</p>
        <div class="wdf-grid">
          <label for="wd-where">Where</label>
          <select id="wd-where" class="field" data-autofocus :value="where" @change="chooseWhere(($event.target as HTMLSelectElement).value)"><option v-for="o in whereOptions" :key="o.value" :value="o.value">{{ o.label }}</option></select>
          <span id="wd-in-l" class="lb">Done in</span>
          <span class="seg" role="radiogroup" aria-labelledby="wd-in-l" @keydown="presetKeys"><button v-for="m in PRESETS" :key="m" type="button" role="radio" :aria-checked="mins === m" :tabindex="mins === m || (mins === null && m === PRESETS[0]) ? 0 : -1" @click="setPreset(m)">{{ m }} min</button></span>
          <label for="wd-by">By</label>
          <span class="by-row"><input id="wd-by" class="field n" :value="byText" inputmode="numeric" autocomplete="off" maxlength="5" aria-describedby="wd-hint" :aria-invalid="timeError ? 'true' : undefined" @input="typeBy(($event.target as HTMLInputElement).value)" /><span class="small">{{ aheadNote }}</span></span>
        </div>
        <p id="wd-hint" class="qhint" :class="{ err: error || pause.leavingError }" aria-live="polite">{{ error || (!ownershipReady && !pause.leavingError ? 'Reading owned sessions…' : '') }}<template v-if="pause.leavingError">{{ pause.leavingError }} <button type="button" class="link-btn" @click="pause.refreshLeaving()">Retry</button></template></p>
        <div class="wdf-acts">
          <button type="button" class="btn ghost" @click="closePanel(true)">Cancel<kbd class="keycap">Esc</kbd></button>
          <button type="submit" class="btn primary" :disabled="!ownershipReady || pause.leavingBusy">Wind down<span class="go-by" :class="{ hidden: !!timeError }">· by {{ goTime }}</span><span class="submit-keys"><KeyCap k="mod" /><KeyCap k="enter" /></span></button>
        </div>
        <p class="wdf-prev"><b>{{ chosen.length }} agent{{ chosen.length === 1 ? '' : 's' }}</b> {{ wherePhrase }} hand over by <template v-if="timeError">the time you set</template><b v-else>{{ byText }}</b>. New starts remain allowed until enforcement exists.<small>{{ previewLevels }}. Preview from the latest reports; eligibility and timing are checked on confirm.</small></p>
        <p v-if="labelsError" class="wdf-note" role="status">{{ labelsError }}</p>
        <div class="wd-sep" role="separator" />
        <p class="eyebrow now-head">Right now</p>
        <button type="button" class="wd-item" :aria-disabled="!canPause.length || pause.leavingBusy || undefined" @click="bulk('pause-all')"><AppIcon name="pause" :size="16" /><span class="t">Pause all…</span><span class="d">{{ canPause.length ? 'Each agent saves a handover and pauses at its next safe point.' : 'Nothing outside the wind-down can pause right now.' }}</span></button>
        <button type="button" class="wd-item" :aria-disabled="!canResume.length || pause.leavingBusy || undefined" @click="bulk('resume-all')"><AppIcon name="play" :size="16" /><span class="t">Resume all…</span><span class="d">{{ canResume.length ? `Continue ${canResume.length} paused agent${canResume.length === 1 ? '' : 's'} from their handovers, leads first.` : 'Nothing is paused.' }}</span></button>
        <fieldset v-if="where === 'pick'" class="wd-pick"><legend class="sr-only">Agents to wind down</legend><label v-for="s in running" :key="s.id" class="pk"><input type="checkbox" :checked="picked.includes(s.id)" @change="togglePick(s.id)" /><span>{{ name(s) }}</span><small>{{ hostLabel(s.host) }}</small></label></fieldset>
      </form>
      <div v-else class="wds">
        <h3>{{ done ? (pause.completedAt === null ? 'Wound down' : `Wound down at ${hhmm(pause.completedAt)}`) : `Winding down · by ${byClock}` }}</h3>
        <!-- Outcome copy and the plan grow as reports arrive. Actions stay above that, so a longer sentence never pushes them (AEON-541). -->
        <div class="wdf-acts">
          <button type="button" class="btn ghost" @click="closePanel(true)">Close</button>
          <button v-if="done" type="button" class="btn" :disabled="pause.leavingBusy" @click="cancel">Dismiss</button>
          <button v-else type="button" class="btn danger" :disabled="pause.leavingBusy" @click="cancel">Cancel the wind-down</button>
        </div>
        <div class="wd-sep" role="separator" />
        <p class="eyebrow now-head">Right now</p>
        <button type="button" class="wd-item" :aria-disabled="!canPause.length || pause.leavingBusy || undefined" @click="bulk('pause-all')"><AppIcon name="pause" :size="16" /><span class="t">Pause all…</span><span class="d">{{ canPause.length ? 'Each agent saves a handover and pauses at its next safe point.' : 'Nothing outside the wind-down can pause right now.' }}</span></button>
        <button type="button" class="wd-item" :aria-disabled="!canResume.length || pause.leavingBusy || undefined" @click="bulk('resume-all')"><AppIcon name="play" :size="16" /><span class="t">Resume all…</span><span class="d">{{ canResume.length ? `Continue ${canResume.length} paused agent${canResume.length === 1 ? '' : 's'} from their handovers, leads first.` : 'Nothing is paused.' }}</span></button>
        <div class="bar" aria-hidden="true"><i :style="{ width: `${progressPct}%`, boxShadow: 'none', background: 'var(--teal)' }" /></div>
        <p>{{ progressSentence }}</p>
        <p v-if="pause.leavingError" class="wds-error" role="alert">{{ pause.leavingError }} <button type="button" class="link-btn" @click="pause.refreshLeaving()">Retry</button></p>
        <p v-if="labelsError" class="wdf-note" role="status">{{ labelsError }}</p>
        <div class="wd-scroll">
          <template v-for="h in planHosts" :key="h.id">
            <p class="mono-label wd-host">{{ h.label }}</p>
            <p v-if="!planRows.some(s => s.host === h.id)" class="wd-un">Nothing running</p>
            <ul class="wd-plan"><li v-for="s in planRows.filter(r => r.host === h.id)" :key="s.id"><span>{{ name(s) }}</span><small>{{ line(s) }}</small></li></ul>
          </template>
          <template v-if="untouched.length"><p class="mono-label wd-host">Keep running, untouched</p><p class="wd-un">{{ untouched.map(s => `${name(s)} · ${hostLabel(s.host)}`).join(', ') }}</p></template>
        </div>
      </div>
    </FloatingPanel>
  </span>
</template>
<style scoped>
.wind-down { display: inline-flex; align-items: center; flex: none; }
.go-by { font-variant-numeric: tabular-nums; }
.go-by.hidden { visibility: hidden; }
.submit-keys { display: inline-flex; gap: 2px; align-items: center; flex: none; }
.wd-btn { color: var(--ink-2); }
.wd-btn svg { color: var(--ink-3); }
.wd-btn:hover svg, .wd-btn[aria-expanded="true"] svg { color: var(--teal-ink); }
.wd-btn[aria-expanded="true"] { background: var(--row-selected); }
.wd-btn.failed, .wd-btn.failed svg { color: var(--danger); }
.wd-chip { display: inline-flex; align-items: center; height: 32px; border-radius: 999px; background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); color: var(--teal-ink); }
.wd-main { display: inline-flex; align-items: center; gap: 6px; height: 100%; padding: 0 6px 0 10px; border: 0; border-radius: 999px 0 0 999px; background: transparent; color: inherit; font-size: 12.5px; font-weight: 600; white-space: nowrap; font-variant-numeric: tabular-nums; }
.wd-main .wd-rest { font-weight: 500; color: var(--ink-2); }
.wd-x { display: grid; place-items: center; width: 30px; height: 100%; padding: 0; border: 0; border-radius: 0 999px 999px 0; background: transparent; color: var(--teal-ink); }
.wd-main:hover, .wd-x:hover { background: var(--row-hover); }
.wd-ring { width: 14px; height: 14px; transform: rotate(-90deg); flex: none; }
.wd-ring circle { fill: none; stroke-width: 2.2; }
.wd-ring .trk { stroke: color-mix(in srgb, var(--teal) 22%, transparent); }
.wd-ring .val { stroke: var(--teal); stroke-linecap: round; }
.wdf, .wds { padding: 10px 10px 6px; }
.wdf h3, .wds h3 { display: flex; align-items: center; gap: 8px; margin: 0; font-size: 14px; }
.wdf h3 svg { color: var(--teal-ink); }
.wdf > p { margin: 4px 0 0; font-size: 12.5px; color: var(--ink-2); }
.wdf-grid { display: grid; grid-template-columns: 74px minmax(0, 1fr); align-items: center; gap: 10px 12px; margin-top: 12px; }
.wdf-grid > label, .wdf-grid > .lb { font-size: 12.5px; font-weight: 600; color: var(--ink); }
.wdf select.field { appearance: none; padding-right: 30px; background-image: linear-gradient(45deg, transparent 50%, var(--ink-3) 50%), linear-gradient(135deg, var(--ink-3) 50%, transparent 50%); background-position: calc(100% - 16px) 15px, calc(100% - 11px) 15px; background-size: 5px 5px; background-repeat: no-repeat; }
.wdf .seg { flex-wrap: wrap; }
.wdf .seg button { height: auto; min-height: 28px; padding: 0 10px; }
.by-row { display: flex; align-items: center; gap: 8px; }
.by-row .field.n { width: 8ch; text-align: center; font-variant-numeric: tabular-nums; }
.by-row .field[aria-invalid="true"] { box-shadow: 0 0 0 1px var(--danger-line), inset 0 0 0 1px var(--danger-line); }
.by-row .small { color: var(--ink-3); font-size: 12px; }
.wd-pick { display: grid; gap: 0; max-height: 168px; overflow: auto; margin: 8px 0 0; padding: 4px 8px; border: 0; border-radius: 10px; background: var(--surface-2); min-width: 0; }
.pk { display: flex; align-items: center; gap: 8px; min-height: 30px; font-size: 12.5px; color: var(--ink); }
.pk span { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.pk small { margin-left: auto; color: var(--ink-3); font-family: var(--mono); font-size: 11px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; max-width: 45%; }
.pk input { width: 16px; height: 16px; margin: 0; accent-color: var(--teal); flex: none; }
.qhint { min-height: 18px; margin: 8px 0 0; font-size: 12px; line-height: 18px; color: var(--ink-3); overflow-wrap: anywhere; }
.qhint.err { color: var(--danger); }
.wdf-prev { margin: 10px 0 0; padding: 10px 12px; border-radius: 10px; background: var(--surface-2); font-size: 12.5px; color: var(--ink-2); }
.wdf-prev b { color: var(--ink); font-weight: 600; }
.wdf-prev small { display: block; margin-top: 4px; font-size: 11.5px; color: var(--ink-3); }
.wdf-note { margin: 8px 0 0; font-size: 12px; color: var(--ink-3); }
.wdf-acts { display: flex; justify-content: flex-end; flex-wrap: wrap; gap: 8px; margin-top: 10px; }
.wd-sep { height: 1px; margin: 10px -4px 6px; background: var(--line); }
.now-head { margin: 0; padding: 2px 0 4px; }
.wd-item { display: grid; grid-template-columns: 16px minmax(0, 1fr); column-gap: 10px; align-items: center; width: 100%; min-height: 48px; padding: 6px 8px; border: 0; border-radius: 6px; background: transparent; color: var(--ink); font-size: 13px; text-align: left; }
.wd-item:hover { background: var(--row-hover); }
.wd-item[aria-disabled="true"] { color: var(--ink-3); cursor: default; }
.wd-item[aria-disabled="true"]:hover { background: transparent; }
.wd-item .t { grid-column: 2; font-weight: 550; }
.wd-item .d { grid-column: 2; font-size: 11px; color: var(--ink-3); }
.wd-item svg { grid-row: 1 / span 2; }
.wds .bar { margin-top: 10px; height: 6px; }
.wds p.now-head { margin: 0; padding: 2px 0 4px; font-size: 10.5px; color: var(--ink-3); }
.wds p { margin: 8px 0 0; font-size: 12.5px; color: var(--ink-2); }
.wds-error { color: var(--danger) !important; }
.wd-scroll { max-height: 220px; overflow: auto; margin-top: 8px; }
.wd-host { margin: 8px 0 0; }
.wd-plan { list-style: none; margin: 0; padding: 0; }
.wd-plan li { display: flex; justify-content: space-between; gap: 12px; padding: 4px 0; border-top: 1px solid var(--line); font-size: 12.5px; color: var(--ink); }
.wd-plan li small { color: var(--ink-3); text-align: right; font-size: 12px; }
.wd-un { margin-top: 4px !important; }
.link-btn { padding: 0; border: 0; background: transparent; color: var(--teal-ink); font: inherit; text-decoration: underline; }
/* Beside the docked session panel the head is narrow: the button keeps its icon and the chip its
   progress, so the counts never wrap the head and push the rows down (CI #355). */
@container agents-head (max-width: 960px) {
  .wd-btn .wl, .wd-main .wd-rest { display: none; }
  .wd-btn { width: 32px; padding: 0; }
}
@media (max-width: 720px) {
  .wd-btn .wl { display: none; }
  .wd-btn { width: 44px; height: 44px; padding: 0; }
  .wd-chip { height: 44px; }
  .wd-main .wd-rest { display: none; }
  .wd-x { width: 44px; }
  .wdf-grid { grid-template-columns: minmax(0, 1fr); gap: 6px; }
  .wdf .seg button, .wdf .field, .wdf .btn { min-height: 44px; }
  .pk { min-height: 44px; }
  .pk input { width: 22px; height: 22px; }
  .wd-item { min-height: 52px; }
  .wds .btn { min-height: 44px; }
}
</style>
