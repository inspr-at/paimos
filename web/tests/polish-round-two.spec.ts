// SPDX-License-Identifier: AGPL-3.0-only
// AEON-679: retain native hours-table scrolling from the retired PL2 capture matrix.
import { test, expect } from '@playwright/test'
import { fixtures, mockWork, watchErrors } from './work-fixtures'
import { businessData, mockBusiness, NOW } from './business-fixtures'

test.use({ timezoneId: 'Europe/Vienna' })
for (const theme of ['light', 'dark'] as const) {
  test(`PL2 hours 390 ${theme}`, async ({ page }) => {
    const errors = watchErrors(page)
    await page.setViewportSize({ width: 390, height: 844 })
    await page.emulateMedia({ colorScheme: theme })
    await mockWork(page, fixtures())
    await mockBusiness(page, businessData())
    await page.clock.setSystemTime(NOW)
    await page.goto('/business/hours')
    await expect(page.locator('.period-chip').first()).toBeVisible()
    await page.evaluate(() => document.fonts.ready)
    expect(errors).toEqual([])
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
    const collisions = await page.locator('.week-grid td.c-day').evaluateAll(cells => cells.filter(cell => {
      const range = document.createRange(); range.selectNodeContents(cell)
      const text = range.getBoundingClientRect(), box = cell.getBoundingClientRect()
      return text.left < box.left + 1 || text.right > box.right - 1
    }).map(cell => cell.textContent))
    expect(collisions).toEqual([])
    const scroll = page.getByRole('region', { name: 'Weekly hours, scroll for all days' })
    await scroll.focus()
    for (let i = 0; i < 5; i++) await page.keyboard.press('ArrowRight')
    await expect.poll(() => scroll.evaluate(el => el.scrollLeft)).toBeGreaterThan(0)
  })
}
