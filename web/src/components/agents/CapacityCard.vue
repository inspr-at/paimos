<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref } from 'vue'
import { can } from '../../lib/authz'
import {
  accountPlan, daysLabel, gauge as gaugeOf, gaugeModeFor, nightLabel, pct, poolSentence, setGlobalMode, sourceLine, todayCell, toggleAccountMode, when, whenFull,
  type AccountRow, type CapacitySchedule, type GaugeMode, type Override, type PoolView,
} from '../../lib/capacity'
import { toast } from '../../lib/toast'
import { useAgents } from '../../stores/agents'
import { useCapacity } from '../../stores/capacity'
import { useSession } from '../../stores/session'
import AppIcon from '../AppIcon.vue'
import HarnessMark from './HarnessMark.vue'
import PlanSentence from './PlanSentence.vue'
import ScheduleEditor from './ScheduleEditor.vue'

// The accounts agents work on, per vendor pool: what is left, today's share and
// where to stop tonight, one plan sentence per pool, and the two simple pacing
// controls (work days, nights) with a gear each for the deep editors. Sign-ins,
// names and which accounts agents may use live in Settings → Accounts.
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
  paused: () => 'Paused in Settings → Accounts',
  unread: () => 'Signed in; no reading yet',
}
function gaugeLabel(row: AccountRow) {
  const plan = planOf(row)
  const g = gaugeOf(row, plan)
  const base = `${row.name}: ${Math.round(figure(row))}% ${modeOf(row)}`
  return plan && g.tick !== null ? `${base}, today's share ${pct(plan.budget)}, ${pct(plan.used)} used today` : base
}
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
const editor = ref<{ kind: 'week' | 'night'; gear: HTMLElement } | null>(null)
const editorRef = ref<InstanceType<typeof ScheduleEditor>>()
const editorStyle = ref<Record<string, string>>({})
const sheet = ref(false)
const saving = ref(false)
const phoneQuery = typeof window !== 'undefined' ? window.matchMedia('(max-width: 720px)') : null
async function openEditor(kind: 'week' | 'night', event: Event) {
  const gear = (event.currentTarget as HTMLElement).closest<HTMLElement>('.setting')?.querySelector<HTMLElement>('.gear') ?? (event.currentTarget as HTMLElement)
  if (editor.value?.kind === kind) { closeEditor(); return }
  closeMenu()
  sheet.value = !!phoneQuery?.matches
  editor.value = { kind, gear }
  editorStyle.value = { visibility: 'hidden' }
  await nextTick()
  if (!sheet.value) { position(); await nextTick(); editorRef.value?.focusTitle() }
}
function position() {
  const el = editorRef.value?.root
  if (!editor.value || !card.value || !el) return
  const box = card.value.getBoundingClientRect(), g = editor.value.gear.getBoundingClientRect(), w = el.offsetWidth
  editorStyle.value = { top: `${g.bottom - box.top + 8}px`, left: `${Math.max(12, Math.min(g.right - box.left - w + 10, box.width - w - 12))}px` }
}
function closeEditor(focus = true) {
  const gear = editor.value?.gear
  editor.value = null
  editorStyle.value = {}
  if (focus) gear?.focus({ preventScroll: true })
}
async function saveEditor(next: CapacitySchedule) {
  saving.value = true
  try { await capacity.saveSchedule(next); closeEditor(); toast("Saved. Today's plan follows the new schedule.") }
  catch (e) { toast(e instanceof Error ? e.message : 'The schedule did not save. Please try again.', { tone: 'error' }) }
  finally { saving.value = false }
}

