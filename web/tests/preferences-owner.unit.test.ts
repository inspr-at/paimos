// SPDX-License-Identifier: AGPL-3.0-only
import { afterEach, expect, it, vi } from 'vitest'
import * as API from '../src/lib/api'
import { sourceModule } from './record-source'
const Preferences = sourceModule<typeof import('../src/lib/preferences')>('lib/preferences.ts', { './api.ts': API })
import { deferred, flush } from './record-source'

const who = (person: string) => ({ tenant: { id: 't' }, principal: { id: person } })
afterEach(() => { Preferences.setPreferenceOwner?.(null); vi.useRealTimers(); vi.unstubAllGlobals() })
it('S9-001: delayed A reads and queued writes cannot be adopted or sent as B', async () => {
  vi.useFakeTimers()
  const read = deferred<Response>(), sent: string[] = []
  let person = 'A'
  vi.stubGlobal('fetch', vi.fn((_: unknown, init?: RequestInit) => {
    if (init?.method === 'PUT') { sent.push(person); return Promise.resolve(new Response('{}')) }
    return person === 'A' ? read.promise : Promise.resolve(new Response(JSON.stringify({ value: { person: 'B' } })))
  }))
  Preferences.setPreferenceOwner?.(who('A'))
  const a = Preferences.usePreference<{ person: string }>('same')
  a.save({ person: 'A-draft' }, 400)
  person = 'B'; Preferences.setPreferenceOwner?.(who('B'))
  const b = Preferences.usePreference<{ person: string }>('same')
  read.resolve(new Response(JSON.stringify({ value: { person: 'A-private' } }))); await b.ready; await flush()
  await vi.runAllTimersAsync()
  expect(b.value.value).toEqual({ person: 'B' })
  expect(sent).toEqual([])
})
it('S9-001: a queued tail behind A\'s in-flight write is cancelled on sign-out', async () => {
  const answer = deferred<Response>(), calls: string[] = [], messages: (() => void)[] = []
  vi.stubGlobal('MessageChannel', class {
    port1 = { onmessage: (() => {}) as () => void, close() {} }
    port2 = { close() {}, postMessage: () => messages.push(() => this.port1.onmessage()) }
  })
  vi.stubGlobal('fetch', vi.fn((_: unknown, init?: RequestInit) => { if (init?.method !== 'PUT') return Promise.resolve(new Response('{}')); calls.push(String(init.body)); return answer.promise }))
  Preferences.setPreferenceOwner?.(who('A'))
  const pref = Preferences.usePreference<{ n: number }>('queued')
  pref.save({ n: 1 }, 0); messages.splice(0).forEach(run => run()); await flush()
  expect(calls).toHaveLength(1)
  pref.save({ n: 2 }, 0); messages.splice(0).forEach(run => run()); await flush()
  Preferences.setPreferenceOwner?.(null); Preferences.setPreferenceOwner?.(who('B'))
  answer.resolve(new Response('{}')); await flush()
  expect(calls).toHaveLength(1)
})

it('AEON-1213: shared preference reads retry failures, coalesce recovery and accept an empty value', async () => {
  const recovery = deferred<Response>()
  const fetch = vi.fn()
    .mockResolvedValueOnce(new Response('{}', { status: 503 }))
    .mockImplementationOnce(() => recovery.promise)
    .mockResolvedValueOnce(new Response(JSON.stringify({ value: null })))
  vi.stubGlobal('fetch', fetch)
  Preferences.setPreferenceOwner(who('A'))
  const pref = Preferences.usePreference<{ handle?: number; folded_projects?: string[] }>('agents:layout')
  await pref.ready
  expect(pref.value.value).toBeNull()
  const first = pref.ensureLoaded(), second = pref.ensureLoaded()
  await flush()
  expect(fetch).toHaveBeenCalledTimes(2)
  expect(pref.value.value).toBeNull()
  recovery.resolve(new Response(JSON.stringify({ value: { handle: 420, folded_projects: ['retained'] } })))
  expect(await first).toBe(true); expect(await second).toBe(true)
  expect(pref.value.value).toEqual({ handle: 420, folded_projects: ['retained'] })
  expect(await pref.ensureLoaded()).toBe(true)
  expect(fetch).toHaveBeenCalledTimes(2)

  Preferences.setPreferenceOwner(who('B'))
  const empty = Preferences.usePreference('agents:layout')
  expect(await empty.ensureLoaded()).toBe(true)
  expect(empty.value.value).toBeNull()
  expect(fetch).toHaveBeenCalledTimes(3)
})
