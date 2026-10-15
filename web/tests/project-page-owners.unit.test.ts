// SPDX-License-Identifier: AGPL-3.0-only
// Risks: extraction loses shared refs, query state, cancellation fences or focus.
import { readFileSync } from 'node:fs'
import ts from 'typescript'
import { afterEach, expect, it, vi } from 'vitest'
import { computed, effectScope, nextTick, reactive, ref, shallowRef, toRefs, watch, type EffectScope } from 'vue'
import { filtersFromQuery, filtersToQuery, hasFilters, totalFrom } from '../src/lib/ticketList'
import { PROJECT_SECTIONS, projectSection, ticketView } from '../src/components/work/projectNavigation'
import { bindProjectRef } from '../src/views/project/context'

// Run the actual owner body with controlled IO, as project-related.unit.test.ts
// does for navigation handlers. Real Vue refs/watchers prove cross-owner changes.
function owner(name: string, dependencies: Record<string, unknown> = {}) {
  const text = readFileSync(new URL(`../src/views/project/${name}.ts`, import.meta.url), 'utf8')
  const source = ts.createSourceFile(`${name}.ts`, text, ts.ScriptTarget.Latest, true)
  const declaration = source.statements.find(ts.isFunctionDeclaration)!
  const body = ts.transpileModule(declaration.getText(source).replace(/^export /, ''), {
    compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ESNext },
  }).outputText
  const imports = { computed, ref, shallowRef, reactive, toRefs, watch, nextTick,
    onMounted: () => {}, onBeforeUnmount: () => {}, ...dependencies }
  return new Function(...Object.keys(imports), `${body}; return ${name}`)(...Object.values(imports))
}
const scopes: EffectScope[] = []
function run<T>(work: () => T): T { const scope = effectScope(); scopes.push(scope); return scope.run(work)! }
afterEach(() => { for (const scope of scopes.splice(0)) scope.stop(); vi.unstubAllGlobals(); vi.useRealTimers() })
function media() {
  vi.stubGlobal('window', {
    matchMedia: () => ({ matches: true, addEventListener: vi.fn(), removeEventListener: vi.fn() }),
    addEventListener: vi.fn(), removeEventListener: vi.fn(), history: { state: {} },
  })
}
function fixture() {
  const route = reactive({ params: { projectKey: 'AEON' }, path: '/p/AEON/tickets', fullPath: '/p/AEON/tickets', query: {} as Record<string, string>, hash: '' })
  const router = { replace: vi.fn(async (location: { query: Record<string, string> }) => { route.query = location.query }), push: vi.fn(), resolve: vi.fn() }
  const state = {
    route, router, projectId: ref<string | null>(null), projectKey: ref('AEON'), routeKey: ref('AEON'), ticketKey: ref(''), section: ref('tickets'),
    project: ref<{ id: string; title: string; status_counts?: unknown } | undefined>(), session: reactive({ identity: null as unknown, requiresSignIn: false }),
    filters: ref(filtersFromQuery({})), knowledgeFilters: ref({ q: '' }), knowledgeDisplay: ref({ view: 'entries' }), knowledgeView: ref('entries'),
    knowledgeActive: ref(false), knowledgeEntryOpen: ref(false), knowledgeType: ref(null), knowledgeSlug: ref(''),
    graphActive: ref(false), settingsActive: ref(false), deliveryActive: ref(false), outlineActive: ref(false), fullView: ref(false),
    ticketsHeader: ref(true), activeTicketView: ref({ id: 'list' }), writable: ref(true), cursorId: ref<string | null>(null),
    collapsed: ref(new Set<string>()), creating: ref(false), me: ref({ id: 'person', name: 'Person' }),
    table: ref({ focusGrid: vi.fn(), scrollToRow: vi.fn() }), panel: ref(null), statusMenu: ref(null), assigneeMenu: ref(null),
    scrollRoot: ref(null), toolbarWrap: ref(null), vocabulary: { leaf: { name: 'Ticket' }, value: {}, load: vi.fn() },
    queueSnapshot: ref(null), queue: { snapshots: {}, errors: {}, load: vi.fn() },
    sectionPath: (section = 'tickets') => `/p/AEON/${section}`, ticketPath: (key: string) => `/p/AEON/${key}`, ticketSectionQuery: () => ({}),
    recovery: ref(null), recovering: ref(false), authorizedProject: ref(undefined),
  }
  const rows = ref([{ id: 'a', key: 'AEON-1', updated_at: 'v1', state: 'open' }, { id: 'b', key: 'AEON-2', updated_at: 'v1', state: 'open' }, { id: 'c', key: 'AEON-3', updated_at: 'v1', state: 'open' }])
  const data = {
    sequence: rows, rowsById: computed(() => new Map(rows.value.map(row => [row.id, row]))), groups: ref([]),
    list: { rows, cursor: ref(null), loadedOnce: ref(false), facets: ref({}), loading: ref(false), load: vi.fn(), invalidate: vi.fn() },
    outline: { loadedRows: rows, findByKey: () => null, rows, matchMode: ref(false) },
    activeLive: ref({ deletedAmong: () => [] as string[], apply: vi.fn() }),
  }
  return { state, data, rows }
}

