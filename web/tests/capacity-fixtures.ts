// SPDX-License-Identifier: AGPL-3.0-only
// The Agents desk's capacity world from the approved prototype (AEON-292/299):
// Codex Spare, Main and Studio (offline), Claude with 5-hour and weekly windows,
// Grok in a fresh week and Cursor on a monthly estimate. The mock paces with a
// small stand-in for the server formula: it reacts to the schedule, Sprint,
// Hold, Away and Keep for you (a runway over the person's hours) so the header
// states and editors can be exercised, not to prove pacing.
import { pairingEnrollment, pairingView } from './agent-pairing-fixtures'

export const NOW = Date.parse('2026-09-29T12:02:00Z') // Tue 14:02 in Vienna
export const TZ = 'Europe/Vienna'
const uuid = (n: number) => `c0000000-0000-4000-8000-0000000000${String(n).padStart(2, '0')}`
export const ACCOUNTS = { spare: uuid(1), main: uuid(2), studio: uuid(3), claude: uuid(4), grok: uuid(5), cursor: uuid(6), pi: uuid(7) }
/** A limit set by hand before the Advanced sentence (AEON-384): pairing's ongoing requests on Spare. */
export const OLD_WINDOW = 'f0000000-0000-4000-8000-000000000001'
const MBP = 'd0000000-0000-4000-8000-000000000001', STUDIO = 'd0000000-0000-4000-8000-000000000002'
const iso = (s: string) => new Date(Date.parse(s)).toISOString()
const minutesAgo = (m: number) => new Date(NOW - m * 60_000).toISOString()

type Schedule = Record<string, unknown> & { week: { on: boolean; start: number; end: number }[]; nights: boolean; off_days: string; override?: string; override_until?: string; reserve?: string; reserve_percent?: number }
export const defaultSchedule = (): Schedule => ({
  timezone: TZ, week: Array.from({ length: 7 }, (_, i) => ({ on: i < 5, start: 8, end: 22 })), off_days: 'expire', nights: false, model: 'daynight',
  night: { start: 22, end: 8, k: 0.6 }, shifts: { early: 6, late: 14, night: 22, k: [1, 1, 0.5] }, blocks: Array.from({ length: 24 }, (_, h) => (h >= 8 && h < 22 ? 1 : 0.5)),
})

interface Win { kind: string; used: number; usedToday: number; budget: number; reset: string; start: string; source: string; readMin: number; finish?: string; plan?: string }
interface Acct { id: string; label: string; harness: string; host: string; hostLabel?: string; plan: string; state?: string; probe?: boolean; failure?: string; windows: Win[] }
export interface CapacityOptions {
  signin?: boolean; stale?: boolean; noCursor?: boolean
  /** Grok's last check failed without a confirmed sign-out. */
  unavailable?: boolean
  /** Studio (Codex) has no reading yet: the Codex pool is measured by 2 of 3 accounts. */
  unmeasured?: boolean
  /** mbp2607's setup reports login_required (computer-wide, not per account). */
  computerLogin?: boolean
  /**
   * The one-time plan card: 'first' has no saved schedule yet, 'new' a release 11
   * schedule without Keep for you. By default Keep for you is confirmed (Auto).
   */
  planCard?: 'first' | 'new'
  /** The person's Keep for you, when not Auto. */
  reserve?: 'off' | number
  /** Away until Monday 08:00, set on the person's schedule. */
  away?: boolean
  /**
   * Settings / Accounts (AEON-384): Main has the Advanced sentence (20% a day,
   * 12% used), Spare an old limit set by hand, Grok 5 runs a week (3 so far).
   */
  limits?: boolean
  /** A Pi account on an API key, with list-price spend this month. */
  apiKey?: boolean
  /** Studio is also called Main, so both Codex rows ask to be named. */
  clash?: boolean
  /** Long account labels and host names, for the accounts-card width sweep. */
  longNames?: boolean
  /** A Pi pool with one account and no reading yet. */
  unread?: boolean
}

