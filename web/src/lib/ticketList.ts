// SPDX-License-Identifier: AGPL-3.0-only
// The ticket list's view state lives in the URL query so reloads, deep links
// and browser history keep it, and a saved view is that same state with a name.
// These helpers translate between the query, the list API parameters, saved
// views and the grouped rows; they are pure for unit tests.
//
// Filter values may carry a leading "!" (excluded). Within one filter the plain
// values are alternatives and every excluded value must not match; filters
// combine with AND. That is classic Paimos's model, and the list API's.
import { HIDE_STATES, hiddenStates, parseHiddenStates, type HideState } from './hideStates.ts'
import { estimateHours } from './estimates.ts'
import { compareModelSort, planningSortValue } from './planning.ts'
import type { Facets, ListItem, ListQuery, WorkCountBucket } from './api.ts'
import { normalizeColumnIds, PINNED, type ColumnId } from './columns.ts'
import { DEFAULT_SORT, KINDS, PRIORITIES, kindLabel, normaliseState, parseSort, priorityLabel, serializeSort, statusMeta, statusOptions, type SortKey } from './work.ts'

export type Dimension = 'status' | 'priority' | 'assignee' | 'type' | 'tag' | 'epic' | 'cost' | 'release' | 'human_check' | 'shape' | 'depth'
export type GroupBy = 'none' | 'status' | 'assignee' | 'priority' | 'type' | 'epic' | 'tag'
export type DateField = 'updated' | 'created' | 'start' | 'end' | 'accepted'
export type DatePreset = 'today' | '7d' | '30d' | '90d' | 'month' | 'year'
// A date filter: one field, and a relative preset (kept relative in saved views)
// or a custom range of calendar days (inclusive, either end open).
export interface DateFilter { field: DateField; preset: DatePreset | null; from: string | null; to: string | null }
export interface FacetOption { value: string; label: string; count?: number; hint?: string; color?: string }
export interface ListFilters {
  mode: 'list' | 'outline' | 'graph'
  q: string
  status: string[]
  // Header filters use canonical statuses or exact server work buckets. Kept
  // with URL/saved-view state; ordinary facet edits return to exact spellings.
  statusScope?: 'canonical' | 'open' | 'in_progress' | 'done' | 'closed'
  priority: string[]
  assignee: string[]
  type: string[]
  tag: string[]
  epic: string[]
  cost: string[]
  release: string[]
  human_check: string[]
  shape: string[]
  depth: string[]
  date: DateFilter | null
  showClosed: boolean
  hideStates?: HideState[]
  // A temporary status-selection override, distinct from a manual Hide toggle.
  hideRestore?: boolean
  sort: SortKey[]
  group: GroupBy
  // An explicit column set (free columns in order; Key and Title always lead).
  cols: ColumnId[] | null
  // The saved view the state came from.
  view: string | null
}
export interface DimensionDef { key: Dimension; title: string; facet: string | null; primary: boolean; none: string }
export const DIMENSIONS: DimensionDef[] = [
  { key: 'status', title: 'Status', facet: 'state', primary: true, none: '' },
  { key: 'priority', title: 'Priority', facet: 'priority', primary: true, none: 'No priority' },
  { key: 'assignee', title: 'Assignee', facet: 'assignee', primary: true, none: 'Unassigned' },
  { key: 'shape', title: 'Parents / Leaves', facet: 'shape', primary: true, none: '' },
  { key: 'depth', title: 'Depth', facet: 'depth', primary: false, none: '' },
  { key: 'type', title: 'Legacy type', facet: 'kind', primary: true, none: '' },
  { key: 'tag', title: 'Labels', facet: 'tag', primary: false, none: 'No labels' },
  { key: 'human_check', title: 'Human check', facet: 'human_check', primary: false, none: 'No human check' },
  { key: 'epic', title: 'Parent', facet: null, primary: false, none: 'No parent' },
  { key: 'cost', title: 'Cost unit', facet: 'cost_unit', primary: false, none: 'No cost unit' },
  // Imported fields.release only. The ticket Release column reads native release membership.
  { key: 'release', title: 'Imported release', facet: 'release', primary: false, none: 'No imported release' },
]
export const DIMENSION_BY_KEY = new Map(DIMENSIONS.map(d => [d.key, d]))
export const DIMENSION_KEYS = DIMENSIONS.map(d => d.key)
// The counts every page carries; the others are asked for when a menu opens.
export const LIST_FACETS = ['state', 'priority', 'assignee', 'kind']
export const WORK_KINDS = ['work', 'ticket', 'task', 'epic']
export const PAGE_SIZE = 200
export const GROUPS: { value: GroupBy; label: string }[] = [
  { value: 'none', label: 'None' }, { value: 'status', label: 'Status' }, { value: 'assignee', label: 'Assignee' }, { value: 'priority', label: 'Priority' },
  { value: 'type', label: 'Type' }, { value: 'epic', label: 'Epic' }, { value: 'tag', label: 'Label' },
]
export const DATE_FIELDS: { value: DateField; label: string }[] = [
  { value: 'updated', label: 'Updated' }, { value: 'created', label: 'Created' }, { value: 'start', label: 'Start' }, { value: 'end', label: 'End' }, { value: 'accepted', label: 'Accepted' },
]
export const DATE_PRESETS: { value: DatePreset; label: string }[] = [
  { value: 'today', label: 'Today' }, { value: '7d', label: 'Last 7 days' }, { value: '30d', label: 'Last 30 days' },
  { value: '90d', label: 'Last 90 days' }, { value: 'month', label: 'This month' }, { value: 'year', label: 'This year' },
]

