// SPDX-License-Identifier: AGPL-3.0-only
// The figures of the list view on screen (AEON-1358 on AEON-1368's GET /api/projects/{id}/figures): counted in the
// background after the list paints, cached per scope (search, status filter and saved view; never Hide closed, sort or
// paging), read by the header work bar and the column hovers. A new scope keeps the previous figures on screen,
// dimmed (busy), until its own land; a cached scope lands at once. Responses for a scope that is no longer on screen
// are dropped. A scope that cannot be counted says so; it never shows another scope's figures as its own.
import { computed, onBeforeUnmount, ref, shallowRef, watch, type Ref } from 'vue'
import { APIError, figuresPath, getProjectFigures, type ProjectFigures } from './api'
import { apiParams, filtersFromQuery, type ListFilters } from './ticketList'
import { workFiguresText } from './workFiguresText'

// The server keeps a scope for 30 s too; older entries still land at once and are counted again quietly.
const MAX_AGE = 30_000
const MAX_ENTRIES = 32
const cache = new Map<string, { value: ProjectFigures; at: number }>()
function remember(key: string, value: ProjectFigures) {
  cache.delete(key)
  cache.set(key, { value, at: Date.now() })
  while (cache.size > MAX_ENTRIES) cache.delete(cache.keys().next().value!)
}
export function forgetViewFigures() { cache.clear() }

export interface ViewFiguresOptions {
  // The signed-in identity (tenant/principal): cached figures belong to the person who read them.
  owner: Ref<string>
  projectId: Ref<string | null>
  filters: Ref<ListFilters>
  // False until the project's default saved view is resolved, so the first count is the view that shows.
  ready: Ref<boolean>
  // While the list loads, the count waits for it (then for an idle moment): it never delays the rows.
  listLoading: Ref<boolean>
  // Changes when the project's work changes: the current scope is counted again quietly, without dimming.
  freshness: Ref<unknown>
}

function failureText(error: unknown): string {
  const text = workFiguresText()
  if (error instanceof APIError && error.status === 422) return text.tooMany
  if (error instanceof APIError && error.status === 503) return text.slow
  return text.failed
}

function idle(): Promise<void> {
  return new Promise(resolve => {
    requestAnimationFrame(() => {
      if (typeof requestIdleCallback === 'function') requestIdleCallback(() => resolve(), { timeout: 600 })
      else setTimeout(resolve, 0)
    })
  })
}

export function useViewFigures(options: ViewFiguresOptions) {
  const scoped = (path: string) => `${options.owner.value} ${path}`
  const key = computed(() => {
    const id = options.projectId.value
    return id && options.owner.value && options.ready.value ? scoped(figuresPath(id, apiParams(id, options.filters.value))) : ''
  })
  const wholeKey = computed(() => {
    const id = options.projectId.value
    return id && options.owner.value ? scoped(figuresPath(id, apiParams(id, filtersFromQuery({})))) : ''
  })
  // The figures on screen (the previous scope's while busy), the whole project's (slot widths), and the state.
  const figures = shallowRef<ProjectFigures | null>(null)
  const whole = shallowRef<ProjectFigures | null>(null)
  const busy = ref(false)
  const failure = shallowRef<{ key: string; message: string } | null>(null)
  let shownKey = ''
  let viewRequest: AbortController | null = null
  let wholeRequest: AbortController | null = null
  let disposed = false

  function land(forKey: string, value: ProjectFigures) {
    remember(forKey, value)
    if (forKey === wholeKey.value) whole.value = value
    if (forKey !== key.value) return
    // Label, total, bar and line switch together: one object replaces the previous in one render.
    figures.value = value; shownKey = forKey
    busy.value = false; failure.value = null
  }

  async function waitForList() {
    if (options.listLoading.value) {
      await new Promise<void>(resolve => {
        const timer = setTimeout(() => { stop(); resolve() }, 2_000)
        const stop = watch(options.listLoading, loading => { if (!loading) { clearTimeout(timer); stop(); resolve() } })
      })
    }
    await idle()
  }

  async function count(forKey: string, quiet: boolean) {
    viewRequest?.abort()
    const request = new AbortController()
    viewRequest = request
    if (!quiet) busy.value = true
    await waitForList()
    if (request.signal.aborted || disposed || forKey !== key.value) return
    const id = options.projectId.value!
    try {
      const value = await getProjectFigures(id, apiParams(id, options.filters.value), request.signal)
      if (request.signal.aborted || disposed) return
      land(forKey, value)
      if (!whole.value && forKey !== wholeKey.value) void countWhole()
    } catch (error) {
      if (request.signal.aborted || disposed || (error instanceof DOMException && error.name === 'AbortError')) return
      if (forKey !== key.value) return
      busy.value = false
      // A quiet refresh that fails keeps the scope's last good figures: they are still this scope's.
      if (!quiet || shownKey !== forKey) failure.value = { key: forKey, message: failureText(error) }
    } finally {
      if (viewRequest === request) viewRequest = null
    }
  }

  // The whole project sizes the value slots when the project opens, so a narrower view never widens them.
  async function countWhole() {
    const forKey = wholeKey.value, id = options.projectId.value
    if (!forKey || !id || wholeRequest) return
    const cached = cache.get(forKey)
    if (cached) { whole.value = cached.value; return }
    const request = new AbortController()
    wholeRequest = request
    try {
      const value = await getProjectFigures(id, apiParams(id, filtersFromQuery({})), request.signal)
      if (!request.signal.aborted && !disposed && forKey === wholeKey.value) { remember(forKey, value); whole.value = value }
    } catch { /* the slots keep their defaults */ }
    finally { if (wholeRequest === request) wholeRequest = null }
  }

  // A new project or another person: nothing of the previous one stays on screen or in the cache.
  watch(options.owner, () => cache.clear())
  watch([options.projectId, options.owner], () => {
    viewRequest?.abort(); wholeRequest?.abort(); wholeRequest = null
    figures.value = null; whole.value = null; busy.value = false; failure.value = null; shownKey = ''
  })
  watch(key, value => {
    if (!value) { viewRequest?.abort(); busy.value = false; return }
    failure.value = null
    const cached = cache.get(value)
    if (cached) {
      // A cached view lands at once; an old one is counted again quietly.
      viewRequest?.abort()
      land(value, cached.value)
      if (Date.now() - cached.at > MAX_AGE) void count(value, true)
      else if (!whole.value && value !== wholeKey.value) void countWhole()
      return
    }
    void count(value, false)
  }, { immediate: true })
  // The work changed (a status, an estimate, a new ticket): forget this project's scopes and count the shown one again.
  watch(options.freshness, (now, before) => {
    if (now === before || !key.value) return
    const project = scoped(figuresPath(options.projectId.value!, {}).split('?')[0])
    for (const cachedKey of [...cache.keys()]) if (cachedKey.split('?')[0] === project) cache.delete(cachedKey)
    void count(key.value, shownKey === key.value)
  })
  onBeforeUnmount(() => { disposed = true; viewRequest?.abort(); wholeRequest?.abort() })

  const failed = computed(() => failure.value && failure.value.key === key.value ? failure.value.message : null)
  const narrowed = computed(() => !!key.value && key.value !== wholeKey.value)
  function retry() { if (key.value) { failure.value = null; void count(key.value, false) } }
  return { figures, whole, busy, failed, narrowed, retry, key }
}
