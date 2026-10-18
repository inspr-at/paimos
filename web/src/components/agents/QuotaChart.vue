<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// AEON-1497 (approved fuel gauge): quota left across the group's accounts, measured until now,
// then at the current pace (dashed) and at the safe pace (dotted), with burn per hour below on the
// same hours. Hover, focus or tap shows one hour in the fixed info line; nothing moves (0 px).
import { computed, onBeforeUnmount, onMounted, ref, useId, watch } from 'vue'
import { chartGeometry, fullLine, hourAt, hourInfo, valueAt, xh, yl, f1, type ChartModel, type InfoPart } from '../../lib/quotaOutlook'

const props = defineProps<{ model: ChartModel; name: string; past: number; ahead: number; tz?: string; infoId: string }>()
const uid = useId()
const plot = ref<HTMLElement>()
const size = ref({ w: 0, h: 0 })
let observer: ResizeObserver | undefined
let canvas: CanvasRenderingContext2D | null | undefined
function measure(text: string, weight: number) {
  canvas ??= document.createElement('canvas').getContext('2d')
  if (!canvas) return text.length * 6.2
  canvas.font = `${weight} 11px ${getComputedStyle(document.body).fontFamily}`
  return canvas.measureText(text).width
}
onMounted(() => {
  const el = plot.value!
  const read = () => { size.value = { w: Math.round(el.clientWidth), h: Math.round(el.clientHeight) } }
  read()
  observer = new ResizeObserver(read)
  observer.observe(el)
})
onBeforeUnmount(() => observer?.disconnect())
const geo = computed(() => size.value.w > 40 ? chartGeometry(props.model, size.value.w, size.value.h, props.past, props.ahead, measure, props.tz) : null)

// One hour shown: the pointer's (mouse), else the focused or tapped one.
const hover = ref<number | null>(null), sel = ref<number | null>(null)
const shown = computed(() => hover.value ?? sel.value)
watch(() => [props.past, props.ahead, props.model], () => { hover.value = null; sel.value = null })
const line = computed(() => fullLine(props.model))
const cursor = computed(() => {
  const g = geo.value, h = shown.value
  if (!g || h === null) return null
  const f = g.frame, v = valueAt(line.value, h + 0.5)
  return { x: xh(f, h), w: Math.max(f.pitch, 2), xm: xh(f, h + 0.5), y: v === null ? null : yl(f, v) }
})
const rest = computed<InfoPart[]>(() => props.model.p24 === null ? [{ text: 'Burn: the pace appears after an hour of readings.' }]
  : [{ text: 'Burn: 24 h pace ' }, { text: `${f1(props.model.p24)} %/h`, strong: true }, { text: ' · ', quiet: true }, { text: props.model.p6 === null ? '6 h pace not measured' : `6 h pace ${f1(props.model.p6)} %/h` }])
const info = computed<InfoPart[]>(() => geo.value && shown.value !== null ? hourInfo(props.model, geo.value, shown.value, props.name, props.tz) : rest.value)
function pointerAt(event: PointerEvent) { const r = plot.value!.getBoundingClientRect(); return geo.value ? hourAt(geo.value.frame, event.clientX - r.left) : null }
function move(event: PointerEvent) { if (event.pointerType !== 'touch') hover.value = pointerAt(event) }
function leave() { hover.value = null }
function down(event: PointerEvent) { if (event.pointerType !== 'mouse') sel.value = pointerAt(event) }
function keys(event: KeyboardEvent) {
  const g = geo.value
  if (!g) return
  const step = ({ ArrowLeft: -1, ArrowRight: 1, ArrowUp: 24, ArrowDown: -24 } as Record<string, number>)[event.key]
  if (step === undefined && !['Home', 'End', 'Escape'].includes(event.key)) return
  if (event.key === 'Escape' && sel.value === null) return
  event.preventDefault()
  const f = g.frame, clampH = (h: number) => Math.max(f.start, Math.min(f.end - 1, h))
  if (event.key === 'Escape') sel.value = null
  else if (event.key === 'Home') sel.value = f.start
  else if (event.key === 'End') sel.value = f.end - 1
  else sel.value = clampH((sel.value ?? Math.floor(props.model.nowH)) + step!)
}
function focus(event: FocusEvent) { if ((event.target as HTMLElement).matches(':focus-visible') && sel.value === null) sel.value = Math.floor(props.model.nowH) }
function blur() { sel.value = null }
const hatch = (i: number) => `${uid}-hp-${i}`
</script>

