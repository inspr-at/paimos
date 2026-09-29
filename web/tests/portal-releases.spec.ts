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
}

async function install(page: Page, body: 'releases' | 'empty' | 'missing') {
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
        const payload = body === 'empty' ? { product: releases.product, releases: [] } : releases
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
  test(`public release history at ${width}`, async ({ page }) => {
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
    await install(page, 'releases')
    await page.goto('/portal/harbour/releases')
    await expect(page.getByRole('heading', { level: 1, name: 'Harbour office' })).toBeVisible()
    await expect(page.getByText('What shipped, in the words saved when it shipped.')).toBeVisible()
    const version = page.getByRole('heading', { level: 2, name: '260926120000.0.0' })
    await expect(version).toBeVisible()
    await expect(version).toHaveAttribute('title', '260926120000.0.0')
    await expect(page.getByText('Clear morning notes')).toBeVisible()
    await expect(page.getByText(benefit)).toBeVisible()
    await expect(page.getByText(/26 Sept? 2026/)).toBeVisible()
    await expect(page.getByRole('heading', { level: 2, name: /10 Sept? 2026/ })).toBeVisible()
    await expect(page.getByText('SECRET-NOTE')).toHaveCount(0)
    await expect(page.getByText('TKT-91')).toHaveCount(0)
    const deutsch = page.getByText('Deutsch', { exact: true })
    await expect(deutsch).toBeVisible()
    await expect(page.getByText('Klare Morgennotizen')).toBeHidden()
    await deutsch.click()
    await expect(page.getByText('Klare Morgennotizen')).toBeVisible()
    await expect(page.getByText('Sichtbar, was geliefert wurde.')).toBeVisible()
    await expect(page.getByRole('link', { name: 'Catalog' })).toHaveAttribute('href', '/portal/harbour')
    await expect(page.getByRole('link', { name: 'llms.txt' })).toHaveAttribute('href', '/portal/harbour/llms.txt')
    await expectFits(page)
    await page.emulateMedia({ colorScheme: 'light' })
    await capture(page, `public-releases-${width}-light.png`)
    await page.emulateMedia({ colorScheme: 'dark' })
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
