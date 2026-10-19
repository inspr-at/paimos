// SPDX-License-Identifier: AGPL-3.0-only
// Ticket list columns: which show at a given table width, in which order and how
// wide. Without a saved choice the table fills its width: wide ones (from ~1500px
// of table, about an 1800px screen) add Assignee, Epic, Release and Tags (when
// some row has them), Estimate and Created. A saved choice fixes order and which
// columns are chosen. Title keeps its minimum at every desktop width (AEON-1470):
// widths beside it give way first, then whole columns, least essential first.
// On wide tables Title stops at ~960px and the spare width goes to the
// text columns, so the metadata stays near the title. Free of Vue for unit tests.
// Planning columns join Automatic when populated. Saved ticks always draw,
// including empty cells. Legacy Paid choices map to Cost (AEON-511).
import type { SortField } from './work.ts'
import { PLANNING_COLUMNS } from './planning.ts'

export type ColumnId = 'key' | 'title' | 'status' | 'priority' | 'assignee' | 'epic' | 'release' | 'tags' | 'cost' | 'estimate' | 'model' | 'suggested' | 'tokens' | 'list_cost' | 'paid' | 'created' | 'updated' | 'progress' | 'eta'
export interface ColumnDef<Id extends string = string> { id: Id; label: string; sort: SortField | null; width: number; min: number; max: number; end?: boolean }
// defaultView: the saved view this person opens the project with.
export interface ListPrefs { order?: ColumnId[]; visible?: ColumnId[]; widths?: Partial<Record<string, number>>; defaultView?: string | null }

