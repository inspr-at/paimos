// SPDX-License-Identifier: AGPL-3.0-only
// INSPR-CalVer3 (AEON-309): the seconds are collapsed at rest and every version
// surface reveals them. Inside a control (the footer's history button, a release
// row) the control's hover or keyboard focus reveals and its click still acts;
// standing alone (the release heading, "Running here") the version is the shared
// renderer's copy pill. Reduced motion switches at once.
import { test, expect, type Locator, type Page } from '@playwright/test'
import { fixtures, mockWork } from './work-fixtures'
import { mockReleases, releaseHistory } from './releases-fixtures'
import { journeyWorld, mockJourney } from './journey-fixtures'

const sheet = (page: Page) => page.getByRole('dialog', { name: 'PAIMOS AEON releases' })
const options = (page: Page) => page.getByRole('listbox', { name: 'Releases, newest first' }).getByRole('option')
const pill = (page: Page) => page.getByRole('button', { name: /^Release history, version / })

// A release with a marketing name shows the name and reveals its stamp (AEON-430,
// release-codenames.spec.ts); these are the versions nobody has named, which keep
// the CalVer3 reveal of the seconds.
async function setup(page: Page) {
  const history = releaseHistory()
  for (const release of history.releases) delete release.codename
  await mockWork(page, fixtures())
  await mockReleases(page, history)
  return history
}

// The rendered version inside `scope`, and its seconds segment.
const version = (scope: Locator) => scope.locator('[data-canonical]').first()
const seconds = (scope: Locator) => version(scope).locator('.ss')
const secondsWidth = async (scope: Locator) => (await seconds(scope).boundingBox())?.width ?? 0

async function expectRevealed(scope: Locator) {
  await expect(version(scope)).toHaveAttribute('data-version-view', 'revealed')
  await expect.poll(() => secondsWidth(scope)).toBeGreaterThan(0)
  await expect.poll(() => seconds(scope).evaluate(el => Number(getComputedStyle(el).opacity))).toBeGreaterThan(0.6)
}
async function expectRest(scope: Locator) {
  await expect(version(scope)).toHaveAttribute('data-version-view', 'pretty')
  await expect.poll(() => secondsWidth(scope)).toBe(0)
}
const away = (page: Page) => page.mouse.move(1, 1)

test('footer pill: hover and keyboard focus reveal the seconds; a click still opens the history', async ({ page }) => {
  await setup(page)
  await page.goto('/p/PHAROS')
  const button = pill(page)
  await expect(version(button)).toBeVisible()
  await expectRest(button)
  await button.hover()
  await expectRevealed(button)
  await away(page)
  await expectRest(button)
  await button.focus()
  await expectRevealed(button)
  await page.locator('body').click({ position: { x: 5, y: 300 } })
  await expectRest(button)
  // A click on the version itself reaches the button.
  await version(button).click()
  await expect(sheet(page)).toBeVisible()
})

test('release history rows: hover and the keyboard’s current row reveal; a click still selects', async ({ page }) => {
  const history = await setup(page)
  await page.goto('/releases')
  await expect(options(page).first()).toHaveAttribute('aria-selected', 'true')
  const row = options(page).nth(2)
  await expectRest(row)
  await row.hover()
  await expectRevealed(row)
  await away(page)
  await expectRest(row)
  // The listbox keeps focus; the current option follows j and reveals.
  await page.getByRole('listbox', { name: 'Releases, newest first' }).focus()
  await page.keyboard.press('j')
  await expect(options(page).nth(1)).toHaveAttribute('aria-selected', 'true')
  await expectRevealed(options(page).nth(1))
  await expectRest(options(page).first())
  await version(row).click()
  await expect(row).toHaveAttribute('aria-selected', 'true')
  await expect(page).toHaveURL(`/releases/${history.releases[2].version}`)
})

test('release heading and "Running here": the copy pill reveals on hover and focus and copies exactly', async ({ page, context }) => {
  await context.grantPermissions(['clipboard-read', 'clipboard-write'])
  await page.setViewportSize({ width: 1280, height: 800 })
  const history = await setup(page)
  await page.goto('/releases')
  const heading = sheet(page).getByRole('heading', { level: 2, name: history.current })
  const copy = heading.getByRole('button', { name: new RegExp(`^${history.current.replace(/\./g, '\\.')} · .* — Copy version$`) })
  await expect(copy).toBeVisible()
  await expectRest(heading)
  await copy.hover()
  await expectRevealed(heading)
  await away(page)
  await expectRest(heading)
  await copy.focus()
  await expectRevealed(heading)
  await page.keyboard.press('Enter')
  await expect(copy).toHaveAttribute('data-copy-state', 'copied')
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(history.current)
  const running = sheet(page).getByRole('group', { name: 'Release cadence' }).locator('.value')
  await expectRest(running)
  await version(running).hover()
  await expectRevealed(running)
})

test('journey release list: hover and focus reveal; a click still opens the release', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  await mockWork(page, fixtures())
  await mockJourney(page, journeyWorld('build', { derived: true }))
  await page.goto('/p/PHAROS?view=journey')
  const list = page.getByRole('list', { name: 'Releases, newest first' })
  const row = list.getByRole('button').filter({ has: page.locator('[data-canonical="260901120000.0.0"]') })
  await expect(row).toBeVisible()
  await expectRest(row)
  await row.hover()
  await expectRevealed(row)
  await away(page)
  await expectRest(row)
  await row.focus()
  await expectRevealed(row)
  await row.blur()
  await expectRest(row)
  await version(row).click()
  await expect(page).toHaveURL(/[?&]release=PHAROS-30(&|$)/)
  await expect(row).toHaveAttribute('aria-current', 'true')
})

test('journey Live heading: the standalone copy pill reveals on keyboard focus and copies', async ({ page, context }) => {
  await context.grantPermissions(['clipboard-read', 'clipboard-write'])
  await page.setViewportSize({ width: 1440, height: 900 })
  await mockWork(page, fixtures())
  await mockJourney(page, journeyWorld('build', { derived: true }))
  await page.goto('/p/PHAROS?view=journey&stage=live&release=PHAROS-30')
  const head = page.locator('section[aria-labelledby="live-tickets"] .j-card-head')
  // Omitted `interactive` means the renderer's own pill, never a bare image.
  const copy = head.getByRole('button', { name: /^260901120000\.0\.0 · .* — Copy version$/ })
  await expect(copy).toBeVisible()
  await expectRest(head)
  await copy.focus()
  await expect(copy).toBeFocused()
  await expectRevealed(head)
  await page.keyboard.press('Enter')
  await expect(copy).toHaveAttribute('data-copy-state', 'copied')
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe('260901120000.0.0')
})

test('reduced motion reveals at once, without a transition', async ({ page }) => {
  await page.emulateMedia({ reducedMotion: 'reduce' })
  await setup(page)
  await page.goto('/p/PHAROS')
  const button = pill(page)
  await expect(version(button)).toBeVisible()
  await button.hover()
  await expect(version(button)).toHaveAttribute('data-version-view', 'revealed')
  expect(await seconds(button).evaluate(el => [el.style.transition, getComputedStyle(el).opacity])).toEqual(['none', '0.7'])
  expect(await secondsWidth(button)).toBeGreaterThan(0)
})
