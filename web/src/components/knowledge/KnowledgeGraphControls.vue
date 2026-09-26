<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import AppIcon from '../AppIcon.vue'
// Domain-specific extension to the reusable GraphControls toolbar.
defineProps<{ tickets: boolean }>()
const emit = defineEmits<{ tickets: [] }>()
</script>
<template>
  <button type="button" class="btn sm kg-tickets" :aria-pressed="tickets" aria-label="Show linked tickets" @click="emit('tickets')"><AppIcon name="ticket" :size="14" /><span>Tickets</span></button>
</template>
<style scoped>
.kg-tickets[aria-pressed="true"] { background: var(--row-selected); box-shadow: inset 0 0 0 1px var(--chip-teal-line); }
@media (max-width: 600px) { .kg-tickets { min-height: 40px; } }
</style>
