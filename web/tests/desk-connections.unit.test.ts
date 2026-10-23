// SPDX-License-Identifier: AGPL-3.0-only
// Risk: separate surfaces disagree or fetch separately; a callback or stale
// response crosses people; an ended grant leaves cached items; notices repeat.
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { createPinia, disposePinia, setActivePinia, type Pinia } from 'pinia'
import { createSSRApp, nextTick, reactive, type Component } from 'vue'
import { renderToString } from '@vue/server-renderer'
import { createMemoryHistory, createRouter } from 'vue-router'
import { useDeskConnections } from '../src/stores/deskConnections'
import { useDecisionDesk } from '../src/stores/decisionDesk'
import { readInstances, readSibling, revokeSibling, ConnectionEnded, type ElsewhereSummary } from '../src/lib/deskElsewhere'
import { loadDeskProjection } from '../src/lib/decisionDesk'
import { titlePrefix } from '../src/lib/deskTab'
import DeskPreview from '../src/components/header/DeskPreview.vue'
import GoalWaiting from '../src/components/goals/GoalWaiting.vue'
import DeskInstanceItems from '../src/components/DeskInstanceItems.vue'
import { exchangeInstance, clearPersonConnections } from '../src/lib/instanceConnect'

vi.mock('../src/stores/session', () => ({ useSession: () => session }))
const language = vi.hoisted(() => ({ value: 'en' }))
vi.mock('../src/lib/displayLanguage', () => ({ displayLanguage: () => language.value }))
vi.mock('../src/lib/deskElsewhere', async original => ({ ...await original<object>(), readInstances: vi.fn(), readSibling: vi.fn(), revokeSibling: vi.fn() }))
vi.mock('../src/lib/instanceConnect', async original => ({ ...await original<object>(), exchangeInstance: vi.fn() }))
vi.mock('../src/lib/decisionDesk', async original => ({ ...await original<object>(), loadDeskProjection: vi.fn() }))
vi.mock('../src/lib/phoneApprovals', () => ({ deviceSubscriptionHash: async () => '', phoneSettings: vi.fn() }))
vi.mock('../src/lib/agents', async original => ({ ...await original<object>(), onProjectionWake: () => () => {} }))
vi.mock('../src/stores/projects', () => ({ useProjects: () => ({ byId: () => undefined, load: async () => {} }) }))

