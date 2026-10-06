<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// The dial's robot (AgentsWorking.vue): awake while its lead is busy, asleep otherwise.
defineProps<{ busy: boolean; small?: boolean }>()
</script>

<template>
  <svg class="bot" :class="{ sm: small, rest: !busy }" viewBox="0 0 24 24" aria-hidden="true" focusable="false"><g class="bob">
    <path class="stalk" d="M12 7.6V3.9" /><circle class="tip" cx="12" cy="2.5" r="1.65" />
    <rect class="ear" x="2.3" y="11.7" width="2.3" height="4.8" rx="1.15" /><rect class="ear" x="19.4" y="11.7" width="2.3" height="4.8" rx="1.15" />
    <rect class="head" x="4.4" y="7.6" width="15.2" height="12.6" rx="4.8" />
    <g v-if="busy"><g class="blink"><rect class="eye" x="8.2" y="11.9" width="2.4" height="3.3" rx="1.2" /><rect class="eye" x="13.4" y="11.9" width="2.4" height="3.3" rx="1.2" /></g><path class="smile" d="M10.5 17.1q1.5 1 3 0" /></g>
    <path v-else class="zz" d="M8.2 13.2q1.2 1.2 2.4 0M13.4 13.2q1.2 1.2 2.4 0M11 17.1h2" />
  </g></svg>
</template>

<style scoped>
.bot { width: 24px; height: 24px; overflow: visible; --rim: var(--teal); --face: color-mix(in srgb, var(--teal) 9%, var(--surface-raised)); }
.bot.sm { width: 18px; height: 18px; }
.bot.rest { --rim: var(--ink-3); --face: var(--surface-raised); }
.stalk { fill: none; stroke: var(--rim); stroke-width: 1.5; stroke-linecap: round; }
.tip, .ear { fill: var(--rim); }
.ear { opacity: .75; }
.head { fill: var(--face); stroke: var(--rim); stroke-width: 1.5; }
.eye { fill: var(--ink); }
.smile, .zz { fill: none; stroke: var(--ink); stroke-width: 1.2; stroke-linecap: round; }
.zz { stroke: var(--ink-2); }
@media (prefers-reduced-motion: no-preference) {
  .bot:not(.rest) .bob { animation: lead-bob 2.8s ease-in-out infinite; }
  .bot:not(.rest) .blink { transform-box: fill-box; transform-origin: center; animation: lead-blink 4.6s linear infinite; }
}
@keyframes lead-bob { 0%, 100% { transform: translateY(.5px); } 50% { transform: translateY(-1.4px); } }
@keyframes lead-blink { 0%, 90%, 100% { transform: scaleY(1); } 93% { transform: scaleY(.12); } 96% { transform: scaleY(1); } }
</style>
