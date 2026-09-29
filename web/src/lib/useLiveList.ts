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
// so an edit or a bulk change still meets it as a conflict.
import { computed, onScopeDispose, reactive, ref, watch, type Ref } from 'vue'
import { listNodes, type ListItem, type ListPage, type ListQuery } from './api'
import { liveNodes, mergeChanges, type LiveNodeStore, type LiveView, type NodeChange } from './liveNodes'
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
// Waits before reading changed rows again after a failed read; then the rows
// wait for the next change or a resumed stream.
export const RETRY_MS = [1_000, 3_000, 10_000, 30_000] as const
// A resync reads again at most this much of the list: what a person can have seen.
const RESYNC_ROWS = 200

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

export interface LiveListOptions {
  projectId: Ref<string | null>
  filters: Ref<ListFilters>
  rows: Ref<ListItem[]>
  loading: Ref<boolean>
  // Completed loads: each starts the live state over.
  loads: Ref<number>
  loadedOnce: Ref<boolean>
  // Later pages exist: a new row that sorts after the loaded ones waits for them.
  more: () => boolean
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
  fetchList?: (query: ListQuery) => Promise<ListPage>
  env?: LiveListEnv
}

interface Queued { change: NodeChange; base: Layout | null; own: boolean }
// List item attributes an older answer may leave out.
const OPTIONAL = ['epic', 'eta', 'lead_worker', 'estimate'] as const

