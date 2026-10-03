// SPDX-License-Identifier: AGPL-3.0-only
// AEON-326: the tab's causal row store. Every read, write and event goes
// through it; revisions only move forward, a deletion leaves a tombstone that
// nothing older crosses, and only this tab's exact write revisions are its own.
import { describe, expect, it } from 'vitest'
import type { ListItem, WorkNode } from '../src/lib/api'
import { compareRevision } from '../src/lib/liveUpdates'
import { RowStore } from '../src/lib/rowStore'
import { stampAt } from '../src/lib/position'

const PROJECT = { id: 'p-1', key: 'PRJ', title: 'Project' }
const at = (n: number) => new Date(Date.parse('2026-09-29T10:00:00Z') + n * 1000).toISOString()
function node(id: string, revision: number, over: Partial<WorkNode> = {}): WorkNode {
  return { id, key: `PRJ-${id}`, kind_id: 'k', title: `Title ${revision}`, body: '', fields: { priority: 'low' }, state: 'new', parent_id: PROJECT.id, position: '1', created_at: at(0), updated_at: at(revision), ...over }
}
function item(id: string, revision: number, over: Partial<ListItem> = {}): ListItem {
  return { ...node(id, revision), kind_slug: 'ticket', kind_label: 'Ticket', priority: 'low', assignee: null, parent: { id: PROJECT.id, key: 'PRJ', title: 'Project', kind_slug: 'project' }, children_count: 0, project: PROJECT, ...over }
}

