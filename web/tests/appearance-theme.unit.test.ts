// SPDX-License-Identifier: AGPL-3.0-only
import { beforeEach, afterEach, expect, it, vi } from 'vitest'
import { reactive } from 'vue'
import { PORCELAIN, themeCss } from '../src/lib/themeEngine'
import type { ActiveTheme, ThemeValues } from '../src/lib/themes'
const { request, notify, session } = vi.hoisted(() => ({ request: vi.fn(), notify: vi.fn(), session: { current: { identity: null as unknown } } }))
vi.mock('../src/lib/api.ts', () => ({ api: request }))
vi.mock('../src/lib/toast', () => ({ toast: notify }))
vi.mock('../src/lib/authz', () => ({ can: () => true }))
vi.mock('../src/stores/session', () => ({ useSession: () => session.current }))
const ME = 'tenant/person'
const selected = (values = PORCELAIN, fallback_notice: ActiveTheme['fallback_notice'] = null): ActiveTheme => ({
  theme: { id: 'personal', tenant_id: 'tenant', name: 'Personal', scope: 'personal', owner_principal_id: 'person', revision: 1, created_at: '', updated_at: '', values },
  default_theme_id: 'default', selected_theme_id: 'personal', revision: 1, fallback_notice,
})
const active = (values = PORCELAIN, fallback_notice: ActiveTheme['fallback_notice'] = null) => new Response(JSON.stringify(selected(values, fallback_notice)))
const page = () => new Response(JSON.stringify({ items: [selected().theme], next_cursor: null }))
function deferred<T>() { let resolve!: (v: T) => void; const promise = new Promise<T>(r => { resolve = r }); return { promise, resolve } }
let css: { textContent: string; remove: () => void } | null
beforeEach(() => {
  vi.resetModules(); request.mockReset(); notify.mockReset(); css = null
  session.current = reactive({ identity: { tenant: { id: 'tenant' }, principal: { id: 'person', kind: 'person' } } })
  vi.stubGlobal('document', { documentElement: { dataset: {} }, getElementById: () => css,
    createElement: () => ({ id: '', textContent: '', nonce: '', remove: () => { css = null } }), head: { append: (node: typeof css) => { css = node } } })
  request.mockImplementation(async (path: string) => path.startsWith('/themes?') ? page() : active())
})
afterEach(() => vi.unstubAllGlobals())
async function runtime() {
  const module = await import('../src/lib/agentTheme')
  module.resetAgentTheme(ME)
  return module
}
it('applies server values, palette and inks, then clears owned CSS on identity reset', async () => {
  const module = await runtime(), values = structuredClone(PORCELAIN)
  values.agents.palette = 'tritan'; values.primary.light = '#8547b0'
  request.mockImplementation(async (path: string) => path.startsWith('/themes?') ? page() : active(values))
  await module.restoreAgentTheme(ME)
  expect(request).toHaveBeenCalledWith('/me/theme', { signal: expect.any(AbortSignal) }, 5000)
  expect(css?.textContent).toBe(themeCss(values))
  expect(css?.textContent).toContain('--agent-working-ink: #ffffff;')
  expect(module.agentTheme.value?.palette).toBe('tritan')
  module.resetAgentTheme()
  expect(css).toBeNull(); expect(module.agentTheme.value).toBeNull()
})
it('a late person/workspace response cannot publish or announce their theme', async () => {
  const module = await runtime(), pending = deferred<Response>(), requested = deferred<void>()
  request.mockImplementation((path: string) => {
    if (path.startsWith('/themes?')) return Promise.resolve(page())
    requested.resolve(); return pending.promise
  })
  const job = module.restoreAgentTheme(ME)
  await requested.promise
  session.current.identity = null; module.resetAgentTheme()
  pending.resolve(active(PORCELAIN, { deleted_theme_id: 'old', deleted_theme_name: 'Other person' })); await job
  expect(css).toBeNull(); expect(module.agentTheme.value).toBeNull(); expect(notify).not.toHaveBeenCalled()
})
it('a newer load owns both editor and runtime when the old response resolves last', async () => {
  const module = await runtime(), pending = deferred<Response>(), requested = deferred<void>()
  let reads = 0
  request.mockImplementation((path: string) => {
    if (path.startsWith('/themes?')) return Promise.resolve(page())
    if (++reads === 1) { requested.resolve(); return pending.promise }
    return Promise.resolve(active())
  })
  const old = module.restoreAgentTheme(ME); await requested.promise
  await module.restoreAgentTheme(ME)
  const values = structuredClone(PORCELAIN); values.primary.light = '#8547b0'
  pending.resolve(active(values)); await old
  expect(css?.textContent).toBe(themeCss(PORCELAIN))
  const { useThemeEditor } = await import('../src/stores/themeEditor')
  expect(useThemeEditor().active.value?.theme.values).toEqual(PORCELAIN)
})
it('reset invalidates pending reads and the shared editor before another sign-in', async () => {
  const module = await runtime(), pending = deferred<Response>(), requested = deferred<void>()
  request.mockImplementation((path: string) => {
    if (path.startsWith('/themes?')) return Promise.resolve(page())
    requested.resolve(); return pending.promise
  })
  const old = module.restoreAgentTheme(ME); await requested.promise
  session.current.identity = null; module.resetAgentTheme(); pending.resolve(active()); await old
  expect(css).toBeNull(); expect(module.agentTheme.value).toBeNull()
})
it('invalid, failed and oversized initial answers expose an honest default-colour error', async () => {
  const module = await runtime()
  const valid = JSON.stringify(selected())
  const { getActiveTheme } = await import('../src/lib/themes')
  for (const [response, reason] of [
    [() => new Response('{}'), 'Invalid theme response'],
    [() => new Response(null, { status: 503 }), 'Your theme could not be loaded'],
    [() => new Response(valid + ' '.repeat(16385)), 'Theme response is too large'],
    [() => new Response(valid, { headers: { 'Content-Length': '16385' } }), 'Theme response is too large'],
  ] as const) {
    request.mockImplementation(async (path: string) => path.startsWith('/themes?') ? page() : response())
    // The oversized payloads are valid themes: JSON/schema failure cannot pass.
    await expect(getActiveTheme()).rejects.toThrow(reason)
    await module.restoreAgentTheme(ME)
    expect(css).toBeNull(); expect(module.agentTheme.value).toBeNull()
    expect(notify).toHaveBeenLastCalledWith(expect.stringContaining('could not be loaded'), { tone: 'error' })
  }
  expect(notify).toHaveBeenCalledTimes(4)
})
it('a deleted selection applies the returned default and explains the fallback', async () => {
  const module = await runtime(), values = structuredClone(PORCELAIN)
  values.secondary.light = '#bf3d6d'
  request.mockImplementation(async (path: string) => path.startsWith('/themes?') ? page() : active(values, { deleted_theme_id: 'old', deleted_theme_name: 'Old' }))
  await module.restoreAgentTheme(ME)
  expect(css?.textContent).toBe(themeCss(values))
  expect(notify).toHaveBeenCalledWith(expect.stringContaining('workspace default'))
})
it('confirmed edits publish through the same editor and stylesheet as restoration', async () => {
  const module = await runtime()
  await module.restoreAgentTheme(ME)
  const values: ThemeValues = structuredClone(PORCELAIN); values.primary.light = '#8547b0'
  request.mockImplementation(async (path: string, init?: RequestInit) => {
    expect(path).toBe('/themes/personal'); expect(init?.method).toBe('PATCH')
    return new Response(JSON.stringify(selected(values).theme))
  })
  const { useThemeEditor } = await import('../src/stores/themeEditor')
  useThemeEditor().update(draft => { draft.values = values })
  await useThemeEditor().save()
  expect(css?.textContent).toBe(themeCss(values))
})
