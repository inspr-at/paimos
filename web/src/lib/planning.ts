// SPDX-License-Identifier: AGPL-3.0-only
// Planning columns (AEON-329): the model a ticket's role and area resolve to,
// tokens spent / estimated, the same at API list prices (≈ Cost, always
// approximate) and what was actually paid. Mirrors api/openapi.yaml
// TicketPlanning. Free of Vue for unit tests.

export interface PlanningRoute { label: string; profile: string; harness: string; model: string; effort: string; revision: string }
export interface PlanningCalibration { basis: 'median' | 'default'; tickets: number; tokens_per_hour: number; any_route?: boolean }
export interface PlanningTokens {
  spent: number | null; input: number; output: number; cached: number
  sessions: number; unreported: number; estimated: number | null
  calibration?: PlanningCalibration
}
export interface PlanningCost {
  list_spent: string | null; list_estimated: string | null; list_unpriced: boolean
  paid_spent: string | null; paid_estimated: string | null; paid_unknown: boolean
  plans: string[]
  // Integer micro-dollars of the sort key (spent when present, otherwise the
  // estimate). Display stays on the USD strings above.
  list_cost_micros?: string | null
  paid_micros?: string | null
}
export interface TicketPlanning {
  route: PlanningRoute | null
  route_gap?: 'area' | 'review_gate' | 'registry'
  tokens: PlanningTokens
  cost?: PlanningCost
  children?: { total: number; estimated: number }
}
// The part of a list row the planning cells read.
export interface PlanningRow { kind_slug: string; fields: Record<string, unknown>; planning?: TicketPlanning }

export type PlanningColumn = 'model' | 'tokens' | 'list_cost' | 'paid'
export const PLANNING_COLUMNS: PlanningColumn[] = ['model', 'tokens', 'list_cost', 'paid']
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
export interface ModelCell { text: string; tip: string }
export function modelCell(row: PlanningRow): ModelCell {
  const plan = row.planning
  if (row.kind_slug === 'epic') return { text: '', tip: 'Epics take no model; their tickets do' }
  if (plan?.route) {
    return { text: plan.route.label, tip: `${roleArea(row)}\nModel registry, revision ${plan.route.revision}` }
  }
  const role = roleOf(row)
  if (!role) return { text: '', tip: 'Set a role and area to suggest a model' }
  switch (plan?.route_gap) {
    case 'area': return { text: '', tip: `${roleArea(row)}\nSet an area to resolve the model` }
    case 'review_gate': return { text: '', tip: `${roleArea(row)}\nChosen at dispatch from a family other than the author's` }
    default: return { text: '', tip: `${roleArea(row)}\nThe model registry has no available route for this role` }
  }
}

// ---------- Tokens ----------
export interface FigureCell {
  spent: string; estimated: string
  // Spent above the estimate: the spent figure is tinted and the label says so.
  over: boolean
  label: string; tip: string
}
const EMPTY: FigureCell = { spent: '', estimated: '', over: false, label: '', tip: '' }
function basisLine(tokens: PlanningTokens, row: PlanningRow): string {
  const cal = tokens.calibration
  if (row.kind_slug === 'epic' && row.planning?.children) {
    const c = row.planning.children
    return `Sum of ${c.estimated} of ${c.total} open and done children with an estimate`
  }
  if (!cal) return ''
  const hours = tokens.estimated !== null && cal.tokens_per_hour > 0 ? tokens.estimated / cal.tokens_per_hour : null
  const rate = `${formatTokenCount(cal.tokens_per_hour)}/h`
  const route = row.planning?.route?.label
  const on = cal.any_route || !route ? 'on any route' : `on ${route}`
  const times = hours !== null ? `${Number(hours.toFixed(2))}h at ${rate}` : rate
  return cal.basis === 'median'
    ? `${times}: median of the last ${cal.tickets} finished tickets ${on}`
    : `${times}: default ${formatTokenCount(DEFAULT_RATE)}/h until 5 finished tickets ${on}`
}
export function tokensCell(row: PlanningRow): FigureCell {
  const tokens = row.planning?.tokens
  if (!tokens || (tokens.spent === null && tokens.estimated === null)) return EMPTY
  const spent = tokens.spent, est = tokens.estimated
  const over = spent !== null && est !== null && spent > est
  const lines: string[] = []
  if (spent !== null) {
    lines.push(`Spent ${grouped.format(spent)} tokens over ${tokens.sessions === 1 ? '1 session' : `${tokens.sessions} sessions`}`)
    lines.push(`Input ${grouped.format(tokens.input)} (${grouped.format(tokens.cached)} cached) · output ${grouped.format(tokens.output)}`)
    if (tokens.unreported) lines.push(`${tokens.unreported} ${tokens.unreported === 1 ? 'session has' : 'sessions have'} no usage report yet`)
  } else if (tokens.sessions) {
    lines.push(`${tokens.sessions === 1 ? 'The session has' : `${tokens.sessions} sessions have`} no usage report yet`)
  }
  if (est !== null) {
    lines.push(`Estimated ${grouped.format(est)} tokens`)
    const basis = basisLine(tokens, row)
    if (basis) lines.push(basis)
  }
  if (over) lines.push(`Over the estimate by ${formatTokenCount(spent! - est!)}`)
  const label = [spent !== null ? `${formatTokenCount(spent)} tokens spent` : '', est !== null ? `${formatTokenCount(est)} estimated` : '', over ? 'over the estimate' : ''].filter(Boolean).join(', ')
  return { spent: spent !== null ? formatTokenCount(spent) : '', estimated: est !== null ? formatTokenCount(est) : '', over, label, tip: lines.join('\n') }
}

