<!-- Robot 2 · calm-friendly: softened linework, rare blinks and a shallow smile.
     SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import type { AgentState } from '../../lib/agentSignals'
import AgentStateMark from './AgentStateMark.vue'
import RobotExpression from './parts/RobotExpression.vue'
import IndicatorRing from './parts/IndicatorRing.vue'
import { artStyle, type IndicatorRing as RingMode } from '../../lib/indicatorVariants'
import { computed, ref, watch } from 'vue'

// IV1 discovers this presentational SFC by filename; its wrapper owns labels,
// event evidence, viewer preferences and hovering. No API or settings access.
const props = withDefaults(defineProps<{
  state: AgentState
  size?: number
  pulse: number
  seed: string
  lead: boolean
  ring?: RingMode
  artScale?: number
}>(), { size: 26, ring: 'still', artScale: 1 })
const inner = computed(() => artStyle(props.artScale))
const style = computed(() => {
  let hash = 2166136261
  for (const char of props.seed) hash = Math.imul(hash ^ char.charCodeAt(0), 16777619)
  return { '--size': `${props.size}px`, '--phase': `${-((hash >>> 0) % 11000) / 1000}s` }
})
const glint = ref(0)
let sequence = 0
// A pre-existing count on mount is history, not a new event. A keyed path
// restarts one 600 ms glint even when another event arrives during the last.
watch(() => props.pulse, (value, previous) => {
  if (Number.isFinite(value) && value > previous && props.state === 'working') glint.value = ++sequence
})
watch(() => props.state, state => { if (state !== 'working') glint.value = 0 })
</script>

<template>
  <svg class="agent-indicator-art robot-indicator robot-2" :class="[state, { lead }]" :style="style" :width="size" :height="size" viewBox="0 0 32 32" aria-hidden="true" focusable="false">
    <circle class="disk" cx="16" cy="16" r="14.5" />
    <circle v-if="ring !== 'off'" class="rim" cx="16" cy="16" r="14" />
    <IndicatorRing v-if="ring === 'moving'" :state="state" :lead="lead" sweep />
    <g class="inner-art" :style="inner">
    <g class="linework">
      <path d="M16 10V7.5M14.5 7h3M8.5 15H7v4h1.5M23.5 15H25v4h-1.5" />
      <rect class="head" x="8.5" y="10" width="15" height="12.5" rx="3" />
      <path d="M16 22.5V25M12.5 25h7" />
      <g v-if="state === 'working'" class="eyes">
        <path d="M12 15.5h2M18 15.5h2" />
      </g>
      <path v-if="state === 'working'" class="smile" d="M12.9 19.2q3.1 1.25 6.2 0" />
    </g>
    <RobotExpression v-if="state !== 'working'" :state="state" />
    </g>
    <AgentStateMark :state="state" x="21" y="0" :size="11" />
    <path v-if="glint && state === 'working'" :key="glint" class="glint" d="m6 3 1.1 2.9L10 7 7.1 8.1 6 11 4.9 8.1 2 7l2.9-1.1Z" @animationend="glint = 0" />
  </svg>
</template>

<style scoped>
.robot-indicator {

  display: inline-block; flex: none; width: var(--size); height: var(--size); vertical-align: middle; overflow: visible;
}
.disk { fill: var(--surface-raised, #fffefa); }
.rim { fill: none; stroke: var(--signal); stroke-width: 1.25; opacity: .38; }
.linework { fill: none; stroke: var(--ink, #203c3d); stroke-width: calc(1.35px * var(--art-stroke, 1)); stroke-linecap: round; stroke-linejoin: round; }
.head { fill: var(--surface-raised, #fffefa); }
.waiting .linework, .stale .linework { stroke: var(--signal); }
.stale .rim { stroke-dasharray: .6 3.2; stroke-linecap: round; opacity: .6; }
.clock { fill: var(--surface-raised, #fffefa); stroke: var(--signal); stroke-width: 1.4; stroke-linecap: round; stroke-linejoin: round; }
.clock path { fill: none; }
.glint { fill: #c9a24a; stroke: var(--surface-raised, #fffefa); stroke-width: .6; opacity: 0; animation: event-opacity .6s ease-out both; }
@media (prefers-reduced-motion: no-preference) {
  .working.lead .eyes { transform-origin: 16px 15.5px; animation: rare-blink 11s linear infinite; animation-delay: var(--phase); }
  .lead .glint { transform-origin: 6px 7px; animation-name: event-glint; }
}
@keyframes rare-blink { 0%, 94%, 98%, 100% { transform: scaleY(1); } 96% { transform: scaleY(.15); } }
@keyframes event-opacity { 0%, 100% { opacity: 0; } 22%, 48% { opacity: .85; } }
@keyframes event-glint { 0% { opacity: 0; transform: scale(.7); } 25% { opacity: 1; transform: scale(1); } 100% { opacity: 0; transform: scale(.9); } }
</style>
