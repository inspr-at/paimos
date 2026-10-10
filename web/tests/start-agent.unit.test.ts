// SPDX-License-Identifier: AGPL-3.0-only
import { beforeEach, afterEach, describe, expect, it, vi } from 'vitest'
import { dispatchHint, launchState, staleGrantRejection, startAgent, type WorkOrder } from '../src/lib/startAgent'
import { APIError } from '../src/lib/api'
import type { AgentAccount, AgentRun, ModelProfile } from '../src/lib/agents'
import type { AgentRunRow } from '../src/lib/agentRows'
import { createPinia, setActivePinia } from 'pinia'
import { useAgents } from '../src/stores/agents'
import { resetPositions } from '../src/lib/position'
import { wired } from './wire-fixtures'
import type { WorkNode } from '../src/lib/api'
import * as Cascade from '../src/lib/accountCascade'
import { flush, setupSource } from './record-source'

const ticket = { id: 'ticket', key: 'AEON-181', title: 'Start agent', is_leaf: true, body: 'Build the launch flow.', fields: { acceptance_criteria: 'Queue, then show a managed session.' } } as unknown as WorkNode
const profile = { id: 'model', harness: 'codex', enabled: true } as ModelProfile
const order = { node_id: 'order', status: 'draft', revision: 3, assignee_principal_id: 'agent', criteria: [] } as WorkOrder
const queued = { id: 'run', work_order_id: 'order', agent_principal_id: 'agent', model_profile_id: 'model', status: 'queued' } as AgentRun
const selection = { ticket, agentId: 'agent', profileId: 'model' }
// The page's run ledger: what startAgent reads and launches is judged by it before it is used (AEON-449).
vi.mock('../src/stores/projects', () => ({ useProjects: () => ({ byId: () => undefined, load: async () => {} }) }))
beforeEach(() => { setActivePinia(createPinia()); resetPositions() })
function admission() {
  const agents = useAgents()
  return { run: agents.admitRun, runs: agents.admitRuns }
}
const now = Date.parse('2026-09-26T14:00:00Z')
function account(overrides: Partial<AgentAccount> = {}): AgentAccount {
  return { id: 'account', account_key: 'local', harness: 'codex', daemon_id: 'mac', label: 'Work account', registered_by_principal_id: 'agent', state: 'available', last_probe_at: new Date(now - 30_000).toISOString(), last_probe_ok: true, created_at: '', windows: [{ id: 'window', account_id: 'account', starts_at: new Date(now - 60_000).toISOString(), ends_at: new Date(now + 60_000).toISOString(), unit: 'requests', allowance: 10, used: 0, reserved: 0, pace_model: 'unrestricted', burst_ratio: 0 }], ...overrides }
}
function backend(options: { existing?: WorkOrder[]; runs?: AgentRun[]; failPatch?: boolean; lostRunResponse?: boolean; whileLaunching?: () => void; whileReadingRuns?: () => void } = {}) {
  const orders = [...(options.existing ?? [])], runs = [...(options.runs ?? [])]
  const calls: { path: string; method: string; body?: Record<string, unknown> }[] = []
  let loseResponse = options.lostRunResponse
  vi.stubGlobal('fetch', vi.fn(async (path: string, init: RequestInit) => {
    const url = new URL(path, 'http://localhost')
    const method = init.method ?? 'GET'
    const body = init.body ? JSON.parse(String(init.body)) : undefined
    calls.push({ path: url.pathname, method, body })
    const json = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status, headers: { 'Content-Type': 'application/json' } })
    if (url.pathname === '/api/nodes/ticket') return json(ticket)
    if (url.pathname === '/api/nodes') { expect(url.searchParams.get('parent_id')).toBe('ticket'); expect(url.searchParams.get('kind')).toBe('work_order'); return json({ items: orders.map(o => ({ id: o.node_id })), next_cursor: null }) }
    if (url.pathname === '/api/runs') { options.whileReadingRuns?.(); return json({ items: runs, next_cursor: null }) }
    if (url.pathname === '/api/work-orders' && method === 'POST') { orders.push({ ...order }); return json(order, 201) }
    if (url.pathname === '/api/work-orders/order') {
      if (method === 'PATCH') {
        if (options.failPatch) return json({ error: 'revision conflict' }, 409)
        Object.assign(orders[0], { ...body, revision: orders[0].revision + 1 })
      }
      return json(orders[0])
    }
    if (url.pathname === '/api/work-orders/order/runs' && method === 'POST') {
      runs.push({ ...queued })
      options.whileLaunching?.()
      if (loseResponse) { loseResponse = false; throw new TypeError('Network connection lost') }
      return json(queued, 201)
    }
    throw new Error(`Unexpected request ${method} ${path}`)
  }))
  return calls
}
afterEach(() => vi.unstubAllGlobals())

