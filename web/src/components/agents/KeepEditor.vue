<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import {
  AUTO_RESERVE, RESERVE_MAX, RESERVE_MIN, RESERVE_STEP, buildPools, buildRows, clone, localDate, plainText, poolSentence, previewCapacity, reserveLabel, when, whenFull, withReserve, workStart, zonedInstant,
  type AccountInput, type CapacitySchedule, type Pool, type PoolReserve, type PoolView, type ReserveMode, type Sentence,
} from '../../lib/capacity'
import { brand } from '../../lib/brand'
import AppIcon from '../AppIcon.vue'
import HarnessMark from './HarnessMark.vue'
import PlanSentence from './PlanSentence.vue'

// Keep for you (AEON-375): the one choice. How much of every limit agents leave
// the person while they work; it shrinks as a reset comes close. Auto by default,
// a fixed share, or nothing; per vendor when wanted; and "I'm away until…". A
// popover on wide screens, a bottom sheet on phones, like the two gear editors.
// Nothing applies until Save; the preview asks the server to pace the draft.
export interface KeepDraft { reserve: ReserveMode; percent?: number; away: string; pools: Record<string, { reserve: ReserveMode; percent?: number }> }
const props = defineProps<{
  schedule: CapacitySchedule; away: string; poolReserves: Record<string, { reserve: ReserveMode; percent?: number }>
  sheet: boolean; now: number; accounts: AccountInput[]; pools: PoolView[]; timezone: string; saving: boolean
}>()
const emit = defineEmits<{ close: []; save: [draft: KeepDraft] }>()

type Mode = 'auto' | 'fixed' | 'off'
const startMode: Mode = props.schedule.reserve === 'fixed' || props.schedule.reserve === 'off' ? props.schedule.reserve : 'auto'
const mode = ref<Mode>(startMode)
const percent = ref(props.schedule.reserve === 'fixed' ? props.schedule.reserve_percent ?? AUTO_RESERVE : AUTO_RESERVE)
// A pool's choice as one select value: '' follows the person, auto, off, or a share.
const poolValue = (r?: { reserve: ReserveMode; percent?: number }) => (!r?.reserve ? '' : r.reserve === 'fixed' ? String(r.percent) : r.reserve)
const perPool = ref<Record<string, string>>(Object.fromEntries(props.pools.map(p => [p.id, poolValue(props.poolReserves[p.id])])))
const awayUntil = ref(props.away)
const vendorsOpen = ref(Object.values(perPool.value).some(Boolean))
const root = ref<HTMLElement>()
const title = ref<HTMLElement>()
const initial = JSON.stringify([mode.value, percent.value, perPool.value, awayUntil.value])
const dirty = computed(() => JSON.stringify([mode.value, percent.value, perPool.value, awayUntil.value]) !== initial)
defineExpose({ dirty: () => dirty.value, root, focusTitle: () => title.value?.focus({ preventScroll: true }) })

// ---------- The share ----------
const SHARES = Array.from({ length: (RESERVE_MAX - RESERVE_MIN) / RESERVE_STEP + 1 }, (_, i) => RESERVE_MIN + i * RESERVE_STEP)
function step(by: number) { percent.value = Math.max(RESERVE_MIN, Math.min(RESERVE_MAX, percent.value + by)); mode.value = 'fixed' }
function stepKeys(event: KeyboardEvent) {
  const moves: Record<string, number> = { ArrowUp: RESERVE_STEP, ArrowRight: RESERVE_STEP, ArrowDown: -RESERVE_STEP, ArrowLeft: -RESERVE_STEP, PageUp: 4 * RESERVE_STEP, PageDown: -4 * RESERVE_STEP, Home: -RESERVE_MAX, End: RESERVE_MAX }
  if (!(event.key in moves)) return
  event.preventDefault()
  step(moves[event.key])
}
const personLabel = computed(() => reserveLabel(mode.value === 'fixed' ? { reserve: 'fixed', reserve_percent: percent.value } : { reserve: mode.value }))
const BLIND = new Set(['grok', 'cursor', 'pi'])
const vendorRows = computed(() => props.pools.filter(p => !p.id.startsWith('group:')).map(p => {
  const measured = p.rows.some(r => r.primary)
  const note = measured ? '' : BLIND.has(p.mark || p.id) ? `${p.name} doesn't show its limit · one run at a time by day` : 'No reading yet'
  const learned = p.rows.some(r => r.learning?.windows.some(w => w.auto_reserve_percent))
  return { id: p.id, mark: p.mark || p.id, name: p.name, measured, note, auto: learned ? 'Auto · learned per account' : `Auto · ~${AUTO_RESERVE}%` }
}))

