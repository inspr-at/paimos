// SPDX-License-Identifier: AGPL-3.0-only
// AEON-305: a compact header per named release (theme as kicker, headline,
// intro) above the same blocks every release shows: Features and Fixes, one
// block per ticket with its pill, key, benefit and folded commits. The rail
// shows version, date and theme; the Git tag message is evidence only.
import { test, expect, type Page } from '@playwright/test'
import { fixtures, mockWork, watchErrors } from './work-fixtures'
import { mockReleases, presentedHistory } from './releases-fixtures'

const NOW = Date.parse('2026-09-29T12:00:00Z')
const sheet = (page: Page) => page.getByRole('dialog', { name: 'PAIMOS AEON releases' })
const detail = (page: Page) => sheet(page).locator('article.detail')
const rows = (page: Page) => sheet(page).getByRole('listbox', { name: 'Releases, newest first' }).getByRole('option')

async function open(page: Page, index = 0, locale?: string) {
  const history = presentedHistory(NOW)
  await mockWork(page, fixtures())
  await mockReleases(page, history)
  if (locale) {
    const profile = { principal_id: '11111111-1111-4111-8111-111111111111', email: 'markus@barta.com', first_name: 'Markus', last_name: 'Barta', preferred_name: '', short_name: 'mba', initials: 'MB', timezone: 'Europe/Vienna', locale, greeting_enabled: false, avatar_color: 'teal', avatar_hashes: {}, week_start: 1, revision: 1 }
    await page.route('**/api/me/profile', route => route.fulfill({ json: profile }))
  }
  await page.goto(`/releases/${history.releases[index]!.version}`)
  await expect(sheet(page)).toBeVisible()
  return history
}

const noHorizontalScroll = (page: Page) => page.evaluate(() => {
  const wide = (el: Element | null) => !!el && el.scrollWidth > el.clientWidth + 1
  return wide(document.querySelector('dialog.releases')) || wide(document.documentElement)
})

for (const colorScheme of ['light', 'dark'] as const) {
  for (const width of [1600, 390]) {
    test(`a presented release leads with its header, then the ticket blocks at ${width} ${colorScheme}`, async ({ page }) => {
      await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
      await page.emulateMedia({ colorScheme, reducedMotion: 'reduce' })
      const errors = watchErrors(page)
      await open(page)
      const d = detail(page)
      const header = d.getByRole('region', { name: 'Release notes' }).locator('.summary')
      await expect(header.locator('.kicker')).toHaveText('Releases with a name')
      await expect(header.locator('.kicker')).toHaveCSS('text-transform', 'uppercase')
      await expect(header.locator('.headline')).toHaveText('Every release says what it is about.')
      await expect(header.locator('.intro')).toContainText('opens each version with its theme')
      // The pills and benefits are the blocks below, not repeated in the header.
      await expect(d.getByRole('list', { name: 'What this release brings' })).toHaveCount(0)
      await expect(header).not.toContainText('Named releases')
      const features = d.getByRole('region', { name: 'Features, 3' })
      await expect(features.locator('.g-icon svg')).toHaveCount(1)
      await expect(features.locator('.pill-title')).toHaveText(['Named releases', 'Changes by ticket', 'Older notes filled in'])
      const named = features.getByRole('article', { name: 'Named releases' })
      await expect(named.locator('.benefit')).toHaveText('Every release opens with its theme and one sentence about what it changes for you.')
      await expect(named.locator('.line-head').getByText('AEON-305', { exact: true })).toBeVisible()
      await expect(named.locator('summary')).toHaveCount(0)
      await sheet(page).getByRole('radio', { name: 'Details', exact: true }).click()
      await expect(named.locator('.benefit')).toHaveCount(0)
      await expect(named.locator('summary')).toHaveText('2 commits')
      await expect(named.getByText('Release presentation store, API and present CLI')).toBeHidden()
      await named.locator('summary').click()
      await expect(named.getByText('Release presentation store, API and present CLI')).toBeVisible()
      await expect(named.getByText('Release detail with kicker, headline and benefit rows')).toBeVisible()
      await expect(features.getByText('P0.x:')).toHaveCount(0)
      await named.locator('summary').click()
      await expect(d.getByRole('region', { name: 'Fixes, 1' }).locator('.pill-title')).toHaveText('Search stays put')
      await expect(d.getByRole('region', { name: 'Other changes, 1' })).toContainText('Pin the Go module vendor hash')
      // Keys named on a pill need no chip above the list.
      await expect(d.locator('.tickets')).toHaveCount(0)
      await expect(sheet(page).getByText('Historical tag headline')).toHaveCount(0)
      // The tag message stays available as evidence.
      await d.getByRole('button', { name: /^Evidence/ }).click()
      await expect(d.locator('#release-evidence')).toContainText('Tag message: stable104')
      expect(await noHorizontalScroll(page)).toBe(false)
      if (width === 390) await sheet(page).getByRole('button', { name: 'All releases' }).click()
      await expect(rows(page).first().locator('.headline')).toHaveText('Releases with a name')
      await expect(rows(page).first().locator('.headline')).toHaveClass(/theme/)
      expect(await noHorizontalScroll(page)).toBe(false)
      expect(errors).toEqual([])
    })
  }
}

