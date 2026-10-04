// SPDX-License-Identifier: AGPL-3.0-only
// AEON-679: retain the filtering interaction from the retired PL1 capture matrix.
import { test, expect } from '@playwright/test'
import { fixtures, mockWork } from './work-fixtures'

for (const theme of ['light', 'dark'] as const) for (const width of [1600, 390]) {
  test(`PL1 tickets-empty ${width} ${theme}`, async ({ page }) => {
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
    await page.emulateMedia({ colorScheme: theme })
    await mockWork(page, fixtures())
    await page.goto('/p/PHAROS/tickets?q=unmatched')
    const state = page.locator('.table-card .state')
    await expect(state.locator('h2')).toBeVisible()
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
    await expect(state.getByRole('button')).toHaveCount(1)
    await expect(state.locator('p')).toHaveCount(0)
    await state.getByRole('button', { name: 'Clear filters' }).click()
    await expect(page.locator('tr.ticket-row:not(.ghost)').first()).toBeVisible()
  })
}
