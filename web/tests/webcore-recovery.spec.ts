// SPDX-License-Identifier: AGPL-3.0-only
import { expect, test } from '@playwright/test'
import { fixtures, mockWork } from './work-fixtures'
import { accessWorld, ME, mockAccess } from './access-fixtures'
import { knowledgeWorld, mockKnowledge } from './knowledge-fixtures'

test('timing failures restore confirmed values, keep both controls anchored and retry the requested value', async ({ page }) => {
  await mockWork(page, fixtures())
  await mockAccess(page, accessWorld())
  let failed = true
  let release!: () => void
  const held = new Promise<void>(resolve => { release = resolve })
  let writes = 0
  await page.route('**/api/settings/eta-interval', async route => {
    if (route.request().method() === 'GET') return route.fulfill({ json: { interval_minutes: 10 } })
    writes++
    if (failed) { await held; return route.fulfill({ status: 500, json: { message: 'refused' } }) }
    expect(route.request().postDataJSON()).toEqual({ interval_minutes: 20 })
    return route.fulfill({ json: { interval_minutes: 21 } })
  })
  await page.route('**/api/settings/heartbeat-lost', route => route.fulfill({ json: { heartbeat_lost_minutes: 15 } }))
  await page.goto('/settings/workspace')
  const estimates = page.getByLabel('Minutes between estimates'), lost = page.getByLabel('Minutes without a heartbeat')
  await expect(estimates).toHaveValue('10'); await expect(lost).toHaveValue('15')
  await estimates.fill('20'); await estimates.press('Tab')
  await expect(estimates).toBeDisabled()
  // Focus scrolls the next input into view. Measure after that intentional
  // scroll, while the response barrier still holds the asynchronous failure.
  const before = [await estimates.boundingBox(), await lost.boundingBox()]
  expect(writes).toBe(1)
  release()
  await expect(estimates).toHaveValue('10')
  await expect(page.getByRole('alert').filter({ hasText: 'Estimates:' })).toContainText('could not be confirmed')
  for (const [i, control] of [estimates, lost].entries()) expect(await control.boundingBox()).toEqual(before[i])
  failed = false
  await page.getByRole('button', { name: 'Retry 20 minutes' }).click()
  await expect(estimates).toHaveValue('21')
  await expect(page.locator('#eta-error')).toHaveCount(0)
  expect(writes).toBe(2)
})

test('knowledge keeps a failed second query visibly stale, with retry and the previous query named', async ({ page }) => {
  await mockWork(page, fixtures())
  await mockKnowledge(page, knowledgeWorld())
  let failed = true
  await page.route('**/api/knowledge?**', route => {
    const q = new URL(route.request().url()).searchParams.get('q')
    if (q === 'no-result-aeon-592' && failed) return route.fulfill({ status: 503, json: { message: 'Search unavailable' } })
    return route.fallback()
  })
  await page.goto('/knowledge?q=deploy')
  await expect(page.locator('.kp-results')).toContainText('Deploy a release to production')
  const search = page.getByRole('searchbox', { name: 'Search knowledge in every project' })
  const bounds = await search.boundingBox()
  await search.fill('no-result-aeon-592')
  await expect(page.getByRole('alert')).toContainText('The server could not do that just now. Please try again.')
  await expect(page.getByRole('status').filter({ hasText: 'Previous results for “deploy”' })).toBeVisible()
  await expect(page.locator('.kp-results')).toHaveClass(/stale/)
  expect(await search.boundingBox()).toEqual(bounds)
  failed = false
  await page.getByRole('button', { name: 'Try again', exact: true }).click()
  await expect(page.locator('.kp-results')).not.toHaveClass(/stale/)
  await expect(page.locator('.kp-results')).toContainText('Nothing matches “no-result-aeon-592”')
})

test('a 2051-event access history shows its newest revocation and accurately reports the bounded window', async ({ page }) => {
  await mockWork(page, fixtures())
  const world = accessWorld()
  world.events = Array.from({ length: 2051 }, (_, index) => ({
    id: index + 1, actor_principal_id: ME, type: index === 2050 ? 'agent_key.revoked' : 'binding.set', before: null,
    after: index === 2050 ? { name: 'Newest revoked key' } : { principal_id: ME, role_id: 'role-member' }, at: '2026-10-02T12:00:00Z',
  }))
  await mockAccess(page, world)
  await page.goto('/settings/access/audit')
  await expect(page.locator('.audit-tab .summary')).toHaveText('2000 changes (the most recent; older ones are not shown)')
  await expect(page.locator('.audit-tab .event').first()).toContainText('Newest revoked key')
  await expect(page.locator('.audit-tab .event')).toHaveCount(2000)
})
