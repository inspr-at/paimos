// SPDX-License-Identifier: AGPL-3.0-only
// The tab's causal row store (AEON-326). One entry per node, shared by the
// ticket list and the ticket panel (wherever the panel opens), and fed by
// every read and every write: list pages, refetches, panel reads, save
// answers and live events. Because every path goes through here, four rules
// hold by construction:
//
// 1. Revisions only move forward. A copy is kept only when its revision is at
//    least the newest the store knows for the node (from a copy, an event or
//    a write); an older one (a slow list page landing after a save or an
//    event) is dropped.
// 2. A deletion leaves a tombstone. Nothing at or before its revision brings
//    the node back, then or after a restore; only a newer revision does, and
//    a restored node shows only once a copy from after the restore arrived.
//    Reads sent before a stream gap are not trusted (they may have missed a
//    deletion): views read again instead of showing them.
// 3. This tab's writes are known by the exact revision the server answered
//    with (a delete's comes in its Aeon-Revision header). Only those events
//    are this tab's own; the node id alone never proves it.
// 4. One display object per node. Views show it; the store assigns copies to
//    it, never older than what it shows. An editor pins it and keeps its own
//    base: one copy, its revision and its values. Adopting a newer version
//    rebases the editor onto the store's copy for the same id.
// 5. Views change what they show only through the store: which rows (adopt
//    returns a display object only while it may show), their values (show,
//    optimistic), their projections (amend, for one revision and parent;
//    child, once per child) and whether a node is gone (isDeleted).
import { reactive, shallowReactive, toRaw } from 'vue'
import type { Kind, ListItem, ListParent, WorkNode } from './api.ts'
import { compareRevision } from './liveUpdates.ts'
import { positionOf } from './position.ts'

// The change an event names (liveNodes' NodeChange, the part the store reads).
export interface RowChange { id: string; change: 'created' | 'updated' | 'deleted'; revision: string | null; fields?: string[]; eventId?: number; type?: string }
// What an event told the store. older: it knew a newer revision (a replay);
// known: that revision; newer: something it did not know.
export type News = 'older' | 'known' | 'newer'

interface Tomb { revision: string | null; at: number }
interface Entry {
  // The display object views show (reactive); null until a copy arrived.
  row: ListItem | null
  // The server copy the display object shows, and the newest one known.
  shown: ListItem | null
  latest: ListItem | null
  // latest came from a list page (every projection), not from a node read.
  full: boolean
  // The newest revision known from anything: a copy, an event, a write.
  revision: string | null
  tomb: Tomb | null
  // The newest revision a deletion had: no copy at or before it is ever kept.
  floor: string | null
  // The newest copy a tombstone without revision held off (read while the
  // deletion was not yet known): once the deletion's revision is, a copy
  // after it is a restore.
  held: { copy: ListItem; sent: number; full: boolean } | null
  // Revisions this tab's writes produced.
  own: string[]
  // Children this tab counted in or out (child), so each counts once.
  children: Map<string, boolean>
  // Child membership changes do not advance the parent's node revision.
  countChangedAt: number
  pins: number
  // Clock of the newest news (a newer revision, a tombstone, a restore).
  touched: number
  // Clock when the newest read that confirmed latest was sent.
  readAt: number
  // List projections change independently of updated_at. Order their snapshots
  // by the server event position (AEON-449), then request order for older servers.
  projectionRead?: { position?: number; sent: number; landed: number }
  projectionChangedAt?: number
  projectionFloor?: number
  // Keep the most recently delivered hint separately from the monotonic floor:
  // after a confirmed database rewind, old positions no longer set that floor.
  projectionHintPosition?: number
}

// List attributes a list page may leave out (an older server): a newer page
// without them clears them.
const OPTIONAL = ['epic', 'eta', 'lead_worker', 'estimate'] as const
const OWN_PER_NODE = 8
const CHILDREN_PER_NODE = 64
const LIMIT = 4000

const clone = <T>(value: T): T => JSON.parse(JSON.stringify(toRaw(value))) as T
function freeze<T>(value: T): T {
  if (value && typeof value === 'object' && !Object.isFrozen(value)) {
    for (const inner of Object.values(value)) freeze(inner)
    Object.freeze(value)
  }
  return value
}
// A server copy: private, and never changed after it arrived.
const frozen = (copy: ListItem): ListItem => freeze(clone(copy))

