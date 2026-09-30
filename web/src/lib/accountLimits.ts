// SPDX-License-Identifier: AGPL-3.0-only
// Settings → Accounts (AEON-384): the words for how an account is read, its
// windows and last readings, inline rename, and the one Advanced sentence
// ("Let agents use at most 20% of this account per day") that replaced the
// allowance form. Limits set by hand before it show as "Set by you" with
// Remove and Make this repeat. A limit caps on top of the readings; it never
// replaces them.
import { api, APIError } from './api.ts'
import type { AgentAccount, AllowanceWindow } from './agents.ts'
import { brand } from './brand.ts'
import { ago, HARNESS_NAME, pct, when, type CapacityReading, type CapacityWindow } from './capacity.ts'

export type LimitUnit = 'percent' | 'runs' | 'requests' | 'tokens' | 'cost_micros'
export type LimitPeriod = 'day' | 'week' | 'month'
export interface LimitWrite { amount: number; unit: LimitUnit; period: LimitPeriod }
export interface LimitRule extends LimitWrite { id: string; account_id: string; from_window_id?: string; created_at: string }
/** A rule with what it counted this period (the capacity projection). */
export interface LimitUse extends LimitRule { used: number; period_end: string }

// ---------- HTTP ----------
async function request<T>(path: string, method = 'GET', body?: unknown): Promise<T> {
  const response = await api(path, { method, ...(body === undefined ? {} : { headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) }) })
  if (!response.ok) {
    const data = await response.json().catch(() => ({}))
    throw new APIError(response.status, typeof data?.error === 'string' ? data.error : `Request failed (${response.status})`, data)
  }
  return response.status === 204 ? (undefined as T) : response.json()
}
const enc = encodeURIComponent
const base = (id: string) => `/agent-accounts/${enc(id)}`
export const putLimit = (id: string, body: LimitWrite) => request<LimitRule>(`${base(id)}/limit`, 'PUT', body)
export const removeLimit = (id: string) => request<void>(`${base(id)}/limit`, 'DELETE')
export const removeWindow = (id: string, windowId: string) => request<void>(`${base(id)}/windows/${enc(windowId)}`, 'DELETE')
export const repeatWindow = (id: string, windowId: string) => request<LimitRule>(`${base(id)}/windows/${enc(windowId)}/repeat`, 'POST')
/** Changes the display name only; plan and model grants stay as they are. */
export const renameAccount = (id: string, label: string) => request<AgentAccount>(`${base(id)}/label`, 'PUT', { label })
export const listReadings = (id: string) => request<CapacityReading[]>(`${base(id)}/readings`)

// ---------- How an account is read ----------
/** Vendors whose limits Aeon cannot see. */
export const BLIND = new Set(['grok', 'cursor', 'pi'])
export function readingSupport(harness: string): string {
  if (harness === 'codex') return 'Reads every 5 min'
  if (harness === 'claude') return 'Reads during runs'
  return "Doesn't show its limit"
}
/** The honest line for an account without a current window. */
export function noWindowLine(harness: string, host: string): string {
  const name = HARNESS_NAME[harness] ?? harness
  if (harness === 'claude') return 'Reads with its first run. Agents can start now.'
  if (BLIND.has(harness)) return `${name} doesn't show its limit to ${brand.value.short_name}. One run at a time by day, freely tonight.`
  return host ? `Reading your limits on ${host}…` : 'Reading your limits…'
}

const KIND_RANK: Record<string, number> = { monthly: 0, weekly: 1, other: 2, '5h': 3 }
const KIND_NAME: Record<string, string> = { '5h': '5-hour', weekly: 'Weekly', monthly: 'Monthly', other: 'Window' }
/** Long windows first, the general bucket before per-model ones. */
export const sortWindows = (windows: CapacityWindow[]) => [...windows].sort((a, b) =>
  (KIND_RANK[a.reading.window_kind] ?? 9) - (KIND_RANK[b.reading.window_kind] ?? 9) || (a.reading.bucket ? 1 : 0) - (b.reading.bucket ? 1 : 0) || (a.reading.bucket ?? '').localeCompare(b.reading.bucket ?? ''))