// ---------- Sprint / Hold menu ----------
const menu = ref<{ pool: PoolView; style: Record<string, string> } | null>(null)
const menuEl = ref<HTMLElement>()
async function openMenu(pool: PoolView, event: Event) {
  const button = event.currentTarget as HTMLElement
  if (menu.value?.pool.id === pool.id) { closeMenu(true); return }
  if (editor.value && !editorRef.value?.dirty()) closeEditor(false)
  menu.value = { pool, style: {} }
  await nextTick()
  if (!card.value || !menuEl.value) return
  const box = card.value.getBoundingClientRect(), r = button.getBoundingClientRect(), w = menuEl.value.offsetWidth
  menu.value.style = { top: `${r.bottom - box.top + 4}px`, left: `${Math.max(8, Math.min(r.right - box.left - w, box.width - w - 8))}px` }
  menuEl.value.querySelector<HTMLElement>('button')?.focus()
}
function closeMenu(focus = false) {
  const id = menu.value?.pool.id
  menu.value = null
  if (focus && id) card.value?.querySelector<HTMLElement>(`[data-menu="${id}"]`)?.focus()
}
function soonestReset(pool: PoolView) {
  const resets = pool.rows.filter(r => r.state === 'live' && r.primary).map(r => r.primary!.reading.resets_at).sort()
  return resets[0] ?? ''
}
function menuItems(pool: PoolView) {
  const reset = soonestReset(pool)
  const items: { value: Override; icon: 'play' | 'bolt' | 'pause'; title: string; desc: string }[] = []
  if (pool.override) items.push({ value: '', icon: 'play', title: 'Back to the plan', desc: 'Pace by your work week again.' })
  if (pool.override !== 'sprint') items.push({ value: 'sprint', icon: 'bolt', title: 'Sprint until reset', desc: reset ? `Agents may use everything left until ${when(reset, now.value)}.` : 'Agents may use everything left until the next reset.' })
  if (pool.override !== 'hold') items.push({ value: 'hold', icon: 'pause', title: 'Hold', desc: `Agents leave ${pool.name} alone until you resume. Running steps finish.` })
  return items
}
function menuKeys(event: KeyboardEvent) {
  if (event.key === 'Escape') { event.preventDefault(); event.stopPropagation(); closeMenu(true); return }
  if (event.key !== 'ArrowDown' && event.key !== 'ArrowUp') return
  event.preventDefault()
  const buttons = [...(menuEl.value?.querySelectorAll<HTMLElement>('button') ?? [])]
  const i = buttons.indexOf(document.activeElement as HTMLElement)
  buttons[(i + (event.key === 'ArrowDown' ? 1 : -1) + buttons.length) % buttons.length]?.focus()
}
function setOverride(pool: PoolView, value: Override) {
  const id = pool.id
  closeMenu()
  const done = value === 'sprint' ? `Sprint: agents may use everything left on ${pool.name} until it resets.` : value === 'hold' ? `Holding ${pool.name}. Running steps finish.` : `${pool.name} follows your work week again.`
  void run(() => capacity.setPoolOverride(id, value), done).then(() => nextTick(() => card.value?.querySelector<HTMLElement>(`[data-menu="${id}"]`)?.focus({ preventScroll: true })))
}

