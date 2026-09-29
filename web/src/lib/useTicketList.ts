// SPDX-License-Identifier: AGPL-3.0-only
import { reactive, ref, type Ref } from 'vue'
import { APIError, bulkChange, getNode, listNodes, undoEvent, updateNode, type BulkChange, type BulkResult, type Facets, type ListItem } from './api'
import { activeDimensions, apiParams, DIMENSION_BY_KEY, LIST_FACETS, rowTags, WORK_KINDS, type Dimension, type EpicOption, type ListFilters } from './ticketList'
import { askDoneGate } from './doneGateAsk'
import { benefitGateError, completionFields, needsBenefitPrompt } from './doneGate'
import { toast } from './toast'
import { normaliseState, statusMeta } from './work'

const FACETS = LIST_FACETS
const facetOf = (dimension: Dimension) => DIMENSION_BY_KEY.get(dimension)!.facet
function message(error: unknown) { return error instanceof Error ? error.message : 'Something went wrong' }

// Loads one project's ticket list page by page (cursor paging, 200 rows),
// with facet counts. A dimension that is itself filtered gets its counts
// from a second, one-row request without that filter, so its menu still
// shows what else could be chosen.
export function useTicketList(projectId: Ref<string | null>, filters: Ref<ListFilters>) {
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
  // Counts asked for when a menu opens (labels, cost units, releases), per query.
  const extraFacets = ref<Record<string, Record<string, number>>>({})
  let generation = 0

  function learn(items: ListItem[]) {
    for (const item of items) {
      if (item.assignee) names.set(item.assignee.id, item.assignee.name)
      for (const tag of rowTags(item)) if (tag.color && !colors.has(tag.name.toLowerCase())) colors.set(tag.name.toLowerCase(), tag.color)
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
      const page = await listNodes(apiParams(within, current, { omit: dimension, facets: [facet], limit: 1 }))
      return [dimension, page.facets?.[facet] ?? {}] as const
    })
    try {
      const page = await listNodes(apiParams(within, current, { facets: FACETS, limit: pageSize }))
      if (request !== generation) return
      rows.value = page.items
      cursor.value = page.next_cursor
      facets.value = page.facets ?? {}
      learn(page.items)
      loadedOnce.value = true
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
      const page = await listNodes(apiParams(within, filters.value, { facets: FACETS, cursor: cursor.value, limit: pageSize }))
      if (request !== generation) return
      const seen = new Set(rows.value.map(row => row.id))
      rows.value = [...rows.value, ...page.items.filter(item => !seen.has(item.id))]
      cursor.value = page.next_cursor
      learn(page.items)
    } catch (e) {
      if (request === generation) moreError.value = message(e)
    } finally {
      if (request === generation) loadingMore.value = false
    }
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
    const run = listNodes(apiParams(within, filters.value, { facets: [facet], limit: 1 }))
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
  // A ticket entering completion is asked for its benefit before the row moves,
  // so cancelling leaves the list as it was.
  async function setStatus(row: ListItem, state: string, options: { undo?: boolean; fields?: Record<string, unknown> } = {}): Promise<boolean> {
    const target = rows.value.find(item => item.id === row.id) ?? row
    const before = { state: target.state, updated_at: target.updated_at, fields: target.fields }
    if (normaliseState(before.state) === normaliseState(state)) return false
    let fields = options.fields
    if (!fields && needsBenefitPrompt(target, state)) {
      const text = await askDoneGate({ key: target.key, title: target.title, state, fields: target.fields ?? {} })
      if (!text) return false
      fields = completionFields(target.fields, text)
    }
    target.state = state
    if (fields) target.fields = fields
    shiftFacet(before.state, state)
    try {
      const saved = await updateNode(target.id, fields ? { state, fields } : { state }, { ifUnmodifiedSince: before.updated_at })
      Object.assign(target, { state: saved.state, fields: saved.fields, updated_at: saved.updated_at })
      if (!options.undo) {
        toast(`${target.key} is now ${statusMeta(saved.state).label}`, { action: { label: 'Undo', run: () => void setStatus(target, before.state, { undo: true }) } })
      }
      return true
    } catch (e) {
      if (e instanceof APIError && e.status === 412) {
        try {
          const latest = await getNode(target.id)
          shiftFacet(state, latest.state)
          Object.assign(target, { title: latest.title, body: latest.body, fields: latest.fields, state: latest.state, updated_at: latest.updated_at })
        } catch {
          shiftFacet(state, before.state)
          Object.assign(target, before)
        }
        toast(`${target.key} was changed elsewhere, so your status change was not saved. The latest version is shown.`, { tone: 'error' })
        return false
      }
      shiftFacet(state, before.state)
      Object.assign(target, before)
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
  // reloads so rows that no longer match the filters leave.
  async function applyBulk(change: BulkChange): Promise<BulkResult> {
    const result = await bulkChange(change)
    const projectRef = rows.value[0]?.project
    for (const node of result.items) {
      const row = rows.value.find(item => item.id === node.id)
      if (!row) continue
      const assignee = typeof node.fields.assignee === 'string' ? node.fields.assignee : null
      const priority = typeof node.fields.priority === 'string' ? node.fields.priority : null
      let parent = row.parent
      if (node.parent_id !== row.parent_id) {
        const epic = epics.value.find(e => e.id === node.parent_id)
        parent = epic ? { id: epic.id, key: epic.key, title: epic.title, kind_slug: 'epic' } : projectRef && node.parent_id === projectRef.id ? { ...projectRef, kind_slug: 'project' } : null
        row.epic = epic ? { id: epic.id, key: epic.key, title: epic.title } : null
      }
      Object.assign(row, {
        state: node.state, fields: node.fields, parent_id: node.parent_id, updated_at: node.updated_at, parent, priority,
        assignee: assignee ? { id: assignee, name: names.get(assignee) ?? 'Someone' } : null,
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
    rows.value = [item, ...rows.value]
    const kind = facets.value.kind
    if (kind) kind[item.kind_slug] = (kind[item.kind_slug] ?? 0) + 1
    const state = facets.value.state
    if (state) state[item.state] = (state[item.state] ?? 0) + 1
  }
  function removeRow(id: string) {
    const row = rows.value.find(item => item.id === id)
    if (!row) return
    rows.value = rows.value.filter(item => item.id !== id)
    const kind = facets.value.kind
    if (kind?.[row.kind_slug]) kind[row.kind_slug]--
    const state = facets.value.state
    if (state?.[row.state]) state[row.state]--
  }

  return { rows, cursor, loading, loadingMore, error, moreError, facets, names, colors, loadedOnce, load, loadMore, loadAll, counts, requestFacet, facetCounts, epics, loadEpics, resolveNames, setStatus, invalidate, insertRow, removeRow, applyBulk, undoBulk }
}