<template>
  <div ref="plot" class="ub-plot" tabindex="0" role="group" aria-roledescription="chart" :aria-label="`Quota left across all ${name} accounts: measured until now, then at the current pace and at the safe pace, with burn per hour below. Arrow keys step through the hours.`" :aria-describedby="infoId"
    @pointermove="move" @pointerleave="leave" @pointerdown="down" @keydown="keys" @focus="focus" @blur="blur">
    <svg v-if="geo" :viewBox="`0 0 ${geo.frame.W} ${geo.frame.H}`" :width="geo.frame.W" :height="geo.frame.H" aria-hidden="true" focusable="false">
      <defs>
        <clipPath :id="`${uid}-clip`"><rect :x="geo.frame.L" y="0" :width="geo.frame.W - geo.frame.L" :height="geo.frame.H" /></clipPath>
        <pattern v-for="(shade, i) in model.shades" :id="hatch(i)" :key="i" width="3.6" height="3.6" patternUnits="userSpaceOnUse" patternTransform="rotate(45)">
          <rect width="3.6" height="3.6" :style="{ fill: shade, fillOpacity: 'var(--hatch-base)' }" /><rect width="1.15" height="3.6" :style="{ fill: shade, fillOpacity: 'var(--hatch-line)' }" />
        </pattern>
        <pattern :id="`${uid}-off`" width="3.2" height="3.2" patternUnits="userSpaceOnUse" patternTransform="rotate(45)"><rect width="1.1" height="3.2" style="fill: var(--warn-ink)" /></pattern>
        <pattern :id="`${uid}-empty`" width="3.4" height="3.4" patternUnits="userSpaceOnUse" patternTransform="rotate(45)"><rect width="1.3" height="3.4" style="fill: var(--danger)" /></pattern>
      </defs>
      <line v-for="(l, i) in geo.levels" :key="`l${i}`" :class="l.cls" :x1="l.x1" :x2="l.x2" :y1="l.y1" :y2="l.y2" />
      <text v-for="(t, i) in geo.levelText" :key="`lt${i}`" :class="t.cls" :x="t.x" :y="t.y" :text-anchor="t.anchor">{{ t.text }}</text>
      <line v-for="(l, i) in geo.days" :key="`d${i}`" :class="l.cls" :x1="l.x1" :x2="l.x2" :y1="l.y1" :y2="l.y2" />
      <text v-for="(t, i) in geo.dayText" :key="`dt${i}`" :class="t.cls" :x="t.x" :y="t.y" :text-anchor="t.anchor">{{ t.text }}</text>
      <line :class="geo.strip.base.cls" :x1="geo.strip.base.x1" :x2="geo.strip.base.x2" :y1="geo.strip.base.y1" :y2="geo.strip.base.y2" />
      <line v-if="geo.strip.sus" :class="geo.strip.sus.cls" :x1="geo.strip.sus.x1" :x2="geo.strip.sus.x2" :y1="geo.strip.sus.y1" :y2="geo.strip.sus.y2" />
      <text :class="geo.strip.label.cls" :x="geo.strip.label.x" :y="geo.strip.label.y" text-anchor="end">{{ geo.strip.label.text }}</text>
      <rect class="cx-hl" :class="{ on: cursor }" :x="cursor?.x ?? 0" :y="geo.frame.sTop" :width="cursor?.w ?? 0" :height="geo.frame.SH" />
      <rect v-for="(b, i) in geo.bars" :key="`b${i}`" :x="b.x.toFixed(2)" :y="b.y.toFixed(2)" :width="b.w.toFixed(2)" :height="b.h.toFixed(2)" :fill="b.projected ? `url(#${hatch(b.i)})` : undefined" :style="b.projected ? undefined : { fill: model.shades[b.i] }" />
      <template v-for="(r, i) in geo.off" :key="`o${i}`"><rect class="cx-off-b" :x="r.x" :y="r.y" :width="r.w" :height="r.h" /><rect :x="r.x" :y="r.y" :width="r.w" :height="r.h" :fill="`url(#${uid}-off)`" /></template>
      <g :clip-path="`url(#${uid}-clip)`">
        <path v-if="geo.area" class="cx-area" :d="geo.area" />
        <path v-if="geo.safePath" class="cx-safe" :d="geo.safePath" />
        <path v-if="geo.curPath" class="cx-cur" :d="geo.curPath" />
        <path v-if="geo.pastPath" class="cx-past" :d="geo.pastPath" />
      </g>
      <template v-for="(r, i) in geo.empty" :key="`e${i}`"><rect class="cx-empty-b" :x="r.x.toFixed(1)" :y="r.y" :width="r.w.toFixed(1)" :height="r.h" /><rect :x="r.x.toFixed(1)" :y="r.y" :width="r.w.toFixed(1)" :height="r.h" :fill="`url(#${uid}-empty)`" /></template>
      <text v-for="(t, i) in geo.emptyText" :key="`et${i}`" :class="t.cls" :x="t.x.toFixed(1)" :y="t.y">{{ t.text }}</text>
      <line v-for="(l, i) in geo.marks" :key="`m${i}`" :class="l.cls" :x1="l.x1" :x2="l.x2" :y1="l.y1" :y2="l.y2" />
      <text v-for="(t, i) in geo.markText" :key="`mt${i}`" :class="t.cls" :x="t.x.toFixed(1)" :y="t.y">{{ t.text }}</text>
      <text v-for="(t, i) in geo.ends" :key="`n${i}`" :class="t.cls" :x="t.x.toFixed(1)" :y="t.y.toFixed(1)" text-anchor="middle">{{ t.text }}</text>
      <line class="cx-cursor" :class="{ on: cursor }" :x1="cursor?.xm ?? 0" :x2="cursor?.xm ?? 0" :y1="geo.frame.T" :y2="geo.frame.sBot" />
      <circle class="cx-dot" :class="{ on: cursor && cursor.y !== null }" :cx="cursor?.xm ?? 0" :cy="cursor?.y ?? 0" r="4" />
    </svg>
  </div>
  <p :id="infoId" class="ub-info" aria-live="polite"><template v-for="(p, i) in info" :key="i"><b v-if="p.strong">{{ p.text }}</b><span v-else-if="p.quiet" class="q">{{ p.text }}</span><template v-else>{{ p.text }}</template></template></p>
