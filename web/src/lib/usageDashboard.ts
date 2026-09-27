// SPDX-License-Identifier: AGPL-3.0-only
import { api, APIError } from './api.ts'
import type { UsageDashboard } from './usageFormat.ts'

export async function loadUsageDashboard(params: { from?: string; to?: string; project?: string }, signal?: AbortSignal): Promise<UsageDashboard> {
  const query = new URLSearchParams()
  if (params.from) query.set('from', params.from)
  if (params.to) query.set('to', params.to)
  if (params.project) query.set('project', params.project)
  const suffix = query.size ? `?${query}` : ''
  const response = await api(`/usage/dashboard${suffix}`, { signal })
  if (!response.ok) {
    const data = await response.json().catch(() => ({}))
    throw new APIError(response.status, typeof data?.error === 'string' ? data.error : `Request failed (${response.status})`)
  }
  return response.json()
}