test('without a presentation: the blocks lead, and a release without them is version, date and list', async ({ page }) => {
  await page.setViewportSize({ width: 1600, height: 1000 })
  const history = await open(page, 1)
  const d = detail(page)
  await expect(d.locator('.kicker')).toHaveCount(0)
  await expect(d.locator('.headline')).toHaveCount(0)
  await expect(d.getByRole('heading', { level: 2, name: history.releases[1]!.version })).toBeVisible()
  await expect(d.locator('.summary')).toHaveCount(0)
  await expect(d.getByRole('region', { name: 'Features, 1' }).locator('.pill-title')).toHaveText('Wide lists')
  // PHAROS-11 has no pill, so it keeps its chip.
  await expect(d.locator('.tickets')).toContainText('PHAROS-11')
  await expect(rows(page).nth(1).locator('.headline')).toHaveText('Wide lists')
  await expect(rows(page).nth(1).locator('.headline')).not.toHaveClass(/theme/)
  await rows(page).nth(3).click()
  await expect(d.locator('.summary')).toHaveCount(0)
  await expect(d.locator('.tickets')).toContainText('PAI-1057')
  await expect(d.getByText('Internal changes only.')).toBeVisible()
  await sheet(page).getByRole('radio', { name: 'Details', exact: true }).click()
  // No ticket tells a benefit, so the fix commit is listed under Other.
  await expect(d.getByRole('region', { name: 'Other changes, 1' })).toContainText('Retry a busy BEGIN in release acceptance transactions')
})

test('a German profile reads the German presentation and pills, with English where German is empty', async ({ page }) => {
  await page.setViewportSize({ width: 1600, height: 1000 })
  await open(page, 0, 'de-AT')
  const d = detail(page)
  await expect(d.locator('.kicker')).toHaveText('Releases mit Namen')
  await expect(d.locator('.kicker')).toHaveAttribute('lang', 'de')
  await expect(d.locator('.summary .headline')).toHaveText('Jedes Release sagt, worum es geht.')
  const features = d.getByRole('region', { name: 'Features, 3' })
  await expect(features.locator('.pill-title').first()).toHaveText('Benannte Releases')
  await expect(features.locator('.benefit').first()).toHaveAttribute('lang', 'de')
  await expect(rows(page).first().locator('.headline')).toHaveText('Releases mit Namen')
})

test('search finds a release by its theme and marks it', async ({ page }) => {
  await page.setViewportSize({ width: 1600, height: 1000 })
  await open(page, 1)
  await sheet(page).getByRole('searchbox', { name: 'Search releases' }).fill('with a name')
  await expect(rows(page)).toHaveCount(1)
  await rows(page).first().click()
  await expect(detail(page).locator('.kicker mark')).toHaveText('with a name')
  await expect(rows(page).first().locator('.headline mark')).toHaveText('with a name')
})
