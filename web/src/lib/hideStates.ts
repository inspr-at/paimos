// SPDX-License-Identifier: AGPL-3.0-only
import type { ProjectStatusCount, ProjectSummary, WorkCountBucket } from './api.ts'
import { canonicalStatus, fallbackBucket } from './projectStatusCounts.ts'
import type { ListFilters } from './ticketList.ts'

export const HIDE_STATES = ['done', 'delivered', 'accepted', 'cancelled', 'archived'] as const
export type HideState = typeof HIDE_STATES[number]
export function hiddenStates(values?: readonly string[]): HideState[] {
  const chosen = HIDE_STATES.filter(state => values?.some(value => canonicalStatus(value) === state))
  return chosen.length ? chosen : [...HIDE_STATES]
}
export function parseHiddenStates(raw: unknown): HideState[] {
  // View/URL inputs are bounded before splitting, like the API selector.
  if (typeof raw !== 'string' || raw.length > 325) return [...HIDE_STATES]
  const values = raw.split(',')
  return values.length <= 5 && values.every(value => HIDE_STATES.includes(canonicalStatus(value) as HideState)) ? hiddenStates(values) : [...HIDE_STATES]
}
export function hideLabel(values?: readonly string[]) {
  const states = hiddenStates(values)
  return states.length === 5 ? 'Hide closed' : states.join(',') === 'done,delivered,accepted' ? 'Hide finished' : 'Hide'
}
export function hideChoice(state: string, bucket: WorkCountBucket): HideState | null {
  const canonical = canonicalStatus(state)
  if (bucket === 'done') return canonical === 'delivered' || canonical === 'accepted' ? canonical : 'done'
  return bucket === 'cancelled' || bucket === 'archived' ? bucket : null
}
export function statusHiddenByPolicy(state: string, counts: readonly ProjectStatusCount[] | undefined, values?: readonly string[], truncated = false): boolean {
  if (truncated) return true
  const selected = hiddenStates(values)
  const entries = counts?.filter(item => item.state === canonicalStatus(state)) ?? []
  return (entries.length ? entries : [{ state, bucket: fallbackBucket(state) }]).some(item => {
    const choice = hideChoice(item.state, item.bucket)
    return choice !== null && selected.includes(choice)
  })
}
export function selectionHiddenByPolicy(status: string[], scope: ListFilters['statusScope'], counts: readonly ProjectStatusCount[] | undefined, values?: readonly string[], truncated = false): boolean {
  if (!status.length) return false
  const selected = hiddenStates(values)
  if (scope && scope !== 'canonical') return scope === 'closed' ? selected.some(state => state === 'cancelled' || state === 'archived')
    : scope === 'done' && selected.some(state => ['done', 'delivered', 'accepted'].includes(state))
  return status.some(state => !state.startsWith('!') && statusHiddenByPolicy(state, counts, values, truncated))
}
export function hideChoiceCounts(summary?: ProjectSummary | null): Record<HideState, number> | null {
  if (!summary?.status_counts || summary.status_counts_truncated) return null
  const result = { done: 0, delivered: 0, accepted: 0, cancelled: 0, archived: 0 }
  for (const item of summary.status_counts) {
    const choice = hideChoice(item.state, item.bucket)
    if (choice) result[choice] += item.count
  }
  return result
}
