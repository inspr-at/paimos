// SPDX-License-Identifier: AGPL-3.0-only
// Public pause contract and pure planning; estimates are predictions, never exit evidence.
import type { HarnessSessionRow } from './agentRows.ts'
import type { DeepReadonly } from './ledger.ts'
export type PauseLevel = 'stop_now' | 'pause_quickly' | 'pause' | 'wrap_up'
export interface PauseProgress {
  reported_at: string; step?: string | null; next_point?: string | null; next_point_in_min?: number | null
  finish_in_min?: number | null; finish_outcome?: string | null; interrupt?: boolean | null
  command?: string | null; command_left_min?: number | null
}
export interface Handover {
  state: string; next_steps: readonly string[]; open_questions: readonly string[]
  worktree_state: 'committed' | 'clean' | 'rolled_back'; commit_sha?: string
}
export interface AgentPause {
  control_id: string; state: 'requested' | 'planned' | 'paused' | 'resume_requested' | 'resumed' | 'cancelled'
  requested_by_principal_id: string; requested_at: string; reason: string; deadline_at: string
  level?: PauseLevel; note?: string; starts_at?: string; leaving_request_id?: string
  stop_requested?: boolean; deliver?: boolean; handover_point?: string; paused_at?: string
  handover?: Handover; successor_session_id?: string
}
export interface WindDownScope { hosts: 'all' | readonly string[]; agents?: readonly string[] }
export interface LeavingReport extends WindDownScope { owner_principal_id?: string; deadline_at: string | null; request_id: string | null; stop_in_flight: boolean }
export const LEVELS: readonly { value: PauseLevel; name: string; rule: string }[] = [
  { value: 'stop_now', name: 'Stop now', rule: 'Requests an immediate stop. No handover; work in progress may be lost.' },
  { value: 'pause_quickly', name: 'Pause quickly', rule: 'Hands over quickly, with work in progress. Limit 3 min.' },
  { value: 'pause', name: 'Pause', rule: 'Finishes or rolls back the current step, commits work in progress and hands over. Limit 10 min.' },
  { value: 'wrap_up', name: 'Wrap up', rule: 'Finishes the task if it takes less than 10 min; otherwise hands over like Pause.' },
]
export const levelName = (level?: PauseLevel) => LEVELS.find(l => l.value === level)?.name ?? 'Pause'
export const MINUTE = 60_000
export const clockTime = (at: number | string) => new Date(at).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
export const liveSession = (s: Pick<HarnessSessionRow, 'phase' | 'stopped_at' | 'archived_at'>) => s.phase !== 'stopped' && !s.stopped_at && !s.archived_at
export const pausedSession = (s: DeepReadonly<HarnessSessionRow>) => !s.archived_at && s.pause?.state === 'paused' && !liveSession(s)
export const pausingSession = (s: DeepReadonly<HarnessSessionRow>) => liveSession(s) && !!s.pause && ['requested', 'planned'].includes(s.pause.state) && !s.pause.stop_requested && s.pause.deliver !== false
export const cooperative = (s: DeepReadonly<HarnessSessionRow>) => s.supported_pause_levels
  ? s.supported_pause_levels.includes('pause') : s.advertised_capabilities.includes('inbox') || (s.management_mode === 'unmanaged' && s.advertised_capabilities.includes('pause'))
