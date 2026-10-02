// SPDX-License-Identifier: AGPL-3.0-only
import { afterEach, expect, it, vi } from 'vitest'
import { briefingCost, briefingDue, eventFact, eventNeed, loadBriefingEvents, loadBriefingOutcomes, loadBriefingWindow, sumBriefingUsage, outcomeFact, recommendedStep, validBriefingTime, type BriefingOutcome } from '../src/lib/morningBriefing'
import { usageDashboard } from './usage-data'

const now = new Date('2026-10-01T08:00:00Z')
const outcome: BriefingOutcome = { id: 'out-1', kind: 'ticket_done', ticket_key: 'AEON-454', ticket_node_id: 'ticket', project_id: 'project', session_id: null, rules_version: null, release_title: null, recorded_at: '2026-10-01T07:00:00Z', payload: { to_state: 'done' } }
afterEach(() => vi.unstubAllGlobals())
it('uses a person’s saved visit and a local daily reminder', () => {
  const local = new Date(2026, 9, 1, 8, 30)
  expect(briefingDue({ time: '09:00' }, local)).toBe(false)
  expect(briefingDue({ time: '08:00' }, local)).toBe(true)
  expect(briefingDue({ time: '08:00', last_visit: new Date(2026, 9, 1, 8, 5).toISOString() }, local)).toBe(false)
  expect(validBriefingTime('24:00')).toBe(false)
  expect(validBriefingTime('08:30')).toBe(true)
})
it('cites facts and keeps done distinct from released or merged', () => {
  expect(outcomeFact(outcome, 'AEON')).toMatchObject({ title: 'AEON-454 · Marked done', source: '/api/outcomes?ticket_node_id=ticket&outcome_id=out-1&limit=1' })
  expect(outcomeFact({ ...outcome, kind: 'review_verdict', payload: { verdict: 'ok' } }, 'AEON')).toBeNull()
  expect(outcomeFact({ ...outcome, kind: 'review_verdict', payload: { verdict: 'changes', summary: 'Fix isolation' } }, 'AEON')).toMatchObject({ detail: 'Fix isolation' })
  const base = { id: 42, node_id: 'ticket', type: 'node.updated', before: { fields: {} }, after: { fields: { merge_commit: 'abcdef1234567' } }, at: outcome.recorded_at }
  expect(eventFact(base, 'AEON', 'AEON-454')).toMatchObject({ title: 'AEON-454 · Merge reported', source: '/api/events?node_id=ticket&after=41&limit=1' })
  expect(eventFact({ ...base, after: { fields: { pr_url: 'https://github.com/example/repo/pull/1' } } }, 'AEON', 'AEON-454')).toBeNull()
  expect(eventFact({ ...base, before: base.after }, 'AEON', 'AEON-454')).toBeNull()
  expect(eventFact({ ...base, type: 'run.telemetry', after: { report: { kind: 'finished' }, run: { status: 'completed', outcome_detail: 'pr_opened' } } }, 'AEON', 'AEON-454')).toBeNull()
  expect(eventFact({ ...base, type: 'node.updated', before: { state: 'delivered' }, after: { state: 'delivered' } }, 'AEON', 'AEON-454')).toBeNull()
})
it('projects autopilot publication and human-check skips from their real snapshots', () => {
  const base = { id: 43, node_id: 'ticket', type: 'status_autopilot.changed', before: { state: 'done', human_check: null }, after: { state: 'delivered', human_check: null }, at: outcome.recorded_at }
  expect(eventFact(base, 'AEON', 'AEON-454')?.title).toBe('AEON-454 · Marked delivered')
  expect(eventNeed(base, 'AEON', 'AEON-454')).toBeNull()
  for (const type of ['status_autopilot.changed', 'status_autopilot.skipped']) {
    const skipped = { ...base, type, before: { state: 'done', human_check: 'Touch ID' }, after: { state: 'done', human_check: 'Touch ID' } }
    expect(eventNeed(skipped, 'AEON', 'AEON-454')).toMatchObject({ title: 'AEON-454 · Human check', detail: 'Touch ID', source: '/api/events?node_id=ticket&after=42&limit=1' })
    expect(eventFact(skipped, 'AEON', 'AEON-454')).toBeNull()
  }
})
it('pages both autopilot event types only within the server window', async () => {
  const requests: URL[] = []
  vi.stubGlobal('fetch', vi.fn(async (input: string) => {
    const url = new URL(input, 'https://aeon.test'); requests.push(url)
    return new Response(JSON.stringify({ items: [], next_cursor: requests.length === 1 ? 'next' : null }), { status: 200 })
  }))
  const range = { from: '2026-09-30T08:00:00.000Z', to: now.toISOString(), first: false, capped: false }
  expect((await loadBriefingEvents(range, undefined, true)).truncated).toBe(false)
  expect(requests).toHaveLength(2)
  for (const url of requests) {
    expect(url.searchParams.get('type')).toBe('status_autopilot.changed,status_autopilot.skipped')
    expect(url.searchParams.get('from')).toBe(range.from)
    expect(url.searchParams.get('to')).toBe(range.to)
  }
})
it('keeps unknown cost unknown and marks partial API value as approximate', () => {
  const group = usageDashboard('unreported').totals
  expect(briefingCost(group).value).toBe('Cost not measured yet')
  expect(briefingCost({ ...group, estimated_cost_usd: '0.000000000000', cost_known_rows: 1, cost_state: 'known' })).toMatchObject({ value: '≈ $0 · measured', measured: true })
  expect(briefingCost({ ...group, estimated_cost_usd: '12.400000000000', cost_known_rows: 1, cost_state: 'partial' })).toMatchObject({ value: '≈ $12', measured: false })
  expect(briefingCost({ ...group, estimated_cost_usd: '12.400000000000', cost_known_rows: 1, cost_state: 'partial' }).detail).toContain('partial')
})
it('recommends one cited person action before findings; invents no next backlog item', () => {
  const need = { id: 'a:1', title: 'Deploy stage', detail: 'Check target', href: '/agents?needs=a%3A1', source: '/agents?needs=a%3A1' }
  expect(recommendedStep([need], [])).toMatchObject({ title: 'Review Deploy stage', source: need.source })
  expect(recommendedStep([], [])).toBeNull()
})
it('continues each existing log without dropping or repeating rows', async () => {
  const requests: string[] = []
  vi.stubGlobal('fetch', vi.fn(async (input: string) => {
    requests.push(input)
    const path = new URL(input, 'https://aeon.test')
    if (path.pathname === '/api/outcomes') return new Response(JSON.stringify(path.searchParams.has('cursor') ? { outcomes: [{ ...outcome, id: 'out-2' }], next_cursor: null } : { outcomes: [outcome], next_cursor: 'next' }), { status: 200 })
    return new Response(JSON.stringify(path.searchParams.get('cursor') === 'time-next' ? { items: [], next_cursor: null } : { items: [{ id: 42 }], next_cursor: 'time-next' }), { status: 200 })
  }))
  const range = { from: '2026-09-30T08:00:00.000Z', to: now.toISOString(), first: true, capped: false }
  expect((await loadBriefingOutcomes(range)).items.map(o => o.id)).toEqual(['out-1', 'out-2'])
  expect((await loadBriefingEvents(range)).items).toHaveLength(1)
  expect(requests[1]).toContain('cursor=next')
  expect(requests[2]).toContain('type=node.updated')
  expect(requests[3]).toContain('cursor=time-next')
  expect(requests[2]).toContain('order=time')
})
it('rejects nonadvancing pagination rather than marking a partial visit read', async () => {
  vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ outcomes: [outcome], next_cursor: 'same' }), { status: 200 })))
  await expect(loadBriefingOutcomes({ from: '2026-09-30T08:00:00.000Z', to: now.toISOString(), first: true, capped: false })).rejects.toThrow('pagination did not advance')
})

