// SPDX-License-Identifier: AGPL-3.0-only
// Who is working where right now (AEON-184): the rules behind the live agents on
// the Projects page. GET /api/harness-sessions/live answers every visible
// project in one read; these helpers group it, mark what is no longer fresh on
// the server's clock, order a project's agents, and say it in words for labels,
// screen readers and the live region. Free of Vue so it can be unit tested.
import { duration, harnessLabel } from './agentState.ts'
import type { Harness, NodeSummary } from './agents.ts'

import { DEFAULT_AGENT_STATE, STATE_LABEL, STATE_PRIORITY, deriveAgentState, waitingLabel, type AttentionReason, type AgentState, type AgentStatePreference } from './agentSignals.ts'
export type LiveBotState = AgentState
export interface LiveAgent {
  project_id: string
  // Present only when the caller may open the session / know the agent (AEON-171).
  session_id?: string; principal_id?: string; name?: string
  // Session label when harness.read at the project included it. Omitted means
  // withheld or unlabeled; never copy name into it.
  display_label?: string
  harness: Harness; management_mode: 'managed' | 'unmanaged'; role: 'worker' | 'coordinator'
  phase: 'starting' | 'working' | 'stopping' | 'yielded' | 'stopped'; activity: 'busy' | 'unknown' | 'idle' | 'throttled'
  stopped_at?: string | null; stop_reason?: string | null; run_status?: string | null; needs_attention?: boolean; has_problem?: boolean; attention_reasons?: AttentionReason[]
  // The bound ticket and the project it lives in now (it may have moved on).
  ticket: (NodeSummary & { project_id: string }) | null; since: string; heartbeat_at: string | null
  // The last persisted activity entry, withheld with the note when harness.read
  // is absent. Heartbeats and sequence changes do not advance it.
  activity_note?: string | null; activity_note_id?: number
  activity_sequence?: number
  // Derived presentation state; shared evidence and viewer thresholds decide it.
  state?: LiveBotState
}
// truncated: more sessions were live than one answer holds (the freshest are listed).
export interface LivePage { items: LiveAgent[]; at: string; fresh_seconds: number; truncated?: boolean }

// Polling cadence: heartbeats arrive every minute, so 20 seconds keeps a card
// honest without asking often.
export const LIVE_POLL_MS = 20_000
export const LIVE_FRESH_MS = 120_000

// The server's clock is the one that counts: skew is how far this browser is ahead.
export const skewOf = (page: Pick<LivePage, 'at'>, receivedAt: number) => {
  const at = Date.parse(page.at)
  return Number.isNaN(at) ? 0 : receivedAt - at
}

// Problems and requests lead so work cannot hide a session needing attention.
// Within each state: ticket, worker, then start.
export function byLead(a: LiveAgent, b: LiveAgent) {
  const rank = (agent: LiveAgent) => STATE_PRIORITY[agent.state ?? 'working']
  return rank(a) - rank(b) || Number(!a.ticket) - Number(!b.ticket)
    || Number(a.role === 'coordinator') - Number(b.role === 'coordinator')
    || Date.parse(a.since) - Date.parse(b.since)
    || (a.session_id ?? a.since).localeCompare(b.session_id ?? b.since)
}

export function liveState(agent: LiveAgent, serverNow: number, preferences: AgentStatePreference = DEFAULT_AGENT_STATE): LiveBotState {
  return deriveAgentState({ ...agent, needs_attention: agent.needs_attention ?? (agent.state === 'waiting' ? true : undefined) }, serverNow, preferences)
}

// State-aware reads retain quiet and failed sessions. A failed poll ages the
// last heartbeat through the same thresholds as /agents, on the server clock.
export function groupLive(items: LiveAgent[], serverNow: number, preferences: AgentStatePreference = DEFAULT_AGENT_STATE) {
  const out = new Map<string, LiveAgent[]>()
  for (const item of items) {
    const agent = { ...item, state: liveState(item, serverNow, preferences) }
    const list = out.get(item.project_id)
    if (list) list.push(agent); else out.set(item.project_id, [agent])
  }
  for (const list of out.values()) list.sort(byLead)
  return out
}

// Two readings that show the same thing, including event evidence, so a poll that
// changes nothing re-renders nothing.
const shown = (a: LiveAgent) => [a.project_id, a.session_id, a.principal_id, a.name, a.display_label, a.harness, a.role, a.phase, a.activity, a.state, a.ticket?.id, a.ticket?.key, a.ticket?.title, a.ticket?.project_id, a.since, a.heartbeat_at, a.activity_note, a.activity_note_id, a.run_status, a.stop_reason, a.stopped_at, a.needs_attention, a.has_problem, JSON.stringify(a.attention_reasons)].join('\u0000')
export function sameLive(a: Map<string, LiveAgent[]>, b: Map<string, LiveAgent[]>) {
  if (a.size !== b.size) return false
  for (const [id, list] of a) {
    const other = b.get(id)
    if (!other || other.length !== list.length || list.some((agent, i) => shown(agent) !== shown(other[i]!))) return false
  }
  return true
}

// Who is on screen: the session label when this feed included one, then the
// principal name the server already permitted. An omitted label is not filled
// in from the principal, and a withheld identity stays the harness.
export const who = (agent: LiveAgent) => agent.display_label?.trim() || agent.name?.trim() || `${harnessLabel(agent.harness)} agent`
export const phaseLabel = (agent: Pick<LiveAgent, 'phase' | 'state' | 'attention_reasons'>) => agent.state === 'waiting' ? waitingLabel(agent) : STATE_LABEL[agent.state ?? (agent.phase === 'stopped' ? 'stopped' : 'working')]
export const elapsedFor = (agent: Pick<LiveAgent, 'since'>, serverNow: number) => duration(serverNow - Date.parse(agent.since))

