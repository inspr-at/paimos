// SPDX-License-Identifier: AGPL-3.0-only
// An in-memory quote API for the Quotes tab, the docked and full workspace and
// the customer link (U18): the list with its summary fields, drafts with CAS
// saves, issue (finalize), revise (branch), duplicate, archive, frozen versions,
// public links (token shown once), acceptance notices and receipts, presence
// and its stream; deleting a never-issued draft, and an event log whose undo
// reverses a delete, an archive, a duplicate or a new link (QL1). Layered over
// mockWork and mockCRM; every name, number and amount is invented. No fixed
// clock: Vue's event timing needs real time.
import type { Page, Route } from '@playwright/test'
import { quoteDocument, type QuoteDoc } from './quote-inspector-fixtures'
import { HOFER, LUMEN, NORDSTERN } from './crm-fixtures'
import { me } from './work-fixtures'

const id = (n: number) => `c0ffee00-0000-4000-8000-${String(n).padStart(12, '0')}`
export const Q = { draft: id(1), issued: id(2), accepted: id(3), expired: id(4), archived: id(5), second: id(6), big: id(7), lumen: id(8) }
export const MIRA = { id: '22222222-2222-4222-8222-222222222222', name: 'Mira Kovač' }
const DIGEST = (n: number) => `${String(n).repeat(8)}${'ab'.repeat(24)}`.slice(0, 64)
const today = new Date()
const iso = (days: number) => { const d = new Date(today); d.setDate(d.getDate() + days); return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}` }
const stamp = (days: number, hour = 9) => { const d = new Date(today); d.setDate(d.getDate() + days); d.setHours(hour, 12, 0, 0); return d.toISOString() }

export interface Row {
  quote_node_id: string; project_node_id: string; customer_org_node_id: string; current_version: number; state: 'draft' | 'issued' | 'accepted' | 'void'
  revision: number; offer_no?: string; archived: boolean; project_ref: string; classic_status: 'draft' | 'sent' | 'expired' | 'accepted' | 'void'
  key: string; title: string; customer_name: string; currency?: string; net_total_cents?: number; offer_date?: string; valid_until?: string
  created_at: string; updated_at: string; issued_at?: string; accepted_at?: string
}
export interface Version { quote_node_id: string; version: number; currency: string; title: string; content_sha256: string; created_by_principal_id: string; created_at: string; digest_mode: 'document-v1'; pricing_mode: 'cent-half-up-v1'; total: string; document: QuoteDoc; offer_no: string; validity_time_zone: string }
export interface Link { id: string; public_tenant: string; quote_node_id: string; version: number; target_content_sha256: string; expires_at: string; revoked_at?: string; path?: string; copy_unavailable_reason?: 'key_not_configured' }
export interface Job { quote_node_id: string; version: number; state: string; attempts: number; next_attempt_at: string; receipt_sha256?: string; renderer_version?: string; updated_at: string }

function doc(title: string, customer: string, positions: [string, string, number][], offer: string, valid: string): QuoteDoc {
  const d = quoteDocument()
  d.title = title
  d.recipient = { ...d.recipient, name: customer }
  d.offer_date = offer; d.valid_until = valid
  d.positions = positions.map(([text, qty, cents], i) => ({ id: `33333333-3333-4333-8333-${String(i + 1).padStart(12, '0')}`, pricing_source: 'manual', short_text: text, long_text: '', quantity: qty, unit_label: 'Stunde', unit_price_cents: cents, total_cents: Math.round(Number(qty) * cents), currency: 'EUR' }))
  d.net_total_cents = d.positions.reduce((s, p) => s + p.total_cents, 0)
  return d
}
function row(q: string, n: number, title: string, org: string, name: string, state: Row['state'], cents: number | undefined, offer: string, valid: string, extra: Partial<Row> = {}): Row {
  return {
    quote_node_id: q, project_node_id: '', customer_org_node_id: org, current_version: state === 'draft' ? 0 : 1, state, revision: 2, offer_no: `A${offer.replace(/-/g, '').slice(2)}-${String(n).padStart(2, '0')}`, archived: false,
    project_ref: '', classic_status: state === 'issued' ? 'sent' : state, key: `QUO-${n}`, title, customer_name: name, currency: 'EUR', net_total_cents: cents, offer_date: offer, valid_until: valid,
    created_at: stamp(-40 + n), updated_at: stamp(-10 + n), ...extra,
  }
}

export function quoteWorld(options: { empty?: boolean } = {}) {
  const rows: Row[] = options.empty ? [] : [
    row(Q.draft, 12, 'Relaunch des Kundenportals', HOFER, 'Bäckerei Hofer', 'draft', 556000, iso(-2), iso(28), { project_ref: 'PRJ-17' }),
    row(Q.issued, 11, 'Filial-Tablets und Kassenanbindung', HOFER, 'Bäckerei Hofer', 'issued', 1284000, iso(-12), iso(5), { issued_at: stamp(-12) }),
    row(Q.accepted, 9, 'Wartungsvertrag 2027', NORDSTERN, 'Klinik Nordstern', 'accepted', 960000, iso(-30), iso(0), { issued_at: stamp(-30), accepted_at: stamp(-26) }),
    row(Q.expired, 7, 'Barrierefreiheits-Audit', NORDSTERN, 'Klinik Nordstern', 'issued', 348000, iso(-70), iso(-40), { classic_status: 'expired', issued_at: stamp(-70) }),
    row(Q.archived, 5, 'Messestand-Konfigurator', LUMEN, 'Atelier Lumen', 'draft', 212500, iso(-120), iso(-90), { archived: true }),
    row(Q.second, 13, 'Onlineshop Erweiterung Weihnachten', HOFER, 'Bäckerei Hofer', 'draft', 88000, iso(-1), iso(29)),
    row(Q.big, 10, 'Plattform für Patient:innen-Termine mit Anbindung an das Krankenhausinformationssystem', NORDSTERN, 'Klinik Nordstern', 'issued', 7430000, iso(-20), iso(10), { issued_at: stamp(-20) }),
    row(Q.lumen, 8, 'Website Refresh', LUMEN, 'Atelier Lumen', 'draft', undefined, iso(-45), iso(-15)),
  ]
  const drafts = new Map<string, { document: QuoteDoc; revision: number; base?: number }>()
  const versions = new Map<string, Version[]>()
  for (const r of rows) {
    const cents = r.net_total_cents
    const positions: [string, string, number][] = cents === undefined ? [] : cents > 176000 ? [['Konzeption', '16', 11000], ['Umsetzung', '1', cents - 176000]] : [['Leistung', '1', cents]]
    const d = doc(r.title, r.customer_name, positions, r.offer_date!, r.valid_until!)
    drafts.set(r.quote_node_id, { document: d, revision: 3 })
    if (r.current_version > 0) versions.set(r.quote_node_id, [{ quote_node_id: r.quote_node_id, version: 1, currency: 'EUR', title: r.title, content_sha256: DIGEST(rows.indexOf(r) + 1), created_by_principal_id: me.id, created_at: r.issued_at ?? stamp(-5), digest_mode: 'document-v1', pricing_mode: 'cent-half-up-v1', total: `${((r.net_total_cents ?? 0) / 100).toFixed(4)}`, document: structuredClone(d), offer_no: r.offer_no!, validity_time_zone: 'Europe/Vienna' }])
  }
  const links = new Map<string, Link>()
  links.set(`${Q.accepted}:1`, { id: 'link-acc', public_tenant: 'sel-demo', quote_node_id: Q.accepted, version: 1, target_content_sha256: DIGEST(3), expires_at: stamp(4), revoked_at: undefined })
  const jobs = new Map<string, Job>([[`${Q.accepted}:1`, { quote_node_id: Q.accepted, version: 1, state: 'ready', attempts: 1, next_attempt_at: stamp(-26), receipt_sha256: 'fe'.repeat(32), renderer_version: 'quote-print chromium-128', updated_at: stamp(-26, 10) }]])
  const notices = [{ quote_node_id: Q.accepted, version: 1, channel: 'public', accepted_at: stamp(-26), confirmation_state: 'ready' }]
  const events: QuoteEvent[] = []
  return { rows, drafts, versions, links, jobs, notices, events, deleted: new Map<string, Row>(), presence: [] as { session_id: string; principal_id: string; name: string; mode: string; observed_revision: number; expires_at: string; anchor?: unknown }[], counter: { next: 20 } }
}
export type QuoteWorld = ReturnType<typeof quoteWorld>
export interface QuoteEvent { id: number; node_id: string; type: string; before: unknown; after: unknown; undo_of: number | null }
export interface QuoteCall { path: string; method: string; body: unknown; query: URLSearchParams; headers: Record<string, string> }
export interface QuoteMockOptions {
  listStatus?: number
  // A host key permits re-copy on GET; without it only the POST shows the path.
  linkKeyConfigured?: boolean
  // The next draft save meets a newer one from Mira (412), with her change applied first.
  conflictNext?: { quote: string; apply: (doc: QuoteDoc) => void }
  role?: 'admin' | 'member'
  // U19: the profile snapshot a draft takes when its title bar picks one (null: archived or unknown).
  profile?: (id: string) => unknown | null
}

export async function mockQuotes(page: Page, world: QuoteWorld, options: QuoteMockOptions = {}) {
  const calls: QuoteCall[] = []
  let conflict = options.conflictNext
  await page.addInitScript(() => { (window as unknown as { printed: number }).printed = 0; window.print = () => { (window as unknown as { printed: number }).printed++ } })
  const handler = async (route: Route) => {
    const firstEvent = world.events.length
    const receipt = () => ({ 'X-Aeon-Event-Ids': world.events.slice(firstEvent).reverse().map(event => event.id).join(',') })
    const request = route.request(), url = new URL(request.url()), path = url.pathname, method = request.method(), q = url.searchParams
    if (!path.startsWith('/api/quotes') || path === '/api/quotes/settings') return route.fallback()
    let body: Record<string, unknown> = {}
    try { body = request.postDataJSON() ?? {} } catch { body = {} }
    calls.push({ path, method, body, query: q, headers: request.headers() })
    const rowOf = (qid: string) => world.rows.find(r => r.quote_node_id === qid)
    const log = (node: string, type: string, before: unknown, after: unknown) => world.events.push({ id: 5000 + world.events.length + 1, node_id: node, type, before: structuredClone(before), after: structuredClone(after), undo_of: null })
    const admin = (options.role ?? 'admin') === 'admin'
    const projection = (r: Row) => ({ quote_node_id: r.quote_node_id, project_node_id: r.project_node_id, customer_org_node_id: r.customer_org_node_id, current_version: r.current_version, state: r.state, revision: r.revision, offer_no: r.offer_no, archived: r.archived, project_ref: r.project_ref, classic_status: r.classic_status })
    if (path === '/api/quotes' && method === 'GET') {
      if (options.listStatus) return route.fulfill({ status: options.listStatus, json: { error: 'quote operation is not available' } })
      return route.fulfill({ json: world.rows.map(r => ({ ...r })) })
    }
    if (path === '/api/quotes' && method === 'POST') {
      const n = ++world.counter.next
      const qid = id(100 + n)
      const r = row(qid, n, String(body.title), String(body.customer_org_node_id), body.customer_org_node_id === HOFER ? 'Bäckerei Hofer' : 'A customer', 'draft', 0, isoToday(), isoPlus(30), { revision: 1, created_at: new Date().toISOString(), updated_at: new Date().toISOString() })
      world.rows.push(r)
      const d = doc(r.title, r.customer_name, [], r.offer_date!, r.valid_until!)
      world.drafts.set(qid, { document: d, revision: 1 })
      return route.fulfill({ status: 201, json: projection(r) })
    }
    if (path === '/api/quotes/acceptances') return route.fulfill({ json: world.notices })
    if (path === '/api/quotes/readiness') return route.fulfill({ json: { renderer_available: true, smtp_enabled: false, smtp_configured: false, email_delivery: 'disabled' } })
    const m = /^\/api\/quotes\/([^/]+)(?:\/(.*))?$/.exec(path)
    if (!m) return route.fallback()
    const [, qid, rest = ''] = m
    const r = rowOf(qid)
    if (!r) return route.fulfill({ status: 404, json: { error: 'quote not found' } })
    const draft = world.drafts.get(qid)!
    const list = world.versions.get(qid) ?? []
    if (!rest && method === 'GET') return route.fulfill({ json: projection(r) })
    if (!rest && method === 'DELETE') {
      if (!admin) return route.fulfill({ status: 403, json: { error: 'quote operation is not available' } })
      if (Number(q.get('expected_revision')) !== r.revision) return route.fulfill({ status: 409, json: { error: 'quote revision is stale' } })
      if (r.state !== 'draft' || r.current_version > 0) return route.fulfill({ status: 409, json: { error: 'only a draft that was never issued can be deleted; archive it instead' } })
      world.rows.splice(world.rows.indexOf(r), 1); r.revision++; world.deleted.set(qid, r)
      log(qid, 'quote.deleted', projection(r), { deleted: true, revision: r.revision })
      return route.fulfill({ status: 204, headers: receipt(), body: '' })
    }
    if (rest === 'draft') {
      if (method === 'PATCH') {
        const write = body as unknown as { document: QuoteDoc; mutation_id: string }
        if (conflict?.quote === qid) { conflict.apply(draft.document); draft.revision++; conflict = undefined }
        if (request.headers()['if-match'] !== `"qd-${draft.revision}"`) return route.fulfill({ status: 412, json: { error: 'stale draft' } })
        draft.document = write.document; draft.revision++
        r.title = write.document.title; r.updated_at = new Date().toISOString()
        return route.fulfill({ json: { mutation_id: write.mutation_id, acknowledged_revision: draft.revision, acknowledged_quote_revision: r.revision, current_revision: draft.revision, current_quote_revision: r.revision, replayed: false, document: draft.document, document_sha256: 'b'.repeat(64), updated_at: new Date().toISOString(), updated_by_principal_id: me.id } })
      }
      // As the server: the version this draft was branched from (0 for the first draft).
      return route.fulfill({ json: { document: draft.document, document_sha256: `${'d'.repeat(60)}${String(draft.revision).padStart(4, '0')}`, draft_revision: draft.revision, quote_revision: r.revision, schema_version: 1, minimum_writer_version: 1, base_version: draft.base ?? 0, updated_at: r.updated_at, updated_by_principal_id: me.id } })
    }
    if (rest === 'finalize' && method === 'POST') {
      if ((options.role ?? 'admin') !== 'admin') return route.fulfill({ status: 403, json: { error: 'quote operation is not available' } })
      if (body.expected_quote_revision !== r.revision || body.expected_draft_revision !== draft.revision || body.expected_document_sha256 !== `${'d'.repeat(60)}${String(draft.revision).padStart(4, '0')}`) return route.fulfill({ status: 409, json: { error: 'quote or draft revision is stale' } })
      if (!draft.document.positions.length) return route.fulfill({ status: 400, json: { error: 'document needs a title and position' } })
      const n = r.current_version + 1
      list.push({ quote_node_id: qid, version: n, currency: 'EUR', title: draft.document.title, content_sha256: DIGEST(n + 4), created_by_principal_id: me.id, created_at: new Date().toISOString(), digest_mode: 'document-v1', pricing_mode: 'cent-half-up-v1', total: '0.0000', document: structuredClone(draft.document), offer_no: r.offer_no!, validity_time_zone: 'Europe/Vienna' })
      world.versions.set(qid, list)
      Object.assign(r, { state: 'issued', classic_status: 'sent', current_version: n, revision: r.revision + 2, issued_at: new Date().toISOString() })
      const linkId = `link-auto-${world.counter.next++}`
      world.links.set(`${qid}:${n}`, { id: linkId, public_tenant: 'sel-demo', quote_node_id: qid, version: n, target_content_sha256: DIGEST(n + 4), expires_at: stamp(30), ...(options.linkKeyConfigured === false ? { copy_unavailable_reason: 'key_not_configured' as const } : { path: `/offers/sel-demo/tok-${linkId}` }) })
      return route.fulfill({ json: projection(r) })
    }
    if (rest === 'draft/branch' && method === 'POST') {
      const current = list.find(v => v.version === r.current_version)
      if (body.expected_quote_revision !== r.revision || body.expected_version !== r.current_version || body.expected_content_sha256 !== current?.content_sha256) return route.fulfill({ status: 409, json: { error: 'quote cannot branch from this version' } })
      Object.assign(r, { state: 'draft', classic_status: 'draft', revision: r.revision + 1 })
      draft.document = structuredClone(current!.document); draft.revision++; draft.base = r.current_version
      return route.fulfill({ json: { document: draft.document, draft_revision: draft.revision } })
    }
    if (rest === 'duplicate' && method === 'POST') {
      if (body.expected_revision !== r.revision) return route.fulfill({ status: 409, json: { error: 'quote revision is stale' } })
      const n = ++world.counter.next
      const copy = row(id(100 + n), n, r.title, r.customer_org_node_id, r.customer_name, 'draft', r.net_total_cents, isoToday(), isoPlus(30), { revision: 1 })
      world.rows.push(copy); world.drafts.set(copy.quote_node_id, { document: structuredClone(draft.document), revision: 1 })
      log(copy.quote_node_id, 'quote.duplicated', null, { source_quote_node_id: qid, quote: projection(copy) })
      return route.fulfill({ status: 201, headers: receipt(), json: projection(copy) })
    }
    if (rest === 'visibility' && method === 'PATCH') {
      if (body.expected_revision !== r.revision) return route.fulfill({ status: 409, json: { error: 'quote revision is stale' } })
      const was = r.archived
      r.archived = body.archived === true; r.revision++
      log(qid, 'quote.visibility_changed', { archived: was }, { archived: r.archived })
      return route.fulfill({ headers: receipt(), json: projection(r) })
    }
    if (rest === 'profile' && method === 'PUT') {
      if (r.state !== 'draft' || r.archived) return route.fulfill({ status: 409, json: { error: 'quote is not editable' } })
      if (body.expected_draft_revision !== draft.revision) return route.fulfill({ status: 409, json: { error: 'draft revision is stale' } })
      const id = String(body.profile_id ?? '')
      const snapshot = id ? options.profile?.(id) ?? null : null
      if (id && !snapshot) return route.fulfill({ status: 409, json: { error: 'profile is archived' } })
      const doc = draft.document as { profile?: unknown }
      if (snapshot) doc.profile = structuredClone(snapshot)
      else delete doc.profile
      draft.revision++; r.revision++
      return route.fulfill({ json: { document: draft.document, document_sha256: `${'d'.repeat(60)}${String(draft.revision).padStart(4, '0')}`, draft_revision: draft.revision, quote_revision: r.revision, schema_version: 1, minimum_writer_version: 1, base_version: draft.base ?? 0, updated_at: new Date().toISOString(), updated_by_principal_id: me.id } })
    }
    if (rest === 'versions') return route.fulfill({ json: list })
    const v = /^versions\/(\d+)(?:\/(.*))?$/.exec(rest)
    if (v) {
      const n = Number(v[1]), sub = v[2] ?? ''
      const version = list.find(x => x.version === n)
      if (!version) return route.fulfill({ status: 404, json: { error: 'version not found' } })
      if (!sub) return route.fulfill({ json: version })
      const key = `${qid}:${n}`
      if (sub === 'public-link') {
        if (method === 'POST') {
          if (world.links.get(key) && !world.links.get(key)!.revoked_at && Date.parse(world.links.get(key)!.expires_at) > Date.now()) return route.fulfill({ status: 409, json: { error: 'link cannot be created' } })
          const linkId = `link-${world.counter.next++}`
          const path = `/offers/sel-demo/tok-${linkId}`
          const link: Link = { id: linkId, public_tenant: 'sel-demo', quote_node_id: qid, version: n, target_content_sha256: version.content_sha256, expires_at: String(body.expires_at), ...(options.linkKeyConfigured === false ? { copy_unavailable_reason: 'key_not_configured' } : { path }) }
          world.links.set(key, link)
          log(qid, 'quote.public_link_created', null, { link_id: linkId, version: n })
          return route.fulfill({ status: 201, json: { ...link, path, token: `tok-${link.id}` } })
        }
        const link = world.links.get(key)
        return link ? route.fulfill({ json: link }) : route.fulfill({ status: 404, json: { error: 'link not found' } })
      }
      if (sub === 'public-link/revoke' && method === 'POST') {
        const link = world.links.get(key)
        if (!link || link.revoked_at) return route.fulfill({ status: 409, json: { error: 'active link not found' } })
        link.revoked_at = new Date().toISOString()
        link.path = undefined
        return route.fulfill({ json: link })
      }
      if (sub === 'confirmation') { const job = world.jobs.get(key); return job ? route.fulfill({ json: job }) : route.fulfill({ status: 404, json: { error: 'confirmation not found' } }) }
      if (sub === 'confirmation/retry' && method === 'POST') { const job = world.jobs.get(key)!; Object.assign(job, { state: 'pending', attempts: job.attempts }); return route.fulfill({ json: job }) }
      if (sub === 'confirmation/receipt') return route.fulfill({ contentType: 'application/pdf', body: '%PDF-1.4 receipt' })
    }
    if (rest === 'presence' && method === 'POST') return route.fulfill({ json: { session_id: '44444444-4444-4444-8444-444444444444', snapshot: { sessions: world.presence, draft_revision: draft.revision, quote_revision: r.revision, state: r.state } } })
    if (rest.startsWith('presence')) return route.fulfill({ json: { sessions: world.presence, draft_revision: draft.revision, quote_revision: r.revision, state: r.state } })
    if (rest === 'collaboration/stream') return route.fulfill({ contentType: 'text/event-stream', body: '' })
    return route.fulfill({ status: 404, json: { error: 'Unmocked quote route' } })
  }
  await page.route('**/api/quotes**', handler)
  // The quote events and their undo; every other event falls through to mockCRM.
  await page.route('**/api/events**', async route => {
    const url = new URL(route.request().url()), path = url.pathname
    const undo = /^\/api\/events\/(\d+)\/undo$/.exec(path)
    if (path === '/api/events') {
      const node = url.searchParams.get('node_id')
      const mine = world.events.filter(e => e.node_id === node)
      if (!mine.length) return route.fallback()
      return route.fulfill({ json: { items: mine.map(e => ({ ...e, actor_principal_id: me.id, at: new Date().toISOString() })), next_after: null } })
    }
    const original = undo && world.events.find(e => e.id === Number(undo[1]))
    if (!original) return route.fallback()
    calls.push({ path, method: 'POST', body: {}, query: url.searchParams, headers: route.request().headers() })
    const conflict = () => route.fulfill({ status: 409, json: { error: 'conflict' } })
    if (original.undo_of !== null || world.events.some(e => e.undo_of === original.id)) return conflict()
    if ((options.role ?? 'admin') !== 'admin') return route.fulfill({ status: 403, json: { error: 'forbidden' } })
    const qid = original.node_id
    const r = world.rows.find(x => x.quote_node_id === qid)
    switch (original.type) {
      case 'quote.deleted': {
        const gone = world.deleted.get(qid)
        if (!gone || gone.revision !== (original.after as { revision: number }).revision) return conflict()
        world.deleted.delete(qid); gone.revision++; world.rows.push(gone)
        break
      }
      case 'quote.visibility_changed':
        if (!r || r.archived !== (original.after as { archived: boolean }).archived) return conflict()
        r.archived = (original.before as { archived: boolean }).archived; r.revision++
        break
      case 'quote.duplicated':
        if (!r || r.revision !== 1) return conflict()
        world.rows.splice(world.rows.indexOf(r), 1); r.revision++; world.deleted.set(qid, r)
        break
      case 'quote.public_link_created': {
        const after = original.after as { version: number }
        const link = world.links.get(`${qid}:${after.version}`)
        if (!link || link.revoked_at) return conflict()
        link.revoked_at = new Date().toISOString(); link.path = undefined
        break
      }
      default: return conflict()
    }
    const undone: QuoteEvent = { id: 5000 + world.events.length + 1, node_id: qid, type: original.type, before: original.after, after: original.before, undo_of: original.id }
    world.events.push(undone)
    return route.fulfill({ status: 201, json: undone })
  })
  return calls
}
function isoToday() { return iso(0) }
function isoPlus(days: number) { return iso(days) }

// A public page for one link: the frozen document, its state and the acceptance.
export async function mockPublicQuote(page: Page, state: { acceptable: boolean; accepted?: boolean; receiptReady?: boolean; linkEnded?: boolean; missing?: boolean }) {
  const posted: unknown[] = []
  const d = doc('Relaunch des Kundenportals', 'Bäckerei Hofer', [['Konzeption', '16', 11000], ['Umsetzung', '40', 9500]], iso(-3), iso(27))
  let accepted = !!state.accepted
  await page.route('**/api/public/quotes/**', route => {
    const path = new URL(route.request().url()).pathname
    if (state.missing) return route.fulfill({ status: 404, json: { error: 'quote link not found' } })
    if (path.endsWith('/accept')) {
      posted.push(route.request().postDataJSON()); accepted = true
      return route.fulfill({ status: 201, json: { version: 1, content_sha256: DIGEST(1), accepted_at: new Date().toISOString(), confirmation_state: 'pending' } })
    }
    return route.fulfill({ headers: { 'Cache-Control': 'no-store' }, json: {
      document: d, offer_no: 'A260922-12', version: 1, content_sha256: DIGEST(1), state: accepted ? 'accepted' : 'issued',
      expires_at: state.linkEnded ? stamp(-1) : stamp(20), acceptable: state.acceptable && !accepted, receipt_ready: !!state.receiptReady, ...(accepted ? { accepted_at: stamp(-1) } : {}),
    } })
  })
  return posted
}
export { MIRA as mira, me }
export const presenceOf = (people: { id: string; name: string; mode: 'viewing' | 'editing' | 'idle'; section?: string }[]) => people.map((p, i) => ({
  session_id: `55555555-5555-4555-8555-${String(i).padStart(12, '0')}`, principal_id: p.id, name: p.name, mode: p.mode, observed_revision: 3, expires_at: stamp(1),
  ...(p.section ? { anchor: { section_id: p.section, observed_revision: 3, fidelity: 'section' } } : {}),
}))