// A node read (GET, a save answer) in list shape: the node's own attributes
// over the newest copy, which keeps the list projections (project, parent,
// epic, lead worker) that a node read does not carry. After a move the
// parent chip and the epic come from the parent as the store knows it.
export function fromNode(previous: ListItem | null, node: WorkNode, name: (id: string) => string | undefined = () => undefined, parentOf: (id: string) => ListParent | undefined = () => undefined, kindOf: (id: string) => Pick<Kind, 'slug' | 'label'> | undefined = () => undefined): ListItem {
  const fields = node.fields ?? {}
  const assigneeId = typeof fields.assignee === 'string' && fields.assignee ? fields.assignee : null
  const base: ListItem = previous ?? { ...node, kind_slug: '', kind_label: '', priority: null, assignee: null, parent: null, children_count: 0, project: null }
  const kind = kindOf(node.kind_id)
  const moved = !!previous && previous.parent_id !== node.parent_id
  const parent = !moved ? {} : placed(node.parent_id, previous!.project, parentOf)
  return {
    ...base, ...node, ...parent,
    ...(kind ? { kind_slug: kind.slug, kind_label: kind.label } : {}),
    priority: typeof fields.priority === 'string' && fields.priority ? fields.priority : null,
    assignee: assigneeId ? (previous?.assignee?.id === assigneeId ? previous.assignee : { id: assigneeId, name: name(assigneeId) ?? 'Someone' }) : null,
  }
}
// The parent chip and the epic under a parent: the project, or a parent the
// store has seen as one (an epic's are its own). Unknown: left to a list read.
function placed(parentId: string | null, project: ListItem['project'], parentOf: (id: string) => ListParent | undefined): Partial<Pick<ListItem, 'parent' | 'epic'>> {
  if (!parentId) return { parent: null, epic: null }
  if (project?.id === parentId) return { parent: { id: project.id, key: project.key, title: project.title, kind_slug: 'project' }, epic: null }
  const parent = parentOf(parentId)
  if (!parent) return {}
  return parent.kind_slug === 'epic' ? { parent, epic: { id: parent.id, key: parent.key, title: parent.title } } : { parent }
}

// An open editor on one node. Its base is one server copy: the revision a
// save sends as its precondition, and the values the save builds on.
export class EditorSession {
  readonly id: string
  private store: RowStore
  private ended = false
  base: ListItem
  constructor(store: RowStore, id: string, base: ListItem) { this.store = store; this.id = id; this.base = base }
  get revision(): string { return this.base.updated_at }
  // A conflict: the store's newest copy for this id shows under the editor
  // and becomes the base.
  rebase(): ListItem {
    this.base = this.store.promote(this.id) ?? this.base
    return this.base
  }
  // This editor's own save: its answer is the base, and the row shows it.
  // A newer copy by someone else still waits.
  saved(node: WorkNode): ListItem {
    this.base = this.store.showWrite(this.id, node, this.base)
    return this.base
  }
  // Editing ended: the row takes what waited meanwhile. True when it did.
  end(): boolean {
    if (this.ended) return false
    this.ended = true
    return this.store.unpin(this.id)
  }
  get open(): boolean { return !this.ended }
}

export class RowStore {
  private entries = new Map<string, Entry>()
  private clock = 0
  private gapAt = 0
  private names = new Map<string, string>()
  private kinds = shallowReactive(new Map<string, Pick<Kind, 'slug' | 'label'> & Partial<Pick<Kind, 'field_schema'>>>())
  // Parents as list copies and moves named them (their chip), for node reads after a move.
  private parents = new Map<string, ListParent>()
  private holders = new Set<() => Iterable<string>>()

  // ---------- Clock ----------
  // A request is about to be sent: its answer is judged against news after this.
  mark(): number { return ++this.clock }
  // The stream could not bridge a gap: what was read before may have missed a change.
  gap() { this.gapAt = ++this.clock }
  gapSince(mark: number): boolean { return this.gapAt > mark }
  // Something newer than a request sent at mark is known about this node.
  touchedSince(id: string, mark: number): boolean { return (this.entries.get(id)?.touched ?? 0) > mark }
  // Every node with news after mark (a reload looks at those it does not show).
  changedSince(mark: number): string[] { return [...this.entries].flatMap(([id, entry]) => entry.touched > mark ? [id] : []) }