describe('RowStore: revisions only move forward', () => {
  it('a late list snapshot cannot restore an ended worker or overdue ETA at the same node revision', () => {
    const rows = new RowStore()
    const old = rows.mark()
    const row = rows.adopt(stampAt(item('n1', 1, { eta: { finished: true }, lead_worker: null }), { position: 52 }), rows.mark(), { show: true })!
    rows.adopt(stampAt(item('n1', 1, { eta: { finished: false, eta_ready_at: at(0) }, lead_worker: { name: 'Ended worker', key: 's:ended' } }), { position: 51 }), old, { show: true })
    expect(row.eta).toEqual({ finished: true })
    expect(row.lead_worker).toBeNull()
  })
  it('prefers server position over request order and still recovers after a database restore', () => {
    const rows = new RowStore()
    const first = rows.mark(), second = rows.mark()
    const row = rows.adopt(stampAt(item('n1', 1, { lead_worker: null }), { position: 52 }), first, { show: true })!
    // A later-started request processed earlier is still an older snapshot.
    rows.adopt(stampAt(item('n1', 1, { lead_worker: { name: 'Ended worker', key: 's:ended' } }), { position: 51 }), second, { show: true })
    expect(row.lead_worker).toBeNull()
    // Asked after the current answer landed: a lower position means the log reset.
    rows.adopt(stampAt(item('n1', 1, { lead_worker: { name: 'Restored worker', key: 's:restored' } }), { position: 1 }), rows.mark(), { show: true })
    expect(row.lead_worker?.name).toBe('Restored worker')
  })
  it('falls back to request order without a position and drops a read sent before a session hint', () => {
    const rows = new RowStore()
    const old = rows.mark()
    const row = rows.adopt(item('n1', 1, { lead_worker: null }), rows.mark(), { show: true })!
    rows.adopt(item('n1', 1, { lead_worker: { name: 'Ended worker', key: 's:ended' } }), old, { show: true })
    expect(row.lead_worker).toBeNull()
    const pending = rows.mark()
    rows.note({ id: 'n1', change: 'updated', revision: null, fields: ['eta', 'lead_worker'] })
    rows.adopt(item('n1', 1, { lead_worker: { name: 'Old snapshot', key: 's:old' } }), pending, { show: true })
    expect(row.lead_worker).toBeNull()
    rows.adopt(item('n1', 1, { lead_worker: { name: 'New worker', key: 's:new' } }), rows.mark(), { show: true })
    expect(row.lead_worker?.name).toBe('New worker')
  })
  it('accepts a late-processed read when its server snapshot includes the stop event', () => {
    const rows = new RowStore()
    const row = rows.adopt(item('n1', 1, { lead_worker: { name: 'Worker', key: 's:worker' } }), rows.mark(), { show: true })!
    const sent = rows.mark()
    rows.note({ id: 'n1', change: 'updated', revision: null, fields: ['eta', 'lead_worker'], eventId: 52 })
    rows.adopt(stampAt(item('n1', 1, { lead_worker: null }), { position: 52 }), sent, { show: true })
    expect(row.lead_worker).toBeNull()
    expect(rows.projectionsCurrent('n1')).toBe(true)
  })
  it('a list page that lands after a save and its event is dropped: the row keeps the save', () => {
    const rows = new RowStore()
    const row = rows.adopt(item('n1', 1), rows.mark(), { show: true })!
    // The load is sent, then the panel saves High and its own event arrives.
    const load = rows.mark()
    rows.wrote(node('n1', 5, { fields: { priority: 'high' } }), rows.mark())
    expect(rows.note({ id: 'n1', change: 'updated', revision: at(5) })).toBe('known')
    // The older page lands.
    expect(rows.adopt(item('n1', 3, { priority: 'medium', fields: { priority: 'medium' } }), load, { show: true })).toBe(row)
    expect(row.priority).toBe('high')
    expect(row.updated_at).toBe(at(5))
    expect(rows.newer('n1', at(3))).toBe(true)
  })

  it('applies a page covering the batch hint while a newer projection hint still waits', () => {
    const rows = new RowStore()
    const row = rows.adopt(item('n1', 1, { eta: { finished: false }, lead_worker: { name: 'Ended worker', key: 's:ended' } }), rows.mark(), { show: true })!
    rows.note({ id: 'n1', change: 'updated', revision: null, fields: ['eta', 'lead_worker'], eventId: 52 })
    const sent = rows.mark()
    rows.note({ id: 'n1', change: 'updated', revision: null, fields: ['eta', 'lead_worker'], eventId: 53 })
    rows.adopt(stampAt(item('n1', 1, { eta: { finished: true }, lead_worker: null }), { position: 52 }), sent, { projectionFloor: 52 })
    expect(row.eta).toEqual({ finished: true })
    expect(row.lead_worker).toBeNull()
    expect(rows.projectionsCurrent('n1', 52)).toBe(true)
    expect(rows.projectionsCurrent('n1')).toBe(false)
    rows.adopt(stampAt(item('n1', 1, { eta: { finished: true, progress_pct: 100 } }), { position: 53 }), rows.mark())
    expect(rows.projectionsCurrent('n1')).toBe(true)
  })

  it('a batch floor cannot admit a pre-hint or unpositioned page or rewind a newer projection', () => {
    const rows = new RowStore()
    const row = rows.adopt(stampAt(item('n1', 1, { lead_worker: null }), { position: 51 }), rows.mark(), { show: true })!
    rows.note({ id: 'n1', change: 'updated', revision: null, fields: ['lead_worker'], eventId: 52 })
    const first = rows.mark(), second = rows.mark()
    rows.note({ id: 'n1', change: 'updated', revision: null, fields: ['lead_worker'], eventId: 53 })
    const stale = item('n1', 1, { lead_worker: { name: 'Ended worker', key: 's:ended' } })
    rows.adopt(stampAt(stale, { position: 51 }), first, { projectionFloor: 52 })
    expect(row.lead_worker).toBeNull()
    rows.adopt(item('n1', 1, stale), first, { projectionFloor: 52 })
    expect(row.lead_worker).toBeNull()
    rows.adopt(stampAt(item('n1', 1, { lead_worker: { name: 'New worker', key: 's:new' } }), { position: 53 }), second)
    rows.adopt(stampAt(stale, { position: 52 }), first, { projectionFloor: 52 })
    expect(row.lead_worker?.name).toBe('New worker')
    // After the current answer landed, a lower counter proves a database rewind.
    rows.adopt(stampAt(item('n1', 1, { lead_worker: null }), { position: 1 }), rows.mark())
    expect(row.lead_worker).toBeNull()
    expect(rows.projectionsCurrent('n1')).toBe(true)
  })

  it('one display object per node: every read of it lands on the same object', () => {
    const rows = new RowStore()
    const row = rows.adopt(item('n1', 1), rows.mark(), { show: true })!
    expect(rows.adopt(item('n1', 2, { title: 'Two' }), rows.mark(), { show: true })).toBe(row)
    expect(rows.adoptNode(node('n1', 3, { title: 'Three' }), rows.mark(), { show: true })).toBe(row)
    expect(row.title).toBe('Three')
    // A node read keeps the list projections.
    expect(row.project).toEqual(PROJECT)
    expect(row.kind_slug).toBe('ticket')
  })

  it('keeps recurring provenance through a save and clears it when a fresh list withholds the source', () => {
    const rows = new RowStore()
    const recurrence = { id: 'r-1', project_id: PROJECT.id, project_key: PROJECT.key, number: 4, retired: false, trigger: { kind: 'time' as const, rrule: 'FREQ=WEEKLY;BYDAY=MO' } }
    const row = rows.adopt(item('n1', 1, { recurrence }), rows.mark(), { show: true })!
    rows.wrote(node('n1', 2, { title: 'Saved title' }), rows.mark())
    expect(row.recurrence).toEqual(recurrence)
    rows.adopt(item('n1', 2, { title: 'Saved title' }), rows.mark(), { show: true })
    expect(row.recurrence).toBeUndefined()
    expect(rows.latest('n1')?.recurrence).toBeUndefined()
    expect(row.title).toBe('Saved title')
  })

  it('a replayed event older than what the store knows is older', () => {
    const rows = new RowStore()
    rows.adopt(item('n1', 5), rows.mark())
    expect(rows.note({ id: 'n1', change: 'updated', revision: at(4) })).toBe('older')
    expect(rows.note({ id: 'n1', change: 'updated', revision: at(5) })).toBe('known')
    expect(rows.note({ id: 'n1', change: 'updated', revision: at(6) })).toBe('newer')
  })
})