// ---------- Values: included and excluded ----------
export const isExcluded = (value: string) => value.startsWith('!')
export const bare = (value: string) => value.replace(/^!/, '')
export function included(values: string[]): string[] { return values.filter(v => !isExcluded(v)) }
export function excluded(values: string[]): string[] { return values.filter(isExcluded).map(bare) }
export type ValueState = 'in' | 'out' | null
export function valueState(values: string[], value: string): ValueState {
  return values.includes(value) ? 'in' : values.includes(`!${value}`) ? 'out' : null
}
// Include (or stop including) a value; an excluded value becomes included.
export function toggleIn(values: string[], value: string): string[] {
  const rest = values.filter(v => bare(v) !== value)
  return valueState(values, value) === 'in' ? rest : [...rest, value]
}
export function toggleOut(values: string[], value: string): string[] {
  const rest = values.filter(v => bare(v) !== value)
  return valueState(values, value) === 'out' ? rest : [...rest, `!${value}`]
}

// ---------- URL ----------
function list(value: unknown): string[] {
  const raw = Array.isArray(value) ? value.join(',') : typeof value === 'string' ? value : ''
  return [...new Set(raw.split(',').map(part => part.trim()).filter(part => part && part !== '!'))]
}
const DATE_RE = /^\d{4}-\d{2}-\d{2}$/
export function parseDate(raw: unknown): DateFilter | null {
  if (typeof raw !== 'string') return null
  const [field, rest = ''] = raw.split(':')
  if (!DATE_FIELDS.some(d => d.value === field)) return null
  const preset = DATE_PRESETS.find(p => p.value === rest)?.value ?? null
  if (preset) return { field: field as DateField, preset, from: null, to: null }
  const [from = '', to = ''] = rest.split('..')
  return { field: field as DateField, preset: null, from: DATE_RE.test(from) ? from : null, to: DATE_RE.test(to) ? to : null }
}
export function serializeDate(date: DateFilter): string {
  if (date.preset) return `${date.field}:${date.preset}`
  if (!date.from && !date.to) return date.field
  return `${date.field}:${date.from ?? ''}..${date.to ?? ''}`
}
function parseCols(raw: unknown): ColumnId[] | null {
  if (typeof raw !== 'string') return null
  const valid = normalizeColumnIds(list(raw))
  return valid.length ? valid.filter(id => !PINNED.includes(id)) : null
}
const VIEW_ID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i
export function filtersFromQuery(query: Record<string, unknown>): ListFilters {
  const group = GROUPS.find(g => g.value === query.group)?.value ?? 'none'
  return {
    mode: query.view === 'outline' || query.view === 'graph' ? query.view : 'list',
    q: typeof query.q === 'string' ? query.q : '',
    status: list(query.status),
    ...(list(query.status).length && ['canonical', 'open', 'in_progress', 'done', 'closed'].includes(String(query.status_scope))
      ? { statusScope: query.status_scope as ListFilters['statusScope'] } : {}),
    priority: list(query.priority),
    assignee: list(query.assignee),
    shape: list(query.shape).filter(v => ['parent', 'leaf'].includes(bare(v))),
    depth: list(query.depth).filter(v => /^[1-9][0-9]{0,4}$/.test(bare(v)) && Number(bare(v)) <= 50000),
    type: list(query.type).filter(kind => WORK_KINDS.includes(bare(kind))),
    tag: list(query.tag),
    epic: list(query.epic).filter(value => /^[\w-]{1,64}$/.test(bare(value))),
    cost: list(query.cost),
    release: list(query.release),
    human_check: list(query.human_check).filter(value => ['pending', 'none'].includes(bare(value))),
    date: parseDate(query.date),
    showClosed: query.closed === '1',
    ...(typeof query.hide_states === 'string' ? { hideStates: parseHiddenStates(query.hide_states) } : {}),
    ...(query.hide_restore === '1' && query.closed === '1' && list(query.status).length ? { hideRestore: true } : {}),
    sort: parseSort(typeof query.sort === 'string' ? query.sort : ''),
    group,
    cols: parseCols(query.cols),
    view: typeof query.v === 'string' && VIEW_ID.test(query.v) ? query.v : null,
  }
}
export function filtersToQuery(filters: ListFilters): Record<string, string> {
  const out: Record<string, string> = {}
  if (filters.status.length && filters.statusScope) out.status_scope = filters.statusScope
  if (filters.q.trim()) out.q = filters.q.trim()
  for (const key of DIMENSION_KEYS) if (filters[key].length) out[key] = filters[key].join(',')
  if (filters.date) out.date = serializeDate(filters.date)
  if (hiddenStates(filters.hideStates).length !== HIDE_STATES.length) out.hide_states = hiddenStates(filters.hideStates).join(',')
  if (filters.showClosed) out.closed = '1'
  if (filters.showClosed && filters.hideRestore && filters.status.length) out.hide_restore = '1'
  if (filters.sort.length) out.sort = serializeSort(filters.sort)
  if (filters.group !== 'none') out.group = filters.group
  if (filters.cols) out.cols = filters.cols.length ? filters.cols.join(',') : PINNED.join(',')
  if (filters.mode !== 'list') out.view = filters.mode
  if (filters.view) out.v = filters.view
  return out
}
export const EMPTY_FILTERS: ListFilters = filtersFromQuery({})

