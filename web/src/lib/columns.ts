// SPDX-License-Identifier: AGPL-3.0-only
// Ticket list columns: which show at a given table width, in which order and how
// wide. Without a saved choice the table fills its width: narrow tables drop
// columns, wide ones (from ~1500px of table, about an 1800px screen) add Assignee,
// Epic, Release and Tags (when some row has them), Estimate and Created. A saved
// choice fixes order and visibility; columns that cannot fit still step aside from
// the end. On wide tables Title stops at ~960px and the spare width goes to the
// text columns, so the metadata stays near the title. Free of Vue for unit tests.
// The planning columns (Model, Tokens, ≈ Cost, Paid; AEON-329) join wide tables
// when a loaded row fills them, leave first when space runs out, and stay hidden
// while no loaded row has a value, even when chosen.
import type { SortField } from './work.ts'
import { PLANNING_COLUMNS } from './planning.ts'

export type ColumnId = 'key' | 'title' | 'status' | 'priority' | 'assignee' | 'epic' | 'release' | 'tags' | 'cost' | 'estimate' | 'model' | 'tokens' | 'list_cost' | 'paid' | 'created' | 'updated' | 'progress' | 'eta'
export interface ColumnDef { id: ColumnId; label: string; sort: SortField | null; width: number; min: number; max: number; end?: boolean }
// defaultView: the saved view this person opens the project with.
export interface ListPrefs { order?: ColumnId[]; visible?: ColumnId[]; widths?: Partial<Record<ColumnId, number>>; defaultView?: string | null }

export const COLUMNS: ColumnDef[] = [
  { id: 'key', label: 'Key', sort: 'key', width: 118, min: 84, max: 220 },
  { id: 'title', label: 'Title', sort: 'title', width: 0, min: 240, max: 4000 },
  { id: 'status', label: 'Status', sort: 'state', width: 138, min: 84, max: 260 },
  { id: 'priority', label: 'Priority', sort: 'priority', width: 112, min: 72, max: 200 },
  { id: 'assignee', label: 'Assignee', sort: 'assignee', width: 156, min: 96, max: 320 },
  { id: 'epic', label: 'Epic', sort: null, width: 220, min: 110, max: 480 },
  { id: 'release', label: 'Release', sort: null, width: 132, min: 84, max: 260 },
  { id: 'tags', label: 'Tags', sort: null, width: 180, min: 96, max: 420 },
  { id: 'cost', label: 'Cost unit', sort: null, width: 150, min: 96, max: 320 },
  { id: 'estimate', label: 'Estimate', sort: 'estimate', width: 96, min: 72, max: 180, end: true },
  // 156px fits "Codex astra · xhigh" at the cell's 12.5px type.
  { id: 'model', label: 'Model', sort: 'model', width: 156, min: 96, max: 260 },
  // 118px fits "1.54M / 10M"; ≈ Cost fits "$4.48 / $27".
  { id: 'tokens', label: 'Tokens', sort: 'tokens', width: 118, min: 84, max: 180, end: true },
  { id: 'list_cost', label: '≈ Cost', sort: 'list_cost', width: 112, min: 80, max: 170, end: true },
  { id: 'paid', label: 'Paid', sort: 'paid', width: 100, min: 72, max: 160, end: true },
  { id: 'created', label: 'Created', sort: 'created_at', width: 104, min: 80, max: 200, end: true },
  { id: 'updated', label: 'Updated', sort: 'updated_at', width: 104, min: 80, max: 200, end: true },
  { id: 'progress', label: 'Progress', sort: 'progress', width: 100, min: 84, max: 140, end: true },
  // 112px fits "overdue 5 min" at the cell's 12.5px type; 96px ellipsizes it.
  { id: 'eta', label: 'ETA', sort: 'eta_ready', width: 112, min: 88, max: 160, end: true },
]
export const COLUMN_BY_ID = new Map(COLUMNS.map(column => [column.id, column]))
// Key and Title always lead; the rest can be hidden and reordered.
export const PINNED: ColumnId[] = ['key', 'title']
export const WIDE_TABLE = 1500
// Title's automatic width on wide tables; a width the person gives Title wins.
export const TITLE_TARGET = 960
// Automatic wide columns keep Title at least this wide; otherwise they step aside.
const TITLE_ROOM = 420
const PHONE: ColumnId[] = ['key', 'title', 'status', 'priority', 'updated']
// The order columns leave in when space runs out: the least essential first.
const DROP_ORDER: ColumnId[] = ['paid', 'list_cost', 'tokens', 'model', 'eta', 'progress', 'estimate', 'cost', 'tags', 'release', 'created', 'epic', 'assignee', 'updated', 'priority', 'status']
// Columns only wide tables add on their own.
const WIDE_EXTRAS: ColumnId[] = ['paid', 'list_cost', 'tokens', 'model', 'estimate', 'tags', 'release', 'created', 'epic', 'assignee']
// The text columns that take spare width on wide tables (their text gets room).
const GROWS: ColumnId[] = ['epic', 'tags', 'assignee', 'release', 'cost']
// Which optional values any loaded row has. `workers` is live ticket work with
// no stored assignee; it earns the Assignee column the same way a person does.
export interface Present { assigned?: boolean; workers?: boolean; estimate?: boolean; release?: boolean; tags?: boolean; progress?: boolean; eta?: boolean; model?: boolean; tokens?: boolean; list_cost?: boolean; paid?: boolean }

