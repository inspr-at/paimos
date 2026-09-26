// SPDX-License-Identifier: AGPL-3.0-only
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

beforeEach(() => vi.resetModules())
afterEach(() => vi.unstubAllGlobals())
const response = (value: unknown) => new Response(JSON.stringify({ value }))

it.each([null, undefined, {}, [], 'robot-5', { style: 'unknown', hovering: 'true' }])('keeps Calm and hovering off for missing or malformed data: %j', async saved => {
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(response(saved)))
  const { useAgentIndicator } = await import('../src/lib/agentIndicator')
  const pref = useAgentIndicator()
  expect(pref.choice.value).toEqual({ style: 'robot-1', hovering: false })
  await pref.ready
  expect(pref.choice.value).toEqual({ style: 'robot-1', hovering: false })
})

it('loads one account preference for every consumer and preserves independent fields on save', async () => {
  const writes: unknown[] = []
  const fetch = vi.fn(async (url: string, init: RequestInit) => {
    expect(url).toBe('/api/preferences/agent-indicator')
    if (init.method === 'PUT') { writes.push(JSON.parse(String(init.body)).value); return response(writes.at(-1)) }
    return response({ style: 'robot-5', hovering: true })
  })
  vi.stubGlobal('fetch', fetch)
  const { useAgentIndicator } = await import('../src/lib/agentIndicator')
  const settings = useAgentIndicator(), card = useAgentIndicator(), agents = useAgentIndicator()
  await settings.ready
  expect(fetch).toHaveBeenCalledTimes(1)
  expect(card.choice.value).toEqual({ style: 'robot-5', hovering: true })
  settings.setStyle('robot-1')
  expect(agents.choice.value).toEqual({ style: 'robot-1', hovering: true })
  await vi.waitFor(() => expect(writes).toEqual([{ style: 'robot-1', hovering: true }]))
  settings.setHovering(false)
  expect(card.choice.value).toEqual({ style: 'robot-1', hovering: false })
  await vi.waitFor(() => expect(writes.at(-1)).toEqual({ style: 'robot-1', hovering: false }))
})

it('a late load cannot overwrite a choice made while loading', async () => {
  let finish!: (response: Response) => void
  vi.stubGlobal('fetch', vi.fn((_url: string, init: RequestInit) => init.method === 'PUT' ? Promise.resolve(response(null)) : new Promise<Response>(resolve => { finish = resolve })))
  const { useAgentIndicator } = await import('../src/lib/agentIndicator')
  const pref = useAgentIndicator()
  pref.setStyle('robot-5')
  pref.setHovering(true)
  finish(response({ style: 'robot-1', hovering: false }))
  await pref.ready
  expect(pref.choice.value).toEqual({ style: 'robot-5', hovering: true })
  // Let the shared zero-delay write finish before restoring fetch.
  await new Promise(resolve => setTimeout(resolve, 20))
})

it('a failed read keeps the defaults and a failed write notifies Settings', async () => {
  vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new Error('offline')))
  const { useAgentIndicator } = await import('../src/lib/agentIndicator')
  const { onPreferenceFailure } = await import('../src/lib/preferences')
  const failed = vi.fn(), stop = onPreferenceFailure(failed)
  const pref = useAgentIndicator()
  await pref.ready
  expect(pref.choice.value).toEqual({ style: 'robot-1', hovering: false })
  pref.setStyle('robot-5')
  await vi.waitFor(() => expect(failed).toHaveBeenCalledWith('agent-indicator'))
  stop()
})

it.each([
  ['calm', 'robot-1'], ['playful', 'robot-5'],
  ['pulse', 'pulse'], ['robot-1', 'robot-1'], ['robot-2', 'robot-2'], ['robot-3', 'robot-3'],
  ['robot-4', 'robot-4'], ['robot-5', 'robot-5'], ['orbit', 'orbit'], ['quill', 'quill'], ['sprite', 'sprite'],
])('normalizes saved %s to %s without changing hovering', async (saved, expected) => {
  const { normalizeAgentIndicator } = await import('../src/lib/agentIndicator')
  expect(normalizeAgentIndicator({ style: saved, hovering: true })).toEqual({ style: expected, hovering: true })
})

it('reads legacy account data without a write, then persists the migrated style on hovering change', async () => {
  const writes: unknown[] = []
  vi.stubGlobal('fetch', vi.fn(async (_url: string, init: RequestInit) => {
    if (init.method === 'PUT') writes.push(JSON.parse(String(init.body)).value)
    return response({ style: 'playful', hovering: false })
  }))
  const { useAgentIndicator } = await import('../src/lib/agentIndicator')
  const pref = useAgentIndicator()
  await pref.ready
  expect(pref.choice.value).toEqual({ style: 'robot-5', hovering: false })
  expect(writes).toEqual([])
  pref.setHovering(true)
  await vi.waitFor(() => expect(writes).toEqual([{ style: 'robot-5', hovering: true }]))
})
