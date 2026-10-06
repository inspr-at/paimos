<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import {
  buildPools, buildRows, clampRate, clone, DAYS, DAYS_LONG, dayProfile, daysSummary, defaultSchedule, fullPaceHours, hourLabel, nightLabel,
  plainText, poolSentence, previewCapacity, scheduleProblem, timeLabel, weekdayIn,
  type AccountInput, type CapacitySchedule, type NightModel, type OffDays, type PoolView, type RibbonCell, type Sentence,
} from '../../lib/capacity'
import AppIcon from '../AppIcon.vue'
import HarnessMark from './HarnessMark.vue'
import PlanSentence from './PlanSentence.vue'

// The two deep editors behind the gears: the work week, and what agents do outside
// it (no shifts, day and night, three shifts, or hour-by-hour blocks). A popover on
// wide screens, a bottom sheet on phones. Nothing applies until Save; the preview
// below asks the server to pace the draft, so it reads exactly like the card will.
const props = defineProps<{
  kind: 'week' | 'night'; schedule: CapacitySchedule; sheet: boolean; now: number
  accounts: AccountInput[]; pools: PoolView[]; timezone: string; saving: boolean
}>()
const emit = defineEmits<{ close: []; save: [schedule: CapacitySchedule] }>()

const draft = ref<CapacitySchedule>(clone(props.schedule))
const onDays = () => draft.value.week.filter(d => d.on)
const same = ref(onDays().every((d, _, all) => d.start === all[0].start && d.end === all[0].end))
const brush = ref<'full' | 'reduced' | 'off'>('full')
const reducedPace = ref(50)
const focusHour = ref(8)
const root = ref<HTMLElement>()
const title = ref<HTMLElement>()
const dirty = computed(() => JSON.stringify(draft.value) !== JSON.stringify(props.schedule))
const problem = computed(() => scheduleProblem(draft.value, props.kind))
defineExpose({ dirty: () => dirty.value, root, focusTitle: () => title.value?.focus({ preventScroll: true }) })

// ---------- Shape ----------
const tint = (k: number) => (k <= 0 ? 'transparent' : k >= 1 ? 'var(--teal)' : `color-mix(in srgb, var(--teal) ${Math.round(22 + k * 70)}%, transparent)`)
const cellStyle = (c: RibbonCell) => (c.expire ? {} : { background: tint(c.k) })
const todayIndex = computed(() => weekdayIn(new Date(props.now).toISOString(), props.timezone))
const nowPct = computed(() => {
  const p = new Intl.DateTimeFormat('en-GB', { timeZone: props.timezone, hour: '2-digit', minute: '2-digit', hourCycle: 'h23' }).formatToParts(new Date(props.now))
  const h = +(p.find(x => x.type === 'hour')?.value ?? 0), m = +(p.find(x => x.type === 'minute')?.value ?? 0)
  return ((h + m / 60) / 24) * 100
})
const profiles = computed(() => DAYS.map((_, i) => dayProfile(draft.value, i)))
const refDay = computed(() => (draft.value.week[todayIndex.value].on ? todayIndex.value : Math.max(0, draft.value.week.findIndex(d => d.on))))
const bigProfile = computed(() => dayProfile(draft.value, refDay.value))
const model = computed<'none' | NightModel>(() => (draft.value.nights ? draft.value.model : 'none'))
const MODELS: { id: 'none' | NightModel; title: string; desc: string }[] = [
  { id: 'none', title: 'No shifts', desc: 'Agents follow my work hours.' },
  { id: 'daynight', title: 'Day and night', desc: 'After work, agents keep going, usually gentler.' },
  { id: 'shifts', title: 'Three shifts', desc: 'Early, late and night, each with its own pace.' },
  { id: 'blocks', title: 'Custom blocks', desc: 'Paint the day hour by hour.' },
]
const OFF_HELP: Record<OffDays, string> = {
  rest: 'Agents rest on days off. Capacity that resets before your next work day is lost.',
  expire: 'Agents rest on days off, except to use capacity that would otherwise reset unused.',
  normal: 'Agents work days off like work days, at the hours set for that day.',
}
const times = (from: number, to: number) => { const out: number[] = []; for (let v = from; v <= to; v += 0.5) out.push(v); return out }
const STARTS = times(0, 23.5), ENDS = times(0.5, 24)
const weekSummary = computed(() => { const n = onDays().length; return `${n} ${n === 1 ? 'day' : 'days'} · ${daysSummary(draft.value.week)}` })
const reducedK = computed(() => {
  const d = draft.value
  if (!d.nights) return 1
  if (d.model === 'shifts') return Math.min(1, ...d.shifts.k.filter(k => k > 0 && k < 1))
  if (d.model === 'blocks') return Math.min(1, ...d.blocks.filter(k => k > 0 && k < 1))
  return d.night.k
})
const blockCounts = computed(() => {
  const b = draft.value.blocks
  return { full: b.filter(k => k >= 1).length, reduced: b.filter(k => k > 0 && k < 1).length, off: b.filter(k => k <= 0).length }
})
const shiftBounds = computed(() => { const s = draft.value.shifts; return [s.early, s.late, s.night, s.early] })
const SHIFTS = [['early', 'Early'], ['late', 'Late'], ['night', 'Night']] as const

