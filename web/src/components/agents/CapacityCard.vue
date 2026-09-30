<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { can } from '../../lib/authz'
import {
  accountPlan, daysLabel, daysSummary, gauge as gaugeOf, gaugeModeFor, nightLabel, pct, poolSentence, reserveLabel, reserveLevel, sameAccountCopy, usingNow, clone, putSchedule, setGlobalMode, sourceLine, timeLabel, todayCell, toggleAccountMode, when, whenFull, workStart,
  type AccountRow, type CapacitySchedule, type CapacityWindow, type GaugeMode, type Override, type PoolView,
} from '../../lib/capacity'
import { toast } from '../../lib/toast'
import { useAgents } from '../../stores/agents'
import { useCapacity } from '../../stores/capacity'
import { useSession } from '../../stores/session'
import AppIcon from '../AppIcon.vue'
import CapacityGauge from './CapacityGauge.vue'
import CapacityLearning from './CapacityLearning.vue'
import CapacityLegend from './CapacityLegend.vue'
import HarnessMark from './HarnessMark.vue'
import KeepEditor, { type KeepDraft } from './KeepEditor.vue'
import PlanSentence from './PlanSentence.vue'
import ScheduleEditor from './ScheduleEditor.vue'

// The accounts agents work on, per vendor pool: what is left, what is kept for
// you, today's share and where to stop tonight, one plan sentence per pool, and
// the pacing controls (work days, Keep for you, nights) with a popover each.
// Sign-ins, names and which accounts agents may use live in Settings → Accounts.
const capacity = useCapacity()
const agents = useAgents()
const session = useSession()
const mayManage = computed(() => session.identity?.principal.kind === 'person' && can('account.manage'))
const manageTip = 'Changing pacing needs permission to manage accounts'
const now = computed(() => agents.now)
const schedule = computed(() => capacity.schedule)
const days = computed(() => daysLabel(schedule.value.week))
const pools = computed(() => capacity.pools)
const card = ref<HTMLElement>()

// ---------- Rows ----------
const planOf = (row: AccountRow) => accountPlan(row, now.value)
const sentence = (pool: PoolView) => poolSentence(pool, now.value)
const modeOf = (row: AccountRow): GaugeMode => gaugeModeFor(capacity.gauge, row.id)
const figure = (row: AccountRow) => { const left = row.primary?.remaining_percent ?? 0; return modeOf(row) === 'used' ? 100 - left : left }
const DOT_TIP: Record<AccountRow['state'], (row: AccountRow) => string> = {
  live: () => 'Live: signed in, computer online, fresh reading',
  offline: row => `${row.host} is offline`,
  signin: () => 'Sign-in expired',
  unavailable: row => `Could not check this account on ${row.host}; agents skip it until the next check`,
  paused: () => 'Paused in Settings, under Accounts',
  unread: () => 'Signed in; no reading yet',
}
function gaugeLabel(row: AccountRow) {
  const plan = planOf(row)
  const g = gaugeOf(row, plan)
  const kept = g.yours >= 0.5 ? `, ${pct(g.yours)} kept for you` : ''
  const base = `${row.name}: ${Math.round(figure(row))}% ${modeOf(row)}${kept}`
  return plan && g.tick !== null ? `${base}, today's share ${pct(plan.budget)}, ${pct(plan.used)} used today` : base
}
/** What the 5-hour window keeps for you now, when it keeps something. */
const fiveKept = (w: CapacityWindow) => { const k = Math.min(w.remaining_percent, w.pacing.reserve_effective_percent ?? 0); return k >= 0.5 ? pct(k) : '' }
const anyKept = computed(() => pools.value.some(p => p.rows.some(r => (planOf(r)?.reserve ?? 0) >= 0.5)))
function toggleMode(row: AccountRow) { capacity.setGauge(toggleAccountMode(capacity.gauge, row.id)) }
function setGlobal(mode: GaugeMode) { capacity.setGauge(setGlobalMode(capacity.gauge, mode)) }
const globalMode = computed(() => capacity.gauge?.mode ?? 'left')
async function copyCommand(row: AccountRow, command: string) {
  try { await navigator.clipboard?.writeText(command) } catch { /* the toast still names it */ }
  toast(`Copied: ${command} — run it on ${row.host}.`)
}

// ---------- Simple controls ----------
const busy = ref(false)
async function run(work: () => Promise<void>, done?: string) {
  if (busy.value) return
  busy.value = true
  try { await work(); if (done) toast(done) }
  catch (e) { toast(e instanceof Error ? e.message : 'That did not save. Please try again.', { tone: 'error' }) }
  finally { busy.value = false }
}
const setPreset = (n: 5 | 6 | 7) => { if (mayManage.value && days.value.preset !== n) void run(() => capacity.setPreset(n)) }
const toggleNights = () => { if (mayManage.value) void run(() => capacity.setNights(!schedule.value.nights)) }
function daysKey(event: KeyboardEvent) {
  if (event.key !== 'ArrowLeft' && event.key !== 'ArrowRight') return
  event.preventDefault()
  const cur = days.value.preset ?? 5
  const next = Math.max(5, Math.min(7, cur + (event.key === 'ArrowRight' ? 1 : -1))) as 5 | 6 | 7
  setPreset(next)
  void nextTick(() => card.value?.querySelector<HTMLElement>(`.days [data-v="${next}"]`)?.focus())
}

