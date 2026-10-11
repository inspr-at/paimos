<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// "Settings › Computers" in a routine sentence (AEON-1182): the chevron is the app's
// SVG, never a text glyph (UG1). The drawn path is hidden from screen readers, which
// hear "Settings, Computers" from one hidden span instead.
import AppIcon from '../AppIcon.vue'

defineProps<{ page: string }>()
</script>

<template>
  <span class="settings-place"><span class="drawn" aria-hidden="true">Settings<AppIcon name="chevron-right" :size="12" class="sep" />{{ page }}</span><span class="sr-only">Settings, {{ page }}</span></span>
</template>

<style scoped>
.drawn { white-space: nowrap; }
.sep { display: inline-block; margin: 0 1px; vertical-align: -1px; }
</style>
