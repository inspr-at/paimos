<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import { effortLevel } from '../../lib/planning'

const props = withDefaults(defineProps<{ level: number | null; enabled?: boolean; planned?: boolean }>(), { enabled: true, planned: false })
const level = computed(() => effortLevel(props.level))
</script>

<template>
  <svg v-if="enabled !== false && level !== null" class="effort" :class="{ planned }" :style="{ '--eff': level ? `var(--eff-${level})` : undefined }" width="6.5" height="12" viewBox="0 0 6.5 12" aria-hidden="true" focusable="false">
    <rect v-for="i in 5" :key="i" x=".25" :y="10.25 - (i - 1) * 2.5" width="6" height="1.5" rx=".4" :class="{ on: i <= level }" />
  </svg>
</template>

<style scoped>
.effort { flex: none; display: block; margin-left: -2px; overflow: visible; }
rect { fill: none; stroke: var(--eff-line); stroke-width: .5; }
rect.on { fill: var(--eff); stroke: var(--eff); }
.planned rect.on { fill: var(--eff-plan); stroke: var(--eff-plan); }
</style>
