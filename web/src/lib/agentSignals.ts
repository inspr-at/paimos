// SPDX-License-Identifier: AGPL-3.0-only
// SC1: one presentation contract for sessions, project indicators and previews.
// Evidence stays separate from presentation; viewer thresholds never mutate it.
export type AgentState = 'working' | 'awaiting' | 'waiting' | 'throttled' | 'problem' | 'unresponsive' | 'idle' | 'stale' | 'stopped'
import { normalizeAgentPalette, type AgentPalette } from './agentPalettes.ts'
export type { AgentPalette }
export interface AgentStatePreference {
  palette: AgentPalette; dimInactive: boolean; inactiveOpacity: number
  yellowMinutes: number; redMinutes: number
}
export const DEFAULT_AGENT_STATE: Readonly<AgentStatePreference> = {
  palette: 'standard', dimInactive: true, inactiveOpacity: 55, yellowMinutes: 3, redMinutes: 10,
}
export const STATE_LABEL: Record<AgentState, string> = {
  working: 'Working', awaiting: 'Awaiting heartbeat', waiting: 'Needs something', throttled: 'Throttled', problem: 'Problem',
  unresponsive: 'No heartbeat', idle: 'Idle', stale: 'Idle · no heartbeat', stopped: 'Stopped',
}
export const inactiveState = (state: AgentState) => state === 'idle' || state === 'stale' || state === 'stopped'
export const movingState = (state: AgentState) => state === 'working'
export const STATE_PRIORITY: Record<AgentState, number> = { problem: 0, unresponsive: 1, waiting: 2, awaiting: 3, throttled: 4, working: 5, idle: 6, stale: 7, stopped: 8 }
export function leadingState(states: (AgentState | undefined)[]): AgentState {
  return states.reduce<AgentState>((lead, state) => STATE_PRIORITY[state ?? 'working'] < STATE_PRIORITY[lead] ? state ?? 'working' : lead, 'stopped')
}
export function normalizeAgentState(value: unknown): AgentStatePreference {
  const v = value && typeof value === 'object' ? value as Record<string, unknown> : {}
  const bounded = (n: unknown, fallback: number, min: number, max: number) => typeof n === 'number' && Number.isFinite(n) ? Math.max(min, Math.min(max, Math.round(n))) : fallback
  const yellowMinutes = bounded(v.yellowMinutes, 3, 1, 1439)
  return {
    palette: normalizeAgentPalette(v.palette),
    dimInactive: v.dimInactive !== false, inactiveOpacity: bounded(v.inactiveOpacity, 55, 40, 80),
    yellowMinutes, redMinutes: bounded(v.redMinutes, Math.max(10, yellowMinutes + 1), yellowMinutes + 1, 1440),
  }
}
export interface AttentionReason {
  kind: 'approval' | 'held_action' | 'reply' | 'reply_due' | 'run_waiting' | 'session_yielded'
  scope: 'session' | 'run' | 'shared'; actor: 'person' | 'agent' | 'unknown'
  count: number; blocking: boolean; location: 'approvals' | 'messages' | 'session'
}
// Text comes from controlled categories, never request bodies, IDs or rationale.
export function attentionReasonText(reason: AttentionReason): StateReason {
  const count = reason.count
  const actor = reason.actor === 'agent' ? 'another agent' : reason.actor === 'person' ? 'a person' : 'an unspecified participant'
  const details: Record<AttentionReason['kind'], string> = {
    approval: `${count} permission request${count === 1 ? '' : 's'} awaiting a person’s decision.`,
    held_action: `${count} held action request${count === 1 ? '' : 's'} awaiting a person’s resolution.`,
    reply: `${count} outstanding repl${count === 1 ? 'y' : 'ies'}; waiting for ${actor}.`,
    reply_due: `${count} requested repl${count === 1 ? 'y' : 'ies'} assigned to this session; this agent is expected to reply.`,
    run_waiting: 'The bound run reports waiting; who must act is not specified.',
    session_yielded: 'This session yielded; who must act is not specified.',
  }
  const locations = { approvals: 'Review permission requests in Needs you.', messages: 'Review the shared principal inbox in Messages below.', session: 'Check the bound run and session activity below.' }
  return { code: `${reason.scope}-${reason.kind}-${reason.actor}`, detail: `${reason.scope === 'shared' ? 'Shared inbox · ' : ''}${details[reason.kind]}`, next: `${reason.scope === 'shared' ? 'No session ownership is recorded. ' : ''}${!reason.blocking && reason.scope !== 'shared' ? 'This does not block ongoing work. ' : ''}${locations[reason.location]}` }
}
export function waitingLabel(evidence: Pick<StateEvidence, 'attention_reasons' | 'eta_stale'>) {
  const reasons = evidence.attention_reasons
  if (reasons?.some(r => r.scope !== 'shared' && r.blocking && r.kind === 'approval')) return 'Awaiting approval'
  const blocking = reasons?.some(r => r.blocking && r.scope !== 'shared')
  if (evidence.eta_stale && !blocking) return 'Estimate stale'
  return reasons?.length ? 'Waiting' : STATE_LABEL.waiting
}
export interface StateEvidence {
  phase: string; activity: string; heartbeat_at?: string | null; created_at?: string; since?: string
  stopped_at?: string | null; stop_reason?: string | null; run_status?: string | null
  needs_attention?: boolean; has_problem?: boolean; attention_reasons?: AttentionReason[]
  eta_stale?: boolean
}
// Stop reasons are free text. Match error words, not arbitrary nonempty reasons:
// "user requested", "completed", "stopped", "cancelled" are normal stops.
export function problemReason(reason?: string | null) {
  return !!reason && /\b(error|errored|failed|failure|blocked|crash(?:ed)?|ownership lost|heartbeat lost|timeout|timed out)\b/i.test(reason.replace(/[_-]+/g, ' '))
}
export interface StateReason { code: string; detail: string; next: string }
export interface StateAssessment { state: AgentState; label: string; reasons: StateReason[] }
export function heartbeatEvidence(evidence: StateEvidence, now: number) {
  const beat = Date.parse(evidence.heartbeat_at ?? '')
  const registered = Date.parse(evidence.created_at ?? evidence.since ?? '')
  const hasHeartbeat = Number.isFinite(beat)
  const reference = hasHeartbeat ? beat : registered
  return { hasHeartbeat, invalid: !!evidence.heartbeat_at && !hasHeartbeat, age: Number.isFinite(reference) ? Math.max(0, now - reference) : Infinity }
}

