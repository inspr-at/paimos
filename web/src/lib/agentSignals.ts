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
export interface StateEvidence {
  phase: string; activity: string; heartbeat_at?: string | null; created_at?: string; since?: string
  stopped_at?: string | null; stop_reason?: string | null; run_status?: string | null
  needs_attention?: boolean; has_problem?: boolean
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
  if ((evidence.needs_attention ?? needs) || evidence.phase === 'yielded' || evidence.run_status === 'waiting') {
    const detail = evidence.phase === 'yielded' ? 'The session yielded and is waiting to continue.' : evidence.run_status === 'waiting' ? 'The bound run is waiting.' : 'An approval, held action or requested reply is outstanding.'
    return result('waiting', [{ code: 'attention', detail, next: 'Review the pending requests and messages for this session.' }])
  }
  if (evidence.activity === 'throttled') return result('throttled', [{ code: 'throttled', detail: 'The session reports throttled activity.', next: 'Check its account allowance and pacing before resuming work.' }])
  if (working && (!heartbeat.hasHeartbeat || heartbeat.age >= preferences.yellowMinutes * 60_000)) return result('awaiting', [heartbeatReason], heartbeat.hasHeartbeat ? 'Heartbeat overdue' : 'Awaiting heartbeat')
  if (working) return result('working')
  return result(!heartbeat.hasHeartbeat || heartbeat.age >= preferences.yellowMinutes * 60_000 ? 'stale' : 'idle')
}
export function deriveAgentState(evidence: StateEvidence, now: number, preferences = DEFAULT_AGENT_STATE, needs = false): AgentState {
  return assessAgentState(evidence, now, preferences, needs).state
}
