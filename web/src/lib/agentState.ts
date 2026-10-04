// SPDX-License-Identifier: AGPL-3.0-only
// Pure rules for the agents workspace: what state a session is in, which group it
// belongs to, and what an approval asks for and how risky it is. Free of Vue so it
// can be unit tested.
import { brand } from './brand.ts'
import type { AgentRun, Approval, HarnessSession, ProjectMessage } from './agents.ts'
import type { HarnessSessionRow } from './agentRows.ts'
import type { DeepReadonly } from './ledger.ts'

import { DEFAULT_AGENT_STATE, assessAgentState, type StateReason, type AgentState, type AgentStatePreference } from './agentSignals.ts'

export const HEARTBEAT_STALE_MS = 3 * 60_000
export const HARNESS_LABEL: Record<string, string> = { codex: 'Codex', claude: 'Claude', pi: 'Pi', cursor: 'Cursor', grok: 'Grok', gemini: 'Gemini CLI', opencode: 'OpenCode' }
export const harnessLabel = (harness: string) => HARNESS_LABEL[harness] ?? harness.charAt(0).toUpperCase() + harness.slice(1)

export type SessionGroup = 'pausing' | 'paused' | 'needs' | 'awaiting' | 'working' | 'throttled' | 'problem' | 'unresponsive' | 'idle' | 'stopped'
export type LiveTone = 'busy' | 'idle' | 'attention' | 'quiet' | 'done' | 'stopped' | 'throttled' | 'problem'
export interface SessionStatus { group: SessionGroup; tone: LiveTone; label: string; state: AgentState; reasons?: StateReason[] }
export const GROUPS: { id: SessionGroup; label: string }[] = [
  { id: 'problem', label: 'Problem' }, { id: 'unresponsive', label: 'No heartbeat' }, { id: 'needs', label: 'Needs something' }, { id: 'awaiting', label: 'Awaiting heartbeat' }, { id: 'throttled', label: 'Throttled' },
  { id: 'pausing', label: 'Pausing' },
  { id: 'working', label: 'Working' }, { id: 'idle', label: 'Idle' }, { id: 'paused', label: 'Paused' }, { id: 'stopped', label: 'Ended' },
]
const STATE_GROUP: Record<AgentState, SessionGroup> = { pausing: 'pausing', paused: 'paused', working: 'working', awaiting: 'awaiting', unresponsive: 'unresponsive', waiting: 'needs', throttled: 'throttled', problem: 'problem', idle: 'idle', stale: 'idle', done: 'stopped', stopped: 'stopped' }
const STATE_TONE: Record<AgentState, LiveTone> = { pausing: 'busy', paused: 'stopped', working: 'busy', awaiting: 'attention', unresponsive: 'attention', waiting: 'attention', throttled: 'throttled', problem: 'problem', idle: 'idle', stale: 'quiet', done: 'done', stopped: 'stopped' }
export function heartbeatStale(session: HarnessSession, now: number, preferences = DEFAULT_AGENT_STATE) {
  return !session.heartbeat_at || now - Date.parse(session.heartbeat_at) >= preferences.yellowMinutes * 60_000
}
export function sessionStatus(session: HarnessSession, now: number, needsYou = false, preferences: AgentStatePreference = DEFAULT_AGENT_STATE, run?: AgentRun): SessionStatus {
  // The session projection is fresh and authoritative, including explicit false
  // and null. Optional run/approval reads can finish later or retain old data.
  const run_status = session.run_status !== undefined ? session.run_status : run?.outcome ?? run?.status
  const assessment = assessAgentState({ ...session, run_status }, now, preferences, needsYou)
  const { state, label, reasons } = assessment
  return { state, label, ...(reasons.length ? { reasons } : {}), group: STATE_GROUP[state], tone: STATE_TONE[state] }
}

