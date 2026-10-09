// SPDX-License-Identifier: AGPL-3.0-only
import { afterEach, expect, it, vi } from 'vitest'
import { draftFor, type DeskProjection } from '../src/lib/decisionDesk'
import { commitDesk, loadDesk, nativeTierAdapter } from '../src/lib/decisionDeskApi'
import type { HarnessSession } from '../src/lib/agents'

afterEach(() => vi.unstubAllGlobals())

const ownership = { daemon_id: 'daemon', generation: 'generation', process_id: 'process', root_pid: 123, group_id: 123, started_at: '2026-10-03T10:00:00Z' }
const session = { id: 'session', project_id: 'project', advertised_capabilities: ['service_tier_v1'], process_ownership: ownership } as HarnessSession
const projection: DeskProjection = { items: [{ id: 'request', kind: 'tier_request', project_id: 'project', revision: 0, created_at: '', held: false, can_decide: true,
  title: 'Tier request', href: '/decision-desk?item=t:request', source: '/api/projects/project/harness-sessions/session/tier' }], counts: { open: 1, held: 0, chores: 0 }, has_more: false, as_of: '' }
function tierFetch(revision: number | null) {
  return vi.fn(async (url: string, init?: RequestInit) => {
    if (!url.includes('/harness-sessions/session/tier')) return new Response(JSON.stringify(url.startsWith('/api/approvals?') ? [] : { items: [], has_more: false, pending: 0 }))
    const decision = init?.method === 'POST' ? JSON.parse(init.body as string).decision : undefined
    return new Response(JSON.stringify({ session_id: 'session', revision: decision ? 1 : revision, read_only: false,
      requests: [{ id: 'request', session_id: 'session', tier: 'fast', reason: 'Unblock the first run', state: decision === 'approve' ? 'approved' : decision === 'decline' ? 'declined' : 'pending', created_at: '' }] }))
  })
}

for (const decision of ['approve', 'decline'] as const) it(`hydrates and ${decision}s a first tier request with native revision zero`, async () => {
  const fetch = tierFetch(0); vi.stubGlobal('fetch', fetch)
  const adapter = nativeTierAdapter(async () => [session], () => true)
  const result = await loadDesk(undefined, { tiers: adapter }, projection)
  const item = result.items.find(item => item.id === 't:request')!
  expect(item.unavailable).toBeUndefined()
  expect(item).toMatchObject({ revision: 0, context: 'Unblock the first run', decided: false })
  const answer = await commitDesk(item, { ...draftFor(item), optionId: decision }, result.sources, `operation-${decision}`, false, { tiers: adapter })
  expect(answer).toMatchObject({ decided: true, optionId: decision })
  const writes = fetch.mock.calls.filter(([, init]) => init?.method === 'POST')
  expect(writes).toHaveLength(1)
  expect(writes[0]![0]).toBe('/api/projects/project/harness-sessions/session/tier/requests/request/decision')
  expect(JSON.parse(writes[0]![1]!.body as string)).toEqual({ request_id: `operation-${decision}`, tier: 'fast', expected_revision: 0, expected_ownership: ownership, decision })
})

for (const revision of [-1, 0.5, Number.MAX_SAFE_INTEGER + 1, null]) it(`keeps invalid native tier revision ${revision} unavailable`, async () => {
  const fetch = tierFetch(revision); vi.stubGlobal('fetch', fetch)
  const adapter = nativeTierAdapter(async () => [session], () => true)
  const result = await adapter.read(projection.items)
  expect(result.items).toEqual([])
  expect(result.warnings).toContain('Some tier requests could not be read. Open Agents to inspect their source.')
  expect(fetch.mock.calls.filter(([, init]) => init?.method === 'POST')).toEqual([])
})
