// SPDX-License-Identifier: AGPL-3.0-only
// AEON-323: EN | DE and Highlights | Details in the release-history header.
// The labels stay those words in both languages. Highlights is the benefit;
// Details is the commits. The choice is in the URL and survives a version
// change, compare, and a reload. Historic 102 and current 105 share the renderer.
import { mkdirSync } from 'node:fs'
import { test, expect, type Page } from '@playwright/test'
import { fixtures, mockWork } from './work-fixtures'
import { mockReleases } from './releases-fixtures'
import { historicHistory, NOTES } from './releases-historic-fixtures'

const SHOTS = process.env.AEON_323_SHOTS
const V102 = '260929082208.0.0'
const V105 = '260929113854.0.0'

const sheet = (page: Page) => page.getByRole('dialog', { name: 'PAIMOS AEON releases' })
const detail = (page: Page) => sheet(page).locator('article.detail')
const rows = (page: Page) => sheet(page).getByRole('listbox', { name: 'Releases, newest first' }).getByRole('option')
const langOf = (page: Page, name: 'EN' | 'DE') => sheet(page).getByRole('radio', { name, exact: true })
const readingOf = (page: Page, name: 'Highlights' | 'Details') => sheet(page).getByRole('radio', { name, exact: true })

function withTickets() {
  const data = fixtures()
  const at = '2026-09-29T06:00:00Z'
  for (const key of Object.keys(NOTES)) {
    data.nodes.push({ id: `n-${key}`, key, kind_slug: 'ticket', title: NOTES[key]!.pill_en, body: '', state: 'done', project: 'p-aeon', fields: { priority: 'high' }, parent_id: 'p-aeon', created_at: at, updated_at: at })
  }
  return data
}

async function open(page: Page, version = V105, query = '', locale?: string) {
  await mockWork(page, withTickets())
  await mockReleases(page, historicHistory())
  if (locale) {
    const profile = { principal_id: '11111111-1111-4111-8111-111111111111', email: 'markus@barta.com', first_name: 'Markus', last_name: 'Barta', preferred_name: '', short_name: 'mba', initials: 'MB', timezone: 'Europe/Vienna', locale, greeting_enabled: false, avatar_color: 'teal', avatar_hashes: {}, week_start: 1, revision: 1 }
    await page.route('**/api/me/profile', route => route.fulfill({ json: profile }))
  }
  await page.goto(`/releases/${version}${query}`)
  await expect(sheet(page)).toBeVisible()
  await expect(detail(page).getByRole('heading', { level: 2 })).toBeVisible()
}

const noHorizontalScroll = (page: Page) => page.evaluate(() => {
  const wide = (el: Element | null) => !!el && el.scrollWidth > el.clientWidth + 1
  return wide(document.querySelector('dialog.releases')) || wide(document.documentElement)
})

