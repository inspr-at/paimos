<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// AEON-1497 addendum (Markus: "yes add it"): a quota-left line in the folded dial's empty gap
// after a quota-reporting harness's count. Placed absolutely over that gap, so the control's
// size and its units' positions are the same with or without it (0 px). Amber only on the end
// and the dot when the line runs dry before the refill.
import type { MiniGeometry } from '../../lib/quotaOutlook'

defineProps<{ geo: MiniGeometry; stale?: boolean; left: number }>()
</script>

<template>
  <span class="mini" :style="{ left: `${left}px`, width: `${geo.w}px` }" aria-hidden="true">
    <svg class="mini-svg" :class="{ 'm-dry': geo.dry && !stale, 'm-stale': stale }" :width="geo.w" :height="geo.h" :viewBox="`0 0 ${geo.w} ${geo.h}`" focusable="false">
      <line class="m-floor" x1="0" :x2="geo.w" :y1="geo.floor" :y2="geo.floor" />
      <path class="m-past" :d="geo.past" />
      <path v-if="geo.proj" class="m-proj" :d="geo.proj" />
      <circle class="m-dot" :cx="geo.dot.x.toFixed(1)" :cy="geo.dot.y.toFixed(1)" r="1.5" />
    </svg>
  </span>
</template>

<style scoped>
.mini { position: absolute; top: 50%; height: 14px; margin-top: -7px; pointer-events: none; transition: opacity .15s ease; }
.mini-svg { display: block; overflow: visible; }
.m-past, .m-proj { fill: none; stroke: var(--mini-ink); stroke-width: 1.3; stroke-linejoin: round; stroke-linecap: round; }
.m-proj { stroke-dasharray: 2.5 2; }
.m-floor { stroke: var(--line-2); stroke-width: 1; }
.m-dot { fill: var(--mini-ink); }
.m-dry .m-proj { stroke: var(--warn); }
.m-dry .m-dot { fill: var(--warn); }
.m-stale :is(.m-past, .m-proj) { stroke: var(--ink-3); }
.m-stale .m-dot { fill: var(--ink-3); }
@media (max-width: 1099px) { .mini { display: none; } }
@media (prefers-reduced-motion: reduce) { .mini { transition: none; } }
</style>
