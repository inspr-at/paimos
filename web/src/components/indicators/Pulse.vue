<!-- Pulse · professional (0/5). A measured radial sweep and a steady centre signal, with no character. -->
<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'

const props = withDefaults(defineProps<{
  state: 'working' | 'waiting' | 'stale'; size?: number; pulse: number; seed: string; lead: boolean
}>(), { size: 26 })
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
  if (props.state === 'stale') return
  clearTimeout(clear)
  glint.value = ++serial
  clear = setTimeout(() => { glint.value = 0 }, 600)
})
watch(() => props.state, state => {
  if (state === 'stale') { clearTimeout(clear); glint.value = 0 }
})
onBeforeUnmount(() => clearTimeout(clear))
</script>

<template>
  <svg class="indicator pulse" :class="[state, { lead }]" :style="style" viewBox="0 0 32 32" :width="size" :height="size" aria-hidden="true" focusable="false">
    <circle class="disk" cx="16" cy="16" r="14" />
    <circle class="track" cx="16" cy="16" r="11" />
    <path class="ticks" d="M16 2v2M28 16h2M16 28v2M2 16h2" />
    <circle class="sweep" cx="16" cy="16" r="11" pathLength="100" />
    <circle class="centre-halo" cx="16" cy="16" r="5" />
    <circle class="centre" cx="16" cy="16" r="3" />
    <g v-if="state === 'waiting'" class="clock">
      <circle cx="26" cy="6" r="5" /><path d="M26 3.5V6l1.8 1.2" />
    </g>
    <path v-if="glint && state !== 'stale'" :key="glint" class="glint" d="m26 1 1.5 3.5L31 6l-3.5 1.5L26 11l-1.5-3.5L21 6l3.5-1.5Z" />
  </svg>
</template>

<style scoped>

.indicator { --signal: var(--accent, #2f8f86); display: block; overflow: visible; }
.indicator.waiting { --signal: var(--warn); }
.indicator.stale { --signal: var(--ink-3); }
:global(:root[data-theme="dark"] .pulse.working) { --signal: var(--accent, var(--teal)); }
@media (prefers-color-scheme: dark) {
  :global(:root:not([data-theme="light"]) .pulse.working) { --signal: var(--accent, var(--teal)); }
}
.clock { fill: var(--surface-raised); stroke: var(--signal); stroke-width: 1.4; stroke-linecap: round; stroke-linejoin: round; }
.clock path { fill: none; }
.glint { fill: #c9a24a; stroke: var(--surface-raised); stroke-width: .65; transform-origin: 26px 6px; animation: event-opacity .6s ease-out both; }
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
  .working.lead .sweep { animation: sweep 3.2s linear infinite; animation-delay: var(--lag); }
  .working:not(.lead) .sweep { animation: quiet-signal 5s ease-in-out infinite; animation-delay: var(--lag); }
}
@keyframes sweep { from { transform: rotate(-90deg); } to { transform: rotate(270deg); } }
@keyframes quiet-signal { 0%, 100% { opacity: .45; } 50% { opacity: .8; } }

</style>
