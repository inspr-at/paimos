<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// PROGRESS (AEON-1457 decision 5 and r5): the bar with its % and "N of M done", and a
// signal row that is always reserved. The bar is a button: it swaps for a stepped
// chart of progress over time in place, in the same footprint, and back (click,
// Enter or Esc; focus returns to the bar). The signal row says the average pace,
// "Ø 0.8 %/h · 24 h", or in gold that nothing moved for hours while items are
// moving (the server decides); with the chart open the range switch leads it.
import { computed, nextTick, onBeforeUnmount, ref } from 'vue'
import AppIcon from '../AppIcon.vue'
import GoalTip from './GoalTip.vue'
import type { SeriesEntry } from './goalsPage'
import type { Goal } from '../../lib/goals'
import { availablePoints, averageRate, chartShape, GOAL_RANGES, historySince, LONGEST_SIGNAL, pointText, RANGE_LABEL, signalText, stallText, CHART, type GoalRange } from '../../lib/goalRate'
import { whenText } from '../../lib/goals'

const props = defineProps<{ goal: Goal; open: boolean; range: GoalRange; entry?: SeriesEntry; now: number }>()
const emit = defineEmits<{ toggle: []; range: [value: GoalRange] }>()

const progress = computed(() => props.goal.progress)
const pct = computed(() => progress.value?.pct ?? 0)
const done = computed(() => !!props.goal.done)
const ready = computed(() => props.entry?.state === 'ready' && props.entry.series?.range === props.range ? props.entry : null)
const points = computed(() => ready.value?.series?.points ?? [])
const createdAt = computed(() => ready.value?.createdAt ?? null)
const rate = computed(() => ready.value ? averageRate(points.value, props.now, createdAt.value) : null)
const windowStart = computed(() => points.value.length ? Date.parse(points.value[0]!.t) : Infinity)
const young = computed(() => createdAt.value !== null && createdAt.value > windowStart.value)
// A goal born inside the window with fewer than three values has no line to draw yet.
const few = computed(() => young.value && (rate.value?.values ?? 0) < 3)

const stalled = computed(() => !done.value && !!props.goal.stall?.stalled)
const signal = computed(() => {
  if (done.value) return null
  if (stalled.value) return { stall: true, text: stallText(Date.parse(props.goal.stall!.since), props.now), tip: `Last change ${whenText(Date.parse(props.goal.stall!.since), props.now)}` }
  return rate.value ? { stall: false, text: signalText(rate.value, props.range), tip: '' } : null
})

// ---------- The chart ----------
const shown = computed(() => availablePoints(points.value, createdAt.value))
const values = computed(() => { const skip = points.value.length - shown.value.length; return points.value.map((point, i) => i < skip ? null : point.pct) })
const shape = computed(() => chartShape(values.value))
const chart = ref<HTMLElement>()
const bar = ref<HTMLButtonElement>()
const index = ref<number | null>(null)
const tipAt = ref<number | null>(null)
const point = computed(() => index.value === null ? null : points.value[index.value] ?? null)
const pointLines = computed(() => point.value ? [{ text: pointText(props.range, point.value) }] : [])
function select(i: number) {
  const n = points.value.length
  if (!n || !chart.value) return
  index.value = Math.max(shape.value.first, Math.min(n - 1, i))
  const box = chart.value.getBoundingClientRect()
  tipAt.value = box.left + box.width * shape.value.xOf(index.value) / 100
}
function indexAt(clientX: number) {
  const box = chart.value!.getBoundingClientRect(), n = points.value.length, w = (CHART.x1 - CHART.x0) / n
  return Math.floor(((clientX - box.left) / box.width * 100 - CHART.x0) / w)
}
function leave() { index.value = null; tipAt.value = null }
let scrubbing = false
function move(event: PointerEvent) { if (event.pointerType !== 'touch' || scrubbing) select(indexAt(event.clientX)) }
function down(event: PointerEvent) { if (event.pointerType === 'touch') { scrubbing = true; select(indexAt(event.clientX)) } }
function up() { scrubbing = false }
function out(event: PointerEvent) { if (event.pointerType !== 'touch' && document.activeElement !== chart.value) leave() }
// A mouse click closes; a tap only reads the point under the finger.
let lastPointer = ''
function click() { if (lastPointer !== 'touch') close() }
function pointerType(event: PointerEvent) { lastPointer = event.pointerType }
function chartKey(event: KeyboardEvent) {
  if (event.key === 'Escape' || event.key === 'Enter' || event.key === ' ') { event.preventDefault(); event.stopPropagation(); close(); return }
  const n = points.value.length, current = index.value ?? n - 1
  const next = event.key === 'ArrowLeft' ? current - 1 : event.key === 'ArrowRight' ? current + 1 : event.key === 'Home' ? shape.value.first : event.key === 'End' ? n - 1 : null
  if (next !== null && n) { event.preventDefault(); event.stopPropagation(); select(next) }
}
function chartFocus(event: FocusEvent) { if ((event.target as HTMLElement).matches(':focus-visible') && points.value.length && !few.value) select(index.value ?? points.value.length - 1) }
function rangeKey(event: KeyboardEvent) { if (event.key === 'Escape') { event.preventDefault(); event.stopPropagation(); close() } }

