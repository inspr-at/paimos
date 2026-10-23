// SPDX-License-Identifier: AGPL-3.0-only
// Rates: cost units with dated bill and internal rates; admins add rates inline.
import { test, expect, type Page } from '@playwright/test'
import { fixtures, mockWork, watchErrors } from './work-fixtures'
import { businessData, mockBusiness, type BusinessMockOptions } from './business-fixtures'

test.use({ timezoneId: 'Europe/Vienna' })

async function setup(page: Page, options: BusinessMockOptions = {}) {
  await mockWork(page, fixtures())
  const data = businessData(options)
  const calls = await mockBusiness(page, data, options)
  return { data, calls }
}
async function openRates(page: Page) {
  await page.goto('/business/rates')
  await expect(page.getByRole('heading', { name: 'Rates', level: 1 })).toBeVisible()
  await expect(page.getByRole('table', { name: 'Rates of Development' })).toBeVisible()
}

test('each cost unit lists its rates with what is in force', async ({ page }) => {
  const errors = watchErrors(page)
  await setup(page)
  await openRates(page)
  await expect(page.getByText('2 cost units · 3 rates in force')).toBeVisible()
  const dev = page.getByRole('table', { name: 'Rates of Development' })
  await expect(dev.locator('tbody tr')).toHaveCount(3)
  const day = dev.locator('tbody tr').first()
  for (const text of ['per day', '720.00', '480.00', 'from 1 Jan 2026', 'In force']) await expect(day).toContainText(text)
  await expect(dev.locator('tbody tr').last()).toContainText('1 Jan 2025 – 1 Jan 2026')
  await expect(dev.locator('tbody tr').last()).toContainText('Ended')
  // Retired cost units stay out of the way until asked for.
  await expect(page.getByRole('heading', { name: 'Legacy support' })).toHaveCount(0)
  await page.getByRole('checkbox', { name: /Retired/ }).check()
  await expect(page.getByRole('heading', { name: 'Legacy support' })).toBeVisible()
  await expect(page.getByText('No rate yet, so quotes and hours cannot use it.')).toBeVisible()
  expect(errors).toEqual([])
})

test('an admin adds a rate that closes the current one', async ({ page }) => {
  const { calls } = await setup(page)
  await openRates(page)
  await page.getByRole('button', { name: 'Add rate' }).nth(1).click()
  await expect(page.getByRole('combobox', { name: 'Unit' })).toBeFocused()
  await page.getByRole('combobox', { name: 'Unit' }).selectOption('hour')
  await page.getByRole('textbox', { name: 'Bill rate' }).fill('99.5')
  await page.getByRole('textbox', { name: 'Internal rate' }).fill('64,25')
  await page.getByRole('textbox', { name: 'Valid from' }).fill('1.10.2026')
  await page.getByRole('textbox', { name: 'Valid from' }).press('Enter')
  await expect(page.getByText('Rate added to Development.')).toBeVisible()
  const post = calls.find(c => c.method === 'POST')!
  expect(post.path).toBe('/api/cost-units/cu-dev/rates')
  expect(post.body).toEqual({ unit: 'hour', currency: 'EUR', bill_amount: 99.5, internal_amount: 64.25, effective_from: '2026-10-01', effective_until: null })
  const dev = page.getByRole('table', { name: 'Rates of Development' })
  await expect(dev.getByText('Scheduled')).toBeVisible()
  await expect(dev.getByText('1 Jan 2026 – 1 Oct 2026')).toBeVisible()
})

test('an admin adds a cost unit and goes straight to its first rate', async ({ page }) => {
  await setup(page)
  await openRates(page)
  await page.getByRole('button', { name: 'New cost unit' }).click()
  await page.getByRole('textbox', { name: 'New cost unit name' }).fill('Consulting')
  await page.keyboard.press('Enter')
  await expect(page.getByRole('heading', { name: 'Consulting' })).toBeVisible()
  await expect(page.getByRole('combobox', { name: 'Unit' })).toBeFocused()
})

test('members read rates without changing them', async ({ page }) => {
  await setup(page, { role: 'member' })
  await openRates(page)
  await expect(page.getByText('Workspace admins set rates.')).toBeVisible()
  await expect(page.getByRole('button', { name: 'Add rate' })).toHaveCount(0)
  await expect(page.getByRole('button', { name: 'New cost unit' })).toHaveCount(0)
})

// Temporary same-host worker timing; removed before handoff.
import { writeFileSync as writeCutATiming } from 'node:fs'
import { hostname as cutAHost } from 'node:os'
let cutAStarted = 0
test.beforeEach(() => { cutAStarted = performance.now() })
test.afterEach(async ({}, testInfo) => {
  writeCutATiming(testInfo.outputPath('p10-cutA-timing.md'), JSON.stringify({
    host: cutAHost(), title: testInfo.title, seconds: (performance.now() - cutAStarted) / 1000,
    status: testInfo.status, retry: testInfo.retry,
  }))
})
