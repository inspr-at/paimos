<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// One trend card (AEON-994 draft 1): p50 line, p50–p90 band (fading when p90 is
// clipped), dashed Arion target, tinted partial and hatched no-data regions.
// Hover and arrow keys move one selection; the readout line above the plot
// says what it is, so nothing reflows. Widths are measured, never assumed.
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { regions, type ChartModel, type ChartPanel } from '../../lib/deliveryNumbers'
import type { DeliveryText } from '../../lib/deliveryNumbersText'
import { vClipTip } from '../../directives/clipTip'
import NightlyStrip from './NightlyStrip.vue'

const props = defineProps<{ model: ChartModel; state: 'loading' | 'error' | 'ready'; text: DeliveryText }>()
const plot = ref<HTMLElement>()
const width = ref(0)
const selected = ref<number | null>(null)
let observer: ResizeObserver | null = null
onMounted(() => {
  observer = new ResizeObserver(entries => { width.value = Math.round(entries[0]?.contentRect.width ?? 0) })
  if (plot.value) { observer.observe(plot.value); width.value = Math.round(plot.value.clientWidth) }
})
onBeforeUnmount(() => observer?.disconnect())
// Another window or a new answer has other buckets: the selection belongs to the old ones.
watch(() => props.model.buckets, () => { selected.value = null })

const nightly = computed(() => props.model.kind === 'nightly')
const H = computed(() => nightly.value ? 70 : 156)
const W = computed(() => Math.max(160, width.value))
const n = computed(() => props.model.buckets.length)
const Lm = computed(() => nightly.value ? 4 : 34), Rm = 6, Tm = 8, Bm = 18
const pw = computed(() => W.value - Lm.value - Rm)
const ph = computed(() => H.value - Tm - Bm)
const step = computed(() => pw.value / Math.max(1, n.value))
const X = (k: number) => Lm.value + (k + 0.5) * step.value
const ready = computed(() => props.state === 'ready')

// Nightly: squares of at most 40 px, left-aligned.
const squares = computed(() => {
  if (!nightly.value || !n.value) return []
  const gap = n.value === 7 ? 12 : 6
  const s = Math.max(4, Math.min(40, Math.floor((W.value - Lm.value - 8 - gap * (n.value - 1)) / n.value)))
  return props.model.buckets.map((_, k) => ({ x: Lm.value + k * (s + gap), y: 6, s }))
})

interface Box { y: number; h: number }
const boxes = computed<Box[]>(() => {
  const panels = props.model.panels
  if (panels.length < 2) return [{ y: Tm, h: ph.value }]
  const first = Math.round(ph.value * panels[0].share)
  return [{ y: Tm, h: first }, { y: Tm + first + 22, h: ph.value - first - 22 }]
})
const fmt = (value: number) => Number.isInteger(value) ? String(value) : value.toFixed(1)
interface Dot { x: number; y: number; partial: boolean; tone: string; shown: boolean; last: boolean }
function drawPanel(panel: ChartPanel, box: Box, main: boolean) {
  const Y = (value: number) => box.y + box.h - (Math.max(0, Math.min(panel.top, value)) / panel.top) * box.h
  const buckets = props.model.buckets
  const ticks = (box.h > 60 ? [0, panel.top / 2, panel.top] : [0, panel.top]).map(value => ({ value, y: Y(value), label: fmt(value) }))
  const zones = regions(buckets).map(zone => {
    const x = Lm.value + zone.start * step.value, w = (zone.end - zone.start) * step.value
    const label = zone.status === 'partial' ? props.text.partial : props.text.noData
    return { x, w, status: zone.status, label: main && w > label.length * 5.8 + 12 ? label : '' }
  })
  const targets = panel.targets.map(target => ({ ...target, y: Y(target.value) }))
  const bands: string[] = []
  if (panel.band && ready.value) {
    let run: [number, number, number][] = []
    const flush = () => {
      if (run.length === 1) { const [x, low, high] = run[0]; bands.push(`M${x - 3},${high}H${x + 3}V${low}H${x - 3}Z`) }
      else if (run.length > 1) bands.push(`M${run.map(([x, , high]) => `${x},${high}`).join('L')}L${run.slice().reverse().map(([x, low]) => `${x},${low}`).join('L')}Z`)
      run = []
    }
    panel.band.forEach((pair, k) => { if (!pair) flush(); else run.push([X(k), Y(pair[0]), Y(pair[1])]) })
    flush()
  }
  const sparse = props.model.kind === 'release' || props.model.kind === 'merge' || n.value <= 13
  const lines = ready.value ? panel.lines.map(line => {
    const points = line.values.map((value, k) => value == null ? null : { x: X(k), y: Y(value), partial: buckets[k]?.status === 'partial' })
    const segments: { d: string; partial: boolean }[] = []
    if (line.connect) for (let k = 1; k < points.length; k++) {
      const a = points[k - 1], b = points[k]
      if (a && b) segments.push({ d: `M${a.x},${a.y}L${b.x},${b.y}`, partial: a.partial || b.partial })
    }
    const dots: Dot[] = points.flatMap((point, k) => point ? [{ ...point, tone: line.tone, last: k === points.length - 1,
      shown: point.partial || sparse || !line.connect || (!points[k - 1] && !points[k + 1]) || k === points.length - 1 }] : [])
    return { tone: line.tone, segments, dots, points }
  }) : []
  // The main target label sits above its line unless data is there; then below.
  const labels = targets.map(target => {
    if (!main) return { label: target.label, x: Lm.value + pw.value - 2, y: box.y - 7 }
    const wide = target.label.length * 5.9 + 6, x0 = Lm.value + pw.value - wide
    const near = lines.flatMap(line => line.dots).filter(dot => dot.x >= x0 - 6)
    const hit = (y: number) => near.some(dot => dot.y > y - 13 && dot.y < y + 5)
    const above = target.y - 5, below = target.y + 13
    return { label: target.label, x: Lm.value + pw.value - 2, y: (above < box.y + 10 || hit(above)) && !hit(below) && below < box.y + box.h + 4 ? below : above }
  })
  return { box, ticks, zones, targets, labels, bands, lines, caption: panel.caption }
}
const drawn = computed(() => width.value ? props.model.panels.map((panel, index) => drawPanel(panel, boxes.value[index] ?? boxes.value[0], index === 0)) : [])
const axis = computed(() => nightly.value ? [] : props.model.buckets.flatMap((bucket, k) => {
  if (!bucket.show) return []
  const last = k === n.value - 1
  return [{ label: bucket.label, last, x: last ? Math.min(X(k) + step.value / 2, W.value - Rm) : Math.max(Lm.value + 18, Math.min(X(k), W.value - Rm - 44)) }]
}))

