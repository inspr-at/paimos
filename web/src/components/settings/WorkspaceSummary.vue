<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { getProjects } from '../../lib/api'

// The first dark-shipped UI feature. Its parent checks both the rollout flag
// and existing permissions; no data request runs while the flag is OFF.
const summary = ref<{ projects: number; open: number; working: number } | null>(null)
onMounted(async () => {
  try {
    const page = await getProjects()
    summary.value = { projects: page.items.length, open: page.items.reduce((n, p) => n + p.open, 0), working: page.items.reduce((n, p) => n + p.in_progress, 0) }
  } catch { /* omit counts when the workspace cannot be read */ }
})
</script>

<template>
  <dl v-if="summary" class="set-facts" aria-label="Workspace summary">
    <div><dt>Active projects</dt><dd>{{ summary.projects }}</dd></div>
    <div><dt>Open work</dt><dd>{{ summary.open }}</dd></div>
    <div><dt>Work in progress</dt><dd>{{ summary.working }}</dd></div>
  </dl>
</template>
