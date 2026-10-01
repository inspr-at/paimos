// SPDX-License-Identifier: AGPL-3.0-only
// The product's names come from the server (brand.json, inspr.brand.v1): a
// deployment with a different brand shows that name on every surface.
import { test, expect, type Page } from '@playwright/test'
import { fixtures, mockWork } from './work-fixtures'
import { mockReleases, releaseHistory } from './releases-fixtures'

const NOVA = { schema: 'inspr.brand.v1', product: 'NOVA', generation: '3', release_name: 'DAWN', wordmark: 'NOVA DAWN', short_name: 'DAWN' }
const DEFAULT = /PAIMOS|\bAEON\b(?!-)/

// Visible text on the page that is not data (ticket keys, project names and release headlines come from fixtures).
async function chromeText(page: Page) {
  return page.evaluate(() => {
    const parts: string[] = [document.title]
    for (const el of document.querySelectorAll('.app-header, .app-footer, .releases .head, .toast, .signin-card, .status-page')) parts.push((el as HTMLElement).innerText)
    for (const el of document.querySelectorAll('.app-header [aria-label], .app-footer [aria-label], .releases .head [aria-label], .toast [aria-label], .signin-card [aria-label]')) parts.push(el.getAttribute('aria-label') ?? '')
    return parts.join('\n')
  })
}

test('a different brand names the title, header, footer, menu, release history and update notice', async ({ page }) => {
  const history = releaseHistory()
  await mockWork(page, fixtures())
  const state = await mockReleases(page, history, { running: history.releases[1].version, brand: NOVA })
  await page.goto('/')
  await expect(page.getByRole('list', { name: 'Projects' })).toBeVisible()
  await expect(page).toHaveTitle('Projects · NOVA DAWN')
  await expect(page.getByRole('link', { name: 'NOVA DAWN home' })).toBeVisible()
  await expect(page.locator('.app-header .wordmark')).toHaveText('NOVADAWN')
  await expect(page.locator('.app-header .wordmark sup')).toHaveText('DAWN')
  await expect(page.locator('footer.app-footer .footer-name .footer-wordmark')).toHaveText('NOVA DAWN')

  await page.getByRole('button', { name: /^Account for / }).click()
  await expect(page.getByRole('menu', { name: 'Account' })).toBeVisible()
  expect(await chromeText(page)).not.toMatch(DEFAULT)
  await page.keyboard.press('Escape')

  await page.locator('footer.app-footer .footer-name').click()
  const sheet = page.getByRole('dialog', { name: 'NOVA DAWN releases' })
  await expect(sheet).toBeVisible()
  // The generation is a label, never a version, and waits for Details (AEON-488).
  await expect(sheet.locator('.head .eyebrow')).toHaveText('NOVA DAWN · Release')
  await sheet.getByRole('radio', { name: 'Details' }).click()
  await expect(sheet.locator('.head .eyebrow')).toHaveText(/^NOVA 3 · DAWN releases · \d+ published · 1 reserved$/)
  await expect(page).toHaveTitle('Releases · NOVA DAWN')
  expect(await chromeText(page)).not.toMatch(DEFAULT)
  await page.keyboard.press('Escape')
  await expect(sheet).toHaveCount(0)

  state.server = history.current
  await page.evaluate(() => window.dispatchEvent(new Event('focus')))
  await expect(page.locator('.toast')).toHaveText(new RegExp(`^NOVA DAWN was updated to ${history.releases[0].codename}`))
  expect(await chromeText(page)).not.toMatch(DEFAULT)
})

test('the sign-in page follows the brand too', async ({ page }) => {
  await page.route('**/api/**', route => {
    const path = new URL(route.request().url()).pathname
    if (path === '/api/version') return route.fulfill({ json: { version: '260923120000.0.0', scheme: 'inspr-calendar-v2', brand: NOVA } })
    if (path === '/api/me') return route.fulfill({ status: 401, json: { error: 'unauthorized', dev_mode: false } })
    return route.fulfill({ status: 404, json: { error: 'Unmocked route' } })
  })
  await page.goto('/signin')
  await expect(page.getByRole('heading', { name: 'Sign in', level: 1 })).toBeVisible()
  await expect(page).toHaveTitle('Sign in · NOVA DAWN')
  await expect(page.locator('.card-foot')).toContainText('NOVA DAWN')
  await expect(page.locator('.wordmark').first()).toHaveText('NOVADAWN')
  expect(await chromeText(page)).not.toMatch(DEFAULT)
})
