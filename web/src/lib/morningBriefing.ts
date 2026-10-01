// SPDX-License-Identifier: AGPL-3.0-only
// AEON-454: a projection of existing logs and requests, never generated facts.
import { api, APIError } from './api.ts'
import { outcomeLine, type OutcomeEvent } from './ticketOutcomes.ts'
import { formatDollars } from './planning.ts'
import type { UsageGroup, UsageDashboard } from './usageFormat.ts'

export const BRIEFING_KEY = 'morning-briefing'
export interface BriefingPreference { time?: string; last_visit?: string }
export interface BriefingRange { from: string; to: string; first: boolean; capped: boolean }
export interface BriefingOutcome extends OutcomeEvent { ticket_node_id: string; project_id: string; session_id: string | null }
export interface BriefingEvent { id: number; node_id: string | null; type: string; before: unknown; after: unknown; at: string }
export interface BriefingFact { id: string; title: string; detail: string; href: string; source: string; at: string }
export interface BriefingNeed { id: string; title: string; detail: string; href: string; source: string }
export interface BriefingPage<T> { items: T[]; truncated: boolean }

export const validBriefingTime = (value: unknown): value is string => typeof value === 'string' && /^(?:[01]\d|2[0-3]):[0-5]\d$/.test(value)
export function briefingDue(pref: BriefingPreference | null, now = new Date()): boolean {
  const time = validBriefingTime(pref?.time) ? pref.time : '08:00'
  const [hour, minute] = time.split(':').map(Number)
  const due = new Date(now); due.setHours(hour!, minute!, 0, 0)
  const last = Date.parse(pref?.last_visit ?? '')
  return now >= due && (!Number.isFinite(last) || last < due.getTime())
}
export async function briefingJSON<T>(path: string, signal?: AbortSignal, init: RequestInit = {}): Promise<T> {
  const response = await api(path, { ...init, signal })
  if (!response.ok) throw new APIError(response.status, `Briefing source could not be loaded (${response.status}).`)
  return response.json()
}
export async function loadBriefingPreference(signal?: AbortSignal): Promise<BriefingPreference | null> {
  const body = await briefingJSON<{ value: BriefingPreference | null }>(`/preferences/${BRIEFING_KEY}`, signal)
  return body.value
}
export async function saveBriefingPreference(value: BriefingPreference, signal?: AbortSignal): Promise<void> {
  await briefingJSON(`/preferences/${BRIEFING_KEY}`, signal, { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ value }) })
}

