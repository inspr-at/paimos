<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import type { ListItem } from '../../lib/api'
import { can } from '../../lib/authz'
import { applyParentQueue, captureParentQueue, parentQueueSummary } from '../../lib/workQueue'
import { workLifecycleRequest, type WorkPreview } from '../../lib/workLifecycle'
import { toast } from '../../lib/toast'
import { useProjectLeads } from '../../stores/projectLeads'
import { useSession } from '../../stores/session'
import { useWorkQueue } from '../../stores/workQueue'
import AppIcon from '../AppIcon.vue'

// AEON-741: a parent's Queue queues its open leaves, in their order. Agents work
// on leaves; the parent follows its children. The snapshot is bounded and every
// leaf is rechecked on the server; the toast reports each skip.
const props = defineProps<{ row: ListItem; projectId: string }>()
const emit = defineEmits<{ preview: [openLeaves: number | null] }>()
const queue = useWorkQueue(), session = useSession(), leads = useProjectLeads()
const open = ref<number | null>(null), busy = ref(false)
// Offered where the lead flow is available and the open leaves have been counted.
const shown = computed(() => open.value !== null && !!leads.views[props.projectId]?.lead)
const continuation = ref<{ parent: string; snapshot: string } | null>(null)
const allowed = computed(() => session.identity?.principal.kind === 'person' && can('run.create', props.projectId))
const label = computed(() => open.value ? `Queue ${open.value}` : 'Queue')
watch(() => [props.row.id, props.row.updated_at, session.identity?.principal.id], async () => {
  const id = props.row.id, who = session.identity?.principal.id
  if (continuation.value?.parent !== id) { continuation.value = null; busy.value = false }
  try {
    const preview = await workLifecycleRequest<WorkPreview>(id)
    if (props.row.id !== id || who !== session.identity?.principal.id) return
    open.value = preview.is_leaf ? null : preview.open_leaves
  } catch { if (props.row.id === id) open.value = null }
  emit('preview', open.value)
}, { immediate: true })
async function toggle() {
  if (!shown.value || !allowed.value || busy.value || open.value === 0) return
  const id = props.row.id, key = props.row.key, revision = props.row.updated_at, project = props.projectId, who = session.identity?.principal.id
  const current = () => props.row.id === id && props.projectId === project && who === session.identity?.principal.id
  busy.value = true
  try {
    const snapshot = await captureParentQueue(id, revision, continuation.value?.parent === id ? continuation.value.snapshot : undefined)
    if (!current()) return
    const result = await applyParentQueue(snapshot.id)
    if (!current()) return
    continuation.value = result.continuation_available ? { parent: id, snapshot: result.id } : null
    void queue.load(project, true)
    toast(`${key}: ${parentQueueSummary(result)}`, { timeout: 8000 })
  } catch (e) { if (current()) toast(e instanceof Error ? e.message : 'The open work was not queued.', { tone: 'error' }) }
  finally { if (current()) busy.value = false }
}
defineExpose({ toggle })
</script>

<template>
  <button v-if="shown" type="button" class="btn sm q-action" :class="{ primary: allowed && open !== 0 }" :aria-disabled="!allowed || open === 0 || busy" aria-keyshortcuts="q"
    :aria-label="open === 0 ? `${row.key} has no open work items to queue` : `Queue the open work items below ${row.key}`" :data-tip="!allowed ? 'Permission to queue work is required' : open === 0 ? 'No open work items below' : `Queue its ${open ?? ''} open work items · q`.replace('  ', ' ')" @click.stop="toggle">
    <span class="stack"><span><AppIcon name="queue-add" :size="14" /><span class="q-word">{{ label }}</span></span><span aria-hidden="true"><AppIcon name="queue-add" :size="14" /><span class="q-word">Queue 100</span></span></span>
    <kbd class="keycap hint q-key" aria-hidden="true">Q</kbd>
  </button>
</template>

<style scoped>
.q-action { gap: 6px; }
.q-action[aria-disabled="true"] { opacity: .4; cursor: default; }
.stack { display: inline-grid; }
.stack > * { grid-area: 1 / 1; display: inline-flex; align-items: center; justify-content: center; gap: 6px; }
.stack > [aria-hidden="true"] { visibility: hidden; }
@media (max-width: 600px) { .q-word, .q-key { display: none; } }
@media (pointer: coarse) { .q-key { display: none; } }
/* A narrower ticket dock (TicketHeaderBar's panel-bar container) keeps Close
   in view with any system font, in TicketHeaderBar's order: the keycap goes
   first, then Queue goes icon-only, as on phones; its name and tooltip still
   say what it does and that q triggers it. */
@container panel-bar (max-width: 620px) { .q-key { display: none; } }
@container panel-bar (max-width: 480px) { .q-word { display: none; } }
</style>
