// SPDX-License-Identifier: AGPL-3.0-only
// How a view takes a live node change (AEON-326). Pure helpers, no Vue:
// field changes patch in place; structural changes (a row that closed, no
// longer matches, was deleted or moved, a new matching row) wait as pending
// updates behind an "N updates · Show" pill and apply only when it is safe.
import { statusMeta } from './work.ts'

export type ChangeKind = 'created' | 'updated' | 'deleted'
export interface NodeChangeSummary { change: ChangeKind; fields: string[] }

// ---------- Revisions ----------
// A revision is a node's updated_at (RFC 3339, any offset, up to nanoseconds).
// Date.parse keeps milliseconds only, and updates can be a microsecond apart.
function revisionParts(value: string): [number, number] | null {
  const match = /^(.*T\d{2}:\d{2}:\d{2})(?:\.(\d{1,9}))?(Z|[+-]\d{2}:?\d{2})$/i.exec(value.trim())
  if (!match) return null
  const seconds = Date.parse(`${match[1]}${match[3]}`)
  if (Number.isNaN(seconds)) return null
  return [seconds / 1000, Number((match[2] ?? '').padEnd(9, '0'))]
}
// Negative when a is older than b, 0 when equal, positive when newer. An
// unreadable or missing revision counts as older than any readable one.
export function compareRevision(a: string | null | undefined, b: string | null | undefined): number {
  const x = a ? revisionParts(a) : null
  const y = b ? revisionParts(b) : null
  if (!x || !y) return (x ? 1 : 0) - (y ? 1 : 0)
  return x[0] !== y[0] ? x[0] - y[0] : x[1] - y[1]
}

// ---------- Classification ----------
// changed: a field change to a row the person works with (selected, or open
// in an editor); it waits so their next change still meets it as a conflict.
export type Structural = 'closed' | 'no_longer_matches' | 'deleted' | 'new' | 'moved' | 'changed'
export type Classification = 'patch' | 'ignore' | Structural

export interface ClassifyInput<N extends { state: string }> {
  change: NodeChangeSummary
  // The node as the view shows it; null when the view does not show it.
  shown: N | null
  // The node refetched through the normal API; null when it is gone or no
  // longer readable; undefined when it was not fetched.
  node: N | null | undefined
  // The view's filter: true, false, or null when the client cannot tell
  // (a full-text query, say).
  matches: (node: N) => boolean | null
  // Attributes the view orders by: a change there would move the row.
  orderFields?: readonly string[]
}

const closed = (state: string) => statusMeta(state).closed

// patch: update the shown row in place. ignore: nothing this view shows.
// Everything else is structural and waits (see PendingUpdates).
export function classifyChange<N extends { state: string }>({ change, shown, node, matches, orderFields = [] }: ClassifyInput<N>): Classification {
  if (change.change === 'deleted' || node === null) return shown ? 'deleted' : 'ignore'
  if (node === undefined) return shown ? 'patch' : 'ignore'
  const fits = matches(node)
  if (!shown) {
    // A node the client cannot place is offered only when it is brand new.
    return fits === true || (fits === null && change.change === 'created') ? 'new' : 'ignore'
  }
  if (fits === false) return closed(node.state) && !closed(shown.state) ? 'closed' : 'no_longer_matches'
  return change.fields.some(field => orderFields.includes(field)) ? 'moved' : 'patch'
}

// ---------- Pending structural updates ----------
export const PENDING_CAP = 200
const LABELS: Record<Structural, string> = {
  closed: 'Closed', no_longer_matches: 'No longer matches', deleted: 'Deleted', moved: 'Moved', new: 'New', changed: 'Changed',
}
// Past the cap the oldest entries go in this order: new rows first (nothing
// shows them yet), deletions last (a selection learns of them).
const DROP_ORDER: readonly Structural[] = ['new', 'moved', 'changed', 'no_longer_matches', 'closed', 'deleted']

// Per view, keyed by node id; the latest classification wins. It keeps at
// most cap entries: once one had to go, the pill offers to reload the view.
export class PendingUpdates {
  private entries = new Map<string, Structural>()
  private dropped = false
  private readonly cap: number
  constructor(cap = PENDING_CAP) { this.cap = cap }

