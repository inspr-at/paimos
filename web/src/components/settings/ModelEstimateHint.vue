<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import { modelEstimateHint, type ModelEstimateHistory } from '../../lib/modelEstimates'
const props = defineProps<{ history?: ModelEstimateHistory | null; kindLabel: string; bucket: 'normal' | 'complex' }>()
const hint = computed(() => modelEstimateHint(props.history, props.kindLabel, props.bucket))
</script>
<template>
  <!-- Reserved below the picker controls; empty/loading history never moves them. -->
  <span class="model-estimate-hint" :title="hint ? history?.basis : undefined" aria-live="polite">{{ hint }}</span>
</template>
<style scoped>
.model-estimate-hint { display: block; height: 1.5em; line-height: 1.5; margin: 6px 0 0; overflow: hidden; white-space: nowrap; text-overflow: ellipsis; font-size: 12px; color: var(--ink-3); }
</style>
