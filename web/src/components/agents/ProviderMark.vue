<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import type { ModelProvider } from './sessionRow'
withDefaults(defineProps<{ provider: ModelProvider }>(), { provider: 'unknown' })
</script>

<template>
  <svg class="provider-mark" :data-provider="provider" viewBox="0 0 16 16" width="14" height="14" aria-hidden="true" focusable="false">
    <g v-if="provider === 'openai'" fill="currentColor">
      <circle cx="8" cy="3.6" r="1.7" /><circle cx="11.6" cy="5.6" r="1.7" /><circle cx="11.6" cy="10.4" r="1.7" />
      <circle cx="8" cy="12.4" r="1.7" /><circle cx="4.4" cy="10.4" r="1.7" /><circle cx="4.4" cy="5.6" r="1.7" />
    </g>
    <path v-else-if="provider === 'anthropic'" d="M8 1.3v3.1M8 11.6v3.1M1.3 8h3.1M11.6 8h3.1M3.2 3.2l2.2 2.2M10.6 10.6l2.2 2.2M12.8 3.2 10.6 5.4M5.4 10.6 3.2 12.8" />
    <path v-else-if="provider === 'xai'" d="M3.2 3.2 12.8 12.8M12.8 3.2 3.2 12.8" />
    <g v-else-if="provider === 'google'" fill="currentColor">
      <circle cx="5" cy="5" r="1.45" /><circle cx="11" cy="5" r="1.45" /><circle cx="5" cy="11" r="1.45" /><circle cx="11" cy="11" r="1.45" />
    </g>
    <template v-else-if="provider === 'cursor'">
      <path d="M8 1.8 13.2 4.8v6.4L8 14.2 2.8 11.2V4.8Z" />
      <path d="M8 7.8 13.2 4.8M8 7.8 2.8 4.8M8 7.8v6.4" />
    </template>
    <template v-else>
      <rect x="2.4" y="2.4" width="11.2" height="11.2" rx="2.4" />
      <circle cx="8" cy="8" r="1.15" fill="currentColor" stroke="none" />
    </template>
  </svg>
</template>

<style scoped>
.provider-mark { display: block; flex: none; overflow: visible; color: var(--ink-3); fill: none; stroke: currentColor; stroke-width: 1.4; stroke-linecap: round; stroke-linejoin: round; }
</style>
