// SPDX-License-Identifier: AGPL-3.0-only
import { computed, onScopeDispose, ref, watch } from 'vue'
import { APIError, api, type ListItem } from './api'
import { normaliseState, statusMeta } from './work'
import { useSession } from '../stores/session'

export type EntryStatus = 'in_progress' | 'qa' | 'done'
export interface EntryPerson { id: string; name: string }
export interface EntryCheck { id: string; label: string; label_de?: string; condition?: string; kinds?: string[]; labels?: string[]; applicable: boolean; passed: boolean; reason: string; owner?: EntryPerson | null; can_tick: boolean; checked_by?: EntryPerson | null; checked_at?: string | null }
export interface EntryBlock { to_status: string; passed: number; total: number; checks: EntryCheck[] }
export interface EntryRecord extends EntryBlock { at: string; by: EntryPerson; override_reason: string | null; definition_id: string; definition_revision: string }
export interface EntryRefusal { to_status: string; failed: EntryCheck[]; at: string; by: EntryPerson; count: number }
export interface EntryChecks { node_id: string; revision: string; definition: { id: string; revision: string; source: 'project' | 'workspace'; copied_from_revision?: string | null; checks: Record<string, unknown[]> } | null; blocks: EntryBlock[]; entries: EntryRecord[]; latest_refusal: EntryRefusal | null }
export interface EntryFailure { error: 'entry_checks_failed'; to_status: string; failed: EntryCheck[]; override_allowed?: boolean }

export function entryStatus(state: string): string {
  const s = normaliseState(state)
  if (['progress', 'active', 'in_progress'].includes(s)) return 'in_progress'
  if (['accepted', 'delivered'].includes(s)) return 'done'
  return s
}
export function nextEntryStatus(state: string): EntryStatus | null {
  const s = entryStatus(state)
  return ['new', 'backlog', 'open', 'blocked'].includes(s) ? 'in_progress' : s === 'in_progress' ? 'qa' : s === 'qa' ? 'done' : null
}
export function entryLabel(status: string, lang = 'en'): string {
  return status === 'in_progress' ? lang === 'de' ? 'Bereit' : 'Ready' : status === 'done' ? lang === 'de' ? 'Erledigt' : 'Done' : 'QA'
}
export function conditionWords(check: EntryCheck): string {
  return ({ ui: 'Only for UI tickets', code: 'Only for code tickets', blocked: 'Only for blocked tickets', release_notes: 'Hidden from release notes', releases: 'Only for projects that use releases' } as Record<string, string>)[check.condition ?? ''] ?? 'Not applicable to this ticket'
}
export function entryLine(data: EntryChecks | null, state: string) {
  if (!data?.definition) return null
  const next = nextEntryStatus(state), block = data.blocks.find(b => b.to_status === next)
  const record = data.entries.find(e => e.to_status === entryStatus(state))
  if (block?.total) return { block, record, readonly: false, text: `${entryLabel(block.to_status)} ${block.passed}/${block.total}` }
  if (record) return { block: record, record, readonly: true, text: record.override_reason ? `${entryLabel(record.to_status)} · overridden` : `${entryLabel(record.to_status)} ${record.passed}/${record.total}` }
  return { block: null, record: null, readonly: true, text: '' }
}
export function entryMenuCount(data: EntryChecks | null, current: string, state: string): { text: string; overridden: boolean } | null {
  const target = entryStatus(state), now = entryStatus(current)
  if (target === now) {
    const record = data?.entries.find(e => e.to_status === target)
    return record ? { text: `${record.passed} of ${record.total}`, overridden: !!record.override_reason } : null
  }
  const order = ['new', 'backlog', 'open', 'in_progress', 'blocked', 'qa', 'done']
  if (order.indexOf(target) <= order.indexOf(now)) return null
  const block = data?.blocks.find(b => b.to_status === target)
  return block?.total ? { text: `${block.passed} of ${block.total}`, overridden: false } : null
}
async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await api(path, init)
  if (!response.ok) { const body = await response.json().catch(() => ({})); throw new APIError(response.status, body.error ?? 'Checks could not be read', body) }
  return response.status === 204 ? undefined as T : response.json()
}
export const readEntryChecks = (id: string, signal?: AbortSignal) => request<EntryChecks>(`/nodes/${encodeURIComponent(id)}/entry-checks`, { signal })
export const tickEntryCheck = (id: string, check: string, checked: boolean, revision: string) => request<EntryChecks>(`/nodes/${encodeURIComponent(id)}/entry-checks/${encodeURIComponent(check)}`, { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ checked, expected_revision: revision }) })
export function entryFailure(error: unknown): EntryFailure | null {
  return error instanceof APIError && error.status === 409 && error.body.error === 'entry_checks_failed' && typeof error.body.to_status === 'string' && Array.isArray(error.body.failed) ? error.body as unknown as EntryFailure : null
}
export interface EntryPanelRequest { row: ListItem; toStatus: string; anchor?: HTMLElement; refusal?: EntryFailure }
export function requestEntryPanel(detail: EntryPanelRequest) { window.dispatchEvent(new CustomEvent<EntryPanelRequest>('aeon:entry-checks', { detail })) }

export function useEntryChecks(row: () => Pick<ListItem, 'id' | 'updated_at'> | null | undefined) {
  const session = useSession(), data = ref<EntryChecks | null>(null), error = ref(''), loading = ref(false)
  let generation = 0, controller: AbortController | undefined
  async function refresh() {
    controller?.abort(); controller = new AbortController()
    const target = row(), actor = session.identity?.principal.id, tenant = session.identity?.tenant.id, requestId = ++generation
    if (!target) { data.value = null; return }
    const id = target.id, revision = target.updated_at
    loading.value = true; error.value = ''
    try {
      const result = await readEntryChecks(id, controller.signal)
      if (requestId === generation && row()?.id === id && row()?.updated_at === revision && session.identity?.principal.id === actor && session.identity?.tenant.id === tenant && result.node_id === id && (!revision || result.revision === revision)) data.value = result
    } catch (e) { if (requestId === generation && !(e instanceof DOMException && e.name === 'AbortError')) error.value = e instanceof Error ? e.message : 'Checks could not be read' }
    finally { if (requestId === generation) loading.value = false }
  }
  watch(() => [row()?.id, row()?.updated_at, session.identity?.principal.id, session.identity?.tenant.id], (value, old) => {
    if (value[0] !== old?.[0] || value[2] !== old?.[2] || value[3] !== old?.[3]) data.value = null
    void refresh()
  }, { immediate: true })
  onScopeDispose(() => { generation++; controller?.abort() })
  return { data, error, loading, refresh, defined: computed(() => !!data.value?.definition) }
}