export function activeDimensions(filters: ListFilters): Dimension[] {
  return DIMENSION_KEYS.filter(key => filters[key].length > 0)
}
export function hasFilters(filters: ListFilters): boolean {
  return !!filters.q.trim() || activeDimensions(filters).length > 0 || !!filters.date
}
// Everything a person can clear with "Clear all": search, filters and the date.
export function clearedFilters(): Partial<ListFilters> {
  return { q: '', status: [], statusScope: undefined, hideRestore: undefined, priority: [], assignee: [], type: [], tag: [], epic: [], cost: [], release: [], human_check: [], shape: [], depth: [], date: null }
}

// ---------- Saved views: the same state, without the view marker ----------
export interface ViewShape { filters: Record<string, string>; sort_keys: string[]; group_by: GroupBy; columns: ColumnId[]; mode: ListFilters['mode'] }
export function viewShape(filters: ListFilters): ViewShape {
  const { sort, group: _group, cols: _cols, v: _v, view: _mode, ...rest } = filtersToQuery(filters)
  // No column choice stays empty and reloads as automatic. An explicit set, including
  // pinned-only, keeps Key and Title so it does not collapse back into the person's columns.
  return { filters: rest, sort_keys: sort ? sort.split(',') : [], group_by: filters.group, columns: filters.cols == null ? [] : [...PINNED, ...filters.cols], mode: filters.mode }
}
export function filtersFromView(view: { id: string; filters: Record<string, unknown>; sort_keys: string[]; group_by: string; columns: string[]; mode?: ListFilters['mode'] }): ListFilters {
  const query: Record<string, unknown> = {}
  for (const [key, value] of Object.entries(view.filters ?? {})) if (typeof value === 'string') query[key] = value
  query.sort = view.sort_keys.join(',')
  query.group = view.group_by
  query.cols = view.columns.join(',')
  query.v = view.id
  query.view = view.mode ?? 'list'
  return filtersFromQuery(query)
}
// The list looks as the view says (a view with changes shows a dot and Save).
export function sameListState(a: ListFilters, b: ListFilters): boolean {
  const x = filtersToQuery({ ...a, view: null }), y = filtersToQuery({ ...b, view: null })
  const keys = new Set([...Object.keys(x), ...Object.keys(y)])
  return [...keys].every(key => normalValues(key, x[key]) === normalValues(key, y[key]))
}
// Filter values are sets: their order does not make a change.
function normalValues(key: string, value: string | undefined): string {
  if (value === undefined) return ''
  return (DIMENSION_KEYS as string[]).includes(key) ? value.split(',').sort().join(',') : value
}
// A name for a new view from what it filters ("Backlog · High · Labels").
export function suggestName(filters: ListFilters, labels: (dimension: Dimension, value: string) => string): string {
  const parts: string[] = []
  for (const key of DIMENSION_KEYS) {
    const values = filters[key]
    if (!values.length) continue
    parts.push(values.length <= 2 ? values.map(v => (isExcluded(v) ? 'not ' : '') + labels(key, bare(v))).join(', ') : DIMENSION_BY_KEY.get(key)!.title)
  }
  if (filters.date) parts.push(`${fieldLabel(filters.date.field)} ${dateLabel(filters.date)}`)
  if (filters.q.trim()) parts.push(`“${filters.q.trim()}”`)
  if (!parts.length && filters.group !== 'none') parts.push(`By ${GROUPS.find(g => g.value === filters.group)!.label.toLowerCase()}`)
  const name = parts.slice(0, 3).join(' · ')
  return name ? (name[0].toUpperCase() + name.slice(1)).slice(0, 80) : 'My view'
}

