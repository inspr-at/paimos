<!-- Robot 1 · calm end (1/5). Original LA2 line robot; an even activity arc does the work. -->
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
  <svg class="indicator robot1" :class="[state, { lead }]" :style="style" viewBox="0 0 32 32" :width="size" :height="size" aria-hidden="true" focusable="false">
    <circle class="disk" cx="16" cy="16" r="14.5" />
    <circle class="ring-track" cx="16" cy="16" r="14" />
    <circle v-if="state !== 'stale'" class="ring-sweep" cx="16" cy="16" r="14" pathLength="100" />
    <g class="robot">
      <path d="M16 10V7.5M14 7.5h4M8.5 15H7v5h1.5M23.5 15H25v5h-1.5" />
      <rect x="9" y="10.5" width="14" height="12" rx="1.5" />
      <path d="M12.5 15.5h2M17.5 15.5h2M13 19h6M12 25h8M16 22.5V25" />
    </g>
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
:global(:root[data-theme="dark"] .robot1.working) { --signal: var(--accent, var(--teal)); }
@media (prefers-color-scheme: dark) {
  :global(:root:not([data-theme="light"]) .robot1.working) { --signal: var(--accent, var(--teal)); }
}
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
.waiting .robot { stroke: var(--signal); }
.stale .ring-track { stroke-dasharray: .6 3.2; stroke-linecap: round; opacity: .8; }
.stale .robot { stroke: var(--ink-3); }
@media (prefers-reduced-motion: no-preference) {
  .working.lead .ring-sweep { animation: activity-sweep 2.8s linear infinite; animation-delay: var(--lag); }
  .working:not(.lead) .ring-sweep { animation: quiet-signal 4.8s ease-in-out infinite; animation-delay: var(--lag); }
}
@keyframes activity-sweep { from { transform: rotate(-85deg); } to { transform: rotate(275deg); } }
@keyframes quiet-signal { 0%, 100% { opacity: .5; } 50% { opacity: .85; } }

</style>
