// SPDX-License-Identifier: AGPL-3.0-only
// A small in-memory business API (plugins, cost unit rates, hours, the principal
// directory) layered over mockWork, for the Business specs. Money goes over the
// wire as JSON number tokens, the way the server writes numeric(18,4), so the
// specs exercise exact parsing. Times are fixed to Thursday 24 Sep 2026 in Vienna.
// Entry corrections follow the server: an If-Unmodified-Since precondition (412
// with the current entry), 409 once the period is approved, and events that
// POST /events/{id}/undo reverses.
import type { Page, Route } from '@playwright/test'
import { mockEffectivePermissions } from './authz-fixtures'
import { me } from './work-fixtures'

export const NOW = new Date('2026-09-24T10:00:00+02:00')
export const mira = { id: '22222222-2222-4222-8222-222222222222', name: 'Mira Holm' }
export const nova = { id: '33333333-3333-4333-8333-333333333333', name: 'Nova' }
const DIGEST = 'ab'.repeat(32)
const PERMS: Record<string, string[]> = {
  business_costs: ['nodes.contribute', 'steps.apply', 'views.provide'],
  business_crm: ['nodes.contribute', 'steps.apply', 'views.provide'],
  business_hours: ['steps.apply', 'views.provide'],
  business_quotes: ['nodes.contribute', 'steps.apply', 'views.provide'],
}
// Local Vienna midnights of the weeks the specs use (CEST, UTC+2).
const local = (day: string, time = '00:00') => new Date(`${day}T${time}:00+02:00`).toISOString()
export const WEEK38 = { starts_at: local('2026-09-14'), ends_at: local('2026-09-21') }
export const WEEK39 = { starts_at: local('2026-09-21'), ends_at: local('2026-09-28') }

export interface BusinessMockOptions { role?: 'admin' | 'member'; enabled?: string[]; approveConflict?: boolean; noDirectory?: boolean }
type Rate = { id: string; cost_unit_node_id: string; unit: string; currency: string; internal_amount: string; bill_amount: string; effective_from: string; effective_until: string | null; created_by_principal_id: string; created_at: string }
type Period = { id: string; principal_id: string; starts_at: string; ends_at: string; state: 'open' | 'approved'; revision: number; approval: { approved_by_principal_id: string; entries_sha256: string; total_seconds: number; event_id: number } | null }
type Entry = { id: string; period_id: string; principal_id: string; node_id: string; cost_unit_node_id: string; source: string; agent_run_id: string | null; started_at: string; ended_at: string; duration_seconds: number; rate_amount: string; currency: string; amount: string; note: string; updated_at: string }
type EntryEvent = { id: number; actor_principal_id: string; node_id: string; type: string; before: Entry | null; after: Entry | null; at: string; undo_of: number | null }
// Microsecond revisions, so the specs check the precondition is echoed exactly.
export const REVISION = '2026-09-24T06:00:00.123456Z'
let revisions = 0
const nextRevision = () => `2026-09-24T08:00:${String(++revisions % 60).padStart(2, '0')}.654321Z`

function costUnit(id: string, key: string, title: string, state = 'new') {
  return {
    id, key, kind_id: 'k-cost_unit', title, body: '', fields: {}, state, parent_id: null, position: '0', created_at: '2026-01-01T09:00:00Z', updated_at: '2026-09-01T09:00:00Z', deleted_at: null,
    kind_slug: 'cost_unit', kind_label: 'Cost unit', priority: null, assignee: null, parent: null, children_count: 0, project: null,
  }
}
function rate(id: string, unit: string, cost: string, bill: string, internal: string, from: string, until: string | null = null): Rate {
  return { id, cost_unit_node_id: cost, unit, currency: 'EUR', internal_amount: internal, bill_amount: bill, effective_from: from, effective_until: until, created_by_principal_id: me.id, created_at: '2026-01-01T09:00:00Z' }
}
// Exact amount: bill × seconds / 3600, half up at four places (as the server's round()).
function amountFor(bill: string, seconds: number) {
  const [whole, frac = ''] = bill.split('.')
  const units = BigInt(whole + frac.padEnd(4, '0').slice(0, 4)) * BigInt(seconds)
  const text = ((units + 1800n) / 3600n).toString().padStart(5, '0')
  return `${text.slice(0, -4)}.${text.slice(-4)}`
}
function entry(id: string, period: string, principal: string, node: string, cost: string, bill: string, day: string, from: string, to: string, note: string): Entry {
  const started = local(day, from), ended = local(day, to)
  const seconds = (Date.parse(ended) - Date.parse(started)) / 1000
  return { id, period_id: period, principal_id: principal, node_id: node, cost_unit_node_id: cost, source: 'manual', agent_run_id: null, started_at: started, ended_at: ended, duration_seconds: seconds, rate_amount: bill, currency: 'EUR', amount: amountFor(bill, seconds), note, updated_at: REVISION }
}

