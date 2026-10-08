// SPDX-License-Identifier: AGPL-3.0-only
import { api, APIError } from './api.ts'
import type { ReviewFamily } from './reviews.ts'
export type ReviewPolicyMode = 'off' | 'other_family' | 'allowlist'
export interface ReviewPolicy { mode: ReviewPolicyMode; allowed_families: ReviewFamily[] }
export interface ReviewPolicySettings {
  project_id: string | null; policy: ReviewPolicy | null; effective: ReviewPolicy; workspace: ReviewPolicy
  valid_families: ReviewFamily[]; source: 'default' | 'tenant' | 'project'; updated_by: string | null; updated_at: string | null
}
const pathFor = (id: string) => id ? `/projects/${encodeURIComponent(id)}/review-policy` : '/settings/review-policy'
async function request(id: string, method: string, body?: ReviewPolicy, signal?: AbortSignal, revision?: string | null): Promise<ReviewPolicySettings> {
  const response = await api(pathFor(id), { method, signal, headers: { ...(body ? { 'Content-Type': 'application/json' } : {}), ...(method !== 'GET' ? { 'If-Unmodified-Since': revision ?? 'none' } : {}) }, ...(body ? { body: JSON.stringify(body) } : {}) })
  if (!response.ok) { const data = await response.json().catch(() => ({})); throw new APIError(response.status, data.error || 'The review rule could not be loaded or saved.') }
  return response.json()
}
export const readReviewPolicy = (id: string, signal?: AbortSignal) => request(id, 'GET', undefined, signal)
export const saveReviewPolicy = (id: string, body: ReviewPolicy, signal?: AbortSignal, revision?: string | null) => request(id, 'PUT', body, signal, revision)
export const resetReviewPolicy = (id: string, signal?: AbortSignal, revision?: string | null) => request(id, 'DELETE', undefined, signal, revision)
export const samePolicy = (a: ReviewPolicy | null, b: ReviewPolicy | null) => a === null || b === null ? a === b : a.mode === b.mode && [...a.allowed_families].sort().join(',') === [...b.allowed_families].sort().join(',')
