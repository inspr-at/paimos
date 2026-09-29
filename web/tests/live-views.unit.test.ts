// SPDX-License-Identifier: AGPL-3.0-only
// AEON-326: the ticket list, the live list and the open ticket, wired as the
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
// - once everything arrived, the list and the panel show the server's state.
// LIVE_VIEW_SEEDS sets the number of seeds; LIVE_VIEW_TRACE=<seed> replays
// one with every step logged (run with --reporter=verbose to see it pass).
import { afterAll, beforeAll, describe, expect, it, vi } from 'vitest'
import { computed, effectScope, nextTick, ref } from 'vue'
import type { ListItem, ListParent, WorkNode } from '../src/lib/api'
import { LiveNodeStore } from '../src/lib/liveNodes'
import { rowStore } from '../src/lib/rowStore'
import { filtersFromQuery } from '../src/lib/ticketList'
import { guardedMove, useTicket } from '../src/lib/useTicket'
import { useLiveList } from '../src/lib/useLiveList'
import { useTicketList } from '../src/lib/useTicketList'

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
