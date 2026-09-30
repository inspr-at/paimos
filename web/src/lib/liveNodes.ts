// SPDX-License-Identifier: AGPL-3.0-only
// The live node stream (AEON-326): one per tab, fed by the tenant event
// stream in live mode (/api/events/stream?after=latest). Events name the
// node, its project, the changed attributes and the new revision, never
// values: the row store learns the revision (or the deletion) at once, and
// this refetches a node a view shows through the normal API (one request
// however many views show it). After a gap it cannot bridge (the first
// connection, a restart past a long backlog) it asks every view to refetch
// what it shows. What is known about a node lives in the row store only.
import { APIError, getNode, type WorkNode } from './api'
import { compareRevision, type ChangeKind } from './liveUpdates'
import { rowStore, type RowStore } from './rowStore'

export interface NodeChange {
  eventId: number; type: string; actorId: string
  id: string; projectId: string | null; change: ChangeKind; fields: string[]; revision: string | null
}

// What a view tells the store. shows() names the nodes the store should
// refetch on a change; changed() gets the node (null: gone or no longer
// readable) for those, and undefined for every other change.
export interface LiveView {
  shows(id: string): boolean
  changed(change: NodeChange, node: WorkNode | null | undefined): void
  // After a gap: refetch what you show. 'initial' on the first connection
  // (anything loaded before it may have missed a change), 'gap' later.
  resync?(reason: 'initial' | 'gap'): void
  // The stream resumed without a gap: retry reads that failed meanwhile.
  resumed?(): void
}

export type LiveState = 'off' | 'connecting' | 'live' | 'reconnecting'

interface SourceLike {
  readyState: number
  onerror: ((this: EventSource, ev: Event) => unknown) | null
  addEventListener(type: string, listener: (event: MessageEvent) => void): void
  close(): void
}

export const NODE_EVENTS = ['node.created', 'node.updated', 'node.moved', 'node.project_moved', 'node.deleted', 'node.bulk_changed'] as const
const CLOSED = 2

// The node_changes of one stream event, in client shape.
export function parseNodeChanges(data: string): NodeChange[] {
  let event: { id?: unknown; type?: unknown; actor_principal_id?: unknown; node_changes?: unknown }
  try { event = JSON.parse(data) } catch { return [] }
  if (typeof event.id !== 'number' || !Array.isArray(event.node_changes)) return []
  const out: NodeChange[] = []
  for (const raw of event.node_changes as Record<string, unknown>[]) {
    if (!raw || typeof raw.id !== 'string' || !['created', 'updated', 'deleted'].includes(raw.change as string)) continue
    out.push({
      eventId: event.id, type: String(event.type ?? ''), actorId: String(event.actor_principal_id ?? ''),
      id: raw.id, projectId: typeof raw.project_id === 'string' ? raw.project_id : null, change: raw.change as ChangeKind,
      fields: Array.isArray(raw.fields) ? raw.fields.filter((field): field is string => typeof field === 'string') : [],
      revision: typeof raw.revision === 'string' ? raw.revision : null,
    })
  }
  return out
}

// Two changes to one node, the older first: the fields of both, the newer
// revision. A deletion ends what came before, a restore after it reads as
// created again, and a later update keeps a new node new.
export function mergeChanges(a: NodeChange, b: NodeChange): NodeChange {
  return {
    ...b, change: b.change === 'updated' && a.change !== 'deleted' ? a.change : b.change,
    fields: [...new Set([...a.fields, ...b.fields])],
    revision: compareRevision(a.revision, b.revision) > 0 ? a.revision : b.revision,
    projectId: b.projectId ?? a.projectId,
    // Someone else's change among them is not the viewer's own.
    actorId: a.actorId === b.actorId ? b.actorId : '',
  }
}

async function fetchLive(id: string): Promise<WorkNode | null> {
  try { return await getNode(id) }
  catch (error) {
    if (error instanceof APIError && [403, 404, 410].includes(error.status)) return null
    throw error
  }
}

export interface LiveOptions {
  url?: string
  // Null when this environment has no EventSource: the store then stays off.
  open?: (url: string) => SourceLike | null
  fetchNode?: (id: string) => Promise<WorkNode | null>
  // How long the stream stays open after the last view leaves (switching tickets).
  graceMs?: number
  // Waits before reopening a stream the browser gave up on.
  retryMs?: readonly number[]
  // Waits before reading a node again after a failed read.
  refetchMs?: readonly number[]
  // The causal row store the stream and its views share (the tab's by default).
  rows?: RowStore
}

