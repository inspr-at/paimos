// SPDX-License-Identifier: AGPL-3.0-only
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { listAccounts, subscribeAgents } from '../src/lib/agents'
import { listAllSessions, type HarnessSessionRow } from '../src/lib/agentRows'
import { useAgents } from '../src/stores/agents'
import { wired, wiredPage } from './wire-fixtures'

type HarnessSession = HarnessSessionRow
vi.mock('../src/lib/agents', async importOriginal => ({
  ...await importOriginal<typeof import('../src/lib/agents')>(),
  listAccounts: vi.fn().mockResolvedValue([]), listApprovals: async () => [],
  listModels: async () => [], listMessages: async () => ({ items: [] }), listTargets: async () => [],
}))
vi.mock('../src/lib/agentRows', async importOriginal => ({
  ...await importOriginal<typeof import('../src/lib/agentRows')>(),
  listAllSessions: vi.fn(), listRuns: async () => ({ items: [], next_cursor: null }),
}))
vi.mock('../src/stores/projects', () => ({ useProjects: () => ({ byId: () => undefined, load: async () => {} }) }))
const page = (ids: string[], cursor: string | null = null) => wiredPage(ids.map(id => ({ id }) as HarnessSession), cursor)
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
  vi.mocked(listAllSessions).mockResolvedValueOnce(wiredPage([session(7, 3, at)]))
    .mockResolvedValueOnce(wiredPage([session(7, 4, '2026-09-26T12:01:00Z')]))
    .mockResolvedValueOnce(wiredPage([session(8, 4, '2026-09-26T12:01:00Z')]))
    .mockResolvedValueOnce(wiredPage([session(7, 4, at)]))
    .mockResolvedValueOnce(wiredPage([session(8, 4, at)]))
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
  expect(stream.url).toBe('/api/events/stream?after=latest')
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
  id: 's1', project_id: 'p1', agent_principal_id: 'a1', run_id: 'r1', revision: 3, row_version: 3, harness: 'codex', host: 'workstation',
  phase: 'working', activity: 'busy', heartbeat_at: new Date().toISOString(), created_at: new Date().toISOString(),
  stopped_at: null, stop_reason: null, has_problem: false, needs_attention: false, run_status: null, ...fields,
}) as HarnessSession

it('retains known false evidence across partial responses and stale run-cache arrivals', async () => {
  const session = currentSession()
  vi.mocked(listAllSessions).mockResolvedValue(wiredPage([session]))
  const store = useAgents()
  await store.refreshSessions()
  store.admitRun(wired({ id: 'r1', status: 'failed' } as never))
  expect(store.views[0]?.status.state).toBe('working')
  const { has_problem, needs_attention, run_status, ...partial } = session
  vi.mocked(listAllSessions).mockResolvedValue(wiredPage([{ ...partial, row_version: 4 } as HarnessSession]))
  await store.refreshSessions()
  expect(store.sessions[0]).toMatchObject({ has_problem: false, needs_attention: false, run_status: null })
  expect(store.views[0]?.status.state).toBe('working')
  // A genuinely new failure reason is allowed through even without projections.
  vi.mocked(listAllSessions).mockResolvedValue(wiredPage([{ ...partial, row_version: 5, stop_reason: 'worker crashed' } as HarnessSession]))
  await store.refreshSessions()
  expect(store.views[0]?.status.state).toBe('problem')
})

it('an older ticket response and lower revision cannot replace a fresh list projection', async () => {
  const store = useAgents()
  let finish!: (value: ReturnType<typeof page>) => void
  vi.mocked(listAllSessions).mockImplementationOnce(() => new Promise(resolve => { finish = resolve }))
  const ticketRead = store.ensureTicket('ticket')
  // The ticket's answer is from a request that started before the list read below.
  const older = wiredPage([currentSession({ has_problem: true })])
  // A read that also started before the list below landed, and names a lower row version.
  const lower = wiredPage([currentSession({ row_version: 2, has_problem: true })])
  vi.mocked(listAllSessions).mockResolvedValueOnce(wiredPage([currentSession()]))
  await store.refreshSessions()
  finish(older)
  await ticketRead
  expect(store.views[0]?.status.state).toBe('working')
  vi.mocked(listAllSessions).mockResolvedValueOnce(lower)
  await store.refreshSessions()
  expect(store.views[0]?.status.state).toBe('working')
})

it('clock correction backwards cannot oscillate a threshold warning', async () => {
  vi.useFakeTimers()
  const at = Date.parse('2026-09-27T12:00:00Z')
  vi.setSystemTime(at)
  vi.mocked(listAllSessions).mockResolvedValue(wiredPage([currentSession({ heartbeat_at: new Date(at - 599_999).toISOString() })]))
  const store = useAgents()
  await store.refreshSessions()
  expect(store.views[0]?.status.state).toBe('awaiting')
  vi.setSystemTime(at + 1); store.tick()
  expect(store.views[0]?.status.state).toBe('unresponsive')
  vi.setSystemTime(at - 1_000); store.tick()
  expect(store.views[0]?.status.state).toBe('unresponsive')
})

it('preserves omitted reasons but clears explicit empty evidence and rejects late attention', async () => {
  const reasons: NonNullable<HarnessSession['attention_reasons']> = [{ kind: 'approval', scope: 'run', actor: 'person', count: 1, blocking: true, location: 'approvals' }]
  const store = useAgents()
  vi.mocked(listAllSessions).mockResolvedValueOnce(wiredPage([currentSession({ needs_attention: true, attention_reasons: reasons })]))
  await store.refreshSessions()
  expect(store.views[0]?.status.label).toBe('Awaiting approval')
  const partial = currentSession({ row_version: 4 })
  delete partial.needs_attention
  vi.mocked(listAllSessions).mockResolvedValueOnce(wiredPage([partial]))
  await store.refreshSessions()
  expect(store.sessions[0]?.attention_reasons).toEqual(reasons)
  let finish!: (value: ReturnType<typeof page>) => void
  vi.mocked(listAllSessions).mockImplementationOnce(() => new Promise(resolve => { finish = resolve }))
  const stale = store.ensureTicket('ticket')
  const late = wiredPage([currentSession({ row_version: 4, needs_attention: true, attention_reasons: reasons })])
  vi.mocked(listAllSessions).mockResolvedValueOnce(wiredPage([currentSession({ row_version: 4, attention_reasons: [] })]))
  await store.refreshSessions()
  finish(late)
  await stale
  expect(store.sessions[0]?.attention_reasons).toEqual([])
  expect(store.views[0]?.status.state).toBe('working')
})

it('principal requests never borrow a sibling session label or claim its ownership', async () => {
  const store = useAgents()
  const principal = { id: 'a1', name: 'Shared fixture principal' }
  vi.mocked(listAllSessions).mockResolvedValueOnce(wiredPage([
    currentSession({ id: 's1', display_label: 'First worker', agent: principal }),
    currentSession({ id: 's2', display_label: 'Future worker', agent: principal }),
  ]))
  await store.refreshSessions()
  expect(store.askerName('a1')).toEqual({ name: 'Shared fixture principal', harness: '', sessionId: '' })
})
