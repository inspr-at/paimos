// SPDX-License-Identifier: AGPL-3.0-only
// The project ticket list, live (AEON-326 slice 1b). Changes by others reach
// the list through the tab's live node store; the list refetches the rows
// that changed through its own query (GET /nodes?ids=…, every filter still
// applied, so a row that is absent no longer matches) in batches, not one
// request per node.
//
// Field changes patch the row in place with a brief tint and a polite
// announcement. Structural changes (closed, no longer matches, deleted, moved,
// new) never move a row under the person: the row keeps its place, dimmed and
// labelled, and waits behind the "N updates · Show" pill. Show applies them at
// once; they also apply by themselves when nothing is going on (no selection,
// editor, menu or dialog, the tab visible, 2 s without input). While a row
// waits, the list groups and orders it by the values it was placed with.
//
// A row the person works with (selected, or open in an editor) keeps the
// values and the revision they see: a change by others waits as "Changed",
// so an edit or a bulk change still meets it as a conflict. Show brings it
// in; a row an editor pins takes it once the editor lets go.
//
// Every copy goes through the tab's row store (rowStore.ts): the list reads
// rows from it and asks it to show them, so a copy older than what the store
// knows, or older than a deletion, never reaches the screen. Only this tab's
// own writes (their exact revisions) are skipped as already shown; the same
// person's changes in another tab come in like anyone else's.
import { computed, onScopeDispose, reactive, ref, watch, type Ref } from 'vue'
import { listNodes, type ListItem, type ListPage, type ListQuery } from './api'
import { liveNodes, mergeChanges, type LiveNodeStore, type LiveView, type NodeChange } from './liveNodes'
import { RefreshRetry, RETRY_MS } from './refreshRetry'
import { autoApplyDelay, classifyChange, compareRevision, describeFields, IDLE_MS, PendingUpdates, pillText, type Classification, type Structural } from './liveUpdates'
import { apiParams, compareRows, effectiveSort, rowTags, WORK_KINDS, type ListFilters } from './ticketList'
import { statusMeta } from './work'

// The values a row is placed by: its group and its order in the list.
type Layout = Pick<ListItem, 'id' | 'key' | 'title' | 'state' | 'priority' | 'assignee' | 'kind_slug' | 'fields' | 'parent' | 'parent_id' | 'epic' | 'updated_at'>
function layoutOf(row: ListItem): Layout {
  const { id, key, title, state, priority, assignee, kind_slug, fields, parent, parent_id, epic, updated_at } = row
  return { id, key, title, state, priority, assignee, kind_slug, fields, parent, parent_id, epic, updated_at }
}

// Where a row sits for these filters: its group and the order fields the list
// sorts by. Updated and created times are left out: every change moves
// updated_at, and a field change patches in place rather than jumping.
export function placeKey(row: Layout, filters: ListFilters): string {
  const parts: unknown[] = []
  for (const key of effectiveSort(filters)) {
    switch (key.field) {
      case 'state': parts.push(statusMeta(row.state).key === 'other' ? row.state : statusMeta(row.state).key); break
      case 'priority': parts.push(row.priority ?? 'none'); break
      case 'assignee': parts.push(row.assignee?.id ?? null); break
      case 'title': parts.push(row.title); break
      case 'key': parts.push(row.key); break
      case 'kind': parts.push(row.kind_slug); break
    }
  }
  switch (filters.group) {
    case 'status': parts.push(statusMeta(row.state).key === 'other' ? row.state : statusMeta(row.state).key); break
    case 'assignee': parts.push(row.assignee?.id ?? null); break
    case 'priority': parts.push(row.priority ?? 'none'); break
    case 'type': parts.push(row.kind_slug); break
    case 'epic': parts.push(row.kind_slug === 'epic' ? row.id : row.epic?.id ?? row.parent_id); break
    case 'tag': parts.push(rowTags(row as ListItem).map(tag => tag.name.toLowerCase()).sort()); break
  }
  return JSON.stringify(parts)
}

export const BATCH_MS = 120
export const FLASH_MS = 2000
const CHECK_MS = 400
const PAGE = 200
// The same waits as every other failed refresh (list gap, list resync, open ticket).
export { RETRY_MS }