// ---------- Dates ----------
const pad = (n: number) => String(n).padStart(2, '0')
function dayOf(iso: string): Date { const [y, m, d] = iso.split('-').map(Number); return new Date(y, m - 1, d) }
function addDays(date: Date, days: number): Date { return new Date(date.getFullYear(), date.getMonth(), date.getDate() + days) }
// Local midnight as RFC 3339 with the offset, so the server compares the person's days.
export function localInstant(date: Date): string {
  const offset = -date.getTimezoneOffset()
  const sign = offset >= 0 ? '+' : '-'
  const abs = Math.abs(offset)
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T00:00:00${sign}${pad(Math.floor(abs / 60))}:${pad(abs % 60)}`
}
export function isoDay(date: Date): string { return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}` }
export function dateBounds(date: DateFilter | null, now = new Date()): { from: string | null; to: string | null } | null {
  if (!date) return null
  const today = new Date(now.getFullYear(), now.getMonth(), now.getDate())
  const range = (from: Date | null, to: Date | null) => ({ from: from ? localInstant(from) : null, to: to ? localInstant(to) : null })
  switch (date.preset) {
    case 'today': return range(today, addDays(today, 1))
    case '7d': return range(addDays(today, -6), addDays(today, 1))
    case '30d': return range(addDays(today, -29), addDays(today, 1))
    case '90d': return range(addDays(today, -89), addDays(today, 1))
    case 'month': return range(new Date(today.getFullYear(), today.getMonth(), 1), new Date(today.getFullYear(), today.getMonth() + 1, 1))
    case 'year': return range(new Date(today.getFullYear(), 0, 1), new Date(today.getFullYear() + 1, 0, 1))
  }
  return range(date.from ? dayOf(date.from) : null, date.to ? addDays(dayOf(date.to), 1) : null)
}
const MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec']
export function shortDate(date: Date, now = new Date()): string {
  return `${date.getDate()} ${MONTHS[date.getMonth()]}${date.getFullYear() === now.getFullYear() ? '' : ` ${date.getFullYear()}`}`
}
function shortDay(iso: string, now: Date): string {
  const d = dayOf(iso)
  return `${d.getDate()} ${MONTHS[d.getMonth()]}${d.getFullYear() === now.getFullYear() ? '' : ` ${d.getFullYear()}`}`
}
export function fieldLabel(field: DateField): string { return DATE_FIELDS.find(f => f.value === field)!.label }
export function dateLabel(date: DateFilter, now = new Date()): string {
  if (date.preset) return DATE_PRESETS.find(p => p.value === date.preset)!.label.toLowerCase()
  if (date.from && date.to) return date.from === date.to ? `on ${shortDay(date.from, now)}` : `${shortDay(date.from, now)} – ${shortDay(date.to, now)}`
  if (date.from) return `since ${shortDay(date.from, now)}`
  if (date.to) return `until ${shortDay(date.to, now)}`
  return 'is set'
}

