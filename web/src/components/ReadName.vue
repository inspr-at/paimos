<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import AppIcon from './AppIcon.vue'

// A separate disclosure action keeps a closing choice available while its name
// is read. TooltipHost owns hover, keyboard focus, touch and source lifecycle.
defineProps<{ text: string }>()
</script>

<template>
  <button type="button" class="read-name" :aria-label="`Read full name: ${text}`" :data-tip="text" data-clip-tip @keydown.enter.stop @keydown.space.stop>
    <AppIcon name="info" :size="14" />
  </button>
</template>

<style scoped>
.read-name { display: grid; place-items: center; align-self: stretch; min-width: 28px; padding: 0 6px; border: 0; background: transparent; color: var(--ink-3); }
.read-name:focus-visible { outline: none; box-shadow: var(--focus-ring); }
@media (hover: hover) { .read-name:hover { color: var(--ink); background: var(--row-hover); } }
@media (pointer: coarse), (max-width: 720px) { .read-name { min-width: 44px; min-height: 44px; } }
</style>