describe('work-order launch', () => {
  it('rejects a freshly read parent or unknown shape before any work-order write', async () => {
    for (const is_leaf of [false, undefined]) {
      const calls = backend()
      const fetch = vi.mocked(globalThis.fetch)
      fetch.mockImplementationOnce(async () => new Response(JSON.stringify({ ...ticket, is_leaf }), { headers: { 'Content-Type': 'application/json' } }))
      await expect(startAgent(selection, admission())).rejects.toThrow('Select a work leaf before starting an agent.')
      expect(fetch).toHaveBeenCalledTimes(1)
      expect(fetch.mock.calls[0]?.[0]).toBe('/api/nodes/ticket')
      expect(calls).toEqual([])
    }
  })
  it('copies ticket content and criteria, readies with revision, then queues the selected agent/profile', async () => {
    const calls = backend()
    expect(await startAgent(selection, admission())).toEqual({ run: queued, reused: false })
    expect(calls.filter(c => c.method !== 'GET')).toEqual([
      { path: '/api/work-orders', method: 'POST', body: { title: 'AEON-181: Start agent', parent_id: 'ticket', assignee_principal_id: 'agent', body: 'Build the launch flow.\n\n## Acceptance criteria\n\nQueue, then show a managed session.', criteria: ['Queue, then show a managed session.'] } },
      { path: '/api/work-orders/order', method: 'PATCH', body: { expected_revision: 3, assignee_principal_id: 'agent', status: 'ready' } },
      { path: '/api/work-orders/order/runs', method: 'POST', body: { agent_principal_id: 'agent', model_profile_id: 'model' } },
    ])
  })
  it('reuses a ready order without resetting its status or budgets', async () => {
    const calls = backend({ existing: [{ ...order, status: 'ready' }] })
    await startAgent(selection, admission())
    expect(calls.filter(c => c.method !== 'GET')).toHaveLength(1)
  })
  it('pins an optional account and refuses to reuse a run pinned elsewhere', async () => {
    const calls = backend()
    await startAgent({ ...selection, accountId: 'chosen' }, admission())
    expect(calls.find(c => c.method === 'POST' && c.path.endsWith('/runs'))?.body?.requested_account_id).toBe('chosen')
    backend({ existing: [order], runs: [{ ...queued, requested_account_id: 'elsewhere' }] })
    await expect(startAgent({ ...selection, accountId: 'chosen' }, admission())).rejects.toThrow('already has an active run')
    backend({ existing: [order], runs: [{ ...queued, account_id: 'chosen' }] })
    await expect(startAgent({ ...selection, accountId: 'chosen' }, admission())).rejects.toThrow('already has an active run')
  })
  it('recovers an accepted run after the POST response was lost', async () => {
    const calls = backend({ lostRunResponse: true })
    await expect(startAgent(selection, admission())).rejects.toThrow('No connection')
    expect(await startAgent(selection, admission())).toEqual({ run: queued, reused: true })
    expect(calls.filter(c => c.path === '/api/work-orders' && c.method === 'POST')).toHaveLength(1)
    expect(calls.filter(c => c.path.endsWith('/runs') && c.method === 'POST')).toHaveLength(1)
  })
  // Review of AEON-449 round 2, P1: the launch result was returned unstamped, so a delayed
  // answer could rewind a run a newer read had already moved forward.
  it('judges the launch result by the run ledger: a newer row that landed while the POST was in flight stands', async () => {
    const page = admission()
    backend({ whileLaunching: () => { page.run(wired({ ...queued, status: 'running' as const, row_version: 2 } as AgentRunRow)) } })
    const result = await startAgent(selection, page)
    expect(result.run.status).toBe('running')
    expect(result.run.row_version).toBe(2)
    expect(result.reused).toBe(false)
  })
  it('reads an existing run through the ledger too, so a reused run is the newest copy', async () => {
    const page = admission()
    // A newer copy of the run lands while the read of the work order's runs is in flight.
    backend({ existing: [{ ...order, status: 'ready' }], runs: [{ ...queued, row_version: 4 } as AgentRun], whileReadingRuns: () => { page.run(wired({ ...queued, status: 'running' as const, row_version: 5 } as AgentRunRow)) } })
    const result = await startAgent(selection, page)
    expect(result).toMatchObject({ reused: true, run: { status: 'running', row_version: 5 } })
  })
  it('does not queue after a revision conflict or unblock an order implicitly', async () => {
    const calls = backend({ existing: [order], failPatch: true })
    await expect(startAgent(selection, admission())).rejects.toThrow('revision conflict')
    expect(calls.some(c => c.method === 'POST')).toBe(false)
    backend({ existing: [{ ...order, status: 'blocked' }] })
    await expect(startAgent(selection, admission())).rejects.toThrow('blocked')
  })
  it('refuses a second model while another run is active', async () => {
    const calls = backend({ existing: [order], runs: [{ ...queued, model_profile_id: 'other' }] })
    await expect(startAgent(selection, admission())).rejects.toThrow('already has an active run')
    expect(calls.some(c => c.method !== 'GET')).toBe(false)
  })
})