export function businessData(options: BusinessMockOptions = {}) {
  const enabled = new Set(options.enabled ?? ['business_costs', 'business_hours'])
  const plugins = Object.keys(PERMS).map(id => ({
    id, version: '1', digest_sha256: DIGEST, owner: 'aeon', permissions: PERMS[id], node_kinds: id === 'business_costs' ? [{ slug: 'cost_unit', field_schema: {} }] : [],
    views: [{ id, panels: ['main'] }], workflow_steps: [], agent_tools: [], integrations: [], background_jobs: [],
    installation: enabled.has(id)
      ? { manifest_digest_sha256: DIGEST, enabled: true, permissions: PERMS[id], plugin_id: id, version: '1', updated_at: '2026-09-20T09:00:00Z' }
      : { manifest_digest_sha256: '0'.repeat(64), enabled: false, permissions: [], plugin_id: id, version: '1', updated_at: '0001-01-01T00:00:00Z' },
  }))
  const units = [costUnit('cu-dev', 'CU-1', 'Development'), costUnit('cu-design', 'CU-2', 'Design'), costUnit('cu-old', 'CU-3', 'Legacy support', 'cancelled')]
  const rates: Rate[] = [
    rate('r-1', 'hour', 'cu-dev', '95', '62.5', '2026-01-01'), rate('r-2', 'day', 'cu-dev', '720', '480', '2026-01-01'),
    rate('r-3', 'hour', 'cu-dev', '90', '60', '2025-01-01', '2026-01-01'), rate('r-4', 'hour', 'cu-design', '85', '55', '2026-01-01'),
  ]
  const periods: Period[] = [
    { id: 'p-me-38', principal_id: me.id, ...WEEK38, state: 'open', revision: 3, approval: null },
    { id: 'p-me-39', principal_id: me.id, ...WEEK39, state: 'open', revision: 5, approval: null },
    { id: 'p-mira-38', principal_id: mira.id, ...WEEK38, state: 'approved', revision: 2, approval: { approved_by_principal_id: me.id, entries_sha256: 'cd'.repeat(32), total_seconds: 14400, event_id: 7 } },
  ]
  const entries: Entry[] = [
    entry('e-1', 'p-me-39', me.id, 'n-1', 'cu-dev', '95', '2026-09-21', '09:00', '11:00', 'Token rotation'),
    entry('e-2', 'p-me-39', me.id, 'n-1', 'cu-dev', '95', '2026-09-22', '09:00', '12:30', 'Cleanup job'),
    entry('e-3', 'p-me-39', me.id, 'n-a1', 'cu-dev', '95', '2026-09-23', '13:00', '14:30', ''),
    entry('e-4', 'p-me-39', me.id, 'n-2', 'cu-design', '85', '2026-09-24', '08:00', '09:30', 'Connector sketch'),
    entry('e-5', 'p-me-38', me.id, 'n-1', 'cu-dev', '95', '2026-09-15', '09:00', '13:00', 'Provisioning spike'),
    entry('e-6', 'p-me-38', me.id, 'n-2', 'cu-dev', '95', '2026-09-17', '10:00', '12:15', ''),
    entry('e-7', 'p-mira-38', mira.id, 'n-3', 'cu-dev', '95', '2026-09-16', '09:00', '13:00', 'End-to-end check'),
  ]
  const principals = [
    { id: me.id, kind: 'person', name: me.name, roles: [options.role ?? 'admin'] },
    { id: mira.id, kind: 'person', name: mira.name, roles: ['member'] },
    { id: 'p-customer', kind: 'person', name: 'Cleo Customer', roles: ['customer'] },
    { id: nova.id, kind: 'agent', name: nova.name, roles: [] },
    { id: 'p-system', kind: 'agent', name: 'System', roles: ['system'] },
  ]
  const kinds = ['epic', 'ticket', 'task', 'project', 'cost_unit'].map(slug => ({ id: `k-${slug}`, slug, label: slug === 'cost_unit' ? 'Cost unit' : slug[0].toUpperCase() + slug.slice(1), short_prefix: slug === 'cost_unit' ? 'CU' : slug.slice(0, 3).toUpperCase(), icon: slug, allowed_child_kinds: null, field_schema: {} }))
  // Changes another session makes to an entry just before this page's next correction.
  const meanwhile: Record<string, Partial<Entry>> = {}
  const events: EntryEvent[] = []
  return { plugins, units, rates, periods, entries, principals, kinds, meanwhile, events, counter: { next: 1 } }
}
export type BusinessData = ReturnType<typeof businessData>

