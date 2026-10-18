// SPDX-License-Identifier: AGPL-3.0-only
// Usage reads for the merged Usage page (AEON-301), shared by the Playwright
// fixtures and the Node unit tests. "unreported" is the reality of 29 Sep 2026:
// many sessions, none reported usage, but done tickets, agent time and waste
// are known. "reported" has tokens from part of the sessions and a little API
// spend. "empty" has no session in the range.
import type { AllowanceWindow, TokenParts, UsageDashboard, UsageGroup, UsageWork, WasteItem, WorkGroup, WorkTicket } from '../src/lib/usageFormat.ts'

export const NOW = Date.parse('2026-09-29T12:02:00Z')
export type UsageVariant = 'unreported' | 'reported' | 'empty'
export const DAY = 86_400_000
const iso = (t: number) => new Date(t).toISOString()
const minutesAgo = (m: number) => iso(NOW - m * 60_000)
const H = 3600

export function usageAllowanceWindow(): AllowanceWindow {
  return {
    account_id: 'account-visible', label: 'Permitted account budget', harness: 'codex', account_state: 'available',
    window_id: 'window-visible', unit: 'requests', allowance: 10, used: 2, reserved: 1,
    pace_model: 'unrestricted', burst_ratio: '0', starts_at: iso(NOW - DAY), ends_at: iso(NOW + DAY),
    provisional: false, pace_cap: 10, headroom: 7, hard_remaining: 7,
  }
}

function group(label: string, sessions: number, extra: Partial<UsageGroup> = {}): UsageGroup {
  return {
    label, key: label, sessions, usage_rows: 0, unreported_sessions: sessions,
    input_tokens: null, input_known_rows: 0, input_unknown_rows: sessions,
    output_tokens: null, output_known_rows: 0, output_unknown_rows: sessions,
    cached_input_tokens: null, cached_input_known_rows: 0, cached_input_unknown_rows: sessions,
    tokens_state: 'unknown', estimated_cost_usd: null, cost_known_rows: 0, cost_unknown_rows: sessions,
    cost_state: 'unknown', provisional_rows: 0, provisional_sessions: 0, ...extra,
  }
}
// Complete reports: every reporting session gave input and output (a fifth of the total), no cached count.
export function parts(tokens: string | null, reported: number): TokenParts {
  const output = tokens === null ? null : BigInt(tokens) / 5n
  return {
    input_tokens: tokens === null ? null : String(BigInt(tokens) - output!), output_tokens: output === null ? null : String(output), cached_input_tokens: null,
    input_reported_sessions: reported, output_reported_sessions: reported, cached_input_reported_sessions: 0, usage_provisional_sessions: 0,
  }
}
const wg = (key: string, label: string, sessions: number, done: number, hours: number, tokens: string | null = null, reported = 0, timed = sessions): WorkGroup =>
  ({ key, label, sessions, timed_sessions: timed, agent_seconds: Math.round(hours * H), done, tokens, usage_reported_sessions: reported, ...parts(tokens, reported) })

const TICKETS: [key: string, title: string, project: string, projectKey: string, state: string, sessions: number, hours: number, extra?: Partial<WorkTicket>][] = [
  ['AEON-292', 'Capacity: accounts and allowances that configure themselves', 'p-aeon', 'PRJ-35', 'done', 14, 31.5, { done_in_range: true, released: true }],
  ['AEON-299', 'Agents top: compact live line, Needs you and the Accounts block', 'p-aeon', 'PRJ-35', 'done', 11, 26.2, { done_in_range: true, released: true }],
  ['AEON-300', 'Usage reporting from every launcher and managed run', 'p-aeon', 'PRJ-35', 'qa', 9, 19.8],
  ['AEON-301', 'Usage page phase A: merged Capacity + Usage page without dashes', 'p-aeon', 'PRJ-35', 'in_progress', 4, 7.4],
  ['PHAROS-11', 'Connect Hetzner Cloud for managed provisioning', 'p-pharos', 'PRJ-17', 'in_progress', 6, 6.9],
  ['AEON-288', 'Run outcome evidence from local git', 'p-aeon', 'PRJ-35', 'done', 5, 6.1, { done_in_range: true }],
  ['AEON-310', 'Playwright drift after the header menus', 'p-aeon', 'PRJ-35', 'done', 3, 3.9, { done_in_range: true }],
  ['PHAROS-12', 'Add an Oracle Cloud connector', 'p-pharos', 'PRJ-17', 'blocked', 4, 3.2],
  ['AEON-331', 'Blocked is a status in the menu', 'p-aeon', 'PRJ-35', 'done', 2, 2.4, { done_in_range: true }],
  ['AEON-316', 'List columns that fit at 390', 'p-aeon', 'PRJ-35', 'done', 2, 1.6, { done_in_range: true }],
  ['AEON-325', 'Update notice after a release', 'p-aeon', 'PRJ-35', 'done', 1, 0.9, { done_in_range: true }],
]

