// SPDX-License-Identifier: AGPL-3.0-only
import { ref } from 'vue'
import { api, APIError } from './api'
import { onAccessChange, permissionsRevoked } from './authz'

export type FeatureKey = 'workspace-summary'
export interface FeatureEvaluation {
  key: FeatureKey; label: string; description: string
  enabled: boolean; source: 'default' | 'tenant' | 'project'
}
export interface FeatureSetting extends FeatureEvaluation { override: boolean | null; revision: number }
export interface FeaturePage<T> { project_id: string | null; items: T[] }
export interface FeatureWrite { feature: FeatureSetting; event_id: number | null }
const query = (projectId?: string) => projectId ? `?project_id=${encodeURIComponent(projectId)}` : ''

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await api(path, init)
  if (!response.ok) {
    const body = await response.json().catch(() => ({}))
    throw new APIError(response.status, typeof body.error === 'string' ? body.error : 'Feature settings unavailable')
  }
  return response.json() as Promise<T>
}

export const getFeatureSettings = (projectId?: string) => request<FeaturePage<FeatureSetting>>(`/settings/features${query(projectId)}`)
export async function setFeatureOverride(key: FeatureKey, enabled: boolean | null, revision: number, projectId?: string): Promise<FeatureWrite> {
  const result = await request<FeatureWrite>(`/settings/features/${encodeURIComponent(key)}${query(projectId)}`, {
    method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ enabled, expected_revision: revision }),
  })
  // A tenant change affects every cached project, and a project change affects
  // that scope. Re-evaluate on the same page without a reload or deployment.
  const scopes = projectId ? [projectId] : [...new Set(['', ...cache.keys(), ...requests.keys()])]
  await Promise.all(scopes.map(scope => refreshFeatures(scope || undefined, true)))
  return result
}

const cache = new Map<string, FeatureEvaluation[] | null>()
const requests = new Map<string, Promise<void>>()
const turns = new Map<string, number>()
const revision = ref(0)
let epoch = 0

export function clearFeatures(): void {
  epoch++; cache.clear(); requests.clear(); turns.clear(); revision.value++
}

// Same shape as can(), but explicitly separate from permissions. A feature
// check never authorizes an operation. Unknown, pending, failed and revoked
// answers are OFF; scope changes cannot reuse a tenant's/project's answer.
export function canFeature(key: FeatureKey, projectId?: string): boolean {
  revision.value
  if (permissionsRevoked()) return false
  const scope = projectId || ''
  if (!cache.has(scope)) { void refreshFeatures(projectId); return false }
  return cache.get(scope)?.find(feature => feature.key === key)?.enabled === true
}

export function refreshFeatures(projectId?: string, force = false): Promise<void> {
  if (permissionsRevoked()) return Promise.resolve()
  const scope = projectId || ''
  if (!force && requests.has(scope)) return requests.get(scope)!
  const started = epoch
  const turn = (turns.get(scope) ?? 0) + 1
  turns.set(scope, turn)
  cache.set(scope, null); revision.value++
  const current = () => epoch === started && turns.get(scope) === turn && !permissionsRevoked()
  const pending = (async () => {
    try {
      const page = await request<FeaturePage<FeatureEvaluation>>(`/features${query(projectId)}`)
      if (page.project_id !== (projectId || null) || !Array.isArray(page.items)
        || page.items.some(item => typeof item?.key !== 'string' || typeof item.enabled !== 'boolean'
          || !['default', 'tenant', 'project'].includes(item.source))) throw new Error('Invalid feature evaluation')
      if (current()) cache.set(scope, page.items)
    } catch { if (current()) cache.set(scope, null) }
    finally { if (current()) { requests.delete(scope); revision.value++ } }
  })()
  requests.set(scope, pending)
  return pending
}

// Identity/workspace changes and 401s discard in-flight results. Focus and
// navigation re-evaluate known scopes through the existing access lifecycle.
onAccessChange(change => {
  if (change === 'reset') { clearFeatures(); return }
  const scopes = new Set([...cache.keys(), ...requests.keys()])
  for (const scope of scopes) void refreshFeatures(scope || undefined, true)
})
