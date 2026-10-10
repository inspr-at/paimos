// SPDX-License-Identifier: AGPL-3.0-only
import { afterEach, expect, it, vi } from 'vitest'
import { createSSRApp } from 'vue'
import { renderToString } from '@vue/server-renderer'
import { createMemoryHistory, createRouter } from 'vue-router'
import DecisionDeskPanel from '../src/components/agents/DecisionDeskPanel.vue'
import { useDecisionDesk } from '../src/stores/decisionDesk'
import { deskItemID, deskLinkOut, draftFor, type DeskProjection } from '../src/lib/decisionDesk'
import { commitDesk, loadDesk, nativeTierAdapter } from '../src/lib/decisionDeskApi'
import type { HarnessSession } from '../src/lib/agents'

vi.mock('../src/stores/decisionDesk', () => ({ useDecisionDesk: vi.fn() }))
// The panel resolves row projects (AEON-1057); no project or person is needed here.
vi.mock('../src/stores/session', () => ({ useSession: () => ({ identity: null, authenticationCurrent: () => true }) }))
vi.mock('../src/stores/projects', () => ({ useProjects: () => ({ byId: () => undefined, load: async () => {} }) }))
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

// Risk: a migrated tenant's account-matrix confirmation must stay reachable
// from the Desk instead of becoming a dead, undecidable source warning.
it('turns an account_matrix projection row into a settings link-out without a source warning', async () => {
  vi.stubGlobal('fetch', vi.fn(async (url: string) => new Response(JSON.stringify(url.startsWith('/api/approvals?') ? [] : { items: [], has_more: false, pending: 0 }))))
  const row = { id: 'a1b2c3d4-0000-4000-8000-000000000001', kind: 'account_matrix' as const, revision: 2, created_at: '2026-10-08T10:00:00Z', held: false, can_decide: true,
    title: 'Looks right? · Account matrix', href: '/settings/accounts#account-use', source: '/api/account-use' }
  const result = await loadDesk(undefined, {}, { items: [row], counts: { open: 1, held: 0, chores: 0 }, has_more: false, as_of: '' })
  expect(result.items).toHaveLength(1)
  expect(result.items[0]).toMatchObject({ id: `u:${row.id}`, kind: 'account_matrix', linkOut: '/settings/accounts#account-use', title: row.title, revision: 2, held: false, decided: false })
  expect(result.items[0]!.unavailable).toBeUndefined()
  expect(result.warnings).toEqual([])
  expect(deskItemID(row)).toBe(`u:${row.id}`)
  expect(deskLinkOut(row)).toBe('/settings/accounts#account-use')
  for (const href of ['//evil.example/settings', 'https://evil.example/', '/settings/accounts?x=1', '/decision-desk?item=u:x']) expect(deskLinkOut({ ...row, href })).toBe('')
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/:any(.*)*', component: { render: () => null } }] })
  const desk = { projection: { items: [row], counts: { open: 1, held: 0, chores: 0 }, has_more: false, as_of: '', truncated: false }, count: 1, error: '', loading: false, refresh: vi.fn() }
  vi.mocked(useDecisionDesk).mockReturnValue(desk as never)
  const html = await renderToString(createSSRApp(DecisionDeskPanel).use(router))
  expect(html).toContain('href="/settings/accounts#account-use"')
  expect(html).not.toContain('item=u')
  expect(html).not.toContain('undefined')
})