describe('Start Agent leaf selection', () => {
  const stops: (() => void)[] = []
  afterEach(() => stops.splice(0).forEach(stop => stop()))
  function dialogSetup() {
    vi.stubGlobal('document', { activeElement: null })
    const listNodes = vi.fn(async (_params: unknown) => ({ items: [ticket, { ...ticket, id: 'parent', is_leaf: false }, { ...ticket, id: 'unknown', is_leaf: undefined }], next_cursor: 'next-page' as string | null }))
    const launch = vi.fn(async () => { throw new Error('launch captured') })
    const putPin = vi.fn()
    const choice = { hostId: 'host', harness: 'codex', accountId: 'account', modelKey: 'model', profileId: 'profile' }
    const made = setupSource('components/agents/StartAgentDialog.vue', {}, {
      '../../lib/api': { listNodes },
      '../../lib/authz': { can: () => true },
      '../../stores/session': { useSession: () => ({ identity: { principal: { kind: 'person' } } }) },
      '../../lib/agents': { listPins: async () => [], putPin, deletePin: vi.fn(), message: (e: Error) => e.message },
      '../../lib/agentRows': {},
      '../../lib/accountCascade': {
        ...Cascade,
        fetchAccountCatalog: async () => ({ catalog: { hosts: [] }, gap: null, message: '' }),
        fillDefaults: () => ({ ...choice }),
        presentCascade: () => ({ agentId: 'agent', profileId: 'profile', efforts: [{ value: 'profile' }], models: [{ value: 'model' }], accounts: [{ value: 'account' }], requested: {} }),
      },
      '../../lib/position': { writeMark: () => 0, wroteSince: () => false },
      '../../lib/startAgent': { launchState, staleGrantRejection, startAgent: launch },
      '../../stores/agents': { useAgents: () => ({ runs: {}, admitRun: vi.fn(), admitRuns: vi.fn(), afterWrite: vi.fn() }) },
      '../../lib/usePolledData': { usePoller: () => ({ start() {}, stop() {} }) },
    })
    stops.push(made.stop)
    return { state: made.state, listNodes, launch, putPin }
  }
  it('queries canonical leaves on every page and excludes parents and unknown shapes from mixed results', async () => {
    const { state, listNodes } = dialogSetup()
    await state.open(); await flush()
    expect(state.tickets.value.map((item: WorkNode) => item.id)).toEqual(['ticket'])
    expect(listNodes).toHaveBeenLastCalledWith({ kind: ['work'], shape: ['leaf'], q: '', limit: 30, sort: '-updated_at' })
    listNodes.mockResolvedValueOnce({ items: [{ ...ticket, id: 'second-leaf' }, { ...ticket, id: 'second-parent', is_leaf: false }], next_cursor: null })
    await state.search(true)
    expect(listNodes).toHaveBeenLastCalledWith({ kind: ['work'], shape: ['leaf'], q: '', limit: 30, sort: '-updated_at', cursor: 'next-page' })
    expect(state.tickets.value.map((item: WorkNode) => item.id)).toEqual(['ticket', 'second-leaf'])
    expect(state.nextCursor.value).toBeNull()
  })
  it('refuses non-leaf selection and initial targets while keeping confirmed leaves launchable', async () => {
    const { state, launch } = dialogSetup()
    for (const is_leaf of [false, undefined]) {
      const parent = { ...ticket, is_leaf }
      await state.open(parent); await flush()
      expect(state.ticket.value).toBeNull()
      expect(state.canSubmit.value).toBe(false)
      state.selectTicket(parent); await flush()
      expect(state.ticket.value).toBeNull()
      await state.submit()
      expect(launch).not.toHaveBeenCalled()
    }
    state.selectTicket(ticket); await flush()
    expect(state.ticket.value.id).toBe(ticket.id)
    expect(state.canSubmit.value).toBe(true)
    await state.submit()
    expect(launch).toHaveBeenCalledExactlyOnceWith(expect.objectContaining({ ticket: expect.objectContaining({ id: ticket.id }) }), expect.any(Object))
  })
  it('rechecks leaf eligibility at submission before remembering an account or launching', async () => {
    const { state, launch, putPin } = dialogSetup()
    await state.open(ticket); await flush()
    expect(state.canSubmit.value).toBe(true)
    state.remember.value = true
    state.ticket.value = { ...state.ticket.value, is_leaf: false }
    expect(state.canSubmit.value).toBe(false)
    await state.submit()
    expect(putPin).not.toHaveBeenCalled()
    expect(launch).not.toHaveBeenCalled()
  })
})

