<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import { formatEta, type EtaInput, type EtaMode } from '../../lib/eta'
import { usePreference } from '../../lib/preferences'

const props = defineProps<{ eta: EtaInput | null; now: number }>()
const pref = usePreference<{ mode?: EtaMode }>('eta-display')
const mode = computed<EtaMode>(() => pref.value.value?.mode === 'clock' || pref.value.value?.mode === 'both' ? pref.value.value.mode : 'relative')
const zone = Intl.DateTimeFormat().resolvedOptions().timeZone
const lines = computed(() => formatEta(props.eta, mode.value, props.now, zone))
</script>

<template>
  <span v-if="lines" class="eta-cell" :class="{ stale: lines.stale }" :data-tip="lines.tip" :aria-label="lines.tip">
    <span v-if="lines.ready" class="when">
      <span class="shown">{{ lines.ready }}</span>
      <span v-if="lines.readyHover" class="hover" aria-hidden="true">{{ lines.readyHover }}</span>
    </span>
    <span v-if="lines.live" class="when">
      <span class="shown">{{ lines.live }}</span>
      <span v-if="lines.liveHover" class="hover" aria-hidden="true">{{ lines.liveHover }}</span>
    </span>
    <span v-if="lines.progress" class="pct">{{ lines.progress }}</span>
  </span>
</template>

<style scoped>
.eta-cell { display: inline-flex; align-items: baseline; gap: 6px; min-width: 0; max-width: 100%; color: var(--ink); font-variant-numeric: tabular-nums; }
.eta-cell.stale { color: var(--ink-3); }
.when { display: inline-grid; min-width: 0; }
.when > span { grid-area: 1 / 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.hover { visibility: hidden; }
.eta-cell:hover .when:has(.hover) .shown { visibility: hidden; }
.eta-cell:hover .hover { visibility: visible; }
.pct { color: var(--ink-2); font: 500 12px/1 var(--mono); }
.eta-cell.stale .pct { color: var(--ink-3); }
</style>
