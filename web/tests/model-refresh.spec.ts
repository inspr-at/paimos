// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect } from '@playwright/test'
import { fixtures, mockWork } from './work-fixtures'
import { businessData, mockBusiness } from './business-fixtures'
import { mockSettings, settingsData } from './settings-fixtures'
import { mockEffectivePermissions } from './authz-fixtures'
import { mockRegistry, registryWorld } from './models-registry-fixtures'

// AEON-1012 replaces the Catalog freshness fold (AEON-879) with the Model registry card.
// Risk: the old #models and #model-refresh links lose their target, or the page offers
// Check now without its cooldown, or writes anything but the refresh itself.
test('the Models page carries the registry card: the old freshness links land on it and Check now shows its cooldown', async ({ page }) => {
  await mockWork(page, fixtures())
  await mockBusiness(page, businessData({ role: 'admin' }), { role: 'admin' })
  await mockSettings(page, settingsData())
  const world = await mockRegistry(page, registryWorld({ check: { status: 429, retryAfter: 120 } }), { permissions: false })
  await page.route('**/api/me/permissions*', route => {
    const answer = mockEffectivePermissions('admin', new URL(route.request().url()).searchParams.get('project_id') ?? undefined)
    answer.workspace.permissions = [...answer.workspace.permissions, 'models.read', 'models.manage', 'models.refresh']
    return route.fulfill({ json: answer })
  })
  await page.goto('/settings/agents#models')
  await expect(page).toHaveURL('/settings/models#model-refresh')
  const card = page.locator('#model-refresh')
  await expect(card.getByRole('heading', { name: 'Model registry' })).toBeVisible()
  await expect(card.locator('[data-reg-row="gpt-6.1-sol"]')).toBeVisible()
  await expect(card.getByRole('switch', { name: 'Auto-update' })).toBeChecked()
  // Catalog freshness lives on the registry card. The minimal Models page removed the old proof fold with the board.
  await expect(page.locator('#models-freshness')).toHaveCount(0)
  await expect(page.locator('#models-proof')).toHaveCount(0)
  await card.getByRole('button', { name: 'Check now' }).click()
  await expect(card.getByRole('button', { name: 'Checked · again in 2 min' })).toBeVisible()
  expect(world.writes.map(write => `${write.method} ${write.path}`)).toEqual(['POST /models/refresh'])
})