  // ---------- Reads ----------
  // A copy read from the server (a list page, a create answer in list shape).
  // Returns the node's display object, or null while a tombstone at or after
  // this copy hides it. show: the display object takes the newest copy now
  // (unless an editor pins it); otherwise views decide when. full: false for
  // a copy with projections the reader could not fill.
  // projectionFloor: the hint that triggered this batch. A page covering it may
  // advance the display while a newer hint still waits for the follow-up read.
  adopt(copy: ListItem, sent = this.clock, options: { show?: boolean; full?: boolean; projectionFloor?: number } = {}): ListItem | null {
    return this.take(copy.id, sent, options.full ?? true, () => frozen(copy), copy.updated_at, !!copy.deleted_at, options.show ?? false, positionOf(copy), options.projectionFloor)
  }
  // A node read (GET, a save answer): merged over the newest copy.
  adoptNode(node: WorkNode, sent = this.clock, options: { show?: boolean } = {}): ListItem | null {
    return this.take(node.id, sent, false, () => {
      const entry = this.entries.get(node.id)
      return frozen(fromNode(entry?.latest ?? entry?.row ?? null, clone(node), id => this.names.get(id), id => this.parents.get(id), id => this.kinds.get(id)))
    }, node.updated_at, !!node.deleted_at, options.show ?? false)
  }
  private take(id: string, sent: number, full: boolean, make: () => ListItem, revision: string, deleted: boolean, show: boolean, position?: number, projectionFloor?: number): ListItem | null {
    const entry = this.entry(id)
    if (deleted) { this.bury(entry, revision, sent); return null }
    // A copy from before a deletion never brings the node back, nor shows
    // after a restore: it is dropped, and the node shows only once a copy
    // from after the restore arrived.
    const early = !!entry.floor && compareRevision(revision, entry.floor) <= 0
    if (entry.tomb && !early) {
      // Without a revision, only a read sent after the deletion was known
      // can; an earlier one waits until the deletion's revision is known.
      if (!entry.tomb.revision && sent <= entry.tomb.at) {
        if (!entry.held || compareRevision(revision, entry.held.copy.updated_at) > 0) entry.held = { copy: make(), sent, full }
        return null
      }
      entry.tomb = null
      entry.held = null
      entry.touched = ++this.clock
    }
    if (entry.tomb) return null
    const order = entry.latest ? compareRevision(revision, entry.latest.updated_at) : 1
    const held = entry.projectionRead
    const backwards = held && sent > held.landed
    const rewound = backwards && position !== undefined && held.position !== undefined && position < held.position
    const olderProjection = held && position !== undefined && held.position !== undefined && position !== held.position
      ? position < held.position && !backwards : held && sent < held.sent
    if (rewound) entry.projectionFloor = (entry.projectionChangedAt ?? 0) > sent ? entry.projectionHintPosition : position
    // A session hint can change ETA/lead without changing the node revision.
    // A read must cover the batch hint (or, without a batch, the newest hint).
    // A newer hint need not prevent progress on the one this read already covers.
    const floor = projectionFloor ?? entry.projectionFloor
    const predatesHint = !rewound && sent < (entry.projectionChangedAt ?? 0) && !(position !== undefined && floor !== undefined && position >= floor)
    if (full && order === 0 && (olderProjection || predatesHint)) return this.visible(entry) ? entry.row : null
    // Older than a revision the store already knows (an event, a write): dropped.
    // The first copy of a node is kept even so; the view reads it again.
    const behind = !!entry.latest && compareRevision(revision, entry.revision) < 0
    if (!early && order >= 0 && !behind) {
      // At least as new as the newest copy: this read confirms it.
      const unversioned = sent >= entry.touched && entry.readAt < entry.touched
      entry.readAt = Math.max(entry.readAt, sent)
      // The same revision from a list page refreshes the projections a node
      // read does not carry; older copies are dropped.
      if (order > 0 || full || !entry.full || unversioned) {
        const incoming = make()
        const staleCount = full && sent < entry.countChangedAt && !!entry.latest
        const copy = staleCount ? frozen({ ...incoming, children_count: entry.latest!.children_count }) : incoming
        const previous = entry.latest
        if (order > 0 && compareRevision(revision, entry.revision) > 0) { entry.revision = revision; entry.touched = ++this.clock }
        entry.latest = copy
        if (full) entry.projectionRead = { position, sent, landed: ++this.clock }
        entry.full = full || (order === 0 && entry.full)
        // A list page replaces the count, so earlier local child deltas no
        // longer describe its baseline. Node reads only carry the previous
        // projection and must keep the dedupe shared by panel and Outline.
        if (full && !staleCount) entry.children.clear()
        if (copy.assignee?.name && copy.assignee.name !== 'Someone') this.names.set(copy.assignee.id, copy.assignee.name)
        if (full && copy.parent?.kind_slug && copy.parent.key) this.learnParent(copy.parent)
        // The same revision with the list projections (the parent chip a node
        // read could only guess): the display object showing it takes them now.
        if (order === 0 && entry.row && entry.shown === previous && entry.pins === 0) this.assign(entry, copy)
      }
    }
    if (!entry.row && entry.latest && this.showable(entry)) {
      entry.row = reactive(clone(entry.latest)) as ListItem
      entry.shown = entry.latest
    } else if (show) this.show(id)
    this.trim()
    return this.visible(entry) ? entry.row : null
  }
  // The display object shows a copy from after the node's last deletion (or
  // an editor pins it: the editor decides what it shows).
  private visible(entry: Entry): boolean {
    return !!entry.row && !entry.tomb && (entry.pins > 0 || !entry.floor || compareRevision(entry.shown?.updated_at, entry.floor) > 0)
  }
  // A read found the node gone (404, or no longer readable).
  gone(id: string, sent = this.clock) {
    const entry = this.entry(id)
    // Something newer arrived after the read was sent (a restore): not this
    // read's to judge. Nor is one sent before a gap (the resync reads again).
    if (entry.touched > sent || this.gapAt > sent) return
    this.bury(entry, null, sent)
  }
  private bury(entry: Entry, revision: string | null, at: number) {
    if (entry.tomb && (!revision || (entry.tomb.revision && compareRevision(revision, entry.tomb.revision) <= 0))) return
    // Older than a revision the store knows (a restore after it): not a deletion any more.
    if (revision && entry.revision && compareRevision(revision, entry.revision) < 0) return
    entry.tomb = { revision, at: Math.max(at, this.clock) }
    if (revision) { entry.revision = revision; entry.floor = revision }
    entry.touched = ++this.clock
    // A copy held off by a tombstone without revision that is newer than
    // this deletion: the node was restored after it.
    const held = entry.held
    if (!revision || !held) return
    entry.held = null
    if (compareRevision(held.copy.updated_at, revision) <= 0) return
    entry.tomb = null
    this.take(held.copy.id, held.sent, held.full, () => held.copy, held.copy.updated_at, false, false)
  }

