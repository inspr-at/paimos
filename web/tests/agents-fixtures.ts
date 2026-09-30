// SPDX-License-Identifier: AGPL-3.0-only
// An in-memory agents backend for specs and review captures: harness sessions per
// project with typed controls, runs, approvals, accounts with allowance windows,
// message targets and project messages. Ids for projects, tickets and the person
// come from the caller, so captures can sit on real projects and tickets.
import type { Page, Route } from '@playwright/test'
import { defaultSchedule, type CapacityWorld } from './capacity-fixtures'

export interface AgentWorld {
  me: string
  projects: { pharos: string; aeon: string; pai: string }
  tickets: { fleet: string; restore: string; web: string; release: string; approvals: string }
  now?: number
  empty?: boolean
  // Node summaries the tenant-wide sessions list carries: id -> key and title.
  nodes?: Record<string, { key: string; title: string }>
}
const agent = (n: number) => `a0000000-0000-4000-8000-00000000000${n}`
const id = (prefix: string, n: number) => `${prefix}000000-0000-4000-8000-0000000000${String(n).padStart(2, '0')}`

export function agentData(world: AgentWorld) {
  const now = world.now ?? Date.now()
  const ago = (minutes: number) => new Date(now - minutes * 60_000).toISOString()
  const ahead = (minutes: number) => new Date(now + minutes * 60_000).toISOString()
  const { pharos, aeon, pai } = world.projects
  const t = world.tickets
  const names: Record<string, string> = {
    [agent(1)]: 'claude:camy', [agent(2)]: 'codex:nova', [agent(3)]: 'pi:pixel', [agent(4)]: 'cursor:kite',
    [agent(5)]: 'grok:amy', [agent(6)]: 'claude:sable', [agent(7)]: 'codex:orbit',
  }
  const base = { parent_harness_session_id: null, work_order_id: null, management_mode: 'managed', role: 'worker', advertised_capabilities: ['inbox', 'status', 'steer', 'interrupt', 'stop'], activity_sequence: 3, revision: 2, stopped_at: null, stop_reason: null }
  const summary = (nodeId: unknown) => typeof nodeId === 'string' && world.nodes?.[nodeId] ? { id: nodeId, ...world.nodes[nodeId] } : null
  const session = (n: number, fields: Record<string, unknown>) => ({ ...base, id: id('5e', n), ...fields, project: summary(fields.project_id) ?? { id: fields.project_id, key: '', title: '' }, ticket: summary(fields.ticket_node_id) })
  const sessions = world.empty ? [] : [
    session(1, { project_id: pharos, agent_principal_id: agent(1), run_id: id('70', 1), ticket_node_id: t.fleet, harness: 'claude', host: 'imac0', role: 'coordinator', work_shape: 'ship', phase: 'working', activity: 'busy', heartbeat_at: ago(0.2), created_at: ago(72) }),
    session(2, { project_id: pharos, agent_principal_id: agent(2), run_id: id('70', 2), ticket_node_id: t.restore, harness: 'codex', host: 'mba', work_shape: 'ship', phase: 'working', activity: 'busy', heartbeat_at: ago(0.4), created_at: ago(26) }),
    session(3, { project_id: aeon, agent_principal_id: agent(3), run_id: id('70', 3), ticket_node_id: t.web, harness: 'pi', host: 'hsb1', work_shape: 'scout', phase: 'working', activity: 'idle', heartbeat_at: ago(0.7), created_at: ago(140) }),
    session(4, { project_id: pai, agent_principal_id: agent(4), run_id: id('70', 4), ticket_node_id: t.release, harness: 'cursor', host: 'mba', work_shape: 'ship', phase: 'yielded', activity: 'idle', heartbeat_at: ago(1), created_at: ago(51) }),
    session(5, { project_id: pharos, agent_principal_id: agent(5), run_id: null, ticket_node_id: null, harness: 'grok', host: 'csb1', management_mode: 'unmanaged', advertised_capabilities: ['inbox', 'status'], work_shape: 'unknown', phase: 'working', activity: 'busy', heartbeat_at: ago(9), created_at: ago(300) }),
    session(6, { project_id: aeon, agent_principal_id: agent(6), run_id: id('70', 7), ticket_node_id: t.approvals, harness: 'claude', host: 'imac0', work_shape: 'ship', phase: 'starting', activity: 'unknown', heartbeat_at: ago(0.1), created_at: ago(1) }),
    session(7, { project_id: pharos, agent_principal_id: agent(1), run_id: id('70', 5), ticket_node_id: t.restore, harness: 'claude', host: 'imac0', work_shape: 'ship', phase: 'stopped', activity: 'idle', heartbeat_at: ago(130), stopped_at: ago(128), stop_reason: 'completed', created_at: ago(190) }),
    session(8, { project_id: pharos, agent_principal_id: agent(1), run_id: id('70', 6), ticket_node_id: null, harness: 'claude', host: 'imac0', work_shape: 'unknown', phase: 'stopped', activity: 'idle', heartbeat_at: ago(1500), stopped_at: ago(1490), stop_reason: 'operator_stop', created_at: ago(1600) }),
    session(9, { project_id: pai, agent_principal_id: agent(7), run_id: id('70', 8), ticket_node_id: null, harness: 'codex', host: 'hsb1', work_shape: 'unknown', phase: 'stopped', activity: 'idle', heartbeat_at: ago(400), stopped_at: ago(395), stop_reason: 'lease_expired', created_at: ago(700) }),
  ]
  const account = { claude: id('ac', 1), codex: id('ac', 2), cursor: id('ac', 3), pi: id('ac', 4), grok: id('ac', 5) }
  const run = (n: number, fields: Record<string, unknown>) => ({ id: id('70', n), work_order_id: id('0d', n), model_profile_id: null, requested_model: null, model_evidence: 'vendor_reported', input_tokens: 0, output_tokens: 0, cost_micros: 0, started_at: null, ended_at: null, ...fields })
  const runs = [
    run(1, { agent_principal_id: agent(1), account_id: account.claude, status: 'running', effective_model: 'claude-fable-high', input_tokens: 184_300, output_tokens: 22_140, cost_micros: 3_840_000, started_at: ago(71), created_at: ago(72) }),
    run(2, { agent_principal_id: agent(2), account_id: account.codex, status: 'waiting', effective_model: 'codex-astra-xhigh', input_tokens: 41_200, output_tokens: 5_900, cost_micros: 610_000, started_at: ago(25), created_at: ago(26) }),
    run(3, { agent_principal_id: agent(3), account_id: account.pi, status: 'running', effective_model: 'pi-anthropic-sonnet-high', input_tokens: 96_000, output_tokens: 8_400, cost_micros: 1_120_000, started_at: ago(139), created_at: ago(140) }),
    run(4, { agent_principal_id: agent(4), account_id: account.cursor, status: 'waiting', effective_model: 'cursor-composer-2-5-fast', input_tokens: 58_700, output_tokens: 12_300, cost_micros: 940_000, started_at: ago(50), created_at: ago(51) }),
    run(5, { agent_principal_id: agent(1), account_id: account.claude, status: 'completed', effective_model: 'claude-fable-high', input_tokens: 212_000, output_tokens: 31_500, cost_micros: 4_620_000, started_at: ago(189), ended_at: ago(129), created_at: ago(190) }),
    run(6, { agent_principal_id: agent(1), account_id: account.claude, status: 'failed', effective_model: 'claude-opus-high', model_evidence: 'unverified', input_tokens: 18_900, output_tokens: 1_200, cost_micros: 410_000, started_at: ago(1599), ended_at: ago(1591), created_at: ago(1600) }),
    run(7, { agent_principal_id: agent(6), account_id: account.claude, status: 'starting', effective_model: null, requested_model: 'claude-fable-xhigh', model_evidence: 'unverified', created_at: ago(1) }),
    run(8, { agent_principal_id: agent(7), account_id: account.codex, status: 'ownership_lost', effective_model: 'codex-luna-high', input_tokens: 33_000, output_tokens: 2_100, cost_micros: 380_000, started_at: ago(699), ended_at: ago(395), created_at: ago(700) }),
  ]
  // Risk as the server computes it (B7): tenant-wide or control/deploy/delete is high, reads low, the rest medium.
  const risk = (scope: string, kind: string) => kind === 'tenant' || /control|deploy|delete/.test(scope) ? 'high' : /\.read$/.test(scope) ? 'low' : 'medium'
  const approval = (n: number, fields: Record<string, unknown>) => ({ id: id('a9', n), agent_name: null as string | null, resource_id: null, run_id: null, decision: null, decided_by_principal_id: null, ...fields, risk: risk(String(fields.scope), String(fields.resource_kind)) })
  const approvals = world.empty ? [] : [
    approval(1, { agent_principal_id: agent(1), scope: 'harness.control', resource_kind: 'node', resource_id: pharos, rationale: 'Stop the Grok scout on csb1: it lost its heartbeat and still holds the fleet list lock.', expires_at: ahead(8), proposed_at: ago(4) }),
    approval(2, { agent_principal_id: agent(2), scope: 'run.claim', resource_kind: 'run', resource_id: id('70', 2), run_id: id('70', 2), rationale: 'Claim the restore run on the Codex Pro account; the Claude window is ahead of pace.', expires_at: ahead(38), proposed_at: ago(3) }),
    approval(3, { agent_principal_id: agent(3), scope: 'nodes.read', resource_kind: 'node', resource_id: t.web, rationale: 'Read the web ticket and its children to scout the agents workspace.', expires_at: ahead(130), proposed_at: ago(12) }),
    approval(4, { agent_principal_id: agent(1), scope: 'run.claim', resource_kind: 'run', resource_id: id('70', 5), run_id: id('70', 5), rationale: 'Claim the fleet list run.', expires_at: ago(100), proposed_at: ago(200), decision: 'approved', decided_by_principal_id: world.me }),
    approval(5, { agent_principal_id: agent(7), scope: 'stage.deploy', resource_kind: 'tenant', rationale: 'Deploy the staging stage for the release check.', expires_at: ago(300), proposed_at: ago(420), decision: 'denied', decided_by_principal_id: world.me }),
    approval(6, { agent_principal_id: agent(4), scope: 'inbox.send', resource_kind: 'node', resource_id: pai, rationale: 'Message the coordinator about the release lock.', expires_at: ago(20), proposed_at: ago(60), decision: 'approved', decided_by_principal_id: world.me }),
    approval(7, { agent_principal_id: agent(5), scope: 'work_orders.write', resource_kind: 'node', resource_id: pharos, rationale: 'Record scout findings on the work order.', expires_at: ago(30), proposed_at: ago(90) }),
  ]
  const window = (unit: string, allowance: number, used: number, started: number, length: number, pace = 'steady') => ({ id: `${unit}-${allowance}`, starts_at: ago(started), ends_at: ahead(length - started), unit, allowance, used, reserved: 0, pace_model: pace, burst_ratio: 0.05, set_by_you: true })
  const acct = (key: keyof typeof account, label: string, state: string, windows: unknown[], plan = '') => ({ id: account[key], account_key: `${key}-studio`, harness: key, daemon_id: 'imac0', label, plan, host_label: 'imac0', registered_by_principal_id: world.me, state, max_parallel_runs: 2, last_probe_at: ago(2), last_probe_ok: state !== 'unavailable', created_at: ago(60 * 24 * 30), windows })
  const accounts = [
    acct('claude', 'Claude Max', 'available', [window('tokens', 5_000_000, 3_600_000, 150, 300)], 'Team'),
    acct('codex', 'Codex Pro', 'available', [window('requests', 1500, 450, 60 * 14, 60 * 24)]),
    acct('cursor', 'Cursor Business', 'available', [window('cost_micros', 200_000_000, 96_000_000, 60 * 24 * 15, 60 * 24 * 30, 'frontload')]),
    acct('pi', 'Pi on hsb1', 'draining', []),
    acct('grok', 'SuperGrok', 'unavailable', []),
  ]
  const targets = Object.entries(names).map(([principal, address], n) => ({ id: id('7a', n + 1), principal_id: principal, address, adapter: 'agentd_claude', target_kind: 'agentd_session', maximum_level: 'steer', role: 'primary', version: 1, enabled: true, has_secret: false, created_at: ago(900) }))
  let event = 100
  const minutes = [52, 47, 31, 6, 9]
  const message = (project: string, from: string, to: string, body: string, fields: Record<string, unknown> = {}) => ({
    id: id('3e', ++event - 100), project, sender_principal_id: from, recipient_principal_id: Object.entries(names).find(([, a]) => a === to)?.[0] ?? world.me, to,
    body, reply_to: null as string | null, sent_event_id: event, is_action_request: false, expects_reply: false, delivery_level: 'simple', status: 'accepted', reply_obligation: 'none',
    created_at: ago(minutes[event - 101] ?? 1), human_resolution_outcome: null as string | null, ...fields,
  })
  const messages = world.empty ? [] : [
    message(pharos, world.me, 'claude:camy', 'Take the fleet list next. Keep the card density we agreed in the design review.'),
    message(pharos, agent(1), 'paimos:markus', 'On it. The list needs the health column back; I will reuse the card metrics instead of a new query.'),
    message(pharos, world.me, 'claude:camy', 'Skip the sparkline for now and ship the counts first.', { delivery_level: 'steer' }),
    message(pharos, agent(1), 'paimos:markus', 'Counts are in. Should stale hosts sort last, or keep their position with a muted row?', { expects_reply: true, reply_obligation: 'open' }),
    message(pai, agent(4), 'claude:camy', 'Please merge the release fix once CI is green; I cannot take the release lock from this session.', { is_action_request: true, status: 'held', expects_reply: true, reply_obligation: 'open' }),
  ]
  return { me: world.me, sessions, runs, approvals, accounts, targets, messages, controls: [] as Record<string, unknown>[], sent: [] as (typeof messages[number])[] }
}
export type AgentData = ReturnType<typeof agentData>