// ---------- Editors ----------
type EditorKind = 'week' | 'night' | 'keep'
const editor = ref<{ kind: EditorKind; gear: HTMLElement } | null>(null)
const editorRef = ref<{ root?: HTMLElement | null; dirty: () => boolean; focusTitle: () => void }>()
const editorStyle = ref<Record<string, string>>({})
const sheet = ref(false)
const saving = ref(false)
const phoneQuery = typeof window !== 'undefined' ? window.matchMedia('(max-width: 720px)') : null
async function openEditor(kind: EditorKind, event: Event) {
  const gear = (event.currentTarget as HTMLElement).closest<HTMLElement>('.setting')?.querySelector<HTMLElement>('.gear, .keep-btn') ?? (event.currentTarget as HTMLElement)
  if (editor.value?.kind === kind) { closeEditor(); return }
  closeMenu()
  sheet.value = !!phoneQuery?.matches
  editor.value = { kind, gear }
  editorStyle.value = { visibility: 'hidden' }
  await nextTick()
  if (!sheet.value) {
    position()
    await nextTick()
    editorRef.value?.root?.scrollIntoView({ block: 'nearest', behavior: window.matchMedia('(prefers-reduced-motion: reduce)').matches ? 'auto' : 'smooth' })
    editorRef.value?.focusTitle()
  }
}
function position() {
  const el = editorRef.value?.root
  if (!editor.value || !card.value || !el) return
  const box = card.value.getBoundingClientRect(), g = editor.value.gear.getBoundingClientRect(), w = el.offsetWidth
  // Fit the room below the control (the app footer takes the last 56 px); a tall
  // editor scrolls inside, with its footer in view.
  editorStyle.value = { top: `${g.bottom - box.top + 8}px`, left: `${Math.max(12, Math.min(g.right - box.left - w + 10, box.width - w - 12))}px`, maxHeight: `${Math.max(440, window.innerHeight - g.bottom - 64)}px` }
}
// On a phone the trigger sits in the inert page. Remember it and focus only
// after the sheet has unmounted and that inert is gone (see the sheet watch).
let sheetReturn: HTMLElement | null = null
function closeEditor(focus = true) {
  const gear = editor.value?.gear
  const onSheet = sheet.value && !!editor.value
  editor.value = null
  editorStyle.value = {}
  if (onSheet) { sheetReturn = focus ? gear ?? null : null; return }
  if (focus) gear?.focus({ preventScroll: true })
}
async function saveEditor(next: CapacitySchedule) {
  saving.value = true
  try { await capacity.saveSchedule(next); closeEditor(); toast("Saved. Today's plan follows the new schedule.") }
  catch (e) { toast(e instanceof Error ? e.message : 'The schedule did not save. Please try again.', { tone: 'error' }) }
  finally { saving.value = false }
}

// ---------- Keep for you ----------
const keepLabel = computed(() => {
  const auto = !schedule.value.reserve || schedule.value.reserve === 'auto'
  const levels = capacity.rows.flatMap(row => row.learning?.windows.map(w => w.auto_reserve_percent).filter((n): n is number => !!n) ?? [])
  return auto && levels.length ? 'Auto · learned' : reserveLabel(schedule.value)
})
async function useHours(row: AccountRow) {
  const hours = row.learning?.suggested_hours
  if (!mayManage.value || !hours || !row.schedule) return
  const next = clone(row.schedule)
  next.week = next.week.map(d => d.on ? { ...d, start: hours.start, end: hours.end } : d)
  await run(async () => { await putSchedule({ scope: 'account', account_id: row.id, schedule: next }); await capacity.load() }, `Saved ${row.name}'s work hours.`)
}
async function saveKeep(draft: KeepDraft) {
  saving.value = true
  try {
    await capacity.saveKeep(draft)
    closeEditor()
    toast(draft.away ? `Saved. Agents use everything until ${when(draft.away, now.value)}.` : draft.reserve === 'off' ? 'Saved. Agents may use everything.' : "Saved. Agents leave you room while you work.")
  } catch (e) { toast(e instanceof Error ? e.message : 'Keep for you did not save. Please try again.', { tone: 'error' }) }
  finally { saving.value = false }
}
const endAway = () => void run(() => capacity.endAway(), 'Welcome back. Agents follow your plan again.')

// ---------- The one-time plan card ----------
const cardClosed = ref(false)
const planCard = computed(() => {
  if (!mayManage.value || cardClosed.value || !capacity.loaded || !capacity.schedulesLoaded || !pools.value.length || capacity.reserveConfirmed) return null
  const s = schedule.value
  const on = s.week.filter(d => d.on)
  const hours = on.every(d => d.start === on[0].start && d.end === on[0].end) ? `${timeLabel(on[0].start)}–${timeLabel(on[0].end)}` : 'in your hours'
  const level = reserveLevel(s)
  return capacity.hasUserSchedule
    ? { title: 'New:', text: `agents now leave you room while you work, about ${level}% of every limit.`, secondary: 'Turn off', primary: 'Keep' }
    : { title: "Here's the plan.", text: `Agents work alongside you ${daysSummary(s.week)}, ${hours}, pace each account to its reset, and leave you about ${level}% of every limit while you work.`, secondary: 'Change', primary: 'Looks right' }
})
function cardPrimary() { void run(() => capacity.confirmReserve('auto'), 'Saved. Agents leave you room while you work.') }
function cardSecondary() {
  if (capacity.hasUserSchedule) { void run(() => capacity.confirmReserve('off'), 'Turned off. Agents may use everything.'); return }
  cardClosed.value = true
  void nextTick(() => card.value?.querySelector<HTMLElement>('.days [aria-checked="true"], .days button')?.focus())
}

