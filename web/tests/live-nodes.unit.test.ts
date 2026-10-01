// SPDX-License-Identifier: AGPL-3.0-only
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { WorkNode } from '../src/lib/api'
import { LiveNodeStore, mergeChanges, parseNodeChanges, type LiveView, type NodeChange } from '../src/lib/liveNodes'
import { RowStore } from '../src/lib/rowStore'
import {
  autoApplyDelay, canAutoApply, classifyChange, compareRevision, describeFields, PendingUpdates, PENDING_CAP, pillText, type ApplyGuard,
} from '../src/lib/liveUpdates'

it('session lifecycle and telemetry hints invalidate both old and new bound ticket projections', () => {
  const changes = parseNodeChanges(JSON.stringify({
    id: 51, type: 'harness.bound', actor_principal_id: 'agent',
    before: { ticket_node_id: 'old', project_id: 'p1' },
    after: { ticket_node_id: 'new', project_id: 'p1', row_version: 7 },
  }))
  expect(changes.map(change => [change.id, change.revision, change.fields])).toEqual([
    ['old', null, ['eta', 'lead_worker']], ['new', null, ['eta', 'lead_worker']],
  ])
  for (const type of ['harness.registered', 'harness.stopped', 'harness.heartbeat', 'harness.metadata_changed']) {
    expect(parseNodeChanges(JSON.stringify({ id: 52, type, after: { ticket_node_id: 'new', project_id: 'p1' } }))).toHaveLength(1)
  }
  expect(parseNodeChanges(JSON.stringify({ id: 53, type: 'harness.control_requested', after: { ticket_node_id: 'new' } }))).toEqual([])
  expect(parseNodeChanges('{')).toEqual([])
})

describe('compareRevision', () => {
  it('orders microseconds that Date.parse would merge', () => {
    expect(compareRevision('2026-09-29T10:00:00.000001Z', '2026-09-29T10:00:00.000002Z')).toBeLessThan(0)
    expect(compareRevision('2026-09-29T10:00:00.5Z', '2026-09-29T10:00:00.499999999Z')).toBeGreaterThan(0)
    expect(compareRevision('2026-09-29T10:00:00.1Z', '2026-09-29T10:00:00.100Z')).toBe(0)
  })
  it('compares across offsets and fraction lengths', () => {
    expect(compareRevision('2026-09-29T12:00:00.25+02:00', '2026-09-29T10:00:00.25Z')).toBe(0)
    expect(compareRevision('2026-09-29T12:00:01+02:00', '2026-09-29T10:00:00.999999Z')).toBeGreaterThan(0)
  })
  it('counts a missing or unreadable revision as older', () => {
    expect(compareRevision(null, '2026-09-29T10:00:00Z')).toBeLessThan(0)
    expect(compareRevision('2026-09-29T10:00:00Z', 'soon')).toBeGreaterThan(0)
    expect(compareRevision(undefined, null)).toBe(0)
  })
})

