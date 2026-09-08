/*
 * PAIMOS — Your Professional & Personal AI Project OS
 * Copyright (C) 2026 Markus Barta <markus@barta.com>
 */

import { afterEach, describe, expect, it, vi } from 'vitest'
import { h, nextTick } from 'vue'

import { api } from '@/api/client'
import { mountComponent } from '@/components/ai/testMount'

const getFlowHostState = vi.fn()

const { route, router } = vi.hoisted(() => ({
  route: { query: { project: '1' } as Record<string, string | undefined>, fullPath: '/?project=1' },
  router: {
    replace: vi.fn().mockResolvedValue(undefined),
    push: vi.fn().mockResolvedValue(undefined),
  },
}))

vi.mock('vue-router', async () => {
  const actual = await vi.importActual<typeof import('vue-router')>('vue-router')
  return {
    ...actual,
    RouterLink: { props: ['to'], template: '<a><slot /></a>' },
    useRoute: () => route,
    useRouter: () => router,
  }
})

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({
    user: { id: 1, username: 'fixture-user', role: 'member', status: 'active' },
    allProjects: true,
    accessibleProjects: new Map(),
  }),
}))

vi.mock('@/services/flowHost', async () => {
  const actual = await vi.importActual<typeof import('@/services/flowHost')>('@/services/flowHost')
  return {
    ...actual,
    getFlowHostState: (...args: unknown[]) => getFlowHostState(...args),
  }
})

vi.mock('@inspr/flow-shell', () => ({}))
vi.mock('@inspr/flow-shell/assets/inspr-logo.svg', () => ({ default: '/flow-logo.svg' }))

import Paimos6Layout from './Paimos6Layout.vue'

function usableFlowState() {
  return {
    evaluatedAt: '2026-09-08T08:41:00.000Z',
    header: { appName: 'Paimos', version: 'ignore-me', projectName: 'Stream' },
    delivery: { status: 'draft', stageEvidence: ['unknown', 'unknown', 'unknown', 'unknown'] },
    prerequisites: {},
    progress: {},
    identityContext: {
      contract_version: 'inspr.flow-identity/0.1-draft',
      host_id: 'paimos',
      principal_kind: 'local_host',
      principal_ref: 'paimos:prin-abc',
      binding_ref: 'paimos:bind-abc',
      organization_ref: null,
      project_ref: 'paimos:proj-abc',
      actor_kind: 'human',
      issued_at: '2026-09-08T08:41:00.000Z',
      expires_at: '2026-09-08T16:41:00.000Z',
      fresh_until: '2026-09-08T08:50:00.000Z',
      context_revision: 'paimos:ctxrev-abc',
      display: { fixture_label: 'Host-issued Paimos session context. Not live identity.' },
    },
  }
}

function expectFooterAfterShellContent(root: ParentNode) {
  const appContent = root.querySelector('.habitat-app-content')
  expect(appContent).not.toBeNull()

  const footer = appContent!.querySelector('footer.habitat-footer')
  const content = appContent!.querySelector('.p6-shell-content')
  expect(footer).not.toBeNull()
  expect(content).not.toBeNull()
  expect(content!.compareDocumentPosition(footer!) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
}

function expectFooterInsideBoundedFlowShell(root: ParentNode) {
  expectFooterAfterShellContent(root)
  const appContent = root.querySelector('.habitat-app-content')!
  expect(appContent.querySelector(':scope > footer.habitat-footer')).toBeNull()

  const shell = appContent.querySelector('inspr-flow-shell')
  const footer = appContent.querySelector('footer.habitat-footer')
  expect(shell).not.toBeNull()
  expect(shell!.contains(footer)).toBe(true)
    expect(shell!.getAttribute('layout-mode')).toBe('bounded')
    expect(shell!.getAttribute('content-layout')).toBeNull()
    expect(shell!.querySelector('.paimos-flow-body')!.contains(footer)).toBe(true)
}

describe('Paimos6Layout Flow footer placement (PAI-967)', () => {
  afterEach(async () => {
    document.body.innerHTML = ''
    vi.restoreAllMocks()
    getFlowHostState.mockReset()
    route.query = { project: '1' }
    route.fullPath = '/?project=1'
    router.replace.mockReset().mockResolvedValue(undefined)
    router.push.mockReset().mockResolvedValue(undefined)
  })

  it('keeps habitat footer inside FlowHost when Flow is inactive', async () => {
    route.query = {}
    route.fullPath = '/'
    getFlowHostState.mockResolvedValue(null)
    vi.spyOn(api, 'get').mockResolvedValue({
      schema_version: 1,
      default_shortcut: 'Mod+KeyK',
      instance_shortcut: null,
      user_shortcut: null,
      effective_shortcut: 'Mod+KeyK',
      source: 'default',
    } as never)

    const mounted = await mountComponent(Paimos6Layout, {}, { default: () => h('main', 'home') })
    await nextTick()

    expectFooterAfterShellContent(mounted.el)
    expect(mounted.el.querySelector('inspr-flow-shell')).toBeNull()
    await mounted.unmount()
  })

  it('keeps habitat footer inside bounded Flow shell body when active', async () => {
    getFlowHostState.mockResolvedValue(usableFlowState())
    vi.spyOn(api, 'get').mockResolvedValue({
      schema_version: 1,
      default_shortcut: 'Mod+KeyK',
      instance_shortcut: null,
      user_shortcut: null,
      effective_shortcut: 'Mod+KeyK',
      source: 'default',
    } as never)

    const mounted = await mountComponent(Paimos6Layout, {}, { default: () => h('main', 'home') })
    await vi.waitFor(() => expect(getFlowHostState).toHaveBeenCalledWith(1))

    expectFooterInsideBoundedFlowShell(mounted.el)
    await mounted.unmount()
  })
})