// ---------- Outside clicks ----------
function outside(event: MouseEvent) {
  const target = event.target as HTMLElement
  if (!target.isConnected) return
  if (menu.value && !menuEl.value?.contains(target) && !target.closest('[data-menu]')) closeMenu()
  if (editor.value && !sheet.value && !editorRef.value?.root?.contains(target) && !target.closest('.gear, .days') && !editorRef.value?.dirty()) closeEditor(false)
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
        <div class="setting nights">
          <span id="nights-lbl" class="lbl" @click="toggleNights">Agents at night</span>
          <button class="tog" type="button" role="switch" :aria-checked="schedule.nights" aria-labelledby="nights-lbl" :disabled="!mayManage || busy" :data-tip="mayManage ? undefined : manageTip" @click="toggleNights" />
          <span class="cap-hint" :class="{ off: !schedule.nights }" :title="schedule.nights ? `Agents also work outside your hours: ${nightLabel(schedule)}` : 'Agents follow your work hours only'">{{ nightLabel(schedule) }}</span>
          <button class="gear" type="button" aria-haspopup="dialog" :aria-expanded="editor?.kind === 'night'" aria-label="Customize night and shifts" :data-tip="mayManage ? 'Customize night and shifts' : manageTip" :disabled="!mayManage" @click="openEditor('night', $event)"><AppIcon name="gear" :size="15" /></button>
        </div>
      </div>
      <RouterLink class="manage" to="/settings/accounts" data-tip="Settings → Accounts: sign-ins, names, which accounts agents may use"><AppIcon name="sliders" :size="15" /><span>Manage<span class="long"> accounts</span></span></RouterLink>
    </div>

    <p v-if="capacity.loaded && !pools.length" class="empty">No accounts yet. Sign in to a harness on a connected computer and it appears here.</p>
    <p v-else-if="!capacity.loaded && capacity.state === 'error'" class="empty">Capacity could not be loaded. <button type="button" class="btn sm" @click="capacity.load()">Try again</button></p>

    <div v-for="pool in pools" :key="pool.id" class="pool" :data-pool="pool.id">
      <div class="pool-info">
        <div class="pool-head">
          <span class="vendor"><HarnessMark :harness="pool.id" :size="16" /></span>
          <span class="pool-name">{{ pool.name }}</span>
          <span v-if="pool.plan" class="pool-plan">{{ pool.plan }}</span>
          <span v-if="pool.override" class="override" :class="{ hold: pool.override === 'hold' }">
            {{ pool.override === 'hold' ? 'On hold' : 'Sprint until reset' }}
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
            <span v-if="row.host" class="chip host">{{ row.host }}</span>
          </div>
          <div class="gauge-cell">
            <div
              v-if="row.primary" class="gauge" :class="{ frozen: gaugeOf(row, planOf(row)).frozen, used: modeOf(row) === 'used' }" role="meter" aria-valuemin="0" aria-valuemax="100"
              :aria-valuenow="Math.round(figure(row))" :aria-label="gaugeLabel(row)" :data-tip="gaugeLabel(row)"
            >
              <div class="track">
                <i class="g-later" :style="{ left: '0', width: `${gaugeOf(row, planOf(row)).later}%` }" />
                <i class="g-today" :style="{ left: `${gaugeOf(row, planOf(row)).later}%`, width: `${gaugeOf(row, planOf(row)).today}%` }" />
                <i class="g-spent" :style="{ left: `${row.primary.remaining_percent}%`, width: `${gaugeOf(row, planOf(row)).spent}%` }" />
              </div>
              <b v-if="gaugeOf(row, planOf(row)).tick !== null" class="g-tick" :style="{ left: `${gaugeOf(row, planOf(row)).tick}%` }" />
            </div>
            <div v-else class="gauge empty"><div class="track" /></div>
            <div v-if="row.five" class="win5">5-hour window <b>{{ Math.round(modeOf(row) === 'used' ? 100 - row.five.remaining_percent : row.five.remaining_percent) }}% {{ modeOf(row) }}</b> · resets {{ when(row.five.reading.resets_at, now) }}</div>
          </div>
          <button
            v-if="row.primary" type="button" class="left" :aria-label="`${row.name}: ${Math.round(figure(row))}% ${modeOf(row)}. Show % ${modeOf(row) === 'left' ? 'used' : 'left'} for this account`"
            :data-tip="`Show % ${modeOf(row) === 'left' ? 'used' : 'left'} for ${row.name}`" @click="toggleMode(row)"
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
            <span v-else class="today quiet">{{ cell.text }}</span>
          </template>
          <span class="resets" :data-tip="row.primary ? whenFull(row.primary.reading.resets_at) : undefined">{{ row.primary ? `resets ${when(row.primary.reading.resets_at, now)}` : '' }}</span>
          <span class="source" :title="sourceLine(row, now)">{{ sourceLine(row, now) }}</span>
        </li>
      </ul>
    </div>

    <div v-if="pools.length" class="cap-foot">
      <span class="legend"><span class="sw"><i class="g-today" /></span>today's share</span>
      <span class="legend"><span class="sw"><i class="g-later" /></span>later days</span>
      <span class="legend"><span class="sw"><i class="g-spent" /></span>used today</span>
      <span class="legend"><span class="sw tick" />stop here tonight</span>
      <span class="fine">Your own use counts toward today's share too. Each account ends at 0% at its reset.</span>
      <span class="gauge-mode">
        <span id="gauge-mode-lbl" class="sr-only">Gauges show</span>
        <span class="seg" role="radiogroup" aria-labelledby="gauge-mode-lbl">
          <button v-for="m in (['left', 'used'] as const)" :key="m" type="button" role="radio" :aria-checked="globalMode === m" :data-tip="`Show % ${m} on every gauge`" @click="setGlobal(m)">% {{ m }}</button>
        </span>
      </span>
    </div>

    <div v-if="menu" ref="menuEl" class="menu" role="menu" :aria-label="`${menu.pool.name}: sprint or hold`" :style="menu.style" @keydown="menuKeys">
      <button v-for="item in menuItems(menu.pool)" :key="item.value" type="button" role="menuitem" @click="setOverride(menu.pool, item.value)">
        <AppIcon :name="item.icon" :size="16" /><span class="t">{{ item.title }}</span><span class="d">{{ item.desc }}</span>
      </button>
    </div>

    <ScheduleEditor
      v-if="editor && !sheet" ref="editorRef" :key="editor.kind" :kind="editor.kind" :schedule="schedule" :sheet="false" :now="now" :accounts="capacity.inputs" :pools="pools" :timezone="capacity.timezone" :saving="saving"
      :style="editorStyle" @close="closeEditor()" @save="saveEditor"
    />
    <Teleport to="body">
      <div v-if="editor && sheet" class="sheet-host">
        <div class="scrim" @click="closeEditor()" />
        <ScheduleEditor
          ref="editorRef" :key="editor.kind" :kind="editor.kind" :schedule="schedule" :sheet="true" :now="now" :accounts="capacity.inputs" :pools="pools" :timezone="capacity.timezone" :saving="saving"
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
.pool-plan { min-width: 0; overflow: hidden; text-overflow: ellipsis; color: var(--ink-3); font-size: 12.5px; white-space: nowrap; }
.pool-head .more { flex: none; width: 32px; height: 32px; margin-left: auto; }
.override { display: inline-flex; align-items: center; gap: 4px; height: 24px; padding: 0 2px 0 9px; border-radius: 999px; background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); color: var(--teal-ink); font-size: 12px; font-weight: 600; white-space: nowrap; }
.override.hold { background: var(--surface-sunken); box-shadow: inset 0 0 0 1px var(--line-2); color: var(--ink-2); }
.override button { display: grid; place-items: center; width: 20px; height: 20px; padding: 0; border: 0; border-radius: 50%; background: transparent; color: inherit; }
.override button:hover { background: var(--row-hover); }
.override button:focus-visible { box-shadow: var(--focus-ring); }
.plan { margin-top: 8px; padding-left: 40px; color: var(--ink-2); font-size: 13.5px; line-height: 1.5; text-wrap: pretty; }
.plan :deep(b) { color: var(--ink); font-weight: 600; }
.plan :deep(.n) { color: var(--teal-ink); font-weight: 700; font-variant-numeric: tabular-nums; }
.plan.ahead :deep(.n) { color: var(--gold-ink); }

