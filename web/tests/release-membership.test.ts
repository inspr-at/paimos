// SPDX-License-Identifier: AGPL-3.0-only
import assert from 'node:assert/strict'
import { test } from 'node:test'
import { APIError } from '../src/lib/api.ts'
import type { Journey, ReleaseRef } from '../src/lib/journey.ts'
import {
  availabilityMark, canOpenRelease, canSelectTicket, isMoveConflict, isStaleRevision, nextReleaseTitle,
  parseMembership, parseTicketOptions, planningRelease, ticketOptionQuery,
} from '../src/lib/releaseMembership.ts'

const release = (id: string, number: number, title = `Release ${number}`): ReleaseRef => ({ id, key: `R-${number}`, title, state: 'backlog', created_at: '', number, version: null })
const journey = (patch: Partial<Journey> = {}): Journey => ({
  project_node_id: 'p', profile: 'professional', revision: 3, stage: 'plan', stages: [],
  next_action: { key: 'start_build', label: 'Start build', stage: 'plan', available: true, approval_request_id: null },
  requirements_revision: 1, current_release_id: 'r-2', requirements_digest_sha256: 'a', requirements_approval_scope: 's',
  launch_readiness: { can_admit: false, reason: '' }, ...patch,
})

test('ticket option query keeps only the filters that are set', () => {
  assert.equal(ticketOptionQuery({}), '')
  assert.equal(ticketOptionQuery({ q: '  pharos-21 ', status: 'backlog', epic: 'n-epic', type: 'ticket', limit: 50 }), '?q=pharos-21&status=backlog&epic=n-epic&type=ticket&limit=50')
})

test('closed, released and included tickets cannot be picked; another open release can', () => {
  assert.equal(canSelectTicket({ availability: 'addable' }), true)
  assert.equal(canSelectTicket({ availability: 'other_release' }), true)
  assert.equal(canSelectTicket({ availability: 'closed' }), false)
  assert.equal(canSelectTicket({ availability: 'released' }), false)
  assert.equal(canSelectTicket({ availability: 'included' }), false)
  assert.equal(availabilityMark({ availability: 'other_release', release_title: 'Release 9' }), 'In Release 9')
  assert.equal(availabilityMark({ availability: 'released', release_title: null }), 'Already released')
  assert.equal(availabilityMark({ availability: 'addable', release_title: null }), '')
})

test('a missing availability is not addable', () => {
  const page = parseTicketOptions({ expected_revision: 7, tickets: [{ ticket_node_id: 'n-1', key: 'PHAROS-11', title: 'One', availability: 'later' }] })
  assert.equal(page.expected_revision, 7)
  assert.equal(page.tickets[0].availability, 'closed')
  assert.throws(() => parseTicketOptions({ tickets: [] }), /revision/)
})

test('membership answer is the walker plus the event to undo', () => {
  const result = parseMembership({ walker: { release_node_id: 'r', project_node_id: 'p', state: 'planning', revision: 2, features: [], tickets: [] }, event_id: 81 })
  assert.equal(result.event_id, 81)
  assert.equal(result.walker.revision, 2)
  assert.throws(() => parseMembership({ event_id: 1 }), /did not answer/)
})

test('the open release is the current one while planning, and the next title counts on', () => {
  const releases = [release('r-1', 1, '260901120000.0.0'), release('r-2', 2)]
  assert.equal(planningRelease(journey(), releases)?.id, 'r-2')
  assert.equal(planningRelease(journey({ stage: 'live' }), releases), null)
  assert.equal(nextReleaseTitle(releases), 'Release 3')
  assert.equal(nextReleaseTitle([]), 'Release 1')
})

test('a new release waits until the current one is live', () => {
  const planning = release('r-2', 2)
  assert.equal(canOpenRelease(journey(), planning).ok, false)
  assert.match(canOpenRelease(journey(), planning).reason, /Release 2 is still in planning/)
  const live = journey({ stage: 'live', next_action: { key: 'plan_next_release', label: 'Plan release 3', stage: 'live', available: true, approval_request_id: null } })
  assert.equal(canOpenRelease(live, null).ok, true)
  const blocked = journey({ stage: 'live', next_action: { key: 'plan_next_release', label: 'Plan release 3', stage: 'live', available: false, reason: 'Not live yet.', approval_request_id: null } })
  assert.equal(canOpenRelease(blocked, null).ok, false)
  assert.equal(canOpenRelease(blocked, null).reason, 'Not live yet.')
})

test('another open release is a confirmable conflict; a stale revision is not', () => {
  const move = new APIError(409, 'ticket belongs to another open release', {})
  const coded = new APIError(409, 'conflict', { code: 'other_release' })
  const stale = new APIError(409, 'release revision changed', {})
  const closed = new APIError(409, 'closed or released tickets cannot be added', {})
  assert.equal(isMoveConflict(move), true)
  assert.equal(isMoveConflict(coded), true)
  assert.equal(isMoveConflict(stale), false)
  assert.equal(isStaleRevision(stale), true)
  assert.equal(isMoveConflict(closed), false)
})
