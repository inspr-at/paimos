// SPDX-License-Identifier: AGPL-3.0-only
// AEON-211: a deploy approval shows its server on the decision card, in Needs you,
// and in history. A legacy request with no target stays unknown.
import { test, expect, type Page } from '@playwright/test'
import { fixtures, me, mockWork } from './work-fixtures'
import { agentData, mockAgents, type AgentWorld } from './agents-fixtures'

const world: AgentWorld = {
  me: me.id,
  projects: { pharos: 'p-pharos', aeon: 'p-aeon', pai: 'p-frozen' },
  tickets: { fleet: 'n-1', restore: 'n-2', web: 'n-a1', release: 'n-5', approvals: 'n-6' },
  nodes: {
    'p-pharos': { key: 'PRJ-17', title: 'Pharos' }, 'p-aeon': { key: 'PRJ-35', title: 'Aeon' }, 'p-frozen': { key: 'PRJ-26', title: 'Studio infrastructure' },
    'n-1': { key: 'PHAROS-11', title: 'Connect Hetzner Cloud for managed provisioning' },
  },
}
const digest = 'ab'.repeat(32)
const otherDigest = 'cd'.repeat(32)
const namedTarget = {
  hosts: ['app-01'], environment: 'production', service: 'pharos',
  image: 'ghcr.io/inspr-at/pharos:260923120000.0.0', change: 'Roll pharos to 260923120000.0.0',
}
const ahead = (minutes: number) => new Date(Date.now() + minutes * 60_000).toISOString()
const ago = (minutes: number) => new Date(Date.now() - minutes * 60_000).toISOString()
// AEON-569 / #458 and AEON-1209 / #586 moved person decisions into the Desk.
const request = (page: Page) => page.getByRole('region', { name: 'Permission request', exact: true })

async function openAgents(page: Page) {
  await mockWork(page, fixtures(), { admin: true })
  const data = agentData(world)
  data.approvals.unshift(
    {
      id: 'a9000000-0000-4000-8000-000000000091', agent_principal_id: 'a0000000-0000-4000-8000-000000000001', agent_name: 'claude:camy',
      scope: 'journey.deploy', resource_kind: 'node', resource_id: 'p-pharos', run_id: null, rationale: 'The session on imac0 is ready.',
      expires_at: ahead(4), proposed_at: ago(2), decision: null, decided_by_principal_id: null, risk: 'high', target: namedTarget, target_digest_sha256: digest,
    },
    {
      id: 'a9000000-0000-4000-8000-000000000092', agent_principal_id: 'a0000000-0000-4000-8000-000000000002', agent_name: 'codex:nova',
      scope: 'journey.deploy', resource_kind: 'node', resource_id: 'p-aeon', run_id: null, rationale: 'Deploy wherever the session host is.',
      expires_at: ahead(5), proposed_at: ago(3), decision: null, decided_by_principal_id: null, risk: 'high',
    },
    {
      id: 'a9000000-0000-4000-8000-000000000093', agent_principal_id: 'a0000000-0000-4000-8000-000000000007', agent_name: 'codex:orbit',
      scope: 'journey.deploy', resource_kind: 'node', resource_id: 'p-pharos', run_id: null, rationale: 'Approved before targets existed.',
      expires_at: ago(30), proposed_at: ago(40), decision: 'approved', decided_by_principal_id: me.id, risk: 'high',
      target: { ...namedTarget, hosts: ['app-02'], environment: 'staging' }, target_digest_sha256: otherDigest,
    },
  )
  await mockAgents(page, data)
  await page.goto('/decision-desk?item=a:a9000000-0000-4000-8000-000000000091')
  await expect(request(page).getByText('app-01 · production', { exact: true })).toBeVisible()
  return data
}


test('Needs you names destinations and keeps target-less approvals usable', async ({ page }) => {
  await openAgents(page)
  const named = request(page)
  await expect(named.getByRole('region', { name: /Deploy target: app-01/ })).toContainText(namedTarget.change)
  await expect(named.getByText('named by the agent', { exact: true })).toBeVisible()
  await expect(named).not.toContainText('imac0')
  await page.getByTestId('desk-close').click()
  await page.getByTestId('desk-row-a:a9000000-0000-4000-8000-000000000092').click()
  await expect(request(page).getByRole('region', { name: 'Target not named' })).toBeVisible()
  await page.getByTestId('choice-0').click()
  await expect(page.getByRole('textbox', { name: 'Reason', exact: true })).toBeEnabled()
  await page.getByTestId('desk-decide').click()
  await expect(page.getByTestId('desk-status')).toContainText('Recorded')
  await page.getByTestId('desk-close').click()
  await expect(page.getByTestId('desk-row-a:a9000000-0000-4000-8000-000000000092')).toHaveCount(0)
  await page.getByRole('button', { name: /^Decided/ }).click()
  await page.getByTestId('desk-row-a:a9000000-0000-4000-8000-000000000093').click()
  await expect(request(page)).toContainText('app-02 · staging')
  await expect(page.getByTestId('desk-body')).toContainText('Approved')
})
