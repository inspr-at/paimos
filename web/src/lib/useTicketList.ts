// SPDX-License-Identifier: AGPL-3.0-only
import { reactive, ref, shallowRef, type Ref } from 'vue'
import { APIError, bulkChange, getNode, listNodes, undoEvent, updateNode, type BulkChange, type BulkResult, type Facets, type ListItem, type ListPage, type ListQuery } from './api'
import { rowStore } from './rowStore'
import type { ListRead } from './useLiveList'
import { activeDimensions, apiParams, DIMENSION_BY_KEY, LIST_FACETS, rowTags, WORK_KINDS, type Dimension, type EpicOption, type ListFilters } from './ticketList'
import { askDoneGate } from './doneGateAsk'
import { benefitGateError, benefitRetryFields, completionFields, needsBenefitPrompt } from './doneGate'
import { toast } from './toast'
import { normaliseState, statusMeta } from './work'

const FACETS = LIST_FACETS
const facetOf = (dimension: Dimension) => DIMENSION_BY_KEY.get(dimension)!.facet
function message(error: unknown) { return error instanceof Error ? error.message : 'Something went wrong' }

// Loads one project's ticket list page by page (cursor paging, 200 rows),
// with facet counts. A dimension that is itself filtered gets its counts
// from a second, one-row request without that filter, so its menu still
// shows what else could be chosen.
// review: a change that met someone else's newer version offers to show it.
export function useTicketList(projectId: Ref<string | null>, filters: Ref<ListFilters>, listOptions: { review?: (row: ListItem) => void; fetchList?: (query: ListQuery) => Promise<ListPage> } = {}) {
  const fetchList = listOptions.fetchList ?? listNodes
  const rows = ref<ListItem[]>([])
  const cursor = ref<string | null>(null)
  const loading = ref(false)
  const loadingMore = ref(false)
  const error = ref('')
  const moreError = ref('')
  const facets = ref<Facets>({})
  const dimensionFacets = ref<Partial<Record<Dimension, Record<string, number>>>>({})
  const names = reactive(new Map<string, string>())
  // Label colours seen on loaded rows (tag facets carry names only).
  const colors = reactive(new Map<string, string>())
  const loadedOnce = ref(false)
  // The latest completed read (a load or the next page): a live list starts
  // over with a load, and looks again at rows read behind newer news.
  const reads = shallowRef<ListRead | null>(null)
  // The last row of the last page as read: the next page starts after it.
  const edge = shallowRef<ListItem | null>(null)
  // Counts asked for when a menu opens (labels, cost units, releases), per query.
  const extraFacets = ref<Record<string, Record<string, number>>>({})
  let generation = 0

  function learn(items: ListItem[]) {
    for (const item of items) {
      if (item.assignee) names.set(item.assignee.id, item.assignee.name)
      for (const tag of rowTags(item)) if (tag.color && !colors.has(tag.name.toLowerCase())) colors.set(tag.name.toLowerCase(), tag.color)
    }
  }
  // A page's rows as the row store has them: the one object per node, never
  // older than what the store knows, and none it knows deleted since.
  // behind: rows this page read before newer news arrived.
  function take(items: ListItem[], sent: number): { rows: ListItem[]; behind: string[] } {
    const out: ListItem[] = [], behind: string[] = []
    for (const item of items) {
      const row = rowStore.adopt(item, sent, { show: true })
      if (!row) continue
      if (rowStore.newer(item.id, item.updated_at)) behind.push(item.id)
      out.push(row)
    }
    return { rows: out, behind }
  }

  // A page read before a gap may miss a change the stream did not deliver (a
  // deletion, say): like every other read, it is read again before any of
  // its rows can show. stale: a newer load replaced this one (no page); what
  // it found still goes to the row store.
  async function trusted(read: () => Promise<ListPage>, stale: () => boolean): Promise<{ page: ListPage | null; sent: number }> {
    for (;;) {
      const sent = rowStore.mark()
      const page = await read()
      const doubtful = rowStore.gapSince(sent)
      if (stale()) { if (!doubtful) for (const item of page.items) rowStore.adopt(item, sent); return { page: null, sent } }
      if (!doubtful) return { page, sent }
    }
  }

  // pageSize 1 fetches counts and facets only (the Outline builds its own rows then).
  let pageSize = 200
  async function load(options: { pageSize?: number } = {}) {
    const within = projectId.value
    if (!within) return
    pageSize = options.pageSize ?? 200
    const request = ++generation
    const current = filters.value
    loading.value = true; loadingMore.value = false; error.value = ''; moreError.value = ''
    extraFacets.value = {}
    const extras = activeDimensions(current).filter(dimension => facetOf(dimension)).map(async dimension => {
      const facet = facetOf(dimension)!
      const page = await fetchList(apiParams(within, current, { omit: dimension, facets: [facet], limit: 1 }))
      return [dimension, page.facets?.[facet] ?? {}] as const
    })
    try {
      const { page, sent } = await trusted(() => fetchList(apiParams(within, current, { facets: FACETS, limit: pageSize })), () => request !== generation)
      if (!page) return
      learn(page.items)
      const read = take(page.items, sent)
      rows.value = read.rows
      cursor.value = page.next_cursor
      edge.value = page.items[page.items.length - 1] ?? null
      facets.value = page.facets ?? {}
      loadedOnce.value = true
      reads.value = { kind: 'load', behind: read.behind, sent }
    } catch (e) {
      if (request === generation) { error.value = message(e); rows.value = []; cursor.value = null }
    } finally {
      if (request === generation) loading.value = false
    }
    const settled = await Promise.allSettled(extras)
    if (request !== generation) return
    dimensionFacets.value = Object.fromEntries(settled.flatMap(result => result.status === 'fulfilled' ? [result.value] : []))
  }

  // One next-page request at a time; callers that need the page await the same promise.
  let moreRequest: Promise<void> | null = null
  function loadMore(): Promise<void> {
    if (loadingMore.value && moreRequest) return moreRequest
    moreRequest = fetchMore().finally(() => { moreRequest = null })
    return moreRequest
  }
  async function fetchMore() {
    const within = projectId.value
    if (!within || !cursor.value || loading.value) return
    const request = generation
    loadingMore.value = true; moreError.value = ''
    try {
      const after = cursor.value
      const { page, sent } = await trusted(() => fetchList(apiParams(within, filters.value, { facets: FACETS, cursor: after, limit: pageSize })), () => request !== generation)
      if (!page) return
      const seen = new Set(rows.value.map(row => row.id))
      learn(page.items)
      const read = take(page.items.filter(item => !seen.has(item.id)), sent)
      rows.value = [...rows.value, ...read.rows]
      cursor.value = page.next_cursor
      edge.value = page.items[page.items.length - 1] ?? edge.value
      reads.value = { kind: 'more', behind: read.behind }
    } catch (e) {
      if (request === generation) moreError.value = message(e)
    } finally {
      if (request === generation) loadingMore.value = false
    }
  }

  // The counts again, without rows: after live updates changed what matches.
  async function refreshCounts() {
    const within = projectId.value
    if (!within || !loadedOnce.value) return
    const request = generation
    try {
      const page = await fetchList(apiParams(within, filters.value, { facets: FACETS, limit: 1 }))
      if (request === generation && page.facets) facets.value = page.facets
    } catch { /* the counts stay as they were */ }
  }

  function counts(dimension: Dimension): Record<string, number> {
    const facet = facetOf(dimension)
    if (!facet) return {}
    return (filters.value[dimension].length ? dimensionFacets.value[dimension] : undefined) ?? facets.value[facet] ?? extraFacets.value[facet] ?? {}
  }
  // Counts that do not come with every page: fetched once per query when needed.
  const asked = new Map<string, Promise<void>>()
  function requestFacet(facet: string): Promise<void> {
    const within = projectId.value
    if (!within || facets.value[facet] || extraFacets.value[facet]) return Promise.resolve()
    const request = generation
    const key = `${request}:${facet}`
    if (asked.has(key)) return asked.get(key)!
    const run = fetchList(apiParams(within, filters.value, { facets: [facet], limit: 1 }))
      .then(page => { if (request === generation) extraFacets.value = { ...extraFacets.value, [facet]: page.facets?.[facet] ?? {} } })
      .catch(() => { /* the menu shows what the loaded rows have */ })
    asked.set(key, run)
    return run
  }
  function facetCounts(facet: string): Record<string, number> {
    return facets.value[facet] ?? extraFacets.value[facet] ?? {}
  }

  // The project's epics, for the Epic filter and the bulk move; loaded once per project.
  const epics = ref<EpicOption[]>([])
  let epicsFor = ''
  let epicsLoad: Promise<void> | null = null
  function loadEpics(): Promise<void> {
    const within = projectId.value
    if (!within) return Promise.resolve()
    if (epicsFor === within && epicsLoad) return epicsLoad
    epicsFor = within
    epicsLoad = listNodes({ within, kind: ['epic'], sort: 'key', limit: 500 })
      .then(page => { if (epicsFor === within) epics.value = page.items.map(item => ({ id: item.id, key: item.key, title: item.title, state: item.state })) })
      .catch(() => { epicsLoad = null })
    return epicsLoad
  }

  async function resolveNames(ids: string[]) {
    const within = projectId.value
    if (!within) return
    await Promise.all(ids.filter(id => id !== 'none' && !names.has(id)).map(async id => {
      try {
        const page = await listNodes({ within, kind: WORK_KINDS, assignee: [id], limit: 1 })
        const person = page.items[0]?.assignee
        if (person) names.set(id, person.name)
      } catch { /* the menu shows "Unknown person" */ }
    }))
  }

  function shiftFacet(from: string, to: string) {
    const state = facets.value.state
    if (!state) return
    if (state[from] !== undefined) state[from] = Math.max(0, state[from] - 1)
    state[to] = (state[to] ?? 0) + 1
  }

  // Optimistic status change: the row updates at once and the server confirms.
  // The PATCH carries the row's updated_at as If-Unmodified-Since; a newer
  // server copy answers 412, nothing is written, and the latest version is shown.
  // Benefit text from the dialog is asked again, still filled in.
  // A ticket entering completion is asked for its benefit before the row moves,
  // so cancelling leaves the list as it was.
  async function setStatus(row: ListItem, state: string, options: { undo?: boolean; fields?: Record<string, unknown> } = {}): Promise<boolean> {
    const target = rowStore.row(row.id) ?? rows.value.find(item => item.id === row.id) ?? row
    // The server copy the row shows: its revision is the precondition, its
    // fields what a completion builds on (never a copy the row has not shown).
    const shown = rowStore.shown(target.id) ?? target
    const before = { state: shown.state, updated_at: shown.updated_at, fields: shown.fields }
    if (normaliseState(target.state) === normaliseState(state)) return false
    let fields = options.fields
    if (!fields && needsBenefitPrompt({ ...target, fields: before.fields }, state)) {
      const text = await askDoneGate({ key: target.key, title: target.title, state, fields: before.fields ?? {} })
      if (!text) return false
      fields = completionFields(before.fields, text)
    }
    // Optimistic: the row shows the choice at once; the store's copy is the fallback.
    rowStore.optimistic(target.id, fields ? { state, fields } : { state })
    shiftFacet(before.state, state)
    const sent = rowStore.mark()
    try {
      const saved = await updateNode(target.id, fields ? { state, fields } : { state }, { ifUnmodifiedSince: before.updated_at })
      // The answer is this tab's write in the row store; the row shows it
      // unless an editor pins it.
      rowStore.wrote(saved, sent)
      rowStore.reshow(target.id)
      if (!options.undo) {
        toast(`${target.key} is now ${statusMeta(saved.state).label}`, { action: { label: 'Undo', run: () => void setStatus(target, before.state, { undo: true }) } })
      }
      return true
    } catch (e) {
      if (e instanceof APIError && e.status === 412) {
        let baseFields = before.fields
        try {
          const sent = rowStore.mark()
          const latest = await getNode(target.id)
          rowStore.adoptNode(latest, sent)
          shiftFacet(state, latest.state)
          baseFields = latest.fields
        } catch {
          shiftFacet(state, before.state)
        }
        rowStore.reshow(target.id)
        toast(`${target.key} was changed elsewhere, so your status change was not saved. The latest version is shown.`, { tone: 'error', ...(listOptions.review ? { action: { label: 'Review', run: () => listOptions.review!(target) } } : {}) })
        // The dialog texts were not written. Ask again with those texts filled in.
        if (fields && !options.undo) {
          const text = await askDoneGate({ key: target.key, title: target.title, state, fields: benefitRetryFields(baseFields, fields) })
          if (!text) return false
          return setStatus(target, state, { ...options, fields: completionFields(target.fields, text) })
        }
        return false
      }
      shiftFacet(state, before.state)
      // Gone meanwhile: the store learns it (a restore after the change was sent keeps it).
      if (e instanceof APIError && (e.status === 404 || e.status === 410)) rowStore.gone(target.id, sent)
      rowStore.reshow(target.id)
      if (!options.fields && benefitGateError(e)) {
        const text = await askDoneGate({ key: target.key, title: target.title, state, fields: before.fields ?? {} })
        if (!text) return false
        return setStatus(target, state, { ...options, fields: completionFields(before.fields, text) })
      }
      if (benefitGateError(e)) {
        toast(`${target.key} keeps its status. Add a 2–4 word pill and a benefit in both languages.`, { tone: 'error' })
        return false
      }
      toast(`${target.key} keeps its status: ${message(e)}`, { tone: 'error', action: { label: 'Retry', run: () => void setStatus(target, state, options) } })
      return false
    }
  }

  // Bulk change: rows update in place from the server's answer; the list then
  // reloads so rows that no longer match the filters leave. Each row sends the
  // version it shows, so one changed meanwhile comes back as a conflict.
  async function applyBulk(change: BulkChange, displayed: ListItem[] = rows.value): Promise<BulkResult> {
    const shown = new Map(displayed.map(row => [row.id, row.updated_at]))
    const since = Object.fromEntries(change.ids.flatMap(id => shown.has(id) ? [[id, shown.get(id)!]] : []))
    for (const [id, name] of names) rowStore.learnName(id, name)
    const parents = new Map(displayed.map(row => [row.id, row.parent_id]))
    // The answers are in the row store (this tab's writes); a move's new
    // parent chip is known here, from the project's epics.
    const result = await bulkChange(Object.keys(since).length ? { ...change, if_unmodified_since: since } : change)
    const projectRef = displayed[0]?.project
    for (const node of result.items) {
      if (!parents.has(node.id) || node.parent_id === parents.get(node.id)) continue
      const epic = epics.value.find(e => e.id === node.parent_id)
      // Only for this answer's revision and parent: a newer move that landed first keeps its chip.
      rowStore.amend(node.id, node.updated_at, {
        parent: epic ? { id: epic.id, key: epic.key, title: epic.title, kind_slug: 'epic' } : projectRef && node.parent_id === projectRef.id ? { ...projectRef, kind_slug: 'project' } : null,
        epic: epic ? { id: epic.id, key: epic.key, title: epic.title } : null,
      })
    }
    return result
  }
  async function undoBulk(eventId: number): Promise<void> {
    await undoEvent(eventId)
  }

  function invalidate() { generation++ }
  // Every page of the current query, up to a cap (the Outline needs the whole match set).
  async function loadAll(cap = 2000): Promise<boolean> {
    const request = generation
    while (cursor.value && rows.value.length < cap && request === generation && !moreError.value) await loadMore()
    return !cursor.value
  }
  // Created and deleted work shows up at once, before the next reload.
  function insertRow(item: ListItem) {
    if (rows.value.some(row => row.id === item.id)) return
    const row = rowStore.adopt(item, undefined, { show: true, full: false })
    if (!row) return
    rows.value = [row, ...rows.value]
    const kind = facets.value.kind
    if (kind) kind[item.kind_slug] = (kind[item.kind_slug] ?? 0) + 1
    const state = facets.value.state
    if (state && !item.estimate?.is_parent) state[item.state] = (state[item.state] ?? 0) + 1
  }
  // Only a row the row store knows deleted leaves (a restore since keeps it).
  function removeRow(id: string) {
    const row = rows.value.find(item => item.id === id)
    if (!row || !rowStore.isDeleted(id)) return
    rows.value = rows.value.filter(item => item.id !== id)
    const kind = facets.value.kind
    if (kind?.[row.kind_slug]) kind[row.kind_slug]--
    const state = facets.value.state
    if (state?.[row.state] && !row.estimate?.is_parent) state[row.state]--
  }

  return { rows, cursor, edge, loading, loadingMore, error, moreError, facets, names, colors, loadedOnce, reads, load, loadMore, loadAll, counts, refreshCounts, requestFacet, facetCounts, epics, loadEpics, resolveNames, setStatus, invalidate, insertRow, removeRow, applyBulk, undoBulk }
}
