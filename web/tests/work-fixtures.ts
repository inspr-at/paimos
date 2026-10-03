// SPDX-License-Identifier: AGPL-3.0-only
// A small in-memory list API for the Projects and project list pages. It applies
// the B1 query semantics (within, kind, state, priority, assignee, q, hide_closed,
// sort, facets, cursor paging) so specs can assert on real behaviour.
import type { Page, Route } from '@playwright/test'
import { hideChoice, hiddenStates } from '../src/lib/hideStates'
import { deriveAgentState, normalizeAgentState, STATE_PRIORITY } from '../src/lib/agentSignals.ts'
import { benefitIssues, completedTicketState } from '../src/lib/ticketBenefits.ts'
import { leadWorkerKey, who, type LiveAgent } from '../src/lib/liveAgents.ts'
import { mockEffectivePermissions } from './authz-fixtures'
import { compareModelSort, planningSortValue, type PlanningColumn } from '../src/lib/planning.ts'
import { defaultStatusHelp } from '../src/lib/statusDefinitions.ts'

export const me = { id: '11111111-1111-4111-8111-111111111111', name: 'Markus Barta' }
const mira = { id: '22222222-2222-4222-8222-222222222222', name: 'Mira Holm' }
const now = Date.parse('2026-09-23T12:00:00Z')
// Like the server, every node write moves updated_at forward, whatever the clock says.
const forward = (previous: string, at: number) => new Date(Math.max(at, Date.parse(previous) + 1)).toISOString()
const ago = (hours: number) => new Date(now - hours * 3_600_000).toISOString()

export interface MockNode {
  id: string; key: string; kind_slug: string; title: string; body: string; state: string
  human_check?: string | null
  fields: Record<string, unknown>; parent_id: string | null; project: string
  created_at: string; updated_at: string
  estimate?: import('../src/lib/estimates').TicketEstimate
  eta?: Record<string, unknown>
  planning?: import('../src/lib/planning').TicketPlanning
}
export interface MockView {
  id: string; owner_principal_id: string; project_id: string | null; name: string; filters: Record<string, string>
  sort: { field: string; direction: string }; sort_keys: string[]; group_by: string; columns: string[]; shared: boolean
  created_at: string; updated_at: string; deleted_at: string | null
}
// A saved view as the server stores it (ids are UUIDs, like the server's).
export function mockView(partial: Partial<MockView> & Pick<MockView, 'id' | 'name'>): MockView {
  return {
    owner_principal_id: me.id, project_id: 'p-pharos', filters: {}, sort: { field: 'position', direction: 'asc' }, sort_keys: [], group_by: 'none', columns: [], shared: false,
    created_at: ago(24 * 3), updated_at: ago(24 * 3), deleted_at: null, ...partial,
  }
}
export interface MockOptions {
  conflictAlways?: string
  delayChildren?: number
  readOnly?: boolean
  failList?: boolean
  failProjects?: boolean
  delayList?: number
  failPatch?: boolean
  conflictOn?: string
  bigProject?: number
  // Cap an API page for pagination specs without generating hundreds of tickets.
  listPageSize?: number
  failUpload?: boolean
  // The signed-in person is a workspace admin (shared project groups).
  admin?: boolean
  principalKind?: 'person' | 'agent'
  // GET /api/harness-sessions/live answers with this status instead (AEON-184).
  liveStatus?: number
  // The live answer names more sessions than it lists (AEON-233).
  liveTruncated?: boolean
  // An answer the test holds back (a slow network, AEON-326): computed when
  // the request arrives (writes apply then), sent once until settles.
  hold?: (request: { path: string; method: string; query: URLSearchParams }) => { until: Promise<unknown>; computed?: () => void } | undefined
}

const STATE_ORDER = ['new', 'backlog', 'open', 'blocked', 'in_progress', 'active', 'qa', 'done', 'delivered', 'accepted', 'cancelled', 'archived']
const PRIORITY_ORDER = ['high', 'medium', 'low', 'none']