  // ---------- Events ----------
  // A live event: the store learns the revision (and a deletion or a
  // restore) before anything reads the node.
  note(change: RowChange): News {
    const entry = this.entry(change.id)
    const order = change.revision && entry.revision ? compareRevision(change.revision, entry.revision) : 1
    if (order < 0) return 'older'
    // Import reparenting, like derivation, can retain updated_at. Its hint
    // must invalidate reads and record causal news even at that revision.
    const sameRevisionChange = change.type === 'status_autopilot.derived' || change.type === 'import.parent_changed'
    if (sameRevisionChange || change.fields?.some(field => field === 'eta' || field === 'lead_worker')) {
      entry.projectionChangedAt = ++this.clock
      entry.projectionHintPosition = change.eventId && change.eventId > 0 ? change.eventId : undefined
      if (change.eventId && change.eventId > 0) entry.projectionFloor = Math.max(entry.projectionFloor ?? 0, change.eventId)
    }
    if (change.change === 'deleted') {
      const known = !!entry.tomb && order === 0
      this.bury(entry, change.revision, this.clock + 1)
      return known ? 'known' : 'newer'
    }
    if (order === 0 && !entry.tomb && !sameRevisionChange) return 'known'
    if (entry.tomb) {
      // Only a restore ends a deletion: a creation (a restore reads as one),
      // or a revision newer than the tombstone's. One without revision (a
      // read found the node gone) cannot tell: a revision newer than any the
      // store knows reads as a restore, and the views read the node again.
      const newer = entry.tomb.revision ? compareRevision(change.revision, entry.tomb.revision) > 0 : order > 0
      const restored = (change.change === 'created' && (!entry.floor || compareRevision(change.revision, entry.floor) > 0)) || (!!change.revision && newer)
      if (!restored) return 'older'
      entry.tomb = null
      entry.held = null
    }
    if (change.revision) entry.revision = change.revision
    entry.touched = ++this.clock
    return 'newer'
  }

