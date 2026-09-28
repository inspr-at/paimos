<!-- Quill · creative friendly · a fountain nib flexes as it draws a looping flourish.
     SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import type { AgentState } from '../../lib/agentSignals'
import AgentStateMark from './AgentStateMark.vue'
import IndicatorRing from './parts/IndicatorRing.vue'
import { artStyle, type IndicatorRing as RingMode } from '../../lib/indicatorVariants'
import { computed, ref, watch } from 'vue'

// IV1 discovers this default SFC export by filename. No fetching, preferences,
// inferred progress or hovering: the wrapper supplies the shared props.
const props = withDefaults(defineProps<{
  state: AgentState
  size?: number
  pulse: number
  seed: string
  lead: boolean
  ring?: RingMode
  artScale?: number
}>(), { size: 26, ring: 'off', artScale: 1 })
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
  <svg class="agent-indicator-art indicator-quill" :class="[state, { lead }]" :style="style" :width="size" :height="size"
    :data-state="state" viewBox="0 0 32 32" aria-hidden="true" focusable="false">
    <IndicatorRing v-if="ring !== 'off'" :state="state" :lead="lead" track :sweep="ring === 'moving'" :r="15.1" />
    <g class="inner-art" :style="inner">
      <path class="ink-track" d="M7.5 22.5C3 24 4 28 10 27S17 23 23 25.5" />
      <path class="ink-line idle" d="M7.5 22.5C3 24 4 28 10 27S17 23 23 25.5" pathLength="100" />
      <g class="pen idle">
        <path class="nib" d="m7.5 22.5 2.5-10.5 9-5 6 6-5 9Z" />
        <path class="nib-shade" d="m7.5 22.5 9-9 8.5-.5-5 9Z" />
        <path class="engraving" d="m7.5 22.5 8-8m2-6 6 6" />
        <circle class="breather" cx="16.5" cy="13.5" r="1.35" />
      </g>
      <path v-if="glint && state === 'working'" :key="glint" class="glint"
        d="m7.5 19 1 2.5 2.5 1-2.5 1-1 2.5-1-2.5-2.5-1 2.5-1Z" @animationend="glint = 0" />
    </g>
    <AgentStateMark :state="state" x="21" y="0" :size="11" />
  </svg>
</template>

<style scoped>
.indicator-quill {

  display: inline-block; flex: none; width: var(--size); height: var(--size); vertical-align: middle;
  color: var(--signal); overflow: visible;
}
.nib, .engraving, .ink-track, .ink-line { fill: none; stroke: currentColor; stroke-width: calc(1.5px * var(--art-stroke, 1)); stroke-linecap: round; stroke-linejoin: round; }
.nib { fill: color-mix(in srgb, currentColor 7%, var(--surface-raised, #fffefa)); }
.nib-shade { fill: currentColor; opacity: .13; }
.breather { fill: currentColor; }
.ink-track { opacity: .18; }
.ink-line { stroke-dasharray: 100; }
.pen { transform-origin: 7.5px 22.5px; }
.clock { fill: var(--surface-raised, #fffefa); stroke: currentColor; stroke-width: 1.5; stroke-linecap: round; stroke-linejoin: round; }
.clock path { fill: none; }
.glint { fill: #c9a24a; stroke: var(--surface-raised, #fffefa); stroke-width: .6; transform-origin: 7.5px 22.5px; animation: quill-flash .6s ease-out both; }
@media (prefers-reduced-motion: no-preference) {
  .working.lead .pen { animation: quill-write 3.6s ease-in-out infinite; animation-delay: var(--phase); }
  .working.lead .ink-line { animation: quill-ink 3.6s ease-in-out infinite; animation-delay: var(--phase); }
  .lead .glint { animation-name: quill-spark; }
}
@keyframes quill-write { 0%, 65%, 100% { transform: rotate(0); } 20% { transform: rotate(-4deg); } 42% { transform: rotate(3deg); } }
@keyframes quill-ink { 0% { stroke-dashoffset: 100; opacity: .4; } 65%, 82% { stroke-dashoffset: 0; opacity: 1; } 100% { stroke-dashoffset: 0; opacity: 0; } }
@keyframes quill-spark { 0% { opacity: 0; transform: scale(.5) rotate(-15deg); } 25% { opacity: 1; transform: scale(1.1); } 100% { opacity: 0; transform: scale(.8) rotate(15deg); } }
@keyframes quill-flash { 0%, 100% { opacity: 0; } 25%, 55% { opacity: 1; } }
</style>