const person = (id: string) => ({ tenant: { id: 'tenant' }, principal: { id, kind: 'person' } })
const session = reactive({ identity: null as ReturnType<typeof person> | null, authenticationCurrent: () => true })
const sibling = { tag: 'pma', origin: 'https://pma.example' }
const token = `desk_${'a'.repeat(8)}-${'a'.repeat(4)}-${'a'.repeat(4)}-${'a'.repeat(4)}-${'a'.repeat(12)}_${'b'.repeat(8)}-${'b'.repeat(4)}-${'b'.repeat(4)}-${'b'.repeat(4)}-${'b'.repeat(12)}_${'c'.repeat(43)}`
const grant = () => ({ token, origin: sibling.origin, account: 'Other account', createdAt: new Date().toISOString(), expiresAt: new Date(Date.now() + 90 * 86400_000).toISOString() })
const prefix = 'aeon.desk-connections.', grantKey = `${prefix}tenant:A.pma`
const summary = (id = 'first', open = 1): ElsewhereSummary => ({ open, held: 0, upcoming: '', asOf: new Date().toISOString(), lines: open ? [{ id, kind: 'question', title: `Waiting ${id}`, projectKey: 'PRJ', askedBy: 'Agent', held: false, createdAt: new Date().toISOString(), url: `${sibling.origin}/decision-desk?item=q:${id}` }] : [] })
let pinia: Pinia
const storage = new Map<string, string>()
let opened: ReturnType<typeof vi.fn>, notices: { title: string; options?: NotificationOptions; onclick?: () => void; close: ReturnType<typeof vi.fn> }[]
beforeEach(() => {
  storage.clear(); notices = []; session.identity = null; language.value = 'en'
  vi.stubGlobal('localStorage', { getItem: (key: string) => storage.get(key) ?? null, setItem: (key: string, value: string) => storage.set(key, value), removeItem: (key: string) => storage.delete(key), get length() { return storage.size }, key: (n: number) => [...storage.keys()][n] ?? null })
  opened = vi.fn()
  vi.stubGlobal('window', Object.assign(new EventTarget(), { open: opened }))
  vi.stubGlobal('location', { origin: 'https://ppm.example', pathname: '/decision-desk', search: '' })
  vi.stubGlobal('navigator', {})
  vi.stubGlobal('Notification', class {
    static permission = 'granted'
    close = vi.fn()
    constructor(public title: string, public options?: NotificationOptions) { notices.push(this) }
  })
  pinia = createPinia(); setActivePinia(pinia)
  vi.mocked(readInstances).mockResolvedValue({ here: 'ppm', siblings: [sibling] })
  vi.mocked(readSibling).mockImplementation(async () => summary())
  vi.mocked(revokeSibling).mockResolvedValue()
  vi.mocked(exchangeInstance).mockImplementation(async () => grant())
  vi.mocked(loadDeskProjection).mockResolvedValue({ items: [], counts: { open: 2, held: 0, chores: 0 }, has_more: false, as_of: new Date().toISOString() })
})
afterEach(() => { disposePinia(pinia); vi.clearAllMocks(); vi.unstubAllGlobals(); vi.useRealTimers() })
async function settle() { for (let n = 0; n < 15; n++) { await Promise.resolve(); await nextTick() } }
async function start(connected = true) {
  if (connected) storage.set(grantKey, JSON.stringify(grant()))
  session.identity = person('A')
  const store = useDeskConnections()
  await settle()
  return store
}
async function html(component: Component, props: Record<string, unknown>) {
  const app = createSSRApp(component, props); app.use(pinia)
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/:pathMatch(.*)*', component: { template: '<div />' } }] })
  app.use(router); await router.push('/'); await router.isReady()
  return renderToString(app)
}

it('connects with one browser-bound single-use state and accepts a credential only in the configured sibling callback', async () => {
  const store = await start(false)
  expect(store.reads[0].state).toBe('not connected')
  expect(readSibling).not.toHaveBeenCalled()
  await store.connect('pma')
  const address = new URL(opened.mock.calls[0][0]), state = address.searchParams.get('state')!
  expect(address.origin + address.pathname).toBe(`${sibling.origin}/instances/connect`)
  expect(address.searchParams.get('origin')).toBe('https://ppm.example')
  expect(state).toMatch(/^[A-Za-z0-9_-]{43}$/)
  const pending = JSON.parse(storage.get(`${prefix}pending.${state}`)!)
  expect(pending.verifier).toMatch(/^[A-Za-z0-9_-]{43}$/)
  const digest = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(pending.verifier))
  expect(address.searchParams.get('code_challenge')).toBe(btoa(String.fromCharCode(...new Uint8Array(digest))).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, ''))
  expect(address.href).not.toContain(pending.verifier)
  expect(store.reads[0].state).toBe('connecting')
  const value = grant(), fragment = new URLSearchParams({ state, code: value.token.replace(/^desk_/, 'code_'), instance: 'pma' }).toString()
  expect(await store.acceptFragment(fragment)).toBe('/decision-desk')
  expect(exchangeInstance).toHaveBeenCalledWith(expect.objectContaining(sibling), value.token.replace(/^desk_/, 'code_'), state, pending.verifier, expect.any(AbortSignal))
  expect(store.reads[0]).toMatchObject({ state: 'ok', account: 'Other account', summary: { open: 1 } })
  expect(await store.acceptFragment(fragment)).toBeNull()
  expect(await html(DeskInstanceItems, { read: store.reads[0] })).not.toContain(token)
  await store.connect('pma'); store.cancel('pma')
  expect(store.reads[0].state).toBe('ok')
})

