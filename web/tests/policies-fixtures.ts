// SPDX-License-Identifier: AGPL-3.0-only
import type { Page } from '@playwright/test'
import { fixtures, mockWork } from './work-fixtures'
import { mockEffectivePermissions } from './authz-fixtures'
import type { Permission } from '../src/lib/access'
import type { PolicyLadder, PolicyRole, PolicyStep } from '../src/lib/policies'

function barrier() { let release!: () => void; const promise = new Promise<void>(resolve => { release = resolve }); return { promise, release } }
export function policyLadder(role: PolicyRole, count = 2): PolicyLadder {
  const steps: PolicyStep[] = Array.from({ length: Math.min(count, 50) }, (_, i) => ({
    role, priority: i + 1, profile_id: `profile-${i}`, state: i === 1 ? 'unavailable' : 'available', reason: i === 1 ? 'out of credit' : '', valid_until: i === 1 ? '2099-10-02T19:30:00Z' : null,
    profile: { id: `profile-${i}`, slug: `fixture-${i}`, display_name: i === 0 ? 'First model' : 'Fallback model', model_version: '1', model: `fixture-${i}`, family: i === 0 ? 'xai' : 'anthropic', harness: i === 0 ? 'grok' : 'claude', effort: 'xhigh', tier: 'frontier', enabled: true },
  }))
  return { role, steps, routes: steps.map(({ profile: _profile, ...route }) => route), edit_token: count > 50 ? null : '"' + 'a'.repeat(64) + '"', can_edit: count <= 50, setup: true, truncated: count > 50, dispatch_family_order: ['openai', 'xai', 'anthropic', 'cursor', 'google', 'local'], review_floors: ['A reviewer must be from a different family than the author.', 'Review dispatch requires a frontier or strong profile at xhigh reasoning.', 'Dispatch checks platform capability, an approved account with capacity, and model preferences.'] }
}
export const policyRegistry: Permission[] = [
  { key: 'rules.publish', group: 'Rules', description: 'Publish agent rules', risk: 'high', agent_grantable: false, grantable_at: ['workspace'] },
  { key: 'keys.read', group: 'Keys', description: 'See agent key metadata', risk: 'medium', agent_grantable: false, grantable_at: ['workspace'] },
  { key: 'models.read', group: 'Models', description: 'Read models', risk: 'low', agent_grantable: true, grantable_at: ['workspace'] },
]
export async function mockPolicies(page: Page, theme: 'light' | 'dark', restricted = false) {
  const work = fixtures()
  work.preferences.theme = { choice: theme }
  await mockWork(page, work, { admin: !restricted })
  const data = { mode: 'loaded' as 'loaded' | 'failed' | 'denied' | 'unseeded', count: 2, calls: [] as string[], grants: restricted ? ['settings.read'] : ['models.read', 'roles.read', 'members.read', 'audit.read', 'rules.read', 'account.read', 'approvals.read', 'harness.read'] }
  let next: { until: ReturnType<typeof barrier>; started: ReturnType<typeof barrier> } | null = null
  await page.route('**/api/me/permissions*', route => {
    const answer = mockEffectivePermissions(restricted ? 'member' : 'admin', new URL(route.request().url()).searchParams.get('project_id') ?? undefined)
    answer.workspace.permissions = [...data.grants]
    return route.fulfill({ json: answer })
  })
  await page.route(/\/api\/(models\/routes|authz\/permissions)(\?|$)/, async route => {
    data.calls.push(new URL(route.request().url()).pathname)
    const held = next; next = null
    const mode = data.mode, count = data.count
    held?.started.release()
    if (held) await held.until.promise
    if (mode === 'failed') return route.fulfill({ status: 503, headers: { 'Retry-After': '1' }, json: { error: 'Review ladder read timed out; retry shortly.' } })
    if (mode === 'denied') return route.fulfill({ status: 403, json: { error: 'permission denied' } })
    if (new URL(route.request().url()).pathname === '/api/authz/permissions') return route.fulfill({ json: policyRegistry })
    const role = new URL(route.request().url()).searchParams.get('role') as PolicyRole
    const answer = policyLadder(role, count)
    if (mode === 'unseeded') { answer.setup = false; answer.steps = []; answer.routes = []; answer.can_edit = false }
    return route.fulfill({ json: answer })
  })
  return { data, holdNext() { const held = { until: barrier(), started: barrier() }; next = held; return { started: held.started.promise, release: held.until.release } } }
}
