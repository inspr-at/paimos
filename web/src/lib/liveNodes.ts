// SPDX-License-Identifier: AGPL-3.0-only
// The live node store (AEON-326): one per tab, keyed by node id, fed by the
// tenant event stream in live mode (/api/events/stream?after=latest). Events
// name the node, its project, the changed attributes and the new revision,
// never values: the store refetches a node a view shows through the normal
// API (one request however many views show it) and hands the result to the
// views. After a gap it cannot bridge (the first connection, a restart past a
// long backlog) it asks every view to refetch what it shows.
import { APIError, getNode, type WorkNode } from './api'
import { compareRevision, type ChangeKind } from './liveUpdates'

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
}

export interface LiveEntry { revision: string | null; projectId: string | null; deleted: boolean; node: WorkNode | null }
export type LiveState = 'off' | 'connecting' | 'live' | 'reconnecting'

interface SourceLike {
  readyState: number
  onerror: ((this: EventSource, ev: Event) => unknown) | null
  addEventListener(type: string, listener: (event: MessageEvent) => void): void
  close(): void
}

export const NODE_EVENTS = ['node.created', 'node.updated', 'node.moved', 'node.project_moved', 'node.deleted', 'node.bulk_changed'] as const
const CLOSED = 2
const CACHE_LIMIT = 500

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
}

export class LiveNodeStore {
  state: LiveState = 'off'
  private entries = new Map<string, LiveEntry>()
  private views = new Set<LiveView>()
  private source: SourceLike | null = null
  private lastEventId: number | null = null
  private connectedOnce = false
  private closeTimer: ReturnType<typeof setTimeout> | undefined
  private retryTimer: ReturnType<typeof setTimeout> | undefined
  private retries = 0
  private fetching = new Map<string, Promise<void>>()
  private refetchAgain = new Map<string, NodeChange>()
  private stateListeners = new Set<(state: LiveState) => void>()
  private readonly options: Required<LiveOptions>

  constructor(options: LiveOptions = {}) {
    this.options = {
      url: '/api/events/stream',
      open: url => typeof EventSource === 'undefined' ? null : new EventSource(url) as unknown as SourceLike,
      fetchNode: fetchLive,
      graceMs: 30_000,
      retryMs: [2_000, 5_000, 15_000, 30_000],
      ...options,
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

  // ---------- Entries ----------
  get(id: string): LiveEntry | undefined { return this.entries.get(id) }
  // Nodes a view loaded or saved: the store keeps the newest version, so an
  // event for a revision the view already has needs no refetch.
  put(nodes: Iterable<WorkNode>) {
    for (const node of nodes) {
      const entry = this.entries.get(node.id)
      if (entry?.node && compareRevision(node.updated_at, entry.node.updated_at) < 0) continue
      this.remember(node.id, {
        revision: entry && compareRevision(entry.revision, node.updated_at) > 0 ? entry.revision : node.updated_at,
        projectId: entry?.projectId ?? null, deleted: !!node.deleted_at, node,
      })
    }
  }
  private remember(id: string, entry: LiveEntry) {
    this.entries.delete(id)
    this.entries.set(id, entry)
    if (this.entries.size <= CACHE_LIMIT) return
    for (const [key] of this.entries) {
      if (![...this.views].some(view => view.shows(key))) { this.entries.delete(key); if (this.entries.size <= CACHE_LIMIT) return }
    }
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
    if (continued) return
    // What the views show may predate this stream: nodes they cached too.
    for (const entry of this.entries.values()) entry.node = null
    for (const view of [...this.views]) view.resync?.(first ? 'initial' : 'gap')
  }
  private receive(event: MessageEvent) {
    const id = Number(event.lastEventId)
    if (Number.isFinite(id) && id > 0) this.lastEventId = Math.max(this.lastEventId ?? 0, id)
    for (const change of parseNodeChanges(event.data)) this.apply(change)
  }

  // ---------- Changes ----------
  // One change, from the stream or a test. Views that show the node get it
  // refetched (or its cached version when that is at least as new); every
  // other view learns of the change without a node.
  apply(change: NodeChange) {
    const entry = this.entries.get(change.id)
    // Older than what the store already saw (a replay after a reconnect): the
    // views have that newer version or will get it with its own event.
    if (entry && change.revision !== null && compareRevision(change.revision, entry.revision) < 0) return
    this.remember(change.id, {
      revision: change.revision ?? entry?.revision ?? null, projectId: change.projectId ?? entry?.projectId ?? null,
      deleted: change.change === 'deleted', node: change.change === 'deleted' ? null : entry?.node ?? null,
    })
    const showing = [...this.views].filter(view => view.shows(change.id))
    for (const view of [...this.views]) if (!showing.includes(view)) view.changed(change, undefined)
    if (!showing.length) return
    if (change.change === 'deleted') { for (const view of showing) view.changed(change, null); return }
    const cached = this.entries.get(change.id)?.node
    if (cached && compareRevision(cached.updated_at, change.revision) >= 0) { for (const view of showing) view.changed(change, cached); return }
    void this.refetch(change)
  }
  // Refetch through the normal API; changes that arrive meanwhile coalesce
  // into one more request.
  private refetch(change: NodeChange): Promise<void> {
    const running = this.fetching.get(change.id)
    if (running) { this.refetchAgain.set(change.id, change); return running }
    const run = (async () => {
      let node: WorkNode | null
      try { node = await this.options.fetchNode(change.id) }
      catch { return } // A failed read leaves the view as it was; the next change or resync catches up.
      finally { this.fetching.delete(change.id) }
      const entry = this.entries.get(change.id)
      if (node) this.put([node])
      else if (entry) this.remember(change.id, { ...entry, deleted: true, node: null })
      for (const view of [...this.views]) if (view.shows(change.id)) view.changed(change, node)
    })()
    this.fetching.set(change.id, run)
    return run.then(() => {
      const again = this.refetchAgain.get(change.id)
      if (!again) return
      this.refetchAgain.delete(change.id)
      const cached = this.entries.get(change.id)?.node
      if (cached && compareRevision(cached.updated_at, again.revision) >= 0) return
      return this.refetch(again)
    })
  }
  // A view can ask for any node (a list checking whether a new node matches).
  async fetch(id: string): Promise<WorkNode | null> {
    const node = await this.options.fetchNode(id)
    if (node) this.put([node])
    return node
  }
}

// The tab's store.
export const liveNodes = new LiveNodeStore()
