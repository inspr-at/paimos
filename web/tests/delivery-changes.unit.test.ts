// SPDX-License-Identifier: AGPL-3.0-only
import { effectScope, nextTick, ref } from 'vue'
import { describe, expect, it, vi } from 'vitest'
import { parseDeliveryEvent, useDeliveryChanges, placeDelivery, type PlacementReceipt } from '../src/lib/deliveryChanges'
vi.mock('../src/lib/api', () => ({ api: vi.fn(), APIError: class extends Error { constructor(public status: number, message: string, public body: unknown) { super(message) } } }))
import { api } from '../src/lib/api'
const move = (id = 7): PlacementReceipt => ({ items: [{ item_id: 'item', project_id: 'project', release_id: 'later', rank: 'V', revision: 3, expedite: false, due_on: null }], undo_event_id: id })
const event = (id: number, project = 'project', type = 'ships_in.changed') => JSON.stringify({ id, type, actor_principal_id: 'me', after: { project_id: project } })
function setup() {
  const project = ref<string | null>('project'), owner = ref('tenant:me'), scope = effectScope(), changes: unknown[] = [], apply = vi.fn()
  const live = scope.run(() => useDeliveryChanges(project, owner, { open: () => ({ close: vi.fn(), addEventListener: vi.fn(), onerror: null }), committed: c => changes.push(c), applied: apply }))!
  return { scope, project, owner, live, changes, apply }
}
describe('exact own receipts and held changes', () => {
  it('Include retains exact own correlation without offering multi-page Undo, and lifecycle clears a prior move', () => {
    const h = setup(), captured = h.live.actions.begin()
    h.live.receive(event(7)); h.live.receive(event(8))
    h.live.actions.commit(captured, { kind:'placement', result:move(7), undoable:false })
    expect(h.live.undo.value).toBeNull(); expect(h.live.pending.value).toBe(1)
    h.live.receive(event(7)); expect(h.live.pending.value).toBe(1)
    h.live.actions.commit(h.live.actions.begin(), { kind:'placement', result:move(9) }); expect(h.live.undo.value?.id).toBe(9)
    h.live.actions.commit(h.live.actions.begin(), { kind:'lifecycle' }); expect(h.live.undo.value).toBeNull(); expect(h.live.pending.value).toBe(1)
    h.scope.stop()
  })
  it('reconciles a pending Undo after a newer move, preserving that move receipt and foreign events', async () => {
    const h = setup(); h.live.actions.commit(h.live.actions.begin(), { kind: 'placement', result: move(17) })
    let finish!: (response: Response) => void
    vi.mocked(api).mockImplementationOnce(() => new Promise(resolve => { finish = resolve }))
    const undo = h.live.undoLast()
    h.live.receive(event(21)); h.live.receive(event(22))
    const newer = { ...move(23), items: [{ ...move().items[0]!, item_id: 'other-item' }] }
    h.live.actions.commit(h.live.actions.begin(), { kind: 'placement', result: newer })
    expect(h.live.undo.value?.id).toBe(23); expect(h.live.pending.value).toBe(0)
    finish(new Response(JSON.stringify({ id: 21, undo_of: 17, type: 'ships_in.changed', after: { members: move().items } }), { status: 201 }))
    await undo
    expect(h.changes).toHaveLength(3)
    expect(h.changes[2]).toEqual({ kind: 'placement', result: { items: move().items, undo_event_id: null } })
    expect(h.live.undo.value?.id).toBe(23); expect(h.live.undoBusy.value).toBe(false)
    expect(h.live.pending.value).toBe(1); expect(h.live.error.value).toBe('')
    h.live.receive(event(21)); expect(h.live.pending.value).toBe(1)
    h.scope.stop()
  })
  it('reports a pending Undo refusal after a newer move without losing the newer receipt', async () => {
    const h = setup(); h.live.actions.commit(h.live.actions.begin(), { kind: 'placement', result: move(17) })
    let finish!: (response: Response) => void
    vi.mocked(api).mockImplementationOnce(() => new Promise(resolve => { finish = resolve }))
    const undo = h.live.undoLast()
    h.live.actions.commit(h.live.actions.begin(), { kind: 'placement', result: move(23) })
    finish(new Response(JSON.stringify({ error: 'Placement changed again' }), { status: 409 }))
    await undo
    expect(h.live.error.value).toBe('Placement changed again')
    expect(h.live.undo.value?.id).toBe(23); expect(h.changes).toHaveLength(2)
    expect(h.live.undoBusy.value).toBe(false); expect(h.apply).not.toHaveBeenCalled()
    h.scope.stop()
  })
  it('deduplicates an echo before or after commit, while same-actor other actions stay held', () => {
    const h = setup(), identity = h.live.actions.begin()
    h.live.receive(event(7)); h.live.receive(event(8)); expect(h.live.pending.value).toBe(0)
    expect(h.live.actions.commit(identity, { kind: 'placement', result: move() })).toBe(true)
    expect(h.live.pending.value).toBe(1); expect(h.changes).toHaveLength(1)
    h.live.receive(event(7)); expect(h.live.pending.value).toBe(1)
    h.live.apply(); expect(h.live.pending.value).toBe(0); expect(h.apply).toHaveBeenCalledOnce(); h.scope.stop()
  })
  it('subscribes to Knowledge deletion, archive, learning and Undo events through the stream', () => {
    const listeners = new Map<string, (e: MessageEvent) => void>(), scope = effectScope(), applied = vi.fn()
    const live = scope.run(() => useDeliveryChanges(ref('project'), ref('person'), { open: () => ({ addEventListener: (type, listener) => { listeners.set(type, listener) }, close: vi.fn(), onerror: null }), applied }))!
    const types = ['knowledge.deleted', 'knowledge.updated', 'knowledge.created', 'knowledge.learning_accepted', 'knowledge.learning_dismissed', 'knowledge.learning_drafted']
    for (const [index, type] of types.entries()) {
      expect(listeners.has(type), type).toBe(true)
      listeners.get(type)!({ data: JSON.stringify({ id: index + 1, type, node_id: 'note', undo_of: index === 2 ? 1 : null, node_changes: [{ id: 'note', project_id: 'project', change: index === 0 ? 'deleted' : index === 2 ? 'created' : 'updated' }] }) } as MessageEvent)
      expect(live.pending.value).toBe(index + 1)
      expect(applied).not.toHaveBeenCalled()
    }
    listeners.get('knowledge.deleted')!({ data: JSON.stringify({ id: 99, type: 'knowledge.deleted', node_changes: [{ id: 'private-note', project_id: 'other' }] }) } as MessageEvent)
    expect(live.pending.value).toBe(types.length)
    live.apply(); expect(applied).toHaveBeenCalledOnce(); expect(live.pending.value).toBe(0); scope.stop()
  })
  it('buffers overlapping writes until both exact receipts reconcile and releases foreign changes after refusal', () => {
    const h = setup(), first = h.live.actions.begin(), second = h.live.actions.begin()
    h.live.receive(event(7)); h.live.receive(event(8)); h.live.receive(event(9))
    expect(h.live.pending.value).toBe(0)
    h.live.actions.commit(first, { kind: 'placement', result: move(7) })
    expect(h.live.pending.value).toBe(0)
    h.live.actions.commit(second, { kind: 'placement', result: move(8) })
    expect(h.live.pending.value).toBe(1)
    const refused = h.live.actions.begin(); h.live.receive(event(10))
    expect(h.live.pending.value).toBe(1)
    h.live.actions.failed(refused); expect(h.live.pending.value).toBe(2); h.scope.stop()
  })
  it('buffers an Undo echo before its response and retains unrelated foreign events', async () => {
    const h = setup(); h.live.actions.commit(h.live.actions.begin(), { kind: 'placement', result: move(17) })
    let finish!: (response: Response) => void
    vi.mocked(api).mockImplementationOnce(() => new Promise(resolve => { finish = resolve }))
    const undo = h.live.undoLast()
    h.live.receive(event(21)); h.live.receive(event(22))
    expect(h.live.pending.value).toBe(0)
    finish(new Response(JSON.stringify({ id: 21, undo_of: 17, type: 'ships_in.changed', after: { members: move().items } }), { status: 201 }))
    await undo
    expect(h.live.pending.value).toBe(1); expect(h.live.undo.value).toBeNull(); h.scope.stop()
  })
  it('a refusal changes nothing and gives no Undo; a foreign adoption waits for Apply', () => {
    const h = setup(), identity = h.live.actions.begin()
    h.live.receive(event(3, 'project', 'delivery.adopted')); h.live.actions.failed(identity)
    expect(h.changes).toEqual([]); expect(h.live.undo.value).toBeNull(); expect(h.live.applied.value).toBe(0)
    h.live.apply(); expect(h.live.applied.value).toBe(1); h.scope.stop()
  })
  it('project/person changes discard late commits and all owned pending/Undo state', () => {
    const h = setup(), identity = h.live.actions.begin()
    h.live.receive(event(3)); h.owner.value = 'tenant:another'
    expect(h.live.actions.commit(identity, { kind: 'placement', result: move() })).toBe(false)
    expect(h.live.pending.value).toBe(0); expect(h.live.undo.value).toBeNull(); expect(h.changes).toEqual([]); h.scope.stop()
  })
  it('Undo uses only the exact receipt and exposes authoritative CAS refusal without losing it', async () => {
    const h = setup(); h.live.actions.commit(h.live.actions.begin(), { kind: 'placement', result: move(17) })
    vi.mocked(api).mockResolvedValueOnce(new Response(JSON.stringify({ error: 'Placement changed again' }), { status: 409 }))
    await h.live.undoLast(); expect(api).toHaveBeenLastCalledWith('/events/17/undo', expect.objectContaining({ method: 'POST' }))
    expect(h.live.error.value).toBe('Placement changed again'); expect(h.live.undo.value?.id).toBe(17); expect(h.apply).not.toHaveBeenCalled()
    vi.mocked(api).mockResolvedValueOnce(new Response(JSON.stringify({ id: 21, undo_of: 17, type: 'ships_in.changed', after: { members: move().items } }), { status: 201 }))
    await h.live.undoLast(); h.live.receive(event(21)); expect(h.live.pending.value).toBe(0); expect(h.live.undo.value).toBeNull(); expect(h.apply).not.toHaveBeenCalled(); expect(h.changes).toHaveLength(2); h.scope.stop()
  })
  it('no-op and lifecycle commits offer no Undo, and native foreign events remain held', () => {
    const h = setup()
    h.live.actions.commit(h.live.actions.begin(), { kind: 'placement', result: { items: [], undo_event_id: null } })
    expect(h.live.undo.value).toBeNull()
    h.live.actions.commit(h.live.actions.begin(), { kind: 'placement', result: move() })
    expect(h.live.undo.value?.id).toBe(7)
    h.live.actions.commit(h.live.actions.begin(), { kind: 'lifecycle' })
    expect(h.live.undo.value).toBeNull()
    h.live.receive(JSON.stringify({ id: 9, type: 'node.project_moved', node_changes: [{ id: 'item', project_id: 'project' }] }))
    h.live.receive(JSON.stringify({ id: 10, type: 'relation.created', after: { type: 'relates', source_node_id: 'note', target_node_id: 'ticket' } }))
    expect(h.live.pending.value).toBe(2); h.scope.stop()
  })
  it('placement writes capture item/project/revision and commit only after success', async () => {
    const h = setup(); let answer!: (r: Response) => void
    vi.mocked(api).mockImplementationOnce(() => new Promise(resolve => { answer = resolve }))
    const pending = placeDelivery('project', { item_id: 'item', project_id: 'project', revision: 2 }, { release_id: 'later', expected_release_revision: 8 }, h.live.actions)
    expect(h.changes).toEqual([])
    const [path, init] = vi.mocked(api).mock.calls.at(-1)!
    expect(path).toBe('/nodes/item/ships-in'); expect(JSON.parse(init!.body as string)).toEqual({ release_id: 'later', expected_release_revision: 8, expected_project_id: 'project', expected_revision: 2 })
    answer(new Response(JSON.stringify(move()), { status: 200 })); await pending
    expect(h.changes).toHaveLength(1); expect(h.live.undo.value?.id).toBe(7); h.scope.stop()
  })
  it('deduplicates and bounds held events without claiming complete reads', async () => {
    const h = setup(); for (let i = 1; i <= 510; i++) h.live.receive(event(i))
    h.live.receive(event(1)); expect(h.live.pending.value).toBe(501); expect(h.live.overflow.value).toBe(true)
    h.project.value = 'other'; await nextTick(); expect(h.live.pending.value).toBe(0); h.scope.stop()
  })
  it('ignores malformed, oversized and other-project events, including nested release snapshots', () => {
    expect(parseDeliveryEvent('x', 'project')).toBeNull(); expect(parseDeliveryEvent(event(1, 'other'), 'project')).toBeNull()
    expect(parseDeliveryEvent('x'.repeat((1 << 20) + 1), 'project')).toBeNull()
    expect(parseDeliveryEvent(JSON.stringify({ id: 1, type: 'release.updated', after: { release: { project_id: 'project' } } }), 'project')?.id).toBe(1)
  })
})