// One agent as a phrase: "hausv on HAUSV-887", "hausv, starting".
export function phrase(agent: LiveAgent) {
  const name = who(agent)
  if (agent.phase !== 'working' || (agent.state && agent.state !== 'working')) return `${name}, ${phaseLabel(agent).toLowerCase()}${agent.ticket ? ` on ${agent.ticket.key}` : ''}`
  return agent.ticket ? `${name} on ${agent.ticket.key}` : name
}
// "1 agent working: hausv on HAUSV-887", "2 agents working: hausv on HAUSV-887, camy".
export function liveSummary(agents: LiveAgent[]) {
  if (!agents.length) return ''
  const allWorking = agents.every(a => !a.state || a.state === 'working')
  return `${agents.length} ${agents.length === 1 ? 'agent' : 'agents'}${allWorking ? ' working' : ''}: ${agents.map(phrase).join(', ')}`
}

// The visible chip: the lead's name and ticket key, and how many more there are.
export function chipText(agents: LiveAgent[]) {
  const lead = agents[0]
  if (!lead) return { name: '', key: '', more: 0 }
  return { name: who(lead), key: lead.ticket?.key ?? '', more: agents.length - 1 }
}

// One agent across readings: its session, else (a caller who may not know
// which agent it is) its harness and start, which a session never changes.
export const agentKey = (agent: LiveAgent) => agent.session_id ?? `${agent.harness}@${agent.since}`

// A session bound to this ticket that the list may name. Stopped and archived
// sessions are not current work. State is the shared derivation: a heartbeat
// with no productive phase stays idle or stale and is never treated as working.
export function isListedTicketWorker(agent: LiveAgent): boolean {
  if (!agent.ticket) return false
  if (/archived/i.test(agent.stop_reason ?? '')) return false
  if (agent.phase === 'stopped' || agent.stopped_at || agent.state === 'stopped') return false
  return true
}

// Workers for the project on screen, keyed by the bound ticket. A session whose
// ticket now lives in another project is left out, and the same session is kept
// once. Order matches the project chips: attention first, then who started.
export function ticketWorkers(agents: LiveAgent[], projectId: string): Map<string, LiveAgent[]> {
  const out = new Map<string, LiveAgent[]>()
  const seen = new Set<string>()
  for (const agent of agents) {
    const ticket = agent.ticket
    if (!ticket || agent.project_id !== projectId || ticket.project_id !== projectId || !isListedTicketWorker(agent)) continue
    const id = `${ticket.id}\u0000${agentKey(agent)}`
    if (seen.has(id)) continue
    seen.add(id)
    const list = out.get(ticket.id)
    if (list) list.push(agent)
    else out.set(ticket.id, [agent])
  }
  for (const list of out.values()) list.sort(byLead)
  return out
}

export interface ActivityEvidence { noteID: number; pulse: number }
// First sight establishes the baseline. A delayed or repeated response can
// never replay a glint, and ordinary heartbeat telemetry cannot create one.
export function advanceActivity(previous: ActivityEvidence | undefined, agent: { activity_note_id?: number }): ActivityEvidence {
  const id = Number.isSafeInteger(agent.activity_note_id) && agent.activity_note_id! > 0 ? agent.activity_note_id! : 0
  const noteID = Math.max(previous?.noteID ?? 0, id)
  return { noteID, pulse: (previous?.pulse ?? 0) + (previous && id > previous.noteID ? 1 : 0) }
}

// What the live region says when agents start or stop working: nothing on the
// first reading; per project, who started and who stopped (the last one to
// stop says the project is quiet again); a count when much changes at once.
export function liveChanges(before: Map<string, LiveAgent[]> | null, after: Map<string, LiveAgent[]>, title: (projectId: string) => string | undefined) {
  if (!before) return ''
  const lines: string[] = []
  let changed = 0
  const names = (agents: LiveAgent[]) => agents.length === 1 ? who(agents[0]!) : `${agents.length} agents`
  for (const id of new Set([...after.keys(), ...before.keys()])) {
    const name = title(id)
    if (!name) continue
    const was = before.get(id) ?? [], now = after.get(id) ?? []
    const wasKeys = new Set(was.map(agentKey)), nowKeys = new Set(now.map(agentKey))
    const started = now.filter(agent => (!agent.state || agent.state === 'working') && !wasKeys.has(agentKey(agent)))
    const stopped = was.filter(agent => !nowKeys.has(agentKey(agent)))
    const previous = new Map(was.map(agent => [agentKey(agent), agent]))
    const stale = now.filter(agent => agent.state === 'stale' && previous.has(agentKey(agent)) && previous.get(agentKey(agent))!.state !== 'stale')
    const resumed = now.filter(agent => agent.state !== 'stale' && previous.get(agentKey(agent))?.state === 'stale')
    const transitions = now.filter(agent => agent.state && !['stale'].includes(agent.state) && !started.includes(agent) && !resumed.includes(agent) && (previous.get(agentKey(agent))?.state ?? 'working') !== agent.state)
    if (started.length || stopped.length || stale.length || resumed.length || transitions.length) changed++
    if (started.length) lines.push(`${names(started)} started working on ${name}.`)
    if (stopped.length) lines.push(now.length ? `${names(stopped)} stopped working on ${name}.` : `No agent is working on ${name} any more.`)
    if (stale.length) lines.push(`No recent activity from ${names(stale)} on ${name}.`)
    if (resumed.length) lines.push(`Activity resumed for ${names(resumed)} on ${name}.`)
    for (const agent of transitions) lines.push(`${who(agent)}: ${phaseLabel(agent)} on ${name}.`)
  }
  if (lines.length > 3) return `Agents changed in ${changed} projects.`
  return lines.join(' ')
}
