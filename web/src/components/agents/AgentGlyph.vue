<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import LiveBot from '../projects/LiveBot.vue'
withDefaults(defineProps<{ id: string; size?: number; index?: number; lead?: boolean }>(), { size: 34, index: 0, lead: false })
</script>

<template>
  <span class="agent-glyph"><LiveBot :id="id" :size="size" :index="index" :lead="lead" /></span>
</template>

<style scoped>
.agent-glyph { display: inline-grid; place-items: center; flex: none; width: fit-content; height: fit-content; }
</style>
