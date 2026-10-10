<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import type { ListItem } from '../../lib/api'
import { can } from '../../lib/authz'
import { canLaunchLead, leadLaunchReason, LEAD_WORDS, ticketSteps, type TicketStepInput } from '../../lib/lead'
import { openStartLead } from '../../lib/leadOverlay'
import { listReviews } from '../../lib/reviews'
import { queueable } from '../../lib/workQueue'
import { statusMeta } from '../../lib/work'
import { useProjectLeads } from '../../stores/projectLeads'
import { useSession } from '../../stores/session'
import { useWorkQueue } from '../../stores/workQueue'
import AppIcon from '../AppIcon.vue'

// AEON-741: where this ticket stands on the lead's line, in one reserved slot
// under the title: Queued → Sized → Working → Gate → Merged. Shown only where
// the project's lead can be read.
const props = defineProps<{ item: ListItem; projectId: string; projectKey: string; openLeaves?: number | null }>()
const w = LEAD_WORDS
const leads = useProjectLeads(), queue = useWorkQueue(), session = useSession()
const lead = computed(() => leads.views[props.projectId]?.lead ?? null)
watch(() => props.projectId, id => { if (!leads.views[id]?.lead) void leads.load(id) }, { immediate: true })
const entry = computed(() => queue.entry(props.projectId, props.item.id))
const status = computed(() => statusMeta(props.item.state).key)
const done = computed(() => ['done', 'delivered', 'accepted'].includes(status.value))
const parent = computed(() => props.item.is_leaf === false)

// The latest cross-family review decides the Gate step; bounded to 50 by the server.
const review = ref<TicketStepInput['review']>('none')
let turn = 0
watch(() => [props.item.id, props.item.updated_at, status.value], async () => {
  const id = props.item.id, mine = ++turn
  if (parent.value || !['progress', 'qa'].includes(status.value) || !can('work_orders.read', props.projectId)) { review.value = 'none'; return }
  try {
    const latest = (await listReviews(id))[0]
    if (mine !== turn || props.item.id !== id) return
    review.value = !latest ? 'none' : latest.gate_open ? 'passed' : latest.status === 'completed' && latest.result.verdict === 'changes' ? 'changes' : ['queued', 'running', 'blocked'].includes(latest.status) ? 'running' : 'none'
  } catch { if (mine === turn) review.value = 'none' }
}, { immediate: true })
onBeforeUnmount(() => { turn++ })

type Mode = 'hidden' | 'notq' | 'parent' | 'nolead' | 'steps'
const mode = computed<Mode>(() => {
  if (!lead.value) return 'hidden'
  if (parent.value) return props.openLeaves ? 'parent' : 'hidden'
  if (['cancelled', 'archived'].includes(status.value)) return 'hidden'
  if (entry.value && lead.value.state === 'none') return 'nolead'
  if (entry.value || ['progress', 'qa'].includes(status.value) || done.value) return 'steps'
  return queueable(props.item) ? 'notq' : 'hidden'
})
const steps = computed(() => ticketSteps({
  queuedPosition: entry.value?.position ?? null, queuedAt: entry.value?.at ?? null,
  estimateHours: typeof props.item.fields.estimate_hours === 'number' ? props.item.fields.estimate_hours : null,
  status: done.value ? 'done' : status.value === 'progress' ? 'progress' : status.value === 'qa' ? 'qa' : 'open',
  worker: props.item.lead_worker?.name ?? null, review: review.value,
}, w))
const mayStart = computed(() => session.identity?.principal.kind === 'person' && can('harness.control', props.projectId) && can('run.create', props.projectId))
const ICON = { queued: 'queue', sized: 'check', working: 'agent', gate: 'shield', merged: 'merge' } as const
</script>

