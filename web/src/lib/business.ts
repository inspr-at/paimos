// SPDX-License-Identifier: AGPL-3.0-only
// R4 business wire types and calls (api/openapi.yaml). Money never passes
// through a binary float: responses are parsed with their exact number
// spelling, and request bodies write validated decimal strings as JSON numbers.
import { api, APIError } from './api'
import { learnPictures } from './avatar'
import { parseJSONExact } from '../components/business/money'

export type Unit = 'hour' | 'day' | 'item'
export type QuoteState = 'draft' | 'issued' | 'accepted' | 'void'

export interface QuoteSummary {
  version: number; title: string; currency: string; recipient_contact_node_id: string
  subtotal: string; tax_total: string; total: string; content_sha256: string; line_count: number; created_at: string
}
export interface Quote {
  quote_node_id: string; project_node_id: string; customer_org_node_id: string
  current_version: number; state: QuoteState; revision: number
  key: string; title: string; created_at: string; updated_at: string
  current: QuoteSummary | null; issued_at: string | null; accepted_at: string | null; viewer_can_accept: boolean
}
export interface QuoteLine {
  position: number; description: string; cost_unit_node_id: string; unit: Unit
  quantity: string; tax_rate: string; rate_amount: string; net_amount: string
}
export interface QuoteVersion {
  quote_node_id: string; version: number; recipient_contact_node_id: string; currency: string; title: string
  terms_markdown: string; lines: QuoteLine[]; subtotal: string; tax_total: string; total: string
  content_sha256: string; created_by_principal_id: string; created_at: string
  issue: { issued_by_principal_id: string; issued_at: string; event_id: number } | null
  acceptance: { customer_principal_id: string; accepted_content_sha256: string; accepted_at: string; event_id: number } | null
}
export interface QuoteLineWrite { description: string; cost_unit_node_id: string; unit: Unit; quantity: string; tax_rate: string }
export interface QuoteVersionWrite {
  expected_revision: number; recipient_contact_node_id: string; currency: string; title: string; terms_markdown: string; lines: QuoteLineWrite[]
}
export interface CostRate {
  id: string; cost_unit_node_id: string; unit: Unit; currency: string; internal_amount: string; bill_amount: string
  effective_from: string; effective_until: string | null; created_by_principal_id: string; created_at: string
}
export interface CostRateWrite { unit: Unit; currency: string; internal_amount: string; bill_amount: string; effective_from: string; effective_until: string | null }
export interface TimePeriod {
  id: string; principal_id: string; starts_at: string; ends_at: string; state: 'open' | 'approved'; revision: number
  approval: { approved_by_principal_id: string; entries_sha256: string; total_seconds: number; event_id: number } | null
}
export interface TimeEntry {
  id: string; period_id: string; principal_id: string; node_id: string; cost_unit_node_id: string
  source: 'manual' | 'agent_run'; agent_run_id?: string | null; started_at: string; ended_at: string
  duration_seconds: number; rate_amount: string; currency: string; amount: string; note: string
  // The revision a correction is made against (If-Unmodified-Since).
  updated_at: string
}
// A correction sends only what changed. A start alone keeps the duration.
export interface EntryPatch { node_id?: string; cost_unit_node_id?: string; started_at?: string; duration_seconds?: number; note?: string }
export interface TimeTotals { node_id: string; duration_seconds: number; amounts: { currency: string; amount: string }[] }
export interface Binding { principal_id: string; contact_node_id: string; bound_by_principal_id: string; bound_at: string; principal_name: string; principal_kind: 'person' | 'agent' }
export interface Principal { id: string; kind: 'person' | 'agent'; name: string; roles: string[]; has_avatar?: boolean }

// A validated decimal written as a JSON number, never through Number().
export class Decimal { readonly text: string; constructor(text: string) { this.text = text } }
const DECIMAL_TEXT = /^-?(0|[1-9]\d*)(\.\d+)?$/
export function encode(value: unknown): string {
  if (value instanceof Decimal) {
    if (!DECIMAL_TEXT.test(value.text)) throw new Error('Not an exact decimal.')
    return value.text
  }
  if (Array.isArray(value)) return `[${value.map(encode).join(',')}]`
  if (value && typeof value === 'object') return `{${Object.entries(value).filter(([, v]) => v !== undefined).map(([k, v]) => `${JSON.stringify(k)}:${encode(v)}`).join(',')}}`
  return JSON.stringify(value)
}

