// SPDX-License-Identifier: AGPL-3.0-only
import { api, APIError } from './api'
import { policyJSON } from './policyEditor'
import type { OrderBody, RegistryProfile, RulesBody, RulesDocument, Scope, SimpleDocument, TailBoard, WriteResult } from './modelsSimple'

async function request<T>(path: string, method = 'GET', body?: unknown, person?: string | null, signal?: AbortSignal): Promise<T> {
  const response = await api(path, { method, signal, headers: { ...(body !== undefined ? { 'Content-Type': 'application/json' } : {}), ...(person ? { 'If-Prefs-Person': person } : {}) }, ...(body !== undefined ? { body: JSON.stringify(body) } : {}) })
  if (!response.ok) {
    const data = await policyJSON<{ message?: string; error?: string }>(response, 64 * 1024).catch(() => ({} as { message?: string; error?: string }))
    throw new APIError(response.status, data.message || data.error || `Request failed (${response.status})`, data)
  }
  return policyJSON<T>(response)
}
const first = (column: string, tail = '') => `/model-preferences/orders/${encodeURIComponent(column)}/first${tail}`
// Personal writes carry the person the page was showing; the server refuses them if the caller changed.
const who = (scope: Scope, person: string | null) => scope === 'me' ? person : undefined

export const getSimple = (scope: Scope, signal?: AbortSignal) => request<SimpleDocument>(`/model-preferences/simple?for=${scope}`, 'GET', undefined, undefined, signal)
export const getTail = (scope: Scope, signal?: AbortSignal) => request<TailBoard>(`/model-preferences/board?${new URLSearchParams({ layer: scope === 'me' ? 'mine' : 'default', situation: 'first' })}`, 'GET', undefined, undefined, signal)
export const getRegistry = async (signal?: AbortSignal) => {
  const profiles = await request<RegistryProfile[]>('/models', 'GET', undefined, undefined, signal)
  if (!Array.isArray(profiles)) throw new Error('Invalid model catalog')
  return profiles
}
export const getWorkspaceRules = (signal?: AbortSignal) => request<RulesDocument>('/model-rules', 'GET', undefined, undefined, signal)
export const putOrder = (scope: Scope, column: string, body: OrderBody, revision: number, person: string | null) => request<WriteResult>(`${first(column)}?for=${scope}`, 'PUT', { ...body, revision }, who(scope, person))
export const resetOrder = (scope: Scope, column: string, revision: number, person: string | null) => request<WriteResult>(`${first(column)}?for=${scope}&revision=${revision}`, 'DELETE', undefined, who(scope, person))
export const putEffort = (scope: Scope, column: string, effort: string | null, revision: number, person: string | null) => request<WriteResult>(`${first(column, '/thinking')}?for=${scope}`, 'PUT', { effort, revision }, who(scope, person))
export const putWorkspaceRules = (column: string, body: RulesBody, revision: number) => request<RulesDocument>(`/model-rules/workspace/${encodeURIComponent(column)}`, 'PUT', { ...body, revision })
export const dismissLine = (scope: Scope, line: string, revision: number, person: string | null) => request<WriteResult>(`/model-preferences/tray/${encodeURIComponent(line)}/dismiss?for=${scope}`, 'POST', { revision }, who(scope, person))
export const putDismissed = (scope: Scope, lines: string[], revision: number, person: string | null) => request<WriteResult>(`/model-preferences/profile?for=${scope}`, 'PUT', { dismissed_lines: lines, revision }, who(scope, person))
