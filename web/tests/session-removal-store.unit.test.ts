// SPDX-License-Identifier: AGPL-3.0-only
import { afterEach, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { listAllSessions, type HarnessSessionRow } from '../src/lib/agentRows'
import { useAgents } from '../src/stores/agents'
import { wired, wiredPage } from './wire-fixtures'

vi.mock('../src/lib/agentRows', async importOriginal => ({
  ...await importOriginal<typeof import('../src/lib/agentRows')>(), listAllSessions: vi.fn(),
}))
afterEach(() => { vi.unstubAllGlobals(); vi.clearAllMocks() })

it('an in-flight poll and an older snapshot cannot resurrect a removed generation', async () => {
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify({ value: null }))))
  setActivePinia(createPinia())
  const store = useAgents()
  const active = {
    id: 'session', project_id: 'project', agent_principal_id: 'agent', harness: 'codex', management_mode: 'unmanaged',
    role: 'worker', phase: 'working', activity: 'busy', revision: 1, row_version: 1, activity_sequence: 1, advertised_capabilities: [],
    heartbeat_at: new Date().toISOString(), created_at: new Date().toISOString(), stopped_at: null,
  } as HarnessSessionRow
  vi.mocked(listAllSessions).mockResolvedValueOnce(wiredPage([active]))
  await store.refreshSessions()
  expect(store.views).toHaveLength(1)
  let finish!: (value: ReturnType<typeof wiredPage<HarnessSessionRow>>) => void
  vi.mocked(listAllSessions).mockReturnValueOnce(new Promise(resolve => { finish = resolve }))
  const flight = store.refreshSessions()
  await vi.waitFor(() => expect(finish).toBeTypeOf('function'))
  // The poll's answer is from a request that started before the removal.
  const older = wiredPage([active])
  store.recordSession(wired({ ...active, revision: 2, row_version: 2, archived_at: new Date().toISOString(), stopped_at: new Date().toISOString(), phase: 'stopped' }))
  finish(older)
  await flight
  expect(store.views).toHaveLength(0)
  expect(store.removedViews).toHaveLength(1)
  // A read that started after the removal and agrees with it keeps it removed.
  vi.mocked(listAllSessions).mockResolvedValueOnce(wiredPage([{ ...active, revision: 2, row_version: 2, archived_at: new Date().toISOString(), stopped_at: new Date().toISOString(), phase: 'stopped' }]))
  await store.refreshSessions()
  expect(store.views).toHaveLength(0)
  expect(store.removedViews).toHaveLength(1)
})