describe('RowStore: tombstones', () => {
  it('a late bulk answer cannot place a retained display object below a restore floor', () => {
    const rows = new RowStore()
    const original = rows.adopt(item('n1', 1), rows.mark(), { show: true })!
    const sent = rows.mark()
    rows.adopt(item('n1', 2, { parent_id: 'elsewhere' }), rows.mark(), { show: true })
    rows.note({ id: 'n1', change: 'deleted', revision: at(3) })
    rows.note({ id: 'n1', change: 'created', revision: at(4) })
    expect(rows.wrote(node('n1', 2, { parent_id: 'elsewhere' }), sent)).toBeNull()
    expect(rows.isDeleted('n1')).toBe(false)
    expect(rows.row('n1')).toBe(original)
    expect(rows.visibleRow('n1')).toBeUndefined()
    rows.adopt(item('n1', 4), rows.mark(), { show: true })
    expect(rows.visibleRow('n1')).toBe(original)
  })

  it('nothing at or before a deletion brings the node back; a restore does', () => {
    const rows = new RowStore()
    const sent = rows.mark()
    rows.adopt(item('n1', 1), sent)
    rows.note({ id: 'n1', change: 'deleted', revision: at(4) })
    expect(rows.isDeleted('n1')).toBe(true)
    // A pending addition, an in-flight read, a late event read: all older.
    expect(rows.adopt(item('n1', 3), sent, { show: true })).toBeNull()
    expect(rows.adoptNode(node('n1', 4), rows.mark())).toBeNull()
    expect(rows.note({ id: 'n1', change: 'updated', revision: at(3) })).toBe('older')
    expect(rows.isDeleted('n1')).toBe(true)
    // The restore (it reads as created) and its newer copy do.
    expect(rows.note({ id: 'n1', change: 'created', revision: at(5) })).toBe('newer')
    expect(rows.isDeleted('n1')).toBe(false)
    expect(rows.adopt(item('n1', 5, { title: 'Back' }), rows.mark(), { show: true })?.title).toBe('Back')
  })

  it('a read that found the node gone does not bury a restore that arrived after it was sent', () => {
    const rows = new RowStore()
    rows.adopt(item('n1', 1), rows.mark())
    const sent = rows.mark()
    rows.note({ id: 'n1', change: 'created', revision: at(5) })
    rows.gone('n1', sent)
    expect(rows.isDeleted('n1')).toBe(false)
    // Without anything newer, a 404 buries it; only a read sent afterwards can restore it.
    const before = rows.mark()
    rows.gone('n1', rows.mark())
    expect(rows.isDeleted('n1')).toBe(true)
    expect(rows.adopt(item('n1', 6), before)).toBeNull()
    expect(rows.adopt(item('n1', 6), rows.mark())).not.toBeNull()
  })

  it('a gap makes every earlier copy uncertain until a later read confirms it', () => {
    const rows = new RowStore()
    rows.adopt(item('n1', 1), rows.mark())
    expect(rows.current('n1')).toBe(true)
    rows.gap()
    expect(rows.current('n1')).toBe(false)
    // The same revision read again after the gap confirms it.
    rows.adopt(item('n1', 1), rows.mark())
    expect(rows.current('n1')).toBe(true)
  })
})

