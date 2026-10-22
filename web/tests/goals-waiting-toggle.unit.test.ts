// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1614 v1.2: the Goals toolbar's Waiting on you toggle and Display › Show ›
// Waiting on you are one per-person setting. Risks: the two controls disagree after a
// flip, a flip from either drops the person's other Goals preferences, or hiding the
// band takes the count of open Desk items off the toggle. Both controls click through
// page.toggleWaiting; goals-hide.spec.ts clicks each of them in a browser.
import { afterEach, describe, expect, it, vi } from 'vitest'
import { createSSRApp, effectScope, nextTick, ref, type Component, type EffectScope } from 'vue'
import { renderToString } from '@vue/server-renderer'
import { createPinia, setActivePinia, type Pinia } from 'pinia'
import type { Identity } from '../src/lib/api'

const mocks = vi.hoisted(() => {
  const media = { matches: false, addEventListener() {}, removeEventListener() {} }
  vi.stubGlobal('window', { history: { state: {} }, matchMedia: () => media, addEventListener() {}, removeEventListener() {}, localStorage: { getItem: () => null, setItem() {}, removeItem() {} }, sessionStorage: { getItem: () => null, setItem() {}, removeItem() {} } })
  vi.stubGlobal('navigator', { platform: 'MacIntel', userAgent: 'test', onLine: true })
  // The person's Goals preferences on the server, and every write of them.
  return { stored: { hideDone: false, group: 'goals' } as Record<string, unknown>, writes: [] as Record<string, unknown>[] }
})
vi.mock('../src/lib/goals', async importOriginal => {
  const actual = await importOriginal<typeof import('../src/lib/goals')>()
  return { ...actual, listGoals: vi.fn(async () => ({ items: [], totals: { moving: 0, queued: 0, paused: 0, done: 0 }, totals_partial: false, quiet_projects: { count: 0, keys: [] }, truncated: false, read_through: 1, generation: 1, hidden_count: 0 })) }
})
vi.mock('../src/lib/api', async importOriginal => {
  const actual = await importOriginal<typeof import('../src/lib/api')>()
  const json = (body: unknown) => new Response(JSON.stringify(body), { status: 200, headers: { 'content-type': 'application/json' } })
  return {
    ...actual,
    api: vi.fn(async (path: string, init?: RequestInit) => {
      if (path !== '/preferences/goals') return json({ value: null })
      if (init?.method === 'PUT') { const { value } = JSON.parse(String(init.body)); mocks.writes.push(value); mocks.stored = value; return json({ value }) }
      return json({ value: mocks.stored })
    }),
  }
})
vi.mock('../src/lib/usePolledData', async importOriginal => {
  const actual = await importOriginal<typeof import('../src/lib/usePolledData')>()
  return { ...actual, usePoller: () => ({ start() {}, stop() {}, tick() {} }) }
})
vi.mock('../src/lib/goalsLive', async importOriginal => {
  const actual = await importOriginal<typeof import('../src/lib/goalsLive')>()
  return { ...actual, openGoalsLive: () => ({ close() {} }) }
})

import { useSession } from '../src/stores/session'
import { useDecisionDesk } from '../src/stores/decisionDesk'
import { useGoalsPage } from '../src/components/goals/goalsPage'
import GoalsToolbar from '../src/components/goals/GoalsToolbar.vue'
import GoalShowSwitches from '../src/components/goals/GoalShowSwitches.vue'

const person: Identity = { principal: { id: 'person-a', name: 'A', kind: 'person' }, tenant: { id: 'tenant', name: 'Workspace' } } as Identity
async function flush() { for (let i = 0; i < 12; i++) { await Promise.resolve(); await nextTick() } }
async function html(pinia: Pinia, component: Component, props: Record<string, unknown>) {
  const app = createSSRApp(component, props)
  app.use(pinia)
  return (await renderToString(app)).replace(/<!--[^>]*-->/g, '')
}
// The toggle's pressed state and the Display switch's checked state, read from what they render.
const pressed = (toolbar: string) => /class="icon-btn wait-toggle"[^>]*aria-pressed="(true|false)"|aria-pressed="(true|false)"[^>]*class="icon-btn wait-toggle"/.exec(toolbar)?.slice(1).find(Boolean)
const checked = (switches: string) => /data-show="waiting" aria-checked="(true|false)"/.exec(switches)?.[1]

