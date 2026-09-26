<!-- Robot 2 · calm-friendly: softened linework, rare blinks and a quiet typing cursor.
     SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref, watch } from 'vue'

// IV1 discovers this presentational SFC by filename; its wrapper owns labels,
// event evidence, viewer preferences and hovering. No API or settings access.
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
// A pre-existing count on mount is history, not a new event. A keyed path
// restarts one 600 ms glint even when another event arrives during the last.
watch(() => props.pulse, (value, previous) => {
  if (Number.isFinite(value) && value > previous && props.state !== 'stale') glint.value = ++sequence
})
watch(() => props.state, state => { if (state === 'stale') glint.value = 0 })
</script>

<template>
  <svg class="robot-indicator robot-2" :class="[state, { lead }]" :style="style" :width="size" :height="size" viewBox="0 0 32 32" aria-hidden="true" focusable="false">
    <circle class="disk" cx="16" cy="16" r="14.5" />
    <circle class="rim" cx="16" cy="16" r="14" />
    <g class="linework">
      <path d="M16 10V7.5M14.5 7h3M8.5 15H7v4h1.5M23.5 15H25v4h-1.5" />
      <rect class="head" x="8.5" y="10" width="15" height="12.5" rx="3" />
      <path d="M16 22.5V25M12.5 25h7" />
      <g class="eyes">
        <path d="M12 15.5h2M18 15.5h2" />
      </g>
    </g>
    <rect class="screen" x="11.5" y="18" width="9" height="2.5" rx="1.25" />
    <path class="typing" d="M13 19.25h2.5" />
    <path class="cursor" d="M18 18.8v.9" />
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
.head { fill: var(--surface-raised, #fffefa); }
.screen { fill: var(--signal); opacity: .12; }
.typing, .cursor { fill: none; stroke: var(--signal); stroke-width: 1.2; stroke-linecap: round; }
.cursor { opacity: .8; }
.waiting .linework, .stale .linework { stroke: var(--signal); }
.stale .rim { stroke-dasharray: .6 3.2; stroke-linecap: round; opacity: .6; }
.clock { fill: var(--surface-raised, #fffefa); stroke: var(--signal); stroke-width: 1.4; stroke-linecap: round; stroke-linejoin: round; }
.clock path { fill: none; }
.glint { fill: #c9a24a; stroke: var(--surface-raised, #fffefa); stroke-width: .6; opacity: 0; animation: event-opacity .6s ease-out both; }
@media (prefers-reduced-motion: no-preference) {
  .working .cursor { animation: cursor-work 3.8s ease-in-out infinite; animation-delay: var(--phase); }
  .working:not(.lead) .cursor { animation-duration: 6.8s; }
  .working.lead .eyes { transform-origin: 16px 15.5px; animation: rare-blink 11s linear infinite; animation-delay: var(--phase); }
  .working.lead .screen { animation: screen-work 3.8s ease-in-out infinite; animation-delay: var(--phase); }
  .lead .glint { transform-origin: 6px 7px; animation-name: event-glint; }
}
@keyframes cursor-work { 0%, 100% { opacity: .35; } 40%, 60% { opacity: 1; } }
@keyframes screen-work { 0%, 100% { opacity: .08; } 50% { opacity: .2; } }
@keyframes rare-blink { 0%, 94%, 98%, 100% { transform: scaleY(1); } 96% { transform: scaleY(.15); } }
@keyframes event-opacity { 0%, 100% { opacity: 0; } 22%, 48% { opacity: .85; } }
@keyframes event-glint { 0% { opacity: 0; transform: scale(.7); } 25% { opacity: 1; transform: scale(1); } 100% { opacity: 0; transform: scale(.9); } }
</style>
