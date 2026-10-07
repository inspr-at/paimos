// SPDX-License-Identifier: AGPL-3.0-only
import { api } from './api.ts'
export const ATTENTION_KINDS = [
  { id: 'proposed', label: 'Proposed changes', labelDe: 'Vorgeschlagene Änderungen', one: 'Proposed change', oneDe: 'Vorgeschlagene Änderung', icon: 'sparkle' },
  { id: 'triage', label: 'Triage list', labelDe: 'Triage-Liste', one: 'Triage list', oneDe: 'Triage-Liste', icon: 'list' },
  { id: 'cancel', label: 'Cancel suggested', labelDe: 'Abbruch vorgeschlagen', one: 'Cancel suggested', oneDe: 'Abbruch vorgeschlagen', icon: 'cancelled' },
  { id: 'blocked', label: 'Blocked reminders', labelDe: 'Blockiert-Erinnerungen', one: 'Blocked reminder', oneDe: 'Blockiert-Erinnerung', icon: 'clock' },
  { id: 'missed', label: 'Missed releases', labelDe: 'Verpasste Releases', one: 'Missed release', oneDe: 'Verpasstes Release', icon: 'alert' },
] as const
export type AttentionKind = typeof ATTENTION_KINDS[number]['id']
export interface AttentionIdentity { event_id: number; node_id: string; revision: string; resolution_event_id?: number; release_id?: string; release_revision?: number; release_project_revision?: number }
export interface AttentionItem extends AttentionIdentity { key: string; title: string; project_id: string; kind: AttentionKind; from: string; to: string; reason: string; at: string; editable: boolean; applicable: boolean; unavailable_reason?: string; release_title?: string }
export interface AttentionFacet { id: string; label: string }
export interface AttentionPage { items: AttentionItem[]; total: number; counts: Partial<Record<AttentionKind, number>>; next_cursor: string | null; facets: { projects: AttentionFacet[]; assignees: AttentionFacet[] }; facets_truncated: boolean }
export interface AttentionResult { event_id: number; ok: boolean; resolution_event_id?: number; revision?: string; release_id?: string; release_revision?: number; previous_release_revision?: number; release_project_revision?: number; previous_release_project_revision?: number; error?: string }
export interface AttentionFilters { kind: string; project_id: string; assignee: string; q: string }
export type AttentionGrouping = 'project' | 'kind' | 'none'
export interface AttentionGroup {
  id: string; kind?: AttentionKind; project_id?: string; key?: string; title?: string
  total: number; counts: Partial<Record<AttentionKind, number>>; applicable: number; editable: number
  override_mode?: 'inherit' | 'on' | 'off'; can_manage: boolean
}
export interface AttentionGroupsPage { groups: AttentionGroup[]; total: number; truncated: boolean }
export const attentionGrouping = (value: unknown): AttentionGrouping => value === 'kind' || value === 'none' ? value : 'project'
const kindOrder = new Map<string, number>(ATTENTION_KINDS.map((kind, index) => [kind.id, index]))
export function orderAttentionGroups(groups: AttentionGroup[], by: Exclude<AttentionGrouping, 'none'>): AttentionGroup[] {
  return [...groups].sort((a, b) => by === 'kind'
    ? (kindOrder.get(a.kind ?? a.id) ?? 5) - (kindOrder.get(b.kind ?? b.id) ?? 5)
    : b.total - a.total || (a.key ?? '').localeCompare(b.key ?? '') || a.id.localeCompare(b.id))
}
export function orderAttentionRows<T extends AttentionItem>(items: T[]): T[] {
  return [...items].sort((a, b) => (kindOrder.get(a.kind) ?? 5) - (kindOrder.get(b.kind) ?? 5) || Date.parse(b.at) - Date.parse(a.at) || b.event_id - a.event_id)
}
// Saved decisions win; searches open matching groups without overwriting them.
export function attentionFolds(groups: AttentionGroup[], total: number, saved?: Record<string, boolean>, searching = false): Set<string> {
  return new Set(groups.flatMap((group, index) => !searching && (saved?.[group.id] ?? (total > 50 && index > 0)) ? [group.id] : []))
}
export const attentionIdentity = (item: AttentionIdentity): AttentionIdentity => ({ event_id: item.event_id, node_id: item.node_id, revision: item.revision, ...(item.resolution_event_id ? { resolution_event_id: item.resolution_event_id } : {}), ...(item.release_id ? { release_id: item.release_id, release_revision: item.release_revision, release_project_revision: item.release_project_revision } : {}) })
async function read<T>(path: string, signal: AbortSignal, body?: unknown): Promise<T> {
  const response = await api(path, { signal, ...(body ? { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) } : {}) })
  if (!response.ok) throw new Error(response.status === 403 ? 'You may no longer open this view.' : 'Needs attention could not be loaded or changed. Try again.')
  return response.json() as Promise<T>
}
export function listAttention(filters: AttentionFilters, signal: AbortSignal, after?: string) {
  const query = new URLSearchParams(Object.entries(filters).filter(([, value]) => value !== ''))
  if (after) query.set('after', after)
  return read<AttentionPage>(`/status-autopilot/attention?${query}`, signal)
}
export function listAttentionGroups(filters: AttentionFilters, by: Exclude<AttentionGrouping, 'none'>, signal: AbortSignal) {
  const query = new URLSearchParams(Object.entries(filters).filter(([, value]) => value !== ''))
  query.set('by', by)
  return read<AttentionGroupsPage>(`/status-autopilot/attention/groups?${query}`, signal)
}
export const actOnAttention = (action: 'apply' | 'dismiss' | 'undo', items: AttentionIdentity[], signal: AbortSignal) => read<{ items: AttentionResult[] }>('/status-autopilot/attention/actions', signal, { action, items })

// Only advance sibling identities that match both resource revisions verified
// by this committed write. A stale sibling remains stale after external edits.
export function refreshAttentionRelease(items: AttentionIdentity[], result: AttentionResult) {
  if (!result.ok || !result.release_id || result.release_revision === undefined || result.previous_release_revision === undefined || result.release_project_revision === undefined || result.previous_release_project_revision === undefined) return
  for (const item of items) if (item.release_id === result.release_id && item.release_revision === result.previous_release_revision && item.release_project_revision === result.previous_release_project_revision) {
    item.release_revision = result.release_revision; item.release_project_revision = result.release_project_revision
  }
}
