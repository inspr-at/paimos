// SPDX-License-Identifier: AGPL-3.0-only
// The Goals view's reads (AEON-1457) on the Goals API (AEON-1456, api/areas/goals.yaml):
// the visible goals with their outcomes, a goal's ETA history and its progress
// series, plus the words the view uses for them. Writes belong to agents.
import { api, APIError, getNode } from './api'
import { ticketPath } from './work'
import type { GoalRange, RatePoint } from './goalRate'

export type GoalState = 'moving' | 'queued' | 'paused' | 'done'
export const GOAL_STATES: readonly GoalState[] = ['moving', 'queued', 'paused', 'done']
export const STATE_LABEL: Record<GoalState, string> = { moving: 'Moving', queued: 'Queued', paused: 'Paused', done: 'Done' }
export type StateFilter = 'all' | GoalState
export const STATE_FILTERS: readonly StateFilter[] = ['all', ...GOAL_STATES]

export interface GoalStep { id: string; text: string; state: 'done' | 'now' | 'later' }
export interface GoalETA { source: 'none' | 'agent' | 'worker' | 'outcomes'; text?: string; at?: string; by?: string; partial: boolean }
export interface GoalOutcome {
  id: string; ref: string; label: string; note: string; position: number; keys: string[]
  state: GoalState; reason?: string; session_ids: string[]; eta: GoalETA; passed: boolean
  coverage: { estimated_leaves: number; total_leaves: number }; weight: number; handed_over: boolean
}
export interface GoalProgress {
  pct: number; computed_pct: number; source: 'computed' | 'override'; basis: 'count' | 'estimate' | 'mixed'
  total: number; done: number; items_without_estimate: number; items_partially_estimated: number
  override_by?: string; override_at?: string; override_note?: string
}
export interface GoalCounts { moving: number; queued: number; paused: number; done: number }
export interface GoalProjectBadge { id: string; key: string; name: string; group_ids: string[] }
export interface Goal {
  id: string; key: string; title: string; kind: 'goal' | 'release'; project_id: string | null; workspace: boolean
  expansion: 'full' | 'too_large'; counts: { outcomes: number; tickets: number }; items?: GoalOutcome[]
  steps: GoalStep[]; next_step: GoalStep | null; projects: GoalProjectBadge[]; waits: { count: number; question_ids: string[] }
  progress?: GoalProgress; eta?: GoalETA; passed?: boolean; states?: GoalCounts; done?: boolean
  stall?: { stalled: boolean; since: string }
  handed_over?: { outcome_id: string; label: string; detail: string; at: string; by: string }
  read_through: number; generation: number
}
export interface GoalList {
  items: Goal[]; totals: GoalCounts; totals_partial: boolean; quiet_projects: { count: number; keys: string[] }
  next_cursor?: string; truncated: boolean; read_through: number; generation: number
}
export interface GoalSeries { range: GoalRange; expansion: 'full' | 'too_large'; points?: (RatePoint & { basis: string; keys: string[] })[]; read_through: number; generation: number }
export interface GoalEvent { id: number; actor_principal_id: string; node_id: string; type: string; before?: unknown; after?: unknown; at: string }

// ---------- Sorting ----------
export type GoalSort = 'eta' | 'name' | 'progress' | 'project' | 'moving' | 'queued' | 'done'
// The direction a column sorts in first; the header flips it on a second click.
export const SORT_DIR: Record<GoalSort, 'asc' | 'desc'> = { eta: 'asc', name: 'asc', project: 'asc', progress: 'desc', moving: 'desc', queued: 'desc', done: 'desc' }
export const isGoalSort = (value: unknown): value is GoalSort => typeof value === 'string' && value in SORT_DIR

// ---------- Reads ----------
async function read<T>(path: string, signal?: AbortSignal): Promise<T> {
  const response = await api(path, signal ? { signal } : {})
  if (!response.ok) {
    const body = await response.json().catch(() => ({})) as { error?: string; message?: string }
    throw new APIError(response.status, body.error ?? body.message ?? `Goals could not be read (${response.status}).`, body)
  }
  return response.json() as Promise<T>
}
export interface GoalQuery { q?: string; state?: GoalState | null; hideDone?: boolean; sort: GoalSort; dir: 'asc' | 'desc'; cursor?: string; limit?: number }
export function goalQuery(query: GoalQuery): string {
  const params = new URLSearchParams()
  const q = query.q?.trim()
  if (q) params.set('q', q.slice(0, 200))
  if (query.state) params.set('state', query.state)
  if (query.hideDone) params.set('hide_done', 'true')
  params.set('sort', query.sort)
  params.set('dir', query.dir)
  params.set('limit', String(query.limit ?? 100))
  if (query.cursor) params.set('cursor', query.cursor)
  return params.toString()
}
export const listGoals = (query: GoalQuery, signal?: AbortSignal) => read<GoalList>(`/goals?${goalQuery(query)}`, signal)
export const goalSeries = (id: string, range: GoalRange, signal?: AbortSignal) => read<GoalSeries>(`/goals/${encodeURIComponent(id)}/series?range=${range}`, signal)
export const goalHistory = (id: string, outcome?: string, signal?: AbortSignal) =>
  read<{ items: GoalEvent[]; next_cursor?: string }>(`/goals/${encodeURIComponent(id)}/history?limit=20${outcome ? `&outcome=${encodeURIComponent(outcome)}` : ''}`, signal)

