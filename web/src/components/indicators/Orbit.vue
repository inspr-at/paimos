<!-- Orbit · creative calm · a precise satellite circles a stationary faceted core.
     SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import type { AgentState } from '../../lib/agentSignals'
import AgentStateMark from './AgentStateMark.vue'
import { artStyle, type IndicatorRing } from '../../lib/indicatorVariants'
import { computed, ref, watch } from 'vue'

// IV1 discovers this default SFC export by filename. The wrapper owns labels,
// viewer preferences and hovering; only real event counter advances flash.
// The track and satellite are this style's ring; the core is its inner artwork.
const props = withDefaults(defineProps<{
  state: AgentState
  size?: number
  pulse: number
  seed: string
  lead: boolean
  ring?: IndicatorRing
  artScale?: number
}>(), { size: 26, ring: 'moving', artScale: 1 })
const inner = computed(() => artStyle(props.artScale))
const style = computed(() => {
  let hash = 2166136261
  for (const char of props.seed) hash = Math.imul(hash ^ char.codePointAt(0)!, 16777619) >>> 0
  return { '--size': `${props.size}px`, '--phase': `${-(hash % 8192) / 1000}s` }
})
const glint = ref(0)
let sequence = 0
let lastPulse = props.pulse
watch(() => props.pulse, value => {
  if (!Number.isFinite(value) || value <= lastPulse) return
  lastPulse = value
  if (props.state === 'working') glint.value = ++sequence
})
watch(() => props.state, state => { if (state !== 'working') glint.value = 0 })
</script>

<template>
  <svg class="agent-indicator-art indicator-orbit" :class="[state, { lead, 'ring-moving': ring === 'moving' }]" :style="style" :width="size" :height="size"
    :data-state="state" viewBox="0 0 32 32" aria-hidden="true" focusable="false">
    <circle v-if="ring !== 'off'" class="orbit-track" cx="16" cy="16" r="12" />
    <g class="inner-art" :style="inner">
      <path class="core" d="m16 8 7 4v8l-7 4-7-4v-8Z" />
      <path class="facet" d="m9 12 7 4 7-4M16 16v8" />
      <path class="light-facet" d="m9 12 7 4v8l-7-4Z" />
    </g>
    <g class="satellite idle">
      <template v-if="ring !== 'off'">
        <path class="trail" d="M7.5 7.5A12 12 0 0 1 16 4" />
        <circle class="point" cx="16" cy="4" r="1.8" />
      </template>
      <path v-if="glint && state === 'working'" :key="glint" class="glint"
        d="m16 .8.9 2.3 2.3.9-2.3.9-.9 2.3-.9-2.3-2.3-.9 2.3-.9Z" @animationend="glint = 0" />
    </g>
    <AgentStateMark :state="state" x="21" y="0" :size="11" />
  </svg>
</template>

<style scoped>
.indicator-orbit {

  display: inline-block; flex: none; width: var(--size); height: var(--size); vertical-align: middle;
  color: var(--signal); overflow: visible;
}
.orbit-track, .core, .facet, .trail { stroke: currentColor; stroke-width: 1.5; stroke-linejoin: round; fill: none; }
.core, .facet { stroke-width: calc(1.5px * var(--art-stroke, 1)); }
.orbit-track { opacity: .3; stroke-width: 1; }
.core { fill: color-mix(in srgb, currentColor 9%, var(--surface-raised)); }
.facet { opacity: .85; }
.light-facet { fill: currentColor; opacity: .1; }
.trail { opacity: .4; stroke-linecap: round; }
.point { fill: currentColor; stroke: var(--surface-raised); stroke-width: 1; }
.satellite { transform-origin: 16px 16px; }
.clock { fill: var(--surface-raised); stroke: currentColor; stroke-width: 1.5; stroke-linecap: round; stroke-linejoin: round; }
.clock path { fill: none; }
.glint { fill: var(--secondary-line); stroke: var(--surface-raised); stroke-width: .6; transform-origin: 16px 4px; animation: orbit-flash .6s ease-out both; }
@media (prefers-reduced-motion: no-preference) {
  .ring-moving.working.lead .satellite { animation: orbit-turn 6s linear infinite; animation-delay: var(--phase); }
  .lead .glint { animation-name: orbit-spark; }
}
@keyframes orbit-turn { to { transform: rotate(360deg); } }
@keyframes orbit-spark { 0% { opacity: 0; transform: scale(.5); } 25% { opacity: 1; transform: scale(1); } 100% { opacity: 0; transform: scale(.8); } }
@keyframes orbit-flash { 0%, 100% { opacity: 0; } 25%, 55% { opacity: 1; } }
</style>
