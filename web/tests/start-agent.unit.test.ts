// SPDX-License-Identifier: AGPL-3.0-only
import { afterEach, describe, expect, it, vi } from 'vitest'
import { dispatchHint, launchState, startAgent, type WorkOrder } from '../src/lib/startAgent'
import type { AgentAccount, AgentRun, ModelProfile } from '../src/lib/agents'
import type { WorkNode } from '../src/lib/api'

const ticket = { id: 'ticket', key: 'AEON-181', title: 'Start agent', body: 'Build the launch flow.', fields: { acceptance_criteria: 'Queue, then show a managed session.' } } as unknown as WorkNode
const profile = { id: 'model', harness: 'codex', enabled: true } as ModelProfile
const order = { node_id: 'order', status: 'draft', revision: 3, assignee_principal_id: 'agent', criteria: [] } as WorkOrder
const queued = { id: 'run', work_order_id: 'order', agent_principal_id: 'agent', model_profile_id: 'model', status: 'queued' } as AgentRun
const selection = { ticket, agentId: 'agent', profileId: 'model' }
const now = Date.parse('2026-09-26T14:00:00Z')
function account(overrides: Partial<AgentAccount> = {}): AgentAccount {
  return { id: 'account', account_key: 'local', harness: 'codex', daemon_id: 'mac', label: 'Work account', registered_by_principal_id: 'agent', state: 'available', last_probe_at: new Date(now - 30_000).toISOString(), last_probe_ok: true, created_at: '', windows: [{ id: 'window', account_id: 'account', starts_at: new Date(now - 60_000).toISOString(), ends_at: new Date(now + 60_000).toISOString(), unit: 'requests', allowance: 10, used: 0, reserved: 0, pace_model: 'unrestricted', burst_ratio: 0 }], ...overrides }
}
function backend(options: { existing?: WorkOrder[]; runs?: AgentRun[]; failPatch?: boolean; lostRunResponse?: boolean } = {}) {
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
    if (url.pathname === '/api/runs') return json({ items: runs, next_cursor: null })
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
      if (loseResponse) { loseResponse = false; throw new TypeError('Network connection lost') }
      return json(queued, 201)
    }
    throw new Error(`Unexpected request ${method} ${path}`)
  }))
  return calls
}
afterEach(() => vi.unstubAllGlobals())

describe('work-order launch', () => {
  it('copies ticket content and criteria, readies with revision, then queues the selected agent/profile', async () => {
    const calls = backend()
    expect(await startAgent(selection)).toEqual({ run: queued, reused: false })
    expect(calls.filter(c => c.method !== 'GET')).toEqual([
      { path: '/api/work-orders', method: 'POST', body: { title: 'AEON-181: Start agent', parent_id: 'ticket', assignee_principal_id: 'agent', body: 'Build the launch flow.\n\n## Acceptance criteria\n\nQueue, then show a managed session.', criteria: ['Queue, then show a managed session.'] } },
      { path: '/api/work-orders/order', method: 'PATCH', body: { expected_revision: 3, assignee_principal_id: 'agent', status: 'ready' } },
      { path: '/api/work-orders/order/runs', method: 'POST', body: { agent_principal_id: 'agent', model_profile_id: 'model' } },
    ])
  })
  it('reuses a ready order without resetting its status or budgets', async () => {
    const calls = backend({ existing: [{ ...order, status: 'ready' }] })
    await startAgent(selection)
    expect(calls.filter(c => c.method !== 'GET')).toHaveLength(1)
  })
  it('pins an optional account and refuses to reuse a run pinned elsewhere', async () => {
    const calls = backend()
    await startAgent({ ...selection, accountId: 'chosen' })
    expect(calls.find(c => c.method === 'POST' && c.path.endsWith('/runs'))?.body?.requested_account_id).toBe('chosen')
    backend({ existing: [order], runs: [{ ...queued, requested_account_id: 'elsewhere' }] })
    await expect(startAgent({ ...selection, accountId: 'chosen' })).rejects.toThrow('already has an active run')
    backend({ existing: [order], runs: [{ ...queued, account_id: 'chosen' }] })
    await expect(startAgent({ ...selection, accountId: 'chosen' })).rejects.toThrow('already has an active run')
  })
  it('recovers an accepted run after the POST response was lost', async () => {
    const calls = backend({ lostRunResponse: true })
    await expect(startAgent(selection)).rejects.toThrow('No connection')
    expect(await startAgent(selection)).toEqual({ run: queued, reused: true })
    expect(calls.filter(c => c.path === '/api/work-orders' && c.method === 'POST')).toHaveLength(1)
    expect(calls.filter(c => c.path.endsWith('/runs') && c.method === 'POST')).toHaveLength(1)
  })
  it('does not queue after a revision conflict or unblock an order implicitly', async () => {
    const calls = backend({ existing: [order], failPatch: true })
    await expect(startAgent(selection)).rejects.toThrow('revision conflict')
    expect(calls.some(c => c.method === 'POST')).toBe(false)
    backend({ existing: [{ ...order, status: 'blocked' }] })
    await expect(startAgent(selection)).rejects.toThrow('blocked')
  })
  it('refuses a second model while another run is active', async () => {
    const calls = backend({ existing: [order], runs: [{ ...queued, model_profile_id: 'other' }] })
    await expect(startAgent(selection)).rejects.toThrow('already has an active run')
    expect(calls.some(c => c.method !== 'GET')).toBe(false)
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
})
