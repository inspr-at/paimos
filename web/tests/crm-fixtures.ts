// SPDX-License-Identifier: AGPL-3.0-only
// An in-memory CRM API (customers, contacts, related work, note proposals, the
// event log with undo) plus the plugin catalog and quote settings, layered over
// mockWork for the Customers specs. Writes follow the server: a PATCH replaces
// the whole record and must carry the current revision (409 otherwise), the
// first contact becomes the primary one, and undo reverses an event once.
import type { Page, Route } from '@playwright/test'
import { me } from './work-fixtures'
import { mockEffectivePermissions } from './authz-fixtures'

type Address = { street: string; postal_code: string; city: string; country: string; freeform: string }
export interface MockCustomer {
  id: string; key: string; name: string; revision: number; customer_no: string | null; primary_contact_node_id: string | null
  legal_name: string; industry: string; website: string; domain: string; phone: string; description: string; customer_notes: string
  vat_id: string; tax_id: string; register_no: string; employee_count: number | null; annual_revenue_minor: number | null; currency: string
  billing_address: Address | null; visiting_address: Address | null; hourly_rate_minor: number | null; lp_rate_minor: number | null
  external_provider: string; external_id: string; external_url: string; deleted?: boolean; archived?: boolean
}
export interface MockContact {
  id: string; key: string; organisation_node_id: string; name: string; revision: number; primary?: boolean
  email: string; phone: string; role: string; note: string; external_provider: string; external_id: string; external_url: string; deleted?: boolean
}
type Event = { id: number; node_id: string; type: string; before: unknown; after: unknown; undo_of: number | null }
export interface CRMMockOptions {
  role?: 'admin' | 'member'
  enabled?: string[]
  // Plugin gate states to show: pinned to another digest, or granted fewer permissions.
  mismatch?: string[]; underGranted?: string[]
  kinds?: string[]
  providers?: { id: string; enabled: boolean; configured: boolean; revision: number }[]
  noQuotes?: boolean
  // The quote list (GET /api/quotes); empty unless given.
  quotes?: { quote_node_id: string; offer_no?: string; state: string }[]
  empty?: boolean
  // Someone else changes this customer just before this page's next write.
  meanwhile?: string
  quoteRevision?: number
  aiEnabled?: boolean
}

const DIGEST = 'cd'.repeat(32)
const PERMS: Record<string, string[]> = {
  business_costs: ['nodes.contribute', 'steps.apply', 'views.provide'],
  business_crm: ['integrations.call', 'nodes.contribute', 'steps.apply', 'tools.invoke', 'views.provide'],
  business_hours: ['steps.apply', 'views.provide'],
  business_quotes: ['nodes.contribute', 'steps.apply', 'views.provide'],
}
export const ORG_SCHEMA = { $id: 'urn:aeon:business_crm:organisation', type: 'object', additionalProperties: false, properties: { legal_name: { type: 'string', maxLength: 200 }, industry: { type: 'string', maxLength: 200 } } }
const at = (street: string, postal: string, city: string, country: string): Address => ({ street, postal_code: postal, city, country, freeform: '' })
function customer(id: string, key: string, name: string, extra: Partial<MockCustomer> = {}): MockCustomer {
  return {
    id, key, name, revision: 3, customer_no: null, primary_contact_node_id: null, legal_name: '', industry: '', website: '', domain: '', phone: '', description: '', customer_notes: '',
    vat_id: '', tax_id: '', register_no: '', employee_count: null, annual_revenue_minor: null, currency: '', billing_address: null, visiting_address: null, hourly_rate_minor: null, lp_rate_minor: null,
    external_provider: '', external_id: '', external_url: '', ...extra,
  }
}
function contact(id: string, org: string, name: string, extra: Partial<MockContact> = {}): MockContact {
  return { id, key: `CON-${id.slice(-2)}`, organisation_node_id: org, name, revision: 2, email: '', phone: '', role: '', note: '', external_provider: '', external_id: '', external_url: '', ...extra }
}
export const HOFER = 'org-hofer'
export const QUOTE_12 = '9b2f3c41-7d1e-4a8b-9c0d-2e3f4a5b6c12'
export const NORDSTERN = 'org-nordstern'
export const LUMEN = 'org-lumen'

