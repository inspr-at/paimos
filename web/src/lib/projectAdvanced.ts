// SPDX-License-Identifier: AGPL-3.0-only
import { api, APIError } from './api'

export type ShadowMode = 'off' | 'shadow'
export interface ShadowSettings { mode: ShadowMode; revision: number }
export interface QueueSettings extends ShadowSettings { freeze: boolean; release_set: string[]; held_slugs: string[]; held_pull_requests: number[] }
export interface AdmissionSettings { project_id: string; shadow_enabled: boolean; revision: number }
export interface AdmissionSettingsInput { expected_revision: number; shadow_enabled: boolean }
export interface MergeBackfillInput { pull_requests: number[]; target: 'delivered'; release_id: string; apply?: boolean; preview_event_id?: number }
export interface MergeBackfillItem {
  ticket_node_id: string; key: string; project_id: string | null; pull_request: number
  revision: string; from: string; to: 'done' | 'delivered'
  result: 'ready' | 'refused' | 'already_complete' | 'already_applied' | 'applied'; reason?: string
}
export interface MergeBackfillResult { preview_event_id: number; applied: boolean; items: MergeBackfillItem[]; not_merged: number[] }
export type Machine = 'queue' | 'reviews' | 'shipping' | 'routing' | 'admission'
export const MACHINE_PATHS: Record<Machine, string> = {
  queue: 'delivery-queue/settings', reviews: 'delivery-reviews/settings', shipping: 'delivery-shipping/settings', routing: 'routing-settings', admission: 'admission-settings',
}
export async function advancedRequest<T>(project: string, path: string, signal: AbortSignal, body?: unknown, method = body === undefined ? 'GET' : 'PUT'): Promise<T> {
  const response = await api(`/projects/${encodeURIComponent(project)}/${path}`, {
    method, signal, ...(body === undefined ? {} : { headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) }),
  }, path === 'delivery/merge-backfill' ? 120_000 : 10_000)
  if (!response.ok) {
    const data = await response.json().catch(() => ({}))
    throw new APIError(response.status, typeof data.error === 'string' ? data.error : typeof data.message === 'string' ? data.message : `Request failed (${response.status})`, data)
  }
  return response.json() as Promise<T>
}
// Reject before splitting or sending work: never silently deduplicate or truncate.
export function pullRequestNumbers(text: string, optional = false): number[] {
  if (text.length > 2400) throw new Error('Enter at most 100 pull request numbers.')
  if (!text.trim() && optional) return []
  const tokens = text.trim().split(/[\s,]+/)
  if (!text.trim() || tokens.length > 100 || tokens.some(token => !/^#?[1-9][0-9]*$/.test(token))) throw new Error('Enter 1 to 100 pull request numbers, separated by commas or spaces.')
  const numbers = tokens.map(token => Number(token.replace(/^#/, '')))
  if (numbers.some(number => !Number.isSafeInteger(number)) || new Set(numbers).size !== numbers.length) throw new Error('Use unique pull request numbers within the supported range.')
  return numbers.sort((a, b) => a - b)
}
export function queueSlugs(text: string): string[] {
  if (text.length > 12_100) throw new Error('Enter at most 100 branch slugs, each at most 120 characters.')
  const slugs = text.trim() ? text.trim().split(/[\s,]+/) : []
  if (slugs.length > 100 || slugs.some(slug => slug.length > 120 || !/^[a-z0-9][a-z0-9-]*$/.test(slug)) || new Set(slugs).size !== slugs.length) throw new Error('Use up to 100 unique branch slugs: lowercase letters, digits and hyphens.')
  return slugs
}
