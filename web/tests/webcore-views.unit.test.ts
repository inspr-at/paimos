// SPDX-License-Identifier: AGPL-3.0-only
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { reactive, type App } from 'vue'
import * as knowledge from '../src/lib/knowledge'
import * as week from '../src/lib/week'
import * as money from '../src/components/business/money'
import * as duration from '../src/components/business/duration'
import { mountView, settle, textOf } from './webcore-view-harness'

const apps: App[] = []
const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status })
function deferred<T>() { let resolve!: (value: T) => void; const promise = new Promise<T>(yes => { resolve = yes }); return { promise, resolve } }
beforeEach(() => {
  vi.stubGlobal('window', { addEventListener() {}, removeEventListener() {} })
  vi.stubGlobal('document', { querySelector: () => null })
})
afterEach(() => { for (const app of apps.splice(0)) app.unmount(); vi.unstubAllGlobals(); vi.useRealTimers() })

describe('S8-011: timing writes', () => {
  it.each([403, 500, 'network'])('restores confirmed values and offers retry after %s', async failure => {
    const api = vi.fn(async (path: string, init?: RequestInit) => {
      if (!init) return json(path.includes('eta') ? { interval_minutes: 10 } : { heartbeat_lost_minutes: 15 })
      if (failure === 'network') throw new TypeError('offline')
      return json({}, failure)
    })
    const view = mountView('../src/components/settings/WorkspaceSection.vue', {
      '../../lib/api': { api }, '../../lib/brand': { brand: { short_name: 'Aeon' } },
      '../../lib/authz': { can: () => true, myWorkspaceRole: () => null, permissionsAvailable: () => true },
      '../../stores/session': { useSession: () => ({ identity: { tenant: { name: 'Test' } } }) },
    })
    apps.push(view.app); await settle()
    for (const [id, original, requested, field] of [['eta-minutes', 10, 20, 'interval_minutes'], ['lost-minutes', 15, 30, 'heartbeat_lost_minutes']] as const) {
      const input = view.find(el => el.props.id === id)
      ;(input.props['onUpdate:modelValue'] as (value: number) => void)(requested)
      await (input.props.onChange as () => Promise<void>)(); await settle()
      expect(input.props.value).toBe(original)
      expect(textOf(view.root)).toContain('could not be confirmed')
      const retry = view.find(el => el.tag === 'button' && textOf(el).includes(`Retry ${requested}`))
      expect(retry).toBeDefined()
      api.mockImplementationOnce(async () => json({ [field]: requested + 1 }))
      await (retry.props.onClick as () => Promise<void>)(); await settle()
      expect(input.props.value).toBe(requested + 1)
    }
  })
  it('disables pending writes and adopts the authoritative value without overlapping requests', async () => {
    const pending = deferred<Response>()
    const api = vi.fn(async (path: string, init?: RequestInit) => init ? pending.promise : json(path.includes('eta') ? { interval_minutes: 10 } : { heartbeat_lost_minutes: 15 }))
    const view = mountView('../src/components/settings/WorkspaceSection.vue', {
      '../../lib/api': { api }, '../../lib/brand': { brand: {} },
      '../../lib/authz': { can: () => true, myWorkspaceRole: () => null, permissionsAvailable: () => true },
      '../../stores/session': { useSession: () => ({ identity: null }) },
    })
    apps.push(view.app); await settle()
    const input = view.find(el => el.props.id === 'eta-minutes')
    ;(input.props['onUpdate:modelValue'] as (value: number) => void)(20)
    const first = (input.props.onChange as () => Promise<void>)(); await settle()
    expect(input.props.disabled).toBe(true)
    const second = (input.props.onChange as () => Promise<void>)()
    expect(api.mock.calls.filter(call => call[1]?.method === 'PUT')).toHaveLength(1)
    pending.resolve(json({ interval_minutes: 21 })); await first; await second; await settle()
    expect(input.props.value).toBe(21)
    expect(input.props.disabled).toBe(false)
  })
})

