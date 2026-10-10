// SPDX-License-Identifier: AGPL-3.0-only
import { api } from './api.ts'
import { compareRevision } from './liveUpdates.ts'
export const ATTENTION_KINDS = [
  { id: 'proposed', label: 'Proposed changes', labelDe: 'Vorgeschlagene Änderungen', one: 'Proposed change', oneDe: 'Vorgeschlagene Änderung', icon: 'sparkle' },
  { id: 'triage', label: 'Triage list', labelDe: 'Triage-Liste', one: 'Triage list', oneDe: 'Triage-Liste', icon: 'list' },
  { id: 'cancel', label: 'Cancel suggested', labelDe: 'Abbruch vorgeschlagen', one: 'Cancel suggested', oneDe: 'Abbruch vorgeschlagen', icon: 'cancelled' },
  { id: 'blocked', label: 'Blocked reminders', labelDe: 'Blockiert-Erinnerungen', one: 'Blocked reminder', oneDe: 'Blockiert-Erinnerung', icon: 'clock' },
  { id: 'missed', label: 'Done, but in no release', labelDe: 'Erledigt, aber in keinem Release', one: 'Done, but in no release', oneDe: 'Erledigt, aber in keinem Release', icon: 'alert' },
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
// AEON-914 serves group headers. Until that route exists, grouped mode folds the
// list endpoint. 500 is that route's group cap; 10 pages (50 rows each) bounds
// the walk so a missing route cannot page the whole queue.
export const ATTENTION_GROUP_CAP = 500
export const ATTENTION_GROUP_PAGE_CAP = 10
export function attentionGroupId(item: Pick<AttentionItem, 'kind' | 'project_id'>, by: Exclude<AttentionGrouping, 'none'>): string {
  return by === 'kind' ? item.kind : (item.project_id || 'none')
}
// Facet labels are `key title` from the list query. A sample key covers a project the facet page did not reach.
export function attentionProjectNames(id: string, sampleKey: string | undefined, facets: readonly AttentionFacet[]): { key: string; title: string } {
  const facet = facets.find(facet => facet.id === id)
  if (facet?.label) {
    const split = facet.label.indexOf(' ')
    if (split > 0) return { key: facet.label.slice(0, split), title: facet.label.slice(split + 1) }
    return { key: facet.label, title: facet.label }
  }
  const key = sampleKey?.replace(/-\d+$/, '') ?? ''
  return { key, title: key }
}
export function foldAttentionGroups(items: readonly AttentionItem[], by: Exclude<AttentionGrouping, 'none'>, cap = ATTENTION_GROUP_CAP): { groups: AttentionGroup[]; truncated: boolean } {
  if (!Number.isInteger(cap) || cap < 1) throw new Error('attention group cap')
  const groups: AttentionGroup[] = []
  const index = new Map<string, AttentionGroup>()
  let truncated = false
  for (const item of items) {
    const id = attentionGroupId(item, by)
    let group = index.get(id)
    if (!group) {
      if (groups.length >= cap) { truncated = true; continue }
      group = {
        id,
        ...(by === 'kind' ? { kind: item.kind } : { ...(item.project_id ? { project_id: item.project_id } : {}), key: item.key.replace(/-\d+$/, ''), title: '' }),
        total: 0, counts: {}, applicable: 0, editable: 0, can_manage: false,
      }
      index.set(id, group)
      groups.push(group)
    }
    group.total += 1
    group.counts[item.kind] = (group.counts[item.kind] ?? 0) + 1
    if (item.editable) group.editable += 1
    if (item.editable && item.applicable) group.applicable += 1
  }
  return { groups, truncated }
}
// Kind-then-newest is not the list cursor yet (AEON-914). Sorting one page and
// appending the next repeats kinds. Sort only once the cursor has ended.
export function mergeAttentionRows<T extends AttentionItem>(existing: readonly T[], page: { items: readonly T[]; next_cursor: string | null }): T[] {
  const seen = new Set(existing.map(row => row.event_id))
  const merged = [...existing, ...page.items.filter(row => !seen.has(row.event_id))]
  return page.next_cursor == null ? orderAttentionRows(merged) : merged
}
function attentionBound(value: number, max: number, label: string) {
  if (!Number.isInteger(value) || value < 1 || value > max) throw new Error(label)
  return value
}
export async function collectAttentionGroups(by: Exclude<AttentionGrouping, 'none'>, readPage: (after?: string) => Promise<AttentionPage>, options: { cap?: number; pageCap?: number } = {}): Promise<{ groups: AttentionGroup[]; truncated: boolean; pages: number }> {
  const cap = attentionBound(options.cap ?? ATTENTION_GROUP_CAP, ATTENTION_GROUP_CAP, 'attention group cap')
  const pageCap = attentionBound(options.pageCap ?? ATTENTION_GROUP_PAGE_CAP, ATTENTION_GROUP_PAGE_CAP, 'attention group page cap')
  const items: AttentionItem[] = []
  const seenEvents = new Set<number>()
  const seenGroups = new Set<string>()
  let after: string | undefined, truncated = false, pages = 0, more = false
  while (pages < pageCap) {
    const page = await readPage(after)
    pages += 1
    for (const item of page.items) {
      if (seenEvents.has(item.event_id)) continue
      seenEvents.add(item.event_id)
      const id = attentionGroupId(item, by)
      if (!seenGroups.has(id)) {
        if (seenGroups.size >= cap) { truncated = true; continue }
        seenGroups.add(id)
      }
      items.push(item)
    }
    more = !!page.next_cursor
    if (truncated || !more) break
    after = page.next_cursor!
  }
  if (more) truncated = true
  const folded = foldAttentionGroups(items, by, cap)
  return { groups: folded.groups, truncated: truncated || folded.truncated, pages }
}
// Kind totals come from the list count, which is not limited to the scanned pages.
// A kind filter keeps only that kind. Project names prefer the facet label.
export function finishAttentionGroups(built: { groups: AttentionGroup[]; truncated: boolean }, page: AttentionPage, by: Exclude<AttentionGrouping, 'none'>, filters: AttentionFilters, cap = ATTENTION_GROUP_CAP): { groups: AttentionGroup[]; truncated: boolean } {
  const groups = built.groups.map(group => ({ ...group, counts: { ...group.counts } }))
  let truncated = built.truncated
  if (by === 'kind') {
    for (const group of groups) {
      const id = (group.kind ?? group.id) as AttentionKind
      const count = page.counts[id] ?? group.total
      group.total = count
      group.counts = { [id]: count }
    }
    for (const kind of ATTENTION_KINDS) {
      const count = page.counts[kind.id] ?? 0
      if (count < 1 || groups.some(group => group.id === kind.id) || (filters.kind && filters.kind !== kind.id)) continue
      if (groups.length >= cap) { truncated = true; continue }
      groups.push({ id: kind.id, kind: kind.id, total: count, counts: { [kind.id]: count }, applicable: 0, editable: truncated ? 1 : 0, can_manage: false })
    }
  }
  for (const group of groups) {
    if (!group.project_id) continue
    const sample = page.items.find(item => item.project_id === group.project_id)
    const names = attentionProjectNames(group.project_id, sample?.key, page.facets.projects)
    if (names.key) group.key = names.key
    if (names.title) group.title = names.title
  }
  return { groups, truncated }
}
// Saved decisions win; searches open matching groups without overwriting them.
export function attentionFolds(groups: AttentionGroup[], total: number, saved?: Record<string, boolean>, searching = false): Set<string> {
  return new Set(groups.flatMap((group, index) => !searching && (saved?.[group.id] ?? (total > 50 && index > 0)) ? [group.id] : []))
}
export const attentionIdentity = (item: AttentionIdentity): AttentionIdentity => ({ event_id: item.event_id, node_id: item.node_id, revision: item.revision, ...(item.resolution_event_id ? { resolution_event_id: item.resolution_event_id } : {}), ...(item.release_id ? { release_id: item.release_id, release_revision: item.release_revision, release_project_revision: item.release_project_revision } : {}) })
const attentionFailure = (status: number) => new Error(status === 403 ? 'You may no longer open this view.' : 'Needs attention could not be loaded or changed. Try again.')
async function read<T>(path: string, signal: AbortSignal, body?: unknown): Promise<T> {
  const response = await api(path, { signal, ...(body ? { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) } : {}) })
  if (!response.ok) throw attentionFailure(response.status)
  return response.json() as Promise<T>
}
export function listAttention(filters: AttentionFilters, signal: AbortSignal, after?: string) {
  const query = new URLSearchParams(Object.entries(filters).filter(([, value]) => value !== ''))
  if (after) query.set('after', after)
  return read<AttentionPage>(`/status-autopilot/attention?${query}`, signal)
}
// Null means the route is not registered yet. AEON-914 ships the real response;
// any other failure still rejects so Retry is not shown a made-up group list.
export async function listAttentionGroups(filters: AttentionFilters, by: Exclude<AttentionGrouping, 'none'>, signal: AbortSignal): Promise<AttentionGroupsPage | null> {
  const query = new URLSearchParams(Object.entries(filters).filter(([, value]) => value !== ''))
  query.set('by', by)
  const response = await api(`/status-autopilot/attention/groups?${query}`, { signal })
  if (response.status === 404) return null
  if (!response.ok) throw attentionFailure(response.status)
  return response.json() as Promise<AttentionGroupsPage>
}
export const actOnAttention = (action: 'apply' | 'dismiss' | 'undo', items: AttentionIdentity[], signal: AbortSignal) => read<{ items: AttentionResult[] }>('/status-autopilot/attention/actions', signal, { action, items })