  // A patch or an ignore settles a pending entry: the node is back to what
  // the view shows (reopened, say). A node that was only pending as new and
  // is deleted again never appears. Returns the ids dropped past the cap.
  note(id: string, kind: Classification): string[] {
    const prior = this.entries.get(id)
    this.entries.delete(id)
    if (kind === 'patch' || kind === 'ignore' || (kind === 'deleted' && prior === 'new')) return []
    this.entries.set(id, kind)
    if (this.entries.size <= this.cap) return []
    this.dropped = true
    const out: string[] = []
    for (const drop of DROP_ORDER) {
      for (const [other, entry] of this.entries) {
        if (this.entries.size <= this.cap) return out
        if (entry === drop) { this.entries.delete(other); out.push(other) }
      }
    }
    return out
  }
  get count() { return this.entries.size }
  // Updates were dropped past the cap: the pill offers to reload the view instead.
  get overflow() { return this.dropped }
  kind(id: string): Structural | undefined { return this.entries.get(id) }
  // Every id still pending: a resync revalidates these as well as the rows on screen.
  ids(): string[] { return [...this.entries.keys()] }
  // The small label a marked row carries ("Closed", "Deleted", ...).
  label(id: string): string | null { const kind = this.entries.get(id); return kind ? LABELS[kind] : null }
  // Ids among these (a selection, say) that were deleted meanwhile.
  deletedAmong(ids: Iterable<string>): string[] { return [...ids].filter(id => this.entries.get(id) === 'deleted') }
  // Everything pending, cleared: the caller applies it at once.
  take(): Map<string, Structural> { const all = this.entries; this.entries = new Map(); this.dropped = false; return all }
  clear() { this.entries.clear(); this.dropped = false }
}

// The pill's words; null when nothing is pending.
export function pillText(pending: { count: number; overflow: boolean }): string | null {
  if (pending.overflow) return 'Many updates · Reload view'
  if (!pending.count) return null
  return `${pending.count} ${pending.count === 1 ? 'update' : 'updates'} · Show`
}

// ---------- When structural updates may apply by themselves ----------
export const IDLE_MS = 2000
export interface ApplyGuard {
  selected: number; editing: boolean; menuOpen: boolean; dialogOpen: boolean
  dragging: boolean; scrolling: boolean; hidden: boolean
  // The last pointer, key, wheel or scroll input, in ms (performance or epoch, like now).
  lastInputAt: number; now: number
}
// Nothing selected, no editor, menu, dialog, drag or scroll, the tab
// visible and 2 s without input. Otherwise the pill waits; no countdown.
export function canAutoApply(guard: ApplyGuard, idleMs = IDLE_MS): boolean {
  return autoApplyDelay(guard, idleMs) === 0
}
// 0 when it may apply now; the ms until idle when only idleness is missing;
// null while something else blocks it (check again when that changes).
export function autoApplyDelay(guard: ApplyGuard, idleMs = IDLE_MS): number | null {
  if (guard.selected > 0 || guard.editing || guard.menuOpen || guard.dialogOpen || guard.dragging || guard.scrolling || guard.hidden) return null
  return Math.max(0, idleMs - (guard.now - guard.lastInputAt))
}

// ---------- Words for a change ----------
const FIELD_WORDS: Record<string, string> = {
  title: 'title', body: 'description', state: 'status', parent_id: 'epic', project_id: 'project', key: 'key',
  'fields.priority': 'priority', 'fields.assignee': 'assignee', 'fields.acceptance_criteria': 'acceptance criteria',
  'fields.notes': 'notes', 'fields.estimate': 'estimate', 'fields.eta': 'ETA', 'fields.release': 'release',
}
// "status and priority" for a screen-reader announcement; other attributes
// read as "details". Empty when nothing a person would name changed.
export function describeFields(fields: readonly string[]): string {
  const words: string[] = []
  let other = false
  for (const field of fields) {
    const word = FIELD_WORDS[field]
    if (word) { if (!words.includes(word)) words.push(word) }
    else if (field !== 'position' && field !== 'kind_id') other = true
  }
  if (other) words.push('details')
  if (words.length <= 1) return words.join('')
  return `${words.slice(0, -1).join(', ')} and ${words[words.length - 1]}`
}
