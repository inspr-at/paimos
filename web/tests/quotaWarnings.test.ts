// SPDX-License-Identifier: AGPL-3.0-only
import test from 'node:test'
import assert from 'node:assert/strict'
import { validQuotaThresholds } from '../src/lib/quotaWarnings.ts'
test('quota thresholds accept whole percentages only, with urgent below early', () => {
  for (const [early, urgent, valid] of [[10, 3, true], [50, 1, true], [2, 1, true], [51, 3, false], [10, 0, false], [10, 10, false], [10, 11, false], [10.5, 3, false], [10, 3.1, false], [NaN, 3, false], [10, Infinity, false]] as const) {
    assert.equal(validQuotaThresholds({ early_percent: early, urgent_percent: urgent }), valid, `${early}/${urgent}`)
  }
})
