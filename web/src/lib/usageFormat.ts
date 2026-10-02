// SPDX-License-Identifier: AGPL-3.0-only
// Display helpers for the usage dashboard. Token totals and the USD list
// estimate stay exact strings until the last formatting step. Null and
// unknown are never rendered as zero. A reported subscription is not coverage.

export type CostState = 'known' | 'provisional' | 'unknown' | 'partial'
export type TokensState = 'known' | 'partial' | 'unknown'
export type BillingMode = 'subscription' | 'api' | 'unknown' | 'unreported' | 'mixed'
export interface UsageGroup {
  id?: string
  key?: string
  label: string
  billing_mode?: BillingMode
  sessions: number
  usage_rows: number
  unreported_sessions: number
  input_tokens: string | null
  input_known_rows: number
  input_unknown_rows: number
  output_tokens: string | null
  output_known_rows: number
  output_unknown_rows: number
  cached_input_tokens: string | null
  cached_input_known_rows: number
  cached_input_unknown_rows: number
  tokens_state: TokensState
  estimated_cost_usd: string | null
  cost_known_rows: number
  cost_unknown_rows: number
  cost_state: CostState
  provisional_rows: number
  provisional_sessions: number
}
export interface UsageTrend { day: string; group: UsageGroup }
export interface UsageTicket extends UsageGroup { project_id: string; project_key: string }
export interface AllowanceWindow {
  account_id: string
  label: string
  harness: string
  account_state: string
  window_id: string
  unit: 'requests' | 'tokens' | 'cost_micros'
  allowance: number
  used: number | null
  reserved: number
  pace_model: 'steady' | 'frontload' | 'unrestricted'
  burst_ratio: string
  starts_at: string
  ends_at: string
  provisional: boolean
  pace_cap: number | null
  headroom: number | null
  hard_remaining: number | null
}
/** Tokens by component, each with the sessions that reported it (AEON-301).
 *  A missing component is left out of the sum, never counted as zero. */
export interface TokenParts {
  input_tokens: string | null
  output_tokens: string | null
  cached_input_tokens: string | null
  input_reported_sessions: number
  output_reported_sessions: number
  cached_input_reported_sessions: number
  usage_provisional_sessions: number
}
export interface WorkGroup extends TokenParts {
  key: string
  label: string
  sessions: number
  timed_sessions: number
  agent_seconds: number
  done: number
  tokens: string | null
  usage_reported_sessions: number
}
export interface WorkTicket extends TokenParts {
  id: string
  key: string
  title: string
  project_id: string
  project_key: string
  state: string
  done_in_range: boolean
  released: boolean
  sessions: number
  timed_sessions: number
  agent_seconds: number
  tokens: string | null
  usage_reported_sessions: number
  last_active_at: string
}
export type WasteKind = 'stuck' | 'failed' | 'lost' | 'no_result' | 'retried'
export interface WasteItem {
  kind: WasteKind
  session_id: string
  project_id: string
  project_key: string
  ticket_id: string | null
  ticket_key: string | null
  ticket_title: string | null
  harness: string
  model: string | null
  label: string | null
  at: string
  sessions: number
  agent_seconds: number | null
}
/** What agents got done and where the time went (AEON-301). */
export interface UsageWork extends TokenParts {
  basis: 'sessions_started_in_range'
  sessions: number
  worker_sessions: number
  timed_sessions: number
  agent_seconds: number
  usage_reported_sessions: number
  model_sessions: number
  done: number
  released: number
  done_attributed: number
  tickets_worked: number
  tickets_done: number
  days: { day: string; done: number; sessions: number; agent_seconds: number }[]
  tickets: WorkTicket[]
  by_harness: WorkGroup[]
  by_model: WorkGroup[]
  by_project: WorkGroup[]
  waste: { total: number; stuck: number; failed: number; lost: number; no_result: number; retried: number; rows: WasteItem[] }
}
export interface UsageDashboard {
  from: string
  to: string
  generated_at: string
  attribution: 'lifetime_for_sessions_started_in_range'
  trend_basis: 'session_started_utc_day'
  list_price_currency: 'USD'
  truncated: boolean
  totals: UsageGroup
  by_project: UsageGroup[]
  by_model: UsageGroup[]
  by_harness?: UsageGroup[]
  by_subscription: UsageGroup[]
  trend: UsageTrend[]
  tickets: UsageTicket[]
  tickets_cost_unknown: number
  allowance: { state: 'visible' | 'partial' | 'withheld' | 'none'; windows: AllowanceWindow[]; truncated?: boolean }
  ratings?: {
    votes: number
    average: string | null
    exceptions: number
    deliveries: number
    rework_rate: string | null
    by_model: { label: string; votes: number; average: string | null; exceptions: number; deliveries: number; rework_rate: string | null }[]
    by_harness: { label: string; votes: number; average: string | null; exceptions: number; deliveries: number; rework_rate: string | null }[]
  }
  work: UsageWork
}

