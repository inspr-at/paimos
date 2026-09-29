// SPDX-License-Identifier: AGPL-3.0-only
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { parseNodeRatings, parseRating, reworkDetail, reworkPercent, signalLine } from '../src/lib/deliveryRating.ts'

test('rework rate is exceptions divided by deliveries', () => {
  assert.equal(reworkPercent(1, 2), '50%')
  assert.equal(reworkPercent(1, 3), '33%')
  assert.equal(reworkPercent(0, 4), '')
  assert.equal(reworkPercent(3, 2), '')
  assert.equal(reworkDetail(1, 4), '1 of 4')
  assert.equal(reworkDetail(0, 4), '')
})

test('objective signals omit zeros', () => {
  assert.equal(signalLine({ review_rounds: 2, ci_failures: 1, reverts: 1 }), '2 review rounds · 1 CI failure · 1 revert')
  assert.equal(signalLine({ review_rounds: 1, ci_failures: 0, reverts: 2 }), '1 review round · 2 reverts')
  assert.equal(signalLine({ review_rounds: 0, ci_failures: 0, reverts: 0 }), '')
})

test('a mark can omit the score', () => {
  assert.equal(parseNodeRatings({ id: 'n-epic', title: 'Guarded', kind_id: 'k', key: 'PHAROS-10' }), null)
  const page = parseNodeRatings({
    node_id: 'n-epic',
    sessions: [{
      session_id: '5e000000-0000-4000-8000-000000000001',
      votes: 1,
      average: null,
      mine: { score: null, tags: ['rework'], comment: 'Needs another pass', updated_at: '2026-09-29T08:00:00Z' },
      harness: 'codex',
      signals: { review_rounds: 1, ci_failures: 0, reverts: 0 },
    }],
  })
  assert.equal(page?.length, 1)
  assert.equal(page?.[0].mine?.score, null)
  assert.equal(page?.[0].mine?.comment, 'Needs another pass')
  assert.equal(parseRating({ session_id: 'x', votes: 0, average: null, mine: { tags: [], comment: 'no', updated_at: '' }, signals: { review_rounds: 0, ci_failures: 0, reverts: 0 } }), null)
})
