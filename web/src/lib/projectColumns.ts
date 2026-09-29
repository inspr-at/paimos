// SPDX-License-Identifier: AGPL-3.0-only
// Projects list columns (AEON-136): Key and Project always lead; the rest can be
// shown, hidden and reordered, remembered per person in the "projects"
// preference. Open, Doing and Done share one width so their icons form straight
// lines. Columns that do not fit the list step aside, least essential first.
// Free of Vue for unit tests.

export type ProjectColumnId = 'open' | 'doing' | 'done' | 'progress' | 'people' | 'activity'
export interface ProjectColumnDef { id: ProjectColumnId; label: string; tip: string; min: number }
export interface ProjectColumnPrefs { order?: ProjectColumnId[]; visible?: ProjectColumnId[] }

// The width Open, Doing and Done share.
export const STAT_WIDTH = 64
export const PROJECT_COLUMNS: ProjectColumnDef[] = [
  { id: 'open', label: 'Open', tip: 'Open · waiting, including blocked', min: STAT_WIDTH },
  { id: 'doing', label: 'Doing', tip: 'In progress · in progress, active and QA', min: STAT_WIDTH },
  { id: 'done', label: 'Done', tip: 'Done · done, delivered and accepted', min: STAT_WIDTH },
  { id: 'progress', label: 'Progress', tip: 'Done out of open, doing and done', min: 150 },
  { id: 'people', label: 'People', tip: 'Recently active people', min: 92 },
  { id: 'activity', label: 'Last activity', tip: 'When anything in the project last changed', min: 108 },
]
export const PROJECT_COLUMN_BY_ID = new Map(PROJECT_COLUMNS.map(c => [c.id, c]))

// Progress is done out of open, doing and done. Cancelled and archived stay
// out of the share, so shelved work does not lower it.
export function projectProgressPercent(open: number, inProgress: number, done: number): number {
  const scope = open + inProgress + done
  return scope > 0 ? Math.round((done / scope) * 100) : 0
}

export function projectProgressTip(open: number, inProgress: number, done: number, cancelled: number): string {
  const scope = open + inProgress + done
  return `${done.toLocaleString('en-GB')} of ${scope.toLocaleString('en-GB')} done${cancelled ? ` · ${cancelled} cancelled` : ''}`
}
export const DEFAULT_PROJECT_COLUMNS: ProjectColumnId[] = ['open', 'doing', 'done', 'progress', 'activity']
// Without a saved choice, lists this wide also show who was active lately.
export const WIDE_LIST = 1500
// The least essential leave first when the list is narrow.
const DROP_ORDER: ProjectColumnId[] = ['people', 'activity', 'progress', 'done', 'doing', 'open']
// Key badge, a readable project name, the … button and the gaps between columns.
const FIXED = 100 + 240 + 28
const GAP = 20

const known = (id: unknown): id is ProjectColumnId => typeof id === 'string' && PROJECT_COLUMN_BY_ID.has(id as ProjectColumnId)

export function projectColumnOrder(prefs: ProjectColumnPrefs | null | undefined): ProjectColumnId[] {
  const saved = (prefs?.order ?? []).filter(known)
  return [...new Set([...saved, ...PROJECT_COLUMNS.map(c => c.id)])]
}
export function chosenProjectColumns(prefs: ProjectColumnPrefs | null | undefined): ProjectColumnId[] {
  const visible = new Set(prefs?.visible ? prefs.visible.filter(known) : DEFAULT_PROJECT_COLUMNS)
  return projectColumnOrder(prefs).filter(id => visible.has(id))
}
export const customisedProjectColumns = (prefs: ProjectColumnPrefs | null | undefined) => !!prefs?.order || !!prefs?.visible

// The chosen columns that fit `width` pixels of list, in order. Without a saved
// choice a wide list adds People.
export function fittingProjectColumns(width: number, prefs: ProjectColumnPrefs | null | undefined): ProjectColumnId[] {
  let ids = !prefs?.visible && width >= WIDE_LIST ? chosenProjectColumns({ ...prefs, visible: [...DEFAULT_PROJECT_COLUMNS, 'people'] }) : chosenProjectColumns(prefs)
  const need = () => FIXED + ids.reduce((sum, id) => sum + PROJECT_COLUMN_BY_ID.get(id)!.min + GAP, 0) + GAP * 2
  for (const id of DROP_ORDER) {
    if (width <= 0 || need() <= width) break
    ids = ids.filter(x => x !== id)
  }
  return ids
}

// The grid track for each column.
export function projectColumnTrack(id: ProjectColumnId): string {
  if (id === 'open' || id === 'doing' || id === 'done') return `${STAT_WIDTH}px`
  if (id === 'progress') return 'minmax(140px, 200px)'
  if (id === 'people') return 'minmax(92px, max-content)'
  return 'max-content'
}

export function moveProjectColumn(order: ProjectColumnId[], id: ProjectColumnId, step: -1 | 1): ProjectColumnId[] {
  const index = order.indexOf(id), to = index + step
  if (index === -1 || to < 0 || to >= order.length) return order
  const next = [...order]
  ;[next[index], next[to]] = [next[to], next[index]]
  return next
}