// Bound browser work. A partial log says so, and never advances the visit marker.
export async function loadBriefingOutcomes(range: BriefingRange, signal?: AbortSignal): Promise<BriefingPage<BriefingOutcome>> {
  const items = new Map<string, BriefingOutcome>(); const cursors = new Set<string>()
  let cursor: string | null = null
  for (let page = 0; page < 20; page++) {
    const query: URLSearchParams = new URLSearchParams({ from: range.from, to: range.to, limit: '100', ...(cursor ? { cursor } : {}) })
    const read: { outcomes: BriefingOutcome[]; next_cursor?: string | null } = await briefingJSON<{ outcomes: BriefingOutcome[]; next_cursor?: string | null }>(`/outcomes?${query}`, signal)
    if (!Array.isArray(read.outcomes)) throw new Error('The outcome log did not return a list.')
    for (const item of read.outcomes) items.set(item.id, item)
    cursor = read.next_cursor ?? null
    if (!cursor) return { items: [...items.values()], truncated: false }
    if (cursors.has(cursor)) throw new Error('Outcome pagination did not advance.')
    cursors.add(cursor)
  }
  return { items: [...items.values()], truncated: true }
}
interface EventPage { items: BriefingEvent[]; next_cursor?: string | null; window?: BriefingRange }
async function eventPages(range: BriefingRange, signal?: AbortSignal, runs?: true | string, first?: EventPage): Promise<BriefingPage<BriefingEvent>> {
  const items: BriefingEvent[] = [], cursors = new Set<string>()
  let cursor: string | null = null
  for (let page = 0; page < 20; page++) {
    const query = new URLSearchParams({ from: range.from, to: range.to, limit: '200', order: 'time', type: runs ? 'run.telemetry' : 'node.updated', ...(typeof runs === 'string' ? { project_id: runs } : {}), ...(cursor ? { cursor } : {}) })
    const read: EventPage = page === 0 && first ? first : await briefingJSON<EventPage>(`/events?${query}`, signal)
    if (!Array.isArray(read.items)) throw new Error('The event log did not return a list.')
    items.push(...read.items)
    cursor = read.next_cursor ?? null
    if (!cursor) return { items, truncated: false }
    if (cursors.has(cursor)) throw new Error('Event pagination did not advance.')
    cursors.add(cursor)
  }
  return { items, truncated: true }
}
export async function loadBriefingWindow(pref: BriefingPreference | null, signal?: AbortSignal): Promise<{ range: BriefingRange; events: BriefingPage<BriefingEvent> }> {
  const query = new URLSearchParams({ briefing: 'true', limit: '200', type: 'node.updated' })
  if (Number.isFinite(Date.parse(pref?.last_visit ?? ''))) query.set('since', pref!.last_visit!)
  const first = await briefingJSON<EventPage>(`/events?${query}`, signal)
  const range = first.window
  if (!range || !Number.isFinite(Date.parse(range.from)) || !Number.isFinite(Date.parse(range.to)) || Date.parse(range.from) > Date.parse(range.to)) throw new Error('The event log did not return a server window.')
  return { range, events: await eventPages(range, signal, undefined, first) }
}
export function loadBriefingEvents(range: BriefingRange, signal?: AbortSignal, runs?: true | string): Promise<BriefingPage<BriefingEvent>> {
  return eventPages(range, signal, runs)
}
export type BriefingUsage = Pick<UsageDashboard, 'totals' | 'allowance' | 'truncated'>
// Dashboard values remain exact fixed-point strings until presentation.
export function sumBriefingUsage(dashboards: UsageDashboard[]): BriefingUsage {
  const totals = { ...dashboards[0]!.totals, label: 'Permitted projects' }
  for (const key of ['sessions', 'usage_rows', 'unreported_sessions', 'input_known_rows', 'input_unknown_rows', 'output_known_rows', 'output_unknown_rows', 'cached_input_known_rows', 'cached_input_unknown_rows', 'cost_known_rows', 'cost_unknown_rows', 'provisional_rows', 'provisional_sessions'] as const) totals[key] = dashboards.reduce((sum, d) => sum + d.totals[key], 0)
  for (const key of ['input_tokens', 'output_tokens', 'cached_input_tokens', 'estimated_cost_usd'] as const) {
    const values = dashboards.map(d => d.totals[key]).filter((v): v is string => v !== null)
    const sum = values.reduce((n, v) => n + BigInt(v.replace('.', '')), 0n)
    totals[key] = values.length ? key === 'estimated_cost_usd' ? `${sum / 1_000_000_000_000n}.${(sum % 1_000_000_000_000n).toString().padStart(12, '0')}` : String(sum) : null
  }
  totals.cost_state = !totals.cost_known_rows ? 'unknown' : totals.cost_unknown_rows || totals.unreported_sessions ? 'partial' : totals.provisional_rows ? 'provisional' : 'known'
  totals.tokens_state = dashboards.every(d => d.totals.tokens_state === 'known') ? 'known' : dashboards.every(d => d.totals.tokens_state === 'unknown') ? 'unknown' : 'partial'
  const windows = [...new Map(dashboards.flatMap(d => d.allowance.windows).map(w => [w.window_id, w])).values()]
  const state = dashboards.some(d => d.allowance.state === 'visible') ? 'visible' : dashboards.some(d => d.allowance.state === 'withheld') ? 'withheld' : 'none'
  return { totals, allowance: { state, windows }, truncated: dashboards.some(d => d.truncated) }
}
export const personJourneyActions = new Set(['continue_intake', 'confirm_brief', 'decide', 'reopen', 'approve_requirements', 'open_first_release', 'start_build', 'mark_candidate', 'approve_candidate', 'approve_deploy', 'retry_deploy', 'approve_permit', 'plan_next_release', 'renew_candidate', 'renew_deploy'])
function object(value: unknown): Record<string, unknown> { return value !== null && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : {} }
function text(value: unknown): string { return typeof value === 'string' ? value : '' }
export const ticketHref = (project: string, ticket: string) => `/p/${encodeURIComponent(project)}/${encodeURIComponent(ticket)}`
export function outcomeFact(item: BriefingOutcome, projectKey: string): BriefingFact | null {
  if (!projectKey || !['ticket_done', 'released', 'review_verdict', 'ci_result', 'revert'].includes(item.kind)) return null
  if (item.kind === 'review_verdict' && !['changes', 'fail'].includes(text(item.payload.verdict))) return null
  if (item.kind === 'ci_result' && item.payload.result !== 'fail') return null
  const line = outcomeLine(item)
  const href = ticketHref(projectKey, item.ticket_key)
  return { id: `outcome:${item.id}`, title: `${item.ticket_key} · ${line.title}`, detail: line.detail, at: item.recorded_at, href, source: `/api/outcomes?ticket_node_id=${encodeURIComponent(item.ticket_node_id)}&outcome_id=${encodeURIComponent(item.id)}&limit=1` }
}
export function eventFact(item: BriefingEvent, projectKey: string, ticketKey: string): BriefingFact | null {
  if (!projectKey || !ticketKey || !item.node_id || item.id < 1) return null
  const after = object(item.after), before = object(item.before)
  let title = ''
  if (item.type === 'node.updated' && after.state === 'delivered' && before.state !== 'delivered') title = 'Marked delivered'
  // Only the immutable finished report with an explicit merged outcome counts.
  // A later telemetry update must not announce the same merge a second time.
  const report = object(after.report), run = object(after.run)
  if (item.type === 'run.telemetry' && report.kind === 'finished' && run.status === 'completed' && run.outcome_detail === 'merged') title = 'Merge reported'
  if (!title) return null
  const href = ticketHref(projectKey, ticketKey)
  return { id: `event:${item.id}`, title: `${ticketKey} · ${title}`, detail: '', at: item.at, href, source: `/api/events?node_id=${encodeURIComponent(item.node_id)}&after=${item.id - 1}&limit=1` }
}
export function briefingCost(group: UsageGroup): { value: string; detail: string; measured: boolean } {
  if (group.estimated_cost_usd === null || !group.cost_known_rows) return { value: 'Cost not measured yet', detail: `${group.sessions} sessions; no priced usage reported.`, measured: false }
  const qualifier = group.cost_state === 'partial' ? ' · partial' : group.cost_state === 'provisional' ? ' · provisional' : ''
  return { value: `≈ ${formatDollars(group.estimated_cost_usd)}${group.cost_state === 'known' ? ' · measured' : ''}`, detail: `API list value${qualifier}; ${group.cost_known_rows} priced reports, ${group.unreported_sessions} sessions unreported.`, measured: group.cost_state === 'known' }
}
export function recommendedStep(needs: BriefingNeed[], failures: BriefingFact[]): BriefingNeed | null {
  const first = needs[0]
  if (first) return { ...first, title: `Review ${first.title}` }
  const failed = failures[0]
  return failed ? { ...failed, title: `Read the findings for ${failed.title}` } : null
}
