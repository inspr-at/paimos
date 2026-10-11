// SPDX-License-Identifier: AGPL-3.0-only
import type { PairingView } from './agentPairing.ts'
import { api } from './api.ts'

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

// ---------- New starts (AEON-1174, design AEON-1149): the chip on each computer row ----------
export type HostCapacityMode = HostCapacityPolicy['mode']
export const NEW_STARTS_MODES: readonly HostCapacityMode[] = ['smart', 'fixed', 'off']
export const NEW_STARTS_NAME: Record<HostCapacityMode, string> = { smart: 'Smart', fixed: 'Fixed limit', off: 'Off' }
/** The chip's words: "Smart", "Fixed · load 12", "Off". */
export function newStartsLabel(policy: HostCapacityPolicy, mode: HostCapacityMode = policy.mode): string {
  return mode === 'smart' ? 'Smart' : mode === 'fixed' ? `Fixed · load ${policy.maximum_load}` : 'Off'
}
/** What each choice does with this computer's own options: only the conditions it has switched on are named. */
export function newStartsChoices(policy: HostCapacityPolicy, cores?: number | null): { mode: HostCapacityMode; title: string; detail: string }[] {
  const waits = [policy.wait_when_busy && 'busy', policy.ease_when_hot && 'hot', policy.ease_on_battery && 'on battery'].filter((w): w is string => !!w)
  const list = waits.length > 1 ? `${waits.slice(0, -1).join(', ')} or ${waits.at(-1)}` : waits[0]
  return [
    { mode: 'smart', title: 'Smart', detail: `Starts new agents whenever the computer has room${cores ? `; the limit comes from its ${cores} cores` : ''}.${list ? ` Waits while it is ${list}.` : ''}` },
    { mode: 'fixed', title: 'Fixed limit', detail: `New starts wait while the one-minute load is above ${policy.maximum_load}.` },
    { mode: 'off', title: 'Off', detail: `Starts without looking at load.${policy.maximum_agents ? ' Maximum agents still applies.' : ''}` },
  ]
}
export const newStartsFoot = (policy: HostCapacityPolicy) => `Maximum agents: ${policy.maximum_agents || 'no limit'}. Only new starts wait; running agents are never stopped.`
/** Who may change New starts: the computer's owner with account management (the server's rule; can_verify marks the owner's sign-ins), on a connected computer. */
export const mayChangeNewStarts = (computer: Pick<PairingView, 'computer_state' | 'enrollments'>, manage: boolean) =>
  manage && computer.computer_state === 'connected' && computer.enrollments.some(e => e.can_verify === true)

const POLICY_FIELDS = ['mode', 'maximum_agents', 'maximum_load', 'wait_when_busy', 'ease_on_battery', 'ease_when_hot', 'consider_activity'] as const
/** The same stored policy, field by field (two reads need not agree on key order). */
export const samePolicy = (a?: HostCapacityPolicy | null, b?: HostCapacityPolicy | null) => !!a && !!b && POLICY_FIELDS.every(field => a[field] === b[field])

export interface HostCapacityWrite { expected_revision: number; policy: HostCapacityPolicy }
/**
 * The chip's write: the panel's PUT with only the mode changed; every other policy field is
 * kept as the server last reported it. Nothing to write without a reported policy and revision
 * (never defaults in place of fields this page has not seen), or when the mode is already set.
 */
export function newStartsWrite(computer: Pick<PairingView, 'revision' | 'host_capacity'>, mode: HostCapacityMode): HostCapacityWrite | null {
  const policy = computer.host_capacity?.policy
  if (!policy || !computer.revision || policy.mode === mode || !NEW_STARTS_MODES.includes(mode)) return null
  return { expected_revision: computer.revision, policy: { ...policy, mode } }
}
export class HostCapacityWriteError extends Error {
  readonly status: number
  constructor(status: number, message: string) { super(message); this.status = status }
}
/**
 * PUT /agent-pairing/computers/{id}/capacity. Resolves with the policy the server saved; the
 * computer's revision is then expected_revision + 1 (the server bumps it in the same write).
 * A refusal or an answer without a valid policy throws: status 409 means the computer changed.
 */
export async function saveHostPolicy(computerId: string, write: HostCapacityWrite): Promise<HostCapacityPolicy> {
  const response = await api(`/agent-pairing/computers/${computerId}/capacity`, { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(write) })
  if (!response.ok) throw new HostCapacityWriteError(response.status, response.status === 409 ? 'changed' : 'refused')
  const body = await response.json().catch(() => null) as { policy?: HostCapacityPolicy } | null
  if (!body?.policy || !validHostPolicy(body.policy)) throw new HostCapacityWriteError(0, 'unconfirmed')
  return body.policy
}
