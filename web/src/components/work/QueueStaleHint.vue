<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { watch } from 'vue'
import type { ListItem } from '../../lib/api'
import { statusMeta } from '../../lib/work'
import { useWorkQueue } from '../../stores/workQueue'
const props = defineProps<{ row: ListItem }>()
const queue = useWorkQueue()
watch(() => [props.row.id, props.row.updated_at], () => {
  if (props.row.queue_stale === undefined && statusMeta(props.row.state).key === 'progress') void queue.checkReadiness(props.row).catch(() => {})
}, { immediate: true })
</script>
<template>
  <!-- Reserve the hint's slot even while readiness arrives or status changes. -->
  <span class="stale-hint" :class="{ shown: queue.stale(row) }" :aria-hidden="!queue.stale(row)" data-tip="In progress, but nobody is working on it. Queue it for the next free agent.">stale</span>
</template>
<style scoped>
.stale-hint { visibility: hidden; flex: none; color: var(--ink-3); font-size: 10px; margin-left: 5px; }
.stale-hint.shown { visibility: visible; }
</style>