test('EN and DE, Highlights and Details, on the list, the release and a comparison, round-trip in the URL', async ({ page }) => {
  await page.setViewportSize({ width: 1600, height: 1000 })
  await page.emulateMedia({ colorScheme: 'light', reducedMotion: 'reduce' })
  await open(page, V105)
  const benefit = 'Sessions whose agent is gone end on their own, and the session menu offers only what works.'
  const german = 'Sitzungen ohne Agent enden von selbst, und das Sitzungsmenü bietet nur, was funktioniert.'
  await expect(langOf(page, 'EN')).toHaveAttribute('aria-checked', 'true')
  await expect(readingOf(page, 'Highlights')).toHaveAttribute('aria-checked', 'true')
  await expect(detail(page).getByRole('article', { name: 'Dead sessions tidy up' }).locator('.benefit')).toHaveText(benefit)
  await expect(detail(page).locator('summary')).toHaveCount(0)
  await expect(rows(page).first().locator('.headline')).toContainText('Dead sessions tidy up')
  await expect(rows(page).first().locator('.subjects')).toHaveCount(0)

  await langOf(page, 'DE').click()
  await expect(page).toHaveURL(new RegExp(`/releases/${V105.replaceAll('.', '\\.')}\\?lang=de$`))
  await expect(langOf(page, 'EN')).toHaveText('EN')
  await expect(langOf(page, 'DE')).toHaveText('DE')
  await expect(readingOf(page, 'Highlights')).toHaveText('Highlights')
  await expect(readingOf(page, 'Details')).toHaveText('Details')
  await expect(detail(page).getByRole('article', { name: 'Tote Sitzungen räumen auf' }).locator('.benefit')).toHaveText(german)
  await expect(detail(page).locator('.benefit').first()).toHaveAttribute('lang', 'de')
  await expect(rows(page).first().locator('.headline')).toContainText('Tote Sitzungen räumen auf')

  await readingOf(page, 'Details').click()
  await expect(page).toHaveURL(/lang=de/)
  await expect(page).toHaveURL(/reading=details/)
  await expect(detail(page).getByRole('article', { name: 'Tote Sitzungen räumen auf' }).locator('.benefit')).toHaveCount(0)
  await expect(detail(page).getByRole('article', { name: 'Tote Sitzungen räumen auf' }).locator('summary')).toHaveText('10 commits')
  await expect(rows(page).first().locator('.subjects')).toContainText('Reserve stable105')
  await expect(rows(page).first().locator('.headline')).toContainText('Tote Sitzungen räumen auf')

  await page.reload()
  await expect(detail(page).getByRole('article', { name: 'Tote Sitzungen räumen auf' }).locator('summary')).toHaveText('10 commits')
  await expect(page).toHaveURL(/lang=de/)
  await expect(page).toHaveURL(/reading=details/)

  if (await sheet(page).getByRole('button', { name: 'All releases' }).isVisible()) {
    await sheet(page).getByRole('button', { name: 'All releases' }).click()
  }
  await rows(page).nth(1).click()
  await expect(page).toHaveURL(new RegExp(`/releases/${V102.replaceAll('.', '\\.')}\\?`))
  await expect(page).toHaveURL(/lang=de/)
  await expect(page).toHaveURL(/reading=details/)
  await expect(detail(page).getByRole('article', { name: 'Deploy-Ziel im Blick' }).locator('summary')).toHaveText('4 commits')
  await expect(detail(page).getByText('Notizen nach dem Release geschrieben')).toBeVisible()

  await langOf(page, 'EN').click()
  await readingOf(page, 'Highlights').click()
  await expect(detail(page).getByRole('article', { name: 'Deploy target on screen' }).locator('.benefit')).toContainText('nothing is approved blind')
  await expect(detail(page).locator('summary')).toHaveCount(0)
  await expect(page).toHaveURL(/lang=en/)
  await expect(page).toHaveURL(/reading=highlights/)

  await sheet(page).getByRole('button', { name: 'Compare', exact: true }).click()
  await expect(page).toHaveURL(new RegExp(`/releases/${V102.replaceAll('.', '\\.')}`))
  await expect(page).toHaveURL(/lang=en/)
  await expect(page).toHaveURL(/reading=highlights/)
  await rows(page).first().click()
  const compare = sheet(page).locator('section.compare')
  await expect(compare.getByText(benefit)).toBeVisible()
  await expect(compare.locator('summary')).toHaveCount(0)
  await readingOf(page, 'Details').click()
  await expect(compare.locator('summary').first()).toBeVisible()
  await expect(compare.getByText(benefit)).toHaveCount(0)
  await expect(rows(page).first().locator('.subjects')).toContainText('Reserve stable105')
  await expect(compare.locator('.inc-headline').first()).toContainText('Dead sessions tidy up')
  await langOf(page, 'DE').click()
  await expect(compare.getByRole('article', { name: 'Tote Sitzungen räumen auf' })).toBeVisible()
  await expect(page).toHaveURL(/lang=de/)
  await expect(page).toHaveURL(/reading=details/)
  await page.reload()
  await expect(langOf(page, 'DE')).toHaveAttribute('aria-checked', 'true')
  await expect(readingOf(page, 'Details')).toHaveAttribute('aria-checked', 'true')
})