.accts { display: grid; gap: 2px; align-self: start; margin: -6px 0 0; padding: 0; list-style: none; }
.acct { display: grid; grid-template-columns: 170px minmax(160px, 1fr) 74px 124px 150px 204px; align-items: center; gap: 16px; min-height: 44px; padding: 6px 10px; border-radius: var(--radius-row); }
@media (hover: hover) { .acct:hover { background: var(--row-hover); } }
.acct-name { display: flex; align-items: center; gap: 9px; min-width: 0; }
.acct-name .nm { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--ink); font-size: 13.5px; font-weight: 600; }
.host { flex: none; }
.dot { position: relative; flex: none; width: 8px; height: 8px; border-radius: 50%; }
.dot.live, .dot.unread { background: var(--ok); box-shadow: 0 0 0 3px color-mix(in srgb, var(--ok) 18%, transparent); }
.dot.offline, .dot.paused { background: transparent; box-shadow: inset 0 0 0 1.6px var(--ink-3); }
.dot.signin { background: var(--gold); box-shadow: 0 0 0 3px color-mix(in srgb, var(--gold) 22%, transparent); }
.left { justify-self: end; padding: 2px 4px; margin: -2px -4px; border: 0; border-radius: 6px; background: transparent; text-align: right; white-space: nowrap; }
button.left { cursor: pointer; }
@media (hover: hover) { button.left:hover { background: var(--row-hover); } }
button.left:focus-visible { box-shadow: var(--focus-ring); }
.left b { color: var(--ink); font-size: 15px; font-weight: 700; font-variant-numeric: tabular-nums; }
.left span { margin-left: 3px; color: var(--ink-3); font-size: 12px; }
.today { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--ink-2); font-size: 13px; font-variant-numeric: tabular-nums; }
.today b { color: var(--teal-ink); font-weight: 700; }
.today.ahead b { color: var(--gold-ink); }
.today.quiet, .today .quiet { color: var(--ink-3); }
.today .btn { height: 26px; padding: 0 10px; font-size: 12px; }
.resets { color: var(--ink-2); font-size: 13px; white-space: nowrap; font-variant-numeric: tabular-nums; }
.source { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--ink-3); font-size: 12px; text-align: right; }
.acct.dim .gauge, .acct.dim .left, .acct.dim .resets { opacity: .6; }
.win5 { margin-top: 5px; color: var(--ink-3); font-size: 11.5px; font-variant-numeric: tabular-nums; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.win5 b { color: var(--ink-2); font-weight: 600; }

/* The gauge: what is left, today's share at its end, the stop tick, and today's spend as a hatch.
   "% used" mirrors it, so the empty part on the left is what has been used. */
.gauge { position: relative; height: 14px; min-width: 0; }
.gauge.used { transform: scaleX(-1); }
.track { position: absolute; inset: 3px 0; border-radius: 999px; background: var(--track); overflow: hidden; box-shadow: inset 0 1px 2px rgba(32, 60, 61, .08); }
.track i { position: absolute; top: 0; bottom: 0; }
.g-later { background: color-mix(in srgb, var(--teal) 32%, transparent); }
.g-today { background: linear-gradient(90deg, color-mix(in srgb, var(--teal) 88%, var(--aqua)), var(--teal)); }
.g-spent { background: repeating-linear-gradient(135deg, color-mix(in srgb, var(--teal) 55%, transparent) 0 1.5px, transparent 1.5px 4.5px); }
.acct.ahead .g-spent { background: repeating-linear-gradient(135deg, color-mix(in srgb, var(--gold) 75%, transparent) 0 1.5px, transparent 1.5px 4.5px); }
.g-tick { position: absolute; top: 0; width: 2px; height: 14px; margin-left: -1px; border-radius: 2px; background: var(--ink); box-shadow: 0 0 0 1.5px var(--surface-raised); }
.acct.ahead .g-tick { background: var(--gold-ink); }
.acct.dim .g-later, .gauge.frozen .g-later { background: color-mix(in srgb, var(--ink-3) 30%, transparent); }
.gauge.frozen { opacity: .6; }

.cap-foot { display: flex; align-items: center; gap: 18px; flex-wrap: wrap; padding: 11px 20px 13px; border-top: 1px solid var(--line); color: var(--ink-3); font-size: 12px; }
.legend { display: inline-flex; align-items: center; gap: 7px; white-space: nowrap; }
.sw { position: relative; display: inline-block; width: 18px; height: 8px; border-radius: 999px; overflow: hidden; background: var(--track); }
.sw i { position: absolute; inset: 0; }
.sw.tick { width: 2px; height: 12px; border-radius: 2px; background: var(--ink); overflow: visible; }
.fine { margin-left: auto; }
.gauge-mode .seg button { height: 24px; min-width: 0; padding: 0 9px; font-size: 11.5px; }

.menu { position: absolute; z-index: 20; width: 300px; padding: 6px; border-radius: 12px; background: var(--surface-raised); border: 1px solid var(--glass-edge); box-shadow: var(--shadow-pop); }
.menu button { display: grid; grid-template-columns: 20px 1fr; gap: 2px 10px; width: 100%; padding: 9px 10px; border: 0; border-radius: 8px; background: transparent; color: var(--ink); text-align: left; }
.menu button:hover, .menu button:focus-visible { background: var(--row-hover); box-shadow: none; outline: none; }
.menu button svg { grid-row: span 2; margin-top: 2px; color: var(--ink-2); }
.menu .t { color: var(--ink); font-size: 13.5px; font-weight: 600; }
.menu .d { color: var(--ink-3); font-size: 12px; line-height: 1.4; }
.sheet-host { position: fixed; inset: 0; z-index: 80; }
.scrim { position: absolute; inset: 0; background: var(--scrim); }
@media (prefers-reduced-motion: reduce) { .tog::after { transition: none; } }

/* Mid width: the plan sits above its accounts. */
@media (max-width: 1320px) {
  .pool { grid-template-columns: minmax(0, 1fr); gap: 10px; }
  .acct { grid-template-columns: 180px minmax(140px, 1fr) 72px 120px 124px 180px; }
  .plan { padding-left: 40px; }
}
@media (max-width: 1100px) {
  .acct { grid-template-columns: 150px minmax(120px, 1fr) 68px 110px 120px; }
  .source { display: none; }
}
/* Phone: stacked rows, the settings as one sunken group. */
@media (max-width: 720px) {
  .cap-head { gap: 10px; padding: 14px 14px 12px; }
  .cap-title { order: 1; flex: 1; margin: 0; }
  .manage { order: 2; height: 44px; margin-right: -6px; }
  .manage .long { display: none; }
  .settings { order: 3; display: grid; grid-template-columns: minmax(0, 1fr); gap: 0; width: 100%; padding: 0 12px; border-radius: 12px; background: var(--surface-sunken); }
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
  .setting .seg button { height: 38px; min-width: 44px; }
  .tog { width: 44px; height: 26px; }
  .tog::after { width: 20px; height: 20px; }
  .tog[aria-checked="true"]::after { transform: translateX(18px); }
  .pool { gap: 8px; padding: 14px 14px 12px; }
  .plan { padding-left: 0; }
  .pool-head .more { width: 44px; height: 44px; margin-right: -8px; }
  .acct { grid-template-columns: minmax(0, 1fr) auto; grid-template-areas: "name left" "gauge gauge" "today resets" "source source"; gap: 4px 10px; padding: 10px 0; border-radius: 0; }
  .acct + .acct { box-shadow: 0 -1px 0 var(--line); }
  .acct:hover { background: transparent; }
  .acct-name { grid-area: name; }
  .gauge-cell { grid-area: gauge; margin: 2px 0; }
  .left { grid-area: left; }
  .today { grid-area: today; }
  .resets { grid-area: resets; text-align: right; color: var(--ink-3); font-size: 12.5px; }
  .source { grid-area: source; display: block; text-align: left; font-size: 11.5px; }
  .today .btn { min-height: 36px; }
  .cap-foot { gap: 8px 14px; padding: 12px 14px; }
  .fine { width: 100%; margin-left: 0; }
  .menu { width: min(300px, calc(100% - 24px)); }
  .menu button { min-height: 52px; }
}
</style>