export function orderOf(prefs: ListPrefs | null | undefined): ColumnId[] {
  const valid = (prefs?.order ?? []).filter((id): id is ColumnId => COLUMN_BY_ID.has(id as ColumnId) && !PINNED.includes(id as ColumnId))
  const rest = COLUMNS.map(c => c.id).filter(id => !PINNED.includes(id) && !valid.includes(id))
  return [...PINNED, ...new Set(valid), ...rest]
}

// Wide tables add Assignee, Epic and Created, and Release, Tags, Estimate and the
// planning columns when some row has one; narrower ones show Assignee when
// someone is assigned or a live worker is on a loaded ticket.
export function automaticColumns(tableWidth: number, present: Present = {}): ColumnId[] {
  const out: ColumnId[] = ['key', 'title', 'status', 'priority']
  if (tableWidth >= WIDE_TABLE) {
    const optional: ColumnId[] = [...(present.release ? ['release' as const] : []), ...(present.tags ? ['tags' as const] : []), ...(present.estimate ? ['estimate' as const] : []),
      ...PLANNING_COLUMNS.filter(id => present[id])]
    const wide: ColumnId[] = [...out, 'assignee', 'epic', ...optional, 'created', 'updated']
    if (present.progress) wide.push('progress')
    if (present.eta) wide.push('eta')
    return wide
  }
  if (tableWidth > 900 && (present.assigned || present.workers)) out.push('assignee')
  if (tableWidth > 740) out.push('updated')
  if (present.progress) out.push('progress')
  if (present.eta) out.push('eta')
  return out
}

export function widthOf(id: ColumnId, prefs: ListPrefs | null | undefined): number {
  const def = COLUMN_BY_ID.get(id)!
  const saved = prefs?.widths?.[id]
  return typeof saved === 'number' && Number.isFinite(saved) ? Math.max(def.min, Math.min(def.max, Math.round(saved))) : def.width
}

