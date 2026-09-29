// SPDX-License-Identifier: AGPL-3.0-only
// AEON-326: the ticket list, live list, open ticket and lazy Outline, wired as the
// project page wires them, over a small server that answers each request
// whenever the test likes. Random interleavings of loads, next pages, stream
// events, gaps, editor sessions (open, save, 412, cancel), moves, deletes,
// bulk moves and status changes, with every answer delivered late and in any
// order. The assertions are about what the views show and do (the rows a
// list shows, the revision and parent chip a row shows, the panel closing),
// judged against what the tab was told, not against the store's state:
// - a row never goes back, never shows a copy older than the tab knew when
//   that copy arrived (except the open editor's own save), nor one at or
//   before a deletion it knew of;
// - nothing moves under an open editor but its own save or a 412 rebase;
// - a row joins a list only when it was read since the last gap, and leaves
//   it (the panel closes) only when the tab knows it deleted;
// - a parent chip that matched never names another parent while it stays;
// - no save overwrites a field it did not mean to change;
// - once everything arrived and Show applies, the live views converge;
// - an authoritative child count starts a new baseline: subsequent local
//   moves count, and children remain reachable through the expand control.
// LIVE_VIEW_SEEDS sets the number of seeds; LIVE_VIEW_TRACE=<seed> replays
// one with every step logged (run with --reporter=verbose to see it pass).
import { afterAll, beforeAll, describe, expect, it, vi } from 'vitest'
import { computed, effectScope, nextTick, ref } from 'vue'
import type { BulkChange, ListItem, ListParent, WorkNode } from '../src/lib/api'
import { LiveNodeStore } from '../src/lib/liveNodes'
import { rowStore } from '../src/lib/rowStore'
import { compareRows, effectiveSort, filtersFromQuery } from '../src/lib/ticketList'
import { guardedMove, kinds, useTicket } from '../src/lib/useTicket'
import { useLiveList } from '../src/lib/useLiveList'
import { useTicketList } from '../src/lib/useTicketList'
import { useOutline } from '../src/lib/useOutline'

vi.mock('../src/lib/toast', () => ({ toast: vi.fn() }))

const ME = 'me-1', MIRA = 'mira-2', PROJECT = 'p-1'
const BASE = Date.parse('2026-09-29T10:00:00Z')
const at = (n: number) => new Date(BASE + n * 1000).toISOString()
const revOf = (iso: string) => Math.round((Date.parse(iso) - BASE) / 1000)
const EPICS = [{ id: 'e1', key: 'PRJ-101', title: 'Epic one' }, { id: 'e2', key: 'PRJ-102', title: 'Epic two' }]
const PARENTS = [PROJECT, 'e1', 'e2']
const IDS = ['n1', 'n2', 'n3', 'n4']
const PAGE = 2
const SEEDS = Number(process.env.LIVE_VIEW_SEEDS ?? 300)
// LIVE_VIEW_TRACE=<seed>: that seed alone, every step logged.
const TRACE = Number(process.env.LIVE_VIEW_TRACE ?? 0)

// A tiny seeded generator, so a failing seed can be replayed.
function random(seed: number) {
  let a = seed >>> 0
  return () => {
    a = (a + 0x6d2b79f5) >>> 0
    let t = a
    t = Math.imul(t ^ (t >>> 15), t | 1)
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61)
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296
  }
}

// The event stream, as the browser's EventSource hands it to the live store.
class FakeSource {
  readyState = 1
  onerror: ((this: EventSource, ev: Event) => unknown) | null = null
  private listeners = new Map<string, ((event: MessageEvent) => void)[]>()
  addEventListener(type: string, listener: (event: MessageEvent) => void) { this.listeners.set(type, [...(this.listeners.get(type) ?? []), listener]) }
  close() { this.readyState = 2 }
  emit(type: string, data: unknown, id: number) {
    for (const listener of this.listeners.get(type) ?? []) listener({ data: JSON.stringify(data), lastEventId: String(id) } as MessageEvent)
  }
}

interface ServerNode { id: string; key: string; title: string; notes: string; priority: string; state: string; parent: string; rev: number; deleted: boolean }
type Kind = 'created' | 'updated' | 'deleted'
interface StreamEvent { id: number; type: string; actor: string; changes: { id: string; change: Kind; fields: string[]; revision: number }[] }
// carries: the copies it holds; gone: nodes it found deleted (at the server's revision then).
interface Answer { status: number; json?: unknown; headers?: Record<string, string>; carries: [string, number][]; gone?: [string, number][]; own?: { id: string; rev: number; kind: Kind }[] }
interface Call {
  method: string; path: string; query: URLSearchParams; body: Record<string, unknown> | undefined; headers: Record<string, string>
  // Gaps before it was sent: an answer is current only while no gap followed.
  epoch: number; answer?: Answer; delivered?: boolean; resolve: (response: Response) => void
  // A save from the open editor, and the fields it meant to change.
  editor?: boolean; changed?: string[]
  // An answer that tends to arrive late (a race the test sets up).
  slow?: boolean
}

let fetchNow: (url: string, init?: RequestInit) => Promise<Response> = () => Promise.reject(new Error('no server'))
beforeAll(() => { vi.stubGlobal('fetch', (url: string, init?: RequestInit) => fetchNow(url, init)) })
afterAll(() => { vi.unstubAllGlobals() })

async function settle() {
  for (let round = 0; round < 4; round++) {
    for (let i = 0; i < 25; i++) await Promise.resolve()
    await nextTick()
  }
}