export class LiveNodeStore {
  state: LiveState = 'off'
  readonly rows: RowStore
  private views = new Set<LiveView>()
  private source: SourceLike | null = null
  private lastEventId: number | null = null
  private connectedOnce = false
  private closeTimer: ReturnType<typeof setTimeout> | undefined
  private retryTimer: ReturnType<typeof setTimeout> | undefined
  private retries = 0
  private fetching = new Map<string, Promise<void>>()
  // Shown nodes with a change not read yet (a read running or failed).
  private dirty = new Map<string, { change: NodeChange; attempts: number }>()
  private retryTimers = new Map<string, ReturnType<typeof setTimeout>>()
  private stateListeners = new Set<(state: LiveState) => void>()
  private readonly options: Required<Omit<LiveOptions, 'rows'>>

  constructor(options: LiveOptions = {}) {
    const { rows, ...rest } = options
    this.rows = rows ?? rowStore
    this.options = {
      url: '/api/events/stream',
      open: url => typeof EventSource === 'undefined' ? null : new EventSource(url) as unknown as SourceLike,
      fetchNode: fetchLive,
      graceMs: 30_000,
      retryMs: [2_000, 5_000, 15_000, 30_000],
      refetchMs: [1_000, 3_000, 10_000, 30_000],
      ...rest,
    }
  }

  // ---------- Views ----------
  subscribe(view: LiveView): () => void {
    this.views.add(view)
    clearTimeout(this.closeTimer)
    if (!this.source && this.retryTimer === undefined) this.connect()
    return () => {
      if (!this.views.delete(view) || this.views.size) return
      clearTimeout(this.closeTimer)
      this.closeTimer = setTimeout(() => { if (!this.views.size) this.disconnect() }, this.options.graceMs)
    }
  }
  onState(listener: (state: LiveState) => void): () => void {
    this.stateListeners.add(listener)
    return () => this.stateListeners.delete(listener)
  }

  // ---------- Stream ----------
  private setState(state: LiveState) {
    if (this.state === state) return
    this.state = state
    for (const listener of this.stateListeners) listener(state)
  }
  private connect() {
    clearTimeout(this.retryTimer); this.retryTimer = undefined
    // A reopened stream resumes after the last event it saw; the first starts now.
    const after = this.lastEventId === null ? 'latest' : String(this.lastEventId)
    let source: SourceLike | null
    try { source = this.options.open(`${this.options.url}?after=${after}`) }
    catch { this.scheduleRetry(); return }
    if (!source) return
    this.source = source
    this.setState(this.connectedOnce ? 'reconnecting' : 'connecting')
    source.addEventListener('stream.ready', event => this.ready(event))
    for (const name of NODE_EVENTS) source.addEventListener(name, event => this.receive(event))
    source.onerror = () => {
      if (this.source !== source) return
      if (source.readyState === CLOSED) { source.close(); this.source = null; this.scheduleRetry() }
      else this.setState('reconnecting')
    }
  }
  private scheduleRetry() {
    this.setState('reconnecting')
    if (!this.views.size) { this.setState('off'); return }
    const wait = this.options.retryMs[Math.min(this.retries, this.options.retryMs.length - 1)]
    this.retries++
    this.retryTimer = setTimeout(() => { this.retryTimer = undefined; if (this.views.size && !this.source) this.connect() }, wait)
  }
  private disconnect() {
    clearTimeout(this.retryTimer); this.retryTimer = undefined
    this.source?.close(); this.source = null
    // A later stream starts fresh: views load again anyway when they open.
    this.lastEventId = null; this.connectedOnce = false; this.retries = 0
    this.forgetDirty()
    this.setState('off')
  }
  private ready(event: MessageEvent) {
    let data: { after?: unknown; resumed?: unknown }
    try { data = JSON.parse(event.data) } catch { return }
    if (typeof data.after !== 'number') return
    const first = !this.connectedOnce
    // The browser resumes after the last event it received, which may be one
    // this store does not listen to: at or past the last node event is whole.
    const continued = data.resumed === true && this.lastEventId !== null && data.after >= this.lastEventId
    this.lastEventId = data.after
    this.connectedOnce = true; this.retries = 0
    this.setState('live')
    // Nothing was missed, but a read that failed meanwhile is tried again now.
    if (continued) { this.readDirty(); for (const view of [...this.views]) view.resumed?.(); return }
    // Anything read before this point may have missed a change: the row store
    // stops trusting it, and the views read what they show again.
    this.rows.gap()
    this.forgetDirty()
    for (const view of [...this.views]) view.resync?.(first ? 'initial' : 'gap')
  }
  private receive(event: MessageEvent) {
    const id = Number(event.lastEventId)
    if (Number.isFinite(id) && id > 0) this.lastEventId = Math.max(this.lastEventId ?? 0, id)
    for (const change of parseNodeChanges(event.data)) this.apply(change)
  }

