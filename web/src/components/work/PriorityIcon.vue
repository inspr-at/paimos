<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
withDefaults(defineProps<{ priority: string | null | undefined; size?: number }>(), { size: 14 })
</script>

<template>
  <svg v-if="priority === 'high'" class="prio high" :width="size" :height="size" viewBox="0 0 14 14" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" focusable="false"><path d="m3.2 9 3.8-3.8L10.8 9" /></svg>
  <svg v-else-if="priority === 'medium'" class="prio medium" :width="size" :height="size" viewBox="0 0 14 14" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" focusable="false"><path d="M7 3.2 10.8 7 7 10.8 3.2 7Z" /></svg>
  <svg v-else-if="priority === 'low'" class="prio low" :width="size" :height="size" viewBox="0 0 14 14" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" focusable="false"><path d="M3.2 5 7 8.8 10.8 5" /></svg>
</template>

<style scoped>
.prio.high { color: var(--pr-high); }
.prio.medium { color: var(--pr-medium); }
.prio.low { color: var(--pr-low); }
</style>
