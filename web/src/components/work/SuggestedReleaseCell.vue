<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import type { ListItem } from '../../lib/api'
import { suggestedRelease, visibleQueueTiming } from '../../lib/suggestedRelease'
import { useReleases } from '../../stores/releases'
import { useWorkQueue } from '../../stores/workQueue'
import MeasuredMark from './MeasuredMark.vue'
const props = defineProps<{ row: ListItem; projectId: string; now: number; descriptionId?: string }>()
const description = computed(() => props.descriptionId ?? `suggested-${props.row.id}`)
const releases = useReleases(), queue = useWorkQueue()
const view = computed(() => suggestedRelease(props.row, releases.history?.releases ?? [], props.now, visibleQueueTiming(queue.snapshots[props.projectId], props.row.id)))
</script>
<template>
  <span class="plan-rel" :class="view.kind" :data-tip="view.tip" role="img" :aria-label="`${view.kind === 'planned' ? 'Suggested: ' : ''}${view.text}${view.kind === 'shipped' ? ', shipped' : ''}`" :aria-describedby="description"><span v-if="view.kind === 'planned'" aria-hidden="true">~</span><span class="rel-name">{{ view.text }}</span><MeasuredMark v-if="view.kind === 'shipped'" /></span>
  <span :id="description" class="sr-only">{{ view.tip }}</span>
</template>
<style scoped>
.plan-rel { display: flex; align-items: center; gap: 5px; min-width: 0; overflow: hidden; white-space: nowrap; color: var(--ink-2); font-size: 12.5px; }
.rel-name { min-width: 0; overflow: hidden; text-overflow: ellipsis; }
.planned, .empty { color: var(--ink-3); }
</style>
