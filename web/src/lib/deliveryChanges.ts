// SPDX-License-Identifier: AGPL-3.0-only
import { computed, onScopeDispose, ref, watch, type InjectionKey, type Ref } from 'vue'
import { api, APIError } from './api'
import { NODE_EVENTS } from './liveNodes'
import type { PlanningItem, PlanningRelease } from './deliveryPlanning'

export interface PlacementReceipt {
  items: { item_id: string; project_id: string; release_id?: string; rank: string; revision: number; expedite: boolean; due_on: string | null }[]
  release_revision?: number; release_ranks?: Record<string, string>; release_revisions?: Record<string, number>; undo_event_id: number | null
}
export type DeliveryCommit = { kind: 'placement'; result: PlacementReceipt } | { kind: 'rank'; result: Pick<PlanningRelease, 'release_id' | 'project_id' | 'revision' | 'rank'> & Partial<PlanningRelease> & { undo_event_id: number | null } } | { kind: 'lifecycle' }
export interface DeliveryActions {
  identity(): string
  begin(): string
  commit(identity: string, change: DeliveryCommit): boolean
  failed(identity: string): void
}
export const DELIVERY_ACTIONS: InjectionKey<DeliveryActions> = Symbol('delivery-actions')
const KNOWLEDGE_EVENTS = ['knowledge.created', 'knowledge.updated', 'knowledge.deleted', 'knowledge.learning_accepted', 'knowledge.learning_dismissed', 'knowledge.learning_drafted']
export const DELIVERY_EVENTS = ['ships_in.changed', 'release.reranked', 'release.state_changed', 'project.delivery_adopted', 'project.delivery_changed', 'project.adopted', 'release.planned', 'release.updated', 'release.frozen', 'release.cut', 'release.published', 'release.closed', 'release.abandoned', 'delivery.adopted', ...NODE_EVENTS, ...KNOWLEDGE_EVENTS, 'relation.created', 'relation.deleted', 'relation.undone', ...['registered','bound','heartbeat','yielded','stopped','stop_confirmed','removed','restored','revived','archived','metadata_changed','adopted','handed_over'].map(kind => `harness.${kind}`)]
interface ChangeEvent { id: number; type: string; project: string; undoOf?: number }
interface Source { addEventListener(type: string, listener: (event: MessageEvent) => void): void; close(): void; onerror: (() => void) | null }
export function parseDeliveryEvent(raw: string, project: string): ChangeEvent | null {
  if (raw.length > 1 << 20) return null
  try {
    const e = JSON.parse(raw)
    if (!Number.isSafeInteger(e.id) || e.id < 1 || typeof e.type !== 'string') return null
    // Relation snapshots carry endpoint ids but no project. Holding their
    // permitted tenant event is conservative and avoids unbounded endpoint reads.
    const relation = e.type.startsWith('relation.') && [e.before, e.after].some(v => v?.type === 'relates' && typeof v.source_node_id === 'string' && typeof v.target_node_id === 'string')
    const relevant = relation || [e.before, e.after].some(value => {
      if (!value || typeof value !== 'object') return false
      return value.project_id === project || value.project_node_id === project || value.session?.project_id === project || value.release?.project_id === project
    }) || e.node_id === project || (Array.isArray(e.node_changes) && e.node_changes.some((n: { project_id?: string; id?: string }) => n.project_id === project || n.id === project))
    if (!relevant) return null
    return { id: e.id, type: e.type, project, undoOf: Number.isSafeInteger(e.undo_of) ? e.undo_of : undefined }
  } catch { return null }
}

