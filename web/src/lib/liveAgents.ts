// SPDX-License-Identifier: AGPL-3.0-only
// Who is working where right now (AEON-184): the rules behind the live agents on
// the Projects page. GET /api/harness-sessions/live answers every visible
// project in one read; these helpers group it, mark what is no longer fresh on
// the server's clock, order a project's agents, and say it in words for labels,
// screen readers and the live region. Free of Vue so it can be unit tested.
import { duration, harnessLabel } from './agentState.ts'
import type { Harness, NodeSummary } from './agents.ts'

export type LiveBotState = 'working' | 'waiting' | 'stale'
export interface LiveAgent {
  project_id: string
  // Present only when the caller may open the session / know the agent (AEON-171).
  session_id?: string; principal_id?: string; name?: string
  harness: Harness; management_mode: 'managed' | 'unmanaged'; role: 'worker' | 'coordinator'
  phase: 'starting' | 'working' | 'stopping'; activity: 'busy' | 'unknown'
  // The bound ticket and the project it lives in now (it may have moved on).
  ticket: (NodeSummary & { project_id: string }) | null; since: string; heartbeat_at: string
  // Optional evidence for callers with richer telemetry (AM1). LA1's compact
  // endpoint currently supplies heartbeat_at only; no sequence is fabricated.
  activity_sequence?: number
  // Presentation state. Waiting must come from explicit approval evidence,
  // never from activity=unknown, starting, elapsed time or a missing heartbeat.
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

// Current activity leads waiting and stale readings so a stale ticket owner
// cannot hide a working agent. Within each state: ticket, worker, then start.
export function byLead(a: LiveAgent, b: LiveAgent) {
  const rank = (agent: LiveAgent) => agent.state === 'stale' ? 2 : agent.state === 'waiting' ? 1 : 0
  return rank(a) - rank(b) || Number(!a.ticket) - Number(!b.ticket)
    || Number(a.role === 'coordinator') - Number(b.role === 'coordinator')
    || Date.parse(a.since) - Date.parse(b.since)
    || (a.session_id ?? a.since).localeCompare(b.session_id ?? b.since)
}

export function liveState(agent: LiveAgent, serverNow: number, freshMs = LIVE_FRESH_MS): LiveBotState {
  const beat = Date.parse(agent.heartbeat_at)
  if (!Number.isFinite(beat) || serverNow - beat > freshMs) return 'stale'
  return agent.state ?? 'working'
}

// Keep the last reading visible but grey when it ages (e.g. a failed poll).
// A successful response removing a session removes it immediately.
export function groupLive(items: LiveAgent[], serverNow: number, freshMs = LIVE_FRESH_MS) {
  const out = new Map<string, LiveAgent[]>()
  for (const item of items) {
    const agent = { ...item, state: liveState(item, serverNow, freshMs) }
    const list = out.get(item.project_id)
    if (list) list.push(agent); else out.set(item.project_id, [agent])
  }
  for (const list of out.values()) list.sort(byLead)
  return out
}

// Two readings that show the same thing, including event evidence, so a poll that
// changes nothing re-renders nothing.
const shown = (a: LiveAgent) => [a.project_id, a.session_id, a.principal_id, a.name, a.harness, a.role, a.phase, a.activity, a.state, a.ticket?.id, a.ticket?.key, a.ticket?.title, a.ticket?.project_id, a.since, a.heartbeat_at, a.activity_sequence].join('\u0000')
export function sameLive(a: Map<string, LiveAgent[]>, b: Map<string, LiveAgent[]>) {
  if (a.size !== b.size) return false
  for (const [id, list] of a) {
    const other = b.get(id)
    if (!other || other.length !== list.length || list.some((agent, i) => shown(agent) !== shown(other[i]!))) return false
  }
  return true
}

// Who: the agent's name, else its harness ("Claude agent") when the caller may
// not know which agent it is.
export const who = (agent: LiveAgent) => agent.name || `${harnessLabel(agent.harness)} agent`
export const phaseLabel = (agent: Pick<LiveAgent, 'phase' | 'state'>) => agent.state === 'stale' ? 'No recent activity' : agent.state === 'waiting' ? 'Waiting for approval' : agent.phase === 'starting' ? 'Starting' : agent.phase === 'stopping' ? 'Stopping' : 'Working'
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

export interface ActivityEvidence { sequence: number; heartbeat: number; pulse: number }
// High-water marks prevent a delayed/duplicate poll from replaying a glint.
// First sight is a baseline, not an event. Two fields advancing together = one pulse.
export function advanceActivity(previous: ActivityEvidence | undefined, agent: Pick<LiveAgent, 'activity_sequence' | 'heartbeat_at'>): ActivityEvidence {
  const sequence = Number.isSafeInteger(agent.activity_sequence) && agent.activity_sequence! >= 0 ? agent.activity_sequence! : -1
  const heartbeat = Date.parse(agent.heartbeat_at)
  const beat = Number.isFinite(heartbeat) ? heartbeat : -1
  const advanced = previous && ((previous.sequence >= 0 && sequence > previous.sequence) || (previous.heartbeat >= 0 && beat > previous.heartbeat))
  return { sequence: Math.max(previous?.sequence ?? -1, sequence), heartbeat: Math.max(previous?.heartbeat ?? -1, beat), pulse: (previous?.pulse ?? 0) + (advanced ? 1 : 0) }
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
    const started = now.filter(agent => agent.state !== 'stale' && !wasKeys.has(agentKey(agent)))
    const stopped = was.filter(agent => !nowKeys.has(agentKey(agent)))
    const previous = new Map(was.map(agent => [agentKey(agent), agent]))
    const stale = now.filter(agent => agent.state === 'stale' && previous.has(agentKey(agent)) && previous.get(agentKey(agent))!.state !== 'stale')
    const resumed = now.filter(agent => agent.state !== 'stale' && previous.get(agentKey(agent))?.state === 'stale')
    if (started.length || stopped.length || stale.length || resumed.length) changed++
    if (started.length) lines.push(`${names(started)} started working on ${name}.`)
    if (stopped.length) lines.push(now.length ? `${names(stopped)} stopped working on ${name}.` : `No agent is working on ${name} any more.`)
    if (stale.length) lines.push(`No recent activity from ${names(stale)} on ${name}.`)
    if (resumed.length) lines.push(`Activity resumed for ${names(resumed)} on ${name}.`)
  }
  if (lines.length > 3) return `Agents changed in ${changed} projects.`
  return lines.join(' ')
}