async function run(seed: number, steps = 70): Promise<string[]> {
  const next = random(seed)
  const pick = <T>(list: readonly T[]): T | undefined => list.length ? list[Math.floor(next() * list.length)] : undefined
  const failures: string[] = []
  let where = 'setup'
  const fail = (text: string) => { if (failures.length < 4) failures.push(`${where}: ${text}`); if (trace) console.log(`FAIL ${where}: ${text}`) }
  const trace = seed === TRACE
  const log = (text: string) => { if (trace) console.log(text) }

  // ---------- The server ----------
  let clock = 10
  const server = new Map<string, ServerNode>(IDS.map((id, i) => [id, { id, key: `PRJ-${i + 1}`, title: `${id} r${i + 1}`, notes: 'first', priority: 'low', state: 'new', parent: PROJECT, rev: i + 1, deleted: false }]))
  const chip = (parent: string): ListParent => parent === PROJECT ? { id: PROJECT, key: 'PRJ', title: 'Project', kind_slug: 'project' } : { ...EPICS.find(e => e.id === parent)!, kind_slug: 'epic' }
  const epicOf = (parent: string) => parent === PROJECT ? null : { ...EPICS.find(e => e.id === parent)! }
  const work = (n: ServerNode): WorkNode => ({ id: n.id, key: n.key, kind_id: 'k-ticket', title: n.title, body: '', fields: { notes: n.notes, priority: n.priority }, state: n.state, parent_id: n.parent, position: '0', created_at: at(0), updated_at: at(n.rev), deleted_at: null })
  const listed = (n: ServerNode): ListItem => ({ ...work(n), kind_slug: 'ticket', kind_label: 'Ticket', priority: n.priority, assignee: null, parent: chip(n.parent), epic: epicOf(n.parent), children_count: 0, project: { id: PROJECT, key: 'PRJ', title: 'Project' } })

  // The stream: connected after its first ready; events before that are never seen.
  const source = new FakeSource()
  let eventSeq = 500, streamAt = 500, connected = false
  let queued: StreamEvent[] = []
  const emit = (type: string, actor: string, changes: StreamEvent['changes']) => { const event = { id: ++eventSeq, type, actor, changes }; if (connected) queued.push(event) }

  // ---------- What the tab has been told (to judge the views by) ----------
  const known = new Map<string, { rev: number; kind: Kind }>()
  const deletedAt = new Map<string, number>()
  const learn = (id: string, rev: number, kind: Kind) => {
    if (rev >= (known.get(id)?.rev ?? 0)) known.set(id, { rev, kind })
    if (kind === 'deleted') deletedAt.set(id, Math.max(deletedAt.get(id) ?? 0, rev))
  }
  // A 404 (or a node an ids query no longer returns) says gone, but not since which revision.
  const learnGone = (id: string) => { const told = known.get(id); known.set(id, { rev: told?.rev ?? 0, kind: 'deleted' }) }
  // Copies that were at least as new as anything the tab knew when they arrived.
  const justified = new Set<string>()
  // The newest revision each node was read at since the last gap (a row a
  // list adds must be confirmed to exist since then).
  const readIn = new Map<string, { epoch: number; rev: number }>()
  // This editor's own saves (they may show under its pin), and a 412 being rebased.
  const editorSaves = new Set<string>()
  // A 412 on the editor's save: open until the save settles and the views were looked at.
  let conflict: 'open' | 'closing' | null = null
  let epoch = 0

  // ---------- Requests ----------
  const calls: Call[] = []
  fetchNow = (url, init = {}) => {
    const parsed = new URL(url, 'http://aeon.test')
    let resolve!: (response: Response) => void
    const promise = new Promise<Response>(done => { resolve = done })
    const headers = Object.fromEntries(Object.entries((init.headers ?? {}) as Record<string, string>).map(([key, value]) => [key.toLowerCase(), String(value)]))
    calls.push({ method: (init.method ?? 'GET').toUpperCase(), path: parsed.pathname, query: parsed.searchParams, body: typeof init.body === 'string' ? JSON.parse(init.body) : undefined, headers, epoch, resolve })
    return promise
  }
  const respond = (answer: Answer) => ({
    ok: answer.status >= 200 && answer.status < 300, status: answer.status, headers: new Headers(answer.headers ?? {}),
    json: async () => answer.json,
  }) as unknown as Response

  // The server handles a request (a write applies now); the answer travels until delivered.
  function process(call: Call) {
    const { method, path, query } = call
    log(`  server ${method} ${path}?${query} (epoch ${call.epoch})`)
    const ok = (json: unknown, carries: [string, number][] = [], own?: Answer['own']): Answer => ({ status: 200, json, carries, own })
    const missing: Answer = { status: 404, json: { error: 'Not found' }, carries: [] }
    if (path === '/api/nodes' && method === 'GET') {
      const ids = query.get('ids')?.split(',').filter(Boolean)
      let rows = [...server.values()].filter(n => !n.deleted && (!ids || ids.includes(n.id)) && (!query.get('parent_id') || n.parent === query.get('parent_id')))
      rows.sort((a, b) => b.rev - a.rev || (a.id < b.id ? 1 : -1))
      const cursor = query.get('cursor')
      if (cursor) { const [rev, id] = cursor.split(':'); rows = rows.filter(n => n.rev < Number(rev) || (n.rev === Number(rev) && n.id < id)) }
      const limit = Number(query.get('limit') ?? 50)
      const page = rows.slice(0, limit)
      const last = page[page.length - 1]
      call.answer = ok({ items: page.map(listed), next_cursor: rows.length > limit ? `${last.rev}:${last.id}` : null, facets: {} }, page.map(n => [n.id, n.rev]))
      // The query names these nodes and applies no filter they could fail: absent means deleted.
      call.answer.gone = (ids ?? []).filter(id => server.get(id)?.deleted).map(id => [id, server.get(id)!.rev])
    } else if (path === '/api/nodes/lookup') {
      const ids = query.get('ids')?.split(',') ?? []
      call.answer = ok({ items: [...EPICS.filter(e => ids.includes(e.id)).map(e => ({ ...e, state: 'new' })), ...[...server.values()].filter(n => ids.includes(n.id)).map(n => ({ id: n.id, key: n.key, title: n.title, state: n.state }))] })
    } else if (path === '/api/relations') {
      call.answer = ok({ items: [], next_cursor: null })
    } else if (path === '/api/nodes/bulk' && method === 'POST') {
      const body = call.body as { ids: string[]; parent_id?: string; if_unmodified_since?: Record<string, string> }
      const items: WorkNode[] = [], skipped: { id: string; reason: string; code?: string }[] = [], unchanged: string[] = [], changes: StreamEvent['changes'] = []
      for (const id of body.ids) {
        const n = server.get(id)
        if (!n || n.deleted) skipped.push({ id, reason: 'not found' })
        else if (body.if_unmodified_since?.[id] && body.if_unmodified_since[id] !== at(n.rev)) skipped.push({ id, reason: 'changed elsewhere', code: 'conflict' })
        else if (!body.parent_id || n.parent === body.parent_id) unchanged.push(id)
        else {
          n.parent = body.parent_id; n.rev = ++clock
          items.push(work(n)); changes.push({ id, change: 'updated', fields: ['parent_id'], revision: n.rev })
        }
      }
      if (changes.length) emit('node.bulk_changed', ME, changes)
      call.answer = ok({ event_id: changes.length ? eventSeq : null, items, unchanged, skipped }, items.map(n => [n.id, revOf(n.updated_at)]), items.map(n => ({ id: n.id, rev: revOf(n.updated_at), kind: 'updated' as Kind })))
    } else {
      const match = /^\/api\/nodes\/([^/]+)(\/move)?$/.exec(path)
      const n = match ? server.get(decodeURIComponent(match[1])) : undefined
      if (!n || n.deleted) { call.answer = { ...missing, gone: n ? [[n.id, n.rev]] : [] }; return }
      const since = call.headers['if-unmodified-since']
      if (method === 'GET') call.answer = ok(work(n), [[n.id, n.rev]])
      else if (method === 'DELETE') {
        n.deleted = true; n.rev = ++clock
        emit('node.deleted', ME, [{ id: n.id, change: 'deleted', fields: ['deleted_at'], revision: n.rev }])
        call.answer = { status: 204, headers: { 'aeon-revision': at(n.rev) }, carries: [], own: [{ id: n.id, rev: n.rev, kind: 'deleted' }] }
      } else if (since && since !== at(n.rev)) {
        call.answer = { status: 412, json: { error: 'node has changed', ...(match![2] ? { node: work(n) } : {}) }, carries: match![2] ? [[n.id, n.rev]] : [] }
      } else if (match![2]) {
        n.parent = String(call.body?.parent_id); n.rev = ++clock
        emit('node.moved', ME, [{ id: n.id, change: 'updated', fields: ['parent_id'], revision: n.rev }])
        call.answer = ok(work(n), [[n.id, n.rev]], [{ id: n.id, rev: n.rev, kind: 'updated' }])
      } else if (method === 'PATCH') {
        const body = call.body as { title?: string; state?: string; fields?: Record<string, string> }
        // A save builds on one copy: a field it did not mean to change goes back as the server has it.
        if (body.fields && call.changed) {
          for (const key of ['notes', 'priority'] as const) if (!call.changed.includes(key) && body.fields[key] !== n[key]) fail(`lost update: ${n.id}.${key} saved as ${body.fields[key]} over ${n[key]}`)
        }
        const fields: string[] = []
        if (body.title !== undefined && body.title !== n.title) { n.title = body.title; fields.push('title') }
        if (body.state !== undefined && body.state !== n.state) { n.state = body.state; fields.push('state') }
        if (body.fields) for (const key of ['notes', 'priority'] as const) if ((body.fields[key] ?? '') !== n[key]) { n[key] = body.fields[key] ?? ''; fields.push(`fields.${key}`) }
        n.rev = ++clock
        emit('node.updated', ME, [{ id: n.id, change: 'updated', fields, revision: n.rev }])
        call.answer = ok(work(n), [[n.id, n.rev]], [{ id: n.id, rev: n.rev, kind: 'updated' }])
      } else call.answer = { status: 405, json: { error: 'no' }, carries: [] }
    }
  }
  function deliver(call: Call) {
    call.delivered = true
    const answer = call.answer!
    log(`  deliver${call.editor ? ' (editor)' : ''} ${call.method} ${call.path}?${call.query} → ${answer.status} ${answer.carries.map(([id, rev]) => `${id}@${rev}`).join(' ')}${answer.own ? ' own ' + answer.own.map(o => `${o.id}@${o.rev}:${o.kind}`).join(' ') : ''}`)
    for (const [id, rev] of answer.carries) {
      if (rev >= (known.get(id)?.rev ?? 0)) justified.add(`${id}@${rev}`)
      const read = readIn.get(id)
      if (call.epoch === epoch) readIn.set(id, { epoch, rev: read?.epoch === epoch ? Math.max(read.rev, rev) : rev })
    }
    // Every answer read since the last gap is news the tab takes: a copy says
    // the node is there at its revision, a 404 that it is gone. (One read
    // before a gap is doubtful: the tab reads again.)
    if (call.epoch === epoch) {
      for (const [id, rev] of answer.carries) learn(id, rev, 'updated')
      for (const [id] of answer.gone ?? []) learnGone(id)
    }
    for (const own of answer.own ?? []) {
      learn(own.id, own.rev, own.kind)
      if (call.editor) editorSaves.add(`${own.id}@${own.rev}`)
    }
    if (call.editor && answer.status === 412) conflict = 'open'
    call.resolve(respond(answer))
  }

  // ---------- Someone else ----------
  function remote(on?: ServerNode) {
    const deleted = [...server.values()].filter(n => n.deleted)
    const n = on ?? (deleted.length && next() < 0.5 ? pick(deleted)! : server.get(pick(IDS)!)!)
    const changes = (change: Kind, fields: string[]) => [{ id: n.id, change, fields, revision: n.rev }]
    if (n.deleted) { n.deleted = false; n.rev = ++clock; emit('node.created', MIRA, changes('created', [])); return }
    const roll = next()
    if (roll < 0.2) { n.deleted = true; n.rev = ++clock; emit('node.deleted', MIRA, changes('deleted', ['deleted_at'])) }
    else if (roll < 0.5) { n.parent = pick(PARENTS.filter(p => p !== n.parent))!; n.rev = ++clock; emit('node.moved', MIRA, changes('updated', ['parent_id'])) }
    else if (roll < 0.75) { n.rev = ++clock; n.title = `${n.id} by mira r${n.rev}`; emit('node.updated', MIRA, changes('updated', ['title'])) }
    else { n.rev = ++clock; n.notes = `mira r${n.rev}`; emit('node.updated', MIRA, changes('updated', ['fields.notes'])) }
  }
  function deliverEvent() {
    const event = queued.shift()
    if (!event) return
    for (const change of event.changes) learn(change.id, change.revision, change.change)
    log(`  event ${event.type} by ${event.actor}: ${event.changes.map(c => `${c.id}@${c.revision}:${c.change}`).join(' ')}`)
    streamAt = event.id
    source.emit(event.type, {
      id: event.id, type: event.type, actor_principal_id: event.actor,
      node_changes: event.changes.map(change => ({ id: change.id, project_id: PROJECT, change: change.change, fields: change.fields, revision: at(change.revision) })),
    }, event.id)
  }
  // The stream could not bridge a gap (or connects for the first time): what it missed is lost.
  function gap() {
    queued = []
    epoch++
    connected = true
    streamAt = eventSeq
    source.emit('stream.ready', { after: eventSeq, resumed: false }, eventSeq)
  }
  function resume() { if (connected) source.emit('stream.ready', { after: streamAt, resumed: true }, streamAt) }

  // ---------- The views, as ProjectView and TicketWorkspace wire them ----------
  rowStore.clear()
  let now = 0
  const store = new LiveNodeStore({ open: () => source, rows: rowStore })
  const filters = ref(filtersFromQuery({}))
  const busy = ref(false)
  const given = ref<ListItem | null>(null)
  const scope = effectScope()
  function removed(item: ListItem) {
    const told = known.get(item.id)
    if (told?.kind !== 'deleted') fail(`the panel removed ${item.id}, which the tab knows ${told?.kind ?? 'unchanged'} at r${told?.rev ?? '?'}`)
    list.removeRow(item.id)
    given.value = null
    busy.value = false
  }
  const { list, live, ticket } = scope.run(() => {
    const list = useTicketList(ref(PROJECT), filters)
    list.epics.value = EPICS.map(epic => ({ ...epic, state: 'new' }))
    const live = useLiveList({
      projectId: ref(PROJECT), filters, rows: list.rows, loading: list.loading, reads: list.reads, loadedOnce: list.loadedOnce,
      more: () => !!list.cursor.value, edge: () => list.edge.value, active: ref(true), me: () => ME,
      quiet: id => given.value?.id === id, holds: id => given.value?.id === id && busy.value,
      blockers: () => ({ selected: 0, editing: busy.value, menuOpen: false, dialogOpen: false, dragging: false }),
      reload: () => void list.load({ pageSize: PAGE }), store, env: { now: () => now, hidden: () => false, listen: () => () => {} },
    })
    const item = computed(() => { const it = given.value; return it ? rowStore.row(it.id) ?? rowStore.adopt(it, 0, { full: false }) ?? it : null })
    const ticket = useTicket(item, { names: new Map(), onRemoved: removed, onCreated: () => {}, onMoved: () => {}, live: { busy, me: () => ME, store } })
    return { list, live, ticket }
  })!

  // ---------- Looking at the views ----------
  const shownAt = new Map<string, number>()
  // The parent each row showed and the chip it named for it.
  const placedAt = new Map<string, { parent: string | null; chip: string | null }>()
  let inList = new Set<string>()
  let lastRead = list.reads.value
  let pinnedBefore: string | null = null
  function observe() {
    const editor = busy.value && given.value ? given.value.id : null
    log(`    list ${list.rows.value.map(r => `${r.id}@${revOf(r.updated_at)}`).join(' ')} | panel ${given.value?.id ?? '-'}${busy.value ? ' editing' : ''}${ticket.gone.value ? ' gone' : ''} | pill ${live.pill.value ?? '-'} ${[...live.pending.ids()].map(id => `${id}:${live.pending.kind(id)}`).join(' ')}${conflict ? ` conflict ${conflict}` : ''}${saving ? ' saving' : ''} | server ${[...server.values()].map(n => `${n.id}@${n.rev}${n.deleted ? 'x' : ''}:${n.parent}`).join(' ')}`)
    for (const id of IDS) {
      const row = rowStore.row(id)
      if (!row) continue
      // A chip that named the row's parent never names another while the parent stays.
      const placed = { parent: row.parent_id, chip: row.parent?.id ?? null }, placedBefore = placedAt.get(id)
      if (placedBefore && placedBefore.chip === placedBefore.parent && placed.parent === placedBefore.parent && placed.chip !== placed.parent) fail(`${id} under ${placed.parent} now shows the parent chip ${placed.chip}`)
      placedAt.set(id, placed)
      const rev = revOf(row.updated_at)
      const before = shownAt.get(id)
      if (before === rev) continue
      shownAt.set(id, rev)
      const key = `${id}@${rev}`
      // The editor's own save shows under its pin (it may have closed in the same step).
      const ownSave = (editor === id || pinnedBefore === id) && editorSaves.has(key)
      if (before !== undefined && rev < before) fail(`${id} went back from r${before} to r${rev}`)
      if (rev <= (deletedAt.get(id) ?? 0)) fail(`${id} shows r${rev}, at or before its deletion at r${deletedAt.get(id)}`)
      if (before !== undefined && !justified.has(key) && !ownSave) fail(`${id} shows r${rev}, older than the tab knew when that copy arrived`)
      if (before !== undefined && editor === id && pinnedBefore === id && !ownSave && !conflict) fail(`${id} moved under the open editor from r${before} to r${rev}`)
    }
    const rows = list.rows.value
    const ids = new Set(rows.map(row => row.id))
    const loaded = list.reads.value !== lastRead && list.reads.value?.kind === 'load'
    lastRead = list.reads.value
    for (const row of rows) {
      if (inList.has(row.id)) continue
      const key = `${row.id}@${revOf(row.updated_at)}`
      const read = readIn.get(row.id)
      if (!rowStore.pinned(row.id) && read?.epoch !== epoch) fail(`${key} joined the list, but was not read since the last gap`)
      if (!rowStore.pinned(row.id) && revOf(row.updated_at) <= (deletedAt.get(row.id) ?? 0)) fail(`${key} joined the list, at or before its deletion at r${deletedAt.get(row.id)}`)
    }
    for (const id of inList) {
      if (!ids.has(id) && !loaded && known.get(id)?.kind !== 'deleted') fail(`${id} left the list, but the tab knows it ${known.get(id)?.kind ?? 'unchanged'}`)
    }
    inList = ids
    pinnedBefore = editor
    if (conflict === 'closing') conflict = null
  }

  // ---------- The person ----------
  let saving = false
  const unprocessed = () => calls.filter(call => !call.answer)
  const undelivered = () => calls.filter(call => call.answer && !call.delivered)
  async function advance(ms: number) { now += ms; await vi.advanceTimersByTimeAsync(ms) }
  function openOrClose() {
    busy.value = false
    if (saving) return
    given.value = next() < 0.8 ? pick(list.rows.value) ?? null : null
  }
  function editOrCancel() {
    if (!given.value || ticket.gone.value) return
    if (!busy.value) { busy.value = true; ticket.hold() }
    else if (!saving) busy.value = false
  }
  function save() {
    if (!busy.value || saving || !given.value || ticket.gone.value) return
    const title = next() < 0.5
    const count = calls.length
    saving = true
    const done = ticket.patch(title ? { title: `mine s${clock}` } : { fields: { priority: pick(['high', 'medium', 'low'])! } })
    const call = calls[count]
    if (call?.method === 'PATCH') Object.assign(call, { editor: true, changed: title ? ['title'] : ['priority'] })
    void done.then(result => {
      saving = false
      if (conflict) conflict = 'closing'
      if (result === 'error' || (result === 'ok' && next() < 0.6)) busy.value = false
    })
  }
  function move() {
    if (given.value && !busy.value && !ticket.gone.value && next() < 0.5) { void ticket.moveTo(pick(EPICS)!); return }
    const row = pick(list.rows.value)
    if (row) void guardedMove(row, chip(pick(PARENTS.filter(p => p !== row.parent_id))!), undefined, rowStore)
  }
  function remove() { if (given.value && !busy.value && !ticket.gone.value) void ticket.remove() }
  function bulk() {
    const ids = list.rows.value.filter(() => next() < 0.5).map(row => row.id)
    if (ids.length) void list.applyBulk({ ids, parent_id: pick(PARENTS)! }).catch(() => {})
  }
  function status() { const row = pick(list.rows.value); if (row) void list.setStatus(row, row.state === 'new' ? 'in_progress' : 'new') }
  // A race: this tab's write lands on the server at once, someone else
  // changes the same node right after, and the answer tends to come late.
  function race() {
    const row = pick(list.rows.value)
    if (!row || saving) return
    const count = calls.length
    const kind = pick(['move', 'delete', 'status'] as const)!
    if (kind === 'move') void guardedMove(row, chip(pick(PARENTS.filter(p => p !== row.parent_id))!), undefined, rowStore)
    else if (kind === 'status') void list.setStatus(row, row.state === 'new' ? 'in_progress' : 'new')
    else { busy.value = false; given.value = row; void ticket.remove() }
    const call = calls.slice(count).find(c => c.method !== 'GET')
    if (!call) return
    process(call)
    call.slow = true
    const n = server.get(row.id)!
    if (n.deleted && next() < 0.7) remote(n)
    else if (!n.deleted) { n.parent = pick(PARENTS.filter(p => p !== n.parent))!; n.rev = ++clock; emit('node.moved', MIRA, [{ id: n.id, change: 'updated', fields: ['parent_id'], revision: n.rev }]) }
  }

  const ops: [number, string, () => unknown][] = [
    [10, 'remote', remote],
    [15, 'process', () => { const call = pick(unprocessed()); if (call) process(call) }],
    [15, 'deliver', () => { const all = undelivered(), ready = all.filter(call => !call.slow); const call = ready.length && next() < 0.85 ? pick(ready) : pick(all); if (call) deliver(call) }],
    [12, 'event', deliverEvent],
    [3, 'gap', gap],
    [2, 'resume', resume],
    [4, 'load', () => void list.load({ pageSize: PAGE })],
    [4, 'more', () => { if (list.cursor.value && !list.loading.value) void list.loadMore() }],
    [4, 'panel', openOrClose],
    [5, 'edit', editOrCancel],
    [5, 'save', save],
    [4, 'move', move],
    [3, 'delete', remove],
    [2, 'bulk', bulk],
    [2, 'status', status],
    [3, 'race', race],
    [4, 'show', () => live.apply()],
    [5, 'wait', () => advance(pick([50, 150, 500, 1500, 2500])!)],
  ]
  const total = ops.reduce((sum, [weight]) => sum + weight, 0)

  vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'setInterval', 'clearInterval'] })
  try {
    // The page opens: the list loads while the stream connects.
    void list.load({ pageSize: PAGE })
    await settle()
    for (let step = 0; step < steps; step++) {
      let roll = next() * total
      const [, name, op] = ops.find(([weight]) => (roll -= weight) < 0) ?? ops[ops.length - 1]
      where = `step ${step} ${name}`
      log(where)
      await op()
      await settle()
      observe()
    }

    // Everything on its way arrives: the server answers, the stream catches up.
    where = 'drain'
    if (!connected) gap()
    if (!saving) busy.value = false
    const drain = async () => {
      for (let round = 0, quiet = 0; round < 200 && quiet < 3; round++) {
        let moved = false
        for (const call of unprocessed()) { process(call); moved = true }
        for (const call of undelivered()) { deliver(call); await settle(); observe(); moved = true }
        while (queued.length) { deliverEvent(); await settle(); observe(); moved = true }
        if (!saving) busy.value = false
        await advance(2500); await settle(); observe()
        quiet = moved ? 0 : quiet + 1
      }
    }
    await drain()
    live.apply(); await settle(); observe()
    await drain()
    const all = list.loadAll()
    await drain(); await all
    live.apply(); await settle(); observe()
    await drain()
    live.apply(); await settle(); observe()

    // The views end where the server is.
    where = 'end'
    const alive = [...server.values()].filter(n => !n.deleted)
    const shown = list.rows.value
    const names = (list: { id: string }[]) => list.map(n => n.id).sort().join(',')
    if (names(shown) !== names(alive)) fail(`the list shows ${names(shown)}, the server has ${names(alive)}`)
    for (const row of shown) {
      const n = server.get(row.id)!
      if (n.deleted) continue
      if (row.updated_at !== at(n.rev) || row.title !== n.title || row.state !== n.state || row.parent_id !== n.parent || row.fields.notes !== n.notes || row.fields.priority !== n.priority) {
        fail(`${row.id} shows r${revOf(row.updated_at)} ${row.title}/${row.state}/${row.parent_id}, the server has r${n.rev} ${n.title}/${n.state}/${n.parent}`)
      }
      if ((row.parent?.id ?? null) !== row.parent_id) fail(`${row.id} under ${row.parent_id} shows the parent chip ${row.parent?.id}`)
      if ((row.epic?.id ?? null) !== (n.parent === PROJECT ? null : n.parent)) fail(`${row.id} under ${n.parent} is grouped under the epic ${row.epic?.id ?? 'none'}`)
    }
    if (live.pending.count) fail(`${live.pending.count} updates still wait after Show`)
    const open = given.value
    if (open && !saving) {
      const n = server.get(open.id)!
      if (ticket.gone.value !== n.deleted) fail(`the panel shows ${open.id} ${ticket.gone.value ? 'gone' : 'here'}, the server has it ${n.deleted ? 'deleted' : 'alive'}`)
      const row = rowStore.row(open.id)
      if (!n.deleted && row?.updated_at !== at(n.rev)) fail(`the panel shows ${open.id} at r${row && revOf(row.updated_at)}, the server has r${n.rev}`)
    }
  } finally {
    scope.stop()
    vi.clearAllTimers()
    vi.useRealTimers()
    rowStore.clear()
  }
  return failures
}