it('refuses unconfigured, expired and other-person callback states without adopting a credential', async () => {
  const store = await start(false)
  const params = { code: token.replace(/^desk_/, 'code_'), instance: 'pma', account: 'Someone', created_at: grant().createdAt, expires_at: grant().expiresAt }
  expect(await store.acceptFragment(new URLSearchParams({ ...params, state: 'z'.repeat(43) }).toString())).toBeNull()
  for (const override of [{ owner: 'tenant:B' }, { until: Date.now() - 1 }, { origin: 'https://evil.example' }]) {
    const state = 's'.repeat(43)
    storage.set(`${prefix}pending.${state}`, JSON.stringify({ owner: 'tenant:A', until: Date.now() + 1000, origin: sibling.origin, tag: 'pma', verifier: 'v'.repeat(43), returnTo: '/decision-desk', ...override }))
    expect(await store.acceptFragment(new URLSearchParams({ ...params, state }).toString())).toBeNull()
    expect(storage.has(`${prefix}pending.${state}`)).toBe(false)
  }
  expect(store.reads[0].state).toBe('not connected')
  expect(storage.has(grantKey)).toBe(false)
})

it('shares one read, consolidated counts and home links across Goals, preview and tab title', async () => {
  const store = await start()
  const desk = useDecisionDesk(); await settle()
  expect(useDeskConnections()).toBe(store)
  await Promise.all([desk.refresh(), store.refresh(), store.refresh()])
  expect(readSibling).toHaveBeenCalledTimes(1)
  expect(desk.hereCount).toBe(2); expect(desk.count).toBe(3)
  expect(desk.countTip).toBe('3 wait for you · 2 here · 1 on pma')
  expect(titlePrefix(desk.count)).toBe('(3) ')
  expect(await html(DeskPreview, {})).toContain('3 things for you')
  const goal = await html(GoalWaiting, { goals: [] })
  expect(goal).toContain('3 open'); expect(goal).toContain('Show')
  expect(goal).not.toContain('Nothing waits on you')
  const preview = await html(DeskPreview, {})
  expect(preview).toContain('https://pma.example/decision-desk?item=q:first')
  expect(preview).toContain('Open pma Desk')
  expect(preview).not.toContain(token)
  const all = summary('all', 6)
  all.lines = Array.from({ length: 6 }, (_, n) => ({ ...all.lines[0], id: `all-${n}`, url: `${sibling.origin}/decision-desk?item=q:all-${n}` }))
  vi.mocked(readSibling).mockResolvedValueOnce(all)
  await store.showAll('pma')
  expect(vi.mocked(readSibling).mock.calls.at(-1)?.[3]).toBe(100)
  expect((await html(DeskInstanceItems, { read: store.reads[0] })).match(/class="instance-item"/g)).toHaveLength(6)
  await store.disconnect('pma'); await settle()
  expect(desk.count).toBe(2)
})

it('retains dated unreachable results, clears ended results and drops late reads after disconnect and person changes', async () => {
  const store = await start()
  vi.mocked(readSibling).mockRejectedValueOnce(new Error('offline'))
  await store.refresh(true)
  expect(store.reads[0]).toMatchObject({ state: 'unreachable', summary: { open: 1 } })
  expect(store.complete).toBe(false)
  const offline = await html(DeskInstanceItems, { read: store.reads[0] })
  expect(offline).toContain('showing'); expect(offline).toContain('Waiting first')
  vi.mocked(readSibling).mockRejectedValueOnce(new ConnectionEnded())
  await store.refresh(true)
  expect(store.reads[0]).toMatchObject({ state: 'ended', summary: null })
  expect(storage.has(grantKey)).toBe(false)
  expect(await html(DeskInstanceItems, { read: store.reads[0] })).not.toContain('Waiting first')
  storage.set(grantKey, JSON.stringify(grant()))
  session.identity = null; await settle(); session.identity = person('A'); await settle()
  let finish!: (summary: ElsewhereSummary) => void
  vi.mocked(readSibling).mockImplementationOnce(() => new Promise(resolve => { finish = resolve }))
  const pending = store.refresh(true)
  await store.disconnect('pma'); finish(summary('late')); await pending
  expect(store.reads[0]).toMatchObject({ state: 'not connected', summary: null })
  storage.set(grantKey, JSON.stringify(grant()))
  session.identity = null; await settle(); session.identity = person('A'); await settle()
  vi.mocked(readSibling).mockImplementationOnce(() => new Promise(resolve => { finish = resolve }))
  const changed = store.refresh(true)
  session.identity = person('B'); await settle(); finish(summary('wrong-person')); await changed
  expect(store.reads[0]).toMatchObject({ state: 'not connected', summary: null })
  expect(store.count).toBe(0)
})