describe('classifyChange', () => {
  type Row = { state: string; project: string }
  const inProject = (row: Row) => row.project === 'A'
  const hideClosed = (row: Row) => row.project === 'A' && !['done', 'cancelled'].includes(row.state)
  const shown: Row = { state: 'progress', project: 'A' }
  const updated = (...fields: string[]) => ({ change: 'updated' as const, fields })

  it('patches a field change on a shown row in place', () => {
    expect(classifyChange({ change: updated('title'), shown, node: { ...shown }, matches: hideClosed })).toBe('patch')
  })
  it('marks a row closed while closed rows are hidden', () => {
    expect(classifyChange({ change: updated('state'), shown, node: { state: 'done', project: 'A' }, matches: hideClosed })).toBe('closed')
  })
  it('keeps a closed row that still matches as a patch', () => {
    expect(classifyChange({ change: updated('state'), shown, node: { state: 'done', project: 'A' }, matches: inProject })).toBe('patch')
  })
  it('marks a row that no longer matches for another reason', () => {
    expect(classifyChange({ change: updated('project_id'), shown, node: { state: 'progress', project: 'B' }, matches: inProject })).toBe('no_longer_matches')
    // Already closed and now in another project: not "closed" again.
    expect(classifyChange({ change: updated('project_id'), shown: { state: 'done', project: 'A' }, node: { state: 'done', project: 'B' }, matches: inProject })).toBe('no_longer_matches')
  })
  it('marks deleted and gone rows', () => {
    expect(classifyChange({ change: { change: 'deleted', fields: ['deleted_at'] }, shown, node: undefined, matches: inProject })).toBe('deleted')
    expect(classifyChange({ change: updated('title'), shown, node: null, matches: inProject })).toBe('deleted')
    expect(classifyChange({ change: { change: 'deleted', fields: [] }, shown: null, node: null, matches: inProject })).toBe('ignore')
  })
  it('marks a row whose order changed as moved', () => {
    expect(classifyChange({ change: updated('fields.priority'), shown, node: { ...shown }, matches: inProject, orderFields: ['fields.priority'] })).toBe('moved')
    expect(classifyChange({ change: updated('title'), shown, node: { ...shown }, matches: inProject, orderFields: ['fields.priority'] })).toBe('patch')
  })
  it('offers new matching nodes and ignores others', () => {
    expect(classifyChange({ change: { change: 'created', fields: [] }, shown: null, node: { state: 'new', project: 'A' }, matches: inProject })).toBe('new')
    expect(classifyChange({ change: updated('state'), shown: null, node: { state: 'new', project: 'A' }, matches: inProject })).toBe('new')
    expect(classifyChange({ change: { change: 'created', fields: [] }, shown: null, node: { state: 'new', project: 'B' }, matches: inProject })).toBe('ignore')
    expect(classifyChange({ change: { change: 'created', fields: [] }, shown: null, node: undefined, matches: inProject })).toBe('ignore')
  })
  it('treats an undecidable filter conservatively', () => {
    const unknown = () => null
    expect(classifyChange({ change: updated('title'), shown, node: { ...shown }, matches: unknown })).toBe('patch')
    expect(classifyChange({ change: { change: 'created', fields: [] }, shown: null, node: { ...shown }, matches: unknown })).toBe('new')
    expect(classifyChange({ change: updated('title'), shown: null, node: { ...shown }, matches: unknown })).toBe('ignore')
    expect(classifyChange({ change: updated('title'), shown, node: undefined, matches: unknown })).toBe('patch')
  })
})

describe('PendingUpdates', () => {
  it('keeps the latest structural kind per node and labels it', () => {
    const pending = new PendingUpdates()
    pending.note('a', 'closed')
    pending.note('b', 'new')
    pending.note('a', 'deleted')
    expect(pending.count).toBe(2)
    expect(pending.kind('a')).toBe('deleted')
    expect(pending.label('a')).toBe('Deleted')
    expect(pending.label('b')).toBe('New')
    expect(pending.label('c')).toBeNull()
  })
  it('settles a pending row when it matches again, and drops a new node deleted again', () => {
    const pending = new PendingUpdates()
    pending.note('reopened', 'closed')
    pending.note('reopened', 'patch')
    pending.note('brief', 'new')
    pending.note('brief', 'deleted')
    pending.note('stray', 'new')
    pending.note('stray', 'ignore')
    expect(pending.count).toBe(0)
  })
  it('reports selected ids that were deleted and hands everything over at once', () => {
    const pending = new PendingUpdates()
    pending.note('a', 'deleted'); pending.note('b', 'closed'); pending.note('c', 'deleted')
    expect(pending.deletedAmong(new Set(['a', 'b', 'x']))).toEqual(['a'])
    const taken = pending.take()
    expect([...taken.entries()]).toEqual([['a', 'deleted'], ['b', 'closed'], ['c', 'deleted']])
    expect(pending.count).toBe(0)
    pending.note('d', 'moved'); pending.clear()
    expect(pending.count).toBe(0)
  })
  it('keeps at most the cap: 10,000 updates hold the newest 200 and offer a reload', () => {
    const pending = new PendingUpdates()
    const dropped: string[] = []
    for (let i = 0; i < 10_000; i++) dropped.push(...pending.note(`n${i}`, 'new'))
    expect(pending.count).toBe(PENDING_CAP)
    expect(dropped).toHaveLength(10_000 - PENDING_CAP)
    expect(pending.kind('n9999')).toBe('new')
    expect(pending.kind('n0')).toBeUndefined()
    expect(pending.overflow).toBe(true)
    expect(pillText(pending)).toBe('Many updates · Reload view')
  })
  it('drops new rows before marked ones and deletions last, so a selection still learns of them', () => {
    const pending = new PendingUpdates(3)
    pending.note('deleted', 'deleted'); pending.note('closed', 'closed'); pending.note('moved', 'moved')
    expect(pending.note('new', 'new')).toEqual(['new'])
    expect(pending.note('changed', 'changed')).toEqual(['moved'])
    expect(pending.note('closed-2', 'closed')).toEqual(['changed'])
    expect(pending.note('closed-3', 'closed')).toEqual(['closed'])
    expect(pending.deletedAmong(['deleted', 'closed-2'])).toEqual(['deleted'])
    expect(pending.label('closed-3')).toBe('Closed')
  })
  it('still offers the reload when what it kept settles, until the updates are taken', () => {
    const pending = new PendingUpdates(1)
    pending.note('a', 'moved'); pending.note('b', 'moved')
    pending.note('b', 'patch')
    expect(pending.count).toBe(0)
    expect(pillText(pending)).toBe('Many updates · Reload view')
    pending.take()
    expect(pending.overflow).toBe(false)
    expect(pillText(pending)).toBeNull()
    pending.note('a', 'moved'); pending.note('b', 'moved'); pending.clear()
    expect(pending.overflow).toBe(false)
  })
  it('overflows past the cap and says so in the pill', () => {
    const pending = new PendingUpdates()
    for (let i = 0; i < PENDING_CAP; i++) pending.note(`n${i}`, 'new')
    expect(pending.overflow).toBe(false)
    expect(pillText(pending)).toBe(`${PENDING_CAP} updates · Show`)
    pending.note('one-more', 'moved')
    expect(pending.overflow).toBe(true)
    expect(pillText(pending)).toBe('Many updates · Reload view')
    expect(pillText({ count: 1, overflow: false })).toBe('1 update · Show')
    expect(pillText({ count: 0, overflow: false })).toBeNull()
  })
})

