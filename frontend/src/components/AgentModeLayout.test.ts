/*
 * PAIMOS — Your Professional & Personal AI Project OS
 * Copyright (C) 2026 Markus Barta <markus@barta.com>
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU Affero General Public License as
 * published by the Free Software Foundation, version 3.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
 * GNU Affero General Public License for more details.
 *
 * You should have received a copy of the GNU Affero General Public
 * License along with this program. If not, see <https://www.gnu.org/licenses/>.
 */

import { afterEach, describe, expect, it, vi } from 'vitest'
import { createApp, h, nextTick } from 'vue'
import { createPinia, setActivePinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'

import i18n from '@/i18n'
import { useAuthStore } from '@/stores/auth'
import AgentModeLayout from './AgentModeLayout.vue'

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
  setup(auth)
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/', component: { render: () => h('div') } },
      { path: '/settings', component: { render: () => h('div') } },
      { path: '/agent-mode', component: { render: () => h('div') }, meta: { shell: 'agent' } },
    ],
  })
  await router.push('/agent-mode')
  await router.isReady()
  const app = createApp({ render: () => h(AgentModeLayout, null, { default: () => h('div', { class: 'slot-content' }, 'canvas') }) })
  app.use(pinia)
  app.use(router)
  app.use(i18n)
  app.mount(document.getElementById('root')!)
  await nextTick()
  await nextTick()
  return { root: document.getElementById('root')!, auth, router, unmount: () => app.unmount() }
}

describe('AgentModeLayout (PAI-805 reduced shell)', () => {
  afterEach(() => {
    document.body.innerHTML = ''
    vi.clearAllMocks()
  })

  it('keeps only the logo rail (top-left anchor), an obvious way back, logout bottom-left and settings top-right', async () => {
    const { root, unmount } = await mountLayout()
    const rail = root.querySelector<HTMLElement>('.aml-rail')!
    expect(rail).not.toBeNull()
    expect(rail.getAttribute('aria-label')).toBe('Agent Mode controls')
    // Top-left: brand anchor + explicit exit, both back to the standard app.
    const top = rail.querySelector<HTMLElement>('.aml-rail-top')!
    const brand = top.querySelector<HTMLAnchorElement>('a.aml-brand')!
    expect(brand.getAttribute('href')).toBe('/')
    expect(brand.querySelector('img')).not.toBeNull()
    const exit = top.querySelector<HTMLAnchorElement>('a.aml-exit')!
    expect(exit.getAttribute('href')).toBe('/')
    expect(exit.textContent).toContain('Exit')
    expect(exit.textContent).toContain('Back to Paimos')
    // Bottom-left: avatar (profile) + logout.
    const bottom = rail.querySelector<HTMLElement>('.aml-rail-bottom')!
    expect(bottom.querySelector('.aml-avatar')?.getAttribute('aria-label')).toBe('Signed in as Markus')
    expect(bottom.querySelector<HTMLButtonElement>('button.aml-logout')?.getAttribute('aria-label')).toBe('Log out')
    // Top-right: settings, after the view's header slot.
    const topRight = root.querySelector<HTMLElement>('.aml-top-right')!
    const settings = topRight.querySelector<HTMLAnchorElement>('a.aml-settings')!
    expect(settings.getAttribute('href')).toBe('/settings')
    expect(settings.getAttribute('aria-label')).toBe('Settings')
    expect(topRight.lastElementChild).toBe(settings)
    // Header teleport targets for the view (title · live chip · lever).
    expect(root.querySelector('#app-header-left')).not.toBeNull()
    expect(root.querySelector('#app-header-right')).not.toBeNull()
    // The route component renders in the content area.
    expect(root.querySelector('.aml-content .slot-content')?.textContent).toBe('canvas')
    unmount()
  })

  it('carries none of the ordinary AppLayout chrome', async () => {
    const { root, unmount } = await mountLayout()
    const text = root.textContent ?? ''
    for (const label of ['Dashboard', 'Projects', 'Customers', 'Issues', 'Reporting', 'New Issue', 'Undo', 'Sprint', 'Voice Intake', 'Integrations']) {
      expect(text, `unexpected "${label}" in the Agent Mode shell`).not.toContain(label)
    }
    expect(root.querySelector('nav')).toBeNull()
    expect(root.querySelector('.sidebar')).toBeNull()
    expect(root.querySelector('.app-header')).toBeNull()
    expect(root.querySelector('input[type="search"]')).toBeNull()
    expect(root.querySelector('.new-issue-btn')).toBeNull()
    expect(root.querySelectorAll('a').length).toBe(4) // brand, exit, avatar, settings
    unmount()
  })

  it('logs out from the rail', async () => {
    const { root, auth, unmount } = await mountLayout()
    const logout = vi.spyOn(auth, 'logout').mockResolvedValue(undefined as never)
    root.querySelector<HTMLButtonElement>('button.aml-logout')!.click()
    expect(logout).toHaveBeenCalledTimes(1)
    unmount()
  })

  it('preserves the auth / session / impersonation / security banners', async () => {
    const { root, unmount } = await mountLayout((auth) => {
      auth.viaDevLogin = true
      auth.impersonation = {
        active: true,
        actor: { id: 1, username: 'root', role: 'super_admin' },
        target: { id: 7, username: 'mba', role: 'admin' },
      } as never
      auth.totpChecked = true
      auth.totpEnabled = false
      auth.suppressSecurityNags = false
      auth.ssoSession = false
    })
    expect(root.querySelector('.dev-login-banner')?.textContent).toContain('DEV LOGIN ACTIVE')
    expect(root.querySelector('.impersonation-banner')?.textContent).toContain('Acting as')
    const nag = root.querySelector<HTMLElement>('.aml-totp-warning')!
    expect(nag.getAttribute('role')).toBe('alert')
    expect(nag.textContent).toContain('Two-factor authentication is not enabled.')
    expect(nag.textContent).toContain('Set it up now')
    unmount()
  })

  it('hides the 2FA nag for SSO sessions and enabled TOTP (same rule as AppLayout)', async () => {
    const sso = await mountLayout((auth) => {
      auth.totpChecked = true
      auth.totpEnabled = false
      auth.ssoSession = true
    })
    expect(sso.root.querySelector('.aml-totp-warning')).toBeNull()
    sso.unmount()
    const enabled = await mountLayout((auth) => {
      auth.totpChecked = true
      auth.totpEnabled = true
    })
    expect(enabled.root.querySelector('.aml-totp-warning')).toBeNull()
    enabled.unmount()
  })
})