<template>
  <div v-if="mode !== 'hidden'" class="tline" :data-mode="mode" aria-live="polite">
    <p v-if="mode === 'notq'" class="tl-msg"><AppIcon name="info" :size="14" /><span><b>Not queued.</b> Queue it and the {{ projectKey }} {{ w.l }} picks it up: it sizes it, chooses who works on it and brings it through the gate.</span></p>
    <p v-else-if="mode === 'parent'" class="tl-msg"><AppIcon name="tree" :size="14" /><span><b>{{ openLeaves }} open work {{ openLeaves === 1 ? 'item' : 'items' }} below.</b> Queue queues them in their order. Agents work on leaves; {{ item.key }} follows its children.</span></p>
    <p v-else-if="mode === 'nolead'" class="tl-msg"><AppIcon name="clock" :size="14" /><span><b>Queued, waiting for a {{ w.l }}.</b> No {{ w.l }} runs {{ projectKey }}, so nothing picks it up yet. <button v-if="mayStart" type="button" class="link-btn" :aria-disabled="!canLaunchLead(lead)" :data-tip="!canLaunchLead(lead) ? leadLaunchReason(lead) : undefined" @click="canLaunchLead(lead) && openStartLead([projectId], $event.currentTarget as HTMLElement)">Start {{ w.l }}</button><template v-if="!canLaunchLead(lead)"> {{ leadLaunchReason(lead) }}</template></span></p>
    <template v-else>
      <div v-for="s in steps" :key="s.id" class="tl-step" :class="s.state" :data-step="s.id">
        <span class="tl-dot"><AppIcon v-if="s.state === 'done'" name="check" :size="11" /><AppIcon v-else-if="s.state === 'now'" :name="ICON[s.id]" :size="11" /></span>
        <b>{{ s.label }}</b><span>{{ s.note }}</span>
      </div>
    </template>
  </div>
</template>

<style scoped>
.tline { display: grid; grid-template-columns: repeat(5, minmax(0, 1fr)); margin-top: 14px; min-height: 64px; }
.tl-step { position: relative; display: grid; align-content: start; gap: 2px; padding: 0 12px 0 0; }
.tl-step::before { content: ''; position: absolute; left: 22px; right: 2px; top: 9px; height: 1px; background: var(--line-2); }
.tl-step:last-child::before { display: none; }
.tl-dot { position: relative; width: 18px; height: 18px; border-radius: 50%; background: var(--surface-raised); box-shadow: inset 0 0 0 1.5px var(--line-2); display: grid; place-items: center; color: var(--ink-3); }
.tl-step.done .tl-dot { background: color-mix(in srgb, var(--ok) 14%, var(--surface-raised)); box-shadow: inset 0 0 0 1.5px color-mix(in srgb, var(--ok) 60%, transparent); color: var(--ok); }
.tl-step.now .tl-dot { box-shadow: inset 0 0 0 1.5px var(--teal), 0 0 0 4px color-mix(in srgb, var(--teal) 14%, transparent); background: color-mix(in srgb, var(--teal) 12%, var(--surface-raised)); color: var(--teal-ink); }
.tl-step.done::before { background: color-mix(in srgb, var(--ok) 50%, transparent); }
.tl-step b { margin-top: 6px; color: var(--ink-2); font-size: 12.5px; font-weight: 600; }
.tl-step.now b, .tl-step.done b { color: var(--ink); }
.tl-step span:not(.tl-dot) { font-size: 12px; color: var(--ink-3); line-height: 1.4; overflow-wrap: anywhere; }
.tl-msg { grid-column: 1 / -1; display: grid; grid-template-columns: 18px minmax(0, 1fr); gap: 10px; align-items: start; margin: 0; padding-top: 2px; font-size: 13.5px; color: var(--ink-2); }
.tl-msg svg { margin-top: 3px; color: var(--ink-3); }
.tl-msg b { color: var(--ink); }
.link-btn { display: inline; padding: 0; border: 0; background: none; color: var(--teal-ink); font: inherit; font-weight: 600; text-decoration: underline; text-decoration-color: color-mix(in srgb, var(--teal) 35%, transparent); text-underline-offset: 3px; cursor: pointer; }
.link-btn:hover { text-decoration-color: currentColor; }
.link-btn[aria-disabled="true"] { color: var(--ink-3); cursor: default; }
@media (max-width: 720px) {
  .tline { grid-template-columns: minmax(0, 1fr); gap: 0; min-height: 0; }
  .tl-step { grid-template-columns: 18px minmax(0, 1fr); column-gap: 12px; padding: 0 0 12px; }
  .tl-step::before { left: 8.5px; right: auto; top: 22px; bottom: 2px; width: 1px; height: auto; }
  .tl-dot { grid-row: span 2; }
  .tl-step b { margin-top: 0; }
  .tl-step > span:not(.tl-dot) { grid-column: 2; }
}
</style>
