// SPDX-License-Identifier: AGPL-3.0-only
import { afterEach, expect, it, vi } from 'vitest'
import type { Page, Route } from '@playwright/test'
import { fixtures, mockWork, type Fixtures } from './work-fixtures'

afterEach(() => { vi.useRealTimers(); vi.unstubAllGlobals() })

it('HTTP snapshot scenarios keep a healthy stream while explicit event transports remain in control', async () => {
  vi.useFakeTimers()
  const browser = { EventSource: class EventSource extends EventTarget { close() {} } }
  vi.stubGlobal('window', browser)
  const page = { addInitScript: async (script: () => void) => script(), route: async () => {} } as unknown as Page
  await mockWork(page, fixtures())
  const stream = new browser.EventSource(), ping = vi.fn()
  stream.addEventListener('stream.ping', ping)
  vi.advanceTimersByTime(5_000)
  expect(ping).toHaveBeenCalledTimes(1)
  stream.close()
  vi.advanceTimersByTime(10_000)
  expect(ping).toHaveBeenCalledTimes(1)
  class ExplicitStream extends EventTarget { close() {} }
  browser.EventSource = ExplicitStream
  await mockWork(page, fixtures())
  expect(browser.EventSource).toBe(ExplicitStream)
})

async function world(data: Fixtures) {
  let handle!: (route: Route) => Promise<unknown>
  await mockWork({ addInitScript: async () => {}, route: async (_pattern: string, handler: typeof handle) => { handle = handler } } as unknown as Page, data)
  return async (path: string, method = 'GET', body: unknown = null) => {
    let answer!: { status?: number; json?: { items?: { id: string }[]; facets?: Record<string, Record<string, number>>; skipped?: { id: string; code?: string }[] } }
    await handle({
      request: () => ({ url: () => `https://fixture.test/api${path}`, method: () => method, postDataJSON: () => body, headers: () => ({}) }),
      fulfill: async (value: typeof answer) => { answer = value },
    } as unknown as Route)
    return answer
  }
}

it('leaf facets count terminal work across retired kinds without removing their parents', async () => {
  const data = fixtures(), original = data.nodes.map(node => node.id)
  const request = await world(data)
  const leaves = await request('/nodes?within=n-epic&kind=work,epic,ticket,task&shape=leaf&facets=state&sort=key')
  expect(leaves.json?.items?.map(node => node.id)).toEqual(['n-1', 'n-3'])
  expect(leaves.json?.facets?.state).toEqual({ 'in-progress': 1, qa: 1 })
  const parents = await request('/nodes?within=n-epic&kind=work,epic,ticket,task&shape=parent')
  expect(parents.json?.items?.map(node => node.id)).toEqual(['n-2'])
  expect(data.nodes.map(node => node.id)).toEqual(original)
  expect(data.nodes.find(node => node.id === 'n-3')?.parent_id).toBe('n-2')
})

it('explicit canonical shape wins over visible children and non-work children do not create a work parent', async () => {
  const data = fixtures()
  const hiddenParent = data.nodes.find(node => node.id === 'n-4')!
  hiddenParent.is_leaf = false
  data.nodes.push({ ...hiddenParent, id: 'file', kind_slug: 'file', parent_id: 'n-5', is_leaf: undefined })
  const request = await world(data)
  const parents = await request('/nodes?ids=n-4,n-5&shape=parent')
  expect(parents.json?.items?.map(node => node.id)).toEqual(['n-4'])
  const leaves = await request('/nodes?ids=n-4,n-5&shape=!parent')
  expect(leaves.json?.items?.map(node => node.id)).toEqual(['n-5'])
})

it('bulk nesting accepts a retired task kind and preserves a separately stale record', async () => {
  const data = fixtures(), request = await world(data)
  const task = data.nodes.find(node => node.id === 'n-3')!
  const stale = data.nodes.find(node => node.id === 'n-4')!
  const seen = stale.updated_at
  stale.updated_at = '2026-09-23T13:00:00.000Z'
  const result = await request('/nodes/bulk', 'POST', {
    ids: [task.id, stale.id], parent_id: 'n-epic', if_unmodified_since: { [task.id]: task.updated_at, [stale.id]: seen },
  })
  expect(result.json?.items?.map(node => node.id)).toEqual(['n-3'])
  expect(result.json?.skipped).toEqual([{ id: 'n-4', key: 'PHAROS-14', reason: 'changed since you loaded it', code: 'conflict' }])
  expect(task.parent_id).toBe('n-epic')
  expect(stale.parent_id).toBe('p-pharos')
  expect(stale.updated_at).toBe('2026-09-23T13:00:00.000Z')
})