it('state recovery keeps only the mounted editor copy, never an authorized project id', () => {
  const { state: input } = fixture()
  const session = reactive({ identity: null, requiresSignIn: true })
  const create = owner('useProjectState', { useRoute: () => input.route, useRouter: () => input.router,
    useProjects: () => ({ byRouteKey: () => undefined }), useSession: () => session,
    PROJECT_SECTIONS, projectSection, ticketView, filtersFromQuery, knowledgeFiltersFrom: () => ({}), isKnowledgeType: () => false,
    useWorkQueue: () => input.queue, useReleases: () => ({ load: vi.fn() }), useWorkVocabulary: () => input.vocabulary,
    usePoller: () => ({}), useDensity: () => ({ density: ref('comfortable'), set: vi.fn() }),
    useProjectHeader: () => ({ headerDensity: ref('compact') }), useDeveloperSettings: () => ({ loading: ref(false), showHeaderGraph: ref(false) }),
    can: () => false, scopeOwner: () => '',
  })
  const state = run(() => create())
  state.recovery.value = { projectKey: 'AEON', ticketKey: '', project: { id: 'old-project' }, owner: 'old-owner', person: { id: 'old-person' } }
  expect(state.project.value.id).toBe('old-project')
  expect(state.projectId.value).toBeNull()
  expect(state.workspaceOwner.value).toBe('old-owner')
  // The recovery must stop at a different address.
  input.route.params.projectKey = 'OTHER'
  expect(state.project.value).toBeUndefined()
})

it('data reports a failed queue read instead of displaying an unfiltered success', async () => {
  const { state, data } = fixture()
  const listNodes = vi.fn()
  const create = owner('useProjectData', { listNodes, useTicketList: () => data.list, useOutline: () => data.outline,
    useLiveList: () => ({}), useLiveAgents: () => ({ updatedAt: null }), totalFrom, hasFilters,
  })
  const value = run(() => create(state, {}))
  state.projectId.value = 'project'
  Object.assign(state.queue.errors, { project: 'Queue offline' })
  await expect(value.fetchWorkList({ state: ['queued'] })).rejects.toThrow('Queue offline')
  expect(listNodes).not.toHaveBeenCalled()
})

it('columns in a saved view change its URL while plain-list columns save the person preference', () => {
  const { state, data } = fixture(), save = vi.fn(), update = vi.fn()
  state.projectId.value = 'project'
  const create = owner('useProjectColumns', { usePreference: () => ({ value: ref({}), save }), PINNED: ['key', 'title'] })
  const value = run(() => create(state, data, update))
  value.saveColumns(['key', 'state'], ['state'])
  expect(save).toHaveBeenCalledWith({ order: ['key', 'state'], visible: ['state'] }, 0)
  state.filters.value.view = 'saved'
  value.saveColumns(['key', 'state'], ['state'])
  expect(update).toHaveBeenCalledExactlyOnceWith({ cols: ['state'] })
})

