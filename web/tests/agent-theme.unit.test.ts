// SPDX-License-Identifier: AGPL-3.0-only
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
beforeEach(() => vi.resetModules())
afterEach(() => vi.unstubAllGlobals())
const active = (avatar = 'sprite', palette = 'tritan') => new Response(JSON.stringify({ theme: { values: { agents: { avatar, palette, ring: null, size: null, hover: true, dim_inactive: false, inactive_opacity: 72 } } } }))

it('theme appearance overrides legacy preferences everywhere while keeping heartbeat behaviour', async () => {
  vi.stubGlobal('fetch', vi.fn(async (url: string) => url === '/api/me/theme' ? active() : new Response(JSON.stringify({ value: url.endsWith('agent-state') ? { palette: 'protan', yellowMinutes: 8, redMinutes: 21 } : { style: 'robot-2', ring: 'off', size: 80 } }))))
  const { restoreAgentTheme } = await import('../src/lib/agentTheme')
  const { useAgentIndicator } = await import('../src/lib/agentIndicator')
  const { useAgentAppearance } = await import('../src/lib/agentAppearance')
  const indicator = useAgentIndicator(), states = useAgentAppearance()
  await Promise.all([indicator.ready, states.ready])
  await restoreAgentTheme('tenant/alice')
  expect(indicator.choice.value).toEqual({ style: 'sprite', hovering: true })
  expect(states.choice.value).toMatchObject({ palette: 'tritan', dimInactive: false, inactiveOpacity: 72, yellowMinutes: 8, redMinutes: 21 })
  expect(states.appearance('waiting')['--agent-state-color']).toBe('var(--agent-tritan-waiting)')
  expect(states.appearance('idle')['--agent-state-opacity']).toBe('1')
})

it('late theme reads cannot overwrite a newly saved theme or a different person', async () => {
  const answers: ((value: Response) => void)[] = []
  vi.stubGlobal('fetch', vi.fn(() => new Promise<Response>(resolve => answers.push(resolve))))
  const { agentTheme, restoreAgentTheme, installAgentTheme, resetAgentTheme, defaultAgentTheme } = await import('../src/lib/agentTheme')
  const stale = restoreAgentTheme('tenant/alice')
  installAgentTheme({ ...defaultAgentTheme, avatar: 'quill' })
  answers.shift()!(active('robot-5')); await stale
  expect(agentTheme.value?.avatar).toBe('quill')
  const oldPerson = restoreAgentTheme('tenant/alice')
  resetAgentTheme('tenant/bob')
  answers.shift()!(active('robot-4')); await oldPerson
  expect(agentTheme.value).toBeNull()
  const bob = restoreAgentTheme('tenant/bob')
  answers.shift()!(active('orbit')); await bob
  expect(agentTheme.value?.avatar).toBe('orbit')
})

it('a failed load after an identity reset uses defaults, never the legacy appearance cache', async () => {
  vi.stubGlobal('fetch', vi.fn(async (url: string) => url === '/api/me/theme' ? new Response('{}', { status: 500 }) : new Response(JSON.stringify({ value: { style: 'sprite', hovering: true, palette: 'monochrome' } }))))
  const { useAgentIndicator } = await import('../src/lib/agentIndicator')
  const { useAgentAppearance } = await import('../src/lib/agentAppearance')
  const indicator = useAgentIndicator(), states = useAgentAppearance()
  await Promise.all([indicator.ready, states.ready])
  expect(indicator.choice.value.style).toBe('sprite')
  const { restoreAgentTheme } = await import('../src/lib/agentTheme')
  await restoreAgentTheme('tenant/bob')
  expect(indicator.choice.value).toEqual({ style: 'robot-1', hovering: false })
  expect(states.choice.value.palette).toBe('standard')
})
