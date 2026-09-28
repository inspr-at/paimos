// SPDX-License-Identifier: AGPL-3.0-only
// AEON-266: a phone can select ticket cards and add them to a release.
import { expect, test, type Page } from '@playwright/test'
import { journeyWorld, mockJourney } from './journey-fixtures'
import { fixtures, mockWork } from './work-fixtures'

test.beforeEach(async ({ page }) => { await page.clock.setSystemTime(new Date('2026-09-23T12:00:00Z')) })

const row = (page: Page, key: string) => page.locator('tr.ticket-row').filter({ has: page.locator('.key', { hasText: new RegExp(`^${key}$`) }) })
const bulkBar = (page: Page) => page.getByRole('toolbar', { name: /selected ticket/ })

test('on a phone, select two tickets, open Add to release, then cancel', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  await mockWork(page, fixtures())
  await mockJourney(page, journeyWorld('plan'))
  await page.goto('/p/PHAROS')
  await expect(row(page, 'PHAROS-12')).toBeVisible()
  await expect(page.getByRole('checkbox', { name: 'Select PHAROS-12' })).toBeHidden()

  await page.getByRole('button', { name: 'Select', exact: true }).click()
  const mark = row(page, 'PHAROS-12').getByRole('checkbox', { name: 'Select PHAROS-12' })
  await expect(mark).toBeVisible()
  const markBox = (await mark.boundingBox())!
  expect(markBox.width).toBeGreaterThanOrEqual(44)
  expect(markBox.height).toBeGreaterThanOrEqual(44)

  await row(page, 'PHAROS-12').click()
  await row(page, 'PHAROS-14').click()
  await expect(page).toHaveURL(/\/p\/PHAROS\/tickets$/)
  await expect(page.locator('.phone-pick-status')).toContainText('2 selected')
  await expect(bulkBar(page)).toContainText('2')
  const barBox = (await bulkBar(page).boundingBox())!
  expect(barBox.x).toBeGreaterThanOrEqual(0)
  expect(barBox.x + barBox.width).toBeLessThanOrEqual(390)
  expect(barBox.height).toBeGreaterThanOrEqual(44)

  await bulkBar(page).getByRole('button', { name: 'Add to release' }).click()
  const dialog = page.getByRole('dialog', { name: 'Release for 2 tickets' })
  await expect(dialog.getByRole('option', { name: 'Release 2 In planning' })).toBeVisible()
  const sheet = (await dialog.boundingBox())!
  expect(sheet.x).toBeLessThanOrEqual(8)
  expect(sheet.width).toBeGreaterThan(370)
  expect(sheet.y + sheet.height).toBeGreaterThan(800)

  await page.keyboard.press('Escape')
  await expect(dialog).toBeHidden()
  await page.getByRole('button', { name: 'Cancel' }).click()
  await expect(bulkBar(page)).toHaveCount(0)
  await expect(page.getByRole('button', { name: 'Select', exact: true })).toBeVisible()
  await expect(page.getByRole('checkbox', { name: 'Select PHAROS-12' })).toBeHidden()
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
})

test('a long press on a phone card selects it and shows the round check', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  await mockWork(page, fixtures())
  await page.goto('/p/PHAROS')
  await expect(row(page, 'PHAROS-11')).toBeVisible()
  await row(page, 'PHAROS-11').dispatchEvent('contextmenu')
  await expect(row(page, 'PHAROS-11').getByRole('checkbox', { name: 'Select PHAROS-11' })).toBeChecked()
  await expect(page.locator('.phone-pick-status')).toContainText('1 selected')
  await expect(page).toHaveURL(/\/p\/PHAROS\/tickets$/)
})
