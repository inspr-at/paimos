// SPDX-License-Identifier: AGPL-3.0-only
import { api } from './api.ts'
export const ATTENTION_KINDS = [
  { id: 'proposed', label: 'Proposed changes', one: 'Proposed change', icon: 'sparkle' },
  { id: 'triage', label: 'Triage list', one: 'Triage list', icon: 'list' },
  { id: 'cancel', label: 'Cancel suggested', one: 'Cancel suggested', icon: 'cancelled' },
  { id: 'blocked', label: 'Blocked reminders', one: 'Blocked reminder', icon: 'clock' },
  { id: 'missed', label: 'Missed releases', one: 'Missed release', icon: 'alert' },
] as const
export type AttentionKind = typeof ATTENTION_KINDS[number]['id']
export interface AttentionIdentity { event_id: number; node_id: string; revision: string; resolution_event_id?: number; release_id?: string; release_revision?: number; release_project_revision?: number }
export interface AttentionItem extends AttentionIdentity { key: string; title: string; project_id: string; kind: AttentionKind; from: string; to: string; reason: string; at: string; editable: boolean; applicable: boolean; unavailable_reason?: string; release_title?: string }
export interface AttentionFacet { id: string; label: string }
export interface AttentionPage { items: AttentionItem[]; total: number; counts: Partial<Record<AttentionKind, number>>; next_cursor: string | null; facets: { projects: AttentionFacet[]; assignees: AttentionFacet[] }; facets_truncated: boolean }
export interface AttentionResult { event_id: number; ok: boolean; resolution_event_id?: number; revision?: string; release_id?: string; release_revision?: number; previous_release_revision?: number; release_project_revision?: number; previous_release_project_revision?: number; error?: string }
export interface AttentionFilters { kind: string; project_id: string; assignee: string; q: string }
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
export const actOnAttention = (action: 'apply' | 'dismiss' | 'undo', items: AttentionIdentity[], signal: AbortSignal) => read<{ items: AttentionResult[] }>('/status-autopilot/attention/actions', signal, { action, items })

// Only advance sibling identities that match both resource revisions verified
// by this committed write. A stale sibling remains stale after external edits.
export function refreshAttentionRelease(items: AttentionIdentity[], result: AttentionResult) {
  if (!result.ok || !result.release_id || result.release_revision === undefined || result.previous_release_revision === undefined || result.release_project_revision === undefined || result.previous_release_project_revision === undefined) return
  for (const item of items) if (item.release_id === result.release_id && item.release_revision === result.previous_release_revision && item.release_project_revision === result.previous_release_project_revision) {
    item.release_revision = result.release_revision; item.release_project_revision = result.release_project_revision
  }
}
