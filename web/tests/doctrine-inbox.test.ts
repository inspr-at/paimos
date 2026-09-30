// SPDX-License-Identifier: AGPL-3.0-only
import assert from 'node:assert/strict'
import { test } from 'node:test'
import { inboxLabel, proposalState } from '../src/lib/doctrine.ts'
import { readSeen, toastText, unseen } from '../src/lib/doctrineInbox.ts'

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
