// SPDX-License-Identifier: AGPL-3.0-only
// Work at a glance (AEON-1358, Tick's approved design r4b): the shared words and numbers of the header work bar and the
// column header figures. The server (AEON-1368, GET /api/projects/{id}/figures) counts every matching row, leaves
// only, Closed out of the hours; this module only formats that one figures object. Hovers read what it returns and
// never compute or fetch. No Vue here, so tests/work-figures.test.ts runs it under plain Node.
//
// Formats (spec §1): durations 40m · 2.4h · 5h · 14h · 1.5d · 12d; hours 7.5h · 728h · 1,029h, with group hours rounded by
// largest remainder so they add up to the shown total, and "~" on totals only; percents whole, adding up to 100;
// tokens 2.74B · 41M · 9.5M · 450K.
import type { ColumnFigures, FigureValue, ProjectFigures } from './api.ts'
import type { ColumnId } from './columns.ts'
import { hiddenStates, hideChoice } from './hideStates.ts'
import { formatDollars } from './planning.ts'
import { fallbackBucket } from './projectStatusCounts.ts'
import { relativeTime, statusMeta } from './work.ts'
import { fill, workFiguresText, type WorkFiguresText } from './workFiguresText.ts'

const nf = new Intl.NumberFormat('en-GB')
export const fmtInt = (n: number) => nf.format(n)
const one = (v: number) => { const r = Math.round(v * 10) / 10; return Number.isInteger(r) ? String(r) : r.toFixed(1) }

// Below 10 h one decimal (".0" dropped); from 10 h whole, with a thousands separator.
export function fmtHours(hours: number): string {
  return hours >= 9.95 ? `${nf.format(Math.round(hours))}h` : `${one(hours)}h`
}
// Under 1 h minutes (at least 1m); 1–9.9 h one decimal; 10–23 h whole; 1–9.9 d one decimal; 10 d and more whole.
export function fmtDuration(minutes: number | null | undefined): string {
  if (minutes == null || !Number.isFinite(minutes)) return '—'
  if (minutes < 59.5) return `${Math.max(1, Math.round(minutes))}m`
  const hours = minutes / 60
  if (hours < 9.95) return `${one(hours)}h`
  if (Math.round(hours) < 24) return `${Math.round(hours)}h`
  const days = minutes / 1440
  return days < 9.95 ? `${one(days)}d` : `${Math.round(days)}d`
}
export function fmtTokens(n: number | null | undefined): string {
  if (n == null || !Number.isFinite(n)) return '—'
  if (n >= 1e9) return `${(n / 1e9).toFixed(n >= 1e10 ? 1 : 2)}B`
  if (n >= 1e6) return n >= 1e7 ? `${Math.round(n / 1e6)}M` : `${one(n / 1e6)}M`
  if (n >= 1e3) return `${Math.round(n / 1e3)}K`
  return String(Math.round(n))
}
export const fmtPct = (value: number) => `${Math.round(value)}%`

// Whole numbers that add up to `total`: floors first, then the largest remainders get the rest.
export function largestRemainder(values: readonly number[], total = 100): number[] {
  const sum = values.reduce((a, b) => a + b, 0)
  if (!sum) return values.map(() => 0)
  const raw = values.map(v => v / sum * total), base = raw.map(Math.floor)
  let left = total - base.reduce((a, b) => a + b, 0)
  raw.map((v, i) => [v - base[i], i] as const).sort((a, b) => b[0] - a[0] || a[1] - b[1]).forEach(([, i]) => { if (left > 0) { base[i]++; left-- } })
  return base
}

const MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec']
// "7 Sep", as the app writes months (never "Sept").
export function shortDate(iso: string): string {
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? '—' : `${d.getDate()} ${MONTHS[d.getMonth()]}`
}
const sameDay = (a: number, b: number) => new Date(a).toDateString() === new Date(b).toDateString()
// "~2 min", "~3.5 h"; a finish already past reads as overdue.
export function fmtFinish(iso: string, now: number): string {
  const minutes = (Date.parse(iso) - now) / 60_000
  if (!Number.isFinite(minutes)) return '—'
  const span = (m: number) => m < 90 ? `${Math.max(1, Math.round(m))} min` : `${one(m / 60)} h`
  return minutes >= 0 ? `~${span(minutes)}` : `overdue ${span(-minutes)}`
}

// The response is the figures object it claims to be; anything else counts as a failed read, never as figures.
export function validFigures(value: unknown): value is ProjectFigures {
  const f = value as ProjectFigures | null
  const num = (n: unknown) => typeof n === 'number' && Number.isFinite(n)
  const obj = (o: unknown) => !!o && typeof o === 'object' && !Array.isArray(o)
  return obj(f) && obj(f!.scope) && obj(f!.groups) && obj(f!.columns) && num(f!.total_hours) && num(f!.without_estimate) && typeof f!.marks_releases === 'boolean'
    && ['whole_project', 'this_view'].includes(f!.scope.kind) && ['rows', 'leaves', 'project_leaves', 'closed'].every(k => num(f!.scope[k as keyof ProjectFigures['scope']]))
    && (['completed', 'done_not_live', 'in_progress', 'open'] as const).every(k => obj(f!.groups[k]) && num(f!.groups[k].hours) && num(f!.groups[k].tickets))
}

// ---------- The four work groups, in bar order ----------
export type GroupId = 'c' | 'd' | 'p' | 'o'
type ServerGroup = keyof ProjectFigures['groups']
const GROUPS: { id: GroupId; key: ServerGroup; states: string[] }[] = [
  { id: 'c', key: 'completed', states: ['delivered', 'accepted'] },
  { id: 'd', key: 'done_not_live', states: ['done'] },
  { id: 'p', key: 'in_progress', states: ['in_progress', 'qa'] },
  { id: 'o', key: 'open', states: ['new', 'backlog', 'open', 'blocked'] },
]
// The bar form of the status icons (styles/status-icons.css, statusFill): Completed is the filled ✓ (solid), Done, not
// live the ✓ in a ring (outline with a light tint), In progress the 45° stripes; Open is the empty track.
export const GROUP_FILL: Record<GroupId, string> = { c: 'f-solid fc-delivered', d: 'f-tint fc-done', p: 'f-diag fc-progress', o: '' }
// A project that doesn't mark releases: Done is the last step, drawn solid.
const DONE_LAST_FILL = 'f-solid fc-done'

export interface WorkGroup {
  id: GroupId; name: string; hours: number; tickets: number; shown: string; hoursShare: number; ticketShare: number; fill: string
  // The statuses inside, for the Status column: [label, count], in status order.
  statuses: [string, number][]
}
export interface WorkScope { narrowed: boolean; label: string; short: string; leaves: number; projectLeaves: number }
export interface WorkHeader {
  key: string; scope: WorkScope; marksReleases: boolean
  hours: number; total: string; tickets: number; closed: number; withoutEstimate: number
  groups: WorkGroup[]
  // Drawn to scale: each segment's width is its share of the track (percent), no gaps, no minimum. Open is the track.
  segments: { id: GroupId; fill: string; left: number; width: number }[]
  accessible: string
}

export function scopeLabel(narrowed: boolean, leaves: number | null, projectLeaves: number | null, text: WorkFiguresText = workFiguresText()): { label: string; short: string } {
  if (!narrowed) return { label: text.wholeProject, short: text.wholeProject }
  return { label: leaves == null || projectLeaves == null ? text.thisView : `${text.thisView} · ${fill(text.ofTickets, { n: fmtInt(leaves), m: fmtInt(projectLeaves) })}`, short: text.thisView }
}

