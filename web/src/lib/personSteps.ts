// SPDX-License-Identifier: AGPL-3.0-only
import { onScopeDispose, ref, watch } from 'vue'
import { APIError, api, type Page, type WorkNode } from './api'
import { onAccessRevalidate } from './authz'
import { useSession } from '../stores/session'

export interface StepPerson { id: string; name: string }
export interface PersonStep {
  id: string; node_id: string; check_id: string; label: string; label_de?: string; to_status: string
  owner: StepPerson; checked: boolean; active: boolean; can_tick: boolean; revision: string
  definition_id: string | null; definition_revision: string | null; created_at: string
  ticket_key: string; ticket_title: string; project_id: string | null
}
export interface PersonSteps { node_id: string; revision: string; steps: PersonStep[]; undo_reassignment?: number }
async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await api(path, init)
  if (!response.ok) { const body = await response.json().catch(() => ({})); throw new APIError(response.status, body.error ?? 'The person step could not be saved.', body) }
  return response.json()
}
export const readPersonSteps = (id: string, signal?: AbortSignal) => request<PersonSteps>(`/nodes/${encodeURIComponent(id)}/person-steps`, { signal })
export const readStepOwners = (step: PersonStep, q: string, after?: string, signal?: AbortSignal) => request<Page<StepPerson>>(`/nodes/${encodeURIComponent(step.node_id)}/person-step-owners?${new URLSearchParams({ check_id: step.check_id, q, ...(after ? { after } : {}) })}`, { signal })
function writeStep(step: PersonStep, body: object, suffix = '') {
  return request<PersonSteps>(`/nodes/${encodeURIComponent(step.node_id)}/person-steps/${encodeURIComponent(step.check_id)}${suffix}`, {
    method: 'PUT', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ ...body, expected_revision: step.revision, definition_revision: step.definition_revision }),
  })
}
export const markPersonStep = (step: PersonStep, checked: boolean) => writeStep(step, { checked })
export const reassignPersonStep = (step: PersonStep, owner: string, undo?: number) => writeStep(step, { owner_id: owner, ...(undo ? { undo_reassignment: undo } : {}) }, '/owner')
export const stepWords = (step: PersonStep, viewer?: string, lang = 'en') => lang === 'de'
  ? step.owner.id === viewer ? 'Wartet auf eigene Prüfung' : `Wartet auf ${step.owner.name}`
  : step.owner.id === viewer ? 'Waiting for you' : `Waiting for ${step.owner.name}`

export function usePersonSteps(row: () => Pick<WorkNode, 'id' | 'updated_at'> | undefined | null) {
  const session = useSession(), data = ref<PersonSteps | null>(null), error = ref('')
  let generation = 0, abort: AbortController | undefined
  function reset() { generation++; abort?.abort(); data.value = null; error.value = '' }
  async function refresh() {
    abort?.abort(); const controller = new AbortController(); abort = controller
    const target = row(), actor = session.identity?.principal.id, tenant = session.identity?.tenant.id, turn = ++generation
    if (!target) { data.value = null; return }
    const id = target.id, revision = target.updated_at
    const current = () => turn === generation && row()?.id === id && row()?.updated_at === revision && session.identity?.principal.id === actor && session.identity?.tenant.id === tenant
    try { const read = await readPersonSteps(id, controller.signal); if (current() && read.node_id === id && read.revision === revision) { data.value = read; error.value = '' } }
    catch (e) { if (current() && !controller.signal.aborted) { data.value = null; error.value = e instanceof Error ? e.message : 'Person steps could not be read.' } }
  }
  watch(() => [row()?.id, row()?.updated_at, session.identity?.principal.id, session.identity?.tenant.id], (value, previous) => { if (value[0] !== previous?.[0] || value[2] !== previous?.[2] || value[3] !== previous?.[3]) reset(); void refresh() }, { immediate: true, flush: 'sync' })
  const stop = onAccessRevalidate({ reset, refresh: () => { void refresh() } })
  onScopeDispose(() => { stop(); reset() })
  return { data, error, refresh }
}
