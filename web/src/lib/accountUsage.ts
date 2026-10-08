// SPDX-License-Identifier: AGPL-3.0-only
import { api, APIError } from './api'
import { policyJSON } from './policyEditor'
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
export interface UsageOverview { accounts: { account_id: string; usage_policy?: AccountUsagePolicy; boost_withheld?: boolean }[]; has_more: boolean; next_cursor?: string }
export const getUsageOverview = (after?: string, signal?: AbortSignal) => json<UsageOverview>(`/agent-accounts/overview${after ? `?after=${encodeURIComponent(after)}` : ''}`, 'GET', undefined, {}, signal)
export const putAccountUsage = (policy: AccountUsagePolicy, value: { posture: UsagePosture | null } | { floor_percent: number }, signal?: AbortSignal) => json<AccountUsagePolicy>(`/agent-accounts/${encodeURIComponent(policy.account_id)}/${'posture' in value ? 'posture' : 'floor'}`, 'PUT', { ...value, revision: policy.revision, binding_revision: policy.binding_revision }, {}, signal)
export const putAccountBoost = (policies: AccountUsagePolicy[], boost_percent: number, timezone: string, signal?: AbortSignal) => json<{ accounts: AccountUsagePolicy[]; withheld_count?: number; withheld_boost_until?: string | null }>('/agent-accounts/boost', 'PUT', { boost_percent, timezone, accounts: policies.map(({ account_id, revision, binding_revision }) => ({ account_id, revision, binding_revision })) }, {}, signal)
