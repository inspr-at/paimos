// SPDX-License-Identifier: AGPL-3.0-only
// A synthetic quote for the editor route: the draft (GET and CAS PATCH), the quote
// record, presence and its stream, the plugin catalog and the signed-in admin,
// layered over mockWork. Document content is German like real quotes; every name
// and number is invented.
import type { QuoteProfileSnapshot } from '../src/lib/quotes/types'
import type { Page, Route } from '@playwright/test'
import { me } from './work-fixtures'
import { mockEffectivePermissions } from './authz-fixtures'

export const QUOTE_ID = 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa'
export const SECTIONS = ['11111111-1111-4111-8111-000000000001', '11111111-1111-4111-8111-000000000002', '11111111-1111-4111-8111-000000000003', '11111111-1111-4111-8111-000000000004']
const n = (i: number) => `22222222-2222-4222-8222-${String(i).padStart(12, '0')}`
export const NODES = Array.from({ length: 12 }, (_, i) => n(i + 1))
export function quoteDocument() {
  return {
    schema_version: 1, minimum_writer_version: 1, title: 'Relaunch des Kundenportals', subtitle: 'Konzeption, Umsetzung und Betrieb',
    project_ref: 'PRJ-17', offer_date: '2026-09-24', valid_until: '2026-10-24', currency: 'EUR',
    sender: { company: 'Beispiel Studio GmbH', street: 'Musterweg 1', postal_code: '8010', city: 'Graz', country: 'Österreich', email: 'hallo@beispiel.invalid', contact_person: 'Alex Beispiel' },
    recipient: { name: 'Muster Handel GmbH', address: 'Hauptstraße 12, 1010 Wien', contact: 'Jana Muster', country: 'Österreich', customer_no: 'K26091' },
    legal: { intro: 'Vielen Dank für Ihre Anfrage. Gerne bieten wir Ihnen die folgenden Leistungen an.', accept_text: 'Mit Ihrer Unterschrift nehmen Sie dieses Angebot an.', vat_note: 'Alle Beträge verstehen sich zuzüglich 20 % USt.' },
    layout: {},
    sections: [
      { id: SECTIONS[0], heading: 'Ausgangslage', body: '', nodes: [
        { id: NODES[0], kind: 'paragraph', text: 'Das bestehende Portal ist in die Jahre gekommen und soll neu aufgesetzt werden.' },
        { id: NODES[1], kind: 'paragraph', text: 'Ziel ist ein schneller, barrierefreier Auftritt mit klarer Navigation.' },
      ] },
      { id: SECTIONS[1], heading: 'Leistungsgegenstand und Vorgehen', body: '', nodes: [
        { id: NODES[2], kind: 'paragraph', text: 'Wir gehen in drei Schritten vor:' },
        { id: NODES[3], kind: 'item', text: 'Analyse der bestehenden Inhalte', marker: 'decimal', numbering: 'outline', depth: 0, section_bound: true },
        { id: NODES[4], kind: 'item', text: 'Interviews mit dem Vertrieb', marker: 'decimal', numbering: 'outline', depth: 1, section_bound: true },
        { id: NODES[5], kind: 'item', text: 'Umsetzung in zwei Iterationen', marker: 'decimal', numbering: 'outline', depth: 0, section_bound: true },
        { id: NODES[6], kind: 'paragraph', text: 'Jede Iteration endet mit einer gemeinsamen Abnahme.' },
      ] },
      { id: SECTIONS[2], heading: 'Termine', body: '', nodes: [
        { id: NODES[7], kind: 'item', text: 'Start im Oktober', marker: 'disc', depth: 0 },
        { id: NODES[8], kind: 'item', text: 'Livegang vor Jahresende', marker: 'disc', depth: 0 },
      ] },
      { id: SECTIONS[3], heading: 'Mitwirkung des Auftraggebers', body: '', nodes: [
        { id: NODES[9], kind: 'paragraph', text: 'Der Auftraggeber stellt Inhalte und Zugänge rechtzeitig bereit.' },
      ] },
    ],
    positions: [
      { id: '33333333-3333-4333-8333-000000000001', pricing_source: 'manual', short_text: 'Konzeption', long_text: 'Workshops, Informationsarchitektur, Prototyp', quantity: '16', unit_label: 'Stunde', unit_price_cents: 11000, total_cents: 176000, currency: 'EUR' },
      { id: '33333333-3333-4333-8333-000000000002', pricing_source: 'manual', short_text: 'Umsetzung', long_text: 'Frontend, Anbindung, Tests', quantity: '40', unit_label: 'Stunde', unit_price_cents: 9500, total_cents: 380000, currency: 'EUR' },
    ],
    net_total_cents: 556000,
  }
}
export type QuoteDoc = ReturnType<typeof quoteDocument> & { profile?: QuoteProfileSnapshot }
export interface QuoteMockOptions { role?: 'admin' | 'member'; state?: 'draft' | 'issued' }
export interface QuoteCall { path: string; method: string; body: unknown }

