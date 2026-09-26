// SPDX-License-Identifier: AGPL-3.0-only
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { listAccounts, listAllSessions, subscribeAgents, type HarnessSession } from '../src/lib/agents'
import { useAgents } from '../src/stores/agents'

vi.mock('../src/lib/agents', async importOriginal => ({
  ...await importOriginal<typeof import('../src/lib/agents')>(),
  listAllSessions: vi.fn(),
  listAccounts: vi.fn().mockResolvedValue([]), listApprovals: async () => [], listRuns: async () => ({ items: [] }),
  listModels: async () => [], listMessages: async () => ({ items: [] }), listTargets: async () => [],
}))
vi.mock('../src/stores/projects', () => ({ useProjects: () => ({ byId: () => undefined, load: async () => {} }) }))
const page = (ids: string[], cursor: string | null = null) => ({ items: ids.map(id => ({ id }) as HarnessSession), next_cursor: cursor })
beforeEach(() => { setActivePinia(createPinia()); vi.mocked(listAllSessions).mockReset() })
afterEach(() => { vi.unstubAllGlobals(); vi.useRealTimers() })

it('reads later pages so old leads and their workers are not silently dropped after two pages', async () => {
  vi.mocked(listAllSessions).mockResolvedValueOnce(page(['child'], 'next-1')).mockResolvedValueOnce(page(['other'], 'next-2')).mockResolvedValueOnce(page(['lead']))
  const store = useAgents()
  await store.refreshSessions()
  expect(store.sessions.map(s => s.id)).toEqual(['child', 'other', 'lead'])
  expect(store.sessionsUpdatedAt).not.toBeNull()
})

it('a live hint during a slow fetch queues a fresh read without racing an older response', async () => {
  let complete!: (value: ReturnType<typeof page>) => void
  vi.mocked(listAllSessions).mockImplementationOnce(() => new Promise(resolve => { complete = resolve })).mockResolvedValueOnce(page(['lead', 'new-worker']))
  const store = useAgents()
  const first = store.refreshSessions()
  const second = store.refreshSessions()
  expect(listAllSessions).toHaveBeenCalledTimes(1)
  complete(page(['lead']))
  await Promise.all([first, second])
  expect(listAllSessions).toHaveBeenCalledTimes(2)
  expect(store.sessions.map(s => s.id)).toEqual(['lead', 'new-worker'])
})

it('a failed refresh preserves the last successful timestamp and recovers on the next read', async () => {
  vi.useFakeTimers()
  vi.setSystemTime(new Date('2026-09-26T12:00:00Z'))
  vi.mocked(listAllSessions).mockResolvedValueOnce(page(['lead'])).mockRejectedValueOnce(new Error('offline')).mockResolvedValueOnce(page(['lead', 'worker']))
  const store = useAgents()
  await store.refreshSessions()
  const previous = store.sessionsUpdatedAt
  vi.setSystemTime(new Date('2026-09-26T12:01:00Z'))
  await store.refreshSessions()
  expect(store.sessionsUpdatedAt).toBe(previous)
  expect(store.sessionsState).toBe('error')
  expect(store.sessions.map(s => s.id)).toEqual(['lead'])
  await store.refreshSessions()
  expect(store.sessionsUpdatedAt).toBeGreaterThan(previous!)
  expect(store.sessionsState).toBe('ready')
})

it('pulses only when the latest activity entry advances across session polls', async () => {
  const session = (activity_note_id: number, activity_sequence: number, heartbeat_at: string) =>
    ({ id: 'lead', activity_note_id, activity_note: 'Running tests', activity_sequence, heartbeat_at }) as HarnessSession
  const at = '2026-09-26T12:00:00Z'
  vi.mocked(listAllSessions).mockResolvedValueOnce({ items: [session(7, 3, at)], next_cursor: null })
    .mockResolvedValueOnce({ items: [session(7, 4, '2026-09-26T12:01:00Z')], next_cursor: null })
    .mockResolvedValueOnce({ items: [session(8, 4, '2026-09-26T12:01:00Z')], next_cursor: null })
    .mockResolvedValueOnce({ items: [session(7, 4, at)], next_cursor: null })
    .mockResolvedValueOnce({ items: [session(8, 4, at)], next_cursor: null })
  const store = useAgents()
  await store.refreshSessions()
  expect(store.eventPulseFor('lead')).toBe(0)
  await store.refreshSessions()
  expect(store.eventPulseFor('lead')).toBe(0)
  await store.refreshSessions()
  expect(store.eventPulseFor('lead')).toBe(1)
  await store.refreshSessions()
  await store.refreshSessions()
  expect(store.eventPulseFor('lead')).toBe(1)
})

it('a stalled optional account read does not hold up a newly registered worker', async () => {
  let finish!: (value: never[]) => void
  vi.mocked(listAccounts).mockImplementationOnce(() => new Promise(resolve => { finish = resolve }))
  vi.mocked(listAllSessions).mockResolvedValueOnce(page(['lead'])).mockResolvedValueOnce(page(['lead', 'new-worker']))
  const store = useAgents()
  const first = store.loadAll()
  await vi.waitFor(() => expect(store.sessions.map(s => s.id)).toEqual(['lead']))
  const second = store.loadAll()
  await vi.waitFor(() => expect(store.sessions.map(s => s.id)).toEqual(['lead', 'new-worker']))
  finish([])
  await Promise.all([first, second])
})

it('consumes registered/stopped signals and refreshes when the existing stream reconnects', () => {
  class Stream extends EventTarget {
    static current: Stream
    onopen: (() => void) | null = null
    onerror: (() => void) | null = null
    close = vi.fn()
    constructor(public url: string) { super(); Stream.current = this }
  }
  vi.stubGlobal('EventSource', Stream)
  const changed = vi.fn(), connection = vi.fn()
  const stop = subscribeAgents(changed, connection)
  const stream = Stream.current
  expect(stream.url).toBe('/api/events/stream')
  stream.onopen!()
  stream.dispatchEvent(new Event('harness.registered'))
  stream.dispatchEvent(new Event('harness.stopped'))
  stream.onerror!()
  stream.onopen!()
  expect(changed).toHaveBeenCalledTimes(4)
  expect(connection.mock.calls).toEqual([[true], [false], [true]])
  stop()
  expect(stream.close).toHaveBeenCalledOnce()
})
