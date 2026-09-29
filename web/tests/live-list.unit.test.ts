// SPDX-License-Identifier: AGPL-3.0-only
// AEON-326 slice 1b: the project ticket list, live. Changes reach it through
// the live node store; it refetches the changed rows through its own query
// (ids plus every filter), patches field changes in place and holds structural
// ones behind the pill until Show or a safe moment.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { effectScope, nextTick, ref, type EffectScope } from 'vue'
import type { ListItem, ListPage, ListQuery } from '../src/lib/api'
import { LiveNodeStore, type NodeChange } from '../src/lib/liveNodes'
import { PENDING_CAP } from '../src/lib/liveUpdates'
import { filtersFromQuery, type ListFilters } from '../src/lib/ticketList'
import { placeKey, useLiveList, type LiveList, type LiveListBlockers } from '../src/lib/useLiveList'

const ME = 'me-1', MIRA = 'mira-2', PROJECT = 'p-1'
let clock = 0
const at = (minutes: number) => new Date(Date.parse('2026-09-29T10:00:00Z') + minutes * 60_000).toISOString()
function item(id: string, over: Partial<ListItem> = {}): ListItem {
  return {
    id, key: `K-${id.slice(1)}`, kind_id: 'k-ticket', title: `Ticket ${id}`, body: '', fields: {}, state: 'new', parent_id: PROJECT, position: '0',
    created_at: at(0), updated_at: at(1), deleted_at: null, kind_slug: 'ticket', kind_label: 'Ticket', priority: null, assignee: null,
    parent: { id: PROJECT, key: 'PRJ', title: 'Project', kind_slug: 'project' }, children_count: 0, project: { id: PROJECT, key: 'PRJ', title: 'Project' }, ...over,
  }
}
const CLOSED = ['done', 'cancelled', 'archived']

// The server: the list API over an array, with within, kind, ids, hide_closed and a priority sort.
function server(nodes: ListItem[]) {
  const calls: ListQuery[] = []
  const fetchList = vi.fn(async (query: ListQuery): Promise<ListPage> => {
    calls.push(query)
    let items = nodes.filter(node => node.project?.id === query.within)
      .filter(node => !query.ids || query.ids.includes(node.id))
      .filter(node => !query.hide_closed || !CLOSED.includes(node.state))
      .filter(node => !query.priority?.length || query.priority.includes(node.priority ?? 'none'))
    items = [...items].sort((a, b) => b.updated_at.localeCompare(a.updated_at))
    return { items: items.slice(0, query.limit ?? 50).map(node => structuredClone(node)), next_cursor: null }
  })
  return { nodes, calls, fetchList }
}

interface Harness {
  live: LiveList; rows: ReturnType<typeof ref<ListItem[]>>; store: LiveNodeStore; srv: ReturnType<typeof server>
  blockers: LiveListBlockers; loading: ReturnType<typeof ref<boolean>>; loads: ReturnType<typeof ref<number>>
  filters: ReturnType<typeof ref<ListFilters>>; reload: ReturnType<typeof vi.fn>; applied: ReturnType<typeof vi.fn>
  send(change: Partial<NodeChange> & Pick<NodeChange, 'id'>): void
  settle(): Promise<void>
}
let scope: EffectScope | undefined
function setup(initial: ListItem[], query: Record<string, string> = {}, extra: ListItem[] = []): Harness {
  const srv = server([...initial.map(row => structuredClone(row)), ...extra])
  const store = new LiveNodeStore({ open: () => null })
  const rows = ref<ListItem[]>(initial)
  const filters = ref(filtersFromQuery(query))
  const loading = ref(false), loads = ref(1)
  const blockers: LiveListBlockers = { selected: 0, editing: false, menuOpen: false, dialogOpen: false, dragging: false }
  const reload = vi.fn(), applied = vi.fn()
  scope = effectScope()
  const live = scope.run(() => useLiveList({
    projectId: ref(PROJECT), filters, rows, loading, loads, loadedOnce: ref(true), more: () => false, active: ref(true),
    me: () => ME, blockers: () => blockers, reload, applied, store, fetchList: srv.fetchList,
    env: { now: () => clock, hidden: () => false, listen: () => () => {} },
  }))!
  let event = 1000
  return {
    live, rows, store, srv, blockers, loading, loads, filters, reload, applied,
    send(change) {
      store.apply({ eventId: ++event, type: 'node.updated', actorId: MIRA, projectId: PROJECT, change: 'updated', fields: [], revision: null, ...change })
    },
    async settle() { await live.flushNow(); await nextTick() },
  }
}
// The server's copy of a node changes, as someone else's save would.
function edit(h: Harness, id: string, patch: Partial<ListItem>) {
  const node = h.srv.nodes.find(n => n.id === id)!
  Object.assign(node, patch, { updated_at: at(10 + h.srv.calls.length) })
  return node
}

