// SPDX-License-Identifier: AGPL-3.0-only
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

beforeEach(async () => {
  vi.resetModules()
  const { setPreferenceOwner } = await import('../src/lib/preferences')
  setPreferenceOwner({ tenant: { id: 'test-tenant' }, principal: { id: 'test-person' } })
})
afterEach(() => vi.unstubAllGlobals())
const response = (value: unknown) => new Response(JSON.stringify({ value }))

it.each([null, undefined, {}, [], 'slow', { pace: 'zippy' }, { seconds: 60 }])('keeps Default for missing or malformed data: %j', async saved => {
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(response(saved)))
  const { normalizeGraphMotion, useGraphMotion, GRAPH_MOTION_SECONDS, graphOrbitSeconds } = await import('../src/lib/graphMotion')
  expect(normalizeGraphMotion(saved)).toEqual({ pace: 'default' })
  const pref = useGraphMotion()
  expect(pref.choice.value).toEqual({ pace: 'default' })
  expect(pref.seconds.value).toBe(GRAPH_MOTION_SECONDS.default)
  expect(graphOrbitSeconds()).toBe(120)
  await pref.ready
  expect(pref.choice.value).toEqual({ pace: 'default' })
  expect(graphOrbitSeconds()).toBe(120)
})

it('maps each pace to its revolution time and OrbitControls speed', async () => {
  const { GRAPH_MOTION_SECONDS, autoRotateSpeedFor } = await import('../src/lib/graphMotion')
  expect(GRAPH_MOTION_SECONDS).toEqual({ slow: 180, default: 120, lively: 60, off: 0 })
  // Speed 1 is the old one-turn-per-minute orbit. Default is half of that.
  expect(autoRotateSpeedFor(GRAPH_MOTION_SECONDS.lively)).toBe(1)
  expect(autoRotateSpeedFor(GRAPH_MOTION_SECONDS.default)).toBe(0.5)
  expect(autoRotateSpeedFor(GRAPH_MOTION_SECONDS.slow)).toBeCloseTo(1 / 3)
  expect(autoRotateSpeedFor(GRAPH_MOTION_SECONDS.off)).toBe(0)
})

it('loads one account preference and publishes the speed for every graph', async () => {
  const writes: unknown[] = []
  const fetch = vi.fn(async (url: string, init: RequestInit) => {
    expect(url).toBe('/api/preferences/graph-motion')
    if (init.method === 'PUT') { writes.push(JSON.parse(String(init.body)).value); return response(writes.at(-1)) }
    return response({ pace: 'lively' })
  })
  vi.stubGlobal('fetch', fetch)
  const { useGraphMotion, graphOrbitSeconds, autoRotateSpeedFor } = await import('../src/lib/graphMotion')
  const settings = useGraphMotion(), canvas = useGraphMotion()
  await settings.ready
  expect(fetch).toHaveBeenCalledTimes(1)
  expect(canvas.choice.value).toEqual({ pace: 'lively' })
  expect(canvas.seconds.value).toBe(60)
  expect(graphOrbitSeconds()).toBe(60)
  expect(autoRotateSpeedFor(graphOrbitSeconds())).toBe(1)
  settings.setPace('slow')
  expect(canvas.choice.value).toEqual({ pace: 'slow' })
  expect(canvas.seconds.value).toBe(180)
  expect(graphOrbitSeconds()).toBe(180)
  await vi.waitFor(() => expect(writes).toEqual([{ pace: 'slow' }]))
  settings.setPace('off')
  expect(graphOrbitSeconds()).toBe(0)
  await vi.waitFor(() => expect(writes.at(-1)).toEqual({ pace: 'off' }))
  settings.setPace('default')
  expect(graphOrbitSeconds()).toBe(120)
})

it('a late load cannot overwrite a pace chosen while loading', async () => {
  let finish!: (response: Response) => void
  vi.stubGlobal('fetch', vi.fn((_url: string, init: RequestInit) => init.method === 'PUT' ? Promise.resolve(response(null)) : new Promise<Response>(resolve => { finish = resolve })))
  const { useGraphMotion, graphOrbitSeconds } = await import('../src/lib/graphMotion')
  const pref = useGraphMotion()
  pref.setPace('slow')
  expect(graphOrbitSeconds()).toBe(180)
  finish(response({ pace: 'lively' }))
  await pref.ready
  expect(pref.choice.value).toEqual({ pace: 'slow' })
  expect(graphOrbitSeconds()).toBe(180)
  await new Promise(resolve => setTimeout(resolve, 20))
})

it('a failed read keeps Default and a failed write notifies Settings', async () => {
  vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new Error('offline')))
  const { useGraphMotion, graphOrbitSeconds } = await import('../src/lib/graphMotion')
  const { onPreferenceFailure } = await import('../src/lib/preferences')
  const failed = vi.fn(), stop = onPreferenceFailure(failed)
  const pref = useGraphMotion()
  await pref.ready
  expect(pref.choice.value).toEqual({ pace: 'default' })
  expect(graphOrbitSeconds()).toBe(120)
  pref.setPace('lively')
  expect(pref.seconds.value).toBe(60)
  await vi.waitFor(() => expect(failed).toHaveBeenCalledWith('graph-motion'))
  stop()
})
