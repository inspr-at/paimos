// SPDX-License-Identifier: AGPL-3.0-only
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { effectScope, ref, type EffectScope } from 'vue'
import type { PlanSnapshot } from '../src/lib/agentsWorking'
const mocks = vi.hoisted(() => ({ api: vi.fn(), queue: vi.fn() }))
vi.mock('../src/lib/api.ts', () => ({ api: mocks.api }))
vi.mock('../src/lib/workQueue.ts', () => ({ queueRequest: mocks.queue }))
vi.mock('../src/lib/usePolledData.ts', () => ({ usePoller: () => ({ start() {}, stop() {} }) }))
import { SETTLE_MS, useAgentPlan } from '../src/lib/useAgentPlan'
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
  mocks.api.mockImplementation(async (path: string, init?: RequestInit) => init?.method === 'PUT' ? json({ updated_at: '2026-10-02T18:00:00.000001Z' }) : path === '/agents/plan' ? json(snapshot) : json({ value: null }))
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
    expect(JSON.parse(writes[0]![1].body)).toEqual({ value: { total: 0, limits: { codex: 'off' } }, expected_updated_at: null })
  })
  it('serializes rapid clicks and coalesces edits behind a held write', async () => {
    const held = deferred<Response>(), writes: number[] = []
    mocks.api.mockImplementation(async (path: string, init?: RequestInit) => {
      if (init?.method !== 'PUT') return path === '/agents/plan' ? json(snapshot) : json({ value: null })
      const total = JSON.parse(init.body as string).value.total
      writes.push(total)
      return writes.length === 1 ? held.promise : json({ updated_at: '2026-10-02T18:00:00.000002Z' })
    })
    const { control } = setup(); await flush()
    control.save({ total: 4, limits: {} }); control.save({ total: 3, limits: {} }); control.save({ total: 2, limits: {} })
    expect(control.plan.value?.total).toBe(2); expect(writes).toEqual([4])
    held.resolve(json({ updated_at: '2026-10-02T18:00:00.000001Z' })); await flush()
    expect(writes).toEqual([4, 2])
    const requests = mocks.api.mock.calls.filter(([, init]) => init?.method === 'PUT')
    expect(requests.map(([, init]) => JSON.parse(init.body).expected_updated_at)).toEqual([null, '2026-10-02T18:00:00.000001Z'])
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
  it('a hold saves only its final value, once, after release', async () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    try {
      const { control } = setup(); await flush()
      const puts = () => mocks.api.mock.calls.filter(([, init]) => init?.method === 'PUT').map(([, init]) => JSON.parse(init.body).value.total)
      control.hold(true)
      control.save({ total: 6, limits: { codex: 4 } }); control.save({ total: 7, limits: { codex: 4 } }); control.save({ total: 8, limits: { codex: 4 } })
      await flush()
      expect(control.plan.value?.total).toBe(8)
      expect(puts()).toEqual([])
      control.hold(false)
      vi.advanceTimersByTime(SETTLE_MS - 1); await flush()
      expect(puts()).toEqual([])
      // A fresh press inside the settle time keeps waiting for its own release.
      control.hold(true); control.save({ total: 9, limits: { codex: 4 } }); control.hold(false)
      vi.advanceTimersByTime(SETTLE_MS - 1); await flush()
      expect(puts()).toEqual([])
      vi.advanceTimersByTime(1); await flush()
      expect(puts()).toEqual([9])
    } finally { vi.useRealTimers() }
  })
  it('a hold that starts during an in-flight write never saves its intermediate steps', async () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    try {
      const held = deferred<Response>(), writes: number[] = []
      const { control } = setup(); await flush()
      mocks.api.mockImplementation(async (path: string, init?: RequestInit) => {
        if (init?.method !== 'PUT') return path === '/agents/plan' ? json(snapshot) : json({ value: null })
        writes.push(JSON.parse(init.body as string).value.total)
        return writes.length === 1 ? held.promise : json({ updated_at: '2026-10-02T18:00:00.000002Z' })
      })
      control.save({ total: 4, limits: {} })
      control.hold(true); control.save({ total: 3, limits: {} }); control.save({ total: 2, limits: {} })
      held.resolve(json({ updated_at: '2026-10-02T18:00:00.000001Z' })); await flush()
      expect(writes).toEqual([4])
      expect(control.plan.value?.total).toBe(2)
      control.hold(false); vi.advanceTimersByTime(SETTLE_MS); await flush()
      expect(writes).toEqual([4, 2])
    } finally { vi.useRealTimers() }
  })
  it('a released edit is dropped, never written, when the viewer changes before it settles', async () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    try {
      const { control, viewer } = setup(); await flush()
      control.hold(true); control.save({ total: 9, limits: {} }); control.hold(false)
      viewer.value = 'tenant:new-person'; await flush()
      vi.advanceTimersByTime(SETTLE_MS * 2); await flush()
      expect(mocks.api.mock.calls.some(([, init]) => init?.method === 'PUT')).toBe(false)
    } finally { vi.useRealTimers() }
  })
  it('a page hidden within the settle time sends the released value with keepalive', async () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    const target = new EventTarget()
    vi.stubGlobal('window', target)
    try {
      const { control } = setup(); await flush()
      control.hold(true); control.save({ total: 12, limits: {} }); control.hold(false)
      target.dispatchEvent(new Event('pagehide'))
      const puts = mocks.api.mock.calls.filter(([, init]) => init?.method === 'PUT')
      expect(puts.map(([, init]) => [JSON.parse(init.body).value.total, init.keepalive])).toEqual([[12, true]])
    } finally { vi.useRealTimers(); vi.unstubAllGlobals() }
  })
  it('leaving the page within the settle time still sends the released value', async () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    try {
      const { control } = setup(); await flush()
      control.hold(true); control.save({ total: 11, limits: {} }); control.hold(false)
      scope.stop()
      const puts = mocks.api.mock.calls.filter(([, init]) => init?.method === 'PUT')
      expect(puts.map(([, init]) => JSON.parse(init.body).value.total)).toEqual([11])
    } finally { vi.useRealTimers() }
  })
  it('does not choose a total when the canonical read is forbidden', async () => {
    mocks.api.mockResolvedValue(json({}, 403))
    const { control } = setup(); await flush()
    expect(control.plan.value).toBeNull()
    expect(control.error.value).toContain('Couldn’t read')
    control.save({ total: 15, limits: {} }); await flush()
    expect(mocks.api.mock.calls.some(([, init]) => init?.method === 'PUT')).toBe(false)
  })
  it('retains a failed save across successful polls until another deliberate edit', async () => {
    const { control } = setup(); await flush()
    mocks.api.mockImplementation(async (path: string, init?: RequestInit) => init?.method === 'PUT' ? json({}, 500) : path === '/agents/plan' ? json({ ...snapshot, running_total: 13 }) : json({ value: null }))
    control.save({ total: 4, limits: {} }); await flush()
    await control.refresh()
    expect(control.error.value).toContain('Couldn’t save')
    expect(control.snapshot.value?.running_total).toBe(13)
    mocks.api.mockImplementation(async (path: string, init?: RequestInit) => init?.method === 'PUT' ? json({ updated_at: '2026-10-02T18:00:00.000001Z' }) : path === '/agents/plan' ? json(snapshot) : json({ value: null }))
    control.save({ total: 3, limits: {} }); await flush()
    expect(control.error.value).toBe('')
  })
  it('re-reads a conflicted plan and discards queued stale edits without overwriting newer limits', async () => {
    const { control } = setup(); await flush()
    const held = deferred<Response>(), newer = { ...snapshot, total: 7, limits: { claude: 'off' }, updated_at: '2026-10-02T18:00:00.000005Z' }
    mocks.api.mockImplementation(async (path: string, init?: RequestInit) => init?.method === 'PUT' ? held.promise : path === '/agents/plan' ? json(newer) : json({ value: null }))
    control.save({ total: 4, limits: { codex: 4 } })
    control.save({ total: 3, limits: { codex: 4 } })
    held.resolve(json({}, 409)); await flush()
    expect(control.plan.value).toEqual({ total: 7, limits: { claude: 'off' } })
    expect(control.error.value).toContain('changed elsewhere')
    expect(mocks.api.mock.calls.filter(([, init]) => init?.method === 'PUT')).toHaveLength(1)
    await control.refresh()
    expect(control.error.value).toContain('changed elsewhere')
    control.save({ total: 6, limits: { claude: 'off' } })
    const last = mocks.api.mock.calls.filter(([, init]) => init?.method === 'PUT').at(-1)!
    expect(JSON.parse(last[1].body).expected_updated_at).toBe(newer.updated_at)
  })
  it('does not save an unchanged total or harness selection', async () => {
    const { control } = setup(); await flush()
    control.save({ total: snapshot.total, limits: { ...snapshot.limits } }); await flush()
    expect(mocks.api.mock.calls.some(([, init]) => init?.method === 'PUT')).toBe(false)
  })
})