export function estimates(s: DeepReadonly<HarnessSessionRow>, now: number, intervalMinutes: number | null) {
  const p = s.pause_progress, at = Date.parse(p?.reported_at ?? s.eta_reported_at ?? '')
  const fresh = intervalMinutes !== null && Number.isFinite(at) && at <= now && now - at <= 2 * intervalMinutes * MINUTE
  const remaining = (m?: number | null) => fresh && typeof m === 'number' && Number.isFinite(m) && m >= 0 ? Math.max(0, at + m * MINUTE - now) : null
  return { fresh, finish: p ? remaining(p.finish_in_min) : fresh && s.eta_ready_at && !s.eta_stale ? Math.max(0, Date.parse(s.eta_ready_at) - now) : null,
    next: p ? remaining(p.next_point_in_min) : null, command: p ? remaining(p.command_left_min) : null,
    interrupt: fresh ? p?.interrupt : null, progress: fresh ? p : undefined }
}
export function predictWindDown(s: DeepReadonly<HarnessSessionRow>, now: number, deadline: number, interval: number | null) {
  const budget = deadline - now, e = estimates(s, now, interval)
  if (budget <= 0 || !cooperative(s)) return { level: 'stop_now' as const, starts: Math.max(now, deadline) }
  if (e.finish !== null && e.finish < 10 * MINUTE && e.finish <= budget - MINUTE) return { level: 'wrap_up' as const, starts: now }
  const wait = e.command !== null && e.interrupt !== true ? e.command : 0
  if (budget <= 10 * MINUTE && !(e.next !== null && wait + e.next + 30_000 <= budget) && e.interrupt !== false && (e.next !== null || budget <= 2 * MINUTE)) return { level: 'pause_quickly' as const, starts: now }
  return { level: 'pause' as const, starts: Math.max(now, deadline - 10 * MINUTE) }
}
export function prediction(s: DeepReadonly<HarnessSessionRow>, level: PauseLevel, now: number, interval: number | null) {
  if (level === 'stop_now') return { time: 'now', outcome: 'no handover', detail: LEVELS[0]!.rule }
  const e = estimates(s, now, interval), limit = (level === 'pause_quickly' ? 3 : 10) * MINUTE
  const finishes = level === 'wrap_up' && e.finish !== null && e.finish < 10 * MINUTE
  const wait = e.command !== null && e.interrupt !== true ? e.command : 0
  const estimate = finishes ? e.finish : level === 'pause_quickly' ? (!e.progress || e.interrupt == null || (e.interrupt === false && e.command === null) ? null : e.interrupt === false ? wait + MINUTE : MINUTE) : e.next === null ? null : wait + e.next
  const time = estimate === null ? `by ${clockTime(now + limit)} at latest` : estimate > limit ? `limit ${clockTime(now + limit)}` : `~${clockTime(now + estimate)} · in ~${Math.max(1, Math.ceil(estimate / MINUTE))} min`
  const outcome = estimate !== null && estimate > limit ? 'stop requested at limit' : finishes ? 'finished task' : level === 'pause_quickly' ? 'WIP handover' : 'full handover'
  const detail = estimate === null ? 'No estimate from the agent. The limit still applies.' : finishes ? e.progress?.finish_outcome || 'The latest estimate fits; the agent can finish its task.' : e.interrupt === false && wait ? `Reacts after its current command${e.progress?.command ? `: ${e.progress.command}` : ''} (~${Math.ceil(wait / MINUTE)} min).` : level === 'wrap_up' ? 'Same as Pause: the latest finish estimate does not fit, or is missing.' : e.progress?.next_point || LEVELS.find(l => l.value === level)!.rule
  return { time, outcome, detail }
}
export function selectedAgent(s: DeepReadonly<HarnessSessionRow>, scope: WindDownScope) {
  return scope.agents ? scope.agents.includes(s.id) : scope.hosts === 'all' || scope.hosts.includes(s.host)
}
export function hostTick(host: string, scope: WindDownScope, sessions: readonly DeepReadonly<HarnessSessionRow>[]): 'true' | 'mixed' | 'false' {
  const running = sessions.filter(s => s.host === host && liveSession(s)), count = running.filter(s => selectedAgent(s, scope)).length
  return running.length ? count === running.length ? 'true' : count ? 'mixed' : 'false' : scope.hosts === 'all' || scope.hosts.includes(host) ? 'true' : 'false'
}
export function normalizeScope(scope: WindDownScope, sessions: readonly DeepReadonly<HarnessSessionRow>[], hosts: readonly string[]): WindDownScope {
  if (scope.hosts === 'all') return { hosts: 'all', ...(scope.agents ? { agents: [...scope.agents] } : {}) }
  const running = sessions.filter(liveSession), agents = running.filter(s => selectedAgent(s, scope)).map(s => s.id)
  const chosen = hosts.filter(host => { const rows = running.filter(s => s.host === host); return rows.length ? rows.every(s => agents.includes(s.id)) : scope.hosts.includes(host) })
  return chosen.length === hosts.length && agents.length === running.length && hosts.length > 0 ? { hosts: 'all' } : { hosts: chosen, agents }
}
export function toggleScope(scope: WindDownScope, kind: 'host' | 'agent', id: string, sessions: readonly DeepReadonly<HarnessSessionRow>[], hosts: readonly string[]): WindDownScope {
  const expanded = { hosts: scope.hosts === 'all' ? [...hosts] : [...scope.hosts], agents: sessions.filter(s => liveSession(s) && selectedAgent(s, scope)).map(s => s.id) }
  if (kind === 'host') {
    const checked = hostTick(id, scope, sessions) === 'true', ids = sessions.filter(s => s.host === id && liveSession(s)).map(s => s.id)
    expanded.hosts = checked ? expanded.hosts.filter(h => h !== id) : [...new Set([...expanded.hosts, id])]
    expanded.agents = checked ? expanded.agents.filter(a => !ids.includes(a)) : [...new Set([...expanded.agents, ...ids])]
  } else expanded.agents = expanded.agents.includes(id) ? expanded.agents.filter(a => a !== id) : [...expanded.agents, id]
  return normalizeScope(expanded, sessions, hosts)
}
