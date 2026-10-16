// SPDX-License-Identifier: AGPL-3.0-only
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { createPanelOpening } from '../src/lib/leadPanel.ts'

// AEON-1439 risk: a refresh that drops and restores the session or the identity
// re-runs the opening, so the chosen tab jumps back and "Opened on …" blinks.
test('AEON-1439: only another person or another session opens the panel again; refreshes never do and an abandoned opening opens on the next sight', () => {
  const openings = createPanelOpening()
  assert.equal(openings.begin('', 'lead'), '', 'no identity yet: nothing opens')
  assert.equal(openings.begin('t1:me', undefined), '', 'no session yet: nothing opens')
  const lead = openings.begin('t1:me', 'lead')
  assert.notEqual(lead, '')
  for (const [owner, session, what] of [['t1:me', 'lead', 'a plain refresh'], ['t1:me', undefined, 'the session briefly missing'], ['', 'lead', 'the identity briefly missing'], ['t1:me', 'lead', 'the same pair back']] as const)
    assert.equal(openings.begin(owner, session), '', what)
  const worker = openings.begin('t1:me', 'worker')
  assert.ok(worker && worker !== lead, 'another session opens')
  assert.equal(openings.begin('t1:me', 'lead'), lead, 'back to the lead from another session opens again')
  assert.notEqual(openings.begin('t1:you', 'lead'), '', 'another person opens')
  assert.notEqual(openings.begin('t2:me', 'lead'), '', 'another workspace opens')
  // An opening whose session was missing when its reads returned opens on the next sight.
  const pending = openings.begin('t1:me', 'next')
  openings.abandon(lead)
  assert.equal(openings.begin('t1:me', 'next'), '', 'abandoning an older key keeps the current opening')
  openings.abandon(pending)
  assert.equal(openings.begin('t1:me', 'next'), pending)
  assert.equal(openings.begin('t1:me', 'next'), '')
})
