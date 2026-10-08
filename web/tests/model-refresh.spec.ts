// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect } from '@playwright/test'
import { fixtures, mockWork } from './work-fixtures'
import { businessData, mockBusiness } from './business-fixtures'
import { mockSettings, settingsData } from './settings-fixtures'

test('model refresh starts private, saves its interval and accepts profiles without route writes', async ({ page }) => {
  await mockWork(page, fixtures())
  await mockBusiness(page, businessData({ role: 'admin' }), { role: 'admin' })
  await mockSettings(page, settingsData())
  const status = { settings: { agent_reports_enabled: true, auto_add_profiles: true, api_enabled: false, interval_minutes: 1440 }, last_run_at: null, last_result: { sources: [{ vendor: 'openai', state: 'limited' }] }, sources: [], observations: [{ harness: 'grok', model: 'grok-next', effort: 'xhigh', pending: true, failures: 0 }] }
  const writes: string[] = []
  await page.route('**/api/models/**', async route => {
    const path = new URL(route.request().url()).pathname
    if (route.request().method() !== 'GET') writes.push(path)
    if (path === '/api/models/refresh/settings') { status.settings = route.request().postDataJSON(); return route.fulfill({ json: status.settings }) }
    if (path === '/api/models/proposals/accept') { status.observations = []; return route.fulfill({ json: {} }) }
    if (path === '/api/models/refresh' && route.request().method() === 'POST') return route.fulfill({ status: 429, json: { error: 'model refresh interval has not elapsed' } })
    return route.fulfill({ json: status })
  })
  // AEON-879 moves refresh settings into the Models proof fold; account quota stays in Accounts.
  await page.goto('/settings/agents#models')
  await expect(page).toHaveURL('/settings/models#model-refresh')
  const card = page.locator('#model-refresh')
  await expect(card.getByRole('link', { name: 'Accounts and computers' })).toHaveAttribute('href', '/settings/accounts')
  await expect(card.getByRole('checkbox', { name: 'Accept agent model reports' })).toBeChecked()
  await expect(card.getByRole('checkbox', { name: 'Enable vendor API discovery' })).not.toBeChecked()
  await expect(card.getByText('Discovered profiles need your acceptance before they can run.')).toBeVisible()
  await expect(card.getByText('openai: Discovery limit reached')).toBeVisible()
  await card.getByLabel('Refresh every (minutes)').fill('2880')
  await card.getByRole('button', { name: 'Save settings' }).click()
  await expect(card.getByRole('status')).toContainText('saved')
  expect(status.settings.interval_minutes).toBe(2880)
  await card.getByRole('button', { name: 'Accept profile' }).click()
  await expect(card.getByText('Discovered models awaiting acceptance')).toHaveCount(0)
  await card.getByRole('button', { name: 'Refresh now' }).click()
  await expect(card.getByRole('alert')).toContainText('configured refresh interval has not elapsed')
  expect(writes).toEqual(['/api/models/refresh/settings', '/api/models/proposals/accept', '/api/models/refresh'])
})
