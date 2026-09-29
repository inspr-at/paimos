<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import { statusMeta } from '../../lib/work'
const props = withDefaults(defineProps<{ state: string; size?: number }>(), { size: 14 })
const key = computed(() => statusMeta(props.state).key)
</script>

<template>
  <svg class="status-icon" :class="`st-${key}`" :width="size" :height="size" viewBox="0 0 14 14" fill="none" aria-hidden="true" focusable="false">
    <circle v-if="key === 'new' || key === 'open'" cx="7" cy="7" r="5.1" stroke="var(--st-new)" stroke-width="1.8" />
    <circle v-else-if="key === 'backlog' || key === 'blocked'" cx="7" cy="7" r="5.1" stroke="var(--st-backlog)" stroke-width="1.6" stroke-dasharray="2.1 1.9" />
    <template v-else-if="key === 'progress'"><circle cx="7" cy="7" r="5.1" stroke="var(--st-progress)" stroke-width="1.8" /><path d="M7 3.6a3.4 3.4 0 0 1 0 6.8Z" fill="var(--st-progress)" /></template>
    <circle v-else-if="key === 'qa'" cx="7" cy="7" r="5.1" fill="var(--st-qa-fill)" stroke="var(--st-qa-ring)" stroke-width="1.8" />
    <template v-else-if="key === 'done'"><circle cx="7" cy="7" r="6" fill="var(--st-ok)" /><path d="m4.4 7.2 1.8 1.8 3.4-3.7" stroke="var(--surface)" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" /></template>
    <template v-else-if="key === 'delivered'"><circle cx="7" cy="7" r="5.1" stroke="var(--st-ok)" stroke-width="1.8" /><circle cx="7" cy="7" r="2.2" fill="var(--st-ok)" /></template>
    <circle v-else-if="key === 'accepted'" cx="7" cy="7" r="5.1" stroke="var(--st-ok)" stroke-width="1.8" />
    <template v-else-if="key === 'cancelled'"><circle cx="7" cy="7" r="5.1" stroke="var(--st-closed)" stroke-width="1.6" /><path d="m3.6 10.4 6.8-6.8" stroke="var(--st-closed)" stroke-width="1.6" stroke-linecap="round" /></template>
    <rect v-else-if="key === 'archived'" x="2.2" y="2.2" width="9.6" height="9.6" rx="2.2" stroke="var(--st-closed)" stroke-width="1.6" />
    <template v-else><circle cx="7" cy="7" r="5.1" stroke="var(--st-backlog)" stroke-width="1.6" /><circle cx="7" cy="7" r="1.4" fill="var(--st-backlog)" /></template>
  </svg>
</template>

<style scoped>
.st-qa { filter: drop-shadow(0 0 3px rgba(164, 229, 223, .7)); }
</style>