it('notifies each newly observed sibling item once with the existing opt-in and opens its home instance', async () => {
  vi.useFakeTimers({ toFake: ['Date'] })
  vi.mocked(readSibling).mockImplementation(async () => summary('none', 0))
  const store = await start()
  store.noticeEnabled = true
  expect(notices).toHaveLength(0) // The first read establishes the baseline.
  vi.setSystemTime(Date.now() + 1000)
  vi.mocked(readSibling).mockImplementation(async () => summary('new'))
  await store.refresh(true); await store.refresh(true)
  expect(notices).toHaveLength(1)
  expect(notices[0].title).toBe('New on pma: Waiting new')
  expect(notices[0].options?.body).toBe('PRJ · decide on pma')
  notices[0].onclick?.()
  expect(opened).toHaveBeenCalledWith('https://pma.example/decision-desk?item=q:new', '_blank', 'noopener,noreferrer')
  store.setNotify('pma', false)
  vi.mocked(readSibling).mockImplementation(async () => summary('instance-opted-out'))
  await store.refresh(true)
  expect(notices).toHaveLength(1)
  store.setNotify('pma', true)
  store.noticeEnabled = false
  vi.mocked(readSibling).mockImplementation(async () => summary('opted-out'))
  await store.refresh(true)
  expect(notices).toHaveLength(1)
  store.noticeEnabled = true; await store.refresh(true)
  expect(notices).toHaveLength(1)
  // Reload/reconnect retains the durable item claims.
  session.identity = null; await settle(); session.identity = person('A'); await settle()
  store.noticeEnabled = true
  vi.mocked(readSibling).mockImplementation(async () => summary('new'))
  await store.refresh(true)
  expect(notices).toHaveLength(1)
})

it('blocks unknown or unread grants but excludes disconnected siblings from all-clear', async () => {
  vi.mocked(readInstances).mockRejectedValueOnce(new Error('offline'))
  const store = await start(false)
  expect(store.discovery).toBe('unknown'); expect(store.allClear).toBe(false)
  await store.refresh()
  expect(store.discovery).toBe('known'); expect(store.reads[0].state).toBe('not connected'); expect(store.allClear).toBe(true)
  await store.connect('pma')
  expect(store.reads[0].state).toBe('connecting'); expect(store.allClear).toBe(true)
  store.cancel('pma')
  storage.set(grantKey, JSON.stringify(grant()))
  session.identity = null; await settle()
  let finish!: (value: ElsewhereSummary) => void
  vi.mocked(readSibling).mockImplementationOnce(() => new Promise(resolve => { finish = resolve }))
  session.identity = person('A'); await settle()
  expect(store.reads[0].state).toBe('reading'); expect(store.allClear).toBe(false)
  finish(summary('empty', 0)); await settle()
  expect(store.allClear).toBe(true)
  await store.connect('pma')
  expect(store.allClear).toBe(false) // Replacing an existing grant still needs a successful read.
  store.cancel('pma'); expect(store.allClear).toBe(true)
  vi.mocked(readSibling).mockRejectedValueOnce(new Error('offline'))
  await store.refresh(true); expect(store.allClear).toBe(false)
  vi.mocked(readSibling).mockRejectedValueOnce(new ConnectionEnded())
  await store.refresh(true)
  expect(store.reads[0].state).toBe('ended'); expect(store.allClear).toBe(true)
  session.identity = null; await settle()
  vi.mocked(readInstances).mockResolvedValue({ here: 'ppm', siblings: [] })
  session.identity = person('A'); await settle()
  expect(store.allClear).toBe(true)
})

it('keeps the checked age and its seconds unit together', async () => {
  const store = await start()
  expect(store.syncTooltip(store.reads[0].lastReadAt + 12_000)).toBe('pma: checked 12\u00a0s ago')
  language.value = 'de'
  expect(store.syncTooltip(store.reads[0].lastReadAt + 12_000)).toBe('pma: vor 12\u00a0s geprüft')
})