// ---------- The list API ----------
// The server matches states exactly; imported data spells in-progress with a hyphen.
function stateSpellings(states: string[]): string[] {
  const out = new Set<string>()
  for (const state of states) {
    out.add(state)
    const normal = normaliseState(state)
    if (normal === 'in_progress') { out.add('in_progress'); out.add('in-progress') }
  }
  return [...out]
}
// The work kinds to ask for; excluding every kind asks for a kind that does not exist.
function kindsFor(values: string[]): string[] {
  const wanted = included(values).filter(kind => WORK_KINDS.includes(kind))
  const not = new Set(excluded(values))
  const kinds = (wanted.length ? wanted : WORK_KINDS).filter(kind => !not.has(kind))
  return kinds.length ? kinds : ['none']
}
const GROUP_SORT: Partial<Record<GroupBy, SortKey['field']>> = { status: 'state', priority: 'priority', type: 'kind', assignee: 'assignee' }
export function effectiveSort(filters: ListFilters): SortKey[] {
  const keys = filters.sort.length ? [...filters.sort] : [...DEFAULT_SORT]
  // Grouping sorts by its key first, so groups stay together across pages.
  const lead = GROUP_SORT[filters.group]
  if (lead && keys[0]?.field !== lead) {
    const index = keys.findIndex(key => key.field === lead)
    keys.unshift(index >= 0 ? keys.splice(index, 1)[0] : { field: lead, desc: false })
  }
  if (!keys.some(key => key.field === 'updated_at')) keys.push({ field: 'updated_at', desc: true })
  return keys
}
export function apiParams(within: string, filters: ListFilters, options: { omit?: Dimension; facets?: string[]; limit?: number; cursor?: string; now?: Date } = {}): ListQuery {
  const { omit } = options
  const take = (dimension: Dimension) => omit === dimension ? [] : filters[dimension]
  const status = take('status')
  const bounds = dateBounds(filters.date, options.now)
  return {
    within,
    shape: take('shape'), depth: take('depth'),
    kind: omit === 'type' ? WORK_KINDS : [...kindsFor(filters.type), ...excluded(filters.type).map(kind => `!${kind}`)],
    state: filters.statusScope && omit !== 'status' ? [] : [...stateSpellings(included(status)), ...stateSpellings(excluded(status)).map(s => `!${s}`)],
    ...(status.length && filters.statusScope === 'canonical' ? { work_state: status } : {}),
    ...(status.length && filters.statusScope && filters.statusScope !== 'canonical'
      ? { work_bucket: filters.statusScope === 'closed' ? ['cancelled', 'archived'] as WorkCountBucket[] : [filters.statusScope] } : {}),
    priority: take('priority'),
    assignee: take('assignee'),
    tag: take('tag'),
    epic: take('epic'),
    cost_unit: take('cost'),
    release: take('release'),
    ...(take('human_check').length ? { human_check: take('human_check') } : {}),
    ...(filters.date ? { date_field: filters.date.field, date_from: bounds?.from ?? undefined, date_to: bounds?.to ?? undefined } : {}),
    q: filters.q.trim(),
    hide_closed: !filters.showClosed,
    ...(hiddenStates(filters.hideStates).length !== HIDE_STATES.length ? { hide_states: hiddenStates(filters.hideStates) } : {}),
    facets: options.facets,
    sort: serializeSort(effectiveSort(filters)),
    limit: options.limit ?? PAGE_SIZE,
    cursor: options.cursor,
  }
}

export function totalFrom(facets: Facets | undefined): number | null {
  const counts = facets?.state ?? facets?.kind
  return counts ? Object.values(counts).reduce((sum, count) => sum + count, 0) : null
}

