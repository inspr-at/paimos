// SPDX-License-Identifier: AGPL-3.0-only
import { APIError, api } from './api'
import { placeDelivery, type DeliveryActions } from './deliveryChanges'
import type { PlanningItem, PlanningRelease } from './deliveryPlanning'
export type MoveSubject = { kind: 'item'; record: PlanningItem } | { kind: 'release'; record: PlanningRelease }
export type MoveSlot = { before_id?: string; after_id?: string; position?: 'top' | 'append' }
export type MoveTarget = { release: PlanningRelease | null; slot: MoveSlot }
export const PICKUP_MS = 500
// A filtered/page gap follows its visible predecessor, or precedes its first
// visible successor. Never fabricate an extremum or send two apparent peers.
export function visibleGap(rows: { item_id?: string; release_id?: string; rank?: string }[], anchor: string, after: boolean, subject: string): MoveSlot {
  const peers = rows.filter(r => (r.item_id ?? r.release_id) !== subject && !!r.rank)
  const index = peers.findIndex(r => (r.item_id ?? r.release_id) === anchor)
  if (index < 0) throw new Error('The anchor changed. Reopen the move.')
  if (after) return { after_id: anchor }
  const previous = peers[index - 1]
  return previous ? { after_id: (previous.item_id ?? previous.release_id)! } : { before_id: anchor }
}
export function moveAdvice(subject: MoveSubject, target: MoveTarget, releases: PlanningRelease[], agent: boolean, allowed: boolean, now = Date.now()): string {
  if (!allowed) return 'Release edit permission is required.'
  const dest = target.release
  if (subject.kind === 'release') {
    if (agent) return 'Only a person can reorder releases.'
    if (subject.record.visibility === 'published') return 'Published releases keep their order.'
    if (['released','abandoned'].includes(subject.record.state) || dest && ['released','abandoned'].includes(dest.state)) return 'Only upcoming releases can be reordered.'
    return ''
  }
  const source = releases.find(r => r.release_id === subject.record.release_id)
  if (source && ['released','abandoned'].includes(source.state) || dest && ['released','abandoned'].includes(dest.state)) return 'Released work needs a separate history correction.'
  if (dest?.state === 'frozen' || source?.state === 'frozen' && (source.cut_at || source.version || dest?.release_id === source.release_id)) return 'This release is frozen. Adding or reordering work is closed, and cut scope stays fixed.'
  if (dest && dest.release_id !== subject.record.release_id && dest.occupied_rows !== undefined && dest.occupied_rows >= 1000) return 'This release is full (1,000 items).'
  if (agent) {
    if (dest?.entry_closes_at && Date.parse(dest.entry_closes_at) <= now && dest.release_id !== subject.record.release_id) return 'Entry has closed. A person must add work.'
    if (!subject.record.rank) return 'Only a person can rank new Backlog work.'
    if (dest && (!source || dest.rank < source.rank)) return 'Agents can only move work later. Ask a person.'
    if ((dest?.release_id ?? '') === (subject.record.release_id ?? '')) {
      if (target.slot.position === 'top') return 'Agents can only move work later. Ask a person.'
      // The server compares the final physical position, including hidden keys.
    }
  }
  return ''
}
export function refusal(error: unknown) {
  if (error instanceof APIError) {
    const codes: Record<string,string> = { frozen: 'This release is frozen. Adding or reordering work is closed, and cut scope stays fixed.', entry_closed: 'Entry has closed. A person must add work.', published_order: 'Published releases keep their order.', promotion: 'Agents can only move work later. Ask a person.', rank_space_exhausted: 'There is no rank space at this position.', revision_changed: 'This work changed. Reopen the move.', neighbours_changed: 'The anchor changed. Reopen the move.' }
    return codes[String(error.body?.code)] || error.message
  }
  return error instanceof Error ? error.message : 'The move was refused.'
}
export async function submitMove(subject: MoveSubject, target: MoveTarget, actions: DeliveryActions) {
  const record = subject.record
  if (subject.kind === 'item') return placeDelivery(record.project_id, subject.record, { release_id: target.release?.release_id ?? null, ...(target.release ? { expected_release_revision: target.release.revision } : {}), ...target.slot }, actions)
  const identity = actions.begin()
  try {
    const response = await api(`/projects/${encodeURIComponent(record.project_id)}/releases/${encodeURIComponent(subject.record.release_id)}/rank`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, signal: AbortSignal.timeout(10000), body: JSON.stringify({ expected_revision: record.revision, ...target.slot }) })
    const result = await response.json()
    if (!response.ok) throw new APIError(response.status, result.error || 'The reorder was refused', result)
    if (!actions.commit(identity, { kind: 'rank', result })) throw new Error('The project or person changed; the result was discarded.')
    return result
  } catch (e) { actions.failed(identity); throw e }
}