export function fixtures(options: MockOptions = {}) {
  const projects = [
    { id: 'p-pharos', key: 'PRJ-17', title: 'Pharos', state: 'active', classic: 'PHAROS', description: 'Fleet management and host access for the **INSPR** family.', last: ago(2) },
    { id: 'p-aeon', key: 'PRJ-35', title: 'Aeon', state: 'active', classic: 'AEON', description: '# Aeon\n\nThe successor of Paimos.', last: ago(30) },
    { id: 'p-glint', key: 'PRJ-28', title: 'Glint', state: 'archived', classic: 'GLINT', description: 'Glanceable ticket context.', last: ago(24 * 40) },
    { id: 'p-frozen', key: 'PRJ-26', title: 'Studio infrastructure', state: 'frozen', classic: '', description: '', last: ago(24 * 9) },
  ]
  const nodes: MockNode[] = []
  const add = (node: Partial<MockNode> & Pick<MockNode, 'id' | 'key' | 'kind_slug' | 'title' | 'state' | 'project'>) => {
    const full: MockNode = { body: '', fields: {}, parent_id: node.project, created_at: ago(24 * 20), updated_at: ago(5), ...node }
    nodes.push(full)
    return full
  }
  const epic = add({ id: 'n-epic', key: 'PHAROS-10', kind_slug: 'epic', title: 'Guarded multi-cloud provisioning', state: 'backlog', project: 'p-pharos', fields: { priority: 'high' }, updated_at: ago(40) })
  add({ id: 'n-1', key: 'PHAROS-11', kind_slug: 'ticket', title: 'Connect Hetzner Cloud for managed provisioning', state: 'in-progress', project: 'p-pharos', parent_id: epic.id, fields: { priority: 'high', assignee: me.id, release: { id: 5668, label: 'v4.7.8' }, tags: [{ id: 16, name: 'CUSTOMERPORTAL', color: 'blue' }, { id: 5, name: 'hsb8', color: 'green' }] }, updated_at: ago(1), body: '## Acceptance\n\n- [x] Token stored in the vault\n- [ ] Cleanup runs => nothing left behind\n\n`a => b`' })
  const parentTicket = add({ id: 'n-2', key: 'PHAROS-12', kind_slug: 'ticket', title: 'Add an Oracle Cloud connector', state: 'backlog', project: 'p-pharos', parent_id: epic.id, fields: { priority: 'medium' }, updated_at: ago(3) })
  add({ id: 'n-3', key: 'PHAROS-13', kind_slug: 'task', title: 'Run the disposable Hetzner end-to-end check', state: 'qa', project: 'p-pharos', parent_id: parentTicket.id, fields: { priority: 'low', assignee: mira.id, tags: [{ id: 11, name: 'BUG', color: 'red' }] }, updated_at: ago(6) })
  add({ id: 'n-4', key: 'PHAROS-14', kind_slug: 'ticket', title: 'Visual acceptance of the version pill', state: 'new', project: 'p-pharos', fields: {}, updated_at: ago(12) })
  add({ id: 'n-5', key: 'PHAROS-15', kind_slug: 'ticket', title: 'Beacon health probes', state: 'done', project: 'p-pharos', fields: { priority: 'medium' }, updated_at: ago(48) })
  add({ id: 'n-6', key: 'PHAROS-16', kind_slug: 'ticket', title: 'Retire the old dashboard', state: 'cancelled', project: 'p-pharos', fields: { priority: 'low' }, updated_at: ago(72) })
  add({ id: 'n-a1', key: 'AEON-1', kind_slug: 'ticket', title: 'Aeon foundation', state: 'backlog', project: 'p-aeon', fields: { priority: 'high' } })
  for (let index = 0; index < (options.bigProject ?? 0); index++) {
    add({ id: `n-big-${index}`, key: `AEON-${100 + index}`, kind_slug: 'ticket', title: `Generated ticket ${index + 1}`, state: 'backlog', project: 'p-aeon', fields: { priority: 'medium' }, updated_at: ago(index + 1) })
  }
  const activity: Record<string, { id: string; at: string; type: 'comment' | 'change' | 'created'; author: { id: string | null; name: string; automatic?: boolean; job?: string; reason?: string }; body_markdown?: string; changes?: { field: string; from: string | null; to: string | null }[] }[]> = {
    // Newest first, like the API.
    'n-1': [
      { id: '9', at: ago(0.5), type: 'comment', author: { id: mira.id, name: mira.name }, body_markdown: 'Token rotation is **done**; cleanup next.' },
      { id: '8', at: ago(0.9), type: 'comment', author: { id: me.id, name: me.name }, body_markdown: 'Picked this up. `a => b` stays literal.' },
      { id: '8a', at: ago(2), type: 'comment', author: { id: me.id, name: me.name }, body_markdown: 'I work on this — session: cursor-harbor-fleet (70648dfe-5a0c-4a6f-86f4-dab0870dde5c); role: builder; model: grok-4.6; started: 2026-09-23T10:00:00Z\n\nDelegated via Cursor CLI; coordinator owns the merge.' },
      { id: '7', at: ago(26), type: 'change', author: { id: me.id, name: me.name }, changes: [{ field: 'status', from: 'backlog', to: 'in-progress' }] },
      { id: '6', at: ago(26.02), type: 'change', author: { id: me.id, name: me.name }, changes: [{ field: 'status', from: 'new', to: 'backlog' }, { field: 'priority', from: 'medium', to: 'high' }] },
      { id: '5', at: ago(24 * 20), type: 'created', author: { id: me.id, name: me.name } },
    ],
  }
  const relations = [
    { id: 'r-1', source_node_id: 'n-4', target_node_id: 'n-1', type: 'blocks', created_at: ago(40) },
    { id: 'r-2', source_node_id: 'n-1', target_node_id: 'n-5', type: 'relates', created_at: ago(40) },
  ]
  // Attachments per node (B8 shape: decimal-string positions, created_by is an id).
  const attachment = (id: string, node: string, name: string, position: number, extra: Record<string, unknown> = {}) => ({
    id, node_id: node, sha256: id.padEnd(64, '0'), name, content_type: 'image/png', size: 184_320 + position, width: 1440, height: 900, caption: '',
    position: String(position), created_by: me.id, created_at: ago(30 - position / 1024), updated_at: ago(30 - position / 1024), deleted_at: null as string | null, ...extra,
  })
  const attachments: Record<string, ReturnType<typeof attachment>[]> = {
    'n-1': [
      attachment('att-1', 'n-1', 'fleet-list-before.png', 1024, { caption: 'Before: card grid' }),
      attachment('att-2', 'n-1', 'fleet-list-after.png', 2048, { caption: 'After: compact list' }),
      attachment('att-3', 'n-1', 'phone.png', 3072, { width: 390, height: 844 }),
      attachment('att-4', 'n-1', 'provider-notes.pdf', 4096, { content_type: 'application/pdf', width: null, height: null, size: 48_200 }),
    ],
  }
  const preferences: Record<string, Record<string, unknown>> = {}
  // Events the mock records (attachment removals and their undo), oldest first.
  const events: { id: number; node_id: string; type: string; before: ReturnType<typeof attachment>; after: ReturnType<typeof attachment>; undo_of: number | null }[] = []
  // U22 saved views and bulk batches (their before and after, for undo).
  const views: MockView[] = []
  const batches: { id: number; before: MockNode[]; after: MockNode[]; undone: boolean }[] = []
  const people: { id: string; name: string; has_avatar?: boolean }[] = [me, mira]
  // Agents working right now (AEON-184): GET /api/harness-sessions/live items.
  const live: LiveAgentMock[] = []
  return { projects, nodes, people, activity, relations, attachments, preferences, events, views, batches, live, counter: { next: 100 } }
}
// A 1x1 PNG for every attachment variant.
export const PNG = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==', 'base64')

export type Fixtures = ReturnType<typeof fixtures>
export interface LiveAgentMock {
  project_id: string; session_id?: string; principal_id?: string; name?: string; display_label?: string
  harness: 'codex' | 'claude' | 'pi' | 'cursor' | 'grok'; management_mode: 'managed' | 'unmanaged'; role: 'worker' | 'coordinator'
  phase: 'starting' | 'working' | 'stopping' | 'yielded' | 'stopped'; activity: 'busy' | 'unknown' | 'idle' | 'throttled'
  ticket: { id: string; key: string; title: string; project_id: string } | null; since: string; heartbeat_at: string | null
  stopped_at?: string | null; stop_reason?: string | null; run_status?: string | null; needs_attention?: boolean; has_problem?: boolean
  progress_pct?: number | null; finished: boolean
}
// One live agent on the fixture clock: started `minutes` ago, heartbeat half a minute ago.
export function liveAgent(fields: Partial<LiveAgentMock> & Pick<LiveAgentMock, 'project_id'>, minutes = 12): LiveAgentMock {
  return {
    harness: 'claude', management_mode: 'unmanaged', role: 'worker', phase: 'working', activity: 'busy', ticket: null,
    since: new Date(now - minutes * 60_000).toISOString(), heartbeat_at: new Date(now - 30_000).toISOString(), finished: false, ...fields,
  }
}
export interface Call { path: string; method: string; query: URLSearchParams; body: unknown; headers: Record<string, string> }