export function useLiveList(options: LiveListOptions) {
  const store = options.store ?? liveNodes
  const fetchList = options.fetchList ?? ((query: ListQuery) => listNodes(query))
  const env = options.env ?? browserEnv
  const { rows, filters, projectId } = options

  const pending = reactive(new PendingUpdates()) as unknown as PendingUpdates
  // The values a waiting row is still placed by.
  const held = reactive(new Map<string, Layout>()) as Map<string, Layout>
  // New matching rows, added when the updates apply.
  const incoming = new Map<string, ListItem>()
  // Newer versions of rows the person works with, patched in when the updates apply.
  const patches = new Map<string, ListItem>()
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
  let retryTimer: ReturnType<typeof setTimeout> | undefined
  let failures = 0

  const rowById = (id: string) => rows.value.find(row => row.id === id)
  const mine = (change: NodeChange) => !!change.actorId && change.actorId === options.me()

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
    resumed() { failures = 0; if (queue.size) schedule() },
  }
  function receive(change: NodeChange) {
    const project = projectId.value
    if (!options.active.value || !project) return
    const row = rowById(change.id)
    if (!row && change.projectId !== project) return
    const own = mine(change)
    // The person's own change is already on the row; only a row that waits
    // is looked at again (reopened after it closed, say).
    if (own && !(row && pending.kind(row.id)) && !queue.has(change.id)) return
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
  // One refetch at a time: an older answer never classifies after a newer one.
  let flushing: Promise<boolean> | null = null
  function schedule() {
    if (batchTimer !== undefined) return
    batchTimer = setTimeout(() => {
      batchTimer = undefined
      if (flushing) return
      flushing = flush().catch(() => true)
      void flushing.then(failed => {
        flushing = null
        if (!queue.size) return
        if (!failed) schedule()
        else if (failures < RETRY_MS.length) {
          clearTimeout(retryTimer)
          retryTimer = setTimeout(() => { retryTimer = undefined; schedule() }, RETRY_MS[failures++])
        }
      })
    }, BATCH_MS)
  }
  // A change that is not read yet goes back to the queue, ahead of any newer one.
  function requeue(id: string, queued: Queued) {
    const newer = queue.get(id)
    queue.set(id, newer ? { change: mergeChanges(queued.change, newer.change), base: queued.base, own: queued.own && newer.own } : queued)
  }

  // ---------- Refetch and classify ----------
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
    const outcome: { id: string; kind: Classification; queued: Queued; row?: ListItem; newer: boolean }[] = []
    let failed = false
    for (let i = 0; i < ids.length; i += PAGE) {
      const chunk = ids.slice(i, i + PAGE)
      let matched: Map<string, ListItem>
      let present = new Map<string, ListItem>()
      try {
        const page = await fetchList({ ...apiParams(project, filters.value, { limit: PAGE }), ids: chunk })
        if (run !== generation) return false
        matched = new Map(page.items.map(item => [item.id, item]))
        // Shown rows that no longer match: closed, or gone from the project?
        const missing = chunk.filter(id => !matched.has(id) && rowById(id) && batch.get(id)!.change.change !== 'deleted')
        if (missing.length) {
          const found = await fetchList({ within: project, kind: WORK_KINDS, ids: missing, limit: PAGE })
          if (run !== generation) return false
          present = new Map(found.items.map(item => [item.id, item]))
        }
      } catch {
        // Nothing is known: the rows stay as they are, and are read again later.
        if (run !== generation) return false
        for (const id of chunk) requeue(id, batch.get(id)!)
        failed = true
        continue
      }
      for (const id of chunk) {
        const queued = batch.get(id)!
        const current = matched.get(id) ?? present.get(id) ?? null
        // A change that arrived during the read and is newer than its answer
        // (a deletion, say) makes the answer stale: the row waits for the next read.
        const newer = queue.get(id)
        if (newer) {
          if (!current || newer.change.change === 'deleted' || compareRevision(current.updated_at, newer.change.revision) < 0) { requeue(id, queued); continue }
          queue.delete(id)
          queued.change = mergeChanges(queued.change, newer.change)
          queued.own = queued.own && newer.own
        }
        outcome.push(settle(id, queued, matched.get(id) ?? null, present.get(id) ?? null))
      }
    }
    if (!failed) failures = 0
    announce(outcome.filter(entry => !entry.queued.own))
    if (outcome.some(entry => entry.kind !== 'ignore' && entry.kind !== 'patch' && !entry.queued.own)) markedAt = env.now()
    // Counts follow the rows: waiting updates change them when they apply.
    if (outcome.some(entry => entry.kind === 'patch' && entry.newer)) options.applied?.()
    watchPending()
    return failed
  }

  function settle(id: string, queued: Queued, fresh: ListItem | null, present: ListItem | null) {
    const { change, base, own } = queued
    const row = rowById(id)
    const current = fresh ?? present
    let kind = classifyChange<Layout>({ change, shown: row ? base : null, node: current, matches: node => node === fresh })
    // Moved out to another project reads as no longer matching, not deleted.
    if (kind === 'deleted' && change.fields.includes('project_id') && change.projectId && change.projectId !== projectId.value) kind = 'no_longer_matches'
    if (kind === 'patch' && fresh && base && placeKey(fresh, filters.value) !== placeKey(base, filters.value)) kind = 'moved'
    if (kind === 'new' && fresh && !fitsLoaded(fresh)) kind = 'ignore'
    const newer = !!row && !!current && compareRevision(current.updated_at, base?.updated_at) > 0
    // A row the person works with keeps what they see until the updates apply.
    const holding = !!row && !!options.holds?.(id)
    patches.delete(id)
    if (row && current && compareRevision(current.updated_at, row.updated_at) >= 0) {
      if (!holding) patchRow(row, current)
      else if (compareRevision(current.updated_at, row.updated_at) > 0) {
        patches.set(id, current)
        if (kind === 'patch') kind = 'changed'
      }
    }
    if (kind === 'new' && fresh) incoming.set(id, fresh)
    else incoming.delete(id)
    if (kind === 'patch' || kind === 'ignore') held.delete(id)
    // Past the cap the oldest updates go; the pill then offers a reload.
    for (const dropped of pending.note(id, kind)) { incoming.delete(dropped); patches.delete(dropped) }
    if (row && newer && !own && !holding && (kind === 'patch' || kind === 'moved')) tint(id)
    return { id, kind, queued, row, newer }
  }
  // The row object stays (the panel may show it); its values become the server's.
  function patchRow(row: ListItem, item: ListItem) {
    for (const key of OPTIONAL) if (!(key in item)) delete row[key]
    Object.assign(row, item)
  }
  // A new row joins the loaded ones when it sorts among them; one that sorts
  // after the last loaded row comes with the next page.
  function fitsLoaded(item: ListItem) {
    const list = rows.value
    if (!options.more() || !list.length) return true
    return compareRows(effectiveSort(filters.value))(item, list[list.length - 1]) <= 0
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
    const key = entry.row?.key ?? incoming.get(entry.id)?.key ?? 'A ticket'
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
    const all = pending.take()
    const removing = new Set([...all].filter(([, kind]) => kind === 'closed' || kind === 'no_longer_matches' || kind === 'deleted').map(([id]) => id))
    const run = () => {
      const compare = compareRows(effectiveSort(filters.value))
      let next = rows.value.filter(row => !removing.has(row.id))
      const place = (item: ListItem) => {
        const at = next.findIndex(row => compare(item, row) < 0)
        if (at === -1) { if (!options.more()) next.push(item) }
        else next.splice(at, 0, item)
      }
      for (const [id, kind] of all) {
        held.delete(id)
        // What waited for the person comes in now, unless something newer already has.
        const later = patches.get(id), shown = rowById(id)
        patches.delete(id)
        if (later && shown && compareRevision(later.updated_at, shown.updated_at) > 0) { patchRow(shown, later); if (kind === 'changed' || kind === 'moved') tint(id) }
        if (kind === 'moved') {
          const row = next.find(item => item.id === id)
          if (row) { next = next.filter(item => item.id !== id); place(row) }
        } else if (kind === 'new') {
          const item = incoming.get(id)
          if (item && !next.some(row => row.id === id)) place(item)
        }
        incoming.delete(id)
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
    if (!pending.count || pending.overflow || !options.active.value || options.loading.value) return
    if (autoApplyDelay(blocked()) === 0) apply()
  }
  // The check runs only while updates wait.
  function watchPending() {
    if (pending.count && checkTimer === undefined) checkTimer = setInterval(check, CHECK_MS)
    else if (!pending.count && checkTimer !== undefined) { clearInterval(checkTimer); checkTimer = undefined }
  }

  // ---------- After a gap: read what the person sees again ----------
  function resync() {
    if (!options.active.value || !options.loadedOnce.value) return
    if (options.loading.value) { resyncWanted = true; return }
    void refreshLoaded()
  }
  async function refreshLoaded() {
    const project = projectId.value
    if (!project) return
    const run = generation
    // The first page again, in one request: rows that arrived meanwhile show
    // as new; loaded rows that changed or are missing from it are looked at.
    const loaded = rows.value.slice(0, RESYNC_ROWS)
    let page: ListPage
    try { page = await fetchList(apiParams(project, filters.value, { limit: Math.max(PAGE / 4, Math.min(PAGE, loaded.length)) })) }
    catch { return }
    if (run !== generation) return
    const synthetic = (id: string, item?: ListItem): NodeChange => ({
      eventId: 0, type: 'resync', actorId: '', id, projectId: item?.project?.id ?? project, change: 'updated', fields: [], revision: item?.updated_at ?? null,
    })
    const fresh = new Map(page.items.map(item => [item.id, item]))
    const known = new Set(rows.value.map(row => row.id))
    for (const item of page.items) if (!known.has(item.id)) receive(synthetic(item.id, item))
    for (const row of loaded) {
      const item = fresh.get(row.id)
      if (!item || compareRevision(item.updated_at, row.updated_at) > 0) receive(synthetic(row.id, item))
    }
  }

  // ---------- Lifecycle ----------
  function reset() {
    generation++
    queue.clear()
    clearTimeout(batchTimer); batchTimer = undefined
    clearTimeout(retryTimer); retryTimer = undefined; failures = 0
    pending.clear(); held.clear(); incoming.clear(); patches.clear()
    watchPending()
  }
  // A new load starts over: the rows are the server's again. Changes that
  // arrived while it ran may be newer than what it read, so they are looked
  // at once more, against the new rows.
  watch(options.loads, () => {
    const waiting = [...queue.values()]
    reset()
    for (const { change, own } of waiting) {
      const row = rowById(change.id)
      if (row) held.set(row.id, layoutOf(row))
      queue.set(change.id, { change, own, base: row ? layoutOf(row) : null })
    }
    if (resyncWanted) { resyncWanted = false; void refreshLoaded() }
    else if (queue.size) schedule()
  })
  watch(options.loading, loading => { if (!loading && queue.size) schedule() })
  watch(options.active, (on, was) => {
    if (!on) { reset(); return }
    // Back from the Outline or another tab: the rows may have missed changes.
    if (!was && options.loadedOnce.value) resync()
  })
  onScopeDispose(store.subscribe(view))
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
