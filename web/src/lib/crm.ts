// SPDX-License-Identifier: AGPL-3.0-only
// Customers (CRM, /api/crm): the records, the calls that read and change them,
// and what the screens derive from them (search, sort, filters, columns,
// addresses, amounts, a line diff for note proposals). A change replaces the
// whole record and carries its revision; the server refuses a stale one with
// 409. Undo goes through the event log. Free of Vue for unit tests.
import { api, listNodes } from './api.ts'
import type { RowAction } from './rowActions.ts'

export interface Address { street: string; postal_code: string; city: string; country: string; freeform: string }
export interface CustomerFields {
  legal_name: string; industry: string; website: string; domain: string; phone: string; description: string; customer_notes: string
  vat_id: string; tax_id: string; register_no: string; employee_count: number | null; annual_revenue_minor: number | null; currency: string
  billing_address: Address | null; visiting_address: Address | null; hourly_rate_minor: number | null; lp_rate_minor: number | null
  external_provider: string; external_id: string; external_url: string
}
export interface Customer extends CustomerFields { id: string; key: string; name: string; revision: number; customer_no: string | null; primary_contact_node_id: string | null; archived?: boolean }
export interface ContactFields { email: string; phone: string; role: string; note: string; external_provider: string; external_id: string; external_url: string }
export interface Contact extends ContactFields { id: string; key: string; organisation_node_id: string; name: string; revision: number; primary: boolean }
export interface Cooperation { engagement: string; ownership: string; environment_responsibility: string; sla: string; report_contract: string; revision: number }
export interface RelatedProject { id: string; key: string; title: string; state: string; cooperation: Cooperation; cooperation_revision: number }
export interface RelatedQuote { id: string; offer_no: string | null; state: string; archived: boolean }
export interface RelatedHours { project_node_id: string; currency: string; duration_seconds: number; amount: string }
export interface RelatedDocument { attachment_id: string; node_id: string; name: string; title: string; category: string; status: string; valid_from: string | null; valid_until: string | null; revision: number }
export interface Related { projects: RelatedProject[]; quotes: RelatedQuote[]; hours: RelatedHours[]; documents: RelatedDocument[] }
export interface Provider { id: string; enabled: boolean; configured: boolean; revision: number }
export interface RemoteCustomer { provider: string; external_id: string; name: string; fields: CustomerFields }
export interface ProviderSyncStatus { state: 'never' | 'ok' | 'error'; provider_id: string; attempted_at: string | null; synced_at: string | null; error: string }
export interface NoteDraft { id: string; organisation_node_id: string; draft_text: string; applied: boolean }
// A proposal as this session keeps it: the draft, and the customer it was made against.
export interface NoteProposal extends NoteDraft { base_revision: number; base_text: string }
// The primary contact as the list shows it (read from the contact nodes).
export interface ContactCard { name: string; email: string; role: string }

// ---------- Calls ----------
export class CRMError extends Error {
  readonly status: number
  readonly code: string
  constructor(status: number, code: string, message: string) { super(message); this.status = status; this.code = code }
}
async function send<T>(path: string, method = 'GET', body?: unknown): Promise<T> {
  const response = await api(`/crm${path}`, body === undefined ? { method } : { method, headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) })
  if (!response.ok) {
    const error = await response.json().catch(() => ({}))
    const message = typeof error?.message === 'string' ? error.message : typeof error?.error === 'string' ? error.error : ''
    throw new CRMError(response.status, typeof error?.code === 'string' ? error.code : '', plainError(response.status, message))
  }
  return response.status === 204 ? (undefined as T) : await response.json() as T
}
// What went wrong, as people say it.
export function plainError(status: number, message: string) {
  if (status === 409 && /not enabled|closed/i.test(message)) return 'Customers are not enabled for this workspace.'
  if (status === 409) return 'This customer was changed elsewhere. Reload to see the newest version.'
  if (status === 403) return 'Only a workspace admin can change customers.'
  if (status === 404) return 'This customer or contact no longer exists.'
  if (status === 400 && /email/i.test(message)) return 'That email address does not look right.'
  if (status === 400 && /currency/i.test(message)) return 'Use a three-letter currency code, such as EUR.'
  if (status === 400 && /provider/i.test(message)) return 'An external link needs both its provider and its id.'
  if (status === 400) return 'Some fields were not accepted. Check the web address and the lengths.'
  if (status === 0 || status >= 500) return 'The server did not answer. Please try again.'
  return 'That did not work. Please try again.'
}
export const errorText = (e: unknown, fallback = 'That did not work. Please try again.') => e instanceof Error && e.message ? e.message : fallback
export const statusOf = (e: unknown) => e instanceof CRMError ? e.status : 0

