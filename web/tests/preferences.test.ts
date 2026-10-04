// SPDX-License-Identifier: AGPL-3.0-only
import { describe, test } from 'node:test'
import assert from 'node:assert/strict'
import { setPreferenceOwner, usePreference } from '../src/lib/preferences.ts'

type Body = Record<string, unknown>
interface Pending { key: string; value: Body; release: () => void }

const stored = new Map<string, Body>()
const inflight: Pending[] = []

function install() {
  setPreferenceOwner({ tenant: { id: 'preference-test' }, principal: { id: 'person' } })
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    const key = decodeURIComponent(String(input).split('/').pop() ?? '')
    if (init?.method !== 'PUT') return new Response(JSON.stringify({ value: stored.get(key) ?? null }), { status: 200 })
    const value = JSON.parse(String(init.body)).value as Body
    return new Promise<Response>(resolve => {
      inflight.push({
        key,
        value,
        release: () => {
          stored.set(key, value)
          resolve(new Response(JSON.stringify({ value }), { status: 200 }))
        },
      })
    })
  }) as typeof fetch
}

// Message delivery is not ordered across channels, so drain the loop before asserting.
async function flush() {
  for (let i = 0; i < 8; i++) await new Promise(resolve => setImmediate(resolve))
}
const sent = (key: string) => inflight.filter(item => item.key === key)

describe('preference writes', { concurrency: 1 }, () => {
  test('a later save waits for the in-flight write and is what the server keeps', async () => {
    install()
    const pref = usePreference<Body>('groups-reorder')
    pref.save({ place: { p: 'moved' } }, 0)
    await flush()
    assert.equal(sent('groups-reorder').length, 1)
    assert.deepEqual(sent('groups-reorder')[0]!.value, { place: { p: 'moved' } })
    // Undo while the move response is still held. The undo must not be sent yet,
    // or a late move response could be applied after it.
    pref.save({ place: {} }, 0)
    await flush()
    assert.equal(sent('groups-reorder').length, 1)
    sent('groups-reorder')[0]!.release()
    await flush()
    const writes = sent('groups-reorder')
    assert.equal(writes.length, 2)
    assert.deepEqual(writes[1]!.value, { place: {} })
    assert.deepEqual(stored.get('groups-reorder'), { place: { p: 'moved' } })
    writes[1]!.release()
    await flush()
    assert.deepEqual(stored.get('groups-reorder'), { place: {} })
  })

  test('two saves in one turn send the latest value once', async () => {
    install()
    const pref = usePreference<Body>('groups-coalesce')
    pref.save({ place: { p: 'a' } }, 0)
    pref.save({ place: { p: 'b' } }, 0)
    await flush()
    assert.equal(sent('groups-coalesce').length, 1)
    assert.deepEqual(sent('groups-coalesce')[0]!.value, { place: { p: 'b' } })
    sent('groups-coalesce')[0]!.release()
    await flush()
    assert.deepEqual(stored.get('groups-coalesce'), { place: { p: 'b' } })
  })

  test('different keys are written at the same time', async () => {
    install()
    const first = usePreference<Body>('groups-a')
    const second = usePreference<Body>('groups-b')
    first.save({ place: { p: '1' } }, 0)
    second.save({ place: { q: '2' } }, 0)
    await flush()
    assert.equal(sent('groups-a').length, 1)
    assert.equal(sent('groups-b').length, 1)
    sent('groups-a')[0]!.release()
    sent('groups-b')[0]!.release()
  })
})
