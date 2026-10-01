<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import type { AgentState } from '../../../lib/agentSignals'
withDefaults(defineProps<{ state: AgentState; cx?: number; eyeY?: number; mouthY?: number; spread?: number }>(), { cx: 16, eyeY: 15, mouthY: 19, spread: 3.5 })
</script>

<template>
  <g class="state-expression" :data-expression="state" :transform="`translate(${cx} ${eyeY})`">
    <template v-if="state === 'stopped'">
      <path v-for="x in [-spread, spread]" :key="x" :d="`M${x - 1} -1l2 2m-2 0 2-2`" />
      <path :d="`M-2 ${mouthY - eyeY}h4`" />
    </template>
    <template v-else-if="state === 'done'">
      <path v-for="x in [-spread, spread]" :key="x" :d="`M${x - 1.2} .5q1.2-1.7 2.4 0`" />
      <path :d="`M-2 ${mouthY - eyeY - .4}q2 2.2 4 0`" />
    </template>
    <template v-else-if="state === 'problem'">
      <path v-for="x in [-spread, spread]" :key="x" :d="`M${x} -.4v1.5M${x - 1.2} -2l2.4 ${x < 0 ? 1 : -1}`" />
      <path class="frown" :d="`M-2.5 ${mouthY - eyeY + .6}q2.5-2.5 5 0`" />
    </template>
    <template v-else-if="state === 'unresponsive'">
      <path v-for="x in [-spread, spread]" :key="x" :d="`M${x - 1} 0h2`" />
      <path :d="`M-2.5 ${mouthY - eyeY}l1.3-.7 1.2.7 1.3-.7 1.2.7`" />
    </template>
    <template v-else-if="state === 'waiting'">
      <circle v-for="x in [-spread, spread]" :key="x" :cx="x" cy="0" r="1.15" />
      <ellipse cx="0" :cy="mouthY - eyeY" rx="1" ry="1.1" />
    </template>
    <template v-else-if="state === 'awaiting'">
      <path :d="`M${-spread} -1v2M${spread - 1} 0h2M-2 ${mouthY - eyeY}h4`" />
    </template>
    <template v-else-if="state === 'throttled'">
      <path v-for="x in [-spread, spread]" :key="x" :d="`M${x - .6} -1v2m1.2-2v2`" />
      <path :d="`M-1.5 ${mouthY - eyeY}h3`" />
    </template>
    <template v-else>
      <path v-for="x in [-spread, spread]" :key="x" :d="`M${x - 1.2} -.3q1.2 1.2 2.4 0`" />
      <path :d="`M-1 ${mouthY - eyeY}h2`" />
    </template>
  </g>
</template>

<style scoped>
.state-expression { fill: none; stroke: var(--ink); stroke-width: calc(1.2px * var(--art-stroke, 1)); stroke-linecap: round; stroke-linejoin: round; }
</style>
