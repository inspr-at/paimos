// SPDX-License-Identifier: AGPL-3.0-only
import { expect, test, type Page } from '@playwright/test'

const portal = {
  product: { key: 'PPR-1', title: 'Harbour office', summary: 'Work that is ready before the morning opens.' },
  catalog: [
    {
      key: 'PCF-1', title: 'Deadline radar', summary: 'Every statutory deadline in one place.', status: 'live',
      live_since: '260926120000.0.0', legal_basis: '§ 20 WEG',
    },
    {
      key: 'PCF-2', title: 'Paper archive', summary: 'Paper files were the old record.', status: 'declined',
      decline_reason: 'The record is already digital.',
    },
  ],
  wishes: [
    { key: 'PWS-1', title: 'Owner assembly on a phone', summary: 'Join without installing an app.', votes: 3 },
  ],
}

async function install(page: Page, missing = false) {
  const posted: { method: string; body: string | null; path: string }[] = []
  await page.route('**/api/**', async route => {
    const url = new URL(route.request().url())
    if (url.pathname === '/api/me') {
      await route.fulfill({ status: 401, contentType: 'application/json', body: '{}' })
      return
    }
    if (url.pathname.startsWith('/api/public/portal/')) {
      if (route.request().method() === 'POST') {
        posted.push({ method: route.request().method(), body: route.request().postData(), path: url.pathname })
        await route.fulfill({ status: 201, contentType: 'application/json', body: JSON.stringify({ votes: 4 }) })
        return
      }
      if (missing) {
        await route.fulfill({ status: 404, contentType: 'application/json', body: JSON.stringify({ error: 'not found' }) })
        return
      }
      await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(portal) })
      return
    }
    await route.fulfill({ status: 404, contentType: 'application/json', body: '{}' })
  })
  return posted
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
  test(`public portal catalog and vote at ${width}`, async ({ page }) => {
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
    const posted = await install(page)
    await page.goto('/portal/harbour')
    await expect(page.getByRole('heading', { level: 1, name: 'Harbour office' })).toBeVisible()
    await expect(page.getByRole('heading', { name: 'Deadline radar' })).toBeVisible()
    await expect(page.getByText('Live', { exact: true })).toBeVisible()
    await expect(page.getByText('260926120000.0.0')).toBeVisible()
    await expect(page.getByText('§ 20 WEG')).toBeVisible()
    await expect(page.getByText('Declined', { exact: true })).toBeVisible()
    await expect(page.getByText('The record is already digital.')).toBeVisible()
    await expect(page.getByText('3 votes')).toBeVisible()
    await expect(page.getByText('SECRET-NOTE')).toHaveCount(0)
    const vote = page.getByRole('button', { name: 'Vote for Owner assembly on a phone' })
    await vote.click()
    await expect.poll(() => posted.length).toBe(1)
    expect(posted[0]).toMatchObject({ method: 'POST', body: '{}', path: '/api/public/portal/harbour/wishes/PWS-1/votes' })
    await expect(page.getByText('4 votes')).toBeVisible()
    await expect(page.getByRole('button', { name: 'Voted for Owner assembly on a phone' })).toBeDisabled()
    await expectFits(page)
  })
}

test('a closed portal reads as unavailable', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  await install(page, true)
  await page.goto('/portal/closed')
  await expect(page.getByRole('heading', { name: 'This portal is not available' })).toBeVisible()
  await expect(page.getByRole('heading', { name: 'Catalog' })).toHaveCount(0)
  await expectFits(page)
})
