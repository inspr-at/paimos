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
import { createApp, defineComponent, h, nextTick, type Ref } from 'vue'
import { createPinia, setActivePinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'

import App from './App.vue'
import { useAuthStore } from '@/stores/auth'

const changeStreamGates = vi.hoisted(() => [] as Ref<boolean>[])
const appLayoutModule = vi.hoisted(() => ({ loads: 0 }))

vi.mock('@/api/client', () => ({ announceSessionExpired: vi.fn() }))

vi.mock('@/stores/auth', async () => {
  const { defineStore } = await import('pinia')
  const { ref } = await import('vue')
  return {
    useAuthStore: defineStore('app-shell-test-auth', () => ({
      checked: ref(false),
      user: ref<{ role: string } | null>(null),
      fetchMe: vi.fn(async () => undefined),
    })),
  }
})

vi.mock('@/stores/undo', async () => {
  const { defineStore } = await import('pinia')
  const { ref } = await import('vue')
  return {
    useUndoStore: defineStore('app-shell-test-undo', () => ({
      conflict: ref(null),
      resolving: ref(false),
      clearConflict: vi.fn(),
      resolveConflict: vi.fn(),
    })),
  }
})

vi.mock('@/composables/useChangesStream', () => ({
  useChangesStream: (enabled: Ref<boolean>) => changeStreamGates.push(enabled),
}))

vi.mock('@/components/LoadingText.vue', async () => {
  const { defineComponent, h } = await import('vue')
  return {
    default: defineComponent({
      name: 'LoadingTextStub',
      setup: () => () => h('p', { 'data-loading': '' }, 'Loading'),
    }),
  }
})

vi.mock('@/components/AppLayout.vue', async () => {
  appLayoutModule.loads += 1
  const { defineComponent, h } = await import('vue')
  return {
    default: defineComponent({
      name: 'AppLayoutStub',
      setup: (_props, { slots }) => () => h('section', { 'data-shell': 'standard' }, slots.default?.()),
    }),
  }
})

vi.mock('@/components/v6/Paimos6Layout.vue', async () => {
  const { defineComponent, h } = await import('vue')
  return {
    default: defineComponent({
      name: 'Paimos6LayoutStub',
      setup: (_props, { slots }) => () => h('section', { 'data-shell': 'v6' }, slots.default?.()),
    }),
  }
})

vi.mock('@/components/AgentModeLayout.vue', async () => {
  const { defineComponent, h } = await import('vue')
  return {
    default: defineComponent({
      setup: (_props, { slots }) => () => h('section', { 'data-shell': 'agent' }, slots.default?.()),
    }),
  }
})

vi.mock('@/components/PortalLayout.vue', async () => {
  const { defineComponent, h } = await import('vue')
  return {
    default: defineComponent({
      setup: (_props, { slots }) => () => h('section', { 'data-shell': 'portal' }, slots.default?.()),
    }),
  }
})

vi.mock('@/components/AppConfirmDialog.vue', async () => {
  const { defineComponent } = await import('vue')
  return { default: defineComponent({ setup: () => () => null }) }
})

vi.mock('@/components/undo/UndoToast.vue', async () => {
  const { defineComponent, h } = await import('vue')
  return { default: defineComponent({ setup: () => () => h('div', { 'data-chrome': 'UndoToast' }) }) }
})

vi.mock('@/components/undo/UndoActivityPanel.vue', async () => {
  const { defineComponent, h } = await import('vue')
  return { default: defineComponent({ setup: () => () => h('div', { 'data-chrome': 'UndoActivityPanel' }) }) }
})

vi.mock('@/components/undo/UndoConflictModal.vue', async () => {
  const { defineComponent, h } = await import('vue')
  return { default: defineComponent({ setup: () => () => h('div', { 'data-chrome': 'UndoConflictModal' }) }) }
})

describe('App route shell isolation', () => {
  afterEach(() => {
    document.body.innerHTML = ''
    changeStreamGates.length = 0
    vi.clearAllMocks()
  })

  it('defers the standard shell module through router start and v6, then loads it for a matched standard route', async () => {
    document.body.innerHTML = '<div id="root"></div>'
    const pinia = createPinia()
    setActivePinia(pinia)
    const auth = useAuthStore(pinia)
    auth.checked = true
    auth.user = { role: 'admin' } as never

    const StandardView = defineComponent({
      setup: () => () => h('div', { 'data-view': 'standard' }),
    })
    const V6View = defineComponent({
      setup: () => () => h('div', { 'data-view': 'v6' }),
    })
    const history = createMemoryHistory()
    history.push('/dev/paimos-6')
    const router = createRouter({
      history,
      routes: [
        { path: '/', component: StandardView },
        { path: '/dev/paimos-6', component: V6View, meta: { shell: 'v6' } },
      ],
    })

    let releaseInitialNavigation!: () => void
    let holdInitialNavigation = true
    router.beforeEach(() => {
      if (!holdInitialNavigation) return true
      holdInitialNavigation = false
      return new Promise<boolean>((resolve) => {
        releaseInitialNavigation = () => resolve(true)
      })
    })
    const app = createApp(App)
    app.use(pinia)
    app.use(router)
    const initialNavigation = router.isReady()
    app.mount(document.getElementById('root')!)
    await nextTick()
    await vi.waitFor(() => expect(releaseInitialNavigation).toBeTypeOf('function'))

    const root = document.getElementById('root')!
    expect(router.currentRoute.value.matched).toHaveLength(0)
    expect(root.querySelector('[data-loading]')).not.toBeNull()
    expect(root.querySelector('[data-shell]')).toBeNull()
    expect(root.querySelector('[data-chrome*="Undo"]')).toBeNull()
    expect(appLayoutModule.loads).toBe(0)
    expect(changeStreamGates).toHaveLength(1)
    expect(changeStreamGates[0].value).toBe(false)

    releaseInitialNavigation()
    await initialNavigation
    await router.isReady()
    await vi.waitFor(() => {
      expect(
        root.querySelector('[data-shell="v6"] [data-view="v6"]'),
        `${router.currentRoute.value.fullPath}: ${root.innerHTML}`,
      ).not.toBeNull()
    })
    expect(root.querySelector('[data-shell="standard"]')).toBeNull()
    expect(root.querySelector('[data-chrome*="Undo"]')).toBeNull()
    expect(appLayoutModule.loads).toBe(0)
    expect(changeStreamGates[0].value).toBe(false)

    await router.push('/')
    await vi.waitFor(() => {
      expect(root.querySelector('[data-shell="standard"] [data-view="standard"]')).not.toBeNull()
    })
    expect(appLayoutModule.loads).toBe(1)
    expect(root.querySelector('[data-chrome="UndoToast"]')).not.toBeNull()
    expect(changeStreamGates[0].value).toBe(true)

    app.unmount()
  })
})
