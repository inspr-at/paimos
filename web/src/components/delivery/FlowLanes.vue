<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// Delivery › Flow detail lanes (AEON-994 draft 5, package 5). One lane per actor,
// rows reserved for the whole data set, so zooming and panning never move a lane.
// Only the playhead moves the time: drag its pill, or Shift+←/→. A click selects a
// step (or clears) and never moves the time; a "+N" click zooms in. Dragging empty
// space or a sideways wheel pans; ⌘/Ctrl+wheel zooms around the pointer.
import { computed, onBeforeUnmount, onMounted, ref, useId } from 'vue'
import AppIcon from '../AppIcon.vue'
import type { DeliveryLanguage } from '../../lib/delivery'
import {
  criticalPath, fitLabel, keyInput, laneFrame, minutesText, pick, placeIncidentCaption, rowPieces, setTime, setWindow, tickStep, timeLabel, wheelInput,
  type FlowData, type FlowLevel, type FlowStep, type LaneItem, type LaneSet, type Timeline,
} from '../../lib/deliveryFlow'
import type { FlowText } from '../../lib/deliveryFlowText'

const props = defineProps<{ data: FlowData; sets: LaneSet[]; timeline: Timeline; selected: FlowStep | null; level: FlowLevel; lang: DeliveryLanguage; text: FlowText }>()
const emit = defineEmits<{ select: [item: LaneItem | null] }>()

const LANES_H = 330, AX = 20, GAPS = 4
const clipId = `ln-clip-${useId()}`
const host = ref<HTMLElement>()
const width = ref(0)
let observer: ResizeObserver | null = null
onMounted(() => {
  width.value = host.value?.clientWidth ?? 0
  observer = new ResizeObserver(() => { width.value = host.value?.clientWidth ?? 0 })
  if (host.value) observer.observe(host.value)
})
onBeforeUnmount(() => observer?.disconnect())

const narrow = computed(() => width.value < 640)
const coarse = typeof matchMedia === 'function' && matchMedia('(pointer: coarse)').matches
const geo = computed(() => {
  const t = props.timeline, W = width.value, { x0, x1 } = laneFrame(W), span = t.v1 - t.v0
  return { W, x0, x1, span, X: (m: number) => x0 + (m - t.v0) / span * (x1 - x0), inv: (x: number) => t.v0 + (x - x0) / (x1 - x0) * span }
})
const rel = computed(() => props.data.origin == null)
const live = computed(() => props.data.now != null)
const at = (m: number) => timeLabel(props.data, m)
const words = (step: FlowStep) => pick(props.level === 'simple' ? step.simple : step.expert, props.lang)
const actor = (lane: string) => (narrow.value ? props.text.actors.short : props.text.actors[props.level])[lane as 'you']

