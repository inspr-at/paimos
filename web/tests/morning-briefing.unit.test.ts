// SPDX-License-Identifier: AGPL-3.0-only
import { afterEach, expect, it, vi } from 'vitest'
import { briefingCost, briefingDue, briefingRange, eventFact, loadBriefingEvents, loadBriefingOutcomes, outcomeFact, recommendedStep, validBriefingTime, type BriefingOutcome } from '../src/lib/morningBriefing'
import { usageDashboard } from './usage-data'

const now = new Date('2026-10-01T08:00:00Z')
const outcome: BriefingOutcome = { id: 'out-1', kind: 'ticket_done', ticket_key: 'AEON-454', ticket_node_id: 'ticket', project_id: 'project', session_id: null, rules_version: null, release_title: null, recorded_at: '2026-10-01T07:00:00Z', payload: { to_state: 'done' } }
afterEach(() => vi.unstubAllGlobals())
it('uses a person’s visit, bounded first use and a local daily reminder', () => {
  expect(briefingRange(null, now)).toMatchObject({ from: '2026-09-30T08:00:00.000Z', first: true, capped: false })
  expect(briefingRange({ last_visit: '2026-09-29T08:00:00Z' }, now)).toMatchObject({ from: '2026-09-29T08:00:00.000Z', first: false })
  expect(briefingRange({ last_visit: '2024-09-29T08:00:00Z' }, now).capped).toBe(true)
  expect(briefingRange({ last_visit: '2027-01-01T08:00:00Z' }, now).first).toBe(false)
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
  const base = { id: 42, node_id: 'ticket', type: 'run.telemetry', before: null, after: { report: { kind: 'finished' }, run: { status: 'completed', outcome_detail: 'merged' } }, at: outcome.recorded_at }
  expect(eventFact(base, 'AEON', 'AEON-454')).toMatchObject({ title: 'AEON-454 · Merge reported', source: '/api/events?node_id=ticket&after=41&limit=1' })
  expect(eventFact({ ...base, after: { report: { kind: 'finished' }, run: { status: 'completed', outcome_detail: 'committed' } } }, 'AEON', 'AEON-454')).toBeNull()
  expect(eventFact({ ...base, after: { report: { kind: 'usage' }, run: { status: 'completed', outcome_detail: 'merged' } } }, 'AEON', 'AEON-454')).toBeNull()
  expect(eventFact({ ...base, type: 'node.updated', before: { state: 'delivered' }, after: { state: 'delivered' } }, 'AEON', 'AEON-454')).toBeNull()
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
  const range = briefingRange(null, now)
  expect((await loadBriefingOutcomes(range)).items.map(o => o.id)).toEqual(['out-1', 'out-2'])
  expect((await loadBriefingEvents(range)).items).toHaveLength(1)
  expect(requests[1]).toContain('cursor=next')
  expect(requests[2]).toContain('type=node.updated')
  expect(requests[3]).toContain('cursor=time-next')
  expect(requests[2]).toContain('order=time')
})
it('rejects nonadvancing pagination rather than marking a partial visit read', async () => {
  vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ outcomes: [outcome], next_cursor: 'same' }), { status: 200 })))
  await expect(loadBriefingOutcomes(briefingRange(null, now))).rejects.toThrow('pagination did not advance')
})