it('S8-014: a failed second search labels retained results and exposes retry until success', async () => {
  const route = reactive({ query: { q: 'first' } })
  const listKnowledge = vi.fn().mockResolvedValueOnce({ items: [{ id: 'entry', title: 'First result', type: 'runbook', slug: 'first', status: 'active', updated_at: '2026-10-01', project: { id: 'p', key: 'P', title: 'Project' } }] })
    .mockRejectedValueOnce(new Error('Search failed'))
  const view = mountView('../src/views/KnowledgeView.vue', {
    'vue-router': { useRoute: () => route, useRouter: () => ({ replace() {} }) },
    '../lib/brand': { brand: { value: { product: 'Aeon' } }, setPageTitle() {} },
    '../lib/knowledge': { ...knowledge, listKnowledge }, '../lib/work': { absoluteTime: (v: string) => v, relativeTime: () => '', plural: (n: number, word: string) => `${n} ${word}` },
    '../stores/projects': { useProjects: () => ({ load() {}, byId: () => null }) },
  })
  apps.push(view.app); await settle()
  expect(textOf(view.root).replace(/\s+/g, ' ')).toContain('First result')
  route.query.q = 'second'; await settle()
  expect(textOf(view.root)).toContain('Search failed')
  expect(textOf(view.root)).toContain('Previous results for “first”')
  expect(view.find(el => el.props.class?.toString().includes('kp-results')).props.class).toContain('stale')
  const retry = view.find(el => el.tag === 'button' && textOf(el).includes('Try again'))
  const pending = deferred<{ items: never[] }>(); listKnowledge.mockReturnValueOnce(pending.promise)
  const retrying = (retry.props.onClick as () => Promise<void>)(); await settle()
  expect(retry.props.disabled).toBe(true)
  expect(textOf(view.root)).toContain('Previous results for “first”')
  pending.resolve({ items: [] }); await retrying; await settle()
  expect(textOf(view.root)).not.toContain('Search failed')
  expect(textOf(view.root)).not.toContain('Previous results')
  expect(textOf(view.root)).toContain('Nothing matches “second”')
})