interface LaneRow { key: string; set: LaneSet; lane: string; y: number; h: number; label: string; alt: boolean }
interface Piece {
  key: string; kind: 'step' | 'cluster'; x: number; y: number; w: number; h: number
  item?: LaneItem; items?: LaneItem[]; from?: number; to?: number
  rects: { x: number; w: number; cls: string }[]; label: string; labelX: number; icon: 'clock' | 'refresh' | null; iconX: number; textCls: string
}
const layout = computed(() => {
  const g = geo.value, H = LANES_H, sets = props.sets, now = props.data.now
  const headH = rel.value ? 16 : 0
  const totalRows = sets.reduce((n, s) => n + s.lanes.reduce((m, a) => m + (s.rows[a] ?? 1), 0), 0)
  const laneN = sets.reduce((n, s) => n + s.lanes.length, 0)
  const rowH = Math.min(32, Math.max(15, Math.floor((H - AX - 40 - headH * sets.length - laneN * GAPS - (sets.length - 1) * 10) / Math.max(1, totalRows))))
  const lanes: LaneRow[] = [], pieces: Piece[] = [], titles: { y: number; text: string }[] = []
  const incidents: { key: string; x: number; y: number; w: number; h: number; fut: boolean }[] = []
  const incidentLabels: { key: string; x: number; y: number; text: string; full: string; healthyX: number | null }[] = []
  const connectors: { key: string; d: string; cx: number; cy: number }[] = []
  const targets: { key: string; x: number; w: number; label: string; lx: number }[] = []
  const finishes: { key: string; x: number; y0: number; y1: number; target: boolean }[] = []
  let y = 18
  sets.forEach((set, si) => {
    if (rel.value && set.title) { titles.push({ y: y + 10, text: pick(set.title, props.lang) }); y += headH }
    const top = y
    for (const lane of set.lanes) {
      const h = (set.rows[lane] ?? 1) * rowH + GAPS
      lanes.push({ key: `${si}:${lane}`, set, lane, y, h, label: actor(lane), alt: lanes.length % 2 === 1 })
      y += h
    }
    const bottom = y
    const inc = set.main.incident
    if (inc && inc.end > props.timeline.v0 && inc.start < props.timeline.v1) {
      const ix0 = g.X(inc.start), ix1 = g.X(inc.end), open = live.value && now! < inc.end
      const split = open ? g.X(now!) : ix1
      incidents.push({ key: `${si}:inc`, x: ix0, y: top - 2, w: Math.max(2, split - ix0), h: bottom - top + 22, fut: false })
      if (open) incidents.push({ key: `${si}:inc-fut`, x: split, y: top - 2, w: Math.max(2, ix1 - split), h: bottom - top + 22, fut: true })
      const label = `${pick(props.level === 'simple' ? inc.simple : inc.expert, props.lang)} · ${open ? `${props.text.since} ${at(inc.start)}` : minutesText(inc.end - inc.start)}`
      const caption = placeIncidentCaption({
        text: label, x0: g.x0, x1: g.x1, anchor: Math.max(ix0 + 6, g.x0 + 4),
        healthy: !open ? { text: props.text.healthy, anchor: ix1 + 4 } : null,
      })
      incidentLabels.push({ key: `${si}:incl`, x: caption.x, y: bottom + 10, text: caption.text, full: caption.full, healthyX: caption.healthyX })
    }
    const placed = new Map<FlowStep, Piece>()
    for (const row of lanes.filter(l => l.set === set)) {
      const items = set.items.filter(i => i.lane === row.lane)
      for (let r = 0; r < (set.rows[row.lane] ?? 1); r++) {
        const sy = row.y + GAPS / 2 + r * rowH, sh = rowH - 3
        for (const p of rowPieces(items.filter(i => i.row === r).sort((a, b) => a.step.start - b.step.start), g.X, g.x0, g.x1)) {
          if (p.kind === 'cluster') {
            const piece: Piece = { key: `${si}:c:${row.lane}:${r}:${p.from}`, kind: 'cluster', x: p.x, y: sy + 2, w: p.w, h: sh - 4, items: p.items, from: p.from, to: p.to, rects: [], label: `+${p.items.length}`, labelX: p.x + p.w / 2, icon: null, iconX: 0, textCls: '' }
            pieces.push(piece)
            for (const item of p.items) placed.set(item.step, { ...piece, y: sy, h: sh })
            continue
          }
          const piece = stepPiece(p.item, p.x, sy, p.w, sh, set, si)
          pieces.push(piece)
          placed.set(p.item.step, piece)
        }
      }
    }
    // Hand-overs of the main run: dotted connectors between lanes.
    const path = criticalPath(set.main)
    for (let i = 1; i < path.length; i++) {
      if (path[i]!.lane === path[i - 1]!.lane) continue
      const a = placed.get(path[i - 1]!), b = placed.get(path[i]!)
      if (!a || !b) continue
      const xa = g.X(path[i - 1]!.end), ya = a.y + a.h / 2, xb = g.X(path[i]!.start), yb = b.y + b.h / 2
      connectors.push({ key: `${si}:h${i}`, d: `M${xa},${ya}C${xa + 10},${ya} ${xb - 10},${yb} ${xb},${yb}`, cx: xb, cy: yb })
    }
    const target = set.main.target
    if (target && !rel.value) {
      const tx = g.X(target.start), tw = g.X(target.start + target.minutes) - tx
      targets.push({ key: `${si}:t`, x: tx, w: tw, label: `${props.text.target} ${minutesText(target.minutes)}`, lx: Math.max(g.x0 + 4, tx + tw + 5) })
    }
    if (rel.value && path.length) finishes.push({ key: `${si}:f`, x: g.X(Math.max(...path.map(s => s.end))), y0: top - 2, y1: bottom + 2, target: !!set.main.isTarget })
    y += 24
  })
  return { rowH, lanes, pieces, titles, incidents, incidentLabels, connectors, targets, finishes }
})

