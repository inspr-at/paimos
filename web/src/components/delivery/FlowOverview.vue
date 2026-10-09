<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// Delivery › Flow overview map (AEON-994 draft 5, package 5): the whole run in 64 px
// with a brush for the visible window. Drag the brush to pan, its edges to resize,
// click beside it to centre it there; the overview playhead moves the time.
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { panBy, setWindow, tickStep, timeLabel, type FlowData, type LaneSet, type Timeline } from '../../lib/deliveryFlow'
import type { FlowText } from '../../lib/deliveryFlowText'

const props = defineProps<{ data: FlowData; sets: LaneSet[]; timeline: Timeline; text: FlowText }>()

const OV_H = 64
const host = ref<HTMLElement>()
const width = ref(0)
let observer: ResizeObserver | null = null
onMounted(() => {
  width.value = host.value?.clientWidth ?? 0
  observer = new ResizeObserver(() => { width.value = host.value?.clientWidth ?? 0 })
  if (host.value) observer.observe(host.value)
})
onBeforeUnmount(() => observer?.disconnect())

const geo = computed(() => {
  const t = props.timeline, W = width.value, narrow = W < 640, x0 = narrow ? 84 : 156, x1 = Math.max(x0 + 1, W - (narrow ? 6 : 12)), span = t.r1 - t.r0
  return { W, x0, x1, X: (m: number) => x0 + (m - t.r0) / span * (x1 - x0), inv: (x: number) => t.r0 + (x - x0) / (x1 - x0) * span }
})
const map = computed(() => {
  const g = geo.value, sets = props.sets, live = props.data.now != null, now = props.data.now ?? 0
  const laneCount = sets.reduce((n, s) => n + s.lanes.length, 0), lh = Math.min(7, (OV_H - 26) / Math.max(1, laneCount))
  const lines: { key: string; y: number }[] = [], segs: { key: string; x: number; y: number; w: number; h: number; cls: string }[] = []
  const bands: { key: string; x: number; y: number; w: number; h: number }[] = [], targets: { key: string; x: number; y: number; w: number }[] = []
  let y = 8
  sets.forEach((set, si) => {
    const main = set.main
    if (main.incident) bands.push({ key: `${si}`, x: g.X(main.incident.start), y: y - 2, w: g.X(main.incident.end) - g.X(main.incident.start), h: set.lanes.length * lh + 3 })
    set.lanes.forEach((lane, k) => {
      const ly = y + k * lh
      lines.push({ key: `${si}:${lane}`, y: ly + lh / 2 })
      for (const item of set.items.filter(i => i.lane === lane)) {
        const s = item.step, fut = live && s.start >= now
        segs.push({ key: `${si}:${item.run.id}:${item.run.steps.indexOf(s)}`, x: g.X(s.start), y: ly + 0.5, w: Math.max(1, g.X(s.end) - g.X(s.start)), h: lh - 1.5, cls: `ln-ovseg ${s.kind}${s.incident ? ' inc' : ''}${fut ? ' fut' : ''}${item.run.isTarget ? ' tgt' : ''}` })
      }
    })
    if (main.target && props.data.origin != null) targets.push({ key: `${si}`, x: g.X(main.target.start), y: y + set.lanes.length * lh + 2, w: g.X(main.target.start + main.target.minutes) - g.X(main.target.start) })
    y += set.lanes.length * lh + 6
  })
  const t = props.timeline, step = tickStep(t.r1 - t.r0, g.x1 - g.x0) * 2, ticks: { x: number; label: string }[] = []
  for (let m = Math.ceil(t.r0 / step) * step; m <= t.r1; m += step) ticks.push({ x: g.X(m), label: timeLabel(props.data, m) })
  return { lines, segs, bands, targets, ticks, nowX: live ? g.X(now) : null }
})
const brush = computed(() => { const g = geo.value, t = props.timeline; return { x0: g.X(t.v0), x1: g.X(t.v1) } })
const playX = computed(() => geo.value.X(props.timeline.T))
const valueText = computed(() => `${timeLabel(props.data, props.timeline.v0)} – ${timeLabel(props.data, props.timeline.v1)}`)

