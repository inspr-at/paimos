// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1618 fix round 2. These fail on 47789e21: a late link, unlink, rename or
// delete still toasts after the person changes, and a created goal stays hidden
// by the search, the state filter or the first page.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { effectScope, nextTick, ref, type EffectScope } from 'vue'
import { createPinia, setActivePinia } from 'pinia'
import { readFileSync } from 'node:fs'
import { compileScript, parse } from '@vue/compiler-sfc'
import ts from 'typescript'
import type { Identity } from '../src/lib/api'
import type { Goal, GoalList, GoalQuery } from '../src/lib/goals'

const mocks = vi.hoisted(() => {
  const media = { matches: false, addEventListener() {}, removeEventListener() {} }
  const location = { pathname: '/', search: '', hash: '', href: 'http://localhost/', origin: 'http://localhost', assign() {}, replace() {} }
  vi.stubGlobal('location', location)
  vi.stubGlobal('window', { location, history: { state: {}, length: 1, pushState() {}, replaceState() {}, go() {}, back() {}, forward() {} }, matchMedia: () => media, addEventListener() {}, removeEventListener() {}, localStorage: { getItem: () => null, setItem() {}, removeItem() {} }, sessionStorage: { getItem: () => null, setItem() {}, removeItem() {} } })
  vi.stubGlobal('navigator', { platform: 'MacIntel', userAgent: 'test', onLine: true })
  const el = () => ({ innerHTML: '', textContent: '', content: {}, style: {}, children: [], setAttribute() {}, appendChild() {}, remove() {}, focus() {}, classList: { contains: () => false } })
  vi.stubGlobal('document', { activeElement: null, fonts: { ready: Promise.resolve() }, documentElement: { lang: 'en' }, head: { append() {} }, body: el(), addEventListener() {}, removeEventListener() {}, querySelector: () => null, querySelectorAll: () => [], createElement: el, createElementNS: el, createTextNode: el, createComment: el })
  return { listGoals: vi.fn(), readGoal: vi.fn(), goalSeries: vi.fn(), goalCreatedAt: vi.fn() }
})
vi.mock('../src/lib/goals', async importOriginal => {
  const actual = await importOriginal<typeof import('../src/lib/goals')>()
  return { ...actual, listGoals: mocks.listGoals, readGoal: mocks.readGoal, goalSeries: mocks.goalSeries, goalCreatedAt: mocks.goalCreatedAt }
})
vi.mock('../src/lib/api', async importOriginal => {
  const actual = await importOriginal<typeof import('../src/lib/api')>()
  return { ...actual, api: vi.fn(async () => new Response(JSON.stringify({ value: null }), { status: 200, headers: { 'content-type': 'application/json' } })), undoEvent: vi.fn() }
})
vi.mock('../src/lib/usePolledData', async importOriginal => {
  const actual = await importOriginal<typeof import('../src/lib/usePolledData')>()
  return { ...actual, usePoller: (run: () => Promise<unknown>) => ({ start(immediate = false) { if (immediate) void run() }, stop() {}, tick() {} }) }
})

import { useSession } from '../src/stores/session'
import { useGoalsPage } from '../src/components/goals/goalsPage'
import { toasts, resetToasts } from '../src/lib/toast'
import * as Vue from 'vue'
import * as sessionStore from '../src/stores/session'
import * as identityScope from '../src/lib/useIdentityScope'
import * as toastLib from '../src/lib/toast'
import * as goalsLib from '../src/lib/goals'
import * as apiLib from '../src/lib/api'

const person = (id: string): Identity => ({ principal: { id, name: id, kind: 'person' }, tenant: { id: 'tenant', name: 'Workspace' } })
function goal(id: string, extra: Partial<Goal> = {}): Goal {
  return {
    id, key: id.toUpperCase(), title: id, kind: 'goal', project_id: null, workspace: true, expansion: 'full',
    counts: { outcomes: 0, tickets: 0 }, items: [], steps: [], next_step: null, projects: [], waits: { count: 0, question_ids: [] },
    progress: { pct: 0, computed_pct: 0, source: 'computed', basis: 'count', total: 0, done: 0, items_without_estimate: 0, items_partially_estimated: 0 },
    states: { moving: 0, queued: 0, paused: 0, done: 0 }, done: false, read_through: 1, generation: 1, ...extra,
  }
}
function listed(items: Goal[], extra: Partial<GoalList> = {}): GoalList {
  return {
    items, totals: { moving: items.length, queued: 0, paused: 0, done: 0 }, totals_partial: false,
    quiet_projects: { count: 0, keys: [] }, next_cursor: undefined, truncated: false, read_through: 2, generation: 2, ...extra,
  }
}
function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>(res => { resolve = res })
  return { promise, resolve }
}
async function flush() {
  for (let i = 0; i < 8; i++) { await Promise.resolve(); await nextTick() }
}

