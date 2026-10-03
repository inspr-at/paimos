// SPDX-License-Identifier: AGPL-3.0-only
export interface QuotaWarningSettings { early_percent: number; urgent_percent: number }
export function validQuotaThresholds(value: QuotaWarningSettings) {
  return Number.isInteger(value.early_percent) && Number.isInteger(value.urgent_percent)
    && value.urgent_percent >= 1 && value.early_percent <= 50 && value.urgent_percent < value.early_percent
}
export interface QuotaWarningSession {
  session_id: string; project_id: string; account_id: string; availability: 'limited'; details_redacted: boolean
  severity?: 'early' | 'urgent'; remaining_percent?: number; threshold_percent?: number; window_key?: string; resets_at?: string | null
}
export interface QuotaWarningPage { items: QuotaWarningSession[]; next_after: string | null }
export async function readQuotaWarnings(): Promise<QuotaWarningPage> {
  const { api } = await import('./api.ts')
  const response = await api('/agent-accounts/quota-warnings?limit=100')
  if (!response.ok) throw new Error('Account availability could not be refreshed.')
  return response.json()
}