  // ---------- This tab's writes ----------
  // A node write this tab made (a save, a move, a creation, a bulk item): the
  // exact revision is its own, and the answer is the newest copy.
  wrote(node: WorkNode, sent = this.clock): ListItem | null {
    if (!node?.id || !node.updated_at) return null
    this.remember(node.id, node.updated_at)
    const entry = this.entries.get(node.id)
    return entry?.latest || entry?.row ? this.adoptNode(node, sent, { show: true }) : null
  }
  // A delete this tab made. revision: the Aeon-Revision it answered with;
  // without one (an older server) nothing can prove the event is this tab's.
  deleted(id: string, revision: string | null, sent = this.clock) {
    const entry = this.entry(id)
    if (revision) { this.remember(id, revision); this.bury(entry, revision, sent); return }
    if (entry.touched <= sent) this.bury(entry, null, sent)
  }
  isOwn(id: string, revision: string | null | undefined): boolean {
    return !!revision && !!this.entries.get(id)?.own.some(known => compareRevision(known, revision) === 0)
  }
  private remember(id: string, revision: string) {
    const entry = this.entry(id)
    if (!entry.own.some(known => compareRevision(known, revision) === 0)) entry.own = [...entry.own.slice(-(OWN_PER_NODE - 1)), revision]
  }

  // ---------- Display ----------
  row(id: string): ListItem | undefined { return this.entries.get(id)?.row ?? undefined }
  // A callback may place only a display copy above the deletion floor. row()
  // also retains the old object for views waiting behind the updates pill.
  visibleRow(id: string): ListItem | undefined {
    const entry = this.entries.get(id)
    return entry && this.visible(entry) ? entry.row! : undefined
  }
  // The server copy the display object shows (values and revision of one copy).
  shown(id: string): ListItem | undefined { return this.entries.get(id)?.shown ?? undefined }
  latest(id: string): ListItem | undefined { return this.entries.get(id)?.latest ?? undefined }
  // The display object takes the newest copy, unless an editor pins it or
  // the node is deleted (it keeps what it showed when it went).
  // True when it changed to a newer revision.
  show(id: string): boolean {
    const entry = this.entries.get(id)
    if (!entry?.row || !entry.latest || !this.showable(entry) || entry.pins > 0 || entry.tomb || entry.shown === entry.latest) return false
    const newer = compareRevision(entry.latest.updated_at, entry.shown?.updated_at) > 0
    this.assign(entry, entry.latest)
    return newer
  }
  // Back to a server copy after an optimistic change: the newest when
  // nothing pins the row, otherwise the one it showed.
  reshow(id: string) {
    const entry = this.entries.get(id)
    if (!entry?.row) return
    if (entry.pins === 0 && this.showable(entry)) this.assign(entry, entry.latest!)
    else if (entry.shown) this.assign(entry, entry.shown)
  }
  // A newer copy than the one shown waits (a held row, a pinned editor).
  waiting(id: string): boolean {
    const entry = this.entries.get(id)
    return !!entry?.latest && !!entry.shown && compareRevision(entry.latest.updated_at, entry.shown.updated_at) > 0
  }
  // The parent chip and the epic a list groups by, which a writer knows
  // better than its answer carries (after a move). They hold for one
  // revision and one parent: a copy older than the revision never takes
  // them, nor one with another parent (a newer move landed first), nor a
  // newer list page, which carries its own.
  amend(id: string, revision: string, projection: Partial<Pick<ListItem, 'parent' | 'epic'>>) {
    if (projection.parent) this.learnParent(projection.parent)
    const entry = this.entries.get(id)
    if (!entry?.latest) return
    const fits = (copy: ListItem | null, full: boolean) => {
      if (!copy) return false
      const order = compareRevision(copy.updated_at, revision)
      if (order < 0 || (order > 0 && full)) return false
      return !('parent' in projection) || copy.parent_id === (projection.parent?.id ?? null)
    }
    const showing = entry.shown === entry.latest
    if (fits(entry.latest, entry.full)) entry.latest = frozen({ ...entry.latest, ...projection })
    const shown = showing ? entry.latest : fits(entry.shown, false) ? frozen({ ...entry.shown!, ...projection }) : entry.shown
    if (shown === entry.shown) return
    entry.shown = shown
    if (entry.row) Object.assign(entry.row, clone(projection))
  }
  // A child this tab added under a node (present) or moved away from it:
  // the node's children count follows once per child, whichever view says
  // so first. A list page read later carries the server's count.
  child(parentId: string, childId: string, present: boolean) {
    const entry = this.entries.get(parentId)
    if (!entry?.latest || entry.children.get(childId) === present) return
    entry.countChangedAt = ++this.clock
    entry.children.delete(childId)
    entry.children.set(childId, present)
    if (entry.children.size > CHILDREN_PER_NODE) entry.children.delete(entry.children.keys().next().value!)
    const count = (copy: ListItem) => frozen({ ...copy, children_count: Math.max(0, (copy.children_count ?? 0) + (present ? 1 : -1)) })
    const showing = entry.shown === entry.latest
    entry.latest = count(entry.latest)
    entry.shown = showing ? entry.latest : entry.shown && count(entry.shown)
    if (entry.row && entry.shown) entry.row.children_count = entry.shown.children_count
  }
  // A change a view shows before the server answers (a status choice): the
  // display object takes it; reshow goes back to a server copy.
  optimistic(id: string, values: Partial<Pick<ListItem, 'state' | 'fields'>>) {
    const row = this.entries.get(id)?.row
    if (row) Object.assign(row, clone(values))
  }
  // The newest copy may show: it is not from before a deletion (a restore
  // shows only once a copy after it arrived), nor older than what shows (an
  // editor's own save shows before a newer copy by someone else arrived).
  private showable(entry: Entry): boolean {
    return !!entry.latest && !(entry.floor && compareRevision(entry.latest.updated_at, entry.floor) <= 0)
      && compareRevision(entry.latest.updated_at, entry.shown?.updated_at) >= 0
  }
  private assign(entry: Entry, copy: ListItem) {
    const row = entry.row!
    for (const key of OPTIONAL) if (!(key in copy)) delete row[key]
    Object.assign(row, clone(copy))
    entry.shown = copy
  }

