// SPDX-License-Identifier: AGPL-3.0-only
import { api, APIError } from './api'
import type { BoardContext, BoardProfile, BoardWriteResult, ModelBoardDocument, OrderBody, RulesBody, RulesDocument, ThinkingWord } from './modelsBoard'
import type { BoardCoverage, BoardEvidence, ModelResolution } from './modelsSettings'
import type { PrefProfile } from './modelPrefs'
import { policyJSON, policyRequest } from './policyEditor'
async function request<T>(path: string, method = 'GET', body?: unknown, person?: string | null, signal?: AbortSignal): Promise<T> {
  const response = await api(path, { method, signal, headers: { ...(body !== undefined ? { 'Content-Type': 'application/json' } : {}), ...(person ? { 'If-Prefs-Person': person } : {}) }, ...(body !== undefined ? { body: JSON.stringify(body) } : {}) })
  if (!response.ok) { const data = await policyJSON<{ message?: string; error?: string }>(response, 64 * 1024).catch(() => ({} as { message?: string; error?: string })); throw new APIError(response.status, data.message || data.error || `Request failed (${response.status})`, data) }
  return policyJSON<T>(response)
}
export const getBoard = (context: BoardContext, signal?: AbortSignal) => request<ModelBoardDocument>(`/model-preferences/board?${new URLSearchParams({ layer: context.layer, situation: context.situation, ...(context.project ? { project_id: context.project } : {}) })}`, 'GET', undefined, undefined, signal)
export const getBoardRules = (context: BoardContext, signal?: AbortSignal) => request<RulesDocument>(`/model-rules${context.project ? `?project_id=${encodeURIComponent(context.project)}` : ''}`, 'GET', undefined, undefined, signal)
const forQuery = (context: BoardContext) => context.layer === 'mine' ? 'me' : 'default'
const orderPath = (context: BoardContext, column: string, thinking = false) => `/model-preferences/orders/${encodeURIComponent(column)}/${context.situation}${thinking ? '/thinking' : ''}?for=${forQuery(context)}`
export const putColumnThinking = (context: BoardContext, column: string, thinking: ThinkingWord | null, revision: number, person: string | null) => request<BoardWriteResult>(orderPath(context, column, true), 'PUT', { thinking, revision }, context.layer === 'mine' ? person : undefined)
export const putBoardOrder = (context: BoardContext, column: string, body: OrderBody, revision: number, person: string | null) => request<BoardWriteResult>(orderPath(context, column), 'PUT', { ...body, revision }, context.layer === 'mine' ? person : undefined)
export const resetBoardOrder = (context: BoardContext, column: string, revision: number, person: string | null) => request<BoardWriteResult>(`${orderPath(context, column)}&revision=${revision}`, 'DELETE', undefined, context.layer === 'mine' ? person : undefined)
export const putBoardProfile = (context: BoardContext, body: Partial<BoardProfile> & { replace_own?: boolean }, revision: number, person: string | null, dryRun = false) => request<BoardWriteResult>(`/model-preferences/profile?for=${forQuery(context)}${dryRun ? '&dry_run=true' : ''}`, 'PUT', { ...body, revision }, context.layer === 'mine' ? person : undefined)
export const putBoardRules = (context: BoardContext, column: string, body: RulesBody, revision: number) => request<RulesDocument>(`/model-rules/${context.project ? 'project' : 'workspace'}/${encodeURIComponent(column)}${context.project ? `?project_id=${encodeURIComponent(context.project)}` : ''}`, 'PUT', { ...body, revision })
export const dismissBoardLine = (context: BoardContext, line: string, revision: number, person: string | null) => request<BoardWriteResult>(`/model-preferences/tray/${encodeURIComponent(line)}/dismiss?for=${forQuery(context)}`, 'POST', { revision }, context.layer === 'mine' ? person : undefined)
export const getBoardCoverage = (signal?: AbortSignal) => request<BoardCoverage>('/model-preferences/coverage', 'GET', undefined, undefined, signal)
export const getBoardEvidence = (kind: string, project?: string, cursor?: string, signal?: AbortSignal) => request<BoardEvidence>(`/model-preferences/evidence?${new URLSearchParams({ limit: '50', ...(kind ? { kind } : {}), ...(project ? { project_id: project } : {}), ...(cursor ? { cursor } : {}) })}`, 'GET', undefined, undefined, signal)
export const resolveBoardModel = (query: Record<string, string>, signal?: AbortSignal) => request<ModelResolution>(`/models/resolve?${new URLSearchParams({ mode: 'placement', ...query })}`, 'GET', undefined, undefined, signal)
export async function getEvidenceProfiles(signal?: AbortSignal) {
  const profiles = await policyJSON<PrefProfile[]>(await policyRequest('/models', { signal }))
  if (!Array.isArray(profiles)) throw new Error('Invalid model catalog')
  return { profiles: profiles.slice(0, 256), truncated: profiles.length > 256 }
}