// Preserve the recent fixtures while adding an open period outside the server's
// default one-year window, with entries that actually belong to that period.
export function addHistoricalPeriod(data: BusinessData) {
  const period = { ...data.periods[0], id: 'p-me-old', starts_at: local('2024-09-16'), ends_at: local('2024-09-23') }
  data.periods.push(period)
  for (const original of data.entries.filter(e => e.period_id === 'p-me-38')) {
    const shift = (at: string) => at.replace('2026-09-15', '2024-09-17').replace('2026-09-17', '2024-09-19')
    data.entries.push({ ...original, id: `${original.id}-old`, period_id: period.id, started_at: shift(original.started_at), ended_at: shift(original.ended_at), rate_amount: '90', amount: amountFor('90', original.duration_seconds) })
  }
  data.rates.find(r => r.id === 'r-3')!.effective_from = '2024-01-01'
  return period
}
export interface BusinessCall { path: string; method: string; body: unknown; query: URLSearchParams }

// Money fields go out as JSON number tokens.
function exact(value: unknown) {
  return JSON.stringify(value).replace(/"(amount|rate_amount|bill_amount|internal_amount)":"(-?\d+(?:\.\d+)?)"/g, '"$1":$2')
}
const digestOf = (period: Period) => (period.revision.toString(16).padStart(2, '0')).repeat(32).slice(0, 64)

