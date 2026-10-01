// SPDX-License-Identifier: AGPL-3.0-only
import { effectScope, nextTick, ref } from 'vue'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { clearPermissions, revokePermissions } from '../src/lib/authz'
import { useIdentityScope } from '../src/lib/useIdentityScope'
import { useSession } from '../src/stores/session'
import type { Identity } from '../src/lib/api'
import { AUTH_GENERATION_KEY } from '../src/lib/authTabs'

// The session store reaches for browser theme storage; the scope only reads its identity.
vi.mock('../src/lib/theme', () => ({ restoreTheme: async () => {} }))
const who = (tenant: string, person: string) => ({ tenant: { id: tenant, name: tenant }, principal: { id: person, name: person, kind: 'person', roles: [] } }) as unknown as Identity
function later<T = void>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>(done => { resolve = done })
  return { promise, resolve }
}
beforeEach(() => { setActivePinia(createPinia()) })
afterEach(() => vi.unstubAllGlobals())

it('drops a callback before a cross-tab storage event even if the local identity still looks unchanged', async () => {
  const storage = new Map<string, string>(), events = new EventTarget()
  vi.stubGlobal('window', {
    localStorage: { getItem: (key: string) => storage.get(key) ?? null },
    addEventListener: events.addEventListener.bind(events), removeEventListener: events.removeEventListener.bind(events),
  })
  vi.stubGlobal('BroadcastChannel', undefined)
  const session = useSession()
  session.identity = who('t1', 'ada')
  const scoped = effectScope(), scope = scoped.run(() => useIdentityScope())!
  const answer = later<string>(), shown: string[] = []
  const running = scope.run(({ after }) => after(answer.promise, value => { shown.push(value) }))
  storage.set(AUTH_GENERATION_KEY, 'another-sign-in')
  answer.resolve('ada-only')
  await running
  expect(shown).toEqual([])
  expect(session.identity?.principal.id).toBe('ada')
  events.dispatchEvent(Object.assign(new Event('storage'), { key: AUTH_GENERATION_KEY, newValue: 'another-sign-in' }))
  expect(session.identity).toBeNull()
  expect(session.requiresSignIn).toBe(true)
  scoped.stop(); session.$dispose()
})

// The scope follows the session store: whoever signs in next is a different owner and
// everything asked for the previous person is dropped at that moment, not at the next render.
it('drops what was asked for the previous person the moment the session changes', async () => {
  const session = useSession()
  session.identity = who('t1', 'ada')
  const scoped = effectScope()
  const scope = scoped.run(() => useIdentityScope())!
  const answer = later<string>()
  const reached: string[] = []
  let signal!: AbortSignal
  const running = scope.run(async ({ after, signal: s }) => { signal = s; await after(answer.promise, value => { reached.push(value) }) })
  expect(scope.owner.value).toBe('t1/ada')
  session.identity = who('t1', 'grace')
  expect(signal.aborted).toBe(true)
  answer.resolve('ada-only')
  await running
  expect(reached).toEqual([])
  scoped.stop()
})

it('does not let the same person revive a run by signing out and back in', async () => {
  const session = useSession()
  session.identity = who('t1', 'ada')
  const scoped = effectScope()
  const scope = scoped.run(() => useIdentityScope())!
  const answer = later<string>()
  const reached: string[] = []
  const running = scope.run(async ({ after }) => { await after(answer.promise, value => { reached.push(value) }) })
  session.identity = null
  session.identity = who('t1', 'ada')
  answer.resolve('old')
  await running
  expect(reached).toEqual([])
  scoped.stop()
})

it('a permission reset drops runs, and nothing starts while nobody is signed in', async () => {
  const session = useSession()
  session.identity = who('t1', 'ada')
  const scoped = effectScope()
  const scope = scoped.run(() => useIdentityScope())!
  const a = later<string>(), b = later<string>()
  const reached: string[] = []
  const first = scope.run(async ({ after }) => { await after(a.promise, value => { reached.push(value) }) })
  clearPermissions()
  const second = scope.run(async ({ after }) => { await after(b.promise, value => { reached.push(value) }) })
  revokePermissions()
  a.resolve('a'); b.resolve('b')
  await Promise.all([first, second])
  expect(reached).toEqual([])
  session.identity = null
  let started = false
  await scope.run(async () => { started = true })
  expect(started).toBe(false)
  scoped.stop()
})

it('a requirement that stops holding ends the runs that needed it', async () => {
  const session = useSession()
  session.identity = who('t1', 'ada')
  const allowed = ref(true)
  const scoped = effectScope()
  const scope = scoped.run(() => useIdentityScope(() => allowed.value))!
  const answer = later<string>()
  const reached: string[] = []
  const running = scope.run(async ({ after }) => { await after(answer.promise, value => { reached.push(value) }) })
  allowed.value = false
  await nextTick()
  answer.resolve('late')
  await running
  expect(reached).toEqual([])
  expect(scope.owner.value).toBe('')
  scoped.stop()
})

it('stopping the component drops everything in flight', async () => {
  const session = useSession()
  session.identity = who('t1', 'ada')
  const scoped = effectScope()
  const scope = scoped.run(() => useIdentityScope())!
  const answer = later<string>()
  const reached: string[] = []
  const running = scope.run(async ({ after }) => { await after(answer.promise, value => { reached.push(value) }) })
  scoped.stop()
  answer.resolve('late')
  await running
  expect(reached).toEqual([])
})
