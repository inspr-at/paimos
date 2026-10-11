// SPDX-License-Identifier: AGPL-3.0-only
import { computed, onScopeDispose, watch, type Ref } from 'vue'
import { useAgentProjection } from '../stores/agentProjection'
import { useLiveAgents } from '../stores/liveAgents'
import { useProjectLeads } from '../stores/projectLeads'
import { useWorkQueue } from '../stores/workQueue'
import { gateOf, leadBand, leadLine, mergedOf, LEAD_WORDS } from './lead'
import type { LiveAgent } from './liveAgents'

// What every lead surface derives from the same reads: band, line and panel.
// Sessions come from the shared live-agents read (AEON-184), never a second list; workers,
// queued counts and questions bind through the shared Agents projection (AEON-1210 G2).
export function useLeadSummary(projectId: Ref<string>, projectKey: Ref<string>) {
  const leads = useProjectLeads(), queue = useWorkQueue(), live = useLiveAgents(), projection = useAgentProjection()
  const stopLive = live.watch()
  let stopProjection = () => {}
  watch(projectId, id => { stopProjection(); stopProjection = projection.watchProjects({ project: id }) }, { immediate: true })
  onScopeDispose(() => { stopLive(); stopProjection() })
  const view = computed(() => leads.views[projectId.value] ?? null)
  const lead = computed(() => view.value?.lead ?? null)
  const queued = computed(() => queue.snapshots[projectId.value]?.items ?? null)
  // The count the Agents tree shows, else this project's own queue read. A tree count from a queue
  // read cut short is only a lower bound: the project's own read stands in for it (AEON-1210 G2).
  const queuedCount = computed(() => {
    const own = queued.value ? queued.value.length : null
    return projection.queue.partial ? own : projection.queuedOf(projectId.value) ?? own
  })
  const band = computed(() => leadBand(lead.value, projectKey.value, queuedCount.value, LEAD_WORDS))
  const sessions = computed<LiveAgent[]>(() => live.items.filter(agent => agent.project_id === projectId.value && !agent.stopped_at))
  const leadSession = computed(() => lead.value?.session_id ? sessions.value.find(agent => agent.session_id === lead.value!.session_id) ?? null : null)
  // The lead's workers by their real parent session, never every worker in the project; null while that tree is unknown.
  const workerIds = computed(() => projection.workersOf(projectId.value, lead.value?.session_id))
  const workers = computed(() => { const ids = workerIds.value; return ids ? sessions.value.filter(agent => !!agent.session_id && ids.has(agent.session_id)) : [] })
  const workersKnown = computed(() => workerIds.value !== null)
  // Only part of the project's sessions could be read: the lead has at least these workers.
  const workersPartial = computed(() => workersKnown.value && projection.treeSource(projectId.value).partial)
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
  // Questions the lead's current session asked, by that exact session, whether or not it still runs.
  // Asks from another generation or without a recorded session stay on the Decision Desk.
  const questions = computed(() => projection.asksOf(lead.value?.session_id).filter(q => q.project_id === projectId.value))
  // More open questions exist than were read, or they could not be read: some of the lead's may be among them.
  const questionsPartial = computed(() => projection.questions.partial || projection.questions.state === 'error' || projection.questions.state === 'forbidden')
  return { view, lead, band, queued, queuedCount, sessions, leadSession, workers, workersKnown, workersPartial, decisions, complete, gateIds, mergedIds, stations, questions, questionsPartial, keyOf }
}
