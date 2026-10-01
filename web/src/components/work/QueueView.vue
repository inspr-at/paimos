<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, ref } from 'vue'
import { can } from '../../lib/authz'
import { absoluteTime } from '../../lib/work'
import { queueHours, type QueuedTicket } from '../../lib/workQueue'
import { toast } from '../../lib/toast'
import { useSession } from '../../stores/session'
import { useWorkQueue } from '../../stores/workQueue'
import AppIcon from '../AppIcon.vue'
import KeyCap from '../KeyCap.vue'
import FloatingPanel from './FloatingPanel.vue'
import PriorityIcon from './PriorityIcon.vue'
import QueueModel from './QueueModel.vue'
const props = defineProps<{ projectId: string; anchor: HTMLElement; filtered?: boolean }>()
const emit = defineEmits<{ close: [restore: boolean]; filter: []; open: [key: string] }>()
const queue = useWorkQueue(), session = useSession(), list = ref<HTMLElement>()
const snapshot = computed(() => queue.snapshots[props.projectId])
const allowed = computed(() => session.identity?.principal.kind === 'person' && can('run.create', props.projectId))
const preview = ref<string[] | null>(null), dragging = ref('')
const shared = computed(() => {
  const items = snapshot.value?.items.filter(item => !item.target_agent_id) ?? []
  return preview.value ? [...items].sort((a, b) => preview.value!.indexOf(a.ticket_id) - preview.value!.indexOf(b.ticket_id)) : items
})
const targeted = computed(() => snapshot.value?.items.filter(item => item.target_agent_id) ?? [])
const hours = computed(() => queueHours(snapshot.value))
async function change(action: () => Promise<unknown>, ticket?: string) {
  if (!allowed.value || queue.busy) return
  const active = document.activeElement as HTMLElement | null, selector = active?.dataset.action
  try {
    await action(); await nextTick()
    if (ticket) {
      const row = list.value?.querySelector<HTMLElement>(`[data-ticket="${CSS.escape(ticket)}"]`)
      ;(row?.querySelector<HTMLElement>(`[data-action="${selector ?? 'open'}"]:not(:disabled)`) ?? row?.querySelector<HTMLElement>('[data-action="open"]'))?.focus({ preventScroll: true })
    }
  } catch (e) { toast(e instanceof Error ? e.message : 'The queue change was not saved.', { tone: 'error' }) }
}
function move(item: QueuedTicket, direction: -1 | 1 | 'top') { void change(() => queue.move(props.projectId, item.ticket_id, direction), item.ticket_id) }
function keys(event: KeyboardEvent, item: QueuedTicket) {
  if (!event.altKey || !['ArrowUp', 'ArrowDown'].includes(event.key) || item.target_agent_id) return
  event.preventDefault(); event.stopPropagation(); move(item, event.key === 'ArrowUp' ? -1 : 1)
}
function drag(event: DragEvent, item: QueuedTicket) {
  if (!allowed.value || queue.busy || !event.dataTransfer) { event.preventDefault(); return }
  dragging.value = item.ticket_id; preview.value = shared.value.map(row => row.ticket_id)
  event.dataTransfer.effectAllowed = 'move'; event.dataTransfer.setData('text/plain', item.ticket_id)
}
function over(event: DragEvent, ticket: string) {
  if (!dragging.value || !preview.value || ticket === dragging.value) return
  event.preventDefault()
  const rect = (event.currentTarget as HTMLElement).getBoundingClientRect()
  const ids = preview.value.filter(id => id !== dragging.value), at = ids.indexOf(ticket)
  ids.splice(at + (event.clientY > rect.top + rect.height / 2 ? 1 : 0), 0, dragging.value); preview.value = ids
}
async function drop() {
  const ids = preview.value, before = snapshot.value?.items.filter(item => !item.target_agent_id).map(item => item.ticket_id)
  const ticket = dragging.value
  dragging.value = ''
  if (ids && before && ids.join() !== before.join()) await change(() => queue.order(props.projectId, ids, ticket), ticket)
  preview.value = null
}
</script>
<template>
  <FloatingPanel :anchor="anchor" :width="480" :tallest="600" align="end" label="Work queue" cycle @close="restore => emit('close', restore)">
    <div class="qv">
      <div class="qv-head"><p class="eyebrow">Work queue</p><span class="qv-count mono">{{ snapshot?.items.length ?? 0 }}</span><span v-if="!snapshot?.manual_order" class="auto-note">Priority order</span></div>
      <p v-if="snapshot?.items.length" class="qv-warn" :class="{ on: snapshot.capacity.warning || (snapshot.capacity.hours ?? 0) >= 4 }"><AppIcon name="clock" :size="13" />{{ hours }}</p>
      <p v-if="queue.errors[projectId]" class="error" role="alert">{{ queue.errors[projectId] }}<button class="btn sm" type="button" @click="queue.load(projectId, true)">Retry</button></p>
      <div v-if="snapshot?.manual_order" class="qv-notice" role="note"><p><AppIcon name="info" :size="13" />Manual order: your moves override priority until you reset the shared queue across projects.</p><button type="button" class="btn sm ghost" :disabled="!allowed || queue.busy" aria-label="Reset shared queue across projects" @click="change(() => queue.reset(projectId))">Reset</button></div>
      <p class="qv-sec mono-label">Next free agent</p>
      <ol v-if="shared.length" ref="list" class="qv-list" aria-label="Queue for the next free agent" @dragover.prevent @drop.prevent="drop" @dragend="dragging = ''; preview = null">
        <li v-for="(item, index) in shared" :key="item.ticket_id" class="qv-item" :class="{ dragging: dragging === item.ticket_id }" :data-ticket="item.ticket_id" :draggable="allowed && !queue.busy" @dragstart="drag($event, item)" @dragover="over($event, item.ticket_id)" @keydown="keys($event, item)">
          <span class="grip" aria-hidden="true" /><span class="qv-pos mono">#{{ item.position }}</span>
          <button type="button" class="qv-main" data-action="open" :aria-label="`Number ${item.position}: ${item.key} ${item.title}. Open it`" aria-keyshortcuts="Alt+ArrowUp Alt+ArrowDown" @click="emit('open', item.key)"><span class="key">{{ item.key }}</span><span class="t">{{ item.title }}</span><PriorityIcon v-if="item.priority" :priority="item.priority" /></button>
          <span class="qv-acts">
            <button type="button" class="icon-btn sm flat" data-action="up" :disabled="!allowed || queue.busy || index === 0" :aria-label="`Move ${item.key} up`" data-tip="Move up · Alt Up arrow" @click="move(item, -1)"><AppIcon name="chevron-up" :size="13" /></button>
            <button type="button" class="icon-btn sm flat" data-action="down" :disabled="!allowed || queue.busy || index === shared.length - 1" :aria-label="`Move ${item.key} down`" data-tip="Move down · Alt Down arrow" @click="move(item, 1)"><AppIcon name="chevron" :size="13" /></button>
            <button type="button" class="icon-btn sm flat" data-action="top" :disabled="!allowed || queue.busy || index === 0" :aria-label="`Move ${item.key} to the top`" data-tip="Move to top" @click="move(item, 'top')"><AppIcon name="to-top" :size="13" /></button>
            <button type="button" class="icon-btn sm flat" :disabled="!allowed || queue.busy" :aria-label="`Remove ${item.key} from the queue`" @click="change(() => queue.remove(projectId, item.ticket_id))"><AppIcon name="close" :size="13" /></button>
          </span>
          <span class="qv-sub"><span v-if="item.waiting_reason" class="qv-wait"><AppIcon name="clock" :size="12" />{{ item.waiting_reason }}</span><QueueModel :model="item.expected_model" :effort="item.expected_effort" /><span>{{ item.expected_agent?.name }}</span><span>{{ item.expected_start ? `starts ~${absoluteTime(item.expected_start)}` : item.state === 'blocked' ? 'starts once unblocked' : 'awaiting capacity' }}</span><span v-if="item.by.kind === 'agent'">queued by {{ item.by.name }}</span></span>
        </li>
      </ol>
      <p v-else class="qv-empty">{{ snapshot ? 'Nothing in line. Queue a ticket from its row or its Assignee menu.' : 'Loading the queue…' }}</p>
      <template v-if="targeted.length"><p class="qv-sec mono-label">For a specific agent</p><ol class="qv-list" aria-label="Waiting for a specific agent"><li v-for="item in targeted" :key="item.ticket_id" class="qv-item">
        <span /><span class="qv-pos mono">#{{ item.position }}</span><button type="button" class="qv-main" @click="emit('open', item.key)"><span class="key">{{ item.key }}</span><span class="t">{{ item.title }}</span></button>
        <span class="qv-acts"><button type="button" class="icon-btn sm flat" :disabled="!allowed || queue.busy" :aria-label="`Remove ${item.key} from the queue`" @click="change(() => queue.remove(projectId, item.ticket_id))"><AppIcon name="close" :size="13" /></button></span>
        <span class="qv-sub"><QueueModel :model="item.expected_model" :effort="item.expected_effort" /><span>for {{ item.expected_agent?.name }}</span><span>{{ item.waiting_reason || (item.expected_start ? `starts ~${absoluteTime(item.expected_start)}` : 'when it frees') }}</span></span>
      </li></ol></template>
      <div class="qv-foot"><button type="button" class="btn sm" :aria-pressed="!!filtered" @click="emit('filter')"><AppIcon name="filter" :size="13" />{{ filtered ? 'Showing queued in the list' : 'Show queued in the list' }}</button><span><KeyCap k="alt" /><KeyCap k="up" /><KeyCap k="down" /> move · drag to reorder</span></div>
    </div>
  </FloatingPanel>