export const costStateLabel: Record<CostState, string> = {
  known: 'Known', provisional: 'Provisional', unknown: 'Unknown', partial: 'Partial',
}
export const billingLabel: Record<BillingMode, string> = {
  subscription: 'Reported subscription',
  api: 'Reported API',
  unknown: 'Reported unknown',
  unreported: 'No report',
  mixed: 'Mixed reports',
}

const day = 86_400_000
const usdPattern = /^(0|[1-9]\d*)\.(\d{12})$/
const intPattern = /^(0|[1-9]\d*)$/

export function rangeBounds(days: number, now: Date): { from: string; to: string } {
  const to = new Date(Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), now.getUTCDate() + 1))
  const from = new Date(to.getTime() - days * day)
  return { from: stamp(from), to: stamp(to) }
}

function stamp(date: Date) {
  return date.toISOString().replace(/\.\d{3}Z$/, 'Z')
}

function groupDigits(digits: string): string {
  return digits.replace(/\B(?=(\d{3})+(?!\d))/g, ',')
}

export function formatInteger(value: string | null): string {
  if (value === null || !intPattern.test(value)) return 'Unknown'
  return groupDigits(value)
}

export function formatUSD(value: string | null): string {
  const match = value === null ? null : usdPattern.exec(value)
  if (!match) return 'Unknown'
  const fraction = match[2] ?? ''
  const trimmed = fraction.replace(/0+$/, '')
  const shown = trimmed.length < 2 ? fraction.slice(0, 2) : trimmed
  return `${groupDigits(match[1] ?? '0')}.${shown} USD`
}

export function formatTokens(value: string | null, known: number, unknown: number): string {
  if (value === null || known === 0) return 'Unknown'
  const text = formatInteger(value)
  if (text === 'Unknown') return text
  if (unknown === 0) return text
  return `${text} from ${known} of ${known + unknown}`
}

export function usdUnits(value: string | null): bigint {
  const match = value === null ? null : usdPattern.exec(value)
  if (!match) return 0n
  return BigInt(match[1] ?? '0') * 1_000_000_000_000n + BigInt(match[2] ?? '0')
}

export function formatCount(n: number): string {
  return n.toLocaleString('en-US')
}

export function formatOptionalCount(value: number | null): string {
  if (value === null || !Number.isSafeInteger(value)) return 'Unknown'
  return formatCount(value)
}

export function formatWhen(iso: string): string {
  const date = new Date(iso)
  if (Number.isNaN(date.getTime())) return iso
  return new Intl.DateTimeFormat('en-GB', { day: 'numeric', month: 'short', year: 'numeric', timeZone: 'UTC' }).format(date)
}

const compact = new Intl.NumberFormat('en-US', { notation: 'compact', maximumSignificantDigits: 3 })

/** Short token count for dense places: 2.28M, 264k, 9,120. Unknown stays null, never zero. */
export function compactCount(value: string | number | null): string | null {
  if (value === null) return null
  const text = String(value)
  if (!intPattern.test(text)) return null
  const n = Number(text)
  if (n < 10_000) return formatCount(n)
  return compact.format(n).replace(/K$/, 'k')
}

/** The day part of a trend point, whether it arrives as a date or a timestamp. */
export function dayKey(value: string): string {
  return value.slice(0, 10)
}

/** Every UTC day in [from, to), as YYYY-MM-DD. */
export function rangeDays(from: string, to: string): string[] {
  const start = Date.parse(`${dayKey(from)}T00:00:00Z`)
  const end = Date.parse(to)
  if (!Number.isFinite(start) || !Number.isFinite(end)) return []
  const days: string[] = []
  for (let t = start; t < end && days.length < 400; t += day) days.push(new Date(t).toISOString().slice(0, 10))
  return days
}

export function formatDay(iso: string): string {
  const date = new Date(`${dayKey(iso)}T00:00:00Z`)
  if (Number.isNaN(date.getTime())) return iso
  return new Intl.DateTimeFormat('en-GB', { day: 'numeric', month: 'short', timeZone: 'UTC' }).format(date)
}

/** USD as a plain number for chart geometry only; display keeps formatUSD. */
export function usdNumber(value: string | null): number | null {
  const match = value === null ? null : usdPattern.exec(value)
  if (!match) return null
  return Number(`${match[1]}.${match[2]}`)
}

/** Allowance amounts in their unit. Cost micros read as USD (1,000,000 is 1 USD). */
export function formatAllowanceAmount(value: number | null, unit: AllowanceWindow['unit']): string | null {
  if (value === null || !Number.isSafeInteger(value)) return null
  if (unit === 'cost_micros') {
    const usd = value / 1_000_000
    return `${usd.toLocaleString('en-US', { minimumFractionDigits: 2, maximumFractionDigits: 2 })} USD`
  }
  return compactCount(value)
}
