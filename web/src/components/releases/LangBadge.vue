<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import type { ReleaseLang } from '../../lib/releases'

// A text shown in the other language because the chosen one has none (AEON-323).
const props = defineProps<{ lang: ReleaseLang }>()
const tip = computed(() => props.lang === 'en' ? 'Not translated yet, shown in English' : 'Not translated yet, shown in German')
</script>

<template>
  <span class="lang-badge mono" :data-tip="tip">{{ lang.toUpperCase() }}</span>
</template>

<style scoped>
.lang-badge { display: inline-flex; align-items: center; height: 16px; padding: 0 5px; margin-left: 6px; border-radius: 5px; background: var(--surface-2); box-shadow: inset 0 0 0 1px var(--line); color: var(--ink); font-size: 9.5px; font-weight: 600; letter-spacing: .06em; line-height: 1; vertical-align: 2px; white-space: nowrap; cursor: default; }
</style>