// hideLead: the caller cannot read harness sessions, so the server sends no
// lead_worker (AEON-316 gates it on the same visibility as the live read).
function item(node: MockNode, data: Fixtures, hideLead = false, usage = true) {
  const kindIds: Record<string, string> = { epic: 'k-epic', ticket: 'k-ticket', task: 'k-task', project: 'k-project' }
  const parent = node.parent_id ? data.nodes.find(n => n.id === node.parent_id) : undefined
  const project = data.projects.find(p => p.id === node.project)!
  // has_avatar as the server sends it (U27), when the spec gives it; absent,
  // the payload reads like an older server's.
  const person = typeof node.fields.assignee === 'string' ? data.people.find(p => p.id === node.fields.assignee) : undefined
  const assignee = person ? { id: person.id, name: person.name, ...(person.has_avatar === undefined ? {} : { has_avatar: person.has_avatar }) } : null
  const lead = hideLead ? undefined : leadOf(data, node)
  return {
    id: node.id, key: node.key, kind_id: kindIds[node.kind_slug], title: node.title, body: node.body, fields: node.fields, state: node.state, human_check: node.human_check ?? null,
    parent_id: node.parent_id, position: '0', created_at: node.created_at, updated_at: node.updated_at, deleted_at: null,
    kind_slug: node.kind_slug, kind_label: node.kind_slug[0].toUpperCase() + node.kind_slug.slice(1),
    priority: typeof node.fields.priority === 'string' ? node.fields.priority : null, assignee,
    parent: parent ? { id: parent.id, key: parent.key, title: parent.title, kind_slug: parent.kind_slug } : node.parent_id ? { id: project.id, key: project.key, title: project.title, kind_slug: 'project' } : null,
    children_count: data.nodes.filter(n => n.parent_id === node.id).length,
    project: { id: project.id, key: project.key, title: project.title },
    epic: epicAbove(node, data),
    // The server always states finished on an estimate; false unless a spec sets it.
    ...(node.eta ? { eta: { finished: false, ...node.eta } } : {}),
    ...(node.estimate ? { estimate: node.estimate } : {}),
    ...(node.planning ? { planning: usage ? node.planning : { ...node.planning, cost: undefined } } : {}),
    ...(lead ? { lead_worker: { name: who(lead), key: leadWorkerKey(lead) } } : {}),
  }
}
// The nearest epic above a node, like the server's list projection.
function epicAbove(node: MockNode, data: Fixtures): { id: string; key: string; title: string } | null {
  let up = node.parent_id ? data.nodes.find(n => n.id === node.parent_id) : undefined
  while (up && up.kind_slug !== 'epic') up = up.parent_id ? data.nodes.find(n => n.id === up!.parent_id) : undefined
  return up ? { id: up.id, key: up.key, title: up.title } : null
}
function projectItem(project: Fixtures['projects'][number]) {
  return {
    id: project.id, key: project.key, kind_id: 'k-project', title: project.title, body: project.description, state: project.state,
    fields: { classic: project.classic ? { key: project.classic, description: project.description } : {} },
    parent_id: null, position: '0', created_at: ago(24 * 90), updated_at: project.last, deleted_at: null,
    kind_slug: 'project', kind_label: 'Project', priority: null, assignee: null, parent: null, children_count: 0,
    project: { id: project.id, key: project.key, title: project.title },
    epic: null,
  }
}
const listParam = (query: URLSearchParams, name: string) => (query.get(name) ?? '').split(',').map(v => v.trim()).filter(Boolean)
const normal = (state: string) => state.replace(/-/g, '_')
// Same buckets as the project summary: a category is not modelled here.
// Archived has a separate count; every other non-closed state is open.
function canonicalWorkStatus(state: string) {
  const norm = state.trim().toLowerCase().replace(/[\s-]+/g, '_')
  return ['active', 'inprogress'].includes(norm) ? 'in_progress' : norm === 'canceled' ? 'cancelled' : norm
}
function workBucket(state: string): 'open' | 'in_progress' | 'done' | 'cancelled' | 'archived' {
  const norm = state.trim().toLowerCase().replace(/[\s-]+/g, '_')
  if (norm === 'cancelled' || norm === 'canceled') return 'cancelled'
  if (norm === 'accepted' || norm === 'delivered' || norm === 'done') return 'done'
  if (norm === 'in_progress' || norm === 'inprogress' || norm === 'active' || norm === 'qa') return 'in_progress'
  if (norm === 'archived') return 'archived'
  return 'open'
}
// "!" excludes: plain values are alternatives, excluded values must all not match.
function passes(values: string[], has: (value: string) => boolean): boolean {
  const plain = values.filter(v => !v.startsWith('!')), not = values.filter(v => v.startsWith('!')).map(v => v.slice(1))
  return (!plain.length || plain.some(has)) && !not.some(has)
}
function tagNames(node: MockNode): { name: string; color: string }[] {
  const tags = Array.isArray(node.fields.tags) ? node.fields.tags : []
  return tags.flatMap(tag => typeof tag === 'string' ? [{ name: tag, color: '' }] : tag && typeof tag === 'object' && typeof (tag as { name?: unknown }).name === 'string' ? [{ name: (tag as { name: string }).name, color: String((tag as { color?: unknown }).color ?? '') }] : [])
}
function labelOf(value: unknown): string {
  if (typeof value === 'string') return value
  if (value && typeof value === 'object') { const v = (value as { label?: unknown; name?: unknown }).label ?? (value as { name?: unknown }).name; return typeof v === 'string' ? v : '' }
  return ''
}
function costUnit(node: MockNode): string {
  if ('cost_unit' in node.fields) return labelOf(node.fields.cost_unit)
  const classic = node.fields.classic as Record<string, unknown> | undefined
  return classic ? labelOf(classic.cost_unit) : ''
}
const release = (node: MockNode) => labelOf(node.fields.release)
const personName = (data: Fixtures, id: unknown) => typeof id === 'string' ? data.people.find(p => p.id === id)?.name ?? '' : ''
// Mock stand-in for the list query's lead order. The server chooses; this
// only keeps fixture rows in that order (attention at the viewer's thresholds,
// then a worker before a coordinator, then start, heartbeat and public facts).
function compareServerLead(a: LiveAgent, b: LiveAgent) {
  const rank = (agent: LiveAgent) => STATE_PRIORITY[agent.state ?? 'working']
  const beat = (agent: LiveAgent) => {
    const parsed = Date.parse(agent.heartbeat_at ?? '')
    return Number.isNaN(parsed) ? Number.POSITIVE_INFINITY : parsed
  }
  return rank(a) - rank(b)
    || Number(a.role === 'coordinator') - Number(b.role === 'coordinator')
    || Date.parse(a.since) - Date.parse(b.since)
    || beat(a) - beat(b)
    || a.harness.localeCompare(b.harness)
    || (a.activity_sequence ?? 0) - (b.activity_sequence ?? 0)
    || a.phase.localeCompare(b.phase)
    || a.activity.localeCompare(b.activity)
    || who(a).localeCompare(who(b))
    || leadWorkerKey(a).localeCompare(leadWorkerKey(b))
}
// Live workers on one ticket, in the server's lead order.
function liveOn(data: Fixtures, node: MockNode): LiveAgent[] {
  const preferences = normalizeAgentState(data.preferences['agent-state'])
  return data.live.flatMap(agent => {
    const ticket = agent.ticket
    if (!ticket || ticket.id !== node.id || agent.project_id !== node.project || ticket.project_id !== node.project) return []
    if (agent.phase === 'stopped' || agent.stopped_at) return []
    if (/archived/i.test(agent.stop_reason ?? '')) return []
    const live: LiveAgent = { ...agent, ticket }
    return [{ ...live, state: deriveAgentState(live, now, preferences) }]
  }).sort(compareServerLead)
}
function leadOf(data: Fixtures, node: MockNode) { return liveOn(data, node)[0] }
// The name an assignee sort uses: the stored person, else the lead live worker.
// Empty is last in both directions.
function shownAssignee(data: Fixtures, node: MockNode): string {
  const stored = personName(data, node.fields.assignee).trim()
  if (stored) return stored.toLowerCase()
  const lead = leadOf(data, node)
  return lead ? who(lead).trim().toLowerCase() : ''
}

function completionRefusal(node: MockNode, nextState: string, fields: Record<string, unknown>): { error: string; code: string } | null {
  if (node.kind_slug !== 'ticket' || completedTicketState(node.state) || !completedTicketState(nextState)) return null
  const issues = benefitIssues(fields)
  if (!issues.length) return null
  return { error: `before done: ${issues.join('; ')}`, code: 'benefit_required' }
}

