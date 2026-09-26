<!-- Robot 4 · lively: busy little hands, quick work glances and one pleased eye-squeeze on a real event.
     SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref, watch } from 'vue'

// Self-contained IV1 variant. The wrapper supplies event evidence and owns
// viewer settings, accessible labels and hovering; no timers or synthetic work.
const props = withDefaults(defineProps<{
  state: 'working' | 'waiting' | 'stale'
  size?: number
  pulse: number
  seed: string
  lead: boolean
}>(), { size: 26 })
const style = computed(() => {
  let hash = 2166136261
  for (const char of props.seed) hash = Math.imul(hash ^ char.charCodeAt(0), 16777619)
  return { '--size': `${props.size}px`, '--phase': `${-((hash >>> 0) % 11000) / 1000}s` }
})
const glint = ref(0)
let sequence = 0
watch(() => props.pulse, (value, previous) => {
  if (Number.isFinite(value) && value > previous && props.state !== 'stale') glint.value = ++sequence
})
watch(() => props.state, state => { if (state === 'stale') glint.value = 0 })
</script>

<template>
  <svg class="robot-indicator robot-4" :class="[state, { lead }]" :style="style" :width="size" :height="size" viewBox="0 0 32 32" aria-hidden="true" focusable="false">
    <circle class="disk" cx="16" cy="16" r="14.5" />
    <circle class="rim" cx="16" cy="16" r="14" />
    <g class="linework">
      <path d="M16 8.5V7l2-2M8 12.5H6.5V17H8M24 12.5h1.5V17H24" />
      <rect class="head" x="8" y="8.5" width="16" height="12.5" rx="4.5" />
    </g>
    <circle class="antenna-halo" cx="18.5" cy="4.5" r="2.4" />
    <circle class="antenna" cx="18.5" cy="4.5" r="1.4" />
    <g class="look">
      <g :key="glint" class="expression" :class="{ celebrating: glint && lead && state === 'working' }">
        <g class="eyes">
          <rect x="11.4" y="12.3" width="2.2" height="3.4" rx="1.1" />
          <rect x="18.4" y="12.3" width="2.2" height="3.4" rx="1.1" />
        </g>
        <path class="pleased" d="M11.2 14.3q1.3-1.7 2.6 0M18.2 14.3q1.3-1.7 2.6 0" />
      </g>
    </g>
    <path class="smile" d="M13.4 18q2.6 2.3 5.2 0" />
    <rect class="keyboard" x="10" y="25" width="12" height="1.8" rx="1.1" />
    <path class="keys" d="M13 25.9h2M17 25.9h2" />
    <g class="hand hand-left"><path d="m9 20-1 2.5 3 1" /><rect x="10" y="22.3" width="3" height="2.4" rx="1.2" /></g>
    <g class="hand hand-right"><path d="m23 20 1 2.5-3 1" /><rect x="19" y="22.3" width="3" height="2.4" rx="1.2" /></g>
    <g v-if="state === 'waiting'" class="clock">
      <circle cx="26" cy="6" r="4.5" /><path d="M26 3.7V6l1.7 1.1" />
    </g>
    <path v-if="glint && state !== 'stale'" :key="glint" class="glint" d="m6 3 1.1 2.9L10 7 7.1 8.1 6 11 4.9 8.1 2 7l2.9-1.1Z" @animationend="glint = 0" />
  </svg>
</template>

