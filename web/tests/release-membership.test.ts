// SPDX-License-Identifier: AGPL-3.0-only
import assert from 'node:assert/strict'
import { afterEach, test } from 'node:test'
import { APIError } from '../src/lib/api.ts'
import { assertReleaseOpen, availabilityMark, canSelectTicket, isMoveConflict, isStaleRevision, listNativeMemberships, membershipQuery, nativeViews, newReleaseFailure, openedMembershipMessage, openReleaseWithTickets, parseMembership, parseNativeMemberships, parseTicketOptions, reconcileOpenedMembership, releaseCell, ReleaseUnconfirmed, resetReleaseOpenForTests, ticketOptionQuery, type ReleaseOpenClient } from '../src/lib/releaseMembership.ts'
afterEach(() => resetReleaseOpenForTests())
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


test('another open release is a confirmable conflict; a stale revision is not', () => {
  const move = new APIError(409, 'ticket belongs to another open release', {})
  const producer = new APIError(409, 'confirm_move required to move a ticket from another release', {})
  const coded = new APIError(409, 'conflict', { code: 'other_release' })
  const stale = new APIError(409, 'release revision changed', {})
  const closed = new APIError(409, 'closed tickets cannot be added', {})
  assert.equal(isMoveConflict(move), true)
  assert.equal(isMoveConflict(producer), true)
  assert.equal(isMoveConflict(coded), true)
  assert.equal(isMoveConflict(stale), false)
  assert.equal(isStaleRevision(stale), true)
  assert.equal(isMoveConflict(closed), false)
})


test('a task is unsupported and not addable', () => {
  const page = parseTicketOptions({ expected_revision: 4, tickets: [{ ticket_node_id: 'n-9', key: 'PHAROS-19', title: 'A task', type: 'task', availability: 'unsupported' }] })
  assert.equal(page.tickets[0].availability, 'unsupported')
  assert.equal(canSelectTicket(page.tickets[0]), false)
  assert.equal(availabilityMark(page.tickets[0]), 'Not a release ticket')
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


test('parents show actual ships-in summaries and deduplicate leaf counts', () => {
  const rows = parseNativeMemberships({ tickets: [
    { ticket_node_id: 'parent', is_parent: true, release_count: 1, release_node_id: 'r-2', release_title: 'Cobalt Comet', release_state: 'planning', leaf_node_ids: ['a', 'b'], assigned_leaf_count: 2 },
    { ticket_node_id: 'a', is_parent: false, release_count: 1, release_node_id: 'r-2', release_title: 'Cobalt Comet', release_state: 'planning', leaf_node_ids: ['a'] },
    { ticket_node_id: 'split', is_parent: true, release_count: 2, release_node_id: null, release_title: null, release_state: null, leaf_node_ids: ['a', 'b'] },
    { ticket_node_id: 'later', is_parent: false, release_count: 0, release_node_id: null, release_title: null, release_state: null, inheritance_note: 'parent_release_closed' },
  ] })
  const views = nativeViews(['parent', 'a', 'split', 'later'], rows)
  assert.equal(releaseCell(views.get('parent')).text, 'Ships in Cobalt Comet')
  assert.equal(releaseCell(views.get('split')).text, 'Ships in 2 releases')
  assert.match(releaseCell(views.get('later')).label, /Backlog.*frozen or released/)
  assert.deepEqual(reconcileOpenedMembership(['parent', 'a'], views), { status: 'added', releaseId: 'r-2', releaseTitle: 'Cobalt Comet', count: 2 })
  assert.equal(reconcileOpenedMembership(['split'], views).status, 'changed')
})


test('parent replay needs valid coverage and counts overlapping full scopes once', () => {
  const parent = { ticket_node_id: 'parent', is_parent: true, release_count: 1,
    release_node_id: 'r-3', release_title: 'Release 3', release_state: 'planning', leaf_node_ids: ['a', 'b'] }
  for (const coverage of [undefined, -1, 1.5, 3]) {
    const views = nativeViews(['parent'], parseNativeMemberships({ tickets: [{ ...parent, assigned_leaf_count: coverage }] }))
    assert.deepEqual(reconcileOpenedMembership(['parent'], views), { status: 'unknown' })
  }
  for (const coverage of [0, 1]) {
    const views = nativeViews(['parent'], parseNativeMemberships({ tickets: [{ ...parent, assigned_leaf_count: coverage }] }))
    assert.deepEqual(reconcileOpenedMembership(['parent'], views), { status: 'changed' })
  }
  for (const leaves of [['a', 'a'], ['a', 7], ['a', '']]) {
    const views = nativeViews(['parent'], parseNativeMemberships({ tickets: [{ ...parent, leaf_node_ids: leaves, assigned_leaf_count: 1 }] }))
    assert.deepEqual(reconcileOpenedMembership(['parent'], views), { status: 'unknown' })
  }
  const full = nativeViews(['parent', 'nested'], parseNativeMemberships({ tickets: [
    { ...parent, assigned_leaf_count: 2 },
    { ...parent, ticket_node_id: 'nested', leaf_node_ids: ['b'], assigned_leaf_count: 1 },
  ] }))
  assert.deepEqual(reconcileOpenedMembership(['parent', 'nested'], full), { status: 'added', releaseId: 'r-3', releaseTitle: 'Release 3', count: 2 })
})

test('native release creation keeps the same key after a lost response and a later denial', async () => {
  const keys: string[] = []
  let attempt = 0
  const client: ReleaseOpenClient = { newKey: () => 'native-key', create: async (_project, body) => {
    keys.push(body.idempotency_key)
    if (++attempt === 1) throw new Error('lost response')
    if (attempt === 2) throw new APIError(403, 'project access required', {})
    return { walker: { release_node_id: 'original', project_node_id: 'p', state: 'planning', revision: 2, features: [], tickets: [] }, event_id: 7 }
  } }
  await assert.rejects(openReleaseWithTickets('p', ['a'], client), ReleaseUnconfirmed)
  await assert.rejects(openReleaseWithTickets('p', ['a'], client), ReleaseUnconfirmed)
  assert.throws(() => assertReleaseOpen('p', ['b']), ReleaseUnconfirmed)
  const result = await openReleaseWithTickets('p', ['a'], client)
  assert.equal(result.result.walker.release_node_id, 'original')
  assert.deepEqual(keys, ['native-key', 'native-key', 'native-key'])
})
