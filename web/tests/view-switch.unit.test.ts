// SPDX-License-Identifier: AGPL-3.0-only
// Risk (AEON-1453): a view switch opens on the wrong view. It forgets the view used last,
// lets the view used last beat an explicit default, lets a link change what is remembered,
// mixes one project's memory into another's, overwrites a memory it could not read, or
// keeps anything in browser storage. The menu must open on hold and Shift+F10 without the
// hold also switching. Writes are awaited through a barrier on the PUT, never a sleep.
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { effectScope, nextTick, ref, type EffectScope } from 'vue'
import { preferenceOwner, setPreferenceOwner } from '../src/lib/preferences'
import { useViewSwitch, viewKey, type ViewSwitchOptions } from '../src/lib/viewSwitch'
import { useViewMenuTrigger } from '../src/lib/viewMenuTrigger'
import { flush } from './record-source'

const store = new Map<string, unknown>()
const puts: { key: string; value: unknown }[] = []
const waiting: (() => void)[] = []
let failReads = false
const scopes: EffectScope[] = []
const who = (person: string) => ({ tenant: { id: 't' }, principal: { id: person } })
const forbidden = new Proxy({}, { get() { throw new Error('a view switch must never use browser storage') } })
beforeEach(() => {
  store.clear(); puts.splice(0); waiting.splice(0); failReads = false
  vi.stubGlobal('localStorage', forbidden); vi.stubGlobal('sessionStorage', forbidden)
  vi.stubGlobal('fetch', vi.fn(async (input: string, init?: RequestInit) => {
    const key = decodeURIComponent(String(input).split('/api/preferences/')[1] ?? '')
    if (init?.method === 'PUT') {
      const { value } = JSON.parse(String(init.body)) as { value: unknown }
      store.set(key, value); puts.push({ key, value }); waiting.splice(0).forEach(done => done())
      return new Response('{}')
    }
    if (failReads) return new Response('{}', { status: 503 })
    return new Response(JSON.stringify({ key, value: store.get(key) ?? null }))
  }))
  setPreferenceOwner(who('A'))
})
afterEach(() => { scopes.splice(0).forEach(scope => scope.stop()); setPreferenceOwner(null); vi.unstubAllGlobals() })
const run = <T,>(work: () => T): T => { const scope = effectScope(); scopes.push(scope); return scope.run(work)! }
const sw = (config: ViewSwitchOptions) => run(() => useViewSwitch(config))
// Resolves on the next PUT, which arrives one MessageChannel turn after a save.
const written = () => new Promise<void>(resolve => waiting.push(resolve))
// A later visit: a new owner epoch drops every cached value, so the next read is the server's.
function revisit() { setPreferenceOwner(null); setPreferenceOwner(who('A')) }
const projects = { key: viewKey('projects'), options: ['list', 'cards', 'goals'], fallback: 'list' }

it('the view used last opens the next visit, kept on the server only', async () => {
  const first = sw(projects)
  await first.ready()
  expect(first.current.value).toBe('list')
  const put = written(); first.choose('cards')
  expect(first.current.value).toBe('cards')
  await put
  expect(puts).toEqual([{ key: 'view:projects', value: { last: 'cards' } }])
  revisit()
  const next = sw(projects)
  await next.ready(); await flush()
  expect(next.current.value).toBe('cards')
  expect(next.defaultId.value).toBeNull()
  // A new default is for the next visit: the view on screen does not change under the person.
  const set = written(); next.setDefault('goals'); await set
  expect(next.current.value).toBe('cards')
  expect(next.preferred.value).toBe('goals')
})

it('an explicit default wins over the view used last; stopping it returns to the last used, and the older store never comes back', async () => {
  // What "Open <project> with it" kept before: a saved view of this project.
  store.set('list:p1', { defaultView: 'v1', order: ['key'] })
  const config: ViewSwitchOptions = {
    key: viewKey('tickets', 'p1'), options: ['list', 'outline', 'graph', 'saved:v1'], fallback: 'list',
    legacy: { key: 'list:p1', read: value => ({ default: typeof value?.defaultView === 'string' ? `saved:${value.defaultView}` : null }) },
  }
  const tickets = sw(config)
  await tickets.ready(); await flush()
  expect(tickets.defaultId.value).toBe('saved:v1')
  expect(tickets.preferred.value).toBe('saved:v1')
  let put = written(); tickets.choose('graph'); await put
  // Used last is graph; the explicit default still decides where the next visit opens.
  expect(tickets.preferred.value).toBe('saved:v1')
  put = written(); tickets.setDefault('outline'); await put
  expect(store.get('view:tickets:p1')).toEqual({ last: 'graph', default: 'outline' })
  put = written(); tickets.setDefault(null); await put
  expect(store.get('view:tickets:p1')).toEqual({ last: 'graph', default: null })
  revisit()
  const next = sw(config)
  await next.ready(); await flush()
  expect(next.defaultId.value).toBeNull()
  expect(next.preferred.value).toBe('graph')
  // The older store is read, never rewritten: a rollback still finds it as it was.
  expect(store.get('list:p1')).toEqual({ defaultView: 'v1', order: ['key'] })
  expect(puts.map(write => write.key)).toEqual(['view:tickets:p1', 'view:tickets:p1', 'view:tickets:p1'])
})

