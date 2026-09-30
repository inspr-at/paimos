// SPDX-License-Identifier: AGPL-3.0-only
// AEON-308: the project flow pill lives in the footer centre and stays put while
// the page scrolls. It is absent off a project route. The release name (AEON-431, the footer's far left) stays put and stays readable.
import { mkdirSync } from 'node:fs'
import { expect, test, type Locator } from '@playwright/test'
import { fixtures, mockWork } from './work-fixtures'
import { journeyWorld, mockJourney } from './journey-fixtures'

const OUT = '/private/tmp/claude-501/-Users-markus-Code-aeon/a4527da9-f872-45f5-a2f2-48dde0ce2ce5/scratchpad/shots/aeon-308/iter2'

function overlaps(a: { x: number; y: number; width: number; height: number }, b: { x: number; y: number; width: number; height: number }) {
  const x = Math.min(a.x + a.width, b.x + b.width) - Math.max(a.x, b.x)
  const y = Math.min(a.y + a.height, b.y + b.height) - Math.max(a.y, b.y)
  return x > 1 && y > 1
}

async function box(locator: Locator) {
  const value = await locator.boundingBox()
  expect(value, await locator.evaluate(el => el.className).catch(() => 'missing')).toBeTruthy()
  return value!
}

test('the flow pill stays centred in the footer on project routes only', async ({ page }) => {
  mkdirSync(OUT, { recursive: true })
  await mockWork(page, fixtures())
  await mockJourney(page, journeyWorld('plan'))
  const chip = () => page.locator('footer.app-footer .journey-chip')
  const name = () => page.locator('footer.app-footer .footer-name')

  await page.setViewportSize({ width: 1600, height: 900 })
  await page.goto('/p/PHAROS')
  await expect(chip()).toBeVisible()
  await expect(page.locator('.project-head .journey-chip')).toHaveCount(0)
  await chip().click()
  await expect(page).toHaveURL('/p/PHAROS/journey')
  await expect(chip()).toBeVisible()

  await page.goto('/')
  await expect(page.getByRole('heading', { name: 'Projects', level: 1 })).toBeVisible()
  await expect(page.locator('.journey-chip')).toHaveCount(0)
  await page.goto('/settings/personal')
  await expect(page.locator('footer.app-footer')).toBeVisible()
  await expect(page.locator('.journey-chip')).toHaveCount(0)

  for (const scheme of ['light', 'dark'] as const) {
    await page.emulateMedia({ colorScheme: scheme })
    for (const width of [375, 390, 430, 1600]) {
      const height = width >= 1000 ? 900 : 812
      await page.setViewportSize({ width, height })
      await page.goto('/p/PHAROS')
      await expect(page.getByRole('heading', { name: 'Pharos', exact: true })).toBeVisible()
      await expect(chip()).toBeVisible()
      await expect(name()).toBeVisible()
      // Whatever the pill needs, the release name stays: only the product name steps aside.
      await expect(name().locator('.footer-codename')).toBeVisible()
      const chipBox = await box(chip())
      const nameBox = await box(name())
      const mainBox = await box(page.locator('#main'))
      expect(overlaps(chipBox, nameBox), `${width} ${scheme}: pill covers the release name`).toBe(false)
      expect(chipBox.y, `${width} ${scheme}: pill overlaps the page`).toBeGreaterThanOrEqual(mainBox.y + mainBox.height - 1)
      expect(nameBox.x, `${width} ${scheme}: the name stays on the left`).toBeLessThan(chipBox.x + 1)
      expect(nameBox.x, `${width} ${scheme}: the name is at the far left`).toBeLessThan(40)
      if (width <= 430) expect(chipBox.height, `${width} ${scheme}: 44px target`).toBeGreaterThanOrEqual(44)
      if (width >= 1000) expect(Math.abs(chipBox.x + chipBox.width / 2 - width / 2), `${width} ${scheme}: page centre`).toBeLessThan(16)
      await page.screenshot({ path: `${OUT}/project-${width}-${scheme}.png` })

      await page.locator('.project-page').evaluate(el => { (el as HTMLElement).style.minHeight = '2400px' })
      await page.locator('#main').evaluate(el => { el.scrollTop = 900 })
      await expect.poll(() => page.locator('#main').evaluate(el => el.scrollTop)).toBeGreaterThan(200)
      await expect(chip()).toBeInViewport()
      await expect(page.locator('.app-shell')).not.toHaveClass(/footer-hidden/)
      const scrolled = await box(chip())
      expect(scrolled.y).toBeGreaterThan(height * 0.7)
      expect(overlaps(scrolled, await box(name()))).toBe(false)
      await page.screenshot({ path: `${OUT}/project-${width}-${scheme}-scrolled.png` })
    }
    await page.setViewportSize({ width: scheme === 'light' ? 390 : 1600, height: 812 })
    await page.goto('/')
    await expect(page.locator('.journey-chip')).toHaveCount(0)
    await page.screenshot({ path: `${OUT}/home-${scheme === 'light' ? 390 : 1600}-${scheme}.png` })
  }
})

test('a project with no next step leaves the footer without a flow pill', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 812 })
  await mockWork(page, fixtures())
  await mockJourney(page, journeyWorld('inspire', { noIntake: true }))
  await page.goto('/p/PHAROS')
  await expect(page.locator('tr.ticket-row').first()).toBeVisible()
  await page.waitForTimeout(300)
  await expect(page.locator('.journey-chip')).toHaveCount(0)
  await expect(page.locator('footer.app-footer .footer-name')).toBeVisible()
})
