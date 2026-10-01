<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import type { StatViz } from '../../lib/releaseStats'

// The small visual under each stat (AEON-488), drawn from the stat's data on a
// 348 × 64 board: releases added up, features and fixes, the last 40 hours, the
// gaps around the median, this week, the streak and the busiest day. Colours
// come from classes, so dark mode follows the tokens.
const props = defineProps<{ viz: StatViz }>()

type Shape =
  | { t: 'rect'; x: number; y: number; w: number; h: number; r: number; c: string }
  | { t: 'line'; x1: number; y1: number; x2: number; y2: number; c: string }
  | { t: 'circle'; x: number; y: number; r: number; c: string }
  | { t: 'path'; d: string; c: string }
  | { t: 'text'; x: number; y: number; text: string; a: 'start' | 'middle' | 'end'; c: string }

const L = 8, R = 340
const clamp = (v: number, lo: number, hi: number) => Math.max(lo, Math.min(hi, v))
const f = (n: number) => Math.round(n * 10) / 10
// Evenly spread centres from 20 to 328, or the middle for one.
const spread = (n: number) => (i: number) => n === 1 ? 174 : 20 + i * 308 / (n - 1)

const shapes = computed<Shape[]>(() => {
  const v = props.viz
  const out: Shape[] = []
  const text = (x: number, y: number, s: string, c = 'label', a: 'start' | 'middle' | 'end' = 'middle') => out.push({ t: 'text', x: f(x), y, text: s, a, c })
  const rect = (x: number, y: number, w: number, h: number, c: string, r = 3) => { if (h > 0) out.push({ t: 'rect', x: f(x), y: f(y), w: f(w), h: f(h), r, c }) }
  const line = (x1: number, y1: number, x2: number, y2: number, c: string) => out.push({ t: 'line', x1: f(x1), y1, x2: f(x2), y2, c })

  if (v.kind === 'cumulative') {
    const n = v.points.length, x = spread(n), top = Math.max(1, v.total)
    const y = (p: number) => f(50 - p / top * 34)
    line(L, 50, R, 50, 'base')
    out.push({ t: 'path', d: `M${x(0)} 50 L${x(n - 1)} 16`, c: 'trend' })
    const pts = v.points.map((p, i) => `${f(x(i))} ${y(p)}`)
    out.push({ t: 'path', d: `M${pts.join(' L')} L${f(x(n - 1))} 50 L${f(x(0))} 50 Z`, c: 'area' })
    out.push({ t: 'path', d: `M${pts.join(' L')}`, c: 'stroke' })
    v.points.forEach((p, i) => out.push({ t: 'circle', x: f(x(i)), y: y(p), r: i === n - 1 ? 4 : 2.2, c: i === n - 1 ? 'dot-end' : 'dot' }))
    text(x(n - 1) + 4, 9, String(v.total), 'label-strong', 'end')
    text(14, 10, v.pace, 'label-gold', 'start')
    text(x(0), 62, v.from)
    if (n > 1) text(x(n - 1), 62, 'today', 'label-on')
  } else if (v.kind === 'stacked') {
    const n = v.bars.length, x = spread(n), w = clamp(308 / n * .55, 4, 20)
    const max = Math.max(1, ...v.bars.map(b => b.features + b.fixes)), k = 34 / max
    line(L, 48, R, 48, 'base')
    v.bars.forEach((b, i) => {
      const hf = b.features * k, hx = b.fixes * k
      rect(x(i) - w / 2, 48 - hf, w, hf, i === n - 1 ? 'col-on' : 'col-mid', 2)
      rect(x(i) - w / 2, 48 - hf - (hf ? 1.5 : 0) - hx, w, hx, 'fix', 2)
    })
    rect(10, 2, 8, 8, 'col-mid', 2); text(22, 9, 'features', 'label', 'start')
    rect(80, 2, 8, 8, 'fix', 2); text(92, 9, 'fixes', 'label', 'start')
    text(x(0), 62, v.from)
    if (n > 1) text(x(n - 1), 62, 'today', 'label-on')
  } else if (v.kind === 'timeline') {
    const x = (p: number) => L + p * (R - L)
    const lx = x(v.last), nx = R - .6, nameX = clamp(lx, 40, 306)
    line(L, 34, R, 34, 'base')
    for (const m of v.marks) {
      line(x(m.at), 22, x(m.at), 46, 'grid')
      if (Math.abs(x(m.at) + 3 - nameX) > 64 && x(m.at) < 300) text(x(m.at) + 3, 60, m.label, 'label', 'start')
    }
    for (const t of v.ticks) line(x(t), 27, x(t), 41, 'tick')
    line(lx, 24, lx, 44, 'tick-on')
    if (nx - 5 > lx + 4) line(lx + 4, 34, nx - 5, 34, 'wait')
    out.push({ t: 'circle', x: f(nx), y: 34, r: 4.2, c: 'now' })
    out.push({ t: 'path', d: `M${f(lx)} 17 V13 H${f(nx)} V17`, c: 'bracket' })
    text(clamp((lx + nx) / 2, 24, 324), 9, v.gap, 'label-gold')
    text(nameX, 60, v.lastName, 'label-on')
  } else if (v.kind === 'histogram') {
    const w = 45, gap = (R - L - 6 * w) / 5, x = (i: number) => L + i * (w + gap)
    const max = Math.max(1, ...v.buckets.map(b => b.n)), on = Math.floor(v.median)
    line(L, 46, R, 46, 'base')
    v.buckets.forEach((b, i) => {
      const h = b.n ? Math.max(2, b.n / max * 34) : 0
      rect(x(i), 46 - h, w, h, v.medianLabel && i === on ? 'col-on' : 'col')
      text(x(i) + w / 2, 60, b.label, v.medianLabel && i === on ? 'label-on small' : 'label small')
    })
    if (v.medianLabel) {
      const mx = x(on) + (v.median - on) * w
      line(mx, 4, mx, 46, 'marker')
      if (mx > 250) text(mx - 5, 9, v.medianLabel, 'label-gold', 'end')
      else text(mx + 5, 9, v.medianLabel, 'label-gold', 'start')
    }
  } else if (v.kind === 'week') {
    const x = (i: number) => L + (R - L) * (i + .5) / 7
    const max = Math.max(1, ...v.days.map(d => d.n ?? 0))
    line(L, 44, R, 44, 'base')
    v.days.forEach((d, i) => {
      if (d.n === null) out.push({ t: 'rect', x: f(x(i) - 11), y: 38, w: 22, h: 6, r: 3, c: 'placeholder' })
      else {
        const h = d.n ? Math.max(2, d.n / max * 30) : 1.5
        rect(x(i) - 11, 44 - h, 22, h, d.today ? 'col-on' : d.n ? 'col' : 'col-dim')
        text(x(i), 44 - h - 4, String(d.n), d.today ? 'value-on small' : 'label small')
      }
      text(x(i), 60, d.letter, d.today ? 'label-strong' : 'label')
    })
  } else if (v.kind === 'streak') {
    const n = v.days.length, x = spread(n), max = Math.max(1, ...v.days)
    line(x(0), 30, x(n - 1), 30, 'chain')
    v.days.forEach((d, i) => {
      const last = i === n - 1
      if (!d) { out.push({ t: 'circle', x: f(x(i)), y: 30, r: 3, c: 'bead-off' }); return }
      const r = f(4 + 5 * Math.sqrt(d / max))
      if (last) out.push({ t: 'circle', x: f(x(i)), y: 30, r: r + 4, c: 'ring' })
      out.push({ t: 'circle', x: f(x(i)), y: 30, r, c: last ? 'bead-on' : 'bead' })
    })
    text(x(0), 58, v.from)
    text(x(n - 1), 58, v.to, 'label-on')
    if (n <= 10) v.labels.forEach((l, i) => { if (i && i < n - 1) text(x(i), 58, l, 'label tiny') })
  } else {
    const n = v.days.length, x = spread(n), w = clamp(308 / n * .55, 3, 20)
    const max = Math.max(1, ...v.days)
    line(L, 46, R, 46, 'base')
    v.days.forEach((d, i) => { const h = d ? Math.max(1.5, d / max * 36) : 0; rect(x(i) - w / 2, 46 - h, w, h, i === v.peak ? 'col-on' : 'col-dim') })
    if (v.peak >= 0) {
      text(x(v.peak), 6, String(v.days[v.peak]), 'label-strong')
      text(clamp(x(v.peak), 34, 314), 60, v.peakLabel, 'label-on')
    }
    if (v.peak !== n - 1 && (v.peak < 0 || Math.abs(x(n - 1) - clamp(x(v.peak), 34, 314)) > 70)) text(x(n - 1), 60, 'today')
  }
  return out
})
</script>

