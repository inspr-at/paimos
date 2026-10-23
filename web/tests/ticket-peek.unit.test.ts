// SPDX-License-Identifier: AGPL-3.0-only
import { effectScope, nextTick, provide, reactive } from 'vue'
import { afterEach, expect, it, vi } from 'vitest'
import { provideTicketPeek, type TicketPeek } from '../src/lib/ticketPeek'

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

it('P03: peek routing preserves Agents, knowledge and graph context and returns focus to its opener', async () => {
  for (const path of ['/agents/session-a', '/p/AEON/knowledge', '/p/AEON/tickets']) {
    const route = reactive({ path, params: {} as Record<string, string>, query: { view: 'graph', q: 'kept' } as Record<string, string>, hash: '#kept' })
    mocked.route = route
    const push = vi.fn(async (location: { query: Record<string, string> }) => { route.query = location.query })
    const replace = vi.fn(async (location: { query: Record<string, string> }) => { route.query = location.query })
    Object.assign(mocked.router, { push, replace })
    const focus = vi.fn()
    class Element { isConnected = true; focus = focus }
    const opener = new Element()
    vi.stubGlobal('HTMLElement', Element)
    vi.stubGlobal('document', { activeElement: opener, querySelector: () => null })
    vi.stubGlobal('window', { history: { state: {} }, addEventListener() {}, removeEventListener() {} })
    const scope = effectScope()
    try {
      const peek = scope.run(() => provideTicketPeek())!
      const supplied = vi.mocked(provide).mock.calls.at(-1)![1] as TicketPeek
      supplied.open(' pharos-11 ', opener as unknown as HTMLElement)
      await nextTick()
      expect(push).toHaveBeenCalledExactlyOnceWith({ path, query: { view: 'graph', q: 'kept', peek: 'PHAROS-11' }, hash: '#kept' })
      expect(peek.openKey.value).toBe('PHAROS-11')
      await peek.close()
      expect(replace).toHaveBeenCalledExactlyOnceWith({ path, query: { view: 'graph', q: 'kept' }, hash: '#kept' })
      expect(peek.openKey.value).toBeNull()
      expect(focus).toHaveBeenCalledExactlyOnceWith({ preventScroll: true })
    } finally { scope.stop() }
  }
})