describe('RowStore: this tab\'s writes, exactly', () => {
  it('a late delete answer after the deletion and a restore arrived does not claim the next deletion', () => {
    const rows = new RowStore()
    rows.adopt(item('n1', 1), rows.mark())
    const sent = rows.mark()
    // The stream delivers the deletion (unrecognised yet) and a restore first.
    rows.note({ id: 'n1', change: 'deleted', revision: at(2) })
    rows.note({ id: 'n1', change: 'created', revision: at(3) })
    // The DELETE answer lands late, naming the revision of its event.
    rows.deleted('n1', at(2), sent)
    expect(rows.isDeleted('n1')).toBe(false)
    expect(rows.isOwn('n1', at(2))).toBe(true)
    // Another tab under the same account deletes it again: not this tab's.
    expect(rows.isOwn('n1', at(4))).toBe(false)
    expect(rows.note({ id: 'n1', change: 'deleted', revision: at(4) })).toBe('newer')
    expect(rows.isDeleted('n1')).toBe(true)
  })

  it('a save is known by its revision only; the node id alone proves nothing', () => {
    const rows = new RowStore()
    rows.adopt(item('n1', 1), rows.mark())
    rows.wrote(node('n1', 2), rows.mark())
    expect(rows.isOwn('n1', at(2))).toBe(true)
    expect(rows.isOwn('n1', at(3))).toBe(false)
    expect(rows.isOwn('n2', at(2))).toBe(false)
  })
})

describe('RowStore: editors', () => {
  it('an editor pins the row: newer copies wait, and a conflict rebases onto the store\'s copy', () => {
    const rows = new RowStore()
    const row = rows.adopt(item('n1', 1, { title: 'Original', fields: { priority: 'low', notes: 'n' } }), rows.mark(), { show: true })!
    const editor = rows.edit('n1')!
    expect(editor.revision).toBe(at(1))
    rows.adopt(item('n1', 5, { title: 'Theirs', fields: { priority: 'low', notes: 'remote' } }), rows.mark(), { show: true })
    expect(row.title).toBe('Original')
    expect(rows.waiting('n1')).toBe(true)
    // 412: the newest copy for this id becomes the base and shows.
    editor.rebase()
    expect(editor.revision).toBe(at(5))
    expect(editor.base.fields).toEqual({ priority: 'low', notes: 'remote' })
    expect(row.title).toBe('Theirs')
    expect(rows.waiting('n1')).toBe(false)
    editor.end()
    expect(rows.pinned('n1')).toBe(false)
  })

  it('an editor\'s own save is its base; someone else\'s newer copy still waits', () => {
    const rows = new RowStore()
    const row = rows.adopt(item('n1', 1), rows.mark(), { show: true })!
    const editor = rows.edit('n1')!
    rows.adopt(item('n1', 7, { title: 'Later, by Mira' }), rows.mark())
    const saved = node('n1', 5, { title: 'Mine' })
    rows.wrote(saved, rows.mark())
    editor.saved(saved)
    expect(editor.revision).toBe(at(5))
    expect(row.title).toBe('Mine')
    expect(rows.waiting('n1')).toBe(true)
    editor.end()
    rows.show('n1')
    expect(row.title).toBe('Later, by Mira')
  })
})