// The columns to show, in order. `customised` is true when a saved choice applies.
export function visibleColumns(tableWidth: number, options: { phone: boolean; present?: Present; prefs?: ListPrefs | null }): { columns: ColumnDef[]; customised: boolean } {
  if (options.phone) {
    // A phone card keeps its own set. Progress, ETA and an hour estimate join it when a
    // loaded row has one; a saved choice does not hide them.
    const phone: ColumnId[] = [...PHONE]
    if (options.present?.progress) phone.push('progress')
    if (options.present?.eta) phone.push('eta')
    if (options.present?.estimate) phone.push('estimate')
    return { columns: phone.map(id => COLUMN_BY_ID.get(id)!), customised: false }
  }
  const prefs = options.prefs
  const customised = !!prefs?.visible
  const chosen = customised ? new Set<ColumnId>([...PINNED, ...prefs!.visible!]) : new Set(automaticColumns(tableWidth, options.present))
  for (const id of PLANNING_COLUMNS) if (!options.present?.[id]) chosen.delete(id)
  let ids = orderOf(prefs).filter(id => chosen.has(id))
  const total = (title: number) => ids.reduce((sum, id) => sum + (id === 'title' ? title : widthOf(id, prefs)), 0)
  // Columns a wide table adds on its own leave first when Title would get cramped.
  if (!customised) for (const id of WIDE_EXTRAS) {
    if (total(TITLE_ROOM) <= tableWidth || tableWidth <= 0) break
    ids = ids.filter(x => x !== id)
  }
  // Whatever does not fit beside a readable title steps aside, least essential first.
  const title = COLUMN_BY_ID.get('title')!.min
  for (const id of DROP_ORDER) {
    if (total(title) <= tableWidth || tableWidth <= 0) break
    ids = ids.filter(x => x !== id)
  }
  return { columns: ids.map(id => COLUMN_BY_ID.get(id)!), customised }
}

const sumWidths = (widths: Partial<Record<ColumnId, number>>) => Object.values(widths).reduce((sum, w) => sum + (w ?? 0), 0)
// The column after Title first, then the rest toward the left, so Title's edge
// resizes its neighbour before it touches a column further away.
function besideTitle(ids: ColumnId[]): ColumnId[] {
  const at = ids.indexOf('title')
  const after = at < 0 ? [] : ids.slice(at + 1)
  const before = at < 0 ? ids : ids.slice(0, at)
  return [...after, ...before].filter(id => id !== 'title')
}
function sizedWidth(id: ColumnId, prefs: ListPrefs | null | undefined, live: Partial<Record<ColumnId, number>>): boolean {
  return live[id] !== undefined || typeof prefs?.widths?.[id] === 'number'
}
// Move flexible columns by `amount` px, never past min or max. Sized columns stay:
// a width the person set is not spent to satisfy Title.
function shiftColumns(ids: ColumnId[], out: Partial<Record<ColumnId, number>>, amount: number, dir: 'grow' | 'shrink', flexible: (id: ColumnId) => boolean) {
  let left = Math.max(0, Math.round(amount))
  for (const id of besideTitle(ids)) {
    if (left <= 0 || !flexible(id)) continue
    const def = COLUMN_BY_ID.get(id)!
    const current = out[id]!
    const next = dir === 'shrink' ? Math.max(def.min, current - left) : Math.min(def.max, current + left)
    left -= Math.abs(next - current)
    out[id] = next
  }
}

// How wide Title can be while every other column stays inside its min and max.
// Columns the person already sized keep that width, so they never collapse.
export function titleRoom(ids: ColumnId[], tableWidth: number, prefs?: ListPrefs | null, live: Partial<Record<ColumnId, number>> = {}): { min: number; max: number } {
  const def = COLUMN_BY_ID.get('title')!
  let fixed = 0, flexMin = 0, flexMax = 0
  for (const id of ids) {
    if (id === 'title') continue
    const col = COLUMN_BY_ID.get(id)!
    if (sizedWidth(id, prefs, live)) fixed += live[id] ?? widthOf(id, prefs)
    else { flexMin += col.min; flexMax += col.max }
  }
  const fitMax = Math.floor(tableWidth - fixed - flexMin)
  const fitMin = Math.ceil(tableWidth - fixed - flexMax)
  const max = Math.min(def.max, fitMax)
  const min = Math.max(def.min, fitMin)
  if (min <= max) return { min, max }
  const left = Math.max(0, fitMax)
  return { min: left, max: left }
}

