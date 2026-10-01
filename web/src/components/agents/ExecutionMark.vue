<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import ProviderMark from './ProviderMark.vue'
import type { ModelProvider } from './sessionRow'
defineProps<{ kind: 'ai' | 'media' | 'terminal'; provider: ModelProvider }>()
</script>

<template>
  <ProviderMark v-if="kind === 'ai'" :provider="provider" class="execution-mark" />
  <svg v-else :data-run-kind="kind" width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" focusable="false">
    <template v-if="kind === 'media'"><rect x="2" y="2" width="12" height="12" rx="1.5" /><path d="M5 2v12M11 2v12M2 5h3M2 8h3M2 11h3M11 5h3M11 8h3M11 11h3" /></template>
    <template v-else><rect x="1.8" y="2.6" width="12.4" height="10.8" rx="1.8" /><path d="m4.7 6.3 2 1.7-2 1.7M8.3 10.1h3" /></template>
  </svg>
</template>

<style scoped>
svg { display: block; flex: none; color: var(--ink-3); }
/* Compensation is local to the Execution cell, preserving all other brand uses. */
.execution-mark[data-provider="xai"] { width: 20px; transform: translateX(30%); }
.execution-mark[data-provider="anthropic"] { width: 15px; height: 15px; }
.execution-mark[data-provider="google"], .execution-mark[data-provider="cursor"] { width: 13px; height: 13px; }
</style>