describe('review 326g: authoritative child counts replace local dedupe history', () => {
  it('AEON-385: pages predating local membership changes cannot replace counts or reset dedupe', () => {
    const rows = new RowStore()
    const parent = rows.adopt(item('parent', 1, { children_count: 3 }), rows.mark(), { show: true })!
    const sent = rows.mark()
    rows.child('parent', 'leaving', false)
    rows.adopt(item('parent', 1, { children_count: 3 }), sent, { show: true })
    expect(parent.children_count).toBe(2)
    rows.child('parent', 'leaving', false)
    expect(parent.children_count).toBe(2)
    const another = rows.mark()
    rows.child('parent', 'arriving', true)
    rows.adopt(item('parent', 2, { children_count: 2 }), another, { show: true })
    expect(parent.children_count).toBe(3)
    rows.child('parent', 'arriving', true)
    expect(parent.children_count).toBe(3)
    rows.adopt(item('parent', 2, { children_count: 1 }), rows.mark(), { show: true })
    expect(parent.children_count).toBe(1)
    rows.child('parent', 'arriving', true)
    expect(parent.children_count).toBe(2)
  })

  it('counts a child returning after a remote move and same-revision reload', () => {
    const rows = new RowStore()
    const parent = rows.adopt(item('parent', 1), rows.mark(), { show: true })!
    rows.child('parent', 'child', true)
    rows.child('parent', 'child', true) // panel and Outline report the creation
    expect(parent.children_count).toBe(1)
    // Moving a child does not change the parent's own revision.
    rows.adopt(item('parent', 1, { children_count: 0 }), rows.mark(), { show: true })
    rows.child('parent', 'child', true)
    expect(parent.children_count).toBe(1)
    rows.child('parent', 'child', false)
    rows.adopt(item('parent', 1, { children_count: 1 }), rows.mark(), { show: true })
    rows.child('parent', 'child', false)
    expect(parent.children_count).toBe(0)
  })

  it('node reads and rejected list copies keep the dedupe for the current count', () => {
    const rows = new RowStore()
    const parent = rows.adopt(item('parent', 2), rows.mark(), { show: true })!
    rows.child('parent', 'child', true)
    rows.adoptNode(node('parent', 3), rows.mark(), { show: true })
    rows.child('parent', 'child', true)
    expect(parent.children_count).toBe(1)
    rows.adopt(item('parent', 1, { children_count: 0 }), rows.mark(), { show: true })
    rows.child('parent', 'child', true)
    expect(parent.children_count).toBe(1)
  })
})

// ---------- Random interleavings ----------
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

interface ServerState { revision: number; title: string; deleted: boolean }
interface Event { id: string; change: 'created' | 'updated' | 'deleted'; revision: number; own: boolean }
// A request in flight: sent (the store's clock), then answered from the
// server as it was when processed, then delivered in any order.
interface Request { id: string; sent: number; kind: 'list' | 'node' | 'save' | 'delete'; answer?: ServerState | null; own?: number }

