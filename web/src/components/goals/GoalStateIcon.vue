<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// One 14 px family for an outcome's state (AEON-1457): Moving is the in-progress
// status icon, Queued a gold ring with its dot, Paused two bars, Done a tick.
import StatusIcon from '../work/StatusIcon.vue'
import type { GoalState } from '../../lib/goals'
defineProps<{ state: GoalState }>()
</script>

<template>
  <StatusIcon v-if="state === 'moving'" state="in_progress" :size="14" />
  <svg v-else-if="state === 'queued'" class="st st-wait" viewBox="0 0 14 14" aria-hidden="true" focusable="false"><path d="M12.36 5.36A5.6 5.6 0 1 1 8.64 1.65" /><circle class="dot" cx="10.96" cy="3.04" r="1.15" /></svg>
  <svg v-else-if="state === 'paused'" class="st st-pause" viewBox="0 0 14 14" aria-hidden="true" focusable="false"><circle cx="7" cy="7" r="5.6" /><path d="M5.5 4.9v4.2M8.5 4.9v4.2" /></svg>
  <svg v-else class="st st-done" viewBox="0 0 14 14" aria-hidden="true" focusable="false"><circle cx="7" cy="7" r="5.6" /><path d="m4.6 7.1 1.7 1.7 3.2-3.5" /></svg>
</template>

<style scoped>
.st { flex: none; width: 14px; height: 14px; fill: none; stroke: currentColor; stroke-width: 1.6; stroke-linecap: round; stroke-linejoin: round; }
.st-wait { color: var(--gold-ink); }
.st-wait .dot { fill: currentColor; stroke: none; }
.st-pause { color: var(--ink-3); }
.st-done { color: var(--st-ok); }
</style>
