// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1036: a person's agents plan with daily limits, as GET /api/agents/plan reads it, and the
// PUT that stores settings. The numbers are the approved design's example (AEON-1030 draft 6):
// Claude on one account, Codex on three accounts with floors, Cursor billed by use (no daily limit).
import type { Page } from '@playwright/test'
import { NOW } from './capacity-fixtures'
import type { DailyAccount, DailyMap, DailySettings, DailyState } from '../src/lib/dailyLimits'

export type DailyExample = 'pace' | 'over' | 'limit' | 'unknown' | 'stale'
export const ME_ID = 'a0000000-0000-4000-8000-0000000000aa'
/** The person's next local midnight in Vienna on the fixture day. */
export const UNTIL = '2026-09-29T22:00:00.000Z'
const acct = (n: number) => `d1000000-0000-4000-8000-0000000000${String(n).padStart(2, '0')}`
const ahead = (hours: number) => new Date(NOW + hours * 3_600_000).toISOString()
const account = (id: number, label: string, order: number, fields: Partial<DailyAccount>): DailyAccount => ({
  account_id: acct(id), label, order, used_pct: null, left_pct: null, start_of_day_used_pct: null, limit_used_pct: null, floor_pct: 0, resets_at: null,
  read_at: new Date(NOW - 20_000).toISOString(), freshness: 'fresh', resets: null, reset_policy: 'suggest', reset_plan: null, routable: true, details_redacted: false, can_edit: true, ...fields,
})
export interface DailyPlanOptions {
  example?: DailyExample; settings?: DailyMap; total?: number; limits?: Record<string, unknown>; running?: Record<string, number>
  /** Every account is the person's to read but not to change (another owner, or private usage). */
  readOnly?: boolean
  /** Harnesses with no account of the person's: the server has no daily state for them. */
  without?: string[]
}

const defaults = (example: DailyExample = 'pace'): DailyMap => ({
  // Over pace and under the limit needs a Boost: without one, today's limit is the pace itself.
  claude: { pace: { mode: 'pace', points_per_day: null }, boost_today: example === 'over' ? { limit_used_pct: 60, entered_as: 'used', until: UNTIL } : null, at_limit: 'ladder' },
  codex: { pace: { mode: 'pace', points_per_day: null }, boost_today: null, at_limit: 'ladder' },
  cursor: { pace: { mode: 'pace', points_per_day: null }, boost_today: null, at_limit: 'ladder' },
})
/** Claude's state follows its saved settings the way the server computes them (limit = start + points, or the Boost). */
export function claudeState(example: DailyExample, settings: DailySettings): DailyState {
  const start = 40, used = { pace: 47, over: 54, limit: 51, unknown: 0, stale: 47 }[example]
  const points = settings.pace.points_per_day ?? 10
  const boost = settings.boost_today, everything = settings.pace.mode === 'everything'
  const limit = Math.min(100, boost ? boost.limit_used_pct : everything ? 100 : start + points)
  const reading = example !== 'unknown'
  const a = account(1, 'markus.barta', 1, reading
    ? { used_pct: used, left_pct: 100 - used, start_of_day_used_pct: start, limit_used_pct: limit, resets_at: ahead(55), freshness: example === 'stale' ? 'stale' : 'fresh', resets: example === 'limit' || example === 'pace' ? { count: 2, expires_at: [ahead(100), ahead(1000)] } : null }
    : { freshness: 'unknown', read_at: null })
  if (!reading) return { state: 'at_limit', limit_used_pct: null, today_points_used: null, today_points_allowed: null, over_pace_points: null, active_account_id: a.account_id, next_on_ladder: null, accounts: [a] }
  const todayUsed = Math.max(0, used - start), allowed = Math.max(0, limit - start)
  const over = everything ? 0 : Math.max(0, used - start - points)
  // The server also says at_limit when the only reading is stale: starts wait, though nothing is measured at the limit.
  const state = used >= limit || example === 'stale' ? 'at_limit' : over > 0 ? 'over_pace' : 'on_pace'
  return { state, limit_used_pct: limit, today_points_used: todayUsed, today_points_allowed: allowed, over_pace_points: over, active_account_id: a.account_id, next_on_ladder: state === 'at_limit' && settings.at_limit === 'ladder' ? 'codex' : null, accounts: [a] }
}
function codexState(): DailyState {
  const rows = [['agentone', 58, 64, 10, 1, 2], ['markus', 20, 22, 20, 2, 3], ['admin', 80, 81, 10, 3, 4]] as const
  const accounts = rows.map(([label, start, used, floor, order, id]) => account(id + 10, label, order, { used_pct: used, left_pct: 100 - used, start_of_day_used_pct: start, floor_pct: floor, limit_used_pct: Math.min(100 - floor, start + 10), resets_at: ahead(24 * order) }))
  return { state: 'on_pace', limit_used_pct: 68, today_points_used: 6, today_points_allowed: 10, over_pace_points: 0, active_account_id: accounts[0]!.account_id, next_on_ladder: null, accounts }
}
const cursorState = (): DailyState => ({ state: 'no_limit', limit_used_pct: null, today_points_used: null, today_points_allowed: null, over_pace_points: null, active_account_id: acct(30), next_on_ladder: null, accounts: [account(30, 'markus@barta.com', 1, { no_daily_limit: true, freshness: 'unknown', read_at: null })] })

