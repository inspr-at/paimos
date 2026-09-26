<!-- Robot 5 · playful end (5/5). Original LA1 round face; curious sideways glances, blinks and an antenna wink. -->
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
  <svg class="indicator robot5" :class="[state, { lead }]" :style="style" viewBox="0 0 32 32" :width="size" :height="size" aria-hidden="true" focusable="false">
    <circle class="halo" cx="16" cy="16" r="15" />
    <circle class="disk" cx="16" cy="16" r="14.5" />
    <g transform="translate(0 -1) scale(1.333333)">
      <path class="stalk" d="M12 7.6V3.9" />
      <circle class="tip-glow" cx="12" cy="2.5" r="1.7" />
      <circle class="tip" cx="12" cy="2.5" r="1.65" />
      <rect class="ear" x="2.3" y="11.7" width="2.3" height="4.8" rx="1.15" />
      <rect class="ear" x="19.4" y="11.7" width="2.3" height="4.8" rx="1.15" />
      <rect class="head" x="4.4" y="7.6" width="15.2" height="12.6" rx="4.8" />
      <g class="look"><g class="blink">
        <rect class="eye" x="8.2" y="11.9" width="2.4" height="3.3" rx="1.2" />
        <rect class="eye" x="13.4" y="11.9" width="2.4" height="3.3" rx="1.2" />
      </g></g>
      <path class="happy" d="M8.1 14.2q1.3-1.9 2.6 0M13.3 14.2q1.3-1.9 2.6 0" />
      <circle class="cheek" cx="7.3" cy="16.6" r="1.05" />
      <circle class="cheek" cx="16.7" cy="16.6" r="1.05" />
      <path class="smile" d="M10.5 17.1q1.5 1 3 0" />
    </g>
    <path v-if="lead && state === 'working'" class="spark" d="m3 4 .8 2.2L6 7l-2.2.8L3 10l-.8-2.2L0 7l2.2-.8Z" />
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
:global(:root[data-theme="dark"] .robot5.working) { --signal: var(--accent, var(--teal)); }
@media (prefers-color-scheme: dark) {
  :global(:root:not([data-theme="light"]) .robot5.working) { --signal: var(--accent, var(--teal)); }
}
.clock { fill: var(--surface-raised); stroke: var(--signal); stroke-width: 1.4; stroke-linecap: round; stroke-linejoin: round; }
.clock path { fill: none; }
.glint { fill: #c9a24a; stroke: var(--surface-raised); stroke-width: .65; transform-origin: 26px 6px; animation: event-opacity .6s ease-out both; }
@keyframes event-opacity { 0%, 100% { opacity: 0; } 25%, 50% { opacity: 1; } }
@keyframes event-glint { 0% { opacity: 0; transform: scale(.65); } 25% { opacity: 1; transform: scale(1); } 100% { opacity: 0; transform: scale(.85); } }
@media (prefers-reduced-motion: no-preference) { .glint { animation-name: event-glint; } }

.robot5 { --face: var(--surface-raised); --rim: color-mix(in srgb, var(--signal) 65%, var(--ink)); --eye: var(--ink); }
.halo { fill: var(--signal); opacity: 0; transform-origin: 16px 16px; }
.disk { fill: color-mix(in srgb, var(--signal) 10%, var(--surface-raised)); stroke: color-mix(in srgb, var(--signal) 24%, var(--surface-raised)); stroke-width: 1; }
.stalk { fill: none; stroke: var(--rim); stroke-width: 1.5; stroke-linecap: round; }
.tip, .tip-glow { fill: var(--signal); }
.tip-glow { opacity: 0; transform-box: fill-box; transform-origin: center; }
.ear { fill: var(--rim); opacity: .75; }
.head { fill: var(--face); stroke: var(--rim); stroke-width: 1.3; }
.eye { fill: var(--eye); }
.cheek { fill: color-mix(in srgb, var(--warn) 28%, transparent); }
.smile, .happy { fill: none; stroke: var(--eye); stroke-width: 1.2; stroke-linecap: round; }
.happy { opacity: 0; }
.spark { fill: var(--signal); opacity: 0; transform-origin: 3px 7px; }
.waiting { --eye: var(--signal); }
.stale { --rim: var(--ink-3); --eye: var(--ink-3); }
.stale .cheek { opacity: 0; }
@media (prefers-reduced-motion: no-preference) {
  .working .blink { transform-box: fill-box; transform-origin: center; animation: bot-blink 4.6s linear infinite; animation-delay: var(--lag); }
  .working:not(.lead) .blink { animation-duration: 7.8s; }
  .working.lead .look { animation: bot-look 7.4s ease-in-out infinite; animation-delay: var(--lag); }
  .working.lead .tip-glow { animation: bot-ping 3.2s ease-out infinite; animation-delay: var(--lag); }
  .working.lead .halo { animation: bot-breathe 3.2s ease-in-out infinite; animation-delay: var(--lag); }
  .working.lead .spark { animation: bot-spark 6.4s ease-out infinite; animation-delay: var(--lag); }
}
.robot5.working:hover .eye { opacity: 0; }
.robot5.working:hover .happy { opacity: 1; }
@keyframes bot-blink { 0%, 90%, 100% { transform: scaleY(1); } 93% { transform: scaleY(.12); } 96% { transform: scaleY(1); } }
@keyframes bot-look { 0%, 34%, 100% { transform: translateX(0); } 40%, 56% { transform: translateX(.8px); } 62%, 80% { transform: translateX(-.6px); } 86% { transform: translateX(0); } }
@keyframes bot-ping { 0% { transform: scale(1); opacity: .4; } 65%, 100% { transform: scale(2); opacity: 0; } }
@keyframes bot-breathe { 0%, 100% { transform: scale(.96); opacity: .05; } 50% { transform: scale(1.08); opacity: .18; } }
@keyframes bot-spark { 0%, 24%, 100% { transform: scale(.4) rotate(0deg); opacity: 0; } 10% { transform: scale(1) rotate(20deg); opacity: .7; } }

</style>
