// SPDX-License-Identifier: AGPL-3.0-only
import { beforeEach, afterEach, expect, it, vi } from 'vitest'
import 'vue'
import { PORCELAIN } from '../src/lib/themeValues'
const { request, notify } = vi.hoisted(() => ({ request: vi.fn(), notify: vi.fn() }))
vi.mock('../src/lib/api.ts', () => ({ api: request }))
vi.mock('../src/lib/toast.ts', () => ({ toast: notify }))
beforeEach(() => { vi.resetModules(); request.mockReset(); notify.mockReset() })
afterEach(() => vi.unstubAllGlobals())
const active = (values = PORCELAIN, fallback_notice: unknown = null) => new Response(JSON.stringify({ theme: { values }, fallback_notice }))
function deferred<T>() { let resolve!: (v: T) => void; const promise = new Promise<T>(r => { resolve = r }); return { promise, resolve } }
function root() {
  const properties = new Map<string, string>()
  vi.stubGlobal('document', { documentElement: { style: { setProperty: (k: string, v: string) => properties.set(k, v), removeProperty: (k: string) => properties.delete(k) } } })
  vi.stubGlobal('getComputedStyle', () => ({ getPropertyValue: (k: string) => properties.get(k) ?? (k === '--danger' ? '#b24a44' : '#287b2c') }))
  return properties
}
it('applies server values, palette and inks, then clears all owned tokens on identity reset', async () => {
  const properties = root(), module = await import('../src/lib/appearanceTheme')
  const values = structuredClone(PORCELAIN); values.agents.palette = 'tritan'; values.primary.light = '#8547b0'
  request.mockResolvedValue(active(values))
  await module.restoreAppearanceTheme()
  expect(request).toHaveBeenCalledWith('/me/theme', {}, 5000)
  expect(properties.get('--teal')).toBe('#8547b0')
  expect(properties.get('--button-ink')).toBe('#ffffff')
  expect(properties.get('--agent-tritan-working-ink')).toBe('#ffffff')
  expect(module.activeThemeValues.value?.agents.palette).toBe('tritan')
  module.resetAppearanceTheme()
  expect(properties.size).toBe(0)
  expect(module.activeThemeValues.value).toBe(null)
})
it('a late person/workspace response is discarded after the identity check changes', async () => {
  const module = await import('../src/lib/appearanceTheme'), pending = deferred<Response>(); let current = true
  request.mockReturnValue(pending.promise)
  const job = module.restoreAppearanceTheme(() => current)
  current = false; pending.resolve(active(PORCELAIN, { deleted_theme_name: 'Other person' }))
  await job
  expect(module.activeThemeValues.value).toBe(null)
  expect(notify).not.toHaveBeenCalled()
})
it('a newer load wins when the old request resolves last', async () => {
  const module = await import('../src/lib/appearanceTheme'), pending = deferred<Response>()
  request.mockReturnValueOnce(pending.promise).mockResolvedValueOnce(active())
  const old = module.restoreAppearanceTheme()
  await module.restoreAppearanceTheme()
  const values = structuredClone(PORCELAIN); values.primary.light = '#8547b0'
  pending.resolve(active(values)); await old
  expect(module.activeThemeValues.value?.primary.light).toBe(PORCELAIN.primary.light)
})
it('reset invalidates pending reads even when their caller still claims to be current', async () => {
  const module = await import('../src/lib/appearanceTheme'), pending = deferred<Response>()
  request.mockReturnValue(pending.promise)
  const old = module.restoreAppearanceTheme()
  module.resetAppearanceTheme(); pending.resolve(active()); await old
  expect(module.activeThemeValues.value).toBe(null)
})
it('invalid, failed and oversized answers show an honest default-colour error', async () => {
  const module = await import('../src/lib/appearanceTheme')
  for (const response of [new Response('{}'), new Response(null, { status: 503 }), new Response(' '.repeat(16385)), new Response('{}', { headers: { 'Content-Length': '16385' } })]) {
    request.mockResolvedValue(response)
    await module.restoreAppearanceTheme()
    expect(module.activeThemeValues.value).toEqual(PORCELAIN)
    expect(notify).toHaveBeenLastCalledWith(expect.stringContaining('could not be loaded'), { tone: 'error' })
  }
  expect(notify).toHaveBeenCalledTimes(4)
})
it('a deleted selection applies the returned workspace default and explains the fallback', async () => {
  const module = await import('../src/lib/appearanceTheme'), values = structuredClone(PORCELAIN)
  values.secondary.light = '#bf3d6d'
  request.mockResolvedValue(active(values, { deleted_theme_id: 'old', deleted_theme_name: 'Old' }))
  await module.restoreAppearanceTheme()
  expect(module.activeThemeValues.value?.secondary.light).toBe('#bf3d6d')
  expect(notify).toHaveBeenCalledWith(expect.stringContaining('workspace default'))
})

it('an accepted saved selection supersedes an earlier active-theme read', async () => {
  const module = await import('../src/lib/appearanceTheme'), pending = deferred<Response>()
  request.mockReturnValue(pending.promise)
  const old = module.restoreAppearanceTheme()
  const saved = structuredClone(PORCELAIN); saved.primary.light = '#8547b0'
  module.applyAppearanceTheme(saved)
  pending.resolve(active()); await old
  expect(module.activeThemeValues.value?.primary.light).toBe('#8547b0')
})