  // Aggregates depend on descendants that may not be loaded. Conservatively
  // invalidate visible parents in the affected project, including former leaves
  // and both sides of a move; null means the source project is unknown.
  // Never guess an incomplete ancestry chain.
  aggregateParents(projectIds: Set<string> | null, includeLeaves = false): ListItem[] {
    const kept = new Set<string>()
    for (const ids of this.holders) for (const id of ids()) kept.add(id)
    return [...kept].flatMap(id => {
      const entry = this.entries.get(id), row = entry?.row
      return row && !entry?.tomb && row.project && (projectIds === null || projectIds.has(row.project.id))
        && (row.estimate?.is_parent || row.children_count > 0 || includeLeaves && ['work', 'ticket', 'task', 'epic'].includes(row.kind_slug)) ? [row] : []
    })
  }

  // ---------- State ----------
  revision(id: string): string | null { return this.entries.get(id)?.revision ?? null }
  // The store knows a newer revision than this one.
  newer(id: string, revision: string | null | undefined): boolean { return compareRevision(this.entries.get(id)?.revision, revision) > 0 }
  isDeleted(id: string): boolean { return !!this.entries.get(id)?.tomb }
  // The newest copy was read after the last gap (nothing it could have
  // missed) and is from after the node's last deletion.
  current(id: string): boolean {
    const entry = this.entries.get(id)
    return !!entry?.latest && !entry.tomb && entry.readAt > this.gapAt && this.showable(entry)
  }
  // By default, confirm the newest hint. A batch may instead confirm its own
  // floor to apply progress without consuming newer queued hints.
  projectionFloor(id: string): number | undefined { return this.entries.get(id)?.projectionFloor }
  projectionsCurrent(id: string, projectionFloor?: number): boolean {
    const entry = this.entries.get(id)
    if (!entry?.projectionChangedAt) return true
    const read = entry.projectionRead
    const floor = projectionFloor ?? entry.projectionFloor
    return !!read && (read.sent > entry.projectionChangedAt || (read.position !== undefined && floor !== undefined && read.position >= floor))
  }