// ---------- Sprint / Hold menu ----------
const menu = ref<{ pool: PoolView; style: Record<string, string> } | null>(null)
const menuEl = ref<HTMLElement>()
async function openMenu(pool: PoolView, event: Event) {
  const button = event.currentTarget as HTMLElement
  if (menu.value?.pool.id === pool.id) { closeMenu(true); return }
  if (editor.value && !editorRef.value?.dirty()) closeEditor(false)
  menu.value = { pool, style: { visibility: 'hidden' } }
  await nextTick()
  if (!card.value || !menuEl.value) return
  const box = card.value.getBoundingClientRect(), r = button.getBoundingClientRect(), w = menuEl.value.offsetWidth
  menu.value.style = { top: `${r.bottom - box.top + 4}px`, left: `${Math.max(8, Math.min(r.right - box.left - w, box.width - w - 8))}px` }
  // Focus only once the menu sits under its button, or the page scrolls to where it was.
  await nextTick()
  menuEl.value?.querySelector<HTMLElement>('button')?.focus({ preventScroll: true })
  menuEl.value?.scrollIntoView({ block: 'nearest' })
}
function closeMenu(focus = false) {
  const id = menu.value?.pool.id
  menu.value = null
  if (focus && id) card.value?.querySelector<HTMLElement>(`[data-menu="${id}"]`)?.focus()
}
/** The pool's own override; Away comes from your own schedule and ends in the header. */
const ownOverride = (pool: PoolView): Override => (pool.override === 'away' ? '' : pool.override)
function menuItems(pool: PoolView) {
  // The server ends a Sprint at the pool's earliest limiting reset (5-hour windows included).
  const reset = pool.sprintEnd
  const own = ownOverride(pool)
  const items: { value: Override; icon: 'play' | 'bolt' | 'pause'; title: string; desc: string }[] = []
  if (own) items.push({ value: '', icon: 'play', title: 'Back to the plan', desc: 'Pace by your work week again.' })
  if (own !== 'sprint') items.push({ value: 'sprint', icon: 'bolt', title: 'Sprint until reset', desc: reset ? `Agents may use everything left until ${when(reset, now.value)}.` : 'Agents may use everything left until the next reset.' })
  if (own !== 'hold') items.push({ value: 'hold', icon: 'pause', title: 'Hold', desc: `Agents leave ${pool.name} alone. Running steps finish.` })
  return items
}
/** Hold for 2 hours, until tomorrow's hours, or until you resume. */
function holdOptions() {
  const later = new Date(Math.floor((now.value + 2 * 3600e3) / 60_000) * 60_000).toISOString()
  const tomorrow = new Date(workStart(schedule.value, now.value, 1)).toISOString()
  // "tomorrow 08:00" or "Mon 08:00": the day in the label, the time as the hint.
  const [day, time] = when(tomorrow, now.value).split(' ')
  return [
    { label: 'For 2 hours', hint: `until ${when(later, now.value)}`, name: 'Hold for 2 hours', until: later },
    { label: `Until ${day}`, hint: time ?? '', name: `Hold until ${when(tomorrow, now.value)}`, until: tomorrow },
    { label: 'Until I resume', hint: '', name: 'Hold until I resume', until: undefined },
  ]
}
function menuKeys(event: KeyboardEvent) {
  if (event.key === 'Escape') { event.preventDefault(); event.stopPropagation(); closeMenu(true); return }
  if (event.key !== 'ArrowDown' && event.key !== 'ArrowUp') return
  event.preventDefault()
  const buttons = [...(menuEl.value?.querySelectorAll<HTMLElement>('button') ?? [])]
  const i = buttons.indexOf(document.activeElement as HTMLElement)
  buttons[(i + (event.key === 'ArrowDown' ? 1 : -1) + buttons.length) % buttons.length]?.focus()
}
function setOverride(pool: PoolView, value: Override, until?: string) {
  const id = pool.id
  closeMenu()
  const done = value === 'sprint' ? `Sprint: agents may use everything left on ${pool.name} until it resets.`
    : value === 'hold' ? `Holding ${pool.name}${until ? ` until ${when(until, now.value)}` : ''}. Running steps finish.` : `${pool.name} follows your work week again.`
  void run(() => capacity.setPoolOverride(id, value, until), done).then(() => nextTick(() => card.value?.querySelector<HTMLElement>(`[data-menu="${id}"]`)?.focus({ preventScroll: true })))
}

// ---------- Phone sheet: the page behind it is inert ----------
const inerted: { el: HTMLElement; inert: boolean; hidden: string | null }[] = []
function setBackground(off: boolean) {
  if (off) {
    const host = document.querySelector('.sheet-host')
    for (const el of [...document.body.children] as HTMLElement[]) {
      if (host && el.contains(host)) continue
      inerted.push({ el, inert: el.inert, hidden: el.getAttribute('aria-hidden') })
      el.inert = true
      el.setAttribute('aria-hidden', 'true')
    }
  } else {
    for (const { el, inert, hidden } of inerted.splice(0)) {
      el.inert = inert
      if (hidden === null) el.removeAttribute('aria-hidden'); else el.setAttribute('aria-hidden', hidden)
    }
  }
}
watch(() => !!editor.value && sheet.value, async open => {
  await nextTick()
  setBackground(false)
  const back = sheetReturn
  sheetReturn = null
  if (open) setBackground(true)
  else if (back?.isConnected) back.focus({ preventScroll: true })
})
onBeforeUnmount(() => setBackground(false))

// ---------- Outside clicks ----------
function outside(event: MouseEvent) {
  const target = event.target as HTMLElement
  if (!target.isConnected) return
  if (menu.value && !menuEl.value?.contains(target) && !target.closest('[data-menu]')) closeMenu()
  if (editor.value && !sheet.value && !editorRef.value?.root?.contains(target) && !target.closest('.gear, .days, .keep-btn') && !editorRef.value?.dirty()) closeEditor(false)
}
onMounted(() => { document.addEventListener('click', outside, true); window.addEventListener('resize', position) })
onBeforeUnmount(() => { document.removeEventListener('click', outside, true); window.removeEventListener('resize', position) })
</script>