// Own writes are correlated by the transaction's receipt, including echoes
// arriving before the response. Another action by the same actor stays foreign.
export function useDeliveryChanges(project: Ref<string | null>, owner: Ref<string>, options: { context?: () => unknown; open?: () => Source; applied?: () => void; committed?: (change: DeliveryCommit) => void } = {}) {
  const pending = ref(0), overflow = ref(false), applied = ref(0), version = ref(0), undoBusy = ref(false), error = ref('')
  const undo = ref<{ id: number; identity: string } | null>(null)
  const identity = computed(() => JSON.stringify([project.value, owner.value, options.context?.()]))
  const held = new Map<number, ChangeEvent>(), buffered = new Map<number, ChangeEvent>(), own = new Set<number>()
  let source: Source | null = null, inFlight = 0, firstOpen = true, bufferedOverflow = false
  const sync = () => { pending.value = held.size + Number(overflow.value) }
  function flushBuffered() {
    if (inFlight || undoBusy.value) return
    for (const [id, event] of buffered) if (!own.has(id)) held.set(id, event)
    buffered.clear()
    overflow.value ||= bufferedOverflow; bufferedOverflow = false
    sync()
  }
  function receive(raw: string) {
    if (!project.value) return
    const event = parseDeliveryEvent(raw, project.value)
    if (!event || own.has(event.id) || held.has(event.id) || buffered.has(event.id)) return
    // A stream echo may beat its HTTP receipt. Keep new events unexposed until
    // every pending write (or Undo) has reconciled its exact committed id.
    const unresolved = inFlight > 0 || undoBusy.value
    if (held.size + buffered.size >= 500) {
      if (unresolved) bufferedOverflow = true
      else overflow.value = true
    } else (unresolved ? buffered : held).set(event.id, event)
    sync()
  }
  function reconnect() { if (firstOpen) { firstOpen = false; return }; overflow.value = true; sync() }
  watch(identity, () => {
    source?.close(); source = null; held.clear(); buffered.clear(); own.clear(); bufferedOverflow = false; inFlight = 0; undoBusy.value = false; undo.value = null; error.value = ''; overflow.value = false; sync(); firstOpen = true
    if (!project.value || !owner.value) return
    source = options.open ? options.open() : typeof EventSource === 'undefined' ? null : new EventSource('/api/events/stream?after=latest') as unknown as Source
    if (!source) return
    // Capture the identity in listeners too: a closed transport may still queue a callback.
    const captured = identity.value
    for (const type of DELIVERY_EVENTS) source.addEventListener(type, e => { if (captured === identity.value) receive(e.data) })
    source.addEventListener('open', () => { if (captured === identity.value) reconnect() })
    source.addEventListener('reset', () => { if (captured === identity.value) { overflow.value = true; sync() } })
    source.onerror = () => { if (captured === identity.value) error.value = 'Live changes are disconnected. Reload or apply to read current work.' }
  }, { immediate: true, flush: 'sync' })
  function remember(id: number | null) {
    if (!id || !Number.isSafeInteger(id)) return
    own.add(id); held.delete(id); buffered.delete(id)
    // Preserve bounded receipts; an old echo is safely held, never auto-applied.
    if (own.size > 500) own.delete(own.values().next().value!)
    sync()
  }
  const actions: DeliveryActions = {
    identity: () => identity.value,
    begin() { inFlight++; return identity.value },
    failed(captured) { if (captured === identity.value) { inFlight = Math.max(0, inFlight - 1); flushBuffered() } },
    commit(captured, change) {
      if (captured !== identity.value) return false
      error.value = ''
      if (change.kind !== 'lifecycle') {
        const result = change.result
        if ('items' in result ? result.items.some(it => it.project_id !== project.value) : result.project_id !== project.value) { error.value = 'The committed action belongs to another project'; return false }
        remember(result.undo_event_id)
        undo.value = result.undo_event_id ? { id: result.undo_event_id, identity: captured } : null
      } else undo.value = null
      inFlight = Math.max(0, inFlight - 1); flushBuffered()
      options.committed?.(change); version.value++; return true
    },
  }
  function apply() {
    if (inFlight || undoBusy.value) return
    held.clear(); overflow.value = false; sync(); applied.value++; error.value = ''; options.applied?.()
  }
  async function undoLast() {
    const receipt = undo.value
    if (!receipt || undoBusy.value || receipt.identity !== identity.value) return
    undoBusy.value = true; error.value = ''
    try {
      const response = await api(`/events/${receipt.id}/undo`, { method: 'POST', signal: AbortSignal.timeout(10000) })
      const body = await response.json().catch(() => ({}))
      if (receipt.identity !== identity.value || undo.value?.id !== receipt.id) return
      if (!response.ok) throw new APIError(response.status, body.error || 'This action changed again and could not be undone.', body)
      // The compensating response also has its exact event id. Discard only that echo.
      if (body.undo_of !== receipt.id || !Number.isSafeInteger(body.id)) throw new Error('Undo returned an invalid event receipt')
      remember(body.id)
      if (body.type === 'ships_in.changed' && Array.isArray(body.after?.members)) options.committed?.({ kind: 'placement', result: { items: body.after.members, release_revisions: body.after.release_revisions, release_ranks: body.after.release_ranks, undo_event_id: null } })
      else if (body.type === 'release.reranked' && body.after?.release_id) options.committed?.({ kind: 'rank', result: { ...body.after, undo_event_id: null } })
      undo.value = null; version.value++
    } catch (e) { if (receipt.identity === identity.value) error.value = e instanceof Error ? e.message : 'Undo failed' }
    finally { if (receipt.identity === identity.value) { undoBusy.value = false; flushBuffered() } }
  }
  onScopeDispose(() => source?.close())
  return { identity, actions, pending, overflow, applied, version, undo, undoBusy, error, receive, apply, undoLast }
}
// Capture actual committed placement, never a inferred newest event or optimistic revision.
export async function placeDelivery(project: string, item: Pick<PlanningItem, 'item_id' | 'revision' | 'project_id'>, body: Record<string, unknown>, actions: DeliveryActions): Promise<PlacementReceipt> {
  if (item.project_id !== project) throw new Error('The item belongs to another project')
  const captured = actions.begin()
  try {
    const response = await api(`/nodes/${encodeURIComponent(item.item_id)}/ships-in`, { method: 'PUT', signal: AbortSignal.timeout(10000), headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ ...body, expected_project_id: item.project_id, expected_revision: item.revision }) })
    const result = await response.json()
    if (!response.ok) throw new APIError(response.status, result.error || 'The move was refused', result)
    if (!actions.commit(captured, { kind: 'placement', result })) throw new Error('The project or person changed; the move result was discarded')
    return result
  } catch (e) { actions.failed(captured); throw e }
}