<template>
  <svg class="viz" viewBox="0 0 348 64" role="img" :aria-label="viz.aria">
    <template v-for="(s, i) in shapes" :key="i">
      <rect v-if="s.t === 'rect'" :x="s.x" :y="s.y" :width="s.w" :height="s.h" :rx="s.r" :class="s.c" />
      <line v-else-if="s.t === 'line'" :x1="s.x1" :y1="s.y1" :x2="s.x2" :y2="s.y2" :class="s.c" />
      <circle v-else-if="s.t === 'circle'" :cx="s.x" :cy="s.y" :r="s.r" :class="s.c" />
      <path v-else-if="s.t === 'path'" :d="s.d" :class="s.c" />
      <text v-else :x="s.x" :y="s.y" :text-anchor="s.a" :class="s.c">{{ s.text }}</text>
    </template>
  </svg>
</template>

<style scoped>
.viz { display: block; width: 100%; height: auto; overflow: visible; }
.base { stroke: var(--line-2); stroke-width: 1.2; }
.grid { stroke: var(--line); stroke-width: 1; }
.trend { fill: none; stroke: var(--gold-ink); stroke-width: 1.3; stroke-dasharray: 3 3; opacity: .7; }
.area { fill: color-mix(in srgb, var(--teal) 12%, transparent); }
.stroke { fill: none; stroke: var(--teal); stroke-width: 2.2; stroke-linejoin: round; stroke-linecap: round; }
.dot { fill: var(--teal); }
.dot-end, .bead-on { fill: var(--teal); stroke: var(--surface); stroke-width: 1.5; }
.col { fill: color-mix(in srgb, var(--teal) 42%, transparent); }
.col-mid { fill: color-mix(in srgb, var(--teal) 55%, transparent); }
.col-dim { fill: color-mix(in srgb, var(--teal) 28%, transparent); }
.col-on { fill: var(--teal); }
.fix { fill: color-mix(in srgb, var(--gold) 65%, transparent); }
.placeholder { fill: none; stroke: var(--line-2); stroke-width: 1.2; stroke-dasharray: 2 2; }
.tick { stroke: color-mix(in srgb, var(--teal) 42%, transparent); stroke-width: 2.2; stroke-linecap: round; }
.tick-on { stroke: var(--teal); stroke-width: 2.8; stroke-linecap: round; }
.wait { stroke: var(--gold-ink); stroke-width: 2.4; stroke-dasharray: 3 3; stroke-linecap: round; }
.now { fill: var(--surface); stroke: var(--gold-ink); stroke-width: 2.2; }
.bracket { fill: none; stroke: var(--gold-ink); stroke-width: 1.2; stroke-linejoin: round; }
.marker { stroke: var(--gold-ink); stroke-width: 1.6; stroke-dasharray: 2.5 2.5; }
.chain { stroke: color-mix(in srgb, var(--teal) 42%, transparent); stroke-width: 2; }
.bead { fill: color-mix(in srgb, var(--teal) 45%, var(--surface)); stroke: var(--surface); stroke-width: 1.5; }
.bead-off { fill: none; stroke: var(--line-2); stroke-width: 1.4; }
.ring { fill: none; stroke: color-mix(in srgb, var(--teal) 45%, transparent); stroke-width: 1.4; }
text { font-family: var(--mono); font-size: 9.5px; font-weight: 500; font-variant-numeric: tabular-nums; }
.label { fill: var(--ink-3); }
.label-on, .value-on { fill: var(--teal-ink); font-weight: 600; }
.label-strong { fill: var(--teal-ink); font-size: 10px; font-weight: 700; }
.label-gold { fill: var(--gold-ink); font-weight: 600; }
.small { font-size: 9px; }
.tiny { font-size: 8.5px; font-weight: 400; }
</style>
