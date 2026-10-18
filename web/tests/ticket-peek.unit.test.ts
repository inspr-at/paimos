// SPDX-License-Identifier: AGPL-3.0-only
import { effectScope, nextTick, reactive } from 'vue'
import { afterEach, expect, it, vi } from 'vitest'
import { provideTicketPeek } from '../src/lib/ticketPeek'

const mocked = vi.hoisted(() => ({ route: null as unknown, router: { push: vi.fn() } }))
vi.mock('vue-router', () => ({ useRoute: () => mocked.route, useRouter: () => mocked.router }))
vi.mock('../src/lib/ticketLinks', () => ({ normalKey: (key: string) => key.trim().toUpperCase() }))
vi.mock('vue', async importOriginal => ({ ...await importOriginal<typeof import('vue')>(), provide: vi.fn() }))
afterEach(() => vi.unstubAllGlobals())

it('the route owns the panel even when a stale peek query survives navigation', async () => {
  const route = reactive({ path: '/p/AEON/AEON-650', params: { projectKey: 'AEON', ticketKey: 'AEON-650' }, query: { peek: 'GUI-18' } })
  mocked.route = route
  vi.stubGlobal('window', { addEventListener: vi.fn(), removeEventListener: vi.fn() })
  const scope = effectScope()
  try {
    const peek = scope.run(() => provideTicketPeek())!
    expect(peek.openKey.value).toBeNull()
    route.params.ticketKey = ''
    await nextTick()
    expect(peek.openKey.value).toBe('GUI-18')
    route.params.ticketKey = 'AEON-650'
    await nextTick()
    expect(peek.openKey.value).toBeNull()
  } finally { scope.stop() }
})