// ---------- ≈ Cost and Paid ----------
export function listCostCell(row: PlanningRow): FigureCell {
  const cost = row.planning?.cost
  if (!cost) return EMPTY
  const spent = num(cost.list_spent), est = num(cost.list_estimated)
  if (spent === null && est === null) return EMPTY
  const over = spent !== null && est !== null && spent > est
  const lines = ['At API list prices, whatever the billing']
  if (spent !== null) lines.push(`Spent ≈ ${exactDollars(cost.list_spent!)}`)
  if (est !== null) {
    const tokens = row.planning!.tokens
    const cal = tokens.calibration
    const hours = cal && tokens.estimated !== null && cal.tokens_per_hour > 0 ? tokens.estimated / cal.tokens_per_hour : null
    lines.push(`Estimated ≈ ${exactDollars(cost.list_estimated!)}${hours ? ` (${exactDollars(String(est / hours))}/h)` : ''}`)
  }
  if (cost.list_unpriced) lines.push('Part of this has no list price, so it is a lower bound')
  if (over) lines.push(`Over the estimate by ${formatDollars(spent! - est!)}`)
  const label = ['approximately', spent !== null ? `${formatDollars(spent)} spent` : '', est !== null ? `${formatDollars(est)} estimated` : '', over ? 'over the estimate' : ''].filter(Boolean).join(' ')
  return { spent: spent !== null ? formatDollars(spent) : '', estimated: est !== null ? formatDollars(est) : '', over, label, tip: lines.join('\n') }
}
export function paidCell(row: PlanningRow): FigureCell {
  const cost = row.planning?.cost
  if (!cost) return EMPTY
  const spent = num(cost.paid_spent), est = num(cost.paid_estimated)
  if (spent === null && est === null) return EMPTY
  const over = spent !== null && est !== null && spent > est
  const plans = cost.plans.length ? cost.plans.join(', ') : ''
  const lines = ['Pay-per-use charges']
  if (spent !== null) lines.push(`Paid ${exactDollars(cost.paid_spent!)}`)
  if (est !== null) lines.push(`Estimated ${exactDollars(cost.paid_estimated!)}`)
  if (plans) lines.push(`Work on ${plans} counts as $0`)
  if (cost.paid_unknown) lines.push('Part of this has no billing on record')
  if (over) lines.push(`Over the estimate by ${formatDollars(spent! - est!)}`)
  const label = [spent !== null ? `${formatDollars(spent)} paid` : '', est !== null ? `${formatDollars(est)} estimated` : '', over ? 'over the estimate' : ''].filter(Boolean).join(', ')
  return { spent: spent !== null ? formatDollars(spent) : '', estimated: est !== null ? formatDollars(est) : '', over, label, tip: lines.join('\n') }
}

// ---------- Columns and sorting ----------
/** Which planning columns any loaded row can fill; empty ones stay hidden. */
export function planningPresent(rows: PlanningRow[]): Record<PlanningColumn, boolean> {
  return {
    model: rows.some(row => row.kind_slug !== 'epic' && (!!roleOf(row) || !!row.planning?.route)),
    tokens: rows.some(row => !!tokensCell(row).label),
    list_cost: rows.some(row => !!listCostCell(row).label),
    paid: rows.some(row => !!paidCell(row).label),
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
    case 'paid': return paidCell(row).tip
  }
}