const scopes: EffectScope[] = []
beforeEach(() => {
  setActivePinia(createPinia())
  resetToasts()
  mocks.listGoals.mockReset().mockResolvedValue(listed([goal('g1')]))
  mocks.readGoal.mockReset().mockImplementation(async (id: string) => goal(id, { title: 'Created' }))
  mocks.goalSeries.mockReset().mockResolvedValue({ range: '24h', expansion: 'full', points: [], read_through: 1, generation: 1 })
  mocks.goalCreatedAt.mockReset().mockResolvedValue(1_000)
})
afterEach(() => { for (const scope of scopes.splice(0)) scope.stop(); resetToasts() })

describe('a created goal is revealed through filters and the first page', () => {
  it('clears search and the state filter, and keeps a goal the first page omits', async () => {
    const session = useSession()
    session.identity = person('person-a')
    mocks.listGoals.mockImplementation(async (query: GoalQuery) => listed([goal('other')], { next_cursor: 'page-2', read_through: 3, generation: 3, ...query.state ? { read_through: 4 } : {} }))
    const term = ref('fleet')
    const scope = effectScope()
    scopes.push(scope)
    const page = scope.run(() => useGoalsPage({ term, active: () => true }))!
    page.filter.value = 'done'
    await flush()
    expect(await page.reveal('created')).toBe(true)
    expect(term.value).toBe('')
    expect(page.filter.value).toBe('all')
    expect(page.goals.value.map(item => item.id)).toEqual(['created', 'other'])
    await page.read()
    await flush()
    expect(page.goals.value[0]?.id).toBe('created')
    page.filter.value = 'queued'
    await flush()
    expect(page.goals.value.map(item => item.id)).toEqual(['other'])
  })
})

const writes = { linkGoalWork: vi.fn(), unlinkGoalWork: vi.fn(), deleteGoal: vi.fn(), updateGoal: vi.fn(), undoEvent: vi.fn() }
type View = {
  picker: { value: { goal: Goal } | null }
  menu: { value: { goal: Goal; item?: { id: string; keys: string[]; label: string } } | null }
  pickMode: { value: { submit: (picked: { id: string; key: string; title: string; state: string }[]) => Promise<string | null> } | null }
  openPicker: (goal: Goal, anchor: HTMLElement) => void
  openMenu: (goal: Goal, anchor: HTMLElement, item?: { id: string; keys: string[]; label: string }) => void
  menuAction: (action: string) => Promise<void>
  rename: (goal: Goal, title: string) => Promise<void>
}
function setupView(): View {
  const modules: Record<string, unknown> = {
    vue: Vue,
    'vue-router': { useRoute: () => ({ query: {} }) },
    '../AppIcon.vue': {}, '../business/RowMenu.vue': {}, '../work/RelationPicker.vue': {},
    './GoalRow.vue': {}, './GoalSheet.vue': {}, './GoalWaiting.vue': {}, './GoalStatusNote.vue': {}, './GoalFreshness.vue': {},
    '../../stores/session': sessionStore,
    '../../stores/liveAgents': { useLiveAgents: () => ({ byProject: new Map() }) },
    '../../stores/projects': { useProjects: () => ({ byId: () => null, projects: [] }) },
    '../../lib/shortcuts': { onAction: () => () => {} },
    '../../lib/toast': toastLib,
    '../../lib/work': { plural: (n: number, word: string) => `${n} ${word}` },
    '../../lib/api': { ...apiLib, undoEvent: writes.undoEvent },
    '../../lib/goals': { ...goalsLib, linkGoalWork: writes.linkGoalWork, unlinkGoalWork: writes.unlinkGoalWork, deleteGoal: writes.deleteGoal, updateGoal: writes.updateGoal },
    '../../lib/useIdentityScope': identityScope,
  }
  const { descriptor } = parse(readFileSync(new URL('../src/components/goals/GoalsView.vue', import.meta.url), 'utf8'))
  const { content } = compileScript(descriptor, { id: 'GoalsView.vue' })
  const exports: { default?: { setup: (props: object, context: object) => View } } = {}
  new Function('require', 'exports', ts.transpileModule(content, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } }).outputText)((name: string) => {
    if (!(name in modules)) throw new Error(`missing module ${name}`)
    return modules[name]
  }, exports)
  const page = {
    group: ref('goals'), replacing: ref(false), goals: ref<Goal[]>([goal('g1')]), loaded: ref(true), filter: ref('all'),
    want() {}, charts: new Set<string>(), sort: ref('eta'), dir: ref('asc'), allOpen: ref(false), range: ref('24h'),
    canEdit: () => true, isOpen: () => false, toggleChart() {}, toggle() {}, openSheet() {}, renaming: ref<string | null>(null),
    read: vi.fn(async () => {}), closeSheet() {}, writableProjects: ref([]), workspaceWrite: ref(true), sheet: ref(null),
    live: ref('off'), etaLastSetAt: ref(null), updatedAt: ref(null), list: ref(null), loadingMore: ref(false), loadMore() {},
    error: ref(''), reading: ref(false), setSort() {}, reveal: vi.fn(async () => true),
  }
  const scope = effectScope()
  scopes.push(scope)
  return scope.run(() => exports.default!.setup({ page, term: '', now: 0, hiddenGroups: false }, { emit: () => {}, expose: () => {} }))!
}
const anchor = { isConnected: true, focus() {}, classList: { contains: () => false } } as unknown as HTMLElement