// Every customer, archived ones included, page by page (the list searches, sorts
// and filters in the browser; pickers leave archived customers out).
export async function listCustomers(): Promise<Customer[]> {
  const all: Customer[] = []
  for (let offset = 0, page = 0; page < 50; page++) {
    const body = await send<{ items: Customer[]; next_offset: number | null }>(`/organisations?limit=100&offset=${offset}&archived=all`)
    all.push(...body.items)
    if (body.next_offset == null) break
    offset = body.next_offset
  }
  return all
}
const seg = encodeURIComponent
export const getCustomer = (id: string) => send<Customer>(`/organisations/${seg(id)}`)
export const createCustomer = (write: CustomerWrite) => send<Customer>('/organisations', 'POST', write)
// Archiving hides a customer from lists and pickers; its quotes, projects and hours stay.
export const setCustomerArchived = (id: string, expectedRevision: number, archived: boolean) => send<Customer>(`/organisations/${seg(id)}/visibility`, 'PATCH', { expected_revision: expectedRevision, archived })
export const updateCustomer = (id: string, write: CustomerWrite, expectedRevision: number) => send<Customer>(`/organisations/${seg(id)}`, 'PATCH', { ...write, expected_revision: expectedRevision })
export const deleteCustomer = (id: string) => send<void>(`/organisations/${seg(id)}`, 'DELETE')
export async function listContacts(id: string): Promise<Contact[]> {
  const out: Contact[] = []
  const seen = new Set<string>()
  let cursor = ''
  for (let page = 0; page < 20; page++) {
    const response = await api(`/crm/organisations/${seg(id)}/contacts?limit=100${cursor ? `&after_id=${seg(cursor)}` : ''}`)
    if (!response.ok) throw new CRMError(response.status, '', plainError(response.status, ''))
    out.push(...await response.json() as Contact[])
    cursor = response.headers.get('X-Next-Cursor') ?? ''
    if (!cursor) return out
    if (seen.has(cursor)) throw new Error('Contact paging did not advance.')
    seen.add(cursor)
  }
  throw new Error('More than 2,000 contacts. Narrow the customer selection.')
}
export const createContact = (orgId: string, write: ContactWrite) => send<Contact>(`/organisations/${seg(orgId)}/contacts`, 'POST', write)
export const updateContact = (id: string, write: ContactWrite, expectedRevision: number) => send<Contact>(`/contacts/${seg(id)}`, 'PATCH', { ...write, expected_revision: expectedRevision })
export const deleteContact = (id: string) => send<void>(`/contacts/${seg(id)}`, 'DELETE')
export const makePrimary = (orgId: string, contactId: string, expectedRevision: number) => send<Customer>(`/organisations/${seg(orgId)}/primary-contact`, 'POST', { contact_node_id: contactId, expected_revision: expectedRevision })
export const getRelated = (id: string) => send<Related>(`/organisations/${seg(id)}/related`)
export const setDocumentMetadata = (doc: RelatedDocument, data: Pick<RelatedDocument, 'title' | 'category' | 'status' | 'valid_from' | 'valid_until'>) => send<RelatedDocument>(`/documents/${seg(doc.attachment_id)}/metadata`, 'PUT', { ...data, expected_revision: doc.revision })
export const setCooperation = (project: RelatedProject, data: Omit<Cooperation, 'revision'>) => send<Cooperation>(`/projects/${seg(project.id)}/cooperation`, 'PUT', { ...data, expected_revision: project.cooperation_revision })
export const draftNote = (id: string, text: string, expectedRevision: number) => send<NoteDraft>(`/organisations/${seg(id)}/note-rewrite`, 'POST', { draft_text: text, expected_revision: expectedRevision })
export const applyNote = (id: string, draftId: string) => send<Customer>(`/organisations/${seg(id)}/note-rewrite/${seg(draftId)}/apply`, 'POST')
export const listProviders = () => send<Provider[]>('/providers')
export const configureProvider = (provider: Provider, enabled: boolean, secretRef: string) => send<Provider>(`/providers/${seg(provider.id)}/config`, 'PUT', { enabled, secret_ref: secretRef, expected_revision: provider.revision })
export const searchProviders = (q: string) => send<RemoteCustomer[]>(`/providers/search?q=${seg(q)}`)
export const importProvider = (providerId: string, externalId: string) => send<Customer>(`/providers/${seg(providerId)}/import`, 'POST', { external_id: externalId })
export const syncProvider = (id: string) => send<Customer>(`/organisations/${seg(id)}/sync`, 'POST')
export const getProviderSyncStatus = (id: string) => send<ProviderSyncStatus>(`/organisations/${seg(id)}/sync-status`)

