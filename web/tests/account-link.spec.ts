// SPDX-License-Identifier: AGPL-3.0-only
import { mkdirSync } from 'node:fs'
import { join } from 'node:path'
import AxeBuilder from '@axe-core/playwright'
import { expect, test, type Page } from '@playwright/test'
import { fixtures, me, mockWork } from './work-fixtures'
import type { AccountLinkReview } from '../src/lib/accountLink'

const account = '22222222-2222-4222-8222-222222222222'
const request = '33333333-3333-4333-8333-333333333333'
const initial: AccountLinkReview = {
  request_id: request, tenant_id: 't1', tenant_name: 'INSPR Studio', account_id: account,
  harness: 'claude', account_label: 'markus@example.test', computer_name: 'Shared workstation',
  person_id: me.id, person_name: 'Markus', revision: 0, state: 'pending',
  expires_at: new Date(Date.now() + 10 * 60_000).toISOString(), request_digest: 'a'.repeat(64),
}
async function setup(page: Page, options: { agent?: boolean; lookupStatus?: number; theme?: 'light' | 'dark' } = {}) {
  const data = fixtures()
  data.preferences.theme = { choice: options.theme ?? 'light' }
  await mockWork(page, data)
  if (options.agent) await page.route('**/api/me', route => route.fulfill({ json: { principal: { id: me.id, name: 'An agent', kind: 'agent' }, tenant: { id: 't1', name: 'INSPR Studio' } } }))
  let own: AccountLinkReview[] = []
  const writes: { path: string; body: unknown }[] = []
  const lookups: unknown[] = []
  await page.route('**/api/agent-pairing/account-links', route => route.fulfill({ json: own }))
  await page.route('**/api/agent-pairing/account-link/lookup', route => {
    lookups.push(route.request().postDataJSON())
    return route.fulfill({ status: options.lookupStatus ?? 200, json: options.lookupStatus ? { code: 'code_expired' } : initial })
  })
  await page.route(`**/api/agent-pairing/account-link/${request}/approve`, route => {
    writes.push({ path: route.request().url(), body: route.request().postDataJSON() })
    own = [{ ...initial, state: 'linked' }]
    return route.fulfill({ json: own[0] })
  })
  await page.route(`**/api/agent-pairing/account-links/${account}/unlink`, route => {
    writes.push({ path: route.request().url(), body: route.request().postDataJSON() }); own = []
    return route.fulfill({ status: 204 })
  })
  return { writes, lookups }
}

