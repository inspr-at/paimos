<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// The small chart inside a Simple tile (AEON-994 draft 5): the target zone shaded
// green, an arrow on the axis marked "better", hatched "no data yet", one point per
// bucket of the window. The nightly run shows one square per night instead.
// Decorative: the tile says the same in words, so the chart is hidden from screen readers.
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { sparkGeometry, type SparkModel } from '../../lib/deliverySimple'
import { fill } from '../../lib/deliveryNumbersText'
import type { SimpleText } from '../../lib/deliverySimpleText'

const props = defineProps<{ model: SparkModel; text: SimpleText }>()
const host = ref<HTMLElement>()
const width = ref(0)
const H = 62
let observer: ResizeObserver | null = null
onMounted(() => {
  observer = new ResizeObserver(entries => { width.value = Math.round(entries[0]?.contentRect.width ?? 0) })
  if (host.value) { width.value = Math.round(host.value.clientWidth); observer.observe(host.value) }
})
onBeforeUnmount(() => observer?.disconnect())
const W = computed(() => Math.max(120, width.value))
const geometry = computed(() => props.model.kind === 'line' ? sparkGeometry(props.model, W.value, H) : null)

// Nightly: the last nights (or weeks, months) as squares of at most 18 px.
const squares = computed(() => {
  const nights = props.model.mode === 'ready' ? props.model.nights : []
  const count = nights.length, gap = 4
  if (!count) return []
  const s = Math.min(18, Math.floor((W.value - gap * (count - 1)) / count))
  return nights.map((night, k) => ({ night, x: k * (s + gap), y: 6, s }))
})
const note = computed(() => fill(props.text.nightsNote[props.model.unit], { n: squares.value.length }))
</script>

<template>
  <div ref="host" class="s-spark" aria-hidden="true">
    <svg v-if="width && model.mode !== 'loading'" :viewBox="`0 0 ${W} ${H}`" :width="W" :height="H">
      <g v-if="model.kind === 'nightly'">
        <template v-for="(square, k) in squares" :key="k">
          <rect v-if="square.night === 'nodata'" :x="square.x" :y="square.y" :width="square.s" :height="square.s" rx="3" class="g-nodata" />
          <rect v-if="square.night === 'none' || square.night === 'nodata'" :x="square.x + .5" :y="square.y + .5" :width="square.s - 1" :height="square.s - 1" rx="3" class="n-sq none" />
          <rect v-else :x="square.x" :y="square.y" :width="square.s" :height="square.s" rx="3" class="n-sq" :class="square.night" />
          <path v-if="square.night === 'ok'" class="n-ic"
            :d="`M${square.x + square.s / 2 - square.s * .264},${square.y + square.s / 2}L${square.x + square.s / 2 - square.s * .055},${square.y + square.s / 2 + square.s * .22}L${square.x + square.s / 2 + square.s * .275},${square.y + square.s / 2 - square.s * .22}`" />
          <path v-else-if="square.night === 'bad'" class="n-ic"
            :d="`M${square.x + square.s * .28},${square.y + square.s * .28}L${square.x + square.s * .72},${square.y + square.s * .72}M${square.x + square.s * .72},${square.y + square.s * .28}L${square.x + square.s * .28},${square.y + square.s * .72}`" />
        </template>
        <rect v-if="!squares.length" x="0" y="6" :width="W" height="18" rx="3" class="g-nodata" />
        <text v-if="squares.length" x="0" :y="H - 8" class="sp-note">{{ note }}</text>
      </g>
      <g v-else-if="geometry">
        <rect v-if="geometry.zone" class="sp-zone" :x="geometry.zone.x" :y="geometry.zone.y" :width="geometry.zone.width" :height="Math.max(0, geometry.zone.height)" />
        <g v-for="(hatch, h) in geometry.hatches" :key="`h${h}`">
          <rect class="g-nodata" :x="hatch.x" :y="geometry.plot.y" :width="hatch.width" :height="geometry.plot.height" />
          <text v-if="hatch.label" class="sp-note" :x="hatch.x + hatch.width / 2" :y="geometry.plot.y + geometry.plot.height / 2 + 3" text-anchor="middle">{{ text.noDataYet }}</text>
        </g>
        <template v-if="geometry.target">
          <line class="sp-tgt" :x1="geometry.plot.x" :x2="geometry.plot.x + geometry.plot.width" :y1="geometry.target.y" :y2="geometry.target.y" />
          <text class="sp-tl" :x="geometry.plot.x + geometry.plot.width - 2" :y="geometry.target.labelY" text-anchor="end">{{ geometry.target.label }}</text>
        </template>
        <line class="sp-axis" x1="6" x2="6" :y1="geometry.axis.line[0]" :y2="geometry.axis.line[1]" />
        <path class="sp-axis" :d="geometry.axis.arrow" />
        <text class="sp-better" :x="geometry.axis.better.x" :y="geometry.axis.better.y">{{ text.better }}</text>
        <path v-for="(segment, s) in geometry.segments" :key="`s${s}`" :d="segment.d" class="g-line" :class="{ part: segment.partial }" />
        <circle v-for="(dot, d) in geometry.dots" :key="`d${d}`" :cx="dot.x" :cy="dot.y" :r="dot.last ? 3.6 : 3" class="g-dot" />
      </g>
    </svg>
    <span v-else-if="model.mode === 'loading'" class="sk" />
  </div>
</template>

<style scoped>
.s-spark { position: relative; height: 62px; margin: 2px 0; }
.s-spark svg { display: block; width: 100%; height: 100%; overflow: visible; }
.sk { position: absolute; inset: 0; display: block; border-radius: 6px; background: var(--skeleton); }
.sp-zone { fill: color-mix(in srgb, var(--ok) 11%, transparent); }
.sp-tgt { stroke: var(--ok); stroke-width: 1.5; stroke-dasharray: 4 3; }
.sp-tl { font: 600 10.5px var(--font); fill: var(--ok); paint-order: stroke; stroke: var(--surface-raised); stroke-width: 3px; stroke-linejoin: round; }
.sp-axis { fill: none; stroke: var(--ink-3); stroke-width: 1.3; stroke-linecap: round; stroke-linejoin: round; }
.sp-better { font: 600 10px var(--font); fill: var(--ink-2); paint-order: stroke; stroke: var(--surface-raised); stroke-width: 3px; stroke-linejoin: round; }
.sp-note { font: 500 10.5px var(--font); fill: var(--ink-3); }
.g-nodata { fill: url(#dl-hatch); }
.g-line { fill: none; stroke: var(--teal); stroke-width: 2; stroke-linejoin: round; stroke-linecap: round; }
.g-line.part { stroke-dasharray: 2 3.5; }
.g-dot { fill: var(--teal); stroke: var(--surface-raised); stroke-width: 2; }
.n-sq.ok { fill: var(--ok); }
.n-sq.bad { fill: var(--danger); }
.n-sq.none { fill: transparent; stroke: var(--line-2); stroke-width: 1; }
.n-ic { fill: none; stroke: var(--canvas); stroke-width: 1.8; stroke-linecap: round; stroke-linejoin: round; }
@media (prefers-reduced-motion: no-preference) {
  .sk { background: linear-gradient(90deg, var(--skeleton) 0%, var(--skeleton-hi) 50%, var(--skeleton) 100%) 0 0 / 200% 100%; animation: dl-sk 1.4s ease-in-out infinite; }
}
@keyframes dl-sk { to { background-position: -200% 0; } }
</style>
