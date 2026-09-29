// SPDX-License-Identifier: AGPL-3.0-only
// The merged Usage page (AEON-301): the four summary tiles, durations, waste
// words and one capacity row per vendor pool. A fact with no data is left out
// or replaced by the sentence that says what is missing, never by a dash.
// Pure, so every partial state is unit tested (tests/usageWork.test.ts).
import { accountPlan, gauge, pct, when, whenFull, type AccountRow, type Gauge, type PoolView } from './capacity.ts'
import { reworkDetail, reworkPercent } from './deliveryRating.ts'
import { compactCount, formatCount, formatUSD, type UsageDashboard, type UsageWork, type WasteItem, type WorkGroup } from './usageFormat.ts'

export const HARNESS: Record<string, string> = { codex: 'Codex', claude: 'Claude', grok: 'Grok', cursor: 'Cursor', pi: 'Pi' }
export const harnessName = (h: string) => HARNESS[h] ?? (h ? h[0].toUpperCase() + h.slice(1) : h)
export const plural = (n: number, one: string, many = `${one}s`) => `${formatCount(n)} ${n === 1 ? one : many}`

/** "<1 min", "25 min", "4 h 12 min", "38 h". */
export function duration(seconds: number): string {
  if (!Number.isFinite(seconds) || seconds < 60) return '<1 min'
  const minutes = Math.round(seconds / 60)
  if (minutes < 60) return `${minutes} min`
  const h = Math.floor(minutes / 60), m = minutes % 60
  if (h < 10) return m ? `${h} h ${m} min` : `${h} h`
  return `${formatCount(Math.round(seconds / 3600))} h`
}

/** "12 min", "3 h", "2 d" since an instant. */
export function since(iso: string, now: number): string {
  const minutes = Math.max(0, Math.round((now - Date.parse(iso)) / 60_000))
  if (minutes < 60) return `${Math.max(1, minutes)} min`
  const hours = Math.round(minutes / 60)
  return hours < 48 ? `${hours} h` : `${Math.round(hours / 24)} d`
}

export function sumTokens(values: (string | null)[]): string | null {
  let total: bigint | null = null
  for (const v of values) if (v !== null && /^(0|[1-9]\d*)$/.test(v)) total = (total ?? 0n) + BigInt(v)
  return total === null ? null : total.toString()
}

/** "reported by all 12 sessions", "reported by 3 of 357 sessions". */
export function coverage(part: number, whole: number, verb = 'reported by'): string {
  if (whole <= 0) return ''
  if (part >= whole) return whole === 1 ? `${verb} the one session` : `${verb} all ${formatCount(whole)} sessions`
  return `${verb} ${formatCount(part)} of ${plural(whole, 'session')}`
}

export interface Tile {
  key: 'done' | 'cost' | 'time' | 'waste'
  /** The bold figure; null turns the tile into a sentence (the reason). */
  value: string | null
  label: string
  detail: string
  note: string
  tip?: string
}

export function tiles(d: Pick<UsageDashboard, 'totals' | 'work' | 'ratings'>): Tile[] {
  const w = d.work
  // Rework by exception (AEON-218): shown only when someone flagged a delivery.
  const r = d.ratings
  const rework = r ? reworkPercent(r.exceptions, r.deliveries) : ''
  const done: Tile = {
    key: 'done', value: formatCount(w.done), label: w.done === 1 ? 'ticket done' : 'tickets done',
    detail: [w.released ? `${formatCount(w.released)} released` : '', rework ? `${rework} rework` : ''].filter(Boolean).join(' · '),
    note: w.tickets_worked ? `${plural(w.tickets_worked, 'ticket')} worked on` : '',
    tip: `Tickets finished in this range that an agent worked on.${rework ? ` Rework: ${reworkDetail(r!.exceptions, r!.deliveries)} deliveries were flagged.` : ''}`,
  }
  const tokens = sumTokens(w.by_harness.map(g => g.tokens))
  const api = d.totals.cost_known_rows > 0 && d.totals.estimated_cost_usd !== null ? formatUSD(d.totals.estimated_cost_usd) : ''
  const split = w.by_harness.filter(g => g.tokens !== null).sort((a, b) => Number(BigInt(b.tokens!) - BigInt(a.tokens!)))
  const cost: Tile = tokens === null
    ? {
        key: 'cost', value: null, label: 'Cost is not measured yet',
        detail: w.sessions ? coverage(w.usage_reported_sessions, w.sessions) : '',
        note: '',
        tip: 'Sessions report tokens through their heartbeat when it has a usage source; capacity readings come from the accounts themselves.',
      }
    : {
        key: 'cost', value: compactCount(tokens), label: 'tokens',
        detail: api ? `${api} at API list price` : split.length > 1 ? split.slice(0, 2).map(g => `${harnessName(g.key)} ${compactCount(g.tokens)}`).join(' · ') : split[0] ? `all ${harnessName(split[0].key)}` : '',
        note: coverage(w.usage_reported_sessions, w.sessions),
        tip: `${formatCount(Number(tokens))} input and output tokens. Subscriptions are measured in capacity, not dollars.`,
      }
  const time: Tile = w.timed_sessions
    ? {
        key: 'time', value: duration(w.agent_seconds), label: 'agent time',
        detail: `across ${plural(w.sessions, 'session')}`,
        note: w.timed_sessions < w.sessions ? coverage(w.timed_sessions, w.sessions, 'timed for') : '',
        tip: 'From each session’s start to its last heartbeat.',
      }
    : { key: 'time', value: null, label: 'Agent time is not known yet', detail: w.sessions ? 'no session has sent a heartbeat' : '', note: '' }
  const x = w.waste
  // A count stays on one line with its words (non-breaking spaces).
  const parts = [
    x.stuck && `${formatCount(x.stuck)}\u00a0stuck`,
    x.no_result && `${formatCount(x.no_result)}\u00a0no\u00a0commit`,
    x.lost && `${formatCount(x.lost)}\u00a0lost\u00a0contact`,
    x.failed && `${formatCount(x.failed)}\u00a0failed`,
    x.retried && `${formatCount(x.retried)}\u00a0retried`,
  ].filter(Boolean) as string[]
  const waste: Tile = {
    key: 'waste', value: formatCount(x.total), label: x.total === 1 ? 'run with nothing to show' : 'runs with nothing to show',
    detail: parts.length ? parts.join(' · ') : 'none stuck, failed or empty',
    note: '',
    tip: 'Worker sessions on a ticket that is not done that left no commit, lost contact, failed, or are stuck now; three or more tries count once.',
  }
  return [done, cost, time, waste]
}

