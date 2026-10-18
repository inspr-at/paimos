// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1204: role ladders moved to Settings › Models; PUT /models/routes answers
// 410 Gone once Models v2 S2 ships. Policies, rules only since AEON-1165, must
// never call the retired ladder endpoints, whatever the person clicks, and its
// intro line must lead to Models.
import { test, expect } from '@playwright/test'
import { ladderRequests, mockPolicies } from './policies-fixtures'
import { ASK_RISKS } from '../src/lib/policies'

test('Policies links to Settings › Models and never calls the ladder endpoints', async ({ page }) => {
  const mock = await mockPolicies(page, 'light')
  const ladders = ladderRequests(page)
  await page.goto('/settings/policies')
  const root = page.locator('.policies')
  await expect(root.locator('.settings-card')).toHaveCount(3)
  expect(ladders).toEqual([])
  // No ladder tab, edit, Save or Undo control remains anywhere on Policies.
  await expect(root.getByRole('tab')).toHaveCount(0)
  for (const id of ['policy-edit', 'policy-save', 'policy-cancel', 'policy-undo', 'policy-reload']) await expect(page.getByTestId(id)).toHaveCount(0)
  // A same-person focus refresh and every risk group leave the ladder endpoints alone.
  const answer = page.waitForResponse(response => response.url().includes('/api/me/permissions') && response.request().method() === 'GET')
  await page.evaluate(() => window.dispatchEvent(new Event('focus'))); await answer
  for (const { risk } of ASK_RISKS) { const head = root.locator(`[data-risk="${risk}"]`); await head.click(); await expect(head).toHaveAttribute('aria-expanded', 'true') }
  expect(mock.retired).toEqual([])
  await root.locator('.intro').getByRole('link', { name: 'Models' }).click()
  await expect(page).toHaveURL(/\/settings\/models$/)
  expect(ladders).toEqual([])
})

test.describe('on a phone', () => {
  test.use({ hasTouch: true, isMobile: true })

  test('the Models link keeps a 44 px target inside its sentence', async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 })
    await mockPolicies(page, 'dark')
    const ladders = ladderRequests(page)
    await page.goto('/settings/policies')
    const link = page.locator('.policies .intro').getByRole('link', { name: 'Models' })
    await expect(link).toHaveAttribute('href', '/settings/models')
    const target = (await link.boundingBox())!
    expect(target.width).toBeGreaterThanOrEqual(43.5)
    expect(target.height).toBeGreaterThanOrEqual(43.5)
    expect(ladders).toEqual([])
  })
})