export type AttentionBulkAction = 'apply' | 'dismiss'
export interface AttentionSkip { reason: string; count: number; sample_keys: string[] }
export interface AttentionMove { id: string; kind: AttentionKind; from: string; to: string; release_id?: string; release_title?: string; count: number; sample_keys: string[] }
export interface AttentionBulkPreview { total: number; moves: AttentionMove[]; skipped: AttentionSkip[]; through_event_id: number; preview_token: string; limit: number; truncated: boolean }
export interface AttentionBulkResult { batch_id: string; changed: number; skipped: AttentionSkip[]; failed: { event_id: number; key: string; error: string }[]; completed: boolean }
export const attentionMoveId = (item: AttentionItem) => item.to === 'release' ? `m:${item.release_id}` : item.kind === 'proposed' ? `p:${item.from}>${item.to}` : item.kind
async function bulkRequest<T>(path: string, body: unknown, signal: AbortSignal, idempotencyKey?: string): Promise<T> {
  // The server reserves 20 seconds for a batch; allow its durable result to arrive.
  const response = await api(path, { method: 'POST', signal, headers: { 'Content-Type': 'application/json', ...(idempotencyKey ? { 'Idempotency-Key': idempotencyKey } : {}) }, body: JSON.stringify(body) }, 25000)
  if (!response.ok) {
    const reason = await response.json().catch(() => ({}))
    throw new Error(typeof reason.error === 'string' ? reason.error : typeof reason.message === 'string' ? reason.message : attentionFailure(response.status).message)
  }
  return response.json() as Promise<T>
}
export const previewAttentionBulk = (action: AttentionBulkAction, scope: AttentionFilters, signal: AbortSignal) => bulkRequest<AttentionBulkPreview>('/status-autopilot/attention/bulk', { action, scope, exclude: [], through_event_id: 0, dry_run: true }, signal)
export const runAttentionBulk = (action: AttentionBulkAction, scope: AttentionFilters, preview: AttentionBulkPreview, exclude: string[], idempotencyKey: string, signal: AbortSignal) => bulkRequest<AttentionBulkResult>('/status-autopilot/attention/bulk', { action, scope, exclude, through_event_id: preview.through_event_id, preview_token: preview.preview_token, dry_run: false }, signal, idempotencyKey)
export const undoAttentionBulk = (batchId: string, idempotencyKey: string, signal: AbortSignal) => bulkRequest<AttentionBulkResult>(`/status-autopilot/attention/bulk/${encodeURIComponent(batchId)}/undo`, {}, signal, idempotencyKey)

