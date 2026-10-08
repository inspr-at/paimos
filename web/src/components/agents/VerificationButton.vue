<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import AppIcon from '../AppIcon.vue'
defineProps<{ busy?: boolean; disabled?: boolean }>()
</script>
<template>
  <button type="button" class="btn verify-button" :disabled="disabled || busy" :aria-busy="busy || undefined">
    <span :class="{ hidden: busy }" :aria-hidden="busy || undefined">Verify again</span>
    <span class="progress-label" :class="{ hidden: !busy }" :aria-hidden="!busy || undefined"><AppIcon name="refresh" :size="14" />Verifying…</span>
  </button>
</template>
<style scoped>
.verify-button { display: inline-grid; }
.verify-button span { grid-area: 1 / 1; }
.hidden { visibility: hidden; }
.progress-label { display: inline-flex; align-items: center; gap: 6px; }
@media (prefers-reduced-motion: no-preference) { .progress-label svg { animation: verify-spin 1s linear infinite; } }
@keyframes verify-spin { to { transform: rotate(360deg); } }
</style>
