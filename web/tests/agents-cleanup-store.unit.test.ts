// SPDX-License-Identifier: AGPL-3.0-only
import { beforeEach, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { archiveAccount, cancelRun, listAccounts, listRuns, type AgentAccount, type AgentRun } from '../src/lib/agents'
import { useAgents } from '../src/stores/agents'

vi.mock('../src/lib/agents', async importOriginal => ({
  ...await importOriginal<typeof import('../src/lib/agents')>(),
  listAccounts: vi.fn(), listRuns: vi.fn(), archiveAccount: vi.fn(), cancelRun: vi.fn(),
  listAllSessions: async () => ({ items: [], next_cursor: null }), listApprovals: async () => [], listModels: async () => [],
  listMessages: async () => ({ items: [] }), listTargets: async () => [],
}))
vi.mock('../src/stores/projects', () => ({ useProjects: () => ({ byId: () => undefined, load: async () => {} }) }))

const account = (id: string) => ({ id, state: 'available' }) as AgentAccount
const run = (id: string, status: AgentRun['status']) => ({ id, status }) as AgentRun
function deferred<T>() {
  let resolve!: (value: T) => void
  return { promise: new Promise<T>(r => { resolve = r }), resolve }
}
beforeEach(() => {
  setActivePinia(createPinia())
  for (const fn of [listAccounts, listRuns, archiveAccount, cancelRun]) vi.mocked(fn).mockReset()
})

// AEON-402: a GET that started before Remove must not bring the row back.
it('a poll in flight during Remove does not restore the removed account', async () => {
  vi.mocked(listAccounts).mockResolvedValueOnce([account('a'), account('b')])
  const store = useAgents()
  await store.refreshAccounts()
  const stale = deferred<AgentAccount[]>()
  vi.mocked(listAccounts).mockReturnValueOnce(stale.promise).mockResolvedValueOnce([account('b')])
  vi.mocked(archiveAccount).mockResolvedValueOnce(account('a'))
  const poll = store.refreshAccounts()
  await store.removeAccount({ id: 'a' })
  stale.resolve([account('a'), account('b')])
  await poll
  expect(store.accounts.map(a => a.id)).toEqual(['b'])
  await store.refreshAccounts()
  expect(store.accounts.map(a => a.id)).toEqual(['b'])
})

// AEON-402: a list or per-agent read older than Cancel must not undo it.
it('a poll in flight during Cancel does not overwrite cancelled with queued', async () => {
  vi.mocked(listRuns).mockResolvedValue({ items: [run('r', 'queued')], next_cursor: null })
  const store = useAgents()
  await store.loadAll()
  expect(store.runs.r?.status).toBe('queued')

  const staleList = deferred<{ items: AgentRun[]; next_cursor: null }>()
  const staleAgent = deferred<{ items: AgentRun[]; next_cursor: null }>()
  vi.mocked(listRuns).mockReset()
  vi.mocked(listRuns).mockReturnValueOnce(staleList.promise).mockReturnValueOnce(staleAgent.promise)
  vi.mocked(cancelRun).mockResolvedValueOnce(run('r', 'cancelled'))
  const poll = store.loadAll()
  const agentPoll = store.refreshAgentRuns('agent')
  await vi.waitFor(() => expect(listRuns).toHaveBeenCalledTimes(2))
  await store.cancelQueuedRun({ id: 'r' })
  staleList.resolve({ items: [run('r', 'queued')], next_cursor: null })
  staleAgent.resolve({ items: [run('r', 'queued')], next_cursor: null })
  await Promise.all([poll, agentPoll])
  expect(store.runs.r?.status).toBe('cancelled')

  vi.mocked(listRuns).mockResolvedValue({ items: [run('r', 'cancelled')], next_cursor: null })
  await store.refreshAgentRuns('agent')
  expect(store.runs.r?.status).toBe('cancelled')
})
