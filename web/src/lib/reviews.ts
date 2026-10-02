// SPDX-License-Identifier: AGPL-3.0-only
import { api, APIError } from './api.ts'

export type ReviewFamily = 'openai' | 'anthropic' | 'xai' | 'cursor' | 'google' | 'local'
export interface ReviewFinding { severity: 'critical' | 'high' | 'medium' | 'low'; file: string; line: number; message: string }
export interface TicketReview {
  work_order_id: string; request_id: string; ticket_node_id: string; repository: string
  base_sha: string; head_sha: string; author_family: ReviewFamily; author_run_id: string | null
  reviewer_profile_id: string | null; reviewer_family: ReviewFamily | null; run_id: string | null
  reviewer_model: string | null; reviewer_effort: string | null; reviewer_profile_version: string | null
  effective_model: string | null; status: string; gate_open: boolean; gate_reason: string
  result: { verdict: '' | 'ok' | 'changes'; findings: ReviewFinding[]; reason: string }
  ladder: { profile_id: string; selected: boolean; skip_reasons: string[] }[]
  cost_micros: number | null; duration_seconds: number | null; created_at: string
  pull_request: number | null; github_status: 'unconfigured' | 'pending' | 'success' | 'failure' | 'error' | 'stale'
}
export interface ReviewRequest {
  request_id: string; repository: string; base_sha: string; head_sha: string
  author_family?: ReviewFamily; author_run_id?: string; pull_request?: number
}
async function reviewRequest<T>(nodeId: string, method: string, body?: ReviewRequest, signal?: AbortSignal): Promise<T> {
  const response = await api(`/nodes/${encodeURIComponent(nodeId)}/reviews`, {
    method, signal, ...(body ? { headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) } : {}),
  })
  if (!response.ok) {
    const result = await response.json().catch(() => ({}))
    throw new APIError(response.status, result.error || 'Review could not be loaded.')
  }
  return response.json()
}
export const listReviews = (id: string, signal?: AbortSignal) => reviewRequest<TicketReview[]>(id, 'GET', undefined, signal)
export const requestReview = (id: string, body: ReviewRequest, signal?: AbortSignal) => reviewRequest<TicketReview>(id, 'POST', body, signal)
export const familyName = (family: ReviewFamily) => ({ openai: 'Codex', anthropic: 'Claude', xai: 'Grok', cursor: 'Cursor', google: 'Google', local: 'Local' })[family]
export function reviewLabel(review: TicketReview): string {
  if (review.gate_open) return 'Passed'
  if (review.result.verdict === 'changes' && review.status === 'completed') return 'Changes needed'
  if (review.status === 'completed') return 'No valid verdict'
  return ({ queued: 'Queued', starting: 'Starting', running: 'Reviewing', waiting: 'Waiting', blocked: 'Reviewer unavailable', cancelled: 'Cancelled', failed: 'Review failed', ownership_lost: 'Lost contact' } as Record<string, string>)[review.status] ?? 'Gate closed'
}
export function reviewUsage(review: TicketReview): string {
  const parts: string[] = []
  if (review.duration_seconds !== null) {
    const seconds = review.duration_seconds
    parts.push(seconds < 60 ? `${seconds}s` : `${Math.floor(seconds / 60)}m ${seconds % 60}s`)
  }
  if (review.cost_micros !== null) parts.push(new Intl.NumberFormat('en', { style: 'currency', currency: 'USD', maximumFractionDigits: 3 }).format(review.cost_micros / 1_000_000))
  return parts.join(' · ')
}
export function reviewSkips(review: TicketReview): string[] {
  return [...new Set(review.ladder.flatMap(step => step.skip_reasons))]
}