export async function mockQuoteEditor(page: Page, doc: QuoteDoc = quoteDocument(), options: QuoteMockOptions = {}) {
  const calls: QuoteCall[] = []
  const draft = { document: doc, revision: 1 }
  // Printing opens the browser dialog; the spec only needs to know it was asked for.
  await page.addInitScript(() => { (window as unknown as { printed: number }).printed = 0; window.print = () => { (window as unknown as { printed: number }).printed++ } })
  const handler = async (route: Route) => {
    const request = route.request(), url = new URL(request.url()), path = url.pathname, method = request.method()
    let body: unknown = null
    try { body = request.postDataJSON() } catch { body = null }
    const known = path === '/api/me' || path === '/api/me/permissions' || path === '/api/plugins' || path.startsWith('/api/quotes')
    if (!known) return route.fallback()
    calls.push({ path, method, body })
    if (path === '/api/me') return route.fulfill({ json: { principal: { id: me.id, name: me.name, kind: 'person', roles: [options.role ?? 'admin'] }, tenant: { id: 't1', name: 'INSPR Studio' } } })
    if (path === '/api/me/permissions') return route.fulfill({ json: mockEffectivePermissions(options.role ?? 'admin', url.searchParams.get('project_id') ?? undefined) })
    if (path === '/api/plugins') return route.fulfill({ json: ['business_costs', 'business_crm', 'business_quotes'].map(id => ({ id, version: '1', digest_sha256: 'ab'.repeat(32), owner: 'aeon', permissions: ['views.provide'], node_kinds: [], views: [], workflow_steps: [], agent_tools: [], integrations: [], background_jobs: [], installation: { manifest_digest_sha256: 'ab'.repeat(32), enabled: true, permissions: ['views.provide'], plugin_id: id, version: '1', updated_at: '2026-09-20T09:00:00Z' } })) })
    if (path === '/api/quotes') return route.fulfill({ json: [{ quote_node_id: QUOTE_ID, offer_no: 'A260924-1', state: options.state ?? 'draft' }] })
    if (path === `/api/quotes/${QUOTE_ID}`) return route.fulfill({ json: { quote_node_id: QUOTE_ID, offer_no: 'A260924-1', state: options.state ?? 'draft', revision: 1 } })
    if (path === `/api/quotes/${QUOTE_ID}/draft`) {
      if (method === 'PATCH') {
        const write = body as { document: QuoteDoc; mutation_id: string }
        const expected = request.headers()['if-match']
        if (expected !== `"qd-${draft.revision}"`) return route.fulfill({ status: 412, json: { error: 'stale draft' } })
        draft.document = write.document; draft.revision++
        return route.fulfill({ json: { mutation_id: write.mutation_id, acknowledged_revision: draft.revision, acknowledged_quote_revision: 1, current_revision: draft.revision, current_quote_revision: 1, replayed: false, document: draft.document, document_sha256: 'b'.repeat(64), updated_at: '2026-09-24T09:05:00Z', updated_by_principal_id: me.id } })
      }
      return route.fulfill({ json: { document: draft.document, document_sha256: 'a'.repeat(64), draft_revision: draft.revision, quote_revision: 1, schema_version: 1, minimum_writer_version: 1, base_version: 0, updated_at: '2026-09-24T09:00:00Z', updated_by_principal_id: me.id } })
    }
    if (path === `/api/quotes/${QUOTE_ID}/presence` && method === 'POST') return route.fulfill({ json: { session_id: '44444444-4444-4444-8444-444444444444', snapshot: { sessions: [], draft_revision: draft.revision, quote_revision: 1, state: options.state ?? 'draft' } } })
    if (path.startsWith(`/api/quotes/${QUOTE_ID}/presence`)) return route.fulfill({ json: { sessions: [], draft_revision: draft.revision, quote_revision: 1, state: options.state ?? 'draft' } })
    if (path.endsWith('/collaboration/stream')) return route.fulfill({ contentType: 'text/event-stream', body: '' })
    return route.fulfill({ status: 404, json: { error: 'not found' } })
  }
  await page.route('**/api/**', handler)
  return { calls, draft }
}