type Drag = { kind: 'ovph' | 'hl' | 'hr' | 'brush' | 'bg'; x: number; v0: number; v1: number; moved: boolean; id: number }
let drag: Drag | null = null
const localX = (event: PointerEvent) => event.clientX - (host.value?.getBoundingClientRect().left ?? 0)
function onDown(event: PointerEvent) {
  if (event.button !== 0) return
  const part = (event.target as Element).closest?.('[data-part]')?.getAttribute('data-part') as Drag['kind'] | null
  const t = props.timeline
  drag = { kind: part ?? 'bg', x: localX(event), v0: t.v0, v1: t.v1, moved: part === 'ovph', id: event.pointerId }
  if (part === 'ovph') { t.follow = false; event.preventDefault() }
  host.value?.setPointerCapture?.(event.pointerId)
}
function onMove(event: PointerEvent) {
  const d = drag
  if (!d || d.id !== event.pointerId) return
  const x = localX(event), dx = x - d.x, t = props.timeline, g = geo.value
  if (Math.abs(dx) > 3) d.moved = true
  if (!d.moved) return
  t.follow = false
  const per = (t.r1 - t.r0) / (g.x1 - g.x0), w = d.v1 - d.v0
  if (d.kind === 'ovph') {
    t.T = Math.min(t.r1, Math.max(t.r0, g.inv(x)))
    if (t.T < t.v0 || t.T > t.v1) setWindow(t, t.v1 - t.v0, t.T, 0.5)
  } else if (d.kind === 'hl') t.v0 = Math.min(d.v1 - 5, Math.max(t.r0, d.v0 + dx * per))
  else if (d.kind === 'hr') t.v1 = Math.min(t.r1, Math.max(d.v0 + 5, d.v1 + dx * per))
  else { t.v0 = Math.min(t.r1 - w, Math.max(t.r0, d.v0 + dx * per)); t.v1 = t.v0 + w }
}
function onUp(event: PointerEvent) {
  const d = drag
  if (!d || d.id !== event.pointerId) return
  drag = null
  if (d.moved || d.kind !== 'bg') return
  props.timeline.follow = false
  setWindow(props.timeline, props.timeline.v1 - props.timeline.v0, geo.value.inv(localX(event)), 0.5)
}
function onKey(event: KeyboardEvent) {
  if (event.altKey || event.ctrlKey || event.metaKey || event.shiftKey) return
  const t = props.timeline, w = t.v1 - t.v0
  const move = { ArrowLeft: -w * 0.1, ArrowDown: -w * 0.1, ArrowRight: w * 0.1, ArrowUp: w * 0.1, Home: t.r0 - t.v0, End: t.r1 - t.v1 }[event.key]
  if (move === undefined) return
  event.preventDefault()
  t.follow = false
  panBy(t, move)
}
</script>

