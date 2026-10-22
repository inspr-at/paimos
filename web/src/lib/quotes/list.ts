// SPDX-License-Identifier: AGPL-3.0-only
// The Quotes list: rows as GET /api/quotes returns them (BusinessQuoteListItem),
// every page of them, and the pure search, filter, sort and column rules the
// list view uses. Nothing here touches the DOM, so it is tested on its own.
import { api, APIError, type ListResult } from '../api'
import { minorMoney, parseMoney } from '../crm'

export type QuoteState = 'draft' | 'issued' | 'accepted' | 'void'
// What the list shows: "issued" splits into issued and expired by the frozen validity day.
export type QuoteStatus = 'draft' | 'issued' | 'expired' | 'accepted' | 'void'
export interface QuoteRow {
  quote_node_id: string; project_node_id: string; customer_org_node_id: string
  current_version: number; state: QuoteState; revision: number; offer_no?: string; archived: boolean
  project_ref: string; classic_status: 'draft' | 'sent' | 'expired' | 'accepted' | 'void'
  key: string; title: string; customer_name: string; currency?: string; net_total_cents?: number
  offer_date?: string; valid_until?: string; created_at: string; updated_at: string; issued_at?: string; accepted_at?: string
}

export const STATUS_META: Record<QuoteStatus, { label: string; order: number; hint: string }> = {
  draft: { label: 'Draft', order: 0, hint: 'Being written; the customer cannot see it' },
  issued: { label: 'Issued', order: 1, hint: 'Frozen and ready for the customer to accept' },
  expired: { label: 'Expired', order: 2, hint: 'Issued, but its validity day has passed' },
  accepted: { label: 'Accepted', order: 3, hint: 'The customer accepted this exact version' },
  void: { label: 'Void', order: 4, hint: 'No longer valid' },
}
export const STATUSES = Object.keys(STATUS_META) as QuoteStatus[]
export function statusOf(row: Pick<QuoteRow, 'state' | 'classic_status'>): QuoteStatus {
  if (row.state === 'issued') return row.classic_status === 'expired' ? 'expired' : 'issued'
  return row.state
}

// ---------- Reading ----------
export class QuoteListError extends Error {
  readonly status: number
  constructor(status: number, message: string) { super(message); this.status = status }
}
function normalise(raw: QuoteRow): QuoteRow {
  return {
    ...raw,
    offer_no: raw.offer_no || undefined,
    key: raw.key ?? '', title: (raw.title ?? '').trim(), customer_name: (raw.customer_name ?? '').trim(),
    project_ref: raw.project_ref ?? '', archived: raw.archived === true,
    net_total_cents: typeof raw.net_total_cents === 'number' && Number.isSafeInteger(raw.net_total_cents) ? raw.net_total_cents : undefined,
    created_at: raw.created_at ?? '', updated_at: raw.updated_at ?? raw.created_at ?? '',
  }
}
// Every quote, archived ones included (the list hides them until asked), 200 at a time.
export async function fetchQuotes(filter: { customer?: string } = {}): Promise<ListResult<QuoteRow[]>> {
  const out: QuoteRow[] = []
  const seen = new Set<string>()
  let cursor = ''
  for (let page = 0; page < 50; page++) {
    const params = new URLSearchParams({ limit: '200', archived: 'all' })
    if (filter.customer) params.set('customer_org_node_id', filter.customer)
    if (cursor) params.set('cursor', cursor)
    const response = await api(`/quotes?${params}`)
    if (!response.ok) {
      const body = await response.json().catch(() => ({})) as { error?: unknown }
      throw new QuoteListError(response.status, response.status === 403 ? 'Quotes are not available to you in this workspace.' : typeof body.error === 'string' ? body.error : `Quotes could not be loaded (${response.status}).`)
    }
    const rows = await response.json() as QuoteRow[]
    out.push(...rows.map(normalise))
    cursor = response.headers.get('X-Next-Cursor') ?? ''
    if (!cursor) return { items: out, partial: false }
    if (seen.has(cursor)) return { items: out, partial: true }
    seen.add(cursor)
  }
  return { items: out, partial: true }
}
export { APIError }

// ---------- Presentation ----------
export const numberOf = (row: Pick<QuoteRow, 'offer_no' | 'key'>) => row.offer_no || row.key || 'Draft'
export const titleOf = (row: Pick<QuoteRow, 'title'>) => row.title || 'Untitled quote'
export const amountOf = (row: Pick<QuoteRow, 'net_total_cents' | 'currency'>) =>
  row.net_total_cents === undefined ? '' : minorMoney(row.net_total_cents, row.currency ?? '')
