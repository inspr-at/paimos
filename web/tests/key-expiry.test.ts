// SPDX-License-Identifier: AGPL-3.0-only
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { keyExpiry, keyExpiryAfter } from '../src/lib/access.ts'

test('expiry choices include Never and use second precision from an injected clock', () => {
  const now = Date.parse('2026-10-02T12:00:00.987Z')
  assert.equal(keyExpiryAfter(0, now), null)
  for (const days of [30, 90, 365]) assert.equal(Date.parse(keyExpiryAfter(days, now)!), Math.floor(now / 1000) * 1000 + days * 86_400_000)
})

const now = Date.parse('2026-09-26T12:00:00Z')
const day = 86_400_000
const key = (offset: number | null, revoked_at: string | null = null) => ({ expires_at: offset === null ? null : new Date(now + offset).toISOString(), revoked_at })

test('key expiry distinguishes never, exact expiry, and the strict 14-day warning boundary', () => {
  assert.deepEqual(keyExpiry(key(null), now), { label: 'Never', soon: false })
  assert.deepEqual(keyExpiry(key(-1), now), { label: 'Expired', soon: false })
  assert.deepEqual(keyExpiry(key(0), now), { label: 'Expired', soon: false })
  assert.deepEqual(keyExpiry(key(day - 1), now), { label: 'In less than a day', soon: true })
  assert.deepEqual(keyExpiry(key(day), now), { label: 'In 1 day', soon: true })
  assert.deepEqual(keyExpiry(key(13 * day), now), { label: 'In 13 days', soon: true })
  assert.equal(keyExpiry(key(14 * day - 1), now).soon, true)
  assert.deepEqual(keyExpiry(key(14 * day), now), { label: 'In 14 days', soon: false })
  assert.deepEqual(keyExpiry(key(90 * day), now), { label: 'In 90 days', soon: false })
  assert.equal(keyExpiry(key(day, '2026-09-25T12:00:00Z'), now).soon, false)
})
