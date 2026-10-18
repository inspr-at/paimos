<!-- Pulse · professional (0/5). A measured radial sweep and a steady centre signal, with no character. -->
<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import type { AgentState } from '../../lib/agentSignals'
import { artStyle, type IndicatorRing } from '../../lib/indicatorVariants'
import AgentStateMark from './AgentStateMark.vue'
import { computed, onBeforeUnmount, ref, watch } from 'vue'

const props = withDefaults(defineProps<{
  state: AgentState; size?: number; pulse: number; seed: string; lead: boolean; ring?: IndicatorRing; artScale?: number
}>(), { size: 26, ring: 'moving', artScale: 1 })
const inner = computed(() => artStyle(props.artScale))
const style = computed(() => ({
  '--lag': `${-(Array.from(props.seed).reduce((hash, char) => (hash * 31 + char.charCodeAt(0)) >>> 0, 0) % 700) / 100}s`,
}))
const glint = ref(0)
let serial = 0
let lastPulse = props.pulse
let clear: ReturnType<typeof setTimeout> | undefined
// Watching without immediate prevents a replay when an existing agent mounts.
watch(() => props.pulse, value => {
  if (!Number.isFinite(value) || value <= lastPulse) return
  lastPulse = value
  if (props.state !== 'working') return
  clearTimeout(clear)
  glint.value = ++serial
  clear = setTimeout(() => { glint.value = 0 }, 600)
})
watch(() => props.state, state => {
  if (state !== 'working') { clearTimeout(clear); glint.value = 0 }
})
onBeforeUnmount(() => clearTimeout(clear))
</script>

<template>
  <svg class="agent-indicator-art indicator pulse" :class="[state, { lead, 'ring-moving': ring === 'moving' }]" :style="style" viewBox="0 0 32 32" :width="size" :height="size" aria-hidden="true" focusable="false">
    <circle class="disk" cx="16" cy="16" r="14" />
    <template v-if="ring !== 'off'">
      <circle class="track" cx="16" cy="16" r="11" />
      <path class="ticks" d="M16 2v2M28 16h2M16 28v2M2 16h2" />
      <circle class="sweep" cx="16" cy="16" r="11" pathLength="100" />
    </template>
    <g class="inner-art" :style="inner">
      <circle class="centre-halo" cx="16" cy="16" r="5" />
      <circle class="centre" cx="16" cy="16" r="3" />
    </g>
    <AgentStateMark :state="state" x="21" y="0" :size="11" />
    <path v-if="glint && state === 'working'" :key="glint" class="glint" d="m26 1 1.5 3.5L31 6l-3.5 1.5L26 11l-1.5-3.5L21 6l3.5-1.5Z" />
  </svg>
</template>

<style scoped>

.indicator {  display: block; overflow: visible; }
@media (prefers-color-scheme: dark) {
}
.clock { fill: var(--surface-raised); stroke: var(--signal); stroke-width: 1.4; stroke-linecap: round; stroke-linejoin: round; }
.clock path { fill: none; }
.glint { fill: var(--secondary-line); stroke: var(--surface-raised); stroke-width: .65; transform-origin: 26px 6px; animation: event-opacity .6s ease-out both; }
@keyframes event-opacity { 0%, 100% { opacity: 0; } 25%, 50% { opacity: 1; } }
@keyframes event-glint { 0% { opacity: 0; transform: scale(.65); } 25% { opacity: 1; transform: scale(1); } 100% { opacity: 0; transform: scale(.85); } }
@media (prefers-reduced-motion: no-preference) { .glint { animation-name: event-glint; } }

.disk { fill: var(--surface-raised); }
.track, .sweep, .ticks { fill: none; stroke: var(--signal); }
.track { stroke-width: 1.5; opacity: .28; }
.ticks { stroke-width: 1; opacity: .5; }
.sweep { stroke-width: 1.75; stroke-linecap: round; stroke-dasharray: 22 78; transform-origin: 16px 16px; transform: rotate(-90deg); }
.centre-halo { fill: var(--signal); opacity: .09; }
.centre { fill: var(--signal); }
.stale .sweep { opacity: .4; }
@media (prefers-reduced-motion: no-preference) {
  .ring-moving.working.lead .sweep { animation: sweep 3.2s linear infinite; animation-delay: var(--lag); }
  .ring-moving.working:not(.lead) .sweep { animation: quiet-signal 5s ease-in-out infinite; animation-delay: var(--lag); }
}
@keyframes sweep { from { transform: rotate(-90deg); } to { transform: rotate(270deg); } }
@keyframes quiet-signal { 0%, 100% { opacity: .45; } 50% { opacity: .8; } }

</style>
