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
  used: number
  reserved: number
  pace_model: 'steady' | 'frontload' | 'unrestricted'
  burst_ratio: string
  starts_at: string
  ends_at: string
  provisional: boolean
  pace_cap: number | null
  headroom: number | null
  hard_remaining: number
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
  by_subscription: UsageGroup[]
  trend: UsageTrend[]
  tickets: UsageTicket[]
  tickets_cost_unknown: number
  allowance: { state: 'visible' | 'withheld' | 'none'; windows: AllowanceWindow[] }
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
export const paceLabel: Record<AllowanceWindow['pace_model'], string> = {
  steady: 'Steady', frontload: 'Front-loaded', unrestricted: 'Unrestricted',
}
export const unitLabel: Record<AllowanceWindow['unit'], string> = {
  requests: 'requests', tokens: 'tokens', cost_micros: 'cost micros',
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

export function formatWhen(iso: string): string {
  const date = new Date(iso)
  if (Number.isNaN(date.getTime())) return iso
  return new Intl.DateTimeFormat('en-GB', { day: 'numeric', month: 'short', year: 'numeric', timeZone: 'UTC' }).format(date)
}