function tickets(reported: boolean): WorkTicket[] {
  return TICKETS.map(([key, title, project, projectKey, state, sessions, hours, extra], i) => {
    const tokens = reported && i % 3 !== 2 ? String(Math.round(hours * 410_000)) : null, covered = tokens === null ? 0 : sessions
    return {
      id: `n-${key}`, key, title, project_id: project, project_key: projectKey, state, done_in_range: false, released: false,
      sessions, timed_sessions: sessions, agent_seconds: Math.round(hours * H),
      tokens, usage_reported_sessions: covered, ...parts(tokens, covered),
      last_active_at: minutesAgo(40 + i * 170), ...extra,
    }
  })
}

const waste = (kind: WasteItem['kind'], key: string, title: string, harness: string, model: string, label: string, minutes: number, hours: number | null, sessions = 1): WasteItem => ({
  kind, session_id: `5e000000-0000-4000-8000-${String(minutes).padStart(12, '0')}`, project_id: key.startsWith('PHAROS') ? 'p-pharos' : 'p-aeon', project_key: key.startsWith('PHAROS') ? 'PRJ-17' : 'PRJ-35',
  ticket_id: `n-${key}`, ticket_key: key, ticket_title: title, harness, model, label, at: minutesAgo(minutes), sessions, agent_seconds: hours === null ? null : Math.round(hours * H),
})