const DAY = new Intl.DateTimeFormat('en-GB', { day: 'numeric', month: 'short', year: 'numeric', timeZone: 'UTC' })
// A calendar day ("2026-10-24") as the app writes dates: "24 Oct 2026".
export function dayText(iso: string | undefined): string {
  if (!iso || !/^\d{4}-\d{2}-\d{2}/.test(iso)) return ''
  const date = new Date(`${iso.slice(0, 10)}T00:00:00Z`)
  return Number.isNaN(date.getTime()) ? '' : DAY.format(date)
}
export function todayIso(now = new Date()): string {
  return `${now.getFullYear()}-${String(now.getMonth() + 1).padStart(2, '0')}-${String(now.getDate()).padStart(2, '0')}`
}

// ---------- Filtering ----------
export type DatePreset = 'any' | '30d' | 'month' | 'quarter' | 'year' | 'custom'
export interface QuoteFilter {
  q: string; statuses: QuoteStatus[]; customers: string[]
  date: { preset: DatePreset; from: string; to: string }
  amount: { min: string; max: string }
  archived: boolean
}
export const NO_FILTER: QuoteFilter = { q: '', statuses: [], customers: [], date: { preset: 'any', from: '', to: '' }, amount: { min: '', max: '' }, archived: false }
export const blankFilter = (): QuoteFilter => ({ ...NO_FILTER, statuses: [], customers: [], date: { ...NO_FILTER.date }, amount: { ...NO_FILTER.amount } })

const shift = (iso: string, days: number) => {
  const d = new Date(`${iso}T00:00:00Z`); d.setUTCDate(d.getUTCDate() + days)
  return d.toISOString().slice(0, 10)
}
// The quote-date range a preset stands for, as inclusive ISO days ('' = open).
export function dateRange(date: QuoteFilter['date'], today: string): { from: string; to: string } {
  const [y, m] = today.split('-').map(Number) as [number, number]
  switch (date.preset) {
    case '30d': return { from: shift(today, -29), to: today }
    case 'month': return { from: `${y}-${String(m).padStart(2, '0')}-01`, to: today }
    case 'quarter': { const q = Math.floor((m - 1) / 3) * 3 + 1; return { from: `${y}-${String(q).padStart(2, '0')}-01`, to: today } }
    case 'year': return { from: `${y}-01-01`, to: today }
    case 'custom': return { from: date.from, to: date.to }
    default: return { from: '', to: '' }
  }
}
export const DATE_PRESETS: { id: DatePreset; label: string }[] = [
  { id: 'any', label: 'Any date' }, { id: '30d', label: 'Last 30 days' }, { id: 'month', label: 'This month' },
  { id: 'quarter', label: 'This quarter' }, { id: 'year', label: 'This year' }, { id: 'custom', label: 'Between…' },
]
// Amount bounds typed in major units ("1,500" or "1500.50"); invalid bounds are ignored, never guessed.
export function amountBounds(amount: QuoteFilter['amount']): { min: number | null; max: number | null; invalid: boolean } {
  const min = parseMoney(amount.min, 'EUR'), max = parseMoney(amount.max, 'EUR')
  return { min: typeof min === 'number' ? min : null, max: typeof max === 'number' ? max : null, invalid: min === 'invalid' || max === 'invalid' }
}
export function dateActive(f: QuoteFilter) { return f.date.preset !== 'any' && (f.date.preset !== 'custom' || !!f.date.from || !!f.date.to) }
export function amountActive(f: QuoteFilter) { const b = amountBounds(f.amount); return b.min !== null || b.max !== null }
export function narrowed(f: QuoteFilter) {
  return !!f.q.trim() || f.statuses.length > 0 || f.customers.length > 0 || dateActive(f) || amountActive(f)
}
const lower = (v: string | undefined | null) => (v ?? '').toLowerCase()
export function matchesQuote(row: QuoteRow, f: QuoteFilter, today: string): boolean {
  if (row.archived && !f.archived) return false
  if (f.statuses.length && !f.statuses.includes(statusOf(row))) return false
  if (f.customers.length && !f.customers.includes(row.customer_org_node_id)) return false
  if (dateActive(f)) {
    const { from, to } = dateRange(f.date, today)
    const day = (row.offer_date || row.created_at).slice(0, 10)
    if (!day || (from && day < from) || (to && day > to)) return false
  }
  const bounds = amountBounds(f.amount)
  if (bounds.min !== null || bounds.max !== null) {
    // Amounts compare in each quote's own currency; a quote without a total never matches a bound.
    if (row.net_total_cents === undefined) return false
    if (bounds.min !== null && row.net_total_cents < bounds.min) return false
    if (bounds.max !== null && row.net_total_cents > bounds.max) return false
  }
  const q = f.q.trim().toLowerCase()
  if (!q) return true
  return [row.offer_no, row.key, row.title, row.customer_name, row.project_ref, STATUS_META[statusOf(row)].label].some(v => lower(v).includes(q))
}

