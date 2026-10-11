// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1204: role ladders moved to Settings › Models and PUT /models/routes now
// answers 410 Gone. The Policies Ladders tab shows one line with a link there and
// never reads or writes the retired ladder endpoints.
import { readFileSync } from 'node:fs'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import { createSSRApp, defineComponent } from 'vue'
import { renderToString } from '@vue/server-renderer'
import { createMemoryHistory, createRouter } from 'vue-router'

const mocks = vi.hoisted(() => ({
  grants: new Set<string>(),
  requests: [] as { path: string; method: string }[],
}))
vi.mock('../src/stores/session', () => ({ useSession: () => ({ identity: { tenant: { id: 'tenant-1' }, principal: { id: 'person-1', kind: 'person' } } }) }))
vi.mock('../src/lib/authz', async original => ({
  ...await original<typeof import('../src/lib/authz')>(),
  can: (permission: string) => mocks.grants.has(permission),
  permissionsKnown: () => true,
  permissionsAvailable: () => true,
  onAccessRevalidate: () => () => {},
}))
vi.mock('../src/lib/api', async original => ({
  ...await original<typeof import('../src/lib/api')>(),
  api: async (path: string, init?: RequestInit) => { mocks.requests.push({ path, method: init?.method ?? 'GET' }); return new Response('[]', { headers: { 'Content-Type': 'application/json' } }) },
}))
vi.mock('../src/components/settings/CrossFamilyReviewCard.vue', () => ({ default: defineComponent({ render: () => null }) }))

import PoliciesSection from '../src/components/settings/PoliciesSection.vue'

beforeEach(() => {
  mocks.grants = new Set(['models.read', 'roles.read'])
  mocks.requests = []
  vi.stubGlobal('fetch', async (input: RequestInfo | URL, init?: RequestInit) => {
    mocks.requests.push({ path: String(input instanceof Request ? input.url : input), method: init?.method ?? (input instanceof Request ? input.method : 'GET') })
    return new Response('[]', { headers: { 'Content-Type': 'application/json' } })
  })
})
afterEach(() => { vi.unstubAllGlobals() })

async function renderPolicies() {
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/:path(.*)*', component: defineComponent({ render: () => null }) }] })
  await router.push('/settings/policies'); await router.isReady()
  const app = createSSRApp(PoliciesSection); app.use(router)
  const html = await renderToString(app)
  // Let any request the setup started reach the transport (no timing sleep).
  for (let i = 0; i < 5; i++) await new Promise(resolve => setImmediate(resolve))
  return html
}
const ladderEndpoint = (request: { path: string }) => /\/models\/routes(\?|$)/.test(request.path)

test('the Ladders tab shows the moved line with its Settings › Models link and requests no ladder endpoint', async () => {
  const html = await renderPolicies()
  expect(mocks.requests.filter(ladderEndpoint)).toEqual([])
  expect(mocks.requests.some(request => request.method === 'PUT')).toBe(false)
  expect(html).toContain('data-testid="policies-ladders-moved"')
  expect(html).toMatch(/Role ladders moved to <a[^>]*href="\/settings\/models"[^>]*>/)
  expect(html).toMatch(/<span class="sr-only"[^>]*>Settings, Models<\/span><\/span><\/a>\.<\/span><\/p>/)
  // No ladder editor, role selector, ladder list or Save action is mounted.
  for (const gone of ['policy-edit', 'policy-save', 'policies-role', 'ladder-slot', 'Configured ladder', 'policies-row-link']) expect(html).not.toContain(gone)
})

test('without See models the tab says so and still requests no ladder endpoint', async () => {
  mocks.grants = new Set(['roles.read'])
  const html = await renderPolicies()
  expect(html).toMatch(/<p class="state-line"[^>]*>You can&#39;t see this: it needs See models\.<\/p>/)
  expect(html).not.toContain('data-testid="policies-ladders-moved"')
  expect(mocks.requests.filter(ladderEndpoint)).toEqual([])
})

test('Policies no longer imports the ladder editor or its writer', () => {
  const source = readFileSync(new URL('../src/components/settings/PoliciesSection.vue', import.meta.url), 'utf8')
  const policies = readFileSync(new URL('../src/lib/policies.ts', import.meta.url), 'utf8')
  for (const text of [source, policies]) expect(text).not.toMatch(/PolicyLadderEditor|policyModels|writeLadder|['`"/]models\/routes/)
})
