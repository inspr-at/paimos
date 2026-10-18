// SPDX-License-Identifier: AGPL-3.0-only
// Saved views per project: the person's own and the ones shared with the
// project, kept for the session so the view bar and the palette share them.
// A view is the list's state (filters, sort, grouping, columns) with a name;
// see ticketList.ts viewShape and filtersFromView.
import { reactive } from 'vue'
import { createView, deleteView, listViews, restoreView, updateView, type SavedView } from './api'
import { viewShape, type ListFilters } from './ticketList'

interface ProjectViews { items: SavedView[]; loaded: boolean; loading: boolean; error: string }
const byProject = reactive(new Map<string, ProjectViews>())
const loads = new Map<string, Promise<void>>()

function entry(projectId: string): ProjectViews {
  if (!byProject.has(projectId)) byProject.set(projectId, { items: [], loaded: false, loading: false, error: '' })
  return byProject.get(projectId)!
}
export function viewsOf(projectId: string | null | undefined): ProjectViews {
  return projectId ? entry(projectId) : { items: [], loaded: false, loading: false, error: '' }
}
export function loadViews(projectId: string, force = false): Promise<void> {
  const state = entry(projectId)
  if (!force && (state.loaded || loads.has(projectId))) return loads.get(projectId) ?? Promise.resolve()
  state.loading = true
  const run = listViews(projectId)
    .then(page => { state.items = page.items; state.loaded = true; state.error = '' })
    .catch(e => { state.error = e instanceof Error ? e.message : 'Views could not be loaded' })
    .finally(() => { state.loading = false; loads.delete(projectId) })
  loads.set(projectId, run)
  return run
}
function replace(projectId: string, view: SavedView) {
  const state = entry(projectId)
  const index = state.items.findIndex(item => item.id === view.id)
  if (index === -1) state.items.push(view)
  else state.items.splice(index, 1, view)
}

export async function saveNewView(projectId: string, name: string, filters: ListFilters, shared: boolean): Promise<SavedView> {
  const view = await createView({ name, project_id: projectId, shared, ...viewShape(filters) })
  replace(projectId, view)
  return view
}
export async function saveViewState(projectId: string, view: SavedView, filters: ListFilters): Promise<SavedView> {
  const saved = await updateView(view.id, viewShape(filters))
  replace(projectId, saved)
  return saved
}
export async function renameView(projectId: string, view: SavedView, name: string): Promise<SavedView> {
  const saved = await updateView(view.id, { name })
  replace(projectId, saved)
  return saved
}
export async function shareView(projectId: string, view: SavedView, shared: boolean): Promise<SavedView> {
  const saved = await updateView(view.id, { shared })
  replace(projectId, saved)
  return saved
}
// A personal copy, for the owner and for anyone reading a shared view.
export async function duplicateView(projectId: string, view: SavedView, name: string): Promise<SavedView> {
  const copy = await createView({
    name, project_id: projectId, shared: false,
    filters: Object.fromEntries(Object.entries(view.filters).filter((entry): entry is [string, string] => typeof entry[1] === 'string')),
    sort_keys: view.sort_keys, group_by: view.group_by, columns: view.columns, mode: view.mode,
  })
  replace(projectId, copy)
  return copy
}
// Deletes the view and returns how to bring it back (the toast's Undo).
export async function removeView(projectId: string, view: SavedView): Promise<() => Promise<SavedView>> {
  await deleteView(view.id)
  const state = entry(projectId)
  const at = state.items.findIndex(item => item.id === view.id)
  if (at !== -1) state.items.splice(at, 1)
  return async () => {
    const back = await restoreView(view.id)
    const items = entry(projectId).items
    if (!items.some(item => item.id === back.id)) {
      // Back where it was in the bar, which is ordered by creation.
      const index = items.findIndex(item => item.created_at > back.created_at)
      items.splice(index === -1 ? items.length : index, 0, back)
    }
    return back
  }
}
// A copy's name that is not taken yet ("Bugs copy", "Bugs copy 2").
export function copyName(name: string, taken: string[]): string {
  const base = `${name.replace(/ copy( \d+)?$/, '')} copy`.slice(0, 80)
  if (!taken.includes(base)) return base
  for (let n = 2; n < 100; n++) if (!taken.includes(`${base} ${n}`)) return `${base} ${n}`.slice(0, 80)
  return base
}
