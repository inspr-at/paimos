// SPDX-License-Identifier: AGPL-3.0-only
import type { Page } from '@playwright/test'
import { fixtures, mockWork } from './work-fixtures'
import { mockEffectivePermissions } from './authz-fixtures'
import type { ReviewPolicySettings } from '../src/lib/reviewPolicy'

// The grants the built-in roles hold for what Policies links to (internal/authz/registry.go).
const GRANTS = {
  admin: ['models.read', 'roles.read', 'members.read', 'audit.read', 'rules.read', 'account.read', 'approvals.read', 'harness.read', 'nodes.read', 'settings.manage', 'reviewpolicy.read', 'reviewpolicy.manage'],
  member: ['models.read', 'members.read', 'rules.read', 'approvals.read', 'harness.read', 'nodes.read', 'reviewpolicy.read'],
}
export const reviewPolicy = (): ReviewPolicySettings => ({
  project_id: null, policy: { mode: 'other_family', allowed_families: [] }, effective: { mode: 'other_family', allowed_families: [] }, workspace: { mode: 'other_family', allowed_families: [] },
  valid_families: ['openai', 'anthropic', 'xai', 'cursor', 'google', 'local'], source: 'tenant', updated_by: 'editor', updated_at: '2026-10-11T08:00:00Z',
  family_order: ['openai', 'xai', 'anthropic', 'cursor', 'google', 'local'], floor: { tiers: ['strong', 'frontier'], min_effort_level: 4 }, security_excludes: ['anthropic'],
})
/** Settings › Policies for an admin or a member. `retired` records any read of the
 * ladder or registry endpoints the old page used; the new page makes none. */
export async function mockPolicies(page: Page, theme: 'light' | 'dark', role: 'admin' | 'member' = 'admin') {
  const work = fixtures()
  work.preferences.theme = { choice: theme }
  await mockWork(page, work, { admin: role === 'admin' })
  await page.route('**/api/me/permissions*', route => {
    const answer = mockEffectivePermissions(role, new URL(route.request().url()).searchParams.get('project_id') ?? undefined)
    answer.workspace.permissions = [...GRANTS[role]]
    return route.fulfill({ json: answer })
  })
  const retired: string[] = []
  await page.route(/\/api\/(models\/routes|authz\/permissions)(\?|$)/, route => { retired.push(new URL(route.request().url()).pathname); return route.fulfill({ status: 410, json: { error: 'gone' } }) })
  await page.route(/\/api\/settings\/review-policy$/, route => route.fulfill({ json: reviewPolicy() }))
  return { retired }
}

// AEON-1204: every request to the retired ladder endpoints, any method.
export function ladderRequests(page: Page) {
  const seen: string[] = []
  page.on('request', request => { if (/\/api\/models\/routes(\?|$)/.test(request.url())) seen.push(`${request.method()} ${new URL(request.url()).pathname}`) })
  return seen
}