export const COLUMNS: ColumnDef<ColumnId>[] = [
  { id: 'key', label: 'Key', sort: 'key', width: 118, min: 84, max: 480 },
  { id: 'title', label: 'Title', sort: 'title', width: 0, min: 240, max: 4000 },
  { id: 'status', label: 'Status', sort: 'state', width: 138, min: 84, max: 260 },
  { id: 'priority', label: 'Priority', sort: 'priority', width: 112, min: 72, max: 200 },
  { id: 'assignee', label: 'Assignee', sort: 'assignee', width: 156, min: 96, max: 320 },
  { id: 'epic', label: 'Epic', sort: null, width: 220, min: 110, max: 480 },
  { id: 'release', label: 'Release', sort: null, width: 132, min: 84, max: 260 },
  { id: 'tags', label: 'Tags', sort: null, width: 180, min: 96, max: 420 },
  { id: 'cost', label: 'Cost unit', sort: null, width: 150, min: 96, max: 320 },
  { id: 'estimate', label: 'Estimate', sort: 'estimate', width: 96, min: 72, max: 180, end: true },
  // Model name and version, brand mark, effort meter, measured check and +N.
  { id: 'model', label: 'Model', sort: 'model', width: 176, min: 96, max: 260 },
  { id: 'suggested', label: 'Suggested release', sort: null, width: 152, min: 120, max: 260 },
  // Fixed measured slot follows the value; running estimates carry "~".
  { id: 'tokens', label: 'Tokens', sort: 'tokens', width: 118, min: 84, max: 180, end: true },
  { id: 'list_cost', label: 'Cost', sort: 'list_cost', width: 128, min: 80, max: 170, end: true },
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
// The order whole columns leave in when even their minimum widths leave Title
// under its own (AEON-1470): the least essential first. Key, Title, Status and
// Priority never leave.
export const GIVE_WAY: ColumnId[] = ['updated', 'created', 'estimate', 'tokens', 'list_cost', 'paid', 'suggested', 'model', 'eta', 'progress', 'tags', 'cost', 'release', 'epic', 'assignee']
// A column that left comes back only with this much to spare, so the set does
// not flap while a window is resized across its threshold.
export const RETURN_ROOM = 16
// Columns only wide tables add on their own.
const WIDE_EXTRAS: ColumnId[] = ['suggested', 'list_cost', 'tokens', 'model', 'estimate', 'tags', 'release', 'created', 'epic', 'assignee']
// The text columns that take spare width on wide tables (their text gets room).
const GROWS: ColumnId[] = ['epic', 'tags', 'assignee', 'release', 'cost']
// Which optional values any loaded row has. `workers` is live ticket work with
// no stored assignee; it earns the Assignee column the same way a person does.
export interface Present { suggested?: boolean; assigned?: boolean; workers?: boolean; estimate?: boolean; release?: boolean; tags?: boolean; progress?: boolean; eta?: boolean; model?: boolean; tokens?: boolean; list_cost?: boolean; paid?: boolean }

/** Normalize saved preferences, shared links and saved views without mutating them. */
export function normalizeColumnIds(ids: readonly unknown[]): ColumnId[] {
  return [...new Set(ids.map(id => id === 'paid' ? 'list_cost' : id).filter((id): id is ColumnId => typeof id === 'string' && COLUMN_BY_ID.has(id as ColumnId)))]
}
/** Ticks represent the saved choice, even when the table is a phone card. */
export function pickerColumns(prefs: ListPrefs | null | undefined, drawn: ColumnId[], costAllowed: boolean) {
  const allowed = (id: ColumnId) => costAllowed || id !== 'list_cost'
  return { order: orderOf(prefs).filter(allowed), visible: normalizeColumnIds(prefs?.visible ?? drawn).filter(allowed), customised: !!prefs?.visible }
}

export function orderOf(prefs: ListPrefs | null | undefined): ColumnId[] {
  const valid = normalizeColumnIds(prefs?.order ?? []).filter(id => !PINNED.includes(id))
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
      ...PLANNING_COLUMNS.filter(id => present[id]), ...(present.suggested ? ['suggested' as const] : [])]
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

export function widthOf(id: string, prefs: ListPrefs | null | undefined, definitions: ReadonlyMap<string, ColumnDef> = COLUMN_BY_ID): number {
  const def = definitions.get(id)!
  const saved = prefs?.widths?.[id] ?? (id === 'list_cost' ? prefs?.widths?.paid : undefined)
  return typeof saved === 'number' && Number.isFinite(saved) ? Math.max(def.min, Math.min(def.max, Math.round(saved))) : def.width
}

// What the give-way measures besides the column definitions. mins: a host's own
// minimum for a column (the ticket grid's first-cell slots, Title's extras);
// reserve: the minimum width of host columns, which never leave; sorted: the
// list's sort fields; previous: the columns this table left out last time.
export interface ColumnFit { mins?: Partial<Record<string, number>>; reserve?: number; sorted?: readonly string[]; previous?: readonly string[] }

// Whole columns leave, in GIVE_WAY order, only while the chosen ones do not fit
// even at their minimum widths beside Title's minimum. Widths alone never decide
// it (they give way first, in layoutWidths), so hover, live updates, more rows
// or a folded group cannot change the set. A column the list is sorted by stays;
// the next one leaves instead. One that left returns with RETURN_ROOM to spare.
export function giveWay(ids: ColumnId[], tableWidth: number, fit: ColumnFit = {}): { ids: ColumnId[]; dropped: ColumnId[] } {
  if (!(tableWidth > 0)) return { ids, dropped: [] }
  const min = (id: ColumnId) => fit.mins?.[id] ?? COLUMN_BY_ID.get(id)!.min
  const sorted = new Set(fit.sorted ?? [])
  const keeps = (id: ColumnId) => { const sort = COLUMN_BY_ID.get(id)?.sort; return (!!sort && sorted.has(sort)) || (id === 'list_cost' && sorted.has('paid')) }
  const candidates = GIVE_WAY.filter(id => ids.includes(id) && !keeps(id))
  // need[k]: the width the chosen columns need with the first k candidates gone.
  const need = [ids.reduce((sum, id) => sum + min(id), fit.reserve ?? 0)]
  for (const id of candidates) need.push(need[need.length - 1] - min(id))
  const fewest = (room: number) => { const k = need.findIndex(width => width <= room); return k === -1 ? candidates.length : k }
  const previous = new Set(fit.previous ?? [])
  const before = candidates.reduce((k, id, index) => previous.has(id) ? index + 1 : k, 0)
  const k = Math.min(Math.max(before, fewest(tableWidth)), fewest(tableWidth - RETURN_ROOM))
  const dropped = candidates.slice(0, k)
  return { ids: ids.filter(id => !dropped.includes(id)), dropped }
}

// The columns to show, in order. `customised` is true when a saved choice applies;
// `dropped` names chosen columns this width leaves out (Display keeps them ticked).
export function visibleColumns(tableWidth: number, options: { phone: boolean; present?: Present; prefs?: ListPrefs | null; costAllowed?: boolean; fit?: ColumnFit }): { columns: ColumnDef<ColumnId>[]; customised: boolean; dropped: ColumnId[] } {
  if (options.phone && !options.prefs?.visible) {
    // Automatic phone cards keep their compact set. Saved choices below apply
    // to cards too; their optional cells grow downward instead of scrolling.
    const phone: ColumnId[] = [...PHONE]
    if (options.present?.progress) phone.push('progress')
    if (options.present?.eta) phone.push('eta')
    if (options.present?.estimate) phone.push('estimate')
    return { columns: phone.map(id => COLUMN_BY_ID.get(id)!), customised: false, dropped: [] }
  }
  const prefs = options.prefs
  const customised = !!prefs?.visible
  const chosen = customised ? new Set<ColumnId>([...PINNED, ...normalizeColumnIds(prefs!.visible!)]) : new Set(automaticColumns(tableWidth, options.present))
  if (options.costAllowed === false) chosen.delete('list_cost')
  let ids = orderOf(prefs).filter(id => chosen.has(id))
  const total = (title: number) => ids.reduce((sum, id) => sum + (id === 'title' ? title : widthOf(id, prefs)), 0)
  // Columns a wide table adds on its own leave first when Title would get cramped.
  if (!customised) for (const id of WIDE_EXTRAS) {
    if (total(TITLE_ROOM) <= tableWidth || tableWidth <= 0) break
    ids = ids.filter(x => x !== id)
  }
  // Phone cards grow downward; desktop columns give way beside Title's minimum.
  const fit = options.phone ? { ids, dropped: [] } : giveWay(ids, tableWidth, options.fit)
  return { columns: fit.ids.map(id => COLUMN_BY_ID.get(id)!), customised, dropped: fit.dropped }
}

const sumWidths = (widths: Partial<Record<string, number>>) => Object.values(widths).reduce<number>((sum, w) => sum + (w ?? 0), 0)
// A loaded measurement can be narrower than the column's designed width, and
// the exact pixels move with the platform font (the model slot measured 152px
// here and 156px in CI, both inside the 176px design, with nothing clipped).
// Keep the design until the value is wider. Title's minimum may still shrink
// the result later; a saved or dragged width never comes through here.
export function loadedFitWidth(def: { width: number; min: number; max: number }, measured: number): number {
  if (!Number.isFinite(measured)) return def.width
  return Math.max(def.width, Math.min(def.max, Math.max(def.min, measured)))
}
// The column after Title first, then the rest toward the left, so Title's edge
// resizes its neighbour before it touches a column further away.
function besideTitle(ids: string[]): string[] {
  const at = ids.indexOf('title')
  const after = at < 0 ? [] : ids.slice(at + 1)
  const before = at < 0 ? ids : ids.slice(0, at)
  return [...after, ...before].filter(id => id !== 'title')
}
function sizedWidth(id: string, prefs: ListPrefs | null | undefined, live: Partial<Record<string, number>>): boolean {
  return live[id] !== undefined || Number.isFinite(prefs?.widths?.[id])
}
// Move flexible columns by `amount` px, never past min or max, the column beside
// Title first.
function shiftColumns(ids: string[], out: Partial<Record<string, number>>, amount: number, dir: 'grow' | 'shrink', flexible: (id: string) => boolean, definitions: ReadonlyMap<string, ColumnDef>) {
  let left = Math.max(0, Math.round(amount))
  for (const id of besideTitle(ids)) {
    if (left <= 0 || !flexible(id)) continue
    const def = definitions.get(id)!
    const current = out[id]!
    const next = dir === 'shrink' ? Math.max(def.min, current - left) : Math.min(def.max, current + left)
    left -= Math.abs(next - current)
    out[id] = next
  }
}

// Title never goes under its minimum while the columns beside it can give way
// (AEON-1470): widths nobody set shrink first, the column beside Title first;
// then saved or dragged widths shrink in proportion to their room above their
// own minimum. The saved values stay as they are; only this layout narrows them.
function keepTitleMin(ids: string[], out: Partial<Record<string, number>>, tableWidth: number, sized: (id: string) => boolean, definitions: ReadonlyMap<string, ColumnDef>) {
  if (!(tableWidth > 0)) return
  const room = tableWidth - definitions.get('title')!.min
  if (sumWidths(out) - room > 0.5) shiftColumns(ids, out, sumWidths(out) - room, 'shrink', id => !sized(id), definitions)
  const over = sumWidths(out) - room
  if (over <= 0.5) return
  const slack = ids.filter(id => id !== 'title' && sized(id)).map(id => [id, Math.max(0, out[id]! - definitions.get(id)!.min)] as const)
  const total = slack.reduce((sum, [, value]) => sum + value, 0)
  if (total <= 0) return
  const share = Math.min(1, over / total)
  for (const [id, value] of slack) out[id] = out[id]! - value * share
}

// How wide Title can be while every other column stays inside its min and max.
// Columns the person already sized keep that width, so they never collapse.
export function titleRoom(ids: string[], tableWidth: number, prefs?: ListPrefs | null, live: Partial<Record<string, number>> = {}, definitions: ReadonlyMap<string, ColumnDef> = COLUMN_BY_ID): { min: number; max: number } {
  const def = definitions.get('title')!
  let fixed = 0, flexMin = 0, flexMax = 0
  for (const id of ids) {
    if (id === 'title') continue
    const col = definitions.get(id)!
    if (sizedWidth(id, prefs, live)) fixed += live[id] ?? widthOf(id, prefs, definitions)
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
// Whatever would leave Title under its minimum gives way in keepTitleMin.
// A width the person gives Title is kept: the unsized columns beside it grow or
// shrink, the next column first, and stop at their own min and max.
export function layoutWidths(ids: string[], tableWidth: number, prefs?: ListPrefs | null, live: Partial<Record<string, number>> = {}, definitions: ReadonlyMap<string, ColumnDef> = COLUMN_BY_ID, automatic: Partial<Record<string, number>> = {}): Partial<Record<string, number>> {
  const out: Partial<Record<string, number>> = {}
  for (const id of ids) if (id !== 'title') out[id] = live[id] ?? (Number.isFinite(prefs?.widths?.[id]) ? widthOf(id, prefs, definitions) : automatic[id] ?? widthOf(id, prefs, definitions))
  const sized = (id: string) => sizedWidth(id, prefs, live)
  if (sized('title')) {
    const target = live.title ?? widthOf('title', prefs, definitions)
    const spare = tableWidth - sumWidths(out) - target
    const flexible = (id: string) => !sized(id)
    if (spare < -0.5) shiftColumns(ids, out, -spare, 'shrink', flexible, definitions)
    else if (spare > 0.5) shiftColumns(ids, out, spare, 'grow', flexible, definitions)
    keepTitleMin(ids, out, tableWidth, sized, definitions)
    for (const id of Object.keys(out) as string[]) out[id] = Math.floor(out[id]!)
    return out
  }
  // A content fit replaces the default width budgeted beside Title. A fit that
  // already leaves Title at least its minimum stays steady.
  keepTitleMin(ids, out, tableWidth, sized, definitions)
  const titleTarget = TITLE_TARGET
  let spare = tableWidth - sumWidths(out) - titleTarget
  // Loaded-content fits stay at the width just chosen, including a shrink that
  // keeps Title at its minimum. An explicit Title resize may still move these
  // unsaved widths, while the person's saved widths remain fixed.
  const growing = GROWS.filter(id => ids.includes(id) && !sized(id) && automatic[id] === undefined)
  while (spare >= 1 && growing.length) {
    const weight = growing.reduce((sum, id) => sum + definitions.get(id)!.width, 0)
    let used = 0
    for (const id of [...growing]) {
      const def = definitions.get(id)!
      const add = Math.min(def.max - out[id]!, spare * def.width / weight)
      out[id] = out[id]! + add
      used += add
      if (out[id]! >= def.max - 0.5) growing.splice(growing.indexOf(id), 1)
    }
    spare -= used
    if (used < 0.5) break
  }
  for (const id of Object.keys(out) as string[]) out[id] = Math.floor(out[id]!)
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

// Host columns are additive, ordered and unique; built-in definitions always win.
export function withHostColumns(base: readonly ColumnDef[], extra: readonly ColumnDef[] = []): ColumnDef[] {
  const seen = new Set<string>(COLUMN_BY_ID.keys())
  return [...base, ...extra.filter(column => {
    if (seen.has(column.id) || !/^[a-z][a-z0-9_-]{0,63}$/.test(column.id) || Object.hasOwn(Object.prototype, column.id)) return false
    seen.add(column.id)
    return true
  })]
}
