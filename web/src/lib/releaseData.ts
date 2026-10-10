// SPDX-License-Identifier: AGPL-3.0-only
// Release planning and intake transport, independent of the retired Flow.
import { api, APIError } from './api.ts'

export type ReleaseState = 'planning' | 'building' | 'candidate' | 'deploying' | 'refused' | 'access' | 'released' | 'superseded'
export interface WalkerFeature { feature_node_id: string; epic_key: string; title: string; selection: 'empty' | 'none' | 'some' | 'all'; included_count: number; open_count: number }
export interface WalkerTicket {
  ticket_node_id: string; key: string; title: string; feature_node_id: string | null; included: boolean; position: number
  estimated_hours: number | null; screen_node_ids: string[]
}
export interface Walker { release_node_id: string; project_node_id: string; state: ReleaseState; revision: number; features: WalkerFeature[]; tickets: WalkerTicket[] }
export interface IntakeSource {
  id: string; project_node_id: string; kind: 'url' | 'file' | 'note' | 'conversation'; label: string; locator?: string; file_id?: string
  content_sha256: string; created_at: string
}
export interface IntakeTurn { id: string; source_id: string; ordinal: number; speaker: 'person' | 'agent'; speaker_principal_id: string; body: string; created_at: string }
export interface Citation { source_id: string; turn_id?: string; locator: string }
export interface TicketSuggestion { title: string; estimated_hours: string | number; later: boolean; access_change: boolean }
export type IntakeExtensions = Record<string, { version: string; data: unknown }>
export interface IntakeDraft {
  id: string; kind: 'brief' | 'requirement'; requirement_kind?: 'functional' | 'nonfunctional'; target_node_id?: string; title: string; body: string
  base_event_id: number; citations: Citation[]; ticket_suggestions: TicketSuggestion[]; status: 'proposed' | 'accepted' | 'rejected' | 'superseded'
  proposed_at: string; accepted_at: string | null
  requester_principal_id?: string; supersedes_draft_id?: string
  extensions?: IntakeExtensions; document_bytes?: string
}
export interface Intake { sources: IntakeSource[]; turns: IntakeTurn[]; drafts: IntakeDraft[] }
export interface IntakePage extends Intake { nextCursor: string | null }

async function request<T>(path: string, method = 'GET', body?: unknown, onResponse?: (response: Response) => void): Promise<T> {
  const response = await api(path, { method, ...(body === undefined ? {} : { headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) }) })
  if (!response.ok) {
    const data = await response.json().catch(() => ({}))
    const text = typeof data?.error === 'string' ? data.error : typeof data?.message === 'string' ? data.message : `Request failed (${response.status})`
    throw new APIError(response.status, text, data && typeof data === 'object' ? data : {})
  }
  onResponse?.(response)
  return response.status === 204 ? undefined as T : response.json()
}
const enc = encodeURIComponent
const root = (project: string) => `/projects/${enc(project)}`
const releaseRoot = (project: string, release: string) => `${root(project)}/releases/${enc(release)}`

export const getWalker = (project: string, release: string) => request<Walker>(`${releaseRoot(project, release)}/walker`)
export async function getIntake(project: string, nodeId?: string, after?: string): Promise<IntakePage> {
  const query = new URLSearchParams()
  if (nodeId) query.set('node_id', nodeId)
  if (after) query.set('after', after)
  let nextCursor: string | null = null
  const data = await request<Intake>(`${root(project)}/intake${query.size ? `?${query}` : ''}`, 'GET', undefined, response => { nextCursor = response.headers.get('X-Next-Cursor') })
  return { ...data, nextCursor }
}
export interface PlanningRelease { id: string; title: string; number: number; state: ReleaseState }
export const listPlanningReleases = (project: string) => request<{ releases: PlanningRelease[]; truncated: boolean }>(`${root(project)}/releases`)

export const releaseName = (release: Pick<PlanningRelease, 'number'>) => `Release ${release.number}`
