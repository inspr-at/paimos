<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// "Settings › Computers" in a routine sentence (AEON-1182): the chevron is the app's
// SVG, never a text glyph (UG1); screen readers hear "Settings, Computers".
import AppIcon from '../AppIcon.vue'

defineProps<{ page: string }>()
</script>

<template>
  <span class="place">Settings<span class="sr-only">, </span><AppIcon name="chevron-right" :size="12" class="sep" />{{ page }}</span>
</template>

<style scoped>
.place { white-space: nowrap; }
.sep { margin: 0 1px; vertical-align: -1px; }
</style>
