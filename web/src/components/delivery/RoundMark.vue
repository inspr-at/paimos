<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// One round of an item (AEON-1493): filled green passed, gold changes, red failed, hollow merge-forward, a dotted
// ring when no verdict was recorded. A mark inside each, so colour is never the only cue. Decorative: the row says it.
import type { RoundOutcome } from '../../lib/deliveryBuilding'

defineProps<{ outcome: RoundOutcome }>()
</script>

<template>
  <svg class="rd" viewBox="0 0 12 12" width="12" height="12" aria-hidden="true" focusable="false">
    <circle v-if="outcome === 'merge_forward'" class="mf" cx="6" cy="6" r="4.9" />
    <circle v-else-if="outcome === 'none'" class="none" cx="6" cy="6" r="4.9" />
    <circle v-else :class="outcome" cx="6" cy="6" r="6" />
    <path v-if="outcome === 'passed'" class="mk" d="M3.6 6.2 5.3 7.8 8.4 4.5" />
    <path v-else-if="outcome === 'failed'" class="mk" d="M4.2 4.2 7.8 7.8M7.8 4.2 4.2 7.8" />
    <path v-else-if="outcome === 'changes'" class="mk" d="M3.8 6h4.4" />
  </svg>
</template>

<style scoped>
.rd { display: block; flex: none; width: 12px; height: 12px; }
.passed { fill: var(--ok); }
.changes { fill: var(--gold); }
.failed { fill: var(--danger); }
.mf { fill: none; stroke: var(--ink-2); stroke-width: 1.5; }
.none { fill: none; stroke: var(--ink-3); stroke-width: 1.5; stroke-dasharray: 1.6 1.9; }
.mk { fill: none; stroke: var(--surface-raised); stroke-width: 1.6; stroke-linecap: round; stroke-linejoin: round; }
</style>