function stepPiece(item: LaneItem, x: number, y: number, w: number, h: number, set: LaneSet, si: number): Piece {
  const g = geo.value, s = item.step, now = props.data.now, run = item.run
  const fut = live.value && !run.isTarget && s.start >= now!, running = live.value && !run.isTarget && s.start < now! && s.end > now!
  const cls = `fl-seg ${s.kind}${s.incident ? ' incseg' : ''}${s.after ? ' after' : ''}${run.isTarget ? ' tgt' : ''}`
  let rects: Piece['rects']
  if (running) { const xn = g.X(now!); rects = [{ x, w: Math.max(1, xn - x), cls }, { x: xn, w: Math.max(1, x + w - xn), cls: `${cls} fut` }] }
  else rects = [{ x, w: Math.max(1.5, w), cls: `${cls}${fut ? ' fut' : ''}` }]
  const lx = Math.max(x, g.x0), room = x + w - lx, cut = x < g.x0 - 0.5
  const since = cut ? ` · ${props.text.since} ${at(s.start)}` : ''
  const icon = s.kind === 'wait' ? 'clock' : s.kind === 'rework' ? 'refresh' : null
  const pad = icon ? 16 : 6
  const label = h >= 12 ? fitLabel({ tag: set.multi ? run.tag : undefined, text: words(s), since, duration: icon ? minutesText(s.end - s.start) : undefined, room, pad }) : ''
  return {
    key: `${si}:${run.id}:${run.steps.indexOf(s)}`, kind: 'step', item, x, y, w, h, rects, label, labelX: lx + pad,
    icon: icon && room > 14 ? icon : null, iconX: lx + 4, textCls: `fl-t ${s.kind}${fut ? ' fut' : ''}`,
  }
}

const grid = computed(() => {
  const g = geo.value, t = props.timeline, step = tickStep(g.span, g.x1 - g.x0), out: { x: number; label: string }[] = []
  for (let m = Math.ceil(t.v0 / step) * step; m <= t.v1 + 1e-6; m += step) out.push({ x: g.X(m), label: at(m) })
  return out
})
const nowX = computed(() => {
  if (!live.value) return null
  const x = geo.value.X(props.data.now!)
  return x >= geo.value.x0 && x <= geo.value.x1 ? x : null
})
const playhead = computed(() => {
  const g = geo.value, xp = g.X(props.timeline.T)
  if (xp < g.x0 - 1 || xp > g.x1 + 1) return null
  const label = at(props.timeline.T), w = label.length * 6.6 + 26, hit = coarse || narrow.value ? 44 : 14
  return { x: xp, label, w, hit, y: LANES_H - AX - 19 }
})
const selBox = computed(() => {
  if (!props.selected) return null
  return layout.value.pieces.find(p => p.item?.step === props.selected || p.items?.some(i => i.step === props.selected)) ?? null
})

// ---------- Keyboard: the lanes are one focusable group ----------
const focusLane = ref(0)
function onKey(event: KeyboardEvent) {
  if (event.altKey || event.ctrlKey || event.metaKey) return
  const result = keyInput(props.timeline, event.key, event.shiftKey)
  if (!result) return
  const n = layout.value.lanes.length || 1
  if (result === 'lane-up') focusLane.value = (focusLane.value + n - 1) % n
  else if (result === 'lane-down') focusLane.value = (focusLane.value + 1) % n
  else if (result === 'select') {
    const lane = layout.value.lanes[focusLane.value], T = props.timeline.T
    const item = lane?.set.items.find(i => i.lane === lane.lane && i.step.start <= T + 1e-6 && i.step.end > T)
    emit('select', item ?? null)
  } else if (result === 'clear') {
    if (!props.selected) return
    emit('select', null)
  }
  event.preventDefault()
}

