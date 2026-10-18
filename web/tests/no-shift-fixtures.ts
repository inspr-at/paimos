// SPDX-License-Identifier: AGPL-3.0-only
import { type Page } from '@playwright/test'
import { fixtures, me, mockWork } from './work-fixtures'
import { agentData, mockAgents } from './agents-fixtures'
import { capacityWorld } from './capacity-fixtures'
import { mockEffectivePermissions } from './authz-fixtures'

import type { Shell } from './helpers/no-shift-shells'
export const session = (n: number) => `5e000000-0000-4000-8000-0000000000${String(n).padStart(2, '0')}`
export async function setup(page: Page, failDecision = false) {
  const work = fixtures()
  work.preferences['agents.working'] = { total: 9, limits: { codex: 1 } }
  await mockWork(page, work, { admin: true })
  const data = agentData({ me: me.id, projects: { pharos: 'p-pharos', aeon: 'p-aeon', pai: 'p-frozen' }, tickets: { fleet: 'n-1', restore: 'n-2', web: 'n-a1', release: 'n-5', approvals: 'n-6' } })
  const capacity = capacityWorld()
  data.accounts = (capacity.accounts as unknown as typeof data.accounts).map(a => ({ ...a, registered_by_principal_id: me.id }))
  await mockAgents(page, data, { capacity, failDecision, workingPreference: () => work.preferences['agents.working'] })
  await page.route('**/api/me/permissions*', route => {
    const answer = mockEffectivePermissions('admin', new URL(route.request().url()).searchParams.get('project_id') ?? undefined)
    answer.workspace.permissions.push('account.read', 'account.manage')
    return route.fulfill({ json: answer })
  })
  return data
}