// ---------- Edits ----------
function setDay(i: number, field: 'start' | 'end', value: number) {
  const list = i < 0 ? draft.value.week : [draft.value.week[i]]
  for (const w of list) {
    w[field] = value
    if (w.end <= w.start) { if (field === 'start') w.end = Math.min(24, value + 0.5); else w.start = Math.max(0, value - 0.5) }
  }
}
function toggleSame(on: boolean) {
  same.value = on
  if (on) { const f = onDays()[0] ?? draft.value.week[0]; for (const w of draft.value.week) { w.start = f.start; w.end = f.end } }
}
function pickModel(id: 'none' | NightModel) {
  if (id === 'none') draft.value.nights = false
  else { draft.value.nights = true; draft.value.model = id }
}
function reset() {
  const def = defaultSchedule(draft.value.timezone)
  if (props.kind === 'week') { draft.value.week = def.week; draft.value.off_days = def.off_days; same.value = true }
  else draft.value = { ...draft.value, nights: def.nights, model: def.model, night: def.night, shifts: def.shifts, blocks: def.blocks }
}
function fromMyHours() {
  const w = draft.value.week[refDay.value]
  draft.value.blocks = Array.from({ length: 24 }, (_, h) => (h >= w.start && h < w.end ? 1 : 0))
}
const brushValue = () => (brush.value === 'full' ? 1 : brush.value === 'off' ? 0 : clampRate(reducedPace.value / 100))
function paint(h: number) { draft.value.blocks[h] = brushValue(); focusHour.value = h }
const hourName = (h: number) => { const k = draft.value.blocks[h]; return `${timeLabel(h)} to ${timeLabel(h + 1)}: ${k >= 1 ? 'full' : k > 0 ? `reduced, ${Math.round(k * 100)}%` : 'off'}` }
let painting = false
function paintDown(event: PointerEvent, h: number) { event.preventDefault(); painting = true; paint(h); (event.currentTarget as HTMLElement).focus({ preventScroll: true }) }
function paintMove(event: PointerEvent) {
  if (!painting) return
  const el = document.elementFromPoint(event.clientX, event.clientY)?.closest<HTMLElement>('.hr')
  if (el && root.value?.contains(el)) { const h = +(el.dataset.h ?? -1); if (h >= 0 && draft.value.blocks[h] !== brushValue()) paint(h) }
}
const paintUp = () => { painting = false }
function paintKey(event: KeyboardEvent, h: number) {
  const keys: Record<string, 'full' | 'reduced' | 'off'> = { f: 'full', r: 'reduced', o: 'off' }
  if (event.key === 'ArrowRight' || event.key === 'ArrowLeft') {
    event.preventDefault()
    const next = Math.max(0, Math.min(23, h + (event.key === 'ArrowRight' ? 1 : -1)))
    focusHour.value = next
    if (event.shiftKey) paint(next)
    void nextTick(() => root.value?.querySelector<HTMLElement>(`.hr[data-h="${next}"]`)?.focus())
  } else if (event.key === ' ' || event.key === 'Enter') { event.preventDefault(); paint(h) }
  else if (keys[event.key.toLowerCase()]) { event.preventDefault(); brush.value = keys[event.key.toLowerCase()]; paint(h) }
}
function radioKeys(event: KeyboardEvent, options: string[], current: string, set: (v: string) => void) {
  if (event.key !== 'ArrowRight' && event.key !== 'ArrowLeft') return
  event.preventDefault()
  const i = options.indexOf(current)
  const next = options[(i + (event.key === 'ArrowRight' ? 1 : -1) + options.length) % options.length]
  set(next)
  void nextTick(() => (event.currentTarget as HTMLElement).querySelector<HTMLElement>(`[data-v="${next}"]`)?.focus())
}

