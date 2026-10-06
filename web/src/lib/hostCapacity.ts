// SPDX-License-Identifier: AGPL-3.0-only
export interface HostCapacityPolicy {
  mode: 'off' | 'smart' | 'fixed'
  maximum_agents: number
  maximum_load: number
  wait_when_busy: boolean
  ease_on_battery: boolean
  ease_when_hot: boolean
  consider_activity: boolean
}
export interface HostCapacityView {
  policy: HostCapacityPolicy
  signals: { load: number | null; cores: number; memory_pressure: string; memory_used_gb?: number | null; memory_total_gb?: number | null; power: string; thermal: string; input_active?: boolean | null } | null
  reported_at: string | null
  running: number
  queued: number
  reason: string
  load_limit: number
  history: { at: string; load: number }[]
}
export const defaultHostPolicy = (): HostCapacityPolicy => ({ mode: 'off', maximum_agents: 0, maximum_load: 30, wait_when_busy: true, ease_on_battery: true, ease_when_hot: true, consider_activity: false })
export function validHostPolicy(p: HostCapacityPolicy): boolean {
  return ['off', 'smart', 'fixed'].includes(p.mode) && Number.isInteger(p.maximum_agents) && p.maximum_agents >= 0 && p.maximum_agents <= 64
    && Number.isFinite(p.maximum_load) && p.maximum_load >= 1 && p.maximum_load <= 200
    && [p.wait_when_busy, p.ease_on_battery, p.ease_when_hot, p.consider_activity].every(v => typeof v === 'boolean')
}
const reasons: Record<string, string> = { maximum_agents: 'maximum agents reached', host_signals_stale: 'waiting for a fresh computer report', host_load_unknown: 'load is not reported', host_load: 'computer busy', memory_pressure: 'memory pressure is high', host_activity_unknown: 'activity is not reported' }
export const hostCapacityReason = (reason: string) => reasons[reason] ?? (reason ? 'waiting for host capacity' : '')
export function parseHostCapacity(raw: unknown): HostCapacityView | undefined {
  if (!raw || typeof raw !== 'object') return undefined
  const v = raw as HostCapacityView
  if (!v.policy || !validHostPolicy(v.policy) || !Number.isSafeInteger(v.running) || v.running < 0 || !Number.isSafeInteger(v.queued) || v.queued < 0
    || typeof v.reason !== 'string' || !Number.isFinite(v.load_limit) || !Array.isArray(v.history) || v.history.length > 60
    || v.history.some(p => !p || !Number.isFinite(p.load) || p.load < 0 || !Number.isFinite(Date.parse(p.at)))
    || v.reported_at !== null && !Number.isFinite(Date.parse(v.reported_at))) return undefined
  return v
}
