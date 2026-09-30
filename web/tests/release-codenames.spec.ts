// SPDX-License-Identifier: AGPL-3.0-only
// AEON-430: every release has a sci-fi codename from its sequence, and the
// marketing name is front and centre: the row's title, the detail's heading,
// the footer's pill. The calendar version appears only on hover or focus, in
// the footer's chip style; the release number stays a quiet line in the notes.
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

test('the marketing name leads every row; the brief is its subtitle, and the version waits for hover', async ({ page }) => {
  await open(page)
  // Newest: the name leads, a presented theme is the subtitle.
  const newest = options(page).nth(0)
  await expect(newest.locator('.rn-name')).toHaveText(CODENAMES[5])
  await expect(newest.locator('.headline')).toHaveText('Releases with a name')
  // A reservation keeps its sequence's name beside its state.
  const reserved = options(page).nth(2)
  await expect(reserved.locator('.rn-name')).toHaveText('Bold Booster')
  await expect(reserved.locator('.headline')).toHaveText('Reserved, never published')
  // No brief: the name alone, no version text at rest.
  const untitled = options(page).nth(3)
  await expect(untitled.locator('.rn-name')).toHaveText(CODENAMES[3])
  await expect(untitled.locator('.headline')).toHaveCount(0)
  await expect(untitled.locator('.rn-stamp')).toHaveCSS('opacity', '0')
  await untitled.hover()
  await expect(untitled.locator('.rn-stamp')).toHaveCSS('opacity', '1')
  await expect(untitled.locator('.rn-name')).toHaveCSS('opacity', '0')
  await expect(untitled.locator('.rn-stamp .calendar-version')).toContainText(/^\d\d·\d\d·\d\d \d\d:\d\d/)
})

test('the detail heading is the name; the number is a quiet line in the notes; search finds a name', async ({ page }) => {
  const history = await open(page)
  const detail = sheet(page).locator('.detail')
  await expect(detail.locator('h2 .rn-name')).toHaveText(CODENAMES[5])
  // No "Release 113"-style number in the header.
  await expect(detail.locator('.eyebrow.top')).not.toContainText(/\d/)
  await expect(detail.locator('.tech')).toHaveText(`Release ${history.releases[0].release_sequence} · ${history.releases[0].version}`)
  await page.getByRole('searchbox', { name: 'Search releases' }).fill('cyan')
  await expect(options(page)).toHaveCount(1)
  await expect(options(page).first().locator('mark')).toHaveText('Cyan')
  await expect(sheet(page).locator('.detail h2 .rn-name')).toHaveText('Cyan Cell')
})

test('the stamp is reachable by keyboard and described to a screen reader', async ({ page }) => {
  await open(page)
  const heading = sheet(page).locator('.detail h2 .release-name')
  await expect(heading).toHaveAttribute('aria-describedby', /.+/)
  const tip = sheet(page).locator(`[id="${await heading.getAttribute('aria-describedby')}"]`)
  await expect(tip).toHaveAttribute('role', 'tooltip')
  await expect(tip.locator('.calendar-version')).toHaveAttribute('aria-label', /^\d{12}\.0\.0 · 20\d\d-/)
  await heading.focus()
  await expect(tip).toHaveCSS('opacity', '1')
})

test('phones show the name without horizontal scroll', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  await open(page)
  await page.getByRole('button', { name: 'All releases' }).click()
  await expect(options(page).nth(3).locator('.rn-name')).toHaveText(CODENAMES[3])
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)
  expect(overflow).toBeLessThanOrEqual(0)
})

test('the footer names the running release; hover reveals its version without moving anything', async ({ page }) => {
  await mockWork(page, fixtures())
  const history = presentedHistory()
  await mockReleases(page, history)
  await page.route('**/api/version', route => route.fulfill({ json: { version: history.current, scheme: 'inspr-calver-3', codename: CODENAMES[5] } }))
  await page.goto('/')
  const pill = page.locator('footer.app-footer .version-pill')
  const name = pill.locator('.rn-name')
  await expect(name).toHaveText(CODENAMES[5])
  await expect(pill).toHaveAccessibleName(new RegExp(`${CODENAMES[5]}, version ${history.current.replace(/\./g, '\\.')}`))
  // No release number anywhere in the pill's text.
  expect(await pill.innerText()).not.toMatch(/Release \d|\b\d+\.\d+\b/)
  const before = (await pill.boundingBox())!
  await pill.hover()
  await expect(pill.locator('.rn-stamp')).toHaveCSS('opacity', '1')
  await expect(pill.locator('.rn-stamp .calendar-version')).toContainText(/^\d\d·\d\d·\d\d \d\d:\d\d/)
  const after = (await pill.boundingBox())!
  expect(Math.abs(after.width - before.width)).toBeLessThanOrEqual(1)
  await page.mouse.move(5, 5)
  await expect(pill.locator('.rn-stamp')).toHaveCSS('opacity', '0')
  // The pill still opens the release history.
  await pill.click()
  await expect(sheet(page)).toBeVisible()
})
