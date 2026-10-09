<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { can } from '../../lib/authz'
import { canLaunchLead, leadBand, leadLaunchReason, LEAD_WORDS, type ProjectLead } from '../../lib/lead'
import { openLeadPanel, openStartLead } from '../../lib/leadOverlay'
import { usePoller } from '../../lib/usePolledData'
import { queueRequest, type QueueWireSnapshot } from '../../lib/workQueue'
import { useAgents } from '../../stores/agents'
import { useProjectLeads } from '../../stores/projectLeads'
import { useProjects } from '../../stores/projects'
import { useSession } from '../../stores/session'
import AppIcon from '../AppIcon.vue'
import LeadBot from './LeadBot.vue'

// AEON-741: one calm line per project lead between the dial and Sessions. A
// project without a lead is listed only while work waits for one.
const w = LEAD_WORDS
const leads = useProjectLeads(), projects = useProjects(), agents = useAgents(), session = useSession()
const queued = ref<Record<string, number> | null>(null)
const active = computed(() => projects.projects.filter(p => !p.archived))
async function readQueued() {
  const who = session.identity?.principal.id
  try {
    const raw = await queueRequest<QueueWireSnapshot>('/queue')
    if (who !== session.identity?.principal.id) return
    const counts: Record<string, number> = {}
    for (const item of raw.items) if (item.project_id) counts[item.project_id] = (counts[item.project_id] ?? 0) + 1
    queued.value = counts
  } catch { if (who === session.identity?.principal.id) queued.value = null }
}
const refresh = () => Promise.all([leads.loadMany(active.value.map(p => p.id)), readQueued()])
const poller = usePoller(refresh, 30_000)
watch(() => [session.identity?.principal.id, active.value.length], () => { void refresh() }, { immediate: true })
onMounted(() => poller.start())
onBeforeUnmount(() => poller.stop())

interface Row { id: string; key: string; lead: ProjectLead | null; queued: number | null; name: string; sub: string; status: string; tone: string; busy: boolean }
const leadSession = (lead: ProjectLead | null) => lead?.session_id ? agents.views.find(v => v.session.id === lead.session_id) ?? null : null
const workersOf = (lead: ProjectLead | null) => lead?.session_id ? agents.views.filter(v => v.session.parent_harness_session_id === lead.session_id && !v.session.stopped_at).length : 0
const rows = computed<Row[]>(() => active.value.flatMap((p): Row[] => {
  const view = leads.views[p.id], lead = view?.lead ?? null, q = queued.value ? queued.value[p.id] ?? 0 : null
  if (!lead) return []
  const band = leadBand(lead, p.routeKey, q, w)
  if (lead.state === 'none') return q ? [{ id: p.id, key: p.routeKey, lead, queued: q, name: band.title, sub: canLaunchLead(lead) ? `${q} queued work ${q === 1 ? 'item waits' : 'items wait'}` : leadLaunchReason(lead), status: `Start ${w.l}…`, tone: 'act', busy: false }] : []
  const s = leadSession(lead), n = workersOf(lead)
  const activity = lead.state === 'working' ? s?.session.current_activity?.text || s?.session.activity_note || '' : band.status
  const sub = [activity, lead.state === 'working' ? `${n} ${n === 1 ? 'worker' : 'workers'}` : '', q === null ? '' : `${q} queued`].filter(Boolean).join(' · ')
  const word = { starting: 'Starting', working: 'Working', waiting_for_room: 'Waiting', paused: 'Paused', cannot_start: 'Can’t start' }[lead.state] ?? ''
  return [{ id: p.id, key: p.routeKey, lead, queued: q, name: band.title, sub, status: word, tone: band.tone, busy: band.busy }]
}).sort((a, b) => Number(a.lead?.state === 'none') - Number(b.lead?.state === 'none')))
const count = computed(() => rows.value.filter(r => r.lead && r.lead.state !== 'none').length)
const mayStart = computed(() => session.identity?.principal.kind === 'person' && can('harness.control') && can('run.create'))
/** Projects a person could start a lead for: the + menu offers Start lead only while one exists. */
const withoutLead = computed(() => active.value.filter(p => leads.views[p.id]?.lead?.state === 'none').map(p => p.id))
const launchAvailable = computed(() => withoutLead.value.some(id => canLaunchLead(leads.views[id]?.lead)))
function open(row: Row, event: MouseEvent) {
  const from = event.currentTarget as HTMLElement
  if (row.lead?.state === 'none') { if (mayStart.value && canLaunchLead(row.lead)) openStartLead([row.id], from, true) }
  else openLeadPanel(row.id, from)
}
defineExpose({ withoutLead, mayStart, launchAvailable })
</script>

