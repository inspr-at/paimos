// SPDX-License-Identifier: AGPL-3.0-only
import assert from 'node:assert/strict'
import { test } from 'node:test'
import { flowNameBudget, placeFlowPill } from '../src/lib/flowPill.ts'

test('the pill sits on the page centre when both sides leave room', () => {
  const placed = placeFlowPill({ left: 0, right: 1000 }, { left: 20, right: 100 }, { left: 860, right: 980 }, 200)
  assert.deepEqual(placed, { left: 400, width: 200 })
})

test('the pill shifts so it does not cover the version control', () => {
  const placed = placeFlowPill({ left: 0, right: 800 }, { left: 16, right: 90 }, { left: 500, right: 780 }, 220)
  assert.equal(placed.width, 220)
  assert.equal(placed.left, 272)
  assert.ok(placed.left >= 90 + 8)
  assert.ok(placed.left + placed.width <= 500 - 8)
})

test('a wide pill shrinks to the gap between the wordmark and the version', () => {
  const placed = placeFlowPill({ left: 0, right: 375 }, { left: 12, right: 130 }, { left: 230, right: 363 }, 220)
  assert.equal(placed.width, 84)
  assert.equal(placed.left, 138)
})

test('no gap yields a zero-width pill instead of an overlap', () => {
  const placed = placeFlowPill({ left: 0, right: 375 }, { left: 12, right: 200 }, { left: 190, right: 360 }, 180)
  assert.equal(placed.width, 0)
})

test('the wordmark stays whole, or hides when the next action would be crushed', () => {
  assert.equal(flowNameBudget(1400, 180, 96, 200), null)
  assert.equal(flowNameBudget(351, 170, 96, 202), 0)
  assert.equal(flowNameBudget(560, 180, 96, 202), null)
})
