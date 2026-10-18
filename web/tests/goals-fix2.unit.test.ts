// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1457 fix round 2. These fail on 05658565: a list read during "Show more"
// left the button disabled; an account change stranded chart reads; a goal too
// large to expand showed its missing counts as zero; a phone item hid every ticket link.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createSSRApp, effectScope, nextTick, ref, type EffectScope } from 'vue'
import { renderToString } from '@vue/server-renderer'
import { createPinia, setActivePinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import type { Identity } from '../src/lib/api'
import type { Goal, GoalList, GoalOutcome, GoalQuery, GoalSeries } from '../src/lib/goals'

const mocks = vi.hoisted(() => {
  const media = { matches: false, addEventListener() {}, removeEventListener() {} }
  // Document stays absent: Vue's server renderer takes that path. Theme still needs matchMedia.
  vi.stubGlobal('window', { history: { state: {} }, matchMedia: () => media, addEventListener() {}, removeEventListener() {}, localStorage: { getItem: () => null, setItem() {}, removeItem() {} }, sessionStorage: { getItem: () => null, setItem() {}, removeItem() {} } })
  vi.stubGlobal('navigator', { platform: 'MacIntel', userAgent: 'test', onLine: true })
  return { listGoals: vi.fn(), goalSeries: vi.fn(), goalCreatedAt: vi.fn() }
})
vi.mock('../src/lib/goals', async importOriginal => {
  const actual = await importOriginal<typeof import('../src/lib/goals')>()
  return { ...actual, listGoals: mocks.listGoals, goalSeries: mocks.goalSeries, goalCreatedAt: mocks.goalCreatedAt }
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

import { useSession } from '../src/stores/session'
import { useGoalsPage } from '../src/components/goals/goalsPage'
import GoalItem from '../src/components/goals/GoalItem.vue'
import GoalRow from '../src/components/goals/GoalRow.vue'
import GoalsToolbar from '../src/components/goals/GoalsToolbar.vue'
import GoalsView from '../src/components/goals/GoalsView.vue'
import type { GoalsPage } from '../src/components/goals/goalsPage'

const person = (id: string): Identity => ({ principal: { id, name: id, kind: 'person' }, tenant: { id: 'tenant', name: 'Workspace' } })
const counts = { moving: 1, queued: 0, paused: 0, done: 0 }
function goal(id: string, extra: Partial<Goal> = {}): Goal {
  return {
    id, key: id.toUpperCase(), title: id, kind: 'goal', project_id: null, workspace: true, expansion: 'full',
    counts: { outcomes: 1, tickets: 1 }, items: [], steps: [], next_step: null, projects: [], waits: { count: 0, question_ids: [] },
    progress: { pct: 10, computed_pct: 10, source: 'computed', basis: 'count', total: 1, done: 0, items_without_estimate: 0, items_partially_estimated: 0 },
    states: { ...counts }, done: false, read_through: 1, generation: 1, ...extra,
  }
}
function listed(items: Goal[], extra: Partial<GoalList> = {}): GoalList {
  return {
    items, totals: { moving: items.length, queued: 0, paused: 0, done: 0 }, totals_partial: false,
    quiet_projects: { count: 0, keys: [] }, next_cursor: 'c1', truncated: false, read_through: 1, generation: 1, ...extra,
  }
}
const series = (): GoalSeries => ({ range: '24h', expansion: 'full', points: [], read_through: 1, generation: 1 })
function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>(res => { resolve = res })
  return { promise, resolve }
}
async function flush() {
  for (let i = 0; i < 5; i++) { await Promise.resolve(); await nextTick() }
}

const scopes: EffectScope[] = []
beforeEach(() => {
  setActivePinia(createPinia())
  mocks.listGoals.mockReset().mockResolvedValue(listed([goal('g1')]))
  mocks.goalSeries.mockReset().mockResolvedValue(series())
  mocks.goalCreatedAt.mockReset().mockResolvedValue(1_000)
})
afterEach(() => { for (const scope of scopes.splice(0)) scope.stop() })

function mount() {
  const term = ref('')
  const scope = effectScope()
  scopes.push(scope)
  const page = scope.run(() => useGoalsPage({ term, active: () => true }))!
  return page
}

describe('goals pagination and charts stay current', () => {
  it('a refresh during Show more clears the disabled state and a stale page cannot cancel the next one', async () => {
    const first = listed([goal('g1')], { next_cursor: 'c1', read_through: 1, generation: 1 })
    const second = listed([goal('g9')], { next_cursor: 'c2', read_through: 2, generation: 2 })
    const third = listed([goal('g3')], { next_cursor: undefined, read_through: 3, generation: 3 })
    const stale = listed([goal('stale')], { next_cursor: 'c9', read_through: 1, generation: 1 })
    const more = deferred<GoalList>()
    const refresh = deferred<GoalList>()
    const later = deferred<GoalList>()
    let reads = 0
    mocks.listGoals.mockImplementation((query: GoalQuery) => {
      if (query.cursor === 'c1') return more.promise
      if (query.cursor === 'c2') return later.promise
      reads += 1
      return reads === 1 ? Promise.resolve(first) : refresh.promise
    })
    useSession().identity = person('person-a')
    const page = mount()
    await flush()
    expect(page.list.value?.items.map(item => item.id)).toEqual(['g1'])

    const pending = page.loadMore()
    expect(page.loadingMore.value).toBe(true)
    page.filter.value = 'moving'
    // The filter watch flushes on the next tick, while the old page is still in flight.
    await flush()
    expect(page.loadingMore.value).toBe(false)

    refresh.resolve(second)
    await flush()
    expect(page.list.value?.items.map(item => item.id)).toEqual(['g9'])
    const again = page.loadMore()
    expect(page.loadingMore.value).toBe(true)
    more.resolve(stale)
    await pending
    await flush()
    expect(page.loadingMore.value).toBe(true)
    expect(page.list.value?.items.map(item => item.id)).toEqual(['g9'])
    later.resolve(third)
    await again
    await flush()
    expect(page.loadingMore.value).toBe(false)
    expect(page.list.value?.items.map(item => item.id)).toEqual(['g9', 'g3'])
  })

  it('an owner change frees chart slots and drops the previous person’s series', async () => {
    const old = [deferred<GoalSeries>(), deferred<GoalSeries>(), deferred<GoalSeries>()]
    let first = 0
    mocks.goalSeries.mockImplementation(() => old[first++]!.promise)
    const session = useSession()
    session.identity = person('person-a')
    const page = mount()
    await flush()
    for (const id of ['a', 'b', 'c']) page.want(goal(id))
    expect(mocks.goalSeries).toHaveBeenCalledTimes(3)

    const next = [deferred<GoalSeries>(), deferred<GoalSeries>(), deferred<GoalSeries>()]
    let second = 0
    mocks.goalSeries.mockImplementation(() => next[second++]!.promise)
    session.identity = person('person-b')
    for (const id of ['d', 'e', 'f']) page.want(goal(id))
    expect(mocks.goalSeries.mock.calls.slice(3).map(call => call[0])).toEqual(['d', 'e', 'f'])
    for (const id of ['d', 'e', 'f']) expect(page.seriesFor(goal(id))?.state).toBe('reading')

    for (const flight of old) flight.resolve(series())
    await flush()
    for (const id of ['a', 'b', 'c']) expect(page.seriesFor(goal(id))).toBeUndefined()
    for (const id of ['d', 'e', 'f']) expect(page.seriesFor(goal(id))?.state).toBe('reading')
    for (const flight of next) flight.resolve(series())
    await flush()
    for (const id of ['d', 'e', 'f']) expect(page.seriesFor(goal(id))?.state).toBe('ready')
  })
})

const outcome = (extra: Partial<GoalOutcome> = {}): GoalOutcome => ({
  id: 'o1', ref: 'o1', label: 'Browser tests fit the limit', note: '', position: 1, keys: ['AEON-1402'],
  state: 'queued', session_ids: [], eta: { source: 'none', partial: false }, passed: false,
  coverage: { estimated_leaves: 1, total_leaves: 1 }, weight: 1, handed_over: false, ...extra,
})

async function html(component: unknown, props: Record<string, unknown>) {
  const pinia = createPinia()
  setActivePinia(pinia)
  useSession().identity = person('person-a')
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/:pathMatch(.*)*', component: { render: () => null } }] })
  const app = createSSRApp(component as never, props)
  app.use(pinia)
  app.use(router)
  return renderToString(app)
}

describe('partial totals and phone tickets', () => {
  it('a goal too large to expand shows unknown counts, and partial totals say so', async () => {
    const huge = goal('big', { expansion: 'too_large', states: undefined, progress: undefined, items: undefined, title: 'Too big' })
    const empty = goal('zero', { states: { moving: 0, queued: 0, paused: 0, done: 0 }, title: 'Empty counts' })
    const row = {
      isOpen: () => false, filter: ref('all'), searchOpens: () => false, folds: new Map(), charts: new Set(),
      range: ref('24h'), seriesFor: () => undefined, toggle() {}, toggleChart() {}, setRange() {}, toggleFold() {},
    }
    const hugeHtml = await html(GoalRow, { goal: huge, page: row, now: 0, agents: new Map(), term: '', menuOpen: false })
    expect(hugeHtml).toContain('Moving unknown')
    expect(hugeHtml).toContain('Queued unknown')
    expect(hugeHtml).toContain('Done unknown')
    expect(hugeHtml).not.toContain('0 moving')
    expect(hugeHtml).toContain('–')
    const zeroHtml = await html(GoalRow, { goal: empty, page: row, now: 0, agents: new Map(), term: '', menuOpen: false })
    expect(zeroHtml).toContain('0 moving')
    expect(zeroHtml).not.toContain('Moving unknown')

    const toolbar = (partial: boolean) => ({
      counts: ref({ all: 4, moving: 1, queued: 2, paused: 0, done: 1 }),
      totalsPartial: ref(partial), filter: ref('all'), hideDone: ref(true), allOpen: ref(false),
      setHideDone() {}, toggleAll() {},
    })
    // The toolbar reads .value on refs the same way it reads the page's computed refs.
    const partialHtml = await html(GoalsToolbar, { page: toolbar(true), part: 'chips' })
    expect(partialHtml).toContain('Counts omit oversized goals.')
    expect(partialHtml).toContain('All, 4, incomplete')
    expect(partialHtml).not.toContain('aria-label="All, 4"')
    const wholeHtml = await html(GoalsToolbar, { page: toolbar(false), part: 'chips' })
    expect(wholeHtml).toContain('aria-label="All, 4"')
    expect(wholeHtml).not.toContain('incomplete')
    expect(wholeHtml).not.toContain('Counts omit oversized goals.')

    const view = toolbar(true) as unknown as GoalsPage
    Object.assign(view, {
      goals: ref([huge]), list: ref(listed([huge], { totals_partial: true, totals: { moving: 1, queued: 2, paused: 0, done: 1 } })),
      loaded: ref(true), error: ref(''), reading: ref(false), loadingMore: ref(false), hiddenByGroups: ref(false),
      sort: ref('eta'), dir: ref('asc'), range: ref('24h'), setSort() {}, setRange() {}, loadMore() {}, read() {}, want() {},
      charts: new Set(), folds: new Map(), isOpen: () => false, searchOpens: () => false, seriesFor: () => undefined,
      toggle() {}, toggleChart() {}, toggleFold() {}, allOpen: ref(false),
    })
    const viewHtml = await html(GoalsView, { page: view, term: '', now: 0, hiddenGroups: false })
    expect(viewHtml).toContain('Counts omit oversized goals.')
    expect(viewHtml).toContain('Moving unknown')
  })

  it('a phone item keeps a link for every ticket, including when no agent is on it', async () => {
    const item = outcome({ keys: ['AEON-1402', 'AEON-1409'], session_ids: [] })
    const rendered = await html(GoalItem, {
      item, goalId: 'g-r136', agents: [], project: { title: 'Release', routeKey: 'AEON' }, now: 0,
    })
    expect(rendered).toContain('Tickets for Browser tests fit the limit')
    expect(rendered).toContain('href="/p/AEON/AEON-1402"')
    expect(rendered).toContain('href="/p/AEON/AEON-1409"')
  })
})
