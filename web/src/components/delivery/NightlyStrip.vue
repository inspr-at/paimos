<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// One square per night (or week, month): green with a tick, red with a cross,
// an empty outline for no run, hatched for no data yet. Shape, never colour alone.
import type { ChartBucket, ChartModel } from '../../lib/deliveryNumbers'

defineProps<{ squares: { x: number; y: number; s: number }[]; nights: ChartModel['nights']; buckets: ChartBucket[]; selected: number | null; muted: boolean }>()
</script>

<template>
  <g>
    <template v-for="(square, index) in squares" :key="index">
      <rect v-if="!muted && nights[index] === 'nodata'" :x="square.x" :y="square.y" :width="square.s" :height="square.s" rx="5" class="g-nodata" />
      <rect v-if="muted || nights[index] === 'none' || nights[index] === 'nodata'" :x="square.x + .5" :y="square.y + .5" :width="square.s - 1" :height="square.s - 1" rx="5" class="n-sq none" />
      <rect v-else :x="square.x" :y="square.y" :width="square.s" :height="square.s" rx="5" class="n-sq" :class="nights[index]" />
      <path v-if="!muted && nights[index] === 'ok'" class="n-ic"
        :d="`M${square.x + square.s / 2 - square.s * .24},${square.y + square.s / 2 + square.s * .01}L${square.x + square.s / 2 - square.s * .05},${square.y + square.s / 2 + square.s * .2}L${square.x + square.s / 2 + square.s * .25},${square.y + square.s / 2 - square.s * .18}`" />
      <path v-else-if="!muted && nights[index] === 'bad'" class="n-ic"
        :d="`M${square.x + square.s * .3},${square.y + square.s * .3}L${square.x + square.s * .7},${square.y + square.s * .7}M${square.x + square.s * .7},${square.y + square.s * .3}L${square.x + square.s * .3},${square.y + square.s * .7}`" />
      <text v-if="buckets[index]?.show" :x="index === squares.length - 1 ? square.x + square.s : square.x + square.s / 2" :y="square.y + square.s + 16"
        class="ax" :class="{ on: index === squares.length - 1 }" :text-anchor="index === squares.length - 1 ? 'end' : 'middle'">{{ buckets[index].label }}</text>
    </template>
    <rect v-if="selected !== null && squares[selected]" :x="squares[selected].x - 2" :y="squares[selected].y - 2" :width="squares[selected].s + 4" :height="squares[selected].s + 4" rx="6" fill="none" class="n-sq sel" />
  </g>
</template>

<style scoped>
.n-sq.ok { fill: var(--ok); }
.n-sq.bad { fill: var(--danger); }
.n-sq.none { fill: transparent; stroke: var(--line-2); stroke-width: 1; }
.n-sq.sel { stroke: var(--ink); stroke-width: 2; }
.n-ic { fill: none; stroke: var(--canvas); stroke-width: 1.8; stroke-linecap: round; stroke-linejoin: round; }
.g-nodata { fill: url(#dl-hatch); }
.ax { font: 500 10px var(--mono); fill: var(--ink-3); font-variant-numeric: tabular-nums; }
.ax.on { fill: var(--ink-2); font-weight: 600; }
</style>
