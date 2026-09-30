// SPDX-License-Identifier: AGPL-3.0-only
import { mkdirSync } from 'node:fs'
import { expect, test, type Page } from '@playwright/test'

const shots = process.env.PORTAL_SHOTS ?? ''

async function capture(page: Page, name: string) {
  if (!shots) return
  mkdirSync(shots, { recursive: true })
  await page.screenshot({ path: `${shots}/${name}`, fullPage: true })
}

const benefit = 'You can see what shipped, including a note long enough to wrap on a phone without pushing the page sideways.'

const releases = {
  product: { key: 'PPR-1', title: 'Harbour office', summary: 'Work that is ready before the morning opens.' },
  releases: [
    {
      released_at: '2026-09-26T12:00:00Z',
      version: '260926120000.0.0',
      notes: [{
        pill_en: 'Clear morning notes',
        pill_de: 'Klare Morgennotizen',
        benefit_en: benefit,
        benefit_de: 'Sichtbar, was geliefert wurde.',
        internal_note: 'SECRET-NOTE',
      }],
    },
    {
      released_at: '2026-09-10T08:00:00Z',
      notes: [{
        pill_en: 'Quiet dated notes',
        pill_de: 'Quiet dated notes',
        benefit_en: 'A date is enough.',
        benefit_de: 'A date is enough.',
      }],
    },
  ],
}

const catalog = {
  product: releases.product,
  catalog: [],
  wishes: [],
  release_history: true,
}

// AEON-430: a release this build's history knows carries its marketing name.
const named = {
  product: releases.product,
  releases: [{ ...releases.releases[0], released_at: '2026-09-30T07:49:21Z', version: '260930074921.0.0', codename: 'Fresh Flyby' }, releases.releases[1]],
}

async function install(page: Page, body: 'releases' | 'named' | 'empty' | 'missing') {
  await page.route('**/api/**', async route => {
    const url = new URL(route.request().url())
    if (url.pathname === '/api/me') {
      await route.fulfill({ status: 401, contentType: 'application/json', body: '{}' })
      return
    }
    if (url.pathname.startsWith('/api/public/portal/')) {
      if (body === 'missing') {
        await route.fulfill({ status: 404, contentType: 'application/json', body: JSON.stringify({ error: 'not found' }) })
        return
      }
      if (url.pathname.endsWith('/releases')) {
        const payload = body === 'empty' ? { product: releases.product, releases: [] } : body === 'named' ? named : releases
        await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(payload) })
        return
      }
      await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(catalog) })
      return
    }
    await route.fulfill({ status: 404, contentType: 'application/json', body: '{}' })
  })
}

async function expectFits(page: Page) {
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
  const seam = await page.evaluate(() => {
    const main = document.querySelector('main')
    const portal = document.querySelector<HTMLElement>('.portal')
    if (!main || !portal) return 'missing'
    const mainStyle = getComputedStyle(main)
    const portalStyle = getComputedStyle(portal)
    if (mainStyle.backgroundColor !== portalStyle.backgroundColor) return `color ${mainStyle.backgroundColor} vs ${portalStyle.backgroundColor}`
    if (!mainStyle.backgroundImage.includes('radial-gradient')) return 'main has no wash'
    return ''
  })
  expect(seam).toBe('')
  const edged = await page.evaluate(() => [...document.querySelectorAll<HTMLElement>('.portal *')].filter(el => {
    const style = getComputedStyle(el)
    const left = parseFloat(style.borderLeftWidth)
    const right = parseFloat(style.borderRightWidth)
    const top = parseFloat(style.borderTopWidth)
    const bottom = parseFloat(style.borderBottomWidth)
    return (left >= 3 && left > right + 1) || (top >= 3 && top > bottom + 1)
  }).map(el => el.className))
  expect(edged).toEqual([])
}

