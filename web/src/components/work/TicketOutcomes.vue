<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import AppIcon from '../AppIcon.vue'
import { loadTicketOutcomes, outcomeLine, type OutcomeEvent } from '../../lib/ticketOutcomes'
import { absoluteTime, relativeTime } from '../../lib/work'

// Quiet history of reviews, fix rounds, CI, reverts, completion and release
// inclusion. An empty ticket shows nothing.
const props = defineProps<{ nodeId: string }>()

const items = ref<OutcomeEvent[]>([])
const error = ref('')
const loading = ref(false)
let generation = 0
let abort: AbortController | undefined

const rows = computed(() => items.value.map(outcomeLine))
const shown = computed(() => !loading.value && (error.value !== '' || rows.value.length > 0))

watch(() => props.nodeId, id => { void load(id) }, { immediate: true })
onBeforeUnmount(() => abort?.abort())

async function load(id: string) {
  const request = ++generation
  abort?.abort()
  abort = new AbortController()
  items.value = []
  error.value = ''
  loading.value = true
  try {
    const next = await loadTicketOutcomes(id, abort.signal)
    if (request === generation) items.value = next
  } catch (cause) {
    if (request !== generation || abort?.signal.aborted) return
    error.value = cause instanceof Error ? cause.message : 'Outcomes could not be loaded.'
  } finally {
    if (request === generation) loading.value = false
  }
}
</script>

<template>
  <section v-if="shown" class="outcomes" aria-label="Outcomes">
    <h3 class="eyebrow"><AppIcon name="history" :size="12" />Outcomes</h3>
    <p v-if="error" class="note" role="alert">{{ error }} <button type="button" class="retry" @click="load(nodeId)">Try again</button></p>
    <ul v-else>
      <li v-for="row in rows" :key="row.id">
        <span class="what" :title="row.full">{{ row.title }}</span>
        <span v-if="row.detail" class="detail" :title="row.full">{{ row.detail }}</span>
        <time class="when" :datetime="row.at" :title="absoluteTime(row.at)">{{ relativeTime(row.at) }}</time>
      </li>
    </ul>
  </section>
</template>

<style scoped>
.outcomes { min-width: 0; }
.eyebrow { display: inline-flex; align-items: center; gap: 6px; margin: 0 0 2px; }
.note { margin: 6px 0 0; font-size: 12.5px; line-height: 1.45; color: var(--ink-3); }
.retry { margin-left: 6px; padding: 0; border: 0; background: transparent; color: var(--teal-ink); font: inherit; cursor: pointer; }
.retry:focus-visible { box-shadow: var(--focus-ring); border-radius: 4px; }
ul { margin: 0; padding: 0; list-style: none; }
li {
  display: flex; flex-wrap: wrap; align-items: baseline; gap: 2px 8px; min-width: 0;
  padding: 6px 0; box-shadow: inset 0 -1px 0 var(--line);
}
li:last-child { box-shadow: none; }
.what { color: var(--ink); font-size: 13px; font-weight: 600; }
.detail {
  min-width: 0; flex: 1 1 8rem; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  color: var(--ink-2); font-size: 12.5px;
}
.when { margin-left: auto; color: var(--ink-3); font-size: 12px; white-space: nowrap; font-variant-numeric: tabular-nums; }
</style>
