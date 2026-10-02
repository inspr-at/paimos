// SPDX-License-Identifier: AGPL-3.0-only
// Business entry and overview: the header link, admin setup, the week and rates.
import { test, expect, type Page } from '@playwright/test'
import { fixtures, mockWork, watchErrors } from './work-fixtures'
import { addHistoricalPeriod, businessData, mockBusiness, type BusinessMockOptions } from './business-fixtures'

test.use({ timezoneId: 'Europe/Vienna' })

async function setup(page: Page, options: BusinessMockOptions = {}) {
  await mockWork(page, fixtures())
  const data = businessData(options)
  const calls = await mockBusiness(page, data, options)
  return { data, calls }
}
const header = (page: Page) => page.getByRole('banner')

test('the overview includes open periods older than a year in its waiting count', async ({ page }) => {
  const { data } = await setup(page)
  addHistoricalPeriod(data)
  await page.goto('/business')
  await expect(page.getByText('8h 30m logged this week · 2 periods to approve · 3 rates in force')).toBeVisible()
  const queue = page.getByRole('list', { name: 'Periods waiting for approval' })
  await expect(queue.getByRole('link')).toHaveCount(2)
  await expect(queue.getByRole('link').first()).toContainText('16–22 Sep 2024')
  await expect(queue.getByRole('link').first()).toContainText('6h 15m')
})

test('the header offers Business once a business plugin is enabled', async ({ page }) => {
  const errors = watchErrors(page)
  await setup(page)
  await page.goto('/')
  await expect(page.getByRole('list', { name: 'Projects' })).toBeVisible()
  const link = header(page).getByRole('link', { name: 'Business' })
  await expect(link).toBeVisible()
  await link.click()
  await expect(page).toHaveURL(/\/business$/)
  await expect(page.getByRole('heading', { name: 'Business', level: 1 })).toBeVisible()
  await expect(link).toHaveAttribute('aria-current', 'page')
  // Customers and Quotes always have tabs; an unknown quote link returns to the list.
  await expect(page.getByRole('navigation', { name: 'Business' }).getByRole('link')).toHaveText(['Overview', 'Customers', 'Quotes', 'Hours', 'Rates'])
  await page.goto('/business/quotes/QUO-3')
  await expect(page).toHaveURL(/\/business\/quotes$/)
  // Quotes is not enabled in this workspace: its page says so and how to open it.
  await expect(page.getByRole('heading', { name: 'Quotes is not enabled for this workspace' })).toBeVisible()
  await page.goto('/business/organisations')
  await expect(page).toHaveURL(/\/business\/customers$/)
  expect(errors).toEqual([])
})

test('without an enabled plugin the header stays quiet and members see why', async ({ page }) => {
  await setup(page, { role: 'member', enabled: [] })
  await page.goto('/')
  await expect(page.getByRole('list', { name: 'Projects' })).toBeVisible()
  await expect(header(page).getByRole('link', { name: 'Business' })).toHaveCount(0)
  await page.goto('/business')
  await expect(page.getByRole('heading', { name: 'Business is not set up for this workspace' })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Enable Business' })).toHaveCount(0)
})

test('an admin enables Business: every part with the compiled digests', async ({ page }) => {
  const { calls } = await setup(page, { enabled: [] })
  await page.goto('/business')
  await expect(page.getByRole('heading', { name: 'Set up Business' })).toBeVisible()
  await expect(page.getByRole('checkbox', { name: 'Hours enabled' })).not.toBeChecked()
  await expect(page.getByText('Also enables Rates.')).toBeVisible()
  await page.getByRole('button', { name: 'Enable Business' }).click()
  await expect(page.getByRole('heading', { name: 'Your week' })).toBeVisible()
  const writes = calls.filter(c => c.method === 'PUT')
  expect(writes.map(c => c.path)).toEqual(['/api/plugins/business_costs/installation', '/api/plugins/business_crm/installation', '/api/plugins/business_quotes/installation', '/api/plugins/business_hours/installation'])
  expect(writes[0].body).toEqual({ manifest_digest_sha256: 'ab'.repeat(32), enabled: true, permissions: ['nodes.contribute', 'steps.apply', 'views.provide'] })
  await expect(header(page).getByRole('link', { name: 'Business' })).toBeVisible()
})

test('disabling a part asks first and closes its page', async ({ page }) => {
  const { calls } = await setup(page)
  await page.goto('/business')
  await page.getByRole('button', { name: 'Manage parts' }).click()
  await page.getByRole('checkbox', { name: 'Hours enabled' }).click()
  const dialog = page.getByRole('dialog', { name: 'Disable Hours?' })
  await expect(dialog).toBeVisible()
  await dialog.getByRole('button', { name: 'Disable Hours' }).click()
  await expect.poll(() => calls.filter(c => c.method === 'PUT').length).toBe(1)
  expect(calls.find(c => c.method === 'PUT')?.body).toMatchObject({ enabled: false })
  await page.goto('/business/hours')
  await expect(page.getByRole('heading', { name: 'Hours is not enabled for this workspace' })).toBeVisible()
})

test('the overview shows the week, where it went, what waits and the rates', async ({ page }) => {
  const errors = watchErrors(page)
  await setup(page)
  await page.goto('/business')
  await expect(page.getByText('8h 30m logged this week · 1 period to approve · 3 rates in force')).toBeVisible()
  const week = page.getByRole('region', { name: 'Your week' })
  await expect(week.getByText('8h 30m', { exact: true })).toBeVisible()
  await expect(week.getByRole('listitem', { name: 'Thu 24: 1h 30m' })).toBeVisible()
  await expect(week.getByRole('list', { name: 'Time per ticket' }).getByRole('listitem').first()).toContainText('PHAROS-11')
  await expect(week.getByRole('list', { name: 'Time per ticket' }).getByRole('listitem').first()).toContainText('5:30')
  const waiting = page.getByRole('region', { name: 'Waiting for approval' })
  const period = waiting.getByRole('list', { name: 'Periods waiting for approval' }).getByRole('link')
  await expect(period).toHaveCount(1)
  for (const text of ['Markus Barta', '14–20 Sep 2026', '6h 15m']) await expect(period).toContainText(text)
  const rates = page.getByRole('region', { name: 'Rates in force' })
  await expect(rates.getByRole('listitem')).toHaveCount(2)
  await expect(rates.getByRole('listitem').last()).toContainText('95.00 EUR/h')
  await expect(rates.getByRole('listitem').last()).toContainText('720.00 EUR/day')
  await period.click()
  await expect(page).toHaveURL(/\/business\/hours\?view=approvals&period=p-me-38/)
  await expect(page.getByRole('complementary', { name: 'Period review' })).toBeVisible()
  expect(errors).toEqual([])
})

test('the palette logs time and goes to Business', async ({ page }) => {
  await setup(page)
  await page.goto('/')
  await expect(page.getByRole('list', { name: 'Projects' })).toBeVisible()
  await page.keyboard.press('Control+k')
  const palette = page.getByRole('dialog', { name: 'Search and commands' })
  await page.keyboard.type('log time')
  await expect(palette.getByRole('option', { name: /Log time/ })).toBeVisible()
  await page.keyboard.press('Enter')
  await expect(page).toHaveURL(/\/business\/hours/)
  await expect(page.getByRole('form', { name: 'Log time' })).toBeVisible()
  await page.keyboard.press('Control+k')
  await page.keyboard.type('business')
  await expect(palette.getByRole('option', { name: /Go to Business/ })).toBeVisible()
  await page.keyboard.press('Enter')
  await expect(page).toHaveURL(/\/business$/)
})