export function crmData(options: CRMMockOptions = {}) {
  const enabled = new Set(options.enabled ?? ['business_costs', 'business_crm', 'business_quotes', 'business_hours'])
  const plugins = Object.keys(PERMS).map(id => {
    const pinned = enabled.has(id) || options.mismatch?.includes(id) || options.underGranted?.includes(id)
    return {
      id, version: id === 'business_crm' ? '3' : '2', digest_sha256: DIGEST, owner: 'aeon', permissions: PERMS[id],
      node_kinds: id === 'business_crm' ? [{ slug: 'organisation', field_schema: ORG_SCHEMA, allowed_child_kinds: [] }, { slug: 'contact', field_schema: { type: 'object', properties: {} }, allowed_child_kinds: [] }] : [],
      views: [{ id, panels: ['main'] }], workflow_steps: [], agent_tools: id === 'business_crm' ? [{ id: 'crm_note_optimize', permission: 'tools.invoke' }] : [], integrations: [], background_jobs: [],
      installation: pinned
        ? { manifest_digest_sha256: options.mismatch?.includes(id) ? 'ef'.repeat(32) : DIGEST, enabled: true, permissions: options.underGranted?.includes(id) ? PERMS[id].slice(1) : PERMS[id], plugin_id: id, version: id === 'business_crm' ? '3' : '2', updated_at: '2026-09-20T09:00:00Z' }
        : { manifest_digest_sha256: '0'.repeat(64), enabled: false, permissions: [], plugin_id: id, version: id === 'business_crm' ? '3' : '2', updated_at: '0001-01-01T00:00:00Z' },
    }
  })
  const kinds = (options.kinds ?? ['epic', 'ticket', 'task', 'project', 'cost_unit', 'organisation', 'contact', 'quote']).map(slug => ({ id: `k-${slug}`, slug, label: slug, short_prefix: slug.slice(0, 3).toUpperCase(), icon: slug, allowed_child_kinds: null, field_schema: {} }))
  const customers: MockCustomer[] = options.empty ? [] : [
    customer(HOFER, 'ORG-1', 'Bäckerei Hofer', {
      customer_no: 'K-0042', primary_contact_node_id: 'con-jana', legal_name: 'Hofer Backwaren GmbH', industry: 'Food', website: 'https://www.hofer-backwaren.at', phone: '+43 316 820 440',
      description: 'Family bakery with nine shops around Graz. Orders the web shop and the shop tablets from us.', vat_id: 'ATU58372910', register_no: 'FN 318822 t', employee_count: 84,
      currency: 'EUR', hourly_rate_minor: 9500, lp_rate_minor: 12000, billing_address: at('Herrengasse 14', '8010', 'Graz', 'Austria'), visiting_address: at('Herrengasse 14', '8010', 'Graz', 'Austria'),
      customer_notes: 'Prefers calls before 10:00.\nInvoices go to accounting, not to Jana.\nSummer break in August.',
    }),
    customer(NORDSTERN, 'ORG-2', 'Klinik Nordstern', { customer_no: 'K-0007', primary_contact_node_id: 'con-paul', industry: 'Health', currency: 'EUR', hourly_rate_minor: 11000, billing_address: at('Spitalgasse 23', '1090', 'Vienna', 'Austria'), website: 'https://nordstern.example' }),
    customer(LUMEN, 'ORG-3', 'Atelier Lumen', { industry: 'Design', billing_address: at('Türkenstraße 8', '80333', 'Munich', 'Germany') }),
    customer('org-meridian', 'ORG-4', 'Studio Meridian', { customer_no: 'K-0015', primary_contact_node_id: 'con-lea', legal_name: 'Meridian Software OG', industry: 'Software', currency: 'EUR', hourly_rate_minor: 10500, billing_address: at('Hauptplatz 3', '4020', 'Linz', 'Austria') }),
    customer('org-gruenwerk', 'ORG-5', 'Grünwerk Energie', { industry: 'Energy', billing_address: at('Alpenstraße 90', '5020', 'Salzburg', 'Austria') }),
    customer('org-vogel', 'ORG-6', 'Café Vogel', { customer_no: 'K-0021', primary_contact_node_id: 'con-tom', industry: 'Food', currency: 'EUR', hourly_rate_minor: 8500, billing_address: at('Sporgasse 2', '8010', 'Graz', 'Austria') }),
    customer('org-alder', 'ORG-7', 'Alder & Rowe Architects', { customer_no: 'K-0031', industry: 'Architecture', currency: 'GBP', hourly_rate_minor: 9000, billing_address: at('12 Clerkenwell Road', 'EC1M 5PQ', 'London', 'United Kingdom') }),
  ]
  const contacts: MockContact[] = options.empty ? [] : [
    contact('con-jana', HOFER, 'Jana Hofer', { role: 'Managing director', email: 'jana@hofer-backwaren.at', phone: '+43 664 120 3344' }),
    contact('con-max', HOFER, 'Max Brandl', { role: 'Accounting', email: 'buchhaltung@hofer-backwaren.at', note: 'Send invoices here, as PDF only.' }),
    contact('con-paul', NORDSTERN, 'Dr. Paul Weiss', { role: 'Head of IT', email: 'p.weiss@nordstern.example' }),
    contact('con-lea', 'org-meridian', 'Lea Aigner', { role: 'CTO', email: 'lea@meridian.example' }),
    contact('con-tom', 'org-vogel', 'Tom Vogel', { role: 'Owner', phone: '+43 316 77 12 90' }),
  ]
  const related: Record<string, { projects: unknown[]; quotes: unknown[]; hours: unknown[]; documents: unknown[] }> = {
    [HOFER]: {
      projects: [{ id: 'p-pharos', key: 'PRJ-17', title: 'Pharos', state: 'active', cooperation: {}, cooperation_revision: 1 }],
      quotes: [{ id: QUOTE_12, offer_no: 'Q-2026-0012', state: 'accepted', archived: false }, { id: '9b2f3c41-7d1e-4a8b-9c0d-2e3f4a5b6c19', offer_no: null, state: 'draft', archived: false }],
      hours: [{ project_node_id: 'p-pharos', currency: 'EUR', duration_seconds: 27000, amount: '712.5000' }],
      documents: [{ attachment_id: 'att-1', node_id: HOFER, name: 'framework-agreement.pdf', title: 'Framework agreement', category: 'contract', status: 'active', valid_from: '2026-01-01', valid_until: '2027-12-31', revision: 1 }],
    },
  }
  const quoteSettings = {
    revision: options.quoteRevision ?? 2, numbering_time_zone: options.quoteRevision === 0 ? '' : 'Europe/Vienna', default_currency: options.quoteRevision === 0 ? '' : 'EUR',
    sender: options.quoteRevision === 0 ? {} as Record<string, string> : { company: 'INSPR Studio', street: 'Annenstraße 1', postal_code: '8020', city: 'Graz', country: 'Austria', email: 'hello@inspr.example', uid: 'ATU77777777', iban: 'AT00 0000 0000 0000 0000', logo_file_id: 'file-logo' } as Record<string, string>,
    defaults: { intro: 'Thank you for your enquiry.' }, layout: { page_style: 'classic' }, smtp_confirmation_enabled: false, smtp_configured: false,
  }
  return { plugins, kinds, customers, contacts, related, quoteSettings, events: [] as Event[], drafts: [] as { id: string; org: string; base: number; text: string; applied: boolean }[], counter: { next: 1, event: 900 } }
}
export type CRMData = ReturnType<typeof crmData>
export interface CRMCall { path: string; method: string; body: unknown; query: URLSearchParams }