/** "Weekly", "5-hour", "Weekly · Opus" (seven_day_opus). A bucket named after the vendor is the general one. */
export function windowLabel(w: CapacityWindow, harness: string): string {
  const kind = KIND_NAME[w.reading.window_kind] ?? 'Window'
  const bucket = (w.reading.bucket ?? '').replace(/^(five_hour|seven_day|weekly|monthly)_?/, '')
  if (!bucket || bucket === harness) return kind
  return `${kind} · ${bucket.split('_').map(s => s.charAt(0).toUpperCase() + s.slice(1)).join(' ')}`
}
/** Who measured it and how old it is. */
export function sourceText(r: CapacityReading, harness: string, host: string, now: number): string {
  if (r.source === 'harness') return `${HARNESS_NAME[harness] ?? harness} reported · ${ago(r.read_at, now)}`
  if (r.source === 'agentd') return `Read on ${host} · ${ago(r.read_at, now)}`
  return `Estimated · ${ago(r.read_at, now)}`
}
export function windowFacts(w: CapacityWindow, now: number, timezone?: string): { left: string; reset: string; stale: boolean } {
  return {
    left: `${pct(w.remaining_percent)} left`,
    reset: Date.parse(w.reading.resets_at) > now ? `resets ${when(w.reading.resets_at, now, timezone)}` : `reset ${when(w.reading.resets_at, now, timezone)} · waiting for a fresh reading`,
    stale: w.freshness === 'stale' || w.freshness === 'expired',
  }
}
/** The last readings of the window the bar shows, newest first, one per moment. */
export function recentReadings(readings: CapacityReading[], primary: CapacityWindow | null, n = 3): CapacityReading[] {
  const same = (r: CapacityReading) => !primary || (r.window_kind === primary.reading.window_kind && (r.bucket ?? '') === (primary.reading.bucket ?? ''))
  const seen = new Set<string>()
  return [...readings].filter(r => r.source !== 'estimate' && same(r)).sort((a, b) => Date.parse(b.read_at) - Date.parse(a.read_at))
    .filter(r => (seen.has(r.read_at) ? false : (seen.add(r.read_at), true))).slice(0, n)
}
export function readingWho(r: CapacityReading, harness: string, host: string): string {
  return r.source === 'harness' ? `${HARNESS_NAME[harness] ?? harness} reported` : `read on ${host}`
}

// ---------- Names ----------
/** Accounts whose name repeats within one vendor: each gets a "Name it" prompt. */
export function nameClashes(accounts: Pick<AgentAccount, 'id' | 'harness' | 'label'>[]): Set<string> {
  const byKey = new Map<string, string[]>()
  for (const a of accounts) {
    const key = `${a.harness}/${a.label.trim().toLocaleLowerCase()}`
    byKey.set(key, [...(byKey.get(key) ?? []), a.id])
  }
  return new Set([...byKey.values()].filter(ids => ids.length > 1).flat())
}
export function renameProblem(label: string): string | null {
  const next = label.trim()
  if (!next) return 'Give the account a name.'
  if ([...next].length > 128) return 'Keep the name to 128 characters.'
  return null
}

// ---------- Amounts ----------
const count = (n: number) => new Intl.NumberFormat('en-US', { maximumFractionDigits: 0 }).format(n)
export function dollars(micros: number): string {
  const d = micros / 1e6
  return `$${d.toLocaleString('en-US', { minimumFractionDigits: Number.isInteger(d) ? 0 : 2, maximumFractionDigits: 2 })}`
}
/** "20%", "5 runs", "1 run", "$50", "100 requests", "2,000,000 tokens". */
export function amountText(amount: number, unit: LimitUnit | AllowanceWindow['unit']): string {
  switch (unit) {
    case 'percent': return `${Math.round(amount)}%`
    case 'cost_micros': return dollars(amount)
    case 'runs': return `${count(amount)} ${amount === 1 ? 'run' : 'runs'}`
    case 'requests': return `${count(amount)} ${amount === 1 ? 'request' : 'requests'}`
    default: return `${count(amount)} tokens`
  }
}
const PERIOD_EACH: Record<LimitPeriod, string> = { day: 'a day', week: 'a week', month: 'a month' }
const PERIOD_THIS: Record<LimitPeriod, string> = { day: 'today', week: 'this week', month: 'this month' }
/** "20% a day": the rule in a few words, for chips and tooltips. */
export const limitSummary = (rule: LimitWrite) => `${amountText(rule.amount, rule.unit)} ${PERIOD_EACH[rule.period]}`
/**
 * How far the rule got this period: "12% used today", "1 of 5 runs this week",
 * "$12.40 of $50 this month", then "reached until tomorrow 00:00" when used up.
 */
export function limitUseLine(use: LimitUse, now: number, timezone?: string): string {
  const reached = use.used >= use.amount
  const head = use.unit === 'percent' ? `${pct(use.used)} used ${PERIOD_THIS[use.period]}`
    : `${use.unit === 'cost_micros' ? dollars(use.used) : count(use.used)} of ${amountText(use.amount, use.unit)} ${PERIOD_THIS[use.period]}`
  return reached ? `${head} · reached until ${when(use.period_end, now, timezone)}` : head
}
/**
 * Whether the limit leaves agents less today than the plan would: the percent
 * left under the rule against today's share left on the bar's window.
 */
export function limitBinds(use: LimitUse | undefined, primary: CapacityWindow | null): boolean {
  if (!use || use.unit !== 'percent' || !primary) return false
  const p = primary.pacing
  const planLeft = Math.max(0, (p.budget_percent ?? p.suggested_today_percent) - (p.used_today_percent ?? 0))
  const ruleLeft = Math.max(0, use.amount - use.used)
  return ruleLeft < planLeft - 0.5
}