// Registration is not a heartbeat, and loss of reporting is not proof that the
// worker failed. Keep its severity visible with a distinct word and explanation.
export function assessAgentState(evidence: StateEvidence, now: number, preferences = DEFAULT_AGENT_STATE, needs = false): StateAssessment {
  const result = (state: AgentState, reasons: StateReason[] = [], label = STATE_LABEL[state]) => ({ state, label, reasons })
  const problems: StateReason[] = []
  if (problemReason(evidence.stop_reason)) problems.push({ code: 'stop', detail: `Reported stop reason: ${evidence.stop_reason}`, next: 'Check the session activity and its bound ticket before starting a replacement.' })
  if (['failed', 'ownership_lost', 'blocked'].includes(evidence.run_status ?? '')) problems.push({ code: 'run', detail: `The bound run reported ${evidence.run_status!.replace(/_/g, ' ')}.`, next: 'Check the current run and its history for the failure context.' })
  if (evidence.has_problem ?? (problems.length > 0)) {
    if (!problems.length) problems.push({ code: 'reported', detail: 'The service reported a problem without a visible reason.', next: 'Refresh this session and check its run history or ask the session owner for the missing reason.' })
    return result('problem', problems)
  }
  if (evidence.phase === 'stopped' || evidence.stopped_at) return result('stopped')
  const heartbeat = heartbeatEvidence(evidence, now)
  const working = ['starting', 'working', 'stopping'].includes(evidence.phase) && !['idle', 'throttled'].includes(evidence.activity)
  const heartbeatReason: StateReason = {
    code: 'heartbeat',
    detail: heartbeat.invalid ? 'The reported heartbeat timestamp is invalid.' : heartbeat.hasHeartbeat ? 'The last heartbeat is overdue; current worker activity is unconfirmed.' : 'No heartbeat has been received since this session registered.',
    next: 'Check the worker and its heartbeat reporter on the recorded host. A missing heartbeat does not establish that the worker failed.',
  }
  if (working && heartbeat.age >= preferences.redMinutes * 60_000) return result('unresponsive', [heartbeatReason])
  if ((evidence.needs_attention ?? (needs || evidence.run_status === 'waiting')) || evidence.phase === 'yielded' || evidence.eta_stale) {
    const detail = evidence.phase === 'yielded' ? 'The session yielded and is waiting to continue.' : evidence.run_status === 'waiting' ? 'The bound run is waiting.' : evidence.eta_stale ? 'The estimate was not refreshed within two reporting intervals.' : 'An approval, held action or requested reply is outstanding.'
    const reasons = evidence.attention_reasons?.filter(r => r.scope !== 'shared' && r.blocking).sort((a, b) => Number(b.kind === 'approval') - Number(a.kind === 'approval')).map(attentionReasonText) ?? []
    if (evidence.eta_stale) reasons.push({ code: 'eta-stale', detail: 'The estimate was not refreshed within two reporting intervals.', next: 'Refresh the estimate from the session working this ticket.' })
    return result('waiting', reasons.length ? reasons : [{ code: 'attention', detail, next: 'Review the session activity and bound run; who must act is not specified.' }], waitingLabel(evidence))
  }
  if (evidence.activity === 'throttled') return result('throttled', [{ code: 'throttled', detail: 'The session reports throttled activity.', next: 'Check its account allowance and pacing before resuming work.' }])
  if (working && (!heartbeat.hasHeartbeat || heartbeat.age >= preferences.yellowMinutes * 60_000)) return result('awaiting', [heartbeatReason], heartbeat.hasHeartbeat ? 'Heartbeat overdue' : 'Awaiting heartbeat')
  if (working) return result('working')
  return result(!heartbeat.hasHeartbeat || heartbeat.age >= preferences.yellowMinutes * 60_000 ? 'stale' : 'idle')
}
export function deriveAgentState(evidence: StateEvidence, now: number, preferences = DEFAULT_AGENT_STATE, needs = false): AgentState {
  return assessAgentState(evidence, now, preferences, needs).state
}
