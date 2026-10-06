// SPDX-License-Identifier: AGPL-3.0-only
import { computed, onBeforeUnmount, type Ref } from 'vue'
import { useLiveAgents } from '../stores/liveAgents'
import { useProjectLeads } from '../stores/projectLeads'
import { useWorkQueue } from '../stores/workQueue'
import { gateOf, leadBand, leadLine, mergedOf, LEAD_WORDS } from './lead'
import type { LiveAgent } from './liveAgents'

// What every lead surface derives from the same reads: band, line and panel.
// Sessions come from the shared live-agents read (AEON-184), never a second list.
export function useLeadSummary(projectId: Ref<string>, projectKey: Ref<string>) {
  const leads = useProjectLeads(), queue = useWorkQueue(), live = useLiveAgents()
  const stopLive = live.watch()
  onBeforeUnmount(stopLive)
  const view = computed(() => leads.views[projectId.value] ?? null)
  const lead = computed(() => view.value?.lead ?? null)
  const queued = computed(() => queue.snapshots[projectId.value]?.items ?? null)
  const queuedCount = computed(() => queued.value ? queued.value.length : null)
  const band = computed(() => leadBand(lead.value, projectKey.value, queuedCount.value, LEAD_WORDS))
  const sessions = computed<LiveAgent[]>(() => live.items.filter(agent => agent.project_id === projectId.value && !agent.stopped_at))
  const leadSession = computed(() => lead.value?.session_id ? sessions.value.find(agent => agent.session_id === lead.value!.session_id) ?? null : null)
  // Workers of this project; one lead per project starts them.
  const workers = computed(() => lead.value?.session_id ? sessions.value.filter(agent => agent.role === 'worker') : [])
  const decisions = computed(() => view.value?.decisions ?? [])
  // Gate and merges come from the project's lead reports across generations;
  // until that history is read in full they are unknown.
  const complete = computed(() => !!view.value && view.value.caughtUp && !view.value.decisionsError)
  const keyOf = (id: string) => leads.keys[id]?.key ?? null
  const gateIds = computed(() => complete.value ? gateOf(view.value!.fold) : null)
  const mergedIds = computed(() => complete.value ? mergedOf(view.value!.fold) : null)
  const keysFor = (ids: string[] | null) => ids && ids.map(id => keyOf(id) ?? '…')
  const stations = computed(() => leadLine({
    state: lead.value?.state ?? 'none',
    queued: queued.value ? queued.value.map(item => item.key) : null,
    waiting: lead.value?.state === 'waiting_for_room',
    working: workers.value.flatMap(agent => agent.ticket ? [agent.ticket.key] : []),
    gate: keysFor(gateIds.value), merged: keysFor(mergedIds.value),
  }))
  // The asker is the lead's agent, whether or not its session still runs.
  const questions = computed(() => {
    const principal = leadSession.value?.principal_id ?? view.value?.principal?.id
    return principal ? (view.value?.questions ?? []).filter(q => q.askers.some(a => a.principal_id === principal)) : []
  })
  // More open questions exist than were read: some of the lead's may be among them.
  const questionsPartial = computed(() => !!view.value?.questionsMore)
  return { view, lead, band, queued, queuedCount, sessions, leadSession, workers, decisions, complete, gateIds, mergedIds, stations, questions, questionsPartial, keyOf }
}
