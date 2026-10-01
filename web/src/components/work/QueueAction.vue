<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import type { ListItem } from '../../lib/api'
import { can } from '../../lib/authz'
import { useSession } from '../../stores/session'
import { useWorkQueue } from '../../stores/workQueue'
import { queueable, readyGaps } from '../../lib/workQueue'
import { toast } from '../../lib/toast'
import AppIcon from '../AppIcon.vue'
import QueueReadyPanel from './QueueReadyPanel.vue'
const props = defineProps<{ row: ListItem; projectId: string; label?: boolean }>()
const queue = useWorkQueue(), session = useSession()
const readyAnchor = ref<HTMLElement | null>(null)
const button = ref<HTMLElement>()
const entry = computed(() => queue.entry(props.projectId, props.row.id))
const allowed = computed(() => session.identity?.principal.kind === 'person' && can('run.create', props.projectId))
const eligible = computed(() => queueable(props.row))
const gaps = computed(() => readyGaps(props.row))
const title = computed(() => !allowed.value ? 'Permission to queue work is required' : entry.value ? `Queued #${entry.value.position} · click to remove · q` : !eligible.value ? `${props.row.state}: the queue takes New, Open, Backlog or Blocked` : gaps.value.length ? 'Not ready to queue; click to fix' : 'Queue for the next free agent · q')
watch(() => props.row.id, () => { readyAnchor.value = null })
async function toggle() {
  if (!allowed.value || queue.busy || (!entry.value && !eligible.value)) return
  if (!entry.value && gaps.value.length) { readyAnchor.value = button.value ?? null; return }
  try {
    if (entry.value) { await queue.remove(props.projectId, props.row.id); toast(`${props.row.key} left the queue`) }
    else { await queue.add(props.projectId, props.row.id); toast(`${props.row.key} queued`) }
  } catch (e) { toast(e instanceof Error ? e.message : 'The queue change was not saved.', { tone: 'error' }) }
}
defineExpose({ toggle })
</script>
<template>
  <button ref="button" type="button" class="q-btn" :class="[label ? 'btn sm q-action' : 'icon-btn sm flat', { unready: !entry && eligible && gaps.length }]" :aria-pressed="!!entry" :aria-disabled="!allowed || (!entry && !eligible)" :disabled="queue.busy" :aria-label="`${entry ? `Remove ${row.key} from the queue` : `Queue ${row.key}`}${!allowed || (!entry && !eligible) ? `: ${title}` : ''}`" :data-tip="title" aria-keyshortcuts="q" @click.stop="toggle">
    <AppIcon v-if="!entry" name="queue-add" :size="label ? 14 : 13" />
    <template v-else><AppIcon name="queue-on" class="g-on" :size="label ? 14 : 13" /><AppIcon name="queue-off" class="g-remove" :size="label ? 14 : 13" /></template>
    <span v-if="label" class="q-label">{{ entry ? `Queued #${entry.position}` : 'Queue' }}</span>
  </button>
  <QueueReadyPanel v-if="readyAnchor" :row="row" :project-id="projectId" :anchor="readyAnchor" @close="restore => { readyAnchor = null; if (restore) button?.focus() }" />
</template>
<style scoped>
.q-btn { position: relative; }
.q-btn .g-remove { display: none; }
.q-btn[aria-pressed="true"] { background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); color: var(--teal-ink); }
@media (hover: hover) { .q-btn[aria-pressed="true"]:hover .g-on { display: none; } .q-btn[aria-pressed="true"]:hover .g-remove { display: block; } }
.q-btn.unready::after { content: ''; position: absolute; top: 3px; right: 3px; width: 6px; height: 6px; border-radius: 50%; background: var(--gold); box-shadow: 0 0 0 1.5px var(--surface-raised); }
.q-btn[aria-disabled="true"] { opacity: .4; cursor: default; }
.q-action { gap: 6px; }
@media (max-width: 600px) { .q-label { display: none; } }
</style>
