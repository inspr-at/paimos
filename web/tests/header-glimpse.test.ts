// SPDX-License-Identifier: AGPL-3.0-only
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { headerGlimpseAllowed, headerGlimpseGraphReady } from '../src/lib/headerGlimpse.ts'

const on = { enabled: true, reduced: false, wide: true, tickets: 8 }

test('the header glimpse needs a wide viewport, motion, and eight tickets', () => {
  assert.equal(headerGlimpseAllowed(on), true)
  assert.equal(headerGlimpseAllowed({ ...on, enabled: false }), false)
  assert.equal(headerGlimpseAllowed({ ...on, reduced: true }), false)
  assert.equal(headerGlimpseAllowed({ ...on, wide: false }), false)
  assert.equal(headerGlimpseAllowed({ ...on, tickets: 7 }), false)
})

test('a ready graph has eight tickets and at least one link', () => {
  assert.equal(headerGlimpseGraphReady(8, 1), true)
  assert.equal(headerGlimpseGraphReady(8, 0), false)
  assert.equal(headerGlimpseGraphReady(7, 4), false)
})
