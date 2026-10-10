// SPDX-License-Identifier: AGPL-3.0-only
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { capacityWaitText, isCapacityWait, WAIT_CODES } from '../src/lib/capacityWait.ts'

test('23:10 wait uses the schedule timezone and says when Codex starts', () => {
  assert.equal(capacityWaitText({ code: 'schedule', until: '2026-09-30T06:00:00Z', timezone: 'Europe/Vienna', run_now_allowed: true }, 'Codex agents', Date.parse('2026-09-29T21:10:00Z')), 'Codex agents start at 08:00')
})
test('every wait has useful copy; absent times are never invented', () => {
  for (const code of WAIT_CODES) assert.ok(capacityWaitText({ code, run_now_allowed: false }).length > 5)
  assert.equal(capacityWaitText({ code: 'reading', read_at: '2026-09-29T21:00:00Z', run_now_allowed: false }, 'Agents', Date.parse('2026-09-29T21:14:00Z')), 'Waiting for a reading · last 14 min ago')
  assert.equal(capacityWaitText({ code: 'reading', timezone: 'UTC', run_now_allowed: false }), 'Waiting for the next allowed run (light by day)')
  assert.equal(capacityWaitText({ code: 'reading', run_now_allowed: false }), 'Waiting for the first run’s reading')
  assert.equal(capacityWaitText({ code: 'vendor', run_now_allowed: false }), 'Waiting for the vendor to allow work again')
})
test('catalog wait validation rejects broken and arbitrary reasons', () => {
  assert.equal(isCapacityWait({ code: 'schedule', until: 'garbage', run_now_allowed: true }), false)
  assert.equal(isCapacityWait({ code: 'raw vendor text', run_now_allowed: true }), false)
  assert.equal(isCapacityWait({ code: 'hold', run_now_allowed: false }), true)
  assert.equal(isCapacityWait({ code: 'context', run_now_allowed: false }), true)
  assert.equal(isCapacityWait({ code: 'context', context: {}, run_now_allowed: false }), false)
})


test('residency waits survive strict parsing and explain the account fence', () => {
  assert.equal(isCapacityWait({ code: 'residency', run_now_allowed: false }), true)
  assert.equal(capacityWaitText({ code: 'residency', run_now_allowed: false }), 'Waiting for an account within the allowed providers')
})
