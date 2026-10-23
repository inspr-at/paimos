// SPDX-License-Identifier: AGPL-3.0-only
import { beforeEach, afterEach, describe, expect, it, vi } from 'vitest'
import { launchState, staleGrantRejection, startAgent, startAgentCopy, type WorkOrder } from '../src/lib/startAgent'
import { APIError } from '../src/lib/api'
import type { AgentRun } from '../src/lib/agents'
import type { AgentRunRow } from '../src/lib/agentRows'
import { createPinia, setActivePinia } from 'pinia'
import { reactive } from 'vue'
import { useAgents } from '../src/stores/agents'
import { resetPositions } from '../src/lib/position'
import { wired } from './wire-fixtures'
import type { WorkNode } from '../src/lib/api'
import * as Cascade from '../src/lib/accountCascade'
import { deferred, flush, setupSource } from './record-source'
import { LEAD_WORDS, canLaunchLead, leadLaunchReason, type ProjectLead } from '../src/lib/lead'

const ticket = { id: 'ticket', key: 'AEON-181', title: 'Start agent', is_leaf: true, project: null, updated_at: 'revision-1', body: 'Build the launch flow.', fields: { acceptance_criteria: 'Queue, then show a managed session.' } } as unknown as WorkNode
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
    if (url.pathname === '/api/runs/daemon-waits') return json({ items: [] })
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
  it('binds a one-off retry to its exact context, instruction and restriction', async () => {
    const sent: { path: string; body: Record<string, unknown> }[] = []
    let lose = true
    vi.stubGlobal('fetch', vi.fn(async (path: string, init: RequestInit) => {
      const body = JSON.parse(String(init.body))
      sent.push({ path, body })
      if (lose) { lose = false; throw new TypeError('lost response after commit') }
      return new Response(JSON.stringify({ ...queued, id: body.request_id, run_once: true, instruction: body.instruction, repository_mutation_allowed: body.repository_mutation_allowed }), { status: 201 })
    }))
    const chosen = { ...selection, ticket: { ...ticket, updated_at: '2026-10-22T08:00:00Z' }, runOnce: { requestId: 'once-request', instruction: '  Inspect exactly this.\n', repositoryMutationAllowed: false }, isCurrent: () => true }
    await expect(startAgent(chosen, admission())).rejects.toThrow('No connection')
    const result = await startAgent(chosen, admission())
    expect(result.run).toMatchObject({ id: 'once-request', run_once: true, instruction: '  Inspect exactly this.\n', repository_mutation_allowed: false })
    expect(sent).toEqual([sent[0], sent[0]])
    expect(sent[0]).toEqual({ path: '/api/nodes/ticket/run-once', body: { request_id: 'once-request', expected_updated_at: '2026-10-22T08:00:00Z', instruction: '  Inspect exactly this.\n', repository_mutation_allowed: false, agent_principal_id: 'agent', model_profile_id: 'model' } })
  })
  it('drops an old one-off response after the context or person changes', async () => {
    let answer!: (response: Response) => void
    vi.stubGlobal('fetch', vi.fn(() => new Promise<Response>(resolve => { answer = resolve })))
    let current = true
    const page = { run: vi.fn(), runs: vi.fn() }
    const pending = startAgent({ ...selection, runOnce: { requestId: 'once', instruction: 'Inspect', repositoryMutationAllowed: false }, isCurrent: () => current }, page)
    current = false
    answer(new Response(JSON.stringify(queued), { status: 201 }))
    await expect(pending).rejects.toThrow('context or person changed')
    expect(page.run).not.toHaveBeenCalled()
    expect(globalThis.fetch).toHaveBeenCalledTimes(1)
  })
  it('does not reuse a one-off run for an ordinary ticket start', async () => {
    backend({ existing: [order], runs: [{ ...queued, run_once: true, instruction: 'Another task', repository_mutation_allowed: false }] })
    await expect(startAgent(selection, admission())).rejects.toThrow('already has an active run')
  })
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
    const putPin = vi.fn(), readLead = vi.fn<(id: string) => Promise<ProjectLead>>()
    const queueAdd = vi.fn(async (_project: string, id: string) => ({ node_id: id }))
    const fetchCatalog = vi.fn(async () => ({ catalog: { hosts: [] }, gap: null, message: '' }))
    let resetAccess = () => {}
    const loadProjects = vi.fn(async () => {})
    const session = reactive({ identity: { tenant: { id: 'tenant' }, principal: { id: 'person', kind: 'person' } } })
    const choice = { hostId: 'host', harness: 'codex', accountId: 'account', modelKey: 'model', profileId: 'profile' }
    const made = setupSource('components/agents/StartAgentDialog.vue', {}, {
      '../../lib/api': { listNodes },
      '../../lib/authz': { can: () => true, onAccessRevalidate: ({ reset }: { reset: () => void }) => { resetAccess = reset; return () => {} } },
      '../../stores/session': { useSession: () => session },
      '../../stores/projects': { useProjects: () => ({ projects: [{ id: 'project', nodeUpdatedAt: 'fresh-revision' }], load: loadProjects, error: '' }) },
      '../../lib/agents': { listPins: async () => [], putPin, deletePin: vi.fn(), message: (e: Error) => e.message },
      '../../lib/agentRows': {},
      '../../lib/accountCascade': {
        ...Cascade,
        fetchAccountCatalog: fetchCatalog,
        fillDefaults: () => ({ ...choice }),
        presentCascade: () => ({ agentId: 'agent', profileId: 'profile', efforts: [{ value: 'profile' }], models: [{ value: 'model' }], accounts: [{ value: 'account' }], requested: {}, status: { tone: 'neutral', label: 'Available', detail: '' } }),
      },
      '../../lib/position': { writeMark: () => 0, wroteSince: () => false },
      '../../lib/startAgent': { launchState, staleGrantRejection, startAgent: launch, startAgentCopy },
      '../../stores/agents': { useAgents: () => ({ runs: {}, admitRun: vi.fn(), admitRuns: vi.fn(), afterWrite: vi.fn() }) },
      '../../lib/usePolledData': { usePoller: () => ({ start() {}, stop() {} }) },
      '../../lib/lead': { LEAD_WORDS, canLaunchLead, leadLaunchReason, readLead },
      '../../stores/workQueue': { useWorkQueue: () => ({ add: queueAdd, busy: false }) },
    })
    stops.push(made.stop)
    return { state: made.state, listNodes, launch, putPin, session, loadProjects, readLead, queueAdd, fetchCatalog, resetAccess: () => resetAccess() }
  }
  it('queries canonical leaves on every page and excludes parents and unknown shapes from mixed results', async () => {
    const { state, listNodes } = dialogSetup()
    await state.open(); await flush()
    expect(state.tickets.value.map((item: WorkNode) => item.id)).toEqual(['ticket'])
    expect(listNodes).toHaveBeenLastCalledWith({ kind: ['work'], shape: ['leaf'], q: '', limit: 30, sort: '-updated_at', rollups: 'omit' })
    listNodes.mockResolvedValueOnce({ items: [{ ...ticket, id: 'second-leaf' }, { ...ticket, id: 'second-parent', is_leaf: false }], next_cursor: null })
    await state.search(true)
    expect(listNodes).toHaveBeenLastCalledWith({ kind: ['work'], shape: ['leaf'], q: '', limit: 30, sort: '-updated_at', rollups: 'omit', cursor: 'next-page' })
    expect(state.tickets.value.map((item: WorkNode) => item.id)).toEqual(['ticket', 'second-leaf'])
    expect(state.nextCursor.value).toBeNull()
  })
  it('tells the person to narrow a ticket search that hits the aggregate limit', async () => {
    const { state, listNodes } = dialogSetup()
    listNodes.mockRejectedValueOnce(new APIError(503, 'Work aggregates exceeded a resource limit; narrow the scope or retry.'))
    await state.open(); await flush()
    expect(state.searchError.value).toBe('Too many matches. Type more of the title or the key.')
    listNodes.mockRejectedValueOnce(new Error('network down'))
    await state.search()
    expect(state.searchError.value).toBe('network down')
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
  it('keeps failed one-off choices but resets them when context or person changes', async () => {
    const { state, session, launch, readLead, queueAdd } = dialogSetup()
    readLead.mockResolvedValue(configured)
    await state.openOnce({ ...ticket, project }); await flush()
    expect(readLead).not.toHaveBeenCalled()
    expect(state.dispatchMode.value).toBe('manual')
    const warning = [{ label: 'Read-only unavailable', detail: 'This account catalog has no qualified read-only one-off adapter. Refresh it or choose another harness.' }]
    expect(state.settingsWarning.value).toEqual(warning)
    expect(state.settingsOpen.value).toBe(true)
    state.instruction.value = 'Inspect this context'
    state.mayChangeFiles.value = true
    await flush()
    expect(state.settingsWarning.value).toEqual([])
    expect(state.lastSettingsWarning.value).toEqual(warning)
    await state.submit()
    expect(launch).toHaveBeenCalledTimes(1)
    expect(state.instruction.value).toBe('Inspect this context')
    expect(state.mayChangeFiles.value).toBe(true)
    expect(queueAdd).not.toHaveBeenCalled()
    state.selectTicket({ ...ticket, id: 'other-ticket' }); await flush()
    expect(state.instruction.value).toBe('')
    expect(state.mayChangeFiles.value).toBe(false)
    state.instruction.value = 'Owned by the first person'
    state.mayChangeFiles.value = true
    session.identity.principal.id = 'another-person'; await flush()
    expect(state.visible.value).toBe(false)
    expect(state.instruction.value).toBe('')
    expect(state.mayChangeFiles.value).toBe(false)
    expect(state.ticket.value).toBeNull()
  })
  it('refreshes project revisions on every open and keeps a task entered before choosing its project', async () => {
    const { state, loadProjects, launch } = dialogSetup()
    await state.openOnce(); await flush()
    expect(loadProjects).toHaveBeenLastCalledWith(true)
    state.task.value = 'Inspect project context'
    state.projectId.value = 'project'
    state.mayChangeFiles.value = true
    await state.submit()
    expect(launch).toHaveBeenCalledWith(expect.objectContaining({
      ticket: expect.objectContaining({ id: 'project', updated_at: 'fresh-revision' }),
      runOnce: expect.objectContaining({ task: 'Inspect project context' }),
    }), expect.any(Object))
    state.close()
    await state.openOnce(); await flush()
    expect(loadProjects).toHaveBeenCalledTimes(2)
    expect(state.task.value).toBe('')
  })

  const project = { id: 'project', key: 'AEON', title: 'Aeon' }
  const configured: ProjectLead = { project_id: project.id, revision: 4, generation: 1, session_id: null, state: 'paused', reason: 'admission_not_configured', process_active: false, automatic_launch_enabled: false }

  // Risk: filling a manual launch form for configured work used to fail only
  // after its work order and pin had already been written.
  it('AEON-1508: configured work offers the lead queue before any catalog, pin or manual launch', async () => {
    const { state, readLead, queueAdd, fetchCatalog, launch, putPin } = dialogSetup()
    const pending = deferred<ProjectLead>()
    readLead.mockReturnValueOnce(pending.promise)
    await state.open({ ...ticket, project })
    expect(readLead).toHaveBeenCalledExactlyOnceWith(project.id)
    expect(state.dispatchMode.value).toBe('checking')
    expect(state.canSubmit.value).toBe(false)
    await state.submit()
    expect(queueAdd).not.toHaveBeenCalled()
    expect(fetchCatalog).not.toHaveBeenCalled()
    pending.resolve(configured); await flush()
    expect(state.dispatchMode.value).toBe('lead')
    expect(state.canSubmit.value).toBe(false)
    expect(state.canQueueForLead.value).toBe(true)
    expect(state.dismissLabel.value).toBe('Cancel')
    expect(state.submitIcon.value).toBe('queue-add')
    await state.submit()
    expect(queueAdd).toHaveBeenCalledExactlyOnceWith(project.id, ticket.id)
    expect(state.queuedForLead.value).toBe(true)
    expect(state.canQueueForLead.value).toBe(false)
    expect(state.dismissLabel.value).toBe('Close')
    expect(state.submitIcon.value).toBe('check')
    expect(fetchCatalog).not.toHaveBeenCalled()
    expect(putPin).not.toHaveBeenCalled()
    expect(launch).not.toHaveBeenCalled()
    state.close()
    expect(state.visible.value).toBe(false)
    expect(queueAdd).toHaveBeenCalledTimes(1)
    readLead.mockResolvedValueOnce(configured)
    await state.open({ ...ticket, project }); await flush()
    expect(state.dismissLabel.value).toBe('Cancel')
    expect(state.submitIcon.value).toBe('queue-add')
  })

  it('AEON-1508: a delayed lead read cannot replace the next ticket or a reopened dialog', async () => {
    const { state, readLead, fetchCatalog } = dialogSetup()
    const pending = deferred<ProjectLead>()
    readLead.mockReturnValueOnce(pending.promise)
    await state.open({ ...ticket, project })
    state.selectTicket({ ...ticket, id: 'other-ticket', project: null }); await flush()
    expect(state.dispatchMode.value).toBe('manual')
    pending.resolve(configured); await flush()
    expect(state.dispatchMode.value).toBe('manual')
    expect(state.dispatchProject.value).toBeNull()
    expect(fetchCatalog).toHaveBeenCalledTimes(1)
    const old = deferred<ProjectLead>()
    readLead.mockReturnValueOnce(old.promise)
    state.selectTicket({ ...ticket, project })
    state.close()
    readLead.mockResolvedValueOnce({ ...configured, revision: 0, state: 'none' })
    await state.open({ ...ticket, project }); await flush()
    old.resolve(configured); await flush()
    expect(state.dispatchMode.value).toBe('manual')
    expect(state.canSubmit.value).toBe(true)
  })

  it('AEON-1508: unreadable project settings keep manual launch disabled until a successful retry', async () => {
    const { state, readLead, queueAdd, launch, fetchCatalog } = dialogSetup()
    readLead.mockRejectedValueOnce(new APIError(403, 'Permission denied'))
    await state.open({ ...ticket, project }); await flush()
    expect(state.dispatchMode.value).toBe('failed')
    expect(state.dispatchError.value).toContain('Permission denied')
    expect(state.canSubmit.value).toBe(false)
    await state.submit()
    expect(queueAdd).not.toHaveBeenCalled()
    expect(launch).not.toHaveBeenCalled()
    expect(fetchCatalog).not.toHaveBeenCalled()
    readLead.mockResolvedValueOnce(configured)
    await state.checkDispatch()
    expect(state.dispatchMode.value).toBe('lead')
    expect(state.canQueueForLead.value).toBe(true)
  })

  it('AEON-1508: queue failures stay visible and an access reset discards an in-flight result', async () => {
    const { state, readLead, queueAdd, resetAccess } = dialogSetup()
    readLead.mockResolvedValue(configured)
    await state.open({ ...ticket, project }); await flush()
    queueAdd.mockRejectedValueOnce(new Error('Queue refused: acceptance criteria are missing'))
    await state.submit()
    expect(state.queuedForLead.value).toBe(false)
    expect(state.error.value).toBe('Queue refused: acceptance criteria are missing')
    expect(state.canQueueForLead.value).toBe(true)
    const pending = deferred<{ node_id: string }>()
    queueAdd.mockReturnValueOnce(pending.promise)
    const saving = state.submit()
    expect(state.busy.value).toBe(true)
    expect(state.dismissLabel.value).toBe('Cancel')
    expect(state.submitIcon.value).toBe('clock')
    resetAccess()
    pending.resolve({ node_id: ticket.id }); await saving
    expect(state.visible.value).toBe(false)
    expect(state.ticket.value).toBeNull()
    expect(state.projectLead.value).toBeNull()
    expect(state.queuedForLead.value).toBe(false)
    expect(state.busy.value).toBe(false)
    expect(state.dismissLabel.value).toBe('Cancel')
  })

  it('AEON-1508: node-only entry points resolve their project with one bounded leaf read', async () => {
    const { state, listNodes, readLead, fetchCatalog } = dialogSetup()
    listNodes.mockResolvedValueOnce({ items: [{ ...ticket, project }], next_cursor: null })
    readLead.mockResolvedValueOnce(configured)
    await state.open({ ...ticket, project: undefined }); await flush()
    expect(listNodes).toHaveBeenCalledExactlyOnceWith({ ids: [ticket.id], shape: ['leaf'], limit: 1, rollups: 'omit' })
    expect(readLead).toHaveBeenCalledExactlyOnceWith(project.id)
    expect(state.dispatchMode.value).toBe('lead')
    expect(fetchCatalog).not.toHaveBeenCalled()
  })
})

describe('honest launch state', () => {
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
  it('shows the checkout sentence while an earlier pairing still holds this computer', () => {
    const detail = "This computer's checkout is still held by an earlier pairing. Release it in Settings › Computers."
    expect(launchState({ ...queued, daemon_wait_reason: 'checkout_held_by_earlier_pairing' })).toEqual({ label: 'Queued', detail })
    expect(launchState({ ...queued, daemon_wait_reason: 'checkout_held_by_earlier_pairing', wait: { code: 'vendor', run_now_allowed: false } })).toEqual({ label: 'Queued', detail })
  })
})
