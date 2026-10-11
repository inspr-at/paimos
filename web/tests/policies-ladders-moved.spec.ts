// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1204: role ladders moved to Settings › Models; PUT /models/routes answers
// 410 Gone once Models v2 S2 ships. Policies must never call the retired ladder
// endpoints, whatever the person clicks, and its one line must lead to Models.
import { test, expect } from '@playwright/test'
import { ladderRequests, mockPolicies } from './policies-fixtures'
import { POLICY_TABS } from '../src/lib/policies'

test('Policies Ladders tab links to Settings › Models and never calls the ladder endpoints', async ({ page }) => {
  const mock = await mockPolicies(page, 'light')
  const ladders = ladderRequests(page)
  await page.goto('/settings/policies')
  const panel = page.locator('.policies'), content = panel.locator('.policy-content')
  await expect(content).toHaveAttribute('aria-busy', 'false')
  expect(ladders).toEqual([])
  await expect(panel.getByTestId('policies-ladders-moved')).toContainText('Role ladders moved to')
  // No edit, Save or Undo control remains anywhere on Policies.
  for (const id of ['policy-edit', 'policy-save', 'policy-cancel', 'policy-undo', 'policy-reload']) await expect(page.getByTestId(id)).toHaveCount(0)
  // A same-person focus refresh and every tab leave the ladder endpoints alone.
  const answer = page.waitForResponse(response => response.url().includes('/api/me/permissions') && response.request().method() === 'GET')
  await page.evaluate(() => window.dispatchEvent(new Event('focus'))); await answer
  for (const item of [...POLICY_TABS, POLICY_TABS[0]]) { await panel.locator(`#policy-tab-${item.id}`).click(); await expect(content).toHaveAttribute('aria-busy', 'false') }
  await expect(panel.getByTestId('policies-ladders-moved')).toBeVisible()
  await panel.getByRole('link', { name: 'Settings, Models' }).click()
  await expect(page).toHaveURL(/\/settings\/models$/)
  expect(ladders).toEqual([])
  expect(mock.data.calls.filter(path => path !== '/api/authz/permissions')).toEqual([])
})

test('on a phone the moved link keeps a 44 px target inside its sentence', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  await mockPolicies(page, 'dark')
  const ladders = ladderRequests(page)
  await page.goto('/settings/policies')
  const link = page.locator('.policies').getByRole('link', { name: 'Settings, Models' })
  await expect(link).toHaveAttribute('href', '/settings/models')
  const target = await link.evaluate(el => { const after = getComputedStyle(el, '::before'); return { width: parseFloat(after.width), height: parseFloat(after.height) } })
  expect(target.width).toBeGreaterThanOrEqual(44)
  expect(target.height).toBeGreaterThanOrEqual(44)
  expect(ladders).toEqual([])
})
