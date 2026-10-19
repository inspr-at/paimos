// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1395 fix round 2. A group switch used to drop the list, so the status
// note and the column header vanished until the next read. Epic live re-reads
// missed a worker heartbeat and a delivery state change; a Goals list must not
// re-read on those.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createSSRApp, effectScope, nextTick, ref, type EffectScope } from 'vue'
import { renderToString } from '@vue/server-renderer'
import { createPinia, setActivePinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import type { Identity } from '../src/lib/api'
import type { Goal, GoalList } from '../src/lib/goals'

const mocks = vi.hoisted(() => {
  const media = { matches: false, addEventListener() {}, removeEventListener() {} }
  vi.stubGlobal('window', { history: { state: {} }, matchMedia: () => media, addEventListener() {}, removeEventListener() {}, localStorage: { getItem: () => null, setItem() {}, removeItem() {} }, sessionStorage: { getItem: () => null, setItem() {}, removeItem() {} } })
  vi.stubGlobal('navigator', { platform: 'MacIntel', userAgent: 'test', onLine: true })
  return { listGoals: vi.fn() }
})
vi.mock('../src/lib/goals', async importOriginal => {
  const actual = await importOriginal<typeof import('../src/lib/goals')>()
  return { ...actual, listGoals: mocks.listGoals, goalSeries: vi.fn(async () => ({ range: '24h', expansion: 'full', points: [], read_through: 1, generation: 1 })), goalCreatedAt: vi.fn(async () => 1_000) }
})
vi.mock('../src/lib/api', async importOriginal => {
  const actual = await importOriginal<typeof import('../src/lib/api')>()
  return { ...actual, api: vi.fn(async () => new Response(JSON.stringify({ value: null }), { status: 200, headers: { 'content-type': 'application/json' } })) }
})
vi.mock('../src/components/projects/LiveAgents.vue', () => ({ default: { name: 'LiveAgents', props: ['agents', 'project', 'variant'], render: () => null } }))
vi.mock('../src/lib/usePolledData', async importOriginal => {
  const actual = await importOriginal<typeof import('../src/lib/usePolledData')>()
  return { ...actual, usePoller: (run: () => Promise<unknown>) => ({ start(immediate = false) { if (immediate) void run() }, stop() {}, tick() {} }) }
})

import { EPIC_STREAM_EVENTS, GOAL_STREAM_EVENTS, openGoalsLive, type GoalsLiveSource } from '../src/lib/goalsLive'
import { useSession } from '../src/stores/session'
import { useGoalsPage } from '../src/components/goals/goalsPage'
import GoalsView from '../src/components/goals/GoalsView.vue'

const person = (id: string): Identity => ({ principal: { id, name: id, kind: 'person' }, tenant: { id: 'tenant', name: 'Workspace' } })
const note = { text: 'Nine items merged since this morning.', by: 'p-lead', by_name: 'LEAD', set_at: '2026-10-17T08:00:00Z' }
function goal(id: string, extra: Partial<Goal> = {}): Goal {
  return {
    id, key: id.toUpperCase(), title: id, kind: 'goal', project_id: null, workspace: true, expansion: 'full',
    counts: { outcomes: 1, tickets: 1 }, items: [], steps: [], next_step: null, projects: [], waits: { count: 0, question_ids: [] },
    progress: { pct: 10, computed_pct: 10, source: 'computed', basis: 'count', total: 1, done: 0, items_without_estimate: 0, items_partially_estimated: 0 },
    states: { moving: 1, queued: 0, paused: 0, done: 0 }, done: false, read_through: 1, generation: 1, ...extra,
  }
}
const listed = (items: Goal[], extra: Partial<GoalList> = {}): GoalList => ({
  items, totals: { moving: items.length, queued: 0, paused: 0, done: 0 }, totals_partial: false,
  quiet_projects: { count: 0, keys: [] }, truncated: false, read_through: 1, generation: 1, status_note: note, ...extra,
})
function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>(res => { resolve = res })
  return { promise, resolve }
}
async function flush() {
  for (let i = 0; i < 8; i++) { await Promise.resolve(); await nextTick() }
}
async function html(props: Record<string, unknown>) {
  const pinia = createPinia()
  setActivePinia(pinia)
  useSession().identity = person('person-a')
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/:pathMatch(.*)*', component: { render: () => null } }] })
  const app = createSSRApp(GoalsView, props)
  app.use(pinia)
  app.use(router)
  return (await renderToString(app)).replace(/<!--[^>]*-->/g, '')
}

class FakeSource implements GoalsLiveSource {
  readyState = 1
  onerror: ((event: Event) => unknown) | null = null
  closed = false
  private listeners = new Map<string, ((event: MessageEvent) => void)[]>()
  constructor(readonly url: string) {}
  addEventListener(type: string, listener: (event: MessageEvent) => void) { this.listeners.set(type, [...(this.listeners.get(type) ?? []), listener]) }
  close() { this.closed = true }
  emit(type: string, data: unknown = {}, id = '') { for (const listener of this.listeners.get(type) ?? []) listener(new MessageEvent(type, { data: JSON.stringify(data), lastEventId: id })) }
}

