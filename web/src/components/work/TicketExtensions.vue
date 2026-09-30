<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { onBeforeUnmount, ref, watch } from 'vue'
import { APIError } from '../../lib/api'
import { can } from '../../lib/authz'
import { getIntake, type IntakeDraft } from '../../lib/journey'
import ExtensionData from '../journey/ExtensionData.vue'

const props = defineProps<{ projectId: string; nodeId: string }>()
const drafts = ref<IntakeDraft[]>([])
const error = ref(false)
let generation = 0
async function load() {
  const mine = ++generation
  drafts.value = []; error.value = false
  if (!can('intake.read', props.projectId)) return
  try {
    const result = await getIntake(props.projectId, props.nodeId)
    if (mine === generation) drafts.value = result.drafts.filter(d => d.extensions && Object.keys(d.extensions).length)
  } catch (e) {
    if (mine === generation && !(e instanceof APIError && [403, 404].includes(e.status))) error.value = true
  }
}
watch([() => props.projectId, () => props.nodeId, () => can('intake.read', props.projectId)], load, { immediate: true })
onBeforeUnmount(() => { generation++ })
</script>

<template>
  <div v-if="drafts.length || error" class="ticket-extensions">
    <ExtensionData v-for="draft in drafts" :key="draft.id" :extensions="draft.extensions" />
    <p v-if="error" class="load-error" role="status">Extension data could not be loaded. <button type="button" @click="load">Retry</button></p>
  </div>
</template>

<style scoped>
.ticket-extensions { display: grid; gap: 12px; min-width: 0; }
.load-error { margin: 0; font-size: 12px; color: var(--ink-3); }
button { border: 0; padding: 2px 4px; background: transparent; color: var(--ink-2); text-decoration: underline; cursor: pointer; }
button:focus-visible { box-shadow: var(--focus-ring); border-radius: 4px; }
</style>
