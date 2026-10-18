// SPDX-License-Identifier: AGPL-3.0-only
import { api, APIError, getNode, type ListItem } from './api.ts'
import { listModels, type ModelProfile } from './agents.ts'
import { accountName, authorFamilyFor, displayText, fetchAccountCatalog, publicModelId, workRoleFor } from './accountCascade.ts'
import { normaliseState, statusMeta } from './work.ts'
import type { AgentRunRow } from './agentRows.ts'

// A projection of the existing agent run queue. Part A owns its HTTP contract.
export interface QueuedTicket {
  run_id: AgentRunRow['id']; ticket_id: string; key: string; title: string; state: string; priority: string
  position: number; by: { id: string; name: string; kind: 'person' | 'agent' }; at: string
  target_agent_id?: string | null; target_account_id?: string | null
  model_profile_id?: string | null
  expected_agent?: { id: string; name: string } | null
  expected_model?: string | null; expected_effort?: string | null; expected_start?: string | null
  estimate_hours?: number | null; waiting_reason?: string | null
}
export interface QueueSnapshot {
  items: QueuedTicket[]; manual_order: boolean
  capacity: { hours: number | null; total: number; warning?: boolean }
}
export interface QueueTarget {
  agent_id: string; account_id: string; profile_id: string; name: string; account: string; model: string; effort: string
  available: boolean; matches_preference?: boolean
}
export type ReadyGap = 'estimate' | 'criteria' | 'blocker'
export const QUEUE_STATES = ['new', 'open', 'backlog', 'blocked']
type QueueRow = Pick<ListItem, 'kind_slug' | 'state'> & Partial<Pick<ListItem, 'assignee' | 'lead_worker' | 'fields' | 'queue_stale' | 'estimate' | 'is_leaf'>>
export function staleProgress(row: QueueRow): boolean {
  return row.is_leaf !== false && (row.kind_slug === 'ticket' || row.kind_slug === 'task' || row.kind_slug === 'work' && (row.is_leaf ?? row.estimate?.is_parent === false)) && statusMeta(row.state).key === 'progress' && !row.assignee && !row.lead_worker
}
export function staleWork(row: QueueRow): boolean {
  return staleProgress(row) && row.queue_stale === true
}
export function queueable(row: QueueRow): boolean {
  return row.is_leaf !== false && (row.kind_slug === 'ticket' || row.kind_slug === 'task' || row.kind_slug === 'work' && (row.is_leaf ?? row.estimate?.is_parent === false)) && (QUEUE_STATES.includes(normaliseState(row.state)) || staleWork(row))
}
export function queueIneligibleReason(row: QueueRow): string {
  const status = statusMeta(row.state)
  if (status.key === 'progress') {
    const worker = row.assignee?.name ?? row.lead_worker?.name
    return worker || row.queue_stale === false
      ? `Already ${status.label.toLowerCase()} (${worker ?? 'assigned or active work'}), can't be queued.`
      : `${status.label}: queue eligibility has not been confirmed.`
  }
  return `${status.label}: this work can't be queued.`
}
export function readyGaps(row: Pick<ListItem, 'fields' | 'state'>): ReadyGap[] {
  const out: ReadyGap[] = []
  const hours = row.fields.estimate_hours
  if (typeof hours !== 'number' || !Number.isFinite(hours) || hours <= 0 || hours > 200) out.push('estimate')
  const ac = row.fields.acceptance_criteria
  if (!(typeof ac === 'string' && ac.split('\n').some(line => line.trim().replace(/^[- *#\[\]xX\t]+|[- *#\[\]xX\t]+$/g, ''))) && !(Array.isArray(ac) && ac.some(x => typeof x === 'string' && x.trim()))) out.push('criteria')
  const blocker = String(row.fields.blocker ?? row.fields.blocked_by ?? '').trim()
  if (normaliseState(row.state) === 'blocked' && (!blocker || blocker.toLowerCase() === 'unnamed')) out.push('blocker')
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
// Part A's published contract. The run property is deliberately discarded: run
// rows belong to agentRows and its ledger, never this ticket projection.
export interface QueueUndoToken { run_id: string; revision: string }
export interface QueueWireEntry {
  undo?: QueueUndoToken
  node_id: string; project_id?: string | null; key: string; title: string; state: string; priority: string; estimate_hours: number
  queued: { run_id: string; position: number; by: QueuedTicket['by']; at: string; target_agent_id: string | null; expected_agent_id: string | null; model_profile_id: string | null; expected_start_at: string | null; waiting: boolean; wait_reason: string }
}
export interface QueueWireSnapshot {
  items: QueueWireEntry[]; manual_order: boolean
  capacity: { queued_hours: number; parallel_runs: number; work_hours: number | null; warning: boolean }
}
export function queueProjection(raw: QueueWireSnapshot, profiles: ModelProfile[] = [], names: { id: string; name: string }[] = []): QueueSnapshot {
  return { manual_order: raw.manual_order, capacity: { hours: raw.capacity.work_hours, total: raw.capacity.parallel_runs, warning: raw.capacity.warning }, items: raw.items.map(item => {
    const q = item.queued, profile = profiles.find(p => p.id === q.model_profile_id)
    const agent = q.expected_agent_id ? names.find(p => p.id === q.expected_agent_id) : undefined
    return { ticket_id: item.node_id, key: item.key, title: item.title, state: item.state, priority: item.priority, estimate_hours: item.estimate_hours,
      run_id: q.run_id, position: q.position, by: q.by, at: q.at, target_agent_id: q.target_agent_id, model_profile_id: q.model_profile_id,
      expected_agent: q.expected_agent_id ? { id: q.expected_agent_id, name: displayText(agent?.name) ?? 'Assigned agent' } : null,
      expected_model: publicModelId(profile?.model), expected_effort: profile?.effort ?? null, expected_start: q.expected_start_at,
      waiting_reason: q.waiting ? q.wait_reason || 'Waiting for capacity' : null }
  }) }
}
export async function readQueue(project: string): Promise<QueueSnapshot> {
  const raw = await queueRequest<QueueWireSnapshot>(`/queue?project_id=${encodeURIComponent(project)}`)
  if (!raw.items.some(item => item.queued.expected_agent_id || item.queued.model_profile_id)) return queueProjection(raw)
  const [profiles, names] = await Promise.allSettled([listModels(), import('./business.ts').then(module => module.listPrincipals())])
  return queueProjection(raw, profiles.status === 'fulfilled' ? profiles.value : [], names.status === 'fulfilled' ? names.value : [])
}
export const addToQueue = (ticket: string, target?: QueueTarget) => queueRequest<QueueWireEntry>('/queue', 'POST', { node_id: ticket, ...(target ? { agent_principal_id: target.agent_id, model_profile_id: target.profile_id, requested_account_id: target.account_id } : {}) })
export const removeFromQueue = (ticket: string) => queueRequest<void>(`/queue/${encodeURIComponent(ticket)}`, 'DELETE')
export const moveQueue = (ticket: string, position: number) => queueRequest<QueueWireSnapshot>(`/queue/${encodeURIComponent(ticket)}/move`, 'POST', { position })
export const resetQueue = () => queueRequest<QueueWireSnapshot>('/queue/reset', 'POST')
export interface QueueReadiness { stale?: boolean; queueable: boolean; ready: boolean; missing: ('status' | ReadyGap)[]; suggested_estimate_hours: number; security_review_required: boolean }
export const queueReadiness = (ticket: string) => queueRequest<QueueReadiness>(`/queue/${encodeURIComponent(ticket)}/readiness`)
export const applyQueueEstimate = (ticket: string, estimate_hours: number) => queueRequest<QueueReadiness>(`/queue/${encodeURIComponent(ticket)}/estimate`, 'POST', { estimate_hours })
export async function queueTargets(ticket: string): Promise<{ items: QueueTarget[] }> {
  const row = await getNode(ticket)
  const [result, principals] = await Promise.all([fetchAccountCatalog(workRoleFor(row).role, authorFamilyFor(row)), import('./business.ts').then(module => module.listPrincipals()).catch(() => [])])
  if (!result.catalog) throw new Error(result.message)
  return { items: result.catalog.hosts.flatMap(host => host.harnesses.flatMap(harness => harness.accounts.flatMap(account => account.models.flatMap(model => model.efforts.flatMap(effort => {
    const name = displayText(principals.find(p => p.id === account.registered_by_principal_id)?.name) ?? displayText(host.label) ?? 'Agent'
    const modelName = publicModelId(model.model)
    return modelName ? [{ agent_id: account.registered_by_principal_id, account_id: account.id, profile_id: effort.model_profile_id, name,
      account: accountName(account), model: modelName, effort: effort.effort, available: account.available,
      matches_preference: account.default_model_profile_id === effort.model_profile_id }] : []
  }))))) }
}

export const undoQueueAddition = (ticket: string, token: QueueUndoToken) => queueRequest<{ removed: boolean }>(`/queue/${encodeURIComponent(ticket)}/undo`, 'POST', token)

// AEON-740: a parent queues a bounded, permission-checked snapshot of its open leaves.
export type ParentLeafOutcome = 'pending' | 'queued' | 'already_queued' | 'changed' | 'not_ready' | 'active' | 'unavailable'
export interface ParentQueueSnapshot {
  id: string; parent_id: string; state: 'pending' | 'applied' | 'cancelled'; truncated: boolean; continuation_available?: boolean
  partial: boolean; tree_changed: boolean; items: { node_id: string; outcome: ParentLeafOutcome; run_id?: string }[]
}
export const captureParentQueue = (parent: string, revision: string, continuationOf?: string) =>
  queueRequest<ParentQueueSnapshot>(`/queue/${encodeURIComponent(parent)}/snapshots`, 'POST', { expected_revision: revision, ...(continuationOf ? { continuation_of: continuationOf } : {}) })
export const applyParentQueue = (snapshot: string) => queueRequest<ParentQueueSnapshot>(`/queue-snapshots/${encodeURIComponent(snapshot)}/apply`, 'POST')
/** One honest sentence for what a parent queue did, including skips and what is left. */
export function parentQueueSummary(result: ParentQueueSnapshot): string {
  const count = (outcome: ParentLeafOutcome) => result.items.filter(item => item.outcome === outcome).length
  const queued = count('queued'), already = count('already_queued')
  const skipped = result.items.length - queued - already
  const parts = [`${queued} queued`]
  if (already) parts.push(`${already} already queued`)
  if (skipped) parts.push(`${skipped} skipped (changed, not ready, active or unavailable)`)
  if (result.continuation_available) parts.push('more open work remains below; Queue again to continue')
  else if (result.truncated) parts.push('the tree is deeper than 32 levels; deeper work was not included')
  return parts.join(' · ')
}
