// SPDX-License-Identifier: AGPL-3.0-only
import type { Page } from '@playwright/test'
import { defaultSchedule } from './capacity-fixtures'
import { fixtures, me, mockWork } from './work-fixtures'
import { mockEffectivePermissions } from './authz-fixtures'
import type { AgentAccount, AgentRun, HarnessSession, ModelProfile } from '../src/lib/agents'
import type { CapacityWait } from '../src/lib/capacityWait'
import type { WorkOrder } from '../src/lib/startAgent'

const agentId = 'a0000000-0000-4000-8000-000000000001'
const profileId = 'b1000000-0000-4000-8000-000000000001'
const spareProfileId = 'b1000000-0000-4000-8000-000000000002'
const accountId = 'ac000000-0000-4000-8000-000000000001'
const spareAccountId = 'ac000000-0000-4000-8000-000000000002'
const laptopAccountId = 'ac000000-0000-4000-8000-000000000003'

export async function mockStartAgent(page: Page, options: { wait?: CapacityWait; theme?: 'light' | 'dark'; offline?: boolean; unavailable?: boolean; forbidden?: boolean; failQueue?: boolean; staleGrant?: boolean; readOnly?: boolean; catalog?: 'missing' | 'invalid' | 'empty-grants' | 'two-hosts' | 'retry' | 'pi' | 'pi-openrouter' | 'provisional'; pin?: { ticket_id: string; harness: string; account_id?: string; group_id?: string } } = {}) {
  const work = fixtures()
  if (options.theme) work.preferences.theme = { choice: options.theme }
  await mockWork(page, work, { admin: true })
  const profile: ModelProfile = { id: profileId, slug: 'Build · deliberate', harness: 'codex', family: 'openai', model: 'workspace-build', effort: 'high', tier: 'standard', enabled: true }
  if (options.catalog === 'pi-openrouter') Object.assign(profile, { harness: 'pi', family: 'unknown', model: 'openrouter/stealth/space-bunny-alpha', effort: 'off' })
  const ungranted: ModelProfile = { ...profile, id: 'c2000000-0000-4000-8000-000000000099', slug: 'Ungranted model', model: 'ungranted-model', enabled: true }
  const now = Date.now()
  const span = { starts_at: new Date(now - 60_000).toISOString(), ends_at: new Date(now - 60_000 + 5 * 3_600_000).toISOString() }
  const reasons = [...(options.offline ? ['probe'] as const : []), ...(options.unavailable ? ['state'] as const : []), ...(options.catalog === 'empty-grants' ? ['models'] as const : [])]
  const available = !options.wait && reasons.length === 0 && options.catalog !== 'empty-grants'
  const efforts = options.catalog === 'empty-grants' ? [] : options.catalog === 'pi'
    ? [{ effort: 'xhigh', model_profile_id: profileId, version: '2' }, { effort: 'high', model_profile_id: spareProfileId, version: '2' }]
    : [{ effort: 'high', model_profile_id: profileId, version: '1' }]
  const piModels = [
    { model: 'anthropic/claude-opus-5', family: 'anthropic', efforts: [{ effort: 'xhigh', model_profile_id: profileId, version: '2' }] },
    { model: 'anthropic/claude-sonnet-5', family: 'anthropic', efforts: [{ effort: 'high', model_profile_id: spareProfileId, version: '2' }] },
    { model: 'sk-live-token', family: 'anthropic', efforts: [{ effort: 'high', model_profile_id: 'b1000000-0000-4000-8000-000000000004', version: '2' }] },
  ]
  const primary = {
    wait: options.wait,
    id: accountId, label: 'Workspace account', plan: 'Pro', registered_by_principal_id: agentId,
    state: options.unavailable ? 'draining' : 'available', last_probe_at: new Date(now - (options.offline ? 180_000 : 1000)).toISOString(), last_probe_ok: !options.offline,
    available, unavailable_reasons: reasons, remaining_fraction: available ? 0.8 : null,
    windows: [{ id: 'd3000000-0000-4000-8000-000000000001', account_id: accountId, ...span, unit: 'requests', allowance: 100, used: 20, reserved: 0, pace_model: 'unrestricted', burst_ratio: 0, provisional: false, remaining: 80, pace_remaining: 80 }],
    models: options.catalog === 'pi-openrouter' ? [{ model: profile.model, family: profile.family, efforts: [{ effort: 'off', model_profile_id: profileId, version: '1' }] }] : options.catalog === 'pi' ? piModels : (efforts.length ? [{ model: 'workspace-build', family: 'openai', efforts }] : []),
    default_model_profile_id: efforts.length ? profileId : null,
  }
  const spare = {
    id: spareAccountId, label: 'Spare account', plan: '', registered_by_principal_id: agentId, state: 'available',
    last_probe_at: new Date(now - 1000).toISOString(), last_probe_ok: true, available: true, unavailable_reasons: [], remaining_fraction: options.catalog === 'provisional' ? null : 0.25,
    windows: [{ id: 'd3000000-0000-4000-8000-000000000002', account_id: spareAccountId, starts_at: new Date(now - 3_600_000).toISOString(), ends_at: new Date(now - 3_600_000 + 7 * 86_400_000).toISOString(), unit: 'requests', allowance: 100, used: options.catalog === 'provisional' ? 0 : 75, reserved: 0, pace_model: 'steady', burst_ratio: 0, provisional: options.catalog === 'provisional', remaining: options.catalog === 'provisional' ? 100 : 25, pace_remaining: options.catalog === 'provisional' ? 100 : 25 }],
    models: [{ model: 'spare-model', family: 'openai', efforts: [{ effort: 'low', model_profile_id: spareProfileId, version: '2' }] }],
    default_model_profile_id: spareProfileId,
  }
  const hosts = options.catalog === 'empty-grants' || options.offline || options.unavailable
    ? [{ daemon_id: 'workstation', label: 'Work Mac', harnesses: [{ harness: 'codex', accounts: [primary], default_account_id: available ? accountId : null }] }]
    : [{ daemon_id: 'workstation', label: 'Work Mac', harnesses: [{ harness: options.catalog?.startsWith('pi') ? 'pi' : 'codex', accounts: [primary, spare], default_account_id: accountId }] }]
  if (options.catalog === 'two-hosts') {
    hosts.push({
      daemon_id: 'laptop', label: 'Laptop', harnesses: [{
        harness: 'claude', default_account_id: laptopAccountId, accounts: [{
          ...spare, id: laptopAccountId, label: 'Laptop account', plan: 'Max', registered_by_principal_id: agentId, remaining_fraction: 0.1,
          models: [{ model: 'other-model', family: 'anthropic', efforts: [{ effort: 'medium', model_profile_id: spareProfileId, version: '3' }] }],
          default_model_profile_id: spareProfileId,
        }],
      }],
    })
  }
  const catalog = { as_of: new Date(now).toISOString(), role: 'build', hosts }
  const account: AgentAccount = {
    id: accountId, account_key: 'should-not-render-key', harness: 'codex', daemon_id: 'workstation', label: 'Workspace account', plan: 'Pro', host_label: 'Work Mac',
    registered_by_principal_id: agentId, state: options.unavailable ? 'draining' : 'available', max_parallel_runs: 2,
    last_probe_at: primary.last_probe_at, last_probe_ok: primary.last_probe_ok, created_at: new Date(now).toISOString(),
    windows: [{ id: primary.windows[0].id, account_id: accountId, ...span, unit: 'requests', allowance: 100, used: 20, reserved: 0, pace_model: 'unrestricted', burst_ratio: 0, provisional: false }],
  }
  if (options.catalog === 'pi-openrouter') Object.assign(account, { harness: 'pi', provider: 'openrouter', model: 'stealth/space-bunny-alpha', model_status: 'known' })
  const state = { orders: [] as WorkOrder[], runs: [] as AgentRun[], sessions: [] as HarnessSession[], failQueue: !!options.failQueue, revoked: false, catalogMisses: options.catalog === 'retry' ? 1 : 0 }
  const pins = options.pin ? [{ ...options.pin }] : [] as { ticket_id: string; harness: string; account_id?: string; group_id?: string }[]
  const calls: { method: string; path: string; body: Record<string, unknown> | null }[] = []
  await page.route('**/api/**', async route => {
    const req = route.request(), url = new URL(req.url()), path = url.pathname, method = req.method()
    const json = (value: unknown, status = 200) => route.fulfill({ status, json: value })
    const body = req.postData() ? req.postDataJSON() as Record<string, unknown> : null
    calls.push({ method, path, body })
    if (path === '/api/me/permissions') {
      const permissions = mockEffectivePermissions(options.readOnly ? 'viewer' : 'admin', url.searchParams.get('project_id') ?? undefined)
      if (!options.readOnly) permissions.workspace.permissions.push('run.create', 'run.read', 'models.read', 'account.read', 'work_orders.read')
      return json(permissions)
    }
    if (path === '/api/business/principals') return json([{ id: agentId, name: 'Studio builder', kind: 'agent', roles: [] }, { ...me, kind: 'person', roles: [] }])
    if (path === '/api/models') return json([profile, ungranted, { ...profile, id: 'c2000000-0000-4000-8000-000000000098', slug: 'Retired model', enabled: false }])
    if (path === '/api/agent-accounts/catalog') {
      if (options.forbidden) return json({ error: 'Permission denied' }, 403)
      if (options.catalog === 'missing' || state.catalogMisses > 0) {
        state.catalogMisses = Math.max(0, state.catalogMisses - 1)
        return json({ error: 'not found' }, 404)
      }
      if (options.catalog === 'invalid') return json({ hosts: [{ id: 'legacy', harnesses: [{ harness: 'codex', installed: true }] }] })
      if (url.searchParams.get('role') === 'review-gate' && !url.searchParams.get('author_family')) return json({ error: 'invalid role or author family' }, 400)
      const bodyCatalog = structuredClone(catalog)
      if (state.revoked) {
        const pinned = bodyCatalog.hosts[0].harnesses[0].accounts[0]
        pinned.models = []
        pinned.default_model_profile_id = null
        pinned.available = false
        pinned.unavailable_reasons = ['models']
      }
      return json({ ...bodyCatalog, role: url.searchParams.get('role') || 'build' })
    }
    if (path === '/api/agent-accounts') return json([account])
    if (path === '/api/agent-accounts/capacity') return json([{ account_id: account.id, schedule: defaultSchedule(), windows: [] }])
    if (path === '/api/agent-accounts/capacity/schedule') return json([])
    if (path === '/api/agent-pairing/computers') return json({ computers: [] })
    if (path === '/api/agent-accounts/pins') {
      if (method === 'GET') return json(pins.filter(pin => pin.ticket_id === url.searchParams.get('ticket_id')))
      if (method === 'PUT' && body) {
        const input = body as (typeof pins)[number]
        const index = pins.findIndex(pin => pin.ticket_id === input.ticket_id && pin.harness === input.harness)
        if (index >= 0) pins[index] = input
        else pins.push(input)
        return route.fulfill({ status: 204, body: '' })
      }
      if (method === 'DELETE') {
        const index = pins.findIndex(pin => pin.ticket_id === url.searchParams.get('ticket_id') && pin.harness === url.searchParams.get('harness'))
        if (index >= 0) pins.splice(index, 1)
        return route.fulfill({ status: 204, body: '' })
      }
    }
    if (path === '/api/agent-accounts/groups' && method === 'GET') return json([])
    if (path === '/api/harness-sessions') return json({ items: state.sessions, next_cursor: null })
    if (path === '/api/approvals') return json([])
    if (path.endsWith('/message-targets')) return json([])
    if (path.endsWith('/messages')) return json({ items: [], next_after: 0 })
    if (path === '/api/runs') return json({ items: state.runs.filter(r => !url.searchParams.get('work_order') || r.work_order_id === url.searchParams.get('work_order')), next_cursor: null })
    if (path.endsWith('/capacity-override') && method === 'POST') {
      const run = state.runs.find(r => path === `/api/runs/${r.id}/capacity-override`)
      if (!run) return json({ error: 'not found' }, 404)
      run.capacity_override = 'now'; run.wait = undefined
      return json(run)
    }
    if (path.startsWith('/api/runs/')) return json(state.runs.find(r => path.endsWith(r.id)))
    if (path === '/api/nodes' && url.searchParams.get('kind') === 'work_order') return json({ items: state.orders.map(o => ({ id: o.node_id })), next_cursor: null })
    if (path === '/api/nodes/order-1') return json({ id: 'order-1', title: 'PHAROS-11: Connect Hetzner Cloud for managed provisioning' })
    if (path === '/api/work-orders' && method === 'POST') {
      const order: WorkOrder = { node_id: 'order-1', status: 'draft', revision: 1, assignee_principal_id: String(body!.assignee_principal_id), criteria: (body!.criteria as string[]).map((description, i) => ({ id: `criterion-${i}`, description, checked_at: null })) }
      state.orders.push(order)
      return json(order, 201)
    }
    if (path === '/api/work-orders/order-1') {
      if (method === 'PATCH') {
        if (body!.expected_revision !== state.orders[0].revision) return json({ error: 'revision conflict' }, 409)
        Object.assign(state.orders[0], body, { revision: state.orders[0].revision + 1 })
      }
      return json(state.orders[0])
    }
    if (path === '/api/work-orders/order-1/runs' && method === 'POST') {
      if (state.failQueue) return json({ error: 'Temporary queue failure' }, 503)
      if (options.staleGrant && !state.revoked) {
        state.revoked = true
        return json({ error: 'requested account must belong to the run agent and allow the model profile' }, 409)
      }
      const run: AgentRun = { capacity_override: body?.capacity_override === 'now' ? 'now' : '', wait: body?.capacity_override === 'now' ? undefined : options.wait, id: 'run-1', work_order_id: 'order-1', agent_principal_id: String(body!.agent_principal_id), model_profile_id: String(body!.model_profile_id), status: 'queued', model_evidence: 'unverified', requested_model: profile.model, input_tokens: 0, output_tokens: 0, cost_micros: 0, created_at: new Date().toISOString() }
      if (body!.requested_account_id) run.requested_account_id = String(body!.requested_account_id)
      state.runs.push(run)
      return json(run, 201)
    }
    return route.fallback()
  })
  function claim(register = false, reported: Partial<Pick<HarnessSession, 'model' | 'account_label' | 'reasoning_effort'>> = {}) {
    state.runs[0].status = 'starting'
    if (register) state.sessions.push({
      id: 'managed-1', project_id: 'p-pharos', agent_principal_id: agentId, run_id: 'run-1', ticket_node_id: 'n-1', work_order_id: 'order-1', parent_harness_session_id: null, harness: 'codex', host: 'workstation', management_mode: 'managed', role: 'worker', work_shape: 'ship', advertised_capabilities: ['interrupt', 'stop'], phase: 'starting', activity: 'unknown', activity_sequence: 1, revision: 1, heartbeat_at: new Date().toISOString(), stopped_at: null, stop_reason: null, finished: false, created_at: new Date().toISOString(),
      model: 'model' in reported ? reported.model : 'workspace-build',
      account_label: 'account_label' in reported ? reported.account_label : 'Workspace account',
      reasoning_effort: 'reasoning_effort' in reported ? reported.reasoning_effort : 'high',
      project: { id: 'p-pharos', key: 'PHAROS', title: 'Pharos' }, ticket: { id: 'n-1', key: 'PHAROS-11', title: 'Connect Hetzner Cloud for managed provisioning' }, agent: { id: agentId, name: 'Studio builder' },
    })
  }
  return { state, calls, claim, account, profile, agentId, spareProfileId, spareAccountId, pins }
}
