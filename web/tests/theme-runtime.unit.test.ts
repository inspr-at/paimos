// SPDX-License-Identifier: AGPL-3.0-only
import { beforeEach, afterEach, expect, it, vi } from 'vitest'
import { PORCELAIN, themeCss } from '../src/lib/themeEngine'
import type { ActiveTheme } from '../src/lib/themes'

const active = (light: string): ActiveTheme => ({
  theme: { id: 'personal', tenant_id: 'tenant', name: 'Never cached', scope: 'personal', owner_principal_id: 'person', revision: 2, created_at: '', updated_at: '',
    values: { ...PORCELAIN, primary: { light, dark: null }, agents: { ...PORCELAIN.agents, avatar: 'sprite' } } },
  default_theme_id: 'default', selected_theme_id: 'personal', revision: 1, fallback_notice: null,
})
class Element {
  id = ''; textContent = ''; nonce = ''; parent: Element[] | null = null
  remove() { if (this.parent) this.parent.splice(this.parent.indexOf(this), 1); this.parent = null }
}
let children: Element[], storage: Map<string, string>, document: { head: { append: (node: Element) => void }; getElementById: (id: string) => Element | null }
beforeEach(() => {
  vi.resetModules()
  storage = new Map()
  const tokens = new Element(); tokens.id = 'tokens'
  const boot = new Element(); boot.id = 'aeon-theme-boot'; boot.nonce = 'server-nonce'
  children = [tokens, boot]
  document = { head: { append(node) { node.remove(); children.push(node); node.parent = children } }, getElementById: id => children.find(node => node.id === id) ?? null }
  vi.stubGlobal('document', { ...document, documentElement: { dataset: {} }, createElement: () => new Element() })
  vi.stubGlobal('localStorage', { getItem: (key: string) => storage.get(key) ?? null, setItem: (key: string, value: string) => storage.set(key, value), removeItem: (key: string) => storage.delete(key) })
})
afterEach(() => vi.unstubAllGlobals())
it('one runtime writer updates CSS and agent appearance and caches only owner plus derived CSS', async () => {
  const runtime = await import('../src/lib/themeRuntime'), agents = await import('../src/lib/agentTheme')
  runtime.resetTheme('tenant/person')
  const selected = active('#3a5fc4')
  expect(runtime.publishTheme('tenant/person', selected)).toBe(true)
  const style = document.getElementById('aeon-theme')!
  expect(style.textContent).toBe(themeCss(selected.theme.values)); expect(style.nonce).toBe('server-nonce')
  expect(children.at(-1)).toBe(style)
  expect(globalThis.document.documentElement.dataset).toMatchObject({ agentRing: 'off', agentFloat: 'false' })
  expect(agents.agentTheme.value).toEqual(selected.theme.values.agents)
  expect(JSON.parse(storage.get(runtime.THEME_CACHE_KEY)!)).toEqual({ principal: 'tenant/person', css: style.textContent })
  runtime.applyTheme(PORCELAIN)
  expect(document.getElementById('aeon-theme')).toBe(style)
  expect(children.filter(node => node.id === 'aeon-theme')).toHaveLength(1)
})
it('first authenticated restore keeps matching first-paint CSS until the server answers', async () => {
  const runtime = await import('../src/lib/themeRuntime')
  const boot = new Element(); boot.id = 'aeon-theme'; boot.textContent = themeCss(active('#8547b0').theme.values); document.head.append(boot)
  storage.set(runtime.THEME_CACHE_KEY, JSON.stringify({ principal: 'tenant/person', css: boot.textContent }))
  runtime.resetTheme('tenant/person')
  expect(document.getElementById('aeon-theme')).toBe(boot)
  runtime.publishTheme('tenant/person', active('#3a5fc4'))
  expect(boot.textContent).toContain('--primary: #3a5fc4;')
})
it('another authenticated person and sign-out clear boot CSS and cache synchronously', async () => {
  const runtime = await import('../src/lib/themeRuntime')
  const boot = new Element(); boot.id = 'aeon-theme'; document.head.append(boot)
  storage.set(runtime.THEME_CACHE_KEY, JSON.stringify({ principal: 'tenant/old', css: themeCss(PORCELAIN) }))
  runtime.resetTheme('tenant/new')
  expect(document.getElementById('aeon-theme')).toBeNull(); expect(storage.size).toBe(0)
  runtime.publishTheme('tenant/new', active('#3a5fc4'))
  runtime.resetTheme()
  expect(document.getElementById('aeon-theme')).toBeNull(); expect(storage.size).toBe(0)
  expect(globalThis.document.documentElement.dataset).toEqual({})
  expect(runtime.publishTheme('tenant/new', active('#8547b0'))).toBe(false)
})
it('late answers from a previous tenant or person cannot change CSS, cache or agent appearance', async () => {
  const runtime = await import('../src/lib/themeRuntime'), agents = await import('../src/lib/agentTheme')
  runtime.resetTheme('tenant/person'); runtime.publishTheme('tenant/person', active('#3a5fc4'))
  const css = document.getElementById('aeon-theme')!.textContent, cache = storage.get(runtime.THEME_CACHE_KEY), appearance = agents.agentTheme.value
  for (const identity of ['', 'tenant/other', 'other/person']) expect(runtime.publishTheme(identity, active('#8547b0'))).toBe(false)
  expect(document.getElementById('aeon-theme')!.textContent).toBe(css); expect(storage.get(runtime.THEME_CACHE_KEY)).toBe(cache)
  expect(agents.agentTheme.value).toBe(appearance)
})
it('blocked browser storage never stops application or identity resets', async () => {
  vi.stubGlobal('localStorage', { getItem() { throw Error('disabled') }, setItem() { throw Error('disabled') }, removeItem() { throw Error('disabled') } })
  const runtime = await import('../src/lib/themeRuntime')
  expect(() => { runtime.resetTheme('tenant/person'); runtime.applyTheme(PORCELAIN); runtime.resetTheme() }).not.toThrow()
})