beforeEach(() => { clock = 0; vi.useFakeTimers() })
afterEach(() => { scope?.stop(); scope = undefined; vi.useRealTimers() })

describe('useLiveList: field changes', () => {
  it('patches the row object in place, tints it and says so politely', async () => {
    const h = setup([item('n1'), item('n2')])
    const row = h.rows.value![1]
    const node = edit(h, 'n2', { priority: 'high', fields: { priority: 'high' } })
    h.send({ id: 'n2', fields: ['fields.priority'], revision: node.updated_at })
    await h.settle()
    expect(h.rows.value![1]).toBe(row)
    expect(row.priority).toBe('high')
    expect(h.live.flash.value.has('n2')).toBe(true)
    expect(h.live.message.value).toBe('K-2 was updated elsewhere: priority.')
    expect(h.live.pill.value).toBeNull()
    // The tint is brief.
    vi.advanceTimersByTime(2000)
    expect(h.live.flash.value.has('n2')).toBe(false)
  })

  it('refetches a burst of changes through the list query in one request, with the filters', async () => {
    const h = setup([item('n1'), item('n2'), item('n3')])
    for (const id of ['n1', 'n2', 'n3']) { const node = edit(h, id, { title: `Renamed ${id}` }); h.send({ id, fields: ['title'], revision: node.updated_at }) }
    await h.settle()
    expect(h.srv.fetchList).toHaveBeenCalledTimes(1)
    expect(h.srv.calls[0]).toMatchObject({ within: PROJECT, hide_closed: true, ids: ['n1', 'n2', 'n3'] })
    expect(h.rows.value!.map(row => row.title)).toEqual(['Renamed n1', 'Renamed n2', 'Renamed n3'])
    expect(h.live.message.value).toBe('3 tickets changed elsewhere.')
  })

  it('leaves the person’s own changes to the code that made them', async () => {
    const h = setup([item('n1')])
    h.send({ id: 'n1', actorId: ME, fields: ['title'] })
    await h.settle()
    expect(h.srv.fetchList).not.toHaveBeenCalled()
  })
})