// When a goal was created never changes: read once per goal and kept for the visit.
const created = new Map<string, Promise<number | null>>()
export function goalCreatedAt(id: string): Promise<number | null> {
  let known = created.get(id)
  if (!known) {
    known = getNode(id).then(node => { const at = Date.parse(node.created_at); return Number.isNaN(at) ? null : at }, () => { created.delete(id); return null })
    created.set(id, known)
  }
  return known
}

// ---------- Words ----------
const clock = (ms: number, timeZone?: string) => new Intl.DateTimeFormat('en-GB', { hour: '2-digit', minute: '2-digit', hourCycle: 'h23', timeZone }).format(new Date(ms))
const sameDay = (a: number, b: number, timeZone?: string) => { const f = new Intl.DateTimeFormat('en-CA', { timeZone }); return f.format(new Date(a)) === f.format(new Date(b)) }
// "17:58" today, "Fri 15:12" this week, "14 Oct 15:12" further away.
export function whenText(ms: number, now: number, timeZone?: string): string {
  if (sameDay(ms, now, timeZone)) return clock(ms, timeZone)
  const far = Math.abs(ms - now) > 6 * 86_400_000
  const day = new Intl.DateTimeFormat('en-GB', far ? { day: 'numeric', month: 'short', timeZone } : { weekday: 'short', timeZone }).format(new Date(ms))
  return `${day} ${clock(ms, timeZone)}`
}
// The ETA as its author wrote it ("~21:00", "after 136"), else its time.
export function etaText(eta: Pick<GoalETA, 'text' | 'at'> | null | undefined, now: number, timeZone?: string): string {
  if (eta?.text?.trim()) return eta.text.trim()
  const at = eta?.at ? Date.parse(eta.at) : NaN
  if (Number.isNaN(at)) return ''
  return sameDay(at, now, timeZone) ? `~${clock(at, timeZone)}` : whenText(at, now, timeZone)
}
export function passedMinutes(eta: Pick<GoalETA, 'at'> | null | undefined, now: number): number {
  const at = eta?.at ? Date.parse(eta.at) : NaN
  return Number.isNaN(at) ? 0 : Math.max(0, Math.round((now - at) / 60_000))
}

// The value-bearing ETA changes of one node, newest first. Change pings carry no value.
export interface EtaChange { at: number; by: string; value: Pick<GoalETA, 'text' | 'at'> | null; before: Pick<GoalETA, 'text' | 'at'> | null }
const etaValue = (raw: unknown): Pick<GoalETA, 'text' | 'at'> | null => {
  if (!raw || typeof raw !== 'object' || !('source' in raw)) return null
  const value = raw as { text?: unknown; at?: unknown }
  const text = typeof value.text === 'string' ? value.text : undefined, at = typeof value.at === 'string' ? value.at : undefined
  return text || at ? { text, at } : null
}
export function etaChanges(events: readonly GoalEvent[], nodeId: string): EtaChange[] {
  return events
    .filter(event => event.type === 'goal.eta_changed' && event.node_id === nodeId && !!event.after && typeof event.after === 'object' && 'source' in (event.after as object))
    .map(event => ({ at: Date.parse(event.at), by: event.actor_principal_id, value: etaValue(event.after), before: etaValue(event.before) }))
    .filter(change => !Number.isNaN(change.at))
}

// The opened goal's items: Moving, then Queued (the first five, then the rest
// behind a fold), then Paused; Done folds into one line; Handed over closes it.
export const QUEUE_SHOWN = 5
export interface ItemGroups { moving: GoalOutcome[]; queued: GoalOutcome[]; paused: GoalOutcome[]; done: GoalOutcome[]; handed: GoalOutcome[] }
export function itemGroups(items: readonly GoalOutcome[]): ItemGroups {
  const at = (item: GoalOutcome) => { const ms = item.eta.at ? Date.parse(item.eta.at) : NaN; return Number.isNaN(ms) ? Infinity : ms }
  const byEta = (a: GoalOutcome, b: GoalOutcome) => at(a) - at(b) || a.position - b.position
  const of = (state: GoalState) => items.filter(item => item.state === state && !item.handed_over).sort(byEta)
  return { moving: of('moving'), queued: of('queued'), paused: of('paused'), done: items.filter(item => item.state === 'done' && !item.handed_over), handed: items.filter(item => item.handed_over) }
}
// What the search finds: the goal itself, or which of its items.
export function goalMatches(goal: Goal, needle: string): boolean {
  const q = needle.trim().toLowerCase()
  if (!q) return true
  return [goal.title, goal.key, goal.next_step?.text ?? '', ...goal.projects.map(p => p.key)].join(' ').toLowerCase().includes(q)
}
export function itemMatches(item: GoalOutcome, needle: string): boolean {
  const q = needle.trim().toLowerCase()
  return !q || [item.label, item.note, ...item.keys].join(' ').toLowerCase().includes(q)
}
// A ticket key opens its ticket; its project's route key is the key's prefix.
export const ticketHref = (key: string) => ticketPath(key.replace(/-\d+$/, ''), key)
