<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import type { AgentState } from '../../lib/agentSignals'
import '../../styles/agent-states.css'
withDefaults(defineProps<{ state: AgentState; size?: number }>(), { size: 14 })
</script>

<template>
  <svg class="agent-state-mark" :style="{ fill: 'var(--agent-state-color, var(--signal))' }" :class="{ clock: state === 'waiting' || state === 'awaiting' }" :data-mark="state" :width="size" :height="size" viewBox="0 0 14 14" aria-hidden="true" focusable="false">
    <template v-if="state === 'problem'"><path d="M7 1.5 13 12H1Z" /><path d="M7 5v3m0 2v.2" /></template>
    <template v-else-if="state === 'unresponsive'"><circle cx="7" cy="7" r="5.5" stroke-dasharray="2 2" /><path d="M3 7h2l1-2 2 4 1-2h2" /></template>
    <template v-else-if="state === 'throttled'"><path d="m4 1.5-3 3v5l3 3h6l3-3v-5l-3-3Z" /><path d="M5 4.5v5m4-5v5" /></template>
    <template v-else-if="state === 'done'"><circle cx="7" cy="7" r="5.5" /><path d="m4.4 7.2 1.9 1.9 3.3-3.7" /></template>
    <template v-else-if="state === 'stopped'"><rect x="2" y="2" width="10" height="10" rx="2" /><path d="M5 5h4v4H5Z" /></template>
    <template v-else><circle cx="7" cy="7" r="5.5" :stroke-dasharray="state === 'stale' ? '1 2' : undefined" />
      <path v-if="state === 'working'" d="m5.5 4 4 3-4 3Z" />
      <path v-else-if="state === 'waiting' || state === 'awaiting'" d="M7 3.5V7l2.5 1.5" />
      <path v-else d="M4.5 7h5" />
    </template>
  </svg>
</template>

<style scoped>
.agent-state-mark { flex: none; overflow: visible; color: var(--agent-state-color); stroke: currentColor; fill: var(--agent-state-color, var(--signal)); stroke-width: 1.3; stroke-linecap: round; stroke-linejoin: round; }
.agent-state-mark path + path, .agent-state-mark circle + path, .agent-state-mark rect + path { fill: none; stroke: var(--agent-state-ink); }
</style>
