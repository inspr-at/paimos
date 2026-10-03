<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { ref, watch } from 'vue'
import { APIError, getRecurrence, type NodeRecurrence } from '../../lib/api'
import { can } from '../../lib/authz'
import { useIdentityScope } from '../../lib/useIdentityScope'
import { recurrenceName, type Recurrence } from '../../lib/recurrences'
import AppIcon from '../AppIcon.vue'
const props = defineProps<{ nodeId: string; recurrence: NodeRecurrence }>()
const emit = defineEmits<{ loaded: [item: Recurrence] }>()
const scope = useIdentityScope(), reads = scope.lane(), name = ref('Recurring work'), deleted = ref(false), error = ref(false)
watch([() => props.nodeId, () => props.recurrence.id, () => props.recurrence.project_id, () => props.recurrence.retired, () => scope.owner.value], () => {
  name.value = 'Recurring work'; deleted.value = false; error.value = false
  reads.cancel()
  if (props.recurrence.retired) { deleted.value = true; return }
  const id = props.recurrence.id, projectId = props.recurrence.project_id
  void reads.run(({ after, signal }) => after(getRecurrence(id, signal), item => { if (item.id !== id || item.project_id !== projectId) { error.value = true; return }; name.value = recurrenceName(item); emit('loaded', item) }), { failed: failure => { deleted.value = failure instanceof APIError && failure.status === 404; error.value = !deleted.value } })
}, { immediate: true, flush: 'sync' })
</script>
<template>
  <p class="recurrence-provenance"><AppIcon name="repeat" :size="13" /><span>Created by Recurring work</span><span>·</span><span v-if="deleted">a deleted recurrence</span><span v-else-if="error">recurrence unavailable</span><RouterLink v-else-if="can('recurrences.manage', recurrence.project_id)" :to="{ path: `/p/${encodeURIComponent(recurrence.project_key)}/settings`, query: { recurrence: recurrence.id } }">{{ name }}</RouterLink><span v-else>{{ name }}</span><span>·</span><span class="mono">#{{ recurrence.number }}</span></p>
</template>
<style scoped>
.recurrence-provenance { display: flex; align-items: center; flex-wrap: wrap; gap: 7px; margin-top: 12px; font-size: 12.5px; color: var(--ink-3); }.recurrence-provenance a { color: var(--teal-ink); font-weight: 600; }.recurrence-provenance a:hover { text-decoration: underline; text-underline-offset: 2px; }
</style>
