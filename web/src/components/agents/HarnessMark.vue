<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import AppIcon from '../AppIcon.vue'
import { BRAND_MARKS, harnessBrand, providerBrand } from './brandMarks'

const props = withDefaults(defineProps<{ harness: string; size?: number; provider?: string }>(), { size: 18 })
const mark = computed(() => {
  const brand = providerBrand(props.provider ?? '') ?? harnessBrand(props.harness)
  return brand ? BRAND_MARKS[brand] : null
})
</script>

<template>
  <span class="mark" :style="{ width: `${size}px`, height: `${size}px` }" aria-hidden="true">
    <svg v-if="mark" :viewBox="mark.viewBox" focusable="false">
      <path v-for="(d, index) in mark.paths" :key="index" :d="d" />
    </svg>
    <AppIcon v-else name="agent" :size="size" />
  </span>
</template>

<style scoped>
.mark { display: grid; place-items: center; flex-shrink: 0; color: var(--ink); }
svg { width: 100%; height: 100%; display: block; overflow: hidden; fill: currentColor; stroke: none; }
</style>