/** What the server reads for a plan with these saved settings; a PUT replaces `settings` and the next read follows. */
export function dailyPlan(options: DailyPlanOptions = {}, updatedAt: string | null = null) {
  const example = options.example ?? 'pace', settings = { ...defaults(example), ...options.settings }
  const running = options.running ?? { codex: 6, claude: 2, cursor: 3 }
  return {
    total: options.total ?? 12, limits: options.limits ?? { codex: 6, claude: 2, cursor: 'no_limit' }, principal_id: ME_ID,
    running, running_total: Object.values(running).reduce((n, x) => n + x, 0), source: 'plan', updated_at: updatedAt,
    daily: settings, daily_state: Object.fromEntries(Object.entries({ claude: claudeState(example, settings.claude), codex: codexState(), cursor: cursorState() })
      .filter(([harness]) => !options.without?.includes(harness))
      .map(([harness, state]) => [harness, options.readOnly ? { ...state, accounts: state.accounts.map(a => ({ ...a, can_edit: false })) } : state])),
    daily_default_points: 10, daily_timezone: 'Europe/Vienna', daily_until: UNTIL,
  }
}

export interface DailyBackend { puts: { value: Record<string, unknown>; expected_updated_at: string | null }[]; example: DailyExample; settings: DailyMap; revision: string | null; options: DailyPlanOptions; failPuts: number; conflicts: number }
/** Serves GET /agents/plan and stores PUT /preferences/agents.working, recomputing what the server would read. */
export async function mockDailyPlan(page: Page, options: DailyPlanOptions = {}): Promise<DailyBackend> {
  const backend: DailyBackend = { puts: [], example: options.example ?? 'pace', settings: { ...defaults(options.example), ...options.settings }, revision: null, options: { ...options }, failPuts: 0, conflicts: 0 }
  await page.route('**/api/agents/plan', route => route.fulfill({ json: dailyPlan({ ...backend.options, example: backend.example, settings: backend.settings }, backend.revision) }))
  await page.route('**/api/preferences/agents.working', route => {
    if (route.request().method() !== 'PUT') return route.fallback()
    const body = route.request().postDataJSON() as DailyBackend['puts'][number]
    backend.puts.push(body)
    if (backend.failPuts > 0) { backend.failPuts--; return route.fulfill({ status: 500, json: { error: 'save failed' } }) }
    if (backend.conflicts > 0) { backend.conflicts--; return route.fulfill({ status: 409, json: { error: 'changed' } }) }
    const value = body.value as { total: number; limits: Record<string, unknown>; daily?: DailyMap }
    backend.options.total = value.total; backend.options.limits = value.limits
    if (value.daily) backend.settings = value.daily
    backend.revision = new Date(NOW + backend.puts.length * 1000).toISOString()
    return route.fulfill({ json: { key: 'agents.working', value: body.value, updated_at: backend.revision } })
  })
  return backend
}
