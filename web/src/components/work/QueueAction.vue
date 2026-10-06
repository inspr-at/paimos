<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { APIError, type ListItem } from '../../lib/api'
import { can } from '../../lib/authz'
import { useLiveAgents } from '../../stores/liveAgents'
import { useSession } from '../../stores/session'
import { useWorkQueue } from '../../stores/workQueue'
import { queueable, readyGaps, queueIneligibleReason, type QueueWireEntry } from '../../lib/workQueue'
import { statusMeta } from '../../lib/work'
import { toast } from '../../lib/toast'
import AppIcon from '../AppIcon.vue'
import QueueReadyPanel from './QueueReadyPanel.vue'
const props = defineProps<{ row: ListItem; projectId: string; label?: boolean }>()
const queue = useWorkQueue(), session = useSession()
const readyAnchor = ref<HTMLElement | null>(null)
const button = ref<HTMLElement>()
const entry = computed(() => queueable(props.row) ? queue.entry(props.projectId, props.row.id) : null)
const allowed = computed(() => session.identity?.principal.kind === 'person' && can('run.create', props.projectId))
const eligible = computed(() => queueable(props.row) || queue.stale(props.row))
const gaps = computed(() => queue.gaps(props.row))
// AEON-741: in the ticket header, a ticket an agent already works on offers Follow instead.
const router = useRouter(), liveAgents = useLiveAgents()
const follow = computed(() => !!props.label && !entry.value && !eligible.value && statusMeta(props.row.state).key === 'progress' && !!props.row.lead_worker)
const shown = computed(() => follow.value ? 'Follow' : entry.value ? 'Queued' : 'Queue')
const title = computed(() => !allowed.value ? 'Permission to queue work is required' : entry.value ? `Queued #${entry.value.position} · click to remove · q` : !eligible.value ? queueIneligibleReason(props.row) : gaps.value.length ? 'Not ready to queue; click to fix' : queue.stale(props.row) ? 'In progress, but nobody is working on it. Queue it for the next free agent · q' : 'Queue for the next free agent · q')
watch(() => [props.row.id, props.projectId, session.identity?.principal.id], () => { readyAnchor.value = null })
// Only ambiguous blockers need an extra row read: a live blocks relation is
// named on the server even when the list has no blocker field.
watch(() => [props.row.id, props.row.updated_at, allowed.value], () => {
  if (session.identity && ((props.row.queue_stale === undefined && statusMeta(props.row.state).key === 'progress') || (allowed.value && eligible.value && readyGaps(props.row).includes('blocker')))) void queue.checkReadiness(props.row).catch(() => {})
}, { immediate: true })
function followWorker() {
  // The live read the project page already watches (AEON-184); no second session list.
  const live = liveAgents.items.find(agent => agent.ticket?.id === props.row.id && agent.session_id && !agent.stopped_at)
  if (live?.session_id) void router.push(`/agents/${live.session_id}`)
  else toast(`No live session is working on ${props.row.key} right now.`)
}
async function toggle() {
  if (follow.value) { followWorker(); return }
  if (!allowed.value || queue.busy || (!entry.value && !eligible.value)) return
  const row = props.row, id = row.id, revision = row.updated_at, project = props.projectId, actor = session.identity?.principal.id
  const current = () => props.row.id === id && props.projectId === project && session.identity?.principal.id === actor && allowed.value
  const queued = entry.value
  try {
    if (!queued && gaps.value.length) {
      const ready = await queue.checkReadiness(row)
      if (!current() || props.row.updated_at !== revision) return
      if (!ready.ready) { readyAnchor.value = button.value ?? null; return }
    }
    if (queued) {
      await queue.remove(project, id)
      if (current()) toast(`${row.key} left the queue`)
    } else {
      const result = await queue.add(project, id)
      await nextTick()
      if (!result || !current()) return
      await showAdded(row, project, actor, result)
    }
  } catch (e) {
    if (!current()) return
    if (e instanceof APIError && e.status === 422 && e.body.code === 'queue_not_ready') readyAnchor.value = button.value ?? null
    else toast(e instanceof Error ? e.message : 'The queue change was not saved.', { tone: 'error' })
  }
}
async function showAdded(row: ListItem, project: string, actor: string | undefined, result: QueueWireEntry) {
  await nextTick()
  const id = row.id
  const current = () => props.row.id === id && props.projectId === project && session.identity?.principal.id === actor && allowed.value
  if (result.node_id !== id || !current()) return
  const queuedRevision = result.undo?.revision ?? props.row.updated_at, token = result.undo
  toast(`${row.key} queued`, token ? { action: { label: 'Undo', run: () => {
    if (!current() || props.row.updated_at !== queuedRevision || queue.entry(project, id)?.run_id !== token.run_id) {
      toast('The ticket changed; this queue addition can no longer be undone.', { tone: 'error' }); return
    }
    void queue.undoAdd(project, id, token).then(result => {
      if (result?.removed && current()) toast(`${row.key} returned to In progress`)
    }).catch(e => { if (current()) toast(e instanceof Error ? e.message : 'The queue addition could not be undone.', { tone: 'error' }) })
  } } } : {})
}
defineExpose({ toggle })
</script>
<template>
  <button v-if="row.is_leaf !== false" ref="button" type="button" class="q-btn" :class="[label ? 'btn sm q-action' : 'icon-btn sm flat', { unready: !entry && eligible && gaps.length, primary: label && !entry && !follow && eligible && allowed }]" :aria-pressed="follow ? undefined : !!entry" :aria-disabled="!follow && (!allowed || (!entry && !eligible))" :disabled="queue.busy" :aria-haspopup="!entry && !follow && gaps.length ? 'dialog' : undefined" :aria-label="follow ? `Follow the agent working on ${row.key}` : `${entry ? `Remove ${row.key} from the queue` : `Queue ${row.key}`}${!allowed || (!entry && !eligible) || (!entry && gaps.length) ? `: ${title}` : ''}`" :data-tip="follow ? 'Follow the worker on this ticket' : title" :aria-keyshortcuts="follow ? undefined : 'q'" @click.stop="toggle">
    <template v-if="!label">
      <AppIcon v-if="!entry" name="queue-add" :size="13" />
      <template v-else><AppIcon name="queue-on" class="g-on" :size="13" /><AppIcon name="queue-off" class="g-remove" :size="13" /></template>
    </template>
    <!-- The slot keeps the width of its widest label through Queue, Queued and Follow. -->
    <span v-else class="stack">
      <span class="q-label"><AppIcon v-if="follow" name="agent" :size="14" /><AppIcon v-else-if="!entry" name="queue-add" :size="14" /><template v-else><AppIcon name="queue-on" class="g-on" :size="14" /><AppIcon name="queue-off" class="g-remove" :size="14" /></template><span class="q-word">{{ shown }}</span></span>
      <span aria-hidden="true"><AppIcon name="queue-add" :size="14" /><span class="q-word">Queued</span></span>
      <span aria-hidden="true"><AppIcon name="agent" :size="14" /><span class="q-word">Follow</span></span>
    </span>
    <kbd v-if="label" class="keycap hint q-key" :class="{ off: follow }" aria-hidden="true">Q</kbd>
  </button>
  <QueueReadyPanel v-if="readyAnchor" :row="row" :project-id="projectId" :anchor="readyAnchor" @queued="result => showAdded(row, projectId, session.identity?.principal.id, result)" @close="restore => { readyAnchor = null; if (restore) button?.focus() }" />
</template>
<style scoped>
.q-btn { position: relative; }
.q-btn .g-remove { display: none; }
.q-btn[aria-pressed="true"] { background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); color: var(--teal-ink); }
@media (hover: hover) { .q-btn[aria-pressed="true"]:hover .g-on { display: none; } .q-btn[aria-pressed="true"]:hover .g-remove { display: block; } }
.q-btn.unready::after { content: ''; position: absolute; top: 3px; right: 3px; width: 6px; height: 6px; border-radius: 50%; background: var(--gold); box-shadow: 0 0 0 1.5px var(--surface-raised); }
.q-btn[aria-disabled="true"] { opacity: .4; cursor: default; }
.q-action { gap: 6px; }
.stack { display: inline-grid; }
.stack > * { grid-area: 1 / 1; display: inline-flex; align-items: center; justify-content: center; gap: 6px; }
.stack > [aria-hidden="true"] { visibility: hidden; }
.q-key.off { visibility: hidden; }
@media (max-width: 600px) { .q-word, .q-key { display: none; } }
@media (pointer: coarse) { .q-key { display: none; } }
</style>