it('panel resolution discards an in-flight response after the owner changes', async () => {
  media()
  const { state, data } = fixture()
  const pending = Promise.withResolvers<{ items: unknown[] }>()
  const listNodes = vi.fn(() => pending.promise)
  let ownerId = 'first'
  const create = owner('useProjectPanel', { listNodes, scopeOwner: () => ownerId, listNativeMemberships: vi.fn(async () => new Map()), rowStore: { mark: () => 1, adopt: vi.fn() } })
  state.projectId.value = 'project'; state.ticketKey.value = 'AEON-9'
  const pane = run(() => create(state, data))
  Object.assign(state.session, { authenticationCurrent: () => true })
  const resolving = pane.resolvePanel()
  expect(listNodes).toHaveBeenCalled()
  ownerId = 'second'
  pending.resolve({ items: [{ id: 'old', key: 'AEON-9' }] })
  await resolving
  expect(pane.fetched.value).toBeNull()
  expect(pane.panelItem.value).toBeNull()
})

it('filters keep a separate query for each section and preserve routed ticket identity', () => {
  const { state, data } = fixture(), reset = vi.fn()
  const create = owner('useProjectFilters', { PROJECT_SECTIONS, filtersToQuery })
  const value = run(() => create(state, data, reset))
  state.route.query = { q: 'tickets' }
  value.setSection('knowledge')
  expect(state.router.push).toHaveBeenLastCalledWith({ path: '/p/AEON/knowledge', query: {} })
  state.section.value = 'knowledge'; state.route.query = { q: 'knowledge', view: 'graph' }
  value.setSection('tickets')
  expect(state.router.push).toHaveBeenLastCalledWith({ path: '/p/AEON/tickets', query: { q: 'tickets' } })
})

it('saved views reject a save response after the person changes', async () => {
  const { state, data } = fixture(), pending = Promise.withResolvers<unknown>(), toast = vi.fn()
  const create = owner('useProjectViews', { viewsOf: () => ({ items: [], loaded: true }), PINNED: [],
    useViewFigures: () => ({ figures: ref(null), whole: ref(null), busy: ref(false), failed: ref(false), narrowed: ref(false), retry: vi.fn() }),
    saveViewState: () => pending.promise, hasFilters, scopeOwner: () => '', toast,
  })
  const columns = { listPrefs: ref({}), listPref: ref(null), toolbarColumns: ref({ order: [], visible: [] }) }
  const controls = { chipLabel: vi.fn(), update: vi.fn() }
  const value = run(() => create(state, data, columns, controls))
  // Set after setup and call before its watchers flush, as an in-flight user action.
  state.projectId.value = 'project'
  state.filters.value.view = 'saved'
  const saving = value.saveActive({ id: 'saved', name: 'Mine' })
  state.me.value = { id: 'another-person', name: 'Another' }
  pending.resolve({ id: 'saved' })
  // Stop watchers before flushing; the save's own captured-owner guard is under test.
  for (const scope of scopes) scope.stop()
  await saving
  expect(controls.update).not.toHaveBeenCalled()
  expect(toast).not.toHaveBeenCalled()
})

it('selection retains ranges and drops rows that have left the loaded list', async () => {
  media()
  const { state, data, rows } = fixture()
  const create = owner('useProjectSelection', { PRIORITIES: [], can: () => true })
  const value = run(() => create(state, data, {}))
  value.selectRow(rows.value[0], 'toggle'); value.selectRow(rows.value[2], 'range')
  expect([...value.selected.value]).toEqual(['a', 'b', 'c'])
  rows.value = rows.value.slice(1)
  await nextTick()
  expect([...value.selected.value]).toEqual(['b', 'c'])
  value.clearSelection()
  expect(value.phonePicking.value).toBe(false)
})