const readout = computed(() => {
  if (props.state === 'loading') return ' '
  if (props.state === 'error') return props.text.notLoaded
  if (!n.value) return props.text.noData
  return props.model.readouts[selected.value ?? n.value - 1] ?? ' '
})
function indexAt(event: PointerEvent): number | null {
  if (!plot.value || !n.value || !ready.value) return null
  const rect = plot.value.getBoundingClientRect(), x = (event.clientX - rect.left) * (W.value / rect.width)
  const stride = nightly.value ? (squares.value[1]?.x ?? squares.value[0].x + squares.value[0].s) - squares.value[0].x : step.value
  return Math.max(0, Math.min(n.value - 1, Math.floor((x - Lm.value) / stride)))
}
function point(event: PointerEvent) { const index = indexAt(event); if (index !== null) selected.value = index }
function leave(event: PointerEvent) { if (document.activeElement !== plot.value && !plot.value?.contains(event.relatedTarget as Node)) selected.value = null }
function focus() { if (ready.value && n.value) selected.value ??= n.value - 1 }
function key(event: KeyboardEvent) {
  if (!ready.value || !n.value || event.altKey || event.ctrlKey || event.metaKey) return
  const current = selected.value ?? n.value - 1
  const next = { ArrowLeft: current - 1, ArrowRight: current + 1, Home: 0, End: n.value - 1 }[event.key]
  if (next === undefined) return
  event.preventDefault()
  selected.value = Math.max(0, Math.min(n.value - 1, next))
}
const selectedDots = computed(() => selected.value === null ? [] : drawn.value.flatMap(panel => panel.lines.flatMap(line => {
  const at = line.points[selected.value!]
  return at ? [{ x: at.x, y: at.y, tone: line.tone }] : []
})))
const id = computed(() => `dl-chart-${props.model.key}`)
</script>

