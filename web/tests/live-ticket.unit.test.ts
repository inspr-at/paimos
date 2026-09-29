// SPDX-License-Identifier: AGPL-3.0-only
// AEON-326: the open ticket follows changes by others, and while the viewer
// edits nothing moves under them: a read that lands during an edit waits like
// a live change, so a save still sends the revision the editor started from.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { effectScope, nextTick, ref, type EffectScope } from 'vue'
import { APIError, type ListItem, type WorkNode } from '../src/lib/api'
import { LiveNodeStore, type NodeChange } from '../src/lib/liveNodes'

const api = vi.hoisted(() => ({
  getNode: vi.fn(), updateNode: vi.fn(),
  listNodes: vi.fn(async () => ({ items: [], next_cursor: null })),
  getRelations: vi.fn(async () => ({ items: [] })),
  lookupNodes: vi.fn(async () => ({ items: [] })),
}))
vi.mock('../src/lib/api', async original => ({ ...(await original<typeof import('../src/lib/api')>()), ...api }))
vi.mock('../src/lib/toast', () => ({ toast: vi.fn() }))
const { useTicket } = await import('../src/lib/useTicket')

const PROJECT = { id: 'p-1', key: 'PRJ', title: 'Project' }
const at = (seconds: number) => `2026-09-29T10:00:${String(seconds).padStart(2, '0')}Z`
function node(over: Partial<WorkNode> = {}): WorkNode {
  return { id: 'n1', key: 'PRJ-1', kind_id: 'k', title: 'Original', body: '', fields: {}, state: 'new', parent_id: 'p-1', position: '1', created_at: at(0), updated_at: at(1), ...over }
}
function item(over: Partial<WorkNode> = {}): ListItem {
  return { ...node(over), kind_slug: 'ticket', kind_label: 'Ticket', priority: null, assignee: null, parent: null, children_count: 0, project: PROJECT } as ListItem
}
// A read the test answers when it wants.
function slow() {
  let answer!: (value: WorkNode | Promise<never>) => void
  api.getNode.mockImplementationOnce(() => new Promise<WorkNode>((resolve, reject) => { answer = value => value instanceof Promise ? value.catch(reject) : resolve(value) }))
  return (value: WorkNode | Promise<never>) => answer(value)
}
const change = (over: Partial<NodeChange> = {}): NodeChange =>
  ({ eventId: 1, type: 'node.updated', actorId: 'mira', id: 'n1', projectId: 'p-1', change: 'updated', fields: ['title'], revision: at(5), ...over })
const settle = async () => { for (let i = 0; i < 6; i++) await Promise.resolve(); await nextTick() }

let scope: EffectScope | undefined
function setup(fetchNode = vi.fn<(id: string) => Promise<WorkNode | null>>()) {
  const store = new LiveNodeStore({ open: () => null, fetchNode })
  const busy = ref(false)
  const current = ref<ListItem | null>(item())
  scope = effectScope()
  const ticket = scope.run(() => useTicket(current, {
    names: new Map(), onRemoved: () => {}, onCreated: () => {}, onMoved: () => {},
    live: { busy, me: () => 'me', store },
  }))!
  return { ticket, store, busy, target: current.value!, fetchNode }
}

beforeEach(() => { for (const fn of Object.values(api)) fn.mockClear(); api.getNode.mockReset(); api.updateNode.mockReset() })
afterEach(() => { scope?.stop(); scope = undefined })

