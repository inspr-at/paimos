// SPDX-License-Identifier: AGPL-3.0-only
import assert from 'node:assert/strict'
import { afterEach, test } from 'node:test'
import { APIError, RequestFailure } from '../src/lib/api.ts'
import type { Journey, ReleaseRef } from '../src/lib/journey.ts'
import {
  assertReleaseOpen, availabilityMark, canOpenRelease, canSelectTicket, isMoveConflict, isStaleRevision, listNativeMemberships, membershipQuery, nativeViews,
  newReleaseAction, newReleaseFailure, nextReleaseTitle, openedMembershipMessage, openReleaseWithTickets, parseMembership, parseNativeMemberships, parseTicketOptions,
  planningRelease, reconcileOpenedMembership, releaseCell, ReleaseUnconfirmed, resetReleaseOpenForTests, ticketOptionQuery,
  type ReleaseOpenClient,
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

test('closed, released and included tickets cannot be picked; another planning release can', () => {
  assert.equal(canSelectTicket({ availability: 'addable' }), true)
  assert.equal(canSelectTicket({ availability: 'other_release' }), true)
  assert.equal(canSelectTicket({ availability: 'active_release' }), false)
  assert.equal(canSelectTicket({ availability: 'closed' }), false)
  assert.equal(canSelectTicket({ availability: 'released' }), false)
  assert.equal(canSelectTicket({ availability: 'included' }), false)
  assert.equal(availabilityMark({ availability: 'other_release', release_title: 'Release 9' }), 'In Release 9')
  assert.equal(availabilityMark({ availability: 'active_release', release_title: 'Release 4' }), 'In Release 4 · not planning')
  assert.equal(availabilityMark({ availability: 'active_release', release_title: null }), 'Not in a planning release')
  assert.equal(availabilityMark({ availability: 'released', release_title: null }), 'Already released')
  assert.equal(availabilityMark({ availability: 'addable', release_title: null }), '')
})

test('a missing availability is not addable', () => {
  const page = parseTicketOptions({ expected_revision: 7, tickets: [{ ticket_node_id: 'n-1', key: 'PHAROS-11', title: 'One', availability: 'later' }] })
  assert.equal(page.expected_revision, 7)
  assert.equal(page.tickets[0].availability, 'unavailable')
  assert.equal(canSelectTicket(page.tickets[0]), false)
  assert.equal(availabilityMark(page.tickets[0]), 'Cannot be added')
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
  const producer = new APIError(409, 'confirm_move required to move a ticket from another release', {})
  const coded = new APIError(409, 'conflict', { code: 'other_release' })
  const stale = new APIError(409, 'release revision changed', {})
  const journeyStale = new APIError(409, 'journey revision is stale', {})
  const closed = new APIError(409, 'closed tickets cannot be added', {})
  assert.equal(isMoveConflict(move), true)
  assert.equal(isMoveConflict(producer), true)
  assert.equal(isMoveConflict(coded), true)
  assert.equal(isMoveConflict(stale), false)
  assert.equal(isMoveConflict(journeyStale), false)
  assert.equal(isStaleRevision(stale), true)
  assert.equal(isStaleRevision(journeyStale), true)
  assert.equal(isMoveConflict(closed), false)
})

test('a task is unsupported and not addable', () => {
  const page = parseTicketOptions({ expected_revision: 4, tickets: [{ ticket_node_id: 'n-9', key: 'PHAROS-19', title: 'A task', type: 'task', availability: 'unsupported' }] })
  assert.equal(page.tickets[0].availability, 'unsupported')
  assert.equal(canSelectTicket(page.tickets[0]), false)
  assert.equal(availabilityMark(page.tickets[0]), 'Not a release ticket')
})

test('a new release carries its tickets on the journey action', () => {
  const ids = ['11111111-1111-4111-8111-111111111111', '22222222-2222-4222-8222-222222222222']
  const next = newReleaseAction(journey({ stage: 'live', current_release_id: 'r-2', revision: 12, next_action: { key: 'plan_next_release', label: 'Plan release 3', stage: 'live', available: true, approval_request_id: null } }), ids, 'next-with-tickets')
  assert.deepEqual(next, { action: 'plan_next_release', expected_revision: 12, idempotency_key: 'next-with-tickets', release_id: 'r-2', ticket_node_ids: ids })
  assert.equal('confirm_move' in next, false)
  const first = newReleaseAction(journey({ current_release_id: null, revision: 4, next_action: { key: 'open_first_release', label: 'Open release 1', stage: 'plan', available: true, approval_request_id: null } }), ids, 'first-with-tickets')
  assert.deepEqual(first, { action: 'open_first_release', expected_revision: 4, idempotency_key: 'first-with-tickets', ticket_node_ids: ids })
  assert.equal('release_id' in first, false)
  const rejected = new APIError(409, 'closed tickets cannot be added', {})
  assert.match(newReleaseFailure(rejected), /^The new release was not opened\. closed tickets cannot be added$/)
  assert.match(newReleaseFailure(new APIError(409, 'confirm_move required to move a ticket from another release', {})), /cannot move it/)
  assert.doesNotMatch(newReleaseFailure(rejected), /is open, but/)
  assert.match(newReleaseFailure(new RequestFailure('network')), /not confirmed/)
  assert.doesNotMatch(newReleaseFailure(new RequestFailure('network')), /not opened/)
  assert.match(newReleaseFailure(new APIError(502, 'bad gateway', {})), /not confirmed/)
  assert.doesNotMatch(newReleaseFailure(new APIError(502, 'bad gateway', {})), /not opened/)
})

const liveJourney = () => journey({
  stage: 'live', revision: 12, current_release_id: 'r-2',
  next_action: { key: 'plan_next_release', label: 'Plan release 3', stage: 'live', available: true, approval_request_id: null },
})
const openedJourney = () => journey({
  stage: 'plan', revision: 14, current_release_id: 'r-new',
  next_action: { key: 'start_build', label: 'Start build', stage: 'plan', available: false, reason: 'Build start needs an approved gate.', approval_request_id: null },
})
function openClient(post: ReleaseOpenClient['postAction'], patch: Partial<ReleaseOpenClient> = {}): ReleaseOpenClient {
  return { getJourney: async () => liveJourney(), postAction: post, listReleases: async () => [], newKey: () => 'key-1', ...patch }
}
afterEach(() => resetReleaseOpenForTests())

test('a lost response after commit retries the same key and does not write membership', async () => {
  const committed = new Map<string, Journey>()
  const keys: string[] = []
  const client = openClient(async (_project, action) => {
    keys.push(action.idempotency_key)
    const existing = committed.get(action.idempotency_key)
    if (existing) return existing
    committed.set(action.idempotency_key, openedJourney())
    throw new RequestFailure('network')
  })
  const ids = ['n-1', 'n-2', 'n-3', 'n-4', 'n-5']
  await assert.rejects(() => openReleaseWithTickets('p', ids, client), ReleaseUnconfirmed)
  assert.equal(assertReleaseOpen('p', ids), 'replay')
  assert.throws(() => assertReleaseOpen('p', ['other']), ReleaseUnconfirmed)
  const opened = await openReleaseWithTickets('p', ids, client)
  assert.equal(committed.size, 1)
  assert.deepEqual(keys, [keys[0], keys[0]])
  assert.equal(opened.journey.current_release_id, 'r-new')
  assert.equal(keys[0], 'key-1')
  assert.equal(assertReleaseOpen('p', ids), 'clear')
})

test('a denial after a lost create keeps the same key until the replay', async () => {
  for (const status of [401, 403, 429]) {
    resetReleaseOpenForTests()
    const committed = new Map<string, Journey>()
    const keys: string[] = []
    let phase: 'lose' | 'deny' | 'replay' = 'lose'
    const client = openClient(async (_project, action) => {
      keys.push(action.idempotency_key)
      if (phase === 'lose') {
        phase = 'deny'
        committed.set(action.idempotency_key, openedJourney())
        throw new RequestFailure('network')
      }
      if (phase === 'deny') {
        phase = 'replay'
        throw new APIError(status, 'project access required', {})
      }
      const saved = committed.get(action.idempotency_key)
      if (!saved) throw new Error('a second release was opened')
      return journey({ ...saved, current_release_id: 'r-4', revision: 30, stage: 'live' })
    })
    const ids = ['n-1']
    await assert.rejects(() => openReleaseWithTickets('p', ids, client), ReleaseUnconfirmed)
    await assert.rejects(() => openReleaseWithTickets('p', ids, client), (error: unknown) => {
      assert.ok(error instanceof ReleaseUnconfirmed)
      assert.match(newReleaseFailure(error), /not confirmed/)
      assert.doesNotMatch(newReleaseFailure(error), /not opened/)
      return true
    })
    assert.equal(assertReleaseOpen('p', ids), 'replay')
    const opened = await openReleaseWithTickets('p', ids, client)
    assert.equal(committed.size, 1)
    assert.deepEqual(keys, ['key-1', 'key-1', 'key-1'])
    assert.equal(opened.journey.current_release_id, 'r-4')
    assert.equal('releaseId' in opened, false)
    assert.equal(assertReleaseOpen('p', ids), 'clear')
  }
})

test('the first denial is a rejection and the next try uses a new key', async () => {
  const keys: string[] = []
  let n = 0
  const client = openClient(async (_project, action) => {
    keys.push(action.idempotency_key)
    throw new APIError(403, 'project access required', {})
  }, { newKey: () => `key-${++n}` })
  await assert.rejects(() => openReleaseWithTickets('p', ['n-1'], client), (error: unknown) => {
    assert.match(newReleaseFailure(error), /The new release was not opened\. project access required/)
    return true
  })
  assert.equal(assertReleaseOpen('p', ['n-1']), 'clear')
  await assert.rejects(() => openReleaseWithTickets('p', ['n-1'], client), (error: unknown) => error instanceof APIError)
  assert.deepEqual(keys, ['key-1', 'key-2'])
})

test('a 200 that does not name the release keeps the same key', async () => {
  const keys: string[] = []
  let calls = 0
  const client = openClient(async (_project, action) => {
    keys.push(action.idempotency_key)
    calls += 1
    if (calls === 1) return { revision: 14 } as Journey
    return openedJourney()
  })
  await assert.rejects(() => openReleaseWithTickets('p', ['n-1'], client), /not confirmed/)
  const opened = await openReleaseWithTickets('p', ['n-1'], client)
  assert.deepEqual(keys, ['key-1', 'key-1'])
  assert.equal(opened.journey.current_release_id, 'r-new')
})

test('a definite rejection is not retried with the same key', async () => {
  const keys: string[] = []
  let n = 0
  const client = openClient(async (_project, action) => {
    keys.push(action.idempotency_key)
    throw new APIError(409, 'closed tickets cannot be added', {})
  }, { newKey: () => `key-${++n}` })
  await assert.rejects(() => openReleaseWithTickets('p', ['n-1'], client), (error: unknown) => {
    assert.match(newReleaseFailure(error), /The new release was not opened\. closed tickets cannot be added/)
    return true
  })
  await assert.rejects(() => openReleaseWithTickets('p', ['n-1'], client), (error: unknown) => error instanceof APIError)
  assert.deepEqual(keys, ['key-1', 'key-2'])
})

test('two clicks share one create', async () => {
  let calls = 0
  const client = openClient(async () => {
    calls += 1
    await new Promise(resolve => setTimeout(resolve, 20))
    return openedJourney()
  }, { newKey: () => 'one-click' })
  const ids = ['n-1', 'n-2']
  const [first, second] = await Promise.all([openReleaseWithTickets('p', ids, client), openReleaseWithTickets('p', ids, client)])
  assert.equal(calls, 1)
  assert.equal(first.journey.current_release_id, 'r-new')
  assert.equal(second.journey.current_release_id, 'r-new')
})

test('replay membership is the tickets’ release, not the journey’s current release', () => {
  const member = nativeViews(['n-1', 'n-2'], [
    { ticket_node_id: 'n-1', release_node_id: 'r-3', release_title: 'Release 3', release_state: 'planning' },
    { ticket_node_id: 'n-2', release_node_id: 'r-3', release_title: 'Release 3', release_state: 'planning' },
  ])
  assert.deepEqual(reconcileOpenedMembership(['n-1', 'n-2'], member), { status: 'added', releaseId: 'r-3', releaseTitle: 'Release 3', count: 2 })
  const moved = nativeViews(['n-1'], [{ ticket_node_id: 'n-1', release_node_id: null, release_title: null, release_state: null }])
  const changed = reconcileOpenedMembership(['n-1'], moved)
  assert.equal(changed.status, 'changed')
  assert.equal(openedMembershipMessage(changed), 'The release action is confirmed, but those tickets are not all in one release.')
  assert.equal('count' in changed, false)
  const split = nativeViews(['n-1', 'n-2'], [
    { ticket_node_id: 'n-1', release_node_id: 'r-3', release_title: 'Release 3', release_state: 'planning' },
    { ticket_node_id: 'n-2', release_node_id: 'r-4', release_title: 'Release 4', release_state: 'planning' },
  ])
  assert.equal(reconcileOpenedMembership(['n-1', 'n-2'], split).status, 'changed')
  const missing = nativeViews(['n-1'], [])
  const unknown = reconcileOpenedMembership(['n-1'], missing)
  assert.equal(unknown.status, 'unknown')
  assert.match(openedMembershipMessage(unknown) ?? '', /could not be read/)
  assert.equal(openedMembershipMessage({ status: 'added', releaseId: 'r-3', releaseTitle: 'Release 3', count: 1 }), null)
})

test('native membership rows stay separate from an omitted ticket and an imported label', () => {
  assert.equal(membershipQuery(['b', 'a']), '?ticket_node_id=b&ticket_node_id=a')
  const rows = parseNativeMemberships({
    tickets: [
      { ticket_node_id: 'a', release_node_id: 'r-2', release_title: 'Release 2', release_state: 'planning' },
      { ticket_node_id: 'b', release_node_id: null, release_title: null, release_state: null },
    ],
  })
  const views = nativeViews(['a', 'b', 'c'], rows)
  assert.equal(views.get('a')?.status, 'member')
  assert.equal(releaseCell(views.get('a')).text, 'Release 2')
  assert.equal(views.get('b')?.status, 'none')
  assert.equal(releaseCell(views.get('b')).text, '—')
  assert.equal(views.get('c')?.status, 'unknown')
  assert.equal(releaseCell(undefined).label, 'Release unknown')
  assert.throws(() => parseNativeMemberships({ items: [] }), /did not answer/)
})

test('a failed membership read is unknown, and ids are asked in batches of 100', async () => {
  const failed = await listNativeMemberships('p', ['a'], async () => { throw new APIError(404, 'missing', {}) })
  assert.equal(failed.get('a')?.status, 'unknown')
  const paths: string[] = []
  const ids = Array.from({ length: 101 }, (_, index) => `id-${index}`)
  await listNativeMemberships('p', ids, async path => {
    paths.push(path)
    return { tickets: [] }
  })
  assert.equal(paths.length, 2)
  assert.equal(new URL(`http://local${paths[0]}`).searchParams.getAll('ticket_node_id').length, 100)
  assert.equal(new URL(`http://local${paths[1]}`).searchParams.getAll('ticket_node_id').length, 1)
})
