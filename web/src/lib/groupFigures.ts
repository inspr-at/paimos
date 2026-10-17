// SPDX-License-Identifier: AGPL-3.0-only
// Group summaries (AEON-1388): what each group of a grouped ticket list holds, in its own columns, without opening it.
// A group's figures are the header's figures (AEON-1358 on AEON-1368's GET /api/projects/{id}/figures) for the view
// narrowed to that one group: the same server function, the same leaves-only, rounding and formats, never a second
// count in the browser. This module builds the narrowed query, the quiet cell texts and the phone line, and the
// bounded per-scope cache behind them. No Vue here, so tests/group-figures.test.ts runs it under plain Node.
import type { ColumnFigures, ListQuery, ProjectFigures } from './api.ts'
import type { ColumnId } from './columns.ts'
import { formatDollars } from './planning.ts'
import { canonicalStatus } from './projectStatusCounts.ts'
import { apiParams, isExcluded, type GroupBy, type ListFilters, type RowGroup } from './ticketList.ts'
import { normaliseState, statusMeta } from './work.ts'
import { fmtFinish, fmtHours, fmtInt, fmtTokens } from './workFigures.ts'

// The column each grouping fills: it stays empty in the group rows, and its header orders the groups (AEON-1390).
export const GROUP_COLUMN: Partial<Record<GroupBy, ColumnId>> = { status: 'status', priority: 'priority', assignee: 'assignee', tag: 'tags', epic: 'epic' }

// ---------- The view narrowed to one group ----------
// Values of one filter are alternatives, so the group's value replaces the view's own included values of the same
// filter (a group only exists for rows the view matches); exclusions stay. null: the group cannot be counted exactly.
export function groupParams(projectId: string, filters: ListFilters, group: GroupBy, entry: RowGroup): ListQuery | null {
  const params = apiParams(projectId, filters)
  const only = (values: string[] | undefined, value: string) => [...(values ?? []).filter(isExcluded), value]
  switch (group) {
    case 'status': {
      const state = entry.state ?? entry.rows[0]?.state
      if (!state) return null
      const canonical = canonicalStatus(state)
      // An unknown spelling that the server folds into a known status would count that status; leave it out.
      if (statusMeta(state).key === 'other' && canonical !== normaliseState(state)) return null
      return { ...params, work_state: only(params.work_state, canonical) }
    }
    case 'priority': return { ...params, priority: only(params.priority, entry.key) }
    case 'assignee': return { ...params, assignee: only(params.assignee, entry.key) }
    case 'epic': return { ...params, epic: only(params.epic, entry.key) }
    case 'tag': {
      // Several labels per ticket: "has this label and one of the filtered ones" has no query; such a group is not counted.
      const chosen = (params.tag ?? []).filter(value => !isExcluded(value)).map(value => value.toLowerCase())
      if (chosen.length && !chosen.includes(entry.key)) return null
      return { ...params, tag: only(params.tag, entry.key) }
    }
    // The list API's kind filter folds Work into every legacy kind; only Work itself is exact.
    case 'type': return entry.key === 'work' ? { ...params, kind: ['work'] } : null
    default: return null
  }
}
// Why a group has no figures although its view has them (the tag case above).
export const UNCOUNTED_GROUP = 'This group isn’t counted: it needs its own label filter.'

// ---------- Cell texts: quiet, and only when there is something to say (never "—", never 0) ----------
export interface GroupCellContext {
  now: number
  // The grouped-by column stays empty.
  grouped: ColumnId | null
  modelName?: (id: string) => string
}
const PRIORITY_WORDS: [string, string][] = [['high', 'high'], ['medium', 'med'], ['low', 'low']]
// The ticket's own copy: "Sol 6.1 ×5" (a count) and "4 → Next" (the word "to"); text, not icons.
const TIMES = '×'
const TO = '→'
// Tokens and cost: one sum with the header's marks, "~" when estimated rows add to it, "≥" when it is a lower bound.
function usageSum(c: ColumnFigures | undefined, format: (n: number) => string): string {
  const measured = c?.measured, estimated = c?.estimated
  const total = (measured?.total ?? 0) + (estimated?.total ?? 0)
  if (!c || !(total > 0)) return ''
  const shown = format(total)
  return shown ? `${c.partial ? '≥' : ''}${estimated?.count ? '~' : ''}${shown}` : ''
}
const statusWord = (state: string) => { const label = statusMeta(state).label; return label === label.toUpperCase() ? label : label.toLowerCase() }
export function groupCellText(id: string, f: ProjectFigures | null, ctx: GroupCellContext): string {
  if (!f || id === ctx.grouped) return ''
  const c = (key: string) => f.columns[key]
  const top = (key: string) => c(key)?.values?.[0]
  switch (id) {
    case 'status': {
      const values = (c('status')?.values ?? []).filter(v => v.count > 0)
      return values.slice(0, 2).map(v => `${fmtInt(v.count)} ${statusWord(v.value)}`).join(' · ')
    }
    case 'priority': {
      const values = c('priority')?.values ?? []
      return PRIORITY_WORDS.map(([value, word]) => [values.find(v => v.value === value)?.count ?? 0, word] as const).filter(([n]) => n > 0).map(([n, word]) => `${fmtInt(n)} ${word}`).join(' · ')
    }
    case 'estimate': {
      const total = c('estimate')?.numbers?.total ?? 0
      return total > 0 ? `~${fmtHours(total)}` : ''
    }
    case 'tokens': return usageSum(c('tokens'), fmtTokens)
    case 'list_cost': case 'paid': return usageSum(c('cost'), formatDollars)
    case 'model': { const v = top('model'); return v && v.count > 0 ? `${ctx.modelName?.(v.value) ?? v.value} ${TIMES}${fmtInt(v.count)}` : '' }
    case 'tags': { const v = top('tag'); return v && v.count > 0 ? `${v.value} ${fmtInt(v.count)}` : '' }
    case 'suggested': { const v = top('suggested_release'); return v && v.count > 0 ? `${fmtInt(v.count)} ${TO} ${v.value}` : '' }
    case 'eta': { const next = c('eta_ready')?.next_finish; return next ? fmtFinish(next.at, ctx.now) : '' }
    default: return ''
  }
}
// The people assigned in the group ("2 people"), when no session is live. A tail beyond the top three may hold more.
export function peopleText(f: ProjectFigures | null): string {
  const c = f?.columns.assignee
  const people = (c?.values ?? []).filter(v => v.kind === 'person' && v.count > 0).length
  if (!people) return ''
  return `${c!.others > 0 ? '≥' : ''}${fmtInt(people)} ${people === 1 ? 'person' : 'people'}`
}
// Facts from the group's own rows, as the rows show them: a lower bound while the group still has rows to load.
export const rowCount = (n: number, complete: boolean) => `${complete ? '' : '≥'}${fmtInt(n)}`

