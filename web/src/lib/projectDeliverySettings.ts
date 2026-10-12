// SPDX-License-Identifier: AGPL-3.0-only
import { api, APIError } from './api'
import type { MetricSource } from './deliveryNumbers'

export const HISTORY_DAYS = [7, 30, 60, 90] as const
export const DEADLINES = [
  ['reviewed', 'Reviewed, not pushed'], ['pushed', 'Pushed, CI not green'],
  ['ci_green', 'CI green, not queued'], ['in_queue', 'In the merge queue'], ['queue_failed', 'Failed in the queue'],
] as const
export type Deadline = typeof DEADLINES[number][0]
export interface DeliverySettings { required_workflow?: string; required_checks?: string[]; deadlines?: Partial<Record<Deadline, number>> }
export interface SettingsSnapshot { effective: DeliverySettings; overrides: DeliverySettings; revision: string }
export interface RepositoryInput { repository: string; ci_workflow: string; nightly_workflow: string; days: number }
export interface HistoryPosition { state: 'running' | 'done'; since: string; phase: 'runs' | 'pulls' | 'jobs' | 'done'; day: string | null; runs: number; pulls: number; jobs?: number; truncated: boolean; retry_at?: string | null }
const path = (project: string, suffix: string) => `/projects/${encodeURIComponent(project)}/${suffix}`
async function request<T>(url: string, signal?: AbortSignal, init: RequestInit = {}): Promise<{ value: T; revision: string }> {
  const response = await api(url, { ...init, signal })
  if (!response.ok) {
    const body = await response.json().catch(() => ({}))
    throw new APIError(response.status, body.error || 'The delivery setting could not be read or saved.')
  }
  return { value: await response.json() as T, revision: response.headers.get('ETag') ?? '' }
}
export async function readProjectDeliverySettings(project: string, signal?: AbortSignal): Promise<SettingsSnapshot> {
  const url = path(project, 'delivery-settings')
  const [effective, overrides] = await Promise.all([request<DeliverySettings>(url, signal), request<DeliverySettings>(`${url}?view=overrides`, signal)])
  if (!overrides.revision || effective.revision !== overrides.revision) throw new Error('Delivery settings changed while loading. Try again.')
  return { effective: effective.value, overrides: overrides.value, revision: overrides.revision }
}
export async function saveProjectDeliverySettings(project: string, overrides: DeliverySettings, revision: string, signal?: AbortSignal): Promise<SettingsSnapshot> {
  const answer = await request<DeliverySettings>(path(project, 'delivery-settings'), signal, { method: 'PUT', headers: { 'Content-Type': 'application/json', 'If-Match': revision }, body: JSON.stringify(overrides) })
  return { effective: answer.value, overrides, revision: answer.revision }
}
export const linkProjectRepository = (project: string, input: RepositoryInput, signal?: AbortSignal) => request<MetricSource>(path(project, 'delivery/metrics/source'), signal, { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(input) }).then(answer => answer.value)
// One request asks the server job to resume/restart; the page never walks its cursor.
export const readRepositoryHistory = (project: string, days: number, restart: boolean, signal?: AbortSignal) => request<HistoryPosition>(path(project, 'delivery/metrics/backfill'), signal, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ days, restart }) }).then(answer => answer.value)
export const checkRepositoryHistory = (project: string, signal?: AbortSignal) => request<HistoryPosition>(path(project, 'delivery/metrics/check-again'), signal, { method: 'POST' }).then(answer => answer.value)
export type RepositoryState = 'none' | 'reading' | 'done' | 'noactions' | 'appoff' | 'stale' | 'unavailable'
export function repositoryState(source: MetricSource | null, now: number): RepositoryState {
  if (!source) return 'none'
  if (source.last_read_error?.reason === 'actions_read_missing') return 'noactions'
  if (!source.app_connected || source.last_read_error?.reason === 'not_installed') return 'appoff'
  if (source.last_read_error?.reason === 'github_unavailable') return 'unavailable'
  if (source.last_event_at && now - Date.parse(source.last_event_at) > 7 * 86_400_000) return 'stale'
  return source.backfill === 'done' ? 'done' : 'reading'
}
export function workflowFile(value: string): string { return value.replace(/^\.github\/workflows\//, '') }
