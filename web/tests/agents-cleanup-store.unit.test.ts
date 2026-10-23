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

// Risk: network recovery invalidates the individual reads but rejoins their old
// bundle, so the accounts tile never reads the recovered (possibly empty) list.
it('a reconnect refreshes accounts before an invalidated bundle finishes and keeps the new bundle owned', async () => {
  vi.mocked(listAccounts).mockResolvedValueOnce([account('a')])
  const store = useAgents()
  await store.refreshAccounts()
  expect(store.accounts.map(a => a.id)).toEqual(['a'])

  const oldAccounts = deferred<AgentAccount[]>()
  const oldRuns = deferred<RunPage>(), newRuns = deferred<RunPage>()
  vi.mocked(listAccounts).mockReturnValueOnce(oldAccounts.promise).mockResolvedValue([])
  vi.mocked(listRuns).mockReturnValueOnce(oldRuns.promise).mockReturnValueOnce(newRuns.promise)
  const oldLoad = store.loadAll()
  let newLoad: Promise<void> | undefined
  try {
    store.invalidatePolls()
    newLoad = store.loadAll()
    // Both bundles are held at explicit barriers; recovery cannot join the old one.
    expect(listAccounts).toHaveBeenCalledTimes(3)
    expect(listRuns).toHaveBeenCalledTimes(2)
    await store.refreshAccounts()
    expect(store.accounts).toEqual([])

    oldAccounts.resolve([account('a')])
    oldRuns.resolve(wiredPage([]))
    await oldLoad
    expect(store.accounts).toEqual([])
    expect(store.loaded).toBe(false)
    expect(store.loading).toBe(true)
    // An obsolete bundle's finally must not clear the replacement's single flight.
    const joinedLoad = store.loadAll()
    expect(listAccounts).toHaveBeenCalledTimes(3)
    expect(listRuns).toHaveBeenCalledTimes(2)
    newRuns.resolve(wiredPage([]))
    await Promise.all([newLoad, joinedLoad])
    expect(store.loaded).toBe(true)
    expect(store.loading).toBe(false)
    expect(store.accounts).toEqual([])
  } finally {
    oldAccounts.resolve([account('a')])
    oldRuns.resolve(wiredPage([]))
    newRuns.resolve(wiredPage([]))
    await Promise.allSettled([oldLoad, newLoad])
  }
})