// Mutation/snapshot responses may omit what only a list or a detail read adds: the
// separately projected state evidence, the node and agent summaries, the viewer's move
// permission and the history a detail carries. Preserve known evidence for the same
// binding, while allowing a new run or stop reason to establish its own state. The
// ledger decides which answer is newer (ledger.ts), so this only ever receives the
// newer one.
export function mergeSessionEvidence(previous: DeepReadonly<HarnessSessionRow> | undefined, incoming: HarnessSessionRow): DeepReadonly<HarnessSessionRow> {
  if (!previous) return incoming
  const carried: Partial<{ -readonly [K in keyof HarnessSessionRow]: DeepReadonly<HarnessSessionRow[K]> }> = {}
  const carry = <K extends keyof HarnessSessionRow>(key: K, when = true) => { if (when && incoming[key] === undefined && previous[key] !== undefined) carried[key] = previous[key] }
  carry('project', incoming.project_id === previous.project_id)
  carry('ticket', incoming.ticket_node_id === previous.ticket_node_id)
  carry('agent', incoming.agent_principal_id === previous.agent_principal_id)
  carry('can_reparent')
  carry('watch')
  carry('activity_note_id')
  // History is read with a detail only. It stays until the next detail read replaces it,
  // so a panel does not blink between a list row and the detail that follows it.
  carry('metadata_history')
  carry('activity_history', incoming.agent_activity_mode === undefined || incoming.agent_activity_mode === 'agent_summary')
  carry('current_activity_history', incoming.agent_activity_mode !== 'off')
  if (incoming.run_id !== previous.run_id || incoming.stop_reason !== previous.stop_reason) return { ...incoming, ...carried }
  return {
    ...incoming,
    ...carried,
    has_problem: incoming.has_problem ?? previous.has_problem,
    vendor_limited: incoming.vendor_limited ?? previous.vendor_limited,
    limit_window: incoming.limit_window ?? previous.limit_window,
    limit_resets_at: incoming.limit_resets_at ?? previous.limit_resets_at,
    needs_attention: incoming.needs_attention ?? previous.needs_attention,
    attention_reasons: incoming.attention_reasons ?? previous.attention_reasons,
    run_status: incoming.run_status !== undefined ? incoming.run_status : previous.run_status,
  }
}

export function stopReasonLabel(reason: string | null | undefined) {
  if (!reason) return ''
  if (reason === 'heartbeat_lost') return 'Lost contact'
  return reason.replace(/[_-]+/g, ' ').replace(/^./, c => c.toUpperCase())
}

// Prefer the session's public label; otherwise the name part of its message address ("claude:camy" is camy),
// else the agent principal's own name (aeon-coordinator); the machine it runs on
// only as a last resort, since a host name is not who the agent is.
export function agentName(session: Pick<HarnessSession, 'agent_principal_id' | 'host' | 'agent' | 'display_label'>, addresses: Record<string, string>) {
  const address = addresses[session.agent_principal_id]
  const name = address?.split(':')[1]
  return session.display_label?.trim() || name || session.agent?.name || session.host
}

// Live sessions keep the order they started in, stopped ones lead with the latest
// stop; UUIDs break ties. Heartbeats only decide state, never position (AEON-468).
const byId = (a: HarnessSession, b: HarnessSession) => a.id < b.id ? -1 : a.id > b.id ? 1 : 0
export const byStart = (a: HarnessSession, b: HarnessSession) => Date.parse(a.created_at) - Date.parse(b.created_at) || byId(a, b)
export const byStopped = (a: HarnessSession, b: HarnessSession) => Date.parse(b.stopped_at ?? b.created_at) - Date.parse(a.stopped_at ?? a.created_at) || byId(a, b)

export interface SessionBranch<T> {
  view: T; children: SessionBranch<T>[]; group: SessionGroup; liveCount: number; workingCount: number; count: number
}

// Parent UUIDs, never shared principals or names, establish the tree. A missing
// or invalid parent leaves a visible root; even malformed cycles lose no rows.
// Group by the most urgent member so a stopped lead cannot hide working children.
export function sessionForest<T extends { session: HarnessSession; status: SessionStatus }>(views: T[], _now: number): SessionBranch<T>[] {
  const branches = new Map(views.map(view => [view.session.id, { view, children: [], group: view.status.group, liveCount: 0, workingCount: 0, count: 0 } as SessionBranch<T>]))
  const roots: SessionBranch<T>[] = []
  for (const branch of branches.values()) {
    const s = branch.view.session
    let parent = s.parent_harness_session_id ? branches.get(s.parent_harness_session_id) : undefined
    if (parent?.view.session.project_id !== s.project_id) parent = undefined
    const seen = new Set([s.id])
    for (let ancestor = parent; ancestor;) {
      const id = ancestor.view.session.id
      if (seen.has(id)) { parent = undefined; break }
      seen.add(id)
      ancestor = branches.get(ancestor.view.session.parent_harness_session_id ?? '')
    }
    if (parent) parent.children.push(branch)
    else roots.push(branch)
  }
  const rank = (group: SessionGroup) => GROUPS.findIndex(g => g.id === group)
  const activityRank = (branch: SessionBranch<T>) => branch.workingCount ? 0 : branch.liveCount ? 1 : 2
  const summarize = (branch: SessionBranch<T>) => {
    const s = branch.view.session
    branch.liveCount = s.phase === 'stopped' || s.stopped_at ? 0 : 1
    branch.workingCount = branch.liveCount && !['idle', 'throttled'].includes(s.activity) && ['working', 'starting', 'stopping'].includes(s.phase) && !['problem', 'unresponsive', 'awaiting', 'idle'].includes(branch.view.status.state) ? 1 : 0
    branch.count = 1
    for (const child of branch.children) {
      summarize(child)
      branch.count += child.count
      branch.liveCount += child.liveCount
      branch.workingCount += child.workingCount
      if (rank(child.group) < rank(branch.group)) branch.group = child.group
    }
    // Active branches lead, including stopped parents of live descendants, in
    // start order; ended ones follow, latest stop first. UUIDs break ties. A
    // heartbeat never moves a row (AEON-468).
    branch.children.sort((a, b) => activityRank(a) - activityRank(b) || (activityRank(a) === 2 ? byStopped : byStart)(a.view.session, b.view.session))
  }
  roots.forEach(summarize)
  return roots
}