for (const width of [1600, 390]) {
  test(`public release history leads with the marketing name at ${width}`, async ({ page }) => {
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
    await install(page, 'named')
    await page.goto('/portal/harbour/releases')
    const heading = page.getByRole('heading', { level: 2, name: /Fresh Flyby/ })
    await expect(heading.locator('.rn-name')).toHaveText('Fresh Flyby')
    // The date is a quiet line below; no version text, no release number at rest.
    await expect(page.locator('time[datetime="2026-09-30T07:49:21Z"]')).toHaveText(/30 Sept? 2026/)
    await expect(heading.locator('.rn-stamp')).toHaveCSS('opacity', '0')
    await expect(page.locator('.portal')).not.toContainText(/260930074921|Release \d/)
    // Hover (or focus) reveals the calendar version as the Pretty stamp.
    await heading.locator('.release-name').focus()
    await expect(heading.locator('.rn-stamp')).toHaveCSS('opacity', '1')
    await expect(heading.locator('.rn-stamp .calendar-version')).toHaveAttribute('aria-label', /^260930074921\.0\.0 · 2026-09-30 07:49:21 UTC$/)
    await expectFits(page)
    await capture(page, `public-releases-named-${width}.png`)
    // A release without a name keeps its dated heading.
    await expect(page.getByRole('heading', { level: 2, name: /10 Sept? 2026/ })).toBeVisible()
  })

  test(`public release history at ${width}`, async ({ page }) => {
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
    await install(page, 'releases')
    await page.goto('/portal/harbour/releases')
    await expect(page.getByRole('heading', { level: 1, name: 'Releases' })).toBeVisible()
    await expect(page).toHaveTitle(/Releases/)
    await expect(page.getByText('What shipped, in the words saved when it shipped.')).toHaveCount(0)
    await expect(page.getByRole('heading', { level: 2, name: '260926120000.0.0' })).toHaveCount(0)
    const version = page.getByText('260926120000.0.0')
    await expect(version).toBeVisible()
    await expect(version).toHaveAttribute('title', '260926120000.0.0')
    await expect(page.locator('time[datetime="2026-09-26T12:00:00Z"]')).toHaveText(/26 Sept? 2026/)
    await expect(page.getByRole('heading', { level: 2, name: /26 Sept? 2026/ })).toBeVisible()
    await expect(page.getByText('Clear morning notes')).toBeVisible()
    await expect(page.getByText(benefit)).toBeVisible()
    await expect(page.getByRole('heading', { level: 2, name: /10 Sept? 2026/ })).toBeVisible()
    await expect(page.locator('time[datetime="2026-09-10T08:00:00Z"]')).toBeVisible()
    await expect(page.getByText('SECRET-NOTE')).toHaveCount(0)
    await expect(page.getByText('TKT-91')).toHaveCount(0)
    const deutsch = page.getByRole('button', { name: 'Deutsch' })
    await expect(deutsch).toHaveCount(1)
    await expect(page.getByText('Klare Morgennotizen')).toBeHidden()
    await deutsch.click()
    await expect(page.locator('[lang="de"]').filter({ hasText: 'Klare Morgennotizen' })).toBeVisible()
    await expect(page.locator('[lang="de"]').filter({ hasText: 'Sichtbar, was geliefert wurde.' })).toBeVisible()
    await expect(page.getByText('Clear morning notes')).toBeHidden()
    await page.reload()
    await expect(page.locator('[lang="de"]').filter({ hasText: 'Klare Morgennotizen' })).toBeVisible()
    await page.getByRole('button', { name: 'English' }).click()
    await expect(page.getByText('Clear morning notes')).toBeVisible()
    await expect(page.getByRole('navigation', { name: 'Portal' }).getByRole('link', { name: 'llms.txt' })).toHaveCount(0)
    await expect(page.getByRole('link', { name: 'Catalog' })).toHaveAttribute('href', '/portal/harbour')
    await expect(page.getByRole('link', { name: 'llms.txt' })).toHaveAttribute('href', '/portal/harbour/llms.txt')
    await expectFits(page)
    await page.emulateMedia({ colorScheme: 'light' })
    await capture(page, `public-releases-${width}-light.png`)
    await page.emulateMedia({ colorScheme: 'dark' })
    await expectFits(page)
    await capture(page, `public-releases-${width}-dark.png`)
    await page.emulateMedia({ colorScheme: 'light' })
    await page.getByRole('link', { name: 'Catalog' }).click()
    await expect(page).toHaveURL('/portal/harbour')
    await expect(page.getByRole('heading', { level: 1, name: 'Harbour office' })).toBeVisible()
    await expect(page.getByRole('link', { name: 'Releases' })).toBeVisible()
  })
}

test('public release history empty and missing', async ({ page }) => {
  await page.setViewportSize({ width: 1600, height: 1000 })
  await install(page, 'empty')
  await page.goto('/portal/harbour/releases')
  await expect(page.getByText('No published releases yet.')).toBeVisible()
  await expect(page.getByRole('heading', { level: 2 })).toHaveCount(0)
  await expectFits(page)
  await capture(page, 'public-releases-empty-1600-light.png')
  await page.setViewportSize({ width: 390, height: 844 })
  await expectFits(page)
  await capture(page, 'public-releases-empty-390-light.png')

  await page.setViewportSize({ width: 1600, height: 1000 })
  await install(page, 'missing')
  await page.goto('/portal/missing/releases')
  await expect(page.getByRole('heading', { level: 1, name: 'This portal is not available' })).toBeVisible()
  await expectFits(page)
  await capture(page, 'public-releases-404-1600-light.png')
  await page.emulateMedia({ colorScheme: 'dark' })
  await capture(page, 'public-releases-404-1600-dark.png')
})