// ---------- Preview: the server paces the draft ----------
const currentSentences = computed(() => props.pools.map(p => ({ id: p.id, mark: p.mark || p.id, sentence: poolSentence(p, props.now) })))
const previewPools = ref<PoolView[] | null>(null)
const previewFailed = ref(false)
let previewTimer: ReturnType<typeof setTimeout> | undefined
let previewTurn = 0
watch(draft, () => {
  clearTimeout(previewTimer)
  if (problem.value) return
  const turn = ++previewTurn
  previewTimer = setTimeout(async () => {
    try {
      const result = await previewCapacity(draft.value)
      if (turn !== previewTurn) return
      previewPools.value = buildPools(buildRows(props.accounts, result), props.now)
      previewFailed.value = false
    } catch { if (turn === previewTurn) previewFailed.value = true }
  }, 220)
}, { deep: true })
const preview = computed(() => currentSentences.value.map(({ id, mark, sentence }) => {
  const pool = previewPools.value?.find(p => p.id === id)
  const next: Sentence = pool && dirty.value && !problem.value ? poolSentence(pool, props.now) : sentence
  return { id, mark, sentence: next, changed: plainText(next) !== plainText(sentence) }
}))

// ---------- Save, close, focus ----------
function save() { if (!problem.value && !props.saving) emit('save', clone(draft.value)) }
function keydown(event: KeyboardEvent) {
  if (event.key === 'Escape') { event.preventDefault(); event.stopPropagation(); emit('close'); return }
  if (props.sheet && event.key === 'Tab') trapTab(event)
}
// The sheet is modal: Tab and Shift+Tab cycle inside it, including from the
// heading (focused on open) or anything else that is not a tab stop.
function focusables() {
  return [...(root.value?.querySelectorAll<HTMLElement>('button:not(:disabled), input:not(:disabled), select:not(:disabled), [tabindex="0"]') ?? [])].filter(x => x.tabIndex >= 0 && x.offsetParent !== null)
}
function trapTab(event: KeyboardEvent) {
  const list = focusables()
  if (!list.length) return
  const i = list.indexOf(document.activeElement as HTMLElement)
  const next = event.shiftKey ? (i <= 0 ? list[list.length - 1] : null) : (i === -1 || i === list.length - 1 ? list[0] : null)
  if (next) { event.preventDefault(); next.focus() }
}
// Focus that lands outside the open sheet (a click through, a script) comes back.
function holdFocus(event: FocusEvent) {
  if (props.sheet && root.value && event.target instanceof Node && !root.value.contains(event.target)) (focusables()[0] ?? title.value)?.focus({ preventScroll: true })
}
onMounted(() => { title.value?.focus({ preventScroll: true }); window.addEventListener('pointerup', paintUp); window.addEventListener('pointermove', paintMove); document.addEventListener('focusin', holdFocus) })
onBeforeUnmount(() => { clearTimeout(previewTimer); window.removeEventListener('pointerup', paintUp); window.removeEventListener('pointermove', paintMove); document.removeEventListener('focusin', holdFocus) })
const titleText = computed(() => (props.kind === 'week' ? 'Work week' : 'Agents outside your hours'))
const zoneNote = computed(() => { try { return Intl.DateTimeFormat().resolvedOptions().timeZone !== props.timezone ? `Times in ${props.timezone}.` : '' } catch { return '' } })
</script>

