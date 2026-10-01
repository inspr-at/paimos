// SPDX-License-Identifier: AGPL-3.0-only
// Planning cells (AEON-511): planned versus used models, measured tokens and
// their list value. Work-start snapshots are the comparison baseline. Mirrors
// api/openapi.yaml TicketPlanning. Free of Vue for unit tests.

export interface PlanningRoute { label: string; profile: string; harness: string; model: string; effort: string; revision: string }
export interface PlanningCalibration { basis: 'median' | 'default'; tickets: number; tokens_per_hour: number; any_route?: boolean }
export interface PlanningTokens {
  spent: number | null; input: number; output: number; cached: number
  sessions: number; running?: number; unreported: number; estimated: number | null
  calibration?: PlanningCalibration
}
export interface PlanningCost {
  list_spent: string | null; list_estimated: string | null; list_unpriced: boolean
  paid_spent: string | null; paid_estimated: string | null; paid_unknown: boolean
  plans: string[]
  billing_modes?: ('api' | 'subscription' | 'unknown')[]
  // Integer micro-dollars of the sort key (spent when present, otherwise the
  // estimate). Display stays on the USD strings above.
  list_cost_micros?: string | null
  paid_micros?: string | null
}
export interface PlanningModelSession { id: string; profile_id?: string; model_raw?: string; effort: string; role: string; running: boolean; tokens: number | null }
export interface PlanningModel { label: string; harness: string; model: string; sessions: PlanningModelSession[] }
export interface PlanningSnapshot {
  id: string; started_at: string; source: 'session' | 'status'; estimate_hours: number | null
  estimated_tokens: number | null; estimated_cost_usd?: string | null; route: PlanningRoute | null
  rate_basis: PlanningCalibration & { list_per_hour?: string | null }
}
export interface TicketPlanning {
  models?: PlanningModel[]
  estimate_snapshot?: PlanningSnapshot
  route: PlanningRoute | null
  route_gap?: 'area' | 'review_gate' | 'registry'
  tokens: PlanningTokens
  cost?: PlanningCost
  children?: { total: number; estimated: number }
}
// The part of a list row the planning cells read.
export interface PlanningRow { kind_slug: string; fields: Record<string, unknown>; state?: string | null; eta?: { has_working_session?: boolean }; planning?: TicketPlanning }

export type PlanningColumn = 'model' | 'tokens' | 'list_cost' | 'paid'
export const PLANNING_COLUMNS: PlanningColumn[] = ['model', 'tokens', 'list_cost']
// Cost columns show only to people who may see usage (harness.read).
export const COST_COLUMNS: PlanningColumn[] = ['list_cost', 'paid']

const DEFAULT_RATE = 5_000_000
const compact = new Intl.NumberFormat('en-US', { notation: 'compact', maximumSignificantDigits: 3 })
const grouped = new Intl.NumberFormat('en-US')

/** Dense token count: 940, 9.12k, 1.54M. */
export function formatTokenCount(n: number): string {
  if (!Number.isFinite(n) || n < 0) return ''
  if (n < 1000) return String(Math.round(n))
  return compact.format(n).replace(/K$/, 'k')
}
/** Dense dollars: $0, <$0.01, $4.48, $27, $1.2k. */
export function formatDollars(value: string | number | null): string {
  const n = typeof value === 'string' ? Number(value) : value
  if (n === null || !Number.isFinite(n) || n < 0) return ''
  if (n === 0) return '$0'
  if (n < 0.01) return '<$0.01'
  if (n < 10) return `$${n.toFixed(2)}`
  if (n < 1000) return `$${Math.round(n)}`
  return `$${compact.format(n).replace(/K$/, 'k')}`
}
function exactDollars(value: string): string {
  const n = Number(value)
  return n > 0 && n < 0.01 ? '<$0.01' : `$${n.toLocaleString('en-US', { minimumFractionDigits: 2, maximumFractionDigits: 2 })}`
}
function num(value: string | null | undefined): number | null {
  if (value === null || value === undefined) return null
  const n = Number(value)
  return Number.isFinite(n) ? n : null
}
/** The API's micro-dollar sort key. A float cannot separate 10^10 dollars plus one micro. */
function integerMicros(value: string | null | undefined): bigint | null {
  if (value == null || !/^[0-9]+$/.test(value)) return null
  return BigInt(value)
}
const ROLE_LABEL: Record<string, string> = { scout: 'Scout', mechanical: 'Mechanical', build: 'Build', 'build-hard': 'Build hard', 'review-gate': 'Review gate' }
function text(fields: Record<string, unknown>, key: string): string {
  const value = fields[key]
  return typeof value === 'string' ? value.trim() : ''
}
export function roleOf(row: PlanningRow): string { return text(row.fields, 'route_role') }
export function areaOf(row: PlanningRow): string { return text(row.fields, 'area') }
function roleArea(row: PlanningRow): string {
  const role = roleOf(row), area = areaOf(row)
  return [role ? ROLE_LABEL[role] ?? role : '', area].filter(Boolean).join(' · ')
}

