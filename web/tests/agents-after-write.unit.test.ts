// SPDX-License-Identifier: AGPL-3.0-only
// AEON-402: every write on /agents drops every shared read already in flight.
// Each case starts the page's reads, holds their answers, writes, lets the fresh
// reads land, then releases the old answers: the old data must never win.
import { beforeEach, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { archiveAccount, setAccountState, type AgentAccount } from '../src/lib/agents'
import { cancelRun, type AgentRunRow } from '../src/lib/agentRows'
import { wired, wiredPage } from './wire-fixtures'
import { disconnectComputer, removeComputer, type PairingView } from '../src/lib/agentPairing'
import type { AccountCapacity, CapacitySchedule, ScheduleOverride } from '../src/lib/capacity'
import { clearPermissions } from '../src/lib/authz'
import { resetPositions } from '../src/lib/position'
import type { DeskProjection } from '../src/lib/decisionDesk'
import { usePolledData } from '../src/lib/usePolledData'
import { useAgents } from '../src/stores/agents'
import { useCapacity } from '../src/stores/capacity'

type Server = { accounts: AgentAccount[]; runs: AgentRunRow[]; capacity: AccountCapacity[]; schedules: ScheduleOverride[]; computers: PairingView[]; desk: DeskProjection }
let server: Server
let hold = false
const held: (() => void)[] = []
// A GET answers with what the server had when it was asked, released later while held.
function get<T>(snapshot: () => T): Promise<T> {
  const value = structuredClone(snapshot())
  if (!hold) return Promise.resolve(value)
  return new Promise(resolve => { held.push(() => resolve(value)) })
}

vi.mock('../src/lib/agents', async importOriginal => ({
  ...await importOriginal<typeof import('../src/lib/agents')>(),
  listAccounts: () => get(() => server.accounts),
  archiveAccount: vi.fn(), setAccountState: vi.fn(),
  listApprovals: async () => [], listModels: async () => [],
  listMessages: async () => ({ items: [] }), listTargets: async () => [],
}))
vi.mock('../src/lib/decisionDesk', () => ({ readDeskProjection: () => get(() => server.desk) }))
vi.mock('../src/lib/agentRows', async importOriginal => ({
  ...await importOriginal<typeof import('../src/lib/agentRows')>(),
  listRuns: () => get(() => wiredPage(server.runs)),
  cancelRun: vi.fn(), listAllSessions: async () => wiredPage([]),
}))
vi.mock('../src/lib/capacity', async importOriginal => ({
  ...await importOriginal<typeof import('../src/lib/capacity')>(),
  listCapacity: () => get(() => server.capacity),
  listSchedules: () => get(() => server.schedules),
  putSchedule: async (body: ScheduleOverride) => {
    server.schedules = [{ scope: 'user', schedule: body.schedule }]
    server.capacity = server.capacity.map(c => ({ ...c, schedule: body.schedule! }))
  },
}))
vi.mock('../src/lib/agentPairing', async importOriginal => ({
  ...await importOriginal<typeof import('../src/lib/agentPairing')>(),
  listPairingComputers: () => get(() => server.computers),
  removeComputer: vi.fn(), disconnectComputer: vi.fn(),
}))
vi.mock('../src/stores/projects', () => ({ useProjects: () => ({ byId: () => undefined, load: async () => {} }) }))

const schedule = { timezone: 'UTC', nights: false, week: [] } as unknown as CapacitySchedule
const account = (id: string, state: AgentAccount['state'] = 'available') => ({ id, state }) as AgentAccount
const computer = (state: PairingView['computer_state']) => ({ computer_id: 'pc', request_id: 'req', computer_state: state, enrollments: [{ account_id: 'a', state: state === 'draining' ? 'draining' : 'active' }] }) as unknown as PairingView

beforeEach(() => {
  setActivePinia(createPinia())
  hold = false
  held.length = 0
  server = {
    desk: { items: [{ id: 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa', kind: 'question', revision: 1, title: 'First canonical question', held: false, created_at: '2026-10-02T10:00:00Z', href: '/agents?needs=q:aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa', source: '/api/questions/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa' }], counts: { open: 8, held: 2, chores: 3 }, has_more: true, next_cursor: 'next', as_of: '2026-10-02T12:00:00Z' },
    accounts: [account('a'), account('b')],
    runs: [{ id: 'r', agent_principal_id: 'agent', account_id: 'a', status: 'queued', row_version: 1 } as AgentRunRow],
    // reserved_runs stands in for the capacity a queued run holds on its account.
    capacity: ['a', 'b'].map(id => ({ account_id: id, schedule, reserved_runs: id === 'a' ? 1 : 0 }) as unknown as AccountCapacity),
    schedules: [],
    computers: [computer('connected')],
  }
  // What the server does on each write.
  vi.mocked(archiveAccount).mockImplementation(async id => {
    server.accounts = server.accounts.filter(a => a.id !== id)
    server.runs = server.runs.map(r => r.account_id === id && r.status === 'queued' ? { ...r, status: 'cancelled', row_version: r.row_version! + 1 } : r)
    server.capacity = server.capacity.filter(c => c.account_id !== id)
    server.computers = server.computers.map(c => ({ ...c, enrollments: c.enrollments.filter(e => e.account_id !== id) }))
    return account(id)
  })
  vi.mocked(cancelRun).mockImplementation(async id => {
    server.runs = server.runs.map(r => r.id === id ? { ...r, status: 'cancelled', row_version: r.row_version! + 1 } : r)
    server.capacity = server.capacity.map(c => ({ ...c, reserved_runs: 0 }) as AccountCapacity)
    return wired(server.runs.find(r => r.id === id)!)
  })
  vi.mocked(setAccountState).mockImplementation(async (id, state) => {
    server.accounts = server.accounts.map(a => a.id === id ? { ...a, state } : a)
    return server.accounts.find(a => a.id === id)!
  })
  vi.mocked(removeComputer).mockImplementation(async view => {
    server.computers = []
    server.accounts = server.accounts.filter(a => a.id !== 'a')
    server.capacity = server.capacity.filter(c => c.account_id !== 'a')
    return { ...view, computer_state: 'revoked' }
  })
  vi.mocked(disconnectComputer).mockImplementation(async () => {
    server.computers = [computer('draining')]
    server.accounts = server.accounts.map(a => a.id === 'a' ? { ...a, state: 'draining' } : a)
    return server.computers[0]
  })
})

const settle = async () => { for (let i = 0; i < 4; i++) await new Promise(resolve => setTimeout(resolve)) }

// Load, hold the page's next reads (list and per-agent), write, then release them late.
async function writeDuringReads(write: (stores: { agents: ReturnType<typeof useAgents>; capacity: ReturnType<typeof useCapacity> }) => Promise<unknown>) {
  const agents = useAgents(), capacity = useCapacity()
  await Promise.all([agents.loadAll(), capacity.load(), agents.refreshAgentRuns('agent')])
  hold = true
  const old = Promise.all([agents.loadAll(), capacity.load(), agents.refreshAgentRuns('agent')])
  await vi.waitFor(() => expect(held.length).toBe(7))
  hold = false
  await write({ agents, capacity })
  await settle()
  for (const release of held.splice(0)) release()
  await old
  await settle()
  return { agents, capacity }
}

it('Remove account: accounts, runs, capacity and computers keep the server state', async () => {
  const { agents, capacity } = await writeDuringReads(({ agents }) => agents.removeAccount({ id: 'a' }))
  expect(agents.accounts.map(a => a.id)).toEqual(['b'])
  expect(agents.runs.r?.status).toBe('cancelled')
  expect([...capacity.byAccount.keys()]).toEqual(['b'])
  expect(capacity.computers[0]?.enrollments).toEqual([])
})

it('Cancel run: no older read brings queued or its held capacity back', async () => {
  const { agents, capacity } = await writeDuringReads(({ agents }) => agents.cancelQueuedRun({ id: 'r' }))
  expect(agents.runs.r?.status).toBe('cancelled')
  expect((capacity.byAccount.get('a') as unknown as { reserved_runs: number }).reserved_runs).toBe(0)
})

it('account state toggle: an older list does not undo Drain', async () => {
  const { agents } = await writeDuringReads(({ agents }) => agents.setAccount(account('a'), 'draining'))
  expect(agents.accounts.find(a => a.id === 'a')?.state).toBe('draining')
})

// ConnectedComputers.vue: removeComputer, then agents.afterWrite with its local result.
it('Remove computer: shared accounts and capacity drop its bindings', async () => {
  const { agents, capacity } = await writeDuringReads(async ({ agents }) => {
    await removeComputer(computer('revoked'), { canDisconnect: true } as never)
    await agents.afterWrite()
  })
  expect(agents.accounts.map(a => a.id)).toEqual(['b'])
  expect(capacity.computers).toEqual([])
  expect([...capacity.byAccount.keys()]).toEqual(['b'])
})

it('Disconnect: shared accounts and computers show draining', async () => {
  const { agents, capacity } = await writeDuringReads(async ({ agents }) => {
    await disconnectComputer(computer('connected'), 'drain', { canDisconnect: true } as never)
    await agents.afterWrite()
  })
  expect(agents.accounts.find(a => a.id === 'a')?.state).toBe('draining')
  expect(capacity.computers[0]?.computer_state).toBe('draining')
})

it('schedule save: an older capacity read does not bring the old schedule back', async () => {
  const { capacity } = await writeDuringReads(({ capacity }) => capacity.setNights(true))
  expect(capacity.byAccount.get('a')?.schedule.nights).toBe(true)
  expect(capacity.schedule.nights).toBe(true)
})

it('a refresh after invalidate reads again instead of joining the dropped read', async () => {
  let n = 0
  const release: ((value: number) => void)[] = []
  const read = usePolledData(() => new Promise<number>(resolve => { n++; release.push(resolve) }), 0)
  const old = read.refresh()
  read.invalidate()
  const fresh = read.refresh()
  expect(n).toBe(2)
  release[1](2)
  await fresh
  release[0](1)
  await old
  expect(read.data.value).toBe(2)
})

it('badge counts the current panel while the future desk adapter drops stale reads', async () => {
  const { agents } = await writeDuringReads(async ({ agents }) => {
    server.desk = { ...server.desk, counts: { open: 6, held: 1, chores: 3 } }
    await agents.afterWrite()
  })
  expect(agents.pending).toHaveLength(0)
  expect(agents.needsCount).toBe(0)
  expect(agents.needsCount).toBe(agents.pending.length + agents.held.length)
  expect(agents.deskProjection?.counts.open).toBe(6)
  expect(agents.deskProjection?.counts.chores).toBe(3)
})

it.each([['access revocation', clearPermissions], ['person change', resetPositions]])('%s clears desk counts and prevents an old read reviving them', async (_label, reset) => {
  const agents = useAgents()
  await agents.loadNeeds(true)
  expect(agents.deskProjection?.counts.open).toBe(8)
  expect(agents.needsCount).toBe(0)
  hold = true
  const old = agents.loadNeeds(true)
  expect(held).toHaveLength(1)
  reset()
  expect(agents.needsCount).toBe(0)
  expect(agents.deskState).toBe('idle')
  for (const release of held.splice(0)) release()
  await old
  expect(agents.deskProjection).toBeNull()
  hold = false
  server.desk = { ...server.desk, counts: { open: 2, held: 0, chores: 0 } }
  await agents.loadNeeds(true)
  expect(agents.deskProjection?.counts.open).toBe(2)
  expect(agents.needsCount).toBe(0)
})
