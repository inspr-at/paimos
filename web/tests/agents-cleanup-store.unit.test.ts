// SPDX-License-Identifier: AGPL-3.0-only
import { beforeEach, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { archiveAccount, listAccounts, type AgentAccount } from '../src/lib/agents'
import { cancelRun, listRuns, type AgentRunRow } from '../src/lib/agentRows'
import { useAgents } from '../src/stores/agents'
import { wired, wiredPage } from './wire-fixtures'

vi.mock('../src/lib/agents', async importOriginal => ({
  ...await importOriginal<typeof import('../src/lib/agents')>(),
  listAccounts: vi.fn(), archiveAccount: vi.fn(),
  listApprovals: async () => [], listModels: async () => [],
  listMessages: async () => ({ items: [] }), listTargets: async () => [],
}))
vi.mock('../src/lib/agentRows', async importOriginal => ({
  ...await importOriginal<typeof import('../src/lib/agentRows')>(),
  listRuns: vi.fn(), cancelRun: vi.fn(), listAllSessions: async () => wiredPage([]),
}))
vi.mock('../src/stores/projects', () => ({ useProjects: () => ({ byId: () => undefined, load: async () => {} }) }))

const account = (id: string) => ({ id, state: 'available' }) as AgentAccount
const run = (id: string, status: AgentRunRow['status'], row_version = 1) => ({ id, status, row_version }) as AgentRunRow
type RunPage = ReturnType<typeof wiredPage<AgentRunRow>>
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
  vi.mocked(listRuns).mockResolvedValue(wiredPage([run('r', 'queued')]))
  const store = useAgents()
  await store.loadAll()
  expect(store.runs.r?.status).toBe('queued')

  const staleList = deferred<RunPage>()
  const staleAgent = deferred<RunPage>()
  vi.mocked(listRuns).mockReset()
  vi.mocked(listRuns).mockReturnValueOnce(staleList.promise).mockReturnValueOnce(staleAgent.promise)
  vi.mocked(cancelRun).mockResolvedValueOnce(wired(run('r', 'cancelled', 2)))
  const poll = store.loadAll()
  const agentPoll = store.refreshAgentRuns('agent')
  await vi.waitFor(() => expect(listRuns).toHaveBeenCalledTimes(2))
  // Both answers are from requests that started before the cancel.
  const olderList = wiredPage([run('r', 'queued')]), olderAgent = wiredPage([run('r', 'queued')])
  await store.cancelQueuedRun({ id: 'r' })
  staleList.resolve(olderList)
  staleAgent.resolve(olderAgent)
  await Promise.all([poll, agentPoll])
  expect(store.runs.r?.status).toBe('cancelled')

  vi.mocked(listRuns).mockResolvedValue(wiredPage([run('r', 'cancelled', 2)]))
  await store.refreshAgentRuns('agent')
  expect(store.runs.r?.status).toBe('cancelled')
})