</template>
<style scoped>
.qv { display: grid; gap: 6px; }
.qv-head { display: flex; align-items: center; gap: 8px; padding: 2px 6px 0 10px; }
.qv-count, .auto-note { font-size: 11px; color: var(--ink-3); }
.auto-note { margin-left: auto; }
.qv-warn, .qv-notice { display: flex; align-items: center; gap: 7px; margin: 0 4px; padding: 6px 10px; border-radius: 10px; background: var(--surface-sunken); color: var(--ink-2); font-size: 12.5px; }
.qv-warn.on, .qv-wait { background: var(--queue-wait-bg); box-shadow: inset 0 0 0 1px var(--queue-wait-line); color: var(--queue-wait-ink); }
.qv-notice { background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); padding-right: 3px; }
.qv-notice p { display: flex; align-items: center; gap: 7px; flex: 1; color: var(--teal-ink); }
.qv-sec { padding: 6px 10px 0; font-size: 10px; }
.qv-list { display: grid; gap: 1px; margin: 0; padding: 0; list-style: none; }
.qv-item { display: grid; grid-template-columns: 12px 24px minmax(0, 1fr) auto; grid-template-areas: 'grip pos main acts' '. . sub sub'; align-items: center; gap: 1px 6px; padding: 5px 4px 6px; border-radius: 10px; }
.qv-item[draggable=true] { cursor: grab; }
.qv-item:hover { background: var(--row-hover); }
.qv-item:focus-within { background: var(--row-selected); }
.dragging { opacity: .45; }
.grip { grid-area: grip; width: 10px; height: 14px; background: radial-gradient(circle, var(--ink-3) 1.2px, transparent 1.5px) 0 0 / 5px 5px; opacity: .75; }
.qv-pos { grid-area: pos; font-size: 11.5px; font-weight: 600; color: var(--ink-2); text-align: right; }
.qv-main { grid-area: main; display: flex; align-items: center; gap: 8px; min-width: 0; height: 26px; padding: 0 6px; border: 0; border-radius: 7px; background: transparent; color: var(--ink); font-size: 13px; text-align: left; }
.key { flex: none; font: 500 11px/1 var(--mono); color: var(--ink-2); }
.t { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.qv-acts { grid-area: acts; display: inline-flex; gap: 1px; opacity: 0; }
.qv-item:hover .qv-acts, .qv-item:focus-within .qv-acts { opacity: 1; }
.qv-acts .icon-btn { width: 26px; height: 26px; }
.qv-sub { grid-area: sub; display: flex; flex-wrap: wrap; align-items: center; gap: 3px 10px; padding-left: 6px; font-size: 12px; color: var(--ink-2); }
.qv-wait { display: inline-flex; align-items: center; gap: 5px; min-height: 20px; padding: 0 8px; border-radius: 999px; }
.qv-empty { padding: 8px 12px; font-size: 12.5px; color: var(--ink-3); }
.qv-foot { display: flex; flex-wrap: wrap; justify-content: space-between; gap: 8px; padding: 8px 4px 2px; border-top: 1px solid var(--line); font-size: 11.5px; color: var(--ink-3); }
.error { padding: 6px; color: var(--danger); }
@media (hover: none) { .qv-acts { opacity: 1; } .qv-foot > span { display: none; } }
@media (max-width: 480px) { .qv-item { grid-template-columns: 10px 22px minmax(0, 1fr); grid-template-areas: 'grip pos main' '. . acts' '. . sub'; } .qv-acts { opacity: 1; } }
</style>