<template>
  <div ref="root" class="ed" :class="[kind, { sheet }]" role="dialog" :aria-modal="sheet" aria-labelledby="ed-title" @keydown="keydown">
    <div v-if="sheet" class="grab" aria-hidden="true" />
    <div class="ed-head">
      <h3 id="ed-title" ref="title" tabindex="-1">{{ titleText }}</h3>
      <button class="icon-btn flat ed-x" type="button" aria-label="Close without saving" @click="emit('close')"><AppIcon name="close" :size="16" /></button>
    </div>
    <div class="ed-foot">
      <button class="btn ghost" type="button" @click="reset">Reset to default</button>
      <span class="sp" />
      <span class="ed-err" :role="problem ? 'alert' : undefined">{{ problem }}</span>
      <button class="btn" type="button" @click="emit('close')">Cancel</button>
      <button class="btn primary" type="button" :disabled="!!problem || saving" @click="save">{{ saving ? 'Saving…' : 'Save' }}</button>
    </div>
    <div class="ed-body">
      <!-- ---------- Work week ---------- -->
      <template v-if="kind === 'week'">
        <p class="ed-help">Agents spread what is left of each account across these hours, so it lands at 0% at its reset. {{ zoneNote }}</p>
        <div>
          <div class="sec-t"><span>{{ weekSummary }}</span><span class="aside"><label class="chk"><input type="checkbox" :checked="same" @change="toggleSame(($event.target as HTMLInputElement).checked)">Same hours every day</label></span></div>
          <div v-if="same" class="same-row">
            <span class="ed-help">Hours</span>
            <select class="sel" aria-label="Every day from" :value="(onDays()[0] ?? draft.week[0]).start" @change="setDay(-1, 'start', +($event.target as HTMLSelectElement).value)"><option v-for="v in STARTS" :key="v" :value="v">{{ timeLabel(v) }}</option></select>
            <span class="dash">–</span>
            <select class="sel" aria-label="Every day until" :value="(onDays()[0] ?? draft.week[0]).end" @change="setDay(-1, 'end', +($event.target as HTMLSelectElement).value)"><option v-for="v in ENDS" :key="v" :value="v">{{ timeLabel(v) }}</option></select>
          </div>
        </div>
        <div class="wkw" :class="{ same }">
          <div class="wk-axis" aria-hidden="true"><span /><span /><div class="axis"><span>00</span><span>06</span><span>12</span><span>18</span><span>24</span></div></div>
          <ul class="wk">
            <li v-for="(w, i) in draft.week" :key="i" class="wk-row" :class="{ off: !w.on }">
              <span class="wk-day">{{ DAYS[i] }}</span>
              <button class="tog sm" type="button" role="switch" :aria-checked="w.on" :aria-label="DAYS_LONG[i]" @click="w.on = !w.on" />
              <div class="rb-wrap">
                <div class="rb"><i v-for="(c, j) in profiles[i]" :key="j" :class="{ exp: c.expire }" :style="cellStyle(c)" /></div>
                <b v-if="i === todayIndex" class="now" :style="{ left: `${nowPct}%` }" title="Now" />
              </div>
              <span v-if="!same" class="wk-hours">
                <select class="sel" :aria-label="`${DAYS_LONG[i]} from`" :value="w.start" @change="setDay(i, 'start', +($event.target as HTMLSelectElement).value)"><option v-for="v in STARTS" :key="v" :value="v">{{ timeLabel(v) }}</option></select>
                <span class="dash">–</span>
                <select class="sel" :aria-label="`${DAYS_LONG[i]} until`" :value="w.end" @change="setDay(i, 'end', +($event.target as HTMLSelectElement).value)"><option v-for="v in ENDS" :key="v" :value="v">{{ timeLabel(v) }}</option></select>
              </span>
            </li>
          </ul>
          <div class="rb-legend">
            <span class="li"><span class="sw"><i :style="{ background: tint(1) }" /></span>full pace</span>
            <span v-if="reducedK < 1" class="li"><span class="sw"><i :style="{ background: tint(reducedK) }" /></span>reduced ({{ nightLabel(draft) }})</span>
            <span v-if="draft.off_days === 'expire'" class="li"><span class="sw"><i class="exp" /></span>only if capacity would expire</span>
            <span class="li"><span class="sw tick" />now</span>
          </div>
        </div>
        <p v-if="draft.nights && (draft.model === 'shifts' || draft.model === 'blocks')" class="note">On work days, agent hours come from your {{ draft.model === 'shifts' ? '3 shifts' : 'custom blocks' }}. The hours here still mark when you are around.</p>
        <div>
          <div id="off-lbl" class="sec-t off-t">On days off</div>
          <span class="seg wide" role="radiogroup" aria-labelledby="off-lbl" @keydown="radioKeys($event, ['rest', 'expire', 'normal'], draft.off_days, v => draft.off_days = v as OffDays)">
            <button v-for="[v, label] in ([['rest', 'Rest'], ['expire', 'Use what would expire'], ['normal', 'Work normally']] as const)" :key="v" type="button" role="radio" :data-v="v" :aria-checked="draft.off_days === v" :tabindex="draft.off_days === v ? 0 : -1" @click="draft.off_days = v">{{ label }}</button>
          </span>
          <p class="ed-help off-help">{{ OFF_HELP[draft.off_days] }}</p>
        </div>
      </template>

      <!-- ---------- Outside your hours ---------- -->
      <template v-else>
        <fieldset class="models">
          <legend>How do you think about the day?</legend>
          <label v-for="m in MODELS" :key="m.id" class="model">
            <input type="radio" name="night-model" :value="m.id" :checked="model === m.id" @change="pickModel(m.id)">
            <span class="t">{{ m.title }}</span><span class="d">{{ m.desc }}</span>
          </label>
        </fieldset>
        <div>
          <div class="sec-t big-t">
            <span>{{ model === 'blocks' ? 'Your day, hour by hour' : `A work day (${DAYS[refDay]})` }}</span>
            <span class="aside">≈ {{ fullPaceHours(draft, refDay).toFixed(1).replace(/\.0$/, '') }} full-pace hours a day</span>
          </div>
          <div class="axis" aria-hidden="true"><span>00</span><span>06</span><span>12</span><span>18</span><span>24</span></div>
          <div class="big">
            <div v-if="model === 'blocks'" class="paint" role="group" aria-label="Agent pace per hour">
              <button
                v-for="(k, h) in draft.blocks" :key="h" type="button" class="hr" :data-h="h" :tabindex="h === focusHour ? 0 : -1" :aria-label="hourName(h)" :style="{ background: k > 0 ? tint(k) : 'var(--track)' }"
                @pointerdown="paintDown($event, h)" @keydown="paintKey($event, h)"
              />
            </div>
            <div v-else class="rb-wrap">
              <div class="rb lg"><i v-for="(c, j) in bigProfile" :key="j" :class="{ exp: c.expire }" :style="cellStyle(c)" /></div>
              <b v-if="refDay === todayIndex" class="now" :style="{ left: `${nowPct}%` }" title="Now" />
            </div>
          </div>
          <div class="rb-legend">
            <template v-if="model === 'none'"><span class="li"><span class="sw"><i :style="{ background: tint(1) }" /></span>agents with you <b>{{ hourLabel(draft.week[refDay].start) }}–{{ hourLabel(draft.week[refDay].end) }}</b></span></template>
            <template v-else-if="model === 'daynight'">
              <span class="li"><span class="sw"><i :style="{ background: tint(1) }" /></span>work hours <b>{{ hourLabel(draft.week[refDay].start) }}–{{ hourLabel(draft.week[refDay].end) }}</b></span>
              <span class="li"><span class="sw"><i :style="{ background: tint(draft.night.k) }" /></span>night <b>{{ hourLabel(draft.night.start) }}–{{ hourLabel(draft.night.end) }} at {{ Math.round(draft.night.k * 100) }}%</b></span>
            </template>
            <template v-else-if="model === 'shifts'">
              <span v-for="([, label], i) in SHIFTS" :key="label" class="li"><span class="sw"><i :style="{ background: tint(draft.shifts.k[i]) }" /></span>{{ label }} <b>{{ hourLabel(shiftBounds[i]) }}–{{ hourLabel(shiftBounds[i + 1]) }} · {{ Math.round(draft.shifts.k[i] * 100) }}%</b></span>
            </template>
            <template v-else>
              <span class="li"><span class="sw"><i :style="{ background: tint(1) }" /></span>full <b>{{ blockCounts.full }} h</b></span>
              <span class="li"><span class="sw"><i :style="{ background: tint(0.5) }" /></span>reduced <b>{{ blockCounts.reduced }} h</b></span>
              <span class="li"><span class="sw"><i /></span>off <b>{{ blockCounts.off }} h</b></span>
            </template>
          </div>
        </div>
        <p v-if="model === 'none'" class="ed-help">Agents work only in the hours set under Work week. Nights stay quiet.</p>
        <div v-else-if="model === 'daynight'" class="drows">
          <div class="drow">
            <span class="nm">Night</span><span class="lb">from</span>
            <select class="sel" aria-label="Night starts" :value="draft.night.start" @change="draft.night.start = +($event.target as HTMLSelectElement).value"><option v-for="v in STARTS" :key="v" :value="v">{{ timeLabel(v) }}</option></select>
            <span class="lb">until</span>
            <select class="sel" aria-label="Night ends" :value="draft.night.end" @change="draft.night.end = +($event.target as HTMLSelectElement).value"><option v-for="v in STARTS" :key="v" :value="v">{{ timeLabel(v) }}</option></select>
          </div>
          <div class="drow pace">
            <span class="nm">Pace</span>
            <input type="range" min="10" max="100" step="10" :value="Math.round(draft.night.k * 100)" aria-label="Night pace" aria-describedby="pace-help" @input="draft.night.k = +($event.target as HTMLInputElement).value / 100">
            <output>{{ Math.round(draft.night.k * 100) }}%</output>
          </div>
          <p id="pace-help" class="ed-help">Pace is the share of a full work hour. Nights follow work days only.</p>
        </div>
        <div v-else-if="model === 'shifts'" class="drows">
          <div v-for="([field, label], i) in SHIFTS" :key="field" class="drow pace">
            <span class="nm">{{ label }}</span><span class="lb">starts</span>
            <select class="sel" :aria-label="`${label} shift starts`" :value="draft.shifts[field]" @change="draft.shifts[field] = +($event.target as HTMLSelectElement).value"><option v-for="v in STARTS" :key="v" :value="v">{{ timeLabel(v) }}</option></select>
            <span class="gap" />
            <input type="range" min="0" max="100" step="10" :value="Math.round(draft.shifts.k[i] * 100)" :aria-label="`${label} shift pace`" @input="draft.shifts.k[i] = clampRate(+($event.target as HTMLInputElement).value / 100)">
            <output>{{ Math.round(draft.shifts.k[i] * 100) }}%</output>
          </div>
          <p class="ed-help">Each shift runs until the next one starts. On work days the shifts replace your hours for agents.</p>
        </div>
        <template v-else>
          <div class="brush">
            <span id="brush-lbl" class="sec-t">Brush</span>
            <span class="seg" role="radiogroup" aria-labelledby="brush-lbl" @keydown="radioKeys($event, ['full', 'reduced', 'off'], brush, v => brush = v as typeof brush)">
              <button v-for="[v, label] in ([['full', 'Full'], ['reduced', 'Reduced'], ['off', 'Off']] as const)" :key="v" type="button" role="radio" :data-v="v" :aria-checked="brush === v" :tabindex="brush === v ? 0 : -1" @click="brush = v">{{ label }}</button>
            </span>
            <button class="btn sm from-hours" type="button" @click="fromMyHours">Start from my hours</button>
          </div>
          <div v-if="brush === 'reduced'" class="drow pace">
            <span class="nm">Reduced</span>
            <input type="range" min="10" max="90" step="10" :value="reducedPace" aria-label="Reduced pace" @input="reducedPace = +($event.target as HTMLInputElement).value">
            <output>{{ reducedPace }}%</output>
          </div>
          <p class="ed-help">Drag across hours to paint, or use the arrow keys and Space. Each reduced hour keeps the pace it was painted with. Replaces your hours for agents on work days.</p>
        </template>
      </template>

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
.ed { position: absolute; z-index: 30; display: flex; flex-direction: column; width: clamp(620px, 50vw, 1040px); max-width: calc(100vw - 24px); max-height: min(720px, calc(100dvh - 96px)); border-radius: 16px; background: var(--surface-raised); border: 1px solid var(--glass-edge); box-shadow: var(--shadow-pop); color: var(--ink); text-align: left; }
.ed.night { width: clamp(580px, 44vw, 960px); }
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
.ed-err { flex: 0 1 110px; height: 38px; overflow: auto; color: var(--warn-ink); font-size: 12.5px; font-weight: 600; }
.btn:disabled { opacity: .5; cursor: default; filter: none; }
.sec-t { display: flex; align-items: center; gap: 10px; color: var(--ink); font-size: 13px; font-weight: 650; }
.sec-t .aside { margin-left: auto; color: var(--ink-3); font-weight: 450; font-size: 12.5px; }
.off-t, .big-t { margin-bottom: 8px; }
.off-help { margin-top: 6px; }
.chk { display: inline-flex; align-items: center; gap: 8px; min-height: 32px; color: var(--ink-2); font-size: 12.5px; font-weight: 550; cursor: pointer; }
.chk input { width: 16px; height: 16px; margin: 0; accent-color: var(--primary); }
.sel { -webkit-appearance: none; appearance: none; height: 30px; padding: 0 24px 0 9px; border: 0; border-radius: 8px; background: var(--field-bg) url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 16 16' fill='none' stroke='%237a8c8d' stroke-width='2' stroke-linecap='round' stroke-linejoin='round'%3E%3Cpath d='m4.5 6.3 3.5 3.5 3.5-3.5'/%3E%3C/svg%3E") no-repeat right 7px center / 11px; box-shadow: inset 0 0 0 1px var(--line-2); color: var(--ink); font: 500 12.5px var(--mono); font-variant-numeric: tabular-nums; cursor: pointer; }
.sel:focus-visible { outline: none; box-shadow: var(--focus-ring); }
.dash { color: var(--ink-3); }
.same-row { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; margin-top: 6px; }
/* Day switches: small, quiet, the same pill as the card's night toggle. */
.tog { position: relative; display: inline-flex; align-items: center; width: 32px; height: 18px; padding: 0; border: 0; border-radius: 999px; background: var(--line-2); box-shadow: inset 0 1px 2px color-mix(in srgb, var(--shadow-black) 12%, transparent); cursor: pointer; }
.tog::after { content: ''; position: absolute; left: 3px; width: 12px; height: 12px; border-radius: 50%; background: var(--switch-knob); box-shadow: 0 1px 3px color-mix(in srgb, var(--shadow-black) 28%, transparent); transition: transform .18s ease; }
.tog[aria-checked="true"] { background: linear-gradient(180deg, var(--primary-hi), var(--primary) 60%, var(--primary-lo)); }
.tog[aria-checked="true"]::after { background: var(--primary-on); transform: translateX(14px); }
.tog:focus-visible { outline: none; box-shadow: var(--focus-ring); }
.tog::before { content: ''; position: absolute; inset: -9px -4px; }
@media (prefers-reduced-motion: reduce) { .tog::after { transition: none; } }
/* Ribbons: 24 h, pace as tint, hairline cuts at 06, 12 and 18. */
.rb-wrap { position: relative; min-width: 0; }
.rb { position: relative; display: flex; height: 12px; border-radius: 999px; overflow: hidden; background: var(--track); }
.rb i { flex: 1; }
.rb::after { content: ''; position: absolute; inset: 0; pointer-events: none; background: linear-gradient(90deg, transparent calc(25% - .5px), var(--surface-raised) calc(25% - .5px) calc(25% + .5px), transparent calc(25% + .5px) calc(50% - .5px), var(--surface-raised) calc(50% - .5px) calc(50% + .5px), transparent calc(50% + .5px) calc(75% - .5px), var(--surface-raised) calc(75% - .5px) calc(75% + .5px), transparent calc(75% + .5px)); }
.rb i.exp, .sw i.exp { background: repeating-linear-gradient(135deg, color-mix(in srgb, var(--teal) 50%, transparent) 0 1.5px, transparent 1.5px 4.5px); }
.rb.lg { height: 26px; border-radius: 8px; }
.now { position: absolute; top: -3px; bottom: -3px; width: 2px; margin-left: -1px; border-radius: 2px; background: var(--ink); box-shadow: 0 0 0 1.5px var(--surface-raised); }
.axis { display: flex; justify-content: space-between; color: var(--ink-3); font: 10.5px/1 var(--mono); font-variant-numeric: tabular-nums; }
.big { margin-top: 5px; }
.rb-legend { display: flex; flex-wrap: wrap; gap: 6px 14px; margin-top: 8px; color: var(--ink-3); font-size: 12px; }
.rb-legend .li { display: inline-flex; align-items: center; gap: 6px; white-space: nowrap; }
.rb-legend b { color: var(--ink-2); font-weight: 600; }
.sw { position: relative; display: inline-block; width: 14px; height: 8px; border-radius: 999px; overflow: hidden; background: var(--track); }
.sw i { position: absolute; inset: 0; }
.sw.tick { width: 2px; height: 12px; border-radius: 2px; background: var(--ink); overflow: visible; }
.wkw .rb-legend { margin-top: 10px; }
.wk { display: grid; gap: 2px; margin: 0; padding: 0; list-style: none; }
.wk-row, .wk-axis { display: grid; grid-template-columns: 34px 36px minmax(0, 1fr) 166px; align-items: center; gap: 10px; min-height: 36px; }
.wk-axis { min-height: 16px; }
.wkw.same .wk-row, .wkw.same .wk-axis { grid-template-columns: 34px 36px minmax(0, 1fr); }
.wk-day { color: var(--ink); font-size: 13px; font-weight: 650; }
.wk-row.off .wk-day { color: var(--ink-3); font-weight: 500; }
.wk-hours { display: flex; align-items: center; gap: 6px; justify-content: flex-end; }
.wk-row.off .wk-hours .sel { opacity: .4; }
.seg.wide button { padding: 0 12px; }
.note { padding: 9px 12px; border-radius: 10px; background: var(--surface-sunken); color: var(--ink-2); font-size: 12.5px; }
.models { display: grid; grid-template-columns: 1fr 1fr; gap: 8px; margin: 0; padding: 0; border: 0; min-width: 0; }
.models legend { margin-bottom: 8px; padding: 0; color: var(--ink); font-size: 13px; font-weight: 650; }
.model { position: relative; display: grid; gap: 1px; align-content: start; min-height: 58px; padding: 10px 12px 10px 38px; border-radius: 12px; background: var(--surface-sunken); box-shadow: inset 0 0 0 1px var(--line); cursor: pointer; }
@media (hover: hover) { .model:hover { background: var(--row-hover); } }
.model input { position: absolute; left: 13px; top: 12px; width: 16px; height: 16px; margin: 0; accent-color: var(--primary); }
.model:has(input:checked) { background: var(--row-selected); box-shadow: inset 0 0 0 1px var(--chip-teal-line); }
.model:has(input:focus-visible) { box-shadow: inset 0 0 0 1px var(--chip-teal-line), var(--focus-ring); }
.model input:focus-visible { outline: none; box-shadow: none; }
.model .t { color: var(--ink); font-size: 13.5px; font-weight: 650; }
.model .d { color: var(--ink-3); font-size: 12px; line-height: 1.35; }
.drows { display: grid; gap: 6px; }
.drow { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; min-height: 36px; color: var(--ink-2); font-size: 13px; }
.drow .nm { width: 58px; color: var(--ink); font-weight: 650; }
.drow .gap { width: 8px; }
.drow input[type="range"] { width: 150px; accent-color: var(--primary); }
.drow output { min-width: 38px; color: var(--teal-ink); font: 600 12.5px var(--mono); font-variant-numeric: tabular-nums; }
.paint { display: flex; gap: 1px; height: 40px; border-radius: 10px; overflow: hidden; background: var(--surface-raised); touch-action: none; user-select: none; }
.paint .hr { flex: 1; min-width: 0; margin: 0; padding: 0; border: 0; border-radius: 0; cursor: crosshair; }
.paint .hr:focus-visible { position: relative; z-index: 1; outline: none; box-shadow: inset 0 0 0 2px var(--ink); }
.brush { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; }
.from-hours { margin-left: auto; }
.pv { padding: 12px 14px; border-radius: 12px; background: var(--surface-sunken); }
.pv ul { display: grid; gap: 4px; margin: 6px 0 0; padding: 0; list-style: none; }
.pv li { display: grid; grid-template-columns: 16px minmax(0, 1fr); gap: 8px; margin: 0 -6px; padding: 3px 6px; border-radius: 6px; color: var(--ink-2); font-size: 12.5px; line-height: 1.45; }
.pv li.none { grid-template-columns: 1fr; color: var(--ink-3); }
.pv li.changed { background: var(--row-selected); }
.pv li :deep(.mark) { margin-top: 3px; color: var(--ink-2); }
.pv :deep(b) { color: var(--ink); font-weight: 600; }
.pv :deep(.n) { color: var(--teal-ink); font-weight: 700; font-variant-numeric: tabular-nums; }

/* Phone: a bottom sheet */
.ed.sheet { position: fixed; left: 0; right: 0; bottom: 0; top: auto; z-index: 81; width: auto; max-width: none; height: 100dvh; max-height: 100dvh; border-radius: 22px 22px 0 0; border-bottom: 0; }
.sheet .ed-head { padding: 4px 8px 2px 16px; }
.sheet .ed-body { order: 1; overflow-y: auto; overscroll-behavior: contain; padding: 0 16px 18px; gap: 14px; }
.sheet .ed-foot { order: 2; border-bottom: 0; border-top: 1px solid var(--line); flex-wrap: wrap; padding: 10px 16px calc(16px + env(safe-area-inset-bottom)); }
.sheet .ed-foot .btn { min-height: 44px; }
.sheet .ed-foot .btn:not(.ghost) { flex: 1; }
.sheet .ed-foot .sp { display: none; }
.sheet .ed-foot .ed-err { flex: none; width: 100%; }
.sheet .ed-foot .ghost { width: 100%; order: 3; min-height: 40px; }
.sheet .ed-x { width: 44px; height: 44px; }
.sheet .models { grid-template-columns: 1fr; }
.sheet .model { min-height: 52px; }
.sheet .sel { min-height: 40px; }
.sheet .wk-row { grid-template-columns: 36px 44px minmax(0, 1fr); grid-template-areas: "day sw hours" "rb rb rb"; row-gap: 6px; padding: 6px 0; }
.sheet .wk-row + .wk-row { box-shadow: 0 -1px 0 var(--line); }
.sheet .wk-row .wk-day { grid-area: day; }
.sheet .wk-row .tog { grid-area: sw; }
.sheet .wk-row .rb-wrap { grid-area: rb; }
.sheet .wk-row .wk-hours { grid-area: hours; }
.sheet .wkw.same .wk-row { grid-template-areas: "day sw rb"; padding: 4px 0; }
.sheet .wk-axis { grid-template-columns: 36px 44px minmax(0, 1fr); }
.sheet .wkw:not(.same) .wk-axis { display: none; }
.sheet .tog::before { inset: -13px -6px; }
.sheet .seg.wide { display: grid; grid-template-columns: 1fr; border-radius: 14px; }
.sheet .seg.wide button { height: 40px; border-radius: 11px; }
.sheet .drow.pace { display: grid; grid-template-columns: 64px auto auto minmax(0, 1fr); grid-template-areas: "nm lb sel ." "rg rg rg out"; column-gap: 8px; row-gap: 2px; padding: 4px 0; }
.sheet .drow.pace + .drow.pace { box-shadow: 0 -1px 0 var(--line); }
.sheet .drow.pace .nm { grid-area: nm; }
.sheet .drow.pace .lb { grid-area: lb; }
.sheet .drow.pace .sel { grid-area: sel; }
.sheet .drow.pace .gap { display: none; }
.sheet .drow.pace input[type="range"] { grid-area: rg; width: 100%; height: 36px; }
.sheet .drow.pace output { grid-area: out; justify-self: end; }
.sheet .drow.pace:not(:has(.sel)) { grid-template-areas: "nm rg rg out"; }
.sheet .paint { height: 48px; }
.sheet .from-hours { margin-left: 0; }
</style>
