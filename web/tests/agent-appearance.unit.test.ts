// SPDX-License-Identifier: AGPL-3.0-only
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

beforeEach(() => vi.resetModules())
afterEach(() => vi.unstubAllGlobals())
const response = (value: unknown) => new Response(JSON.stringify({ value }))

it('shares viewer state preferences across surfaces and preserves independent settings', async () => {
  const writes: unknown[] = []
  const fetch = vi.fn(async (url: string, init: RequestInit) => {
    expect(url).toBe('/api/preferences/agent-state')
    if (init.method === 'PUT') {
      writes.push(JSON.parse(String(init.body)).value)
      return response(writes.at(-1))
    }
    return response({ palette: 'colour-blind', dimInactive: true, inactiveOpacity: 70, yellowMinutes: 5, redMinutes: 12 })
  })
  vi.stubGlobal('fetch', fetch)
  const { useAgentAppearance } = await import('../src/lib/agentAppearance')
  const settings = useAgentAppearance(), cards = useAgentAppearance(), sessions = useAgentAppearance()
  await settings.ready
  expect(fetch).toHaveBeenCalledTimes(1)
  expect(cards.appearance('stopped')).toMatchObject({ '--agent-state-opacity': '0.7', '--agent-state-saturation': '0' })
  expect(cards.appearance('working')['--agent-state-color']).toBe('var(--agent-colour-blind-working)')
  settings.save({ palette: 'monochrome', dimInactive: false })
  expect(sessions.appearance('idle')['--agent-state-opacity']).toBe('1')
  expect(sessions.appearance('working')['--agent-state-saturation']).toBe('0')
  await vi.waitFor(() => expect(writes).toEqual([{ palette: 'monochrome', dimInactive: false, inactiveOpacity: 70, yellowMinutes: 5, redMinutes: 12 }]))
  settings.save({ yellowMinutes: 15 })
  expect(cards.choice.value.redMinutes).toBe(16)
  await vi.waitFor(() => expect(writes).toHaveLength(2))
})

it('reports a failed state save and lets the same choice be retried', async () => {
  let offline = false
  vi.stubGlobal('fetch', vi.fn(async () => {
    if (offline) throw new Error('offline')
    return response(null)
  }))
  const { useAgentAppearance } = await import('../src/lib/agentAppearance')
  const { onPreferenceFailure } = await import('../src/lib/preferences')
  const failed = vi.fn(), stop = onPreferenceFailure(failed)
  const settings = useAgentAppearance()
  await settings.ready
  offline = true
  settings.save({ inactiveOpacity: 40 })
  await vi.waitFor(() => expect(failed).toHaveBeenCalledWith('agent-state'))
  offline = false
  settings.save({})
  await new Promise(resolve => setTimeout(resolve, 20))
  expect(failed).toHaveBeenCalledTimes(1)
  expect(settings.choice.value.inactiveOpacity).toBe(40)
  stop()
})