<template>
  <section ref="card" class="cap glass-card" aria-labelledby="cap-title">
    <div class="cap-head">
      <div class="cap-title">
        <h2 id="cap-title">Accounts</h2>
        <span v-if="capacity.ready.total" class="cap-meta">{{ capacity.ready.live }} of {{ capacity.ready.total }} ready</span>
        <span v-if="capacity.away" class="away-chip" :data-tip="`Away: agents use everything left until ${whenFull(capacity.away)}`">
          Away until {{ when(capacity.away, now) }}
          <button v-if="mayManage" type="button" aria-label="Back now: end Away" data-tip="Back now" :disabled="busy" @click="endAway"><AppIcon name="close" :size="12" /></button>
        </span>
      </div>
      <div class="settings" role="group" aria-label="Pacing">
        <div class="setting days">
          <span id="days-lbl" class="lbl">Work days a week</span>
          <span class="seg" role="radiogroup" aria-labelledby="days-lbl" @keydown="daysKey">
            <button
              v-for="n in ([5, 6, 7] as const)" :key="n" type="button" role="radio" :data-v="n" :aria-checked="days.preset === n" :tabindex="(days.preset ?? 5) === n && days.preset !== null ? 0 : -1"
              :disabled="!mayManage || busy" :data-tip="mayManage ? undefined : manageTip" @click="setPreset(n)"
            >{{ n }}</button>
            <button v-if="days.preset === null" type="button" role="radio" data-v="custom" aria-checked="true" tabindex="0" :disabled="!mayManage" @click="openEditor('week', $event)">{{ days.custom }}</button>
          </span>
          <span class="cap-hint" :class="{ custom: days.preset === null }">{{ days.hint }}</span>
          <button class="gear" type="button" aria-haspopup="dialog" :aria-expanded="editor?.kind === 'week'" aria-label="Customize work week" :data-tip="mayManage ? 'Customize work week' : manageTip" :disabled="!mayManage" @click="openEditor('week', $event)"><AppIcon name="gear" :size="15" /></button>
        </div>
        <span class="divider" aria-hidden="true" />
        <div class="setting keep">
          <span id="keep-lbl" class="lbl">Keep for you</span>
          <button
            class="keep-btn" type="button" aria-haspopup="dialog" :aria-expanded="editor?.kind === 'keep'" aria-labelledby="keep-lbl keep-val" :disabled="!mayManage"
            :data-tip="mayManage ? 'How much of every limit agents leave you while you work' : manageTip" @click="openEditor('keep', $event)"
          ><span id="keep-val">{{ keepLabel }}</span><AppIcon name="chevron" :size="14" /></button>
        </div>
        <span class="divider" aria-hidden="true" />
        <div class="setting nights">
          <span id="nights-lbl" class="lbl" @click="toggleNights">Agents at night</span>
          <button class="tog" type="button" role="switch" :aria-checked="schedule.nights" aria-labelledby="nights-lbl" :disabled="!mayManage || busy" :data-tip="mayManage ? undefined : manageTip" @click="toggleNights" />
          <span class="cap-hint" :class="{ off: !schedule.nights }" :title="schedule.nights ? `Agents also work outside your hours: ${nightLabel(schedule)}` : 'Agents follow your work hours only'">{{ nightLabel(schedule) }}</span>
          <button class="gear" type="button" aria-haspopup="dialog" :aria-expanded="editor?.kind === 'night'" aria-label="Customize night and shifts" :data-tip="mayManage ? 'Customize night and shifts' : manageTip" :disabled="!mayManage" @click="openEditor('night', $event)"><AppIcon name="gear" :size="15" /></button>
        </div>
      </div>
      <RouterLink class="manage" to="/settings/accounts" data-tip="Accounts in Settings: sign-ins, names, which accounts agents may use"><AppIcon name="sliders" :size="15" /><span>Manage<span class="long"> accounts</span></span></RouterLink>
      <div v-if="planCard" class="plan-card" role="region" aria-label="The plan">
        <p><b>{{ planCard.title }}</b> {{ planCard.text }}</p>
        <span class="pc-actions">
          <button class="btn" type="button" :disabled="busy" @click="cardSecondary">{{ planCard.secondary }}</button>
          <button class="btn primary" type="button" :disabled="busy" @click="cardPrimary">{{ planCard.primary }}</button>
        </span>
      </div>
    </div>

    <p v-if="capacity.loaded && !pools.length" class="empty">No accounts yet. Sign in to a harness on a connected computer and it appears here.</p>
    <p v-else-if="!capacity.loaded && capacity.state === 'error'" class="empty">Capacity could not be loaded. <button type="button" class="btn sm" @click="capacity.load()">Try again</button></p>

    <div v-for="pool in pools" :key="pool.id" class="pool" :data-pool="pool.id">
      <div class="pool-info">
        <div class="pool-head">
          <span class="vendor"><HarnessMark :harness="pool.mark || pool.id" :size="16" /></span>
          <span class="pool-name">{{ pool.name }}</span>
          <span v-if="pool.plan" class="pool-plan" :title="pool.plan">{{ pool.plan }}</span>
          <span v-if="ownOverride(pool)" class="override" :class="{ hold: pool.override === 'hold' }">
            {{ pool.override === 'hold' ? 'On hold' : 'Sprint' }}
            <button v-if="mayManage" type="button" aria-label="Back to the plan" data-tip="Back to the plan" @click="setOverride(pool, '')"><AppIcon name="close" :size="12" /></button>
          </span>
          <button
            v-if="mayManage" class="icon-btn flat more" type="button" :data-menu="pool.id" aria-haspopup="menu" :aria-expanded="menu?.pool.id === pool.id"
            :aria-label="`${pool.name}: sprint or hold`" data-tip="Sprint or hold" @click="openMenu(pool, $event)"
          ><AppIcon name="more" :size="16" /></button>
        </div>
        <p class="plan" :class="{ ahead: sentence(pool).ahead }"><PlanSentence :sentence="sentence(pool)" /></p>
      </div>
      <ul class="accts">
        <li v-for="row in pool.rows" :key="row.id" class="acct" :class="{ dim: row.state !== 'live' && row.state !== 'unread', ahead: planOf(row)?.ahead }" :data-account="row.id">
          <div class="acct-name">
            <span class="dot" :class="row.state" :data-tip="DOT_TIP[row.state](row)"><span class="sr-only">{{ DOT_TIP[row.state](row) }}</span></span>
            <span class="nm" :title="row.name">{{ row.name }}</span>
            <span v-if="usingNow(row, now)" class="presence" title="You're using this account" aria-label="You're using this account"><AppIcon name="user" :size="13" /></span>
            <span v-for="host in (row.hosts.length ? row.hosts : row.host ? [row.host] : [])" :key="host" class="chip host">{{ host }}</span>
            <p v-if="sameAccountCopy(row.hosts)" class="same-quota">{{ sameAccountCopy(row.hosts) }}</p>
          </div>
          <div class="gauge-cell">
            <CapacityGauge v-if="row.primary || !row.learning"
              :gauge="row.primary ? gaugeOf(row, planOf(row)) : null" :left="row.primary?.remaining_percent" :value="figure(row)" :used="modeOf(row) === 'used'"
              :estimated="row.primary?.reading.source === 'estimate'" :label="gaugeLabel(row)" :ahead="!!planOf(row)?.ahead" :dim="row.state !== 'live' && row.state !== 'unread'"
            />
            <div v-if="row.five" class="win5" :title="`5-hour window: ${pct(row.five.remaining_percent)} left${fiveKept(row.five) ? `, ${fiveKept(row.five)} kept for you` : ''}, resets ${whenFull(row.five.reading.resets_at)}`">5-hour <b>{{ Math.round(modeOf(row) === 'used' ? 100 - row.five.remaining_percent : row.five.remaining_percent) }}% {{ modeOf(row) }}</b><template v-if="fiveKept(row.five)"> · keeps <b>{{ fiveKept(row.five) }}</b> for you</template> · resets {{ when(row.five.reading.resets_at, now) }}</div>
          </div>
          <button
            v-if="row.primary" type="button" class="left" :aria-label="`${row.name}: ${Math.round(figure(row))}% ${modeOf(row)}. Show % ${modeOf(row) === 'left' ? 'used' : 'left'} for this account`"
            :data-tip="`Show % ${modeOf(row) === 'left' ? 'used' : 'left'} for ${row.name}`" @click="toggleMode(row)"
            :class="{ kept: planOf(row)?.atReserve }"
          ><b>{{ Math.round(figure(row)) }}%</b><span>{{ modeOf(row) }}</span></button>
          <span v-else class="left" />
          <template v-for="cell in [todayCell(row, planOf(row))]" :key="cell.kind">
            <span v-if="cell.kind === 'signin'" class="today">
              <button v-if="cell.command" class="btn" type="button" :data-tip="`Copies ${cell.command} to run on ${row.host}`" @click="copyCommand(row, cell.command)">Sign in again</button>
              <span v-else class="quiet">sign in on {{ row.host }}</span>
            </span>
            <span v-else-if="cell.kind === 'sprint'" class="today" data-tip="Sprint: everything left may be used before the reset"><b>{{ cell.text }}</b></span>
            <span v-else-if="cell.kind === 'ahead'" class="today ahead" :data-tip="`Plan for today ${cell.plan}; ${cell.used} already used`"><b>{{ cell.used }}</b> of {{ cell.plan }} · ahead</span>
            <span v-else-if="cell.kind === 'share'" class="today" :data-tip="cell.tip"><b>{{ cell.value }}</b> today</span>
            <span v-else-if="cell.kind === 'reserve'" class="today kept" :data-tip="cell.tip">{{ cell.text }}</span>
            <span v-else class="today quiet">{{ cell.text }}</span>
          </template>
          <span class="resets" :data-tip="row.primary ? whenFull(row.primary.reading.resets_at) : undefined">{{ row.primary ? `resets ${when(row.primary.reading.resets_at, now)}` : '' }}</span>
          <span class="source" :title="sourceLine(row, now)">{{ sourceLine(row, now) }}</span>
          <CapacityLearning class="row-learning" :learning="row.learning" :host="row.host" :now="now" :may-manage="mayManage" :saving="busy" @hours="useHours(row)" @away="openEditor('keep', $event)" />
        </li>
      </ul>
    </div>

    <div v-if="pools.length" class="cap-foot">
      <CapacityLegend :yours="anyKept" />
      <span class="fine">Your own use counts toward today's share too. Each account ends at 0% at its reset.</span>
      <span class="gauge-mode">
        <span id="gauge-mode-lbl" class="sr-only">Gauges show</span>
        <span class="seg" role="radiogroup" aria-labelledby="gauge-mode-lbl">
          <button v-for="m in (['left', 'used'] as const)" :key="m" type="button" role="radio" :aria-checked="globalMode === m" :data-tip="`Show % ${m} on every gauge`" @click="setGlobal(m)">% {{ m }}</button>
        </span>
      </span>
    </div>

    <div v-if="menu" ref="menuEl" class="menu" role="menu" :aria-label="`${menu.pool.name}: sprint or hold`" :style="menu.style" @keydown="menuKeys">
      <template v-for="item in menuItems(menu.pool)" :key="item.value">
        <button v-if="item.value !== 'hold'" type="button" role="menuitem" @click="setOverride(menu.pool, item.value)">
          <AppIcon :name="item.icon" :size="16" /><span class="t">{{ item.title }}</span><span class="d">{{ item.desc }}</span>
        </button>
        <div v-else class="hold" role="group" aria-labelledby="hold-t">
          <AppIcon :name="item.icon" :size="16" /><span id="hold-t" class="t">{{ item.title }}</span><span class="d">{{ item.desc }}</span>
          <span class="hold-opts">
            <button v-for="h in holdOptions()" :key="h.name" type="button" role="menuitem" :aria-label="h.name" @click="setOverride(menu.pool, 'hold', h.until)"><span>{{ h.label }}</span><span class="hint">{{ h.hint }}</span></button>
          </span>
        </div>
      </template>
    </div>

    <template v-if="editor && !sheet">
      <KeepEditor
        v-if="editor.kind === 'keep'" ref="editorRef" :schedule="schedule" :away="capacity.away" :pool-reserves="capacity.poolReserves" :sheet="false" :now="now" :accounts="capacity.inputs" :pools="pools"
        :timezone="capacity.timezone" :saving="saving" :style="editorStyle" @close="closeEditor()" @save="saveKeep"
      />
      <ScheduleEditor
        v-else ref="editorRef" :key="editor.kind" :kind="editor.kind === 'night' ? 'night' : 'week'" :schedule="schedule" :sheet="false" :now="now" :accounts="capacity.inputs" :pools="pools" :timezone="capacity.timezone" :saving="saving"
        :style="editorStyle" @close="closeEditor()" @save="saveEditor"
      />
    </template>
    <Teleport to="body">
      <div v-if="editor && sheet" class="sheet-host">
        <div class="scrim" @click="closeEditor()" />
        <KeepEditor
          v-if="editor.kind === 'keep'" ref="editorRef" :schedule="schedule" :away="capacity.away" :pool-reserves="capacity.poolReserves" :sheet="true" :now="now" :accounts="capacity.inputs" :pools="pools"
          :timezone="capacity.timezone" :saving="saving" @close="closeEditor()" @save="saveKeep"
        />
        <ScheduleEditor
          v-else ref="editorRef" :key="editor.kind" :kind="editor.kind === 'night' ? 'night' : 'week'" :schedule="schedule" :sheet="true" :now="now" :accounts="capacity.inputs" :pools="pools" :timezone="capacity.timezone" :saving="saving"
          @close="closeEditor()" @save="saveEditor"
        />
      </div>
    </Teleport>
  </section>