// ---------- Away ----------
const tomorrow = computed(() => workStart(props.schedule, props.now, 1))
const monday = computed(() => {
  const next = workStart(props.schedule, props.now, 1, 0)
  return next === tomorrow.value ? workStart(props.schedule, props.now, 8, 0) : next
})
const cap = (s: string) => s.charAt(0).toUpperCase() + s.slice(1)
const awayLabel = (at: number) => (at - props.now < 5.5 * 864e5 ? cap(when(at, props.now, props.timezone)) : whenFull(new Date(at).toISOString(), props.timezone))
const awayOptions = computed(() => [tomorrow.value, monday.value].map(at => ({ at: new Date(at).toISOString(), label: awayLabel(at) })))
const minDate = computed(() => localDate(props.now + 864e5, props.timezone))
const maxDate = computed(() => localDate(props.now + 364 * 864e5, props.timezone))
function pickDate(value: string) {
  const [y, m, d] = value.split('-').map(Number)
  if (!y || !m || !d) return
  const wd = (new Date(Date.UTC(y, m - 1, d)).getUTCDay() + 6) % 7
  const day = props.schedule.week[wd]
  const at = zonedInstant(y, m - 1, d, day.on ? day.start : (props.schedule.week.find(x => x.on)?.start ?? 8), props.timezone)
  if (at > props.now) awayUntil.value = new Date(at).toISOString()
}
const awayPicked = computed(() => !!awayUntil.value && !awayOptions.value.some(o => Date.parse(o.at) === Date.parse(awayUntil.value)))
const toggleAway = (at: string) => { awayUntil.value = Date.parse(awayUntil.value) === Date.parse(at) ? '' : at }

// ---------- The draft and its preview ----------
function poolDraft(value: string): { reserve: ReserveMode; percent?: number } {
  if (value === 'auto' || value === 'off' || value === '') return { reserve: value }
  return { reserve: 'fixed', percent: Number(value) }
}
// pool_reserves on the preview wire is reserve_percent (OpenAPI); percent is only the editor's own draft.
function previewPool(id: Pool, value: string): PoolReserve {
  const choice = poolDraft(value)
  if (choice.reserve !== 'fixed') return { pool: id, reserve: choice.reserve }
  return { pool: id, reserve: 'fixed', reserve_percent: choice.percent }
}
const draftSchedule = computed<CapacitySchedule>(() => {
  const s = withReserve(clone(props.schedule), mode.value, mode.value === 'fixed' ? percent.value : undefined)
  return awayUntil.value ? { ...s, override: 'away', override_until: awayUntil.value } : s
})
const draftPools = computed(() => vendorRows.value.filter(v => v.measured).map(v => previewPool(v.id as Pool, perPool.value[v.id] ?? '')))
const currentSentences = computed(() => props.pools.filter(p => !p.id.startsWith('group:')).map(p => ({ id: p.id, mark: p.mark || p.id, sentence: poolSentence(p, props.now) })))
const previewPools = ref<PoolView[] | null>(null)
const previewFailed = ref(false)
let previewTimer: ReturnType<typeof setTimeout> | undefined
let previewTurn = 0
watch([draftSchedule, draftPools], () => {
  clearTimeout(previewTimer)
  const turn = ++previewTurn
  previewTimer = setTimeout(async () => {
    try {
      const result = await previewCapacity(draftSchedule.value, draftPools.value)
      if (turn !== previewTurn) return
      previewPools.value = buildPools(buildRows(props.accounts, result), props.now)
      previewFailed.value = false
    } catch { if (turn === previewTurn) previewFailed.value = true }
  }, 220)
}, { deep: true })
const preview = computed(() => currentSentences.value.map(({ id, mark, sentence }) => {
  const pool = previewPools.value?.find(p => p.id === id)
  const next: Sentence = pool && dirty.value ? poolSentence(pool, props.now) : sentence
  return { id, mark, sentence: next, changed: plainText(next) !== plainText(sentence) }
}))