// Primary contacts for the list: one read of the contact nodes (names, email,
// role), not one request per customer. Missing on failure; the list still works.
export async function contactDirectory(): Promise<Map<string, ContactCard>> {
  const out = new Map<string, ContactCard>()
  let cursor: string | undefined
  for (let page = 0; page < 20; page++) {
    const body = await listNodes({ kind: ['contact'], limit: 500, cursor })
    for (const node of body.items) {
      const f = node.fields ?? {}
      out.set(node.id, { name: node.title, email: typeof f.email === 'string' ? f.email : '', role: typeof f.role === 'string' ? f.role : '' })
    }
    if (!body.next_cursor) break
    cursor = body.next_cursor
  }
  return out
}

// ---------- Undo through the event log ----------
// The newest change of `types` on `nodeId` that is not itself an undo.
export async function latestEvent(nodeId: string, types: string[]): Promise<number | null> {
  let after = 0, found: number | null = null
  for (let page = 0; page < 50; page++) {
    const response = await api(`/events?node_id=${seg(nodeId)}&limit=200${after ? `&after=${after}` : ''}`)
    if (!response.ok) return found
    const body = await response.json() as { items?: { id: number; type: string; undo_of: number | null }[]; next_after?: number | null }
    for (const event of body.items ?? []) if (types.includes(event.type) && event.undo_of == null) found = Number(event.id)
    if (!body.next_after) break
    after = Number(body.next_after)
  }
  return found
}
// Undo the newest matching change of each step, in order (a restored contact
// before the primary choice that pointed away from it).
export async function undoLatest(steps: { node: string; types: string[] }[]): Promise<void> {
  for (const step of steps) {
    const id = await latestEvent(step.node, step.types)
    if (id == null) throw new Error('There is nothing to undo.')
    const response = await api(`/events/${id}/undo`, { method: 'POST' })
    if (!response.ok) throw new Error(response.status === 409 ? 'It changed again since, so it cannot be undone.' : response.status === 403 ? 'Only a workspace admin can undo this.' : 'Undo did not work. Please try again.')
  }
}

