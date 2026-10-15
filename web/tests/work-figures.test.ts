// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1358 (design r4b §1–§3): the shared words and numbers of the header work bar and the column figures. Risk: shown
// figures that do not add up (group hours vs. the total, shares vs. 100 %), the wrong format, a total without "~", or
// a column popup whose shape changes between "Counting…" and the landed figures.
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { columnTip, fmtDuration, fmtHours, fmtTokens, FIGURE_COLUMNS, hidesRows, largestRemainder, validFigures, workHeader, workSlots, workTipFoot } from '../src/lib/workFigures.ts'
import { FIGURES_NOW, viewFigures, wholeFigures } from './work-figures-fixture.ts'
import { DEFAULT_MODEL_DISPLAY, modelCell, shownModelNames } from '../src/lib/planning.ts'

test('durations and hours use the one format table', () => {
  const minutes: [number, string][] = [[0.2, '1m'], [40, '40m'], [59.4, '59m'], [144, '2.4h'], [300, '5h'], [14 * 60, '14h'], [23.4 * 60, '23h'], [1.5 * 1440, '1.5d'], [2 * 1440, '2d'], [12 * 1440, '12d']]
  for (const [value, shown] of minutes) assert.equal(fmtDuration(value), shown, `${value} min`)
  assert.equal(fmtDuration(null), '—')
  const hours: [number, string][] = [[7.5, '7.5h'], [5, '5h'], [9.94, '9.9h'], [9.96, '10h'], [728, '728h'], [1029, '1,029h']]
  for (const [value, shown] of hours) assert.equal(fmtHours(value), shown, `${value} h`)
  assert.deepEqual([2.74e9, 4.1e7, 9.5e6, 4.5e5].map(fmtTokens), ['2.74B', '41M', '9.5M', '450K'])
})

test('largest remainder keeps every set adding up to its total', () => {
  assert.deepEqual(largestRemainder([618.6, 31.4, 34.6, 43.4], 728), [619, 31, 35, 43])
  for (const set of [[1, 1, 1], [118, 11, 6, 4], [0.2, 0.3, 0.5], [5, 0, 0, 1]]) assert.equal(largestRemainder(set).reduce((a, b) => a + b, 0), 100)
  assert.deepEqual(largestRemainder([0, 0]), [0, 0])
})

test('the header adds up: group hours sum to the total, shares to 100, "~" only on the total', () => {
  const header = workHeader(wholeFigures())
  assert.equal(header.scope.label, 'Whole project')
  assert.equal(header.total, '~728h')
  assert.deepEqual(header.groups.map(g => [g.name, g.shown]), [['Completed', '619h'], ['Done, not live', '31h'], ['In progress', '35h'], ['Open', '43h']])
  assert.equal(header.groups.reduce((a, g) => a + Number(g.shown.replace(/\D/g, '')), 0), 728)
  for (const g of header.groups) assert.doesNotMatch(g.shown, /~/)
  assert.equal(header.groups.reduce((a, g) => a + g.hoursShare, 0), 100)
  assert.equal(header.groups.reduce((a, g) => a + g.ticketShare, 0), 100)
  assert.equal(header.accessible, 'Whole project. Total about 728h. Completed 619h · Done, not live 31h · In progress 35h · Open 43h · 18 without estimate.')
  // Drawn to scale: three segments, Open is the track; their widths are the hours' shares.
  assert.deepEqual(header.segments.map(s => s.id), ['c', 'd', 'p'])
  assert.ok(Math.abs(header.segments[0].width - 618.6 / 728 * 100) < 1e-9)
  assert.ok(Math.abs(header.segments[2].left - (618.6 + 31.4) / 728 * 100) < 1e-9)
  const view = workHeader(viewFigures())
  assert.equal(view.scope.label, 'This view · 10 of 166 tickets')
  assert.equal(view.total, '~78h')
  assert.equal(view.groups.reduce((a, g) => a + Number(g.shown.replace(/\D/g, '')), 0), 78)
})

test('the hover footer is one fact per line; Hide closed and projects without releases add theirs', () => {
  const header = workHeader(wholeFigures())
  assert.deepEqual(workTipFoot(header, null), ['Hours are agent estimates · leaves only', '18 without estimate count as 0h', '27 closed not counted'])
  assert.equal(workTipFoot(header, 'Hide closed')[3], 'Hide closed doesn’t change these figures.')
  const bare = workHeader({ ...wholeFigures(), without_estimate: 0, scope: { ...wholeFigures().scope, closed: 0 } })
  assert.deepEqual(workTipFoot(bare, null).slice(1), ['Every ticket has an estimate', 'No closed tickets'])
  const noReleases = workHeader(wholeFigures({ marksReleases: false }))
  assert.deepEqual(noReleases.groups.map(g => g.name), ['Done', 'In progress', 'Open'])
  assert.equal(noReleases.groups[0].fill, 'f-solid fc-done')
  assert.equal(workTipFoot(noReleases, null).at(-1), 'This project doesn’t mark releases, so Done is its last step.')
})