describe('canAutoApply', () => {
  const idle: ApplyGuard = { selected: 0, editing: false, menuOpen: false, dialogOpen: false, dragging: false, scrolling: false, hidden: false, lastInputAt: 0, now: 5000 }
  it('applies after 2 s without input when nothing blocks it', () => {
    expect(canAutoApply(idle)).toBe(true)
    expect(canAutoApply({ ...idle, lastInputAt: 3500 })).toBe(false)
    expect(autoApplyDelay({ ...idle, lastInputAt: 3500 })).toBe(500)
    expect(autoApplyDelay({ ...idle, lastInputAt: 3000 })).toBe(0)
  })
  it.each([
    ['a selection', { selected: 1 }], ['an editor', { editing: true }], ['a menu', { menuOpen: true }], ['a dialog', { dialogOpen: true }],
    ['a drag', { dragging: true }], ['a scroll', { scrolling: true }], ['a hidden tab', { hidden: true }],
  ])('waits for %s', (_, blocker) => {
    expect(canAutoApply({ ...idle, ...blocker })).toBe(false)
    expect(autoApplyDelay({ ...idle, ...blocker })).toBeNull()
  })
})

describe('describeFields', () => {
  it('names what a person would name', () => {
    expect(describeFields(['state'])).toBe('status')
    expect(describeFields(['title', 'state', 'fields.priority'])).toBe('title, status and priority')
    expect(describeFields(['fields.custom_thing', 'position'])).toBe('details')
    expect(describeFields(['position'])).toBe('')
  })
})

describe('mergeChanges', () => {
  const change = (over: Partial<NodeChange>): NodeChange => ({ eventId: 1, type: 'node.updated', actorId: 'mira', id: 'n1', projectId: 'A', change: 'updated', fields: [], revision: null, ...over })
  it('adds up the fields and keeps the newer revision', () => {
    const merged = mergeChanges(change({ fields: ['title'], revision: '2026-09-29T10:00:01Z' }), change({ fields: ['fields.priority'], revision: '2026-09-29T10:00:02Z' }))
    expect(merged).toMatchObject({ change: 'updated', fields: ['title', 'fields.priority'], revision: '2026-09-29T10:00:02Z' })
  })
  it('keeps a new node new, ends it with a deletion and brings it back with a restore', () => {
    expect(mergeChanges(change({ change: 'created' }), change({})).change).toBe('created')
    expect(mergeChanges(change({ change: 'created' }), change({ change: 'deleted' })).change).toBe('deleted')
    expect(mergeChanges(change({ change: 'deleted' }), change({ change: 'created' })).change).toBe('created')
    expect(mergeChanges(change({ change: 'deleted' }), change({})).change).toBe('updated')
  })
  it('is only the viewer’s own when every change was', () => {
    expect(mergeChanges(change({ actorId: 'me' }), change({ actorId: 'me' })).actorId).toBe('me')
    expect(mergeChanges(change({ actorId: 'me' }), change({ actorId: 'mira' })).actorId).toBe('')
  })
})