  // ---------- Editors ----------
  // Editing starts: the row is pinned (nothing moves under the editor) and
  // the editor keeps the copy it shows as its base.
  edit(id: string): EditorSession | null {
    const entry = this.entries.get(id)
    const base = entry?.shown ?? entry?.latest
    if (!entry || !base) return null
    entry.pins++
    return new EditorSession(this, id, base)
  }
  // An editor adopts the newest copy: shown under it, while it stays pinned.
  promote(id: string): ListItem | null {
    const entry = this.entries.get(id)
    if (!entry?.row || !entry.latest) return null
    if (entry.shown !== entry.latest && this.showable(entry)) this.assign(entry, entry.latest)
    return compareRevision(entry.latest.updated_at, entry.shown?.updated_at) >= 0 ? entry.latest : entry.shown
  }
  // An editor's own save: the copy at that revision shows under it (the
  // newest copy when it is that one, else the answer over the editor's base).
  showWrite(id: string, node: WorkNode, base: ListItem): ListItem {
    const entry = this.entries.get(id)
    if (!entry?.row) return base
    const copy = entry.latest && compareRevision(entry.latest.updated_at, node.updated_at) === 0
      ? entry.latest
      : frozen(fromNode(base, clone(node), key => this.names.get(key), key => this.parents.get(key), key => this.kinds.get(key)))
    if (compareRevision(copy.updated_at, entry.shown?.updated_at) >= 0) this.assign(entry, copy)
    return copy
  }
  unpin(id: string): boolean {
    const entry = this.entries.get(id)
    if (!entry) return false
    entry.pins = Math.max(0, entry.pins - 1)
    return this.show(id)
  }
  pinned(id: string): boolean { return (this.entries.get(id)?.pins ?? 0) > 0 }

  // ---------- Names ----------
  // Kind labels belong to the kind id, so a converted node never keeps its old label.
  learnKinds(kinds: readonly (Pick<Kind, 'id' | 'slug' | 'label'> & Partial<Pick<Kind, 'field_schema'>>)[]) {
    for (const kind of kinds) this.kinds.set(kind.id, { slug: kind.slug, label: kind.label, field_schema: kind.field_schema ?? this.kinds.get(kind.id)?.field_schema })
  }
  kindSchema(id: string | undefined): Record<string, unknown> | undefined { return id ? this.kinds.get(id)?.field_schema : undefined }
  learnName(id: string, name: string) { if (id && name) this.names.set(id, name) }
  learnParent(parent: ListParent) { if (parent.id) this.parents.set(parent.id, { id: parent.id, key: parent.key, title: parent.title, kind_slug: parent.kind_slug }) }
  name(id: string): string | undefined { return this.names.get(id) }

  // ---------- Bounds ----------
  // Views name the nodes they show or wait on; past the limit the store
  // forgets the oldest others, never a held or pinned one, and tombstones last.
  hold(ids: () => Iterable<string>): () => void {
    this.holders.add(ids)
    return () => { this.holders.delete(ids) }
  }
  private trim() {
    if (this.entries.size <= LIMIT) return
    const kept = new Set<string>()
    for (const ids of this.holders) for (const id of ids()) kept.add(id)
    for (const tombs of [false, true]) {
      for (const [id, entry] of this.entries) {
        if (this.entries.size <= LIMIT * 0.9) return
        if (!kept.has(id) && entry.pins === 0 && !!entry.tomb === tombs) this.entries.delete(id)
      }
    }
  }
  clear() { this.entries.clear(); this.names.clear(); this.kinds.clear(); this.parents.clear(); this.holders.clear(); this.clock = 0; this.gapAt = 0 }

  private entry(id: string): Entry {
    let entry = this.entries.get(id)
    if (!entry) {
      entry = { row: null, shown: null, latest: null, full: false, revision: null, tomb: null, floor: null, held: null, own: [], children: new Map(), countChangedAt: 0, pins: 0, touched: 0, readAt: 0 }
      this.entries.set(id, entry)
    }
    return entry
  }
}

// The tab's store.
export const rowStore = new RowStore()