// ---------- Save, close, focus ----------
function save() {
  if (props.saving) return
  const pools: KeepDraft['pools'] = {}
  for (const v of vendorRows.value) if (v.measured && (perPool.value[v.id] ?? '') !== poolValue(props.poolReserves[v.id])) pools[v.id] = poolDraft(perPool.value[v.id] ?? '')
  emit('save', { reserve: mode.value, ...(mode.value === 'fixed' ? { percent: percent.value } : {}), away: awayUntil.value, pools })
}
function reset() {
  mode.value = 'auto'
  percent.value = AUTO_RESERVE
  for (const id of Object.keys(perPool.value)) perPool.value[id] = ''
  awayUntil.value = ''
}
function keydown(event: KeyboardEvent) {
  if (event.key === 'Escape') { event.preventDefault(); event.stopPropagation(); emit('close'); return }
  if (props.sheet && event.key === 'Tab') trapTab(event)
}
function focusables() {
  return [...(root.value?.querySelectorAll<HTMLElement>('button:not(:disabled), input:not(:disabled), select:not(:disabled), [tabindex="0"]') ?? [])].filter(x => x.offsetParent !== null)
}
function trapTab(event: KeyboardEvent) {
  const list = focusables()
  if (!list.length) return
  const i = list.indexOf(document.activeElement as HTMLElement)
  const next = event.shiftKey ? (i <= 0 ? list[list.length - 1] : null) : (i === -1 || i === list.length - 1 ? list[0] : null)
  if (next) { event.preventDefault(); next.focus() }
}
function holdFocus(event: FocusEvent) {
  if (props.sheet && root.value && event.target instanceof Node && !root.value.contains(event.target)) (focusables()[0] ?? title.value)?.focus({ preventScroll: true })
}
onMounted(() => { title.value?.focus({ preventScroll: true }); document.addEventListener('focusin', holdFocus) })
onBeforeUnmount(() => { clearTimeout(previewTimer); document.removeEventListener('focusin', holdFocus) })
const zoneNote = computed(() => { try { return Intl.DateTimeFormat().resolvedOptions().timeZone !== props.timezone ? ` Times in ${props.timezone}.` : '' } catch { return '' } })
</script>