describe('parseNodeChanges', () => {
  it('reads node_changes and skips anything malformed', () => {
    const data = JSON.stringify({
      id: 7, type: 'node.updated', actor_principal_id: 'p',
      node_changes: [
        { id: 'n1', project_id: 'A', change: 'updated', fields: ['state', 3], revision: '2026-09-29T10:00:00Z' },
        { id: 'n2', change: 'exploded' }, null, { id: 'n3', project_id: null, change: 'deleted', fields: null, revision: null },
      ],
    })
    expect(parseNodeChanges(data)).toEqual([
      { eventId: 7, type: 'node.updated', actorId: 'p', id: 'n1', projectId: 'A', change: 'updated', fields: ['state'], revision: '2026-09-29T10:00:00Z' },
      { eventId: 7, type: 'node.updated', actorId: 'p', id: 'n3', projectId: null, change: 'deleted', fields: [], revision: null },
    ])
    expect(parseNodeChanges('{')).toEqual([])
    expect(parseNodeChanges(JSON.stringify({ id: 1, type: 'comment.created' }))).toEqual([])
  })
})

// ---------- The store against a fake stream ----------
class FakeSource {
  static all: FakeSource[] = []
  readyState = 0
  onerror: (() => void) | null = null
  closed = false
  private listeners = new Map<string, ((event: MessageEvent) => void)[]>()
  constructor(readonly url: string) { FakeSource.all.push(this) }
  addEventListener(type: string, listener: (event: MessageEvent) => void) { this.listeners.set(type, [...(this.listeners.get(type) ?? []), listener]) }
  close() { this.closed = true; this.readyState = 2 }
  emit(type: string, data: unknown, id?: number) {
    this.readyState = 1
    const event = { type, data: JSON.stringify(data), lastEventId: id === undefined ? '' : String(id) } as MessageEvent
    for (const listener of this.listeners.get(type) ?? []) listener(event)
  }
  ready(after: number, resumed: boolean) { this.emit('stream.ready', { after, resumed }, after) }
  node(id: number, changes: Partial<NodeChange & { project_id: string }>[], type = 'node.updated', actor = 'someone') {
    this.emit(type, { id, type, actor_principal_id: actor, node_changes: changes.map(c => ({ change: 'updated', fields: ['title'], project_id: 'A', ...c })) }, id)
  }
  fail(fatal: boolean) { if (fatal) this.readyState = 2; else this.readyState = 0; this.onerror?.() }
}
const latest = () => FakeSource.all[FakeSource.all.length - 1]
const node = (id: string, updated_at: string, extra: Partial<WorkNode> = {}): WorkNode =>
  ({ id, key: id.toUpperCase(), kind_id: 'k', title: `Title ${updated_at}`, body: '', fields: {}, state: 'new', parent_id: null, position: '1', created_at: updated_at, updated_at, ...extra })

function view(ids: string[]) {
  const calls: { id: string; node: WorkNode | null | undefined; change: NodeChange }[] = []
  const resyncs: string[] = []
  const v: LiveView = { shows: id => ids.includes(id), changed: (change, n) => calls.push({ id: change.id, node: n, change }), resync: reason => resyncs.push(reason) }
  return { v, calls, resyncs }
}