describe('honest launch state', () => {
  it('distinguishes missing daemon evidence from unavailable account capacity', () => {
    expect(dispatchHint([], 'agent', profile, now).label).toBe('No daemon online')
    expect(dispatchHint([account({ registered_by_principal_id: 'other' })], 'agent', profile, now).label).toBe('No daemon online')
    expect(dispatchHint([account({ last_probe_at: new Date(now - 120_001).toISOString() })], 'agent', profile, now).label).toBe('No daemon online')
    expect(dispatchHint([account({ state: 'draining' })], 'agent', profile, now).label).toBe('No eligible account')
    expect(dispatchHint([account({ harness: 'claude' })], 'agent', profile, now).label).toBe('No eligible account')
    expect(dispatchHint([account({ windows: [] })], 'agent', profile, now).label).toBe('No eligible account')
    expect(dispatchHint([account()], 'agent', profile, now).label).toBe('Account available')
    expect(dispatchHint([account()], 'agent', profile, now, 'missing').label).toBe('No eligible account')
  })
  it('does not round monetary headroom into eligibility', () => {
    const a = account()
    Object.assign(a.windows![0], { unit: 'cost_micros', allowance: Number.MAX_SAFE_INTEGER, used: Number.MAX_SAFE_INTEGER - 1, reserved: 1 })
    expect(dispatchHint([a], 'agent', profile, now).label).toBe('No eligible account')
    a.windows![0].reserved = 0
    expect(dispatchHint([a], 'agent', profile, now).label).toBe('Account available')
  })
  it('never presents queued or claimed as a registered managed session', () => {
    expect(launchState(queued).label).toBe('Queued')
    expect(launchState({ ...queued, status: 'starting' }).label).toBe('Claimed')
    expect(launchState({ ...queued, status: 'failed' }).label).toBe('Failed')
  })
  it('recognizes a stale model-grant rejection and leaves other conflicts alone', () => {
    expect(staleGrantRejection(new APIError(409, 'requested account must belong to the run agent and allow the model profile'))).toBe(true)
    expect(staleGrantRejection(new APIError(409, 'reserved model profile is not eligible'))).toBe(true)
    expect(staleGrantRejection(new APIError(409, 'work order is not ready for dispatch'))).toBe(false)
    expect(staleGrantRejection(new APIError(404, 'not found'))).toBe(false)
    expect(staleGrantRejection(new Error('allow the model profile'))).toBe(false)
  })
})


describe('run now once', () => {
  it('only sends the override when the person chooses it', async () => {
    const calls = backend()
    await startAgent({ ...selection, runNow: true }, admission())
    expect(calls.find(c => c.method === 'POST' && c.path.endsWith('/runs'))?.body?.capacity_override).toBe('now')
  })
  it('uses the server wait reason on a queued run', () => {
    expect(launchState({ ...queued, wait: { code: 'vendor', run_now_allowed: false } })).toEqual({ label: 'Waiting', detail: 'Waiting for the vendor to allow work again' })
  })
})
