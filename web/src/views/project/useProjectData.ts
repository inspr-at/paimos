// SPDX-License-Identifier: AGPL-3.0-only

import { computed, onBeforeUnmount, watch } from 'vue'
import { listNodes, type ListItem } from '../../lib/api'
import { useOutline } from '../../lib/useOutline'
import { effectiveSort, groupFacet, groupRows, hasFilters, orderByStatus, totalFrom } from '../../lib/ticketList'
import { useTicketList } from '../../lib/useTicketList'
import { useLiveList } from '../../lib/useLiveList'
import { useLiveAgents } from '../../stores/liveAgents'
import { queueFilteredNodes } from '../../lib/queueFilter'
import type { ListQuery } from '../../lib/api'
import type { ProjectState } from './types'
import type { LiveListBlockers } from '../../lib/useLiveList'
interface ProjectDataHooks {
  openRow: (row: ListItem) => void
  quiet: (id: string) => boolean
  holds: (id: string) => boolean
  blockers: () => Omit<LiveListBlockers, 'dragging'>
  around: (removing: Set<string>, apply: () => void) => void
  applied: () => void
}

export function useProjectData(state: ProjectState, hooks: ProjectDataHooks) {
  const { session, project, projectId, queue, vocabulary, queueSnapshot, section, filters, viewMode, graphActive, graphState, outlineActive, collapsed } = state
  // A status change that met someone else's newer version opens the ticket to review it.
  async function fetchWorkList(query: ListQuery) {
    if (!query.state?.some(state => state === 'queued' || state === '!queued') || !projectId.value) return listNodes(query)
    const id = projectId.value
    await queue.load(id)
    const snapshot = queue.snapshots[id]
    if (!snapshot || queue.errors[id]) throw new Error(queue.errors[id] || 'The work queue could not be loaded.')
    return queueFilteredNodes(query, snapshot)
  }
  const list = useTicketList(projectId, filters, { review: hooks.openRow, fetchList: fetchWorkList })
  const outline = useOutline(projectId, filters, outlineActive, list, {
    fetchList: fetchWorkList,
    me: () => session.identity?.principal.id ?? null,
    quiet: hooks.quiet, holds: hooks.holds,
    blockers: () => ({ ...hooks.blockers(), dragging: !!document.querySelector('.dragging') }),
    around: hooks.around, applied: hooks.applied,
  })
  // Changes by others, live (AEON-326): the list patches rows in place and holds
  // structural updates behind the "N updates · Show" pill until it is safe.
  const listActive = computed(() => section.value === 'tickets' && viewMode.value === 'list')
  const liveList = useLiveList({
    projectId, filters, rows: list.rows, loading: list.loading, reads: list.reads, loadedOnce: list.loadedOnce,
    more: () => !!list.cursor.value, edge: () => list.edge.value, active: listActive, me: () => session.identity?.principal.id ?? null,
    quiet: hooks.quiet, holds: hooks.holds,
    blockers: () => ({ ...hooks.blockers(), dragging: false }),
    around: hooks.around, applied: hooks.applied, reload: () => void list.load(), fetchList: fetchWorkList,
  })

  const activeLive = computed(() => outlineActive.value ? outline.live : liveList)
  const liveActive = computed(() => listActive.value || outlineActive.value)
  const liveAgents = useLiveAgents()
  watch(() => liveAgents.updatedAt, () => { if (projectId.value) void queue.load(projectId.value, true) })
  const liveDataStale = computed(() => activeLive.value.dataStale.value || liveAgents.dataStale)
  const liveUpdatedAt = computed(() => {
    const listAt = activeLive.value.updatedAt.value, agentsAt = liveAgents.updatedAt
    return listAt === null || agentsAt === null ? null : Math.min(listAt, agentsAt)
  })
  // ---------- Rows, groups and keyboard order ----------
  const displayRows = computed(() => {
    if (filters.value.status.length === 1 && filters.value.status[0] === 'queued' && !filters.value.sort.length) {
      const positions = new Map(queueSnapshot.value?.items.map((item, i) => [item.ticket_id, i]) ?? [])
      return [...list.rows.value].sort((a, b) => (positions.get(a.id) ?? 0) - (positions.get(b.id) ?? 0))
    }
    const primary = effectiveSort(filters.value)[0]
    return primary?.field === 'state' ? orderByStatus(list.rows.value, primary.desc, liveList.layout) : list.rows.value
  })
  const rowsById = computed(() => new Map(list.rows.value.map(row => [row.id, row])))
  const groups = computed(() => {
    const facet = groupFacet(filters.value.group)
    return groupRows(displayRows.value, filters.value.group, facet ? list.facetCounts(facet) : {}, { me: session.identity?.principal.id, layout: liveList.layout, workName: vocabulary.leaf.name, reversed: !!filters.value.groupDesc })
  })
  // Keyboard order: every visible row once (a ticket under two labels is visited once).
  const sequence = computed(() => {
    if (outlineActive.value) return outline.rows.value
    const out: ListItem[] = []
    const seen = new Set<string>()
    const push = (row: ListItem) => { if (!seen.has(row.id)) { seen.add(row.id); out.push(row) } }
    for (const group of groups.value) {
      const epicRow = group.epic ? rowsById.value.get(group.epic.id) : undefined
      if (epicRow) push(epicRow)
      if (!collapsed.value.has(group.key)) group.rows.forEach(push)
    }
    return out
  })
  const total = computed(() => graphActive.value ? graphState.value.loading ? null : graphState.value.visible.nodes.length : totalFrom(list.facets.value))
  const showAssignee = computed(() => (outlineActive.value ? outline.rows.value : list.rows.value).some(row => row.assignee))

  // One source for the header: the project summary (work counts). It arrives with
  // the project itself and refreshes after a status change.
  const counts = computed(() => {
    const p = project.value
    return p ? { open: p.open, progress: p.in_progress, done: p.done, cancelled: p.cancelled, total: p.total, percent: p.percent } : null
  })
  // The skeleton holds about the height the first page will take, so the hint and
  // footer below the table do not jump when rows arrive.
  const expectedRows = computed(() => {
    const c = counts.value
    if (!c || filtered.value) return 8
    if (outlineActive.value) return 12
    return filters.value.showClosed ? c.total : c.open + c.progress
  })
  const knownStates = computed(() => Object.keys(list.facets.value.state ?? {}))
  const filtered = computed(() => hasFilters(filters.value))

  onBeforeUnmount(() => list.invalidate())

  return { fetchWorkList, list, outline, listActive, liveList, activeLive, liveActive, liveAgents, liveDataStale, liveUpdatedAt, displayRows, rowsById, groups, sequence, total, showAssignee, counts, expectedRows, knownStates, filtered }
}