test('a German profile stays German until EN is chosen, and arrow keys move the switch', async ({ page }) => {
  await page.setViewportSize({ width: 1600, height: 1000 })
  await open(page, V102, '', 'de-AT')
  await expect(detail(page).getByRole('article', { name: 'Lesbarer Risiko-Chip' })).toBeVisible()
  await expect(page).not.toHaveURL(/lang=/)
  await page.goto(`/releases/${V102}?lang=en`)
  await expect(detail(page).getByRole('article', { name: 'Readable risk chip' }).locator('.benefit')).toBeVisible()
  await langOf(page, 'EN').focus()
  await page.keyboard.press('ArrowRight')
  await expect(langOf(page, 'DE')).toHaveAttribute('aria-checked', 'true')
  await expect(page).toHaveURL(/lang=de/)
  await expect(langOf(page, 'DE')).toBeFocused()
})

for (const width of [1600, 390]) {
  for (const colorScheme of ['light', 'dark'] as const) {
    test(`switches fit ${width} ${colorScheme}`, async ({ page }) => {
      await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
      await page.emulateMedia({ colorScheme, reducedMotion: 'reduce' })
      await open(page, V105)
      await expect(readingOf(page, 'Highlights')).toBeVisible()
      await expect(langOf(page, 'DE')).toBeVisible()
      expect(await noHorizontalScroll(page)).toBe(false)
      if (width === 390) {
        await sheet(page).getByRole('button', { name: 'All releases' }).click()
        await expect(rows(page).first()).toBeVisible()
        expect(await noHorizontalScroll(page)).toBe(false)
        await rows(page).first().click()
      }
      await readingOf(page, 'Details').click()
      await langOf(page, 'DE').click()
      expect(await noHorizontalScroll(page)).toBe(false)
      await sheet(page).getByRole('button', { name: 'Compare', exact: true }).click()
      if (width === 390) await rows(page).nth(1).click()
      else await rows(page).nth(1).click()
      await expect(sheet(page).locator('section.compare')).toBeVisible()
      expect(await noHorizontalScroll(page)).toBe(false)
    })
  }
}

test('screenshots of the switches on 102 and 105', async ({ page }) => {
  test.skip(!SHOTS, 'set AEON_323_SHOTS to a directory')
  mkdirSync(SHOTS!, { recursive: true })
  for (const width of [1600, 390]) {
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
    for (const colorScheme of ['light', 'dark'] as const) {
      await page.emulateMedia({ colorScheme, reducedMotion: 'reduce' })
      await open(page, V105)
      const list = async () => {
        if (width === 390 && await sheet(page).getByRole('button', { name: 'All releases' }).isVisible()) {
          await sheet(page).getByRole('button', { name: 'All releases' }).click()
        }
      }
      const shot = async (name: string) => {
        await page.screenshot({ path: `${SHOTS}/${name}-${width}-${colorScheme}.png` })
      }
      await list()
      await shot('list-highlights-en')
      if (width === 390) await rows(page).first().click()
      await shot('detail-highlights-en')
      await langOf(page, 'DE').click()
      await expect(detail(page).getByRole('article', { name: 'Tote Sitzungen räumen auf' })).toBeVisible()
      await shot('detail-highlights-de')
      await readingOf(page, 'Details').click()
      await expect(detail(page).locator('summary').first()).toBeVisible()
      await shot('detail-details-de')
      await list()
      await shot('list-details-de')
      if (width === 390) await rows(page).first().click()
      await sheet(page).getByRole('button', { name: 'Compare', exact: true }).click()
      await list()
      await rows(page).nth(1).click()
      await shot('compare-details-de')
    }
  }
})