<template>
  <section v-if="rows.length" class="zone" aria-labelledby="leads-title">
    <div class="zone-head"><h2 id="leads-title">{{ w.P }}<span class="count mono">{{ count }}</span></h2><p class="small faint">One per project · each starts its workers within the dial</p></div>
    <ul class="list">
      <li v-for="row in rows" :key="row.id">
        <button type="button" class="lead-row" :data-project="row.key" :aria-disabled="row.lead?.state === 'none' && (!mayStart || !canLaunchLead(row.lead))" @click="open(row, $event)">
          <span class="lead-bot"><LeadBot :busy="row.busy" /></span>
          <span class="lr-who"><span class="lr-name" :class="{ faint: row.lead?.state === 'none' }">{{ row.name }}</span><span class="lr-sub">{{ row.sub }}</span></span>
          <span class="st" :class="row.tone">
            <span v-if="row.tone === 'live'" class="live-mark" aria-hidden="true" /><span v-else-if="row.tone === 'wait'" class="dot wait" aria-hidden="true" /><span v-else-if="row.tone === 'warn'" class="dot warn" aria-hidden="true" /><AppIcon v-else-if="row.tone === 'rest'" name="pause" :size="13" /><AppIcon v-else name="play" :size="13" />{{ row.status }}
          </span>
          <span class="chev"><AppIcon name="chevron-right" :size="16" /></span>
        </button>
      </li>
    </ul>
    <p v-if="leads.truncated" class="small faint more">Showing the first 50 projects. Open a project to see its {{ w.l }}.</p>
  </section>
</template>

<style scoped>
.zone { margin-top: 30px; }
.zone-head { display: flex; align-items: baseline; justify-content: space-between; flex-wrap: wrap; gap: 4px 16px; margin-bottom: 10px; }
.zone-head h2 { margin: 0; font-size: 17px; }
.zone-head .count { margin-left: 6px; color: var(--ink-3); font-size: 14px; font-weight: 500; }
.small { margin: 0; font-size: 12.5px; }
.faint { color: var(--ink-3); }
.more { margin-top: 8px; }
.list { margin: 0; padding: 0; list-style: none; border-top: 1px solid var(--line); }
.lead-row { display: grid; grid-template-columns: 36px minmax(0, 1.4fr) minmax(0, 1fr) 16px; grid-template-areas: 'icon who status chev'; align-items: center; gap: 2px 16px; width: 100%; min-height: 68px; padding: 10px; border: 0; border-bottom: 1px solid var(--line); background: transparent; color: var(--ink); text-align: left; cursor: pointer; }
.lead-row:hover { background: var(--row-hover); }
.lead-row[aria-disabled="true"] { cursor: default; }
.lead-bot { grid-area: icon; display: grid; place-items: center; width: 36px; height: 36px; border-radius: 11px; background: var(--surface-sunken); box-shadow: inset 0 0 0 1px var(--line); }
.lr-who { grid-area: who; display: grid; min-width: 0; }
.lr-name { color: var(--ink); font-size: 14px; font-weight: 650; }
.lr-name.faint { color: var(--ink-3); }
.lr-sub { color: var(--ink-2); font-size: 12.5px; overflow-wrap: anywhere; }
.st { grid-area: status; display: inline-flex; align-items: center; gap: 8px; min-width: 0; color: var(--ink-2); font-size: 13px; }
.st.warn { color: var(--warn-ink); font-weight: 600; }
.st.rest { color: var(--ink-3); }
.st.act { color: var(--teal-ink); font-weight: 600; }
.chev { grid-area: chev; color: var(--ink-3); }
.live-mark { display: inline-block; flex: none; width: 8px; height: 8px; border-radius: 50%; background: var(--teal); box-shadow: 0 0 0 3px color-mix(in srgb, var(--teal) 18%, transparent); }
.dot { display: inline-block; flex: none; width: 8px; height: 8px; border-radius: 50%; }
.dot.warn { background: var(--gold); }
.dot.wait { box-shadow: inset 0 0 0 1.8px var(--gold); }
@media (max-width: 720px) {
  .lead-row { grid-template-columns: 36px minmax(0, 1fr) 16px; grid-template-areas: 'icon who chev' 'icon status chev'; gap: 4px 12px; }
  .lead-bot { align-self: start; }
}
</style>