export function capacityWorld(options: CapacityOptions = {}) {
  const accts: Acct[] = [
    { id: ACCOUNTS.spare, label: 'Spare', harness: 'codex', host: 'mbp2607', plan: 'Pro', windows: [{ kind: 'weekly', used: 91, usedToday: 2, budget: 6, reset: '2026-09-30T16:02:00Z', start: '2026-09-23T16:02:00Z', source: 'harness', readMin: 9, plan: 'Pro' }] },
    { id: ACCOUNTS.main, label: 'Main', harness: 'codex', host: 'mbp2607', plan: 'Pro', windows: [{ kind: 'weekly', used: 58, usedToday: 3, budget: 15, reset: '2026-10-02T07:14:00Z', start: '2026-09-25T07:14:00Z', source: 'harness', readMin: 2, plan: 'Pro' }] },
    { id: ACCOUNTS.studio, label: 'Studio', harness: 'codex', host: 'studio', plan: 'Pro', windows: [{ kind: 'weekly', used: 22, usedToday: 0, budget: 0, reset: '2026-10-05T05:40:00Z', start: '2026-09-28T05:40:00Z', source: 'agentd', readMin: 180, plan: 'Pro' }] },
    { id: ACCOUNTS.claude, label: 'markus', harness: 'claude', host: 'mbp2607', plan: 'Max', windows: [
      { kind: 'weekly', used: 63, usedToday: 4, budget: 10, reset: '2026-10-04T09:00:00Z', start: '2026-09-27T09:00:00Z', source: 'harness', readMin: 6, finish: '2026-10-02T20:00:00Z', plan: 'Max' },
      { kind: '5h', used: 40, usedToday: 0, budget: 60, reset: '2026-09-29T14:40:00Z', start: '2026-09-29T09:40:00Z', source: 'harness', readMin: 6, plan: 'Max' },
    ] },
    { id: ACCOUNTS.grok, label: 'markus', harness: 'grok', host: 'mbp2607', plan: 'SuperGrok Heavy', ...(options.unavailable ? { probe: false, failure: 'unavailable' } : {}), windows: [{ kind: 'weekly', used: 0, usedToday: 0, budget: 13, reset: '2026-10-06T11:10:00Z', start: '2026-09-29T11:10:00Z', source: 'agentd', readMin: options.stale ? 400 : 12, finish: '2026-10-06T11:10:00Z' }] },
  ]
  if (options.unmeasured) accts.find(a => a.id === ACCOUNTS.studio)!.windows = []
  if (options.clash) accts.find(a => a.id === ACCOUNTS.studio)!.label = 'Main'
  if (options.apiKey) accts.push({ id: ACCOUNTS.pi, label: 'OpenRouter key', harness: 'pi', host: 'mbp2607', plan: '', windows: [] })
  if (!options.noCursor) accts.push({ id: ACCOUNTS.cursor, label: 'markus', harness: 'cursor', host: 'mbp2607', plan: 'Pro', state: options.signin ? 'unavailable' : 'available', probe: !options.signin, ...(options.signin ? { failure: 'auth_failed' } : {}), windows: [{ kind: 'monthly', used: 43, usedToday: 7, budget: 6, reset: '2026-10-14T07:00:00Z', start: '2026-09-14T07:00:00Z', source: 'estimate', readMin: options.signin ? 2 * 24 * 60 : 20 }] })
  if (options.unread) accts.push({ id: uuid(7), label: options.longNames ? 'Pi on the home server waiting for its first run' : 'Pi on hsb1', harness: 'pi', host: 'mbp2607', plan: '', windows: [] })
  if (options.longNames) {
    const labels: Record<string, string> = {
      [ACCOUNTS.spare]: 'Spare workstation account for the Tuesday release train',
      [ACCOUNTS.main]: 'Main production subscription shared by the whole studio',
      [ACCOUNTS.studio]: 'Studio offline machine in the Graz office rack',
      [ACCOUNTS.claude]: 'markus on the long-lived Claude Max seat',
      [ACCOUNTS.grok]: 'markus on the studio SuperGrok Heavy seat',
      [ACCOUNTS.cursor]: 'markus on the Cursor Business seat for reviews',
    }
    const hosts: Record<string, string> = { mbp2607: 'mbp2607-markus-primary', studio: 'graz-studio-rack-07' }
    for (const a of accts) {
      if (labels[a.id]) a.label = labels[a.id]
      a.hostLabel = hosts[a.host] ?? a.host
    }
  }

  const schedules: { scope: string; pool?: string; account_id?: string; schedule: Schedule }[] = []
  if (options.planCard !== 'first') {
    const user: Schedule = defaultSchedule()
    if (options.planCard !== 'new') Object.assign(user, typeof options.reserve === 'number' ? { reserve: 'fixed', reserve_percent: options.reserve } : { reserve: options.reserve ?? 'auto' })
    if (options.away) Object.assign(user, { override: 'away', override_until: iso('2026-10-05T06:00:00Z') })
    schedules.push({ scope: 'user', schedule: user })
  }
  const live = (x: Schedule) => !!x.override && !(x.override_until && Date.parse(x.override_until) <= NOW)
  // The server's resolveSchedule: the most specific shape, Away from the person's
  // schedule where no override of its own is in force, the most specific reserve.
  const resolveChain = (chain: Schedule[]): Schedule => {
    const s: Schedule = { ...chain[0] }
    const top = chain[chain.length - 1]
    if (!live(s) && live(top) && top.override === 'away') Object.assign(s, { override: 'away', override_until: top.override_until })
    delete s.reserve; delete s.reserve_percent
    const own = chain.find(c => c.reserve)
    if (own) Object.assign(s, { reserve: own.reserve, ...(own.reserve === 'fixed' ? { reserve_percent: own.reserve_percent } : {}) })
    return s
  }
  const entryOf = (a: Acct) => ({ account: schedules.find(e => e.scope === 'account' && e.account_id === a.id)?.schedule, pool: schedules.find(e => e.scope === 'pool' && e.pool === a.harness)?.schedule })
  const resolve = (a: Acct): Schedule => {
    const { account, pool } = entryOf(a)
    return resolveChain([account, pool, schedules.find(e => e.scope === 'user')?.schedule ?? defaultSchedule()].filter(Boolean) as Schedule[])
  }
  const effective = (a: Acct): Schedule => resolve(a)
  // The runway stand-in: the person's work-band hours from now to the reset, on
  // a half-hour walk in Vienna, against min(window, one work-day band).
  const localHour = (at: number) => { const p = new Intl.DateTimeFormat('en-GB', { timeZone: TZ, weekday: 'short', hour: '2-digit', minute: '2-digit', hourCycle: 'h23' }).formatToParts(new Date(at)); const get = (t: string) => p.find(x => x.type === t)!.value; return { wd: ['Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat', 'Sun'].indexOf(get('weekday')), h: +get('hour') + +get('minute') / 60 } }
  function runway(s: Schedule, reset: number, minutes: number, level: number) {
    const band = Math.max(...s.week.filter(d => d.on).map(d => d.end - d.start))
    const ref = Math.min(band, minutes / 60)
    let hours = 0, until = 0
    for (let t = reset; t > NOW && hours < ref; t -= 30 * 60_000) {
      const from = Math.max(NOW, t - 30 * 60_000)
      const { wd, h } = localHour((from + t) / 2)
      const d = s.week[wd]
      if (d.on && h >= d.start && h < d.end) { hours += (t - from) / 3600e3; until ||= t }
    }
    return { reserve: until ? level * Math.min(1, hours / ref) : 0, until }
  }
  // Stand-in pacing: fewer work days give a larger daily share, nights add a
  // share tonight, a day off rests unless capacity would expire.
  function pace(w: Win, s: Schedule) {
    const onDays = s.week.filter(d => d.on).length || 1
    const tuesdayOn = s.week[1].on || s.off_days === 'normal'
    const override = live(s) ? s.override ?? '' : ''
    const left = 100 - w.used, leftAtStart = left + w.usedToday
    let budget = Math.min(leftAtStart, w.budget * (5 / onDays) * (s.nights ? 1.2 : 1))
    let tonight = s.nights ? budget * 0.2 : 0
    let allowOff = false, unused = false
    if (!tuesdayOn) {
      const soon = Date.parse(w.reset) - NOW < 30 * 3600e3
      if (soon && s.off_days === 'expire') { allowOff = true; budget = leftAtStart; tonight = 0 }
      else { budget = w.usedToday; tonight = 0; unused = soon && left > 0 }
    }
    if (override === 'sprint' || override === 'away') { budget = leftAtStart; tonight = 0 }
    if (override === 'hold') { budget = w.usedToday; tonight = 0 }
    const level = s.reserve === 'off' ? 0 : s.reserve === 'fixed' ? s.reserve_percent ?? 30 : 30
    const kept = level && override !== 'sprint' && override !== 'away' ? runway(s, Date.parse(w.reset), w.kind === '5h' ? 300 : w.kind === 'monthly' ? 43200 : 10080, level) : { reserve: 0, until: 0 }
    const share = Math.max(0, budget - w.usedToday)
    return {
      usable_hours: 40, percent_per_hour: left / 40, suggested_today_percent: share, available_now_percent: Math.max(0, Math.min(share, left - kept.reserve)),
      budget_percent: budget, used_today_percent: w.usedToday, tonight_percent: tonight, period_start: iso('2026-09-29T06:00:00Z'), period_end: iso('2026-09-30T06:00:00Z'),
      finish: w.finish ? iso(w.finish) : iso(w.reset), allow_off: allowOff, unused, ahead: w.usedToday > budget + 0.5 && !override,
      reserve_percent: level, reserve_effective_percent: kept.reserve, ...(kept.until ? { reserve_until: new Date(kept.until).toISOString() } : {}),
    }
  }
  const freshness = (w: Win) => (w.readMin <= 10 ? 'fresh' : w.readMin <= 50 || (w.kind !== '5h' && w.readMin <= 28 * 60) ? (w.readMin > 120 ? 'stale' : 'aging') : 'stale')
  const project = (draft?: Schedule, pools: { pool: string; reserve: string; reserve_percent?: number }[] = []) => accts.map(a => {
    const saved = effective(a)
    let s = saved
    if (draft) {
      // The preview: the draft as the person's schedule; pool entries follow its
      // shape with their own override and reserve, pool drafts on top.
      const { account, pool } = entryOf(a)
      const wanted = pools.find(p => p.pool === a.harness)
      const { override: _o, override_until: _u, reserve: _r, reserve_percent: _p, ...shape } = draft
      const own = wanted ?? (pool?.reserve ? { reserve: pool.reserve, reserve_percent: pool.reserve_percent } : null)
      const carried: Schedule | undefined = pool || own?.reserve ? {
        ...shape, ...(pool && live(pool) ? { override: pool.override, override_until: pool.override_until } : {}),
        ...(own?.reserve ? { reserve: own.reserve, ...(own.reserve === 'fixed' ? { reserve_percent: own.reserve_percent } : {}) } : {}),
      } as Schedule : undefined
      s = resolveChain([account, carried, draft].filter(Boolean) as Schedule[])
    }
    const resets = a.windows.map(w => Date.parse(w.reset)).filter(t => t > NOW)
    const rule = rules.get(a.id)
    return {
      account_id: a.id, ongoing_use_approved: true, schedule: s,
      ...(rule ? { limit: { ...rule, period_end: periodEnd(rule.period) } } : {}),
      ...(a.harness === 'pi' && options.apiKey ? { spend_month_usd: '12.40', cost_limit_supported: true } : {}),
      ...(a.failure ? { probe_failure: a.failure } : {}),
      ...(resets.length ? { limiting_reset: new Date(Math.min(...resets)).toISOString() } : {}),
      windows: a.windows.map(w => ({
        reading: { window_kind: w.kind, bucket: '', window_minutes: w.kind === '5h' ? 300 : w.kind === 'monthly' ? 43200 : 10080, used_percent: w.used, resets_at: iso(w.reset), plan: w.plan ?? '', source: w.source, read_at: minutesAgo(w.readMin) },
        starts_at: iso(w.start), allowance: 100, remaining_percent: 100 - w.used, freshness: freshness(w), usage_today_known: true, pacing: pace(w, s),
      })),
    }
  })
  // The Advanced sentence per account, with what it counted this period.
  type Rule = { id: string; account_id: string; amount: number; unit: string; period: string; created_at: string; used: number; from_window_id?: string }
  const rules = new Map<string, Rule>()
  const periodEnd = (period: string) => iso(period === 'day' ? '2026-09-29T22:00:00Z' : period === 'week' ? '2026-10-04T22:00:00Z' : '2026-10-31T23:00:00Z')
  let ruleSeq = 0
  const rule = (accountId: string, body: { amount: number; unit: string; period: string }, used = 0, from?: string): Rule => ({
    id: `a0000000-0000-4000-8000-${String(++ruleSeq).padStart(12, '0')}`, account_id: accountId, ...body, created_at: minutesAgo(60), used, ...(from ? { from_window_id: from } : {}),
  })
  type Manual = { id: string; account_id: string; starts_at: string; ends_at: string; unit: string; allowance: number; used: number; reserved: number; pace_model: string; burst_ratio: number; provisional: boolean; set_by_you: true }
  const manual = new Map<string, Manual[]>()
  if (options.limits) {
    rules.set(ACCOUNTS.main, rule(ACCOUNTS.main, { amount: 20, unit: 'percent', period: 'day' }, 12))
    rules.set(ACCOUNTS.grok, rule(ACCOUNTS.grok, { amount: 5, unit: 'runs', period: 'week' }, 3))
    manual.set(ACCOUNTS.spare, [{ id: OLD_WINDOW, account_id: ACCOUNTS.spare, starts_at: iso('2026-09-28T06:00:00Z'), ends_at: iso('2026-10-28T07:00:00Z'), unit: 'requests', allowance: 300, used: 40, reserved: 0, pace_model: 'unrestricted', burst_ratio: 0, provisional: false, set_by_you: true }])
  }
  const agentAccounts = accts.map(a => ({
    id: a.id, account_key: `${a.harness}-${a.label}`, harness: a.harness, daemon_id: a.host, label: a.label, plan: a.plan, host_label: a.hostLabel ?? a.host, registered_by_principal_id: 'me',
    state: a.state ?? 'available', max_parallel_runs: 2, last_probe_at: minutesAgo(3), last_probe_ok: a.probe ?? true, created_at: minutesAgo(60 * 24 * 30), windows: manual.get(a.id) ?? [] as Manual[],
  }))
  // The last readings of an account: the current one, then earlier ones a little lower.
  const readings = (a: Acct) => a.windows.flatMap(w => [0, 1, 2, 3].map(i => ({
    window_kind: w.kind, bucket: '', window_minutes: w.kind === '5h' ? 300 : w.kind === 'monthly' ? 43200 : 10080, used_percent: Math.max(0, w.used - i),
    resets_at: iso(w.reset), plan: w.plan ?? '', source: i === 0 ? w.source : 'agentd', read_at: minutesAgo(w.readMin + [0, 22, 295, 540][i]),
  })))
  const writes: { path: string; method: string; body: unknown }[] = []
  const computers = [
    pairingView({ state: 'redeemed', computer_id: MBP, computer_name: 'mbp2607', computer_state: 'connected', setup_state: options.computerLogin ? 'login_required' : 'connected', connectivity: 'online', last_seen_at: minutesAgo(0.2),
      enrollments: accts.filter(a => a.host === 'mbp2607').map(a => pairingEnrollment(a.id, `${a.harness}-${a.label}`, a.harness, a.label)) }),
    pairingView({ state: 'redeemed', request_id: 'e0000000-0000-4000-8000-000000000002', computer_id: STUDIO, computer_name: 'studio', computer_state: 'connected', setup_state: 'connected', connectivity: 'offline', last_seen_at: minutesAgo(180),
      enrollments: accts.filter(a => a.host === 'studio').map(a => pairingEnrollment(a.id, `${a.harness}-${a.label}`, a.harness, a.label)) }),
  ]
  const puts: unknown[] = []
  const previews: unknown[] = []
  function handle(path: string, method: string, body: unknown): { status?: number; json?: unknown } | null {
    if (path === '/api/agent-accounts/capacity' && method === 'GET') return { json: project() }
    if (path === '/api/agent-accounts/capacity/preview' && method === 'POST') { previews.push(body); const b = body as { schedule: Schedule; pool_reserves?: { pool: string; reserve: string; reserve_percent?: number }[] }; return { json: project(b.schedule, b.pool_reserves) } }
    if (path === '/api/agent-accounts/capacity/schedule' && method === 'GET') return { json: schedules }
    if (path === '/api/agent-accounts/capacity/schedule' && method === 'PUT') {
      puts.push(body)
      const input = body as { scope: string; pool?: string; account_id?: string; schedule: Schedule | null; carry_overrides?: boolean }
      if (input.carry_overrides && input.scope === 'user' && input.schedule) {
        // The server rule (carryDraft): entries that only carry Sprint/Hold or their own reserve follow the new schedule.
        const shape = (x: Schedule) => { const { override: _o, override_until: _u, timezone: _t, reserve: _r, reserve_percent: _p, ...rest } = x; return JSON.stringify(rest) }
        const previous = schedules.find(e => e.scope === 'user')?.schedule ?? defaultSchedule()
        for (const e of [...schedules]) {
          if (e.scope === 'user' || shape(e.schedule) !== shape(previous)) continue
          const active = live(e.schedule)
          if (!active && !e.schedule.reserve) { schedules.splice(schedules.indexOf(e), 1); continue }
          const { override: _o, override_until: _u, reserve: _r, reserve_percent: _p, ...base } = input.schedule
          e.schedule = { ...base, ...(active ? { override: e.schedule.override, ...(e.schedule.override_until ? { override_until: e.schedule.override_until } : {}) } : {}), ...(e.schedule.reserve ? { reserve: e.schedule.reserve, ...(e.schedule.reserve === 'fixed' ? { reserve_percent: e.schedule.reserve_percent } : {}) } : {}) } as Schedule
        }
      }
      const at = schedules.findIndex(e => e.scope === input.scope && e.pool === input.pool && e.account_id === input.account_id)
      if (at >= 0) schedules.splice(at, 1)
      if (input.schedule) {
        const schedule = { ...input.schedule }
        if (schedule.override === 'sprint') {
          const resets = accts.filter(a => input.scope === 'user' || a.harness === input.pool || a.id === input.account_id).flatMap(a => a.windows.map(w => Date.parse(w.reset))).filter(t => t > NOW)
          schedule.override_until = new Date(Math.min(...resets)).toISOString()
        } else if (schedule.override !== 'hold' && schedule.override !== 'away') delete schedule.override_until
        schedules.push({ scope: input.scope, ...(input.pool ? { pool: input.pool } : {}), ...(input.account_id ? { account_id: input.account_id } : {}), schedule })
      }
      return { status: 204 }
    }
    if (path === '/api/agent-pairing/computers' && method === 'GET') return { json: { computers } }
    // Settings / Accounts (AEON-384): rename, the Advanced sentence, old limits, readings.
    const one = /^\/api\/agent-accounts\/([^/]+)\/(label|limit|readings|windows\/([^/]+)(\/repeat)?)$/.exec(path)
    if (!one) return null
    const [, id, what, windowId, repeat] = one
    const acct = accts.find(a => a.id === id)
    const entry = agentAccounts.find(a => a.id === id)
    if (!acct || !entry) return { status: 404, json: { error: 'account not found' } }
    if (what === 'readings' && method === 'GET') return { json: readings(acct) }
    writes.push({ path, method, body })
    if (what === 'label' && method === 'PUT') { acct.label = entry.label = (body as { label: string }).label; return { json: entry } }
    if (what === 'limit' && method === 'PUT') { const r = rule(id, body as { amount: number; unit: string; period: string }); rules.set(id, r); const { used: _u, ...out } = r; return { json: out } }
    if (what === 'limit' && method === 'DELETE') { rules.delete(id); return { status: 204 } }
    const w = entry.windows.find(x => x.id === windowId)
    if (!w) return { status: 404, json: { error: 'limit not found' } }
    entry.windows = entry.windows.filter(x => x.id !== windowId)
    if (method === 'DELETE' && !repeat) return { status: 204 }
    if (method === 'POST' && repeat) { const r = rule(id, { amount: w.allowance, unit: w.unit, period: 'month' }, w.used, w.id); rules.set(id, r); const { used: _u, ...out } = r; return { json: out } }
    return null
  }
  return { accounts: agentAccounts, computers, schedules, puts, previews, writes, handle }
}
export type CapacityWorld = ReturnType<typeof capacityWorld>