// ---------- Model ----------
export interface ModelCell { text: string; tip: string; state: 'none' | 'planned' | 'measured'; harness: string; more: number; label: string }
function plannedRoute(row: PlanningRow): PlanningRoute | null | undefined {
  return row.planning?.estimate_snapshot ? row.planning.estimate_snapshot.route : row.planning?.route
}
function shortModelLabel(label: string): string { return label.split(' · ')[0]!.replace(/\bgpt-\d+(?:\.\d+)?-/, '') }
function shortModel(route: PlanningRoute): string { return shortModelLabel(route.label) }
function planLine(row: PlanningRow, models: PlanningModel[] = []): string {
  const route = plannedRoute(row)
  if (!route) return 'No model planned: set a role and area'
  const asUsed = models.every(model => model.harness === route.harness && model.model === route.model)
  const suffix = models.length ? asUsed ? ', as used' : ' (a different model ran)' : ` (${route.model})`
  return `Planned: ${route.label}${suffix}`
}
function usedLines(models: PlanningModel[]): string[] {
  return models.map(model => {
    const efforts = [...new Set(model.sessions.map(session => session.effort).filter(Boolean))]
    const reported = model.sessions.filter(session => session.tokens !== null)
    const total = reported.reduce((sum, session) => sum + session.tokens!, 0)
    const review = model.sessions.length > 0 && model.sessions.every(session => session.role === 'reviewer')
    return [model.label, ...efforts, `${model.sessions.length} session${model.sessions.length === 1 ? '' : 's'}${model.sessions.some(session => session.running) ? ', running' : ''}`,
      ...(models.length > 1 ? [reported.length ? `${formatTokenCount(total)}${review ? ' (review)' : ''}` : 'usage not reported yet'] : [])].join(' · ')
  })
}
export function modelCell(row: PlanningRow): ModelCell {
  const models = row.kind_slug === 'epic' ? [] : row.planning?.models ?? []
  if (models.length) {
    const first = models[0]!, more = models.length - 1
    return { text: shortModelLabel(first.label), harness: first.harness, state: 'measured', more,
      label: `${first.label}, measured${more ? `, and ${more} more model${more === 1 ? '' : 's'}` : ''}`,
      tip: [...(more ? ['Used, per session:', ...usedLines(models)] : [`Used: ${usedLines(models)[0]}`]), planLine(row, models)].join('\n') }
  }
  const route = plannedRoute(row)
  if (row.kind_slug !== 'epic' && route) {
    return { text: shortModel(route), harness: route.harness, state: 'planned', more: 0,
      label: `${shortModel(route)}, estimated (planned)`,
      tip: [planLine(row), `${roleArea(row)} · Model registry, revision ${route.revision}`, row.planning?.tokens.sessions ? 'Session model not reported yet' : 'No agent session yet'].join('\n') }
  }
  let reason = 'No model planned: Set a role and area'
  if (row.kind_slug === 'epic') reason = 'Epics take no model; their tickets do'
  else if (roleOf(row)) switch (row.planning?.route_gap) {
    case 'area': reason = `${roleArea(row)}\nSet an area to resolve the model`; break
    case 'review_gate': reason = `${roleArea(row)}\nChosen at dispatch from a family other than the author's`; break
    default: reason = `${roleArea(row)}\nThe model registry has no available route for this role`
  }
  return { text: '', harness: '', state: 'none', more: 0, label: 'No model', tip: `${row.planning?.tokens.sessions ? 'Session model not reported yet' : 'No agent session yet'}\n${reason}` }
}

