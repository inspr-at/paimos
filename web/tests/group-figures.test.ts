// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1388: group summaries. Risk: a group that shows another scope's figures or a sum its rows don't make (the
// narrowed query is wrong, a cached scope lands under the wrong key, a late answer for a scope that left the screen
// lands anyway), "—" or 0 where nothing is to say, a phone line with more than three facts, and a count that floods
// the server (more than two at a time, or the same scope twice).
import { test } from 'node:test'
import assert from 'node:assert/strict'
import type { ListQuery, ProjectFigures } from '../src/lib/api.ts'
import { figuresCache, figuresLoader, groupCellText, groupParams, groupTipTitle, peopleText, phoneFacts, rowCount } from '../src/lib/groupFigures.ts'
import { filtersFromQuery, groupRows, type RowGroup } from '../src/lib/ticketList.ts'
import { validFigures } from '../src/lib/workFigures.ts'
import { FIGURES_NOW, viewFigures, wholeFigures } from './work-figures-fixture.ts'

const P = 'p-1'
const group = (key: string, extra: Partial<RowGroup> = {}): RowGroup => ({ key, label: key, rows: [], total: 0, ...extra })
const ctx = { now: FIGURES_NOW, grouped: null }

test('a group is the view narrowed to that group: its value replaces the view’s own, exclusions stay', () => {
  const filters = filtersFromQuery({ status: 'in_progress,qa,!blocked', status_scope: 'canonical', assignee: 'u1,u2,!u3', group: 'assignee' })
  const params = groupParams(P, filters, 'assignee', group('u2'))!
  assert.deepEqual(params.assignee, ['!u3', 'u2'])
  assert.deepEqual(params.work_state, ['in_progress', 'qa', '!blocked'], 'the view’s status filter is untouched')
  assert.equal(params.within, P)
  const status = groupParams(P, filters, 'status', group('progress', { state: 'in-progress' }))!
  assert.deepEqual(status.work_state, ['!blocked', 'in_progress'])
  assert.deepEqual(groupParams(P, filtersFromQuery({}), 'priority', group('none'))!.priority, ['none'])
  assert.deepEqual(groupParams(P, filtersFromQuery({ epic: '!e9' }), 'epic', group('e1'))!.epic, ['!e9', 'e1'])
  assert.deepEqual(groupParams(P, filtersFromQuery({}), 'tag', group('bug'))!.tag, ['bug'])
  assert.deepEqual(groupParams(P, filtersFromQuery({}), 'type', group('work'))!.kind, ['work'])
})

test('a group that has no exact query is not counted rather than counted wrong', () => {
  // "Has label z and one of the filtered labels" has no query; a filtered label itself is exact.
  assert.equal(groupParams(P, filtersFromQuery({ tag: 'ops,docs' }), 'tag', group('bug')), null)
  assert.deepEqual(groupParams(P, filtersFromQuery({ tag: 'ops,Docs' }), 'tag', group('docs'))!.tag, ['docs'])
  // An unknown spelling the server folds into In progress would count that status.
  assert.equal(groupParams(P, filtersFromQuery({}), 'status', group('inprogress', { state: 'inprogress' })), null)
  assert.ok(groupParams(P, filtersFromQuery({}), 'status', group('triage', { state: 'triage' })))
  // The kind filter folds Work into every legacy kind; only Work itself is exact. Projects have no figures.
  assert.equal(groupParams(P, filtersFromQuery({}), 'type', group('ticket')), null)
  assert.equal(groupParams(P, filtersFromQuery({}), 'project', group('p-2')), null)
})

test('cells say something or nothing: sums with the header’s marks, never "—" or 0', () => {
  const f = viewFigures()
  assert.equal(groupCellText('estimate', f, ctx), '~78h', 'leaves only, "~" on a sum of estimates')
  assert.equal(groupCellText('priority', f, ctx), '64 high · 85 med · 17 low')
  assert.equal(groupCellText('model', f, { ...ctx, modelName: id => id === 'claude-opus-5-5' ? 'Opus 5.5' : id }), 'Opus 5.5 ×70')
  assert.equal(groupCellText('tags', f, ctx), 'bug 19')
  assert.equal(groupCellText('status', f, ctx), '3 accepted · 3 open')
  assert.equal(groupCellText('eta', f, ctx), '~2 min')
  assert.equal(groupCellText('tokens', f, ctx), '~4.20B', 'measured and estimated tokens add up, "~" for the estimated part')
  // The grouped-by column stays empty; so do columns without figures and a group without any.
  assert.equal(groupCellText('priority', f, { ...ctx, grouped: 'priority' }), '')
  assert.equal(groupCellText('created', f, ctx), '')
  assert.equal(groupCellText('estimate', null, ctx), '')
  const empty: ProjectFigures = { ...f, columns: { ...f.columns, estimate: { ...f.columns.estimate, numbers: { count: 0, total: 0, median: null, average: null } }, priority: { ...f.columns.priority, values: [] }, suggested_release: { ...f.columns.suggested_release, values: [{ value: 'Next', count: 4, hours: 0 }] }, eta_ready: { ...f.columns.eta_ready, next_finish: undefined }, tokens: { ...f.columns.tokens, measured: { count: 0, total: 0, median: null, average: null }, estimated: { count: 0, total: 0, median: null, average: null } } } }
  for (const id of ['estimate', 'priority', 'eta', 'tokens']) assert.equal(groupCellText(id, empty, ctx), '', id)
  assert.equal(groupCellText('suggested', empty, ctx), '4 → Next')
  const partial: ProjectFigures = { ...f, columns: { ...f.columns, tokens: { ...f.columns.tokens, partial: true, estimated: { count: 0, total: 0, median: null, average: null } } } }
  assert.equal(groupCellText('tokens', partial, ctx), '≥1.06B', 'a lower bound says so')
})

