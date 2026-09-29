// SPDX-License-Identifier: AGPL-3.0-only
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { crowdLine, formatRating, parseNodeRatings, signalLine } from '../src/lib/deliveryRating.ts'

test('a rating average trims trailing zeros and hides a single vote', () => {
  assert.equal(formatRating('4.50'), '4.5')
  assert.equal(formatRating('5.00'), '5')
  assert.equal(formatRating('1.33'), '1.33')
  assert.equal(formatRating('4.5'), '')
  assert.equal(formatRating(null), '')
  assert.equal(crowdLine(1, '5.00'), '')
  assert.equal(crowdLine(3, '4.50'), '4.5 from 3')
})

test('objective signals omit zeros', () => {
  assert.equal(signalLine({ review_rounds: 2, ci_failures: 1, reverts: 1 }), '2 review rounds · 1 CI failure · 1 revert')
  assert.equal(signalLine({ review_rounds: 1, ci_failures: 0, reverts: 2 }), '1 review round · 2 reverts')
  assert.equal(signalLine({ review_rounds: 0, ci_failures: 0, reverts: 0 }), '')
})

test('a ticket node is not a rating page', () => {
  assert.equal(parseNodeRatings({ id: 'n-epic', title: 'Guarded', kind_id: 'k', key: 'PHAROS-10' }), null)
  assert.equal(parseNodeRatings({ sessions: [{ title: 'nope' }] }), null)
  const page = parseNodeRatings({
    node_id: 'n-epic',
    sessions: [{
      session_id: '5e000000-0000-4000-8000-000000000001',
      votes: 0,
      average: null,
      mine: null,
      harness: 'codex',
      signals: { review_rounds: 1, ci_failures: 0, reverts: 0 },
    }],
  })
  assert.equal(page?.length, 1)
  assert.equal(page?.[0].session_id, '5e000000-0000-4000-8000-000000000001')
})