<template>
  <article class="chart-card" :class="{ wide: nightly }" :aria-labelledby="`${id}-title`">
    <div class="c-head">
      <h4 :id="`${id}-title`">{{ model.label }}</h4>
      <span v-if="model.legend === 'two'" class="c-key">
        <span><svg class="lg-sw" width="14" height="8" aria-hidden="true"><line x1="0" y1="4" x2="14" y2="4" class="g-line" /></svg>{{ text.firstTry }}</span>
        <span><svg class="lg-sw" width="14" height="8" aria-hidden="true"><line x1="0" y1="4" x2="14" y2="4" class="g-line gold" /></svg>{{ text.required }}</span>
      </span>
      <span v-if="model.legend === 'nightly'" class="n-legend" aria-hidden="true">
        <span><svg width="12" height="12"><rect width="12" height="12" rx="3" class="n-key ok" /></svg>{{ text.green }}</span>
        <span><svg width="12" height="12"><rect width="12" height="12" rx="3" class="n-key bad" /></svg>{{ text.red }}</span>
        <span><svg width="12" height="12"><rect x=".5" y=".5" width="11" height="11" rx="3" class="n-key none" /></svg>{{ text.noRun }}</span>
      </span>
      <span v-else class="c-unit">{{ model.unit }}</span>
    </div>
    <p :id="`${id}-readout`" v-clip-tip class="readout" aria-live="polite">{{ readout }}</p>
    <div ref="plot" class="plot" :class="{ nightly }" tabindex="0" role="group" aria-roledescription="chart" :aria-label="`${text.chartOf} ${model.label}. ${text.keys}`"
      :aria-describedby="`${id}-readout`" @pointermove="point" @pointerdown="point" @pointerleave="leave" @focus="focus" @blur="selected = null" @keydown="key">
      <span v-if="state === 'loading'" class="sk plot-sk" />
      <svg v-else-if="width" :viewBox="`0 0 ${W} ${H}`" :width="W" :height="H" aria-hidden="true">
        <NightlyStrip v-if="nightly" :squares="squares" :nights="model.nights" :buckets="model.buckets" :selected="selected" :muted="state !== 'ready'" />
        <template v-else>
          <rect v-if="!n" :x="Lm" :y="Tm" :width="pw" :height="ph" rx="4" class="g-nodata" />
          <text v-if="!n || state === 'error'" :x="Lm + pw / 2" :y="Tm + ph / 2" class="g-rlabel" text-anchor="middle">{{ state === 'error' ? text.notLoaded : text.noData }}</text>
          <g v-for="(panel, index) in drawn" :key="index">
            <template v-if="ready">
              <g v-for="(zone, z) in panel.zones" :key="z">
                <rect :x="zone.x" :y="panel.box.y" :width="zone.w" :height="panel.box.h" rx="4" :class="zone.status === 'partial' ? 'g-partial' : 'g-nodata'" />
                <text v-if="zone.label" :x="zone.x + 6" :y="panel.box.y + 13" class="g-rlabel" :class="{ p: zone.status === 'partial' }">{{ zone.label }}</text>
              </g>
            </template>
            <g v-for="tick in panel.ticks" :key="tick.value">
              <line :x1="Lm" :x2="Lm + pw" :y1="tick.y" :y2="tick.y" :class="tick.value === 0 ? 'g-base' : 'g-grid'" />
              <text :x="Lm - 6" :y="tick.y + 3.5" class="ax" text-anchor="end">{{ tick.label }}</text>
            </g>
            <path v-for="(band, b) in panel.bands" :key="`b${b}`" :d="band" class="g-band" :class="{ fade: model.panels[index].fade }" />
            <line v-for="(target, t) in panel.targets" :key="`t${t}`" :x1="Lm" :x2="Lm + pw" :y1="target.y" :y2="target.y" class="g-target" />
            <template v-for="(line, l) in panel.lines" :key="`l${l}`">
              <path v-for="(segment, s) in line.segments" :key="s" :d="segment.d" class="g-line" :class="[line.tone, { part: segment.partial }]" />
              <template v-for="(dot, d) in line.dots" :key="`d${d}`">
                <circle v-if="dot.partial" :cx="dot.x" :cy="dot.y" r="3" class="g-dot hollow" :class="line.tone" />
                <circle v-else-if="dot.shown" :cx="dot.x" :cy="dot.y" :r="dot.last ? 4 : 3" class="g-dot" :class="line.tone" />
              </template>
            </template>
            <text v-if="panel.caption" :x="Lm + 2" :y="panel.box.y - 7" class="g-rlabel">{{ panel.caption }}</text>
            <text v-for="(label, t) in panel.labels" :key="`tl${t}`" :x="label.x" :y="label.y" class="g-tlabel" text-anchor="end">{{ label.label }}</text>
          </g>
          <text v-for="tick in axis" :key="tick.x" :x="tick.x" :y="H - 4" class="ax" :class="{ on: tick.last }" :text-anchor="tick.last ? 'end' : 'middle'">{{ tick.label }}</text>
          <g v-if="selected !== null">
            <line :x1="X(selected)" :x2="X(selected)" :y1="Tm" :y2="Tm + ph" class="g-cross" />
            <circle v-for="(dot, d) in selectedDots" :key="d" :cx="dot.x" :cy="dot.y" r="4.5" class="g-dot big" :class="dot.tone" />
          </g>
        </template>
      </svg>
    </div>
    <div class="c-foot"><span>{{ state === 'ready' ? model.foot : model.foot.split(' · ')[0] }}</span></div>
  </article>
</template>