<template>
  <div ref="host" class="fl-ov" tabindex="0" role="slider" :aria-label="text.overviewLabel" :aria-valuemin="Math.round(timeline.r0)" :aria-valuemax="Math.round(timeline.r1)"
    :aria-valuenow="Math.round(timeline.v0)" :aria-valuetext="valueText" data-testid="flow-overview"
    @pointerdown="onDown" @pointermove="onMove" @pointerup="onUp" @pointercancel="drag = null" @keydown="onKey">
    <svg v-if="width" :viewBox="`0 0 ${geo.W} ${OV_H}`" :width="geo.W" :height="OV_H" aria-hidden="true">
      <text class="ln-ovt" x="8" y="14">{{ text.overview }}</text>
      <rect v-for="b in map.bands" :key="b.key" class="ov-inc" :x="b.x" :y="b.y" :width="b.w" :height="b.h" rx="2" />
      <line v-for="l in map.lines" :key="l.key" class="ln-ovlane" :x1="geo.x0" :x2="geo.x1" :y1="l.y" :y2="l.y" />
      <rect v-for="s in map.segs" :key="s.key" :class="s.cls" :x="s.x" :y="s.y" :width="s.w" :height="s.h" />
      <rect v-for="tg in map.targets" :key="tg.key" class="ov-tgt" :x="tg.x" :y="tg.y" :width="tg.w" height="2.5" rx="1" />
      <text v-for="tick in map.ticks" :key="tick.x" class="ln-ovax" :x="tick.x" :y="OV_H - 3" text-anchor="middle">{{ tick.label }}</text>
      <line v-if="map.nowX != null" class="ov-now" :x1="map.nowX" :x2="map.nowX" y1="4" :y2="OV_H - 14" />
      <rect class="ln-brush" data-part="brush" :x="brush.x0" y="1" :width="Math.max(6, brush.x1 - brush.x0)" :height="OV_H - 15" rx="4" />
      <g v-for="[x, side] in ([[brush.x0, 'hl'], [brush.x1, 'hr']] as const)" :key="side">
        <rect class="ln-handle" :data-part="side" :x="x - 4" y="10" width="8" :height="OV_H - 33" rx="3" />
        <path class="ln-grip" :d="`M${x - 1},${(OV_H - 14) / 2 - 4}v8M${x + 1},${(OV_H - 14) / 2 - 4}v8`" />
      </g>
      <line class="ln-ph" :x1="playX" :x2="playX" y1="1" :y2="OV_H - 14" />
      <path class="ln-phtri" :d="`M${playX - 5},1h10l-5,6z`" />
      <rect class="ln-phhit" data-part="ovph" :x="playX - 7" y="0" width="14" :height="OV_H - 14" />
    </svg>
  </div>
</template>

<style scoped>
.fl-ov { position: relative; height: 64px; margin-top: 2px; border-radius: 10px; background: var(--surface-sunken); cursor: pointer; touch-action: none; -webkit-user-select: none; user-select: none; }
.fl-ov:focus-visible { outline: none; box-shadow: var(--focus-ring); }
.fl-ov svg { display: block; width: 100%; height: 100%; }
.ln-ovt { font: 600 10px var(--mono); letter-spacing: .1em; text-transform: uppercase; fill: var(--ink-3); }
.ln-ovlane { stroke: var(--line); stroke-width: 1; }
.ln-ovseg.work { fill: color-mix(in srgb, var(--teal) 55%, transparent); }
.ln-ovseg.wait { fill: color-mix(in srgb, var(--gold) 70%, transparent); }
.ln-ovseg.rework { fill: color-mix(in srgb, var(--danger) 55%, transparent); }
.ln-ovseg.inc { fill: color-mix(in srgb, var(--danger) 75%, transparent); }
.ln-ovseg.fut { fill-opacity: .4; }
.ln-ovseg.tgt { fill: color-mix(in srgb, var(--ok) 60%, transparent); }
.ov-inc { fill: color-mix(in srgb, var(--danger) 11%, transparent); stroke: color-mix(in srgb, var(--danger) 55%, transparent); stroke-width: 1; }
.ov-tgt { fill: color-mix(in srgb, var(--ok) 55%, transparent); }
.ov-now { stroke: var(--ink); stroke-width: 1.5; }
.ln-ovax { font: 500 9.5px var(--mono); fill: var(--ink-3); }
.ln-brush { fill: color-mix(in srgb, var(--teal) 9%, transparent); stroke: var(--teal); stroke-width: 1.5; cursor: grab; }
.ln-handle { fill: var(--surface-raised); stroke: var(--teal); stroke-width: 1.2; cursor: ew-resize; }
.ln-grip { stroke: var(--teal-ink); stroke-width: 1; pointer-events: none; }
.ln-ph { stroke: var(--teal); stroke-width: 2; pointer-events: none; }
.ln-phtri { fill: var(--teal); pointer-events: none; }
.ln-phhit { fill: transparent; cursor: ew-resize; }
</style>