// ---------- Figures ----------
export interface FigureCell {
  state: 'none' | 'estimated' | 'running' | 'measured'
  spent: string; estimated: string; over: boolean; plan: boolean
  label: string; tip: string
}
function running(row: PlanningRow): boolean {
  const count = row.planning?.tokens.running
  return count !== undefined ? count > 0 : !!row.eta?.has_working_session || row.state === 'in_progress'
}
function delta(spent: number | null, est: number | null, live: boolean): string {
  if (spent === null || est === null || est <= 0) return ''
  const pct = Math.round((live ? spent / est : (spent - est) / est) * 100)
  return live ? ` (${pct}%)` : ` (${pct > 0 ? '+' : pct < 0 ? '−' : ''}${Math.abs(pct)}%)`
}
function snapshotLine(row: PlanningRow): string {
  const snap = row.planning?.estimate_snapshot
  return snap ? `Estimate taken when work started, ${new Date(snap.started_at).toLocaleString('en-GB', { dateStyle: 'medium', timeStyle: 'short' })}` : 'No work-start estimate snapshot; comparison uses the current estimate'
}
function figure(row: PlanningRow, spent: number | null, est: number | null, format: (n: number) => string, tip: string, unit: string, plan = false): FigureCell {
  const live = running(row)
  const state = spent === null ? est === null ? 'none' : 'estimated' : live ? 'running' : 'measured'
  const over = state === 'running' && est !== null && spent! > est
  const parts = [spent !== null ? `${format(spent)}${unit} measured${live ? ' so far' : ''}` : '', est !== null && state !== 'measured' ? `${format(est)} estimated` : '', over ? 'over the estimate' : '', plan && state === 'measured' ? 'included in your plan' : '']
  return { state, spent: spent !== null ? format(spent) : '', estimated: est !== null ? format(est) : '', over, plan: plan && state === 'measured', label: parts.filter(Boolean).join(', '), tip }
}
function basisLine(tokens: PlanningTokens, row: PlanningRow): string {
  const snap = row.planning?.estimate_snapshot
  const cal = snap?.rate_basis ?? tokens.calibration
  if (row.kind_slug === 'epic' && row.planning?.children) {
    const c = row.planning.children
    return `Sum of ${c.estimated} of ${c.total} open and done children with an estimate`
  }
  if (!cal) return ''
  const est = snap ? snap.estimated_tokens : tokens.estimated
  const hours = snap ? snap.estimate_hours : est !== null && cal.tokens_per_hour > 0 ? est / cal.tokens_per_hour : null
  const rate = `${formatTokenCount(cal.tokens_per_hour)}/h`
  const route = plannedRoute(row)?.label
  const on = cal.any_route || !route ? 'on any route' : `on ${route}`
  const times = hours !== null ? `${Number(hours.toFixed(2))}h at ${rate}` : rate
  return cal.basis === 'median'
    ? `${times}: median of the last ${cal.tickets} finished tickets ${on}`
    : `${times}: default ${formatTokenCount(DEFAULT_RATE)}/h until 5 finished tickets ${on}`
}
export function tokensCell(row: PlanningRow): FigureCell {
  const tokens = row.planning?.tokens
  const snap = row.planning?.estimate_snapshot
  const spent = tokens?.spent ?? null, est = snap ? snap.estimated_tokens : tokens?.estimated ?? null
  const live = running(row)
  const lines: string[] = []
  if (spent !== null) {
    const comparison = live
      ? [`Measured so far ${formatTokenCount(spent)}`, est !== null ? `estimated ~${formatTokenCount(est)}${delta(spent, est, true)}` : '']
      : [est !== null ? `Estimated ~${formatTokenCount(est)}` : '', `measured ${formatTokenCount(spent)}${delta(spent, est, false)}`]
    lines.push([...comparison, ...(row.planning?.models?.length ? [row.planning.models.length === 1 ? row.planning.models[0]!.label : `${row.planning.models.length} models`] : [])].filter(Boolean).join(' · '))
    lines.push(`${tokens!.sessions} session${tokens!.sessions === 1 ? '' : 's'}${live ? ', running' : ''} · input ${grouped.format(tokens!.input)} (${grouped.format(tokens!.cached)} cached) · output ${grouped.format(tokens!.output)}`)
    if (est !== null) lines.push(snapshotLine(row))
  } else if (est !== null) lines.push(`Estimated ~${formatTokenCount(est)} tokens · ${tokens?.sessions ? 'usage not reported yet' : 'no agent session yet'}`)
  else lines.push(tokens?.sessions ? 'Usage not reported yet' : 'No agent session yet')
  if (tokens?.unreported) lines.push(`${tokens.unreported} ${tokens.unreported === 1 ? 'session has' : 'sessions have'} no usage report yet`)
  if (tokens && est !== null) { const basis = basisLine(tokens, row); if (basis) lines.push(basis) }
  return figure(row, spent, est, formatTokenCount, lines.join('\n'), ' tokens')
}