it('a link wins once: it shows its view without writing, until the person chooses or follows another link', async () => {
  store.set('view:projects', { last: 'cards', default: null })
  const link = ref<string | null>('goals')
  const projectsSwitch = sw({ ...projects, link })
  await projectsSwitch.ready(); await flush()
  expect(projectsSwitch.current.value).toBe('goals')
  expect(projectsSwitch.preferred.value).toBe('cards')
  const put = written(); projectsSwitch.choose('list')
  // The address still says goals; the person's choice is what shows.
  expect(projectsSwitch.current.value).toBe('list')
  await put
  // The first and only write is the person's choice: the link wrote nothing before it.
  expect(puts).toEqual([{ key: 'view:projects', value: { last: 'list', default: null } }])
  link.value = null; await nextTick()
  link.value = 'goals'; await nextTick()
  expect(projectsSwitch.current.value).toBe('goals')
  expect(store.get('view:projects')).toEqual({ last: 'list', default: null })
})

it('each project keeps its own memory under its own key', async () => {
  store.set('view:tickets:p2', { last: 'graph' })
  const project = ref('p1')
  const tickets = sw({ key: () => viewKey('tickets', project.value), options: ['list', 'outline', 'graph'], fallback: 'list' })
  await tickets.ready()
  const put = written(); tickets.choose('outline'); await put
  project.value = 'p2'; await nextTick()
  await tickets.ready(); await flush()
  expect(tickets.current.value).toBe('graph')
  expect(store.get('view:tickets:p1')).toEqual({ last: 'outline' })
  expect(store.get('view:tickets:p2')).toEqual({ last: 'graph' })
})

it('a memory that could not be read is never overwritten; the next write after a read merges into it', async () => {
  store.set('view:projects', { last: 'cards', default: 'cards' })
  failReads = true
  const projectsSwitch = sw(projects)
  await projectsSwitch.ready()
  expect(projectsSwitch.current.value).toBe('list')
  projectsSwitch.choose('goals')
  expect(projectsSwitch.current.value).toBe('goals')
  await flush()
  failReads = false
  const put = written(); projectsSwitch.setDefault('list'); await put
  // The unread choice wrote nothing; the default merged into what the server held.
  expect(puts).toEqual([{ key: 'view:projects', value: { last: 'cards', default: 'list' } }])
})

it('a hold or Shift+F10 opens the menu; the hold never also switches, movement cancels it, and a mouse never holds', () => {
  const timers: (() => void)[] = []
  const schedule = { set: ((run: () => void) => timers.push(run)) as unknown as typeof setTimeout, clear: vi.fn() as unknown as typeof clearTimeout }
  const opened: string[] = []
  const trigger = run(() => useViewMenuTrigger(id => opened.push(id), schedule))
  const segment = { isConnected: true, contains: (node: unknown) => node === segment } as unknown as HTMLElement
  const pointer = (type: string, x = 0) => ({ pointerType: type, isPrimary: true, button: 0, pointerId: 1, clientX: x, clientY: 0, currentTarget: segment }) as unknown as PointerEvent
  const click = (detail = 1) => ({ detail, target: segment, preventDefault: vi.fn() }) as unknown as MouseEvent
  trigger.pointerdown(pointer('touch'), 'cards')
  timers.shift()!()
  expect(opened).toEqual(['cards'])
  expect(trigger.heldClick(click())).toBe(true)
  // The next tap switches as usual.
  trigger.pointerdown(pointer('touch'), 'cards'); trigger.pointerend(pointer('touch'))
  expect(trigger.heldClick(click())).toBe(false)
  timers.shift()!()
  expect(opened).toEqual(['cards'])
  // Scrolling the row is not a hold.
  trigger.pointerdown(pointer('touch'), 'list'); trigger.pointermove(pointer('touch', 20))
  timers.shift()!()
  expect(opened).toEqual(['cards'])
  trigger.pointerdown(pointer('mouse'), 'list')
  expect(timers).toEqual([])
  const key = (init: KeyboardEventInit) => ({ ...init, preventDefault: vi.fn(), stopPropagation: vi.fn(), currentTarget: segment }) as unknown as KeyboardEvent
  expect(trigger.keydown(key({ key: 'F10' }), 'list')).toBe(false)
  expect(trigger.keydown(key({ key: 'F10', shiftKey: true }), 'list')).toBe(true)
  expect(trigger.keydown(key({ key: 'ContextMenu' }), 'goals')).toBe(true)
  expect(opened).toEqual(['cards', 'list', 'goals'])
})