describe('useTicket: a read that lands during an edit', () => {
  it('waits, and the save still sends the revision the editor started from', async () => {
    const answer = slow()
    const { ticket, busy, target } = setup()
    // The editor opens while the ticket is being read again.
    busy.value = true
    await nextTick()
    answer(node({ title: 'Theirs', updated_at: at(5) }))
    await settle()
    expect(target.title).toBe('Original')
    expect(target.updated_at).toBe(at(1))
    expect(ticket.liveHeld.value).toBe('changed')
    api.updateNode.mockRejectedValueOnce(new APIError(412, 'changed'))
    api.getNode.mockResolvedValueOnce(node({ title: 'Theirs', updated_at: at(5) }))
    expect(await ticket.setTitle('Mine')).toBe('conflict')
    expect(api.updateNode).toHaveBeenCalledWith('n1', { title: 'Mine' }, { ifUnmodifiedSince: at(1) })
    // The conflict shows their version; nothing waits any more.
    expect(target.title).toBe('Theirs')
    expect(ticket.liveHeld.value).toBeNull()
  })

  it('a gap resync that lands during an edit waits too, and applies once the editor closes', async () => {
    api.getNode.mockResolvedValueOnce(node())
    const { ticket, store, busy, target } = setup()
    await settle()
    const answer = slow()
    ;(store as unknown as { views: Set<{ resync?: (r: string) => void }> }).views.forEach(view => view.resync?.('gap'))
    busy.value = true
    await nextTick()
    answer(node({ title: 'Theirs', updated_at: at(5) }))
    await settle()
    expect(target.updated_at).toBe(at(1))
    expect(ticket.liveHeld.value).toBe('changed')
    busy.value = false
    await settle()
    expect(target.title).toBe('Theirs')
    expect(target.updated_at).toBe(at(5))
    expect(ticket.liveHeld.value).toBeNull()
  })

  it('a ticket found deleted during an edit waits as deleted instead of closing the editor', async () => {
    const answer = slow()
    const { ticket, busy } = setup()
    busy.value = true
    await nextTick()
    answer(Promise.reject(new APIError(404, 'gone')))
    await settle()
    expect(ticket.gone.value).toBe(false)
    expect(ticket.liveHeld.value).toBe('deleted')
    busy.value = false
    await settle()
    expect(ticket.gone.value).toBe(true)
  })
})

describe('useTicket: deleted and restored', () => {
  it('a restored ticket is here again', async () => {
    api.getNode.mockResolvedValueOnce(node())
    const fetchNode = vi.fn<(id: string) => Promise<WorkNode | null>>()
    const { ticket, store, target } = setup(fetchNode)
    await settle()
    store.apply(change({ change: 'deleted', fields: ['deleted_at'], revision: at(3) }))
    expect(ticket.gone.value).toBe(true)
    fetchNode.mockResolvedValueOnce(node({ title: 'Back', updated_at: at(4) }))
    store.apply(change({ change: 'created', fields: [], revision: at(4) }))
    await settle()
    expect(ticket.gone.value).toBe(false)
    expect(target.title).toBe('Back')
  })

  it('a restore during an edit clears the waiting deletion', async () => {
    api.getNode.mockResolvedValueOnce(node())
    const fetchNode = vi.fn<(id: string) => Promise<WorkNode | null>>()
    const { ticket, store, busy } = setup(fetchNode)
    await settle()
    busy.value = true
    await nextTick()
    store.apply(change({ change: 'deleted', fields: ['deleted_at'], revision: at(3) }))
    expect(ticket.liveHeld.value).toBe('deleted')
    // Restored as it was: the editor's version is current again.
    fetchNode.mockResolvedValueOnce(node())
    store.apply(change({ change: 'created', fields: [], revision: at(4) }))
    await settle()
    expect(ticket.liveHeld.value).toBeNull()
    busy.value = false
    await settle()
    expect(ticket.gone.value).toBe(false)
  })

  it('a stale read that a deletion overtook during an edit leaves the deletion waiting; cancelling shows it gone', async () => {
    api.getNode.mockResolvedValueOnce(node())
    const fetchNode = vi.fn<(id: string) => Promise<WorkNode | null>>()
    const { ticket, store, busy, target } = setup(fetchNode)
    await settle()
    busy.value = true
    await nextTick()
    let release!: (value: WorkNode) => void
    fetchNode.mockImplementationOnce(() => new Promise(resolve => { release = resolve }))
    store.apply(change({ revision: at(5) }))
    store.apply(change({ change: 'deleted', fields: ['deleted_at'], revision: at(6) }))
    expect(ticket.liveHeld.value).toBe('deleted')
    release(node({ title: 'Stale', updated_at: at(5) }))
    await settle()
    expect(ticket.liveHeld.value).toBe('deleted')
    busy.value = false
    await settle()
    expect(ticket.gone.value).toBe(true)
    expect(target.title).toBe('Original')
  })
})