it('gets the cutoff from the server log snapshot and preserves its submillisecond precision', async () => {
  const range = { from: '2026-09-30T08:00:00.000001Z', to: '2026-10-01T08:00:00.123456Z', first: false, capped: false }
  const requests: URL[] = []
  vi.stubGlobal('fetch', vi.fn(async (input: string) => {
    const url = new URL(input, 'https://aeon.test'); requests.push(url)
    return new Response(JSON.stringify(url.searchParams.has('briefing') ? { items: [{ id: 1 }], next_cursor: 'time-cursor', window: range } : { items: [{ id: 2 }], next_cursor: null }), { status: 200 })
  }))
  expect(await loadBriefingWindow({ last_visit: range.from })).toEqual({ range, events: { items: [{ id: 1 }, { id: 2 }], truncated: false } })
  expect(requests[0]!.searchParams.get('since')).toBe(range.from)
  expect(requests[0]!.searchParams.has('to')).toBe(false)
  expect(requests[1]!.searchParams.get('to')).toBe(range.to)
  expect(requests[1]!.searchParams.get('from')).toBe(range.from)
})
it('sums permitted dashboard amounts exactly and deduplicates shared account windows', () => {
  const first = usageDashboard('reported'), second = usageDashboard('reported')
  first.totals.estimated_cost_usd = '9007199254740993.000000000001'
  second.totals.estimated_cost_usd = '0.000000000009'
  first.totals.input_tokens = '9007199254740993'; second.totals.input_tokens = '10'
  first.allowance.windows = [{ window_id: 'shared' } as typeof first.allowance.windows[number]]
  second.allowance.windows = [...first.allowance.windows]
  const total = sumBriefingUsage([first, second])
  expect(total.totals.estimated_cost_usd).toBe('9007199254740993.000000000010')
  expect(total.totals.input_tokens).toBe('9007199254741003')
  expect(total.allowance.windows).toHaveLength(1)
  expect(total.totals.unreported_sessions).toBe(first.totals.unreported_sessions + second.totals.unreported_sessions)
})
