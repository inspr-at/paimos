<!-- Sprite · creative playful · a little firefly works its wings and lights up for real events.
     SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import type { AgentState } from '../../lib/agentSignals'
import AgentStateMark from './AgentStateMark.vue'
import RobotExpression from './parts/RobotExpression.vue'
import { computed, ref, watch } from 'vue'

// IV1 discovers this default SFC export by filename. This companion stays
// anchored: wing motion is work; any whole-character hovering belongs to IV1.
const props = withDefaults(defineProps<{
  state: AgentState
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
  if (props.state === 'working') glint.value = ++sequence
})
watch(() => props.state, state => { if (state !== 'working') glint.value = 0 })
</script>

<template>
  <svg class="agent-indicator-art indicator-sprite" :class="[state, { lead, flashing: glint && state === 'working' }]" :style="style" :width="size" :height="size"
    :data-state="state" viewBox="0 0 32 32" aria-hidden="true" focusable="false">
    <g class="wings idle">
      <path d="M12 15C9 8 3 10 3.5 16C4 20 9 20 12 18ZM20 15C23 8 29 10 28.5 16C28 20 23 20 20 18Z" />
    </g>
    <path class="antennae" d="M13 9Q12 4 9.5 5M19 9Q20 4 22.5 5" />
    <path class="body" d="M16 7C12 7 10.5 10 10.5 14C10.5 19.5 12 26 16 27C20 26 21.5 19.5 21.5 14C21.5 10 20 7 16 7Z" />
    <path class="lantern idle" d="M11.4 19Q16 21 20.6 19C20 23 18.5 26.4 16 27C13.5 26.4 12 23 11.4 19Z" />
    <g v-if="state === 'working'" class="eyes idle"><ellipse cx="14" cy="12.7" rx=".85" ry="1.2" /><ellipse cx="18" cy="12.7" rx=".85" ry="1.2" /></g>
    <path v-if="state === 'working'" class="smile" d="M14.8 16q1.2 1 2.4 0" />
    <path v-if="state === 'working'" class="joy" d="M12.8 13.3q1.2-1.7 2.4 0M16.8 13.3q1.2-1.7 2.4 0" />
    <g v-if="glint && state === 'working'" :key="glint" class="glint" @animationend.self="glint = 0">
      <path class="lit-tail" d="M11.4 19Q16 21 20.6 19C20 23 18.5 26.4 16 27C13.5 26.4 12 23 11.4 19Z" />
      <path class="sparks" d="m27 19 .8 2.2L30 22l-2.2.8L27 25l-.8-2.2L24 22l2.2-.8ZM5 20l.6 1.4L7 22l-1.4.6L5 24l-.6-1.4L3 22l1.4-.6Z" />
    </g>
    <RobotExpression v-if="state !== 'working'" :state="state" :eye-y="12.7" :mouth-y="16" :spread="2" />
    <AgentStateMark :state="state" x="21" y="0" :size="11" />
  </svg>
</template>

<style scoped>
.indicator-sprite {

  display: inline-block; flex: none; width: var(--size); height: var(--size); vertical-align: middle;
  color: var(--signal); overflow: visible;
}
.wings, .body, .antennae, .smile, .joy { stroke: currentColor; stroke-width: 1.5; stroke-linecap: round; stroke-linejoin: round; }
.wings { fill: color-mix(in srgb, currentColor 16%, var(--surface-raised, #fffefa)); transform-origin: 16px 16px; }
.body { fill: color-mix(in srgb, currentColor 7%, var(--surface-raised, #fffefa)); }
.antennae, .smile, .joy { fill: none; }
.lantern { fill: currentColor; opacity: .36; }
.eyes { fill: var(--ink, #203c3d); transform-origin: 16px 12.7px; }
.stale .eyes { fill: currentColor; }
.joy { opacity: 0; }
.flashing.lead .eyes { opacity: 0; }
.flashing.lead .joy { opacity: 1; }
.clock { fill: var(--surface-raised, #fffefa); stroke: currentColor; stroke-width: 1.5; stroke-linecap: round; stroke-linejoin: round; }
.clock path { fill: none; }
.glint { fill: #c9a24a; animation: sprite-flash .6s ease-out both; }
.sparks { transform-origin: 16px 22px; }
@media (prefers-reduced-motion: no-preference) {
  .working.lead .wings { animation: sprite-wings 1.4s ease-in-out infinite; animation-delay: var(--phase); }
  .working.lead .lantern { animation: sprite-light 2.8s ease-in-out infinite; animation-delay: var(--phase); }
  .working.lead .eyes { animation: sprite-blink 6.8s linear infinite; animation-delay: var(--phase); }
  .lead .sparks { animation: sprite-spark .6s ease-out both; }
}
@keyframes sprite-wings { 0%, 100% { transform: scaleX(1); } 50% { transform: scaleX(.78); } }
@keyframes sprite-light { 0%, 100% { opacity: .25; } 50% { opacity: .55; } }
@keyframes sprite-blink { 0%, 91%, 97%, 100% { transform: scaleY(1); } 94% { transform: scaleY(.15); } }
@keyframes sprite-spark { 0% { opacity: 0; transform: scale(.85); } 25% { opacity: 1; transform: scale(1.05); } 100% { opacity: 0; transform: scale(1); } }
@keyframes sprite-flash { 0%, 100% { opacity: 0; } 25%, 55% { opacity: 1; } }
</style>