</template>

<style scoped>
.ub-plot { position: relative; height: 226px; margin-top: 2px; border-radius: 8px; touch-action: pan-y; cursor: crosshair; }
.ub-plot:focus-visible { outline: none; box-shadow: var(--focus-ring); }
.ub-plot > svg { position: absolute; inset: 0; width: 100%; height: 100%; overflow: visible; }
.ub-info { height: 20px; margin: 4px 0 0; overflow: hidden; color: var(--ink-2); font-size: 12.5px; line-height: 20px; white-space: nowrap; text-overflow: ellipsis; font-variant-numeric: tabular-nums; }
.ub-info b { color: var(--ink); font-weight: 650; }
.ub-info .q { color: var(--ink-3); }
.cx-grid { stroke: var(--line); stroke-width: 1; }
.cx-base, .cx-sbase { stroke: var(--line-2); stroke-width: 1; }
.cx-day { stroke: var(--line); stroke-width: 1; }
.cx-yl, .cx-dl, .cx-gl, .cx-hl-t { fill: var(--ink-3); font: 500 10.5px/1 var(--font); font-variant-numeric: tabular-nums; }
.cx-hl-t { opacity: .75; }
.cx-dl.today { fill: var(--ink-2); font-weight: 650; }
.cx-area { fill: var(--line-area); }
.cx-past { fill: none; stroke: var(--teal); stroke-width: 2; stroke-linejoin: round; stroke-linecap: round; }
.cx-cur { fill: none; stroke: var(--teal); stroke-width: 2; stroke-dasharray: 6 4; stroke-linejoin: round; }
.cx-safe { fill: none; stroke: var(--ink-3); stroke-width: 1.8; stroke-dasharray: .1 4; stroke-linecap: round; stroke-linejoin: round; }
.cx-now { stroke: var(--ink); stroke-width: 1.25; }
.cx-now-t { fill: var(--ink); font: 650 11px/1 var(--font); }
.cx-refill { stroke: var(--ink-3); stroke-width: 1; stroke-dasharray: 3 3; }
.cx-refill-t { fill: var(--ink-2); font: 500 11px/1 var(--font); }
.cx-end-c, .cx-end-s, .cx-empty-t { font: 600 11px/1 var(--font); font-variant-numeric: tabular-nums; paint-order: stroke; stroke: var(--surface-raised); stroke-width: 4px; stroke-linejoin: round; }
.cx-end-c { fill: var(--teal-ink); }
.cx-end-s { fill: var(--ink-2); }
.cx-empty-t { fill: var(--danger); font-weight: 650; }
.cx-empty-b { fill: color-mix(in srgb, var(--danger) 14%, transparent); }
.cx-ssus { stroke: var(--ink-3); stroke-width: 1; stroke-dasharray: 3 3; opacity: .8; }
.cx-hl { fill: var(--ink); opacity: 0; }
.cx-hl.on { opacity: var(--cx-hl-opacity); }
.cx-cursor { stroke: var(--ink-2); stroke-width: 1; opacity: 0; }
.cx-cursor.on { opacity: .7; }
.cx-dot { fill: var(--surface-raised); stroke: var(--teal); stroke-width: 2; opacity: 0; }
.cx-dot.on { opacity: 1; }
.cx-off-b { fill: color-mix(in srgb, var(--gold) 16%, transparent); }
@container working (max-width: 760px) { .ub-plot { height: 186px; } }
</style>