export interface MockAccountGroup { id: string; harness: string; name: string; exclusive: boolean; account_ids: string[]; project_ids: string[] }
export interface MockTicketPin { ticket_id: string; harness: string; account_id?: string; group_id?: string }
export interface AgentMockOptions {
  sessionsMissing?: boolean
  messagesMissing?: boolean
  accountsForbidden?: boolean
  failDecision?: boolean
  readMark?: { sessionId: string; event: number; id: string }
  // The next N marker PUTs fail. The call is still recorded.
  failReadMarks?: number
  capacity?: CapacityWorld
  capacityForbidden?: boolean
  groups?: MockAccountGroup[]
  pins?: MockTicketPin[]
}
// Routes only the agents surfaces; everything else falls through to earlier routes
// or the real server.
export async function mockAgents(page: Page, data: AgentData, options: AgentMockOptions = {}) {
  const calls: { path: string; method: string; body: unknown; query?: URLSearchParams }[] = []
  const groups: MockAccountGroup[] = options.groups ? options.groups.map(group => ({ ...group, account_ids: [...group.account_ids], project_ids: [...group.project_ids] })) : []
  const pins: MockTicketPin[] = options.pins ? options.pins.map(pin => ({ ...pin })) : []
  const readMarkers = new Map<string, { last_read_message_id: string; last_read_event_id: number; read_at: string }>()
  if (options.readMark) {
    readMarkers.set(options.readMark.sessionId, { last_read_message_id: options.readMark.id, last_read_event_id: options.readMark.event, read_at: '2026-09-29T05:00:00.000Z' })
  }
  const handler = async (route: Route) => {
    const request = route.request(), url = new URL(request.url()), path = url.pathname, method = request.method()
    let body: unknown = null
    try { body = request.postDataJSON() } catch { body = null }
    const provenancePath = /^\/api\/projects\/([^/]+)\/harness-sessions\/([^/]+)\/provenance$/.exec(path)
    const readMarkerPath = /^\/api\/projects\/([^/]+)\/harness-sessions\/([^/]+)\/read-marker$/.exec(path)
    const sessionsPath = /^\/api\/projects\/([^/]+)\/harness-sessions(?:\/([^/]+)(?:\/controls\/([^/]+))?)?$/.exec(path)
    const messagesPath = /^\/api\/projects\/([^/]+)\/(messages|message-targets)$/.exec(path)
    const resolutionPath = /^\/api\/projects\/([^/]+)\/messages\/([^/]+)\/resolution$/.exec(path)
    const known = provenancePath || readMarkerPath || sessionsPath || messagesPath || resolutionPath || path === '/api/harness-sessions' || path === '/api/runs' || path === '/api/approvals' || path.startsWith('/api/approvals/') || path.startsWith('/api/agent-accounts') || path.startsWith('/api/runs/')
    const pairing = !!options.capacity && path === '/api/agent-pairing/computers'
    if (!known && !pairing) return route.fallback()
    calls.push({ path, method, body, query: url.searchParams })
    const q = url.searchParams
    if (path === '/api/harness-sessions') {
      if (options.sessionsMissing) return route.fulfill({ status: 404, json: { error: 'not found' } })
      const items = data.sessions
        .filter(s => (!q.get('ticket') || s.ticket_node_id === q.get('ticket')) && (!q.get('agent') || s.agent_principal_id === q.get('agent')) && (!q.get('project') || s.project_id === q.get('project')))
        .sort((a, b) => Date.parse(String(b.created_at)) - Date.parse(String(a.created_at)))
      return route.fulfill({ json: { items: items.slice(0, Number(q.get('limit') ?? 50)), next_cursor: null } })
    }
    if (path === '/api/runs') {
      const session = q.get('session') ? data.sessions.find(s => s.id === q.get('session')) : undefined
      const items = data.runs
        .filter(r => (!q.get('agent') || r.agent_principal_id === q.get('agent')) && (!session || r.id === session.run_id))
        .sort((a, b) => Date.parse(String(b.created_at)) - Date.parse(String(a.created_at)))
      return route.fulfill({ json: { items: items.slice(0, Number(q.get('limit') ?? 50)), next_cursor: null } })
    }
    if (resolutionPath) {
      const found = data.messages.find(m => m.id === resolutionPath[2])
      if (!found || !found.is_action_request) return route.fulfill({ status: 409, json: { error: 'message is not held' } })
      const decision = (body as { decision: string }).decision
      found.human_resolution_outcome = decision
      return route.fulfill({ json: { message_id: found.id, decision, created_at: new Date().toISOString() } })
    }
    if (readMarkerPath) {
      const sessionId = readMarkerPath[2]!
      const current = readMarkers.get(sessionId) ?? null
      if (method === 'PUT') {
        const input = body as { last_read_message_id?: string; last_read_event_id?: number }
        if (typeof input?.last_read_message_id !== 'string' || typeof input.last_read_event_id !== 'number') {
          return route.fulfill({ status: 400, json: { error: 'invalid read marker' } })
        }
        if ((options.failReadMarks ?? 0) > 0) {
          options.failReadMarks! -= 1
          return route.fulfill({ status: 503, json: { error: 'unavailable' } })
        }
        if (!current || input.last_read_event_id > current.last_read_event_id) {
          readMarkers.set(sessionId, { last_read_message_id: input.last_read_message_id, last_read_event_id: input.last_read_event_id, read_at: '2026-09-29T06:10:00.000Z' })
        }
      } else if (method !== 'GET') return route.fulfill({ status: 405, json: { error: 'method not allowed' } })
      const stored = readMarkers.get(sessionId)
      return route.fulfill({ json: { session_id: sessionId, last_read_message_id: stored?.last_read_message_id ?? null, last_read_event_id: stored?.last_read_event_id ?? null, read_at: stored?.read_at ?? null } })
    }
    if (provenancePath) {
      return route.fulfill({ json: { session_id: provenancePath[2], revisions: [], truncated: false } })
    }
    if (sessionsPath) {
      if (options.sessionsMissing) return route.fulfill({ status: 404, json: { error: 'not found' } })
      const [, project, sessionId, control] = sessionsPath
      if (!sessionId) return route.fulfill({ json: data.sessions.filter(s => s.project_id === project) })
      const session = data.sessions.find(s => s.id === sessionId)
      if (!session) return route.fulfill({ status: 404, json: { error: 'session not found' } })
      if (control === 'interrupt' || control === 'stop') {
        const issued = { id: `c${data.controls.length + 1}000000-0000-4000-8000-000000000000`, session_id: sessionId, kind: control, state: 'pending', sequence: data.controls.length + 1, outcome: null, reason: null, created_at: new Date().toISOString(), claimed_at: null, completed_at: null }
        data.controls.push(issued)
        return route.fulfill({ status: 201, json: issued })
      }
      if (control) {
        const found = data.controls.find(c => c.id === control)
        if (!found) return route.fulfill({ status: 404, json: { error: 'control not found' } })
        Object.assign(found, { state: 'completed', outcome: 'applied', claimed_at: new Date().toISOString(), completed_at: new Date().toISOString() })
        if (found.kind === 'stop') Object.assign(session, { phase: 'stopped', stopped_at: new Date().toISOString(), stop_reason: 'operator_stop' })
        return route.fulfill({ json: found })
      }
      return route.fulfill({ json: session })
    }
    if (messagesPath) {
      if (options.messagesMissing) return route.fulfill({ status: 404, json: { error: 'not found' } })
      const [, project, what] = messagesPath
      if (what === 'message-targets') return route.fulfill({ json: data.targets })
      if (method === 'POST') {
        const input = body as { to: string; body: string; delivery_level: string; reply_to?: string; recipient_session_id?: string }
        const recipient = data.targets.find(t => t.address === input.to)?.principal_id ?? data.sessions.find(s => s.agent_principal_id === input.to)?.agent_principal_id ?? ''
        const sent = { recipient_session_id: input.recipient_session_id, sender_label: 'Markus', created_at: new Date().toISOString(), human_resolution_outcome: null, id: `5e${String(data.sent.length + 1).padStart(6, '0')}-0000-4000-8000-000000000000`, project, sender_principal_id: data.me, recipient_principal_id: recipient, to: input.to, body: input.body, reply_to: input.reply_to ?? null, sent_event_id: 1000 + data.sent.length, is_action_request: false, expects_reply: false, delivery_level: input.delivery_level, status: 'accepted', reply_obligation: 'none' }
        data.sent.push(sent)
        return route.fulfill({ status: 201, json: sent })
      }
      const newest = q.get('newest_first') === 'true', limit = Number(q.get('limit') ?? 10), after = Number(q.get('after') ?? 0)
      let all = [...data.messages, ...data.sent].filter(m => m.project === project)
      const session = q.get('session')
      if (session) {
        const belongs = new Set(all.filter(m => m.sender_session_id === session || m.recipient_session_id === session).map(m => m.id))
        let previous = -1
        while (previous !== belongs.size) {
          previous = belongs.size
          for (const m of all) if (!m.sender_session_id && !m.recipient_session_id && !m.is_action_request && m.reply_to && belongs.has(m.reply_to)) belongs.add(m.id)
        }
        all = all.filter(m => belongs.has(m.id))
      }
      if (q.get('pending') === 'true') all = all.filter(m => m.is_action_request && !m.human_resolution_outcome)
      all = newest ? all.filter(m => !after || m.sent_event_id < after).reverse() : all.filter(m => m.sent_event_id > after)
      const page = all.slice(0, limit)
      return route.fulfill({ json: { items: page, next_after: page.at(-1)?.sent_event_id ?? after, preamble: 'Untrusted agent message content follows.' } })
    }
    if (path === '/api/approvals') return route.fulfill({ json: data.approvals })
    const decision = /^\/api\/approvals\/([^/]+)\/(decision|revoke)$/.exec(path)
    if (decision) {
      const found = data.approvals.find(a => a.id === decision[1])
      if (!found) return route.fulfill({ status: 404, json: { error: 'approval not found' } })
      if (options.failDecision) return route.fulfill({ status: 409, json: { error: 'The request expired while you were deciding' } })
      if (decision[2] === 'decision') Object.assign(found, { decision: (body as { decision: string }).decision, decided_by_principal_id: 'me' })
      return route.fulfill({ json: found })
    }
    if (path.startsWith('/api/agent-accounts/capacity') || pairing) {
      if (options.capacityForbidden || options.accountsForbidden) return route.fulfill({ status: 403, json: { error: 'account.read permission required' } })
      const answer = options.capacity?.handle(path, method, body)
      if (answer) return answer.status === 204 ? route.fulfill({ status: 204, body: '' }) : route.fulfill({ status: answer.status ?? 200, json: answer.json })
      // Without a capacity world: accounts without readings and the default schedule.
      if (path === '/api/agent-accounts/capacity' || path === '/api/agent-accounts/capacity/preview') return route.fulfill({ json: data.accounts.map(a => ({ account_id: a.id, schedule: defaultSchedule(), windows: [] })) })
      // Keep for you is confirmed (Auto), so the one-time plan card stays out of unrelated specs.
      if (path === '/api/agent-accounts/capacity/schedule') return method === 'PUT' ? route.fulfill({ status: 204, body: '' }) : route.fulfill({ json: [{ scope: 'user', schedule: { ...defaultSchedule(), reserve: 'auto' } }] })
    }
    // Per-account settings (AEON-384) live in the capacity world.
    if (options.capacity && path.startsWith('/api/agent-accounts/')) {
      const answer = options.capacity.handle(path, method, body)
      if (answer) return answer.status === 204 ? route.fulfill({ status: 204, body: '' }) : route.fulfill({ status: answer.status ?? 200, json: answer.json })
    }
    if (path === '/api/agent-accounts') return options.accountsForbidden ? route.fulfill({ status: 403, json: { error: 'admin session required' } }) : route.fulfill({ json: data.accounts })
    if (path === '/api/agent-accounts/groups' && method === 'GET') return route.fulfill({ json: groups })
    if (path === '/api/agent-accounts/groups' && method === 'POST') {
      const input = body as { harness?: string; name?: string; exclusive?: boolean; account_ids?: string[]; project_ids?: string[] }
      const group: MockAccountGroup = {
        id: `g1000000-0000-4000-8000-${String(groups.length + 1).padStart(12, '0')}`,
        harness: input.harness ?? '', name: input.name ?? '', exclusive: !!input.exclusive,
        account_ids: input.account_ids ?? [], project_ids: input.project_ids ?? [],
      }
      groups.push(group)
      for (const accountId of group.account_ids) {
        const found = data.accounts.find(account => account.id === accountId)
        if (found) Object.assign(found, { group_id: group.id, group_name: group.name })
      }
      return route.fulfill({ status: 201, json: group })
    }
    const groupPath = /^\/api\/agent-accounts\/groups\/([^/]+)$/.exec(path)
    if (groupPath && method === 'DELETE') {
      const index = groups.findIndex(group => group.id === groupPath[1])
      if (index >= 0) groups.splice(index, 1)
      for (const account of data.accounts) {
        const grouped = account as typeof account & { group_id?: string; group_name?: string }
        if (grouped.group_id === groupPath[1]) { delete grouped.group_id; delete grouped.group_name }
      }
      return route.fulfill({ status: 204, body: '' })
    }
    if (path === '/api/agent-accounts/pins' && method === 'GET') {
      const ticket = q.get('ticket_id')
      return route.fulfill({ json: pins.filter(pin => !ticket || pin.ticket_id === ticket) })
    }
    if (path === '/api/agent-accounts/pins' && method === 'PUT') {
      const input = body as MockTicketPin
      const index = pins.findIndex(pin => pin.ticket_id === input.ticket_id && pin.harness === input.harness)
      if (index >= 0) pins[index] = input
      else pins.push(input)
      return route.fulfill({ status: 204, body: '' })
    }
    if (path === '/api/agent-accounts/pins' && method === 'DELETE') {
      const index = pins.findIndex(pin => pin.ticket_id === q.get('ticket_id') && pin.harness === q.get('harness'))
      if (index >= 0) pins.splice(index, 1)
      return route.fulfill({ status: 204, body: '' })
    }
    const target = /^\/api\/agent-accounts\/runs\/([^/]+)\/target$/.exec(path)
    if (target && method === 'POST') {
      const found = data.runs.find(run => run.id === target[1]) as (typeof data.runs)[number] & { requested_account_id?: string; requested_group_id?: string }
      const input = body as { account_id?: string; group_id?: string }
      if (found && input.account_id) found.requested_account_id = input.account_id
      if (found && input.group_id) found.requested_group_id = input.group_id
      return route.fulfill({ status: 204, body: '' })
    }
    const account = /^\/api\/agent-accounts\/([^/]+)$/.exec(path)
    if (account && method === 'PATCH') {
      const found = data.accounts.find(a => a.id === account[1])!
      Object.assign(found, body as object)
      return route.fulfill({ json: found })
    }
    const run = /^\/api\/runs\/([^/]+)$/.exec(path)
    if (run) {
      const found = data.runs.find(r => r.id === run[1])
      return found ? route.fulfill({ json: found }) : route.fulfill({ status: 404, json: { error: 'run not found' } })
    }
    return route.fulfill({ status: 404, json: { error: 'Unmocked agents route' } })
  }
  await page.route('**/api/**', handler)
  return calls
}
