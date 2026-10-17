// SPDX-License-Identifier: AGPL-3.0-only
// Risks (AEON-1441): the START capability stays in the address bar, history or
// a return path; a malformed one is held; or a person who first has to sign
// in loses the handoff on the way through OIDC.
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { createMemoryHistory, createRouter, type Router, type RouteRecordRaw } from 'vue-router'
import { defineComponent } from 'vue'
import { sourceModule } from './record-source'
import { settleConfirm } from '../src/lib/confirm'
import { displayLanguage } from '../src/lib/displayLanguage'

const empty = defineComponent({ render: () => null })
const CAPABILITY = 'Q2FwYWJpbGl0eS1mb3ItQUVPTi0xNDQxLXRlc3RzXzA'
type Draft = { heldDraft: () => { draft: string; lang: string | null } | null }
type Return = { rememberSignInReturn: (path: string) => void }
let storage: Map<string, string>, session: { identity: unknown; requiresSignIn: boolean; error: string; refresh: () => Promise<void> }
let draft: Draft, signInReturn: Return

function app(): Router {
  const views = (routes: RouteRecordRaw[]): RouteRecordRaw[] => routes.map(route => ({ ...route, ...(route.component ? { component: empty } : {}), ...(route.children ? { children: views(route.children) } : {}) })) as RouteRecordRaw[]
  return sourceModule<{ router: Router }>('router.ts', {
    './lib/projectSettings': sourceModule('lib/projectSettings.ts', {}),
    'vue-router': { createWebHistory: createMemoryHistory, createRouter: (options: Parameters<typeof createRouter>[0]) => createRouter({ ...options, routes: views(options.routes) }) },
    './lib/brand': { setPageTitle() {} }, './stores/projects': {}, './stores/session': { useSession: () => session }, './stores/workVocabulary': { useWorkVocabulary: () => ({ load: async () => {} }) },
    './lib/api': { sessionEnded: {} }, './lib/authz': {}, './lib/toast': {}, './lib/confirm': { settleConfirm },
    './lib/attachLink': { hasAttachFragment: () => false, announceAttachCode() {}, dropAttachCode() {} }, './lib/importDraft': draft, './lib/identityScope': {}, './lib/signInReturn': signInReturn,
    './lib/knowledge': {}, './components/work/projectNavigation': {}, './lib/ticketPeek': {},
    './views/ProjectsView.vue': { default: empty }, './views/SignInView.vue': { default: empty }, './views/NotFoundView.vue': { default: empty },
  }).router
}
beforeEach(() => {
  storage = new Map()
  vi.stubGlobal('sessionStorage', { getItem: (k: string) => storage.get(k) ?? null, setItem: (k: string, v: string) => { storage.set(k, v) }, removeItem: (k: string) => { storage.delete(k) } })
  session = { identity: { tenant: { id: 'tenant' }, principal: { id: 'person', kind: 'person' } }, requiresSignIn: false, error: '', refresh: async () => {} }
  draft = sourceModule<Draft>('lib/importDraft.ts', { './api.ts': {}, './displayLanguage.ts': { displayLanguage } })
  signInReturn = sourceModule<Return>('lib/signInReturn.ts', { './attachLink.ts': { stripAttachCode: (path: string) => path } })
})
afterEach(() => { vi.unstubAllGlobals() })

it('moves the capability out of the address into this tab before anything else', async () => {
  const router = app()
  await router.push(`/new-project#draft=${CAPABILITY}&lang=de`)
  expect(router.currentRoute.value.fullPath).toBe('/new-project')
  expect(draft.heldDraft()).toEqual({ draft: CAPABILITY, lang: 'de' })
  await router.push(`/new-project?draft=${CAPABILITY}`)
  expect(router.currentRoute.value.fullPath).toBe('/new-project')
  expect(draft.heldDraft()).toEqual({ draft: CAPABILITY, lang: null })
})

it('holds nothing for a malformed capability, and still opens the page', async () => {
  const router = app()
  await router.push('/new-project#draft=not-a-capability&lang=de')
  expect(router.currentRoute.value.fullPath).toBe('/new-project')
  expect(draft.heldDraft()).toBeNull()
})

it('keeps the handoff through sign-in: the continuation is the bare assistant', async () => {
  session.identity = null
  let router = app()
  await router.push(`/new-project#draft=${CAPABILITY}&lang=en`)
  expect(router.currentRoute.value.fullPath).toBe('/signin?return=/new-project')
  expect(router.currentRoute.value.fullPath).not.toContain(CAPABILITY)
  // SignInView stores its return before OIDC; the callback lands on / in a fresh page.
  signInReturn.rememberSignInReturn(String(router.currentRoute.value.query.return))
  session.identity = { tenant: { id: 'tenant' }, principal: { id: 'person', kind: 'person' } }
  router = app()
  await router.push('/')
  expect(router.currentRoute.value.fullPath).toBe('/new-project')
  expect(draft.heldDraft()).toEqual({ draft: CAPABILITY, lang: 'en' })
})