function run(seed: number, steps = 60) {
  const next = random(seed)
  const pick = <T>(list: T[]) => list[Math.floor(next() * list.length)]
  const rows = new RowStore()
  const ids = ['a', 'b']
  const server = new Map<string, ServerState>(ids.map(id => [id, { revision: 1, title: `${id}1`, deleted: false }]))
  let clock = 1
  const events: Event[] = []
  let delivered = 0
  const flight: Request[] = []
  const answered: Request[] = []
  const owned = new Set<string>()
  const ownOrder: string[] = []
  const tombs = new Map<string, number>()
  const failures: string[] = []
  const copy = (id: string, state: ServerState): ListItem => item(id, state.revision, { title: state.title })

  // Both rows start loaded.
  for (const id of ids) rows.adopt(copy(id, server.get(id)!), rows.mark(), { show: true })
  const shownAt = new Map(ids.map(id => [id, rows.shown(id)!.updated_at]))

  function mutate(id: string, own: boolean, kind?: 'update' | 'delete' | 'restore') {
    const state = server.get(id)!
    const op = kind ?? (state.deleted ? 'restore' : next() < 0.25 ? 'delete' : 'update')
    if ((op === 'restore') !== state.deleted) return null
    const revision = ++clock
    const change = op === 'delete' ? 'deleted' : op === 'restore' ? 'created' : 'updated'
    server.set(id, { revision, title: `${id}${revision}`, deleted: op === 'delete' })
    events.push({ id, change, revision, own })
    return { ...server.get(id)! }
  }
  const latestAt = new Map(ids.map(id => [id, rows.latest(id)!.updated_at]))
  // The store buried the node at this revision (an event or this tab's delete).
  function buried(id: string, revision: number) { if (rows.isDeleted(id)) tombs.set(id, revision) }
  function check(where: string) {
    for (const id of ids) {
      const shown = rows.shown(id)?.updated_at
      if (compareRevision(shown, shownAt.get(id)) < 0) failures.push(`${where}: ${id} shown went back ${shownAt.get(id)} → ${shown}`)
      shownAt.set(id, shown!)
      const latest = rows.latest(id)?.updated_at
      if (compareRevision(latest, latestAt.get(id)) < 0) failures.push(`${where}: ${id} latest went back ${latestAt.get(id)} → ${latest}`)
      // After a tombstone at R, no copy at or before R ever becomes the newest again.
      const tomb = tombs.get(id)
      if (latest !== latestAt.get(id) && tomb !== undefined && compareRevision(latest, at(tomb)) <= 0) {
        failures.push(`${where}: ${id} took ${latest} after its tombstone at ${at(tomb)}`)
      }
      latestAt.set(id, latest!)
    }
  }
  function deliver(request: Request) {
    const { id, answer } = request
    if (request.kind === 'save') {
      const saved = answer!
      rows.wrote(node(id, saved.revision, { title: saved.title }), request.sent)
      owned.add(`${id}@${saved.revision}`); ownOrder.push(`${id}@${saved.revision}`)
    } else if (request.kind === 'delete') {
      rows.deleted(id, answer ? at(answer.revision) : null, request.sent)
      if (answer) { owned.add(`${id}@${answer.revision}`); ownOrder.push(`${id}@${answer.revision}`); buried(id, answer.revision) }
    } else if (!answer || answer.deleted) rows.gone(id, request.sent)
    else if (request.kind === 'list') rows.adopt(copy(id, answer), request.sent, { show: next() < 0.5 })
    else rows.adoptNode(node(id, answer.revision, { title: answer.title }), request.sent, { show: next() < 0.5 })
  }

  for (let step = 0; step < steps; step++) {
    const roll = next()
    const id = pick(ids)
    if (roll < 0.2) mutate(id, false)
    else if (roll < 0.3) {
      // This tab saves: the server applies it when processed.
      if (!server.get(id)!.deleted) flight.push({ id, sent: rows.mark(), kind: 'save' })
    } else if (roll < 0.35) {
      if (!server.get(id)!.deleted) flight.push({ id, sent: rows.mark(), kind: 'delete' })
    } else if (roll < 0.55) flight.push({ id, sent: rows.mark(), kind: next() < 0.5 ? 'list' : 'node' })
    else if (roll < 0.7 && flight.length) {
      // The server processes a request.
      const request = flight.splice(Math.floor(next() * flight.length), 1)[0]
      if (request.kind === 'save') request.answer = server.get(request.id)!.deleted ? null : mutate(request.id, true, 'update')
      else if (request.kind === 'delete') request.answer = server.get(request.id)!.deleted ? null : mutate(request.id, true, 'delete')
      else request.answer = { ...server.get(request.id)! }
      // A write the server refused (the node was deleted) answers nothing to deliver.
      if ((request.kind === 'save' || request.kind === 'delete') && !request.answer) continue
      answered.push(request)
    } else if (roll < 0.85 && answered.length) {
      deliver(answered.splice(Math.floor(next() * answered.length), 1)[0])
      check(`step ${step} answer`)
    } else if (delivered < events.length) {
      // The stream delivers in order; a view reads what an event names.
      const event = events[delivered++]
      rows.note({ id: event.id, change: event.change, revision: at(event.revision) })
      if (event.change === 'deleted') buried(event.id, event.revision)
      if (event.change !== 'deleted') flight.push({ id: event.id, sent: rows.mark(), kind: 'node' })
      check(`step ${step} event`)
    }
  }
  // Everything still on the way arrives: the server answers, the stream
  // catches up, every read and write answer lands, in any order.
  for (const request of flight.splice(0)) {
    if (request.kind === 'save') request.answer = server.get(request.id)!.deleted ? null : mutate(request.id, true, 'update')
    else if (request.kind === 'delete') request.answer = server.get(request.id)!.deleted ? null : mutate(request.id, true, 'delete')
    else request.answer = { ...server.get(request.id)! }
    if ((request.kind === 'save' || request.kind === 'delete') && !request.answer) continue
    answered.push(request)
  }
  const last: Request[] = []
  while (delivered < events.length || answered.length) {
    if (answered.length && (delivered >= events.length || next() < 0.5)) {
      deliver(answered.splice(Math.floor(next() * answered.length), 1)[0])
    } else {
      const event = events[delivered++]
      rows.note({ id: event.id, change: event.change, revision: at(event.revision) })
      if (event.change === 'deleted') buried(event.id, event.revision)
      // The read an event asks for is answered from the server's final state.
      if (event.change !== 'deleted') last.push({ id: event.id, sent: rows.mark(), kind: 'node', answer: { ...server.get(event.id)! } })
    }
    check('drain')
  }
  for (const request of last.sort(() => next() - 0.5)) { deliver(request); check('last reads') }

  // The final state equals the server's.
  for (const id of ids) {
    const state = server.get(id)!
    if (state.deleted) {
      if (!rows.isDeleted(id)) failures.push(`${id} deleted on the server (r${state.revision}) but not in the store`)
    } else {
      if (rows.isDeleted(id)) failures.push(`${id} alive on the server (r${state.revision}) but deleted in the store`)
      const latest = rows.latest(id)
      if (latest?.updated_at !== at(state.revision) || latest?.title !== state.title) failures.push(`${id}: store has ${latest?.updated_at} ${latest?.title}, server r${state.revision} ${state.title}`)
      rows.show(id)
      if (rows.row(id)?.title !== state.title) failures.push(`${id}: the row shows ${rows.row(id)?.title}`)
    }
  }
  // Own is exact: an event is this tab's only when a write of this tab
  // answered its revision (the store remembers the last few per node).
  for (const event of events) {
    const own = rows.isOwn(event.id, at(event.revision))
    const recent = ownOrder.filter(key => key.startsWith(`${event.id}@`)).slice(-8)
    if (!own && recent.includes(`${event.id}@${event.revision}`)) failures.push(`this tab's event ${event.id}@${event.revision} not recognised`)
    if (own && !event.own) failures.push(`someone else's event ${event.id}@${event.revision} counted as own`)
    if (own && !owned.has(`${event.id}@${event.revision}`)) failures.push(`event ${event.id}@${event.revision} own before its answer`)
  }
  return failures
}

describe('RowStore: any delivery order ends at the server\'s state', () => {
  it('holds for random interleavings of loads, events, saves, deletes and restores', () => {
    const failed: string[] = []
    for (let seed = 1; seed <= 400; seed++) {
      const failures = run(seed)
      if (failures.length) failed.push(`seed ${seed}: ${failures.slice(0, 3).join('; ')}`)
    }
    if (failed.length) console.log(`ROW-STORE ${failed.length}/400 seeds failed`)
    expect(failed).toEqual([])
  })
})
