// SPDX-License-Identifier: AGPL-3.0-only
import { api, APIError } from './api'
import { policyJSON } from './policyEditor'
import type { AccountResetState, ResetCredits, ResetPlan, ResetPolicy } from './accountResets'
async function json<T>(path: string, method: string, body?: unknown, headers: Record<string, string> = {}, signal?: AbortSignal): Promise<T> {
  const response = await api(path, { method, headers: { 'Content-Type': 'application/json', ...headers }, ...(body === undefined ? {} : { body: JSON.stringify(body) }), signal })
  if (!response.ok) throw new APIError(response.status, 'Usage settings request failed')
  return policyJSON<T>(response)
}
export type UsagePosture = 'careful' | 'balanced' | 'maxout'
export interface AccountUsagePolicy {
  account_id: string; posture: UsagePosture; source: 'account' | 'person' | 'workspace' | 'schedule'
  floor_percent: number; own_floor_percent: number; revision: number; binding_revision: number
  can_set_posture: boolean; can_set_floor: boolean
  boost_percent?: number; boost_until?: string | null
}
export interface UsageOverviewRow {
  account_id: string; harness?: string; provider?: string; usage_policy?: AccountUsagePolicy; boost_withheld?: boolean
  // AEON-1035: vendor-reported credits (null when unreported) and the owner's policy, revisioned with the floor.
  resets?: ResetCredits | null; reset_policy?: ResetPolicy; reset_plan?: ResetPlan | null; reset_revision?: number; binding_revision?: number
}
export interface UsageOverview { accounts: UsageOverviewRow[]; has_more: boolean; next_cursor?: string }
export const getUsageOverview = (after?: string, signal?: AbortSignal) => json<UsageOverview>(`/agent-accounts/overview${after ? `?after=${encodeURIComponent(after)}` : ''}`, 'GET', undefined, {}, signal)
// The account posture write is superseded by Pace on the dial (AEON-1030); only the floor stays per account.
export const putAccountUsage = (policy: AccountUsagePolicy, value: { floor_percent: number }, signal?: AbortSignal) => json<AccountUsagePolicy>(`/agent-accounts/${encodeURIComponent(policy.account_id)}/floor`, 'PUT', { ...value, revision: policy.revision, binding_revision: policy.binding_revision }, {}, signal)
export const putAccountBoost = (policies: AccountUsagePolicy[], boost_percent: number, timezone: string, signal?: AbortSignal) => json<{ accounts: AccountUsagePolicy[]; withheld_count?: number; withheld_boost_until?: string | null }>('/agent-accounts/boost', 'PUT', { boost_percent, timezone, accounts: policies.map(({ account_id, revision, binding_revision }) => ({ account_id, revision, binding_revision })) }, {}, signal)
export const putResetPolicy = (policy: AccountUsagePolicy, reset_policy: ResetPolicy, signal?: AbortSignal) => json<AccountResetState>(`/agent-accounts/${encodeURIComponent(policy.account_id)}/reset-policy`, 'PUT', { reset_policy, revision: policy.revision, binding_revision: policy.binding_revision }, {}, signal)
