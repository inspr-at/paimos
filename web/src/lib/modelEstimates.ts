// SPDX-License-Identifier: AGPL-3.0-only
import { api } from './api'

export interface ModelEstimateHistory {
  state: 'calibrated' | 'uncalibrated'
  basis: string
  tickets: number
  hours: number | null
  tokens: number | null
  speed_factor: number | null
  speed_tickets: number
}

export async function modelEstimateHistory(profileId: string, kind: string, bucket: 'normal' | 'complex', projectId?: string, signal?: AbortSignal): Promise<ModelEstimateHistory> {
  const params = new URLSearchParams({ profile_id: profileId, kind, bucket })
  if (projectId) params.set('project_id', projectId)
  const response = await api(`/usage/model-estimates?${params}`, { signal })
  if (!response.ok) throw new Error('Model history could not be loaded')
  return response.json() as Promise<ModelEstimateHistory>
}

// A picker may render only measured history, never the planning fallback.
export function modelEstimateHint(history: ModelEstimateHistory | null | undefined, kindLabel: string, bucket: 'normal' | 'complex'): string {
  if (!history || history.state !== 'calibrated' || history.tickets < 5 || !Number.isInteger(history.tickets) ||
      history.hours === null || history.tokens === null || !Number.isFinite(history.hours) || history.hours <= 0 ||
      !Number.isSafeInteger(history.tokens) || history.tokens <= 0) return ''
  const hours = Number(history.hours.toFixed(1))
  const tokens = history.tokens >= 1_000_000 ? `${Number((history.tokens / 1_000_000).toFixed(1))}M` : history.tokens >= 1000 ? `${Number((history.tokens / 1000).toFixed(1))}k` : `${history.tokens}`
  return `typically ~${hours} h · ~${tokens} tokens for ${bucket} ${kindLabel} (n=${history.tickets})`
}
