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
//    the node back, then or after a restore; only a newer revision does.
// 3. This tab's writes are known by the exact revision the server answered
//    with (a delete's comes in its Aeon-Revision header). Only those events
//    are this tab's own; the node id alone never proves it.
// 4. One display object per node. Views show it; the store assigns copies to
//    it, never older than what it shows. An editor pins it and keeps its own
//    base: one copy, its revision and its values. Adopting a newer version
//    rebases the editor onto the store's copy for the same id.
import { reactive, toRaw } from 'vue'
import type { ListItem, WorkNode } from './api.ts'
import { compareRevision } from './liveUpdates.ts'

// The change an event names (liveNodes' NodeChange, the part the store reads).
export interface RowChange { id: string; change: 'created' | 'updated' | 'deleted'; revision: string | null }
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
  // Revisions this tab's writes produced.
  own: string[]
  pins: number
  // Clock of the newest news (a newer revision, a tombstone, a restore).
  touched: number
  // Clock when the newest read that confirmed latest was sent.
  readAt: number
}

// List attributes a list page may leave out (an older server): a newer page
// without them clears them.
const OPTIONAL = ['epic', 'eta', 'lead_worker', 'estimate'] as const
const OWN_PER_NODE = 8
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
// epic, lead worker) that a node read does not carry.
export function fromNode(previous: ListItem | null, node: WorkNode, name: (id: string) => string | undefined = () => undefined): ListItem {
  const fields = node.fields ?? {}
  const assigneeId = typeof fields.assignee === 'string' && fields.assignee ? fields.assignee : null
  const base: ListItem = previous ?? { ...node, kind_slug: '', kind_label: '', priority: null, assignee: null, parent: null, children_count: 0, project: null }
  return {
    ...base, ...node,
    priority: typeof fields.priority === 'string' && fields.priority ? fields.priority : null,
    assignee: assigneeId ? (previous?.assignee?.id === assigneeId ? previous.assignee : { id: assigneeId, name: name(assigneeId) ?? 'Someone' }) : null,
  }
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
  private holders = new Set<() => Iterable<string>>()

  // ---------- Clock ----------
  // A request is about to be sent: its answer is judged against news after this.
  mark(): number { return ++this.clock }
  // The stream could not bridge a gap: what was read before may have missed a change.
  gap() { this.gapAt = ++this.clock }
  gapSince(mark: number): boolean { return this.gapAt > mark }
  // Something newer than a request sent at mark is known about this node.
  touchedSince(id: string, mark: number): boolean { return (this.entries.get(id)?.touched ?? 0) > mark }

  // ---------- Reads ----------
  // A copy read from the server (a list page, a create answer in list shape).
  // Returns the node's display object, or null while a tombstone at or after
  // this copy hides it. show: the display object takes the newest copy now
  // (unless an editor pins it); otherwise views decide when. full: false for
  // a copy with projections the reader could not fill.
  adopt(copy: ListItem, sent = this.clock, options: { show?: boolean; full?: boolean } = {}): ListItem | null {
    return this.take(copy.id, sent, options.full ?? true, () => frozen(copy), copy.updated_at, !!copy.deleted_at, options.show ?? false)
  }
  // A node read (GET, a save answer): merged over the newest copy.
  adoptNode(node: WorkNode, sent = this.clock, options: { show?: boolean } = {}): ListItem | null {
    return this.take(node.id, sent, false, () => {
      const entry = this.entries.get(node.id)
      return frozen(fromNode(entry?.latest ?? entry?.row ?? null, clone(node), id => this.names.get(id)))
    }, node.updated_at, !!node.deleted_at, options.show ?? false)
  }
  private take(id: string, sent: number, full: boolean, make: () => ListItem, revision: string, deleted: boolean, show: boolean): ListItem | null {
    const entry = this.entry(id)
    if (deleted) { this.bury(entry, revision, sent); return null }
    // A copy from before a deletion never brings the node back, nor shows
    // after a restore.
    if (entry.floor && compareRevision(revision, entry.floor) <= 0) return entry.tomb ? null : entry.row
    if (entry.tomb) {
      // Without a revision, only a read sent after the deletion was known can.
      if (!entry.tomb.revision && sent <= entry.tomb.at) return null
      entry.tomb = null
      entry.touched = ++this.clock
    }
    const order = entry.latest ? compareRevision(revision, entry.latest.updated_at) : 1
    // Older than a revision the store already knows (an event, a write): dropped.
    // The first copy of a node is kept even so; the view reads it again.
    const behind = !!entry.latest && compareRevision(revision, entry.revision) < 0
    if (order >= 0 && !behind) {
      // At least as new as the newest copy: this read confirms it.
      entry.readAt = Math.max(entry.readAt, sent)
      // The same revision from a list page refreshes the projections a node
      // read does not carry; older copies are dropped.
      if (order > 0 || full || !entry.full) {
        const copy = make()
        if (order > 0 && compareRevision(revision, entry.revision) > 0) { entry.revision = revision; entry.touched = ++this.clock }
        entry.latest = copy
        entry.full = full || (order === 0 && entry.full)
        if (copy.assignee?.name && copy.assignee.name !== 'Someone') this.names.set(copy.assignee.id, copy.assignee.name)
      }
    }
    if (!entry.row && entry.latest) {
      entry.row = reactive(clone(entry.latest)) as ListItem
      entry.shown = entry.latest
    } else if (show) this.show(id)
    this.trim()
    return entry.row
  }
  // A read found the node gone (404, or no longer readable).
  gone(id: string, sent = this.clock) {
    const entry = this.entry(id)
    // Something newer arrived after the read was sent (a restore): not this read's to judge.
    if (entry.touched > sent) return
    this.bury(entry, null, sent)
  }
  private bury(entry: Entry, revision: string | null, at: number) {
    if (entry.tomb && (!revision || (entry.tomb.revision && compareRevision(revision, entry.tomb.revision) <= 0))) return
    // Older than a revision the store knows (a restore after it): not a deletion any more.
    if (revision && entry.revision && compareRevision(revision, entry.revision) < 0) return
    entry.tomb = { revision, at: Math.max(at, this.clock) }
    if (revision) { entry.revision = revision; entry.floor = revision }
    entry.touched = ++this.clock
  }

  // ---------- Events ----------
  // A live event: the store learns the revision (and a deletion or a
  // restore) before anything reads the node.
  note(change: RowChange): News {
    const entry = this.entry(change.id)
    const order = change.revision && entry.revision ? compareRevision(change.revision, entry.revision) : 1
    if (order < 0) return 'older'
    if (change.change === 'deleted') {
      const known = !!entry.tomb && order === 0
      this.bury(entry, change.revision, this.clock + 1)
      return known ? 'known' : 'newer'
    }
    if (order === 0 && !entry.tomb) return 'known'
    if (entry.tomb) {
      // Only a restore ends a deletion: a creation (a restore reads as one),
      // or a revision newer than the tombstone's.
      const restored = (change.change === 'created' && (!entry.floor || compareRevision(change.revision, entry.floor) > 0)) || (!!change.revision && !!entry.tomb.revision && compareRevision(change.revision, entry.tomb.revision) > 0)
      if (!restored) return 'older'
      entry.tomb = null
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
  // The server copy the display object shows (values and revision of one copy).
  shown(id: string): ListItem | undefined { return this.entries.get(id)?.shown ?? undefined }
  latest(id: string): ListItem | undefined { return this.entries.get(id)?.latest ?? undefined }
  // The display object takes the newest copy, unless an editor pins it.
  // True when it changed to a newer revision.
  show(id: string): boolean {
    const entry = this.entries.get(id)
    if (!entry?.row || !entry.latest || entry.pins > 0 || entry.shown === entry.latest) return false
    const newer = compareRevision(entry.latest.updated_at, entry.shown?.updated_at) > 0
    this.assign(entry, entry.latest)
    return newer
  }
  // Back to a server copy after an optimistic change: the newest when
  // nothing pins the row, otherwise the one it showed.
  reshow(id: string) {
    const entry = this.entries.get(id)
    if (!entry?.row) return
    if (entry.pins === 0 && entry.latest) this.assign(entry, entry.latest)
    else if (entry.shown) this.assign(entry, entry.shown)
  }
  // A newer copy than the one shown waits (a held row, a pinned editor).
  waiting(id: string): boolean {
    const entry = this.entries.get(id)
    return !!entry?.latest && !!entry.shown && compareRevision(entry.latest.updated_at, entry.shown.updated_at) > 0
  }
  // Attributes a writer knows better than its answer carries (the parent
  // chip after a move, a new child): they are not bound to a revision, so
  // every copy and the row take them.
  amend(id: string, projection: Partial<Pick<ListItem, 'parent' | 'epic' | 'assignee' | 'children_count'>>) {
    const entry = this.entries.get(id)
    if (!entry?.latest) return
    const showing = entry.shown === entry.latest
    entry.latest = frozen({ ...entry.latest, ...projection })
    entry.shown = showing ? entry.latest : entry.shown && frozen({ ...entry.shown, ...projection })
    if (entry.row) Object.assign(entry.row, clone(projection))
    if (projection.assignee?.name) this.names.set(projection.assignee.id, projection.assignee.name)
  }
  private assign(entry: Entry, copy: ListItem) {
    const row = entry.row!
    for (const key of OPTIONAL) if (!(key in copy)) delete row[key]
    Object.assign(row, clone(copy))
    entry.shown = copy
  }

  // ---------- State ----------
  revision(id: string): string | null { return this.entries.get(id)?.revision ?? null }
  // The store knows a newer revision than this one.
  newer(id: string, revision: string | null | undefined): boolean { return compareRevision(this.entries.get(id)?.revision, revision) > 0 }
  isDeleted(id: string): boolean { return !!this.entries.get(id)?.tomb }
  // The newest copy was read after the last gap: nothing it could have missed.
  current(id: string): boolean {
    const entry = this.entries.get(id)
    return !!entry?.latest && !entry.tomb && entry.readAt > this.gapAt
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
    if (entry.shown !== entry.latest) this.assign(entry, entry.latest)
    return entry.latest
  }
  // An editor's own save: the copy at that revision shows under it (the
  // newest copy when it is that one, else the answer over the editor's base).
  showWrite(id: string, node: WorkNode, base: ListItem): ListItem {
    const entry = this.entries.get(id)
    if (!entry?.row) return base
    const copy = entry.latest && compareRevision(entry.latest.updated_at, node.updated_at) === 0
      ? entry.latest
      : frozen(fromNode(base, clone(node), key => this.names.get(key)))
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
  learnName(id: string, name: string) { if (id && name) this.names.set(id, name) }
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
  clear() { this.entries.clear(); this.names.clear(); this.holders.clear(); this.clock = 0; this.gapAt = 0 }

  private entry(id: string): Entry {
    let entry = this.entries.get(id)
    if (!entry) {
      entry = { row: null, shown: null, latest: null, full: false, revision: null, tomb: null, floor: null, own: [], pins: 0, touched: 0, readAt: 0 }
      this.entries.set(id, entry)
    }
    return entry
  }
}

// The tab's store.
export const rowStore = new RowStore()