export async function mockWork(page: Page, data: Fixtures, options: MockOptions = {}) {
  const calls: Call[] = []
  const started = Date.now()
  await page.route('**/api/**', async arrived => {
    const request = arrived.request(), url = new URL(request.url()), path = url.pathname, method = request.method(), query = url.searchParams
    const held = options.hold?.({ path, method, query })
    const route = held ? heldRoute(arrived, held) : arrived
    let body: unknown = null
    try { body = request.postDataJSON() } catch { body = request.postData() }
    calls.push({ path, method, query, body, headers: request.headers() })
    if (path === '/api/status/help' && method === 'GET') return route.fulfill({ json: defaultStatusHelp() })
    // ---------- Preferences ----------
    const prefPath = /^\/api\/preferences\/([^/]+)$/.exec(path)
    if (prefPath) {
      const key = prefPath[1]
      if (method === 'PUT') { data.preferences[key] = (body as { value: Record<string, unknown> }).value; return route.fulfill({ json: { key, value: data.preferences[key], updated_at: new Date(now).toISOString() } }) }
      return route.fulfill({ json: { key, value: data.preferences[key] ?? null, updated_at: null } })
    }
    // ---------- Attach requests waiting on /agents: none unless a spec registers its own route ----------
    if (path === '/api/agent-pairing/attach/pending' && method === 'GET') return route.fulfill({ json: { requests: [] } })
    // ---------- Events (attachment removals and their undo) ----------
    if (path === '/api/events' && method === 'GET') {
      const after = Number(query.get('after') ?? 0)
      const items = data.events.filter(e => e.id > after && (!query.get('node_id') || e.node_id === query.get('node_id')))
      return route.fulfill({ json: { items, next_after: null } })
    }
    const undoPath = /^\/api\/events\/(\d+)\/undo$/.exec(path)
    if (undoPath && method === 'POST' && !data.batches.some(b => b.id === Number(undoPath[1]))) {
      const event = data.events.find(e => e.id === Number(undoPath[1]))
      if (!event || data.events.some(e => e.undo_of === event.id)) return route.fulfill({ status: 409, json: { error: 'conflict' } })
      const restored = { ...event.before, updated_at: new Date(now + 5000).toISOString() }
      ;(data.attachments[event.node_id] ??= []).push(restored)
      const undone = { id: data.events.length + 1, node_id: event.node_id, type: event.type, before: event.after, after: restored, undo_of: event.id }
      data.events.push(undone)
      return route.fulfill({ status: 201, json: undone })
    }
    // ---------- Attachments ----------
    const listPath = /^\/api\/nodes\/([^/]+)\/attachments$/.exec(path)
    if (listPath) {
      const list = (data.attachments[listPath[1]] ??= [])
      if (method === 'GET') return route.fulfill({ json: [...list].sort((a, b) => Number(a.position) - Number(b.position)) })
      if (options.failUpload) return route.fulfill({ status: 413, json: { error: 'The file is too large' } })
      const raw = request.postDataBuffer()?.toString('latin1') ?? ''
      const files = [...raw.matchAll(/name="file"; filename="([^"]*)"\r\nContent-Type: ([^\r]+)/g)]
      const created = files.map(([, name, type], i) => {
        const id = `att-new-${calls.length}-${i}`
        const last = Math.max(0, ...list.map(a => Number(a.position)))
        const item = { id, node_id: listPath[1], sha256: id.padEnd(64, '0'), name, content_type: type, size: 1024, width: type.startsWith('image/') ? 800 : null, height: type.startsWith('image/') ? 500 : null, caption: '', position: String(last + 1024), created_by: me.id, created_at: new Date(now).toISOString(), updated_at: new Date(now).toISOString(), deleted_at: null }
        list.push(item)
        return item
      })
      return route.fulfill({ status: 201, json: created })
    }
    const attPath = /^\/api\/attachments\/([^/]+)(\/content)?$/.exec(path)
    if (attPath) {
      const [, id, content] = attPath
      if (content) return route.fulfill({ contentType: 'image/png', body: PNG })
      for (const list of Object.values(data.attachments)) {
        const index = list.findIndex(a => a.id === id)
        if (index === -1) continue
        if (method === 'DELETE') {
          const [gone] = list.splice(index, 1)
          data.events.push({ id: data.events.length + 1, node_id: gone.node_id, type: 'attachment.removed', before: gone, after: { ...gone, deleted_at: new Date(now).toISOString() }, undo_of: null })
          return route.fulfill({ status: 204 })
        }
        if (method === 'PATCH') {
          const expected = request.headers()['if-unmodified-since']
          if (expected && expected !== list[index].updated_at) return route.fulfill({ status: 412, json: { error: 'stale attachment' } })
          Object.assign(list[index], body as object, { updated_at: new Date(now + 1000 * calls.length).toISOString() })
          return route.fulfill({ json: list[index] })
        }
      }
      return route.fulfill({ status: 404, json: { error: 'attachment not found' } })
    }
    // ---------- Saved views (U22) ----------
    if (path === '/api/views' && method === 'GET') {
      const project = query.get('project_id')
      const items = data.views.filter(v => !v.deleted_at && (v.owner_principal_id === me.id || v.shared) && (!project || v.project_id === project))
      return route.fulfill({ json: { items } })
    }
    if (path === '/api/views' && method === 'POST') {
      const input = body as Partial<MockView>
      if (!input.name?.trim()) return route.fulfill({ status: 400, json: { error: 'name is required' } })
      const view = mockView({ id: `00000000-0000-4000-8000-${String(data.counter.next++).padStart(12, '0')}`, name: input.name.trim(), project_id: input.project_id ?? null, filters: input.filters ?? {}, sort_keys: input.sort_keys ?? [], group_by: input.group_by ?? 'none', columns: input.columns ?? [], shared: !!input.shared, created_at: new Date(now + calls.length * 1000).toISOString(), updated_at: new Date(now + calls.length * 1000).toISOString() })
      data.views.push(view)
      return route.fulfill({ status: 201, json: view })
    }
    const viewPath = /^\/api\/views\/([^/]+)(\/restore)?$/.exec(path)
    if (viewPath) {
      const view = data.views.find(v => v.id === viewPath[1])
      if (!view || (view.deleted_at && !viewPath[2])) return route.fulfill({ status: 404, json: { error: 'view not found' } })
      if (method !== 'GET' && view.owner_principal_id !== me.id) return route.fulfill({ status: 403, json: { error: 'only the owner can change this view' } })
      if (viewPath[2]) {
        if (!view.deleted_at) return route.fulfill({ status: 409, json: { error: 'view is not deleted' } })
        view.deleted_at = null
        return route.fulfill({ json: view })
      }
      if (method === 'PATCH') { Object.assign(view, body as object, { updated_at: new Date(now + calls.length * 1000).toISOString() }); return route.fulfill({ json: view }) }
      if (method === 'DELETE') { view.deleted_at = new Date(now).toISOString(); return route.fulfill({ status: 204 }) }
      return route.fulfill({ json: view })
    }
    // ---------- Bulk changes (U22) ----------
    if (path === '/api/nodes/bulk' && method === 'POST') {
      if (options.readOnly) return route.fulfill({ status: 403, json: { error: 'forbidden' } })
      const input = body as { ids: string[]; state?: string; priority?: string | null; assignee?: string | null; tags_add?: (string | { name: string; color?: string })[]; tags_remove?: string[]; parent_id?: string; if_unmodified_since?: Record<string, string> }
      const before: MockNode[] = [], after: MockNode[] = [], skipped: { id: string; key?: string; reason: string; code?: string }[] = [], unchanged: string[] = []
      for (const id of input.ids) {
        const node = data.nodes.find(n => n.id === id)
        if (!node) { skipped.push({ id, reason: 'not found' }); continue }
        // Per-node preconditions (AEON-326): changed since the list showed it.
        const seen = input.if_unmodified_since?.[id]
        if (seen && Date.parse(seen) !== Date.parse(node.updated_at)) { skipped.push({ id, key: node.key, reason: 'changed since you loaded it', code: 'conflict' }); continue }
        if (input.parent_id && node.kind_slug === 'task' && data.nodes.find(n => n.id === input.parent_id)?.kind_slug === 'epic') { skipped.push({ id, key: node.key, reason: 'a task cannot sit under an epic' }); continue }
        const old = JSON.parse(JSON.stringify(node)) as MockNode
        const fields = { ...node.fields }
        if ('priority' in input) fields.priority = input.priority
        if ('assignee' in input) fields.assignee = input.assignee
        if (input.tags_add?.length || input.tags_remove?.length) {
          const drop = new Set((input.tags_remove ?? []).map(t => t.toLowerCase()))
          const kept = (Array.isArray(fields.tags) ? fields.tags : []).filter(tag => !drop.has((typeof tag === 'string' ? tag : (tag as { name: string }).name).toLowerCase()))
          for (const tag of input.tags_add ?? []) {
            const name = typeof tag === 'string' ? tag : tag.name
            if (!kept.some(t => (typeof t === 'string' ? t : (t as { name: string }).name).toLowerCase() === name.toLowerCase())) kept.push(typeof tag === 'string' ? { name } : tag)
          }
          fields.tags = kept
        }
        const nextState = input.state ?? node.state
        const refusal = completionRefusal(node, nextState, fields)
        if (refusal) { skipped.push({ id, key: node.key, reason: refusal.error, code: refusal.code }); continue }
        const next = { ...node, fields, state: nextState, parent_id: input.parent_id ?? node.parent_id }
        if (JSON.stringify(next) === JSON.stringify(node)) { unchanged.push(id); continue }
        Object.assign(node, next, { updated_at: forward(node.updated_at, now + 120_000 + calls.length) })
        before.push(old); after.push(JSON.parse(JSON.stringify(node)))
      }
      const eventId = after.length ? 5000 + data.batches.length : null
      if (eventId) data.batches.push({ id: eventId, before, after, undone: false })
      const items = after.map(({ kind_slug: kind, project: _p, ...rest }) => ({ ...rest, kind_id: `k-${kind}`, position: '0', deleted_at: null }))
      return route.fulfill({ json: { event_id: eventId, items, unchanged, skipped } })
    }
    const batchUndo = /^\/api\/events\/(\d+)\/undo$/.exec(path)
    if (batchUndo && method === 'POST' && data.batches.some(b => b.id === Number(batchUndo[1]))) {
      const batch = data.batches.find(b => b.id === Number(batchUndo[1]))!
      const stale = batch.after.some(a => data.nodes.find(n => n.id === a.id)?.updated_at !== a.updated_at)
      if (batch.undone || stale) return route.fulfill({ status: 409, json: { code: 'conflict', message: 'resource changed or event is not reversible' } })
      for (const old of batch.before) { const node = data.nodes.find(n => n.id === old.id)!; Object.assign(node, old, { updated_at: forward(node.updated_at, now + 180_000 + calls.length) }) }
      batch.undone = true
      return route.fulfill({ status: 201, json: { id: batch.id + 1, type: 'node.bulk_changed', undo_of: batch.id } })
    }
    if (path === '/api/me/permissions') return route.fulfill({ json: mockEffectivePermissions(options.readOnly ? 'viewer' : options.admin ? 'admin' : 'member', query.get('project_id') ?? undefined) })
    if (path === '/api/me') return route.fulfill({ json: { principal: { id: me.id, name: me.name, kind: options.principalKind ?? 'person', roles: options.readOnly ? ['viewer'] : options.admin ? ['admin'] : ['member'] }, tenant: { id: 't1', name: 'INSPR Studio' } } })
    if (path === '/api/kinds') return route.fulfill({ json: { items: ['epic', 'ticket', 'task', 'project'].map(slug => ({ id: `k-${slug}`, slug, label: slug[0].toUpperCase() + slug.slice(1), short_prefix: slug.slice(0, 3).toUpperCase(), icon: slug, allowed_child_kinds: null, field_schema: {} })) } })
    // Relations (U27): the server's refusals, in its words, for the picker to show.
    if (path === '/api/relations' && method === 'POST') {
      if (options.readOnly) return route.fulfill({ status: 403, json: { code: 'forbidden', message: 'forbidden' } })
      const input = body as { source_node_id: string; target_node_id: string; type: string }
      let source = input.source_node_id, target = input.target_node_id
      if (source === target) return route.fulfill({ status: 400, json: { code: 'invalid_request', message: 'an item cannot be linked to itself' } })
      if (input.type === 'relates' && source > target) [source, target] = [target, source]
      const keyOf = (id: string) => data.nodes.find(n => n.id === id)?.key ?? 'an item'
      const verb = RELATION_VERBS[input.type] ?? [input.type, input.type]
      if (data.relations.some(r => r.source_node_id === source && r.target_node_id === target && r.type === input.type)) {
        return route.fulfill({ status: 409, json: { code: 'conflict', message: `${keyOf(source)} already ${verb[0]} ${keyOf(target)}.` } })
      }
      const loop = ['blocks', 'implements', 'duplicates'].includes(input.type) ? loopPath(data.relations, input.type, target, source) : null
      if (loop) {
        const chain = loop.length === 2 ? `${keyOf(loop[0])} already ${verb[0]} ${keyOf(loop[1])}` : loop.slice(1).reduce((text, id, i) => i === 0 ? `${keyOf(loop[0])} ${verb[0]} ${keyOf(id)}` : `${text}, which ${verb[0]} ${keyOf(id)}`, '')
        return route.fulfill({ status: 409, json: { code: 'conflict', message: `${keyOf(source)} cannot ${verb[1]} ${keyOf(target)}: ${chain}, so this link would make a loop.` } })
      }
      const relation = { id: `r-new-${data.counter.next++}`, source_node_id: source, target_node_id: target, type: input.type, created_at: new Date(now + calls.length * 1000).toISOString() }
      data.relations.push(relation)
      return route.fulfill({ status: 201, json: relation })
    }
    const relationPath = /^\/api\/relations\/([^/]+)$/.exec(path)
    if (relationPath && method === 'DELETE') {
      if (options.readOnly) return route.fulfill({ status: 403, json: { code: 'forbidden', message: 'forbidden' } })
      const index = data.relations.findIndex(r => r.id === relationPath[1])
      if (index === -1) return route.fulfill({ status: 404, json: { code: 'not_found', message: 'relation or node not found' } })
      data.relations.splice(index, 1)
      return route.fulfill({ status: 204 })
    }
    if (path === '/api/relations') return route.fulfill({ json: { items: data.relations.filter(r => r.source_node_id === query.get('node_id') || r.target_node_id === query.get('node_id')), next_cursor: null } })
    if (path === '/api/nodes/lookup') {
      const ids = (query.get('ids') ?? '').split(',').filter(Boolean)
      // Keys (ticket keys in release notes) also name the key asked for and the project.
      const keys = (query.get('keys') ?? '').split(',').filter(Boolean).map(key => key.toUpperCase())
      return route.fulfill({ json: { items: [...ids.flatMap(id => {
        const node = data.nodes.find(n => n.id === id)
        return node ? [{ id: node.id, key: node.key, title: node.title, state: node.state }] : []
      }), ...keys.flatMap(key => {
        const node = data.nodes.find(n => n.key === key)
        return node ? [{ id: node.id, key: node.key, title: node.title, state: node.state, requested_key: key, project_id: node.project }] : []
      })] } })
    }
    const activityPath = /^\/api\/nodes\/([^/]+)\/(activity|comments)(?:\/(\d+))?$/.exec(path)
    if (activityPath) {
      const [, nodeId, part, commentId] = activityPath
      const list = (data.activity[nodeId] ??= [{ id: `c-${nodeId}`, at: ago(24 * 20), type: 'created', author: { id: me.id, name: me.name } }])
      if (part === 'activity') return route.fulfill({ json: { items: list, next_cursor: null } })
      const text = (body as { body_markdown?: string } | null)?.body_markdown ?? ''
      if (method === 'POST') { const item = { id: String(1000 + calls.length), at: new Date(now).toISOString(), type: 'comment' as const, author: { id: me.id, name: me.name }, body_markdown: text }; list.unshift(item); return route.fulfill({ status: 201, json: item }) }
      const index = list.findIndex(item => item.id === commentId)
      if (index === -1) return route.fulfill({ status: 404, json: { error: 'comment not found' } })
      if (method === 'PATCH') { list[index] = { ...list[index], body_markdown: text }; return route.fulfill({ json: list[index] }) }
      if (method === 'DELETE') { list.splice(index, 1); return route.fulfill({ status: 204 }) }
    }
    if (path === '/api/nodes' && method === 'POST') {
      const input = body as { kind_id: string; title: string; state?: string; fields?: Record<string, unknown>; parent_id?: string; key_prefix?: string }
      const parent = data.nodes.find(n => n.id === input.parent_id)
      const project = parent?.project ?? data.projects.find(p => p.id === input.parent_id)?.id ?? 'p-pharos'
      const node = { id: `n-new-${data.counter.next}`, key: `${input.key_prefix ?? 'TKT'}-${data.counter.next++}`, kind_slug: input.kind_id.slice(2), title: input.title, body: '', state: input.state ?? 'open', fields: input.fields ?? {}, parent_id: input.parent_id ?? null, project, created_at: new Date(now).toISOString(), updated_at: new Date(now).toISOString() }
      data.nodes.push(node)
      const { kind_slug: _k, project: _p, ...rest } = node
      return route.fulfill({ status: 201, json: { ...rest, kind_id: input.kind_id, position: '0', deleted_at: null } })
    }
    const convertPath = /^\/api\/nodes\/([^/]+)\/convert$/.exec(path)
    if (convertPath && method === 'POST') {
      const node = data.nodes.find(n => n.id === convertPath[1])
      if (!node) return route.fulfill({ status: 404, json: { error: 'node not found' } })
      const to = (body as { to_kind?: string } | null)?.to_kind ?? ''
      if (!['epic', 'ticket', 'task'].includes(to)) return route.fulfill({ status: 409, json: { error: 'kind is immutable', code: 'kind_change_not_allowed' } })
      if (to === node.kind_slug) {
        const { kind_slug: kind, project: _project, ...rest } = node
        return route.fulfill({ json: { ...rest, kind_id: `k-${kind}`, position: '0', deleted_at: null } })
      }
      const from = node.kind_slug
      node.kind_slug = to
      node.updated_at = new Date(now + 120_000).toISOString()
      const list = (data.activity[node.id] ??= [])
      list.unshift({ id: `kind-${calls.length}`, at: node.updated_at, type: 'change', author: { id: me.id, name: me.name }, changes: [{ field: 'kind', from, to }] })
      const { kind_slug: kind, project: _project, ...rest } = node
      return route.fulfill({ json: { ...rest, kind_id: `k-${kind}`, position: '0', deleted_at: null } })
    }
    const movePath = /^\/api\/nodes\/([^/]+)\/move$/.exec(path)
    if (movePath) {
      const node = data.nodes.find(n => n.id === movePath[1])!
      // B5: If-Unmodified-Since compared at second precision; 412 carries the current node.
      const expected = request.headers()['if-unmodified-since']
      const seconds = (iso: string) => Math.floor(Date.parse(iso) / 1000)
      if (expected && seconds(expected) !== seconds(node.updated_at)) {
        const { kind_slug: kind, project: _p, ...current } = node
        return route.fulfill({ status: 412, json: { error: 'node has changed', node: { ...current, kind_id: `k-${kind}`, position: '0', deleted_at: null } } })
      }
      node.parent_id = (body as { parent_id: string }).parent_id
      node.updated_at = forward(node.updated_at, now + 90_000)
      const { kind_slug: kind, project: _p, ...rest } = node
      return route.fulfill({ json: { ...rest, kind_id: `k-${kind}`, position: '0', deleted_at: null } })
    }
    if (path === '/api/version') return route.fulfill({ json: { version: '260923120000.0.0', scheme: 'inspr-calendar-v2' } })
    if (path === '/api/harness-sessions/live') {
      if (options.liveStatus) return route.fulfill({ status: options.liveStatus, json: { error: 'not here' } })
      // The server's clock follows the fixture clock from the moment the mock starts.
      return route.fulfill({ json: { items: data.live, at: new Date(now + (Date.now() - started)).toISOString(), fresh_seconds: 120, truncated: options.liveTruncated === true } })
    }
    if (path === '/api/projects') {
      if (options.failProjects) return route.fulfill({ status: 503, json: { error: 'Projects are resting' } })
      const archived = query.get('include_archived') === 'true'
      return route.fulfill({ json: { items: data.projects.filter(p => archived || p.state !== 'archived').map(p => {
        const work = data.nodes.filter(n => n.project === p.id && ['ticket', 'task', 'epic'].includes(n.kind_slug))
        const tally = { open: 0, in_progress: 0, done: 0, cancelled: 0, archived: 0 }
        const statusCounts = new Map<string, { state: string; bucket: ReturnType<typeof workBucket>; count: number }>()
        for (const node of work) {
          const bucket = workBucket(node.state)
          tally[bucket]++
          const state = canonicalWorkStatus(node.state)
          const key = `${state}:${bucket}`, previous = statusCounts.get(key)
          statusCounts.set(key, { state, bucket, count: (previous?.count ?? 0) + 1 })
        }
        const { archived: archivedCount, ...counts } = tally
        return { id: p.id, key: p.key, title: p.title, state: p.state, ...counts, archived_count: archivedCount, status_counts: [...statusCounts.values()], status_counts_truncated: false, total: work.length, last_activity: p.last, people: (p.id === 'p-pharos' ? [mira, me] : p.id === 'p-aeon' ? [me] : []).map(person => ({ ...data.people.find(x => x.id === person.id) ?? person, kind: 'person' })) }
      }) } })
    }
    if (path === '/api/nodes' && method === 'GET') {
      const kinds = listParam(query, 'kind')
      if (kinds.length === 1 && kinds[0] === 'project') return route.fulfill({ json: { items: data.projects.map(projectItem), next_cursor: null } })
      if (options.failList) return route.fulfill({ status: 503, json: { error: 'The list is resting' } })
      if (options.delayList) await new Promise(resolve => setTimeout(resolve, options.delayList))
      if (options.delayChildren && query.get('parent_id') && !data.projects.some(p => p.id === query.get('parent_id'))) await new Promise(resolve => setTimeout(resolve, options.delayChildren))
      const parentFilter = query.get('parent_id')
      const within = query.get('within'), states = listParam(query, 'state'), priorities = listParam(query, 'priority'), assignees = listParam(query, 'assignee'), q = (query.get('q') ?? '').toLowerCase()
      // within: a project, or any node's subtree; parent_id: direct children only.
      const inside = (n: MockNode) => {
        if (!within) return true
        if (data.projects.some(p => p.id === within)) return n.project === within
        for (let parent = n.parent_id; parent; parent = data.nodes.find(x => x.id === parent)?.parent_id ?? null) if (parent === within) return true
        return false
      }
      const tags = listParam(query, 'tag').map(v => v.toLowerCase()), epics = listParam(query, 'epic'), costs = listParam(query, 'cost_unit').map(v => v.toLowerCase()), releases = listParam(query, 'release').map(v => v.toLowerCase())
      const dateField = query.get('date_field'), dateFrom = query.get('date_from'), dateTo = query.get('date_to')
      const dateOf = (n: MockNode): number | null => {
        const raw = dateField === 'created' ? n.created_at : dateField === 'updated' ? n.updated_at : dateField === 'start' ? n.fields.start_date : dateField === 'end' ? n.fields.end_date : n.fields.accepted_at
        return typeof raw === 'string' && !Number.isNaN(Date.parse(raw)) ? Date.parse(raw) : null
      }
      // ids (AEON-326): only those nodes, every other filter still applied.
      const onlyIds = listParam(query, 'ids')
      let rows = data.nodes.filter(n => inside(n) && (!parentFilter || n.parent_id === parentFilter) && (!kinds.length || kinds.includes(n.kind_slug)))
        .filter(n => !onlyIds.length || onlyIds.includes(n.id))
        .filter(n => passes(states, v => v === n.state))
        .filter(n => passes(listParam(query, 'work_state'), v => v === canonicalWorkStatus(n.state)))
        .filter(n => !listParam(query, 'work_bucket').length || listParam(query, 'work_bucket').includes(workBucket(n.state)))
        .filter(n => passes(listParam(query, 'human_check'), v => v === (n.human_check?.trim() ? 'pending' : 'none')))
        .filter(n => passes(priorities, v => v === (typeof n.fields.priority === 'string' ? n.fields.priority : 'none')))
        .filter(n => passes(assignees, v => v === (typeof n.fields.assignee === 'string' ? n.fields.assignee : 'none')))
        .filter(n => passes(tags, v => v === 'none' ? !tagNames(n).length : tagNames(n).some(t => t.name.toLowerCase() === v)))
        .filter(n => passes(epics, v => v === 'none' ? n.kind_slug !== 'epic' && !epicAbove(n, data) : epicAbove(n, data)?.id === v))
        .filter(n => passes(costs, v => v === (costUnit(n).toLowerCase() || 'none')))
        .filter(n => passes(releases, v => v === (release(n).toLowerCase() || 'none')))
        .filter(n => !dateField || (dateOf(n) !== null && (!dateFrom || dateOf(n)! >= Date.parse(dateFrom)) && (!dateTo || dateOf(n)! < Date.parse(dateTo))))
        .filter(n => !q || n.key.toLowerCase().includes(q) || n.title.toLowerCase().includes(q) || n.body.toLowerCase().includes(q))
        .filter(n => {
          if (query.get('hide_closed') !== 'true') return true
          const choice = hideChoice(n.state, workBucket(n.state))
          return !choice || !hiddenStates(listParam(query, 'hide_states')).includes(choice)
        })
      const sort = (query.get('sort') ?? 'position').split(',')
      rows = [...rows].sort((a, b) => {
        for (const raw of sort) {
          const desc = raw.startsWith('-'), field = desc ? raw.slice(1) : raw
          if (field === 'assignee') {
            const x = shownAssignee(data, a), y = shownAssignee(data, b)
            if (!x !== !y) return x ? -1 : 1
            if (x !== y) return (x < y ? -1 : 1) * (desc ? -1 : 1)
            continue
          }
          const etaMissing = (n: MockNode) => field === 'estimate' ? (n.kind_slug === 'epic' ? n.estimate?.hours == null : typeof n.fields.estimate_hours !== 'number' || n.fields.estimate_hours <= 0) : field === 'eta_ready' ? !n.eta?.eta_ready_at : field === 'progress' ? typeof n.eta?.progress_pct !== 'number' : false
          if ((field === 'estimate' || field === 'eta_ready' || field === 'progress') && etaMissing(a) !== etaMissing(b)) return etaMissing(a) ? 1 : -1
          if (field === 'model') {
            const delta = compareModelSort(a, b, desc)
            if (delta !== 0) return delta
            continue
          }
          if (field === 'tokens' || field === 'list_cost' || field === 'paid') {
            // Spent, else estimated; missing values sort last in both directions.
            const plan = (n: MockNode) => (options.readOnly || options.liveStatus === 403) ? null : planningSortValue({ kind_slug: n.kind_slug, fields: n.fields, planning: n.planning }, field as PlanningColumn)
            const x = plan(a), y = plan(b)
            if ((x === null) !== (y === null)) return x === null ? 1 : -1
            if (x !== null && y !== null && x !== y) {
              const before = typeof x === 'bigint' && typeof y === 'bigint' ? x < y
                : typeof x === 'number' && typeof y === 'number' ? x < y
                : String(x) < String(y)
              return (before ? -1 : 1) * (desc ? -1 : 1)
            }
            continue
          }
          const value = (n: MockNode): string | number => field === 'estimate' ? (n.kind_slug === 'epic' ? n.estimate?.hours ?? 0 : Number(n.fields.estimate_hours ?? 0)) : field === 'state' ? (STATE_ORDER.indexOf(normal(n.state)) + 1 || 99)
            : field === 'priority' ? PRIORITY_ORDER.indexOf(typeof n.fields.priority === 'string' ? n.fields.priority : 'none')
            : field === 'updated_at' ? Date.parse(n.updated_at) : field === 'created_at' ? Date.parse(n.created_at) : field === 'key' ? Number(n.key.split('-')[1]) : field === 'title' ? n.title
            : field === 'kind' ? n.kind_slug
            : field === 'eta_ready' ? Date.parse(String(n.eta?.eta_ready_at ?? '')) || 0
            : field === 'progress' ? (typeof n.eta?.progress_pct === 'number' ? n.eta.progress_pct : 0) : 0
          const x = value(a), y = value(b)
          if (x !== y) return (x < y ? -1 : 1) * (desc ? -1 : 1)
        }
        return a.id < b.id ? -1 : 1
      })
      const facets: Record<string, Record<string, number>> = {}
      for (const facet of listParam(query, 'facets')) {
        facets[facet] = {}
        for (const n of rows) {
          const values = facet === 'human_check' ? [n.human_check?.trim() ? 'pending' : 'none'] : facet === 'tag' ? (tagNames(n).length ? tagNames(n).map(t => t.name) : ['none'])
            : facet === 'cost_unit' ? [costUnit(n) || 'none'] : facet === 'release' ? [release(n) || 'none']
            : [facet === 'state' ? n.state : facet === 'kind' ? n.kind_slug : facet === 'priority' ? (typeof n.fields.priority === 'string' ? n.fields.priority : 'none') : (typeof n.fields.assignee === 'string' ? n.fields.assignee : 'none')]
          for (const value of values) facets[facet][value] = (facets[facet][value] ?? 0) + 1
        }
      }
      const limit = Math.min(Number(query.get('limit') ?? 50), options.listPageSize ?? Infinity), offset = Number((query.get('cursor') ?? 'o:0').slice(2))
      const pageRows = rows.slice(offset, offset + limit)
      return route.fulfill({ json: { items: pageRows.map(n => item(n, data, options.liveStatus === 403, !options.readOnly && options.liveStatus !== 403)), next_cursor: offset + limit < rows.length ? `o:${offset + limit}` : null, ...(Object.keys(facets).length ? { facets } : {}) } })
    }
    const agentWorkPath = /^\/api\/nodes\/([^/]+)\/agent-work$/.exec(path)
    if (agentWorkPath && method === 'GET') {
      const id = decodeURIComponent(agentWorkPath[1])
      const node = data.nodes.find(n => n.id === id)
      const kind = node?.kind_slug === 'epic' || node?.kind_slug === 'task' ? node.kind_slug : 'ticket'
      return route.fulfill({ json: {
        node_id: id, kind, currency: 'USD', usage_available: true, includes_descendants: kind === 'epic',
        scope_truncated: false, list_truncated: false, sessions: [],
        totals: {
          session_count: 0, input_tokens: null, output_tokens: null, cached_input_tokens: null,
          tokens_state: 'unknown', cached_state: 'unknown', estimated_cost_usd: null,
          cost_state: 'unknown', currency: 'USD', duration_seconds: null, duration_state: 'unknown',
          unknown_token_sessions: 0, unknown_cost_sessions: 0, unknown_token_models: 0, unknown_cost_models: 0,
        },
      } })
    }
    if (path.startsWith('/api/nodes/')) {
      const id = decodeURIComponent(path.split('/')[3]), node = data.nodes.find(n => n.id === id)
      if (!node) return route.fulfill({ status: 404, json: { error: 'Not found' } })
      if (method === 'DELETE') {
        if (data.nodes.some(n => n.parent_id === id)) return route.fulfill({ status: 409, json: { error: 'node has children' } })
        data.nodes.splice(data.nodes.indexOf(node), 1)
        // The revision of the deletion, as its event names it (live-server.ts, AEON-326).
        return route.fulfill({ status: 204, headers: { 'aeon-revision': new Date(Date.parse(node.updated_at) + 1000).toISOString() } })
      }
      if (method === 'PATCH' && options.readOnly) return route.fulfill({ status: 403, json: { error: 'forbidden' } })
      if (method === 'PATCH' && options.conflictAlways === id) {
        node.updated_at = forward(node.updated_at, now + 45_000 + calls.length); node.title = 'Renamed by Mira'
        return route.fulfill({ status: 412, json: { error: 'node has changed' } })
      }
      if (method === 'PATCH') {
        if (options.failPatch) return route.fulfill({ status: 422, json: { error: 'State is not allowed here' } })
        // Someone else saved this node after the list was read.
        if (options.conflictOn === id && !node.title.endsWith('(edited elsewhere)')) { node.updated_at = forward(node.updated_at, now + 30_000); node.title = `${node.title} (edited elsewhere)` }
        const expected = request.headers()['if-unmodified-since']
        if (expected && expected !== node.updated_at) return route.fulfill({ status: 412, json: { error: 'node has changed' } })
        const patch = body as { state?: string; fields?: Record<string, unknown>; human_check?: string | null }
        if (options.principalKind === 'agent' && 'human_check' in patch) {
          if (patch.human_check === null && node.human_check) return route.fulfill({ status: 403, json: { error: 'only a person can mark a human check checked' } })
          if (patch.human_check && node.fields.human_check_completed) return route.fulfill({ status: 403, json: { error: 'only a person can undo a human check' } })
        }
        const nextState = typeof patch.state === 'string' ? patch.state : node.state
        const nextFields = patch.fields && typeof patch.fields === 'object' && !Array.isArray(patch.fields) ? patch.fields : node.fields
        const refusal = completionRefusal(node, nextState, nextFields)
        if (refusal) return route.fulfill({ status: 422, json: refusal })
        if ('human_check' in patch) {
          if (patch.human_check === null && node.human_check) nextFields.human_check_completed = { text: node.human_check, by: me.id, at: new Date(now).toISOString() }
          else if (patch.human_check) delete nextFields.human_check_completed
        }
        Object.assign(node, body as object, { fields: nextFields, updated_at: forward(node.updated_at, now + 60_000 + calls.length) })
      }
      const { kind_slug: kind, project: _project, ...rest } = node
      return route.fulfill({ json: { ...rest, kind_id: `k-${kind}`, position: '0', deleted_at: null } })
    }
    if (path === '/api/search') {
      const q = (query.get('q') ?? '').toLowerCase()
      const hits = data.nodes.filter(n => n.title.toLowerCase().includes(q)).map(n => ({ node: { ...item(n, data) }, score: 0.9 }))
      return route.fulfill({ json: { items: hits, next_cursor: null } })
    }
    if (path === '/api/outcomes' && method === 'GET') return route.fulfill({ json: { outcomes: [] } })
    return route.fulfill({ status: 404, json: { error: 'Unmocked route' } })
  })
  return calls
}