const statusOrder = (status: string) => statusMeta(status).order
export function workHeader(f: ProjectFigures, text: WorkFiguresText = workFiguresText()): WorkHeader {
  const releases = f.marks_releases
  const defs = GROUPS.filter(g => releases || g.id !== 'd')
  const raw = defs.map(g => f.groups[g.key] ?? { hours: 0, tickets: 0, statuses: {} })
  const hours = f.total_hours
  const rounded = hours >= 9.95 ? largestRemainder(raw.map(g => g.hours), Math.round(hours)) : null
  const byHours = largestRemainder(raw.map(g => g.hours)), byCount = largestRemainder(raw.map(g => g.tickets))
  const name = (id: GroupId) => id === 'c' ? (releases ? text.groups.completed : text.groups.done) : id === 'd' ? text.groups.doneNotLive : id === 'p' ? text.groups.inProgress : text.groups.open
  const groups: WorkGroup[] = defs.map((g, i) => ({
    id: g.id, name: name(g.id), hours: raw[i].hours, tickets: raw[i].tickets,
    shown: rounded ? `${fmtInt(rounded[i])}h` : fmtHours(raw[i].hours), hoursShare: byHours[i], ticketShare: byCount[i],
    fill: g.id === 'c' && !releases ? DONE_LAST_FILL : GROUP_FILL[g.id],
    statuses: Object.entries(raw[i].statuses ?? {}).filter(([, n]) => n > 0).sort((a, b) => statusOrder(a[0]) - statusOrder(b[0])).map(([s, n]) => [statusMeta(s).label, n]),
  }))
  const segments: WorkHeader['segments'] = []
  let left = 0
  for (const g of groups) {
    if (g.id === 'o') break
    const width = hours > 0 ? g.hours / hours * 100 : 0
    segments.push({ id: g.id, fill: g.fill, left, width })
    left += width
  }
  const narrowed = f.scope.kind === 'this_view'
  const scope = { narrowed, ...scopeLabel(narrowed, f.scope.leaves, f.scope.project_leaves, text), leaves: f.scope.leaves, projectLeaves: f.scope.project_leaves }
  const tickets = groups.reduce((a, g) => a + g.tickets, 0)
  const ne = f.without_estimate
  return {
    key: f.view_key, scope, marksReleases: releases, hours, total: f.approximate ? `~${fmtHours(hours)}` : fmtHours(hours), tickets, closed: f.scope.closed, withoutEstimate: ne, groups, segments,
    accessible: `${scope.label}. ${text.total} ${text.about} ${fmtHours(hours)}. ${groups.map(g => `${g.name} ${g.shown}`).join(' · ')}${ne ? ` · ${fill(text.withoutEstimate, { n: fmtInt(ne) })}` : ''}.`,
  }
}

// The value slots, fixed from the whole-project figures when the project opens (like the status counts' digits): the
// widest hours value, the total with its "~", and the project's leaf count for "N without estimate".
export interface WorkSlots { digits: number; total: number; leaves: number }
export function workSlots(whole: WorkHeader): WorkSlots {
  const widest = Math.max(fmtHours(whole.hours).length, ...whole.groups.map(g => g.shown.length))
  return { digits: widest, total: fmtHours(whole.hours).length + 1, leaves: Math.max(1, String(whole.scope.projectLeaves).length) }
}

export function workTipFoot(header: WorkHeader, hideLabel: string | null, text: WorkFiguresText = workFiguresText()): string[] {
  return [
    text.foot.estimates,
    header.withoutEstimate ? fill(text.foot.withoutEstimate, { n: fmtInt(header.withoutEstimate) }) : text.foot.everyEstimated,
    header.closed ? fill(text.foot.closed, { n: fmtInt(header.closed) }) : text.foot.noClosed,
    ...(hideLabel ? [fill(text.foot.hide, { hide: hideLabel })] : []),
    ...(header.marksReleases ? [] : [text.foot.noReleases]),
  ]
}