describe('the list, the live list and the panel over any delivery order', () => {
  it(`show only what the tab may trust, and end at the server's state (${SEEDS} seeds)`, async () => {
    const failed: string[] = []
    for (let seed = TRACE || 1; seed <= (TRACE || SEEDS); seed++) {
      const failures = await run(seed)
      if (failures.length) failed.push(`seed ${seed}: ${failures.join('; ')}`)
    }
    if (failed.length) console.log(`LIVE-VIEWS ${failed.length}/${SEEDS} seeds failed\n${failed.slice(0, 12).join('\n')}`)
    expect(failed).toEqual([])
  }, 600_000)
})

// AEON-385: the live Outline shares the actual stream and row store with
// the panel. Delayed lazy pages and live batches must agree, with structural
// placement held until Show. Local callbacks count each child once.
async function runOutline(seed: number): Promise<string[]> {
  vi.useFakeTimers()
  const next = random(seed)
  const pick = <T>(items: T[]) => items[Math.floor(next() * items.length)]!
  const root = `outline-${seed}` // expansion memory belongs to one project
  const failures: string[] = []
  let phase = 'initial', epoch = 0, revision = 20, sequence = 500
  const fail = (message: string) => { if (failures.length < 6) failures.push(`${phase}: ${message}`) }
  const log = (message: string) => { if (seed === TRACE) console.log(`OUTLINE ${phase}: ${message}`) }
  const data = new Map<string, ListItem>()
  const parent = (id: string): ListParent => ({ id, key: id === root ? 'PRJ' : `PRJ-${id}`, title: id, kind_slug: id === root ? 'project' : 'epic' })
  function make(id: string, under: string, kind = 'ticket'): ListItem {
    return { id, key: `PRJ-${id}`, title: id, body: '', fields: {}, state: 'new', kind_id: `k-${kind}`, kind_slug: kind, kind_label: kind,
      parent_id: under, parent: parent(under), epic: under === root ? null : parent(under), position: '0', created_at: at(0), updated_at: at(++revision),
      deleted_at: null, priority: null, assignee: null, children_count: 0, project: { id: root, key: 'PRJ', title: root } }
  }
  for (const id of ['a', 'b']) data.set(id, make(id, root, 'epic'))
  for (let i = 0; i < 6; i++) data.set(`child-${i}`, make(`child-${i}`, i < 3 ? root : i < 5 ? 'a' : 'b'))
  const count = (id: string) => [...data.values()].filter(n => n.parent_id === id).length
  const copy = (n: ListItem) => structuredClone({ ...n, children_count: count(n.id) })
  const nodeCopy = (n: ListItem): WorkNode => ({ id: n.id, key: n.key, title: n.title, body: n.body, kind_id: n.kind_id,
    state: n.state, fields: structuredClone(n.fields), parent_id: n.parent_id, position: n.position, created_at: n.created_at, updated_at: n.updated_at, deleted_at: n.deleted_at })
  const source = new FakeSource()
  rowStore.clear()
  const store = new LiveNodeStore({ open: () => source, rows: rowStore, graceMs: 0 })
  const off = store.subscribe({ shows: () => false, changed: () => {} })
  const gap = () => { epoch++; source.emit('stream.ready', { after: ++sequence, resumed: false }, sequence) }
  const resume = () => source.emit('stream.ready', { after: sequence, resumed: true }, sequence)
  const loss = () => { if (store.state !== 'reconnecting') epoch++; source.onerror?.call(source as unknown as EventSource, new Event('error')) }
  const floors = new Map<string, number>()
  function event(id: string, change: Kind, rev: number, fields: string[] = []) {
    if (change === 'deleted') floors.set(id, rev)
    source.emit(change === 'deleted' ? 'node.deleted' : 'node.created', {
      id: ++sequence, type: change === 'deleted' ? 'node.deleted' : 'node.created', actor_principal_id: MIRA,
      node_changes: [{ id, project_id: root, change, fields, revision: at(rev) }],
    }, sequence)
  }
  interface Read { method: string; url: URL; body?: Record<string, string>; epoch: number; answer?: { status: number; json: unknown }; resolve: (r: Response) => void; done?: boolean }
  const calls: Read[] = []
  const readIn = new Map<string, number>()
  const own = new Set<string>()
  fetchNow = (url, init = {}) => new Promise(resolve => {
    calls.push({ method: init.method ?? 'GET', url: new URL(url, 'http://aeon.test'), body: init.body ? JSON.parse(String(init.body)) : undefined, epoch, resolve })
  })
  function process(call: Read) {
    const { pathname: path, searchParams: q } = call.url
    log(`process ${call.method} ${path}?${q} sent in epoch ${call.epoch}`)
    let json: unknown, status = 200
    if (path === '/api/nodes' && call.method === 'GET') {
      // Small server pages exercise root and child pagination independently
      // of the production page-size constant.
      const kinds = q.get('kind')?.split(',')
      const ids = q.get('ids')?.split(',')
      const rows = [...data.values()].filter(n => (!q.has('parent_id') || n.parent_id === q.get('parent_id')) && (!ids || ids.includes(n.id)) && (!kinds || kinds.includes(n.kind_slug)) && (!q.has('q') || n.title.includes(q.get('q')!)) && (q.get('hide_closed') !== 'true' || n.state !== 'done')).sort(compareRows(effectiveSort(filtersFromQuery({ sort: q.get('sort') }))))
      const start = Number(q.get('cursor') ?? 0), limit = q.has('parent_id') ? Math.min(2, Number(q.get('limit') ?? 2)) : Number(q.get('limit') ?? 200)
      json = { items: rows.slice(start, start + limit).map(copy), next_cursor: start + limit < rows.length ? String(start + limit) : null, facets: {} }
    } else if (path === '/api/nodes/bulk') {
      const change = call.body as unknown as BulkChange
      const items = change.ids.map(id => {
        const n = data.get(id)!
        n.parent_id = change.parent_id!; n.parent = parent(n.parent_id); n.updated_at = at(++revision); own.add(id)
        return nodeCopy(n)
      })
      json = { items, unchanged: [], skipped: [], event_id: ++sequence }
    } else if (path === '/api/kinds') {
      json = { items: ['ticket', 'epic', 'task'].map(slug => ({ id: `k-${slug}`, slug, label: slug, schema: {} })) }
    } else if (path === '/api/nodes' && call.method === 'POST') {
      const n = make(`new-${++revision}`, call.body!.parent_id)
      n.title = call.body!.title
      data.set(n.id, n); own.add(n.id); json = nodeCopy(n)
    } else if (path.endsWith('/move')) {
      const n = data.get(path.split('/')[3])!
      n.parent_id = call.body!.parent_id; n.parent = parent(n.parent_id); n.epic = n.parent_id === root ? null : n.parent
      n.updated_at = at(++revision); own.add(n.id); json = nodeCopy(n)
    } else if (path === '/api/relations' || path === '/api/nodes/lookup') json = { items: [] }
    else {
      const n = data.get(path.split('/')[3])
      if (n) json = nodeCopy(n)
      else { status = 404; json = { error: 'Not found' } }
    }
    call.answer = { status, json }
  }
  function deliver(call: Read) {
    call.done = true
    log(`deliver ${call.method} ${call.url.pathname}?${call.url.searchParams} in epoch ${epoch}`)
    const { status, json } = call.answer!
    const body = json as { items?: ListItem[]; id?: string }
    if (call.epoch === epoch) for (const n of body.items ?? (body.id ? [body as ListItem] : [])) readIn.set(n.id, epoch)
    call.resolve({ ok: status < 400, status, headers: new Headers(), json: async () => json } as Response)
  }
  const scope = effectScope(), selected = ref<ListItem | null>(null), outlineBusy = ref(false)
  const outlineFilters = ref(filtersFromQuery({ closed: '1' }))
  const { outline, ticket, list } = scope.run(() => {
    const list = useTicketList(ref(root), outlineFilters)
    const outline = useOutline(ref(root), outlineFilters, ref(true), list, { store, me: () => ME, blockers: () => ({ selected: 1, editing: false, menuOpen: false, dialogOpen: false, dragging: false }) })
    const ticket = useTicket(selected, { names: new Map(), onCreated: item => outline.insert(item), onRemoved: item => outline.remove(item),
      onMoved: (item, from) => outline.relocate(item, from, from === root ? null : from), live: { busy: outlineBusy, me: () => ME, store } })
    return { outline, ticket, list }
  })!
  let admitted = new Map<string, ListItem>()
  function observe() {
    const now = new Map<string, ListItem>()
    for (const id of new Set([...data.keys(), ...admitted.keys(), ...floors.keys()])) {
      const row = outline.node(id)
      if (!row) continue
      now.set(id, row)
      if (!admitted.has(id)) {
        if (readIn.get(id) !== epoch && !own.has(id)) fail(`${id} admitted without a read since gap ${epoch}`)
        if (revOf(row.updated_at) <= (floors.get(id) ?? 0)) fail(`${id} admitted at/below its deletion floor`)
      }
    }
    admitted = now
    const ids = outline.rows.value.map(n => n.id)
    log(`visible ${ids.join(',')}; counts ${['a', 'b'].map(id => `${id}=${outline.node(id)?.children_count}`).join(',')}`)
    if (new Set(ids).size !== ids.length) fail('duplicate displayed row')
  }
  // Process and deliver are separate, and random across all outstanding
  // root, child, panel and stats reads. Every delivery is observed.
  let flushing = false
  async function drain() {
    for (let i = 0; i < 400; i++) {
      await settle()
      let pending = calls.filter(c => !c.done)
      if (!pending.length && !flushing) {
        flushing = true
        void outline.live.flushNow().finally(() => { flushing = false })
        await settle()
        pending = calls.filter(c => !c.done)
      }
      if (!pending.length) { if (flushing) continue; return }
      const call = pick(pending)
      if (!call.answer) process(call)
      else deliver(call)
      await settle(); observe()
    }
    fail('requests did not settle')
  }
  async function reload() { outline.reload(); await settle(); observe(); await drain() }
  async function move(id: string, to: string) {
    const row = outline.node(id)
    if (!row) { fail(`${id} was not available for a local move`); return }
    const moving = guardedMove(row, parent(to), (item, from) => outline.relocate(item, from, from === root ? null : from))
    await drain(); await moving; observe()
  }
  function checkCounts() {
    for (const id of ['a', 'b']) {
      if (outline.node(id)?.children_count !== count(id)) fail(`${id} shows ${outline.node(id)?.children_count} children; server has ${count(id)}`)
      if (count(id) > 0 && !outline.hasChildren(id)) fail(`${id} has no expand control`)
    }
  }
  try {
    gap(); await drain()
    // A collapsed destination can start reading before or after the event.
    // Keep its answer separate from live reads, and reopen it while it waits.
    for (const timing of ['page before move', 'page after move']) {
      phase = timing
      outline.collapseAll(); await reload()
      const from = pick(['a', 'b'].filter(id => count(id) > 0)), to = from === 'a' ? 'b' : 'a'
      outline.setExpanded(from, true); await drain()
      const moving = pick([...data.values()].filter(n => n.parent_id === from && outline.node(n.id)))
      let held: Read | undefined
      async function expandDestination() {
        outline.setExpanded(to, true); await settle()
        held = calls.find(c => !c.done && c.url.searchParams.get('parent_id') === to)
        if (!held) { fail('destination page did not start'); return }
        process(held); held.done = true
        outline.setExpanded(to, false); outline.setExpanded(to, true); await settle()
      }
      if (timing === 'page before move') await expandDestination()
      moving.parent_id = to; moving.parent = parent(to); moving.updated_at = at(++revision)
      event(moving.id, 'updated', revision, ['parent_id']); await drain()
      if (outline.live.pending.kind(moving.id) !== 'moved') fail('move was not held')
      if (timing === 'page after move') await expandDestination()
      if (timing === 'page before move') { outline.live.apply(); await settle(); await drain() }
      if (held) { deliver(held); await settle(); observe(); await drain() }
      const entries = outline.entries.value.filter(e => e.type === 'row' && e.row.id === moving.id)
      const expected = timing === 'page before move' ? to : from
      if (entries.length !== 1 || entries[0]?.type !== 'row' || entries[0].tree.parentId !== expected) fail('late page lost or duplicated the held placement')
      outline.live.apply(); await settle(); await drain()
      const placed = outline.entries.value.filter(e => e.type === 'row' && e.row.id === moving.id)
      if (placed.length !== 1 || placed[0]?.type !== 'row' || placed[0].tree.parentId !== to) fail('Show failed to retain the moved row')
    }
    phase = 'lazy field patches stay ordered across Show'
    while (outline.hasMoreRoot.value) { outline.loadMoreRoot(); await drain() }
    const before = outline.rows.value.filter(n => n.parent_id === root && n.kind_slug === 'ticket').map(n => n.id)
    const patch = data.get(before[before.length - 1]!)!
    patch.title = 'patched in place'; patch.updated_at = at(++revision)
    event(patch.id, 'updated', revision, ['title']); await drain()
    const added = make('order-new', root); data.set(added.id, added)
    event(added.id, 'created', revision); await drain()
    outline.live.apply(); await settle(); await drain()
    if (outline.rows.value.filter(n => before.includes(n.id)).map(n => n.id).join() !== before.join()) fail('Show reordered lazy field-only patches')
    // Randomly interleave lazy loads, pagination, expansion and repeated gaps.
    // A deletion during the gap is deliberately not delivered as an event.
    for (let round = 0; round < 5; round++) {
      phase = `lazy ${round}`
      outline.collapseAll(); await reload()
      const under = pick([root, 'a', 'b'])
      if (under === root) { outline.reload(); await settle(); observe() }
      else { outline.setExpanded(under, true); await settle() }
      const pending = calls.filter(c => !c.done && c.url.searchParams.get('parent_id') === under && c.url.searchParams.get('kind') !== 'epic')
      if (pending.length) {
        const held = pick(pending)
        process(held)
        const items = (held.answer!.json as { items: ListItem[] }).items
        const victim = pick(items)
        if (victim) data.delete(victim.id)
        const missed = next() < 0.75
        if (missed) { if (next() < 0.5) loss(); else gap() } else resume()
        // Sometimes the deletion and restore are delivered, but the first
        // post-restore copy is still on its way: the floor must hide the old page.
        if (victim && !missed) {
          event(victim.id, 'deleted', ++revision)
          const restored = { ...victim, updated_at: at(++revision) }
          data.set(victim.id, restored); event(victim.id, 'created', revision)
        }
        deliver(held); await settle(); observe()
        if (next() < 0.5) { gap(); outline.toggle('a'); await settle(); observe() }
      }
      await drain()
      if (outline.hasMoreRoot.value) { outline.loadMoreRoot(); await drain() }
      if (next() < 0.5) { void outline.expandAll(); await drain() }
      else { outline.toggle(pick(['a', 'b'])); await drain() }
    }
    // Create through the actual panel, remote move without a callback, reload,
    // then move back through guardedMove + Outline's actual relocation callback.
    // Repeat in either direction so both retained true and false flags matter.
    phase = 'create / remote move / authoritative reload / local return'
    await reload()
    const a = pick(['a', 'b']), b = a === 'a' ? 'b' : 'a'
    selected.value = outline.node(a)!
    await drain()
    const creating = ticket.addChild(`seed ${seed}`, 'PRJ')
    await drain()
    const child = await creating
    if (!child) { fail('creation failed'); return failures }
    selected.value = null; await settle()
    checkCounts()
    for (let round = 0; round < 4; round++) {
      const n = data.get(child.id)!
      const from = n.parent_id!, to = from === a ? b : a
      n.parent_id = to; n.parent = parent(to); n.epic = n.parent; n.updated_at = at(++revision)
      if (next() < 0.5) gap(); else resume()
      outline.collapseAll(); await reload(); checkCounts()
      outline.setExpanded(to, true); await drain()
      for (let page = 0; page < 5 && !outline.node(child.id); page++) { const loading = outline.loadChildren(to, true); await drain(); await loading }
      await move(child.id, from); checkCounts()
      outline.setExpanded(from, true); await drain()
      for (let page = 0; page < 5 && !outline.rows.value.some(row => row.id === child.id); page++) {
        const loading = outline.loadChildren(from, true); await drain(); await loading
      }
      if (!outline.rows.value.some(row => row.id === child.id)) fail('the returned child is hidden')
      await move(child.id, to); checkCounts()
    }
    phase = 'final reload and expand'
    await reload(); checkCounts()
    void outline.expandAll(); await drain()
    while (outline.hasMoreRoot.value) { outline.loadMoreRoot(); await drain() }
    for (const id of ['a', 'b']) { const loading = outline.loadChildren(id, true); await drain(); await loading }
    if (outline.rows.value.map(n => n.id).sort().join() !== [...data.keys()].sort().join()) fail('fully loaded Outline differs from server')
    // A same-revision parent page is computed before a local move, but
    // arrives after it. Observe counts directly, before any correcting read.
    phase = 'stale count page crossing local move'
    const moving = [...data.values()].find(n => n.kind_slug === 'ticket' && outline.node(n.id))!
    const origin = moving.parent_id!, destination = origin === 'a' ? 'b' : 'a'
    gap(); await settle()
    const heldCount = calls.find(c => !c.done && c.url.searchParams.get('kind') === 'epic' && c.url.searchParams.get('parent_id') === root)
    if (!heldCount) fail('gap did not refresh loaded roots')
    else {
      process(heldCount)
      // Let all other reads finish without delivering this page.
      heldCount.done = true
      await move(moving.id, destination)
      deliver(heldCount); await settle()
      checkCounts()
      await drain()
      outline.live.apply(); await settle(); await drain()
    }
    phase = 'bulk moves use the displayed Outline revisions and relocate store rows'
    const bulkRows = outline.rows.value.filter(n => n.kind_slug === 'ticket').slice(0, 2)
    const from = new Map(bulkRows.map(n => [n.id, n.parent_id]))
    const to = pick(['a', 'b'])
    const bulk = list.applyBulk({ ids: bulkRows.map(n => n.id), parent_id: to }, bulkRows)
    await drain()
    for (const answer of (await bulk).items) {
      const row = rowStore.visibleRow(answer.id)
      if (row && row.parent_id !== from.get(row.id)) outline.relocate(row, from.get(row.id)!, null)
    }
    await drain(); observe(); checkCounts()
    for (const item of bulkRows) {
      const entry = outline.entries.value.find(e => e.type === 'row' && e.row.id === item.id)
      if (entry?.type !== 'row' || entry.tree.parentId !== to) fail('bulk move did not place the accepted row')
    }
    phase = 'live move under an open editor'
    const editing = pick([...data.values()].filter(n => n.kind_slug === 'ticket'))
    selected.value = outline.node(editing.id)!
    await drain()
    if (!selected.value) { fail('the editor row was missing'); return failures }
    outlineBusy.value = true; ticket.hold(); await settle()
    const base = selected.value.updated_at, parentBefore = selected.value.parent_id
    editing.parent_id = parentBefore === 'a' ? 'b' : 'a'
    editing.parent = parent(editing.parent_id); editing.updated_at = at(++revision)
    event(editing.id, 'updated', revision, ['parent_id'])
    await drain()
    if (selected.value.updated_at !== base || selected.value.parent_id !== parentBefore) fail('live update changed the editor base')
    outline.live.apply(); await settle(); await drain()
    if (selected.value.updated_at !== base) fail('Show changed the editor base')
    outlineBusy.value = false; await settle()
    outline.live.checkNow(); await settle(); await drain()
    const released = outline.entries.value.find(e => e.type === 'row' && e.row.id === editing.id)
    if (released?.type !== 'row' || released.tree.parentId !== editing.parent_id) fail('Show did not place the move after the editor released it')
    selected.value = null; await settle()
    // Convergence comes from the stream and Show, with no reload. Moves,
    // additions, deletes and restored rows keep their old placement until Show.
    for (let round = 0; round < 16; round++) {
      phase = `live ${round}`
      const n = pick([...data.values()].filter(n => n.kind_slug === 'ticket'))
      const before = outline.rows.value.map(n => n.id).join(',')
      const oldParent = outline.entries.value.find(e => e.type === 'row' && e.row.id === n.id)
      const mode = round % 4
      if (mode === 0) {
        n.title = `live-${seed}-${round}`; n.updated_at = at(++revision)
        event(n.id, 'updated', revision, ['title'])
      } else if (mode === 1) {
        n.parent_id = pick([root, 'a', 'b'].filter(id => id !== n.parent_id))
        n.parent = parent(n.parent_id); n.updated_at = at(++revision)
        event(n.id, 'updated', revision, ['parent_id'])
      } else if (mode === 2) {
        data.delete(n.id); event(n.id, 'deleted', ++revision)
      } else {
        const added = make(`live-${round}`, pick([root, 'a', 'b']))
        data.set(added.id, added); event(added.id, 'created', revision)
      }
      await drain()
      if (outline.rows.value.map(n => n.id).join(',') !== before) fail('remote change moved rows before Show')
      if (mode === 0 && outline.node(n.id)?.title !== n.title) fail('live field patch did not arrive')
      if (mode === 1 && oldParent?.type === 'row') {
        const entry = outline.entries.value.find(e => e.type === 'row' && e.row.id === n.id)
        if (entry?.type !== 'row' || entry.tree.parentId !== oldParent.tree.parentId) fail('parent moved before Show')
        if (outline.live.pending.kind(n.id) !== 'moved') fail('remote move did not wait behind Show')
      }
      outline.live.apply(); await settle(); await drain()
      checkCounts()
      // Every level is already expanded: Show must place all surviving rows.
      if (outline.rows.value.map(n => n.id).sort().join() !== [...data.keys()].sort().join()) fail('live Outline membership did not converge')
      for (const entry of outline.entries.value) if (entry.type === 'row') {
        const server = data.get(entry.row.id)
        if (!server || entry.tree.parentId !== server.parent_id || entry.row.updated_at !== server.updated_at) fail(`${entry.row.id} live placement/revision did not converge`)
      }
    }

    phase = 'filtered live ancestor cache'
    const match = pick([...data.values()].filter(n => n.kind_slug === 'ticket'))
    if (match.parent_id !== 'a') await move(match.id, 'a')
    match.title = 'unique-filter-match'; match.updated_at = at(++revision)
    outlineFilters.value = filtersFromQuery({ q: 'unique-filter-match', closed: '1' })
    const loading = list.load()
    await drain(); await loading
    for (let round = 0; round < 4; round++) {
      phase = `filtered live ancestor ${round}`
      const ancestor = data.get('a')!
      ancestor.title = `renamed-${seed}-${round}`; ancestor.updated_at = at(++revision)
      event(ancestor.id, 'updated', revision, ['title'])
      await drain()
      if (outline.node('a')?.title !== ancestor.title) fail('cached ancestor ignored live field change')
      if (outline.live.pending.kind('a')) fail('an ancestor field patch was classified as structural')
      const oldParent = ancestor.parent_id
      ancestor.parent_id = oldParent === root ? 'b' : root; ancestor.parent = parent(ancestor.parent_id)
      ancestor.updated_at = at(++revision); event('a', 'updated', revision, ['parent_id'])
      await drain()
      if (outline.live.pending.kind('a') !== 'moved') fail('an ancestor move was not classified as moved')
      const held = outline.entries.value.find(e => e.type === 'row' && e.row.id === 'a')
      if (held?.type !== 'row' || held.tree.parentId !== oldParent) fail('filtered ancestor moved before Show')
      outline.live.apply(); await settle(); await drain()
      const entry = outline.entries.value.find(e => e.type === 'row' && e.row.id === match.id)
      if (entry?.type !== 'row' || entry.tree.depth !== (ancestor.parent_id === root ? 1 : 2)) fail('filtered live ancestry did not converge')
    }
    phase = 'filtered field patches keep sort placement'
    const sibling = make('filtered-sibling', 'a')
    sibling.title = 'unique-filter-match-sibling'; data.set(sibling.id, sibling)
    event(sibling.id, 'created', revision)
    await drain(); outline.live.apply(); await settle(); await drain()
    const order = outline.rows.value.map(row => row.id).join(',')
    match.title = 'unique-filter-match-patched'; match.updated_at = at(++revision)
    event(match.id, 'updated', revision, ['title']); await drain()
    if (outline.rows.value.map(row => row.id).join(',') !== order) fail('filtered field patch reordered rows')
    if (outline.node(match.id)?.title !== match.title) fail('filtered field patch did not arrive')
    const addedMatch = make('filtered-new', 'a')
    addedMatch.title = 'unique-filter-match-new'; data.set(addedMatch.id, addedMatch)
    event(addedMatch.id, 'created', revision); await drain()
    outline.live.apply(); await settle(); await drain()
    if (outline.rows.value.filter(row => row.id !== addedMatch.id).map(row => row.id).join(',') !== order) fail('Show reordered filtered field-only patches')
    const ancestor = data.get('a')!
    ancestor.title = 'unique-filter-match-ancestor'; ancestor.updated_at = at(++revision)
    event('a', 'updated', revision, ['title']); await drain()
    const promoted = outline.entries.value.find(e => e.type === 'row' && e.row.id === 'a')
    if (promoted?.type !== 'row' || promoted.tree.dimmed) fail('an ancestor that now matches stayed dimmed')
    if (list.rows.value[0]?.id !== ancestor.id) fail('promoted ancestor was not inserted in sort order')

    phase = 'closing an ancestor under hide-closed'
    ancestor.parent_id = root; ancestor.parent = parent(root); ancestor.updated_at = at(++revision)
    outlineFilters.value = filtersFromQuery({})
    const hiding = list.load(); await drain(); await hiding
    outline.setExpanded('a', true); await drain()
    ancestor.state = 'done'; ancestor.updated_at = at(++revision)
    event(ancestor.id, 'updated', revision, ['state']); await drain()
    if (outline.live.pending.kind(ancestor.id) !== 'closed') fail('closed parent did not wait for Show')
    outline.live.apply(); await settle(); await drain()
    // The presence check must not promote the context ancestor on a later patch.
    match.title = 'still open'; match.updated_at = at(++revision)
    event(match.id, 'updated', revision, ['title']); await drain()
    const context = outline.entries.value.find(e => e.type === 'row' && e.row.id === ancestor.id)
    if (context?.type !== 'row' || !context.tree.dimmed) fail('closed parent became a match instead of context')
    if (list.rows.value.some(row => row.id === ancestor.id)) fail('closed parent leaked into List matches')
  } finally { selected.value = null; scope.stop(); off(); vi.clearAllTimers(); vi.useRealTimers(); rowStore.clear() }
  return failures
}

