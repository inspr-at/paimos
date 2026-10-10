// SPDX-License-Identifier: AGPL-3.0-only
import { api, APIError } from './api'
import { preferenceFailure } from './modelsSettings'
import type { ModelPreferences, ModelSelector, PrefLevel, PrefScope, PrefWriteResult, TicketModelResolution, WorkKind } from './modelPrefs'
export async function preferenceRequest<T>(path: string, method = 'GET', body?: unknown, signal?: AbortSignal, person?: string | null): Promise<T> {
  const response = await api(path, { method, signal, headers: { ...(body === undefined ? {} : { 'Content-Type': 'application/json' }), ...(person ? { 'If-Prefs-Person': person } : {}) }, ...(body === undefined ? {} : { body: JSON.stringify(body) }) })
  if (!response.ok) { const data = await response.json().catch(() => ({})); throw new APIError(response.status, [409, 428].includes(response.status) ? preferenceFailure(response.status) : data.message || data.error || `Request failed (${response.status})`, data) }
  return response.json()
}
const query = (project?: string, revision?: number) => { const q = new URLSearchParams(); if (project) q.set('project_id', project); if (revision !== undefined) q.set('revision', String(revision)); return q.size ? `?${q}` : '' }
const scopePath = (level: PrefLevel, kind?: string) => `/model-preferences/levels/${level}${kind ? `/rows/${encodeURIComponent(kind)}` : ''}`
export const getModelPreferences = (project?: string, signal?: AbortSignal) => preferenceRequest<ModelPreferences>(`/model-preferences${query(project)}`, 'GET', undefined, signal)
export const putPreferenceScope = (level: PrefLevel, body: Partial<PrefScope> & { revision: number }, project?: string, person?: string | null) => preferenceRequest<PrefWriteResult>(scopePath(level) + query(project), 'PUT', body, undefined, person)
export const putPreferenceRow = (level: PrefLevel, kind: string, body: { revision: number; locked?: boolean; normal?: ModelSelector; complex?: ModelSelector }, project?: string, person?: string | null) => preferenceRequest<PrefWriteResult>(scopePath(level, kind) + query(project), 'PUT', body, undefined, person)
export const resetPreference = (level: PrefLevel, revision: number, project?: string, kind?: string, person?: string | null) => preferenceRequest<PrefWriteResult>(scopePath(level, kind) + query(project, revision), 'DELETE', undefined, undefined, person)
export const createWorkKind = (label: string, project?: string) => preferenceRequest<WorkKind>('/work-kinds', 'POST', { label, ...(project ? { project_id: project } : {}) })
export const archiveWorkKind = (kind: string) => preferenceRequest<WorkKind>(`/work-kinds/${encodeURIComponent(kind)}`, 'DELETE')
export const getTicketModelResolution = (ticket: string, signal?: AbortSignal) => preferenceRequest<TicketModelResolution>(`/models/resolve?${new URLSearchParams({ ticket })}`, 'GET', undefined, signal)
