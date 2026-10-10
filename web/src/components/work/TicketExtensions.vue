<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { onBeforeUnmount, ref, watch } from 'vue'
import { APIError } from '../../lib/api'
import { can } from '../../lib/authz'
import { getIntake, type IntakeDraft } from '../../lib/releaseData'
import ExtensionData from './ExtensionData.vue'

const props = defineProps<{ projectId: string; nodeId: string }>()
const drafts = ref<IntakeDraft[]>([])
const error = ref(false)
const nextCursor = ref<string | null>(null)
const loading = ref(false)
const paged = ref(false)
let generation = 0
async function load() {
  const mine = ++generation
  drafts.value = []; error.value = false; nextCursor.value = null; loading.value = false; paged.value = false
  if (!can('intake.read', props.projectId)) return
  try {
    const result = await getIntake(props.projectId, props.nodeId)
    if (mine === generation) {
      drafts.value = result.drafts.filter(d => d.extensions && Object.keys(d.extensions).length)
      nextCursor.value = result.nextCursor
      paged.value = !!result.nextCursor
    }
  } catch (e) {
    if (mine === generation && !(e instanceof APIError && [403, 404].includes(e.status))) error.value = true
  }
}
async function loadOlder() {
  if (!nextCursor.value || loading.value) return
  const mine = generation
  const project = props.projectId, node = props.nodeId, after = nextCursor.value
  const current = () => mine === generation && project === props.projectId && node === props.nodeId
  loading.value = true; error.value = false
  try {
    const result = await getIntake(project, node, after)
    if (current()) {
      const known = new Set(drafts.value.map(d => d.id))
      drafts.value.push(...result.drafts.filter(d => !known.has(d.id) && d.extensions && Object.keys(d.extensions).length))
      nextCursor.value = result.nextCursor
    }
  } catch (e) {
    if (current() && !(e instanceof APIError && [403, 404].includes(e.status))) error.value = true
  } finally {
    if (current()) loading.value = false
  }
}
watch([() => props.projectId, () => props.nodeId, () => can('intake.read', props.projectId)], load, { immediate: true })
onBeforeUnmount(() => { generation++ })
</script>

<template>
  <div v-if="drafts.length || error || paged" class="ticket-extensions">
    <button v-if="paged" class="history-more" type="button" :disabled="loading || !nextCursor" @click="loadOlder">Show older extension data</button>
    <p v-if="paged" class="history-status" role="status">{{ loading ? 'Loading older extension data…' : !nextCursor ? 'All extension history loaded.' : '' }}</p>
    <ExtensionData v-for="draft in drafts" :key="draft.id" :extensions="draft.extensions" />
    <p v-if="error" class="load-error" role="status">Extension data could not be loaded. <button type="button" @click="load">Retry</button></p>
  </div>
</template>

<style scoped>
.ticket-extensions { display: grid; gap: 12px; min-width: 0; }
.load-error { margin: 0; font-size: 12px; color: var(--ink-3); }
button { border: 0; padding: 2px 4px; background: transparent; color: var(--ink-2); text-decoration: underline; cursor: pointer; }
button:focus-visible { box-shadow: var(--focus-ring); border-radius: 4px; }
.history-more { justify-self: start; min-height: 44px; }
.history-status { margin: 0; min-height: 1.5em; font-size: 12px; color: var(--ink-3); }
</style>