// The phone line (spec §2): at most three facts, in this order: working, stale, est. Each is [value, word].
export function phoneFacts(facts: { working: string; stale: string; estimate: string }): [string, string][] {
  const all: [string, string][] = [[facts.working, 'working'], [facts.stale, 'stale'], [facts.estimate, '']]
  return all.filter(([value]) => !!value).slice(0, 3)
}
// The group's tooltip title: "In progress · 10 tickets".
export const groupTipTitle = (name: string, tickets: number) => `${name} · ${fmtInt(tickets)} ${tickets === 1 ? 'ticket' : 'tickets'}`

// ---------- The cache and the loader ----------
// One entry per scope key (person + narrowed query), bounded in count; an entry older than maxAge still lands at once
// and is counted again quietly.
export interface FiguresCache {
  get(key: string): { value: ProjectFigures; fresh: boolean } | null
  put(key: string, value: ProjectFigures): void
  forget(match: (key: string) => boolean): void
  clear(): void
  readonly size: number
}
export function figuresCache(options: { maxEntries: number; maxAge: number; now: () => number }): FiguresCache {
  const entries = new Map<string, { value: ProjectFigures; at: number }>()
  return {
    get(key) { const entry = entries.get(key); return entry ? { value: entry.value, fresh: options.now() - entry.at <= options.maxAge } : null },
    put(key, value) {
      entries.delete(key)
      entries.set(key, { value, at: options.now() })
      while (entries.size > options.maxEntries) entries.delete(entries.keys().next().value!)
    },
    forget(match) { for (const key of [...entries.keys()]) if (match(key)) entries.delete(key) },
    clear() { entries.clear() },
    get size() { return entries.size },
  }
}

export interface FiguresRequest { key: string; params: ListQuery }
export interface FiguresLoader {
  // The scopes on screen now. Cached ones land at once; missing or old ones are counted, at most `concurrency` at a
  // time, in the order given. Counts for scopes no longer wanted are aborted, and their answers dropped.
  want(requests: FiguresRequest[]): void
  // The work changed: forget what was counted and count the wanted scopes again, keeping what shows until then.
  refresh(): void
  stop(): void
}
export function figuresLoader(options: {
  cache: FiguresCache
  fetch: (params: ListQuery, signal: AbortSignal) => Promise<ProjectFigures>
  valid: (value: unknown) => value is ProjectFigures
  land: (key: string, value: ProjectFigures) => void
  fail: (key: string, error: unknown) => void
  concurrency: number
}): FiguresLoader {
  let wanted: FiguresRequest[] = []
  const running = new Map<string, AbortController>()
  const queued: string[] = []
  let stopped = false
  const isWanted = (key: string) => wanted.some(request => request.key === key)
  function pump() {
    while (!stopped && running.size < options.concurrency && queued.length) {
      const key = queued.shift()!
      const request = wanted.find(entry => entry.key === key)
      if (!request || running.has(key)) continue
      const controller = new AbortController()
      running.set(key, controller)
      options.fetch(request.params, controller.signal).then(value => {
        if (controller.signal.aborted || stopped) return
        if (!options.valid(value)) throw new Error('figures response has an unexpected shape')
        options.cache.put(key, value)
        if (isWanted(key)) options.land(key, value)
      }).catch(error => {
        if (controller.signal.aborted || stopped || !isWanted(key)) return
        options.fail(key, error)
      }).finally(() => {
        if (running.get(key) === controller) running.delete(key)
        pump()
      })
    }
  }
  function schedule(key: string) { if (!running.has(key) && !queued.includes(key)) queued.push(key) }
  return {
    want(requests) {
      wanted = requests
      for (const [key, controller] of running) if (!isWanted(key)) { controller.abort(); running.delete(key) }
      for (let i = queued.length - 1; i >= 0; i--) if (!isWanted(queued[i])) queued.splice(i, 1)
      for (const { key } of requests) {
        const cached = options.cache.get(key)
        if (cached) options.land(key, cached.value)
        if (!cached || !cached.fresh) schedule(key)
      }
      pump()
    },
    refresh() {
      for (const { key } of wanted) {
        options.cache.forget(entry => entry === key)
        running.get(key)?.abort(); running.delete(key)
        schedule(key)
      }
      pump()
    },
    stop() { stopped = true; for (const controller of running.values()) controller.abort(); running.clear(); queued.length = 0 },
  }
}
// A group's own key, so a cached scope from another project, person or view never lands as this group's.
export const groupScopeKey = (owner: string, path: string) => `${owner} ${path}`
