// SPDX-License-Identifier: AGPL-3.0-only
import { mkdirSync } from 'node:fs'
import { expect, test, type Page } from '@playwright/test'

const shots = process.env.PORTAL_SHOTS ?? ''

async function capture(page: Page, name: string) {
  if (!shots) return
  mkdirSync(shots, { recursive: true })
  await page.screenshot({ path: `${shots}/${name}`, fullPage: true })
}

const benefit = 'You can see what is coming, including a note long enough to wrap on a phone without pushing the page sideways.'

const roadmap = {
  schema: 'inspr.release-roadmap.v1',
  product: { key: 'PPR-1', title: 'Harbour office', summary: 'Work that is ready before the morning opens.' },
  items: [
    {
      pill_en: 'Clear morning notes',
      pill_de: 'Klare Morgennotizen',
      benefit_en: benefit,
      benefit_de: 'Sichtbar, was als Nächstes kommt.',
      target: 'r16',
      status: 'in_progress',
      id: 'SECRET-ID',
      title: 'SECRET-TITLE',
    },
    {
      pill_en: 'Quiet next notes',
      pill_de: 'Leise nächste Notizen',
      benefit_en: 'The next release stays in order.',
      benefit_de: 'Die nächste Ausgabe bleibt in der Reihe.',
      target: 'r17',
      status: 'planned',
    },
    {
      pill_en: 'Later public notes',
      pill_de: 'Spätere öffentliche Notizen',
      benefit_en: 'Later still has a place.',
      benefit_de: 'Später hat noch einen Platz.',
      target: 'later',
      status: 'shipped',
    },
  ],
}

const catalog = {
  product: roadmap.product,
  catalog: [],
  wishes: [],
  release_history: true,
  roadmap: true,
}

async function install(page: Page, body: 'roadmap' | 'empty' | 'missing') {
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
      if (url.pathname.endsWith('/roadmap') || url.pathname.endsWith('/roadmap.json')) {
        const payload = body === 'empty'
          ? { schema: roadmap.schema, product: roadmap.product, items: [] }
          : roadmap
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
  test(`public roadmap at ${width}`, async ({ page }) => {
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
    await install(page, 'roadmap')
    await page.goto('/portal/harbour/roadmap')
    await expect(page.getByRole('heading', { level: 1, name: "What's coming" })).toBeVisible()
    await expect(page).toHaveTitle(/What's coming/)
    await expect(page.getByRole('heading', { level: 2 })).toHaveText(['Release 16', 'Release 17', 'Later'])
    await expect(page.getByText('Clear morning notes')).toBeVisible()
    await expect(page.getByText('In progress', { exact: true })).toBeVisible()
    await expect(page.getByText(benefit)).toBeVisible()
    await expect(page.getByText('Quiet next notes')).toBeVisible()
    await expect(page.getByText('Planned', { exact: true })).toBeVisible()
    await expect(page.getByText('Later public notes')).toBeVisible()
    await expect(page.getByText('Shipped', { exact: true })).toBeVisible()
    await expect(page.getByText('SECRET-ID')).toHaveCount(0)
    await expect(page.getByText('SECRET-TITLE')).toHaveCount(0)
    const deutsch = page.getByRole('button', { name: 'Deutsch' })
    await expect(deutsch).toHaveCount(1)
    await expect(page.getByText('Klare Morgennotizen')).toBeHidden()
    await deutsch.click()
    await expect(page.locator('[lang="de"]').filter({ hasText: 'Klare Morgennotizen' })).toBeVisible()
    await expect(page.locator('[lang="de"]').filter({ hasText: 'Sichtbar, was als Nächstes kommt.' })).toBeVisible()
    await expect(page.getByRole('heading', { level: 2, name: 'Später' })).toBeVisible()
    await expect(page.getByText('In Arbeit', { exact: true })).toBeVisible()
    await expect(page.getByText('Clear morning notes')).toBeHidden()
    await page.reload()
    await expect(page.locator('[lang="de"]').filter({ hasText: 'Klare Morgennotizen' })).toBeVisible()
    await page.getByRole('button', { name: 'English' }).click()
    await expect(page.getByText('Clear morning notes')).toBeVisible()
    await expect(page.getByRole('link', { name: 'Catalog' })).toHaveAttribute('href', '/portal/harbour')
    await expect(page.getByRole('link', { name: 'llms.txt' })).toHaveAttribute('href', '/portal/harbour/llms.txt')
    await expectFits(page)
    await page.emulateMedia({ colorScheme: 'light' })
    await capture(page, `public-roadmap-${width}-light.png`)
    await page.emulateMedia({ colorScheme: 'dark' })
    await expectFits(page)
    await capture(page, `public-roadmap-${width}-dark.png`)
    await page.emulateMedia({ colorScheme: 'light' })
    await page.getByRole('link', { name: 'Catalog' }).click()
    await expect(page).toHaveURL('/portal/harbour')
    await expect(page.getByRole('heading', { level: 1, name: 'Harbour office' })).toBeVisible()
    await expect(page.getByRole('link', { name: "What's coming" })).toHaveAttribute('href', '/portal/harbour/roadmap')
    await expect(page.getByRole('link', { name: 'Releases' })).toBeVisible()
  })
}

test('public roadmap empty and missing', async ({ page }) => {
  await page.setViewportSize({ width: 1600, height: 1000 })
  await install(page, 'empty')
  await page.goto('/portal/harbour/roadmap')
  await expect(page.getByText('Nothing public on the roadmap yet.')).toBeVisible()
  await expect(page.getByRole('heading', { level: 2 })).toHaveCount(0)
  await expectFits(page)
  await capture(page, 'public-roadmap-empty-1600-light.png')
  await page.setViewportSize({ width: 390, height: 844 })
  await expectFits(page)
  await capture(page, 'public-roadmap-empty-390-light.png')

  await page.setViewportSize({ width: 1600, height: 1000 })
  await install(page, 'missing')
  await page.goto('/portal/missing/roadmap')
  await expect(page.getByRole('heading', { level: 1, name: 'This portal is not available' })).toBeVisible()
  await expectFits(page)
  await capture(page, 'public-roadmap-404-1600-light.png')
  await page.emulateMedia({ colorScheme: 'dark' })
  await capture(page, 'public-roadmap-404-1600-dark.png')
})