// ---------- Column header figures (spec §3.2): a fixed number of lines per column, so "Counting…" reserves the size ----------
export type FigureLead = { kind: 'swatch'; group: GroupId; fill: string } | { kind: 'priority'; priority: string } | { kind: 'measured' } | { kind: 'estimated' } | { kind: 'dash' }
export interface FigureLine { label: string; value: string; note?: string; head?: boolean; sub?: boolean; lead?: FigureLead }
// null is a reserved, empty line.
export interface ColumnTip { lines: (FigureLine | null)[]; foot: string }
export interface FigureColumn { key: string; lines: number; wide?: boolean }
// The column definitions' figure shape: which server column feeds it and how many lines it always has. Title has none.
export const FIGURE_COLUMNS: Record<ColumnId, FigureColumn | null> = {
  key: { key: 'key', lines: 3 }, title: null, status: { key: 'status', lines: 4, wide: true }, priority: { key: 'priority', lines: 4 },
  assignee: { key: 'assignee', lines: 4, wide: true }, epic: { key: 'epic', lines: 4 }, release: { key: 'release', lines: 4 },
  tags: { key: 'tag', lines: 4, wide: true }, cost: { key: 'cost_unit', lines: 4, wide: true }, estimate: { key: 'estimate', lines: 3 },
  model: { key: 'model', lines: 4, wide: true }, suggested: { key: 'suggested_release', lines: 4 }, tokens: { key: 'tokens', lines: 4 },
  list_cost: { key: 'cost', lines: 4 }, paid: { key: 'cost', lines: 4 }, created: { key: 'created_at', lines: 3 }, updated: { key: 'updated_at', lines: 3 },
  progress: { key: 'progress', lines: 3 }, eta: { key: 'eta_ready', lines: 3 },
}

// The figure shape of a column when it has figures in this view (Cost has none without harness.read).
export function figureColumn(id: string, figures: ProjectFigures | null): FigureColumn | null {
  const def = (FIGURE_COLUMNS as Record<string, FigureColumn | null>)[id] ?? null
  return def && (!figures || figures.columns[def.key]) ? def : null
}

export interface ColumnContext {
  // Hide closed is on and hides rows of this view: footers say "incl. hidden rows" for "shown".
  hiddenRows: boolean
  now: number
  text?: WorkFiguresText
}

const L = (label: string, value: string, extra: Partial<FigureLine> = {}): FigureLine => ({ label, value, ...extra })
const pad = (lines: (FigureLine | null)[], n: number) => { while (lines.length < n) lines.push(null); return lines.slice(0, n) }
const count = (n: number | undefined) => fmtInt(n ?? 0)