<template>
  <div ref="root" class="ed keep" :class="{ sheet }" role="dialog" :aria-modal="sheet" aria-labelledby="keep-title" @keydown="keydown">
    <div v-if="sheet" class="grab" aria-hidden="true" />
    <div class="ed-head">
      <h3 id="keep-title" ref="title" tabindex="-1">Keep for you</h3>
      <button class="icon-btn flat ed-x" type="button" aria-label="Close without saving" @click="emit('close')"><AppIcon name="close" :size="16" /></button>
    </div>
    <div class="ed-foot">
      <button class="btn ghost" type="button" @click="reset">Reset to default</button>
      <span class="sp" />
      <button class="btn" type="button" @click="emit('close')">Cancel</button>
      <button class="btn primary" type="button" :disabled="saving" @click="save">{{ saving ? 'Saving…' : 'Save' }}</button>
    </div>
    <div class="ed-body">
      <p class="ed-help">While you work, agents leave you this much of every limit, less as a reset comes close: after it the window is new anyway.{{ zoneNote }}</p>
      <fieldset class="modes">
        <legend class="sr-only">How much agents leave you</legend>
        <div class="opt" :class="{ on: mode === 'auto' }">
          <label><input v-model="mode" type="radio" name="keep-mode" value="auto"><span class="t">Auto</span><span class="d">What you usually use yourself; ~{{ AUTO_RESERVE }}% until {{ brand.short_name }} has seen a week of it.</span></label>
        </div>
        <div class="opt fixed" :class="{ on: mode === 'fixed' }">
          <label><input v-model="mode" type="radio" name="keep-mode" value="fixed"><span class="t">A fixed share</span></label>
          <span class="stepper">
            <button type="button" class="step" aria-label="Keep 5% less" :disabled="mode === 'fixed' && percent <= RESERVE_MIN" @click="step(-RESERVE_STEP)"><AppIcon name="minus" :size="14" /></button>
            <span
              class="val" role="spinbutton" tabindex="0" aria-label="Fixed share" :aria-valuenow="percent" :aria-valuemin="RESERVE_MIN" :aria-valuemax="RESERVE_MAX" :aria-valuetext="`${percent}%`"
              @keydown="stepKeys" @focus="mode = 'fixed'"
            >{{ percent }}%</span>
            <button type="button" class="step" aria-label="Keep 5% more" :disabled="mode === 'fixed' && percent >= RESERVE_MAX" @click="step(RESERVE_STEP)"><AppIcon name="plus" :size="14" /></button>
          </span>
        </div>
        <div class="opt" :class="{ on: mode === 'off' }">
          <label><input v-model="mode" type="radio" name="keep-mode" value="off"><span class="t">Nothing</span><span class="d">Agents may use everything.</span></label>
        </div>
      </fieldset>

      <div class="vendors">
        <button type="button" class="disclose" :aria-expanded="vendorsOpen" aria-controls="keep-vendors" @click="vendorsOpen = !vendorsOpen">
          <span>Per vendor</span><AppIcon name="chevron" :size="14" />
        </button>
        <ul v-show="vendorsOpen" id="keep-vendors" class="vrows">
          <li v-for="v in vendorRows" :key="v.id">
            <HarnessMark :harness="v.mark" :size="14" />
            <span class="vn">{{ v.name }}</span>
            <select v-if="v.measured" v-model="perPool[v.id]" class="sel" :aria-label="`${v.name}: keep for you`">
              <option value="">Same as above · {{ personLabel }}</option>
              <option value="auto">{{ v.auto }}</option>
              <option v-for="n in SHARES" :key="n" :value="String(n)">{{ n }}%</option>
              <option value="off">Nothing</option>
            </select>
            <span v-else class="vnote">{{ v.note }}</span>
          </li>
        </ul>
      </div>

      <div class="away" role="group" aria-labelledby="away-lbl">
        <span id="away-lbl" class="sec-t">I'm away until…</span>
        <div class="chips">
          <button v-for="o in awayOptions" :key="o.at" type="button" class="chip-btn" :aria-pressed="Date.parse(awayUntil) === Date.parse(o.at)" @click="toggleAway(o.at)">{{ o.label }}</button>
          <label class="chip-btn date" :class="{ picked: awayPicked }">
            <AppIcon name="calendar" :size="14" /><span>{{ awayPicked ? awayLabel(Date.parse(awayUntil)) : 'Pick a date' }}</span>
            <input type="date" :min="minDate" :max="maxDate" aria-label="Away until a date" @change="pickDate(($event.target as HTMLInputElement).value)">
          </label>
        </div>
        <p v-if="awayUntil" class="ed-help away-on">Agents use everything left until {{ when(awayUntil, now, timezone) }}, across resets. <button type="button" class="link" @click="awayUntil = ''">Not away</button></p>
      </div>

      <div class="pv" aria-live="polite">
        <p class="eyebrow">Today with these settings</p>
        <ul>
          <li v-for="item in preview" :key="item.id" :class="{ changed: item.changed }">
            <HarnessMark :harness="item.mark" :size="13" />
            <span><PlanSentence :sentence="item.sentence" /><span v-if="item.changed" class="sr-only"> (changed)</span></span>
          </li>
          <li v-if="!preview.length" class="none">No accounts to plan yet.</li>
        </ul>
        <p v-if="previewFailed" class="ed-help">The preview could not be updated; Save still applies these settings.</p>
      </div>
    </div>
  </div>
</template>

<style scoped>
.ed { position: absolute; z-index: 30; display: flex; flex-direction: column; width: 540px; max-width: calc(100vw - 24px); max-height: min(720px, calc(100dvh - 96px)); border-radius: 16px; background: var(--surface-raised); border: 1px solid var(--glass-edge); box-shadow: var(--shadow-pop); color: var(--ink); text-align: left; }
.grab { width: 36px; height: 5px; margin: 8px auto 0; border-radius: 3px; background: var(--line-2); }
.ed-head { display: flex; align-items: center; gap: 10px; padding: 14px 12px 2px 20px; }
.ed-head h3 { margin: 0 auto 0 0; font: 600 16px/1.3 var(--font); color: var(--ink); }
.ed-head h3:focus, .ed-head h3:focus-visible { outline: none; box-shadow: none; }
.ed-body { flex: 1; scrollbar-gutter: stable; align-content: start; display: grid; gap: 16px; padding: 2px 20px 18px; min-height: 0; overflow-y: auto; overscroll-behavior: contain; }
.ed p { margin: 0; }
.ed-help { color: var(--ink-3); font-size: 12.5px; line-height: 1.45; }
.ed-foot { flex: none; display: flex; align-items: center; gap: 8px; padding: 12px 16px; border-bottom: 1px solid var(--line); }
.ed-foot .sp { flex: 1; }
.ed-foot .btn.primary { min-width: 88px; }
.btn:disabled { opacity: .5; cursor: default; filter: none; }
.sec-t { color: var(--ink); font-size: 13px; font-weight: 650; }

