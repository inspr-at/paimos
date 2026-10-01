// SPDX-License-Identifier: AGPL-3.0-only
// AEON-515: the release stays at the right, beside the opt-in project flow.
import { mkdirSync } from 'node:fs'
import { join } from 'node:path'
import { expect, test, type Locator, type Page } from '@playwright/test'
import { fixtures, mockWork } from './work-fixtures'
import { mockReleases, presentedHistory } from './releases-fixtures'
import { journeyWorld, mockJourney } from './journey-fixtures'

const screenshots = process.env.AEON_SCREENSHOTS_DIR ?? 'test-results/footer-release-pill'
const pill = (page: Page) => page.locator('footer.app-footer .version-pill')
const version = (page: Page) => pill(page).locator('.calendar-version')

async function setup(page: Page, options: { flow?: boolean; fresh?: boolean; name?: string } = {}) {
  const history = presentedHistory()
  const data = fixtures()
  if (options.flow) data.preferences['developer-ui'] = { show_flow_controls: true }
  if (options.fresh) data.preferences.releases = { last_seen: history.releases[4].version }
  await mockWork(page, data)
  await mockJourney(page, journeyWorld('plan'), { flowControls: options.flow ?? false })
  await mockReleases(page, history, { codename: options.name ?? 'Lucky Lune' })
  return history
}

async function bounds(locator: Locator) {
  const rect = await locator.boundingBox()
  expect(rect).toBeTruthy()
  return rect!
}

async function insideFooter(page: Page) {
  const footer = await bounds(page.locator('footer.app-footer'))
  const right = await bounds(pill(page))
  expect(right.x + right.width).toBeLessThanOrEqual(footer.x + footer.width)
  expect(right.x + right.width).toBeGreaterThan(footer.x + footer.width - 40)
  const mark = await bounds(page.locator('footer.app-footer .footer-name'))
  expect(mark.x + mark.width).toBeLessThanOrEqual(right.x)
  expect(await page.evaluate(() => document.documentElement.scrollWidth - innerWidth)).toBe(0)
}

for (const colorScheme of ['light', 'dark'] as const) {
  for (const width of [1440, 390]) {
    test(`release pill and product mark at ${width} in ${colorScheme}`, async ({ page }) => {
      await page.setViewportSize({ width, height: width < 600 ? 844 : 900 })
      await page.emulateMedia({ colorScheme })
      const history = await setup(page, { fresh: true })
      await page.goto('/')
      await expect(page.getByRole('list', { name: 'Projects' })).toBeVisible()
      await expect(page.locator('footer.app-footer .footer-name')).toHaveText('PAIMOS AEON')
      await expect(pill(page).locator('.footer-codename')).toHaveText('Lucky Lune')
      await expect(version(page)).toHaveAttribute('data-version-view', 'pretty')
      await expect(version(page)).toContainText(/^\d\d·\d\d·\d\d \d\d:\d\d/)
      await expect(pill(page).locator('.new-badge')).toHaveText('3 new')
      await expect(pill(page)).toHaveAccessibleName(`Release history, Lucky Lune, version ${history.current}, 3 new since your last visit`)
      expect(await pill(page).locator('.pill-face').evaluate(el => [...el.children].map(child => child.tagName === 'svg' ? 'history' : child.className.split(' ')[0]))).toEqual(['history', 'footer-codename', 'calendar-version', 'new-badge'])
      await insideFooter(page)
      await page.evaluate(() => document.fonts.ready)
      mkdirSync(screenshots, { recursive: true })
      await page.screenshot({ path: join(screenshots, `${width < 600 ? 'phone' : 'desktop'}-${colorScheme}.png`) })
      await pill(page).hover()
      await expect(version(page)).toHaveAttribute('data-version-view', 'revealed')
      await expect(version(page).locator('.ss')).toHaveCSS('transition', 'none')
      await expect(pill(page).locator('.footer-codename')).toBeVisible()
      await insideFooter(page)
      await page.mouse.move(1, 1)
      await expect(version(page)).toHaveAttribute('data-version-view', 'pretty')
      await pill(page).focus()
      await expect(version(page)).toHaveAttribute('data-version-view', 'revealed')
      await page.keyboard.press('Enter')
      await expect(page.getByRole('dialog', { name: 'PAIMOS AEON releases' })).toBeVisible()
    })
  }
}

test('opt-in flow avoids the release pill, including during the version reveal', async ({ page }) => {
  await setup(page, { flow: true, fresh: true })
  for (const width of [1440, 390, 320]) {
    await page.setViewportSize({ width, height: 900 })
    await page.goto('/p/PHAROS')
    const chip = page.locator('footer.app-footer .journey-chip')
    await expect(chip).toBeVisible()
    for (const reveal of [false, true]) {
      if (reveal) await pill(page).hover()
      await expect(version(page)).toHaveAttribute('data-version-view', reveal ? 'revealed' : 'pretty')
      await expect.poll(async () => {
        const flow = await bounds(chip)
        return flow.x + flow.width - (await bounds(pill(page))).x
      }).toBeLessThanOrEqual(0)
      await insideFooter(page)
    }
    await chip.click()
    await expect(page).toHaveURL('/p/PHAROS/journey')
    await page.goto('/')
    await expect(page.locator('footer.app-footer .flow-slot')).toHaveCount(0)
    await expect(page.locator('footer.app-footer .footer-name')).toBeVisible()
  }
})

test('flow is absent by default and a long codename cannot push the version off a phone', async ({ page }) => {
  await setup(page, { name: 'An exceptionally long release codename for a narrow screen' })
  await page.setViewportSize({ width: 320, height: 844 })
  await page.goto('/p/PHAROS')
  await expect(pill(page)).toBeVisible()
  await expect(page.locator('footer.app-footer .flow-slot')).toHaveCount(0)
  await expect(pill(page).locator('.new-badge')).toHaveCount(0)
  await insideFooter(page)
  await pill(page).hover()
  await expect(version(page)).toHaveAttribute('data-version-view', 'revealed')
  await insideFooter(page)
})

test('unknown count says New; failed versions still open history', async ({ page }) => {
  await setup(page, { fresh: true })
  await page.route('**/api/releases**', route => route.fulfill({ status: 503, json: { error: 'Unavailable' } }))
  await page.goto('/')
  await expect(pill(page).locator('.new-badge')).toHaveText('New')
  await expect(pill(page)).toHaveAccessibleName(/new releases since your last visit$/)
  await page.route('**/api/version', route => route.fulfill({ status: 503 }))
  await page.reload()
  await expect(pill(page).locator('.fallback')).toHaveText('Version unavailable')
  await pill(page).click()
  await expect(page.getByRole('dialog', { name: 'PAIMOS AEON releases' })).toBeVisible()
})

test('motion uses the shared reveal duration', async ({ page }) => {
  await setup(page)
  await page.emulateMedia({ reducedMotion: 'no-preference' })
  await page.goto('/')
  await expect(version(page)).toHaveAttribute('data-version-view', 'pretty')
  await pill(page).hover()
  await expect(version(page)).toHaveAttribute('data-version-view', 'revealed')
  await expect(version(page).locator('.ss')).toHaveCSS('transition-duration', '1s, 1s')
})