it('assignee opening rejects people loaded for an older project generation', async () => {
  const { state, data } = fixture(), pending = Promise.withResolvers<void>()
  let generation = 1
  const create = owner('useProjectActions', { command: ref(null), remember: vi.fn() })
  const value = run(() => create(state, data, { panelItem: ref(null), loadProjectPeople: () => pending.promise, getPeopleGeneration: () => generation }, {}))
  const opening = value.openAssignee({ id: 'a', updated_at: 'v1' }, { isConnected: true })
  generation++
  pending.resolve(); await opening
  expect(state.assigneeMenu.value).toBeNull()
})

it('live application preserves an open row and advances the cursor before removing it', async () => {
  media(); vi.stubGlobal('document', { activeElement: null, getElementById: () => null })
  const { state, data, rows } = fixture(), pane = { panelItem: ref(rows.value[0]), fetched: ref(null) }
  state.cursorId.value = 'a'
  const create = owner('useProjectLive')
  const value = run(() => create(state, data, pane))
  const apply = vi.fn(() => { expect(state.cursorId.value).toBe('b'); expect(pane.fetched.value).toEqual(rows.value[0]) })
  value.aroundLiveApply(new Set(['a']), apply)
  expect(apply).toHaveBeenCalledExactlyOnceWith()
  await nextTick()
})

it('knowledge queues filter writes against the latest landed query', async () => {
  media()
  const { state } = fixture()
  state.knowledgeActive.value = true
  state.route.query = { entry: 'howto/one' }
  const create = owner('useProjectKnowledge', { provide: vi.fn(), routeLocationKey: Symbol(), routerKey: Symbol(), DOCK_MEDIA: '',
    parseEntryParam: () => null, useKnowledge: () => ({ sequence: ref([]) }), knowledgeQuery: (filters: unknown) => filters,
    settledNavigation: () => ({ stop: vi.fn() }), isNavigationFailure: () => false, NavigationFailureType: {},
  })
  // The real filters are computed from the URL, including the preceding write.
  Object.assign(state, { knowledgeFilters: computed(() => ({ q: state.route.query.q ?? '', tag: state.route.query.tag ?? '' })) })
  const value = run(() => create(state, () => ({})))
  value.updateKnowledge({ q: 'needle' }); value.updateKnowledge({ tag: 'guide' })
  for (let i = 0; i < 8; i++) await Promise.resolve()
  expect(state.router.replace).toHaveBeenCalledTimes(2)
  expect(state.route.query).toEqual({ view: 'entries', q: 'needle', tag: 'guide', entry: 'howto/one' })
})

it('keyboard keeps browser shortcuts native and submits selection through its owner', () => {
  vi.stubGlobal('HTMLElement', class {}); vi.stubGlobal('HTMLInputElement', class {})
  vi.stubGlobal('document', { querySelector: () => null, activeElement: null })
  const { state, data } = fixture(), selectAll = vi.fn()
  const create = owner('useProjectKeyboard')
  const keys = run(() => create(state, data, { panelItem: ref(null), trail: ref([]) }, { selectable: ref(true), selectAll }, {}, {}))
  const event = { key: 's', metaKey: true, preventDefault: vi.fn(), target: null }
  keys.keydown(event)
  expect(event.preventDefault).not.toHaveBeenCalled()
  keys.keydown({ ...event, key: 'a' })
  expect(selectAll).toHaveBeenCalledExactlyOnceWith(true)
})

it('render sections attach and clear the same page-owned ref', () => {
  const element = { id: 'toolbar' }, target = ref<unknown>()
  const attach = bindProjectRef(target)
  attach(element); expect(target.value).toEqual(element)
  attach(null); expect(target.value).toBeNull()
})