</template>

<style scoped>
.cap { padding: 0; z-index: 3; }
.cap-head { display: flex; align-items: center; gap: 16px; flex-wrap: wrap; padding: 14px 18px 14px 20px; border-bottom: 1px solid var(--line); }
.cap-title { display: flex; align-items: baseline; gap: 10px; margin-right: auto; }
.cap-title h2 { font-size: 19px; }
.cap-meta { color: var(--ink-3); font-size: 13px; font-variant-numeric: tabular-nums; white-space: nowrap; }
.settings { display: flex; align-items: center; gap: 22px; }
.setting { display: flex; align-items: center; gap: 8px; color: var(--ink-2); font-size: 13px; font-weight: 550; white-space: nowrap; }
.setting .lbl { margin-right: 2px; }
.setting.nights .lbl { cursor: pointer; }
.cap-hint { min-width: 52px; color: var(--ink-3); font-size: 12px; font-weight: 450; font-variant-numeric: tabular-nums; }
.cap-hint.custom { color: var(--ink-2); }
.cap-hint.off { opacity: .55; text-decoration: line-through; text-decoration-color: var(--line-2); }
.seg button { min-width: 32px; padding: 0 10px; font-variant-numeric: tabular-nums; }
.seg button[data-v="custom"] { padding: 0 12px; white-space: nowrap; }
.seg button:disabled { cursor: default; }
.seg button:disabled:not([aria-checked="true"]) { opacity: .6; }
.divider { width: 1px; height: 22px; background: var(--line); }
.tog { position: relative; display: inline-flex; align-items: center; flex: none; width: 38px; height: 22px; padding: 0; border: 0; border-radius: 999px; background: var(--line-2); box-shadow: inset 0 1px 2px rgba(0, 0, 0, .12); cursor: pointer; }
.tog::after { content: ''; position: absolute; left: 3px; width: 16px; height: 16px; border-radius: 50%; background: #fff; box-shadow: 0 1px 3px rgba(0, 0, 0, .28); transition: transform .18s ease; }
.tog[aria-checked="true"] { background: linear-gradient(180deg, #1a8683, #0e6f6c); }
.tog[aria-checked="true"]::after { transform: translateX(16px); }
.tog:focus-visible { outline: none; box-shadow: var(--focus-ring); }
.tog::before { content: ''; position: absolute; inset: -11px -4px; }
.tog:disabled { cursor: default; opacity: .6; }
.gear { display: inline-grid; place-items: center; width: 28px; height: 28px; padding: 0; border: 0; border-radius: 999px; background: transparent; color: var(--ink-3); }
@media (hover: hover) { .gear:hover:not(:disabled) { background: var(--row-hover); color: var(--teal-ink); } }
.gear[aria-expanded="true"] { background: var(--row-selected); color: var(--teal-ink); }
.gear:disabled { opacity: .45; cursor: default; }
.gear:focus-visible { box-shadow: var(--focus-ring); }
/* Keep for you: a quiet pill with the answer already filled in. */
.keep-btn { display: inline-flex; align-items: center; gap: 6px; height: 30px; padding: 0 10px 0 12px; border: 0; border-radius: 999px; background: var(--surface-sunken); box-shadow: inset 0 0 0 1px var(--line); color: var(--ink); font-size: 12.5px; font-weight: 600; font-variant-numeric: tabular-nums; white-space: nowrap; }
.keep-btn svg { color: var(--ink-3); }
@media (hover: hover) { .keep-btn:hover:not(:disabled) { background: var(--row-hover); } }
.keep-btn[aria-expanded="true"] { background: var(--row-selected); box-shadow: inset 0 0 0 1px var(--chip-teal-line); color: var(--teal-ink); }
.keep-btn:disabled { opacity: .6; cursor: default; }
.keep-btn:focus-visible { outline: none; box-shadow: var(--focus-ring); }
.away-chip { display: inline-flex; align-items: center; gap: 4px; align-self: center; height: 24px; padding: 0 2px 0 10px; border-radius: 999px; background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); color: var(--teal-ink); font-size: 12px; font-weight: 600; white-space: nowrap; }
.away-chip button { display: grid; place-items: center; width: 20px; height: 20px; padding: 0; border: 0; border-radius: 50%; background: transparent; color: inherit; }
.away-chip button:hover { background: var(--row-hover); }
.away-chip button:focus-visible { box-shadow: var(--focus-ring); }
/* The one-time plan card: a subtle teal tint and a hairline ring, never an edge bar. */
.plan-card { order: 10; flex-basis: 100%; display: flex; align-items: center; gap: 12px 18px; flex-wrap: wrap; padding: 12px 14px 12px 16px; border-radius: 12px; background: color-mix(in srgb, var(--teal) 7%, var(--surface-raised)); box-shadow: inset 0 0 0 1px var(--chip-teal-line); }
.plan-card p { flex: 1 1 420px; margin: 0; color: var(--ink-2); font-size: 13.5px; line-height: 1.5; text-wrap: pretty; }
.plan-card b { color: var(--ink); font-weight: 650; }
.pc-actions { display: inline-flex; gap: 8px; margin-left: auto; }
.manage { display: inline-flex; align-items: center; gap: 7px; height: 32px; padding: 0 10px; border-radius: 999px; color: var(--ink-2); font-size: 13px; font-weight: 550; white-space: nowrap; text-decoration: none; }
@media (hover: hover) { .manage:hover { background: var(--row-hover); color: var(--ink); } }
.manage:focus-visible { box-shadow: var(--focus-ring); }
.empty { display: flex; align-items: center; gap: 10px; padding: 16px 20px; color: var(--ink-3); font-size: 13px; }

.pool { display: grid; grid-template-columns: 330px minmax(0, 1fr); gap: 28px; padding: 16px 18px 16px 20px; }
.pool + .pool { border-top: 1px solid var(--line); }
.pool-info { min-width: 0; }
.pool-head { display: flex; align-items: center; gap: 10px; min-height: 32px; }
.vendor { display: grid; place-items: center; flex: none; width: 30px; height: 30px; border-radius: 9px; background: var(--surface-raised); box-shadow: inset 0 0 0 1px var(--line), 0 1px 2px rgba(32, 60, 61, .06); color: var(--ink); }
.pool-name { color: var(--ink); font-size: 15px; font-weight: 650; }
.pool-plan { min-width: 0; max-width: 100%; overflow: hidden; text-overflow: ellipsis; color: var(--ink-3); font-size: 12.5px; white-space: nowrap; }
.pool-head .more { flex: none; width: 32px; height: 32px; margin-left: auto; }
.override { flex: none; display: inline-flex; align-items: center; gap: 4px; height: 24px; padding: 0 2px 0 9px; border-radius: 999px; background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); color: var(--teal-ink); font-size: 12px; font-weight: 600; white-space: nowrap; }
.override.hold { background: var(--surface-sunken); box-shadow: inset 0 0 0 1px var(--line-2); color: var(--ink-2); }
.override button { display: grid; place-items: center; width: 20px; height: 20px; padding: 0; border: 0; border-radius: 50%; background: transparent; color: inherit; }
.override button:hover { background: var(--row-hover); }
.override button:focus-visible { box-shadow: var(--focus-ring); }
.plan { margin-top: 8px; padding-left: 40px; color: var(--ink-2); font-size: 13.5px; line-height: 1.5; text-wrap: pretty; }
.plan :deep(b) { color: var(--ink); font-weight: 600; }
.plan :deep(.n) { color: var(--teal-ink); font-weight: 700; font-variant-numeric: tabular-nums; }
.plan.ahead :deep(.n) { color: var(--gold-ink); }

.presence { display: inline-grid; place-items: center; color: var(--ink-2); flex: none; }
.row-learning { grid-column: 1 / -1; }
.accts { display: grid; gap: 2px; align-self: start; margin: -6px 0 0; padding: 0; list-style: none; }
.acct { display: grid; grid-template-columns: minmax(220px, 1.15fr) minmax(140px, 1.5fr) 74px 118px 140px minmax(120px, 180px); align-items: center; gap: 16px; min-height: 44px; padding: 6px 10px; border-radius: var(--radius-row); }
@media (hover: hover) { .acct:hover { background: var(--row-hover); } }
.acct-name { display: flex; align-items: center; flex-wrap: wrap; gap: 6px 9px; min-width: 0; }
.same-quota { flex-basis: 100%; margin: 0; font-size: 12px; line-height: 1.35; color: var(--ink-3); text-wrap: pretty; }
.acct-name .nm { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--ink); font-size: 13.5px; font-weight: 600; }
.host { flex: none; }
.dot { position: relative; flex: none; width: 8px; height: 8px; border-radius: 50%; }
.dot.live, .dot.unread { background: var(--ok); box-shadow: 0 0 0 3px color-mix(in srgb, var(--ok) 18%, transparent); }
.dot.offline, .dot.paused, .dot.unavailable { background: transparent; box-shadow: inset 0 0 0 1.6px var(--ink-3); }
.dot.signin { background: var(--gold); box-shadow: 0 0 0 3px color-mix(in srgb, var(--gold) 22%, transparent); }
.left { justify-self: end; padding: 2px 4px; margin: -2px -4px; border: 0; border-radius: 6px; background: transparent; text-align: right; white-space: nowrap; }
button.left { cursor: pointer; }
@media (hover: hover) { button.left:hover { background: var(--row-hover); } }
button.left:focus-visible { box-shadow: var(--focus-ring); }
.left.kept b { color: var(--gold-ink); }
.left b { color: var(--ink); font-size: 15px; font-weight: 700; font-variant-numeric: tabular-nums; }
.left span { margin-left: 3px; color: var(--ink-3); font-size: 12px; }
.today { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--ink-2); font-size: 13px; font-variant-numeric: tabular-nums; }
.today b { color: var(--teal-ink); font-weight: 700; }
.today.ahead b { color: var(--gold-ink); }
.today.quiet, .today .quiet { color: var(--ink-3); }
.today.kept { color: var(--gold-ink); font-weight: 600; }
.today .btn { height: 26px; padding: 0 10px; font-size: 12px; }
.resets { color: var(--ink-2); font-size: 13px; white-space: nowrap; font-variant-numeric: tabular-nums; }
.source { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--ink-3); font-size: 12px; text-align: right; }
.acct.dim .gauge, .acct.dim .left, .acct.dim .resets { opacity: .6; }
.win5 { margin-top: 5px; color: var(--ink-3); font-size: 11.5px; font-variant-numeric: tabular-nums; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.win5 b { color: var(--ink-2); font-weight: 600; }

/* The gauge itself is CapacityGauge, shared with the Usage page. */
.cap-foot { display: flex; align-items: center; gap: 18px; flex-wrap: wrap; padding: 11px 20px 13px; border-top: 1px solid var(--line); color: var(--ink-3); font-size: 12px; }
.fine { margin-left: auto; }
.gauge-mode .seg button { height: 24px; min-width: 0; padding: 0 9px; font-size: 11.5px; }

.menu { position: absolute; z-index: 20; width: 300px; padding: 6px; border-radius: 12px; background: var(--surface-raised); border: 1px solid var(--glass-edge); box-shadow: var(--shadow-pop); }
.menu button { display: grid; grid-template-columns: 20px 1fr; gap: 2px 10px; width: 100%; padding: 9px 10px; border: 0; border-radius: 8px; background: transparent; color: var(--ink); text-align: left; }
.menu button:hover, .menu button:focus-visible { background: var(--row-hover); box-shadow: none; outline: none; }
.menu button svg { grid-row: span 2; margin-top: 2px; color: var(--ink-2); }
.menu .t { color: var(--ink); font-size: 13.5px; font-weight: 600; }
.menu .d { color: var(--ink-3); font-size: 12px; line-height: 1.4; }
.menu .hold { display: grid; grid-template-columns: 20px 1fr; gap: 2px 10px; padding: 9px 10px; }
.menu .hold svg { grid-row: span 2; margin-top: 2px; color: var(--ink-2); }
/* The three hold durations: compact rows under Hold, the time they end on the right. */
.hold-opts { grid-column: 1 / -1; display: grid; gap: 1px; margin: 6px -4px 0 26px; }
.menu .hold-opts button { display: flex; align-items: center; justify-content: space-between; gap: 10px; min-height: 32px; padding: 0 10px; color: var(--ink); font-size: 13px; font-weight: 550; }
.menu .hold-opts .hint { color: var(--ink-3); font-size: 12px; font-weight: 450; font-variant-numeric: tabular-nums; }
.sheet-host { position: fixed; inset: 0; z-index: 80; }
.scrim { position: absolute; inset: 0; background: var(--scrim); }
@media (prefers-reduced-motion: reduce) { .tog::after { transition: none; } }

/* Mid width: the plan sits above its accounts. */
@media (max-width: 1320px) {
  .pool { grid-template-columns: minmax(0, 1fr); gap: 10px; }
  .acct { grid-template-columns: minmax(220px, 1.1fr) minmax(120px, 1.4fr) 72px 112px 120px minmax(100px, 160px); }
  .plan { padding-left: 40px; }
}
@media (max-width: 1100px) {
  .acct { grid-template-columns: minmax(200px, 1fr) minmax(100px, 1.2fr) 68px 100px minmax(90px, 120px); }
  .source { display: none; }
}
/* Phone: stacked rows, the settings as one sunken group. */
@media (max-width: 720px) {
  .cap-head { gap: 10px; padding: 14px 14px 12px; }
  .cap-title { order: 1; flex: 1; margin: 0; }
  .manage { order: 2; height: 44px; margin-right: -6px; }
  .manage .long { display: none; }
  .settings { order: 4; display: grid; grid-template-columns: minmax(0, 1fr); gap: 0; width: 100%; padding: 0 12px; border-radius: 12px; background: var(--surface-sunken); }
  .settings .divider { display: none; }
  .setting { display: grid; grid-template-columns: minmax(0, 1fr) auto auto; grid-template-areas: "lbl ctl gear" "hint ctl gear"; align-items: center; column-gap: 6px; min-height: 60px; padding: 8px 0; white-space: normal; }
  .setting.days { grid-template-columns: minmax(0, 1fr) auto; grid-template-areas: "lbl gear" "hint gear" "ctl ctl"; row-gap: 0; padding-bottom: 12px; }
  .setting.days .seg { display: flex; margin-top: 8px; }
  .setting.days .seg button { flex: 1; }
  .setting.days .seg button[data-v="custom"] { flex: 2.4; }
  .setting .gear { grid-area: gear; width: 44px; height: 44px; margin-right: -8px; }
  .setting + .setting { box-shadow: 0 -1px 0 var(--line); }
  .setting .lbl { grid-area: lbl; align-self: end; }
  .setting .cap-hint { grid-area: hint; align-self: start; min-width: 0; }
  .setting .seg, .setting .tog { grid-area: ctl; }
  .setting.keep { grid-template-columns: minmax(0, 1fr) auto; grid-template-areas: "lbl ctl"; min-height: 56px; }
  .setting.keep .lbl { align-self: center; }
  .keep-btn { grid-area: ctl; height: 40px; padding: 0 12px 0 14px; margin-right: -2px; }
  .plan-card { order: 3; padding: 14px; }
  .plan-card p { flex-basis: 100%; }
  .pc-actions { display: grid; grid-template-columns: 1fr 1fr; width: 100%; margin: 0; }
  .pc-actions .btn { min-height: 44px; }
  .cap-title { flex-wrap: wrap; row-gap: 6px; }
  .menu .hold-opts button { min-height: 44px; }
  .win5 { white-space: normal; }
  .setting .seg button { height: 38px; min-width: 44px; }
  .tog { width: 44px; height: 26px; }
  .tog::after { width: 20px; height: 20px; }
  .tog[aria-checked="true"]::after { transform: translateX(18px); }
  .pool { gap: 8px; padding: 14px 14px 12px; }
  .plan { padding-left: 0; }
  .pool-head .more { width: 44px; height: 44px; margin-right: -8px; }
  .acct { grid-template-columns: minmax(0, 1fr) auto; grid-template-areas: "name left" "gauge gauge" "today resets" "source source" "learned learned"; gap: 4px 10px; padding: 10px 0; border-radius: 0; }
  .acct + .acct { box-shadow: 0 -1px 0 var(--line); }
  .acct:hover { background: transparent; }
  .acct-name { grid-area: name; }
  .gauge-cell { grid-area: gauge; margin: 2px 0; }
  .left { grid-area: left; }
  .today { grid-area: today; }
  .resets { grid-area: resets; text-align: right; color: var(--ink-3); font-size: 12.5px; }
  .row-learning { grid-area: learned; }
  .source { grid-area: source; display: block; text-align: left; font-size: 11.5px; }
  .today .btn { min-height: 36px; }
  .cap-foot { gap: 8px 14px; padding: 12px 14px; }
  .fine { width: 100%; margin-left: 0; }
  .menu { width: min(300px, calc(100% - 24px)); }
  .menu button { min-height: 52px; }
}
</style>
