<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import type { EtaInput } from '../../lib/eta'
import EtaCell from '../work/EtaCell.vue'

defineProps<{ eta: EtaInput | null; now: number; missing?: boolean; labelled?: boolean }>()
</script>

<template>
  <span class="session-estimate">
    <span class="report-source" :class="{ pending: !eta }" :tabindex="eta ? 0 : undefined" :aria-hidden="!eta || undefined" data-tip="ETA and progress are self-reported by this agent, not measured.">Agent report</span>
    <EtaCell align="start" :labelled="labelled" :eta="eta" :now="now" :missing="missing" />
  </span>
</template>

<style scoped>
.session-estimate { display: inline-flex; flex-direction: column; min-width: 0; max-width: 100%; }
.report-source { color: var(--ink-3); font-size: 11px; line-height: 16px; white-space: nowrap; }
/* Keep the attribution slot when the first report arrives or is cleared. */
.report-source.pending { visibility: hidden; }
.report-source:focus-visible { outline: 1px solid var(--teal-ink); outline-offset: 1px; }
</style>