// List revisions are Go RFC3339Nano (Z). Event snapshots are PostgreSQL jsonb
// timestamps (numeric offset, up to microseconds). Compare the instant.
const sameInstant = (left: string | undefined, right: string | undefined) => !!left && !!right && compareRevision(left, right) === 0
const resolutionTypes = ['status_autopilot.attention_apply', 'status_autopilot.attention_dismiss', 'status_autopilot.attention_undone']

// Aggregate batch answers are not row identities. Confirm each loaded row from
// one bounded page of resolution events before offering its existing /actions Undo.
// No matching event returns null; the caller must show a row failure.
export async function attentionResolution(item: AttentionIdentity, action: 'apply' | 'dismiss' | 'undo', actorId: string, signal: AbortSignal): Promise<AttentionResult | null> {
  const query = new URLSearchParams({ node_id: item.node_id, after: String(action === 'undo' ? item.resolution_event_id : item.event_id), limit: '200', type: resolutionTypes.join(',') })
  type Event = { id: number; actor_principal_id: string; node_id: string; type: string; before?: { updated_at?: string }; after?: { updated_at?: string }; undo_of?: number }
  const page = await read<{ items: Event[]; next_after: number | null }>(`/events?${query}`, signal)
  const event = [...(page.items ?? [])].reverse().find(event => event.node_id === item.node_id && event.actor_principal_id === actorId && event.type === `status_autopilot.attention_${action === 'undo' ? 'undone' : action}` && (action === 'undo' ? event.undo_of === item.resolution_event_id : sameInstant(event.before?.updated_at, item.revision)))
  if (!event?.after?.updated_at) return null
  return { event_id: item.event_id, ok: true, revision: event.after.updated_at, ...(action !== 'undo' ? { resolution_event_id: event.id } : {}) }
}

// Only advance sibling identities that match both resource revisions verified
// by this committed write. A stale sibling remains stale after external edits.
export function refreshAttentionRelease(items: AttentionIdentity[], result: AttentionResult) {
  if (!result.ok || !result.release_id || result.release_revision === undefined || result.previous_release_revision === undefined || result.release_project_revision === undefined || result.previous_release_project_revision === undefined) return
  for (const item of items) if (item.release_id === result.release_id && item.release_revision === result.previous_release_revision && item.release_project_revision === result.previous_release_project_revision) {
    item.release_revision = result.release_revision; item.release_project_revision = result.release_project_revision
  }
}