export interface WasteWords { title: string; detail: string; action: 'session' | 'ticket' }
export function wasteWords(item: WasteItem, now: number): WasteWords {
  switch (item.kind) {
    case 'stuck': return { title: 'Stuck', detail: `silent for ${since(item.at, now)}`, action: 'session' }
    case 'failed': return { title: 'Failed', detail: 'ended in an error', action: 'session' }
    case 'lost': return { title: 'Lost contact', detail: 'went silent, no commit', action: 'session' }
    case 'no_result': return { title: 'Nothing to show', detail: 'no commit', action: 'session' }
    case 'retried': return { title: `Tried ${item.sessions} times`, detail: 'not done yet', action: 'ticket' }
  }
}

/** Groups for the breakdown tabs, with the coverage note each tab needs. */
export type Breakdown = 'harness' | 'model' | 'project'
export function breakdown(w: UsageWork, by: Breakdown): { rows: WorkGroup[]; note: string } {
  if (by === 'harness') return { rows: w.by_harness, note: '' }
  if (by === 'project') return { rows: w.by_project, note: '' }
  const note = w.model_sessions < w.sessions ? `Model registered by ${formatCount(w.model_sessions)} of ${plural(w.sessions, 'session')}.` : ''
  return { rows: w.by_model, note }
}

// ---------- Capacity now: one row per vendor pool ----------
export interface BandRow {
  pool: PoolView
  /** Pooled gauge: the plain mean of the accounts with a reading. */
  gauge: Gauge | null
  left: number | null
  /** Today's spend against today's share, pooled; empty when there is no plan. */
  today: { used: string; share: string; ahead: boolean } | null
  /** window names the reset's window only when the pool has two kinds; kind always does. */
  reset: { at: string; label: string; full: string; window: string; kind: string } | null
  accounts: number
  measured: number
}

const WINDOW_WORD: Record<string, string> = { '5h': '5-hour', weekly: 'weekly', monthly: 'monthly', other: '' }

export function bandRows(pools: PoolView[], now: number): BandRow[] {
  const rows = pools.map(pool => {
    const measured = pool.rows.filter(r => r.primary)
    const plans = measured.map(r => ({ r, plan: accountPlan(r, now) }))
    const gauges = plans.map(({ r, plan }) => gauge(r, plan))
    const mean = (xs: number[]) => xs.reduce((a, b) => a + b, 0) / xs.length
    const g: Gauge | null = gauges.length ? {
      later: mean(gauges.map(x => x.later)), today: mean(gauges.map(x => x.today)), spent: mean(gauges.map(x => x.spent)),
      tick: gauges.every(x => x.tick !== null) ? mean(gauges.map(x => x.tick!)) : null, frozen: gauges.every(x => x.frozen),
    } : null
    const live = plans.filter(({ r, plan }) => r.state === 'live' && plan && !plan.override && !(plan.dayOff && !plan.expiring))
    const today = live.length ? {
      used: pct(mean(live.map(x => x.plan!.used))),
      share: `~${pct(mean(live.map(x => x.plan!.budget)))}`,
      ahead: live.some(x => x.plan!.ahead),
    } : null
    return { pool, gauge: g, left: measured.length ? mean(measured.map(r => r.primary!.remaining_percent)) : null, today, reset: soonestReset(pool.rows, now), accounts: pool.rows.length, measured: measured.length }
  })
  // What binds first comes first; pools without a reading last.
  return rows.sort((a, b) => (a.reset ? Date.parse(a.reset.at) : Infinity) - (b.reset ? Date.parse(b.reset.at) : Infinity) || a.pool.name.localeCompare(b.pool.name))
}

function soonestReset(rows: AccountRow[], now: number): BandRow['reset'] {
  let best: { at: string; kind: string } | null = null
  for (const r of rows) for (const w of [r.primary, r.five]) {
    if (!w || Date.parse(w.reading.resets_at) <= now) continue
    if (!best || Date.parse(w.reading.resets_at) < Date.parse(best.at)) best = { at: w.reading.resets_at, kind: w.reading.window_kind }
  }
  if (!best) return null
  // Name the window only when the pool has more than one kind; the plan says it otherwise.
  const kinds = new Set(rows.flatMap(r => [r.primary?.reading.window_kind, r.five?.reading.window_kind]).filter(Boolean))
  const window = WINDOW_WORD[best.kind] ?? ''
  return { at: best.at, label: when(best.at, now), full: whenFull(best.at), window: kinds.size > 1 ? window : '', kind: window }
}
