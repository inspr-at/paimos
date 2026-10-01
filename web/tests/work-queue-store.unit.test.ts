// SPDX-License-Identifier: AGPL-3.0-only
import { beforeEach, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { readQueue, addToQueue, moveQueue, removeFromQueue } from '../src/lib/workQueue'
import type { QueueSnapshot } from '../src/lib/workQueue'
import { useWorkQueue } from '../src/stores/workQueue'
import { resetPositions } from '../src/lib/position'
vi.mock('../src/lib/api', async original => ({ ...await original<typeof import('../src/lib/api')>(), getNode: vi.fn().mockResolvedValue(null) }))
vi.mock('../src/lib/workQueue', async original => ({ ...await original<typeof import('../src/lib/workQueue')>(), readQueue: vi.fn(), addToQueue: vi.fn(), moveQueue: vi.fn(), removeFromQueue: vi.fn() }))
const snapshot = (manual_order = false): QueueSnapshot => ({ items: [], manual_order, capacity: { hours: 0, total: 2 } })
const deferred = <T>() => { let resolve!: (value: T) => void; const promise = new Promise<T>(done => { resolve = done }); return { promise, resolve } }
beforeEach(() => { resetPositions(); setActivePinia(createPinia()); vi.mocked(readQueue).mockReset(); vi.mocked(addToQueue).mockReset(); vi.mocked(moveQueue).mockReset(); vi.mocked(removeFromQueue).mockReset() })
it('joins a read and rejects an older response after a forced refresh', async () => {
  const store = useWorkQueue(), older = deferred<QueueSnapshot>(), newer = deferred<QueueSnapshot>()
  vi.mocked(readQueue).mockReturnValueOnce(older.promise).mockReturnValueOnce(newer.promise)
  const first = store.load('p'); void store.load('p'); expect(readQueue).toHaveBeenCalledOnce()
  const forced = store.load('p', true)
  newer.resolve(snapshot(true)); await forced
  older.resolve(snapshot(false)); await first
  expect(store.snapshots.p?.manual_order).toBe(true)
})
it('retains the last successful read on failure, with an error', async () => {
  const store = useWorkQueue()
  vi.mocked(readQueue).mockResolvedValueOnce(snapshot(true)).mockRejectedValueOnce(new Error('offline'))
  await store.load('p'); await store.load('p')
  expect(store.snapshots.p?.manual_order).toBe(true); expect(store.errors.p).toBe('offline')
})
it('clears tenant state and prevents an old tenant read landing after reset', async () => {
  const store = useWorkQueue(), result = deferred<QueueSnapshot>()
  vi.mocked(readQueue).mockReturnValueOnce(result.promise)
  const request = store.load('p'); resetPositions(); result.resolve(snapshot(true)); await request
  expect(store.snapshots).toEqual({}); expect(store.errors).toEqual({})
})
it('serializes queue writes, reports failures, and refreshes only after success', async () => {
  const store = useWorkQueue(), writing = deferred<never>()
  vi.mocked(addToQueue).mockReturnValueOnce(writing.promise)
  const first = store.add('p', 'a')
  await expect(store.add('p', 'b')).rejects.toThrow('already being saved')
  writing.resolve(undefined as never); vi.mocked(readQueue).mockResolvedValue(snapshot()); await first
  expect(readQueue).toHaveBeenCalledOnce(); expect(store.busy).toBe(false)
  vi.mocked(addToQueue).mockRejectedValueOnce(new Error('not ready'))
  await expect(store.add('p', 'c')).rejects.toThrow('not ready'); expect(store.busy).toBe(false)
  expect(readQueue).toHaveBeenCalledOnce()
})
it('reports a saved write whose authoritative projection could not refresh', async () => {
  const store = useWorkQueue()
  vi.mocked(addToQueue).mockResolvedValue(undefined as never)
  vi.mocked(readQueue).mockRejectedValue(new Error('offline'))
  await expect(store.add('p', 'a')).rejects.toThrow('Saved, but the queue could not be refreshed')
  expect(store.errors.p).toBe('offline'); expect(store.busy).toBe(false)
})
it('uses server workspace positions in project subsets and can move their first ticket to the workspace top', async () => {
  const store = useWorkQueue(), projection = snapshot()
  projection.items = [{ ticket_id: 'a', position: 5 }, { ticket_id: 'b', position: 8 }] as QueueSnapshot['items']
  vi.mocked(readQueue).mockResolvedValue(projection); vi.mocked(moveQueue).mockResolvedValue(undefined as never)
  await store.load('p'); await store.move('p', 'b', -1)
  expect(moveQueue).toHaveBeenLastCalledWith('b', 5)
  await store.move('p', 'a', 'top'); expect(moveQueue).toHaveBeenLastCalledWith('a', 1)
})
it('refreshes a partial route change and reports loss of the former place without choosing an automatic fallback', async () => {
  const store = useWorkQueue(), projection = snapshot()
  projection.items = [{ ticket_id: 'a', target_agent_id: null }] as QueueSnapshot['items']
  vi.mocked(readQueue).mockResolvedValueOnce(projection).mockResolvedValueOnce(snapshot())
  await store.load('p')
  vi.mocked(removeFromQueue).mockResolvedValue(undefined)
  vi.mocked(addToQueue).mockRejectedValue(new Error('target unavailable'))
  await expect(store.add('p', 'a', { agent_id: 'agent', account_id: 'account', profile_id: 'profile', name: 'Builder', account: 'Pro', model: 'model', effort: 'high', available: false })).rejects.toThrow('left its previous queue place')
  expect(removeFromQueue).toHaveBeenCalledWith('a'); expect(addToQueue).toHaveBeenCalledOnce()
  expect(store.snapshots.p?.items).toEqual([]); expect(store.busy).toBe(false)
})
