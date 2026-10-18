// SPDX-License-Identifier: AGPL-3.0-only
import { api } from './api'

export type ActivityView = 'everything' | 'automatic' | 'people' | 'agents'
export interface WorkspaceActivityItem {
  event_id: number; node_id: string | null; project_id: string | null; key: string; title: string; actor: string; type: string
  rule: string; reason: string; from: string; to: string; at: string; revision: string | null
  automatic: boolean; undone: boolean; changed_since: boolean; undoable: boolean; requires_preview: boolean
}
export interface WorkspaceActivityFilters { view: ActivityView; rule: string; project_id: string; q: string }
export interface WorkspaceActivityPage { items: WorkspaceActivityItem[]; next_cursor: string | null }
export class ActivityRequestError extends Error {
  readonly status: number
  constructor(status: number, message: string) { super(message); this.status = status }
}
export async function getWorkspaceActivity(filters: WorkspaceActivityFilters, cursor: string | null, signal: AbortSignal): Promise<WorkspaceActivityPage> {
  if (new TextEncoder().encode(filters.q).length > 200) throw new ActivityRequestError(400, 'Search is too long. Shorten it to load Activity.')
  const query = new URLSearchParams({ view: filters.view, limit: '50' })
  for (const key of ['rule', 'project_id', 'q'] as const) if (filters[key]) query.set(key, filters[key])
  if (cursor) query.set('cursor', cursor)
  const response = await api(`/events/activity?${query}`, { signal })
  if (!response.ok) throw new ActivityRequestError(response.status, response.status === 403 ? 'Activity is not available with the current permissions.' : 'Activity could not be loaded. Retry the same page.')
  return response.json() as Promise<WorkspaceActivityPage>
}
export async function undoActivity(item: Pick<WorkspaceActivityItem, 'event_id'>, signal: AbortSignal): Promise<void> {
  const response = await api(`/events/${item.event_id}/undo`, { method: 'POST', signal })
  if (!response.ok) throw new ActivityRequestError(response.status, response.status === 409 ? 'The ticket changed since this move. Undo was not applied.' : response.status === 403 ? 'Undo is no longer permitted. The ticket was not changed.' : 'Undo could not be confirmed. Reload Activity before trying again.')
}