// ---------- Writes ----------
export type CustomerWrite = { name: string } & CustomerFields
export type ContactWrite = { name: string } & ContactFields
export const emptyAddress = (): Address => ({ street: '', postal_code: '', city: '', country: '', freeform: '' })
export function blankCustomer(name = ''): CustomerWrite {
  return {
    name, legal_name: '', industry: '', website: '', domain: '', phone: '', description: '', customer_notes: '', vat_id: '', tax_id: '', register_no: '',
    employee_count: null, annual_revenue_minor: null, currency: '', billing_address: null, visiting_address: null, hourly_rate_minor: null, lp_rate_minor: null,
    external_provider: '', external_id: '', external_url: '',
  }
}
export const blankContact = (name = ''): ContactWrite => ({ name, email: '', phone: '', role: '', note: '', external_provider: '', external_id: '', external_url: '' })
// The whole record as the server takes it back (a PATCH replaces every field).
export function customerWrite(c: Customer): CustomerWrite {
  const base = blankCustomer(c.name) as unknown as Record<string, unknown>
  for (const key of Object.keys(base)) { const value = (c as unknown as Record<string, unknown>)[key]; if (value !== undefined && value !== null) base[key] = value }
  return base as unknown as CustomerWrite
}
export function contactWrite(c: Contact): ContactWrite {
  return { name: c.name, email: c.email ?? '', phone: c.phone ?? '', role: c.role ?? '', note: c.note ?? '', external_provider: c.external_provider ?? '', external_id: c.external_id ?? '', external_url: c.external_url ?? '' }
}
// An address with nothing in it is no address.
export function tidyAddress(a: Address | null): Address | null {
  if (!a) return null
  const t = { street: a.street.trim(), postal_code: a.postal_code.trim(), city: a.city.trim(), country: a.country.trim(), freeform: a.freeform.trim() }
  return Object.values(t).some(Boolean) ? t : null
}
// "hofer.at" as a web address the server accepts.
export function normalizeWebsite(text: string) {
  const t = text.trim()
  if (!t) return ''
  return /^[a-z][a-z0-9+.-]*:\/\//i.test(t) ? t : `https://${t}`
}
export function validWebsite(text: string) {
  if (!text) return true
  try { const u = new URL(text); return (u.protocol === 'https:' || u.protocol === 'http:') && !!u.host && !u.username } catch { return false }
}
export const validEmail = (text: string) => !text || /^[^\s@<>()[\]",;:]+@[^\s@<>()[\]",;:]+\.[^\s@<>()[\]",;:]+$/.test(text)

// ---------- Edit mode: the whole customer as text fields ----------
export interface CustomerDraft {
  name: string; legal_name: string; industry: string; website: string; domain: string; phone: string; description: string; customer_notes: string
  vat_id: string; tax_id: string; register_no: string; employees: string; revenue: string; currency: string; hourly: string; lp: string
  billing: Address; visiting: Address
}
export function draftOf(c: Customer): CustomerDraft {
  const cur = c.currency ?? ''
  return {
    name: c.name, legal_name: c.legal_name ?? '', industry: c.industry ?? '', website: c.website ?? '', domain: c.domain ?? '', phone: c.phone ?? '',
    description: c.description ?? '', customer_notes: c.customer_notes ?? '', vat_id: c.vat_id ?? '', tax_id: c.tax_id ?? '', register_no: c.register_no ?? '',
    employees: c.employee_count == null ? '' : String(c.employee_count), revenue: minorInput(c.annual_revenue_minor, cur), currency: cur,
    hourly: minorInput(c.hourly_rate_minor, cur), lp: minorInput(c.lp_rate_minor, cur),
    billing: { ...emptyAddress(), ...(c.billing_address ?? {}) }, visiting: { ...emptyAddress(), ...(c.visiting_address ?? {}) },
  }
}
export type DraftProblems = Partial<Record<'name' | 'website' | 'currency' | 'hourly' | 'lp' | 'employees' | 'revenue', string>>
export function draftProblems(d: CustomerDraft): DraftProblems {
  const out: DraftProblems = {}
  const currency = d.currency.trim().toUpperCase()
  if (!d.name.trim()) out.name = 'A name is needed.'
  else if (d.name.trim().length > 200) out.name = 'At most 200 characters.'
  if (!validWebsite(normalizeWebsite(d.website))) out.website = 'Use a web address like hofer.at.'
  if (currency && !/^[A-Z]{3}$/.test(currency)) out.currency = 'Three letters, such as EUR.'
  if (parseMoney(d.hourly, currency) === 'invalid') out.hourly = 'An amount like 95 or 95.50.'
  if (parseMoney(d.lp, currency) === 'invalid') out.lp = 'An amount like 120 or 120.50.'
  if (parseMoney(d.revenue, currency) === 'invalid') out.revenue = 'An amount like 1200000.'
  if (parseCount(d.employees) === 'invalid') out.employees = 'A whole number.'
  if (!currency && (d.hourly.trim() || d.lp.trim() || d.revenue.trim())) out.currency = 'Amounts need a currency, such as EUR.'
  return out
}
// The draft as the server's full write; fields the page does not edit (the
// link to another CRM) are kept from the customer as it is.
export function writeOf(d: CustomerDraft, base: Customer): CustomerWrite {
  const currency = d.currency.trim().toUpperCase()
  const money = (text: string) => { const v = parseMoney(text, currency); return v === 'invalid' ? null : v }
  const count = parseCount(d.employees)
  return {
    ...customerWrite(base),
    name: d.name.trim(), legal_name: d.legal_name.trim(), industry: d.industry.trim(), website: normalizeWebsite(d.website), domain: d.domain.trim(), phone: d.phone.trim(),
    description: d.description, customer_notes: d.customer_notes, vat_id: d.vat_id.trim(), tax_id: d.tax_id.trim(), register_no: d.register_no.trim(),
    employee_count: count === 'invalid' ? null : count, annual_revenue_minor: money(d.revenue), currency, hourly_rate_minor: money(d.hourly), lp_rate_minor: money(d.lp),
    billing_address: tidyAddress(d.billing), visiting_address: tidyAddress(d.visiting),
  }
}
export const sameDraft = (a: CustomerDraft, b: CustomerDraft) => JSON.stringify(a) === JSON.stringify(b)

// ---------- Reading ----------
export function addressLines(a: Address | null): string[] {
  if (!a) return []
  if (a.freeform.trim() && !a.street.trim() && !a.city.trim()) return a.freeform.split('\n').map(l => l.trim()).filter(Boolean)
  const place = [a.postal_code, a.city].map(s => s.trim()).filter(Boolean).join(' ')
  return [a.street, place, a.country].map(l => l.trim()).filter(Boolean)
}
export const addressOf = (c: Pick<Customer, 'billing_address' | 'visiting_address'>) => c.billing_address ?? c.visiting_address
export const countryOf = (c: Pick<Customer, 'billing_address' | 'visiting_address'>) => addressOf(c)?.country.trim() ?? ''
export const placeOf = (c: Pick<Customer, 'billing_address' | 'visiting_address'>) => {
  const a = addressOf(c)
  return a ? [a.city.trim(), a.country.trim()].filter(Boolean).join(', ') : ''
}
const scaleOf = (currency: string) => ['JPY', 'KRW', 'ISK', 'CLP', 'VND'].includes(currency.toUpperCase()) ? 0 : 2
// Minor units as money: 9500 in EUR reads "95.00 EUR"; without a currency, "95.00".
export function minorMoney(minor: number | null, currency: string) {
  if (minor == null) return ''
  const code = currency.trim().toUpperCase()
  const scale = scaleOf(code)
  const whole = Math.floor(minor / 10 ** scale).toString().replace(/\B(?=(\d{3})+(?!\d))/g, ',')
  const frac = scale ? `.${String(minor % 10 ** scale).padStart(scale, '0')}` : ''
  return code ? `${whole}${frac} ${code}` : `${whole}${frac}`
}
// Minor units as the field shows them for editing: 9500 → "95.00".
export function minorInput(minor: number | null, currency: string) {
  if (minor == null) return ''
  const scale = scaleOf(currency)
  return scale ? `${Math.floor(minor / 10 ** scale)}.${String(minor % 10 ** scale).padStart(scale, '0')}` : String(minor)
}
// "12.50" typed for a rate becomes 1250 minor units; empty is no amount.
export function parseMoney(text: string, currency: string): number | null | 'invalid' {
  const t = text.trim().replace(/[\s,’']/g, '')
  if (!t) return null
  const scale = scaleOf(currency)
  const m = /^(\d{1,13})(?:\.(\d+))?$/.exec(t)
  if (!m || (m[2] && m[2].length > scale)) return 'invalid'
  return Number(m[1]) * 10 ** scale + Number((m[2] ?? '').padEnd(scale, '0') || 0)
}
export function parseCount(text: string): number | null | 'invalid' {
  const t = text.trim().replace(/[\s,’'.]/g, '')
  if (!t) return null
  return /^\d{1,9}$/.test(t) ? Number(t) : 'invalid'
}
export const websiteHost = (url: string) => { try { return new URL(url).host.replace(/^www\./, '') } catch { return url } }
export const telHref = (phone: string) => `tel:${phone.replace(/[^\d+]/g, '')}`

// ---------- List: search, filters, sort ----------
export interface CustomerFilter { q: string; number: 'all' | 'with' | 'without'; countries: string[]; industries: string[]; archived: boolean }
export const NO_FILTER: CustomerFilter = { q: '', number: 'all', countries: [], industries: [], archived: false }
export const filtered = (f: CustomerFilter) => f.number !== 'all' || f.countries.length > 0 || f.industries.length > 0
export type SortKey = 'name' | 'number' | 'contact' | 'place' | 'industry' | 'rate'
const lower = (s: string | null | undefined) => (s ?? '').toLowerCase()
export function matchesCustomer(c: Customer, f: CustomerFilter, contact?: ContactCard) {
  if (c.archived && !f.archived) return false
  if (f.number === 'with' && !c.customer_no) return false
  if (f.number === 'without' && c.customer_no) return false
  if (f.countries.length && !f.countries.includes(countryOf(c))) return false
  if (f.industries.length && !f.industries.includes(c.industry.trim())) return false
  const q = f.q.trim().toLowerCase()
  if (!q) return true
  return [c.name, c.legal_name, c.customer_no, c.industry, c.domain, c.website, contact?.name, contact?.email, placeOf(c)].some(v => lower(v).includes(q))
}
export function sortCustomers(list: Customer[], key: SortKey, dir: 'asc' | 'desc', contactName: (c: Customer) => string = () => '') {
  const value = (c: Customer): string | number => {
    switch (key) {
      case 'number': return c.customer_no ?? ''
      case 'contact': return contactName(c)
      case 'place': return placeOf(c)
      case 'industry': return c.industry.trim()
      case 'rate': return c.hourly_rate_minor ?? -1
      default: return c.name
    }
  }
  const collator = new Intl.Collator('en', { numeric: true, sensitivity: 'base' })
  return [...list].sort((a, b) => {
    const x = value(a), y = value(b)
    // Blanks go last either way.
    const blankX = x === '' || x === -1, blankY = y === '' || y === -1
    if (blankX !== blankY) return blankX ? 1 : -1
    const order = typeof x === 'number' && typeof y === 'number' ? x - y : collator.compare(String(x), String(y))
    return (dir === 'asc' ? order : -order) || collator.compare(a.name, b.name)
  })
}
export interface Facet { value: string; label: string; count: number }
export function facetOf(list: Customer[], pick: (c: Customer) => string): Facet[] {
  const counts = new Map<string, number>()
  for (const c of list) { const v = pick(c); if (v) counts.set(v, (counts.get(v) ?? 0) + 1) }
  return [...counts].map(([value, count]) => ({ value, label: value, count })).sort((a, b) => a.label.localeCompare(b.label))
}

// ---------- List columns ----------
export type ColumnId = SortKey
export interface ColumnDef { id: ColumnId; label: string; width: number; min: number; max: number; end?: boolean }
export const COLUMNS: ColumnDef[] = [
  { id: 'name', label: 'Customer', width: 0, min: 220, max: 4000 },
  { id: 'number', label: 'Number', width: 132, min: 96, max: 240 },
  { id: 'contact', label: 'Primary contact', width: 260, min: 140, max: 460 },
  { id: 'place', label: 'Location', width: 190, min: 110, max: 380 },
  { id: 'industry', label: 'Industry', width: 160, min: 96, max: 320 },
  { id: 'rate', label: 'Hourly rate', width: 136, min: 96, max: 220, end: true },
]
export const COLUMN_BY_ID = new Map(COLUMNS.map(c => [c.id, c]))
// The order columns leave in when the table narrows: the least essential first.
const DROP: ColumnId[] = ['industry', 'rate', 'place', 'contact']
export function clampWidth(id: ColumnId, width: number | undefined) {
  const def = COLUMN_BY_ID.get(id)!
  return typeof width === 'number' && Number.isFinite(width) ? Math.max(def.min, Math.min(def.max, Math.round(width))) : def.width
}
// Name and Number always show; the others step aside while Name would drop under its minimum.
export function visibleColumns(tableWidth: number, widths: Partial<Record<ColumnId, number>> = {}): ColumnId[] {
  let ids = COLUMNS.map(c => c.id)
  const total = () => ids.reduce((sum, id) => sum + (id === 'name' ? COLUMN_BY_ID.get('name')!.min : clampWidth(id, widths[id])), 0)
  for (const id of DROP) { if (tableWidth <= 0 || total() <= tableWidth) break; ids = ids.filter(x => x !== id) }
  return ids
}

// Customer stops near NAME_TARGET on its own; spare width widens Primary
// contact, Location and Industry (not ones the person sized) up to their
// maximum, in proportion to their normal width. What is left goes to Customer.
export const NAME_TARGET = 360
const GROWS: ColumnId[] = ['contact', 'place', 'industry']
export function layoutWidths(ids: ColumnId[], tableWidth: number, sized: Partial<Record<ColumnId, number>> = {}): Partial<Record<ColumnId, number>> {
  const out: Partial<Record<ColumnId, number>> = {}
  for (const id of ids) if (id !== 'name') out[id] = clampWidth(id, sized[id])
  let spare = tableWidth - Object.values(out).reduce((sum, w) => sum + (w ?? 0), 0) - NAME_TARGET
  const growing = GROWS.filter(id => ids.includes(id) && sized[id] === undefined)
  while (spare >= 1 && growing.length) {
    const weight = growing.reduce((sum, id) => sum + COLUMN_BY_ID.get(id)!.width, 0)
    let used = 0
    for (const id of [...growing]) {
      const def = COLUMN_BY_ID.get(id)!
      const add = Math.min(def.max - out[id]!, spare * def.width / weight)
      out[id] = out[id]! + add; used += add
      if (out[id]! >= def.max - 0.5) growing.splice(growing.indexOf(id), 1)
    }
    spare -= used
    if (used < 0.5) break
  }
  for (const id of Object.keys(out) as ColumnId[]) out[id] = Math.floor(out[id]!)
  return out
}

// ---------- Note proposals: a line diff ----------
export type DiffLine = { kind: 'same' | 'add' | 'remove' | 'summary'; text: string }
export const DIFF_CELL_BUDGET = 250_000
export const DIFF_LINE_BUDGET = 2_000
export function lineDiff(before: string, after: string): DiffLine[] {
  // Bound text before splitting, and matrix work before allocating. Reviews
  // explain omitted detail instead of freezing the render thread.
  if (before.length + after.length > 1_000_000) return [{ kind: 'summary', text: 'Text is too large for a line comparison. Review the two full versions.' }]
  const a = before ? before.split('\n') : [], b = after ? after.split('\n') : []
  let start = 0, endA = a.length, endB = b.length
  while (start < endA && start < endB && a[start] === b[start]) start++
  while (endA > start && endB > start && a[endA - 1] === b[endB - 1]) { endA--; endB-- }
  const n = endA - start, m = endB - start
  if ((n + 1) * (m + 1) > DIFF_CELL_BUDGET || a.length + b.length > DIFF_LINE_BUDGET) {
    return [{ kind: 'summary', text: n || m ? `Large comparison: ${n} lines before and ${m} lines after in the changed region; ${start} common leading and ${a.length - endA} common trailing lines. Review the two full versions.` : `The text is identical (${a.length} lines).` }]
  }
  const lcs = Array.from({ length: n + 1 }, () => new Uint32Array(m + 1))
  for (let i = n - 1; i >= 0; i--) for (let j = m - 1; j >= 0; j--) lcs[i][j] = a[start + i] === b[start + j] ? lcs[i + 1][j + 1] + 1 : Math.max(lcs[i + 1][j], lcs[i][j + 1])
  const out: DiffLine[] = a.slice(0, start).map(text => ({ kind: 'same', text }))
  let i = 0, j = 0
  while (i < n && j < m) {
    if (a[start + i] === b[start + j]) { out.push({ kind: 'same', text: a[start + i] }); i++; j++ }
    else if (lcs[i + 1][j] >= lcs[i][j + 1]) out.push({ kind: 'remove', text: a[start + i++] })
    else out.push({ kind: 'add', text: b[start + j++] })
  }
  while (i < n) out.push({ kind: 'remove', text: a[start + i++] })
  while (j < m) out.push({ kind: 'add', text: b[start + j++] })
  out.push(...a.slice(endA).map(text => ({ kind: 'same' as const, text })))
  return out
}
export const diffCounts = (lines: DiffLine[]) => ({ added: lines.filter(l => l.kind === 'add').length, removed: lines.filter(l => l.kind === 'remove').length })

// ---------- Row actions ----------
// What a customer row offers: open it, start a quote for it, copy its number,
// archive or restore it. Archiving is an admin's and can be undone; a customer
// is deleted on its own page, where what depends on it is shown first.
export type CustomerActionId = 'open' | 'quote' | 'copyNumber' | 'archive' | 'restore'
export function customerActions(c: Customer, ctx: { admin: boolean; canQuote: boolean; busy?: CustomerActionId | null }): (RowAction & { id: CustomerActionId })[] {
  const out: (RowAction & { id: CustomerActionId })[] = []
  const add = (a: RowAction & { id: CustomerActionId }) => out.push(ctx.busy === a.id ? { ...a, busy: true } : a)
  add({ id: 'open', label: 'Open', icon: 'expand', group: 0, keys: 'Enter' })
  if (ctx.canQuote) add({ id: 'quote', label: 'New quote for this customer…', icon: 'document', group: 0, reason: c.archived ? 'Restore it from the archive first.' : undefined })
  if (c.customer_no) add({ id: 'copyNumber', label: 'Copy customer number', icon: 'tag', group: 1 })
  if (ctx.admin) add(c.archived ? { id: 'restore', label: 'Restore from the archive', icon: 'rollback', group: 2 } : { id: 'archive', label: 'Archive', icon: 'archive', group: 2 })
  return out
}
