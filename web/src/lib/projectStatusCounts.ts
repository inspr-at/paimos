// SPDX-License-Identifier: AGPL-3.0-only
import type { ProjectSummary, WorkCountBucket } from './api.ts'
import { normaliseState } from './work.ts'

export const HEADER_STATUS_GROUPS = [
  { id: 'open', label: 'Open', icon: 'open', states: ['new', 'backlog', 'open', 'blocked'] },
  { id: 'in_progress', label: 'Doing', icon: 'in_progress', states: ['in_progress', 'qa'] },
  { id: 'done', label: 'Done', icon: 'done', states: ['done', 'delivered', 'accepted'] },
  { id: 'closed', label: 'Closed', icon: 'cancelled', states: ['cancelled', 'archived'] },
] as const
export type HeaderStatusGroup = typeof HEADER_STATUS_GROUPS[number]

export function canonicalStatus(state: string): string {
  const normal = normaliseState(state)
  return ['active', 'inprogress'].includes(normal) ? 'in_progress' : normal === 'canceled' ? 'cancelled' : normal
}
export function fallbackBucket(state: string): WorkCountBucket {
  const canonical = canonicalStatus(state)
  if (['done', 'delivered', 'accepted'].includes(canonical)) return 'done'
  if (['in_progress', 'qa'].includes(canonical)) return 'in_progress'
  if (canonical === 'cancelled' || canonical === 'archived') return canonical
  return 'open'
}
export function groupTotal(summary: ProjectSummary, id: HeaderStatusGroup['id']): number {
  return id === 'closed' ? (summary.cancelled ?? 0) + (summary.archived_count ?? Math.max(0, summary.total - summary.open - summary.in_progress - summary.done - (summary.cancelled ?? 0)))
    : summary[id]
}
export function statusCount(summary: ProjectSummary, state: string): number | null {
  if (!summary.status_counts || summary.status_counts_truncated) return null
  return summary.status_counts.filter(item => item.state === state).reduce((sum, item) => sum + item.count, 0)
}
export function groupStates(summary: ProjectSummary, group: HeaderStatusGroup): string[] {
  const buckets = group.id === 'closed' ? ['cancelled', 'archived'] : [group.id]
  return [...new Set([...group.states, ...(summary.status_counts ?? []).filter(item => buckets.includes(item.bucket)).map(item => item.state)])]
}
export function reconcileStatusHide(showClosed: boolean, automatic: boolean, hiddenSelection: boolean) {
  if (!showClosed && hiddenSelection) return { showClosed: true, automatic: true, note: 'Hide is off to show the selected statuses.' }
  if (automatic && !hiddenSelection) return { showClosed: false, automatic: false, note: 'Hide is on again.' }
  return { showClosed, automatic, note: '' }
}
