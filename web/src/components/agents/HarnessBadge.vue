<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import type { Harness } from '../../lib/agents'
import { BRAND_MARKS, harnessBrand } from './brandMarks'
const props = defineProps<{ harness: Harness | string }>()
const mark = computed(() => {
  const brand = harnessBrand(props.harness)
  return brand ? BRAND_MARKS[brand] : null
})
</script>

<template>
  <!-- The harness name is written in the execution cell. This mark only repeats it. -->
  <span class="harness-badge" :data-harness="harness" aria-hidden="true">
    <svg v-if="mark" :viewBox="mark.viewBox" width="10" height="10" focusable="false">
      <path v-for="(d, index) in mark.paths" :key="index" :d="d" />
    </svg>
    <svg v-else viewBox="0 0 16 16" width="10" height="10" focusable="false">
      <circle cx="8" cy="8" r="2.3" />
    </svg>
  </span>
</template>

<style scoped>
/* 13px plate is inside the 12–14px range the real marks need, on a theme surface at 70%. */
.harness-badge {
  position: absolute; right: 0; bottom: 0; display: grid; place-items: center; width: 13px; height: 13px;
  border-radius: 3px; color: var(--ink); background: color-mix(in srgb, var(--surface-raised) 70%, transparent);
  box-shadow: inset 0 0 0 1px color-mix(in srgb, var(--ink) 16%, transparent);
}
svg { display: block; overflow: hidden; fill: currentColor; stroke: none; }
</style>
