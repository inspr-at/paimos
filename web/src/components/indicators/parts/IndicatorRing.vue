<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import type { AgentState } from '../../../lib/agentSignals'
// Ring layers for renderers whose own drawing lacks them: a plain track for
// ringless art, an activity sweep for still rims. Drawn on the 32-unit grid.
withDefaults(defineProps<{ state: AgentState; lead: boolean; track?: boolean; sweep?: boolean; r?: number }>(), { track: false, sweep: false, r: 14 })
</script>

<template>
  <g class="indicator-ring" :class="[state, { lead }]">
    <circle v-if="track" class="ring-track" cx="16" cy="16" :r="r" />
    <circle v-if="sweep && state !== 'stale'" class="ring-sweep" cx="16" cy="16" :r="r" pathLength="100" />
  </g>
</template>

<style scoped>
.ring-track { fill: none; stroke: var(--signal); stroke-width: 1.25; opacity: .38; }
.stale .ring-track { stroke-dasharray: .6 3.2; stroke-linecap: round; opacity: .6; }
.ring-sweep { fill: none; stroke: var(--signal); stroke-width: 1.35; stroke-dasharray: 30 70; stroke-linecap: round; transform-origin: 16px 16px; transform: rotate(-85deg); }
@media (prefers-reduced-motion: no-preference) {
  .working .ring-sweep { animation: ring-sweep 2.4s cubic-bezier(.4, .25, .6, .75) infinite; animation-delay: var(--lag, 0s); }
  .working:not(.lead) .ring-sweep { animation-duration: 4.8s; }
}
@keyframes ring-sweep { from { transform: rotate(-85deg); } to { transform: rotate(275deg); } }
</style>