const view = (c: MockCustomer) => { const { deleted: _d, ...rest } = c; void _d; return { ...rest, archived: !!c.archived } }
const contactView = (data: CRMData, c: MockContact) => {
  const { deleted: _d, primary: _p, ...rest } = c; void _d; void _p
  return { ...rest, primary: data.customers.find(o => o.id === c.organisation_node_id)?.primary_contact_node_id === c.id }
}
const FIELDS = ['legal_name', 'industry', 'website', 'domain', 'phone', 'description', 'customer_notes', 'vat_id', 'tax_id', 'register_no', 'employee_count', 'annual_revenue_minor', 'currency', 'billing_address', 'visiting_address', 'hourly_rate_minor', 'lp_rate_minor', 'external_provider', 'external_id', 'external_url'] as const
const CONTACT_FIELDS = ['email', 'phone', 'role', 'note', 'external_provider', 'external_id', 'external_url'] as const

export async function mockCRM(page: Page, data: CRMData, options: CRMMockOptions = {}) {
  const calls: CRMCall[] = []
  const admin = (options.role ?? 'admin') === 'admin'
  let meanwhile = options.meanwhile
  const bad = (route: Route, status: number, code: string, message: string) => route.fulfill({ status, json: { code, message } })
  const event = (node: string, type: string, before: unknown, after: unknown) => data.events.push({ id: ++data.counter.event, node_id: node, type, before: structuredClone(before), after: structuredClone(after), undo_of: null })
  const live = (id: string) => data.customers.find(c => c.id === id && !c.deleted)
  const liveContact = (id: string) => data.contacts.find(c => c.id === id && !c.deleted)
  const crmOpen = () => {
    const p = data.plugins.find(x => x.id === 'business_crm')!
    return p.installation.enabled && p.installation.manifest_digest_sha256 === p.digest_sha256 && PERMS.business_crm.filter(x => x !== 'tools.invoke').every(x => p.installation.permissions.includes(x))
  }
  const handler = async (route: Route) => {
    const firstEvent = data.events.length
    const receipt = () => ({ 'X-Aeon-Event-Ids': data.events.slice(firstEvent).reverse().map(event => event.id).join(',') })
    const request = route.request(), url = new URL(request.url()), path = url.pathname, method = request.method(), q = url.searchParams
    let body: Record<string, unknown> = {}
    try { body = request.postDataJSON() ?? {} } catch { body = {} }
    const contactNodes = path === '/api/nodes' && method === 'GET' && q.get('kind') === 'contact'
    const known = path === '/api/me' || path === '/api/me/permissions' || path === '/api/plugins' || path.startsWith('/api/plugins/') || path === '/api/kinds' || path === '/api/business/principals'
      || path.startsWith('/api/crm/') || path === '/api/events' || /^\/api\/events\/\d+\/undo$/.test(path) || path === '/api/quotes/settings' || contactNodes
      || (path === '/api/quotes' && method === 'GET')
    if (!known) return route.fallback()
    calls.push({ path, method, body, query: q })
    if (path === '/api/me') return route.fulfill({ json: { principal: { id: me.id, name: me.name, kind: 'person', roles: [options.role ?? 'admin'] }, tenant: { id: 't1', name: 'INSPR Studio' } } })
    if (path === '/api/me/permissions') return route.fulfill({ json: mockEffectivePermissions(options.role ?? 'admin', q.get('project_id') ?? undefined) })
    if (path === '/api/plugins') return route.fulfill({ json: data.plugins })
    const install = /^\/api\/plugins\/([a-z_]+)\/installation$/.exec(path)
    if (install) {
      if (!admin) return route.fulfill({ status: 403, json: { error: 'admin required' } })
      const plugin = data.plugins.find(p => p.id === install[1])!
      plugin.installation = { ...plugin.installation, ...(body as object), updated_at: '2026-09-24T08:00:00Z' }
      return route.fulfill({ json: plugin.installation })
    }
    if (path === '/api/kinds') {
      if (method === 'POST') { const kind = { id: `k-${body.slug}`, ...body }; data.kinds.push(kind as never); return route.fulfill({ status: 201, json: kind }) }
      return route.fulfill({ json: { items: data.kinds } })
    }
    if (path === '/api/business/principals') return route.fulfill({ json: [{ id: me.id, kind: 'person', name: me.name, roles: [options.role ?? 'admin'] }] })
    if (contactNodes) {
      const items = data.contacts.filter(c => !c.deleted).map(c => ({ id: c.id, key: c.key, kind_id: 'k-contact', kind_slug: 'contact', kind_label: 'Contact', title: c.name, body: '', state: 'new', fields: { email: c.email, role: c.role, phone: c.phone }, parent_id: null, position: '0', created_at: '2026-01-01T00:00:00Z', updated_at: '2026-01-01T00:00:00Z', deleted_at: null, priority: null, assignee: null, parent: null, children_count: 0, project: null }))
      return route.fulfill({ json: { items, next_cursor: null } })
    }
    if (path === '/api/quotes') return route.fulfill({ json: options.quotes ?? [] })
    if (path === '/api/quotes/settings') {
      if (options.noQuotes) return route.fulfill({ status: 409, json: { error: 'business_quotes is not enabled' } })
      if (method === 'PATCH') {
        if (!admin) return route.fulfill({ status: 403, json: { error: 'admin required' } })
        if (body.expected_revision !== data.quoteSettings.revision) return route.fulfill({ status: 409, json: { error: 'settings revision is stale' } })
        if (!/^[A-Z]{3}$/.test(String(body.default_currency))) return route.fulfill({ status: 400, json: { error: 'invalid default currency' } })
        Object.assign(data.quoteSettings, { numbering_time_zone: body.numbering_time_zone, default_currency: body.default_currency, sender: body.sender, defaults: body.defaults, layout: body.layout, revision: data.quoteSettings.revision + 1 })
      }
      return route.fulfill({ json: data.quoteSettings })
    }
    if (path === '/api/events') {
      const node = q.get('node_id'), after = Number(q.get('after') ?? 0)
      return route.fulfill({ json: { items: data.events.filter(e => e.id > after && (!node || e.node_id === node)).map(e => ({ ...e, actor_principal_id: me.id, at: '2026-09-24T08:00:00Z' })), next_after: null } })
    }
    const undo = /^\/api\/events\/(\d+)\/undo$/.exec(path)
    if (undo) {
      const original = data.events.find(e => e.id === Number(undo[1]))
      if (!original || original.undo_of !== null || data.events.some(e => e.undo_of === original.id)) return route.fulfill({ status: 409, json: { error: 'conflict' } })
      if (!admin) return route.fulfill({ status: 403, json: { error: 'forbidden' } })
      const org = data.customers.find(c => c.id === original.node_id)
      const person = data.contacts.find(c => c.id === original.node_id)
      switch (original.type) {
        case 'crm.customer_created':
          if (!org || data.contacts.some(c => c.organisation_node_id === org.id && !c.deleted)) return route.fulfill({ status: 409, json: { error: 'conflict' } })
          org.deleted = true; break
        case 'crm.customer_updated': Object.assign(org!, pickFields(original.before as MockCustomer), { name: (original.before as MockCustomer).name, revision: org!.revision + 1 }); break
        case 'crm.note_rewrite_applied': Object.assign(org!, { customer_notes: (original.before as MockCustomer).customer_notes, revision: org!.revision + 1 }); break
        case 'crm.customer_deleted': org!.deleted = false; break
        case 'crm.customer_visibility_changed':
          if (!!org!.archived !== (original.after as { archived: boolean }).archived) return route.fulfill({ status: 409, json: { error: 'conflict' } })
          Object.assign(org!, { archived: (original.before as { archived: boolean }).archived, revision: org!.revision + 1 }); break
        case 'crm.primary_contact_changed': Object.assign(org!, { primary_contact_node_id: (original.before as MockCustomer).primary_contact_node_id, revision: org!.revision + 1 }); break
        case 'crm.contact_created':
          if (data.customers.find(c => c.id === person!.organisation_node_id)?.primary_contact_node_id === person!.id) return route.fulfill({ status: 409, json: { error: 'conflict' } })
          person!.deleted = true; break
        case 'crm.contact_updated': Object.assign(person!, pickContact(original.before as MockContact), { name: (original.before as MockContact).name, revision: person!.revision + 1 }); break
        case 'crm.contact_deleted': person!.deleted = false; break
        default: return route.fulfill({ status: 409, json: { error: 'conflict' } })
      }
      const undone = { id: ++data.counter.event, node_id: original.node_id, type: original.type, before: original.after, after: original.before, undo_of: original.id }
      data.events.push(undone)
      return route.fulfill({ status: 201, json: undone })
    }

    // ---------- /api/crm ----------
    if (!crmOpen()) return bad(route, 409, 'conflict', 'business_crm is not enabled for this operation')
    const write = method !== 'GET'
    if (write && !admin) return bad(route, 403, 'forbidden', 'admin session required')
    if (path === '/api/crm/providers') return admin ? route.fulfill({ json: options.providers ?? [] }) : bad(route, 403, 'forbidden', 'admin session required')
    if (path === '/api/crm/organisations') {
      if (method === 'POST') {
        if (!String(body.name ?? '').trim()) return bad(route, 400, 'invalid_request', 'invalid customer fields')
        const id = `org-new-${data.counter.next++}`
        const created = customer(id, `ORG-${20 + data.counter.next}`, String(body.name), { ...pickFields(body as unknown as MockCustomer), revision: 1 })
        data.customers.push(created)
        event(id, 'crm.customer_created', null, view(created))
        return route.fulfill({ status: 201, headers: receipt(), json: view(created) })
      }
      const limit = Number(q.get('limit') ?? 50), offset = Number(q.get('offset') ?? 0)
      const archived = q.get('archived') ?? 'false'
      const list = data.customers.filter(c => !c.deleted && (archived === 'all' || !!c.archived === (archived === 'true'))).sort((a, b) => a.name.localeCompare(b.name))
      const items = list.slice(offset, offset + limit).map(view)
      return route.fulfill({ json: { items, next_offset: offset + limit < list.length ? offset + limit : null } })
    }
    const orgMatch = /^\/api\/crm\/organisations\/([^/]+)(?:\/(.+))?$/.exec(path)
    if (orgMatch) {
      const [, id, rest] = orgMatch
      const org = live(id)
      if (!org) return bad(route, 404, 'not_found', 'contact or principal not found')
      if (meanwhile === id && write) { org.revision += 1; org.phone = '+43 316 999 000'; meanwhile = undefined }
      if (!rest) {
        if (method === 'GET') return route.fulfill({ json: view(org) })
        if (method === 'PATCH') {
          if (body.expected_revision !== org.revision) return bad(route, 409, 'conflict', 'contact binding requires a live contact and a person principal')
          if (body.currency && !/^[A-Z]{3}$/.test(String(body.currency))) return bad(route, 400, 'invalid_request', 'invalid currency')
          const before = view(org)
          Object.assign(org, pickFields(body as unknown as MockCustomer), { name: String(body.name), revision: org.revision + 1 })
          event(id, 'crm.customer_updated', before, view(org))
          return route.fulfill({ headers: receipt(), json: view(org) })
        }
        if (method === 'DELETE') {
          const rel = data.related[id]
          if (rel && (rel.projects.length || rel.quotes.length)) return bad(route, 409, 'conflict', 'contact binding requires a live contact and a person principal')
          if (org.primary_contact_node_id) { const before = view(org); org.primary_contact_node_id = null; org.revision += 1; event(id, 'crm.primary_contact_changed', before, view(org)) }
          for (const c of data.contacts.filter(x => x.organisation_node_id === id && !x.deleted)) { c.deleted = true; event(c.id, 'crm.contact_deleted', contactView(data, c), null) }
          org.deleted = true
          event(id, 'crm.customer_deleted', view(org), null)
          return route.fulfill({ status: 204, headers: receipt(), body: '' })
        }
      }
      if (rest === 'contacts') {
        if (method === 'POST') {
          if (!String(body.name ?? '').trim()) return bad(route, 400, 'invalid_request', 'invalid contact fields')
          if (body.email && !/^[^\s@<>]+@[^\s@<>]+\.[^\s@<>]+$/.test(String(body.email))) return bad(route, 400, 'invalid_request', 'invalid email')
          const created = contact(`con-new-${data.counter.next++}`, id, String(body.name), { ...pickContact(body as unknown as MockContact), revision: 1 })
          data.contacts.push(created)
          event(created.id, 'crm.contact_created', null, contactView(data, created))
          if (!org.primary_contact_node_id) { const before = view(org); org.primary_contact_node_id = created.id; org.revision += 1; event(id, 'crm.primary_contact_changed', before, view(org)) }
          return route.fulfill({ status: 201, headers: receipt(), json: contactView(data, created) })
        }
        return route.fulfill({ json: data.contacts.filter(c => c.organisation_node_id === id && !c.deleted).sort((a, b) => a.name.localeCompare(b.name)).map(c => contactView(data, c)) })
      }
      if (rest === 'primary-contact') {
        if (body.expected_revision !== org.revision) return bad(route, 409, 'conflict', 'stale')
        const before = view(org)
        org.primary_contact_node_id = String(body.contact_node_id); org.revision += 1
        event(id, 'crm.primary_contact_changed', before, view(org))
        return route.fulfill({ headers: receipt(), json: view(org) })
      }
      if (rest === 'visibility' && method === 'PATCH') {
        if (body.expected_revision !== org.revision) return bad(route, 409, 'conflict', 'customer revision is stale')
        const before = view(org)
        org.archived = body.archived === true; org.revision += 1
        event(id, 'crm.customer_visibility_changed', before, view(org))
        return route.fulfill({ headers: receipt(), json: view(org) })
      }
      if (rest === 'related') return route.fulfill({ json: data.related[id] ?? { projects: [], quotes: [], hours: [], documents: [] } })
      if (rest === 'note-ai') {
        if (!admin) return bad(route, 403, 'forbidden', 'admin session required')
        return route.fulfill({ json: options.aiEnabled && data.plugins.find(p => p.id === 'business_crm')?.installation.permissions.includes('tools.invoke')
          ? { enabled: true }
          : { enabled: false, reason: 'No model is configured for AI note rewriting.' } })
      }
      if (rest === 'note-ai/generate') {
        if (!options.aiEnabled || !data.plugins.find(p => p.id === 'business_crm')?.installation.permissions.includes('tools.invoke')) return bad(route, 409, 'ai_unavailable', 'No model is configured for AI note rewriting.')
        if (body.expected_revision !== org.revision) return bad(route, 409, 'conflict', 'stale')
        const proposed = org.customer_notes.replace('Invoices go to accounting, not to Jana.', 'Invoices go to Max Brandl in accounting.')
        const draft = { id: `draft-${data.counter.next++}`, org: id, base: org.revision, text: proposed, applied: false }
        data.drafts.push(draft)
        event(id, 'crm.note_rewrite_drafted', null, { draft_id: draft.id, base_revision: org.revision, source: 'ai', model_profile_id: 'profile-fake' })
        return route.fulfill({ status: 201, json: { id: draft.id, organisation_node_id: id, draft_text: proposed, base_revision: org.revision, applied: false } })
      }
      if (rest === 'note-rewrite') {
        if (!String(body.draft_text ?? '').trim()) return bad(route, 400, 'invalid_request', 'invalid note draft')
        if (body.expected_revision !== org.revision) return bad(route, 409, 'conflict', 'stale')
        const draft = { id: `draft-${data.counter.next++}`, org: id, base: org.revision, text: String(body.draft_text), applied: false }
        data.drafts.push(draft)
        event(id, 'crm.note_rewrite_drafted', null, { draft_id: draft.id, base_revision: org.revision })
        return route.fulfill({ status: 201, json: { id: draft.id, organisation_node_id: id, draft_text: draft.text, applied: false } })
      }
      const apply = /^note-rewrite\/([^/]+)\/apply$/.exec(rest ?? '')
      if (apply) {
        const draft = data.drafts.find(d => d.id === apply[1] && d.org === id)
        if (!draft) return bad(route, 404, 'not_found', 'not found')
        if (draft.applied || draft.base !== org.revision) return bad(route, 409, 'conflict', 'stale')
        const before = view(org)
        org.customer_notes = draft.text; org.revision += 1; draft.applied = true
        event(id, 'crm.note_rewrite_applied', before, { customer: view(org), draft_id: draft.id })
        return route.fulfill({ headers: receipt(), json: view(org) })
      }
    }
    const contactMatch = /^\/api\/crm\/contacts\/([^/]+)$/.exec(path)
    if (contactMatch) {
      const c = liveContact(contactMatch[1])
      if (!c) return bad(route, 404, 'not_found', 'contact or principal not found')
      if (method === 'PATCH') {
        if (body.expected_revision !== c.revision) return bad(route, 409, 'conflict', 'stale')
        if (body.email && !/^[^\s@<>]+@[^\s@<>]+\.[^\s@<>]+$/.test(String(body.email))) return bad(route, 400, 'invalid_request', 'invalid email')
        const before = contactView(data, c)
        Object.assign(c, pickContact(body as unknown as MockContact), { name: String(body.name), revision: c.revision + 1 })
        event(c.id, 'crm.contact_updated', before, contactView(data, c))
        return route.fulfill({ headers: receipt(), json: contactView(data, c) })
      }
      if (method === 'DELETE') {
        const org = live(c.organisation_node_id)!
        if (org.primary_contact_node_id === c.id) {
          const before = view(org)
          const successor = data.contacts.filter(x => x.organisation_node_id === org.id && !x.deleted && x.id !== c.id).sort((a, b) => a.name.localeCompare(b.name))[0]
          org.primary_contact_node_id = successor?.id ?? null; org.revision += 1
          event(org.id, 'crm.primary_contact_changed', before, view(org))
        }
        const before = contactView(data, c)
        c.deleted = true
        event(c.id, 'crm.contact_deleted', before, null)
        return route.fulfill({ status: 204, headers: receipt(), body: '' })
      }
      return route.fulfill({ json: contactView(data, c) })
    }
    return bad(route, 404, 'not_found', 'not found')
  }
  await page.route('**/api/**', handler)
  return calls
}
function pickFields(source: Partial<MockCustomer>) {
  const out: Partial<MockCustomer> = {}
  for (const key of FIELDS) if (key in source) (out as Record<string, unknown>)[key] = structuredClone(source[key])
  return out
}
function pickContact(source: Partial<MockContact>) {
  const out: Partial<MockContact> = {}
  for (const key of CONTACT_FIELDS) if (key in source) (out as Record<string, unknown>)[key] = source[key]
  return out
}
