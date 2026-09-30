// SPDX-License-Identifier: AGPL-3.0-only
import assert from 'node:assert/strict'
import { test } from 'node:test'
import { inboxLabel, proposalState, type DoctrineInboxHeadline } from '../src/lib/doctrine.ts'
import { doctrineInbox, inboxChanged, pollDoctrineInbox, resetDoctrineInbox, toastText, unclaimed } from '../src/lib/doctrineInbox.ts'

type Summary = { pending: number; items: DoctrineInboxHeadline[] }
const headline = (id: string, label: string, at: string, notified = false): DoctrineInboxHeadline => ({ id, label, created_at: at, notified })

// The server's claim: the first caller per proposal wins, whichever tab asks.
function server() {
  const claimed: string[] = []
  const claim = async (id: string) => {
    await new Promise(resolve => setTimeout(resolve, 1))
    if (claimed.includes(id)) return false
    claimed.push(id)
    return true
  }
  return { claimed, claim }
}

// A second, independent tab: its own module instance, its own state.
async function otherTab(name: string): Promise<typeof import('../src/lib/doctrineInbox.ts')> {
  return await import(new URL(`../src/lib/doctrineInbox.ts?tab=${name}`, import.meta.url).href) as typeof import('../src/lib/doctrineInbox.ts')
}

test('a toast names one new proposal, or counts several', () => {
  assert.equal(toastText([]), null)
  assert.equal(toastText([headline('a', 'Estimate before work.', '2026-09-30T10:00:00Z')]), 'Doctrine change proposed: Estimate before work')
  assert.equal(toastText([headline('a', 'x', '2026-09-30T10:00:00Z'), headline('b', 'y', '2026-09-30T10:01:00Z')]), '2 doctrine changes proposed')
})