<style scoped>
.chart-card { position: relative; display: flex; flex-direction: column; gap: 4px; min-width: 0; padding: 12px 14px 10px 16px; border-radius: 16px; background: var(--glass); -webkit-backdrop-filter: blur(14px) saturate(1.3); backdrop-filter: blur(14px) saturate(1.3); box-shadow: 0 0 0 1px var(--line), inset 0 1px 0 var(--glass-edge), 0 14px 34px -22px color-mix(in srgb, var(--primary-line) 35%, transparent); }
.chart-card.wide { grid-column: 1 / -1; }
.c-head { display: flex; align-items: center; gap: 8px; min-height: 24px; }
.c-head h4 { flex: 1; min-width: 0; margin: 0; font: 600 13.5px/1.3 var(--font); color: var(--ink); overflow-wrap: anywhere; }
.c-unit { font: 500 11px/1 var(--mono); color: var(--ink-3); white-space: nowrap; }
.c-key { display: inline-flex; gap: 10px; font-size: 11.5px; color: var(--ink-2); white-space: nowrap; }
.c-key > span, .n-legend > span { display: inline-flex; align-items: center; gap: 5px; }
.n-legend { display: flex; gap: 14px; margin-left: auto; font-size: 11.5px; color: var(--ink-2); }
.readout { height: 18px; margin: 0; overflow: hidden; font: 500 11px/18px var(--mono); font-variant-numeric: tabular-nums; color: var(--ink-2); white-space: nowrap; text-overflow: ellipsis; }
.plot { position: relative; height: 156px; margin: 2px -4px 0; border-radius: 10px; cursor: crosshair; touch-action: pan-y; }
.plot.nightly { height: 70px; cursor: default; }
.plot:focus-visible { outline: none; box-shadow: var(--focus-ring); }
.plot svg { display: block; width: 100%; height: 100%; overflow: visible; }
.plot-sk { position: absolute; inset: 8px 4px 20px 36px; }
.c-foot { display: flex; gap: 6px; align-items: center; min-height: 18px; font-size: 11.5px; line-height: 18px; color: var(--ink-3); }
.lg-sw { display: inline-block; flex: none; }
.ax { font: 500 10px var(--mono); fill: var(--ink-3); font-variant-numeric: tabular-nums; }
.ax.on { fill: var(--ink-2); font-weight: 600; }
.g-grid { stroke: var(--line); stroke-width: 1; }
.g-base { stroke: var(--line-2); stroke-width: 1; }
.g-band { fill: color-mix(in srgb, var(--teal) 13%, transparent); }
.g-band.fade { fill: url(#dl-fade); }
.g-line { fill: none; stroke: var(--teal); stroke-width: 2; stroke-linejoin: round; stroke-linecap: round; }
.g-line.gold { stroke: var(--gold); }
.g-line.part { stroke-dasharray: 2 3.5; }
.g-dot { fill: var(--teal); stroke: var(--surface-raised); stroke-width: 2; }
.g-dot.gold { fill: var(--gold); }
.g-dot.hollow { fill: var(--surface-raised); stroke: var(--teal); stroke-width: 1.6; }
.g-dot.hollow.gold { stroke: var(--gold); }
.g-dot.big { stroke-width: 2.5; }
.g-target { stroke: var(--ink-2); stroke-width: 1.5; stroke-dasharray: 5 4; fill: none; }
.g-tlabel { font: 600 10.5px var(--font); fill: var(--ink-2); paint-order: stroke; stroke: var(--surface-raised); stroke-width: 3px; stroke-linejoin: round; }
.g-nodata { fill: url(#dl-hatch); }
.g-partial { fill: color-mix(in srgb, var(--gold) 9%, transparent); }
.g-rlabel { font: 500 10.5px var(--font); fill: var(--ink-3); }
.g-rlabel.p { fill: var(--queue-wait-ink); }
.g-cross { stroke: var(--ink-3); stroke-width: 1; }
.n-key.ok { fill: var(--ok); }
.n-key.bad { fill: var(--danger); }
.n-key.none { fill: transparent; stroke: var(--line-2); stroke-width: 1; }
.sk { display: block; border-radius: 6px; background: var(--skeleton); }
@media (prefers-reduced-motion: no-preference) {
  .sk { background: linear-gradient(90deg, var(--skeleton) 0%, var(--skeleton-hi) 50%, var(--skeleton) 100%) 0 0 / 200% 100%; animation: dl-sk 1.4s ease-in-out infinite; }
}
@keyframes dl-sk { to { background-position: -200% 0; } }
@container delivery (max-width: 640px) {
  .c-head { flex-wrap: wrap; }
  .c-key { order: 3; flex-basis: 100%; }
  .n-legend { display: none; }
  /* Two reserved lines on phones: a long readout wraps without moving the plot. */
  .readout { display: -webkit-box; height: 36px; white-space: normal; -webkit-line-clamp: 2; -webkit-box-orient: vertical; }
}
</style>
