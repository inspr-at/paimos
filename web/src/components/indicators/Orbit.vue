<!-- Orbit · creative calm · a precise satellite circles a stationary faceted core.
     SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref, watch } from 'vue'

// IV1 discovers this default SFC export by filename. The wrapper owns labels,
// viewer preferences and hovering; only real event counter advances flash.
const props = withDefaults(defineProps<{
  state: 'working' | 'waiting' | 'stale'
  size?: number
  pulse: number
  seed: string
  lead: boolean
}>(), { size: 26 })
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
  if (props.state !== 'stale') glint.value = ++sequence
})
watch(() => props.state, state => { if (state === 'stale') glint.value = 0 })
</script>

<template>
  <svg class="indicator-orbit" :class="[state, { lead }]" :style="style" :width="size" :height="size"
    :data-state="state" viewBox="0 0 32 32" aria-hidden="true" focusable="false">
    <circle class="orbit-track" cx="16" cy="16" r="12" />
    <path class="core" d="m16 8 7 4v8l-7 4-7-4v-8Z" />
    <path class="facet" d="m9 12 7 4 7-4M16 16v8" />
    <path class="light-facet" d="m9 12 7 4v8l-7-4Z" />
    <g class="satellite idle">
      <path class="trail" d="M7.5 7.5A12 12 0 0 1 16 4" />
      <circle class="point" cx="16" cy="4" r="1.8" />
      <path v-if="glint && state !== 'stale'" :key="glint" class="glint"
        d="m16 .8.9 2.3 2.3.9-2.3.9-.9 2.3-.9-2.3-2.3-.9 2.3-.9Z" @animationend="glint = 0" />
    </g>
    <g v-if="state === 'waiting'" class="clock">
      <circle cx="25.5" cy="6.5" r="4.5" /><path d="M25.5 4v2.5l1.7 1" />
    </g>
  </svg>
</template>

<style scoped>
.indicator-orbit {
  --signal: var(--accent, var(--teal, #2f8f86));
  display: inline-block; flex: none; width: var(--size); height: var(--size); vertical-align: middle;
  color: var(--signal); overflow: hidden;
}
.waiting { --signal: var(--warn, #9a6b12); }
.stale { --signal: var(--st-backlog, #889397); }
.orbit-track, .core, .facet, .trail { stroke: currentColor; stroke-width: 1.5; stroke-linejoin: round; fill: none; }
.orbit-track { opacity: .3; stroke-width: 1; }
.core { fill: color-mix(in srgb, currentColor 9%, var(--surface-raised, #fffefa)); }
.facet { opacity: .85; }
.light-facet { fill: currentColor; opacity: .1; }
.trail { opacity: .4; stroke-linecap: round; }
.point { fill: currentColor; stroke: var(--surface-raised, #fffefa); stroke-width: 1; }
.satellite { transform-origin: 16px 16px; }
.clock { fill: var(--surface-raised, #fffefa); stroke: currentColor; stroke-width: 1.5; stroke-linecap: round; stroke-linejoin: round; }
.clock path { fill: none; }
.glint { fill: #c9a24a; stroke: var(--surface-raised, #fffefa); stroke-width: .6; transform-origin: 16px 4px; animation: orbit-flash .6s ease-out both; }
@media (prefers-reduced-motion: no-preference) {
  .working.lead .satellite { animation: orbit-turn 6s linear infinite; animation-delay: var(--phase); }
  .lead .glint { animation-name: orbit-spark; }
}
@keyframes orbit-turn { to { transform: rotate(360deg); } }
@keyframes orbit-spark { 0% { opacity: 0; transform: scale(.5); } 25% { opacity: 1; transform: scale(1); } 100% { opacity: 0; transform: scale(.8); } }
@keyframes orbit-flash { 0%, 100% { opacity: 0; } 25%, 55% { opacity: 1; } }
</style>
