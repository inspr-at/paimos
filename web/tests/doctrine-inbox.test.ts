// SPDX-License-Identifier: AGPL-3.0-only
import assert from 'node:assert/strict'
import { test } from 'node:test'
import { inboxLabel, proposalState } from '../src/lib/doctrine.ts'
import { claimUnseen, doctrineInbox, inboxChanged, pollDoctrineInbox, readSeen, resetDoctrineInbox, toastText, unseen } from '../src/lib/doctrineInbox.ts'

const headline = (id: string, label: string, at: string) => ({ id, label, created_at: at })

test('a toast names one new proposal, or counts several', () => {
  assert.equal(toastText([]), null)
  assert.equal(toastText([headline('a', 'Estimate before work.', '2026-09-30T10:00:00Z')]), 'Doctrine change proposed: Estimate before work')
  assert.equal(toastText([headline('a', 'x', '2026-09-30T10:00:00Z'), headline('b', 'y', '2026-09-30T10:01:00Z')]), '2 doctrine changes proposed')
})

test('only proposals never seen toast, oldest first', () => {
  const items = [headline('b', 'second', '2026-09-30T10:01:00Z'), headline('a', 'first', '2026-09-30T10:00:00Z'), headline('c', 'seen', '2026-09-30T09:00:00Z')]
  assert.deepEqual(unseen(items, ['c']).map(item => item.id), ['a', 'b'])
  assert.deepEqual(unseen(items, ['a', 'b', 'c']), [])
})

test('seen proposals survive a reload per person and tolerate a broken store', () => {
  const store = new Map<string, string>()
  Object.assign(globalThis, { localStorage: { getItem: (k: string) => store.get(k) ?? null, setItem: (k: string, v: string) => { store.set(k, v) } } })
  assert.deepEqual(readSeen('p1'), [])
  store.set('aeon.doctrine-inbox.seen.p1', JSON.stringify(['a', 7, 'b']))
  assert.deepEqual(readSeen('p1'), ['a', 'b'])
  assert.deepEqual(readSeen('p2'), [])
  store.set('aeon.doctrine-inbox.seen.p1', '{')
  assert.deepEqual(readSeen('p1'), [])
})

test('labels fit a toast and inbox states read plainly', () => {
  assert.equal(inboxLabel('  Estimate   before work.  '), 'Estimate before work')
  assert.equal(inboxLabel('x'.repeat(80)).length, 60)
  assert.ok(inboxLabel('x'.repeat(80)).endsWith('…'))
  assert.equal(proposalState({ state: 'promoted', pinned_machines: 0 }), 'In the pinned doctrine')
  assert.equal(proposalState({ state: 'dismissed', pinned_machines: 0 }), 'Dismissed')
})

// A summary read whose answer the test releases later.
function deferred() {
  let release!: (value: { pending: number; items: ReturnType<typeof headline>[] }) => void
  const read = () => new Promise<{ pending: number; items: ReturnType<typeof headline>[] }>(resolve => { release = resolve })
  return { read, release: (value: { pending: number; items: ReturnType<typeof headline>[] }) => release(value) }
}
function memoryStore() {
  const store = new Map<string, string>()
  Object.assign(globalThis, { localStorage: { getItem: (k: string) => store.get(k) ?? null, setItem: (k: string, v: string) => { store.set(k, v) } } })
  return store
}

test('a late summary never restores a dot or a toast after a person acted', async () => {
  memoryStore()
  resetDoctrineInbox()
  const late = deferred()
  const poll = pollDoctrineInbox('p3', late.read)
  inboxChanged(0) // the person dismissed the last proposal meanwhile
  late.release({ pending: 1, items: [headline('d1', 'Dismissed meanwhile', '2026-09-30T10:00:00Z')] })
  assert.equal(await poll, null)
  assert.equal(doctrineInbox.pending, 0)
  // The next read is current and toasts what is new.
  const text = await pollDoctrineInbox('p3', async () => ({ pending: 1, items: [headline('n1', 'New one', '2026-09-30T10:05:00Z')] }))
  assert.equal(text, 'Doctrine change proposed: New one')
  assert.equal(doctrineInbox.pending, 1)
})

test('a session change drops the previous principal\'s reads', async () => {
  memoryStore()
  resetDoctrineInbox()
  const old = deferred()
  const poll = pollDoctrineInbox('old', old.read)
  resetDoctrineInbox()
  old.release({ pending: 2, items: [headline('o1', 'Old', '2026-09-30T10:00:00Z')] })
  assert.equal(await poll, null)
  assert.equal(doctrineInbox.principal, '')
  assert.equal(doctrineInbox.pending, 0)
  assert.equal(await pollDoctrineInbox('new', async () => ({ pending: 1, items: [headline('x1', 'Fresh', '2026-09-30T10:00:00Z')] })), 'Doctrine change proposed: Fresh')
  assert.equal(doctrineInbox.principal, 'new')
})

test('tabs polling at once claim each proposal once', async () => {
  memoryStore()
  const items = [headline('t1', 'one', '2026-09-30T10:00:00Z'), headline('t2', 'two', '2026-09-30T10:01:00Z')]
  const [a, b] = await Promise.all([claimUnseen('tabs', items), claimUnseen('tabs', items)])
  assert.deepEqual([...a, ...b].map(item => item.id).sort(), ['t1', 't2'])
  assert.ok(!a.length || !b.length)
})

test('a proposal another tab claimed does not toast here', async () => {
  memoryStore()
  const other = new BroadcastChannel('aeon.doctrine-inbox')
  // Open this tab's channel first, then let the other tab announce its claim.
  await claimUnseen('bc', [])
  await claimUnseen('bc', [headline('warm', 'warm', '2026-09-30T09:00:00Z')])
  other.postMessage({ principal: 'bc', ids: ['b1'] })
  for (let i = 0; i < 50 && !readSeen('bc').includes('b1'); i++) await new Promise(resolve => setTimeout(resolve, 5))
  other.close()
  assert.deepEqual(await claimUnseen('bc', [headline('b1', 'claimed there', '2026-09-30T10:00:00Z')]), [])
})

test('without localStorage a proposal still toasts once', async () => {
  Object.assign(globalThis, { localStorage: { getItem: () => { throw new Error('denied') }, setItem: () => { throw new Error('denied') } } })
  resetDoctrineInbox()
  const read = async () => ({ pending: 1, items: [headline('m1', 'Once', '2026-09-30T10:00:00Z')] })
  assert.equal(await pollDoctrineInbox('mem', read), 'Doctrine change proposed: Once')
  assert.equal(await pollDoctrineInbox('mem', read), null)
  assert.deepEqual(readSeen('mem'), ['m1'])
})
