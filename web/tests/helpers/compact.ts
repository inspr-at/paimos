// SPDX-License-Identifier: AGPL-3.0-only
import { expect, type Page } from '@playwright/test'

// AEON-1459 (design AEON-1279 r2, D1): phones and short screens get the compact chrome.
export const isCompact = (page: Page) => { const size = page.viewportSize(); return !!size && (size.width < 720 || size.height <= 500) }
// On compact screens the Agents page's capacity row (the dial and the accounts chip) lives in the
// capacity line's sheet (R2.1). Opens it there; on wider screens the row is on the page already.
export async function showCapacityRow(page: Page) {
  if (!isCompact(page)) return
  const sheet = page.getByRole('dialog', { name: 'Capacity' })
  if (!await sheet.isVisible()) await page.locator('.agents-page .cap-line').click()
  await expect(sheet.locator('.cap-row')).toBeVisible()
}
// The Desk preview chevron steps aside on compact screens (D2). The avatar menu carries the same preview.
export async function openDeskPreview(page: Page) {
  if (isCompact(page)) {
    await page.getByRole('button', { name: /^Account for / }).click()
    await page.getByRole('menu', { name: 'Account' }).getByRole('menuitem', { name: 'Preview the Decision Desk' }).click()
    return
  }
  await page.getByRole('button', { name: 'Preview the Decision Desk', exact: true }).click()
}
// The project crumb's sheet (D11) holds what the crumb's fold did on phones: whether the project head
// shows above the ticket list. Ticks or clears it; the choice closes the sheet (AEON-1460),
// so the sheet is reopened to read the flipped choice and closed again.
export async function toggleProjectHead(page: Page) {
  const crumb = page.locator('.app-header .crumb-sheet')
  await crumb.click()
  const item = page.getByRole('menuitemcheckbox', { name: /^Show the project head/ })
  const before = await item.getAttribute('aria-checked')
  await item.click()
  await expect(item).toHaveCount(0)
  await crumb.click()
  await expect(item).not.toHaveAttribute('aria-checked', before ?? '')
  await page.keyboard.press('Escape')
  await expect(item).toHaveCount(0)
}