describe('epic live events', () => {
  it('a goals stream ignores a heartbeat and a delivery state change; an epics stream re-reads on both', () => {
    const changed = vi.fn()
    const goals = new FakeSource('/goals')
    openGoalsLive({ open: () => goals, changed, state() {}, events: GOAL_STREAM_EVENTS })
    goals.emit('stream.ready', { after: 1, resumed: false })
    goals.emit('harness.heartbeat', {}, '2')
    goals.emit('delivery.state_changed', {}, '3')
    expect(changed).not.toHaveBeenCalled()

    const epicChanged = vi.fn()
    const epics = new FakeSource('/epics')
    openGoalsLive({ open: () => epics, changed: epicChanged, state() {}, events: EPIC_STREAM_EVENTS })
    epics.emit('stream.ready', { after: 1, resumed: false })
    epics.emit('harness.heartbeat', {}, '2')
    epics.emit('delivery.state_changed', {}, '3')
    epics.emit('delivery.held', {}, '4')
    expect(epicChanged).toHaveBeenCalledTimes(2)
  })
})

describe('the epics page re-reads on heartbeat and delivery state', () => {
  const scopes: EffectScope[] = []
  const streams: FakeSource[] = []
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'setInterval', 'clearInterval'] })
    vi.stubGlobal('EventSource', class extends FakeSource { constructor(url: string) { super(url); streams.push(this) } })
    mocks.listGoals.mockReset().mockResolvedValue(listed([goal('g1')]))
  })
  afterEach(() => { for (const scope of scopes.splice(0)) scope.stop(); streams.length = 0; vi.useRealTimers() })

  it('epics re-read on those events and a goals page does not', async () => {
    useSession().identity = person('person-a')
    const scope = effectScope()
    scopes.push(scope)
    const page = scope.run(() => useGoalsPage({ term: ref(''), active: () => true, groups: {} }))!
    await vi.advanceTimersByTimeAsync(0)
    expect(mocks.listGoals).toHaveBeenCalled()
    page.setGroup('epics')
    await vi.advanceTimersByTimeAsync(0)
    const before = mocks.listGoals.mock.calls.length
    const stream = streams.at(-1)!
    stream.emit('stream.ready', { after: 3, resumed: false })
    stream.emit('harness.heartbeat', {}, '4')
    await vi.advanceTimersByTimeAsync(400)
    expect(mocks.listGoals.mock.calls.length).toBe(before + 1)
    stream.emit('delivery.state_changed', {}, '5')
    await vi.advanceTimersByTimeAsync(400)
    expect(mocks.listGoals.mock.calls.length).toBe(before + 2)

    const goals = scope.run(() => useGoalsPage({ term: ref(''), active: () => true }))!
    await vi.advanceTimersByTimeAsync(0)
    const goalStream = streams.at(-1)!
    const held = mocks.listGoals.mock.calls.length
    goalStream.emit('stream.ready', { after: 1, resumed: false })
    goalStream.emit('harness.heartbeat', {}, '8')
    goalStream.emit('delivery.state_changed', {}, '9')
    await vi.advanceTimersByTimeAsync(400)
    expect(mocks.listGoals.mock.calls.length).toBe(held)
    expect(goals.loaded.value).toBe(true)
  })
})

describe('a group switch keeps the frame', () => {
  const scopes: EffectScope[] = []
  beforeEach(() => {
    setActivePinia(createPinia())
    mocks.listGoals.mockReset().mockResolvedValue(listed([goal('g1')]))
  })
  afterEach(() => { for (const scope of scopes.splice(0)) scope.stop() })

  it('keeps the status note and header while the rows are replaced, then shows an empty list', async () => {
    useSession().identity = person('person-a')
    const scope = effectScope()
    scopes.push(scope)
    const page = scope.run(() => useGoalsPage({ term: ref(''), active: () => true, groups: {} }))!
    await flush()
    expect(page.loaded.value).toBe(true)
    expect(page.statusNote.value?.text).toBe(note.text)

    const pending = deferred<GoalList>()
    mocks.listGoals.mockImplementation(() => pending.promise)
    page.setGroup('epics')
    await flush()
    expect(page.loaded.value).toBe(true)
    expect(page.replacing.value).toBe(true)
    expect(page.statusNote.value?.text).toBe(note.text)
    expect(page.goals.value).toEqual([])
    const pendingHtml = await html({ page, term: '', now: Date.parse(note.set_at), hiddenGroups: false })
    expect(pendingHtml).toContain(note.text)
    expect(pendingHtml).toContain('goal-head')
    expect(pendingHtml).toContain('Lands')
    expect(pendingHtml).toContain('aria-busy="true"')
    expect(pendingHtml).not.toContain('No open epics')
    expect(pendingHtml).not.toContain('No goals yet')

    pending.resolve(listed([goal('e1', { kind: 'epic', title: 'Queued epic' })], { read_through: 2, generation: 2 }))
    await flush()
    expect(page.replacing.value).toBe(false)
    expect(page.goals.value.map(item => item.title)).toEqual(['Queued epic'])

    mocks.listGoals.mockResolvedValue(listed([], { read_through: 3, generation: 3 }))
    page.setGroup('goals')
    await flush()
    expect(page.replacing.value).toBe(false)
    expect(page.goals.value).toEqual([])
    const emptyHtml = await html({ page, term: '', now: Date.parse(note.set_at), hiddenGroups: false })
    expect(emptyHtml).toContain('No goals yet')
    expect(emptyHtml).not.toContain('goal-head')
  })
})