// Widths for every visible column except Title (which takes the rest). Without a
// saved title width, Title aims for TITLE_TARGET and spare width beyond that widens
// Epic, Tags, Assignee and Release (not ones the person sized) up to their maximum,
// in proportion to their normal width. What is still left goes back to Title.
// A width the person gives Title is kept: the unsized columns beside it grow or
// shrink, the next column first, and stop at their own min and max.
export function layoutWidths(ids: ColumnId[], tableWidth: number, prefs?: ListPrefs | null, live: Partial<Record<ColumnId, number>> = {}): Partial<Record<ColumnId, number>> {
  const out: Partial<Record<ColumnId, number>> = {}
  for (const id of ids) if (id !== 'title') out[id] = live[id] ?? widthOf(id, prefs)
  const sized = (id: ColumnId) => sizedWidth(id, prefs, live)
  if (sized('title')) {
    const target = live.title ?? widthOf('title', prefs)
    const spare = tableWidth - sumWidths(out) - target
    const flexible = (id: ColumnId) => !sized(id)
    if (spare < -0.5) shiftColumns(ids, out, -spare, 'shrink', flexible)
    else if (spare > 0.5) shiftColumns(ids, out, spare, 'grow', flexible)
    for (const id of Object.keys(out) as ColumnId[]) out[id] = Math.floor(out[id]!)
    return out
  }
  const titleTarget = TITLE_TARGET
  let spare = tableWidth - sumWidths(out) - titleTarget
  const growing = GROWS.filter(id => ids.includes(id) && !sized(id))
  while (spare >= 1 && growing.length) {
    const weight = growing.reduce((sum, id) => sum + COLUMN_BY_ID.get(id)!.width, 0)
    let used = 0
    for (const id of [...growing]) {
      const def = COLUMN_BY_ID.get(id)!
      const add = Math.min(def.max - out[id]!, spare * def.width / weight)
      out[id] = out[id]! + add
      used += add
      if (out[id]! >= def.max - 0.5) growing.splice(growing.indexOf(id), 1)
    }
    spare -= used
    if (used < 0.5) break
  }
  for (const id of Object.keys(out) as ColumnId[]) out[id] = Math.floor(out[id]!)
  return out
}

// The release label and tags classic imports keep in the ticket's fields.
export function releaseLabel(fields: Record<string, unknown> | null | undefined): string {
  const value = fields?.release
  if (typeof value === 'string') return value.trim()
  if (value && typeof value === 'object') { const label = (value as { label?: unknown; name?: unknown }).label ?? (value as { name?: unknown }).name; return typeof label === 'string' ? label.trim() : '' }
  return ''
}
// The cost unit a ticket books to: its own, else the one classic PPM gave it.
export function costUnitLabel(fields: Record<string, unknown> | null | undefined): string {
  const label = (value: unknown): string => {
    if (typeof value === 'string') return value.trim()
    if (value && typeof value === 'object') { const v = (value as { label?: unknown; name?: unknown }).label ?? (value as { name?: unknown }).name; return typeof v === 'string' ? v.trim() : '' }
    return ''
  }
  if (fields && 'cost_unit' in fields) return label(fields.cost_unit)
  const classic = fields?.classic
  return classic && typeof classic === 'object' ? label((classic as Record<string, unknown>).cost_unit) : ''
}
export interface TagRef { name: string; color: string }
export function tagList(fields: Record<string, unknown> | null | undefined): TagRef[] {
  const value = fields?.tags
  if (!Array.isArray(value)) return []
  return value.flatMap(tag => {
    if (typeof tag === 'string') return tag.trim() ? [{ name: tag.trim(), color: '' }] : []
    if (tag && typeof tag === 'object' && typeof (tag as { name?: unknown }).name === 'string') {
      const { name, color } = tag as { name: string; color?: unknown }
      return name.trim() ? [{ name: name.trim(), color: typeof color === 'string' ? color : '' }] : []
    }
    return []
  })
}

// Moving a column one step within the reorderable part.
export function moveColumn(order: ColumnId[], id: ColumnId, step: -1 | 1): ColumnId[] {
  const free = order.filter(x => !PINNED.includes(x))
  const index = free.indexOf(id)
  const to = index + step
  if (index === -1 || to < 0 || to >= free.length) return order
  const next = [...free]
  ;[next[index], next[to]] = [next[to], next[index]]
  return [...PINNED, ...next]
}