<style scoped>
.robot-indicator {
  --signal: color-mix(in srgb, var(--accent, #2f8f86) 70%, var(--teal, #2f8f86));
  display: inline-block; flex: none; width: var(--size); height: var(--size); vertical-align: middle; overflow: visible;
}
.waiting { --signal: var(--warn, #9a6b12); }
.stale { --signal: var(--ink-3, #7b8585); }
.disk { fill: var(--surface-raised, #fffefa); }
.rim { fill: none; stroke: var(--signal); stroke-width: 1.25; opacity: .38; }
.linework { fill: none; stroke: var(--ink, #203c3d); stroke-width: 1.35; stroke-linecap: round; stroke-linejoin: round; }
.head { fill: color-mix(in srgb, var(--signal) 8%, var(--surface-raised, #fffefa)); }
.eyes { fill: var(--ink, #203c3d); }
.smile, .pleased { fill: none; stroke: var(--ink, #203c3d); stroke-width: 1.2; stroke-linecap: round; }
.pleased { opacity: 0; }
.antenna, .antenna-halo { fill: var(--signal); }
.antenna-halo { opacity: .14; }
.keyboard { fill: var(--signal); opacity: .18; }
.keys { fill: none; stroke: var(--signal); stroke-width: .9; stroke-linecap: round; }
.hand { fill: var(--surface-raised, #fffefa); stroke: var(--signal); stroke-width: 1.2; stroke-linecap: round; stroke-linejoin: round; }
.hand path { fill: none; }
.waiting .linework, .waiting .smile, .stale .linework, .stale .smile { stroke: var(--signal); }
.waiting .eyes, .stale .eyes { fill: var(--signal); }
.stale .antenna-halo { opacity: 0; }
.stale .rim { stroke-dasharray: .6 3.2; stroke-linecap: round; opacity: .6; }
.clock { fill: var(--surface-raised, #fffefa); stroke: var(--signal); stroke-width: 1.4; stroke-linecap: round; stroke-linejoin: round; }
.clock path { fill: none; }
.glint { fill: #c9a24a; stroke: var(--surface-raised, #fffefa); stroke-width: .6; opacity: 0; animation: event-opacity .6s ease-out both; }
@media (prefers-reduced-motion: no-preference) {
  .working .antenna { animation: antenna-work 2.2s ease-in-out infinite; animation-delay: var(--phase); }
  .working:not(.lead) .antenna { animation-duration: 5.4s; }
  .working.lead .antenna-halo { animation: halo-work 2.2s ease-in-out infinite; animation-delay: var(--phase); }
  .working.lead .look { animation: busy-glance 5.8s ease-in-out infinite; animation-delay: var(--phase); }
  .working.lead .eyes { transform-origin: 16px 14px; animation: lively-blink 5.3s linear infinite; animation-delay: var(--phase); }
  .working.lead .hand-left { transform-origin: 9px 20px; animation: typing-left 1.8s ease-in-out infinite; animation-delay: var(--phase); }
  .working.lead .hand-right { transform-origin: 23px 20px; animation: typing-right 1.8s ease-in-out infinite; animation-delay: var(--phase); }
  .celebrating .eyes { opacity: 0; }
  .celebrating .pleased { animation: pleased-beat .6s ease-out both; }
  .lead .glint { transform-origin: 6px 7px; animation-name: event-glint; }
}
@keyframes antenna-work { 0%, 100% { opacity: .55; } 50% { opacity: 1; } }
@keyframes halo-work { 0%, 100% { opacity: .06; } 50% { opacity: .32; } }
@keyframes busy-glance { 0%, 18%, 62%, 100% { transform: translateX(0); } 25%, 38% { transform: translateX(1px); } 45%, 54% { transform: translateX(-.7px); } }
@keyframes lively-blink { 0%, 91%, 98%, 100% { transform: scaleY(1); } 94% { transform: scaleY(.12); } }
@keyframes typing-left { 0%, 24%, 52%, 100% { transform: rotate(0); } 12%, 40% { transform: rotate(8deg); } }
@keyframes typing-right { 0%, 36%, 64%, 100% { transform: rotate(0); } 24%, 52% { transform: rotate(-8deg); } }
@keyframes pleased-beat { 0%, 100% { opacity: .5; } 20%, 70% { opacity: 1; } }
@keyframes event-opacity { 0%, 100% { opacity: 0; } 22%, 48% { opacity: .85; } }
@keyframes event-glint { 0% { opacity: 0; transform: scale(.6) rotate(-12deg); } 25% { opacity: 1; transform: scale(1.1) rotate(0); } 100% { opacity: 0; transform: scale(.9) rotate(12deg); } }
</style>
