<!-- Robot 1 · calm end (1/5). Original LA2 line robot; an even activity arc does the work. -->
<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import type { AgentState } from '../../lib/agentSignals'
import AgentStateMark from './AgentStateMark.vue'
import { onBeforeUnmount, ref, watch } from 'vue'

const props = withDefaults(defineProps<{
  state: AgentState; size?: number; pulse: number; seed: string; lead: boolean
}>(), { size: 26 })
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
  <svg class="agent-indicator-art indicator robot1" :class="[state, { lead }]" viewBox="0 0 32 32" :width="size" :height="size" aria-hidden="true" focusable="false">
    <circle class="disk" cx="16" cy="16" r="14.5" />
    <circle class="ring-track" cx="16" cy="16" r="14" />
    <circle v-if="state !== 'stale'" class="ring-sweep" cx="16" cy="16" r="14" pathLength="100" />
    <g class="robot">
      <path d="M16 10V7.5M14 7.5h4M8.5 15H7v5h1.5M23.5 15H25v5h-1.5" />
      <rect x="9" y="10.5" width="14" height="12" rx="1.5" />
      <path d="M12.5 15.5h2M17.5 15.5h2M13 19h6M12 25h8M16 22.5V25" />
    </g>
    <AgentStateMark :state="state" x="21" y="0" :size="11" />
    <path v-if="glint && state === 'working'" :key="glint" class="glint" d="m26 1 1.5 3.5L31 6l-3.5 1.5L26 11l-1.5-3.5L21 6l3.5-1.5Z" />
  </svg>
</template>

<style scoped>

.indicator {  display: block; overflow: visible; }
.clock { fill: var(--surface-raised); stroke: var(--signal); stroke-width: 1.4; stroke-linecap: round; stroke-linejoin: round; }
.clock path { fill: none; }
.glint { fill: #c9a24a; stroke: var(--surface-raised); stroke-width: .65; transform-origin: 26px 6px; animation: event-opacity .6s ease-out both; }
@keyframes event-opacity { 0%, 100% { opacity: 0; } 25%, 50% { opacity: 1; } }
@keyframes event-glint { 0% { opacity: 0; transform: scale(.65); } 25% { opacity: 1; transform: scale(1); } 100% { opacity: 0; transform: scale(.85); } }
@media (prefers-reduced-motion: no-preference) { .glint { animation-name: event-glint; } }

.disk { fill: var(--surface-raised); }
.ring-track, .ring-sweep { fill: none; stroke: var(--signal); stroke-width: 1.35; }
.ring-track { opacity: .24; }
.ring-sweep { stroke-dasharray: 30 70; stroke-linecap: round; transform-origin: 16px 16px; transform: rotate(-85deg); }
.robot { fill: none; stroke: var(--ink); stroke-width: 1.35; stroke-linecap: round; stroke-linejoin: round; }
.waiting .ring-sweep { animation-play-state: paused; }
.stale .ring-track { stroke-dasharray: .6 3.2; stroke-linecap: round; opacity: .8; }
.stale .robot { stroke: var(--ink-3); }
@media (prefers-reduced-motion: no-preference) {
  .working .ring-sweep { animation: activity-sweep 2.4s cubic-bezier(.4, .25, .6, .75) infinite; }
  :global(.live-bot.hovering .robot1.working .robot) { animation: bot-hover 1.9s ease-in-out infinite; animation-delay: var(--lag); }
  :global(.live-bot.hovering .robot1.working:not(.lead) .robot) { animation-duration: 2.3s; }
}
@keyframes bot-hover { 0%, 100% { transform: translateY(0); } 50% { transform: translateY(-1.1px); } }
@keyframes activity-sweep { from { transform: rotate(-85deg); } to { transform: rotate(275deg); } }

</style>
