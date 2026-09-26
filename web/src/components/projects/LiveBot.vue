<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import type { Harness } from '../../lib/agents'
import type { LiveBotState } from '../../lib/liveAgents'

// Shared LA2 / AM1 indicator: import LiveBot from components/projects/LiveBot.vue.
// `state`: working | waiting (approval) | stale; `harness`: identity metadata,
// never a state colour; `size`: CSS pixels. The parent supplies an accessible
// status label (this artwork is decorative). No store or API dependency.
// `eventPulse`: a monotonic counter advanced ONLY by observed activity_sequence
// or heartbeat evidence. Initial render, remount, hover and state changes do
// not pulse. Optional eventCaption must describe that evidence, never a guess.
const props = withDefaults(defineProps<{
  state?: LiveBotState; harness?: Harness; size?: number; eventPulse?: number; eventCaption?: string
}>(), { state: 'working', size: 28, eventPulse: 0, eventCaption: '' })
const style = computed(() => ({ '--size': `${props.size}px` }))
const pulse = ref(0)
let lastPulse = props.eventPulse
let clear: ReturnType<typeof setTimeout> | undefined
watch(() => props.eventPulse, value => {
  if (!Number.isFinite(value) || value <= lastPulse) return
  lastPulse = value
  if (props.state === 'stale') return
  clearTimeout(clear)
  pulse.value++
  clear = setTimeout(() => { pulse.value = 0 }, 600)
})
watch(() => props.state, state => {
  if (state === 'stale') { clearTimeout(clear); pulse.value = 0 }
})
onBeforeUnmount(() => clearTimeout(clear))
</script>

<template>
  <span class="live-bot" :class="state" :style="style" :data-state="state" :data-harness="harness" aria-hidden="true">
    <svg class="bot" viewBox="0 0 32 32" focusable="false">
      <circle class="disk" cx="16" cy="16" r="14.5" />
      <circle class="ring-track" cx="16" cy="16" r="14" />
      <circle v-if="state !== 'stale'" class="ring-sweep" cx="16" cy="16" r="14" pathLength="100" />
      <g class="robot">
        <path d="M16 10V7.5M14 7.5h4M8.5 15H7v5h1.5M23.5 15H25v5h-1.5" />
        <rect x="9" y="10.5" width="14" height="12" rx="1.5" />
        <path d="M12.5 15.5h2M17.5 15.5h2M13 19h6M12 25h8M16 22.5V25" />
      </g>
      <g v-if="state === 'waiting'" class="clock">
        <circle cx="26" cy="6" r="5" />
        <path d="M26 3.5V6l1.8 1.2" />
      </g>
      <path v-if="pulse && state !== 'stale'" :key="pulse" class="glint" d="m26 1 1.5 3.5L31 6l-3.5 1.5L26 11l-1.5-3.5L21 6l3.5-1.5Z" />
    </svg>
    <span v-if="pulse && eventCaption" :key="`caption-${pulse}`" class="event-caption">{{ eventCaption }}</span>
  </span>
</template>

<style scoped>
.live-bot {
  --signal: var(--teal); position: relative; display: inline-grid; place-items: center;
  flex-shrink: 0; width: var(--size); height: var(--size); vertical-align: middle;
}
.live-bot.waiting { --signal: var(--warn); }
.live-bot.stale { --signal: var(--ink-3); }
.bot { display: block; width: 100%; height: 100%; overflow: visible; }
.disk { fill: var(--surface-raised); }
.ring-track, .ring-sweep { fill: none; stroke: var(--signal); stroke-width: 1.35; }
.ring-track { opacity: .24; }
.ring-sweep { stroke-dasharray: 30 70; stroke-linecap: round; transform-origin: 16px 16px; transform: rotate(-85deg); }
.robot { fill: none; stroke: var(--ink); stroke-width: 1.35; stroke-linecap: round; stroke-linejoin: round; }
.clock { fill: var(--surface-raised); stroke: var(--signal); stroke-width: 1.4; stroke-linecap: round; stroke-linejoin: round; }
.clock path { fill: none; }
.stale .ring-track { stroke-dasharray: .6 3.2; stroke-linecap: round; opacity: .8; }
.stale .robot { stroke: var(--ink-3); }
.glint { fill: var(--gold-ink); stroke: var(--surface-raised); stroke-width: .7; transform-origin: 26px 6px; animation: event-opacity .6s ease-out both; }
.event-caption { position: absolute; top: calc(100% + 3px); left: 50%; translate: -50% 0; white-space: nowrap; font: 500 10px/1.2 var(--font); color: var(--gold-ink); pointer-events: none; animation: event-opacity .6s ease-out both; }
@media (prefers-reduced-motion: no-preference) {
  .ring-sweep { animation: activity-sweep 2.4s cubic-bezier(.4, .25, .6, .75) infinite; }
  .waiting .ring-sweep { animation-play-state: paused; }
  .glint { animation-name: event-glint; }
}
@keyframes activity-sweep { from { transform: rotate(-85deg); } to { transform: rotate(275deg); } }
@keyframes event-glint { 0% { opacity: 0; transform: scale(.65); } 25% { opacity: 1; transform: scale(1); } 100% { opacity: 0; transform: scale(.85); } }
@keyframes event-opacity { 0%, 100% { opacity: 0; } 25%, 50% { opacity: 1; } }
</style>

<style>
/* The project chip pauses the ring while outside the viewport. */
.asleep .live-bot .ring-sweep { animation-play-state: paused; }
</style>
