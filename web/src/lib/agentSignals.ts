// SPDX-License-Identifier: AGPL-3.0-only
// SC1: one presentation contract for sessions, project indicators and previews.
// Evidence stays separate from presentation; viewer thresholds never mutate it.
export type AgentState = 'working' | 'waiting' | 'throttled' | 'problem' | 'idle' | 'stale' | 'stopped'
export type AgentPalette = 'standard' | 'colour-blind' | 'monochrome'
export interface AgentStatePreference {
  palette: AgentPalette; dimInactive: boolean; inactiveOpacity: number
  yellowMinutes: number; redMinutes: number
}
export const DEFAULT_AGENT_STATE: Readonly<AgentStatePreference> = {
  palette: 'standard', dimInactive: true, inactiveOpacity: 55, yellowMinutes: 3, redMinutes: 10,
}
export const STATE_LABEL: Record<AgentState, string> = {
  working: 'Working', waiting: 'Needs something', throttled: 'Throttled', problem: 'Problem',
  idle: 'Idle', stale: 'Idle · no heartbeat', stopped: 'Stopped',
}
export const inactiveState = (state: AgentState) => state === 'idle' || state === 'stale' || state === 'stopped'
export const movingState = (state: AgentState) => state === 'working'
export const STATE_PRIORITY: Record<AgentState, number> = { problem: 0, waiting: 1, throttled: 2, working: 3, idle: 4, stale: 5, stopped: 6 }
export function leadingState(states: (AgentState | undefined)[]): AgentState {
  return states.reduce<AgentState>((lead, state) => STATE_PRIORITY[state ?? 'working'] < STATE_PRIORITY[lead] ? state ?? 'working' : lead, 'stopped')
}
export function normalizeAgentState(value: unknown): AgentStatePreference {
  const v = value && typeof value === 'object' ? value as Record<string, unknown> : {}
  const bounded = (n: unknown, fallback: number, min: number, max: number) => typeof n === 'number' && Number.isFinite(n) ? Math.max(min, Math.min(max, Math.round(n))) : fallback
  const yellowMinutes = bounded(v.yellowMinutes, 3, 1, 1439)
  return {
    palette: v.palette === 'colour-blind' || v.palette === 'monochrome' ? v.palette : 'standard',
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
export function deriveAgentState(evidence: StateEvidence, now: number, preferences = DEFAULT_AGENT_STATE, needs = false): AgentState {
  if (evidence.has_problem || problemReason(evidence.stop_reason) || ['failed', 'ownership_lost', 'blocked'].includes(evidence.run_status ?? '')) return 'problem'
  if (evidence.phase === 'stopped' || evidence.stopped_at) return 'stopped'
  const beat = Date.parse(evidence.heartbeat_at ?? evidence.created_at ?? evidence.since ?? '')
  const age = Number.isFinite(beat) ? Math.max(0, now - beat) : Infinity
  const working = ['starting', 'working', 'stopping'].includes(evidence.phase) && !['idle', 'throttled'].includes(evidence.activity)
  if (working && age >= preferences.redMinutes * 60_000) return 'problem'
  if (needs || evidence.needs_attention || evidence.phase === 'yielded' || evidence.run_status === 'waiting') return 'waiting'
  if (evidence.activity === 'throttled') return 'throttled'
  if (working && age >= preferences.yellowMinutes * 60_000) return 'waiting'
  if (working) return 'working'
  return !evidence.heartbeat_at || age >= preferences.yellowMinutes * 60_000 ? 'stale' : 'idle'
}