// ---------- Facet options ----------
export interface EpicOption { id: string; key: string; title: string; state?: string }
export interface OptionContext { names?: Map<string, string>; me?: string; colors?: Map<string, string>; epics?: EpicOption[]; workName?: string }
// Labels (tags, cost units, releases) are counted by spelling; one label is one option.
function labelOptions(dimension: Dimension, counts: Record<string, number>, selected: string[], colors: Map<string, string> = new Map()): FacetOption[] {
  const byName = new Map<string, FacetOption>()
  for (const [value, count] of Object.entries(counts)) {
    if (value === 'none') continue
    const key = value.toLowerCase()
    const existing = byName.get(key)
    if (existing) existing.count = (existing.count ?? 0) + count
    else byName.set(key, { value, label: value, count, color: colors.get(key) })
  }
  for (const value of selected.map(bare)) {
    if (value !== 'none' && !byName.has(value.toLowerCase())) byName.set(value.toLowerCase(), { value, label: value, count: 0, color: colors.get(value.toLowerCase()) })
  }
  const options = [...byName.values()].sort((a, b) => (b.count ?? 0) - (a.count ?? 0) || a.label.localeCompare(b.label))
  return [{ value: 'none', label: DIMENSION_BY_KEY.get(dimension)!.none, count: counts.none ?? 0 }, ...options]
}
export function facetOptions(dimension: Dimension, counts: Record<string, number> = {}, selectedValues: string[] = [], names: Map<string, string> = new Map(), me?: string, context: OptionContext = {}): FacetOption[] {
  const selected = selectedValues.map(bare)
  if (dimension === 'human_check') return [{ value: 'pending', label: 'Needs a human check', count: counts.pending ?? 0 }, { value: 'none', label: 'No human check', count: counts.none ?? 0 }]
  if (dimension === 'status') {
    const byKey = new Map<string, FacetOption>()
    const seen = new Set<string>()
    const add = (value: string) => {
      if (seen.has(value)) return
      seen.add(value)
      const meta = statusMeta(value)
      const key = meta.key === 'other' ? normaliseState(value) : meta.key
      const existing = byKey.get(key)
      const count = counts[value] ?? 0
      // Spellings of one status (in_progress, in-progress) share one option; the
      // option keeps a selected spelling, else the one the data uses.
      if (existing) {
        existing.count = (existing.count ?? 0) + count
        if (counts[value] !== undefined && !selected.includes(existing.value)) existing.value = value
        return
      }
      byKey.set(key, { value, label: meta.label, count })
    }
    for (const option of statusOptions(Object.keys(counts))) add(option.value)
    for (const value of [...Object.keys(counts), ...selected]) add(value)
    return [...byKey.values()].sort((a, b) => statusMeta(a.value).order - statusMeta(b.value).order)
  }
  if (dimension === 'priority') {
    return [...PRIORITIES.map(p => ({ value: p.value as string, label: p.label as string })), { value: 'none', label: 'No priority' }]
      .map(option => ({ ...option, count: counts[option.value] ?? 0 }))
  }
  if (dimension === 'shape') return [{ value: 'parent', label: 'Parents', count: counts.parent ?? 0 }, { value: 'leaf', label: 'Leaves', count: counts.leaf ?? 0 }]
  if (dimension === 'depth') return [...new Set([...Object.keys(counts), ...selected, '1', '2', '3'])].sort((a,b) => Number(a)-Number(b)).map(value => ({ value, label: `Level ${value}`, count: counts[value] ?? 0 }))
  if (dimension === 'type') return KINDS.map(kind => ({ value: kind.value, label: kind.value === 'work' && context.workName ? context.workName : kind.label, count: counts[kind.value] ?? 0 }))
  if (dimension === 'tag' || dimension === 'cost' || dimension === 'release') return labelOptions(dimension, counts, selectedValues, context.colors)
  if (dimension === 'epic') {
    const epics = context.epics ?? []
    const known = new Set(epics.map(e => e.id))
    return [
      { value: 'none', label: 'No parent' },
      ...epics.map(e => ({ value: e.id, label: e.title, hint: e.key })),
      ...selected.filter(id => id !== 'none' && !known.has(id)).map(id => ({ value: id, label: names.get(id) ?? 'Parent', hint: '' })),
    ]
  }
  const ids = [...new Set([...Object.keys(counts), ...selected])].filter(id => id !== 'none')
  ids.sort((a, b) => (a === me ? -1 : b === me ? 1 : (counts[b] ?? 0) - (counts[a] ?? 0)))
  return [
    { value: 'none', label: 'Unassigned', count: counts.none ?? 0 },
    ...ids.map(id => ({ value: id, label: names.get(id) ?? 'Unknown person', count: counts[id] ?? 0 })),
  ]
}
// The words a chip or a view name uses for one value.
export function valueLabel(dimension: Dimension, value: string, context: OptionContext = {}): string {
  if (value === 'none' && DIMENSION_BY_KEY.get(dimension)!.none) return DIMENSION_BY_KEY.get(dimension)!.none
  switch (dimension) {
    case 'human_check': return value === 'pending' ? 'Needs a human check' : 'No human check'
    case 'status': return value === 'queued' ? 'Queued' : statusMeta(value).label
    case 'priority': return priorityLabel(value)
    case 'shape': return value === 'parent' ? 'Parents' : 'Leaves'
    case 'depth': return `Level ${value}`
    case 'type': return value === 'work' && context.workName ? context.workName : kindLabel(value)
    case 'assignee': return value === context.me ? 'Me' : context.names?.get(value) ?? 'Someone'
    case 'epic': return context.epics?.find(e => e.id === value)?.title ?? context.names?.get(value) ?? 'A parent'
    default: return value
  }
}