export function columnTip(id: ColumnId, f: ProjectFigures, ctx: ColumnContext): ColumnTip | null {
  const def = FIGURE_COLUMNS[id]
  if (!def) return null
  const c = f.columns[def.key]
  if (!c) return null
  const t = ctx.text ?? workFiguresText()
  const shown = ctx.hiddenRows ? t.inclHidden : t.shown
  const leavesFoot = (n: number, extra = '') => `${fmtInt(n)} ${shown}${extra} · ${t.leavesOnly}`
  const workFoot = (col: ColumnFigures) => col.included === col.rows ? leavesFoot(col.rows) : `${fmtInt(col.included)} of ${fmtInt(col.rows)} ${shown} · ${t.closedLeftOut}`
  const allRowsFoot = (col: ColumnFigures) => `${fmtInt(col.rows)} ${shown} · ${t.allRows}`
  const without = (template: string, n: number) => n ? ` · ${fill(template, { n: fmtInt(n) })}` : ''
  const values = c.values ?? []
  const top = (n: number, note?: (v: FigureValue) => string | undefined) => {
    const lines: (FigureLine | null)[] = values.slice(0, n).map(v => L(v.value, fmtInt(v.count), { note: note?.(v) }))
    while (lines.length < n) lines.push(L('—', '', { sub: true }))
    return lines
  }
  const others = () => L(t.lines.others, fmtInt(c.others), { sub: true })
  const lowerBound = (s: string) => c.partial && s !== '—' ? `≥${s}` : s
  const tickets = (n: number) => `${fmtInt(n)} ${n === 1 ? t.lines.ticket : t.lines.tickets}`
  let lines: (FigureLine | null)[]
  let foot: string
  switch (id) {
    case 'key':
      lines = [L(t.lines.parents, count(c.parents)), L(t.lines.leaves, count(c.leaves)), L(t.lines.keys, c.first_key && c.last_key ? (c.first_key === c.last_key ? c.first_key : `${c.first_key} – ${c.last_key}`) : '—')]
      foot = allRowsFoot(c)
      break
    case 'status': {
      const header = workHeader(f, t)
      lines = header.groups.map(g => {
        const def = GROUPS.find(x => x.id === g.id)!
        const detail = g.statuses.map(([label, n]) => `${label} ${fmtInt(n)}`).join(' · ')
        const plain = g.statuses.length === 1 && g.statuses[0][0] === statusMeta(def.states[0]).label
        return L(g.name, fmtInt(g.tickets), { note: detail && !plain ? detail : undefined, lead: { kind: 'swatch', group: g.id, fill: g.fill } })
      })
      foot = leavesFoot(f.scope.leaves, f.scope.closed ? ` · ${fill(t.closedCount, { n: fmtInt(f.scope.closed) })}` : '')
      break
    }
    case 'eta':
      lines = [
        L(t.lines.running, count(c.running), { head: true }),
        L(t.lines.nextFinish, c.next_finish ? fmtFinish(c.next_finish.at, ctx.now) : '—', { note: c.next_finish?.key }),
        L(t.lines.lastFinish, c.last_finish ? fmtFinish(c.last_finish.at, ctx.now) : '—', { note: c.last_finish?.key }),
      ]
      foot = leavesFoot(c.rows)
      break
    case 'progress':
      lines = [
        L(t.lines.weighted, c.weighted_percent == null ? '—' : fmtPct(c.weighted_percent), { head: true }),
        L(t.lines.plain, c.numbers?.average == null ? '—' : fmtPct(c.numbers.average), { sub: true }),
        L(t.lines.readings, count(c.reading_count ?? c.numbers?.count)),
      ]
      foot = workFoot(c)
      break
    case 'priority': {
      const n = (p: string) => values.find(v => v.value === p)?.count ?? 0
      lines = [
        L(t.lines.high, fmtInt(n('high')), { lead: { kind: 'priority', priority: 'high' } }),
        L(t.lines.medium, fmtInt(n('medium')), { lead: { kind: 'priority', priority: 'medium' } }),
        L(t.lines.low, fmtInt(n('low')), { lead: { kind: 'priority', priority: 'low' } }),
        L(t.lines.noPriority, fmtInt(c.without_value), { lead: { kind: 'dash' } }),
      ]
      foot = leavesFoot(c.rows)
      break
    }
    case 'assignee':
      lines = [...top(3, v => v.kind === 'agent' ? t.lines.agent : t.lines.person), others()]
      foot = leavesFoot(c.rows, without(t.unassigned, c.without_value))
      break
    case 'model':
      lines = [...top(3), others()]
      foot = leavesFoot(c.rows, without(t.withoutModel, c.without_value))
      break
    case 'cost': {
      const money = (v: FigureValue) => v.measured_cost == null && v.estimated_cost == null ? '' : ` · ${formatDollars((v.measured_cost ?? 0) + (v.estimated_cost ?? 0))}`
      lines = values.slice(0, 3).map(v => L(v.value, `${fmtHours(v.hours)}${money(v)}`))
      while (lines.length < 3) lines.push(L('—', '', { sub: true }))
      lines.push(others())
      foot = leavesFoot(c.rows, without(t.withoutCostUnit, c.without_value))
      break
    }
    case 'epic': case 'release': case 'suggested':
      lines = [...top(2), others(), L(id === 'epic' ? t.lines.noEpic : id === 'release' ? t.lines.noRelease : t.lines.none, fmtInt(c.without_value), { sub: true })]
      foot = leavesFoot(c.rows)
      break
    case 'tags': {
      const joined = (from: number, to: number) => values.slice(from, to).map(v => `${v.value} ${fmtInt(v.count)}`).join(' · ')
      lines = [L(joined(0, 3) || '—', '', { sub: !values.length }), L(joined(3, 5) || '—', '', { sub: values.length <= 3 }), others(), L(t.lines.untagged, fmtInt(c.without_value), { sub: true })]
      foot = leavesFoot(c.rows)
      break
    }
    case 'estimate': {
      const total = c.numbers?.total ?? 0
      lines = [
        L(t.total, total > 0 ? `~${fmtHours(total)}` : fmtHours(total), { head: true }),
        L(t.lines.median, c.numbers?.median == null ? '—' : fmtHours(c.numbers.median)),
        L(t.lines.withoutEstimate, fmtInt(c.without_value), { note: t.lines.countAsZero }),
      ]
      foot = workFoot(c)
      break
    }
    case 'tokens': case 'list_cost': case 'paid': {
      const fmt = id === 'tokens' ? fmtTokens : (v: number | null | undefined) => v == null ? '—' : formatDollars(v) || '—'
      const typical = c.typical, tl = typical?.approximate ? '~' : ''
      const measured = c.measured, estimated = c.estimated
      lines = [
        L(t.lines.median, typical?.median == null ? '—' : tl + fmt(typical.median), { head: true }),
        L(t.lines.average, typical?.average == null ? '—' : tl + fmt(typical.average), { sub: true }),
        L(t.lines.measured, measured?.count ? lowerBound(fmt(measured.total)) : '—', { lead: { kind: 'measured' }, note: tickets(measured?.count ?? 0) }),
        L(t.lines.estimated, estimated?.count ? lowerBound(`~${fmt(estimated.total)}`) : '—', { lead: { kind: 'estimated' }, note: tickets(estimated?.count ?? 0) }),
      ]
      foot = leavesFoot(c.rows, without(id === 'tokens' ? t.withoutTokens : t.withoutCost, c.without_value))
      break
    }
    case 'created':
      lines = [
        L(t.lines.range, c.from && c.to ? `${shortDate(c.from)} – ${sameDay(Date.parse(c.to), ctx.now) ? t.lines.today : shortDate(c.to)}` : '—'),
        L(t.lines.medianAge, c.median_age_seconds == null ? '—' : fmtDuration(c.median_age_seconds / 60)),
        L(t.lines.staleCreated, count(c.stale)),
      ]
      foot = allRowsFoot(c)
      break
    case 'updated':
      lines = [
        L(t.lines.range, c.from && c.to ? `${shortDate(c.from)} – ${relativeTime(c.to, { now: ctx.now })}` : '—'),
        L(t.lines.medianSinceUpdate, c.median_age_seconds == null ? '—' : fmtDuration(c.median_age_seconds / 60)),
        L(t.lines.staleUpdated, count(c.stale)),
      ]
      foot = allRowsFoot(c)
      break
    default:
      return null
  }
  return { lines: pad(lines, def.lines), foot }
}

// Hide closed hides some rows of this view: the footers then say "incl. hidden rows" instead of "shown".
export function hidesRows(f: ProjectFigures | null, hideOn: boolean, hideStates?: readonly string[]): boolean {
  if (!f || !hideOn) return false
  const chosen = new Set<string>(hiddenStates(hideStates))
  return (f.columns.status?.values ?? []).some(v => {
    const choice = hideChoice(v.value, fallbackBucket(v.value))
    return v.count > 0 && choice !== null && chosen.has(choice)
  })
}