// Older servers may omit projected evidence. Even then, a shared principal or
// held message cannot identify a sender generation. Only exact run evidence can.
export function approvalRun(approval: Approval) {
  return approval.run_id ?? (approval.resource_kind === 'run' ? approval.resource_id : null)
}
export function needsYou(session: HarnessSession, pending: Approval[], _held: (ProjectMessage & { projectId?: string })[]) {
  if (session.phase === 'stopped' || session.stopped_at || !session.run_id) return false
  return pending.some(a => a.agent_principal_id === session.agent_principal_id && approvalRun(a) === session.run_id)
}

export function groupSessions(sessions: HarnessSession[], now: number, needs: (session: HarnessSession) => boolean) {
  const buckets: Record<SessionGroup, { session: HarnessSession; status: SessionStatus }[]> = { pausing: [], paused: [], problem: [], unresponsive: [], needs: [], awaiting: [], throttled: [], working: [], idle: [], stopped: [] }
  for (const session of sessions) {
    const status = sessionStatus(session, now, needs(session))
    buckets[status.group].push({ session, status })
  }
  for (const group of ['problem', 'unresponsive', 'needs', 'awaiting', 'throttled', 'working', 'idle'] as const) buckets[group].sort((a, b) => byStart(a.session, b.session))
  buckets.stopped.sort((a, b) => byStopped(a.session, b.session))
  return buckets
}

export function duration(ms: number) {
  const s = Math.max(0, Math.round(ms / 1000))
  if (s < 60) return `${s}s`
  const m = Math.floor(s / 60)
  if (m < 60) return `${m}m`
  const h = Math.floor(m / 60)
  if (h < 24) return m % 60 ? `${h}h ${m % 60}m` : `${h}h`
  const d = Math.floor(h / 24)
  return h % 24 ? `${d}d ${h % 24}h` : `${d}d`
}
export function elapsed(session: HarnessSession, now: number) {
  return duration(Date.parse(session.stopped_at ?? new Date(now).toISOString()) - Date.parse(session.created_at))
}
export function runDuration(run: AgentRun, now: number) {
  if (typeof run.duration_ms === 'number') return duration(run.duration_ms)
  if (!run.started_at) return ''
  return duration(Date.parse(run.ended_at ?? new Date(now).toISOString()) - Date.parse(run.started_at))
}

// ---------- Runs ----------
export const RUN_OUTCOME: Record<AgentRun['status'], { label: string; tone: 'ok' | 'busy' | 'bad' | 'muted' }> = {
  queued: { label: 'Queued', tone: 'muted' }, starting: { label: 'Starting', tone: 'busy' }, running: { label: 'Running', tone: 'busy' },
  waiting: { label: 'Waiting', tone: 'busy' }, completed: { label: 'Completed', tone: 'ok' }, failed: { label: 'Failed', tone: 'bad' },
  cancelled: { label: 'Cancelled', tone: 'muted' }, ownership_lost: { label: 'Lost', tone: 'bad' },
}
export const runModel = (run: AgentRun | undefined) => run?.effective_model ?? run?.requested_model ?? ''
export function tokens(n: number) {
  if (n < 1000) return String(n)
  if (n < 1_000_000) return `${(n / 1000).toFixed(n < 10_000 ? 1 : 0).replace(/\.0$/, '')}k`
  return `${(n / 1_000_000).toFixed(1).replace(/\.0$/, '')}M`
}
export const cost = (micros: number) => `$${(micros / 1_000_000).toFixed(micros < 10_000_000 ? 2 : 0)}`

