// SPDX-License-Identifier: AGPL-3.0-only
// Time in each status (AEON-1369 API, AEON-1359 UI): the ticket lifeline and Delivery › Tickets, both computed by the
// server from the event log. Durations are seconds, p50/p90 nearest rank. A failed read never stands in as figures.
import { api, APIError } from './api.ts'

// ---------- Contract (api/areas/projects-status-timing.yaml) ----------
export interface StatusTimingActor { name: string; kind: 'agent' | 'person' | 'unknown' }
export interface StatusTimingVisit {
  status: string; from: string; to: string | null; seconds: number
  moved_by: StatusTimingActor | null; next_status: string | null; ended_by: StatusTimingActor | null
  release: string | null; source: 'event' | 'imported' | 'inferred'; again: boolean; ongoing: boolean
}
export interface TicketLifeline {
  node_id: string; generated_at: string; visits: StatusTimingVisit[]
  complete: boolean; inferred: boolean; gap_count: number; initial_offset_seconds: number
}
export type StatusTimingStepKey = 'created_to_live' | 'created_to_done' | 'new_to_picked_up' | 'in_progress' | 'qa_to_merged' | 'done_to_live' | 'live_to_accepted'
export interface StatusTimingStep { step: StatusTimingStepKey; n: number; p50: number | null; p90: number | null; prev_n: number; prev_p50: number | null; prev_p90: number | null }
export interface StatusTimingBucket { from: string; to: string; state: 'observed' | 'partial' | 'no_data'; n: number; p50: number | null; p90: number | null }
export interface TicketStatusTiming {
  project_id: string; generated_at: string; window: number; final_status: 'done' | 'delivered'
  steps: StatusTimingStep[]; rework: number
  coverage: { tickets: number; complete: number; inferred: number; gaps: number; since: string | null; partial: boolean }
  buckets: StatusTimingBucket[]
}
export type TimingType = 'all' | 'bug' | 'other'
export type TimingPriority = 'all' | 'high' | 'medium' | 'low'
export type TimingWorkedBy = 'all' | 'agent' | 'person'
export interface TimingFilters { type: TimingType; priority: TimingPriority; worked_by: TimingWorkedBy }
export const NO_FILTERS: TimingFilters = { type: 'all', priority: 'all', worked_by: 'all' }

async function read<T>(path: string, signal?: AbortSignal): Promise<T> {
  const response = await api(path, { signal })
  if (!response.ok) {
    const data = await response.json().catch(() => ({}))
    throw new APIError(response.status, typeof data?.error === 'string' && data.error ? data.error : `Request failed (${response.status})`)
  }
  return response.json() as Promise<T>
}

export async function readLifeline(nodeId: string, signal?: AbortSignal): Promise<TicketLifeline> {
  const body = await read<TicketLifeline>(`/nodes/${encodeURIComponent(nodeId)}/lifeline`, signal)
  if (!body || body.node_id !== nodeId || !Array.isArray(body.visits) || !body.visits.length) throw new APIError(200, 'Invalid lifeline response')
  return body
}

export async function readTicketTiming(projectId: string, window: number, filters: TimingFilters = NO_FILTERS, signal?: AbortSignal): Promise<TicketStatusTiming> {
  const query = new URLSearchParams({ window: String(window), type: filters.type, priority: filters.priority, worked_by: filters.worked_by })
  const body = await read<TicketStatusTiming>(`/projects/${encodeURIComponent(projectId)}/delivery/tickets?${query}`, signal)
  if (!body || body.project_id !== projectId || !Array.isArray(body.steps) || !Array.isArray(body.buckets)) throw new APIError(200, 'Invalid ticket timing response')
  return body
}

export const stepOf = (timing: TicketStatusTiming | null, key: StatusTimingStepKey): StatusTimingStep | null => timing?.steps.find(step => step.step === key) ?? null

// ---------- The lifeline's typicals: Delivery › Tickets at 90 days, no filters, cached per person and project ----------
// One definition everywhere (decision 21). Only answers are kept; a failed read is asked again next time. The key
// carries the signed-in person and workspace, so another identity never sees an answer read for someone else.
const TYPICALS_TTL = 5 * 60_000
const typicals = new Map<string, { at: number; answer: Promise<TicketStatusTiming> }>()
export function readTypicals(owner: string, projectId: string, now = Date.now()): Promise<TicketStatusTiming> {
  const key = `${owner}|${projectId}`, kept = typicals.get(key)
  if (kept && now - kept.at < TYPICALS_TTL) return kept.answer
  const answer = readTicketTiming(projectId, 90)
  typicals.set(key, { at: now, answer })
  answer.catch(() => { if (typicals.get(key)?.answer === answer) typicals.delete(key) })
  return answer
}
/** Delivery › Tickets at 90 days with no filters is the same answer: keep it for the lifelines. */
export function keepTypicals(owner: string, timing: TicketStatusTiming, filters: TimingFilters, now = Date.now()) {
  if (timing.window === 90 && filters.type === 'all' && filters.priority === 'all' && filters.worked_by === 'all') typicals.set(`${owner}|${timing.project_id}`, { at: now, answer: Promise.resolve(timing) })
}