test('one person confirmation names account, computer and workspace; unlink is one action', async ({ page }) => {
  const calls = await setup(page)
  await page.goto('/link')
  await page.getByLabel('Account code').fill('482 913')
  await expect(page.getByRole('heading', { name: 'Link Claude (markus@example.test) to Markus?' })).toBeVisible()
  await expect(page.getByText('Shared workstation · INSPR Studio', { exact: true })).toBeVisible()
  expect(calls.lookups).toEqual([{ user_code: '482913' }])
  expect(calls.writes).toHaveLength(0)
  await page.getByRole('button', { name: 'Link account', exact: true }).click()
  await expect(page.getByRole('heading', { name: 'Linked to Markus' })).toBeVisible()
  expect(calls.writes).toHaveLength(1)
  expect(calls.writes[0]!.body).toEqual({ tenant_id: 't1', person_id: me.id, expected_revision: 0, request_digest: initial.request_digest, user_code: '482913' })
  await page.getByRole('button', { name: 'Unlink Claude (markus@example.test)' }).click()
  await expect(page.getByRole('button', { name: /^Unlink/ })).toHaveCount(0)
  expect(calls.writes).toHaveLength(2)
  expect(calls.writes[1]!.body).toEqual({ expected_revision: 0 })
  expect(await page.evaluate(() => [location.href, ...Object.values(sessionStorage), ...Object.values(localStorage)].join(' '))).not.toContain('482913')
})
test('agent sessions cannot look up, confirm or unlink', async ({ page }) => {
  const calls = await setup(page, { agent: true })
  await page.goto('/link')
  await expect(page.getByText('Only a signed-in person can link their own account.')).toBeVisible()
  await expect(page.getByLabel('Account code')).toHaveCount(0)
  expect(calls.writes).toHaveLength(0); expect(calls.lookups).toHaveLength(0)
})
test('the reviewed account can be confirmed once from the keyboard', async ({ page }) => {
  const calls = await setup(page)
  await page.goto('/link')
  await page.getByLabel('Account code').fill('482913')
  await expect(page.getByRole('button', { name: 'Link account', exact: true })).toBeFocused()
  await page.keyboard.press('Enter')
  await expect(page.getByRole('heading', { name: 'Linked to Markus' })).toBeVisible()
  expect(calls.writes).toHaveLength(1)
})
test('expired and consumed codes show a fresh-code instruction and no confirmation', async ({ page }) => {
  const calls = await setup(page, { lookupStatus: 410 })
  await page.goto('/link')
  await page.getByLabel('Account code').fill('482913')
  await expect(page.getByRole('alert')).toHaveText('This code expired or was used. Request a fresh code in the agent window.')
  await expect(page.getByRole('button', { name: 'Link account', exact: true })).toHaveCount(0)
  expect(calls.writes).toHaveLength(0)
})
test('reviews from another person or tenant never enable confirmation', async ({ page }) => {
  const calls = await setup(page)
  await page.route('**/api/agent-pairing/account-link/lookup', route => route.fulfill({ json: { ...initial, person_id: 'another-person', tenant_id: 'foreign' } }))
  await page.goto('/link'); await page.getByLabel('Account code').fill('482913')
  await expect(page.getByRole('alert')).toContainText('another session')
  await expect(page.getByRole('button', { name: 'Link account', exact: true })).toHaveCount(0)
  expect(calls.writes).toHaveLength(0)
})
test('a lost confirmation response never retries ownership automatically', async ({ page }) => {
  const calls = await setup(page)
  let approvals = 0
  await page.route(`**/api/agent-pairing/account-link/${request}/approve`, route => { approvals++; return route.abort('failed') })
  await page.goto('/link'); await page.getByLabel('Account code').fill('482913')
  await page.getByRole('button', { name: 'Link account', exact: true }).click()
  await expect(page.getByRole('alert')).toBeVisible()
  await expect(page.getByRole('button', { name: 'Link account', exact: true })).toHaveCount(0)
  expect(approvals).toBe(1); expect(calls.lookups).toHaveLength(1)
})
test('a review expires while the person is reading it', async ({ page }) => {
  await setup(page)
  await page.route('**/api/agent-pairing/account-link/lookup', route => route.fulfill({ json: { ...initial, expires_at: new Date(Date.now() + 2000).toISOString() } }))
  await page.goto('/link'); await page.getByLabel('Account code').fill('482913')
  await expect(page.getByRole('button', { name: 'Link account', exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Link account', exact: true })).toBeDisabled({ timeout: 7000 })
})
for (const [theme, width] of [['light', 1280], ['dark', 1280], ['light', 390]] as const) {
  test(`account linking review screenshot ${theme} ${width}`, async ({ page }, testInfo) => {
    await page.setViewportSize({ width, height: 850 })
    await setup(page, { theme })
    await page.goto('/link'); await page.getByLabel('Account code').fill('482913')
    await expect(page.getByRole('button', { name: 'Link account', exact: true })).toBeVisible()
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
    const report = await new AxeBuilder({ page }).include('.link-page').analyze()
    const directory = process.env.ACCOUNT_LINK_SHOTS ?? testInfo.outputDir
    mkdirSync(directory, { recursive: true })
    const path = join(directory, `account-link-${theme}-${width}.png`)
    await page.screenshot({ path, fullPage: true })
    await testInfo.attach(`account-link-${theme}-${width}`, { path, contentType: 'image/png' })
    expect(report.violations).toEqual([])
  })
}