export async function mockBusiness(page: Page, data: BusinessData, options: BusinessMockOptions = {}) {
  const calls: BusinessCall[] = []
  await page.clock.setFixedTime(NOW)
  await page.addInitScript(() => { class Quiet { addEventListener() {} close() {} }; Object.assign(window, { EventSource: Quiet }) })
  const json = (route: Route, status: number, value: unknown, headers: Record<string, string> = {}) => route.fulfill({ status, contentType: 'application/json', body: exact(value), headers })
  const handler = async (route: Route) => {
    const request = route.request(), url = new URL(request.url()), path = url.pathname, method = request.method(), q = url.searchParams
    let body: unknown = null
    try { body = request.postDataJSON() } catch { body = null }
    const isCostNodes = path === '/api/nodes' && ((method === 'GET' && q.get('kind') === 'cost_unit') || (method === 'POST' && (body as { kind_id?: string })?.kind_id === 'k-cost_unit'))
    const known = path === '/api/me' || path === '/api/me/permissions' || path === '/api/members' || path === '/api/plugins' || path.startsWith('/api/plugins/') || path === '/api/kinds' || path === '/api/business/principals'
      || path.startsWith('/api/cost-units/') || path.startsWith('/api/time-') || path.endsWith('/time-totals') || isCostNodes
      || (path === '/api/events' && data.events.length > 0) || (/^\/api\/events\/\d+\/undo$/.test(path) && data.events.some(e => path === `/api/events/${e.id}/undo`))
    if (!known) return route.fallback()
    calls.push({ path, method, body, query: q })
    if (path === '/api/me') return route.fulfill({ json: { principal: { id: me.id, name: me.name, kind: 'person', roles: [options.role ?? 'admin'] }, tenant: { id: 't1', name: 'INSPR Studio' } } })
    if (path === '/api/me/permissions') return route.fulfill({ json: mockEffectivePermissions(options.role ?? 'admin', q.get('project_id') ?? undefined) })
    if (path === '/api/members') {
      if (options.noDirectory) return route.fulfill({ status: 403, json: { error: 'members unavailable' } })
      return route.fulfill({ json: {
        people: data.principals.filter(p => p.kind === 'person').map(p => ({ principal_id: p.id, name: p.name, workspace_role: { id: `role-${p.roles[0]}`, key: p.roles[0], name: p.roles[0][0].toUpperCase() + p.roles[0].slice(1) } })),
        agents: data.principals.filter(p => p.kind === 'agent').map(p => ({ principal_id: p.id, name: p.name, workspace_role: null, key_count: 0, last_seen_at: null, service: p.roles.includes('system') })),
        invites: [], imported: [],
      } })
    }
    if (path === '/api/plugins') return route.fulfill({ json: data.plugins })
    const install = /^\/api\/plugins\/([a-z_]+)\/installation$/.exec(path)
    if (install) {
      if ((options.role ?? 'admin') !== 'admin') return route.fulfill({ status: 403, json: { error: 'admin required' } })
      const plugin = data.plugins.find(p => p.id === install[1])!
      const write = body as { manifest_digest_sha256: string; enabled: boolean; permissions: string[] }
      plugin.installation = { ...plugin.installation, ...write, updated_at: NOW.toISOString() }
      return route.fulfill({ json: plugin.installation })
    }
    if (path === '/api/kinds') {
      if (method === 'POST') { const kind = { id: `k-${(body as { slug: string }).slug}`, ...(body as object) }; data.kinds.push(kind as never); return route.fulfill({ status: 201, json: kind }) }
      return route.fulfill({ json: { items: data.kinds } })
    }
    if (path === '/api/business/principals') {
      if (options.noDirectory) return route.fulfill({ status: 403, json: { error: 'admin or member person required' } })
      return route.fulfill({ json: data.principals })
    }
    if (isCostNodes) {
      if (method === 'POST') { const node = costUnit(`cu-new-${data.counter.next}`, `CU-${10 + data.counter.next++}`, (body as { title: string }).title); data.units.push(node); return route.fulfill({ status: 201, json: node }) }
      return route.fulfill({ json: { items: data.units, next_cursor: null } })
    }
    const rates = /^\/api\/cost-units\/([^/]+)\/rates$/.exec(path)
    if (rates) {
      const list = data.rates.filter(r => r.cost_unit_node_id === rates[1])
      if (method === 'GET') return json(route, 200, list)
      const write = body as Omit<Rate, 'id' | 'cost_unit_node_id' | 'created_by_principal_id' | 'created_at'> & { internal_amount: number | string; bill_amount: number | string }
      const same = list.filter(r => r.unit === write.unit && r.currency === write.currency)
      if (same.some(r => r.effective_from === write.effective_from)) return route.fulfill({ status: 409, json: { code: 'conflict', message: 'a rate already starts on that date' } })
      const open = same.find(r => !r.effective_until && r.effective_from < write.effective_from)
      if (open) open.effective_until = write.effective_from
      const created: Rate = { ...write, internal_amount: String(write.internal_amount), bill_amount: String(write.bill_amount), id: `r-new-${data.counter.next++}`, cost_unit_node_id: rates[1], created_by_principal_id: me.id, created_at: NOW.toISOString() } as Rate
      data.rates.push(created)
      return json(route, 201, created)
    }
    if (path === '/api/time-periods') {
      if (method === 'POST') {
        const write = body as { principal_id: string; starts_at: string; ends_at: string }
        if (data.periods.some(p => p.principal_id === write.principal_id && p.starts_at < write.ends_at && p.ends_at > write.starts_at)) return route.fulfill({ status: 409, json: { error: 'period overlaps an existing period' } })
        const period: Period = { id: `p-new-${data.counter.next++}`, ...write, state: 'open', revision: 1, approval: null }
        data.periods.push(period)
        return route.fulfill({ status: 201, json: period })
      }
      const who = q.get('principal_id')
      const defaultSince = new Date(NOW)
      defaultSince.setUTCHours(0, 0, 0, 0)
      defaultSince.setUTCFullYear(defaultSince.getUTCFullYear() - 1)
      const since = Date.parse(q.get('since') ?? defaultSince.toISOString())
      const until = q.has('until') ? Date.parse(q.get('until')!) : Infinity
      const list = data.periods.filter(p => (!who || p.principal_id === who) && Date.parse(p.ends_at) >= since && Date.parse(p.starts_at) < until)
        .sort((a, b) => b.starts_at.localeCompare(a.starts_at) || a.id.localeCompare(b.id))
      const after = q.get('after_id')
      const offset = after ? list.findIndex(p => p.id === after) + 1 : 0
      const limit = Number(q.get('limit') ?? 100)
      const items = list.slice(offset, offset + limit)
      return route.fulfill({ json: items, headers: offset + limit < list.length ? { 'X-Next-Cursor': items.at(-1)!.id } : {} })
    }
    const approve = /^\/api\/time-periods\/([^/]+)\/approve$/.exec(path)
    if (approve) {
      const period = data.periods.find(p => p.id === approve[1])!
      const write = body as { expected_revision: number; expected_entries_sha256: string }
      if (options.approveConflict || write.expected_revision !== period.revision || write.expected_entries_sha256 !== digestOf(period)) return route.fulfill({ status: 409, json: { error: 'period changed; reload before approving' } })
      const seconds = data.entries.filter(e => e.period_id === period.id).reduce((s, e) => s + e.duration_seconds, 0)
      Object.assign(period, { state: 'approved', approval: { approved_by_principal_id: me.id, entries_sha256: digestOf(period), total_seconds: seconds, event_id: 99 } })
      return route.fulfill({ json: period })
    }
    const one = /^\/api\/time-periods\/([^/]+)$/.exec(path)
    if (one) {
      const period = data.periods.find(p => p.id === one[1])
      if (!period) return route.fulfill({ status: 404, json: { error: 'not found' } })
      return route.fulfill({ json: period, headers: { 'X-Entries-SHA256': digestOf(period), 'Access-Control-Expose-Headers': 'X-Entries-SHA256' } })
    }
    if (path === '/api/time-entries') {
      if (method === 'POST') {
        const write = body as { period_id: string; cost_unit_node_id: string; currency: string; principal_id: string; node_id: string; started_at: string; ended_at: string; note: string }
        const period = data.periods.find(p => p.id === write.period_id)!
        if (period.state !== 'open' || write.started_at < period.starts_at || write.ended_at > period.ends_at) return route.fulfill({ status: 409, json: { error: 'entry must fit its principal\'s open period' } })
        const bill = data.rates.find(r => r.cost_unit_node_id === write.cost_unit_node_id && r.unit === 'hour' && !r.effective_until)!.bill_amount
        const seconds = (Date.parse(write.ended_at) - Date.parse(write.started_at)) / 1000
        const created: Entry = { id: `e-new-${data.counter.next++}`, ...write, source: 'manual', agent_run_id: null, duration_seconds: seconds, rate_amount: bill, amount: amountFor(bill, seconds), updated_at: nextRevision() }
        data.entries.push(created)
        period.revision += 1
        return json(route, 201, created)
      }
      const list = data.entries.filter(e => (!q.get('period_id') || e.period_id === q.get('period_id')) && (!q.get('principal_id') || e.principal_id === q.get('principal_id')) && (!q.get('node_id') || e.node_id === q.get('node_id')))
      return json(route, 200, list)
    }
    const oneEntry = /^\/api\/time-entries\/([^/]+)$/.exec(path)
    if (oneEntry) {
      const current = data.entries.find(e => e.id === oneEntry[1])
      if (!current) return route.fulfill({ status: 404, json: { error: 'not found' } })
      const period = data.periods.find(p => p.id === current.period_id)!
      if (current.principal_id !== me.id && (options.role ?? 'admin') !== 'admin') return route.fulfill({ status: 403, json: { error: 'entry author or admin required' } })
      if (period.state !== 'open') return route.fulfill({ status: 409, json: { error: 'approved periods are immutable; time entries cannot be changed' } })
      const other = data.meanwhile[current.id]
      if (other) { Object.assign(current, other, { updated_at: nextRevision() }); delete data.meanwhile[current.id]; period.revision += 1 }
      const since = request.headers()['if-unmodified-since']
      if (since !== undefined && since !== current.updated_at) return json(route, 412, { error: 'time entry changed; reload before correcting', current }, { ETag: `"${current.updated_at}"` })
      const event = (type: string, before: Entry | null, after: Entry | null) => data.events.push({ id: 500 + data.events.length, actor_principal_id: me.id, node_id: current.node_id, type, before, after, at: NOW.toISOString(), undo_of: null })
      if (method === 'DELETE') {
        data.entries.splice(data.entries.indexOf(current), 1)
        event('time_entry.deleted', { ...current }, null)
        period.revision += 1
        return route.fulfill({ status: 204, body: '' })
      }
      const patch = body as { node_id?: string; cost_unit_node_id?: string; started_at?: string; ended_at?: string; duration_seconds?: number; note?: string }
      const next = { ...current }
      if (patch.node_id) next.node_id = patch.node_id
      if (patch.note !== undefined) next.note = patch.note
      if (patch.cost_unit_node_id) next.cost_unit_node_id = patch.cost_unit_node_id
      const seconds = patch.duration_seconds ?? current.duration_seconds
      if (patch.started_at) next.started_at = new Date(patch.started_at).toISOString()
      next.ended_at = new Date(Date.parse(next.started_at) + seconds * 1000).toISOString()
      if (next.started_at < period.starts_at || next.ended_at > period.ends_at) return route.fulfill({ status: 409, json: { error: 'entry must fit its open period' } })
      if (next.cost_unit_node_id !== current.cost_unit_node_id || next.started_at.slice(0, 10) !== current.started_at.slice(0, 10)) {
        const day = next.started_at.slice(0, 10)
        const found = data.rates.filter(r => r.cost_unit_node_id === next.cost_unit_node_id && r.unit === 'hour' && r.currency === next.currency && r.effective_from <= day && (!r.effective_until || r.effective_until > day))
        if (found.length !== 1) return route.fulfill({ status: 409, json: { error: 'exactly one effective hourly rate is required' } })
        next.rate_amount = found[0].bill_amount
      }
      Object.assign(next, { duration_seconds: seconds, amount: amountFor(next.rate_amount, seconds), updated_at: nextRevision() })
      event('time_entry.updated', { ...current }, { ...next })
      Object.assign(current, next)
      period.revision += 1
      return json(route, 200, current, { ETag: `"${current.updated_at}"` })
    }
    if (path === '/api/events') {
      const after = Number(q.get('after') ?? 0), node = q.get('node_id')
      return json(route, 200, { items: data.events.filter(e => e.id > after && (!node || e.node_id === node)), next_after: null })
    }
    const undo = /^\/api\/events\/(\d+)\/undo$/.exec(path)
    if (undo) {
      const original = data.events.find(e => e.id === Number(undo[1]))!
      if (original.undo_of !== null || data.events.some(e => e.undo_of === original.id) || original.type !== 'time_entry.deleted' || !original.before) return route.fulfill({ status: 409, json: { code: 'conflict', message: 'conflict' } })
      const period = data.periods.find(p => p.id === original.before!.period_id)!
      if (period.state !== 'open') return route.fulfill({ status: 409, json: { code: 'conflict', message: 'conflict' } })
      const restored = { ...original.before, updated_at: nextRevision() }
      data.entries.push(restored)
      period.revision += 1
      const undone: EntryEvent = { id: 500 + data.events.length, actor_principal_id: me.id, node_id: original.node_id, type: original.type, before: null, after: restored, at: NOW.toISOString(), undo_of: original.id }
      data.events.push(undone)
      return json(route, 201, undone)
    }
    const totals = /^\/api\/nodes\/([^/]+)\/time-totals$/.exec(path)
    if (totals) {
      const approvedOnly = q.get('approved_only') === 'true'
      const list = data.entries.filter(e => e.node_id === totals[1] && (!approvedOnly || data.periods.find(p => p.id === e.period_id)?.state === 'approved'))
      const seconds = list.reduce((s, e) => s + e.duration_seconds, 0)
      const amount = list.reduce((sum, e) => sum + BigInt(e.amount.replace('.', '')), 0n).toString().padStart(5, '0')
      return json(route, 200, { node_id: totals[1], duration_seconds: seconds, amounts: list.length ? [{ currency: 'EUR', amount: `${amount.slice(0, -4)}.${amount.slice(-4)}` }] : [] })
    }
    return route.fallback()
  }
  await page.route('**/api/**', handler)
  return calls
}
