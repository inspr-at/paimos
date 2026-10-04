<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { brand } from '../../lib/brand'
import { axis, cadence, dayMonth, DEFAULT_RANGE, RANGES, rangeOf, releaseRangeKey, type RangeKey, type Slot } from '../../lib/releaseStats'
import type { Release } from '../../lib/releases'
import { useSession } from '../../stores/session'
import AppIcon from '../AppIcon.vue'

// The cadence chart of the release history (AEON-488): releases per day over
// 7, 14 or 30 days, per week over 13 weeks, per month over a year, chosen with
// < > and remembered for this person on this device. Gridlines with round
// ticks, the average as a dashed line, the peak emphasised, today ringed as
// still filling, weekends shaded, and the time before the first release drawn
// dashed and named rather than as zeros. Each bar is one stop for the keyboard
// (← → Home End move along); hover or focus names the slot, its count and its
// first and last release. Today is the last bar.
const props = defineProps<{ releases: Release[]; now: number; compact?: boolean }>()
const session = useSession()
const storeKey = computed(() => session.identity ? releaseRangeKey(session.identity.principal.id) : '')
const range = ref<RangeKey>(DEFAULT_RANGE)
watch(storeKey, key => { try { range.value = rangeOf(key ? localStorage.getItem(key) : null) } catch { range.value = DEFAULT_RANGE } }, { immediate: true })
const at = computed(() => RANGES.findIndex(r => r.key === range.value))
function step(delta: number) {
  const next = RANGES[at.value + delta]
  if (!next) return
  range.value = next.key
  try { if (storeKey.value) localStorage.setItem(storeKey.value, next.key) } catch { /* a private window keeps it for this visit */ }
}

const data = computed(() => cadence(props.releases, props.now, range.value))
const slots = computed(() => data.value.slots)
const N = computed(() => slots.value.length)
const dense = computed(() => N.value > 14)
const showAvg = computed(() => !props.compact && data.value.avg > 0 && data.value.range.unit !== 'month')
const scale = computed(() => axis(Math.max(slots.value[data.value.peak]?.n ?? 0, showAvg.value ? data.value.avg : 0)))
const pct = (v: number) => `${v / scale.value.top * 100}%`
const ticks = computed(() => {
  const out: { v: number; at: string }[] = []
  for (let v = 0; v <= scale.value.top; v += scale.value.step) out.push({ v, at: pct(v) })
  return out
})
const AXIS = 34
const axisW = computed(() => props.compact ? 0 : AXIS)
const cols = computed(() => ({ gridTemplateColumns: `repeat(${N.value}, minmax(0, 1fr))` }))
const total = computed(() => `${data.value.total.toLocaleString('en-GB')} ${data.value.total === 1 ? 'release' : 'releases'}`)
const firstLabel = computed(() => data.value.firstAt === null ? '' : dayMonth(data.value.firstAt))
const beforeCount = computed(() => slots.value.filter(s => s.before).length)
const beforeNote = computed(() => beforeCount.value >= 3 ? `before the first ${brand.value.short_name} release · ${firstLabel.value}` : '')

// Labels thin out where a column is too narrow for them, counted back from the last.
const chart = ref<HTMLElement>()
const width = ref(0)
const tipBox = ref<HTMLElement>()
const tipWidth = ref(180)
let observer: ResizeObserver | undefined
onMounted(() => {
  if (!chart.value) return
  width.value = chart.value.clientWidth
  observer = new ResizeObserver(entries => {
    for (const entry of entries) {
      if (entry.target === chart.value) width.value = entry.contentRect.width
      if (entry.target === tipBox.value) tipWidth.value = tipBox.value.getBoundingClientRect().width
    }
  })
  observer.observe(chart.value)
  if (tipBox.value) observer.observe(tipBox.value)
})
watch(tipBox, (box, previous) => {
  if (previous) observer?.unobserve(previous)
  if (box) observer?.observe(box)
})
onBeforeUnmount(() => observer?.disconnect())
const thin = computed(() => !dense.value && data.value.range.unit === 'day' && width.value > 0 && (width.value - axisW.value) / N.value < 44)
const labelOf = (s: Slot, i: number) => thin.value && (N.value - 1 - i) % 2 ? '' : s.label

