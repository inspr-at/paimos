// SPDX-License-Identifier: AGPL-3.0-only
import { afterEach, expect, it, vi } from 'vitest'
import type { Page, Route } from '@playwright/test'
import { agentData, mockAgents } from './agents-fixtures'

afterEach(() => vi.restoreAllMocks())

it('serves a projection after the browser has ended without evaluating the page', async () => {
  const now = Date.parse('2026-10-02T12:00:00Z')
  vi.spyOn(Date, 'now').mockReturnValue(now)
  let handler!: (route: Route) => Promise<unknown>
  const evaluate = vi.fn(() => { throw new Error('page.evaluate: Test ended.') })
  const page = { evaluate, route: async (_pattern: string, callback: typeof handler) => { handler = callback } } as unknown as Page
  const data = agentData({ me: 'person', projects: { pharos: 'pharos', aeon: 'aeon', pai: 'pai' }, tickets: { fleet: 'fleet', restore: 'restore', web: 'web', release: 'release', approvals: 'approvals' }, now })
  await mockAgents(page, data)
  let response!: { json: { as_of: string; counts: { open: number }; items: { id: string }[] } }
  await handler({
    request: () => ({ url: () => 'https://fixture.test/api/decision-desk/projection', method: () => 'GET', postDataJSON: () => null }),
    fulfill: async (value: typeof response) => { response = value },
  } as unknown as Route)
  expect(evaluate).not.toHaveBeenCalled()
  expect(response.json.as_of).toBe(new Date(now).toISOString())
  expect(response.json.counts.open).toBeGreaterThan(0)
  expect(response.json.items.some(item => item.id === data.approvals[0].id)).toBe(true)
  expect(response.json.items.some(item => item.id === data.approvals[3].id)).toBe(false)
})

// Risk: a historical browser clock must not expire approvals on the runner's date.
it('uses the injected projection clock for approval membership, counts and as_of', async () => {
  const now = Date.parse('2026-09-30T12:01:00Z')
  vi.spyOn(Date, 'now').mockReturnValue(Date.parse('2026-10-09T20:00:00Z'))
  let handler!: (route: Route) => Promise<unknown>
  const evaluate = vi.fn(() => { throw new Error('page.evaluate: Test ended.') })
  const page = { evaluate, route: async (_pattern: string, callback: typeof handler) => { handler = callback } } as unknown as Page
  const data = agentData({ me: 'person', projects: { pharos: 'pharos', aeon: 'aeon', pai: 'pai' }, tickets: { fleet: 'fleet', restore: 'restore', web: 'web', release: 'release', approvals: 'approvals' }, now })
  data.messages.splice(0)
  data.approvals.splice(1)
  const expires = Date.parse(String(data.approvals[0]!.expires_at))
  let clock = now
  await mockAgents(page, data, { now: () => clock })
  let response!: { json: { as_of: string; counts: { open: number }; items: { id: string }[] } }
  const read = () => handler({
    request: () => ({ url: () => 'https://fixture.test/api/decision-desk/projection', method: () => 'GET', postDataJSON: () => null }),
    fulfill: async (value: typeof response) => { response = value },
  } as unknown as Route)
  await read()
  expect(response.json.as_of).toBe(new Date(now).toISOString())
  expect(response.json.counts.open).toBe(1)
  expect(response.json.items.map(item => item.id)).toEqual([data.approvals[0]!.id])
  clock = expires
  await read()
  expect(response.json.as_of).toBe(new Date(expires).toISOString())
  expect(response.json.counts.open).toBe(0)
  expect(response.json.items).toEqual([])
  expect(evaluate).not.toHaveBeenCalled()
})
