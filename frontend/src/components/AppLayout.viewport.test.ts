/*
 * PAIMOS — Your Professional & Personal AI Project OS
 * Copyright (C) 2026 Markus Barta <markus@barta.com>
 */

import { afterEach, describe, expect, it, vi } from 'vitest'
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { createApp, h, nextTick } from 'vue'
import { createPinia, setActivePinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'

import i18n from '@/i18n'
import { useAuthStore } from '@/stores/auth'
import AppLayout from './AppLayout.vue'

vi.mock('@/api/client', async () => {
  const actual = await vi.importActual<typeof import('@/api/client')>('@/api/client')
  return {
    ...actual,
    api: {
      ...actual.api,
      get: vi.fn(async () => ({})),
      post: vi.fn(async () => ({})),
    },
  }
})

vi.mock('@/services/flowHost', async () => {
  const actual = await vi.importActual<typeof import('@/services/flowHost')>('@/services/flowHost')
  return {
    ...actual,
    getFlowHostState: vi.fn(async () => null),
  }
})

vi.mock('@/components/SidebarFooter.vue', () => ({
  default: { template: '<div class="sidebar-footer-stub" />' },
}))
vi.mock('@/components/SidebarTimerPanel.vue', () => ({
  default: { template: '<div class="sidebar-timer-stub" />' },
}))
vi.mock('@/components/SidebarSprintTargets.vue', () => ({
  default: { template: '<div class="sidebar-sprints-stub" />' },
}))
vi.mock('@/components/SidebarRecentProjects.vue', () => ({
  default: { template: '<div class="sidebar-recent-stub" />' },
}))
vi.mock('@/components/AppHeader.vue', () => ({
  default: { template: '<header class="app-header-stub" />' },
}))
vi.mock('@/components/IssuePreviewCard.vue', () => ({
  default: { template: '<div />' },
}))
vi.mock('@/components/GlobalNewIssueModal.vue', () => ({
  default: { template: '<div />' },
}))
vi.mock('@/components/issue/AttachmentLightbox.vue', () => ({
  default: { template: '<div />' },
}))
vi.mock('@/components/SessionExpiredModal.vue', () => ({
  default: { template: '<div />' },
}))

vi.mock('@inspr/flow-shell', () => ({}))
vi.mock('@inspr/flow-shell/assets/inspr-logo.svg', () => ({ default: '/flow-logo.svg' }))

function fakeUser(overrides: Record<string, unknown> = {}) {
  return {
    id: 7,
    username: 'mba',
    role: 'admin',
    nickname: 'Markus',
    first_name: 'Markus',
    last_name: 'B',
    email: 'm@example.com',
    avatar_path: '',
    status: 'active',
    ...overrides,
  }
}

async function mountLayout(setup: (auth: ReturnType<typeof useAuthStore>) => void = () => {}) {
  document.body.innerHTML = '<div id="root"></div>'
  const pinia = createPinia()
  setActivePinia(pinia)
  const auth = useAuthStore()
  auth.user = fakeUser() as never
  auth.checked = true
  auth.allProjects = true
  auth.accessibleProjects = new Map()
  setup(auth)
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: '/projects/:id', component: { render: () => h('div', 'project') } }],
  })
  await router.push('/projects/1')
  await router.isReady()
  const app = createApp({
    render: () =>
      h(AppLayout, null, {
        default: () => h('div', { class: 'fixture-project-body' }, 'Project body'),
      }),
  })
  app.use(pinia)
  app.use(router)
  app.use(i18n)
  app.mount(document.getElementById('root')!)
  await nextTick()
  await nextTick()
  return {
    root: document.getElementById('root')!,
    unmount: () => app.unmount(),
  }
}

describe('AppLayout viewport allocation (PAI-967)', () => {
  afterEach(() => {
    document.body.innerHTML = ''
    vi.restoreAllMocks()
  })

  it('renders the layout grid after visible security banners', async () => {
    const mounted = await mountLayout((auth) => {
      auth.viaDevLogin = true
    })
    const banner = mounted.root.querySelector('.dev-login-banner')
    const layout = mounted.root.querySelector('.layout')
    const main = mounted.root.querySelector('.main')

    expect(banner).not.toBeNull()
    expect(layout).not.toBeNull()
    expect(main).not.toBeNull()
    expect(banner!.compareDocumentPosition(layout!) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    expect(getComputedStyle(main!).height).not.toBe('100vh')

    await mounted.unmount()
  })

  it('starts the layout at the shell top when no banners render', async () => {
    const mounted = await mountLayout((auth) => {
      auth.viaDevLogin = false
      auth.impersonation = null
    })
    const layout = mounted.root.querySelector('.layout')

    expect(mounted.root.querySelector('.dev-login-banner')).toBeNull()
    expect(mounted.root.querySelector('.impersonation-banner')).toBeNull()
    expect(layout).not.toBeNull()
    expect(getComputedStyle(mounted.root.querySelector('.main')!).height).not.toBe('100vh')

    await mounted.unmount()
  })

  it('stacks impersonation and dev-login banners before the layout grid', async () => {
    const mounted = await mountLayout((auth) => {
      auth.viaDevLogin = true
      auth.impersonation = {
        actor: { id: 1, username: 'operator', role: 'super_admin' },
        target: { id: 7, username: 'mba', role: 'admin' },
      } as never
    })
    const layout = mounted.root.querySelector('.layout')
    const banners = mounted.root.querySelectorAll('.dev-login-banner, .impersonation-banner')

    expect(banners.length).toBe(2)
    expect(banners[1]!.compareDocumentPosition(layout!) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()

    await mounted.unmount()
  })

  it('uses a bounded flex viewport chain instead of nested 100vh columns', () => {
    const source = readFileSync(
      join(dirname(fileURLToPath(import.meta.url)), 'AppLayout.vue'),
      'utf8',
    )
    expect(source).toMatch(/\.app-shell\s*\{[\s\S]*height:\s*100vh/)
    expect(source).toMatch(/\.app-shell\s*>\s*\.layout\s*\{[\s\S]*min-height:\s*0/)
    expect(source).not.toMatch(/\.layout\s*\{[\s\S]*min-height:\s*100vh/)
    expect(source).not.toMatch(/\.main\s*\{[\s\S]*height:\s*100vh/)
    expect(source).not.toMatch(/\.sidebar\s*\{[\s\S]*height:\s*100vh/)
    expect(source).toMatch(/data-flow-host-region="footer"/)
  })
})
