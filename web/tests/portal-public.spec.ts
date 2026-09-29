// SPDX-License-Identifier: AGPL-3.0-only
import { mkdirSync } from 'node:fs'
import { expect, test, type Page } from '@playwright/test'

// PORTAL_SHOTS names a directory. Unset, this file creates nothing at import.
const shots = process.env.PORTAL_SHOTS ?? ''

async function capture(page: Page, name: string) {
  if (!shots) return
  mkdirSync(shots, { recursive: true })
  await page.screenshot({ path: `${shots}/${name}`, fullPage: true })
}

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
  pace: { releases_30d: 4, median_release_gap_days: 21, wish_to_live_median_days: 18 },
  comparison: [{
    aspect: 'Statutory deadlines',
    cells: [
      { competitor: 'Northwind', stance: 'yes', quote: 'Every deadline is on the public help page.', source_url: 'https://northwind.example/deadlines', retrieved_on: '2026-09-19' },
      { competitor: 'Linden', stance: 'no', quote: 'The brochure still describes a paper archive.', source_url: 'https://linden.example/archive', retrieved_on: '2026-01-01', stale: true },
      { competitor: 'Alden', stance: 'unknown' },
    ],
  }],
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
        const accepted = url.pathname.endsWith('/wishes') || url.pathname.endsWith('/corrections')
        const body = accepted ? { accepted: true } : { votes: 4 }
        await route.fulfill({ status: 201, contentType: 'application/json', body: JSON.stringify(body) })
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
    await expect(page.getByRole('link', { name: 'Releases' })).toHaveAttribute('href', '/portal/harbour/releases')
    await expect(page.getByRole('link', { name: 'llms.txt' })).toHaveAttribute('href', '/portal/harbour/llms.txt')
    await expect(page.getByRole('group', { name: 'Feature status' })).toHaveCount(0)
    await expect(page.getByRole('heading', { name: 'Deadline radar' })).toBeVisible()
    await expect(page.getByText('Live', { exact: true })).toBeVisible()
    await expect(page.getByRole('img', { name: '260926120000.0.0' })).toHaveCount(0)
    await expect(page.getByTitle('260926120000.0.0')).toHaveText(/since 26 Sept? 2026/)
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
    await expect(page.getByRole('heading', { name: 'Pace' })).toBeVisible()
    await expect(page.getByText('releases in 30 days')).toBeVisible()
    await expect(page.getByRole('link', { name: /Every deadline is on the public help page/ })).toHaveAttribute('href', 'https://northwind.example/deadlines')
    await expect(page.getByRole('link', { name: /Every deadline is on the public help page/ })).toHaveAttribute('rel', 'noopener noreferrer nofollow')
    await expect(page.getByRole('link', { name: /The brochure still describes a paper archive/ })).toHaveAttribute('rel', 'noopener noreferrer nofollow')
    await expect(page.getByText('Stale')).toBeVisible()
    await expect(page.locator('[aria-label="Not sourced"]')).toBeVisible()
    await expect(page.getByText('Unknown')).toHaveCount(0)
    await expectFits(page)
    await page.emulateMedia({ colorScheme: 'light' })
    await capture(page, `public-catalog-${width}-light.png`)
    await page.getByRole('link', { name: /Every deadline is on the public help page/ }).scrollIntoViewIfNeeded()
    await capture(page, `public-comparison-${width}-light.png`)
    await page.emulateMedia({ colorScheme: 'dark' })
    await capture(page, `public-comparison-${width}-dark.png`)
    await page.getByRole('heading', { name: 'Pace' }).scrollIntoViewIfNeeded()
    await capture(page, `public-catalog-${width}-dark.png`)
  })
}

