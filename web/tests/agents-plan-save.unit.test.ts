// SPDX-License-Identifier: AGPL-3.0-only
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { effectScope, ref, type EffectScope } from 'vue'
import type { PlanSnapshot } from '../src/lib/agentsWorking'
const mocks = vi.hoisted(() => ({ api: vi.fn(), queue: vi.fn() }))
vi.mock('../src/lib/api.ts', () => ({ api: mocks.api }))
vi.mock('../src/lib/workQueue.ts', () => ({ queueRequest: mocks.queue }))
vi.mock('../src/lib/usePolledData.ts', () => ({ usePoller: () => ({ start() {}, stop() {} }) }))
import { FOLD_KEY, useAgentPlan } from '../src/lib/useAgentPlan'
const snapshot: PlanSnapshot = { total: 5, limits: { codex: 4 }, principal_id: 'canonical', running: { codex: 12 }, running_total: 12, source: 'plan', updated_at: null }
const flush = () => new Promise<void>(resolve => setImmediate(resolve))
const deferred = <T>() => { let resolve!: (value: T) => void; const promise = new Promise<T>(done => { resolve = done }); return { promise, resolve } }
const json = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status, headers: { 'Content-Type': 'application/json' } })
let scope: EffectScope
function setup() {
  const viewer = ref('tenant:person')
  scope = effectScope()
  const control = scope.run(() => useAgentPlan(() => viewer.value, () => undefined))!
  return { control, viewer }
}
beforeEach(() => {
  mocks.api.mockReset(); mocks.queue.mockReset()
  mocks.api.mockImplementation(async (path: string, init?: RequestInit) => init?.method === 'PUT' ? json({}) : path === '/agents/plan' ? json(snapshot) : json({ value: null }))
  mocks.queue.mockResolvedValue({ items: [], capacity: {} })
})
afterEach(() => scope?.stop())
describe('canonical plan persistence', () => {
  it('loads the canonical owner count and saves only total and limits, never session controls', async () => {
    const { control } = setup(); await flush()
    expect(control.snapshot.value?.principal_id).toBe('canonical')
    expect(control.snapshot.value?.running_total).toBe(12)
    control.save({ total: 0, limits: { codex: 'off' } }); await flush()
    const writes = mocks.api.mock.calls.filter(([, init]) => init?.method === 'PUT')
    expect(writes.map(([path]) => path)).toEqual(['/preferences/agents.working'])
    expect(JSON.parse(writes[0]![1].body)).toEqual({ value: { total: 0, limits: { codex: 'off' } } })
  })
  it('serializes rapid clicks and coalesces edits behind a held write', async () => {
    const held = deferred<Response>(), writes: number[] = []
    mocks.api.mockImplementation(async (path: string, init?: RequestInit) => {
      if (init?.method !== 'PUT') return path === '/agents/plan' ? json(snapshot) : json({ value: null })
      const total = JSON.parse(init.body as string).value.total
      writes.push(total)
      return writes.length === 1 ? held.promise : json({})
    })
    const { control } = setup(); await flush()
    control.save({ total: 4, limits: {} }); control.save({ total: 3, limits: {} }); control.save({ total: 2, limits: {} })
    expect(control.plan.value?.total).toBe(2); expect(writes).toEqual([4])
    held.resolve(json({})); await flush()
    expect(writes).toEqual([4, 2])
  })
  it('a late read cannot erase a newer dial edit', async () => {
    const read = deferred<Response>(), write = deferred<Response>()
    const { control } = setup(); await flush()
    mocks.api.mockImplementation((path: string, init?: RequestInit) => init?.method === 'PUT' ? write.promise : path === '/agents/plan' ? read.promise : Promise.resolve(json({ value: null })))
    const refresh = control.refresh()
    control.save({ total: 0, limits: { codex: 'off' } })
    read.resolve(json({ ...snapshot, total: 30, running_total: 13 })); await refresh
    expect(control.plan.value?.total).toBe(0)
    expect(control.snapshot.value?.running_total).toBe(13)
    write.resolve(json({}, 500)); await flush()
    expect(control.plan.value?.total).toBe(5)
    expect(control.error.value).toContain('Couldn’t save')
  })
  it('failure is visible and restores the last confirmed ceiling without touching running work', async () => {
    const { control } = setup(); await flush()
    mocks.api.mockImplementation(async (_path: string, init?: RequestInit) => init?.method === 'PUT' ? json({}, 403) : json(snapshot))
    control.save({ total: 0, limits: { codex: 'off' } }); await flush()
    expect(control.plan.value).toEqual({ total: 5, limits: { codex: 4 } })
    expect(control.snapshot.value?.running_total).toBe(12)
    expect(control.error.value).toContain('Couldn’t save')
    expect(control.saving.value).toBe(false)
  })
  it('drops pending writes and old responses when the viewer changes', async () => {
    const held = deferred<Response>(), writes: number[] = []
    const { control, viewer } = setup(); await flush()
    mocks.api.mockImplementation(async (path: string, init?: RequestInit) => {
      if (init?.method === 'PUT') { writes.push(JSON.parse(init.body as string).value.total); return held.promise }
      return path === '/agents/plan' ? json({ ...snapshot, total: 8, principal_id: 'new-owner' }) : json({ value: null })
    })
    control.save({ total: 4, limits: {} }); control.save({ total: 3, limits: {} })
    viewer.value = 'tenant:new-person'; await flush()
    held.resolve(json({})); await flush()
    expect(writes).toEqual([4])
    expect(control.plan.value?.total).toBe(8)
    expect(control.snapshot.value?.principal_id).toBe('new-owner')
    expect(control.error.value).toBe('')
  })
  it('fold memory is private to the viewer and independent of the start ceiling', async () => {
    const { control } = setup(); await flush()
    control.toggleFold(); await flush()
    expect(control.folded.value).toBe(true)
    const writes = mocks.api.mock.calls.filter(([, init]) => init?.method === 'PUT')
    expect(writes.map(([path]) => path)).toEqual([`/preferences/${FOLD_KEY}`])
    expect(JSON.parse(writes[0]![1].body)).toEqual({ value: { folded: true } })
    expect(control.plan.value?.total).toBe(5)
  })
  it('does not choose a total when the canonical read is forbidden', async () => {
    mocks.api.mockResolvedValue(json({}, 403))
    const { control } = setup(); await flush()
    expect(control.plan.value).toBeNull()
    expect(control.error.value).toContain('Couldn’t read')
    control.save({ total: 15, limits: {} }); await flush()
    expect(mocks.api.mock.calls.some(([, init]) => init?.method === 'PUT')).toBe(false)
  })
})