function close() { leave(); emit('toggle'); void nextTick(() => bar.value?.focus({ preventScroll: true })) }
function openChart() { emit('toggle'); void nextTick(() => chart.value?.focus({ preventScroll: true })) }
onBeforeUnmount(leave)

const label = computed(() => `${props.goal.title}, ${pct.value} percent done. Show progress over time`)
const chartLabel = computed(() => few.value
  ? `${props.goal.title}: ${historySince(createdAt.value!, props.now).toLowerCase()}. Enter or Escape closes.`
  : `${props.goal.title}: progress over time, ${RANGE_LABEL[props.range]}. Arrow keys move between points, Enter or Escape closes.`)
const placeholder = computed(() => {
  const state = props.entry?.series?.range === props.range || props.entry?.state === 'failed' ? props.entry?.state : 'reading'
  return state === 'failed' ? 'History couldn’t be read' : state === 'too_large' ? 'Too much history to chart' : 'Reading history'
})
</script>

<template>
  <div class="goal-progress" :class="{ 'is-open': open }">
    <template v-if="goal.expansion !== 'full' || !progress">
      <span class="of wide">Too many items to sum up</span>
    </template>
    <template v-else>
      <template v-if="open">
        <div v-if="ready && !few && points.length" :id="`chart-${goal.id}`" ref="chart" class="chart" :class="{ on: index !== null }" tabindex="0" role="group" :aria-label="chartLabel"
          @keydown="chartKey" @focus="chartFocus" @blur="leave" @pointermove="move" @pointerdown="pointerType($event); down($event)" @pointerup="up" @pointercancel="up" @pointerleave="out" @click="click"
        >
          <svg class="plot" :viewBox="`0 0 100 ${CHART.height}`" preserveAspectRatio="none" aria-hidden="true">
            <line v-for="y in shape.refs" :key="y" class="ref" x1="0" x2="100" :y1="y" :y2="y" />
            <path :d="shape.path" />
          </svg>
          <i class="end-dot" :style="{ left: `${shape.end.x}%`, top: `${shape.end.y / CHART.height * 100}%` }" />
          <template v-if="point && index !== null">
            <i class="hover-line" :style="{ left: `${shape.xOf(index)}%` }" />
            <i class="hover-dot" :style="{ left: `${shape.xOf(index)}%`, top: `${shape.yOf(point.pct) / CHART.height * 100}%` }" />
            <GoalTip :key="index" :anchor="chart!" :lines="pointLines" prefer="above" align="center" :x="tipAt" />
          </template>
        </div>
        <div v-else :id="`chart-${goal.id}`" ref="chart" class="nohist" tabindex="0" role="group" :aria-label="few ? chartLabel : placeholder" @keydown="chartKey" @click="close">
          {{ few ? historySince(createdAt!, now) : placeholder }}
        </div>
        <button type="button" class="pct" :aria-label="`${pct} percent. Hide progress over time`" @click="close"><AppIcon v-if="progress.source === 'override'" name="edit" :size="11" />{{ pct }}%</button>
      </template>
      <template v-else>
        <button ref="bar" type="button" class="bar-btn" data-tip="Progress over time" :aria-expanded="false" :aria-controls="`chart-${goal.id}`" :aria-label="label" @click="openChart">
          <span class="bar" role="progressbar" aria-valuemin="0" aria-valuemax="100" :aria-valuenow="pct" :aria-label="`${goal.title} progress`"><i :style="{ width: `${pct}%` }" /></span>
        </button>
        <span class="pct" :data-tip="progress.source === 'override' ? `Progress set by hand${progress.override_note ? ` · ${progress.override_note}` : ''}` : undefined"><AppIcon v-if="progress.source === 'override'" name="edit" :size="11" />{{ pct }}%</span>
        <span class="of">{{ progress.done }} of {{ progress.total }} done</span>
      </template>
      <div class="sigrow">
        <div v-if="open && !few" class="range" role="group" aria-label="Range" @keydown="rangeKey">
          <button v-for="value in GOAL_RANGES" :key="value" type="button" :aria-pressed="value === range" @click="emit('range', value)">{{ RANGE_LABEL[value] }}</button>
        </div>
        <span class="signal" :class="{ stall: signal?.stall }" :data-tip="signal?.tip || undefined">
          <span class="sizer" aria-hidden="true">{{ LONGEST_SIGNAL }}</span>
          <span class="sg-t"><AppIcon v-if="signal?.stall" name="clock" :size="12" />{{ signal?.text ?? '' }}</span>
        </span>
      </div>
    </template>
  </div>