// ---------- Pointer: drag the playhead, drag empty space to pan, click to select ----------
type Drag = { kind: 'ph' | 'pan'; x: number; v0: number; v1: number; moved: boolean; target: EventTarget | null; id: number }
let drag: Drag | null = null
const dragging = ref(false)
const localX = (event: PointerEvent | WheelEvent) => event.clientX - (host.value?.getBoundingClientRect().left ?? 0)
function onDown(event: PointerEvent) {
  if (event.button !== 0) return
  const part = (event.target as Element).closest?.('[data-part]')?.getAttribute('data-part')
  const t = props.timeline
  drag = { kind: part === 'ph' ? 'ph' : 'pan', x: localX(event), v0: t.v0, v1: t.v1, moved: part === 'ph', target: event.target, id: event.pointerId }
  if (part === 'ph') { t.follow = false; dragging.value = true; event.preventDefault() }
  host.value?.setPointerCapture?.(event.pointerId)
}
function onMove(event: PointerEvent) {
  const d = drag
  if (!d || d.id !== event.pointerId) return
  const x = localX(event), dx = x - d.x, t = props.timeline, g = geo.value
  if (Math.abs(dx) > 3) d.moved = true
  if (!d.moved) return
  t.follow = false
  if (d.kind === 'ph') { t.T = Math.min(Math.min(t.r1, t.v1), Math.max(Math.max(t.r0, t.v0), g.inv(x))); return }
  dragging.value = true
  const w = d.v1 - d.v0, per = w / (g.x1 - g.x0)
  t.v0 = Math.min(t.r1 - w, Math.max(t.r0, d.v0 - dx * per)); t.v1 = t.v0 + w
}
function onUp(event: PointerEvent) {
  const d = drag
  if (!d || d.id !== event.pointerId) return
  drag = null; dragging.value = false
  if (d.moved || d.kind !== 'pan') return
  // A click selects (or clears); it never moves the time. A "+N" zooms in.
  const target = d.target as Element | null
  const cluster = target?.closest?.('[data-cluster]')?.getAttribute('data-cluster')
  if (cluster) {
    const [a, b] = cluster.split('|').map(Number) as [number, number]
    props.timeline.follow = false
    setWindow(props.timeline, Math.max(6, (b - a) * 3), (a + b) / 2, 0.5)
    return
  }
  const key = target?.closest?.('[data-step]')?.getAttribute('data-step')
  const piece = key ? layout.value.pieces.find(p => p.key === key) : undefined
  emit('select', piece?.item ?? null)
}
function onCancel() { drag = null; dragging.value = false }
function onWheel(event: WheelEvent) {
  const g = geo.value
  const handled = wheelInput(props.timeline, {
    deltaX: event.deltaX, deltaY: event.deltaY, zoomKey: event.ctrlKey || event.metaKey, shiftKey: event.shiftKey,
    at: g.inv(localX(event)), minutesPerPx: g.span / (g.x1 - g.x0),
  })
  if (handled) event.preventDefault()
}
defineExpose({ focusLane, setTime: (m: number) => setTime(props.timeline, m) })
</script>