// ---------- Rows and groups ----------
// Client-side ordering with the same keys as the list API, for siblings in the Outline
// that come from different requests (matches and their ancestors).
const PRIORITY_RANK: Record<string, number> = { high: 0, medium: 1, low: 2 }
function lessThan(x: string | number | bigint, y: string | number | bigint): boolean {
  if (typeof x === 'bigint' && typeof y === 'bigint') return x < y
  if (typeof x === 'number' && typeof y === 'number') return x < y
  if (typeof x === 'string' && typeof y === 'string') return x < y
  return false
}
export function compareRows(keys: SortKey[]): (a: ListItem, b: ListItem) => number {
  const value = (row: ListItem, field: SortKey['field']): string | number | bigint => {
    switch (field) {
      case 'state': return statusMeta(row.state).order
      case 'priority': return PRIORITY_RANK[row.priority ?? ''] ?? 3
      case 'updated_at': return Date.parse(row.updated_at) || 0
      case 'created_at': return Date.parse(row.created_at) || 0
      case 'key': { const [prefix, number = ''] = row.key.split('-'); return `${prefix}-${number.padStart(9, '0')}` }
      case 'title': return row.title.toLowerCase()
      case 'kind': return row.kind_slug
      case 'assignee': return row.assignee?.name.toLowerCase() ?? ''
      case 'estimate': return estimateHours(row) ?? 0
      case 'eta_ready': return row.eta?.eta_ready_at ? Date.parse(row.eta.eta_ready_at) : 0
      case 'progress': return typeof row.eta?.progress_pct === 'number' ? row.eta.progress_pct : 0
      case 'model': case 'tokens': case 'list_cost': case 'paid': return planningSortValue(row, field) ?? 0
    }
  }
  const missing = (row: ListItem, field: SortKey['field']) => field === 'estimate' ? estimateHours(row) === null : field === 'eta_ready' ? !row.eta?.eta_ready_at : field === 'progress' ? typeof row.eta?.progress_pct !== 'number'
    : field === 'model' || field === 'tokens' || field === 'list_cost' || field === 'paid' ? planningSortValue(row, field) === null : false
  return (a, b) => {
    for (const key of keys) {
      if (key.field === 'model') {
        const delta = compareModelSort(a, b, key.desc)
        if (delta !== 0) return delta
        continue
      }
      // Unassigned work, estimates nobody reported, and unknown states come last in both directions.
      if (key.field === 'assignee' && !a.assignee !== !b.assignee) return a.assignee ? -1 : 1
      if (key.field === 'state') {
        const unknownA = statusMeta(a.state).key === 'other'
        const unknownB = statusMeta(b.state).key === 'other'
        if (unknownA !== unknownB) return unknownA ? 1 : -1
      }
      if (missing(a, key.field) !== missing(b, key.field)) return missing(a, key.field) ? 1 : -1
      const x = value(a, key.field), y = value(b, key.field)
      if (x !== y) return (lessThan(x, y) ? -1 : 1) * (key.desc ? -1 : 1)
    }
    return a.id === b.id ? 0 : a.id < b.id ? -1 : 1
  }
}

// Stable status ordering by workflow. An unknown spelling stays after that
// workflow in both directions, matching the list API. layout gives the values
// a row is placed by (a live list holds them while an update waits).
export function orderByStatus(rows: ListItem[], desc = false, layout: (row: ListItem) => ListItem = row => row): ListItem[] {
  return rows
    .map((row, index) => ({ row, index, meta: statusMeta(layout(row).state) }))
    .sort((a, b) => {
      if ((a.meta.key === 'other') !== (b.meta.key === 'other')) return a.meta.key === 'other' ? 1 : -1
      return (desc ? b.meta.order - a.meta.order : a.meta.order - b.meta.order) || a.index - b.index
    })
    .map(entry => entry.row)
}

export interface EpicRef { id: string; key: string; title: string }
export function epicOf(row: ListItem, byId: Map<string, ListItem>): EpicRef | null {
  if (row.kind_slug === 'epic') return { id: row.id, key: row.key, title: row.title }
  if (row.epic !== undefined) return row.epic
  let parent = row.parent
  for (let depth = 0; parent && depth < 8; depth++) {
    if (['work','epic'].includes(parent.kind_slug)) return { id: parent.id, key: parent.key, title: parent.title }
    const loaded = byId.get(parent.id)
    parent = loaded?.parent ?? null
  }
  return null
}