// Cost is measured tokens at list prices, with actual billing named in the hover.
export function listCostCell(row: PlanningRow): FigureCell {
  const cost = row.planning?.cost, snap = row.planning?.estimate_snapshot
  const spent = num(cost?.list_spent), est = snap ? num(snap.estimated_cost_usd) : num(cost?.list_estimated)
  const modes = cost?.billing_modes ?? (cost?.plans.length && row.planning?.tokens.sessions ? ['subscription'] : cost && !cost.paid_unknown && row.planning?.tokens.sessions ? ['api'] : [])
  const subscription = modes.includes('subscription'), api = modes.includes('api'), unknown = modes.includes('unknown')
  const subscriptionOnly = modes.length === 1 && subscription
  const live = running(row)
  const lines: string[] = []
  if (spent !== null) {
    if (live) {
      lines.push([`Measured so far ${formatDollars(spent)}`, est !== null ? `estimated ~${formatDollars(est)}${delta(spent, est, true)}` : ''].filter(Boolean).join(' · '))
      if (subscriptionOnly) lines.push('Included in your plan · at list prices', 'Subscription: not charged per use')
      else lines.push([api ? 'API-billed' : '', subscription ? 'Subscription portion included in your plan' : '', unknown || !modes.length ? 'Billing not reported yet' : '', 'at list prices'].filter(Boolean).join(' · '))
    } else {
      if (subscriptionOnly) lines.push(`Included in your plan · list value ${formatDollars(spent)}`, 'Subscription: not charged per use')
      else if (api && !subscription && !unknown) lines.push(`API-billed · measured ${formatDollars(spent)} at list prices`)
      else lines.push(`Measured ${formatDollars(spent)} at list prices`, [api ? 'API-billed' : '', subscription ? 'Subscription portion included in your plan' : '', unknown || !modes.length ? 'Billing not reported yet' : ''].filter(Boolean).join(' · '))
      if (est !== null) lines.push(`Estimated ~${formatDollars(est)}${delta(spent, est, false)}`)
    }
    if (cost?.plans.length && subscription) lines.push(cost.plans.join(', '))
    if (est !== null) lines.push(snapshotLine(row))
  } else if (est !== null) {
    const hours = snap ? snap.estimate_hours : row.planning?.tokens.calibration && row.planning.tokens.estimated !== null ? row.planning.tokens.estimated / row.planning.tokens.calibration.tokens_per_hour : null
    lines.push(`Estimated ~${formatDollars(est)} at API list prices${hours && hours > 0 ? ` (${exactDollars(String(est / hours))}/h)` : ''}`, 'Billing shows once a session reports')
  } else lines.push(row.planning?.tokens.sessions ? 'Billing not reported yet' : 'No agent session yet')
  if (cost?.list_unpriced) lines.push('Part of this has no list price, so it is a lower bound')
  if (cost?.paid_unknown && spent !== null) lines.push('Part of this has no billing on record')
  return figure(row, spent, est, formatDollars, lines.filter(Boolean).join('\n'), '', subscriptionOnly)
}

// ---------- Columns and sorting ----------
/** Which planning columns any loaded row can fill; empty ones stay hidden. */
export function planningPresent(rows: PlanningRow[]): Record<PlanningColumn, boolean> {
  return {
    model: rows.some(row => !!modelCell(row).text),
    tokens: rows.some(row => tokensCell(row).state !== 'none'),
    list_cost: rows.some(row => listCostCell(row).state !== 'none'),
    paid: false,
  }
}
const ROLE_RANK: Record<string, number> = { scout: 0, mechanical: 1, build: 2, 'build-hard': 3, 'review-gate': 4 }
interface ModelOrder { rank: number; area: string }
function modelOrder(row: PlanningRow): ModelOrder | null {
  const rank = ROLE_RANK[roleOf(row)]
  return rank === undefined ? null : { rank, area: areaOf(row) }
}
/**
 * Model order matches the list API: the role's rung, then the area.
 * A missing role stays last. Rows that both lack a role still compare areas.
 * A missing area stays last in either direction.
 */
export function compareModelSort(a: PlanningRow, b: PlanningRow, desc: boolean): number {
  const x = modelOrder(a), y = modelOrder(b)
  if ((x === null) !== (y === null)) return x === null ? 1 : -1
  const dir = desc ? -1 : 1
  if (x && y && x.rank !== y.rank) return (x.rank < y.rank ? -1 : 1) * dir
  const xa = areaOf(a), ya = areaOf(b)
  if ((xa === '') !== (ya === '')) return xa === '' ? 1 : -1
  if (xa !== ya) return (xa < ya ? -1 : 1) * dir
  return 0
}
/** The list API's order for a numeric planning sort key: null sorts last in both directions. Cost keys are integer micro-dollars. */
export function planningSortValue(row: PlanningRow, field: PlanningColumn): number | string | bigint | null {
  switch (field) {
    case 'model': { const order = modelOrder(row); return order ? `${order.rank}:${order.area}` : null }
    case 'tokens': return row.planning?.tokens.spent ?? row.planning?.tokens.estimated ?? null
    case 'list_cost': return integerMicros(row.planning?.cost?.list_cost_micros)
    case 'paid': return integerMicros(row.planning?.cost?.paid_micros)
  }
}
/** Screen-reader text for one planning cell; empty when the cell has nothing to explain. */
export function planningTip(row: PlanningRow, column: PlanningColumn): string {
  switch (column) {
    case 'model': return modelCell(row).tip
    case 'tokens': return tokensCell(row).tip
    case 'list_cost': return listCostCell(row).tip
    case 'paid': return listCostCell(row).tip
  }
}
