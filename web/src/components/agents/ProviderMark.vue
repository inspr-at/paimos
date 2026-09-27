<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import { BRAND_MARKS, providerBrand } from './brandMarks'
import type { ModelProvider } from './sessionRow'
const props = withDefaults(defineProps<{ provider: ModelProvider }>(), { provider: 'unknown' })
const mark = computed(() => {
  const brand = providerBrand(props.provider)
  return brand ? BRAND_MARKS[brand] : null
})
</script>

<template>
  <svg v-if="mark" class="provider-mark" :data-provider="provider" :viewBox="mark.viewBox" :width="mark.width" height="14" aria-hidden="true" focusable="false">
    <path v-for="(d, index) in mark.paths" :key="index" :d="d" />
  </svg>
  <svg v-else class="provider-mark unknown" data-provider="unknown" viewBox="0 0 16 16" width="14" height="14" aria-hidden="true" focusable="false">
    <rect x="2.4" y="2.4" width="11.2" height="11.2" rx="2.4" />
    <circle cx="8" cy="8" r="1.15" />
  </svg>
</template>

<style scoped>
.provider-mark { display: block; flex: none; overflow: hidden; color: var(--ink-3); fill: currentColor; stroke: none; }
.unknown { fill: none; stroke: currentColor; stroke-width: 1.4; }
.unknown circle { fill: currentColor; stroke: none; }
</style>