describe('Waiting on you: one setting for the toolbar toggle and Display › Show', () => {
  const scopes: EffectScope[] = []
  afterEach(() => { for (const scope of scopes.splice(0)) scope.stop(); mocks.writes.length = 0 })

  it('a flip from either control shows in both, keeps the other Goals preferences, and the count stays while the band is hidden', async () => {
    const pinia = createPinia()
    setActivePinia(pinia)
    useSession().identity = person
    useDecisionDesk().projection = { items: [], counts: { open: 2, held: 0, chores: 0 }, has_more: false, as_of: '2026-10-21T08:00:00Z' } as never
    const scope = effectScope()
    scopes.push(scope)
    const page = scope.run(() => useGoalsPage({ term: ref(''), active: () => true, groups: {} }))!
    await flush()
    expect(page.prefsSettled.value).toBe(true)

    // On until the person turns it off: pressed, checked, and the count on the icon.
    let toolbar = await html(pinia, GoalsToolbar, { page, part: 'waiting' })
    let display = await html(pinia, GoalShowSwitches, { page })
    expect(pressed(toolbar)).toBe('true')
    expect(checked(display)).toBe('true')
    expect(toolbar).toContain('aria-label="Waiting on you, 2 open"')
    expect(toolbar).toContain('data-tip="Waiting on you · 2 open"')
    expect(toolbar).toMatch(/<span class="wait-count mono" aria-hidden="true"[^>]*>2<\/span>/)

    // A flip: the band goes, both controls say so, the count stays.
    page.toggleWaiting()
    // The write leaves on the next task (a delay-0 save); wait for it to reach the server.
    await vi.waitFor(() => expect(mocks.writes.at(-1)).toEqual({ hideDone: false, group: 'goals', showWaiting: false }))
    toolbar = await html(pinia, GoalsToolbar, { page, part: 'waiting' })
    display = await html(pinia, GoalShowSwitches, { page })
    expect(pressed(toolbar)).toBe('false')
    expect(checked(display)).toBe('false')
    expect(toolbar).toMatch(/<span class="wait-count mono" aria-hidden="true"[^>]*>2<\/span>/)
    // The phone ⋯ row carries the count too; the Display panel's row does not.
    const menu = await html(pinia, GoalShowSwitches, { page, menu: true })
    expect(menu).toMatch(/data-show="waiting" aria-checked="false"[^]*?Waiting on you<span class="row-count"[^>]*> · 2<\/span>/)
    expect(display).not.toContain('row-count')

    // A second flip turns it back on in both.
    page.toggleWaiting()
    // The write leaves on the next task (a delay-0 save); wait for it to reach the server.
    await vi.waitFor(() => expect(mocks.writes.at(-1)).toEqual({ hideDone: false, group: 'goals', showWaiting: true }))
    expect(pressed(await html(pinia, GoalsToolbar, { page, part: 'waiting' }))).toBe('true')
    expect(checked(await html(pinia, GoalShowSwitches, { page }))).toBe('true')

    // Nothing open: no count on the icon (the toggle keeps its place and size either way).
    useDecisionDesk().projection = { items: [], counts: { open: 0, held: 0, chores: 0 }, has_more: false, as_of: '2026-10-21T08:00:00Z' } as never
    toolbar = await html(pinia, GoalsToolbar, { page, part: 'waiting' })
    expect(toolbar).not.toContain('wait-count')
    expect(toolbar).toContain('aria-label="Waiting on you, 0 open"')
  })
})
