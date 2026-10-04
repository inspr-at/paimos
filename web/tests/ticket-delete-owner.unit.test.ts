// SPDX-License-Identifier: AGPL-3.0-only
import { expect, it, vi } from 'vitest'
import { effectScope, ref } from 'vue'
import { useTicket } from '../src/lib/useTicket'
import { RowStore } from '../src/lib/rowStore'
import { flush } from './record-source'

const fixture = (id: string) => ({ id, key: `AEON-${id}`, title: id, kind_slug: 'ticket', kind_id: 'kind', state: 'open', fields: {}, body: '', parent_id: null, updated_at: '2026-10-01T00:00:00Z', created_at: '2026-10-01T00:00:00Z' })
const io = vi.hoisted(() => ({ deleted: [] as string[], patched: [] as string[] }))
vi.mock('../src/lib/api', async importOriginal => ({
  ...await importOriginal<object>(),
  getNode: async (id: string) => fixture(id), getRelations: async () => ({ items: [] }), listNodes: async () => ({ items: [] }), getKinds: async () => ({ items: [] }),
  deleteNode: async (id: string) => { io.deleted.push(id); return { revision: '2026-10-02T00:00:00Z' } },
  patchNode: async (id: string) => { io.patched.push(id); return fixture(id) },
}))
vi.mock('../src/lib/toast', () => ({ toast: () => {} }))
it('S8-007: useTicket deletes the captured target instead of re-reading the selection', async () => {
  const scope = effectScope(), a = fixture('A'), selected = ref(a), rows = new RowStore()
  const ticket = scope.run(() => useTicket(selected as never, { names: new Map(), onRemoved() {}, onCreated() {}, onMoved() {}, live: { busy: ref(false), me: () => null, store: { rows, subscribe: () => () => {} } as never } }))!
  try {
    selected.value = fixture('B'); await flush()
    // A delayed draft callback cannot submit its body against B's selection.
    expect(await ticket.patch({ body: 'A draft' }, undefined, 'A')).toBe('error')
    expect(await ticket.patch({ body: 'No record draft' }, undefined, null)).toBe('error')
    expect(io.patched).toEqual([])
    await ticket.remove(a as never)
    expect(io.deleted).toEqual(['A'])
  } finally { scope.stop() }
})