// Risk: promoting an old item into the top five is mistaken for its creation.
it('notifies only items created after the previous source read, never existing items entering the top five', async () => {
  vi.useFakeTimers({ toFake: ['Date'] })
  const store = await start()
  store.noticeEnabled = true
  const before = Date.now()
  vi.setSystemTime(before + 1000)
  const old = summary('old-entering-top-five')
  old.lines[0].createdAt = new Date(before - 1000).toISOString()
  vi.mocked(readSibling).mockResolvedValue(old)
  await store.refresh(true)
  expect(notices).toHaveLength(0)
  vi.setSystemTime(before + 2000)
  vi.mocked(readSibling).mockResolvedValue(summary('created-after-last-read'))
  await store.refresh(true)
  expect(notices).toHaveLength(1)
  expect(notices[0].title).toContain('created-after-last-read')
})

it('revokes at home before disconnect or replacement and keeps a failed disconnect retryable', async () => {
  const store = await start()
  vi.mocked(revokeSibling).mockRejectedValueOnce(new Error('offline'))
  await expect(store.disconnect('pma')).rejects.toThrow('offline')
  expect(storage.has(grantKey)).toBe(true)
  expect(store.reads[0].state).toBe('ok')
  await store.connect('pma')
  const address = new URL(opened.mock.calls.at(-1)![0]), state = address.searchParams.get('state')!
  let ended = false
  vi.mocked(revokeSibling).mockImplementationOnce(async () => { ended = true })
  vi.mocked(exchangeInstance).mockImplementationOnce(async () => { expect(ended).toBe(true); return grant() })
  expect(await store.acceptFragment(new URLSearchParams({ state, code: token.replace(/^desk_/, 'code_'), instance: 'pma' }).toString())).toBe('/decision-desk')
  expect(await store.disconnect('pma')).toBe(true)
  expect(storage.has(grantKey)).toBe(false)
  expect(store.reads[0].state).toBe('not connected')
})

it('clears only the departing person storage and in-memory grants on explicit sign-out', async () => {
  const store = await start()
  await store.connect('pma')
  storage.set(`${prefix}tenant:B.pma`, JSON.stringify(grant()))
  clearPersonConnections('tenant:A')
  expect(store.count).toBe(0)
  expect(storage.has(grantKey)).toBe(false)
  expect([...storage.keys()].some(key => key.startsWith(`${prefix}pending.`))).toBe(false)
  expect(storage.has(`${prefix}tenant:B.pma`)).toBe(true)
})

it('uses plural-aware waiting copy and impersonal German across Goals and the header', async () => {
  const store = await start(), desk = useDecisionDesk(); await settle()
  expect(await html(DeskInstanceItems, { read: store.reads[0], variant: 'goals' })).toContain('1 waits for you')
  vi.mocked(loadDeskProjection).mockResolvedValue({ items: [], counts: { open: 0, held: 0, chores: 0 }, has_more: false, as_of: new Date().toISOString() })
  await desk.refresh()
  expect(desk.countTip).toBe('1 waits for you · 0 here · 1 on pma')
  language.value = 'de'
  // A reactive read re-evaluates the labels after the language preference changes.
  await store.refresh(true); await desk.refresh()
  expect(desk.countTip).toBe('1 offen · 0 hier · 1 auf pma')
  expect(await html(DeskInstanceItems, { read: store.reads[0], variant: 'goals' })).toContain('1 offen')
})

it('upgrades the legacy notice marker without notifying existing backlog', async () => {
  vi.useFakeTimers({ toFake: ['Date'] })
  const store = await start(), before = Date.now()
  store.noticeEnabled = true
  storage.set(`${grantKey}.seen`, '1')
  vi.setSystemTime(before + 1000)
  const backlog = summary('backlog-after-connection')
  backlog.lines[0].createdAt = new Date(before + 500).toISOString()
  vi.mocked(readSibling).mockResolvedValue(backlog)
  await store.refresh(true)
  expect(notices).toHaveLength(0)
  expect(Number(storage.get(`${grantKey}.seen`))).toBeGreaterThan(1)
})
