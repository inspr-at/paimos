// SPDX-License-Identifier: AGPL-3.0-only
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { PLACES, placeOf, releaseChordOpen, sequence, visiblePlaces } from '../src/lib/places.ts'

test('places are Projects, Decision Desk, Agents, Business in that order; pages belong to one or none', () => {
  assert.deepEqual(PLACES.map(p => p.label), ['Projects', 'Decision Desk', 'Agents', 'Business'])
  assert.equal(placeOf('/'), 'projects')
  assert.equal(placeOf('/briefing'), null)
  assert.equal(placeOf('/decision-desk'), 'desk')
  assert.equal(placeOf('/p/PHAROS/PHAROS-11'), 'projects')
  assert.equal(placeOf('/agents/5e00'), 'agents')
  assert.equal(placeOf('/business/quotes'), 'business')
  assert.equal(placeOf('/settings/personal'), null)
  assert.equal(placeOf('/businesses'), null)
})

test('Business shows only while one of its parts is open', () => {
  assert.deepEqual(visiblePlaces({ signedIn: true, business: false }).map(p => p.id), ['projects', 'desk', 'agents'])
  assert.deepEqual(visiblePlaces({ signedIn: true, business: true }).map(p => p.id), ['projects', 'desk', 'agents', 'business'])
  assert.deepEqual(visiblePlaces({ signedIn: false, business: true }), [])
})

test('g then a place key goes there within the window; anything else disarms', () => {
  const next = sequence(1000)
  const places = visiblePlaces({ signedIn: true, business: false })
  assert.equal(next('g', 0, places), 'armed')
  assert.equal((next('a', 400, places) as { id: string }).id, 'agents')
  assert.equal(next('p', 500, places), null) // not armed any more
  assert.equal(next('g', 1000, places), 'armed')
  assert.equal(next('b', 1200, places), null) // Business is not visible
  assert.equal(next('g', 2000, places), 'armed')
  assert.equal(next('p', 3500, places), null) // too late
  assert.equal(next('G', 4000, places), 'armed')
  assert.equal((next('P', 4100, places) as { id: string }).id, 'projects')
})

test('a place chord finishes only through the release menu g opened, before it expires', () => {
  assert.equal(releaseChordOpen(['Release for PHAROS-11'], 0, 1500), true)
  assert.equal(releaseChordOpen(['Release for PHAROS-11'], 0, 1501), false)
  assert.equal(releaseChordOpen(['Status of PHAROS-11'], 0, 100), false)
  assert.equal(releaseChordOpen(['Release for PHAROS-11', 'Status of PHAROS-11'], 0, 100), false)
  assert.equal(releaseChordOpen([], 0, 100), false)
  assert.equal(releaseChordOpen(['Release for PHAROS-11'], null, 100), false)
})
