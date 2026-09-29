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
import { RowStore } from '../src/lib/rowStore'
import { filtersFromQuery, type ListFilters } from '../src/lib/ticketList'
import { BATCH_MS, placeKey, RETRY_MS, useLiveList, type ListRead, type LiveList, type LiveListBlockers, type LiveListOptions } from '../src/lib/useLiveList'

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
  live: LiveList; rows: ReturnType<typeof ref<ListItem[]>>; store: LiveNodeStore; nodes: RowStore; srv: ReturnType<typeof server>
  blockers: LiveListBlockers; loading: ReturnType<typeof ref<boolean>>; reads: ReturnType<typeof ref<ListRead | null>>
  filters: ReturnType<typeof ref<ListFilters>>; reload: ReturnType<typeof vi.fn>; applied: ReturnType<typeof vi.fn>
  // Rows the person works with (selected, open in an editor).
  holding: Set<string>
  send(change: Partial<NodeChange> & Pick<NodeChange, 'id'>): void
  views(): { resync?: (reason: 'initial' | 'gap') => void; resumed?: () => void }[]
  // The stream could not bridge a gap: the store stops trusting what was read, and views read again.
  gap(): void
  // A load of the list, as useTicketList makes it: sent now, landing with these items.
  load(items: ListItem[], sent?: number): void
  settle(): Promise<void>
}
let scope: EffectScope | undefined
function setup(initial: ListItem[], query: Record<string, string> = {}, extra: ListItem[] = [], more: Partial<LiveListOptions> = {}): Harness {
  const srv = server([...initial.map(row => structuredClone(row)), ...extra])
  const nodes = new RowStore()
  const store = new LiveNodeStore({ open: () => null, rows: nodes })
  // The rows are the store's display objects, as useTicketList loads them.
  const loaded = nodes.mark()
  const rows = ref<ListItem[]>(initial.map(row => nodes.adopt(row, loaded, { show: true })!))
  const filters = ref(filtersFromQuery(query))
  const loading = ref(false), reads = ref<ListRead | null>({ kind: 'load', behind: [] })
  const blockers: LiveListBlockers = { selected: 0, editing: false, menuOpen: false, dialogOpen: false, dragging: false }
  const reload = vi.fn(), applied = vi.fn()
  const holding = new Set<string>()
  scope = effectScope()
  const live = scope.run(() => useLiveList({
    projectId: ref(PROJECT), filters, rows, loading, reads, loadedOnce: ref(true), more: () => false, active: ref(true),
    me: () => ME, blockers: () => blockers, reload, applied, store, fetchList: srv.fetchList, holds: id => holding.has(id),
    env: { now: () => clock, hidden: () => false, listen: () => () => {} }, ...more,
  }))!
  let event = 1000
  const views = () => [...(store as unknown as { views: Set<{ resync?: (reason: 'initial' | 'gap') => void; resumed?: () => void }> }).views]
  return {
    live, rows, store, nodes, srv, blockers, loading, reads, filters, reload, applied, holding, views,
    send(change) {
      store.apply({ eventId: ++event, type: 'node.updated', actorId: MIRA, projectId: PROJECT, change: 'updated', fields: [], revision: null, ...change })
    },
    gap() { nodes.gap(); for (const view of views()) view.resync?.('gap') },
    load(items, sent = nodes.mark()) {
      const behind: string[] = []
      rows.value = items.flatMap(copy => {
        const row = nodes.adopt(copy, sent, { show: true })
        if (row && nodes.newer(copy.id, copy.updated_at)) behind.push(copy.id)
        return row ? [row] : []
      })
      reads.value = { kind: 'load', behind }
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

  it('refetches one batch at a time, so an older answer never lands after a newer one', async () => {
    const h = setup([item('n1')])
    let release!: () => void
    const slow = new Promise<void>(resolve => { release = resolve })
    const plain = h.srv.fetchList.getMockImplementation()!
    h.srv.fetchList.mockImplementationOnce(async query => { await slow; return plain(query) })
    let node = edit(h, 'n1', { title: 'First' })
    h.send({ id: 'n1', fields: ['title'], revision: node.updated_at })
    await vi.advanceTimersByTimeAsync(150)
    node = edit(h, 'n1', { title: 'Second' })
    h.send({ id: 'n1', fields: ['title'], revision: node.updated_at })
    await vi.advanceTimersByTimeAsync(150)
    expect(h.srv.fetchList).toHaveBeenCalledTimes(1)
    release()
    await vi.advanceTimersByTimeAsync(300)
    // The first answer already carries the second change: no second request.
    expect(h.srv.fetchList).toHaveBeenCalledTimes(1)
    expect(h.rows.value![0].title).toBe('Second')
  })

  it('a failed read keeps the rows waiting and reads them again after a growing wait', async () => {
    const h = setup([item('n1')])
    const plain = h.srv.fetchList.getMockImplementation()!
    h.srv.fetchList.mockRejectedValueOnce(new Error('offline')).mockRejectedValueOnce(new Error('offline'))
    const node = edit(h, 'n1', { title: 'Read at last' })
    h.send({ id: 'n1', fields: ['title'], revision: node.updated_at })
    await vi.advanceTimersByTimeAsync(150)
    expect(h.srv.fetchList).toHaveBeenCalledTimes(1)
    expect(h.rows.value![0].title).toBe('Ticket n1')
    await vi.advanceTimersByTimeAsync(RETRY_MS[0] + 150)
    expect(h.srv.fetchList).toHaveBeenCalledTimes(2)
    h.srv.fetchList.mockImplementation(plain)
    await vi.advanceTimersByTimeAsync(RETRY_MS[1] + 150)
    expect(h.srv.fetchList).toHaveBeenCalledTimes(3)
    expect(h.rows.value![0].title).toBe('Read at last')
    expect(h.live.message.value).toBe('K-1 was updated elsewhere: title.')
  })

  it('after the retries the rows wait for a resumed stream', async () => {
    const h = setup([item('n1')])
    const plain = h.srv.fetchList.getMockImplementation()!
    h.srv.fetchList.mockRejectedValue(new Error('offline'))
    const node = edit(h, 'n1', { title: 'After the resume' })
    h.send({ id: 'n1', fields: ['title'], revision: node.updated_at })
    await vi.advanceTimersByTimeAsync(RETRY_MS.reduce((a, b) => a + b + 150, 150) + 60_000)
    expect(h.srv.fetchList).toHaveBeenCalledTimes(RETRY_MS.length + 1)
    h.srv.fetchList.mockImplementation(plain)
    for (const view of h.views()) view.resumed?.()
    await vi.advanceTimersByTimeAsync(150)
    expect(h.rows.value![0].title).toBe('After the resume')
  })

  it('an answer a deletion overtook is not used: the row is marked deleted, never patched', async () => {
    const h = setup([item('n1'), item('n2')])
    let release!: () => void
    const slow = new Promise<void>(resolve => { release = resolve })
    const plain = h.srv.fetchList.getMockImplementation()!
    const node = edit(h, 'n1', { title: 'Before the deletion' })
    const answer = plain({ within: PROJECT, ids: ['n1'], hide_closed: true })
    h.srv.fetchList.mockImplementationOnce(async () => { await slow; return answer })
    h.send({ id: 'n1', fields: ['title'], revision: node.updated_at })
    await vi.advanceTimersByTimeAsync(150)
    h.srv.nodes.splice(0, 1)
    h.send({ id: 'n1', change: 'deleted', fields: ['deleted_at'] })
    release()
    await vi.advanceTimersByTimeAsync(300)
    expect(h.rows.value![0].title).toBe('Ticket n1')
    expect(h.live.labels.value.get('n1')).toBe('Deleted')
  })

  it('a deletion and a restore before the list looked leave the row as it is', async () => {
    const h = setup([item('n1')])
    const node = edit(h, 'n1', { title: 'Restored' })
    h.send({ id: 'n1', change: 'deleted', fields: ['deleted_at'] })
    h.send({ id: 'n1', change: 'created', fields: [], revision: node.updated_at })
    await h.settle()
    expect(h.live.labels.value.size).toBe(0)
    expect(h.live.pill.value).toBeNull()
    expect(h.rows.value![0].title).toBe('Restored')
  })

  it('leaves the changes this tab made to the code that made them', async () => {
    const h = setup([item('n1')])
    const node = edit(h, 'n1', { title: 'Mine' })
    h.nodes.wrote(node)
    h.send({ id: 'n1', actorId: ME, fields: ['title'], revision: node.updated_at })
    await h.settle()
    expect(h.srv.fetchList).not.toHaveBeenCalled()
  })

  it('follows the same person’s changes from another tab', async () => {
    const h = setup([item('n1')])
    const node = edit(h, 'n1', { title: 'From my other tab' })
    // This tab never wrote that revision: the actor alone proves nothing.
    h.send({ id: 'n1', actorId: ME, fields: ['title'], revision: node.updated_at })
    await h.settle()
    expect(h.srv.fetchList).toHaveBeenCalledTimes(1)
    expect(h.rows.value![0].title).toBe('From my other tab')
    expect(h.live.message.value).toBe('K-1 was updated elsewhere: title.')
  })

  it('a write of this tab whose answer lands after its event is still its own: nothing waits or is said', async () => {
    const h = setup([item('n1', { state: 'backlog' }), item('n2')])
    const node = edit(h, 'n1', { state: 'cancelled' })
    h.send({ id: 'n1', actorId: ME, fields: ['state'], revision: node.updated_at })
    // The answer of the write comes back while the list waits to read.
    h.nodes.wrote(node)
    expect(h.rows.value![0].state).toBe('cancelled')
    await h.settle()
    expect(h.live.pill.value).toBeNull()
    expect(h.live.labels.value.size).toBe(0)
    expect(h.live.message.value).toBe('')
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

  it('a selected row, or one open in an editor, keeps the version the person sees until Show', async () => {
    const h = setup([item('n1'), item('n2')])
    const selected = h.rows.value![0], seen = selected.updated_at
    h.holding.add('n1')
    const node = edit(h, 'n1', { priority: 'high', fields: { priority: 'high' } })
    h.send({ id: 'n1', fields: ['fields.priority'], revision: node.updated_at })
    await h.settle()
    // Not patched: a bulk change or a save still sends the revision the person saw.
    expect(selected.priority).toBeNull()
    expect(selected.updated_at).toBe(seen)
    expect(h.live.labels.value.get('n1')).toBe('Changed')
    expect(h.live.pill.value).toBe('1 update · Show')
    expect(h.live.message.value).toBe('K-1 was updated elsewhere: priority. Press U to show updates.')
    expect(h.live.flash.value.has('n1')).toBe(false)
    // A row nobody works with patches in place as before.
    const other = edit(h, 'n2', { title: 'In place' })
    h.send({ id: 'n2', fields: ['title'], revision: other.updated_at })
    await h.settle()
    expect(h.rows.value![1].title).toBe('In place')
    // Show brings the newer version in, tinted.
    h.live.apply()
    expect(selected.priority).toBe('high')
    expect(selected.updated_at).toBe(node.updated_at)
    expect(h.live.flash.value.has('n1')).toBe(true)
    expect(h.live.labels.value.size).toBe(0)
  })

  it('Show leaves the row an editor pins as it is: the editor still saves against the copy it started from', async () => {
    const h = setup([item('n1'), item('n2')])
    const row = h.rows.value![0], seen = row.updated_at
    const editor = h.nodes.edit('n1')!
    h.holding.add('n1')
    const node = edit(h, 'n1', { title: 'Theirs' })
    h.send({ id: 'n1', fields: ['title'], revision: node.updated_at })
    await h.settle()
    expect(h.live.labels.value.get('n1')).toBe('Changed')
    expect(h.live.pill.value).toBe('1 update · Show')
    h.live.apply()
    // The editor takes the newer version when it closes, or meets it as a conflict when it saves.
    expect(row.updated_at).toBe(seen)
    expect(row.title).toBe('Ticket n1')
    expect(editor.revision).toBe(seen)
    expect(h.live.pill.value).toBeNull()
    expect(h.live.labels.value.size).toBe(0)
    h.live.checkNow()
    expect(row.updated_at).toBe(seen)
    // Closed without taking it: the row catches up.
    editor.end(); h.holding.delete('n1')
    h.live.checkNow()
    expect(row.title).toBe('Theirs')
    expect(row.updated_at).toBe(node.updated_at)
    expect(h.live.flash.value.has('n1')).toBe(true)
  })

  it('a row an editor pins waits as changed even when nothing else holds it', async () => {
    const h = setup([item('n1'), item('n2')])
    const row = h.rows.value![0], seen = row.updated_at
    const editor = h.nodes.edit('n1')!
    const node = edit(h, 'n1', { title: 'Remote' })
    h.send({ id: 'n1', fields: ['title'], revision: node.updated_at })
    await h.settle()
    expect(h.live.labels.value.get('n1')).toBe('Changed')
    expect(row.updated_at).toBe(seen)
    editor.end()
    h.live.apply()
    expect(row.title).toBe('Remote')
  })

  it('an editor that saved on top of the newer version leaves nothing to catch up', async () => {
    const h = setup([item('n1')])
    const row = h.rows.value![0]
    const editor = h.nodes.edit('n1')!
    h.holding.add('n1')
    const node = edit(h, 'n1', { title: 'Theirs' })
    h.send({ id: 'n1', fields: ['title'], revision: node.updated_at })
    await h.settle()
    h.live.apply()
    // The save met the change as a conflict, and was saved again on top of it.
    editor.rebase()
    const saved = edit(h, 'n1', { title: 'Mine, on top of theirs' })
    h.nodes.wrote(saved)
    editor.saved(saved)
    editor.end(); h.holding.delete('n1')
    h.live.checkNow()
    expect(row.title).toBe('Mine, on top of theirs')
    expect(h.live.flash.value.has('n1')).toBe(false)
  })

  it('a held row that someone else closed keeps its values until it leaves', async () => {
    const h = setup([item('n1', { state: 'backlog' }), item('n2')])
    const row = h.rows.value![0], seen = row.updated_at
    h.holding.add('n1')
    const node = edit(h, 'n1', { state: 'cancelled' })
    h.send({ id: 'n1', fields: ['state'], revision: node.updated_at })
    await h.settle()
    expect(h.live.labels.value.get('n1')).toBe('Closed')
    expect(row.state).toBe('backlog')
    expect(row.updated_at).toBe(seen)
  })

  it('a newer version already on the row (the panel saved it) is not replaced by an older held one', async () => {
    const h = setup([item('n1')])
    const row = h.rows.value![0]
    h.holding.add('n1')
    const node = edit(h, 'n1', { title: 'Theirs' })
    h.send({ id: 'n1', fields: ['title'], revision: node.updated_at })
    await h.settle()
    // The panel saves after the conflict: its answer is the newest copy.
    h.nodes.wrote(edit(h, 'n1', { title: 'Mine, after the conflict' }))
    h.live.apply()
    expect(row.title).toBe('Mine, after the conflict')
  })

  it('keeps no more than the cap of new rows past it, and still names selected deletions', async () => {
    const shown = [item('n1'), item('n2')]
    const extra = Array.from({ length: PENDING_CAP + 50 }, (_, i) => item(`x${i}`, { updated_at: at(40 + i) }))
    const h = setup(shown, {}, extra)
    h.srv.nodes.splice(h.srv.nodes.findIndex(n => n.id === 'n1'), 1)
    h.send({ id: 'n1', change: 'deleted', fields: ['deleted_at'] })
    await h.settle()
    for (const row of extra) h.send({ id: row.id, change: 'created' })
    await h.settle()
    expect(h.live.pending.count).toBe(PENDING_CAP)
    expect(h.live.pill.value).toBe('Many updates · Reload view')
    expect(h.live.deletedAmong(['n1', 'n2'])).toEqual(['n1'])
    h.live.apply()
    expect(h.reload).toHaveBeenCalledTimes(1)
  })

  it('offers to reload the view past the cap instead of applying', async () => {
    const many = Array.from({ length: PENDING_CAP + 1 }, (_, i) => item(`n${i + 1}`, { state: 'backlog' }))
    const h = setup(many)
    for (const row of many) { const node = edit(h, row.id, { state: 'cancelled' }); h.send({ id: row.id, fields: ['state'], revision: node.updated_at }) }
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
  it('AEON-385: reading the same pending change again does not restart the idle wait', async () => {
    const h = setup([item('n1'), item('n2')])
    await closedRow(h)
    clock += 1500
    const node = h.srv.nodes.find(n => n.id === 'n1')!
    h.send({ id: 'n1', fields: ['state'], revision: node.updated_at })
    await h.settle()
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
    h.load([item('n1')])
    h.loading.value = false
    await nextTick()
    await h.settle()
    expect(h.rows.value[0].title).toBe('After the load')
  })

  it('after a gap reads the first page again: a row added meanwhile waits as new', async () => {
    const h = setup([item('n1'), item('n2')], {}, [item('n7', { updated_at: at(20) })])
    // The store asks every view to read again after a gap it could not bridge.
    h.gap()
    await vi.waitFor(() => expect(h.srv.fetchList).toHaveBeenCalled())
    await h.settle()
    expect(h.live.pill.value).toBe('1 update · Show')
    expect(h.rows.value!.map(r => r.id)).toEqual(['n1', 'n2'])
    // One request for the page; only the new row is looked at more closely.
    expect(h.srv.calls.map(q => q.ids ?? null)).toEqual([null, ['n7']])
  })

  it('a resync that failed runs again at once when the stream resumes', async () => {
    const h = setup([item('n1')])
    h.srv.fetchList.mockRejectedValueOnce(new Error('offline'))
    edit(h, 'n1', { title: 'Remote' })
    h.gap()
    await vi.advanceTimersByTimeAsync(0)
    expect(h.srv.fetchList).toHaveBeenCalledTimes(1)
    for (const view of h.views()) view.resumed?.()
    await vi.advanceTimersByTimeAsync(BATCH_MS * 3)
    expect(h.rows.value![0].title).toBe('Remote')
    expect(h.live.message.value).toBe('K-1 was updated elsewhere.')
  })

  it('a resync that keeps failing is tried again after growing waits, then waits for a resumed stream', async () => {
    const h = setup([item('n1')])
    const plain = h.srv.fetchList.getMockImplementation()!
    h.srv.fetchList.mockRejectedValue(new Error('offline'))
    edit(h, 'n1', { title: 'Remote' })
    h.gap()
    await vi.advanceTimersByTimeAsync(0)
    expect(h.srv.fetchList).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(RETRY_MS[0])
    expect(h.srv.fetchList).toHaveBeenCalledTimes(2)
    await vi.advanceTimersByTimeAsync(RETRY_MS.reduce((a, b) => a + b, 0) + 60_000)
    expect(h.srv.fetchList).toHaveBeenCalledTimes(RETRY_MS.length + 1)
    expect(h.rows.value![0].title).toBe('Ticket n1')
    h.srv.fetchList.mockImplementation(plain)
    for (const view of h.views()) view.resumed?.()
    await vi.advanceTimersByTimeAsync(BATCH_MS * 3)
    expect(h.rows.value![0].title).toBe('Remote')
  })

  it('after a gap every loaded row is read again, past the first 200 too, in batches', async () => {
    const loaded = Array.from({ length: 250 }, (_, i) => item(`n${i}`))
    const h = setup(loaded)
    edit(h, 'n249', { title: 'Changed past row 200' })
    h.srv.nodes.splice(h.srv.nodes.findIndex(n => n.id === 'n240'), 1)
    h.gap()
    await vi.advanceTimersByTimeAsync(BATCH_MS * 3)
    await h.settle()
    expect(h.rows.value!.find(row => row.id === 'n249')!.title).toBe('Changed past row 200')
    expect(h.live.labels.value.get('n240')).toBe('Deleted')
    expect(h.srv.calls.every(query => !query.ids || query.ids.length <= 200)).toBe(true)
  })

  it('Show keeps the base of an open editor: the pinned row takes the newer version when it closes', async () => {
    const h = setup([item('n1')])
    const row = h.rows.value![0], seen = row.updated_at
    const editor = h.nodes.edit('n1')!
    h.holding.add('n1')
    const node = edit(h, 'n1', { title: 'Remote' })
    h.send({ id: 'n1', fields: ['title'], revision: node.updated_at })
    await h.settle()
    expect(row.title).toBe('Ticket n1')
    h.live.apply()
    expect(row.title).toBe('Ticket n1')
    expect(editor.revision).toBe(seen)
    editor.end(); h.holding.delete('n1')
    h.live.checkNow()
    expect(row.title).toBe('Remote')
  })

  it('a reload keeps the base of an open editor', async () => {
    const h = setup([item('n1')])
    const row = h.rows.value![0], seen = row.updated_at
    const editor = h.nodes.edit('n1')!
    h.load([item('n1', { title: 'Remote', updated_at: at(80) })])
    await nextTick()
    expect(h.rows.value![0]).toBe(row)
    expect(row.updated_at).toBe(seen)
    expect(editor.revision).toBe(seen)
    expect(h.nodes.waiting('n1')).toBe(true)
  })

  it('Reload view after more than 200 updates keeps the base of an open editor', async () => {
    const many = Array.from({ length: PENDING_CAP + 1 }, (_, i) => item(`n${i + 1}`))
    const h = setup(many)
    const seen = h.rows.value!.find(row => row.id === 'n1')!.updated_at
    const editor = h.nodes.edit('n1')!
    for (const row of many) { const node = edit(h, row.id, { state: 'cancelled' }); h.send({ id: row.id, fields: ['state'], revision: node.updated_at }) }
    await h.settle()
    expect(h.live.pill.value).toBe('Many updates · Reload view')
    h.reload.mockImplementation(() => h.load(many.map(row => item(row.id, row.id === 'n1' ? { title: 'Remote', updated_at: at(80) } : { state: 'cancelled', updated_at: at(80) }))))
    h.live.apply()
    await nextTick()
    const next = h.rows.value!.find(row => row.id === 'n1')!
    expect(next.title).toBe('Ticket n1')
    expect(next.updated_at).toBe(seen)
    expect(editor.revision).toBe(seen)
  })

  it('a resync keeps the base of an open editor and marks its row changed', async () => {
    const h = setup([item('n1')])
    const row = h.rows.value![0], seen = row.updated_at
    const editor = h.nodes.edit('n1')!
    edit(h, 'n1', { title: 'Remote' })
    h.gap()
    await vi.advanceTimersByTimeAsync(BATCH_MS * 3)
    await h.settle()
    expect(row.title).toBe('Ticket n1')
    expect(row.updated_at).toBe(seen)
    expect(editor.revision).toBe(seen)
    expect(h.live.labels.value.get('n1')).toBe('Changed')
  })

  it('a pending addition deleted during a gap is not inserted on Show', async () => {
    const fresh = item('n2', { updated_at: at(30) })
    const h = setup([item('n1')], {}, [fresh])
    h.send({ id: 'n2', change: 'created' })
    await h.settle()
    expect(h.live.pill.value).toBe('1 update · Show')
    h.srv.nodes.splice(h.srv.nodes.findIndex(node => node.id === 'n2'), 1)
    const plain = h.srv.fetchList.getMockImplementation()!
    let releaseIds!: () => void
    const gate = new Promise<void>(resolve => { releaseIds = resolve })
    h.srv.fetchList.mockImplementation(async query => {
      const page = await plain(query)
      if (query.ids?.includes('n2')) await gate
      return page
    })
    h.gap()
    await vi.advanceTimersByTimeAsync(BATCH_MS)
    h.live.apply()
    expect(h.rows.value!.map(row => row.id)).toEqual(['n1'])
    releaseIds()
    await h.settle()
    h.live.apply()
    expect(h.rows.value!.map(row => row.id)).toEqual(['n1'])
    expect(h.live.pill.value).toBeNull()
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

describe('own writes: one mutation, not the node', () => {
  it('the delete of this tab is skipped by its exact revision', async () => {
    const h = setup([item('n1')])
    const sent = h.nodes.mark()
    h.srv.nodes.splice(0, 1)
    h.nodes.deleted('n1', at(4), sent)
    h.send({ id: 'n1', actorId: ME, change: 'deleted', revision: at(4) })
    await h.settle()
    expect(h.srv.fetchList).not.toHaveBeenCalled()
    expect(h.live.labels.value.size).toBe(0)
  })

  it('delete here, restore elsewhere, delete again from another tab: the later delete is labelled', async () => {
    const h = setup([item('n1')])
    const sent = h.nodes.mark()
    h.nodes.deleted('n1', at(2), sent)
    h.send({ id: 'n1', actorId: ME, change: 'deleted', revision: at(2) })
    const restored = h.srv.nodes.find(node => node.id === 'n1')!
    Object.assign(restored, { title: 'Restored', updated_at: at(5) })
    h.send({ id: 'n1', actorId: ME, change: 'created', revision: at(5) })
    await h.settle()
    h.live.apply()
    expect(h.rows.value!.some(row => row.id === 'n1')).toBe(true)
    h.srv.nodes.splice(h.srv.nodes.findIndex(node => node.id === 'n1'), 1)
    h.send({ id: 'n1', actorId: ME, change: 'deleted', revision: at(8) })
    await h.settle()
    expect(h.live.labels.value.get('n1')).toBe('Deleted')
    expect(h.live.pill.value).toBe('1 update · Show')
  })

  // Review 326d #2: the DELETE answer lands after the stream delivered both its
  // deletion and a restore. It names its own revision, so it claims nothing else.
  it('a late delete answer does not hide a later delete by the same account in another tab', async () => {
    const h = setup([item('n1')])
    const sent = h.nodes.mark()
    h.send({ id: 'n1', actorId: ME, change: 'deleted', revision: at(2) })
    const restored = h.srv.nodes.find(node => node.id === 'n1')!
    Object.assign(restored, { title: 'Restored', updated_at: at(3) })
    h.send({ id: 'n1', actorId: MIRA, change: 'created', revision: at(3) })
    await h.settle()
    // The answer of this tab's DELETE, at last.
    h.nodes.deleted('n1', at(2), sent)
    expect(h.nodes.isDeleted('n1')).toBe(false)
    // The restored row is loaded again.
    h.load(h.srv.nodes.map(node => structuredClone(node)))
    await nextTick()
    expect(h.rows.value!.map(row => row.title)).toEqual(['Restored'])
    expect(h.live.pill.value).toBeNull()
    // Deleted from another tab under the same account.
    h.srv.nodes.splice(0, 1)
    h.send({ id: 'n1', actorId: ME, change: 'deleted', revision: at(4) })
    await h.settle()
    expect(h.live.labels.value.get('n1')).toBe('Deleted')
    expect(h.live.pill.value).toBe('1 update · Show')
  })
})

describe('tombstones in the list (review 326d #3)', () => {
  it('an addition whose read was in flight when a gap hid its deletion is not inserted on Show', async () => {
    const h = setup([item('n1')], {}, [item('n2', { updated_at: at(30) })])
    const plain = h.srv.fetchList.getMockImplementation()!
    let release!: () => void
    const gate = new Promise<void>(resolve => { release = resolve })
    // The addition's read is answered with n2, but held on the way.
    h.srv.fetchList.mockImplementationOnce(async query => { const page = await plain(query); await gate; return page })
    h.send({ id: 'n2', change: 'created', revision: at(30) })
    await vi.advanceTimersByTimeAsync(BATCH_MS + 10)
    expect(h.srv.fetchList).toHaveBeenCalledTimes(1)
    // Deleted while the stream was away; the resync finishes without it.
    h.srv.nodes.splice(h.srv.nodes.findIndex(node => node.id === 'n2'), 1)
    h.gap()
    await vi.advanceTimersByTimeAsync(BATCH_MS * 3)
    // The old read lands with the addition.
    release()
    await vi.advanceTimersByTimeAsync(BATCH_MS * 3)
    await h.settle()
    h.live.apply()
    expect(h.rows.value!.map(row => row.id)).toEqual(['n1'])
    expect(h.live.pill.value).toBeNull()
  })

  it('Show before the first resync page returns does not insert an addition the gap deleted', async () => {
    const h = setup([item('n1')], {}, [item('n2', { updated_at: at(30) })])
    h.send({ id: 'n2', change: 'created', revision: at(30) })
    await h.settle()
    expect(h.live.pill.value).toBe('1 update · Show')
    h.srv.nodes.splice(h.srv.nodes.findIndex(node => node.id === 'n2'), 1)
    const plain = h.srv.fetchList.getMockImplementation()!
    let release!: () => void
    const gate = new Promise<void>(resolve => { release = resolve })
    h.srv.fetchList.mockImplementationOnce(async query => { await gate; return plain(query) })
    h.gap()
    await vi.advanceTimersByTimeAsync(0)
    // The first resync page is still on its way.
    h.live.apply()
    expect(h.rows.value!.map(row => row.id)).toEqual(['n1'])
    release()
    await vi.advanceTimersByTimeAsync(BATCH_MS * 3)
    await h.settle()
    h.live.apply()
    expect(h.rows.value!.map(row => row.id)).toEqual(['n1'])
    expect(h.live.pill.value).toBeNull()
  })

  it('a deleted row never comes back from an older load or page', async () => {
    const h = setup([item('n1'), item('n2')])
    const sent = h.nodes.mark()
    h.srv.nodes.splice(h.srv.nodes.findIndex(node => node.id === 'n2'), 1)
    h.send({ id: 'n2', change: 'deleted', revision: at(9) })
    // A load sent before the deletion lands after it.
    h.load([item('n1'), item('n2')], sent)
    await nextTick()
    expect(h.rows.value!.map(row => row.id)).toEqual(['n1'])
  })
})

describe('revisions only move forward in the list (review 326d #4)', () => {
  it('a load that was on its way when this tab saved and its event arrived does not undo the save', async () => {
    const h = setup([item('n1', { priority: 'medium', fields: { priority: 'medium' } })])
    const row = h.rows.value![0]
    const sent = h.nodes.mark()
    const stale = structuredClone(h.srv.nodes[0])
    // The panel saves High; its own event arrives.
    const saved = edit(h, 'n1', { priority: 'high', fields: { priority: 'high' } })
    h.nodes.wrote(saved)
    h.send({ id: 'n1', actorId: ME, fields: ['fields.priority'], revision: saved.updated_at })
    await h.settle()
    // The older load lands.
    h.load([stale], sent)
    await nextTick()
    await h.settle()
    expect(h.rows.value![0]).toBe(row)
    expect(row.priority).toBe('high')
    expect(row.updated_at).toBe(saved.updated_at)
  })
})