describe('LiveNodeStore', () => {
  let fetchNode: ReturnType<typeof vi.fn<(id: string) => Promise<WorkNode | null>>>
  let store: LiveNodeStore
  let rows: RowStore
  beforeEach(() => {
    vi.useFakeTimers()
    FakeSource.all = []
    fetchNode = vi.fn<(id: string) => Promise<WorkNode | null>>()
    rows = new RowStore()
    store = new LiveNodeStore({ open: url => new FakeSource(url) as never, fetchNode, graceMs: 1000, retryMs: [100, 200], refetchMs: [10, 20], rows })
  })

  it('forwards a streamed session stop to the list without inventing a node revision', () => {
    const list = view([])
    store.subscribe(list.v)
    latest().ready(40, false)
    latest().emit('harness.stopped', { id: 41, type: 'harness.stopped', after: { ticket_node_id: 'n1', project_id: 'A' } }, 41)
    expect(list.calls).toEqual([{ id: 'n1', node: undefined, change: expect.objectContaining({ fields: ['eta', 'lead_worker'], revision: null }) }])
    expect(fetchNode).not.toHaveBeenCalled()
  })
  afterEach(() => vi.useRealTimers())

  it('opens one live stream for all views and asks them to refetch after connecting', () => {
    const a = view(['n1']), b = view(['n2'])
    store.subscribe(a.v); store.subscribe(b.v)
    expect(FakeSource.all.map(s => s.url)).toEqual(['/api/events/stream?after=latest'])
    expect(store.state).toBe('connecting')
    latest().ready(40, false)
    expect(store.state).toBe('live')
    expect(a.resyncs).toEqual(['initial'])
    expect(b.resyncs).toEqual(['initial'])
  })

  it('refetches a changed node once for every view that shows it and tells the others', async () => {
    const panel = view(['n1']), list = view(['n1', 'n2']), other = view(['n9'])
    for (const x of [panel, list, other]) store.subscribe(x.v)
    latest().ready(40, false)
    fetchNode.mockResolvedValueOnce(node('n1', '2026-09-29T10:00:01Z', { title: 'Renamed' }))
    latest().node(41, [{ id: 'n1', revision: '2026-09-29T10:00:01Z' }])
    await vi.waitFor(() => expect(panel.calls).toHaveLength(1))
    expect(fetchNode).toHaveBeenCalledTimes(1)
    expect(panel.calls[0].node?.title).toBe('Renamed')
    expect(list.calls[0].node?.title).toBe('Renamed')
    expect(other.calls).toEqual([{ id: 'n1', node: undefined, change: expect.objectContaining({ id: 'n1', projectId: 'A', fields: ['title'], eventId: 41 }) }])
    // The row store learned the revision and keeps the copy.
    expect(rows.revision('n1')).toBe('2026-09-29T10:00:01Z')
    expect(rows.isDeleted('n1')).toBe(false)
    expect(rows.latest('n1')?.title).toBe('Renamed')
  })

  it('refetches a kind conversion from the stream and updates its kind projection', async () => {
    const panel = view(['n1'])
    rows.learnKinds([{ id: 'k-epic', slug: 'epic', label: 'Epic' }])
    rows.adoptNode(node('n1', '2026-09-29T10:00:00Z'))
    store.subscribe(panel.v)
    latest().ready(40, false)
    fetchNode.mockResolvedValueOnce(node('n1', '2026-09-29T10:00:01Z', { kind_id: 'k-epic' }))
    latest().node(41, [{ id: 'n1', fields: ['kind_id'], revision: '2026-09-29T10:00:01Z' }], 'node.kind_changed')
    await vi.waitFor(() => expect(panel.calls).toHaveLength(1))
    expect(panel.calls[0].change).toMatchObject({ type: 'node.kind_changed', fields: ['kind_id'] })
    expect(rows.latest('n1')).toMatchObject({ kind_id: 'k-epic', kind_slug: 'epic', kind_label: 'Epic' })
    expect(rows.isOwn('n1', '2026-09-29T10:00:01Z')).toBe(false)
  })

  it('does not refetch a node it already has at that revision (the viewer’s own save)', () => {
    const panel = view(['n1'])
    store.subscribe(panel.v)
    latest().ready(40, false)
    const saved = node('n1', '2026-09-29T10:00:02Z')
    rows.adoptNode(saved, rows.mark())
    latest().node(41, [{ id: 'n1', revision: '2026-09-29T10:00:02Z' }], 'node.updated', 'me')
    expect(fetchNode).not.toHaveBeenCalled()
    expect(panel.calls[0].node).toMatchObject({ id: 'n1', updated_at: saved.updated_at })
    expect(panel.calls[0].change.actorId).toBe('me')
  })

  it('ignores a replayed change older than what it knows', () => {
    const panel = view(['n1'])
    store.subscribe(panel.v)
    rows.adoptNode(node('n1', '2026-09-29T10:00:05Z'))
    latest().node(41, [{ id: 'n1', revision: '2026-09-29T10:00:04Z' }])
    expect(panel.calls).toEqual([])
    expect(fetchNode).not.toHaveBeenCalled()
  })

  it('hands over a deletion without a request and a node that became unreadable as null', async () => {
    const panel = view(['n1', 'n2'])
    store.subscribe(panel.v)
    latest().node(41, [{ id: 'n1', change: 'deleted', fields: ['deleted_at'], revision: '2026-09-29T10:00:03Z' }], 'node.deleted')
    expect(panel.calls[0]).toMatchObject({ id: 'n1', node: null })
    expect(rows.isDeleted('n1')).toBe(true)
    fetchNode.mockResolvedValueOnce(null)
    latest().node(42, [{ id: 'n2', fields: ['project_id'], revision: '2026-09-29T10:00:04Z' }], 'node.project_moved')
    await vi.waitFor(() => expect(panel.calls).toHaveLength(2))
    expect(panel.calls[1]).toMatchObject({ id: 'n2', node: null })
    expect(fetchNode).toHaveBeenCalledTimes(1)
  })

  it('coalesces changes that arrive during a refetch into one more request; the stale answer is not handed on', async () => {
    const panel = view(['n1'])
    store.subscribe(panel.v)
    let release!: (n: WorkNode) => void
    fetchNode.mockImplementationOnce(() => new Promise(resolve => { release = resolve }))
    fetchNode.mockResolvedValueOnce(node('n1', '2026-09-29T10:00:03Z', { title: 'Third' }))
    latest().node(41, [{ id: 'n1', revision: '2026-09-29T10:00:01Z' }])
    latest().node(42, [{ id: 'n1', revision: '2026-09-29T10:00:02Z' }])
    latest().node(43, [{ id: 'n1', revision: '2026-09-29T10:00:03Z' }])
    expect(fetchNode).toHaveBeenCalledTimes(1)
    release(node('n1', '2026-09-29T10:00:01Z', { title: 'First' }))
    await vi.waitFor(() => expect(panel.calls.map(c => c.node?.title)).toEqual(['Third']))
    expect(fetchNode).toHaveBeenCalledTimes(2)
  })

  it('hands on every field and kind of coalesced changes, so ordering changes still classify', async () => {
    const panel = view(['n1'])
    store.subscribe(panel.v)
    let release!: (n: WorkNode) => void
    fetchNode.mockImplementationOnce(() => new Promise(resolve => { release = resolve }))
    latest().node(41, [{ id: 'n1', fields: ['title'], revision: '2026-09-29T10:00:01Z' }])
    latest().node(42, [{ id: 'n1', fields: ['fields.priority'], revision: '2026-09-29T10:00:02Z' }])
    release(node('n1', '2026-09-29T10:00:02Z', { fields: { priority: 'high' } }))
    await vi.waitFor(() => expect(panel.calls).toHaveLength(1))
    const [{ change, node: fresh }] = panel.calls
    expect(change.fields).toEqual(['title', 'fields.priority'])
    expect(classifyChange({ change, shown: { state: 'new' }, node: fresh!, matches: () => true, orderFields: ['fields.priority'] })).toBe('moved')
    expect(fetchNode).toHaveBeenCalledTimes(1)
  })

  it('a read that a deletion overtook never brings the node back', async () => {
    const panel = view(['n1'])
    store.subscribe(panel.v)
    rows.adoptNode(node('n1', '2026-09-29T10:00:00Z'))
    let release!: (n: WorkNode) => void
    fetchNode.mockImplementationOnce(() => new Promise(resolve => { release = resolve }))
    latest().node(41, [{ id: 'n1', revision: '2026-09-29T10:00:01Z' }])
    latest().node(42, [{ id: 'n1', change: 'deleted', fields: ['deleted_at'], revision: '2026-09-29T10:00:02Z' }], 'node.deleted')
    expect(panel.calls.map(c => c.node)).toEqual([null])
    release(node('n1', '2026-09-29T10:00:01Z', { title: 'Stale' }))
    await Promise.resolve(); await Promise.resolve(); await Promise.resolve()
    expect(panel.calls.map(c => c.node)).toEqual([null])
    expect(rows.isDeleted('n1')).toBe(true)
    // A copy a view still holds from before does not bring it back either.
    expect(rows.adoptNode(node('n1', '2026-09-29T10:00:01Z'))).toBeNull()
    expect(rows.isDeleted('n1')).toBe(true)
    // A restore does.
    fetchNode.mockResolvedValueOnce(node('n1', '2026-09-29T10:00:03Z', { title: 'Back' }))
    latest().node(43, [{ id: 'n1', change: 'created', fields: [], revision: '2026-09-29T10:00:03Z' }], 'node.updated')
    await vi.waitFor(() => expect(panel.calls.map(c => c.node?.title ?? null)).toEqual([null, 'Back']))
    expect(rows.isDeleted('n1')).toBe(false)
  })

  it('a refetch that already returned the newest version needs no second request', async () => {
    const panel = view(['n1'])
    store.subscribe(panel.v)
    let release!: (n: WorkNode) => void
    fetchNode.mockImplementationOnce(() => new Promise(resolve => { release = resolve }))
    latest().node(41, [{ id: 'n1', revision: '2026-09-29T10:00:01Z' }])
    latest().node(42, [{ id: 'n1', revision: '2026-09-29T10:00:02Z' }])
    release(node('n1', '2026-09-29T10:00:02Z'))
    await vi.waitFor(() => expect(panel.calls).toHaveLength(1))
    await Promise.resolve(); await Promise.resolve()
    expect(fetchNode).toHaveBeenCalledTimes(1)
  })

  it('a failed refetch leaves the view as it was and tries again after a growing wait', async () => {
    const panel = view(['n1'])
    store.subscribe(panel.v)
    fetchNode.mockRejectedValueOnce(new Error('offline')).mockRejectedValueOnce(new Error('offline'))
    fetchNode.mockResolvedValueOnce(node('n1', '2026-09-29T10:00:02Z', { title: 'Later' }))
    latest().node(41, [{ id: 'n1', fields: ['title'], revision: '2026-09-29T10:00:01Z' }])
    await vi.waitFor(() => expect(fetchNode).toHaveBeenCalledTimes(1))
    await Promise.resolve()
    expect(panel.calls).toEqual([])
    await vi.advanceTimersByTimeAsync(10)
    expect(fetchNode).toHaveBeenCalledTimes(2)
    await vi.advanceTimersByTimeAsync(20)
    expect(fetchNode).toHaveBeenCalledTimes(3)
    expect(panel.calls.map(c => [c.node?.title, c.change.fields])).toEqual([['Later', ['title']]])
  })

  it('after the retries a dirty node waits for a resumed stream, which reads it again', async () => {
    const panel = view(['n1'])
    store.subscribe(panel.v)
    const source = latest()
    source.ready(40, false)
    fetchNode.mockRejectedValue(new Error('offline'))
    source.node(41, [{ id: 'n1', revision: '2026-09-29T10:00:01Z' }])
    await vi.advanceTimersByTimeAsync(1000)
    // One read and one retry per wait, then nothing more.
    expect(fetchNode).toHaveBeenCalledTimes(3)
    fetchNode.mockReset()
    fetchNode.mockResolvedValue(node('n1', '2026-09-29T10:00:01Z', { title: 'Back online' }))
    source.fail(false)
    source.ready(41, true)
    await vi.waitFor(() => expect(panel.calls.map(c => c.node?.title)).toEqual(['Back online']))
    expect(panel.resyncs).toEqual(['initial'])
  })

  it('a gap asks the views to read again and forgets dirty nodes', async () => {
    const panel = view(['n1'])
    store.subscribe(panel.v)
    const source = latest()
    source.ready(40, false)
    fetchNode.mockRejectedValue(new Error('offline'))
    source.node(41, [{ id: 'n1', revision: '2026-09-29T10:00:01Z' }])
    await vi.waitFor(() => expect(fetchNode).toHaveBeenCalledTimes(1))
    source.fail(false)
    source.ready(900, false)
    expect(panel.resyncs).toEqual(['initial', 'gap'])
    await vi.advanceTimersByTimeAsync(1000)
    expect(fetchNode).toHaveBeenCalledTimes(1)
  })

  it('AEON-385: stream loss distrusts reads immediately; resume replays events and restart resyncs', () => {
    const panel = view(['n1'])
    store.subscribe(panel.v)
    const source = latest()
    source.ready(40, false)
    fetchNode.mockResolvedValue(node('n1', '2026-09-29T10:00:01Z'))
    source.node(41, [{ id: 'n1', revision: '2026-09-29T10:00:01Z' }])
    const sent = rows.mark()
    source.fail(false)
    expect(rows.gapSince(sent)).toBe(true)
    expect(rows.current('n1')).toBe(false)
    expect(store.state).toBe('reconnecting')
    // The browser last saw 44 (an event the store does not listen to).
    source.ready(44, true)
    expect(store.state).toBe('live')
    expect(panel.resyncs).toEqual(['initial'])
    source.fail(false)
    source.ready(900, false)
    expect(panel.resyncs).toEqual(['initial', 'gap'])
    // What was read before the restart is no longer trusted as current.
    expect(rows.current('n1')).toBe(false)
  })

  it('reopens a stream the browser gave up on after the last event it saw', () => {
    const panel = view(['n1'])
    store.subscribe(panel.v)
    latest().ready(40, false)
    latest().node(45, [{ id: 'n9', revision: '2026-09-29T10:00:01Z' }])
    latest().fail(true)
    expect(latest().closed).toBe(true)
    expect(FakeSource.all).toHaveLength(1)
    vi.advanceTimersByTime(100)
    expect(latest().url).toBe('/api/events/stream?after=45')
    latest().fail(true)
    vi.advanceTimersByTime(199)
    expect(FakeSource.all).toHaveLength(2)
    vi.advanceTimersByTime(1)
    expect(FakeSource.all).toHaveLength(3)
    latest().ready(45, true)
    expect(panel.resyncs).toEqual(['initial'])
  })

  it('closes the stream a while after the last view leaves and starts fresh later', () => {
    const a = view(['n1']), b = view(['n2'])
    const leaveA = store.subscribe(a.v)
    latest().ready(10, false)
    leaveA()
    const leaveB = store.subscribe(b.v)
    leaveB()
    vi.advanceTimersByTime(999)
    expect(latest().closed).toBe(false)
    // Switching tickets within the grace keeps the stream.
    const leaveAgain = store.subscribe(a.v)
    vi.advanceTimersByTime(2000)
    expect(latest().closed).toBe(false)
    leaveAgain()
    vi.advanceTimersByTime(1000)
    expect(latest().closed).toBe(true)
    expect(store.state).toBe('off')
    store.subscribe(a.v)
    expect(latest().url).toBe('/api/events/stream?after=latest')
  })

  it('stays off where there is no EventSource', () => {
    const offline = new LiveNodeStore({ open: () => null, fetchNode, rows })
    offline.subscribe(view(['n1']).v)
    expect(offline.state).toBe('off')
  })

  it('clock-gap wake reconnects, invalidates pre-sleep pages and fully resyncs every view', () => {
    const list = view([]), outline = view([])
    store.subscribe(list.v); store.subscribe(outline.v)
    const sleeping = latest()
    sleeping.ready(40, false)
    const sent = rows.mark()
    vi.setSystemTime(Date.now() + 12 * 60_000)
    vi.advanceTimersByTime(5_000)
    expect(sleeping.closed).toBe(true)
    expect(latest().url).toBe('/api/events/stream?after=latest')
    expect(rows.gapSince(sent)).toBe(true)
    expect(list.resyncs).toEqual(['initial', 'gap'])
    expect(outline.resyncs).toEqual(['initial', 'gap'])
    // Late events from the retired source cannot revive it or apply old state.
    sleeping.node(41, [{ id: 'n1', revision: '2026-09-29T10:00:01Z' }])
    expect(list.calls).toEqual([])
    expect(store.state).toBe('reconnecting')
    latest().ready(50, false)
    expect(store.state).toBe('live')
  })

  it('pings keep a quiet stream alive without row reads; missing pings reconnect and resync at 45s', () => {
    const list = view([])
    store.subscribe(list.v)
    const source = latest()
    source.ready(40, false)
    for (let i = 0; i < 4; i++) {
      vi.advanceTimersByTime(15_000)
      source.emit('stream.ping', {})
    }
    expect(source.closed).toBe(false)
    expect(list.resyncs).toEqual(['initial'])
    expect(fetchNode).not.toHaveBeenCalled()
    vi.advanceTimersByTime(44_999)
    expect(source.closed).toBe(false)
    vi.advanceTimersByTime(1)
    expect(source.closed).toBe(true)
    expect(list.resyncs).toEqual(['initial', 'gap'])
    expect(latest().url).toBe('/api/events/stream?after=latest')
  })

  it('fetch() reads any node for a view and keeps it', async () => {
    fetchNode.mockResolvedValueOnce(node('n5', '2026-09-29T10:00:01Z'))
    expect((await store.fetch('n5'))?.id).toBe('n5')
    expect(rows.latest('n5')?.id).toBe('n5')
  })
})