const RELATION_VERBS: Record<string, [string, string]> = { blocks: ['blocks', 'block'], relates: ['relates to', 'relate to'], implements: ['implements', 'implement'], cites: ['cites', 'cite'], duplicates: ['duplicates', 'duplicate'] }
// The shortest chain of one relation type from `from` to `to`, as the server finds it.
function loopPath(relations: { source_node_id: string; target_node_id: string; type: string }[], type: string, from: string, to: string): string[] | null {
  const parent = new Map<string, string>([[from, '']])
  let frontier = [from]
  while (frontier.length) {
    const next: string[] = []
    for (const r of relations.filter(r => r.type === type && frontier.includes(r.source_node_id))) {
      if (parent.has(r.target_node_id)) continue
      parent.set(r.target_node_id, r.source_node_id)
      if (r.target_node_id === to) {
        const path: string[] = []
        for (let at = to; at; at = parent.get(at)!) path.unshift(at)
        return path
      }
      next.push(r.target_node_id)
    }
    frontier = next
  }
  return null
}

// A route whose answer waits: computed now, fulfilled once the hold settles.
function heldRoute(route: Route, held: { until: Promise<unknown>; computed?: () => void }): Route {
  return {
    request: () => route.request(),
    fulfill: async (answer: Parameters<Route['fulfill']>[0]) => { held.computed?.(); await held.until; return route.fulfill(answer) },
    fallback: (options?: Parameters<Route['fallback']>[0]) => route.fallback(options),
    continue: (options?: Parameters<Route['continue']>[0]) => route.continue(options),
    abort: (code?: string) => route.abort(code),
  } as unknown as Route
}

export function watchErrors(page: Page) {
  const errors: string[] = []
  page.on('pageerror', error => errors.push(error.message))
  page.on('console', message => { if (message.type() === 'error' && !/Failed to load resource/.test(message.text())) errors.push(message.text()) })
  return errors
}
