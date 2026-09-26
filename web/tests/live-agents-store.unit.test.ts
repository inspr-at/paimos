// SPDX-License-Identifier: AGPL-3.0-only
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { getLiveAgents } from '../src/lib/agents'
import { useLiveAgents } from '../src/stores/liveAgents'
import type { LiveAgent } from '../src/lib/liveAgents'

vi.mock('../src/lib/agents', () => ({ getLiveAgents: vi.fn() }))
const at = Date.parse('2026-09-26T12:00:00Z')
const agent = (overrides: Partial<LiveAgent> = {}): LiveAgent => ({
  project_id: 'p1', session_id: 's1', name: 'builder', harness: 'codex', management_mode: 'unmanaged', role: 'worker',
  phase: 'working', activity: 'busy', ticket: null, since: new Date(at - 60_000).toISOString(), heartbeat_at: new Date(at).toISOString(), ...overrides,
})
function answer(items: LiveAgent[], clock = at) {
  vi.mocked(getLiveAgents).mockResolvedValue({ items, at: new Date(clock).toISOString(), fresh_seconds: 120 })
}
beforeEach(() => { setActivePinia(createPinia()); vi.useFakeTimers(); vi.setSystemTime(at); vi.mocked(getLiveAgents).mockReset() })
afterEach(() => { vi.unstubAllGlobals(); vi.useRealTimers() })

it('keeps the map stable on identical polls but publishes heartbeat and sequence advances', async () => {
  const store = useLiveAgents()
  answer([agent({ activity_sequence: 3 })])
  await store.refresh()
  const first = store.byProject
  expect(store.eventPulseFor(agent())).toBe(0)
  await store.refresh()
  expect(store.byProject).toBe(first)
  answer([agent({ activity_sequence: 4 })])
  await store.refresh()
  expect(store.eventPulseFor(agent())).toBe(1)
  expect(store.byProject).not.toBe(first)
  const heartbeat = new Date(at + 10_000).toISOString()
  answer([agent({ activity_sequence: 4, heartbeat_at: heartbeat })])
  await store.refresh()
  expect(store.eventPulseFor(agent())).toBe(2)
  answer([agent({ activity_sequence: 2 })])
  await store.refresh()
  answer([agent({ activity_sequence: 4, heartbeat_at: heartbeat })])
  await store.refresh()
  expect(store.eventPulseFor(agent())).toBe(2)
})

it('ages a failed read to stale, recovers, and removes ended sessions on a successful read', async () => {
  const store = useLiveAgents()
  answer([agent()])
  await store.refresh()
  vi.mocked(getLiveAgents).mockRejectedValue(new Error('offline'))
  store.now = at + 121_000
  await store.refresh()
  expect(store.forProject('p1')[0]?.state).toBe('stale')
  expect(store.eventPulseFor(agent())).toBe(0)
  answer([agent({ heartbeat_at: new Date(at + 121_000).toISOString() })], at + 121_000)
  await store.refresh()
  expect(store.forProject('p1')[0]?.state).toBe('working')
  expect(store.eventPulseFor(agent())).toBe(1)
  answer([])
  await store.refresh()
  expect(store.forProject('p1')).toEqual([])
  expect(store.eventPulseFor(agent())).toBe(0)
})

it('uses the server freshness window and does not pulse a newly appearing session', async () => {
  const store = useLiveAgents()
  vi.mocked(getLiveAgents).mockResolvedValue({ items: [agent()], at: new Date(at + 16_000).toISOString(), fresh_seconds: 15 })
  await store.refresh()
  expect(store.forProject('p1')[0]?.state).toBe('stale')
  expect(store.eventPulseFor(agent())).toBe(0)
})