describe('the live Outline over delayed pages, gaps and child moves', () => {
  it(`admits trusted pages, patches live, and holds placement until Show (${SEEDS} seeds)`, async () => {
    // Session-cached metadata must not consume random choices only for the
    // first seed: replaying any seed alone uses exactly the same schedule.
    fetchNow = async () => ({ ok: true, status: 200, headers: new Headers(), json: async () => ({
      items: ['ticket', 'epic', 'task'].map(slug => ({ id: `k-${slug}`, slug, label: slug, schema: {} })),
    }) }) as Response
    await kinds()
    const failed: string[] = []
    for (let seed = TRACE || 1; seed <= (TRACE || SEEDS); seed++) {
      const failures = await runOutline(seed)
      if (failures.length) failed.push(`seed ${seed}: ${failures.join('; ')}`)
    }
    if (failed.length) console.log(`OUTLINE-VIEWS ${failed.length}/${SEEDS} seeds failed\n${failed.slice(0, 12).join('\n')}`)
    expect(failed).toEqual([])
  }, 600_000)
})

// Isolating take()'s floor check is intentionally a store-level assertion:
// no view calls the private take() directly. Outline/list use adopt()'s
// return, which is also gated by showable()/visible(); the panel's child-page
// loader reads latest() only after adopt() succeeds. show() and current() enforce the
// same floor too. Removing only take's check can poison latest without making
// a view show it; row-store.unit.test.ts detects that state. Removing the
// floor altogether must (and does) fail both view-level seeded suites.