<template>
  <div ref="host" class="fl-stage" :class="{ dragging }" tabindex="0" role="group" :aria-roledescription="text.chart" :aria-label="text.lanesLabel" data-testid="flow-lanes"
    @keydown="onKey" @pointerdown="onDown" @pointermove="onMove" @pointerup="onUp" @pointercancel="onCancel" @lostpointercapture="onCancel" @wheel="onWheel">
    <svg v-if="width" :viewBox="`0 0 ${geo.W} ${LANES_H}`" :width="geo.W" :height="LANES_H" aria-hidden="true">
      <defs><clipPath :id="clipId"><rect :x="geo.x0" y="0" :width="geo.x1 - geo.x0" :height="LANES_H" /></clipPath></defs>
      <rect v-for="(lane, i) in layout.lanes" :key="lane.key" class="ln-lane" :class="{ alt: lane.alt, focus: i === focusLane }" x="0" :y="lane.y" :width="geo.W" :height="lane.h" :data-lane="lane.lane" />
      <g>
        <template v-for="tick in grid" :key="tick.x">
          <line class="fl-grid" :x1="tick.x" :x2="tick.x" y1="16" :y2="LANES_H - 18" />
          <text class="fl-axis" :x="tick.x" :y="LANES_H - 5" text-anchor="middle">{{ tick.label }}</text>
        </template>
      </g>
      <g>
        <text v-for="lane in layout.lanes" :key="lane.key" class="fl-lab" :x="narrow ? 2 : 8" :y="lane.y + Math.min(lane.h, layout.rowH + 4) / 2 + 4">{{ lane.label }}</text>
        <text v-for="title in layout.titles" :key="title.y" class="fl-ttl" x="0" :y="title.y">{{ title.text }}</text>
      </g>
      <g :clip-path="`url(#${clipId})`">
        <rect v-for="band in layout.incidents" :key="band.key" class="fl-inc" :class="{ fut: band.fut }" :x="band.x" :y="band.y" :width="band.w" :height="band.h" rx="5" />
        <g v-for="piece in layout.pieces" :key="piece.key" :data-step="piece.kind === 'step' ? piece.key : undefined" :data-cluster="piece.kind === 'cluster' ? `${piece.from}|${piece.to}` : undefined">
          <template v-if="piece.kind === 'cluster'">
            <rect class="ln-cluster" :x="piece.x" :y="piece.y" :width="piece.w" :height="piece.h" :rx="piece.h / 2" />
            <text class="ln-clut" :x="piece.labelX" :y="piece.y + piece.h / 2 + 3.6" text-anchor="middle">{{ piece.label }}</text>
          </template>
          <template v-else>
            <rect v-for="(r, i) in piece.rects" :key="i" :class="r.cls" :x="r.x" :y="piece.y" :width="r.w" :height="piece.h" rx="4" />
            <rect class="fl-hit" :x="piece.x" :y="piece.y - 1" :width="Math.max(8, piece.w)" :height="piece.h + 2" />
            <g v-if="piece.icon" :class="piece.icon === 'clock' ? 'fl-clk' : 'fl-rw'" :transform="`translate(${piece.iconX},${piece.y + piece.h / 2 - 5})`"><AppIcon :name="piece.icon" :size="10" /></g>
            <text v-if="piece.label" :class="piece.textCls" :x="piece.labelX" :y="piece.y + piece.h / 2 + 3.8">{{ piece.label }}</text>
          </template>
        </g>
        <g v-for="c in layout.connectors" :key="c.key"><path class="fl-conn" :d="c.d" /><circle class="fl-hand" :cx="c.cx" :cy="c.cy" r="2.6" /></g>
        <g v-for="tg in layout.targets" :key="tg.key">
          <rect class="fl-tgtbar" :x="tg.x" y="8" :width="tg.w" height="4" rx="2" />
          <text class="fl-tgtl" :x="tg.lx" y="13">{{ tg.label }}</text>
        </g>
        <path v-for="f in layout.finishes" :key="f.key" class="fl-finish" :class="{ tgt: f.target }" :d="`M${f.x},${f.y0}V${f.y1}`" />
        <rect v-if="selBox" class="ln-selbox" :x="selBox.x - 1.5" :y="selBox.y - 1.5" :width="selBox.w + 3" :height="selBox.h + 3" rx="5" />
        <g v-for="l in layout.incidentLabels" :key="l.key">
          <title>{{ l.full }}</title>
          <circle class="fl-inc-dot" :cx="l.x + 5" :cy="l.y" r="5" />
          <path class="fl-inc-mark" :d="`M${l.x + 5} ${l.y - 2.5}v3M${l.x + 5} ${l.y + 2.4}v.1`" />
          <text v-if="l.text" class="fl-inc-t" :x="l.x + 14" :y="l.y + 3.6">{{ l.text }}</text>
          <g v-if="l.healthyX != null" class="fl-rec">
            <g :transform="`translate(${l.healthyX},${l.y - 5})`"><AppIcon name="check" :size="10" /></g>
            <text class="fl-rec-t" :x="l.healthyX + 13" :y="l.y + 3.6">{{ text.healthy }}</text>
          </g>
        </g>
      </g>
      <g v-if="nowX != null">
        <line class="fl-now" :x1="nowX" :x2="nowX" y1="14" :y2="LANES_H - AX + 2" />
        <text class="fl-nowt" :x="nowX + 4" :y="LANES_H - AX - 2">{{ text.now }}</text>
      </g>
      <g v-if="playhead" class="ln-phg" data-part="ph" data-testid="flow-playhead">
        <line class="ln-ph" :x1="playhead.x" :x2="playhead.x" y1="10" :y2="LANES_H - AX + 2" />
        <rect class="ln-phhit" data-part="ph" :x="playhead.x - playhead.hit / 2" y="8" :width="playhead.hit" :height="LANES_H - AX - 6" />
        <rect class="ln-phpill" data-part="ph" :x="playhead.x - playhead.w / 2" :y="playhead.y" :width="playhead.w" height="18" rx="9" />
        <path class="ln-phgrip" :d="`M${playhead.x - playhead.w / 2 + 7},${playhead.y + 6}v6M${playhead.x - playhead.w / 2 + 10},${playhead.y + 6}v6`" />
        <text class="ln-pht" :x="playhead.x + 5" :y="playhead.y + 12.6" text-anchor="middle">{{ playhead.label }}</text>
      </g>
    </svg>
    <template v-if="width">
      <span v-for="l in layout.incidentLabels" :key="`${l.key}:full`" class="sr-only">{{ l.full }}</span>
    </template>
  </div>
</template>