// Hover and focus show one slot; the keyboard keeps one bar in the tab order.
const active = ref<number | null>(null)
const focusAt = ref(0)
const bars: HTMLElement[] = []
watch(slots, (list, previous) => {
  // Counts and relative labels change on clock ticks. Keep the same bar and
  // tooltip unless the calendar window itself moves or the range changes.
  if (previous?.length === list.length && list.every((s, i) => s.start === previous[i]?.start)) return
  const hadFocus = bars[focusAt.value] === document.activeElement
  const nextFocus = list.findIndex(s => s.start === previous?.[focusAt.value]?.start && !s.before)
  const activeStart = active.value === null ? undefined : previous?.[active.value]?.start
  const nextActive = list.findIndex(s => s.start === activeStart && !s.before)
  focusAt.value = nextFocus >= 0 ? nextFocus : list.length - 1
  active.value = nextActive >= 0 ? nextActive : null
  if (hadFocus) void nextTick(() => bars[focusAt.value]?.focus())
}, { immediate: true })
const usable = computed(() => slots.value.flatMap((s, i) => s.before ? [] : [i]))
function keys(event: KeyboardEvent) {
  const list = usable.value
  const pos = list.indexOf(focusAt.value)
  const next = event.key === 'ArrowRight' ? list[Math.min(list.length - 1, pos + 1)]
    : event.key === 'ArrowLeft' ? list[Math.max(0, pos - 1)]
      : event.key === 'Home' ? list[0] : event.key === 'End' ? list[list.length - 1] : undefined
  if (next === undefined) return
  event.preventDefault()
  event.stopPropagation()
  focusAt.value = next
  void nextTick(() => bars[next]?.focus())
}
const count = (s: Slot) => `${s.n.toLocaleString('en-GB')} ${s.n === 1 ? 'release' : 'releases'}${s.current ? ' so far' : ''}`
const aria = (s: Slot) => s.before ? `${s.full}: before the first ${brand.value.short_name} release`
  : `${s.full}: ${count(s)}${s.first ? `, ${s.first}${s.last ? ` to ${s.last}` : ''}` : ''}`
const showValue = (s: Slot, i: number) => s.n > 0 && (!dense.value || active.value === i || i === data.value.peak || s.current)
const barHeight = (s: Slot) => s.before ? undefined : s.n ? `max(2.5%, ${pct(s.n)})` : '0'
const tip = computed(() => active.value === null ? null : slots.value[active.value] ?? null)
const tipStyle = computed(() => {
  const i = active.value ?? 0, s = tip.value
  const pos = (i + .5) / N.value
  const center = axisW.value + (width.value - axisW.value) * pos
  return {
    left: `${Math.max(0, Math.min(width.value - tipWidth.value, center - tipWidth.value / 2))}px`,
    bottom: `calc(${s ? Math.max(8, s.n / scale.value.top * 100) : 0}% + 30px)`,
  }
})
</script>

<template>
  <section class="cadence" :class="{ compact }" aria-label="Release cadence" :style="{ '--axis': `${axisW}px` }">
    <div class="top">
      <p class="title">{{ data.range.title }}</p>
      <div class="stepper" role="group" aria-label="Range">
        <button type="button" class="step" aria-label="Shorter range" :disabled="at === 0" @click="step(-1)"><AppIcon name="chevron-left" :size="15" /></button>
        <span class="range" aria-live="polite">{{ data.range.short }}</span>
        <button type="button" class="step" aria-label="Longer range" :disabled="at === RANGES.length - 1" @click="step(1)"><AppIcon name="chevron-right" :size="15" /></button>
      </div>
    </div>
    <div class="summary">
      <p class="total">{{ total }}</p>
      <div v-if="data.chips.length" class="chips">
        <span v-for="chip in data.chips" :key="chip" class="quiet-chip">{{ chip }}</span>
      </div>
    </div>
    <div ref="chart" class="chart">
      <template v-for="t in ticks" :key="t.v">
        <span class="grid" :class="{ zero: !t.v }" :style="{ bottom: t.at }" aria-hidden="true" />
        <span v-if="!compact" class="tick" :style="{ bottom: `calc(${t.at} - 6px)` }" aria-hidden="true">{{ t.v }}</span>
      </template>
      <p v-if="beforeNote" class="before-note" :style="{ width: `calc((100% - var(--axis)) * ${beforeCount / N})` }" aria-hidden="true">{{ beforeNote }}</p>
      <ol class="bars" :class="{ dense }" role="list" :aria-label="data.aria" :style="cols" @keydown="keys">
        <li
          v-for="(s, i) in slots" :key="`${data.range.key}-${s.start}`" :ref="el => { if (el) bars[i] = el as HTMLElement }"
          class="slot" :class="{ before: s.before, weekend: s.weekend, peak: i === data.peak, current: s.current, active: active === i }"
          :tabindex="s.before ? undefined : i === focusAt ? 0 : -1" :aria-label="aria(s)"
          @mouseenter="active = s.before ? null : i" @mouseleave="active = null" @focus="active = i; focusAt = i" @blur="active = null"
        >
          <span v-if="showValue(s, i)" class="val" aria-hidden="true">{{ s.n }}</span>
          <span class="column" :style="{ height: barHeight(s) }" aria-hidden="true" />
        </li>
      </ol>
      <template v-if="showAvg">
        <span class="avg" :style="{ bottom: pct(data.avg) }" aria-hidden="true" />
        <span class="avg-label" :style="{ bottom: `calc(${pct(data.avg)} + 4px)` }" aria-hidden="true">{{ data.avgLabel }}</span>
      </template>
      <div v-if="tip && !tip.before" ref="tipBox" class="tip" :style="tipStyle" aria-hidden="true">
        <p class="tip-title">{{ tip.full }}</p>
        <p class="tip-count">{{ count(tip) }}</p>
        <p v-if="tip.first" class="tip-range">{{ tip.first }}<template v-if="tip.last"><br><AppIcon name="arrow" :size="11" class="tip-arrow" />{{ tip.last }}</template></p>
      </div>
    </div>
    <div class="labels" :class="{ dense }" :style="cols" aria-hidden="true">
      <span v-for="(s, i) in slots" :key="`${data.range.key}-${i}`" :class="{ current: s.current, before: s.before }">{{ labelOf(s, i) }}</span>
    </div>
  </section>
