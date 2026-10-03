<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { useProjectHeader } from '../../lib/useProjectHeader'
import AppIcon from '../AppIcon.vue'
const { roomyHeader, setHeaderDensity } = useProjectHeader()
</script>
<template>
  <div class="header-choice">
    <p class="eyebrow">Project header</p>
    <div class="seg" role="radiogroup" aria-label="Project header density">
      <button type="button" role="radio" :aria-checked="roomyHeader === 'comfortable'" @click="setHeaderDensity('comfortable')"><AppIcon name="rows-comfortable" :size="14" />Comfortable</button>
      <button type="button" role="radio" :aria-checked="roomyHeader === 'compact'" @click="setHeaderDensity('compact')"><AppIcon name="rows-compact" :size="14" />Compact</button>
    </div>
  </div>
</template>
<style scoped>
.seg { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); }
.seg button { height: 30px; }
@media (max-width: 900px), (pointer: coarse) { .seg button { min-height: 44px; } }
</style>
