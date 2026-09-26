<!-- Robot 3 · friendly midpoint: attentive eyes glance at work while the antenna softly signals activity.
     SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref, watch } from 'vue'

// Filename is the IV1 registration surface; state and real-event pulses come
// from the wrapper. The wrapper also owns accessibility and optional hovering.
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
  <svg class="robot-indicator robot-3" :class="[state, { lead }]" :style="style" :width="size" :height="size" viewBox="0 0 32 32" aria-hidden="true" focusable="false">
    <circle class="disk" cx="16" cy="16" r="14.5" />
    <circle class="rim" cx="16" cy="16" r="14" />
    <g class="linework">
      <path d="M16 9.5V6M8 14H6.5v5H8M24 14h1.5v5H24M16 23V25M12.5 25h7" />
      <rect class="head" x="8" y="9.5" width="16" height="13.5" rx="4" />
    </g>
    <circle class="antenna-halo" cx="16" cy="5.5" r="2.5" />
    <circle class="antenna" cx="16" cy="5.5" r="1.4" />
    <g class="look">
      <g class="eyes">
        <rect x="11.5" y="13.5" width="2" height="3.2" rx="1" />
        <rect x="18.5" y="13.5" width="2" height="3.2" rx="1" />
      </g>
    </g>
    <path class="smile" d="M13.5 19.3q2.5 1.7 5 0" />
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
.head { fill: color-mix(in srgb, var(--signal) 5%, var(--surface-raised, #fffefa)); }
.eyes { fill: var(--ink, #203c3d); }
.smile { fill: none; stroke: var(--ink, #203c3d); stroke-width: 1.2; stroke-linecap: round; }
.antenna, .antenna-halo { fill: var(--signal); }
.antenna-halo { opacity: .12; }
.waiting .linework, .waiting .smile, .stale .linework, .stale .smile { stroke: var(--signal); }
.waiting .eyes, .stale .eyes { fill: var(--signal); }
.stale .antenna-halo { opacity: 0; }
.stale .rim { stroke-dasharray: .6 3.2; stroke-linecap: round; opacity: .6; }
.clock { fill: var(--surface-raised, #fffefa); stroke: var(--signal); stroke-width: 1.4; stroke-linecap: round; stroke-linejoin: round; }
.clock path { fill: none; }
.glint { fill: #c9a24a; stroke: var(--surface-raised, #fffefa); stroke-width: .6; opacity: 0; animation: event-opacity .6s ease-out both; }
@media (prefers-reduced-motion: no-preference) {
  .working .antenna { animation: antenna-work 2.8s ease-in-out infinite; animation-delay: var(--phase); }
  .working:not(.lead) .antenna { animation-duration: 5.6s; }
  .working.lead .antenna-halo { animation: halo-work 2.8s ease-in-out infinite; animation-delay: var(--phase); }
  .working.lead .look { animation: attentive-glance 8.6s ease-in-out infinite; animation-delay: var(--phase); }
  .working.lead .eyes { transform-origin: 16px 15px; animation: friendly-blink 7.3s linear infinite; animation-delay: var(--phase); }
  .lead .glint { transform-origin: 6px 7px; animation-name: event-glint; }
}
@keyframes antenna-work { 0%, 100% { opacity: .55; } 50% { opacity: 1; } }
@keyframes halo-work { 0%, 100% { opacity: .06; } 50% { opacity: .28; } }
@keyframes attentive-glance { 0%, 62%, 86%, 100% { transform: translateX(0); } 69%, 78% { transform: translateX(.75px); } }
@keyframes friendly-blink { 0%, 92%, 98%, 100% { transform: scaleY(1); } 95% { transform: scaleY(.12); } }
@keyframes event-opacity { 0%, 100% { opacity: 0; } 22%, 48% { opacity: .85; } }
@keyframes event-glint { 0% { opacity: 0; transform: scale(.7); } 25% { opacity: 1; transform: scale(1); } 100% { opacity: 0; transform: scale(.9); } }
</style>
