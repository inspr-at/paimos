// SPDX-License-Identifier: AGPL-3.0-only
import { beforeEach, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { readQueue, addToQueue, moveQueue, removeFromQueue, queueReadiness, undoQueueAddition } from '../src/lib/workQueue'
import type { QueueSnapshot } from '../src/lib/workQueue'
import { getNode } from '../src/lib/api'
import { rowStore } from '../src/lib/rowStore'
import { useWorkQueue } from '../src/stores/workQueue'
import { resetPositions } from '../src/lib/position'
vi.mock('../src/lib/api', async original => ({ ...await original<typeof import('../src/lib/api')>(), getNode: vi.fn(async (id: string) => ({ id, key: 'AEON-1', kind_id: 'ticket', title: 'Work', body: '', state: 'open', fields: {}, parent_id: null, position: '0', created_at: '2026-10-04T00:00:00Z', updated_at: '2026-10-04T00:00:01Z', deleted_at: null })) }))
vi.mock('../src/lib/workQueue', async original => ({ ...await original<typeof import('../src/lib/workQueue')>(), readQueue: vi.fn(), addToQueue: vi.fn(), moveQueue: vi.fn(), removeFromQueue: vi.fn(), queueReadiness: vi.fn(), undoQueueAddition: vi.fn() }))
const snapshot = (manual_order = false): QueueSnapshot => ({ items: [], manual_order, capacity: { hours: 0, total: 2 } })
const deferred = <T>() => { let resolve!: (value: T) => void, reject!: (reason: unknown) => void; const promise = new Promise<T>((done, fail) => { resolve = done; reject = fail }); return { promise, resolve, reject } }
beforeEach(() => { resetPositions(); rowStore.clear(); setActivePinia(createPinia()); vi.mocked(readQueue).mockReset(); vi.mocked(addToQueue).mockReset(); vi.mocked(moveQueue).mockReset(); vi.mocked(removeFromQueue).mockReset(); vi.mocked(queueReadiness).mockReset(); vi.mocked(undoQueueAddition).mockReset(); vi.mocked(getNode).mockClear() })
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
it('uses server workspace positions for all moves, including the project subset top', async () => {
  const store = useWorkQueue(), projection = snapshot()
  projection.items = [{ ticket_id: 'a', position: 5 }, { ticket_id: 'b', position: 8 }] as QueueSnapshot['items']
  vi.mocked(readQueue).mockResolvedValue(projection); vi.mocked(moveQueue).mockResolvedValue(undefined as never)
  await store.load('p'); await store.move('p', 'b', -1)
  expect(moveQueue).toHaveBeenLastCalledWith('b', 5)
  await store.move('p', 'b', 'top'); expect(moveQueue).toHaveBeenLastCalledWith('b', 5)
  expect(store.firstShared('p')?.ticket_id).toBe('a')
  vi.mocked(moveQueue).mockClear()
  await store.move('p', 'a', 'top'); expect(moveQueue).not.toHaveBeenCalled()
})
it('exposes the initial read error before any snapshot exists, and clears it on retry', async () => {
  const store = useWorkQueue()
  vi.mocked(readQueue).mockRejectedValueOnce(new Error('offline')).mockResolvedValueOnce(snapshot())
  await store.load('p'); expect(store.snapshots.p).toBeUndefined(); expect(store.errors.p).toBe('offline')
  await store.load('p', true); expect(store.snapshots.p).toEqual(snapshot()); expect(store.errors.p).toBeUndefined()
})
it('accepts authoritative relation-blocker readiness, coalesces checks and fences revisions and tenant resets', async () => {
  const store = useWorkQueue(), result = deferred<Awaited<ReturnType<typeof queueReadiness>>>()
  const row = { id: 'ticket', state: 'blocked', updated_at: 'v1', fields: { estimate_hours: 3, acceptance_criteria: 'Verified' } } as import('../src/lib/api').ListItem
  const ready = { queueable: true, ready: true, missing: [], suggested_estimate_hours: 3, security_review_required: false }
  expect(store.gaps(row)).toEqual(['blocker'])
  vi.mocked(queueReadiness).mockReturnValueOnce(result.promise)
  const first = store.checkReadiness(row), joined = store.checkReadiness(row)
  expect(queueReadiness).toHaveBeenCalledOnce()
  result.resolve(ready); await Promise.all([first, joined]); expect(store.gaps(row)).toEqual([])
  row.updated_at = 'v2'; expect(store.gaps(row)).toEqual(['blocker'])
  const stale = deferred<Awaited<ReturnType<typeof queueReadiness>>>()
  vi.mocked(queueReadiness).mockReturnValueOnce(stale.promise)
  const pending = store.checkReadiness(row); resetPositions(); stale.resolve(ready); await pending
  expect(store.gaps(row)).toEqual(['blocker'])
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

it('returns the stale Undo receipt and drops an old identity write result', async () => {
  const store = useWorkQueue()
  const receipt = { node_id: 'a', undo: { run_id: 'run-a', revision: '2026-10-04T00:00:01Z' } } as Awaited<ReturnType<typeof addToQueue>>
  vi.mocked(addToQueue).mockResolvedValueOnce(receipt)
  vi.mocked(readQueue).mockResolvedValue(snapshot())
  expect((await store.add('p', 'a'))?.undo).toEqual(receipt.undo)
  const pending = deferred<typeof receipt>()
  vi.mocked(addToQueue).mockReturnValueOnce(pending.promise)
  const writing = store.add('p', 'a')
  resetPositions(); pending.resolve(receipt)
  expect(await writing).toBeUndefined()
  expect(store.snapshots).toEqual({})
})

it('reports a saved queue addition when the ticket refresh fails', async () => {
  const store = useWorkQueue()
  vi.mocked(addToQueue).mockResolvedValueOnce({ node_id: 'a' } as Awaited<ReturnType<typeof addToQueue>>)
  vi.mocked(getNode).mockRejectedValueOnce(new Error('ticket read offline'))
  vi.mocked(readQueue).mockResolvedValueOnce(queuedSnapshot())
  await expect(store.add('p', 'a')).rejects.toThrow('Saved, but the ticket could not be refreshed: ticket read offline')
  expect(readQueue).toHaveBeenCalledOnce()
  expect(store.entry('p', 'a')?.run_id).toBe(undoToken.run_id)
  expect(store.busy).toBe(false)
})

const undoToken = { run_id: 'run-a', revision: '2026-10-04T00:00:01Z' }
const queuedSnapshot = (): QueueSnapshot => ({ ...snapshot(), items: [{ ticket_id: 'a', run_id: undoToken.run_id, position: 1 }] as QueueSnapshot['items'] })
it('refreshes queue membership after successful Undo even when the ticket read fails', async () => {
  const store = useWorkQueue(), queued = queuedSnapshot()
  vi.mocked(readQueue).mockResolvedValueOnce(queued).mockResolvedValueOnce(snapshot())
  await store.load('p')
  expect(store.entry('p', 'a')?.run_id).toBe(undoToken.run_id)
  vi.mocked(undoQueueAddition).mockResolvedValueOnce({ removed: true })
  vi.mocked(getNode).mockRejectedValueOnce(new Error('ticket read offline'))
  await expect(store.undoAdd('p', 'a', undoToken)).rejects.toThrow('Undone, but the ticket could not be refreshed: ticket read offline')
  expect(undoQueueAddition).toHaveBeenCalledExactlyOnceWith('a', undoToken)
  expect(readQueue).toHaveBeenCalledTimes(2)
  expect(store.entry('p', 'a')).toBeNull()
  expect(store.errors.p).toBeUndefined()
  expect(store.busy).toBe(false)
})
it('refreshes queue membership without waiting for the successful Undo ticket read', async () => {
  const store = useWorkQueue(), ticket = deferred<Awaited<ReturnType<typeof getNode>>>()
  const refreshed = deferred<void>()
  vi.mocked(readQueue).mockResolvedValueOnce(queuedSnapshot()).mockImplementationOnce(async () => { refreshed.resolve(); return snapshot() })
  await store.load('p')
  vi.mocked(undoQueueAddition).mockResolvedValueOnce({ removed: true })
  vi.mocked(getNode).mockReturnValueOnce(ticket.promise)
  const writing = store.undoAdd('p', 'a', undoToken)
  await refreshed.promise
  expect(store.busy).toBe(true)
  ticket.resolve({ id: 'a', state: 'in_progress', updated_at: '2026-10-04T00:00:02Z' } as Awaited<ReturnType<typeof getNode>>)
  expect(await writing).toEqual({ removed: true })
  expect(store.entry('p', 'a')).toBeNull()
  expect(store.busy).toBe(false)
})
it('reports both failed refreshes as a completed Undo and keeps the queue read error', async () => {
  const store = useWorkQueue(), queued = queuedSnapshot()
  vi.mocked(readQueue).mockResolvedValueOnce(queued).mockRejectedValueOnce(new Error('queue read offline'))
  await store.load('p')
  vi.mocked(undoQueueAddition).mockResolvedValueOnce({ removed: true })
  vi.mocked(getNode).mockRejectedValueOnce(new Error('ticket read offline'))
  await expect(store.undoAdd('p', 'a', undoToken)).rejects.toThrow('Undone, but the ticket could not be refreshed: ticket read offline; the queue could not be refreshed: queue read offline')
  expect(readQueue).toHaveBeenCalledTimes(2)
  expect(store.snapshots.p).toEqual(queued)
  expect(store.errors.p).toBe('queue read offline')
  expect(store.busy).toBe(false)
})
it('reports the completed Undo when only the queue read fails', async () => {
  const store = useWorkQueue()
  vi.mocked(undoQueueAddition).mockResolvedValueOnce({ removed: true })
  vi.mocked(readQueue).mockRejectedValueOnce(new Error('queue read offline'))
  await expect(store.undoAdd('p', 'a', undoToken)).rejects.toThrow('Undone, but the queue could not be refreshed: queue read offline')
  expect(getNode).toHaveBeenCalledExactlyOnceWith('a')
  expect(store.errors.p).toBe('queue read offline')
  expect(store.busy).toBe(false)
})
it('does not refresh or report success when Undo is refused', async () => {
  const store = useWorkQueue(), queued = queuedSnapshot()
  vi.mocked(readQueue).mockResolvedValueOnce(queued)
  await store.load('p')
  vi.mocked(undoQueueAddition).mockRejectedValueOnce(new Error('ticket changed')).mockResolvedValueOnce({ removed: false })
  await expect(store.undoAdd('p', 'a', undoToken)).rejects.toThrow('ticket changed')
  await expect(store.undoAdd('p', 'a', undoToken)).rejects.toThrow('The queue change was not undone.')
  expect(getNode).not.toHaveBeenCalled()
  expect(readQueue).toHaveBeenCalledOnce()
  expect(store.snapshots.p).toEqual(queued)
  expect(store.busy).toBe(false)
})
it('discards Undo refresh results after the identity resets', async () => {
  const store = useWorkQueue(), ticket = deferred<Awaited<ReturnType<typeof getNode>>>()
  const queue = deferred<QueueSnapshot>(), reading = deferred<void>()
  vi.mocked(undoQueueAddition).mockResolvedValueOnce({ removed: true })
  vi.mocked(getNode).mockImplementationOnce(() => { reading.resolve(); return ticket.promise })
  vi.mocked(readQueue).mockReturnValueOnce(queue.promise)
  const writing = store.undoAdd('p', 'a', undoToken)
  await reading.promise
  resetPositions()
  queue.resolve(queuedSnapshot())
  ticket.resolve({ id: 'a', state: 'in_progress', updated_at: '2026-10-04T00:00:02Z' } as Awaited<ReturnType<typeof getNode>>)
  expect(await writing).toBeUndefined()
  expect(store.snapshots).toEqual({})
  expect(store.errors).toEqual({})
  expect(rowStore.revision('a')).toBeNull()
  expect(store.busy).toBe(false)
})

it('discards failed Undo refreshes after the identity resets', async () => {
  const store = useWorkQueue(), ticket = deferred<Awaited<ReturnType<typeof getNode>>>()
  const queue = deferred<QueueSnapshot>(), reading = deferred<void>()
  // The baseline never starts this read; keep its rejection handled there too.
  void queue.promise.catch(() => {})
  vi.mocked(undoQueueAddition).mockResolvedValueOnce({ removed: true })
  vi.mocked(getNode).mockImplementationOnce(() => { reading.resolve(); return ticket.promise })
  vi.mocked(readQueue).mockReturnValueOnce(queue.promise)
  const writing = store.undoAdd('p', 'a', undoToken)
  await reading.promise
  resetPositions()
  queue.reject(new Error('old queue read offline'))
  ticket.reject(new Error('old ticket read offline'))
  expect(await writing).toBeUndefined()
  expect(store.snapshots).toEqual({})
  expect(store.errors).toEqual({})
  expect(rowStore.revision('a')).toBeNull()
  expect(store.busy).toBe(false)
})
