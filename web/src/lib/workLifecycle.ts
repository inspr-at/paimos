// SPDX-License-Identifier: AGPL-3.0-only
import { api, APIError } from './api.ts'

export interface WorkAction {
  id: string; node_id: string; kind: 'split' | 'cancel'; state: 'waiting' | 'completed' | 'abandoned'
  target_count: number; waiting_count: number; result: string[]
}
export interface WorkPreview {
  is_leaf: boolean; busy: boolean; open_leaves: number; updated_at: string; scope_revision: string; pending: WorkAction | null
}
export async function workLifecycleRequest<T>(nodeId: string, suffix = '', method = 'GET', body?: unknown): Promise<T> {
  const response = await api(`/nodes/${encodeURIComponent(nodeId)}/work-lifecycle${suffix}`, {
    method, ...(body === undefined ? {} : { headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) }),
  }, 25_000)
  if (!response.ok) {
    const data = await response.json().catch(() => ({}))
    throw new APIError(response.status, typeof data.error === 'string' ? data.error : 'Work action could not be completed', data)
  }
  return response.json()
}