</template>

<style scoped>
/* Bar and %, "N of M done", the reserved signal row. The chart swaps in for the first two rows; the cell is 52 px tall with a −3 px margin, so the row stays 64 px. */
.goal-progress { display: grid; grid-template-columns: minmax(0, 1fr) 38px; grid-template-rows: 16px 15px 15px; column-gap: 8px; row-gap: 3px; align-items: center; margin-block: -3px; min-width: 0; }
.bar-btn { grid-column: 1; grid-row: 1; position: relative; display: block; height: 16px; padding: 0; border: 0; border-radius: 6px; background: transparent; color: inherit; cursor: pointer; }
.bar-btn::before, button.pct::before { content: ''; position: absolute; inset: -4px -3px; }
.bar-btn .bar { position: absolute; inset: 5px 0 auto 0; }
.pct { grid-column: 2; grid-row: 1; display: inline-flex; align-items: center; justify-content: flex-end; gap: 4px; height: 16px; font-size: 12px; line-height: 16px; color: var(--ink-2); font-variant-numeric: tabular-nums; }
.pct svg { color: var(--ink-3); }
button.pct { position: relative; padding: 0; border: 0; border-radius: 6px; background: transparent; font-family: inherit; cursor: pointer; }
.bar-btn:focus-visible, button.pct:focus-visible { outline: none; box-shadow: var(--focus-ring); }
@media (hover: hover) { .bar-btn:hover, button.pct:hover { background: var(--primary-tint); } }
.of { grid-column: 1 / -1; grid-row: 2; font-size: 12px; line-height: 15px; color: var(--ink-3); font-variant-numeric: tabular-nums; white-space: nowrap; }
.of.wide { grid-row: 1 / 3; white-space: normal; }
/* The signal row may run up to 72 px past the cell, into the free space under the waits column. */
.sigrow { grid-column: 1 / -1; grid-row: 3; justify-self: start; display: flex; align-items: center; gap: 6px; width: max-content; max-width: calc(100% + 72px); min-width: 0; height: 15px; }
.signal { flex: 0 1 auto; display: inline-grid; min-width: 0; height: 15px; font-size: 12px; line-height: 15px; color: var(--ink-3); font-variant-numeric: tabular-nums; white-space: nowrap; }
/* The slot reserves the longest form, so a changing signal never moves anything (r5 rule 4). */
.signal > span { grid-area: 1 / 1; min-width: 0; }
.signal .sizer { visibility: hidden; overflow: hidden; }
.sg-t { display: flex; align-items: center; gap: 5px; overflow: hidden; text-overflow: ellipsis; }
.sg-t svg { flex: none; }
.signal.stall { color: var(--gold-ink); }
.range { flex: none; display: flex; gap: 1px; padding: 1px; height: 15px; border-radius: 7px; background: var(--seg-bg); }
.range button { position: relative; height: 13px; padding: 0 3px; border: 0; border-radius: 6px; background: transparent; color: var(--ink-3); font: 500 11px/13px var(--font); font-variant-numeric: tabular-nums; text-align: center; white-space: nowrap; cursor: pointer; }
.range button::before { content: ''; position: absolute; inset: -1px 0 -6px; }
.range button[aria-pressed="true"] { background: var(--seg-on); color: var(--ink); box-shadow: inset 0 0 0 1px var(--glass-edge); }
.range button:focus-visible { outline: none; box-shadow: var(--focus-ring); }
@media (hover: hover) { .range button:not([aria-pressed="true"]):hover { color: var(--ink); } }
/* The chart: the same footprint as bar and "N of M done", nothing on top of the plot. */
.chart, .nohist { grid-column: 1; grid-row: 1 / 3; position: relative; align-self: stretch; border-radius: 6px; outline: none; cursor: pointer; touch-action: pan-y; }
.chart:focus-visible, .nohist:focus-visible { box-shadow: var(--focus-ring); }
.plot { position: absolute; inset: 0; width: 100%; height: 100%; overflow: visible; }
.plot path { fill: none; stroke: var(--primary); stroke-width: 1.6; stroke-linejoin: round; stroke-linecap: round; vector-effect: non-scaling-stroke; }
.plot .ref { stroke: var(--line-2); stroke-width: 1; vector-effect: non-scaling-stroke; }
.end-dot { position: absolute; width: 7px; height: 7px; margin: -3.5px 0 0 -3.5px; border-radius: 50%; background: var(--primary); }
.hover-line { position: absolute; top: 0; bottom: 0; width: 1px; margin-left: -.5px; background: var(--line-2); }
.hover-dot { position: absolute; width: 9px; height: 9px; margin: -4.5px 0 0 -4.5px; border-radius: 50%; background: var(--canvas); box-shadow: inset 0 0 0 2px var(--primary); }
.nohist { display: grid; align-content: start; justify-items: center; text-align: center; font-size: 12px; line-height: 16px; color: var(--ink-3); }
@media (max-width: 900px) {
  .sigrow { gap: 4px; max-width: calc(100% + 80px); }
  .range button { padding: 0 2px; }
}
@media (max-width: 760px) {
  .goal-progress { grid-template-columns: minmax(0, 1fr) 44px; grid-template-rows: 22px 15px; row-gap: 2px; column-gap: 8px; margin-block: 0; }
  .bar-btn, .pct { height: 22px; line-height: 22px; }
  .bar-btn .bar { top: 8px; }
  .bar-btn::before, button.pct::before { inset: -11px -3px; }
  .of { display: none; }
  .sigrow { grid-row: 2; justify-self: stretch; width: auto; max-width: 100%; gap: 8px; }
  .chart, .nohist { grid-row: 1; }
  .nohist { line-height: 22px; }
  .range { padding: 0; }
  .range button { width: 42px; height: 15px; padding: 0; font-size: 11.5px; line-height: 15px; border-radius: 7px; }
  .range button::before { inset: -6px -1px -23px; }
}
</style>
