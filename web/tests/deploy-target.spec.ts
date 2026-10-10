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
const queue = (page: Page) => page.getByRole('region', { name: 'Needs you' })

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
  await page.goto('/agents')
  await expect(page.getByRole('heading', { name: 'Agents', level: 1 })).toBeVisible()
  await expect(queue(page).getByText('app-01 · production', { exact: true }).first()).toBeVisible()
  return data
}


function namedJourney() {
  const world = journeyWorld('deploy')
  Object.assign(world.approvals[0], { target: namedTarget, target_digest_sha256: digest })
  Object.assign(world.journey.stages.find(stage => stage.key === 'deploy')!, { target: namedTarget, target_digest_sha256: digest })
  return world
}

test('Needs you names destinations and keeps target-less approvals usable', async ({ page }) => {
  await openAgents(page)
  const named = queue(page).locator('.item', { hasText: 'app-01' })
  await expect(named.getByRole('region', { name: /Deploy target: app-01/ })).toContainText(namedTarget.change)
  await expect(named.getByText('named by the agent', { exact: true })).toBeVisible()
  await expect(named.getByRole('region')).not.toContainText('imac0')
  const absent = queue(page).locator('.item', { hasText: 'Deploy wherever the session host is' })
  await expect(absent.getByRole('region', { name: 'Target not named' })).toBeVisible()
  await absent.getByRole('button', { name: 'Approve', exact: true }).click()
  await expect(absent.getByLabel('Reason (optional)')).toBeVisible()
  await absent.getByRole('button', { name: 'Approve permission', exact: true }).click()
  await expect(absent).toHaveCount(0)
  await queue(page).getByRole('button', { name: /^Decided/ }).click()
  const historical = queue(page).locator('.past', { hasText: 'app-02' })
  await expect(historical).toContainText('staging')
  await expect(historical.locator('.past-outcome')).toHaveText('Approved')
})

for (const width of [390, 1600]) for (const named of [true, false]) {
}
