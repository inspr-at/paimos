// SPDX-License-Identifier: AGPL-3.0-only
// The projects' leads and their queued work for the Agents page (AEON-741, moved into the
// Sessions list by AEON-1210): one bounded lead read per active project and one queue read,
// refreshed every 30 seconds. The list heads each project group with its lead, or a slim line
// while work waits for one; the New menu offers Start lead only while a project has none.
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { can } from '../../lib/authz'
import { canLaunchLead, leadLaunchReason, LEAD_LAUNCH_OFF, type ProjectLead } from '../../lib/lead'
import { usePoller } from '../../lib/usePolledData'
import { queueRequest, type QueueWireSnapshot } from '../../lib/workQueue'
import { useProjectLeads } from '../../stores/projectLeads'
import { useProjects } from '../../stores/projects'
import { useSession } from '../../stores/session'

export interface AgentProjects {
  /** The project's lead as last read; null or undefined until one could be read. */
  lead: (projectId: string) => ProjectLead | null | undefined
  /** Queued work items per project; null while the queue cannot be read. */
  queued: (projectId: string) => number | null
  /** The person may start a lead (each project still checks its own launch). */
  mayStart: boolean
  /** Only the first 50 projects' leads are read. */
  truncated: boolean
}

export function useAgentProjects() {
  const leads = useProjectLeads(), projects = useProjects(), session = useSession()
  const counts = ref<Record<string, number> | null>(null)
  const active = computed(() => projects.projects.filter(p => !p.archived))
  async function readQueued() {
    const who = session.identity?.principal.id
    try {
      const raw = await queueRequest<QueueWireSnapshot>('/queue')
      if (who !== session.identity?.principal.id) return
      const next: Record<string, number> = {}
      for (const item of raw.items) if (item.project_id) next[item.project_id] = (next[item.project_id] ?? 0) + 1
      counts.value = next
    } catch { if (who === session.identity?.principal.id) counts.value = null }
  }
  const refresh = () => Promise.all([leads.loadMany(active.value.map(p => p.id)), readQueued()])
  const poller = usePoller(refresh, 30_000)
  watch(() => [session.identity?.principal.id, active.value.length], () => { counts.value = null; void refresh() }, { immediate: true })
  onMounted(() => poller.start())
  onBeforeUnmount(() => poller.stop())

  const mayStart = computed(() => session.identity?.principal.kind === 'person' && can('harness.control') && can('run.create'))
  /** Projects a person could start a lead for: the New menu offers Start lead only while one exists. */
  const withoutLead = computed(() => active.value.filter(p => leads.views[p.id]?.lead?.state === 'none').map(p => p.id))
  const launchAvailable = computed(() => withoutLead.value.some(id => canLaunchLead(leads.views[id]?.lead)))
  const launchReason = computed(() => withoutLead.value.length && withoutLead.value.every(id => leads.views[id]?.lead?.automatic_launch_enabled === false) ? LEAD_LAUNCH_OFF : leadLaunchReason(null))
  const view = computed<AgentProjects>(() => ({
    lead: id => leads.views[id]?.lead,
    queued: id => counts.value ? counts.value[id] ?? 0 : null,
    mayStart: mayStart.value,
    truncated: leads.truncated,
  }))
  return { view, withoutLead, mayStart, launchAvailable, launchReason }
}