  // ---------- Changes ----------
  // One change, from the stream or a test. The row store learns it first.
  // Views that show the node get it refetched (or the store's copy when that
  // is at least as new); every other view learns of the change without a node.
  apply(change: NodeChange) {
    // Older than what the store knows (a replay after a reconnect): the
    // views have that newer version or will get it with its own event.
    if (this.rows.note(change) === 'older') return
    const showing = this.showing(change.id)
    for (const view of [...this.views]) if (!showing.includes(view)) view.changed(change, undefined)
    if (!showing.length) { this.settled(change.id); return }
    if (change.change === 'deleted') { this.settled(change.id); for (const view of showing) view.changed(change, null); return }
    // Changes not read yet add up: the views get every field and kind at once.
    const prior = this.dirty.get(change.id)
    this.dirty.set(change.id, { change: prior ? mergeChanges(prior.change, change) : change, attempts: 0 })
    clearTimeout(this.retryTimers.get(change.id)); this.retryTimers.delete(change.id)
    void this.read(change.id)
  }
  private showing(id: string) { return [...this.views].filter(view => view.shows(id)) }
  private settled(id: string) {
    this.dirty.delete(id)
    clearTimeout(this.retryTimers.get(id)); this.retryTimers.delete(id)
  }
  // Refetch a dirty node through the normal API, one read per node at a
  // time. The row store keeps the answer only when it is at least as new as
  // what it knows; an answer that news after the request made stale (a newer
  // revision, a deletion, a restore) is not handed on: the node is read once
  // more, and after a deletion not at all. A failed read keeps the node dirty
  // and tries again later.
  private read(id: string): Promise<void> {
    const running = this.fetching.get(id)
    if (running) return running
    const run = this.readLoop(id).finally(() => { if (this.fetching.get(id) === run) this.fetching.delete(id) })
    this.fetching.set(id, run)
    return run
  }
  private async readLoop(id: string) {
    for (;;) {
      const dirty = this.dirty.get(id)
      if (!dirty) return
      const showing = this.showing(id)
      if (!showing.length) { this.settled(id); return }
      if (this.rows.isDeleted(id)) { this.settled(id); return }
      const cached = this.rows.latest(id)
      if (cached && compareRevision(cached.updated_at, dirty.change.revision) >= 0 && !this.rows.newer(id, cached.updated_at)) {
        this.settled(id)
        for (const view of showing) view.changed(dirty.change, cached)
        return
      }
      const sent = this.rows.mark()
      let node: WorkNode | null
      try { node = await this.options.fetchNode(id) }
      catch { this.retry(id); return }
      // Sent before a gap: the answer may predate a change the stream missed,
      // so the store does not take it. The resync reads what views show, and
      // a change noted after the gap reads the node again.
      if (this.rows.gapSince(sent)) continue
      // News after the request was sent (not this answer's own): the answer
      // may speak for an older state.
      const news = this.rows.touchedSince(id, sent)
      if (node) this.rows.adoptNode(node, sent)
      else this.rows.gone(id, sent)
      if (news && (!node || this.rows.isDeleted(id) || this.rows.newer(id, node.updated_at))) continue
      const change = this.dirty.get(id)?.change
      if (!change) return
      this.settled(id)
      const copy = node && !this.rows.isDeleted(id) ? this.rows.latest(id) ?? null : null
      for (const view of this.showing(id)) view.changed(change, copy)
      return
    }
  }
  // A failed read tries again after a growing wait, a few times; then the
  // node waits for its next change or a resumed stream.
  private retry(id: string) {
    const dirty = this.dirty.get(id)
    const waits = this.options.refetchMs
    if (!dirty || dirty.attempts >= waits.length) return
    const wait = waits[dirty.attempts++]
    clearTimeout(this.retryTimers.get(id))
    this.retryTimers.set(id, setTimeout(() => { this.retryTimers.delete(id); void this.read(id) }, wait))
  }
  // After a stream resumed: read every node still dirty now.
  private readDirty() {
    for (const [id, dirty] of this.dirty) {
      dirty.attempts = 0
      clearTimeout(this.retryTimers.get(id)); this.retryTimers.delete(id)
      void this.read(id)
    }
  }
  private forgetDirty() {
    for (const timer of this.retryTimers.values()) clearTimeout(timer)
    this.retryTimers.clear(); this.dirty.clear()
  }
  // A view can ask for any node (a list checking whether a new node matches).
  async fetch(id: string): Promise<WorkNode | null> {
    const sent = this.rows.mark()
    const node = await this.options.fetchNode(id)
    if (node) this.rows.adoptNode(node, sent)
    else this.rows.gone(id, sent)
    return node
  }
}

// The tab's store.
export const liveNodes = new LiveNodeStore()