</template>

<style scoped>
.cadence {
  display: flex; flex-direction: column; min-width: 0; padding: 20px 22px 16px; border-radius: 20px;
  background: var(--glass); -webkit-backdrop-filter: blur(14px) saturate(1.3); backdrop-filter: blur(14px) saturate(1.3);
  box-shadow: 0 0 0 1px var(--line), inset 0 1px 0 var(--glass-edge), 0 14px 34px -20px var(--card-glow, rgba(14, 111, 108, .35));
}
.top { display: flex; align-items: flex-start; gap: 12px; }
.title { margin: 6px 0 0; font: 500 10.5px/1.4 var(--mono); letter-spacing: .16em; text-transform: uppercase; color: var(--ink-3); }
.stepper { display: flex; align-items: center; gap: 2px; margin-left: auto; padding: 3px; border-radius: 999px; background: var(--surface-2); }
.step { display: flex; align-items: center; justify-content: center; width: 30px; height: 30px; padding: 0; border: 0; border-radius: 999px; background: transparent; color: var(--ink-2); cursor: pointer; }
@media (hover: hover) { .step:not(:disabled):hover { background: var(--row-hover); color: var(--teal-ink); } }
.step:focus-visible { outline: none; box-shadow: var(--focus-ring); }
.step:disabled { color: color-mix(in srgb, var(--ink) 25%, transparent); cursor: default; }
.range { min-width: 78px; text-align: center; font: 600 13px/1 var(--font); color: var(--ink); }
.summary { display: flex; flex-wrap: wrap; align-items: baseline; gap: 8px 12px; margin-top: 6px; }
.total { margin: 0; font: 650 30px/1.2 var(--font); letter-spacing: -.025em; font-variant-numeric: tabular-nums; color: var(--ink); }
.chips { display: flex; flex-wrap: wrap; gap: 6px; }
.quiet-chip { display: inline-flex; align-items: center; height: 26px; padding: 0 10px; border-radius: 999px; background: var(--surface-2); color: var(--ink-2); font: 600 12.5px/1 var(--font); white-space: nowrap; }
.chart { position: relative; height: clamp(104px, 17vh, 176px); margin-top: 22px; }
.grid { position: absolute; left: var(--axis); right: 0; border-top: 1px solid var(--line); }
.grid.zero { border-top-color: var(--line-2); }
.tick { position: absolute; left: 0; width: 26px; text-align: right; font: 500 10px/1 var(--mono); font-variant-numeric: tabular-nums; color: var(--ink-3); }
.before-note { position: absolute; left: var(--axis); bottom: 18%; margin: 0; padding: 0 6px; text-align: center; font: 500 12px/1.4 var(--font); color: var(--ink-3); pointer-events: none; }
.bars { position: absolute; inset: 0 0 0 var(--axis); display: grid; align-items: end; column-gap: 6px; margin: 0; padding: 0; list-style: none; }
.bars.dense, .labels.dense { column-gap: 2px; }
.slot { position: relative; display: flex; flex-direction: column; justify-content: flex-end; align-items: center; height: 100%; min-width: 0; border-radius: 8px; outline: none; cursor: pointer; }
.slot.weekend { background: color-mix(in srgb, var(--ink) 3.5%, transparent); }
.slot.active { background: color-mix(in srgb, var(--teal) 7%, transparent); }
.slot:focus-visible { box-shadow: var(--focus-ring); }
.slot.before { cursor: default; }
.column { display: block; width: 46%; min-width: 3px; max-width: 30px; border-radius: 7px 7px 2px 2px; background: color-mix(in srgb, var(--teal) 38%, transparent); }
.dense .column { width: 64%; border-radius: 3px 3px 1px 1px; }
.peak .column { background: color-mix(in srgb, var(--teal) 72%, transparent); }
/* Today, still filling: solid and ringed. */
.current .column { background: var(--teal); box-shadow: 0 0 0 3px color-mix(in srgb, var(--teal) 14%, transparent); }
.active .column { background: var(--teal-ink); box-shadow: none; }
/* Before the first release: a dashed stroke on the baseline, never a zero. */
.before .column { width: 70%; height: 2px; border-radius: 0; background: repeating-linear-gradient(90deg, var(--line-2) 0 4px, transparent 4px 7px); }
.val { margin-bottom: 4px; font: 500 11.5px/1 var(--mono); font-variant-numeric: tabular-nums; color: var(--ink-3); }
.dense .val { font-size: 10px; }
.peak .val { color: var(--ink); font-weight: 700; }
.current .val, .active .val { color: var(--teal-ink); font-weight: 700; }
.avg { position: absolute; left: var(--axis); right: 0; height: 1.5px; margin-bottom: -.75px; background: repeating-linear-gradient(90deg, color-mix(in srgb, var(--gold-ink) 75%, transparent) 0 5px, transparent 5px 9px); pointer-events: none; }
.avg-label { position: absolute; right: 2px; padding: 1px 6px; border-radius: 6px; background: color-mix(in srgb, var(--surface) 92%, transparent); font: 600 10.5px/1.4 var(--mono); color: var(--gold-ink); pointer-events: none; }
.tip { position: absolute; z-index: 2; width:max-content; min-width:min(180px,100%); max-width:100%; padding: 10px 12px; border-radius: 12px; background: var(--tip-bg); color: var(--tip-ink); box-shadow: 0 8px 24px rgba(16, 35, 39, .25); pointer-events: none; }
.tip-title { margin: 0; font: 600 13px/1.3 var(--font); color: var(--tip-ink); }
.tip-count { margin: 2px 0 0; font: 700 18px/1.3 var(--font); letter-spacing: -.01em; color: var(--tip-ink); }
.tip-range { margin: 6px 0 0; font: 500 11.5px/1.45 var(--mono); color: color-mix(in srgb, var(--tip-ink) 80%, transparent); overflow-wrap:anywhere; }
.tip-arrow { display: inline-block; margin-right: 5px; vertical-align: -1px; }
.labels { display: grid; column-gap: 6px; margin: 8px 0 0 var(--axis); }
.labels span { min-width: 0; overflow: visible; text-align: center; white-space: nowrap; font: 500 11px/1.2 var(--mono); color: var(--ink-3); }
.labels .current { font-weight: 700; color: var(--teal-ink); }
/* Phones: no axis labels or average line, 44 px steps, a lower chart. */
.compact { padding: 16px 14px 12px 16px; }
.compact .step { width: 44px; height: 44px; }
.compact .range { min-width: 64px; }
.compact .total { font-size: 24px; }
.compact .chart { height: 130px; margin-top: 16px; }
.compact .labels span { font-size: 10px; }
@media (hover: none), (pointer: coarse) { .step { width: 44px; height: 44px; } }
/* Short screens keep the list in view: a tighter card. */
@media (max-height: 800px) and (min-width: 761px) {
  .cadence:not(.compact) { padding: 16px 20px 12px; }
  .cadence:not(.compact) .total { font-size: 26px; }
  .cadence:not(.compact) .chart { margin-top: 14px; }
}
@media (prefers-reduced-motion: no-preference) { .column { transition: background .15s ease, height .3s ease; } }
</style>