async function send<T>(path: string, method = 'GET', body?: unknown, headers: Record<string, string> = {}): Promise<{ data: T; response: Response }> {
  const response = await api(path, {
    method,
    ...(body === undefined ? { headers } : { headers: { ...headers, 'Content-Type': 'application/json' }, body: encode(body) }),
  })
  const text = await response.text()
  let data: unknown = null
  try { data = text ? parseJSONExact(text) : null } catch { data = null }
  if (!response.ok) {
    const row = (data && typeof data === 'object' ? data : {}) as Record<string, unknown>
    const message = typeof row.message === 'string' && row.message ? row.message : typeof row.error === 'string' && row.error ? row.error : `Request failed (${response.status})`
    throw new APIError(response.status, message, row)
  }
  return { data: data as T, response }
}
async function call<T>(path: string, method = 'GET', body?: unknown): Promise<T> { return (await send<T>(path, method, body)).data }
async function listAll<T>(path: string): Promise<T[]> {
  const out: T[] = []
  const seen = new Set<string>()
  let cursor = ''
  for (let page = 0; page < 20; page++) {
    const suffix = `${path.includes('?') ? '&' : '?'}limit=100${cursor ? `&after_id=${encodeURIComponent(cursor)}` : ''}`
    const { data, response } = await send<T[]>(path + suffix)
    out.push(...data)
    cursor = response.headers.get('X-Next-Cursor') ?? ''
    if (!cursor) return out
    if (seen.has(cursor)) throw new Error('History paging did not advance.')
    seen.add(cursor)
  }
  throw new Error('More than 2,000 history records. Narrow the period or principal selection.')
}
const id = (value: string) => encodeURIComponent(value)
const int = (value: unknown) => typeof value === 'string' ? Number(value) : typeof value === 'number' ? value : 0

// parseJSONExact turns every number into its exact string; integers come back here.
function quote(raw: Quote): Quote {
  return {
    ...raw, current_version: int(raw.current_version), revision: int(raw.revision),
    current: raw.current ? { ...raw.current, version: int(raw.current.version), line_count: int(raw.current.line_count) } : null,
    issued_at: raw.issued_at ?? null, accepted_at: raw.accepted_at ?? null, viewer_can_accept: raw.viewer_can_accept === true,
  }
}
function version(raw: QuoteVersion): QuoteVersion {
  return {
    ...raw, version: int(raw.version), lines: (raw.lines ?? []).map(line => ({ ...line, position: int(line.position) })),
    issue: raw.issue ? { ...raw.issue, event_id: int(raw.issue.event_id) } : null,
    acceptance: raw.acceptance ? { ...raw.acceptance, event_id: int(raw.acceptance.event_id) } : null,
  }
}
function period(raw: TimePeriod): TimePeriod {
  return { ...raw, revision: int(raw.revision), approval: raw.approval ? { ...raw.approval, total_seconds: int(raw.approval.total_seconds), event_id: int(raw.approval.event_id) } : null }
}
function entry(raw: TimeEntry): TimeEntry { return { ...raw, duration_seconds: int(raw.duration_seconds) } }

// ---------- Quotes ----------
export const listQuotes = async (filter: { project_node_id?: string; customer_org_node_id?: string } = {}) => {
  const params = new URLSearchParams(Object.entries(filter).filter(([, v]) => v) as [string, string][]).toString()
  return (await call<Quote[]>(`/quotes${params ? `?${params}` : ''}`)).map(quote)
}
export const getQuote = async (quoteId: string) => quote(await call<Quote>(`/quotes/${id(quoteId)}`))
export const createQuote = async (body: { title: string; project_node_id: string; customer_org_node_id: string }) => quote(await call<Quote>('/quotes', 'POST', body))
export const listVersions = async (quoteId: string) => (await call<QuoteVersion[]>(`/quotes/${id(quoteId)}/versions`)).map(version)
export const createVersion = async (quoteId: string, body: QuoteVersionWrite) => version(await call<QuoteVersion>(`/quotes/${id(quoteId)}/versions`, 'POST', {
  ...body, lines: body.lines.map(line => ({ ...line, quantity: new Decimal(line.quantity), tax_rate: new Decimal(line.tax_rate) })),
}))
export const issueVersion = async (quoteId: string, n: number) => quote(await call<Quote>(`/quotes/${id(quoteId)}/versions/${n}/issue`, 'POST'))
export const acceptVersion = (quoteId: string, n: number, digest: string) => call<unknown>(`/quotes/${id(quoteId)}/versions/${n}/accept`, 'POST', { expected_content_sha256: digest })
export const exportURL = (quoteId: string, n: number, format: 'markdown' | 'pdf') => `/api/quotes/${id(quoteId)}/versions/${n}/export?format=${format}`

// ---------- Cost units ----------
export const listRates = (costUnitId: string) => call<CostRate[]>(`/cost-units/${id(costUnitId)}/rates`)
export const createRate = (costUnitId: string, body: CostRateWrite) => call<CostRate>(`/cost-units/${id(costUnitId)}/rates`, 'POST', {
  ...body, internal_amount: new Decimal(body.internal_amount), bill_amount: new Decimal(body.bill_amount),
})