test('status chips appear only past six features, and a wish is title and summary', async ({ page }) => {
  const posted: { method: string; body: string | null; path: string }[] = []
  const catalog = ['live', 'planned', 'in_progress', 'declined', 'idea', 'reviewed', 'live'].map((status, index) => ({
    key: `PCF-${index + 10}`,
    title: `${status} feature ${index + 1}`,
    summary: 'A public line.',
    status,
    ...(status === 'live' ? { live_since: '260926120000.0.0' } : {}),
    ...(status === 'declined' ? { decline_reason: 'Not this one.' } : {}),
  }))
  await page.route('**/api/**', async route => {
    const url = new URL(route.request().url())
    if (url.pathname === '/api/me') {
      await route.fulfill({ status: 401, contentType: 'application/json', body: '{}' })
      return
    }
    if (url.pathname.startsWith('/api/public/portal/') && route.request().method() === 'POST' && url.pathname.endsWith('/wishes')) {
      posted.push({ method: 'POST', body: route.request().postData(), path: url.pathname })
      await route.fulfill({ status: 201, contentType: 'application/json', body: JSON.stringify({ accepted: true }) })
      return
    }
    if (url.pathname.startsWith('/api/public/portal/')) {
      await route.fulfill({ json: { ...portal, catalog } })
      return
    }
    await route.fulfill({ status: 404, contentType: 'application/json', body: '{}' })
  })
  for (const width of [1600, 390]) {
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
    await page.emulateMedia({ colorScheme: width === 1600 ? 'light' : 'dark' })
    await page.goto('/portal/harbour')
    await expect(page.getByRole('group', { name: 'Feature status' }).getByRole('button')).toHaveText(['Live', 'Planned', 'In progress', 'Declined'])
    await page.getByRole('button', { name: 'Planned', exact: true }).click()
    await expect(page.getByRole('heading', { name: 'planned feature 2' })).toBeVisible()
    await expect(page.getByRole('heading', { name: 'live feature 1' })).toHaveCount(0)
    await page.getByRole('button', { name: 'Planned', exact: true }).click()
    await expect(page.getByRole('heading', { name: 'live feature 1' })).toBeVisible()
    await expectFits(page)
    await capture(page, `public-${width}-${width === 1600 ? 'light' : 'dark'}.png`)
  }
  await page.getByLabel('Title').fill('A morning bell')
  await page.getByLabel('Summary').fill('Ring once, before the office opens.')
  await page.getByRole('button', { name: 'Send wish' }).click()
  await expect(page.getByRole('status')).toHaveText('Sent for review.')
  await expect(page.getByLabel('Email')).toHaveCount(0)
  await expect(page.getByLabel('Name')).toHaveCount(0)
  expect(posted).toHaveLength(1)
  expect(JSON.parse(posted[0].body ?? '')).toEqual({ title: 'A morning bell', summary: 'Ring once, before the office opens.', website: '' })
  expect(posted[0].path).toBe('/api/public/portal/harbour/wishes')
})

test('a correction sends the statement and no name', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  const posted = await install(page)
  await page.goto('/portal/harbour')
  await page.getByLabel('Competitor').fill('Northwind')
  await page.getByLabel('Aspect').fill('Statutory deadlines')
  await page.getByLabel('Statement').fill('The page quotes the wrong paragraph.')
  await page.getByLabel('Source page').fill('https://northwind.example/correction')
  await page.getByRole('button', { name: 'Send correction' }).click()
  await expect(page.getByRole('status')).toHaveText('Sent.')
  await expect(page.getByLabel('Email')).toHaveCount(0)
  await expect(page.getByLabel('Name')).toHaveCount(0)
  expect(posted).toHaveLength(1)
  expect(posted[0].path).toBe('/api/public/portal/harbour/corrections')
  expect(JSON.parse(posted[0].body ?? '')).toEqual({
    competitor: 'Northwind',
    aspect: 'Statutory deadlines',
    statement: 'The page quotes the wrong paragraph.',
    source_url: 'https://northwind.example/correction',
    website: '',
  })
  await expectFits(page)
})

test('a closed portal reads as unavailable', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  await install(page, true)
  await page.goto('/portal/closed')
  await expect(page.getByRole('heading', { name: 'This portal is not available' })).toBeVisible()
  await expect(page.getByRole('heading', { name: 'Catalog' })).toHaveCount(0)
  await expectFits(page)
})
