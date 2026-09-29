// SPDX-License-Identifier: AGPL-3.0-only
// The merged Usage page (AEON-301): the capacity world of the Agents desk plus a
// usage read from usage-data.ts.
import type { Page } from '@playwright/test'
import { fixtures, me, mockWork } from './work-fixtures'
import { agentData, mockAgents, type AgentWorld } from './agents-fixtures'
import { NOW, capacityWorld, type CapacityOptions } from './capacity-fixtures'
import { mockEffectivePermissions } from './authz-fixtures'
import { usageDashboard, type UsageVariant } from './usage-data'

export { NOW }
export const USAGE_TZ = 'Europe/Vienna'
const DAY = 86_400_000

const WORLD: AgentWorld = {
  me: me.id, now: NOW,
  projects: { pharos: 'p-pharos', aeon: 'p-aeon', pai: 'p-frozen' },
  tickets: { fleet: 'n-1', restore: 'n-2', web: 'n-a1', release: 'n-5', approvals: 'n-6' },
  nodes: {
    'p-pharos': { key: 'PRJ-17', title: 'Pharos' }, 'p-aeon': { key: 'PRJ-35', title: 'Aeon' }, 'p-frozen': { key: 'PRJ-26', title: 'Studio infrastructure' },
    'n-1': { key: 'PHAROS-11', title: 'Connect Hetzner Cloud for managed provisioning' }, 'n-2': { key: 'PHAROS-12', title: 'Add an Oracle Cloud connector' },
    'n-a1': { key: 'AEON-1', title: 'Aeon foundation' }, 'n-5': { key: 'PHAROS-15', title: 'Beacon health probes' }, 'n-6': { key: 'PHAROS-16', title: 'Retire the old dashboard' },
  },
}

export interface UsageSetup extends CapacityOptions {
  variant?: UsageVariant
  theme?: 'light' | 'dark'
  /** No account.read: the capacity band is hidden, the rest works. */
  noAccounts?: boolean
  /** No accounts enrolled at all. */
  noPools?: boolean
}

/** Mocks the app, the agents desk data, capacity and the usage read; returns the usage URLs requested. */
export async function setupUsage(page: Page, options: UsageSetup = {}) {
  await page.clock.setSystemTime(NOW)
  const data = fixtures()
  if (options.theme) data.preferences.theme = { choice: options.theme }
  await mockWork(page, data, { admin: true })
  const agents = agentData(WORLD)
  const capacity = capacityWorld(options)
  agents.accounts = (options.noPools ? [] : capacity.accounts) as unknown as typeof agents.accounts
  await mockAgents(page, agents, { capacity })
  if (options.noAccounts) {
    await page.route('**/api/agent-accounts**', route => route.fulfill({ status: 403, json: { error: 'forbidden' } }))
  }
  if (options.noPools) await page.route('**/api/agent-accounts/capacity', route => route.fulfill({ json: [] }))
  await page.route('**/api/me/permissions*', route => {
    const answer = mockEffectivePermissions('admin', new URL(route.request().url()).searchParams.get('project_id') ?? undefined)
    answer.workspace.permissions = [...answer.workspace.permissions, ...(options.noAccounts ? [] : ['account.read', 'account.manage']), 'run.read', 'models.read']
    return route.fulfill({ json: answer })
  })
  const calls: string[] = []
  await page.route('**/api/usage/dashboard**', async route => {
    const url = new URL(route.request().url())
    calls.push(url.href)
    const from = Date.parse(url.searchParams.get('from') ?? ''), to = Date.parse(url.searchParams.get('to') ?? '')
    const days = Number.isFinite(from) && Number.isFinite(to) ? Math.round((to - from) / DAY) : 30
    await route.fulfill({ json: usageDashboard(options.variant ?? 'unreported', days) })
  })
  return calls
}
