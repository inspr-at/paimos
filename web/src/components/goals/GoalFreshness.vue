<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// The freshness stamp (AEON-1494 decision 4), right of the quiet-projects line:
// "Live · ETAs last set 18:41" while the event stream is up, "Reconnecting ·
// last update 19:03" in warn ink after it dropped. Both labels share one grid
// cell and the hidden one keeps its size, so a reconnect never moves anything.
// Phones show it as the list's last line.
import { computed } from 'vue'
import AppIcon from '../AppIcon.vue'
import { whenText } from '../../lib/goals'
import type { GoalsLiveState } from '../../lib/goalsLive'

const props = defineProps<{ live: GoalsLiveState; etaSetAt: number | null; updatedAt: number | null; now: number }>()
const on = computed(() => props.live !== 'reconnecting')
const etaWhen = computed(() => props.etaSetAt === null ? '' : whenText(props.etaSetAt, props.now))
const lastWhen = computed(() => props.updatedAt === null ? '' : whenText(props.updatedAt, props.now))
</script>

<template>
  <p class="fresh" :data-live="on ? 1 : 0" role="status">
    <span class="f-live" :aria-hidden="on ? undefined : 'true'"><AppIcon name="clock" :size="13" /><span><b>Live</b><template v-if="etaWhen"> · ETAs last set {{ etaWhen }}</template></span></span>
    <span class="f-off" :aria-hidden="on ? 'true' : undefined"><AppIcon name="reconnect" :size="13" /><span><b>Reconnecting</b><template v-if="lastWhen"> · last update {{ lastWhen }}</template></span></span>
  </p>
</template>

<style scoped>
.fresh { display: inline-grid; margin: 0 0 0 auto; font-size: 12.5px; line-height: 18px; color: var(--ink-3); white-space: nowrap; font-variant-numeric: tabular-nums; }
.fresh > span { grid-area: 1 / 1; justify-self: end; display: inline-flex; align-items: center; gap: 6px; }
.fresh b { font-weight: 500; color: var(--ink-2); }
.fresh .f-off, .fresh .f-off b { color: var(--warn-ink); }
.fresh[data-live="1"] .f-off, .fresh[data-live="0"] .f-live { visibility: hidden; }
@media (max-width: 760px) {
  .fresh { display: grid; align-items: center; min-height: 44px; margin: 2px 0 0; font-size: 13px; }
  .fresh > span { justify-self: start; }
}
</style>