export interface LiveListBlockers { selected: number; editing: boolean; menuOpen: boolean; dialogOpen: boolean; dragging: boolean }
export interface LiveListEnv {
  now(): number
  hidden(): boolean
  // Calls back on pointer, key, wheel, touch and scroll input; returns a stop.
  listen(input: () => void): () => void
}
const browserEnv: LiveListEnv = {
  now: () => Date.now(),
  hidden: () => typeof document !== 'undefined' && document.visibilityState === 'hidden',
  listen(input) {
    if (typeof window === 'undefined') return () => {}
    const kinds = ['pointerdown', 'keydown', 'wheel', 'touchstart', 'scroll'] as const
    for (const kind of kinds) window.addEventListener(kind, input, { capture: true, passive: true })
    return () => { for (const kind of kinds) window.removeEventListener(kind, input, { capture: true }) }
  },
}

// A completed read of the list: a load (the rows start over) or the next
// page. behind: rows it read that the row store already knew newer news of
// (a save, an event or a panel read landed while it ran): they are looked at again.
// sent: when a load was sent (the row store's clock).
export interface ListRead { kind: 'load' | 'more'; behind: string[]; sent?: number }
// Presence reads establish existence only; they never establish filter membership.
export type LiveReadPurpose = 'matches' | 'presence'

export interface LiveListOptions {
  projectId: Ref<string | null>
  filters: Ref<ListFilters>
  rows: Ref<ListItem[]>
  loading: Ref<boolean>
  // The latest completed read: a load starts the live state over.
  reads: Ref<ListRead | null>
  loadedOnce: Ref<boolean>
  // Later pages exist: a new row that sorts after the loaded ones waits for them.
  more: () => boolean
  // The last row of the last page as it was read: the next page starts after
  // it, so a row that sorts before it belongs to the loaded ones.
  edge?: () => ListItem | null
  // A tree also places rows by their immediate parent, at every depth.
  placement?: (row: Layout) => string
  // Lazy trees discover additions in the levels they have loaded.
  resyncQueries?: () => ListQuery[]
  // Show can release a tree move only after its editor lets go of the row.
  released?: () => void
  // The list is on show (not the Outline, the graph or another tab).
  active: Ref<boolean>
  me: () => string | null
  // The ticket open in the panel says its own changes; the list only tints its row.
  quiet?: (id: string) => boolean
  // The person works with this row (selected, open in an editor, a menu on
  // it): changes by others wait instead of patching it.
  holds?: (id: string) => boolean
  blockers: () => LiveListBlockers
  // Applying removes, adds and moves rows; the page keeps scroll, focus and the cursor around it.
  around?: (removing: Set<string>, apply: () => void) => void
  applied?: () => void
  // Too many updates to apply: the list loads again.
  reload: () => void
  store?: LiveNodeStore
  fetchList?: (query: ListQuery, purpose: LiveReadPurpose) => Promise<ListPage>
  env?: LiveListEnv
}

interface Queued { change: NodeChange; base: Layout | null; own: boolean }

