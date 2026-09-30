// SPDX-License-Identifier: AGPL-3.0-only
// AEON-430: every release has a sci-fi codename from its sequence. Without a
// brief the codename is the row's title; with one it is a quiet line beside
// the version. The detail says "Release N · Codename", and search finds it.
import { test, expect, type Page } from '@playwright/test'
import { fixtures, mockWork } from './work-fixtures'
import { CODENAMES, mockReleases, presentedHistory } from './releases-fixtures'

const sheet = (page: Page) => page.getByRole('dialog', { name: 'PAIMOS AEON releases' })
const options = (page: Page) => page.getByRole('listbox', { name: 'Releases, newest first' }).getByRole('option')

async function open(page: Page, version?: string) {
  await mockWork(page, fixtures())
  const history = presentedHistory()
  Object.assign(history.releases.find(r => r.state === 'reserved')!, { release_sequence: 54, codename: 'Bold Booster' })
  await mockReleases(page, history)
  await page.goto(`/releases/${version ?? history.releases[0].version}`)
  await expect(sheet(page)).toBeVisible()
  return history
}

test('without a brief the codename is the row title; with one it sits quietly beside the version', async ({ page }) => {
  await open(page)
  // Newest: a presented theme is the title, the codename is the quiet line.
  const newest = options(page).nth(0)
  await expect(newest.locator('.row-codename')).toHaveText(CODENAMES[5])
  await expect(newest.locator('.headline')).toHaveText('Releases with a name')
  // A reservation keeps its sequence's name beside its state.
  const reserved = options(page).nth(2)
  await expect(reserved.locator('.row-codename')).toHaveText('Bold Booster')
  await expect(reserved.locator('.headline')).toHaveText('Reserved, never published')
  // No brief: the codename is the title, not repeated beside the version.
  const untitled = options(page).nth(3)
  await expect(untitled.locator('.headline.codename-title')).toHaveText(CODENAMES[3])
  await expect(untitled.locator('.row-codename')).toHaveCount(0)
})

test('the detail header reads "Release N · Codename" and search finds a codename', async ({ page }) => {
  const history = await open(page)
  const header = sheet(page).locator('.detail .eyebrow.top')
  await expect(header).toContainText(`Release ${history.releases[0].release_sequence}`)
  await expect(header.locator('.codename')).toHaveText(CODENAMES[5])
  await page.getByRole('searchbox', { name: 'Search releases' }).fill('cobalt')
  await expect(options(page)).toHaveCount(1)
  await expect(options(page).first().locator('mark')).toHaveText('Cobalt')
  await expect(sheet(page).locator('.detail .eyebrow.top')).toContainText('Release 3')
  await expect(sheet(page).locator('.detail .eyebrow.top .codename')).toHaveText('Cobalt Comet')
})

test('phones show the codename without horizontal scroll', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  await open(page)
  await page.getByRole('button', { name: 'All releases' }).click()
  await expect(options(page).nth(3).locator('.codename-title')).toHaveText(CODENAMES[3])
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)
  expect(overflow).toBeLessThanOrEqual(0)
})
