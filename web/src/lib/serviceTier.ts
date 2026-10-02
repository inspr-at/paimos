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
  history?: TierHistory[]; history_truncated?: boolean; run_cost?: TierRunCost | null; estimates?: TierEstimate[]
  last_change?: TierControl | null
  read_only: boolean; read_only_reason?: string; reports: readonly TierReport[]; requests: TierRequest[]
}
export interface TierChange { undo_of_control_id?: string; request_id: string; tier: ServiceTier; expected_revision: number; expected_ownership: ProcessOwnership }
// The cancelled daemon control and the successful Undo receipt have different
// outcomes. Neither is a vendor rejection.
export const tierCancelled = (control?: TierControl | null) => control?.state === 'completed' && (
  (control.outcome === 'rejected' && control.reason === 'tier_cancelled') ||
  (control.outcome === 'applied' && control.reason === 'tier_cancelled_pending')
)
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

export interface TierHistory {
  reason?: string; id: number; action: 'requested' | 'switch_requested' | 'approved' | 'declined' | 'changed' | 'cancelled' | 'undo_requested' | 'undone' | 'rejected'
  from_tier: ServiceTier | null; to_tier: ServiceTier; actor_id: string; actor_name: string; asked_by_name: string | null; at: string
}
export interface TierRunCost {
  run_id: string; cost_usd: string; default_cost_usd: string; provisional: boolean
  segments: { tier: ServiceTier; price_multiplier: number }[]
}
export interface TierEstimate { tier: ServiceTier; n: number; basis: string; run_id: string | null; cost_usd: string | null; duration_ms: number | null }
const dollars = (value: string) => new Intl.NumberFormat('en-US', { style: 'currency', currency: 'USD' }).format(Number(value))
// The multiplier is arithmetic text in the approved fragment, not an icon.
export const tierRunCostLabel = (cost?: TierRunCost | null) => !cost ? 'Tier cost unavailable' : `${cost.segments.map(s => `${TIER_NAME[s.tier]} ×${s.price_multiplier}`).join(' + ')} · ${dollars(cost.default_cost_usd)} at Default`
export const tierCostAmount = (cost: TierRunCost) => dollars(cost.cost_usd)
export const tierEstimate = (state: TierState | undefined, tier: ServiceTier): TierEstimate => state?.estimates?.find(e => e.tier === tier) ?? { tier, n: 0, basis: 'No estimate yet · 0 runs. A completed run with frozen prices is required.', run_id: null, cost_usd: null, duration_ms: null }
export const estimateCostText = (e: TierEstimate) => e.n > 0 && e.cost_usd !== null ? `≈ ${dollars(e.cost_usd)}` : 'no estimate yet'
export const estimateTimeText = (e: TierEstimate) => e.n > 0 && e.duration_ms !== null ? `≈ ${Math.round(e.duration_ms / 6000) / 10} min` : 'time: no estimate yet'
export function tierHistoryText(h: TierHistory): string {
  const from = h.from_tier ? TIER_NAME[h.from_tier] : 'unreported tier', to = TIER_NAME[h.to_tier]
  const actor = ` · by ${h.actor_name}`, asked = h.asked_by_name ? `, asked by ${h.asked_by_name}` : ''
  switch (h.action) {
    case 'requested': return `Asked for ${to} from ${from}${actor}`
    case 'switch_requested': return `Switch requested: ${from} to ${to}${actor}`
    case 'approved': return `Approved ${from} to ${to}${actor}${asked} · waiting for confirmation`
    case 'declined': return `Declined ${to} for ${h.asked_by_name || 'the agent'} · kept ${from}${actor}`
    case 'cancelled': return `Undo: cancelled switch from ${from} to ${to}${actor} · kept ${from}`
    case 'undo_requested': return `Undo requested: ${from} to ${to}${actor}`
    case 'undone': return `Tier ${from} to ${to}${actor} (undo)`
    case 'rejected': return h.reason === 'outcome_unconfirmed' ? `Tier ${from} to ${to}${actor} · outcome unconfirmed` : `Rejected switch from ${from} to ${to}${actor}`
    case 'changed': return `Tier ${from} to ${to}${actor}${asked}`
  }
}
