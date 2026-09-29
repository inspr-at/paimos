// SPDX-License-Identifier: AGPL-3.0-only
import type { ListItem } from '../src/lib/api.ts'
import { compareRows } from '../src/lib/ticketList.ts'
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { estimateControlLabel, estimateDisplay, estimateHours, formatEstimate, parseEstimate } from '../src/lib/estimates.ts'

test('agent hours accept the same hour/minute syntax and bounds as the CLI', () => {
  for (const [value, hours] of [['2h', 2], ['90m', 1.5], ['1.5', 1.5], ['.5h', .5], [' 200 ', 200], ['12000m', 200]] as const) assert.equal(parseEstimate(value), hours)
  for (const value of ['', '0', '-1h', '200.1', '12001m', 'NaN', 'Inf', '1e2', '2 h', '1h30m', '1.', '+2']) assert.equal(parseEstimate(value), null, value)
  assert.equal(formatEstimate(.5), '30m'); assert.equal(formatEstimate(2), '~2h'); assert.equal(formatEstimate(.001), '<1m')
})
test('an unconfirmed agent estimate names the draft and the value', () => {
  const item = { fields: { estimate_hours: 2, estimate_source: 'agent', estimate_confirmed: false }, estimate: { hours: 2, open_children: 0, estimated_children: 0, by: { id: 'worker', name: 'Estimate worker' } } }
  assert.equal(estimateControlLabel(item), 'Estimate: ~2h, agent draft, not confirmed, by Estimate worker. Edit estimate')
  assert.match(estimateControlLabel(item), /agent draft, not confirmed/)
  assert.match(estimateControlLabel(item), /~2h/)
  assert.equal(estimateControlLabel({ ...item, estimate: { hours: 2, open_children: 0, estimated_children: 0 } }), 'Estimate: ~2h, agent draft, not confirmed. Edit estimate')
  assert.equal(estimateControlLabel({ ...item, fields: { ...item.fields, estimate_confirmed: true } }), 'Estimate: ~2h. Edit estimate')
  assert.equal(estimateControlLabel({ fields: { estimate_hours: .5, estimate_source: 'person', estimate_confirmed: true } }), 'Estimate: 30m. Edit estimate')
  assert.equal(estimateControlLabel({ fields: {} }), 'Add estimate')
})
test('draft attribution, confirmation, legacy points and epic coverage remain distinct', () => {
  const item = { fields: { estimate_hours: 2, estimate_source: 'agent', estimate_at: '2026-09-29T12:00:00Z' }, estimate: { hours: 2, open_children: 0, estimated_children: 0, by: { id: 'worker', name: 'Estimate worker' } } }
  assert.equal(estimateDisplay(item).draft, true)
  assert.match(estimateDisplay(item).tip, /Agent estimate by Estimate worker, /)
  assert.equal(estimateDisplay({ ...item, fields: { ...item.fields, estimate_confirmed: true } }).draft, false)
  assert.equal(estimateDisplay({ fields: { estimate_lp: 3 } }).text, '3 pt')
  assert.equal(estimateHours({ fields: { estimate_hours: '2' } }), null)
  assert.equal(estimateDisplay({ ...item, kind_slug: 'epic', estimate: { hours: 250, estimated_children: 3, open_children: 5 } }).tip, '3 of 5 open children estimated · agent hours')
  const uncovered = estimateDisplay({ kind_slug: 'epic', fields: {}, estimate: { hours: null, estimated_children: 0, open_children: 4 } })
  assert.equal(uncovered.text, '')
  assert.equal(uncovered.tip, '0 of 4 open children estimated · agent hours')
  assert.equal(estimateHours({ ...item, kind_slug: 'epic', estimate: { hours: null, estimated_children: 0, open_children: 5 } }), null)
})

test('outline estimate sorting uses epic totals and leaves empty values last in either direction', () => {
  const rows = [
    { id: 'missing', kind_slug: 'ticket', fields: {} },
    { id: 'ticket', kind_slug: 'ticket', fields: { estimate_hours: .5 } },
    { id: 'epic', kind_slug: 'epic', fields: { estimate_hours: 99 }, estimate: { hours: 2, open_children: 1, estimated_children: 1 } },
  ] as ListItem[]
  assert.deepEqual([...rows].sort(compareRows([{ field: 'estimate', desc: false }])).map(r => r.id), ['ticket', 'epic', 'missing'])
  assert.deepEqual([...rows].sort(compareRows([{ field: 'estimate', desc: true }])).map(r => r.id), ['epic', 'ticket', 'missing'])
})
