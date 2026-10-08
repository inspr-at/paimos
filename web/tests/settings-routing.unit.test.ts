// SPDX-License-Identifier: AGPL-3.0-only
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { createMemoryHistory, createRouter, type Router, type RouteRecordRaw } from 'vue-router'
import { defineComponent } from 'vue'
import { sourceModule } from './record-source'

const empty = defineComponent({ render: () => null })
let router: Router, loadVocabulary: ReturnType<typeof vi.fn>
beforeEach(() => {
  loadVocabulary = vi.fn(async () => {})
  vi.stubGlobal('sessionStorage', { getItem: () => null, removeItem() {} })
  const session = { identity: { tenant: { id: 'tenant' }, principal: { id: 'person', kind: 'person' } }, requiresSignIn: false, error: '', refresh: vi.fn(async () => {}) }
  // Keep the real route records and guards; lazy views are outside this routing test.
  const views = (routes: RouteRecordRaw[]): RouteRecordRaw[] => routes.map(route => ({ ...route, ...(route.component ? { component: empty } : {}), ...(route.children ? { children: views(route.children) } : {}) })) as RouteRecordRaw[]
  router = sourceModule<{ router: Router }>('router.ts', {
    'vue-router': { createWebHistory: createMemoryHistory, createRouter: (options: Parameters<typeof createRouter>[0]) => createRouter({ ...options, routes: views(options.routes) }) },
    './lib/brand': { setPageTitle() {} }, './stores/projects': {}, './stores/session': { useSession: () => session }, './stores/workVocabulary': { useWorkVocabulary: () => ({ load: loadVocabulary }) },
    './lib/api': { sessionEnded: {} }, './lib/authz': {}, './lib/toast': {},
    './lib/attachLink': { hasAttachFragment: () => false, announceAttachCode() {} }, './lib/identityScope': {}, './lib/signInReturn': {},
    './lib/knowledge': {}, './components/work/projectNavigation': {}, './lib/ticketPeek': {},
    './views/ProjectsView.vue': { default: empty }, './views/SignInView.vue': { default: empty }, './views/NotFoundView.vue': { default: empty },
  }).router
})
afterEach(() => { vi.unstubAllGlobals() })

const moved = [
  ['work-vocabulary', 'vocabulary'], ['ticket-types', 'vocabulary'],
  ['models', 'models'], ['model-refresh', 'models'], ['estimates', 'agents'], ['silent-sessions', 'agents'], ['agent-activity', 'agents'],
  ['quota-warnings', 'accounts'], ['status-autopilot', 'autopilot'], ['autopilot-projects', 'autopilot'],
  ['autopilot-suggestions', 'autopilot'], ['autopilot-recent', 'autopilot'], ['autopilot-proposals', 'autopilot'], ['autopilot-changes', 'autopilot'], ['members', 'access'],
] as const

it.each(moved)('direct Workspace #%s entry keeps the query and opens %s', async (card, section) => {
  await router.push(`/settings/workspace?source=bookmark#${card}`)
  expect(router.currentRoute.value.fullPath).toBe(`/settings/${section}?source=bookmark${card === 'members' ? '' : `#${card}`}`)
})

it.each(moved)('same-record navigation to Workspace #%s opens %s', async (card, section) => {
  await router.push('/settings/personal')
  const record = router.currentRoute.value.matched[0]
  expect(router.resolve(`/settings/workspace#${card}`).matched[0]).toBe(record)
  await router.push(`/settings/workspace?source=in-app#${card}`)
  expect(router.currentRoute.value.fullPath).toBe(`/settings/${section}?source=in-app${card === 'members' ? '' : `#${card}`}`)
})

it('a hash-only change on Workspace redirects and keeps its query', async () => {
  await router.push('/settings/workspace?source=menu#brand')
  await router.push({ query: router.currentRoute.value.query, hash: '#estimates' })
  expect(router.currentRoute.value.fullPath).toBe('/settings/agents?source=menu#estimates')
})

it('a trailing slash on a legacy Workspace bookmark still redirects', async () => {
  await router.push('/settings/workspace/#estimates')
  expect(router.currentRoute.value.fullPath).toBe('/settings/agents#estimates')
})

it('Back returns to the previous section after following a legacy bookmark', async () => {
  await router.push('/settings/personal')
  await router.push('/settings/workspace#estimates')
  expect(router.currentRoute.value.fullPath).toBe('/settings/agents#estimates')
  const back = new Promise<void>(resolve => {
    const stop = router.afterEach(() => { stop(); resolve() })
  })
  router.back()
  await back
  expect(router.currentRoute.value.fullPath).toBe('/settings/personal')
})

it('current cards and unknown bookmarks keep their section', async () => {
  for (const url of ['/settings/workspace#brand', '/settings/workspace#unknown', '/settings/theme#agents', '/settings/agents#estimates']) {
    await router.push(url)
    expect(router.currentRoute.value.fullPath).toBe(url)
  }
})
it.each(['models', 'model-refresh'])('retired Agents #%s bookmark opens catalog freshness and keeps its context', async anchor => {
  await router.push(`/settings/agents?project_id=project-a#${anchor}`)
  expect(router.currentRoute.value.fullPath).toBe('/settings/models?project_id=project-a#model-refresh')
})

it('retired briefing bookmarks open Agents and keep request links', async () => {
  for (const path of ['/briefing', '/briefing/', '/briefing?needs=a:00000000-0000-4000-8000-000000000001#request']) {
    await router.push('/settings/personal')
    loadVocabulary.mockClear()
    await router.push(path)
    expect(router.currentRoute.value.path).toBe('/agents')
    // The Agents page names leads in the workspace word (AEON-791).
    expect(loadVocabulary).toHaveBeenCalledOnce()
    expect(router.currentRoute.value.meta.title).toBe('Agents')
    expect(router.currentRoute.value.redirectedFrom?.path).toBe(path.split('?')[0])
    if (path.includes('?')) {
      expect(router.currentRoute.value.query.needs).toBe('a:00000000-0000-4000-8000-000000000001')
      expect(router.currentRoute.value.hash).toBe('#request')
    }
  }
})

it('Kinds of work resolves directly to the Settings route and retains definition deep links', async () => {
  await router.push('/settings/kinds#kind-security')
  expect(router.currentRoute.value.params.section).toBe('kinds')
  expect(router.currentRoute.value.meta.title).toBe('Settings')
  expect(router.currentRoute.value.hash).toBe('#kind-security')
})
