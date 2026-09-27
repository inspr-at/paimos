// SPDX-License-Identifier: AGPL-3.0-only
import { expect, test } from '@playwright/test'
import { fixtures, me, mockWork, watchErrors } from './work-fixtures'
import { SETUP_COMMAND, mockAnonymousGuide, mockPairing } from './agent-pairing-fixtures'

test('the public guide is readable without sign-in and keeps only the human code', async ({ page }) => {
  await mockAnonymousGuide(page)
  await page.goto('/agents/register-agent')
  await expect(page).toHaveURL('/agents/register-agent')
  await expect(page.getByRole('heading', { name: 'Connect a computer' })).toBeVisible()
  await expect(page.getByText(SETUP_COMMAND, { exact: false })).toBeVisible()
  await expect(page.getByText('This Aeon has not published a verified installer.')).toBeVisible()
  await expect(page.getByText(/curl\|sh|aeon\.barta\.cm/)).toHaveCount(0)
  await expect(page.getByRole('button', { name: 'Sign in to review the code' })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Connect computer' })).toHaveCount(0)

  await page.getByLabel('Pairing code').fill('123456789')
  await page.getByRole('button', { name: 'Sign in to review the code' }).click()
  await expect(page).toHaveURL(/\/signin\?return=/)
  await expect.poll(() => page.evaluate(() => sessionStorage.getItem('aeon.pairingUserCode'))).toBe('123-456-789')
  const stored = await page.evaluate(() => JSON.stringify(sessionStorage))
  expect(stored).not.toMatch(/device_secret|runtime_secret|lifecycle_secret/)
})

test('a person reviews real accounts, can leave a harness out, and does not treat approval as connected', async ({ page }) => {
  const errors = watchErrors(page)
  await mockWork(page, fixtures())
  const calls = await mockPairing(page)
  await page.goto('/agents/register-agent')
  await expect(page.getByRole('heading', { name: 'Connect a computer' })).toBeVisible()
  await page.getByLabel('Pairing code').fill('123-456-789')
  await page.getByRole('button', { name: 'Look up code' }).click()
  await expect(page.getByRole('heading', { name: 'Review this computer' })).toBeVisible()
  await expect(page.getByText('studio')).toBeVisible()
  await expect(page.getByText('INSPR')).toBeVisible()
  await expect(page.getByText('/Users/markus/work')).toBeVisible()
  await expect(page.getByText('Cursor work')).toBeVisible()
  await expect(page.getByText('Codex work')).toBeVisible()
  await expect(page.getByRole('checkbox', { name: 'Verify selected harnesses' })).toBeChecked()
  await expect(page.getByText('2026-09-27T20:30:00.000Z')).toBeVisible()
  await expect(page.getByText(/15-minute|15 minutes/)).toHaveCount(0)

  await page.getByRole('checkbox', { name: 'Connect Cursor' }).uncheck()
  await page.getByRole('radio', { name: /Set ongoing limits/ }).check()
  await expect(page.getByText('Cost in micros')).toHaveCount(0)
  await expect(page.getByText('Tokens')).toHaveCount(0)
  await expect(page.getByLabel('Requests')).toBeVisible()
  await page.getByRole('radio', { name: /Connect only/ }).check()

  await page.getByRole('button', { name: 'Connect computer', exact: true }).click()
  await expect(page.getByRole('heading', { name: 'Setting up' })).toBeVisible()
  await expect(page.getByText('The computer reported that setup finished, and a recent probe succeeded.')).toHaveCount(0)
  const approval = calls.find(call => call.path.endsWith('/approve'))
  expect(approval?.body).toEqual({
    request_digest: 'ab'.repeat(32),
    verification: 'one_per_harness',
    selected_account_keys: ['codex-1'],
  })
  expect(JSON.stringify(approval?.body)).not.toMatch(/device_secret|expected_revision|allowance/)

  await page.getByRole('button', { name: 'Disconnect' }).click()
  await page.getByRole('button', { name: 'Finish runs and disconnect' }).click()
  await expect.poll(() => calls.some(call => call.path.endsWith('/disconnect'))).toBe(true)
  const disconnect = calls.find(call => call.path.endsWith('/disconnect'))
  expect(disconnect?.body).toEqual({ mode: 'drain', expected_revision: 4 })
  expect(errors).toEqual([])
})

test('add harness is labeled from the request, and an agent cannot approve', async ({ page }) => {
  await mockWork(page, fixtures())
  await mockPairing(page)
  await page.route('**/api/me', route => {
    if (new URL(route.request().url()).pathname !== '/api/me') return route.fallback()
    return route.fulfill({ json: { principal: { id: me.id, name: 'Runtime', kind: 'agent' }, tenant: { id: 't1', name: 'INSPR Studio' } } })
  })
  await page.goto('/agents/register-agent')
  await page.getByLabel('Pairing code').fill('111-222-333')
  await page.getByRole('button', { name: 'Look up code' }).click()
  await expect(page.getByRole('heading', { name: 'Add a harness' })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Add harness', exact: true })).toBeDisabled()
  await expect(page.getByText('Only a signed-in person who can manage accounts can connect this computer.')).toBeVisible()
})

test('Agents links to Connect computer', async ({ page }) => {
  await mockWork(page, fixtures())
  await page.goto('/agents')
  await expect(page.getByRole('link', { name: 'Connect computer' })).toBeVisible()
})