<style scoped>
.fl-stage { position: relative; height: 330px; margin-top: 8px; border-radius: 10px; cursor: grab; touch-action: pan-y; -webkit-user-select: none; user-select: none; }
.fl-stage.dragging { cursor: grabbing; }
.fl-stage:focus-visible { outline: none; box-shadow: var(--focus-ring); }
.fl-stage svg { display: block; width: 100%; height: 100%; overflow: visible; }
.ln-lane { fill: transparent; }
.ln-lane.alt { fill: var(--surface-sunken); }
.fl-stage:focus .ln-lane.focus { fill: var(--row-selected); }
.fl-axis { font: 500 10px var(--mono); fill: var(--ink-3); }
.fl-grid { stroke: var(--line); stroke-width: 1; }
.fl-lab { font: 600 11.5px var(--font); fill: var(--ink-2); }
.fl-ttl { font: 650 12.5px var(--font); fill: var(--ink); }
.fl-seg { stroke-width: 1; }
.fl-seg.work { fill: color-mix(in srgb, var(--teal) 22%, transparent); stroke: color-mix(in srgb, var(--teal) 55%, transparent); }
.fl-seg.rework { fill: var(--danger-bg); stroke: var(--danger-line); }
.fl-seg.wait { fill: var(--queue-wait-bg); stroke: var(--queue-wait-line); }
.fl-seg.fut { fill-opacity: .45; stroke-dasharray: 4 3; }
.fl-seg.after { stroke-dasharray: 2 3; }
.fl-seg.tgt { fill: color-mix(in srgb, var(--ok) 20%, transparent); stroke: color-mix(in srgb, var(--ok) 60%, transparent); }
.fl-seg.incseg { stroke: color-mix(in srgb, var(--danger) 60%, transparent); }
.fl-hit { fill: transparent; }
.fl-t { font: 600 11px var(--font); fill: var(--teal-ink); pointer-events: none; }
.fl-t.rework { fill: var(--danger); }
.fl-t.wait { fill: var(--queue-wait-ink); }
.fl-t.fut { font-weight: 500; fill: var(--ink-2); }
.fl-clk { color: var(--queue-wait-ink); pointer-events: none; }
.fl-rw { color: var(--danger); pointer-events: none; }
.fl-hand { fill: var(--surface-raised); stroke: var(--ink-2); stroke-width: 1.5; }
.fl-conn { fill: none; stroke: var(--ink-3); stroke-width: 1.2; stroke-dasharray: 2 2.5; pointer-events: none; }
.fl-now { stroke: var(--ink); stroke-width: 1.5; pointer-events: none; }
.fl-nowt { font: 600 10.5px var(--mono); fill: var(--ink); pointer-events: none; }
.fl-tgtbar { fill: color-mix(in srgb, var(--ok) 55%, transparent); }
.fl-tgtl { font: 600 10.5px var(--font); fill: var(--ok); }
.fl-finish { stroke: var(--ink-2); stroke-width: 2; }
.fl-finish.tgt { stroke: var(--ok); }
.fl-inc { fill: color-mix(in srgb, var(--danger) 11%, transparent); stroke: color-mix(in srgb, var(--danger) 55%, transparent); stroke-width: 1; pointer-events: none; }
.fl-inc.fut { fill: color-mix(in srgb, var(--danger) 4%, transparent); stroke-dasharray: 4 3; }
.fl-inc-dot { fill: var(--danger); }
.fl-inc-mark { stroke: var(--canvas); stroke-width: 1.8; stroke-linecap: round; fill: none; }
.fl-inc-t, .fl-rec-t { font: 650 11px var(--font); paint-order: stroke; stroke: var(--surface-raised); stroke-width: 3px; stroke-linejoin: round; }
.fl-inc-t { fill: var(--danger); }
.fl-rec { color: var(--ok); }
.fl-rec-t { fill: var(--ok); }
.ln-cluster { fill: var(--surface-raised); stroke: var(--ink-3); stroke-width: 1; cursor: zoom-in; }
.ln-clut { font: 600 10px var(--mono); fill: var(--ink-2); pointer-events: none; }
.ln-selbox { fill: none; stroke: var(--ink); stroke-width: 2; pointer-events: none; }
.ln-phg, .ln-phhit, .ln-phpill { cursor: ew-resize; }
.ln-ph { stroke: var(--teal); stroke-width: 2; pointer-events: none; }
.ln-phhit { fill: transparent; }
.ln-phpill { fill: var(--teal); stroke: var(--surface-raised); stroke-width: 1.5; }
.ln-phgrip { stroke: var(--canvas); stroke-width: 1.2; stroke-linecap: round; pointer-events: none; }
.ln-pht { font: 600 10px var(--mono); fill: var(--canvas); pointer-events: none; }
</style>