// ---------- Approvals ----------
export interface Asker { name: string; harness: string; sessionId: string }
export interface Resource { label: string; key?: string; title?: string; href?: string }
export function pendingApprovals(approvals: Approval[], now: number) {
  return approvals.filter(a => a.decision === null && Date.parse(a.expires_at) > now)
    .sort((a, b) => Date.parse(a.expires_at) - Date.parse(b.expires_at))
}
export function decidedApprovals(approvals: Approval[], now: number) {
  return approvals.filter(a => a.decision !== null || Date.parse(a.expires_at) <= now)
    .sort((a, b) => Date.parse(b.proposed_at) - Date.parse(a.proposed_at))
}

const SCOPES: Record<string, string> = {
  'run.claim': 'Claim a run and start work', 'run.create': 'Start new runs', 'run.read': 'Read runs',
  'harness.control': 'Interrupt or stop agent sessions', 'harness.write': 'Register and bind sessions', 'harness.read': 'See agent sessions',
  'harness.worker': 'Act as a session worker', 'inbox.send': 'Send messages to people and agents', 'nodes.read': 'Read tickets',
  'nodes.write': 'Change tickets', 'work_orders.write': 'Change work orders', 'work_orders.read': 'Read work orders', 'stage.deploy': 'Deploy a stage',
}
export function scopeLabel(scope: string) {
  if (SCOPES[scope]) return SCOPES[scope]
  const [resource, ...rest] = scope.split('.')
  const verb = rest.join(' ').replace(/_/g, ' ')
  return `${verb.charAt(0).toUpperCase()}${verb.slice(1)} ${resource.replace(/_/g, ' ')}`
}

// Risk is read from what the permission allows: reading is low, changing work is
// medium, and control, deployment, deletion, spending or anything tenant-wide is high.
export type Risk = 'low' | 'medium' | 'high'
export function riskOf(approval: Pick<Approval, 'scope' | 'resource_kind'>): Risk {
  const verbs = approval.scope.split('.').slice(1).join('.')
  if (approval.resource_kind === 'tenant' || /control|deploy|delete|admin|release|spend|merge|push|secret|credential|budget|stop/.test(verbs)) return 'high'
  if (/^read$|\.read$|^read/.test(verbs)) return 'low'
  return 'medium'
}
// The server computes risk (B7); the local rule only covers older servers.
export const riskFor = (approval: Pick<Approval, 'scope' | 'resource_kind' | 'risk'>): Risk => approval.risk ?? riskOf(approval)
export const RISK_LABEL: Record<Risk, string> = { low: 'Low risk', medium: 'Medium risk', high: 'High risk' }

// Match the server's approvalPermission lookup: a scoped agent request is
// granted only by someone who can perform the underlying action themselves.
export function canDecideApproval(approval: Approval, allowed: (permission: string) => boolean): boolean {
  if (!allowed('approvals.decide') || (riskFor(approval) === 'high' && !allowed('approvals.decide_high'))) return false
  let scope = approval.scope === 'release.deploy' || approval.scope.startsWith('release.deploy.') ? 'releases.deploy' : approval.scope.startsWith('journey.') ? 'journey.act' : approval.scope
  while (scope) {
    if (allowed(scope)) return true
    const dot = scope.lastIndexOf('.')
    if (dot < 0) break
    scope = scope.slice(0, dot)
  }
  return false
}

export function expiresIn(approval: Approval, now: number) {
  const left = Date.parse(approval.expires_at) - now
  return left <= 0 ? 'Expired' : `Expires in ${duration(left)}`
}
export const expiresSoon = (approval: Approval, now: number) => Date.parse(approval.expires_at) - now < 10 * 60_000

// ---------- Held action requests ----------
export const heldRequests = (messages: ProjectMessage[]) => messages.filter(m => m.is_action_request && !m.human_resolution_outcome)

// Why a typed control cannot be sent right now, or '' when it can. Controls exist
// only for sessions Aeon owns that advertise them, one at a time.
export function controlBlocked(session: HarnessSession, kind: 'interrupt' | 'stop', name: string, canControl: boolean, current?: { kind: string; state: string } | null) {
  if (!canControl) return 'Only people who may write can control sessions'
  if (session.phase === 'stopped') return 'This session has stopped'
  if (session.management_mode !== 'managed') return `This session runs outside ${brand.value.short_name}, so it cannot be controlled from here`
  if (!session.advertised_capabilities.includes(kind)) return `${name} does not accept ${kind === 'stop' ? 'a stop' : 'interrupts'}`
  if (current && current.state !== 'completed') return `${current.kind === 'stop' ? 'A stop' : 'An interrupt'} is on its way`
  return ''
}
