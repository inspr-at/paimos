// SPDX-License-Identifier: AGPL-3.0-only
import { api, APIError, type ListItem } from './api.ts'
import { apiParams, type ListFilters } from './ticketList.ts'

export const RELEASE_PAGE_SIZE = 50
export const WORK_PAGE_SIZE = 200
export const WORK_RENDER_LIMIT = 2000
export const READ_CONCURRENCY = 4
export interface MatchCounts {
  matched_count: number; shown_count: number; hidden_count: number
  hidden_finished: number; hidden_exit: number; hidden_other?: number; incomplete: boolean
}
export interface PlanningRelease {
  release_id: string; project_id: string; title: string; display_name?: string
  visibility: 'published' | 'internal'; state: 'planned' | 'building' | 'frozen' | 'released' | 'abandoned'
  rank: string; revision: number; version?: string; version_scheme?: string
  rollup: { units: number; completed: number; open_hours: number }
  matches?: MatchCounts
  build_summary: { budget_outlook?: string; agents?: number; waiting?: number }
}
export interface ReleasePage { items: PlanningRelease[]; next_cursor?: string; matches?: MatchCounts }
export interface PlanningOverview {
  active: PlanningRelease[]; active_next_cursor?: string; released: ReleasePage
  backlog: { ranked: number; tail: number }; backlog_matches?: Record<string, MatchCounts>
  abandoned: number; matches?: MatchCounts; counts_incomplete: boolean
}
export interface PlanningItem {
  item_id: string; project_id: string; release_id?: string; revision: number; node_revision: string
  rank?: string; key: string; title: string; kind: string; state: string; created_at: string
  estimated_hours: number | null; epic_id?: string; expedite: boolean; due_on: string | null
}
export interface ItemPage { items: PlanningItem[]; next_cursor?: string; matches?: MatchCounts; count: number; incomplete: boolean }
export type PlanningSource = 'overview' | 'active' | 'released' | 'backlog:ranked' | 'backlog:tail' | `release:${string}`
export type PlanningAnswer = PlanningOverview | ReleasePage | ItemPage
export interface PlanningContext { project: string; person: string; scope: unknown; query: string }
export type PlanningRead = (context: PlanningContext, source: PlanningSource, cursor: string, limit: number, signal: AbortSignal) => Promise<PlanningAnswer>

// The planning list stays project-wide: ships_in is request identity only, never
// a second membership selector. Reuse ticket filters, including their exclusions
// and date bounds, but keep release lifecycle separate from work_state.
export function planningQuery(project: string, filters: ListFilters, hideStates: readonly string[] = []): string {
  const ordinary = apiParams(project, { ...filters, ships_in: [] })
  const params = new URLSearchParams({ view: 'planning', hide_closed: String(!filters.showClosed) })
  for (const key of ['q', 'kind', 'priority', 'assignee', 'tag', 'cost_unit', 'human_check', 'epic', 'date_field', 'date_from', 'date_to'] as const) {
    const value = ordinary[key]
    if (Array.isArray(value)) value.forEach(v => params.append(key, v))
    else if (value) params.set(key, value)
  }
  ordinary.state?.forEach(v => params.append('work_state', v))
  hideStates.forEach(v => params.append('hide_state', v))
  if (new TextEncoder().encode(params.get('q') ?? '').length > 200) throw new Error('Search is limited to 200 UTF-8 bytes')
  for (const key of ['kind', 'priority', 'assignee', 'tag', 'cost_unit', 'human_check', 'epic', 'work_state', 'hide_state']) {
    const values = params.getAll(key)
    if (values.reduce((n, v) => n + v.split(',').length, 0) > 100 || new TextEncoder().encode(values.join(',')).length > 6400) throw new Error('Too many filter values')
  }
  return params.toString()
}

export const readPlanning: PlanningRead = async (context, source, cursor, limit, signal) => {
  if (cursor.length > 2048) throw new Error('Invalid page cursor')
  const params = new URLSearchParams(context.query)
  const root = `/projects/${encodeURIComponent(context.project)}`
  let path: string
  if (source === 'overview') path = `${root}/delivery/overview`
  else if (source === 'active' || source === 'released') { path = `${root}/releases`; params.set('state', source) }
  else if (source.startsWith('backlog:')) { path = `${root}/backlog`; params.set('part', source.slice(8)) }
  else path = `${root}/releases/${encodeURIComponent(source.slice(8))}/items`
  params.set('limit', String(Math.min(source === 'overview' || source === 'active' || source === 'released' ? RELEASE_PAGE_SIZE : WORK_PAGE_SIZE, limit)))
  if (cursor) params.set(source === 'overview' ? 'released_cursor' : 'cursor', cursor)
  const response = await api(`${path}?${params}`, { signal }, 60_000)
  if (!response.ok) {
    const body = await response.json().catch(() => ({}))
    throw new APIError(response.status, body.error || body.message || `Release read failed (${response.status})`, body)
  }
  const answer = await response.json() as PlanningAnswer
  if (source === 'overview') {
    const overview = answer as PlanningOverview
    if (!Array.isArray(overview.active) || overview.active.length > 50 || !Array.isArray(overview.released?.items) || overview.released.items.length > 50 || !overview.backlog) throw new Error('Invalid release overview')
  } else if (!Array.isArray((answer as ItemPage).items) || (answer as ItemPage).items.length > limit) throw new Error('Invalid release page')
  return answer
}

export function releaseName(release: PlanningRelease) { return release.display_name || release.title }
export function releaseState(release: PlanningRelease) {
  return release.state === 'released' && release.visibility === 'internal' ? 'Done' : release.state[0]!.toUpperCase() + release.state.slice(1)
}
export function hiddenCopy(counts?: MatchCounts) {
  if (!counts?.hidden_count) return ''
  const parts = [counts.hidden_finished ? `${counts.hidden_finished} finished` : '', counts.hidden_exit ? `${counts.hidden_exit} cancelled or archived` : '', counts.hidden_other ? `${counts.hidden_other} other` : ''].filter(Boolean)
  return `${counts.incomplete ? 'At least ' : ''}${parts.join(', ') || counts.hidden_count} hidden by Hide closed`
}
// Delivery pages currently omit rich ticket summaries. Leave progress, ETA and
// assignee unknown; no extra node requests and no fabricated completion percent.
export function planningListItem(item: PlanningItem): ListItem {
  return {
    id: item.item_id, key: item.key, kind_id: '', kind_slug: item.kind, kind_label: item.kind,
    title: item.title, body: '', fields: item.estimated_hours === null ? {} : { estimate_hours: item.estimated_hours },
    state: item.state, parent_id: item.epic_id || item.project_id, parent: null,
    position: item.rank || '', created_at: item.created_at, updated_at: item.node_revision,
    priority: null, assignee: null, children_count: 0, project: null,
  }
}