describe('S8-012: hours preview uses the submitted start instant', () => {
  it('retains the chosen cost unit during incremental start typing and revalidates the completed UTC day', async () => {
    const prior = process.env.TZ; process.env.TZ = 'Europe/Vienna'
    try {
      const units = ['other', 'chosen'].map(id => ({ node: { id, title: id, state: 'new' }, rates: [{ unit: 'hour', currency: 'EUR', effective_from: id === 'chosen' ? '2026-10-02' : '2020-01-01', effective_until: null }] }))
      const rateOn = vi.fn((id: string, _unit: string, _currency: string, on: string) => units.find(unit => unit.node.id === id)?.rates.some(r => r.effective_from <= on) ? { bill_amount: '120.0000', currency: 'EUR' } : null)
      const logs: { costUnitId: string; startedAt: string }[] = []
      const view = mountView('../src/components/business/LogTimeBar.vue', {
        '../../lib/api': { listNodes: vi.fn() }, '../../lib/recents': { recents: [] }, '../../lib/week': week,
        './duration': duration, './money': money,
        '../../stores/business': { useBusiness: () => ({ costUnits: units, costUnit: (id: string) => units.find(unit => unit.node.id === id), rateOn }) },
      }, { days: [new Date(2026, 9, 2)], suggestions: [], busy: false, preset: { id: 'ticket', key: 'T-1', title: 'Ticket' }, onLog: (log: typeof logs[number]) => logs.push(log) })
      apps.push(view.app); await settle()
      const cost = view.find(el => el.props['aria-label'] === 'Cost unit')
      const start = view.find(el => el.props['aria-label'] === 'Start time (optional)')
      const form = view.find(el => el.tag === 'form')
      ;(cost.props['onUpdate:modelValue'] as (v: string) => void)('chosen')
      ;(view.find(el => el.props['aria-label'] === 'Duration').props['onUpdate:modelValue'] as (v: string) => void)('15m')
      await settle()
      expect(cost.props.value).toBe('chosen')
      for (const value of ['9', '9:', '9:3', '9:30']) {
        ;(start.props['onUpdate:modelValue'] as (v: string) => void)(value); await settle()
        expect(cost.props.value, `selection after typing ${value}`).toBe('chosen')
        expect(cost.children.filter(el => el.tag === 'option')).toHaveLength(3)
        if (value === '9:') {
          ;(form.props.onSubmit as (e: unknown) => void)({ preventDefault() {} })
          expect(logs).toHaveLength(0)
        }
      }
      ;(form.props.onSubmit as (e: unknown) => void)({ preventDefault() {} })
      expect(logs).toEqual([expect.objectContaining({ costUnitId: 'chosen', startedAt: new Date(2026, 9, 2, 9, 30).toISOString() })])
      ;(start.props['onUpdate:modelValue'] as (v: string) => void)('0:30'); await settle()
      expect(cost.props.value).toBe('other')
      expect(rateOn.mock.calls.at(-1)?.[3]).toBe('2026-10-01')
    } finally { if (prior === undefined) delete process.env.TZ; else process.env.TZ = prior }
  })
  it.each([
    ['Europe/Vienna', 30, '2026-10-01', false], ['Europe/Vienna', 30, '2026-10-01', true],
    ['America/Los_Angeles', 23 * 60 + 30, '2026-10-03', false], ['America/Los_Angeles', 23 * 60 + 30, '2026-10-03', true],
  ] as const)('prices %s at %s minutes on UTC day %s (explicit=%s)', async (zone, minutes, expectedDay, explicit) => {
    const prior = process.env.TZ; process.env.TZ = zone
    try {
      const day = new Date(2026, 9, 2)
      const rateOn = vi.fn((_id: string, _unit: string, _currency: string, on: string) => ({ bill_amount: on === expectedDay ? '120.0000' : '60.0000', currency: 'EUR' }))
      const logs: { startedAt: string }[] = []
      const view = mountView('../src/components/business/LogTimeBar.vue', {
        '../../lib/api': { listNodes: vi.fn() }, '../../lib/recents': { recents: [] }, '../../lib/week': week,
        './duration': duration, './money': money,
        '../../stores/business': { useBusiness: () => ({ costUnits: [{ node: { id: 'rate', state: 'new' }, rates: [{ unit: 'hour', currency: 'EUR', effective_from: '2020-01-01', effective_until: null }] }], costUnit: () => ({ rates: [{ unit: 'hour', currency: 'EUR' }] }), rateOn }) },
      }, { days: [day], suggestions: [], busy: false, preset: { id: 'ticket', key: 'T-1', title: 'Ticket' }, defaultStartMinutes: () => explicit ? 9 * 60 : minutes, onLog: (log: { startedAt: string }) => logs.push(log) })
      apps.push(view.app); await settle()
      if (explicit) {
        const start = view.find(el => el.props['aria-label'] === 'Start time (optional)')
        ;(start.props['onUpdate:modelValue'] as (v: string) => void)(`${Math.floor(minutes / 60)}:${String(minutes % 60).padStart(2, '0')}`)
      }
      const field = view.find(el => el.props['aria-label'] === 'Duration')
      ;(field.props['onUpdate:modelValue'] as (v: string) => void)('15m'); await settle()
      expect(rateOn.mock.calls.at(-1)?.[3]).toBe(expectedDay)
      expect(textOf(view.root)).toContain('30.00 EUR')
      ;(view.find(el => el.tag === 'form').props.onSubmit as (e: unknown) => void)({ preventDefault() {} })
      expect(logs[0]?.startedAt).toBe(new Date(2026, 9, 2, 0, minutes).toISOString())
      expect(logs[0]?.startedAt.slice(0, 10)).toBe(expectedDay)
    } finally { if (prior === undefined) delete process.env.TZ; else process.env.TZ = prior }
  })
})
