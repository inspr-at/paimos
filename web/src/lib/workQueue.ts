// SPDX-License-Identifier: AGPL-3.0-only
import { api, APIError, type ListItem } from './api.ts'
import type { AgentRunRow } from './agentRows.ts'

// A projection of the existing agent run queue. Part A owns its HTTP contract.
export interface QueuedTicket {
  run_id: AgentRunRow['id']; ticket_id: string; key: string; title: string; state: string; priority: string
  position: number; by: { id: string; name: string; kind: 'person' | 'agent' }; at: string
  target_agent_id?: string | null; target_account_id?: string | null
  expected_agent?: { id: string; name: string } | null
  expected_model?: string | null; expected_effort?: string | null; expected_start?: string | null
  estimate_hours?: number | null; waiting_reason?: string | null
}
export interface QueueSnapshot {
  items: QueuedTicket[]; manual_order: boolean
  capacity: { hours: number | null; busy: number; total: number }
}
export interface QueueTarget {
  agent_id: string; account_id?: string; name: string; model: string; effort: string
  busy: number; slots: number; matches_preference?: boolean
}
export type ReadyGap = 'estimate' | 'criteria' | 'blocker'
export const QUEUE_STATES = ['new', 'open', 'backlog', 'blocked']
export function queueable(row: Pick<ListItem, 'kind_slug' | 'state'>): boolean {
  return (row.kind_slug === 'ticket' || row.kind_slug === 'task') && QUEUE_STATES.includes(row.state)
}
export function readyGaps(row: Pick<ListItem, 'fields' | 'state'>): ReadyGap[] {
  const out: ReadyGap[] = []
  const hours = row.fields.estimate_hours
  if (typeof hours !== 'number' || !Number.isFinite(hours) || hours <= 0) out.push('estimate')
  const ac = row.fields.acceptance_criteria
  if (!(typeof ac === 'string' && ac.trim()) && !(Array.isArray(ac) && ac.some(x => typeof x === 'string' && x.trim()))) out.push('criteria')
  if (row.state === 'blocked' && !String(row.fields.blocker ?? row.fields.blocked_by ?? '').trim()) out.push('blocker')
  return out
}
export function queueHours(snapshot?: QueueSnapshot | null): string {
  const hours = snapshot?.capacity.hours
  return typeof hours === 'number' && Number.isFinite(hours) ? `~${Math.max(1, Math.round(hours))} h of work at current capacity` : 'Capacity estimate is not available yet'
}
export function movedQueue(ids: string[], id: string, direction: -1 | 1 | 'top'): string[] {
  const from = ids.indexOf(id), to = direction === 'top' ? 0 : from + direction
  if (from < 0 || to < 0 || to >= ids.length || to === from) return ids
  const next = [...ids]; next.splice(from, 1); next.splice(to, 0, id); return next
}
export async function queueRequest<T>(path: string, method = 'GET', body?: unknown): Promise<T> {
  const response = await api(path, { method, ...(body === undefined ? {} : { headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) }) })
  if (!response.ok) {
    const data = await response.json().catch(() => ({}))
    throw new APIError(response.status, typeof data.error === 'string' ? data.error : `The work queue could not be updated (${response.status})`, data)
  }
  return response.status === 204 ? undefined as T : response.json()
}
export const readQueue = (project: string) => queueRequest<QueueSnapshot>(`/queue?project_id=${encodeURIComponent(project)}`)
export const addToQueue = (ticket: string, target?: QueueTarget) => queueRequest<QueuedTicket>('/queue', 'POST', { ticket_id: ticket, ...(target ? { agent_id: target.agent_id, account_id: target.account_id, start_now: true } : {}) })
export const removeFromQueue = (ticket: string) => queueRequest<void>(`/queue/${encodeURIComponent(ticket)}`, 'DELETE')
export const reorderQueue = (project: string, ticket_ids: string[]) => queueRequest<void>('/queue/order', 'PUT', { project_id: project, ticket_ids })
export const resetQueue = (project: string) => queueRequest<void>('/queue/reset', 'POST', { project_id: project })
export const queueTargets = (ticket: string) => queueRequest<{ items: QueueTarget[] }>(`/queue/targets?ticket_id=${encodeURIComponent(ticket)}`)
export const fixQueueReady = (ticket: string, fix: ReadyGap, blocker?: string) => queueRequest<ListItem>(`/queue/${encodeURIComponent(ticket)}/ready`, 'POST', { fix, ...(blocker ? { blocker } : {}) })