// The tags of a row as the table shows them (classic imports keep them in fields).
export function rowTags(row: ListItem): { name: string; color: string }[] {
  const value = row.fields?.tags
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

export interface RowGroup {
  key: string; label: string; rows: ListItem[]; total: number
  state?: string; epic?: EpicRef; person?: { id: string; name: string }; priority?: string; kind?: string; tag?: { name: string; color: string }
}
// Counts are the list API's facet for the grouped dimension (state, assignee,
// priority, kind or tag), so a group shows its whole size while pages load.
// layout gives the values a row is grouped by (a live list holds them while an
// update waits); the group still lists the row itself.
export function groupRows(rows: ListItem[], group: GroupBy, counts: Record<string, number> = {}, options: { me?: string; layout?: (row: ListItem) => ListItem; workName?: string } = {}): RowGroup[] {
  if (group === 'none') return [{ key: 'all', label: '', rows, total: rows.length }]
  const layout = options.layout ?? (row => row)
  if (group === 'status') {
    const groups = new Map<string, RowGroup>()
    for (const row of rows) {
      const state = layout(row).state
      const meta = statusMeta(state)
      const key = meta.key === 'other' ? normaliseState(state) : meta.key
      if (!groups.has(key)) groups.set(key, { key, label: meta.label, state, rows: [], total: 0 })
      groups.get(key)!.rows.push(row)
    }
    for (const entry of groups.values()) {
      const counted = Object.entries(counts).filter(([state]) => {
        const meta = statusMeta(state)
        return (meta.key === 'other' ? normaliseState(state) : meta.key) === entry.key
      }).reduce((sum, [, count]) => sum + count, 0)
      entry.total = Math.max(counted, entry.rows.length)
    }
    return [...groups.values()].sort((a, b) => statusMeta(a.state!).order - statusMeta(b.state!).order)
  }
  if (group === 'epic') {
    const byId = new Map(rows.map(row => [row.id, row]))
    const groups = new Map<string, RowGroup>()
    const none: RowGroup = { key: 'none', label: 'No parent', rows: [], total: 0 }
    for (const row of rows) {
      const placed = layout(row)
      const epic = epicOf(placed, byId)
      if (!epic) { none.rows.push(row); continue }
      if (!groups.has(epic.id)) groups.set(epic.id, { key: epic.id, label: epic.title, epic, rows: [], total: 0 })
      if (placed.kind_slug !== 'epic') groups.get(epic.id)!.rows.push(row)
    }
    const out = [...groups.values(), ...(none.rows.length ? [none] : [])]
    for (const entry of out) entry.total = entry.rows.length
    return out
  }
  const groups = new Map<string, RowGroup>()
  const put = (key: string, make: () => Omit<RowGroup, 'rows' | 'total'>, row: ListItem) => {
    if (!groups.has(key)) groups.set(key, { ...make(), rows: [], total: 0 })
    groups.get(key)!.rows.push(row)
  }
  for (const row of rows) {
    const placed = layout(row)
    if (group === 'assignee') {
      const person = placed.assignee
      if (person) put(person.id, () => ({ key: person.id, label: person.name, person }), row)
      else put('none', () => ({ key: 'none', label: 'Unassigned' }), row)
    } else if (group === 'priority') {
      const value = placed.priority && placed.priority !== 'none' ? placed.priority : 'none'
      put(value, () => ({ key: value, label: priorityLabel(value), priority: value }), row)
    } else if (group === 'type') {
      put(placed.kind_slug, () => ({ key: placed.kind_slug, label: placed.kind_slug === 'work' && options.workName ? options.workName : kindLabel(placed.kind_slug), kind: placed.kind_slug }), row)
    } else {
      // A ticket with several labels shows under each of them.
      const tags = rowTags(placed)
      if (!tags.length) put('none', () => ({ key: 'none', label: 'No labels' }), row)
      for (const tag of new Map(tags.map(t => [t.name.toLowerCase(), t])).values()) put(tag.name.toLowerCase(), () => ({ key: tag.name.toLowerCase(), label: tag.name, tag }), row)
    }
  }
  const lower = new Map<string, number>()
  for (const [value, count] of Object.entries(counts)) lower.set(group === 'tag' ? value.toLowerCase() : value, (lower.get(group === 'tag' ? value.toLowerCase() : value) ?? 0) + count)
  const out = [...groups.values()]
  for (const entry of out) entry.total = Math.max(lower.get(entry.key) ?? 0, entry.rows.length)
  const rank = (entry: RowGroup): [number, string | number] => {
    if (entry.key === 'none') return [9, '']
    if (group === 'assignee') return [entry.key === options.me ? 0 : 1, entry.label.toLowerCase()]
    if (group === 'priority') return [1, PRIORITY_RANK[entry.key] ?? 3]
    if (group === 'type') return [1, WORK_KINDS.indexOf(entry.key) === -1 ? 9 : ['work', 'epic', 'ticket', 'task'].indexOf(entry.key)]
    return [1, entry.label.toLowerCase()]
  }
  return out.sort((a, b) => {
    const [x1, x2] = rank(a), [y1, y2] = rank(b)
    return x1 - y1 || (x2 < y2 ? -1 : x2 > y2 ? 1 : 0)
  })
}
// The facet whose counts give a grouping its totals.
export function groupFacet(group: GroupBy): string | null {
  return ({ status: 'state', assignee: 'assignee', priority: 'priority', type: 'kind', tag: 'tag' } as Partial<Record<GroupBy, string>>)[group] ?? null
}
