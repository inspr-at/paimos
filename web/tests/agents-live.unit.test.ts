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

it('a live hint during a slow fetch does not stack another read', async () => {
  let complete!: (value: ReturnType<typeof page>) => void
  vi.mocked(listAllSessions).mockImplementationOnce(() => new Promise(resolve => { complete = resolve })).mockResolvedValueOnce(page(['lead', 'new-worker']))
  const store = useAgents()
  const first = store.refreshSessions()
  const second = store.refreshSessions()
  await vi.waitFor(() => expect(listAllSessions).toHaveBeenCalledTimes(1))
  expect(listAllSessions).toHaveBeenCalledTimes(1)
  complete(page(['lead']))
  await Promise.all([first, second])
  expect(listAllSessions).toHaveBeenCalledTimes(1)
  expect(store.sessions.map(s => s.id)).toEqual(['lead'])
  await store.refreshSessions()
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
  expect(store.sessionsState).toBe('ready')
  expect(store.sessionsStale).toBe(true)
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

const currentSession = (fields: Partial<HarnessSession> = {}) => ({
  id: 's1', project_id: 'p1', agent_principal_id: 'a1', run_id: 'r1', revision: 3, harness: 'codex', host: 'workstation',
  phase: 'working', activity: 'busy', heartbeat_at: new Date().toISOString(), created_at: new Date().toISOString(),
  stopped_at: null, stop_reason: null, has_problem: false, needs_attention: false, run_status: null, ...fields,
}) as HarnessSession

it('retains known false evidence across partial responses and stale run-cache arrivals', async () => {
  const session = currentSession()
  vi.mocked(listAllSessions).mockResolvedValue({ items: [session], next_cursor: null })
  const store = useAgents()
  await store.refreshSessions()
  store.recordRun({ id: 'r1', status: 'failed' } as never)
  expect(store.views[0]?.status.state).toBe('working')
  const { has_problem, needs_attention, run_status, ...partial } = session
  vi.mocked(listAllSessions).mockResolvedValue({ items: [{ ...partial, revision: 4 } as HarnessSession], next_cursor: null })
  await store.refreshSessions()
  expect(store.sessions[0]).toMatchObject({ has_problem: false, needs_attention: false, run_status: null })
  expect(store.views[0]?.status.state).toBe('working')
  // A genuinely new failure reason is allowed through even without projections.
  vi.mocked(listAllSessions).mockResolvedValue({ items: [{ ...partial, revision: 5, stop_reason: 'worker crashed' } as HarnessSession], next_cursor: null })
  await store.refreshSessions()
  expect(store.views[0]?.status.state).toBe('problem')
})

it('an older ticket response and lower revision cannot replace a fresh list projection', async () => {
  const store = useAgents()
  let finish!: (value: { items: HarnessSession[]; next_cursor: null }) => void
  vi.mocked(listAllSessions).mockImplementationOnce(() => new Promise(resolve => { finish = resolve }))
  const ticketRead = store.ensureTicket('ticket')
  vi.mocked(listAllSessions).mockResolvedValueOnce({ items: [currentSession()], next_cursor: null })
  await store.refreshSessions()
  finish({ items: [currentSession({ has_problem: true })], next_cursor: null })
  await ticketRead
  expect(store.views[0]?.status.state).toBe('working')
  vi.mocked(listAllSessions).mockResolvedValueOnce({ items: [currentSession({ revision: 2, has_problem: true })], next_cursor: null })
  await store.refreshSessions()
  expect(store.views[0]?.status.state).toBe('working')
})

it('clock correction backwards cannot oscillate a threshold warning', async () => {
  vi.useFakeTimers()
  const at = Date.parse('2026-09-27T12:00:00Z')
  vi.setSystemTime(at)
  vi.mocked(listAllSessions).mockResolvedValue({ items: [currentSession({ heartbeat_at: new Date(at - 599_999).toISOString() })], next_cursor: null })
  const store = useAgents()
  await store.refreshSessions()
  expect(store.views[0]?.status.state).toBe('awaiting')
  vi.setSystemTime(at + 1); store.tick()
  expect(store.views[0]?.status.state).toBe('unresponsive')
  vi.setSystemTime(at - 1_000); store.tick()
  expect(store.views[0]?.status.state).toBe('unresponsive')
})
