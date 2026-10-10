// SPDX-License-Identifier: AGPL-3.0-only
// AEON-741: the lead lifecycle (AEON-734), its settings, decisions, the Queue and
// parent snapshots (AEON-740) on top of the in-memory work API.
import type { Page } from '@playwright/test'
import { fixtures, me, mockWork, type LiveAgentMock } from './work-fixtures'
import { mockEffectivePermissions } from './authz-fixtures'
import { pairedComputer } from './agent-pairing-fixtures'
import type { LeadDecision, LeadState, ProjectLead } from '../src/lib/lead'
import type { QueueWireEntry } from '../src/lib/workQueue'

export const NOW = '2026-10-06T10:00:00Z'
export const LEAD_SESSION = 'aaaaaaaa-0000-4000-8000-000000000001'
export const LEAD_AGENT = 'aaaaaaaa-0000-4000-8000-0000000000a1'
export const HOST_A = '77777777-7777-4777-8777-777777777771'
export const HOST_B = '77777777-7777-4777-8777-777777777772'
export interface LeadFlowOptions {
  /** PHAROS's lead; 'none' until a person starts one. */
  lead?: LeadState | 'none'; reason?: string; processActive?: boolean
  /** AEON's lead, for the Agents page list. */
  aeon?: LeadState | 'none'
  queue?: string[]; expert?: boolean; question?: boolean; decisions?: boolean; owner?: boolean
  longText?: boolean
  /** Simulates a future qualified workspace for the existing start-flow tests.
   * Production availability tests explicitly pass false; servers still ship false. */
  launchEnabled?: boolean
  leadName?: string
}
export async function mockLeadFlow(page: Page, options: LeadFlowOptions = {}) {
  const data = fixtures()
  const parents = new Set(data.nodes.map(n => n.parent_id))
  for (const node of data.nodes) {
    if (!['ticket', 'task', 'epic'].includes(node.kind_slug)) continue
    node.kind_slug = 'work'; node.is_leaf = !parents.has(node.id); node.work_children_count = data.nodes.filter(n => n.parent_id === node.id).length
    node.fields = { ...node.fields, estimate_hours: 2, acceptance_criteria: '- [ ] Verified with evidence' }
  }
  const n4 = data.nodes.find(n => n.id === 'n-4')!
  n4.state = 'open'
  if (options.longText) n4.title = 'Agenten-Dial zeigt „Konten: gerade voll“, obwohl nichts läuft, und verschweigt die nicht gemessene Reserve'
  if (options.expert) data.preferences['developer-ui'] = { show_expert_start: true }
  const pharos = (state: LeadState | 'none'): ProjectLead => state === 'none'
    ? { project_id: 'p-pharos', revision: 0, generation: 0, session_id: null, state: 'none', reason: '', process_active: false, automatic_launch_enabled: options.launchEnabled ?? true }
    : { project_id: 'p-pharos', revision: 4, generation: 2, session_id: LEAD_SESSION, state, reason: options.reason ?? '', process_active: options.processActive ?? state !== 'paused', automatic_launch_enabled: options.launchEnabled ?? true }
  const state = {
    leads: { 'p-pharos': pharos(options.lead ?? 'working'), 'p-aeon': { project_id: 'p-aeon', revision: options.aeon && options.aeon !== 'none' ? 1 : 0, generation: 0, session_id: null, state: options.aeon ?? 'none', reason: options.aeon === 'paused' ? 'idle_yield' : '', process_active: false, automatic_launch_enabled: options.launchEnabled ?? true } as ProjectLead } as Record<string, ProjectLead>,
    queue: options.queue ?? ['n-4'],
    calls: [] as { method: string; path: string; body: unknown }[],
    settings: { revision: 2, workspace_revision: 1, owner_person_id: me.id, overrides: {}, effective: {}, model_revisions: [1], automatic_launch_enabled: options.launchEnabled ?? true, details_redacted: options.owner === false, dial_path: '/api/agents/plan',
      model_selector: { cell: { mode: 'pick', family: 'anthropic', line: 'Claude Opus 5.5', effort: 'high' }, set_by: 'workspace', kind_fallback: false, prefs_revision: 1 }, required_start_gates: ['dial', 'harness', 'account_room', 'host_load'] } as Record<string, unknown>,
  }
  if (options.lead !== 'none' && options.lead !== 'paused') {
    data.live.push({ project_id: 'p-pharos', session_id: LEAD_SESSION, principal_id: LEAD_AGENT, name: 'Claude lead', harness: 'claude', management_mode: 'managed', role: 'coordinator', phase: 'working', activity: 'busy', ticket: null,
      since: '2026-10-06T08:02:00Z', heartbeat_at: '2026-10-06T09:59:40Z', finished: false, model: 'Claude Opus 5.5', reasoning_effort: 'high', activity_note: 'Fixing three review findings on PHAROS-11 with a Codex worker.' } as LiveAgentMock)
    data.live.push({ project_id: 'p-pharos', session_id: 'aaaaaaaa-0000-4000-8000-000000000002', principal_id: 'aaaaaaaa-0000-4000-8000-0000000000a2', name: 'Codex worker', harness: 'codex', management_mode: 'managed', role: 'worker', phase: 'working', activity: 'busy',
      ticket: { id: 'n-1', key: 'PHAROS-11', title: 'Connect Hetzner Cloud for managed provisioning', project_id: 'p-pharos' }, since: '2026-10-06T09:46:00Z', heartbeat_at: '2026-10-06T09:59:40Z', finished: false, model: 'gpt-6-sol', reasoning_effort: 'medium', activity_note: 'fixing 3 review findings' } as LiveAgentMock)
  }
  const decisions: LeadDecision[] = options.decisions === false ? [] : [
    { event_id: 11, project_id: 'p-pharos', session_id: LEAD_SESSION, recorded_at: '2026-10-06T08:55:00Z', outcome: 'selected', reason_codes: ['oldest_eligible'], gate_freshness: [], results: [], request: { ticket_node_id: 'n-1', stage: 'queue', outcome: 'selected', reason_codes: ['oldest_eligible'], attempt: 1 } },
    { event_id: 12, project_id: 'p-pharos', session_id: LEAD_SESSION, recorded_at: '2026-10-06T09:02:00Z', outcome: 'selected', reason_codes: ['gates_ready'], results: [], request: { ticket_node_id: 'n-1', stage: 'admission', outcome: 'selected', reason_codes: ['gates_ready'], attempt: 1 },
      gate_freshness: [{ kind: 'dial', freshness: 'fresh' }, { kind: 'harness', freshness: 'fresh' }, { kind: 'account_room', freshness: 'fresh' }, { kind: 'host_load', freshness: options.reason === 'host_unavailable' ? 'unreadable' : 'fresh' }] },
    { event_id: 13, project_id: 'p-pharos', session_id: LEAD_SESSION, recorded_at: '2026-10-06T09:20:00Z', outcome: 'requested', reason_codes: ['review_requested'], gate_freshness: [], results: [], request: { ticket_node_id: 'n-3', stage: 'review', outcome: 'requested', reason_codes: ['review_requested'], attempt: 1 } },
    { event_id: 14, project_id: 'p-pharos', session_id: LEAD_SESSION, recorded_at: '2026-10-06T09:41:00Z', outcome: 'handoff', reason_codes: ['release_handoff'], gate_freshness: [], results: [], request: { ticket_node_id: 'n-5', stage: 'release_handoff', outcome: 'handoff', reason_codes: ['release_handoff'], attempt: 1 } },
  ]
  const question = {
    id: 'qqqqqqqq-0000-4000-8000-000000000001', project_id: 'p-pharos', revision: 1, state: 'open', suggested_outcome: 'once', suggestion_reason: 'agent_suggestion', created_at: NOW, updated_at: NOW, pending: [],
    input: { request_id: 'r1', question: 'Show “not measured” or hide the Accounts line while nothing runs?', options: [{ id: 'a', title: 'Not measured', description: '', answer: 'a' }, { id: 'b', title: 'Hide', description: '', answer: 'b' }], recommend: 'a', meanwhile: 'carries_on' },
  }
  const askers = [{ id: 'k1', principal_id: LEAD_AGENT, reply_root_id: 'x', comment_node_id: 'y', input: question.input }]
  const entries = (project?: string | null): QueueWireEntry[] => state.queue.map((id, i) => {
    const node = data.nodes.find(n => n.id === id)!
    return { node_id: id, project_id: node.project, key: node.key, title: node.title, state: node.state, priority: String(node.fields.priority ?? 'medium'), estimate_hours: 2,
      queued: { run_id: `run-${id}`, position: i + 1, by: { ...me, kind: 'person' as const }, at: '2026-10-06T09:30:00Z', target_agent_id: null, expected_agent_id: null, model_profile_id: null, expected_start_at: null, waiting: options.lead === 'waiting_for_room', wait_reason: options.lead === 'waiting_for_room' ? 'Waiting for room' : '' } }
  }).filter(entry => !project || entry.project_id === project)

  // Controlled HTTP snapshots: a quiet stream keeps an unmocked SSE disconnect from invalidating the live read.
  await page.addInitScript(() => {
    class QuietStream extends EventTarget { close() {} }
    Object.assign(window, { EventSource: QuietStream })
  })
  const calls = await mockWork(page, data, { admin: true })
  await page.route('**/api/**', async route => {
    const req = route.request(), url = new URL(req.url()), path = url.pathname, method = req.method()
    const body = req.postData() ? req.postDataJSON() as Record<string, unknown> : null
    const json = (value: unknown, status = 200) => route.fulfill({ status, json: value })
    if (path === '/api/settings/work-vocabulary' && options.leadName) return json({ revision: 1, leaf: { name: '', icon: '' }, levels: [], lead: { singular: options.leadName, plural: `${options.leadName}n` } })
    if (path === '/api/me/permissions') {
      const permissions = mockEffectivePermissions('admin', url.searchParams.get('project_id') ?? undefined)
      permissions.workspace.permissions.push('run.create', 'harness.read', 'harness.control', 'work_orders.read')
      if (permissions.project) permissions.project.permissions.push('run.create', 'harness.read', 'harness.control', 'work_orders.read')
      return json(permissions)
    }
    const lead = /^\/api\/projects\/([^/]+)\/lead(\/pause)?$/.exec(path)
    if (lead) {
      state.calls.push({ method, path, body })
      const current = state.leads[lead[1]!]
      if (!current) return json({ error: 'project not found' }, 404)
      if (method === 'GET') return json(current)
      if (method === 'DELETE') {
        if (body?.expected_revision !== current.revision) return json({ error: 'lead revision conflict' }, 409)
        if (current.revision === 0 || current.generation !== 0 || current.session_id !== null) return json({ error: 'only a never-started lead can be removed' }, 409)
        state.leads[lead[1]!] = { ...current, state: 'none', reason: '', revision: 0, generation: 0, session_id: null, process_active: false }
        return json(state.leads[lead[1]!])
      }
      if (!lead[2]) {
        if (body?.expected_revision !== current.revision) return json({ error: 'lead revision conflict' }, 409)
        state.leads[lead[1]!] = { ...current, state: 'waiting_for_room', reason: 'awaiting_generation', revision: current.revision + 1, session_id: null, process_active: false }
        return json(state.leads[lead[1]!])
      }
      if (body?.expected_revision !== current.revision || body?.generation !== current.generation) return json({ error: 'lead revision or generation conflict' }, 409)
      state.leads[lead[1]!] = { ...current, state: 'paused', reason: 'handover_pending', revision: current.revision + 1 }
      return json(state.leads[lead[1]!])
    }
    if (/^\/api\/projects\/[^/]+\/lead-settings$/.test(path)) {
      state.calls.push({ method, path, body })
      if (method === 'PUT') { state.settings = { ...state.settings, overrides: body!.overrides, revision: Number(state.settings.revision) + 1 }; return json(state.settings) }
      return json(state.settings)
    }
    if (/^\/api\/projects\/p-pharos\/lead-decisions$/.test(path)) return json({ items: decisions.filter(d => d.event_id > Number(url.searchParams.get('after') ?? 0)), next_after: null })
    if (path === '/api/decision-desk') return json({ items: options.question ? [{ ...question, askers }] : [], has_more: false })
    if (path === '/api/projects/p-pharos/questions') return json({ items: options.question ? [{ ...question, askers }] : [], has_more: false })
    // The lead's agent, read once per session so its questions outlive the session.
    if (path === `/api/projects/p-pharos/harness-sessions/${LEAD_SESSION}` && method === 'GET') return json({ id: LEAD_SESSION, project_id: 'p-pharos', agent_principal_id: LEAD_AGENT, role: 'coordinator', harness: 'claude', phase: options.lead === 'paused' ? 'stopped' : 'working' })
    if (path === '/api/agents/plan') return json({ principal_id: me.id, total: 5, limits: {}, running: { codex: 1, claude: 2 }, running_total: 3, source: 'plan', updated_at: NOW })
    if (path === '/api/agent-pairing/computers') return json({ computers: [
      pairedComputer({ request_id: '88888888-8888-4888-8888-888888888881', computer_id: HOST_A, computer_name: 'build-7' }),
      pairedComputer({ request_id: '88888888-8888-4888-8888-888888888882', computer_id: HOST_B, computer_name: 'build-6', computer_state: 'draining' }),
    ] })
    const lifecycle = /^\/api\/nodes\/([^/]+)\/work-lifecycle$/.exec(path)
    if (lifecycle && method === 'GET') {
      const node = data.nodes.find(n => n.id === lifecycle[1])!
      const open = data.nodes.filter(n => n.parent_id === node.id && !['done', 'cancelled'].includes(n.state)).length
      return json({ is_leaf: node.is_leaf !== false, busy: false, open_leaves: open, updated_at: node.updated_at, scope_revision: 'a'.repeat(64), pending: null })
    }
    if (/^\/api\/nodes\/[^/]+\/reviews$/.test(path)) return json([])
    if (!path.startsWith('/api/queue')) return route.fallback()
    state.calls.push({ method, path, body })
    if (path === '/api/queue' && method === 'GET') { const items = entries(url.searchParams.get('project_id')); return json({ items, count: items.length, manual_order: false, capacity: { queued_hours: 2, parallel_runs: 1, work_hours: 2, warning: false } }) }
    if (path === '/api/queue' && method === 'POST') { const id = String(body!.node_id); if (!state.queue.includes(id)) state.queue.push(id); return json(entries().find(e => e.node_id === id)) }
    const snap = /^\/api\/queue\/([^/]+)\/snapshots$/.exec(path)
    if (snap) return json({ id: 'snap-1', parent_id: snap[1], parent_revision: body!.expected_revision, tree_revision: 'b'.repeat(64), state: 'pending', truncated: false, continuation_available: false, partial: false, tree_changed: false, items: [{ node_id: 'n-3', revision: NOW, outcome: 'pending' }] })
    if (path === '/api/queue-snapshots/snap-1/apply') { if (!state.queue.includes('n-3')) state.queue.push('n-3'); return json({ id: 'snap-1', parent_id: 'n-2', parent_revision: NOW, tree_revision: 'b'.repeat(64), state: 'applied', truncated: false, continuation_available: false, partial: false, tree_changed: false, items: [{ node_id: 'n-3', revision: NOW, outcome: 'queued', run_id: 'run-n-3' }] }) }
    const one = /^\/api\/queue\/([^/]+)(?:\/(readiness))?$/.exec(path)
    if (one && method === 'DELETE') { state.queue = state.queue.filter(id => id !== one[1]); return json({ removed: true }) }
    if (one?.[2] === 'readiness') return json({ queueable: true, ready: true, missing: [], suggested_estimate_hours: 2, security_review_required: false })
    return json({ error: 'Unknown queue endpoint' }, 404)
  })
  await page.clock.setSystemTime(new Date(NOW))
  return { data, state, calls }
}
