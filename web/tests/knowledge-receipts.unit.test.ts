// SPDX-License-Identifier: AGPL-3.0-only
import { effectScope, ref } from 'vue'
import { afterEach, expect, it, vi } from 'vitest'
import { useDeliveryChanges, type DeliveryActions } from '../src/lib/deliveryChanges'
import { acceptLearning, createKnowledge, deleteKnowledge, dismissLearning, draftLearning, undoKnowledge, updateKnowledge } from '../src/lib/knowledge'
import { api } from '../src/lib/api'
vi.mock('../src/lib/api', () => ({ api: vi.fn(), APIError: class extends Error {} }))
afterEach(() => vi.resetAllMocks())
const event = (id: number) => JSON.stringify({ id, type: 'knowledge.updated', actor_principal_id: 'me', node_changes: [{ id: 'note', project_id: 'project', change: 'updated' }] })
function setup() {
  const scope = effectScope(), owner = ref('tenant:person'), committed = vi.fn()
  const live = scope.run(() => useDeliveryChanges(ref('project'), owner, { committed }))!
  live.actions.commit(live.actions.begin(), { kind: 'placement', result: { items: [], undo_event_id: 10 } })
  return { scope, live, owner, committed }
}
const writes: Record<string, (actions: DeliveryActions) => Promise<unknown>> = {
  create: actions => createKnowledge({ project_id: 'project', type: 'runbook', title: 'Note', slug: 'note' }, actions),
  archive: actions => updateKnowledge('note', { status: 'archived' }, 'stamp', actions),
  delete: actions => deleteKnowledge('note', 'stamp', actions),
  accept: actions => acceptLearning('learning', 'note', 'stamp', false, actions),
  dismiss: actions => dismissLearning('learning', actions),
  draft: actions => draftLearning('learning', { layer_id: 'layer', set_id: 'set' }, actions),
  Undo: actions => undoKnowledge(20, actions),
}
for (const [name, write] of Object.entries(writes)) for (const ordering of ['echo first', 'response first']) {
  it(`${name} correlates its exact receipt (${ordering}) and preserves unrelated changes and move Undo`, async () => {
    const h = setup()
    let finish!: (response: Response) => void
    vi.mocked(api).mockImplementationOnce(() => new Promise(resolve => { finish = resolve }))
    const pending = write(h.live.actions)
    if (ordering === 'echo first') { h.live.receive(event(30)); h.live.receive(event(31)) }
    const beforeResponse = h.live.pending.value
    finish(new Response(JSON.stringify(name === 'Undo' ? { id: 30, undo_of: 20 } : { event_id: 30 }), { status: 201 }))
    await pending
    if (ordering === 'echo first') expect(beforeResponse).toBe(0)
    if (ordering === 'response first') { h.live.receive(event(30)); h.live.receive(event(31)) }
    expect(h.live.pending.value).toBe(1)
    h.live.receive(event(30)); expect(h.live.pending.value).toBe(1)
    expect(h.live.undo.value?.id).toBe(10)
    expect(h.committed).toHaveBeenCalledOnce()
    expect(h.live.error.value).toBe('')
    h.scope.stop()
  })
}
it('a refused Knowledge write releases foreign events and preserves move Undo', async () => {
  const h = setup()
  let finish!: (response: Response) => void
  vi.mocked(api).mockImplementationOnce(() => new Promise(resolve => { finish = resolve }))
  const pending = updateKnowledge('note', { status: 'archived' }, 'stamp', h.live.actions)
  const refused = expect(pending).rejects.toMatchObject({ status: 412 })
  h.live.receive(event(31)); const beforeResponse = h.live.pending.value
  finish(new Response(JSON.stringify({ code: 'stale' }), { status: 412 }))
  await refused
  expect(beforeResponse).toBe(0)
  expect(h.live.pending.value).toBe(1); expect(h.live.undo.value?.id).toBe(10)
  h.scope.stop()
})
it('person changes discard a late Knowledge response without consuming the new person’s write', async () => {
  const h = setup()
  let finish!: (response: Response) => void
  vi.mocked(api).mockImplementationOnce(() => new Promise(resolve => { finish = resolve }))
  const old = updateKnowledge('note', { status: 'archived' }, 'stamp', h.live.actions)
  const discarded = expect(old).rejects.toThrow('Knowledge result was discarded')
  h.owner.value = 'tenant:other'
  const current = h.live.actions.begin(); h.live.receive(event(32))
  finish(new Response(JSON.stringify({ event_id: 30 }), { status: 200 }))
  await discarded
  expect(h.live.pending.value).toBe(0); expect(h.live.undo.value).toBeNull()
  h.live.actions.failed(current); expect(h.live.pending.value).toBe(1)
  h.scope.stop()
})
it('Undo rejects a mismatched receipt and releases buffered events honestly', async () => {
  const h = setup()
  vi.mocked(api).mockImplementationOnce(async () => { h.live.receive(event(30)); return new Response(JSON.stringify({ id: 30, undo_of: 99 }), { status: 201 }) })
  await expect(undoKnowledge(20, h.live.actions)).rejects.toThrow('invalid event receipt')
  expect(h.live.pending.value).toBe(1); expect(h.live.undo.value?.id).toBe(10)
  h.scope.stop()
})
