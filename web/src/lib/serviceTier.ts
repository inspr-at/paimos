// SPDX-License-Identifier: AGPL-3.0-only
import type { HarnessSessionRow, ProcessOwnership } from './agentRows.ts'
import type { HarnessSession } from './agents.ts'
import { controlPermitted, ownershipFresh, type ControlGrant } from './managedControl.ts'

export const SERVICE_TIERS = ['default', 'fast', 'fastest'] as const
export type ServiceTier = typeof SERVICE_TIERS[number]
export const TIER_NAME: Record<ServiceTier, string> = { default: 'Default', fast: 'Fast', fastest: 'Fastest' }
export interface TierCapability {
  tier: ServiceTier; name: string; offered: boolean; speed_factor: number | null
  price_multiplier: number | null; usage_multiplier: number | null; mechanism: string; reason?: string
}
export interface TierReport {
  harness: string; model: string; harness_version: string; adapter_version: string; checked_at: string
  source: string; applies: 'next_run' | 'next_turn'; change_instructions: string; tiers: readonly TierCapability[]
}
export interface TierControl {
  id: string; session_id: string; kind: string; value: ServiceTier; state: 'pending' | 'claimed' | 'completed'
  outcome: 'applied' | 'rejected' | null; reason: string | null
}
export interface TierRequest {
  id: string; session_id: string; tier: ServiceTier; reason: string; requested_by_principal_id: string
  state: 'pending' | 'approved' | 'declined'; created_at: string; decided_at?: string | null; control_id?: string | null
}
export interface TierState {
  session_id: string; revision: number; active_tier: ServiceTier | null; pending: TierControl | null
  last_change?: TierControl | null
  read_only: boolean; read_only_reason?: string; reports: readonly TierReport[]; requests: TierRequest[]
}
export interface TierChange { request_id: string; tier: ServiceTier; expected_revision: number; expected_ownership: ProcessOwnership }
export const tierReport = (s: Pick<HarnessSessionRow, 'model' | 'harness'>, reports: readonly TierReport[] = []) =>
  reports.find(report => report.model === s.model && report.harness === s.harness)
export const offeredTier = (t: TierCapability | undefined): t is TierCapability & { offered: true; price_multiplier: number } =>
  !!t?.offered && t.price_multiplier !== null && Number.isFinite(t.price_multiplier) && t.price_multiplier > 0
export const tierOptions = (report?: TierReport): TierCapability[] => SERVICE_TIERS.map(tier => {
  const t = report?.tiers.find(item => item.tier === tier)
  return offeredTier(t) ? t : { tier, name: TIER_NAME[tier], offered: false, speed_factor: null, price_multiplier: null, usage_multiplier: null, mechanism: t?.mechanism ?? '', reason: t?.reason || 'not offered: no published price' }
})
export const offeredTiers = (report?: TierReport) => tierOptions(report).filter(offeredTier)
export const adjacentTier = (active: ServiceTier | null, report: TierReport | undefined, direction: -1 | 1) => {
  const offered = offeredTiers(report), index = offered.findIndex(t => t.tier === active)
  return index < 0 ? undefined : offered[index + direction]
}
export const tierPrice = (t?: TierCapability) => !offeredTier(t) ? 'price unknown' : t.price_multiplier === 1 ? 'list price' : `${t.price_multiplier} times list price`
export const tierSpeed = (t?: TierCapability) => !t?.speed_factor ? 'speed not published' : t.speed_factor === 1 ? 'Standard speed' : `~${t.speed_factor} times faster output`
export function tierUnavailable(s: HarnessSession, grant: ControlGrant, now: number, state?: TierState): string {
  if (!controlPermitted(s, grant)) return 'Only a person with permission to control this session can change its tier.'
  if (s.phase === 'stopped' || s.phase === 'stopping' || s.stopped_at || s.archived_at) return `This session ran at ${s.service_tier ? TIER_NAME[s.service_tier] : 'an unreported tier'}.`
  if (s.management_mode !== 'managed') return 'Reported by the harness; change it in its terminal.'
  if (!s.run_id || !s.advertised_capabilities.includes('stop') || !s.advertised_capabilities.includes('service_tier_v1') || s.watch) return 'This daemon does not support confirmed tier changes.'
  if (!ownershipFresh(s, now)) return 'Waiting for the owning daemon to confirm this process.'
  if (state?.read_only) return state.read_only_reason || 'This session is read-only.'
  return ''
}
export const sameOwnership = (a?: ProcessOwnership, b?: ProcessOwnership) => !!a && !!b &&
  a.daemon_id === b.daemon_id && a.generation === b.generation && a.process_id === b.process_id &&
  a.root_pid === b.root_pid && a.group_id === b.group_id && a.started_at === b.started_at

// Refuse an Undo after another person's switch, a catalog update or a process
// replacement. Confirmation adds exactly one revision to our accepted change.
export function undoTierAllowed(current: TierState, receipt: { session: string; revision: number; control: string; from: ServiceTier; to: ServiceTier }) {
  if (current.session_id !== receipt.session || current.read_only) return false
  if (current.pending) return current.revision === receipt.revision && current.pending.id === receipt.control && current.pending.state === 'pending' && current.pending.value === receipt.to && current.active_tier === receipt.from
  return current.revision === receipt.revision + 1 && current.active_tier === receipt.to
}
