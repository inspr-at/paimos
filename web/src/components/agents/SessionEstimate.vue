<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import type { EtaInput } from '../../lib/eta'
import EtaCell from '../work/EtaCell.vue'

defineProps<{ eta: EtaInput | null; now: number; missing?: boolean; labelled?: boolean }>()
</script>

<template>
  <span class="session-estimate">
    <span v-if="eta" class="report-source" tabindex="0" data-tip="ETA and progress are self-reported by this agent, not measured.">Agent report</span>
    <EtaCell align="start" :labelled="labelled" :eta="eta" :now="now" :missing="missing" />
  </span>
</template>

<style scoped>
.session-estimate { display: inline-flex; flex-direction: column; min-width: 0; max-width: 100%; }
.report-source { color: var(--ink-3); font-size: 11px; line-height: 16px; white-space: nowrap; }
.report-source:focus-visible { outline: 1px solid var(--teal-ink); outline-offset: 1px; }
</style>