// ---------- The Advanced sentence ----------
/** The sentence's menu: "%" of the account, runs, dollars; requests or tokens only for a repeated old window. */
export type DraftUnit = 'percent' | 'runs' | 'dollars' | 'requests' | 'tokens'
export interface LimitDraft { amount: string; unit: DraftUnit; period: LimitPeriod }
export const UNIT_WORD: Record<DraftUnit, string> = { percent: '%', runs: 'runs', dollars: 'dollars', requests: 'requests', tokens: 'tokens' }
export const PERIODS: LimitPeriod[] = ['day', 'week', 'month']
const toDraftUnit = (unit: LimitUnit): DraftUnit => (unit === 'cost_micros' ? 'dollars' : unit)
/**
 * What the menu offers an account, the likeliest first: a percent when Aeon
 * reads its limit, dollars when it is billed by API key, runs always, and a
 * repeated old unit.
 */
export function unitChoices(input: { harness: string; measured: boolean; money: boolean; current?: LimitUnit }): DraftUnit[] {
  const out: DraftUnit[] = []
  if (input.measured || input.harness === 'codex' || input.harness === 'claude') out.push('percent')
  if (input.money) out.push('dollars')
  out.push('runs')
  const current = input.current ? toDraftUnit(input.current) : null
  if (current && (current !== 'dollars' || input.money) && !out.includes(current)) out.push(current)
  return out
}
export function draftFor(rule: LimitWrite | null | undefined, choices: DraftUnit[]): LimitDraft {
  if (!rule) return { amount: '', unit: choices[0] ?? 'runs', period: 'day' }
  const unit = toDraftUnit(rule.unit)
  // Dollars keep every micro a repeated old limit had, so an untouched draft is no change.
  const amount = unit === 'dollars' ? String(rule.amount / 1e6) : String(rule.amount)
  return { amount, unit, period: rule.period }
}
/** The draft as the server's sentence, or the one thing to fix. */
export function draftWrite(d: LimitDraft): { ok: true; body: LimitWrite } | { ok: false; message: string } {
  const text = d.amount.trim().replace(/^\$/, '')
  if (d.unit === 'dollars') {
    if (!/^\d+(\.\d{1,6})?$/.test(text) || Number(text) <= 0) return { ok: false, message: 'Enter an amount in dollars, like 50.' }
    return { ok: true, body: { amount: Math.round(Number(text) * 1e6), unit: 'cost_micros', period: d.period } }
  }
  if (!/^\d+$/.test(text) || Number(text) < 1) return { ok: false, message: d.unit === 'percent' ? 'Enter a whole number from 1 to 100.' : 'Enter a whole number above 0.' }
  const amount = Number(text)
  if (d.unit === 'percent' && amount > 100) return { ok: false, message: 'Enter a whole number from 1 to 100.' }
  if (!Number.isSafeInteger(amount)) return { ok: false, message: 'Enter a smaller number.' }
  return { ok: true, body: { amount, unit: d.unit, period: d.period } }
}
export const sameLimit = (a: LimitWrite | null | undefined, b: LimitWrite | null | undefined) =>
  !!a && !!b && a.amount === b.amount && a.unit === b.unit && a.period === b.period

// ---------- Limits set by hand before the sentence ----------
/** The old manual windows still in force or still to come. */
export const setByYou = (windows: AllowanceWindow[] | undefined, now: number) =>
  (windows ?? []).filter(w => w.set_by_you && Date.parse(w.ends_at) > now).sort((a, b) => Date.parse(a.starts_at) - Date.parse(b.starts_at))
const MO = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec']
function ymd(at: number, timezone?: string) {
  const f = new Intl.DateTimeFormat('en-GB', { timeZone: timezone, year: 'numeric', month: 'numeric', day: 'numeric' })
  const p = Object.fromEntries(f.formatToParts(new Date(at)).map(x => [x.type, x.value]))
  return { y: +p.year, m: +p.month - 1, d: +p.day }
}
/** "1–31 Oct", "15 Sep – 15 Oct", "20 Dec 2026 – 10 Jan 2027"; the end is inclusive. */
export function dateRange(startsAt: string, endsAt: string, timezone?: string): string {
  const a = ymd(Date.parse(startsAt), timezone)
  const b = ymd(Date.parse(endsAt) - 60_000, timezone)
  if (a.y === b.y && a.m === b.m) return a.d === b.d ? `${a.d} ${MO[a.m]}` : `${a.d}–${b.d} ${MO[a.m]}`
  if (a.y === b.y) return `${a.d} ${MO[a.m]} – ${b.d} ${MO[b.m]}`
  return `${a.d} ${MO[a.m]} ${a.y} – ${b.d} ${MO[b.m]} ${b.y}`
}
/** "100 requests · 1–31 Oct" (the row says "Set by you"). */
export const setByYouText = (w: AllowanceWindow, timezone?: string) => `${amountText(w.allowance, w.unit)} · ${dateRange(w.starts_at, w.ends_at, timezone)}`
/** Money for an account billed by API key: "$12.40 this month · no limit", or against a dollar limit. */
export function spendLine(usd: string, limit: LimitUse | undefined, now: number, timezone?: string): string {
  const spent = `$${usd}`
  if (limit?.unit === 'cost_micros' && limit.period === 'month') return `${spent} of ${dollars(limit.amount)} · resets ${when(limit.period_end, now, timezone)}`
  return `${spent} this month${limit?.unit === 'cost_micros' ? '' : ' · no limit'}`
}
