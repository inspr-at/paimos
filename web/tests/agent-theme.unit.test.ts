// SPDX-License-Identifier: AGPL-3.0-only
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { reactive } from 'vue'
import type { ActiveTheme, ThemeRecord } from '../src/lib/themes'
const mocks = vi.hoisted(() => ({ session: { identity: null as unknown } }))
vi.mock('../src/stores/session', () => ({ useSession: () => mocks.session }))
vi.mock('../src/lib/authz', () => ({ can: () => true }))
beforeEach(() => { vi.resetModules(); mocks.session = reactive({ identity: null }) })
afterEach(() => vi.unstubAllGlobals())
const chosen = (avatar: ThemeRecord['values']['agents']['avatar'] = 'sprite', palette: ThemeRecord['values']['agents']['palette'] = 'tritan'): ActiveTheme => ({
  theme: { id: 'personal', tenant_id: 'tenant', name: 'Personal', scope: 'personal', owner_principal_id: 'alice', revision: 2, created_at: '', updated_at: '',
    values: { primary: { light: '#0e6f6c', dark: null }, secondary: { light: '#d69b31', dark: null }, recurring_marker: { source: 'secondary', custom: null },
      agents: { avatar, palette, ring: null, size: null, hover: true, dim_inactive: false, inactive_opacity: 72 } } },
  default_theme_id: 'default', selected_theme_id: 'personal', revision: 7, fallback_notice: null,
})
const active = (avatar: ThemeRecord['values']['agents']['avatar'] = 'sprite', palette: ThemeRecord['values']['agents']['palette'] = 'tritan') => new Response(JSON.stringify(chosen(avatar, palette)))
const page = () => new Response(JSON.stringify({ items: [chosen().theme], next_cursor: null }))
const signIn = (id: string) => { mocks.session.identity = { tenant: { id: 'tenant' }, principal: { id, kind: 'person' } } }
function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>(yes => { resolve = yes })
  return { promise, resolve }
}

it('theme appearance overrides legacy preferences everywhere while keeping heartbeat behaviour', async () => {
  vi.stubGlobal('fetch', vi.fn(async (url: string) => url === '/api/me/theme' ? active() : url.startsWith('/api/themes?') ? page() : new Response(JSON.stringify({ value: url.endsWith('agent-state') ? { palette: 'protan', yellowMinutes: 8, redMinutes: 21 } : { style: 'robot-2', ring: 'off', size: 80 } }))))
  const { restoreAgentTheme } = await import('../src/lib/agentTheme')
  const { useAgentIndicator } = await import('../src/lib/agentIndicator')
  const { useAgentAppearance } = await import('../src/lib/agentAppearance')
  const indicator = useAgentIndicator(), states = useAgentAppearance()
  await Promise.all([indicator.ready, states.ready])
  signIn('alice')
  await restoreAgentTheme('tenant/alice')
  expect(indicator.choice.value).toEqual({ style: 'sprite', hovering: true })
  expect(states.choice.value).toMatchObject({ palette: 'tritan', dimInactive: false, inactiveOpacity: 72, yellowMinutes: 8, redMinutes: 21 })
  expect(states.appearance('waiting')['--agent-state-color']).toBe('var(--agent-tritan-waiting)')
  expect(states.appearance('idle')['--agent-state-opacity']).toBe('1')
})

it('late theme reads cannot overwrite a newer confirmed theme or a different person', async () => {
  let response = deferred<Response>(), requested = deferred<void>()
  vi.stubGlobal('fetch', vi.fn((url: string) => {
    if (url.startsWith('/api/themes?')) return Promise.resolve(page())
    expect(url).toBe('/api/me/theme'); requested.resolve(); return response.promise.then(answer => answer.clone())
  }))
  const { agentTheme, restoreAgentTheme, resetAgentTheme } = await import('../src/lib/agentTheme')
  signIn('alice')
  const stale = restoreAgentTheme('tenant/alice')
  await requested.promise
  const older = response
  response = deferred<Response>(); requested = deferred<void>()
  const fresh = restoreAgentTheme('tenant/alice')
  await requested.promise
  response.resolve(active('quill')); await fresh
  older.resolve(active('robot-5')); await stale
  expect(agentTheme.value?.avatar).toBe('quill')
  response = deferred<Response>(); requested = deferred<void>()
  const oldPerson = restoreAgentTheme('tenant/alice')
  await requested.promise
  mocks.session.identity = null; resetAgentTheme()
  response.resolve(active('robot-4')); await oldPerson
  expect(agentTheme.value).toBeNull()
  response = deferred<Response>(); requested = deferred<void>()
  // Bind the neutral runtime before the editor starts Bob's identity load.
  resetAgentTheme('tenant/bob'); signIn('bob')
  const bob = restoreAgentTheme('tenant/bob')
  await requested.promise
  response.resolve(active('orbit')); await bob
  expect(agentTheme.value?.avatar).toBe('orbit')
})

it('a failed load after an identity reset uses defaults, never the legacy appearance cache', async () => {
  vi.stubGlobal('fetch', vi.fn(async (url: string) => url === '/api/me/theme' ? new Response('{}', { status: 500 }) : url.startsWith('/api/themes?') ? page() : new Response(JSON.stringify({ value: { style: 'sprite', hovering: true, palette: 'monochrome' } }))))
  const { useAgentIndicator } = await import('../src/lib/agentIndicator')
  const { useAgentAppearance } = await import('../src/lib/agentAppearance')
  const indicator = useAgentIndicator(), states = useAgentAppearance()
  await Promise.all([indicator.ready, states.ready])
  expect(indicator.choice.value.style).toBe('sprite')
  const { restoreAgentTheme } = await import('../src/lib/agentTheme')
  signIn('bob')
  await restoreAgentTheme('tenant/bob')
  expect(indicator.choice.value).toEqual({ style: 'robot-1', hovering: false })
  expect(states.choice.value.palette).toBe('standard')
})
