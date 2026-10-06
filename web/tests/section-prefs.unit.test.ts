// SPDX-License-Identifier: AGPL-3.0-only
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { reactive } from 'vue'
const mocks = vi.hoisted(() => ({ api: vi.fn(), session: null as null | { identity: { tenant: { id: string }; principal: { id: string } } | null } }))
vi.mock('../src/lib/api', () => ({ api: mocks.api }))
vi.mock('../src/stores/session', () => ({ useSession: () => mocks.session }))
import { cacheKey, DEFAULT_SECTIONS, LEGACY_DIAL_KEY, readSections, SECTIONS_KEY } from '../src/lib/sectionPrefs'
import { useSectionPrefs } from '../src/stores/sectionPrefs'

const flush = () => new Promise<void>(resolve => setImmediate(resolve))
const deferred = <T>() => { let resolve!: (value: T) => void; const promise = new Promise<T>(done => { resolve = done }); return { promise, resolve } }
const json = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status, headers: { 'Content-Type': 'application/json' } })
const store = new Map<string, string>()
const puts = () => mocks.api.mock.calls.filter(([, init]) => init?.method === 'PUT').map(([path, init]) => [path, JSON.parse(init.body).value])

beforeEach(() => {
  store.clear()
  vi.stubGlobal('localStorage', { getItem: (k: string) => store.get(k) ?? null, setItem: (k: string, v: string) => { store.set(k, v) } })
  mocks.session = reactive({ identity: { tenant: { id: 't' }, principal: { id: 'p' } } })
  mocks.api.mockReset()
  setActivePinia(createPinia())
})
afterEach(() => vi.unstubAllGlobals())

describe('per-person section folds', () => {
  it('first visit: dial open, Accounts folded, Sessions and Queued open', async () => {
    mocks.api.mockImplementation(async () => json({ value: null }))
    const prefs = useSectionPrefs(); await flush()
    expect(prefs.open).toEqual({ dial: true, accounts: false, sessions: true, queued: true })
    expect(DEFAULT_SECTIONS).toEqual(prefs.open)
  })

  it('the server is the truth; the cache paints the first frame; the old dial fold is honoured once', async () => {
    store.set(cacheKey('t:p'), JSON.stringify({ dial: false, accounts: true }))
    const read = deferred<Response>()
    mocks.api.mockImplementation((path: string) => path === `/preferences/${SECTIONS_KEY}` ? read.promise : Promise.resolve(json({ value: { folded: true } })))
    const prefs = useSectionPrefs()
    expect(prefs.open).toMatchObject({ dial: false, accounts: true })
    read.resolve(json({ value: { sessions: false, junk: 1, accounts: 'yes' } })); await flush()
    // No dial entry yet: the legacy agents.working.display { folded: true } decides it.
    expect(mocks.api.mock.calls.map(([path]) => path)).toContain(`/preferences/${LEGACY_DIAL_KEY}`)
    expect(prefs.open).toEqual({ dial: false, accounts: false, sessions: false, queued: true })
    expect(JSON.parse(store.get(cacheKey('t:p'))!)).toEqual(prefs.open)
    expect(readSections({ dial: 'no', queued: false })).toEqual({ queued: false })
  })

  it('a toggle during the first read keeps the choice and never writes defaults over other sections', async () => {
    const read = deferred<Response>()
    mocks.api.mockImplementation((path: string, init?: RequestInit) => init?.method === 'PUT' ? Promise.resolve(json({})) : path === `/preferences/${SECTIONS_KEY}` ? read.promise : Promise.resolve(json({ value: null })))
    const prefs = useSectionPrefs()
    prefs.toggle('dial')
    expect(prefs.open.dial).toBe(false)
    await flush()
    expect(puts()).toEqual([])
    read.resolve(json({ value: { dial: true, accounts: true, sessions: false, queued: true } })); await flush()
    expect(prefs.open).toEqual({ dial: false, accounts: true, sessions: false, queued: true })
    expect(puts()).toEqual([[`/preferences/${SECTIONS_KEY}`, { dial: false, accounts: true, sessions: false, queued: true }]])
  })

  it('serial toggles each send the whole current object, so the last one wins', async () => {
    const first = deferred<Response>()
    let writes = 0
    mocks.api.mockImplementation((_path: string, init?: RequestInit) => init?.method === 'PUT' ? (++writes === 1 ? first.promise : Promise.resolve(json({}))) : Promise.resolve(json({ value: null })))
    const prefs = useSectionPrefs(); await flush()
    prefs.toggle('accounts'); prefs.toggle('sessions'); await flush()
    expect(puts()).toHaveLength(1)
    first.resolve(json({})); await flush()
    expect(puts().map(([, value]) => value)).toEqual([
      { dial: true, accounts: true, sessions: false, queued: true },
      { dial: true, accounts: true, sessions: false, queued: true },
    ])
  })

  it('a failed read is retried before a write; failures are reported, not hidden', async () => {
    let fail = true
    mocks.api.mockImplementation(async (_path: string, init?: RequestInit) => init?.method === 'PUT' ? json({}, 500) : fail ? json({}, 500) : json({ value: { accounts: true } }))
    const prefs = useSectionPrefs(); await flush()
    expect(prefs.open).toEqual(DEFAULT_SECTIONS)
    prefs.toggle('sessions'); await flush()
    expect(puts()).toEqual([])
    expect(prefs.error).toContain('Couldn’t remember')
    fail = false
    prefs.toggle('dial'); await flush()
    expect(puts()).toEqual([[`/preferences/${SECTIONS_KEY}`, { dial: false, accounts: true, sessions: false, queued: true }]])
    expect(prefs.error).toContain('Couldn’t remember')
  })

  it('a new viewer starts from their own state; the old viewer’s queued write is dropped', async () => {
    const read = deferred<Response>()
    mocks.api.mockImplementation((path: string, init?: RequestInit) => init?.method === 'PUT' ? Promise.resolve(json({})) : path === `/preferences/${SECTIONS_KEY}` ? read.promise : Promise.resolve(json({ value: null })))
    const prefs = useSectionPrefs()
    prefs.toggle('accounts')
    store.set(cacheKey('t:q'), JSON.stringify({ sessions: false }))
    mocks.session!.identity = { tenant: { id: 't' }, principal: { id: 'q' } }
    expect(prefs.open).toEqual({ dial: true, accounts: false, sessions: false, queued: true })
    read.resolve(json({ value: null })); await flush()
    expect(puts()).toEqual([])
    expect(prefs.error).toBe('')
  })
})