// The preference is already loaded, so ensureLoaded's continuation is queued before this
// microtask. The write that follows must not land on whoever owns the switch by then.
function personStore() {
  const saved = new Map<string, unknown>([
    ['t/A/view:projects', { last: 'cards', default: 'goals' }],
    ['t/B/view:projects', { last: 'list', default: 'outline' }],
  ])
  const writes: { owner: string; key: string; value: unknown }[] = []
  vi.stubGlobal('fetch', vi.fn(async (input: string, init?: RequestInit) => {
    const key = decodeURIComponent(String(input).split('/api/preferences/')[1] ?? '')
    const id = `${preferenceOwner.value}/${key}`
    if ((init?.method ?? 'GET').toUpperCase() === 'PUT') {
      const { value } = JSON.parse(String(init?.body)) as { value: unknown }
      saved.set(id, value); writes.push({ owner: preferenceOwner.value, key, value })
      return new Response('{}')
    }
    return new Response(JSON.stringify({ key, value: saved.get(id) ?? null }))
  }))
  return { saved, writes }
}
async function drain() {
  await flush()
  await new Promise<void>(resolve => {
    const channel = new MessageChannel()
    channel.port1.onmessage = () => { channel.port1.close(); channel.port2.close(); resolve() }
    channel.port2.postMessage(undefined)
  })
  await flush()
}

it('a delayed default never writes one person\'s choice into the next person\'s preferences', async () => {
  const { saved, writes } = personStore()
  const view = sw({ ...projects, options: ['list', 'cards', 'goals', 'outline'] })
  await view.ready(); await flush()
  expect(view.current.value).toBe('goals')
  view.setDefault(null)
  queueMicrotask(() => setPreferenceOwner(who('B')))
  await drain()
  expect(saved.get('t/A/view:projects')).toEqual({ last: 'cards', default: 'goals' })
  expect(saved.get('t/B/view:projects')).toEqual({ last: 'list', default: 'outline' })
  expect(writes.filter(write => write.owner === 't/B')).toEqual([])
  expect(view.chosen.value).toBeNull()
  await view.ready(); await flush()
  expect(view.current.value).toBe('outline')
})

it('signing in again as the same person during a default write keeps the stored default', async () => {
  const { saved, writes } = personStore()
  const view = sw(projects)
  await view.ready(); await flush()
  expect(view.current.value).toBe('goals')
  view.setDefault(null)
  queueMicrotask(() => { setPreferenceOwner(null); setPreferenceOwner(who('A')) })
  await drain()
  expect(saved.get('t/A/view:projects')).toEqual({ last: 'cards', default: 'goals' })
  expect(writes).toEqual([])
  expect(view.chosen.value).toBeNull()
  await view.ready(); await flush()
  expect(view.current.value).toBe('goals')
})

it('a failed remember stays unconfirmed until the same choice is accepted', async () => {
  const releases = sw({ key: viewKey('releases.lang'), options: ['en', 'de'], fallback: 'en' })
  await releases.ready()
  let status = 503
  let begin: () => void = () => {}
  const started = new Promise<void>(resolve => { begin = () => resolve() })
  const fetch = vi.fn(async (input: string, init?: RequestInit) => {
    const key = decodeURIComponent(String(input).split('/api/preferences/')[1] ?? '')
    if (init?.method === 'PUT') {
      begin()
      if (status >= 400) return new Response('{}', { status })
      const { value } = JSON.parse(String(init.body)) as { value: unknown }
      store.set(key, value)
      return new Response('{}')
    }
    return new Response(JSON.stringify({ key, value: store.get(key) ?? null }))
  })
  vi.stubGlobal('fetch', fetch)
  const puts = () => fetch.mock.calls.filter(call => (call[1] as RequestInit | undefined)?.method === 'PUT').length
  const first = releases.remember('de')
  await started
  expect(await first).toBe(false)
  expect(releases.failed.value).toBe(true)
  expect(releases.lastId.value).toBe('de')
  expect(store.get('view:releases.lang')).toBeUndefined()
  expect(puts()).toBe(1)
  status = 200
  expect(await releases.remember('de')).toBe(true)
  expect(releases.failed.value).toBe(false)
  expect(store.get('view:releases.lang')).toEqual({ last: 'de' })
  expect(puts()).toBe(2)
  // Already on the server: confirming it does not write again.
  expect(await releases.remember('de')).toBe(true)
  expect(puts()).toBe(2)
})
