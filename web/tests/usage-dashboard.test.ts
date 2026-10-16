// SPDX-License-Identifier: AGPL-3.0-only
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { compactCount, formatUSD, rangeBounds, type UsageGroup } from '../src/lib/usageFormat.ts'

const blank = (): UsageGroup => ({
  label: 'All', sessions: 2, usage_rows: 0, unreported_sessions: 2,
  input_tokens: null, input_known_rows: 0, input_unknown_rows: 2,
  output_tokens: null, output_known_rows: 0, output_unknown_rows: 2,
  cached_input_tokens: null, cached_input_known_rows: 0, cached_input_unknown_rows: 2,
  tokens_state: 'unknown', estimated_cost_usd: null, cost_known_rows: 0, cost_unknown_rows: 2,
  cost_state: 'unknown', provisional_rows: 1, provisional_sessions: 1,
})

test('exact USD keeps fractional digits and never treats unknown as zero', () => {
  assert.equal(formatUSD('528.000000000000'), '528.00 USD')
  assert.equal(formatUSD('1.500000000001'), '1.500000000001 USD')
  assert.equal(formatUSD('0.000000000001'), '0.000000000001 USD')
  assert.equal(formatUSD('0.000000000000'), '0.00 USD')
  assert.equal(formatUSD(null), 'Unknown')
  assert.equal(formatUSD('1.25'), 'Unknown')

  const unknown = blank()
  assert.equal(formatUSD(unknown.estimated_cost_usd), 'Unknown')
})

test('a day range is exclusive at the next UTC midnight', () => {
  const bounds = rangeBounds(30, new Date('2026-09-27T15:04:00Z'))
  assert.equal(bounds.from, '2026-08-29T00:00:00Z')
  assert.equal(bounds.to, '2026-09-28T00:00:00Z')
})

test('compact counts shorten dense places and keep unknown apart from zero', () => {
  assert.equal(compactCount(null), null)
  assert.equal(compactCount('nope'), null)
  assert.equal(compactCount('0'), '0')
  assert.equal(compactCount('9120'), '9,120')
  assert.equal(compactCount('264000'), '264k')
  assert.equal(compactCount('2280000'), '2.28M')
  assert.equal(compactCount(48_960_000), '49M')
})
