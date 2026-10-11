// SPDX-License-Identifier: AGPL-3.0-only
// The Agents page's view of every project's lead, queued work and open asks (AEON-741, moved into
// the Sessions list by AEON-1210). It reads the shared projection (stores/agentProjection.ts) for
// every active project while the page shows it: the list heads each project group with its lead,
// or a slim line while work waits for one; the New menu offers Start lead only while a project has none.
import { computed, onScopeDispose } from 'vue'
import { can } from '../../lib/authz'
import { canLaunchLead, leadLaunchReason, LEAD_LAUNCH_OFF, type ProjectLead } from '../../lib/lead'
import { useAgentProjection } from '../../stores/agentProjection'
import { useProjectLeads } from '../../stores/projectLeads'
import { useProjects } from '../../stores/projects'
import { useSession } from '../../stores/session'

export interface AgentProjects {
  /** The project's lead as last read; null or undefined until one could be read. */
  lead: (projectId: string) => ProjectLead | null | undefined
  /** Queued work items per project; null while the queue cannot be read; at least this many while `queuePartial`. */
  queued: (projectId: string) => number | null
  /** The queue could not be read: queue chips and no-lead lines are missing, not absent. Empty once it is read. */
  queueError: string
  /** Only part of the queue was read: each queued count is at least what it shows. */
  queuePartial: boolean
  /** Open questions asked by exactly this session (never by its principal or another generation). */
  asks: (sessionId: string) => number
  /** Not every open question could be read: the questions counted below a lead may be fewer than it has. */
  asksPartial: boolean
  /** The person may start a lead (each project still checks its own launch). */
  mayStart: boolean
  /** Only the first 50 projects' leads are read. */
  truncated: boolean
}

export function useAgentProjects() {
  const leads = useProjectLeads(), projects = useProjects(), session = useSession(), projection = useAgentProjection()
  onScopeDispose(projection.watchProjects({ all: true }))
  const active = computed(() => projects.projects.filter(p => !p.archived))
  const mayStart = computed(() => session.identity?.principal.kind === 'person' && can('harness.control') && can('run.create'))
  /** Projects a person could start a lead for: the New menu offers Start lead only while one exists. */
  const withoutLead = computed(() => active.value.filter(p => leads.views[p.id]?.lead?.state === 'none').map(p => p.id))
  const launchAvailable = computed(() => withoutLead.value.some(id => canLaunchLead(leads.views[id]?.lead)))
  const launchReason = computed(() => withoutLead.value.length && withoutLead.value.every(id => leads.views[id]?.lead?.automatic_launch_enabled === false) ? LEAD_LAUNCH_OFF : leadLaunchReason(null))
  const view = computed<AgentProjects>(() => ({
    lead: id => leads.views[id]?.lead,
    queued: id => projection.queuedOf(id),
    // A refusal shows nothing to this person; a failure or a cut-short read says so (AEON-1210 G2).
    queueError: projection.queue.state === 'error' ? projection.queue.error || 'The queue can’t be read right now.' : '',
    queuePartial: projection.queue.state === 'ready' && projection.queue.partial,
    asks: id => projection.asksOf(id).length,
    asksPartial: projection.questions.partial,
    mayStart: mayStart.value,
    truncated: leads.truncated,
  }))
  return { view, withoutLead, mayStart, launchAvailable, launchReason }
}
