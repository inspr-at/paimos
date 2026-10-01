// SPDX-License-Identifier: AGPL-3.0-only
// AEON-454: a projection of existing logs and requests, never generated facts.
import { api, APIError } from './api.ts'
import { outcomeLine, type OutcomeEvent } from './ticketOutcomes.ts'
import { formatDollars } from './planning.ts'
import type { UsageGroup } from './usageFormat.ts'

export const BRIEFING_KEY = 'morning-briefing'
const DAY = 86_400_000
export interface BriefingPreference { time?: string; last_visit?: string }
export interface BriefingRange { from: string; to: string; first: boolean; capped: boolean }
export interface BriefingOutcome extends OutcomeEvent { ticket_node_id: string; project_id: string; session_id: string | null }
export interface BriefingEvent { id: number; node_id: string | null; type: string; before: unknown; after: unknown; at: string }
export interface BriefingFact { id: string; title: string; detail: string; href: string; source: string; at: string }
export interface BriefingNeed { id: string; title: string; detail: string; href: string; source: string }
export interface BriefingPage<T> { items: T[]; truncated: boolean }

export const validBriefingTime = (value: unknown): value is string => typeof value === 'string' && /^(?:[01]\d|2[0-3]):[0-5]\d$/.test(value)
export function briefingRange(pref: BriefingPreference | null, now = new Date()): BriefingRange {
  const last = Date.parse(pref?.last_visit ?? '')
  const end = now.getTime()
  const first = !Number.isFinite(last) || last >= end
  const start = first ? end - DAY : last
  const floor = end - 366 * DAY
  return { from: new Date(Math.max(start, floor)).toISOString(), to: now.toISOString(), first, capped: start < floor }
}
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
export async function loadBriefingEvents(range: BriefingRange, signal?: AbortSignal, includeRuns = false): Promise<BriefingPage<BriefingEvent>> {
  const items: BriefingEvent[] = []; let after = 0
  for (let page = 0; page < 20; page++) {
    const query: URLSearchParams = new URLSearchParams({ from: range.from, to: range.to, limit: '200', after: String(after), type: includeRuns ? 'node.updated,run.telemetry' : 'node.updated' })
    const read = await briefingJSON<{ items: BriefingEvent[]; next_after: number | null }>(`/events?${query}`, signal)
    if (!Array.isArray(read.items)) throw new Error('The event log did not return a list.')
    items.push(...read.items)
    if (read.next_after === null) return { items, truncated: false }
    if (!Number.isSafeInteger(read.next_after) || read.next_after <= after) throw new Error('Event pagination did not advance.')
    after = read.next_after
  }
  return { items, truncated: true }
}
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
  return { value: `≈ ${formatDollars(group.estimated_cost_usd)}`, detail: `API list value${qualifier}; ${group.cost_known_rows} priced reports, ${group.unreported_sessions} sessions unreported.`, measured: group.cost_state === 'known' }
}
export function recommendedStep(needs: BriefingNeed[], failures: BriefingFact[]): BriefingNeed | null {
  const first = needs[0]
  if (first) return { ...first, title: `Review ${first.title}` }
  const failed = failures[0]
  return failed ? { ...failed, title: `Read the findings for ${failed.title}` } : null
}
