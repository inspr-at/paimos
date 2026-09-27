// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect } from '@playwright/test'
import { fixtures, mockWork, me } from './work-fixtures'
import { journeyWorld, mockJourney } from './journey-fixtures'

function renewalWorld(state: 'pending' | 'approved' | 'expired' | 'missing' = 'pending') {
  const world = journeyWorld('deploy')
  const template = world.approvals[0]
  const future = new Date(Date.now() + 3_600_000).toISOString()
  const past = new Date(Date.now() - 60_000).toISOString()
  world.approvals = []
  for (const gate of ['candidate', 'deploy'] as const) {
    world.approvals.push({ ...template, id: `old-${gate}`, scope: `journey.${gate}`, decision: 'approved', decided_by_principal_id: me.id, expires_at: past })
    if (state !== 'missing') world.approvals.push({ ...template, id: `fresh-${gate}`, scope: `journey.${gate}`, decision: state === 'pending' ? null : 'approved', decided_by_principal_id: state === 'pending' ? null : me.id, expires_at: state === 'expired' ? past : future })
    Object.assign(world.journey.stages.find(s => s.key === (gate === 'candidate' ? 'build' : 'deploy'))!, {
      gate_approval_id: `old-${gate}`, gate_live: false,
      ...(state === 'missing' ? {} : { gate_offer_id: `fresh-${gate}`, gate_offer_state: state === 'pending' ? 'pending' : state === 'expired' ? 'expired' : 'approved_live', gate_offer_expires_at: state === 'expired' ? past : future }),
    })
  }
  world.journey.next_action = { key: 'approve_candidate', renewal_action: 'renew_candidate', label: 'Renew candidate approval', stage: 'deploy', available: state === 'approved', reason: 'The standing candidate gate is no longer live. A fresh approval is required.', approval_request_id: state === 'missing' ? null : 'fresh-candidate' }
  world.journey.launch_readiness = { can_admit: false, reason: 'Candidate gate is not approved.' }
  world.walkers['r-2'].state = 'deploying'
  return world
}

for (const width of [1600, 390]) {
  test(`renews both standing gates with fresh exact requests at ${width}`, async ({ page }, testInfo) => {
    await page.setViewportSize({ width, height: 1000 })
    await mockWork(page, fixtures())
    const world = renewalWorld()
    const calls = await mockJourney(page, world)
    await page.goto('/p/PHAROS?view=journey')
    await expect(page.getByText('Apply a fresh candidate approval to this release.', { exact: false }).first()).toBeVisible()
    await expect(page.getByText('Applied by you · no longer live').first()).toBeVisible()
    await page.screenshot({ path: testInfo.outputPath('candidate-renewal.png'), fullPage: true })
    await page.getByRole('button', { name: 'Approve and renew candidate approval', exact: true }).click()
    const dialog = page.getByRole('dialog', { name: 'Renew candidate approval?' })
    await expect(dialog).toContainText('fresh evidence')
    await dialog.getByRole('button', { name: 'Approve and renew candidate approval' }).click()
    await expect.poll(() => calls.filter(c => c.path.endsWith('/journey/actions')).length).toBe(1)
    expect(calls.find(c => c.path.endsWith('/journey/actions'))!.body).toMatchObject({ action: 'renew_candidate', release_id: 'r-2', expected_revision: 12, approval_request_id: 'fresh-candidate' })
    // The new projection still requires a separate deployment decision.
    await expect(page.getByRole('button', { name: 'Approve and renew deployment approval', exact: true })).toBeEnabled()
    await page.screenshot({ path: testInfo.outputPath('deployment-renewal.png'), fullPage: true })
    await page.getByRole('button', { name: 'Approve and renew deployment approval', exact: true }).click()
    await page.getByRole('dialog', { name: 'Renew deployment approval?' }).getByRole('button', { name: 'Approve and renew deployment approval' }).click()
    await expect.poll(() => calls.filter(c => c.path.endsWith('/journey/actions')).length).toBe(2)
    expect(calls.filter(c => c.path.endsWith('/journey/actions'))[1].body).toMatchObject({ action: 'renew_deploy', release_id: 'r-2', expected_revision: 13, approval_request_id: 'fresh-deploy' })
    expect(calls.filter(c => c.path.endsWith('/decision')).map(c => c.path)).toEqual(['/api/approvals/fresh-candidate/decision', '/api/approvals/fresh-deploy/decision'])
    await expect(page.getByRole('button', { name: 'Await deployment evidence', exact: true })).toBeDisabled()
  })
}

for (const state of ['expired', 'missing'] as const) test(`renewal requires a fresh ${state === 'missing' ? 'request' : 'live grant'}`, async ({ page }) => {
  await mockWork(page, fixtures())
  const calls = await mockJourney(page, renewalWorld(state))
  await page.goto('/p/PHAROS?view=journey')
  await expect(page.getByRole('button', { name: 'Renew candidate approval', exact: true })).toBeDisabled()
  expect(calls.filter(c => c.method !== 'GET')).toEqual([])
})

test('candidate renewal stays in Deploy when reviewing Build history', async ({ page }) => {
  await mockWork(page, fixtures())
  await mockJourney(page, renewalWorld('approved'))
  await page.goto('/p/PHAROS?view=journey&stage=build')
  await expect(page.getByText('The candidate approval is no longer live. Renew it in Deploy.', { exact: false })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Send it back', exact: true })).toHaveCount(0)
  await page.getByRole('button', { name: 'Deploy', exact: true }).click()
  await expect(page.getByRole('button', { name: 'Renew candidate approval', exact: true })).toBeEnabled()
})

test('renewal confirmation rejects a changed renewal action', async ({ page }) => {
  await mockWork(page, fixtures())
  const world = renewalWorld('approved')
  const calls = await mockJourney(page, world)
  await page.goto('/p/PHAROS?view=journey')
  await expect(page.getByRole('button', { name: 'Renew candidate approval', exact: true })).toBeEnabled()
  // Start a refresh before the modal opens to exercise an in-flight update.
  let finish!: () => void
  const held = new Promise<void>(resolve => { finish = resolve })
  let started = false
  await page.route('**/api/projects/p-pharos/journey', async route => {
    started = true
    await held
    await route.fulfill({ json: world.journey })
  })
  await page.evaluate(() => window.dispatchEvent(new Event('online')))
  await expect.poll(() => started).toBe(true)
  await page.getByRole('button', { name: 'Renew candidate approval', exact: true }).click()
  delete world.journey.next_action.renewal_action
  world.journey.next_action.label = 'Review updated decision'
  finish()
  await expect(page.getByRole('button', { name: /^Journey:.*Next: Review updated decision/ })).toBeVisible()
  await page.getByRole('dialog', { name: 'Renew candidate approval?' }).getByRole('button', { name: 'Renew candidate approval' }).click()
  await expect(page.locator('.toast').filter({ hasText: /review.*again/i })).toBeVisible()
  expect(calls.filter(c => c.method !== 'GET')).toEqual([])
})
