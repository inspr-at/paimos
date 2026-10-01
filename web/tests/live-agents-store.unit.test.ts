// SPDX-License-Identifier: AGPL-3.0-only
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { getLiveAgents } from '../src/lib/agentRows'
import { useLiveAgents } from '../src/stores/liveAgents'
import type { LiveAgent } from '../src/lib/liveAgents'
import { ticketWorkers } from '../src/lib/liveAgents'

vi.mock('../src/lib/agentRows', async importOriginal => ({ ...await importOriginal<typeof import('../src/lib/agentRows')>(), getLiveAgents: vi.fn() }))
const at = Date.parse('2026-09-26T12:00:00Z')
const agent = (overrides: Partial<LiveAgent> = {}): LiveAgent => ({
  project_id: 'p1', session_id: 's1', name: 'builder', harness: 'codex', management_mode: 'unmanaged', role: 'worker',
  phase: 'working', activity: 'busy', finished: false, ticket: null, since: new Date(at - 60_000).toISOString(), heartbeat_at: new Date(at).toISOString(), ...overrides,
})
function answer(items: LiveAgent[], clock = at) {
  vi.mocked(getLiveAgents).mockResolvedValue({ items, at: new Date(clock).toISOString(), fresh_seconds: 120 })
}
beforeEach(() => { vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify({ value: null })))); setActivePinia(createPinia()); vi.useFakeTimers(); vi.setSystemTime(at); vi.mocked(getLiveAgents).mockReset() })
afterEach(() => { vi.unstubAllGlobals(); vi.useRealTimers() })

it('keeps polls quiet until a new activity note is recorded', async () => {
  const store = useLiveAgents()
  answer([agent({ activity_note_id: 3 })])
  await store.refresh()
  const first = store.byProject
  expect(store.eventPulseFor(agent())).toBe(0)
  await store.refresh()
  expect(store.byProject).toBe(first)
  answer([agent({ activity_note_id: 4 })])
  await store.refresh()
  expect(store.eventPulseFor(agent())).toBe(1)
  expect(store.byProject).not.toBe(first)
  const heartbeat = new Date(at + 10_000).toISOString()
  answer([agent({ activity_note_id: 4, heartbeat_at: heartbeat })])
  await store.refresh()
  expect(store.eventPulseFor(agent())).toBe(1)
  answer([agent({ activity_note_id: 2 })])
  await store.refresh()
  answer([agent({ activity_note_id: 4, heartbeat_at: heartbeat })])
  await store.refresh()
  expect(store.eventPulseFor(agent())).toBe(1)
})

it('ages a failed read through awaiting heartbeat and no heartbeat, recovers, and removes ended sessions on a successful read', async () => {
  const store = useLiveAgents()
  answer([agent()])
  await store.refresh()
  vi.mocked(getLiveAgents).mockRejectedValue(new Error('offline'))
  store.now = at + 181_000
  await store.refresh()
  expect(store.forProject('p1')[0]?.state).toBe('awaiting')
  expect(store.eventPulseFor(agent())).toBe(0)
  store.now = at + 600_000
  expect(store.forProject('p1')[0]?.state).toBe('unresponsive')
  answer([agent({ heartbeat_at: new Date(at + 181_000).toISOString() })], at + 181_000)
  await store.refresh()
  expect(store.forProject('p1')[0]?.state).toBe('working')
  expect(store.eventPulseFor(agent())).toBe(0)
  answer([])
  await store.refresh()
  expect(store.forProject('p1')).toEqual([])
  expect(store.eventPulseFor(agent())).toBe(0)
})

it('uses viewer thresholds rather than the legacy server freshness window', async () => {
  const store = useLiveAgents()
  vi.mocked(getLiveAgents).mockResolvedValue({ items: [agent()], at: new Date(at + 16_000).toISOString(), fresh_seconds: 15 })
  await store.refresh()
  expect(store.forProject('p1')[0]?.state).toBe('working')
  expect(store.eventPulseFor(agent())).toBe(0)
})

class Stream extends EventTarget {
  static current: Stream | undefined
  onopen: (() => void) | null = null
  onerror: (() => void) | null = null
  close = vi.fn()
  constructor(public url: string) { super(); Stream.current = this }
}
const ticket = { id: 't1', key: 'P-1', title: 'Ticket', project_id: 'p1' }
async function watching(items: LiveAgent[]) {
  Stream.current = undefined
  vi.stubGlobal('EventSource', Stream)
  vi.stubGlobal('document', Object.assign(new EventTarget(), { visibilityState: 'visible' }))
  vi.stubGlobal('window', new EventTarget())
  vi.stubGlobal('navigator', { onLine: true })
  answer(items)
  const store = useLiveAgents()
  const stop = store.watch()
  await vi.advanceTimersByTimeAsync(0)
  return { store, stop, workers: () => ticketWorkers(store.forProject('p1'), 'p1').get('t1') ?? [] }
}

it('removes the ended assignee within seconds of the stop hint, before the next poll', async () => {
  const { stop, workers } = await watching([agent({ ticket })])
  try {
    expect(workers()).toHaveLength(1)
    answer([agent({ ticket, phase: 'stopped', stopped_at: new Date(at).toISOString() })])
    Stream.current?.dispatchEvent(new Event('harness.stopped'))
    await vi.advanceTimersByTimeAsync(500)
    expect(workers()).toEqual([])
  } finally { stop() }
})

it('joins a session registered after the list loaded without waiting for a poll', async () => {
  const { stop, workers } = await watching([])
  try {
    answer([agent({ ticket })])
    Stream.current?.dispatchEvent(new Event('harness.registered'))
    await vi.advanceTimersByTimeAsync(500)
    expect(workers().map(worker => worker.session_id)).toEqual(['s1'])
  } finally { stop() }
})

it('resyncs on reconnect and shares one stream until the last watcher leaves', async () => {
  const { store, stop, workers } = await watching([agent({ ticket })])
  const stopOther = store.watch()
  try {
    const stream = Stream.current
    expect(stream).toBeDefined()
    stream?.onerror?.()
    answer([])
    stream?.onopen?.()
    await vi.advanceTimersByTimeAsync(500)
    expect(workers()).toEqual([])
    stop()
    expect(stream?.close).not.toHaveBeenCalled()
    stopOther()
    expect(stream?.close).toHaveBeenCalledOnce()
  } finally { stop(); stopOther() }
})

it('a stop during a slow poll discards the old response and coalesces a catch-up read', async () => {
  const { stop, workers } = await watching([agent({ ticket })])
  try {
    let finish!: (page: Awaited<ReturnType<typeof getLiveAgents>>) => void
    vi.mocked(getLiveAgents).mockImplementationOnce(() => new Promise(resolve => { finish = resolve }))
    await vi.advanceTimersByTimeAsync(20_000)
    answer([])
    for (let i = 0; i < 10; i++) Stream.current?.dispatchEvent(new Event('harness.stopped'))
    finish({ items: [agent({ ticket })], at: new Date(at).toISOString(), fresh_seconds: 120 })
    await vi.advanceTimersByTimeAsync(0)
    expect(workers()).toEqual([])
    expect(getLiveAgents).toHaveBeenCalledTimes(3)
  } finally { stop() }
})
