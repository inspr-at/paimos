// SPDX-License-Identifier: AGPL-3.0-only
import { afterEach, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { listAllSessions, type HarnessSession } from '../src/lib/agents'
import { useAgents } from '../src/stores/agents'

vi.mock('../src/lib/agents', async importOriginal => ({
  ...await importOriginal<typeof import('../src/lib/agents')>(), listAllSessions: vi.fn(),
}))
afterEach(() => { vi.unstubAllGlobals(); vi.clearAllMocks() })

it('an in-flight poll and an older snapshot cannot resurrect a removed generation', async () => {
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify({ value: null }))))
  setActivePinia(createPinia())
  const store = useAgents()
  const active = {
    id: 'session', project_id: 'project', agent_principal_id: 'agent', harness: 'codex', management_mode: 'unmanaged',
    role: 'worker', phase: 'working', activity: 'busy', revision: 1, activity_sequence: 1, advertised_capabilities: [],
    heartbeat_at: new Date().toISOString(), created_at: new Date().toISOString(), stopped_at: null,
  } as HarnessSession
  vi.mocked(listAllSessions).mockResolvedValueOnce({ items: [active], next_cursor: null })
  await store.refreshSessions()
  expect(store.views).toHaveLength(1)
  let finish!: (value: { items: HarnessSession[]; next_cursor: null }) => void
  vi.mocked(listAllSessions).mockReturnValueOnce(new Promise(resolve => { finish = resolve }))
  const flight = store.refreshSessions()
  await vi.waitFor(() => expect(finish).toBeTypeOf('function'))
  store.recordRemoval({ ...active, revision: 2, archived_at: new Date().toISOString(), stopped_at: new Date().toISOString(), phase: 'stopped' })
  finish({ items: [active], next_cursor: null })
  await flight
  expect(store.views).toHaveLength(0)
  expect(store.removedViews).toHaveLength(1)
  vi.mocked(listAllSessions).mockResolvedValueOnce({ items: [active], next_cursor: null })
  await store.refreshSessions()
  expect(store.views).toHaveLength(0)
  expect(store.removedViews).toHaveLength(1)
})