test('the slots are sized from the whole project', () => {
  assert.deepEqual(workSlots(workHeader(wholeFigures())), { digits: 4, total: 5, leaves: 3 })
})

test('column popups have a fixed shape and the design\'s words', () => {
  const figures = wholeFigures(), ctx = { hiddenRows: false, now: FIGURES_NOW }
  const show = (id: Parameters<typeof columnTip>[0], context = ctx) => {
    const tip = columnTip(id, figures, context)!
    assert.equal(tip.lines.length, FIGURE_COLUMNS[id]!.lines, `${id} keeps its line count`)
    return { lines: tip.lines.map(l => l && [l.label, l.value, l.note ?? ''].filter(Boolean).join(' ')), foot: tip.foot }
  }
  assert.deepEqual(show('key'), { lines: ['Parents 2', 'Leaves 166', 'Keys AIT-4 – AIT-177'], foot: '168 shown · all rows' })
  assert.deepEqual(show('status'), { lines: ['Completed 118 Delivered 28 · Accepted 90', 'Done, not live 11', 'In progress 6 In progress 5 · QA 1', 'Open 4 Backlog 1 · Open 3'], foot: '166 shown · 27 closed · leaves only' })
  assert.deepEqual(show('eta'), { lines: ['Running 5', 'Next finish ~2 min AIT-174', 'Last finish ~3.5 h AIT-135'], foot: '166 shown · leaves only' })
  assert.deepEqual(show('progress'), { lines: ['Weighted by hours 71%', 'Plain average 64%', 'With a progress reading 6'], foot: '139 of 166 shown · closed left out' })
  assert.deepEqual(show('priority'), { lines: ['High 64', 'Medium 85', 'Low 17', 'No priority 0'], foot: '166 shown · leaves only' })
  assert.deepEqual(show('assignee'), { lines: ['AIT lead 126 agent', 'Markus Barta 12 person', 'AIT-105-b4 1 agent', 'Others 5'], foot: '166 shown · 22 unassigned · leaves only' })
  assert.deepEqual(show('estimate'), { lines: ['Total ~728h', 'Median 4h', 'Without estimate 18 count as 0h'], foot: '139 of 166 shown · closed left out' })
  assert.deepEqual(show('tokens'), { lines: ['Median ~18M', 'Average ~29M', 'Measured 1.06B 48 tickets', 'Estimated ~3.14B 96 tickets'], foot: '166 shown · 22 without tokens · leaves only' })
  assert.deepEqual(show('created'), { lines: ['Range 7 Sep – today', 'Median age 5.7d', 'Open, created 14d+ ago 1'], foot: '168 shown · all rows' })
  assert.deepEqual(show('updated'), { lines: ['Range 12 Sep – 8m ago', 'Median since update 2.8d', 'Open, untouched 7d+ 0'], foot: '168 shown · all rows' })
  // Reserved top-N lines without a value are blank, and Others shows only with a tail (Tick, fix round 6).
  assert.deepEqual(show('cost').lines, ['AIT core 412h · $1.9k', null, null, null])
  assert.deepEqual(show('suggested').lines, [null, null, null, 'None 166'])
  assert.deepEqual(show('epic').lines, ['AIT-12 40', 'AIT-3 22', 'Others 9', 'No epic 95'])
  // With Hide closed hiding rows of the view, "shown" becomes "incl. hidden rows".
  assert.equal(show('estimate', { ...ctx, hiddenRows: true }).foot, '139 of 166 incl. hidden rows · closed left out')
  // The Estimate total is the header total.
  assert.equal(columnTip('estimate', figures, ctx)!.lines[0]!.value, workHeader(figures).total)
  assert.equal(columnTip('title', figures, ctx), null)
})