describe('useLiveList: structural changes wait', () => {
  it('keeps a row closed elsewhere in place, dimmed and labelled, and Show removes it', async () => {
    const h = setup([item('n1', { state: 'backlog' }), item('n2', { state: 'backlog' })], { group: 'status' })
    const row = h.rows.value![0]
    const node = edit(h, 'n1', { state: 'cancelled' })
    h.send({ id: 'n1', fields: ['state'], revision: node.updated_at })
    // The row keeps its group from the moment the change arrives.
    expect(h.live.layout(row).state).toBe('backlog')
    await h.settle()
    // Two requests: the filtered one leaves it out, the unfiltered one finds it closed.
    expect(h.srv.calls.map(q => q.hide_closed ?? false)).toEqual([true, false])
    expect(row.state).toBe('cancelled')
    expect(h.live.layout(row).state).toBe('backlog')
    expect(h.live.labels.value.get('n1')).toBe('Closed')
    expect(h.live.pill.value).toBe('1 update · Show')
    expect(h.live.message.value).toBe('K-1 was closed elsewhere. Press U to show updates.')
    // The counts wait with the row.
    expect(h.applied).not.toHaveBeenCalled()
    h.live.apply()
    expect(h.rows.value!.map(r => r.id)).toEqual(['n2'])
    expect(h.live.pill.value).toBeNull()
    expect(h.applied).toHaveBeenCalled()
  })

  it('marks a row that no longer matches for another reason, and a deleted one, and reports selected deletions', async () => {
    const h = setup([item('n1', { priority: 'high' }), item('n2', { priority: 'high' }), item('n3', { priority: 'high' })], { priority: 'high' })
    const node = edit(h, 'n1', { priority: 'low', fields: { priority: 'low' } })
    h.send({ id: 'n1', fields: ['fields.priority'], revision: node.updated_at })
    h.srv.nodes.splice(h.srv.nodes.findIndex(n => n.id === 'n2'), 1)
    h.send({ id: 'n2', change: 'deleted', fields: ['deleted_at'] })
    await h.settle()
    expect(h.live.labels.value.get('n1')).toBe('No longer matches')
    expect(h.live.labels.value.get('n2')).toBe('Deleted')
    expect(h.live.deletedAmong(['n2', 'n3'])).toEqual(['n2'])
    expect(h.live.pill.value).toBe('2 updates · Show')
  })

  it('reads a move to another project as no longer matching', async () => {
    const h = setup([item('n1')])
    h.srv.nodes.splice(0, 1)
    h.send({ id: 'n1', fields: ['project_id', 'parent_id'], projectId: 'p-other' })
    await h.settle()
    expect(h.live.labels.value.get('n1')).toBe('No longer matches')
  })

  it('holds a regrouped row as moved and places it on Show', async () => {
    const h = setup([item('n1', { state: 'backlog' }), item('n2', { state: 'backlog' })], { group: 'status' })
    const row = h.rows.value![1]
    const node = edit(h, 'n2', { state: 'in_progress' })
    h.send({ id: 'n2', fields: ['state'], revision: node.updated_at })
    await h.settle()
    expect(row.state).toBe('in_progress')
    expect(h.live.labels.value.get('n2')).toBe('Moved')
    expect(h.live.layout(row).state).toBe('backlog')
    h.live.apply()
    expect(h.live.layout(row).state).toBe('in_progress')
    // Grouped by status the list sorts by status first: it now follows the backlog row.
    expect(h.rows.value!.map(r => r.id)).toEqual(['n1', 'n2'])
  })

  it('offers a new matching row and adds it where it sorts on Show', async () => {
    const fresh = item('n9', { updated_at: at(30) })
    const h = setup([item('n1'), item('n2')], {}, [fresh])
    h.send({ id: 'n9', change: 'created' })
    await h.settle()
    expect(h.live.pill.value).toBe('1 update · Show')
    expect(h.live.labels.value.size).toBe(0)
    expect(h.live.message.value).toBe('K-9 was added to this view. Press U to show updates.')
    h.live.apply()
    expect(h.rows.value!.map(r => r.id)).toEqual(['n9', 'n1', 'n2'])
  })

  it('settles a waiting row that matches again (reopened by the person)', async () => {
    const h = setup([item('n1', { state: 'backlog' })])
    let node = edit(h, 'n1', { state: 'cancelled' })
    h.send({ id: 'n1', fields: ['state'], revision: node.updated_at })
    await h.settle()
    expect(h.live.pill.value).toBe('1 update · Show')
    node = edit(h, 'n1', { state: 'backlog' })
    h.send({ id: 'n1', actorId: ME, fields: ['state'], revision: node.updated_at })
    await h.settle()
    expect(h.live.pill.value).toBeNull()
    expect(h.live.labels.value.size).toBe(0)
  })

  it('offers to reload the view past the cap instead of applying', async () => {
    const many = Array.from({ length: PENDING_CAP + 1 }, (_, i) => item(`n${i + 1}`, { state: 'backlog' }))
    const h = setup(many)
    for (const row of many) { edit(h, row.id, { state: 'cancelled' }); h.send({ id: row.id, fields: ['state'] }) }
    await h.settle()
    expect(h.live.pill.value).toBe('Many updates · Reload view')
    h.live.apply()
    expect(h.reload).toHaveBeenCalledTimes(1)
    expect(h.live.pill.value).toBeNull()
  })
})