/* The one choice: three quiet cards, the chosen one tinted with a hairline ring. */
.modes { display: grid; gap: 6px; margin: 0; padding: 0; border: 0; min-width: 0; }
.opt { position: relative; display: flex; align-items: center; gap: 10px; min-height: 48px; padding: 0 12px 0 0; border-radius: 12px; background: var(--surface-sunken); box-shadow: inset 0 0 0 1px var(--line); }
@media (hover: hover) { .opt:hover { background: var(--row-hover); } }
.opt.on { background: var(--row-selected); box-shadow: inset 0 0 0 1px var(--chip-teal-line); }
.opt:has(input:focus-visible) { box-shadow: inset 0 0 0 1px var(--chip-teal-line), var(--focus-ring); }
.opt label { flex: 1; display: grid; grid-template-columns: auto minmax(0, 1fr); column-gap: 10px; align-items: center; min-width: 0; padding: 10px 0 10px 13px; cursor: pointer; }
.opt input { grid-row: span 2; width: 16px; height: 16px; margin: 0; accent-color: #0e6f6c; }
.opt input:focus-visible { outline: none; box-shadow: none; }
.opt .t { color: var(--ink); font-size: 13.5px; font-weight: 650; }
.opt .d { color: var(--ink-3); font-size: 12px; line-height: 1.35; }
.stepper { display: inline-flex; align-items: center; gap: 2px; padding: 2px; border-radius: 999px; background: var(--field-bg); box-shadow: inset 0 0 0 1px var(--line-2); }
.step { display: grid; place-items: center; width: 28px; height: 28px; padding: 0; border: 0; border-radius: 50%; background: transparent; color: var(--ink-2); }
@media (hover: hover) { .step:hover:not(:disabled) { background: var(--row-hover); color: var(--ink); } }
.step:disabled { opacity: .4; cursor: default; }
.step:focus-visible, .val:focus-visible { outline: none; box-shadow: var(--focus-ring); }
.val { min-width: 44px; padding: 0 2px; border-radius: 6px; color: var(--teal-ink); font: 650 13px var(--mono); font-variant-numeric: tabular-nums; text-align: center; }
.opt:not(.on) .val { color: var(--ink-2); }

.vendors { display: grid; gap: 6px; }
.disclose { display: inline-flex; align-items: center; gap: 6px; justify-self: start; height: 30px; padding: 0 8px; margin-left: -8px; border: 0; border-radius: 8px; background: transparent; color: var(--ink); font-size: 13px; font-weight: 650; }
.disclose svg { color: var(--ink-3); transition: transform .15s ease; transform: rotate(-90deg); }
.disclose[aria-expanded="true"] svg { transform: none; }
@media (hover: hover) { .disclose:hover { background: var(--row-hover); } }
.disclose:focus-visible { box-shadow: var(--focus-ring); }
.vrows { max-height: 184px; min-height: 0; overflow: auto; align-content: start; display: grid; gap: 2px; margin: 0; padding: 0; list-style: none; }
.vrows li { display: grid; grid-template-columns: 16px 72px minmax(0, 1fr); align-items: center; gap: 10px; min-height: 36px; color: var(--ink-2); font-size: 13px; }
.vrows li :deep(.mark) { color: var(--ink-2); }
.vn { color: var(--ink); font-weight: 600; }
.vnote { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--ink-3); font-size: 12.5px; }
.sel { -webkit-appearance: none; appearance: none; justify-self: start; max-width: 100%; height: 30px; padding: 0 26px 0 10px; border: 0; border-radius: 8px; background: var(--field-bg) url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 16 16' fill='none' stroke='%237a8c8d' stroke-width='2' stroke-linecap='round' stroke-linejoin='round'%3E%3Cpath d='m4.5 6.3 3.5 3.5 3.5-3.5'/%3E%3C/svg%3E") no-repeat right 8px center / 11px; box-shadow: inset 0 0 0 1px var(--line-2); color: var(--ink); font: 500 12.5px var(--font); cursor: pointer; }
.sel:focus-visible { outline: none; box-shadow: var(--focus-ring); }

.away { display: grid; gap: 8px; }
.chips { display: flex; flex-wrap: wrap; gap: 6px; }
.chip-btn { position: relative; display: inline-flex; align-items: center; gap: 6px; height: 32px; padding: 0 12px; border: 0; border-radius: 999px; background: var(--surface-sunken); box-shadow: inset 0 0 0 1px var(--line-2); color: var(--ink-2); font-size: 12.5px; font-weight: 550; white-space: nowrap; cursor: pointer; }
@media (hover: hover) { .chip-btn:hover { background: var(--row-hover); color: var(--ink); } }
.chip-btn[aria-pressed="true"], .chip-btn.picked { background: var(--row-selected); box-shadow: inset 0 0 0 1px var(--chip-teal-line); color: var(--teal-ink); }
.chip-btn:focus-visible, .chip-btn.date:has(input:focus-visible) { outline: none; box-shadow: var(--focus-ring); }
.chip-btn.date input { position: absolute; inset: 0; width: 100%; height: 100%; opacity: 0; cursor: pointer; }
.chip-btn.date input::-webkit-calendar-picker-indicator { position: absolute; inset: 0; width: 100%; height: 100%; cursor: pointer; }
.away-on { color: var(--ink-2); }
.link { padding: 0; border: 0; background: none; color: var(--teal-ink); font: inherit; font-weight: 600; text-decoration: underline; text-underline-offset: 2px; cursor: pointer; }
.link:focus-visible { border-radius: 3px; box-shadow: var(--focus-ring); }

.pv { padding: 12px 14px; border-radius: 12px; background: var(--surface-sunken); }
.pv ul { display: grid; gap: 4px; margin: 6px 0 0; padding: 0; list-style: none; }
.pv li { display: grid; grid-template-columns: 16px minmax(0, 1fr); gap: 8px; margin: 0 -6px; padding: 3px 6px; border-radius: 6px; color: var(--ink-2); font-size: 12.5px; line-height: 1.45; }
.pv li.none { grid-template-columns: 1fr; color: var(--ink-3); }
.pv li.changed { background: var(--row-selected); }
.pv li :deep(.mark) { margin-top: 3px; color: var(--ink-2); }
.pv :deep(b) { color: var(--ink); font-weight: 600; }
.pv :deep(.n) { color: var(--teal-ink); font-weight: 700; font-variant-numeric: tabular-nums; }
@media (prefers-reduced-motion: reduce) { .disclose svg { transition: none; } }

/* Phone: a bottom sheet with sticky Cancel and Save. */
.ed.sheet { position: fixed; left: 0; right: 0; bottom: 0; top: auto; z-index: 81; width: auto; max-width: none; height: 100dvh; max-height: 100dvh; border-radius: 22px 22px 0 0; border-bottom: 0; }
.sheet .ed-head { padding: 4px 8px 2px 16px; }
.sheet .ed-body { order: 1; padding: 0 16px 18px; gap: 14px; }
.sheet .ed-foot { order: 2; border-bottom: 0; border-top: 1px solid var(--line); flex-wrap: wrap; padding: 10px 16px calc(16px + env(safe-area-inset-bottom)); }
.sheet .ed-foot .btn { min-height: 44px; }
.sheet .ed-foot .btn:not(.ghost) { flex: 1; }
.sheet .ed-foot .sp { display: none; }
.sheet .ed-foot .ghost { width: 100%; order: 3; min-height: 40px; }
.sheet .ed-x { width: 44px; height: 44px; }
.sheet .opt { min-height: 52px; }
.sheet .step { width: 40px; height: 40px; }
.sheet .sel { min-height: 40px; }
.sheet .chip-btn { height: 40px; }
.sheet .vrows li { grid-template-columns: 16px 60px minmax(0, 1fr); min-height: 44px; }
</style>
