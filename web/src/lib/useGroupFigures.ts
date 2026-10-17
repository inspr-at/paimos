// SPDX-License-Identifier: AGPL-3.0-only
// Group summaries (AEON-1388) for the list on screen: one figures object per group, from the header's figures API for
// the view narrowed to that group. Counted after the list and the header figures have painted, two at a time in group
// order, cached per scope (person + narrowed query) and dropped when the scope leaves the screen. A group never shows
// another scope's figures; when the work changes, the shown figures stay until the new count replaces them in place.
import { onBeforeUnmount, shallowRef, watch, type Ref } from 'vue'
import { APIError, figuresPath, getProjectFigures, type ProjectFigures } from './api'
import { figuresCache, figuresLoader, groupParams, groupScopeKey, UNCOUNTED_GROUP, type FiguresRequest } from './groupFigures'
import type { ListFilters, RowGroup } from './ticketList'
import { validFigures } from './workFigures'
import { workFiguresText } from './workFiguresText'

// The groups counted per view; a longer list says so in the groups beyond it.
export const MAX_COUNTED_GROUPS = 100
const TOO_MANY_GROUPS = `Only the first ${MAX_COUNTED_GROUPS} groups are counted.`
const cache = figuresCache({ maxEntries: 400, maxAge: 30_000, now: () => Date.now() })

export interface GroupFigureState { figures: ProjectFigures | null; failed: string | null }

function failureText(error: unknown): string {
  const text = workFiguresText()
  if (error instanceof APIError && error.status === 422) return text.tooMany
  if (error instanceof APIError && error.status === 503) return text.slow
  return text.failed
}
function idle(): Promise<void> {
  return new Promise(resolve => requestAnimationFrame(() => {
    if (typeof requestIdleCallback === 'function') requestIdleCallback(() => resolve(), { timeout: 600 })
    else setTimeout(resolve, 0)
  }))
}

export function useGroupFigures(options: {
  owner: Ref<string>
  projectId: Ref<string | null>
  filters: Ref<ListFilters>
  groups: Ref<RowGroup[]>
  // True once the list and the header's figures have painted (or the header could not be counted).
  ready: Ref<boolean>
  // Changes when the project's work changes.
  freshness: Ref<unknown>
}) {
  const state = shallowRef(new Map<string, GroupFigureState>())
  // Scope key per group on screen; a group whose scope changed loses its figures at once.
  let scopes = new Map<string, string>()
  const set = (groupKey: string, value: GroupFigureState) => { const next = new Map(state.value); next.set(groupKey, value); state.value = next }
  const loader = figuresLoader({
    cache, valid: validFigures, concurrency: 2,
    fetch: (params, signal) => getProjectFigures(options.projectId.value!, params, signal),
    land: (key, value) => { for (const [group, scope] of scopes) if (scope === key) set(group, { figures: value, failed: null }) },
    fail: (key, error) => { for (const [group, scope] of scopes) if (scope === key && !state.value.get(group)?.figures) set(group, { figures: null, failed: failureText(error) }) },
  })

  let pass = 0
  async function plan() {
    const run = ++pass
    const id = options.projectId.value, owner = options.owner.value, group = options.filters.value.group
    if (!id || !owner || group === 'none' || !options.ready.value) { loader.want([]); return }
    await idle()
    if (run !== pass) return
    const requests: FiguresRequest[] = []
    const next = new Map<string, string>()
    const shown = new Map<string, GroupFigureState>()
    options.groups.value.forEach((entry, index) => {
      const params = index < MAX_COUNTED_GROUPS ? groupParams(id, options.filters.value, group, entry) : null
      if (!params) { shown.set(entry.key, { figures: null, failed: index < MAX_COUNTED_GROUPS ? UNCOUNTED_GROUP : TOO_MANY_GROUPS }); return }
      const key = groupScopeKey(owner, figuresPath(id, params))
      next.set(entry.key, key)
      const before = state.value.get(entry.key)
      if (scopes.get(entry.key) === key && before) shown.set(entry.key, before)
      if (!requests.some(request => request.key === key)) requests.push({ key, params })
    })
    scopes = next
    state.value = shown
    loader.want(requests)
  }
  watch(() => [options.owner.value, options.projectId.value, options.ready.value, options.filters.value, options.groups.value.map(entry => entry.key).join('\u0000')], () => { void plan() }, { immediate: true })
  // Another person: nothing of the previous one's counts stays.
  watch(options.owner, () => { cache.clear(); state.value = new Map(); scopes = new Map() })
  watch(options.freshness, (now, before) => {
    const id = options.projectId.value
    if (now === before || !id) return
    const project = groupScopeKey(options.owner.value, figuresPath(id, {}).split('?')[0])
    cache.forget(key => key.split('?')[0] === project)
    if (scopes.size) loader.refresh()
  })
  onBeforeUnmount(() => { pass++; loader.stop() })
  return { groupFigures: state }
}