test('top-N popups keep their lines reserved but blank, and name models as the Model column does', () => {
  const figures = wholeFigures(), ctx = { hiddenRows: false, now: FIGURES_NOW }
  const sparse = { ...figures, columns: { ...figures.columns,
    model: { ...figures.columns.model, values: [{ value: 'gpt-6.1-sol', count: 4, hours: 0 }], others: 0 },
    epic: { ...figures.columns.epic, values: [{ value: 'AIT-12', count: 3, hours: 0 }], others: 0 },
    release: { ...figures.columns.release, values: [], others: 0 },
    tag: { ...figures.columns.tag, values: [{ value: 'bug', count: 2, hours: 0 }], others: 0 },
    assignee: { ...figures.columns.assignee, values: [{ value: 'AIT lead', kind: 'agent' as const, count: 7, hours: 0 }], others: 0 },
  } }
  const lines = (id: Parameters<typeof columnTip>[0], context: Parameters<typeof columnTip>[2] = ctx) => columnTip(id, sparse, context)!.lines.map(l => l && [l.label, l.value, l.note ?? ''].filter(Boolean).join(' '))
  assert.deepEqual(lines('model'), ['gpt-6.1-sol 4', null, null, null])
  assert.deepEqual(lines('epic'), ['AIT-12 3', null, null, 'No epic 95'])
  assert.deepEqual(lines('release'), [null, null, null, 'No release 58'])
  assert.deepEqual(lines('tags'), ['bug 2', null, null, 'Untagged 101'])
  assert.deepEqual(lines('assignee'), ['AIT lead 7 agent', null, null, null])
  for (const id of ['model', 'epic', 'release', 'suggested', 'tags', 'cost', 'assignee'] as const) {
    const shown = columnTip(id, sparse, ctx)!.lines.filter(l => l !== null).map(l => l!.label)
    assert.ok(!shown.includes('—') && !shown.includes('Others'), `${id} shows no placeholder and no empty Others`)
  }
  // The Model popup's names come from the Model column's own formatter; unknown ids keep their id.
  const rows = [
    { kind_slug: 'ticket', fields: {}, planning: { route: null, models: [{ label: 'Claude Opus', harness: 'claude', model: 'claude-opus-5-5', display_name: 'Claude Opus', short_name: 'Opus', model_version: '5.5', sessions: [] }], tokens: { spent: null, input: 0, output: 0, cached: 0, sessions: 1, unreported: 0, estimated: null } } },
    { kind_slug: 'ticket', fields: {}, planning: { route: { label: 'Codex Sol', profile: 'model-codex-high', harness: 'codex', model: 'gpt-6.1-sol', effort: 'high', revision: '1', display_name: 'GPT', short_name: 'Sol', model_version: '6.1' }, tokens: { spent: null, input: 0, output: 0, cached: 0, sessions: 0, unreported: 0, estimated: null } } },
  ]
  for (const prefs of [DEFAULT_MODEL_DISPLAY, { ...DEFAULT_MODEL_DISPLAY, modelNames: 'full' as const, modelVersion: 'hide' as const }]) {
    const named = columnTip('model', figures, { ...ctx, modelName: shownModelNames(rows, prefs) })!.lines.slice(0, 3).map(l => l!.label)
    assert.deepEqual(named, [...rows.map(row => modelCell(row, prefs).text), 'grok-4.7'])
  }
  assert.deepEqual(columnTip('model', figures, { ...ctx, modelName: shownModelNames(rows) })!.lines.slice(0, 2).map(l => l!.label), ['Opus 5.5', 'Sol 6.1'])
})

test('Hide closed hides rows only when the view has rows in the hidden statuses', () => {
  assert.equal(hidesRows(wholeFigures(), true), true)
  assert.equal(hidesRows(wholeFigures(), false), false)
  const open = { ...viewFigures(), columns: { ...viewFigures().columns, status: { rows: 3, included: 3, without_value: 0, others: 0, partial: false, values: [{ value: 'open', count: 3, hours: 0 }] } } }
  assert.equal(hidesRows(open, true), false)
  // Hide finished hides Done, Delivered and Accepted only.
  const cancelledOnly = { ...open, columns: { ...open.columns, status: { ...open.columns.status, values: [{ value: 'cancelled', count: 2, hours: 0 }] } } }
  assert.equal(hidesRows(cancelledOnly, true, ['done', 'delivered', 'accepted']), false)
  assert.equal(hidesRows(cancelledOnly, true), true)
})

test('only a figures object counts as figures', () => {
  assert.equal(validFigures(wholeFigures()), true)
  assert.equal(validFigures({ items: [] }), false)
  assert.equal(validFigures(null), false)
  assert.equal(validFigures({ ...wholeFigures(), groups: { completed: { hours: 1, tickets: 1, statuses: {} } } }), false)
  assert.equal(validFigures({ ...wholeFigures(), total_hours: 'many' }), false)
})