// ---------- CRM ----------
export const listBindings = (contactId: string) => call<Binding[]>(`/crm/contacts/${id(contactId)}/principals`)
export const bindContact = (contactId: string, principalId: string) => call<Binding>(`/crm/contacts/${id(contactId)}/principals`, 'POST', { principal_id: principalId })
// Teaches the avatars who has a picture (lib/avatar), for every list of members.
export const listPrincipals = () => call<Principal[]>('/business/principals').then(items => { learnPictures(items); return items })

// ---------- Hours ----------
// Approvals need the complete history: valid period intervals start in year 1.
// Omitting since uses the server's one-year window instead.
export const PERIOD_HISTORY_START = '0001-01-01T00:00:00Z'
export const listPeriods = async (principalId?: string, bounds: { since?: string; until?: string } = {}) => {
  const params = new URLSearchParams({ ...(principalId ? { principal_id: principalId } : {}), ...bounds }).toString()
  return (await listAll<TimePeriod>(`/time-periods${params ? `?${params}` : ''}`)).map(period)
}
// The entries digest travels in a header; approval must send it back unchanged.
export async function getPeriod(periodId: string): Promise<{ period: TimePeriod; digest: string }> {
  const { data, response } = await send<TimePeriod>(`/time-periods/${id(periodId)}`)
  return { period: period(data), digest: response.headers.get('X-Entries-SHA256') ?? '' }
}
export const createPeriod = async (body: { principal_id: string; starts_at: string; ends_at: string }) => period(await call<TimePeriod>('/time-periods', 'POST', body))
export const approvePeriod = async (periodId: string, revision: number, digest: string) =>
  period(await call<TimePeriod>(`/time-periods/${id(periodId)}/approve`, 'POST', { expected_revision: revision, expected_entries_sha256: digest }))
export const listEntries = async (filter: { period_id?: string; principal_id?: string; node_id?: string }) => {
  const params = new URLSearchParams(Object.entries(filter).filter(([, v]) => v) as [string, string][]).toString()
  return (await listAll<TimeEntry>(`/time-entries${params ? `?${params}` : ''}`)).map(entry)
}
export const createEntry = async (body: { period_id: string; cost_unit_node_id: string; currency: string; principal_id: string; node_id: string; started_at: string; ended_at: string; note: string }) =>
  entry(await call<TimeEntry>('/time-entries', 'POST', { ...body, source: 'manual' }))
// Corrections name the revision they were made against; a 412 carries the newer
// entry (conflictEntry), a 409 means the period was approved or the time or rate
// does not fit. Both write an event that POST /events/{id}/undo reverses.
export const updateEntry = async (e: TimeEntry, patch: EntryPatch) =>
  entry((await send<TimeEntry>(`/time-entries/${id(e.id)}`, 'PATCH', patch, { 'If-Unmodified-Since': e.updated_at })).data)
export const deleteEntry = async (e: TimeEntry) => { await send<null>(`/time-entries/${id(e.id)}`, 'DELETE', undefined, { 'If-Unmodified-Since': e.updated_at }) }
export function conflictEntry(error: unknown): TimeEntry | null {
  const current = error instanceof APIError && error.status === 412 ? error.body.current : null
  return current && typeof current === 'object' ? entry(current as TimeEntry) : null
}
// DELETE answers 204 without the event, so undo finds the newest deletion of
// this entry on its ticket's event log (the same way attachments do).
export async function findEntryEvent(nodeId: string, entryId: string, type: 'time_entry.deleted' | 'time_entry.updated'): Promise<number | null> {
  let after = 0, found: number | null = null
  for (let page = 0; page < 50; page++) {
    const body = await call<{ items: { id: unknown; type: string; before: { id?: string } | null; undo_of: unknown }[]; next_after: unknown }>(`/events?node_id=${id(nodeId)}&limit=200${after ? `&after=${after}` : ''}`)
    for (const event of body.items ?? []) if (event.type === type && event.before?.id === entryId && event.undo_of == null) found = int(event.id)
    if (!body.next_after) break
    after = int(body.next_after)
  }
  return found
}
export async function undoEntryEvent(eventId: number): Promise<TimeEntry | null> {
  const event = await call<{ after: TimeEntry | null }>(`/events/${eventId}/undo`, 'POST')
  return event?.after ? entry(event.after) : null
}
export const getTimeTotals = async (nodeId: string, approvedOnly = false) => {
  const raw = await call<TimeTotals>(`/nodes/${id(nodeId)}/time-totals${approvedOnly ? '?approved_only=true' : ''}`)
  return { ...raw, duration_seconds: int(raw.duration_seconds) }
}

// ---------- Graph links (R1 relations) ----------
export const createRelation = (source: string, target: string, type: 'customer_of' | 'contact_for') =>
  call<{ id: string; source_node_id: string; target_node_id: string; type: string; created_at: string }>('/relations', 'POST', { source_node_id: source, target_node_id: target, type })
export const deleteRelation = (relationId: string) => call<null>(`/relations/${id(relationId)}`, 'DELETE')