test('people, row facts and the phone line', () => {
  assert.equal(peopleText(wholeFigures()), '≥1 person', 'one person in the top three and a tail beyond it')
  const two: ProjectFigures = { ...viewFigures(), columns: { ...viewFigures().columns, assignee: { rows: 4, included: 4, without_value: 0, others: 0, partial: false, values: [{ value: 'Ada', kind: 'person', count: 3, hours: 0 }, { value: 'Bo', kind: 'person', count: 1, hours: 0 }] } } }
  assert.equal(peopleText(two), '2 people')
  assert.equal(peopleText(null), '')
  assert.equal(rowCount(3, true), '3')
  assert.equal(rowCount(3, false), '≥3', 'a group with rows still to load gives a lower bound')
  assert.deepEqual(phoneFacts({ working: '3', stale: '6', estimate: '~12h' }), [['3', 'working'], ['6', 'stale'], ['~12h', '']])
  assert.deepEqual(phoneFacts({ working: '', stale: '6', estimate: '' }), [['6', 'stale']])
  assert.ok(phoneFacts({ working: '1', stale: '2', estimate: '~3h' }).length <= 3)
  assert.equal(groupTipTitle('In progress', 10), 'In progress · 10 tickets')
  assert.equal(groupTipTitle('QA', 1), 'QA · 1 ticket')
})

test('the figures cache: one entry per scope, old entries still land but count as stale, bounded in size', () => {
  let now = 0
  const cache = figuresCache({ maxEntries: 2, maxAge: 30_000, now: () => now })
  cache.put('a', wholeFigures())
  assert.equal(cache.get('a')!.fresh, true)
  now = 30_001
  assert.equal(cache.get('a')!.fresh, false, 'older than 30 s: shown, then counted again')
  cache.put('b', viewFigures()); cache.put('c', viewFigures())
  assert.equal(cache.size, 2)
  assert.equal(cache.get('a'), null, 'the oldest scope leaves first')
  cache.forget(key => key === 'b')
  assert.equal(cache.get('b'), null)
})

// A fetch the test answers by hand, so overlap and order are what the test makes them, not timing.
function manualFetch() {
  const calls: { params: ListQuery; signal: AbortSignal; resolve: (value: ProjectFigures) => void; reject: (error: unknown) => void }[] = []
  return {
    calls,
    fetch: (params: ListQuery, signal: AbortSignal) => new Promise<ProjectFigures>((resolve, reject) => { calls.push({ params, signal, resolve, reject }) }),
  }
}
const settle = () => new Promise(resolve => setImmediate(resolve))

test('the loader counts each scope once, two at a time in order, lands cached scopes at once and drops scopes that left', async () => {
  let now = 0
  const cache = figuresCache({ maxEntries: 10, maxAge: 30_000, now: () => now })
  const { calls, fetch } = manualFetch()
  const landed: [string, string][] = [], failed: string[] = []
  const loader = figuresLoader({ cache, fetch, valid: validFigures, concurrency: 2, land: (key, value) => landed.push([key, value.view_key]), fail: key => failed.push(key) })
  const req = (key: string) => ({ key, params: { within: key } })
  loader.want([req('a'), req('b'), req('c'), req('a')])
  assert.deepEqual(calls.map(call => call.params.within), ['a', 'b'], 'two at a time, in group order, each scope once')
  calls[0].resolve({ ...wholeFigures(), view_key: 'A' })
  await settle()
  assert.deepEqual(landed, [['a', 'A']])
  assert.deepEqual(calls.map(call => call.params.within), ['a', 'b', 'c'], 'the next scope starts when one finishes')
  // b leaves the screen before its answer: it is aborted and its late answer never lands.
  landed.length = 0
  loader.want([req('a'), req('c')])
  assert.deepEqual(landed, [['a', 'A']], 'a cached scope lands again at once')
  assert.equal(calls[1].signal.aborted, true)
  calls[1].resolve({ ...wholeFigures(), view_key: 'B' })
  calls[2].resolve({ ...wholeFigures(), view_key: 'C' })
  await settle()
  assert.deepEqual(landed, [['a', 'A'], ['c', 'C']], 'b’s late answer never lands')
  // Wanted again within 30 s: cached scopes land at once, nothing is counted again.
  landed.length = 0
  loader.want([req('a'), req('c')])
  assert.deepEqual(landed, [['a', 'A'], ['c', 'C']])
  assert.equal(calls.length, 3)
  // The work changed: both are counted again; what shows stays until then. A malformed answer is a failure, not figures.
  loader.refresh()
  assert.equal(calls.length, 5)
  calls[3].resolve({ nope: true } as unknown as ProjectFigures)
  calls[4].reject(new Error('down'))
  await settle()
  assert.deepEqual(failed, ['a', 'c'])
  loader.stop()
})
