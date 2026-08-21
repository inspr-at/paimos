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

import { afterEach, describe, expect, it } from 'vitest'
import { createApp, h, nextTick } from 'vue'
import { createPinia, setActivePinia } from 'pinia'
import { RouterView, createMemoryHistory, createRouter } from 'vue-router'

import i18n from '@/i18n'
import AgentModeDevReference from './AgentModeDevReference.vue'

async function settle() {
  await new Promise((r) => setTimeout(r, 320))
  for (let i = 0; i < 64; i += 1) {
    await Promise.resolve()
    await nextTick()
  }
}

describe('AgentModeDevReference (DEV-only fixture route)', () => {
  afterEach(() => {
    document.body.innerHTML = ''
  })

  it('renders fixture-backed Agent Mode and labels the data source honestly', async () => {
    document.body.innerHTML = '<div id="app-header-left"></div><div id="app-header-right"></div><div id="root"></div>'
    const pinia = createPinia()
    setActivePinia(pinia)
    const router = createRouter({
      history: createMemoryHistory(),
      routes: [{ path: '/dev/agent-mode', component: AgentModeDevReference }],
    })
    await router.push('/dev/agent-mode?n=10')
    await router.isReady()
    const app = createApp({ render: () => h(RouterView) })
    app.use(pinia)
    app.use(router)
    app.use(i18n)
    app.mount(document.getElementById('root')!)
    await settle()
    const root = document.getElementById('root')!
    expect(root.querySelectorAll('.am-lanes .am-card')).toHaveLength(9)
    expect(root.querySelectorAll('.am-selected-above')).toHaveLength(1)
    expect(root.querySelectorAll('[data-selected="true"]')).toHaveLength(1)
    expect(document.getElementById('app-header-right')!.textContent).toContain('Fixture data')
    app.unmount()
  })
})
