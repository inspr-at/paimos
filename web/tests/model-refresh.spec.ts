// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect } from '@playwright/test'
import { fixtures, mockWork } from './work-fixtures'
import { businessData, mockBusiness } from './business-fixtures'
import { mockSettings, settingsData } from './settings-fixtures'

test('model refresh starts private, saves its interval and accepts profiles without route writes', async ({ page }) => {
  await mockWork(page, fixtures())
  await mockBusiness(page, businessData({ role: 'admin' }), { role: 'admin' })
  await mockSettings(page, settingsData())
  const status = { settings: { agent_reports_enabled: true, auto_add_profiles: true, api_enabled: false, interval_minutes: 1440 }, last_run_at: null, last_result: {}, sources: [], observations: [{ harness: 'grok', model: 'grok-next', effort: 'xhigh', pending: true, failures: 0 }] }
  const writes: string[] = []
  await page.route('**/api/models/**', async route => {
    const path = new URL(route.request().url()).pathname
    if (route.request().method() !== 'GET') writes.push(path)
    if (path === '/api/models/refresh/settings') { status.settings = route.request().postDataJSON(); return route.fulfill({ json: status.settings }) }
    if (path === '/api/models/proposals/accept') { status.observations = []; return route.fulfill({ json: {} }) }
    return route.fulfill({ json: status })
  })
  await page.goto('/settings/workspace')
  const card = page.locator('#models')
  await expect(card.getByRole('checkbox', { name: 'Accept agent model reports' })).toBeChecked()
  await expect(card.getByRole('checkbox', { name: 'Enable vendor API discovery' })).not.toBeChecked()
  await card.getByLabel('Refresh every (minutes)').fill('2880')
  await card.getByRole('button', { name: 'Save settings' }).click()
  await expect(card.getByRole('status')).toContainText('saved')
  expect(status.settings.interval_minutes).toBe(2880)
  await card.getByRole('button', { name: 'Accept profile' }).click()
  await expect(card.getByText('Discovered models awaiting acceptance')).toHaveCount(0)
  expect(writes).toEqual(['/api/models/refresh/settings', '/api/models/proposals/accept'])
})
