// SPDX-License-Identifier: AGPL-3.0-only
import type { Page } from '@playwright/test'
import { type Fixtures, me } from '../work-fixtures'
import { agentData, mockAgents } from '../agents-fixtures'
import { mockSettings, settingsData } from '../settings-fixtures'
import { businessData, mockBusiness } from '../business-fixtures'
import { mockReleases, releaseHistory } from '../releases-fixtures'
import { groupsWorld, mockProjectGroups } from '../project-groups-fixtures'

// Successful, explicit empty reads for unrelated chrome. A missing mock must
// not make a reference depict an accidental outage; unknown paths still fail.
export async function visualChrome(page: Page, data: Fixtures, now: number, options: { agents?: boolean } = {}) {
  await mockSettings(page, settingsData())
  await mockBusiness(page, businessData({ enabled: ['business_costs', 'business_crm', 'business_quotes', 'business_hours'] }), {
    grant: ['account.read', 'account.manage', 'account.use.manage', 'questions.read', 'questions.decide', 'approvals.read', 'approvals.revoke'],
  })
  await mockReleases(page, releaseHistory(now))
  await mockProjectGroups(page, data, groupsWorld())
  if (options.agents !== false) await mockAgents(page, agentData({ me: me.id, now, empty: true,
    projects: { pharos: 'p-pharos', aeon: 'p-aeon', pai: 'p-frozen' },
    tickets: { fleet: 'n-1', restore: 'n-2', web: 'n-a1', release: 'n-5', approvals: 'n-6' } }), { now: () => now })
  await page.route('**/api/**', route => {
    const request = route.request(), path = new URL(request.url()).pathname
    if (request.method() !== 'GET') return route.fallback()
    const json = (body: unknown) => route.fulfill({ json: body })
    if (path === '/api/models' || path === '/api/me/host-labels') return json([])
    if (path === '/api/quote-profiles') return json([])
    if (path === '/api/models/outdated') return json({ versions: [], revision: 'a'.repeat(64), incomplete: false, next_cursor: '' })
    if (path === '/api/recurrences') return json({ items: [], next_cursor: null })
    if (path === '/api/queue') return json({ items: [], manual_order: false, capacity: { queued_hours: 0, parallel_runs: 0, work_hours: null, warning: false } })
    if (/^\/api\/queue\/[^/]+\/readiness$/.test(path)) return json({ queueable: false, ready: false, missing: ['status'], suggested_estimate_hours: 1, security_review_required: false })
    const lead = /^\/api\/projects\/([^/]+)\/lead$/.exec(path)
    if (lead) return json({ project_id: lead[1], revision: 0, generation: 0, session_id: null, state: 'none', reason: '', process_active: false, automatic_launch_enabled: false })
    if (/^\/api\/projects\/[^/]+\/lead-settings$/.test(path)) return json({ revision: 1, details_redacted: false, automatic_launch_enabled: false, owner_person_id: null })
    if (/^\/api\/projects\/[^/]+\/release-memberships$/.test(path)) return json({ items: [], next_cursor: null })
    if (path === '/api/decision-desk') return json({ items: [], has_more: false })
    if (path === '/api/rules/doctrine/inbox/summary') return json({ pending: 0 })
    if (path === '/api/agent-pairing/computers') return json({ computers: [] })
    if (path === '/api/me/agent-pause-settings') return json({ default_level: 'pause' })
    if (path === '/api/settings/eta-interval') return json({ interval_minutes: 10 })
    if (path === '/api/me/leaving-at') return json({ deadline_at: null, request_id: null, stop_in_flight: false, hosts: 'all', items: [] })
    return route.fallback()
  })
}