export function usageWork(variant: UsageVariant, days = 30): UsageWork {
  const start = Date.UTC(2026, 8, 30) - days * DAY
  const doneSeries = [0, 1, 0, 2, 0, 0, 1, 3, 2, 1, 0, 2, 4, 1, 0, 0, 2, 3, 1, 2, 5, 1, 0, 1, 2, 3, 1, 0, 2, 1]
  const daysList = Array.from({ length: days }, (_, i) => {
    const done = variant === 'empty' ? 0 : doneSeries[(i + 30 - days) % 30]!
    const sessions = variant === 'empty' ? 0 : 6 + ((i * 7) % 11)
    return { day: iso(start + i * DAY).slice(0, 10), done, sessions, agent_seconds: sessions * 6200 }
  })
  if (variant === 'empty') {
    return {
      basis: 'sessions_started_in_range', sessions: 0, worker_sessions: 0, timed_sessions: 0, agent_seconds: 0, usage_reported_sessions: 0, ...parts(null, 0), model_sessions: 0,
      done: 0, released: 0, done_attributed: 0, tickets_worked: 0, tickets_done: 0, days: daysList, tickets: [], by_harness: [], by_model: [], by_project: [],
      waste: { total: 0, stuck: 0, failed: 0, lost: 0, no_result: 0, retried: 0, rows: [] },
    }
  }
  const reported = variant === 'reported'
  const tok = (v: number) => (reported ? String(v) : null)
  return {
    basis: 'sessions_started_in_range', sessions: 357, worker_sessions: 318, timed_sessions: 349, agent_seconds: Math.round(612.4 * H),
    usage_reported_sessions: reported ? 212 : 0, ...parts(tok(100_100_000), reported ? 212 : 0), model_sessions: 331,
    done: 41, released: 6, done_attributed: 39, tickets_worked: 64, tickets_done: 43,
    days: daysList,
    tickets: tickets(reported),
    by_harness: [
      wg('claude', 'claude', 148, 22, 290.2, tok(61_400_000), reported ? 131 : 0, 145),
      wg('codex', 'codex', 131, 13, 214.5, tok(38_900_000), reported ? 81 : 0, 128),
      wg('grok', 'grok', 48, 3, 70.6),
      wg('cursor', 'cursor', 30, 1, 37.1),
    ],
    by_model: [
      wg('claude-opus-5-5', 'claude-opus-5-5', 120, 19, 241.0, tok(52_000_000), reported ? 110 : 0),
      wg('gpt-6-sol', 'gpt-6-sol', 124, 13, 206.3, tok(38_900_000), reported ? 81 : 0),
      wg('claude-fable-5-1', 'claude-fable-5-1', 21, 3, 41.5, tok(9_400_000), reported ? 21 : 0),
      wg('grok-4.7', 'grok-4.7', 40, 2, 60.2),
      wg('composer-2.5', 'composer-2.5', 26, 1, 31.8),
    ],
    by_project: [
      wg('p-aeon', 'Aeon', 301, 35, 520.9, tok(88_100_000), reported ? 188 : 0),
      wg('p-pharos', 'Pharos', 42, 5, 70.3, tok(12_200_000), reported ? 24 : 0),
      wg('p-frozen', 'Studio infrastructure', 14, 1, 21.2),
    ],
    waste: {
      total: 9, stuck: 1, failed: 1, lost: 2, no_result: 4, retried: 1,
      rows: [
        waste('retried', 'PHAROS-12', 'Add an Oracle Cloud connector', 'grok', 'grok-4.7', 'pharos-12-oracle', 2880, 3.2, 4),
        waste('no_result', 'AEON-300', 'Usage reporting from every launcher and managed run', 'codex', 'gpt-6-sol', 'aeon-300-usage-reporting', 610, 2.6),
        waste('stuck', 'AEON-301', 'Usage page phase A: merged Capacity + Usage page without dashes', 'claude', 'claude-opus-5-5', 'aeon-301-usage-page-a', 42, 1.9),
        waste('failed', 'PHAROS-11', 'Connect Hetzner Cloud for managed provisioning', 'cursor', 'composer-2.5', 'pharos-11-hetzner', 1450, 1.1),
        waste('no_result', 'AEON-310', 'Playwright drift after the header menus', 'codex', 'gpt-6-sol', 'aeon-310-playwright-drift', 3900, 0.7),
        waste('lost', 'AEON-316', 'List columns that fit at 390', 'grok', 'grok-4.7', 'aeon-316-list-columns', 5020, 0.4),
        waste('no_result', 'AEON-325', 'Update notice after a release', 'codex', 'gpt-6-sol', 'aeon-325-update-notice', 6100, 0.3),
        waste('no_result', 'AEON-331', 'Blocked is a status in the menu', 'claude', 'claude-opus-5-5', 'aeon-331-blocked', 7300, 0.2),
        waste('lost', 'AEON-288', 'Run outcome evidence from local git', 'claude', 'claude-opus-5-5', 'aeon-288-outcome', 9800, null),
      ],
    },
  }
}

export function usageDashboard(variant: UsageVariant, days = 30): UsageDashboard {
  const work = usageWork(variant, days)
  const to = iso(Date.UTC(2026, 8, 30)), from = iso(Date.UTC(2026, 8, 30) - days * DAY)
  const reported = variant === 'reported'
  const totals = group('All visible sessions', work.sessions, reported
    ? { usage_rows: 214, unreported_sessions: 145, input_tokens: '91200000', input_known_rows: 214, output_tokens: '9100000', output_known_rows: 214, tokens_state: 'partial', estimated_cost_usd: '12.400000000000', cost_known_rows: 3, cost_unknown_rows: 356, cost_state: 'partial' }
    : {})
  return {
    from, to, generated_at: iso(NOW), attribution: 'lifetime_for_sessions_started_in_range', trend_basis: 'session_started_utc_day', list_price_currency: 'USD', truncated: false,
    totals, by_project: [], by_model: [], by_harness: [], by_subscription: [], trend: [], tickets: [], tickets_cost_unknown: 0,
    allowance: { state: 'none', windows: [] },
    ratings: {
      votes: 3, average: null, exceptions: 3, deliveries: 280, rework_rate: '3/280', by_model: [],
      by_harness: variant === 'empty' ? [] : [{ label: 'claude', votes: 1, average: null, exceptions: 1, deliveries: 130, rework_rate: '1/130' }, { label: 'grok', votes: 2, average: null, exceptions: 2, deliveries: 41, rework_rate: '2/41' }],
    },
    work,
  }
}