describe('a late goal write does not toast for the next person', () => {
  beforeEach(() => { for (const fn of Object.values(writes)) fn.mockReset() })

  it('drops a link, its Undo, the picker and the menu when the person changes', async () => {
    const session = useSession()
    session.identity = person('person-a')
    const pending = deferred<{ changed: boolean; delta: string[]; node_ids: string[] }>()
    writes.linkGoalWork.mockReturnValue(pending.promise)
    const view = setupView()
    const linked = goal('g1', { items: [] })
    view.openPicker(linked, anchor)
    const submit = view.pickMode.value!.submit([{ id: 'n-1', key: 'PHAROS-11', title: 'One', state: 'queued' }])
    session.identity = person('person-b')
    pending.resolve({ changed: true, delta: ['n-1'], node_ids: ['n-1'] })
    await submit
    await flush()
    expect(toasts).toEqual([])
    expect(view.picker.value).toBeNull()
    expect(writes.unlinkGoalWork).not.toHaveBeenCalled()
    view.openMenu(linked, anchor)
    expect(view.menu.value).not.toBeNull()
    session.identity = person('person-c')
    expect(view.menu.value).toBeNull()
  })

  it('offers no Undo when the link changes nothing, and undoes only the returned delta', async () => {
    const session = useSession()
    session.identity = person('person-a')
    writes.linkGoalWork.mockResolvedValueOnce({ changed: false, delta: [], node_ids: ['n-old'] })
    const view = setupView()
    await view.pickMode.value ? null : null
    view.openPicker(goal('g1'), anchor)
    await view.pickMode.value!.submit([{ id: 'n-old', key: 'PHAROS-11', title: 'Old', state: 'queued' }])
    await flush()
    expect(toasts.map(item => item.message)).toEqual(['Already linked'])
    expect(toasts[0]?.actions).toEqual([])
    resetToasts()
    writes.linkGoalWork.mockResolvedValueOnce({ changed: true, delta: ['n-new'], node_ids: ['n-old', 'n-new'] })
    view.openPicker(goal('g1'), anchor)
    await view.pickMode.value!.submit([{ id: 'n-old', key: 'PHAROS-11', title: 'Old', state: 'queued' }, { id: 'n-new', key: 'PHAROS-12', title: 'New', state: 'queued' }])
    await flush()
    expect(toasts.map(item => item.message)).toEqual(['1 linked'])
    writes.unlinkGoalWork.mockResolvedValue({ changed: true, delta: ['n-new'], node_ids: ['n-old'] })
    toasts[0]!.actions[0]!.run()
    await flush()
    expect(writes.unlinkGoalWork).toHaveBeenCalledWith('g1', ['n-new'])
  })

  it('drops a late unlink, rename and delete', async () => {
    const session = useSession()
    session.identity = person('person-a')
    const unlink = deferred<{ changed: boolean; delta: string[] }>()
    const rename = deferred<{ changed: boolean }>()
    const remove = deferred<{ changed: boolean; event_id: number }>()
    writes.unlinkGoalWork.mockReturnValue(unlink.promise)
    writes.updateGoal.mockReturnValue(rename.promise)
    writes.deleteGoal.mockReturnValue(remove.promise)
    const view = setupView()
    const item = { id: 'n-1', keys: ['PHAROS-11'], label: 'One' }
    view.openMenu(goal('g1'), anchor, item)
    const unlinking = view.menuAction('unlink')
    view.openMenu(goal('g1'), anchor)
    const renaming = view.rename(goal('g1', { title: 'Before' }), 'After')
    view.openMenu(goal('g1'), anchor)
    const deleting = view.menuAction('delete')
    session.identity = person('person-b')
    unlink.resolve({ changed: true, delta: ['n-1'] })
    rename.resolve({ changed: true })
    remove.resolve({ changed: true, event_id: 9 })
    await Promise.all([unlinking, renaming, deleting])
    await flush()
    expect(toasts).toEqual([])
    expect(writes.undoEvent).not.toHaveBeenCalled()
    expect(writes.linkGoalWork).not.toHaveBeenCalled()
  })
})