describe('useLiveList: applying by itself only when it is safe', () => {
  async function closedRow(h: Harness) {
    const node = edit(h, 'n1', { state: 'cancelled' })
    h.send({ id: 'n1', fields: ['state'], revision: node.updated_at })
    await h.settle()
    expect(h.live.pill.value).toBe('1 update · Show')
  }
  it('waits 2 s after the mark, then applies with nothing going on', async () => {
    const h = setup([item('n1'), item('n2')])
    await closedRow(h)
    clock += 1500; h.live.checkNow()
    expect(h.live.pill.value).toBe('1 update · Show')
    clock += 600; h.live.checkNow()
    expect(h.live.pill.value).toBeNull()
    expect(h.rows.value!.map(r => r.id)).toEqual(['n2'])
  })
  it.each(['selected', 'editing', 'menuOpen', 'dialogOpen'] as const)('waits while %s', async blocker => {
    const h = setup([item('n1'), item('n2')])
    await closedRow(h)
    Object.assign(h.blockers, { [blocker]: blocker === 'selected' ? 1 : true })
    clock += 60_000; h.live.checkNow()
    expect(h.live.pill.value).toBe('1 update · Show')
    // Released: it applies at the next check.
    Object.assign(h.blockers, { [blocker]: blocker === 'selected' ? 0 : false })
    h.live.checkNow()
    expect(h.live.pill.value).toBeNull()
  })
})

describe('useLiveList: loads and gaps', () => {
  it('looks at a change that arrived during a load against the new rows', async () => {
    const h = setup([item('n1')])
    h.loading.value = true
    const node = edit(h, 'n1', { title: 'After the load' })
    h.send({ id: 'n1', fields: ['title'], revision: node.updated_at })
    await h.settle()
    expect(h.srv.fetchList).not.toHaveBeenCalled()
    // The load lands with the older copy; the change is then read again.
    h.rows.value = [item('n1')]
    h.loads.value++
    h.loading.value = false
    await nextTick()
    await h.settle()
    expect(h.rows.value[0].title).toBe('After the load')
  })

  it('after a gap reads the first page again: a row added meanwhile waits as new', async () => {
    const h = setup([item('n1'), item('n2')], {}, [item('n7', { updated_at: at(20) })])
    // The store asks every view to read again after a gap it could not bridge.
    ;(h.store as unknown as { views: Set<{ resync?: (r: string) => void }> }).views.forEach(view => view.resync?.('gap'))
    await vi.waitFor(() => expect(h.srv.fetchList).toHaveBeenCalled())
    await h.settle()
    expect(h.live.pill.value).toBe('1 update · Show')
    expect(h.rows.value!.map(r => r.id)).toEqual(['n1', 'n2'])
  })

  it('places a row by the order fields and the group, never by its update time', () => {
    const filters = filtersFromQuery({ group: 'priority' })
    const a = item('n1', { priority: 'high' })
    expect(placeKey({ ...a, updated_at: at(99) }, filters)).toBe(placeKey(a, filters))
    expect(placeKey({ ...a, priority: 'low' }, filters)).not.toBe(placeKey(a, filters))
    expect(placeKey({ ...a, title: 'Other' }, filters)).toBe(placeKey(a, filters))
    expect(placeKey({ ...a, title: 'Other' }, filtersFromQuery({ sort: 'title' }))).not.toBe(placeKey(a, filtersFromQuery({ sort: 'title' })))
  })
})