test('only proposals not notified yet are claimed, oldest first', () => {
  const items = [headline('b', 'second', '2026-09-30T10:01:00Z'), headline('a', 'first', '2026-09-30T10:00:00Z'), headline('c', 'told', '2026-09-30T09:00:00Z', true)]
  assert.deepEqual(unclaimed(items).map(item => item.id), ['a', 'b'])
  assert.deepEqual(unclaimed([headline('c', 'told', '2026-09-30T09:00:00Z', true)]), [])
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
  let release!: (value: Summary) => void
  const read = () => new Promise<Summary>(resolve => { release = resolve })
  return { read, release: (value: Summary) => release(value) }
}

test('a late summary never restores a dot or a toast after a person acted', async () => {
  resetDoctrineInbox()
  const api = server()
  const late = deferred()
  const poll = pollDoctrineInbox('p3', late.read, api.claim)
  inboxChanged(0) // the person dismissed the last proposal meanwhile
  late.release({ pending: 1, items: [headline('d1', 'Dismissed meanwhile', '2026-09-30T10:00:00Z')] })
  assert.equal(await poll, null)
  assert.equal(doctrineInbox.pending, 0)
  assert.deepEqual(api.claimed, [])
  // The next read is current and toasts what is new.
  const text = await pollDoctrineInbox('p3', async () => ({ pending: 1, items: [headline('n1', 'New one', '2026-09-30T10:05:00Z')] }), api.claim)
  assert.equal(text, 'Doctrine change proposed: New one')
  assert.equal(doctrineInbox.pending, 1)
})

test('a stale poll after a dismissal claims nothing; the next poll toasts what still waits', async () => {
  resetDoctrineInbox()
  const api = server()
  const stale = deferred()
  const poll = pollDoctrineInbox('p4', stale.read, api.claim)
  inboxChanged(1) // one of two dismissed while the read was in flight
  stale.release({ pending: 2, items: [headline('gone', 'Dismissed', '2026-09-30T10:00:00Z'), headline('left', 'Still waits', '2026-09-30T10:01:00Z')] })
  assert.equal(await poll, null)
  assert.deepEqual(api.claimed, [])
  assert.equal(await pollDoctrineInbox('p4', async () => ({ pending: 1, items: [headline('left', 'Still waits', '2026-09-30T10:01:00Z')] }), api.claim), 'Doctrine change proposed: Still waits')
  assert.deepEqual(api.claimed, ['left'])
})

test('a session change drops the previous principal\'s reads', async () => {
  resetDoctrineInbox()
  const api = server()
  const old = deferred()
  const poll = pollDoctrineInbox('old', old.read, api.claim)
  resetDoctrineInbox()
  old.release({ pending: 2, items: [headline('o1', 'Old', '2026-09-30T10:00:00Z')] })
  assert.equal(await poll, null)
  assert.equal(doctrineInbox.principal, '')
  assert.equal(doctrineInbox.pending, 0)
  assert.deepEqual(api.claimed, [])
  assert.equal(await pollDoctrineInbox('new', async () => ({ pending: 1, items: [headline('x1', 'Fresh', '2026-09-30T10:00:00Z')] }), api.claim), 'Doctrine change proposed: Fresh')
  assert.equal(doctrineInbox.principal, 'new')
})

// How many proposals a toast text announces.
const announced = (text: string | null) => text === null ? 0 : text.startsWith('Doctrine change proposed:') ? 1 : Number.parseInt(text, 10)

test('two independent tabs polling at once show one toast', async () => {
  resetDoctrineInbox()
  const tab = await otherTab('two')
  assert.notEqual(tab.doctrineInbox, doctrineInbox)
  const api = server()
  const one = async () => ({ pending: 1, items: [headline('t1', 'one', '2026-09-30T10:00:00Z')] })
  const texts = await Promise.all([pollDoctrineInbox('tabs', one, api.claim), tab.pollDoctrineInbox('tabs', one, api.claim)])
  assert.equal(texts.filter(Boolean).length, 1)
  assert.deepEqual(texts.filter(Boolean), ['Doctrine change proposed: one'])
  // Several proposals: each is announced in exactly one tab, and never again.
  const two = async () => ({ pending: 3, items: [headline('t1', 'one', '2026-09-30T10:00:00Z'), headline('t2', 'two', '2026-09-30T10:01:00Z'), headline('t3', 'three', '2026-09-30T10:02:00Z')] })
  const more = await Promise.all([pollDoctrineInbox('tabs', two, api.claim), tab.pollDoctrineInbox('tabs', two, api.claim)])
  assert.equal(announced(more[0]!) + announced(more[1]!), 2)
  assert.deepEqual([...api.claimed].sort(), ['t1', 't2', 't3'])
  assert.equal(await pollDoctrineInbox('tabs', two, api.claim), null)
  assert.equal(await tab.pollDoctrineInbox('tabs', two, api.claim), null)
})

test('with storage disabled a proposal still toasts once', async () => {
  const saved = Object.getOwnPropertyDescriptor(globalThis, 'localStorage')
  Object.defineProperty(globalThis, 'localStorage', { configurable: true, get: () => { throw new Error('denied') } })
  try {
    resetDoctrineInbox()
    const tab = await otherTab('storage')
    const api = server()
    const read = async () => ({ pending: 1, items: [headline('m1', 'Once', '2026-09-30T10:00:00Z')] })
    assert.equal(await pollDoctrineInbox('mem', read, api.claim), 'Doctrine change proposed: Once')
    assert.equal(await pollDoctrineInbox('mem', read, api.claim), null)
    assert.equal(await tab.pollDoctrineInbox('mem', read, api.claim), null)
  } finally {
    if (saved) Object.defineProperty(globalThis, 'localStorage', saved)
    else delete (globalThis as { localStorage?: unknown }).localStorage
  }
})

test('a failed claim leaves the rest for the next poll', async () => {
  resetDoctrineInbox()
  const api = server()
  let fail = true
  const flaky = async (id: string) => {
    if (id === 'f2' && fail) throw new Error('offline')
    return api.claim(id)
  }
  const read = async () => ({ pending: 2, items: [headline('f1', 'first', '2026-09-30T10:00:00Z'), headline('f2', 'second', '2026-09-30T10:01:00Z')] })
  assert.equal(await pollDoctrineInbox('flaky', read, flaky), 'Doctrine change proposed: first')
  fail = false
  assert.equal(await pollDoctrineInbox('flaky', read, flaky), 'Doctrine change proposed: second')
})
