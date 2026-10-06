// SPDX-License-Identifier: AGPL-3.0-only
// Release planning and intake transport, independent of the retired Flow.
import { api, APIError, getKinds, type WorkNode } from './api.ts'

export type ReleaseState = 'planning' | 'building' | 'candidate' | 'deploying' | 'refused' | 'access' | 'released' | 'superseded'
export interface WalkerFeature { feature_node_id: string; epic_key: string; title: string; selection: 'empty' | 'none' | 'some' | 'all'; included_count: number; open_count: number }
export interface WalkerTicket {
  ticket_node_id: string; key: string; title: string; feature_node_id: string | null; included: boolean; position: number
  estimated_hours: number | null; screen_node_ids: string[]
}
export interface Walker { release_node_id: string; project_node_id: string; state: ReleaseState; revision: number; features: WalkerFeature[]; tickets: WalkerTicket[] }
export interface PlanWrite { expected_revision: number; ordered_ticket_ids: string[]; included_ticket_ids: string[] }
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

async function request<T>(path: string, method = 'GET', body?: unknown): Promise<T> {
  const response = await api(path, { method, ...(body === undefined ? {} : { headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) }) })
  if (!response.ok) {
    const data = await response.json().catch(() => ({}))
    const text = typeof data?.error === 'string' ? data.error : typeof data?.message === 'string' ? data.message : `Request failed (${response.status})`
    throw new APIError(response.status, text, data && typeof data === 'object' ? data : {})
  }
  return response.status === 204 ? undefined as T : response.json()
}
const enc = encodeURIComponent
const root = (project: string) => `/projects/${enc(project)}`
const releaseRoot = (project: string, release: string) => `${root(project)}/releases/${enc(release)}`

export const getWalker = (project: string, release: string) => request<Walker>(`${releaseRoot(project, release)}/walker`)
export const putPlan = (project: string, release: string, body: PlanWrite) => request<Walker>(`${releaseRoot(project, release)}/plan`, 'PUT', body)
export const getIntake = (project: string, nodeId?: string) => request<Intake>(`${root(project)}/intake${nodeId ? `?node_id=${encodeURIComponent(nodeId)}` : ''}`)
export const acceptDraft = (project: string, draft: IntakeDraft) =>
  request<IntakeDraft>(`${root(project)}/intake/drafts/${enc(draft.id)}/accept`, 'POST', { expected_base_event_id: draft.base_event_id })

export async function listReleases(project: string): Promise<WorkNode[]> {
  const { items: kinds } = await getKinds()
  const kind = kinds.find(k => k.slug === 'release')
  if (!kind) return []
  const items: WorkNode[] = []
  let cursor: string | undefined
  for (let page = 0; page < 20; page++) {
    const result = await request<{ items: WorkNode[]; next_cursor: string | null }>(`/nodes?parent_id=${enc(project)}&include_descendants=true&kind_id=${enc(kind.id)}&sort=created_at&limit=200${cursor ? `&cursor=${enc(cursor)}` : ''}`)
    items.push(...result.items)
    cursor = result.next_cursor ?? undefined
    if (!cursor) break
  }
  return items
}

export interface ReleaseRef { id: string; key: string; title: string; state: string; created_at: string; number: number; version: string | null }
// Journey releases are titled "Release N"; backfilled ones carry their version as
// the title. Numbers count releases in the order they were created.
export function releaseRefs(nodes: WorkNode[]): ReleaseRef[] {
  const sorted = [...nodes].sort((a, b) => Date.parse(a.created_at) - Date.parse(b.created_at) || a.key.localeCompare(b.key))
  return sorted.map((node, index) => {
    const named = /^Release (\d+)$/i.exec(node.title.trim())
    return { id: node.id, key: node.key, title: node.title, state: node.state, created_at: node.created_at, number: named ? Number(named[1]) : index + 1, version: named ? null : node.title.trim() }
  })
}
export const CALENDAR_VERSION = /^v?[1-9]\d(0[1-9]|1[0-2])(0[1-9]|[12]\d|3[01])([01]\d|2[0-3])[0-5]\d[0-5]\d\.0\.0$/
export const releaseName = (release: Pick<ReleaseRef, 'number'>) => `Release ${release.number}`

export interface PlanningRelease { id: string; title: string; number: number; state: ReleaseState }
export const listPlanningReleases = (project: string) => request<{ releases: PlanningRelease[]; truncated: boolean }>(`${root(project)}/releases`)