// ---------- Sorting ----------
export type SortKey = 'number' | 'title' | 'customer' | 'status' | 'date' | 'valid' | 'amount' | 'updated'
export interface Sort { key: SortKey; dir: 'asc' | 'desc' }
export const DEFAULT_SORT: Sort = { key: 'number', dir: 'desc' }
// Newest first for numbers, dates and amounts; A to Z for words.
export const firstDir = (key: SortKey): 'asc' | 'desc' => ['number', 'date', 'valid', 'amount', 'updated'].includes(key) ? 'desc' : 'asc'
const collator = new Intl.Collator('en', { numeric: true, sensitivity: 'base' })
export function sortQuotes(list: QuoteRow[], sort: Sort): QuoteRow[] {
  const value = (r: QuoteRow): string | number | null => {
    switch (sort.key) {
      case 'title': return r.title || null
      case 'customer': return r.customer_name || null
      case 'status': return STATUS_META[statusOf(r)].order
      case 'date': return r.offer_date || r.created_at.slice(0, 10) || null
      case 'valid': return r.valid_until || null
      case 'amount': return r.net_total_cents ?? null
      case 'updated': return r.updated_at || null
      default: return r.offer_no || r.key || null
    }
  }
  const sign = sort.dir === 'asc' ? 1 : -1
  return [...list].sort((a, b) => {
    const x = value(a), y = value(b)
    // Blanks go last either way; ties fall back to the newest first.
    if (x === null || y === null) return x === y ? 0 : x === null ? 1 : -1
    const c = typeof x === 'number' && typeof y === 'number' ? x - y : collator.compare(String(x), String(y))
    return c * sign || b.created_at.localeCompare(a.created_at) || a.quote_node_id.localeCompare(b.quote_node_id)
  })
}

// ---------- Columns ----------
export type ColumnId = Exclude<SortKey, 'updated'>
export interface ColumnDef { id: ColumnId; label: string; width: number; min: number; max: number; end?: boolean }
// Title takes what the others leave; each other column keeps its own width.
export const COLUMNS: ColumnDef[] = [
  { id: 'number', label: 'Number', width: 150, min: 110, max: 260 },
  { id: 'title', label: 'Quote', width: 0, min: 200, max: 4000 },
  { id: 'customer', label: 'Customer', width: 220, min: 120, max: 420 },
  { id: 'status', label: 'Status', width: 124, min: 104, max: 200 },
  { id: 'date', label: 'Date', width: 124, min: 100, max: 200 },
  { id: 'valid', label: 'Valid until', width: 140, min: 120, max: 220 },
  { id: 'amount', label: 'Net amount', width: 150, min: 110, max: 260, end: true },
]
export const COLUMN_BY_ID = new Map(COLUMNS.map(c => [c.id, c]))
// The order columns leave in when the table narrows: the least essential first.
const DROP: ColumnId[] = ['valid', 'date', 'amount', 'customer']
export function clampWidth(id: ColumnId, width: number | undefined): number {
  const def = COLUMN_BY_ID.get(id)!
  return typeof width === 'number' && Number.isFinite(width) ? Math.max(def.min, Math.min(def.max, Math.round(width))) : def.width
}
// The title never shrinks below this, whatever the other columns were dragged to;
// the number keeps room for its whole chip (A260924-13).
export const TITLE_FLOOR = 150
const FIT_FLOOR: Partial<Record<ColumnId, number>> = { number: 126 }
// The widths the visible columns get in a table this wide: each its own (or its
// dragged) width; when the title would drop below its minimum, the other columns
// give width back down to their minimums, in order (the number first), and the
// title takes what is left, never less than TITLE_FLOOR.
export function fitColumns(ids: ColumnId[], tableWidth: number, widths: Partial<Record<ColumnId, number>> = {}): Record<string, number> {
  const out: Record<string, number> = {}
  const others = ids.filter(id => id !== 'title')
  for (const id of others) out[id] = clampWidth(id, widths[id])
  const used = () => others.reduce((sum, id) => sum + out[id]!, 0)
  let excess = used() + COLUMN_BY_ID.get('title')!.min - tableWidth
  for (const id of others) {
    if (excess <= 0) break
    const give = Math.min(excess, out[id]! - Math.max(FIT_FLOOR[id] ?? 0, COLUMN_BY_ID.get(id)!.min))
    out[id]! -= give
    excess -= give
  }
  out.title = Math.max(TITLE_FLOOR, Math.round(tableWidth - used()))
  return out
}
export function visibleColumns(tableWidth: number, widths: Partial<Record<ColumnId, number>> = {}): ColumnId[] {
  let ids = COLUMNS.map(c => c.id)
  const total = () => ids.reduce((sum, id) => sum + (id === 'title' ? COLUMN_BY_ID.get('title')!.min : clampWidth(id, widths[id])), 0)
  for (const id of DROP) { if (tableWidth <= 0 || total() <= tableWidth) break; ids = ids.filter(x => x !== id) }
  return ids
}