export function useLiveList(options: LiveListOptions) {
  const store = options.store ?? liveNodes
  const nodes = store.rows
  const fetchList = options.fetchList ?? ((query: ListQuery) => listNodes(query))
  const env = options.env ?? browserEnv
  const { rows, filters, projectId } = options

  const pending = reactive(new PendingUpdates()) as unknown as PendingUpdates
  // The values a waiting row is still placed by.
  const held = reactive(new Map<string, Layout>()) as Map<string, Layout>
  // Rows Show brought in while an editor pinned them: they take the newer
  // copy once it lets go, with a tint.
  const afterEditor = new Set<string>()
  // When each waiting update was classified (the row store's clock): news
  // since then makes a removal stale.
  const classified = new Map<string, { at: number; revision: string | null }>()
  const flash = ref(new Set<string>())
  const message = ref('')
  let queue = new Map<string, Queued>()
  let batchTimer: ReturnType<typeof setTimeout> | undefined
  let checkTimer: ReturnType<typeof setInterval> | undefined
  const flashTimers = new Map<string, ReturnType<typeof setTimeout>>()
  let generation = 0
  let lastInput = env.now()
  // The newest structural update counts as activity: a marked row stays in
  // view for the idle time before it goes by itself.
  let markedAt = 0
  let resyncWanted = false

  const rowById = (id: string) => rows.value.find(row => row.id === id)
  // A write this tab made: its exact revision, already on screen.
  const mine = (change: NodeChange) => !!change.actorId && change.actorId === options.me() && nodes.isOwn(change.id, change.revision)
  // A read of a row, as a change of unknown fields (a resync, a load that ran behind).
  const reread = (id: string, item?: ListItem): NodeChange => ({
    eventId: 0, type: 'resync', actorId: '', id, projectId: item?.project?.id ?? projectId.value, change: 'updated', fields: [], revision: item?.updated_at ?? null,
  })

  // ---------- Layout: rows keep their place while an update waits ----------
  function layout(row: ListItem): ListItem {
    const values = held.get(row.id)
    return values ? { ...row, ...values } : row
  }

  // ---------- Changes from the store ----------
  const view: LiveView = {
    // The list refetches through its own query, never one node at a time.
    shows: () => false,
    changed(change) { receive(change) },
    resync() { resync() },
    // Reads that failed while the stream was away are tried again.
    resumed() {
      flushRetry.resume()
      // A pending addition read before the loss cannot join until a read
      // confirms it again, even when the stream resumes its full history.
      for (const id of pending.ids()) if (pending.kind(id) === 'new' && !nodes.current(id)) receive(reread(id))
      if (queue.size) schedule()
      resyncRetry.resume()
    },
  }
  function receive(change: NodeChange) {
    const project = projectId.value
    if (!options.active.value || !project) return
    const row = rowById(change.id)
    if (!row && change.projectId !== project) return
    const own = mine(change)
    // This tab's own change is already on a row the list shows; only a row
    // that waits is looked at again (reopened after it closed, say). A node
    // the list does not show may have moved into it (changed from the panel).
    if (own && row && !pending.kind(row.id) && !queue.has(change.id)) return
    const entry = queue.get(change.id)
    if (entry) {
      entry.change = mergeChanges(entry.change, change)
      entry.own = entry.own && own
    } else {
      // The row's place as the person sees it, before anything patches it.
      const base = row ? held.get(row.id) ?? layoutOf(row) : null
      if (row && !held.has(row.id)) held.set(row.id, base!)
      queue.set(change.id, { change, base, own })
    }
    schedule()
  }
  // One refetch at a time, retried like every other failed refresh. A batch
  // that landed with more still queued waits the usual beat; a failed one
  // waits the shared backoff, then the next resume.
  const flushRetry = new RefreshRetry(async () => {
    const run = generation
    const failed = await flush().catch(() => true)
    if (run !== generation) return false
    if (!failed && queue.size) schedule()
    return failed && queue.size > 0
  })
  function schedule() {
    if (batchTimer !== undefined) return
    batchTimer = setTimeout(() => {
      batchTimer = undefined
      flushRetry.request()
    }, BATCH_MS)
  }
  // A change that is not read yet goes back to the queue, ahead of any newer one.
  function requeue(id: string, queued: Queued) {
    const newer = queue.get(id)
    queue.set(id, newer ? { change: mergeChanges(queued.change, newer.change), base: queued.base, own: queued.own && newer.own } : queued)
  }

  // ---------- Refetch and classify ----------
  // A read the list no longer needs (a load replaced the rows meanwhile)
  // still tells the row store what it found, unless a gap makes it doubtful.
  function keep(items: ListItem[], sent: number) {
    if (!nodes.gapSince(sent)) for (const item of items) nodes.adopt(item, sent)
  }
  // True when a read failed: those rows went back to the queue.
  async function flush(): Promise<boolean> {
    // A load in flight replaces the rows: look once it has landed.
    if (!queue.size || options.loading.value) return false
    const project = projectId.value
    if (!project) { queue.clear(); return false }
    const batch = queue
    queue = new Map()
    const run = generation
    const ids = [...batch.keys()]
    const outcome: { id: string; kind: Classification; queued: Queued; row?: ListItem; newer: boolean; marked?: boolean }[] = []
    let failed = false
    for (let i = 0; i < ids.length; i += PAGE) {
      const chunk = ids.slice(i, i + PAGE)
      const sent = nodes.mark()
      // What the store knew when the query was sent: a later copy that finds
      // a row the query did not return changed after it (a restore, say).
      const knew = new Map(chunk.map(id => [id, nodes.revision(id)]))
      let matched: Map<string, ListItem>
      let present = new Map<string, ListItem>()
      try {
        const page = await fetchList({ ...apiParams(project, filters.value, { limit: PAGE }), ids: chunk }, 'matches')
        if (run !== generation) { keep(page.items, sent); return false }
        // The store has what the query found at once, not after the next read.
        keep(page.items, sent)
        matched = new Map(page.items.map(item => [item.id, item]))
        // Shown rows that no longer match: closed, or gone from the project?
        const missing = chunk.filter(id => !matched.has(id) && rowById(id) && batch.get(id)!.change.change !== 'deleted')
        if (missing.length) {
          const found = await fetchList({ within: project, kind: WORK_KINDS, ids: missing, limit: PAGE }, 'presence')
          if (run !== generation) { keep(found.items, sent); return false }
          present = new Map(found.items.map(item => [item.id, item]))
        }
      } catch {
        // Nothing is known: the rows stay as they are, and are read again later.
        if (run !== generation) return false
        for (const id of chunk) requeue(id, batch.get(id)!)
        failed = true
        continue
      }
      // Sent before a gap: the answer may predate a change the stream missed.
      // Every row in it is read again (the resync that gap asked for reads the rest).
      if (nodes.gapSince(sent)) { for (const id of chunk) requeue(id, batch.get(id)!); continue }
      for (const id of chunk) {
        const queued = batch.get(id)!
        const copy = matched.get(id) ?? present.get(id) ?? null
        // News since the read was sent (a newer revision, a deletion, a
        // restore, this tab's save) makes the answer stale: the row waits for
        // the next read, which a queued change or this requeue asks for. A row
        // the query did not return is judged by that read, not by the later
        // one that found it: news since the first (a restore) makes it stale.
        const overtaken = !matched.has(id) && (nodes.touchedSince(id, sent) || (!!copy && compareRevision(copy.updated_at, knew.get(id)) > 0))
        if (copy) nodes.adopt(copy, sent)
        // A copy the store holds off (a deletion it learned after the read
        // was sent, without knowing when) cannot tell either: read again.
        if (overtaken || (copy && (nodes.newer(id, copy.updated_at) || nodes.isDeleted(id)))) { requeue(id, queued); continue }
        const newer = queue.get(id)
        if (newer) {
          queue.delete(id)
          queued.change = mergeChanges(queued.change, newer.change)
          queued.own = queued.own && newer.own
        }
        outcome.push(settle(id, queued, matched.get(id) ?? null, present.get(id) ?? null))
      }
    }
    announce(outcome.filter(entry => !entry.queued.own))
    if (outcome.some(entry => entry.marked && !entry.queued.own)) markedAt = env.now()
    // Counts follow the rows: waiting updates change them when they apply.
    if (outcome.some(entry => entry.kind === 'patch' && entry.newer)) options.applied?.()
    watchPending()
    return failed
  }

  function settle(id: string, queued: Queued, fresh: ListItem | null, present: ListItem | null) {
    const row = rowById(id)
    // Queued before the row joined the list (a next page, a load): it is
    // judged against the row as the person sees it now.
    if (row && !queued.base) queued.base = held.get(id) ?? layoutOf(row)
    const { change, base } = queued
    // A write of this tab whose answer landed after its event: left to the
    // code that made it, when the list shows the row.
    const own = queued.own || mine(change)
    if (own && !queued.own && row && !pending.kind(id)) {
      queued.own = true
      held.delete(id)
      return { id, kind: 'ignore' as Classification, queued, row, newer: false }
    }
    queued.own = own
    // A tombstone in the store outranks any copy: the node is gone.
    const current = nodes.isDeleted(id) ? null : fresh ?? present
    // A deletion that a newer copy overtook (its restore went missing in a
    // gap) reads as the restore it was.
    const told = change.change === 'deleted' && current && compareRevision(current.updated_at, change.revision) > 0 ? { ...change, change: 'created' as const } : change
    let kind = classifyChange<Layout>({ change: told, shown: row ? base : null, node: current, matches: node => node === fresh })
    // Moved out to another project reads as no longer matching, not deleted.
    if (kind === 'deleted' && change.fields.includes('project_id') && change.projectId && change.projectId !== projectId.value) kind = 'no_longer_matches'
    const placement = options.placement ?? ((item: Layout) => placeKey(item, filters.value))
    if (kind === 'patch' && fresh && base && placement(fresh) !== placement(base)) kind = 'moved'
    if (kind === 'new' && fresh && !fitsLoaded(fresh)) kind = 'ignore'
    const newer = !!row && !!current && compareRevision(current.updated_at, base?.updated_at) > 0
    // A row the person works with, or one an editor pins, keeps what they see
    // until the updates apply.
    const holding = !!row && (!!options.holds?.(id) || nodes.pinned(id))
    if (row && current) {
      if (!holding) nodes.show(id)
      else if (kind === 'patch' && nodes.waiting(id)) kind = 'changed'
    }
    const marked = kind !== 'patch' && kind !== 'ignore'
      && (pending.kind(id) !== kind || classified.get(id)?.revision !== nodes.revision(id))
    if (kind === 'patch' || kind === 'ignore') { held.delete(id); classified.delete(id) }
    else classified.set(id, { at: nodes.mark(), revision: nodes.revision(id) })
    // Past the cap the oldest updates go; the pill then offers a reload.
    pending.note(id, kind)
    if (row && newer && !own && !holding && (kind === 'patch' || kind === 'moved')) tint(id)
    return { id, kind, queued, row, newer, marked }
  }
  // A new row joins the loaded ones when it sorts among them; one that sorts
  // after where the next page starts comes with that page.
  function fitsLoaded(item: ListItem) {
    const list = rows.value
    const edge = options.edge?.() ?? list[list.length - 1]
    if (!options.more() || !edge) return true
    const compare = compareRows(effectiveSort(filters.value))
    // The edge itself, unmoved, is one of the loaded rows (ties sort by id, never equal).
    return compare(item, edge) <= 0 || (item.id === edge.id && compare(edge, item) > 0)
  }

  function tint(id: string) {
    clearTimeout(flashTimers.get(id))
    flash.value = new Set([...flash.value, id])
    flashTimers.set(id, setTimeout(() => {
      flashTimers.delete(id)
      const next = new Set(flash.value); next.delete(id); flash.value = next
    }, FLASH_MS))
  }

  // One polite sentence per batch; the panel speaks for the ticket it shows.
  function announce(entries: { id: string; kind: Classification; queued: Queued; row?: ListItem; newer: boolean }[]) {
    const said = entries.filter(entry => {
      if (entry.kind === 'ignore') return false
      if (entry.kind === 'patch' || entry.kind === 'moved' || entry.kind === 'changed') return entry.newer && !options.quiet?.(entry.id)
      return true
    })
    if (!said.length) return
    const waiting = said.some(entry => entry.kind !== 'patch')
    const tail = waiting ? ' Press U to show updates.' : ''
    if (said.length > 1) { message.value = `${said.length} tickets changed elsewhere.${tail}`; return }
    const [entry] = said
    const key = entry.row?.key ?? nodes.row(entry.id)?.key ?? 'A ticket'
    const words = describeFields(entry.queued.change.fields)
    const sentence: Record<Classification, string> = {
      patch: words ? `${key} was updated elsewhere: ${words}.` : `${key} was updated elsewhere.`,
      moved: words ? `${key} was updated elsewhere: ${words}.` : `${key} was updated elsewhere.`,
      changed: words ? `${key} was updated elsewhere: ${words}.` : `${key} was updated elsewhere.`,
      closed: `${key} was closed elsewhere.`,
      no_longer_matches: `${key} no longer matches this view.`,
      deleted: `${key} was deleted elsewhere.`,
      new: `${key} was added to this view.`,
      ignore: '',
    }
    message.value = sentence[entry.kind] + tail
  }

  // ---------- Applying: Show, the shortcut, or by itself when it is safe ----------
  function apply() {
    if (!pending.count && !pending.overflow) return
    if (pending.overflow) { reset(); options.reload(); return }
    const all: [string, Structural][] = []
    for (const [id, kind] of pending.take()) {
      if (kind === 'new') {
        // A new row comes in only as the store has it now: not deleted, and
        // read after the last gap. One a resync still has to read waits.
        if (nodes.isDeleted(id) || !nodes.row(id)) continue
        if (!nodes.current(id)) { pending.note(id, kind); continue }
      } else if (kind === 'closed' || kind === 'no_longer_matches' || kind === 'deleted') {
        // A row leaves only as the store has it now: none the store has news
        // of since it was classified (a restore, a reopen, a newer revision).
        // Such a row is read again instead.
        if (nodes.touchedSince(id, classified.get(id)?.at ?? 0)) {
          if (rowById(id)) receive(reread(id))
          continue
        }
      }
      classified.delete(id)
      all.push([id, kind])
    }
    if (!all.length) { watchPending(); return }
    const removing = new Set(all.filter(([, kind]) => kind === 'closed' || kind === 'no_longer_matches' || kind === 'deleted').map(([id]) => id))
    const run = () => {
      const compare = compareRows(effectiveSort(filters.value))
      let next = rows.value.filter(row => !removing.has(row.id))
      const place = (item: ListItem) => {
        const at = next.findIndex(row => compare(item, row) < 0)
        if (at === -1) { if (fitsLoaded(item)) next.push(item) }
        else next.splice(at, 0, item)
      }
      for (const [id, kind] of all) {
        held.delete(id)
        // What waited for the person comes in now. A row an editor pins
        // takes it when the editor lets go.
        if (rowById(id) && nodes.waiting(id)) {
          if (nodes.show(id)) { if (kind === 'changed' || kind === 'moved') tint(id) }
          else if (nodes.pinned(id)) afterEditor.add(id)
        }
        if (kind === 'moved') {
          const row = next.find(item => item.id === id)
          if (row) { next = next.filter(item => item.id !== id); place(row) }
        } else if (kind === 'new') {
          nodes.show(id)
          const item = nodes.row(id)
          if (item && !next.some(row => row.id === id)) place(item)
        }
      }
      rows.value = next
    }
    if (options.around) options.around(removing, run)
    else run()
    options.applied?.()
    watchPending()
  }
  const blocked = () => {
    const b = options.blockers()
    return { ...b, scrolling: false, hidden: env.hidden(), lastInputAt: Math.max(lastInput, markedAt), now: env.now() }
  }
  function check() {
    catchUp()
    if (!pending.count || pending.overflow || !options.active.value || options.loading.value) return
    if (autoApplyDelay(blocked()) === 0) apply()
  }
  // A row Show left to an editor takes the newer copy once the editor let go.
  // An editor that saved on top leaves its own version: nothing to tint.
  function catchUp() {
    if (!afterEditor.size) return
    let released = false
    for (const id of afterEditor) {
      if (nodes.pinned(id)) continue
      afterEditor.delete(id)
      released = true
      nodes.show(id)
      const shown = nodes.shown(id)
      if (rowById(id) && shown && !nodes.isOwn(id, shown.updated_at)) tint(id)
    }
    if (released) options.released?.()
    watchPending()
  }
  // The check runs only while updates wait.
  function watchPending() {
    const waiting = pending.count > 0 || afterEditor.size > 0
    if (waiting && checkTimer === undefined) checkTimer = setInterval(check, CHECK_MS)
    else if (!waiting && checkTimer !== undefined) { clearInterval(checkTimer); checkTimer = undefined }
  }

  // ---------- After a gap: read what the person sees again ----------
  // The same retry as a failed row read and the open ticket: again after a
  // growing wait, and at once when the stream resumes, until one succeeds.
  const resyncRetry = new RefreshRetry(() => refreshLoaded())
  function resync() {
    if (!options.active.value || !options.loadedOnce.value) return
    if (options.loading.value) { resyncWanted = true; return }
    resyncRetry.request()
  }
  // True when the read failed.
  async function refreshLoaded(): Promise<boolean> {
    const project = projectId.value
    if (!project) return false
    const run = generation
    // The first page again, in one request: rows that arrived meanwhile show
    // as new. Every loaded row that changed or is not in it (a later page,
    // or gone) is read again through the queue, in batches. So is everything
    // else the list holds: waiting additions and updates, and the queue.
    // (Additions read before the gap cannot come in on Show meanwhile: the
    // store no longer counts them as current.)
    const sent = nodes.mark()
    let page: ListPage
    try {
      const queries = options.resyncQueries?.()
      if (queries) {
        const pages = await Promise.all(queries.map(query => fetchList(query, 'matches')))
        page = { items: pages.flatMap(page => page.items), next_cursor: null }
      } else page = await fetchList(apiParams(project, filters.value, { limit: Math.max(PAGE / 4, Math.min(PAGE, rows.value.length)) }), 'matches')
    }
    catch { return run === generation }
    if (run !== generation) { keep(page.items, sent); return false }
    // Another gap after this read was sent: the resync it asked for reads again.
    if (nodes.gapSince(sent)) return false
    const fresh = new Map(page.items.map(item => [item.id, item]))
    const known = new Set(rows.value.map(row => row.id))
    const seen = new Set<string>()
    for (const item of page.items) {
      seen.add(item.id)
      nodes.adopt(item, sent)
      if (!known.has(item.id)) receive(reread(item.id, item))
    }
    for (const row of rows.value) {
      seen.add(row.id)
      const item = fresh.get(row.id)
      if (!item || nodes.waiting(row.id) || nodes.newer(row.id, item.updated_at)) receive(reread(row.id, item))
    }
    for (const id of [...pending.ids(), ...queue.keys(), ...afterEditor]) {
      if (seen.has(id)) continue
      seen.add(id)
      if (!queue.has(id)) receive(reread(id))
    }
    return false
  }

  // ---------- Lifecycle ----------
  function reset() {
    generation++
    queue.clear()
    clearTimeout(batchTimer); batchTimer = undefined
    // A load reads the list anew: a refresh that failed is not needed any more.
    flushRetry.clear(); resyncRetry.clear()
    pending.clear(); held.clear(); afterEditor.clear(); classified.clear()
    watchPending()
  }
  // A load starts over: the rows are the store's again, and changes that
  // arrived while it ran are looked at once more, with every row the load
  // read behind newer news. The next page looks at its rows the same way.
  watch(options.reads, read => {
    if (!read) return
    if (read.kind === 'load') {
      const waiting = [...queue.values()]
      reset()
      for (const { change, own } of waiting) {
        const row = rowById(change.id)
        if (row) held.set(row.id, layoutOf(row))
        queue.set(change.id, { change, own, base: row ? layoutOf(row) : null })
      }
    }
    for (const id of read.behind) if (rowById(id)) receive(reread(id))
    // A node with news since the load was sent (this tab's own change, say)
    // may belong among the rows it read, or no longer: it is looked at again.
    if (read.kind === 'load' && read.sent !== undefined) for (const id of nodes.changedSince(read.sent)) if (!rowById(id) && !queue.has(id)) receive(reread(id))
    if (read.kind === 'load' && resyncWanted) { resyncWanted = false; resyncRetry.request() }
    else if (queue.size) schedule()
  })
  watch(options.loading, loading => { if (!loading && queue.size) schedule() })
  watch(options.active, (on, was) => {
    if (!on) { reset(); return }
    // Back from the Outline or another tab: the rows may have missed changes.
    if (!was && options.loadedOnce.value) resync()
  })
  // A row this tab removed (its own delete) no longer waits behind the pill.
  watch(() => rows.value, list => {
    const shown = new Set(list.map(row => row.id))
    for (const id of pending.ids()) if (pending.kind(id) !== 'new' && !shown.has(id)) { pending.note(id, 'ignore'); held.delete(id) }
    watchPending()
  })
  onScopeDispose(store.subscribe(view))
  onScopeDispose(nodes.hold(() => [...rows.value.map(row => row.id), ...pending.ids(), ...queue.keys()]))
  onScopeDispose(env.listen(() => { lastInput = env.now() }))
  onScopeDispose(() => {
    reset()
    for (const timer of flashTimers.values()) clearTimeout(timer)
  })

  const pill = computed(() => pillText(pending))
  const labels = computed(() => {
    const out = new Map<string, string>()
    for (const row of rows.value) { const label = pending.label(row.id); if (label && pending.kind(row.id) !== 'new') out.set(row.id, label) }
    return out
  })
  return {
    pending, pill, labels, flash, message, layout, apply,
    // Selected ids deleted meanwhile, for the bulk bar.
    deletedAmong: (ids: Iterable<string>) => pending.deletedAmong(ids),
    // For tests: run the batch and the safe-apply check now.
    flushNow: () => { clearTimeout(batchTimer); batchTimer = undefined; return flush() },
    checkNow: check,
    idleMs: IDLE_MS,
  }
}
export type LiveList = ReturnType<typeof useLiveList>
export type { Structural }
